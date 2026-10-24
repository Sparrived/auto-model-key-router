package metrics

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件固化「状态监控」读数（StatusMonitor）的口径：三种 group_by 的行身份与计数、
// 与 series 逐列对齐的桶网格、行序与 60 行截断、两个筛选维度，以及与 snapshot 的
// 同窗口一致性。
//
// 它是这条读数的**唯一一条可运行检查**（参照实现没有状态看板，没有可比对的 oracle），
// 因此要同时锁住「算得对」与「形状稳定」两件事。

// statusMonitorMoment 是所有用例共用的窗口封口时刻。
//
// 取正点：它同时对齐 30 秒到 6 小时之间的全部自动桶梯级，桶网格的断言因此可以写
// 精确的桶数，而不是「大概 145 个」。
const statusMonitorMoment = "2026-07-14T12:00:00+08:00"

// seedStatusMonitor 写入一组覆盖状态看板全部分支的样本（7 行，全部落在 T）。
//
// 分工：
//   - prov-a：成功 100 + 失败 500 + 成功 20 三行（成功率高但有一次失败，缓存 40）；
//   - prov-b：两行都成功，另一把上游 Key 与另一个上游模型（各家同名字段要各归各的）；
//   - provider_id = NULL：两行历史行，其中一行连 upstream_model_id 也是 NULL——它们必须
//     保留成「未归属」行，并且不能混进按供应商筛选的任何一组。
//
// 访问密钥归属：ak1 两行（都打 prov-a），ak2 两行（一行 prov-b/model-y、一行
// prov-b/model-x），其余三行没有归属——维度间 AND 的用例靠它区分。
func seedStatusMonitor(t *testing.T, store *Store) {
	t.Helper()
	installClock(t, beijingTime(t, statusMonitorMoment))

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
	records := []RecordParams{
		// 1. prov-a/model-x/key-main，ak1，成功。
		{ModelID: "m1", KeyName: "key-main", Usage: usage(100, 40), StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak1", ProviderID: &provA,
			UpstreamModelID: &modelX, FirstTokenMS: 50, DurationMS: 100},
		// 2. 同一条渠道的失败请求（成功率与 failures 都要能断言）。
		{ModelID: "m1", KeyName: "key-main", Usage: usage(50, 0), StatusCode: &failed,
			CallerType: "access_key", AccessKeyID: "ak1", ProviderID: &provA,
			UpstreamModelID: &modelX, FirstTokenMS: 0, DurationMS: 200},
		// 3. prov-b/model-y/key-backup，ak2，成功。
		{ModelID: "m2", KeyName: "key-backup", Usage: usage(30, 10), StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak2", ProviderID: &provB,
			UpstreamModelID: &modelY, FirstTokenMS: 30, DurationMS: 60},
		// 4. prov-a/model-y/key-main，完整权限（无访问密钥归属）。
		{ModelID: "m3", KeyName: "key-main", Usage: usage(20, 0), StatusCode: &ok,
			CallerType: "local", ProviderID: &provA,
			UpstreamModelID: &modelY, FirstTokenMS: 20, DurationMS: 40},
		// 5. 未归属：provider 为 NULL，但上游模型有值。
		{ModelID: "m4", KeyName: "key-main", Usage: usage(10, 5), StatusCode: &ok,
			CallerType: "local", UpstreamModelID: &modelX, FirstTokenMS: 10, DurationMS: 20},
		// 6. 未归属：provider 与上游模型都是 NULL，状态码也缺（历史行）。成功率的分母
		//    因此包含一次「无状态码」的失败。
		{ModelID: "m5", KeyName: "key-legacy", Usage: usage(5, 0),
			CallerType: "local", FirstTokenMS: 0, DurationMS: 5},
		// 7. prov-b/model-x/key-backup，ak2：与第 3 行同一家、不同上游模型。
		{ModelID: "m6", KeyName: "key-backup", Usage: usage(15, 0), StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak2", ProviderID: &provB,
			UpstreamModelID: &modelX, FirstTokenMS: 15, DurationMS: 25},
	}
	for _, record := range records {
		if err := store.Record(record); err != nil {
			t.Fatalf("写入状态监控样本失败: %v", err)
		}
	}
}

