package api

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/quota"
)

// 账号级冻结策略（quota_freeze_policies）的用例：
//
// 策略只在**账号**这一层覆盖全局默认；未配置时必须保持 7.0.3 的行为（尤其是
// Freeze.Whitelist=nil，让 keypool 动态回退全局名单）。这些用例不启动 Run 循环，
// 用确定性时钟直接驱动 ApplyReport / L3 信号落地。

func quotaPolicyFloat(value float64) *float64 { return &value }

// setQuotaGuardConfig 复制当前配置后修改，避免测试之间共享指针。
func setQuotaGuardConfig(t *testing.T, fixture *quotaGuardFixture, mutate func(*config.RouterConfig)) {
	t.Helper()
	current := fixture.currentConfig()
	if current == nil {
		t.Fatal("测试夹具没有配置")
	}
	next := *current
	mutate(&next)
	fixture.setConfig(&next)
}

func withQuotaGuardModels(cfg *config.RouterConfig) {
	cfg.Models = []config.ModelConfig{{
		ID:      "model-a",
		Aliases: []string{"alias-a"},
	}}
}

// TestEffectiveQuotaFreezePolicyMerges 钉住策略合并的四个边界：全局继承、逐档覆盖、
// 白名单显式设置（含空数组）与 disabled。无策略/继承全局名单时 Whitelist 必须为 nil，
// 交给 keypool 动态回退全局，避免把全局热改的名单冻结在旧记录里。
func TestEffectiveQuotaFreezePolicyMerges(t *testing.T) {
	cfg := &config.RouterConfig{
		QuotaFreezeThreshold5h:        0.1,
		QuotaFreezeThreshold7d:        0.2,
		QuotaFreezeThresholdWhitelist: []string{"alias-global"},
		Models: []config.ModelConfig{
			{ID: "model-a", Aliases: []string{"alias-a"}},
			{ID: "model-global", Aliases: []string{"alias-global"}},
		},
		QuotaFreezePolicies: []config.QuotaFreezePolicy{
			{
				Scope:        "subscription",
				Target:       "go-a/key-1",
				Enabled:      true,
				Threshold5h:  quotaPolicyFloat(0.5),
				Whitelist:    []string{"alias-a", "model-a", " "},
				WhitelistSet: true,
			},
			{
				Scope:       "cpa_instance",
				Target:      "jp",
				Enabled:     false,
				Threshold7d: quotaPolicyFloat(0.3),
			},
			{
				Scope:        "cpa_instance",
				Target:       "empty",
				Enabled:      true,
				Whitelist:    []string{},
				WhitelistSet: true,
			},
			{
				Scope:        "cpa_instance",
				Target:       "nil-explicit",
				Enabled:      true,
				WhitelistSet: true,
			},
		},
	}

	overridden := effectiveQuotaFreezePolicy(cfg, "subscription", "go-a/key-1")
	if !overridden.Enabled {
		t.Error("显式 enabled=true 的策略应保持启用")
	}
	if overridden.Thresholds.FiveHour != 0.5 || overridden.Thresholds.SevenDay != 0.2 {
		t.Errorf("阈值应逐档覆盖、nil 继承: %+v", overridden.Thresholds)
	}
	if !slices.Equal(overridden.Whitelist, []string{"model-a"}) {
		t.Errorf("白名单应别名归一、去重排序: %#v", overridden.Whitelist)
	}

	inherited := effectiveQuotaFreezePolicy(cfg, "subscription", "go-a/no-policy")
	if !inherited.Enabled {
		t.Error("没有策略时应继承全局 enabled")
	}
	if inherited.Thresholds.FiveHour != 0.1 || inherited.Thresholds.SevenDay != 0.2 {
		t.Errorf("没有策略时应继承全局阈值: %+v", inherited.Thresholds)
	}
	if inherited.Whitelist != nil {
		t.Errorf("没有策略时 Freeze 白名单应保持 nil，让 keypool 回退全局: %#v", inherited.Whitelist)
	}

	disabled := effectiveQuotaFreezePolicy(cfg, "cpa_instance", "jp")
	if disabled.Enabled {
		t.Error("策略 enabled=false 应关闭自动冻结")
	}
	if disabled.Thresholds.FiveHour != 0.1 || disabled.Thresholds.SevenDay != 0.3 {
		t.Errorf("disabled 策略仍应能合并阈值供展示: %+v", disabled.Thresholds)
	}
	if disabled.Whitelist != nil {
		t.Errorf("WhitelistSet=false 应继承（nil）而不是冻结当前全局名单: %#v", disabled.Whitelist)
	}

	empty := effectiveQuotaFreezePolicy(cfg, "cpa_instance", "empty")
	if empty.Whitelist == nil || len(empty.Whitelist) != 0 {
		t.Errorf("策略显式空白名单应是非 nil 空切片: %#v", empty.Whitelist)
	}
	nilExplicit := effectiveQuotaFreezePolicy(cfg, "cpa_instance", "nil-explicit")
	if nilExplicit.Whitelist == nil || len(nilExplicit.Whitelist) != 0 {
		t.Errorf("WhitelistSet=true 的 nil 名单也应是非 nil 空切片: %#v", nilExplicit.Whitelist)
	}
}

