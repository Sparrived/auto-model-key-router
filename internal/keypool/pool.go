package keypool

import (
	"errors"
	"slices"
	"strconv"
	"sync"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/endpoint"
	"github.com/Sparrived/auto-model-key-router/internal/formatting"
)

// accessKeyProviders 返回某把访问密钥允许的供应商集合；nil 表示不限制。
//
// 调用方（proxy / app 面）手里有一把 *config.AccessKeyConfig，nil 表示「完整权限」。
// 把判定收在这里而不是散布到各调用点：供应商过滤必须与「挑选上游 key」在同一处锁内
// 完成，否则会出现「先选中一把 key、再发现无权」的中间态。
func accessKeyProviders(key *config.AccessKeyConfig) map[string]bool {
	if key == nil || key.Providers == nil {
		return nil
	}
	allowed := make(map[string]bool, len(key.Providers))
	for _, provider := range key.Providers {
		allowed[provider] = true
	}
	return allowed
}

// accessKeyStickyID 返回粘滞映射里代表该作用域的标识。
//
// 必须参与粘滞键：两把访问密钥对同一模型可以有不同供应商清单，共用一个粘滞键会让
// 清单更窄的那把被粘到一把它无权使用的上游 key 上。完整权限用空串，既有行为不变。
func accessKeyStickyID(key *config.AccessKeyConfig) string {
	if key == nil {
		return ""
	}
	return key.ID
}

// 错误分类与参照实现的异常一一对应，且**文本逐字一致**：
// Python 的 `KeyError(x)` 文本是 repr，即带引号的 `'x'`；RuntimeError 直接是消息。
// 用自定义类型而非 fmt.Errorf 包装，是为了让 Error() 只输出参照实现的文本，
// 同时保留 errors.Is 的可判定性（Go 惯例的 "sentinel: detail" 会污染文本）。
var (
	ErrNoUsableKey    = errors.New("没有可用 key")
	ErrUnknownModel   = errors.New("未配置的模型")
	ErrNoUnifiedModel = errors.New("未配置 unified_model")
)

// noUsableKeyError 对应 RuntimeError(f"模型 {model_id} 没有可用 key")。
type noUsableKeyError struct{ modelID string }

func (e *noUsableKeyError) Error() string { return "模型 " + e.modelID + " 没有可用 key" }
func (e *noUsableKeyError) Is(target error) bool {
	return target == ErrNoUsableKey
}

// unknownModelError 对应 KeyError(model_id)。
type unknownModelError struct{ modelID string }

func (e *unknownModelError) Error() string { return "'" + e.modelID + "'" }
func (e *unknownModelError) Is(target error) bool {
	return target == ErrUnknownModel
}

// missingKeyError 对应 RuntimeError(f"模型 {model_id} 未配置 key: {key_name}")。
type missingKeyError struct {
	modelID string
	keyName string
}

func (e *missingKeyError) Error() string {
	return "模型 " + e.modelID + " 未配置 key: " + e.keyName
}
func (e *missingKeyError) Is(target error) bool { return target == ErrNoUsableKey }

// noUnifiedModelError 对应 resolve_unified_plan 的 KeyError(UNIFIED_MODEL_ID)。
type noUnifiedModelError struct{}

func (e *noUnifiedModelError) Error() string { return "'" + config.UNIFIED_MODEL_ID + "'" }
func (e *noUnifiedModelError) Is(target error) bool {
	return target == ErrNoUnifiedModel
}

// ErrConcurrencyLimited 表示候选 Key 或供应商的并发已满（配置声明的并发上限）。
//
// 与 ErrNoUsableKey 分开，是因为两者的语义与处置完全不同：没有可用 key 是**配置问题**
// （都不存在、都停用、都欠费），而并发满是**容量问题**——配置完全正确，只是此刻排满了。
// 调用方据此回 503 还是 429、要不要重试，靠这一个哨兵就能分开。
var ErrConcurrencyLimited = errors.New("并发已达上限")

// concurrencyLimitedError 描述「谁满了」。
//
// 对外文本以「模型 X 并发已达上限：」开头，后面接具体那一层：供应商上限是最常见的
// 解释（一个账号池的上限会让整批候选一起没空），Key 级用尽时如实说候选都用满了。
type concurrencyLimitedError struct {
	modelID string
	detail  string
}

func (e *concurrencyLimitedError) Error() string {
	return "模型 " + e.modelID + " 并发已达上限：" + e.detail
}
func (e *concurrencyLimitedError) Is(target error) bool { return target == ErrConcurrencyLimited }

