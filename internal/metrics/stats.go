package metrics

import (
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// ValidationError 对应 metrics.py 各 _sync 方法抛出的 ValueError。
//
// management API 用它区分「请求非法」与「存储故障」并映射成 400，所以 Message
// 必须逐字与 Python 一致，前端会原样展示。
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// metricFilter 对应 _metric_filter_sql 的关键字参数。
//
// 用指针表示「未提供」（Python 的 None），因为空字符串与 0 都是**有效**的过滤值，
// 不能与 None 混同。
type metricFilter struct {
	SinceCreatedAt   *string
	UntilCreatedAt   *string
	CallerType       *string
	ModelID          *string
	RequestedModelID *string
	ProviderID       *string
	PoolName         *string
	UpstreamModelID  *string
	KeyName          *string
	StatusCode       *int64
	Success          *bool
	Attributed       *bool
}

// attributionColumns 是三个「归属」维度，顺序与 metrics.py:1004 一致。
var attributionColumns = []string{"provider_id", "pool_name", "upstream_model_id"}

// sql 对应 _metric_filter_sql（metrics.py:964）。
//
// 过滤条件的**拼接顺序**是查询计划与参数顺序的一部分：时间范围在前，随后是
// 固定顺序的各列、success，最后是 attributed。改变顺序会让 EXPLAIN 结果与
// 参照实现不一致，也会让本包与 Python 的 SQL 文本无法逐字比对。
func (f metricFilter) sql() (string, []any) {
	filters := make([]string, 0, 14)
	parameters := make([]any, 0, 14)
	if f.SinceCreatedAt != nil {
		filters = append(filters, "created_at >= ?")
		parameters = append(parameters, *f.SinceCreatedAt)
	}
	if f.UntilCreatedAt != nil {
		filters = append(filters, "created_at <= ?")
		parameters = append(parameters, *f.UntilCreatedAt)
	}
	for _, entry := range []struct {
		Column string
		Value  *string
	}{
		{"caller_type", f.CallerType},
		{"model_id", f.ModelID},
		{"requested_model_id", f.RequestedModelID},
		{"provider_id", f.ProviderID},
		{"pool_name", f.PoolName},
		{"upstream_model_id", f.UpstreamModelID},
		{"key_name", f.KeyName},
	} {
		if entry.Value == nil {
			continue
		}
		filters = append(filters, entry.Column+" = ?")
		parameters = append(parameters, *entry.Value)
	}
	if f.StatusCode != nil {
		filters = append(filters, "status_code = ?")
		parameters = append(parameters, *f.StatusCode)
	}
	if f.Success != nil {
		filters = append(filters, "success = ?")
		// Python 的 `1 if success else 0`：显式写成分支，别依赖 bool→int 转换。
		if *f.Success {
			parameters = append(parameters, int64(1))
		} else {
			parameters = append(parameters, int64(0))
		}
	}
	if f.Attributed != nil {
		if *f.Attributed {
			for _, column := range attributionColumns {
				filters = append(filters, column+" IS NOT NULL")
			}
		} else {
			parts := make([]string, 0, len(attributionColumns))
			for _, column := range attributionColumns {
				parts = append(parts, column+" IS NULL")
			}
			filters = append(filters, "("+strings.Join(parts, " OR ")+")")
		}
	}
	if len(filters) == 0 {
		return "", parameters
	}
	return " WHERE " + strings.Join(filters, " AND "), parameters
}

// appendFilter 对应 _append_filter（metrics.py:1014）。
func appendFilter(whereSQL, condition string) string {
	joiner := "WHERE"
	if whereSQL != "" {
		joiner = "AND"
	}
	return whereSQL + " " + joiner + " " + condition
}

// values 返回 filters 需要的、按 metrics.py 顺序排列的 (键, 值) 列表，供构造
// 响应里的 "filters" 字典。顺序即 dict 的插入顺序：caller_type, model_id,
// requested_model_id, provider_id, pool_name, upstream_model_id, key_name,
// status_code, success, attributed。
func (q queryFilter) filterPairs() []canonical.ObjectPair {
	pairs := make([]canonical.ObjectPair, 0, 10)
	if q.CallerType != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "caller_type", Value: canonical.NewString(*q.CallerType)})
	}
	if q.ModelID != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "model_id", Value: canonical.NewString(*q.ModelID)})
	}
	if q.RequestedModelID != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "requested_model_id", Value: canonical.NewString(*q.RequestedModelID)})
	}
	if q.ProviderID != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "provider_id", Value: canonical.NewString(*q.ProviderID)})
	}
	if q.PoolName != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "pool_name", Value: canonical.NewString(*q.PoolName)})
	}
	if q.UpstreamModelID != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "upstream_model_id", Value: canonical.NewString(*q.UpstreamModelID)})
	}
	if q.KeyName != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "key_name", Value: canonical.NewString(*q.KeyName)})
	}
	if q.StatusCode != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "status_code", Value: canonical.NewIntValue(*q.StatusCode)})
	}
	if q.Success != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "success", Value: canonical.NewBool(*q.Success)})
	}
	if q.Attributed != nil {
		pairs = append(pairs, canonical.ObjectPair{Key: "attributed", Value: canonical.NewBool(*q.Attributed)})
	}
	return pairs
}