// TestQuotaGuardSubscriptionPolicyOverridesGlobal 钉住订阅条目的账号策略：
// 全局底限不触发、策略底限触发；策略白名单随 Freeze 落盘。
func TestQuotaGuardSubscriptionPolicyOverridesGlobal(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezeThreshold5h = 0.01
		cfg.QuotaFreezeThreshold7d = 0
		withQuotaGuardModels(cfg)
	})

	resetAt := formatFreezeTime(fixture.Clock.Now().Add(2 * time.Hour))
	report := subscriptionReport(true, quotaWindow("claude/5h", "5 小时", 0.3, "allowed", resetAt))

	// 无策略：0.3 高于全局 0.01，不应冻结（同时确认没有策略时的旧行为）。
	fixture.Guard.ApplyReport(report)
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("全局底限下 0.3 不应触发冻结")
	}

	// 账号策略把 5h 底限抬到 0.5：同一份读数应触发提前冻结，并写入归一后的白名单。
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{{
			Scope:        "subscription",
			Target:       "go-a/key-1",
			Enabled:      true,
			Threshold5h:  quotaPolicyFloat(0.5),
			Whitelist:    []string{"alias-a", "model-a"},
			WhitelistSet: true,
		}}
	})
	fixture.Guard.ApplyReport(report)

	freeze, found := fixture.Store.Get("go-a", "key-1")
	if !found {
		t.Fatal("订阅账号策略应触发冻结")
	}
	if freeze.Scope != "subscription" || freeze.Trigger != "threshold" || freeze.Threshold != 0.5 {
		t.Errorf("订阅策略冻结字段不对: %+v", freeze)
	}
	if got := fixture.Store.FrozenWhitelist("go-a", "key-1"); !slices.Equal(got, []string{"model-a"}) {
		t.Errorf("策略白名单应写成真实模型 ID: %#v", got)
	}
	if got := quotaFreezeView(freeze).Whitelist; !slices.Equal(got, []string{"model-a"}) {
		t.Errorf("冻结视图应带生效白名单: %#v", got)
	}
}

// TestQuotaGuardNoPolicyKeepsWhitelistNil 钉住 7.0.3 兼容：即使配置了全局白名单，
// 没有账号策略的新冻结也不捕获名单，而是让 keypool 动态回退全局。
func TestQuotaGuardNoPolicyKeepsWhitelistNil(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezeThreshold5h = 0.5
		cfg.QuotaFreezeThresholdWhitelist = []string{"model-a"}
		withQuotaGuardModels(cfg)
	})

	resetAt := formatFreezeTime(fixture.Clock.Now().Add(2 * time.Hour))
	fixture.Guard.ApplyReport(subscriptionReport(true,
		quotaWindow("claude/5h", "5 小时", 0.3, "allowed", resetAt)))

	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("全局底限应触发冻结")
	}
	if got := fixture.Store.FrozenWhitelist("go-a", "key-1"); got != nil {
		t.Errorf("无策略时 Freeze.Whitelist 应为 nil（让 keypool 回退全局）: %#v", got)
	}
	freeze, found := fixture.Store.Get("go-a", "key-1")
	if !found {
		t.Fatal("前置冻结不存在")
	}
	if got := quotaFreezeView(freeze).Whitelist; got != nil {
		t.Errorf("无策略时冻结视图 whitelist 应为 null: %#v", got)
	}
}

