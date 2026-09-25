package configops

import (
	"sort"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// cpaInstancesKey 是 CPA 实例清单在配置里的顶层键。
//
// 为什么不放进 /api/settings：settings 那批字段是参照实现已发布的对外契约，形状与
// 错误文本都要逐字对齐（见 internal/api/validate.go 的 specSettingsUpdate）；CPA 实例
// 是 Go 侧新增能力，跟着 workspacePatterns 那批走（见 internal/api/router.go）。给它
// 一个独立顶层键，两侧的契约就互不牵动。
//
// 顶层新增键是安全的：配置解析容忍未知字段、写回时原样保留
// （TestUnknownFieldsSurviveRoundTrip），因此旧版本二进制读一次配置不会把它删掉。
const cpaInstancesKey = "cpa_instances"

// CPAInstance 是一个已配置的 CPA 实例。
//
// management_key 是 CPA **管理面**的密钥（`/v0/management/*`），不是模型推理 key：
// 两者用途不同，混用会拿到 401。
type CPAInstance struct {
	ID            string
	Label         string
	BaseURL       string
	ManagementKey string
}

// CPAInstances 返回配置里的 CPA 实例如射；缺失时返回空对象，**不写入配置**。
//
// 读路径刻意不建键：只看一眼账号资源不该改动配置文件（与 Providers 一致）。
func CPAInstances(data *canonical.Value) (*canonical.Value, error) {
	if data == nil || !data.IsObject() {
		return canonical.NewObject(), nil
	}
	raw := data.Lookup(cpaInstancesKey)
	if raw == nil || raw.IsNull() {
		return canonical.NewObject(), nil
	}
	if !raw.IsObject() {
		return nil, opErr(422, "cpa_instances 必须是对象")
	}
	return raw, nil
}

// ReplaceCPAInstances 用调用方给的清单**整体替换** cpa_instances。
//
// 整体替换而不是逐条增删：界面上的实例通常两三个，一次提交整份清单既省掉三条接口，
// 也让「删掉一个实例」和「改一个实例」走同一条读-改-写（由 config_revision 兜住并发）。
func ReplaceCPAInstances(data *canonical.Value, incoming *canonical.Value) error {
	if data == nil || !data.IsObject() {
		return opErr(500, "配置根必须是对象")
	}
	if incoming == nil || !incoming.IsObject() {
		return opErr(422, "instances 必须是对象")
	}
	normalized := canonical.NewObject()
	for _, id := range incoming.Obj.Keys() {
		raw, _ := incoming.Obj.Get(id)
		trimmed, err := nonEmptyString(id, "实例 ID")
		if err != nil {
			return err
		}
		// JSON 对象解析后不会有重复键（后者覆盖前者），但 "a" 与 " a " 去空白后会撞车，
		// 后者若悄悄覆盖前者，用户会以为实例还在。先判重复、再校验条目：撞车是 ID
		// 的问题，此时报「第二个实例的 base_url 不对」会把人引到错的地方。
		if normalized.Obj.Has(trimmed) {
			return opErrf(409, "实例 ID 重复: %s", trimmed)
		}
		entry, err := normalizeCPAInstance(raw, trimmed)
		if err != nil {
			return err
		}
		normalized.SetKey(trimmed, entry)
	}
	data.SetKey(cpaInstancesKey, normalized)
	return nil
}

// normalizeCPAInstance 校验单个实例并返回写回配置的对象。
//
// 从调用方对象克隆再覆盖三个已知键，而不是重新拼一个：将来给实例加字段时，旧版本
// 保存一次配置不该把新字段抹掉（与配置整体的取舍一致）。
func normalizeCPAInstance(raw *canonical.Value, id string) (*canonical.Value, error) {
	if raw == nil || !raw.IsObject() {
		return nil, opErrf(422, "实例 %s 必须是对象", id)
	}
	baseRaw := raw.Lookup("base_url")
	if !baseRaw.IsString() {
		return nil, opErrf(422, "实例 %s 的 base_url 必须是字符串", id)
	}
	baseURL, err := NormalizeBaseURL(baseRaw)
	if err != nil {
		return nil, err
	}
	keyRaw := raw.Lookup("management_key")
	if !keyRaw.IsString() {
		return nil, opErrf(422, "实例 %s 的 management_key 必须是字符串", id)
	}
	managementKey, err := nonEmptyString(keyRaw.StringValue(), "management_key")
	if err != nil {
		return nil, err
	}
	entry := raw.Clone()
	entry.SetKey("base_url", canonical.NewString(baseURL))
	entry.SetKey("management_key", canonical.NewString(managementKey))
	// 没起名字就用 ID：界面上总得有个可读的标题，让前端自己兜底会把同一件事
	// 分散到两个地方。
	label := id
	if labelRaw := raw.Lookup("label"); labelRaw.IsString() {
		if trimmed := strings.TrimSpace(labelRaw.StringValue()); trimmed != "" {
			label = trimmed
		}
	}
	entry.SetKey("label", canonical.NewString(label))
	return entry, nil
}

// ParseCPAInstances 把实例映射解析成按 ID 排序的 Go 结构，供网络扇出使用。
//
// 刻意宽松：字段缺失或类型不对的条目照样返回，由调用方给出**逐实例**的错误提示。
// 这里直接报错会让一个手改坏的实例挡住整个看板——那正是最需要看板的时候。
func ParseCPAInstances(data *canonical.Value) ([]CPAInstance, error) {
	instances, err := CPAInstances(data)
	if err != nil {
		return nil, err
	}
	ids := instances.Obj.Keys()
	sort.Strings(ids)
	out := make([]CPAInstance, 0, len(ids))
	for _, id := range ids {
		raw, _ := instances.Obj.Get(id)
		item := CPAInstance{ID: id, Label: id}
		if raw.IsObject() {
			if label := strings.TrimSpace(raw.Lookup("label").StringValue()); label != "" {
				item.Label = label
			}
			item.BaseURL = strings.TrimRight(strings.TrimSpace(raw.Lookup("base_url").StringValue()), "/")
			item.ManagementKey = strings.TrimSpace(raw.Lookup("management_key").StringValue())
		}
		out = append(out, item)
	}
	return out, nil
}
