package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/quota"
)

// 额度冻结评估器（quota_guard.go）的用例。
//
// 判定规则的错误都不会报错、只表现为「Key 该冻没冻」或「不该冻却冻了」，因此这里按
// 设计稿第 2 节逐条钉住：订阅条目、实例级联动、无意见、清理、L3 单/多账号分流。
//
// 全部用确定性时钟：冻结到期、库存抑制窗口、信号去抖都跨时间，真实时钟会让用例
// 变成 sleep 比赛。

// —— 夹具 ——

// guardClock 是可推进的假时钟。
type guardClock struct {
	mu sync.Mutex
	at time.Time
}

func newGuardClock(at time.Time) *guardClock { return &guardClock{at: at} }

func (c *guardClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *guardClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(delta)
}

// quietLogger 丢弃日志：这些用例断言的是状态，不是日志行。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// quotaGuardFixture 是一份「Server + 冻结表 + 评估器」的最小装配，配置来自临时文件。
//
// cfg 通过 setConfig 可变：模拟热重载后换了供应商清单，而冻结表与评估器不重建。
type quotaGuardFixture struct {
	Server *Server
	Store  *quota.Store
	Guard  *QuotaGuard
	Clock  *guardClock
	Path   string

	mu  sync.Mutex
	cfg *config.RouterConfig
}

func (f *quotaGuardFixture) currentConfig() *config.RouterConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

func (f *quotaGuardFixture) setConfig(cfg *config.RouterConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
}

// writeQuotaConfig 写一份最小 v4 配置；extra 会原样插在 webui_enabled 之后
// （给需要覆盖 quota_freeze_interval_seconds 的用例用）。
func writeQuotaConfig(t *testing.T, path, providersJSON, instancesJSON, extra string) string {
	t.Helper()
	fixture := `{"config_version":4,"local_api_key":"local-key","ops_enabled":false,` +
		`"webui_enabled":false,` + extra +
		`"cpa_instances":` + instancesJSON + `,"providers":` + providersJSON + `}`
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return path
}

