package metrics

import (
	"database/sql"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件是访问密钥的用量读数（**有意增补**：参照实现没有访问密钥）。
//
// 与 request_metrics 的关系：那份表一字未改，访问密钥归属存在 request_access_key
// 旁挂表里（理由见 schema.go）。这里全部查询都从 request_metrics 出发、INNER JOIN
// 旁挂表，因此：
//
//   - 完整权限与工作空间推理凭据写入的行、以及升级前的历史行，都**不会**被算进任一
//     访问密钥。这是对的：它们本来就不出自访问密钥，兜底成某把 key 等于把别人的
//     用量记到这个人头上。
//   - 旁挂表是 (request_id) 主键，JOIN 走 rowid，一对一，不会让聚合行数翻倍。
//
// 与 workspace.go 的分工：那份按工作空间聚合（嵌入方面板），这份按访问密钥聚合
// （访问密钥看板）。两者形状刻意接近，前端可以复用渲染代码；但**不能合并成一份
// 读数**——访问密钥不绑定工作空间，一把 key 的流量会散在多个 workspace 值里，
// 按 workspace 过滤答不出「这把 key 用了多少」。

// AccessKeyLastUsed 返回每把访问密钥最近一次已记录的上游尝试时间，含失败，不限统计窗口。
func (s *Store) AccessKeyLastUsed() (map[string]string, error) {
	// 与最近调用一致按写入序取最后一条，避免 ISO 整秒/微秒混写时 MAX(created_at) 的字典序误判。
	rows, err := s.db().Query(
		"SELECT a.access_key_id, m.created_at FROM" +
			" (SELECT access_key_id, MAX(request_id) AS request_id FROM request_access_key GROUP BY access_key_id) a" +
			" JOIN request_metrics m ON m.id = a.request_id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lastUsed := map[string]string{}
	for rows.Next() {
		var id, createdAt string
		if err := rows.Scan(&id, &createdAt); err != nil {
			return nil, err
		}
		lastUsed[id] = createdAt
	}
	return lastUsed, rows.Err()
}

// AccessKeyUsageParams 是访问密钥读数的参数。
//
// Hours 为 nil 表示不设时间窗口（对应 /metrics 的 all_history）。
//
// AccessKeyID 必填：这份读数**只**服务「给一把 key 看它自己」的场景（访问密钥看板）。
// 不做「全部访问密钥的总览」——那是管理面的 /metrics 按 caller_type=access_key 过滤
// 就能拿到的视图，没必要在这里再开一个入口，更不该让一个受限凭据有可能读到别人的
// 数字（与 workspace.go 的 scope 是同一个原则）。
type AccessKeyUsageParams struct {
	Hours       *float64
	AccessKeyID string
	// RecentLimit 是最近调用明细的条数上限；<=0 时取 DefaultAccessKeyRecentLimit。
	RecentLimit int64
}

// DefaultAccessKeyRecentLimit 是最近调用明细的默认条数。
//
// 与 key_stats 的 50 对齐：看板上的表是给人扫一眼的，再多就要分页，而分页属于管理面
// 的 /metrics/requests。
const DefaultAccessKeyRecentLimit = 50

// MaxAccessKeyRecentLimit 是最近调用明细的条数上限。
//
// 与 request_history 的 200 对齐：这是同一种「一次取一批」的读数，上限也该一致，
// 否则调用方会以为传 200 有效而实际被静默截断。
const MaxAccessKeyRecentLimit = 200

// accessKeyDimensions 是看板上要按维度拆分的列，与响应里的键名一一对应。
//
// 三个维度的分工：
//   - model_id：本地路由名，回答「用了哪些模型」——这是调用方自己配的名字；
//   - provider_id：回答「打到哪些供应商」；
//   - upstream_model_id：真正发给上游的模型名，**唯一能与价格目录对上的字段**
//     （见 web/src/lib/pricing.ts 的 requestCost 注释）。看板的「花费」必须按它分组求和，
//     否则只能拿本地路由名去猜单价。代价是这一维通常比前两维多几项，但那是真实
//     数据，而把成本算错更糟。
//
// 不含 requested_model_id（任务名/别名）：访问密钥不能使用任务，别名也已经由
// model_id 归一，这一维对访问密钥恒等于 model_id 或为空。
var accessKeyDimensions = []string{"model_id", "provider_id", "upstream_model_id"}

// AccessKeyUsage 返回一把访问密钥的用量统计、按维度拆分与最近调用明细。
//
// 形状由本项目自己定（参照实现没有对应接口，没有可比对的 oracle）：
//
//	{
//	  count_semantics, window: {from, to, hours},
//	  access_key_id,
//	  stats: {<UsageStats 的全部字段>},
//	  dimensions: {
//	    model_id:    {<模型名>: {<UsageStats>}, ...},
//	    provider_id: {<供应商>: {<UsageStats>}, ...}
//	  },
//	  recent_requests: [{created_at, model_id, provider_id, upstream_model_id,
//	                     status_code, success, retried, prompt_tokens,
//	                     completion_tokens, total_tokens, cached_tokens,
//	                     first_token_ms, duration_ms}, ...]
//	}
//
// dimensions 的键就是**原始列名**（model_id / provider_id），不做翻译：与
// workspace.go 的 layers 同一套约定（那里也是 workspace / model_id / provider_id 等
// 原始列名），前端只在一处维护中文名映射。
//
// 统计、拆分与明细共用同一个时间窗口：分几次取会让界面上的合计与拆分后的行在窗口
// 边界处对不上（与 WorkspaceUsage 同一条理由）。
func (s *Store) AccessKeyUsage(params AccessKeyUsageParams) (*canonical.Value, error) {
	now := nowBeijing()
	var since *string
	if params.Hours != nil {
		if *params.Hours <= 0 {
			// 与 TimeSeries / WorkspaceUsage 同一套口径与文案。
			return nil, formatValidationError("hours must be greater than zero")
		}
		value := formatISO(addHours(now, *params.Hours))
		since = &value
	}
	untilStr := formatISO(now)

	stats, err := s.accessKeyStats(since, &untilStr, params.AccessKeyID)
	if err != nil {
		return nil, err
	}
	breakdowns, err := s.accessKeyBreakdowns(since, &untilStr, params.AccessKeyID)
	if err != nil {
		return nil, err
	}
	recent, err := s.accessKeyRecent(since, &untilStr, params.AccessKeyID, recentLimit(params.RecentLimit))
	if err != nil {
		return nil, err
	}

	dimensions := canonical.NewObjectOf()
	for _, dimension := range accessKeyDimensions {
		grouped := canonical.NewObjectOf()
		for _, entry := range breakdowns[dimension].Entries {
			// 维度值为 NULL 的请求不进分组（与 workspace.go 的「两端都非空才成边」
			// 同一原则）：给空值补一个键会与真实取值混在一起，看的人分不出哪条是
			// 数据、哪条是兜底。
			grouped.SetKey(entry.Key.A, entry.Stats.dict())
		}
		dimensions.SetKey(dimension, grouped)
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: nullableStringValue(since)},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(untilStr)},
			canonical.ObjectPair{Key: "hours", Value: hoursValue(params.Hours)},
		)},
		canonical.ObjectPair{Key: "access_key_id", Value: canonical.NewString(params.AccessKeyID)},
		canonical.ObjectPair{Key: "stats", Value: stats.dict()},
		canonical.ObjectPair{Key: "dimensions", Value: dimensions},
		canonical.ObjectPair{Key: "recent_requests", Value: recent},
	), nil
}

