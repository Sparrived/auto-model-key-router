package keypool

import (
	"errors"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// QuotaFreezes 是选路层需要的「额度冻结」最小读接口。
//
// 刻意不 import internal/quota：选路层不该依赖状态文件包的存储细节（那会把
// 「挑 key」与「落盘 JSON」绑在一起），而额度包由另一条并行任务在改。装配层手里的
// *quota.Store 天然满足它（IsFrozen / FrozenReason 签名一致），不需要适配器；
// 测试里用一个查表的假实现即可。
//
// 基本接口只保留两个方法：这是选路层真正依赖的最小面，旧实现/测试假实现不必为了
// 白名单增加方法而重写。白名单需要知道冻结类型时，再可选探测下面的
// quotaFreezeTrigger；探测不到就按 hard 处理（宁可少放行，不把耗尽额度打出去）。
type QuotaFreezes interface {
	IsFrozen(providerID, keyName string) bool
	FrozenReason(providerID, keyName string) string
}

// quotaFreezeTrigger 是 QuotaFreezes 的可选扩展：返回 hard / threshold。
// 只有提前冻结能被模型白名单豁免，硬性耗尽永远不豁免。
type quotaFreezeTrigger interface {
	FrozenTrigger(providerID, keyName string) string
}

// ErrQuotaFrozen 表示模型的候选 Key 全部因额度冻结被停用。
//
// 与 ErrNoUsableKey 分开，因为两者的语义与处置完全不同：没有可用 key 是**配置问题**
// （没配、人工停用、作用域外），而额度冻结是**临时状态**——5h/7d 等硬性窗口耗尽后
// 自动冻结，窗口重置或观测到有余额后自动解除，配置完全正确。调用方据此回 503，并且
// 文案能指出「不是配置错了，是额度用完了、什么时候重置」（见 quotaFrozenError）。
//
// **硬约束语义**：与并发上限同级，冷却的软回退（全部冷却时放行冷却中的 Key）**不**
// 放行冻结中的 Key；放行一次就是把一个必然失败（或必然被上游拒绝）的请求打出去。
var ErrQuotaFrozen = errors.New("额度冻结")

// quotaFrozenError 描述「哪些 Key 因为什么被冻结」。
//
// 文本形如「模型 X 的 Key 因额度冻结停用：provider/key：原因；provider/key：原因」。
// 与既有错误类型同一取舍：Error() 只输出人话，errors.Is 靠自定义 Is 保判定性，不把
// 哨兵文本拼进消息（那会让「模型 X 的 Key 因额度冻结停用」变成「额度冻结: …」）。
type quotaFrozenError struct {
	modelID string
	// entries 是「provider/key：原因」，保持配置顺序（同一作用域内稳定）。
	entries []string
}

func (e *quotaFrozenError) Error() string {
	return "模型 " + e.modelID + " 的 Key 因额度冻结停用：" + strings.Join(e.entries, "；")
}

func (e *quotaFrozenError) Is(target error) bool { return target == ErrQuotaFrozen }

// SetQuotaFreezes 注入额度冻结判定；nil 表示不启用（测试默认，也是嵌入场景）。
//
// 与 ApplyConfig 同一把锁：注入发生在装配阶段，但选路是并发的，读写必须同锁。
//
// 注意热重载：配置重载会新建 KeyPool，**不会**继承这里的注入；装配层负责重新调用本
// 方法（冻结表本身不随配置重建，那是持久化状态）。
func (p *KeyPool) SetQuotaFreezes(freezes QuotaFreezes) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quotaFreezes = freezes
}

// FrozenKeyCount 返回模型候选里因额度冻结被剔除的 Key 数（含访问密钥作用域过滤）。
//
// 只给诊断用（回答「为什么这个模型一把 Key 都用不了」），不参与任何决策：排除逻辑在
// keysForModelLocked 里，两处各算一份迟早会漂移。与 BalanceLimitedKeys 同一取舍。
func (p *KeyPool) FrozenKeyCount(modelID string, accessKey *config.AccessKeyConfig) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.frozenCandidatesLocked(p.resolveModelID(modelID), accessKey))
}

