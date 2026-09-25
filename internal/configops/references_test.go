package configops

import (
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件固化「模型/供应商清单是引用」这条口径：被引用的目标没了，清单里的那个名字
// 必须跟着摘掉，而不是让整份配置解析不过去。
//
// 与 internal/api/model_cascade_test.go 的分工：那边走 HTTP，管预演与连带变动的**响应**；
// 这里直接打 configops，管清理规则本身（哪些名字算失效、摘空后留什么形状），并钉住
// 别名也算法（清单里写别名是允许的）。

// listFixture 是一份「处处都被引用着」的配置：model-a 被访问密钥清单、工作空间清单与
// 任务同时引用，prov-a 被访问密钥的供应商清单引用。
const listFixture = `{
	"config_version": 4,
	"providers": {
		"prov-a": {"base_url": "https://a.example.test", "keys": {"key-a": {"api_key": "sk-a"}}},
		"prov-b": {"base_url": "https://b.example.test", "keys": {"key-b": {"api_key": "sk-b"}}}
	},
	"models": {
		"model-a": {"aliases": ["alias-a"], "targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}]},
		"model-b": {"aliases": [], "targets": [{"provider": "prov-b", "key": "key-b", "upstream_model": "model-b"}]}
	},
	"tasks": {"shared": {"model": "model-a"}},
	"workspaces": {"teamA": {"models": ["model-a", "model-b"], "tasks": {}}},
	"access_keys": {
		"public": {"name": "公开", "key": "amkr_ak_public", "models": ["model-a", "alias-a", "model-b"], "providers": ["prov-a", "prov-b"]},
		"open": {"name": "不限制", "key": "amkr_ak_open"}
	}
}`

// listNames 取一份清单的字符串项。
func listNames(t *testing.T, container *canonical.Value, key string) ([]string, bool) {
	t.Helper()
	value, present := container.LookupOK(key)
	if !present {
		return nil, false
	}
	if !value.IsArray() {
		t.Fatalf("%s 不是数组", key)
	}
	names := make([]string, 0, len(value.Arr))
	for _, item := range value.Arr {
		names = append(names, item.StringValue())
	}
	return names, true
}

// TestRepairReferenceListsPrunesDanglingNames 固化：摘掉失效的名字，其余原样保留。
func TestRepairReferenceListsPrunesDanglingNames(t *testing.T) {
	data := mustParse(t, listFixture)
	models := mustLookup(t, data, "models")
	models.DeleteKey("model-a")

	if err := RepairReferenceLists(data); err != nil {
		t.Fatalf("修复引用清单失败: %v", err)
	}

	public := mustLookup(t, mustLookup(t, data, "access_keys"), "public")
	names, present := listNames(t, public, "models")
	// `alias-a` 也必须摘掉：它是被删模型的别名，而别名是**可以**写进清单的写法。
	if !present || len(names) != 1 || names[0] != "model-b" {
		t.Errorf("访问密钥模型清单 = %v（present=%v），期望只剩 model-b", names, present)
	}
	if providers, _ := listNames(t, public, "providers"); len(providers) != 2 {
		t.Errorf("没有供应商被删，providers 清单不该动: %v", providers)
	}
	teamA := mustLookup(t, mustLookup(t, data, "workspaces"), "teamA")
	if names, _ := listNames(t, teamA, "models"); len(names) != 1 || names[0] != "model-b" {
		t.Errorf("工作空间直呼清单 = %v，期望只剩 model-b", names)
	}
}

// TestRepairReferenceListsKeepsEmptyArray 固化：摘空后保留空数组，不删字段。
//
// 这两份清单是三态的：字段缺失或 null 表示「不限制」，`[]` 表示「一个都不许」。
// 删字段等于把一条禁令松成不限制——那是**扩权**，比留下一个调不动任何模型的空清单危险得多。
func TestRepairReferenceListsKeepsEmptyArray(t *testing.T) {
	data := mustParse(t, listFixture)
	models := mustLookup(t, data, "models")
	models.DeleteKey("model-a")
	models.DeleteKey("model-b")
	providers := mustLookup(t, data, "providers")
	providers.DeleteKey("prov-a")
	providers.DeleteKey("prov-b")

	if err := RepairReferenceLists(data); err != nil {
		t.Fatalf("修复引用清单失败: %v", err)
	}

	public := mustLookup(t, mustLookup(t, data, "access_keys"), "public")
	for _, key := range []string{"models", "providers"} {
		names, present := listNames(t, public, key)
		if !present {
			t.Errorf("%s 被摘空后应保留字段（删字段等于松成「不限制」）", key)
			continue
		}
		if len(names) != 0 {
			t.Errorf("%s = %v，期望空数组", key, names)
		}
	}
	// 本来就没写清单的那把密钥不该被凭空补上一份空清单。
	open := mustLookup(t, mustLookup(t, data, "access_keys"), "open")
	if open.Obj.Has("models") || open.Obj.Has("providers") {
		t.Errorf("未配置清单的访问密钥不应被补上空清单: %v", open)
	}
}

// TestRepairReferenceListsLeavesUnrelatedProvidersAlone 固化：只摘失效的供应商。
func TestRepairReferenceListsLeavesUnrelatedProvidersAlone(t *testing.T) {
	data := mustParse(t, listFixture)
	mustLookup(t, data, "providers").DeleteKey("prov-a")

	if err := RepairReferenceLists(data); err != nil {
		t.Fatalf("修复引用清单失败: %v", err)
	}

	public := mustLookup(t, mustLookup(t, data, "access_keys"), "public")
	if providers, _ := listNames(t, public, "providers"); len(providers) != 1 || providers[0] != "prov-b" {
		t.Errorf("供应商清单 = %v，期望只剩 prov-b", providers)
	}
}

// TestDeleteProviderKeyNoLongerBlockedByAccessKeyList 固化端到端后果：删掉一把 Key 之后
// 整份配置仍然解析得过——这正是原先被 `access_keys.public.models[0] 引用了未配置的模型`
// 挡住的那一步。
func TestDeleteProviderKeyNoLongerBlockedByAccessKeyList(t *testing.T) {
	data := mustParse(t, listFixture)

	if _, err := DeleteProviderKey(data, "prov-a", "key-a"); err != nil {
		t.Fatalf("删除供应商 Key 失败: %v", err)
	}
	if _, err := config.FromDict(data); err != nil {
		t.Fatalf("连带清理后配置仍解析失败: %v", err)
	}

	// model-a 失去唯一目标 → 被删；引用它的任务、清单一起清掉。
	if mustLookup(t, data, "models").Obj.Has("model-a") {
		t.Error("失去全部目标的模型应被删除")
	}
	if tasks := lookup(data, "tasks"); tasks != nil && tasks.Obj.Has("shared") {
		t.Error("引用已删模型的任务应被清理")
	}
	public := mustLookup(t, mustLookup(t, data, "access_keys"), "public")
	names, present := listNames(t, public, "models")
	if !present || len(names) != 1 || names[0] != "model-b" {
		t.Errorf("访问密钥模型清单 = %v（present=%v），期望只剩 model-b", names, present)
	}
	// 供应商清单里 prov-a 已经不存在了（最后一个 Key 被删，供应商随之消失）。
	providers, present := listNames(t, public, "providers")
	if !present || len(providers) != 1 || providers[0] != "prov-b" {
		t.Errorf("供应商清单 = %v（present=%v），期望只剩 prov-b", providers, present)
	}
}
