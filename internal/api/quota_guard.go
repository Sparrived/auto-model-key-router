package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
	"github.com/Sparrived/auto-model-key-router/internal/quota"
)

// 额度冻结评估器：账号资源读数 → 选路硬约束（设计稿第 4 节）。
//
// 它同时承担三件事，三处共用同一套判定：
//
//  1. **后台巡检**：按当前运行时配置的 quota_freeze_interval_seconds 周期采集一次；
//  2. **页面即时评估**：账号资源页每次刷新都会把刚采集到的读数交进来（打开页面 =
//     立刻检测），不额外扇出；
//  3. **L3 被动信号**：代理请求路径拿到上游响应头后只投递一个信号，由后台循环合并
//     处理（请求路径绝不写盘、不评估、不长时间持锁）。
//
// 判定的唯一出口是 applyReportLocked：三种触发源产出的都是同一份「账号资源读数」或
// 等价的窗口信号，规则只在那一处实现，避免「巡检冻了、页面又解了」这种自相矛盾。

const (
	// quotaFreezeResetGrace 是冻结到期的宽限：窗口 reset_at 之后 60 秒（设计稿 2）。
	//
	// 留这 60 秒是因为上游的重置时刻与本地时钟总有几十秒抖动：刚好在 reset 点把 Key
	// 放回来，第一个请求很可能仍然撞上「还没重置」。宁可晚一分钟恢复，也不要用一个
	// 必然失败的请求去试探。
	quotaFreezeResetGrace = 60 * time.Second
	// quotaFreezeFallbackTTL 是拿不到（或拿到已经过去的）reset_at 时的冻结时长。
	//
	// 与 quota.fallbackFreezeTTL 同口径：额度端点长期不可达时，一个错判不能让 Key
	// 永久消失。10 分钟后自动解除，下一轮观测若仍耗尽会重新冻结。
	quotaFreezeFallbackTTL = 10 * time.Minute
	// quotaSignalBuffer 是 L3 信号通道容量。
	//
	// 够大是为了扛住「一个模型十几个 Key 同时撞额度」这样的瞬时脉冲；再大也只是把
	// 内存换成延迟。满了直接丢——下一轮巡检会把状态重新算对，信号本身不是事实来源。
	quotaSignalBuffer = 64
	// quotaSignalDebounce 是同一把 Key 的 L3 信号去抖窗口。
	//
	// 冻结中的 Key 不再被选中，正常不会持续收到它的信号；去抖真正防的是「刚解冻、
	// 上游仍回旧读数」这类抖动，以及同一时刻多条连接各投一次信号造成重复落盘。
	quotaSignalDebounce = 30 * time.Second
	// quotaSignalSeenLimit 是去抖表的软上限，超过就顺手清理过期项。
	quotaSignalSeenLimit = 256
)

// QuotaGuardOptions 是评估器的装配输入。字段全部可空，便于测试只注入需要的部分。
type QuotaGuardOptions struct {
	// Server 提供采集能力（复用 collectAccountsReport，不另写抓取逻辑）。
	Server *Server
	// Store 是冻结表；nil 表示不启用（评估会退化成空操作）。
	Store *quota.Store
	// Config 返回**当前生效**的运行时配置；热重载后这个回调自然给出新值。
	Config func() *config.RouterConfig
	// Logger 用于记录每条冻结变化；nil 时退回 slog.Default()。
	Logger *slog.Logger
	// Now 是时钟注入点，便于确定性测试；nil 时用 time.Now。
	Now func() time.Time
}

// QuotaGuardStatus 是评估器的对外状态（设计稿 6.1 的 guard 对象）。
type QuotaGuardStatus struct {
	Enabled         bool    `json:"enabled"`
	IntervalSeconds float64 `json:"interval_seconds"`
	LastEvaluatedAt string  `json:"last_evaluated_at"`
	LastError       string  `json:"last_error"`
}

// QuotaEvaluation 描述一次评估的结果。
//
// 它主要用于测试与日志：Handler 不读取它（响应里的数字全部来自冻结表本身），
// 因此这里不加 JSON tag，也不承诺字段集合稳定。
type QuotaEvaluation struct {
	// EvaluatedAt 是这次评估的起点时刻（guard 的时钟）。
	EvaluatedAt time.Time
	// Frozen 是评估完成后仍生效的冻结条数。
	Frozen int
	// Changed 是本次新增/刷新/解除/清理的条数（0 表示与上一轮完全一致）。
	Changed int
	// Skipped 表示这次没有真正评估：机制关闭、配置拿不到，或采集失败。
	Skipped bool
	// Error 是采集失败的原因（Skipped 时为唯一可能非空的路径）。
	Error string
}

