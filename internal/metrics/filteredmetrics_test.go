package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// TestMetricFilterDimensionConditionsAppendedLast 锁定两个多值筛选维度的 SQL 形状。
//
// 这是「新能力不得改动已发布读数」的落点：/metrics、/metrics/series、
// /metrics/requests 三条路由的 SQL 都出自 metricFilter.sql()，只要未筛选时它的输出
// 逐字节不变，那三条路径的行为就不可能被这次改动碰到；筛选时也只是在末尾追加条件与
// 参数，已有条件与参数的顺序都不动。
//
// 顺带锁住另外两条形状：
//   - 空切片与 nil 等价（都不追加条件）；
//   - 多值条件**不得**出现在 filterPairs() 里。那是 /metrics/* 响应里 filters 回显的
//     内容，多一个键就是响应形状变了。
func TestMetricFilterDimensionConditionsAppendedLast(t *testing.T) {
	since := "2026-07-14T00:00:00+08:00"
	until := "2026-07-14T12:00:00+08:00"
	attributed := true
	unfiltered := metricFilter{
		SinceCreatedAt: &since,
		UntilCreatedAt: &until,
		Attributed:     &attributed,
	}

	want := " WHERE created_at >= ? AND created_at <= ?" +
		" AND provider_id IS NOT NULL AND pool_name IS NOT NULL AND upstream_model_id IS NOT NULL"
	whereSQL, parameters := unfiltered.sql()
	if whereSQL != want {
		t.Errorf("未筛选 SQL = %q，期望 %q（必须与加多值筛选之前逐字节相同）", whereSQL, want)
	}
	if len(parameters) != 2 {
		t.Errorf("未筛选参数个数 = %d，期望 2", len(parameters))
	}

	// 空切片与 nil 等价：存储层收到的是「没给」，不是「给了一个空集合」。
	emptySlices := unfiltered
	emptySlices.ProviderIDs = []string{}
	emptySlices.AccessKeyIDs = []string{}
	if whereSQL, parameters = emptySlices.sql(); whereSQL != want || len(parameters) != 2 {
		t.Errorf("空切片不应追加条件：SQL = %q，参数 %d 个", whereSQL, len(parameters))
	}

	// 只有供应商：在末尾追加 `provider_id IN (?, ?)`。
	providersOnly := unfiltered
	providersOnly.ProviderIDs = []string{"openai", "azure"}
	whereSQL, parameters = providersOnly.sql()
	if wantProviders := want + " AND provider_id IN (?, ?)"; whereSQL != wantProviders {
		t.Errorf("供应商筛选 SQL = %q，期望 %q", whereSQL, wantProviders)
	}
	if len(parameters) != 4 || parameters[2] != "openai" || parameters[3] != "azure" {
		t.Errorf("供应商筛选参数 = %v，期望末两位 openai / azure", parameters)
	}

	// 只有访问密钥：单元素也走 IN (?)（单值与多值同一条路径，见
	// TestFilteredMetricsSingleValueMatchesSingleElement）。
	keysOnly := unfiltered
	keysOnly.AccessKeyIDs = []string{"ak1"}
	whereSQL, parameters = keysOnly.sql()
	wantKeys := want + " AND id IN (SELECT request_id FROM request_access_key WHERE access_key_id IN (?))"
	if whereSQL != wantKeys {
		t.Errorf("访问密钥筛选 SQL = %q，期望 %q", whereSQL, wantKeys)
	}
	if len(parameters) != 3 || parameters[2] != "ak1" {
		t.Errorf("访问密钥筛选参数 = %v，期望末位 ak1", parameters)
	}

	// 两个维度都给：先供应商、后访问密钥，参数顺序与条件顺序一致。
	both := unfiltered
	both.ProviderIDs = []string{"openai"}
	both.AccessKeyIDs = []string{"ak1", "ak2"}
	whereSQL, parameters = both.sql()
	wantBoth := want + " AND provider_id IN (?)" +
		" AND id IN (SELECT request_id FROM request_access_key WHERE access_key_id IN (?, ?))"
	if whereSQL != wantBoth {
		t.Errorf("双维度筛选 SQL = %q，期望 %q", whereSQL, wantBoth)
	}
	if len(parameters) != 5 || parameters[2] != "openai" ||
		parameters[3] != "ak1" || parameters[4] != "ak2" {
		t.Errorf("双维度筛选参数 = %v，期望 [since until openai ak1 ak2]", parameters)
	}

	// filters 回显是已发布形状：两个维度都不进它。
	if pairs := (queryFilter{Dimensions: Dimensions{
		ProviderIDs: []string{"openai"}, AccessKeyIDs: []string{"ak1"},
	}}).filterPairs(); len(pairs) != 0 {
		t.Errorf("filterPairs 不应回显这两个维度，实得 %v", pairs)
	}
}