// newQuotaGuardFixture 装配夹具；extra 见 writeQuotaConfig。
func newQuotaGuardFixture(t *testing.T, providersJSON, instancesJSON, extra string) *quotaGuardFixture {
	t.Helper()
	path := writeQuotaConfig(t, filepath.Join(t.TempDir(), "router-config.json"),
		providersJSON, instancesJSON, extra)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	clock := newGuardClock(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	store := quota.NewStoreWithClock("", clock.Now)
	fixture := &quotaGuardFixture{
		Server: &Server{ConfigPath: path},
		Store:  store,
		Clock:  clock,
		Path:   path,
		cfg:    loaded,
	}
	fixture.Guard = NewQuotaGuard(QuotaGuardOptions{
		Server: fixture.Server,
		Store:  store,
		Config: func() *config.RouterConfig { return fixture.currentConfig() },
		Logger: quietLogger(),
		Now:    clock.Now,
	})
	fixture.Server.QuotaFreezes = store
	fixture.Server.QuotaGuard = fixture.Guard
	return fixture
}

// reloadProviders 模拟热重载：替换运行时配置快照并重写磁盘文件（Evaluate 走文件）。
func (f *quotaGuardFixture) reloadProviders(t *testing.T, providersJSON string) {
	t.Helper()
	writeQuotaConfig(t, f.Path, providersJSON, `{}`, "")
	loaded, err := config.Load(f.Path)
	if err != nil {
		t.Fatalf("载入新配置失败: %v", err)
	}
	f.setConfig(loaded)
}

// quotaWindow 造一个账号资源窗口读数。
func quotaWindow(key, label string, remaining float64, status, resetAt string) cpaQuotaWindow {
	return cpaQuotaWindow{Key: key, Label: label, Window: key, Remaining: remaining, Status: status, ResetAt: resetAt}
}

// exhaustedWindow 是一个硬性耗尽窗口（剩余 0 且上游给了拒绝状态）。
func exhaustedWindow() cpaQuotaWindow {
	return quotaWindow("claude/5h", "5 小时", 0, "rejected", "")
}

// healthyWindow 是一个仍有额度的窗口。
func healthyWindow() cpaQuotaWindow {
	return quotaWindow("claude/5h", "5 小时", 0.5, "allowed", "")
}

// subscriptionReport 造一份只有一条订阅读数的采集结果。
func subscriptionReport(ok bool, windows ...cpaQuotaWindow) cpaAccountsReport {
	return cpaAccountsReport{Subscriptions: []subscriptionEntry{{
		ID:         "go-a/key-1",
		Kind:       "opencode-go",
		Vendor:     "OpenCode Go",
		ProviderID: "go-a",
		KeyName:    "key-1",
		OK:         ok,
		Windows:    windows,
	}}}
}

// instanceReport 造一份实例的账号读数。
func instanceReport(ok bool, accounts ...cpaAccount) cpaAccountsReport {
	return cpaAccountsReport{Instances: []cpaInstanceAccountReport{{
		ID:       "jp",
		Label:    "日本 CPA",
		BaseURL:  "http://127.0.0.1:8317",
		OK:       ok,
		Accounts: accounts,
	}}}
}

// drainSignal 取出一个已投递的 L3 信号并交给 handleSignal（等价于后台循环那一步）。
//
// 不用 Run 循环是为了确定性：Run 里的 select 顺序、定时器与去抖都会引入等待，
// 而这些用例要钉住的是「信号到了之后判定对不对」。
func drainSignal(t *testing.T, guard *QuotaGuard) {
	t.Helper()
	select {
	case signal := <-guard.signals:
		guard.handleSignal(signal)
	default:
		t.Fatal("期望投递一个 L3 信号，实际没有")
	}
}

// —— 订阅条目 ——

// TestQuotaGuardSubscriptionRules 钉住订阅条目的三条判定：耗尽冻结、健康解冻、读不到不动。
func TestQuotaGuardSubscriptionRules(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	evaluation := fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow()))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("订阅条目硬性耗尽后应冻结对应 Key")
	}
	if evaluation.Changed != 1 || evaluation.Frozen != 1 {
		t.Errorf("期望 1 条变化、1 条生效，实际 %+v", evaluation)
	}
	freeze, found := fixture.Store.Get("go-a", "key-1")
	if !found {
		t.Fatal("冻结记录应可读回")
	}
	if freeze.Scope != "subscription" || freeze.Source != "account_resource" {
		t.Errorf("订阅冻结的 scope/source 不对: %+v", freeze)
	}
	if freeze.Vendor != "OpenCode Go" || freeze.WindowKey != "claude/5h" || freeze.WindowLabel != "5 小时" {
		t.Errorf("冻结记录缺少展示字段: %+v", freeze)
	}
	if freeze.Reason == "" {
		t.Error("冻结记录必须带一句人话原因")
	}
	// 没有 reset_at 时按兜底 TTL（10 分钟）冻结，不能永久冻住。
	if want := fixture.Clock.Now().Add(10 * time.Minute); !freeze.ExpiresAt.Equal(want) {
		t.Errorf("到期时间应为 now+10min（%v），实际 %v", want, freeze.ExpiresAt)
	}

	// 下一次成功观测里没有硬性耗尽窗口 → 解冻。
	evaluation = fixture.Guard.ApplyReport(subscriptionReport(true, healthyWindow()))
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("观测到有余额后应解冻")
	}
	if evaluation.Changed != 1 {
		t.Errorf("解冻应计为一次变化，实际 %+v", evaluation)
	}

	// 读取失败（ok=false）：无意见，既不冻结也不解冻。
	fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow()))
	fixture.Guard.ApplyReport(subscriptionReport(false, exhaustedWindow()))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("读取失败不该把已有冻结解掉")
	}
	// 反向同理：先解冻，再拿失败读数来，不能凭空冻一把。
	fixture.Guard.ApplyReport(subscriptionReport(true, healthyWindow()))
	fixture.Guard.ApplyReport(subscriptionReport(false, exhaustedWindow()))
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("读取失败不该冻结 Key")
	}
}

// TestQuotaGuardSubscriptionResetAndWarning 钉住 reset_at 的到期计算与预警状态不冻结。
func TestQuotaGuardSubscriptionResetAndWarning(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	// 预警（allowed_warning）不是拒绝：即使剩余不多也不冻结。
	fixture.Guard.ApplyReport(subscriptionReport(true, quotaWindow("claude/5h", "5 小时", 0.2, "allowed_warning", "")))
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("预警状态不应触发冻结")
	}

	// reset_at 是 Unix 秒：内置计时器的目标**精确**停在 reset_at，不再加宽限。
	resetAt := fixture.Clock.Now().Add(2 * time.Hour)
	window := exhaustedWindow()
	window.ResetAt = formatFreezeTime(resetAt)
	fixture.Guard.ApplyReport(subscriptionReport(true, window))
	freeze, found := fixture.Store.Get("go-a", "key-1")
	if !found {
		t.Fatal("应冻结")
	}
	if !freeze.ResetAt.Equal(resetAt) {
		t.Errorf("reset_at 应解析为 %v，实际 %v", resetAt, freeze.ResetAt)
	}
	if !freeze.ExpiresAt.Equal(resetAt) {
		t.Errorf("到期时间应精确等于 reset_at（%v），实际 %v", resetAt, freeze.ExpiresAt)
	}
	if freeze.Trigger != "hard" || freeze.Threshold != 0 {
		t.Errorf("硬性耗尽应记 trigger=hard、threshold=0: %+v", freeze)
	}

	// 跨过到期时间：冻结自动失效，清理把它从表里删掉。
	fixture.Clock.Advance(3 * time.Hour)
	if got := fixture.Store.Snapshot(); len(got) != 0 {
		t.Fatalf("到期后不该再出现在快照里: %+v", got)
	}
	fixture.Guard.ApplyReport(cpaAccountsReport{})
	if len(fixture.Store.Snapshot()) != 0 {
		t.Fatal("清理之后仍应有 0 条冻结")
	}
}

