package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/proxysupport"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// attempt 是一次尝试的结果：要么是一个已经就绪的非流式响应，要么是一段待执行的
// 流式响应。
//
// 之所以要接口而不是结构体：流式响应的状态码与响应头**必须在开始写之前**确定
// （决定是否走备选模型），而写完之后的字节不再属于任何「响应对象」。用两种具体
// 类型承载两种生命周期的结果，接口只暴露「状态码 + 写 + 标记备选」三件事。
type attempt interface {
	// statusCode 返回将写给下游（或已写）的状态码。
	statusCode() int
	// writeTo 把结果写到下游。
	writeTo(w http.ResponseWriter)
	// markFallback 标记该结果来自备选模型（必须在 writeTo 之前调用）。
	markFallback()
}

// handleSingleTarget 在同一个目标模型上循环尝试，直到成功或预算耗尽。
//
// 移植 proxy_handler.py:166。循环体的三个不变量：
//   - **排除集只在会换 Key 时增长**（rotatesKeys）；否则重试用的是同一个 Key，
//     排除它会让 next_key 直接失败；
//   - **首字节超时且不会换 Key 时立刻返回**：再等一轮只是把同一个慢上游的等待
//     时间乘以重试次数，对调用方没有任何新信息（proxy_handler.py:183）；
//   - **key 的释放恰好一次**：普通路径由 runAttempt 的 defer 负责，流式路径交给
//     streamLifecycle.Finish（proxy_handler.py:192、1148）。这一条是全项目最容易
//     泄漏在途计数/租约的地方。
func (h *Handler) handleSingleTarget(context *RequestContext) attempt {
	excluded := map[string]bool{}
	var lastError attempt

	for attemptIndex := 0; attemptIndex < context.Attempts; attemptIndex++ {
		selected, selectionError := h.selectKey(context, excluded)
		if selectionError != nil {
			return selectionError
		}
		key := *selected // 每轮独立副本，避免闭包捕获到下一轮的值
		result, retryError := h.runAttempt(context, key, attemptIndex, excluded)
		if retryError != nil {
			lastError = retryError
			continue
		}
		return result
	}
	if lastError != nil {
		return lastError
	}
	return jsonResult(http.StatusServiceUnavailable, jsonErrorResponse("没有可用 key"))
}

// runAttempt 执行一轮尝试并**恰好一次**释放 key（流式除外）。
//
// 返回值二选一非 nil：result 是要写回下游的响应，retryError 表示这一轮失败但
// 预算没用完、应当继续下一轮。
func (h *Handler) runAttempt(
	context *RequestContext,
	key config.KeyConfig,
	attemptIndex int,
	excluded map[string]bool,
) (attempt, attempt) {
	if rotatesKeys(context) {
		excluded[key.Name] = true
	}
	outcome := h.executeAttempt(context, key, attemptIndex)

	// 流式响应在写完后由 streamLifecycle.Finish 释放 key；其余路径在这里释放。
	// 判定必须用 outcome.stream，而不是 outcome.response——流式尝试的 response
	// 字段恒为 nil。
	if outcome.stream == nil {
		defer context.pool().ReleaseKey(context.ModelID, key.Name)
	}
	if outcome.retryErr != nil {
		if !outcome.retrySameKey && !rotatesKeys(context) {
			// 首字节超时且重试不会换 Key：直接返回失败，见函数顶部说明。
			return outcome.retryErr, nil
		}
		return nil, outcome.retryErr
	}
	if outcome.stream != nil {
		// 流式响应：key 由 streamLifecycle.Finish 释放（**唯一**释放路径）。
		return outcome.stream, nil
	}
	if outcome.response == nil {
		// 参照实现在这里抛 RuntimeError；对下游而言等价于 502。
		return internalErrorResult("proxy attempt completed without a response"), nil
	}
	return outcome.response, nil
}

// attemptResult 是一次尝试后可直接写回下游的**非流式**结果。
type attemptResult struct {
	status  int
	headers map[string]string
	body    []byte
	isJSON  bool
	// fallback 为 true 时补 X-AMKR-Fallback: true（仅备选模型成功时）。
	fallback bool
}