// seedFilteredMetrics 写入一组覆盖「本 key / 别的 key / 完整权限 / 窗口外」的样本。
//
// 六行的分工（时钟固定在 T，写每行时会先把时钟拨到它的时刻）：
//   - T-2h：ak1 的历史行——24 小时窗口内、1 小时窗口外；
//   - T-10m：ak1 的正常行——1 小时窗口内、rate 窗口外；
//   - T：ak1 的成功行（进 rate 窗口）；
//   - T：ak1 的**无上游归属**行（provider 为空、状态码为空）——它只该进 unattributed；
//   - T：ak2 的行——别的访问密钥，任何按 ak1 的读数都不该看见它；
//   - T：完整权限的行（没有访问密钥归属）——同样不该被算进任何一把 key。
//
// 六行都在 openai 一家：要覆盖「两家供应商」的 OR 请用 keyusage_test.go 的
// seedKeyUsage（那边有 openai 与 azure）。
func seedFilteredMetrics(t *testing.T, store *Store, moment string) {
	t.Helper()
	openAI := "openai"
	requested := "m1"
	ok := int64(200)
	now := beijingTime(t, moment)

	installClock(t, now.Add(-2*60*time.Minute))
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m-old", KeyName: "main", Usage: keyUsagePrompt(1000),
		StatusCode: &ok, CallerType: "access_key", AccessKeyID: "ak1",
		ProviderID: &openAI,
	})

	installClock(t, now.Add(-10*time.Minute))
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m1", KeyName: "main", Usage: keyUsagePrompt(100),
		StatusCode: &ok, CallerType: "access_key", AccessKeyID: "ak1",
		RequestedModelID: &requested, ProviderID: &openAI,
	})

	installClock(t, now)
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m1", KeyName: "main", Usage: keyUsagePrompt(50),
		StatusCode: &ok, CallerType: "access_key", AccessKeyID: "ak1",
		RequestedModelID: &requested, ProviderID: &openAI,
	})
	// 这把 key 的「没有上游归属」行：provider 与状态码都是空。
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m2", KeyName: "legacy", Usage: keyUsagePrompt(30),
		CallerType: "access_key", AccessKeyID: "ak1",
	})
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m2", KeyName: "main", Usage: keyUsagePrompt(20),
		StatusCode: &ok, CallerType: "access_key", AccessKeyID: "ak2",
		ProviderID: &openAI,
	})
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m3", KeyName: "main", Usage: keyUsagePrompt(10),
		StatusCode: &ok, CallerType: "local", ProviderID: &openAI,
	})
}

// statRequests 取一行统计里的 requests。
func statRequests(t *testing.T, stats *canonical.Value) int64 {
	t.Helper()
	return statInt(t, stats, "requests")
}

// snapshotTotalRequests 取整页读数里 snapshot.total.requests。
func snapshotTotalRequests(t *testing.T, value *canonical.Value) int64 {
	t.Helper()
	return statRequests(t, snapshotOf(t, value).Lookup("total"))
}

// snapshotOf 取整页读数里的 snapshot 子对象。
func snapshotOf(t *testing.T, value *canonical.Value) *canonical.Value {
	t.Helper()
	snapshot, ok := value.Obj.Get("snapshot")
	if !ok {
		t.Fatal("响应缺少 snapshot")
	}
	return snapshot
}

// seriesRequests 汇总 series 各桶的 requests。
func seriesRequests(t *testing.T, value *canonical.Value) int64 {
	t.Helper()
	series, ok := value.Obj.Get("series")
	if !ok {
		t.Fatal("响应缺少 series")
	}
	points, _ := series.Obj.Get("points")
	total := int64(0)
	for _, point := range points.Arr {
		total += statRequests(t, point)
	}
	return total
}

