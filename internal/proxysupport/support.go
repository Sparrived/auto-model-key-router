// Package proxysupport 实现代理转发前的请求体、路径、头部与错误响应的构造。
//
// 移植 auto_model_key_router/proxy_support.py。这一层做的是「把下游请求翻译成上游
// 请求」的纯计算：改写 model、合并任务参数、按上游方言调整参数名、挑选上游路径、
// 过滤头部、构造错误信封。它不碰网络，因此便于逐条测试。
//
// 三条贯穿全文件的约束：
//
//  1. **紧凑且不排序**的 JSON 输出（canonical.DumpsOrdered）。上游看到的字节必须与
//     参照实现一致，键顺序即构造顺序。
//  2. Python 的 `payload.get(k)` 与「键不存在」在若干处语义不同（如
//     reasoning_effort 的判断），因此需要区分 Lookup 返回 nil 与返回 null 值。
//  3. 真值判断一律走 canonical 的 Truthy，不要用 != "" / != 0——Python 的 `or`
//     会把 0、空串、空列表、false 都当作假。
package proxysupport

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/protocol"
)

// UnsupportedEndpointStatusCodes 标记「上游不支持该原生端点」。
//
// 只有这三个状态码说明端点不存在；其它状态码（含认证错误、参数错误）都说明端点存在。
var UnsupportedEndpointStatusCodes = []int{404, 405, 501}

// cloudflareUpstreamErrorReasons 是 521 的结构化错误模板。
//
// 移植 proxy_support.py:32。上游返回非 JSON 体时用它生成可读错误，避免把 HTML
// 错误页原样透传给调用方。
var cloudflareUpstreamErrorReasons = map[int]struct {
	Code    string
	Type    string
	Message string
	Reason  string
}{
	521: {
		Code:    "cloudflare_521",
		Type:    "upstream_cloudflare_error",
		Message: "上游服务不可用：Cloudflare 521 Web Server Is Down",
		Reason:  "Cloudflare 无法连接到上游源站，通常表示源站服务离线、端口未监听或防火墙拒绝 Cloudflare 连接。",
	},
}

// RequestRouteKind 把代理路径归类为 unified 路由类型：default / image / embeddings。
//
// 直接转调 keypool.RequestRouteKind：AMKR 只此一份路径分类，KeyPool 与本包共用它。
// 在这里再写一遍会让两处分类在新增路由类型时漂移，而漂移的表现是「某个端点偶尔
// 选错 key」，极难排查。
func RequestRouteKind(path string) string {
	return keypool.RequestRouteKind(path)
}

// UpstreamMode 把代理路径映射到上游方言，返回空串表示无对应方言。
//
// 移植 proxy_support.py:265。
func UpstreamMode(path string) string {
	switch path {
	case "chat/completions":
		return "openai"
	case "messages":
		return "anthropic"
	case "responses":
		return "responses"
	case "images/generations", "images/edits":
		return "images"
	case "embeddings":
		return "embeddings"
	}
	return ""
}

// UpstreamPath 选定上游路径。
//
// 移植 proxy_support.py:279。分支顺序不可交换，四个特例各自有原因：
//
//   - images/generations 无论是否 native 都按配置路径走。
//   - embeddings 没有转换语义（请求体本就是 OpenAI 形状），直接按配置路径转发。
//   - native 且方言已知：按该方言的配置路径走。
//   - messages/responses 且**带 model 字段**：说明要走转换而非原生，因此上游是
//     OpenAI 兼容端点，用 openai 的配置路径。
//
// images/edits 的处理是**与参照实现有意分叉**的一处（产品决策：修掉真实缺陷）。
//
// 参照实现在非 native 时**不查配置路由**，直接拼 "v1/images/edits"，导致用户在
// upstream_routes 里配的 images 路径对 /v1/images/edits 静默无效。这里改为：
// **显式配置了 images 路由就用它；没配置则保持 "v1/images/edits"**。
//
// 为什么不能简单地套用 images 的默认路由：`upstreamRouteDefaultPaths["images"]` 是
// "v1/images/generations"，若照搬，未配置路由的用户会把自己的**图片编辑**请求发到
// **图片生成**端点——那是更严重的行为变更。因此只在用户显式配置时才改道。
//
// native 形态不受影响：它走上面的 `native && mode != ""` 分支，本来就是查配置的
// （这也是参照实现里 edits 唯一会查配置的情形）。
func UpstreamPath(path string, payload *canonical.Value, native bool, upstreamRoutes map[string]string) (string, error) {
	mode := UpstreamMode(path)
	if path == "images/generations" {
		return config.UpstreamRoutePath(upstreamRoutes, "images")
	}
	if path == "images/edits" && !native {
		if route, ok := upstreamRoutes["images"]; ok && route != "" {
			return route, nil
		}
		return "v1/images/edits", nil
	}
	// embeddings 没有转换语义：请求体本来就是 OpenAI 形状，直接按配置路径转发。
	if path == "embeddings" {
		return config.UpstreamRoutePath(upstreamRoutes, "embeddings")
	}
	if native && mode != "" {
		return config.UpstreamRoutePath(upstreamRoutes, mode)
	}
	if (path == "messages" || path == "responses") && payload.IsObject() && payload.Lookup("model").Truthy() {
		return config.UpstreamRoutePath(upstreamRoutes, "openai")
	}
	if mode == "openai" {
		return config.UpstreamRoutePath(upstreamRoutes, "openai")
	}
	return "v1/" + path, nil
}

