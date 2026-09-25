package metrics

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件回答「这些流量是哪一把 Key 出去的」（**有意增补**：参照实现没有这个维度）。
//
// # 为什么不塞进 /metrics
//
// /metrics 是**对照参照实现**的读数，形状已经发布，不能加字段（见
// internal/server/workspace_usage.go 的说明）。「按供应商 + Key 名」这个组合分组
// 不是参照实现的口径（那边只有「真实模型 → Key 名」的嵌套统计），因此单独一份本
// 项目自有的读数，挂在 /ui/key-usage.json 上。
//
// # 与 /metrics 的 keys 的分工
//
// 那边的 Key 只是模型的子项，答不出两件事：
//
//   - 「这一把 Key 一共出去了多少流量」——它的量被拆在多个模型行里，要自己加总；
//   - 「这是哪一家的 Key」——key_name 只在**同一个供应商内**唯一，两家的同名 Key
//     在那边会并成一行。request_metrics 里 provider_id 与 key_name 是两列，本文件
//     因此把供应商一并作为分组键。
//
// # 三份拆分
//
//   - upstream_keys：按 (provider_id, key_name)，即转发时实际用的那把上游 Key；
//   - model_keys：按 (model_id, provider_id, key_name)，给「模型 / Key」明细表用，
//     同名 Key 因此带上供应商前缀，不再与别家的同名 Key 混成一行；
//   - access_keys：按 access_key_id，即**调用方**凭据（来自 request_access_key
//     旁挂表，见 schema.go）。它与上游 Key 是两件事：一把访问密钥的流量会打到多把
//     上游 Key 上，反之亦然，所以两份拆分各自独立、互不嵌套。
//
// 三份拆分与总量共用同一个时间窗口：分几次取会让界面上的合计与拆分后的行在窗口
// 边界处对不上（与 WorkspaceUsage / AccessKeyUsage 同一条理由）。
//
// # 未归属
//
// provider_id 可空（升级前的历史行、以及不走 proxy 的写入路径），而 key_name 恒
// 非空。这些行因此进不了 upstream_keys / model_keys——它们没有可归属的供应商。
// 响应里的 unattributed 就是它们，让「按 Key 明细加起来 != total」这件事有出处，
// 而不是静默少一截。**不给空值补一个占位供应商**：那会与真实取值混在一起，看的人
// 分不出哪条是数据、哪条是兜底（与 workspace.go 的「两端都非空才成边」同一原则）。

// KeyUsageParams 是 Key 读数的参数。
//
// Hours 为 nil 表示不设时间窗口（对应 /metrics 的 all_history）。
type KeyUsageParams struct {
	Hours *float64
}

// keyUsageFinestColumns 是最细分组里的列，顺序即 SQL 的 SELECT / GROUP BY 列序。
//
// 四列恰好是「哪把上游 Key + 哪把访问密钥」这两个身份的全部组成：前两列（含
// key_name）决定上游 Key，第四列决定调用方凭据。列序只影响可读性与分组顺序，
// 不影响结果——三份拆分都在 Go 侧从同一批最细组合投影出来。
var keyUsageFinestColumns = [...]string{
	"m.model_id",
	"m.provider_id",
	"m.key_name",
	"a.access_key_id",
}

