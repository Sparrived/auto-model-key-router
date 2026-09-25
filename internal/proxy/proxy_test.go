package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// --- 测试脚手架 ---------------------------------------------------------------

// testEnv 是一次请求所需的全部装配，并暴露可断言的观测面。
type testEnv struct {
	handler   *Handler
	cfg       *config.RouterConfig
	pool      *keypool.KeyPool
	manager   *runtime.RuntimeManager
	resources *runtime.RuntimeResources
	metrics   *recordingMetrics
	transport *scriptedTransport
}

// testModel 构造一个测试模型（默认 native_first=false，与参照实现里
// 「模型未显式开启原生」的常见形态一致）。
func testModel(id string, keys []config.KeyConfig, mutate ...func(*config.ModelConfig)) config.ModelConfig {
	model := config.ModelConfig{ID: id, Keys: keys, RoutingMode: "round_robin"}
	for _, apply := range mutate {
		apply(&model)
	}
	return model
}

// testKey 构造一个测试 key。
func testKey(name string, baseURL string) config.KeyConfig {
	return config.KeyConfig{
		Name:    name,
		APIKey:  "sk-" + name,
		BaseURL: baseURL,
		Enabled: true,
	}
}

// newTestEnv 装配一套测试环境。routes 是脚本化的上游响应。
// newTestEnvPreservingOptions 与 newTestEnv 相同，但**不覆盖任何 Options**。
//
// newTestEnv 为了让无关测试不被上游调用上限截断，会把 MaxUpstreamCallsPerRequest
// 抬到至少 10000；那样就永远验证不到「未设置时采用默认上限」这条路径。需要验证
// 默认值本身的测试用这个入口。
func newTestEnvPreservingOptions(t *testing.T, cfg *config.RouterConfig, options Options) *testEnv {
	return newTestEnvRaw(t, cfg, options)
}

// newTestEnv 构造测试环境，并把上游调用上限抬高以免与用例无关的上限截断相互干扰。
func newTestEnv(t *testing.T, cfg *config.RouterConfig, options Options) *testEnv {
	options.MaxUpstreamCallsPerRequest = maxInt(options.MaxUpstreamCallsPerRequest, 10_000)
	return newTestEnvRaw(t, cfg, options)
}

func newTestEnvRaw(t *testing.T, cfg *config.RouterConfig, options Options) *testEnv {
	t.Helper()
	if cfg.LocalAPIKey == "" {
		cfg.LocalAPIKey = "local-key"
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 10
	}
	if cfg.StreamFirstByteTimeout == 0 {
		cfg.StreamFirstByteTimeout = 60
	}
	if cfg.StreamIdleTimeout == 0 {
		cfg.StreamIdleTimeout = 60
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 1
	}
	if cfg.KeyFailureThreshold == 0 {
		cfg.KeyFailureThreshold = 2
	}
	if cfg.KeyCooldownSeconds == 0 {
		cfg.KeyCooldownSeconds = 60
	}
	if cfg.ReasoningEffortByModel == nil {
		cfg.ReasoningEffortByModel = map[string]string{}
	}
	pool := keypool.New(cfg, nil, nil)
	transport := newScriptedTransport(nil)
	metrics := &recordingMetrics{}
	resources := runtime.NewRuntimeResources(cfg, pool, nil, nil)
	resources.HTTPClient = &stubUpstreamClient{transport: transport}
	manager := runtime.NewRuntimeManager(resources)
	if options.Logger == nil {
		options.Logger = discardLogger()
	}
	return &testEnv{
		handler:   New(manager, metrics, options),
		cfg:       cfg,
		pool:      pool,
		manager:   manager,
		resources: resources,
		metrics:   metrics,
		transport: transport,
	}
}

// maxInt 返回两者较大值（0 表示未设置时取 fallback）。
func maxInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// route 注册一条脚本化路由。
func (e *testEnv) route(path string, steps ...upstreamStep) {
	if e.transport.routes == nil {
		e.transport.routes = map[string][]upstreamStep{}
	}
	e.transport.routes[path] = steps
}

// request 发一次下游请求并返回记录器。
func (e *testEnv) request(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, "http://testserver/v1/"+path, strings.NewReader(body))
	if _, ok := headers["Authorization"]; !ok {
		req.Header.Set("Authorization", "Bearer "+e.cfg.LocalAPIKey)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	e.handler.Handle(recorder, req, path)
	return recorder
}

// jsonStep 构造一个 JSON 上游响应。
func jsonStep(status int, body string) upstreamStep {
	return upstreamStep{Status: status, Headers: map[string]string{"content-type": "application/json"}, Body: body}
}

// sseStep 构造一个 SSE 上游响应。
func sseStep(chunks ...string) upstreamStep {
	return upstreamStep{
		Status:  200,
		Headers: map[string]string{"content-type": "text/event-stream"},
		Chunks:  chunks,
	}
}

// simpleChatConfig 是「单模型单 key」的常用配置。
func simpleChatConfig() *config.RouterConfig {
	return &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("vendor-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
		},
	}
}

// --- 决策 3：畸形请求体在 HTTP 边界被拒绝 -------------------------------------

// TestMalformedBodyIsRejected 固化产品决策 3：非对象 / 类型错误的请求体返回显式
// 400，而不是参照实现的「静默变 {} ⇒ 400 缺少 model 字段」。
func TestMalformedBodyIsRejected(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantBody string
	}{
		{
			name:     "非对象_数组",
			body:     "[1,2,3]",
			wantBody: `{"error":{"message":"请求体必须是 JSON 对象，实际是 list"}}`,
		},
		{
			name:     "非对象_字符串",
			body:     `"hello"`,
			wantBody: `{"error":{"message":"请求体必须是 JSON 对象，实际是 str"}}`,
		},
		{
			name:     "非对象_null",
			body:     "null",
			wantBody: `{"error":{"message":"请求体必须是 JSON 对象，实际是 NoneType"}}`,
		},
		{
			name:     "非法JSON",
			body:     `{"model":`,
			wantBody: `{"error":{"message":"请求体不是合法的 JSON"}}`,
		},
		{
			// 空体不是「畸形」：参照实现走 `if not body: return {}`，因此保留它的
			// 文案（缺少 model 字段），在两档策略下都一样。
			name:     "空体_保留参照文案",
			body:     "",
			wantBody: `{"error":{"message":"请求体中缺少 model 字段"}}`,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyStrict})
			recorder := env.request(http.MethodPost, "chat/completions", item.body, nil)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("状态码: got %d want 400", recorder.Code)
			}
			if got := recorder.Body.String(); got != item.wantBody {
				t.Fatalf("响应体:\n got=%s\nwant=%s", got, item.wantBody)
			}
			// 畸形体不该触达上游。
			if len(env.transport.calls) != 0 {
				t.Fatalf("畸形体不应触发上游调用，实得 %v", describeUpstreams(env.transport.calls))
			}
		})
	}
}

