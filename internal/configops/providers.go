package configops

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// CreateProvider 新建一个供应商并返回它。
//
// 对齐 config_operations.py:89。顺序值得注意：先校验 ID、再 setdefault
// providers、再查重、最后才校验 base_url。因此 base_url 非法时，data 里已经
// 多了一个空的 providers 对象——这是参照实现的可观察行为，刻意保留
// （由 TestCreateProviderFailureStillCreatesProvidersKey 钉住）。
func CreateProvider(data *canonical.Value, providerID, baseURL string) (*canonical.Value, error) {
	id, err := nonEmptyString(providerID, "供应商 ID")
	if err != nil {
		return nil, err
	}
	all, err := Providers(data)
	if err != nil {
		return nil, err
	}
	if all.Obj.Has(id) {
		return nil, opErrf(409, "供应商已存在: %s", id)
	}
	normalized, err := NormalizeBaseURL(canonical.NewString(baseURL))
	if err != nil {
		return nil, err
	}
	provider := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "base_url", Value: canonical.NewString(normalized)},
		canonical.ObjectPair{Key: "keys", Value: canonical.NewObject()},
	)
	all.SetKey(id, provider)
	return provider, nil
}

// UpdateProviderOptions 是 UpdateProvider 的可选参数。
//
// nil 表示 Python 的 None：NewID 为 nil 时「不改 ID」，而指向空串的 NewID 会走
// _non_empty 报 422。Routes 为 nil 等价于 Python 的 routes=None（规范化成空映射）。
type UpdateProviderOptions struct {
	NewID        *string
	BaseURL      *string
	Routes       *canonical.Value
	UpdateRoutes bool
}

// UpdateProvider 修改供应商，返回最终 ID（改名时是新 ID）。
//
// 对齐 config_operations.py:99。三个容易踩的点：
//
//   - 改名用 `all[new] = all.pop(old)`，于是供应商在 providers 里移到末尾；
//   - 改 base_url 时要搬 data["upstream_routes"] 里的键，只搬顶层键；
//   - provider["base_url"] 缺失/为空时 normalize_upstream_base_url 抛裸
//     ValueError（不是 ConfigOperationError），对外是 500 而不是 400。
func UpdateProvider(data *canonical.Value, providerID string, options UpdateProviderOptions) (string, error) {
	all, err := Providers(data)
	if err != nil {
		return "", err
	}
	provider, err := RequireProvider(data, providerID)
	if err != nil {
		return "", err
	}
	targetID := providerID
	if options.NewID != nil {
		if targetID, err = nonEmptyString(*options.NewID, "供应商 ID"); err != nil {
			return "", err
		}
	}
	if targetID != providerID && all.Obj.Has(targetID) {
		return "", opErrf(409, "供应商已存在: %s", targetID)
	}
	if options.BaseURL != nil {
		oldURL, err := config.NormalizeUpstreamBaseURL(lookup(provider, "base_url"))
		if err != nil {
			return "", err
		}
		newURL, err := NormalizeBaseURL(canonical.NewString(*options.BaseURL))
		if err != nil {
			return "", err
		}
		provider.SetKey("base_url", canonical.NewString(newURL))
		if routeMap := lookup(data, "upstream_routes"); routeMap.IsObject() && oldURL != newURL && routeMap.Obj.Has(oldURL) {
			if !routeMap.Obj.Has(newURL) {
				moved, _ := routeMap.LookupOK(oldURL)
				routeMap.DeleteKey(oldURL)
				routeMap.SetKey(newURL, moved)
			} else {
				routeMap.DeleteKey(oldURL)
			}
		}
	}
	if options.UpdateRoutes {
		normalized, err := normalizeUpstreamRoutesOrdered(options.Routes)
		if err != nil {
			return "", wrapRoutesValueError(err)
		}
		if normalized.Obj.Len() > 0 {
			provider.SetKey("routes", normalized)
		} else {
			provider.DeleteKey("routes")
		}
	}
	if targetID != providerID {
		moved, _ := all.LookupOK(providerID)
		all.DeleteKey(providerID)
		all.SetKey(targetID, moved)
		// 与模型改名同一件事：改名要**跟随**到引用上。模型 target 里的 provider 在下面
		// 逐条改写，访问密钥的供应商清单在这里跟着改。
		FollowProviderRename(data, providerID, targetID)
		allModels, err := Models(data)
		if err != nil {
			return "", err
		}
		for _, pair := range objectItems(allModels) {
			model := pair.Value
			if !model.IsObject() {
				continue
			}
			targets, err := ModelTargets(model)
			if err != nil {
				return "", err
			}
			for _, target := range targets.Arr {
				if pyEqualsString(lookup(target, "provider"), providerID) {
					target.SetKey("provider", canonical.NewString(targetID))
				}
			}
		}
	}
	return targetID, nil
}

