package metrics

import (
	"database/sql"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件回答「状态监控」页该看到什么：**渠道健康矩阵**。它把三份必须同窗口的读数
// 拼成一个响应（与 filteredmetrics.go 同一条理由）：
//
//   - snapshot：与 GET /metrics 同形，窗口内这组条件下的总量与九个分组；
//   - series：与 GET /metrics/series 的点**完全同形**的趋势；
//   - matrix：按渠道维度（供应商 / 供应商 + 上游模型 / 供应商 + 上游 Key）分组、
//     按与 series **同一个桶网格**铺开的请求数、成功数、失败数、token 与耗时。
//
// # 为什么还要一条新读数
//
// /metrics 的 providers / upstream_models 是窗口内**各维度各自**的合计，答不出
// 「某一家在某个时间片里健康吗」：合计没问题、但最近 10 分钟全 500 的渠道，在
// 合计里只是成功率略低一格。matrix 把「渠道 × 时间片」摆成一张表，异常因此是
// 看得见的格子；而 /metrics 系列的形状已发布、不能加这种字段（见 filteredmetrics.go
// 与 internal/server/status_monitor.go 的说明）。
//
// # 三份读数共用同一个 now
//
// 服务端一次定下 nowBeijing()，snapshot / series / matrix 都收同一个 now：分成三次
// 请求取会让窗口边界各差一点，界面上的「概览说 12 次、矩阵加起来 11 次」没法归因。
// 同理，matrix 与 series 用**同一个桶网格**（同一个 firstEpoch = bucketEpoch(窗口起点)）：
// 行内每个格子的 requests 逐列相加应当等于 series 对应点位的 requests（在 60 行截断
// 之前成立；series 始终统计全部行，见下）。
//
// # 矩阵为什么只扫一遍 SQL
//
// 一次 GROUP BY「维度列 + 桶」拿到全部格子，再在 Go 侧按格子定位铺开。按行各查一遍
// 是「60 行 × 一条聚合查询」，窗口稍大就慢到不可用；这里与 seriesPoints 的做法一致。
//
// # 60 行上限与 series
//
// 一行一个渠道（供应商 + 上游模型/Key），渠道多时表格会长到没法看，因此只回前 60 行
// （按窗口内总请求数降序），被截断的行数回在 rows_truncated 里让界面能说明「还有 N 行
// 未显示」。**series 不受截断影响**：它统计窗口内全部行（含被截断行与未归属行），
// 否则「趋势图的总量」会随着翻页悄悄变小。
//
// # 未归属行
//
// provider_id 可空（升级前的历史行、以及不走 proxy 的写入路径）。这些行**不丢弃**，
// 而是保留成 provider_id = null 的一行：界面显示「未归属」，否则矩阵加总与
// snapshot.total.requests 对不上，而缺的那一截没有出处。同理，NULL 与空串是两件事，
// 前者渲染成 JSON null，后者是空串。

// StatusMonitorParams 是状态监控页的参数。
//
// Hours 恒有值（缺省 24）：这条读数没有 all_history 形态——矩阵把窗口切成桶，没有
// 起点就没有列。BucketSeconds 为 0 表示由服务端自动选（见 statusAutoBucket）。
type StatusMonitorParams struct {
	Hours         float64
	BucketSeconds int64
	// GroupBy 是矩阵的行身份，取值见 StatusGroupBy* 常量；空串按缺省值
	// StatusGroupByProvider 处理（服务端总归会填上缺省值，存储层不依赖这一点）。
	GroupBy string
	// ProviderIDs / AccessKeyIDs 与 filtered metrics 同一套语义：维度内 OR、维度间
	// AND，都为空 = 全实例读数。三份读数（含 matrix 的行集合）一并收窄。
	ProviderIDs  []string
	AccessKeyIDs []string
}

// 矩阵的分组维度。这三个字符串是**对外契约**（查询参数 group_by 的取值域），
// 服务端 query.go 直接引用这里，避免同一条取值域被抄成两份而漂移。
const (
	// StatusGroupByProvider 只按供应商分组：一张渠道级健康表。
	StatusGroupByProvider = "provider"
	// StatusGroupByProviderModel 按「供应商 + 上游模型」分组：同一家的不同上游模型
	// 可能走不同的限流/配额，健康状况未必一起变。
	StatusGroupByProviderModel = "provider_model"
	// StatusGroupByProviderKey 按「供应商 + 上游 Key」分组：一把 Key 被限流或欠费时，
	// 同一家的其它 Key 通常还是健康的——这一档用来把「渠道问题」与「单把 Key 问题」
	// 分开。
	StatusGroupByProviderKey = "provider_key"
)

// statusBucketLadder 是自动桶的候选梯级（秒）。
//
// 从 30 秒起步而不是 /metrics/series 的下限 15 秒：矩阵一行最多铺 180 格，30 秒桶
// 能覆盖 1.5 小时，再细的桶对健康看板没有信息增量，只会让载荷翻倍。
var statusBucketLadder = [...]int64{30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400}

const (
	// statusMatrixMaxColumns 是自动桶让列数落进的上限。
	//
	// 与 MaxSeriesPoints（500）不同的原因：series 只是 500 个点，matrix 是
	// 「最多 60 行 × 列数」个格子，每个格子还有 10 个字段；180 列时最坏约 1 万个
	// 格子，再大就不是一张能看的表了。
	statusMatrixMaxColumns = 180
	// statusMatrixMaxRows 是矩阵的行数上限（见文件头的说明）。
	statusMatrixMaxRows = 60
	// statusMaxBucketSeconds / statusMaxHours 是防御性上界：它们与服务端 query.go
	// 的 qParam 一致，存储层不假设调用方一定走过了那层校验。
	statusMaxBucketSeconds = 86400
	statusMaxHours         = 8760
)

// StatusMonitor 返回概览、趋势与渠道健康矩阵三份同窗口的读数。
//
// 形状由本项目自己定（参照实现没有状态看板，没有可比对的 oracle）：
//
//	{
//	  count_semantics, window: {from, to, hours},
//	  filters: {provider_ids: [...], access_key_ids: [...]},
//	  group_by, bucket_seconds,
//	  snapshot: {…与 GET /metrics 完全同形…},
//	  series: {bucket_seconds, points: [与 /metrics/series 同形的点]},
//	  matrix: {group_by, rows: [{provider_id, upstream_model_id, key_name, cells: [...]}],
//	           rows_truncated}
//	}
//
// cells 的 10 个字段是刻意精简过的：矩阵最多约 1 万个格子，每个格子带完整
// UsageStats（20 个键）会让载荷膨胀一倍，而第一列 token 缓存率、平均耗时之类都能由
// 界面从这 10 个原始计数推出来。series 的点保持完整形状——那是与 /metrics/series
// 的契约，不能砍。
func (s *Store) StatusMonitor(params StatusMonitorParams) (*canonical.Value, error) {
	// 校验顺序：hours → bucket_seconds → group_by → 点数上限。与 FilteredMetrics
	// 一样只做**防御性**校验：正常请求先过服务端的 Pydantic 口径校验（query.go），
	// 这里再把关一次，避免别的调用方绕过 HTTP 层拿到一份没有意义的读数。
	if params.Hours <= 0 {
		return nil, formatValidationError("hours must be greater than zero")
	}
	if params.Hours > statusMaxHours {
		return nil, formatValidationError("hours must be less than or equal to %d", statusMaxHours)
	}
	if params.BucketSeconds < 0 {
		return nil, formatValidationError("bucket_seconds must be greater than zero")
	}
	if params.BucketSeconds > 0 &&
		(params.BucketSeconds < minSeriesBucketSeconds || params.BucketSeconds > statusMaxBucketSeconds) {
		return nil, formatValidationError(
			"bucket_seconds must be between %d and %d",
			minSeriesBucketSeconds, statusMaxBucketSeconds,
		)
	}
	groupBy := params.GroupBy
	if groupBy == "" {
		// 零值兜底：缺省值是契约的一部分，但存储层不把「调用方忘了填」当成错误。
		groupBy = StatusGroupByProvider
	}
	dimensions, ok := statusDimensions(groupBy)
	if !ok {
		return nil, formatValidationError(
			"group_by must be one of '%s', '%s' or '%s'",
			StatusGroupByProvider, StatusGroupByProviderModel, StatusGroupByProviderKey,
		)
	}

	bucketSeconds := params.BucketSeconds
	if bucketSeconds == 0 {
		bucketSeconds = statusAutoBucket(params.Hours)
	} else if seriesPointLimitExceeded(params.Hours, bucketSeconds) {
		// 显式给了桶宽就照它算，超上限报错而不是静默换一个更粗的桶——与
		// /metrics/series、FilteredMetrics 同一取舍。
		return nil, formatValidationError("time series cannot exceed %d points", MaxSeriesPoints)
	}

	// 统一 now：三份读数都收它，窗口边界因此不可能各差一截。
	now := nowBeijing()
	windowStart := addHours(now, params.Hours)
	// 桶网格的首尾都用 bucketEpoch（向下对齐到桶边界）。seriesPoints 的语义就是
	// 「从第一个桶的起点开始铺」，matrix 必须跟它用同一个 firstEpoch，否则同一列
	// 在两份读数里装的是不同区间的请求。
	firstEpoch := bucketEpoch(windowStart, bucketSeconds)
	lastEpoch := bucketEpoch(now, bucketSeconds)

	snapshot, err := s.snapshotSync(&params.Hours, nil, now, params.ProviderIDs, params.AccessKeyIDs)
	if err != nil {
		return nil, err
	}
	points, err := s.seriesPoints(
		queryFilter{ProviderIDs: params.ProviderIDs, AccessKeyIDs: params.AccessKeyIDs},
		bucketSeconds,
		firstEpoch,
		now,
	)
	if err != nil {
		return nil, err
	}
	groups, err := s.statusMatrixGroups(dimensions, params, firstEpoch, bucketSeconds, now)
	if err != nil {
		return nil, err
	}
	rows, rowsTruncated := statusMatrixValue(groupBy, groups, firstEpoch, lastEpoch, bucketSeconds, now)

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: canonical.NewString(formatISO(windowStart))},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(formatISO(now))},
			canonical.ObjectPair{Key: "hours", Value: canonical.NewFloat(params.Hours)},
		)},
		canonical.ObjectPair{Key: "filters", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "provider_ids", Value: stringArray(params.ProviderIDs)},
			canonical.ObjectPair{Key: "access_key_ids", Value: stringArray(params.AccessKeyIDs)},
		)},
		canonical.ObjectPair{Key: "group_by", Value: canonical.NewString(groupBy)},
		canonical.ObjectPair{Key: "bucket_seconds", Value: canonical.NewIntValue(bucketSeconds)},
		canonical.ObjectPair{Key: "snapshot", Value: snapshot},
		canonical.ObjectPair{Key: "series", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "bucket_seconds", Value: canonical.NewIntValue(bucketSeconds)},
			canonical.ObjectPair{Key: "points", Value: points},
		)},
		canonical.ObjectPair{Key: "matrix", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "group_by", Value: canonical.NewString(groupBy)},
			canonical.ObjectPair{Key: "rows", Value: rows},
			canonical.ObjectPair{Key: "rows_truncated", Value: canonical.NewIntValue(rowsTruncated)},
		)},
	), nil
}

