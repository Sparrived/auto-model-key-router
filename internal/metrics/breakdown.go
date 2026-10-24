package metrics

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件回答「这段时间的用量按某个维度怎么拆、每一份随时间怎么变」：**按一个维度分组的
// 趋势**——取前 N 组各自一条序列，其余并成 other（**有意增补**：参照实现没有，没有可
// 比对的 oracle）。
//
// # 为什么要一条新读数
//
// 用量统计页原先靠「很多张排行卡片」回答「谁用得多」：每张卡片是一个维度在整个窗口里
// 的合计，看不出「谁在什么时候涨起来的」。这里一次给出「维度 × 时间桶」的完整网格，
// 界面据此画堆叠面积图与每行的迷你折线，一张图替掉一排卡片。
//
// 它也修掉一个真实缺口：snapshot 的 upstream_models / providers 分组要求三个归属列
// （provider_id、pool_name、upstream_model_id）全非空——那是参照实现的口径，而 v4 配置
// 下写入的 pool_name 恒为 NULL，于是这两个分组在新数据上**永远是空的**，按上游模型算
// 成本无从下手。这里的 upstream_model 按 (provider_id, upstream_model_id) 分组，不看
// pool_name。snapshot 那两个分组是已发布形状，口径不能改，所以修在这条新读数上。
//
// # 与 /ui/filtered-metrics.json 的关系
//
// 同一套筛选维度（Dimensions）、同一套 hours / all_history / bucket_seconds 语义、
// **同一张桶网格**（seriesGrid + seriesBuckets）：同一组参数下，这里全部分组（含 other）
// 的序列逐桶相加，等于 filtered-metrics 的 series 对应点位。分开取两次而不是在那条读数
// 上加字段，是因为两者的载荷量级不同——filtered-metrics 每次刷新都取，这条只在打开
// 拆分图时取。
//
// # 统计口径
//
// 取数只有一次 SQL：GROUP BY「分组列 + 桶 + status_code」，再在 Go 侧折成每组的总量
// （stats）与每组每桶的格子（series）。status_code 进分组键是为了让 stats 带出
// status_codes 分布而不必再扫一遍（与 rollup.go 同一个技巧）。
//
// stats 与 series 都覆盖**桶网格**（从 buckets[0].started_at 到 window.to），而不是
// [window.from, window.to]：首桶向前对齐到桶边界，最多多算不到一个桶的历史行——与
// filtered-metrics 的 series 完全同一口径。于是每组的 stats.requests 恒等于它
// series.requests 的逐桶合计，界面拿哪一个都对得上。

// 分组维度的取值。这些字符串是**对外契约**（查询参数 group_by 的取值域），服务端
// query.go 直接引用 UsageBreakdownGroupByValues，避免同一条取值域被抄成两份而漂移。
const (
	// UsageBreakdownGroupByModel 按解析后的本地路由模型（model_id）分组。
	UsageBreakdownGroupByModel = "model"
	// UsageBreakdownGroupByRequestedModel 按调用方请求里写的模型名（requested_model_id）分组。
	UsageBreakdownGroupByRequestedModel = "requested_model"
	// UsageBreakdownGroupByProvider 按供应商分组；NULL（历史行）是一组「未归属」。
	UsageBreakdownGroupByProvider = "provider"
	// UsageBreakdownGroupByUpstreamModel 按 (供应商, 上游模型) 分组，不要求 pool_name。
	UsageBreakdownGroupByUpstreamModel = "upstream_model"
	// UsageBreakdownGroupByUpstreamKey 按 (供应商, 上游 Key 名) 分组。
	UsageBreakdownGroupByUpstreamKey = "upstream_key"
	// UsageBreakdownGroupByAccessKey 按发起请求的访问密钥分组；NULL = 不是访问密钥发起的。
	UsageBreakdownGroupByAccessKey = "access_key"
	// UsageBreakdownGroupByWorkspace 按工作空间归属分组；NULL = 没有归属。
	UsageBreakdownGroupByWorkspace = "workspace"
	// UsageBreakdownGroupByCallerType 按调用方档位分组。
	UsageBreakdownGroupByCallerType = "caller_type"
	// UsageBreakdownGroupByStatus 按上游状态码分组；NULL = 没拿到上游响应。
	UsageBreakdownGroupByStatus = "status"
	// UsageBreakdownGroupByAPIFormat 按入站 API 路径分组；NULL = 没有形态记录。
	UsageBreakdownGroupByAPIFormat = "api_format"

	// UsageBreakdownRankByRequests 按请求数排名（缺省）。
	UsageBreakdownRankByRequests = "requests"
	// UsageBreakdownRankByTokens 按 total_tokens 排名。
	UsageBreakdownRankByTokens = "tokens"
)

