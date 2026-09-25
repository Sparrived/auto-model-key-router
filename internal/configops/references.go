package configops

import (
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件管「模型/供应商清单」这类**引用**：`access_keys.<id>.models`、
// `access_keys.<id>.providers` 与 `workspaces.<空间>.models`。
//
// 它们与 tasks、unified_model 是同一类东西——写的是别处的名字。被引用的目标没了，
// 留着这个名字整份配置就再也解析不过去（config.parseAccessKeyModels /
// parseWorkspaceModels 直接报「引用了未配置的模型」），于是「删掉一个没人用的模型」
// 会被一句引用错误顶回来：用户想删的恰恰是那个模型，却因为一份清单里还写着它而删不动。
//
// 这里负责把失效的名字摘掉（RepairReferenceLists）与陪着改名一起走（FollowModelRename /
// FollowProviderRename）；「动手之前先算出会摘掉谁」在 impact.go。

// RepairReferenceLists 摘掉配置里指向已不存在模型/供应商的名字。
//
// 摘空之后**保留空数组**而不删字段：本项目的清单是三态的——字段缺失或 null 表示
// 「不限制」，`[]` 表示「一个都不许」（见 config.AccessKeyConfig）。删字段等于把禁令
// 松开，那是扩权而不是收权；留成 `[]` 至少收得住，用户看得见也能改回来。
func RepairReferenceLists(data *canonical.Value) error {
	knownModels, err := knownModelNames(data)
	if err != nil {
		return err
	}
	knownProviders := map[string]bool{}
	for _, pair := range objectItems(lookup(data, "providers")) {
		knownProviders[pair.Key] = true
	}

	for _, pair := range objectItems(lookup(data, "access_keys")) {
		if !pair.Value.IsObject() {
			continue
		}
		pruneNameList(lookup(pair.Value, "models"), knownModels)
		pruneNameList(lookup(pair.Value, "providers"), knownProviders)
	}
	for _, pair := range objectItems(lookup(data, "workspaces")) {
		if !pair.Value.IsObject() {
			continue
		}
		pruneNameList(lookup(pair.Value, "models"), knownModels)
	}
	return nil
}

// knownModelNames 返回所有可被引用的模型名（真实 ID + 别名）。
//
// 别名必须算数：清单里写别名是允许的（parseAccessKeyModels 与 workspaces.models
// 同一口径，调用方就是用这些名字请求的），只认 ID 会把一份正确的配置改坏。
func knownModelNames(data *canonical.Value) (map[string]bool, error) {
	known := map[string]bool{}
	for _, pair := range objectItems(lookup(data, "models")) {
		known[pair.Key] = true
		aliases, err := iterateOrEmpty(pair.Value, "aliases")
		if err != nil {
			return nil, err
		}
		for _, alias := range aliases {
			known[alias] = true
		}
	}
	return known, nil
}

// pruneNameList 就地摘掉数组里不在 known 中的名字。
//
// 只处理**数组**：字段缺失 / null 是「不限制」，没有名字可摘；其它类型原样留着，
// 交给 config 层去报「必须是数组」——在这里顺手改形状会把一处配置错误变成一个
// 静默的行为变更。
//
// 空串与非字符串同样算「指向不了任何东西」，一并摘掉。
func pruneNameList(list *canonical.Value, known map[string]bool) {
	if list == nil || !list.IsArray() {
		return
	}
	kept := make([]*canonical.Value, 0, len(list.Arr))
	for _, item := range list.Arr {
		// 比对前 trim：config 层解析清单时也 trim，带上空白的名字是能用的引用，
		// 原样保留（这里只判断「指向得住吗」，不顺手改写用户的写法）。
		if item.Kind == canonical.KindString && known[strings.TrimSpace(item.Str)] {
			kept = append(kept, item)
		}
	}
	if len(kept) != len(list.Arr) {
		list.Arr = kept
	}
}

// FollowModelRename 把清单与任务里对旧模型名的引用改成新名字。
//
// 「跟随」而不是「清理」：模型只是换了个名字，引用它的那把访问密钥并没有失去权限。
// 留着旧名字配置层会以「引用了未配置的模型」拒绝整次保存；摘掉清单项则等于把一次改名
// 变成一次静默减权。两种都不对，正确的动作是把引用一起改名。
//
// unified_model 的跟随由 ReplaceUnifiedModelName 负责（它要处理 primary/fallback 的结构
// 与迁移副本），这里管 access_keys / workspaces 的模型清单与任务的 model / fallback_model。
//
// 只改**与旧 ID 完全相同**的那些名字：清单与任务里允许写别名，而别名不随 ID 改名失效，
// 跟着改反而会把一个仍然有效的别名改没。
func FollowModelRename(data *canonical.Value, oldName, newName string) {
	for _, pair := range objectItems(lookup(data, "access_keys")) {
		if pair.Value.IsObject() {
			renameListItems(lookup(pair.Value, "models"), oldName, newName)
		}
	}
	for _, pair := range objectItems(lookup(data, "workspaces")) {
		if !pair.Value.IsObject() {
			continue
		}
		renameListItems(lookup(pair.Value, "models"), oldName, newName)
		renameTaskModels(lookup(pair.Value, "tasks"), oldName, newName)
	}
	renameTaskModels(lookup(data, "tasks"), oldName, newName)
}

// FollowProviderRename 把访问密钥的供应商清单里的旧供应商 ID 改成新 ID。
//
// 与模型清单同一件事：供应商只是换了个名字，那把密钥仍然打得动这个上游，摘掉清单项就是
// 静默减权。模型 target 里的 `provider` 由 UpdateProvider 自己改写（那份要连带处理
// targets 结构），这里只管访问密钥清单。
func FollowProviderRename(data *canonical.Value, oldName, newName string) {
	for _, pair := range objectItems(lookup(data, "access_keys")) {
		if pair.Value.IsObject() {
			renameListItems(lookup(pair.Value, "providers"), oldName, newName)
		}
	}
}

// renameListItems 就地改写数组里等于旧名字的字符串项。
//
// 比对前 trim（config 层解析清单时也 trim，带空白的名字是能用的引用），写回时用干净的新
// 名字——旧名字已经不存在了，保留空白只是留个噪声。
func renameListItems(list *canonical.Value, oldName, newName string) {
	if list == nil || !list.IsArray() {
		return
	}
	for index, item := range list.Arr {
		if item.Kind == canonical.KindString && strings.TrimSpace(item.Str) == oldName {
			list.Arr[index] = canonical.NewString(newName)
		}
	}
}

// renameTaskModels 就地改写一组任务里的 model / fallback_model。
func renameTaskModels(tasks *canonical.Value, oldName, newName string) {
	for _, pair := range objectItems(tasks) {
		if !pair.Value.IsObject() {
			continue
		}
		for _, field := range []string{"model", "fallback_model"} {
			value := lookup(pair.Value, field)
			if value != nil && value.Kind == canonical.KindString && strings.TrimSpace(value.Str) == oldName {
				pair.Value.SetKey(field, canonical.NewString(newName))
			}
		}
	}
}
