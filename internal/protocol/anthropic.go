package protocol

import (
	"slices"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件移植 auto_model_key_router/protocols/anthropic.py：把上游 OpenAI
// chat.completions 形态的响应体（或流）转换成 Anthropic Messages 形态。
//
// 与参照实现有两处**有意差异**，都不是笔误：
//
//  1. SSE 行切分改用 SSESplitter（在字节层面找换行，拿到完整一行才 decode）。
//     参照实现写 `buffer += chunk.decode("utf-8", errors="replace")` 再按 "\n"
//     切，TCP 分片切开多字节字符时两个分片各自 decode 都会得到 U+FFFD，中文与
//     emoji 会静默变成乱码。详见 common.go 中 SSESplitter 的注释。
//  2. usage 数值取自 common.go 的 usageInt / usageIntOr，它们只认 JSON 数字；
//     参照实现的 `_usage_int` 还有 `str.isdigit()` 分支。差异与升级路径写在
//     AnthropicUsage 的注释里。
//
// 其余语义（含 None 与 "" 的区别、dict 键的插入顺序、工具参数的**码点**切片）逐条
// 对齐参照实现，测试期望值全部由真实 Python 实现打印得到。

// AnthropicToolCall 是一次流式工具调用的累积状态。
//
// 对应参照实现 stream_state["tool_calls"][index] 里那个 dict。字段名刻意保持
// 小写下划线式的直译，便于与 anthropic.py 对照。
type AnthropicToolCall struct {
	// ID 是上游给的调用 id；为空表示尚未收到 id（结束时回落成 call_amkr_{index}）。
	ID string
	// Name 是函数名；为空时不会开始这个工具块。
	Name string
	// Arguments 是逐块拼起来的参数文本（上游既可能分片给字符串，也可能给 JSON
	// 值，后者在这里被序列化成紧凑 JSON）。用**码点**计数，见 toolArgumentEvents。
	Arguments string
	// EmittedArguments 是已通过 input_json_delta 下发的码点数。
	EmittedArguments int
	// ContentIndex 是本工具块在 Anthropic content 数组里的下标。
	ContentIndex int64
	// Started 表示 content_block_start 已发出。
	Started bool
	// Stopped 表示 content_block_stop 已发出。
	Stopped bool
}

// AnthropicStreamState 是流式转换的跨块状态。
//
// 对应参照实现里由 proxy_handler 构造并传入的那个 stream_state dict；外层还需要
// 读 NextContentIndex / StopReason / ToolCalls 来决定收尾事件（message_delta、
// 以及"一个块都没有"时补一对空文本块），所以这些字段是导出的。
//
// 与参照实现的一处结构差异：Python 每次调用都要显式传入并返回 buffer，这里把缓冲
// 藏进 SSESplitter，由 Push/Finish 自己维护——因为字节级切分必须保留**原始字节**，
// 而 Python 的 buffer 是已经 decode 过的字符串，不能照搬。
type AnthropicStreamState struct {
	// NextContentIndex 是下一个 content 块的 index。
	NextContentIndex int64
	// TextIndex 是当前文本块的下标；nil 表示还没有文本块（Python 的 None）。
	TextIndex *int64
	// TextStopped 表示当前文本块已经发出 content_block_stop。
	// 为 true 时再来文本会**新开**一个文本块。
	TextStopped bool
	// ToolCalls 按上游给的 index 累积工具调用（Python 的 dict[int, dict]）。
	ToolCalls map[int64]*AnthropicToolCall
	// ActiveToolIndex 是当前正在接收参数增量的工具下标；nil 表示没有。
	ActiveToolIndex *int64
	// StopReason 来自 finish_reason，会作为 message_delta 的 stop_reason。
	// 空串表示 Python 的 None——下游只用真值判断，两者等价。
	StopReason string

	splitter *SSESplitter
}

// NewAnthropicStreamState 构造初始状态。
//
// 字段初值与 proxy_handler.py:1208 的 stream_state 字面量一致。
func NewAnthropicStreamState() *AnthropicStreamState {
	return &AnthropicStreamState{
		ToolCalls: map[int64]*AnthropicToolCall{},
		splitter:  NewSSESplitter(),
	}
}

// Push 追加一块上游数据，返回本次产出的事件与**本块最后一次**看到的 usage。
//
// 移植 anthropic.py:10。usage 为 nil 表示本块没有可提取的 usage（调用方不应覆盖
// 已有值）；同一块里出现多次时后者覆盖前者，与参照实现一致。
func (s *AnthropicStreamState) Push(chunk []byte) ([][]byte, *canonical.Value) {
	if s.splitter == nil {
		s.splitter = NewSSESplitter()
	}
	var events [][]byte
	var usage *canonical.Value
	for _, line := range s.splitter.Push(chunk) {
		if extracted := ExtractUsage(line.Payload); extracted != nil {
			usage = extracted
		}
		events = append(events, s.consume(line.Payload)...)
	}
	return events, usage
}

// PushPayload 直接消费已解析好的单个上游 SSE 载荷，避免外层已做完 JSON 解析后再次
// 序列化并二次解析。
func (s *AnthropicStreamState) PushPayload(payload *canonical.Value) ([][]byte, *canonical.Value) {
	if payload == nil {
		return nil, nil
	}
	return s.consume(payload), ExtractUsage(payload)
}

// Finish 处理流结束时残留在缓冲里、没有换行结尾的那半行。
//
// 参照实现把这一步放在 proxy_handler.py:1247（`if buffer.strip():` 后再喂一个
// "\n"），不属于本模块，但少了它最后一行事件就会丢，所以一并提供。空白残余与
// 参照实现一样直接丢弃。
func (s *AnthropicStreamState) Finish() ([][]byte, *canonical.Value) {
	if s.splitter == nil {
		return nil, nil
	}
	pending := s.splitter.Flush()
	if strings.TrimSpace(pending) == "" {
		return nil, nil
	}
	// 把残余原样放回并补一个换行——等价于参照实现的 chunk=b"\n"。
	return s.Push([]byte(pending + "\n"))
}

// PendingBytes 返回尚未凑成完整一行的缓冲字节数（供测试与诊断观测）。
func (s *AnthropicStreamState) PendingBytes() int {
	if s.splitter == nil {
		return 0
	}
	return s.splitter.Pending()
}

// consume 把一个上游载荷转换成本次新增的事件。
//
// 移植 anthropic.py:32 的 choices 循环体。
func (s *AnthropicStreamState) consume(payload *canonical.Value) [][]byte {
	var events [][]byte
	for _, choice := range OpenaiChoices(payload) {
		// 非字典的 delta 在参照实现里被替换成 {}，而 nil 的 Lookup/IsArray 都返回
		// 假值，所以 Go 无需真的造一个空对象。
		delta := choice.Lookup("delta")

		if text := DeltaText(delta); text != "" {
			// 首次出现文本、或上一个文本块已被工具调用打断时，新开一个文本块。
			if s.TextIndex == nil || s.TextStopped {
				index := s.NextContentIndex
				s.NextContentIndex = index + 1
				s.TextIndex = &index
				s.TextStopped = false
				events = append(events, EncodeSSE(
					"content_block_start", anthropicTextBlockStart(index)))
			}
			events = append(events, EncodeSSE(
				"content_block_delta", anthropicTextDelta(*s.TextIndex, text)))
		}

		if toolCalls := delta.Lookup("tool_calls"); toolCalls.IsArray() {
			// 非字典元素被跳过，但 enumerate 的下标照样前进（参照实现如此）。
			for index, toolCall := range toolCalls.Items() {
				if toolCall.IsObject() {
					events = append(events, s.toolCallEvents(toolCall, int64(index))...)
				}
			}
		}
		if functionCall := delta.Lookup("function_call"); functionCall.IsObject() {
			// 旧式 function_call 等价于「只有一个、index 为 0 的 tool_call」。
			wrapper := canonical.NewObjectOf(
				canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(0)},
				canonical.ObjectPair{Key: "function", Value: functionCall},
			)
			events = append(events, s.toolCallEvents(wrapper, 0)...)
		}

		// Python 判的是 `is not None`：显式的 null 不覆盖已有 stop_reason。
		if reason := choice.Lookup("finish_reason"); !reason.IsNull() {
			s.StopReason = AnthropicStopReason(reason, false)
		}
	}
	return events
}