// KeyUsage 返回按上游 Key、模型 × 上游 Key 与访问密钥的用量拆分。
//
// 形状由本项目自己定（参照实现没有对应接口，没有可比对的 oracle）：
//
//	{
//	  count_semantics, window: {from, to, hours},
//	  upstream_keys: [{provider_id, key_name, stats}, ...],
//	  model_keys:    [{model_id, provider_id, key_name, stats}, ...],
//	  access_keys:   [{access_key_id, stats}, ...],
//	  unattributed:  {<UsageStats 的全部字段>}
//	}
//
// 三份拆分都是**数组**而不是嵌套对象：每一行的身份是 2~3 个字段，用嵌套对象要
// 挑一个当外层键，而那恰好会重新引入「同名 Key 并成一行」的问题。数组的顺序是
// 身份字段的字典序（稳定输出，便于用例与 diff），界面自己按列再排一次。
//
// access_keys 里**只有 access_key_id**：显示名在配置里，而本包不读配置。服务端
// 在写响应前补上 access_key_name（见 internal/server/key_usage.go）。
func (s *Store) KeyUsage(params KeyUsageParams) (*canonical.Value, error) {
	now := nowBeijing()
	var since *string
	if params.Hours != nil {
		if *params.Hours <= 0 {
			// 与 TimeSeries / WorkspaceUsage / AccessKeyUsage 同一套口径与文案。
			return nil, formatValidationError("hours must be greater than zero")
		}
		value := formatISO(addHours(now, *params.Hours))
		since = &value
	}
	untilStr := formatISO(now)

	// 时间条件必须落在 **WHERE** 里。
	//
	// 别照抄 workspace.go 那几处「把 metricWindow 的片段直接接在查询串尾部」的写法：
	// 那几处是 INNER JOIN，ON 与 WHERE 等价，所以看不出问题；这里是 LEFT JOIN，
	// 接在 ON 后面会被当成**连接条件**——窗口外的行照样返回，只是右表整列被置空
	// （实测：整库的行都会冒出来，且 access_key_id 全是 NULL，看起来像"访问密钥
	// 归属丢了"而不是"窗口没生效"）。
	filters := make([]string, 0, 2)
	parameters := make([]any, 0, 2)
	if since != nil {
		filters = append(filters, "m.created_at >= ?")
		parameters = append(parameters, *since)
	}
	// 上界恒有值（就是 now）：与 Snapshot / WorkspaceUsage 一致，窗口总是封口的。
	filters = append(filters, "m.created_at <= ?")
	parameters = append(parameters, untilStr)
	whereSQL := " WHERE " + strings.Join(filters, " AND ")

	// 一次最细粒度扫描 + Go 侧上卷：三份拆分各自 GROUP BY 一遍要扫三次窗口，而
	// 「全部历史」在 20 万行库上是每遍约 0.25 秒（见 rollup.go 的成本说明）。
	//
	// LEFT JOIN 而不是 JOIN：完整权限与工作空间凭据的请求**没有**访问密钥归属，
	// 但它们照样有上游 Key，必须留在 upstream_keys / model_keys 里。旁挂表是
	// (request_id) 主键，一对一，不会让聚合行数翻倍。
	columns := strings.Join(keyUsageFinestColumns[:], ", ")
	rows, err := s.db().Query(
		"SELECT "+columns+", m.status_code, "+usageAggregates+
			" FROM request_metrics m LEFT JOIN request_access_key a ON a.request_id = m.id"+
			whereSQL+
			" GROUP BY "+columns+", m.status_code",
		parameters...,
	)
	if err != nil {
		return nil, err
	}

	upstreamKeys := map[[2]string]*UsageStats{}
	modelKeys := map[[3]string]*UsageStats{}
	accessKeys := map[string]*UsageStats{}
	// 未归属用「只有一个键的映射」承载，好复用下面同一套「采纳第一个子组」的折叠
	// 逻辑（MIN/MAX 直接落在真实极值上，而不是从零值恒等元起步）。
	unattributed := map[struct{}]*UsageStats{}

	for rows.Next() {
		var modelID, keyName string
		var providerID, accessKeyID sql.NullString
		var statusCode sql.NullInt64
		dests := []any{&modelID, &providerID, &keyName, &accessKeyID, &statusCode}
		var scan statsScan
		dests = append(dests, scan.dests()...)
		if err := rows.Scan(dests...); err != nil {
			rows.Close()
			return nil, err
		}
		stats := scan.stats()

		if providerID.Valid {
			foldKeyUsage(upstreamKeys, [2]string{providerID.String, keyName}, stats, statusCode)
			foldKeyUsage(modelKeys, [3]string{modelID, providerID.String, keyName}, stats, statusCode)
		} else {
			foldKeyUsage(unattributed, struct{}{}, stats, statusCode)
		}
		if accessKeyID.Valid {
			foldKeyUsage(accessKeys, accessKeyID.String, stats, statusCode)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: nullableStringValue(since)},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(untilStr)},
			canonical.ObjectPair{Key: "hours", Value: hoursValue(params.Hours)},
		)},
		canonical.ObjectPair{Key: "upstream_keys", Value: upstreamKeyRows(upstreamKeys)},
		canonical.ObjectPair{Key: "model_keys", Value: modelKeyRows(modelKeys)},
		canonical.ObjectPair{Key: "access_keys", Value: accessKeyRows(accessKeys)},
		canonical.ObjectPair{Key: "unattributed", Value: unattributedStats(unattributed).dict()},
	), nil
}