// TestPythonBodyPolicyIsByteCompatible 确认「参照档」仍可逐字节对齐：两者对畸形
// 体都返回 400「请求体中缺少 model 字段」。
func TestPythonBodyPolicyIsByteCompatible(t *testing.T) {
	for _, body := range []string{"[1,2,3]", `"hello"`, `{"model":`, "null", ""} {
		env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyPython})
		recorder := env.request(http.MethodPost, "chat/completions", body, nil)
		got := recorder.Body.String()
		want := `{"error":{"message":"请求体中缺少 model 字段"}}`
		if got != want {
			t.Fatalf("body=%q 响应体: got=%s want=%s", body, got, want)
		}
	}
}

// --- 决策 2：multipart/form-data ----------------------------------------------

// TestImagesEditsMultipartRejectedByDefault 固化默认档：明确拒绝而不是静默改写。
func TestImagesEditsMultipartRejectedByDefault(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{Multipart: MultipartReject})
	body, contentType := buildMultipart(t, map[string]string{"model": "vendor-model", "prompt": "x"})
	recorder := env.request(http.MethodPost, "images/edits", body,
		map[string]string{"Content-Type": contentType})
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("状态码: got %d want 415（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "multipart/form-data 请求体不被支持") {
		t.Fatalf("响应体应说明原因，实得 %s", recorder.Body.String())
	}
	if len(env.transport.calls) != 0 {
		t.Fatalf("拒绝时不应触达上游，实得 %v", describeUpstreams(env.transport.calls))
	}
}

// TestImagesEditsMultipartPassthrough 覆盖真正「修好 multipart」的那一档：从表单
// 字段取 model 用于路由，原始字节与原始 Content-Type 一并转发。
func TestImagesEditsMultipartPassthrough(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{Multipart: MultipartPassthrough})
	env.route("/v1/images/edits", jsonStep(200, `{"created":1,"data":[]}`))
	body, contentType := buildMultipart(t, map[string]string{"model": "vendor-model", "prompt": "a cat"})
	recorder := env.request(http.MethodPost, "images/edits", body,
		map[string]string{"Content-Type": contentType})

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != `{"created":1,"data":[]}` {
		t.Fatalf("响应体: got=%s", got)
	}
	if len(env.transport.calls) != 1 {
		t.Fatalf("上游调用数: got %d want 1", len(env.transport.calls))
	}
	call := env.transport.calls[0]
	if call.path != "/v1/images/edits" {
		t.Fatalf("上游路径: got %s", call.path)
	}
	if call.body != body {
		t.Fatalf("multipart 字节必须原样转发:\n got=%q\nwant=%q", call.body, body)
	}
	if got := call.headers["content-type"]; got != contentType {
		t.Fatalf("上游 Content-Type 必须保留原始 boundary: got=%q want=%q", got, contentType)
	}
}

// TestMultipartWithoutModel 覆盖「表单里没有 model 字段」：仍走 400 缺少 model，
// 与 JSON 路径一致。
func TestMultipartWithoutModel(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{Multipart: MultipartPassthrough})
	body, contentType := buildMultipart(t, map[string]string{"prompt": "x"})
	recorder := env.request(http.MethodPost, "images/edits", body,
		map[string]string{"Content-Type": contentType})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("状态码: got %d want 400", recorder.Code)
	}
}

// TestMultipartTooLarge 覆盖缓冲上限：显式 413 而不是把内存吃光。
func TestMultipartTooLarge(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{
		Multipart:         MultipartPassthrough,
		MaxMultipartBytes: 64,
	})
	body, contentType := buildMultipart(t, map[string]string{"model": "vendor-model", "prompt": strings.Repeat("x", 512)})
	recorder := env.request(http.MethodPost, "images/edits", body,
		map[string]string{"Content-Type": contentType})
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码: got %d want 413（body=%s）", recorder.Code, recorder.Body.String())
	}
}

