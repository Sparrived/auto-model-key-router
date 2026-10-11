package proxy

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/protocol"
	"github.com/Sparrived/auto-model-key-router/internal/proxysupport"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// errDownstreamWrite 表示下游写失败（客户端断开或写出错）。
//
// 它只在 pump 内部用于短路循环；不需要与上游错误区分，因为两者的收尾完全相同：
// 静默终止 + 记 failed（proxy_handler.py:1137）。
var errDownstreamWrite = errors.New("下游写入失败")

// streamResponse 是待执行的流式响应。
//
// 与参照实现的结构差异：Python 的 StreamingResponse 拿着一个 async generator，
// body 在框架消费时才被拉取；Go 的 http.ResponseWriter 必须在同一个 goroutine 里
// 写，因此这里把「状态码 + 响应头」与「写函数」打包成一个值，由 Handle 立刻执行。
// 可观测语义完全一致：**状态码与响应头在下游收到任何字节之前就已确定**，中途失败
// 只能静默终止（proxy_handler.py:1137）。
type streamResponse struct {
	code      int
	headers   map[string]string
	mediaType string
	lifecycle *streamLifecycle

	// write 把整条流转发到下游；它负责逐块 Flush 与收尾（含释放 key）。
	write func(w http.ResponseWriter)
}

// statusCode 返回状态码（用在最终写回下游之前，供顶层判定是否走备选模型）。
func (s *streamResponse) statusCode() int { return s.code }

// writeTo 把流式响应写到下游。
//
// 响应头必须在 WriteHeader 之前全部设置好：Go 一旦写出状态行就不再接受新头。
// content-length 被显式剔除——流式响应的长度未知，留着上游的值会让下游按错误的
// 长度读取（Go 的 ResponseWriter 甚至可能直接截断）。
func (s *streamResponse) writeTo(w http.ResponseWriter) {
	headers := w.Header()
	for key, value := range s.headers {
		headers.Set(key, value)
	}
	headers.Del("content-length")
	if s.mediaType != "" {
		headers.Set("content-type", applyCharset(s.mediaType))
	}
	w.WriteHeader(s.code)
	s.write(w)
}

// markFallback 标记该响应来自备选模型。
//
// X-AMKR-Fallback 必须**在开始写**之前放进响应头里：一旦 WriteHeader 被调用就
// 不能再加头了（Go 会忽略并打印 superfluous 警告）。
func (s *streamResponse) markFallback() {
	if s.headers == nil {
		s.headers = map[string]string{}
	}
	s.headers["X-AMKR-Fallback"] = "true"
}

// bufferedResponse 读取整个上游响应体，按路径做响应转换或字节级透传。
//
// 移植 proxy_handler.py:932。转换只在两条路径上发生：
//   - messages：**总是**走 Anthropic 重建（原生上游的响应本来就是 Anthropic 形
//     态，重建是幂等的，参照实现也照做）；
//   - responses：只在**非原生**上游时重建（原生响应已经是 Responses 形态）。
func (h *Handler) bufferedResponse(
	context *RequestContext,
	key config.KeyConfig,
	response *http.Response,
	durationMS int64,
	nativeUpstream bool,
) *attemptResult {
	content, readErr := readResponseBody(response)
	if readErr != nil {
		// 响应体读到一半断开：参照实现会抛 httpx.RequestError 并冒泡，这里折算成
		// 502——与「上游无响应」时的形状一致。
		h.recordUpstreamFailure(context, key, durationMS, false)
		if response.StatusCode != http.StatusOK {
			// 状态码已经拿到了、只是体没读完：非 200 的口径仍要留痕。原因只能是读
			// 失败本身——上游原文已经不可用（这就是本分支的处境）。
			h.logUpstreamNotOK(context, key, response.StatusCode,
				readableReason("读取上游响应体失败: "+readErr.Error()), durationMS, false)
		}
		return jsonResult(http.StatusBadGateway,
			jsonErrorResponse("上游请求失败: RemoteProtocolError"))
	}
	h.recordUpstreamResponse(context, key, response.StatusCode, response.Header,
		content, durationMS, false, false)
	headers := proxysupport.ResponseHeaders(map[string][]string(response.Header))

	if response.StatusCode >= 400 {
		return errorResponseFromContent(response.StatusCode, content,
			context.Path == "messages")
	}
	if context.Path == "messages" {
		converted := protocol.AnthropicMessageResponse(parseJSONOrNil(content),
			context.RequestedModelID)
		if converted == nil {
			return jsonResult(http.StatusBadGateway, anthropicNonJSONError())
		}
		result := jsonResult(response.StatusCode, converted)
		result.headers = headers
		return result
	}
	if context.Path == "responses" && !nativeUpstream {
		converted, err := protocol.ResponsesResponse(parseJSONOrNil(content),
			context.RequestedModelID)
		if err != nil || converted == nil {
			return jsonResult(http.StatusBadGateway, responsesNonJSONError())
		}
		result := jsonResult(response.StatusCode, converted)
		result.headers = headers
		return result
	}
	return rawResult(response.StatusCode, headers, content)
}

