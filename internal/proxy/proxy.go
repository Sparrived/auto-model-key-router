// Package proxy 移植 auto_model_key_router/proxy_handler.py：AMKR 的编排中枢。
//
// 它把鉴权、路由解析、key 选择与冷却、上游调用与重试、流式转发、协议响应转换
// 和指标落库串成一条链路。本包**不**自己实现这些能力，而是通过接口吃进
// internal/runtime 的运行时资源（它已经持有 config / keypool / metrics / 上游
// 客户端），因此这里只负责编排。
//
// 四条最容易写错、必须逐条复刻的语义：
//
//  1. **重试预算三档**（runtime.RetryPolicy）：指定 key / only_first / 单 key
//     ⇒ max_retries+1；否则 ⇒ key 数量（即只轮换一遍、没有额外重试）。
//     401/403 **可重试**，会换 key（streaming.py:17）。
//  2. **一旦下游开始收到字节就永不重试**：响应头已经写出去，任何中途失败都只能
//     静默终止流（proxy_handler.py:1137）。因此流式尝试的 key 释放**只**由
//     runtime.StreamLifecycle.Finish 承担（proxy_handler.py:1148、1307、1400），
//     普通路径由 defer 承担。
//  3. **冷却过滤是软的**：可用集合为空时回退到「只排除 excluded」的集合，
//     冷却中的 key 仍可能被再次选中（key_pool.py:214）。这是刻意的降级。
//  4. **`payload` 为空或没有 model 键时字节级透传**，不做任何适配、不注入
//     stream_options（proxy_support.py:95）。
//
// 与参照实现的三处**有意分叉**（产品决策）：上游调用上限 + 结构化日志、显式
// multipart 处理、畸形请求体显式 400。三者都可通过 Options 关掉以回到逐字一致
// 的参照行为（见 BodyPolicy / MultipartPolicy / DefaultMaxUpstreamCallsPerRequest），
// 与参照实现比对时走的正是「关掉」档。
package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
	"github.com/Sparrived/auto-model-key-router/internal/upstream"
)

// DefaultMaxUpstreamCallsPerRequest 是单次下游请求允许发起的上游调用上限。
//
// **这是对参照实现的有意增补。** 参照实现的最坏情况是
// `attempts × 2（native→chat 回退）× 2（400 tool 重试）`，再叠加 unified/task
// 备选（proxy_handler.py:574-832），3 个 key 时约 24 次上游调用；而 `attempts`
// 本身是 key_count，所以 key 越多放大越严重。参照实现没有任何上限，也没有一条
// 能看出放大倍数的日志——上游账单涨了却查不出原因。
//
// 这里保留全部语义（分支、顺序、错误形状都不变），只加一个硬上限与结构化日志：
// 达到上限后不再发起新的上游调用，直接返回 502 + `upstream_call_cap_exceeded`。
// 32 是「正常路径 24 次」再留一点余量的取值，可通过 Options 调整。
const DefaultMaxUpstreamCallsPerRequest = 32

// DefaultMaxMultipartBytes 是 multipart/form-data 请求体的默认缓冲上限（64 MiB）。
//
// 之所以要缓冲而不是流式转发：路由需要先知道 model，而 multipart 的 model 只在
// 表单字段里，必须先读。64 MiB 是「足够大但不至于让单个请求吃掉内存」的取值。
const DefaultMaxMultipartBytes = 64 << 20

// 关于 JSON 请求体上限：**默认不限**（Options.MaxJSONBytes 为 0）。
//
// 参照实现对 JSON 体没有任何上限（Starlette 的 request.body() 直接读全量），因此
// 加一个默认上限属于行为变更：一张内联 base64 图片就能把请求体推到几 MiB，1 MiB
// 这种「看起来合理」的默认值会让真实用户的请求 413。所以这里默认保持参照行为，
// 把上限做成**显式可选**的加固开关（部署在公网时可设成 32 MiB 之类）。
// multipart 是另一回事：它是本包新增的能力，没有可对齐的参照行为，因此带上限。

// RetryableStatusCodes 是需要重试的上游状态码（对齐 streaming.py:17）。
//
// 401 与 403 **在列**：参照实现把「key 鉴权失败」也当作可重试，因为轮换到另一个
// key 常常就能成功。这与直觉相反，但属既定契约，不能「顺手修掉」。
var RetryableStatusCodes = map[int]bool{
	401: true, 403: true, 429: true, 500: true,
	502: true, 503: true, 504: true, 521: true,
}