// UsageBreakdownGroupByValues 是 group_by 的取值域，顺序即契约里列出的顺序。
var UsageBreakdownGroupByValues = []string{
	UsageBreakdownGroupByModel,
	UsageBreakdownGroupByRequestedModel,
	UsageBreakdownGroupByProvider,
	UsageBreakdownGroupByUpstreamModel,
	UsageBreakdownGroupByUpstreamKey,
	UsageBreakdownGroupByAccessKey,
	UsageBreakdownGroupByWorkspace,
	UsageBreakdownGroupByCallerType,
	UsageBreakdownGroupByStatus,
	UsageBreakdownGroupByAPIFormat,
}

// UsageBreakdownRankByValues 是 rank_by 的取值域。
var UsageBreakdownRankByValues = []string{
	UsageBreakdownRankByRequests,
	UsageBreakdownRankByTokens,
}

const (
	// usageBreakdownDefaultLimit / usageBreakdownMaxLimit 是保留的分组数缺省值与上限。
	//
	// 上限 50：每组带 10 条与桶数等长的序列，500 个桶 × 50 组已经是 25 万个数；再多就不是
	// 一张能看的堆叠图了，界面也分不出 50 种以上的颜色。
	usageBreakdownDefaultLimit = 8
	usageBreakdownMaxLimit     = 50
)

// UsageBreakdownParams 是按维度拆分的趋势读数的参数。
type UsageBreakdownParams struct {
	// Hours 为 nil 表示 all_history：桶网格的起点取这组条件自己最早的一条记录（与
	// FilteredMetrics 同一语义，见 seriesGrid）。
	Hours *float64
	// BucketSeconds 为 0 = 自动（与 FilteredMetrics 同一套 autoSeriesBucket）。
	BucketSeconds int64
	// GroupBy 取 UsageBreakdownGroupBy* 之一；空串按缺省 model 处理。
	GroupBy string
	// RankBy 取 UsageBreakdownRankBy* 之一；空串按缺省 requests 处理。
	RankBy string
	// Limit 是保留的分组数（1..50），其余并进 other；0 按缺省 8 处理。
	Limit int64
	// Dimensions 与 filtered metrics 同一套筛选语义（见 dimensions.go）。
	Dimensions
}

// breakdownColumn 是一个分组列：SQL 表达式与它在响应 key 对象里的字段名。
type breakdownColumn struct {
	expr  string
	field string
}

// breakdownSpec 描述一种 group_by 怎样落成 SQL 与 key 对象。
type breakdownSpec struct {
	// join 是需要的旁挂表 LEFT JOIN（没有则为空）。LEFT 而不是 INNER：没有归属的行
	// 必须照常计入，自成 NULL 那一组——否则各组加起来对不上 filtered-metrics 的总量，
	// 缺的那一截没有出处。
	join string
	// columns 最多两列（dimKey 同一个上限）：上游模型与上游 Key 都只在同一个供应商内唯一，
	// 必须带上 provider_id 才是一个身份。
	columns []breakdownColumn
	// integer 表示分组列是整数（status_code）：key 渲染成 JSON 整数，排序按数值比较。
	integer bool
	// nameField 非空时在 key 对象末尾放一个 null 占位，由服务端按配置原位补上显示名
	// （只有 access_key 用：本包不读配置）。
	nameField string
}

