package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

// 本文件固化 /ui/access-key-metrics.json（按访问密钥筛选的整页读数）与
// /ui/key-usage.json 的新过滤参数。
//
// 夹具在 key-usage 的基础上补了一个工作空间：这条路由要拒掉**四种**非完整权限，
// 其中访问密钥与工作空间推理凭据必须真的配在配置里，否则测的是「凭据不认识」而不是
// 「凭据合法但权限不够」。

const accessKeyMetricsFixtureConfig = `{
  "config_version": 4,
  "endpoint_capabilities_path": "<CAP>",
  "metrics_db_path": "<DB>",
  "log_file_path": "<LOG>",
  "local_api_key": "local-key",
  "ops_enabled": true,
  "webui_enabled": true,
  "providers": {
    "prov-a": {
      "base_url": "https://a.example.test",
      "keys": {"key-a": {"api_key": "sk-secret-a", "enabled": true}},
      "routes": {}
    }
  },
  "models": {
    "model-a": {
      "targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}],
      "aliases": ["alias-a"]
    }
  },
  "access_keys": {
    "ak1": {"key": "amkr-ak-1", "name": "试用账号 A", "enabled": true}
  },
  "workspaces": {
    "teamA": {"api_key": "panel-a", "inference_key": "ws-infer-a"}
  }
}`

// newAccessKeyMetricsApp 装配一个 /ui 真的挂载、且带访问密钥与工作空间凭据的 App。
func newAccessKeyMetricsApp(t *testing.T) *App {
	t.Helper()
	return newTestAppWith(t, t.TempDir(), appFixture{
		opsEnabled:   true,
		webUIEnabled: true,
		configText:   accessKeyMetricsFixtureConfig,
		mutate: func(options *Options) {
			options.WebUIAssets = fs.FS(fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")},
			})
		},
	})
}

// accessKeyMetricsBody 是响应的可断言子集。
type accessKeyMetricsBody struct {
	CountSemantics string `json:"count_semantics"`
	AccessKeyID    string `json:"access_key_id"`
	Window         struct {
		From  *string  `json:"from"`
		Hours *float64 `json:"hours"`
	} `json:"window"`
	Snapshot struct {
		Total struct {
			Requests    int64 `json:"requests"`
			TotalTokens int64 `json:"total_tokens"`
		} `json:"total"`
		CallerTypes map[string]struct {
			Requests int64 `json:"requests"`
		} `json:"caller_types"`
		CurrentRPM int64 `json:"current_rpm"`
	} `json:"snapshot"`
	Series struct {
		BucketSeconds int64 `json:"bucket_seconds"`
		Points        []struct {
			Requests int64 `json:"requests"`
		} `json:"points"`
	} `json:"series"`
	Requests *struct {
		Items []struct {
			ID      int64  `json:"id"`
			ModelID string `json:"model_id"`
		} `json:"items"`
		NextBeforeID *int64 `json:"next_before_id"`
		TotalItems   int64  `json:"total_items"`
	} `json:"requests"`
}

// decodeAccessKeyMetrics 发一条请求并解析响应。
func decodeAccessKeyMetrics(t *testing.T, app *App, path string) (int, accessKeyMetricsBody) {
	t.Helper()
	recorder := serve(app, http.MethodGet, path, fullAuthorization)
	var body accessKeyMetricsBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, recorder.Body.String())
	}
	return recorder.Code, body
}

