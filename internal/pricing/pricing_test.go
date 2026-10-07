package pricing

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// catalogFixture 是一份最小的 models.dev 形状目录：两个供应商重复列出同一个模型，
// 其中一家标 0 元。
//
// 这份夹具是**手写的形状样本**：价格目录来自外部服务、每周都在变，逐字节锁定
// 它没有意义（也无法复现没有网络时的上游状态）。真正需要钉住的是**挑选规则**，
// 那才是本包唯一容易算错的地方。
const catalogFixture = `{
  "free-provider": {
    "id": "free-provider",
    "models": {
      "claude-sonnet-4-6": {"id": "claude-sonnet-4-6", "cost": {"input": 0, "output": 0}},
      "gpt-4o": {"id": "gpt-4o", "cost": {"input": 0, "output": 0, "cache_read": 0}},
      "no-cost": {"id": "no-cost"},
      "null-cost": {"id": "null-cost", "cost": null}
    }
  },
  "real-provider": {
    "id": "real-provider",
    "models": {
      "claude-sonnet-4-6": {"id": "claude-sonnet-4-6", "cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75}},
      "gpt-4o": {"id": "gpt-4o", "cost": {"input": 2.5, "output": 10}},
      "gpt-4o-mini": {"id": "gpt-4o-mini", "cost": {"input": 0.15, "output": 0.6}}
    }
  },
  "expensive-provider": {
    "id": "expensive-provider",
    "models": {
      "claude-sonnet-4-6": {"id": "claude-sonnet-4-6", "cost": {"input": 3.75, "output": 18.75}},
      "MiXeD-CaSe": {"id": "MiXeD-CaSe", "cost": {"input": 1, "output": 2}}
    }
  },
  "broken-provider": {"id": "broken-provider"},
  "hostile-provider": {"models": "not-an-object"}
}`

func buildFixture(t *testing.T) map[string]Entry {
	t.Helper()
	parsed, err := canonical.ParseString(catalogFixture)
	if err != nil {
		t.Fatalf("解析夹具失败: %v", err)
	}
	return Build(parsed)
}

// TestBuildSkipsZeroPriceListings 是本包最重要的一条断言：标 0 元的挂名条目
// **不能**赢得比价。
//
// models.dev 上实测有 465 个模型被某些供应商标成 input/output 全 0（nvidia、kenari
// 等）。若按"取最小和"挑选，claude-sonnet-4-6、qwen3-max、glm-4.6 这些真实模型都会
// 变成 $0，成本页会显示一张全零的假账——比"没有定价"更糟，因为它看起来像真的。
func TestBuildSkipsZeroPriceListings(t *testing.T) {
	index := buildFixture(t)

	sonnet, ok := index["claude-sonnet-4-6"]
	if !ok {
		t.Fatal("claude-sonnet-4-6 未进索引")
	}
	if sonnet.Input != 3 || sonnet.Output != 15 {
		t.Errorf("claude-sonnet-4-6 = %v/%v，期望 3/15（跳过 0 元的挂名条目）",
			sonnet.Input, sonnet.Output)
	}
	if sonnet.CacheRead == nil || *sonnet.CacheRead != 0.3 {
		t.Errorf("claude-sonnet-4-6 的 cache_read = %v，期望 0.3", sonnet.CacheRead)
	}
	if sonnet.CacheWrite == nil || *sonnet.CacheWrite != 3.75 {
		t.Errorf("claude-sonnet-4-6 的 cache_write = %v，期望 3.75", sonnet.CacheWrite)
	}
}

