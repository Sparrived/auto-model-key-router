package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
)

// 本文件固化 /ui 读数新增的五个筛选维度参数（model_id / upstream_key / workspace /
// caller_type / success）的 HTTP 契约，以及 GET /ui/metric-dimensions.json（筛选器选项）。
//
// 三条既有读数（filtered-metrics / key-usage / status-monitor）与新的 usage-breakdown 共用
// 同一份 dimensionFilterParams，因此参数校验用例对四条路由逐条跑一遍：漏接一条就是
// 「同一个参数在这页 422、在那页被静默忽略」。/metrics 系列不认这些参数——那组用例
// 证明它们的响应逐字节不变。

// pinMetricsClock 把指标库的时钟钉在一个固定时刻，测试结束时还原。
//
// 比较两次请求的响应字节时必须钉住：window.to 与 rate 窗口都取 now，不钉住两次请求之间
// 会差几微秒。写入也走同一个时钟，因此种数据之前就要钉好。
func pinMetricsClock(t *testing.T) time.Time {
	t.Helper()
	fixed := time.Date(2026, 7, 14, 12, 17, 30, 0, metrics.BeijingTZ())
	restore := metrics.SetNowForTest(func() time.Time { return fixed })
	t.Cleanup(restore)
	return fixed
}

// seedDimensionData 写入一组让五个新维度彼此可区分的样本（5 行）。
//
//	#  模型     供应商  上游 Key  调用方          工作空间  状态
//	1  model-a  prov-a  key-a     local           teamA     200
//	2  model-a  prov-a  key-b     access_key/ak1  teamA     200
//	3  model-b  prov-b  key-a     access_key/ak2  —         500
//	4  model-b  prov-b  key-b     workspace       teamA     200
//	5  model-c  NULL    legacy    local           —         NULL
//
// 上游 Key 的四个组合都出现过：筛 `["prov-a","key-a"]` + `["prov-b","key-b"]` 若被实现成
// 笛卡尔积，会把 2、3 两行也带进来。配置里只配了 ak1（有名字），ak2 没配。
func seedDimensionData(t *testing.T, app *App) {
	t.Helper()
	adapter := app.currentMetricsAdapter()
	if adapter == nil || adapter.store == nil {
		t.Fatal("指标不可用，无法准备测试数据")
	}
	provA, provB := "prov-a", "prov-b"
	ok, failed := int64(200), int64(500)
	usage := func(prompt int64) *canonical.Value {
		return canonical.NewObjectOf(
			canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(prompt)},
		)
	}
	records := []metrics.RecordParams{
		{ModelID: "model-a", KeyName: "key-a", ProviderID: &provA, CallerType: "local",
			Workspace: "teamA", StatusCode: &ok, Usage: usage(10), APIFormat: "chat/completions"},
		{ModelID: "model-a", KeyName: "key-b", ProviderID: &provA, CallerType: "access_key",
			AccessKeyID: "ak1", Workspace: "teamA", StatusCode: &ok, Usage: usage(20)},
		{ModelID: "model-b", KeyName: "key-a", ProviderID: &provB, CallerType: "access_key",
			AccessKeyID: "ak2", StatusCode: &failed, Usage: usage(30)},
		{ModelID: "model-b", KeyName: "key-b", ProviderID: &provB, CallerType: "workspace",
			Workspace: "teamA", StatusCode: &ok, Usage: usage(40), APIFormat: "messages"},
		{ModelID: "model-c", KeyName: "legacy", CallerType: "local", Usage: usage(5)},
	}
	for _, record := range records {
		if err := adapter.store.Record(record); err != nil {
			t.Fatalf("写入指标失败: %v", err)
		}
	}
}

// upstreamKeyQuery 把一对 (供应商, Key 名) 编成 upstream_key 参数（JSON 数组再做 URL 转义）。
func upstreamKeyQuery(providerID, keyName string) string {
	raw, _ := json.Marshal([]string{providerID, keyName})
	return "upstream_key=" + url.QueryEscape(string(raw))
}

// parseBody 把响应体解析成保序的 canonical 值。
func parseBody(t *testing.T, body []byte) *canonical.Value {
	t.Helper()
	value, err := canonical.Parse(body)
	if err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, body)
	}
	return value
}

// getOK 发一条带完整权限的 GET，要求 200 并返回解析后的响应。
func getOK(t *testing.T, app *App, path string) *canonical.Value {
	t.Helper()
	recorder := serve(app, http.MethodGet, path, fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s 状态码 = %d，期望 200（body=%s）", path, recorder.Code, recorder.Body.String())
	}
	return parseBody(t, recorder.Body.Bytes())
}

