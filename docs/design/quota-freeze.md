# 账号额度冻结（quota freeze）设计稿

面向维护者的实现契约。目标是让「账号资源」页已有的额度读数**真正参与选路**：
5h / 7d 等窗口耗尽或剩余比例低于配置底限后，AMKR 立即把对应的上游 Key 从候选里摘掉，
不再把必然失败的请求打给上游；冻结由首次捕获的窗口刷新时间驱动内置计时器自动解除，
窗口重置或重新观测到有余额时也会提前解冻。

## 1. 语义边界

- 冻结的是 **Provider Key**（`providers.<id>.keys.<name>`），不是供应商整体，也不是
  配置里的 `enabled` 字段。二者必须分开：
  - `enabled=false` 是**人工停用**，长期、由人在配置里显式设置；
  - 额度冻结是**自动的临时状态**，到期或观测到有余额即自动解除，不写回配置。
- 冻结对选路是**硬约束**，与并发上限同级：冷却的软回退（全部冷却时放行冷却中的 Key）
  **不**放行冻结中的 Key。全部候选都被冻结时，模型等同没有可用 Key（503 + 原因）。
- **提前冻结白名单**：`quota_freeze_threshold_whitelist` 里的模型只豁免 `trigger=threshold`
  的冻结，可以继续把该 Key 的额度用到硬性耗尽；`trigger=hard` 对所有模型都不可豁免。
  同一把 Key 的一次冻结只要汇总出任意硬性耗尽窗口，就必须记为 `hard`，不能被白名单绕过。
- 冻结状态**持久化**（独立状态文件，不进配置）：进程重启、配置热重载都不能让耗尽的
  Key 复活。状态文件损坏/缺失按空表处理。
- 每条冻结在**首次写入时**捕获一个解冻时刻（窗口的 `reset_at`；拿不到时 `now + 10 分钟`），
  由服务端内置计时器在那一刻自动解冻并落盘。后续巡检只允许把计时器**提前**，绝不能
  再把解冻时刻往后推：上游长期回旧读数时，冻结不能变成无限续期。
- 只影响 AMKR 自己的选路，不向上游 / CPA 写任何状态。
- 手工解冻（供应商页或账号资源页点「解除冻结」）只解除本次冻结，并给该 Key 一个
  **抑制窗口**：窗口内即使观测仍显示耗尽也不再自动冻结（否则下一次巡检立刻冻回去，
  按钮等于没用）；抑制窗口取「冻结记录的到期时间」与「现在 + 10 分钟」的较大者。

## 2. 触发源与判定

冻结有两条触发线：

1. **硬性耗尽**：窗口 `remaining <= 0`，或窗口状态词表示上游已拒绝
   （`rejected` / `exceeded` / `rate_limited` / `rate-limited` / `limit_reached`）。
2. **低于底限（提前冻结）**：5h / 7d 窗口的剩余比例 `remaining <= 配置底限`。
   底限按窗口语义匹配（`claude/5h`、`codex` 的 5 小时窗口、`commandcode/fiveHour` 等
   都算 5h；`claude/7d`、`7d_oi`、`weekly` 都算 7d）。底限配置为 `0` 时这条线关闭。
   状态词是 `allowed_warning` 但比例已低于底限时，按「提前冻结」处理。

`allowed_warning` 且高于底限的窗口**不**冻结。

| 触发源 | 判定 | 冻结目标 | 解冻条件 |
| --- | --- | --- | --- |
| 订阅类账号资源（OpenCode Go / Command Code） | 该条目的任一窗口硬性耗尽或低于对应底限 | 该条目出处的 `(provider_id, key_name)` 这一把 Key | 内置计时器到期 / 下一次成功观测里没有触发窗口 |
| CPA 实例账号（实例级联动） | 实例内**全部**启用账号都至少有 1 个窗口硬性耗尽或低于底限 | `base_url` 指向该实例的**所有启用 Key**（同供应商下逐把） | 内置计时器到期 / 该实例至少有一个账号不再触发 |
| 上游响应头（L3 被动信号） | 响应头里出现硬性耗尽或低于底限的窗口 | 该次响应对应的上游 Key | 内置计时器到期 / 账号资源观测到健康 / 手工解冻 |

补充规则：

- **无意见不改变现状**：额度读取失败、实例不可达、账号额度未知（`quota_error`）、
  实例没有启用账号时，既不冻结也不解冻（不能把「读不到」当成「有余额」）。