// TestBuildAcceptsGenuinelyFreeModel 断言"所有供应商都报 0"时仍然承认它免费。
//
// 这是上一条的另一半：排除 0 元条目的依据是"存在有价条目"，而不是"0 元一律不要"。
// 真实的 :free 变体与 TTS/图像模型就是全 0，把它们丢掉会让界面显示"无定价"。
func TestBuildAcceptsGenuinelyFreeModel(t *testing.T) {
	parsed, err := canonical.ParseString(`{"p": {"models": {"gpt-4o": {"cost": {"input": 0, "output": 0}}}}}`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	entry, ok := Build(parsed)["gpt-4o"]
	if !ok {
		t.Fatal("全 0 的模型应当进索引")
	}
	if entry.Input != 0 || entry.Output != 0 {
		t.Errorf("gpt-4o = %v/%v，期望 0/0", entry.Input, entry.Output)
	}
}

// TestBuildPicksCheapestAmongPriced 断言有价条目之间取最便宜的（用户确认的口径）。
func TestBuildPicksCheapestAmongPriced(t *testing.T) {
	index := buildFixture(t)
	if entry := index["gpt-4o"]; entry.Input != 2.5 || entry.Output != 10 {
		t.Errorf("gpt-4o = %v/%v，期望 2.5/10（real 比 free 的 0 元条目更可信）",
			entry.Input, entry.Output)
	}
}

// TestBuildLowercasesKeys 断言索引键是小写：本地 upstream_model 的大小写不可控，
// 匹配必须大小写不敏感。
func TestBuildLowercasesKeys(t *testing.T) {
	index := buildFixture(t)
	entry, ok := index["mixed-case"]
	if !ok {
		t.Fatal("索引键应当是小写 mixed-case")
	}
	if entry.Input != 1 || entry.Output != 2 {
		t.Errorf("mixed-case = %v/%v，期望 1/2", entry.Input, entry.Output)
	}
	if _, exists := index["MiXeD-CaSe"]; exists {
		t.Error("索引里不应保留原始大小写的键")
	}
}

// TestBuildSkipsMissingCost 断言缺 cost / cost 为 null 的模型被跳过而不是记成 0。
func TestBuildSkipsMissingCost(t *testing.T) {
	index := buildFixture(t)
	for _, key := range []string{"no-cost", "null-cost"} {
		if _, ok := index[key]; ok {
			t.Errorf("%s 没有可用价格，不应进索引", key)
		}
	}
}

// TestBuildToleratesMalformedProviders 断言结构异常的供应商不会 panic 也不会污染索引。
func TestBuildToleratesMalformedProviders(t *testing.T) {
	index := buildFixture(t)
	// broken-provider 没有 models，hostile-provider 的 models 是字符串：两者都跳过，
	// 但同一份目录里其它供应商照常解析。
	if len(index) != 4 {
		t.Errorf("索引大小 = %d，期望 4（claude-sonnet-4-6/gpt-4o/gpt-4o-mini/mixed-case）", len(index))
	}
	for _, key := range []string{"", "not-an-object"} {
		if _, ok := index[key]; ok {
			t.Errorf("索引里出现了非法键 %q", key)
		}
	}
}

// TestBuildRejectsNonObject 断言顶层不是对象时返回空索引（而不是 nil panic）。
func TestBuildRejectsNonObject(t *testing.T) {
	parsed, err := canonical.ParseString(`[1, 2, 3]`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if index := Build(parsed); len(index) != 0 {
		t.Errorf("非对象顶层应当得到空索引，实得 %d 项", len(index))
	}
}

// TestSelectCheapestOrdering 直接钉住比对规则的每一档。
func TestSelectCheapestOrdering(t *testing.T) {
	zero := Entry{Input: 0, Output: 0}
	cheap := Entry{Input: 1, Output: 2}
	pricey := Entry{Input: 5, Output: 5}

	cases := []struct {
		name      string
		candidate Entry
		current   Entry
		want      bool
	}{
		{"有价取代免费", cheap, zero, true},
		{"免费不取代有价", zero, cheap, false},
		{"低价取代高价", cheap, pricey, true},
		{"高价不取代低价", pricey, cheap, false},
		{"等价不取代", cheap, cheap, false},
		{"零价之间取更小（都为 0 时同分）", zero, zero, false},
		{"只有 output 有价也算有价", Entry{Input: 0, Output: 1}, zero, true},
		{"只有 input 有价也算有价", Entry{Input: 1, Output: 0}, zero, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := selectCheapest(testCase.candidate, testCase.current); got != testCase.want {
				t.Errorf("selectCheapest(%+v, %+v) = %v，期望 %v",
					testCase.candidate, testCase.current, got, testCase.want)
			}
		})
	}
}

// —— 刷新与缓存 ——

// fakeFetcher 记录调用并按脚本作答。
type fakeFetcher struct {
	calls  []string // 每次调用的 etag
	result []FetchResult
	errs   []error
}

func (f *fakeFetcher) fetch(url, etag string, timeout time.Duration) (FetchResult, error) {
	f.calls = append(f.calls, etag)
	index := len(f.calls) - 1
	if index < len(f.result) {
		return f.result[index], nil
	}
	if index < len(f.errs) {
		return FetchResult{}, f.errs[index]
	}
	return FetchResult{}, errors.New("fakeFetcher: 没有更多脚本")
}

func newTestCatalog(fetcher Fetcher) *Catalog {
	catalog := New(fetcher)
	// 固定时钟：TTL 判定必须可复现。
	current := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	catalog.now = func() time.Time { return current }
	return catalog
}

// TestPayloadUnavailableBeforeFirstFetch 断言首次取回前是"不可用"而不是空目录。
//
// 这条决定了界面行为：不可用时成本列显示 "—"，而不是把成本算成 $0。
func TestPayloadUnavailableBeforeFirstFetch(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{{Body: []byte(catalogFixture), ETag: `W/"a"`}}}
	catalog := newTestCatalog(fake.fetch)

	if payload, ok := catalog.Payload(); ok || payload != nil {
		t.Errorf("取回前 Payload = (%q, %v)，期望 (nil, false)", payload, ok)
	}
}