// QuotaInstanceLink 是「供应商 → CPA 实例」的关联读数，供 L3 判断单/多账号实例。
type QuotaInstanceLink struct {
	// InstanceID 是 base_url 指向的 CPA 实例；空串表示未关联。
	InstanceID string
	// EnabledAccounts 是该实例的启用账号数（L3 只看这一个数）。
	EnabledAccounts int
}

// quotaSignal 是 L3 投递的一条信号：只带「哪把 Key + 哪些硬性耗尽窗口」。
type quotaSignal struct {
	ProviderID string
	KeyName    string
	Windows    []quota.Window
}

// QuotaGuard 是后台巡检 + 页面即时评估 + L3 信号落地的共用实现。
//
// 并发模型（四把锁，各管一件事，锁序固定，不在持锁时做 IO）：
//
//   - collectMu：**后台采集**的单飞锁。采集可能长达 cpaAccountsBudget（60 秒），
//     所以它在 evalMu 之外：页面即时评估（ApplyReport）只等「应用判定」这一小段，
//     不会陪一次完整的网络扇出一起等；
//   - evalMu：判定应用互斥。三条触发源（巡检、页面、L3）写冻结表时同一时刻只有
//     一个在应用，避免同一份内存状态被两个 goroutine 交错改写。采集与判定拆开后，
//     页面读数可能在巡检采集中途先落盘（两份读数来自同一批上游、相差几秒），最终
//     状态以「后应用的那份」为准，下一轮巡检会收敛；这是用「最多晚一轮」换「页面
//     永不陪 60 秒扇出等待」的有意取舍；
//   - mu：状态与关联表的读写锁，保护 lastEvaluatedAt/lastError/links/signalSeen，
//     只做内存赋值；
//   - store 自己的锁：冻结表的持久化，落盘在它内部完成。
//
// 请求路径（ObserveUpstreamQuota）**只碰信号通道**：不取 evalMu、不碰文件、不解析
// 之外的任何东西，因此不会被巡检的落盘或网络等待拖住。
type QuotaGuard struct {
	server *Server
	store  *quota.Store
	config func() *config.RouterConfig
	logger *slog.Logger
	now    func() time.Time

	// trigger 是「尽快评估一次」的电平信号（容量 1，非阻塞发送）。
	trigger chan struct{}
	// signals 是 L3 的投递队列（容量见 quotaSignalBuffer，满了丢弃）。
	signals chan quotaSignal

	collectMu sync.Mutex
	evalMu    sync.Mutex
	lastEval  QuotaEvaluation

	mu              sync.Mutex
	lastEvaluatedAt time.Time
	lastError       string
	links           map[string]QuotaInstanceLink
	linksReady      bool
	signalSeen      map[string]time.Time
}

// NewQuotaGuard 构造评估器。字段缺省按注释兜底；Server/Store 为 nil 时评估退化成
// 空操作（嵌入场景与单元测试都依赖这一点）。
func NewQuotaGuard(opts QuotaGuardOptions) *QuotaGuard {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &QuotaGuard{
		server:     opts.Server,
		store:      opts.Store,
		config:     opts.Config,
		logger:     logger,
		now:        now,
		trigger:    make(chan struct{}, 1),
		signals:    make(chan quotaSignal, quotaSignalBuffer),
		links:      map[string]QuotaInstanceLink{},
		signalSeen: map[string]time.Time{},
	}
}

// nowTime 返回注入的时钟读数（同包内让 Handler 与本评估器用同一个时钟）。
func (g *QuotaGuard) nowTime() time.Time {
	if g == nil || g.now == nil {
		return time.Now()
	}
	return g.now()
}

// intervalSeconds 返回当前生效的巡检间隔（秒）；0 表示整套机制关闭。
func (g *QuotaGuard) intervalSeconds() float64 {
	if g == nil || g.config == nil {
		return 0
	}
	cfg := g.config()
	if cfg == nil {
		return 0
	}
	return cfg.QuotaFreezeIntervalSeconds
}