- **L3 与多账号实例**：CPA 一个实例后面可能挂多个账号，单次响应里的限额头只说明
  「刚才那个账号」用完了，不代表整把 Key 不可用。因此 L3 的落点是：
  - 供应商未关联 CPA 实例，或关联实例只有 1 个启用账号 → 直接冻结；
  - 关联实例有多个启用账号（或关联关系尚未评估出来）→ 不直接冻结，改为**触发一次
    即时重新评估**，由实例级规则决定。
- **内置计时器（解冻时刻）**：首次冻结时取触发窗口里最晚的 `reset_at` 作为计时器目标，
  不做额外宽限；拿不到 `reset_at` 时用 `now + 10 分钟`。计时器到点由服务端主动删除冻结
  并落盘，不依赖下一轮巡检、也不依赖页面刷新。
  - 同一把 Key 已冻结时，后续观测只允许把解冻时刻**提前**（例如先按兜底 10 分钟冻上，
    随后拿到更早的 `reset_at`），不允许延后；否则上游持续回旧读数会让冻结无限续期。
  - 触发窗口的 `reset_at` 已经过去时，**不新建冻结**：这条读数已经过期，等下一轮拿到
    新的刷新时间再判断；这正是「到点自动解冻后不会再被旧读数冻回去」的保证。
  - 计时器到期后若下一次观测仍命中触发线，只有给出了**新的、未来的** `reset_at` 才会
    重新冻结；上游一直回旧 `reset_at` 时保持解冻。
- **失效清理**：配置里已不存在的 `(provider, key)`、已被人工停用的 Key、已到期的
  冻结与抑制记录，在每次评估时清理。

## 3. 状态模型（`internal/quota`）

```go
// Freeze 是一条额度冻结记录。时间字段一律 UTC。
type Freeze struct {
    ProviderID  string    // 供应商 ID
    KeyName     string    // 供应商下的 Key 名
    Vendor      string    // 厂商/订阅名，供界面显示（OpenCode Go、Claude…）
    Scope       string    // "subscription" | "cpa_instance" | "upstream"
    Source      string    // "account_resource" | "upstream_header"
    Account     string    // 触发冻结的账号标识（邮箱/文件名等，可空）
    InstanceID  string    // CPA 实例 ID（scope=cpa_instance 时非空）
    WindowKey   string    // 窗口稳定标识，如 claude/5h
    WindowLabel string    // 人话标签，如「5 小时」
    Remaining   float64   // 触发时的剩余比例（0..1）
    Status      string    // 触发时上游给的窗口状态词（可空）
    Trigger     string    // "hard"（硬性耗尽）| "threshold"（低于底限）
    Threshold   float64   // 触发时的底限（0..1；trigger=threshold 时有值）
    ResetAt     time.Time // 触发时捕获的窗口刷新时间（零值=未知）
    FrozenAt    time.Time // 首次冻结时刻
    ExpiresAt   time.Time // 内置计时器的解冻时刻；首次写入后只可提前、不可延后
    Reason      string    // 一句人话，界面直接显示
}
```

`Store`（`internal/quota/freeze.go`）对外接口：

```go
func NewStore(path string) *Store                 // path 为空 = 只驻内存
func NewStoreWithClock(path string, clock func() time.Time) *Store
func (s *Store) Snapshot() []Freeze                              // 未过期，按 provider/key 排序
func (s *Store) Get(providerID, keyName string) (Freeze, bool)   // 未过期
func (s *Store) IsFrozen(providerID, keyName string) bool        // 选路热路径用
func (s *Store) Upsert(f Freeze) (bool, error)                   // 新增或刷新；见下方计时器规则
func (s *Store) Release(providerID, keyName string, suppressUntil time.Time) (Freeze, bool, error)
func (s *Store) Suppressed(providerID, keyName string) bool
func (s *Store) Prune(valid func(providerID, keyName string) bool) (removed []Freeze, err error)
func (s *Store) ExpireDue() (removed []Freeze, err error)         // 内置计时器到点清理并落盘
func (s *Store) NextExpiry() (deadline time.Time, ok bool)        // 最早的解冻时刻
```

持久化文件 JSON（原子写：同目录临时文件 + rename；权限 0644）：

```json
{"version": 1, "updated_at": "...", "freezes": [{"provider_id": "...", "key_name": "...", "...": "..."}],
 "suppressions": [{"provider_id": "...", "key_name": "...", "until": "..."}]}
```