// statusMonitorValue 调一次 StatusMonitor，错误时直接失败。
func statusMonitorValue(t *testing.T, store *Store, params StatusMonitorParams) *canonical.Value {
	t.Helper()
	value, err := store.StatusMonitor(params)
	if err != nil {
		t.Fatalf("StatusMonitor: %v", err)
	}
	return value
}

// statusMonitorField 取响应里的一个字段，缺失时直接失败。
func statusMonitorField(t *testing.T, value *canonical.Value, key string) *canonical.Value {
	t.Helper()
	field, ok := value.Obj.Get(key)
	if !ok {
		t.Fatalf("响应缺少字段 %s（body=%s）", key, compact(t, value))
	}
	return field
}

// statusMatrixRows 取 matrix.rows。
func statusMatrixRows(t *testing.T, value *canonical.Value) []*canonical.Value {
	t.Helper()
	rows := statusMonitorField(t, statusMonitorField(t, value, "matrix"), "rows")
	if rows == nil || rows.Kind != canonical.KindArray {
		t.Fatalf("matrix.rows 应为数组，实得 %s", compact(t, rows))
	}
	return rows.Arr
}

// statusSeriesPoints 取 series.points。
func statusSeriesPoints(t *testing.T, value *canonical.Value) []*canonical.Value {
	t.Helper()
	points := statusMonitorField(t, statusMonitorField(t, value, "series"), "points")
	if points == nil || points.Kind != canonical.KindArray {
		t.Fatalf("series.points 应为数组，实得 %s", compact(t, points))
	}
	return points.Arr
}

// statusCells 取一行里的 cells。
func statusCells(t *testing.T, row *canonical.Value) []*canonical.Value {
	t.Helper()
	cells, ok := row.Obj.Get("cells")
	if !ok || cells.Kind != canonical.KindArray {
		t.Fatalf("行缺少 cells 数组（body=%s）", compact(t, row))
	}
	return cells.Arr
}

// statusRowIdentity 把一行的身份渲染成 "provider|upstream_model|key_name"，NULL 用
// 字面量 null。三项**恒在**（不属于当前分组的那两项必须是 null），这也一并断言了。
func statusRowIdentity(t *testing.T, row *canonical.Value) string {
	t.Helper()
	parts := make([]string, 0, 3)
	for _, key := range []string{"provider_id", "upstream_model_id", "key_name"} {
		field, ok := row.Obj.Get(key)
		if !ok {
			t.Fatalf("行缺少身份字段 %s（body=%s）", key, compact(t, row))
		}
		switch field.Kind {
		case canonical.KindNull:
			parts = append(parts, "null")
		case canonical.KindString:
			parts = append(parts, field.Str)
		default:
			t.Fatalf("身份字段 %s 应为字符串或 null，实得 %s", key, compact(t, field))
		}
	}
	return strings.Join(parts, "|")
}

// statusRowRequests 汇总一行全部格子的 requests。
func statusRowRequests(t *testing.T, row *canonical.Value) int64 {
	t.Helper()
	total := int64(0)
	for _, cell := range statusCells(t, row) {
		total += statInt(t, cell, "requests")
	}
	return total
}

// statusMatrixRequests 汇总矩阵可见行的全部 requests。
func statusMatrixRequests(t *testing.T, value *canonical.Value) int64 {
	t.Helper()
	total := int64(0)
	for _, row := range statusMatrixRows(t, value) {
		total += statusRowRequests(t, row)
	}
	return total
}

