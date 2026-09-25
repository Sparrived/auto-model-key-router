package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件复刻 Pydantic v2 的请求体校验：字段声明顺序、错误类型名、错误消息、
// ctx 内容、loc 结构、以及 input 字段都要与参照实现逐字节一致，因为 422 响应体
// 会把它们全部回给客户端（management_api.py 的 APIModel 系列模型）。
//
// 只覆盖 management_api.py 真正用到的类型与约束，不做通用实现。

// fieldKind 是字段的校验类型。
type fieldKind int

const (
	kindStr          fieldKind = iota // str
	kindBool                          // bool
	kindInt                           // int
	kindFloat                         // float
	kindStrList                       // list[str]
	kindStrOrNullMap                  // dict[str, str | None]
	kindAnyDict                       // dict[str, Any]
	kindNested                        // 子模型
	kindNestedList                    // list[子模型]
	kindTargetList                    // list[dict[str, str]]
)

// fieldSpec 描述一个字段。
//
// required 与 nullable 是两个独立维度，与 Pydantic 对应：
//   - required 缺省且请求里没有该键 -> missing；
//   - nullable 为假时显式传 null -> 对应类型的 *_type 错误。
//
// 例如 `name: str = Field(min_length=1)` 是「必填 + 不可为 null」，而
// `name: str | None = Field(default=None, min_length=1)` 是「可省 + 可为 null」。
type fieldSpec struct {
	name     string
	kind     fieldKind
	required bool
	nullable bool
	minLen   int
	ge, gt   *canonical.Value
	le, lt   *canonical.Value
	nested   *modelSpec
}

// modelSpec 描述一个 Pydantic 模型。
type modelSpec struct {
	name   string
	fields []fieldSpec
	index  map[string]int
}

// newModelSpec 构造模型描述；字段顺序即错误顺序。
func newModelSpec(name string, fields ...fieldSpec) *modelSpec {
	spec := &modelSpec{name: name, fields: fields, index: make(map[string]int, len(fields))}
	for i, field := range fields {
		spec.index[field.name] = i
	}
	return spec
}

// req 声明必填字段（不可为 null）。
func req(name string, kind fieldKind) fieldSpec {
	return fieldSpec{name: name, kind: kind, required: true}
}

// def 声明带默认值字段（可省，不可为 null）。
func def(name string, kind fieldKind) fieldSpec {
	return fieldSpec{name: name, kind: kind}
}

// nul 声明 `T | None = None` 字段。
func nul(name string, kind fieldKind) fieldSpec {
	return fieldSpec{name: name, kind: kind, nullable: true}
}

// minLen 追加 min_length 约束（字符串按字符数，列表按元素数）。
func (f fieldSpec) minLenOf(n int) fieldSpec { f.minLen = n; return f }

// geV/gtV/leV/ltV 追加数值边界；bound 同时用于 ctx 与消息文本。
func (f fieldSpec) geV(bound *canonical.Value) fieldSpec { f.ge = bound; return f }
func (f fieldSpec) gtV(bound *canonical.Value) fieldSpec { f.gt = bound; return f }
func (f fieldSpec) leV(bound *canonical.Value) fieldSpec { f.le = bound; return f }
func (f fieldSpec) ltV(bound *canonical.Value) fieldSpec { f.lt = bound; return f }

// 各字段约束用的字面量。数字字面量的类型（int 还是 float）会原样进入 ctx：
// `port: int = Field(ge=1)` 给 `{"ge":1}`，而 `timeout_seconds: float =
// Field(gt=0)` 给 `{"gt":0.0}`——两者都是参照实现里真实出现过的形状，不能统一。
var (
	intGe1     = canonical.NewInt("1")
	intLe65535 = canonical.NewInt("65535")
	intGe0     = canonical.NewInt("0")
	floatGt0   = canonical.NewFloat(0)
	floatLe120 = canonical.NewFloat(120)
)

// specRevisionPayload 对应 RevisionPayload：config_revision 必填。
var specRevisionPayload = newModelSpec("RevisionPayload",
	req("config_revision", kindStr).minLenOf(1),
)

// specKeyCreate 对应 KeyCreate。
//
// 它继承 APIModel，所以**也有 config_revision**。这条继承带来两个不对称：
// POST /api/models/{id}/keys 会把它弹掉，而 POST /api/models 的 keys 数组不会
// （见 modelCreateData 的说明）——两者都是参照实现的历史行为，刻意保留。
var specKeyCreate = newModelSpec("KeyCreate",
	nul("config_revision", kindStr).minLenOf(1),
	req("name", kindStr).minLenOf(1),
	req("api_key", kindStr).minLenOf(1),
	nul("base_url", kindStr),
	def("enabled", kindBool),
	nul("upstream_routes", kindStrOrNullMap),
)

