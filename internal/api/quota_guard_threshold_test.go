package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/quota"
)

// 额度冻结「提前冻结 + 内置计时器」的用例（设计稿 2/4/6.2）。
//
// 全部用夹具里的确定性时钟：到期、去抖、抑制窗口都跨时间，真实 sleep 会让用例变成
// 等待比赛。Run 循环本身不在这里跑（它的 select 只做编译级验证），到点清理直接调
// releaseExpired，计时器目标用 expiryWait 钉住。

// thresholdConfigExtra 打开 5h 5% / 7d 10% 两条底限（写进临时配置的顶层键）。
const thresholdConfigExtra = `"quota_freeze_threshold_5h":0.05,"quota_freeze_threshold_7d":0.1,`

// TestQuotaGuardThresholdPreFreeze 钉住订阅条目的提前冻结：低于底限就冻、边界值
// （remaining == 底限）也算命中、高于底限不冻、底限为 0 时这条线关闭。
func TestQuotaGuardThresholdPreFreeze(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, thresholdConfigExtra)

	resetAt := fixture.Clock.Now().Add(2 * time.Hour)
	window := quotaWindow("claude/5h", "5 小时", 0.04, "allowed", formatFreezeTime(resetAt))
	evaluation := fixture.Guard.ApplyReport(subscriptionReport(true, window))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("5h 剩余 4% 低于 5% 底限时，订阅条目应提前冻结")
	}
	if evaluation.Changed != 1 {
		t.Errorf("提前冻结应计为一次变化，实际 %+v", evaluation)
	}
	freeze, found := fixture.Store.Get("go-a", "key-1")
	if !found {
		t.Fatal("冻结记录应可读回")
	}
	if freeze.Trigger != "threshold" || freeze.Threshold != 0.05 {
		t.Errorf("提前冻结应记 trigger=threshold、threshold=0.05: %+v", freeze)
	}
	if !freeze.ExpiresAt.Equal(resetAt) {
		t.Errorf("到期时间应精确等于 reset_at（%v），实际 %v", resetAt, freeze.ExpiresAt)
	}
	if !strings.Contains(freeze.Reason, "剩余 4%") || !strings.Contains(freeze.Reason, "低于 5% 底限") {
		t.Errorf("提前冻结的原因要说清剩余与底限: %q", freeze.Reason)
	}

	// 边界：remaining 恰好等于底限也触发（remaining <= floor）。
	fixture.Guard.ApplyReport(subscriptionReport(true, healthyWindow()))
	boundary := quotaWindow("claude/5h", "5 小时", 0.05, "allowed", formatFreezeTime(resetAt))
	fixture.Guard.ApplyReport(subscriptionReport(true, boundary))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("remaining == 底限时应按 <= 的口径触发提前冻结")
	}

	// 高于底限：不冻。
	fixture.Guard.ApplyReport(subscriptionReport(true, healthyWindow()))
	above := quotaWindow("claude/5h", "5 小时", 0.06, "allowed", formatFreezeTime(resetAt))
	fixture.Guard.ApplyReport(subscriptionReport(true, above))
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("剩余 6% 高于 5% 底限时不该冻结")
	}

	// allowed_warning 但低于底限：仍按提前冻结处理（设计稿 2）。
	warning := quotaWindow("claude/5h", "5 小时", 0.03, "allowed_warning", formatFreezeTime(resetAt))
	fixture.Guard.ApplyReport(subscriptionReport(true, warning))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("allowed_warning 且低于底限时应提前冻结")
	}
}

