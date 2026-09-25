package api

import (
	"sort"
	"strconv"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/formatting"
)

// 本文件复刻 management_api.py 里所有「响应构造」函数。键的顺序必须与 Python 的
// 字典字面量一致：响应体是逐字节比对的对象，键序不同就是失败。

// nullableString 把 Go 的空串渲染成 JSON null。
//
// 参照实现里 unified_model/task 的可选字段是 `str | None`，而 Go 的
// config.RouteTarget.Key / TaskConfig.FallbackModel 用空串表示「没有」。两者必须
// 在边界上换算，否则响应体里会冒出 Python 永远不会输出的 ""。
func nullableString(value string) *canonical.Value {
	if value == "" {
		return canonical.NewNull()
	}
	return canonical.NewString(value)
}

// stringArray 把字符串切片渲染成 JSON 数组；nil 渲染成空数组。
//
// Python 的 list(...) 永远给数组而不是 null（config 层的 aliases 默认是 []）。
func stringArray(values []string) *canonical.Value {
	items := make([]*canonical.Value, 0, len(values))
	for _, value := range values {
		items = append(items, canonical.NewString(value))
	}
	return canonical.NewArray(items...)
}

// serializePlan 对应 management_api.py:213 的 serialize_plan。
func serializePlan(plan config.RoutePlan) *canonical.Value {
	result := canonical.NewObject()
	result.SetKey("primary", objectOf(
		canonical.ObjectPair{Key: "model", Value: canonical.NewString(plan.Primary.Model)},
		canonical.ObjectPair{Key: "key", Value: nullableString(plan.Primary.Key)},
	))
	if plan.Fallback != nil {
		result.SetKey("fallback", objectOf(
			canonical.ObjectPair{Key: "model", Value: canonical.NewString(plan.Fallback.Model)},
			canonical.ObjectPair{Key: "key", Value: nullableString(plan.Fallback.Key)},
		))
	}
	return result
}

// serializeUnified 对应 management_api.py:209 的 serialize_unified。
func serializeUnified(cfg *config.RouterConfig) *canonical.Value {
	if cfg == nil || cfg.UnifiedModel == nil {
		return canonical.NewNull()
	}
	unified := cfg.UnifiedModel
	result := canonical.NewObject()
	result.SetKey("default", serializePlan(unified.Default))
	if unified.Image != nil {
		result.SetKey("image", serializePlan(*unified.Image))
	}
	if unified.Embeddings != nil {
		result.SetKey("embeddings", serializePlan(*unified.Embeddings))
	}
	return result
}

// taskResponse 对应 management_api.py:1416 的 _task_response。
//
// display_name 是 Go 侧新增（无 Python 先例，见 docs/API.md 与 CHANGELOG），且**只在
// 设了名字时才出现**：没有显示名的任务响应因此与改动前逐字节相同，已发布的 tasks
// 接口对既有调用方零影响。这里的条件包含与 unified 响应里 image/embeddings 只在
// 配置了才出现的写法一致。
func taskResponse(task config.TaskConfig) *canonical.Value {
	params := task.Params
	if params == nil {
		params = canonical.NewObject()
	}
	result := objectOf(
		canonical.ObjectPair{Key: "name", Value: canonical.NewString(task.Name)},
	)
	if task.DisplayName != "" {
		result.SetKey("display_name", canonical.NewString(task.DisplayName))
	}
	result.SetKey("model", canonical.NewString(task.Model))
	result.SetKey("fallback_model", nullableString(task.FallbackModel))
	result.SetKey("params", params)
	return result
}

// modelResponse 对应 management_api.py:1440 的 _model_response。
//
// 可调用名只有 id 与 aliases，两者都会出现在 /v1/models 里，因此响应里不再有
// hidden_aliases / auto_hidden_aliases（上游名只是上游名）。
func modelResponse(model config.ModelConfig) *canonical.Value {
	keys := canonical.NewArray()
	for _, key := range model.Keys {
		keys.Arr = append(keys.Arr, keyResponse(key))
	}
	return objectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString(model.ID)},
		canonical.ObjectPair{Key: "aliases", Value: stringArray(model.Aliases)},
		canonical.ObjectPair{Key: "routing_mode", Value: canonical.NewString(model.RoutingMode)},
		canonical.ObjectPair{Key: "reasoning_effort", Value: nullableString(model.ReasoningEffort)},
		canonical.ObjectPair{Key: "keys", Value: keys},
	)
}