// statusDimensions 把 group_by 映射成 SQL 分组列。
//
// provider_id 恒参与分组：上游模型与 Key 名都只在「同一家供应商内」唯一（两家的同名
// Key 是两把 Key），不带上它会并成一行。第二个返回值表示取值合法；空串已被
// StatusMonitor 兜成缺省值，走到这里为空只可能是调用方传了别的字符串。
func statusDimensions(groupBy string) ([]string, bool) {
	switch groupBy {
	case StatusGroupByProvider:
		return []string{"provider_id"}, true
	case StatusGroupByProviderModel:
		return []string{"provider_id", "upstream_model_id"}, true
	case StatusGroupByProviderKey:
		return []string{"provider_id", "key_name"}, true
	}
	return nil, false
}

// statusRowID 是矩阵一行的身份。
//
// Provider 恒有；Second 按 group_by 取 upstream_model_id 或 key_name（provider 分组时
// 恒为 NULL）。两个字段都用 sql.NullString：NULL 是一个**合法身份**（未归属的渠道行），
// 必须与空串分开——渲染时前者给 JSON null，后者给空串。
type statusRowID struct {
	Provider sql.NullString
	Second   sql.NullString
}

// statusRowIDLess 是行身份的字典序：NULL 排在最前，其后逐字段比较字符串。
//
// 只在窗口内总请求数相同时用作决胜键：同量的两行必须有一个稳定顺序，否则同样一份
// 数据两次请求的行序可能不同，界面（和用例）都没法依赖。
func statusRowIDLess(left, right statusRowID) bool {
	if left.Provider.Valid != right.Provider.Valid {
		return !left.Provider.Valid
	}
	if left.Provider.Valid && left.Provider.String != right.Provider.String {
		return left.Provider.String < right.Provider.String
	}
	if left.Second.Valid != right.Second.Valid {
		return !left.Second.Valid
	}
	return left.Second.Valid && left.Second.String < right.Second.String
}

