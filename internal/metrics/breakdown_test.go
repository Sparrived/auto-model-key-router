package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件固化 UsageBreakdown（按一个维度拆分的趋势）的口径：十种 group_by 的 key 形状
// 与组序、rank_by、前 N 组 + other 的折叠、stats 与 series 的自洽，以及与
// FilteredMetrics 的 series **逐桶对齐**（同一张桶网格、同一套筛选）。

// usageBreakdownValue 调一次 UsageBreakdown，错误时直接失败。
func usageBreakdownValue(t *testing.T, store *Store, params UsageBreakdownParams) *canonical.Value {
	t.Helper()
	value, err := store.UsageBreakdown(params)
	if err != nil {
		t.Fatalf("UsageBreakdown: %v", err)
	}
	return value
}

// breakdownKeys 把每组的 key 对象渲染成一行文本，按组序拼接。
func breakdownKeys(t *testing.T, value *canonical.Value) string {
	t.Helper()
	keys := make([]string, 0)
	for _, group := range value.Lookup("groups").Arr {
		keys = append(keys, compact(t, group.Lookup("key")))
	}
	return strings.Join(keys, ",")
}

// seriesSum 汇总一条序列。
func seriesSum(series *canonical.Value, field string) int64 {
	total := int64(0)
	for _, item := range series.Lookup(field).Arr {
		value, _ := item.AsInt()
		total += value
	}
	return total
}

// TestUsageBreakdownGroupByKeys 断言十种 group_by 的 key 形状与组序（requests 降序、key
// 升序决胜，NULL 排在同量的最前）。
func TestUsageBreakdownGroupByKeys(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	seedDimensions(t, store)

	cases := []struct {
		groupBy string
		want    string
	}{
		{UsageBreakdownGroupByModel, `{"model_id":"m1"},{"model_id":"m2"},{"model_id":"m3"}`},
		{UsageBreakdownGroupByRequestedModel,
			`{"requested_model_id":"m1"},{"requested_model_id":"m2"},{"requested_model_id":"alias-1"},{"requested_model_id":"m3"}`},
		{UsageBreakdownGroupByProvider, `{"provider_id":"openai"},{"provider_id":"azure"},{"provider_id":null}`},
		// pool_name 全为 NULL，照样按 (供应商, 上游模型) 分出组来——snapshot 的
		// upstream_models 在同一份数据上是空的（见下面的对照断言）。
		{UsageBreakdownGroupByUpstreamModel,
			`{"provider_id":"openai","upstream_model_id":"gpt-4o"},` +
				`{"provider_id":null,"upstream_model_id":null},` +
				`{"provider_id":"azure","upstream_model_id":"gpt-35"},` +
				`{"provider_id":"azure","upstream_model_id":"gpt-4o"},` +
				`{"provider_id":"openai","upstream_model_id":"gpt-4o-mini"}`},
		{UsageBreakdownGroupByUpstreamKey,
			`{"provider_id":"openai","key_name":"main"},` +
				`{"provider_id":null,"key_name":"legacy"},` +
				`{"provider_id":"azure","key_name":"backup"},` +
				`{"provider_id":"azure","key_name":"main"},` +
				`{"provider_id":"openai","key_name":"backup"}`},
		// 显示名由服务端从配置补，指标层只占位。
		{UsageBreakdownGroupByAccessKey,
			`{"access_key_id":null,"access_key_name":null},` +
				`{"access_key_id":"ak1","access_key_name":null},` +
				`{"access_key_id":"ak2","access_key_name":null}`},
		{UsageBreakdownGroupByWorkspace, `{"workspace":null},{"workspace":"teamA"},{"workspace":"teamB"}`},
		{UsageBreakdownGroupByCallerType,
			`{"caller_type":"access_key"},{"caller_type":"local"},{"caller_type":"workspace"}`},
		// 状态码是整数；同量时 NULL 在前、其余按数值升序。
		{UsageBreakdownGroupByStatus,
			`{"status_code":200},{"status_code":null},{"status_code":429},{"status_code":500}`},
		{UsageBreakdownGroupByAPIFormat,
			`{"api_format":"chat/completions"},{"api_format":null},{"api_format":"messages"},{"api_format":"responses"}`},
	}
	hours := 24.0
	for _, testCase := range cases {
		value := usageBreakdownValue(t, store, UsageBreakdownParams{
			Hours: &hours, BucketSeconds: 3600, GroupBy: testCase.groupBy, Limit: 50,
		})
		if got := breakdownKeys(t, value); got != testCase.want {
			t.Errorf("group_by=%s 的 key =\n %s\n期望\n %s", testCase.groupBy, got, testCase.want)
		}
		if got := fieldOf(t, value, "group_by"); got != testCase.groupBy {
			t.Errorf("group_by 回显 = %q，期望 %q", got, testCase.groupBy)
		}
		// 每组 stats 与自己的 series 自洽，全部组加起来是全量。
		total := int64(0)
		for _, group := range value.Lookup("groups").Arr {
			stats, series := group.Lookup("stats"), group.Lookup("series")
			for _, pair := range [][2]string{
				{"requests", "requests"}, {"total_tokens", "total_tokens"},
				{"failures", "failures"}, {"total_duration_ms", "duration_ms"},
				{"total_first_token_ms", "first_token_ms"},
			} {
				if got, want := seriesSum(series, pair[1]), statInt(t, stats, pair[0]); got != want {
					t.Errorf("group_by=%s 的 %s：series 合计 %d != stats %d", testCase.groupBy, pair[1], got, want)
				}
			}
			total += statRequests(t, stats)
		}
		if total != 6 {
			t.Errorf("group_by=%s 各组 requests 合计 = %d，期望 6", testCase.groupBy, total)
		}
		if other := value.Lookup("other"); other.Kind != canonical.KindNull {
			t.Errorf("group_by=%s 没有被截掉的组，other 应为 null，实得 %s", testCase.groupBy, compact(t, other))
		}
	}

	// 对照：同一份数据上 snapshot 的 upstream_models 是空的（三个归属列要求全非空，而
	// pool_name 恒为 NULL）——这正是 upstream_model 分组要修的缺口。
	snapshot, err := store.Snapshot(&hours, nil)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := compact(t, snapshot.Lookup("upstream_models")); got != `{}` {
		t.Errorf("snapshot.upstream_models = %s，期望 {}（pool_name 为 NULL 的数据进不了它）", got)
	}
}

