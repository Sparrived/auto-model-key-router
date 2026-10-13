package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
)

// 本文件固化 /ui/filtered-metrics.json（按供应商 / 访问密钥筛选的整页读数）与
// /ui/key-usage.json 的多值筛选参数。
//
// 夹具在 key-usage 的基础上补了一个工作空间：这条路由要拒掉**四种**非完整权限，
// 其中访问密钥与工作空间推理凭据必须真的配在配置里，否则测的是「凭据不认识」而不是
// 「凭据合法但权限不够」。

const filteredMetricsFixtureConfig = `{
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

// newFilteredMetricsApp 装配一个 /ui 真的挂载、且带访问密钥与工作空间凭据的 App。
func newFilteredMetricsApp(t *testing.T) *App {
	t.Helper()
	return newTestAppWith(t, t.TempDir(), appFixture{
		opsEnabled:   true,
		webUIEnabled: true,
		configText:   filteredMetricsFixtureConfig,
		mutate: func(options *Options) {
			options.WebUIAssets = fs.FS(fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")},
			})
		},
	})
}

// seedFilteredMetrics 写入一组覆盖「两家供应商 × 两把访问密钥」的样本。
//
// 四行的分工：
//   - ak1 → prov-a：只该被「ak1」或「prov-a」两边的筛选选中；
//   - ak1 → prov-b：与上一行**同一把 key、另一家**；
//   - ak2 → prov-a：另一把 key、同一家——AND 用例靠它与前两行区分；
//   - 完整权限 → prov-b：没有访问密钥归属，任何按 key 的筛选都不该看见它，按供应商则
//     看得见（它证明「按供应商筛」与「按 key 筛」是两件事）。
func seedFilteredMetrics(t *testing.T, app *App) {
	t.Helper()
	adapter := app.currentMetricsAdapter()
	if adapter == nil || adapter.store == nil {
		t.Fatal("指标不可用，无法准备测试数据")
	}
	provA, provB := "prov-a", "prov-b"
	ok := int64(200)
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
	)
	records := []metrics.RecordParams{
		{ModelID: "model-a", KeyName: "key-a", Usage: usage, StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak1", ProviderID: &provA},
		{ModelID: "model-a", KeyName: "key-a", Usage: usage, StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak1", ProviderID: &provB},
		{ModelID: "model-a", KeyName: "key-a", Usage: usage, StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak2", ProviderID: &provA},
		{ModelID: "model-a", KeyName: "key-a", Usage: usage, StatusCode: &ok,
			CallerType: "local", ProviderID: &provB},
	}
	for _, record := range records {
		if err := adapter.store.Record(record); err != nil {
			t.Fatalf("写入指标失败: %v", err)
		}
	}
}

// filteredMetricsBody 是响应的可断言子集。
type filteredMetricsBody struct {
	CountSemantics string `json:"count_semantics"`
	Filters        struct {
		ProviderIDs  []string `json:"provider_ids"`
		AccessKeyIDs []string `json:"access_key_ids"`
	} `json:"filters"`
	Window struct {
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
			ID            int64   `json:"id"`
			ModelID       string  `json:"model_id"`
			AccessKeyID   *string `json:"access_key_id"`
			AccessKeyName *string `json:"access_key_name"`
		} `json:"items"`
		NextBeforeID *int64 `json:"next_before_id"`
		TotalItems   int64  `json:"total_items"`
	} `json:"requests"`
}

// decodeFilteredMetrics 发一条请求并解析响应。
func decodeFilteredMetrics(t *testing.T, app *App, path string) (int, filteredMetricsBody) {
	t.Helper()
	recorder := serve(app, http.MethodGet, path, fullAuthorization)
	var body filteredMetricsBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, recorder.Body.String())
	}
	return recorder.Code, body
}

// seriesRequests 汇总 series 各桶的 requests。
func seriesRequests(body filteredMetricsBody) int64 {
	total := int64(0)
	for _, point := range body.Series.Points {
		total += point.Requests
	}
	return total
}

// TestFilteredMetricsEndpoint 断言 200 的形状与两个维度的筛选取数。
//
// 库里有四行：ak1/prov-a、ak1/prov-b、ak2/prov-a、完整权限/prov-b。筛 ak1 之后总量必须
// 是 2 而不是 4，且 caller_types.local 是 0——访问密钥归属在旁挂表里，完整权限的行不该
// 被顺手算进来。
func TestFilteredMetricsEndpoint(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedFilteredMetrics(t, app)

	recorder := serve(app, http.MethodGet, "/ui/filtered-metrics.json?access_key_id=ak1", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", got)
	}
	status, body := decodeFilteredMetrics(t, app, "/ui/filtered-metrics.json?access_key_id=ak1")
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", status)
	}

	if body.CountSemantics != "upstream_attempt" {
		t.Errorf("count_semantics = %q，期望 upstream_attempt", body.CountSemantics)
	}
	// filters 回显两个数组：给的维度是原值，没给的维度是空数组。
	if len(body.Filters.AccessKeyIDs) != 1 || body.Filters.AccessKeyIDs[0] != "ak1" {
		t.Errorf("filters.access_key_ids = %v，期望 [ak1]", body.Filters.AccessKeyIDs)
	}
	if len(body.Filters.ProviderIDs) != 0 {
		t.Errorf("filters.provider_ids = %v，期望空数组", body.Filters.ProviderIDs)
	}
	if body.Window.Hours == nil || *body.Window.Hours != 24 {
		t.Errorf("window.hours = %v，期望默认 24", body.Window.Hours)
	}
	if body.Window.From == nil {
		t.Error("window.from 不应为 null（默认 24 小时窗口是封口的）")
	}
	if got := body.Snapshot.Total.Requests; got != 2 {
		t.Errorf("snapshot.total.requests = %d，期望 2（ak1 的两行）", got)
	}
	if got := body.Snapshot.CallerTypes["local"].Requests; got != 0 {
		t.Errorf("snapshot.caller_types.local.requests = %d，期望 0", got)
	}
	if got := body.Snapshot.CallerTypes["access_key"].Requests; got != 2 {
		t.Errorf("snapshot.caller_types.access_key.requests = %d，期望 2", got)
	}
	// rate 窗口同样按 key 收窄：未筛选时这里是 4。
	if got := body.Snapshot.CurrentRPM; got != 2 {
		t.Errorf("snapshot.current_rpm = %d，期望 2（未筛选时是 4）", got)
	}

	if body.Series.BucketSeconds < 15 {
		t.Errorf("series.bucket_seconds = %d，期望不小于 15", body.Series.BucketSeconds)
	}
	if got := seriesRequests(body); got != 2 {
		t.Errorf("series 各桶 requests 合计 = %d，期望 2（与 snapshot 同一个窗口）", got)
	}

	// limit 缺省是 0：不要明细，requests 为 null。
	if body.Requests != nil {
		t.Errorf("limit=0 时 requests = %+v，期望 null", body.Requests)
	}

	// 要明细时才取：条目与 /metrics/requests 同形（这里只断言身份字段）。
	status, body = decodeFilteredMetrics(t, app, "/ui/filtered-metrics.json?access_key_id=ak1&limit=3")
	if status != http.StatusOK {
		t.Fatalf("带 limit 的状态码 = %d，期望 200", status)
	}
	if body.Requests == nil {
		t.Fatal("limit=3 时 requests 不应为 null")
	}
	if len(body.Requests.Items) != 2 {
		t.Fatalf("items 条数 = %d，期望 2", len(body.Requests.Items))
	}
	if got := body.Requests.Items[0].ModelID; got != "model-a" {
		t.Errorf("明细 model_id = %q，期望 model-a", got)
	}
	// 明细里的访问密钥：id 来自指标库，显示名由服务端从配置补（两行都是 ak1）。
	// 这条路径的 items 嵌在 requests 之下，与 /metrics/requests 的顶层 items 不是同一处
	// 装配代码——补名字时漏掉一边，请求流在筛选态就会显示成一个光秃秃的 id。
	for _, item := range body.Requests.Items {
		if item.AccessKeyID == nil || *item.AccessKeyID != "ak1" {
			t.Errorf("明细 access_key_id = %v，期望 ak1", item.AccessKeyID)
		}
		if item.AccessKeyName == nil || *item.AccessKeyName != "试用账号 A" {
			t.Errorf("明细 access_key_name = %v，期望配置里的名字", item.AccessKeyName)
		}
	}
	if body.Requests.TotalItems != 2 {
		t.Errorf("total_items = %d，期望 2", body.Requests.TotalItems)
	}
	if body.Requests.NextBeforeID != nil {
		t.Errorf("next_before_id = %v，期望 null", *body.Requests.NextBeforeID)
	}

	// 两个维度都不给 = 全实例读数（与 /metrics 等价）：四行都在，filters 是两个空数组。
	status, body = decodeFilteredMetrics(t, app, "/ui/filtered-metrics.json")
	if status != http.StatusOK {
		t.Fatalf("不带筛选的状态码 = %d，期望 200", status)
	}
	if got := body.Snapshot.Total.Requests; got != 4 {
		t.Errorf("不带筛选的 total.requests = %d，期望 4（全实例）", got)
	}
	if len(body.Filters.ProviderIDs) != 0 || len(body.Filters.AccessKeyIDs) != 0 {
		t.Errorf("不带筛选的 filters = %+v，期望两个空数组", body.Filters)
	}
}

// TestFilteredMetricsEndpointRepeatedParameters 断言同名参数重复出现的语义。
//
// 维度内 OR、维度间 AND：
//   - `provider_id=prov-a&provider_id=prov-b` 两家都进来（4 行）；
//   - `access_key_id=ak1&access_key_id=ak2` 两把 key 都进来（3 行），完整权限的行不进来；
//   - `access_key_id=ak1&provider_id=prov-b` 只该有 ak1 打给 prov-b 的那一行——这一条
//     正是「维度内 OR 被误实现成维度间 OR」时唯一会露馅的用例；
//   - ak2 从未打给 prov-b，因此那个组合必须是 0。
func TestFilteredMetricsEndpointRepeatedParameters(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedFilteredMetrics(t, app)

	cases := []struct {
		name            string
		query           string
		wantRequests    int64
		wantProviderIDs []string
		wantAccessKeys  []string
	}{
		{
			name: "供应商多值", query: "provider_id=prov-a&provider_id=prov-b",
			wantRequests: 4, wantProviderIDs: []string{"prov-a", "prov-b"},
		},
		{
			name: "访问密钥多值", query: "access_key_id=ak1&access_key_id=ak2",
			wantRequests: 3, wantAccessKeys: []string{"ak1", "ak2"},
		},
		{
			name: "维度间 AND", query: "access_key_id=ak1&provider_id=prov-b",
			wantRequests: 1, wantProviderIDs: []string{"prov-b"}, wantAccessKeys: []string{"ak1"},
		},
		{
			name: "AND 把不匹配的组合排空", query: "access_key_id=ak2&provider_id=prov-b",
			wantRequests: 0, wantProviderIDs: []string{"prov-b"}, wantAccessKeys: []string{"ak2"},
		},
	}
	for _, testCase := range cases {
		status, body := decodeFilteredMetrics(t, app, "/ui/filtered-metrics.json?"+testCase.query)
		if status != http.StatusOK {
			t.Fatalf("%s: 状态码 = %d，期望 200", testCase.name, status)
		}
		if got := body.Snapshot.Total.Requests; got != testCase.wantRequests {
			t.Errorf("%s: total.requests = %d，期望 %d", testCase.name, got, testCase.wantRequests)
		}
		if got := seriesRequests(body); got != testCase.wantRequests {
			t.Errorf("%s: series 各桶 requests 合计 = %d，期望 %d（与 snapshot 同一个窗口）",
				testCase.name, got, testCase.wantRequests)
		}
		if strings.Join(body.Filters.ProviderIDs, ",") != strings.Join(testCase.wantProviderIDs, ",") {
			t.Errorf("%s: filters.provider_ids = %v，期望 %v",
				testCase.name, body.Filters.ProviderIDs, testCase.wantProviderIDs)
		}
		if strings.Join(body.Filters.AccessKeyIDs, ",") != strings.Join(testCase.wantAccessKeys, ",") {
			t.Errorf("%s: filters.access_key_ids = %v，期望 %v",
				testCase.name, body.Filters.AccessKeyIDs, testCase.wantAccessKeys)
		}
	}
}

// TestFilteredMetricsEndpointValidatesParameters 断言参数校验先于鉴权，且边界齐全。
//
// 全部 422 用例都不带凭据：FastAPI 在调用路由函数前解析参数，所以这些应当全是 422 而
// 不是 401（401 那条顺序另有覆盖）。多值参数逐项校验，因此「第二项非法」也在这里锁住
// ——input 必须是**那一项**的原文，否则用户拿到一句「有一项超长」还得自己找是哪一项。
func TestFilteredMetricsEndpointValidatesParameters(t *testing.T) {
	app := newFilteredMetricsApp(t)
	longItem := strings.Repeat("x", 513)

	cases := []struct {
		name      string
		path      string
		wantInput string
		wantType  string
	}{
		{"hours 为 0", "/ui/filtered-metrics.json?hours=0", "0", "greater_than"},
		{"hours 超过一年", "/ui/filtered-metrics.json?hours=8761", "8761", "less_than_equal"},
		{"bucket_seconds 小于下限", "/ui/filtered-metrics.json?bucket_seconds=14", "14", "greater_than_equal"},
		{"bucket_seconds 超过上限", "/ui/filtered-metrics.json?bucket_seconds=86401", "86401", "less_than_equal"},
		{"limit 超过上限", "/ui/filtered-metrics.json?limit=201", "201", "less_than_equal"},
		{"provider_id 为空串", "/ui/filtered-metrics.json?provider_id=", "", "string_too_short"},
		{"provider_id 超长", "/ui/filtered-metrics.json?provider_id=" + longItem, longItem, "string_too_long"},
		{"provider_id 第二项为空串",
			"/ui/filtered-metrics.json?provider_id=prov-a&provider_id=", "", "string_too_short"},
		{"access_key_id 第二项超长",
			"/ui/filtered-metrics.json?access_key_id=ak1&access_key_id=" + longItem, longItem, "string_too_long"},
	}
	for _, testCase := range cases {
		recorder := serve(app, http.MethodGet, testCase.path, "")
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s：状态码 = %d，期望 422（body=%s）", testCase.name, recorder.Code, recorder.Body.String())
			continue
		}
		var envelope struct {
			Detail []struct {
				Type  string   `json:"type"`
				Loc   []string `json:"loc"`
				Input *string  `json:"input"`
			} `json:"detail"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Errorf("%s: 解析 422 响应失败: %v", testCase.name, err)
			continue
		}
		if len(envelope.Detail) != 1 {
			t.Errorf("%s: 422 错误条数 = %d，期望 1", testCase.name, len(envelope.Detail))
			continue
		}
		detail := envelope.Detail[0]
		if detail.Type != testCase.wantType {
			t.Errorf("%s: type = %q，期望 %q", testCase.name, detail.Type, testCase.wantType)
		}
		if detail.Input == nil || *detail.Input != testCase.wantInput {
			t.Errorf("%s: input = %v，期望 %q（必须是出错的那一项原文）",
				testCase.name, detail.Input, testCase.wantInput)
		}
		if len(detail.Loc) != 2 || detail.Loc[0] != "query" {
			t.Errorf("%s: loc = %v，期望 [query <参数名>]", testCase.name, detail.Loc)
		}
	}

	// 参数合法但不带凭据：401 而不是 422——「校验先于鉴权」的反面也要成立。
	if recorder := serve(app, http.MethodGet, "/ui/filtered-metrics.json?provider_id=prov-a", ""); recorder.Code != http.StatusUnauthorized {
		t.Errorf("合法参数不带凭据的状态码 = %d，期望 401", recorder.Code)
	}

	// 显式给了太细的桶：这是**参数**问题，与 /metrics/series 同一套文本与状态码。
	// hours=24 配 60 秒桶 = 1441 点，超过 500 的上限。
	recorder := serve(app, http.MethodGet,
		"/ui/filtered-metrics.json?access_key_id=ak1&hours=24&bucket_seconds=60", fullAuthorization)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("桶太细的状态码 = %d，期望 422（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "time series cannot exceed 500 points") {
		t.Errorf("桶太细的响应 = %s，期望带上点数上限的说明", recorder.Body.String())
	}
}

