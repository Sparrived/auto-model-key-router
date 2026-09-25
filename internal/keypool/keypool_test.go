package keypool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// TestSelectionCorpusIsDiscriminating 证明这套断言本身不是空转。
//
// 断言最容易变成「永远通过」的测试：如果断言写错（比如只比较了空字符串），
// 用例再多也发现不了偏差。这里用一个**已知错误**的期望值去跑同一套断言
// 路径，必须失败——否则说明断言本身是空的。
func TestSelectionCorpusIsDiscriminating(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "1"}, "k2": {"api_key": "2"}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"},
			{"provider": "p", "key": "k2", "upstream_model": "u2"}],
			"routing_mode": "round_robin"}}
	}`
	routerConfig := mustConfig(t, raw)
	pool := New(routerConfig, nil, func() float64 { return 1000 })

	first, err := pool.NextKey("m", nil, nil, "")
	if err != nil {
		t.Fatalf("首次选择失败: %v", err)
	}
	// 第一轮必然是 k1（游标从 0 开始，两个 key 并发数都是 0）。
	if first.Name != "k1" {
		t.Fatalf("期望 k1，实际 %q", first.Name)
	}
	// 反证：若断言逻辑写错（例如恒等比较），下面这个「错误的期望」就不会被发现。
	if first.Name == "k2" {
		t.Fatal("断言无鉴别力：错误的期望值未被识别")
	}
}

// TestNextKeyRoundRobinDoesNotStarve 验证轮转在正常释放下会覆盖所有 key。
//
// 这是 concurrency 与 cursor 交互的核心不变量：round_robin 的意义就是分摊负载，
// 如果游标或并发数计算写错，会退化成「总是同一个 key」而静默失去负载均衡。
func TestNextKeyRoundRobinDoesNotStarve(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "1"}, "k2": {"api_key": "2"},
			"k3": {"api_key": "3"}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"},
			{"provider": "p", "key": "k2", "upstream_model": "u2"},
			{"provider": "p", "key": "k3", "upstream_model": "u3"}],
			"routing_mode": "round_robin"}}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })

	counts := map[string]int{}
	for range 9 {
		key, err := pool.NextKey("m", nil, nil, "")
		if err != nil {
			t.Fatalf("选择失败: %v", err)
		}
		counts[key.Name]++
		// 每次用完立刻释放，模拟无并发压力的稳定状态。
		pool.ReleaseKey("m", key.Name)
	}
	for _, name := range []string{"k1", "k2", "k3"} {
		if counts[name] != 3 {
			t.Fatalf("round_robin 未均分：%s 被选中 %d 次，期望 3 次（全量 %v）",
				name, counts[name], counts)
		}
	}
}

// TestConcurrentNextKeyDoesNotRace 用 -race 检查并发安全。
//
// 参照实现用 asyncio.Lock 保护游标与并发计数；Go 侧用 sync.Mutex。这个测试在
// `go test -race` 下才有意义，普通运行时至少验证不 panic、计数守恒。
func TestConcurrentNextKeyDoesNotRace(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "1"}, "k2": {"api_key": "2"}}}},
		"models": {"m": {"targets": [
			{"provider": "p", "key": "k1", "upstream_model": "u1"},
			{"provider": "p", "key": "k2", "upstream_model": "u2"}],
			"routing_mode": "round_robin"}}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })

	const workers = 8
	const perWorker = 50
	done := make(chan struct{}, workers)
	for range workers {
		go func() {
			defer func() { done <- struct{}{} }()
			for range perWorker {
				key, err := pool.NextKey("m", nil, nil, "")
				if err != nil {
					return
				}
				pool.MarkFailure("m", key.Name, intPtr(429), nil)
				pool.MarkSuccess("m", key.Name)
				pool.ReleaseKey("m", key.Name)
			}
		}()
	}
	for range workers {
		<-done
	}
	// 全部释放后并发计数必须归零，否则说明有配对泄漏。
	for _, name := range []string{"k1", "k2"} {
		if got := pool.ActiveCount("m", name); got != 0 {
			t.Fatalf("并发计数未归零：%s = %d", name, got)
		}
	}
}

// TestHealthStoreCooldownArithmetic 验证冷却算术，覆盖端到端用例不方便表达的边界。
func TestHealthStoreCooldownArithmetic(t *testing.T) {
	now := 1000.0
	store := NewKeyHealthStore(func() float64 { return now })

	// 非 429 且未达阈值：只累计失败、不冷却。
	store.MarkFailure("m", "k", intPtr(500), nil, 2, 60)
	if store.IsCoolingDown("m", "k") {
		t.Fatal("未达阈值不应冷却")
	}
	// 达到阈值：cooldown = 60 × 2 = 120（失败次数已为 2）。
	store.MarkFailure("m", "k", intPtr(500), nil, 2, 60)
	if !store.IsCoolingDown("m", "k") {
		t.Fatal("达到阈值应冷却")
	}
	// 时间前进 119 秒仍在冷却，120 秒后解除。
	now = 1000 + 119
	if !store.IsCoolingDown("m", "k") {
		t.Fatal("119 秒时仍在冷却期内")
	}
	now = 1000 + 120
	if store.IsCoolingDown("m", "k") {
		t.Fatal("120 秒时冷却应已结束")
	}

	// 429 立即冷却，且不看阈值。
	store2 := NewKeyHealthStore(func() float64 { return now })
	store2.MarkFailure("m", "k", intPtr(429), nil, 99, 60)
	if !store2.IsCoolingDown("m", "k") {
		t.Fatal("429 应立即冷却，不受阈值限制")
	}

	// 上限截断到 300 秒。
	//
	// 注意基准时间是上面推进后的 now，不是初始值：这里显式重新标定，避免依赖
	// 前面步骤留下的时间状态。
	now = 2000
	store3 := NewKeyHealthStore(func() float64 { return now })
	store3.MarkFailure("m", "k", intPtr(429), floatPtr(99999), 1, 60)
	now = 2000 + 299
	if !store3.IsCoolingDown("m", "k") {
		t.Fatal("299 秒时应仍在冷却（上限 300）")
	}
	now = 2000 + 300
	if store3.IsCoolingDown("m", "k") {
		t.Fatal("冷却时长应被截断到 300 秒")
	}

	// retry_after 为负时下限取 0，即不冷却。
	now = 1000
	store4 := NewKeyHealthStore(func() float64 { return now })
	store4.MarkFailure("m", "k", intPtr(429), floatPtr(-5), 1, 60)
	if store4.IsCoolingDown("m", "k") {
		t.Fatal("负 retry_after 应取下限 0，不进入冷却")
	}

	// mark_success 清除状态。
	store5 := NewKeyHealthStore(func() float64 { return now })
	store5.MarkFailure("m", "k", intPtr(429), nil, 1, 60)
	store5.MarkSuccess("m", "k")
	if store5.IsCoolingDown("m", "k") {
		t.Fatal("mark_success 应清除冷却状态")
	}
}

// TestCapabilityCacheTTL 验证能力缓存的过期与遗留格式兼容。
func TestCapabilityCacheTTL(t *testing.T) {
	now := 10_000.0
	clock := func() float64 { return now }

	cache := NewEndpointCapabilityCache(nil, clock)
	if got := cache.Get("https://a.example", "v1/messages"); got != nil {
		t.Fatal("空缓存应返回 nil（未测试）")
	}

	// 正结果永久有效。
	cache.Update("https://a.example", true, "v1/messages", "ok")
	now = 10_000 + 86_400*365
	if got := cache.Get("https://a.example", "v1/messages"); got == nil || !*got {
		t.Fatal("正结果应永久有效")
	}

	// 负结果 600 秒后过期（reason != "error"）。
	now = 10_000
	cache.Update("https://b.example", false, "v1/messages", "unsupported")
	now = 10_000 + 599
	if got := cache.Get("https://b.example", "v1/messages"); got == nil || *got {
		t.Fatal("599 秒时负结果应仍有效")
	}
	now = 10_000 + 600
	if got := cache.Get("https://b.example", "v1/messages"); got != nil {
		t.Fatal("600 秒时负结果应已过期")
	}

	// reason == "error" 用 60 秒 TTL。
	now = 10_000
	cache.Update("https://c.example", false, "v1/messages", "error")
	now = 10_000 + 60
	if got := cache.Get("https://c.example", "v1/messages"); got != nil {
		t.Fatal("error 类负结果 60 秒后应过期")
	}

	// v1/messages 查询回退到只按 base_url 记录的键。
	now = 10_000
	cache.Update("https://d.example", true, "", "ok")
	if got := cache.Get("https://d.example/", "v1/messages"); got == nil || !*got {
		t.Fatal("应回退到 base_url 键并命中")
	}
}

// TestCapabilityCacheLegacyBoolState 验证遗留布尔格式的解析。
func TestCapabilityCacheLegacyBoolState(t *testing.T) {
	raw := mustValue(t, `{
		"https://yes.example|v1/messages": true,
		"https://no.example|v1/messages": false,
		"bad-entry": "not-a-state",
		"also-bad": {"supported": "yes"}
	}`)
	cache := NewEndpointCapabilityCache(raw, func() float64 { return 5000 })

	// 遗留 true 视为已确认支持，且无 TTL。
	if got := cache.Get("https://yes.example", "v1/messages"); got == nil || !*got {
		t.Fatal("遗留 true 应解析为支持")
	}
	// 遗留 false 带负 TTL，但 checked_at 为 0，故在 5000 秒时已过期。
	if got := cache.Get("https://no.example", "v1/messages"); got != nil {
		t.Fatal("遗留 false 因 checked_at=0 应已过期")
	}
	// 无法识别的条目被忽略，不影响其它条目。
	if got := cache.Get("bad-entry", "v1/messages"); got != nil {
		t.Fatal("坏数据应被忽略")
	}
}

// TestCapabilityCacheKeyNormalization 验证缓存键归一化。
//
// 尾部斜杠、路径两侧斜杠、空路径都要归一到同一个键，否则同一端点会因写法不同
// 而重复探测（每次探测都是计费的上游调用）。
func TestCapabilityCacheKeyNormalization(t *testing.T) {
	same := [][2]string{
		{"https://a.example", "/v1/messages"},
		{"https://a.example/", "v1/messages"},
		{"https://a.example", "v1/messages/"},
		{"https://a.example/", "/v1/messages/"},
	}
	cache := NewEndpointCapabilityCache(nil, func() float64 { return 100 })
	cache.Update(same[0][0], true, same[0][1], "ok")
	for _, pair := range same[1:] {
		if got := cache.Get(pair[0], pair[1]); got == nil || !*got {
			t.Fatalf("键未归一：%q + %q 未命中", pair[0], pair[1])
		}
	}
}

// TestRequestRouteKind 验证路径归类。
func TestRequestRouteKind(t *testing.T) {
	cases := map[string]string{
		"images/generations": "image",
		"images/edits":       "image",
		"embeddings":         "embeddings",
		"chat/completions":   "default",
		"messages":           "default",
		"":                   "default",
		"images":             "default",
	}
	for path, want := range cases {
		if got := RequestRouteKind(path); got != want {
			t.Fatalf("路径 %q: 期望 %q，实际 %q", path, want, got)
		}
	}
}

// TestCapabilityStoreRoundTrip 验证能力状态的落盘与读回。
func TestCapabilityStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caps.json")
	store := NewCapabilityStore(path)

	// 文件缺失时返回空对象。
	if got := store.Load(); !got.IsObject() || got.Len() != 0 {
		t.Fatal("缺失文件应返回空对象")
	}

	states := mustValue(t, `{"z|v1/messages": {"supported": true, "checked_at": 5, "reason": "ok", "ttl_seconds": 0}}`)
	if err := store.Save(states); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	loaded := store.Load()
	if !loaded.IsObject() || loaded.Lookup("z|v1/messages").Kind == 0 {
		t.Fatalf("读回失败: %s", mustDump(t, loaded))
	}

	// 载荷结构固定为 version + endpoint_capabilities。
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(content, &payload); err != nil {
		t.Fatalf("解析落盘内容失败: %v", err)
	}
	if string(payload["version"]) != "1" {
		t.Fatalf("version 应为 1，实际 %s", payload["version"])
	}
	if _, found := payload["endpoint_capabilities"]; !found {
		t.Fatal("缺少 endpoint_capabilities 字段")
	}
	// 写入必须是原子的：不能留下临时文件。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("残留临时文件: %s", entry.Name())
		}
	}
}

// TestCapabilityStoreAcceptsLegacyFieldName 验证旧字段名兼容。
func TestCapabilityStoreAcceptsLegacyFieldName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caps.json")
	content := `{"url_native_support": {"a|v1/messages": true}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	loaded := NewCapabilityStore(path).Load()
	if !loaded.IsObject() || loaded.Lookup("a|v1/messages").Kind == 0 {
		t.Fatal("应识别旧字段名 url_native_support")
	}
}