// TestUsageBreakdownShapeAndOther 断言响应形状、limit 截断与 other 的折叠。
func TestUsageBreakdownShapeAndOther(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	seedDimensions(t, store)

	hours := 24.0
	value := usageBreakdownValue(t, store, UsageBreakdownParams{
		Hours: &hours, BucketSeconds: 3600, GroupBy: UsageBreakdownGroupByStatus, Limit: 1,
	})
	wantKeys := "count_semantics,window,group_by,rank_by,bucket_seconds,filters,buckets,groups,other,total_groups"
	if got := strings.Join(value.Obj.Keys(), ","); got != wantKeys {
		t.Errorf("顶层键 = %s，期望 %s", got, wantKeys)
	}
	if got := fieldOf(t, value, "rank_by"); got != UsageBreakdownRankByRequests {
		t.Errorf("rank_by 缺省 = %q，期望 requests", got)
	}
	if got := statInt(t, value, "total_groups"); got != 4 {
		t.Errorf("total_groups = %d，期望 4（截断之前）", got)
	}
	groups := value.Lookup("groups").Arr
	if len(groups) != 1 || compact(t, groups[0].Lookup("key")) != `{"status_code":200}` {
		t.Fatalf("limit=1 时 groups = %s", compact(t, value.Lookup("groups")))
	}
	group := groups[0]
	if got := strings.Join(group.Obj.Keys(), ","); got != "key,stats,series" {
		t.Errorf("组的键 = %s", got)
	}
	wantSeries := "requests,successes,failures,retries,prompt_tokens,completion_tokens,total_tokens," +
		"cached_tokens,duration_ms,first_token_ms"
	if got := strings.Join(group.Lookup("series").Obj.Keys(), ","); got != wantSeries {
		t.Errorf("series 的键 = %s，期望 %s", got, wantSeries)
	}
	// stats 与 snapshot 的分组同形（UsageStats.dict 的 20 个键）。
	if got, want := strings.Join(group.Lookup("stats").Obj.Keys(), ","),
		strings.Join((&UsageStats{}).dict().Obj.Keys(), ","); got != want {
		t.Errorf("stats 的键 = %s，期望与 snapshot 分组同形 %s", got, want)
	}

	other := value.Lookup("other")
	if got := strings.Join(other.Obj.Keys(), ","); got != "group_count,stats,series" {
		t.Fatalf("other 的键 = %s", got)
	}
	if got := statInt(t, other, "group_count"); got != 3 {
		t.Errorf("other.group_count = %d，期望 3", got)
	}
	otherStats := other.Lookup("stats")
	if got := statRequests(t, otherStats); got != 3 {
		t.Errorf("other.stats.requests = %d，期望 3", got)
	}
	// 被并掉的三组：NULL、429、500。status_codes 逐码相加（NULL 不进分布）。
	if got := compact(t, otherStats.Lookup("status_codes")); got != `{"429":1,"500":1}` {
		t.Errorf("other.stats.status_codes = %s", got)
	}
	if got := statInt(t, otherStats, "total_tokens"); got != 30+7+5 {
		t.Errorf("other.stats.total_tokens = %d，期望 42", got)
	}
	if got := statInt(t, otherStats, "max_duration_ms"); got != 30 {
		t.Errorf("other.stats.max_duration_ms = %d，期望 30（取极值而不是相加）", got)
	}
	if got := seriesSum(other.Lookup("series"), "requests"); got != 3 {
		t.Errorf("other.series.requests 合计 = %d，期望 3", got)
	}

	// buckets 与 series 等长：24 小时、1 小时桶、正点封口 = 25 个桶，最后一个未走完。
	buckets := value.Lookup("buckets").Arr
	if len(buckets) != 25 {
		t.Fatalf("buckets = %d 个，期望 25", len(buckets))
	}
	if got := strings.Join(buckets[0].Obj.Keys(), ","); got != "started_at,complete" {
		t.Errorf("bucket 的键 = %s", got)
	}
	if last := buckets[len(buckets)-1]; last.Lookup("complete").Bool {
		t.Error("最后一个桶还在填充中，complete 应为 false")
	}
	if !buckets[0].Lookup("complete").Bool {
		t.Error("第一个桶已经走完，complete 应为 true")
	}
	for _, series := range []*canonical.Value{group.Lookup("series"), other.Lookup("series")} {
		for _, key := range series.Obj.Keys() {
			if got := len(series.Lookup(key).Arr); got != len(buckets) {
				t.Errorf("series.%s 长度 = %d，期望与 buckets 等长 %d", key, got, len(buckets))
			}
		}
	}
	if got := compact(t, value.Lookup("filters")); got != compact(t, dimensionsEcho(Dimensions{})) {
		t.Errorf("未筛选的 filters = %s", got)
	}
}