// TestQuotaGuardInstancePolicyOverridesGlobal 钉住 CPA 实例策略与视图底限：
// 实例横幅与账号徽标都必须使用实例策略的 effective thresholds。
func TestQuotaGuardInstancePolicyOverridesGlobal(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"cpa-a":{"base_url":"http://127.0.0.1:8317","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{"jp":{"label":"日本 CPA","base_url":"http://127.0.0.1:8317","management_key":"mk-1"}}`, "")
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezeThreshold5h = 0.01
		cfg.QuotaFreezeThreshold7d = 0
		withQuotaGuardModels(cfg)
	})

	resetAt := formatFreezeTime(fixture.Clock.Now().Add(2 * time.Hour))
	report := instanceReport(true, cpaAccount{
		Name:    "a1.json",
		Windows: []cpaQuotaWindow{quotaWindow("claude/5h", "5 小时", 0.3, "allowed", resetAt)},
	})

	fixture.Guard.ApplyReport(report)
	if fixture.Store.IsFrozen("cpa-a", "key-1") {
		t.Fatal("全局 0.01 下 0.3 不应触发实例冻结")
	}

	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{{
			Scope:        "cpa_instance",
			Target:       "jp",
			Enabled:      true,
			Threshold5h:  quotaPolicyFloat(0.5),
			Whitelist:    []string{"alias-a", "model-a"},
			WhitelistSet: true,
		}}
	})
	fixture.Guard.ApplyReport(report)

	freeze, found := fixture.Store.Get("cpa-a", "key-1")
	if !found {
		t.Fatal("实例账号策略应触发冻结")
	}
	if freeze.Scope != "cpa_instance" || freeze.Threshold != 0.5 {
		t.Errorf("实例策略冻结字段不对: %+v", freeze)
	}
	if got := fixture.Store.FrozenWhitelist("cpa-a", "key-1"); !slices.Equal(got, []string{"model-a"}) {
		t.Errorf("实例策略白名单应写成真实模型 ID: %#v", got)
	}

	view := fixture.Server.cpaAccountsView(fixture.currentConfig(), report)
	instance := view.Instances[0]
	if !instance.AllAccountsBelowFloor {
		t.Errorf("实例横幅应使用策略底限 0.5: %+v", instance)
	}
	if !instance.Accounts[0].QuotaBelowFloor {
		t.Errorf("账号徽标应使用实例策略底限 0.5: %+v", instance.Accounts[0])
	}
	if instance.AllAccountsExhausted {
		t.Errorf("all_accounts_exhausted 仍应只按硬性耗尽计算: %+v", instance)
	}

	// 策略关闭后实例级「低于底限」不能继续为 true：UI 的横幅文案是「已冻结」，
	// 而 disabled 策略根本不会产生冻结。
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{{
			Scope:       "cpa_instance",
			Target:      "jp",
			Enabled:     false,
			Threshold5h: quotaPolicyFloat(0.5),
		}}
	})
	disabledView := fixture.Server.cpaAccountsView(fixture.currentConfig(), report)
	if disabledView.Instances[0].AllAccountsBelowFloor {
		t.Errorf("策略 disabled 时 all_accounts_below_floor 应为 false: %+v", disabledView.Instances[0])
	}
}

