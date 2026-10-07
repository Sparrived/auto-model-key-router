package configops

import (
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// StringPtr 返回字符串指针，便于填写可选的 *string 参数（nil 表示 Python None）。
func StringPtr(value string) *string { return &value }

// BoolPtr 返回布尔指针，便于填写可选的 *bool 参数（nil 表示使用 Python 默认值）。
func BoolPtr(value bool) *bool { return &value }

// Providers 返回 data["providers"]，缺失时先写入一个空对象。
//
// 对齐 config_operations.py:25 的 `data.setdefault("providers", {})`：注意
// setdefault 只在**键缺失**时写入；键存在但值为 null 时保持原样，随后因为不是
// 对象而报错。Go 侧同样先查存在性再做类型判断，顺序不能反。
func Providers(data *canonical.Value) (*canonical.Value, error) {
	if !data.IsObject() {
		return nil, pyAttributeError(data, "setdefault")
	}
	value, ok := data.LookupOK("providers")
	if !ok {
		value = canonical.NewObject()
		data.SetKey("providers", value)
	}
	if !value.IsObject() {
		return nil, opErr(400, "providers 必须是对象")
	}
	return value, nil
}

// Models 返回 data["models"]，缺失时先写入一个空对象。
//
// 对齐 config_operations.py:32。语义与 Providers 完全相同。
func Models(data *canonical.Value) (*canonical.Value, error) {
	if !data.IsObject() {
		return nil, pyAttributeError(data, "setdefault")
	}
	value, ok := data.LookupOK("models")
	if !ok {
		value = canonical.NewObject()
		data.SetKey("models", value)
	}
	if !value.IsObject() {
		return nil, opErr(400, "models 必须是对象")
	}
	return value, nil
}

// ProviderKeys 返回 provider["keys"]，缺失时先写入一个空对象。
//
// 对齐 config_operations.py:39。
func ProviderKeys(provider *canonical.Value) (*canonical.Value, error) {
	if !provider.IsObject() {
		return nil, pyAttributeError(provider, "setdefault")
	}
	value, ok := provider.LookupOK("keys")
	if !ok {
		value = canonical.NewObject()
		provider.SetKey("keys", value)
	}
	if !value.IsObject() {
		return nil, opErr(400, "provider.keys 必须是对象")
	}
	return value, nil
}

// ModelTargets 返回 model["targets"]，缺失时先写入一个空数组。
//
// 对齐 config_operations.py:46。返回的是同一个数组对象，调用方可以直接改
// 它的 Arr 来原地增删（Python 里的 `targets[:] = [...]`）。
func ModelTargets(model *canonical.Value) (*canonical.Value, error) {
	if !model.IsObject() {
		return nil, pyAttributeError(model, "setdefault")
	}
	value, ok := model.LookupOK("targets")
	if !ok {
		value = canonical.NewArray()
		model.SetKey("targets", value)
	}
	if !value.IsArray() {
		return nil, opErr(400, "model.targets 必须是数组")
	}
	return value, nil
}

// RequireProvider 返回指定 provider，不存在时报 404。
//
// 对齐 config_operations.py:53：非对象（含 null）都算「不存在」。
func RequireProvider(data *canonical.Value, providerID string) (*canonical.Value, error) {
	all, err := Providers(data)
	if err != nil {
		return nil, err
	}
	provider, _ := all.LookupOK(providerID)
	if !provider.IsObject() {
		return nil, opErrf(404, "供应商不存在: %s", providerID)
	}
	return provider, nil
}

// RequireKey 返回 provider 下指定名字的 key，不存在时报 404。
//
// 对齐 config_operations.py:60。
func RequireKey(provider *canonical.Value, keyName string) (*canonical.Value, error) {
	keys, err := ProviderKeys(provider)
	if err != nil {
		return nil, err
	}
	key, _ := keys.LookupOK(keyName)
	if !key.IsObject() {
		return nil, opErrf(404, "Key 不存在: %s", keyName)
	}
	return key, nil
}

// RequireModel 返回指定模型，不存在时报 404。
//
// 对齐 config_operations.py:67。
func RequireModel(data *canonical.Value, modelID string) (*canonical.Value, error) {
	all, err := Models(data)
	if err != nil {
		return nil, err
	}
	model, _ := all.LookupOK(modelID)
	if !model.IsObject() {
		return nil, opErrf(404, "模型不存在: %s", modelID)
	}
	return model, nil
}

// nonEmpty 复刻 _non_empty（config_operations.py:74）：`str(value or "").strip()`，
// 空串时报 422。
//
// 用 canonical 的 StringValue 而不是 AsString：Python 的 `value or ""` 会把 0、
// false、空容器都替换成空串，StringValue 正是这个语义。
func nonEmpty(value *canonical.Value, label string) (string, error) {
	result := strings.TrimSpace(value.StringValue())
	if result == "" {
		return "", opErr(422, label+"不能为空")
	}
	return result, nil
}

// nonEmptyString 是 nonEmpty 的 Go 字符串版本：参数已经是 string，只需去空白。
func nonEmptyString(value, label string) (string, error) {
	result := strings.TrimSpace(value)
	if result == "" {
		return "", opErr(422, label+"不能为空")
	}
	return result, nil
}

// NormalizeBaseURL 规范化上游 base_url。
//
// 对齐 config_operations.py:81：先按 _non_empty 去空白取非空，再 rstrip("/")，
// 最后要求 scheme 是 http/https 且 netloc 非空。
//
// 值得注意：urlparse 会把 scheme 小写化（"HTTP://X" 合法），并且容忍
// `http://host:bad` 这种 Go 的 net/url 会拒绝的 netloc——为了逐字兼容参照实现，
// 这里手写 scheme/netloc 提取而不是调用 net/url。
func NormalizeBaseURL(value *canonical.Value) (string, error) {
	result, err := nonEmpty(value, "base_url")
	if err != nil {
		return "", err
	}
	result = strings.TrimRight(result, "/")
	scheme, netloc, err := pyURLSplit(result)
	if err != nil {
		return "", err
	}
	if (scheme != "http" && scheme != "https") || netloc == "" {
		return "", opErr(422, "base_url 必须是 http 或 https URL")
	}
	return result, nil
}

// pyURLSplit 复刻 urllib.parse.urlsplit 中本包用到的那部分逻辑：C0 控制字符的
// 首尾剥离、\t\r\n 的移除、scheme 的提取与校验、以及 "//" 之后到第一个
// '/', '?', '#' 之间的 netloc。
//
// 唯一额外实现的是 IPv6 括号校验：CPython 对 `http://[::1` 抛
// ValueError("Invalid IPv6 URL")，这里保持同一异常类型与文本。
func pyURLSplit(raw string) (string, string, error) {
	url := strings.TrimFunc(raw, func(r rune) bool { return r < 0x20 })
	url = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(url)

	rest := url
	scheme := ""
	// CPython: `if i > 0 and url[0].isascii() and url[0].isalpha()`，然后要求
	// 冒号前的每个字符都在 scheme_chars 里。
	if i := strings.Index(url, ":"); i > 0 && isASCIIAlpha(url[0]) {
		valid := true
		for index := 0; index < i; index++ {
			if !isSchemeChar(url[index]) {
				valid = false
				break
			}
		}
		if valid {
			scheme = strings.ToLower(url[:i])
			rest = url[i+1:]
		}
	}

	netloc := ""
	if strings.HasPrefix(rest, "//") {
		body := rest[2:]
		if end := strings.IndexAny(body, "/?#"); end >= 0 {
			netloc = body[:end]
		} else {
			netloc = body
		}
		if strings.Contains(netloc, "[") != strings.Contains(netloc, "]") {
			return "", "", &PyError{TypeName: "ValueError", Message: "Invalid IPv6 URL"}
		}
	}
	return scheme, netloc, nil
}

func isASCIIAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isSchemeChar 对应 CPython 的 scheme_chars（字母、数字、'+'、'-'、'.'）。
func isSchemeChar(c byte) bool {
	return isASCIIAlpha(c) || (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.'
}

// UNIFIEDPlanNames 是 unified_model 的全部计划名。
//
// 顺序即 repair_unified_model 的处理顺序，不能改（default 必须最前，见
// config.UnifiedPlanNames 的说明）。这里**从 config 包取**而不是自己再写一份：
// 计划名同时驱动解析、校验、切换、修复与展示，两份清单漂移的后果是「某个族的
// unified 配置被静默丢掉」——升级时会丢用户数据，比崩溃更难查。
var UNIFIEDPlanNames = config.UnifiedPlanNames

// UnifiedTargets 是 unified_model 的全部「计划.角色」目标名。
//
// 移植 config_operations.py:167 的 UNIFIED_TARGETS。Python 侧是模块级 tuple，
// Go 没有常量切片，因此是包级变量；调用方不得修改。
var UnifiedTargets = func() []string {
	targets := make([]string, 0, len(UNIFIEDPlanNames)*2)
	for _, planName := range UNIFIEDPlanNames {
		for _, role := range []string{"primary", "fallback"} {
			targets = append(targets, planName+"."+role)
		}
	}
	return targets
}()

// objectItems 按插入顺序返回对象的 (键, 值) 对。
//
// Python 的 `for k, v in d.items()` 是插入顺序（覆盖已有键不改变位置），
// Go 的 Object 保持了同样语义，这里只是把它包成便于 for range 的形式。
func objectItems(value *canonical.Value) []canonical.ObjectPair {
	if !value.IsObject() || value.Obj == nil {
		return nil
	}
	keys := value.Obj.Keys()
	pairs := make([]canonical.ObjectPair, 0, len(keys))
	for _, key := range keys {
		child, _ := value.Obj.Get(key)
		pairs = append(pairs, canonical.ObjectPair{Key: key, Value: child})
	}
	return pairs
}

// objectKeys 按插入顺序返回对象的键；非对象返回 nil。
func objectKeys(value *canonical.Value) []string {
	if !value.IsObject() || value.Obj == nil {
		return nil
	}
	return value.Obj.Keys()
}

// iterateOrDefault 复刻 `for item in container.get(key, [])`。
//
// 两个细节都要保留：键缺失时迭代空列表；键存在但值为 null 时 Python 抛
// TypeError（`for x in None`），不是「当作空」。canonical.PyIterate 负责后者。
func iterateOrDefault(container *canonical.Value, key string) ([]string, error) {
	if container == nil || !container.IsObject() {
		return nil, pyAttributeError(container, "get")
	}
	value, ok := container.LookupOK(key)
	if !ok {
		return nil, nil
	}
	return canonical.PyIterate(value)
}

// cloneOf 复刻 copy.deepcopy：nil 与 null 都返回 null 值。
func cloneOf(value *canonical.Value) *canonical.Value {
	if value == nil {
		return canonical.NewNull()
	}
	return value.Clone()
}

// pyContains 复刻 Python 的 `needle in container`（needle 是 str）。
//
// 与 PyIterate 的 for 循环语义不同，**报错文本也不一样**：
//
//	"x" in None   → TypeError: argument of type 'NoneType' is not iterable
//	for x in None → TypeError: 'NoneType' object is not iterable
//
// 字符串容器走子串匹配（Python 的 `"a" in "abc"` 为真），字典容器查键，
// 数组按元素 ==（元素是 str 才可能相等），数字/布尔直接报 TypeError。
func pyContains(container *canonical.Value, needle string) (bool, error) {
	if container == nil || container.Kind == canonical.KindNull {
		return false, &PyError{
			TypeName: "TypeError",
			Message:  "argument of type 'NoneType' is not iterable",
		}
	}
	switch container.Kind {
	case canonical.KindString:
		return strings.Contains(container.Str, needle), nil
	case canonical.KindArray:
		for _, item := range container.Arr {
			if pyEqualsString(item, needle) {
				return true, nil
			}
		}
		return false, nil
	case canonical.KindObject:
		return container.Obj.Has(needle), nil
	}
	return false, &PyError{
		TypeName: "TypeError",
		Message:  "argument of type '" + canonical.PyTypeName(container) + "' is not iterable",
	}
}

// iterateOrEmpty 复刻 `for item in (value or [])`。
//
// 与 iterateOrDefault 的差别在 `or []`：null / 空串 / 0 / false 都先被换成空列表，
// 因此不会抛 TypeError；真值的字符串仍按字符展开，真值的数字仍报 TypeError。
func iterateOrEmpty(container *canonical.Value, key string) ([]string, error) {
	if container == nil || !container.IsObject() {
		return nil, pyAttributeError(container, "get")
	}
	value, ok := container.LookupOK(key)
	if !ok || !value.Truthy() {
		return nil, nil
	}
	return canonical.PyIterate(value)
}

// getOr 复刻 `container.get(key, fallback)` 的存在性语义：返回 (值, 是否存在)。
func getOr(container *canonical.Value, key string) (*canonical.Value, bool) {
	if container == nil || !container.IsObject() {
		return nil, false
	}
	return container.LookupOK(key)
}

// lookup 复刻 `container.get(key)`：键缺失与 container 非对象都得到 nil。
func lookup(container *canonical.Value, key string) *canonical.Value {
	if container == nil || !container.IsObject() {
		return nil
	}
	value, _ := container.LookupOK(key)
	return value
}