// anthropicNonJSONError 是「上游非 JSON，无法转成 Anthropic 响应」的错误体。
//
// 移植 proxy_handler.py:964：状态码 502，外层 type=error、内层 type=api_error。
func anthropicNonJSONError() *canonical.Value {
	inner := canonical.NewObject()
	inner.Obj.Set("type", canonical.NewString("api_error"))
	inner.Obj.Set("message", canonical.NewString(
		"上游返回了非 JSON 响应，无法转换为 Anthropic Messages 响应"))
	outer := canonical.NewObject()
	outer.Obj.Set("type", canonical.NewString("error"))
	outer.Obj.Set("error", inner)
	return outer
}

// responsesNonJSONError 是「上游非 JSON，无法转成 Responses 响应」的错误体。
func responsesNonJSONError() *canonical.Value {
	return jsonErrorResponse("上游返回了非 JSON 响应，无法转换为 Responses 响应")
}

// readResponseBody 读空并关闭响应体。
func readResponseBody(response *http.Response) ([]byte, error) {
	defer func() { _ = response.Body.Close() }()
	return readAllLimited(response.Body, 0)
}

// streamingResponse 构造流式响应。
//
// 移植 proxy_handler.py:855。四处必须对齐：
//   - media_type 取自**上游** content-type；
//   - SSE 时补 cache-control / x-accel-buffering 两个反缓冲头；
//   - messages 且非原生上游：换成 Anthropic SSE 重建流，media_type 强制
//     text/event-stream；
//   - responses 且非原生上游：换成 Responses SSE 重建流，同样强制
//     text/event-stream。
func (h *Handler) streamingResponse(
	context *RequestContext,
	key config.KeyConfig,
	response *http.Response,
	upstreamURL string,
	started time.Time,
	nativeUpstream bool,
) *streamResponse {
	mediaType := upstreamContentType(response.Header)
	headers := proxysupport.ResponseHeaders(map[string][]string(response.Header))
	if isSSEMediaType(mediaType) {
		setStreamingHeaders(headers)
	}

	lifecycle := h.newLifecycle(context, key, response, upstreamURL, mediaType, started)
	// 首块沿用「发起请求时」算出的绝对截止时间：这样「首块的剩余窗口」就等于
	// stream_first_byte_timeout 减去等待响应头所花的时间（proxy_handler.py:530）。
	firstByteDeadline := started.Add(secondsToDuration(context.Config.StreamFirstByteTimeout))
	idleTimeout := secondsToDuration(context.Config.StreamIdleTimeout)

	var stream streamWriter
	switch {
	case context.Path == "messages" && !nativeUpstream:
		stream = newAnthropicStream(lifecycle, h.now)
		mediaType = "text/event-stream"
		setStreamingHeaders(headers)
	case context.Path == "responses" && !nativeUpstream:
		stream = newResponsesStream(lifecycle, h.now)
		mediaType = "text/event-stream"
		setStreamingHeaders(headers)
	default:
		stream = newRawStream(lifecycle, mediaType, h.now)
	}

	return &streamResponse{
		code:      response.StatusCode,
		headers:   headers,
		mediaType: mediaType,
		lifecycle: lifecycle,
		write: func(w http.ResponseWriter) {
			h.pumpUpstream(context, response, lifecycle, firstByteDeadline, idleTimeout,
				stream, mediaType, w)
		},
	}
}