// TestUsageBreakdownRankByTokens 断言 rank_by=tokens 改变组序：按请求数三组同量（各 2 次，
// 只能靠 key 决胜），按 token 则 teamA（160）> teamB（50）> 未归属（12）。
func TestUsageBreakdownRankByTokens(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	seedDimensions(t, store)

	hours := 24.0
	byRequests := usageBreakdownValue(t, store, UsageBreakdownParams{
		Hours: &hours, BucketSeconds: 3600, GroupBy: UsageBreakdownGroupByWorkspace,
	})
	if got := breakdownKeys(t, byRequests); got != `{"workspace":null},{"workspace":"teamA"},{"workspace":"teamB"}` {
		t.Errorf("按请求数的组序 = %s", got)
	}
	byTokens := usageBreakdownValue(t, store, UsageBreakdownParams{
		Hours: &hours, BucketSeconds: 3600, GroupBy: UsageBreakdownGroupByWorkspace,
		RankBy: UsageBreakdownRankByTokens, Limit: 2,
	})
	if got := breakdownKeys(t, byTokens); got != `{"workspace":"teamA"},{"workspace":"teamB"}` {
		t.Errorf("按 token 的组序 = %s", got)
	}
	if got := fieldOf(t, byTokens, "rank_by"); got != "tokens" {
		t.Errorf("rank_by 回显 = %q", got)
	}
	if got := statInt(t, byTokens.Lookup("other").Lookup("stats"), "total_tokens"); got != 12 {
		t.Errorf("other 的 total_tokens = %d，期望 12（未归属那组）", got)
	}
}