// usageBreakdownSpec 把 group_by 映射成分组规格；第二个返回值表示取值合法。
//
// 旁挂表的别名与 RequestHistory 的明细查询一致（w / sh / ak）。外层主表不起别名：
// metricFilter 生成的 WHERE 用裸列名，而旁挂表只有 request_id 与各自的列，裸列名
// （created_at、id、status_code……）不会产生歧义——RequestHistory 靠的也是这一点。
func usageBreakdownSpec(groupBy string) (breakdownSpec, bool) {
	switch groupBy {
	case UsageBreakdownGroupByModel:
		return breakdownSpec{columns: []breakdownColumn{{"model_id", "model_id"}}}, true
	case UsageBreakdownGroupByRequestedModel:
		return breakdownSpec{columns: []breakdownColumn{{"requested_model_id", "requested_model_id"}}}, true
	case UsageBreakdownGroupByProvider:
		return breakdownSpec{columns: []breakdownColumn{{"provider_id", "provider_id"}}}, true
	case UsageBreakdownGroupByUpstreamModel:
		return breakdownSpec{columns: []breakdownColumn{
			{"provider_id", "provider_id"}, {"upstream_model_id", "upstream_model_id"},
		}}, true
	case UsageBreakdownGroupByUpstreamKey:
		return breakdownSpec{columns: []breakdownColumn{
			{"provider_id", "provider_id"}, {"key_name", "key_name"},
		}}, true
	case UsageBreakdownGroupByAccessKey:
		return breakdownSpec{
			join:      " LEFT JOIN request_access_key ak ON ak.request_id = request_metrics.id",
			columns:   []breakdownColumn{{"ak.access_key_id", "access_key_id"}},
			nameField: "access_key_name",
		}, true
	case UsageBreakdownGroupByWorkspace:
		return breakdownSpec{
			join:    " LEFT JOIN request_workspace w ON w.request_id = request_metrics.id",
			columns: []breakdownColumn{{"w.workspace", "workspace"}},
		}, true
	case UsageBreakdownGroupByCallerType:
		return breakdownSpec{columns: []breakdownColumn{{"caller_type", "caller_type"}}}, true
	case UsageBreakdownGroupByStatus:
		return breakdownSpec{columns: []breakdownColumn{{"status_code", "status_code"}}, integer: true}, true
	case UsageBreakdownGroupByAPIFormat:
		return breakdownSpec{
			join:    " LEFT JOIN request_shape sh ON sh.request_id = request_metrics.id",
			columns: []breakdownColumn{{"sh.api_format", "api_format"}},
		}, true
	}
	return breakdownSpec{}, false
}

// breakdownKey 是一组的身份：最多两个分组列的取值，NULL 是一个合法身份。
//
// 用 sql.NullString 而不是把 NULL 渲染成占位串：NULL 与空串是两件事（前者渲染成 JSON
// null），而任何占位串都可能与真实取值撞上。整数列（status_code）也以十进制文本存放，
// 渲染与排序时再按 integer 还原。
type breakdownKey [2]sql.NullString

// breakdownCell 是一组在一个桶里的格子。
type breakdownCell struct {
	Key   breakdownKey
	Epoch int64
}

// breakdownKeyLess 是 key 的升序：逐列比较，NULL 排最前（与 statusRowIDLess 同一约定），
// 整数列按数值、其余按字节序（SQLite 的 BINARY 排序）。
//
// 只在排名值相同时用作决胜键：同量的两组必须有一个稳定顺序，否则同一份数据两次请求的
// 组序可能不同，界面（和用例）都没法依赖。
func breakdownKeyLess(left, right breakdownKey, integer bool) bool {
	for column := range left {
		a, b := left[column], right[column]
		if a.Valid != b.Valid {
			return !a.Valid
		}
		if !a.Valid || a.String == b.String {
			continue
		}
		if integer {
			leftNumber, _ := strconv.ParseInt(a.String, 10, 64)
			rightNumber, _ := strconv.ParseInt(b.String, 10, 64)
			if leftNumber != rightNumber {
				return leftNumber < rightNumber
			}
			continue
		}
		return a.String < b.String
	}
	return false
}

