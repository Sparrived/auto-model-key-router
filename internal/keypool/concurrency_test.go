package keypool

import (
	"errors"
	"strings"
	"testing"
)

// 本文件锁定「并发上限」的选路语义。
//
// 与余额限流（balance_test.go）的断言风格一致：只断言**可观察行为**（选到谁、
// 报什么错、释放后能不能恢复），不直接读内部计数表——内部表怎么实现都可以，
// 但「上限到了就不再放行、释放后立刻恢复」是对外的承诺。

// TestProviderConcurrencyLimitSpansKeys 验证供应商上限跨 Key 累计。
//
// 这是供应商级限流最容易写错的地方：如果计数按 Key 各算一份，配 2 的供应商上限
// 会在两把 Key 上变成 4。断言方式是把上限设成 2、两把 Key 都不设上限，然后连续
// 取三次——第三次必须被挡下，且文案要指出是供应商满了。
func TestProviderConcurrencyLimitSpansKeys(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "max_concurrency": 2, "keys": {
			"k1": {"api_key": "1"}, "k2": {"api_key": "2"}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"},
			{"provider": "p", "key": "k2", "upstream_model": "u2"}],
			"routing_mode": "round_robin"}}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })

	first, err := pool.NextKey("m", nil, nil, "")
	if err != nil {
		t.Fatalf("第 1 个请求应当成功: %v", err)
	}
	second, err := pool.NextKey("m", nil, nil, "")
	if err != nil {
		t.Fatalf("第 2 个请求应当成功: %v", err)
	}
	if first.Name == second.Name {
		t.Fatalf("两个请求应落在不同的 key 上（round_robin），实际都是 %q", first.Name)
	}

	if _, err := pool.NextKey("m", nil, nil, ""); !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("第 3 个请求应被供应商上限挡下，实际 err = %v", err)
	} else if !strings.Contains(err.Error(), "供应商 p 已达上限 2") {
		t.Fatalf("文案应指明是供应商满了，实际: %v", err)
	}

	// 释放一个在途请求后立刻恢复：上限是「在途数」而不是累计配额。
	pool.ReleaseKey("m", first.Name)
	if _, err := pool.NextKey("m", nil, nil, ""); err != nil {
		t.Fatalf("释放后应当能再取一个: %v", err)
	}
}

// TestKeyConcurrencyLimitSpansModels 验证 Key 级上限按**上游 Key** 计。
//
// 同一把上游 Key 被两个模型引用时，它承受的是两个模型叠加的并发；按 (模型, key)
// 各算一份会让「单 Key 上限 1」在两个模型上变成 2——这正是上游账号被打满的路径。
func TestKeyConcurrencyLimitSpansModels(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "1", "max_concurrency": 1}}}},
		"models": {
			"m1": {"targets": [{"provider": "p", "key": "k1", "upstream_model": "u1"}]},
			"m2": {"targets": [{"provider": "p", "key": "k1", "upstream_model": "u2"}]}}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })

	if _, err := pool.NextKey("m1", nil, nil, ""); err != nil {
		t.Fatalf("m1 的第一个请求应当成功: %v", err)
	}
	if _, err := pool.NextKey("m2", nil, nil, ""); !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("同一个上游 Key 在另一个模型上也应被挡下，实际 err = %v", err)
	} else if !strings.Contains(err.Error(), "候选 Key 均已达各自上限") {
		t.Fatalf("Key 级用尽的文案不对: %v", err)
	}

	// 点名这把 key 的路径同样受限：绕过它的后果是上限在 `model[key]` 语法下不存在。
	if err := pool.AcquireKey("m2", "k1"); !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("点名路径也应被挡下，实际 err = %v", err)
	}
	// 释放之后两级计数都要回到可用状态（漏减任何一层都会让这把 key 永久占位）。
	pool.ReleaseKey("m1", "k1")
	if err := pool.AcquireKey("m2", "k1"); err != nil {
		t.Fatalf("释放后点名路径应当恢复: %v", err)
	}
	pool.ReleaseKey("m2", "k1")
	if _, err := pool.NextKey("m2", nil, nil, ""); err != nil {
		t.Fatalf("全部释放后选路应当恢复: %v", err)
	}
}

