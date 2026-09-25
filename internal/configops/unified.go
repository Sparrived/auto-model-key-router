package configops

import (
	"sort"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// migratedUnified 返回按要求迁移后的 data["unified_model"]。
//
// Python 侧一律写成 `migrate_config_data(data).get("unified_model")`：migrate 先
// 深拷贝整份配置，所以拿到的是**副本**；调用方改完必须自己赋回 data。这一点很
// 重要——它顺带让 v3 的旧 unified_model 形状在这些函数里被无痛迁移。
//
// 第二个返回值报告该值是否存在（Python 的 None 与「键不存在」在这里等价，
// 因为 `.get()` 对两者都给出 None）。
func migratedUnified(data *canonical.Value) (*canonical.Value, error) {
	migrated, err := config.MigrateConfigData(data)
	if err != nil {
		return nil, err
	}
	value, ok := migrated.LookupOK("unified_model")
	if !ok {
		return nil, nil
	}
	return value, nil
}

// unifiedTargets 按固定顺序展开 unified_model 里的所有目标对象。
//
// 对齐 config_operations.py:174：只认 default/image/embeddings 三个计划，每个
// 只看 primary/fallback，且只有值是对象才算数（非对象静默跳过）。
func unifiedTargets(unified *canonical.Value) []*canonical.Value {
	result := []*canonical.Value{}
	for _, planName := range UNIFIEDPlanNames {
		plan := lookup(unified, planName)
		if !plan.IsObject() {
			continue
		}
		for _, role := range []string{"primary", "fallback"} {
			target := lookup(plan, role)
			if target.IsObject() {
				result = append(result, target)
			}
		}
	}
	return result
}

// ReplaceUnifiedModelName 把 unified_model 里对 oldName 的引用改成 newName。
//
// 对齐 config_operations.py:187。只在 unified_model 是对象时生效；注意它是写在
// **迁移后**的副本上再赋回 data。
func ReplaceUnifiedModelName(data *canonical.Value, oldName, newName string) error {
	unified, err := migratedUnified(data)
	if err != nil {
		return err
	}
	if !unified.IsObject() {
		return nil
	}
	for _, target := range unifiedTargets(unified) {
		if pyEqualsString(lookup(target, "model"), oldName) {
			target.SetKey("model", canonical.NewString(newName))
		}
	}
	data.SetKey("unified_model", unified)
	return nil
}

// modelHasProviderKey 报告 unified 目标里选的 key 是否落在该 provider 下。
//
// 对齐 config_operations.py:197：模型名匹配 key 名本身或 `{provider}-{key}` 形式
// 的带前缀名字；config 为 nil（解析失败）时一律 false。
func modelHasProviderKey(routerConfig *config.RouterConfig, modelID, providerID string, keyName *string) bool {
	if routerConfig == nil {
		return false
	}
	var model *config.ModelConfig
	for index := range routerConfig.Models {
		if routerConfig.Models[index].ID == modelID {
			model = &routerConfig.Models[index]
			break
		}
	}
	if model == nil || keyName == nil || *keyName == "" {
		return false
	}
	for _, key := range model.Keys {
		nameMatches := key.Name == *keyName || strings.HasSuffix(key.Name, "-"+*keyName)
		if !nameMatches || key.Provider != providerID {
			continue
		}
		return true
	}
	return false
}

// parseConfigWithoutUnified 复刻「拷一份 data、去掉 unified_model、再解析」的套路。
//
// 参照实现刻意先摘掉 unified_model 再解析：否则残留的失效 unified 目标会让解析
// 失败，调用方就什么都修不了。返回 (配置, 是否解析成功)。
func parseConfigWithoutUnified(data *canonical.Value) *config.RouterConfig {
	candidate := data.Clone()
	candidate.DeleteKey("unified_model")
	parsed, err := config.FromDict(candidate)
	if err != nil {
		return nil
	}
	return parsed
}

// clearUnifiedKeysFromProvider 清掉 unified_model 里指向该 provider 的 key 选择。
//
// 对齐 config_operations.py:214。keyName 为 nil 表示该 provider 的所有 key 都清。
func clearUnifiedKeysFromProvider(data *canonical.Value, providerID string, keyName *string) error {
	unified, err := migratedUnified(data)
	if err != nil {
		return err
	}
	if !unified.IsObject() {
		return nil
	}
	routerConfig := parseConfigWithoutUnified(data)
	if routerConfig == nil {
		return nil
	}
	for _, target := range unifiedTargets(unified) {
		modelID := lookup(target, "model").StringValue()
		targetKey := lookup(target, "key").StringValue()
		var selected *string
		if targetKey != "" {
			selected = &targetKey
		}
		if !modelHasProviderKey(routerConfig, modelID, providerID, selected) {
			continue
		}
		if keyName == nil || modelHasProviderKey(routerConfig, modelID, providerID, keyName) {
			target.SetKey("key", canonical.NewNull())
		}
	}
	data.SetKey("unified_model", unified)
	return nil
}

// renameUnifiedKey 把 unified_model 里选中的 `{前缀}{旧 key 名}` 改写成新 key 名。
//
// 对齐 config_operations.py:243。与 clearUnifiedKeysFromProvider 的差别是解析
// 失败时 config 置 nil 而不是直接返回（后面的 modelHasProviderKey 于是恒 false，
// 效果一样，但赋值语句仍然执行）。
func renameUnifiedKey(data *canonical.Value, providerID, oldName, newName string) error {
	unified, err := migratedUnified(data)
	if err != nil {
		return err
	}
	if !unified.IsObject() {
		return nil
	}
	routerConfig := parseConfigWithoutUnified(data)
	for _, target := range unifiedTargets(unified) {
		selected := lookup(target, "key").StringValue()
		if selected == "" {
			continue
		}
		modelID := lookup(target, "model").StringValue()
		if !modelHasProviderKey(routerConfig, modelID, providerID, &oldName) {
			continue
		}
		// 选中的名字可能带 provider 前缀（`{provider}-{key}`），只替换后缀部分。
		if selected == oldName || strings.HasSuffix(selected, "-"+oldName) {
			prefix := ""
			if strings.HasSuffix(selected, "-"+oldName) {
				prefix = selected[:len(selected)-len(oldName)]
			}
			target.SetKey("key", canonical.NewString(prefix+newName))
		}
	}
	data.SetKey("unified_model", unified)
	return nil
}

// FallbackModelID 返回第一个「有 target」的模型 ID（按模型 ID 升序），没有则
// 返回 found=false。
//
// 对齐 config_operations.py:299。它被用于给失效的 unified_model.default 找替代；
// 注意 ModelTargets 会顺带给模型补上空的 targets 数组。
func FallbackModelID(data *canonical.Value) (string, bool, error) {
	all, err := Models(data)
	if err != nil {
		return "", false, err
	}
	keys := objectKeys(all)
	sort.Strings(keys)
	for _, modelID := range keys {
		model := lookup(all, modelID)
		if !model.IsObject() {
			continue
		}
		targets, err := ModelTargets(model)
		if err != nil {
			return "", false, err
		}
		if len(targets.Arr) > 0 {
			return modelID, true, nil
		}
	}
	return "", false, nil
}

// RepairModelReferences 修好所有指向已删改模型的引用。
//
// 对齐 config_operations.py:306，并补上参照实现漏掉的两类清单（本项目自有行为）：
// `access_keys` 与 `workspaces` 的模型/供应商清单。漏掉它们的后果不只是「清单里留了
// 个死名字」——config 层会因此拒绝整份配置，于是删模型、删 Key、删供应商全被一句
// 「引用了未配置的模型」挡住，而用户想删的恰恰是那个模型。
//
// 顺序不能反，而且现在更严格：repair_unified_model 要先把候选配置完整解析一遍才敢改，
// 任何一处残留的失效引用（任务、访问密钥清单、工作空间清单）都会让那次解析直接失败。
// 所以失效引用必须先全部清掉，才轮到 unified_model。
func RepairModelReferences(data *canonical.Value) error {
	if err := RepairReferenceLists(data); err != nil {
		return err
	}
	if _, err := RepairTasks(data); err != nil {
		return err
	}
	return RepairUnifiedModel(data)
}

// RepairUnifiedModel 修好 unified_model：把模型名规范化成模型 ID、丢掉失效的
// 计划与目标、必要时换一个 model 当 default.primary。
//
// 对齐 config_operations.py:316。整段逻辑都建立在「解析失败就什么都不改」之上，
// 因此改动前必须先复制一份去掉 unified_model 的候选配置解析成功。
func RepairUnifiedModel(data *canonical.Value) error {
	unified, err := migratedUnified(data)
	if err != nil {
		return err
	}
	if !unified.IsObject() {
		data.DeleteKey("unified_model")
		return nil
	}
	routerConfig := parseConfigWithoutUnified(data)
	if routerConfig == nil {
		return nil
	}
	configured := map[string]config.ModelConfig{}
	names := map[string]string{}
	for _, model := range routerConfig.Models {
		configured[model.ID] = model
		names[model.ID] = model.ID
		for _, alias := range model.Aliases {
			names[alias] = model.ID
		}
	}

	defaultPlan := lookup(unified, "default")
	var primary *canonical.Value
	if defaultPlan.IsObject() {
		primary = lookup(defaultPlan, "primary")
	}
	_, primaryKnown := names[lookup(primary, "model").StringValue()]
	if !primary.IsObject() || !primaryKnown {
		replacement, found, err := FallbackModelID(data)
		if err != nil {
			return err
		}
		if !found {
			data.DeleteKey("unified_model")
			return nil
		}
		unified.SetKey("default", canonical.NewObjectOf(
			canonical.ObjectPair{Key: "primary", Value: canonical.NewObjectOf(
				canonical.ObjectPair{Key: "model", Value: canonical.NewString(replacement)},
				canonical.ObjectPair{Key: "key", Value: canonical.NewNull()},
			)},
		))
	}

	for _, planName := range UNIFIEDPlanNames {
		plan := lookup(unified, planName)
		if !plan.IsObject() {
			if planName != "default" {
				unified.DeleteKey(planName)
			}
			continue
		}
		for _, role := range []string{"primary", "fallback"} {
			target := lookup(plan, role)
			if !target.IsObject() {
				if role == "fallback" {
					plan.DeleteKey(role)
				}
				continue
			}
			modelID, known := names[lookup(target, "model").StringValue()]
			if !known {
				if planName == "default" && role == "primary" {
					continue
				}
				if role == "primary" {
					unified.DeleteKey(planName)
					break
				}
				plan.DeleteKey(role)
				continue
			}
			target.SetKey("model", canonical.NewString(modelID))
			selected := strings.TrimSpace(lookup(target, "key").StringValue())
			if selected != "" {
				usable := false
				for _, key := range configured[modelID].Keys {
					if key.Enabled && (key.Name == selected || strings.HasSuffix(key.Name, "-"+selected)) {
						usable = true
						break
					}
				}
				if !usable {
					target.SetKey("key", canonical.NewNull())
				}
			}
		}
		planPrimary := lookup(plan, "primary")
		planFallback := lookup(plan, "fallback")
		if planPrimary.IsObject() && planFallback.IsObject() &&
			pyEqualValues(lookup(planPrimary, "model"), lookup(planFallback, "model")) {
			plan.DeleteKey("fallback")
		}
	}
	data.SetKey("unified_model", unified)
	return nil
}

// SetUnifiedModel 设置（或删除）unified_model，并把它规范化成 primary/fallback 形状。
//
// 对齐 config_operations.py:1047。unified 为 nil / null 时删除该键；否则先在一份
// 候选配置上跑完整校验（失败即 422 且不改 data），再把解析结果重新序列化回去——
// 也就是说未知字段会被丢弃，模型名/别名会被规范化成模型 ID。
func SetUnifiedModel(data *canonical.Value, unified *canonical.Value) error {
	if unified == nil || unified.IsNull() {
		data.DeleteKey("unified_model")
		return nil
	}
	candidate := data.Clone()
	candidate.SetKey("unified_model", unified.Clone())
	parsed, err := config.FromDict(candidate)
	if err != nil {
		return opErr(422, err.Error())
	}
	serialized := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "default", Value: serializePlan(parsed.UnifiedModel.Default)},
	)
	if parsed.UnifiedModel.Image != nil {
		serialized.SetKey("image", serializePlan(*parsed.UnifiedModel.Image))
	}
	if parsed.UnifiedModel.Embeddings != nil {
		serialized.SetKey("embeddings", serializePlan(*parsed.UnifiedModel.Embeddings))
	}
	data.SetKey("unified_model", serialized)
	return nil
}