// statusCode 返回状态码。
func (r *attemptResult) statusCode() int {
	if r == nil {
		return http.StatusBadGateway
	}
	return r.status
}

// markFallback 标记来自备选模型。
func (r *attemptResult) markFallback() { r.fallback = true }

// writeTo 把结果写到下游。
//
// 头部按 proxysupport.ResponseHeaders 的规则设置（剔除逐跳头、重复头按序拼接）；
// isJSON 时把 content-type 固定为 application/json，与 Starlette 的 JSONResponse
// 一致（它会**覆盖**上游给的 content-type）。
func (r *attemptResult) writeTo(w http.ResponseWriter) {
	if r == nil {
		writeJSON(w, http.StatusBadGateway, jsonErrorResponse("上游请求失败"))
		return
	}
	headers := w.Header()
	for key, value := range r.headers {
		headers.Set(key, value)
	}
	if r.fallback {
		headers.Set("X-AMKR-Fallback", "true")
	}
	if r.isJSON {
		headers.Set("content-type", "application/json")
	}
	headers.Set("content-length", strconv.Itoa(len(r.body)))
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body)
}

// jsonResult 构造一个 JSON 结果。
func jsonResult(status int, payload *canonical.Value) *attemptResult {
	return &attemptResult{
		status:  status,
		headers: map[string]string{},
		body:    []byte(canonical.DumpsOrdered(payload)),
		isJSON:  true,
	}
}

// rawResult 构造一个字节级透传结果。
func rawResult(status int, headers map[string]string, body []byte) *attemptResult {
	if headers == nil {
		headers = map[string]string{}
	}
	return &attemptResult{status: status, headers: headers, body: body}
}

// internalErrorResult 表示编排器自身的不变量被破坏（不可能状态）。
func internalErrorResult(message string) *attemptResult {
	return jsonResult(http.StatusBadGateway, jsonErrorResponse(message))
}

