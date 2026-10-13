package configops

import (
	"fmt"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// ValidateTargets 校验一组模型 target：非空、每项是对象、provider/key/
// upstream_model 非空、且 provider 与 key 真实存在。
//
// 对齐 config_operations.py:518。它是所有写路径的守门人：先给出「人类可读」的
// 422，再由 config 包兜底真正的合法性。
func ValidateTargets(data *canonical.Value, targets []*canonical.Value) error {
	if len(targets) == 0 {
		return opErr(422, "targets 不能为空")
	}
	for _, target := range targets {
		if !target.IsObject() {
			return opErr(422, "targets 必须是对象数组")
		}
		providerID, err := nonEmpty(lookup(target, "provider"), "target.provider")
		if err != nil {
			return err
		}
		keyName, err := nonEmpty(lookup(target, "key"), "target.key")
		if err != nil {
			return err
		}
		if _, err := nonEmpty(lookup(target, "upstream_model"), "target.upstream_model"); err != nil {
			return err
		}
		provider, err := RequireProvider(data, providerID)
		if err != nil {
			return err
		}
		if _, err := RequireKey(provider, keyName); err != nil {
			return err
		}
	}
	return nil
}

// AddModelTarget 给模型追加一条 target（去重按 provider+key+upstream_model）。
//
// 对齐 config_operations.py:531。写入的是 target 的**深拷贝**，调用方之后改自己
// 那份不会影响配置。
func AddModelTarget(data *canonical.Value, modelID string, target *canonical.Value) error {
	if err := ValidateTargets(data, []*canonical.Value{target}); err != nil {
		return err
	}
	model, err := RequireModel(data, modelID)
	if err != nil {
		return err
	}
	targets, err := ModelTargets(model)
	if err != nil {
		return err
	}
	identity := targetIdentity(target)
	for _, item := range targets.Arr {
		if targetIdentity(item) == identity {
			return opErr(409, "该路由已存在")
		}
	}
	targets.Arr = append(targets.Arr, target.Clone())
	return nil
}

// UpdateModelTarget 按下标改写一条 target 的 upstream_model。
//
// 对齐 config_operations.py:540：先按下标取出、深拷贝、改 upstream_model，
// 再整条校验，最后才写回原位置（校验失败不会留下半个改动）。
func UpdateModelTarget(data *canonical.Value, modelID string, targetIndex int, upstreamModel string) error {
	model, err := RequireModel(data, modelID)
	if err != nil {
		return err
	}
	targets, err := ModelTargets(model)
	if err != nil {
		return err
	}
	if targetIndex < 0 || targetIndex >= len(targets.Arr) {
		return opErr(404, "模型路由不存在")
	}
	updated := targets.Arr[targetIndex].Clone()
	// Python 先算 RHS 再赋值：`updated["upstream_model"] = _non_empty(...)`，所以
	// 空 upstream_model 的 422 排在「目标不是字典」的 TypeError 之前。
	upstream, err := nonEmptyString(upstreamModel, "target.upstream_model")
	if err != nil {
		return err
	}
	if !updated.IsObject() {
		return pyItemAssignmentError(updated)
	}
	updated.SetKey("upstream_model", canonical.NewString(upstream))
	if err := ValidateTargets(data, []*canonical.Value{updated}); err != nil {
		return err
	}
	targets.Arr[targetIndex] = updated
	return nil
}

// DeleteModelTarget 按下标删除一条 target 并返回被删掉的那条（已是脱离配置的副本）。
//
// 这是「无 target 即无路由」不变式的落点之一：删掉最后一条 target 会连带删掉整个
// 路由（并修复 unified_model / 任务对它的引用），而不是留下一条空路由。
//
// 对齐 config_operations.py:550，额外补上最后一条 target 被删时的路由清理。
func DeleteModelTarget(data *canonical.Value, modelID string, targetIndex int) (*canonical.Value, error) {
	model, err := RequireModel(data, modelID)
	if err != nil {
		return nil, err
	}
	targets, err := ModelTargets(model)
	if err != nil {
		return nil, err
	}
	if targetIndex < 0 || targetIndex >= len(targets.Arr) {
		return nil, opErr(404, "模型路由不存在")
	}
	removed := targets.Arr[targetIndex]
	remaining := make([]*canonical.Value, 0, len(targets.Arr)-1)
	remaining = append(remaining, targets.Arr[:targetIndex]...)
	remaining = append(remaining, targets.Arr[targetIndex+1:]...)
	targets.Arr = remaining
	if len(remaining) == 0 {
		// 模型随最后一条 target 一起消失，得走删除模型那条路径（引用修复 + 孤儿 Key 清理）。
		// 但此刻模型的 targets 已经被清空，DeleteModel 读不到刚解绑的这一条，所以由这里
		// 把它交出去：谁删的谁交，否则 WebUI 删路由会清 Key、这条解绑路径却会留下孤儿。
		return removed, deleteModelWithDroppedTargets(data, modelID, []*canonical.Value{removed})
	}
	if err := RepairModelReferences(data); err != nil {
		return nil, err
	}
	return removed, nil
}

// normalizeAliases 复刻 _normalize_aliases（config_operations.py:579）。
func normalizeAliases(aliases []string) ([]string, error) {
	return normalizeNameList(aliases, "模型别称不能重复")
}

// normalizeNameList 去空白、丢空项、查重。
func normalizeNameList(aliases []string, duplicateMessage string) ([]string, error) {
	result := make([]string, 0, len(aliases))
	seen := map[string]bool{}
	for _, alias := range aliases {
		name := strings.TrimSpace(alias)
		if name == "" {
			continue
		}
		if seen[name] {
			return nil, opErr(422, duplicateMessage)
		}
		seen[name] = true
		result = append(result, name)
	}
	return result, nil
}

// validateModelNames 校验模型 ID / 别称不与现有名字冲突。
//
// 可调用名只有模型 ID 与 aliases，两者同处一个全局命名空间：同一个名字落在两个模型
// 上会让解析结果取决于遍历顺序。exclude 用于「改名/改别名时跳过自己」。
func validateModelNames(data *canonical.Value, modelID string, aliases []string, exclude *string) error {
	all, err := Models(data)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, pair := range objectItems(all) {
		if exclude != nil && pair.Key == *exclude {
			continue
		}
		model := pair.Value
		if !model.IsObject() {
			continue
		}
		names[pair.Key] = true
		visible, err := iterateOrDefault(model, "aliases")
		if err != nil {
			return err
		}
		for _, alias := range visible {
			if alias != "" {
				names[alias] = true
			}
		}
	}
	if names[modelID] {
		return opErrf(409, "模型名称重复: %s", modelID)
	}
	for _, alias := range aliases {
		if alias == modelID || names[alias] {
			return opErrf(409, "模型名称重复: %s", alias)
		}
	}
	return nil
}

// CreateModelOptions 是 CreateModel 的可选参数。
//
// RoutingMode 的零值 "" 与 Python 的默认值等价：参照实现写的是
// `str(routing_mode or "round_robin")`，空串同样回落到 round_robin。
// Targets 为 nil 表示 Python 的 None（不校验 targets），空而非 nil 表示显式传了
// 空数组（会报 "targets 不能为空"）。
type CreateModelOptions struct {
	Aliases         []string
	RoutingMode     string
	ReasoningEffort *string
	Targets         []*canonical.Value
	// Endpoints 是模型的端点计划（见 endpoints.go）：nil 表示不配。
	Endpoints *canonical.Value
}

// CreateModel 新建模型并返回它。
//
// 模型对象的键顺序固定为 aliases、routing_mode、targets、[reasoning_effort]，
// 落盘直接受影响，不能改。
func CreateModel(data *canonical.Value, modelID string, options CreateModelOptions) (*canonical.Value, error) {
	id, err := nonEmptyString(modelID, "模型 ID")
	if err != nil {
		return nil, err
	}
	all, err := Models(data)
	if err != nil {
		return nil, err
	}
	if all.Obj.Has(id) {
		return nil, opErrf(409, "模型已存在: %s", id)
	}
	aliases, err := normalizeAliases(options.Aliases)
	if err != nil {
		return nil, err
	}
	if err := validateModelNames(data, id, aliases, nil); err != nil {
		return nil, err
	}
	routingMode := strings.TrimSpace(options.RoutingMode)
	if routingMode == "" {
		routingMode = "round_robin"
	}
	if !isValidRoutingMode(routingMode) {
		return nil, opErr(422, "routing_mode 无效")
	}
	if options.Targets != nil {
		if err := ValidateTargets(data, options.Targets); err != nil {
			return nil, err
		}
	}
	model := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "aliases", Value: canonical.NewStringArray(aliases)},
		canonical.ObjectPair{Key: "routing_mode", Value: canonical.NewString(routingMode)},
		canonical.ObjectPair{Key: "targets", Value: cloneValues(options.Targets)},
	)
	if options.ReasoningEffort != nil {
		effort := strings.TrimSpace(*options.ReasoningEffort)
		if effort != "" && effort != "default" && effort != "downstream" {
			model.SetKey("reasoning_effort", canonical.NewString(effort))
		}
	}
	all.SetKey(id, model)
	// 端点计划走 SetModelEndpoints 而不是就地写：那边要跑一遍完整解析校验，还要把
	// 别名折成真实模型 ID（与 unified_model 同一套规范化）。
	if err := SetModelEndpoints(data, id, options.Endpoints); err != nil {
		return nil, err
	}
	return model, nil
}

