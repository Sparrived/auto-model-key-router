package protocol

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

var (
	ErrGeminiUpstream   = errors.New("上游返回了无法转换的 Chat 响应")
	ErrGeminiIncomplete = errors.New("上游未提供流终止原因")
)

const gemMaxToolArgs = 256 << 10

type gemOutputCall struct {
	id, name  string
	arguments strings.Builder
}

func gemOutputCalls(calls []*gemOutputCall) ([]*canonical.Value, error) {
	var parts []*canonical.Value
	used := map[string]bool{}
	for _, c := range calls {
		if c == nil || strings.TrimSpace(c.name) == "" {
			return nil, ErrGeminiUpstream
		}
		if c.id != "" {
			if used[c.id] {
				return nil, ErrGeminiUpstream
			}
			used[c.id] = true
		}
	}
	for i, c := range calls {
		obj, err := gemParse([]byte(c.arguments.String()))
		if err != nil || !obj.IsObject() {
			return nil, ErrGeminiUpstream
		}
		id := c.id
		if id == "" {
			id = "amkr_call_" + strconv.Itoa(i)
			for n := 1; used[id]; n++ {
				id = fmt.Sprintf("amkr_call_%d_%d", i, n)
			}
			used[id] = true
		}
		parts = append(parts, gemObj("functionCall", gemObj("id", canonical.NewString(id), "name", canonical.NewString(c.name), "args", obj)))
	}
	return parts, nil
}

func gemOutputPart(v *canonical.Value, thought bool) (*canonical.Value, error) {
	if v.IsNull() {
		return nil, nil
	}
	if !v.IsString() {
		return nil, ErrGeminiUpstream
	}
	if v.Str == "" {
		return nil, nil
	}
	obj := gemObj("text", v)
	if thought {
		obj.Obj.Set("thought", canonical.NewBool(true))
	}
	return obj, nil
}

func gemFinish(v *canonical.Value) (string, error) {
	if v.IsNull() {
		return "", nil
	}
	if !v.IsString() || strings.TrimSpace(v.Str) == "" {
		return "", ErrGeminiUpstream
	}
	switch v.Str {
	case "stop", "tool_calls":
		return "STOP", nil
	case "length":
		return "MAX_TOKENS", nil
	case "content_filter":
		return "SAFETY", nil
	default:
		return "OTHER", nil
	}
}

func gemResponse(parts []*canonical.Value, finish string, usage *canonical.Value) *canonical.Value {
	candidate := gemObj("index", canonical.NewIntValue(0))
	if len(parts) != 0 {
		candidate.Obj.Set("content", gemObj("role", canonical.NewString("model"), "parts", canonical.NewArray(parts...)))
	}
	if finish != "" {
		candidate.Obj.Set("finishReason", canonical.NewString(finish))
	}
	result := gemObj("candidates", canonical.NewArray(candidate))
	if usage.Len() > 0 {
		result.Obj.Set("usageMetadata", usage)
	}
	return result
}

// gemUsageMetadata keeps presence. A generic Chat-compatible provider does not establish reasoning inclusion.
func gemUsageMetadata(usage *canonical.Value, reasoningIncluded bool) *canonical.Value {
	result := canonical.NewObject()
	for _, entry := range []struct {
		value *canonical.Value
		name  string
	}{
		{usage.Lookup("prompt_tokens"), "promptTokenCount"},
		{usage.Lookup("total_tokens"), "totalTokenCount"},
		{usage.Lookup("prompt_tokens_details").Lookup("cached_tokens"), "cachedContentTokenCount"},
	} {
		if n, ok := gemInteger(entry.value); ok && n >= 0 {
			result.Obj.Set(entry.name, entry.value)
		}
	}
	if reasoningIncluded {
		r, rok := gemInteger(usage.Lookup("completion_tokens_details").Lookup("reasoning_tokens"))
		c, cok := gemInteger(usage.Lookup("completion_tokens"))
		if rok && cok && r >= 0 && c >= r {
			result.Obj.Set("thoughtsTokenCount", canonical.NewIntValue(r))
			result.Obj.Set("candidatesTokenCount", canonical.NewIntValue(c-r))
		}
	}
	return result
}

