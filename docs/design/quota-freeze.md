# 账号额度冻结（quota freeze）设计稿

面向维护者的实现契约。目标是让「账号资源」页已有的额度读数**真正参与选路**：
5h / 7d 等硬性窗口耗尽后，AMKR 立即把对应的上游 Key 从候选里摘掉，不再把必然失败的
请求打给上游；窗口重置或重新观测到有余额后自动解冻。

## 1. 语义边界

- 冻结的是 **Provider Key**（`providers.<id>.keys.<name>`），不是供应商整体，也不是
  配置里的 `enabled` 字段。二者必须分开：
  - `enabled=false` 是**人工停用**，长期、由人在配置里显式设置；
  - 额度冻结是**自动的临时状态**，到期或观测到有余额即自动解除，不写回配置。
- 冻结对选路是**硬约束**，与并发上限同级：冷却的软回退（全部冷却时放行冷却中的 Key）
  **不**放行冻结中的 Key。全部候选都被冻结时，模型等同没有可用 Key（503 + 原因）。
- 冻结状态**持久化**（独立状态文件，不进配置）：进程重启、配置热重载都不能让耗尽的
  Key 复活。状态文件损坏/缺失按空表处理。
- 只影响 AMKR 自己的选路，不向上游 / CPA 写任何状态。
- 手工解冻（供应商页或账号资源页点「解除冻结」）只解除本次冻结，并给该 Key 一个
  **抑制窗口**：窗口内即使观测仍显示耗尽也不再自动冻结（否则下一次巡检立刻冻回去，
  按钮等于没用）；抑制窗口取「冻结记录的到期时间」与「现在 + 10 分钟」的较大者。

## 2. 触发源与判定

「硬性耗尽」只有一个判据：窗口 `remaining <= 0`，或窗口状态词表示上游已拒绝
（`rejected` / `exceeded` / `rate_limited` / `rate-limited` / `limit_reached`）。
`allowed_warning` 等预警状态**不**冻结。

| 触发源 | 判定 | 冻结目标 | 解冻条件 |
| --- | --- | --- | --- |
| 订阅类账号资源（OpenCode Go / Command Code） | 该条目的任一窗口硬性耗尽 | 该条目出处的 `(provider_id, key_name)` 这一把 Key | 下一次成功观测里没有硬性耗尽窗口 |
| CPA 实例账号（实例级联动） | 实例内**全部**启用账号都至少有 1 个窗口硬性耗尽 | `base_url` 指向该实例的**所有启用 Key**（同供应商下逐把） | 该实例至少有一个账号不再硬性耗尽 |
| 上游响应头（L3 被动信号） | 响应头里出现硬性耗尽窗口 | 该次响应对应的上游 Key | 到期自动解冻 / 账号资源观测到健康 / 手工解冻 |

补充规则：

- **无意见不改变现状**：额度读取失败、实例不可达、账号额度未知（`quota_error`）、
  实例没有启用账号时，既不冻结也不解冻（不能把「读不到」当成「有余额」）。
- **L3 与多账号实例**：CPA 一个实例后面可能挂多个账号，单次响应里的限额头只说明
  「刚才那个账号」用完了，不代表整把 Key 不可用。因此 L3 的落点是：
  - 供应商未关联 CPA 实例，或关联实例只有 1 个启用账号 → 直接冻结；
  - 关联实例有多个启用账号（或关联关系尚未评估出来）→ 不直接冻结，改为**触发一次
    即时重新评估**，由实例级规则决定。
- **冻结到期时间**：取本次硬性耗尽窗口里最晚的 `reset_at` 再宽限 60 秒；拿不到
  `reset_at` 时用 `now + 10 分钟`。到期即自动解除（防止额度端点长期不可达导致永久冻结），
  之后若再次观测到耗尽会重新冻结。
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
    ResetAt     time.Time // 窗口重置时间（零值=未知）
    FrozenAt    time.Time // 首次冻结时刻
    ExpiresAt   time.Time // 到期自动解冻
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
func (s *Store) Upsert(f Freeze) (bool, error)                   // 新增或刷新；返回是否有变化
func (s *Store) Release(providerID, keyName string, suppressUntil time.Time) (Freeze, bool, error)
func (s *Store) Suppressed(providerID, keyName string) bool
func (s *Store) Prune(valid func(providerID, keyName string) bool) (removed []Freeze, err error)
```

持久化文件 JSON（原子写：同目录临时文件 + rename；权限 0644）：

```json
{"version": 1, "updated_at": "...", "freezes": [{"provider_id": "...", "key_name": "...", "...": "..."}],
 "suppressions": [{"provider_id": "...", "key_name": "...", "until": "..."}]}
