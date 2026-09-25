package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// 本文件固化「改模型会连带改掉别处引用」这条写路径的两半：
//
//  1. 连带改动必须**自动完成**，而不是让保存被一句「引用了未配置的模型」挡回来——
//     用户想删的恰恰是那个模型，却因为一份清单里还写着它而删不动；
//  2. 动手之前必须能**问清楚**：`dry_run=1` 返回这次会连带摘掉谁，且一个字节都不落盘。
//
// 两类引用各锁一条：`access_keys.models`（模型清单）与 `access_keys.providers`
// （供应商清单）。后者是删供应商时才会失效的那种——同一个坑的另一个入口。

// cascadeFixture 是一份"处处都被引用着"的配置：model-a 被任务、工作空间清单、
// 访问密钥清单与 unified_model 同时引用，因此删它必然牵动一大片。
const cascadeFixture = `{
  "config_version": 4,
  "local_api_key": "local-key",
  "ops_enabled": false,
  "webui_enabled": false,
  "host": "127.0.0.1",
  "port": 8000,
  "endpoint_capabilities_path": "caps.json",
  "metrics_db_path": "metrics.sqlite3",
  "log_file_path": "server.log",
  "providers": {
    "prov-a": {"base_url": "https://a.example.test", "keys": {"key-a": {"api_key": "sk-a"}, "key-b": {"api_key": "sk-b"}}}
  },
  "models": {
    "model-a": {"targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}]},
    "model-b": {"targets": [{"provider": "prov-a", "key": "key-b", "upstream_model": "model-b"}]}
  },
  "tasks": {"shared": {"model": "model-a"}},
  "workspaces": {"teamA": {"models": ["model-a", "model-b"], "tasks": {"only-a": {"model": "model-a"}}}},
  "access_keys": {"public": {"name": "公开", "key": "amkr_ak_public", "models": ["model-a", "model-b"], "providers": ["prov-a"]}},
  "unified_model": {"default": {"primary": {"model": "model-a", "key": null}}}
}`

// cascadeServer 装配一个指向临时配置文件的 Server，返回它与配置文件路径。
func cascadeServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")
	if err := os.WriteFile(path, []byte(cascadeFixture), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return &Server{ConfigPath: path}, path
}

// callCascade 向管理 API 发一条请求（工作空间无关，凭据固定用本地 key）。
func callCascade(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callTasks(t, server, method, path, "", body)
}

// dryRunImpact 是预演响应里被断言到的那部分。
type dryRunImpact struct {
	DryRun        bool     `json:"dry_run"`
	RemovedModels []string `json:"removed_models"`
	AccessKeys    []struct {
		ID               string   `json:"id"`
		Name             string   `json:"name"`
		Models           []string `json:"models"`
		Providers        []string `json:"providers"`
		ModelsCleared    bool     `json:"models_cleared"`
		ProvidersCleared bool     `json:"providers_cleared"`
	} `json:"access_keys"`
	Workspaces []struct {
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		Models        []string `json:"models"`
		ModelsCleared bool     `json:"models_cleared"`
	} `json:"workspaces"`
	RemovedTasks   []string `json:"removed_tasks"`
	UnifiedModel   bool     `json:"unified_model"`
	ConfigRevision string   `json:"config_revision"`
}

// decodeImpact 解析预演响应。
func decodeImpact(t *testing.T, recorder *httptest.ResponseRecorder) dryRunImpact {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("预演状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var impact dryRunImpact
	if err := json.Unmarshal(recorder.Body.Bytes(), &impact); err != nil {
		t.Fatalf("解析预演响应失败: %v（body=%s）", err, recorder.Body.String())
	}
	return impact
}

// readConfig 读回磁盘上的配置。
func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}
	return data
}

// nestedObject 取嵌套对象，缺失时失败。
func nestedObject(t *testing.T, root map[string]any, keys ...string) map[string]any {
	t.Helper()
	current := root
	for _, key := range keys {
		child, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("配置里缺少对象 %v（当前层=%v）", keys, current)
		}
		current = child
	}
	return current
}

