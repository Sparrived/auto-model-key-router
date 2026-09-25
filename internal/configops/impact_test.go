package configops

import "testing"

// 本文件固化预演的结果：一次会连带改动的写操作，要能逐项报出它会摘掉谁。
//
// 与 references_test.go 的分工：那边管**清理规则**本身（哪些名字算失效、摘空后留什么
// 形状），这里管**把它们报出来**。预演与真写共用同一段 mutation，所以这里对比的是
// 「同一份配置在两个时刻的差」，而不是另写一遍判定规则。

// TestModelEditImpactOfReportsCascade 固化预演的结果：连带改动要能逐项报出来。
func TestModelEditImpactOfReportsCascade(t *testing.T) {
	before := mustParse(t, listFixture)
	after := mustParse(t, listFixture)

	if _, err := DeleteProviderKey(after, "prov-a", "key-a"); err != nil {
		t.Fatalf("删除供应商 Key 失败: %v", err)
	}
	impact := ModelEditImpactOf(before, after)

	if len(impact.RemovedModels) != 1 || impact.RemovedModels[0] != "model-a" {
		t.Errorf("被删模型 = %v，期望 [model-a]", impact.RemovedModels)
	}
	if len(impact.AccessKeys) != 1 {
		t.Fatalf("应报告 public 一把密钥的清单变动: %+v", impact.AccessKeys)
	}
	change := impact.AccessKeys[0]
	if change.ID != "public" || change.Name != "公开" {
		t.Errorf("标识/显示名不符: %+v", change)
	}
	// `alias-a` 与被删模型一起失效，因此要一并报出来。
	if len(change.Models) != 2 || change.Models[0] != "alias-a" || change.Models[1] != "model-a" {
		t.Errorf("被摘掉的模型名 = %v，期望 [alias-a model-a]", change.Models)
	}
	if len(change.Providers) != 1 || change.Providers[0] != "prov-a" {
		t.Errorf("被摘掉的供应商 = %v，期望 [prov-a]", change.Providers)
	}
	if len(impact.Workspaces) != 1 || impact.Workspaces[0].ID != "teamA" ||
		len(impact.Workspaces[0].Models) != 1 {
		t.Errorf("工作空间清单变动不符: %+v", impact.Workspaces)
	}
	if len(impact.RemovedTasks) != 1 || impact.RemovedTasks[0] != "shared" {
		t.Errorf("被删任务 = %v，期望 [shared]", impact.RemovedTasks)
	}
	if impact.UnifiedModel {
		t.Error("这份配置没有 unified_model，不该报告它被改写")
	}
}

// TestModelEditImpactOfReportsCleared 固化：清单被摘空时要单独标出来。
//
// 「移除最后一项」与「回到不限制」在界面上是相反的结论，确认框必须能说清是前者。
func TestModelEditImpactOfReportsCleared(t *testing.T) {
	before := mustParse(t, listFixture)
	after := mustParse(t, listFixture)
	// 删掉两个模型：public 的模型清单被摘空，teamA 的也一样。
	models := mustLookup(t, after, "models")
	models.DeleteKey("model-a")
	models.DeleteKey("model-b")
	if _, err := DeleteProviderKey(after, "prov-b", "key-b"); err != nil {
		t.Fatalf("删除供应商 Key 失败: %v", err)
	}

	impact := ModelEditImpactOf(before, after)
	if len(impact.AccessKeys) != 1 || !impact.AccessKeys[0].ModelsCleared {
		t.Errorf("访问密钥模型清单应报成被摘空: %+v", impact.AccessKeys)
	}
	if len(impact.Workspaces) != 1 || !impact.Workspaces[0].ModelsCleared {
		t.Errorf("工作空间清单应报成被摘空: %+v", impact.Workspaces)
	}
}

// TestTaskIdentitiesQualifyNamedWorkspace 固化任务标识的形状：命名空间带 `空间/` 前缀。
//
// 两个空间可能有同名任务，确认框要能说清被删的是哪一个。
func TestTaskIdentitiesQualifyNamedWorkspace(t *testing.T) {
	data := mustParse(t, `{
		"models": {},
		"tasks": {"shared": {"model": ""}},
		"workspaces": {"teamA": {"models": [], "tasks": {"shared": {"model": ""}}}}
	}`)
	identities := taskIdentities(data)
	if len(identities) != 2 || identities[0] != "shared" || identities[1] != "teamA/shared" {
		t.Errorf("任务标识 = %v，期望 [shared teamA/shared]", identities)
	}
}