// TestStatusMonitorMatrix 断言三种 group_by 的行身份、计数、行序与桶网格。
func TestStatusMonitorMatrix(t *testing.T) {
	store := tempStore(t)
	seedStatusMonitor(t, store)

	const hours = 24.0
	value := statusMonitorValue(t, store, StatusMonitorParams{Hours: hours, BucketSeconds: 3600})

	// —— 三份读数共用同一个窗口：窗口起点对齐桶边界时合计必须相等 ——
	// 定点时钟取正点，窗口起点也落在桶边界上；未对齐时首桶会多看窗口前的记录，
	// 这条等式不成立（见 docs/API.md 的「桶网格从桶边界起算」）。
	if got := snapshotTotalRequests(t, value); got != 7 {
		t.Errorf("snapshot.total.requests = %d，期望 7", got)
	}
	if got := seriesRequests(t, value); got != 7 {
		t.Errorf("series 各桶 requests 合计 = %d，期望 7（窗口对齐桶边界时与 snapshot 相等）", got)
	}
	if got := statusMatrixRequests(t, value); got != 7 {
		t.Errorf("matrix 可见格 requests 合计 = %d，期望 7（未截断时应与 series 相同）", got)
	}

	// —— 顶层与 window ——
	if got := fieldOf(t, value, "group_by"); got != StatusGroupByProvider {
		t.Errorf("group_by = %q，期望缺省 provider", got)
	}
	if got := statInt(t, value, "bucket_seconds"); got != 3600 {
		t.Errorf("bucket_seconds = %d，期望显式给的 3600", got)
	}
	window := statusMonitorField(t, value, "window")
	if got := fieldOf(t, window, "from"); got != "2026-07-13T12:00:00+08:00" {
		t.Errorf("window.from = %q，期望窗口起点", got)
	}
	if got := fieldOf(t, window, "to"); got != statusMonitorMoment {
		t.Errorf("window.to = %q，期望统一的 now", got)
	}
	if got := statInt(t, statusMonitorField(t, value, "series"), "bucket_seconds"); got != 3600 {
		t.Errorf("series.bucket_seconds = %d，期望与顶层一致", got)
	}

	// —— provider 分组：行序按 requests 降序，未归属行保留成 provider=null ——
	rows := statusMatrixRows(t, value)
	wantProviderRows := []struct {
		identity string
		requests int64
	}{
		{"prov-a|null|null", 3},
		{"null|null|null", 2}, // 与 prov-b 同为 2：身份字典序里 NULL 排最前
		{"prov-b|null|null", 2},
	}
	if len(rows) != len(wantProviderRows) {
		t.Fatalf("provider 分组的行数 = %d，期望 %d（body=%s）", len(rows), len(wantProviderRows), compact(t, value))
	}
	for index, want := range wantProviderRows {
		if got := statusRowIdentity(t, rows[index]); got != want.identity {
			t.Errorf("provider 分组 rows[%d] 身份 = %q，期望 %q", index, got, want.identity)
		}
		if got := statusRowRequests(t, rows[index]); got != want.requests {
			t.Errorf("provider 分组 rows[%d] requests = %d，期望 %d", index, got, want.requests)
		}
	}

	// —— 桶网格：与 series 逐列对齐、零填充、尾部未完成桶 complete=false ——
	cells := statusCells(t, rows[0])
	points := statusSeriesPoints(t, value)
	if len(cells) != len(points) {
		t.Fatalf("cells 列数 = %d，series 点数 = %d，两者必须同网格", len(cells), len(points))
	}
	if len(cells) != 25 {
		t.Errorf("24 小时 / 3600 秒桶的列数 = %d，期望 25", len(cells))
	}
	wantCellKeys := []string{
		"started_at", "ended_at", "complete", "requests", "successes",
		"failures", "prompt_tokens", "cached_tokens", "total_first_token_ms", "total_duration_ms",
	}
	if got := cells[0].Obj.Keys(); !slices.Equal(got, wantCellKeys) {
		t.Errorf("cell 键序 = %v，期望 %v（只回这 10 个键，不带完整 UsageStats）", got, wantCellKeys)
	}
	if got := fieldOf(t, cells[0], "started_at"); got != "2026-07-13T12:00:00+08:00" {
		t.Errorf("首格 started_at = %q，期望窗口起点（桶对齐后）", got)
	}
	if got := fieldOf(t, points[0], "started_at"); got != fieldOf(t, cells[0], "started_at") {
		t.Errorf("矩阵首格与 series 首点的 started_at 不同：%q vs %q",
			fieldOf(t, cells[0], "started_at"), fieldOf(t, points[0], "started_at"))
	}
	if got := fieldOf(t, cells[len(cells)-1], "ended_at"); got != fieldOf(t, points[len(points)-1], "ended_at") {
		t.Errorf("矩阵末格与 series 末点的 ended_at 不同")
	}
	// 数据都写在窗口末端的桶里：其余格子必须零填充（不是缺格），末格才是那一行。
	for index := 0; index < len(cells)-1; index++ {
		if got := statInt(t, cells[index], "requests"); got != 0 {
			t.Errorf("零填充失败：cells[%d].requests = %d，期望 0", index, got)
		}
	}
	last := cells[len(cells)-1]
	lastWant := map[string]int64{
		"requests": 3, "successes": 2, "failures": 1,
		"prompt_tokens": 170, "cached_tokens": 40,
		"total_first_token_ms": 70, "total_duration_ms": 340,
	}
	for key, want := range lastWant {
		if got := statInt(t, last, key); got != want {
			t.Errorf("prov-a 末格 %s = %d，期望 %d", key, got, want)
		}
	}
	if complete, _ := last.Lookup("complete").AsBool(); complete {
		t.Error("窗口末格 complete = true，期望 false（这一格还没走完）")
	}
	if complete, _ := cells[0].Lookup("complete").AsBool(); !complete {
		t.Error("历史格 complete = false，期望 true")
	}

	// —— provider_model 分组 ——
	byModel := statusMonitorValue(t, store, StatusMonitorParams{
		Hours: hours, BucketSeconds: 3600, GroupBy: StatusGroupByProviderModel,
	})
	if got := fieldOf(t, byModel, "group_by"); got != StatusGroupByProviderModel {
		t.Errorf("provider_model 的 group_by = %q", got)
	}
	wantModelRows := []struct {
		identity string
		requests int64
	}{
		{"prov-a|model-x|null", 2},
		{"null|null|null", 1},
		{"null|model-x|null", 1},
		{"prov-a|model-y|null", 1},
		{"prov-b|model-x|null", 1},
		{"prov-b|model-y|null", 1},
	}
	modelRows := statusMatrixRows(t, byModel)
	if len(modelRows) != len(wantModelRows) {
		t.Fatalf("provider_model 分组行数 = %d，期望 %d（body=%s）",
			len(modelRows), len(wantModelRows), compact(t, byModel))
	}
	for index, want := range wantModelRows {
		if got := statusRowIdentity(t, modelRows[index]); got != want.identity {
			t.Errorf("provider_model rows[%d] 身份 = %q，期望 %q", index, got, want.identity)
		}
		if got := statusRowRequests(t, modelRows[index]); got != want.requests {
			t.Errorf("provider_model rows[%d] requests = %d，期望 %d", index, got, want.requests)
		}
	}

	// —— provider_key 分组 ——
	byKey := statusMonitorValue(t, store, StatusMonitorParams{
		Hours: hours, BucketSeconds: 3600, GroupBy: StatusGroupByProviderKey,
	})
	wantKeyRows := []struct {
		identity string
		requests int64
	}{
		{"prov-a|null|key-main", 3},
		{"prov-b|null|key-backup", 2},
		{"null|null|key-legacy", 1},
		{"null|null|key-main", 1},
	}
	keyRows := statusMatrixRows(t, byKey)
	if len(keyRows) != len(wantKeyRows) {
		t.Fatalf("provider_key 分组行数 = %d，期望 %d（body=%s）",
			len(keyRows), len(wantKeyRows), compact(t, byKey))
	}
	for index, want := range wantKeyRows {
		if got := statusRowIdentity(t, keyRows[index]); got != want.identity {
			t.Errorf("provider_key rows[%d] 身份 = %q，期望 %q", index, got, want.identity)
		}
		if got := statusRowRequests(t, keyRows[index]); got != want.requests {
			t.Errorf("provider_key rows[%d] requests = %d，期望 %d", index, got, want.requests)
		}
	}
}