// queryFilter 是请求级过滤条件，对应 _metric_filter_sql 的全部命名参数。
type queryFilter struct {
	CallerType       *string
	ModelID          *string
	RequestedModelID *string
	ProviderID       *string
	PoolName         *string
	UpstreamModelID  *string
	KeyName          *string
	StatusCode       *int64
	Success          *bool
	Attributed       *bool
}

// metricFilter 把请求级过滤条件与时间边界合并成 SQL 过滤器。
func (q queryFilter) metricFilter(since, until *string) metricFilter {
	return metricFilter{
		SinceCreatedAt:   since,
		UntilCreatedAt:   until,
		CallerType:       q.CallerType,
		ModelID:          q.ModelID,
		RequestedModelID: q.RequestedModelID,
		ProviderID:       q.ProviderID,
		PoolName:         q.PoolName,
		UpstreamModelID:  q.UpstreamModelID,
		KeyName:          q.KeyName,
		StatusCode:       q.StatusCode,
		Success:          q.Success,
		Attributed:       q.Attributed,
	}
}

// dimKey 是 _query_stats 的维度元组键。
//
// Python 用 tuple[str, ...] 当字典键；Go 的切片不可比较，而本包最多只按两个维度
// 分组（见 snapshot 的全部调用），因此用定长结构体代替拼接字符串——避免自定义
// 分隔符与维度值冲突。
type dimKey struct {
	N    int
	A, B string
}

// dims 返回维度值，按原顺序。
func (k dimKey) dims() []string {
	switch k.N {
	case 0:
		return nil
	case 1:
		return []string{k.A}
	default:
		return []string{k.A, k.B}
	}
}

// statsEntry 保住 _query_stats 结果的行序（ORDER BY 维度），以便复刻 Python 字典
// 的插入顺序。
type statsEntry struct {
	Key   dimKey
	Stats *UsageStats
}

// statsResult 是按维度分组的结果，兼具顺序与查找。
type statsResult struct {
	Entries []statsEntry
	index   map[dimKey]*UsageStats
}

func newStatsResult() *statsResult {
	return &statsResult{index: map[dimKey]*UsageStats{}}
}

func (r *statsResult) get(key dimKey) *UsageStats { return r.index[key] }

// set 覆盖已有键（对应 result[key] = ...，Python 覆盖时保留原位置）。
func (r *statsResult) set(key dimKey, stats *UsageStats) {
	if _, exists := r.index[key]; exists {
		r.index[key] = stats
		return
	}
	r.index[key] = stats
	r.Entries = append(r.Entries, statsEntry{Key: key, Stats: stats})
}

// setDefault 对应 dict.setdefault：仅当键不存在时插入新的空统计。
func (r *statsResult) setDefault(key dimKey) *UsageStats {
	if stats, exists := r.index[key]; exists {
		return stats
	}
	stats := &UsageStats{StatusCodes: map[string]int64{}}
	r.set(key, stats)
	return stats
}