// toolCallEvents 处理 delta 里的一个工具调用，返回本次新增的事件。
//
// 移植 anthropic.py:85。
func (s *AnthropicStreamState) toolCallEvents(
	toolCall *canonical.Value, fallbackIndex int64,
) [][]byte {
	var events [][]byte

	// 工具调用一旦出现，正在输出的文本块必须立刻收尾——否则下游会把 tool_use
	// 块挂在文本块内部。参照实现只在文本块"还开着"时发一次 stop。
	if s.TextIndex != nil && !s.TextStopped {
		events = append(events, EncodeSSE(
			"content_block_stop", anthropicContentBlockStop(*s.TextIndex)))
		s.TextStopped = true
	}

	// `int(tool_call.get("index", fallback_index))` 在 TypeError/ValueError 时回落到
	// 枚举下标；canonical.ToInt 覆盖同样的失败面（bool/str/float/None）。
	toolIndex := fallbackIndex
	if raw, ok := toolCall.LookupOK("index"); ok {
		if parsed, err := canonical.ToInt(raw); err == nil {
			toolIndex = parsed
		}
	}

	accumulated := s.toolCall(toolIndex)
	if id := toolCall.Lookup("id"); id.Truthy() && accumulated.ID == "" {
		accumulated.ID = id.PyStr()
	}
	if function := toolCall.Lookup("function"); function.IsObject() {
		if name := function.Lookup("name"); name.Truthy() && accumulated.Name == "" {
			accumulated.Name = name.PyStr()
		}
		// dict.get("arguments") 缺键得 None → 什么都不拼；显式 null 同理。
		if arguments, ok := function.LookupOK("arguments"); ok {
			switch {
			case arguments.IsString():
				accumulated.Arguments += arguments.Str
			case !arguments.IsNull():
				accumulated.Arguments += canonical.DumpsOrdered(arguments)
			}
		}
	}

	// 必须同时拿到 id 与 name 才开块，这样 argument 增量不会先于 content_block_start。
	if s.ActiveToolIndex == nil && accumulated.ID != "" && accumulated.Name != "" {
		index := toolIndex
		s.ActiveToolIndex = &index
		events = append(events, s.startToolBlock(toolIndex)...)
	}
	// 只有当前活跃的那个工具才接收参数增量：上游可以把多个工具的参数交错着发。
	if s.ActiveToolIndex != nil && *s.ActiveToolIndex == toolIndex {
		events = append(events, toolArgumentEvents(accumulated)...)
	}
	return events
}

