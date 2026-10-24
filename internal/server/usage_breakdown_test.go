package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件固化 GET /ui/usage-breakdown.json（按一个维度拆分的趋势）的 HTTP 契约：形状、
// 前 N 组 + other、访问密钥显示名的补齐、与 /ui/filtered-metrics.json 的桶网格逐桶对齐、
// 参数校验先于鉴权、鉴权与方法。
//
// 数据复用 metric_dimensions_test.go 的 seedDimensionData（5 行）与 newFilteredMetricsApp
// （配置里只有 ak1 带名字）。

// TestUsageBreakdownEndpoint 断言 200 的形状、组序、other 的折叠与 filters 回显。
func TestUsageBreakdownEndpoint(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedDimensionData(t, app)

	recorder := serve(app, http.MethodGet, "/ui/usage-breakdown.json?limit=2&bucket_seconds=3600", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	body := parseBody(t, recorder.Body.Bytes())
	if got := strings.Join(body.Obj.Keys(), ","); got !=
		"count_semantics,window,group_by,rank_by,bucket_seconds,filters,buckets,groups,other,total_groups" {
		t.Errorf("顶层键 = %s", got)
	}
	// 缺省 group_by=model、rank_by=requests；显式桶宽原样回显。
	if body.Lookup("group_by").Str != "model" || body.Lookup("rank_by").Str != "requests" {
		t.Errorf("缺省 group_by / rank_by = %s / %s", body.Lookup("group_by").Str, body.Lookup("rank_by").Str)
	}
	if got := intField(t, body, "bucket_seconds"); got != 3600 {
		t.Errorf("bucket_seconds = %d，期望 3600", got)
	}
	groups := body.Lookup("groups").Arr
	if len(groups) != 2 {
		t.Fatalf("limit=2 时 groups = %d 组", len(groups))
	}
	if got := canonical.DumpsOrdered(groups[0].Lookup("key")) + canonical.DumpsOrdered(groups[1].Lookup("key")); got !=
		`{"model_id":"model-a"}{"model_id":"model-b"}` {
		t.Errorf("组序 = %s", got)
	}
	if got := intField(t, groups[0], "stats", "requests"); got != 2 {
		t.Errorf("model-a 的 stats.requests = %d，期望 2", got)
	}
	buckets := body.Lookup("buckets").Arr
	for _, key := range groups[0].Lookup("series").Obj.Keys() {
		if got := len(groups[0].Lookup("series").Lookup(key).Arr); got != len(buckets) {
			t.Errorf("series.%s 长度 = %d，期望与 buckets 等长 %d", key, got, len(buckets))
		}
	}
	other := body.Lookup("other")
	if got := intField(t, other, "group_count"); got != 1 {
		t.Errorf("other.group_count = %d，期望 1（model-c）", got)
	}
	if got := intField(t, other, "stats", "requests"); got != 1 {
		t.Errorf("other.stats.requests = %d，期望 1", got)
	}
	if got := intField(t, body, "total_groups"); got != 3 {
		t.Errorf("total_groups = %d，期望 3", got)
	}

	// 带筛选：filters 回显与 filtered-metrics 同形，组集合随之收窄。
	filtered := getOK(t, app, "/ui/usage-breakdown.json?group_by=upstream_key&workspace=teamA&"+
		upstreamKeyQuery("prov-a", "key-a")+"&"+upstreamKeyQuery("prov-b", "key-b"))
	if got := canonical.DumpsOrdered(filtered.Lookup("filters")); got !=
		`{"provider_ids":[],"access_key_ids":[],"model_ids":[],`+
			`"upstream_keys":[{"provider_id":"prov-a","key_name":"key-a"},{"provider_id":"prov-b","key_name":"key-b"}],`+
			`"workspaces":["teamA"],"caller_types":[],"success":null}` {
		t.Errorf("filters 回显 = %s", got)
	}
	keys := make([]string, 0)
	for _, group := range filtered.Lookup("groups").Arr {
		keys = append(keys, canonical.DumpsOrdered(group.Lookup("key")))
	}
	if got := strings.Join(keys, ","); got !=
		`{"provider_id":"prov-a","key_name":"key-a"},{"provider_id":"prov-b","key_name":"key-b"}` {
		t.Errorf("按上游 Key 拆分（成对筛选）的组 = %s", got)
	}
	if filtered.Lookup("other").Kind != canonical.KindNull {
		t.Errorf("没有被截掉的组时 other 应为 null")
	}
}

// TestUsageBreakdownEndpointAttachesAccessKeyNames 断言 group_by=access_key 时显示名由
// 服务端从配置补齐：配置里有名字的给名字，没配的 key 与「不是访问密钥发起」的那组都是
// null，key 对象的键序不变。
func TestUsageBreakdownEndpointAttachesAccessKeyNames(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedDimensionData(t, app)

	body := getOK(t, app, "/ui/usage-breakdown.json?group_by=access_key")
	keys := make([]string, 0)
	for _, group := range body.Lookup("groups").Arr {
		keys = append(keys, canonical.DumpsOrdered(group.Lookup("key")))
	}
	want := `{"access_key_id":null,"access_key_name":null},` +
		`{"access_key_id":"ak1","access_key_name":"试用账号 A"},` +
		`{"access_key_id":"ak2","access_key_name":null}`
	if got := strings.Join(keys, ","); got != want {
		t.Errorf("access_key 分组的 key =\n %s\n期望\n %s", got, want)
	}
}

// TestUsageBreakdownEndpointAlignsWithFilteredMetrics 断言同一组参数下两条读数落在同一张
// 桶网格上：buckets 与 series.points 逐个同 started_at / complete，各组与 other 逐桶相加
// 等于 series 的点位。
func TestUsageBreakdownEndpointAlignsWithFilteredMetrics(t *testing.T) {
	pinMetricsClock(t)
	app := newFilteredMetricsApp(t)
	seedDimensionData(t, app)

	for _, query := range []string{
		"hours=6",
		"hours=2&bucket_seconds=900&success=true",
		"all_history=true&caller_type=local&caller_type=access_key",
	} {
		filtered := getOK(t, app, "/ui/filtered-metrics.json?"+query)
		breakdown := getOK(t, app, "/ui/usage-breakdown.json?group_by=status&limit=1&"+query)
		points := filtered.Lookup("series").Lookup("points").Arr
		buckets := breakdown.Lookup("buckets").Arr
		if len(points) == 0 || len(points) != len(buckets) {
			t.Fatalf("%s：points = %d，buckets = %d", query, len(points), len(buckets))
		}
		if got, want := intField(t, breakdown, "bucket_seconds"), intField(t, filtered, "series", "bucket_seconds"); got != want {
			t.Errorf("%s：bucket_seconds = %d，期望 %d", query, got, want)
		}
		series := []*canonical.Value{}
		for _, group := range breakdown.Lookup("groups").Arr {
			series = append(series, group.Lookup("series"))
		}
		if other := breakdown.Lookup("other"); other.Kind != canonical.KindNull {
			series = append(series, other.Lookup("series"))
		}
		for index, point := range points {
			if point.Lookup("started_at").Str != buckets[index].Lookup("started_at").Str ||
				point.Lookup("complete").Bool != buckets[index].Lookup("complete").Bool {
				t.Errorf("%s：第 %d 桶不对齐：%s vs %s", query, index,
					canonical.DumpsOrdered(buckets[index]), point.Lookup("started_at").Str)
			}
			sum := int64(0)
			for _, item := range series {
				value, _ := item.Lookup("requests").Arr[index].AsInt()
				sum += value
			}
			if want := intField(t, point, "requests"); sum != want {
				t.Errorf("%s：第 %d 桶 requests 合计 = %d，期望 %d", query, index, sum, want)
			}
		}
	}
}

// TestUsageBreakdownEndpointValidation 断言参数校验（先于鉴权、Pydantic 同形的 422）与
// 存储层的点数上限（与 filtered-metrics 同一套 422 文本）。
func TestUsageBreakdownEndpointValidation(t *testing.T) {
	app := newFilteredMetricsApp(t)
	cases := []struct {
		query, wantParam, wantType, wantInput string
	}{
		{"group_by=pool", "group_by", "literal_error", "pool"},
		{"rank_by=cost", "rank_by", "literal_error", "cost"},
		{"limit=0", "limit", "greater_than_equal", "0"},
		{"limit=51", "limit", "less_than_equal", "51"},
		{"limit=1.5", "limit", "int_parsing", "1.5"},
		{"hours=0", "hours", "greater_than", "0"},
		{"bucket_seconds=14", "bucket_seconds", "greater_than_equal", "14"},
		{"bucket_seconds=86401", "bucket_seconds", "less_than_equal", "86401"},
		{"all_history=maybe", "all_history", "bool_parsing", "maybe"},
	}
	for _, testCase := range cases {
		recorder := serve(app, http.MethodGet, "/ui/usage-breakdown.json?"+testCase.query, "")
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 状态码 = %d，期望 422（body=%s）", testCase.query, recorder.Code, recorder.Body.String())
			continue
		}
		detail := parseBody(t, recorder.Body.Bytes()).Lookup("detail")
		if len(detail.Arr) != 1 {
			t.Errorf("%s 的 detail 条数 = %d，期望 1", testCase.query, len(detail.Arr))
			continue
		}
		item := detail.Arr[0]
		loc := item.Lookup("loc")
		if item.Lookup("type").Str != testCase.wantType || item.Lookup("input").Str != testCase.wantInput ||
			len(loc.Arr) != 2 || loc.Arr[1].Str != testCase.wantParam {
			t.Errorf("%s 的 detail = %s", testCase.query, canonical.DumpsOrdered(item))
		}
	}
	// group_by 的期望文案列出全部十个取值，顺序即契约顺序。
	recorder := serve(app, http.MethodGet, "/ui/usage-breakdown.json?group_by=pool", "")
	if !strings.Contains(recorder.Body.String(),
		`"expected":"'model', 'requested_model', 'provider', 'upstream_model', 'upstream_key', `+
			`'access_key', 'workspace', 'caller_type', 'status' or 'api_format'"`) {
		t.Errorf("group_by 的 literal_error = %s", recorder.Body.String())
	}

	// 显式桶太细：hours=24 配 60 秒桶 = 1441 点，与 filtered-metrics 同一句 422。
	recorder = serve(app, http.MethodGet, "/ui/usage-breakdown.json?hours=24&bucket_seconds=60", fullAuthorization)
	if recorder.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(recorder.Body.String(), "time series cannot exceed 500 points") {
		t.Errorf("桶太细：状态码 = %d，body = %s", recorder.Code, recorder.Body.String())
	}
}

// TestUsageBreakdownEndpointGuards 断言四种非完整权限一律 401、非 GET 405 + Allow。
func TestUsageBreakdownEndpointGuards(t *testing.T) {
	app := newFilteredMetricsApp(t)
	for _, authorization := range []string{"", otherAuthorization, "Bearer amkr-ak-1", "Bearer ws-infer-a", "Bearer panel-a"} {
		if recorder := serve(app, http.MethodGet, "/ui/usage-breakdown.json", authorization); recorder.Code != http.StatusUnauthorized {
			t.Errorf("凭据 %q 的状态码 = %d，期望 401", authorization, recorder.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/usage-breakdown.json", fullAuthorization)
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s 状态码 = %d / Allow = %q，期望 405 / GET", method, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
}