// specKeyUpdate 对应 KeyUpdate。
var specKeyUpdate = newModelSpec("KeyUpdate",
	nul("config_revision", kindStr).minLenOf(1),
	nul("name", kindStr).minLenOf(1),
	nul("api_key", kindStr).minLenOf(1),
	nul("base_url", kindStr),
	nul("enabled", kindBool),
	nul("upstream_routes", kindStrOrNullMap),
)

// specTaskParams 对应 TaskParams。
//
// 注意它继承 APIModel，因此**也接受 config_revision**。这不是笔误：参照实现真的
// 允许在 params 里写 config_revision，随后由 config.normalize_task_params 以
// 422「params 不支持的参数」拒绝。Go 侧必须同样先接受、
// 再由 config 层拒绝，否则会提前变成 extra_forbidden，错误文本就不一样了。
var specTaskParams = newModelSpec("TaskParams",
	nul("config_revision", kindStr).minLenOf(1),
	nul("temperature", kindFloat),
	nul("top_p", kindFloat),
	nul("top_k", kindInt),
	nul("frequency_penalty", kindFloat),
	nul("presence_penalty", kindFloat),
	nul("seed", kindInt),
	nul("stop", kindStrList),
	nul("max_tokens", kindInt),
	nul("reasoning_effort", kindStr),
)

// specModelCreate 对应 ModelCreate。
var specModelCreate = newModelSpec("ModelCreate",
	nul("config_revision", kindStr).minLenOf(1),
	req("id", kindStr).minLenOf(1),
	def("aliases", kindStrList),
	def("routing_mode", kindStr),
	nul("reasoning_effort", kindStr),
	def("keys", kindNestedList).withNested(specKeyCreate),
)

// withNested 绑定子模型。
func (f fieldSpec) withNested(spec *modelSpec) fieldSpec { f.nested = spec; return f }

// specModelUpdate 对应 ModelUpdate。
var specModelUpdate = newModelSpec("ModelUpdate",
	nul("config_revision", kindStr).minLenOf(1),
	nul("id", kindStr).minLenOf(1),
	nul("aliases", kindStrList),
	nul("routing_mode", kindStr),
	nul("reasoning_effort", kindStr),
)

// specUnifiedModelUpdate 对应 UnifiedModelUpdate。
var specUnifiedModelUpdate = newModelSpec("UnifiedModelUpdate",
	nul("config_revision", kindStr).minLenOf(1),
	nul("model", kindStr).minLenOf(1),
	nul("key", kindStr),
	nul("image_model", kindStr),
	nul("image_key", kindStr),
	nul("default", kindAnyDict),
	nul("image", kindAnyDict),
	nul("embeddings", kindAnyDict),
)

// specProbeKeysRequest 对应 ProbeKeysRequest。
var specProbeKeysRequest = newModelSpec("ProbeKeysRequest",
	nul("config_revision", kindStr).minLenOf(1),
	req("provider_id", kindStr).minLenOf(1),
	def("keys", kindStrList),
	def("timeout_seconds", kindFloat).gtV(floatGt0).leV(floatLe120),
)

// specConfigImportRequest 对应 ConfigImportRequest。
var specConfigImportRequest = newModelSpec("ConfigImportRequest",
	req("config_revision", kindStr).minLenOf(1),
	req("config", kindAnyDict),
)

// specTaskCreate 对应 TaskCreate。
//
// model 可省略（Go 侧放宽，见 docs/API.md 与 CHANGELOG）：任务可以先建出来占位，
// 之后再选模型；被请求时由 proxy 明确报 404，而不是静默落到别的模型上。传了就必须
// 非空且引用已配置的模型。
var specTaskCreate = newModelSpec("TaskCreate",
	req("config_revision", kindStr).minLenOf(1),
	req("name", kindStr).minLenOf(1),
	nul("model", kindStr),
	nul("display_name", kindStr),
	nul("fallback_model", kindStr),
	nul("params", kindNested).withNested(specTaskParams),
)

// specTaskUpdate 对应 TaskUpdate。
//
// 三个可选字段都用 nul：显式传 null（或空串）表示清空该字段，因此不能再要求
// model 非空——否则「清空模型」这条路径根本表达不出来。
var specTaskUpdate = newModelSpec("TaskUpdate",
	req("config_revision", kindStr).minLenOf(1),
	nul("model", kindStr),
	nul("display_name", kindStr),
	nul("fallback_model", kindStr),
	nul("params", kindNested).withNested(specTaskParams),
)

// specWorkspaceRename 是工作空间改名请求体（Go 侧新增，无 Python 先例）。
//
// config_revision 必填，与 TaskUpdate 一致：改名会把整组任务搬到新键下，是明确的
// 读-改-写，必须能防住「基于过期配置提交」。
var specWorkspaceRename = newModelSpec("WorkspaceRename",
	req("config_revision", kindStr).minLenOf(1),
	req("name", kindStr).minLenOf(1),
)

