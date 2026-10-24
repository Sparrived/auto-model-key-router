package proxy

import (
	"net/http"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/protocol"
)

// 本文件是三个流式转发状态机。它们共同的结构是：pumpUpstream 负责超时、观察
// 首字节、收尾；每个 writer 只负责「一块上游数据 → 若干下游字节」。
//
// 每个事件后 Flush。聊天 SSE 的错误与完成判定由 pumpUpstream 统一处理，失败时
// 丢弃残片而不补成功收尾；first_token_ms 同样由 pumpUpstream 观察上游首字节。

// streamWriter 是三种流式转发的统一接口。
type streamWriter interface {
	// begin 在下游开始读取上游之前产出「开场事件」。
	//
	// 只有 Anthropic 重建流需要它：message_start 必须在**任何**上游字节之前发出
	// （proxy_handler.py:1217），否则首块超时这类失败路径留下的下游字节会与参照
	// 实现不同。其余两种是空操作。
	begin(w http.ResponseWriter) error
	// push 区分下游写失败与上游转换失败，避免把客户端断开记到 key 健康上。
	push(w http.ResponseWriter, chunk []byte) error
	// flush 处理流**正常结束**时的残余缓冲与收尾事件。
	flush(w http.ResponseWriter) error
	// abort 处理流因错误终止时的残余。
	//
	// 两者的区别是参照实现里一个有、一个没有的分支：_stream_upstream 在 except
	// 里也把 SSE 残余吐出去（proxy_handler.py:1138），而两个转换流的收尾事件写在
	// try 内、async for 之后（proxy_handler.py:1247、1359），一旦循环抛错就被整段
	// 跳过——不会补 content_block_stop / message_delta / message_stop。
	abort(w http.ResponseWriter)
}

// rawStream 是「字节级原样转发（SSE 时按事件边界切分）」的流式处理器。
//
// 移植 proxy_handler.py:1081 的 _stream_upstream。
//
// 它虽然不改写下游字节，但**必须**顺带抽 usage：`/v1/chat/completions` 的流式请求
// 恒走这一条（messages / responses 才走重建流），少了这一步所有 chat 流式请求的
// token 用量都会记成 0。
type rawStream struct {
	isSSE    bool
	splitter *sseEventSplitter
	// lifecycle 用于回写抽取到的 usage；nil 时只转发（等价于不记账）。
	lifecycle *streamLifecycle
	// usageSplitter 按**行**缓冲以抽取 usage，与按事件边界切分的 splitter 是两件事
	// （对应参照实现里并列的 buffer 与 sse_buffer 两个局部变量）。
	usageSplitter *protocol.SSESplitter
}

// newRawStream 构造原样转发流。
func newRawStream(lifecycle *streamLifecycle, mediaType string, _ func() time.Time) *rawStream {
	stream := &rawStream{
		isSSE:         isSSEMediaType(mediaType),
		lifecycle:     lifecycle,
		usageSplitter: protocol.NewSSESplitter(),
	}
	if stream.isSSE {
		stream.splitter = &sseEventSplitter{}
	}
	return stream
}

// absorbUsage 从本块里抽取 usage 并合并进生命周期记录。
//
// 移植 proxy_handler.py:1123 的 `_stream_usage`：逐行找 `data:` 载荷，**本块最后一处**
// usage 生效（同名函数在三个流式路径上共用）。这里复刻两个容易漏掉的细节：
//
//   - 该动作对 **SSE 与非 SSE 一律执行**（参照实现把它放在 is_sse 分支之前），
//     因此原生 Anthropic 端点的 message_start / message_delta 也能取到 usage；
//   - 行缓冲跨块保留，被 TCP 分片切开的 usage 行能拼回。参照实现只在收到换行时
//     才处理该行，**流末尾没有换行的半行会被丢弃**——这里保持同一语义（不调用
//     Flush），避免在多出一个 usage 的同时改变与参照实现的可比性。升级路径：若
//     实测有上游以无换行结尾发送 usage，再把残余行补一个换行喂一次。
func (s *rawStream) absorbUsage(chunk []byte) {
	if s.lifecycle == nil || s.usageSplitter == nil {
		return
	}
	var usage *canonical.Value
	for _, line := range s.usageSplitter.Push(chunk) {
		if extracted := protocol.ExtractUsage(line.Payload); extracted != nil {
			usage = extracted
		}
	}
	if usage != nil {
		s.lifecycle.SetUsage(mergeUsage(s.lifecycle.Usage(), usage))
	}
}

func (s *rawStream) begin(_ http.ResponseWriter) error { return nil }