// statsScan 承接一行聚合结果，min_* 用 NullInt64 保留 NULL。
type statsScan struct {
	requests, successes, failures, retries int64
	prompt, completion, total, cached      int64
	cacheCreation, cacheRead               int64
	totalDuration                          int64
	minDuration                            sql.NullInt64
	maxDuration                            int64
	totalFirstToken                        int64
	minFirstToken                          sql.NullInt64
	maxFirstToken                          int64
}

// dests 返回 Scan 目标，顺序必须与 usageAggregates 一致。
func (s *statsScan) dests() []any {
	return []any{
		&s.requests, &s.successes, &s.failures, &s.retries,
		&s.prompt, &s.completion, &s.total, &s.cached,
		&s.cacheCreation, &s.cacheRead,
		&s.totalDuration, &s.minDuration, &s.maxDuration,
		&s.totalFirstToken, &s.minFirstToken, &s.maxFirstToken,
	}
}

// stats 对应 _stats_from_aggregate（metrics.py:1073）。
func (s *statsScan) stats() *UsageStats {
	stats := &UsageStats{
		Requests:                 s.requests,
		Successes:                s.successes,
		Failures:                 s.failures,
		Retries:                  s.retries,
		PromptTokens:             s.prompt,
		CompletionTokens:         s.completion,
		TotalTokens:              s.total,
		CachedTokens:             s.cached,
		CacheCreationInputTokens: s.cacheCreation,
		CacheReadInputTokens:     s.cacheRead,
		TotalDurationMS:          s.totalDuration,
		MaxDurationMS:            s.maxDuration,
		TotalFirstTokenMS:        s.totalFirstToken,
		MaxFirstTokenMS:          s.maxFirstToken,
		StatusCodes:              map[string]int64{},
	}
	if s.minDuration.Valid {
		value := s.minDuration.Int64
		stats.MinDurationMS = &value
	}
	if s.minFirstToken.Valid {
		value := s.minFirstToken.Int64
		stats.MinFirstTokenMS = &value
	}
	return stats
}