// KeyPool 负责按路由模式挑选 key，并管理并发计数、粘滞与冷却。
//
// 并发计数用 mutex 保护而非 Python 的 asyncio.Lock：那只锁保护的是字典更新的
// 原子性，Go 侧同样需要，但应该是普通互斥锁而非协程锁。
type KeyPool struct {
	mu sync.Mutex

	keys            map[string][]config.KeyConfig
	routingModes    map[string]string
	reasoningEffort map[string]string
	aliases         map[string]string

	// credentials 是 (模型真实 ID, key 名) → 上游账号标识，见 credentialID。
	//
	// 之所以预先算好而不是在选路时现算：选路在每次请求的关键路径上，而账号标识是
	// sha256；配置里的 key 数量固定，算一次就够，配置热重载时自然重建。
	credentials map[[2]string]string

	// providersOf 是 (模型真实 ID, key 名) → 供应商 ID，与 credentials 同一批重建。
	//
	// 归还并发计数时只有 (模型, key 名) 可用，而供应商那一层计数必须靠它定位；
	// 去 p.keys 里线性扫一遍也能得到，但那会把归还路径与配置切片的形状绑在一起。
	providersOf map[[2]string]string

	// keyLimits / providerLimits 是配置声明的并发上限，只收 >0 的条目
	// （0 与缺省同义：不限制，不必留在表里参与每次判断）。
	//
	// keyLimits 的键是**账号标识**而不是 (模型, key 名)：同一把上游 Key 被多个模型
	// 引用时，它同时承受这些模型的流量，上限必须按那把 key 本身算，否则「单 Key 限流 5」
	// 在两个模型上会变成 10。
	keyLimits      map[string]int
	providerLimits map[string]int

	failureThreshold int
	cooldownSeconds  float64
	// balanceCooldownSeconds 是「余额不足」的**首次**限流窗口；后续命中按次数翻倍，
	// 上限 MaxBalanceCooldownSeconds。0 表示关闭这项机制。
	balanceCooldownSeconds float64

	// unifiedPlans 是 unified-model 伪模型的分族计划，键即
	// config.UnifiedPlanNames 里的名字（default / image / embeddings / speech /
	// transcriptions / video / rerank）。
	//
	// 用 map 而不是一组具名字段：族数从 3 涨到 7 之后，每加一族都要在结构体、
	// applyConfig、计划选择、展示四处各加一行，而漏掉任何一处的表现都是「配置写了，
	// 但请求仍落到 default」。键集合由 config.UnifiedPlanNames 定义，未知键查不到。
	unifiedPlans map[string]*config.RoutePlan
	// taskPlans / taskParams 的键是 (工作空间, 任务名)：任务名只在工作空间内唯一，
	// 用单个字符串当键会让两个空间里的同名任务互相覆盖。
	taskPlans  map[[2]string]config.RoutePlan
	taskParams map[[2]string]*canonical.Value

	cursors        map[string]int
	activeRequests map[[2]string]int
	stickyKeys     map[[3]string]string

	// activeByKey / activeByProvider 是并发上限的**在途计数**，与 activeRequests
	// 一样跨热重载保留（在途请求不会因为一次配置改动而消失）。
	activeByKey      map[string]int
	activeByProvider map[string]int

	health       *KeyHealthStore
	capabilities *EndpointCapabilityCache

	// capabilityStore 为 nil 时不持久化（测试用）。
	capabilityStore *CapabilityStore
}

// stickyKey 是粘滞映射的键：(模型, 作用域标识, 粘滞哈希)。
//
// 第二元是 accessKeyStickyID：同一模型下，不同访问密钥各有各的粘滞序列（互不干扰），
// 全权凭据用空串。见 accessKeyStickyID 的说明。
type stickyKey = [3]string

// applyConfig 装载配置。
//
// 可调用名只有模型的 ID 与 aliases（target 的 upstream_model 只是发给上游的名字，
// 不参与本地解析），因此这里的别名表与 `/v1/models` 的清单恒等。
func (p *KeyPool) applyConfig(cfg *config.RouterConfig) {
	p.keys = map[string][]config.KeyConfig{}
	p.routingModes = map[string]string{}
	p.reasoningEffort = map[string]string{}
	for _, model := range cfg.Models {
		p.keys[model.ID] = model.Keys
		p.routingModes[model.ID] = model.RoutingMode
		p.reasoningEffort[model.ID] = model.ReasoningEffort
	}
	p.failureThreshold = cfg.KeyFailureThreshold
	p.cooldownSeconds = cfg.KeyCooldownSeconds
	p.balanceCooldownSeconds = cfg.KeyBalanceCooldownSeconds

	p.credentials = map[[2]string]string{}
	p.providersOf = map[[2]string]string{}
	p.keyLimits = map[string]int{}
	p.providerLimits = map[string]int{}
	for _, provider := range cfg.Providers {
		if provider.MaxConcurrency > 0 {
			p.providerLimits[provider.ID] = provider.MaxConcurrency
		}
	}
	for _, model := range cfg.Models {
		for _, key := range model.Keys {
			tuple := [2]string{model.ID, key.Name}
			credential := credentialID(key)
			p.credentials[tuple] = credential
			p.providersOf[tuple] = key.Provider
			if key.MaxConcurrency <= 0 || credential == "" {
				continue
			}
			// 同一把密钥写成两个 provider key 时（配置里不禁止），两者共享一个账号
			// 标识，也就共享同一个在途计数。此时取**更严**的那个上限：两个条目声明的
			// 是同一份真实配额，取大的等于凭空放宽。
			if existing, found := p.keyLimits[credential]; !found || key.MaxConcurrency < existing {
				p.keyLimits[credential] = key.MaxConcurrency
			}
		}
	}

	p.aliases = map[string]string{}
	for _, model := range cfg.Models {
		p.aliases[model.ID] = model.ID
		for _, alias := range model.Aliases {
			p.aliases[alias] = model.ID
		}
	}

	// unified 的分族计划：只收**已配置**的族（nil 表示该族没配，请求会回落到
	// default）。default 缺席即整个 unified-model 不可用（resolveUnifiedPlanLocked
	// 报 noUnifiedModelError）。
	p.unifiedPlans = map[string]*config.RoutePlan{}
	for _, planName := range config.UnifiedPlanNames {
		if plan := p.canonicalPlan(cfg.UnifiedModel, func(u *config.UnifiedModelConfig) *config.RoutePlan {
			return u.Plan(planName)
		}); plan != nil {
			p.unifiedPlans[planName] = plan
		}
	}

	p.taskPlans = map[[2]string]config.RoutePlan{}
	p.taskParams = map[[2]string]*canonical.Value{}
	for _, task := range cfg.Tasks {
		workspace := task.Workspace
		if workspace == "" {
			workspace = config.DefaultWorkspace
		}
		key := [2]string{workspace, task.Name}
		p.taskPlans[key] = p.canonicalizePlan(task.Plan())
		p.taskParams[key] = task.Params
	}
}

