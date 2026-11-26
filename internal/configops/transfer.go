package configops

import (
	"strconv"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// TransferableConfig 抽出可迁移的路由配置与显式设置的追踪策略。
//
// 对齐 config_operations.py:927。刻意**不**导出 local_api_key、监听地址、超时
// 等机器相关设置；同时清掉两类本地信息：
//
//   - `_amkr_model_key_clone` 标记：那是「为某个模型临时拆出来的克隆供应商」，
//     迁移到别的实例后应作为普通供应商存在；
//   - capabilities 探测缓存：各 Key 看到的模型清单因机器而异。
//
// 原先还有一个 include_visitor 参数（为假时清掉各 key 的 allow_visitor）：该字段已
// 随访客模式删除，参数也随之取消——导出的形状从此与配置无关。
//
// **访问密钥不随这份导出走**：它和 local_api_key 同类，是本实例的入站凭据。导出文件
// 会被贴到工单、聊天记录、文档里，带上 key 等于把凭据散出去。
func TransferableConfig(data *canonical.Value) (*canonical.Value, error) {
	// Python 先 deepcopy(providers(data))，因此这里的 setdefault 会落到 data 上。
	allProviders, err := Providers(data)
	if err != nil {
		return nil, err
	}
	resultProviders := allProviders.Clone()
	for _, provider := range objectItems(resultProviders) {
		if !provider.Value.IsObject() {
			continue
		}
		provider.Value.DeleteKey("_amkr_model_key_clone")
		keys, err := ProviderKeys(provider.Value)
		if err != nil {
			return nil, err
		}
		for _, key := range objectItems(keys) {
			if key.Value.IsObject() {
				key.Value.DeleteKey("capabilities")
			}
		}
	}
	allModels, err := Models(data)
	if err != nil {
		return nil, err
	}
	resultModels := allModels.Clone()
	result := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "config_version", Value: canonical.NewInt(strconv.Itoa(config.CONFIG_VERSION))},
		canonical.ObjectPair{Key: "providers", Value: resultProviders},
		canonical.ObjectPair{Key: "models", Value: resultModels},
	)
	// 任务的模型引用与 models 一起走；不导出等于静默丢掉任务路由。
	if tasks := ExistingTasks(data); tasks.Obj.Len() > 0 {
		result.SetKey("tasks", tasks.Clone())
	}
	// 工作空间随任务一起迁移，否则导入方只剩默认空间，命名空间的任务全丢。
	//
	// 但**必须摘掉两把 key**：和 local_api_key 同类，它们都是本实例的入站凭据。导出文件
	// 会被贴到工单、聊天记录、文档里，把凭据一起带出去等于把每个空间的门禁卡散出去；
	// 导入方也会凭空获得一堆能开自己实例面板或刷自己额度的 key（而原名空间可能压根不是
	// 同一回事）。工作空间有自己的独立迁移通道（带 api_key，见 workspace_bundle.go），
	// 那是**有意**的凭据搬迁，与这里顺手带出去性质完全不同。
	//
	// inference_key 必须一起摘：它比 api_key 更敏感——api_key 只开一个空间的**面板**，
	// inference_key 能直接消耗上游额度。只摘 api_key 会让这条「导出不含凭据」的承诺
	// 在新增字段后悄悄失效。
	if workspaces := lookup(data, "workspaces"); workspaces.IsObject() && workspaces.Obj.Len() > 0 {
		exported := workspaces.Clone()
		for _, workspace := range objectItems(exported) {
			if workspace.Value.IsObject() {
				workspace.Value.DeleteKey("api_key")
				workspace.Value.DeleteKey("inference_key")
			}
		}
		result.SetKey("workspaces", exported)
	}
	if raw, present := data.LookupOK("route_trace"); present {
		cfg, err := config.ParseRouteTraceConfig(raw)
		if err != nil {
			return nil, err
		}
		result.SetKey("route_trace", cfg.Value())
	}
	return result, nil
}

// MergeResult 是 MergeTransferableConfig 的结果。
type MergeResult struct {
	// Config 是合并后的完整配置（current 的深拷贝 + 导入内容）。
	Config *canonical.Value
	// AddedModels / AddedKeys / SkippedKeys 对应 Python 返回的三元组计数。
	AddedModels int
	AddedKeys   int
	SkippedKeys int
}

