package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/health"
)

// 本文件覆盖运维面（ops_api.py 的 7 条路由）：
//
//  1. 控制开关（OpsEnabled）与三条未接线接缝的**响亮失败**各有具名用例；
//  2. 真实路径（internal/updatecheck）用一个假取回器端到端打通，证明接缝为 nil 时
//     走的是真实现而不是「假装成功」；
//  3. 服务动作表、日志截断与宽容解码的边界规则。

// testLocalAPIKey 是这些用例使用的本地主凭据。
const testLocalAPIKey = "local-key"

// testInvalidAPIKey 是一个不被任何入口接受的凭据。
const testInvalidAPIKey = "not-the-local-key"

// jsonEscapedPath 把路径转成它出现在 JSON 响应体里的样子。
//
// 409 的 detail 本身是 JSON 字符串，路径里的反斜杠在**响应体字节**里是 `\\`，
// 因此比较时必须用转义后的形式。
func jsonEscapedPath(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}

// TestOpsRoutesFollowOpsEnabled 锁定「ops 关闭时整条 URL 空间不存在」。
//
// 迁移方案明确要求：ops_enabled=false 时运维接口整体 404，且 /health.ops_enabled=false。
// 参照实现是**不注册**这些路由（app.py:144-145），因此这里不能靠 handler 里判断开关，
// 必须真的落到兜底的 {"detail":"Not Found"}。
func TestOpsRoutesFollowOpsEnabled(t *testing.T) {
	replacer := strings.NewReplacer("{agent}", "x", "{action}", "x")
	for _, enabled := range []bool{true, false} {
		server, _ := newOpsTestServer(t, func(s *Server) { s.OpsEnabled = enabled })
		for _, pattern := range opsRoutePatterns() {
			parts := strings.SplitN(pattern, " ", 2)
			path := replacer.Replace(parts[1])
			recorder := opsRequest(t, server, parts[0], path, nil, "full")
			isCatchAll := recorder.Result().StatusCode == http.StatusNotFound &&
				recorder.Body.String() == `{"detail":"Not Found"}`
			if enabled && isCatchAll {
				t.Errorf("OpsEnabled 为真时路由未注册: %s", pattern)
			}
			if !enabled && !isCatchAll {
				t.Errorf("OpsEnabled 为假时应整体 404: %s -> %d %s",
					pattern, recorder.Result().StatusCode, recorder.Body.String())
			}
		}
	}
}

// TestOpsUnwiredSeamsFailLoudly 锁定三条未接线接缝的失败行为。
//
// agent_config.py 与 service.py 尚未移植；接缝为 nil 时**必须**响亮失败，绝不能
// 静默返回空列表 / 空文本 / 200。状态码取参照实现的失败状态码（详见 handlers_ops.go
// 的说明），文案点名接缝，便于定位。
func TestOpsUnwiredSeamsFailLoudly(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		path     string
		body     string
		contains string
	}{
		{"integrations-list", "GET", "/api/integrations", "", "Server.Integrations"},
		{"integrations-apply", "POST", "/api/integrations/claude-code", `{"mode":"unified-model"}`, "Server.Integrations"},
		{"integrations-rollback", "POST", "/api/integrations/claude-code/rollback", "", "Server.Integrations"},
		{"service-action", "POST", "/api/service/status_amkr", "", "RunServiceAction"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			server, _ := newOpsTestServer(t, nil)
			var body []byte
			if item.body != "" {
				body = []byte(item.body)
			}
			recorder := opsRequest(t, server, item.method, item.path, body, "full")
			if got := recorder.Result().StatusCode; got != http.StatusInternalServerError {
				t.Fatalf("未接线接缝应 500，实际 %d：%s", got, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), item.contains) {
				t.Errorf("错误文案应点名接缝 %q，实际 %s", item.contains, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), `"integrations":[]`) {
				t.Errorf("不得静默返回空列表: %s", recorder.Body.String())
			}
		})
	}
}