// buildMultipart 构造一个 multipart/form-data 请求体，返回 (body, content-type)。
func buildMultipart(t *testing.T, fields map[string]string) (string, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	// 固定顺序，避免 map 迭代顺序让用例不可复现。
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	for _, key := range keys {
		if err := writer.WriteField(key, fields[key]); err != nil {
			t.Fatalf("写表单字段失败: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	return buffer.String(), writer.FormDataContentType()
}

// --- 决策 1：上游调用上限 -----------------------------------------------------

// TestUpstreamCallCapStopsAmplification 覆盖有意增补的上游调用上限：达到上限后
// 不再发起新调用，直接 502。
func TestUpstreamCallCapStopsAmplification(t *testing.T) {
	cfg := simpleChatConfig()
	cfg.MaxRetries = 5 // attempts = max_retries+1 = 6（单 key）
	env := newTestEnv(t, cfg, Options{
		MaxUpstreamCallsPerRequest: 2,
		BodyPolicy:                 BodyPolicyPython,
		Multipart:                  MultipartPython,
	})
	// 一直返回 500：没有上限时会有 6 次上游调用。
	env.route("/v1/chat/completions", jsonStep(500, `{"error":{"message":"boom"}}`))

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[]}`, nil)

	if len(env.transport.calls) != 2 {
		t.Fatalf("上游调用数应被上限截断为 2，实得 %d", len(env.transport.calls))
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码: got %d want 502（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "上游请求失败") {
		t.Fatalf("响应体应说明上游失败，实得 %s", recorder.Body.String())
	}
}

// TestDefaultUpstreamCallCapIsApplied 验证**未显式设置时默认上限确实生效**。
//
// 上面那条测试用 Options 显式指定上限为 2，验的是「机制」；若把默认常量改成极大值它
// 照样通过（实测：把 DefaultMaxUpstreamCallsPerRequest 改成 100000 不会被任何现有
// 测试捕获）。默认值是决策 1 的一部分——它决定「最坏情况下一次下游请求打几次上游」，
// 因此单独钉住：配置大量重试且**不设** Options 上限时，调用数应停在默认值。
func TestDefaultUpstreamCallCapIsApplied(t *testing.T) {
	cfg := simpleChatConfig()
	// 单 key 时 attempts = max_retries + 1，特意让它远超默认上限。
	// 用一个**与常量无关**的绝对重试次数，且明显大于任何合理的默认上限。
	//
	// 不能写成 `cfg.MaxRetries = DefaultMaxUpstreamCallsPerRequest + 50`：那样上限与
	// 尝试次数会随常量一起缩放，把常量从 32 改成 100000 时实际调用数也变成 100000，
	// 断言自己和自己相等从而"通过"——这条测试最初就是这么写的，实测确实抓不到改动。
	cfg.MaxRetries = 200
	// 必须用不覆盖 Options 的入口：newTestEnv 会把上限抬到 10000，那样本测试永远测不到
	// 「未设置时采用默认上限」这条路径（这正是它最初漏掉该路径的原因）。
	env := newTestEnvPreservingOptions(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", jsonStep(500, `{"error":{"message":"boom"}}`))

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[]}`, nil)

	if len(env.transport.calls) != DefaultMaxUpstreamCallsPerRequest {
		t.Fatalf("未设上限时上游调用数应停在默认值 %d，实得 %d",
			DefaultMaxUpstreamCallsPerRequest, len(env.transport.calls))
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码: got %d want 502（body=%s）", recorder.Code, recorder.Body.String())
	}
}

// --- 重试与冷却 ---------------------------------------------------------------

// TestRetryPolicyThreeRegimes 固化 RetryPolicy 的三档规则。
func TestRetryPolicyThreeRegimes(t *testing.T) {
	t.Run("单key_用max_retries加一", func(t *testing.T) {
		cfg := simpleChatConfig()
		cfg.MaxRetries = 2
		env := newTestEnv(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		env.route("/v1/chat/completions",
			jsonStep(500, `{"error":{"message":"a"}}`),
			jsonStep(500, `{"error":{"message":"b"}}`),
			jsonStep(200, `{"ok":true}`))
		recorder := env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码: got %d（%s）", recorder.Code, recorder.Body.String())
		}
		if len(env.transport.calls) != 3 {
			t.Fatalf("上游调用数: got %d want 3", len(env.transport.calls))
		}
	})

	t.Run("多key_用key数量_不加额外重试", func(t *testing.T) {
		cfg := &config.RouterConfig{
			MaxRetries: 5,
			Models: []config.ModelConfig{testModel("vendor-model", []config.KeyConfig{
				testKey("k1", "https://upstream.test"),
				testKey("k2", "https://upstream.test"),
				testKey("k3", "https://upstream.test"),
			})},
		}
		env := newTestEnv(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		env.route("/v1/chat/completions",
			jsonStep(500, `{"error":{"message":"boom"}}`))
		recorder := env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("状态码: got %d want 500", recorder.Code)
		}
		if len(env.transport.calls) != 3 {
			t.Fatalf("上游调用数应为 key 数量 3，实得 %d", len(env.transport.calls))
		}
	})

	t.Run("指定key_用max_retries加一且只用该key", func(t *testing.T) {
		cfg := &config.RouterConfig{
			MaxRetries: 1,
			Models: []config.ModelConfig{testModel("vendor-model", []config.KeyConfig{
				testKey("k1", "https://upstream.test"),
				testKey("k2", "https://upstream.test"),
			})},
		}
		env := newTestEnv(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		env.route("/v1/chat/completions", jsonStep(500, `{"error":{"message":"boom"}}`))
		env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model[k2]"}`, nil)
		if len(env.transport.calls) != 2 {
			t.Fatalf("上游调用数: got %d want 2", len(env.transport.calls))
		}
		for index, call := range env.transport.calls {
			if call.headers["authorization"] != "Bearer sk-k2" {
				t.Fatalf("上游 #%d 应使用指定的 k2，实得 %q", index, call.headers["authorization"])
			}
		}
	})
}

// TestRetryableStatusCodesInclude401And403 固化「401/403 可重试（会换 key）」这条
// 与直觉相反的契约。
func TestRetryableStatusCodesInclude401And403(t *testing.T) {
	for _, statusCode := range []int{401, 403, 429, 500, 502, 503, 504, 521} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			if !IsRetryableStatus(statusCode) {
				t.Fatalf("%d 应在可重试集合里", statusCode)
			}
		})
	}
	for _, statusCode := range []int{400, 404, 409, 422, 200, 301} {
		if IsRetryableStatus(statusCode) {
			t.Fatalf("%d 不应在可重试集合里", statusCode)
		}
	}
}

