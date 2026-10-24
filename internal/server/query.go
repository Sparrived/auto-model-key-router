package server

import (
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
)

// 本文件复刻 FastAPI + Pydantic v2 对**查询参数**的解析与校验。
//
// 为什么不复用 internal/api 的实现：那一套（api/validate.go）针对的是请求体，
// 它的 input 是解析后的 JSON 值、错误 loc 是 ["body", ...]，而查询参数的 input 是
// **原始字符串**、loc 是 ["query", <name>]，两者在 422 响应体里是不同的字节，不能
// 共用。
//
// 覆盖范围刻意收窄：只做三条 /metrics* 路由用到的形态（float / int / bool / str /
// Literal，以及 gt/ge/le/min_length/max_length）。参照实现里没有别的查询参数。
//
// 三条容易写错的语义（都是参照实现当年用真实 Python 应用钉住的行为，刻意保留）：
//
//  1. **重复参数取最后一个**。Starlette 的 QueryParams.__getitem__ 取列表末位
//     （`?hours=1&hours=2` 得到 2.0），而 Go 的 url.Values.Get 取第一位——必须显式取
//     末位，否则多值请求会静默选错值。唯一的例外是 qStrList / qPairList：/ui 读数的多值
//     筛选维度**故意**收齐全部出现值（维度内多选是它们的语义），见 qStrList 的说明。
//  2. **校验先于鉴权**。FastAPI 在调用路由函数之前解析依赖与参数，所以
//     `GET /metrics?hours=0`（不带任何凭据）是 422 而不是 401。装配层必须保持这个
//     顺序。
//  3. **未知参数被忽略**。FastAPI 不校验未声明的查询参数，因此
//     `/metrics/series?all_history=true` 既不是 422 也不是 200 带 all_history，
//     而是完全忽略它（series 根本没有这个参数）。
//
// 已知的**有意收紧**（无法在 Go 侧逐字复刻，因此刻意不追求与参照实现逐字一致）：
//
//   - 只认 ASCII 空白与 ASCII 数字。Python 的 float() 还接受 Unicode 数字
//     （float("١٢") == 12.0）与非 ASCII 空白，这里统一按解析失败处理。
//   - Go 的 strconv.ParseFloat 接受十六进制浮点（"0x1p-2"），而 Python 的 float()
//     不接受；见 parseFloatQuery 里显式挡掉的那一步。

// qKind 是查询参数的校验类型。
type qKind int

const (
	qFloat qKind = iota
	qInt
	qBool
	qStr
	qLiteral
	// qStrList 是**可重复出现**的字符串参数：同名参数的每一次出现都是一个独立取值
	// （维度内多选，见 validateQuery）。声明了 literals 时每一项按 Literal 校验
	// （caller_type），否则按 minLen/maxLen 校验。
	//
	// 只有本项目自有的 /ui 筛选维度用它：/metrics 系列的参数一律保持「取末位」，那是
	// 已发布行为（对齐 FastAPI 的单值语义）。
	qStrList
	// qPairList 是可重复出现的**成对**字符串参数（upstream_key）：每一项是恰好两个非空
	// 字符串的 JSON 数组 `["<provider_id>","<key_name>"]`。
	//
	// 为什么用 JSON 而不是分隔符：供应商 ID 与 Key 名都是配置里的任意字符串，任何分隔符
	// （`/`、`:`、`|`）都可能出现在其中，拆分就有二义性；JSON 字符串的转义规则是现成且
	// 无歧义的。每一部分仍按 minLen/maxLen（按字符数）校验，与 listTextParam 同一口径。
	qPairList
)

// qParam 描述一个查询参数，字段顺序即响应里错误的顺序（Pydantic 按声明顺序报错）。
type qParam struct {
	name string
	kind qKind
	// def 是缺省值；nil 表示 `T | None = None`（缺省即 null）。
	def *canonical.Value
	// 数值边界。字面量的 int/float 之别会原样进入 422 的 ctx
	// （`{"le":8760.0}` 与 `{"le":200}` 是不同的字节），因此直接存 canonical 值。
	ge, gt, le, lt *canonical.Value
	// minLen / maxLen 是 min_length / max_length（按**字符数**而不是字节数）。
	minLen, maxLen int
	// literals 是 Literal 的取值集合。
	literals []string
}