// TestOpsServiceActionSeam 覆盖 RunServiceAction 接线后的成功与失败两条路径。
//
// 参照的失败映射是 500「服务操作失败: ...」（ops_api.py:193-194）；成功时是
// {"action", "text"}，text 是已经渲染好的纯文本。
func TestOpsServiceActionSeam(t *testing.T) {
	var gotAction, gotPath string
	server, configPath := newOpsTestServer(t, func(s *Server) {
		s.RunServiceAction = func(action, path string, cfg *config.RouterConfig) (string, error) {
			gotAction, gotPath = action, path
			return "系统服务注册\n已注册", nil
		}
	})
	recorder := opsRequest(t, server, "POST", "/api/service/status_amkr", nil, "full")
	if got := recorder.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("状态码: %d %s", got, recorder.Body.String())
	}
	if gotAction != "status_amkr" || gotPath != configPath {
		t.Errorf("接缝入参不对: action=%q path=%q（期望 %q）", gotAction, gotPath, configPath)
	}
	want := `{"action":"status_amkr","text":"系统服务注册\n已注册"}`
	if got := recorder.Body.String(); got != want {
		t.Errorf("响应体不一致:\n got=%s\nwant=%s", got, want)
	}

	failing, _ := newOpsTestServer(t, func(s *Server) {
		s.RunServiceAction = func(action, path string, cfg *config.RouterConfig) (string, error) {
			return "", errors.New("动作不可用")
		}
	})
	recorder = opsRequest(t, failing, "POST", "/api/service/status_amkr", nil, "full")
	if got := recorder.Result().StatusCode; got != http.StatusInternalServerError {
		t.Fatalf("失败应 500，实际 %d", got)
	}
	if want := `{"detail":"服务操作失败: 动作不可用"}`; recorder.Body.String() != want {
		t.Errorf("失败响应体不一致:\n got=%s\nwant=%s", recorder.Body.String(), want)
	}
}

// TestOpsServiceValidatesActionBeforeAuth 顺序锁：动作名白名单先于鉴权。
//
// ops_api.py:187-188 的 422 在 _authorized_config 之前，因此无凭据请求不支持的动作
// 得到 422 而不是 401；合法动作则是 401。运维面不接受受限凭据，非法凭据同样 401。
func TestOpsServiceValidatesActionBeforeAuth(t *testing.T) {
	server, _ := newOpsTestServer(t, nil)
	recorder := opsRequest(t, server, "POST", "/api/service/nope", nil, "none")
	if got := recorder.Result().StatusCode; got != http.StatusUnprocessableEntity {
		t.Fatalf("未知动作应 422（先于鉴权），实际 %d：%s", got, recorder.Body.String())
	}
	recorder = opsRequest(t, server, "POST", "/api/service/status_amkr", nil, "none")
	if got := recorder.Result().StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("合法动作无凭据应 401，实际 %d：%s", got, recorder.Body.String())
	}
	recorder = opsRequest(t, server, "POST", "/api/service/status_amkr", nil, "invalid")
	if got := recorder.Result().StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("合法动作 + 非法凭据应 401，实际 %d：%s", got, recorder.Body.String())
	}
}

// TestOpsServiceConfigPathCheckedBeforeSeam 顺序锁：配置文件存在性先于接缝调用。
//
// ops_api.py:190 的 config_path() 在 _run_service_action 之前，因此配置文件不在磁盘
// 上时得到 409，而不是「未接线」的 500。
func TestOpsServiceConfigPathCheckedBeforeSeam(t *testing.T) {
	server, configPath := newOpsTestServer(t, nil)
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("删除配置失败: %v", err)
	}
	recorder := opsRequest(t, server, "POST", "/api/service/status_amkr", nil, "full")
	if got := recorder.Result().StatusCode; got != http.StatusConflict {
		t.Fatalf("配置缺失应 409，实际 %d：%s", got, recorder.Body.String())
	}
	if want := `{"detail":"配置文件不存在: ` + jsonEscapedPath(configPath) + `"}`; recorder.Body.String() != want {
		t.Errorf("响应体不一致:\n got=%s\nwant=%s", recorder.Body.String(), want)
	}
}

// opsFakeError 是测试用的接缝错误，带 Python 风格的类名。
type opsFakeError struct {
	name    string
	message string
}

func (e *opsFakeError) Error() string { return e.message }
func (e *opsFakeError) PyErrorName() string {
	return e.name
}

