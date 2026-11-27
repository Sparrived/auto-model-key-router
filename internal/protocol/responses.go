package protocol

import (
	"slices"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件移植 auto_model_key_router/protocols/responses.py：把上游的 OpenAI
// chat-completions 响应（非流式与流式两种）重建成 OpenAI Responses API 的形状。
//
// 与 Python 的唯一结构性差异是**流式解析的状态载体**。参照实现把缓冲区当字符串
// 在参数与返回值之间传来传去（`buffer += chunk.decode("utf-8", errors="replace")`），
// 那正是跨分片多字节字符变乱码的根源（见 common.go 的 SSESplitter 注释）。这里把
// 缓冲区连同跨事件的累积状态收进 ResponsesStreamState，缓冲区交给字节级切分器
// 持有，调用方不再经手字符串。

// --- 非流式：响应重建 ---

// ResponsesResponse 把上游响应体重建成 Responses API 形态。
//
// 移植 responses.py:146。返回 nil 表示上游返回的不是 JSON 对象，调用方应回 502
// （proxy_handler.py:985 的 `if converted is None`）。
//
// 两条早退路径的语义要分清：上游**本来就是** Responses 形态（`object == "response"`
// 且 `output` 是数组）时原样返回同一个对象（不做拷贝，参照实现也是 `return data`）；
// 数据里没有 choices 时返回一个 `output: []` 的空壳，而不是 nil。
func ResponsesResponse(data *canonical.Value, requestedModelID string) (*canonical.Value, error) {
	if !data.IsObject() {
		return nil, nil
	}
	if object := data.Lookup("object"); object.IsString() && object.Str == "response" && data.Lookup("output").IsArray() {
		return data, nil
	}
	usage, err := ResponsesUsage(responsesUsageArgument(data))
	if err != nil {
		return nil, err
	}
	choices := OpenaiChoices(data)
	var output *canonical.Value
	if len(choices) == 0 {
		output = canonical.NewArray()
	} else {
		message := choices[0].Lookup("message")
		if !message.IsObject() {
			// 参照实现把非字典的 message 换成空字典，于是走到「无内容」分支。
			message = canonical.NewObject()
		}
		output = ResponsesMessageOutputItems(message)
	}
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString(responsesResponseID(data))},
		canonical.ObjectPair{Key: "object", Value: canonical.NewString("response")},
		canonical.ObjectPair{Key: "status", Value: canonical.NewString("completed")},
		canonical.ObjectPair{Key: "model", Value: canonical.NewString(requestedModelID)},
		canonical.ObjectPair{Key: "output", Value: output},
		canonical.ObjectPair{Key: "usage", Value: usage},
	), nil
}

// responsesResponseID 复刻 `str(data.get("id") or "resp_amkr")`。
//
// 用 StringValue 而不是 fmt：Python 的 `or` 会让 0 / false / [] / {} 一并退到默认值，
// 而 StringValue 的真值判断与 Python 的 bool() 完全一致。
func responsesResponseID(data *canonical.Value) string {
	if id := data.Lookup("id").StringValue(); id != "" {
		return id
	}
	return "resp_amkr"
}

// responsesUsageArgument 复刻 `data.get("usage") if isinstance(..., dict) else None`。
func responsesUsageArgument(data *canonical.Value) *canonical.Value {
	if usage := data.Lookup("usage"); usage.IsObject() {
		return usage
	}
	return nil
}

