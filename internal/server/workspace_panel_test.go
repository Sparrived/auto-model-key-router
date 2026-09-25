package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
)

// 本文件固化嵌入方面板的读数端点 /ui/workspace-panel.json。
//
// 与 workspace_usage_test.go 的分工：那条给**完整权限**看全部空间；这条只认面板
// key，且只回 key 所属的**那一个**空间。

// panelFixture 是一份带两个面板空间的最小配置。
//
// 用 configText 整段替换夹具：默认夹具里没有 workspaces 段，而面板 key 只存在于
// workspaces.<名字>.api_key 上。
const panelFixture = `{
  "config_version": 4,
  "local_api_key": "local-key",
  "ops_enabled": true,
  "webui_enabled": true,
  "host": "127.0.0.1",
  "port": 8000,
  "endpoint_capabilities_path": "<CAP>",
  "metrics_db_path": "<DB>",
  "log_file_path": "<LOG>",
  "providers": {
    "prov-a": {"base_url": "https://a.example.test", "keys": {"key-a": {"api_key": "sk-a"}}}
  },
  "models": {
    "model-a": {"targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}],
                "aliases": ["alias-a"]},
    "model-b": {"targets": []}
  },
  "workspaces": {
    "teamA": {"api_key": "panel-a"},
    "teamB": {"api_key": "panel-b"},
    "plain": {"tasks": {"t": {"model": "model-a"}}}
  }
}`

// newWorkspacePanelApp 装配一个 /ui 真的挂载、且带面板空间的 App。
func newWorkspacePanelApp(t *testing.T) *App {
	t.Helper()
	return newTestAppWith(t, t.TempDir(), appFixture{
		configText: panelFixture,
		mutate: func(options *Options) {
			options.WebUIAssets = fs.FS(fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")},
			})
		},
	})
}

// seedPanelUsage 往指标库写几行归属不同的记录。
func seedPanelUsage(t *testing.T, app *App) {
	t.Helper()
	adapter := app.currentMetricsAdapter()
	if adapter == nil || adapter.store == nil {
		t.Fatal("指标不可用，无法准备测试数据")
	}
	openai := "openai"
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
	)
	for _, item := range []struct{ workspace string }{
		{workspace: "teamA"}, {workspace: "teamA"}, {workspace: "teamB"},
	} {
		if err := adapter.store.Record(metrics.RecordParams{
			ModelID:    "model-a",
			KeyName:    "key-a",
			Usage:      usage,
			CallerType: "local",
			ProviderID: &openai,
			Workspace:  item.workspace,
		}); err != nil {
			t.Fatalf("写入指标失败: %v", err)
		}
	}
}

// TestWorkspacePanelIsScopedToItsKey 固化：面板只看到 key 自己那个空间。
func TestWorkspacePanelIsScopedToItsKey(t *testing.T) {
	app := newWorkspacePanelApp(t)
	seedPanelUsage(t, app)

	recorder := serve(app, http.MethodGet, "/ui/workspace-panel.json", "Bearer panel-a")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Workspace string   `json:"workspace"`
		Models    []string `json:"models"`
		Links     []struct {
			Source string `json:"source"`
		} `json:"links"`
		Workspaces []struct {
			Name  string `json:"name"`
			Stats struct {
				Requests int64 `json:"requests"`
			} `json:"stats"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, recorder.Body.String())
	}

	if body.Workspace != "teamA" {
		t.Errorf("workspace = %q，期望 teamA", body.Workspace)
	}
	// 关键断言：只回 teamA 一个空间，teamB 的流量**不在**响应里。
	if len(body.Workspaces) != 1 || body.Workspaces[0].Name != "teamA" {
		t.Fatalf("workspaces = %+v，期望只有 teamA", body.Workspaces)
	}
	if body.Workspaces[0].Stats.Requests != 2 {
		t.Errorf("teamA 请求数 = %d，期望 2", body.Workspaces[0].Stats.Requests)
	}
	if len(body.Links) != 0 {
		for _, link := range body.Links {
			if link.Source == "teamB" {
				t.Errorf("响应里出现了别的空间: %+v", link)
			}
		}
	}
	// 模型清单供面板建任务时挑选：模型 ID + 别名，与 /v1/models 同口径。
	if len(body.Models) == 0 {
		t.Errorf("models 不应为空: %+v", body.Models)
	}
}

// TestWorkspacePanelRejectsOtherCredentials 固化：完整权限与访客都不能用这条端点。
//
// 完整权限走 /ui/workspace-usage.json（能看全部空间），从这条受限路径拿数据会让
// 「完整权限能看什么」取决于它恰好也满足面板判定，语义重叠。
func TestWorkspacePanelRejectsOtherCredentials(t *testing.T) {
	app := newWorkspacePanelApp(t)

	for _, authorization := range []string{"", fullAuthorization, otherAuthorization, "Bearer nope"} {
		recorder := serve(app, http.MethodGet, "/ui/workspace-panel.json", authorization)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("凭据 %q 状态码 = %d，期望 401", authorization, recorder.Code)
		}
	}
}

// TestWorkspacePanelEmptyWorkspaceStillAnswers 固化：空间没有流量时照样 200。
//
// 面板刚建出来就是这个状态（有 key、没任务、没流量），回 404 会让嵌入方以为接口
// 坏了；回一个零值读数是"还没有数据"，语义正确。
func TestWorkspacePanelEmptyWorkspaceStillAnswers(t *testing.T) {
	app := newWorkspacePanelApp(t)
	// 只给 teamB 写流量，teamA 一条都没有。
	adapter := app.currentMetricsAdapter()
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
	)
	if err := adapter.store.Record(metrics.RecordParams{
		ModelID: "model-a", KeyName: "key-a", Usage: usage, CallerType: "local",
		Workspace: "teamB",
	}); err != nil {
		t.Fatalf("写入指标失败: %v", err)
	}

	recorder := serve(app, http.MethodGet, "/ui/workspace-panel.json", "Bearer panel-a")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Workspaces   []any `json:"workspaces"`
		Unattributed struct {
			Requests int64 `json:"requests"`
		} `json:"unattributed"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.Workspaces) != 0 {
		t.Errorf("teamA 没有流量，workspaces 应为空: %+v", body.Workspaces)
	}
	// 未归属永远是 0：它是**全实例**的口径，不该透露给单个空间的嵌入方。
	if body.Unattributed.Requests != 0 {
		t.Errorf("面板的 unattributed 必须是 0，实际 %d", body.Unattributed.Requests)
	}
}

// TestWorkspacePanelRejectsNonGet 固化非 GET 回 405 并带 Allow 头。
func TestWorkspacePanelRejectsNonGet(t *testing.T) {
	app := newWorkspacePanelApp(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/workspace-panel.json", "Bearer panel-a")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s 状态码 = %d，期望 405", method, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf("%s 的 Allow = %q，期望 GET", method, allow)
		}
	}
}

// TestWorkspacePanelValidatesHours 固化参数校验先于鉴权（与 /metrics 同序）。
func TestWorkspacePanelValidatesHours(t *testing.T) {
	app := newWorkspacePanelApp(t)

	recorder := serve(app, http.MethodGet, "/ui/workspace-panel.json?hours=0", "")
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hours=0 状态码 = %d，期望 422（body=%s）", recorder.Code, recorder.Body.String())
	}
}
