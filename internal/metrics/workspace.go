package metrics

import (
	"database/sql"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件是工作空间的用量读数（**有意增补**：参照实现没有工作空间）。
//
// 与 request_metrics 的关系：那份表一字未改，工作空间归属存在 request_workspace
// 旁挂表里（理由见 schema.go）。这里全部查询都从 request_metrics 出发、INNER JOIN
// 旁挂表，因此：
//
//   - 升级前写入的历史行、以及任何没有归属的写入路径，都**不会**被算进任一工作
//     空间，而是统一落在 unattributed 里。这点很关键：把它们兜底成 default 会把
//     旧账记到默认工作空间头上，看起来像真的。
//   - 旁挂表是 (request_id) 主键，JOIN 走 rowid，一对一，不会让聚合行数翻倍。

// workspaceFlowLayers 是桑基图的层级顺序，与前端约定一致。
//
// 六层恰好是请求在系统里的流转链路：
//
//	工作空间 → 请求模型（任务名/别名）→ 实际模型 → Provider → 上游 Key → 上游模型
//
// 任务名以 requested_model_id 的身份出现（调用方传 TASK_XXXXXX），因此第 1→2 层
// 就是「哪个工作空间在用哪个任务」——这正是工作空间隔离后最需要看清的一段。
//
// Key 这一层是后补的（原先是五层，供应商直接连到上游模型）：同一家供应商可以配多把
// Key，而 v4 起「模型 → target」的选择就是**选 Key**，所以「打到哪家」与「用的哪把
// Key」是两个不同的问题，只画到供应商会把它们并成一条流带。key_name 就是被选中的
// 那把上游 Provider Key 名（见 internal/proxy 的 recordMetric），该列恒非空。
var workspaceFlowLayers = []string{
	"workspace",
	"requested_model_id",
	"model_id",
	"provider_id",
	"key_name",
	"upstream_model_id",
}

// flowPairs 是相邻两层之间的连边查询，source/target 都是本文件里的常量表达式，
// 不含用户输入，因此可以直接拼进 SQL（与 ensureColumn 的处理一致）。
//
// 顺序即 layers 的相邻顺序。第 1 段的 source 是旁挂表的 workspace（表定义 NOT
// NULL）；其余都是 request_metrics 的列，其中 provider_id / upstream_model_id 可空。
var flowPairs = []struct{ Source, Target string }{
	{"w.workspace", "m.requested_model_id"},
	{"m.requested_model_id", "m.model_id"},
	{"m.model_id", "m.provider_id"},
	{"m.provider_id", "m.key_name"},
	{"m.key_name", "m.upstream_model_id"},
}

// WorkspaceUsageParams 是工作空间读数的参数。
//
// Hours 为 nil 表示不设时间窗口（对应 /metrics 的 all_history）。
//
// Workspace 非空时只统计该工作空间：嵌入方的面板 key 只能看自己那个空间的用量，
// 否则响应里会把别的空间的流量一并交出去（见 internal/server 的面板端点）。
type WorkspaceUsageParams struct {
	Hours *float64
	// Workspace 限定要统计的工作空间名；空串表示不限定（完整权限的全部空间视图）。
	Workspace string
}

// WorkspaceUsage 返回各工作空间的用量统计与流向连边。
//
// 形状由本项目自己定（参照实现没有对应接口，没有可比对的 oracle）：
//
//	{
//	  count_semantics, window: {from, to, hours},
//	  workspaces: [{name, <UsageStats 的全部字段>}, ...],
//	  unattributed: {<UsageStats 的全部字段>},
//	  layers: [...],
//	  links: [{source_layer, target_layer, source, target, requests, total_tokens}, ...]
//	}
//
// 把「统计」与「流向」放在同一个响应里：两者共用同一个时间窗口，分两次取会让
// 界面上的合计与图上的宽度在窗口边界处对不上。
func (s *Store) WorkspaceUsage(params WorkspaceUsageParams) (*canonical.Value, error) {
	now := nowBeijing()
	var since *string
	if params.Hours != nil {
		if *params.Hours <= 0 {
			// 与 TimeSeries 同一套口径与文案（hours 为 nil 时根本不会走到这里）。
			return nil, formatValidationError("hours must be greater than zero")
		}
		value := formatISO(addHours(now, *params.Hours))
		since = &value
	}
	untilStr := formatISO(now)

	stats, err := s.workspaceStats(since, &untilStr, params.Workspace)
	if err != nil {
		return nil, err
	}
	unattributed, err := s.unattributedStats(since, &untilStr, params.Workspace)
	if err != nil {
		return nil, err
	}
	links, err := s.workspaceFlowLinks(since, &untilStr, params.Workspace)
	if err != nil {
		return nil, err
	}

	workspaces := canonical.NewArray()
	for _, entry := range stats.Entries {
		workspaces.Arr = append(workspaces.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "name", Value: canonical.NewString(entry.Key.A)},
			canonical.ObjectPair{Key: "stats", Value: entry.Stats.dict()},
		))
	}

	layers := canonical.NewArray()
	for _, layer := range workspaceFlowLayers {
		layers.Arr = append(layers.Arr, canonical.NewString(layer))
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: nullableStringValue(since)},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(untilStr)},
			canonical.ObjectPair{Key: "hours", Value: hoursValue(params.Hours)},
		)},
		canonical.ObjectPair{Key: "workspaces", Value: workspaces},
		canonical.ObjectPair{Key: "unattributed", Value: unattributed.dict()},
		canonical.ObjectPair{Key: "layers", Value: layers},
		canonical.ObjectPair{Key: "links", Value: links},
	), nil
}