// foldKeyUsage 把一个最细组合折进目标分组。
//
// 采纳第一个子组（而不是累加到零值上）能让 MIN/MAX 直接落在真实极值上：duration_ms
// / first_token_ms 都是非负列，但零值当恒等元会把「全为负」的极端数据抹成 0，采纳
// 语义则没有这个前提（与 rollup.go 的 snapshotRollup 同一取舍）。
//
// status_codes 在这里按状态码逐个累加：最细组合里带了 status_code，因此一个目标
// 分组在该状态码下的请求数就是各行 COUNT(*) 的和。NULL 状态码仍参与主聚合（计数
// 口径是全部请求），只是不进 status_codes 字典——与 queryStats 的第二遍带
// `status_code IS NOT NULL` 一致。
func foldKeyUsage[K comparable](
	groups map[K]*UsageStats,
	key K,
	stats *UsageStats,
	statusCode sql.NullInt64,
) {
	merged, exists := groups[key]
	if !exists {
		merged = stats.clone()
		groups[key] = merged
	} else {
		merged.merge(stats)
	}
	if statusCode.Valid {
		merged.StatusCodes[strconv.FormatInt(statusCode.Int64, 10)] += stats.Requests
	}
}

// unattributedStats 取出未归属那一桶；窗口内没有这类行时给一个零值统计（而不是
// 缺字段），界面因此不必为「这一档暂时是空的」写分支。
func unattributedStats(groups map[struct{}]*UsageStats) *UsageStats {
	if stats, exists := groups[struct{}{}]; exists {
		return stats
	}
	return &UsageStats{StatusCodes: map[string]int64{}}
}

// upstreamKeyRows 把上游 Key 分组渲染成数组，按 (供应商, Key 名) 升序。
func upstreamKeyRows(groups map[[2]string]*UsageStats) *canonical.Value {
	keys := make([][2]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	rows := canonical.NewArray()
	for _, key := range keys {
		rows.Arr = append(rows.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "provider_id", Value: canonical.NewString(key[0])},
			canonical.ObjectPair{Key: "key_name", Value: canonical.NewString(key[1])},
			canonical.ObjectPair{Key: "stats", Value: groups[key].dict()},
		))
	}
	return rows
}

// modelKeyRows 把模型 × 上游 Key 分组渲染成数组，按 (模型, 供应商, Key 名) 升序。
func modelKeyRows(groups map[[3]string]*UsageStats) *canonical.Value {
	keys := make([][3]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		for field := 0; field < 3; field++ {
			if keys[i][field] != keys[j][field] {
				return keys[i][field] < keys[j][field]
			}
		}
		return false
	})
	rows := canonical.NewArray()
	for _, key := range keys {
		rows.Arr = append(rows.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "model_id", Value: canonical.NewString(key[0])},
			canonical.ObjectPair{Key: "provider_id", Value: canonical.NewString(key[1])},
			canonical.ObjectPair{Key: "key_name", Value: canonical.NewString(key[2])},
			canonical.ObjectPair{Key: "stats", Value: groups[key].dict()},
		))
	}
	return rows
}

// accessKeyRows 把访问密钥分组渲染成数组，按 key_id 升序。
func accessKeyRows(groups map[string]*UsageStats) *canonical.Value {
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := canonical.NewArray()
	for _, id := range ids {
		rows.Arr = append(rows.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "access_key_id", Value: canonical.NewString(id)},
			canonical.ObjectPair{Key: "stats", Value: groups[id].dict()},
		))
	}
	return rows
}