// UsageBreakdown 返回按一个维度分组的趋势：前 Limit 组各自的总量与序列，其余并成 other。
//
// 形状由本项目自己定：
//
//	{
//	  count_semantics, window: {from, to, hours}, group_by, rank_by, bucket_seconds,
//	  filters: {…与 /ui/filtered-metrics.json 同形，见 dimensionsEcho…},
//	  buckets: [{started_at, complete}],
//	  groups: [{key: {…分组字段…}, stats: {…UsageStats…}, series: {requests: [...], …}}],
//	  other: null | {group_count, stats, series},
//	  total_groups
//	}
//
// buckets 与 filtered-metrics 的 series 点位逐个对应（同一张网格，见文件头）。series 的
// 每个数组都与 buckets 等长、空桶给 0；给的是**合计**（duration_ms / first_token_ms 是
// 耗时之和）而不是平均：平均值不能跨桶、跨组相加，界面要画「按请求数加权的平均耗时」
// 只能从合计除出来。
//
// 排名按窗口内（桶网格内）的 requests 或 total_tokens 降序、key 升序决胜；total_groups 是
// 截断之前的组数，other.group_count 是被并掉的组数——界面据此说明「其余 N 组」。没有被
// 截掉的组时 other 是 null（不是一个全零对象），界面据此决定画不画那一层。
func (s *Store) UsageBreakdown(params UsageBreakdownParams) (*canonical.Value, error) {
	// 校验顺序：hours → bucket_seconds → limit → group_by → rank_by → 点数上限。与
	// FilteredMetrics 一样是**防御性**校验，正常请求先过服务端的参数校验（query.go）。
	if params.Hours != nil && *params.Hours <= 0 {
		return nil, formatValidationError("hours must be greater than zero")
	}
	if params.BucketSeconds < 0 {
		return nil, formatValidationError("bucket_seconds must be greater than zero")
	}
	limit := params.Limit
	if limit == 0 {
		// 零值兜底：缺省值是契约的一部分，但存储层不把「调用方忘了填」当成错误。
		limit = usageBreakdownDefaultLimit
	}
	if limit < 1 || limit > usageBreakdownMaxLimit {
		return nil, formatValidationError("limit must be between 1 and %d", usageBreakdownMaxLimit)
	}
	groupBy := params.GroupBy
	if groupBy == "" {
		groupBy = UsageBreakdownGroupByModel
	}
	spec, ok := usageBreakdownSpec(groupBy)
	if !ok {
		return nil, formatValidationError("group_by must be one of %s", quotedChoices(UsageBreakdownGroupByValues))
	}
	rankBy := params.RankBy
	if rankBy == "" {
		rankBy = UsageBreakdownRankByRequests
	}
	if rankBy != UsageBreakdownRankByRequests && rankBy != UsageBreakdownRankByTokens {
		return nil, formatValidationError("rank_by must be one of %s", quotedChoices(UsageBreakdownRankByValues))
	}

	now := nowBeijing()
	var since *string
	if params.Hours != nil {
		value := formatISO(addHours(now, *params.Hours))
		since = &value
	}
	// 与 FilteredMetrics 同一个函数定网格：同一组参数得到同一组 started_at。
	bucketSeconds, firstEpoch, found, err := s.seriesGrid(params.Hours, params.BucketSeconds, params.Dimensions, now)
	if err != nil {
		return nil, err
	}

	var buckets []seriesBucket
	totals := map[breakdownKey]*UsageStats{}
	cells := map[breakdownCell]*UsageStats{}
	if found {
		buckets = seriesBuckets(firstEpoch, bucketSeconds, now)
		if err := s.usageBreakdownScan(spec, params.Dimensions, firstEpoch, bucketSeconds, now, totals, cells); err != nil {
			return nil, err
		}
	}

	keys := make([]breakdownKey, 0, len(totals))
	for key := range totals {
		keys = append(keys, key)
	}
	rankValue := func(key breakdownKey) int64 {
		if rankBy == UsageBreakdownRankByTokens {
			return totals[key].TotalTokens
		}
		return totals[key].Requests
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := rankValue(keys[i]), rankValue(keys[j])
		if left != right {
			return left > right
		}
		return breakdownKeyLess(keys[i], keys[j], spec.integer)
	})

	kept := keys
	var rest []breakdownKey
	if int64(len(keys)) > limit {
		kept, rest = keys[:limit], keys[limit:]
	}

	groups := canonical.NewArray()
	for _, key := range kept {
		groups.Arr = append(groups.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "key", Value: breakdownKeyValue(spec, key)},
			canonical.ObjectPair{Key: "stats", Value: totals[key].dict()},
			canonical.ObjectPair{Key: "series", Value: breakdownSeries(buckets, func(epoch int64) *UsageStats {
				return cells[breakdownCell{Key: key, Epoch: epoch}]
			})},
		))
	}

	other := canonical.NewNull()
	if len(rest) > 0 {
		// 被截掉的组并成一份：总量逐组折叠（status_codes 逐码相加），格子逐桶折叠。
		var otherTotal *UsageStats
		otherCells := map[int64]*UsageStats{}
		for _, key := range rest {
			otherTotal = absorbStats(otherTotal, totals[key])
			for _, bucket := range buckets {
				if cell := cells[breakdownCell{Key: key, Epoch: bucket.Epoch}]; cell != nil {
					otherCells[bucket.Epoch] = absorbStats(otherCells[bucket.Epoch], cell)
				}
			}
		}
		other = canonical.NewObjectOf(
			canonical.ObjectPair{Key: "group_count", Value: canonical.NewIntValue(int64(len(rest)))},
			canonical.ObjectPair{Key: "stats", Value: otherTotal.dict()},
			canonical.ObjectPair{Key: "series", Value: breakdownSeries(buckets, func(epoch int64) *UsageStats {
				return otherCells[epoch]
			})},
		)
	}

	bucketValues := canonical.NewArray()
	for _, bucket := range buckets {
		bucketValues.Arr = append(bucketValues.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "started_at", Value: canonical.NewString(formatISO(bucket.StartedAt))},
			canonical.ObjectPair{Key: "complete", Value: canonical.NewBool(bucket.Complete)},
		))
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: nullableStringValue(since)},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(formatISO(now))},
			canonical.ObjectPair{Key: "hours", Value: hoursValue(params.Hours)},
		)},
		canonical.ObjectPair{Key: "group_by", Value: canonical.NewString(groupBy)},
		canonical.ObjectPair{Key: "rank_by", Value: canonical.NewString(rankBy)},
		canonical.ObjectPair{Key: "bucket_seconds", Value: canonical.NewIntValue(bucketSeconds)},
		canonical.ObjectPair{Key: "filters", Value: dimensionsEcho(params.Dimensions)},
		canonical.ObjectPair{Key: "buckets", Value: bucketValues},
		canonical.ObjectPair{Key: "groups", Value: groups},
		canonical.ObjectPair{Key: "other", Value: other},
		canonical.ObjectPair{Key: "total_groups", Value: canonical.NewIntValue(int64(len(keys)))},
	), nil
}

