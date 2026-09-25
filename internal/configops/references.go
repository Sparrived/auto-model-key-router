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
// 这里负责把失效的名字摘掉；「动手之前先算出会摘掉谁」在 impact.go。

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
