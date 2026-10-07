// Package pricing 维护 models.dev 目录的两份派生索引，供 WebUI 使用：
//
//   - **价格**（Build / Payload）：成本估算的唯一数据源；
//   - **模型类型**（ModelKinds）：文本 / 图像 / 视频 / 语音 / 嵌入 / 重排的分类证据。
//
// 两份索引刻意由**同一次取回**构建：它们读的是同一份 4.7 MB 文档、共享同一个保鲜期
// 与同一套失败语义（取回失败一律保留旧数据，见 recordFailure）。若各自维护一份取回
// 循环，目录会被下载两遍，而两条路径的失败时机不同还会让"价格是新的、类型是旧的"
// 这种状态凭空出现。
//
// 指标库只记录 **token 用量**，从不记录金额——参照实现也是如此（metrics.py 的表结构
// 里没有任何价格列）。成本因此是**派生读数**：token 用量 × 目录单价，现算不落库。
// 目录更新后历史成本会跟着变，这是刻意的：单价是外部事实，不是本项目的记账结果。
//
// 目录来自 https://models.dev/api.json（约 4.7 MB、7800+ 模型、220+ 供应商）。
// 上游把**同一个模型 id 在多家供应商下重复列出**且价格不同，因此这里按「小写模型
// id」去重并挑一条，挑选口径见 selectCheapest。
//
// 目录只缓存在内存里：不落盘、不进配置、不进指标库，进程重启即重新拉取。
package pricing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/modelkind"
)

const (
	// SourceURL 是 models.dev 的完整目录。
	SourceURL = "https://models.dev/api.json"
	// DocumentVersion 是 /ui/pricing.json 的载荷版本。前端据此判断能否解析，
	// 字段形状有破坏性变化时 +1。
	DocumentVersion = 1
	// DefaultTTL 是目录的保鲜期。
	//
	// models.dev 的 Cache-Control 是 "public, must-revalidate, max-age=0" 且带
	// ETag，条件请求命中时回 304，因此到期后是一次廉价的复验而不是重新下载
	// 4.7 MB。
	DefaultTTL = 6 * time.Hour
	// DefaultRetry 是取回失败后的重试间隔。失败时不丢弃已有目录（见 recordFailure），
	// 这个间隔只用来避免每次读都重试。
	DefaultRetry = 5 * time.Minute
	// DefaultTimeout 是单次取回的整体超时。
	//
	// 给得宽松是有意的：目录有 4.7 MB，冷取实测 4 秒、慢链路上见过 35 秒，而刷新只
	// 跑在后台循环里、**不阻塞任何请求**，所以宁可多等也不要误判成失败。它同时也是
	// HTTPFetcher 施加在单次请求上的截止时间；cmd/amkr 的 http.Client.Timeout 取同一
	// 个量级，两者不会互相截断。
	DefaultTimeout = 90 * time.Second

	// userAgent 让上游的访问日志能认出请求来源。
	userAgent = "auto-model-key-router (pricing catalog)"
)

// Entry 是一个模型的价格，单位是 **USD / 100 万 token**（与 models.dev 一致）。
//
// 缓存价用指针：缺失与「0 元」是两回事——缺失时计费要回退到输入价（否则缓存读会被
// 算成免费，凭空压低成本），而真正的 0 表示上游明确免费。
type Entry struct {
	Input      float64
	Output     float64
	CacheRead  *float64
	CacheWrite *float64
}

// FetchResult 是一次取回的结果。
type FetchResult struct {
	// NotModified 表示上游回了 304，Body 为空且目录未变。
	NotModified bool
	// ETag 是本次响应的 ETag，供下次条件请求使用。
	ETag string
	// Body 是原始目录 JSON。
	Body []byte
}

// Fetcher 取回原始目录。etag 为空表示无条件请求。
//
// 做成函数类型而不是直接用 http.Client，理由与 internal/updatecheck 的同名接缝
// 一致：测试必须能不联网。
type Fetcher func(url, etag string, timeout time.Duration) (FetchResult, error)