// MergeTransferableConfig 把一份可迁移配置合并进当前配置。
//
// 对齐 config_operations.py:954。合并规则：
//
//   - 供应商同名时按 base_url 判断是否同一个；URL 不同则改名成 `{id}-2`、`{id}-3`；
//   - key 按 **api_key 去重**：secret 已存在的直接跳过（计入 skipped），同名不同
//     secret 则改名成 `{name}-2`……；target 引用通过两张映射表重写；
//   - 任务整体并入后交给 repair_tasks 规范化，解析不了的任务被丢掉；
//   - 最后用完整的 RouterConfig.from_dict 校验一次，失败抛 422（**所有**解析异常
//     都被折叠成 422，因此这里连 Go 的 InternalError 也要包成
//     ConfigOperationError）。
func MergeTransferableConfig(currentData, transferData *canonical.Value) (MergeResult, error) {
	merged := currentData.Clone()
	merged.SetKey("config_version", canonical.NewInt(strconv.Itoa(config.CONFIG_VERSION)))
	// 旧迁移包没有该设置，不应覆盖目标实例当前的追踪策略。
	if raw, present := transferData.LookupOK("route_trace"); present {
		cfg, err := config.ParseRouteTraceConfig(raw)
		if err != nil {
			return MergeResult{}, opErr(422, err.Error())
		}
		merged.SetKey("route_trace", cfg.Value())
	}
	currentProviders, err := Providers(merged)
	if err != nil {
		return MergeResult{}, err
	}
	currentModels, err := Models(merged)
	if err != nil {
		return MergeResult{}, err
	}
	transferProviders, err := Providers(transferData)
	if err != nil {
		return MergeResult{}, err
	}
	transferModels, err := Models(transferData)
	if err != nil {
		return MergeResult{}, err
	}

	addedModels, addedKeys, skippedKeys := 0, 0, 0
	providerMap := map[string]string{}
	// keyMap 的 nil 值表示「该 key 因 secret 重复被跳过」，与「键不存在」都要
	// 让调用方放弃这条 target 绑定。
	keyMap := map[[2]string]*string{}

	for _, source := range objectItems(transferProviders) {
		sourceID := source.Key
		sourceProvider := source.Value
		if !sourceProvider.IsObject() {
			continue
		}
		targetID := sourceID
		var currentProvider *canonical.Value
		existing, present := currentProviders.LookupOK(targetID)
		switch {
		case present && existing.IsObject():
			currentProvider = existing
			existingURL, err := config.NormalizeUpstreamBaseURL(lookup(currentProvider, "base_url"))
			if err != nil {
				return MergeResult{}, err
			}
			sourceURL, err := config.NormalizeUpstreamBaseURL(lookup(sourceProvider, "base_url"))
			if err != nil {
				return MergeResult{}, err
			}
			if existingURL != sourceURL {
				base, suffix := targetID, 2
				for currentProviders.Obj.Has(base + "-" + strconv.Itoa(suffix)) {
					suffix++
				}
				targetID = base + "-" + strconv.Itoa(suffix)
				currentProvider = detachedProviderClone(sourceProvider)
				currentProviders.SetKey(targetID, currentProvider)
			}
		case present:
			// 键存在但不是对象：Python 的 `current_provider.get("base_url")`
			// 会抛 AttributeError。
			return MergeResult{}, pyAttributeError(existing, "get")
		default:
			currentProvider = detachedProviderClone(sourceProvider)
			currentProviders.SetKey(targetID, currentProvider)
		}

		targetKeys, err := ProviderKeys(currentProvider)
		if err != nil {
			return MergeResult{}, err
		}
		existingSecrets := map[string]bool{}
		for _, key := range objectItems(targetKeys) {
			if key.Value.IsObject() {
				existingSecrets[secretOf(key.Value)] = true
			}
		}
		sourceKeys, err := ProviderKeys(sourceProvider)
		if err != nil {
			return MergeResult{}, err
		}
		for _, sourceKey := range objectItems(sourceKeys) {
			secret := ""
			if sourceKey.Value.IsObject() {
				secret = secretOf(sourceKey.Value)
			}
			if existingSecrets[secret] {
				skippedKeys++
				keyMap[[2]string{sourceID, sourceKey.Key}] = nil
				continue
			}
			destination, base := sourceKey.Key, sourceKey.Key
			suffix := 2
			for targetKeys.Obj.Has(destination) {
				destination = base + "-" + strconv.Itoa(suffix)
				suffix++
			}
			targetKeys.SetKey(destination, sourceKey.Value.Clone())
			mapped := destination
			keyMap[[2]string{sourceID, sourceKey.Key}] = &mapped
			existingSecrets[secret] = true
			addedKeys++
		}
		providerMap[sourceID] = targetID
	}

	for _, source := range objectItems(transferModels) {
		modelID := source.Key
		sourceModel := source.Value
		if !sourceModel.IsObject() {
			continue
		}
		destination := lookup(currentModels, modelID)
		if !destination.IsObject() {
			destination = sourceModel.Clone()
			destination.SetKey("targets", canonical.NewArray())
			currentModels.SetKey(modelID, destination)
			addedModels++
		}
		targets, err := ModelTargets(destination)
		if err != nil {
			return MergeResult{}, err
		}
		identities := map[string]bool{}
		for _, target := range targets.Arr {
			identities[mergeIdentity(target, modelID)] = true
		}
		sourceTargets, err := ModelTargets(sourceModel)
		if err != nil {
			return MergeResult{}, err
		}
		for _, sourceTarget := range sourceTargets.Arr {
			sourceProviderID := lookup(sourceTarget, "provider").StringValue()
			mapped, ok := providerMap[sourceProviderID]
			if !ok || mapped == "" {
				continue
			}
			sourceKey := lookup(sourceTarget, "key").StringValue()
			mappedKey := keyMap[[2]string{sourceProviderID, sourceKey}]
			if mappedKey == nil {
				continue
			}
			upstream := lookup(sourceTarget, "upstream_model").StringValue()
			if upstream == "" {
				upstream = modelID
			}
			newTarget := canonical.NewObjectOf(
				canonical.ObjectPair{Key: "provider", Value: canonical.NewString(mapped)},
				canonical.ObjectPair{Key: "key", Value: canonical.NewString(*mappedKey)},
				canonical.ObjectPair{Key: "upstream_model", Value: canonical.NewString(upstream)},
			)
			identity := mergeIdentity(newTarget, modelID)
			if identities[identity] {
				continue
			}
			targets.Arr = append(targets.Arr, newTarget)
			identities[identity] = true
		}
	}

	// 任务随 models 一起迁移；导入的任务可能引用别名或没被带过来的模型，统一
	// 交给 repair_tasks 规范化。
	if transferTasks := lookup(transferData, "tasks"); transferTasks.IsObject() {
		mergedTasks := ExistingTasks(merged)
		for _, task := range objectItems(transferTasks) {
			if !task.Value.IsObject() {
				continue
			}
			mergedTasks.SetKey(task.Key, task.Value.Clone())
		}
		if mergedTasks.Obj.Len() > 0 {
			merged.SetKey("tasks", mergedTasks)
		}
	}
	// 命名工作空间同样并入：同名空间的任务逐个覆盖，导入方的同空间同名任务胜出。
	if transferWorkspaces := lookup(transferData, "workspaces"); transferWorkspaces.IsObject() {
		for _, source := range objectItems(transferWorkspaces) {
			if !source.Value.IsObject() {
				continue
			}
			mergedTasks := WorkspaceTasks(merged, source.Key)
			for _, task := range objectItems(lookup(source.Value, "tasks")) {
				if task.Value.IsObject() {
					mergedTasks.SetKey(task.Key, task.Value.Clone())
				}
			}
			writeWorkspaceTasks(merged, source.Key, mergedTasks)
		}
	}
	if lookup(transferData, "tasks").IsObject() || lookup(transferData, "workspaces").IsObject() {
		if _, err := RepairTasks(merged); err != nil {
			return MergeResult{}, err
		}
	}
	if ExistingTasks(merged).Obj.Len() == 0 {
		merged.DeleteKey("tasks")
	}
	if _, err := config.FromDict(merged); err != nil {
		return MergeResult{}, opErr(422, err.Error())
	}
	return MergeResult{
		Config:      merged,
		AddedModels: addedModels,
		AddedKeys:   addedKeys,
		SkippedKeys: skippedKeys,
	}, nil
}

// detachedProviderClone 复刻「deepcopy 供应商、清空 keys、去掉 capabilities」。
//
// 保留其余字段（含 `_amkr_model_key_clone`）：参照实现也只 pop capabilities。
func detachedProviderClone(source *canonical.Value) *canonical.Value {
	clone := source.Clone()
	clone.SetKey("keys", canonical.NewObject())
	clone.DeleteKey("capabilities")
	return clone
}

// secretOf 复刻 `str(key.get("api_key") or "")`。
func secretOf(key *canonical.Value) string {
	return lookup(key, "api_key").StringValue()
}

// mergeIdentity 复刻合流用的三元组：
// `(str(provider or ""), str(key or ""), str(upstream_model or model_id))`。
//
// 注意与 models.go 的 targetIdentity 不同：那里没有 `or ""`，因此 null 得到
// "None"；这里有 `or ""`，null/0/false 都折成空串。用错会让去重行为漂移。
func mergeIdentity(target *canonical.Value, modelID string) string {
	upstream := lookup(target, "upstream_model").StringValue()
	if upstream == "" {
		upstream = modelID
	}
	return lookup(target, "provider").StringValue() + "\x00" +
		lookup(target, "key").StringValue() + "\x00" + upstream
}
