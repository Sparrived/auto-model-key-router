package keypool

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件锁定「额度冻结」的选路语义。
//
// 与 concurrency_test.go / balance_test.go 的断言风格一致：只断言**可观察行为**
// （选到谁、报什么错、解冻后能不能恢复），不直接读内部状态——冻结表怎么实现都可以，
// 但「冻结中的 Key 一律不参与选路、全部冻结时报额度冻结而不是没有可用 key」是对外
// 的承诺（docs/design/quota-freeze.md 第 1、2、5 节）。

// fakeQuotaFreezes 是 QuotaFreezes 的假实现：按 (provider, key) 查一张冻结表。
//
// 生产实现是 *quota.Store（带持久化），选路层只依赖这两个读方法。假实现同样加锁，
// 因为 SetQuotaFreezes 与读会并发（见并发用例）。
type fakeQuotaFreezes struct {
	mu       sync.Mutex
	reasons  map[[2]string]string
	triggers map[[2]string]string
}

// newFakeQuotaFreezes 构造空的冻结表。
func newFakeQuotaFreezes() *fakeQuotaFreezes {
	return &fakeQuotaFreezes{
		reasons:  map[[2]string]string{},
		triggers: map[[2]string]string{},
	}
}

// Freeze 冻结一把 Key，并记下原因（硬性耗尽，白名单不可豁免）。
func (f *fakeQuotaFreezes) Freeze(providerID, keyName, reason string) {
	f.freeze(providerID, keyName, reason, "hard")
}

// FreezeThreshold 冻结一把 Key，并标记为提前冻结（白名单模型可豁免）。
func (f *fakeQuotaFreezes) FreezeThreshold(providerID, keyName, reason string) {
	f.freeze(providerID, keyName, reason, "threshold")
}

func (f *fakeQuotaFreezes) freeze(providerID, keyName, reason, trigger string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := [2]string{providerID, keyName}
	f.reasons[key] = reason
	f.triggers[key] = trigger
}

// Release 解除一把 Key 的冻结。
func (f *fakeQuotaFreezes) Release(providerID, keyName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := [2]string{providerID, keyName}
	delete(f.reasons, key)
	delete(f.triggers, key)
}

// IsFrozen 报告是否在冻结表里。
func (f *fakeQuotaFreezes) IsFrozen(providerID, keyName string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, found := f.reasons[[2]string{providerID, keyName}]
	return found
}

// FrozenReason 返回冻结原因。
func (f *fakeQuotaFreezes) FrozenReason(providerID, keyName string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reasons[[2]string{providerID, keyName}]
}

// FrozenTrigger 返回冻结类型；未冻结时为硬性（空串按 hard 处理）。
func (f *fakeQuotaFreezes) FrozenTrigger(providerID, keyName string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.triggers[[2]string{providerID, keyName}]
}

// frozenReasonText 是测试里统一使用的冻结原因，形态与生产 quota.Store 写入的一致。
const frozenReasonText = "5 小时窗口已用尽（剩余 0%），15:00 后重置"