// TestQuotaGuardPolicyDisabledReleases 钉住 enabled=false 的两个方向：不新建冻结，
// 并解除该账号此前由同一层规则产生的冻结（订阅还要解除 upstream）。
func TestQuotaGuardPolicyDisabledReleases(t *testing.T) {
	t.Run("订阅策略关闭", func(t *testing.T) {
		fixture := newQuotaGuardFixture(t,
			`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
			`{}`, "")
		setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
			cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{{
				Scope:   "subscription",
				Target:  "go-a/key-1",
				Enabled: false,
			}}
		})
		now := fixture.Clock.Now()
		if _, err := fixture.Store.Upsert(quota.Freeze{
			ProviderID: "go-a", KeyName: "key-1", Scope: "subscription",
			Source: "account_resource", WindowKey: "claude/5h", WindowLabel: "5 小时",
			Trigger: "hard", FrozenAt: now, ExpiresAt: now.Add(time.Hour), Reason: "seed",
		}); err != nil {
			t.Fatalf("写入前置冻结失败: %v", err)
		}

		fixture.Guard.ApplyReport(subscriptionReport(true,
			quotaWindow("claude/5h", "5 小时", 0.0, "rejected", formatFreezeTime(now.Add(time.Hour)))))

		if fixture.Store.IsFrozen("go-a", "key-1") {
			t.Fatal("订阅策略 disabled 时应解除已有的 subscription 冻结")
		}
	})

	t.Run("实例策略关闭", func(t *testing.T) {
		fixture := newQuotaGuardFixture(t,
			`{"cpa-a":{"base_url":"http://127.0.0.1:8317","keys":{"key-1":{"api_key":"sk-1"}}}}`,
			`{"jp":{"label":"日本 CPA","base_url":"http://127.0.0.1:8317","management_key":"mk-1"}}`, "")
		setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
			cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{{
				Scope:   "cpa_instance",
				Target:  "jp",
				Enabled: false,
			}}
		})
		now := fixture.Clock.Now()
		if _, err := fixture.Store.Upsert(quota.Freeze{
			ProviderID: "cpa-a", KeyName: "key-1", Scope: "cpa_instance",
			Source: "account_resource", WindowKey: "claude/5h", WindowLabel: "5 小时",
			Trigger: "hard", FrozenAt: now, ExpiresAt: now.Add(time.Hour), Reason: "seed",
		}); err != nil {
			t.Fatalf("写入前置冻结失败: %v", err)
		}

		fixture.Guard.ApplyReport(instanceReport(true, cpaAccount{
			Name:    "a1.json",
			Windows: []cpaQuotaWindow{quotaWindow("claude/5h", "5 小时", 0, "rejected", formatFreezeTime(now.Add(time.Hour)))},
		}))

		if fixture.Store.IsFrozen("cpa-a", "key-1") {
			t.Fatal("实例策略 disabled 时应解除已有的 cpa_instance 冻结")
		}
	})
}

// TestQuotaGuardL3UsesPolicyThresholds 钉住 L3 观察点与 handleSignal 都使用账号策略：
// 全局底限不投递、策略底限投递，并把策略白名单写进 upstream 冻结。
func TestQuotaGuardL3UsesPolicyThresholds(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezeThreshold5h = 0.01
		cfg.QuotaFreezeThreshold7d = 0
		withQuotaGuardModels(cfg)
	})
	// 先评估一次，让关联表就绪（未关联实例 → subscription 策略）。
	fixture.Guard.ApplyReport(cpaAccountsReport{})

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.7"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1800000000"},
	}
	// 全局 0.01 下剩余 0.3 不触发，观察点不应投递。
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", headers)
	select {
	case signal := <-fixture.Guard.signals:
		t.Fatalf("全局底限下不该投递信号: %+v", signal)
	default:
	}

	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{{
			Scope:        "subscription",
			Target:       "prov/key-1",
			Enabled:      true,
			Threshold5h:  quotaPolicyFloat(0.5),
			Whitelist:    []string{"alias-a"},
			WhitelistSet: true,
		}}
	})
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", headers)
	drainSignal(t, fixture.Guard)

	freeze, found := fixture.Store.Get("prov", "key-1")
	if !found {
		t.Fatal("策略底限 0.5 下 L3 应直接冻结")
	}
	if freeze.Scope != "upstream" || freeze.Trigger != "threshold" || freeze.Threshold != 0.5 {
		t.Errorf("L3 策略冻结字段不对: %+v", freeze)
	}
	if got := fixture.Store.FrozenWhitelist("prov", "key-1"); !slices.Equal(got, []string{"model-a"}) {
		t.Errorf("L3 冻结应写入策略白名单: %#v", got)
	}
}

// TestQuotaGuardL3PolicyDisabledSkips 钉住策略 disabled 时 L3 连信号都不投递；
// 观察点不做「先投递再被 handleSignal 丢掉」的无用工作。
func TestQuotaGuardL3PolicyDisabledSkips(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"prov":{"base_url":"https://api.example.test","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{{
			Scope:       "subscription",
			Target:      "prov/key-1",
			Enabled:     false,
			Threshold5h: quotaPolicyFloat(0.5),
		}}
	})
	fixture.Guard.ApplyReport(cpaAccountsReport{})

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.7"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1800000000"},
	}
	fixture.Guard.ObserveUpstreamQuota("prov", "key-1", headers)
	select {
	case signal := <-fixture.Guard.signals:
		t.Fatalf("策略 disabled 时不该投递信号: %+v", signal)
	default:
	}
	if fixture.Store.IsFrozen("prov", "key-1") {
		t.Fatal("策略 disabled 时 L3 不得冻结")
	}
}

// TestQuotaGuardL3InstancePolicyDisabledSkips 钉住已关联实例（即使单账号）的
// cpa_instance 策略 enabled=false 会让 L3 跳过，而不是误用 subscription 策略。
func TestQuotaGuardL3InstancePolicyDisabledSkips(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"cpa-a":{"base_url":"http://127.0.0.1:8317","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{"jp":{"label":"日本 CPA","base_url":"http://127.0.0.1:8317","management_key":"mk-1"}}`, "")
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezeThreshold5h = 0.01
		cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{
			{
				Scope:       "subscription",
				Target:      "cpa-a/key-1",
				Enabled:     true,
				Threshold5h: quotaPolicyFloat(0.5),
			},
			{
				Scope:       "cpa_instance",
				Target:      "jp",
				Enabled:     false,
				Threshold5h: quotaPolicyFloat(0.5),
			},
		}
	})
	fixture.Guard.ApplyReport(instanceReport(true, cpaAccount{
		Name:    "a1.json",
		Windows: []cpaQuotaWindow{healthyWindow()},
	}))

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.7"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1800000000"},
	}
	fixture.Guard.ObserveUpstreamQuota("cpa-a", "key-1", headers)
	select {
	case signal := <-fixture.Guard.signals:
		t.Fatalf("cpa_instance 策略 disabled 时不该投递信号: %+v", signal)
	default:
	}
	if fixture.Store.IsFrozen("cpa-a", "key-1") {
		t.Fatal("cpa_instance 策略 disabled 时 L3 不得冻结")
	}
}