// specWorkspaceCreate 是新建工作空间请求体（Go 侧新增，无 Python 先例）。
//
// api_key 可选：不传由服务端生成（WebUI 的用户没有理由自己想一个），传了就用调用方
// 给的（应用侧通常已有既定凭据）。
//
// config_revision 必填，与 WorkspaceRename 一致：新建会往配置里写一个键，是明确的
// 读-改-写，必须能防住「基于过期配置提交」。
var specWorkspaceCreate = newModelSpec("WorkspaceCreate",
	req("config_revision", kindStr).minLenOf(1),
	req("name", kindStr).minLenOf(1),
	nul("api_key", kindStr).minLenOf(1),
)

// specWorkspaceModels 是设定工作空间可直呼模型清单的请求体（Go 侧新增，无 Python 先例）。
//
// models 必填但可为 null，三种取值各有含义：
//   - `[...]` 允许直呼这些模型；
//   - `[]`  一个都不许直呼（只走任务名）；
//   - `null` 清除清单，回到「不限制」。
//
// 必填是为了不让「漏传字段」被当成「清除限制」——那是把一条授权悄悄放宽。要清除就
// 显式写 null。
var specWorkspaceModels = newModelSpec("WorkspaceModels",
	req("config_revision", kindStr).minLenOf(1),
	fieldSpec{name: "models", kind: kindStrList, required: true, nullable: true},
)

// specWorkspaceExport 是工作空间导出请求体（Go 侧新增，无 Python 先例）。
//
// 整个 body 可省略（导出全部）；给了就按 workspaces 列出的名字导出。
var specWorkspaceExport = newModelSpec("WorkspaceExport",
	nul("config_revision", kindStr).minLenOf(1),
	def("workspaces", kindStrList),
)

// specWorkspaceImport 是工作空间导入请求体（Go 侧新增，无 Python 先例）。
//
// bundle 用 kindAnyDict：它是配置形状的一段，先原样收下再交给 configops 解析并报
// 出精确的中文错误——在这一层用嵌套模型校验会把「包是旧版本」这种正常情况判成
// extra_forbidden。
//
// prefix 可选：同名空间改名前缀，留空表示覆盖同名空间。
var specWorkspaceImport = newModelSpec("WorkspaceImport",
	req("config_revision", kindStr).minLenOf(1),
	req("bundle", kindAnyDict),
	nul("prefix", kindStr).minLenOf(1),
)

// specCPAInstances 是 CPA 实例清单的请求体（Go 侧新增，无 Python 先例）。
//
// instances 用 kindAnyDict：它是配置里的一整段，先原样收下再交给 configops 逐条校验
// 并报出精确的中文错误（与 specWorkspaceImport 的 bundle 同一思路）。
//
// config_revision 必填：整体替换实例清单是明确的读-改-写，必须能防住「基于过期配置
// 提交」——两个标签页各自加一个实例时，后提交的那个不该把先提交的悄悄抹掉。
var specCPAInstances = newModelSpec("CPAInstances",
	req("config_revision", kindStr).minLenOf(1),
	req("instances", kindAnyDict),
)

// —— 访问密钥（Go 侧新增，取代已删除的访客模式）——

// specAccessKeyCreate 是新建访问密钥请求体。
//
// name 必填（列表里要有个可读标签）；key 可选，不传由服务端生成。
//
// providers / models 用 kindStrList 而不是带 nullable 的必填字段：这里的「不传」与
// 「传 null」都表示**本次不设这份清单**（不限制）。三态中的「清除限制」只出现在更新
// 接口上（见 specAccessKeyUpdate），新建时没有「清除」可言。
var specAccessKeyCreate = newModelSpec("AccessKeyCreate",
	req("config_revision", kindStr).minLenOf(1),
	req("name", kindStr).minLenOf(1),
	nul("key", kindStr).minLenOf(1),
	def("enabled", kindBool),
	nul("providers", kindStrList),
	nul("models", kindStrList),
)

// specAccessKeyUpdate 是修改访问密钥请求体。
//
// providers / models 必填但可为 null，与 specWorkspaceModels 同一套三态：
//   - `[...]` 限定为这些；
//   - `[]`  一个都不许；
//   - `null` 清除清单，回到「不限制」。
//
// 必填是为了不让「漏传字段」被当成「清除限制」——那是把一条授权悄悄放宽。
var specAccessKeyUpdate = newModelSpec("AccessKeyUpdate",
	req("config_revision", kindStr).minLenOf(1),
	nul("name", kindStr).minLenOf(1),
	nul("enabled", kindBool),
	fieldSpec{name: "providers", kind: kindStrList, required: true, nullable: true},
	fieldSpec{name: "models", kind: kindStrList, required: true, nullable: true},
)

