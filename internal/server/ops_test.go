package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Sparrived/auto-model-key-router/internal/api"
	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// fixtureTemplate 是 Go 侧测试用的最小 v4 配置：取自迁移期的夹具形状（含一个没有任何 key
// 的模型、一个别名、unified_model），但路径由调用方填。
//
// 三个 %s 依次是 endpoint_capabilities_path、metrics_db_path、log_file_path（都指向
// 测试的临时目录），随后两个 %v 是 ops_enabled 与 webui_enabled。
const fixtureTemplate = `{
  "config_version": 4,
  "host": "127.0.0.1",
  "port": 8000,
  "request_timeout": 10,
  "stream_first_byte_timeout": 30,
  "stream_idle_timeout": 60,
  "max_retries": 1,
  "key_failure_threshold": 1,
  "key_cooldown_seconds": 60,
  "endpoint_capabilities_path": "%s",
  "metrics_db_path": "%s",
  "log_file_path": "%s",
  "local_api_key": "local-key",
  "ops_enabled": %v,
  "webui_enabled": %v,
  "providers": {
    "prov-a": {
      "base_url": "https://a.example.test",
      "keys": {
        "key-a": {"api_key": "sk-secret-a", "enabled": true},
        "key-b": {"api_key": "sk-secret-b", "enabled": true}
      },
      "routes": {"openai": "v1/chat/completions"}
    },
    "prov-b": {
      "base_url": "https://b.example.test",
      "keys": {"key-c": {"api_key": "sk-secret-c", "enabled": true}},
      "routes": {}
    }
  },
  "models": {
    "model-a": {
      "targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}],
      "aliases": ["alias-a"],
      "routing_mode": "round_robin"
    },
    "model-b": {"targets": [], "aliases": ["alias-b"]},
    "model-v": {
      "targets": [{"provider": "prov-a", "key": "key-b", "upstream_model": "model-v"}],
      "aliases": ["alias-v"]
    },
    "model-nv": {
      "targets": [{"provider": "prov-b", "key": "key-c", "upstream_model": "model-nv"}],
      "aliases": ["alias-nv"]
    }
  },
  "unified_model": {"default": {"primary": {"model": "model-a", "key": "key-a"}}}
}`

// jsonPath 把 Windows 路径转成能嵌进 JSON 字符串的形式（反斜杠要成对）。
func jsonPath(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}

// writeFixture 把测试配置写进 dir，返回配置文件路径。
//
// 用 os.WriteFile 而不是 config.SaveConfigData：夹具文本本身就是配置的期望内容，
// 走保存路径会额外引入「迁移/重排」这一层与测试无关的行为。
func writeFixture(t *testing.T, dir string, opsEnabled, webUIEnabled bool) string {
	t.Helper()
	path := filepath.Join(dir, "router-config.json")
	text := fmt.Sprintf(fixtureTemplate,
		jsonPath(filepath.Join(dir, "endpoint-capabilities.json")),
		jsonPath(filepath.Join(dir, "metrics.sqlite3")),
		jsonPath(filepath.Join(dir, "server.log")),
		opsEnabled, webUIEnabled,
	)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	return path
}

// appFixture 描述测试夹具的两个配置开关与装配后的 Options 覆盖。
type appFixture struct {
	// opsEnabled 写进配置文件的 ops_enabled。
	opsEnabled bool
	// webUIEnabled 写进配置文件的 webui_enabled。
	webUIEnabled bool
	// configText 非空时**整段替换**夹具配置（three-path 占位符 <CAP>/<DB>/<LOG>
	// 仍会被替换）。给需要夹具里没有的配置段（如 workspaces）的测试用。
	configText string
	// mutate 在装配前覆盖 Options（注入桩、覆盖开关）。
	mutate func(*Options)
}

// newTestApp 装配一个指向临时目录的 App（夹具 ops_enabled=true），并在测试结束时关停它。
func newTestApp(t *testing.T, dir string, mutate func(*Options)) *App {
	t.Helper()
	return newTestAppWith(t, dir, appFixture{opsEnabled: true, mutate: mutate})
}

// newTestAppWith 装配一个指向临时目录的 App。
func newTestAppWith(t *testing.T, dir string, fixture appFixture) *App {
	t.Helper()
	var path string
	if fixture.configText != "" {
		path = filepath.Join(dir, "router-config.json")
		text := strings.NewReplacer(
			"<CAP>", jsonPath(filepath.Join(dir, "endpoint-capabilities.json")),
			"<DB>", jsonPath(filepath.Join(dir, "metrics.sqlite3")),
			"<LOG>", jsonPath(filepath.Join(dir, "server.log")),
		).Replace(fixture.configText)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatalf("写入测试配置失败: %v", err)
		}
	} else {
		path = writeFixture(t, dir, fixture.opsEnabled, fixture.webUIEnabled)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入测试配置失败: %v", err)
	}
	options := Options{
		ConfigPath: path,
		Config:     loaded,
		Version:    testVersion,
	}
	if fixture.mutate != nil {
		fixture.mutate(&options)
	}
	app, err := New(options)
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := app.Close(); closeErr != nil {
			t.Errorf("关停失败: %v", closeErr)
		}
	})
	return app
}