// TestAccessKeyMetricsEndpoint 断言 200 的形状与「只含这把 key」的取数。
//
// 库里有三行：完整权限 prov-a、访问密钥 ak1 的 prov-a、完整权限 prov-b。筛选 ak1 之后
// 总量必须是 1 而不是 3，且 caller_types.local 是 0——访问密钥归属在旁挂表里，完整权限
// 的行不该被顺手算进来。
func TestAccessKeyMetricsEndpoint(t *testing.T) {
	app := newAccessKeyMetricsApp(t)
	seedKeyUsage(t, app)

	recorder := serve(app, http.MethodGet, "/ui/access-key-metrics.json?access_key_id=ak1", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", got)
	}
	status, body := decodeAccessKeyMetrics(t, app, "/ui/access-key-metrics.json?access_key_id=ak1")
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", status)
	}

	if body.CountSemantics != "upstream_attempt" {
		t.Errorf("count_semantics = %q，期望 upstream_attempt", body.CountSemantics)
	}
	if body.AccessKeyID != "ak1" {
		t.Errorf("access_key_id = %q，期望 ak1", body.AccessKeyID)
	}
	if body.Window.Hours == nil || *body.Window.Hours != 24 {
		t.Errorf("window.hours = %v，期望默认 24", body.Window.Hours)
	}
	if body.Window.From == nil {
		t.Error("window.from 不应为 null（默认 24 小时窗口是封口的）")
	}
	if got := body.Snapshot.Total.Requests; got != 1 {
		t.Errorf("snapshot.total.requests = %d，期望 1（只有 ak1 那一行）", got)
	}
	if got := body.Snapshot.CallerTypes["local"].Requests; got != 0 {
		t.Errorf("snapshot.caller_types.local.requests = %d，期望 0", got)
	}
	if got := body.Snapshot.CallerTypes["access_key"].Requests; got != 1 {
		t.Errorf("snapshot.caller_types.access_key.requests = %d，期望 1", got)
	}
	// rate 窗口同样按 key 收窄：未过滤时这里是 3。
	if got := body.Snapshot.CurrentRPM; got != 1 {
		t.Errorf("snapshot.current_rpm = %d，期望 1（未过滤时是 3）", got)
	}

	if body.Series.BucketSeconds < 15 {
		t.Errorf("series.bucket_seconds = %d，期望不小于 15", body.Series.BucketSeconds)
	}
	points := int64(0)
	for _, point := range body.Series.Points {
		points += point.Requests
	}
	if points != 1 {
		t.Errorf("series 各桶 requests 合计 = %d，期望 1（与 snapshot 同一个窗口）", points)
	}

	// limit 缺省是 0：不要明细，requests 为 null。
	if body.Requests != nil {
		t.Errorf("limit=0 时 requests = %+v，期望 null", body.Requests)
	}

	// 要明细时才取：条目与 /metrics/requests 同形（这里只断言身份字段）。
	status, body = decodeAccessKeyMetrics(t, app, "/ui/access-key-metrics.json?access_key_id=ak1&limit=3")
	if status != http.StatusOK {
		t.Fatalf("带 limit 的状态码 = %d，期望 200", status)
	}
	if body.Requests == nil {
		t.Fatal("limit=3 时 requests 不应为 null")
	}
	if len(body.Requests.Items) != 1 {
		t.Fatalf("items 条数 = %d，期望 1", len(body.Requests.Items))
	}
	if got := body.Requests.Items[0].ModelID; got != "model-a" {
		t.Errorf("明细 model_id = %q，期望 model-a", got)
	}
	if body.Requests.TotalItems != 1 {
		t.Errorf("total_items = %d，期望 1", body.Requests.TotalItems)
	}
	if body.Requests.NextBeforeID != nil {
		t.Errorf("next_before_id = %v，期望 null", *body.Requests.NextBeforeID)
	}
}