// modelNamesIn 返回 models 段的键（升序无关，只用于断言存在与否）。
func modelNamesIn(t *testing.T, root map[string]any) []string {
	t.Helper()
	models, _ := root["models"].(map[string]any)
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// stringList 把配置里的一份清单读成 []string；字段不存在返回 (nil, false)。
func stringList(t *testing.T, container map[string]any, key string) ([]string, bool) {
	t.Helper()
	raw, present := container[key]
	if !present {
		return nil, false
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s 不是数组: %v", key, raw)
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("%s 里出现非字符串: %v", key, item)
		}
		values = append(values, text)
	}
	return values, true
}

// TestDryRunKeyModelsReportsCascade 固化：取消勾选一个只绑在这把 Key 上的模型时，
// 预演要一次说清四个去处（被删的模型、访问密钥清单、工作空间清单、任务），并且不落盘。
func TestDryRunKeyModelsReportsCascade(t *testing.T) {
	server, path := cascadeServer(t)
	before := readConfig(t, path)
	revision := currentRevision(t, path)

	// key-a 只服务 model-a，把它的服务模型改成 model-b 等于删掉 model-a。
	recorder := callCascade(t, server, http.MethodPut,
		"/api/providers/prov-a/keys/key-a/models?dry_run=1",
		`{"config_revision":"`+revision+`","models":["model-b"]}`)
	impact := decodeImpact(t, recorder)

	if !impact.DryRun {
		t.Error("预演响应必须带 dry_run=true")
	}
	if impact.ConfigRevision != revision {
		t.Errorf("预演响应的版本号 = %q，期望未改动的 %q", impact.ConfigRevision, revision)
	}
	if !slices.Equal(impact.RemovedModels, []string{"model-a"}) {
		t.Errorf("被删模型 = %v，期望 [model-a]", impact.RemovedModels)
	}
	if len(impact.AccessKeys) != 1 || impact.AccessKeys[0].ID != "public" ||
		!slices.Equal(impact.AccessKeys[0].Models, []string{"model-a"}) {
		t.Errorf("访问密钥清单变动不符: %+v", impact.AccessKeys)
	}
	if impact.AccessKeys[0].ModelsCleared {
		t.Error("访问密钥还留着 model-b，清单不该报成「被摘空」")
	}
	if len(impact.Workspaces) != 1 || impact.Workspaces[0].ID != "teamA" ||
		!slices.Equal(impact.Workspaces[0].Models, []string{"model-a"}) {
		t.Errorf("工作空间清单变动不符: %+v", impact.Workspaces)
	}
	// 默认空间的任务报原名，命名空间带 `空间/` 前缀——两个空间可能有同名任务。
	if !slices.Equal(impact.RemovedTasks, []string{"shared", "teamA/only-a"}) {
		t.Errorf("被删任务 = %v，期望 [shared teamA/only-a]", impact.RemovedTasks)
	}
	if !impact.UnifiedModel {
		t.Error("unified_model 指向被删的 model-a，必须报告它会被改写")
	}

	// 预演**一个字节都不能落盘**：它只是把真写会发生的事算一遍。
	after := readConfig(t, path)
	encoded, _ := json.Marshal(after)
	original, _ := json.Marshal(before)
	if string(encoded) != string(original) {
		t.Errorf("预演改动了磁盘上的配置: %s", encoded)
	}
}