// interval 把巡检间隔换成 Duration；<=0 返回 0（调用方据此不装定时器）。
func (g *QuotaGuard) interval() time.Duration {
	seconds := g.intervalSeconds()
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// enabled 报告整套机制是否开启（配置里的 interval > 0）。
func (g *QuotaGuard) enabled() bool { return g.intervalSeconds() > 0 }

// Run 跑后台循环：启动立即评估一次，此后按当前间隔巡检、响应 Trigger 与 L3 信号。
//
// 间隔每轮重新读配置，因此热重载改了 quota_freeze_interval_seconds 无需重启，下一轮
// 即生效；改成 0 后仍然处理 Trigger（重载本身会发一个）与信号（信号会被直接忽略），
// 但不装定时器、不评估——「0 = 关闭」不能因为循环还在转就漏出去。
//
// ctx 取消立即返回，并等待在途的判定应用结束（页面评估可能正持有 evalMu）：不这样做
// 的话，Close 返回之后仍可能有一次判定往冻结表里写记录，测试进程退出时会看到未完成的
// 文件写。Run 自己的采集与判定在同一个 goroutine 里串行，ctx 取消会让在途采集立刻返回。
func (g *QuotaGuard) Run(ctx context.Context) {
	if g == nil {
		return
	}
	// 启动立即评估一次：不阻塞调用方（装配层本来就是 go Run）。
	if g.enabled() {
		g.Evaluate(ctx)
	}
	for {
		wait := g.interval()
		var timer *time.Timer
		var deadline <-chan time.Time
		if wait > 0 {
			timer = time.NewTimer(wait)
			deadline = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			// 等在途判定应用结束：Lock/Unlock 是「排队到锁空出来」的最短写法，
			// 也顺带保证不会与之后的 Close 抢冻结表。
			g.evalMu.Lock()
			g.evalMu.Unlock()
			return
		case <-g.trigger:
			if timer != nil {
				timer.Stop()
			}
			if g.enabled() {
				g.Evaluate(ctx)
			}
		case signal := <-g.signals:
			if timer != nil {
				timer.Stop()
			}
			g.handleSignal(signal)
		case <-deadline:
			// timer 已触发，无需 Stop。
			if g.enabled() {
				g.Evaluate(ctx)
			}
		}
	}
}

// Trigger 请求尽快评估一次（热重载、L3 多账号实例等）。不阻塞、可重复调用。
func (g *QuotaGuard) Trigger() {
	if g == nil {
		return
	}
	select {
	case g.trigger <- struct{}{}:
	default:
		// 已经有一次待处理的触发：合并掉，不必排队。
	}
}

// Evaluate 采集一次账号资源并应用判定（后台巡检的主入口）。
//
// 拆成「采集」与「应用」两段：采集在 collectMu 之下、**不持 evalMu**，因为一次扇出
// 的预算是 60 秒；应用在 evalMu 之下，只做内存判定与一次原子落盘。这样页面刷新
// （ApplyReport）即使撞上巡检，也只等那一小段，而不是陪一次完整扇出。采集本身仍是
// 单飞：已有采集在跑时不重复扇出，直接返回上一份结果。
//
// 配置在采集前后各读一次：采集中途热重载把 `quota_freeze_interval_seconds` 改成 0
// 时，应用前会再判一次，避免「刚关掉机制又落一条冻结」。
func (g *QuotaGuard) Evaluate(ctx context.Context) QuotaEvaluation {
	if g == nil {
		return QuotaEvaluation{Skipped: true}
	}
	if !g.collectMu.TryLock() {
		return g.lastEvaluation()
	}
	defer g.collectMu.Unlock()

	cfg := g.currentConfig()
	if cfg == nil || cfg.QuotaFreezeIntervalSeconds <= 0 {
		return QuotaEvaluation{Skipped: true}
	}
	report, err := g.collect(ctx, cfg)
	if err != nil {
		if ctx.Err() != nil {
			// 关停中：ctx 取消导致采集中断，不是故障，不写 last_error、不打日志。
			return QuotaEvaluation{EvaluatedAt: g.now(), Skipped: true}
		}
		g.recordFailure(err)
		return QuotaEvaluation{EvaluatedAt: g.now(), Skipped: true, Error: err.Error()}
	}

	g.evalMu.Lock()
	defer g.evalMu.Unlock()
	cfg = g.currentConfig()
	if cfg == nil || cfg.QuotaFreezeIntervalSeconds <= 0 {
		return QuotaEvaluation{Skipped: true}
	}
	return g.applyReportLocked(cfg, report)
}

// ApplyReport 用一份**已有**读数评估（页面刷新走这里，复用刚扇出的结果）。
//
// 与 Evaluate 共用同一把评估锁：页面路径不能与后台巡检同时改冻结表。这里用阻塞
// Lock 而不是 TryLock——采集已经完成，再等一次短小的判定没有代价，而丢弃这份新鲜
// 读数（等下一轮巡检）反而会让页面显示旧状态。
func (g *QuotaGuard) ApplyReport(report cpaAccountsReport) QuotaEvaluation {
	if g == nil {
		return QuotaEvaluation{Skipped: true}
	}
	g.evalMu.Lock()
	defer g.evalMu.Unlock()

	cfg := g.currentConfig()
	if cfg == nil || cfg.QuotaFreezeIntervalSeconds <= 0 {
		return QuotaEvaluation{Skipped: true}
	}
	return g.applyReportLocked(cfg, report)
}

// currentConfig 返回当前运行时配置；任何一段缺失都返回 nil（调用方按「关闭」处理）。
func (g *QuotaGuard) currentConfig() *config.RouterConfig {
	if g == nil || g.config == nil {
		return nil
	}
	return g.config()
}

// collect 复用账号资源页的同一次扇出：CPA 实例 + 订阅，不分两次取。
func (g *QuotaGuard) collect(ctx context.Context, cfg *config.RouterConfig) (cpaAccountsReport, error) {
	if g.server == nil {
		return cpaAccountsReport{}, errors.New("额度冻结评估器未装配 api.Server")
	}
	data, err := g.server.managementConfigData()
	if err != nil {
		return cpaAccountsReport{}, err
	}
	instances, err := configops.ParseCPAInstances(data)
	if err != nil {
		return cpaAccountsReport{}, err
	}
	return g.server.collectAccountsReport(ctx, cfg, instances), nil
}

// applyReportLocked 是全部判定规则的唯一实现；调用方必须持有 evalMu。
//
// 判定顺序有意义：先订阅条目、再实例级联动、最后清理失效记录。订阅规则只解冻自己
// 那一层的冻结（subscription/upstream），实例级冻结交给实例规则——同一个 Key 可能
// 同时被两条规则盯着，谁都不能顺手把对方刚下的结论推翻。
func (g *QuotaGuard) applyReportLocked(cfg *config.RouterConfig, report cpaAccountsReport) QuotaEvaluation {
	now := g.now()
	evaluation := QuotaEvaluation{EvaluatedAt: now}
	if g.store == nil {
		evaluation.Skipped = true
		return evaluation
	}

	providers := make(map[string]config.ProviderConfig, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		providers[provider.ID] = provider
	}
	links, providersByInstance := instanceLinkIndex(cfg, report.Instances)

	// —— 1. 订阅类账号资源：条目级判定 ——

	for _, entry := range report.Subscriptions {
		if !entry.OK {
			// 无意见：读不到不等于有余额，不动现状。
			continue
		}
		hard := hardQuotaWindows(entry.Windows)
		if len(hard) > 0 {
			window := latestResetWindow(hard)
			evaluation.Changed += g.upsertFreeze(quota.Freeze{
				ProviderID:  entry.ProviderID,
				KeyName:     entry.KeyName,
				Vendor:      entry.Vendor,
				Scope:       "subscription",
				Source:      "account_resource",
				Account:     entry.Account,
				WindowKey:   window.Key,
				WindowLabel: window.Label,
				Remaining:   window.Remaining,
				Status:      window.Status,
				ResetAt:     window.ResetAt,
				FrozenAt:    now,
				ExpiresAt:   freezeExpiresAt(now, hard),
				Reason:      windowExhaustedReason(window),
			})
			continue
		}
		// 健康：解冻这把 Key 的订阅/上游信号冻结。实例级冻结不在这里解（见上）。
		evaluation.Changed += g.releaseFreeze(entry.ProviderID, entry.KeyName, "subscription", "upstream")
	}

	// —— 2. CPA 实例级联动：全部启用账号耗尽才冻 ——

	configuredInstances := make(map[string]bool, len(report.Instances))
	for _, instance := range report.Instances {
		configuredInstances[instance.ID] = true
		if !instance.OK {
			// 实例不可达：无意见（不能把「读不到」当成「账号有余额」）。
			continue
		}
		enabled := enabledAccounts(instance.Accounts)
		if len(enabled) == 0 {
			// 实例没有启用账号：没有判定对象，同样无意见。
			continue
		}
		linked := providersByInstance[instance.ID]
		allExhausted, account, window := instanceExhaustion(enabled)
		if allExhausted {
			for _, providerID := range linked {
				for _, key := range providers[providerID].Keys {
					if !key.Enabled {
						continue
					}
					evaluation.Changed += g.upsertFreeze(quota.Freeze{
						ProviderID:  providerID,
						KeyName:     key.Name,
						Vendor:      instanceLabel(instance),
						Scope:       "cpa_instance",
						Source:      "account_resource",
						Account:     account,
						InstanceID:  instance.ID,
						WindowKey:   window.Key,
						WindowLabel: window.Label,
						Remaining:   window.Remaining,
						Status:      window.Status,
						ResetAt:     window.ResetAt,
						FrozenAt:    now,
						ExpiresAt:   freezeExpiresAt(now, []quota.Window{window}),
						Reason:      instanceExhaustedReason(instance, len(enabled), window),
					})
				}
			}
			continue
		}
		// 至少一个启用账号恢复：解冻该实例关联供应商的实例级与上游信号冻结。
		for _, providerID := range linked {
			for _, key := range providers[providerID].Keys {
				evaluation.Changed += g.releaseFreeze(providerID, key.Name, "cpa_instance", "upstream")
			}
		}
	}

	// —— 2b. 关联关系已失效的实例级冻结 ——
	//
	// 实例从配置里删掉、或供应商 base_url 改成指向别处之后，旧冻结不该继续挂在这把
	// Key 上等它自然到期：那时规则已经不可能再判定它，界面还会显示一条没人认领的
	// 冻结。Prune 只看「Key 是否存在」，补不了这一层。
	for _, freeze := range g.store.Snapshot() {
		if freeze.Scope != "cpa_instance" {
			continue
		}
		if !configuredInstances[freeze.InstanceID] {
			evaluation.Changed += g.releaseFreeze(freeze.ProviderID, freeze.KeyName, "cpa_instance")
			continue
		}
		if link, linked := links[freeze.ProviderID]; !linked || link.InstanceID != freeze.InstanceID {
			evaluation.Changed += g.releaseFreeze(freeze.ProviderID, freeze.KeyName, "cpa_instance")
		}
	}

	// —— 3. 关联表、失效清理与状态 ——

	g.setLinks(links)
	removed, pruneErr := g.store.Prune(func(providerID, keyName string) bool {
		provider, ok := providers[providerID]
		if !ok {
			return false
		}
		for _, key := range provider.Keys {
			if key.Name == keyName {
				return key.Enabled
			}
		}
		return false
	})
	if pruneErr != nil {
		g.logger.Warn("额度冻结表清理后落盘失败", "error", pruneErr)
	}
	for _, freeze := range removed {
		g.logger.Info("额度冻结已清理",
			"provider_id", freeze.ProviderID, "key_name", freeze.KeyName,
			"reason", freeze.Reason, "expires_at", formatFreezeTime(freeze.ExpiresAt))
	}
	evaluation.Changed += len(removed)
	evaluation.Frozen = len(g.store.Snapshot())
	g.recordEvaluation(evaluation)
	return evaluation
}

// upsertFreeze 写一条冻结并记日志（只在真的有变化时打日志，避免每轮巡检刷屏）。
func (g *QuotaGuard) upsertFreeze(freeze quota.Freeze) int {
	if g.store == nil {
		return 0
	}
	changed, err := g.store.Upsert(freeze)
	if err != nil {
		g.logger.Warn("额度冻结写入失败",
			"provider_id", freeze.ProviderID, "key_name", freeze.KeyName, "error", err)
		return 0
	}
	if !changed {
		return 0
	}
	g.logger.Info("额度已冻结",
		"provider_id", freeze.ProviderID, "key_name", freeze.KeyName,
		"scope", freeze.Scope, "window", freeze.WindowKey,
		"reason", freeze.Reason, "expires_at", formatFreezeTime(freeze.ExpiresAt))
	return 1
}

// releaseFreeze 解除一条冻结（仅当它当前的 scope 在给定集合里）。
//
// 按 scope 过滤而不是无条件解除：同一个 Key 只保留一条冻结记录，但「谁下的结论」
// 不同（订阅读数 / 实例联动 / 上游信号）。订阅条目健康只证明这把 Key 自己的账号有
// 余额，证明不了实例里别的账号也有——把实例级冻结顺手解掉，下一轮巡检又会冻回来，
// 表现为界面每分钟闪一次。
func (g *QuotaGuard) releaseFreeze(providerID, keyName string, scopes ...string) int {
	if g.store == nil {
		return 0
	}
	freeze, found := g.store.Get(providerID, keyName)
	if !found || !containsString(scopes, freeze.Scope) {
		return 0
	}
	_, released, err := g.store.Release(providerID, keyName, time.Time{})
	if err != nil {
		g.logger.Warn("额度冻结解除后落盘失败",
			"provider_id", providerID, "key_name", keyName, "error", err)
	}
	if !released {
		return 0
	}
	g.logger.Info("额度冻结已解除",
		"provider_id", providerID, "key_name", keyName,
		"scope", freeze.Scope, "reason", "账号资源观测到额度恢复")
	return 1
}

// ObserveUpstreamQuota 是 L3 观察点：代理请求路径上每次拿到上游响应头都调一次。
//
// 这里只做三件廉价的事：有没有额度头（前缀判断）→ 解析出窗口 → 若有硬性耗尽则
// 非阻塞投递一个信号。**不做文件 IO、不发网络请求、不取评估锁**：它跑在请求的关键
// 路径上，任何阻塞都会直接变成用户可见的延迟。真正的判定与落盘在 Run 循环里做。
func (g *QuotaGuard) ObserveUpstreamQuota(providerID, keyName string, headers http.Header) {
	if g == nil || g.store == nil {
		return
	}
	if !quota.HasQuotaHeaders(headers) {
		return
	}
	windows := quota.HeaderWindows(headers)
	hard := make([]quota.Window, 0, len(windows))
	for _, window := range windows {
		if quota.HardExhausted(window) {
			hard = append(hard, window)
		}
	}
	if len(hard) == 0 {
		// 预警状态（allowed_warning）不参与冻结：硬约束宁可晚一次请求，也不能误伤
		// 还有额度的 Key。
		return
	}
	select {
	case g.signals <- quotaSignal{ProviderID: providerID, KeyName: keyName, Windows: hard}:
	default:
		// 缓冲满：丢弃。信号不是事实来源，下一轮巡检会把状态重新算对。
	}
}

// handleSignal 处理一条 L3 信号（只在 Run 循环里调用，单线程，顺序确定）。
func (g *QuotaGuard) handleSignal(signal quotaSignal) {
	if g == nil || g.store == nil || !g.enabled() {
		// interval=0 表示整套机制关闭：L3 信号不落地。
		return
	}
	if signal.ProviderID == "" || signal.KeyName == "" || len(signal.Windows) == 0 {
		return
	}
	// 已经冻住 / 处于手工抑制窗口内：无需重复动作。放在去抖之前，这样解冻后紧跟着的
	// 一条新信号仍然能被立刻处理，不会被「刚才冻过」的去抖表格挡掉。
	if g.store.IsFrozen(signal.ProviderID, signal.KeyName) ||
		g.store.Suppressed(signal.ProviderID, signal.KeyName) {
		return
	}
	if !g.markSignalSeen(signal) {
		return
	}
	link, linked, linksReady := g.instanceLink(signal.ProviderID)
	if !linksReady || (linked && link.EnabledAccounts != 1) {
		// 多账号实例（或关联关系尚未评估出来）：单次响应的限额头只说明「刚才那个
		// 账号」用完了，不代表整把 Key 不可用，交给实例级规则决定。
		g.Trigger()
		return
	}
	window := latestResetWindow(signal.Windows)
	now := g.now()
	g.upsertFreeze(quota.Freeze{
		ProviderID:  signal.ProviderID,
		KeyName:     signal.KeyName,
		Scope:       "upstream",
		Source:      "upstream_header",
		WindowKey:   window.Key,
		WindowLabel: window.Label,
		Remaining:   window.Remaining,
		Status:      window.Status,
		ResetAt:     window.ResetAt,
		FrozenAt:    now,
		ExpiresAt:   freezeExpiresAt(now, signal.Windows),
		Reason:      "上游响应头报告：" + windowExhaustedReason(window),
	})
}

// markSignalSeen 记录信号并报告是否该处理（同一把 Key 在去抖窗口内只处理一次）。
func (g *QuotaGuard) markSignalSeen(signal quotaSignal) bool {
	key := signal.ProviderID + "/" + signal.KeyName
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if seen, ok := g.signalSeen[key]; ok && now.Sub(seen) < quotaSignalDebounce {
		return false
	}
	g.signalSeen[key] = now
	if len(g.signalSeen) > quotaSignalSeenLimit {
		for name, at := range g.signalSeen {
			if now.Sub(at) >= quotaSignalDebounce {
				delete(g.signalSeen, name)
			}
		}
	}
	return true
}

// instanceLink 返回某供应商的实例关联读数（linked 表示是否关联了实例）。
func (g *QuotaGuard) instanceLink(providerID string) (link QuotaInstanceLink, linked bool, ready bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	link, linked = g.links[providerID]
	return link, linked, g.linksReady
}

// setLinks 用本轮评估算出的关联表替换旧表（只做内存赋值，不持锁做别的）。
func (g *QuotaGuard) setLinks(links map[string]QuotaInstanceLink) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.links = links
	g.linksReady = true
}