// requestItems 取 requests 明细的条数；limit=0 时 requests 是 null，此时直接失败
// （调用方只在要了明细时才用它）。
func requestItems(t *testing.T, value *canonical.Value) int {
	t.Helper()
	requests, ok := value.Obj.Get("requests")
	if !ok || requests.Kind != canonical.KindObject {
		t.Fatalf("requests 应为对象，实得 %s", compact(t, requests))
	}
	items, _ := requests.Obj.Get("items")
	return len(items.Arr)
}

// filterIDs 取 filters 里的一个字符串数组，用逗号拼成可比对的文本。
//
// 拼成文本是为了让 nil / 空切片 / 顺序都能直接比：这两个维度的回显必须与请求里给的
// 顺序一致（界面据此回显 chip），而空维度必须渲染成空数组。
func filterIDs(t *testing.T, value *canonical.Value, key string) string {
	t.Helper()
	filters, ok := value.Obj.Get("filters")
	if !ok || filters.Kind != canonical.KindObject {
		t.Fatalf("响应缺少 filters 对象，实得 %s", compact(t, filters))
	}
	field, ok := filters.Obj.Get(key)
	if !ok || field.Kind != canonical.KindArray {
		t.Fatalf("filters.%s 应为数组，实得 %s", key, compact(t, field))
	}
	values := make([]string, 0, len(field.Arr))
	for _, item := range field.Arr {
		values = append(values, item.Str)
	}
	return strings.Join(values, ",")
}

// TestFilteredMetricsMultiValueOrAnd 断言两个维度的语义：维度内 OR、维度间 AND。
//
// 数据用 seedKeyUsage（5 行）：openai 3 行（其中 ak1、ak2 各一行，另有一行完整权限）、
// azure 1 行（完整权限）、没有供应商的一行（完整权限）。这几个数字正好能分开三种筛选：
//   - 供应商多值：openai + azure 两家都该进来，且没有供应商的那行不该被顺手带进来；
//   - 访问密钥多值：ak1 + ak2 两把都该进来，完整权限的行不该进来；
//   - 维度间 AND：ak1 与 ak2 都打给 openai，筛 `access_key_id=ak1&provider_id=openai`
//     时只该有前者——这正是「维度内 OR 被误当成维度间 OR」会露馅的地方。
//
// 每一行都同时校验 snapshot / series / requests 三份读数：它们共用同一个窗口，合计必须
// 相等，否则界面上会出现「概览说 2 次、趋势加起来 3 次」。
func TestFilteredMetricsMultiValueOrAnd(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedKeyUsage(t, store)

	hours := 24.0
	matrix := []struct {
		name         string
		providerIDs  []string
		accessKeyIDs []string
		want         int64
	}{
		{"两个维度都不给", nil, nil, 5},
		{"供应商单值", []string{"openai"}, nil, 3},
		{"供应商多值 OR", []string{"openai", "azure"}, nil, 4},
		{"访问密钥单值", nil, []string{"ak1"}, 1},
		{"访问密钥多值 OR", nil, []string{"ak1", "ak2"}, 2},
		{"维度间 AND", []string{"openai"}, []string{"ak1"}, 1},
		{"维度间 AND 排除不匹配的组合", []string{"azure"}, []string{"ak2"}, 0},
		{"两个维度都多值", []string{"openai", "azure"}, []string{"ak1", "ak2"}, 2},
	}
	for _, testCase := range matrix {
		value, err := store.FilteredMetrics(FilteredMetricsParams{
			Hours:         &hours,
			BucketSeconds: 3600,
			Limit:         200,
			Dimensions: Dimensions{
				ProviderIDs:  testCase.providerIDs,
				AccessKeyIDs: testCase.accessKeyIDs,
			},
		})
		if err != nil {
			t.Fatalf("%s: FilteredMetrics: %v", testCase.name, err)
		}
		if got := snapshotTotalRequests(t, value); got != testCase.want {
			t.Errorf("%s: snapshot.total.requests = %d，期望 %d", testCase.name, got, testCase.want)
		}
		if got := seriesRequests(t, value); got != testCase.want {
			t.Errorf("%s: series 各桶 requests 合计 = %d，期望 %d（与 snapshot 同一个窗口）",
				testCase.name, got, testCase.want)
		}
		if got := requestItems(t, value); int64(got) != testCase.want {
			t.Errorf("%s: requests.items 条数 = %d，期望 %d（与 snapshot 同一个窗口）",
				testCase.name, got, testCase.want)
		}
		if got := filterIDs(t, value, "provider_ids"); got != strings.Join(testCase.providerIDs, ",") {
			t.Errorf("%s: filters.provider_ids = %q，期望 %q", testCase.name, got, testCase.providerIDs)
		}
		if got := filterIDs(t, value, "access_key_ids"); got != strings.Join(testCase.accessKeyIDs, ",") {
			t.Errorf("%s: filters.access_key_ids = %q，期望 %q", testCase.name, got, testCase.accessKeyIDs)
		}
	}
}