// TestOpsIntegrationsSeam 覆盖接线后三条集成路由的形状与错误隔离。
func TestOpsIntegrationsSeam(t *testing.T) {
	statuses := map[string]OpsAgentStatus{
		"claude-code": {TargetPath: "/tmp/claude.json", BackupAvailable: true, CurrentIsApplied: true},
		"codex":       {TargetPath: "/tmp/codex.toml"},
		// pi-agent 探测失败：只应影响它自己那一条。
	}
	src := &OpsIntegrations{
		SupportedAgents: opsSupportedAgents,
		DisplayName:     func(agent string) string { return "显示名:" + agent },
		Status: func(agent string) (OpsAgentStatus, error) {
			if agent == "pi-agent" {
				return OpsAgentStatus{}, &opsFakeError{name: "AgentConfigError", message: "探测失败"}
			}
			return statuses[agent], nil
		},
		Configure: func(agent string, cfg *config.RouterConfig) (OpsAgentConfigResult, error) {
			if agent == "codex" {
				return OpsAgentConfigResult{}, &opsFakeError{name: "AgentConfigError", message: "写不了"}
			}
			return OpsAgentConfigResult{
				TargetPath:       "/tmp/" + agent,
				BackupPath:       "/tmp/" + agent + ".bak",
				RouterURL:        "http://127.0.0.1:8000",
				ExtraTargetPaths: []string{"/tmp/extra"},
			}, nil
		},
		Rollback: func(agent string) (OpsAgentRollbackResult, error) {
			return OpsAgentRollbackResult{TargetPath: "/tmp/" + agent, Restored: true}, nil
		},
	}
	server, _ := newOpsTestServer(t, func(s *Server) { s.Integrations = src })

	recorder := opsRequest(t, server, "GET", "/api/integrations", nil, "full")
	if got := recorder.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("状态码: %d %s", got, recorder.Body.String())
	}
	want := `{"integrations":[` +
		`{"agent":"claude-code","display_name":"显示名:claude-code","target_path":"/tmp/claude.json","target_exists":false,` +
		`"backup_available":true,"current_is_applied":true,"error":null},` +
		`{"agent":"codex","display_name":"显示名:codex","target_path":"/tmp/codex.toml","target_exists":false,` +
		`"backup_available":false,"current_is_applied":false,"error":null},` +
		`{"agent":"pi-agent","display_name":"显示名:pi-agent","target_path":"","target_exists":false,` +
		`"backup_available":false,"current_is_applied":false,"error":"AgentConfigError: 探测失败"}]}`
	if got := recorder.Body.String(); got != want {
		t.Errorf("列表响应体不一致:\n got=%s\nwant=%s", got, want)
	}

	// 应用成功：integration 是**写操作之后**重新读到的状态。
	recorder = opsRequest(t, server, "POST", "/api/integrations/claude-code", []byte(`{"mode":"unified-model"}`), "full")
	if got := recorder.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("状态码: %d %s", got, recorder.Body.String())
	}
	want = `{"integration":{"agent":"claude-code","display_name":"显示名:claude-code","target_path":"/tmp/claude.json",` +
		`"target_exists":false,"backup_available":true,"current_is_applied":true,"error":null},` +
		`"target_path":"/tmp/claude-code","backup_path":"/tmp/claude-code.bak","router_url":"http://127.0.0.1:8000",` +
		`"extra_target_paths":["/tmp/extra"]}`
	if got := recorder.Body.String(); got != want {
		t.Errorf("应用响应体不一致:\n got=%s\nwant=%s", got, want)
	}

	// 应用失败：409 + 原样错误文本（ops_api.py:221-222）。
	recorder = opsRequest(t, server, "POST", "/api/integrations/codex", []byte(`{"mode":"native"}`), "full")
	if got := recorder.Result().StatusCode; got != http.StatusConflict {
		t.Fatalf("失败应 409，实际 %d：%s", got, recorder.Body.String())
	}
	if want := `{"detail":"写不了"}`; recorder.Body.String() != want {
		t.Errorf("失败响应体不一致:\n got=%s\nwant=%s", recorder.Body.String(), want)
	}

	// 旧请求体的兼容：`mode` 字段仍被接受（值被忽略），不会 422。
	recorder = opsRequest(t, server, "POST", "/api/integrations/claude-code", []byte(`{"mode":"native"}`), "full")
	if got := recorder.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("旧请求体应 200（mode 被忽略），实际 %d：%s", got, recorder.Body.String())
	}

	recorder = opsRequest(t, server, "POST", "/api/integrations/codex/rollback", nil, "full")
	if got := recorder.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("回退应 200，实际 %d：%s", got, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"restored":true`) ||
		!strings.Contains(recorder.Body.String(), `"target_path":"/tmp/codex"`) {
		t.Errorf("回退响应体不符: %s", recorder.Body.String())
	}
}

// TestOpsIntegrationEntryErrorText 锁定单 Agent 错误文本的两条分支。
//
// 参照实现写 `f"{type(exc).__name__}: {exc}"`（ops_api.py:265）。Go 的错误类型没有
// 稳定的短名，因此接缝可以可选地实现 PyErrorName() 来逐字对齐；未实现时退化为
// err.Error()——这是一条**已知分歧**，这里显式锁定，避免以后被当成 bug「修掉」。
func TestOpsIntegrationEntryErrorText(t *testing.T) {
	src := &OpsIntegrations{
		DisplayName: func(agent string) string { return agent },
		Status: func(agent string) (OpsAgentStatus, error) {
			if agent == "codex" {
				return OpsAgentStatus{}, errors.New("裸错误")
			}
			return OpsAgentStatus{}, &opsFakeError{name: "AgentConfigError", message: "带名字"}
		},
	}
	entry := opsIntegrationEntry("codex", src)
	if got := entry.Lookup("error").StringValue(); got != "裸错误" {
		t.Errorf("未实现 PyErrorName 时应只用 err.Error(): %s", got)
	}
	entry = opsIntegrationEntry("claude-code", src)
	if got := entry.Lookup("error").StringValue(); got != "AgentConfigError: 带名字" {
		t.Errorf("实现 PyErrorName 时应与参照逐字一致: %s", got)
	}
}

// TestOpsToolUsesUpdatecheckWhenSeamAbsent 证明接缝为 nil 时走的是真实实现。
//
// 不联网：用 UpdateFetcher 注入假取回器，覆盖「GitHub 命中」与「网络失败」两条路径，
// 从而把 internal/updatecheck 的返回值到 HTTP 响应体的映射一并验证。
//
// 走 GitHub 而非 PyPI：Python 退役后 CheckLatestVersion 只查 GitHub Releases
// （原 PyPI 优先会在 PyPI 冻结后永远不回退 GitHub）。
func TestOpsToolUsesUpdatecheckWhenSeamAbsent(t *testing.T) {
	server, _ := newOpsTestServer(t, func(s *Server) {
		s.Version = "1.2.3"
		s.UpdateFetcher = func(url string, headers map[string]string, timeout time.Duration) (*canonical.Value, error) {
			if !strings.Contains(url, "api.github.com") {
				return nil, errors.New("offline")
			}
			return canonical.ParseString(`{"tag_name":"v1.3.0","html_url":"https://example.test/1.3.0"}`)
		}
	})
	recorder := opsRequest(t, server, "GET", "/api/tool", nil, "full")
	if got := recorder.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("状态码: %d %s", got, recorder.Body.String())
	}
	want := `{"version":"1.2.3","latest_version":"1.3.0","update_available":true,` +
		`"release_url":"https://example.test/1.3.0","source":"GitHub","error":null,` +
		`"webui_available":false,"webui_enabled":false,"webui_mounted":false,"webui_path":null}`
	if got := recorder.Body.String(); got != want {
		t.Errorf("响应体不一致:\n got=%s\nwant=%s", got, want)
	}

	// 两个源都失败：latest_version 为 null、update_available 为 false、error 有文本。
	offline, _ := newOpsTestServer(t, func(s *Server) {
		s.Version = "1.2.3"
		s.UpdateFetcher = func(url string, headers map[string]string, timeout time.Duration) (*canonical.Value, error) {
			return nil, errors.New("offline")
		}
	})
	recorder = opsRequest(t, offline, "GET", "/api/tool", nil, "full")
	body := recorder.Body.String()
	if !strings.Contains(body, `"latest_version":null`) ||
		!strings.Contains(body, `"update_available":false`) ||
		!strings.Contains(body, `"error":"offline"`) {
		t.Errorf("离线响应体不符: %s", body)
	}
}

// TestOpsToolSeamGuardsMissingLatestVersion 锁定 `latest_version and ...` 短路。
//
// update.py:52-54 的 update_available 是 `bool(latest_version and is_newer_version(...))`，
// 因此接缝即使把 UpdateAvailable 填成 true、只要没有最新版本号就必须是 false。
func TestOpsToolSeamGuardsMissingLatestVersion(t *testing.T) {
	server, _ := newOpsTestServer(t, func(s *Server) {
		s.CheckUpdate = func(timeout float64) UpdateCheckResult {
			return UpdateCheckResult{CurrentVersion: "1.2.3", UpdateAvailable: true}
		}
	})
	recorder := opsRequest(t, server, "GET", "/api/tool", nil, "full")
	if !strings.Contains(recorder.Body.String(), `"update_available":false`) {
		t.Errorf("没有最新版本号时应恒为 false: %s", recorder.Body.String())
	}
}

// TestOpsWebUIFieldsMatchHealthShape 锁定 /api/tool 的 webui_* 与 /health 同形同序。
//
// 两处都来自 webui_status(app)（webui.py:48-63），字段顺序也是响应契约的一部分；
// 这个用例把「ops 的末尾四个字段」与「health.Build 的末尾四个字段」逐字节比一遍，
// 以后任何一侧改字段名或顺序都会立刻失败。
func TestOpsWebUIFieldsMatchHealthShape(t *testing.T) {
	state := health.WebUI{Available: true, Enabled: true, Mounted: true, MountPrefix: "/amkr/"}
	server, _ := newOpsTestServer(t, func(s *Server) {
		s.CheckUpdate = func(timeout float64) UpdateCheckResult {
			return UpdateCheckResult{CurrentVersion: "1.2.3"}
		}
		s.WebUIStatus = func() health.WebUI { return state }
	})
	recorder := opsRequest(t, server, "GET", "/api/tool", nil, "full")
	opsBody, err := canonical.ParseString(recorder.Body.String())
	if err != nil {
		t.Fatalf("解析 /api/tool 响应失败: %v", err)
	}
	healthBody := health.Build(health.Inputs{
		Version:     "1.2.3",
		LocalAPIKey: "local-key",
		OpsEnabled:  true,
		WebUI:       state,
	})

	opsKeys := opsBody.Obj.Keys()
	if len(opsKeys) != 10 {
		t.Fatalf("/api/tool 字段数不对: %v", opsKeys)
	}
	opsTail := opsKeys[len(opsKeys)-4:]
	healthKeys := healthBody.Obj.Keys()
	healthTail := healthKeys[len(healthKeys)-4:]
	for i := range opsTail {
		if opsTail[i] != healthTail[i] {
			t.Fatalf("webui_* 字段名/顺序不一致: ops=%v health=%v", opsTail, healthTail)
		}
		opsValue, _ := opsBody.Obj.Get(opsTail[i])
		healthValue, _ := healthBody.Obj.Get(healthTail[i])
		if canonical.DumpsOrdered(opsValue) != canonical.DumpsOrdered(healthValue) {
			t.Errorf("%s 取值不一致: ops=%s health=%s",
				opsTail[i], canonical.DumpsOrdered(opsValue), canonical.DumpsOrdered(healthValue))
		}
	}
	// 挂载前缀要先 rstrip("/")（webui.py:67-70）。
	if got := opsBody.Lookup("webui_path").StringValue(); got != "/amkr/ui" {
		t.Errorf("webui_path = %q，期望 /amkr/ui", got)
	}
}

// TestOpsServiceTargetsMatchReference 锁定服务动作表本身。
//
// 表是 URL 契约的一部分（键集合决定 422 的边界），值部分供装配层实现
// RunServiceAction 时按同一份映射分派；这里逐条对照 ops_api.py:50-62。
func TestOpsServiceTargetsMatchReference(t *testing.T) {
	want := map[string]OpsServiceTarget{
		"start_amkr":            {Kind: "background-start"},
		"stop_amkr":             {Kind: "background-stop"},
		"restart_amkr":          {Kind: "background-restart"},
		"status_amkr":           {Kind: "system", Argument: "status"},
		"install_user_amkr":     {Kind: "system", Argument: "install-user"},
		"uninstall_amkr":        {Kind: "system", Argument: "uninstall"},
		"install_system_amkr":   {Kind: "system", Argument: "install"},
		"uninstall_system_amkr": {Kind: "system", Argument: "uninstall"},
		"start_system_amkr":     {Kind: "system", Argument: "start"},
		"stop_system_amkr":      {Kind: "system", Argument: "stop"},
		"restart_system_amkr":   {Kind: "system", Argument: "restart"},
	}
	if len(OpsServiceTargets) != len(want) {
		t.Fatalf("服务动作数量不对: %d", len(OpsServiceTargets))
	}
	for action, target := range want {
		got, present := OpsServiceTargets[action]
		if !present {
			t.Errorf("缺少服务动作: %s", action)
			continue
		}
		if got != target {
			t.Errorf("服务动作 %s 的分派目标不一致: got %+v want %+v", action, got, target)
		}
	}
}

// TestOpsLogsTailRule 直接锁定截断与宽容解码的边界规则。
//
// 规则本身由 HTTP 层已经覆盖，这里把取值写成独立用例是为了失败时能一眼看懂
// 「是 > 而不是 >=」「最大子部分只产一个 U+FFFD」。
func TestOpsLogsTailRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	cases := []struct {
		name          string
		content       []byte
		wantTruncated bool
		wantTail      string
	}{
		{"小于窗口", []byte("abc"), false, "abc"},
		{"恰好窗口", bytes.Repeat([]byte{'a'}, 8), false, strings.Repeat("a", 8)},
		{"超一字节", bytes.Repeat([]byte{'a'}, 9), true, strings.Repeat("a", 8)},
		// 10 字节 -> 从 offset=2 起读：a + 截断的 中（e4 b8 + 非续字节 X）+ Xcdef。
		{"边界切在最大子部分", []byte("aaa\xe4\xb8Xcdef"), true, "a\uFFFDXcdef"},
		// 9 字节 -> 从 offset=1 起读：末尾的孤立续字节 ad 产一个 U+FFFD。
		{"边界切出孤立续字节", []byte("abcdefgh\xad"), true, "bcdefgh\uFFFD"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if err := os.WriteFile(path, item.content, 0o644); err != nil {
				t.Fatalf("写入失败: %v", err)
			}
			text, truncated, err := tailLogFile(path, 8)
			if err != nil {
				t.Fatalf("读取失败: %v", err)
			}
			if truncated != item.wantTruncated {
				t.Errorf("truncated = %v，期望 %v", truncated, item.wantTruncated)
			}
			if text != item.wantTail {
				t.Errorf("text = %q，期望 %q", text, item.wantTail)
			}
		})
	}

	if _, _, err := tailLogFile(filepath.Join(dir, "missing.log"), 8); err == nil {
		t.Errorf("文件不存在时应返回错误")
	}
}

// —— 测试脚手架 —— //

// newOpsTestServer 构造一个带最小合法配置的运维面服务端。
//
// mutate 用于替换接缝；返回的 configPath 供断言 409 的 detail 与接缝入参。
func newOpsTestServer(t *testing.T, mutate func(*Server)) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "router-config.json")
	data := canonical.NewObject()
	data.SetKey("config_version", canonical.NewInt("4"))
	data.SetKey("host", canonical.NewString("127.0.0.1"))
	data.SetKey("port", canonical.NewInt("8000"))
	data.SetKey("local_api_key", canonical.NewString("local-key"))
	data.SetKey("log_file_path", canonical.NewString(filepath.Join(dir, "server.log")))
	data.SetKey("webui_enabled", canonical.NewBool(false))
	migrated, err := config.MigrateConfigData(data)
	if err != nil {
		t.Fatalf("迁移配置失败: %v", err)
	}
	if err := config.SaveConfigData(configPath, migrated); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	initial, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	server := &Server{
		ConfigPath:    configPath,
		OpsEnabled:    true,
		Version:       "1.2.3",
		CurrentConfig: func() *config.RouterConfig { return initial },
	}
	if mutate != nil {
		mutate(server)
	}
	return server, configPath
}

// opsRequest 发一次请求并返回 recorder；auth 取 "full" / "invalid" / "none"。
func opsRequest(t *testing.T, server *Server, method, path string, body []byte, auth string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	switch auth {
	case "none":
	case "invalid":
		request.Header.Set("Authorization", "Bearer "+testInvalidAPIKey)
	default:
		request.Header.Set("Authorization", "Bearer "+testLocalAPIKey)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// strPtr 返回字符串指针。
func strPtr(value string) *string { return &value }
