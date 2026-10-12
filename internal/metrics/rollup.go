package metrics

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
)

// 本文件是 snapshot 的聚合捷径：把参照实现的 9 个分组从「每组各扫一遍窗口」
// 压成「一次最细粒度扫描 + Go 侧上卷」。
//
// 参照实现的 _snapshot_sync（metrics.py:680）对同一个窗口调用 9 次 _query_stats，
// 而每次 _query_stats 自己又要跑两遍（聚合 + status_code 分布），合计 18 遍全窗口
// 扫描；窗口拉到一个月以上时行数接近全表，这 18 遍扫描就是 /metrics 的全部耗时
// （20 万行、8760 小时实测：18 遍约 5 秒，单遍只要约 0.25 秒）。用量统计页切到
// 月/季/半年/一年档时要等好几秒，根因就在这里。
//
// 能上卷的前提是 usageAggregates 的 16 个聚合对分组**可结合**：COUNT/SUM 相加、
// MIN/MAX 取极值。于是按「最细维度组合 + status_code」分组一次，再把每个最细组合
// 折进它所属的各个目标分组，数值与逐分组查询逐字节相同（由
// TestSnapshotRollupMatchesGroupedQueries 锁定）。
//
// 为什么最细组合里带 status_code：status_codes 分布本来是第二次扫描
// （`GROUP BY dims, status_code`）的结果，把它并进分组键以后，同一批行同时给出
// 聚合与状态分布，省掉那一整轮扫描。NULL 状态码仍然参与主聚合（计数口径是全部
// 请求），只是不进 status_codes 字典——参照实现的第二遍带 `status_code IS NOT NULL`。
//
// 这是**有意的实现偏离**：响应体逐字节不变，变的只是取的路径。参照实现那份
// queryStats 仍在（rate 窗口那一小段用它），留作上卷的对照物。

// snapshotDim 是最细分组里的维度列。
type snapshotDim int

const (
	dimCallerType snapshotDim = iota
	dimModelID
	dimRequestedModelID
	dimProviderID
	dimPoolName
	dimUpstreamModelID
	dimKeyName
)

// finestDimColumns 按 snapshotDim 的取值顺序排列，同时决定 SQL 里 SELECT /
// GROUP BY 的列序。
//
// 列序不影响正确性（各目标分组的键序由 sortEntries 在 Go 侧重排），只影响可读性
// 与最细 B 树的分组顺序，因此按「调用方 → 模型 → 任务 → 归属 → Key」的链路顺序写。
var finestDimColumns = [...]string{
	"caller_type",
	"model_id",
	"requested_model_id",
	"provider_id",
	"pool_name",
	"upstream_model_id",
	"key_name",
}

// rollupScope 决定一个目标分组接受哪些最细组合。
//
// 三个归属列（provider_id / pool_name / upstream_model_id）在参照实现里有两种互补
// 口径：归属维度分组只统计三者**全非空**的行（include_null_dimensions=False 会把
// 三个列一起加上 IS NOT NULL，而不是只加被选中的那个），未归属统计恰好统计其余的行。
// 两者互补，并集是全部行，所以一次不带归属条件的扫描足以同时供出两者。
type rollupScope int

const (
	scopeAll rollupScope = iota
	scopeAttributed
	scopeUnattributed
)

// accepts 报告某个最细组合是否属于该口径。
func (s rollupScope) accepts(values *[len(finestDimColumns)]sql.NullString) bool {
	switch s {
	case scopeAttributed:
		return values[dimProviderID].Valid && values[dimPoolName].Valid &&
			values[dimUpstreamModelID].Valid
	case scopeUnattributed:
		return !values[dimProviderID].Valid || !values[dimPoolName].Valid ||
			!values[dimUpstreamModelID].Valid
	default:
		return true
	}
}

// rollupTarget 是一个目标分组：从最细组合投影出哪几个维度、接受哪些行、结果写进哪。
//
// 维度最多两个，因为 snapshot 只按单维或两维分组（dimKey 的上限）。
type rollupTarget struct {
	dims  []snapshotDim
	scope rollupScope
	out   *statsResult
}

// key 把最细组合投影成目标分组的键。
//
// 目标分组用到的四个非归属列都是 NOT NULL，归属列又在口径里被要求非空，因此这里
// 实际取不到 NULL；仍走 renderDimension 是为了与 dimensionKey 共用同一套渲染规则，
// 将来若有人放宽口径也不会两处不一致。
func (t rollupTarget) key(values *[len(finestDimColumns)]sql.NullString) dimKey {
	key := dimKey{N: len(t.dims)}
	if len(t.dims) > 0 {
		key.A = renderDimension(values[t.dims[0]])
	}
	if len(t.dims) > 1 {
		key.B = renderDimension(values[t.dims[1]])
	}
	return key
}