// InstanceLinks 返回最近一次评估算出的「供应商 → CPA 实例」关联表副本。
//
// 供账号资源响应增强与测试使用；L3 也读它判断单/多账号实例。从未评估过时返回空表
// （调用方用 linksReady 区分「空表」与「还没算过」，见 instanceLink）。
func (g *QuotaGuard) InstanceLinks() map[string]QuotaInstanceLink {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]QuotaInstanceLink, len(g.links))
	for providerID, link := range g.links {
		out[providerID] = link
	}
	return out
}

// Status 返回评估器状态（未装配时是零值：enabled=false、interval=0）。
func (g *QuotaGuard) Status() QuotaGuardStatus {
	if g == nil {
		return QuotaGuardStatus{}
	}
	status := QuotaGuardStatus{
		Enabled:         g.enabled(),
		IntervalSeconds: g.intervalSeconds(),
	}
	g.mu.Lock()
	if !g.lastEvaluatedAt.IsZero() {
		status.LastEvaluatedAt = formatFreezeTime(g.lastEvaluatedAt)
	}
	status.LastError = g.lastError
	g.mu.Unlock()
	return status
}

// lastEvaluation 返回上一份评估结果（TryLock 失败时复用）。
func (g *QuotaGuard) lastEvaluation() QuotaEvaluation {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastEval
}