// UpdateModelOptions 是 UpdateModel 的可选参数。
//
// nil 表示 Python 的 None（不改）；Aliases 的非 nil 空切片表示「显式清空别名」。
type UpdateModelOptions struct {
	NewID                 *string
	Aliases               []string
	RoutingMode           *string
	ReasoningEffort       *string
	UpdateReasoningEffort bool
	Targets               []*canonical.Value
	// Endpoints 是模型的端点计划（见 endpoints.go）：nil 表示不改，null / 空对象表示清空。
	//
	// 与 Targets 的差别：targets 是「不改」用 nil、「清空」用一个**非 nil 的空切片**
	// 表达（空 targets 等于删掉整条路由，由调用方另行处理）。这里没有那条不变式，
	// 清空就是清空，因此直接收原始值。
	Endpoints *canonical.Value
}

// UpdateModel 修改模型，返回最终 ID（改名时是新 ID）。
//
// 对齐 config_operations.py:636。改名会连带改写 unified_model 里的引用，且
// 改名用 `all[new] = all.pop(old)` —— 模型在 models 里因此移到末尾。
func UpdateModel(data *canonical.Value, modelID string, options UpdateModelOptions) (string, error) {
	all, err := Models(data)
	if err != nil {
		return "", err
	}
	model, err := RequireModel(data, modelID)
	if err != nil {
		return "", err
	}
	targetID := modelID
	if options.NewID != nil {
		if targetID, err = nonEmptyString(*options.NewID, "模型 ID"); err != nil {
			return "", err
		}
	}
	if targetID != modelID && all.Obj.Has(targetID) {
		return "", opErrf(409, "模型已存在: %s", targetID)
	}
	if targetID != modelID {
		// 新 ID 不能撞上本模型自己的别称。
		own, err := ownNames(model)
		if err != nil {
			return "", err
		}
		if own[targetID] {
			return "", opErrf(409, "模型名称重复: %s", targetID)
		}
		aliases, err := iterateOrDefault(model, "aliases")
		if err != nil {
			return "", err
		}
		if err := validateModelNames(data, targetID, aliases, StringPtr(modelID)); err != nil {
			return "", err
		}
	}
	if options.Aliases != nil {
		aliases, err := normalizeAliases(options.Aliases)
		if err != nil {
			return "", err
		}
		if err := validateModelNames(data, targetID, aliases, StringPtr(modelID)); err != nil {
			return "", err
		}
		model.SetKey("aliases", canonical.NewStringArray(aliases))
	}
	if options.RoutingMode != nil {
		routingMode := strings.TrimSpace(*options.RoutingMode)
		if !isValidRoutingMode(routingMode) {
			return "", opErr(422, "routing_mode 无效")
		}
		model.SetKey("routing_mode", canonical.NewString(routingMode))
	}
	if options.UpdateReasoningEffort {
		effort := ""
		if options.ReasoningEffort != nil {
			effort = strings.TrimSpace(*options.ReasoningEffort)
		}
		if effort == "" || effort == "default" || effort == "downstream" {
			model.DeleteKey("reasoning_effort")
		} else {
			model.SetKey("reasoning_effort", canonical.NewString(effort))
		}
	}
	if options.Targets != nil {
		if err := ValidateTargets(data, options.Targets); err != nil {
			return "", err
		}
		targets, err := ModelTargets(model)
		if err != nil {
			return "", err
		}
		targets.Arr = cloneSlice(options.Targets)
	}
	if targetID != modelID {
		moved, _ := all.LookupOK(modelID)
		all.DeleteKey(modelID)
		all.SetKey(targetID, moved)
		// 改名必须**跟随**到所有引用上。漏掉任何一处，配置层就会以「引用了未配置的
		// 模型」拒绝整次保存——而那正是用户想做的改名；顺手把引用摘掉又等于静默减权。
		if err := ReplaceUnifiedModelName(data, modelID, targetID); err != nil {
			return "", err
		}
		FollowModelRename(data, modelID, targetID)
		FollowModelEndpointRename(data, modelID, targetID)
	}
	if options.Endpoints != nil {
		if err := SetModelEndpoints(data, targetID, options.Endpoints); err != nil {
			return "", err
		}
	}
	return targetID, nil
}