// workspaceStats 按工作空间分组聚合，复用 usageAggregates 与 statsScan。
//
// 列名不加表别名：usageAggregates 里的 success / total_tokens 等只存在于
// request_metrics，旁挂表只有 request_id 与 workspace，因此不会歧义。
//
// scope 非空时只统计那个工作空间（面板 key 的场景）；此时结果最多一项。
func (s *Store) workspaceStats(since, until *string, scope string) (*statsResult, error) {
	where, parameters := workspaceWindow(since, until)
	scopeWhere, scopeParameters := workspaceScope(scope)
	where += scopeWhere
	parameters = append(parameters, scopeParameters...)
	rows, err := s.db().Query(
		"SELECT w.workspace, "+usageAggregates+
			" FROM request_metrics m JOIN request_workspace w ON w.request_id = m.id"+where+
			" GROUP BY w.workspace ORDER BY w.workspace",
		parameters...,
	)
	if err != nil {
		return nil, err
	}
	result := newStatsResult()
	for rows.Next() {
		var workspace sql.NullString
		var scan statsScan
		dests := append([]any{&workspace}, scan.dests()...)
		if err := rows.Scan(dests...); err != nil {
			rows.Close()
			return nil, err
		}
		result.set(dimKey{N: 1, A: workspace.String}, scan.stats())
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// status_code 二次遍历：工作空间的失败分布要靠它，口径与 queryStats 一致
	// （只统计非 NULL 的状态码）。
	statusRows, err := s.db().Query(
		"SELECT w.workspace, status_code, COUNT(*) AS total"+
			" FROM request_metrics m JOIN request_workspace w ON w.request_id = m.id"+
			where+" AND m.status_code IS NOT NULL"+
			" GROUP BY w.workspace, status_code ORDER BY w.workspace, status_code",
		parameters...,
	)
	if err != nil {
		return nil, err
	}
	for statusRows.Next() {
		var workspace sql.NullString
		var statusCode sql.NullInt64
		var total int64
		if err := statusRows.Scan(&workspace, &statusCode, &total); err != nil {
			statusRows.Close()
			return nil, err
		}
		stats := result.setDefault(dimKey{N: 1, A: workspace.String})
		stats.StatusCodes[nullStringString(statusCode)] = total
	}
	if err := statusRows.Err(); err != nil {
		statusRows.Close()
		return nil, err
	}
	statusRows.Close()
	return result, nil
}

// unattributedStats 统计**没有**工作空间归属的行。
//
// 存在的意义是升级后的第一眼：老库的历史行全都进这里，若直接丢掉，界面在升级后
// 会显示成一片空白，看起来像功能坏了。用 NOT EXISTS 而不是 LEFT JOIN ... IS NULL，
// 语义更直白且能走 request_id 主键索引。
//
// scope 非空时（面板 key 的场景）直接返回零值：无归属的行本来就不属于任何空间，
// 把全实例的未归属流量告诉一个只有单个空间权限的嵌入方，等于泄露出「别人还有多少
// 流量没记上」这种全局信息。
func (s *Store) unattributedStats(since, until *string, scope string) (*UsageStats, error) {
	if scope != "" {
		return &UsageStats{StatusCodes: map[string]int64{}}, nil
	}
	where, parameters := workspaceWindow(since, until)
	rows, err := s.db().Query(
		"SELECT "+usageAggregates+
			" FROM request_metrics m WHERE NOT EXISTS ("+
			"SELECT 1 FROM request_workspace w WHERE w.request_id = m.id)"+where,
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

	statusRows, err := s.db().Query(
		"SELECT status_code, COUNT(*) AS total FROM request_metrics m"+
			" WHERE NOT EXISTS (SELECT 1 FROM request_workspace w WHERE w.request_id = m.id)"+
			where+" AND m.status_code IS NOT NULL GROUP BY status_code ORDER BY status_code",
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

// workspaceFlowLinks 取相邻两层之间的连边。
//
// 统一规则：**两端都非空才成边**。某一端为空（provider_id / upstream_model_id 可空）
// 的请求因此在图上留出一段缺口——这是刻意的，缺口就是「未归属的流量」。若给空值
// 补一个占位节点，它会与真实取值混在一起，看图的人分不出哪条是数据、哪条是兜底。
func (s *Store) workspaceFlowLinks(since, until *string, scope string) (*canonical.Value, error) {
	where, parameters := workspaceWindow(since, until)
	scopeWhere, scopeParameters := workspaceScope(scope)
	where += scopeWhere
	parameters = append(parameters, scopeParameters...)
	links := canonical.NewArray()
	for index, pair := range flowPairs {
		rows, err := s.db().Query(
			"SELECT "+pair.Source+" AS source, "+pair.Target+" AS target,"+
				" COUNT(*) AS requests, COALESCE(SUM(m.total_tokens), 0) AS total_tokens"+
				" FROM request_metrics m JOIN request_workspace w ON w.request_id = m.id"+
				where+" AND "+pair.Source+" IS NOT NULL AND "+pair.Target+" IS NOT NULL"+
				" GROUP BY "+pair.Source+", "+pair.Target+
				" ORDER BY "+pair.Source+", "+pair.Target,
			parameters...,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var source, target string
			var requests, totalTokens int64
			if err := rows.Scan(&source, &target, &requests, &totalTokens); err != nil {
				rows.Close()
				return nil, err
			}
			links.Arr = append(links.Arr, canonical.NewObjectOf(
				canonical.ObjectPair{Key: "source_layer", Value: canonical.NewIntValue(int64(index))},
				canonical.ObjectPair{Key: "target_layer", Value: canonical.NewIntValue(int64(index + 1))},
				canonical.ObjectPair{Key: "source", Value: canonical.NewString(source)},
				canonical.ObjectPair{Key: "target", Value: canonical.NewString(target)},
				canonical.ObjectPair{Key: "requests", Value: canonical.NewIntValue(requests)},
				canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewIntValue(totalTokens)},
			))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return links, nil
}

// workspaceWindow 拼出时间窗口条件与参数。
//
// 用 m.created_at 限定（JOIN 后无歧义，但显式写别名更抗后续改动）。
func workspaceWindow(since, until *string) (string, []any) {
	where := ""
	parameters := make([]any, 0, 2)
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

// workspaceScope 拼出「只看某个工作空间」的条件与参数。
//
// 单独一个 helper 而不是塞进 workspaceWindow：unattributed 那份查询**没有** w 别名
// （它查的正是没有归属的行），把 w.workspace 条件混进去会直接是 SQL 错误。
func workspaceScope(workspace string) (string, []any) {
	if workspace == "" {
		return "", nil
	}
	return " AND w.workspace = ?", []any{workspace}
}