// serializePlan 复刻 _serialize_plan（config_operations.py:1065）。
//
// Go 的 RouteTarget.Key 是 string，Python 是 `str | None`；两者在这里等价：
// config 包的 parse_target 已经把空 key 折成 ""（Python 折成 None）。
func serializePlan(plan config.RoutePlan) *canonical.Value {
	primary := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "model", Value: canonical.NewString(plan.Primary.Model)},
	)
	if plan.Primary.Key != "" {
		primary.SetKey("key", canonical.NewString(plan.Primary.Key))
	}
	result := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "primary", Value: primary},
	)
	if plan.Fallback != nil {
		fallback := canonical.NewObjectOf(
			canonical.ObjectPair{Key: "model", Value: canonical.NewString(plan.Fallback.Model)},
		)
		if plan.Fallback.Key != "" {
			fallback.SetKey("key", canonical.NewString(plan.Fallback.Key))
		}
		result.SetKey("fallback", fallback)
	}
	return result
}

// validateUnifiedKey 校验 key 名在该模型下存在且启用。
//
// 对齐 config_operations.py:1078。
func validateUnifiedKey(routerConfig *config.RouterConfig, modelID, keyName string) error {
	var model *config.ModelConfig
	for index := range routerConfig.Models {
		if routerConfig.Models[index].ID == modelID {
			model = &routerConfig.Models[index]
			break
		}
	}
	if model == nil {
		return opErrf(404, "模型 %s 的 key 不存在或未启用: %s", modelID, keyName)
	}
	for _, key := range model.Keys {
		if key.Enabled && (key.Name == keyName || strings.HasSuffix(key.Name, "-"+keyName)) {
			return nil
		}
	}
	return opErrf(404, "模型 %s 的 key 不存在或未启用: %s", modelID, keyName)
}

