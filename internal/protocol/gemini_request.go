package protocol

import (
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

const gemMaxTools = 32

type gemHistoryCall struct {
	id, name string
	matched  bool
}

func gemMessages(contents, system *canonical.Value) (*canonical.Value, error) {
	if !contents.IsArray() || contents.Len() == 0 {
		return nil, geminiField("contents")
	}
	var messages []*canonical.Value
	if system != nil {
		const path = "systemInstruction"
		fields, err := gemFields(system, path, "role", "parts")
		if err != nil {
			return nil, err
		}
		if role := fields["role"]; role != nil && role.StringValue() != "system" {
			return nil, geminiField(path + ".role")
		}
		parts := fields["parts"]
		if !parts.IsArray() || parts.Len() == 0 {
			return nil, geminiField(path + ".parts")
		}
		var text strings.Builder
		for _, part := range parts.Items() {
			p, err := gemFields(part, path+".parts[]", "text")
			if err != nil {
				return nil, err
			}
			if !p["text"].IsString() {
				return nil, geminiField(path + ".parts[].text")
			}
			text.WriteString(p["text"].Str)
		}
		messages = append(messages, gemObj("role", canonical.NewString("system"), "content", canonical.NewString(text.String())))
	}
	const partPath = "contents[].parts[]"
	var pending []*gemHistoryCall
	seenIDs := map[string]bool{}
	images := 0
	for i, content := range contents.Items() {
		fields, err := gemFields(content, "contents[]", "role", "parts")
		if err != nil {
			return nil, err
		}
		role := "user"
		if v := fields["role"]; v != nil {
			role = v.StringValue()
		}
		if role != "user" && role != "model" {
			return nil, geminiField("contents[].role")
		}
		parts := fields["parts"]
		if !parts.IsArray() || parts.Len() == 0 {
			return nil, geminiField("contents[].parts")
		}
		assistant := role == "model"
		if assistant && len(pending) != 0 {
			return nil, geminiField(partPath + ".functionResponse")
		}
		var blocks, calls []*canonical.Value
		var text strings.Builder
		hasImage := false
		for j, part := range parts.Items() {
			if part.Lookup("thoughtSignature") != nil || part.Lookup("thought_signature") != nil {
				return nil, geminiField(partPath + ".thoughtSignature")
			}
			fields, err := gemFields(part, partPath, "text", "inlineData", "functionCall", "functionResponse", "thought")
			if err != nil {
				return nil, err
			}
			if thought := fields["thought"]; thought != nil && (!thought.IsBool() || thought.Bool) {
				return nil, geminiField(partPath + ".thought")
			}
			if fields["thought"] != nil {
				delete(fields, "thought")
			}
			if len(fields) != 1 {
				return nil, geminiField(partPath)
			}
			switch {
			case fields["text"] != nil:
				v := fields["text"]
				if !v.IsString() || len(pending) != 0 {
					return nil, geminiField(partPath + ".text")
				}
				text.WriteString(v.Str)
				blocks = append(blocks, gemObj("type", canonical.NewString("text"), "text", v))
			case fields["inlineData"] != nil:
				images++
				if assistant || len(pending) != 0 || images > 4 {
					return nil, geminiField(partPath + ".inlineData")
				}
				block, err := gemImage(fields["inlineData"], partPath+".inlineData")
				if err != nil {
					return nil, err
				}
				hasImage = true
				blocks = append(blocks, block)
			case fields["functionCall"] != nil:
				if !assistant || len(calls) >= gemMaxTools {
					return nil, geminiField(partPath + ".functionCall")
				}
				name, id, data, err := gemHistoryFunction(fields["functionCall"], partPath+".functionCall", "args")
				if err != nil {
					return nil, err
				}
				if id == "" {
					id = fmt.Sprintf("amkr_history_%d_%d", i, j)
				}
				if seenIDs[id] {
					return nil, geminiField(partPath + ".functionCall.id")
				}
				seenIDs[id] = true
				calls = append(calls, gemObj("id", canonical.NewString(id), "type", canonical.NewString("function"), "function", gemObj("name", canonical.NewString(name), "arguments", canonical.NewString(canonical.DumpsOrdered(data)))))
				pending = append(pending, &gemHistoryCall{id: id, name: name})
			case fields["functionResponse"] != nil:
				if assistant || len(blocks) != 0 {
					return nil, geminiField(partPath + ".functionResponse")
				}
				name, id, data, err := gemHistoryFunction(fields["functionResponse"], partPath+".functionResponse", "response")
				if err != nil {
					return nil, err
				}
				var match *gemHistoryCall
				for _, item := range pending {
					if !item.matched && item.name == name && (id == "" || id == item.id) {
						if match != nil {
							return nil, geminiField(partPath + ".functionResponse.id")
						}
						match = item
					}
				}
				if match == nil {
					return nil, geminiField(partPath + ".functionResponse.id")
				}
				match.matched = true
				messages = append(messages, gemObj("role", canonical.NewString("tool"), "tool_call_id", canonical.NewString(match.id), "content", canonical.NewString(canonical.DumpsOrdered(data))))
				complete := true
				for _, item := range pending {
					complete = complete && item.matched
				}
				if complete {
					pending = nil
				}
			}
		}
		if len(blocks) != 0 || len(calls) != 0 {
			if assistant {
				role = "assistant"
			}
			value := canonical.NewString(text.String())
			if hasImage || (len(blocks) > 1 && !assistant) {
				value = canonical.NewArray(blocks...)
			}
			if len(blocks) == 0 {
				value = canonical.NewNull()
			}
			message := gemObj("role", canonical.NewString(role), "content", value)
			if len(calls) != 0 {
				message.Obj.Set("tool_calls", canonical.NewArray(calls...))
			}
			messages = append(messages, message)
		}
	}
	if len(pending) != 0 {
		return nil, geminiField(partPath + ".functionResponse")
	}
	return canonical.NewArray(messages...), nil
}

func gemHistoryFunction(v *canonical.Value, path, dataKey string) (name, id string, data *canonical.Value, err error) {
	fields, err := gemFields(v, path, "name", "id", dataKey)
	if err != nil {
		return "", "", nil, err
	}
	if name = fields["name"].StringValue(); strings.TrimSpace(name) == "" {
		return "", "", nil, geminiField(path + ".name")
	}
	if value := fields["id"]; value != nil {
		if id = value.StringValue(); strings.TrimSpace(id) == "" {
			return "", "", nil, geminiField(path + ".id")
		}
	}
	if data = fields[dataKey]; !data.IsObject() {
		return "", "", nil, geminiField(path + "." + dataKey)
	}
	return name, id, data, nil
}

func gemImage(v *canonical.Value, path string) (*canonical.Value, error) {
	fields, err := gemFields(v, path, "mimeType", "data")
	if err != nil {
		return nil, err
	}
	mime := fields["mimeType"].StringValue()
	if mime != "image/png" && mime != "image/jpeg" {
		return nil, geminiField(path + ".mimeType")
	}
	s := fields["data"].StringValue()
	if s == "" || len(s)%4 != 0 || strings.ContainsAny(s, "\r\n") {
		return nil, geminiField(path + ".data")
	}
	size := base64.StdEncoding.DecodedLen(len(s))
	if strings.HasSuffix(s, "=") {
		size--
	}
	if strings.HasSuffix(s, "==") {
		size--
	}
	if size > 8<<20 {
		return nil, &GeminiInputError{http.StatusRequestEntityTooLarge, path + ".data"}
	}
	// DecodeConfig reads only headers; drain the same decoder to validate the entire base64 without holding decoded pixels.
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(s))
	cfg, format, err := image.DecodeConfig(decoder)
	if err != nil || "image/"+format != mime || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 16_000_000/cfg.Height {
		return nil, geminiField(path + ".data")
	}
	if _, err := io.Copy(io.Discard, decoder); err != nil {
		return nil, geminiField(path + ".data")
	}
	return gemObj("type", canonical.NewString("image_url"), "image_url", gemStringObj("url", "data:"+mime+";base64,"+s)), nil
}