// TestQuotaGuardSuppressionSurvivesEvaluation 钉住手工解冻的抑制窗口：
// 窗口内即使读数仍显示耗尽也不能立刻冻回去（否则「解除冻结」按钮等于没用）。
func TestQuotaGuardSuppressionSurvivesEvaluation(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow()))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("应冻结")
	}
	suppressUntil := fixture.Clock.Now().Add(10 * time.Minute)
	if _, found, err := fixture.Store.Release("go-a", "key-1", suppressUntil); err != nil || !found {
		t.Fatalf("手工解冻失败: found=%v err=%v", found, err)
	}

	evaluation := fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow()))
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("抑制窗口内不得重新冻结")
	}
	if evaluation.Changed != 0 {
		t.Errorf("抑制期内不应产生变化，实际 %+v", evaluation)
	}

	// 窗口过期后，仍耗尽的读数会重新冻结。
	fixture.Clock.Advance(11 * time.Minute)
	fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow()))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("抑制窗口过期后应重新冻结")
	}
}

// —— CPA 实例级联动 ——

// TestQuotaGuardInstanceRules 钉住实例级判定：全部启用账号耗尽才冻、部分恢复即解、地址不同不联动。
func TestQuotaGuardInstanceRules(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{
			"cpa-claude": {"base_url": "http://127.0.0.1:8317", "keys": {
				"claude-a": {"api_key": "sk-a"},
				"claude-b": {"api_key": "sk-b", "enabled": false}
			}},
			"unrelated": {"base_url": "https://api.example.test", "keys": {"k-1": {"api_key": "sk-x"}}}
		}`,
		`{"jp":{"label":"日本 CPA","base_url":"http://127.0.0.1:8317","management_key":"mk-1"}}`, "")

	resetAt := fixture.Clock.Now().Add(time.Hour)
	hard := exhaustedWindow()
	hard.ResetAt = formatFreezeTime(resetAt)
	report := instanceReport(true,
		cpaAccount{Name: "a1.json", Email: "ops@example.com", Windows: []cpaQuotaWindow{hard}},
		cpaAccount{Name: "a2.json", Windows: []cpaQuotaWindow{quotaWindow("claude/7d", "7 天", 0, "exceeded", "")}},
	)
	evaluation := fixture.Guard.ApplyReport(report)

	if !fixture.Store.IsFrozen("cpa-claude", "claude-a") {
		t.Fatal("实例内全部启用账号耗尽时应冻结关联供应商的启用 Key")
	}
	if fixture.Store.IsFrozen("cpa-claude", "claude-b") {
		t.Fatal("人工停用的 Key 不该被冻结（它本来就不参与选路）")
	}
	if fixture.Store.IsFrozen("unrelated", "k-1") {
		t.Fatal("base_url 不指向该实例的供应商不该被牵连")
	}
	freeze, found := fixture.Store.Get("cpa-claude", "claude-a")
	if !found {
		t.Fatal("冻结记录应可读回")
	}
	if freeze.Scope != "cpa_instance" || freeze.Source != "account_resource" || freeze.InstanceID != "jp" {
		t.Errorf("实例级冻结的 scope/source/instance_id 不对: %+v", freeze)
	}
	if freeze.Account == "" {
		t.Error("实例级冻结应记下触发账号")
	}
	// 触发窗口取最晚重置的那个（a1 的 1 小时后），内置计时器精确停在 reset_at。
	if !freeze.ExpiresAt.Equal(resetAt) {
		t.Errorf("到期时间应精确等于最晚 reset_at（%v），实际 %v", resetAt, freeze.ExpiresAt)
	}
	if evaluation.Frozen != 1 {
		t.Errorf("期望 1 条生效冻结，实际 %+v", evaluation)
	}
	if links := fixture.Guard.InstanceLinks(); links["cpa-claude"].InstanceID != "jp" || links["cpa-claude"].EnabledAccounts != 2 {
		t.Errorf("关联表不对: %+v", links)
	}

	// 一个账号恢复额度：实例级冻结解除。
	report = instanceReport(true,
		cpaAccount{Name: "a1.json", Windows: []cpaQuotaWindow{healthyWindow()}},
		cpaAccount{Name: "a2.json", Windows: []cpaQuotaWindow{exhaustedWindow()}},
	)
	fixture.Guard.ApplyReport(report)
	if fixture.Store.IsFrozen("cpa-claude", "claude-a") {
		t.Fatal("实例内至少一个账号恢复后应解冻")
	}
}

// TestQuotaGuardInstanceNoOpinion 钉住实例侧的「无意见」：读不到、没启用账号、
// base_url 不匹配时都不能改变现状（尤其不能把「读不到」当成「有余额」）。
func TestQuotaGuardInstanceNoOpinion(t *testing.T) {
	providers := `{"cpa-claude":{"base_url":"http://127.0.0.1:8317","keys":{"claude-a":{"api_key":"sk-a"}}}}`
	fixture := newQuotaGuardFixture(t, providers,
		`{"jp":{"label":"日本 CPA","base_url":"http://127.0.0.1:8317","management_key":"mk-1"}}`, "")

	// 先制造一条实例级冻结。
	fixture.Guard.ApplyReport(instanceReport(true,
		cpaAccount{Name: "a1.json", Windows: []cpaQuotaWindow{exhaustedWindow()}},
	))
	if !fixture.Store.IsFrozen("cpa-claude", "claude-a") {
		t.Fatal("前置条件：应已冻结")
	}

	// 实例不可达：无意见，保留冻结。
	fixture.Guard.ApplyReport(instanceReport(false,
		cpaAccount{Name: "a1.json", Windows: []cpaQuotaWindow{healthyWindow()}},
	))
	if !fixture.Store.IsFrozen("cpa-claude", "claude-a") {
		t.Fatal("实例读取失败时不该解冻")
	}

	// 实例没有启用账号：无意见。
	fixture.Guard.ApplyReport(instanceReport(true,
		cpaAccount{Name: "a1.json", Disabled: true, Windows: []cpaQuotaWindow{healthyWindow()}},
	))
	if !fixture.Store.IsFrozen("cpa-claude", "claude-a") {
		t.Fatal("实例没有启用账号时不该解冻")
	}

	// 有账号额度读取失败（quota_error）：无意见，保留冻结。
	fixture.Guard.ApplyReport(instanceReport(true,
		cpaAccount{Name: "a1.json", QuotaError: "quota/fetch 501"},
	))
	if !fixture.Store.IsFrozen("cpa-claude", "claude-a") {
		t.Fatal("账号额度未知时不该解冻")
	}

	// base_url 不指向该实例：不联动（既不冻也不解冻）。
	fixture.Guard.ApplyReport(cpaAccountsReport{Instances: []cpaInstanceAccountReport{{
		ID:      "jp",
		Label:   "日本 CPA",
		BaseURL: "http://127.0.0.1:9999",
		OK:      true,
		Accounts: []cpaAccount{
			{Name: "a1.json", Windows: []cpaQuotaWindow{exhaustedWindow()}},
		},
	}}})
	if freeze, found := fixture.Store.Get("cpa-claude", "claude-a"); found && freeze.Scope == "cpa_instance" {
		// 关联关系变了（实例地址对不上）→ 关联失效的实例级冻结应被清掉。
		t.Fatalf("地址不匹配后实例级冻结应被清理: %+v", freeze)
	}
}

// —— 清理 ——

// TestQuotaGuardPrunesMissingAndDisabledKeys 钉住失效清理：配置里删掉/停用的 Key 不留冻结。
func TestQuotaGuardPrunesMissingAndDisabledKeys(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow()))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("前置条件：应已冻结")
	}

	// 供应商被删掉：Key 已不存在，冻结必须清理。
	fixture.reloadProviders(t, `{}`)
	evaluation := fixture.Guard.ApplyReport(cpaAccountsReport{})
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("配置里已不存在的 Key 的冻结应被清理")
	}
	if evaluation.Changed == 0 {
		t.Errorf("清理应计为一次变化，实际 %+v", evaluation)
	}

	// 人工停用：同样清理（设计稿第 2 节的「失效清理」）。
	fixture.reloadProviders(t, `{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`)
	fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow()))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("前置条件：重新冻结")
	}
	fixture.reloadProviders(t, `{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1","enabled":false}}}}`)
	fixture.Guard.ApplyReport(cpaAccountsReport{})
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("已人工停用的 Key 的冻结应被清理")
	}
}

// —— L3 上游响应头 ——

// hardAnthropicHeaders 是一组硬性耗尽的 Anthropic 响应头。
func hardAnthropicHeaders() http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"1"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1800000000"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"rejected"},
	}
}

