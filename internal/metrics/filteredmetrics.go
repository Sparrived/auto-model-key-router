package metrics

import (
	"database/sql"
	"math"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件回答「**按一组维度筛选后**这一页该看到什么」：概览读数（snapshot）、
// 趋势（series）与最近明细（requests）三份同窗口的读数拼成一个响应（**有意增补**：参照
// 实现既没有供应商筛选也没有访问密钥，没有可比对的 oracle）。
//
// # 为什么合成一个端点
//
// 三份读数必须落在**同一个时间窗口**上。分成三次请求取，界面上的「概览说 12 次、
// 趋势图加起来 11 次」就没法归因：窗口边界各差一点、或者两次请求之间又落进来一行。
// 这里由服务端一次定下 now 与窗口起点，三份取数共用（snapshotSync / seriesPoints /
// requestHistorySync 都收同一个 now）。
//
// # 为什么挂在 /ui/ 而不是给 /metrics 加参数
//
// /metrics、/metrics/series、/metrics/requests 是**对照参照实现**的读数，响应形状与
// 查询参数都已经发布：给它们加一个 provider_id / access_key_id 会改掉契约（那三条路由
// 的 filters 回显也是已发布字节）。因此过滤能力只对本项目自有的读数开放，单独一条 /ui
// 路由，与 keyusage.go、accesskey.go 同一条理由。
//
// # 维度的语义
//
// 维度最早只有供应商与访问密钥两个，现在是 Dimensions 的七个（供应商、访问密钥、模型、
// 上游 Key、工作空间、调用方档位、成败，见 dimensions.go）。**维度内多选是 OR、维度间是
// AND**：同一参数重复出现收成 `IN (?, …)`，多个维度同时出现则是几组条件同时成立
// （`provider_id IN (?) AND id IN (SELECT …)`）。
//
// 全部维度都不给 = 全实例读数（与 /metrics 系列等价）。这条路径**保留**着，不是为了给
// 界面用（界面不会这么发），而是因为「不筛」与「筛一个不存在的值」必须是两件不同的事：
// 前者该回全量，后者该回空。
//
// # 与 /ui/key-usage.json、/ui/access-key-usage.json 的分工
//
//   - key-usage.json 回答「这些流量是哪把 Key 出去的」（按上游 Key 与调用方凭据拆分）；
//   - access-key-usage.json 是**访客看板**：只认访问密钥凭据，只回它自己那把；
//   - 这里回答「管理面按这组条件筛选后，整页读数长什么样」，因此认**完整权限**，
//     筛选值由参数指定（管理面本来就能看到全实例的读数）。
//
// 三者读同一个指标库、共用同一套窗口语义，但回答的问题不同，因此不合并。

// maxFilteredMetricsItems 是 requests 明细的条数上限，与 request_history 的 limit
// 上限一致（见 RequestHistory 的校验）。
const maxFilteredMetricsItems = 200

// minSeriesBucketSeconds 是自动桶的下限，与 /metrics/series 的 bucket_seconds 下界一致。
const minSeriesBucketSeconds = 15

// FilteredMetricsParams 是「按一组维度筛选的整页读数」的参数。
type FilteredMetricsParams struct {
	// Hours 为 nil 表示不设时间窗口（对应 /metrics 的 all_history）。
	Hours *float64
	// BucketSeconds 是 series 的桶宽；0 = 由服务端按窗口跨度自动选（见 autoSeriesBucket）。
	BucketSeconds int64
	// Limit 是 requests 明细的条数；0 表示不要明细（响应里 requests 为 null），
	// 大于 0 时沿用 request_history 的 limit 语义（1..200）。
	Limit int64
	// Dimensions 是筛选维度：每个维度内部是「或」的一组取值，维度之间是「与」。全部为空
	// = 不筛（全实例读数），此时 SQL 与没有这些维度时一致（filters 回显全是空数组与 null）。
	//
	// 访问密钥 / 工作空间的归属读自旁挂表：没有归属的行（完整权限、历史行等）不会被任何
	// 一把 key、任何一个空间算进去（与 accesskey.go、workspace.go 同一口径）。
	Dimensions
}

// FilteredMetrics 返回这组筛选条件下的概览、趋势与（可选的）最近明细。
//
// 形状由本项目自己定：
//
//	{
//	  count_semantics, window: {from, to, hours},
//	  filters: {provider_ids, access_key_ids, model_ids, upstream_keys: [{provider_id, key_name}],
//	            workspaces, caller_types, success},
//	  snapshot: {…与 GET /metrics 完全同形，但全部分组/总量/rate 窗口都收窄到这些条件…},
//	  series: {bucket_seconds, points: [与 /metrics/series 同形的点]},
//	  requests: null | {items: [与 /metrics/requests 同形的条目],
//	                    next_before_id, total_items}
//	}
//
// filters 回显**筛选值本身**（见 dimensionsEcho：未筛的列表维度是空数组，未筛的 success
// 是 null）：界面据此回显当前条件，不必把 URL 再解析一遍。
//
// snapshot 直接复用 snapshotSync 的整份响应（含 started_at / database_path /
// active_requests）：同形比精简更值钱——界面渲染概览的那套代码可以原样复用。
// 唯一的例外是 active_requests，它是进程内的全局在途数，按定义无法按筛选条件拆分
// （见 snapshotSync 的说明）。
func (s *Store) FilteredMetrics(params FilteredMetricsParams) (*canonical.Value, error) {
	// 校验顺序与 RequestHistory / TimeSeries 一致：hours → bucket_seconds → limit。
	if params.Hours != nil && *params.Hours <= 0 {
		// 与 TimeSeries / KeyUsage / AccessKeyUsage 同一套口径与文案。
		return nil, formatValidationError("hours must be greater than zero")
	}
	if params.BucketSeconds < 0 {
		// 0 是「自动」，负数是非法输入。上界由服务端参数校验把关（86400），
		// 自动桶可能超出它——那是服务端自己选的，不是调用方要求的。
		return nil, formatValidationError("bucket_seconds must be greater than zero")
	}
	if params.Limit < 0 || params.Limit > maxFilteredMetricsItems {
		return nil, formatValidationError("limit must be between 0 and %d", maxFilteredMetricsItems)
	}

	now := nowBeijing()

	var since *string
	if params.Hours != nil {
		value := formatISO(addHours(now, *params.Hours))
		since = &value
	}

	// 三份读数共用同一个 now：snapshotSync 与 requestHistorySync 都从这里拿到同一个
	// 封口（created_at <= now），series 的最后一个桶也以它收尾。
	snapshot, err := s.snapshotSync(params.Hours, nil, now, params.Dimensions)
	if err != nil {
		return nil, err
	}
	bucketSeconds, points, err := s.filteredSeries(params, now)
	if err != nil {
		return nil, err
	}

	// 没要明细时回 null 而不是空对象：界面据此区分「这次没要明细」与「要了但一条都
	// 没有」，前者不该画出一张空表。
	requests := canonical.NewNull()
	if params.Limit > 0 {
		history, err := s.requestHistorySync(RequestHistoryParams{
			Hours:      params.Hours,
			Dimensions: params.Dimensions,
			Limit:      params.Limit,
		}, now)
		if err != nil {
			return nil, err
		}
		// 只取这三项：summary / latest_request_at / rate 那几项在这份读数里由 snapshot
		// 承担，重复一遍只会让同一个数字出现在两个地方。
		requests = canonical.NewObjectOf(
			canonical.ObjectPair{Key: "items", Value: objectField(history, "items")},
			canonical.ObjectPair{Key: "next_before_id", Value: objectField(history, "next_before_id")},
			canonical.ObjectPair{Key: "total_items", Value: objectField(history, "total_items")},
		)
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: nullableStringValue(since)},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(formatISO(now))},
			canonical.ObjectPair{Key: "hours", Value: hoursValue(params.Hours)},
		)},
		canonical.ObjectPair{Key: "filters", Value: dimensionsEcho(params.Dimensions)},
		canonical.ObjectPair{Key: "snapshot", Value: snapshot},
		canonical.ObjectPair{Key: "series", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "bucket_seconds", Value: canonical.NewIntValue(bucketSeconds)},
			canonical.ObjectPair{Key: "points", Value: points},
		)},
		canonical.ObjectPair{Key: "requests", Value: requests},
	), nil
}