// IsRetryableStatus 报告状态码是否需要重试。
func IsRetryableStatus(statusCode int) bool { return RetryableStatusCodes[statusCode] }

// KeyPool 是本包需要的 key 选择与健康状态接缝。
//
// *keypool.KeyPool 天然满足它（签名一致），所以生产代码无需适配层。用接口而非
// 具体类型有两个理由：让测试可以用确定性假实现钉住重试/冷却的编排顺序，以及让
// 本包不与 keypool 的存储细节耦合。
type KeyPool interface {
	ResolveRoute(modelID string, keyName *string, path string) (string, string, error)
	// ResolveRouteIn 在指定工作空间里解析路由；任务名只在该空间内查表。
	ResolveRouteIn(workspace, modelID string, keyName *string, path string) (string, string, error)
	ResolveUnifiedPlan(routeKind string, keyName *string) (config.RoutePlan, error)
	TaskPlan(taskName string) (config.RoutePlan, bool)
	// TaskPlanIn / TaskParamsIn 按 (工作空间, 任务名) 查表。
	TaskPlanIn(workspace, taskName string) (config.RoutePlan, bool)
	TaskParams(taskName string) *canonical.Value
	TaskParamsIn(workspace, taskName string) *canonical.Value
	KeyCount(modelID string) int
	// KeyCountFor 返回模型在**某把访问密钥作用域内**可用的 key 数（nil = 不限制）。
	KeyCountFor(modelID string, accessKey *config.AccessKeyConfig) int
	RoutingMode(modelID string) string
	KeyByName(modelID, keyName string, accessKey *config.AccessKeyConfig) (config.KeyConfig, error)
	NextKey(modelID string, excluded []string, accessKey *config.AccessKeyConfig, affinityKey string) (config.KeyConfig, error)
	AcquireKey(modelID, keyName string)
	ReleaseKey(modelID, keyName string)
	MarkSuccess(modelID, keyName string)
	MarkFailure(modelID, keyName string, statusCode *int, retryAfter *float64)
	SupportsNativeEndpoint(baseURL, routePath string) *bool
	UpdateNativeEndpoint(baseURL string, supported bool, routePath, reason string) error
}

// UpstreamClient 是本包需要的上游 HTTP 接缝。
//
// 只要求 Do / DoStream：参照实现用同一个 httpx 客户端兼顾非流式与流式，但两者的
// 超时语义不同（整请求 vs 首字节窗口），所以在 Go 侧显式分成两个方法
// （见 internal/upstream/client.go）。
type UpstreamClient interface {
	Do(req *http.Request) (*http.Response, error)
	DoStream(req *http.Request) (*http.Response, error)
}

// 编译期断言：internal/upstream 的客户端满足本接缝。
var _ UpstreamClient = (*upstream.Client)(nil)

// UsageExtractor 从上游响应体里取出 usage 对象；没有则返回 nil。
//
// 参照实现用 metrics.extract_usage（metrics.py:1054）：依次看顶层 usage、
// response.usage、message.usage，只接受对象。因为调用方可能要按自己的方言扩展
// 提取规则，这里做成可注入的函数；nil 时用 DefaultExtractUsage。
type UsageExtractor func(body *canonical.Value) *canonical.Value

// MetricsSink 是本包需要的指标落库接缝。
//
// **刻意不复用 internal/metrics 的具体类型**：该包当时由另一个 agent 在写、测试
// 还是红的。这里只声明迁移期真正需要的动作（写一行），让调用方在两个包都稳定后
// 再注入适配器。internal/metrics 的 Store.Record 已经与本结构一一对应
// （metrics/store.go:64 的 RecordParams），适配只需字段搬运。
type MetricsSink interface {
	// Record 写入一行请求指标。ctx 用于上游取消时快速返回。
	Record(ctx context.Context, record MetricRecord) error
}

