package metrics

import (
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件是 /ui 读数的**多值筛选维度**：一组条件怎样落成 SQL、怎样回显给界面。
//
// # 为什么单独一个结构
//
// 最早只有供应商与访问密钥两个维度，它们以 `providerIDs, accessKeyIDs []string` 的参数对
// 一路穿过 snapshotSync / snapshotRollup / queryStats / seriesPoints / KeyUsage /
// StatusMonitor 七八个函数。再加五个维度时继续摊参数，每个调用点都要按位置对齐七个实参
// ——抄错一个位置就会把模型名绑到工作空间的条件上，而类型系统查不出来（全是 []string）。
// 收成一个结构以后，「这组条件」在整条取数链上只有一个名字。
//
// # 语义
//
// **维度内 OR、维度间 AND**：同一维度的多个取值收成 `IN (?, …)`（上游 Key 是成对的
// `(provider_id = ? AND key_name = ?) OR …`），不同维度的条件之间是 AND。全部为空 = 不筛，
// 此时追加零个条件，SQL 与加这些维度之前**逐字节相同**——/metrics、/metrics/series、
// /metrics/requests 这三条已发布读数从不填这个结构，它们的 SQL 因此不可能被这里的改动碰到。
//
// # 与参照实现那几个单值过滤参数的关系
//
// metricFilter / queryFilter / RequestHistoryParams / TimeSeriesParams 里的 ModelID、
// CallerType、Success 等单值字段是**参照实现已发布**的过滤参数（`model_id = ?`、
// `success = ?`），这里的多值维度不替换、不合并它们：两者各自生成条件，可以同时成立。
// 例如 statusFailureCodes 把单值 Success 固定为 false（只看失败），调用方再给
// Dimensions.Success = true 时两个条件都在，结果是空集——这正是「筛成功的请求里有哪些
// 失败状态码」的正确答案，不是 bug。
//
// 嵌入这些结构后，单值的 Success 与 Dimensions.Success 同名：Go 的规则是外层字段遮蔽
// 被嵌入的同名字段，所以 `f.Success` 恒指参照实现那个单值参数，多值维度那个必须写全
// `f.Dimensions.Success`。这是有意的——已发布参数的写法一个字都不用改。

// UpstreamKey 是一把上游 Key 的身份：供应商 + Key 名。
//
// 必须成对：key_name 只在**同一个供应商内**唯一，两家都叫 main 的 Key 是两把 Key
// （与 keyusage.go 按 (provider_id, key_name) 分组同一条理由）。只按 key_name 筛会把别家
// 的同名 Key 一起带进来。
type UpstreamKey struct {
	ProviderID string
	KeyName    string
}

// Dimensions 是一组多值筛选条件（本项目新增，参照实现没有）。
//
// 每个切片为空与 nil 等价（都不追加条件）；Success 为 nil 表示不按成败筛。顺序固定，
// 也是 SQL 里条件与参数的追加顺序：供应商、访问密钥（最早的两个维度），然后是模型、
// 上游 Key、工作空间、调用方档位、成败。
type Dimensions struct {
	// ProviderIDs 筛 provider_id（转发到哪一家）。历史行的 provider_id 是 NULL，
	// `IN (…)` 对 NULL 恒不成立，因此按供应商筛时它们自然被排除。
	ProviderIDs []string
	// AccessKeyIDs 筛发起请求的访问密钥（request_access_key 旁挂表）。完整权限、工作空间
	// 凭据与历史行都没有归属，不会被任何一把 key 算进去。
	AccessKeyIDs []string
	// ModelIDs 筛 model_id，即**解析后的本地路由模型**（别名、任务名都已归一到它），
	// 不是调用方请求里写的名字（那是 requested_model_id）。
	ModelIDs []string
	// UpstreamKeys 筛实际转发用的上游 Key（成对，见 UpstreamKey）。
	UpstreamKeys []UpstreamKey
	// Workspaces 筛工作空间归属（request_workspace 旁挂表）。没有归属的行（历史行、
	// 不走 proxy 的写入）不属于任何工作空间——不会被兜底算进 default。
	Workspaces []string
	// CallerTypes 筛 caller_type（local / workspace / access_key）。取值域由服务端校验，
	// 存储层收到别的字符串只是筛不出行，不报错。
	CallerTypes []string
	// Success 筛成败（`success = 1/0`）。与参照实现的单值 Success 并存，见文件头。
	Success *bool
}

// dimensionColumns 是一套维度条件的列写法。
//
// 主表是裸 request_metrics 的取数路径（metricFilter、queryStats、snapshotRollup）与主表
// 带别名 m、访问密钥走 LEFT JOIN 的 KeyUsage 写法不同，但条件的**种类与顺序**必须一致
// ——两套写法共用 appendDimensionColumns，差别只在列名与两个旁挂表维度的包装上。
type dimensionColumns struct {
	providerID, modelID, keyName, callerType, success string
	// accessKeyOpen/Close、workspaceOpen/Close 包住 `?, ?, …` 占位符。
	accessKeyOpen, accessKeyClose string
	workspaceOpen, workspaceClose string
}

// bareDimensionColumns 是主表为裸 request_metrics 时的写法。
//
// 两个旁挂表维度用子查询而不是 JOIN：这些查询的主表一律是裸 `request_metrics`，加 JOIN
// 会动到既有 SQL 的形状；子查询只引用旁挂表的 request_id，不会与主表的 id 产生歧义（旁挂
// 表都只有 request_id 一列做主键，没有 id 列）。即便外层另外 LEFT JOIN 了同一张旁挂表
// （UsageBreakdown 按工作空间分组时就是这样），子查询里未限定的 workspace / request_id
// 也按 SQL 的作用域规则先绑到子查询自己的 FROM 上，不会串到外层。
var bareDimensionColumns = dimensionColumns{
	providerID:     "provider_id",
	modelID:        "model_id",
	keyName:        "key_name",
	callerType:     "caller_type",
	success:        "success",
	accessKeyOpen:  "id IN (SELECT request_id FROM request_access_key WHERE access_key_id IN (",
	accessKeyClose: "))",
	workspaceOpen:  "id IN (SELECT request_id FROM request_workspace WHERE workspace IN (",
	workspaceClose: "))",
}

// keyUsageDimensionColumns 是 KeyUsage 那张 `request_metrics m LEFT JOIN
// request_access_key a` 查询的写法。
//
// 访问密钥直接过滤 a.access_key_id（JOIN 已经在那里，不需要再套一层子查询）；工作空间
// 没有被 JOIN，仍走子查询，但外层的 id 必须写成 m.id——主表带了别名。
var keyUsageDimensionColumns = dimensionColumns{
	providerID:     "m.provider_id",
	modelID:        "m.model_id",
	keyName:        "m.key_name",
	callerType:     "m.caller_type",
	success:        "m.success",
	accessKeyOpen:  "a.access_key_id IN (",
	accessKeyClose: ")",
	workspaceOpen:  "m.id IN (SELECT request_id FROM request_workspace WHERE workspace IN (",
	workspaceClose: "))",
}

// appendDimensionFilters 把多值筛选维度追加到 filters / parameters 末尾（裸主表写法）。
//
// 三处调用（metricFilter.sql、queryStats、snapshotRollup）的主表都是**裸**
// request_metrics（无别名、无 JOIN），条件形状因此可以共用；KeyUsage 那边主表带别名、
// 访问密钥走 LEFT JOIN，用 appendKeyUsageDimensionFilters。
//
// 顺序固定：供应商、访问密钥、模型、上游 Key、工作空间、调用方档位、成败。前两个的位置
// 与只有两个维度时一致，TestMetricFilterDimensionConditionsAppendedLast 锁着它；全部为空
// 时不追加任何条件——未筛选路径的 SQL 因此逐字节不变。
func appendDimensionFilters(filters []string, parameters []any, dims Dimensions) ([]string, []any) {
	return appendDimensionColumns(filters, parameters, dims, bareDimensionColumns)
}

// appendKeyUsageDimensionFilters 是 appendDimensionFilters 的 KeyUsage 写法（见
// keyUsageDimensionColumns）。条件种类与顺序完全相同。
func appendKeyUsageDimensionFilters(filters []string, parameters []any, dims Dimensions) ([]string, []any) {
	return appendDimensionColumns(filters, parameters, dims, keyUsageDimensionColumns)
}

// appendDimensionColumns 按给定列写法追加全部维度条件。
func appendDimensionColumns(
	filters []string,
	parameters []any,
	dims Dimensions,
	columns dimensionColumns,
) ([]string, []any) {
	if len(dims.ProviderIDs) > 0 {
		filters = append(filters, columns.providerID+" IN ("+placeholders(len(dims.ProviderIDs))+")")
		parameters = append(parameters, stringParams(dims.ProviderIDs)...)
	}
	if len(dims.AccessKeyIDs) > 0 {
		filters = append(filters,
			columns.accessKeyOpen+placeholders(len(dims.AccessKeyIDs))+columns.accessKeyClose)
		parameters = append(parameters, stringParams(dims.AccessKeyIDs)...)
	}
	if len(dims.ModelIDs) > 0 {
		filters = append(filters, columns.modelID+" IN ("+placeholders(len(dims.ModelIDs))+")")
		parameters = append(parameters, stringParams(dims.ModelIDs)...)
	}
	if len(dims.UpstreamKeys) > 0 {
		// 成对条件不能写成 `provider_id IN (…) AND key_name IN (…)`：那是笛卡尔积，筛
		// (openai, main) 与 (azure, backup) 会顺手把 (openai, backup) 也带进来。外层括号
		// 恒在（单对时也有），让它与其它条件之间的 AND 不受 OR 的优先级影响。
		pairs := make([]string, 0, len(dims.UpstreamKeys))
		pairCondition := "(" + columns.providerID + " = ? AND " + columns.keyName + " = ?)"
		for _, key := range dims.UpstreamKeys {
			pairs = append(pairs, pairCondition)
			parameters = append(parameters, key.ProviderID, key.KeyName)
		}
		filters = append(filters, "("+strings.Join(pairs, " OR ")+")")
	}
	if len(dims.Workspaces) > 0 {
		filters = append(filters,
			columns.workspaceOpen+placeholders(len(dims.Workspaces))+columns.workspaceClose)
		parameters = append(parameters, stringParams(dims.Workspaces)...)
	}
	if len(dims.CallerTypes) > 0 {
		filters = append(filters, columns.callerType+" IN ("+placeholders(len(dims.CallerTypes))+")")
		parameters = append(parameters, stringParams(dims.CallerTypes)...)
	}
	if dims.Success != nil {
		filters = append(filters, columns.success+" = ?")
		// 与 metricFilter 的单值 Success 同一写法：显式分支，别依赖 bool→int 转换。
		if *dims.Success {
			parameters = append(parameters, int64(1))
		} else {
			parameters = append(parameters, int64(0))
		}
	}
	return filters, parameters
}

// dimensionsEcho 渲染响应里的 filters 回显。
//
// 界面据此回显当前条件（chip），不必把 URL 再解析一遍。键序固定：最早的 provider_ids /
// access_key_ids 在前（只有两个维度时就是这两个键，读端按名字取值，新键追加在后不影响
// 它们），然后按 Dimensions 的字段顺序。
//
// 未设的维度是 `[]` 而不是 null（「这个维度不设限」，见 stringArray）；success 是唯一的
// 例外——它是三态（不筛 / 只看成功 / 只看失败），`[]` 表达不了，因此未设时是 null。
// upstream_keys 回成对象数组而不是 `["p","k"]` 二元组：参数里用 JSON 数组只是因为
// 供应商 ID 与 Key 名都可能含任何分隔符，响应没有这个约束，按名字取值更不容易读反。
func dimensionsEcho(dims Dimensions) *canonical.Value {
	upstreamKeys := make([]*canonical.Value, 0, len(dims.UpstreamKeys))
	for _, key := range dims.UpstreamKeys {
		upstreamKeys = append(upstreamKeys, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "provider_id", Value: canonical.NewString(key.ProviderID)},
			canonical.ObjectPair{Key: "key_name", Value: canonical.NewString(key.KeyName)},
		))
	}
	success := canonical.NewNull()
	if dims.Success != nil {
		success = canonical.NewBool(*dims.Success)
	}
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "provider_ids", Value: stringArray(dims.ProviderIDs)},
		canonical.ObjectPair{Key: "access_key_ids", Value: stringArray(dims.AccessKeyIDs)},
		canonical.ObjectPair{Key: "model_ids", Value: stringArray(dims.ModelIDs)},
		canonical.ObjectPair{Key: "upstream_keys", Value: canonical.NewArray(upstreamKeys...)},
		canonical.ObjectPair{Key: "workspaces", Value: stringArray(dims.Workspaces)},
		canonical.ObjectPair{Key: "caller_types", Value: stringArray(dims.CallerTypes)},
		canonical.ObjectPair{Key: "success", Value: success},
	)
}