// DeleteModel 删除模型，修好所有指向它的引用（unified_model 与任务），并清理它留下的
// 孤儿供应商 Key。
//
// 对齐 config_operations.py:694；差异只有一处——Python 版把 Key 留给用户自己收拾，这里
// 顺手清掉不再被任何模型绑定的 Key（仍被别的路由绑定的原样保留，见 pruneOrphanProviderKeys）。
func DeleteModel(data *canonical.Value, modelID string) error {
	model, err := RequireModel(data, modelID)
	if err != nil {
		return err
	}
	targets, err := ModelTargets(model)
	if err != nil {
		return err
	}
	return deleteModelWithDroppedTargets(data, modelID, targets.Arr)
}

// deleteModelWithDroppedTargets 删掉模型，并清理 dropped 这批 target 留下的孤儿供应商 Key。
//
// dropped 由调用方给出，而不是在这里现读模型的 targets：DeleteModelTarget 先把 targets 清空
// 才删模型，那一刻模型身上已经没有刚被解绑的那一条了。谁删的谁交出来，两条路径（WebUI 删
// 路由 / TUI 解绑最后一条绑定）才不会一条清、一条留。
func deleteModelWithDroppedTargets(data *canonical.Value, modelID string, dropped []*canonical.Value) error {
	all, err := Models(data)
	if err != nil {
		return err
	}
	all.DeleteKey(modelID)
	if err := pruneOrphanProviderKeys(data, dropped); err != nil {
		return err
	}
	return RepairModelReferences(data)
}