// ResponsesMessageOutputItems 把 OpenAI 的 message 对象转成 Responses 的 output 数组。
//
// 移植 responses.py:176。注意三处容易漏掉的细节：
//
//  1. `tool_calls` 不存在时回退到旧的 `function_call` 字段，并合成一个
//     `call_amkr_0` 的 id；
//  2. `enumerate(tool_calls)` 的下标包含**被跳过的非字典元素**，所以
//     `fc_amkr_{index}` 里的 index 与有效工具数量无关；
//  3. arguments 非字符串时先做 `or {}` 再 JSON 序列化，因此 `0` / `""` / `[]`
//     都会变成 `"{}"`。
func ResponsesMessageOutputItems(message *canonical.Value) *canonical.Value {
	items := []*canonical.Value{}
	if text := MessageText(message.Lookup("content")); text != "" {
		items = append(items, responsesMessageItem(text))
	}

	toolCalls := message.Lookup("tool_calls")
	if !toolCalls.IsArray() {
		functionCall := message.Lookup("function_call")
		if functionCall.IsObject() {
			toolCalls = canonical.NewArray(canonical.NewObjectOf(
				canonical.ObjectPair{Key: "id", Value: canonical.NewString("call_amkr_0")},
				canonical.ObjectPair{Key: "function", Value: functionCall},
			))
		} else {
			toolCalls = canonical.NewArray()
		}
	}

	for index, toolCall := range toolCalls.Items() {
		if !toolCall.IsObject() {
			continue
		}
		function := toolCall.Lookup("function")
		if !function.IsObject() {
			continue
		}
		callID := toolCall.Lookup("id").StringValue()
		if callID == "" {
			callID = "call_amkr_" + intString(int64(index))
		}
		arguments := function.Lookup("arguments")
		argumentsText := ""
		switch {
		case arguments.IsString():
			argumentsText = arguments.Str
		case arguments.Truthy():
			argumentsText = canonical.DumpsOrdered(arguments)
		default:
			argumentsText = "{}"
		}
		items = append(items, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("function_call")},
			canonical.ObjectPair{Key: "id", Value: canonical.NewString("fc_amkr_" + intString(int64(index)))},
			canonical.ObjectPair{Key: "call_id", Value: canonical.NewString(callID)},
			canonical.ObjectPair{Key: "name", Value: canonical.NewString(function.Lookup("name").StringValue())},
			canonical.ObjectPair{Key: "arguments", Value: canonical.NewString(argumentsText)},
		))
	}

	if len(items) == 0 {
		items = append(items, responsesMessageItem(""))
	}
	return canonical.NewArray(items...)
}

// responsesMessageItem 构造 output 里的 assistant message 项。
func responsesMessageItem(text string) *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("message")},
		canonical.ObjectPair{Key: "id", Value: canonical.NewString("msg_amkr")},
		canonical.ObjectPair{Key: "role", Value: canonical.NewString("assistant")},
		canonical.ObjectPair{Key: "content", Value: canonical.NewArray(canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("output_text")},
			canonical.ObjectPair{Key: "text", Value: canonical.NewString(text)},
		))},
	)
}

// --- 非流式：usage 归一 ---