// TestRefreshBuildsPayload 断言成功取回后载荷可用，且形状符合前端契约。
func TestRefreshBuildsPayload(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{{Body: []byte(catalogFixture), ETag: `W/"a"`}}}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh 失败: %v", err)
	}
	payload, ok := catalog.Payload()
	if !ok {
		t.Fatal("取回后 Payload 应当可用")
	}

	parsed, err := canonical.Parse(payload)
	if err != nil {
		t.Fatalf("载荷不是合法 JSON: %v", err)
	}
	if got, _ := parsed.Lookup("version").AsInt(); got != DocumentVersion {
		t.Errorf("version = %d，期望 %d", got, DocumentVersion)
	}
	if got, _ := parsed.Lookup("source").AsString(); got != SourceURL {
		t.Errorf("source = %q，期望 %q", got, SourceURL)
	}
	if got, _ := parsed.Lookup("updated_at").AsString(); got != "2026-01-02T03:04:05Z" {
		t.Errorf("updated_at = %q，期望 2026-01-02T03:04:05Z", got)
	}
	if !parsed.Lookup("error").IsNull() {
		t.Errorf("成功取回时 error 应当是 null，实得 %v", parsed.Lookup("error"))
	}
	models := parsed.Lookup("models")
	if models == nil || models.Obj.Len() != 4 {
		t.Fatalf("models 应当有 4 项，实得 %v", models)
	}
	// 载荷里的键必须按码点排序，否则输出不可复现。
	keys := models.Obj.Keys()
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Fatalf("models 键未排序: %q 在 %q 之前", keys[i-1], keys[i])
		}
	}
	// 缓存价缺失时不能补 0：前端需要区分"没有这个价"与"这个价是 0"。
	gpt4o := models.Lookup("gpt-4o")
	if gpt4o.Lookup("cache_read") != nil {
		t.Error("gpt-4o 的 cache_read 缺失时不应出现在载荷里")
	}
}