// pruneOrphanProviderKeys 删掉这批 target 引用过、而已经没有模型再引用的供应商 Key。
//
// 「一条路由下没有 target 就不该存在」的另一半：路由没了，它绑定的 Key 也可能没人再用了。
// 判据逐 **Key** 算，不逐路由算：
//
//   - 还挂在别的模型 targets 里的 Key 原样保留。同一把 Key 服务多条路由是常态，顺手删掉
//     它等于把另一条路由的上游凭据删了——而凭据是用户手输的，删了不复原；
//   - 不再被任何模型引用的 Key 一并删掉，否则每删一条路由就在配置里留一把再也调不动、
//     界面上却还看得见的孤儿凭据；
//   - **供应商本身不动**，哪怕它一把 Key 都不剩：空 `keys` 是合法状态（新建的供应商就是
//     这样），而删供应商会连带改写访问密钥的供应商清单，超出「删一条路由」的范围。
//
// 判据复用解绑 Key 那条路径的 providerKeyStillReferenced：同一条规则只有一份实现，两条
// 路径才不会各自漂移成「有的地方清、有的地方留」。
func pruneOrphanProviderKeys(data *canonical.Value, targets []*canonical.Value) error {
	considered := map[[2]string]bool{}
	for _, target := range targets {
		providerID := lookup(target, "provider").StringValue()
		keyName := lookup(target, "key").StringValue()
		// 缺 provider 或 key 的 target 在写路径上早就该被校验挡下；这里跳过而不是报错，
		// 是为了不让一次「清理」把原本能删掉的模型重新变成删不动。
		if providerID == "" || keyName == "" || considered[[2]string{providerID, keyName}] {
			continue
		}
		considered[[2]string{providerID, keyName}] = true
		stillReferenced, err := providerKeyStillReferenced(data, providerID, keyName)
		if err != nil {
			return err
		}
		if stillReferenced {
			continue
		}
		keys := lookup(lookup(data, "providers"), providerID)
		keys = lookup(keys, "keys")
		if !keys.IsObject() || !keys.Obj.Has(keyName) {
			continue
		}
		keys.DeleteKey(keyName)
	}
	return nil
}