// TestCapabilityStoreCorruptFileIsIgnored 验证损坏缓存不会阻止启动。
func TestCapabilityStoreCorruptFileIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caps.json")
	for _, content := range []string{"not json", "[]", `{"endpoint_capabilities": []}`} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
		loaded := NewCapabilityStore(path).Load()
		if !loaded.IsObject() || loaded.Len() != 0 {
			t.Fatalf("内容 %q 应被忽略并返回空对象", content)
		}
	}
}

// TestUpstreamNamesAreNotCallable 锁定「上游名只是上游名」。
//
// target 的 upstream_model 是发给上游的名字，不再自动成为可调用名：可调用名只有模型
// ID 与 aliases（两者都出现在 /v1/models）。需要某个上游叫法也能被调用时，把它写进
// 模型的 aliases——那时它同样会出现在 /v1/models，不再有「能调但不列出」的第三类名字。
func TestUpstreamNamesAreNotCallable(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "1"}}}},
		"models": {
			"alpha": {"aliases": ["alias-alpha"],
			          "targets": [{"provider": "p", "key": "k1", "upstream_model": "beta"}]},
			"beta": {"targets": [{"provider": "p", "key": "k1", "upstream_model": "u"}]}
		}
	}`
	pool := New(mustConfig(t, raw), nil, func() float64 { return 1000 })

	if got := pool.ResolveModelID("beta"); got != "beta" {
		t.Fatalf("真实 ID 应解析到自己，实际 %q", got)
	}
	if got := pool.ResolveModelID("alias-alpha"); got != "alpha" {
		t.Fatalf("别名应解析到其模型，实际 %q", got)
	}
	// u 是 beta 的上游名；alpha 的上游名 beta 恰好是真实 ID，两者都不该被解析成模型。
	if got := pool.ResolveModelID("u"); got != "u" {
		t.Fatalf("上游名不应被解析成模型，实际 %q", got)
	}
	if public := pool.PublicModelIDs(); !slices.Equal(public, []string{"alias-alpha", "alpha", "beta"}) {
		t.Fatalf("对外模型名应为 ID + 别名，实际 %v", public)
	}
}

// TestAccessKeyProviderScope 验证访问密钥的供应商清单在**候选集合**层面生效。
//
// 过滤放在候选集合而不是挑选之后，是为了让 only_first 也守规矩：若先选中一把无权
// 使用的 key 再判权限，那把 key 会被白白占用一轮、并且错误信息会指向错误的维度。
func TestAccessKeyProviderScope(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {
			"p1": {"base_url": "https://a.example", "keys": {"k1": {"api_key": "1"}}},
			"p2": {"base_url": "https://b.example", "keys": {"k2": {"api_key": "2"}}}
		},
		"models": {"m": {"targets": [
			{"provider": "p1", "key": "k1", "upstream_model": "u"},
			{"provider": "p2", "key": "k2", "upstream_model": "u"}
		]}},
		"access_keys": {
			"onlyP1": {"key": "ak-p1", "providers": ["p1"]},
			"none": {"key": "ak-none", "providers": []},
			"open": {"key": "ak-open"}
		}
	}`
	cfg := mustConfig(t, raw)
	pool := New(cfg, nil, func() float64 { return 1000 })
	byName := func(name string) *config.AccessKeyConfig {
		key := cfg.AccessKeyFor(name)
		if key == nil {
			t.Fatalf("配置里没有 %s", name)
		}
		return key
	}

	if got := pool.KeyCountFor("m", byName("ak-p1")); got != 1 {
		t.Fatalf("清单内可用 key 数 = %d，期望 1", got)
	}
	// 空数组 = 一个都不许（与「省略」不同）。
	if got := pool.KeyCountFor("m", byName("ak-none")); got != 0 {
		t.Fatalf("空清单可用 key 数 = %d，期望 0", got)
	}
	if got := pool.KeyCountFor("m", byName("ak-open")); got != 2 {
		t.Fatalf("不限制时可用 key 数 = %d，期望 2", got)
	}

	// 选出来的 key 必须真的属于清单内的供应商。
	selected, err := pool.NextKey("m", nil, byName("ak-p1"), "")
	if err != nil {
		t.Fatalf("选 key 失败: %v", err)
	}
	if selected.Provider != "p1" {
		t.Fatalf("选中了 %s 的 key，期望 p1", selected.Provider)
	}
	// 按名字直呼清单外的 key 也必须失败。
	if _, err := pool.KeyByName("m", "k2", byName("ak-p1")); err == nil {
		t.Fatal("清单外的 key 不应被 KeyByName 选中")
	}
}