// MetricRecord 是一次请求的可观测结果，字段与参照实现的 record() 实参一一对应。
//
// 用指针表达 Python 的可选参数：StatusCode 为 nil 表示参照实现传了 None（上游
// 请求直接失败，没有响应），而不是「状态码 0」；ProviderID / PoolName /
// UpstreamModelID 为 nil 表示相应字段缺失。
//
// Workspace 是**有意增补**：参照实现没有工作空间概念。它不进入 request_metrics
// 的列，而是单独落到 request_workspace 旁挂表（见 internal/metrics/schema.go）。
type MetricRecord struct {
	ModelID          string
	KeyName          string
	StatusCode       *int
	Usage            *canonical.Value
	Retried          bool
	Failed           bool
	DurationMS       int64
	FirstTokenMS     int64
	RequestedModelID string
	CallerType       string
	ProviderID       *string
	PoolName         *string
	UpstreamModelID  *string
	Workspace        string
	// AccessKeyID 是发起请求的访问密钥的 key_id（配置里的稳定标识符），空串表示
	// 这次请求不出自访问密钥。
	//
	// 与 Workspace 同样是**有意增补**，同样不进入 request_metrics 的列，而是落到
	// request_access_key 旁挂表（见 internal/metrics/schema.go）。CallerType 只说
	// 「来自访问密钥」，说不出**哪一把**，而访问密钥是按人分发的——没有这个字段，
	// 访问密钥看板就答不出「这个人用了多少」。
	//
	// 注意不要拿 KeyName 顶替：那是**被选中的上游 Provider key 名**（见 internal/proxy
	// 的 recordMetric），与调用方身份无关。
	AccessKeyID string
	// ClientAddr / UserAgent 是入站请求的来源（客户端地址与 User-Agent）。
	//
	// 与 Workspace / AccessKeyID 同样是**有意增补**，同样不进入 request_metrics 的列，
	// 而是落到 request_source 旁挂表（见 internal/metrics/schema.go）。看板的请求流
	// 要回答「这次请求是谁、从哪儿发来的」，而 request_metrics 里没有任何一列能表达
	// 网络位置。
	//
	// ClientAddr 取 http.Request.RemoteAddr（host:port），**不读 X-Forwarded-For**：
	// 那个头由调用方自带、可伪造，与访问日志（internal/server/accesslog.go）保持
	// 同一个取值来源。
	ClientAddr string
	UserAgent  string
}

// Options 是 Handler 的可选配置。零值即「按产品决策的默认档」。
type Options struct {
	// Logger 用于结构化日志；nil 时按 AMKR_LOG_FORMAT=json 选择 JSON 或文本。
	Logger *slog.Logger
	// UsageExtractor 覆盖 usage 提取；nil 时用 DefaultExtractUsage。
	UsageExtractor UsageExtractor
	// MaxUpstreamCallsPerRequest 覆盖上游调用上限；<=0 时用
	// DefaultMaxUpstreamCallsPerRequest。见该常量的说明（有意增补）。
	MaxUpstreamCallsPerRequest int
	// BodyPolicy 决定畸形请求体的处理方式（默认 BodyPolicyStrict）。
	BodyPolicy BodyPolicy
	// Multipart 是 multipart/form-data 的处理方式（默认 MultipartReject）。
	Multipart MultipartPolicy
	// MaxMultipartBytes 是 multipart 请求体的缓冲上限；<=0 时用
	// DefaultMaxMultipartBytes。
	MaxMultipartBytes int64
	// MaxJSONBytes 是 JSON 请求体的上限；<=0（默认）表示**不限**，与参照实现一致。
	// 只在需要对外网暴露的部署里显式设置。
	MaxJSONBytes int64
	// Clock 返回当前时间；nil 时用 time.Now。仅用于可观测的耗时，注入它便于测试。
	Clock func() time.Time
	// Authorizer 覆盖鉴权判定；nil 时用 auth.DefaultAuthorizer。
	Authorizer func(r *http.Request, localAPIKey string) *AuthorizerResult
}

// AuthorizerResult 是鉴权结果。
type AuthorizerResult struct {
	// Workspace 非空表示本次请求由一把**作用域推理凭据**发起，且它被钉死在这个
	// 工作空间上。
	//
	// 钉死的意思是忽略请求头 X-AMKR-Workspace：空间由 key 决定。这是这个模式存在的
	// 全部意义——key 会被配进各个项目的环境变量，若能用一个请求头换空间，一把泄漏的
	// key 就等于所有空间的推理权限。与面板 key 的做法一致（见 api 的
	// authorizedTaskConfig）。
	Workspace string
	// AccessKey 非空表示本次请求由一把**访问密钥**发起。
	//
	// 它是完整的配置对象（而不是只抽出两份清单）：调用方要按它过滤供应商（选上游
	// key）、按它过滤模型名（解析阶段），还要把 name 写进日志与指标。抽字段会让这三处
	// 各拿一份副本，改一处漏一处的风险大于多传一个指针。
	AccessKey *config.AccessKeyConfig
	// Denied 非空表示凭据本身有效但**被停用**，值是给调用方的说明。
	//
	// 与「未通过鉴权」分开：停用的 key 是已知身份被管理员关掉，回 403 并说清原因，
	// 而错误凭据回 401。合并两者会让「我明明配对了 key」这种排查无从下手。
	Denied string
}