// TestStatusMonitorFilters 断言两个筛选维度的语义与三份读数的一致收窄。
//
// 维度内 OR、维度间 AND；都不给 = 全实例。矩阵的行集合也必须跟着收窄——否则界面上
// 「筛了 prov-a」而矩阵里还挂着 prov-b 的行。
func TestStatusMonitorFilters(t *testing.T) {
	store := tempStore(t)
	seedStatusMonitor(t, store)

	const hours = 24.0
	cases := []struct {
		name         string
		providerIDs  []string
		accessKeyIDs []string
		want         int64
		wantRows     []string
	}{
		{
			name: "两个维度都不给", want: 7,
			wantRows: []string{"prov-a|null|null", "null|null|null", "prov-b|null|null"},
		},
		{
			name: "供应商单值", providerIDs: []string{"prov-a"}, want: 3,
			wantRows: []string{"prov-a|null|null"},
		},
		{
			name: "供应商多值 OR", providerIDs: []string{"prov-a", "prov-b"}, want: 5,
			wantRows: []string{"prov-a|null|null", "prov-b|null|null"},
		},
		{
			name: "访问密钥单值", accessKeyIDs: []string{"ak1"}, want: 2,
			wantRows: []string{"prov-a|null|null"},
		},
		{
			name: "访问密钥多值 OR", accessKeyIDs: []string{"ak1", "ak2"}, want: 4,
			wantRows: []string{"prov-a|null|null", "prov-b|null|null"},
		},
		{
			name: "维度间 AND", providerIDs: []string{"prov-b"}, accessKeyIDs: []string{"ak2"}, want: 2,
			wantRows: []string{"prov-b|null|null"},
		},
		{
			name:        "维度间 AND 把不匹配的组合排空",
			providerIDs: []string{"prov-b"}, accessKeyIDs: []string{"ak1"},
			want: 0, wantRows: []string{},
		},
		{
			name:        "两个维度都多值",
			providerIDs: []string{"prov-a", "prov-b"}, accessKeyIDs: []string{"ak1", "ak2"},
			want:     4,
			wantRows: []string{"prov-a|null|null", "prov-b|null|null"},
		},
	}
	for _, testCase := range cases {
		value := statusMonitorValue(t, store, StatusMonitorParams{
			Hours: hours, BucketSeconds: 3600,
			Dimensions: Dimensions{ProviderIDs: testCase.providerIDs, AccessKeyIDs: testCase.accessKeyIDs},
		})
		if got := snapshotTotalRequests(t, value); got != testCase.want {
			t.Errorf("%s: snapshot.total.requests = %d，期望 %d", testCase.name, got, testCase.want)
		}
		if got := seriesRequests(t, value); got != testCase.want {
			t.Errorf("%s: series 各桶 requests 合计 = %d，期望 %d（定点时钟下窗口对齐桶边界，与 snapshot 相等）",
				testCase.name, got, testCase.want)
		}
		if got := statusMatrixRequests(t, value); got != testCase.want {
			t.Errorf("%s: matrix 可见格 requests 合计 = %d，期望 %d", testCase.name, got, testCase.want)
		}
		rows := statusMatrixRows(t, value)
		gotRows := make([]string, 0, len(rows))
		for _, row := range rows {
			gotRows = append(gotRows, statusRowIdentity(t, row))
		}
		if !slices.Equal(gotRows, testCase.wantRows) {
			t.Errorf("%s: matrix 行 = %v，期望 %v（矩阵要跟着筛选收窄）",
				testCase.name, gotRows, testCase.wantRows)
		}
		// filters 回显筛选值本身；未筛的维度是空数组。
		if got := filterIDs(t, value, "provider_ids"); got != strings.Join(testCase.providerIDs, ",") {
			t.Errorf("%s: filters.provider_ids = %q，期望 %q", testCase.name, got, testCase.providerIDs)
		}
		if got := filterIDs(t, value, "access_key_ids"); got != strings.Join(testCase.accessKeyIDs, ",") {
			t.Errorf("%s: filters.access_key_ids = %q，期望 %q", testCase.name, got, testCase.accessKeyIDs)
		}
	}
}

