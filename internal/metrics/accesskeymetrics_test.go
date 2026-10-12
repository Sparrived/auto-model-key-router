package metrics

import (
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// TestMetricFilterAccessKeyConditionAppendedLast 锁定访问密钥过滤条件的 SQL 形状。
//
// 这是「新能力不得改动已发布读数」的落点：/metrics、/metrics/series、
// /metrics/requests 三条路由的 SQL 都出自 metricFilter.sql()，只要未过滤时它的输出
// 逐字节不变，那三条路径的行为就不可能被这次改动碰到；过滤时也只是在末尾追加一个
// 条件、一个参数，已有条件与参数的顺序都不动。
//
// 顺带锁住另一条形状：过滤条件**不得**出现在 filterPairs() 里。那是 /metrics/* 响应
// 里 filters 回显的内容，多一个键就是响应形状变了。
func TestMetricFilterAccessKeyConditionAppendedLast(t *testing.T) {
	since := "2026-07-14T00:00:00+08:00"
	until := "2026-07-14T12:00:00+08:00"
	attributed := true
	unfiltered := metricFilter{
		SinceCreatedAt: &since,
		UntilCreatedAt: &until,
		Attributed:     &attributed,
	}

	whereSQL, parameters := unfiltered.sql()
	want := " WHERE created_at >= ? AND created_at <= ?" +
		" AND provider_id IS NOT NULL AND pool_name IS NOT NULL AND upstream_model_id IS NOT NULL"
	if whereSQL != want {
		t.Errorf("未过滤 SQL = %q，期望 %q（必须与加过滤能力之前逐字节相同）", whereSQL, want)
	}
	if len(parameters) != 2 {
		t.Errorf("未过滤参数个数 = %d，期望 2", len(parameters))
	}

	accessKeyID := "ak1"
	filtered := unfiltered
	filtered.AccessKeyID = &accessKeyID
	whereSQL, parameters = filtered.sql()
	want += " AND id IN (SELECT request_id FROM request_access_key WHERE access_key_id = ?)"
	if whereSQL != want {
		t.Errorf("过滤 SQL = %q，期望 %q（条件只能追加在末尾）", whereSQL, want)
	}
	if len(parameters) != 3 {
		t.Fatalf("过滤参数个数 = %d，期望 3", len(parameters))
	}
	if parameters[2] != "ak1" {
		t.Errorf("末位参数 = %v，期望 ak1", parameters[2])
	}

	// filters 回显是已发布形状：过滤条件不进它。
	if pairs := (queryFilter{AccessKeyID: &accessKeyID}).filterPairs(); len(pairs) != 0 {
		t.Errorf("filterPairs 不应回显访问密钥过滤，实得 %v", pairs)
	}
}

// seedAccessKeyMetrics 写入一组覆盖「本 key / 别的 key / 完整权限 / 窗口外」的样本。
//
// 六行的分工（时钟固定在 T，写每行时会先把时钟拨到它的时刻）：
//   - T-2h：ak1 的历史行——24 小时窗口内、1 小时窗口外；
//   - T-10m：ak1 的正常行——1 小时窗口内、rate 窗口外；
//   - T：ak1 的成功行（进 rate 窗口）；
//   - T：ak1 的**无上游归属**行（provider 为空、状态码为空）——它只该进 unattributed；
//   - T：ak2 的行——别的访问密钥，任何按 ak1 的读数都不该看见它；
//   - T：完整权限的行（没有访问密钥归属）——同样不该被算进任何一把 key。
func seedAccessKeyMetrics(t *testing.T, store *Store, moment string) {
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

// TestAccessKeyMetricsNarrowsEveryReadingToTheKey 断言整页读数的三部分都收窄到这一把 key。
//
// 锁四件事：
//   - snapshot 的总量、分组与 rate 窗口（current_rpm / current_tpm）只含该 key；
//   - series 的桶里只有该 key 的行（且窗口与 snapshot 相同）；
//   - requests 明细只有该 key 的条目；
//   - 完整权限与别的访问密钥的行一次都不出现——归属只在旁挂表里，不能被「顺手」
//     算进筛选结果。
func TestAccessKeyMetricsNarrowsEveryReadingToTheKey(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedAccessKeyMetrics(t, store, moment)

	hours := 1.0
	value, err := store.AccessKeyMetrics(AccessKeyMetricsParams{
		Hours:         &hours,
		BucketSeconds: 60,
		Limit:         10,
		AccessKeyID:   "ak1",
	})
	if err != nil {
		t.Fatalf("AccessKeyMetrics: %v", err)
	}

	if got := fieldOf(t, value, "access_key_id"); got != "ak1" {
		t.Errorf("access_key_id = %q，期望 ak1", got)
	}

	// —— snapshot：与 /metrics 同形，但只算 ak1 在 1 小时窗口内的 3 行 ——
	snapshot, ok := value.Obj.Get("snapshot")
	if !ok {
		t.Fatal("响应缺少 snapshot")
	}
	total, _ := snapshot.Obj.Get("total")
	if got := statRequests(t, total); got != 3 {
		t.Errorf("snapshot.total.requests = %d，期望 3（1 小时窗口内 ak1 的 3 行）", got)
	}
	if got := statInt(t, total, "total_tokens"); got != 180 {
		t.Errorf("snapshot.total.total_tokens = %d，期望 180", got)
	}
	// 六行里只有三行属于 ak1 且落在窗口内：窗口外的历史行、ak2 的行与完整权限的行
	// 都不该让它变大。
	callerTypes, _ := snapshot.Obj.Get("caller_types")
	local, _ := callerTypes.Obj.Get("local")
	if got := statRequests(t, local); got != 0 {
		t.Errorf("caller_types.local.requests = %d，期望 0（完整权限的行没有访问密钥归属）", got)
	}
	accessKeyCaller, _ := callerTypes.Obj.Get("access_key")
	if got := statRequests(t, accessKeyCaller); got != 3 {
		t.Errorf("caller_types.access_key.requests = %d，期望 3", got)
	}
	models, _ := snapshot.Obj.Get("models")
	if keys := models.Obj.Keys(); len(keys) != 2 || keys[0] != "m1" || keys[1] != "m2" {
		t.Errorf("snapshot.models 键 = %v，期望 [m1 m2]（只有这把 key 用过的模型）", keys)
	}
	// rate 窗口（60 秒）只覆盖 T 时刻的两行，且同样只算 ak1。
	if got := statInt(t, snapshot, "current_rpm"); got != 2 {
		t.Errorf("current_rpm = %d，期望 2（只有 T 时刻的 ak1 两行）", got)
	}
	if got := statInt(t, snapshot, "current_tpm"); got != 80 {
		t.Errorf("current_tpm = %d，期望 80", got)
	}

	// —— series：窗口与 snapshot 相同，桶里也只有 ak1 ——
	series, ok := value.Obj.Get("series")
	if !ok {
		t.Fatal("响应缺少 series")
	}
	if got := statInt(t, series, "bucket_seconds"); got != 60 {
		t.Errorf("series.bucket_seconds = %d，期望 60（显式给的值原样回显）", got)
	}
	points, _ := series.Obj.Get("points")
	if len(points.Arr) == 0 {
		t.Fatal("series.points 为空")
	}
	var seriesRequests int64
	for _, point := range points.Arr {
		seriesRequests += statRequests(t, point)
	}
	if seriesRequests != 3 {
		t.Errorf("series 各桶 requests 合计 = %d，期望 3（与 snapshot.total 同一个窗口）", seriesRequests)
	}

	// —— requests：明细只有 ak1，且是 id 倒序 ——
	requests, ok := value.Obj.Get("requests")
	if !ok || requests.Kind != canonical.KindObject {
		t.Fatalf("requests 应为对象，实得 %s", compact(t, requests))
	}
	if got := statInt(t, requests, "total_items"); got != 3 {
		t.Errorf("requests.total_items = %d，期望 3", got)
	}
	items, _ := requests.Obj.Get("items")
	if len(items.Arr) != 3 {
		t.Fatalf("requests.items 条数 = %d，期望 3", len(items.Arr))
	}
	if got := fieldOf(t, items.Arr[0], "model_id"); got != "m2" {
		t.Errorf("明细首条 model_id = %q，期望 m2（按 id 倒序，最后写入的 ak1 行）", got)
	}
	nextBeforeID, _ := requests.Obj.Get("next_before_id")
	if nextBeforeID.Kind != canonical.KindNull {
		t.Errorf("requests.next_before_id = %s，期望 null（没有下一页）", compact(t, nextBeforeID))
	}

	// 不传 limit 时不取明细：requests 为 null（「没要明细」与「要了但为空」要分得开）。
	withoutItems, err := store.AccessKeyMetrics(AccessKeyMetricsParams{
		Hours: &hours, BucketSeconds: 60, AccessKeyID: "ak1",
	})
	if err != nil {
		t.Fatalf("AccessKeyMetrics（不带明细）: %v", err)
	}
	if requests, _ := withoutItems.Obj.Get("requests"); requests.Kind != canonical.KindNull {
		t.Errorf("limit=0 时 requests = %s，期望 null", compact(t, requests))
	}

	// —— 与未过滤的 /metrics 对照：总量必须比它小，证明过滤真的生效 ——
	unfiltered, err := store.Snapshot(&hours, nil)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	unfilteredTotal, _ := unfiltered.Obj.Get("total")
	if got := statRequests(t, unfilteredTotal); got != 5 {
		t.Fatalf("未过滤 total.requests = %d，期望 5（未过滤路径本身不得被改动）", got)
	}
	if got := statInt(t, unfiltered, "current_rpm"); got != 4 {
		t.Errorf("未过滤 current_rpm = %d，期望 4", got)
	}
}

// TestAccessKeyMetricsAllHistoryUsesTheKeysOwnSpan 断言 all_history 时三部分仍同窗口。
//
// series 没有「不限窗口」的形态（点是零填充出来的），因此自动桶要按**这把 key 自己的**
// 最早一条记录折算跨度：用别人的跨度（或全库跨度）会让趋势图开头多出一大段空桶。
func TestAccessKeyMetricsAllHistoryUsesTheKeysOwnSpan(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedAccessKeyMetrics(t, store, moment)

	value, err := store.AccessKeyMetrics(AccessKeyMetricsParams{AccessKeyID: "ak1"})
	if err != nil {
		t.Fatalf("AccessKeyMetrics: %v", err)
	}

	window, _ := value.Obj.Get("window")
	if from, _ := window.Obj.Get("from"); from.Kind != canonical.KindNull {
		t.Errorf("all_history 的 window.from = %s，期望 null", compact(t, from))
	}
	if hours, _ := window.Obj.Get("hours"); hours.Kind != canonical.KindNull {
		t.Errorf("all_history 的 window.hours = %s，期望 null", compact(t, hours))
	}

	snapshot, _ := value.Obj.Get("snapshot")
	total, _ := snapshot.Obj.Get("total")
	if got := statRequests(t, total); got != 4 {
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
	var seriesRequests int64
	for _, point := range points.Arr {
		seriesRequests += statRequests(t, point)
	}
	if seriesRequests != 4 {
		t.Errorf("series 各桶 requests 合计 = %d，期望 4（与 snapshot 同一个窗口）", seriesRequests)
	}
	// 自动桶按这把 key 的跨度（2 小时）挑：全库跨度或别人的跨度都会让桶更大或更小。
	// 这里只锁「点数落在上限内」，具体桶宽由 autoSeriesBucket 的公式决定。
	if len(points.Arr) < 2 {
		t.Errorf("两小时跨度至少应有两个桶，实得 %d", len(points.Arr))
	}
}

// TestAccessKeyMetricsRejectsTooFineBucket 断言显式桶宽超点数上限时报参数错误。
//
// 与 /metrics/series 同一套文本：hours=24 配 60 秒桶是 1441 点。**不**静默换成更粗的
// 桶——调用方以为拿到的是 60 秒的桶，比一句明确的拒绝糟糕得多。
func TestAccessKeyMetricsRejectsTooFineBucket(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)

	hours := 24.0
	_, err := store.AccessKeyMetrics(AccessKeyMetricsParams{
		Hours: &hours, BucketSeconds: 60, AccessKeyID: "ak1",
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
	if _, err := store.AccessKeyMetrics(AccessKeyMetricsParams{
		Hours: &hours, AccessKeyID: "ak1",
	}); err != nil {
		t.Fatalf("自动桶应当可用，实得 %v", err)
	}
}

// TestKeyUsageAccessKeyFilterNarrowsSplits 断言 key-usage 的过滤收窄三份拆分与未归属。
//
// filter 语义是「谁**发起**的这次请求」（request_access_key 旁挂表），因此：
//   - upstream_keys / model_keys 里只剩这把 key 打到过的上游 Key；
//   - access_keys 里只剩它自己；
//   - unattributed 变成「这把 key 里没有上游归属的行」，而不是全库的未归属。
func TestKeyUsageAccessKeyFilterNarrowsSplits(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedAccessKeyMetrics(t, store, moment)

	hours := 1.0
	accessKeyID := "ak1"
	value, err := store.KeyUsage(KeyUsageParams{Hours: &hours, AccessKeyID: &accessKeyID})
	if err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}

	upstream, _ := value.Obj.Get("upstream_keys")
	if len(upstream.Arr) != 1 {
		t.Fatalf("过滤后 upstream_keys 行数 = %d，期望 1（body=%s）", len(upstream.Arr), compact(t, value))
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
		t.Errorf("过滤后 model_keys 行数 = %d，期望 1", len(modelKeys.Arr))
	}

	access, _ := value.Obj.Get("access_keys")
	if len(access.Arr) != 1 {
		t.Fatalf("过滤后 access_keys 行数 = %d，期望 1（别的 key 不该出现）", len(access.Arr))
	}
	if got := statRequests(t, statsAt(t, rowAt(t, access, 0))); got != 3 {
		t.Errorf("过滤后 access_keys[0].requests = %d，期望 3", got)
	}

	unattributed, _ := value.Obj.Get("unattributed")
	if got := statRequests(t, unattributed); got != 1 {
		t.Errorf("过滤后 unattributed.requests = %d，期望 1（ak1 里没有上游归属的那行）", got)
	}
	if got := statInt(t, unattributed, "total_tokens"); got != 30 {
		t.Errorf("过滤后 unattributed.total_tokens = %d，期望 30", got)
	}

	// 不传过滤：三份拆分必须回到全量口径（ak1 + ak2 两把访问密钥、两个上游行）。
	unfiltered, err := store.KeyUsage(KeyUsageParams{Hours: &hours})
	if err != nil {
		t.Fatalf("KeyUsage（未过滤）: %v", err)
	}
	allAccess, _ := unfiltered.Obj.Get("access_keys")
	if len(allAccess.Arr) != 2 {
		t.Fatalf("未过滤 access_keys 行数 = %d，期望 2", len(allAccess.Arr))
	}
	allUpstream, _ := unfiltered.Obj.Get("upstream_keys")
	if got := statRequests(t, statsAt(t, rowAt(t, allUpstream, 0))); got != 4 {
		t.Errorf("未过滤 upstream_keys 首行 requests = %d，期望 4（local 1 + ak1 2 + ak2 1）", got)
	}
	allUnattributed, _ := unfiltered.Obj.Get("unattributed")
	if got := statRequests(t, allUnattributed); got != 1 {
		t.Errorf("未过滤 unattributed.requests = %d，期望 1", got)
	}
}