// canonicalPlan 取 unified_model 的某个计划并做别名归一；unified_model 为空或
// 该计划缺失时返回 nil。
func (p *KeyPool) canonicalPlan(
	unified *config.UnifiedModelConfig,
	pick func(*config.UnifiedModelConfig) *config.RoutePlan,
) *config.RoutePlan {
	if unified == nil {
		return nil
	}
	plan := pick(unified)
	if plan == nil {
		return nil
	}
	canonicalized := p.canonicalizePlan(*plan)
	return &canonicalized
}

// canonicalizePlan 把计划里的模型名（可能是别名）归一成真实 ID。
func (p *KeyPool) canonicalizePlan(plan config.RoutePlan) config.RoutePlan {
	result := config.RoutePlan{Primary: p.canonicalTarget(plan.Primary)}
	if plan.Fallback != nil {
		fallback := p.canonicalTarget(*plan.Fallback)
		result.Fallback = &fallback
	}
	return result
}

// canonicalTarget 归一单个路由目标。
func (p *KeyPool) canonicalTarget(target config.RouteTarget) config.RouteTarget {
	return config.RouteTarget{Model: p.resolveModelID(target.Model), Key: target.Key}
}

// New 按配置构造 KeyPool。capabilityStore 可为 nil（不持久化能力缓存）。
func New(cfg *config.RouterConfig, capabilityStore *CapabilityStore, clock func() float64) *KeyPool {
	if clock == nil {
		clock = RealClock
	}
	pool := &KeyPool{
		cursors:          map[string]int{},
		activeRequests:   map[[2]string]int{},
		activeByKey:      map[string]int{},
		activeByProvider: map[string]int{},
		stickyKeys:       map[stickyKey]string{},
		health:           NewKeyHealthStore(clock),
		capabilityStore:  capabilityStore,
	}
	var rawStates *canonical.Value
	if capabilityStore != nil {
		rawStates = capabilityStore.Load()
	}
	pool.capabilities = NewEndpointCapabilityCache(rawStates, clock)
	pool.applyConfig(cfg)
	return pool
}

// ApplyConfig 热重载配置，保留游标、并发计数、粘滞与健康状态。
//
// 对齐参照实现的行为：只替换配置派生的字段（_apply_config），其余状态沿用。
func (p *KeyPool) ApplyConfig(cfg *config.RouterConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyConfig(cfg)
}

// ModelIDs 返回所有模型真实 ID（排序）。
func (p *KeyPool) ModelIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return sortedMapKeys(p.keys)
}

// PublicModelIDs 返回对外可见的模型名（排序）。
//
// 可调用名与对外可见名是同一份集合（模型 ID + aliases），因此这里就是别名表的键。
func (p *KeyPool) PublicModelIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	for name := range p.aliases {
		seen[name] = true
	}
	return sortedSet(seen)
}

// AvailableModelIDs 返回当前确实有可用 key 的对外模型名（排序）。
//
// accessKey 非 nil 且配了供应商清单时，只考虑属于这些供应商的 key：一把访问密钥不该
// 在模型清单里看到自己一个上游都用不了的模型——那会让调用方以为能调，实际每次都 403。
// 这份过滤与选 key 走同一个 keysForModelLocked，因此「列出来的」与「调得动的」恒等。
func (p *KeyPool) AvailableModelIDs(accessKey *config.AccessKeyConfig) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := []string{}
	for name, modelID := range p.aliases {
		if len(p.keysForModelLocked(modelID, accessKey)) > 0 {
			result = append(result, name)
		}
	}
	slices.Sort(result)
	if p.unifiedPlans["default"] != nil {
		// 追加到末尾（参照实现用 append，不参与排序）。
		result = append(result, config.UNIFIED_MODEL_ID)
	}
	return result
}

// ResolveModelID 把模型名（ID 或别名）解析成真实 ID；未知名原样返回。
func (p *KeyPool) ResolveModelID(modelID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resolveModelID(modelID)
}

// resolveModelID 是无锁版本，调用方须持锁。
func (p *KeyPool) resolveModelID(modelID string) string {
	if resolved, found := p.aliases[modelID]; found {
		return resolved
	}
	return modelID
}

// ErrNoUnifiedModel 由上面的 noUnifiedModelError 承载，见文件顶部定义。

// ResolveRoute 返回 (真实模型 ID, key 名)。
//
// 对齐 key_pool.py:115：unified-model 按路径类型挑计划，任务名用任务计划且
// **不接受调用方指定的 key**，其余走别名解析。
//
// 入参 keyName 用 *string：Python 区分 `None`（未指定）与 `""`（由
// `model[key]` 语法显式指定的空白 key，proxy_support.py:77 的 strip() 也是来源），
// 前者保留计划原值、后者替换成空串。
//
// 返回的 key 用 string，"" 表示「无 key」。Go 侧把 None 与 "" 统一成 ""：
// 下游四处使用（proxy_handler.py:111/232/391/424）全部是真值判断，两者行为
// 完全相同，因此这里不做区分；测试对 key 位置做 null≡"" 归一后比较。
func (p *KeyPool) ResolveRoute(modelID string, keyName *string, path string) (string, string, error) {
	return p.ResolveRouteIn(config.DefaultWorkspace, modelID, keyName, path)
}