// testVersion 是测试注入的版本号（与 pyproject 的 4.1.0 区分开，便于断言真的传到了
// /api/tool）。
const testVersion = "9.9.9-test"

// serve 发一条请求并返回响应记录器。
func serve(app *App, method, path, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder
}

// fullAuthorization 是本地主凭据；otherAuthorization 是一个**不被接受**的凭据。
//
// 它刻意沿用已取消的访客 key 字面量：这些用例关心的是「非完整权限的凭据会被挡在
// 管理面之外」，而 amkr-visitor 现在正好就是一把不存在的凭据（见 internal/auth 与
// TestVisitorKeyIsRejected）。用它比编一个假 key 更能说明「访客模式确实没了」。
const (
	fullAuthorization  = "Bearer local-key"
	otherAuthorization = "Bearer amkr-visitor"
	notFoundBody       = `{"detail":"Not Found"}`
)

// stubCheckUpdate 是版本检查桩：不联网，且 LatestVersion 比任一测试版本都新。
func stubCheckUpdate(float64) api.UpdateCheckResult {
	return api.UpdateCheckResult{
		CurrentVersion:  testVersion,
		LatestVersion:   "99.0.0",
		ReleaseURL:      "https://example.test/release",
		Source:          "PyPI",
		ArtifactURL:     "https://example.test/a.whl",
		ArtifactSHA256:  "deadbeef",
		UpdateAvailable: true,
	}
}

// TestOpsRoutesReachableWhenEnabled 是装配层的接线断言：**开启 ops 后**，7 条运维
// 路由必须能在装配好的 App 上命中，而不是落进管理 API 的兜底 404。
//
// 这条测试存在的理由：api.Server 只在 OpsEnabled 为真时才注册运维路由，而装配层漏
// 传这个字段不会有任何编译错误或现存测试失败——四个 WebUI 接口会静默变成 404。
// 因此这里不依赖 api 包自己的测试，而是从**装配后的 Handler** 发请求。
func TestOpsRoutesReachableWhenEnabled(t *testing.T) {
	app := newTestApp(t, t.TempDir(), func(options *Options) {
		options.CheckUpdate = stubCheckUpdate
	})

	// 无凭据：鉴权失败（401）而不是兜底 404 —— 说明路由确实注册了。
	unauthorized := serve(app, http.MethodGet, "/api/logs", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/logs（无凭据）状态码 = %d，期望 401（body=%s）",
			unauthorized.Code, unauthorized.Body.String())
	}

	// 带凭据：200。日志文件不存在也要 200（ops_api.py 把错误写进响应体）。
	logs := serve(app, http.MethodGet, "/api/logs", fullAuthorization)
	if logs.Code != http.StatusOK {
		t.Fatalf("GET /api/logs 状态码 = %d，期望 200（body=%s）",
			logs.Code, logs.Body.String())
	}

	// 第二条运维路由：GET /api/tool 必须报出装配层注入的版本号。
	tool := serve(app, http.MethodGet, "/api/tool", fullAuthorization)
	if tool.Code != http.StatusOK {
		t.Fatalf("GET /api/tool 状态码 = %d，期望 200（body=%s）",
			tool.Code, tool.Body.String())
	}
	parsed, err := canonical.ParseString(tool.Body.String())
	if err != nil {
		t.Fatalf("解析 /api/tool 响应失败: %v", err)
	}
	if got := parsed.Lookup("version").StringValue(); got != testVersion {
		t.Errorf("/api/tool 的 version = %q，期望 %q（装配层必须把 Options.Version 传下去）",
			got, testVersion)
	}
	if got := parsed.Lookup("latest_version").StringValue(); got != "99.0.0" {
		t.Errorf("/api/tool 的 latest_version = %q，期望注入的桩值 99.0.0", got)
	}

	// 第三条：集成路由**已接线** —— 经接缝返回 200，而不是 500。
	//
	// 这里读的是开发机真实的 Agent 配置目录（Options 零值即真实主目录）。本断言只看状态码
	// 与结构，不看具体内容，因此不依赖机器状态；它同时是"接缝确实接上了"的端到端证据——
	// 一旦有人摘掉 opsIntegrations()，这里会退回 500 并立刻失败。
	integrations := serve(app, http.MethodGet, "/api/integrations", fullAuthorization)
	if integrations.Code != http.StatusOK {
		t.Fatalf("GET /api/integrations 状态码 = %d，期望 200（接缝已接线；body=%s）",
			integrations.Code, integrations.Body.String())
	}
	if !strings.Contains(integrations.Body.String(), `"integrations"`) {
		t.Errorf("GET /api/integrations 的响应体应含 integrations 列表，实际 %q",
			integrations.Body.String())
	}
}