// TestCooldownRules 固化冷却的三条规则：429 立即冷却；其他状态码需累计到阈值；
// 成功会**删除**状态；冷却过滤是软的。
func TestCooldownRules(t *testing.T) {
	t.Run("429立即冷却", func(t *testing.T) {
		env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		env.route("/v1/chat/completions",
			upstreamStep{
				Status: 429,
				Headers: map[string]string{
					"content-type": "application/json",
					"retry-after":  "5",
				},
				Body: `{"error":{"message":"slow"}}`,
			})
		env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)
		if !env.pool.IsCoolingDown("vendor-model", "k1") {
			t.Fatal("429 之后该 key 应立即处于冷却")
		}
	})

	t.Run("达到阈值才冷却", func(t *testing.T) {
		cfg := simpleChatConfig()
		cfg.KeyFailureThreshold = 2
		env := newTestEnv(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		env.route("/v1/chat/completions", jsonStep(500, `{"error":{"message":"boom"}}`))
		// 单 key ⇒ attempts = max_retries+1 = 2，正好把失败计数推到阈值。
		env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)
		if !env.pool.IsCoolingDown("vendor-model", "k1") {
			t.Fatal("累计 2 次失败后应进入冷却")
		}
	})

	t.Run("成功删除冷却状态", func(t *testing.T) {
		env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		env.route("/v1/chat/completions",
			upstreamStep{
				Status:  429,
				Headers: map[string]string{"content-type": "application/json", "retry-after": "30"},
				Body:    `{"error":{"message":"slow"}}`,
			},
			jsonStep(200, `{"ok":true}`))
		// 第一次：429 冷却；第二次仍会被选中（冷却过滤是软的），成功后状态被删除。
		env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)
		env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)
		if env.pool.IsCoolingDown("vendor-model", "k1") {
			t.Fatal("成功后应立即清除冷却状态")
		}
	})
}

// --- 指标 ---------------------------------------------------------------------

// TestMetricsRowsForRetryAndFailure 断言重试路径写的指标行数与关键字段。
func TestMetricsRowsForRetryAndFailure(t *testing.T) {
	cfg := &config.RouterConfig{
		MaxRetries: 1,
		Models: []config.ModelConfig{testModel("vendor-model", []config.KeyConfig{
			testKey("k1", "https://upstream.test"),
			testKey("k2", "https://upstream.test"),
		})},
	}
	env := newTestEnv(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions",
		jsonStep(500, `{"error":{"message":"boom"}}`),
		jsonStep(200, `{"id":"x","usage":{"prompt_tokens":3,"completion_tokens":4}}`))
	env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)

	records := env.metrics.take()
	if len(records) != 2 {
		t.Fatalf("指标行数: got %d want 2", len(records))
	}
	// 第一行：500，retried=true。
	if records[0].StatusCode == nil || *records[0].StatusCode != 500 {
		t.Fatalf("第一行状态码: %v", records[0].StatusCode)
	}
	if !records[0].Retried {
		t.Fatal("重试前的那次失败应记 retried=true")
	}
	// 第二行：200，带 usage，且 first_token_ms == duration_ms（非流式的契约）。
	if records[1].StatusCode == nil || *records[1].StatusCode != 200 {
		t.Fatalf("第二行状态码: %v", records[1].StatusCode)
	}
	if records[1].Usage == nil || records[1].Usage.Lookup("prompt_tokens").PyStr() != "3" {
		t.Fatalf("第二行应带上游 usage，实得 %v", records[1].Usage)
	}
	if records[1].FirstTokenMS != records[1].DurationMS {
		t.Fatalf("非流式路径 first_token_ms 应等于 duration_ms: %d vs %d",
			records[1].FirstTokenMS, records[1].DurationMS)
	}
}

// TestMetricsRowCarriesRequestSource 断言指标行带上入站请求的来源。
//
// 来源（客户端地址与 User-Agent）是看板请求流的输入，却不在 request_metrics 的列里
// （落到 request_source 旁挂表）。派生字段只有一个填充点（recordMetric），流式路径
// 的收尾也走它，因此这条用例守着非流式路径；流式那条见 stream_test.go。
func TestMetricsRowCarriesRequestSource(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", jsonStep(200, `{"id":"x"}`))
	env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`,
		map[string]string{"User-Agent": "claude-cli/1.0"})

	records := env.metrics.take()
	if len(records) != 1 {
		t.Fatalf("指标行数: got %d want 1", len(records))
	}
	// httptest.NewRequest 把 RemoteAddr 固定成 192.0.2.1:1234。
	if records[0].ClientAddr != "192.0.2.1:1234" {
		t.Fatalf("ClientAddr = %q，期望 192.0.2.1:1234", records[0].ClientAddr)
	}
	if records[0].UserAgent != "claude-cli/1.0" {
		t.Fatalf("UserAgent = %q，期望 claude-cli/1.0", records[0].UserAgent)
	}
}

// TestMetricsRowCarriesRequestShape 断言指标行带上请求形态（流式与否、API 格式、推理强度）。
//
// 形态同样不在 request_metrics 的列里（落到 request_shape 旁挂表），且与来源一样只有
// 一个填充点（recordMetric）。这里覆盖三条容易各错一处的事：
//   - 非流式请求的 stream 必须是 false 而不是"缺省"——两者在库里是 0 与 NULL 的区别；
//   - api_format 是**入站路径**（不是上游路径，也不是归一后的方言名）；
//   - reasoning_effort 取模型级配置（它覆盖载荷里已有的值），且非流式请求同样带着它。
func TestMetricsRowCarriesRequestShape(t *testing.T) {
	cfg := simpleChatConfig()
	cfg.ReasoningEffortByModel = map[string]string{"vendor-model": "high"}
	env := newTestEnv(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", jsonStep(200, `{"id":"x"}`))
	// 载荷里显式写一个更低的强度：模型级配置必须覆盖它。
	env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","reasoning_effort":"low"}`, nil)

	records := env.metrics.take()
	if len(records) != 1 {
		t.Fatalf("指标行数: got %d want 1", len(records))
	}
	if records[0].Stream {
		t.Fatal("非流式请求的 Stream 应为 false")
	}
	if records[0].APIFormat != "chat/completions" {
		t.Fatalf("APIFormat = %q，期望 chat/completions", records[0].APIFormat)
	}
	if records[0].ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q，期望 high（模型级配置覆盖载荷）", records[0].ReasoningEffort)
	}
}