// ResolveRouteIn 是 ResolveRoute 的工作空间版本。
//
// 任务查表只在指定工作空间内进行：workspace 里没有这个任务名时按普通模型继续解析，
// 而不是去别的空间里找——否则「隔离」就名存实亡。
func (p *KeyPool) ResolveRouteIn(workspace, modelID string, keyName *string, path string) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if modelID == config.UNIFIED_MODEL_ID {
		plan, err := p.resolveUnifiedPlanLocked(RequestRouteKind(path), keyName)
		if err != nil {
			return "", "", err
		}
		return plan.Primary.Model, plan.Primary.Key, nil
	}
	if plan, found := p.taskPlans[taskKey(workspace, modelID)]; found {
		// 任务固定模型，不接受调用方指定的 Key。
		return plan.Primary.Model, plan.Primary.Key, nil
	}
	if keyName == nil {
		return p.resolveModelID(modelID), "", nil
	}
	return p.resolveModelID(modelID), *keyName, nil
}

// taskKey 归一化任务查表键：空工作空间视作默认工作空间。
func taskKey(workspace, taskName string) [2]string {
	if workspace == "" {
		workspace = config.DefaultWorkspace
	}
	return [2]string{workspace, taskName}
}

// TaskPlan 返回默认工作空间里任务名的路由计划。
func (p *KeyPool) TaskPlan(taskName string) (config.RoutePlan, bool) {
	return p.TaskPlanIn(config.DefaultWorkspace, taskName)
}

// TaskPlanIn 返回指定工作空间里任务名的路由计划。
func (p *KeyPool) TaskPlanIn(workspace, taskName string) (config.RoutePlan, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	plan, found := p.taskPlans[taskKey(workspace, taskName)]
	return plan, found
}

// TaskParams 返回默认工作空间里任务的固定参数（副本）。
func (p *KeyPool) TaskParams(taskName string) *canonical.Value {
	return p.TaskParamsIn(config.DefaultWorkspace, taskName)
}

// TaskParamsIn 返回指定工作空间里任务的固定参数（副本）。
func (p *KeyPool) TaskParamsIn(workspace, taskName string) *canonical.Value {
	p.mu.Lock()
	defer p.mu.Unlock()
	params, found := p.taskParams[taskKey(workspace, taskName)]
	if !found || params == nil {
		return canonical.NewObject()
	}
	return params.Clone()
}

// ResolveUnifiedPlan 按路径类型挑选 unified 计划。
func (p *KeyPool) ResolveUnifiedPlan(routeKind string, keyName *string) (config.RoutePlan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resolveUnifiedPlanLocked(routeKind, keyName)
}

// resolveUnifiedPlanLocked 是无锁版本，调用方须持锁。
//
// 指定的 keyName 会**替换** primary 的 key，但 fallback 原样保留（包括它自己的
// key）——这是参照实现的行为（key_pool.py:145）。keyName 为 nil（未指定）时
// 保留计划原值；非 nil 时空串也会替换（`unified-model[ ]` 可产出空串，
// proxy_support.py:77 的 strip() 是来源）。
func (p *KeyPool) resolveUnifiedPlanLocked(routeKind string, keyName *string) (config.RoutePlan, error) {
	// 按端点族挑计划；该族没配时回落到 default（该族请求仍可用，只是用对话模型，
	// 与本次改动前「非 image/embeddings 一律 default」的行为一致）。routeKind 的取值
	// 由 RequestRouteKind 给出，不在表里的名字自然落到 default。
	plan := p.unifiedPlans[routeKind]
	if plan == nil {
		plan = p.unifiedPlans["default"]
	}
	// 计划缺失时参照实现抛 KeyError(UNIFIED_MODEL_ID)，其文本是 repr（带引号）。
	if plan == nil {
		return config.RoutePlan{}, &noUnifiedModelError{}
	}
	if keyName == nil {
		return *plan, nil
	}
	return config.RoutePlan{
		Primary:  config.RouteTarget{Model: plan.Primary.Model, Key: *keyName},
		Fallback: plan.Fallback,
	}, nil
}

// UnifiedRoute 返回 unified 路由的展示结构；未启用 unified_model 时返回 nil。
//
// 键集合与顺序取自 config.UnifiedPlanNames，且**只列出已配置的族**：没配的族不出现在
// /health 里，界面据此能分清「这族没配」与「这族配了空值」。
func (p *KeyPool) UnifiedRoute() *canonical.Value {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unifiedPlans["default"] == nil {
		return nil
	}
	serialize := func(plan *config.RoutePlan) *canonical.Value {
		pairs := []canonical.ObjectPair{
			{Key: "model", Value: canonical.NewString(plan.Primary.Model)},
			{Key: "key", Value: nullable(plan.Primary.Key)},
		}
		result := canonical.NewObjectOf(canonical.ObjectPair{
			Key: "primary", Value: canonical.NewObjectOf(pairs...),
		})
		if plan.Fallback != nil {
			result.SetKey("fallback", canonical.NewObjectOf(
				canonical.ObjectPair{Key: "model", Value: canonical.NewString(plan.Fallback.Model)},
				canonical.ObjectPair{Key: "key", Value: nullable(plan.Fallback.Key)},
			))
		}
		return result
	}
	result := canonical.NewObject()
	for _, planName := range config.UnifiedPlanNames {
		if plan := p.unifiedPlans[planName]; plan != nil {
			result.SetKey(planName, serialize(plan))
		}
	}
	return result
}

// KeyCount 返回模型启用的 key 数（不按访问密钥作用域过滤）。
func (p *KeyPool) KeyCount(modelID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keysForModelLocked(p.resolveModelID(modelID), nil))
}