// specProviderCreate 对应 ProviderCreate。
var specProviderCreate = newModelSpec("ProviderCreate",
	req("config_revision", kindStr).minLenOf(1),
	req("id", kindStr).minLenOf(1),
	req("base_url", kindStr).minLenOf(1),
)

// specProviderUpdate 对应 ProviderUpdate。
var specProviderUpdate = newModelSpec("ProviderUpdate",
	req("config_revision", kindStr).minLenOf(1),
	nul("id", kindStr).minLenOf(1),
	nul("base_url", kindStr).minLenOf(1),
	nul("routes", kindStrOrNullMap),
)

// specProviderKeyCreate 对应 ProviderKeyCreate。
var specProviderKeyCreate = newModelSpec("ProviderKeyCreate",
	req("config_revision", kindStr).minLenOf(1),
	req("name", kindStr).minLenOf(1),
	req("api_key", kindStr).minLenOf(1),
	def("enabled", kindBool),
)

// specProviderKeyUpdate 对应 ProviderKeyUpdate。
var specProviderKeyUpdate = newModelSpec("ProviderKeyUpdate",
	req("config_revision", kindStr).minLenOf(1),
	nul("name", kindStr).minLenOf(1),
	nul("api_key", kindStr).minLenOf(1),
	nul("enabled", kindBool),
)

// specProviderKeyProbeRequest 对应 ProviderKeyProbeRequest。
var specProviderKeyProbeRequest = newModelSpec("ProviderKeyProbeRequest",
	req("config_revision", kindStr).minLenOf(1),
	nul("modes", kindStrList),
)

// specProviderKeyModelsRequest 对应 ProviderKeyModelsRequest。
var specProviderKeyModelsRequest = newModelSpec("ProviderKeyModelsRequest",
	req("config_revision", kindStr).minLenOf(1),
	def("models", kindStrList),
)

// specRouteCreate 对应 RouteCreate。
var specRouteCreate = newModelSpec("RouteCreate",
	req("config_revision", kindStr).minLenOf(1),
	req("id", kindStr).minLenOf(1),
	req("targets", kindTargetList).minLenOf(1),
	def("aliases", kindStrList),
	nul("routing_mode", kindStr),
)

// specRouteUpdate 对应 RouteUpdate。
//
// targets 允许空数组：把一个路由的目标清空就等于删掉这个路由（见
// handleUpdateRoute），因此这里不能加 minLenOf(1)。
var specRouteUpdate = newModelSpec("RouteUpdate",
	req("config_revision", kindStr).minLenOf(1),
	nul("id", kindStr).minLenOf(1),
	nul("targets", kindTargetList),
	nul("aliases", kindStrList),
	nul("routing_mode", kindStr),
)

// specSettingsUpdate 对应 SettingsUpdate。
var specSettingsUpdate = newModelSpec("SettingsUpdate",
	req("config_revision", kindStr).minLenOf(1),
	nul("host", kindStr).minLenOf(1),
	nul("port", kindInt).geV(intGe1).leV(intLe65535),
	nul("request_timeout", kindFloat).gtV(floatGt0),
	nul("stream_first_byte_timeout", kindFloat).gtV(floatGt0),
	nul("stream_idle_timeout", kindFloat).gtV(floatGt0),
	nul("max_retries", kindInt).geV(intGe0),
)

// validationError 是一条 Pydantic 校验错误，渲染顺序固定为
// type、loc、msg、input、ctx。
type validationError struct {
	typ   string
	loc   []any
	msg   string
	input *canonical.Value
	ctx   *canonical.Value
}

// toValue 渲染成 JSON 对象。
func (e *validationError) toValue() *canonical.Value {
	loc := canonical.NewArray()
	for _, segment := range e.loc {
		switch typed := segment.(type) {
		case string:
			loc.Arr = append(loc.Arr, canonical.NewString(typed))
		case int:
			loc.Arr = append(loc.Arr, canonical.NewInt(strconv.Itoa(typed)))
		}
	}
	item := canonical.NewObject()
	item.SetKey("type", canonical.NewString(e.typ))
	item.SetKey("loc", loc)
	item.SetKey("msg", canonical.NewString(e.msg))
	input := e.input
	if input == nil {
		input = canonical.NewNull()
	}
	item.SetKey("input", input)
	if e.ctx != nil && e.ctx.Obj.Len() > 0 {
		item.SetKey("ctx", e.ctx)
	}
	return item
}

// payloadValidationError 是 Pydantic 校验失败（HTTP 422）的载体。
//
// 它实现 error，好让校验失败与其它错误共用 handler 的 error 出口；writeError 再
// 把它还原成 `{"detail": [...]}`。
type payloadValidationError struct {
	errs []validationError
}

// newPayloadValidationError 构造校验失败。
func newPayloadValidationError(errs ...validationError) *payloadValidationError {
	return &payloadValidationError{errs: errs}
}

func (e *payloadValidationError) Error() string { return "请求体校验失败" }

