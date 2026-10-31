package configops

import (
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// CreateModelKeyOptions 是 CreateModelKey 的可选参数。
//
// 对应 config_operations.py:744 的关键字参数；nil 一律表示 Python 的 None。
// Enabled 为 nil 表示 Python 默认值 true。UpdateUpstreamRoutes 为 true 时才会
// 写 upstream_routes（即使 UpstreamRoutes 为空）。
type CreateModelKeyOptions struct {
	BaseURL              *string
	Enabled              *bool
	UpstreamModel        *string
	UpstreamRoutes       *canonical.Value
	UpdateUpstreamRoutes bool
}

// CreateModelKey 把一个新的供应商 key 绑到模型上（v4 里 key 只是一个 target）。
//
// 对齐 config_operations.py:744。它做的事比名字多：按 base_url 找到（必要时
// 新建）供应商、建 provider key、再往模型 targets 里追加一条引用。步骤顺序是
// 可观察行为——例如「模型 key 已存在」的 409 发生在校验 base_url 之前。
func CreateModelKey(data *canonical.Value, modelID, keyName, apiKey string, options CreateModelKeyOptions) error {
	model, err := RequireModel(data, modelID)
	if err != nil {
		return err
	}
	name, err := nonEmptyString(keyName, "Key 名称")
	if err != nil {
		return err
	}
	// 解析失败被当成「没有可用模型」而不是错误：配置里可能还有别的问题，
	// 但这里只想复用它的 key 名归一规则。
	parsedModel := parsedModelByID(data, modelID)
	if parsedModel != nil {
		for _, key := range parsedModel.Keys {
			if key.Name == name || strings.HasSuffix(key.Name, "-"+name) {
				return opErrf(409, "模型 %s 的 key 已存在: %s", modelID, name)
			}
		}
	}
	targetURL, err := NormalizeBaseURL(canonical.NewString(baseURLWithDefault(data, options.BaseURL)))
	if err != nil {
		return err
	}
	providerID, err := ProviderIDForBaseURL(data, targetURL)
	if err != nil {
		return err
	}
	provider, err := RequireProvider(data, providerID)
	if err != nil {
		return err
	}
	keys, err := ProviderKeys(provider)
	if err != nil {
		return err
	}
	if keys.Obj.Has(name) {
		return opErrf(409, "供应商 %s 的 Key 已存在: %s", providerID, name)
	}
	enabled := true
	if options.Enabled != nil {
		enabled = *options.Enabled
	}
	if _, err := CreateProviderKey(data, providerID, name, apiKey, CreateProviderKeyOptions{
		Enabled: &enabled,
	}); err != nil {
		return err
	}
	upstream := modelID
	if options.UpstreamModel != nil && *options.UpstreamModel != "" {
		upstream = *options.UpstreamModel
	}
	upstream, err = nonEmptyString(upstream, "target.upstream_model")
	if err != nil {
		return err
	}
	targets, err := ModelTargets(model)
	if err != nil {
		return err
	}
	targets.Arr = append(targets.Arr, canonical.NewObjectOf(
		canonical.ObjectPair{Key: "provider", Value: canonical.NewString(providerID)},
		canonical.ObjectPair{Key: "key", Value: canonical.NewString(name)},
		canonical.ObjectPair{Key: "upstream_model", Value: canonical.NewString(upstream)},
	))
	if options.UpdateUpstreamRoutes {
		if err := SetUpstreamRoutesForBaseURL(data, targetURL, options.UpstreamRoutes); err != nil {
			return err
		}
	}
	return nil
}

// CreateModelWithKeys 建模型再逐条绑 key。
//
// 对齐 config_operations.py:769。keys 里的每一项是一个 JSON 对象，字段含义与
// CreateModelKey 的选项一致；这里刻意保留 `bool(key.get("enabled", True))` 的
// 语义——字段**缺失**才是 true，显式 null 会变成 false。
func CreateModelWithKeys(data *canonical.Value, modelID string, options CreateModelOptions, keys []*canonical.Value) error {
	if _, err := CreateModel(data, modelID, options); err != nil {
		return err
	}
	for _, key := range keys {
		if !key.IsObject() {
			// Python: key.get(...) 对非字典抛 AttributeError，这里失败关闭。
			return pyAttributeError(key, "get")
		}
		name := lookup(key, "name").StringValue()
		secret := lookup(key, "api_key").StringValue()

		var baseURL *string
		if value, ok := getOr(key, "base_url"); ok {
			text := value.StringValue()
			baseURL = &text
		}
		enabled := true
		if value, ok := getOr(key, "enabled"); ok {
			enabled = value.Truthy()
		}
		upstream := modelID
		if value := lookup(key, "upstream_model").StringValue(); value != "" {
			upstream = value
		}
		upstreamRoutes, _ := key.LookupOK("upstream_routes")
		if err := CreateModelKey(data, modelID, name, secret, CreateModelKeyOptions{
			BaseURL:              baseURL,
			Enabled:              &enabled,
			UpstreamModel:        &upstream,
			UpstreamRoutes:       upstreamRoutes,
			UpdateUpstreamRoutes: key.Obj.Has("upstream_routes"),
		}); err != nil {
			return err
		}
	}
	return nil
}

// UpdateModelKeyOptions 是 UpdateModelKey 的可选参数。
//
// BaseURL 与 UpdateBaseURL 的组合语义见 UpdateModelKey：BaseURL 为 nil 且
// UpdateBaseURL 为 true 时取 data["default_base_url"]，两者都缺省则不改 URL。
type UpdateModelKeyOptions struct {
	NewName              *string
	APIKey               *string
	BaseURL              *string
	UpdateBaseURL        bool
	Enabled              *bool
	UpstreamRoutes       *canonical.Value
	UpdateUpstreamRoutes bool
}

// UpdateModelKey 修改模型下某个 key（provider key 的本地视图），返回最终 key 名。
//
// 对齐 config_operations.py:854（Python 里叫 update_model_key_local，另有
// `update_model_key = update_model_key_local` 这个兼容别名；Go 侧只保留一个名字，
// 用的是别名那一个，因为它才是管理 API 与 TUI 实际调用的名字）。
//
// 三处必须照抄的行为：
//
//   - 当 provider key 被别的模型共用、或改了 base_url 时，会把这条 target 拆到
//     一个**克隆供应商**上（`_amkr_model_key_clone`），避免影响其它模型；
//   - new_name 为 nil 时目标名字用的是**调用方传入的 key_name**，而不是 provider
//     里那个去掉前缀的裸名——传 "p-k1" 会把 key 真的改名成 "p-k1"；
//   - 最后无条件跑一次 repair_model_references。
func UpdateModelKey(data *canonical.Value, modelID, keyName string, options UpdateModelKeyOptions) (string, error) {
	target, provider, rawName, err := locateModelTarget(data, modelID, keyName)
	if err != nil {
		return "", err
	}
	var cloneBaseURL *string
	if options.BaseURL != nil {
		cloneBaseURL = options.BaseURL
	} else if options.UpdateBaseURL {
		if value, ok := getOr(data, "default_base_url"); ok {
			text := value.StringValue()
			cloneBaseURL = &text
		}
	}
	target, provider, rawName, err = cloneProviderKeyTarget(data, modelID, target, provider, rawName, cloneBaseURL)
	if err != nil {
		return "", err
	}
	key, err := RequireKey(provider, rawName)
	if err != nil {
		return "", err
	}
	actual := keyName
	if options.NewName != nil {
		if actual, err = nonEmptyString(*options.NewName, "Key 名称"); err != nil {
			return "", err
		}
	}
	if actual != rawName {
		keys, err := ProviderKeys(provider)
		if err != nil {
			return "", err
		}
		if keys.Obj.Has(actual) {
			return "", opErrf(409, "Key 已存在: %s", actual)
		}
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
	if actual != rawName {
		keys, err := ProviderKeys(provider)
		if err != nil {
			return "", err
		}
		moved, _ := keys.LookupOK(rawName)
		keys.DeleteKey(rawName)
		keys.SetKey(actual, moved)
		target.SetKey("key", canonical.NewString(actual))
		FollowProviderKeyRename(data, lookup(target, "provider").StringValue(), rawName, actual)
	}
	if options.Enabled != nil && !*options.Enabled {
		if err := clearUnifiedKeysFromProvider(data, lookup(target, "provider").StringValue(), StringPtr(actual)); err != nil {
			return "", err
		}
	}
	if options.UpdateUpstreamRoutes {
		if err := SetUpstreamRoutesForBaseURL(data, lookup(provider, "base_url").StringValue(), options.UpstreamRoutes); err != nil {
			return "", err
		}
	}
	if err := RepairModelReferences(data); err != nil {
		return "", err
	}
	return actual, nil
}

// DeleteModelKey 删除模型下某个 key（本地视图）。
//
// 对齐 config_operations.py:919（delete_model_key 就是 delete_model_key_local 的
// 别名，Go 侧只保留一个名字）。
func DeleteModelKey(data *canonical.Value, modelID, keyName string) error {
	return deleteModelKeyLocal(data, modelID, keyName)
}

// deleteModelKeyLocal 删除模型下某个 key 并清理失去引用的 provider / provider key。
//
// 对齐 config_operations.py:889。清理规则很绕，但都是可观察行为：
// 该 provider key 不再被任何模型的任何 target 引用时删掉它；provider 空了就删
// provider；克隆供应商只要不再被引用就删（哪怕还有别的 key）。
func deleteModelKeyLocal(data *canonical.Value, modelID, keyName string) error {
	model, err := RequireModel(data, modelID)
	if err != nil {
		return err
	}
	target, provider, rawName, err := locateModelTarget(data, modelID, keyName)
	if err != nil {
		return err
	}
	targets, err := ModelTargets(model)
	if err != nil {
		return err
	}
	// Python 用 list.remove(target)（按 == 查找）。因为 locateModelTarget 返回的
	// 必然是第一个匹配项，而相等的字典必然同样匹配，所以按位置删除等价。
	index := -1
	for position, item := range targets.Arr {
		if pyEqualValues(item, target) {
			index = position
			break
		}
	}
	if index < 0 {
		return &PyError{TypeName: "ValueError", Message: "list.remove(x): x not in list"}
	}
	remaining := make([]*canonical.Value, 0, len(targets.Arr)-1)
	remaining = append(remaining, targets.Arr[:index]...)
	remaining = append(remaining, targets.Arr[index+1:]...)
	targets.Arr = remaining

	providerID := lookup(target, "provider").StringValue()
	stillReferenced, err := providerKeyStillReferenced(data, providerID, rawName)
	if err != nil {
		return err
	}
	if !stillReferenced {
		keys, err := ProviderKeys(provider)
		if err != nil {
			return err
		}
		keys.DeleteKey(rawName)
		isClone := lookup(provider, "_amkr_model_key_clone").Truthy()
		if keys.Obj.Len() == 0 || isClone {
			if isClone {
				providerReferenced, err := providerStillReferenced(data, providerID)
				if err != nil {
					return err
				}
				if !providerReferenced {
					all, err := Providers(data)
					if err != nil {
						return err
					}
					all.DeleteKey(providerID)
				}
			} else if keys.Obj.Len() == 0 {
				all, err := Providers(data)
				if err != nil {
					return err
				}
				all.DeleteKey(providerID)
			}
		}
	}
	modelTargets, err := ModelTargets(model)
	if err != nil {
		return err
	}
	if len(modelTargets.Arr) == 0 {
		allModels, err := Models(data)
		if err != nil {
			return err
		}
		allModels.DeleteKey(modelID)
	}
	return RepairModelReferences(data)
}

// locateModelTarget 找到「provider key 名字匹配」的那条 target。
//
// 对齐 config_operations.py:775。key_name 既可能是 provider 里的裸名，也可能是
// 解析后的带前缀名字 `{provider}-{key}`。注意每个 target 都会先 require_provider：
// 只要有一条 target 指向不存在的供应商，这里就先报 404（哪怕它并不匹配）。
func locateModelTarget(data *canonical.Value, modelID, keyName string) (*canonical.Value, *canonical.Value, string, error) {
	model, err := RequireModel(data, modelID)
	if err != nil {
		return nil, nil, "", err
	}
	targets, err := ModelTargets(model)
	if err != nil {
		return nil, nil, "", err
	}
	for _, target := range targets.Arr {
		providerID := lookup(target, "provider").StringValue()
		provider, err := RequireProvider(data, providerID)
		if err != nil {
			return nil, nil, "", err
		}
		rawName := lookup(target, "key").StringValue()
		if rawName == "" {
			continue
		}
		if keyName == rawName || keyName == providerID+"-"+rawName {
			return target, provider, rawName, nil
		}
	}
	return nil, nil, "", opErrf(404, "模型 %s 的 key 不存在: %s", modelID, keyName)
}

// providerKeyReferencedByOtherModel 报告该 provider key 是否被别的模型引用。
//
// 对齐 config_operations.py:794。
func providerKeyReferencedByOtherModel(data *canonical.Value, modelID, providerID, keyName string) (bool, error) {
	all, err := Models(data)
	if err != nil {
		return false, err
	}
	for _, pair := range objectItems(all) {
		if pair.Key == modelID || !pair.Value.IsObject() {
			continue
		}
		targets, err := ModelTargets(pair.Value)
		if err != nil {
			return false, err
		}
		for _, target := range targets.Arr {
			if pyEqualsString(lookup(target, "provider"), providerID) && pyEqualsString(lookup(target, "key"), keyName) {
				return true, nil
			}
		}
	}
	return false, nil
}

// providerKeyStillReferenced 报告是否有**任意**模型（含自己）还在引用该 provider key。
func providerKeyStillReferenced(data *canonical.Value, providerID, keyName string) (bool, error) {
	all, err := Models(data)
	if err != nil {
		return false, err
	}
	for _, pair := range objectItems(all) {
		if !pair.Value.IsObject() {
			continue
		}
		targets, err := ModelTargets(pair.Value)
		if err != nil {
			return false, err
		}
		for _, target := range targets.Arr {
			if pyEqualsString(lookup(target, "provider"), providerID) && pyEqualsString(lookup(target, "key"), keyName) {
				return true, nil
			}
		}
	}
	return false, nil
}

// providerStillReferenced 报告是否还有模型的任何 target 指向该供应商。
func providerStillReferenced(data *canonical.Value, providerID string) (bool, error) {
	all, err := Models(data)
	if err != nil {
		return false, err
	}
	for _, pair := range objectItems(all) {
		if !pair.Value.IsObject() {
			continue
		}
		targets, err := ModelTargets(pair.Value)
		if err != nil {
			return false, err
		}
		for _, target := range targets.Arr {
			if pyEqualsString(lookup(target, "provider"), providerID) {
				return true, nil
			}
		}
	}
	return false, nil
}

// cloneProviderKeyTarget 在必要时把这条 target 拆到一个克隆供应商上。
//
// 对齐 config_operations.py:821。两种情况需要克隆：目标 base_url 与来源不同，
// 或者该 provider key 被别的模型共用（拆开才能各改各的）。
//
// 返回 (target, 有效的 provider, 裸 key 名)。注意克隆时会先把它塞进 providers，
// 再去取要复制的 key——key 不存在时 Python 抛 KeyError，且那个半成品克隆已经
// 留在配置里了。这里保持同样的顺序（先插入、后取键）。
func cloneProviderKeyTarget(data *canonical.Value, modelID string, target, provider *canonical.Value, keyName string, baseURL *string) (*canonical.Value, *canonical.Value, string, error) {
	sourceURL, err := NormalizeBaseURL(lookup(provider, "base_url"))
	if err != nil {
		return nil, nil, "", err
	}
	targetURLInput := sourceURL
	if baseURL != nil && *baseURL != "" {
		targetURLInput = *baseURL
	}
	targetURL, err := NormalizeBaseURL(canonical.NewString(targetURLInput))
	if err != nil {
		return nil, nil, "", err
	}
	currentProviderID := lookup(target, "provider").StringValue()
	if targetURL == sourceURL {
		shared, err := providerKeyReferencedByOtherModel(data, modelID, currentProviderID, keyName)
		if err != nil {
			return nil, nil, "", err
		}
		if !shared {
			return target, provider, keyName, nil
		}
	}
	preferred := "provider"
	if targetURL != sourceURL {
		// Python: target_url.split("://", 1)[-1].replace("/", "-").replace(":", "-") or "provider"
		host := targetURL
		if _, rest, found := strings.Cut(targetURL, "://"); found {
			host = rest
		}
		host = strings.ReplaceAll(host, "/", "-")
		host = strings.ReplaceAll(host, ":", "-")
		if host != "" {
			preferred = host
		}
	} else {
		preferred = currentProviderID + "-" + modelID
	}
	providerID, err := uniqueProviderID(data, preferred)
	if err != nil {
		return nil, nil, "", err
	}
	clone := provider.Clone()
	clone.SetKey("base_url", canonical.NewString(targetURL))
	clone.SetKey("keys", canonical.NewObject())
	clone.DeleteKey("capabilities")
	clone.SetKey("_amkr_model_key_clone", canonical.NewBool(true))
	all, err := Providers(data)
	if err != nil {
		return nil, nil, "", err
	}
	all.SetKey(providerID, clone)

	sourceKeys, err := ProviderKeys(provider)
	if err != nil {
		return nil, nil, "", err
	}
	copied, ok := sourceKeys.LookupOK(keyName)
	if !ok {
		// Python 的 provider_keys(provider)[key_name] 在这里抛 KeyError，消息是
		// 键的 repr（带引号）。
		return nil, nil, "", pyKeyError(keyName)
	}
	cloneKeys, err := ProviderKeys(clone)
	if err != nil {
		return nil, nil, "", err
	}
	cloneKeys.SetKey(keyName, copied.Clone())
	if targetURL == sourceURL {
		// 只有在「同 URL、只为拆共用」时才把兄弟 key 一起复制过去。
		for _, pair := range objectItems(sourceKeys) {
			if pair.Key == keyName {
				continue
			}
			if !cloneKeys.Obj.Has(pair.Key) {
				cloneKeys.SetKey(pair.Key, pair.Value.Clone())
			}
		}
	}
	target.SetKey("provider", canonical.NewString(providerID))
	return target, clone, keyName, nil
}

// parsedModelByID 在 data 的解析结果里按 ID 找模型；解析失败或找不到返回 nil。
func parsedModelByID(data *canonical.Value, modelID string) *config.ModelConfig {
	parsed, err := config.FromDict(data)
	if err != nil {
		return nil
	}
	for index := range parsed.Models {
		if parsed.Models[index].ID == modelID {
			return &parsed.Models[index]
		}
	}
	return nil
}

// baseURLWithDefault 复刻 `base_url or data.get("default_base_url") or "https://api.openai.com"`。
func baseURLWithDefault(data *canonical.Value, baseURL *string) string {
	if baseURL != nil && *baseURL != "" {
		return *baseURL
	}
	if value := lookup(data, "default_base_url").StringValue(); value != "" {
		return value
	}
	return "https://api.openai.com"
}

// pyKeyError 复刻 `d[missing]` 抛出的 KeyError。
//
// Python 的 str(KeyError('k1')) 是 "'k1'"，即键的 repr（带引号）。
func pyKeyError(key string) *PyError {
	return &PyError{TypeName: "KeyError", Message: canonical.PyRepr(canonical.NewString(key))}
}