// 失败时丢弃未完成事件，避免它与随后发出的错误帧拼成一个无效事件。
func (s *rawStream) abort(_ http.ResponseWriter) {
	if s.splitter != nil {
		s.splitter.clear()
	}
}

// push 转发一块：SSE 路径按事件切分，其余原样吐出。
//
// usage 抽取排在转发**之前**，与参照实现的 `_stream_usage` 调用位置一致
// （proxy_handler.py:1123）：下游写失败时该块已经计过 usage，行为可比。
func (s *rawStream) push(w http.ResponseWriter, chunk []byte) error {
	s.absorbUsage(chunk)
	if s.isSSE {
		for _, event := range s.splitter.push(chunk) {
			if !writeBytes(w, event) {
				return errDownstreamWrite
			}
		}
		return nil
	}
	if !writeBytes(w, chunk) {
		return errDownstreamWrite
	}
	return nil
}

// flush 处理流结束时的残余。
//
// 参照实现只对 SSE 路径保留残余并外发（proxy_handler.py:1133）；非 SSE 路径的
// 数据在 push 里已经全部吐出，没有残余。注意这里外发的是**未完成事件**（可能不含
// `\n\n`），与「一个完整 SSE 事件」不同——参照实现就是这么写的。
func (s *rawStream) flush(w http.ResponseWriter) error {
	if s.splitter == nil || len(s.splitter.pending()) == 0 {
		return nil
	}
	ok := writeBytes(w, s.splitter.pending())
	s.splitter.clear()
	if !ok {
		return errDownstreamWrite
	}
	return nil
}

// anthropicStream 把上游 OpenAI SSE 重建为 Anthropic Messages SSE。
//
// 移植 proxy_handler.py:1175 的 _stream_anthropic_messages。事件切分与 usage 提取
// 都交给 protocol.AnthropicStreamState（它内部用字节级 SSESplitter，顺带修掉了
// 参照实现跨分片多字节 UTF-8 变 U+FFFD 的缺陷）。
type anthropicStream struct {
	lifecycle *streamLifecycle
	state     *protocol.AnthropicStreamState
}

// newAnthropicStream 构造 Anthropic 重建流。
func newAnthropicStream(lifecycle *streamLifecycle, _ func() time.Time) *anthropicStream {
	return &anthropicStream{
		lifecycle: lifecycle,
		state:     protocol.NewAnthropicStreamState(),
	}
}

// push 处理一块上游数据。
// begin 发出 message_start。
//
// 参照实现在读取任何上游字节**之前**就 yield 了 message_start
// （proxy_handler.py:1217），因此「首块超时」这类失败也已经在下游留下了一个
// message_start——这个顺序是可观测的，不能挪到读到首块之后再发。
// abort 不做任何事：转换流的收尾事件在参照实现里位于 try 内，异常会整段跳过。
func (s *anthropicStream) abort(_ http.ResponseWriter) {}

func (s *anthropicStream) begin(w http.ResponseWriter) error {
	if !writeSSE(w, "message_start", anthropicMessageStart(s.lifecycle.requestedModelID)) {
		return errDownstreamWrite
	}
	return nil
}

// push 处理一块上游数据。
func (s *anthropicStream) push(w http.ResponseWriter, chunk []byte) error {
	events, usage := s.state.Push(chunk)
	s.absorbUsage(usage)
	for _, event := range events {
		if !writeBytes(w, event) {
			return errDownstreamWrite
		}
	}
	return nil
}

// absorbUsage 合并本块解析出的 usage。
func (s *anthropicStream) absorbUsage(usage *canonical.Value) {
	if usage == nil {
		return
	}
	s.lifecycle.SetUsage(mergeUsage(s.lifecycle.Usage(), usage))
}

// flush 仅在上游完整结束且已转换出内容后收尾，空输出不能补成成功消息。
func (s *anthropicStream) flush(w http.ResponseWriter) error {
	if s.state.NextContentIndex == 0 && len(s.state.ToolCalls) == 0 {
		return errEmptyStream
	}
	events, usage := s.state.Finish()
	s.absorbUsage(usage)
	for _, event := range events {
		if !writeBytes(w, event) {
			return errDownstreamWrite
		}
	}

	state := s.state
	if state.TextIndex != nil && !state.TextStopped {
		if !writeSSE(w, "content_block_stop", anthropicContentBlockStop(*state.TextIndex)) {
			return errDownstreamWrite
		}
		state.TextStopped = true
	}
	for _, event := range state.FinishTools() {
		if !writeBytes(w, event) {
			return errDownstreamWrite
		}
	}
	if state.NextContentIndex == 0 {
		if !writeSSE(w, "content_block_start", anthropicEmptyTextBlock()) {
			return errDownstreamWrite
		}
		if !writeSSE(w, "content_block_stop", anthropicContentBlockStop(0)) {
			return errDownstreamWrite
		}
	}
	stopReason := state.StopReason
	if stopReason == "" {
		if len(state.ToolCalls) > 0 {
			stopReason = "tool_use"
		} else {
			stopReason = "end_turn"
		}
	}
	if !writeSSE(w, "message_delta", anthropicMessageDelta(stopReason, s.lifecycle.Usage())) ||
		!writeSSE(w, "message_stop", anthropicMessageStop()) {
		return errDownstreamWrite
	}
	return nil
}

