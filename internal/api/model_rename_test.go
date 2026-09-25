package api

import (
	"net/http"
	"slices"
	"testing"
)

// 本文件固化「改名要跟随到引用上」这条写路径在管理接口这一层的接线。
//
// 「跟随」而不是「清理」：模型/供应商只是换了个名字，引用它的那把访问密钥并没有失去权限。
// 留着旧名字，配置层会以「引用了未配置的模型/供应商」拒绝整次保存——而那正是用户想做的
// 改名；顺手把引用摘掉又等于把一次改名变成一次静默减权。
//
// 逐字段的跟随规则由 internal/configops 的用例钉住，这里只管**接线**：管理接口确实走到
// 了那条改名路径（而不是先被校验卡住），落盘的配置里也看不到旧名字。
// 夹具与读取助手复用 model_cascade_test.go 的 cascadeFixture。

// TestRenameModelFollowsReferences 固化：PUT /api/routes/{id} 带新 id 时引用跟着改名。
func TestRenameModelFollowsReferences(t *testing.T) {
	server, path := cascadeServer(t)

	recorder := callCascade(t, server, http.MethodPut, "/api/routes/model-a",
		`{"config_revision":"`+currentRevision(t, path)+`","id":"model-c"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("改名状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}

	data := readConfig(t, path)
	if names := modelNamesIn(t, data); !slices.Equal(names, []string{"model-b", "model-c"}) {
		t.Errorf("落盘后的模型 = %v，期望 [model-b model-c]", names)
	}
	public := nestedObject(t, data, "access_keys", "public")
	if models, _ := stringList(t, public, "models"); !slices.Equal(models, []string{"model-c", "model-b"}) {
		t.Errorf("访问密钥模型清单 = %v，期望 [model-c model-b]", models)
	}
	teamA := nestedObject(t, data, "workspaces", "teamA")
	if models, _ := stringList(t, teamA, "models"); !slices.Equal(models, []string{"model-c", "model-b"}) {
		t.Errorf("工作空间直呼清单 = %v，期望 [model-c model-b]", models)
	}
	tasks, _ := data["tasks"].(map[string]any)
	shared, _ := tasks["shared"].(map[string]any)
	if shared["model"] != "model-c" {
		t.Errorf("任务 shared 的 model = %v，期望 model-c", shared["model"])
	}
	primary := nestedObject(t, data, "unified_model", "default", "primary")
	if primary["model"] != "model-c" {
		t.Errorf("unified_model.default.primary.model = %v，期望 model-c", primary["model"])
	}
}

// TestRenameProviderFollowsAccessKeyList 固化：改供应商名时访问密钥的供应商清单跟着改名。
//
// 这条路径就是供应商页「编辑」表单里的改名：改名一个被访问密钥清单写着的供应商，原先会
// 以 `access_keys.public.providers[0] 引用了未配置的供应商` 拒绝整次保存。
func TestRenameProviderFollowsAccessKeyList(t *testing.T) {
	server, path := cascadeServer(t)

	recorder := callCascade(t, server, http.MethodPut, "/api/providers/prov-a",
		`{"config_revision":"`+currentRevision(t, path)+`","id":"prov-c"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("改名状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}

	data := readConfig(t, path)
	public := nestedObject(t, data, "access_keys", "public")
	// 跟随改名，而不是被摘掉：那把密钥仍然打得动这个上游。
	if providers, _ := stringList(t, public, "providers"); !slices.Equal(providers, []string{"prov-c"}) {
		t.Errorf("供应商清单 = %v，期望 [prov-c]", providers)
	}
	// 模型 target 里的 provider 也跟了（既有行为，别被这条路径碰坏）。
	modelA := nestedObject(t, data, "models", "model-a")
	targets, _ := modelA["targets"].([]any)
	target, _ := targets[0].(map[string]any)
	if target["provider"] != "prov-c" {
		t.Errorf("模型 target 的 provider = %v，期望 prov-c", target["provider"])
	}
}
