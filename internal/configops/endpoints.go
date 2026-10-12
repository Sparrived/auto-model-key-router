package configops

import (
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件管**模型的端点计划**：`models.<id>.endpoints` 是「端点族 → 改发给哪个模型
// （可带固定 Key）」的覆盖表，与 `unified_model` 的计划同构，只是可以配在任意模型上。
//
// 一张表只跳一跳：直呼某个模型时，落在它配置过的端点族上的请求改发给那一族指定的模型，
// 没配置的族照旧用该模型自己的 targets（见 keypool.ResolveRouteIn）。
//
// 与 unified_model 的关系：那边是「固定名称 unified-model 的计划表」，这边是「任意模型的
// 计划表」，因此解析、序列化、校验、修复、改名跟随全都是同一套规则。

// SetModelEndpoints 整体替换某个模型的端点计划；endpoints 为 nil / null / 空对象表示
// 清空（该模型不再按端点分派）。
//
// 与 SetUnifiedModel 同一套路：先在一份**候选配置**上跑完整解析校验（失败即 422 且不改
// data），再把解析结果序列化回去——也就是说未知字段会被丢弃、模型名/别名会被规范化成
// 真实模型 ID。
func SetModelEndpoints(data *canonical.Value, modelID string, endpoints *canonical.Value) error {
	model, err := RequireModel(data, modelID)
	if err != nil {
		return err
	}
	if endpoints == nil || endpoints.IsNull() || (endpoints.IsObject() && len(objectKeys(endpoints)) == 0) {
		model.DeleteKey("endpoints")
		return nil
	}
	candidate := data.Clone()
	candidateModel, err := RequireModel(candidate, modelID)
	if err != nil {
		return err
	}
	candidateModel.SetKey("endpoints", endpoints.Clone())
	parsed, err := config.FromDict(candidate)
	if err != nil {
		return opErr(422, err.Error())
	}
	if plans := parsedModelEndpoints(parsed, model, modelID); len(plans) != 0 {
		model.SetKey("endpoints", serializeEndpointPlans(plans))
		return nil
	}
	// 解析后一个族都不剩（例如只写了空的 primary）：与显式清空同义。
	model.DeleteKey("endpoints")
	return nil
}

// parsedModelEndpoints 在解析结果里找出 modelID 对应的端点计划。
//
// 模型在配置里的键与它解析出的 ID 可以不同（对象里带 `id` 字段的历史形状），因此先按
// 原始对象的 `id` 找、再退回按键名找。
func parsedModelEndpoints(parsed *config.RouterConfig, rawModel *canonical.Value, modelID string) map[string]*config.RoutePlan {
	if parsed == nil {
		return nil
	}
	id := strings.TrimSpace(lookup(rawModel, "id").StringValue())
	if id == "" {
		id = modelID
	}
	for index := range parsed.Models {
		if parsed.Models[index].ID == id {
			return parsed.Models[index].Endpoints
		}
	}
	return nil
}

// serializeEndpointPlans 按 UNIFIEDPlanNames 顺序写出**已配置**的族。
//
// 顺序固定：它直接落在配置文件里，用户与 diff 都按这个顺序读。
func serializeEndpointPlans(plans map[string]*config.RoutePlan) *canonical.Value {
	result := canonical.NewObject()
	for _, planName := range UNIFIEDPlanNames {
		if plan := plans[planName]; plan != nil {
			result.SetKey(planName, serializePlan(*plan))
		}
	}
	return result
}

// RepairModelEndpoints 修好所有模型的端点计划：模型名折成真实 ID、丢掉指向已不存在
// 模型的族与目标、清掉在那个模型下不可用的 Key，最后丢掉变空的端点表。
//
// 与 RepairUnifiedModel 同一套规则（共用 repairPlanContainer），但没有「default 必须
// 存在」那条：模型的端点计划全是可选的，清空了就等于这个模型不再按端点分派。
func RepairModelEndpoints(data *canonical.Value) error {
	all, err := Models(data)
	if err != nil {
		return err
	}
	parsed := parseConfigWithoutPlans(data)
	if parsed == nil {
		return nil
	}
	names, configured := configuredModelIndex(parsed)
	for _, pair := range objectItems(all) {
		if !pair.Value.IsObject() {
			continue
		}
		endpoints := lookup(pair.Value, "endpoints")
		if !endpoints.IsObject() {
			// 形状不对（不是对象）时整段丢掉：解析器会因此拒绝整份配置，而这里正是
			// 「把配置修回可解析」的落点。
			if endpoints != nil {
				pair.Value.DeleteKey("endpoints")
			}
			continue
		}
		repairPlanContainer(endpoints, names, configured, nil)
		if len(objectKeys(endpoints)) == 0 {
			pair.Value.DeleteKey("endpoints")
		}
	}
	return nil
}

// FollowModelEndpointRename 把各模型端点计划里对旧模型名的引用改成新名字。
//
// 与 FollowModelRename 同一口径：只改**与旧 ID 完全相同**的名字。计划里的模型名在
// 写入时已被规范化成真实 ID，别名不随 ID 改名失效，跟着改反而会把一个仍有效的写法改没。
func FollowModelEndpointRename(data *canonical.Value, oldName, newName string) {
	for _, pair := range objectItems(lookup(data, "models")) {
		if !pair.Value.IsObject() {
			continue
		}
		endpoints := lookup(pair.Value, "endpoints")
		if !endpoints.IsObject() {
			continue
		}
		for _, target := range planTargets(endpoints) {
			if pyEqualsString(lookup(target, "model"), oldName) {
				target.SetKey("model", canonical.NewString(newName))
			}
		}
	}
}

// planTargets 展开一个计划容器（unified_model 或某个模型的 endpoints）里的全部目标对象。
//
// 按 UNIFIEDPlanNames 顺序遍历每个计划的 primary/fallback，只有值是对象才算数（非对象
// 静默跳过）。两处共用同一份遍历：写死一份会让新增的族在修复/改名时被漏掉，表现为
// 「删了模型，但某个端点计划还引用着它」。
func planTargets(container *canonical.Value) []*canonical.Value {
	result := []*canonical.Value{}
	for _, planName := range UNIFIEDPlanNames {
		plan := lookup(container, planName)
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

// configuredModelIndex 把解析结果折成「名字（ID 或别名）→ 真实 ID」与「真实 ID → 模型」。
func configuredModelIndex(parsed *config.RouterConfig) (map[string]string, map[string]config.ModelConfig) {
	configured := map[string]config.ModelConfig{}
	names := map[string]string{}
	for _, model := range parsed.Models {
		configured[model.ID] = model
		names[model.ID] = model.ID
		for _, alias := range model.Aliases {
			names[alias] = model.ID
		}
	}
	return names, configured
}

// repairPlanContainer 就地修好一个计划容器里的每个计划。
//
// keepPrimary 里的计划名即使 primary 失效也保留（unified_model.default 由调用方另行
// 补位；模型的端点计划传 nil）。
func repairPlanContainer(
	container *canonical.Value,
	names map[string]string,
	configured map[string]config.ModelConfig,
	keepPrimary map[string]bool,
) {
	for _, planName := range UNIFIEDPlanNames {
		plan := lookup(container, planName)
		if !plan.IsObject() {
			if !keepPrimary[planName] {
				container.DeleteKey(planName)
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
				if keepPrimary[planName] && role == "primary" {
					continue
				}
				if role == "primary" {
					container.DeleteKey(planName)
					break
				}
				plan.DeleteKey(role)
				continue
			}
			target.SetKey("model", canonical.NewString(modelID))
			selected := strings.TrimSpace(lookup(target, "key").StringValue())
			if selected == "" {
				continue
			}
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
		// primary 与 fallback 指向同一个模型时，备选是纯粹的重复：留着只会让重试白跑
		// 一轮（与 unified_model 的清理同一口径）。
		planPrimary := lookup(plan, "primary")
		planFallback := lookup(plan, "fallback")
		if planPrimary.IsObject() && planFallback.IsObject() &&
			pyEqualValues(lookup(planPrimary, "model"), lookup(planFallback, "model")) {
			plan.DeleteKey("fallback")
		}
	}
}

// parseConfigWithoutPlans 在去掉 unified_model 与**所有模型的端点计划**之后解析配置。
//
// 修复路径必须用它：两份计划互相引用对方的模型，任何一份残留的失效引用都会让整份配置
// 解析失败，于是「删掉一个模型」会连带卡住其它引用的修复。去掉的只是即将被修复的字段，
// 模型列表与 targets 照常解析。
func parseConfigWithoutPlans(data *canonical.Value) *config.RouterConfig {
	candidate := data.Clone()
	candidate.DeleteKey("unified_model")
	for _, pair := range objectItems(lookup(candidate, "models")) {
		if pair.Value.IsObject() {
			pair.Value.DeleteKey("endpoints")
		}
	}
	parsed, err := config.FromDict(candidate)
	if err != nil {
		return nil
	}
	return parsed
}