// stringArray 把筛选值渲染成 JSON 数组；空维度是空数组而不是 null。
//
// 界面据这些数组回显当前筛选条件，空维度必须是 `[]`（「这个维度不设限」）：给 null
// 会让读端在两处各写一次判空，而且「没筛」与「筛了空列表」在响应里也就分不开了。
func stringArray(values []string) *canonical.Value {
	items := make([]*canonical.Value, 0, len(values))
	for _, value := range values {
		items = append(items, canonical.NewString(value))
	}
	return canonical.NewArray(items...)
}

// objectField 取对象里的一个字段。
//
// 只用于本文件里**由本包自己构造**的响应：RequestHistory 恒会写出这三个键，缺一个就是
// 装配错误，给 nil 会让序列化在下游炸掉而不是在这里说清。因此缺键时返回 null——至少
// 响应是完整的，缺的那个字段一眼就能看出来。
func objectField(object *canonical.Value, key string) *canonical.Value {
	if value, ok := object.Obj.Get(key); ok {
		return value
	}
	return canonical.NewNull()
}

// filteredSeries 取 series 部分的桶宽与点数组，窗口与 snapshot 相同（同一个 now）。
//
// 桶网格由 seriesGrid 定下（UsageBreakdown 用的是同一个函数，两份读数的桶因此逐个对齐）；
// 这里只负责「没有网格时给空数组」与取点。
func (s *Store) filteredSeries(params FilteredMetricsParams, now time.Time) (int64, *canonical.Value, error) {
	bucketSeconds, firstEpoch, found, err := s.seriesGrid(params.Hours, params.BucketSeconds, params.Dimensions, now)
	if err != nil {
		return 0, nil, err
	}
	if !found {
		// 这组条件一条记录都没有：空窗口是合法状态，给空数组而不是报错——与
		// snapshot / requests 在空库上的行为一致。
		return bucketSeconds, canonical.NewArray(), nil
	}
	points, err := s.seriesPoints(queryFilter{Dimensions: params.Dimensions}, bucketSeconds, firstEpoch, now)
	if err != nil {
		return 0, nil, err
	}
	return bucketSeconds, points, nil
}