// recordEvaluation 记下成功评估的时刻、结果并清掉旧错误。
func (g *QuotaGuard) recordEvaluation(evaluation QuotaEvaluation) {
	g.mu.Lock()
	g.lastEvaluatedAt = evaluation.EvaluatedAt
	g.lastError = ""
	g.lastEval = evaluation
	g.mu.Unlock()
}

// recordFailure 记下采集失败：last_error 要能被运维看到，last_evaluated_at 也更新
// （那是「最后一次尝试」而不是「最后一次成功」；成功会清空 last_error）。
func (g *QuotaGuard) recordFailure(err error) {
	now := g.now()
	g.mu.Lock()
	g.lastEvaluatedAt = now
	g.lastError = err.Error()
	g.lastEval = QuotaEvaluation{EvaluatedAt: now, Skipped: true, Error: err.Error()}
	g.mu.Unlock()
	g.logger.Warn("额度冻结巡检采集失败", "error", err)
}

// —— 判定与归一化的纯函数 ——

// quotaWindowsFromCPA 把账号资源页的窗口读法转成额度包的归一窗口。
//
// 两个包的字段名刻意一致（Key/Label/Remaining/ResetAt/Status），差别只在时间类型：
// api 的窗口给前端看的是字符串（可能是上游原话），quota 的判定要 time.Time。
func quotaWindowsFromCPA(windows []cpaQuotaWindow) []quota.Window {
	out := make([]quota.Window, 0, len(windows))
	for _, window := range windows {
		out = append(out, quota.Window{
			Key:       window.Key,
			Label:     firstNonEmpty(window.Label, window.Window, window.Key),
			Remaining: window.Remaining,
			ResetAt:   quota.ParseResetText(window.ResetAt),
			Status:    window.Status,
		})
	}
	return out
}

