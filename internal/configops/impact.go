package configops

import (
	"sort"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件负责「动手之前先算出会改掉谁」：`access_keys` / `workspaces` 的清单引用只是
// 连带变动的一部分（还有任务与 unified_model），而且判定规则分散在 RepairReferenceLists /
// RepairTasks / RepairUnifiedModel 三处、各有边界情形。所以这里不去重写规则，而是
// **对比一次写操作前后的两份配置**——预演看到的就是真写会发生的。
//
// 供 WebUI 弹二次确认用（见 web/src/lib/model-impact.ts）。清理本身在 references.go。

// ReferenceListChange 描述一次写操作从一份清单里摘掉了哪些名字。
type ReferenceListChange struct {
	// ID 是资源标识：访问密钥的 key_id，或工作空间名。
	ID string
	// Name 是给人看的显示名（访问密钥的 name；工作空间与 ID 相同）。
	Name string
	// Models / Providers 是被摘掉的名字（升序）。
	Models    []string
	Providers []string
	// ModelsCleared / ProvidersCleared 报告这份清单是否被摘空。摘空**不等于**「不限制」：
	// 空数组在配置里的含义是「一个都不许」，两者对调用方是相反的结论。
	ModelsCleared    bool
	ProvidersCleared bool
}

// RemovedKey 是一把被本次写操作清理掉的供应商 Key。
type RemovedKey struct {
	Provider string
	Key      string
}

// ModelEditImpact 描述一次模型写操作**连带**改掉的引用。
//
// 它是给二次确认用的：用户动的是一个模型，被连带改掉的却可能是别的资源（某把访问密钥
// 少一个模型、某个任务被删）。不先说清楚，这些变动就只能靠事后翻配置才发现。
type ModelEditImpact struct {
	// RemovedModels 是因为失去全部目标而被删除的模型（升序）。
	RemovedModels []string
	// RemovedKeys 是因为失去最后一个模型绑定而被清理的供应商 Key（按 provider、key 升序）。
	//
	// 与 RemovedModels 同一性质的东西，只是位置在 providers 那一段：删掉一条路由会顺手
	// 清掉它留下的孤儿凭据，而凭据是用户手输的、删了不复原——所以它必须出现在预演里。
	// 仍被其他模型绑定的 Key 不在这里：那是「保留」，不是「变动」。
	RemovedKeys []RemovedKey
	// AccessKeys 是清单被摘掉条目的访问密钥（按 key_id 升序）。
	AccessKeys []ReferenceListChange
	// Workspaces 是直呼模型清单被摘掉条目的工作空间（按名称升序）。
	Workspaces []ReferenceListChange
	// RemovedTasks 是被连带删除的任务（命名空间用 `空间/任务名`，默认空间按原名，升序）。
	RemovedTasks []string
	// UnifiedModel 报告 unified_model 是否被一并改写。
	UnifiedModel bool
	// EndpointPlans 报告是否有模型的端点计划被一并改写：被删的模型被别的模型的端点
	// 计划引用时，那些端点会被移除（该模型不再按那个端点分派）。
	EndpointPlans bool
}

// ModelEditImpactOf 对比一次写操作前后的配置，算出它连带改掉了哪些引用。
//
// 两侧都必须是**迁移后**的配置（`after` 还得是施加过 mutation 的那一份），否则
// 「迁移本身带来的改动」会被当成这次写操作的影响。
func ModelEditImpactOf(before, after *canonical.Value) ModelEditImpact {
	return ModelEditImpact{
		RemovedModels: removedNames(
			objectKeys(lookup(before, "models")),
			objectKeys(lookup(after, "models")),
		),
		RemovedKeys:   removedProviderKeys(before, after),
		RemovedTasks:  removedNames(taskIdentities(before), taskIdentities(after)),
		AccessKeys:    accessKeyListChanges(before, after),
		Workspaces:    workspaceListChanges(before, after),
		UnifiedModel:  !pyEqualValues(lookup(before, "unified_model"), lookup(after, "unified_model")),
		EndpointPlans: endpointPlansChanged(before, after),
	}
}

// removedProviderKeys 算出本次写操作清理掉的供应商 Key（升序）。
//
// 只报「供应商还在、Key 没了」的那些。整个供应商一起消失时（删供应商、删掉它的最后一把
// Key）它的 Key 是删除的**主体**而不是连带，逐把罗列只会把确认框塞满；而删一条路由顺手
// 清掉的孤儿 Key 恰恰相反——用户根本没提过它，不说出来就等于悄悄删掉一把凭据。
func removedProviderKeys(before, after *canonical.Value) []RemovedKey {
	removed := []RemovedKey{}
	for _, pair := range objectItems(lookup(before, "providers")) {
		beforeKeys := lookup(pair.Value, "keys")
		afterProvider := lookup(lookup(after, "providers"), pair.Key)
		if !beforeKeys.IsObject() || !afterProvider.IsObject() {
			continue
		}
		afterKeys := lookup(afterProvider, "keys")
		for _, keyName := range objectKeys(beforeKeys) {
			if !afterKeys.IsObject() || !afterKeys.Obj.Has(keyName) {
				removed = append(removed, RemovedKey{Provider: pair.Key, Key: keyName})
			}
		}
	}
	sort.Slice(removed, func(i, j int) bool {
		if removed[i].Provider != removed[j].Provider {
			return removed[i].Provider < removed[j].Provider
		}
		return removed[i].Key < removed[j].Key
	})
	return removed
}

// endpointPlansChanged 报告两次配置里是否有模型的端点计划不同。
//
// 只比 endpoints 这一段：模型本身的增删改由 RemovedModels 与调用方自己说明，这里回答的
// 是「有别的模型的端点分派被连带改掉了」——那是最容易事后才发现的一种连带变动。
func endpointPlansChanged(before, after *canonical.Value) bool {
	endpointsOf := func(data *canonical.Value) map[string]*canonical.Value {
		result := map[string]*canonical.Value{}
		for _, pair := range objectItems(lookup(data, "models")) {
			if !pair.Value.IsObject() {
				continue
			}
			if endpoints := lookup(pair.Value, "endpoints"); endpoints != nil {
				result[pair.Key] = endpoints
			}
		}
		return result
	}
	left, right := endpointsOf(before), endpointsOf(after)
	if len(left) != len(right) {
		return true
	}
	for name, value := range left {
		other, found := right[name]
		if !found || !pyEqualValues(value, other) {
			return true
		}
	}
	return false
}

// accessKeyListChanges 算出每把访问密钥被摘掉的名字。
func accessKeyListChanges(before, after *canonical.Value) []ReferenceListChange {
	changes := []ReferenceListChange{}
	for _, pair := range objectItems(lookup(before, "access_keys")) {
		current := lookup(lookup(after, "access_keys"), pair.Key)
		// 整把密钥在本次写操作里消失（不是本次的用况）时没有「清单被摘」可言。
		if !pair.Value.IsObject() || !current.IsObject() {
			continue
		}
		change := ReferenceListChange{ID: pair.Key, Name: entryDisplayName(pair.Value, pair.Key)}
		change.Models, change.ModelsCleared = listRemovals(
			lookup(pair.Value, "models"), lookup(current, "models"))
		change.Providers, change.ProvidersCleared = listRemovals(
			lookup(pair.Value, "providers"), lookup(current, "providers"))
		if len(change.Models) == 0 && len(change.Providers) == 0 {
			continue
		}
		changes = append(changes, change)
	}
	sortReferenceChanges(changes)
	return changes
}

// workspaceListChanges 算出每个工作空间被摘掉的模型。
func workspaceListChanges(before, after *canonical.Value) []ReferenceListChange {
	changes := []ReferenceListChange{}
	for _, pair := range objectItems(lookup(before, "workspaces")) {
		current := lookup(lookup(after, "workspaces"), pair.Key)
		if !pair.Value.IsObject() || !current.IsObject() {
			continue
		}
		change := ReferenceListChange{ID: pair.Key, Name: pair.Key}
		change.Models, change.ModelsCleared = listRemovals(
			lookup(pair.Value, "models"), lookup(current, "models"))
		if len(change.Models) == 0 {
			continue
		}
		changes = append(changes, change)
	}
	sortReferenceChanges(changes)
	return changes
}

// listRemovals 返回 before 清单里有、after 清单里没有的名字（升序），以及清单是否被摘空。
//
// before 不是数组（= 不限制，本来就没有名字）时返回 (nil, false)：从「不限制」变成
// 某种限制不是这里发生的事。
func listRemovals(beforeList, afterList *canonical.Value) ([]string, bool) {
	if beforeList == nil || !beforeList.IsArray() || len(beforeList.Arr) == 0 {
		return nil, false
	}
	kept := map[string]bool{}
	if afterList.IsArray() {
		for _, item := range afterList.Arr {
			if item.Kind == canonical.KindString {
				kept[item.Str] = true
			}
		}
	}
	removed := map[string]bool{}
	for _, item := range beforeList.Arr {
		if item.Kind == canonical.KindString && !kept[item.Str] {
			removed[item.Str] = true
		}
	}
	return sortedSet(removed), afterList.IsArray() && len(afterList.Arr) == 0
}

// taskIdentities 返回配置里所有任务的标识（升序去重）。
//
// 命名工作空间用 `空间/任务名` 限定，与 RepairTasks 报出的名字同形：两个空间可能有
// 同名任务，不限定就无法在确认框里说清被删的是哪一个。
func taskIdentities(data *canonical.Value) []string {
	seen := map[string]bool{}
	collect := func(container *canonical.Value, workspace string) {
		qualify := workspace != "" && workspace != config.DefaultWorkspace
		for _, name := range objectKeys(container) {
			identity := name
			if qualify {
				identity = workspace + "/" + name
			}
			seen[identity] = true
		}
	}
	collect(lookup(data, "tasks"), config.DefaultWorkspace)
	for _, pair := range objectItems(lookup(data, "workspaces")) {
		collect(lookup(pair.Value, "tasks"), pair.Key)
	}
	return sortedSet(seen)
}

// removedNames 返回 before 有而 after 没有的名字（升序）。
func removedNames(before, after []string) []string {
	remaining := map[string]bool{}
	for _, name := range after {
		remaining[name] = true
	}
	removed := map[string]bool{}
	for _, name := range before {
		if !remaining[name] {
			removed[name] = true
		}
	}
	return sortedSet(removed)
}

// entryDisplayName 取清单所属资源的显示名，缺失时回落到 ID。
func entryDisplayName(entry *canonical.Value, fallback string) string {
	if name := strings.TrimSpace(lookup(entry, "name").StringValue()); name != "" {
		return name
	}
	return fallback
}

// sortReferenceChanges 按 ID 升序排列，让响应与确认框的顺序稳定可复现。
func sortReferenceChanges(changes []ReferenceListChange) {
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
}