func GeminiResponse(raw []byte, includeThoughts, reasoningIncluded bool) (*canonical.Value, *canonical.Value, error) {
	root, err := gemParse(raw)
	if err != nil || !root.IsObject() {
		return nil, nil, ErrGeminiUpstream
	}
	usage := root.Lookup("usage")
	if !usage.IsObject() {
		usage = nil
	}
	choices := root.Lookup("choices")
	if !root.Lookup("error").IsNull() || !choices.IsArray() || choices.Len() != 1 {
		return nil, usage, ErrGeminiUpstream
	}
	choice := choices.Items()[0]
	if index := choice.Lookup("index"); index != nil {
		if i, ok := gemInteger(index); !ok || i != 0 {
			return nil, usage, ErrGeminiUpstream
		}
	}
	message := choice.Lookup("message")
	if !message.IsObject() {
		return nil, usage, ErrGeminiUpstream
	}
	var parts []*canonical.Value
	if includeThoughts && message.Lookup("reasoning_content").IsString() {
		if part, _ := gemOutputPart(message.Lookup("reasoning_content"), true); part != nil {
			parts = append(parts, part)
		}
	}
	part, err := gemOutputPart(message.Lookup("content"), false)
	if err != nil {
		return nil, usage, err
	}
	if part != nil {
		parts = append(parts, part)
	}
	toolCalls := message.Lookup("tool_calls")
	if !toolCalls.IsNull() {
		if !toolCalls.IsArray() || toolCalls.Len() > gemMaxTools {
			return nil, usage, ErrGeminiUpstream
		}
		calls := make([]*gemOutputCall, 0, toolCalls.Len())
		for _, tool := range toolCalls.Items() {
			call := &gemOutputCall{}
			if !tool.IsObject() || call.add(tool, false) != nil {
				return nil, usage, ErrGeminiUpstream
			}
			calls = append(calls, call)
		}
		toolParts, err := gemOutputCalls(calls)
		if err != nil {
			return nil, usage, err
		}
		parts = append(parts, toolParts...)
	}
	finish, err := gemFinish(choice.Lookup("finish_reason"))
	if err != nil || (len(parts) == 0 && (finish == "" || finish == "OTHER" || choice.Lookup("finish_reason").StringValue() == "tool_calls")) {
		return nil, usage, ErrGeminiUpstream
	}
	return gemResponse(parts, finish, gemUsageMetadata(usage, reasoningIncluded)), usage, nil
}

func (c *gemOutputCall) add(tool *canonical.Value, streamed bool) error {
	if v := tool.Lookup("type"); !v.IsNull() && v.StringValue() != "function" {
		return ErrGeminiUpstream
	}
	if v := tool.Lookup("id"); !v.IsNull() {
		if !v.IsString() || len(v.Str) > 4096 || (c.id != "" && v.Str != "" && c.id != v.Str) {
			return ErrGeminiUpstream
		}
		if v.Str != "" {
			c.id = v.Str
		}
	}
	fn := tool.Lookup("function")
	if fn.IsNull() && streamed {
		return nil
	}
	if !fn.IsObject() {
		return ErrGeminiUpstream
	}
	if v := fn.Lookup("name"); !v.IsNull() {
		if !v.IsString() || len(c.name)+len(v.Str) > 4096 {
			return ErrGeminiUpstream
		}
		c.name += v.Str
	}
	if v := fn.Lookup("arguments"); !v.IsNull() {
		if !v.IsString() || c.arguments.Len()+len(v.Str) > gemMaxToolArgs {
			return ErrGeminiUpstream
		}
		c.arguments.WriteString(v.Str)
	}
	return nil
}

// GeminiStream holds one upstream attempt. No partial tool argument is sent downstream.
type GeminiStream struct {
	IncludeThoughts, ReasoningIncluded bool
	calls                              []*gemOutputCall
	finish, rawFinish                  string
	usage                              *canonical.Value
}

func (s *GeminiStream) Usage() *canonical.Value { return s.usage }