// hardQuotaWindows 筛出硬性耗尽的窗口（判定本身只有 quota.HardExhausted 一份实现）。
func hardQuotaWindows(windows []cpaQuotaWindow) []quota.Window {
	hard := make([]quota.Window, 0, len(windows))
	for _, window := range quotaWindowsFromCPA(windows) {
		if quota.HardExhausted(window) {
			hard = append(hard, window)
		}
	}
	return hard
}

// latestResetWindow 从一组硬性耗尽窗口里挑出「最晚重置」的那个。
//
// 冻结到期取最晚的 reset + 宽限：同一把 Key 上可能同时有两个窗口耗尽（5h 用完、
// 7d 也用完），按最早的那个解冻会让 Key 带着另一个仍耗尽的窗口回到选路里。
// 重置时间相同时取剩余更低的那个，让记录里的读数更贴近真实原因。
func latestResetWindow(windows []quota.Window) quota.Window {
	if len(windows) == 0 {
		return quota.Window{}
	}
	best := windows[0]
	for _, window := range windows[1:] {
		if window.ResetAt.After(best.ResetAt) {
			best = window
			continue
		}
		if window.ResetAt.Equal(best.ResetAt) && window.Remaining < best.Remaining {
			best = window
		}
	}
	return best
}

// freezeExpiresAt 计算冻结到期：最晚 reset + 60 秒；拿不到（或已过去）时 now + 10 分钟。
func freezeExpiresAt(now time.Time, windows []quota.Window) time.Time {
	latest := latestResetWindow(windows)
	if latest.ResetAt.IsZero() {
		return now.Add(quotaFreezeFallbackTTL)
	}
	expires := latest.ResetAt.Add(quotaFreezeResetGrace)
	if !expires.After(now) {
		// 上游给的 reset 已经过去：读数可能是旧的。按兜底时长短冻一次，下一轮巡检
		// 若仍观测到耗尽会重新冻结——不能拿一个过去的时间点让冻结立刻失效。
		return now.Add(quotaFreezeFallbackTTL)
	}
	return expires
}