// responsesStream 把上游 OpenAI SSE 重建为 Responses SSE。
//
// 移植 proxy_handler.py:1310 的 _stream_responses。
type responsesStream struct {
	lifecycle *streamLifecycle
	state     *protocol.ResponsesStreamState
}

// newResponsesStream 构造 Responses 重建流。
func newResponsesStream(lifecycle *streamLifecycle, _ func() time.Time) *responsesStream {
	return &responsesStream{
		lifecycle: lifecycle,
		state:     protocol.NewResponsesStreamState(),
	}
}

// push 处理一块上游数据。
// abort 不做任何事：见 anthropicStream.abort。
func (s *responsesStream) abort(_ http.ResponseWriter) {}

func (s *responsesStream) begin(_ http.ResponseWriter) error { return nil }

// push 处理一块上游数据。
func (s *responsesStream) push(w http.ResponseWriter, chunk []byte) error {
	events, usage, err := s.state.Push(chunk)
	if err != nil {
		return errStreamConversion
	}
	s.absorbUsage(usage)
	for _, event := range events {
		if !writeBytes(w, event) {
			return errDownstreamWrite
		}
	}
	return nil
}

// absorbUsage 合并本块解析出的 usage。
func (s *responsesStream) absorbUsage(usage *canonical.Value) {
	if usage == nil {
		return
	}
	s.lifecycle.SetUsage(mergeUsage(s.lifecycle.Usage(), usage))
}

// flush 处理残余行并发出收尾事件。
//
// 移植 proxy_handler.py:1359-1391：
//  1. 残余行（补一个换行再解析）；
//  2. 每个累积的 output item 一个 response.output_item.done；
//  3. response.completed（带 id / model / usage）。
func (s *responsesStream) flush(w http.ResponseWriter) error {
	if !hasStreamContent(s.state.OutputItems()) {
		return errEmptyStream
	}
	events, usage, err := s.state.Flush()
	if err != nil {
		return errStreamConversion
	}
	s.absorbUsage(usage)
	for _, event := range events {
		if !writeBytes(w, event) {
			return errDownstreamWrite
		}
	}
	outputItems := s.state.OutputItems()
	if outputItems.IsArray() {
		for index, item := range outputItems.Items() {
			if !writeSSE(w, "response.output_item.done",
				responsesOutputItemDone(int64(index), item)) {
				return errDownstreamWrite
			}
		}
	}
	completed, usageErr := responsesCompleted(s.lifecycle.requestedModelID, s.lifecycle.Usage())
	if usageErr != nil {
		return errStreamConversion
	}
	if !writeSSE(w, "response.completed", completed) {
		return errDownstreamWrite
	}
	return nil
}

// --- SSE 载荷构造 ---
//
// 每个构造函数都严格按参照实现里 dict 字面量的**键顺序**插入：canonical 的
// DumpsOrdered 按插入顺序输出，而事件字节是下游可解析的契约。

// anthropicMessageStart 构造 message_start 载荷。
//
// 移植 proxy_handler.py:1217。id 硬编码为 msg_amkr、content 为空数组、usage 全 0；
// model 用**请求的**模型 ID（而非解析后的真实模型），这是客户端看到的模型名。
func anthropicMessageStart(requestedModelID string) *canonical.Value {
	message := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString("msg_amkr")},
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("message")},
		canonical.ObjectPair{Key: "role", Value: canonical.NewString("assistant")},
		canonical.ObjectPair{Key: "model", Value: canonical.NewString(requestedModelID)},
		canonical.ObjectPair{Key: "content", Value: canonical.NewArray()},
		canonical.ObjectPair{Key: "stop_reason", Value: canonical.NewNull()},
		canonical.ObjectPair{Key: "stop_sequence", Value: canonical.NewNull()},
		canonical.ObjectPair{Key: "usage", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "input_tokens", Value: canonical.NewIntValue(0)},
			canonical.ObjectPair{Key: "output_tokens", Value: canonical.NewIntValue(0)},
		)},
	)
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("message_start")},
		canonical.ObjectPair{Key: "message", Value: message},
	)
}