// TestModelRemovalCascadesInsteadOfBlocking 固化：真正落盘的那一次不再被
// 「引用了未配置的模型」挡住，四类引用按预演承诺的结果一并清掉。
func TestModelRemovalCascadesInsteadOfBlocking(t *testing.T) {
	server, path := cascadeServer(t)

	recorder := callCascade(t, server, http.MethodPut, "/api/providers/prov-a/keys/key-a/models",
		`{"config_revision":"`+currentRevision(t, path)+`","models":["model-b"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("保存状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}

	data := readConfig(t, path)
	if names := modelNamesIn(t, data); !slices.Equal(names, []string{"model-b"}) {
		t.Errorf("落盘后的模型 = %v，期望只剩 model-b", names)
	}
	tasks, _ := data["tasks"].(map[string]any)
	if len(tasks) != 0 {
		t.Errorf("引用被删模型的默认空间任务应被清掉，实际 %v", tasks)
	}
	teamA := nestedObject(t, data, "workspaces", "teamA")
	if models, present := stringList(t, teamA, "models"); !present || !slices.Equal(models, []string{"model-b"}) {
		t.Errorf("teamA 的直呼清单 = %v（present=%v），期望只剩 model-b", models, present)
	}
	if teamATasks, _ := teamA["tasks"].(map[string]any); len(teamATasks) != 0 {
		t.Errorf("引用被删模型的空间任务应被清掉，实际 %v", teamATasks)
	}

	// 访问密钥的模型清单里，指向被删模型的那一项被摘掉，其余原样保留。
	public := nestedObject(t, data, "access_keys", "public")
	models, present := stringList(t, public, "models")
	if !present {
		t.Error("清单字段不该被删掉（删字段等于把它从「限定为这些」松开成「不限制」）")
	}
	if !slices.Equal(models, []string{"model-b"}) {
		t.Errorf("访问密钥的模型清单 = %v，期望 [model-b]", models)
	}
	if providers, present := stringList(t, public, "providers"); !present || !slices.Equal(providers, []string{"prov-a"}) {
		t.Errorf("供应商没被删，providers 清单不该动: %v（present=%v）", providers, present)
	}
}

// TestDryRunProviderDeletionReportsProviderList 固化：删供应商时失效的是
// `access_keys.providers`（另一份清单），它同样要说清楚。
func TestDryRunProviderDeletionReportsProviderList(t *testing.T) {
	server, path := cascadeServer(t)
	before := readConfig(t, path)

	recorder := callCascade(t, server, http.MethodDelete, "/api/providers/prov-a?dry_run=1",
		`{"config_revision":"`+currentRevision(t, path)+`"}`)
	impact := decodeImpact(t, recorder)

	if !slices.Equal(impact.RemovedModels, []string{"model-a", "model-b"}) {
		t.Errorf("被删模型 = %v，期望两个模型都失去目标", impact.RemovedModels)
	}
	if len(impact.AccessKeys) != 1 {
		t.Fatalf("应报告 public 这一把访问密钥的变动: %+v", impact.AccessKeys)
	}
	change := impact.AccessKeys[0]
	if !slices.Equal(change.Providers, []string{"prov-a"}) || !change.ProvidersCleared {
		t.Errorf("供应商清单变动不符: providers=%v cleared=%v", change.Providers, change.ProvidersCleared)
	}
	if !slices.Equal(change.Models, []string{"model-a", "model-b"}) || !change.ModelsCleared {
		t.Errorf("模型清单变动不符: models=%v cleared=%v", change.Models, change.ModelsCleared)
	}
	if len(impact.Workspaces) != 1 || !impact.Workspaces[0].ModelsCleared {
		t.Errorf("工作空间清单应被摘空: %+v", impact.Workspaces)
	}

	after := readConfig(t, path)
	encoded, _ := json.Marshal(after)
	original, _ := json.Marshal(before)
	if string(encoded) != string(original) {
		t.Errorf("预演改动了磁盘上的配置: %s", encoded)
	}

	// 真删一次：两份清单都摘空了，但**保留空数组**——删字段等于把「一个都不许」
	// 松开成「不限制」，那是扩权而不是收权。
	recorder = callCascade(t, server, http.MethodDelete, "/api/providers/prov-a",
		`{"config_revision":"`+currentRevision(t, path)+`"}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("删除供应商状态码 = %d，期望 204（body=%s）", recorder.Code, recorder.Body.String())
	}
	public := nestedObject(t, readConfig(t, path), "access_keys", "public")
	if providers, present := stringList(t, public, "providers"); !present || !slices.Equal(providers, []string{}) {
		t.Errorf("供应商清单 = %v（present=%v），期望摘空但保留字段", providers, present)
	}
	if models, present := stringList(t, public, "models"); !present || !slices.Equal(models, []string{}) {
		t.Errorf("模型清单 = %v（present=%v），期望摘空但保留字段", models, present)
	}
}