// newLifecycle 构造流式生命周期记录器。
//
// keyPool 与指标写入分别注入：前者是 key 并发计数的唯一释放路径，后者是本包的
// MetricsSink。retry-after 在这里就解析好（上游响应头此时仍可用），避免收尾时
// 再去碰可能已经关闭的响应。
func (h *Handler) newLifecycle(
	context *RequestContext,
	key config.KeyConfig,
	response *http.Response,
	upstreamURL string,
	mediaType string,
	started time.Time,
) *streamLifecycle {
	return &streamLifecycle{
		modelID:          context.ModelID,
		keyName:          key.Name,
		statusCode:       response.StatusCode,
		requestedModelID: context.RequestedModelID,
		upstream:         upstreamURL,
		callerType:       context.CallerType,
		providerID:       key.Provider,
		poolName:         "",
		upstreamModelID:  upstreamModelOr(context.ModelID, key.UpstreamModel),
		mediaType:        mediaType,
		started:          started,
		ctx:              contextOf(context.Request),
		now:              context.now,
		keyPool:          context.pool(),
		retryAfter:       runtime.RetryAfterSeconds(response.Header.Get("retry-after"), context.now()),
		onFinish: func(record MetricRecord, _ bool) {
			h.recordMetric(context, key, record)
		},
	}
}

// upstreamModelOr 定义在 retry.go（与指标字段同源，只有一份）。

// applyCharset 复刻 Starlette 的 media_type 处理。//
// 只有 `text/*` 且未显式带 charset 时才补 `; charset=utf-8`（starlette
// responses.py 的 init_headers）。`application/json` 走 JSONResponse 的路径，由
// writeJSON 直接写死，不经过这里。
func applyCharset(mediaType string) string {
	if strings.HasPrefix(mediaType, "text/") &&
		!strings.Contains(strings.ToLower(mediaType), "charset=") {
		return mediaType + "; charset=utf-8"
	}
	return mediaType
}

// setStreamingHeaders 给 SSE 响应补两个反缓冲头。
//
// 移植 proxy_handler.py:1025：`x-accel-buffering: no` 是给 nginx 的，
// `cache-control: no-cache` 是给所有中间缓存的——少了它们，SSE 会被中间层攒成
// 一个大包，流式退化成一次性返回。
func setStreamingHeaders(headers map[string]string) {
	headers["cache-control"] = "no-cache"
	headers["x-accel-buffering"] = "no"
}

// isSSEMediaType 判断媒体类型是否为 text/event-stream（忽略参数与大小写）。
func isSSEMediaType(mediaType string) bool {
	if mediaType == "" {
		return false
	}
	base := mediaType
	if index := strings.Index(base, ";"); index >= 0 {
		base = base[:index]
	}
	return strings.EqualFold(strings.TrimSpace(base), "text/event-stream")
}

// upstreamContentType 取上游响应头里的 content-type。
//
// net/http 把同名头的多个值放进切片；参照实现的 `response.headers.get` 对重复头
// 返回逗号拼接的整体（httpx 的行为），因此这里也拼接——两者都会成为下游的
// content-type 字节。
func upstreamContentType(headers http.Header) string {
	values := headers.Values("content-type")
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, ", ")
}