// statusMatrixGroup 是矩阵的一行：身份 + 每个桶的统计。
//
// Cells 以桶起点（unix 秒）为键：SQL 按「维度 + 桶」分组，行内桶天然稀疏，用 map
// 收下再在渲染时按网格零填充。Requests 是该行窗口内的总请求数，行序与 60 行截断
// 都按它排。
type statusMatrixGroup struct {
	ID       statusRowID
	Cells    map[int64]*UsageStats
	Requests int64
}

// statusMatrixGroups 用一次 SQL 扫描取出矩阵的全部格子，并按行聚好。
//
// 与 seriesPoints 一样：分桶表达式放进 SELECT、窗口与筛选条件放进 WHERE，参数顺序
// （先分桶的 4 个锚点/宽度参数，再窗口参数，最后两个筛选维度）与 seriesPoints 逐字
// 相同——两份读数因此落在同一个桶网格上。GROUP BY 用别名 bucket_epoch（SQLite 允许
// 在 GROUP BY 里引用结果别名，seriesPoints 同样这么写）。
func (s *Store) statusMatrixGroups(
	dimensions []string,
	params StatusMonitorParams,
	firstEpoch, bucketSeconds int64,
	now time.Time,
) ([]*statusMatrixGroup, error) {
	// 窗口下界取**桶起点**而不是窗口起点：与 seriesPoints 完全一致（首桶向前对齐到
	// 桶边界，最多多算不到一个桶的历史行）。矩阵格子与 series 点位因此能逐列相加。
	startStr := formatISO(fromUnix(firstEpoch))
	untilStr := formatISO(now)
	whereSQL, parameters := metricFilter{
		SinceCreatedAt: &startStr,
		UntilCreatedAt: &untilStr,
		ProviderIDs:    params.ProviderIDs,
		AccessKeyIDs:   params.AccessKeyIDs,
	}.sql()

	columnSQL := strings.Join(dimensions, ", ")
	bucketParams := []any{
		int64(BucketAnchorEpoch),
		bucketSeconds,
		bucketSeconds,
		int64(BucketAnchorEpoch),
	}
	query := "SELECT " + columnSQL + ", " + bucketSQL + " AS bucket_epoch, " + usageAggregates +
		" FROM request_metrics" + whereSQL +
		" GROUP BY " + columnSQL + ", bucket_epoch"
	rows, err := s.db().Query(query, append(append([]any{}, bucketParams...), parameters...)...)
	if err != nil {
		return nil, err
	}

	groups := map[statusRowID]*statusMatrixGroup{}
	for rows.Next() {
		var providerID, second sql.NullString
		dests := []any{&providerID}
		if len(dimensions) > 1 {
			dests = append(dests, &second)
		}
		var epoch sql.NullInt64
		dests = append(dests, &epoch)
		var scan statsScan
		dests = append(dests, scan.dests()...)
		if err := rows.Scan(dests...); err != nil {
			rows.Close()
			return nil, err
		}
		if !epoch.Valid {
			// created_at 不是合法日期时 strftime 返回 NULL：与 seriesPoints 一样整行丢弃。
			continue
		}
		// 桶网格由 bucketEpoch 产出，SQL 的桶起点必然对齐；真出现不对齐说明 Go 侧与
		// SQL 的锚点不一致（那就是 bug），这里丢弃而不是写出一个错位的格子。
		index := (epoch.Int64 - firstEpoch) / bucketSeconds
		if epoch.Int64 < firstEpoch || epoch.Int64-firstEpoch != index*bucketSeconds {
			continue
		}
		id := statusRowID{Provider: providerID, Second: second}
		group, exists := groups[id]
		if !exists {
			group = &statusMatrixGroup{ID: id, Cells: map[int64]*UsageStats{}}
			groups[id] = group
		}
		stats := scan.stats()
		group.Cells[epoch.Int64] = stats
		group.Requests += stats.Requests
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	sorted := make([]*statusMatrixGroup, 0, len(groups))
	for _, group := range groups {
		sorted = append(sorted, group)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Requests != sorted[j].Requests {
			// 请求多的渠道排前面：健康看板先看流量大的渠道。
			return sorted[i].Requests > sorted[j].Requests
		}
		return statusRowIDLess(sorted[i].ID, sorted[j].ID)
	})
	return sorted, nil
}

