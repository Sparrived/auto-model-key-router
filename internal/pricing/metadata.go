package pricing

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// MetadataSnapshot 与价格来自同一目录版本，内部索引只读。
type MetadataSnapshot struct {
	entries map[string]*canonical.Value
}

// Lookup 只按原始、区分大小写的对外名查询；副本可由调用方安全修改。
func (s *MetadataSnapshot) Lookup(id string) *canonical.Value {
	if s == nil || s.entries[id] == nil {
		return nil
	}
	return s.entries[id].Clone()
}

func buildMetadata(parsed *canonical.Value) *MetadataSnapshot {
	index := make(map[string]*canonical.Value)
	seen := make(map[string]bool)
	if parsed.IsObject() {
		for _, providerID := range parsed.Obj.Keys() {
			provider, _ := parsed.Obj.Get(providerID)
			models := provider.Lookup("models")
			if !models.IsObject() {
				continue
			}
			for _, id := range models.Obj.Keys() {
				model, _ := models.Obj.Get(id)
				candidate := metadataOf(id, model)
				if !seen[id] {
					seen[id] = true
					index[id] = candidate
					continue
				}
				// 每个字段都必须在所有候选中有效且一致，缺失不能由别家补齐。
				current := index[id]
				for _, field := range current.Obj.Keys() {
					other := candidate.Lookup(field)
					if field == "modalities" && other.IsObject() {
						directions := current.Lookup(field)
						for _, direction := range directions.Obj.Keys() {
							value := other.Lookup(direction)
							if value == nil || canonical.DumpsOrdered(directions.Lookup(direction)) != canonical.DumpsOrdered(value) {
								directions.DeleteKey(direction)
							}
						}
						if directions.Len() == 0 {
							current.DeleteKey(field)
						}
					} else if other == nil || canonical.DumpsOrdered(current.Lookup(field)) != canonical.DumpsOrdered(other) {
						current.DeleteKey(field)
					}
				}
			}
		}
	}
	return &MetadataSnapshot{entries: index}
}

func metadataOf(id string, model *canonical.Value) *canonical.Value {
	result := canonical.NewObject()
	if !model.IsObject() {
		return result
	}
	if actual, exists := model.LookupOK("id"); exists && (!actual.IsString() || actual.Str != id) {
		return result
	}
	if name, ok := model.Lookup("name").AsString(); ok {
		valid := utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl)
		name = strings.TrimSpace(name)
		if valid && len(name) > 0 && len(name) <= 128 {
			result.SetKey("display_name", canonical.NewString(name))
		}
	}
	if reasoning, ok := model.Lookup("reasoning").AsBool(); ok {
		result.SetKey("reasoning", canonical.NewBool(reasoning))
	}
	limits := model.Lookup("limit")
	context, hasContext := metadataInteger(limits.Lookup("context"))
	output, hasOutput := metadataInteger(limits.Lookup("output"))
	if hasContext && hasOutput && output > context {
		hasContext, hasOutput = false, false
	}
	if hasContext {
		result.SetKey("context_window", canonical.NewIntValue(context))
	}
	if hasOutput {
		result.SetKey("max_output_tokens", canonical.NewIntValue(output))
	}
	modalities := canonical.NewObject()
	for _, direction := range []string{"input", "output"} {
		if values := metadataModalities(model.Lookup("modalities").Lookup(direction)); values != nil {
			modalities.SetKey(direction, values)
		}
	}
	if modalities.Len() > 0 {
		result.SetKey("modalities", modalities)
	}
	return result
}

func metadataInteger(value *canonical.Value) (int64, bool) {
	if !value.IsNumber() {
		return 0, false
	}
	// canonical 已将浮点解析为 float64，拒绝它可避免舍入后把小数误当作已知整数。
	if strings.ContainsAny(value.Num, ".eE") {
		return 0, false
	}
	number, err := strconv.ParseInt(value.Num, 10, 64)
	return number, err == nil && number > 0 && number <= 1<<53-1
}

func metadataModalities(value *canonical.Value) *canonical.Value {
	if !value.IsArray() || value.Len() == 0 {
		return nil
	}
	values := make([]string, 0, value.Len())
	for _, item := range value.Items() {
		name, ok := item.AsString()
		if !ok {
			return nil
		}
		switch name {
		case "text", "image", "audio", "video", "pdf":
			values = append(values, name)
		default:
			return nil
		}
	}
	slices.Sort(values)
	values = slices.Compact(values)
	result := make([]*canonical.Value, 0, len(values))
	for _, name := range values {
		result = append(result, canonical.NewString(name))
	}
	return canonical.NewArray(result...)
}
