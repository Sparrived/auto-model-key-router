package api

import (
	"fmt"
	"os"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/formatting"
)

// trimSpace 对应 Python 的 str.strip()。
//
// Go 的 strings.TrimSpace 与 Python 的 str.strip() 裁剪的空白集合不完全相同
// （Python 还裁 \x1c-\x1f 等），但配置里的名字来自人类输入，差异不可观测；此处
// 刻意用标准库而不是复刻 Python 的空白表。
func trimSpace(value string) string { return strings.TrimSpace(value) }

// trimmedOrNil 对应 `str(v).strip() if v else None`。
//
// Python 的 `if v` 是**真值**判断：空串为假 → None。因此 `{"key": ""}` 得到 None，
// 与完全不传 key 在后续分支里的效果一致（但 key_provided 仍为真，两者不混淆）。
func trimmedOrNil(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := trimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// fingerprintOf 返回密钥指纹（sha256 前 12 位十六进制）。
//
// management_api.py 里对 local_api_key 的指纹是手写的
// `hashlib.sha256(key.encode("utf-8")).hexdigest()[:12]`，与 formatting.KeyFingerprint
// 是同一个算法；统一走 formatting，避免两处实现漂移。
func fingerprintOf(apiKey string) *canonical.Value {
	return canonical.NewString(formatting.KeyFingerprint(apiKey))
}

// uuidHex 返回 32 位十六进制（`uuid.uuid4().hex`）。
//
// 参照实现有两处用它：探测 id（management_api.py:663）与导入备份文件名后缀
// （management_api.py:860）。测试通过 UUIDHex 注入常量。
func (s *Server) uuidHex() string {
	if s.UUIDHex != nil {
		return s.UUIDHex()
	}
	return probeIDHex()
}

// rejectNullFields 对应 management_api.py:1407 的 _reject_null_fields。
//
// 显式传 null 与「字段不支持 null」是两回事：Pydantic 允许 `str | None` 字段收
// null，参照实现随后在这里把其中一部分字段用 400 拒掉。错误文本里的字段名按声明
// 顺序、用 ", " 连接。
func rejectNullFields(data *canonical.Value, fields ...string) error {
	var nullFields []string
	for _, field := range fields {
		if value, present := data.LookupOK(field); present && (value == nil || value.IsNull()) {
			nullFields = append(nullFields, field)
		}
	}
	if len(nullFields) > 0 {
		return httpErrorf(400, "字段不能为 null: %s", strings.Join(nullFields, ", "))
	}
	return nil
}

// normalizeModelUpdates 对应 management_api.py:1319 的 _normalize_model_updates。
//
// 它会就地改写 data：字符串字段 strip、reasoning_effort 的空串收敛成 None。
// 「strip 后为空 → None」这步很关键：它让 `reasoning_effort: ""` 与
// `reasoning_effort: null` 在下游的 update_reasoning_effort 语义上都能清空配置。
func normalizeModelUpdates(data *canonical.Value) error {
	if err := rejectNullFields(data, "id", "aliases", "routing_mode"); err != nil {
		return err
	}
	if value, present := data.LookupOK("id"); present && value != nil && !value.IsNull() {
		data.SetKey("id", canonical.NewString(trimSpace(value.PyStr())))
	}
	for _, field := range []string{"aliases"} {
		value, present := data.LookupOK(field)
		if !present || value == nil || value.IsNull() || !value.IsArray() {
			continue
		}
		trimmed := canonical.NewArray()
		for _, item := range value.Arr {
			trimmed.Arr = append(trimmed.Arr, canonical.NewString(trimSpace(item.PyStr())))
		}
		data.SetKey(field, trimmed)
	}
	if value, present := data.LookupOK("routing_mode"); present && value != nil && !value.IsNull() {
		data.SetKey("routing_mode", canonical.NewString(trimSpace(value.PyStr())))
	}
	if value, present := data.LookupOK("reasoning_effort"); present && value != nil && !value.IsNull() {
		trimmed := trimSpace(value.PyStr())
		if trimmed == "" {
			data.SetKey("reasoning_effort", canonical.NewNull())
		} else {
			data.SetKey("reasoning_effort", canonical.NewString(trimmed))
		}
	}
	return nil
}

// normalizeKeyUpdates 对应 management_api.py:1335 的 _normalize_key_updates。
//
// 空 key 名与空 api_key 在这里被 400 拒掉——注意它们是 **HTTPException**，发生在
// _update_config 之外，所以是规整的 400 JSON 而不是 500。
func normalizeKeyUpdates(data *canonical.Value) error {
	if err := rejectNullFields(data, "name", "api_key", "enabled"); err != nil {
		return err
	}
	if value, present := data.LookupOK("name"); present && value != nil && !value.IsNull() {
		trimmed := trimSpace(value.PyStr())
		if trimmed == "" {
			return httpErrorf(400, "key 名称不能为空")
		}
		data.SetKey("name", canonical.NewString(trimmed))
	}
	if value, present := data.LookupOK("api_key"); present {
		if trimSpace(value.PyStr()) == "" {
			return httpErrorf(400, "api_key 不能为空")
		}
	}
	if value, present := data.LookupOK("base_url"); present && value != nil && !value.IsNull() {
		data.SetKey("base_url", canonical.NewString(trimSpace(value.PyStr())))
	}
	if value, present := data.LookupOK("upstream_routes"); present {
		routes, err := normalizeUpstreamRoutesOrdered(value)
		if err != nil {
			return err
		}
		data.SetKey("upstream_routes", routes)
	}
	return nil
}

// normalizeUpstreamRoutesOrdered 复刻 config.py 的 normalize_upstream_routes，
// 但**保留输入键顺序**。
//
// 为什么不用 config.NormalizeUpstreamRoutes：它返回 map，键顺序丢失，而
// normalize_upstream_routes 返回的 dict 会原样进入配置并落盘（键顺序可见）。
// 这里逐键调用 config 包导出的 mode/path 规范化函数，保证校验规则一致。
//
// 空值与空白值被跳过（`raw_route is None or not str(raw_route).strip()`），
// 而**不是**报错：`{"openai": null}` 是合法的「不配置」。
func normalizeUpstreamRoutesOrdered(raw *canonical.Value) (*canonical.Value, error) {
	if raw == nil || raw.IsNull() {
		return canonical.NewObject(), nil
	}
	if !raw.IsObject() {
		return nil, &pyValueError{message: "upstream_routes 必须是对象"}
	}
	out := canonical.NewObject()
	for _, rawMode := range raw.Obj.Keys() {
		value, _ := raw.Obj.Get(rawMode)
		if value == nil || value.IsNull() || trimSpace(value.PyStr()) == "" {
			continue
		}
		mode, err := config.NormalizeUpstreamRouteMode(canonical.NewString(rawMode))
		if err != nil {
			return nil, err
		}
		path, err := config.NormalizeUpstreamRoutePath(mode, value)
		if err != nil {
			return nil, err
		}
		out.SetKey(mode, canonical.NewString(path))
	}
	return out, nil
}

// modelCreateData 对应 management_api.py:1306 的 _model_create_data。
//
// 三个副作用按顺序发生：字符串规整、null 字段拒绝、keys 逐个规整。
//
// **不对称点**：这里替换 keys 时**不**弹出每个 KeyCreate 自带的 config_revision，
// 而 POST /api/models/{id}/keys 会把它弹掉。也就是说在 ModelCreate 的 keys 里写
// config_revision 会被原样交给 config 层（可能落进配置）。这是参照实现的历史行为，
// 刻意保留。
func modelCreateData(payload *canonical.Value) (*canonical.Value, error) {
	data := payload.Clone()
	if err := normalizeModelUpdates(data); err != nil {
		return nil, err
	}
	keys := canonical.NewArray()
	if rawKeys, present := payload.LookupOK("keys"); present && rawKeys != nil && rawKeys.IsArray() {
		for _, rawKey := range rawKeys.Arr {
			keyData, err := keyCreateData(rawKey)
			if err != nil {
				return nil, err
			}
			keys.Arr = append(keys.Arr, keyData)
		}
	}
	data.SetKey("keys", keys)
	return data, nil
}

// keyCreateData 对应 management_api.py:1313 的 _key_create_data。
func keyCreateData(payload *canonical.Value) (*canonical.Value, error) {
	data := payload.Clone()
	if err := normalizeKeyUpdates(data); err != nil {
		return nil, err
	}
	return data, nil
}

// boolOr 对应 `bool(x.get(key, fallback))`。
func boolOr(data *canonical.Value, key string, fallback bool) bool {
	value, present := data.LookupOK(key)
	if !present || value == nil || value.Kind == canonical.KindNull {
		return fallback
	}
	return value.Truthy()
}

// padMicroseconds 把微秒数补成 6 位，对应 strftime 的 %f。
func padMicroseconds(micros int) string {
	return fmt.Sprintf("%06d", micros)
}

// splitPath 把路径拆成目录与文件名（Python 的 Path.parent / Path.name）。
func splitPath(path string) (string, string) {
	index := strings.LastIndexAny(path, `\/`)
	if index < 0 {
		return "", path
	}
	return path[:index], path[index+1:]
}

// joinPath 拼接目录与文件名。
func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + string(os.PathSeparator) + name
}

// sortedStrings 就地升序排序（Python 的 sorted）。
func sortedStrings(values []string) []string {
	out := append([]string{}, values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