// targetIdentity 复刻 target 三元组的 `str()` 归一（config_operations.py:534）。
//
// 缺失键与 null 都得到 "None"，与 Python 的 str(None) 一致。
func targetIdentity(target *canonical.Value) string {
	return pyStrOf(lookup(target, "provider")) + "\x00" +
		pyStrOf(lookup(target, "key")) + "\x00" +
		pyStrOf(lookup(target, "upstream_model"))
}

// ownNames 返回模型自身的别称集合（str() 归一后）。
func ownNames(model *canonical.Value) (map[string]bool, error) {
	result := map[string]bool{}
	aliases, err := iterateOrDefault(model, "aliases")
	if err != nil {
		return nil, err
	}
	for _, name := range aliases {
		if name != "" {
			result[name] = true
		}
	}
	return result, nil
}

// pyItemAssignmentError 复刻对非字典做 `obj["key"] = value` 时的 TypeError。
func pyItemAssignmentError(value *canonical.Value) error {
	if value.IsArray() {
		return &PyError{
			TypeName: "TypeError",
			Message:  "list indices must be integers or slices, not str",
		}
	}
	return &PyError{
		TypeName: "TypeError",
		Message:  fmt.Sprintf("'%s' object does not support item assignment", canonical.PyTypeName(value)),
	}
}

// cloneValues 复刻 `deepcopy(targets or [])`：nil 与空切片都得到空数组。
func cloneValues(targets []*canonical.Value) *canonical.Value {
	return canonical.NewArray(cloneSlice(targets)...)
}

// cloneSlice 深拷贝一组值。
func cloneSlice(values []*canonical.Value) []*canonical.Value {
	cloned := make([]*canonical.Value, 0, len(values))
	for _, value := range values {
		cloned = append(cloned, cloneOf(value))
	}
	return cloned
}

// isValidRoutingMode 报告路由模式是否合法（与 config 包同一份取值）。
func isValidRoutingMode(mode string) bool {
	return mode == "priority" || mode == "round_robin" || mode == "only_first"
}