// KeyCountFor 返回模型在某把访问密钥作用域内可用的 key 数；accessKey 为 nil 时等同
// KeyCount。
//
// 与 KeyCount 分开而不是加个可选参数：调用点对「模型总共配了几把 key」与「本请求
// 能用几把」都要，混成一个函数会让「模型未配置」与「供应商不在清单里」两种情况
// 分不开——而它们该回 404 还是 403 是不同的。
func (p *KeyPool) KeyCountFor(modelID string, accessKey *config.AccessKeyConfig) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keysForModelLocked(p.resolveModelID(modelID), accessKey))
}

// RoutingMode 返回模型的路由模式（缺省 round_robin）。
func (p *KeyPool) RoutingMode(modelID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	mode, found := p.routingModes[p.resolveModelID(modelID)]
	if !found {
		return "round_robin"
	}
	return mode
}

// ReasoningEffort 返回模型配置的 reasoning_effort。
func (p *KeyPool) ReasoningEffort(modelID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reasoningEffort[p.resolveModelID(modelID)]
}

// KeysForModel 返回模型启用且落在作用域内的 key。
func (p *KeyPool) KeysForModel(modelID string, accessKey *config.AccessKeyConfig) []config.KeyConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keysForModelLocked(p.resolveModelID(modelID), accessKey)
}

// keysForModelLocked 是无锁版本，返回副本切片。
//
// accessKey 非 nil 且配了供应商清单时，只保留属于这些供应商的 key。过滤放在
// **候选集合**这一层而不是挑选之后：作用域之外的 key 根本不该进入候选，否则
// only_first 会先选中一把无权使用的 key 再失败。
func (p *KeyPool) keysForModelLocked(modelID string, accessKey *config.AccessKeyConfig) []config.KeyConfig {
	allowed := accessKeyProviders(accessKey)
	all := p.keys[modelID]
	result := make([]config.KeyConfig, 0, len(all))
	for _, key := range all {
		if !key.Enabled {
			continue
		}
		if allowed != nil && !allowed[key.Provider] {
			continue
		}
		result = append(result, key)
	}
	return result
}

// KeyByName 按名字取启用中的 key。
func (p *KeyPool) KeyByName(modelID, keyName string, accessKey *config.AccessKeyConfig) (config.KeyConfig, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	modelID = p.resolveModelID(modelID)
	keys, found := p.keys[modelID]
	if !found || len(keys) == 0 {
		return config.KeyConfig{}, &unknownModelError{modelID: modelID}
	}
	allowed := accessKeyProviders(accessKey)
	for _, key := range keys {
		if key.Name != keyName || !key.Enabled {
			continue
		}
		if allowed != nil && !allowed[key.Provider] {
			continue
		}
		return key, nil
	}
	return config.KeyConfig{}, &missingKeyError{modelID: modelID, keyName: keyName}
}