// ResponsesUsage 把上游 usage 归一成 Responses API 的 usage 形状。
//
// 移植 responses.py:229。字段探测链一律用 Python 的 `or`：`0`、`""`、`None`
// 都会退到下一个来源，因此缓存字段可能在 `cached_tokens`、`cache_read_input_tokens`
// 与 `input_tokens_details` 之间依次尝试。
//
// 与参照实现的一处刻意差异：Python 在这里直接写 `int(...)`，遇到
// `{"prompt_tokens": "abc"}` 会抛 ValueError——非流式路径变成 500，流式路径
// **静默截断整条流**。这里把 `int()` 的失败如实上报成 error，让调用方决定是
// 记一条 500 还是带着 0 继续；不在此处自行吞掉，因为「悄悄把 token 记成 0」
// 正是迁移计划要求避免的那类静默偏差。
func ResponsesUsage(usage *canonical.Value) (*canonical.Value, error) {
	source := usage
	inputTokens, err := responsesUsageIntOr(
		responsesUsageField{source: source, key: "prompt_tokens"},
		responsesUsageField{source: source, key: "input_tokens"},
	)
	if err != nil {
		return nil, err
	}
	outputTokens, err := responsesUsageIntOr(
		responsesUsageField{source: source, key: "completion_tokens"},
		responsesUsageField{source: source, key: "output_tokens"},
	)
	if err != nil {
		return nil, err
	}

	// `if not isinstance(input_details, dict): input_details = {}`——用 nil 表示空表，
	// Lookup 对 nil 返回 nil，效果等价。
	inputDetails := source.Lookup("input_tokens_details")
	if !inputDetails.IsObject() {
		inputDetails = nil
	}
	cachedTokens, err := responsesUsageIntOr(
		responsesUsageField{source: source, key: "cached_tokens"},
		responsesUsageField{source: source, key: "cache_read_input_tokens"},
		responsesUsageField{source: inputDetails, key: "cached_tokens"},
		responsesUsageField{source: inputDetails, key: "cache_read_input_tokens"},
	)
	if err != nil {
		return nil, err
	}
	reasoningTokens, err := responsesUsageIntOr(
		responsesUsageField{source: source, key: "reasoning_tokens"},
	)
	if err != nil {
		return nil, err
	}

	// `int(source.get("total_tokens") or input_tokens + output_tokens)`：第二个操作数
	// 不是字段查询，所以不能并入上面的探测链——注意 `"total_tokens": "0"` 是真值，
	// int("0") == 0 时**不**回退到求和。
	totalTokens := inputTokens + outputTokens
	if value, ok := responsesUsageFirstTruthy(
		responsesUsageField{source: source, key: "total_tokens"},
	); ok {
		totalTokens, err = responsesUsageInt(value)
		if err != nil {
			return nil, err
		}
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "input_tokens", Value: canonical.NewIntValue(inputTokens)},
		canonical.ObjectPair{Key: "input_tokens_details", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "cached_tokens", Value: canonical.NewIntValue(cachedTokens)},
		)},
		canonical.ObjectPair{Key: "output_tokens", Value: canonical.NewIntValue(outputTokens)},
		canonical.ObjectPair{Key: "output_tokens_details", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "reasoning_tokens", Value: canonical.NewIntValue(reasoningTokens)},
		)},
		canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewIntValue(totalTokens)},
	), nil
}

// responsesUsageField 是 usage 探测链上的一个候选来源。
type responsesUsageField struct {
	source *canonical.Value
	key    string
}

// responsesUsageFirstTruthy 返回探测链上第一个真值字段。
func responsesUsageFirstTruthy(fields ...responsesUsageField) (*canonical.Value, bool) {
	for _, field := range fields {
		if value := field.source.Lookup(field.key); value.Truthy() {
			return value, true
		}
	}
	return nil, false
}

// responsesUsageIntOr 复刻 `a or b or ... or 0` 外面套一层 int()。
//
// 关键点：链在**第一个真值**处停止，不会因为该值无法转成整数而继续往后找。
func responsesUsageIntOr(fields ...responsesUsageField) (int64, error) {
	value, ok := responsesUsageFirstTruthy(fields...)
	if !ok {
		return 0, nil
	}
	return responsesUsageInt(value)
}

// responsesUsageInt 复刻 Python 的 int(value)。
//
// 用 canonical.ToInt 而不是只认 KindNumber：Python 的 int("5") 是 5、int(True) 是 1，
// 都成功；而 int("5.0") 抛 ValueError。ToInt 的语义与此逐条对齐。
func responsesUsageInt(value *canonical.Value) (int64, error) {
	if value == nil {
		return 0, nil
	}
	return canonical.ToInt(value)
}

// --- 流式 ---

// ResponsesToolCall 是流式拼接过程中的一个工具调用。
//
// 字段名对应参照实现里 stream_state["tool_calls"][index] 的三个键。
type ResponsesToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ResponsesStreamState 是 Responses 流式转换的跨分片状态。
//
// 对应 proxy_handler.py:1343 里手工维护的
// `{"text": [], "text_started": False, "tool_calls": {}}`，外加参照实现用
// `buffer` 局部变量传递的行缓冲——后者改由字节级 SSESplitter 持有。
type ResponsesStreamState struct {
	splitter    *SSESplitter
	text        strings.Builder
	textStarted bool
	toolCalls   map[int]*ResponsesToolCall
}

// NewResponsesStreamState 构造流式状态。
func NewResponsesStreamState() *ResponsesStreamState {
	return &ResponsesStreamState{
		splitter:  NewSSESplitter(),
		toolCalls: map[int]*ResponsesToolCall{},
	}
}