// TestStatusMonitorTruncatesRows 断言 60 行截断只影响矩阵，series 仍统计全部行。
//
// 截断如果也作用在 series 上，翻页/换维度时趋势图的总量会悄悄变化——那正是这份读数
// 「三份同窗口」承诺要避免的事。
func TestStatusMonitorTruncatesRows(t *testing.T) {
	installClock(t, beijingTime(t, statusMonitorMoment))
	store := tempStore(t)

	const rowCount = 65
	ok := int64(200)
	for index := 0; index < rowCount; index++ {
		provider := fmt.Sprintf("p%02d", index)
		recordKeyUsage(t, store, RecordParams{
			ModelID: "m", KeyName: "k", Usage: keyUsagePrompt(1),
			StatusCode: &ok, CallerType: "local", ProviderID: &provider,
		})
	}

	value := statusMonitorValue(t, store, StatusMonitorParams{Hours: 24, BucketSeconds: 3600})
	rows := statusMatrixRows(t, value)
	if len(rows) != statusMatrixMaxRows {
		t.Fatalf("矩阵行数 = %d，期望上限 %d", len(rows), statusMatrixMaxRows)
	}
	matrix := statusMonitorField(t, value, "matrix")
	if got := statInt(t, matrix, "rows_truncated"); got != rowCount-statusMatrixMaxRows {
		t.Errorf("rows_truncated = %d，期望 %d", got, rowCount-statusMatrixMaxRows)
	}
	// 同量（每行 1 次）时按身份字典序：p00 打头、p59 收尾，被截掉的是 p60~p64。
	if got := statusRowIdentity(t, rows[0]); got != "p00|null|null" {
		t.Errorf("首行 = %q，期望 p00", got)
	}
	if got := statusRowIdentity(t, rows[len(rows)-1]); got != "p59|null|null" {
		t.Errorf("末行 = %q，期望 p59", got)
	}
	if got := statusMatrixRequests(t, value); got != statusMatrixMaxRows {
		t.Errorf("矩阵可见格 requests 合计 = %d，期望 %d（只含可见行）", got, statusMatrixMaxRows)
	}
	if got := seriesRequests(t, value); got != rowCount {
		t.Errorf("series 各桶 requests 合计 = %d，期望 %d（必须统计被截断的行）", got, rowCount)
	}
	if got := snapshotTotalRequests(t, value); got != rowCount {
		t.Errorf("snapshot.total.requests = %d，期望 %d", got, rowCount)
	}
}

