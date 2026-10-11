// Package keypool 实现可用 key 的选择、冷却与原生端点能力缓存。
package keypool

import "math"

// MaxCooldownSeconds 是冷却时长上限（对齐 key_health.py:9）。
const MaxCooldownSeconds = 300.0

// MaxBalanceCooldownSeconds 是「余额不足」退避窗口的上限（30 分钟）。
//
// 上限不能像普通冷却一样压到 300 秒：欠费不会自己好，每 5 分钟试探一次等于白打上游；
// 也不能放得太长：充值是人手动做的事，窗口太长就变成「充完了还要等半天」。30 分钟是
// 两头的折中——充值后最迟 30 分钟内自动回到路由，而一个长期欠费的账号一天最多被试探
// 48 次（欠费请求不计费，这只是噪音量级）。
const MaxBalanceCooldownSeconds = 1800.0

// KeyHealth 是单个 (模型, key) 的健康状态。
type KeyHealth struct {
	Failures      int
	CooldownUntil float64
}

// BalanceState 是某个上游**账号**（凭据）的欠费状态。
//
// 与 KeyHealth 分开存而不是并进它，原因有两条：
//   - 键不是一回事。KeyHealth 的键是 (模型, key)，而「余额不足」是账号级的事实：
//     同一个账号服务三个模型时，它在这三个模型上都没钱。按 (模型, key) 记会让欠费
//     的账号在另一个模型上继续被选中，正是这次要修的那个洞。
//   - 生命周期不是一回事。普通失败每次都要累计次数并算冷却，欠费只需要「窗口 + 连续
//     命中次数」两项，且**只能**被一次成功请求清掉（见 KeyPool.MarkSuccess）。
type BalanceState struct {
	// Until 是限流窗口的截止时刻。到期后账号重新进入候选，用一次真实请求试探是否
	// 已充值（半开）；试探成功即清空状态，再次被判欠费则窗口翻倍。
	Until float64
	// Attempts 是连续被判欠费的次数，退避窗口按它翻倍。
	Attempts int
	// Reason 是上游原话（调用方已截断），仅供诊断与日志。
	Reason string
	// ObservedAt 是最近一次命中欠费的时刻。
	ObservedAt float64
}

// KeyHealthStore 记录失败计数、冷却截止时间与账号欠费状态。
//
// 参照实现用注入的 clock 以便测试；这里保持同样做法，因为冷却相关的行为全部
// 依赖「当前时间」，不注入就无法确定性断言。
type KeyHealthStore struct {
	clock  func() float64
	states map[[2]string]*KeyHealth
	// balance 的键是**账号标识**（同一把上游凭据在不同模型下是同一个键），见
	// KeyPool 的 credentialID：用 (模型, key) 记载会让欠费账号换个模型就复活。
	balance map[string]*BalanceState
}

// NewKeyHealthStore 构造健康状态表。clock 为 nil 时使用真实时钟。
func NewKeyHealthStore(clock func() float64) *KeyHealthStore {
	if clock == nil {
		clock = RealClock
	}
	return &KeyHealthStore{
		clock:   clock,
		states:  map[[2]string]*KeyHealth{},
		balance: map[string]*BalanceState{},
	}
}

// IsCoolingDown 报告该 key 是否仍在冷却中。
//
// 注意用「取值」而非「取或建」：查询不该产生状态条目（Python 侧用 dict.get）。
func (s *KeyHealthStore) IsCoolingDown(modelID, keyName string) bool {
	state, found := s.states[[2]string{modelID, keyName}]
	return found && state.CooldownUntil > s.clock()
}

// MarkSuccess 清除健康状态。
//
// 参照实现的判断是「状态存在且失败计数与冷却时间都为零时直接返回」，因此一个
// 全零状态会**留在表里**。行为上等价（查询仍返回不冷却），这里保持同样的删除
// 条件以免维护期出现无意义的状态差异。
func (s *KeyHealthStore) MarkSuccess(modelID, keyName string) {
	key := [2]string{modelID, keyName}
	state, found := s.states[key]
	if !found || (state.Failures == 0 && state.CooldownUntil == 0) {
		return
	}
	delete(s.states, key)
}