// validationErrorsValue 把错误列表包成 FastAPI 的 {"detail": [...]}。
func validationErrorsValue(errs []validationError) *canonical.Value {
	items := make([]*canonical.Value, 0, len(errs))
	for i := range errs {
		items = append(items, errs[i].toValue())
	}
	return objectOf(canonical.ObjectPair{Key: "detail", Value: canonical.NewArray(items...)})
}

// requestBody 读取请求体。
//
// 空体与「显式 JSON null」在 FastAPI 里等价于「没有请求体」，由调用方决定是可省
// （None）还是必填（missing ["body"]）。
func requestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// decodePayload 解析并校验请求体。
//
// 返回 (已校验字段对象, 请求体是否存在, error)。optional 对应 Python 签名里的
// `payload: X | None = None`：为真时「没有请求体」直接返回 present=false，为假时
// 是 422 missing。
//
// 已知分歧：请求体不是合法 JSON 时，Pydantic 的 `json_invalid` 错误里带的是
// CPython json 模块的错误文本与偏移（例如「Expecting property name enclosed in
// double quotes」），Go 侧无法逐字复刻，这里给同类错误形状但文本取 encoding/json
// 的说明。这条分歧尚未被覆盖，修改时注意没有既有用例会替你发现。
func decodePayload(r *http.Request, spec *modelSpec, optional bool) (*canonical.Value, bool, error) {
	raw, err := requestBody(r)
	if err != nil {
		return nil, false, err
	}
	absent := func() (*canonical.Value, bool, error) {
		if optional {
			return nil, false, nil
		}
		return nil, false, newPayloadValidationError(validationError{
			typ:   "missing",
			loc:   []any{"body"},
			msg:   "Field required",
			input: canonical.NewNull(),
		})
	}
	if len(raw) == 0 {
		return absent()
	}
	parsed, parseErr := canonical.Parse(raw)
	if parseErr != nil {
		ctx := canonical.NewObject()
		ctx.SetKey("error", canonical.NewString(parseErr.Error()))
		return nil, false, newPayloadValidationError(validationError{
			typ:   "json_invalid",
			loc:   []any{"body", 0},
			msg:   "JSON decode error",
			input: canonical.NewObject(),
			ctx:   ctx,
		})
	}
	// 顶层显式 null：Pydantic 把 `X | None` 的 null 视作「没有请求体」。
	if parsed == nil || parsed.Kind == canonical.KindNull {
		return absent()
	}
	if !parsed.IsObject() {
		return nil, false, newPayloadValidationError(validationError{
			typ:   "model_attributes_type",
			loc:   []any{"body"},
			msg:   "Input should be a valid dictionary or object to extract fields from",
			input: parsed,
		})
	}
	value, errs := validateModel(spec, parsed, []any{"body"}, true)
	if len(errs) > 0 {
		return nil, false, newPayloadValidationError(errs...)
	}
	return value, true, nil
}

// validateModel 按字段声明顺序校验并强制类型转换。
//
// describeExtras 为真时对未声明字段报 extra_forbidden（APIModel 全部是
// extra="forbid"）。返回的对象只含**请求里真正出现过**的字段，等价于 Pydantic 的
// model_dump(exclude_unset=True)——参照实现的 _payload_dict 正是这么取的，缺省值
// 与「显式传了缺省值」在后续分支里语义不同（例如 update_task 用 `"params" in
// updates` 判断是否要覆盖）。
func validateModel(spec *modelSpec, input *canonical.Value, loc []any, describeExtras bool) (*canonical.Value, []validationError) {
	out := canonical.NewObject()
	var errs []validationError
	for i := range spec.fields {
		field := &spec.fields[i]
		child, present := input.Obj.Get(field.name)
		fieldLoc := append(append([]any{}, loc...), field.name)
		if !present {
			if field.required {
				errs = append(errs, validationError{
					typ:   "missing",
					loc:   fieldLoc,
					msg:   "Field required",
					input: input,
				})
			}
			continue
		}
		coerced, fieldErrs := validateField(field, child, fieldLoc)
		errs = append(errs, fieldErrs...)
		if len(fieldErrs) == 0 {
			out.SetKey(field.name, coerced)
		}
	}
	if describeExtras {
		for _, key := range input.Obj.Keys() {
			if _, known := spec.index[key]; known {
				continue
			}
			child, _ := input.Obj.Get(key)
			errs = append(errs, validationError{
				typ:   "extra_forbidden",
				loc:   append(append([]any{}, loc...), key),
				msg:   "Extra inputs are not permitted",
				input: child,
			})
		}
	}
	return out, errs
}