// statusMatrixValue 把行聚好的矩阵渲染成响应里的 matrix.rows 与 rows_truncated。
//
// 截断只作用在**渲染**上：入参 groups 里的每一行都已经统计完毕，series 那边也不看
// 这份结果（它在 StatusMonitor 里独立取数），所以被截掉的行仍计入趋势。
func statusMatrixValue(
	groupBy string,
	sorted []*statusMatrixGroup,
	firstEpoch, lastEpoch, bucketSeconds int64,
	now time.Time,
) (*canonical.Value, int64) {
	rowsTruncated := int64(0)
	if len(sorted) > statusMatrixMaxRows {
		rowsTruncated = int64(len(sorted) - statusMatrixMaxRows)
		sorted = sorted[:statusMatrixMaxRows]
	}
	rows := canonical.NewArray()
	for _, group := range sorted {
		rows.Arr = append(rows.Arr, statusMatrixRowValue(groupBy, group, firstEpoch, lastEpoch, bucketSeconds, now))
	}
	return rows, rowsTruncated
}

// statusMatrixRowValue 渲染矩阵的一行。
//
// 身份三键**恒在**，不属于当前 group_by 的给 null（不是缺键）：读端按固定键取数，
// 少一个键就得为三种分组各写一次判空。cells 与 series 的网格逐列对齐。
func statusMatrixRowValue(
	groupBy string,
	group *statusMatrixGroup,
	firstEpoch, lastEpoch, bucketSeconds int64,
	now time.Time,
) *canonical.Value {
	pairs := []canonical.ObjectPair{
		{Key: "provider_id", Value: nullableString(group.ID.Provider)},
	}
	switch groupBy {
	case StatusGroupByProviderModel:
		pairs = append(pairs, canonical.ObjectPair{Key: "upstream_model_id", Value: nullableString(group.ID.Second)})
		pairs = append(pairs, canonical.ObjectPair{Key: "key_name", Value: canonical.NewNull()})
	case StatusGroupByProviderKey:
		pairs = append(pairs, canonical.ObjectPair{Key: "upstream_model_id", Value: canonical.NewNull()})
		pairs = append(pairs, canonical.ObjectPair{Key: "key_name", Value: nullableString(group.ID.Second)})
	default:
		pairs = append(pairs, canonical.ObjectPair{Key: "upstream_model_id", Value: canonical.NewNull()})
		pairs = append(pairs, canonical.ObjectPair{Key: "key_name", Value: canonical.NewNull()})
	}

	cells := canonical.NewArray()
	for epoch := firstEpoch; epoch <= lastEpoch; epoch += bucketSeconds {
		cells.Arr = append(cells.Arr, statusCellValue(group.Cells[epoch], epoch, bucketSeconds, now))
	}
	// cells 排在身份三键之后：一行先说明「是谁」，再看时间片。
	return canonical.NewObjectOf(append(pairs, canonical.ObjectPair{Key: "cells", Value: cells})...)
}