// usageBreakdownScan 用一次 SQL 取出全部「组 × 桶 × 状态码」的聚合，并折进 totals 与 cells。
//
// 窗口、分桶表达式与参数顺序和 seriesPoints 逐字相同（先分桶的 4 个锚点/宽度参数，再
// metricFilter 的窗口与维度参数）：下界取**首桶起点**而不是窗口起点，两份读数因此落在
// 同一个桶网格上、逐桶可加。GROUP BY 用别名 bucket_epoch（SQLite 允许，seriesPoints
// 同样这么写）；group_by=status 时分组键里会出现两次 status_code，SQLite 接受重复的
// 分组项，结果不变。
func (s *Store) usageBreakdownScan(
	spec breakdownSpec,
	dims Dimensions,
	firstEpoch, bucketSeconds int64,
	now time.Time,
	totals map[breakdownKey]*UsageStats,
	cells map[breakdownCell]*UsageStats,
) error {
	startStr := formatISO(fromUnix(firstEpoch))
	untilStr := formatISO(now)
	whereSQL, parameters := queryFilter{Dimensions: dims}.metricFilter(&startStr, &untilStr).sql()

	expressions := make([]string, 0, len(spec.columns))
	for _, column := range spec.columns {
		expressions = append(expressions, column.expr)
	}
	columnSQL := strings.Join(expressions, ", ")
	bucketParams := []any{
		int64(BucketAnchorEpoch),
		bucketSeconds,
		bucketSeconds,
		int64(BucketAnchorEpoch),
	}
	rows, err := s.db().Query(
		"SELECT "+columnSQL+", "+bucketSQL+" AS bucket_epoch, status_code, "+usageAggregates+
			" FROM request_metrics"+spec.join+whereSQL+
			" GROUP BY "+columnSQL+", bucket_epoch, status_code",
		append(append([]any{}, bucketParams...), parameters...)...,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var texts [2]sql.NullString
		var integers [2]sql.NullInt64
		dests := make([]any, 0, len(spec.columns)+2+16)
		for i := range spec.columns {
			if spec.integer {
				dests = append(dests, &integers[i])
			} else {
				dests = append(dests, &texts[i])
			}
		}
		var epoch, statusCode sql.NullInt64
		dests = append(dests, &epoch, &statusCode)
		var scan statsScan
		dests = append(dests, scan.dests()...)
		if err := rows.Scan(dests...); err != nil {
			return err
		}
		if !epoch.Valid {
			// created_at 不是合法日期时 strftime 返回 NULL：与 seriesPoints 一样整行丢弃。
			continue
		}
		if epoch.Int64 < firstEpoch || (epoch.Int64-firstEpoch)%bucketSeconds != 0 {
			// 网格由 bucketEpoch 产出，SQL 的桶起点必然对齐；真出现不对齐说明 Go 侧与 SQL
			// 的锚点不一致（那就是 bug），丢弃而不是写出一个错位的格子（与
			// statusMatrixGroups 同一处理）。
			continue
		}
		var key breakdownKey
		for i := range spec.columns {
			if spec.integer {
				if integers[i].Valid {
					key[i] = sql.NullString{String: strconv.FormatInt(integers[i].Int64, 10), Valid: true}
				}
				continue
			}
			key[i] = texts[i]
		}
		stats := scan.stats()
		// 与 KeyUsage 同一套折叠：采纳第一个子组（MIN/MAX 落在真实极值上），status_codes
		// 按状态码累加各行的 COUNT(*)；NULL 状态码进主聚合、不进分布。
		foldKeyUsage(totals, key, stats, statusCode)
		foldKeyUsage(cells, breakdownCell{Key: key, Epoch: epoch.Int64}, stats, statusCode)
	}
	return rows.Err()
}