// executeAttempt 执行一次尝试：选路径、探能力、发请求、按分支处理响应。
//
// 移植 proxy_handler.py:439。这是全链路最长的一段，分支顺序不可交换。
func (h *Handler) executeAttempt(context *RequestContext, key config.KeyConfig, attemptIndex int) attemptOutcome {
	upstreamRoutes := context.Config.UpstreamRoutesForBaseURL(key.BaseURL)
	upstreamModelID := upstreamModelOr(context.ModelID, key.UpstreamModel)

	// 自定义路由里显式配置了对应方言时，即使模型没开 native_first 也走原生
	// （proxy_handler.py:446）。
	customNativeRoute := (context.Path == "messages" && hasRoute(upstreamRoutes, "anthropic")) ||
		(context.Path == "responses" && hasRoute(upstreamRoutes, "responses"))
	useNative := context.UseNative || customNativeRoute
	nativeRoutePath := upstreamPathFor(context.Path, context.Payload, true, upstreamRoutes)

	if context.Path == "messages" && useNative {
		nativeSupport := context.pool().SupportsNativeEndpoint(key.BaseURL, nativeRoutePath)
		if nativeSupport == nil {
			// 能力探测在**请求路径上惰性执行**：这是新 base_url 的首次请求额外付出的
			// 1~2 次（计费的）上游调用。正结果永久缓存，负结果 600s/60s 过期。
			supported, reason := h.probeNativeMessages(context, key, upstreamModelID, nativeRoutePath)
			_ = context.pool().UpdateNativeEndpoint(key.BaseURL, supported, nativeRoutePath, reason)
			nativeSupport = &supported
		}
		useNative = *nativeSupport
	} else if context.Path == "responses" {
		nativeSupport := context.pool().SupportsNativeEndpoint(key.BaseURL, nativeRoutePath)
		if nativeSupport == nil {
			supported, reason := h.probeNativeResponses(context, key, upstreamModelID, nativeRoutePath)
			_ = context.pool().UpdateNativeEndpoint(key.BaseURL, supported, nativeRoutePath, reason)
			nativeSupport = &supported
		}
		useNative = *nativeSupport
	}

	upstreamPath := upstreamPathFor(context.Path, context.Payload, useNative, upstreamRoutes)
	upstreamURL := proxysupport.JoinURL(key.BaseURL, upstreamPath)

	// native=true 时上游体基本原样转发，且**不注入** stream_options（那是 OpenAI
	// 方言专有的补丁，见 proxy_support.py:84）。
	//
	// FlatPayload（multipart 表单）走**字节透传**：AMKR 只从表单里取了 model 用于
	// 路由，其余字段对它不透明，任何「重写成 JSON」的动作都会破坏请求。参照实现
	// 在这一档上不可比——它把 multipart 当 JSON，payload 恒为 `{}`，因此永远到不了
	// 这里（那条路径在模型校验阶段就 400 了）。
	var upstreamBody []byte
	var bodyErr error
	if context.FlatPayload {
		upstreamBody = context.OriginalRaw
	} else if useNative {
		upstreamBody, bodyErr = proxysupport.UpstreamBody(
			context.OriginalRaw, context.Payload, upstreamModelID, context.Config,
			false, true, context.ModelID, context.TaskParams, context.Path)
	} else {
		upstreamBody, bodyErr = proxysupport.UpstreamBody(
			context.OriginalRaw, context.Payload, upstreamModelID, context.Config,
			context.IsStream, false, context.ModelID, context.TaskParams, context.Path)
	}
	if bodyErr != nil {
		return attemptOutcome{
			retryErr:     internalErrorResult("构造上游请求体失败: " + bodyErr.Error()),
			retrySameKey: false,
		}
	}

	headers := upstreamHeaders(context, key.APIKey)
	if useNative && context.Path == "messages" {
		// 原生 Anthropic 端点必须要 anthropic-version；调用方没给时用参照实现的
		// 默认值 2023-06-01，anthropic-beta 则只在调用方给了才转发
		// （proxy_handler.py:522）。
		anthropicVersion := context.Request.Header.Get("anthropic-version")
		if anthropicVersion == "" {
			anthropicVersion = "2023-06-01"
		}
		headers["anthropic-version"] = anthropicVersion
		if anthropicBeta := context.Request.Header.Get("anthropic-beta"); anthropicBeta != "" {
			headers["anthropic-beta"] = anthropicBeta
		}
	}

	started := context.now()
	response, sendErr := h.sendUpstream(context, upstreamURL, headers, upstreamBody)
	finished := context.now()
	if sendErr != nil {
		name, retrySameKey := classifyOutcome(sendErr)
		durationMS := elapsedMS(started, finished)
		h.recordUpstreamFailure(context, key, durationMS, true)
		return attemptOutcome{
			retryErr:     jsonResult(http.StatusBadGateway, jsonErrorResponse("上游请求失败: "+name)),
			retrySameKey: retrySameKey,
		}
	}
	durationMS := elapsedMS(started, finished)

	if useNative && isUnsupportedEndpointStatus(response.StatusCode) {
		content, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		h.recordUpstreamResponse(context, key, response.StatusCode, response.Header,
			content, durationMS, true, false)
		h.logNativeFallback(key, response.StatusCode, upstreamURL, upstreamPath)
		if context.Path == "messages" || context.Path == "responses" {
			_ = context.pool().UpdateNativeEndpoint(key.BaseURL, false, nativeRoutePath, "unsupported")
		}
		fallbackPath := upstreamPathFor(context.Path, context.Payload, false, upstreamRoutes)
		fallbackURL := proxysupport.JoinURL(key.BaseURL, fallbackPath)
		var fallbackBody []byte
		var fallbackBodyErr error
		if context.FlatPayload {
			fallbackBody = context.OriginalRaw
		} else {
			fallbackBody, fallbackBodyErr = proxysupport.UpstreamBody(
				context.OriginalRaw, context.Payload, upstreamModelID, context.Config,
				context.IsStream, false, context.ModelID, context.TaskParams, context.Path)
		}
		if fallbackBodyErr != nil {
			return attemptOutcome{
				retryErr:     internalErrorResult("构造上游请求体失败: " + fallbackBodyErr.Error()),
				retrySameKey: false,
			}
		}
		fallbackStarted := context.now()
		fallbackResponse, fallbackErr := h.sendUpstream(
			context, fallbackURL, upstreamHeaders(context, key.APIKey), fallbackBody)
		fallbackFinished := context.now()
		if fallbackErr != nil {
			name, retrySameKey := classifyOutcome(fallbackErr)
			fallbackDuration := elapsedMS(fallbackStarted, fallbackFinished)
			h.recordUpstreamFailure(context, key, fallbackDuration, true)
			return attemptOutcome{
				retryErr:     jsonResult(http.StatusBadGateway, jsonErrorResponse("上游请求失败: "+name)),
				retrySameKey: retrySameKey,
			}
		}
		response = fallbackResponse
		durationMS = elapsedMS(fallbackStarted, fallbackFinished)
		started = fallbackStarted
		upstreamURL = fallbackURL
		useNative = false
	}

	if IsRetryableStatus(response.StatusCode) && attemptIndex+1 < context.Attempts {
		content, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		h.recordUpstreamResponse(context, key, response.StatusCode, response.Header,
			content, durationMS, true, false)
		return attemptOutcome{
			retryErr: errorResponseFromContent(response.StatusCode, content,
				context.Path == "messages"),
			retrySameKey: true,
		}
	}

	if context.IsStream && response.StatusCode >= 400 {
		content, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		h.recordUpstreamResponse(context, key, response.StatusCode, response.Header,
			content, durationMS, false, false)
		return attemptOutcome{response: errorResponseFromContent(
			response.StatusCode, content, context.Path == "messages")}
	}

	// 400 错误且与工具有关时，尝试过滤非 function 工具重试一次。
	// embeddings 没有 tools 可言，且它的 input 会被当 Responses 的 input 改写，
	// 所以这条重试路径对它不适用（proxy_handler.py:714）。
	if response.StatusCode == 400 && proxysupport.RequestRouteKind(context.Path) == "default" {
		content, _ := io.ReadAll(response.Body)
		if proxysupport.IsToolError(content) {
			h.recordUpstreamResponse(context, key, response.StatusCode, response.Header,
				content, durationMS, true, false)
			_ = response.Body.Close()
			// FlatPayload（multipart）没有 tools 可过滤，仍按字节透传。
			var filteredBody []byte
			var filteredErr error
			if context.FlatPayload {
				filteredBody = context.OriginalRaw
			} else {
				filteredBody, filteredErr = proxysupport.UpstreamBodyWithFilteredTools(
					context.OriginalRaw, context.Payload, upstreamModelID, context.Config,
					context.IsStream, context.ModelID, context.TaskParams)
			}
			if filteredErr != nil {
				return attemptOutcome{response: errorResponseFromContent(
					response.StatusCode, content, context.Path == "messages")}
			}
			retryStarted := context.now()
			retryResponse, retryErr := h.sendUpstream(
				context, upstreamURL, upstreamHeaders(context, key.APIKey), filteredBody)
			retryFinished := context.now()
			retryDuration := elapsedMS(retryStarted, retryFinished)
			if retryErr != nil {
				// 重试请求本身失败：返回**原始** 400 错误体，并记一次 key 失败
				// （proxy_handler.py:791）。注意这条指标行的 retried 是**默认的**
				// False——参照实现此处没有传 retried=True。
				h.recordUpstreamFailure(context, key, retryDuration, false)
				return attemptOutcome{response: errorResponseFromContent(
					response.StatusCode, content, context.Path == "messages")}
			}
			if retryResponse.StatusCode < 400 {
				response = retryResponse
				durationMS = retryDuration
				started = retryStarted
			} else {
				retryContent, _ := io.ReadAll(retryResponse.Body)
				_ = retryResponse.Body.Close()
				h.recordUpstreamResponse(context, key, retryResponse.StatusCode,
					retryResponse.Header, retryContent, retryDuration, false, false)
				return attemptOutcome{response: errorResponseFromContent(
					response.StatusCode, content, context.Path == "messages")}
			}
		} else {
			_ = response.Body.Close()
			h.recordUpstreamResponse(context, key, response.StatusCode, response.Header,
				content, durationMS, false, false)
			return attemptOutcome{response: errorResponseFromContent(
				response.StatusCode, content, context.Path == "messages")}
		}
	}

	if context.IsStream {
		return attemptOutcome{stream: h.streamingResponse(
			context, key, response, upstreamURL, started, useNative)}
	}
	return attemptOutcome{response: h.bufferedResponse(
		context, key, response, durationMS, useNative)}
}

