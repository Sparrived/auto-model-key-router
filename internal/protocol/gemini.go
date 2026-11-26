package protocol

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

const GeminiMaxJSONBytes = 20 << 20

// GeminiInputError contains only known field paths or fixed descriptions.
type GeminiInputError struct {
	Code  int
	Field string
}

func (e *GeminiInputError) Error() string { return "无效或不支持的 Gemini 字段：" + e.Field }
func geminiField(path string) error       { return &GeminiInputError{http.StatusBadRequest, path} }

func GeminiError(code int, message string) *canonical.Value {
	status, defaultMessage := "UNAVAILABLE", "上游服务不可用或返回了无效响应"
	switch code {
	case 400:
		status, defaultMessage = "INVALID_ARGUMENT", "请求参数无效或不受支持"
	case 401:
		status, defaultMessage = "UNAUTHENTICATED", "凭据缺失或无效"
	case 403:
		status, defaultMessage = "PERMISSION_DENIED", "无权执行该请求"
	case 404:
		status, defaultMessage = "NOT_FOUND", "端点、模型或目标不存在"
	case 405:
		status, defaultMessage = "METHOD_NOT_ALLOWED", "仅支持 POST"
	case 413:
		status, defaultMessage = "RESOURCE_EXHAUSTED", "请求体或图片超过大小上限"
	case 415:
		status, defaultMessage = "UNSUPPORTED_MEDIA_TYPE", "请求体必须为 UTF-8 application/json"
	case 429:
		status, defaultMessage = "RESOURCE_EXHAUSTED", "上游暂时限流"
	case 500:
		status, defaultMessage = "INTERNAL", "服务内部错误"
	case 504:
		status, defaultMessage = "DEADLINE_EXCEEDED", "等待上游响应超时"
	}
	if message == "" {
		message = defaultMessage
	}
	return gemObj("error", gemObj("code", canonical.NewIntValue(int64(code)), "status", canonical.NewString(status), "message", canonical.NewString(message)))
}

func WriteGeminiError(w http.ResponseWriter, code int, message string) {
	body := []byte(canonical.DumpsOrdered(GeminiError(code, message)))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func gemObj(pairs ...any) *canonical.Value {
	v := canonical.NewObject()
	for i := 0; i < len(pairs); i += 2 {
		v.Obj.Set(pairs[i].(string), pairs[i+1].(*canonical.Value))
	}
	return v
}

func gemStringObj(key, value string) *canonical.Value { return gemObj(key, canonical.NewString(value)) }

func gemParse(data []byte) (*canonical.Value, error) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return nil, fmt.Errorf("invalid UTF-8 JSON")
	}
	return canonical.Parse(data)
}

// gemFields canonicalizes only the fields of the current object, not opaque user JSON schemas or arguments.
func gemFields(obj *canonical.Value, path string, names ...string) (map[string]*canonical.Value, error) {
	if !obj.IsObject() {
		return nil, geminiField(path)
	}
	fields := make(map[string]*canonical.Value, len(names))
	for _, key := range obj.Obj.Keys() {
		matched := ""
		for _, name := range names {
			if key == name || key == gemSnake(name) {
				matched = name
				break
			}
		}
		if matched == "" {
			return nil, geminiField(path + ".<未知字段>")
		}
		if _, duplicate := fields[matched]; duplicate {
			return nil, geminiField(path + "." + matched)
		}
		fields[matched] = obj.Lookup(key)
	}
	return fields, nil
}

func gemSnake(name string) string {
	buf := make([]byte, 0, len(name)+4)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			buf = append(buf, '_')
			c += 'a' - 'A'
		}
		buf = append(buf, c)
	}
	return string(buf)
}

func gemInteger(v *canonical.Value) (int64, bool) {
	if !v.IsNumber() {
		return 0, false
	}
	n, err := strconv.ParseInt(v.Num, 10, 64)
	return n, err == nil
}

// GeminiRequest translates only the first-version Gemini subset into an independent Chat payload.
func GeminiRequest(raw []byte, model string, stream bool) (*canonical.Value, bool, error) {
	root, err := gemParse(raw)
	if err != nil {
		return nil, false, geminiField("请求体 JSON")
	}
	for _, name := range []string{"model", "stream"} {
		if root.Lookup(name) != nil {
			return nil, false, geminiField(name)
		}
	}
	fields, err := gemFields(root, "请求体", "contents", "systemInstruction", "generationConfig", "tools", "toolConfig")
	if err != nil {
		return nil, false, err
	}
	payload := gemObj("model", canonical.NewString(model), "stream", canonical.NewBool(stream))
	messages, err := gemMessages(fields["contents"], fields["systemInstruction"])
	if err != nil {
		return nil, false, err
	}
	payload.Obj.Set("messages", messages)
	thoughts, err := gemGeneration(payload, fields["generationConfig"])
	if err != nil {
		return nil, false, err
	}
	if err := gemTools(payload, fields["tools"], fields["toolConfig"]); err != nil {
		return nil, false, err
	}
	return payload, thoughts, nil
}

func gemGeneration(payload, generation *canonical.Value) (bool, error) {
	if generation == nil {
		return false, nil
	}
	const path = "generationConfig"
	fields, err := gemFields(generation, path, "temperature", "topP", "maxOutputTokens", "stopSequences", "candidateCount", "thinkingConfig")
	if err != nil {
		return false, err
	}
	for _, spec := range []struct {
		field, chat string
		max         float64
	}{{"temperature", "temperature", 2}, {"topP", "top_p", 1}} {
		if v := fields[spec.field]; v != nil {
			n, err := strconv.ParseFloat(v.Num, 64)
			if !v.IsNumber() || err != nil || !(n >= 0 && n <= spec.max) {
				return false, geminiField(path + "." + spec.field)
			}
			payload.Obj.Set(spec.chat, v)
		}
	}
	if v := fields["maxOutputTokens"]; v != nil {
		n, ok := gemInteger(v)
		if !ok || n < 1 || n > 2147483647 {
			return false, geminiField(path + ".maxOutputTokens")
		}
		payload.Obj.Set("max_tokens", v)
	}
	if v := fields["candidateCount"]; v != nil {
		if n, ok := gemInteger(v); !ok || n != 1 {
			return false, geminiField(path + ".candidateCount")
		}
	}
	if v := fields["stopSequences"]; v != nil {
		if !v.IsArray() || v.Len() < 1 || v.Len() > 5 {
			return false, geminiField(path + ".stopSequences")
		}
		for _, stop := range v.Items() {
			if !stop.IsString() || len(stop.Str) == 0 || len(stop.Str) > 256 {
				return false, geminiField(path + ".stopSequences")
			}
		}
		payload.Obj.Set("stop", v)
	}
	if v := fields["thinkingConfig"]; v != nil {
		const thinking = path + ".thinkingConfig"
		for _, name := range []string{"thinkingBudget", "thinkingLevel"} {
			if v.Lookup(name) != nil || v.Lookup(gemSnake(name)) != nil {
				return false, geminiField(thinking + "." + name)
			}
		}
		cfg, err := gemFields(v, thinking, "includeThoughts")
		if err != nil {
			return false, err
		}
		if value := cfg["includeThoughts"]; value != nil {
			if !value.IsBool() {
				return false, geminiField(thinking + ".includeThoughts")
			}
			return value.Bool, nil
		}
	}
	return false, nil
}