// NextKey 按路由模式挑选下一个可用 key。
//
// 四种模式的行为差异（对齐 key_pool.py:214）：
//   - only_first：只用第一个；被排除即失败，**不进入冷却判断**；
//   - priority：按配置顺序取第一个可用项，受冷却排除影响；
//   - round_robin：在并发数最低的候选里按游标轮转，支持粘滞；
//
// 冷却过滤是**软**的：可用集合为空时会回退到「仅排除 excluded」的集合，因此
// 冷却中的 key 仍可能被选中。这是刻意的降级策略，避免全部冷却时彻底不可用。
//
// **欠费是这条降级的唯一例外**：余额不足的账号在软回退里也不放行（剩下的候选仍会
// 借用冷却中的 key）。两条理由：冷却（429/5xx）是「暂时」，欠费是「现在一定不行」，
// 放它进来只会把同一个 402 原样转给调用方；而且放行一次就等于每来一个请求都试一次，
// 「自动限流」也就无从谈起。全部账号都欠费时返回「没有可用 key」（503），由调用方
// 决定怎么处理——这正是它比一个上游欠费错误更好处理的情形。
//
// **并发上限是第二个例外，而且是硬约束**：已满的 key 与已满的供应商一律不放行，
// 软回退也不放行——放行一次就是直接越过用户配的上限，而这个上限的意义正是「宁可
// 让调用方等」而不是「宁可把上游打满」。候选全被并发挡下时返回 concurrencyLimitedError
// （429），与「没有可用 key」区分开。
func (p *KeyPool) NextKey(modelID string, excluded []string, accessKey *config.AccessKeyConfig, affinityKey string) (config.KeyConfig, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	modelID = p.resolveModelID(modelID)
	excludedSet := map[string]bool{}
	for _, name := range excluded {
		excludedSet[name] = true
	}
	keys := p.keysForModelLocked(modelID, accessKey)
	if len(keys) == 0 {
		if len(p.keys[modelID]) > 0 {
			return config.KeyConfig{}, &noUsableKeyError{modelID: modelID}
		}
		return config.KeyConfig{}, &unknownModelError{modelID: modelID}
	}

	if p.routingModeLocked(modelID) == "only_first" {
		first := keys[0]
		if !excludedSet[first.Name] {
			if !p.hasCapacityLocked(modelID, first) {
				return config.KeyConfig{}, p.concurrencyLimitedErrorLocked(modelID, []config.KeyConfig{first})
			}
			p.acquireLocked(modelID, first.Name)
			return first, nil
		}
		return config.KeyConfig{}, &noUsableKeyError{modelID: modelID}
	}

	// blocked 只收「仅因并发上限被挡下」的候选，用来在最终失败时说清是谁满了。
	blocked := make([]config.KeyConfig, 0, len(keys))
	available := make([]config.KeyConfig, 0, len(keys))
	for _, key := range keys {
		if excludedSet[key.Name] || p.health.IsCoolingDown(modelID, key.Name) {
			continue
		}
		if p.balanceExhaustedLocked(modelID, key) {
			continue
		}
		if !p.hasCapacityLocked(modelID, key) {
			blocked = append(blocked, key)
			continue
		}
		available = append(available, key)
	}
	if len(available) == 0 {
		// 软回退：冷却中的 key 可以放行（既定降级），欠费的账号与并发满的 key 不行。
		for _, key := range keys {
			if excludedSet[key.Name] || p.balanceExhaustedLocked(modelID, key) {
				continue
			}
			if !p.hasCapacityLocked(modelID, key) {
				// 与第一轮一样记进 blocked：一个既在冷却、又已满的候选，说「并发满了」
				// 比说「没有可用 key」更接近事实，也更能指出该等什么。
				blocked = append(blocked, key)
				continue
			}
			available = append(available, key)
		}
	}
	if len(available) == 0 {
		if len(blocked) > 0 {
			return config.KeyConfig{}, p.concurrencyLimitedErrorLocked(modelID, blocked)
		}
		return config.KeyConfig{}, &noUsableKeyError{modelID: modelID}
	}

	if p.routingModeLocked(modelID) == "priority" {
		// 按 keys 的配置顺序（不是 available 的顺序）取第一个可用项。
		availableSet := map[string]bool{}
		for _, key := range available {
			availableSet[key.Name] = true
		}
		for _, candidate := range keys {
			if availableSet[candidate.Name] {
				p.acquireLocked(modelID, candidate.Name)
				return candidate, nil
			}
		}
		return config.KeyConfig{}, &noUsableKeyError{modelID: modelID}
	}

	var sticky stickyKey
	hasSticky := false
	if affinityKey != "" && p.routingModeLocked(modelID) == "round_robin" {
		sticky = stickyKey{modelID, accessKeyStickyID(accessKey), affinityKey}
		hasSticky = true

		if stickyName, found := p.stickyKeys[sticky]; found {
			for _, candidate := range available {
				if candidate.Name == stickyName {
					p.acquireLocked(modelID, candidate.Name)
					return candidate, nil
				}
			}
			delete(p.stickyKeys, sticky)
		}
	}

	lowestActive := -1
	for _, key := range available {
		active := p.activeRequests[[2]string{modelID, key.Name}]
		if lowestActive < 0 || active < lowestActive {
			lowestActive = active
		}
	}
	// 游标走 len(keys) 步，因此最多遍历一整轮；只有并发数恰好最低的候选被选中。
	for range len(keys) {
		cursor := p.cursors[modelID] % len(keys)
		p.cursors[modelID]++
		candidate := keys[cursor]
		if isInAvailable(available, candidate.Name) &&
			p.activeRequests[[2]string{modelID, candidate.Name}] == lowestActive {
			p.acquireLocked(modelID, candidate.Name)
			if hasSticky {
				p.stickyKeys[sticky] = candidate.Name
			}
			return candidate, nil
		}
	}
	return config.KeyConfig{}, &noUsableKeyError{modelID: modelID}
}

// hasCapacityLocked 报告该候选此刻还有并发余量（供应商层与账号层都要有余量）。
//
// 两层上限都只在该层配了上限时判定，且判定用的是**在途数**而不是「已选中数」：
// 在途包含正在流式返回的那些请求，而流式恰恰是最占上游并发的那种。
func (p *KeyPool) hasCapacityLocked(modelID string, key config.KeyConfig) bool {
	if limit := p.providerLimits[key.Provider]; limit > 0 && p.activeByProvider[key.Provider] >= limit {
		return false
	}
	credential := p.credentials[[2]string{modelID, key.Name}]
	if limit := p.keyLimits[credential]; limit > 0 && p.activeByKey[credential] >= limit {
		return false
	}
	return true
}

// concurrencyLimitedErrorLocked 给「候选全被并发挡下」凑一句能指明谁满了的说明。
//
// 优先说供应商：一个账号池的上限通常一次性解释了整批候选为什么都没空；只有 Key 级
// 各满各的（或供应商没配上限）时，才如实说候选 Key 都用满了。
func (p *KeyPool) concurrencyLimitedErrorLocked(modelID string, blocked []config.KeyConfig) *concurrencyLimitedError {
	for _, key := range blocked {
		if limit := p.providerLimits[key.Provider]; limit > 0 && p.activeByProvider[key.Provider] >= limit {
			return &concurrencyLimitedError{
				modelID: modelID,
				detail:  "供应商 " + key.Provider + " 已达上限 " + strconv.Itoa(limit),
			}
		}
	}
	return &concurrencyLimitedError{modelID: modelID, detail: "候选 Key 均已达各自上限"}
}

// acquireLocked 记一次在途请求：key、账号、供应商三层计数一起加。
//
// 三层必须同增同减：round_robin 的负载均衡看第一层，并发上限看后两层，而释放路径
// 只有一条（ReleaseKey）。漏掉任何一层，上限都会慢慢变成摆设。
func (p *KeyPool) acquireLocked(modelID, keyName string) {
	p.activeRequests[[2]string{modelID, keyName}]++
	if credential := p.credentials[[2]string{modelID, keyName}]; credential != "" {
		p.activeByKey[credential]++
	}
	if provider := p.providersOf[[2]string{modelID, keyName}]; provider != "" {
		p.activeByProvider[provider]++
	}
}

