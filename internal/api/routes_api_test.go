package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 本文件固化模型路由（/api/routes）在「可调用名与上游名解耦」之后的写语义。
//
// 三条对外承诺：
//
//   - 可调用名只有模型 ID 与 aliases；hidden_aliases / auto_hidden_aliases 已移除，
//     请求里带上它会被 Pydantic 的 extra_forbidden 挡掉（而不是被静默忽略）；
//   - target 的 upstream_model 是纯上游概念，可以等于、也可以不等于路由 ID——「同一个
//     模型在上游各叫各的」由它表达，不再靠上游名自动变成别名；
//   - 「路由下没有目标就不该存在」由服务端保证：targets 清空即删除路由，并修好
//     unified_model / 任务里对它的引用。

// routeRevision 读取当前配置版本（写接口用它防并发覆盖）。
func routeRevision(t *testing.T, server *Server) string {
	t.Helper()
	return accessKeyRevision(t, server)
}

// callRoutesWrite 向 /api/routes* 发一条带版本的写请求。
//
// 夹具文件在首次落盘前没有 config_revision 字段，而写接口要求它，因此这里先读一次
// 再把它拼成 body 的第一个字段（与 callAccessKeysWrite 同一套做法）。body 传 `{}`
// 表示只要版本号。
func callRoutesWrite(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	fields := strings.TrimSuffix(strings.TrimPrefix(body, "{"), "}")
	payload := `{"config_revision":"` + routeRevision(t, server) + `"`
	if fields != "" {
		payload += "," + fields
	}
	return callTasks(t, server, method, path, "", payload+"}")
}