// TestFilteredMetricsSingleValueMatchesSingleElement 断言单元素 `IN (?)` 与旧单值路径同量。
//
// 单值时代生成的是 `access_key_id = ?`，现在统一走 `IN (?)`：两者必须给出同一份读数。
// 用一个「一条记录都没有」的第二项来验证这一点——多一个值只该多出它自己的量，而它一条
// 都没有，因此结果必须与单元素时逐字段相同。
func TestFilteredMetricsSingleValueMatchesSingleElement(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedKeyUsage(t, store)

	hours := 24.0
	cases := []struct {
		name         string
		providerIDs  []string
		accessKeyIDs []string
		want         int64
	}{
		{"供应商", []string{"openai"}, nil, 3},
		{"访问密钥", nil, []string{"ak1"}, 1},
	}
	for _, testCase := range cases {
		single, err := store.FilteredMetrics(FilteredMetricsParams{
			Hours: &hours, BucketSeconds: 3600, Limit: 200,
			Dimensions: Dimensions{ProviderIDs: testCase.providerIDs, AccessKeyIDs: testCase.accessKeyIDs},
		})
		if err != nil {
			t.Fatalf("%s: 单元素筛选失败: %v", testCase.name, err)
		}
		// 只在**被测的那个维度**上多挂一个匹配不到的值：另一个维度原样不动，否则测的
		// 就不是「多一个值」而是「多了一个筛不中的维度」了。
		extendedParams := FilteredMetricsParams{Hours: &hours, BucketSeconds: 3600, Limit: 200}
		if len(testCase.providerIDs) > 0 {
			extendedParams.ProviderIDs = append(append([]string{}, testCase.providerIDs...), "no-such-provider")
		}
		if len(testCase.accessKeyIDs) > 0 {
			extendedParams.AccessKeyIDs = append(append([]string{}, testCase.accessKeyIDs...), "no-such-key")
		}
		extended, err := store.FilteredMetrics(extendedParams)
		if err != nil {
			t.Fatalf("%s: 多项筛选失败: %v", testCase.name, err)
		}
		if got := snapshotTotalRequests(t, single); got != testCase.want {
			t.Errorf("%s: 单元素 total.requests = %d，期望 %d", testCase.name, got, testCase.want)
		}
		if got, want := compact(t, snapshotOf(t, extended)), compact(t, snapshotOf(t, single)); got != want {
			t.Errorf("%s: 多一个匹配不到的值不应改变读数:\n 多项=%s\n 单项=%s", testCase.name, got, want)
		}
		if requestItems(t, extended) != requestItems(t, single) {
			t.Errorf("%s: 明细条数也应相同", testCase.name)
		}
	}
}

// TestFilteredMetricsEmptyDimensionsMatchMetrics 断言两个维度都不给时的 snapshot
// 与 `/metrics` 逐字节相同。
//
// 这是「不筛」与「筛一个不存在的值」必须分得开的那条：前者回全实例读数（与 /metrics
// 同源同形），后者回空。filters 回显也要给出两个空数组，界面据此知道当前没有筛选。
func TestFilteredMetricsEmptyDimensionsMatchMetrics(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedKeyUsage(t, store)

	hours := 24.0
	reference, err := store.Snapshot(&hours, nil)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	value, err := store.FilteredMetrics(FilteredMetricsParams{Hours: &hours, BucketSeconds: 3600})
	if err != nil {
		t.Fatalf("FilteredMetrics: %v", err)
	}
	if got, want := compact(t, snapshotOf(t, value)), compact(t, reference); got != want {
		t.Errorf("空筛选的 snapshot 与 /metrics 不同:\n 空筛选=%s\n /metrics=%s", got, want)
	}
	if got := filterIDs(t, value, "provider_ids"); got != "" {
		t.Errorf("filters.provider_ids = %q，期望空数组", got)
	}
	if got := filterIDs(t, value, "access_key_ids"); got != "" {
		t.Errorf("filters.access_key_ids = %q，期望空数组", got)
	}
	// 不存在的值回空，与「不筛」是两件事。
	missing, err := store.FilteredMetrics(FilteredMetricsParams{
		Hours: &hours, BucketSeconds: 3600, Dimensions: Dimensions{AccessKeyIDs: []string{"no-such-key"}},
	})
	if err != nil {
		t.Fatalf("FilteredMetrics（不存在的 key）: %v", err)
	}
	if got := snapshotTotalRequests(t, missing); got != 0 {
		t.Errorf("筛一个不存在的访问密钥 total.requests = %d，期望 0", got)
	}
}