// startToolBlock 发出工具块的 content_block_start（若尚未发出）。
//
// 移植 anthropic.py:143。
func (s *AnthropicStreamState) startToolBlock(toolIndex int64) [][]byte {
	toolCall := s.toolCall(toolIndex)
	if toolCall.Started {
		return nil
	}
	contentIndex := s.NextContentIndex
	s.NextContentIndex = contentIndex + 1
	toolCall.ContentIndex = contentIndex
	toolCall.Started = true

	id := toolCall.ID
	if id == "" {
		// 上游没给 id（function_call 形态常见）时补一个可辨识的占位 id。
		id = "call_amkr_" + intString(toolIndex)
	}
	payload := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("content_block_start")},
		canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(contentIndex)},
		canonical.ObjectPair{Key: "content_block", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("tool_use")},
			canonical.ObjectPair{Key: "id", Value: canonical.NewString(id)},
			canonical.ObjectPair{Key: "name", Value: canonical.NewString(toolCall.Name)},
			canonical.ObjectPair{Key: "input", Value: canonical.NewObject()},
		)},
	)
	return [][]byte{EncodeSSE("content_block_start", payload)}
}

// toolArgumentEvents 把新累积到的参数文本作为 input_json_delta 下发。
//
// 移植 anthropic.py:170。**切片单位是码点而不是字节**：Python 的
// `len(str)`/`str[i:]` 数的是码点，若按字节切，"北京" 这种中文参数会被切成半个
// 字符。emitted_arguments 记的也是码点数。
func toolArgumentEvents(toolCall *AnthropicToolCall) [][]byte {
	arguments := []rune(toolCall.Arguments)
	emitted := toolCall.EmittedArguments
	if !toolCall.Started || emitted >= len(arguments) {
		return nil
	}
	toolCall.EmittedArguments = len(arguments)
	payload := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("content_block_delta")},
		canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(toolCall.ContentIndex)},
		canonical.ObjectPair{Key: "delta", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("input_json_delta")},
			canonical.ObjectPair{Key: "partial_json", Value: canonical.NewString(string(arguments[emitted:]))},
		)},
	)
	return [][]byte{EncodeSSE("content_block_delta", payload)}
}