// validateField 校验并转换单个字段。
func validateField(field *fieldSpec, value *canonical.Value, loc []any) (*canonical.Value, []validationError) {
	if value == nil || value.Kind == canonical.KindNull {
		if field.nullable {
			return canonical.NewNull(), nil
		}
		return nil, []validationError{typeError(field.kind, loc, value)}
	}
	switch field.kind {
	case kindStr:
		text, ok := value.AsString()
		if !ok {
			return nil, []validationError{typeError(kindStr, loc, value)}
		}
		if field.minLen > 0 && len([]rune(text)) < field.minLen {
			ctx := canonical.NewObject()
			ctx.SetKey("min_length", canonical.NewInt(strconv.Itoa(field.minLen)))
			return nil, []validationError{{
				typ:   "string_too_short",
				loc:   loc,
				msg:   fmt.Sprintf("String should have at least %d character", field.minLen),
				input: value,
				ctx:   ctx,
			}}
		}
		return value, nil
	case kindBool:
		converted, ok := pyBool(value)
		if !ok {
			return nil, []validationError{{
				typ:   "bool_parsing",
				loc:   loc,
				msg:   "Input should be a valid boolean, unable to interpret input",
				input: value,
			}}
		}
		return canonical.NewBool(converted), nil
	case kindInt:
		converted, err := pyInt(value)
		if err != nil {
			return nil, []validationError{err.withLoc(loc)}
		}
		if errs := checkBounds(field, converted, value, loc); len(errs) > 0 {
			return nil, errs
		}
		return converted, nil
	case kindFloat:
		converted, err := pyFloat(value)
		if err != nil {
			return nil, []validationError{err.withLoc(loc)}
		}
		if errs := checkBounds(field, converted, value, loc); len(errs) > 0 {
			return nil, errs
		}
		return converted, nil
	case kindStrList:
		return validateStrList(value, loc)
	case kindStrOrNullMap:
		return validateStrOrNullMap(value, loc)
	case kindAnyDict:
		if !value.IsObject() {
			return nil, []validationError{typeError(kindAnyDict, loc, value)}
		}
		return value.Clone(), nil
	case kindTargetList:
		return validateTargetList(field, value, loc)
	case kindNested:
		if !value.IsObject() {
			return nil, []validationError{{
				typ:   "model_attributes_type",
				loc:   loc,
				msg:   "Input should be a valid dictionary or object to extract fields from",
				input: value,
			}}
		}
		return validateModel(field.nested, value, loc, true)
	case kindNestedList:
		return validateNestedList(field, value, loc)
	}
	return nil, nil
}

// validateNestedList 校验 list[子模型]（ModelCreate 的 keys）。
//
// 元素不是对象时报 model_attributes_type（Pydantic 对「期望模型、拿到标量」用的就是
// 这个类型），元素内部字段错误的位置形如 ["body","keys",0,"name"]。
func validateNestedList(field *fieldSpec, value *canonical.Value, loc []any) (*canonical.Value, []validationError) {
	if !value.IsArray() {
		return nil, []validationError{typeError(kindNestedList, loc, value)}
	}
	out := canonical.NewArray()
	var errs []validationError
	for index, item := range value.Arr {
		itemLoc := append(append([]any{}, loc...), index)
		if !item.IsObject() {
			errs = append(errs, validationError{
				typ:   "model_attributes_type",
				loc:   itemLoc,
				msg:   "Input should be a valid dictionary or object to extract fields from",
				input: item,
			})
			continue
		}
		converted, itemErrs := validateModel(field.nested, item, itemLoc, true)
		errs = append(errs, itemErrs...)
		if len(itemErrs) == 0 {
			out.Arr = append(out.Arr, converted)
		}
	}
	return out, errs
}

// typeErrorOf 返回「类型不对」的 Pydantic 错误。
func typeError(kind fieldKind, loc []any, value *canonical.Value) validationError {
	var typ, msg string
	switch kind {
	case kindStr:
		typ, msg = "string_type", "Input should be a valid string"
	case kindBool:
		typ, msg = "bool_type", "Input should be a valid boolean"
	case kindInt:
		typ, msg = "int_type", "Input should be a valid integer"
	case kindFloat:
		typ, msg = "float_type", "Input should be a valid number"
	case kindStrList, kindTargetList, kindNestedList:
		typ, msg = "list_type", "Input should be a valid list"
	case kindStrOrNullMap, kindAnyDict:
		typ, msg = "dict_type", "Input should be a valid dictionary"
	default:
		typ, msg = "model_attributes_type", "Input should be a valid dictionary or object to extract fields from"
	}
	return validationError{typ: typ, loc: loc, msg: msg, input: value}
}

// withLoc 补上错误位置。
func (e *validationError) withLoc(loc []any) validationError {
	e.loc = loc
	return *e
}