// TestFilteredMetricsAllHistoryUsesTheFiltersOwnSpan 断言 all_history 时三部分仍同窗口。
//
// series 没有「不限窗口」的形态（点是零填充出来的），因此自动桶要按**这组条件自己的**
// 最早一条记录折算跨度：用别人的跨度（或全库跨度）会让趋势图开头多出一大段空桶。
func TestFilteredMetricsAllHistoryUsesTheFiltersOwnSpan(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedFilteredMetrics(t, store, moment)

	value, err := store.FilteredMetrics(FilteredMetricsParams{Dimensions: Dimensions{AccessKeyIDs: []string{"ak1"}}})
	if err != nil {
		t.Fatalf("FilteredMetrics: %v", err)
	}

	window, _ := value.Obj.Get("window")
	if from, _ := window.Obj.Get("from"); from.Kind != canonical.KindNull {
		t.Errorf("all_history 的 window.from = %s，期望 null", compact(t, from))
	}
	if hours, _ := window.Obj.Get("hours"); hours.Kind != canonical.KindNull {
		t.Errorf("all_history 的 window.hours = %s，期望 null", compact(t, hours))
	}

	if got := snapshotTotalRequests(t, value); got != 4 {
		t.Errorf("all_history 的 total.requests = %d，期望 4（含 2 小时前那行）", got)
	}

	series, _ := value.Obj.Get("series")
	bucket := statInt(t, series, "bucket_seconds")
	if bucket < minSeriesBucketSeconds {
		t.Errorf("自动桶 = %d，期望不小于 %d", bucket, minSeriesBucketSeconds)
	}
	points, _ := series.Obj.Get("points")
	if len(points.Arr) > MaxSeriesPoints {
		t.Errorf("series 点数 = %d，超过上限 %d", len(points.Arr), MaxSeriesPoints)
	}
	if got := seriesRequests(t, value); got != 4 {
		t.Errorf("series 各桶 requests 合计 = %d，期望 4（与 snapshot 同一个窗口）", got)
	}
	// 自动桶按这组条件的跨度（2 小时）挑：全库跨度或别人的跨度都会让桶更大或更小。
	// 这里只锁「点数落在上限内」，具体桶宽由 autoSeriesBucket 的公式决定。
	if len(points.Arr) < 2 {
		t.Errorf("两小时跨度至少应有两个桶，实得 %d", len(points.Arr))
	}
}

// TestFilteredMetricsAllHistoryEmptyFilter 断言一组筛不出任何行的条件在 all_history
// 下也能出结果：series 给空数组而不是报错（与空库上的 snapshot / requests 一致）。
func TestFilteredMetricsAllHistoryEmptyFilter(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	seedFilteredMetrics(t, store, "2026-07-14T12:00:00+08:00")

	value, err := store.FilteredMetrics(FilteredMetricsParams{Dimensions: Dimensions{AccessKeyIDs: []string{"no-such-key"}}})
	if err != nil {
		t.Fatalf("FilteredMetrics: %v", err)
	}
	series, _ := value.Obj.Get("series")
	points, _ := series.Obj.Get("points")
	if points.Kind != canonical.KindArray || len(points.Arr) != 0 {
		t.Errorf("筛不出行时 series.points = %s，期望空数组", compact(t, points))
	}
	if got := snapshotTotalRequests(t, value); got != 0 {
		t.Errorf("筛不出行时 total.requests = %d，期望 0", got)
	}
}