// FinishTools 在流结束时补齐所有工具块（含从未开始的），返回收尾事件。
//
// 移植 anthropic.py:191。顺序很关键：当前活跃的工具**最先**收尾，其余按 index
// 升序——上游交错发参数时，只有这样才能与已经下发的事件顺序自洽。
//
// 副作用：清空 ActiveToolIndex（再次调用不会重复产出事件，靠 Stopped 保证幂等）。
func (s *AnthropicStreamState) FinishTools() [][]byte {
	var events [][]byte

	ordered := make([]int64, 0, len(s.ToolCalls))
	if s.ActiveToolIndex != nil {
		ordered = append(ordered, *s.ActiveToolIndex)
	}
	// Go 的 map 无序，必须显式排序；Python 的 sorted(dict) 排的是键。
	rest := make([]int64, 0, len(s.ToolCalls))
	for index := range s.ToolCalls {
		if s.ActiveToolIndex != nil && index == *s.ActiveToolIndex {
			continue
		}
		rest = append(rest, index)
	}
	slices.Sort(rest)
	ordered = append(ordered, rest...)

	for _, toolIndex := range ordered {
		toolCall := s.ToolCalls[toolIndex]
		if toolCall.Stopped {
			continue
		}
		if !toolCall.Started {
			// 从未开块的工具（例如 function_call 形态一直没等到 id）在这里补 id 并开块。
			if toolCall.ID == "" {
				toolCall.ID = "call_amkr_" + intString(toolIndex)
			}
			events = append(events, s.startToolBlock(toolIndex)...)
		}
		events = append(events, toolArgumentEvents(toolCall)...)
		events = append(events, EncodeSSE(
			"content_block_stop", anthropicContentBlockStop(toolCall.ContentIndex)))
		toolCall.Stopped = true
	}
	s.ActiveToolIndex = nil
	return events
}

// toolCall 取或建某个下标的累积状态，等价于 Python 的 dict.setdefault。
func (s *AnthropicStreamState) toolCall(toolIndex int64) *AnthropicToolCall {
	if s.ToolCalls == nil {
		s.ToolCalls = map[int64]*AnthropicToolCall{}
	}
	if existing, ok := s.ToolCalls[toolIndex]; ok {
		return existing
	}
	created := &AnthropicToolCall{}
	s.ToolCalls[toolIndex] = created
	return created
}

// AnthropicUsage 把 usage 归一成 Anthropic 的计数字段。
//
// 移植 anthropic.py:236。键的顺序即输出顺序：input_tokens、output_tokens，然后
// 按需追加两个缓存字段。
//
// 与参照实现 `_usage_int`（anthropic.py:258）的差异：这里用 common.go 的
// usageInt，它只接受 JSON 数字；参照实现还会 `str.isdigit()` 后 int()。因此上游
// 把 token 数写成字符串（`"prompt_tokens": "123"`）时本实现给 0。同理 usageIntOr
// 在"字段为真值但转换结果为 0"时会继续看下一个键，而 Python 的 `a or b` 会停在 a。
//
// ponytail: 这两点都是 common.go 既有 helper 的行为，本模块不重复实现转换逻辑。
// 升级路径：若实测有上游返回字符串计数，把 common.go 的 usageInt 补上字符串分支
// （Python 的 str.isdigit 语义）即可，调用方无需改动。
func AnthropicUsage(usage *canonical.Value, includeInput bool) *canonical.Value {
	source := usage
	if !source.IsObject() {
		// Python 的 `usage or {}`：None / 非字典都退化成空字典。
		source = canonical.NewObject()
	}
	result := canonical.NewObject()
	if includeInput {
		result.SetKey("input_tokens", canonical.NewIntValue(
			usageIntOr(source, "prompt_tokens", "input_tokens")))
	}
	result.SetKey("output_tokens", canonical.NewIntValue(
		usageIntOr(source, "completion_tokens", "output_tokens")))
	for _, key := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
		if value := usageInt(source.Lookup(key)); value != 0 {
			result.SetKey(key, canonical.NewIntValue(value))
		}
	}
	// 通用字段 cached_tokens 只有在没有显式 cache_read_input_tokens 时才顶上
	// （后者是 Anthropic 原生字段，优先级更高）。
	if cached := usageInt(source.Lookup("cached_tokens")); cached != 0 {
		if _, exists := result.LookupOK("cache_read_input_tokens"); !exists {
			result.SetKey("cache_read_input_tokens", canonical.NewIntValue(cached))
		}
	}
	return result
}