// recentLimit 归一化最近明细的条数上限。
//
// 超上限时报错而不是静默截断：与 request_history 的 limit 一致（那边越界也是 400）。
// 静默截断会让调用方以为拿到了全部。
func recentLimit(limit int64) int64 {
	if limit <= 0 {
		return DefaultAccessKeyRecentLimit
	}
	if limit > MaxAccessKeyRecentLimit {
		return MaxAccessKeyRecentLimit
	}
	return limit
}

// accessKeyStats 聚合一把访问密钥的总体用量。
//
// 复用 usageAggregates 与 statsScan，口径与 /metrics 完全一致（尤其是 failures 用
// SUM(CASE WHEN success = 0 ...) 而非 COUNT(*) - SUM(success)）。
func (s *Store) accessKeyStats(since, until *string, accessKeyID string) (*UsageStats, error) {
	where, parameters := accessKeyWindow(since, until, accessKeyID)
	rows, err := s.db().Query(
		"SELECT "+usageAggregates+
			" FROM request_metrics m JOIN request_access_key a ON a.request_id = m.id"+where,
		parameters...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scan statsScan
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		// 聚合查询恒返回一行，但驱动层仍可能给空结果；给一个零值统计。
		return &UsageStats{StatusCodes: map[string]int64{}}, nil
	}
	if err := rows.Scan(scan.dests()...); err != nil {
		return nil, err
	}
	stats := scan.stats()
	rows.Close()

	// status_code 二次遍历，口径与 queryStats / workspaceStats 一致（只统计非 NULL
	// 的状态码）；key_stats 的 recent_requests 不带它，但看板上要显示失败分布。
	statusRows, err := s.db().Query(
		"SELECT m.status_code, COUNT(*) AS total"+
			" FROM request_metrics m JOIN request_access_key a ON a.request_id = m.id"+
			where+" AND m.status_code IS NOT NULL"+
			" GROUP BY m.status_code ORDER BY m.status_code",
		parameters...,
	)
	if err != nil {
		return nil, err
	}
	defer statusRows.Close()
	for statusRows.Next() {
		var statusCode sql.NullInt64
		var total int64
		if err := statusRows.Scan(&statusCode, &total); err != nil {
			return nil, err
		}
		stats.StatusCodes[nullStringString(statusCode)] = total
	}
	return stats, statusRows.Err()
}