// JoinURL 拼接基址与路径，两侧的斜杠数量都不敏感。
//
// 移植 proxy_support.py:304：rstrip("/") + "/" + lstrip("/")。空 base 会得到 "/v1/x"
// （前导斜杠保留），这一点与朴素拼接不同，已由测试锁定。
func JoinURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// UpstreamHeaders 构造发往上游的头部。
//
// 移植 proxy_support.py:308。先剔除一批头部再补两个：
//
//   - 被剔除的键各有原因：authorization/x-api-key 要换成上游凭据；
//     host/content-length 由 HTTP 客户端重算（照抄会出错）；destination-addr 是
//     Cloudflare Workers 的私有头，不该外泄；accept-encoding 强制改写为 identity
//     以便自行处理编码；anthropic-version/anthropic-beta 属于下游方言，上游可能是
//     OpenAI 端点，照抄会误导上游。
//   - 补 Authorization 与 Accept-Encoding: identity。
//
// x-amkr-workspace 是 Go 侧新增的：**有意增补**，参照实现没有工作空间概念。它是
// AMKR 自己的路由状态，上游既看不懂也不该看到——与 x-api-key 同类，因此一并剔除。
// 剔除它不会改变既有调用方的行为（那些请求根本不带这个头）。
//
// 注意 `request.headers.items()` 在 Python 里对同名头只给出一个值（Starlette 按
// 逗号合并），Go 侧由调用方在传入前完成合并；本函数按 Go 的 map 语义处理。
func UpstreamHeaders(clientHeaders map[string][]string, apiKey string) map[string]string {
	blocked := map[string]bool{
		"authorization":     true,
		"host":              true,
		"content-length":    true,
		"destination-addr":  true,
		"accept-encoding":   true,
		"x-api-key":         true,
		"anthropic-version": true,
		"anthropic-beta":    true,
		"x-amkr-workspace":  true,
	}
	headers := make(map[string]string, len(clientHeaders)+2)
	for key, values := range clientHeaders {
		if blocked[strings.ToLower(key)] || len(values) == 0 {
			continue
		}
		// 键按**原始大小写**保留（Starlette 的 Headers.items() 逐个产出原始对，参照实现
		// 用 dict 推导式收集，因此 "X-Multi" 与 "x-multi" 是两个不同的键、各自存活）。
		// 同一大小写下重复出现时**后者胜**：dict 赋值覆盖先前的值。
		// 实测：raw=[(X-Multi,first),(x-multi,second)] -> {'X-Multi':'first','x-multi':'second'}。
		headers[key] = values[len(values)-1]
	}
	headers["Authorization"] = "Bearer " + apiKey
	headers["Accept-Encoding"] = "identity"
	return headers
}

// ResponseHeaders 过滤上游响应头后返回。
//
// 移植 proxy_support.py:329。剔除 content-encoding/content-length/transfer-encoding/
// connection：这四个描述的是**上游这一跳**的传输方式，透传给下游会撒谎——尤其是
// content-length，Go 的 ResponseWriter 会据此截断或报错。
func ResponseHeaders(upstreamHeaders map[string][]string) map[string]string {
	blocked := map[string]bool{
		"content-encoding":  true,
		"content-length":    true,
		"transfer-encoding": true,
		"connection":        true,
	}
	headers := make(map[string]string, len(upstreamHeaders))
	for key, values := range upstreamHeaders {
		lower := strings.ToLower(key)
		if blocked[lower] || len(values) == 0 {
			continue
		}
		// httpx 的 Headers 把同名头用 ", " **按序拼接**，并把键统一成小写——不是后者覆盖。
		// 实测：响应头 X-Multi: first/second/third 经 _response_headers 得到
		// {'x-multi': 'first, second, third'}。若按 last-wins 实现，下游会静默丢掉前两个值。
		headers[lower] = strings.Join(values, ", ")
	}
	return headers
}