// TestUsageBreakdownAlignsWithFilteredSeries 断言同一组参数下，全部分组与 other 的序列逐桶
// 相加等于 FilteredMetrics 的 series 对应点位，且桶的 started_at / complete 逐个相同。
//
// 封口时刻刻意不在正点（12:17:30）：首桶向前对齐、末桶未走完，两种边界都要对得上。
// 覆盖 hours 窗口、显式桶宽、自动桶宽、all_history 与带筛选五种组合。
func TestUsageBreakdownAlignsWithFilteredSeries(t *testing.T) {
	const moment = "2026-07-14T12:17:30+08:00"
	now := beijingTime(t, moment)
	store := tempStore(t)
	ok := int64(200)
	openai := "openai"
	for _, offset := range []time.Duration{-5 * time.Hour, -150 * time.Minute, -61 * time.Minute, -20 * time.Minute} {
		installClock(t, now.Add(offset))
		recordKeyUsage(t, store, RecordParams{
			ModelID: "m-early", KeyName: "main", ProviderID: &openai, StatusCode: &ok,
			Usage: keyUsagePrompt(11), DurationMS: 9, Workspace: "teamA",
		})
	}
	installClock(t, now)
	seedDimensions(t, store)

	six, three := 6.0, 3.0
	no := false
	cases := []struct {
		name   string
		hours  *float64
		bucket int64
		dims   Dimensions
	}{
		{"6 小时 / 1 小时桶", &six, 3600, Dimensions{}},
		{"6 小时 / 自动桶", &six, 0, Dimensions{}},
		{"3 小时 / 15 分钟桶 / 筛选", &three, 900, Dimensions{Workspaces: []string{"teamA", "teamB"}}},
		{"全部历史 / 自动桶", nil, 0, Dimensions{}},
		{"全部历史 / 失败筛选", nil, 0, Dimensions{Success: &no}},
	}
	for _, testCase := range cases {
		filtered, err := store.FilteredMetrics(FilteredMetricsParams{
			Hours: testCase.hours, BucketSeconds: testCase.bucket, Dimensions: testCase.dims,
		})
		if err != nil {
			t.Fatalf("%s: FilteredMetrics: %v", testCase.name, err)
		}
		points := filtered.Lookup("series").Lookup("points").Arr
		breakdown := usageBreakdownValue(t, store, UsageBreakdownParams{
			Hours: testCase.hours, BucketSeconds: testCase.bucket, Dimensions: testCase.dims,
			GroupBy: UsageBreakdownGroupByModel, Limit: 1,
		})
		if got, want := statInt(t, breakdown, "bucket_seconds"),
			statInt(t, filtered.Lookup("series"), "bucket_seconds"); got != want {
			t.Errorf("%s: bucket_seconds = %d，期望与 filtered-metrics 相同 %d", testCase.name, got, want)
		}
		buckets := breakdown.Lookup("buckets").Arr
		if len(buckets) != len(points) || len(points) == 0 {
			t.Fatalf("%s: buckets = %d 个，series.points = %d 个", testCase.name, len(buckets), len(points))
		}
		series := []*canonical.Value{}
		for _, group := range breakdown.Lookup("groups").Arr {
			series = append(series, group.Lookup("series"))
		}
		if other := breakdown.Lookup("other"); other.Kind != canonical.KindNull {
			series = append(series, other.Lookup("series"))
		}
		for index, point := range points {
			if got, want := fieldOf(t, buckets[index], "started_at"), fieldOf(t, point, "started_at"); got != want {
				t.Errorf("%s: 第 %d 桶 started_at = %s，期望 %s", testCase.name, index, got, want)
			}
			if got, want := buckets[index].Lookup("complete").Bool, point.Lookup("complete").Bool; got != want {
				t.Errorf("%s: 第 %d 桶 complete = %v，期望 %v", testCase.name, index, got, want)
			}
			for _, pair := range [][2]string{
				{"requests", "requests"}, {"failures", "failures"}, {"total_tokens", "total_tokens"},
				{"total_duration_ms", "duration_ms"},
			} {
				sum := int64(0)
				for _, item := range series {
					value, _ := item.Lookup(pair[1]).Arr[index].AsInt()
					sum += value
				}
				if want := statInt(t, point, pair[0]); sum != want {
					t.Errorf("%s: 第 %d 桶 %s 合计 = %d，期望 %d", testCase.name, index, pair[1], sum, want)
				}
			}
		}
		if got, want := compact(t, breakdown.Lookup("window")), compact(t, filtered.Lookup("window")); got != want {
			t.Errorf("%s: window = %s，期望与 filtered-metrics 相同 %s", testCase.name, got, want)
		}
	}
}