// TestFilteredMetricsRejectsTooFineBucket 断言显式桶宽超点数上限时报参数错误。
//
// 与 /metrics/series 同一套文本：hours=24 配 60 秒桶是 1441 点。**不**静默换成更粗的
// 桶——调用方以为拿到的是 60 秒的桶，比一句明确的拒绝糟糕得多。
func TestFilteredMetricsRejectsTooFineBucket(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)

	hours := 24.0
	_, err := store.FilteredMetrics(FilteredMetricsParams{
		Hours: &hours, BucketSeconds: 60, Dimensions: Dimensions{AccessKeyIDs: []string{"ak1"}},
	})
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

	// 同一组参数换成自动桶（0）必须能出结果：否则这个端点的默认调用是不可用的。
	if _, err := store.FilteredMetrics(FilteredMetricsParams{
		Hours: &hours, Dimensions: Dimensions{AccessKeyIDs: []string{"ak1"}},
	}); err != nil {
		t.Fatalf("自动桶应当可用，实得 %v", err)
	}
}

// TestKeyUsageAccessKeyFilterNarrowsSplits 断言 key-usage 的访问密钥筛选收窄三份拆分与
// 未归属。
//
// 筛选语义是「谁**发起**的这次请求」（request_access_key 旁挂表），因此：
//   - upstream_keys / model_keys 里只剩这把 key 打到过的上游 Key；
//   - access_keys 里只剩它自己；
//   - unattributed 变成「这把 key 里没有上游归属的行」，而不是全库的未归属。
func TestKeyUsageAccessKeyFilterNarrowsSplits(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedFilteredMetrics(t, store, moment)

	hours := 1.0
	value, err := store.KeyUsage(KeyUsageParams{Hours: &hours, Dimensions: Dimensions{AccessKeyIDs: []string{"ak1"}}})
	if err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}

	upstream, _ := value.Obj.Get("upstream_keys")
	if len(upstream.Arr) != 1 {
		t.Fatalf("筛选后 upstream_keys 行数 = %d，期望 1（body=%s）", len(upstream.Arr), compact(t, value))
	}
	only := rowAt(t, upstream, 0)
	if got := fieldOf(t, only, "provider_id"); got != "openai" {
		t.Errorf("upstream_keys[0].provider_id = %q，期望 openai", got)
	}
	if got := statRequests(t, statsAt(t, only)); got != 2 {
		t.Errorf("upstream_keys[0].requests = %d，期望 2（T 与 T-10m 两行）", got)
	}

	modelKeys, _ := value.Obj.Get("model_keys")
	if len(modelKeys.Arr) != 1 {
		t.Errorf("筛选后 model_keys 行数 = %d，期望 1", len(modelKeys.Arr))
	}

	access, _ := value.Obj.Get("access_keys")
	if len(access.Arr) != 1 {
		t.Fatalf("筛选后 access_keys 行数 = %d，期望 1（别的 key 不该出现）", len(access.Arr))
	}
	if got := statRequests(t, statsAt(t, rowAt(t, access, 0))); got != 3 {
		t.Errorf("筛选后 access_keys[0].requests = %d，期望 3", got)
	}

	unattributed, _ := value.Obj.Get("unattributed")
	if got := statRequests(t, unattributed); got != 1 {
		t.Errorf("筛选后 unattributed.requests = %d，期望 1（ak1 里没有上游归属的那行）", got)
	}
	if got := statInt(t, unattributed, "total_tokens"); got != 30 {
		t.Errorf("筛选后 unattributed.total_tokens = %d，期望 30", got)
	}

	// 不传筛选：三份拆分必须回到全量口径（ak1 + ak2 两把访问密钥、两个上游行）。
	unfiltered, err := store.KeyUsage(KeyUsageParams{Hours: &hours})
	if err != nil {
		t.Fatalf("KeyUsage（未筛选）: %v", err)
	}
	allAccess, _ := unfiltered.Obj.Get("access_keys")
	if len(allAccess.Arr) != 2 {
		t.Fatalf("未筛选 access_keys 行数 = %d，期望 2", len(allAccess.Arr))
	}
	allUpstream, _ := unfiltered.Obj.Get("upstream_keys")
	if got := statRequests(t, statsAt(t, rowAt(t, allUpstream, 0))); got != 4 {
		t.Errorf("未筛选 upstream_keys 首行 requests = %d，期望 4（local 1 + ak1 2 + ak2 1）", got)
	}
	allUnattributed, _ := unfiltered.Obj.Get("unattributed")
	if got := statRequests(t, allUnattributed); got != 1 {
		t.Errorf("未筛选 unattributed.requests = %d，期望 1", got)
	}
}