// releaseLocked 归还一次在途请求的账号与供应商计数；归零时删除条目。
//
// 删除而非留 0，与 activeRequests 同一套做法：这两张表都不该随访问过的 key 增长。
func (p *KeyPool) releaseLocked(modelID, keyName string) {
	tuple := [2]string{modelID, keyName}
	if credential := p.credentials[tuple]; credential != "" {
		if p.activeByKey[credential] <= 1 {
			delete(p.activeByKey, credential)
		} else {
			p.activeByKey[credential]--
		}
	}
	if provider := p.providersOf[tuple]; provider != "" {
		if p.activeByProvider[provider] <= 1 {
			delete(p.activeByProvider, provider)
		} else {
			p.activeByProvider[provider]--
		}
	}
}

// routingModeLocked 是无锁版本。
func (p *KeyPool) routingModeLocked(modelID string) string {
	mode, found := p.routingModes[modelID]
	if !found {
		return "round_robin"
	}
	return mode
}

// AcquireKey 记一个在途请求，并在并发已满时拒绝。
//
// 与 NextKey 的分工：NextKey **负责挑**（顺带计数），本方法只回答「点名的这把 key
// 还能不能再塞一个」。返回值从「无」变成 error，是因为点名路径（`model[key]` 语法）
// 同样受并发上限约束——绕过它等于上限在显式指定 key 时形同不存在。
func (p *KeyPool) AcquireKey(modelID, keyName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	modelID = p.resolveModelID(modelID)
	for _, key := range p.keys[modelID] {
		if key.Name != keyName {
			continue
		}
		if !p.hasCapacityLocked(modelID, key) {
			return p.concurrencyLimitedErrorLocked(modelID, []config.KeyConfig{key})
		}
		break
	}
	// 配置里查不到这个 key 时照记不误：调用方（selectKey）总是先用 KeyByName 校验过，
	// 走到这里说明配置在两者之间被热重载过，此时宁可多记一笔也绝不能漏记。
	p.acquireLocked(modelID, keyName)
	return nil
}

// ReleaseKey 减少某 key 的在途计数；归零时删除条目。
//
// 删除而非留 0 是为了让计数表不随访问过的 key 无限增长（与 Python 的 pop 语义一致），
// 账号层与供应商层的计数同理，见 releaseLocked。
func (p *KeyPool) ReleaseKey(modelID, keyName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	modelID = p.resolveModelID(modelID)
	activeKey := [2]string{modelID, keyName}
	if count := p.activeRequests[activeKey]; count <= 1 {
		delete(p.activeRequests, activeKey)
	} else {
		p.activeRequests[activeKey] = count - 1
	}
	p.releaseLocked(modelID, keyName)
}

// ActiveCount 返回某 key 的当前在途请求数（供测试与观测）。
func (p *KeyPool) ActiveCount(modelID, keyName string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeRequests[[2]string{p.resolveModelID(modelID), keyName}]
}

// MarkSuccess 记录成功。
//
// 成功同时清掉该账号的**欠费状态**：一次成功的上游调用就是「这个账号现在有钱」唯一
// 可信的证据，比等退避窗口到期更准也更快。这是「在可用时自动恢复」的落点，也是为什么
// 欠费状态不必单独做后台探测——试探用的就是真实流量。
func (p *KeyPool) MarkSuccess(modelID, keyName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resolved := p.resolveModelID(modelID)
	p.health.MarkSuccess(resolved, keyName)
	p.health.ClearBalanceExhausted(p.credentials[[2]string{resolved, keyName}])
}

// MarkFailure 记录失败。
func (p *KeyPool) MarkFailure(modelID, keyName string, statusCode *int, retryAfter *float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health.MarkFailure(
		p.resolveModelID(modelID), keyName,
		statusCode, retryAfter, p.failureThreshold, p.cooldownSeconds,
	)
}

// IsCoolingDown 报告某 key 是否在冷却中。
func (p *KeyPool) IsCoolingDown(modelID, keyName string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.health.IsCoolingDown(p.resolveModelID(modelID), keyName)
}

// MarkBalanceExhausted 记一次「余额不足」，返回本次限流窗口（秒）与连续命中次数。
//
// 记账落在**账号**上（见 credentialID），因此在 A 模型上发现的欠费会同时把该账号在
// B 模型上挡掉——欠费说的就是账号，不是某一条路由。
func (p *KeyPool) MarkBalanceExhausted(modelID, keyName, reason string) (float64, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resolved := p.resolveModelID(modelID)
	return p.health.MarkBalanceExhausted(
		p.credentials[[2]string{resolved, keyName}], reason, p.balanceCooldownSeconds)
}

// IsBalanceExhausted 报告某 key 所属账号是否仍在欠费限流窗口内。
//
// 选路用的是这个判据（窗口内不放行）。要看「这个账号有没有欠费记录」得用 BalanceState：
// 窗口一过它就为假，而恢复日志恰恰要记在窗口刚过的那次成功上。
func (p *KeyPool) IsBalanceExhausted(modelID, keyName string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	resolved := p.resolveModelID(modelID)
	return p.health.IsBalanceExhausted(p.credentials[[2]string{resolved, keyName}])
}

// BalanceState 返回某 key 所属账号的欠费记录，与窗口是否到期无关。
//
// 与 IsBalanceExhausted 的分工是刻意的：
//   - 选路只在窗口内排除（IsBalanceExhausted）；
//   - 日志与诊断要看**有没有这条记录**，否则窗口一过就再也看不出这个账号曾经欠费，
//     「账号余额恢复」这条日志也就永远不会出现在最主要的恢复路径（半开试成功）上。
func (p *KeyPool) BalanceState(modelID, keyName string) (BalanceState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resolved := p.resolveModelID(modelID)
	return p.health.BalanceExhausted(p.credentials[[2]string{resolved, keyName}])
}