// anthropicContentBlockStop 构造 content_block_stop 载荷。'
//
// 移植 proxy_handler.py:1259、1281。
func anthropicContentBlockStop(index int64) *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("content_block_stop")},
		canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(index)},
	)
}

// anthropicEmptyTextBlock 构造「一个块都没有」时补的空文本块 start 载荷。
//
// 移植 proxy_handler.py:1272。
func anthropicEmptyTextBlock() *canonical.Value {
	block := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("text")},
		canonical.ObjectPair{Key: "text", Value: canonical.NewString("")},
	)
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("content_block_start")},
		canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(0)},
		canonical.ObjectPair{Key: "content_block", Value: block},
	)
}

// anthropicMessageDelta 构造 message_delta 载荷。
//
// 移植 proxy_handler.py:1289。delta 在前、usage 在后。
func anthropicMessageDelta(stopReason string, usage *canonical.Value) *canonical.Value {
	delta := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "stop_reason", Value: canonical.NewString(stopReason)},
		canonical.ObjectPair{Key: "stop_sequence", Value: canonical.NewNull()},
	)
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("message_delta")},
		canonical.ObjectPair{Key: "delta", Value: delta},
		canonical.ObjectPair{Key: "usage", Value: protocol.AnthropicStreamUsage(usage)},
	)
}

// anthropicMessageStop 构造 message_stop 载荷。
func anthropicMessageStop() *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("message_stop")},
	)
}

// responsesOutputItemDone 构造 response.output_item.done 载荷。
//
// 移植 proxy_handler.py:1372。item 直接复用 OutputItems 产出的对象（不复制），
// 与参照实现一致。
func responsesOutputItemDone(index int64, item *canonical.Value) *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("response.output_item.done")},
		canonical.ObjectPair{Key: "output_index", Value: canonical.NewIntValue(index)},
		canonical.ObjectPair{Key: "item", Value: item},
	)
}

// responsesCompleted 构造 response.completed 载荷。
//
// 移植 proxy_handler.py:1381。id 与 model 是硬编码 / 请求模型名。
func responsesCompleted(requestedModelID string, usage *canonical.Value) (*canonical.Value, error) {
	normalized, err := protocol.ResponsesUsage(usage)
	if err != nil {
		return nil, err
	}
	response := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString("resp_amkr")},
		canonical.ObjectPair{Key: "model", Value: canonical.NewString(requestedModelID)},
		canonical.ObjectPair{Key: "usage", Value: normalized},
	)
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("response.completed")},
		canonical.ObjectPair{Key: "response", Value: response},
	), nil
}

// writeBytes 写一块字节并刷出。
//
// 写失败返回 false：这意味着下游已经断开，继续读上游只是浪费（参照实现里
// generator 被关闭会抛 GeneratorExit/CancelledError）。
func writeBytes(w http.ResponseWriter, chunk []byte) bool {
	if _, err := w.Write(chunk); err != nil {
		return false
	}
	flush(w)
	return true
}

// writeSSE 编码一个 SSE 事件并写出。
func writeSSE(w http.ResponseWriter, event string, payload *canonical.Value) bool {
	return writeBytes(w, protocol.EncodeSSE(event, payload))
}

// flush 在每一块之后显式刷出。
//
// 参照实现是 `await asyncio.sleep(0)`（proxy_handler.py:1130），让出事件循环以便
// ASGI 把已产出的字节发出去；Go 没有对应物，等价动作是 http.Flusher.Flush
// （迁移方案 §Phase 3 明确要求「每事件显式 Flush」）。
func flush(w http.ResponseWriter) {
	if flusher, ok := w.(interface{ Flush() }); ok {
		flusher.Flush()
	}
}

// mergeUsage 把新解析到的 usage 合并进已有值。
//
// 移植 proxy_handler.py:1030：`merged = dict(current); merged.update(update)`。
// 用 canonical 的 SetKey 复刻 dict 的「覆盖不移动位置、新键追加到末尾」规则——
// 键顺序会体现在最终 message_delta / response.completed 的 usage 里，属可观测行为。
func mergeUsage(current, update *canonical.Value) *canonical.Value {
	if update == nil {
		return current
	}
	if current == nil || !current.IsObject() {
		return update.Clone()
	}
	merged := current.Clone()
	if update.IsObject() {
		for _, key := range update.Obj.Keys() {
			value, _ := update.Obj.Get(key)
			merged.Obj.Set(key, value)
		}
	}
	return merged
}