// JSONBody 解析请求体为对象；空体、非对象、坏 JSON 都返回空对象。
//
// 移植 proxy_support.py:54。这里的宽容是刻意的：代理要先看 body 才知道模型，若因
// 为 body 不是对象就报错，会让上游自己返回更准确的错误变成 AMKR 的错误。
func JSONBody(body []byte) *canonical.Value {
	if len(body) == 0 {
		return canonical.NewObject()
	}
	value, err := canonical.Parse(body)
	if err != nil || !value.IsObject() {
		return canonical.NewObject()
	}
	return value
}

// decisionEndpointModel 是"不带 model 的结构化决策端点"回落到的默认模型名。
//
// 为什么需要一个约定名字：Laya / Jev 的 /v1/decide 与 /v1/classify 由上游按 Key 决定
// 用哪个模型，规范调用根本不带 model；而 AMKR 必须先有模型名才能选 Key。约定一个固定
// 名字之后，"哪些 Key 服务这类端点"就表达为"建一条叫这个名字的路由、把 Key 绑上去"，
// 不需要为这两个端点新增一类配置。代价是配置里必须有这条路由，否则请求以 404
// 「模型 laya 未配置」结束（见 ResolveModelID 的说明）。
const decisionEndpointModel = "laya"

// isDecisionEndpoint 报告路径是否为结构化决策端点（decide / classify）。
//
// 这两个路径在 UpstreamMode 里没有对应方言，因此上游路径按 "v1/" + path 拼，而请求体
// 在载荷没有 model 时被 UpstreamBody 原样转发——正是这类端点需要的透传形态。
func isDecisionEndpoint(path string) bool {
	return path == "decide" || path == "classify"
}

// ResolveModelID 从路径与载荷里取出请求的模型 ID。
//
// 移植 proxy_support.py:66。返回 (值, 是否存在)：`models` 路径返回空串但**存在**
// （表示"列出模型"，不需要模型）；载荷里 model 为假值时返回**不存在**。这个区分很
// 关键——空串意味着"无需模型"，不存在意味着"没有模型，走默认路由"。
//
// 与参照实现的差异（Go 侧新增）：路径为 decide / classify 时，载荷没有可用的 model
// **不是**错误，而是这类端点的规范调用形态，因此回落到 decisionEndpointModel 去选 Key，
// 而不是让调用方必然收到 400「请求体中缺少 model 字段」。参照实现在这里一视同仁，
// 于是那两个端点上的免 model 调用从来没能通过——这也是移植时能逐字节比对的地方，改动
// 它会让"对齐 Python"的用例失效，因此行为差异只在这两个路径上，并由
// TestResolveModelIDDefaultsDecisionEndpoints 单独锁定。
//
// 失败面随之变化：没配这条默认路由时是 404「模型 laya 未配置」而不是 400。对不接这类
// 端点的部署没有影响（它们的请求本来都带 model），对接入方来说 404 的文案也直接指出
// 了该建哪个模型。
func ResolveModelID(path string, payload *canonical.Value) (string, bool) {
	if path == "models" {
		return "", true
	}
	model := payload.Lookup("model")
	if model.Truthy() {
		return model.PyStr(), true
	}
	if isDecisionEndpoint(path) {
		return decisionEndpointModel, true
	}
	return "", false
}

// requestedModelKeyPattern 对应 Python 的 `(.+)\[([^\[\]]+)\]` 全匹配。
//
// 用 ^...$ 显式锚定：Go 的 regexp 默认不加锚，而 Python 用的是 re.fullmatch。
// 内层字符类排除方括号，所以 `a[[b]]` 不匹配（嵌套不算）。
var requestedModelKeyPattern = regexp.MustCompile(`^(.+)\[([^\[\]]+)\]$`)