// BalanceLimitedKeys 返回模型下的候选 key 总数与其中因欠费被限流的数量。
//
// 只给选路失败时的诊断用（回答「为什么没有可用 key」），不参与任何决策：排除逻辑
// 在 NextKey 里，两处各算一份迟早会漂移。访问密钥的作用域与选路一致（用同一个
// keysForModelLocked），否则诊断会把作用域外的 key 也算进去。
func (p *KeyPool) BalanceLimitedKeys(modelID string, accessKey *config.AccessKeyConfig) (limited, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resolved := p.resolveModelID(modelID)
	keys := p.keysForModelLocked(resolved, accessKey)
	for _, key := range keys {
		if p.health.IsBalanceExhausted(p.credentials[[2]string{resolved, key.Name}]) {
			limited++
		}
	}
	return limited, len(keys)
}

// balanceExhaustedLocked 报告该 key 所属账号是否被欠费限流。调用方须持锁，且 modelID
// 必须已经过 resolveModelID 归一（credentialID 的键用的是配置里的模型 ID）。
func (p *KeyPool) balanceExhaustedLocked(modelID string, key config.KeyConfig) bool {
	return p.health.IsBalanceExhausted(p.credentials[[2]string{modelID, key.Name}])
}

// credentialID 返回一个 key 对应的**上游账号**标识。
//
// 用凭据指纹而不是 (模型, key 名)：key 名只在单个模型内唯一（config 的去重闭包按模型
// 作用域），「同名不同账号」与「同账号不同名」两种情况都存在，而欠费说的是账号。指纹
// 与 /api 的凭据指纹同一口径（sha256 前 12 位），日志里对得上人。
//
// 没有 api_key 的 key（本地网关、免鉴权上游）退回「供应商 + 基址」：这类部署里账号就是
// 那个端点；退回 key 名则会让同一端点上的一堆 key 各自记账，等于失去了账号级语义。
// 两者都空时返回空串，调用方（健康状态表）会拒绝记账。
func credentialID(key config.KeyConfig) string {
	if key.APIKey != "" {
		return formatting.KeyFingerprint(key.APIKey)
	}
	if key.Provider == "" && key.BaseURL == "" {
		return ""
	}
	return key.Provider + "@" + key.BaseURL
}

// EndpointCapabilityStates 返回所有 URL 的探测状态（供管理 API）。
func (p *KeyPool) EndpointCapabilityStates() *canonical.Value {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.capabilities.Payloads()
}

// SupportsNativeEndpoint 返回 nil=未测试、true/false=已测试。
func (p *KeyPool) SupportsNativeEndpoint(baseURL, routePath string) *bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.capabilities.Get(baseURL, routePath)
}

// UpdateNativeEndpoint 记录探测结果并尝试持久化。
//
// 持久化失败不影响内存状态：能力缓存是缓存而非数据源，写不进去不该让请求失败。
// 参照实现同样吞掉写入错误（endpoint_capability_store.py:42）。
func (p *KeyPool) UpdateNativeEndpoint(baseURL string, supported bool, routePath, reason string) error {
	p.mu.Lock()
	p.capabilities.Update(baseURL, supported, routePath, reason)
	persisted := p.capabilities.Persisted()
	store := p.capabilityStore
	p.mu.Unlock()

	if store == nil {
		return nil
	}
	return store.Save(persisted)
}

// routeKindByFamily 把端点族映射成 unified_model 的计划键。
//
// 两者名字**不**一一对应，因此需要这张表：
//   - 族的模式名是 `images`（upstream_routes 的键），而计划键是 `image`（历史形状，
//     不能改：已有配置里写的是 unified_model.image）；
//   - 语音转写与语音翻译共用一条计划：它们用的是同一类模型（whisper 一族），
//     分成两条只会让用户配两遍、忘一遍。
//
// 不在表里的族（对话三方言）与未知路径都落到 default。
var routeKindByFamily = map[endpoint.Family]string{
	endpoint.FamilyImages:         "image",
	endpoint.FamilyEmbeddings:     "embeddings",
	endpoint.FamilySpeech:         "speech",
	endpoint.FamilyTranscriptions: "transcriptions",
	endpoint.FamilyTranslations:   "transcriptions",
	endpoint.FamilyVideo:          "video",
	endpoint.FamilyRerank:         "rerank",
}

// RequestRouteKind 把代理路径归类为 unified 路由类型。
//
// 对齐 proxy_support.py:252——AMKR 只此一份路径分类，KeyPool 与 proxy_handler
// 共用它。分类表本身在 internal/endpoint（proxysupport 也从那里派生上游路径），
// 这里只负责把它翻译成 unified_model 的计划键。
func RequestRouteKind(path string) string {
	if kind, ok := routeKindByFamily[endpoint.FamilyOf(path)]; ok {
		return kind
	}
	return "default"
}

// isInAvailable 报告 available 中是否存在该名字的 key。
//
// available 元素来自 keysForModelLocked，是 KeyConfig 值副本，因此按名字比较
// （不能比较结构体：KeyConfig 含 map 字段，不可比较）。
func isInAvailable(available []config.KeyConfig, name string) bool {
	for _, key := range available {
		if key.Name == name {
			return true
		}
	}
	return false
}

// nullable 把空字符串表示为 JSON null。
func nullable(value string) *canonical.Value {
	if value == "" {
		return canonical.NewNull()
	}
	return canonical.NewString(value)
}

// sortedMapKeys 返回 map 的排序键。
func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// sortedSet 返回集合的排序切片。
func sortedSet(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