`Upsert` 对同一 `(provider_id, key_name)` 只保留一条：刷新 `Reason` / 窗口字段，保留
首次的 `FrozenAt`；`ExpiresAt` 是内置计时器目标，已生效时只允许被更早的值替换，
绝不被更晚的值延长。`Set`/`Release`/`Prune`/`ExpireDue` 在内容变化时落盘；落盘失败
只记日志（不阻断选路）。

状态文件路径由装配层给：`<配置文件目录>/quota-freezes.json`；配置文件路径为空
（嵌入/测试场景）时用内存态。

## 4. 评估器（`internal/api` 的 `QuotaGuard`）

`QuotaGuard` 同时承担后台巡检、页面即时评估与 L3 信号落地，三处共用同一套判定：

```go
type QuotaGuardOptions struct {
    Server *Server
    Store  *quota.Store
    Config func() *config.RouterConfig
    Logger *slog.Logger
    Now    func() time.Time
}

func NewQuotaGuard(opts QuotaGuardOptions) *QuotaGuard
func (g *QuotaGuard) Run(ctx context.Context)                       // 后台循环，ctx 取消退出
func (g *QuotaGuard) Trigger()                                      // 请求尽快评估（热重载等）
func (g *QuotaGuard) Evaluate(ctx context.Context) QuotaEvaluation  // 采集 + 应用
func (g *QuotaGuard) ApplyReport(report cpaAccountsReport) QuotaEvaluation // 用已有读数评估
func (g *QuotaGuard) ObserveUpstreamQuota(providerID, keyName string, headers http.Header)
func (g *QuotaGuard) Status() QuotaGuardStatus
```

- 后台周期取**当前运行时配置**的 `quota_freeze_interval_seconds`；`0` 表示整套机制关闭
  （不再巡检、L3 不落地、页面不进评估 → 不产生新冻结），但**已有冻结记录不抹掉**：仍展示、
  仍参与选路，直到内置计时器到期或手工解冻。热重载改了这个值无需重启，下一轮即生效。
- `Run` 的 `select` 同时维护两件事：巡检间隔与**下一条冻结的解冻时刻**。解冻时刻先到就
  先清理并落盘，不等待下一次巡检；新增/解除冻结会唤醒循环重算最近截止时间。即使
  `interval_seconds=0`，已有冻结的内置计时器仍然生效。
- 同一时刻只允许一个评估在跑（互斥）；巡检与页面评估撞上时后来者直接复用上一份结果，
  不重复扇出。启动时立即评估一次（不阻塞服务启动）。
- 阈值判定读取当前运行时配置的 `quota_freeze_threshold_5h` / `quota_freeze_threshold_7d`
  （0..1 的剩余比例，0 关闭），与硬性耗尽共用 `quota.TriggerWindows` 这一份筛选实现。
  `quota_freeze_threshold_whitelist` 在 keypool 侧生效：只豁免 `trigger=threshold` 的冻结；
  一次冻结只要汇总出任意硬性耗尽窗口就必须记 `hard`，白名单不能绕过。
- `ObserveUpstreamQuota` 在**代理请求路径**上被调用：只做「有没有额度头 → 解析 →
  非阻塞投递一个信号」，绝不在这里做文件 IO 或网络请求。信号由后台循环合并处理。
- `ApplyReport` 更新「供应商 → CPA 实例」关联表（供 L3 判断单/多账号实例）并落盘变化。

后台循环与页面路径的采集复用现有 `collectAccountsReport`（CPA 实例 + 订阅同一次扇出），
不新写抓取逻辑。

## 5. KeyPool 接入（`internal/keypool/freeze.go`）

- `SetQuotaFreezes(freezes QuotaFreezes)`：装配层注入；`nil` 表示不启用（测试默认）。
  keypool 只依赖一个最小接口（`IsFrozen` / `FrozenReason`），不 import `internal/quota`——
  选路层不该依赖状态文件包，这也让冻结表在测试里可以用假实现替换。白名单需要额外的
  `FrozenTrigger` 时做可选接口探测；探测不到按 hard 保守处理。
- 冻结是**候选集合级**的硬过滤（在 `keysForModelLocked` 里剔除），因此
  `KeyCount` / `KeysForModel` / 选路三处天然一致。`AvailableModelIDs` 是例外：它只看
  配置上的启用 Key，不因冻结把模型从 `/v1/models` 摘掉，冻结期间的调用在选路层失败。