// SplitRequestedModelKey 把 `model[key]` 拆成模型名与指定的 key 名。
//
// 移植 proxy_support.py:73。返回 (模型名, key名, 是否指定了key)。key 名会 strip。
//
// 注意**不做首尾 strip**：`"  gpt-4[x]  "` 因为 `.` 能匹配空格而整体匹配成功，
// 但模型名会带着前后空格（实测确认）。这与直觉不符，故由测试锁定。
func SplitRequestedModelKey(modelID string) (string, string, bool) {
	match := requestedModelKeyPattern.FindStringSubmatch(modelID)
	if match == nil {
		return modelID, "", false
	}
	return match[1], strings.TrimSpace(match[2]), true
}

// IsStreamRequest 判断是否为流式请求。
//
// 移植 proxy_support.py:80。用的是 `is True`，因此 1、"true" 都**不是**流式请求。
// 这个严格性是有意的：非布尔值说明调用方传错了，静默当作流式会让下游拿到 SSE
// 却以为是 JSON。
func IsStreamRequest(payload *canonical.Value) bool {
	stream := payload.Lookup("stream")
	return stream != nil && stream.IsBool() && stream.Bool
}

// ApplyTaskParams 把任务固定参数盖到载荷上；任务参数总是赢。
//
// 移植 proxy_support.py:138。返回**新对象**（浅拷贝后覆盖），不修改入参。
func ApplyTaskParams(payload, taskParams *canonical.Value) *canonical.Value {
	if !taskParams.IsObject() || taskParams.Len() == 0 {
		return payload
	}
	merged := payload.Clone()
	for _, key := range taskParams.Obj.Keys() {
		value, _ := taskParams.Obj.Get(key)
		merged.Obj.Set(key, value)
	}
	return merged
}

// TaskParamConflicts 列出调用方显式传了、但任务已经固定的采样参数。
//
// 移植 proxy_support.py:147。冲突交给调用方决定是否拒绝——静默覆盖会让调用方以为
// 自己的值生效了。
//
// **与参照实现的有意差异**：Python 把 reasoning_effort 排除在冲突之外（客户端框架常
// 自动带上，且与模型级设置一致）。Go 侧不再例外——任务路由是给别的 AI 服务用的，
// 不是给 Agent 用的：调用方显式传了任务已固定的 reasoning_effort，就该和 temperature
// 一样被明确拒绝，而不是收下再被静默覆盖。见 support_test.go 的
// TestTaskParamConflictsRejectsCallerReasoningEffort。
//
// 另一条特殊规则保留：任务的 `stop` 与载荷的 `stop_sequences` 视为同一个参数的冲突
// （Anthropic 方言里 stop 叫 stop_sequences），此时报告的是 `stop_sequences`。
// 同一条规则也适用于 `max_tokens` 与载荷的 `max_output_tokens`（Responses 方言里
// 输出上限叫 max_output_tokens，protocol.requestNormalizeChatCompatParameters 会把
// 它归一成 max_tokens）。缺了这条，调用方用 Responses 方言传的 max_output_tokens 会
// 被静默丢掉——而检查必须在归一化**之前**做：归一化之后载荷里已经没有这个键了。
//
// 返回顺序与 taskParams 的键顺序一致（`{}` 在 Python 3.7+ 保序）。
func TaskParamConflicts(payload, taskParams *canonical.Value) []string {
	if !taskParams.IsObject() || taskParams.Len() == 0 {
		return nil
	}
	var conflicts []string
	for _, key := range taskParams.Obj.Keys() {
		if _, ok := payload.Obj.Get(key); ok {
			conflicts = append(conflicts, key)
			continue
		}
		// 同一参数在不同方言里的别名，命中即为冲突（报告别名本身）。
		alias := ""
		switch key {
		case "stop":
			alias = "stop_sequences"
		case "max_tokens":
			alias = "max_output_tokens"
		}
		if alias != "" {
			if _, ok := payload.Obj.Get(alias); ok {
				conflicts = append(conflicts, alias)
			}
		}
	}
	return conflicts
}

// IsToolError 判断错误响应体是否与工具有关。
//
// 移植 proxy_support.py:169。用途是决定是否用「去掉非 function 工具」的请求体重试。
// 检测 message 与 param 两个字段里是否出现 tool/function，大小写不敏感。
// 坏 JSON 与非对象一律返回 false（保守：不重试）。
func IsToolError(content []byte) bool {
	data, err := canonical.Parse(content)
	if err != nil || !data.IsObject() {
		return false
	}
	errorValue := data.Lookup("error")
	if !errorValue.IsObject() {
		return false
	}
	message := strings.ToLower(fieldText(errorValue, "message"))
	param := strings.ToLower(fieldText(errorValue, "param"))
	return strings.Contains(message, "tool") || strings.Contains(message, "function") ||
		strings.Contains(param, "tool") || strings.Contains(param, "function")
}