// Push 追加一块上游字节并返回其中已完成的事件。
//
// 移植 responses.py:10 的 `_responses_stream_events`，但**不再接收也不返回字符串
// 缓冲**：行切分与 UTF-8 解码都在 SSESplitter 内部按字节完成，因此跨 TCP 分片的
// 多字节字符能正确拼回（参照实现在这里会产生 U+FFFD）。
//
// 返回值中的 usage 是最后一个出现 usage 的块的**原始引用**（对应 Python 的
// `usage = chunk_usage` 覆盖语义），未出现过时为 nil；归一化由调用方走 ResponsesUsage。
//
// 唯一会返回 error 的情形是工具调用的 `index` 无法转成整数。参照实现在这里直接
// `int(...)`，异常会穿透流式生成器被外层的 `except Exception` 捕获，结果是记一条
// 警告并**静默终止整条响应流**（不发错误帧）。这里如实上报，让调用方能记录与
// 参照实现同等的日志，而不是悄悄换一个下标继续拼。
func (s *ResponsesStreamState) Push(chunk []byte) ([][]byte, *canonical.Value, error) {
	var events [][]byte
	var usage *canonical.Value
	for _, line := range s.splitter.Push(chunk) {
		lineEvents, chunkUsage, err := s.PushPayload(line.Payload)
		if err != nil {
			return nil, nil, err
		}
		if chunkUsage != nil {
			usage = chunkUsage
		}
		events = append(events, lineEvents...)
	}
	return events, usage, nil
}

// PushPayload 直接消费已解析好的单个上游 SSE 载荷，避免外层已做完 JSON 解析后再次
// 序列化并二次解析。
func (s *ResponsesStreamState) PushPayload(payload *canonical.Value) ([][]byte, *canonical.Value, error) {
	if payload == nil {
		return nil, nil, nil
	}
	var events [][]byte
	usage := ExtractUsage(payload)
	for _, choice := range OpenaiChoices(payload) {
		delta := choice.Lookup("delta")
		if !delta.IsObject() {
			delta = nil
		}
		if text := DeltaText(delta); text != "" {
			if !s.textStarted {
				events = append(events,
					EncodeSSE("response.output_item.added", responsesOutputItemAddedPayload()),
					EncodeSSE("response.content_part.added", responsesContentPartAddedPayload()),
				)
				s.textStarted = true
			}
			s.text.WriteString(text)
			events = append(events, EncodeSSE("response.output_text.delta", canonical.NewObjectOf(
				canonical.ObjectPair{Key: "type", Value: canonical.NewString("response.output_text.delta")},
				canonical.ObjectPair{Key: "item_id", Value: canonical.NewString("msg_amkr")},
				canonical.ObjectPair{Key: "output_index", Value: canonical.NewIntValue(0)},
				canonical.ObjectPair{Key: "content_index", Value: canonical.NewIntValue(0)},
				canonical.ObjectPair{Key: "delta", Value: canonical.NewString(text)},
			)))
		}
		toolCalls := delta.Lookup("tool_calls")
		if !toolCalls.IsArray() {
			continue
		}
		for fallbackIndex, toolCall := range toolCalls.Items() {
			if !toolCall.IsObject() {
				continue
			}
			index := fallbackIndex
			if raw, ok := toolCall.LookupOK("index"); ok {
				parsed, err := canonical.ToInt(raw)
				if err != nil {
					return nil, nil, err
				}
				index = int(parsed)
			}
			stored := s.toolCalls[index]
			if stored == nil {
				stored = &ResponsesToolCall{}
				s.toolCalls[index] = stored
			}
			if id := toolCall.Lookup("id"); id.Truthy() {
				stored.ID = id.PyStr()
			}
			function := toolCall.Lookup("function")
			if !function.IsObject() {
				continue
			}
			// 名字是**累加**（与 anthropic.py 的「只取首个非空」不同），
			// 上游把名字拆成多块时这里必须续接。
			if name := function.Lookup("name"); name.Truthy() {
				stored.Name += name.PyStr()
			}
			if arguments := function.Lookup("arguments"); !arguments.IsNull() {
				if arguments.IsString() {
					stored.Arguments += arguments.Str
				} else {
					stored.Arguments += canonical.DumpsOrdered(arguments)
				}
			}
		}
	}
	return events, usage, nil
}