- `NextKey` 的软回退（冷却放行）**不**放行冻结 Key；`only_first` 模式下第一把被冻结时
  直接失败。
- 点名路径（`KeyByName` / `AcquireKey`）遇到冻结 Key 也要失败，不能绕过；提前冻结对
  `quota_freeze_threshold_whitelist` 里的模型是唯一例外，硬性耗尽仍按硬约束处理。
- 新增哨兵 `ErrQuotaFrozen` 与带明细的错误类型；全部候选被冻结时的文案要指出是
  「额度冻结」以及窗口/重置时间，不要退化成「没有可用 key」。
- 代理侧把 `ErrQuotaFrozen` 映射成 503，响应体沿用现有错误信封。

## 6. 接口与 JSON 形状

### 6.1 新增管理接口

`GET /api/quota-freezes`（完整管理权限）

```json
{
  "fetched_at": "2026-10-25T12:00:00Z",
  "enabled": true,
  "interval_seconds": 60,
  "threshold_5h": 0.05,
  "threshold_7d": 0.1,
  "last_evaluated_at": "2026-10-25T11:59:30Z",
  "last_error": "",
  "freezes": [{
    "provider_id": "cpa-claude", "key_name": "claude-pro", "vendor": "Claude",
    "scope": "cpa_instance", "source": "account_resource",
    "account": "ops@example.com", "instance_id": "jp-cpa-claude",
    "window_key": "claude/5h", "window_label": "5 小时",
    "remaining": 0.03, "status": "allowed",
    "trigger": "threshold", "threshold": 0.05,
    "reset_at": "2026-10-25T15:00:00Z",
    "frozen_at": "2026-10-25T11:00:00Z",
    "expires_at": "2026-10-25T15:00:00Z",
    "reason": "5 小时窗口剩余 3%，低于 5% 底限，15:00 后重置"
  }]
}
```

`DELETE /api/quota-freezes/{provider_id}/{key_name}`（完整管理权限）

- 200：`{"released": true, "provider_id": "...", "key_name": "...", "suppressed_until": "..."}`
- 404：该 Key 不在配置里 / 当前没有冻结（`{"detail": "..."}`，沿用现有错误信封）
- 未装配冻结表（嵌入场景关闭了该机制）时 503。

### 6.2 账号资源响应（`GET /api/cpa-accounts`）新增字段

- 顶层：`freezes`（同 6.1 的 `freezes` 数组，全部当前冻结）、`guard`
  （`{enabled, interval_seconds, threshold_5h, threshold_7d, last_evaluated_at, last_error}`）。
- `subscriptions[]`：`freeze`（`QuotaFreeze` 视图或省略）。
- `instances[]`：`linked_providers`（`base_url` 指向该实例的供应商 ID 列表）、
  `all_accounts_exhausted`（实例内启用账号是否全部硬性耗尽）、
  `all_accounts_below_floor`（是否全部低于底限；与前者可同时为 true）、
  `frozen_keys`（该实例关联供应商下当前被冻结的 `{provider_id, key_name}` 列表）。
- `instances[].accounts[]`：`quota_exhausted`（该账号是否有硬性耗尽窗口）、
  `quota_below_floor`（该账号是否有低于底限的窗口）。

页面刷新本身会触发一次即时评估（`ApplyReport`），因此「打开账号资源页」=「立刻检测」。

## 7. 前端交互

### 7.1 账号资源页

- 页头说明补充：后台每 `interval_seconds` 秒自动检测；进页立即评估一次；命中硬性
  额度或低于 5h/7d 底限时对应 Key 会被**自动冻结**（不写回配置的启用开关）；冻结按
  捕获的刷新时刻由内置计时器自动解冻。
- 顶部指标条新增「额度冻结」计数。
- 冻结的倒计时**以 `expires_at`（内置计时器）为准**，文案是「X 分钟后自动解冻」，
  不再拿窗口的 `reset_at` 当倒计时；`reset_at` 只作为「窗口刷新时间」的说明。
- 页面不轮询，但用一个轻量计时器在最近一条冻结的 `expires_at` 到点时自动重新拉取
  账号资源（到点后徽标必须自己消失，不能等用户手动刷新）。
- 订阅卡片：有 `freeze` 时显示 critical/warn 徽标「额度冻结」/「提前冻结」+ 原因/
  自动解冻倒计时 + 「解除冻结」按钮（调用 DELETE，成功后刷新账号资源与冻结列表查询）。