// fieldText 取字段的字符串形式，缺失为 ""。
//
// 对应 Python 的 `str(error.get("message", ""))`：None 会变成 "None" 而不是 ""。
func fieldText(obj *canonical.Value, key string) string {
	value := obj.Lookup(key)
	if value == nil {
		return ""
	}
	return value.PyStr()
}

// FilterFunctionTools 只保留 type == "function" 且函数名非空的工具。
//
// 移植 proxy_support.py:188。tools 不是列表时**原样保留**（不改成空列表）——上游
// 可能接受别的形态，擅自改写会改变请求语义。
func FilterFunctionTools(payload *canonical.Value) *canonical.Value {
	adapted := payload.Clone()
	tools := adapted.Lookup("tools")
	if !tools.IsArray() {
		return adapted
	}
	kept := make([]*canonical.Value, 0, tools.Len())
	for _, tool := range tools.Items() {
		if !tool.IsObject() || tool.Lookup("type").StringValue() != "function" {
			continue
		}
		function := tool.Lookup("function")
		// 三重要求：function 是对象、且有非空的 name。
		if !function.IsObject() || !function.Lookup("name").Truthy() {
			continue
		}
		kept = append(kept, tool)
	}
	adapted.Obj.Set("tools", canonical.NewArray(kept...))
	return adapted
}

// UpstreamBody 构造发往默认（OpenAI 方言）上游的请求体。
//
// 移植 proxy_support.py:84。没有 model 字段时**原样返回入参 body**（不做任何改写），
// 这是"不是我们要代理的对话请求"的信号。
//
// native=true 表示调用方给的就是目标方言的体，基本原样转发，只把任务的固定参数按
// 方言改名：
//   - reasoning_effort 在原生体里没有对应概念，直接丢掉（与模型级设置一致）。
//   - Anthropic 的 messages 体只认 stop_sequences，直接塞 stop 会被上游静默忽略；
//     Responses 体两者都没有，保持原样。
func UpstreamBody(
	body []byte,
	payload *canonical.Value,
	modelID string,
	cfg *config.RouterConfig,
	stream bool,
	native bool,
	reasoningModelID string,
	taskParams *canonical.Value,
	path string,
) ([]byte, error) {
	if payload.Len() == 0 || payload.Lookup("model") == nil {
		return body, nil
	}
	upstreamPayload := payload.Clone()
	upstreamPayload.Obj.Set("model", canonical.NewString(modelID))

	if native {
		anthropicBody := upstreamPayload.Lookup("messages") != nil
		nativeParams := canonical.NewObject()
		if taskParams.IsObject() {
			for _, key := range taskParams.Obj.Keys() {
				if key == "reasoning_effort" {
					continue
				}
				value, _ := taskParams.Obj.Get(key)
				target := key
				if key == "stop" && anthropicBody {
					target = "stop_sequences"
				}
				// 目标键已存在时**保留原位置**：Python 的 dict 推导式按插入顺序
				// 构造 native_params，但 _apply_task_params 的 `{**payload, **params}`
				// 对已存在的键只更新值、不移动位置。因此要先删后设才能改位置——
				// 这里保持原位置，与 Python 一致。
				nativeParams.Obj.Set(target, value)
			}
		}
		upstreamPayload = ApplyTaskParams(upstreamPayload, nativeParams)
		return []byte(canonical.DumpsOrdered(upstreamPayload)), nil
	}

	if RequestRouteKind(path) == "embeddings" {
		// embeddings 体本来就是 OpenAI 形状（model/input/encoding_format 等），走
		// AdaptMessagePayload 会把 input 当成 Responses 的 input 改写成 messages，
		// 上游于是收到一个没有 input 的 chat 请求。这里只替换 model。
		return []byte(canonical.DumpsOrdered(upstreamPayload)), nil
	}

	reasoningTarget := reasoningModelID
	if reasoningTarget == "" {
		reasoningTarget = modelID
	}
	upstreamPayload = ApplyReasoningEffort(upstreamPayload, reasoningTarget, cfg)
	upstreamPayload = ApplyTaskParams(upstreamPayload, taskParams)
	upstreamPayload = protocol.AdaptMessagePayload(upstreamPayload)

	if stream {
		// include_usage 让上游在流末尾补一个 usage 块。non-dict 的 stream_options
		// 被整个丢弃并替换（Python 的 `if not isinstance(..., dict)`），不是合并。
		streamOptions := upstreamPayload.Lookup("stream_options")
		if !streamOptions.IsObject() {
			streamOptions = canonical.NewObject()
		}
		streamOptions.Obj.Set("include_usage", canonical.NewBool(true))
		upstreamPayload.Obj.Set("stream_options", streamOptions)
	}
	return []byte(canonical.DumpsOrdered(upstreamPayload)), nil
}