// statusCellValue 渲染矩阵的一个格子。
//
// 只给 10 个字段（见 StatusMonitor 的说明）：requests / successes / failures 直接取
// 聚合，prompt_tokens / cached_tokens / total_first_token_ms / total_duration_ms 是
// 「分子」，成功率、缓存率与平均耗时都可由界面除出来——这里再给平均/比率只是把同一
// 个数字算两遍，还多一份舍入口径要维护。
//
// 空桶给零值统计而不是省略这个格子：cells 是**定长**的时间轴，少一格会让界面把后面
// 所有格子错位一列。
func statusCellValue(stats *UsageStats, epoch, bucketSeconds int64, now time.Time) *canonical.Value {
	if stats == nil {
		stats = &UsageStats{}
	}
	startedAt := fromUnix(epoch)
	endedAt := startedAt.Add(time.Duration(bucketSeconds) * time.Second)
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "started_at", Value: canonical.NewString(formatISO(startedAt))},
		canonical.ObjectPair{Key: "ended_at", Value: canonical.NewString(formatISO(endedAt))},
		// 尾部那个桶的结束时间在 now 之后，因此 complete=false：界面据此区分
		// 「这一格确实是 0」与「这一格还没走完」。
		canonical.ObjectPair{Key: "complete", Value: canonical.NewBool(!endedAt.After(now))},
		canonical.ObjectPair{Key: "requests", Value: canonical.NewIntValue(stats.Requests)},
		canonical.ObjectPair{Key: "successes", Value: canonical.NewIntValue(stats.Successes)},
		canonical.ObjectPair{Key: "failures", Value: canonical.NewIntValue(stats.Failures)},
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(stats.PromptTokens)},
		canonical.ObjectPair{Key: "cached_tokens", Value: canonical.NewIntValue(stats.CachedTokens)},
		canonical.ObjectPair{Key: "total_first_token_ms", Value: canonical.NewIntValue(stats.TotalFirstTokenMS)},
		canonical.ObjectPair{Key: "total_duration_ms", Value: canonical.NewIntValue(stats.TotalDurationMS)},
	)
}

