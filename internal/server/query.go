package server

import (
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
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
//     末位，否则多值请求会静默选错值。
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
	qInt60     = canonical.NewInt("60")
	// 三档：本机（local_api_key）、工作空间推理凭据、访问密钥。后两者是 Go 侧新增
	// （参照实现只有 local 与已删除的 visitor 两档）。
	//
	// 访问密钥取代了 visitor 档：两者都是「分发给外部使用者的受限凭据」，但访问密钥
	// 一人一把、各带清单，因此指标里分开统计才有意义。老库里残留的 visitor 行不再
	// 出现在过滤器里（值域即契约，见 qLiteral）。
	callerTypes = []string{"local", "workspace", "access_key"}
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
var keyUsageParams = []qParam{
	{name: "hours", kind: qFloat, def: qFloat24, gt: qFloat0, le: qFloat8760},
	{name: "all_history", kind: qBool, def: qBoolFalse},
}

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
	flag    bool   // qBool
}

// queryResult 是整条路由的校验结果。
type queryResult struct {
	values map[string]queryValue
}

// queryError 是一条 Pydantic 校验错误。
//
// 渲染顺序固定为 type、loc、msg、input、ctx，与 internal/api 的同名结构一致；
// input 对查询参数永远是**原始字符串**（Pydantic 报的是进入该字段校验器之前的值）。
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