// MarkFailure 记录一次失败，必要时进入冷却。
//
// 规则（对齐 key_health.py:36）：
//   - 429 立即冷却，不看阈值；
//   - 其他状态码要累计到 failure_threshold 才开始冷却；
//   - 有 retry_after 时直接用它，否则用 cooldown_seconds × 失败次数；
//   - 两者都截断到 MaxCooldownSeconds，且取下限 0；
//   - 冷却截止时间只延后不提前（取 max）。
func (s *KeyHealthStore) MarkFailure(
	modelID, keyName string,
	statusCode *int,
	retryAfter *float64,
	failureThreshold int,
	cooldownSeconds float64,
) {
	key := [2]string{modelID, keyName}
	state, found := s.states[key]
	if !found {
		state = &KeyHealth{}
		s.states[key] = state
	}
	state.Failures++
	// statusCode 为 nil 时「不是 429」，因此走阈值判断（与 Python 的 None != 429 一致）。
	if (statusCode == nil || *statusCode != 429) && state.Failures < failureThreshold {
		return
	}
	var cooldown float64
	if retryAfter != nil {
		cooldown = math.Min(MaxCooldownSeconds, math.Max(0, *retryAfter))
	} else {
		cooldown = math.Min(
			MaxCooldownSeconds,
			math.Max(0, cooldownSeconds)*math.Max(1, float64(state.Failures)),
		)
	}
	if cooldown <= 0 {
		return
	}
	state.CooldownUntil = math.Max(state.CooldownUntil, s.clock()+cooldown)
}

// MarkBalanceExhausted 记录一次「余额不足」，返回本次限流窗口（秒）与连续命中次数。
//
// 窗口按命中次数翻倍（base、2×base、4×base…），上限 MaxBalanceCooldownSeconds：
// 第一次命中的窗口必须够短，否则「刚充完值」的账号会被自己上一次的判定挡住；之后
// 每次试探都失败就说明还没充，再缩短窗口只会重复白打。
func (s *KeyHealthStore) MarkBalanceExhausted(credential, reason string, baseCooldown float64) (float64, int) {
	if credential == "" {
		// 没有账号标识（上游 key 为空且供应商/基址也空）时无从记账：宁可不动，
		// 也不能把「所有无标识的 key」当成同一个账号一起限流。
		return 0, 0
	}
	if baseCooldown <= 0 {
		// 关闭档（key_balance_cooldown_seconds = 0）：不记账、不留状态，与
		// key_cooldown_seconds 取 0 时不做冷却同源。
		return 0, 0
	}
	state, found := s.balance[credential]
	if !found {
		state = &BalanceState{}
		s.balance[credential] = state
	}
	state.Attempts++
	state.Reason = reason
	state.ObservedAt = s.clock()
	window := balanceCooldown(baseCooldown, state.Attempts)
	state.Until = s.clock() + window
	return window, state.Attempts
}

// balanceCooldown 计算第 attempts 次命中欠费的限流窗口。
//
// baseCooldown <= 0 表示**关闭**这项机制（与 key_cooldown_seconds 取 0 表示不冷却
// 同源）：窗口恒为 0，IsBalanceExhausted 因此恒假，选路不做任何排除。
func balanceCooldown(baseCooldown float64, attempts int) float64 {
	if baseCooldown <= 0 || attempts <= 0 {
		return 0
	}
	window := math.Min(baseCooldown, MaxBalanceCooldownSeconds)
	for index := 1; index < attempts; index++ {
		if window >= MaxBalanceCooldownSeconds {
			break
		}
		window = math.Min(window*2, MaxBalanceCooldownSeconds)
	}
	return window
}

// IsBalanceExhausted 报告该账号是否仍在欠费限流窗口内。
//
// 与 IsCoolingDown 一样用「取值」而非「取或建」：查询不该产生状态条目。
func (s *KeyHealthStore) IsBalanceExhausted(credential string) bool {
	if credential == "" {
		return false
	}
	state, found := s.balance[credential]
	return found && state.Until > s.clock()
}

// BalanceExhausted 返回账号的欠费状态（含窗口已过、但还没被成功请求清掉的那种）。
//
// 与 IsBalanceExhausted 分开：前者回答「现在要不要绕过它」，后者回答「它身上有没有
// 欠费记录」——日志与诊断要看的是后者，否则窗口一过就再也看不出这个账号曾经欠费。
func (s *KeyHealthStore) BalanceExhausted(credential string) (BalanceState, bool) {
	if credential == "" {
		return BalanceState{}, false
	}
	state, found := s.balance[credential]
	if !found {
		return BalanceState{}, false
	}
	return *state, true
}

// ClearBalanceExhausted 清空账号的欠费状态。
//
// 触发点只有一个：该账号上出现了一次**成功**请求。那是「它现在有钱」唯一可信的证据，
// 比任何时间窗口都准，因此不必等窗口到期。
func (s *KeyHealthStore) ClearBalanceExhausted(credential string) {
	if credential == "" {
		return
	}
	delete(s.balance, credential)
}

// Reset 清空全部状态（配置热重载时使用）。
func (s *KeyHealthStore) Reset() {
	s.states = map[[2]string]*KeyHealth{}
	s.balance = map[string]*BalanceState{}
}

// RealClock 返回秒级 Unix 时间（对齐 Python 的 time()，含小数）。
func RealClock() float64 {
	return float64(nowUnixNano()) / 1e9
}