// TestFilteredMetricsEndpointRequiresFullAuth 断言四种非完整权限凭据都被拒。
//
// 内容是全实例的读数（管理面信息），与 /metrics、key-usage 同级。访问密钥与工作空间
// 凭据各有自己的读数端点（/ui/access-key-usage.json、/ui/workspace-panel.json）。
func TestFilteredMetricsEndpointRequiresFullAuth(t *testing.T) {
	app := newFilteredMetricsApp(t)

	for _, authorization := range []string{
		"",
		otherAuthorization,
		"Bearer amkr-ak-1",  // 配置里真实存在的访问密钥
		"Bearer ws-infer-a", // 配置里真实存在的工作空间推理凭据
		"Bearer panel-a",    // 配置里真实存在的工作空间面板凭据
	} {
		recorder := serve(app, http.MethodGet, "/ui/filtered-metrics.json?access_key_id=ak1", authorization)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("凭据 %q 的状态码 = %d，期望 401", authorization, recorder.Code)
		}
	}
}

// TestFilteredMetricsEndpointRejectsNonGet 断言非 GET 回 405 并带 Allow 头。
func TestFilteredMetricsEndpointRejectsNonGet(t *testing.T) {
	app := newFilteredMetricsApp(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/filtered-metrics.json?access_key_id=ak1", fullAuthorization)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s 状态码 = %d，期望 405", method, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf("%s 的 Allow = %q，期望 GET", method, allow)
		}
	}
}