// HTTPFetcher 返回真实实现。client 为 nil 时用 http.DefaultClient。
//
// 与 internal/updatecheck.HTTPFetcher 一样把超时施加在**单个请求**上而不是
// Client.Timeout，以免污染共享 client。
func HTTPFetcher(client *http.Client) Fetcher {
	if client == nil {
		client = http.DefaultClient
	}
	return func(url, etag string, timeout time.Duration) (FetchResult, error) {
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return FetchResult{}, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", userAgent)
		if etag != "" {
			request.Header.Set("If-None-Match", etag)
		}
		if timeout > 0 {
			ctx, cancel := context.WithTimeout(request.Context(), timeout)
			defer cancel()
			request = request.WithContext(ctx)
		}
		response, err := client.Do(request)
		if err != nil {
			return FetchResult{}, err
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusNotModified {
			// 上游确认目录未变。保留原 ETag：304 可能不带 ETag 头。
			return FetchResult{NotModified: true, ETag: etag}, nil
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return FetchResult{}, &HTTPStatusError{StatusCode: response.StatusCode, URL: url}
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return FetchResult{}, err
		}
		return FetchResult{ETag: response.Header.Get("ETag"), Body: body}, nil
	}
}

// HTTPStatusError 表示上游返回非 2xx（与 internal/updatecheck 的同名错误同形）。
type HTTPStatusError struct {
	StatusCode int
	URL        string
}

// Error 实现 error。
func (e *HTTPStatusError) Error() string {
	return "HTTP " + strconv.Itoa(e.StatusCode) + ": " + e.URL
}

// Build 把 models.dev 的原始目录编译成价格索引，键是**小写模型 id**。
//
// 导出是为了让测试直接喂固定目录，不必联网。
func Build(parsed *canonical.Value) map[string]Entry {
	index := make(map[string]Entry)
	if !parsed.IsObject() {
		return index
	}
	// 顶层是 {供应商 id: {models: {模型 id: {...}}}}。按 canonical 的插入顺序遍历，
	// 因此同分时保留上游先出现的那条，结果可复现。
	for _, providerID := range parsed.Obj.Keys() {
		provider, ok := parsed.Obj.Get(providerID)
		if !ok {
			continue
		}
		models, ok := provider.LookupOK("models")
		if !ok || !models.IsObject() {
			continue
		}
		for _, modelID := range models.Obj.Keys() {
			model, ok := models.Obj.Get(modelID)
			if !ok {
				continue
			}
			entry, ok := costOf(model)
			if !ok {
				continue
			}
			key := strings.ToLower(modelID)
			if current, seen := index[key]; !seen || selectCheapest(entry, current) {
				index[key] = entry
			}
		}
	}
	return index
}

// costOf 取一个模型的价格；没有可用的 input/output 时报告 false。
//
// cost 为 null、缺字段、字段不是数字（models.dev 里确有 null）时一律跳过：宁可不给
// 报价，也不要拿 0 冒充。
func costOf(model *canonical.Value) (Entry, bool) {
	cost, ok := model.LookupOK("cost")
	if !ok || !cost.IsObject() {
		return Entry{}, false
	}
	input, ok := numberOf(cost, "input")
	if !ok {
		return Entry{}, false
	}
	output, ok := numberOf(cost, "output")
	if !ok {
		return Entry{}, false
	}
	entry := Entry{Input: input, Output: output}
	if value, ok := numberOf(cost, "cache_read"); ok {
		entry.CacheRead = &value
	}
	if value, ok := numberOf(cost, "cache_write"); ok {
		entry.CacheWrite = &value
	}
	return entry, true
}

// numberOf 取一个数字字段。
func numberOf(object *canonical.Value, key string) (float64, bool) {
	value, ok := object.LookupOK(key)
	if !ok || !value.IsNumber() {
		return 0, false
	}
	return value.AsFloat()
}

// selectCheapest 报告 candidate 是否应当取代 current（用户确认的口径：歧义时取最便宜的）。
//
// **先排除"标 0 元"的挂名条目，再比价格。** 这不是优化，是正确性：models.dev 里有
// 一批供应商把模型白挂在目录里（input/output 都是 0），天真的"取最小和"会让
// claude-sonnet-4-6、qwen3-max、glm-4.6 等 465 个模型全部算成 $0，成本页会显示一张
// 全零的假账。只有当**所有**供应商都报 0 时才承认它免费（多为 :free 变体、TTS 与
// 图像模型，实测 303 个）。
func selectCheapest(candidate, current Entry) bool {
	candidatePriced := candidate.Input > 0 || candidate.Output > 0
	currentPriced := current.Input > 0 || current.Output > 0
	if candidatePriced != currentPriced {
		return candidatePriced
	}
	return candidate.Input+candidate.Output < current.Input+current.Output
}

// snapshot 是一次成功取回后的只读状态。
type snapshot struct {
	index     map[string]Entry
	kinds     modelkind.Catalog
	updatedAt time.Time
	payload   []byte
}

// Catalog 持有内存中的价格目录，并按需在后台刷新。
//
// 并发安全。刷新**永远不在请求路径上**：Payload 只读当前快照并（必要时）踢一个
// 后台 goroutine，因此首个请求拿不到数据时得到的是"不可用"，而不是几十秒的等待。
type Catalog struct {
	fetch   Fetcher
	ttl     time.Duration
	retry   time.Duration
	timeout time.Duration
	now     func() time.Time

	mu      sync.Mutex
	snap    *snapshot
	etag    string
	nextAt  time.Time
	loading bool
}

// New 构造目录。fetch 为 nil 时用真实的 HTTP 实现。
//
// **生产路径不会传 nil**（server.Options.PricingFetch 的 nil 表示完全不出网，见
// internal/server/pricing.go）；这里的兜底只是为了让本包独立可用。
func New(fetch Fetcher) *Catalog {
	if fetch == nil {
		fetch = HTTPFetcher(nil)
	}
	return &Catalog{
		fetch:   fetch,
		ttl:     DefaultTTL,
		retry:   DefaultRetry,
		timeout: DefaultTimeout,
		now:     time.Now,
	}
}

// Payload 返回当前可用的 /ui/pricing.json 载荷，以及它是否可用。
//
// 不可用只发生在**从未成功取回**时（首次启动、或一直连不上 models.dev）。此时前端
// 应当显示"无定价"而不是把成本算成 0。
//
// 已过期时踢一次后台刷新；刷新立刻返回，绝不阻塞请求（目录 4.7 MB，冷取实测数秒）。
func (c *Catalog) Payload() ([]byte, bool) {
	c.mu.Lock()
	stale := !c.now().Before(c.nextAt)
	var payload []byte
	if c.snap != nil {
		payload = c.snap.payload
	}
	c.mu.Unlock()

	if stale {
		// 后台跑：请求线程只取快照，不参与取回。
		go func() { _ = c.refreshOnce(context.Background()) }()
	}
	return payload, payload != nil
}

// ModelKinds 返回 models.dev 目录里的**模型类型索引**（与价格同一次取回构建）。
//
// 第二个返回值报告"目录是否可用"：false 表示从未成功取回过（首次启动、或一直连不上
// models.dev），此时调用方应当退回名字规则而不是放弃判定——规则层不需要网络。
//
// 返回的索引由多个请求共享，**调用方不得改写**（BuildCatalog 之后就再没有别人写过它）。
// 与 Payload 一样，本方法不会阻塞在网络上：过期时只踢一个后台刷新。
func (c *Catalog) ModelKinds() (modelkind.Catalog, bool) {
	c.mu.Lock()
	stale := !c.now().Before(c.nextAt)
	var kinds modelkind.Catalog
	if c.snap != nil {
		kinds = c.snap.kinds
	}
	c.mu.Unlock()

	if stale {
		go func() { _ = c.refreshOnce(context.Background()) }()
	}
	return kinds, kinds != nil
}

// refreshOnce 是**唯一**的取回入口，并保证一次过期只触发一次真实取回。
//
// 为什么必须收在一处：取回有两条触发路径——Payload 的过期触发，与 Start 的定期循环。
// 若各自直接调 Refresh，两条路径可以同时打向上游，等于把 4.7 MB 下载两遍、并让两次
// 结果互相覆盖（先返回的那次可能把后返回的更新结果盖掉）。
//
// 只挡"同时在途"是不够的：一次过期会踢出**多个** goroutine（连着几次
// /ui/pricing.json 读就会），第一个取回成功、闸门落下之后，其余 goroutine 若直接
// 取回，就会把刚刚已经新鲜的目录再下载一遍——读几次就下载几次。因此拿到闸门后要
// 再确认一次新鲜度，不新鲜才取回。DefaultRetry 的失败退避也靠这一步生效：否则一次
// 失败会被同一批 goroutine 立刻穿透重试。
func (c *Catalog) refreshOnce(ctx context.Context) error {
	c.mu.Lock()
	if c.loading {
		c.mu.Unlock()
		return nil
	}
	if c.now().Before(c.nextAt) {
		// 目录已新鲜（别人刚取回，或失败退避尚未到期）：无需再取。
		c.mu.Unlock()
		return nil
	}
	c.loading = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.loading = false
		c.mu.Unlock()
	}()
	return c.Refresh(ctx)
}