// queryStats 对应 _query_stats（metrics.py:789）。
//
// dimensions 为空时不分组的聚合恒返回一行，Python 的 `if not dimensions and not
// result` 因此是死分支，这里照抄以保持结构一致。
//
// 调用方只剩 snapshot 的 rate 窗口（几分钟、行数极少）与
// TestSnapshotRollupMatchesGroupedQueries：snapshot 的 9 个分组已经改走
// snapshotRollup（一次扫描 + 上卷，见 rollup.go），但**别删这里**——它是参照实现
// 那份取数口径的落点，既是上卷的对照物，也是除上卷之外唯一覆盖任意维度组合的路径。
func (s *Store) queryStats(
	dimensions []string,
	sinceCreatedAt, untilCreatedAt *string,
	includeNullDimensions bool,
) (*statsResult, error) {
	dimensionSQL := strings.Join(dimensions, ", ")
	selectPrefix := ""
	if dimensionSQL != "" {
		selectPrefix = dimensionSQL + ", "
	}

	filters := make([]string, 0, 8)
	parameters := make([]any, 0, 8)
	if sinceCreatedAt != nil {
		filters = append(filters, "created_at >= ?")
		parameters = append(parameters, *sinceCreatedAt)
	}
	if untilCreatedAt != nil {
		filters = append(filters, "created_at <= ?")
		parameters = append(parameters, *untilCreatedAt)
	}
	if !includeNullDimensions {
		// 关键细节：只要**任一**维度属于归属维度，就把三个归属维度全部加上
		// IS NOT NULL（而非只加被选中的那些）。照抄 metrics.py:806-819。
		required := dimensions
		for _, dimension := range dimensions {
			if isAttributionColumn(dimension) {
				required = attributionColumns
				break
			}
		}
		for _, dimension := range required {
			filters = append(filters, dimension+" IS NOT NULL")
		}
	}
	whereSQL := ""
	if len(filters) > 0 {
		whereSQL = " WHERE " + strings.Join(filters, " AND ")
	}
	groupSQL := ""
	if dimensionSQL != "" {
		groupSQL = " GROUP BY " + dimensionSQL
	}
	orderSQL := ""
	if dimensionSQL != "" {
		orderSQL = " ORDER BY " + dimensionSQL
	}

	query := "SELECT " + selectPrefix + usageAggregates +
		" FROM request_metrics" + whereSQL + groupSQL + orderSQL
	rows, err := s.db().Query(query, parameters...)
	if err != nil {
		return nil, err
	}
	result := newStatsResult()
	for rows.Next() {
		dests := make([]any, 0, len(dimensions)+16)
		values := make([]sql.NullString, len(dimensions))
		for i := range values {
			dests = append(dests, &values[i])
		}
		var scan statsScan
		dests = append(dests, scan.dests()...)
		if err := rows.Scan(dests...); err != nil {
			rows.Close()
			return nil, err
		}
		result.set(dimensionKey(values), scan.stats())
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(dimensions) == 0 && len(result.Entries) == 0 {
		result.set(dimKey{}, &UsageStats{StatusCodes: map[string]int64{}})
	}

	// status_code 二次遍历。GROUP BY / ORDER BY 的拼法有分支：无维度时补
	// "GROUP BY status_code" / "ORDER BY status_code"，有维度时在既有子句后
	// 追加 ", status_code"。照抄 metrics.py:853-863。
	statusWhere := appendFilter(whereSQL, "status_code IS NOT NULL")
	statusGroup := " GROUP BY status_code"
	if groupSQL != "" {
		statusGroup = groupSQL + ", status_code"
	}
	statusOrder := " ORDER BY status_code"
	if orderSQL != "" {
		statusOrder = orderSQL + ", status_code"
	}
	statusQuery := "SELECT " + selectPrefix + "status_code, COUNT(*) AS total" +
		"\n            FROM request_metrics\n            " + statusWhere +
		"\n            " + statusGroup + "\n            " + statusOrder
	statusRows, err := s.db().Query(statusQuery, parameters...)
	if err != nil {
		return nil, err
	}
	for statusRows.Next() {
		dests := make([]any, 0, len(dimensions)+2)
		values := make([]sql.NullString, len(dimensions))
		for i := range values {
			dests = append(dests, &values[i])
		}
		var statusCode sql.NullInt64
		var total int64
		dests = append(dests, &statusCode, &total)
		if err := statusRows.Scan(dests...); err != nil {
			statusRows.Close()
			return nil, err
		}
		stats := result.setDefault(dimensionKey(values))
		stats.StatusCodes[nullStringString(statusCode)] = total
	}
	if err := statusRows.Err(); err != nil {
		statusRows.Close()
		return nil, err
	}
	statusRows.Close()
	return result, nil
}

// dimensionKey 把维度值转成键。
//
// Python 是 `tuple(str(row[dimension]) for ...)`：NULL 会变成字面量 "None"。
// 各维度列都是 TEXT 亲和，存储时已被转成文本，因此 NullString 足以覆盖；NULL 时
// 显式取 "None" 以复刻 str(None)。
func dimensionKey(values []sql.NullString) dimKey {
	key := dimKey{N: len(values)}
	if len(values) > 0 {
		key.A = renderDimension(values[0])
	}
	if len(values) > 1 {
		key.B = renderDimension(values[1])
	}
	return key
}

// renderDimension 渲染单个维度值：NULL 渲染成 "None"（复刻 str(None)）。
//
// 与 rollup.go 的上卷共用：两处的键必须由同一条规则产生，否则同一份数据会得到
// 两份不同的响应。
func renderDimension(value sql.NullString) string {
	if !value.Valid {
		return "None"
	}
	return value.String
}

// nullStringString 复刻 str(row["status_code"])；SQLite 的 INTEGER 在 Go 侧以
// int64 读出，这里转回十进制文本。
func nullStringString(value sql.NullInt64) string {
	if !value.Valid {
		return "None"
	}
	return strconv.FormatInt(value.Int64, 10)
}

func isAttributionColumn(dimension string) bool {
	for _, column := range attributionColumns {
		if column == dimension {
			return true
		}
	}
	return false
}

// queryFilteredStats 对应 _query_filtered_stats（metrics.py:540）。
func (s *Store) queryFilteredStats(whereSQL string, parameters []any) (*UsageStats, error) {
	query := "SELECT " + usageAggregates + " FROM request_metrics" + whereSQL
	row := s.db().QueryRow(query, parameters...)
	var scan statsScan
	if err := row.Scan(scan.dests()...); err != nil {
		return nil, err
	}
	stats := scan.stats()

	statusWhere := appendFilter(whereSQL, "status_code IS NOT NULL")
	statusQuery := "SELECT status_code, COUNT(*) AS total FROM request_metrics" +
		statusWhere + " GROUP BY status_code ORDER BY status_code"
	rows, err := s.db().Query(statusQuery, parameters...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var statusCode sql.NullInt64
		var total int64
		if err := rows.Scan(&statusCode, &total); err != nil {
			return nil, err
		}
		stats.StatusCodes[nullStringString(statusCode)] = total
	}
	return stats, rows.Err()
}

// routerStatus 对应 _router_status（metrics.py:1164）。
func routerStatus(recent *UsageStats) string {
	if recent.Requests == 0 {
		return "green"
	}
	successRate := float64(recent.Successes) / float64(recent.Requests)
	retryRate := float64(recent.Retries) / float64(recent.Requests)
	if successRate < 0.80 {
		return "red"
	}
	if successRate < 0.95 || retryRate > 0.5 {
		return "yellow"
	}
	return "green"
}

// seriesPointLimitExceeded 对应
// `math.ceil(hours * 3600 / bucket_seconds) + 1 > MAX_SERIES_POINTS`。
//
// 逐字的浮点运算顺序必须保持：先乘 3600 再除，不能改成 hours/bucket*3600。
func seriesPointLimitExceeded(hours float64, bucketSeconds int64) bool {
	return math.Ceil(hours*3600/float64(bucketSeconds))+1 > MaxSeriesPoints
}

// requestItem 对应 _request_item（metrics.py:1026）。
//
// 与 key_stats 的 recent_requests 不同，这里的 success/retried 会经过 bool()
// 转成真正的布尔（JSON true/false），而后者原样返回整数。别把两处合并。
//
// 末尾三列来自两条旁挂表（workspace / client_addr / user_agent，见 store.go 的
// RequestHistory 与 schema.go）：没有归属的行是 NULL，渲染成 JSON null 而不是空串，
// 让「这次请求没有来源记录」与「来源是空字符串」在读端分得开。
func requestItem(row *sql.Rows) (*canonical.Value, error) {
	var (
		id                                                  int64
		createdAt, callerType, modelID, requestedModelID    sql.NullString
		providerID, poolName, upstreamModelID, keyName      sql.NullString
		statusCode                                          sql.NullInt64
		success, retried                                    sql.NullInt64
		promptTokens, completionTokens, totalTokens, cached sql.NullInt64
		cacheCreation, cacheRead, firstTokenMS, durationMS  sql.NullInt64
		workspace, clientAddr, userAgent                    sql.NullString
	)
	if err := row.Scan(
		&id, &createdAt, &callerType, &modelID, &requestedModelID,
		&providerID, &poolName, &upstreamModelID, &keyName,
		&statusCode, &success, &retried, &promptTokens,
		&completionTokens, &totalTokens, &cached,
		&cacheCreation, &cacheRead, &firstTokenMS, &durationMS,
		&workspace, &clientAddr, &userAgent,
	); err != nil {
		return nil, err
	}
	prompt := promptTokens.Int64
	cachedValue := cached.Int64
	uncached := prompt - cachedValue
	if uncached < 0 {
		uncached = 0
	}
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewIntValue(id)},
		canonical.ObjectPair{Key: "created_at", Value: canonical.NewString(createdAt.String)},
		canonical.ObjectPair{Key: "caller_type", Value: canonical.NewString(callerType.String)},
		canonical.ObjectPair{Key: "model_id", Value: canonical.NewString(modelID.String)},
		canonical.ObjectPair{Key: "requested_model_id", Value: canonical.NewString(requestedModelID.String)},
		canonical.ObjectPair{Key: "provider_id", Value: nullableString(providerID)},
		canonical.ObjectPair{Key: "pool_name", Value: nullableString(poolName)},
		canonical.ObjectPair{Key: "upstream_model_id", Value: nullableString(upstreamModelID)},
		canonical.ObjectPair{Key: "key_name", Value: canonical.NewString(keyName.String)},
		canonical.ObjectPair{Key: "status_code", Value: nullableInt(statusCode)},
		canonical.ObjectPair{Key: "success", Value: canonical.NewBool(success.Int64 != 0)},
		canonical.ObjectPair{Key: "retried", Value: canonical.NewBool(retried.Int64 != 0)},
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(prompt)},
		canonical.ObjectPair{Key: "uncached_prompt_tokens", Value: canonical.NewIntValue(uncached)},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(completionTokens.Int64)},
		canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewIntValue(totalTokens.Int64)},
		canonical.ObjectPair{Key: "cached_tokens", Value: canonical.NewIntValue(cachedValue)},
		canonical.ObjectPair{Key: "cache_creation_input_tokens", Value: canonical.NewIntValue(cacheCreation.Int64)},
		canonical.ObjectPair{Key: "cache_read_input_tokens", Value: canonical.NewIntValue(cacheRead.Int64)},
		canonical.ObjectPair{Key: "first_token_ms", Value: canonical.NewIntValue(firstTokenMS.Int64)},
		canonical.ObjectPair{Key: "duration_ms", Value: canonical.NewIntValue(durationMS.Int64)},
		// —— 请求来源（本项目增补，参照实现的 items 没有这三项）——
		// 插在末尾而不是中间：既有调用方按名字取字段，新增键只影响读全量的用法。
		canonical.ObjectPair{Key: "workspace", Value: nullableString(workspace)},
		canonical.ObjectPair{Key: "client_addr", Value: nullableString(clientAddr)},
		canonical.ObjectPair{Key: "user_agent", Value: nullableString(userAgent)},
	), nil
}