// TestKeyUsageMultiValueDimensions 断言 key-usage 的供应商与访问密钥可以多值，且两个
// 维度同时给值时是 AND。
//
// 数据用 seedKeyUsage（openai / azure 两家）。这里锁三件事：
//   - 供应商多值 = 两家的行都在（维度内 OR）；
//   - 访问密钥多值 = 两把 key 的行都在，完整权限的行不在；
//   - 两个维度同时给值 = 交集（ak1 只打给 openai，所以 `provider_id=azure` 配上 ak1
//     时三份拆分与 unattributed 全空）。
func TestKeyUsageMultiValueDimensions(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedKeyUsage(t, store)

	hours := 24.0
	cases := []struct {
		name         string
		providerIDs  []string
		accessKeyIDs []string
		// wantUpstream 是 upstream_keys 的行数；wantUpstreamFirst 是首行的 requests。
		wantUpstream      int
		wantUpstreamFirst int64
		wantAccessKeys    []string
		wantUnattributed  int64
	}{
		{
			name: "两个维度都不给", wantUpstream: 3, wantUpstreamFirst: 1,
			wantAccessKeys: []string{"ak1", "ak2"}, wantUnattributed: 1,
		},
		{
			name: "供应商多值 OR", providerIDs: []string{"openai", "azure"},
			wantUpstream: 3, wantUpstreamFirst: 1,
			wantAccessKeys: []string{"ak1", "ak2"}, wantUnattributed: 0,
		},
		{
			name: "访问密钥多值 OR", accessKeyIDs: []string{"ak1", "ak2"},
			wantUpstream: 2, wantUpstreamFirst: 1,
			wantAccessKeys: []string{"ak1", "ak2"}, wantUnattributed: 0,
		},
		{
			name: "维度间 AND", providerIDs: []string{"openai"}, accessKeyIDs: []string{"ak1"},
			wantUpstream: 1, wantUpstreamFirst: 1,
			wantAccessKeys: []string{"ak1"}, wantUnattributed: 0,
		},
		{
			name: "维度间 AND 把不匹配的组合排空", providerIDs: []string{"azure"}, accessKeyIDs: []string{"ak1"},
			wantUpstream: 0, wantAccessKeys: nil, wantUnattributed: 0,
		},
	}
	for _, testCase := range cases {
		value, err := store.KeyUsage(KeyUsageParams{
			Hours: &hours,
			Dimensions: Dimensions{
				ProviderIDs:  testCase.providerIDs,
				AccessKeyIDs: testCase.accessKeyIDs,
			},
		})
		if err != nil {
			t.Fatalf("%s: KeyUsage: %v", testCase.name, err)
		}
		upstream, _ := value.Obj.Get("upstream_keys")
		if len(upstream.Arr) != testCase.wantUpstream {
			t.Errorf("%s: upstream_keys 行数 = %d，期望 %d（body=%s）",
				testCase.name, len(upstream.Arr), testCase.wantUpstream, compact(t, value))
		} else if testCase.wantUpstream > 0 {
			if got := statRequests(t, statsAt(t, rowAt(t, upstream, 0))); got != testCase.wantUpstreamFirst {
				t.Errorf("%s: upstream_keys 首行 requests = %d，期望 %d",
					testCase.name, got, testCase.wantUpstreamFirst)
			}
		}
		access, _ := value.Obj.Get("access_keys")
		gotKeys := make([]string, 0, len(access.Arr))
		for _, row := range access.Arr {
			gotKeys = append(gotKeys, fieldOf(t, row, "access_key_id"))
		}
		if strings.Join(gotKeys, ",") != strings.Join(testCase.wantAccessKeys, ",") {
			t.Errorf("%s: access_keys = %v，期望 %v", testCase.name, gotKeys, testCase.wantAccessKeys)
		}
		unattributed, _ := value.Obj.Get("unattributed")
		if got := statRequests(t, unattributed); got != testCase.wantUnattributed {
			t.Errorf("%s: unattributed.requests = %d，期望 %d",
				testCase.name, got, testCase.wantUnattributed)
		}
	}
}