// seriesGrid 定下 /ui 趋势读数的桶网格：桶宽与首桶起点（已按桶对齐）。
//
// hours 给了就用 hours；all_history（hours 为 nil）时用**这组条件自己的**最早一条记录
// 折算跨度——series 没有「不限窗口」的形态（点是从起点零填充出来的，必须有起点），而一组
// 筛选结果有多长只有库里知道。第三个返回值为 false 表示 all_history 下这组条件一条记录都
// 没有：此时没有网格，桶宽回调用方给的值（自动时回下限 15 秒），调用方给空序列。
//
// requested 为 0 表示自动（autoSeriesBucket）；大于 0 时照它算，点数超上限报
// ValidationError 而不是静默换一个更粗的桶。FilteredMetrics 与 UsageBreakdown 都走这里：
// 「同一组参数得到同一张网格」只有共用一处实现才成立。
func (s *Store) seriesGrid(
	hours *float64,
	requested int64,
	dims Dimensions,
	now time.Time,
) (bucketSeconds, firstEpoch int64, found bool, err error) {
	var start time.Time
	// 点数上限的判据与 TimeSeries 相同（ceil(跨度/桶宽) + 1 > 上限），只是 hours 分支
	// 直接用调用方给的 hours、all_history 分支用数据里真实的跨度。
	var spanSeconds float64
	if hours != nil {
		start = addHours(now, *hours)
		spanSeconds = *hours * 3600
	} else {
		earliest, exists, err := s.firstFilteredSecond(dims, now)
		if err != nil {
			return 0, 0, false, err
		}
		if !exists {
			bucketSeconds = requested
			if bucketSeconds == 0 {
				bucketSeconds = minSeriesBucketSeconds
			}
			return bucketSeconds, 0, false, nil
		}
		start = fromUnix(earliest)
		spanSeconds = float64(unixSecondsTruncated(now) - earliest)
	}

	bucketSeconds = requested
	if bucketSeconds == 0 {
		bucketSeconds = autoSeriesBucket(spanSeconds)
	} else {
		// hours 分支直接用调用方给的值（别用 spanSeconds/3600 回转，浮点往返会在
		// 边界上差一个点）；all_history 分支没有 hours 可用，用数据真实跨度折算。
		spanHours := spanSeconds / 3600
		if hours != nil {
			spanHours = *hours
		}
		if seriesPointLimitExceeded(spanHours, bucketSeconds) {
			// 显式给了桶宽就照它算，超上限报错而不是静默换一个更粗的桶：调用方以为拿到
			// 了 60 秒的桶、实际拿到 15 分钟的，比一句明确的拒绝糟糕得多（与
			// /metrics/series 同一取舍）。
			return 0, 0, false, formatValidationError("time series cannot exceed %d points", MaxSeriesPoints)
		}
	}
	return bucketSeconds, bucketEpoch(start, bucketSeconds), true, nil
}