// AnthropicStreamUsage 是流式收尾 message_delta 里用的 usage。
//
// 移植 anthropic.py:232：与 AnthropicUsage 相同，但一定带上 input_tokens。
func AnthropicStreamUsage(usage *canonical.Value) *canonical.Value {
	return AnthropicUsage(usage, true)
}

// AnthropicMessageResponse 把 OpenAI 形态的响应体转换成 Anthropic 的 message。
//
// 移植 anthropic.py:268。返回 nil 表示无法转换（非字典、或没有任何 choice）。
//
// 两个细节照抄参照实现：
//   - 已经是 Anthropic message 的响应体**原样返回同一个对象**（不复制、不规范化），
//     调用方不应就地改写它；
//   - content 为空时补一个空文本块——Anthropic 的客户端不接受 content: []。
func AnthropicMessageResponse(data *canonical.Value, requestedModelID string) *canonical.Value {
	if !data.IsObject() {
		return nil
	}
	if msgType := data.Lookup("type"); msgType.IsString() && msgType.Str == "message" &&
		data.Lookup("content").IsArray() {
		return data
	}
	choices := OpenaiChoices(data)
	if len(choices) == 0 {
		return nil
	}
	choice := choices[0]

	// 非字典的 message 在参照实现里被替换成 {}；nil 的 Lookup 全是假值，够用。
	message := choice.Lookup("message")
	content := []*canonical.Value{}
	if text := MessageText(message.Lookup("content")); text != "" {
		content = append(content, anthropicTextBlock(text))
	}
	content = append(content, AnthropicToolUseBlocks(message)...)
	if len(content) == 0 {
		content = append(content, anthropicTextBlock(""))
	}
	hasToolUse := false
	for _, block := range content {
		if blockType := block.Lookup("type"); blockType.IsString() &&
			blockType.Str == "tool_use" {
			hasToolUse = true
		}
	}

	// 非字典的 usage 在参照实现里被替换成 {}。
	usage := data.Lookup("usage")
	if !usage.IsObject() {
		usage = nil
	}
	id := "msg_amkr"
	if raw := data.Lookup("id"); raw.Truthy() {
		id = raw.PyStr()
	}
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString(id)},
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("message")},
		canonical.ObjectPair{Key: "role", Value: canonical.NewString("assistant")},
		canonical.ObjectPair{Key: "model", Value: canonical.NewString(requestedModelID)},
		canonical.ObjectPair{Key: "content", Value: canonical.NewArray(content...)},
		canonical.ObjectPair{Key: "stop_reason", Value: canonical.NewString(
			AnthropicStopReason(choice.Lookup("finish_reason"), hasToolUse))},
		// stop_sequence 是 null（不是 ""）：Anthropic 客户端会区分二者。
		canonical.ObjectPair{Key: "stop_sequence", Value: canonical.NewNull()},
		canonical.ObjectPair{Key: "usage", Value: AnthropicUsage(usage, true)},
	)
}

