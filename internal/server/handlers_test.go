package server

import (
	"encoding/json"
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
)

// TestModelsExcludesUnconfiguredModelAndAppendsUnified 断言 /v1/models 的语义。
//
// 与「整体字节」层面的断言互补（键序、owned_by 等由 handlers.go 的编码路径决定），
// 这里把「哪些模型该出现」显式写出来，读者不必去读别处的 JSON：
//
//   - model-b 没有任何 key（未配置）⇒ 它自己与它的别名都不出现；
//   - unified-model 是**末尾追加**的（key_pool.available_model_ids 的 append 语义）。
//
// 访问密钥那份收窄清单由 TestModelsNarrowedForAccessKey 覆盖。
func TestModelsExcludesUnconfiguredModelAndAppendsUnified(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)

	full := modelIDs(t, serve(app, http.MethodGet, "/v1/models", fullAuthorization))
	for _, want := range []string{"alias-a", "alias-v", "alias-nv", "model-a", "model-v", "model-nv"} {
		if !contains(full, want) {
			t.Errorf("完整清单缺少 %q: %v", want, full)
		}
	}
	for _, unwanted := range []string{"model-b", "alias-b"} {
		if contains(full, unwanted) {
			t.Errorf("未配置的模型不应出现: %q（%v）", unwanted, full)
		}
	}
	if len(full) == 0 || full[len(full)-1] != "unified-model" {
		t.Errorf("unified-model 必须是最后一项: %v", full)
	}
}

// accessKeyFixture 是一份带访问密钥的配置文本。
//
// 三把 key 覆盖三种清单组合：
//   - 带清单的（providers=[prov-a], models=[model-a]）；
//   - 只管供应商的（providers=[prov-b]）；
//   - 停用的。
//
// 它们合起来验证 /v1/models 的配置与授权收窄（额度冻结不参与收窄）。
const accessKeyFixture = `{
  "config_version": 4,
  "local_api_key": "local-key",
  "host": "127.0.0.1",
  "port": 8000,
  "endpoint_capabilities_path": "<CAP>",
  "metrics_db_path": "<DB>",
  "log_file_path": "<LOG>",
  "webui_enabled": false,
  "ops_enabled": true,
  "providers": {
    "prov-a": {"base_url": "https://a.test", "keys": {"key-a": {"api_key": "sk-a"}}},
    "prov-b": {"base_url": "https://b.test", "keys": {"key-b": {"api_key": "sk-b"}}}
  },
  "models": {
    "model-a": {"targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}], "aliases": ["alias-a"]},
    "model-b": {"targets": [{"provider": "prov-b", "key": "key-b", "upstream_model": "model-b"}]}
  },
  "access_keys": {
    "scoped": {"name": "只管 prov-a", "key": "amkr_ak_scoped", "providers": ["prov-a"], "models": ["model-a"]},
    "provonly": {"name": "只管 prov-b", "key": "amkr_ak_provonly", "providers": ["prov-b"]},
    "off": {"name": "已停用", "key": "amkr_ak_off", "enabled": false}
  }
}`

// TestModelsNarrowedForAccessKey 固化 /v1/models 对访问密钥的收窄。
//
// 清单必须与 proxy 的判定一致：列了却调不动、或调得动却不在清单里，都会让接入方
// 以为自己配错了。四种情况：
//
//   - 两份清单都配 -> 只列交集里的那个模型名；
//   - 只配 providers -> 列该供应商能服务的全部模型名；
//   - unified-model 一律不列（访问密钥用不了它）；
//   - 停用的 key -> 403，且**不是** 401（停用是可恢复的配置状态，不是错误凭据）。
func TestModelsNarrowedForAccessKey(t *testing.T) {
	app := newTestAppWith(t, t.TempDir(), appFixture{opsEnabled: true, configText: accessKeyFixture})

	scoped := modelIDs(t, serve(app, http.MethodGet, "/v1/models", "Bearer amkr_ak_scoped"))
	if len(scoped) != 1 || scoped[0] != "model-a" {
		t.Errorf("两份清单都配时应只列交集，实得 %v", scoped)
	}
	if contains(scoped, "model-b") {
		t.Errorf("清单外的模型不该出现: %v", scoped)
	}
	if contains(scoped, "unified-model") {
		t.Errorf("访问密钥用不了 unified-model，不该列出: %v", scoped)
	}

	providerOnly := modelIDs(t, serve(app, http.MethodGet, "/v1/models", "Bearer amkr_ak_provonly"))
	if len(providerOnly) != 1 || providerOnly[0] != "model-b" {
		t.Errorf("只配 providers 时应列该供应商的全部模型，实得 %v", providerOnly)
	}

	disabled := serve(app, http.MethodGet, "/v1/models", "Bearer amkr_ak_off")
	if disabled.Code != http.StatusForbidden {
		t.Errorf("停用的访问密钥应 403，实得 %d（body=%s）", disabled.Code, disabled.Body.String())
	}
}