// TestQuotaGuardObserveUpstreamQuota 钉住 L3 的观察点只投递、不动作，且预警状态不投递。
func TestQuotaGuardObserveUpstreamQuota(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	// 没有额度头：不投递。
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", http.Header{"Content-Type": {"application/json"}})
	select {
	case signal := <-fixture.Guard.signals:
		t.Fatalf("没有额度头时不该投递信号: %+v", signal)
	default:
	}

	// 预警状态：硬性判据不成立，不投递。
	warning := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.4"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed_warning"},
	}
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", warning)
	select {
	case signal := <-fixture.Guard.signals:
		t.Fatalf("预警状态不该投递信号: %+v", signal)
	default:
	}

	// 硬性耗尽：投递一个信号，观察点本身绝不写冻结表。
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hardAnthropicHeaders())
	if fixture.Store.IsFrozen("prov", "key-1") {
		t.Fatal("观察点不得在请求路径上直接冻结")
	}
	drainSignal(t, fixture.Guard)
}

// TestQuotaGuardL3DirectFreeze 钉住「未关联实例 → 直接冻结」与到期计算。
func TestQuotaGuardL3DirectFreeze(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")
	// 先评估一次，让关联表就绪（否则 L3 会保守地要求重新评估）。
	fixture.Guard.ApplyReport(cpaAccountsReport{})

	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hardAnthropicHeaders())
	drainSignal(t, fixture.Guard)

	freeze, found := fixture.Store.Get("prov", "key-1")
	if !found {
		t.Fatal("未关联 CPA 实例的供应商，L3 应直接冻结")
	}
	if freeze.Scope != "upstream" || freeze.Source != "upstream_header" {
		t.Errorf("L3 冻结的 scope/source 不对: %+v", freeze)
	}
	resetAt := time.Unix(1800000000, 0).UTC()
	if !freeze.ResetAt.Equal(resetAt) {
		t.Errorf("reset_at 应解析为 %v，实际 %v", resetAt, freeze.ResetAt)
	}
	if !freeze.ExpiresAt.Equal(resetAt) {
		t.Errorf("到期时间应精确等于 reset_at（%v），实际 %v", resetAt, freeze.ExpiresAt)
	}
	if freeze.Trigger != "hard" {
		t.Errorf("L3 硬性耗尽应记 trigger=hard: %+v", freeze)
	}
}

