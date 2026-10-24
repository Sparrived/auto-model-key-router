package proxy

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/protocol"
)

var (
	errUpstreamStream   = errors.New("upstream returned a stream error")
	errEmptyStream      = errors.New("upstream stream contained no output")
	errIncompleteStream = errors.New("upstream stream ended before completion")
	errStreamConversion = errors.New("upstream stream could not be converted")
)

// streamOutcome 只检查聊天 SSE；传输 EOF 不等于模型已经完成。事件缓冲保留分片边界，
// 正常终止后仍继续读 usage 尾帧，不重放已经开始的请求。
type streamOutcome struct {
	splitter       sseEventSplitter
	content        bool
	terminal       bool
	failure        error
	forwardedError bool
	neutralHealth  bool
	choices        map[string]bool
	payload        *canonical.Value
}

func checkStreamOutcome(path, mediaType string) bool {
	return isSSEMediaType(mediaType) && (path == "chat/completions" || path == "messages" || path == "responses")
}

func (s *streamOutcome) complete() bool { return s.content && s.terminal && s.failure == nil }

func (s *streamOutcome) eofError() error {
	if s.failure != nil {
		return s.failure
	}
	if !s.content {
		return errEmptyStream
	}
	if !s.terminal {
		return errIncompleteStream
	}
	return nil
}

// observe 复用协议层的 choices/content 访问器；event 字段也参与失败判定，不能只看 data。
func (s *streamOutcome) observe(event []byte) {
	s.payload = nil
	var name string
	var data [][]byte
	for _, line := range bytes.Split(event, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if bytes.HasPrefix(line, []byte("event:")) {
			name = strings.TrimSpace(string(line[len("event:"):]))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			data = append(data, bytes.TrimPrefix(line[len("data:"):], []byte{' '}))
		}
	}
	raw := bytes.TrimSpace(bytes.Join(data, []byte{'\n'}))
	if bytes.Equal(raw, []byte("[DONE]")) {
		s.terminal = true
		return
	}
	payload := parseJSONOrNil(raw)
	s.payload = payload
	kind, _ := payload.Lookup("type").AsString()
	if kind == "" {
		kind = name
	}
	response := payload.Lookup("response")
	if kind == "response.incomplete" || name == "response.incomplete" || response.Lookup("status").PyStr() == "incomplete" {
		reason := response.Lookup("incomplete_details").Lookup("reason").PyStr()
		s.neutralHealth = reason == "max_output_tokens" || reason == "content_filter"
	}
	if name == "error" || name == "response.failed" || name == "response.incomplete" ||
		kind == "error" || kind == "response.failed" || kind == "response.incomplete" ||
		payload.Lookup("error").Truthy() || response.Lookup("error").Truthy() ||
		response.Lookup("status").PyStr() == "failed" || response.Lookup("status").PyStr() == "incomplete" {
		s.failure = errUpstreamStream
		return
	}

	choices := protocol.OpenaiChoices(payload)
	for index, choice := range choices {
		if s.choices == nil {
			s.choices = map[string]bool{}
		}
		key := strconv.Itoa(index)
		if value, ok := choice.LookupOK("index"); ok {
			key = value.PyStr()
		}
		if _, ok := s.choices[key]; !ok {
			s.choices[key] = false
		}
		for _, delta := range []*canonical.Value{choice.Lookup("delta"), choice.Lookup("message")} {
			if hasStreamContent(delta.Lookup("content")) ||
				delta.Lookup("reasoning_content").Truthy() || delta.Lookup("reasoning").Truthy() ||
				delta.Lookup("thinking").Truthy() || delta.Lookup("refusal").Truthy() ||
				delta.Lookup("tool_calls").Truthy() || delta.Lookup("function_call").Truthy() ||
				delta.Lookup("audio").Truthy() {
				s.content = true
			}
		}
		if reason := choice.Lookup("finish_reason"); reason.Truthy() {
			s.choices[key] = true
			if reason.Str == "content_filter" {
				s.content = true
			}
		}
	}
	if len(choices) > 0 {
		s.terminal = true
		for _, finished := range s.choices {
			s.terminal = s.terminal && finished
		}
	}

	switch kind {
	case "message_start":
		s.content = s.content || hasStreamContent(payload.Lookup("message").Lookup("content"))
	case "content_block_start":
		s.content = s.content || hasStreamContent(payload.Lookup("content_block"))
	case "content_block_delta":
		delta := payload.Lookup("delta")
		s.content = s.content || delta.Lookup("text").Truthy() || delta.Lookup("thinking").Truthy() || delta.Lookup("partial_json").Truthy()
	case "message_stop":
		s.terminal = true
	case "response.completed":
		s.content = s.content || hasStreamContent(response.Lookup("output"))
		s.terminal = true
	case "response.output_item.added", "response.output_item.done":
		s.content = s.content || hasStreamContent(payload.Lookup("item"))
	case "response.content_part.added", "response.content_part.done":
		s.content = s.content || hasStreamContent(payload.Lookup("part"))
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta":
		s.content = s.content || payload.Lookup("delta").Truthy()
	case "response.output_text.done", "response.reasoning_text.done", "response.reasoning_summary_text.done":
		s.content = s.content || payload.Lookup("text").Truthy()
	case "response.function_call_arguments.done":
		s.content = s.content || payload.Lookup("arguments").Truthy()
	}
}