// TestVisitorKeyIsRejected 断言取消访客模式后固定访客 key 不再被接受。
//
// 这是那条「删干净」的回归线：只要有人把 amkr-visitor 特例加回来，这里就会红。
func TestVisitorKeyIsRejected(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	recorder := serve(app, http.MethodGet, "/v1/models", "Bearer amkr-visitor")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("amkr-visitor 应被当成错误凭据（401），实得 %d（body=%s）",
			recorder.Code, recorder.Body.String())
	}
}

// workspaceModelsFixture 是一份带作用域推理凭据的配置文本。
//
// teamA 有 models 清单（只许直呼 model-a）；teamB 没有清单（不限制）。两者各有自己的
// 任务，用来验证「没配清单时清单退化成任务名」这条回退语义。
const workspaceModelsFixture = `{
  "config_version": 4,
  "local_api_key": "local-key",
  "host": "127.0.0.1",
  "port": 8000,
  "endpoint_capabilities_path": "<CAP>",
  "metrics_db_path": "<DB>",
  "log_file_path": "<LOG>",
  "webui_enabled": false,
  "ops_enabled": true,
  "providers": {"prov-a": {"base_url": "https://a.test", "keys": {"key-a": {"api_key": "sk-a"}}}},
  "models": {
    "model-a": {"targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}]},
    "model-b": {"targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-b"}]}
  },
  "tasks": {"only-b": {"workspace": "teamB", "model": "model-b"}},
  "workspaces": {
    "teamA": {"inference_key": "ia", "models": ["model-a"]},
    "teamB": {"inference_key": "ib", "tasks": {"only-b": {"model": "model-b"}}}
  }
}`

// TestModelsNarrowedForScopedInferenceKey 固化 /v1/models 对作用域凭据的收窄。
//
// 清单必须与 proxy 的判定一致：列了却调不动、或调得动却不在清单里，都会让接入方
// 以为自己配错了。三种情况：
//
//   - teamA 配了清单 -> 只列清单里的 model-a；
//   - teamB 没配清单 -> 退化成它的**任务名**（那是它天然被授权调用的东西）；
//   - 未知 key -> 401（作用域凭据不是万能通行证）。
func TestModelsNarrowedForScopedInferenceKey(t *testing.T) {
	app := newTestAppWith(t, t.TempDir(), appFixture{opsEnabled: true, configText: workspaceModelsFixture})

	restricted := modelIDs(t, serve(app, http.MethodGet, "/v1/models", "Bearer ia"))
	if len(restricted) != 1 || restricted[0] != "model-a" {
		t.Errorf("配了清单的空间应只看到清单内容，实得 %v", restricted)
	}
	// unified-model 是全局计划，作用域凭据用不了，因此不该出现。
	if contains(restricted, "unified-model") {
		t.Errorf("作用域清单不应含 unified-model: %v", restricted)
	}

	byTask := modelIDs(t, serve(app, http.MethodGet, "/v1/models", "Bearer ib"))
	if len(byTask) != 1 || byTask[0] != "only-b" {
		t.Errorf("没配清单的空间应看到自己的任务名，实得 %v", byTask)
	}

	unknown := serve(app, http.MethodGet, "/v1/models", "Bearer nope")
	if unknown.Code != http.StatusUnauthorized {
		t.Errorf("未知 key 应 401，实得 %d（body=%s）", unknown.Code, unknown.Body.String())
	}
}

// modelIDs 从 /v1/models 响应里取出 id 列表。
func modelIDs(t *testing.T, recorder *httptest.ResponseRecorder) []string {
	t.Helper()
	parsed, err := canonical.ParseString(recorder.Body.String())
	if err != nil {
		t.Fatalf("解析 /v1/models 响应失败: %v（body=%s）", err, recorder.Body.String())
	}
	items := parsed.Lookup("data")
	if items == nil || !items.IsArray() {
		t.Fatalf("响应里没有 data 数组: %s", recorder.Body.String())
	}
	ids := make([]string, 0, len(items.Arr))
	for _, item := range items.Arr {
		ids = append(ids, item.Lookup("id").StringValue())
	}
	return ids
}