// TestConcurrencyLimitNeverSoftFallsBack 验证并发约束在软回退里也不放行。
//
// 唯一候选同时处于冷却窗口内时，既有的「全冷却就放行一个」降级会把它交出去；
// 但达到并发上限的候选不能走这条路——放行一次就等于上限不存在。
func TestConcurrencyLimitNeverSoftFallsBack(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local", "key_failure_threshold": 1,
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "1", "max_concurrency": 1}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"}],
			"routing_mode": "round_robin"}}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })

	if _, err := pool.NextKey("m", nil, nil, ""); err != nil {
		t.Fatalf("第一个请求应当成功: %v", err)
	}
	// 先把唯一候选推进冷却窗口：此时「可用集合为空」有两条原因（冷却 + 并发满），
	// 而并发是硬约束，因此必须报并发而不是软回退给这把已满的 key。
	pool.MarkFailure("m", "k1", intPtr(429), nil)
	if !pool.IsCoolingDown("m", "k1") {
		t.Fatal("准备阶段应让 k1 进入冷却")
	}
	if _, err := pool.NextKey("m", nil, nil, ""); !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("已满的候选不该被软回退放行，实际 err = %v", err)
	}
}

// TestConcurrencyLimitAppliesToOnlyFirst 验证 only_first 模式不绕过上限。
//
// only_first 的语义是「只用第一把、失败就走人」，它不参与冷却/负载均衡的判断；
// 并发上限不属于调度策略，而是上游的硬约束，因此这个模式也必须挡住。
func TestConcurrencyLimitAppliesToOnlyFirst(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "1", "max_concurrency": 1},
			"k2": {"api_key": "2"}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"},
			{"provider": "p", "key": "k2", "upstream_model": "u2"}],
			"routing_mode": "only_first"}}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })

	if key, err := pool.NextKey("m", nil, nil, ""); err != nil || key.Name != "k1" {
		t.Fatalf("only_first 应选中 k1，实际 %v / %v", key.Name, err)
	}
	if _, err := pool.NextKey("m", nil, nil, ""); !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("only_first 也要受并发上限约束（不能落到 k2），实际 err = %v", err)
	}
}

// TestConcurrencyCountersSurviveHotReload 验证热重载不丢在途计数。
//
// 配置改动发生在请求之间，但在途请求不会因此结束：重载如果顺手把计数清零，正在流式返回
// 的请求就凭空让出了名额，上限只在「不重载」时才成立——而热重载恰恰是这个项目里最日常的
// 操作（WebUI 存一次配置就会触发）。
func TestConcurrencyCountersSurviveHotReload(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "max_concurrency": 2, "keys": {
			"k1": {"api_key": "1"}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"}]}}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })
	first, err := pool.NextKey("m", nil, nil, "")
	if err != nil {
		t.Fatalf("第 1 个请求应当成功: %v", err)
	}
	if _, err := pool.NextKey("m", nil, nil, ""); err != nil {
		t.Fatalf("第 2 个请求应当成功（上限 2）: %v", err)
	}
	if _, err := pool.NextKey("m", nil, nil, ""); !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("上限已满，应当被挡下，实际 err = %v", err)
	}

	// 只改基址，上限与 key 名不变，模拟一次普通的热重载。
	reloaded := strings.Replace(raw, "https://a.example", "https://b.example", 1)
	pool.ApplyConfig(mustConfig(t, reloaded))

	if _, err := pool.NextKey("m", nil, nil, ""); !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("热重载后两个在途请求应继续占名额，实际 err = %v", err)
	}
	pool.ReleaseKey("m", first.Name)
	if _, err := pool.NextKey("m", nil, nil, ""); err != nil {
		t.Fatalf("释放一个之后应当能再取一个: %v", err)
	}
}

// TestConcurrencyLimitZeroMeansUnlimited 钉住 0/缺省的语义。
//
// 配了 0 与没配必须是同一件事：一个「上限 0」如果被当成「一个都不许」，会让升级到
// 本功能后所有请求直接 429；反过来把缺省当成 0 也一样。两把 key 各取两次，没有
// 任何一次该被挡下。
func TestConcurrencyLimitZeroMeansUnlimited(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "max_concurrency": 0, "keys": {
			"k1": {"api_key": "1", "max_concurrency": 0}, "k2": {"api_key": "2"}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"},
			{"provider": "p", "key": "k2", "upstream_model": "u2"}],
			"routing_mode": "round_robin"}}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })
	for i := range 8 {
		if _, err := pool.NextKey("m", nil, nil, ""); err != nil {
			t.Fatalf("第 %d 个请求不该被挡下: %v", i+1, err)
		}
	}
}