- CPA 实例区块：`all_accounts_exhausted` 或 `all_accounts_below_floor` 时显示横幅
  「实例内全部账号额度均已用尽/均已低于冻结底限，已冻结指向该实例的 N 把 Key」，
  列出 `frozen_keys` 与逐把「解冻」按钮。
- 账号卡片：`quota_exhausted` 显示「额度用尽」徽标，`quota_below_floor` 显示
  「低于底限」徽标。

### 7.2 供应商页

- Key 表每一行：该 Key 有冻结记录时，Key 名后显示「额度冻结」/「提前冻结」徽标，
  tooltip 给原因 + 自动解冻倒计时 + 绝对解冻时刻；行操作菜单出现「解除冻结」（仅冻结时）。
- 同样按 `expires_at` 到点自动刷新冻结列表，倒计时随内置计时器走。
- 页面顶部（或 Key 区块上方）在存在冻结时显示一条提示：`N 把上游 Key 因额度耗尽或
  低于底限被自动冻结（不影响启用开关）`，可跳转账号资源页。
- 冻结**不改**启用开关的显示值：开关仍表示配置里的 `enabled`。

## 8. 配置项

| 键 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `quota_freeze_interval_seconds` | number | `60` | 额度冻结后台巡检间隔（秒）；`0` 关闭整套机制（后台巡检、L3 信号、页面评估全部不生效，不产生新冻结；已有冻结仍按内置计时器到期或手工解冻） |
| `quota_freeze_threshold_5h` | number | `0` | 5h 窗口的剩余比例底限（0..1）；`remaining <= 该值` 时提前冻结，`0` 表示只在硬性耗尽时冻结 |
| `quota_freeze_threshold_7d` | number | `0` | 7d 窗口的剩余比例底限（0..1）；规则同上 |
| `quota_freeze_threshold_whitelist` | string[] | `[]` | 提前冻结期间仍允许使用 Key 的模型名（ID 或别名）；只豁免 `threshold` 冻结，`hard` 冻结对所有模型生效 |

- 状态文件路径不新增配置键：固定为 `<config 文件目录>/quota-freezes.json`。
- `config.EmptyConfigDict` 的键序、README / docs/USAGE.md 的配置表、`router-config.example.json`
  需要同步。

## 9. 模块边界（并行开发用）

| 归属 | 文件 |
| --- | --- |
| quota-core | `internal/quota/**`（窗口解析 + 冻结表）、`internal/api/cpa_quota.go` 改为复用 quota 包 |
| keypool-l3 | `internal/keypool/freeze.go` + `internal/keypool/pool.go`、`internal/proxy/proxy.go`（Options/接口）、`internal/proxy/retry.go`（观察点与 503 映射） |
| guard-api | `internal/api/quota_guard.go`、`internal/api/quota_freeze.go`（新 handler）、`internal/api/handlers_cpa.go`、`internal/api/subscriptions.go`、`internal/api/router.go`、`internal/api/server.go`、`internal/server/server.go`、`internal/server/reload.go` |
| webui | `web/src/**` |
| lead | 本设计稿、`internal/config/**`（新配置项）、README/docs/CHANGELOG、最终构建与提交 |

## 10. 验收要点

- 单测：窗口解析（anthropic/codex/异常值）、窗口语义（5h/7d 识别）、底限筛选、冻结表
  （持久化/到期/抑制/并发/损坏文件/计时器只提前不延后/`ExpireDue`）、评估规则（订阅、
  实例级、无意见、清理、L3 单/多账号分流、低于底限、过去 reset 不冻结）、KeyPool
  （硬排除、软回退不放行、only_first、点名路径、503 错误、提前冻结白名单、硬性耗尽
  不豁免）、API 形状、前端组件。
- 端到端：写一份含订阅与 CPA 实例的临时配置 + fake 额度端点，跑起 App，验证
  「观测到 5h 低于底限 → 提前冻结并拿到 reset_at 作为 expires_at → `/v1/models` 里该
  模型仍列出、调用被 503 拒绝 → 不做任何人工操作，计时器到点后冻结自动消失、模型恢复
  可调用」；带白名单时同名模型在提前冻结期间仍可调用，硬性耗尽后立即被拒。另外验证
  上游持续回同一个过去的 `reset_at` 时不会被重新冻回去。
- 不改动任何真实生产配置；临时文件全部清理。