// windowExhaustedReason 把「为什么冻」写成一句人话（界面直接显示）。
func windowExhaustedReason(window quota.Window) string {
	label := firstNonEmpty(window.Label, window.Key)
	if label == "" {
		label = "额度"
	}
	text := fmt.Sprintf("%s窗口已用尽（剩余 %.0f%%）", label, window.Remaining*100)
	if !window.ResetAt.IsZero() {
		text += "，" + formatFreezeTime(window.ResetAt) + " 后重置"
	}
	return text
}

// instanceExhaustedReason 是实例级冻结的原因文本。
func instanceExhaustedReason(instance cpaInstanceAccountReport, accountCount int, window quota.Window) string {
	return fmt.Sprintf("CPA 实例 %s 内 %d 个启用账号额度均已用尽；%s",
		instanceLabel(instance), accountCount, windowExhaustedReason(window))
}

// instanceExhaustion 判定实例内**全部**启用账号是否都至少有一个硬性耗尽窗口。
//
// 返回值里的 account/window 是写进冻结记录的「触发者」：取最晚重置的那个账号窗口，
// 让界面上的原因与到期时间对得上。
func instanceExhaustion(accounts []cpaAccount) (bool, string, quota.Window) {
	if len(accounts) == 0 {
		return false, "", quota.Window{}
	}
	var chosen quota.Window
	chosenAccount := ""
	for _, account := range accounts {
		hard := hardQuotaWindows(account.Windows)
		if len(hard) == 0 {
			// 有一个账号还有额度：实例整体不算耗尽。
			return false, "", quota.Window{}
		}
		window := latestResetWindow(hard)
		if chosenAccount == "" ||
			window.ResetAt.After(chosen.ResetAt) ||
			(window.ResetAt.Equal(chosen.ResetAt) && window.Remaining < chosen.Remaining) {
			chosen = window
			chosenAccount = accountLabel(account)
		}
	}
	return true, chosenAccount, chosen
}