// TestAccessKeyMetricsEndpointValidatesParameters 断言参数校验先于鉴权，且边界齐全。
//
// 全部用例都不带凭据：FastAPI 在调用路由函数前解析参数，所以这些应当全是 422 而不是
// 401。唯一的例外是「桶太细」——那要等存储层按窗口算点数，因此必须带凭据（401 那条
// 顺序另有 TestMetricsEndpointValidatesQueryBeforeAuth 覆盖）。
func TestAccessKeyMetricsEndpointValidatesParameters(t *testing.T) {
	app := newAccessKeyMetricsApp(t)

	cases := []struct {
		name string
		path string
	}{
		{"缺 access_key_id", "/ui/access-key-metrics.json"},
		{"access_key_id 为空串", "/ui/access-key-metrics.json?access_key_id="},
		{"hours 为 0", "/ui/access-key-metrics.json?access_key_id=ak1&hours=0"},
		{"hours 超过一年", "/ui/access-key-metrics.json?access_key_id=ak1&hours=8761"},
		{"bucket_seconds 小于下限", "/ui/access-key-metrics.json?access_key_id=ak1&bucket_seconds=14"},
		{"bucket_seconds 超过上限", "/ui/access-key-metrics.json?access_key_id=ak1&bucket_seconds=86401"},
		{"limit 超过上限", "/ui/access-key-metrics.json?access_key_id=ak1&limit=201"},
	}
	for _, testCase := range cases {
		recorder := serve(app, http.MethodGet, testCase.path, "")
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s：状态码 = %d，期望 422（body=%s）", testCase.name, recorder.Code, recorder.Body.String())
		}
	}

	// 缺参数的错误形状与 FastAPI 的 `Query(...)` 同形：type=missing、input=null。
	recorder := serve(app, http.MethodGet, "/ui/access-key-metrics.json", "")
	var envelope struct {
		Detail []struct {
			Type  string   `json:"type"`
			Loc   []string `json:"loc"`
			Msg   string   `json:"msg"`
			Input *string  `json:"input"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析 422 响应失败: %v", err)
	}
	if len(envelope.Detail) != 1 {
		t.Fatalf("422 错误条数 = %d，期望 1", len(envelope.Detail))
	}
	missing := envelope.Detail[0]
	if missing.Type != "missing" || missing.Msg != "Field required" {
		t.Errorf("缺参数错误 = %+v，期望 missing / Field required", missing)
	}
	if len(missing.Loc) != 2 || missing.Loc[0] != "query" || missing.Loc[1] != "access_key_id" {
		t.Errorf("loc = %v，期望 [query access_key_id]", missing.Loc)
	}
	if missing.Input != nil {
		t.Errorf("input = %q，期望 null（缺参数与空串必须分得开）", *missing.Input)
	}

	// 显式给了太细的桶：这是**参数**问题，与 /metrics/series 同一套文本与状态码。
	// hours=24 配 60 秒桶 = 1441 点，超过 500 的上限。
	recorder = serve(app, http.MethodGet,
		"/ui/access-key-metrics.json?access_key_id=ak1&hours=24&bucket_seconds=60", fullAuthorization)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("桶太细的状态码 = %d，期望 422（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "time series cannot exceed 500 points") {
		t.Errorf("桶太细的响应 = %s，期望带上点数上限的说明", recorder.Body.String())
	}
}

// TestAccessKeyMetricsEndpointRequiresFullAuth 断言四种非完整权限凭据都被拒。
//
// 内容是全实例的读数（管理面信息），与 /metrics、key-usage 同级。访问密钥与工作空间
// 凭据各有自己的读数端点（/ui/access-key-usage.json、/ui/workspace-panel.json）。
func TestAccessKeyMetricsEndpointRequiresFullAuth(t *testing.T) {
	app := newAccessKeyMetricsApp(t)

	for _, authorization := range []string{
		"",
		otherAuthorization,
		"Bearer amkr-ak-1",  // 配置里真实存在的访问密钥
		"Bearer ws-infer-a", // 配置里真实存在的工作空间推理凭据
		"Bearer panel-a",    // 配置里真实存在的工作空间面板凭据
	} {
		recorder := serve(app, http.MethodGet, "/ui/access-key-metrics.json?access_key_id=ak1", authorization)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("凭据 %q 的状态码 = %d，期望 401", authorization, recorder.Code)
		}
	}
}

// TestAccessKeyMetricsEndpointRejectsNonGet 断言非 GET 回 405 并带 Allow 头。
func TestAccessKeyMetricsEndpointRejectsNonGet(t *testing.T) {
	app := newAccessKeyMetricsApp(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/access-key-metrics.json?access_key_id=ak1", fullAuthorization)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s 状态码 = %d，期望 405", method, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf("%s 的 Allow = %q，期望 GET", method, allow)
		}
	}
}

// TestKeyUsageEndpointFiltersByAccessKey 断言 key-usage 的 access_key_id 参数生效。
//
// 库里 prov-a 的那把 Key 被完整权限与 ak1 各用过一次（未过滤时合计 2），prov-b 的同名
// Key 被完整权限用过一次。筛 ak1 之后只剩 prov-a 那一行、requests = 1，access_keys
// 也只剩 ak1——过滤按「谁发起的请求」收窄，而不是按上游 Key。
func TestKeyUsageEndpointFiltersByAccessKey(t *testing.T) {
	app := newKeyUsageApp(t)
	seedKeyUsage(t, app)

	type keyUsageBody struct {
		UpstreamKeys []struct {
			ProviderID string `json:"provider_id"`
			KeyName    string `json:"key_name"`
			Stats      struct {
				Requests int64 `json:"requests"`
			} `json:"stats"`
		} `json:"upstream_keys"`
		AccessKeys []struct {
			AccessKeyID string `json:"access_key_id"`
			Stats       struct {
				Requests int64 `json:"requests"`
			} `json:"stats"`
		} `json:"access_keys"`
	}
	read := func(t *testing.T, path string) keyUsageBody {
		t.Helper()
		recorder := serve(app, http.MethodGet, path, fullAuthorization)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
		}
		var body keyUsageBody
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		return body
	}

	unfiltered := read(t, "/ui/key-usage.json")
	if len(unfiltered.UpstreamKeys) != 2 {
		t.Fatalf("未过滤 upstream_keys 行数 = %d，期望 2", len(unfiltered.UpstreamKeys))
	}
	if unfiltered.UpstreamKeys[0].Stats.Requests != 2 {
		t.Errorf("未过滤 prov-a/key-a requests = %d，期望 2", unfiltered.UpstreamKeys[0].Stats.Requests)
	}
	if len(unfiltered.AccessKeys) != 1 {
		t.Fatalf("未过滤 access_keys 行数 = %d，期望 1", len(unfiltered.AccessKeys))
	}

	filtered := read(t, "/ui/key-usage.json?access_key_id=ak1")
	if len(filtered.UpstreamKeys) != 1 {
		t.Fatalf("过滤后 upstream_keys 行数 = %d，期望 1（body=%v）", len(filtered.UpstreamKeys), filtered)
	}
	if got := filtered.UpstreamKeys[0]; got.ProviderID != "prov-a" || got.Stats.Requests != 1 {
		t.Errorf("过滤后 upstream_keys[0] = %+v，期望 prov-a/key-a 且 requests=1", got)
	}
	if len(filtered.AccessKeys) != 1 || filtered.AccessKeys[0].AccessKeyID != "ak1" {
		t.Errorf("过滤后 access_keys = %+v，期望只剩 ak1", filtered.AccessKeys)
	}
}