// breakdownKeyValue 渲染一组的 key 对象。
//
// 分组字段**恒在**，NULL 渲染成 JSON null；access_key 额外带一个 null 的显示名占位，由
// 服务端原位补上（canonical 覆盖已有键时保留位置，键序因此稳定）。
func breakdownKeyValue(spec breakdownSpec, key breakdownKey) *canonical.Value {
	pairs := make([]canonical.ObjectPair, 0, len(spec.columns)+1)
	for i, column := range spec.columns {
		value := canonical.NewNull()
		if key[i].Valid {
			if spec.integer {
				number, _ := strconv.ParseInt(key[i].String, 10, 64)
				value = canonical.NewIntValue(number)
			} else {
				value = canonical.NewString(key[i].String)
			}
		}
		pairs = append(pairs, canonical.ObjectPair{Key: column.field, Value: value})
	}
	if spec.nameField != "" {
		pairs = append(pairs, canonical.ObjectPair{Key: spec.nameField, Value: canonical.NewNull()})
	}
	return canonical.NewObjectOf(pairs...)
}

// breakdownSeriesFields 是 series 的字段与它们取自 UsageStats 的哪一项，顺序即响应的键序。
//
// 只给能跨桶、跨组相加的计数与合计：成功率、缓存率、平均耗时都能由界面从这些合计除
// 出来（与 statusCellValue 只给分子同一取舍）。
var breakdownSeriesFields = []struct {
	name  string
	value func(*UsageStats) int64
}{
	{"requests", func(s *UsageStats) int64 { return s.Requests }},
	{"successes", func(s *UsageStats) int64 { return s.Successes }},
	{"failures", func(s *UsageStats) int64 { return s.Failures }},
	{"retries", func(s *UsageStats) int64 { return s.Retries }},
	{"prompt_tokens", func(s *UsageStats) int64 { return s.PromptTokens }},
	{"completion_tokens", func(s *UsageStats) int64 { return s.CompletionTokens }},
	{"total_tokens", func(s *UsageStats) int64 { return s.TotalTokens }},
	{"cached_tokens", func(s *UsageStats) int64 { return s.CachedTokens }},
	{"duration_ms", func(s *UsageStats) int64 { return s.TotalDurationMS }},
	{"first_token_ms", func(s *UsageStats) int64 { return s.TotalFirstTokenMS }},
}