// Flush 处理流结束时残留在缓冲区里的最后半行。
//
// 对应 proxy_handler.py:1359 的
// `if buffer.strip(): buffer, events, _ = _responses_stream_events(buffer, b"\n", ...)`：
// 补一个换行让最后一行成为完整行。空白残留与参照实现殊途同归——它会被切分成
// 一行空白，既不以 `data:` 开头也不是合法 JSON，因此不产生任何事件。
func (s *ResponsesStreamState) Flush() ([][]byte, *canonical.Value, error) {
	if s.splitter.Pending() == 0 {
		return nil, nil, nil
	}
	return s.Push([]byte("\n"))
}

// Pending 返回尚未构成完整行的缓冲字节数（供调用方与测试观测）。
func (s *ResponsesStreamState) Pending() int { return s.splitter.Pending() }

// OutputItems 汇总流式累积的文本与工具调用，用于收尾的 output_item.done 事件。
//
// 移植 responses.py:111。`if text or not tool_calls` 的含义是：只要出了文本，
// 或者**一个工具调用都没有**，就补一条 message 项——纯工具调用的响应不会多出
// 一条空 message。
func (s *ResponsesStreamState) OutputItems() *canonical.Value {
	items := []*canonical.Value{}
	text := s.text.String()
	if text != "" || len(s.toolCalls) == 0 {
		items = append(items, responsesMessageItem(text))
	}
	for _, index := range responsesSortedIndexes(s.toolCalls) {
		toolCall := s.toolCalls[index]
		callID := toolCall.ID
		if callID == "" {
			callID = "call_amkr_" + intString(int64(index))
		}
		arguments := toolCall.Arguments
		if arguments == "" {
			arguments = "{}"
		}
		items = append(items, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("function_call")},
			canonical.ObjectPair{Key: "id", Value: canonical.NewString("fc_amkr_" + intString(int64(index)))},
			canonical.ObjectPair{Key: "call_id", Value: canonical.NewString(callID)},
			canonical.ObjectPair{Key: "name", Value: canonical.NewString(toolCall.Name)},
			canonical.ObjectPair{Key: "arguments", Value: canonical.NewString(arguments)},
		))
	}
	return canonical.NewArray(items...)
}

// responsesSortedIndexes 返回按升序排列的工具调用下标（对应 Python 的 sorted(dict)）。
func responsesSortedIndexes(toolCalls map[int]*ResponsesToolCall) []int {
	indexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	return indexes
}

// responsesOutputItemAddedPayload 构造首个文本分片之前的 output_item.added 事件体。
func responsesOutputItemAddedPayload() *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("response.output_item.added")},
		canonical.ObjectPair{Key: "output_index", Value: canonical.NewIntValue(0)},
		canonical.ObjectPair{Key: "item", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("message")},
			canonical.ObjectPair{Key: "id", Value: canonical.NewString("msg_amkr")},
			canonical.ObjectPair{Key: "role", Value: canonical.NewString("assistant")},
			canonical.ObjectPair{Key: "status", Value: canonical.NewString("in_progress")},
			canonical.ObjectPair{Key: "content", Value: canonical.NewArray()},
		)},
	)
}

// responsesContentPartAddedPayload 构造首个文本分片之前的 content_part.added 事件体。
func responsesContentPartAddedPayload() *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("response.content_part.added")},
		canonical.ObjectPair{Key: "item_id", Value: canonical.NewString("msg_amkr")},
		canonical.ObjectPair{Key: "output_index", Value: canonical.NewIntValue(0)},
		canonical.ObjectPair{Key: "content_index", Value: canonical.NewIntValue(0)},
		canonical.ObjectPair{Key: "part", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("output_text")},
			canonical.ObjectPair{Key: "text", Value: canonical.NewString("")},
			canonical.ObjectPair{Key: "annotations", Value: canonical.NewArray()},
		)},
	)
}