func gemTools(payload, tools, config *canonical.Value) error {
	var declarations []*canonical.Value
	declared := map[string]bool{}
	const declPath = "tools[].functionDeclarations"
	if tools != nil {
		if !tools.IsArray() {
			return geminiField("tools")
		}
		for _, tool := range tools.Items() {
			fields, err := gemFields(tool, "tools[]", "functionDeclarations")
			if err != nil {
				return err
			}
			list := fields["functionDeclarations"]
			if !list.IsArray() || list.Len() == 0 {
				return geminiField(declPath)
			}
			for _, decl := range list.Items() {
				f, err := gemFields(decl, declPath+"[]", "name", "description", "parameters")
				if err != nil {
					return err
				}
				name := f["name"].StringValue()
				if strings.TrimSpace(name) == "" || declared[name] {
					return geminiField(declPath + "[].name")
				}
				declared[name] = true
				function := gemObj("name", f["name"])
				if v := f["description"]; v != nil {
					if !v.IsString() {
						return geminiField(declPath + "[].description")
					}
					function.Obj.Set("description", v)
				}
				if v := f["parameters"]; v != nil {
					if !v.IsObject() {
						return geminiField(declPath + "[].parameters")
					}
					function.Obj.Set("parameters", v)
				}
				declarations = append(declarations, gemObj("type", canonical.NewString("function"), "function", function))
			}
		}
	}
	if config != nil {
		const fcPath = "toolConfig.functionCallingConfig"
		f, err := gemFields(config, "toolConfig", "functionCallingConfig")
		if err != nil {
			return err
		}
		mode := "auto"
		if v := f["functionCallingConfig"]; v != nil {
			cfg, err := gemFields(v, fcPath, "mode", "allowedFunctionNames")
			if err != nil {
				return err
			}
			if v := cfg["mode"]; v != nil {
				switch v.StringValue() {
				case "AUTO":
				case "NONE":
					mode = "none"
				case "ANY":
					mode = "required"
				default:
					return geminiField(fcPath + ".mode")
				}
			}
			if v := cfg["allowedFunctionNames"]; v != nil {
				if mode != "required" || !v.IsArray() || v.Len() == 0 {
					return geminiField(fcPath + ".allowedFunctionNames")
				}
				keep := map[string]bool{}
				for _, item := range v.Items() {
					name := item.StringValue()
					if !declared[name] || keep[name] {
						return geminiField(fcPath + ".allowedFunctionNames")
					}
					keep[name] = true
				}
				filtered := declarations[:0]
				for _, d := range declarations {
					if keep[d.Lookup("function").Lookup("name").Str] {
						filtered = append(filtered, d)
					}
				}
				declarations = filtered
			}
		}
		if mode == "required" && len(declarations) == 0 {
			return geminiField(fcPath + ".mode")
		}
		payload.Obj.Set("tool_choice", canonical.NewString(mode))
	}
	if len(declarations) > 0 {
		payload.Obj.Set("tools", canonical.NewArray(declarations...))
	}
	return nil
}