// freezeModelConfig 构造「一个模型两把 key」的配置；routingMode 可指定。
//
// key_failure_threshold 取 1，是为了让软回退用例能一键把某把 key 推进冷却。
func freezeModelConfig(t *testing.T, routingMode string) *config.RouterConfig {
	t.Helper()
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"key_failure_threshold": 1, "key_cooldown_seconds": 60,
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "1"}, "k2": {"api_key": "2"}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"},
			{"provider": "p", "key": "k2", "upstream_model": "u2"}],
			"routing_mode": "` + routingMode + `"}}
	}`
	return mustConfig(t, raw)
}

// newFrozenPool 构造一个注入了冻结表的池子。
func newFrozenPool(t *testing.T, routingMode string) (*KeyPool, *fakeQuotaFreezes) {
	t.Helper()
	freezes := newFakeQuotaFreezes()
	pool := New(freezeModelConfig(t, routingMode), nil, func() float64 { return 1000 })
	pool.SetQuotaFreezes(freezes)
	return pool, freezes
}

// keyNames 提取候选 key 的名字（保持配置顺序）。
func keyNames(keys []config.KeyConfig) []string {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key.Name)
	}
	return names
}

// TestFrozenKeyExcludedFromCandidates 固化「冻结在候选集合级剔除」。
//
// KeyCount / KeysForModel / 选路都走同一层过滤：冻结一把 Key 后，它从计数与游标里
// 同时消失，而不是等到挑选时才被跳过。AvailableModelIDs 是例外：它只看启用 Key，
// 避免 /v1/models 抖动导致客户端删模型；冻结期间调用会返回 ErrQuotaFrozen。
func TestFrozenKeyExcludedFromCandidates(t *testing.T) {
	pool, freezes := newFrozenPool(t, "round_robin")
	freezes.Freeze("p", "k1", frozenReasonText)

	if got := pool.KeyCount("m"); got != 1 {
		t.Fatalf("冻结一把后 KeyCount: got %d want 1", got)
	}
	if got := pool.FrozenKeyCount("m", nil); got != 1 {
		t.Fatalf("FrozenKeyCount: got %d want 1", got)
	}
	if names := keyNames(pool.KeysForModel("m", nil)); len(names) != 1 || names[0] != "k2" {
		t.Fatalf("候选应只剩 k2，实得 %v", names)
	}
	// 模型清单只按配置上的启用 Key 过滤；冻结不能让它从 /v1/models 里消失。
	if !containsString(pool.AvailableModelIDs(nil), "m") {
		t.Fatal("模型有启用 Key 时应始终出现在模型清单里，冻结不应摘掉它")
	}

	for i := 0; i < 5; i++ {
		key, err := pool.NextKey("m", nil, nil, "")
		if err != nil {
			t.Fatalf("第 %d 次选路失败: %v", i+1, err)
		}
		if key.Name != "k2" {
			t.Fatalf("冻结的 k1 不应被选中，实得 %q", key.Name)
		}
		pool.ReleaseKey("m", key.Name)
	}
}

// TestFrozenKeyExcludedInPriority 固化 priority 模式同样跳过冻结的 Key。
func TestFrozenKeyExcludedInPriority(t *testing.T) {
	pool, freezes := newFrozenPool(t, "priority")
	freezes.Freeze("p", "k1", frozenReasonText)

	key, err := pool.NextKey("m", nil, nil, "")
	if err != nil {
		t.Fatalf("priority 模式应回退到第二把 Key，实得错误: %v", err)
	}
	if key.Name != "k2" {
		t.Fatalf("priority 应选 k2，实得 %q", key.Name)
	}
}

// TestFrozenKeyNeverSoftFallsBack 固化「冻结是硬约束：软回退也不放行」。
//
// 构造一个最容易误放行的局面：唯一可用的 k2 进入冷却，软回退会把冷却中的 Key 放行。
// 如果冻结被当成冷却处理（或软回退漏了冻结过滤），游标第一轮就会选中 k1。
func TestFrozenKeyNeverSoftFallsBack(t *testing.T) {
	pool, freezes := newFrozenPool(t, "round_robin")
	freezes.Freeze("p", "k1", frozenReasonText)
	pool.MarkFailure("m", "k2", intPtr(429), nil)
	if !pool.IsCoolingDown("m", "k2") {
		t.Fatal("准备阶段应让 k2 进入冷却")
	}

	for i := 0; i < 5; i++ {
		key, err := pool.NextKey("m", nil, nil, "")
		if err != nil {
			t.Fatalf("第 %d 次选路失败（软回退应放行冷却中的 k2）: %v", i+1, err)
		}
		if key.Name != "k2" {
			t.Fatalf("软回退也不能放行冻结的 k1，实得 %q", key.Name)
		}
		pool.ReleaseKey("m", key.Name)
	}
}

// TestAllCandidatesFrozenReturnsQuotaFrozen 固化「全部候选冻结」的错误语义。
//
// 三条文案要求：能看出是额度冻结、能看出是哪把 Key、能看出原因（窗口/重置时间）。
// 同时必须**不**退化成 ErrNoUsableKey——那是配置问题的说法，会把人引向配置检查，
// 而这里配置完全正确。
func TestAllCandidatesFrozenReturnsQuotaFrozen(t *testing.T) {
	pool, freezes := newFrozenPool(t, "round_robin")
	freezes.Freeze("p", "k1", frozenReasonText)
	freezes.Freeze("p", "k2", "7 天窗口已用尽（剩余 0%）")

	if got := pool.KeyCount("m"); got != 0 {
		t.Fatalf("全部冻结后 KeyCount: got %d want 0", got)
	}
	// 全部 Key 冻结只影响选路，不影响模型清单：清单抖动会让客户端误删模型。
	if !containsString(pool.AvailableModelIDs(nil), "m") {
		t.Fatal("全部 Key 冻结时模型仍应留在 /v1/models 清单里")
	}

	_, err := pool.NextKey("m", nil, nil, "")
	if !errors.Is(err, ErrQuotaFrozen) {
		t.Fatalf("全部冻结应返回 ErrQuotaFrozen，实得 %v", err)
	}
	if errors.Is(err, ErrNoUsableKey) {
		t.Fatalf("不应退化成 ErrNoUsableKey: %v", err)
	}
	message := err.Error()
	for _, want := range []string{
		"额度冻结", "模型 m", "p/k1", "p/k2", frozenReasonText,
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("错误文案缺少 %q，实得: %s", want, message)
		}
	}
	if strings.Contains(message, "没有可用 key") {
		t.Fatalf("冻结不应说成「没有可用 key」: %s", message)
	}
}

// TestThresholdFreezeWhitelist 固化「提前冻结可对白名单模型豁免，硬性耗尽不豁免」。
//
// 配置里白名单写别名也要生效：keypool 在 applyConfig 归一成真实模型 ID；同一把
// Key 上未进入白名单的模型照常被提前冻结挡住。
func TestThresholdFreezeWhitelist(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"quota_freeze_threshold_whitelist": ["alias-m"],
		"providers": {"p": {"base_url": "https://a.example", "keys": {"k": {"api_key": "1"}}}},
		"models": {
			"m": {"aliases": ["alias-m"], "targets": [
				{"provider": "p", "key": "k", "upstream_model": "u"}]},
			"other": {"targets": [
				{"provider": "p", "key": "k", "upstream_model": "u"}]}
		}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })
	freezes := newFakeQuotaFreezes()
	pool.SetQuotaFreezes(freezes)
	freezes.FreezeThreshold("p", "k", frozenReasonText)

	if got := pool.KeyCount("m"); got != 1 {
		t.Fatalf("白名单模型的提前冻结 Key 应可继续使用: KeyCount=%d", got)
	}
	if key, err := pool.NextKey("m", nil, nil, ""); err != nil || key.Name != "k" {
		t.Fatalf("白名单模型应能选中 k: key=%+v err=%v", key, err)
	}
	if key, err := pool.KeyByName("m", "k", nil); err != nil || key.Name != "k" {
		t.Fatalf("白名单模型的点名路径应能取到 k: key=%+v err=%v", key, err)
	}
	if err := pool.AcquireKey("m", "k"); err != nil {
		t.Fatalf("白名单模型的 AcquireKey 应放行 k: %v", err)
	}
	if got := pool.KeyCount("other"); got != 0 {
		t.Fatalf("非白名单模型仍应被提前冻结挡住: KeyCount=%d", got)
	}
	if _, err := pool.NextKey("other", nil, nil, ""); !errors.Is(err, ErrQuotaFrozen) {
		t.Fatalf("非白名单模型应返回 ErrQuotaFrozen，实得 %v", err)
	}

	// 额度完全耗尽：白名单也必须失效，不能把已经见底的 Key 继续打出去。
	freezes.Freeze("p", "k", "5 小时窗口已用尽（剩余 0%）")
	if got := pool.KeyCount("m"); got != 0 {
		t.Fatalf("硬性耗尽后白名单模型也必须停用: KeyCount=%d", got)
	}
	if _, err := pool.NextKey("m", nil, nil, ""); !errors.Is(err, ErrQuotaFrozen) {
		t.Fatalf("硬性耗尽后应返回 ErrQuotaFrozen，实得 %v", err)
	}
}

// TestOnlyFirstFailsWhenFirstKeyFrozen 固化 only_first 的钉死语义。
//
// only_first 的契约是「只用第一把」：第一把被冻结时直接失败，**不能**悄悄把流量
// 挪给第二把——那等于把配置意图偷改成「用第一把能用的」。
func TestOnlyFirstFailsWhenFirstKeyFrozen(t *testing.T) {
	pool, freezes := newFrozenPool(t, "only_first")
	freezes.Freeze("p", "k1", frozenReasonText)

	_, err := pool.NextKey("m", nil, nil, "")
	if !errors.Is(err, ErrQuotaFrozen) {
		t.Fatalf("only_first 第一把被冻结时应失败，实得 %v", err)
	}
	if !strings.Contains(err.Error(), "p/k1") || !strings.Contains(err.Error(), frozenReasonText) {
		t.Fatalf("文案应指出被冻结的是 k1 及其原因，实得: %v", err)
	}
	if got := pool.ActiveCount("m", "k2"); got != 0 {
		t.Fatalf("失败路径不该给 k2 记在途计数，实得 %d", got)
	}

	// 解冻后立刻恢复：钉死的是第一把，恢复正常后仍用它。
	freezes.Release("p", "k1")
	key, err := pool.NextKey("m", nil, nil, "")
	if err != nil {
		t.Fatalf("解冻后应恢复选路: %v", err)
	}
	if key.Name != "k1" {
		t.Fatalf("only_first 解冻后应回到第一把，实得 %q", key.Name)
	}
}

// TestOnlyFirstIgnoresFrozenLaterKey 固化 only_first 只看第一把：
// 后面的 Key 被冻结不该让第一把的正常选路失败。
func TestOnlyFirstIgnoresFrozenLaterKey(t *testing.T) {
	pool, freezes := newFrozenPool(t, "only_first")
	freezes.Freeze("p", "k2", frozenReasonText)

	key, err := pool.NextKey("m", nil, nil, "")
	if err != nil {
		t.Fatalf("第一把未冻结时应正常选中: %v", err)
	}
	if key.Name != "k1" {
		t.Fatalf("only_first 应选 k1，实得 %q", key.Name)
	}
}

// TestNamedKeyPathsRejectFrozenKey 固定点名路径不能绕过冻结。
//
// `model[key]` 语法与 AcquireKey（selectKey 的记数入口）都必须失败：绕过等于冻结在
// 显式指定 Key 时不存在，而这正是最容易被「我就要用这把」撞上的路径。
func TestNamedKeyPathsRejectFrozenKey(t *testing.T) {
	pool, freezes := newFrozenPool(t, "round_robin")
	freezes.Freeze("p", "k1", frozenReasonText)

	if _, err := pool.KeyByName("m", "k1", nil); !errors.Is(err, ErrQuotaFrozen) {
		t.Fatalf("KeyByName 点名冻结 Key 应返回 ErrQuotaFrozen，实得 %v", err)
	} else if !strings.Contains(err.Error(), "p/k1") || !strings.Contains(err.Error(), frozenReasonText) {
		t.Fatalf("KeyByName 文案应含 provider/key 与原因，实得: %v", err)
	}
	if err := pool.AcquireKey("m", "k1"); !errors.Is(err, ErrQuotaFrozen) {
		t.Fatalf("AcquireKey 点名冻结 Key 应返回 ErrQuotaFrozen，实得 %v", err)
	}
	if got := pool.ActiveCount("m", "k1"); got != 0 {
		t.Fatalf("被拒的点名路径不该记在途计数，实得 %d", got)
	}

	// 未冻结的第二把照旧可用（点名路径不能因别的 Key 被冻结而整体失效）。
	if _, err := pool.KeyByName("m", "k2", nil); err != nil {
		t.Fatalf("未冻结的 k2 应仍可点名: %v", err)
	}
	if err := pool.AcquireKey("m", "k2"); err != nil {
		t.Fatalf("未冻结的 k2 应仍可计数: %v", err)
	}
	pool.ReleaseKey("m", "k2")

	// 解冻后点名路径恢复。
	freezes.Release("p", "k1")
	if _, err := pool.KeyByName("m", "k1", nil); err != nil {
		t.Fatalf("解冻后 KeyByName 应恢复: %v", err)
	}
}

// TestSetQuotaFreezesNilRestoresCandidates 固化「nil = 不启用」：注入冻结表后再置 nil
// （嵌入场景关闭机制、或装配层回退）必须完全回到没有该机制时的行为。
func TestSetQuotaFreezesNilRestoresCandidates(t *testing.T) {
	pool := New(freezeModelConfig(t, "round_robin"), nil, func() float64 { return 1000 })
	freezes := newFakeQuotaFreezes()
	freezes.Freeze("p", "k1", frozenReasonText)
	pool.SetQuotaFreezes(freezes)
	if got := pool.KeyCount("m"); got != 1 {
		t.Fatalf("注入冻结表后 KeyCount: got %d want 1", got)
	}

	pool.SetQuotaFreezes(nil)
	if got := pool.KeyCount("m"); got != 2 {
		t.Fatalf("置 nil 后 KeyCount: got %d want 2", got)
	}
	if got := pool.FrozenKeyCount("m", nil); got != 0 {
		t.Fatalf("置 nil 后 FrozenKeyCount: got %d want 0", got)
	}
	// 游标还没动过，round_robin 的第一把就是 k1：置 nil 后必须能选中它。
	key, err := pool.NextKey("m", nil, nil, "")
	if err != nil {
		t.Fatalf("置 nil 后应正常选路: %v", err)
	}
	if key.Name != "k1" {
		t.Fatalf("置 nil 后应能选中 k1，实得 %q", key.Name)
	}
}

// TestSetQuotaFreezesConcurrentWithSelection 固化注入与选路的并发安全（配 -race 跑）。
//
// 装配层在启动/热重载时注入，而选路在任意请求 goroutine 上：两者不同锁就是数据竞争，
// 而这种竞争在生产上表现为「偶发读到 nil 指针」。
func TestSetQuotaFreezesConcurrentWithSelection(t *testing.T) {
	pool := New(freezeModelConfig(t, "round_robin"), nil, func() float64 { return 1000 })
	first := newFakeQuotaFreezes()
	second := newFakeQuotaFreezes()
	second.Freeze("p", "k2", frozenReasonText)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if i%2 == 0 {
				pool.SetQuotaFreezes(first)
			} else {
				pool.SetQuotaFreezes(second)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			key, err := pool.NextKey("m", nil, nil, "")
			if err == nil {
				pool.ReleaseKey("m", key.Name)
			}
			_ = pool.KeyCount("m")
			_ = pool.FrozenKeyCount("m", nil)
			_ = pool.AvailableModelIDs(nil)
		}
	}()
	wg.Wait()
}

// TestQuotaFrozenErrorReportsOnlyAllFrozen 固化诊断接口的判据。
//
// 代理的模型校验阶段靠它把「额度用完了」与「模型未配置」分开，因此它必须严格：
// 还有一把可用 Key 就不能报冻结，未知模型（压根没有启用候选）也不能报冻结。
func TestQuotaFrozenErrorReportsOnlyAllFrozen(t *testing.T) {
	pool, freezes := newFrozenPool(t, "round_robin")
	if err := pool.QuotaFrozenError("m", nil); err != nil {
		t.Fatalf("没有冻结时不该报错: %v", err)
	}
	if err := pool.QuotaFrozenError("不存在的模型", nil); err != nil {
		t.Fatalf("未知模型（没有启用候选）不属于额度冻结: %v", err)
	}

	freezes.Freeze("p", "k1", frozenReasonText)
	if err := pool.QuotaFrozenError("m", nil); err != nil {
		t.Fatalf("只冻结一把时不该报错（k2 仍可用）: %v", err)
	}

	freezes.Freeze("p", "k2", "7 天窗口已用尽（剩余 0%）")
	err := pool.QuotaFrozenError("m", nil)
	if !errors.Is(err, ErrQuotaFrozen) {
		t.Fatalf("全部冻结应返回 ErrQuotaFrozen，实得 %v", err)
	}
	for _, want := range []string{"p/k1", "p/k2", frozenReasonText} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("诊断文案缺少 %q，实得: %v", want, err)
		}
	}

	// 解冻任意一把即恢复 nil：诊断不能滞后于真实状态。
	freezes.Release("p", "k2")
	if err := pool.QuotaFrozenError("m", nil); err != nil {
		t.Fatalf("解冻一把后不该再报冻结: %v", err)
	}
}

// containsString 报告切片里是否包含目标字符串。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