// QuotaFrozenError 返回「该模型在本作用域内的候选**全部**因额度冻结不可用」的错误；
// 只要还有一把未冻结（或压根没有启用候选）就返回 nil。
//
// 给代理层的模型校验阶段用：那一阶段看的是 KeyCount，而 KeyCount 走的是同一份候选
// 过滤（全部冻结时它为 0）。不加区分就会把「额度用完了」报成 404「模型未配置」，
// 把运维引向配置检查——而这里配置完全正确（docs/design/quota-freeze.md §1）。
//
// 返回的错误与 NextKey 在同样局面下返回的是同一个类型、同一套文案，因此「谁先发现」
// 不影响对外说法：都是 503 + 额度冻结原因。
func (p *KeyPool) QuotaFrozenError(modelID string, accessKey *config.AccessKeyConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	resolved := p.resolveModelID(modelID)
	enabled := p.enabledKeysForModelLocked(resolved, accessKey)
	if len(enabled) == 0 {
		return nil
	}
	frozen := make([]config.KeyConfig, 0, len(enabled))
	for _, key := range enabled {
		if !p.frozenForModelLocked(resolved, key) {
			return nil
		}
		frozen = append(frozen, key)
	}
	return p.quotaFrozenErrorLocked(resolved, frozen)
}

// frozenLocked 报告该 Key 是否有一条额度冻结记录。调用方须持 p.mu。
func (p *KeyPool) frozenLocked(key config.KeyConfig) bool {
	return p.quotaFreezes != nil && p.quotaFreezes.IsFrozen(key.Provider, key.Name)
}

// frozenForModelLocked 报告该 Key 对指定模型是否不可用。
//
// 提前冻结（trigger=threshold）只停用未进入 quota_freeze_threshold_whitelist 的模型；
// 硬性耗尽（含旧记录/未知 trigger）对所有模型都不可豁免。白名单按真实模型 ID 存放，
// 解析与别名归一在 applyConfig 完成，因此这里只做一次 map 查找。
func (p *KeyPool) frozenForModelLocked(modelID string, key config.KeyConfig) bool {
	if !p.frozenLocked(key) {
		return false
	}
	if !p.thresholdWhitelist[modelID] {
		return true
	}
	trigger, ok := p.quotaFreezes.(quotaFreezeTrigger)
	if !ok {
		// 旧实现没有提供冻结类型：按 hard 处理，宁可不豁免。
		return true
	}
	return trigger.FrozenTrigger(key.Provider, key.Name) != "threshold"
}

// frozenReasonLocked 返回冻结的人话原因；未冻结返回空串。调用方须持 p.mu。
func (p *KeyPool) frozenReasonLocked(key config.KeyConfig) string {
	if p.quotaFreezes == nil {
		return ""
	}
	return p.quotaFreezes.FrozenReason(key.Provider, key.Name)
}

// frozenCandidatesLocked 返回「配置启用且落在作用域内、但对当前模型不可用」的冻结
// 候选，保持配置顺序。提前冻结对白名单模型不算不可用，因此不会进入这个集合。
// 调用方须持 p.mu。
//
// 必须在 keysForModelLocked（已剔除冻结）之外单独扫一遍：候选集合被冻结清空时，
// 错误文案要说清是额度冻结而不是「没有可用 key」，而那时冻结集合已经被过滤掉了。
func (p *KeyPool) frozenCandidatesLocked(modelID string, accessKey *config.AccessKeyConfig) []config.KeyConfig {
	if p.quotaFreezes == nil {
		return nil
	}
	var frozen []config.KeyConfig
	for _, key := range p.enabledKeysForModelLocked(modelID, accessKey) {
		if p.frozenForModelLocked(modelID, key) {
			frozen = append(frozen, key)
		}
	}
	return frozen
}

// quotaFrozenErrorLocked 组装「哪些 Key 因什么被冻结」的错误。调用方须持 p.mu。
func (p *KeyPool) quotaFrozenErrorLocked(modelID string, keys []config.KeyConfig) *quotaFrozenError {
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		reason := p.frozenReasonLocked(key)
		if reason == "" {
			// IsFrozen 为真而 FrozenReason 为空，只可能发生在实现不自洽时（两处各查
			// 一次，冻结恰好在这中间到期）。仍然说得出「额度已耗尽」，而不是留一个
			// 空冒号让人以为这是别的原因。
			reason = "额度已耗尽"
		}
		entries = append(entries, key.Provider+"/"+key.Name+"："+reason)
	}
	return &quotaFrozenError{modelID: modelID, entries: entries}
}