// UpstreamBodyWithFilteredTools 构造过滤掉非 function 工具的请求体。
//
// 移植 proxy_support.py:202。IsToolError 判定上游因工具格式报错时用它重试一次。
// 与 UpstreamBody 的差别只有一处：先过滤工具，且**不走 native 分支**（过滤只对
// OpenAI 方言有意义）。
func UpstreamBodyWithFilteredTools(
	body []byte,
	payload *canonical.Value,
	modelID string,
	cfg *config.RouterConfig,
	stream bool,
	reasoningModelID string,
	taskParams *canonical.Value,
) ([]byte, error) {
	if payload.Len() == 0 || payload.Lookup("model") == nil {
		return body, nil
	}
	upstreamPayload := FilterFunctionTools(payload)
	upstreamPayload.Obj.Set("model", canonical.NewString(modelID))

	reasoningTarget := reasoningModelID
	if reasoningTarget == "" {
		reasoningTarget = modelID
	}
	upstreamPayload = ApplyReasoningEffort(upstreamPayload, reasoningTarget, cfg)
	upstreamPayload = ApplyTaskParams(upstreamPayload, taskParams)
	upstreamPayload = protocol.AdaptMessagePayload(upstreamPayload)

	if stream {
		streamOptions := upstreamPayload.Lookup("stream_options")
		if !streamOptions.IsObject() {
			streamOptions = canonical.NewObject()
		}
		streamOptions.Obj.Set("include_usage", canonical.NewBool(true))
		upstreamPayload.Obj.Set("stream_options", streamOptions)
	}
	return []byte(canonical.DumpsOrdered(upstreamPayload)), nil
}

// ApplyReasoningEffort 按模型级设置与请求体里的 reasoning 补 reasoning_effort。
//
// 移植 proxy_support.py:232。优先级：
//
//  1. 模型级配置（config.reasoning_effort_by_model）——**覆盖**载荷里已有的值。
//  2. 载荷已有的 reasoning_effort——不动。
//  3. 载荷里的 reasoning.effort——提升为顶层 reasoning_effort（仅当顶层没有时）。
//
// 返回新对象，不修改入参。
func ApplyReasoningEffort(payload *canonical.Value, modelID string, cfg *config.RouterConfig) *canonical.Value {
	adapted := payload.Clone()
	if cfg != nil {
		if effort, ok := cfg.ReasoningEffortByModel[modelID]; ok && effort != "" {
			adapted.Obj.Set("reasoning_effort", canonical.NewString(effort))
			return adapted
		}
	}
	if _, exists := adapted.Obj.Get("reasoning_effort"); exists {
		return adapted
	}
	reasoning := adapted.Lookup("reasoning")
	if reasoning.IsObject() && reasoning.Lookup("effort").Truthy() {
		adapted.Obj.Set("reasoning_effort", reasoning.Lookup("effort"))
	}
	return adapted
}

// StructuredUpstreamError 生成 521 一类的结构化错误体。
//
// 移植 proxy_support.py:414。消息把 message 与 reason 用全角冒号拼起来。
// anthropic=true 时外层多包一层 type: error。
func StructuredUpstreamError(statusCode int, anthropic bool) *canonical.Value {
	template, ok := cloudflareUpstreamErrorReasons[statusCode]
	if !ok {
		return nil
	}
	message := template.Message + "：" + template.Reason
	inner := canonical.NewObject()
	// 字段顺序必须对齐参照实现，两个方言不同：
	//   OpenAI    -> message, type, code, status_code, reason
	//   Anthropic -> type, message, code, status_code, reason
	// 顺序会体现在响应字节里，且错误体是给调用方程序解析的，不能随意重排。
	if anthropic {
		inner.Obj.Set("type", canonical.NewString("api_error"))
	}
	inner.Obj.Set("message", canonical.NewString(message))
	if !anthropic {
		inner.Obj.Set("type", canonical.NewString(template.Type))
	}
	inner.Obj.Set("code", canonical.NewString(template.Code))
	inner.Obj.Set("status_code", canonical.NewIntValue(int64(statusCode)))
	inner.Obj.Set("reason", canonical.NewString(template.Reason))

	if anthropic {
		outer := canonical.NewObject()
		outer.Obj.Set("type", canonical.NewString("error"))
		outer.Obj.Set("error", inner)
		return outer
	}
	outer := canonical.NewObject()
	outer.Obj.Set("error", inner)
	return outer
}