// CreateProviderKeyOptions 是 CreateProviderKey 的可选参数。
//
// Enabled 为 nil 表示 Python 默认值 true（不能用 Go 的零值 false 代替，否则
// 默认行为会反过来）。
type CreateProviderKeyOptions struct {
	Enabled *bool
}

// CreateProviderKey 在供应商下新建一个 key 并返回它。
//
// 对齐 config_operations.py:142。Python 侧的 pool_name 参数刻意不移植：v4 没有
// pool 概念，参照实现也只是为了兼容旧调用方而接受它并忽略。
func CreateProviderKey(data *canonical.Value, providerID, keyName, apiKey string, options CreateProviderKeyOptions) (*canonical.Value, error) {
	provider, err := RequireProvider(data, providerID)
	if err != nil {
		return nil, err
	}
	name, err := nonEmptyString(keyName, "Key 名称")
	if err != nil {
		return nil, err
	}
	keys, err := ProviderKeys(provider)
	if err != nil {
		return nil, err
	}
	if keys.Obj.Has(name) {
		return nil, opErrf(409, "Key 已存在: %s", name)
	}
	secret, err := nonEmptyString(apiKey, "API key")
	if err != nil {
		return nil, err
	}
	enabled := true
	if options.Enabled != nil {
		enabled = *options.Enabled
	}
	key := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "api_key", Value: canonical.NewString(secret)},
		canonical.ObjectPair{Key: "enabled", Value: canonical.NewBool(enabled)},
	)
	keys.SetKey(name, key)
	return key, nil
}

// UpdateProviderKeyOptions 是 UpdateProviderKey 的可选参数。
type UpdateProviderKeyOptions struct {
	NewName *string
	APIKey  *string
	Enabled *bool
}

// UpdateProviderKey 修改供应商 key，返回最终名字（改名时是新名字）。
//
// 对齐 config_operations.py:267。两个副作用必须保留：
//
//   - 换 api_key 时删掉 capabilities（探测缓存属于旧凭据）；
//   - enabled 显式置 false 时清掉 unified_model 里指向它的 key 选择。
func UpdateProviderKey(data *canonical.Value, providerID, keyName string, options UpdateProviderKeyOptions) (string, error) {
	provider, err := RequireProvider(data, providerID)
	if err != nil {
		return "", err
	}
	keys, err := ProviderKeys(provider)
	if err != nil {
		return "", err
	}
	key, err := RequireKey(provider, keyName)
	if err != nil {
		return "", err
	}
	targetName := keyName
	if options.NewName != nil {
		if targetName, err = nonEmptyString(*options.NewName, "Key 名称"); err != nil {
			return "", err
		}
	}
	if targetName != keyName && keys.Obj.Has(targetName) {
		return "", opErrf(409, "Key 已存在: %s", targetName)
	}
	if options.APIKey != nil {
		secret, err := nonEmptyString(*options.APIKey, "API key")
		if err != nil {
			return "", err
		}
		key.SetKey("api_key", canonical.NewString(secret))
		key.DeleteKey("capabilities")
	}
	if options.Enabled != nil {
		key.SetKey("enabled", canonical.NewBool(*options.Enabled))
	}
	if options.Enabled != nil && !*options.Enabled {
		if err := clearUnifiedKeysFromProvider(data, providerID, StringPtr(keyName)); err != nil {
			return "", err
		}
	}
	if targetName != keyName {
		if err := renameUnifiedKey(data, providerID, keyName, targetName); err != nil {
			return "", err
		}
		moved, _ := keys.LookupOK(keyName)
		keys.DeleteKey(keyName)
		keys.SetKey(targetName, moved)
		allModels, err := Models(data)
		if err != nil {
			return "", err
		}
		for _, pair := range objectItems(allModels) {
			model := pair.Value
			if !model.IsObject() {
				continue
			}
			targets, err := ModelTargets(model)
			if err != nil {
				return "", err
			}
			for _, target := range targets.Arr {
				if pyEqualsString(lookup(target, "provider"), providerID) && pyEqualsString(lookup(target, "key"), keyName) {
					target.SetKey("key", canonical.NewString(targetName))
				}
			}
		}
	}
	return targetName, nil
}