// nullableString 把 SQL NULL 渲染成 JSON null，否则给字符串。
func nullableString(value sql.NullString) *canonical.Value {
	if !value.Valid {
		return canonical.NewNull()
	}
	return canonical.NewString(value.String)
}

// nullableInt 把 SQL NULL 渲染成 JSON null，否则给整数。
func nullableInt(value sql.NullInt64) *canonical.Value {
	if !value.Valid {
		return canonical.NewNull()
	}
	return canonical.NewIntValue(value.Int64)
}

// nestedStats 对应 _nested_stats（metrics.py:1099）：两个维度的结果转成
// {parent: {child: stats}}。
func nestedStats(result *statsResult) *canonical.Value {
	nested := canonical.NewObject()
	for _, entry := range result.Entries {
		parent := canonical.NewObject()
		if existing, ok := nested.Obj.Get(entry.Key.A); ok {
			parent = existing
		}
		parent.SetKey(entry.Key.B, entry.Stats.dict())
		nested.SetKey(entry.Key.A, parent)
	}
	return nested
}

// flatStats 对应 `{key[0]: stats.to_dict() for key, stats in result.items()}`。
func flatStats(result *statsResult) *canonical.Value {
	flat := canonical.NewObject()
	for _, entry := range result.Entries {
		flat.SetKey(entry.Key.A, entry.Stats.dict())
	}
	return flat
}

// hoursValue 渲染响应里的 "hours"：None 原样为 null。
func hoursValue(hours *float64) *canonical.Value {
	if hours == nil {
		return canonical.NewNull()
	}
	return canonical.NewFloat(*hours)
}

// formatValidationError 构造带参数的错误信息。
func formatValidationError(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}