// attemptOutcome 是一次尝试的结果。
//
// 对应参照实现的 AttemptOutcome（proxy_handler.py:94）。response 与 stream 二选
// 一非空：非流式已经在内存里（可以随意检查状态码），流式则是一个待执行的写函数
// （状态码一旦写出去就不能再改，因此没有可读的 status 字段）。
type attemptOutcome struct {
	response attempt
	retryErr attempt
	stream   *streamResponse

	// retrySameKey 为 false 表示「上游慢到首字节超时」，用同一个 key 原地重试没有
	// 意义（proxy_handler.py:99）。其余失败都为 true。
	retrySameKey bool
}

// selectKey 挑一个 key，或返回错误响应。
//
// 移植 proxy_handler.py:387。两处异常各自对应一个响应：
//   - KeyError ⇒ 404「模型未配置」；
//   - RuntimeError ⇒（访问密钥且指定了 key 时）403，否则 503 且异常文本原样回给调用方
//     （key_pool.py 的文案如「模型 X 没有可用 key」是对外契约）。
func (h *Handler) selectKey(context *RequestContext, excluded map[string]bool) (*config.KeyConfig, attempt) {
	pool := context.pool()
	if context.RequestedKeyName != nil && *context.RequestedKeyName != "" {
		key, err := pool.KeyByName(context.ModelID, *context.RequestedKeyName, context.AccessKey)
		if err != nil {
			return nil, h.keySelectionFailure(context, err)
		}
		pool.AcquireKey(context.ModelID, key.Name)
		return &key, nil
	}
	excludedNames := make([]string, 0, len(excluded))
	for name := range excluded {
		excludedNames = append(excludedNames, name)
	}
	affinity := ""
	if context.CacheAffinityKey != nil {
		affinity = *context.CacheAffinityKey
	}
	key, err := pool.NextKey(context.ModelID, excludedNames, context.AccessKey, affinity)
	if err != nil {
		return nil, h.keySelectionFailure(context, err)
	}
	return &key, nil
}