// validateStrList 校验 list[str]。
func validateStrList(value *canonical.Value, loc []any) (*canonical.Value, []validationError) {
	if !value.IsArray() {
		return nil, []validationError{typeError(kindStrList, loc, value)}
	}
	out := canonical.NewArray()
	var errs []validationError
	for index, item := range value.Arr {
		if !item.IsString() {
			errs = append(errs, validationError{
				typ:   "string_type",
				loc:   append(append([]any{}, loc...), index),
				msg:   "Input should be a valid string",
				input: item,
			})
			continue
		}
		out.Arr = append(out.Arr, item)
	}
	return out, errs
}

// validateStrOrNullMap 校验 dict[str, str | None]。
//
// 值可以是 null 或字符串；其它类型报 string_type。注意 dict 的**键**在 Pydantic
// 里也会被校验为 str，但参照实现的请求体里键总是字符串（JSON 对象键必为字符串），
// 所以这里不额外处理。
func validateStrOrNullMap(value *canonical.Value, loc []any) (*canonical.Value, []validationError) {
	if !value.IsObject() {
		return nil, []validationError{typeError(kindStrOrNullMap, loc, value)}
	}
	out := canonical.NewObject()
	var errs []validationError
	for _, key := range value.Obj.Keys() {
		child, _ := value.Obj.Get(key)
		childLoc := append(append([]any{}, loc...), key)
		if child == nil || child.Kind == canonical.KindNull {
			out.SetKey(key, canonical.NewNull())
			continue
		}
		if !child.IsString() {
			errs = append(errs, validationError{
				typ:   "string_type",
				loc:   childLoc,
				msg:   "Input should be a valid string",
				input: child,
			})
			continue
		}
		out.SetKey(key, child)
	}
	return out, errs
}

// validateTargetList 校验 list[dict[str, str]]（RouteCreate/RouteUpdate 的 targets）。
func validateTargetList(field *fieldSpec, value *canonical.Value, loc []any) (*canonical.Value, []validationError) {
	if !value.IsArray() {
		return nil, []validationError{typeError(kindTargetList, loc, value)}
	}
	if field.minLen > 0 && len(value.Arr) < field.minLen {
		ctx := canonical.NewObject()
		ctx.SetKey("field_type", canonical.NewString("List"))
		ctx.SetKey("min_length", canonical.NewInt(strconv.Itoa(field.minLen)))
		ctx.SetKey("actual_length", canonical.NewInt(strconv.Itoa(len(value.Arr))))
		return nil, []validationError{{
			typ: "too_short",
			loc: loc,
			msg: fmt.Sprintf("List should have at least %d item after validation, not %d",
				field.minLen, len(value.Arr)),
			input: value,
			ctx:   ctx,
		}}
	}
	out := canonical.NewArray()
	var errs []validationError
	for index, item := range value.Arr {
		itemLoc := append(append([]any{}, loc...), index)
		if !item.IsObject() {
			errs = append(errs, validationError{
				typ:   "dict_type",
				loc:   itemLoc,
				msg:   "Input should be a valid dictionary",
				input: item,
			})
			continue
		}
		converted := canonical.NewObject()
		for _, key := range item.Obj.Keys() {
			child, _ := item.Obj.Get(key)
			if !child.IsString() {
				errs = append(errs, validationError{
					typ:   "string_type",
					loc:   append(append([]any{}, itemLoc...), key),
					msg:   "Input should be a valid string",
					input: child,
				})
				continue
			}
			converted.SetKey(key, child)
		}
		out.Arr = append(out.Arr, converted)
	}
	return out, errs
}

// checkBounds 校验数值边界。
//
// input 用的是**原始输入**而不是转换后的值：`timeout_seconds: float = Field(gt=0)`
// 收到整数 0 时，Pydantic 报的是 `"input":0`（整数），而 ctx 里的约束是 `0.0`
// （浮点）。两者必须分别取，否则 int/float 的差异会让响应体对不上。
func checkBounds(field *fieldSpec, value *canonical.Value, original *canonical.Value, loc []any) []validationError {
	number, ok := value.AsFloat()
	if !ok {
		return nil
	}
	report := func(typ, msg string, bound *canonical.Value) []validationError {
		ctx := canonical.NewObject()
		ctx.SetKey(typContextKey(typ), bound)
		return []validationError{{typ: typ, loc: loc, msg: msg, input: original, ctx: ctx}}
	}
	if field.ge != nil {
		bound, _ := field.ge.AsFloat()
		if number < bound {
			return report("greater_than_equal", fmt.Sprintf("Input should be greater than or equal to %s", numText(field.ge)), field.ge)
		}
	}
	if field.gt != nil {
		bound, _ := field.gt.AsFloat()
		if number <= bound {
			return report("greater_than", fmt.Sprintf("Input should be greater than %s", numText(field.gt)), field.gt)
		}
	}
	if field.le != nil {
		bound, _ := field.le.AsFloat()
		if number > bound {
			return report("less_than_equal", fmt.Sprintf("Input should be less than or equal to %s", numText(field.le)), field.le)
		}
	}
	if field.lt != nil {
		bound, _ := field.lt.AsFloat()
		if number >= bound {
			return report("less_than", fmt.Sprintf("Input should be less than %s", numText(field.lt)), field.lt)
		}
	}
	return nil
}