// TestOpsRoutesAbsentWhenDisabled 锁定开关的负面：ops_enabled=false 时 7 条 URL
// **一条都不注册**，与参照实现一样落进 {"detail":"Not Found"}。
func TestOpsRoutesAbsentWhenDisabled(t *testing.T) {
	disabled := false
	app := newTestApp(t, t.TempDir(), func(options *Options) {
		options.OpsEnabled = &disabled
		options.CheckUpdate = stubCheckUpdate
	})
	paths := []string{
		"/api/logs",
		"/api/tool",
		"/api/integrations",
		"/api/integrations/claude-code",
		"/api/service/status_amkr",
	}
	for _, path := range paths {
		for _, authorization := range []string{"", fullAuthorization} {
			recorder := serve(app, http.MethodGet, path, authorization)
			if recorder.Code != http.StatusNotFound {
				t.Errorf("GET %s（凭据=%q）状态码 = %d，期望 404（body=%s）",
					path, authorization, recorder.Code, recorder.Body.String())
			}
			if recorder.Body.String() != notFoundBody {
				t.Errorf("GET %s（凭据=%q）响应体 = %s，期望 %s",
					path, authorization, recorder.Body.String(), notFoundBody)
			}
		}
	}
	// 反向确认：同一份配置改成 ops_enabled=true 后它又能命中（排除「路径本来就错」）。
	enabled := true
	enabledApp := newTestApp(t, t.TempDir(), func(options *Options) {
		options.OpsEnabled = &enabled
		options.CheckUpdate = stubCheckUpdate
	})
	if recorder := serve(enabledApp, http.MethodGet, "/api/logs", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("ops_enabled=true 时 GET /api/logs 状态码 = %d，期望 401", recorder.Code)
	}
}

// TestOpsToolReportsWebUIStatus 断言 /api/tool 的 webui_* 四个字段来自装配层注入的
// WebUIStatus（与 /health 同源），而不是 api 侧的零值。
//
// 两种组合都要覆盖，因为参照实现的 webui_status 读的是**运行时配置**里的
// webui_enabled（webui.py:73-78），而不是启动时那个「配置 + CLI 覆盖」的开关：
//
//	配置 true  + 无覆盖     -> enabled/mounted 都是 true
//	配置 false + 覆盖 true  -> mounted 是 true（真的挂上了），enabled 仍是 false
//
// 第二种组合正是「CLI --webui 打开但配置里没开」的现场，/health 与 /api/tool 都会
// 报 enabled=false —— 这不是缺陷，是参照实现的既定语义。
func TestOpsToolReportsWebUIStatus(t *testing.T) {
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")}}
	override := true
	cases := []struct {
		name         string
		fixtureWebUI bool
		override     *bool
		wantEnabled  bool
	}{
		{"配置打开且无覆盖", true, nil, true},
		{"配置关闭但被 CLI 覆盖", false, &override, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app := newTestAppWith(t, t.TempDir(), appFixture{
				opsEnabled:   true,
				webUIEnabled: testCase.fixtureWebUI,
				mutate: func(options *Options) {
					options.CheckUpdate = stubCheckUpdate
					options.WebUIAssets = fs.FS(assets)
					options.WebUIEnabled = testCase.override
				},
			})

			tool := serve(app, http.MethodGet, "/api/tool", fullAuthorization)
			if tool.Code != http.StatusOK {
				t.Fatalf("GET /api/tool 状态码 = %d，期望 200（body=%s）",
					tool.Code, tool.Body.String())
			}
			parsed, err := canonical.ParseString(tool.Body.String())
			if err != nil {
				t.Fatalf("解析 /api/tool 响应失败: %v", err)
			}
			if got := parsed.Lookup("webui_available"); got == nil || !got.Bool {
				t.Errorf("/api/tool 的 webui_available = %v，期望 true", got)
			}
			if got := parsed.Lookup("webui_enabled"); got == nil || got.Bool != testCase.wantEnabled {
				t.Errorf("/api/tool 的 webui_enabled = %v，期望 %v",
					got, testCase.wantEnabled)
			}
			if got := parsed.Lookup("webui_mounted"); got == nil || !got.Bool {
				t.Errorf("/api/tool 的 webui_mounted = %v，期望 true", got)
			}
			if got := parsed.Lookup("webui_path").StringValue(); got != "/ui" {
				t.Errorf("/api/tool 的 webui_path = %q，期望 /ui", got)
			}
			// 挂载真的生效：/ui/ 返回资产本体。
			if recorder := serve(app, http.MethodGet, "/ui/", ""); recorder.Code != http.StatusOK {
				t.Errorf("GET /ui/ 状态码 = %d，期望 200", recorder.Code)
			}
		})
	}
}