// snapshotGroups 是 snapshot 需要的全部分组，由一次最细粒度扫描上卷得到。
//
// 字段与 snapshotSync 里的变量一一对应，只是 total / unattributed 换成了单键的
// statsResult（它们是无维度分组，dict 里只有一个 dimKey{} 项）。
type snapshotGroups struct {
	total           *statsResult
	callerTypes     *statsResult
	models          *statsResult
	requestedModels *statsResult
	modelRequested  *statsResult
	keys            *statsResult
	providers       *statsResult
	providerPools   *statsResult
	upstreamModels  *statsResult
	unattributed    *statsResult
}

// newSnapshotGroups 建好全部分组的空容器，并预置两个无维度分组。
func newSnapshotGroups() *snapshotGroups {
	groups := &snapshotGroups{
		total:           newStatsResult(),
		callerTypes:     newStatsResult(),
		models:          newStatsResult(),
		requestedModels: newStatsResult(),
		modelRequested:  newStatsResult(),
		keys:            newStatsResult(),
		providers:       newStatsResult(),
		providerPools:   newStatsResult(),
		upstreamModels:  newStatsResult(),
		unattributed:    newStatsResult(),
	}
	// 空窗口也要有 total / unattributed 两个零值分组：参照实现的聚合查询恒返回
	// 一行（COUNT(*) 为 0），to_dict() 因此渲染出全零对象而不是缺键。
	groups.total.setDefault(dimKey{})
	groups.unattributed.setDefault(dimKey{})
	return groups
}

// targets 返回上卷的全部目标分组。顺序只影响遍历次序，不影响结果。
func (g *snapshotGroups) targets() []rollupTarget {
	return []rollupTarget{
		{dims: nil, scope: scopeAll, out: g.total},
		{dims: []snapshotDim{dimCallerType}, scope: scopeAll, out: g.callerTypes},
		{dims: []snapshotDim{dimModelID}, scope: scopeAll, out: g.models},
		{dims: []snapshotDim{dimRequestedModelID}, scope: scopeAll, out: g.requestedModels},
		{dims: []snapshotDim{dimModelID, dimRequestedModelID}, scope: scopeAll, out: g.modelRequested},
		{dims: []snapshotDim{dimModelID, dimKeyName}, scope: scopeAll, out: g.keys},
		{dims: []snapshotDim{dimProviderID}, scope: scopeAttributed, out: g.providers},
		{dims: []snapshotDim{dimProviderID, dimPoolName}, scope: scopeAttributed, out: g.providerPools},
		{dims: []snapshotDim{dimUpstreamModelID}, scope: scopeAttributed, out: g.upstreamModels},
		{dims: nil, scope: scopeUnattributed, out: g.unattributed},
	}
}