func (s *GeminiStream) Update(raw []byte) (frame *canonical.Value, ready bool, err error) {
	root, err := gemParse(raw)
	if err != nil || !root.IsObject() || !root.Lookup("error").IsNull() {
		return nil, false, ErrGeminiUpstream
	}
	usage := root.Lookup("usage")
	if usage.IsObject() {
		s.usage = gemMergeUsage(s.usage, usage)
	}
	choices := root.Lookup("choices")
	if !choices.IsArray() || choices.Len() > 1 {
		return nil, false, ErrGeminiUpstream
	}
	if choices.Len() == 0 {
		if !usage.IsObject() {
			return nil, false, ErrGeminiUpstream
		}
		return nil, false, nil
	}
	if s.finish != "" {
		return nil, false, ErrGeminiUpstream
	}
	choice := choices.Items()[0]
	if index, ok := gemInteger(choice.Lookup("index")); !ok || index != 0 {
		return nil, false, ErrGeminiUpstream
	}
	stop, err := gemFinish(choice.Lookup("finish_reason"))
	if err != nil {
		return nil, false, err
	}
	delta := choice.Lookup("delta")
	if !delta.IsObject() {
		return nil, false, ErrGeminiUpstream
	}
	if role := delta.Lookup("role"); !role.IsNull() && role.StringValue() != "assistant" {
		return nil, false, ErrGeminiUpstream
	}
	var parts []*canonical.Value
	reason := delta.Lookup("reasoning_content")
	if s.IncludeThoughts && reason.IsString() {
		if p, _ := gemOutputPart(reason, true); p != nil {
			parts = append(parts, p)
		}
	}
	if p, e := gemOutputPart(delta.Lookup("content"), false); e != nil {
		return nil, false, e
	} else if p != nil {
		parts = append(parts, p)
	}
	tools := delta.Lookup("tool_calls")
	if !tools.IsNull() {
		if !tools.IsArray() || tools.Len() == 0 || tools.Len() > gemMaxTools {
			return nil, false, ErrGeminiUpstream
		}
		indices := map[int64]bool{}
		for _, tool := range tools.Items() {
			idx, ok := gemInteger(tool.Lookup("index"))
			if !ok || idx < 0 || idx >= gemMaxTools || indices[idx] {
				return nil, false, ErrGeminiUpstream
			}
			indices[idx] = true
			for int(idx) >= len(s.calls) {
				s.calls = append(s.calls, nil)
			}
			c := s.calls[idx]
			if c == nil {
				c = &gemOutputCall{}
				s.calls[idx] = c
			}
			if err := c.add(tool, true); err != nil {
				return nil, false, err
			}
		}
	}
	if stop != "" {
		s.finish, s.rawFinish = stop, choice.Lookup("finish_reason").Str
	}
	if len(parts) > 0 {
		frame = gemResponse(parts, "", nil)
	}
	ready = stop != "" || len(parts) > 0 || delta.Lookup("role").StringValue() == "assistant" || reason.IsString() || tools.IsArray()
	return frame, ready, nil
}

func (s *GeminiStream) Finish() (*canonical.Value, error) {
	if s.finish == "" {
		return nil, ErrGeminiIncomplete
	}
	if s.rawFinish == "tool_calls" && len(s.calls) == 0 {
		return nil, ErrGeminiUpstream
	}
	parts, err := gemOutputCalls(s.calls)
	if err != nil {
		return nil, err
	}
	return gemResponse(parts, s.finish, gemUsageMetadata(s.usage, s.ReasoningIncluded)), nil
}

func gemMergeUsage(old, new *canonical.Value) *canonical.Value {
	if old == nil {
		old = canonical.NewObject()
	}
	for _, name := range new.Obj.Keys() {
		v := new.Lookup(name)
		if v.IsObject() {
			prior := old.Lookup(name)
			if !prior.IsObject() {
				prior = nil
			}
			old.Obj.Set(name, gemMergeUsage(prior, v))
		} else if !v.IsNull() {
			old.Obj.Set(name, v)
		}
	}
	return old
}