// TestUpdateRouteWithEmptyTargetsDeletesRoute 钉住「清空 targets 即删除路由」。
//
// 为什么这条语义放在服务端：取消 Key 勾选、解绑 Key、删 Key 三条写路径早就会删掉
// 失去全部 target 的路由，只有「整体替换 targets」还要求调用方先清空再 DELETE。同一个
// 不变式由不同调用方各自维持，迟早会漂移出一条谁都用不了的空路由（它不会出现在
// /v1/models 里，却在 /api/routes 里阴魂不散）。
func TestUpdateRouteWithEmptyTargetsDeletesRoute(t *testing.T) {
	server, path := workspaceServer(t)

	recorder := callRoutesWrite(t, server, http.MethodPut, "/api/routes/model-a", `"targets":[]}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("清空 targets 状态码 = %d，期望 204（body=%s）", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); body != "" {
		t.Errorf("204 不应带响应体，实际 %q", body)
	}

	// 路由本身没了：列表里不再出现，再删一次是 404（说明它已经不在配置里）。
	routes := callTasks(t, server, http.MethodGet, "/api/routes", "", "")
	if strings.Contains(routes.Body.String(), "model-a") {
		t.Errorf("路由列表不应再含 model-a: %s", routes.Body.String())
	}
	if got := callRoutesWrite(t, server, http.MethodDelete, "/api/routes/model-a", "{}"); got.Code != http.StatusNotFound {
		t.Errorf("再次删除已消失的路由应 404，实际 %d（body=%s）", got.Code, got.Body.String())
	}

	// 引用它的任务被一并修掉（任务 shared 指向 model-a），配置里不再残留这个名字。
	// 这条断言同时证明「删路由」走的是 DeleteModel 那条会修复引用的路径。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if strings.Contains(string(raw), "model-a") {
		t.Errorf("清空路由后配置里不应残留 model-a: %s", raw)
	}
}

// TestUpdateRouteWithoutTargetsFieldKeepsThem 断言「不带 targets 字段」不等于清空。
//
// 区分这两者是必须的：`targets` 缺失表示不改目标，`targets: []` 表示清空并因此删除
// 路由。把前者也当成删除会让任何一个只想改别名的 PUT 顺手删掉整条路由。
func TestUpdateRouteWithoutTargetsFieldKeepsThem(t *testing.T) {
	server, _ := workspaceServer(t)

	recorder := callRoutesWrite(t, server, http.MethodPut, "/api/routes/model-a", `"aliases":["alias-new"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("只改别名状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"alias-new"`) {
		t.Errorf("别名未写进响应: %s", body)
	}
	if !strings.Contains(body, `"upstream_model":"model-a"`) {
		t.Errorf("不带 targets 字段时目标应原样保留: %s", body)
	}
}

// TestRouteTargetAcceptsUpstreamNameAheadOfRouteID 钉住「上游名与路由名解耦」。
//
// 这是这次改造要支持的核心用法：外部只有一个名字（路由 ID），而上游各叫各的。把一个
// 「上游叫 vendor-flash」的 Key 收进 model-b 这条路由之后，对外仍然只有 model-b，
// vendor-flash 不会因此变成可调用名（它只是发给上游的参数）。
func TestRouteTargetAcceptsUpstreamNameAheadOfRouteID(t *testing.T) {
	server, path := workspaceServer(t)

	recorder := callRoutesWrite(t, server, http.MethodPut, "/api/routes/model-b",
		`"targets":[{"provider":"prov-a","key":"key-a","upstream_model":"vendor-flash"}]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("改上游名状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"upstream_model":"vendor-flash"`) {
		t.Errorf("响应未回显新的上游名: %s", body)
	}
	if !strings.Contains(body, `"id":"model-b"`) {
		t.Errorf("路由 ID 不应随上游名变化: %s", body)
	}

	// 落盘之后可调用名（models 的键与 aliases）仍然只有 model-b。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if !strings.Contains(string(raw), `"upstream_model": "vendor-flash"`) {
		t.Errorf("上游名未落盘: %s", raw)
	}
	if !strings.Contains(string(raw), `"model-b"`) {
		t.Errorf("路由名不应被上游名取代: %s", raw)
	}
}

// TestHiddenAliasesAreRejectedOnEveryWritePath 断言隐藏别名在所有写路径上都被拒。
//
// 选 422 + extra_forbidden 而不是「静默忽略」：静默忽略会让一份还写着 hidden_aliases
// 的配置看起来生效了，而实际上那些名字已经不能调用——排查成本远高于一次明确的 422。
func TestHiddenAliasesAreRejectedOnEveryWritePath(t *testing.T) {
	server, _ := workspaceServer(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			"POST /api/routes",
			http.MethodPost, "/api/routes",
			`"id":"new-route","targets":[{"provider":"prov-a","key":"key-a","upstream_model":"x"}],"hidden_aliases":["legacy"]}`,
		},
		{
			"PUT /api/routes/{route_id}",
			http.MethodPut, "/api/routes/model-a",
			`"targets":[{"provider":"prov-a","key":"key-a","upstream_model":"model-a"}],"hidden_aliases":["legacy"]}`,
		},
		{
			"POST /api/models",
			http.MethodPost, "/api/models",
			`"id":"new-model","hidden_aliases":["legacy"]}`,
		},
		{
			"PUT /api/models/{model_id}",
			http.MethodPut, "/api/models/model-a",
			`"hidden_aliases":["legacy"]}`,
		},
	}
	for _, testCase := range cases {
		recorder := callRoutesWrite(t, server, testCase.method, testCase.path, testCase.body)
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 状态码 = %d，期望 422（body=%s）", testCase.name, recorder.Code, recorder.Body.String())
			continue
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "extra_forbidden") || !strings.Contains(body, "hidden_aliases") {
			t.Errorf("%s 的 422 应指名 hidden_aliases 是多余字段: %s", testCase.name, body)
		}
	}
}

// TestModelResponseDropsHiddenAliasFields 断言响应形状里不再有这两个字段。
//
// 响应形状是契约：客户端按 auto_hidden_aliases 做过展示的代码必须能察觉到字段消失，
// 而不是拿到一个永远为空的数组继续渲染一列「隐藏别名」。
func TestModelResponseDropsHiddenAliasFields(t *testing.T) {
	server, _ := workspaceServer(t)

	for _, path := range []string{"/api/models", "/api/models/model-a"} {
		recorder := callTasks(t, server, http.MethodGet, path, "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s 状态码 = %d（body=%s）", path, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if strings.Contains(body, "hidden_alias") {
			t.Errorf("GET %s 的响应不应再含隐藏别名字段: %s", path, body)
		}
		if !strings.Contains(body, `"aliases"`) {
			t.Errorf("GET %s 的响应应保留 aliases: %s", path, body)
		}
	}
}