// contains 报告字符串切片是否包含目标。
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// TestUnauthorizedBodiesMatchReference 锁定两处 401 的信封差异。
//
// /v1/models 与三条 metrics 路由用 proxy 风格的 {"error":{"message":...}}，而管理 API
// 用 FastAPI 风格的 {"detail":...}；两者都在参照实现里出现过，不能统一。
func TestUnauthorizedBodiesMatchReference(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	const appSurface = `{"error":{"message":"本地 API key 验证失败"}}`
	for _, path := range []string{
		"/v1/models", "/metrics", "/metrics/requests", "/metrics/series",
	} {
		recorder := serve(app, http.MethodGet, path, "")
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("GET %s 状态码 = %d，期望 401", path, recorder.Code)
		}
		if recorder.Body.String() != appSurface {
			t.Errorf("GET %s 响应体 = %s，期望 %s", path, recorder.Body.String(), appSurface)
		}
	}
	management := serve(app, http.MethodGet, "/api/providers", "")
	if management.Body.String() != `{"detail":"本地 API key 验证失败"}` {
		t.Errorf("管理 API 的 401 应用 detail 信封: %s", management.Body.String())
	}
}

// TestHealthReportsAssembledInputs 断言 /health 的取数来自装配层。
//
// 响应体的字段顺序与语义由 internal/health 自己的用例锁定；这里只钉住「装配层喂进去的
// 是哪些值」——尤其是 local_api_key 的指纹（它证明真的把运行时配置里的 key 传下去了）
// 与 config_path（绝对路径），这两项一旦漏传会静默变成空串。
func TestHealthReportsAssembledInputs(t *testing.T) {
	dir := t.TempDir()
	app := newTestApp(t, dir, nil)
	recorder := serve(app, http.MethodGet, "/health", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /health 状态码 = %d，期望 200（body=%s）",
			recorder.Code, recorder.Body.String())
	}
	parsed, err := canonical.ParseString(recorder.Body.String())
	if err != nil {
		t.Fatalf("解析 /health 失败: %v", err)
	}

	if got := parsed.Lookup("status").StringValue(); got != "ok" {
		t.Errorf("status = %q", got)
	}
	if got := parsed.Lookup("version").StringValue(); got != testVersion {
		t.Errorf("version = %q，期望 %q", got, testVersion)
	}
	// "local-key" 的 sha256 前 12 位（与真实 Python /health 的实测值一致）。
	if got := parsed.Lookup("local_api_key_fingerprint").StringValue(); got != "dba73d3fcb67" {
		t.Errorf("local_api_key_fingerprint = %q，期望 dba73d3fcb67", got)
	}
	if got := parsed.Lookup("local_auth_enabled"); got == nil || !got.Bool {
		t.Errorf("local_auth_enabled = %v，期望 true", got)
	}
	wantPath := filepath.Join(dir, "router-config.json")
	if got := parsed.Lookup("config_path").StringValue(); got != wantPath {
		t.Errorf("config_path = %q，期望绝对路径 %q", got, wantPath)
	}
	if got := parsed.Lookup("models"); got == nil || !got.IsArray() || len(got.Arr) == 0 {
		t.Errorf("models 应非空: %v", got)
	}
	if got := parsed.Lookup("ops_enabled"); got == nil || !got.Bool {
		t.Errorf("ops_enabled = %v，期望 true（配置里是 true）", got)
	}
	if got := parsed.Lookup("webui_path"); got == nil || !got.IsNull() {
		t.Errorf("未挂载时 webui_path 必须是 null: %v", got)
	}
}

// TestHealthOpsFlagFollowsOverride 断言 ops_enabled 的覆盖语义（app.py:142-146）。
func TestHealthOpsFlagFollowsOverride(t *testing.T) {
	// 配置里 ops_enabled=true，但显式覆盖为 false。
	off := false
	app := newTestAppWith(t, t.TempDir(), appFixture{
		opsEnabled: true,
		mutate:     func(options *Options) { options.OpsEnabled = &off },
	})
	parsed, err := canonical.ParseString(serve(app, http.MethodGet, "/health", "").Body.String())
	if err != nil {
		t.Fatalf("解析 /health 失败: %v", err)
	}
	if got := parsed.Lookup("ops_enabled"); got == nil || got.Bool {
		t.Errorf("ops_enabled = %v，期望 false（被 Options.OpsEnabled 覆盖）", got)
	}
	// 覆盖只影响应用层开关：api 侧同样不注册运维路由。
	if recorder := serve(app, http.MethodGet, "/api/logs", ""); recorder.Code != http.StatusNotFound {
		t.Errorf("覆盖为 false 时 GET /api/logs 状态码 = %d，期望 404", recorder.Code)
	}
}