func hasStreamContent(value *canonical.Value) bool {
	if value.IsString() {
		return value.Str != ""
	}
	if value.IsArray() {
		for _, item := range value.Items() {
			if hasStreamContent(item) {
				return true
			}
		}
		return false
	}
	if !value.IsObject() {
		return false
	}
	for _, key := range []string{"text", "thinking", "refusal", "content", "summary"} {
		if hasStreamContent(value.Lookup(key)) {
			return true
		}
	}
	kind, _ := value.Lookup("type").AsString()
	switch kind {
	case "", "text", "output_text", "reasoning_text", "thinking", "refusal", "message", "reasoning":
		return false
	default:
		// 工具调用、音频、图片和 redacted_thinking 本身也是业务输出，不要求文本 delta。
		return true
	}
}

// 转换器按 data 行读取：把标准多行 data JSON 合并成一行，并适配两个常见增量方言。
// raw 路径不调用它，保留上游原始事件字节。
func normalizedConversionEvent(payload *canonical.Value) []byte {
	for _, choice := range protocol.OpenaiChoices(payload) {
		delta := choice.Lookup("delta")
		if choice.Lookup("finish_reason").PyStr() == "content_filter" && !delta.IsObject() {
			delta = canonical.NewObject()
			choice.SetKey("delta", delta)
		}
		if choice.Lookup("finish_reason").PyStr() == "content_filter" && protocol.DeltaText(delta) == "" {
			delta.SetKey("content", canonical.NewString("上游内容安全策略阻止了输出"))
		}
		if protocol.DeltaText(delta) == "" && delta.Lookup("refusal").IsString() {
			delta.SetKey("content", delta.Lookup("refusal"))
		}
		if function := delta.Lookup("function_call"); function.IsObject() && !delta.Lookup("tool_calls").Truthy() {
			delta.SetKey("tool_calls", canonical.NewArray(canonical.NewObjectOf(
				canonical.ObjectPair{Key: "index", Value: canonical.NewIntValue(0)},
				canonical.ObjectPair{Key: "type", Value: canonical.NewString("function")},
				canonical.ObjectPair{Key: "function", Value: function},
			)))
			delta.DeleteKey("function_call")
		}
	}
	return []byte("data: " + canonical.DumpsOrdered(payload) + "\n\n")
}

// 新增错误只使用本地固定文案，不把上游可能含密钥/URL 的错误详情写给客户端。
func writeStreamError(w http.ResponseWriter, path string) {
	inner := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "type", Value: canonical.NewString("api_error")},
		canonical.ObjectPair{Key: "code", Value: canonical.NewString("upstream_stream_error")},
		canonical.ObjectPair{Key: "message", Value: canonical.NewString("上游流式响应失败或未完整结束")},
	)
	switch path {
	case "messages":
		_ = writeSSE(w, "error", canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("error")},
			canonical.ObjectPair{Key: "error", Value: inner},
		))
	case "responses":
		_ = writeSSE(w, "error", canonical.NewObjectOf(
			canonical.ObjectPair{Key: "type", Value: canonical.NewString("error")},
			canonical.ObjectPair{Key: "code", Value: inner.Lookup("code")},
			canonical.ObjectPair{Key: "message", Value: inner.Lookup("message")},
		))
	default:
		_ = writeBytes(w, []byte("data: "+canonical.DumpsOrdered(canonical.NewObjectOf(canonical.ObjectPair{Key: "error", Value: inner}))+"\n\n"))
	}
}