// DeleteProviderKey 删除供应商 key，返回因为失去全部 target 而被删掉的模型 ID
// （升序）。
//
// 对齐 config_operations.py:474。Python 返回 set[str]；Go 侧排序后返回，避免
// 调用方依赖不可复现的集合顺序。
func DeleteProviderKey(data *canonical.Value, providerID, keyName string) ([]string, error) {
	provider, err := RequireProvider(data, providerID)
	if err != nil {
		return nil, err
	}
	keys, err := ProviderKeys(provider)
	if err != nil {
		return nil, err
	}
	if _, err := RequireKey(provider, keyName); err != nil {
		return nil, err
	}
	if err := clearUnifiedKeysFromProvider(data, providerID, StringPtr(keyName)); err != nil {
		return nil, err
	}
	keys.DeleteKey(keyName)

	removed := map[string]bool{}
	allModels, err := Models(data)
	if err != nil {
		return nil, err
	}
	for _, modelID := range objectKeys(allModels) {
		model := lookup(allModels, modelID)
		if !model.IsObject() {
			continue
		}
		targets, err := ModelTargets(model)
		if err != nil {
			return nil, err
		}
		targets.Arr = filterTargets(targets.Arr, func(target *canonical.Value) bool {
			return !(pyEqualsString(lookup(target, "provider"), providerID) && pyEqualsString(lookup(target, "key"), keyName))
		})
		if len(targets.Arr) == 0 {
			allModels.DeleteKey(modelID)
			removed[modelID] = true
		}
	}
	if keys.Obj.Len() == 0 {
		all, err := Providers(data)
		if err != nil {
			return nil, err
		}
		all.DeleteKey(providerID)
	}
	if err := RepairModelReferences(data); err != nil {
		return nil, err
	}
	return sortedSet(removed), nil
}

// DeleteProvider 删除供应商，返回因为失去全部 target 而被删掉的模型 ID（升序）。
//
// 对齐 config_operations.py:501。
func DeleteProvider(data *canonical.Value, providerID string) ([]string, error) {
	if _, err := RequireProvider(data, providerID); err != nil {
		return nil, err
	}
	if err := clearUnifiedKeysFromProvider(data, providerID, nil); err != nil {
		return nil, err
	}
	all, err := Providers(data)
	if err != nil {
		return nil, err
	}
	all.DeleteKey(providerID)

	removed := map[string]bool{}
	allModels, err := Models(data)
	if err != nil {
		return nil, err
	}
	for _, modelID := range objectKeys(allModels) {
		model := lookup(allModels, modelID)
		if !model.IsObject() {
			continue
		}
		targets, err := ModelTargets(model)
		if err != nil {
			return nil, err
		}
		targets.Arr = filterTargets(targets.Arr, func(target *canonical.Value) bool {
			return !pyEqualsString(lookup(target, "provider"), providerID)
		})
		if len(targets.Arr) == 0 {
			allModels.DeleteKey(modelID)
			removed[modelID] = true
		}
	}
	if err := RepairModelReferences(data); err != nil {
		return nil, err
	}
	return sortedSet(removed), nil
}