// TestQuotaGuardThresholdSevenDay 钉住 7d 窗口按 7d 底限判定（不拿 5h 的阈值）。
func TestQuotaGuardThresholdSevenDay(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, thresholdConfigExtra)

	resetAt := fixture.Clock.Now().Add(6 * time.Hour)
	window := quotaWindow("claude/7d", "7 天", 0.08, "allowed", formatFreezeTime(resetAt))
	fixture.Guard.ApplyReport(subscriptionReport(true, window))

	freeze, found := fixture.Store.Get("go-a", "key-1")
	if !found {
		t.Fatal("7d 剩余 8% 低于 10% 底限时应提前冻结")
	}
	if freeze.Trigger != "threshold" || freeze.Threshold != 0.1 {
		t.Errorf("7d 应使用 10%% 底限: %+v", freeze)
	}
	if !freeze.ExpiresAt.Equal(resetAt) {
		t.Errorf("到期时间应精确等于 reset_at，实际 %v", freeze.ExpiresAt)
	}
}

// TestQuotaGuardThresholdDisabled 钉住「底限为 0 时提前冻结线关闭」：剩余 3% 但未
// 硬性耗尽时不冻结，只有硬性耗尽照常冻。
func TestQuotaGuardThresholdDisabled(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	resetAt := fixture.Clock.Now().Add(time.Hour)
	window := quotaWindow("claude/5h", "5 小时", 0.03, "allowed", formatFreezeTime(resetAt))
	fixture.Guard.ApplyReport(subscriptionReport(true, window))
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("底限为 0 时，未硬性耗尽的窗口不该冻结")
	}

	hard := exhaustedWindow()
	hard.ResetAt = formatFreezeTime(resetAt)
	fixture.Guard.ApplyReport(subscriptionReport(true, hard))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("硬性耗尽在底限为 0 时仍要冻结")
	}
}

// TestQuotaGuardThresholdUsesLatestReset 钉住「多个触发窗口取最晚 reset_at」。
func TestQuotaGuardThresholdUsesLatestReset(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, thresholdConfigExtra)

	short := quotaWindow("claude/5h", "5 小时", 0.03, "allowed", formatFreezeTime(fixture.Clock.Now().Add(time.Hour)))
	long := quotaWindow("claude/7d", "7 天", 0.05, "allowed", formatFreezeTime(fixture.Clock.Now().Add(4*time.Hour)))
	fixture.Guard.ApplyReport(subscriptionReport(true, short, long))

	freeze, found := fixture.Store.Get("go-a", "key-1")
	if !found {
		t.Fatal("两个窗口都触发时应冻结")
	}
	if freeze.WindowKey != "claude/7d" {
		t.Errorf("触发窗口应取最晚 reset 的那个（claude/7d），实际 %+v", freeze)
	}
	if want := fixture.Clock.Now().Add(4 * time.Hour); !freeze.ExpiresAt.Equal(want) {
		t.Errorf("到期时间应取最晚 reset_at（%v），实际 %v", want, freeze.ExpiresAt)
	}
}

// TestQuotaGuardHardTriggerDominatesThreshold 钉住混合窗口的 trigger 口径：同一把
// Key 同时有硬性耗尽与低于底限的窗口时，必须记 hard，白名单不能豁免已经见底的 Key。
func TestQuotaGuardHardTriggerDominatesThreshold(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, thresholdConfigExtra)

	now := fixture.Clock.Now()
	hard := quotaWindow("claude/5h", "5 小时", 0, "rejected", formatFreezeTime(now.Add(time.Hour)))
	threshold := quotaWindow("claude/7d", "7 天", 0.05, "allowed", formatFreezeTime(now.Add(4*time.Hour)))
	fixture.Guard.ApplyReport(subscriptionReport(true, hard, threshold))

	freeze, found := fixture.Store.Get("go-a", "key-1")
	if !found {
		t.Fatal("至少一个窗口触发时应冻结")
	}
	if freeze.Trigger != "hard" {
		t.Fatalf("任一窗口硬性耗尽时应记 trigger=hard，实际 %+v", freeze)
	}
	if freeze.WindowKey != "claude/5h" {
		t.Errorf("原因窗口应点名真正硬性耗尽的 claude/5h，实际 %+v", freeze)
	}
	if want := now.Add(4 * time.Hour); !freeze.ExpiresAt.Equal(want) {
		t.Errorf("到期时间仍应取所有触发窗口最晚的 reset_at（%v），实际 %v", want, freeze.ExpiresAt)
	}
}