// TestRefreshSendsETagAndHandles304 断言条件请求与 304 路径：目录未变时不重建载荷，
// 也不把 updated_at 往前推。
func TestRefreshSendsETagAndHandles304(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{
		{Body: []byte(catalogFixture), ETag: `W/"first"`},
		{NotModified: true},
	}}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatalf("第一次 Refresh 失败: %v", err)
	}
	before, _ := catalog.Payload()
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatalf("第二次 Refresh 失败: %v", err)
	}
	after, ok := catalog.Payload()
	if !ok {
		t.Fatal("304 之后目录应当仍然可用")
	}
	if fake.calls[0] != "" {
		t.Errorf("第一次请求的 If-None-Match = %q，期望空串", fake.calls[0])
	}
	if fake.calls[1] != `W/"first"` {
		t.Errorf("第二次请求的 If-None-Match = %q，期望 W/\"first\"", fake.calls[1])
	}
	if string(before) != string(after) {
		t.Error("304 不应改变载荷（updated_at 不该被复验刷新）")
	}
}

// TestRefreshKeepsStaleOnError 断言刷新失败时保留旧目录并把错误挂进载荷。
//
// 这是"绝不静默清零"的实现：宁可显示旧价格 + 告警，也不要让成本读数消失。
func TestRefreshKeepsStaleOnError(t *testing.T) {
	fake := &fakeFetcher{
		result: []FetchResult{{Body: []byte(catalogFixture), ETag: `W/"first"`}},
		errs:   []error{nil, errors.New("网络不通")},
	}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatalf("第一次 Refresh 失败: %v", err)
	}
	good, _ := catalog.Payload()

	err := catalog.Refresh(t.Context())
	if err == nil {
		t.Fatal("第二次 Refresh 应当报错")
	}
	stale, ok := catalog.Payload()
	if !ok {
		t.Fatal("刷新失败后目录应当仍然可用（保留旧价格）")
	}
	parsed, parseErr := canonical.Parse(stale)
	if parseErr != nil {
		t.Fatalf("载荷不是合法 JSON: %v", parseErr)
	}
	if got, _ := parsed.Lookup("error").AsString(); got != "网络不通" {
		t.Errorf("error = %q，期望 %q", got, "网络不通")
	}
	// 价格本身不变（只是多了 error 字段），所以内容会不同但模型表必须一致。
	goodParsed, _ := canonical.Parse(good)
	if goodParsed.Lookup("models").Obj.Len() != parsed.Lookup("models").Obj.Len() {
		t.Error("刷新失败不应丢掉任何模型")
	}
	if got, _ := parsed.Lookup("updated_at").AsString(); got != "2026-01-02T03:04:05Z" {
		t.Errorf("旧数据的 updated_at = %q，不该被失败刷新改写", got)
	}
}

// TestRefreshRejectsEmptyIndex 断言结构不对的响应被当作失败，不覆盖已有目录。
//
// 实测过：models.dev 的某些路径会返回 SPA 兜底页（HTTP 200 但是别的 JSON），
// 若不检查就会把目录清空成 0 个模型。
func TestRefreshRejectsEmptyIndex(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{
		{Body: []byte(catalogFixture), ETag: `W/"first"`},
		{Body: []byte(`{"error": "not the catalog"}`)},
	}}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatalf("第一次 Refresh 失败: %v", err)
	}
	if err := catalog.Refresh(t.Context()); err == nil {
		t.Fatal("空索引的响应应当被当作失败")
	}
	if _, ok := catalog.Payload(); !ok {
		t.Error("失败后应当保留旧目录")
	}
}

// TestRefreshInvalidJSONIsError 断言非法 JSON 不会 panic。
func TestRefreshInvalidJSONIsError(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{{Body: []byte(`{not json`)}}}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.Refresh(t.Context()); err == nil {
		t.Fatal("非法 JSON 应当报错")
	}
	if _, ok := catalog.Payload(); ok {
		t.Error("从未成功取回时不应有载荷")
	}
}