// TestStatusMonitorEmptyStore 断言空库也回一份完整可渲染的读数。
//
// series 必须是**零填充的完整网格**（不是空数组）：界面拿它直接画折线，少一个点就
// 得为空库写分支；矩阵的行则是空数组（不是 null），界面不必再判空。
func TestStatusMonitorEmptyStore(t *testing.T) {
	installClock(t, beijingTime(t, statusMonitorMoment))
	store := tempStore(t)

	value := statusMonitorValue(t, store, StatusMonitorParams{Hours: 24})

	if got := statInt(t, value, "bucket_seconds"); got != 600 {
		t.Errorf("空库 24 小时窗口的自动桶 = %d，期望梯级里的 600", got)
	}
	points := statusSeriesPoints(t, value)
	if len(points) != 145 {
		t.Errorf("空库 series 点数 = %d，期望 145（24 小时 / 600 秒 + 1）", len(points))
	}
	for index, point := range points {
		if got := statInt(t, point, "requests"); got != 0 {
			t.Errorf("空库 points[%d].requests = %d，期望 0", index, got)
			break
		}
	}
	if complete, _ := points[len(points)-1].Lookup("complete").AsBool(); complete {
		t.Error("空库末点 complete = true，期望 false")
	}
	if got := seriesRequests(t, value); got != 0 {
		t.Errorf("空库 series 合计 = %d，期望 0", got)
	}
	if got := snapshotTotalRequests(t, value); got != 0 {
		t.Errorf("空库 snapshot.total.requests = %d，期望 0", got)
	}

	rows := statusMatrixRows(t, value)
	if len(rows) != 0 {
		t.Errorf("空库 matrix.rows = %d 行，期望空数组", len(rows))
	}
	matrix := statusMonitorField(t, value, "matrix")
	if got := statInt(t, matrix, "rows_truncated"); got != 0 {
		t.Errorf("空库 rows_truncated = %d，期望 0", got)
	}
	// 窗口端点也不能是 null：这条读数没有 all_history 形态。
	window := statusMonitorField(t, value, "window")
	if from, _ := window.Obj.Get("from"); from.Kind != canonical.KindString {
		t.Errorf("空库 window.from = %s，期望字符串", compact(t, from))
	}
}