// 复用的边界字面量。它们是只读的（校验过程绝不改写），因此可以共享。
var (
	qFloat0    = canonical.NewFloat(0)
	qFloat8760 = canonical.NewFloat(8760)
	qFloat720  = canonical.NewFloat(720)
	qInt1      = canonical.NewInt("1")
	qInt15     = canonical.NewInt("15")
	qInt50     = canonical.NewInt("50")
	qInt100    = canonical.NewInt("100")
	qInt200    = canonical.NewInt("200")
	qInt599    = canonical.NewInt("599")
	qInt86400  = canonical.NewInt("86400")
	qBoolFalse = canonical.NewBool(false)
	qFloat24   = canonical.NewFloat(24)
	qFloatOne  = canonical.NewFloat(1)
	qInt0      = canonical.NewInt("0")
	qInt60     = canonical.NewInt("60")
	// 三档：本机（local_api_key）、工作空间推理凭据、访问密钥。后两者是 Go 侧新增
	// （参照实现只有 local 与已删除的 visitor 两档）。
	//
	// 访问密钥取代了 visitor 档：两者都是「分发给外部使用者的受限凭据」，但访问密钥
	// 一人一把、各带清单，因此指标里分开统计才有意义。老库里残留的 visitor 行不再
	// 出现在过滤器里（值域即契约，见 qLiteral）。
	//
	// 取值直接引用 metrics.CallerTypes：/ui/metric-dimensions.json 的筛选器选项按它补齐，
	// 两边抄两份迟早漂移成「选项里有、参数校验却拒绝」。取值与顺序和原先的字面量完全
	// 相同，/metrics/requests 与 /metrics/series 的 literal_error 文案因此逐字节不变。
	callerTypes = metrics.CallerTypes
	// qInt8 是 usage-breakdown 的 limit 缺省值（保留的分组数）。
	qInt8 = canonical.NewInt("8")
)