// TestQuotaGuardInstanceThresholdPreFreeze 钉住实例级提前冻结：实例内全部启用账号都
// 至少有一个触发窗口才冻；任一账号恢复即解冻。
func TestQuotaGuardInstanceThresholdPreFreeze(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"cpa-claude":{"base_url":"http://127.0.0.1:8317","keys":{"claude-a":{"api_key":"sk-a"}}}}`,
		`{"jp":{"label":"日本 CPA","base_url":"http://127.0.0.1:8317","management_key":"mk-1"}}`,
		thresholdConfigExtra)

	firstReset := fixture.Clock.Now().Add(time.Hour)
	secondReset := fixture.Clock.Now().Add(2 * time.Hour)
	report := instanceReport(true,
		cpaAccount{Name: "a1.json", Email: "a1@example.com", Windows: []cpaQuotaWindow{
			quotaWindow("claude/5h", "5 小时", 0.03, "allowed", formatFreezeTime(firstReset)),
		}},
		cpaAccount{Name: "a2.json", Windows: []cpaQuotaWindow{
			quotaWindow("claude/7d", "7 天", 0.05, "allowed", formatFreezeTime(secondReset)),
		}},
	)
	fixture.Guard.ApplyReport(report)

	freeze, found := fixture.Store.Get("cpa-claude", "claude-a")
	if !found {
		t.Fatal("实例内全部启用账号都低于底限时应冻结关联 Key")
	}
	if freeze.Scope != "cpa_instance" || freeze.Trigger != "threshold" {
		t.Errorf("实例级提前冻结的 scope/trigger 不对: %+v", freeze)
	}
	if freeze.Threshold != 0.1 || freeze.WindowKey != "claude/7d" {
		t.Errorf("触发者应取最晚 reset 的 7d 窗口（底限 10%%）: %+v", freeze)
	}
	if !freeze.ExpiresAt.Equal(secondReset) {
		t.Errorf("到期时间应精确等于最晚 reset_at，实际 %v", freeze.ExpiresAt)
	}

	// 一个账号恢复额度：实例级冻结解除。
	fixture.Guard.ApplyReport(instanceReport(true,
		cpaAccount{Name: "a1.json", Windows: []cpaQuotaWindow{healthyWindow()}},
		cpaAccount{Name: "a2.json", Windows: []cpaQuotaWindow{
			quotaWindow("claude/7d", "7 天", 0.05, "allowed", formatFreezeTime(secondReset)),
		}},
	))
	if fixture.Store.IsFrozen("cpa-claude", "claude-a") {
		t.Fatal("实例内至少一个账号不再触发时应解冻")
	}

	// 全部账号的触发窗口 reset_at 都已过去：不拿过期读数冻结整台实例。
	past := formatFreezeTime(fixture.Clock.Now().Add(-time.Hour))
	fixture.Guard.ApplyReport(instanceReport(true,
		cpaAccount{Name: "a1.json", Windows: []cpaQuotaWindow{
			quotaWindow("claude/5h", "5 小时", 0.03, "allowed", past),
		}},
		cpaAccount{Name: "a2.json", Windows: []cpaQuotaWindow{
			quotaWindow("claude/7d", "7 天", 0.05, "allowed", past),
		}},
	))
	if fixture.Store.IsFrozen("cpa-claude", "claude-a") {
		t.Fatal("reset_at 已过去的实例读数不该新建冻结")
	}
}

// TestQuotaGuardPastResetDoesNotFreeze 钉住「reset_at 已过去就不新建冻结」：
// 这是到点自动解冻后不被旧读数冻回去的保证。
func TestQuotaGuardPastResetDoesNotFreeze(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, thresholdConfigExtra)

	past := quotaWindow("claude/5h", "5 小时", 0, "rejected", formatFreezeTime(fixture.Clock.Now().Add(-time.Hour)))
	fixture.Guard.ApplyReport(subscriptionReport(true, past))
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("reset_at 已经过去的读数不该新建冻结")
	}

	// 上游持续回旧读数：依旧不冻。
	fixture.Clock.Advance(time.Minute)
	fixture.Guard.ApplyReport(subscriptionReport(true, past))
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("旧读数持续观测时不该重新冻结")
	}

	// 给出新的、未来的 reset_at：重新冻结。
	future := quotaWindow("claude/5h", "5 小时", 0, "rejected", formatFreezeTime(fixture.Clock.Now().Add(time.Hour)))
	fixture.Guard.ApplyReport(subscriptionReport(true, future))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("给出新的未来 reset_at 后应重新冻结")
	}
}

// TestQuotaGuardFreezeExpiryOnlyMovesEarlier 钉住 Upsert 的计时器纪律：已生效的
// ExpiresAt 只会被更早的候选替换，后续观测不能把解冻时刻往后推。
func TestQuotaGuardFreezeExpiryOnlyMovesEarlier(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, thresholdConfigExtra)

	now := fixture.Clock.Now()
	apply := func(remaining float64, resetAt time.Time) quota.Freeze {
		t.Helper()
		fixture.Guard.ApplyReport(subscriptionReport(true,
			quotaWindow("claude/5h", "5 小时", remaining, "allowed", formatFreezeTime(resetAt))))
		freeze, found := fixture.Store.Get("go-a", "key-1")
		if !found {
			t.Fatalf("剩余 %.2f 时应冻结", remaining)
		}
		return freeze
	}

	first := apply(0.03, now.Add(2*time.Hour))
	// 后续观测给了更晚的 reset：窗口字段刷新，但计时器目标保持第一次的 2h。
	second := apply(0.02, now.Add(5*time.Hour))
	if !second.ExpiresAt.Equal(first.ExpiresAt) {
		t.Errorf("后续观测不得延后 ExpiresAt：首次 %v，之后 %v", first.ExpiresAt, second.ExpiresAt)
	}
	if !second.ResetAt.Equal(now.Add(5 * time.Hour)) {
		t.Errorf("窗口字段应刷新为最新 reset_at，实际 %v", second.ResetAt)
	}
	// 更早的 reset 应把计时器提前。
	third := apply(0.01, now.Add(time.Hour))
	if !third.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("更早的 reset_at 应把 ExpiresAt 提前到 %v，实际 %v", now.Add(time.Hour), third.ExpiresAt)
	}
}

// TestQuotaGuardReleaseExpired 钉住内置计时器的清理：只清已到期的，逐条返回，
// 没到期的不动，重复调用无副作用。
func TestQuotaGuardReleaseExpired(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"},"key-2":{"api_key":"sk-2"}}}}`,
		`{}`, "")

	now := fixture.Clock.Now()
	report := cpaAccountsReport{Subscriptions: []subscriptionEntry{
		{
			ID: "go-a/key-1", Kind: "opencode-go", Vendor: "OpenCode Go",
			ProviderID: "go-a", KeyName: "key-1", OK: true,
			Windows: []cpaQuotaWindow{quotaWindow("claude/5h", "5 小时", 0, "rejected", formatFreezeTime(now.Add(5*time.Minute)))},
		},
		{
			ID: "go-a/key-2", Kind: "opencode-go", Vendor: "OpenCode Go",
			ProviderID: "go-a", KeyName: "key-2", OK: true,
			Windows: []cpaQuotaWindow{quotaWindow("claude/5h", "5 小时", 0, "rejected", formatFreezeTime(now.Add(10*time.Minute)))},
		},
	}}
	fixture.Guard.ApplyReport(report)
	if got := len(fixture.Store.Snapshot()); got != 2 {
		t.Fatalf("前置条件：应写入 2 条冻结，实际 %d", got)
	}
	if wait, ok := fixture.Guard.expiryWait(); !ok || wait != 5*time.Minute {
		t.Fatalf("最近截止时间应为 5 分钟后，实际 wait=%v ok=%v", wait, ok)
	}

	fixture.Clock.Advance(6 * time.Minute)
	if removed := fixture.Guard.releaseExpired(); removed != 1 {
		t.Fatalf("应只清理 1 条到期冻结，实际 %d", removed)
	}
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("到期冻结应被清理")
	}
	if !fixture.Store.IsFrozen("go-a", "key-2") {
		t.Fatal("未到期的冻结不该被清理")
	}
	if wait, ok := fixture.Guard.expiryWait(); !ok || wait != 4*time.Minute {
		t.Fatalf("清理后最近截止时间应为 4 分钟后，实际 wait=%v ok=%v", wait, ok)
	}

	fixture.Clock.Advance(5 * time.Minute)
	if removed := fixture.Guard.releaseExpired(); removed != 1 {
		t.Fatalf("第二条也到期后应清理 1 条，实际 %d", removed)
	}
	if got := len(fixture.Store.Snapshot()); got != 0 {
		t.Fatalf("全部到期后快照应为空，实际 %d", got)
	}
	if _, ok := fixture.Guard.expiryWait(); ok {
		t.Fatal("没有冻结时 expiryWait 应返回 ok=false")
	}
	if removed := fixture.Guard.releaseExpired(); removed != 0 {
		t.Fatalf("重复清理不应再有变化，实际 %d", removed)
	}
}