// enabledAccounts 筛出实例里的启用账号（CPA 的 disabled 才是人工停用）。
func enabledAccounts(accounts []cpaAccount) []cpaAccount {
	enabled := make([]cpaAccount, 0, len(accounts))
	for _, account := range accounts {
		if account.Disabled {
			continue
		}
		enabled = append(enabled, account)
	}
	return enabled
}

// instanceLinkIndex 算出「供应商 → CPA 实例」的关联读数与「实例 → 供应商列表」。
//
// 关联判据只有 base_url 相等（归一化后）：不猜 127.0.0.1 与 localhost 是同机、也不
// 按 label 或 ID 前缀猜——猜错的后果是「把一个实例的额度结论套到别的 Key 上」，
// 那比不联动更糟。供应商与实例都是配置里人填的，同一条地址就该写成同一串字符。
func instanceLinkIndex(cfg *config.RouterConfig, instances []cpaInstanceAccountReport) (map[string]QuotaInstanceLink, map[string][]string) {
	links := make(map[string]QuotaInstanceLink, len(instances))
	providersByInstance := make(map[string][]string, len(instances))
	if cfg == nil {
		return links, providersByInstance
	}
	byEndpoint := make(map[string]cpaInstanceAccountReport, len(instances))
	for _, instance := range instances {
		endpoint := normalizeEndpoint(instance.BaseURL)
		if endpoint == "" {
			continue
		}
		byEndpoint[endpoint] = instance
	}
	for _, provider := range cfg.Providers {
		instance, ok := byEndpoint[normalizeEndpoint(provider.BaseURL)]
		if !ok {
			continue
		}
		links[provider.ID] = QuotaInstanceLink{
			InstanceID:      instance.ID,
			EnabledAccounts: len(enabledAccounts(instance.Accounts)),
		}
		providersByInstance[instance.ID] = append(providersByInstance[instance.ID], provider.ID)
	}
	for instanceID := range providersByInstance {
		sort.Strings(providersByInstance[instanceID])
	}
	return links, providersByInstance
}

// normalizeEndpoint 归一化 base_url 以便比较：scheme/host 小写、保留端口、去路径尾斜杠。
//
// 解析失败时退化成「小写 + 去尾斜杠」而不是返回空串：配置里写 `127.0.0.1:8317`
// 这类没 scheme 的地址时，宁可比对失败（不联动）也不要让整条规则消失——退化后同一
// 种写法仍然能互相匹配上。
func normalizeEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.ToLower(strings.TrimRight(trimmed, "/"))
	}
	return strings.ToLower(parsed.Scheme) + "://" +
		strings.ToLower(parsed.Host) + strings.TrimRight(parsed.EscapedPath(), "/")
}

// accountLabel 取账号的展示名：邮箱优先，其次 label/文件名/auth_index。
func accountLabel(account cpaAccount) string {
	return firstNonEmpty(account.Email, account.Label, account.Name, account.AuthIndex)
}

// instanceLabel 取实例的展示名：label 优先，其次配置 ID。
func instanceLabel(instance cpaInstanceAccountReport) string {
	return firstNonEmpty(instance.Label, instance.ID, instance.BaseURL)
}

// formatFreezeTime 把冻结记录里的时间写成接口口径（RFC3339 UTC）；零值写空串。
func formatFreezeTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