```

`Upsert` 对同一 `(provider_id, key_name)` 只保留一条：刷新 `ExpiresAt` / `Reason` /
窗口字段，保留首次的 `FrozenAt`。`Set`/`Release`/`Prune` 在内容变化时落盘；落盘失败
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
  仍参与选路，直到自然到期或手工解冻。热重载改了这个值无需重启，下一轮即生效。
- 同一时刻只允许一个评估在跑（互斥）；巡检与页面评估撞上时后来者直接复用上一份结果，
  不重复扇出。启动时立即评估一次（不阻塞服务启动）。
- `ObserveUpstreamQuota` 在**代理请求路径**上被调用：只做「有没有额度头 → 解析 →
  非阻塞投递一个信号」，绝不在这里做文件 IO 或网络请求。信号由后台循环合并处理。
- `ApplyReport` 更新「供应商 → CPA 实例」关联表（供 L3 判断单/多账号实例）并落盘变化。

后台循环与页面路径的采集复用现有 `collectAccountsReport`（CPA 实例 + 订阅同一次扇出），
不新写抓取逻辑。

## 5. KeyPool 接入（`internal/keypool/freeze.go`）

- `SetQuotaFreezes(freezes QuotaFreezes)`：装配层注入；`nil` 表示不启用（测试默认）。
  keypool 只依赖一个最小接口（`IsFrozen` / `FrozenReason`），不 import `internal/quota`——
  选路层不该依赖状态文件包，这也让冻结表在测试里可以用假实现替换。
- 冻结是**候选集合级**的硬过滤（在 `keysForModelLocked` 里剔除），因此
  `KeyCount` / `AvailableModelIDs` / 选路三处天然一致。
- `NextKey` 的软回退（冷却放行）**不**放行冻结 Key；`only_first` 模式下第一把被冻结时
  直接失败。
- 点名路径（`KeyByName` / `AcquireKey`）遇到冻结 Key 也要失败，不能绕过。
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
  "last_evaluated_at": "2026-10-25T11:59:30Z",
  "last_error": "",
  "freezes": [{
    "provider_id": "cpa-claude", "key_name": "claude-pro", "vendor": "Claude",
    "scope": "cpa_instance", "source": "account_resource",
    "account": "ops@example.com", "instance_id": "jp-cpa-claude",
    "window_key": "claude/5h", "window_label": "5 小时",
    "remaining": 0, "status": "rejected",
    "reset_at": "2026-10-25T15:00:00Z",
    "frozen_at": "2026-10-25T11:00:00Z",
    "expires_at": "2026-10-25T15:01:00Z",
    "reason": "5 小时窗口已用尽（剩余 0%），15:00 后重置"
  }]
}
```

`DELETE /api/quota-freezes/{provider_id}/{key_name}`（完整管理权限）

- 200：`{"released": true, "provider_id": "...", "key_name": "...", "suppressed_until": "..."}`
- 404：该 Key 不在配置里 / 当前没有冻结（`{"detail": "..."}`，沿用现有错误信封）
- 未装配冻结表（嵌入场景关闭了该机制）时 503。

### 6.2 账号资源响应（`GET /api/cpa-accounts`）新增字段

- 顶层：`freezes`（同 6.1 的 `freezes` 数组，全部当前冻结）、`guard`
  （`{enabled, interval_seconds, last_evaluated_at, last_error}`）。
- `subscriptions[]`：`freeze`（`QuotaFreeze` 视图或省略）。
- `instances[]`：`linked_providers`（`base_url` 指向该实例的供应商 ID 列表）、
  `all_accounts_exhausted`（实例内启用账号是否全部硬性耗尽）、
  `frozen_keys`（该实例关联供应商下当前被冻结的 `{provider_id, key_name}` 列表）。
- `instances[].accounts[]`：`quota_exhausted`（该账号是否有硬性耗尽窗口）。

页面刷新本身会触发一次即时评估（`ApplyReport`），因此「打开账号资源页」=「立刻检测」。

## 7. 前端交互

### 7.1 账号资源页

- 页头说明补充：后台每 `interval_seconds` 秒自动检测；进页立即评估一次；命中硬性
  额度后对应 Key 会被**自动冻结**（不写回配置的启用开关）。
- 顶部指标条新增「额度冻结」计数。
- 订阅卡片：有 `freeze` 时显示 critical 徽标「额度冻结」+ 原因/重置倒计时 +
  「解除冻结」按钮（调用 DELETE，成功后刷新账号资源与冻结列表查询）。
- CPA 实例区块：`all_accounts_exhausted` 时显示 critical 横幅「实例内全部账号额度均已
  用尽，已冻结指向该实例的 N 把 Key」，列出 `frozen_keys` 与逐把「解冻」按钮。
- 账号卡片：`quota_exhausted` 显示「额度用尽」徽标。

### 7.2 供应商页

- Key 表每一行：该 Key 有冻结记录时，Key 名后显示「额度冻结」徽标，tooltip 给原因 +
  重置时间；行操作菜单出现「解除冻结」（仅冻结时）。
- 页面顶部（或 Key 区块上方）在存在冻结时显示一条提示：`N 把上游 Key 因额度耗尽被
  自动冻结（不影响启用开关）`，可跳转账号资源页。
- 冻结**不改**启用开关的显示值：开关仍表示配置里的 `enabled`。

## 8. 配置项

| 键 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `quota_freeze_interval_seconds` | number | `60` | 额度冻结后台巡检间隔（秒）；`0` 关闭整套机制（后台巡检、L3 信号、页面评估全部不生效，不产生新冻结；已有冻结仍在选路里生效至到期或手工解冻） |

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

- 单测：窗口解析（anthropic/codex/异常值）、冻结表（持久化/到期/抑制/并发/损坏文件）、
  评估规则（订阅、实例级、无意见、清理、L3 单/多账号分流）、KeyPool（硬排除、软回退
  不放行、only_first、点名路径、503 错误）、API 形状、前端组件。
- 端到端：写一份含订阅与 CPA 实例的临时配置 + fake 额度端点，跑起 App，验证
  「观测到 5h 用尽 → 冻结 → `/v1/models` 里该模型消失 / 选中被拒 → 窗口重置后解冻」。
- 不改动任何真实生产配置；临时文件全部清理。