// AnthropicToolUseBlocks 把 message 里的工具调用转成 tool_use 内容块。
//
// 移植 anthropic.py:302。优先级：`tool_calls` 数组优先，缺失或不是数组时才回落到
// 旧式的单个 `function_call`（并补 id call_amkr_0）。
//
// arguments 的处理是这里最容易出错的地方，三条分支都要有：
//   - 字符串 → 当 JSON 解析；解析失败或字面量是 `null` 都退化成 {}（Python 里
//     _json_text 对这两种情况都返回 None）；
//   - 缺键 → {}（dict.get 的默认值）；
//   - 显式 null → {}（末尾的 `arguments if arguments is not None else {}`）。
func AnthropicToolUseBlocks(message *canonical.Value) []*canonical.Value {
	toolCalls := message.Lookup("tool_calls")
	if !toolCalls.IsArray() {
		functionCall := message.Lookup("function_call")
		if functionCall.IsObject() {
			toolCalls = canonical.NewArray(canonical.NewObjectOf(
				canonical.ObjectPair{Key: "id", Value: canonical.NewString("call_amkr_0")},
				canonical.ObjectPair{Key: "function", Value: functionCall},
			))
		} else {
			toolCalls = nil
		}
	}

	var blocks []*canonical.Value
	for index, toolCall := range toolCalls.Items() {
		if !toolCall.IsObject() {
			continue
		}
		function := toolCall.Lookup("function")
		if !function.IsObject() {
			continue
		}
		arguments, _ := function.LookupOK("arguments")
		if arguments == nil {
			arguments = canonical.NewObject()
		}
		if arguments.IsString() {
			if parsed := jsonText(arguments.Str); parsed != nil && !parsed.IsNull() {
				arguments = parsed
			} else {
				arguments = canonical.NewObject()
			}
		}
		id := "call_amkr_" + intString(int64(index))
		if raw := toolCall.Lookup("id"); raw.Truthy() {
			id = raw.PyStr()
		}
		name := ""
		if raw := function.Lookup("name"); raw.Truthy() {
			name = raw.PyStr()
		}
		if arguments.IsNull() {
			arguments = canonical.NewObject()
		}
		blocks = append(blocks, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("tool_use")},
			canonical.ObjectPair{Key: "id", Value: canonical.NewString(id)},
			canonical.ObjectPair{Key: "name", Value: canonical.NewString(name)},
			canonical.ObjectPair{Key: "input", Value: arguments},
		))
	}
	return blocks
}

// AnthropicStopReason 把 OpenAI 的 finish_reason 映射成 Anthropic 的 stop_reason。
//
// 移植 anthropic.py:333。顺序不可交换：length 优先于工具调用。
//
// 与参照实现的一处差异：Python 的 `reason in {"tool_calls", "function_call"}`
// 对不可哈希的 reason（数组/对象）会抛 TypeError 而中断整条流；这里返回 end_turn。
// 合法上游不会发这种值，属于「稳健性提高、不改变正常路径」。
func AnthropicStopReason(reason *canonical.Value, hasToolUse bool) string {
	if reason.IsString() && reason.Str == "length" {
		return "max_tokens"
	}
	if hasToolUse {
		return "tool_use"
	}
	if reason.IsString() &&
		(reason.Str == "tool_calls" || reason.Str == "function_call") {
		return "tool_use"
	}
	return "end_turn"
}

// --- 事件载荷构造 ---

// anthropicTextBlock 构造 {"type":"text","text":text}。
func anthropicTextBlock(text string) *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("text")},
		canonical.ObjectPair{Key: "text", Value: canonical.NewString(text)},
	)
}

// anthropicTextBlockStart 构造文本块的 content_block_start 载荷。
//
// 注意 content_block 里的 text 是**空串**（不是 null）：块的内容全部由后续的
// content_block_delta 提供。
func anthropicTextBlockStart(index int64) *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("content_block_start")},
		canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(index)},
		canonical.ObjectPair{Key: "content_block", Value: anthropicTextBlock("")},
	)
}

// anthropicTextDelta 构造 text_delta 的 content_block_delta 载荷。
func anthropicTextDelta(index int64, text string) *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("content_block_delta")},
		canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(index)},
		canonical.ObjectPair{Key: "delta", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("text_delta")},
			canonical.ObjectPair{Key: "text", Value: canonical.NewString(text)},
		)},
	)
}

// anthropicContentBlockStop 构造 content_block_stop 载荷。
func anthropicContentBlockStop(index int64) *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("content_block_stop")},
		canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(index)},
	)
}