// pumpUpstream 按两阶段超时语义读取上游并逐块转发。
//
// 这是全包最需要小心的一段：参照实现的两套超时**不能**用同一个 context 实现
// （Go 取消 context 会终止整条请求）：
//
//   - 首块：firstByteDeadline 是**绝对**截止时间，从发起请求那一刻算起，也就是
//     「等到响应头」的窗口；
//   - 之后每块：各自重置 idleTimeout，**没有总时长上限**。
//
// runtime.IterStreamBytes 正是这套语义的实现（它对每一块派生带超时的子 context，
// 父 context 的错误优先返回），所以这里只做编排。
//
// 错误处理与参照实现一致（proxy_handler.py:1137、1299、1392）：先把残余缓冲冲给
// 下游（非 SSE 路径会原样吐出，SSE 路径**丢弃**未完成的事件）、把错误写进日志，
// 然后**静默结束**——不写错误帧，因为响应头已经发出去了。
//
// 最后无条件调用 lifecycle.Finish：它是流式尝试**唯一**的 key 释放路径。
func (h *Handler) pumpUpstream(
	context *RequestContext,
	response *http.Response,
	lifecycle *streamLifecycle,
	firstByteDeadline time.Time,
	idleTimeout time.Duration,
	stream streamWriter,
	mediaType string,
	w http.ResponseWriter,
) {
	// 开场事件必须在下游开始读上游**之前**发出：参照实现在任何上游字节到来之前就
	// yield 了 Anthropic 的 message_start（proxy_handler.py:1217），因此首块超时
	// 这类失败也会在下游留下一个 message_start。
	stream.begin(w)
	failed := false
	err := runtime.IterStreamBytes(
		contextOf(context.Request), runtime.NewBodyStreamReader(response.Body),
		firstByteDeadline, idleTimeout,
		func(chunk []byte) error {
			// first_token_ms 记录的是**上游首字节**时间，不是下游首事件时间
			// （proxy_handler.py:1122、1238、1350 都在转发前 observe）。
			lifecycle.ObserveChunk(len(chunk))
			if !stream.push(w, chunk) {
				return errDownstreamWrite
			}
			return nil
		})
	if err != nil {
		failed = true
		payload := lifecycle.ErrorPayload(err, mediaType)
		h.logger.Warn("upstream stream error",
			"error_type", payload["error_type"],
			"error", payload["error"],
			"model_id", payload["model_id"],
			"requested_model_id", payload["requested_model_id"],
			"key_name", payload["key_name"],
			"upstream", payload["upstream"],
			"status_code", payload["status_code"],
			"content_type", payload["content_type"],
			"chunks", payload["chunks"],
			"bytes", payload["bytes"],
			"duration_ms", payload["duration_ms"])
	}
	if failed {
		// 错误路径：只有原样转发流会把 SSE 残余吐出去；两个转换流**不补**收尾事件
		// （参照实现里那些事件在 try 内、循环之后，异常会整段跳过）。
		stream.abort(w)
	} else {
		stream.flush(w)
	}
	lifecycle.Finish(failed, response.Body)
}

// sseEventSplitter 按 SSE 事件边界切分字节流。
//
// **必须逐块复用同一个实例**：跨 TCP 分片的事件（以及跨分片的多字节 UTF-8）要靠
// 它累积。移植 proxy_handler.py:1158 的 `_split_sse_events`，两处语义一致：
//   - 事件边界取 `\n\n` 与 `\r\n\r\n` 中**最早**出现的那个（不是先找到 `\n\n`）；
//   - 只产出**已完整**的事件，末尾残片留在缓冲里等下一块。
type sseEventSplitter struct {
	buffer []byte
}

// push 追加一块数据并返回其中已完整的事件。
func (s *sseEventSplitter) push(chunk []byte) [][]byte {
	s.buffer = append(s.buffer, chunk...)
	var events [][]byte
	for {
		index := earliestSeparator(s.buffer)
		if index < 0 {
			return events
		}
		separatorLength := 2
		if bytes.HasPrefix(s.buffer[index:], []byte("\r\n\r\n")) {
			separatorLength = 4
		}
		end := index + separatorLength
		// 复制出事件字节：下一轮的 append 可能复用底层数组，直接切片会互相踩。
		event := make([]byte, end)
		copy(event, s.buffer[:end])
		events = append(events, event)
		rest := make([]byte, len(s.buffer)-end)
		copy(rest, s.buffer[end:])
		s.buffer = rest
	}
}

// pending 返回尚未构成完整事件的缓冲（流结束时由 flush 处理）。
func (s *sseEventSplitter) pending() []byte { return s.buffer }

// clear 丢弃缓冲。
func (s *sseEventSplitter) clear() { s.buffer = nil }

// earliestSeparator 返回最早出现的 SSE 事件分隔符位置；没有则返回 -1。
//
// 参照实现构造 (index, separator) 列表后取最小 index（proxy_handler.py:1162），
// 因此两者同时存在时取更早的那个，而不是优先 `\n\n`。
func earliestSeparator(buffer []byte) int {
	lf := bytes.Index(buffer, []byte("\n\n"))
	crlf := bytes.Index(buffer, []byte("\r\n\r\n"))
	switch {
	case lf < 0:
		return crlf
	case crlf < 0:
		return lf
	case lf <= crlf:
		return lf
	}
	return crlf
}

// mergeUsage 把新解析到的 usage 合并进已有值（实现见 stream_writers.go，那里是
// 唯一的一份，避免两个文件各写一遍造成漂移）。