// breakdownSeries 把一组（或 other）的格子渲染成与 buckets 等长的稠密数组；空桶给 0。
//
// 空桶不省略：序列是**定长**的时间轴，少一格会让界面把后面所有点错位一列（与
// statusCellValue 同一条理由）。
func breakdownSeries(buckets []seriesBucket, cellAt func(epoch int64) *UsageStats) *canonical.Value {
	arrays := make([][]*canonical.Value, len(breakdownSeriesFields))
	for i := range arrays {
		arrays[i] = make([]*canonical.Value, 0, len(buckets))
	}
	empty := &UsageStats{}
	for _, bucket := range buckets {
		stats := cellAt(bucket.Epoch)
		if stats == nil {
			stats = empty
		}
		for i, field := range breakdownSeriesFields {
			arrays[i] = append(arrays[i], canonical.NewIntValue(field.value(stats)))
		}
	}
	pairs := make([]canonical.ObjectPair, 0, len(breakdownSeriesFields))
	for i, field := range breakdownSeriesFields {
		pairs = append(pairs, canonical.ObjectPair{Key: field.name, Value: canonical.NewArray(arrays[i]...)})
	}
	return canonical.NewObjectOf(pairs...)
}

// absorbStats 把 src 折进 dst 并返回结果；dst 为 nil 时采纳 src 的副本。
//
// 与 foldKeyUsage 的区别：那边折的是**一个状态码下的最细行**，状态分布由调用方按行数
// 累加；这里折的是两份已经聚好的统计，status_codes 要逐码相加（merge 不碰它）。
func absorbStats(dst, src *UsageStats) *UsageStats {
	if dst == nil {
		return src.clone()
	}
	dst.merge(src)
	for code, total := range src.StatusCodes {
		dst.StatusCodes[code] += total
	}
	return dst
}

// quotedChoices 把取值域渲染成 "'a', 'b' or 'c'"（与服务端 literal_error 的文案同一写法）。
func quotedChoices(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}
