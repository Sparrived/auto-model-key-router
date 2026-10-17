package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
)

// 本文件固化 GET /ui/status-monitor.json（状态监控的渠道健康矩阵）的 HTTP 契约：
// 鉴权、方法、参数校验、三种 group_by 的行身份、桶网格与 series 的同形/同窗口，
// 以及与 snapshot 的合计一致性。
//
// 数据夹具复用 newFilteredMetricsApp：那条夹具已经配好了访问密钥与两种非完整权限
// 凭据（本路由要拒的正是它们），并且 /ui 真的挂载着。状态监控本身不读配置里的
// provider/model，因此不需要额外的配置段。

// statusMonitorCellBody 是矩阵格子里可断言的字段。
type statusMonitorCellBody struct {
	StartedAt         string `json:"started_at"`
	EndedAt           string `json:"ended_at"`
	Complete          bool   `json:"complete"`
	Requests          int64  `json:"requests"`
	Successes         int64  `json:"successes"`
	Failures          int64  `json:"failures"`
	PromptTokens      int64  `json:"prompt_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	TotalFirstTokenMS int64  `json:"total_first_token_ms"`
	TotalDurationMS   int64  `json:"total_duration_ms"`
}

// statusMonitorRowBody 是矩阵的一行；cells 先收原文，键序/键集合由 canonical 解析断言。
type statusMonitorRowBody struct {
	ProviderID      *string           `json:"provider_id"`
	UpstreamModelID *string           `json:"upstream_model_id"`
	KeyName         *string           `json:"key_name"`
	Cells           []json.RawMessage `json:"cells"`
}

// statusMonitorBody 是响应的可断言子集。
type statusMonitorBody struct {
	CountSemantics string `json:"count_semantics"`
	Window         struct {
		From  *string  `json:"from"`
		To    string   `json:"to"`
		Hours *float64 `json:"hours"`
	} `json:"window"`
	Filters struct {
		ProviderIDs  []string `json:"provider_ids"`
		AccessKeyIDs []string `json:"access_key_ids"`
	} `json:"filters"`
	GroupBy       string `json:"group_by"`
	BucketSeconds int64  `json:"bucket_seconds"`
	Snapshot      struct {
		Total struct {
			Requests int64 `json:"requests"`
		} `json:"total"`
	} `json:"snapshot"`
	Series struct {
		BucketSeconds int64 `json:"bucket_seconds"`
		Points        []struct {
			StartedAt string `json:"started_at"`
			EndedAt   string `json:"ended_at"`
			Complete  bool   `json:"complete"`
			Requests  int64  `json:"requests"`
		} `json:"points"`
	} `json:"series"`
	Matrix struct {
		GroupBy       string                 `json:"group_by"`
		Rows          []statusMonitorRowBody `json:"rows"`
		RowsTruncated int64                  `json:"rows_truncated"`
	} `json:"matrix"`
}

// seedStatusMonitorData 写入一组覆盖状态看板全部分支的样本（7 行）。
//
// 与 metrics 包 status_test.go 的 seedStatusMonitor 同一份分工：prov-a 成功/失败/成功
// 三行，prov-b 两行（不同上游模型），两行 provider_id 为 NULL 的历史行；ak1 打 prov-a，
// ak2 打 prov-b，其余行没有访问密钥归属。
func seedStatusMonitorData(t *testing.T, app *App) {
	t.Helper()
	adapter := app.currentMetricsAdapter()
	if adapter == nil || adapter.store == nil {
		t.Fatal("指标不可用，无法准备测试数据")
	}
	provA, provB := "prov-a", "prov-b"
	modelX, modelY := "model-x", "model-y"
	ok, failed := int64(200), int64(500)
	usage := func(prompt, cached int64) *canonical.Value {
		return canonical.NewObjectOf(
			canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(prompt)},
			canonical.ObjectPair{Key: "cached_tokens", Value: canonical.NewIntValue(cached)},
			canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(0)},
		)
	}
	records := []metrics.RecordParams{
		{ModelID: "m1", KeyName: "key-main", Usage: usage(100, 40), StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak1", ProviderID: &provA,
			UpstreamModelID: &modelX, FirstTokenMS: 50, DurationMS: 100},
		{ModelID: "m1", KeyName: "key-main", Usage: usage(50, 0), StatusCode: &failed,
			CallerType: "access_key", AccessKeyID: "ak1", ProviderID: &provA,
			UpstreamModelID: &modelX, FirstTokenMS: 0, DurationMS: 200},
		{ModelID: "m2", KeyName: "key-backup", Usage: usage(30, 10), StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak2", ProviderID: &provB,
			UpstreamModelID: &modelY, FirstTokenMS: 30, DurationMS: 60},
		{ModelID: "m3", KeyName: "key-main", Usage: usage(20, 0), StatusCode: &ok,
			CallerType: "local", ProviderID: &provA,
			UpstreamModelID: &modelY, FirstTokenMS: 20, DurationMS: 40},
		{ModelID: "m4", KeyName: "key-main", Usage: usage(10, 5), StatusCode: &ok,
			CallerType: "local", UpstreamModelID: &modelX, FirstTokenMS: 10, DurationMS: 20},
		{ModelID: "m5", KeyName: "key-legacy", Usage: usage(5, 0),
			CallerType: "local", FirstTokenMS: 0, DurationMS: 5},
		{ModelID: "m6", KeyName: "key-backup", Usage: usage(15, 0), StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak2", ProviderID: &provB,
			UpstreamModelID: &modelX, FirstTokenMS: 15, DurationMS: 25},
	}
	for _, record := range records {
		if err := adapter.store.Record(record); err != nil {
			t.Fatalf("写入状态监控样本失败: %v", err)
		}
	}
}

// decodeStatusMonitor 发一条请求并解析响应（连同原始记录器一起返回，便于断言原始 JSON）。
func decodeStatusMonitor(t *testing.T, app *App, path string) (*httptest.ResponseRecorder, statusMonitorBody) {
	t.Helper()
	recorder := serve(app, http.MethodGet, path, fullAuthorization)
	var body statusMonitorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, recorder.Body.String())
	}
	return recorder, body
}

// statusMonitorCell 解码一个原始格子。
func statusMonitorCell(t *testing.T, raw json.RawMessage) statusMonitorCellBody {
	t.Helper()
	var cell statusMonitorCellBody
	if err := json.Unmarshal(raw, &cell); err != nil {
		t.Fatalf("解析矩阵格子失败: %v（raw=%s）", err, raw)
	}
	return cell
}

// statusMonitorCellKeys 返回一个格子的键序（canonical 保留插入顺序）。
func statusMonitorCellKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	parsed, err := canonical.Parse(raw)
	if err != nil {
		t.Fatalf("解析矩阵格子失败: %v（raw=%s）", err, raw)
	}
	if parsed.Kind != canonical.KindObject {
		t.Fatalf("矩阵格子应为对象，实得 %s", canonical.DumpsOrdered(parsed))
	}
	return parsed.Obj.Keys()
}

// statusMonitorRowIdentity 把一行渲染成 "provider|upstream_model|key_name"，null 用
// 字面量 null。三个身份键必须**恒在**：不属于当前分组的字段是 null 而不是缺键。
func statusMonitorRowIdentity(t *testing.T, row statusMonitorRowBody) string {
	t.Helper()
	render := func(value *string) string {
		if value == nil {
			return "null"
		}
		return *value
	}
	return render(row.ProviderID) + "|" + render(row.UpstreamModelID) + "|" + render(row.KeyName)
}

// statusMonitorRowRequests 汇总一行全部格子的 requests。
func statusMonitorRowRequests(t *testing.T, row statusMonitorRowBody) int64 {
	t.Helper()
	total := int64(0)
	for _, raw := range row.Cells {
		total += statusMonitorCell(t, raw).Requests
	}
	return total
}

// statusMonitorRowsRequests 汇总矩阵可见行全部格子的 requests。
func statusMonitorRowsRequests(t *testing.T, body statusMonitorBody) int64 {
	t.Helper()
	total := int64(0)
	for _, row := range body.Matrix.Rows {
		total += statusMonitorRowRequests(t, row)
	}
	return total
}

// statusMonitorSeriesRequests 汇总 series 各桶的 requests（端点行语义，不覆盖同包
// filtered_metrics_test.go 里的 seriesRequests——那个收的是另一份响应结构）。
func statusMonitorSeriesRequests(body statusMonitorBody) int64 {
	total := int64(0)
	for _, point := range body.Series.Points {
		total += point.Requests
	}
	return total
}

// TestStatusMonitorEndpoint 断言 200 的形状、三份同窗口读数与矩阵的桶网格。
func TestStatusMonitorEndpoint(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedStatusMonitorData(t, app)

	recorder, body := decodeStatusMonitor(t, app, "/ui/status-monitor.json")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", got)
	}
	if body.CountSemantics != "upstream_attempt" {
		t.Errorf("count_semantics = %q，期望 upstream_attempt", body.CountSemantics)
	}

	// —— window / filters / 缺省 group_by / 自动桶 ——
	if body.Window.From == nil {
		t.Error("window.from 不应为 null（这条读数没有 all_history 形态）")
	}
	if body.Window.To == "" {
		t.Error("window.to 不应为空")
	}
	if body.Window.Hours == nil || *body.Window.Hours != 24 {
		t.Errorf("window.hours = %v，期望默认 24", body.Window.Hours)
	}
	if len(body.Filters.ProviderIDs) != 0 || len(body.Filters.AccessKeyIDs) != 0 {
		t.Errorf("不带筛选的 filters = %+v，期望两个空数组", body.Filters)
	}
	if body.GroupBy != "provider" || body.Matrix.GroupBy != "provider" {
		t.Errorf("group_by = %q / matrix.group_by = %q，期望缺省 provider",
			body.GroupBy, body.Matrix.GroupBy)
	}
	// 24 小时窗口的自动桶是梯级里的 600 秒：144 个整桶 + 1 = 145 个点。
	if body.BucketSeconds != 600 || body.Series.BucketSeconds != 600 {
		t.Errorf("bucket_seconds = %d / series.bucket_seconds = %d，期望自动桶 600",
			body.BucketSeconds, body.Series.BucketSeconds)
	}
	if len(body.Series.Points) != 145 {
		t.Errorf("series 点数 = %d，期望 145", len(body.Series.Points))
	}
	if body.Series.Points[len(body.Series.Points)-1].Complete {
		t.Error("series 末点 complete = true，期望 false（这一格还没走完）")
	}

	// —— 三份读数同窗口：合计必须相等 ——
	if got := body.Snapshot.Total.Requests; got != 7 {
		t.Errorf("snapshot.total.requests = %d，期望 7", got)
	}
	if got := statusMonitorSeriesRequests(body); got != 7 {
		t.Errorf("series 各桶 requests 合计 = %d，期望 7（与 snapshot 同一个窗口）", got)
	}
	if got := statusMonitorRowsRequests(t, body); got != 7 {
		t.Errorf("matrix 可见格 requests 合计 = %d，期望 7（未截断时应与 series 相同）", got)
	}

	// —— 矩阵：provider 分组的行身份、行序、桶网格与零填充 ——
	if len(body.Matrix.Rows) != 3 {
		t.Fatalf("矩阵行数 = %d，期望 3（body=%s）", len(body.Matrix.Rows), recorder.Body.String())
	}
	wantRows := []struct {
		identity string
		requests int64
	}{
		{"prov-a|null|null", 3},
		{"null|null|null", 2},
		{"prov-b|null|null", 2},
	}
	for index, want := range wantRows {
		row := body.Matrix.Rows[index]
		if got := statusMonitorRowIdentity(t, row); got != want.identity {
			t.Errorf("rows[%d] 身份 = %q，期望 %q", index, got, want.identity)
		}
		if got := statusMonitorRowRequests(t, row); got != want.requests {
			t.Errorf("rows[%d] requests = %d，期望 %d", index, got, want.requests)
		}
		if len(row.Cells) != len(body.Series.Points) {
			t.Errorf("rows[%d] 的格子数 = %d，series 点数 = %d，两者必须同网格",
				index, len(row.Cells), len(body.Series.Points))
		}
	}
	// 未归属行必须保留成 provider_id = null（前端显示「未归属」），而不是被丢掉。
	if body.Matrix.Rows[1].ProviderID != nil {
		t.Errorf("未归属行的 provider_id = %v，期望 null", *body.Matrix.Rows[1].ProviderID)
	}

	// 格子只回 10 个键且顺序固定（矩阵最多约 1 万个格子，不能带完整 UsageStats）。
	wantCellKeys := []string{
		"started_at", "ended_at", "complete", "requests", "successes",
		"failures", "prompt_tokens", "cached_tokens", "total_first_token_ms", "total_duration_ms",
	}
	if got := statusMonitorCellKeys(t, body.Matrix.Rows[0].Cells[0]); !slices.Equal(got, wantCellKeys) {
		t.Errorf("cell 键序 = %v，期望 %v", got, wantCellKeys)
	}

	// 数据都落在窗口末端：各行的聚合值应等于逐格求和（零填充不丢数据）。
	provA := body.Matrix.Rows[0]
	sums := map[string]int64{}
	for _, raw := range provA.Cells {
		cell := statusMonitorCell(t, raw)
		sums["successes"] += cell.Successes
		sums["failures"] += cell.Failures
		sums["prompt_tokens"] += cell.PromptTokens
		sums["cached_tokens"] += cell.CachedTokens
		sums["total_first_token_ms"] += cell.TotalFirstTokenMS
		sums["total_duration_ms"] += cell.TotalDurationMS
	}
	wantSums := map[string]int64{
		"successes": 2, "failures": 1, "prompt_tokens": 170, "cached_tokens": 40,
		"total_first_token_ms": 70, "total_duration_ms": 340,
	}
	for key, want := range wantSums {
		if got := sums[key]; got != want {
			t.Errorf("prov-a 逐格求和 %s = %d，期望 %d", key, got, want)
		}
	}
	// 桶网格的边界：首格与 series 首点对齐，末格与 series 末点对齐。
	firstCell := statusMonitorCell(t, body.Matrix.Rows[0].Cells[0])
	lastCell := statusMonitorCell(t, body.Matrix.Rows[0].Cells[len(provA.Cells)-1])
	if firstCell.StartedAt != body.Series.Points[0].StartedAt {
		t.Errorf("矩阵首格 started_at = %q，series 首点 = %q，两者必须同网格",
			firstCell.StartedAt, body.Series.Points[0].StartedAt)
	}
	if lastCell.EndedAt != body.Series.Points[len(body.Series.Points)-1].EndedAt {
		t.Errorf("矩阵末格 ended_at = %q，series 末点 = %q，两者必须同网格",
			lastCell.EndedAt, body.Series.Points[len(body.Series.Points)-1].EndedAt)
	}

	// —— series 的点必须与 /metrics/series 完全同形（直接复用 seriesPoints） ——
	reference := serve(app, http.MethodGet, "/metrics/series?hours=24&bucket_seconds=600", fullAuthorization)
	if reference.Code != http.StatusOK {
		t.Fatalf("/metrics/series 状态码 = %d，期望 200（body=%s）", reference.Code, reference.Body.String())
	}
	ourPointKeys := statusMonitorSeriesPointKeys(t, recorder)
	referencePointKeys := statusMonitorMetricsSeriesPointKeys(t, reference)
	if !slices.Equal(ourPointKeys, referencePointKeys) {
		t.Errorf("series 点键序 = %v，期望与 /metrics/series 相同 %v", ourPointKeys, referencePointKeys)
	}
}

// statusMonitorSeriesPointKeys 取 /ui/status-monitor.json 响应的 series 首点键序。
func statusMonitorSeriesPointKeys(t *testing.T, recorder *httptest.ResponseRecorder) []string {
	t.Helper()
	parsed, err := canonical.Parse(recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("解析状态监控响应失败: %v", err)
	}
	points := parsed.Lookup("series").Lookup("points")
	if points == nil || points.Kind != canonical.KindArray || len(points.Arr) == 0 {
		t.Fatalf("series.points 不是非空数组")
	}
	return points.Arr[0].Obj.Keys()
}

// statusMonitorMetricsSeriesPointKeys 取 /metrics/series 响应的首点键序。
func statusMonitorMetricsSeriesPointKeys(t *testing.T, recorder *httptest.ResponseRecorder) []string {
	t.Helper()
	parsed, err := canonical.Parse(recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("解析 /metrics/series 响应失败: %v", err)
	}
	points := parsed.Lookup("points")
	if points == nil || points.Kind != canonical.KindArray || len(points.Arr) == 0 {
		t.Fatalf("/metrics/series 的 points 不是非空数组")
	}
	return points.Arr[0].Obj.Keys()
}

// TestStatusMonitorGroupByModes 断言三种 group_by 的行身份与行序。
func TestStatusMonitorGroupByModes(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedStatusMonitorData(t, app)

	cases := []struct {
		groupBy  string
		wantRows []string
	}{
		{
			groupBy: "provider",
			wantRows: []string{
				"prov-a|null|null", "null|null|null", "prov-b|null|null",
			},
		},
		{
			groupBy: "provider_model",
			wantRows: []string{
				"prov-a|model-x|null",
				"null|null|null",
				"null|model-x|null",
				"prov-a|model-y|null",
				"prov-b|model-x|null",
				"prov-b|model-y|null",
			},
		},
		{
			groupBy: "provider_key",
			wantRows: []string{
				"prov-a|null|key-main",
				"prov-b|null|key-backup",
				"null|null|key-legacy",
				"null|null|key-main",
			},
		},
	}
	for _, testCase := range cases {
		_, body := decodeStatusMonitor(t, app,
			"/ui/status-monitor.json?group_by="+testCase.groupBy+"&bucket_seconds=3600")
		if body.GroupBy != testCase.groupBy || body.Matrix.GroupBy != testCase.groupBy {
			t.Errorf("%s: group_by = %q / matrix.group_by = %q，期望回显",
				testCase.groupBy, body.GroupBy, body.Matrix.GroupBy)
		}
		if body.BucketSeconds != 3600 {
			t.Errorf("%s: bucket_seconds = %d，期望显式给的 3600", testCase.groupBy, body.BucketSeconds)
		}
		gotRows := make([]string, 0, len(body.Matrix.Rows))
		for _, row := range body.Matrix.Rows {
			gotRows = append(gotRows, statusMonitorRowIdentity(t, row))
		}
		if !slices.Equal(gotRows, testCase.wantRows) {
			t.Errorf("%s: 矩阵行 = %v，期望 %v", testCase.groupBy, gotRows, testCase.wantRows)
		}
		// 分组换了，但三份读数的窗口与合计不变。
		if got := body.Snapshot.Total.Requests; got != 7 {
			t.Errorf("%s: snapshot.total.requests = %d，期望 7", testCase.groupBy, got)
		}
		if got := statusMonitorSeriesRequests(body); got != 7 {
			t.Errorf("%s: series 合计 = %d，期望 7", testCase.groupBy, got)
		}
		if got := statusMonitorRowsRequests(t, body); got != 7 {
			t.Errorf("%s: matrix 可见格合计 = %d，期望 7", testCase.groupBy, got)
		}
	}
}

// TestStatusMonitorFilters 断言两个筛选维度在端点上的语义（维度内 OR、维度间 AND）。
func TestStatusMonitorFilters(t *testing.T) {
	app := newFilteredMetricsApp(t)
	seedStatusMonitorData(t, app)

	cases := []struct {
		name         string
		query        string
		want         int64
		wantRows     []string
		wantProvider []string
		wantKeys     []string
	}{
		{
			name: "两个维度都不给", query: "", want: 7,
			wantRows: []string{"prov-a|null|null", "null|null|null", "prov-b|null|null"},
		},
		{
			name: "供应商单值", query: "provider_id=prov-a", want: 3,
			wantRows: []string{"prov-a|null|null"}, wantProvider: []string{"prov-a"},
		},
		{
			name: "供应商多值 OR", query: "provider_id=prov-a&provider_id=prov-b", want: 5,
			wantRows:     []string{"prov-a|null|null", "prov-b|null|null"},
			wantProvider: []string{"prov-a", "prov-b"},
		},
		{
			name: "访问密钥单值", query: "access_key_id=ak1", want: 2,
			wantRows: []string{"prov-a|null|null"}, wantKeys: []string{"ak1"},
		},
		{
			name: "访问密钥多值 OR", query: "access_key_id=ak1&access_key_id=ak2", want: 4,
			wantRows: []string{"prov-a|null|null", "prov-b|null|null"},
			wantKeys: []string{"ak1", "ak2"},
		},
		{
			name: "维度间 AND", query: "provider_id=prov-b&access_key_id=ak2", want: 2,
			wantRows:     []string{"prov-b|null|null"},
			wantProvider: []string{"prov-b"}, wantKeys: []string{"ak2"},
		},
		{
			name:  "维度间 AND 把不匹配的组合排空",
			query: "provider_id=prov-b&access_key_id=ak1", want: 0,
			wantRows: []string{}, wantProvider: []string{"prov-b"}, wantKeys: []string{"ak1"},
		},
		{
			name:  "两个维度都多值",
			query: "provider_id=prov-a&provider_id=prov-b&access_key_id=ak1&access_key_id=ak2", want: 4,
			wantRows:     []string{"prov-a|null|null", "prov-b|null|null"},
			wantProvider: []string{"prov-a", "prov-b"}, wantKeys: []string{"ak1", "ak2"},
		},
	}
	for _, testCase := range cases {
		_, body := decodeStatusMonitor(t, app, "/ui/status-monitor.json?"+testCase.query)
		if got := body.Snapshot.Total.Requests; got != testCase.want {
			t.Errorf("%s: snapshot.total.requests = %d，期望 %d", testCase.name, got, testCase.want)
		}
		if got := statusMonitorSeriesRequests(body); got != testCase.want {
			t.Errorf("%s: series 合计 = %d，期望 %d（与 snapshot 同一个窗口）",
				testCase.name, got, testCase.want)
		}
		if got := statusMonitorRowsRequests(t, body); got != testCase.want {
			t.Errorf("%s: matrix 可见格合计 = %d，期望 %d", testCase.name, got, testCase.want)
		}
		gotRows := make([]string, 0, len(body.Matrix.Rows))
		for _, row := range body.Matrix.Rows {
			gotRows = append(gotRows, statusMonitorRowIdentity(t, row))
		}
		if !slices.Equal(gotRows, testCase.wantRows) {
			t.Errorf("%s: 矩阵行 = %v，期望 %v（矩阵要跟着筛选收窄）",
				testCase.name, gotRows, testCase.wantRows)
		}
		if strings.Join(body.Filters.ProviderIDs, ",") != strings.Join(testCase.wantProvider, ",") {
			t.Errorf("%s: filters.provider_ids = %v，期望 %v",
				testCase.name, body.Filters.ProviderIDs, testCase.wantProvider)
		}
		if strings.Join(body.Filters.AccessKeyIDs, ",") != strings.Join(testCase.wantKeys, ",") {
			t.Errorf("%s: filters.access_key_ids = %v，期望 %v",
				testCase.name, body.Filters.AccessKeyIDs, testCase.wantKeys)
		}
	}
}

// TestStatusMonitorRejectsInvalidParameters 断言参数校验先于鉴权，且边界齐全。
//
// 全部 422 用例都不带凭据：FastAPI 在调用路由函数前解析参数，所以这些应当是 422
// 而不是 401。多值参数逐项校验，`input` 必须是出错那一项的原文。
func TestStatusMonitorRejectsInvalidParameters(t *testing.T) {
	app := newFilteredMetricsApp(t)
	longItem := strings.Repeat("x", 513)

	cases := []struct {
		name      string
		path      string
		wantInput string
		wantType  string
	}{
		{"hours 为 0", "/ui/status-monitor.json?hours=0", "0", "greater_than"},
		{"hours 超过一年", "/ui/status-monitor.json?hours=8761", "8761", "less_than_equal"},
		{"bucket_seconds 小于下限",
			"/ui/status-monitor.json?bucket_seconds=14", "14", "greater_than_equal"},
		{"bucket_seconds 超过上限",
			"/ui/status-monitor.json?bucket_seconds=86401", "86401", "less_than_equal"},
		{"bucket_seconds 显式 0", "/ui/status-monitor.json?bucket_seconds=0", "0", "greater_than_equal"},
		{"group_by 非法", "/ui/status-monitor.json?group_by=model", "model", "literal_error"},
		{"provider_id 为空串", "/ui/status-monitor.json?provider_id=", "", "string_too_short"},
		{"provider_id 第二项为空串",
			"/ui/status-monitor.json?provider_id=prov-a&provider_id=", "", "string_too_short"},
		{"access_key_id 超长",
			"/ui/status-monitor.json?access_key_id=" + longItem, longItem, "string_too_long"},
	}
	for _, testCase := range cases {
		recorder := serve(app, http.MethodGet, testCase.path, "")
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: 状态码 = %d，期望 422（body=%s）",
				testCase.name, recorder.Code, recorder.Body.String())
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
			t.Errorf("%s: input = %v，期望 %q（必须是出错那一项的原文）",
				testCase.name, detail.Input, testCase.wantInput)
		}
		if len(detail.Loc) != 2 || detail.Loc[0] != "query" {
			t.Errorf("%s: loc = %v，期望 [query <参数名>]", testCase.name, detail.Loc)
		}
	}

	// 参数合法但不带凭据：401 而不是 422——「校验先于鉴权」的反面也要成立。
	if recorder := serve(app, http.MethodGet, "/ui/status-monitor.json?provider_id=prov-a", ""); recorder.Code != http.StatusUnauthorized {
		t.Errorf("合法参数不带凭据的状态码 = %d，期望 401", recorder.Code)
	}

	// 显式给了太细的桶：参数本身合法（15..86400），但点数超上限是**参数**问题，
	// 由存储层的防御性校验给出与 /metrics/series 同一套文本的 422。
	recorder := serve(app, http.MethodGet,
		"/ui/status-monitor.json?hours=24&bucket_seconds=60", fullAuthorization)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("桶太细的状态码 = %d，期望 422（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "time series cannot exceed 500 points") {
		t.Errorf("桶太细的响应 = %s，期望带上点数上限的说明", recorder.Body.String())
	}
}

// TestStatusMonitorRequiresFullAuth 断言四种非完整权限凭据都被拒。
func TestStatusMonitorRequiresFullAuth(t *testing.T) {
	app := newFilteredMetricsApp(t)

	for _, authorization := range []string{
		"",
		otherAuthorization,
		"Bearer amkr-ak-1",  // 配置里真实存在的访问密钥
		"Bearer ws-infer-a", // 配置里真实存在的工作空间推理凭据
		"Bearer panel-a",    // 配置里真实存在的工作空间面板凭据
	} {
		recorder := serve(app, http.MethodGet, "/ui/status-monitor.json?provider_id=prov-a", authorization)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("凭据 %q 的状态码 = %d，期望 401", authorization, recorder.Code)
		}
	}
}

// TestStatusMonitorRejectsNonGet 断言非 GET 回 405 并带 Allow 头。
func TestStatusMonitorRejectsNonGet(t *testing.T) {
	app := newFilteredMetricsApp(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/status-monitor.json", fullAuthorization)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s 状态码 = %d，期望 405", method, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf("%s 的 Allow = %q，期望 GET", method, allow)
		}
	}
}

// TestStatusMonitorTruncatesRows 断言 60 行截断：矩阵只回前 60 行，series 统计全部行。
func TestStatusMonitorTruncatesRows(t *testing.T) {
	app := newFilteredMetricsApp(t)
	adapter := app.currentMetricsAdapter()
	if adapter == nil || adapter.store == nil {
		t.Fatal("指标不可用，无法准备测试数据")
	}
	ok := int64(200)
	const rowCount = 65
	for index := 0; index < rowCount; index++ {
		provider := "p" + twoDigits(index)
		if err := adapter.store.Record(metrics.RecordParams{
			ModelID: "m", KeyName: "k", StatusCode: &ok, CallerType: "local",
			ProviderID: &provider,
		}); err != nil {
			t.Fatalf("写入指标失败: %v", err)
		}
	}

	_, body := decodeStatusMonitor(t, app, "/ui/status-monitor.json?bucket_seconds=3600")
	if len(body.Matrix.Rows) != 60 {
		t.Fatalf("矩阵行数 = %d，期望上限 60", len(body.Matrix.Rows))
	}
	if body.Matrix.RowsTruncated != rowCount-60 {
		t.Errorf("rows_truncated = %d，期望 %d", body.Matrix.RowsTruncated, rowCount-60)
	}
	if got := statusMonitorRowIdentity(t, body.Matrix.Rows[0]); got != "p00|null|null" {
		t.Errorf("首行 = %q，期望 p00", got)
	}
	if got := statusMonitorRowIdentity(t, body.Matrix.Rows[59]); got != "p59|null|null" {
		t.Errorf("末行 = %q，期望 p59", got)
	}
	if got := statusMonitorRowsRequests(t, body); got != 60 {
		t.Errorf("matrix 可见格 requests 合计 = %d，期望 60（只含可见行）", got)
	}
	if got := statusMonitorSeriesRequests(body); got != rowCount {
		t.Errorf("series 合计 = %d，期望 %d（series 必须统计被截断的行）", got, rowCount)
	}
	if got := body.Snapshot.Total.Requests; got != rowCount {
		t.Errorf("snapshot.total.requests = %d，期望 %d", got, rowCount)
	}
}

// twoDigits 把 0..99 渲染成两位十进制（用例里的 provider id 需要字典序与数字序一致）。
func twoDigits(value int) string {
	return fmt.Sprintf("%02d", value)
}

// TestStatusMonitorEmptyStore 断言空库也回完整可渲染的读数（零填充网格 + 空行数组）。
func TestStatusMonitorEmptyStore(t *testing.T) {
	app := newFilteredMetricsApp(t)

	recorder, body := decodeStatusMonitor(t, app, "/ui/status-monitor.json")
	if recorder.Code != http.StatusOK {
		t.Fatalf("空库状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := body.Snapshot.Total.Requests; got != 0 {
		t.Errorf("空库 snapshot.total.requests = %d，期望 0", got)
	}
	if len(body.Series.Points) != 145 {
		t.Errorf("空库 series 点数 = %d，期望 145（零填充的完整网格）", len(body.Series.Points))
	}
	for index, point := range body.Series.Points {
		if point.Requests != 0 {
			t.Errorf("空库 points[%d].requests = %d，期望 0", index, point.Requests)
			break
		}
	}
	if body.Matrix.Rows == nil {
		t.Error("空库 matrix.rows = null，期望空数组（界面不必为 null 写分支）")
	}
	if len(body.Matrix.Rows) != 0 {
		t.Errorf("空库 matrix.rows 行数 = %d，期望 0", len(body.Matrix.Rows))
	}
	if body.Matrix.RowsTruncated != 0 {
		t.Errorf("空库 rows_truncated = %d，期望 0", body.Matrix.RowsTruncated)
	}
}