// TestKeyUsageEndpointMultiValueFilters 断言 key-usage 的两个参数都能重复出现且同时生效。
//
// 库里的三行见 seedKeyUsage：完整权限 prov-a、ak1 的 prov-a、完整权限 prov-b。
//   - 供应商多值：两家的行都进来（prov-a 2 + prov-b 1）；
//   - 访问密钥多值：只有这些 key 的行（seed 里只有 ak1 有归属，因此与单值同量）；
//   - 两个维度同时给值：交集（ak1 只打给 prov-a，配上 prov-b 时三份拆分全空）。
func TestKeyUsageEndpointMultiValueFilters(t *testing.T) {
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
		ModelKeys []struct {
			ModelID    string `json:"model_id"`
			ProviderID string `json:"provider_id"`
			KeyName    string `json:"key_name"`
		} `json:"model_keys"`
		AccessKeys []struct {
			AccessKeyID   string `json:"access_key_id"`
			AccessKeyName string `json:"access_key_name"`
			Stats         struct {
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

	// 单值：仍然是「只看这一把」（原来的行为）。
	single := read(t, "/ui/key-usage.json?access_key_id=ak1")
	if len(single.UpstreamKeys) != 1 || single.UpstreamKeys[0].ProviderID != "prov-a" ||
		single.UpstreamKeys[0].Stats.Requests != 1 {
		t.Errorf("单值筛选 upstream_keys = %+v，期望只剩 prov-a/key-a 且 requests=1", single.UpstreamKeys)
	}
	if len(single.AccessKeys) != 1 || single.AccessKeys[0].AccessKeyID != "ak1" {
		t.Errorf("单值筛选 access_keys = %+v，期望只剩 ak1", single.AccessKeys)
	}

	// 供应商多值：两家的行都在，且 requests 各归各的。
	providers := read(t, "/ui/key-usage.json?provider_id=prov-a&provider_id=prov-b")
	byProvider := map[string]int64{}
	for _, row := range providers.UpstreamKeys {
		byProvider[row.ProviderID] = row.Stats.Requests
	}
	if len(providers.UpstreamKeys) != 2 || byProvider["prov-a"] != 2 || byProvider["prov-b"] != 1 {
		t.Errorf("供应商多值筛选 upstream_keys = %+v，期望 prov-a=2 与 prov-b=1", providers.UpstreamKeys)
	}
	if len(providers.AccessKeys) != 1 {
		t.Errorf("供应商多值筛选 access_keys 行数 = %d，期望 1（只有 ak1 有归属）", len(providers.AccessKeys))
	}

	// 访问密钥多值：ak2 在 seed 里没有归属，因此与单值同量；关键是它**不能**把完整权限
	// 的行带进来。
	keys := read(t, "/ui/key-usage.json?access_key_id=ak1&access_key_id=ak2")
	if len(keys.UpstreamKeys) != 1 || keys.UpstreamKeys[0].Stats.Requests != 1 {
		t.Errorf("访问密钥多值筛选 upstream_keys = %+v，期望只剩 prov-a/key-a 的 1 次", keys.UpstreamKeys)
	}

	// 两个维度同时给值：交集。ak1 只打给 prov-a，配上 prov-b 时三份拆分全空。
	intersection := read(t, "/ui/key-usage.json?access_key_id=ak1&provider_id=prov-b")
	if len(intersection.UpstreamKeys) != 0 || len(intersection.ModelKeys) != 0 ||
		len(intersection.AccessKeys) != 0 {
		t.Errorf("两个维度的交集应为空，实得 %+v", intersection)
	}
	// 同一个 key 配上它真正打过的供应商：明细都回来。
	matched := read(t, "/ui/key-usage.json?access_key_id=ak1&provider_id=prov-a")
	if len(matched.UpstreamKeys) != 1 || len(matched.ModelKeys) != 1 || len(matched.AccessKeys) != 1 {
		t.Errorf("两个维度的交集 = %+v，期望各一份", matched)
	}
	if matched.UpstreamKeys[0].Stats.Requests != 1 {
		t.Errorf("交集 upstream_keys[0].requests = %d，期望 1", matched.UpstreamKeys[0].Stats.Requests)
	}
}
