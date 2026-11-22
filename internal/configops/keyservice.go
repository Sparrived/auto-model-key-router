package configops

import (
	"sort"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// KeyServiceModels 返回「当前绑定了该 provider key」的模型 ID（升序）。
//
// 对齐 config_operations.py:372。Python 返回 set[str]；Go 侧排序后返回，避免
// 调用方依赖不可复现的集合顺序。
func KeyServiceModels(data *canonical.Value, providerID, keyName string) ([]string, error) {
	return keyServiceModels(data, providerID, keyName, false)
}

// KeyUpstreamModels 汇总该 Key 的目标实际发给上游的模型名称。
func KeyUpstreamModels(data *canonical.Value, providerID, keyName string) ([]string, error) {
	return keyServiceModels(data, providerID, keyName, true)
}

func keyServiceModels(data *canonical.Value, providerID, keyName string, upstream bool) ([]string, error) {
	provider, err := RequireProvider(data, providerID)
	if err != nil {
		return nil, err
	}
	if _, err := RequireKey(provider, keyName); err != nil {
		return nil, err
	}
	result := map[string]bool{}
	all, err := Models(data)
	if err != nil {
		return nil, err
	}
	for _, pair := range objectItems(all) {
		if !pair.Value.IsObject() {
			continue
		}
		targets, err := ModelTargets(pair.Value)
		if err != nil {
			return nil, err
		}
		for _, target := range targets.Arr {
			if matchesServiceKey(target, providerID, keyName, upstream) {
				result[serviceModelName(pair.Key, target, upstream)] = true
			}
		}
	}
	return sortedSet(result), nil
}

func matchesServiceKey(target *canonical.Value, providerID, keyName string, upstream bool) bool {
	if upstream {
		return strings.TrimSpace(lookup(target, "provider").StringValue()) == providerID &&
			strings.TrimSpace(lookup(target, "key").StringValue()) == keyName
	}
	return pyEqualsString(lookup(target, "provider"), providerID) && pyEqualsString(lookup(target, "key"), keyName)
}

func serviceModelName(routeID string, target *canonical.Value, upstream bool) string {
	if upstream {
		if name := strings.TrimSpace(lookup(target, "upstream_model").StringValue()); name != "" {
			return name
		}
	}
	// 未填写上游名称时，运行时也会把路由 ID 发给上游。
	return routeID
}

// KeyServiceModelsResult 是 SetKeyServiceModels 的结果。
//
// 三个列表在 Python 侧就是 sorted(...)，因此顺序是契约的一部分。字段顺序与
// Python 返回的字典键顺序（added、removed、models_removed）一致。
type KeyServiceModelsResult struct {
	Added         []string
	Removed       []string
	ModelsRemoved []string
}

// Value 把结果转成与 Python 返回值逐字节一致的 JSON 对象（键顺序固定）。
func (r KeyServiceModelsResult) Value() *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "added", Value: canonical.NewStringArray(r.Added)},
		canonical.ObjectPair{Key: "removed", Value: canonical.NewStringArray(r.Removed)},
		canonical.ObjectPair{Key: "models_removed", Value: canonical.NewStringArray(r.ModelsRemoved)},
	)
}

// SetKeyServiceModels 原子地把「该 provider key 服务哪些模型」设成 modelIDs。
//
// 对齐 config_operations.py:391。行为要点：
//
//   - 每个目标模型不存在就创建，并追加一条以**模型 ID 作为 upstream_model** 的
//     target；
//   - 之前绑定该 key 但这次没被勾选的模型，会丢掉所有引用该 key 的 target；
//     因此变成没有 target 的模型会被整条删掉；
//   - 只要有 removed，就顺手 repair_model_references，避免留下指向已删模型的
//     unified/任务引用。
//
// 与 Python 的唯一差别：desired 在 Python 里是 set，**一次新增多个模型**时写入
// models 的顺序取决于字符串哈希（进程间随机）；Go 侧按调用方给出的顺序去重后
// 依次创建，由 TestSetKeyServiceModelsCreatesModelsInCallerOrder 钉住。
func SetKeyServiceModels(data *canonical.Value, providerID, keyName string, modelIDs []string) (KeyServiceModelsResult, error) {
	return setKeyServiceModels(data, providerID, keyName, modelIDs, false)
}

// SetKeyUpstreamModels 只解绑未选的目标，以免误删同一路由的其他上游模型。
func SetKeyUpstreamModels(data *canonical.Value, providerID, keyName string, names []string) error {
	_, err := setKeyServiceModels(data, providerID, keyName, names, true)
	return err
}