// TestMetricsRowRequestShapeForStreaming 断言流式请求（SSE）的形态是 stream=true，
// 且 API 格式来自入站路径而非上游路径。
func TestMetricsRowRequestShapeForStreaming(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", sseStep(
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
		"data: [DONE]\n\n",
	))
	env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[],"stream":true}`, nil)

	records := env.metrics.take()
	if len(records) != 1 {
		t.Fatalf("指标行数: got %d want 1", len(records))
	}
	if !records[0].Stream {
		t.Fatal("流式请求的 Stream 应为 true")
	}
	if records[0].APIFormat != "chat/completions" {
		t.Fatalf("APIFormat = %q，期望 chat/completions", records[0].APIFormat)
	}
	// 模型与载荷都没给强度：空串表示"没有生效的强度"，落库为 NULL。
	if records[0].ReasoningEffort != "" {
		t.Fatalf("ReasoningEffort = %q，期望空串", records[0].ReasoningEffort)
	}
}

// TestEffectiveReasoningEffort 逐条锁定三级优先级与两处刻意的留白。
//
// 这里测的是形态字段的**取值来源**，因此直接打这个函数；上游体那条路径的同一份优先级
// 由 internal/proxysupport 的 TestApplyReasoningEffortPrecedence 守着，两处共用同一个
// ApplyReasoningEffort，不会漂移。
func TestEffectiveReasoningEffort(t *testing.T) {
	cfg := &config.RouterConfig{ReasoningEffortByModel: map[string]string{"m1": "medium"}}
	cases := []struct {
		name    string
		payload string
		modelID string
		want    string
	}{
		{"模型级覆盖载荷", `{"reasoning_effort":"low"}`, "m1", "medium"},
		{"无模型级配置时取载荷", `{"reasoning_effort":"low"}`, "other", "low"},
		{"从 reasoning.effort 提升", `{"reasoning":{"effort":"high"}}`, "other", "high"},
		{"两处都没有", `{"model":"m"}`, "other", ""},
		// Anthropic 的 thinking 不参与：折算成 reasoning_effort 会造出一个上游并不
		// 认识的取值，宁可留空。
		{"Anthropic thinking 不参与", `{"thinking":{"type":"enabled","budget_tokens":16000}}`, "other", ""},
		// 非字符串强度不解释：照 str() 展示成 "true" 只会让看板多一个读不懂的词。
		{"非字符串强度不解释", `{"reasoning_effort":true}`, "other", ""},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			payload, err := canonical.ParseString(item.payload)
			if err != nil {
				t.Fatalf("解析载荷: %v", err)
			}
			if got := effectiveReasoningEffort(payload, item.modelID, cfg); got != item.want {
				t.Fatalf("effectiveReasoningEffort = %q，期望 %q", got, item.want)
			}
		})
	}
	// multipart 表单的载荷是 `{}`，不是对象时同样不解释（这里直接给非对象值）。
	if got := effectiveReasoningEffort(canonical.NewString("x"), "m1", cfg); got != "" {
		t.Fatalf("非对象载荷应返回空串，实得 %q", got)
	}
}

// TestMetricsRowForConnectionFailure 覆盖「上游请求直接失败」：状态码为 nil，
// failed=true，retried=true，且 first_token_ms == duration_ms。
func TestMetricsRowForConnectionFailure(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", upstreamStep{Fail: true})
	env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)

	records := env.metrics.take()
	if len(records) == 0 {
		t.Fatal("应至少写一行指标")
	}
	first := records[0]
	if first.StatusCode != nil {
		t.Fatalf("无响应时状态码应为 nil，实得 %v", *first.StatusCode)
	}
	if !first.Failed || !first.Retried {
		t.Fatalf("连接失败应记 failed=true retried=true，实得 %+v", first)
	}
	if first.FirstTokenMS != first.DurationMS {
		t.Fatalf("first_token_ms 应等于 duration_ms: %d vs %d", first.FirstTokenMS, first.DurationMS)
	}
}

// TestCountTokensWritesNoMetricsRow 固化 count_tokens 的本地计算契约。
func TestCountTokensWritesNoMetricsRow(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	recorder := env.request(http.MethodPost, "messages/count_tokens",
		`{"model":"vendor-model","messages":[{"role":"user","content":"你好"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d", recorder.Code)
	}
	if env.metrics.Count() != 0 {
		t.Fatalf("count_tokens 不应写指标行，实得 %d 行", env.metrics.Count())
	}
	if len(env.transport.calls) != 0 {
		t.Fatalf("count_tokens 不应转发上游，实得 %v", describeUpstreams(env.transport.calls))
	}
	if !strings.Contains(recorder.Body.String(), `"input_tokens"`) {
		t.Fatalf("响应体应含 input_tokens，实得 %s", recorder.Body.String())
	}
}

// --- 流式：租约与释放 ---------------------------------------------------------

// TestLeaseReleasedOnEveryExitPath 覆盖租约释放。流式尝试的 key 释放**只**由
// streamLifecycle.Finish 承担，漏掉一次就会让 RuntimeManager.Close() 永久阻塞，
// 因此这里逐条退出路径断言「租约归零 + 在途计数归零」。
func TestLeaseReleasedOnEveryExitPath(t *testing.T) {
	cases := []struct {
		name   string
		stream bool
		step   upstreamStep
		route  string
		path   string
	}{
		{
			name:  "非流式成功",
			route: "/v1/chat/completions",
			path:  "chat/completions",
			step:  jsonStep(200, `{"ok":true}`),
		},
		{
			name:   "流式成功",
			stream: true,
			route:  "/v1/chat/completions",
			path:   "chat/completions",
			step:   sseStep("data: {}\n\n", "data: [DONE]\n\n"),
		},
		{
			name:  "非流式上游失败",
			route: "/v1/chat/completions",
			path:  "chat/completions",
			step:  jsonStep(500, `{"error":{"message":"boom"}}`),
		},
		{
			name:   "流式上游失败",
			stream: true,
			route:  "/v1/chat/completions",
			path:   "chat/completions",
			step:   jsonStep(503, `{"error":{"message":"boom"}}`),
		},
		{
			name:  "连接失败",
			route: "/v1/chat/completions",
			path:  "chat/completions",
			step:  upstreamStep{Fail: true},
		},
		{
			name:  "鉴权失败",
			route: "/v1/chat/completions",
			path:  "chat/completions",
			step:  jsonStep(200, `{"ok":true}`),
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			env := newTestEnv(t, simpleChatConfig(), Options{
				BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
			env.route(item.route, item.step)
			body := `{"model":"vendor-model","messages":[]`
			if item.stream {
				body = `{"model":"vendor-model","messages":[],"stream":true}`
			} else {
				body += "}"
			}
			env.request(http.MethodPost, item.path, body, nil)

			if got := env.resources.ActiveLeases(); got != 0 {
				t.Fatalf("租约未释放: %d", got)
			}
			if got := env.pool.ActiveCount("vendor-model", "k1"); got != 0 {
				t.Fatalf("key 在途计数未归零: %d", got)
			}
			// Close 会等待所有租约归零；泄漏时这里会永久阻塞（用超时兜住）。
			done := make(chan struct{})
			go func() {
				_ = env.manager.Close()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("RuntimeManager.Close() 被未释放的租约阻塞")
			}
		})
	}
}

// failingWriter 模拟「下游读到一半断开」。
//
// 它在写出 failAfter 字节后开始返回错误，用来验证流式路径的收尾（key 释放）不依赖
// 下游写成功。
type failingWriter struct {
	header    http.Header
	status    int
	written   int
	failAfter int
}

func (w *failingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failingWriter) WriteHeader(status int) { w.status = status }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.written >= w.failAfter {
		return 0, errors.New("客户端已断开")
	}
	w.written += len(p)
	return len(p), nil
}

func (w *failingWriter) Flush() {}

// TestStreamLeaseReleasedWhenDownstreamDisconnects 覆盖「客户端中途断开」。
//
// 这是迁移方案 §Phase 3 明确要求新增的两个测试之一：Go 里下游写失败会短路读取
// 循环，但收尾（写指标、回写健康、释放 key）必须照样执行。
//
// **未能与参照实现比对的一点**：下游断开时参照实现记的 failed 是 true 还是 false，取决于
// Starlette/anyio 抛的是 Exception（被 `except Exception` 捕获 ⇒ true）还是
// BaseException（GeneratorExit / CancelledError，不被捕获 ⇒ false）。本包按 true
// 处理（把「写不出去」视为失败），因为更保守、也更能被监控看到。要钉死这一点需要
// 在真实 uvicorn 上做一次提前断开的端到端实验，属未验证项。
func TestStreamLeaseReleasedWhenDownstreamDisconnects(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", sseStep(
		"data: {\"a\":1}\n\n", "data: {\"b\":2}\n\n", "data: [DONE]\n\n"))

	writer := &failingWriter{failAfter: 0}
	req := httptest.NewRequest(http.MethodPost,
		"http://testserver/v1/chat/completions",
		strings.NewReader(`{"model":"vendor-model","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+env.cfg.LocalAPIKey)
	env.handler.Handle(writer, req, "chat/completions")

	if got := env.resources.ActiveLeases(); got != 0 {
		t.Fatalf("下游断开后租约未释放: %d", got)
	}
	if got := env.pool.ActiveCount("vendor-model", "k1"); got != 0 {
		t.Fatalf("下游断开后 key 在途计数未归零: %d", got)
	}
	// 断开也要留下指标行（failed=true），否则这类故障在监控里完全不可见。
	records := env.metrics.take()
	if len(records) != 1 {
		t.Fatalf("指标行数: got %d want 1", len(records))
	}
	if !records[0].Failed {
		t.Fatalf("下游断开应记 failed=true，实得 %+v", records[0])
	}
}

// TestShutdownWithActiveStreamDoesNotDeadlock 覆盖「关停时仍有活跃流」。
//
// 迁移方案 §Phase 3 要求证明这一点：Close() 会等待每一代租约归零，而流式请求的
// 租约覆盖整条流。这里用一个阻塞的上游读来模拟长流，断言 Close 会等到流结束后
// 才返回，而不是死锁。
func TestShutdownWithActiveStreamDoesNotDeadlock(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", sseStep("data: {\"a\":1}\n\n"))

	released := make(chan struct{})
	writer := &blockingWriter{started: make(chan struct{}), release: released}
	req := httptest.NewRequest(http.MethodPost,
		"http://testserver/v1/chat/completions",
		strings.NewReader(`{"model":"vendor-model","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+env.cfg.LocalAPIKey)

	streamDone := make(chan struct{})
	go func() {
		env.handler.Handle(writer, req, "chat/completions")
		close(streamDone)
	}()

	// 等流真正开始（writer 被写入）后再发起关停。
	<-writer.started
	closed := make(chan struct{})
	go func() {
		_ = env.manager.Close()
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("关停不应在活跃流结束前返回")
	case <-time.After(100 * time.Millisecond):
	}

	close(released)
	select {
	case <-streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("活跃流未能结束")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("关停被未释放的租约永久阻塞")
	}
}

// blockingWriter 在第一次写入时就通知调用方「流已开始」，然后阻塞到 release。
type blockingWriter struct {
	header  http.Header
	status  int
	started chan struct{}
	release chan struct{}
	once    atomic.Bool
}

func (w *blockingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *blockingWriter) WriteHeader(status int) { w.status = status }

func (w *blockingWriter) Write(p []byte) (int, error) {
	if w.once.CompareAndSwap(false, true) {
		if w.started != nil {
			close(w.started)
		}
		<-w.release
	}
	return len(p), nil
}

func (w *blockingWriter) Flush() {}

// --- 流式：两套超时 -----------------------------------------------------------

// slowReader 按 plan 逐个产出数据块，并在块之间等待指定时长。
type slowReader struct {
	chunks []struct {
		delay time.Duration
		data  []byte
	}
	index int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	item := r.chunks[r.index]
	r.index++
	if item.delay > 0 {
		time.Sleep(item.delay)
	}
	count := copy(p, item.data)
	return count, nil
}

func (r *slowReader) Close() error { return nil }

// TestStreamingTimeoutRegimes 固化两套超时语义：
//   - 首块受**绝对**截止时间约束（从发起请求算起）；
//   - 之后每块各自重置 idle 超时，且没有总时长上限。
func TestStreamingTimeoutRegimes(t *testing.T) {
	t.Run("首块超过首字节窗口则终止", func(t *testing.T) {
		env := newTestEnv(t, simpleChatConfig(), Options{
			BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		cfg := env.cfg
		cfg.StreamFirstByteTimeout = 0.05
		cfg.StreamIdleTimeout = 5
		env.resync()

		env.route("/v1/chat/completions", upstreamStep{
			Status:  200,
			Headers: map[string]string{"content-type": "text/event-stream"},
			Reader: &slowReader{chunks: []struct {
				delay time.Duration
				data  []byte
			}{{delay: 400 * time.Millisecond, data: []byte("data: {}\n\n")}}},
		})
		recorder := env.request(http.MethodPost, "chat/completions",
			`{"model":"vendor-model","messages":[],"stream":true}`, nil)
		if recorder.Body.Len() != 0 {
			t.Fatalf("首块超时不应转发任何字节，实得 %q", recorder.Body.String())
		}
		records := env.metrics.take()
		if len(records) != 1 || !records[0].Failed {
			t.Fatalf("首块超时应记一行 failed=true，实得 %+v", records)
		}
	})

	t.Run("块间空闲超过idle则终止且无总时长上限", func(t *testing.T) {
		env := newTestEnv(t, simpleChatConfig(), Options{
			BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		cfg := env.cfg
		cfg.StreamFirstByteTimeout = 5
		cfg.StreamIdleTimeout = 0.08
		env.resync()

		// 三块，每块之间 50ms（小于 idle 80ms），总耗时 150ms——远超 idle 窗口。
		// 若总时长有上限，第三块之后就会被截断；实测不会被截断，因此三块都到达。
		env.route("/v1/chat/completions", upstreamStep{
			Status:  200,
			Headers: map[string]string{"content-type": "text/event-stream"},
			Reader: &slowReader{chunks: []struct {
				delay time.Duration
				data  []byte
			}{
				{data: []byte("data: {\"i\":1}\n\n")},
				{delay: 50 * time.Millisecond, data: []byte("data: {\"i\":2}\n\n")},
				{delay: 50 * time.Millisecond, data: []byte("data: {\"i\":3}\n\n")},
			}},
		})
		recorder := env.request(http.MethodPost, "chat/completions",
			`{"model":"vendor-model","messages":[],"stream":true}`, nil)
		body := recorder.Body.String()
		for _, marker := range []string{`"i":1`, `"i":2`, `"i":3`} {
			if !strings.Contains(body, marker) {
				t.Fatalf("块 %s 应已转发（无总时长上限），实得 %q", marker, body)
			}
		}
	})
}

// resync 在修改配置后重建 pool 与 handler。
//
// 测试里直接改 cfg 字段不会影响已构造的 keypool / handler（它们各自持有一份配置
// 引用），因此显式重建，避免「改了但没生效」的假通过。
func (e *testEnv) resync() {
	manager := runtime.NewRuntimeManager(e.resources)
	e.manager = manager
	e.handler = New(manager, e.metrics, Options{
		BodyPolicy:                 BodyPolicyPython,
		Multipart:                  MultipartPython,
		MaxUpstreamCallsPerRequest: 10_000,
		Logger:                     discardLogger(),
	})
}

// --- query 重编码 -------------------------------------------------------------

// TestReencodeQueryMatchesPython 固化 query 的重编码规则。
//
// 期望值全部由真实 Python 实测得到（httpx.AsyncClient + MockTransport），见
// query.go 顶部注释——这一条与迁移方案 §4.5 的「原样转发」表述不符。
func TestReencodeQueryMatchesPython(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"x=1&y=%20z":  "x=1&y=+z",
		"a=%2Fb":      "a=%2Fb",
		"k":           "k=",
		"k=":          "k=",
		"a=1&a=2":     "a=2",
		"a=1&b=2&a=3": "a=3&b=2",
		"b=2&a=1":     "b=2&a=1",
		"q=%E4%B8%AD": "q=%E4%B8%AD",
		"a+b=c+d":     "a+b=c+d",
		"x=1&y=+z":    "x=1&y=+z",
		"a=%7E":       "a=~",
		"a=~":         "a=~",
		"a=%2A":       "a=%2A",
		"a=%26b":      "a=%26b",
		"a=%3D":       "a=%3D",
		"a=%25":       "a=%25",
		"中=文":         "%E4%B8%AD=%E6%96%87",
		"a=1&":        "a=1",
		"&a=1":        "a=1",
		"a[]=1":       "a%5B%5D=1",
		"a=1&a=":      "a=",
		"=x":          "=x",
		"a=%zz":       "a=%25zz",
	}
	for raw, want := range cases {
		if got := reencodeQuery(raw); got != want {
			t.Errorf("reencodeQuery(%q) = %q，want %q", raw, got, want)
		}
	}
}

// --- SSE 事件切分 -------------------------------------------------------------

// TestSSEEventSplitter 固化事件切分：只产出完整事件；残余留在缓冲里；分隔符取
// 最早出现的那个（`\n\n` 或 `\r\n\r\n`）。
func TestSSEEventSplitter(t *testing.T) {
	splitter := &sseEventSplitter{}
	if events := splitter.push([]byte("data: {\"a\"")); len(events) != 0 {
		t.Fatalf("未完成事件不应外发，实得 %d 个", len(events))
	}
	events := splitter.push([]byte(":1}\n\ndata: {\"b\":2}\n\n"))
	if len(events) != 2 {
		t.Fatalf("应产出 2 个事件，实得 %d", len(events))
	}
	if string(events[0]) != "data: {\"a\":1}\n\n" || string(events[1]) != "data: {\"b\":2}\n\n" {
		t.Fatalf("事件字节不符: %q / %q", events[0], events[1])
	}
	if len(splitter.pending()) != 0 {
		t.Fatalf("缓冲应已清空，实得 %q", splitter.pending())
	}

	// CRLF 分隔符，且与 LF 混用时取更早的那个。
	crlf := &sseEventSplitter{}
	got := crlf.push([]byte("a\r\n\r\nb\n\n"))
	if len(got) != 2 {
		t.Fatalf("CRLF 应切出 2 个事件，实得 %d", len(got))
	}
	if string(got[0]) != "a\r\n\r\n" || string(got[1]) != "b\n\n" {
		t.Fatalf("CRLF 事件字节不符: %q / %q", got[0], got[1])
	}

	// 混合：`\n\n` 出现在 `\r\n\r\n` 之前时必须先切前者。
	mixed := &sseEventSplitter{}
	got = mixed.push([]byte("a\n\nb\r\n\r\n"))
	if len(got) != 2 || string(got[0]) != "a\n\n" {
		t.Fatalf("应取最早出现的分隔符: %q", got)
	}
}

// --- 有意差异的显式记录 -------------------------------------------------------

// TestContentEncodingIsNotDecoded 记录一处**已知差异**：参照实现用的 httpx 会
// 透明解压带 content-encoding 的响应体，而 Go 侧刻意关闭了透明解压
// （internal/upstream 的 DisableCompression，为了不改变上游看到的请求头）。
//
// 后果：上游返回压缩体时，Python 版把解压后的内容交给下游，Go 版原样转发压缩
// 字节；但 content-encoding 头会被 ResponseHeaders 剔除，因此下游无法自行解压。
// 这是需要产品决策的一点，本测试只把它钉住，不假装一致。
func TestContentEncodingIsNotDecoded(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", upstreamStep{
		Status:  200,
		Headers: map[string]string{"content-type": "application/json", "content-encoding": "gzip"},
		Body:    "not-actually-gzip",
	})
	recorder := env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model"}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != "not-actually-gzip" {
		t.Fatalf("Go 侧不做透明解压，响应体应为原始字节，实得 %q", got)
	}
	if recorder.Header().Get("content-encoding") != "" {
		t.Fatalf("content-encoding 应被剔除，实得 %q", recorder.Header().Get("content-encoding"))
	}
}

// TestTaskFixedReasoningEffortRejectsCaller 记录一处**有意差异**（产品决策）。
//
// 参照实现把 reasoning_effort 排除在任务冲突检查之外：调用方传了就传了，服务端收下
// 再静默覆盖成任务的值。Go 侧不再例外——任务路由是给别的 AI 服务用的，不是给 Agent
// 用的，调用方显式传了任务已固定的 reasoning_effort 就该和 temperature 一样收到 400，
// 而不是拿到一个「我传的值没生效」的静默结果。
//
// 同一测试顺带锁定 max_tokens 进入任务白名单后的冲突行为：包括 Responses 方言里
// 叫 max_output_tokens 的同义字段——冲突检查跑在归一化之前，所以这个别名必须在这里
// 就被认出来，否则它会绕过后被静默丢掉。
func TestTaskFixedReasoningEffortRejectsCaller(t *testing.T) {
	cases := []struct {
		name   string
		params string
		body   string
		want   string
	}{
		{
			name:   "调用方传 reasoning_effort 被拒",
			params: `{"reasoning_effort":"high"}`,
			body:   `{"model":"TASK_X","messages":[],"reasoning_effort":"low"}`,
			want:   "任务 TASK_X 已固定参数 reasoning_effort，调用方不能再传这些参数",
		},
		{
			name:   "任务固定 max_tokens 后调用方传 max_tokens 被拒",
			params: `{"max_tokens":32}`,
			body:   `{"model":"TASK_X","messages":[],"max_tokens":5}`,
			want:   "任务 TASK_X 已固定参数 max_tokens，调用方不能再传这些参数",
		},
		{
			// Responses 方言的输出上限叫 max_output_tokens，归一化后会变成 max_tokens。
			// 报告的是调用方实际写的那个键名（与 stop/stop_sequences 一致）。
			name:   "Responses 方言的 max_output_tokens 同样被拒",
			params: `{"max_tokens":32}`,
			body:   `{"model":"TASK_X","input":"hi","max_output_tokens":5}`,
			want:   "任务 TASK_X 已固定参数 max_output_tokens，调用方不能再传这些参数",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			cfg := &config.RouterConfig{
				Models: []config.ModelConfig{
					testModel("task-model", []config.KeyConfig{testKey("tk1", "https://upstream.test")}),
				},
				Tasks: []config.TaskConfig{{
					Name: "TASK_X", Model: "task-model",
					Params: taskParamsValue(t, item.params),
				}},
			}
			env := newTestEnv(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
			recorder := env.request(http.MethodPost, "chat/completions", item.body, nil)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("状态码: got %d want 400（body=%s）", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Body.String(); got != `{"error":{"message":"`+item.want+`"}}` {
				t.Fatalf("响应体不符:\n got=%s\nwant=%s", got, item.want)
			}
			// 冲突在触达上游之前就判掉了。
			if len(env.transport.calls) != 0 {
				t.Fatalf("冲突请求不该触达上游，实得 %v", describeUpstreams(env.transport.calls))
			}
		})
	}
}

// taskParamsValue 把任务的 params 字面量解析成 canonical 值。
func taskParamsValue(t *testing.T, text string) *canonical.Value {
	t.Helper()
	value, err := canonical.ParseString(text)
	if err != nil {
		t.Fatalf("解析任务参数 %s 失败: %v", text, err)
	}
	return value
}

// TestMissingUpstreamClientFailsClosed 记录一处**有意增补**：装配错误时失败关闭。
//
// 参照实现不会遇到这种情况（它直接持有 httpx 客户端）；Go 侧的上游客户端是接口，
// 装配漏掉时必须在请求路径上明确报错，而不是 panic。
func TestMissingUpstreamClientFailsClosed(t *testing.T) {
	cfg := simpleChatConfig()
	pool := keypool.New(cfg, nil, nil)
	resources := runtime.NewRuntimeResources(cfg, pool, nil, nil)
	// 刻意不设置 HTTPClient。
	manager := runtime.NewRuntimeManager(resources)
	handler := New(manager, &recordingMetrics{}, Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython, Logger: discardLogger()})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://testserver/v1/chat/completions",
		strings.NewReader(`{"model":"vendor-model"}`))
	req.Header.Set("Authorization", "Bearer local-key")
	handler.Handle(recorder, req, "chat/completions")

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码: got %d want 502（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := resources.ActiveLeases(); got != 0 {
		t.Fatalf("装配错误路径也必须释放租约，实得 %d", got)
	}
}

// 编译期哨兵：确保测试文件引用的 canonical 断言工具不被误删。
var _ = canonical.DumpsOrdered
var _ = context.Background