// AnthropicErrorResponse 把任意上游错误体改写成 Anthropic 的错误信封。
//
// 移植 proxy_support.py:440。已经是 Anthropic 形态时**原样返回**（幂等）。
// 从 error.message、error（字符串）、顶层 message 依次取消息，都取不到时用
// 兜底文案。注意 `error.message` 为假值（空串/None）时**不会**退到顶层 message
// ——参照实现是 `if isinstance(error, dict): message = str(error.get("message") or message)`，
// 用的是 or，所以空串会退到默认值而不是顶层 message。实测确认。
func AnthropicErrorResponse(data *canonical.Value) *canonical.Value {
	if data.IsObject() &&
		data.Lookup("type").StringValue() == "error" &&
		data.Lookup("error").IsObject() {
		return data
	}
	message := "上游请求失败"
	if data.IsObject() {
		errorValue := data.Lookup("error")
		switch {
		case errorValue.IsObject():
			if text := errorValue.Lookup("message"); text.Truthy() {
				message = text.PyStr()
			}
		case errorValue.IsString():
			message = errorValue.Str
		case data.Lookup("message").Truthy():
			message = data.Lookup("message").PyStr()
		}
	}
	inner := canonical.NewObject()
	inner.Obj.Set("type", canonical.NewString("api_error"))
	inner.Obj.Set("message", canonical.NewString(message))
	outer := canonical.NewObject()
	outer.Obj.Set("type", canonical.NewString("error"))
	outer.Obj.Set("error", inner)
	return outer
}

// JSONErrorResponseFromContent 把上游的非 2xx 响应体规范化成错误信封。
//
// 移植 proxy_support.py:389。三种输入：
//
//  1. 可解析的 JSON：anthropic 时改写成 Anthropic 信封，否则原样透传。
//  2. 不可解析且状态码有结构化模板（521）：用模板生成。
//  3. 不可解析且无模板：用响应体文本（UTF-8 宽容解码）或"响应体为空"兜底。
//
// 第二种必须排在第三种之前，否则 521 会退化成把 Cloudflare 的 HTML 错误页塞进
// message 里，调用方看不到有用信息。
func JSONErrorResponseFromContent(statusCode int, content []byte, anthropic bool) *canonical.Value {
	data, err := canonical.Parse(content)
	if err != nil {
		if structured := StructuredUpstreamError(statusCode, anthropic); structured != nil {
			return structured
		}
		// decode("utf-8", errors="replace")：非法字节变成 U+FFFD，不报错。
		message := decodeUTF8Replacing(content)
		if message == "" {
			message = "上游返回 HTTP " + strconv.Itoa(statusCode) + "，且响应体为空"
		}
		// Anthropic 分支的字段顺序是 type 在前（参照实现一次性构造 dict）。
		if anthropic {
			inner := canonical.NewObject()
			inner.Obj.Set("type", canonical.NewString("api_error"))
			inner.Obj.Set("message", canonical.NewString(message))
			outer := canonical.NewObject()
			outer.Obj.Set("type", canonical.NewString("error"))
			outer.Obj.Set("error", inner)
			return outer
		}
		inner := canonical.NewObject()
		inner.Obj.Set("message", canonical.NewString(message))
		outer := canonical.NewObject()
		outer.Obj.Set("error", inner)
		return outer
	}
	if anthropic {
		return AnthropicErrorResponse(data)
	}
	return data
}