// TestHTTPStatusErrorText 钉住错误文案（与 internal/updatecheck 同名错误同形）。
func TestHTTPStatusErrorText(t *testing.T) {
	err := &HTTPStatusError{StatusCode: 503, URL: SourceURL}
	if got := err.Error(); got != "HTTP 503: "+SourceURL {
		t.Errorf("Error() = %q", got)
	}
}

// TestRefreshWithoutSnapshotOn304 断言没有本地目录时收到 304 会报错（防御上游异常行为）。
func TestRefreshWithoutSnapshotOn304(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{{NotModified: true}}}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.Refresh(t.Context()); err == nil {
		t.Fatal("没有本地目录时的 304 应当报错")
	}
}

// TestStartPreheatsAndStops 断言 Start 会先预热一次，且 stop 之后不再刷新。
//
// 用户要求"定期更新"，所以刷新必须**不依赖有人打开界面**：冷启动后第一次 /ui/pricing.json
// 就应当有价格，而不是等到有人访问才去下载 4.7 MB。
func TestStartPreheatsAndStops(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{
		{Body: []byte(catalogFixture), ETag: `W/"first"`},
	}}
	catalog := newTestCatalog(fake.fetch)

	stop := catalog.Start()
	// 预热在后台 goroutine 里跑，等它落地。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := catalog.Payload(); ok {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, ok := catalog.Payload(); !ok {
		t.Fatal("Start 之后应当已经预热出目录")
	}
	stop()

	// stop 之后 ticking 停止：把时钟推过 TTL 再等，取回次数不应增长。
	catalog.mu.Lock()
	before := len(fake.calls)
	catalog.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	catalog.mu.Lock()
	after := len(fake.calls)
	catalog.mu.Unlock()
	if before != after {
		t.Errorf("stop 之后仍在刷新: %d -> %d", before, after)
	}
}

// TestStartStopIsIdempotent 断言 stop 可以安全地调用多次（App.Close 可能被重复调用）。
func TestStartStopIsIdempotent(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{{Body: []byte(catalogFixture)}}}
	catalog := newTestCatalog(fake.fetch)
	stop := catalog.Start()
	stop()
	stop()
}

// TestRefreshOnceIsSingleFlight 断言**并发**触发取回时只会真正下载一次。
//
// 触发路径有两条：Payload 的过期触发与 Start 的定期循环。若它们各自直接取回，两路可以
// 同时打向上游——把 4.7 MB 下载两遍，并且两次结果互相覆盖。这里用一个"卡住"的取回
// 模拟慢网络，在它返回前并发触发多次，断言只发生一次真实取回。
func TestRefreshOnceIsSingleFlight(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	fetcher := func(url, etag string, timeout time.Duration) (FetchResult, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			// 第一次取回挂住，让并发的其它触发都落在闸门内。
			<-release
		}
		return FetchResult{Body: []byte(catalogFixture), ETag: `W/"a"`}, nil
	}
	catalog := newTestCatalog(fetcher)

	// 直接并发调 refreshOnce：第一次拿到闸门并阻塞，其余应立即返回而不取回。
	const parallel = 8
	var wg sync.WaitGroup
	wg.Add(parallel)
	for range parallel {
		go func() {
			defer wg.Done()
			_ = catalog.refreshOnce(t.Context())
		}()
	}

	// 等到第一次取回确实已经开始，且闸门已落下。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		catalog.mu.Lock()
		loading := catalog.loading
		catalog.mu.Unlock()
		mu.Lock()
		started := calls >= 1
		mu.Unlock()
		if loading && started {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	close(release)
	wg.Wait()

	mu.Lock()
	total := calls
	mu.Unlock()
	if total != 1 {
		t.Errorf("并发触发下的取回次数 = %d，期望 1（单飞闸门失效）", total)
	}
	// 取回成功后目录必须可用。
	if _, ok := catalog.Payload(); !ok {
		t.Error("单飞取回后目录应可用")
	}
}