// ProviderIDForBaseURL 返回（必要时创建）与 base_url 对应的供应商 ID。
//
// 对齐 config_operations.py:700。已有供应商按 rstrip("/") 后的 base_url 匹配；
// 否则用 URL 的主机与路径拼一个 ID，冲突时追加 -2、-3……
func ProviderIDForBaseURL(data *canonical.Value, baseURL string) (string, error) {
	normalized, err := NormalizeBaseURL(canonical.NewString(baseURL))
	if err != nil {
		return "", err
	}
	all, err := Providers(data)
	if err != nil {
		return "", err
	}
	for _, pair := range objectItems(all) {
		provider := pair.Value
		if !provider.IsObject() {
			continue
		}
		if strings.TrimRight(lookup(provider, "base_url").StringValue(), "/") == normalized {
			return pair.Key, nil
		}
	}
	// Python: normalized.split("://", 1)[-1].strip("/").replace("/", "-").replace(":", "-") or "default"
	// normalize_base_url 已保证存在 "://"，Cut 的 found 分支必然命中。
	host := normalized
	if _, rest, found := strings.Cut(normalized, "://"); found {
		host = rest
	}
	candidate := strings.Trim(host, "/")
	candidate = strings.ReplaceAll(candidate, "/", "-")
	candidate = strings.ReplaceAll(candidate, ":", "-")
	if candidate == "" {
		candidate = "default"
	}
	id, err := uniqueProviderID(data, candidate)
	if err != nil {
		return "", err
	}
	if _, err := CreateProvider(data, id, normalized); err != nil {
		return "", err
	}
	return id, nil
}

// SetUpstreamRoutesForBaseURL 设置（或清除）某上游 URL 的路由映射。
//
// 对齐 config_operations.py:715。空映射会连带删掉 data["upstream_routes"] 本身；
// data["upstream_routes"] 不是对象时会被替换成一个新对象（而不是报错）。
func SetUpstreamRoutesForBaseURL(data *canonical.Value, baseURL string, routes *canonical.Value) error {
	normalizedURL, err := config.NormalizeUpstreamBaseURL(canonical.NewString(baseURL))
	if err != nil {
		return wrapRoutesValueError(err)
	}
	normalizedRoutes, err := normalizeUpstreamRoutesOrdered(routes)
	if err != nil {
		return wrapRoutesValueError(err)
	}
	routeMap := lookup(data, "upstream_routes")
	if !routeMap.IsObject() {
		routeMap = canonical.NewObject()
	}
	if normalizedRoutes.Obj.Len() > 0 {
		routeMap.SetKey(normalizedURL, normalizedRoutes)
		data.SetKey("upstream_routes", routeMap)
		return nil
	}
	routeMap.DeleteKey(normalizedURL)
	if routeMap.Obj.Len() > 0 {
		data.SetKey("upstream_routes", routeMap)
	} else {
		data.DeleteKey("upstream_routes")
	}
	return nil
}

// uniqueProviderID 返回首个未被占用的 `preferred`、`preferred-2`、`preferred-3`……
//
// 对齐 config_operations.py:735。
func uniqueProviderID(data *canonical.Value, preferred string) (string, error) {
	all, err := Providers(data)
	if err != nil {
		return "", err
	}
	candidate := preferred
	for suffix := 2; all.Obj.Has(candidate); suffix++ {
		candidate = preferred + "-" + strconv.Itoa(suffix)
	}
	return candidate, nil
}

// normalizeUpstreamRoutesOrdered 规范化 {模式: 路径} 映射并**保留键顺序**。
//
// config.NormalizeUpstreamRoutes 返回 Go map（顺序丢失），而 provider["routes"]
// 会原样落盘，键顺序属于字节级兼容的一部分，因此这里按 Python 的插入顺序重写
// 一遍。语义与它逐条对齐（config_operations.py:125）：
//
//   - nil / null → 空映射；
//   - 非对象 → ValueError("upstream_routes 必须是对象")；
//   - 值为 null 或空白字符串的条目静默跳过（数字 0、false 会被当成路径去校验）；
//   - 规范化后的模式名重复时，后者覆盖前者且**保留首次出现的位置**。
func normalizeUpstreamRoutesOrdered(raw *canonical.Value) (*canonical.Value, error) {
	if raw == nil || raw.IsNull() {
		return canonical.NewObject(), nil
	}
	if !raw.IsObject() {
		return nil, &config.ConfigError{Message: "upstream_routes 必须是对象"}
	}
	result := canonical.NewObject()
	for _, rawMode := range raw.Obj.Keys() {
		rawRoute := lookup(raw, rawMode)
		if rawRoute == nil || rawRoute.IsNull() || strings.TrimSpace(rawRoute.PyStr()) == "" {
			continue
		}
		mode, err := config.NormalizeUpstreamRouteMode(canonical.NewString(rawMode))
		if err != nil {
			return nil, err
		}
		path, err := config.NormalizeUpstreamRoutePath(mode, rawRoute)
		if err != nil {
			return nil, err
		}
		result.SetKey(mode, canonical.NewString(path))
	}
	return result, nil
}