// firstFilteredSecond 取这组筛选条件在窗口内最早一条记录的 unix 秒。
//
// 用 SQLite 的 strftime('%s') 而不是把 created_at 文本解析回 time.Time：分桶表达式
// 本来就是这么算的（见 bucketSQL），两边必须用同一套秒定义，否则桶边界会位移。这里
// 复用 metricFilter 拼条件，省得把各维度的条件再抄一遍。
func (s *Store) firstFilteredSecond(dims Dimensions, until time.Time) (int64, bool, error) {
	untilStr := formatISO(until)
	whereSQL, parameters := metricFilter{
		UntilCreatedAt: &untilStr,
		Dimensions:     dims,
	}.sql()
	var epoch sql.NullInt64
	err := s.db().QueryRow(
		"SELECT MIN(CAST(strftime('%s', created_at) AS INTEGER)) FROM request_metrics"+whereSQL,
		parameters...,
	).Scan(&epoch)
	if err != nil {
		return 0, false, err
	}
	if !epoch.Valid {
		// 窗口内一条记录都没有：MIN() 是 NULL。
		return 0, false, nil
	}
	return epoch.Int64, true, nil
}

// autoSeriesBucket 按窗口跨度选一个桶宽，使点数落进 MaxSeriesPoints。
//
// 为什么要自动：/metrics/series 要求调用方自己按跨度挑桶——hours=24 配默认的 60 秒桶
// 是 1441 点，直接被 500 的上限拒掉。整页读数把概览、趋势与明细拼在一起，再让调用方
// 为每个时间档各算一次桶宽没有意义，因此默认（bucket_seconds=0）由服务端挑：最小的
// 15 秒整数倍，刚好装得下这个跨度。
//
// 分母取 MaxSeriesPoints-2 而不是 -1：桶对齐会把窗口起点向前挪不到一个桶，极端情况下
// 会多出一个点；留一点余量后 ceil(跨度/桶) + 1 仍然小于上限，而向上取整到 15 秒的倍数
// 只会让桶更宽、点数更少。跨度极大时桶会超过 86400 秒——86400 那道上界只约束调用方
// 显式传给 /metrics/series 的值，服务端自动选的桶不受它限制（响应里回真实桶宽）。
func autoSeriesBucket(spanSeconds float64) int64 {
	if spanSeconds <= 0 {
		return minSeriesBucketSeconds
	}
	bucket := int64(math.Ceil(spanSeconds / float64(MaxSeriesPoints-2)))
	bucket = (bucket + minSeriesBucketSeconds - 1) / minSeriesBucketSeconds * minSeriesBucketSeconds
	if bucket < minSeriesBucketSeconds {
		bucket = minSeriesBucketSeconds
	}
	return bucket
}