// TestQuotaGuardL3SingleAccountUsesInstancePolicy 钉住「已关联实例（即使单账号）」
// 使用 cpa_instance 策略，而不是 subscription 策略：实例级 enabled=false 必须对
// L3 生效，单账号只是允许直接落 upstream 冻结。
func TestQuotaGuardL3SingleAccountUsesInstancePolicy(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"cpa-a":{"base_url":"http://127.0.0.1:8317","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{"jp":{"label":"日本 CPA","base_url":"http://127.0.0.1:8317","management_key":"mk-1"}}`, "")
	setQuotaGuardConfig(t, fixture, func(cfg *config.RouterConfig) {
		cfg.QuotaFreezeThreshold5h = 0.01
		cfg.QuotaFreezePolicies = []config.QuotaFreezePolicy{
			{
				Scope:       "subscription",
				Target:      "cpa-a/key-1",
				Enabled:     true,
				Threshold5h: quotaPolicyFloat(0.01),
			},
			{
				Scope:       "cpa_instance",
				Target:      "jp",
				Enabled:     true,
				Threshold5h: quotaPolicyFloat(0.5),
			},
		}
	})
	// 单账号实例：让关联表就绪，EnabledAccounts=1。
	fixture.Guard.ApplyReport(instanceReport(true, cpaAccount{
		Name:    "a1.json",
		Windows: []cpaQuotaWindow{healthyWindow()},
	}))

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.7"},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1800000000"},
	}
	fixture.Guard.ObserveUpstreamQuota("cpa-a", "key-1", headers)
	drainSignal(t, fixture.Guard)

	freeze, found := fixture.Store.Get("cpa-a", "key-1")
	if !found {
		t.Fatal("单账号实例的 L3 应使用 cpa_instance 策略直接冻结")
	}
	if freeze.Threshold != 0.5 {
		t.Errorf("单账号实例应使用 cpa_instance 策略阈值 0.5，实际 %+v", freeze)
	}
}