// keyResponse 对应 management_api.py:1464 的 _key_response。
//
// 与 _raw_key_response 的关键差异：这里**带 base_url**，且 base_url 来自
// KeyConfig（可能为空串），不返回 capabilities。参照实现的两个 key 响应形状不同
// 是历史遗留，不能统一。
//
// allow_visitor 已随访客模式删除：上游 key 不再带「是否允许访客」的开关，受限凭据的
// 授权改成访问密钥自己的 providers/models 清单（见 config.AccessKeyConfig）。
func keyResponse(key config.KeyConfig) *canonical.Value {
	return objectOf(
		canonical.ObjectPair{Key: "name", Value: canonical.NewString(key.Name)},
		canonical.ObjectPair{Key: "base_url", Value: canonical.NewString(key.BaseURL)},
		canonical.ObjectPair{Key: "enabled", Value: canonical.NewBool(key.Enabled)},
		canonical.ObjectPair{Key: "api_key_fingerprint", Value: canonical.NewString(formatting.KeyFingerprint(key.APIKey))},
	)
}

// providerResponse 对应 management_api.py:1477 的 _provider_response。
//
// routeOrder 是原始配置里该 provider 的 routes 键顺序。
//
// 为什么需要它：Python 的 `dict(provider.routes)` 保留插入顺序，而 Go 的
// config.ProviderConfig.Routes 是 map，顺序已丢失。为了仍然输出与 Python 相同的
// 键序，调用方从原始配置里取出键顺序传进来。这是**已知的、被测试锁定的**补偿，
// 不是行为分歧——真实分歧是「Go 侧若不补偿就只能排序输出」。
func providerResponse(provider config.ProviderConfig, routeOrder []string) *canonical.Value {
	keys := canonical.NewArray()
	for _, key := range provider.Keys {
		keys.Arr = append(keys.Arr, objectOf(
			canonical.ObjectPair{Key: "name", Value: canonical.NewString(key.Name)},
			canonical.ObjectPair{Key: "enabled", Value: canonical.NewBool(key.Enabled)},
			canonical.ObjectPair{Key: "api_key_fingerprint", Value: canonical.NewString(formatting.KeyFingerprint(key.APIKey))},
			canonical.ObjectPair{Key: "capabilities", Value: capabilitiesField(key.Capabilities)},
		))
	}
	routes := canonical.NewObject()
	emitted := map[string]bool{}
	for _, mode := range routeOrder {
		path, present := provider.Routes[mode]
		if !present || emitted[mode] {
			continue
		}
		routes.SetKey(mode, canonical.NewString(path))
		emitted[mode] = true
	}
	// 兜底：原始配置里没有的键（理论上不会出现）按字典序补齐，保证输出确定。
	for _, mode := range sortedStringMapKeys(provider.Routes) {
		if emitted[mode] {
			continue
		}
		routes.SetKey(mode, canonical.NewString(provider.Routes[mode]))
	}
	return objectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString(provider.ID)},
		canonical.ObjectPair{Key: "base_url", Value: canonical.NewString(provider.BaseURL)},
		canonical.ObjectPair{Key: "keys", Value: keys},
		canonical.ObjectPair{Key: "routes", Value: routes},
	)
}

// capabilitiesField 对应 management_api.py:1556 的 _key_capabilities_field。
//
// 只接受 dict，其它类型（含 nil）一律 null。返回 dict() 的**浅拷贝**：Python 侧
// 拷贝后调用方即使改写响应对象也不会污染配置；Go 侧同一意图要求深拷贝，因为
// canonical.Value 是引用语义。
func capabilitiesField(capabilities *canonical.Value) *canonical.Value {
	if capabilities == nil || !capabilities.IsObject() {
		return canonical.NewNull()
	}
	return capabilities.Clone()
}

// rawKeyResponse 对应 management_api.py:1545 的 _raw_key_response。
//
// 注意两点与 keyResponse 的差异，都是参照实现的原样行为：
//   - **没有** base_url；
//   - enabled 用 bool(x.get(...)) 的默认值语义（缺失算 true）；
//   - fingerprint 取 str(key.get("api_key", ""))，非字符串会被 str() 渲染。
//
// allow_visitor 已随访客模式删除（见 keyResponse 的说明）。
func rawKeyResponse(name string, key *canonical.Value) *canonical.Value {
	enabled := canonical.NewBool(true)
	if raw, present := key.LookupOK("enabled"); present {
		enabled = canonical.NewBool(raw.Truthy())
	}
	apiKey := ""
	if raw, present := key.LookupOK("api_key"); present && raw != nil {
		apiKey = raw.PyStr()
	}
	return objectOf(
		canonical.ObjectPair{Key: "name", Value: canonical.NewString(name)},
		canonical.ObjectPair{Key: "enabled", Value: enabled},
		canonical.ObjectPair{Key: "api_key_fingerprint", Value: canonical.NewString(formatting.KeyFingerprint(apiKey))},
		canonical.ObjectPair{Key: "capabilities", Value: capabilitiesField(key.Lookup("capabilities"))},
	)
}