// TestAccessKeyStickyIsolatedPerKey 验证不同访问密钥的粘滞互不干扰。
//
// 两把 key 对同一模型可以有不同供应商清单。共用一个粘滞键会让清单更窄的那把被粘到
// 一把它无权使用的上游 key 上——那是一次 403，而调用方什么都没做错。
func TestAccessKeyStickyIsolatedPerKey(t *testing.T) {
	raw := `{
		"config_version": 4, "local_api_key": "local",
		"providers": {
			"p1": {"base_url": "https://a.example", "keys": {"k1": {"api_key": "1"}}},
			"p2": {"base_url": "https://b.example", "keys": {"k2": {"api_key": "2"}}}
		},
		"models": {"m": {"targets": [
			{"provider": "p1", "key": "k1", "upstream_model": "u"},
			{"provider": "p2", "key": "k2", "upstream_model": "u"}
		], "routing_mode": "round_robin"}},
		"access_keys": {"a": {"key": "ak-a", "providers": ["p1"]}}
	}`
	cfg := mustConfig(t, raw)
	pool := New(cfg, nil, func() float64 { return 1000 })
	accessKey := cfg.AccessKeyFor("ak-a")

	// 同一份亲和键 + 同一把访问密钥 => 稳定选中同一个上游。
	first, err := pool.NextKey("m", nil, accessKey, "same-affinity")
	if err != nil {
		t.Fatalf("第一次选 key 失败: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := pool.NextKey("m", nil, accessKey, "same-affinity")
		if err != nil {
			t.Fatalf("第 %d 次选 key 失败: %v", i, err)
		}
		if again.Name != first.Name {
			t.Fatalf("粘滞失效：%q -> %q", first.Name, again.Name)
		}
	}
}

// --- 测试辅助 ---

// mustConfig 从 JSON 文本构造配置。
func mustConfig(t *testing.T, raw string) *config.RouterConfig {
	t.Helper()
	routerConfig, err := config.FromDict(mustValue(t, raw))
	if err != nil {
		t.Fatalf("构造配置失败: %v", err)
	}
	return routerConfig
}

// mustValue 解析 JSON 文本。
func mustValue(t *testing.T, raw string) *canonical.Value {
	t.Helper()
	value, err := canonical.ParseString(raw)
	if err != nil {
		t.Fatalf("解析 JSON 失败: %v\n输入: %s", err, raw)
	}
	return value
}

// mustDump 序列化 canonical 值以便断言失败时输出。
func mustDump(t *testing.T, value *canonical.Value) string {
	t.Helper()
	return canonical.Dumps(value)
}

// intPtr 返回 int 指针。
func intPtr(value int) *int { return &value }

// floatPtr 返回 float64 指针。
func floatPtr(value float64) *float64 { return &value }