// decodeUTF8Replacing 复刻 Python 的 `bytes.decode("utf-8", errors="replace")`。
//
// 不能用 strings.ToValidUTF8：它把**连续**的非法字节折叠成一个 U+FFFD，而 Python 按
// Unicode 的"最大子部分"（maximal subpart, Unicode 3.9 D93）每段各产生一个。实测差异
// （字节 -> U+FFFD 个数）：
//
//	ff fe        Python 2 / ToValidUTF8 1
//	e4 b8        Python 1 / ToValidUTF8 1   （截断的 中）
//	f4 90 80 80  Python 4 / ToValidUTF8 1   （超出 U+10FFFF）
//	ed a0 80     Python 3 / ToValidUTF8 1   （UTF-16 代理）
//
// 也不能逐字节用 utf8.DecodeRune：它会把 `e4 b8` 拆成两个 U+FFFD，Python 只给一个。
//
// 这段文本会进入上游错误响应的 message 字段被调用方读到，因此必须逐字对齐。
func decodeUTF8Replacing(content []byte) string {
	const replacement = "\uFFFD"
	var sb strings.Builder
	for i := 0; i < len(content); {
		b0 := content[i]
		if b0 < 0x80 {
			sb.WriteByte(b0)
			i++
			continue
		}
		// 首字节决定续字节个数，以及**第二个字节**的合法区间。区间约束不可省：它同时
		// 排除过长编码（C0/C1、E0 8x、F0 8x）、代理对（ED A0-BF）与超出 U+10FFFF
		//（F4 90-BF）。这些情形下最大子部分只有首字节本身，于是每个字节各算一个 U+FFFD。
		extra := 0
		var lo, hi byte = 0x80, 0xBF
		switch {
		case b0 >= 0xC2 && b0 <= 0xDF:
			extra = 1
		case b0 == 0xE0:
			extra, lo = 2, 0xA0
		case b0 >= 0xE1 && b0 <= 0xEC:
			extra = 2
		case b0 == 0xED:
			extra, hi = 2, 0x9F
		case b0 == 0xEE || b0 == 0xEF:
			extra = 2
		case b0 == 0xF0:
			extra, lo = 3, 0x90
		case b0 >= 0xF1 && b0 <= 0xF3:
			extra = 3
		case b0 == 0xF4:
			extra, hi = 3, 0x8F
		default:
			// 非法首字节：80-BF（续字节误作首字节）、C0/C1（过长）、F5-FF。
			sb.WriteString(replacement)
			i++
			continue
		}
		// 贪心吃掉最长的合法前缀：先校验第二个字节（区间收窄过），再校验其余续字节。
		j := i + 1
		if j < len(content) && content[j] >= lo && content[j] <= hi {
			j++
			for j < i+1+extra && j < len(content) && content[j] >= 0x80 && content[j] <= 0xBF {
				j++
			}
		}
		if j == i+1+extra {
			sb.Write(content[i:j])
		} else {
			// 一个最大子部分只产生一个 U+FFFD，然后从 j 继续（不吞掉后续子部分）。
			sb.WriteString(replacement)
		}
		i = j
	}
	return sb.String()
}

// NativeEndpointProbeBody 构造探测原生端点用的最小请求体。
//
// 移植 proxy_support.py:489 与 544。两个方言的探测体不同：Anthropic 用
// max_tokens + messages，Responses 用 max_output_tokens + input。
func NativeEndpointProbeBody(mode string, modelID string) *canonical.Value {
	body := canonical.NewObject()
	body.Obj.Set("model", canonical.NewString(modelID))
	if mode == "anthropic" {
		body.Obj.Set("max_tokens", canonical.NewIntValue(1))
		message := canonical.NewObject()
		message.Obj.Set("role", canonical.NewString("user"))
		message.Obj.Set("content", canonical.NewString("test"))
		body.Obj.Set("messages", canonical.NewArray(message))
		return body
	}
	body.Obj.Set("input", canonical.NewString("test"))
	body.Obj.Set("max_output_tokens", canonical.NewIntValue(1))
	return body
}

// NativeEndpointProbeHeaders 构造探测原生端点用的头部。
//
// Anthropic 需要 anthropic-version（否则上游拒绝），Responses 不需要。
func NativeEndpointProbeHeaders(mode string, apiKey string) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Content-Type":  "application/json",
	}
	if mode == "anthropic" {
		headers["anthropic-version"] = "2023-06-01"
	}
	return headers
}

// NativeEndpointSupported 根据探测响应的状态码判断上游是否支持原生端点。
//
// 移植 proxy_support.py:507 与 560。只有 404/405/501 说明端点不存在；其它状态码
// （认证错误、参数错误）都说明端点存在。
func NativeEndpointSupported(statusCode int) (bool, string) {
	for _, unsupported := range UnsupportedEndpointStatusCodes {
		if statusCode == unsupported {
			return false, "unsupported"
		}
	}
	return true, "ok"
}