// Refresh 同步取回并按需重建目录。测试直接调它；生产路径由 Payload 在后台调用。
//
// 任何失败都**不丢弃已有目录**：宁可让界面显示"X 分钟前的价格 + 更新失败"，也不要
// 让成本读数悄悄变成 0 或消失。
func (c *Catalog) Refresh(ctx context.Context) error {
	c.mu.Lock()
	etag := c.etag
	hasSnapshot := c.snap != nil
	c.mu.Unlock()

	result, err := c.fetch(SourceURL, etag, c.timeout)
	if err != nil {
		c.recordFailure(err)
		return err
	}
	if result.NotModified {
		if !hasSnapshot {
			// 只在有条件缓存时才会带 If-None-Match，走到这里说明上游行为异常。
			err := errors.New("pricing: 收到 304 但本地没有目录")
			c.recordFailure(err)
			return err
		}
		// 目录未变：只把保鲜期往后推，不重建载荷（updated_at 含义是"上次真正取到
		// 新数据的时间"，不该被 304 刷新）。
		c.mu.Lock()
		c.nextAt = c.now().Add(c.ttl)
		c.mu.Unlock()
		return nil
	}

	parsed, err := canonical.Parse(result.Body)
	if err != nil {
		c.recordFailure(err)
		return err
	}
	index := Build(parsed)
	if len(index) == 0 {
		// 上游返回了结构不对的东西（例如 Cloudflare 的 SPA 兜底页）。当作失败，
		// 保留旧目录。
		err := errors.New("pricing: 目录里没有任何带价格的模型")
		c.recordFailure(err)
		return err
	}

	now := c.now()
	c.mu.Lock()
	c.etag = result.ETag
	c.snap = &snapshot{
		index:     index,
		kinds:     modelkind.BuildCatalog(parsed),
		updatedAt: now,
		payload:   render(index, now, ""),
	}
	c.nextAt = now.Add(c.ttl)
	c.mu.Unlock()
	return nil
}

