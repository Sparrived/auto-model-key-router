package metrics

import (
	"database/sql"
	"sort"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件回答「WebUI 的筛选器里该列出哪些选项」：窗口内指标库里**实际出现过**的各维度
// 取值与各自的请求数（**有意增补**：参照实现没有筛选器，没有可比对的 oracle）。
//
// # 为什么从指标库取而不是从配置取
//
// 配置只说得出「现在配着哪些」，说不出「这段时间里谁真的有流量」：删掉的供应商、改名
// 之前的模型、已经吊销的访问密钥，它们的历史流量照样在库里，也照样值得筛出来看；反过来，
// 配着但从没被打到的 Key 列进筛选器只会让人筛出一张空图。
//
// # 为什么不带筛选
//
// 选项必须是**不筛**的读数：如果按当前条件收窄，选了「模型 A」以后供应商列表里就只剩
// A 走过的那几家，想改成「模型 A 或 B」时 B 已经从模型列表里消失了——筛选器会把自己
// 锁死。因此这条读数只接受时间窗口，不接受任何维度参数。
//
// # 成本
//
// 一次扫描：窗口内按全部维度列的组合 GROUP BY（两张旁挂表用 LEFT JOIN 带上），再在 Go
// 侧上卷成七份列表——与 rollup.go / keyusage.go 同一个思路，分七次 GROUP BY 就是七遍窗口
// 扫描。组合数只与实际出现过的取值组合有关，远小于行数。

// MetricDimensionsParams 是筛选器选项读数的参数。
//
// Hours 为 nil 表示不设时间窗口（对应 /metrics 的 all_history）。
type MetricDimensionsParams struct {
	Hours *float64
}

// CallerTypes 是 caller_type 的合法取值域（本机主凭据、工作空间推理凭据、访问密钥）。
//
// 服务端的 caller_type 参数校验直接引用它，筛选器选项也按它补齐：两边认同同一组字符串，
// 才不会出现「选项里有、参数校验却拒绝」的情况。顺序即契约里列出的顺序。
var CallerTypes = []string{"local", "workspace", "access_key"}

// metricDimensionColumns 是选项扫描的分组列，顺序即 SELECT / GROUP BY 的列序。
//
// 主表带别名 m：两张旁挂表都有 request_id，主表的列写全以免读的人去猜归属。
var metricDimensionColumns = [...]string{
	"m.caller_type",
	"m.model_id",
	"m.requested_model_id",
	"m.provider_id",
	"m.key_name",
	"a.access_key_id",
	"w.workspace",
}

// MetricDimensions 返回窗口内各维度实际出现过的取值与请求数。
//
// 形状由本项目自己定：
//
//	{
//	  window: {from, to, hours},
//	  providers:        [{id, requests}],
//	  models:           [{id, requests}],
//	  requested_models: [{id, requests}],
//	  upstream_keys:    [{provider_id, key_name, requests}],
//	  access_keys:      [{id, name, requests}],
//	  workspaces:       [{name, requests}],
//	  caller_types:     [{id, requests}]
//	}
//
// 每份列表按 requests 降序、再按 id 升序（流量大的选项排前面，同量时顺序稳定）。NULL 取值
// 不进列表：provider_id 为 NULL 的历史行没有可筛的供应商（`IN (…)` 对 NULL 恒不成立），
// 列成一个选项只会让人筛出空图。upstream_keys 同理只列 provider_id 非空的组合。
//
// access_keys[].name 恒为 null：显示名是配置的属性，本包不读配置，服务端在写响应前按
// 当前配置补上（配置里已经没有的 key 保持 null，见 internal/server/metric_dimensions.go）。
// 占位写在 id 与 requests 之间，是为了让补名字时**原位替换**、不打乱键序。
//
// caller_types 恒列出 CallerTypes 的三档（没有流量的档给 0）：取值域是固定的契约，界面
// 据此渲染固定的选项，不该因为这段时间恰好没有工作空间流量就少一个选项。库里残留的
// 其它取值（已删除的 visitor 档）不列：它们不是合法的筛选值，列出来只会换来一个 422。
func (s *Store) MetricDimensions(params MetricDimensionsParams) (*canonical.Value, error) {
	now := nowBeijing()
	var since *string
	if params.Hours != nil {
		if *params.Hours <= 0 {
			// 与 KeyUsage / WorkspaceUsage / AccessKeyUsage 同一套口径与文案。
			return nil, formatValidationError("hours must be greater than zero")
		}
		value := formatISO(addHours(now, *params.Hours))
		since = &value
	}
	untilStr := formatISO(now)

	// 窗口条件落在 WHERE 而不是 ON 上：两张旁挂表都是 LEFT JOIN，接在 ON 后面会被当成
	// 连接条件——窗口外的行照样返回，只是右表整列被置空（keyusage.go 有同一条说明）。
	filters := make([]string, 0, 2)
	parameters := make([]any, 0, 2)
	if since != nil {
		filters = append(filters, "m.created_at >= ?")
		parameters = append(parameters, *since)
	}
	filters = append(filters, "m.created_at <= ?")
	parameters = append(parameters, untilStr)

	columns := strings.Join(metricDimensionColumns[:], ", ")
	// LEFT JOIN 而不是 JOIN：没有访问密钥 / 工作空间归属的行照样有模型、供应商与上游
	// Key，必须留在那几份列表里。两张旁挂表都是 (request_id) 主键，一对一，不会让计数
	// 翻倍。
	rows, err := s.db().Query(
		"SELECT "+columns+", COUNT(*) AS requests"+
			" FROM request_metrics m"+
			" LEFT JOIN request_access_key a ON a.request_id = m.id"+
			" LEFT JOIN request_workspace w ON w.request_id = m.id"+
			" WHERE "+strings.Join(filters, " AND ")+
			" GROUP BY "+columns,
		parameters...,
	)
	if err != nil {
		return nil, err
	}

	providers := map[string]int64{}
	models := map[string]int64{}
	requestedModels := map[string]int64{}
	upstreamKeys := map[[2]string]int64{}
	accessKeys := map[string]int64{}
	workspaces := map[string]int64{}
	callerTypes := map[string]int64{}
	for rows.Next() {
		var values [len(metricDimensionColumns)]sql.NullString
		var requests int64
		dests := make([]any, 0, len(values)+1)
		for i := range values {
			dests = append(dests, &values[i])
		}
		dests = append(dests, &requests)
		if err := rows.Scan(dests...); err != nil {
			rows.Close()
			return nil, err
		}
		callerType, modelID, requestedModelID := values[0], values[1], values[2]
		providerID, keyName, accessKeyID, workspace := values[3], values[4], values[5], values[6]
		if callerType.Valid {
			callerTypes[callerType.String] += requests
		}
		if modelID.Valid {
			models[modelID.String] += requests
		}
		if requestedModelID.Valid {
			requestedModels[requestedModelID.String] += requests
		}
		if providerID.Valid {
			providers[providerID.String] += requests
			if keyName.Valid {
				upstreamKeys[[2]string{providerID.String, keyName.String}] += requests
			}
		}
		if accessKeyID.Valid {
			accessKeys[accessKeyID.String] += requests
		}
		if workspace.Valid {
			workspaces[workspace.String] += requests
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// caller_types 只列合法取值域，缺的档补 0（见函数说明）。
	callerTypeOptions := make(map[string]int64, len(CallerTypes))
	for _, callerType := range CallerTypes {
		callerTypeOptions[callerType] = callerTypes[callerType]
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: nullableStringValue(since)},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(untilStr)},
			canonical.ObjectPair{Key: "hours", Value: hoursValue(params.Hours)},
		)},
		canonical.ObjectPair{Key: "providers", Value: dimensionOptionRows(providers, "id", false)},
		canonical.ObjectPair{Key: "models", Value: dimensionOptionRows(models, "id", false)},
		canonical.ObjectPair{Key: "requested_models", Value: dimensionOptionRows(requestedModels, "id", false)},
		canonical.ObjectPair{Key: "upstream_keys", Value: upstreamKeyOptionRows(upstreamKeys)},
		canonical.ObjectPair{Key: "access_keys", Value: dimensionOptionRows(accessKeys, "id", true)},
		canonical.ObjectPair{Key: "workspaces", Value: dimensionOptionRows(workspaces, "name", false)},
		canonical.ObjectPair{Key: "caller_types", Value: dimensionOptionRows(callerTypeOptions, "id", false)},
	), nil
}