// TestStatusMonitorAutoBucket 断言自动桶按契约的梯级与列数上限选取。
func TestStatusMonitorAutoBucket(t *testing.T) {
	installClock(t, beijingTime(t, statusMonitorMoment))
	store := tempStore(t)

	cases := []struct {
		hours float64
		want  int64
	}{
		{1, 30},
		{6, 300},
		{24, 600},
		{168, 3600},
		{720, 21600},
		// 一年（8760 小时）连梯级顶端（一天一桶）也有 366 列：自动桶按整天继续放大到
		// 3 天一桶（123 列）。列数上限是硬约束，梯级是偏好。
		{8760, 259200},
	}
	for _, testCase := range cases {
		value := statusMonitorValue(t, store, StatusMonitorParams{Hours: testCase.hours})
		if got := statInt(t, value, "bucket_seconds"); got != testCase.want {
			t.Errorf("hours=%v: bucket_seconds = %d，期望 %d", testCase.hours, got, testCase.want)
		}
		series := statusMonitorField(t, value, "series")
		if got := statInt(t, series, "bucket_seconds"); got != testCase.want {
			t.Errorf("hours=%v: series.bucket_seconds = %d，期望 %d", testCase.hours, got, testCase.want)
		}
		// 列数上界是**公式**给的：实际列数因为向下对齐只会更少。
		if columns := int64(len(statusSeriesPoints(t, value))); columns > statusMatrixMaxColumns+1 {
			t.Errorf("hours=%v: series 点数 = %d，超过矩阵列数上限", testCase.hours, columns)
		}
	}

	// 显式桶太细：与 /metrics/series 同一套文本的 422（由服务端翻译）。
	_, err := store.StatusMonitor(StatusMonitorParams{Hours: 24, BucketSeconds: 60})
	if err == nil {
		t.Fatal("hours=24 配 60 秒桶应当报错")
	}
	validation, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("错误类型 = %T，期望 *ValidationError", err)
	}
	if validation.Message != "time series cannot exceed 500 points" {
		t.Errorf("错误文本 = %q，期望与 /metrics/series 一致", validation.Message)
	}
}