// Handler 是代理请求的编排器。
//
// 它可以从任意 goroutine 并发调用：所有可变状态（每次请求的上下文、上游调用
// 计数）都挂在单次请求的结构上，共享的 RuntimeManager 自带锁。
type Handler struct {
	manager      *runtime.RuntimeManager
	metrics      MetricsSink
	extract      UsageExtractor
	logger       *slog.Logger
	authorizer   func(*http.Request, string) *AuthorizerResult
	maxCalls     int
	bodyPolicy   BodyPolicy
	multipart    MultipartPolicy
	maxMultipart int64
	maxJSONBytes int64
	now          func() time.Time
}

// New 构造 Handler。metrics 可以为 nil（此时不写指标行，key 健康回写仍然发生）。
func New(manager *runtime.RuntimeManager, metrics MetricsSink, options Options) *Handler {
	logger := options.Logger
	if logger == nil {
		logger = newLogger()
	}
	extract := options.UsageExtractor
	if extract == nil {
		extract = DefaultExtractUsage
	}
	maxCalls := options.MaxUpstreamCallsPerRequest
	if maxCalls <= 0 {
		maxCalls = DefaultMaxUpstreamCallsPerRequest
	}
	maxMultipart := options.MaxMultipartBytes
	if maxMultipart <= 0 {
		maxMultipart = DefaultMaxMultipartBytes
	}
	// maxJSONBytes <= 0 表示不限（与参照实现一致），见 DefaultMaxMultipartBytes
	// 上方的说明。
	maxJSON := options.MaxJSONBytes
	bodyPolicy := options.BodyPolicy
	if bodyPolicy == "" {
		bodyPolicy = BodyPolicyStrict
	}
	multipart := options.Multipart
	if multipart == "" {
		multipart = MultipartReject
	}
	now := options.Clock
	if now == nil {
		now = time.Now
	}
	return &Handler{
		manager:      manager,
		metrics:      metrics,
		extract:      extract,
		logger:       logger,
		authorizer:   options.Authorizer,
		maxCalls:     maxCalls,
		bodyPolicy:   bodyPolicy,
		multipart:    multipart,
		maxMultipart: maxMultipart,
		maxJSONBytes: maxJSON,
		now:          now,
	}
}

// newLogger 按 AMKR_LOG_FORMAT=json 选择日志格式，与参照实现的 logging 配置同源。
func newLogger() *slog.Logger {
	options := &slog.HandlerOptions{Level: slog.LevelInfo}
	if os.Getenv("AMKR_LOG_FORMAT") == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, options))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, options))
}

// jsonErrorResponse 构造与参照实现逐字节一致的错误信封。
//
// 字段顺序固定为 {"error": {"message": ...}}——错误体是给调用方程序解析的，
// 参照实现用一次性 dict 字面量构造，顺序即如此。
func jsonErrorResponse(message string) *canonical.Value {
	inner := canonical.NewObject()
	inner.Obj.Set("message", canonical.NewString(message))
	outer := canonical.NewObject()
	outer.Obj.Set("error", inner)
	return outer
}

// writeJSON 写出一个 JSON 响应。
//
// Content-Type 固定为 application/json（与 Starlette 的 JSONResponse 一致），
// Content-Length 显式写出（Starlette 也写），body 不做末尾换行。
func writeJSON(w http.ResponseWriter, statusCode int, payload *canonical.Value) {
	body := []byte(canonical.DumpsOrdered(payload))
	w.Header().Set("content-type", "application/json")
	w.Header().Set("content-length", strconv.Itoa(len(body)))
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

// secondsToDuration 把秒（float，可含小数）折算成 time.Duration。
//
// 配置里的超时全是浮点秒（Python 侧是 float），因此不能直接乘 time.Second——
// 那会丢掉小数部分（例如 0.5 秒会变成 0，从而**立即超时**）。
func secondsToDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
