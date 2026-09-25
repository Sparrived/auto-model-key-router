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
// 供 WebUI 弹二次确认用（见 webui/model-impact.js）。清理本身在 references.go。

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

// ModelEditImpact 描述一次模型写操作**连带**改掉的引用。
//
// 它是给二次确认用的：用户动的是一个模型，被连带改掉的却可能是别的资源（某把访问密钥
// 少一个模型、某个任务被删）。不先说清楚，这些变动就只能靠事后翻配置才发现。
type ModelEditImpact struct {
	// RemovedModels 是因为失去全部目标而被删除的模型（升序）。
	RemovedModels []string
	// AccessKeys 是清单被摘掉条目的访问密钥（按 key_id 升序）。
	AccessKeys []ReferenceListChange
	// Workspaces 是直呼模型清单被摘掉条目的工作空间（按名称升序）。
	Workspaces []ReferenceListChange
	// RemovedTasks 是被连带删除的任务（命名空间用 `空间/任务名`，默认空间按原名，升序）。
	RemovedTasks []string
	// UnifiedModel 报告 unified_model 是否被一并改写。
	UnifiedModel bool
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
		RemovedTasks: removedNames(taskIdentities(before), taskIdentities(after)),
		AccessKeys:   accessKeyListChanges(before, after),
		Workspaces:   workspaceListChanges(before, after),
		UnifiedModel: !pyEqualValues(lookup(before, "unified_model"), lookup(after, "unified_model")),
	}
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