// TestQuotaGuardExpiryTimerSurvivesDisabledInterval 钉住「interval=0 不关内置计时器」：
// 冻结表里已有的记录仍按 expires_at 到期解冻。
func TestQuotaGuardExpiryTimerSurvivesDisabledInterval(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	resetAt := fixture.Clock.Now().Add(2 * time.Hour)
	fixture.Guard.ApplyReport(subscriptionReport(true,
		quotaWindow("claude/5h", "5 小时", 0, "rejected", formatFreezeTime(resetAt))))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("前置条件：应已冻结")
	}

	// 热重载把巡检关掉：配置里的 interval 变 0，冻结表与评估器不重建。
	writeQuotaConfig(t, fixture.Path,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, `"quota_freeze_interval_seconds":0,`)
	disabled, err := config.Load(fixture.Path)
	if err != nil {
		t.Fatalf("载入关闭版配置失败: %v", err)
	}
	fixture.setConfig(disabled)
	if fixture.Guard.enabled() {
		t.Fatal("前置条件：interval=0 应报告机制关闭")
	}

	// 计时器目标仍然存在，且到点后 releaseExpired 照常清理。
	if wait, ok := fixture.Guard.expiryWait(); !ok || wait != 2*time.Hour {
		t.Fatalf("interval=0 时已有冻结仍应有 2 小时计时器，实际 wait=%v ok=%v", wait, ok)
	}
	fixture.Clock.Advance(3 * time.Hour)
	if removed := fixture.Guard.releaseExpired(); removed != 1 {
		t.Fatalf("interval=0 时到期冻结仍应清理，实际 %d", removed)
	}
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("interval=0 时到期冻结仍应解冻")
	}
}