// keySelectionFailure 把选 key 的错误折算成下游响应。
//
// 判定的依据是 keypool 的两个哨兵错误：`ErrUnknownModel` 对应 Python 的
// `KeyError(model_id)`（⇒ 404「模型未配置」），`ErrNoUsableKey` 对应
// `RuntimeError("模型 X 没有可用 key")`（⇒ 503，文本原样回给调用方）。用哨兵错误
// 而不是 Python 的异常类做分支，因为后者是实现细节。
func (h *Handler) keySelectionFailure(context *RequestContext, err error) attempt {
	if errors.Is(err, keypool.ErrUnknownModel) || errors.Is(err, keypool.ErrNoUnifiedModel) {
		h.logModelNotConfigured(context.Path, context.RequestedModelID, context.ModelID,
			context.Workspace, "key_selection_failed")
		return jsonResult(http.StatusNotFound, jsonErrorResponse(
			"模型 "+context.RequestedModelID+" 未配置；请先在 AMKR 的模型设置中配置该模型"))
	}
	if context.AccessKey != nil && context.RequestedKeyName != nil && *context.RequestedKeyName != "" {
		return jsonResult(http.StatusForbidden, jsonErrorResponse(
			"访问密钥 "+context.AccessKey.Name+" 无权访问模型 key: "+context.RequestedModelName+
				"["+*context.RequestedKeyName+"]"))
	}
	return jsonResult(http.StatusServiceUnavailable, jsonErrorResponse(err.Error()))
}

// hasRoute 报告显式配置了某个方言的上游路由。
func hasRoute(routes map[string]string, mode string) bool {
	_, ok := routes[mode]
	return ok
}