// TestQuotaGuardL3Debounce 钉住同一把 Key 的重复信号在去抖窗口内只处理一次。
func TestQuotaGuardL3Debounce(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")
	fixture.Guard.ApplyReport(cpaAccountsReport{})

	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hardAnthropicHeaders())
	drainSignal(t, fixture.Guard)
	if !fixture.Store.IsFrozen("prov", "key-1") {
		t.Fatal("前置条件：应已冻结")
	}

	// 手工解冻（不留抑制窗口），紧接着的同一条信号应被去抖挡掉。
	if _, _, err := fixture.Store.Release("prov", "key-1", time.Time{}); err != nil {
		t.Fatalf("解除冻结失败: %v", err)
	}
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hardAnthropicHeaders())
	drainSignal(t, fixture.Guard)
	if fixture.Store.IsFrozen("prov", "key-1") {
		t.Fatal("去抖窗口内的重复信号不该重新冻结")
	}

	// 跨过去抖窗口后，同样的信号应被正常处理。
	fixture.Clock.Advance(quotaSignalDebounce + time.Second)
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hardAnthropicHeaders())
	drainSignal(t, fixture.Guard)
	if !fixture.Store.IsFrozen("prov", "key-1") {
		t.Fatal("跨过去抖窗口后应重新冻结")
	}
}

