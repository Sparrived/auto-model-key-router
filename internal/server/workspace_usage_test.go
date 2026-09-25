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

// newWorkspaceUsageApp 装配一个 /ui 真的挂载的 App。
//
// /ui 下的路由（含本文件测的这条）只有在 index.html 存在时才注册
// （webui.Available），所以必须给一个桩资产，否则拿到的是 404 而不是被测行为。
func newWorkspaceUsageApp(t *testing.T) *App {
	t.Helper()
	return newTestAppWith(t, t.TempDir(), appFixture{
		opsEnabled:   true,
		webUIEnabled: true,
		mutate: func(options *Options) {
			options.WebUIAssets = fs.FS(fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")},
			})
		},
	})
}

// seedWorkspaceUsage 往当前租约那一代的指标库里写几行归属不同的记录。
//
// 用 currentMetricsAdapter 而不是另开一个 store：热重载会换库，写进另一个库的
// 文件端点读不到。
func seedWorkspaceUsage(t *testing.T, app *App) {
	t.Helper()
	adapter := app.currentMetricsAdapter()
	if adapter == nil || adapter.store == nil {
		t.Fatal("指标不可用，无法准备测试数据")
	}
	store := adapter.store
	openai, gpt4o := "openai", "gpt-4o"
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
	)
	for _, item := range []struct {
		workspace string
		model     string
	}{
		{workspace: "teamA", model: "gpt-4o"},
		{workspace: "teamA", model: "gpt-4o"},
		{workspace: "teamB", model: "gpt-4o"},
	} {
		if err := store.Record(metrics.RecordParams{
			ModelID:         item.model,
			KeyName:         "key-a",
			Usage:           usage,
			CallerType:      "local",
			ProviderID:      &openai,
			UpstreamModelID: &gpt4o,
			Workspace:       item.workspace,
		}); err != nil {
			t.Fatalf("写入工作空间指标失败: %v", err)
		}
	}
}

// TestWorkspaceUsageEndpoint 断言 /ui/workspace-usage.json 的形状与分组。
func TestWorkspaceUsageEndpoint(t *testing.T) {
	app := newWorkspaceUsageApp(t)
	seedWorkspaceUsage(t, app)

	recorder := serve(app, http.MethodGet, "/ui/workspace-usage.json", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", got)
	}

	var body struct {
		Layers []string `json:"layers"`
		Links  []struct {
			SourceLayer int    `json:"source_layer"`
			TargetLayer int    `json:"target_layer"`
			Source      string `json:"source"`
			Target      string `json:"target"`
			Requests    int64  `json:"requests"`
		} `json:"links"`
		Workspaces []struct {
			Name  string `json:"name"`
			Stats struct {
				Requests    int64 `json:"requests"`
				TotalTokens int64 `json:"total_tokens"`
			} `json:"stats"`
		} `json:"workspaces"`
		Unattributed struct {
			Requests int64 `json:"requests"`
		} `json:"unattributed"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, recorder.Body.String())
	}

	// 六层：供应商与上游模型之间还有「上游 Key」这一层（同一家可以配多把 Key）。
	if len(body.Layers) != 6 {
		t.Errorf("层数 = %d，期望 6", len(body.Layers))
	}
	if len(body.Workspaces) != 2 {
		t.Fatalf("工作空间数 = %d，期望 2", len(body.Workspaces))
	}
	// 按名字排序，teamA 在前。
	if body.Workspaces[0].Name != "teamA" || body.Workspaces[0].Stats.Requests != 2 {
		t.Errorf("teamA = %+v，期望 2 次请求", body.Workspaces[0])
	}
	if body.Workspaces[1].Name != "teamB" || body.Workspaces[1].Stats.Requests != 1 {
		t.Errorf("teamB = %+v，期望 1 次请求", body.Workspaces[1])
	}
	// 没有未归属行时该桶为 0，而不是缺字段。
	if body.Unattributed.Requests != 0 {
		t.Errorf("unattributed.requests = %d，期望 0", body.Unattributed.Requests)
	}
	if len(body.Links) == 0 {
		t.Fatal("连边为空，期望有流向数据")
	}
	for _, link := range body.Links {
		if link.TargetLayer != link.SourceLayer+1 {
			t.Errorf("连边不是相邻层: %d -> %d", link.SourceLayer, link.TargetLayer)
		}
	}
}

// TestWorkspaceUsageEndpointRequiresFullAuth 断言访客凭据被拒。
//
// 内容会暴露各工作空间的用量与模型流向（配置结构的投影），与 /metrics 同级。
func TestWorkspaceUsageEndpointRequiresFullAuth(t *testing.T) {
	app := newWorkspaceUsageApp(t)

	if recorder := serve(app, http.MethodGet, "/ui/workspace-usage.json", ""); recorder.Code != http.StatusUnauthorized {
		t.Errorf("无凭据状态码 = %d，期望 401", recorder.Code)
	}
	if recorder := serve(app, http.MethodGet, "/ui/workspace-usage.json", otherAuthorization); recorder.Code != http.StatusUnauthorized {
		t.Errorf("访客状态码 = %d，期望 401", recorder.Code)
	}
}

// TestWorkspaceUsageEndpointRejectsNonGet 断言非 GET 回 405 并带 Allow 头。
func TestWorkspaceUsageEndpointRejectsNonGet(t *testing.T) {
	app := newWorkspaceUsageApp(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/workspace-usage.json", fullAuthorization)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s 状态码 = %d，期望 405", method, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf("%s 的 Allow = %q，期望 GET", method, allow)
		}
	}
}

// TestWorkspaceUsageEndpointValidatesHours 断言参数校验先于鉴权（与 /metrics 同序）。
func TestWorkspaceUsageEndpointValidatesHours(t *testing.T) {
	app := newWorkspaceUsageApp(t)

	// hours=0 越界：不带凭据也应当是 422 而不是 401。
	recorder := serve(app, http.MethodGet, "/ui/workspace-usage.json?hours=0", "")
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hours=0 状态码 = %d，期望 422（body=%s）", recorder.Code, recorder.Body.String())
	}
}

// TestWorkspaceUsageEndpointAllHistory 断言 all_history 可用（历史行才看得到）。
func TestWorkspaceUsageEndpointAllHistory(t *testing.T) {
	app := newWorkspaceUsageApp(t)
	seedWorkspaceUsage(t, app)

	recorder := serve(app, http.MethodGet, "/ui/workspace-usage.json?all_history=true", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Window struct {
			From any `json:"from"`
		} `json:"window"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	// 全量窗口没有下界，from 必须是 null（而不是伪造一个很早的时间）。
	if body.Window.From != nil {
		t.Errorf("all_history 的 window.from = %v，期望 null", body.Window.From)
	}
}