// isUnsupportedEndpointStatus 报告状态码是否意味着「原生端点不存在」。
//
// 只有 404/405/501 判为不支持；401/403/429/5xx 都说明端点存在
// （proxy_support.py:507）。
func isUnsupportedEndpointStatus(statusCode int) bool {
	for _, candidate := range proxysupport.UnsupportedEndpointStatusCodes {
		if candidate == statusCode {
			return true
		}
	}
	return false
}

// logNativeFallback 记录原生端点回退到 chat/completions。
//
// 5xx 用 Error 级别、其余用 Warn，对齐参照实现（proxy_handler.py:591）。这条日志
// 是排查「为什么我的 Anthropic 请求变成了 OpenAI 格式」的唯一线索。
func (h *Handler) logNativeFallback(key config.KeyConfig, statusCode int, url, upstreamPath string) {
	attributes := []any{
		"status", statusCode,
		"base_url", key.BaseURL,
		"upstream_path", upstreamPath,
		"upstream_url", url,
	}
	if statusCode >= 500 {
		h.logger.Error("原生端点不支持，回退到 chat/completions", attributes...)
		return
	}
	h.logger.Warn("原生端点不支持，回退到 chat/completions", attributes...)
}

// sendUpstream 发起一次上游请求。
//
// 时间预算的三层语义（proxy_handler.py:536）：
//   - 流式：等到**响应头**的窗口由 upstream.Client 的 FirstByteTimeout 约束
//     （装配时取自 config.stream_first_byte_timeout）；
//   - 响应体的首块由 runtime.IterStreamBytes 的绝对 deadline 约束；
//   - 之后每块各自重置 idle timeout，且没有总时长上限。
//
// 本函数只负责第一层；第二、三层在 pumpUpstream 里。
func (h *Handler) sendUpstream(context *RequestContext, upstreamURL string, headers map[string]string, body []byte) (*http.Response, error) {
	if !context.upstreamCalls.take() {
		return nil, &upstreamCapError{message: context.upstreamCalls.exhaustedMessage()}
	}
	client := upstreamClientOf(context.Runtime)
	if client == nil {
		return nil, errors.New("上游客户端未装配")
	}
	request, err := newUpstreamRequest(context, upstreamURL, headers, body)
	if err != nil {
		return nil, err
	}
	if context.IsStream {
		return client.DoStream(request)
	}
	return client.Do(request)
}