// wrapRoutesValueError 复刻 `except ValueError as exc: raise ConfigOperationError(str(exc), 422)`。
//
// 只把 config 里的 ConfigError（对应 Python ValueError）折叠成 422；其余
// （InternalError，对应 TypeError/AttributeError）原样冒泡，与参照实现一致。
func wrapRoutesValueError(err error) error {
	var configErr *config.ConfigError
	if errors.As(err, &configErr) {
		return opErr(422, configErr.Message)
	}
	return err
}

// pyEqualsString 复刻 `value == want`（want 是 Python str）。
//
// 数字 5 与字符串 "5" 在 Python 里不相等，Go 侧必须同样按类型比较。
func pyEqualsString(value *canonical.Value, want string) bool {
	if value == nil || value.Kind != canonical.KindString {
		return false
	}
	return value.Str == want
}

// pyEqualValues 复刻两个 JSON 值之间的 Python `==`。
//
// 只用于 repair_unified_model 里 `primary.get("model") == fallback.get("model")`
// 这一处，但那处正好需要 Python 的宽松语义：两边都缺失或都是 null 也算相等，
// 而 1 == 1.0、True == 1 在 Python 里同样成立。数字比较走 float 转换——配置里
// 的模型名不可能是数字，这里只求「与 Python 同判定」，不追求超大整数精度。
func pyEqualValues(a, b *canonical.Value) bool {
	aIsNull := a == nil || a.Kind == canonical.KindNull
	bIsNull := b == nil || b.Kind == canonical.KindNull
	if aIsNull || bIsNull {
		return aIsNull && bIsNull
	}
	aNumber, aIsNumber := pythonNumber(a)
	bNumber, bIsNumber := pythonNumber(b)
	if aIsNumber || bIsNumber {
		return aIsNumber && bIsNumber && aNumber == bNumber
	}
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case canonical.KindString:
		return a.Str == b.Str
	case canonical.KindArray:
		if len(a.Arr) != len(b.Arr) {
			return false
		}
		for index := range a.Arr {
			if !pyEqualValues(a.Arr[index], b.Arr[index]) {
				return false
			}
		}
		return true
	case canonical.KindObject:
		if a.Obj.Len() != b.Obj.Len() {
			return false
		}
		for _, pair := range objectItems(a) {
			other, ok := b.Obj.Get(pair.Key)
			if !ok || !pyEqualValues(pair.Value, other) {
				return false
			}
		}
		return true
	}
	return false
}

// pythonNumber 把数字与布尔折成 Python 意义上的数值（bool 是 int 的子类）。
func pythonNumber(value *canonical.Value) (float64, bool) {
	switch value.Kind {
	case canonical.KindBool:
		if value.Bool {
			return 1, true
		}
		return 0, true
	case canonical.KindNumber:
		number, err := canonical.ToFloat(value)
		if err != nil {
			return 0, false
		}
		return number, true
	}
	return 0, false
}

// pyStrOf 复刻 `str(value)`：缺失键得到 "None"，null 也得到 "None"。
func pyStrOf(value *canonical.Value) string { return value.PyStr() }

// filterTargets 复刻 `targets[:] = [t for t in targets if keep(t)]`。
func filterTargets(targets []*canonical.Value, keep func(*canonical.Value) bool) []*canonical.Value {
	kept := make([]*canonical.Value, 0, len(targets))
	for _, target := range targets {
		if keep(target) {
			kept = append(kept, target)
		}
	}
	return kept
}

// sortedSet 把 Go 集合转成升序切片（对应 Python set 的可复现表示）。
func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for item := range set {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}