func TestStatusMonitorFailureStatusCodes(t *testing.T) {
	store := tempStore(t)
	seedStatusMonitor(t, store)
	ok, redirect := int64(200), int64(302)
	provider := "prov-a"
	for _, record := range []RecordParams{
		{ModelID: "empty", KeyName: "k", ProviderID: &provider, AccessKeyID: "ak1", StatusCode: &ok, Failed: true},
		{ModelID: "redirect", KeyName: "k", ProviderID: &provider, AccessKeyID: "ak1", StatusCode: &redirect},
	} {
		if err := store.Record(record); err != nil {
			t.Fatal(err)
		}
	}
	for _, testCase := range []struct {
		name   string
		params StatusMonitorParams
		want   string
	}{
		{"all", StatusMonitorParams{Hours: 24}, `{"none":1,"200":1,"500":1}`},
		{"provider", StatusMonitorParams{Hours: 24, Dimensions: Dimensions{ProviderIDs: []string{"prov-a"}}}, `{"200":1,"500":1}`},
		{"key", StatusMonitorParams{Hours: 24, Dimensions: Dimensions{AccessKeyIDs: []string{"ak2"}}}, `{}`},
		{"matching key", StatusMonitorParams{Hours: 24, Dimensions: Dimensions{AccessKeyIDs: []string{"ak1"}}}, `{"200":1,"500":1}`},
		{"intersection", StatusMonitorParams{Hours: 24, Dimensions: Dimensions{ProviderIDs: []string{"prov-b"}, AccessKeyIDs: []string{"ak1"}}}, `{}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := statusMonitorValue(t, store, testCase.params)
			got := canonical.DumpsOrdered(statusMonitorField(t, value, "failure_status_codes"))
			if got != testCase.want {
				t.Fatalf("失败分组 = %s，期望 %s", got, testCase.want)
			}
		})
	}
	// 窗口外的 200 失败不污染分组；旧快照的状态码合计仍保持原协议。
	installClock(t, beijingTime(t, "2026-07-15T13:00:00+08:00"))
	value := statusMonitorValue(t, store, StatusMonitorParams{Hours: 24})
	if got := canonical.DumpsOrdered(statusMonitorField(t, value, "failure_status_codes")); got != `{}` {
		t.Fatalf("窗口外失败不应计入: %s", got)
	}
}

// TestStatusMonitorValidatesParameters 断言存储层的防御性校验。
//
// 服务端 query.go 会先把参数挡一道（Pydantic 口径的 422），但存储层是公开接缝，
// 不能假设调用方都走过 HTTP。文案与既有读数风格一致（FilteredMetrics 的 hours 文案）。
func TestStatusMonitorValidatesParameters(t *testing.T) {
	installClock(t, beijingTime(t, statusMonitorMoment))
	store := tempStore(t)

	cases := []struct {
		name    string
		params  StatusMonitorParams
		message string
	}{
		{"hours 为 0", StatusMonitorParams{Hours: 0}, "hours must be greater than zero"},
		{"hours 为负", StatusMonitorParams{Hours: -1}, "hours must be greater than zero"},
		{"hours 超过一年", StatusMonitorParams{Hours: 8761}, "hours must be less than or equal to 8760"},
		{"bucket 为负", StatusMonitorParams{Hours: 24, BucketSeconds: -1},
			"bucket_seconds must be greater than zero"},
		{"bucket 小于下限", StatusMonitorParams{Hours: 24, BucketSeconds: 7},
			"bucket_seconds must be between 15 and 86400"},
		{"bucket 超过上限", StatusMonitorParams{Hours: 24, BucketSeconds: 86401},
			"bucket_seconds must be between 15 and 86400"},
		{"group_by 非法", StatusMonitorParams{Hours: 24, GroupBy: "model"},
			"group_by must be one of 'provider', 'provider_model' or 'provider_key'"},
	}
	for _, testCase := range cases {
		_, err := store.StatusMonitor(testCase.params)
		if err == nil {
			t.Errorf("%s: 期望报错，实得 nil", testCase.name)
			continue
		}
		validation, ok := err.(*ValidationError)
		if !ok {
			t.Errorf("%s: 错误类型 = %T，期望 *ValidationError", testCase.name, err)
			continue
		}
		if validation.Message != testCase.message {
			t.Errorf("%s: 错误文本 = %q，期望 %q", testCase.name, validation.Message, testCase.message)
		}
	}

	// 空 group_by 是缺省值，不是非法值：存储层自己兜成 provider。
	value := statusMonitorValue(t, store, StatusMonitorParams{Hours: 24, GroupBy: ""})
	if got := fieldOf(t, value, "group_by"); got != StatusGroupByProvider {
		t.Errorf("空 group_by 的响应 = %q，期望兜成 provider", got)
	}
}