// TestRefreshOnceSkipsFreshCatalog 断言目录仍新鲜时再触发不会重复取回。
//
// 这是"一次过期只下载一次"的核心不变式，且**同步可判**：过期时会一口气踢出多个后台
// goroutine（连续几次 /ui/pricing.json 读就会），而闸门只挡"同时在途"——若无脑取回，
// 第一个成功之后其余每个都会再把 4.7 MB 下一遍。这里刻意不靠 goroutine 调度巧合去撞，
// 直接断言"刚取回的目录不会被再取一次"。
func TestRefreshOnceSkipsFreshCatalog(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{
		{Body: []byte(catalogFixture), ETag: `W/"first"`},
		{Body: []byte(catalogFixture), ETag: `W/"second"`},
	}}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.refreshOnce(t.Context()); err != nil {
		t.Fatalf("预热取回失败: %v", err)
	}
	if err := catalog.refreshOnce(t.Context()); err != nil {
		t.Fatalf("新鲜期内重复触发应被跳过，却返回错误: %v", err)
	}
	if calls := len(fake.calls); calls != 1 {
		t.Errorf("取回次数 = %d，期望 1（新鲜期内不得重复取回）", calls)
	}
}

// TestRefreshOnceRespectsRetryBackoff 断言取回失败后不会立刻被穿透重试。
//
// 失败时 recordFailure 把下次允许取回的时间推到 now+DefaultRetry。若 refreshOnce 不看
// 这个时间，一次网络抖动会被同一批 goroutine 立刻重试多次，把 4.7 MB 反复打向上游。
func TestRefreshOnceRespectsRetryBackoff(t *testing.T) {
	fake := &fakeFetcher{errs: []error{errors.New("上游不可达"), errors.New("上游不可达")}}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.refreshOnce(t.Context()); err == nil {
		t.Fatal("取回失败应当返回错误")
	}
	// 退避期内再触发：应直接跳过（返回 nil），不再打上游。
	if err := catalog.refreshOnce(t.Context()); err != nil {
		t.Fatalf("退避期内重复触发应被跳过，却返回错误: %v", err)
	}
	if calls := len(fake.calls); calls != 1 {
		t.Errorf("取回次数 = %d，期望 1（退避期内不得重试）", calls)
	}
}

// TestPayloadTriggersBackgroundRefreshOnce 断言过期时只踢一次后台刷新。
//
// 目录 4.7 MB，若每个 /ui/pricing.json 请求都触发一次取回，页面轮询会把上游打爆。
func TestPayloadTriggersBackgroundRefreshOnce(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{
		{Body: []byte(catalogFixture), ETag: `W/"first"`},
		{Body: []byte(catalogFixture), ETag: `W/"second"`},
	}}
	catalog := newTestCatalog(fake.fetch)
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatalf("预热失败: %v", err)
	}

	// 让时钟越过 TTL，模拟目录过期。
	base := catalog.now()
	catalog.now = func() time.Time { return base.Add(DefaultTTL + time.Minute) }

	// 连续读多次：第一次踢后台刷新，后续在 loading 闸门落下前不应重复踢。
	for range 5 {
		if _, ok := catalog.Payload(); !ok {
			t.Fatal("过期后旧目录仍应可用")
		}
	}
	// 等到所有被踢出的后台 goroutine 都跑完再读数。不能在"看到 ≥2 次"时就收工：
	// 那会在其余 goroutine 尚未调度时提前读数，漏掉它们各自的重复取回，而本测试
	// 要抓的正是这个（闸门只挡同时在途、挡不住后继者再下 4.7 MB）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		catalog.mu.Lock()
		loading := catalog.loading
		catalog.mu.Unlock()
		if !loading {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	// 静默窗口：在途 goroutine 若还要取回，这段时间内必然已经计数。
	time.Sleep(100 * time.Millisecond)

	catalog.mu.Lock()
	calls := len(fake.calls)
	catalog.mu.Unlock()
	if calls != 2 {
		t.Errorf("取回次数 = %d，期望 2（预热 1 次 + 过期后台刷新 1 次）", calls)
	}
}