// TestDryRunDeleteRouteReportsCascade 固化：删路由（`targets: []` 与 DELETE 同义）
// 也要先预演，且预演不落盘。
func TestDryRunDeleteRouteReportsCascade(t *testing.T) {
	server, path := cascadeServer(t)
	before := readConfig(t, path)
	revision := currentRevision(t, path)

	for _, target := range []string{
		"/api/routes/model-a?dry_run=1",
		"/api/providers/prov-a/keys/key-a?dry_run=1",
	} {
		recorder := callCascade(t, server, http.MethodDelete, target,
			`{"config_revision":"`+revision+`"}`)
		impact := decodeImpact(t, recorder)
		if !slices.Equal(impact.RemovedModels, []string{"model-a"}) {
			t.Errorf("%s 的被删模型 = %v，期望 [model-a]", target, impact.RemovedModels)
		}
		if len(impact.AccessKeys) != 1 || !slices.Equal(impact.AccessKeys[0].Models, []string{"model-a"}) {
			t.Errorf("%s 的访问密钥变动不符: %+v", target, impact.AccessKeys)
		}
		if !slices.Equal(impact.RemovedTasks, []string{"shared", "teamA/only-a"}) {
			t.Errorf("%s 的被删任务 = %v", target, impact.RemovedTasks)
		}
	}
	// DELETE 的请求体是必填的（带 {} 即可），带不带版本号不影响预演。
	recorder := callCascade(t, server, http.MethodDelete, "/api/routes/model-a?dry_run=1",
		`{"config_revision":"`+revision+`"}`)
	if impact := decodeImpact(t, recorder); !slices.Equal(impact.RemovedModels, []string{"model-a"}) {
		t.Errorf("带版本号的预演 = %v，期望 [model-a]", impact.RemovedModels)
	}
	// PUT 把 targets 清空与 DELETE 是同一条写路径（服务端的既有不变式）。
	recorder = callCascade(t, server, http.MethodPut, "/api/routes/model-a?dry_run=1",
		`{"config_revision":"`+revision+`","targets":[]}`)
	if impact := decodeImpact(t, recorder); !slices.Equal(impact.RemovedModels, []string{"model-a"}) {
		t.Errorf("清空 targets 的预演 = %v，期望 [model-a]", impact.RemovedModels)
	}

	after := readConfig(t, path)
	encoded, _ := json.Marshal(after)
	original, _ := json.Marshal(before)
	if string(encoded) != string(original) {
		t.Errorf("预演改动了磁盘上的配置: %s", encoded)
	}
}

// TestDryRunWithoutImpactStillReturnsShape 固化：没有连带变动时也返回完整形状。
//
// 客户端只按这一个形状读（列表为空就跳过确认框），少一个键就得在每一处补兜底。
func TestDryRunWithoutImpactStillReturnsShape(t *testing.T) {
	server, path := cascadeServer(t)

	// 把两个模型都留给这把 Key：没有任何模型会消失。
	recorder := callCascade(t, server, http.MethodPut,
		"/api/providers/prov-a/keys/key-a/models?dry_run=1",
		`{"config_revision":"`+currentRevision(t, path)+`","models":["model-a","model-b"]}`)
	impact := decodeImpact(t, recorder)

	if !impact.DryRun || len(impact.RemovedModels) != 0 || len(impact.AccessKeys) != 0 ||
		len(impact.Workspaces) != 0 || len(impact.RemovedTasks) != 0 || impact.UnifiedModel {
		t.Errorf("这组改动不该有连带变动: %+v（body=%s）", impact, recorder.Body.String())
	}
	if !jsonHasKeys(t, recorder.Body.String(),
		"dry_run", "removed_models", "access_keys", "workspaces", "removed_tasks", "unified_model", "config_revision") {
		t.Errorf("预演响应必须给全所有键: %s", recorder.Body.String())
	}
}

// jsonHasKeys 报告 JSON 对象里是否逐个存在这些键（只看顶层）。
func jsonHasKeys(t *testing.T, body string, keys ...string) bool {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, body)
	}
	for _, key := range keys {
		if _, present := payload[key]; !present {
			return false
		}
	}
	return true
}