// SwitchUnifiedTarget 切换 unified_model 的某个「计划.角色」指向的模型。
//
// 对齐 config_operations.py:1084。target 形如 `default.primary`、`image.fallback`。
// modelName 为 nil（Python None）表示沿用当前目标，此时必须已经有目标，否则 422。
// keyName 与 updateKey 分别对应 Python 的 key_name 与 update_key。
func SwitchUnifiedTarget(data *canonical.Value, target string, modelName, keyName *string, updateKey bool) error {
	valid := false
	for _, candidate := range UnifiedTargets {
		if candidate == target {
			valid = true
			break
		}
	}
	if !valid {
		return opErrf(422, "无效 unified 目标: %s", target)
	}
	routerConfig, err := config.FromDict(data)
	if err != nil {
		return err
	}
	planName, role, _ := strings.Cut(target, ".")

	var currentPlan *config.RoutePlan
	if routerConfig.UnifiedModel != nil {
		switch planName {
		case "default":
			plan := routerConfig.UnifiedModel.Default
			currentPlan = &plan
		case "image":
			currentPlan = routerConfig.UnifiedModel.Image
		case "embeddings":
			currentPlan = routerConfig.UnifiedModel.Embeddings
		}
	}
	var currentTarget *config.RouteTarget
	if currentPlan != nil {
		if role == "primary" {
			primary := currentPlan.Primary
			currentTarget = &primary
		} else {
			currentTarget = currentPlan.Fallback
		}
	}

	var modelID string
	if modelName == nil {
		if currentTarget == nil {
			return opErrf(422, "尚未配置 %s，请先选择模型", target)
		}
		modelID = currentTarget.Model
	} else {
		resolved, found := routerConfig.ConfiguredModelID(strings.TrimSpace(*modelName))
		if !found {
			return opErrf(404, "未配置模型或别名: %s", *modelName)
		}
		modelID = resolved
	}

	// Python 用 None 表示「不指定 key」；Go 的 RouteTarget.Key 把 None 折成空串。
	var selectedKey *string
	if currentTarget != nil && currentTarget.Key != "" {
		key := currentTarget.Key
		selectedKey = &key
	}
	if currentTarget == nil || modelID != currentTarget.Model {
		selectedKey = nil
	}
	if updateKey {
		selectedKey = nil
		// Python: `key_name.strip() if key_name else None`。strip 后为空串时它
		// 不是 None，但随后的 set_unified_model 会把空 key 规范成「无 key」，
		// 因此 Go 侧直接折成 nil，最终落盘结果一致。
		if keyName != nil && *keyName != "" {
			if trimmed := strings.TrimSpace(*keyName); trimmed != "" {
				value := trimmed
				selectedKey = &value
			}
		}
		if selectedKey != nil {
			if err := validateUnifiedKey(routerConfig, modelID, *selectedKey); err != nil {
				return err
			}
		}
	}

	migrated, err := migratedUnified(data)
	if err != nil {
		return err
	}
	if !migrated.IsObject() {
		migrated = canonical.NewObjectOf(canonical.ObjectPair{Key: "default", Value: canonical.NewObject()})
	}
	plan := lookup(migrated, planName)
	if plan == nil {
		plan = canonical.NewObject()
		migrated.SetKey(planName, plan)
	}
	if !plan.IsObject() {
		return opErrf(422, "unified_model.%s 必须是对象", planName)
	}
	entry := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "model", Value: canonical.NewString(modelID)},
	)
	if selectedKey == nil {
		entry.SetKey("key", canonical.NewNull())
	} else {
		entry.SetKey("key", canonical.NewString(*selectedKey))
	}
	plan.SetKey(role, entry)
	if planName != "default" && role == "fallback" && !plan.Obj.Has("primary") {
		return opErrf(422, "配置 %s.fallback 前必须先配置 %s.primary", planName, planName)
	}
	return SetUnifiedModel(data, migrated)
}