// intField 取一个整数字段。
func intField(t *testing.T, value *canonical.Value, path ...string) int64 {
	t.Helper()
	current := value
	for _, key := range path {
		current = current.Lookup(key)
	}
	number, ok := current.AsInt()
	if !ok {
		t.Fatalf("字段 %v 不是整数：%s", path, canonical.DumpsOrdered(current))
	}
	return number
}

// dimensionReadingPaths 是接受七个筛选维度的四条 /ui 读数。
var dimensionReadingPaths = []string{
	"/ui/filtered-metrics.json",
	"/ui/key-usage.json",
	"/ui/status-monitor.json",
	"/ui/usage-breakdown.json",
}

// TestDimensionParametersValidated 断言新参数在四条读数上同一套校验：先于鉴权、一条
// detail、loc 是 ["query", <参数名>]、input 是出错那一项的原文。
func TestDimensionParametersValidated(t *testing.T) {
	app := newFilteredMetricsApp(t)
	longItem := strings.Repeat("x", 513)
	cases := []struct {
		name      string
		query     string
		wantParam string
		wantType  string
		wantInput string
	}{
		{"upstream_key 不是 JSON", "upstream_key=" + url.QueryEscape("prov-a/key-a"),
			"upstream_key", "value_error", "prov-a/key-a"},
		{"upstream_key 只有一项", "upstream_key=" + url.QueryEscape(`["prov-a"]`),
			"upstream_key", "value_error", `["prov-a"]`},
		{"upstream_key 三项", "upstream_key=" + url.QueryEscape(`["a","b","c"]`),
			"upstream_key", "value_error", `["a","b","c"]`},
		{"upstream_key 有空串", "upstream_key=" + url.QueryEscape(`["prov-a",""]`),
			"upstream_key", "value_error", `["prov-a",""]`},
		{"upstream_key 元素不是字符串", "upstream_key=" + url.QueryEscape(`["prov-a",1]`),
			"upstream_key", "value_error", `["prov-a",1]`},
		{"upstream_key 是对象", "upstream_key=" + url.QueryEscape(`{"provider_id":"a"}`),
			"upstream_key", "value_error", `{"provider_id":"a"}`},
		{"upstream_key 是 null", "upstream_key=null", "upstream_key", "value_error", "null"},
		{"upstream_key 第二项非法", upstreamKeyQuery("prov-a", "key-a") + "&upstream_key=nope",
			"upstream_key", "value_error", "nope"},
		{"caller_type 不在取值域", "caller_type=visitor", "caller_type", "literal_error", "visitor"},
		{"caller_type 第二项非法", "caller_type=local&caller_type=", "caller_type", "literal_error", ""},
		{"model_id 为空串", "model_id=", "model_id", "string_too_short", ""},
		{"workspace 超长", "workspace=" + longItem, "workspace", "string_too_long", longItem},
		{"success 不是布尔", "success=maybe", "success", "bool_parsing", "maybe"},
	}
	for _, path := range dimensionReadingPaths {
		for _, testCase := range cases {
			recorder := serve(app, http.MethodGet, path+"?"+testCase.query, "")
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s %s：状态码 = %d，期望 422（body=%s）",
					path, testCase.name, recorder.Code, recorder.Body.String())
				continue
			}
			var envelope struct {
				Detail []struct {
					Type  string         `json:"type"`
					Loc   []string       `json:"loc"`
					Msg   string         `json:"msg"`
					Input *string        `json:"input"`
					Ctx   map[string]any `json:"ctx"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Errorf("%s %s：解析 422 失败: %v", path, testCase.name, err)
				continue
			}
			if len(envelope.Detail) != 1 {
				t.Errorf("%s %s：detail 条数 = %d，期望 1（body=%s）",
					path, testCase.name, len(envelope.Detail), recorder.Body.String())
				continue
			}
			detail := envelope.Detail[0]
			if detail.Type != testCase.wantType {
				t.Errorf("%s %s：type = %q，期望 %q", path, testCase.name, detail.Type, testCase.wantType)
			}
			if len(detail.Loc) != 2 || detail.Loc[0] != "query" || detail.Loc[1] != testCase.wantParam {
				t.Errorf("%s %s：loc = %v，期望 [query %s]", path, testCase.name, detail.Loc, testCase.wantParam)
			}
			if detail.Input == nil || *detail.Input != testCase.wantInput {
				t.Errorf("%s %s：input = %v，期望 %q", path, testCase.name, detail.Input, testCase.wantInput)
			}
			switch testCase.wantType {
			case "literal_error":
				// 与 /metrics/requests 的 caller_type 同一句文案。
				if detail.Ctx["expected"] != "'local', 'workspace' or 'access_key'" {
					t.Errorf("%s %s：ctx.expected = %q", path, testCase.name, detail.Ctx["expected"])
				}
			case "value_error":
				if !strings.HasPrefix(detail.Msg, "Value error, upstream_key must be a JSON array") {
					t.Errorf("%s %s：msg = %q", path, testCase.name, detail.Msg)
				}
				if detail.Ctx != nil {
					t.Errorf("%s %s：value_error 不应带 ctx，实得 %v", path, testCase.name, detail.Ctx)
				}
			}
		}
		// 合法的新参数不带凭据：401（校验通过之后才轮到鉴权）。
		valid := path + "?" + upstreamKeyQuery("prov-a", "key-a") +
			"&model_id=model-a&workspace=teamA&caller_type=local&caller_type=access_key&success=true"
		if recorder := serve(app, http.MethodGet, valid, ""); recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s 合法参数不带凭据的状态码 = %d，期望 401", path, recorder.Code)
		}
	}
}

// TestFilteredMetricsEndpointNewDimensions 断言五个新参数的筛选语义与 filters 回显。
func TestFilteredMetricsEndpointNewDimensions(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedDimensionData(t, app)

	cases := []struct {
		name  string
		query string
		want  int64
	}{
		{"不筛", "", 5},
		{"模型多值", "model_id=model-a&model_id=model-b", 4},
		{"上游 Key 成对 OR", upstreamKeyQuery("prov-a", "key-a") + "&" + upstreamKeyQuery("prov-b", "key-b"), 2},
		{"上游 Key 含分隔符的名字照样能筛", upstreamKeyQuery("prov-a", "key/a"), 0},
		{"工作空间", "workspace=teamA", 3},
		{"调用方档位多值", "caller_type=access_key&caller_type=workspace", 3},
		{"只看失败", "success=false", 2},
		{"只看成功（宽松布尔）", "success=yes", 3},
		{"维度间 AND", "model_id=model-a&caller_type=access_key&success=true", 1},
		{"新旧维度 AND", "provider_id=prov-b&workspace=teamA", 1},
	}
	for _, testCase := range cases {
		// 1 小时 / 1 小时桶：样本都落在 now，窄窗口足够，且响应只有两个点（默认 24 小时的
		// 自动桶有约 480 个点，逐条解析会拖慢用例而不增加覆盖）。
		body := getOK(t, app, "/ui/filtered-metrics.json?hours=1&bucket_seconds=3600&limit=200&"+testCase.query)
		if got := intField(t, body, "snapshot", "total", "requests"); got != testCase.want {
			t.Errorf("%s: snapshot.total.requests = %d，期望 %d", testCase.name, got, testCase.want)
		}
		if got := len(body.Lookup("requests").Lookup("items").Arr); int64(got) != testCase.want {
			t.Errorf("%s: requests.items = %d，期望 %d", testCase.name, got, testCase.want)
		}
	}

	body := getOK(t, app, "/ui/filtered-metrics.json?hours=1&bucket_seconds=3600&provider_id=prov-a&"+
		upstreamKeyQuery("prov-a", "key-b")+"&model_id=model-a&workspace=teamA"+
		"&caller_type=access_key&success=1&access_key_id=ak1")
	want := `{"provider_ids":["prov-a"],"access_key_ids":["ak1"],"model_ids":["model-a"],` +
		`"upstream_keys":[{"provider_id":"prov-a","key_name":"key-b"}],"workspaces":["teamA"],` +
		`"caller_types":["access_key"],"success":true}`
	if got := canonical.DumpsOrdered(body.Lookup("filters")); got != want {
		t.Errorf("filters 回显 =\n %s\n期望\n %s", got, want)
	}
	if got := intField(t, body, "snapshot", "total", "requests"); got != 1 {
		t.Errorf("七个维度同时给值 total.requests = %d，期望 1", got)
	}
	empty := getOK(t, app, "/ui/filtered-metrics.json?hours=1&bucket_seconds=3600")
	if got := canonical.DumpsOrdered(empty.Lookup("filters")); got !=
		`{"provider_ids":[],"access_key_ids":[],"model_ids":[],"upstream_keys":[],"workspaces":[],"caller_types":[],"success":null}` {
		t.Errorf("未筛选的 filters = %s", got)
	}
}

// TestKeyUsageAndStatusMonitorNewDimensions 断言另外两条既有读数同样接上了新参数。
func TestKeyUsageAndStatusMonitorNewDimensions(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedDimensionData(t, app)

	keyUsage := getOK(t, app, "/ui/key-usage.json?"+upstreamKeyQuery("prov-a", "key-b"))
	upstream := keyUsage.Lookup("upstream_keys").Arr
	if len(upstream) != 1 || upstream[0].Lookup("key_name").Str != "key-b" ||
		intField(t, upstream[0], "stats", "requests") != 1 {
		t.Errorf("按上游 Key 筛选的 upstream_keys = %s", canonical.DumpsOrdered(keyUsage.Lookup("upstream_keys")))
	}
	byWorkspace := getOK(t, app, "/ui/key-usage.json?workspace=teamA&success=true")
	if got := len(byWorkspace.Lookup("model_keys").Arr); got != 3 {
		t.Errorf("teamA 成功请求的 model_keys 行数 = %d，期望 3", got)
	}

	status := getOK(t, app, "/ui/status-monitor.json?workspace=teamA&success=true")
	if got := intField(t, status, "snapshot", "total", "requests"); got != 3 {
		t.Errorf("status-monitor 筛选后 total.requests = %d，期望 3", got)
	}
	if got := canonical.DumpsOrdered(status.Lookup("failure_status_codes")); got != `{}` {
		t.Errorf("只看成功时 failure_status_codes = %s，期望 {}", got)
	}
	if got := canonical.DumpsOrdered(status.Lookup("filters")); got !=
		`{"provider_ids":[],"access_key_ids":[],"model_ids":[],"upstream_keys":[],"workspaces":["teamA"],"caller_types":[],"success":true}` {
		t.Errorf("status-monitor filters = %s", got)
	}
}

// TestMetricsEndpointsIgnoreDimensionParameters 断言 /metrics、/metrics/series、
// /metrics/requests 对新参数一无所知：带上它们（哪怕取值非法）响应也与不带时逐字节相同，
// 已发布的单值参数仍是「取末位」。
func TestMetricsEndpointsIgnoreDimensionParameters(t *testing.T) {
	pinMetricsClock(t)
	app := newFilteredMetricsApp(t)
	seedDimensionData(t, app)

	noise := "upstream_key=not-json&workspace=teamA&access_key_id=ak1&model_ids=x&caller_types=bad"
	pairs := [][2]string{
		{"/metrics", "/metrics?" + noise},
		{"/metrics?hours=48", "/metrics?hours=48&" + noise + "&provider_id=prov-a"},
		{"/metrics/series?hours=1&bucket_seconds=60", "/metrics/series?hours=1&bucket_seconds=60&" + noise},
		{"/metrics/requests?model_id=model-a", "/metrics/requests?model_id=model-a&" + noise},
	}
	for _, pair := range pairs {
		plain := serve(app, http.MethodGet, pair[0], fullAuthorization)
		noisy := serve(app, http.MethodGet, pair[1], fullAuthorization)
		if plain.Code != http.StatusOK || noisy.Code != http.StatusOK {
			t.Errorf("%s：状态码 %d / %d，期望都是 200（body=%s）", pair[1], plain.Code, noisy.Code, noisy.Body.String())
			continue
		}
		if plain.Body.String() != noisy.Body.String() {
			t.Errorf("%s 的响应随未知参数变了:\n 不带=%s\n 带上=%s", pair[1], plain.Body.String(), noisy.Body.String())
		}
	}

	// /metrics/requests 的 model_id 仍是单值、取末位；filters 回显仍是已发布形状。
	requests := getOK(t, app, "/metrics/requests?model_id=model-a&model_id=model-b&caller_type=local&caller_type=access_key")
	if got := canonical.DumpsOrdered(requests.Lookup("filters")); got != `{"caller_type":"access_key","model_id":"model-b"}` {
		t.Errorf("/metrics/requests 的 filters = %s，期望取末位的单值回显", got)
	}
	if got := intField(t, requests, "total_items"); got != 1 {
		t.Errorf("/metrics/requests 取末位后 total_items = %d，期望 1", got)
	}
	// 已发布的 caller_type 仍是单值 Literal：非法值仍是 422（与新参数无关）。
	if recorder := serve(app, http.MethodGet, "/metrics/requests?caller_type=visitor", ""); recorder.Code != http.StatusUnprocessableEntity {
		t.Errorf("/metrics/requests?caller_type=visitor 状态码 = %d，期望 422", recorder.Code)
	}
}

// TestMetricDimensionsEndpoint 断言筛选器选项的形状、排序、显示名与「不随筛选收窄」。
func TestMetricDimensionsEndpoint(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedDimensionData(t, app)

	recorder := serve(app, http.MethodGet, "/ui/metric-dimensions.json", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	body := parseBody(t, recorder.Body.Bytes())
	if got := strings.Join(body.Obj.Keys(), ","); got !=
		"window,providers,models,requested_models,upstream_keys,access_keys,workspaces,caller_types" {
		t.Errorf("顶层键 = %s", got)
	}
	if got := strings.Join(body.Lookup("window").Obj.Keys(), ","); got != "from,to,hours" {
		t.Errorf("window 的键 = %s", got)
	}
	if hours, _ := body.Lookup("window").Lookup("hours").AsFloat(); hours != 24 {
		t.Errorf("window.hours = %v，期望缺省 24", hours)
	}
	expect := map[string]string{
		"providers": `[{"id":"prov-a","requests":2},{"id":"prov-b","requests":2}]`,
		"models":    `[{"id":"model-a","requests":2},{"id":"model-b","requests":2},{"id":"model-c","requests":1}]`,
		"upstream_keys": `[{"provider_id":"prov-a","key_name":"key-a","requests":1},` +
			`{"provider_id":"prov-a","key_name":"key-b","requests":1},` +
			`{"provider_id":"prov-b","key_name":"key-a","requests":1},` +
			`{"provider_id":"prov-b","key_name":"key-b","requests":1}]`,
		// ak1 配在配置里（带名字），ak2 没配：名字是 null 而不是回填成 id。
		"access_keys":  `[{"id":"ak1","name":"试用账号 A","requests":1},{"id":"ak2","name":null,"requests":1}]`,
		"workspaces":   `[{"name":"teamA","requests":3}]`,
		"caller_types": `[{"id":"access_key","requests":2},{"id":"local","requests":2},{"id":"workspace","requests":1}]`,
	}
	for key, want := range expect {
		if got := canonical.DumpsOrdered(body.Lookup(key)); got != want {
			t.Errorf("%s =\n %s\n期望\n %s", key, got, want)
		}
	}

	// 维度参数一律忽略：选项是不筛的读数，不会因为当前选了 model-a 就把 model-b 藏起来。
	filtered := getOK(t, app, "/ui/metric-dimensions.json?model_id=model-a&provider_id=prov-a")
	if got := canonical.DumpsOrdered(filtered.Lookup("models")); got != expect["models"] {
		t.Errorf("带维度参数时 models = %s，期望与不带时相同", got)
	}
	// all_history：window.from 与 hours 都是 null。
	all := getOK(t, app, "/ui/metric-dimensions.json?all_history=true")
	if got := canonical.DumpsOrdered(all.Lookup("window").Lookup("from")); got != "null" {
		t.Errorf("all_history 的 window.from = %s，期望 null", got)
	}
}

// TestMetricDimensionsEndpointGuards 断言校验先于鉴权、四种非完整权限一律 401、非 GET 405。
func TestMetricDimensionsEndpointGuards(t *testing.T) {
	app := newFilteredMetricsApp(t)
	for _, testCase := range []struct {
		query, wantType, wantInput string
	}{
		{"hours=0", "greater_than", "0"},
		{"hours=8761", "less_than_equal", "8761"},
		{"all_history=maybe", "bool_parsing", "maybe"},
	} {
		recorder := serve(app, http.MethodGet, "/ui/metric-dimensions.json?"+testCase.query, "")
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 状态码 = %d，期望 422", testCase.query, recorder.Code)
			continue
		}
		detail := parseBody(t, recorder.Body.Bytes()).Lookup("detail")
		if len(detail.Arr) != 1 || detail.Arr[0].Lookup("type").Str != testCase.wantType ||
			detail.Arr[0].Lookup("input").Str != testCase.wantInput {
			t.Errorf("%s 的 detail = %s", testCase.query, canonical.DumpsOrdered(detail))
		}
	}
	for _, authorization := range []string{"", otherAuthorization, "Bearer amkr-ak-1", "Bearer ws-infer-a", "Bearer panel-a"} {
		if recorder := serve(app, http.MethodGet, "/ui/metric-dimensions.json", authorization); recorder.Code != http.StatusUnauthorized {
			t.Errorf("凭据 %q 的状态码 = %d，期望 401", authorization, recorder.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/metric-dimensions.json", fullAuthorization)
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s 状态码 = %d / Allow = %q，期望 405 / GET", method, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
}