// rawProviderResponse 对应 management_api.py:1560 的 _raw_provider_response。
//
// 与 providerResponse 的差异：这里直接读原始 dict，所以 base_url 可能是 null、
// routes 缺失时算空对象、keys 顺序就是配置里的插入顺序。
func rawProviderResponse(name string, provider *canonical.Value) *canonical.Value {
	baseURL := canonical.NewNull()
	if raw, present := provider.LookupOK("base_url"); present {
		baseURL = raw
	}
	keys := canonical.NewArray()
	if rawKeys, present := provider.LookupOK("keys"); present && rawKeys != nil && rawKeys.IsObject() {
		for _, keyName := range rawKeys.Obj.Keys() {
			child, _ := rawKeys.Obj.Get(keyName)
			if child == nil || !child.IsObject() {
				continue
			}
			keys.Arr = append(keys.Arr, rawKeyResponse(keyName, child))
		}
	}
	routes := canonical.NewObject()
	if rawRoutes, present := provider.LookupOK("routes"); present && rawRoutes != nil && rawRoutes.IsObject() {
		for _, mode := range rawRoutes.Obj.Keys() {
			child, _ := rawRoutes.Obj.Get(mode)
			routes.SetKey(mode, child)
		}
	}
	return objectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString(name)},
		canonical.ObjectPair{Key: "base_url", Value: baseURL},
		canonical.ObjectPair{Key: "keys", Value: keys},
		canonical.ObjectPair{Key: "routes", Value: routes},
	)
}

// settingsResponse 对应 management_api.py:1288 的 _settings_response。
func settingsResponse(cfg *config.RouterConfig) *canonical.Value {
	fingerprint := canonical.NewNull()
	if cfg.LocalAPIKey != "" {
		fingerprint = canonical.NewString(formatting.KeyFingerprint(cfg.LocalAPIKey))
	}
	return objectOf(
		canonical.ObjectPair{Key: "host", Value: canonical.NewString(cfg.Host)},
		canonical.ObjectPair{Key: "port", Value: canonical.NewInt(pyIntLiteral(cfg.Port))},
		canonical.ObjectPair{Key: "request_timeout", Value: canonical.NewFloat(cfg.RequestTimeout)},
		canonical.ObjectPair{Key: "stream_first_byte_timeout", Value: canonical.NewFloat(cfg.StreamFirstByteTimeout)},
		canonical.ObjectPair{Key: "stream_idle_timeout", Value: canonical.NewFloat(cfg.StreamIdleTimeout)},
		canonical.ObjectPair{Key: "max_retries", Value: canonical.NewInt(pyIntLiteral(cfg.MaxRetries))},
		canonical.ObjectPair{Key: "local_auth_enabled", Value: canonical.NewBool(cfg.LocalAPIKey != "")},
		canonical.ObjectPair{Key: "local_api_key_fingerprint", Value: fingerprint},
	)
}

// probeResponse 对应 management_api.py:1260 的 _probe_response。
func probeResponse(record *probeRecord) *canonical.Value {
	results := canonical.NewArray()
	results.Arr = append(results.Arr, record.results...)
	return objectOf(
		canonical.ObjectPair{Key: "probe_id", Value: canonical.NewString(record.probeID)},
		canonical.ObjectPair{Key: "status", Value: canonical.NewString(record.status)},
		canonical.ObjectPair{Key: "provider", Value: canonical.NewString(record.provider)},
		canonical.ObjectPair{Key: "results", Value: results},
		canonical.ObjectPair{Key: "error", Value: nullableString(record.err)},
	)
}

// sortedStringMapKeys 返回 map 的键并按字典序排序。
func sortedStringMapKeys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// pyIntLiteral 把 int 渲染成 canonical 的整数字面量。
func pyIntLiteral(value int) string {
	return strconv.Itoa(value)
}