// recordFailure 安排下次重试，并把错误挂进载荷（保留旧价格）。
func (c *Catalog) recordFailure(err error) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextAt = now.Add(c.retry)
	if c.snap == nil {
		return
	}
	// 保留旧价格，只在载荷里挂上错误说明，让界面能表达"这是旧数据"。
	c.snap = &snapshot{
		index:     c.snap.index,
		kinds:     c.snap.kinds,
		updatedAt: c.snap.updatedAt,
		payload:   render(c.snap.index, c.snap.updatedAt, err.Error()),
	}
}

// render 把索引序列化成 /ui/pricing.json 的载荷。
//
// 键按码点排序：目录有 3000+ 项，输出必须可复现（否则 ETag/调试对比都无意义）。
func render(index map[string]Entry, updatedAt time.Time, lastErr string) []byte {
	keys := make([]string, 0, len(index))
	for key := range index {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	models := canonical.NewObject()
	for _, key := range keys {
		entry := index[key]
		pairs := []canonical.ObjectPair{
			{Key: "input", Value: canonical.NewFloat(entry.Input)},
			{Key: "output", Value: canonical.NewFloat(entry.Output)},
		}
		// 缓存价缺失时**不补 0**：前端据此回退到输入价。
		if entry.CacheRead != nil {
			pairs = append(pairs, canonical.ObjectPair{
				Key: "cache_read", Value: canonical.NewFloat(*entry.CacheRead),
			})
		}
		if entry.CacheWrite != nil {
			pairs = append(pairs, canonical.ObjectPair{
				Key: "cache_write", Value: canonical.NewFloat(*entry.CacheWrite),
			})
		}
		models.SetKey(key, canonical.NewObjectOf(pairs...))
	}

	document := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "version", Value: canonical.NewIntValue(DocumentVersion)},
		canonical.ObjectPair{Key: "source", Value: canonical.NewString(SourceURL)},
		canonical.ObjectPair{Key: "updated_at", Value: timestampValue(updatedAt)},
		// 非 null 表示这份价格是旧的（最近一次刷新失败），界面应当同时显示价格与告警。
		canonical.ObjectPair{Key: "error", Value: errorValue(lastErr)},
		canonical.ObjectPair{Key: "models", Value: models},
	)
	return []byte(canonical.DumpsOrdered(document))
}

// timestampValue 渲染 RFC3339（UTC）；零值渲染成 null。
func timestampValue(at time.Time) *canonical.Value {
	if at.IsZero() {
		return canonical.NewNull()
	}
	return canonical.NewString(at.UTC().Format(time.RFC3339))
}

// errorValue 把空串渲染成 null。
func errorValue(message string) *canonical.Value {
	if message == "" {
		return canonical.NewNull()
	}
	return canonical.NewString(message)
}

// Start 起一个后台循环按 TTL 定期刷新目录，并立刻做一次预热取回。
//
// 用户要求"缓存可以，但是需要定期更新"，因此刷新**不依赖有人打开界面**：进程启动后
// 就取一次，此后每 TTL 复验一次（条件请求命中时是 304）。
//
// 返回的 stop 函数停掉循环并等它退出；它与 App.Close 一样必须被调用，否则进程退出
// 前会有一个 goroutine 持有 HTTP 连接。
//
// 预热与复验都用 Refresh，因此失败不会清空已有目录；预热失败仅仅是首次请求回 503。
func (c *Catalog) Start() (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		// 预热：失败不致命，界面会显示"无定价"，下一轮 TTL 后再试。
		_ = c.refreshOnce(ctx)
		ticker := time.NewTicker(c.ttl)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 走 refreshOnce 而不是 Refresh：若此刻恰好有一次过期触发的后台取回
				// 在途，这一轮就跳过，避免两路同时下载 4.7 MB。
				_ = c.refreshOnce(ctx)
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}