// newUpstreamRequest 构造上游请求。
//
// query string 走 reencodeQuery：参照实现把 Starlette 的 query_params 交给 httpx
// 的 params（proxy_support.py:355），httpx 对 Mapping 会 urlencode，因此**不是**
// 字节级透传。实测规则与理由见 query.go 顶部注释——这是与迁移方案 §4.5
// 「query string 原样转发」表述不符的一处，以参照实现为准。
func newUpstreamRequest(context *RequestContext, upstreamURL string, headers map[string]string, body []byte) (*http.Request, error) {
	target := upstreamURL
	if rawQuery := context.Request.URL.RawQuery; rawQuery != "" {
		target += "?" + reencodeQuery(rawQuery)
	}
	request, err := http.NewRequestWithContext(
		contextOf(context.Request), context.Request.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return request, nil
}

// upstreamHeaders 构造上游请求头。
//
// net/http 的 http.Header 已经把键规范成 CanonicalMIMEHeaderKey，因此这里直接交给
// proxysupport（它按**传入的键大小写**保留）。**已知差异**：Starlette 能同时看到
// "X-Multi" 与 "x-multi" 两个键，Go 侧已被 http.Header 合并为一个——只影响「调用
// 方故意用两种大小写发同一个头」的畸形请求。
func upstreamHeaders(context *RequestContext, apiKey string) map[string]string {
	return proxysupport.UpstreamHeaders(map[string][]string(context.Request.Header), apiKey)
}

// upstreamCapError 表示上游调用次数达到上限。
//
// **有意增补**（见 DefaultMaxUpstreamCallsPerRequest）：参照实现没有任何上限。
// 它被归类成「连接错误」（可原地重试）以便沿用既有分支，但 take() 对超限后的每次
// 调用都返回 false，所以不会真的多打上游。
type upstreamCapError struct{ message string }

func (e *upstreamCapError) Error() string { return e.message }

// classifyOutcome 把 Go 的上游错误折算成「Python 异常类名 + 是否可原地重试」。
//
// 参照实现用 `exc.__class__.__name__` 填进错误文案（proxy_handler.py:566），并用
// `isinstance(exc, UpstreamFirstByteTimeout)` 判断能否原地重试。Go 没有同一个异常
// 树，因此用 upstream.Classify 折算；文案保留 Python 的类名，因为它是**对外契约**
// （错误体里的 `上游请求失败: ReadTimeout` 会被客户端解析）。
func classifyOutcome(err error) (string, bool) {
	var capError *upstreamCapError
	if errors.As(err, &capError) {
		return "ConnectError", false
	}
	switch upstreamClassify(err) {
	case causeTimeout:
		return "ReadTimeout", false
	case causeConnection, causeDNS, causeTLS:
		return "ConnectError", true
	}
	return "RequestError", true
}

// DefaultExtractUsage 从上游响应体里取出 usage 对象。
//
// 移植 metrics.extract_usage（metrics.py:1054）：依次看顶层 usage、response.usage、
// message.usage，只接受对象。**只认对象**这一点很关键——`usage` 是数组或字符串时
// 参照实现返回 None，而不是把它交给归一化逻辑。
func DefaultExtractUsage(body *canonical.Value) *canonical.Value {
	if body == nil || !body.IsObject() {
		return nil
	}
	if usage := body.Lookup("usage"); usage.IsObject() {
		return usage
	}
	if response := body.Lookup("response"); response.IsObject() {
		if usage := response.Lookup("usage"); usage.IsObject() {
			return usage
		}
	}
	if message := body.Lookup("message"); message.IsObject() {
		if usage := message.Lookup("usage"); usage.IsObject() {
			return usage
		}
	}
	return nil
}

// parseJSONOrNil 解析上游响应体为 JSON；失败或为空时返回 nil。
//
// 对应参照实现的 _json_bytes（proxy_support.py:459）：空体、非法 JSON 都返回 None，
// 而不是空对象。
func parseJSONOrNil(content []byte) *canonical.Value {
	if len(content) == 0 {
		return nil
	}
	value, err := canonical.Parse(content)
	if err != nil {
		return nil
	}
	return value
}

// errorResponseFromContent 把上游错误体折算成下游的错误信封。
//
// 参照实现的 _json_error_response_from_content（proxy_support.py:389）返回的
// JSONResponse **不带**上游响应头，因此这里也不带。
func errorResponseFromContent(statusCode int, content []byte, anthropic bool) *attemptResult {
	return jsonResult(statusCode,
		proxysupport.JSONErrorResponseFromContent(statusCode, content, anthropic))
}

// recordUpstreamResponse 写一条带上游响应的指标行，并按状态码回写 key 健康。
//
// 移植 proxy_handler.py:1040。顺序固定：先写指标、再回写健康。状态码 <400 记成功
// （**删除**冷却状态）；可重试状态码记失败（可能进入冷却）。
func (h *Handler) recordUpstreamResponse(
	context *RequestContext,
	key config.KeyConfig,
	statusCode int,
	headers http.Header,
	content []byte,
	durationMS int64,
	retried bool,
	failed bool,
) {
	status := statusCode
	h.recordMetric(context, key, MetricRecord{
		StatusCode:   &status,
		Usage:        h.extractUsage(content),
		Retried:      retried,
		Failed:       failed,
		DurationMS:   durationMS,
		FirstTokenMS: durationMS,
	})
	if statusCode < 400 {
		context.pool().MarkSuccess(context.ModelID, key.Name)
		return
	}
	if IsRetryableStatus(statusCode) {
		retryAfter := runtime.RetryAfterSeconds(headers.Get("retry-after"), context.now())
		context.pool().MarkFailure(context.ModelID, key.Name, &status, retryAfter)
	}
}

// extractUsage 解析响应体并抽取 usage。
func (h *Handler) extractUsage(content []byte) *canonical.Value {
	return h.extract(parseJSONOrNil(content))
}

// recordUpstreamFailure 写一条「上游请求失败」的指标行并记一次 key 失败。
//
// 这条路径上 duration_ms 与 first_token_ms **同为**耗时（proxy_handler.py:549-562），
// 因为没有任何字节回来。
func (h *Handler) recordUpstreamFailure(context *RequestContext, key config.KeyConfig, durationMS int64, retried bool) {
	h.recordMetric(context, key, MetricRecord{
		StatusCode:   nil,
		Retried:      retried,
		Failed:       true,
		DurationMS:   durationMS,
		FirstTokenMS: durationMS,
	})
	context.pool().MarkFailure(context.ModelID, key.Name, nil, nil)
}

// recordMetric 是 MetricsSink 的薄包装，顺带补齐上下文派生的字段。
//
// 这样调用点只需要给出真正因分支而异的部分（状态码、usage、duration、retried、
// failed），其余字段的填充只有一个来源，避免某条分支漏填 provider/upstream_model。
func (h *Handler) recordMetric(context *RequestContext, key config.KeyConfig, record MetricRecord) {
	if h.metrics == nil {
		return
	}
	record.ModelID = context.ModelID
	record.KeyName = key.Name
	record.RequestedModelID = context.RequestedModelID
	record.CallerType = context.CallerType
	provider := key.Provider
	record.ProviderID = &provider
	// pool_name 在本迁移里恒为 nil：v4 的 KeyConfig 没有 pool 字段，参照实现里
	// 的 key.pool 因此恒为 None（metrics.record 的 pool_name 可选参数）。
	record.PoolName = nil
	upstreamModel := upstreamModelOr(context.ModelID, key.UpstreamModel)
	record.UpstreamModelID = &upstreamModel
	// 工作空间已在读请求头时归一化（handler.go），这里直接沿用，不再判空。
	record.Workspace = context.Workspace
	// 访问密钥身份同样只在解析鉴权时确定（handler.go 的 authorize）。非访问密钥的
	// 请求保持空串，落库时不写 request_access_key 旁挂表。
	if context.AccessKey != nil {
		record.AccessKeyID = context.AccessKey.ID
	}
	// 请求来源同样只在这里填：流式与非流式两条路径最终都汇到本函数
	// （streamLifecycle 的 onFinish 也调用它），漏一处就会出现「流式请求没有来源」。
	if context.Request != nil {
		record.ClientAddr = context.Request.RemoteAddr
		record.UserAgent = context.Request.UserAgent()
	}
	// 请求形态（流式与否、API 格式、推理强度）同理：三个字段在 prepare 时就定了，
	// 每次尝试写下的都是同一份读数，重试不会让它们漂移。
	record.Stream = context.IsStream
	record.APIFormat = context.Path
	record.ReasoningEffort = context.ReasoningEffort
	_ = h.metrics.Record(contextOf(context.Request), record)
}

// upstreamModelOr 返回上游模型名，缺省时退回请求模型 ID。
//
// 参照实现到处写 `key.upstream_model or context.model_id`，包括指标行的
// upstream_model_id（proxy_handler.py:561）。
func upstreamModelOr(modelID, upstreamModel string) string {
	if upstreamModel != "" {
		return upstreamModel
	}
	return modelID
}

// elapsedMS 返回两个时刻之间的毫秒数。
//
// 用 roundHalfEven 而不是 time.Duration.Milliseconds()：Python 的 round 是银行家
// 舍入，而 .Milliseconds() 是**截断**。这是会写进指标库的数值，必须一致
// （同 streamLifecycle.ElapsedMS 的处理）。
func elapsedMS(started, finished time.Time) int64 {
	return roundHalfEven(finished.Sub(started).Seconds() * 1000)
}

// roundHalfEven 复刻 Python 的 round()（无 ndigits）：四舍六入五成双。
func roundHalfEven(value float64) int64 {
	floor := int64(value)
	remainder := value - float64(floor)
	switch {
	case remainder > 0.5:
		return floor + 1
	case remainder < 0.5:
		return floor
	}
	if floor%2 == 0 {
		return floor
	}
	return floor + 1
}