// statusAutoBucket 按窗口跨度从梯级里挑一个桶宽，让矩阵列数不超过 statusMatrixMaxColumns。
//
// 为什么自动：调用方为每个时间档各算一次桶宽没有意义，而 24 小时配 60 秒桶是 1441 列，
// 远超矩阵上限。规则是「梯级中**最小**的、使 ceil(hours*3600/bucket)+1 ≤ 180 的那个」，
// 因此常规窗口都落在 30 秒 ~ 1 天这些好读的档位上。
//
// 梯级顶端也不够时（hours 超过约 4296 小时，一天一桶仍超过 180 列）按整天继续放大：
// 列数上限是硬约束，梯级只是「优先用常用档位」的偏好；一年窗口若生成 366 列 × 最多
// 60 行的载荷，比换成 3 天桶要糟得多。此时桶宽不是梯级里的值，响应会如实回显它。
func statusAutoBucket(hours float64) int64 {
	spanSeconds := hours * 3600
	for _, bucketSeconds := range statusBucketLadder {
		if statusMatrixColumns(spanSeconds, bucketSeconds) <= statusMatrixMaxColumns {
			return bucketSeconds
		}
	}
	largest := statusBucketLadder[len(statusBucketLadder)-1]
	bucketSeconds := largest
	for statusMatrixColumns(spanSeconds, bucketSeconds) > statusMatrixMaxColumns {
		bucketSeconds += largest
	}
	return bucketSeconds
}

// statusMatrixColumns 是契约里的列数公式：ceil(窗口秒数/桶宽)+1。
//
// 逐字的浮点运算顺序必须保持（先乘 3600 再除，与 seriesPointLimitExceeded 同一口径）。
// 记住这只是**上界**：桶向下对齐后实际列数可能少一列（窗口起点落在桶中间时），
// 自动桶因此不会因为对齐而越过 180。
func statusMatrixColumns(spanSeconds float64, bucketSeconds int64) int64 {
	return int64(math.Ceil(spanSeconds/float64(bucketSeconds))) + 1
}