// TestQuotaGuardL3InstanceRouting 钉住 L3 的单/多账号分流：
// 单账号实例直接冻结、多账号实例只触发重评估、关联未知也先重评估。
func TestQuotaGuardL3InstanceRouting(t *testing.T) {
	t.Run("多账号实例只触发重评估", func(t *testing.T) {
		fixture := newQuotaGuardFixture(t,
			`{"prov":{"base_url":"http://127.0.0.1:8317","keys":{"key-1":{"api_key":"sk-1"}}}}`,
			`{"jp":{"label":"JP","base_url":"http://127.0.0.1:8317","management_key":"mk"}}`, "")
		fixture.Guard.ApplyReport(instanceReport(true,
			cpaAccount{Name: "a1.json", Windows: []cpaQuotaWindow{healthyWindow()}},
			cpaAccount{Name: "a2.json", Windows: []cpaQuotaWindow{healthyWindow()}},
		))

		fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hardAnthropicHeaders())
		drainSignal(t, fixture.Guard)
		if fixture.Store.IsFrozen("prov", "key-1") {
			t.Fatal("多账号实例下 L3 不该直接冻结")
		}
		select {
		case <-fixture.Guard.trigger:
		default:
			t.Fatal("多账号实例下 L3 应触发一次即时重评估")
		}
	})

	t.Run("单账号实例直接冻结", func(t *testing.T) {
		fixture := newQuotaGuardFixture(t,
			`{"prov":{"base_url":"http://127.0.0.1:8317","keys":{"key-1":{"api_key":"sk-1"}}}}`,
			`{"jp":{"label":"JP","base_url":"http://127.0.0.1:8317","management_key":"mk"}}`, "")
		fixture.Guard.ApplyReport(instanceReport(true,
			cpaAccount{Name: "a1.json", Windows: []cpaQuotaWindow{healthyWindow()}},
		))

		fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hardAnthropicHeaders())
		drainSignal(t, fixture.Guard)
		if !fixture.Store.IsFrozen("prov", "key-1") {
			t.Fatal("单账号实例下 L3 应直接冻结")
		}
	})

	t.Run("关联关系尚未评估时先重评估", func(t *testing.T) {
		fixture := newQuotaGuardFixture(t,
			`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
			`{}`, "")

		fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hardAnthropicHeaders())
		drainSignal(t, fixture.Guard)
		if fixture.Store.IsFrozen("prov", "key-1") {
			t.Fatal("关联关系未知时不该冒进直接冻结")
		}
		select {
		case <-fixture.Guard.trigger:
		default:
			t.Fatal("关联关系未知时应触发一次即时重评估")
		}
	})
}

// —— 评估入口与状态 ——

// TestQuotaGuardEvaluate 钉住 Evaluate 的采集装配（读配置 → 扇出 → 应用）与状态读数。
func TestQuotaGuardEvaluate(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	evaluation := fixture.Guard.Evaluate(context.Background())
	if evaluation.Skipped {
		t.Fatalf("机制开启时 Evaluate 不该跳过: %+v", evaluation)
	}
	if evaluation.Frozen != 0 {
		t.Errorf("没有账号资源读数时不该有冻结: %+v", evaluation)
	}

	status := fixture.Guard.Status()
	if !status.Enabled || status.IntervalSeconds != 60 {
		t.Errorf("默认配置下应为开启、间隔 60 秒: %+v", status)
	}
	if status.LastEvaluatedAt == "" {
		t.Error("成功评估后应记录 last_evaluated_at")
	}
	if _, err := time.Parse(time.RFC3339, status.LastEvaluatedAt); err != nil {
		t.Errorf("last_evaluated_at 不是 RFC3339: %q（%v）", status.LastEvaluatedAt, err)
	}
	if status.LastError != "" {
		t.Errorf("成功评估不该留下 last_error: %q", status.LastError)
	}
}

// TestQuotaGuardApplyReportNotBlockedByCollection 钉住锁的职责分离：
// 后台采集可能长达 cpaAccountsBudget（60 秒），它占的是 collectMu；页面即时评估只应
// 等「判定应用」那一小段（evalMu），不能陪一次完整扇出一起等。
//
// 回归方式：直接占住 collectMu 模拟「巡检正在采集」，再在另一个 goroutine 里调
// ApplyReport——锁职责一旦混用（采集也持 evalMu），这个调用会一直阻塞到超时。
func TestQuotaGuardApplyReportNotBlockedByCollection(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	if evaluation := fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow())); evaluation.Changed != 1 {
		t.Fatalf("先制造一条冻结: %+v", evaluation)
	}

	// 模拟后台巡检正在采集：collectMu 被占住。
	fixture.Guard.collectMu.Lock()
	defer fixture.Guard.collectMu.Unlock()

	done := make(chan QuotaEvaluation, 1)
	go func() { done <- fixture.Guard.ApplyReport(subscriptionReport(true, healthyWindow())) }()
	select {
	case evaluation := <-done:
		if evaluation.Skipped || evaluation.Changed != 1 {
			t.Fatalf("页面即时评估应在采集中途照常应用判定: %+v", evaluation)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("页面即时评估被后台采集阻塞：采集锁与判定锁职责混用")
	}
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Error("健康读数应解冻，说明这次 ApplyReport 真的落到了判定")
	}

	// Evaluate 在采集进行中必须单飞返回上一份结果，而不是排队等 60 秒再扇出一次。
	started := time.Now()
	evaluation := fixture.Guard.Evaluate(context.Background())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("采集中途的 Evaluate 不该排队等待，实际等了 %v", elapsed)
	}
	if evaluation.Frozen != 0 {
		t.Errorf("单飞返回的应是上一份结果: %+v", evaluation)
	}
}

// TestQuotaGuardDisabledByInterval 钉住「0 = 整套机制关闭」：后台不评估、页面不进评估、
// L3 不落地，但状态读数要如实报告 enabled=false。
func TestQuotaGuardDisabledByInterval(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, `"quota_freeze_interval_seconds":0,`)

	if evaluation := fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow())); !evaluation.Skipped {
		t.Fatalf("机制关闭时 ApplyReport 应跳过，实际 %+v", evaluation)
	}
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("机制关闭时不得冻结")
	}
	fixture.Guard.ObserveUpstreamQuota("go-a", "key-1", hardAnthropicHeaders())
	drainSignal(t, fixture.Guard)
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("机制关闭时 L3 信号不得落地")
	}
	status := fixture.Guard.Status()
	if status.Enabled || status.IntervalSeconds != 0 {
		t.Errorf("状态应报告机制关闭: %+v", status)
	}
}

// TestQuotaGuardNilSafety 钉住「未装配/字段缺省」时全部路径安全（嵌入场景）。
func TestQuotaGuardNilSafety(t *testing.T) {
	var nilGuard *QuotaGuard
	nilGuard.Trigger()
	nilGuard.ObserveUpstreamQuota("p", "k", hardAnthropicHeaders())
	if got := nilGuard.Status(); got.Enabled {
		t.Errorf("nil 评估器的状态应是关闭: %+v", got)
	}
	if got := nilGuard.Status(); got.LastEvaluatedAt != "" || got.LastError != "" {
		t.Errorf("nil 评估器不该有状态: %+v", got)
	}

	// 只有 Store、没有 Server：Evaluate 采集失败但不能 panic/挂住。
	store := quota.NewStoreWithClock("", time.Now)
	guard := NewQuotaGuard(QuotaGuardOptions{Store: store, Config: func() *config.RouterConfig {
		return &config.RouterConfig{QuotaFreezeIntervalSeconds: 60}
	}})
	evaluation := guard.Evaluate(context.Background())
	if !evaluation.Skipped || evaluation.Error == "" {
		t.Errorf("未装配 Server 时 Evaluate 应跳过并报错: %+v", evaluation)
	}
	if status := guard.Status(); status.LastError == "" {
		t.Error("采集失败应写进 last_error")
	}
	// 没有配置回调时一切按关闭处理。
	noConfig := NewQuotaGuard(QuotaGuardOptions{Store: store})
	if evaluation := noConfig.ApplyReport(subscriptionReport(true, exhaustedWindow())); !evaluation.Skipped {
		t.Errorf("无配置回调时 ApplyReport 应跳过: %+v", evaluation)
	}
	// 没有冻结表时也不能写。
	noStore := NewQuotaGuard(QuotaGuardOptions{Config: func() *config.RouterConfig {
		return &config.RouterConfig{QuotaFreezeIntervalSeconds: 60}
	}})
	if evaluation := noStore.ApplyReport(subscriptionReport(true, exhaustedWindow())); !evaluation.Skipped {
		t.Errorf("无冻结表时 ApplyReport 应跳过: %+v", evaluation)
	}
}

// —— quota/fetch 缺字段的口径 ——
//
// RemainingFraction 是**指针**：JSON 缺字段与显式 0 必须分开。缺字段是「上游没说」，
// 按 docs/USAGE.md 的口径整个窗口不画；显式 0 才是「真的用完了」。这条区分在引入额度
// 冻结之后成了安全问题：把缺失当 0 会凭空冻掉一把还有额度的 Key。

// quotaProbeFixture 起一个假 CPA 实例，quota/fetch 回指定的 groups，并装配冻结评估器。
func quotaProbeFixture(t *testing.T, groups []any) *quotaGuardFixture {
	t.Helper()
	stub := cpaStub(t, cpaStubSpec{
		files: []map[string]any{{
			"auth_index": "1", "name": "claude-a.json", "provider": "claude", "status": "active",
			"supports_quota": true, "quota_provider": "claude",
		}},
		quota: map[string]any{"1": map[string]any{"groups": groups}},
	})
	providers := fmt.Sprintf(`{"cpa-a":{"base_url":%q,"keys":{"key-1":{"api_key":"sk-1"}}}}`, stub.URL)
	instances := fmt.Sprintf(`{"jp":{"label":"日本 CPA","base_url":%q,"management_key":"mk-1"}}`, stub.URL)
	return newQuotaGuardFixture(t, providers, instances, "")
}

// listGuardedAccounts 刷一次账号资源页（会触发 ApplyReport）并解出响应。
func listGuardedAccounts(t *testing.T, fixture *quotaGuardFixture) cpaAccountsResponse {
	t.Helper()
	recorder := callCPA(t, fixture.Server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var response cpaAccountsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	return response
}

// TestQuotaGuardIgnoresBucketWithoutRemainingFraction 钉住「缺比例 ≠ 0%」：
// 缺字段的 bucket 不产生窗口，账号因此不算耗尽，也就不会冻结 Key。
func TestQuotaGuardIgnoresBucketWithoutRemainingFraction(t *testing.T) {
	fixture := quotaProbeFixture(t, []any{map[string]any{
		"displayName": "ChatGPT",
		"buckets": []any{
			// 缺 remainingFraction：上游没给比例。
			map[string]any{"window": "5h", "resetTime": "2027-01-15T08:00:00Z"},
			// 显式给了 50%：这一条要照常出现。
			map[string]any{"window": "7d", "remainingFraction": 0.5},
		},
	}})

	response := listGuardedAccounts(t, fixture)
	if len(response.Instances) != 1 || len(response.Instances[0].Accounts) != 1 {
		t.Fatalf("扇出形状不对: %+v", response.Instances)
	}
	account := response.Instances[0].Accounts[0]
	if account.QuotaError != "" {
		t.Errorf("探到有效窗口不该报错: %q", account.QuotaError)
	}
	if len(account.Windows) != 1 {
		t.Fatalf("缺 remainingFraction 的 bucket 不该产生窗口，应只剩显式给比例的那条: %+v", account.Windows)
	}
	if account.Windows[0].Window != "7d" || account.Windows[0].Remaining != 0.5 {
		t.Errorf("保留的窗口不对: %+v", account.Windows[0])
	}
	if account.QuotaExhausted {
		t.Errorf("只剩 50%% 的额度，不该判成用尽: %+v", account)
	}
	if len(response.Freezes) != 0 || fixture.Store.IsFrozen("cpa-a", "key-1") {
		t.Fatalf("缺比例不能当成 0%%，更不能据此冻结: %+v", response.Freezes)
	}
}

// TestQuotaGuardFreezesBucketWithExplicitZeroRemaining 钉住修复后的另一半：
// 显式 remainingFraction=0 仍然是 0%（产生窗口、计入实例级耗尽、触发冻结）。
func TestQuotaGuardFreezesBucketWithExplicitZeroRemaining(t *testing.T) {
	fixture := quotaProbeFixture(t, []any{map[string]any{
		"displayName": "ChatGPT",
		"buckets": []any{
			map[string]any{
				"window": "5h", "remainingFraction": 0,
				"resetTime": "2027-01-15T08:00:00Z", "description": "5 小时窗口",
			},
			// 同一组里再放一个缺比例的窗口：跳过它不该影响显式 0 的判定。
			map[string]any{"window": "7d"},
		},
	}})

	response := listGuardedAccounts(t, fixture)
	instance := response.Instances[0]
	account := instance.Accounts[0]
	if len(account.Windows) != 1 || account.Windows[0].Remaining != 0 {
		t.Fatalf("显式 0 应产生一条 0%% 窗口: %+v", account.Windows)
	}
	if !account.QuotaExhausted {
		t.Errorf("显式 0 应判成用尽: %+v", account)
	}
	if !instance.AllAccountsExhausted {
		t.Errorf("唯一启用账号用尽时实例应判为全部耗尽: %+v", instance)
	}
	if len(response.Freezes) != 1 || response.Freezes[0].Scope != "cpa_instance" {
		t.Fatalf("显式 0 应触发实例级冻结: %+v", response.Freezes)
	}
	if !fixture.Store.IsFrozen("cpa-a", "key-1") {
		t.Fatal("显式 0 应冻结关联供应商的启用 Key")
	}
}