// snapshotRollup 用一次最细粒度扫描算出 snapshot 的全部分组。
//
// 时间边界与参照实现一致（`created_at >= ?` / `created_at <= ?`），为 nil 表示不设该
// 边界（all_history 与内部 since 都是这种形态）。
//
// providerIDs / accessKeyIDs 非空时只统计这些供应商 / 这些访问密钥发起的请求（维度内
// OR、维度间 AND），两者都为空时 SQL 与加这两个参数之前逐字节相同。用子查询而不是
// JOIN：这条扫描的主表是裸 `request_metrics`，加 JOIN 会动到既有 SQL 的形状（与
// stats.go 的 metricFilter 同一处理，条件形状也共用 appendDimensionFilters）。
func (s *Store) snapshotRollup(
	sinceCreatedAt, untilCreatedAt *string,
	providerIDs, accessKeyIDs []string,
) (*snapshotGroups, error) {
	groups := newSnapshotGroups()

	filters := make([]string, 0, 4)
	parameters := make([]any, 0, 4)
	if sinceCreatedAt != nil {
		filters = append(filters, "created_at >= ?")
		parameters = append(parameters, *sinceCreatedAt)
	}
	if untilCreatedAt != nil {
		filters = append(filters, "created_at <= ?")
		parameters = append(parameters, *untilCreatedAt)
	}
	filters, parameters = appendDimensionFilters(filters, parameters, providerIDs, accessKeyIDs)
	whereSQL := ""
	if len(filters) > 0 {
		whereSQL = " WHERE " + strings.Join(filters, " AND ")
	}

	groupColumns := strings.Join(finestDimColumns[:], ", ")
	rows, err := s.db().Query(
		"SELECT "+groupColumns+", status_code, "+usageAggregates+
			" FROM request_metrics"+whereSQL+
			" GROUP BY "+groupColumns+", status_code",
		parameters...,
	)
	if err != nil {
		return nil, err
	}

	targets := groups.targets()
	for rows.Next() {
		var values [len(finestDimColumns)]sql.NullString
		var statusCode sql.NullInt64
		dests := make([]any, 0, len(values)+1+16)
		for i := range values {
			dests = append(dests, &values[i])
		}
		dests = append(dests, &statusCode)
		var scan statsScan
		dests = append(dests, scan.dests()...)
		if err := rows.Scan(dests...); err != nil {
			rows.Close()
			return nil, err
		}
		stats := scan.stats()
		for _, target := range targets {
			if !target.scope.accepts(&values) {
				continue
			}
			key := target.key(&values)
			merged, exists := target.out.index[key]
			if !exists {
				// 采纳第一个子组（而不是累加到零值上）能让 MIN/MAX 直接落在真实
				// 极值上：duration_ms / first_token_ms 都是非负列，但零值当恒等元
				// 会把「全为负」的极端数据抹成 0，采纳语义则没有这个前提。
				merged = stats.clone()
				target.out.set(key, merged)
			} else {
				merged.merge(stats)
			}
			if statusCode.Valid {
				// 状态计数就是这个最细组合的行数（COUNT(*)），累加即得目标分组
				// 在该状态码下的请求数。键序不影响输出：statusCodeDict 按字符串
				// 重新排序（对应 Python 的 dict(sorted(counter.items()))）。
				merged.StatusCodes[strconv.FormatInt(statusCode.Int64, 10)] += stats.Requests
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	groups.sortEntries()
	return groups, nil
}

// sortEntries 把每个目标分组的行序重排成「按自己的维度升序」。
//
// 一次扫描只能给出一个全局顺序，而各目标分组投影出的顺序互不相同：最细键里
// caller_type 排在 model_id 前面，于是「local 的行」贡献的模型会整体先于「access_key
// 的行」里的新模型，投影到 model_id 就不是字典序了。参照实现是每个分组各自
// `ORDER BY dims`，而这个顺序就是响应里 JSON 对象的键序（canonical 按插入顺序输出），
// 所以必须逐分组重排。
//
// 比较规则与 SQLite 的 BINARY 排序一致：TEXT 列按 UTF-8 字节逐字节比较，Go 的字符串
// `<` 正是这个语义。目标分组的维度列都是 NOT NULL 列（归属列又被口径要求非空），
// 因此不用处理「NULL 排最前」。
func (g *snapshotGroups) sortEntries() {
	for _, target := range g.targets() {
		entries := target.out.Entries
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].Key.A != entries[j].Key.A {
				return entries[i].Key.A < entries[j].Key.A
			}
			return entries[i].Key.B < entries[j].Key.B
		})
	}
}

// clone 复制一份统计结果。
//
// StatusCodes 必须换成新字典：同一个最细行会被折进多个目标分组，共用一张字典会让
// 它们互相污染（上卷结果是各自独立的计数）。
func (s *UsageStats) clone() *UsageStats {
	copied := *s
	copied.StatusCodes = make(map[string]int64, len(s.StatusCodes))
	for code, total := range s.StatusCodes {
		copied.StatusCodes[code] = total
	}
	return &copied
}

// merge 把另一个子组的聚合折进自身。
//
// 调用方保证自身已经采纳过至少一个子组（见 snapshotRollup 的采纳分支），因此这里
// 只需要处理「相加」与「取极值」，不用再考虑零值恒等元。status_codes 不在这里合并：
// 它要按状态码逐个累加，由调用方连同行数一起处理。
func (s *UsageStats) merge(other *UsageStats) {
	s.Requests += other.Requests
	s.Successes += other.Successes
	s.Failures += other.Failures
	s.Retries += other.Retries
	s.PromptTokens += other.PromptTokens
	s.CompletionTokens += other.CompletionTokens
	s.TotalTokens += other.TotalTokens
	s.CachedTokens += other.CachedTokens
	s.CacheCreationInputTokens += other.CacheCreationInputTokens
	s.CacheReadInputTokens += other.CacheReadInputTokens
	s.TotalDurationMS += other.TotalDurationMS
	s.TotalFirstTokenMS += other.TotalFirstTokenMS
	s.MaxDurationMS = maxInt64(s.MaxDurationMS, other.MaxDurationMS)
	s.MaxFirstTokenMS = maxInt64(s.MaxFirstTokenMS, other.MaxFirstTokenMS)
	s.MinDurationMS = minInt64Ptr(s.MinDurationMS, other.MinDurationMS)
	s.MinFirstTokenMS = minInt64Ptr(s.MinFirstTokenMS, other.MinFirstTokenMS)
}

// maxInt64 取较大值。
func maxInt64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

// minInt64Ptr 取较小的非空值；nil 表示「该子组没有 MIN」（实际上不会出现，每个最细
// 组合至少一行，而非负列的 MIN 恒有值，这里只是把恒等元写清楚）。
func minInt64Ptr(current, other *int64) *int64 {
	if other == nil || (current != nil && *current <= *other) {
		return current
	}
	return other
}