// TestUsageBreakdownEmptyAllHistory 断言 all_history 下一条记录都筛不出时是合法的空读数。
func TestUsageBreakdownEmptyAllHistory(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	seedDimensions(t, store)

	value := usageBreakdownValue(t, store, UsageBreakdownParams{
		Dimensions: Dimensions{ModelIDs: []string{"no-such-model"}},
	})
	for key, want := range map[string]string{
		"buckets": `[]`, "groups": `[]`, "other": `null`, "total_groups": `0`, "bucket_seconds": `15`,
		"group_by": `"model"`,
	} {
		if got := compact(t, value.Lookup(key)); got != want {
			t.Errorf("%s = %s，期望 %s", key, got, want)
		}
	}
}

// TestUsageBreakdownValidates 断言存储层的防御性校验与 filtered-metrics 同一套文案。
func TestUsageBreakdownValidates(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	zero, day := 0.0, 24.0
	cases := []struct {
		name   string
		params UsageBreakdownParams
		want   string
	}{
		{"hours 为 0", UsageBreakdownParams{Hours: &zero}, "hours must be greater than zero"},
		{"bucket 为负", UsageBreakdownParams{Hours: &day, BucketSeconds: -1}, "bucket_seconds must be greater than zero"},
		{"limit 超上限", UsageBreakdownParams{Hours: &day, Limit: 51}, "limit must be between 1 and 50"},
		{"group_by 非法", UsageBreakdownParams{Hours: &day, GroupBy: "pool"},
			"group_by must be one of 'model', 'requested_model', 'provider', 'upstream_model', " +
				"'upstream_key', 'access_key', 'workspace', 'caller_type', 'status' or 'api_format'"},
		{"rank_by 非法", UsageBreakdownParams{Hours: &day, RankBy: "cost"}, "rank_by must be one of 'requests' or 'tokens'"},
		{"桶太细", UsageBreakdownParams{Hours: &day, BucketSeconds: 60}, "time series cannot exceed 500 points"},
	}
	for _, testCase := range cases {
		_, err := store.UsageBreakdown(testCase.params)
		validation, ok := err.(*ValidationError)
		if !ok {
			t.Errorf("%s: 错误 = %v，期望 *ValidationError", testCase.name, err)
			continue
		}
		if validation.Message != testCase.want {
			t.Errorf("%s: 文案 = %q，期望 %q", testCase.name, validation.Message, testCase.want)
		}
	}
	// 零值即缺省：group_by=model、rank_by=requests、limit=8、自动桶。
	value := usageBreakdownValue(t, store, UsageBreakdownParams{Hours: &day})
	if got := fieldOf(t, value, "group_by") + "/" + fieldOf(t, value, "rank_by"); got != "model/requests" {
		t.Errorf("缺省 group_by/rank_by = %s", got)
	}
}