// —— 模型类型索引 ——

// kindFixture 是一份同时带价格与 modalities 的目录（价格是必需的：没有价格条目的
// 目录会被 Refresh 判成取回失败）。
const kindFixture = `{
  "p": {"models": {
    "gpt-image-1": {"cost": {"input": 1, "output": 2}, "modalities": {"input": ["text"], "output": ["image"]}},
    "whisper-1": {"cost": {"input": 1, "output": 2}, "modalities": {"input": ["audio"], "output": ["text"]}}
  }}
}`

// TestModelKindsUnavailableBeforeFirstFetch 断言首次取回前是"不可用"。
//
// 与 Payload 的语义一致，但调用方的处理**刻意不同**：没有价格时必须响亮失败，
// 而没有类型目录时可以退回名字规则（见 internal/server 的 model-kinds.json）。
func TestModelKindsUnavailableBeforeFirstFetch(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{{Body: []byte(kindFixture), ETag: `W/"a"`}}}
	catalog := newTestCatalog(fake.fetch)

	if kinds, ok := catalog.ModelKinds(); ok || kinds != nil {
		t.Errorf("取回前 ModelKinds = (%v, %v)，期望 (nil, false)", kinds, ok)
	}
}

// TestRefreshBuildsModelKinds 断言类型索引与价格由**同一次取回**构建出来。
func TestRefreshBuildsModelKinds(t *testing.T) {
	fake := &fakeFetcher{result: []FetchResult{{Body: []byte(kindFixture), ETag: `W/"a"`}}}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh 失败: %v", err)
	}
	kinds, ok := catalog.ModelKinds()
	if !ok {
		t.Fatal("取回后 ModelKinds 应当可用")
	}
	if len(fake.calls) != 1 {
		t.Errorf("取回次数 = %d，期望 1（类型索引不该另起一次取回）", len(fake.calls))
	}
	image, present := kinds["gpt-image-1"]
	if !present {
		t.Fatal("gpt-image-1 未进类型索引")
	}
	if len(image.Kinds) != 1 || string(image.Kinds[0]) != "image" {
		t.Errorf("gpt-image-1 的类型 = %v，期望 [image]", image.Kinds)
	}
	if got := kinds["whisper-1"]; len(got.Kinds) != 1 || string(got.Kinds[0]) != "stt" {
		t.Errorf("whisper-1 的类型 = %v，期望 [stt]", got.Kinds)
	}
}

// TestModelKindsKeptStaleOnError 断言刷新失败时类型索引与价格一起保留。
//
// 两份索引共享快照，因此这条与 TestRefreshKeepsStaleOnError 是同一件事的两面：
// 若哪次重构让类型索引单独重建，失败后模型分组会凭空消失，而价格还在——那种
// "一半新一半旧"的状态没有任何界面能解释。
func TestModelKindsKeptStaleOnError(t *testing.T) {
	fake := &fakeFetcher{
		result: []FetchResult{{Body: []byte(kindFixture), ETag: `W/"first"`}},
		errs:   []error{nil, errors.New("网络不通")},
	}
	catalog := newTestCatalog(fake.fetch)

	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatalf("第一次 Refresh 失败: %v", err)
	}
	if err := catalog.Refresh(t.Context()); err == nil {
		t.Fatal("第二次 Refresh 应当报错")
	}
	kinds, ok := catalog.ModelKinds()
	if !ok {
		t.Fatal("刷新失败后类型索引应当仍然可用")
	}
	if _, present := kinds["gpt-image-1"]; !present {
		t.Error("刷新失败后类型索引不该丢内容")
	}
}