// accessKeyBreakdowns 按每个维度分别聚合，返回 维度 -> 分组结果。
//
// 每个维度一条查询（而不是一条 GROUP BY 多个维度）：那会得到笛卡尔式的组合键，而
// 看板要的是「按模型」与「按供应商」两张独立的榜，各自的小计应等于总量。
func (s *Store) accessKeyBreakdowns(since, until *string, accessKeyID string) (map[string]*statsResult, error) {
	where, parameters := accessKeyWindow(since, until, accessKeyID)
	result := make(map[string]*statsResult, len(accessKeyDimensions))
	for _, dimension := range accessKeyDimensions {
		// dimension 来自本文件的常量表，不来自用户输入，因此拼进 SQL 无注入面
		// （与 ensureColumn 的处理一致）。
		rows, err := s.db().Query(
			"SELECT "+dimension+", "+usageAggregates+
				" FROM request_metrics m JOIN request_access_key a ON a.request_id = m.id"+where+
				" AND "+dimension+" IS NOT NULL"+
				" GROUP BY "+dimension+" ORDER BY "+dimension,
			parameters...,
		)
		if err != nil {
			return nil, err
		}
		grouped := newStatsResult()
		for rows.Next() {
			var value sql.NullString
			var scan statsScan
			dests := append([]any{&value}, scan.dests()...)
			if err := rows.Scan(dests...); err != nil {
				rows.Close()
				return nil, err
			}
			grouped.set(dimKey{N: 1, A: value.String}, scan.stats())
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		result[dimension] = grouped
	}
	return result, nil
}

// accessKeyRecent 取最近的若干条调用明细。
//
// 按 id 倒序而不是 created_at：id 是插入序，同一微秒内的多条也能稳定排序，而
// created_at 是文本且同秒内会重复。这与 request_history 的排序键一致。
func (s *Store) accessKeyRecent(since, until *string, accessKeyID string, limit int64) (*canonical.Value, error) {
	where, parameters := accessKeyWindow(since, until, accessKeyID)
	parameters = append(parameters, limit)
	rows, err := s.db().Query(
		"SELECT m.created_at, m.model_id, m.provider_id, m.upstream_model_id,"+
			" m.status_code, m.success, m.retried,"+
			" m.prompt_tokens, m.completion_tokens, m.total_tokens, m.cached_tokens,"+
			" m.first_token_ms, m.duration_ms"+
			" FROM request_metrics m JOIN request_access_key a ON a.request_id = m.id"+where+
			" ORDER BY m.id DESC LIMIT ?",
		parameters...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := canonical.NewArray()
	for rows.Next() {
		var (
			createdAt, modelID                string
			providerID, upstreamModelID       sql.NullString
			statusCode                        sql.NullInt64
			success, retried                  int64
			prompt, completion, total, cached int64
			firstToken, duration              int64
		)
		if err := rows.Scan(
			&createdAt, &modelID, &providerID, &upstreamModelID,
			&statusCode, &success, &retried,
			&prompt, &completion, &total, &cached,
			&firstToken, &duration,
		); err != nil {
			return nil, err
		}
		// success / retried 在这里过 bool()（真正的 JSON 布尔），与 request_history
		// 的 items 一致、与 key_stats 的 recent_requests 不同（后者给整数）。
		// 明细是人看的，布尔比 0/1 更直白。
		items.Arr = append(items.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "created_at", Value: canonical.NewString(createdAt)},
			canonical.ObjectPair{Key: "model_id", Value: canonical.NewString(modelID)},
			canonical.ObjectPair{Key: "provider_id", Value: nullableString(providerID)},
			canonical.ObjectPair{Key: "upstream_model_id", Value: nullableString(upstreamModelID)},
			canonical.ObjectPair{Key: "status_code", Value: nullableInt(statusCode)},
			canonical.ObjectPair{Key: "success", Value: canonical.NewBool(success != 0)},
			canonical.ObjectPair{Key: "retried", Value: canonical.NewBool(retried != 0)},
			canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(prompt)},
			canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(completion)},
			canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewIntValue(total)},
			canonical.ObjectPair{Key: "cached_tokens", Value: canonical.NewIntValue(cached)},
			canonical.ObjectPair{Key: "first_token_ms", Value: canonical.NewIntValue(firstToken)},
			canonical.ObjectPair{Key: "duration_ms", Value: canonical.NewIntValue(duration)},
		))
	}
	return items, rows.Err()
}

// accessKeyWindow 拼出「某把访问密钥 + 时间窗口」的条件与参数。
//
// 时间用 m.created_at 限定，归属用 a.access_key_id 限定（JOIN 后无歧义，但显式写
// 别名更抗后续改动）。
func accessKeyWindow(since, until *string, accessKeyID string) (string, []any) {
	where := " WHERE a.access_key_id = ?"
	parameters := []any{accessKeyID}
	if since != nil {
		where += " AND m.created_at >= ?"
		parameters = append(parameters, *since)
	}
	if until != nil {
		where += " AND m.created_at <= ?"
		parameters = append(parameters, *until)
	}
	return where, parameters
}