func setKeyServiceModels(data *canonical.Value, providerID, keyName string, modelIDs []string, upstream bool) (KeyServiceModelsResult, error) {
	provider, err := RequireProvider(data, providerID)
	if err != nil {
		return KeyServiceModelsResult{}, err
	}
	if _, err := RequireKey(provider, keyName); err != nil {
		return KeyServiceModelsResult{}, err
	}
	desiredList := make([]string, 0, len(modelIDs))
	desired := map[string]bool{}
	for _, item := range modelIDs {
		id, err := nonEmptyString(item, "模型 ID")
		if err != nil {
			return KeyServiceModelsResult{}, err
		}
		if !desired[id] {
			desired[id] = true
			desiredList = append(desiredList, id)
		}
	}

	bound := map[string]bool{}
	boundOrder := []string{}
	all, err := Models(data)
	if err != nil {
		return KeyServiceModelsResult{}, err
	}
	for _, pair := range objectItems(all) {
		if !pair.Value.IsObject() {
			continue
		}
		targets, err := ModelTargets(pair.Value)
		if err != nil {
			return KeyServiceModelsResult{}, err
		}
		hasBoundTarget := false
		for _, target := range targets.Arr {
			if matchesServiceKey(target, providerID, keyName, upstream) {
				bound[serviceModelName(pair.Key, target, upstream)] = true
				hasBoundTarget = true
			}
		}
		if hasBoundTarget {
			boundOrder = append(boundOrder, pair.Key)
		}
	}

	added := []string{}
	removed := []string{}
	removedModels := []string{}
	for _, modelID := range desiredList {
		if bound[modelID] {
			continue
		}
		// Python 的 `all_models.get(id)` 对「键缺失」与「值为 null」都返回 None，
		// 于是两者都会走 create_model——而 create_model 对后者报 409（键已存在）。
		if !all.Obj.Has(modelID) || lookup(all, modelID).IsNull() {
			if _, err := CreateModel(data, modelID, CreateModelOptions{}); err != nil {
				return KeyServiceModelsResult{}, err
			}
		}
		if err := AddModelTarget(data, modelID, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "provider", Value: canonical.NewString(providerID)},
			canonical.ObjectPair{Key: "key", Value: canonical.NewString(keyName)},
			canonical.ObjectPair{Key: "upstream_model", Value: canonical.NewString(modelID)},
		)); err != nil {
			return KeyServiceModelsResult{}, err
		}
		added = append(added, modelID)
	}

	for _, modelID := range boundOrder {
		targets, err := ModelTargets(lookup(all, modelID))
		if err != nil {
			return KeyServiceModelsResult{}, err
		}
		remaining := filterTargets(targets.Arr, func(target *canonical.Value) bool {
			return !matchesServiceKey(target, providerID, keyName, upstream) || desired[serviceModelName(modelID, target, upstream)]
		})
		if len(remaining) == len(targets.Arr) {
			continue
		}
		targets.Arr = remaining
		removed = append(removed, modelID)
		if len(targets.Arr) == 0 {
			all.DeleteKey(modelID)
			removedModels = append(removedModels, modelID)
		}
	}
	if len(removed) > 0 {
		if err := RepairModelReferences(data); err != nil {
			return KeyServiceModelsResult{}, err
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(removedModels)
	return KeyServiceModelsResult{Added: added, Removed: removed, ModelsRemoved: removedModels}, nil
}

// RegenerateLocalAPIKey 换掉本地鉴权 key。
//
// 对齐 config_operations.py:559。校验（422）先于赋值，因此失败不会留下半个改动。
func RegenerateLocalAPIKey(data *canonical.Value, apiKey string) error {
	secret, err := nonEmptyString(apiKey, "本地鉴权 key")
	if err != nil {
		return err
	}
	data.SetKey("local_api_key", canonical.NewString(secret))
	return nil
}

// UpdateSettings 逐项校验并写入设置项。
//
// 对齐 config_operations.py:563。updates 是一个 JSON 对象，键即 Python 的
// **kwargs，键顺序会被保留（data.update 按顺序写入）。
//
// 两个必须保留的细节：
//
//   - host 校验通过后写回的是**去空白后的字符串**，而不是原值；
//   - 校验只针对键存在且值非 null 的项；值为 null 的项照样会被 data.update
//     写进配置（Python 的 `updates.get(k) is not None` 只跳过 None，不跳过
//     「键不存在」——而 None 值会被原样 update 进去）。
func UpdateSettings(data *canonical.Value, updates *canonical.Value) error {
	if updates != nil && !updates.IsObject() {
		return opErr(400, "updates 必须是对象")
	}
	host, hasHost := getOr(updates, "host")
	if hasHost && !host.IsNull() {
		value, err := nonEmpty(host, "监听地址")
		if err != nil {
			return err
		}
		if strings.Contains(value, "://") || strings.Contains(value, "/") {
			return opErr(422, "监听地址只填写 IP 或主机名，不要包含协议或路径")
		}
		updates.SetKey("host", canonical.NewString(value))
	}
	port, hasPort := getOr(updates, "port")
	if hasPort && !port.IsNull() {
		number, err := canonical.ToInt(port)
		if err != nil {
			return err
		}
		if number < 1 || number > 65535 {
			return opErr(422, "端口范围必须是 1-65535")
		}
	}
	for _, field := range []string{"request_timeout", "stream_first_byte_timeout", "stream_idle_timeout"} {
		value, ok := getOr(updates, field)
		if !ok || value.IsNull() {
			continue
		}
		number, err := canonical.ToFloat(value)
		if err != nil {
			return err
		}
		if number <= 0 {
			return opErr(422, "超时必须大于 0")
		}
	}
	retries, hasRetries := getOr(updates, "max_retries")
	if hasRetries && !retries.IsNull() {
		number, err := canonical.ToInt(retries)
		if err != nil {
			return err
		}
		if number < 0 {
			return opErr(422, "max_retries 不能小于 0")
		}
	}
	for _, pair := range objectItems(updates) {
		data.SetKey(pair.Key, pair.Value)
	}
	return nil
}