// dimensionOptionRows 把单值维度的计数渲染成 [{<idKey>, requests}]，按 requests 降序、
// 再按取值升序。
//
// withName 为真时在 id 与 requests 之间插一个 null 的 name 占位（只有 access_keys 用，
// 见 MetricDimensions 的说明）。
func dimensionOptionRows(counts map[string]int64, idKey string, withName bool) *canonical.Value {
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if counts[ids[i]] != counts[ids[j]] {
			return counts[ids[i]] > counts[ids[j]]
		}
		return ids[i] < ids[j]
	})
	rows := canonical.NewArray()
	for _, id := range ids {
		pairs := []canonical.ObjectPair{{Key: idKey, Value: canonical.NewString(id)}}
		if withName {
			pairs = append(pairs, canonical.ObjectPair{Key: "name", Value: canonical.NewNull()})
		}
		pairs = append(pairs, canonical.ObjectPair{Key: "requests", Value: canonical.NewIntValue(counts[id])})
		rows.Arr = append(rows.Arr, canonical.NewObjectOf(pairs...))
	}
	return rows
}

// upstreamKeyOptionRows 把上游 Key 的计数渲染成 [{provider_id, key_name, requests}]，按
// requests 降序、再按 (供应商, Key 名) 升序。
//
// 成对给出：筛选参数 upstream_key 收的就是这一对（key_name 只在同一个供应商内唯一）。
func upstreamKeyOptionRows(counts map[[2]string]int64) *canonical.Value {
	keys := make([][2]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
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
			canonical.ObjectPair{Key: "requests", Value: canonical.NewIntValue(counts[key])},
		))
	}
	return rows
}