// TestQuotaGuardStatusThresholds 钉住 Status 从当前运行时配置读底限。
func TestQuotaGuardStatusThresholds(t *testing.T) {
	fixture := newQuotaGuardFixture(t, `{}`, `{}`, thresholdConfigExtra)
	status := fixture.Guard.Status()
	if status.Threshold5h != 0.05 || status.Threshold7d != 0.1 {
		t.Errorf("Status 应报告当前底限 0.05/0.1，实际 %+v", status)
	}
}

// TestQuotaGuardWakesExpiryTimer 钉住冻结表变化会唤醒内置计时器（非阻塞信号）：
// 新增冻结与解冻都必须发，否则 Run 会等到旧定时器或下一轮巡检才重排。
func TestQuotaGuardWakesExpiryTimer(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	assertWoken := func(stage string) {
		t.Helper()
		select {
		case <-fixture.Guard.expiryWake:
		default:
			t.Fatalf("%s后应唤醒内置计时器", stage)
		}
	}

	resetAt := fixture.Clock.Now().Add(time.Hour)
	fixture.Guard.ApplyReport(subscriptionReport(true,
		quotaWindow("claude/5h", "5 小时", 0, "rejected", formatFreezeTime(resetAt))))
	assertWoken("新增冻结")

	fixture.Guard.ApplyReport(subscriptionReport(true, healthyWindow()))
	assertWoken("解冻")
}