// TestActiveRequestsReflectsInflightProxyRequest 是 /v1/ 包装层的端到端证据。
//
// 参照实现里 app.py:354 在进入 proxy 前 acquire_active()、在流式响应结束或非流式响应
// 写完后 release_active()，因此 /metrics 的 active_requests 在一次真实代理请求期间是 1。
// Go 侧同样在 handleProxy 里成对调用（defer 覆盖整条同步流），这里用「上游慢响应」
// 把那个窗口拉到可观测：
//
//	发起代理请求 → 等它进入上游等待 → GET /metrics 必须看到 active_requests=1
//	                → 等响应返回     → 再查必须是 0
//
// 用真实 httptest 上游（而不是桩）是为了同时验证装配层的上游客户端接缝真的通了。
func TestActiveRequestsReflectsInflightProxyRequest(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		// 用带 choices 的**正常** chat completion：空 choices 的 2xx 会被 proxy
		// 按「空响应」故障重试/回 502（见 internal/proxy 的 empty_completion_test.go），
		// 与本用例要验证的 active_requests 窗口无关。
		_, _ = io.WriteString(w,
			`{"id":"chatcmpl-1","object":"chat.completion",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	// 把夹具里的第一个 provider 指向上游测试服务器：POST 探针会走到它。
	path := writeFixture(t, dir, true, false)
	rewriteBaseURL(t, path, upstream.URL)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()

	activeRequests := func() int {
		recorder := serve(app, http.MethodGet, "/metrics?hours=1", fullAuthorization)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /metrics 状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		parsed, parseErr := canonical.ParseString(recorder.Body.String())
		if parseErr != nil {
			t.Fatalf("解析 /metrics 失败: %v", parseErr)
		}
		value, _ := parsed.Lookup("active_requests").AsFloat()
		return int(value)
	}

	body := `{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`
	done := make(chan int, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", fullAuthorization)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		done <- recorder.Code
	}()

	// 轮询等待请求进入上游等待窗口（最多 2 秒），此时活跃数必须是 1。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if activeRequests() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("上游等待期间 active_requests 始终不是 1")
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(release)
	if status := <-done; status != http.StatusOK {
		t.Fatalf("代理请求状态码 = %d，期望 200", status)
	}
	// 响应写完后必须归零（否则计数会随请求数单调增长）。
	if got := activeRequests(); got != 0 {
		t.Errorf("请求结束后 active_requests = %d，期望 0", got)
	}
}

// TestStreamingProxyRequestRecordsTokens 是「流式请求的 token 用量真的进库」的端到端
// 证据，覆盖 WebUI 总览页「Token 用量」显示为 0 的那个真实故障。
//
// 链路：POST /v1/chat/completions(stream) → proxy 原样转发流 → 抽取上游 SSE 里的 usage
// → metrics.Store.Record → GET /metrics 的 total.total_tokens。
//
// 用真实 httptest 上游（而非桩）把每一段都串起来：只有原样转发流自己解析 usage，
// 这个数字才不会是 0。
func TestStreamingProxyRequestRecordsTokens(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"+
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":22,"+
				"\"total_tokens\":33}}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	path := writeFixture(t, dir, true, false)
	rewriteBaseURL(t, path, upstream.URL)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()

	body := `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", fullAuthorization)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("代理请求状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}

	snapshot := serve(app, http.MethodGet, "/metrics?hours=1", fullAuthorization)
	if snapshot.Code != http.StatusOK {
		t.Fatalf("GET /metrics 状态码 = %d（body=%s）", snapshot.Code, snapshot.Body.String())
	}
	parsed, parseErr := canonical.ParseString(snapshot.Body.String())
	if parseErr != nil {
		t.Fatalf("解析 /metrics 失败: %v", parseErr)
	}
	total := parsed.Lookup("total")
	if total == nil {
		t.Fatalf("/metrics 缺少 total 段: %s", snapshot.Body.String())
	}
	want := map[string]string{
		"requests": "1", "prompt_tokens": "11", "completion_tokens": "22", "total_tokens": "33",
	}
	for field, value := range want {
		if got := total.Lookup(field).PyStr(); got != value {
			t.Errorf("total.%s = %s，期望 %s", field, got, value)
		}
	}
}

// rewriteBaseURL 把配置里第一个 provider 的 base_url 换成本地测试上游。
func rewriteBaseURL(t *testing.T, path, baseURL string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	text := strings.Replace(string(raw), "https://a.example.test", baseURL, 1)
	text = strings.Replace(text, "https://b.example.test", baseURL, 1)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
}

// TestMetricsSnapshotSeesKeyStatsWiring 断言管理 API 的 key_stats 接缝接到了当前代
// 的指标库上（否则 /api/models/{id}/keys/{name}/stats 会回 500「未接入」）。
func TestMetricsSnapshotSeesKeyStatsWiring(t *testing.T) {
	app := newTestApp(t, t.TempDir(), func(options *Options) {
		options.CheckUpdate = stubCheckUpdate
	})
	recorder := serve(app, http.MethodGet,
		"/api/models/model-a/keys/key-a/stats", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET key stats 状态码 = %d，期望 200（body=%s）",
			recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 key stats 响应失败: %v", err)
	}
	if payload["model_id"] != "model-a" || payload["key_name"] != "key-a" {
		t.Errorf("key stats 的 model/key 不对: %v", payload)
	}
	if _, ok := payload["stats"]; !ok {
		t.Errorf("key stats 缺少 stats 字段: %v", payload)
	}
}