// metricsSnapshotParams 对应 app.py:212-216 的 GET /metrics 签名。
var metricsSnapshotParams = []qParam{
	{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	{name: "all_history", kind: qBool, def: qBoolFalse},
}

// workspaceUsageParams 是 GET /ui/workspace-usage.json 的签名。
//
// 与 /metrics 用同一对参数（hours + all_history），让界面上的时间选择器对两个
// 读数语义一致。**有意增补**的读数：参照实现没有工作空间，因此没有对应签名可比对，
// 这里的取值方式只是复用既有解析器，不构成兼容性声明。
var workspaceUsageParams = []qParam{
	{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	{name: "all_history", kind: qBool, def: qBoolFalse},
}

// keyUsageParams 是 GET /ui/key-usage.json 的签名。
//
// 同样复用 hours + all_history 这一对，让「哪把 Key 出去了」的读数与 /metrics、
// workspace-usage 的时间选择器语义一致。**有意增补**的读数：参照实现没有按 Key 拆分
// 的口径，因此没有对应签名可比对，这里的取值方式只是复用既有解析器，不构成兼容性声明。
//
// 七个筛选维度（dimensionFilterParams）都是可省的多值过滤：同名参数重复出现是维度内
// OR、维度之间是 AND，都不给时与加这些参数之前完全一致（三份拆分与 unattributed 都是
// 全量口径）。
var keyUsageParams = append([]qParam{
	{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	{name: "all_history", kind: qBool, def: qBoolFalse},
}, dimensionFilterParams()...)

// dimensionFilterParams 是 /ui 读数共用的七个筛选维度参数（存储层的 metrics.Dimensions）。
//
// 声明顺序即 422 的报错顺序，也与 Dimensions 的字段顺序一致：最早的 provider_id /
// access_key_id 在前（只有两个维度时它们就排在这两个位置），然后是 model_id、
// upstream_key、workspace、caller_type、success。
//
//   - provider_id / access_key_id / model_id / workspace：可重复出现的字符串，每项
//     1..512 个字符；
//   - upstream_key：可重复出现的 `["<provider_id>","<key_name>"]` JSON 数组（见 qPairList）；
//   - caller_type：可重复出现的 Literal，取值域与 /metrics/requests 的 caller_type 相同；
//   - success：可省的单值布尔（三态：不给 = 不筛）。
//
// 每次调用返回一份新切片：几条路由会在它前后拼上自己的参数，共用底层数组会互相覆盖。
func dimensionFilterParams() []qParam {
	return []qParam{
		listTextParam("provider_id"),
		listTextParam("access_key_id"),
		listTextParam("model_id"),
		{name: "upstream_key", kind: qPairList, minLen: 1, maxLen: 512},
		listTextParam("workspace"),
		{name: "caller_type", kind: qStrList, literals: callerTypes},
		{name: "success", kind: qBool},
	}
}

// filteredMetricsParams 是 GET /ui/filtered-metrics.json 的签名。
//
// 与 /metrics 系列有两处刻意的差别：
//
//   - 七个筛选维度（dimensionFilterParams）都是**可重复出现**的多值参数（维度内 OR、
//     维度间 AND；success 是单值三态），且**都可省**：全都不给就是全实例读数。这条路径
//     保留着，因为「不筛」与「筛一个不存在的值」必须是两件不同的事——前者回全量、后者
//     回空。
//   - bucket_seconds 默认 **0 = 自动**：/metrics/series 要求调用方自己按跨度挑桶，
//     24 小时配 60 秒桶是 1441 点、直接超上限被拒；整页读数把概览、趋势与明细拼在
//     一起，默认交给服务端选最小的合适桶（响应里回真实桶宽）。显式给值时仍走
//     /metrics/series 那一套（15..86400，点数超限报 422）。
//
// 注意 0 只能来自缺省：validateQuery 与 Pydantic 一样**不校验缺省值**，所以下界写
// 15 不会把默认值判成非法，而 `?bucket_seconds=0` 这种显式传法照旧报 422——「自动」
// 是没说，不是说了个 0。
var filteredMetricsParams = append(dimensionFilterParams(),
	qParam{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	qParam{name: "all_history", kind: qBool, def: qBoolFalse},
	qParam{name: "bucket_seconds", kind: qInt, def: qInt0, ge: qInt15, le: qInt86400},
	qParam{name: "limit", kind: qInt, def: qInt0, ge: qInt0, le: qInt200},
)

// statusGroupByLiterals 是状态监控矩阵 group_by 的取值域。
//
// 直接引用 metrics 包的常量而不是再抄一遍字面量：这三个值是**对外契约**，服务端校验
// 与指标层的分组实现必须认同同一组字符串，抄两份迟早会漂移成「校验通过但指标层
// 报错」这种最难查的错。
var statusGroupByLiterals = []string{
	metrics.StatusGroupByProvider,
	metrics.StatusGroupByProviderModel,
	metrics.StatusGroupByProviderKey,
}

// statusMonitorParams 是 GET /ui/status-monitor.json 的签名。
//
// 七个筛选维度与 /ui/filtered-metrics.json 逐字同义（dimensionFilterParams：可重复出现、
// 维度内 OR、维度间 AND、都可省），hours 的上界也一致（8760）。与 filtered-metrics 的
// 差别有两处：
//
//   - **没有 all_history**：矩阵把窗口切成桶，没有起点就没有列，因此窗口恒封口；
//   - **多了 group_by**：矩阵的行身份由它决定（供应商 / 供应商+上游模型 /
//     供应商+上游 Key），缺省 provider。
//
// bucket_seconds 同样默认 0 = 自动：`?bucket_seconds=0` 这种显式传法照旧 422——
// 「自动」是没说，不是说了个 0（validateQuery 与 Pydantic 都不校验缺省值）。
var statusMonitorParams = append(dimensionFilterParams(),
	qParam{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	qParam{name: "bucket_seconds", kind: qInt, def: qInt0, ge: qInt15, le: qInt86400},
	qParam{name: "group_by", kind: qLiteral, def: canonical.NewString(metrics.StatusGroupByProvider),
		literals: statusGroupByLiterals},
)

// metricDimensionsParams 是 GET /ui/metric-dimensions.json（筛选器选项）的签名。
//
// 只有时间窗口，与 key-usage 同一对 hours + all_history：选项是**不筛**的读数（筛了以后
// 选项会随当前条件消失，筛选器把自己锁死，见 metrics/metricdimensions.go），因此刻意
// 不接受任何维度参数——传了也按未知参数忽略（见文件顶部第 3 条）。
var metricDimensionsParams = []qParam{
	{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	{name: "all_history", kind: qBool, def: qBoolFalse},
}

// usageBreakdownParams 是 GET /ui/usage-breakdown.json（按维度拆分的趋势）的签名。
//
// 筛选维度与时间参数和 /ui/filtered-metrics.json 逐字同义（同一份 dimensionFilterParams、
// 同一组 hours / all_history / bucket_seconds 的边界与缺省，bucket_seconds 缺省 0 = 自动），
// 两条读数对同一组参数因此落在同一张桶网格上。多出三个：
//
//   - group_by：按哪个维度拆，取值域引用 metrics.UsageBreakdownGroupByValues，缺省 model；
//   - rank_by：按 requests 还是 tokens（total_tokens）排名，缺省 requests；
//   - limit：保留前几组（1..50，缺省 8），其余并进 other。
var usageBreakdownParams = append(dimensionFilterParams(),
	qParam{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	qParam{name: "all_history", kind: qBool, def: qBoolFalse},
	qParam{name: "bucket_seconds", kind: qInt, def: qInt0, ge: qInt15, le: qInt86400},
	qParam{name: "group_by", kind: qLiteral, def: canonical.NewString(metrics.UsageBreakdownGroupByModel),
		literals: metrics.UsageBreakdownGroupByValues},
	qParam{name: "rank_by", kind: qLiteral, def: canonical.NewString(metrics.UsageBreakdownRankByRequests),
		literals: metrics.UsageBreakdownRankByValues},
	qParam{name: "limit", kind: qInt, def: qInt8, ge: qInt1, le: qInt50},
)

// accessKeyUsageParams 是 GET /ui/access-key-usage.json（访客看板）的签名。
//
// 同样复用 hours + all_history 这一对，让看板与 /metrics、workspace-usage 的时间
// 选择器语义一致。
//
// **刻意没有 key_id 之类的参数**：要查哪一把 key 完全由 Bearer 凭据决定。加一个
// 这样的参数就等于让任何一把 key 读别人的用量（见 accesskey_usage.go 的说明）。
var accessKeyUsageParams = []qParam{
	{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	{name: "all_history", kind: qBool, def: qBoolFalse},
}

// requestHistoryParams 对应 app.py:235-251 的 GET /metrics/requests 签名。
//
// 声明顺序与 app.py 一致：hours、all_history、caller_type、六个字符串过滤、
// status_code、success、attributed、limit、before_id。
var requestHistoryParams = []qParam{
	{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat720},
	{name: "all_history", kind: qBool, def: qBoolFalse},
	{name: "caller_type", kind: qLiteral, literals: callerTypes},
	filterTextParam("model_id"),
	filterTextParam("requested_model_id"),
	filterTextParam("provider_id"),
	filterTextParam("pool_name"),
	filterTextParam("upstream_model_id"),
	filterTextParam("key_name"),
	{name: "status_code", kind: qInt, ge: qInt100, le: qInt599},
	{name: "success", kind: qBool},
	{name: "attributed", kind: qBool},
	{name: "limit", kind: qInt, def: qInt50, ge: qInt1, le: qInt200},
	{name: "before_id", kind: qInt, ge: qInt1},
}

// metricsSeriesParams 对应 app.py:280-293 的 GET /metrics/series 签名。
//
// 注意 series **没有** all_history（app.py 的系列接口永远用 hours，全量会让
// SQLite 扫全表并生成几十万个点位）。
//
// hours 的上界这里是 8760（1 年）而不是参照实现的 720（30 天）：**有意的放宽**，
// 让「用量统计」页能画到年与全量。放宽是安全的，因为点数上限（MaxSeriesPoints）
// 与它无关——`seriesPointLimitExceeded` 仍然独立把关，长窗口只能配粗桶
// （1 年 = 366 个日桶，仍在 500 以内；想拿 1 年配 15 秒桶照样是 422）。
// 这条边界是有意偏离参照实现的，放宽不会破坏既有一年以内窗口的行为
// （`hours=0`、`bucket_seconds` 越界与点数超限的拒绝都原样保留）；
// `/metrics/requests` 仍保持 720。
var metricsSeriesParams = []qParam{
	{name: "hours", kind: qFloat, def: qFloatOne, gt: qFloat0, le: qFloat8760},
	{name: "bucket_seconds", kind: qInt, def: qInt60, ge: qInt15, le: qInt86400},
	{name: "caller_type", kind: qLiteral, literals: callerTypes},
	filterTextParam("model_id"),
	filterTextParam("requested_model_id"),
	filterTextParam("provider_id"),
	filterTextParam("pool_name"),
	filterTextParam("upstream_model_id"),
	filterTextParam("key_name"),
	{name: "status_code", kind: qInt, ge: qInt100, le: qInt599},
	{name: "success", kind: qBool},
	{name: "attributed", kind: qBool},
}

// filterTextParam 构造一个可省的字符串过滤参数：`str | None = Query(default=None,
// min_length=1, max_length=512)`（app.py:241-246）。
func filterTextParam(name string) qParam {
	return qParam{name: name, kind: qStr, minLen: 1, maxLen: 512}
}

// listTextParam 构造一个**可重复出现**的字符串过滤参数：每一项按 `min_length=1,
// max_length=512` 校验，缺省是空列表。
//
// 与 filterTextParam 的唯一区别在「同名参数出现多次」时：那个取末位（对齐 FastAPI 的
// 单值语义，是已发布行为），这个收齐全部出现值。
func listTextParam(name string) qParam {
	return qParam{name: name, kind: qStrList, minLen: 1, maxLen: 512}
}

// queryValue 是校验后的参数值。
type queryValue struct {
	kind qKind
	// null 表示「缺省且类型可空」，即 Python 的 None。
	null bool
	// num 用于 qFloat。
	num float64
	// integer 用于 qInt：整数不能走 float64 中转，否则 int64 边界值会在回转时溢出。
	integer int64
	text    string // qStr / qLiteral
	// texts 用于 qStrList：同名参数的全部出现值，缺省为空。
	texts []string
	// pairs 用于 qPairList：每一项解析出的 [供应商, Key 名]，缺省为空。
	pairs [][2]string
	flag  bool // qBool
}

// queryResult 是整条路由的校验结果。
type queryResult struct {
	values map[string]queryValue
}

// queryError 是一条 Pydantic 校验错误。
//
// 渲染顺序固定为 type、loc、msg、input、ctx，与 internal/api 的同名结构一致；
// input 对查询参数永远是**原始字符串**（Pydantic 报的是进入该字段校验器之前的值）。
// 查询侧没有必填参数（每个参数都有缺省值），因此不存在缺失参数那种 input 为 null 的
// 错误——那一档在请求体校验里（internal/api），与本结构无关。
type queryError struct {
	name  string
	typ   string
	msg   string
	input string
	ctx   *canonical.Value
}

// toValue 渲染成 JSON 对象。
func (e queryError) toValue() *canonical.Value {
	item := canonical.NewObject()
	item.SetKey("type", canonical.NewString(e.typ))
	item.SetKey("loc", canonical.NewArray(
		canonical.NewString("query"), canonical.NewString(e.name)))
	item.SetKey("msg", canonical.NewString(e.msg))
	item.SetKey("input", canonical.NewString(e.input))
	if e.ctx != nil && e.ctx.Obj.Len() > 0 {
		item.SetKey("ctx", e.ctx)
	}
	return item
}

// validateQuery 按声明顺序校验查询参数。
//
// 返回的 errs 为空时 values 里每个参数都有一个值（缺省值或 null）。
func validateQuery(raw url.Values, specs []qParam) (*queryResult, []queryError) {
	result := &queryResult{values: make(map[string]queryValue, len(specs))}
	var errs []queryError
	for _, spec := range specs {
		texts, present := raw[spec.name]
		if !present || len(texts) == 0 {
			result.values[spec.name] = defaultValue(spec)
			continue
		}
		// 取末位：见文件顶部第 1 条。
		if spec.kind == qStrList {
			// 列表参数是这条规则的**例外**：同名参数重复出现就是维度内多选，因此收齐
			// 全部出现值。逐项走同一套单值校验（空串仍是 string_too_short，Literal 列表
			// 的非法项仍是 literal_error），任一项非法就以**那一项**的原文作为 input 报错
			// 并停止——一次请求里同一个参数报一堆同型错误没有额外信息，而「哪一项错了」
			// 正是最难自查的那个。
			itemSpec := qParam{name: spec.name, kind: qStr, minLen: spec.minLen, maxLen: spec.maxLen}
			if len(spec.literals) > 0 {
				itemSpec = qParam{name: spec.name, kind: qLiteral, literals: spec.literals}
			}
			items := make([]string, 0, len(texts))
			var firstErr *queryError
			for _, text := range texts {
				value, queryErr := parseQueryValue(itemSpec, text)
				if queryErr != nil {
					firstErr = queryErr
					break
				}
				items = append(items, value.text)
			}
			if firstErr != nil {
				errs = append(errs, *firstErr)
				continue
			}
			result.values[spec.name] = queryValue{kind: qStrList, texts: items}
			continue
		}
		if spec.kind == qPairList {
			// 与 qStrList 同一条例外与同一套报错取舍：收齐全部出现值，第一项非法即停。
			pairs := make([][2]string, 0, len(texts))
			var firstErr *queryError
			for _, text := range texts {
				pair, queryErr := parsePairQuery(spec, text)
				if queryErr != nil {
					firstErr = queryErr
					break
				}
				pairs = append(pairs, pair)
			}
			if firstErr != nil {
				errs = append(errs, *firstErr)
				continue
			}
			result.values[spec.name] = queryValue{kind: qPairList, pairs: pairs}
			continue
		}
		text := texts[len(texts)-1]
		value, queryErr := parseQueryValue(spec, text)
		if queryErr != nil {
			errs = append(errs, *queryErr)
			continue
		}
		result.values[spec.name] = value
	}
	return result, errs
}

// defaultValue 返回缺省值。
func defaultValue(spec qParam) queryValue {
	if spec.def == nil {
		return queryValue{kind: spec.kind, null: true}
	}
	switch spec.def.Kind {
	case canonical.KindBool:
		return queryValue{kind: spec.kind, flag: spec.def.Bool}
	case canonical.KindString:
		return queryValue{kind: spec.kind, text: spec.def.Str}
	}
	if spec.kind == qInt {
		// 缺省值来自本文件的字面量常量，解析失败不可能发生；失败时保守地当成 0。
		integer, _ := strconv.ParseInt(spec.def.Num, 10, 64)
		return queryValue{kind: spec.kind, integer: integer}
	}
	number, _ := spec.def.AsFloat()
	return queryValue{kind: spec.kind, num: number}
}

// parseQueryValue 解析一个参数值；返回 nil 错误表示通过。
func parseQueryValue(spec qParam, text string) (queryValue, *queryError) {
	switch spec.kind {
	case qFloat:
		value, ok := parseFloatQuery(text)
		if !ok {
			return queryValue{}, &queryError{
				name: spec.name, typ: "float_parsing", input: text,
				msg: "Input should be a valid number, unable to parse string as a number",
			}
		}
		if queryErr := checkNumericBounds(spec, value, text); queryErr != nil {
			return queryValue{}, queryErr
		}
		return queryValue{kind: qFloat, num: value}, nil
	case qInt:
		value, ok := parseIntQuery(text)
		if !ok {
			return queryValue{}, &queryError{
				name: spec.name, typ: "int_parsing", input: text,
				msg: "Input should be a valid integer, unable to parse string as an integer",
			}
		}
		if queryErr := checkNumericBounds(spec, float64(value), text); queryErr != nil {
			return queryValue{}, queryErr
		}
		return queryValue{kind: qInt, integer: value}, nil
	case qBool:
		value, ok := parseBoolQuery(text)
		if !ok {
			return queryValue{}, &queryError{
				name: spec.name, typ: "bool_parsing", input: text,
				msg: "Input should be a valid boolean, unable to interpret input",
			}
		}
		return queryValue{kind: qBool, flag: value}, nil
	case qLiteral:
		for _, candidate := range spec.literals {
			if candidate == text {
				return queryValue{kind: qLiteral, text: text}, nil
			}
		}
		expected := literalExpectation(spec.literals)
		ctx := canonical.NewObject()
		ctx.SetKey("expected", canonical.NewString(expected))
		return queryValue{}, &queryError{
			name: spec.name, typ: "literal_error", input: text,
			msg: "Input should be " + expected, ctx: ctx,
		}
	case qStr:
		// 字符串不 strip：Pydantic 的 str 默认不改写内容，只按字符数校验长度。
		// （实测 `?model_id=%20` 通过且过滤值是单个空格。）
		length := len([]rune(text))
		if spec.minLen > 0 && length < spec.minLen {
			ctx := canonical.NewObject()
			ctx.SetKey("min_length", canonical.NewInt(strconv.Itoa(spec.minLen)))
			return queryValue{}, &queryError{
				name: spec.name, typ: "string_too_short", input: text,
				msg: "String should have at least " + strconv.Itoa(spec.minLen) +
					plural("character", spec.minLen),
				ctx: ctx,
			}
		}
		if spec.maxLen > 0 && length > spec.maxLen {
			ctx := canonical.NewObject()
			ctx.SetKey("max_length", canonical.NewInt(strconv.Itoa(spec.maxLen)))
			return queryValue{}, &queryError{
				name: spec.name, typ: "string_too_long", input: text,
				msg: "String should have at most " + strconv.Itoa(spec.maxLen) +
					plural("character", spec.maxLen),
				ctx: ctx,
			}
		}
		return queryValue{kind: qStr, text: text}, nil
	}
	return queryValue{kind: spec.kind, null: true}, nil
}

// parsePairQuery 解析一项 qPairList：恰好两个字符串的 JSON 数组，每一部分按字符数落在
// [minLen, maxLen] 内。
//
// 报错是 Pydantic 自定义校验器的那一档（type=value_error，msg 以 "Value error, " 开头），
// loc 仍是 ["query", <name>]、input 是**这一项的原文**——与 qStrList 的逐项报错同形。
// 不细分「不是 JSON」「不是数组」「元素不是字符串」「长度不对」几种情形：调用方要改成
// 的形状只有一种，一句完整的期望形状比四种错误码更好自查。不带 ctx：FastAPI 把
// value_error 的 ctx 渲染成一个空对象，没有信息量。
func parsePairQuery(spec qParam, text string) ([2]string, *queryError) {
	invalid := &queryError{
		name: spec.name, typ: "value_error", input: text,
		msg: "Value error, " + spec.name + " must be a JSON array of exactly two non-empty strings " +
			`["<provider_id>","<key_name>"], each at most ` + strconv.Itoa(spec.maxLen) + " characters",
	}
	var items []any
	if err := json.Unmarshal([]byte(text), &items); err != nil || len(items) != 2 {
		// null 也走这里：Unmarshal 把它解成 nil 切片，长度 0。
		return [2]string{}, invalid
	}
	var pair [2]string
	for i, item := range items {
		part, ok := item.(string)
		if !ok {
			return [2]string{}, invalid
		}
		length := len([]rune(part))
		if (spec.minLen > 0 && length < spec.minLen) || (spec.maxLen > 0 && length > spec.maxLen) {
			return [2]string{}, invalid
		}
		pair[i] = part
	}
	return pair, nil
}

// plural 复刻 Pydantic 消息里的单复数（1 用单数，其余用复数）。
func plural(word string, count int) string {
	if count == 1 {
		return " " + word
	}
	return " " + word + "s"
}

// literalExpectation 复刻 Pydantic 对 Literal 的期望文案：用 ", " 连接，最后一个
// 用 " or "（两个取值时即 "'local' or 'visitor'"）。
func literalExpectation(literals []string) string {
	quoted := make([]string, 0, len(literals))
	for _, literal := range literals {
		quoted = append(quoted, "'"+literal+"'")
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

// checkNumericBounds 校验数值边界，并把越界渲染成 Pydantic 的错误。
//
// 比较用 compareNumbers（全序），不是 IEEE 的 < / >：`hours=nan` 在参照实现里报的是
// less_than_equal 而不是 greater_than，只有把 NaN 当作「比任何数都大」才能复现。
func checkNumericBounds(spec qParam, value float64, text string) *queryError {
	report := func(typ, message string, bound *canonical.Value) *queryError {
		ctx := canonical.NewObject()
		ctx.SetKey(boundContextKey(typ), bound)
		return &queryError{name: spec.name, typ: typ, msg: message, input: text, ctx: ctx}
	}
	if spec.ge != nil {
		if bound, ok := spec.ge.AsFloat(); ok && compareNumbers(value, bound) < 0 {
			return report("greater_than_equal",
				"Input should be greater than or equal to "+numberText(spec.ge), spec.ge)
		}
	}
	if spec.gt != nil {
		if bound, ok := spec.gt.AsFloat(); ok && compareNumbers(value, bound) <= 0 {
			return report("greater_than",
				"Input should be greater than "+numberText(spec.gt), spec.gt)
		}
	}
	if spec.le != nil {
		if bound, ok := spec.le.AsFloat(); ok && compareNumbers(value, bound) > 0 {
			return report("less_than_equal",
				"Input should be less than or equal to "+numberText(spec.le), spec.le)
		}
	}
	if spec.lt != nil {
		if bound, ok := spec.lt.AsFloat(); ok && compareNumbers(value, bound) >= 0 {
			return report("less_than",
				"Input should be less than "+numberText(spec.lt), spec.lt)
		}
	}
	return nil
}

// boundContextKey 返回错误类型在 ctx 里用的键名（Pydantic 用约束名）。
func boundContextKey(typ string) string {
	switch typ {
	case "greater_than_equal":
		return "ge"
	case "greater_than":
		return "gt"
	case "less_than_equal":
		return "le"
	case "less_than":
		return "lt"
	}
	return typ
}

// numberText 渲染约束字面量在**消息文本**里的形式。
//
// Pydantic 的 ctx 保留字面量类型（float 约束是 8760.0），消息里却用短写法
// （"Input should be less than or equal to 8760"）。整数约束两者相同。
func numberText(value *canonical.Value) string {
	return strings.TrimSuffix(value.Num, ".0")
}

// compareNumbers 是 f64 的**全序**比较：NaN 大于一切，-0.0 等于 0.0。
//
// 这是 Rust 的 f64::total_cmp 语义（pydantic-core 的约束比较用它）。用 IEEE 的
// `>` 会得到不同结果：`nan > 0` 是 false，于是 `hours=nan` 会先撞上 gt 约束报
// greater_than，而真实 Python 报的是 less_than_equal。
func compareNumbers(left, right float64) int {
	leftNaN, rightNaN := math.IsNaN(left), math.IsNaN(right)
	switch {
	case leftNaN && rightNaN:
		return 0
	case leftNaN:
		return 1
	case rightNaN:
		return -1
	case left < right:
		return -1
	case left > right:
		return 1
	}
	return 0
}

// parseFloatQuery 复刻 Pydantic 的 str -> float 宽松解析。
//
// 与 Python 的 float() 的差别只有两处，都是刻意不追平的分歧（见文件顶部说明）：
//   - 十六进制浮点被显式拒绝（Go 接受 "0x1p-2"，Python 不接受）；
//   - Unicode 数字与空白不支持（Go 只认 ASCII）。
//
// "1e400" 这类超出 float64 范围的值：Python 得到 inf 而**不报错**，Go 会同时返回
// ±Inf 与 ErrRange，因此这里按 Python 语义接受它（随后必然被 le 边界拒绝，与参照
// 实现一致）。
func parseFloatQuery(text string) (float64, bool) {
	trimmed := strings.TrimSpace(text)
	if isHexFloat(trimmed) {
		return 0, false
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return value, true
		}
		return 0, false
	}
	return value, true
}

// isHexFloat 报告字符串是否形如十六进制浮点（可选符号 + 0x/0X）。
func isHexFloat(text string) bool {
	unsigned := strings.TrimPrefix(strings.TrimPrefix(text, "+"), "-")
	return strings.HasPrefix(unsigned, "0x") || strings.HasPrefix(unsigned, "0X")
}

// parseIntQuery 复刻 Pydantic 的 str -> int 宽松解析。
//
// 参照实现的历史行为（刻意保留）：
//
//	"50.0" -> 50    （字符串先当浮点解析，再要求是整数）
//	"1.5"  -> int_parsing（有小数部分就拒绝，注意**不是** int_from_float：
//	                       请求体里 int 字段收到 JSON 浮点才会报 int_from_float）
//	"true" -> int_parsing
//
// 超出 int64 的整数（`before_id=999999999999999999999`）会被钳到边界：Python 的
// int 无上限，随后把它交给 SQLite 会抛 OverflowError 变成 500——那是参照实现的
// 缺陷（未移植）。钳位在语义上等价：唯一没有上界的 int 参数是
// before_id，而它只参与 `id < ?` 比较，"比任何 id 都大" 与真实值结果相同。
func parseIntQuery(text string) (int64, bool) {
	trimmed := strings.TrimSpace(text)
	if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return parsed, true
	}
	value, ok := parseFloatQuery(trimmed)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false
	}
	switch {
	case value >= math.MaxInt64:
		return math.MaxInt64, true
	case value <= math.MinInt64:
		return math.MinInt64, true
	}
	return int64(value), true
}

// boolLiterals 是 Pydantic 宽松 bool 解析接受的字符串（大小写不敏感）。
//
// 取值表用真实 Python 应用实测：t/f/y/n 这些单字母形态**也**被接受
// （`all_history=t` 得到 true）。
var boolLiterals = map[string]bool{
	"true": true, "1": true, "yes": true, "on": true, "t": true, "y": true,
	"false": false, "0": false, "no": false, "off": false, "f": false, "n": false,
}

// parseBoolQuery 复刻 Pydantic 的 str -> bool 宽松解析。
func parseBoolQuery(text string) (bool, bool) {
	value, ok := boolLiterals[strings.ToLower(strings.TrimSpace(text))]
	return value, ok
}

// floatValue 返回浮点参数值。
func (r *queryResult) floatValue(name string) float64 {
	return r.values[name].num
}

// intValue 返回整数参数值。
func (r *queryResult) intValue(name string) int64 {
	return r.values[name].integer
}

// boolValue 返回布尔参数值。
func (r *queryResult) boolValue(name string) bool {
	return r.values[name].flag
}

// stringList 返回列表参数的全部取值；缺省时长度 0。
//
// 不回 nil/空切片的区分：存储层把两者当同一件事（都不追加条件），多一层判空只会让
// 调用方以为它们有区别。
func (r *queryResult) stringList(name string) []string {
	return r.values[name].texts
}

// upstreamKeys 返回 qPairList 参数的全部取值；缺省时长度 0。
func (r *queryResult) upstreamKeys(name string) []metrics.UpstreamKey {
	pairs := r.values[name].pairs
	keys := make([]metrics.UpstreamKey, 0, len(pairs))
	for _, pair := range pairs {
		keys = append(keys, metrics.UpstreamKey{ProviderID: pair[0], KeyName: pair[1]})
	}
	return keys
}

// dimensions 把 dimensionFilterParams 声明的七个参数收成存储层的一组筛选维度。
//
// 只有用 dimensionFilterParams 声明过参数的路由才能调用它：别的路由的 values 里没有这些
// 键，取出来全是零值（等于不筛），不会报错，但那就不是调用方的意图了。
func (r *queryResult) dimensions() metrics.Dimensions {
	return metrics.Dimensions{
		ProviderIDs:  r.stringList("provider_id"),
		AccessKeyIDs: r.stringList("access_key_id"),
		ModelIDs:     r.stringList("model_id"),
		UpstreamKeys: r.upstreamKeys("upstream_key"),
		Workspaces:   r.stringList("workspace"),
		CallerTypes:  r.stringList("caller_type"),
		Success:      r.optionalBool("success"),
	}
}

// stringValue 返回一个字符串 / Literal 参数的值。
//
// 只用于**声明了缺省值**的 Literal（status-monitor 的 group_by、usage-breakdown 的
// group_by / rank_by）：
// 缺省时 defaultValue 已经把 def 里的字符串放进 queryValue.text，因此这里不需要
// 判空分支。可空字符串参数请用 optionalString——那条路径的 nil 表示「请求里没给」。
func (r *queryResult) stringValue(name string) string {
	return r.values[name].text
}

// optionalString 返回可空字符串参数；缺省或 null 时返回 nil。
//
// 与 metricsadapter.go 的 optionalText 区分开：那个接收 string、把空串折叠成 nil，
// 这里的 nil 表示「请求里根本没给这个参数」。
func (r *queryResult) optionalString(name string) *string {
	value := r.values[name]
	if value.null {
		return nil
	}
	text := value.text
	return &text
}

// optionalInt 返回可空整数参数；缺省或 null 时返回 nil。
func (r *queryResult) optionalInt(name string) *int64 {
	value := r.values[name]
	if value.null {
		return nil
	}
	number := value.integer
	return &number
}

// optionalBool 返回可空布尔参数；缺省或 null 时返回 nil。
func (r *queryResult) optionalBool(name string) *bool {
	value := r.values[name]
	if value.null {
		return nil
	}
	flag := value.flag
	return &flag
}

// hoursOrNil 复刻 `hours=None if all_history else hours`（app.py:229、261）。
//
// all_history 为真时 hours 必须变 nil：metrics.Store 用 nil 表示「不设时间窗口、
// 扫全表」，这正是参照实现把全量查询挪到显式开关后面的原因（app.py:226-228）。
//
// allHistory 为空串表示该路由没有这个参数（GET /metrics/series，app.py:280-293），
// 此时按「永远用 hours」处理。
func (r *queryResult) hoursOrNil(name, allHistory string) *float64 {
	if allHistory != "" && r.boolValue(allHistory) {
		return nil
	}
	value := r.floatValue(name)
	return &value
}