// TestQuotaGuardL3ThresholdSignal 钉住 L3 也走底限：低于底限的响应头会投递信号并
// 冻结；高于底限的预警不投递；过去的 reset 不落地。
func TestQuotaGuardL3ThresholdSignal(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, thresholdConfigExtra)
	// 先评估一次，让关联表就绪（否则 L3 会保守地要求重新评估）。
	fixture.Guard.ApplyReport(cpaAccountsReport{})

	low := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.97"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1800000000"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
	}
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", low)
	drainSignal(t, fixture.Guard)
	freeze, found := fixture.Store.Get("prov", "key-1")
	if !found {
		t.Fatal("L3 低于底限的信号应冻结对应 Key")
	}
	if freeze.Trigger != "threshold" || freeze.Threshold != 0.05 {
		t.Errorf("L3 提前冻结应记 trigger=threshold、threshold=0.05: %+v", freeze)
	}
	if !strings.Contains(freeze.Reason, "低于 5% 底限") {
		t.Errorf("L3 提前冻结的原因应说明底限: %q", freeze.Reason)
	}

	// 高于底限的 allowed_warning：不投递。
	if _, _, err := fixture.Store.Release("prov", "key-1", time.Time{}); err != nil {
		t.Fatalf("解除冻结失败: %v", err)
	}
	fixture.Clock.Advance(quotaSignalDebounce + time.Second)
	high := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.90"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1800000000"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed_warning"},
	}
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", high)
	select {
	case signal := <-fixture.Guard.signals:
		t.Fatalf("高于底限的预警不该投递信号: %+v", signal)
	default:
	}

	// 过去的 reset：信号可以投递，但 handleSignal 不落地冻结。
	past := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.97"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1000000000"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"rejected"},
	}
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", past)
	drainSignal(t, fixture.Guard)
	if fixture.Store.IsFrozen("prov", "key-1") {
		t.Fatal("reset_at 已经过去的 L3 信号不该冻结")
	}
}

// TestQuotaGuardL3HardUpgradesThreshold 钉住「提前冻结后收到硬性耗尽」必须升级
// trigger：否则白名单模型会继续绕过已经见底的 Key，直到旧计时器到期。
func TestQuotaGuardL3HardUpgradesThreshold(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, thresholdConfigExtra)
	fixture.Guard.ApplyReport(cpaAccountsReport{})

	low := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.97"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1800000000"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
	}
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", low)
	drainSignal(t, fixture.Guard)
	before, found := fixture.Store.Get("prov", "key-1")
	if !found || before.Trigger != "threshold" {
		t.Fatalf("前置提前冻结未建立: %+v", before)
	}

	// 硬性耗尽带一个已经过去的 reset：已有冻结不应被过期读数延长，但 trigger
	// 必须升级，白名单从此失效。
	hard := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"1"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1000000000"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"rejected"},
	}
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", hard)
	drainSignal(t, fixture.Guard)
	after, found := fixture.Store.Get("prov", "key-1")
	if !found {
		t.Fatal("硬性信号不应把已有冻结解掉")
	}
	if after.Trigger != "hard" {
		t.Fatalf("已有提前冻结应升级为 hard，实际 %+v", after)
	}
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Errorf("硬性信号带过去 reset_at 时应保留原到期时间，before=%v after=%v", before.ExpiresAt, after.ExpiresAt)
	}
}