// typContextKey 返回错误类型在 ctx 里用的键名。
//
// Pydantic 的 ctx 键就是约束名：ge/gt/le/lt。
func typContextKey(typ string) string {
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

// numText 把约束字面量渲染成错误消息里的形式。
//
// Pydantic 的消息用「短」写法：ctx 里是 0.0、消息里是 0（参照实现里
// timeout_seconds=0 的 msg 是 "Input should be greater than 0"，而 ctx 是
// {"gt":0.0}）。因此整数值的浮点要去掉小数点。
func numText(value *canonical.Value) string {
	text := value.Num
	if strings.HasSuffix(text, ".0") {
		return strings.TrimSuffix(text, ".0")
	}
	return text
}

// pyBool 复刻 Pydantic 的 lax bool 解析。
//
// 接受 bool、0/1 整数、以及 "true/false/yes/no/on/off/1/0/t/f/y/n"（大小写不敏感，
// 首尾空白容忍）。整数 2 会报 bool_parsing——这是参照实现的历史行为，刻意保留。
func pyBool(value *canonical.Value) (bool, bool) {
	switch value.Kind {
	case canonical.KindBool:
		return value.Bool, true
	case canonical.KindNumber:
		if !isFloatLiteral(value.Num) {
			switch value.Num {
			case "0":
				return false, true
			case "1":
				return true, true
			}
			return false, false
		}
		switch value.Num {
		case "0.0":
			return false, true
		case "1.0":
			return true, true
		}
		return false, false
	case canonical.KindString:
		switch strings.ToLower(strings.TrimSpace(value.Str)) {
		case "true", "1", "yes", "on", "t", "y":
			return true, true
		case "false", "0", "no", "off", "f", "n":
			return false, true
		}
		return false, false
	}
	return false, false
}

// isFloatLiteral 报告数字字面量是否为浮点写法。
func isFloatLiteral(literal string) bool {
	return strings.ContainsAny(literal, ".eE") || literal == "NaN" || strings.Contains(literal, "Infinity")
}

// pyInt 复刻 Pydantic 的 lax int 解析。
//
// 与 Python 的 int() 有两处刻意差异：
//   - 浮点必须**没有小数部分**才接受，否则报 int_from_float（Python 的 int(1.5) 会
//     截断成 1，Pydantic 拒绝）；
//   - 解析失败时错误类型是 int_parsing/int_type 而不是 ValueError 文本。
func pyInt(value *canonical.Value) (*canonical.Value, *validationError) {
	switch value.Kind {
	case canonical.KindBool:
		if value.Bool {
			return canonical.NewInt("1"), nil
		}
		return canonical.NewInt("0"), nil
	case canonical.KindNumber:
		if !isFloatLiteral(value.Num) {
			return canonical.NewInt(value.Num), nil
		}
		number, err := canonical.ToFloat(value)
		if err != nil {
			converted := typeError(kindInt, nil, value)
			return nil, &converted
		}
		truncated := float64(int64(number))
		if truncated != number {
			return nil, &validationError{
				typ:   "int_from_float",
				msg:   "Input should be a valid integer, got a number with a fractional part",
				input: value,
			}
		}
		return canonical.NewInt(strconv.FormatInt(int64(number), 10)), nil
	case canonical.KindString:
		parsed, err := canonical.ToInt(value)
		if err != nil {
			return nil, &validationError{
				typ:   "int_parsing",
				msg:   "Input should be a valid integer, unable to parse string as an integer",
				input: value,
			}
		}
		return canonical.NewInt(strconv.FormatInt(parsed, 10)), nil
	}
	converted := typeError(kindInt, nil, value)
	return nil, &converted
}

// pyFloat 复刻 Pydantic 的 lax float 解析。
func pyFloat(value *canonical.Value) (*canonical.Value, *validationError) {
	switch value.Kind {
	case canonical.KindBool:
		if value.Bool {
			return canonical.NewFloat(1), nil
		}
		return canonical.NewFloat(0), nil
	case canonical.KindNumber:
		if !isFloatLiteral(value.Num) {
			parsed, err := canonical.ToFloat(value)
			if err != nil {
				converted := typeError(kindFloat, nil, value)
				return nil, &converted
			}
			return canonical.NewFloat(parsed), nil
		}
		return value, nil
	case canonical.KindString:
		parsed, err := canonical.ToFloat(value)
		if err != nil {
			return nil, &validationError{
				typ:   "float_parsing",
				msg:   "Input should be a valid number, unable to parse string as a number",
				input: value,
			}
		}
		return canonical.NewFloat(parsed), nil
	}
	converted := typeError(kindFloat, nil, value)
	return nil, &converted
}