// TestAccountsReportBelowFloorViews 钉住账号资源响应里的两个新字段：
// accounts[].quota_below_floor 与 instances[].all_accounts_below_floor，且
// all_accounts_exhausted 仍按硬性耗尽单独算。
func TestAccountsReportBelowFloorViews(t *testing.T) {
	t.Run("低于底限但未耗尽", func(t *testing.T) {
		stub := cpaStub(t, cpaStubSpec{files: []map[string]any{{
			"auth_index": "1",
			"name":       "claude-a.json",
			"provider":   "claude",
			"email":      "ops@example.com",
			"quota": map[string]any{
				"signals": map[string]any{
					"Anthropic-Ratelimit-Unified-5h-Utilization": "0.97",
					"Anthropic-Ratelimit-Unified-5h-Reset":       "1800000000",
					"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
				},
			},
		}}})
		providers := fmt.Sprintf(`{"cpa-a":{"base_url":%q,"keys":{"key-1":{"api_key":"sk-1"}}}}`, stub.URL)
		instances := fmt.Sprintf(`{"jp":{"label":"日本 CPA","base_url":%q,"management_key":"mk-1"}}`, stub.URL)
		fixture := newQuotaGuardFixture(t, providers, instances, thresholdConfigExtra)

		response := listGuardedAccounts(t, fixture)
		instance := response.Instances[0]
		account := instance.Accounts[0]
		if account.QuotaExhausted {
			t.Errorf("剩余 3%% 未硬性耗尽，quota_exhausted 应为 false: %+v", account)
		}
		if !account.QuotaBelowFloor {
			t.Errorf("剩余 3%% 低于 5%% 底限，quota_below_floor 应为 true: %+v", account)
		}
		if instance.AllAccountsExhausted {
			t.Errorf("未硬性耗尽时 all_accounts_exhausted 应为 false: %+v", instance)
		}
		if !instance.AllAccountsBelowFloor {
			t.Errorf("唯一启用账号低于底限时 all_accounts_below_floor 应为 true: %+v", instance)
		}
		if len(response.Freezes) != 1 || response.Freezes[0].Trigger != "threshold" ||
			response.Freezes[0].Threshold != 0.05 {
			t.Fatalf("顶层 freezes 应带提前冻结字段: %+v", response.Freezes)
		}
	})

	t.Run("硬性耗尽与低于底限可同时为 true", func(t *testing.T) {
		stub := cpaStub(t, cpaStubSpec{files: []map[string]any{{
			"auth_index": "1",
			"name":       "claude-a.json",
			"provider":   "claude",
			"quota": map[string]any{
				"signals": map[string]any{
					"Anthropic-Ratelimit-Unified-5h-Utilization": "1",
					"Anthropic-Ratelimit-Unified-5h-Reset":       "1800000000",
					"Anthropic-Ratelimit-Unified-5h-Status":      "rejected",
				},
			},
		}}})
		providers := fmt.Sprintf(`{"cpa-a":{"base_url":%q,"keys":{"key-1":{"api_key":"sk-1"}}}}`, stub.URL)
		instances := fmt.Sprintf(`{"jp":{"label":"日本 CPA","base_url":%q,"management_key":"mk-1"}}`, stub.URL)
		fixture := newQuotaGuardFixture(t, providers, instances, thresholdConfigExtra)

		response := listGuardedAccounts(t, fixture)
		instance := response.Instances[0]
		account := instance.Accounts[0]
		if !account.QuotaExhausted {
			t.Errorf("硬性耗尽时 quota_exhausted 应为 true: %+v", account)
		}
		if !account.QuotaBelowFloor {
			t.Errorf("底限配置后，剩余 0%% 也落在底限内，quota_below_floor 应为 true: %+v", account)
		}
		if !instance.AllAccountsExhausted {
			t.Errorf("硬性耗尽时 all_accounts_exhausted 应为 true: %+v", instance)
		}
		if !instance.AllAccountsBelowFloor {
			t.Errorf("全部账号都落在底限内时 all_accounts_below_floor 应为 true: %+v", instance)
		}
	})

	t.Run("被动 5h 低于底限", func(t *testing.T) {
		stub := cpaStub(t, cpaStubSpec{files: []map[string]any{{
			"auth_index": "1",
			"name":       "claude-a.json",
			"provider":   "claude",
			"quota": map[string]any{
				"signals": map[string]any{
					"Anthropic-Ratelimit-Unified-5h-Utilization": "0.97",
					"Anthropic-Ratelimit-Unified-5h-Reset":       "1800000000",
					"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
				},
			},
		}}})
		providers := fmt.Sprintf(`{"cpa-a":{"base_url":%q,"keys":{"key-1":{"api_key":"sk-1"}}}}`, stub.URL)
		instances := fmt.Sprintf(`{"jp":{"label":"日本 CPA","base_url":%q,"management_key":"mk-1"}}`, stub.URL)
		fixture := newQuotaGuardFixture(t, providers, instances, thresholdConfigExtra)

		response := listGuardedAccounts(t, fixture)
		if len(response.Freezes) != 1 {
			t.Fatalf("被动 5h 剩余 3%% 时应提前冻结一条，实际 %+v", response.Freezes)
		}
		freeze := response.Freezes[0]
		if freeze.Trigger != "threshold" || freeze.Threshold != 0.05 {
			t.Errorf("被动窗口应记 trigger=threshold/threshold=0.05: %+v", freeze)
		}
		if freeze.WindowLabel != "5 小时" || freeze.ExpiresAt != "2027-01-15T08:00:00Z" {
			t.Errorf("被动窗口的标签或计时器不对: %+v", freeze)
		}
		if account := response.Instances[0].Accounts[0]; !account.QuotaBelowFloor {
			t.Errorf("被动 5h 低于底限时账号应标记 quota_below_floor: %+v", account)
		}
	})

	t.Run("底限为 0 时不标记低于底限", func(t *testing.T) {
		stub := cpaStub(t, cpaStubSpec{files: []map[string]any{{
			"auth_index": "1",
			"name":       "claude-a.json",
			"provider":   "claude",
			"quota": map[string]any{
				"signals": map[string]any{
					"Anthropic-Ratelimit-Unified-5h-Utilization": "1",
					"Anthropic-Ratelimit-Unified-5h-Reset":       "1800000000",
					"Anthropic-Ratelimit-Unified-5h-Status":      "rejected",
				},
			},
		}}})
		providers := fmt.Sprintf(`{"cpa-a":{"base_url":%q,"keys":{"key-1":{"api_key":"sk-1"}}}}`, stub.URL)
		instances := fmt.Sprintf(`{"jp":{"label":"日本 CPA","base_url":%q,"management_key":"mk-1"}}`, stub.URL)
		fixture := newQuotaGuardFixture(t, providers, instances, "")

		response := listGuardedAccounts(t, fixture)
		instance := response.Instances[0]
		if account := instance.Accounts[0]; !account.QuotaExhausted || account.QuotaBelowFloor {
			t.Errorf("底限为 0 时应只报额度用尽，不报低于底限: %+v", account)
		}
		if instance.AllAccountsBelowFloor {
			t.Errorf("底限为 0 时 all_accounts_below_floor 应为 false: %+v", instance)
		}
	})
}
