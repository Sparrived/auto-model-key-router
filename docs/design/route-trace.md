# 单请求选路解释设计

> 状态：**设计提案，尚未实现**。本文依据当前工作区源码作静态核对；未运行服务、构建或测试。下文第 2 节是源码可证的现状，其余接口、字段、行为和实现文件均为拟新增契约，不代表已经可用。本文只覆盖 HTTP 代理，不声称覆盖 WebSocket。

## 1. 目标与用户流程

当一次模型调用失败、发生切换或流式中断时，调用方能取得不可猜测的 AMKR 请求 ID；运维可用 ID 看到当时实际执行的模型解析、候选筛选、选中目标、各次业务上游调用、原生协议降级和最终结果。受限调用方可查询**自己的最小化记录**，但不得因诊断获得全局配置、其他候选或管理权限。每个事件是当时决策的事实，不是事后拿当前状态重算的解释。

1. 管理员显式开启追踪；旧配置默认关闭。客户端按原有方式调用 HTTP /v1 代理，不需要改请求体。
2. 追踪开启、认证成功且获得追踪容量时，AMKR 在响应头给出 `X-AMKR-Request-ID`。成功、上游错误、认证后的前置校验失败和流式响应均适用；网络中断导致头未到达时不能保证客户端拿到 ID。认证失败、关闭追踪、随机数/追踪容量异常时不承诺有头。
3. 完整权限管理员在「状态监控」输入或粘贴 ID，查看解释；受限调用方用自己当前有效的推理凭据调用查询接口，只看受限投影。流尚未结束时返回进行中；完成后刷新查看最终结果。
4. 找不到记录时提示「不存在、已过期、未保存或无权访问」，不要断言某一具体原因；存储故障则明确提示暂时不可用。记录可能因容量、进程退出、落库失败而丢失，不能把 ID 当持久化成功凭证。

**范围**：支持进入现有 HTTP 代理处理器的普通和 SSE/其他流式请求、模型直接调用/别名、任务/统一模型、主备目标、指定上游 Key、重试和原生端点降级；保留候选筛选与发送事实。**不做**：修改选路策略、自动重放请求、提示词/响应审计、统计计数改口径、跨实例聚合、WebSocket 追踪、历史记录回填、按 ID 搜索其他人的记录、分页全局追踪列表或新运行时依赖。未来新协议只有明确接入相同生命周期才能宣称支持。

## 2. 当前源码基线（事实）

| 事实 | 静态证据 |
| --- | --- |
| `/v1/` 的 WebSocket 升级走 `wsproxy`，普通 HTTP 经 `handleProxy`；代理同步写完流后才返回 | `internal/server/routes.go:153-169`、`internal/server/routes.go:228-246` |
| 代理先获取 runtime 租约，再准备请求、解析目标、执行目标；可重试时才尝试统一模型、任务或端点族备选 | `internal/proxy/handler.go:27-65`、`internal/proxy/handler.go:103-142` |
| 本地完整凭据、访问密钥和工作空间推理凭据的判定不同；访问密钥的模型清单在别名解析前、工作空间直呼模型清单在解析后生效；访问密钥不能使用任务/统一模型 | `internal/proxy/handler.go:199-219`、`internal/proxy/handler.go:241-289`、`internal/proxy/handler.go:316-341`、`internal/proxy/handler.go:452-471` |
| 池先过滤未启用、供应商作用域和额度冻结；普通选路再过滤本次排除、冷却、欠费和并发。全部冷却时会软回退，但冻结、欠费和并发不放宽；`only_first` 有专门语义，不能概括成普通软回退 | `internal/keypool/pool.go:645-690`、`internal/keypool/pool.go:720-825` |
| priority 按配置顺序；round_robin 在可用候选中先考虑粘滞，再按最低并发和游标选取；指定 Key 是另一条路径 | `internal/keypool/pool.go:828-879`、`internal/proxy/retry.go:550-574` |
| 一次入站请求可有多次尝试，备选复用整次上游调用预算；原生端点探测是额外上游调用，**不计入**该预算；同一 Key 也可能原生转兼容端点 | `internal/proxy/retry.go:45-88`、`internal/proxy/handler.go:124-138`、`internal/proxy/handler.go:600-633`、`internal/proxy/probe.go:21-33`、`internal/proxy/probe.go:74-78`、`internal/proxy/retry.go:322-343` |
| 流式响应先写 HTTP 状态行再写流；流结束时 `streamLifecycle.Finish` 用 `sync.Once` 收尾、记指标并释放 Key；SSE 传输 EOF 不等于模型完成 | `internal/proxy/stream.go:40-55`、`internal/proxy/lifecycle.go:130-187`、`internal/proxy/stream_outcome.go:21-50` |
| 指标库 `request_metrics.id` 是自增行 ID，统计口径是 `upstream_attempt`；它不是入站请求 ID，也不能据此可靠重建同一请求的所有尝试、候选或本地拒绝 | `internal/metrics/schema.go:14-35`、`internal/metrics/clock.go:33-34`、`internal/metrics/store.go:387-428` |
| 状态监控及其数据接口要求完整权限；页面的「最近失败」来自尝试明细，展示时间、请求/渠道、状态与耗时，并非单次入站请求解释 | `internal/server/status_monitor.go:20-47`、`web/src/pages/status/index.tsx:321-343`；明细查询见 `internal/metrics/store.go:818-843` |
| 管理面常规 `authorizedConfig` 只接受 full；工作空间面板 Key 是任务面的特殊例外，不能推断它能读全局监控；宿主自定义鉴权接口也不自动提供受限主体身份 | `internal/api/server.go:423-467`、`internal/auth/auth.go:23-51` |
| 已有设计提出只保存元数据、区分受限主体；它是需求方向，不是已实现的追踪接口 | 总体设计稿（本地文档，未入库） |

此外，现有请求上下文持有当代配置快照（`internal/proxy/context.go:38-44`）；指标写入使用随当前代切换的 sink（`internal/server/server.go:431-445`、`internal/server/server.go:471-486`）。因此不能假设跨热重载的每次尝试必落同一指标库。

## 3. 拟议配置与兼容

使用现有完整权限的 `GET /api/settings` / `PUT /api/settings` 扩展以下可选设置；现有设置读写和 revision 接缝见 `internal/api/handlers_meta.go:511-560`。

```json
{
  "route_trace": { "enabled": false, "retention_hours": 24 },
  "config_revision": "<读取设置时得到的版本>"
}
```

- 以上是 **PUT 请求片段的拟议形状**；GET 的 `settings.route_trace` 回显有效值和顶层 `config_revision`。缺少整个对象按关闭/24 小时处理；设置更新为部分更新，省略子字段保持原值。对象必须为对象，`enabled` 必须是布尔，`retention_hours` 必须是 1–168 的整数，否则返回现有管理 API 风格 422，不静默修正。
- 配置解析、校验、默认值、规范化、迁移、设置返回、导入导出和热重载必须一起支持。带旧 `config_revision` 的修改走现有冲突检查并保留 UI 草稿；不改旧客户端请求体和路由返回格式。设置成功与追踪存储可用是两件事：存储不可用时保留设置并报告降级，不阻断推理。
- 开关和保留期由新请求读取当代快照。**关闭只停止接纳新追踪**，已接纳的在途请求仍可收尾，历史记录仍可在各自保留期内查询；如需立即删除，v1 由管理员依照部署流程离线处置数据库和备份，不增加删除 API。记录按入站时取得的保留期计算到期时间，调整保留期仅影响新接纳的请求，既不缩短也不延长已有记录的期限。
- 响应头仅在开启且成功分配追踪时新增，对原有状态码、响应体、重试次数、选路和 `X-AMKR-Fallback` 语义零改动。准入追踪时不采用或转发客户端伪造的同名头：入站 `X-AMKR-Request-ID` 忽略并从上游请求头剥除；上游响应中的同名头丢弃，再由 AMKR 设置。追踪关闭或本次未准入时保持原有请求和响应头转发行为，不宣称已有同名头来自 AMKR。浏览器跨域读取需要部署方现有 CORS 策略显式暴露此头；本功能不放宽来源或认证策略。

## 4. 标识、数据模型与生命周期（拟新增）

### 4.1 标识和来源

- 每个**成功认证并准入追踪**的 HTTP 入站请求，在任何可能写响应头的分支前生成一个 `rt_` 加 32 位小写十六进制字符的 ID（16 字节密码学随机数）；不接受调用方指定。随机源失败则跳过追踪，原请求继续。启动时在后台将库中未删除 ID 的索引载入有界内存，并与写入、淘汰及在途 ID 维护同一同步的保留集合；新请求仅在内存中查重和保留，冲突则重新生成，不在代理热路径查库。索引未就绪或异常时跳过新追踪。落库仍以唯一约束兜底：若意外冲突则丢弃新追踪并计数，不能覆写旧记录或更换已发送的 ID。不能以时间戳、自增行号替代。
- 同一请求的主目标、Key 轮换、同 Key 重试、协议降级、备选模型以及流式收尾共享一个追踪对象和 ID；`attempt_no` 表示发送的**业务上游 HTTP 调用**序号，从 1 开始。一次 Key 选择可能发生多次业务发送；只选中但未发送时不假造 attempt。探测单独记 `probe` 事件和结果，不计为业务 attempt，也不将它误写为现有调用预算的一部分。
- 现有 `request_metrics.id` 保持上游尝试主键；v1 不更改该表、不依据时间/模型模糊 JOIN、不将它展示成追踪 ID。监控通过手动输入响应头 ID 查询，不承诺从既有「最近失败」行跳转到追踪；指标和追踪计数不能相加。需要精确逐条跳转时，须后续设计显式关联及跨库生命周期。

### 4.2 逻辑记录

以一个入站请求一条 `trace` 和有序事件数组表达。无值用 `null`，不用 0/空字符串冒充未知；所有墙上时间采用 UTC RFC 3339，耗时使用单调时钟差值。以下名称和枚举是 v1 新契约：

| 字段 | 语义与约束 |
| --- | --- |
| `request_id`、`schema_version` | 上述 ID；版本固定为 1，供将来兼容扩展 |
| `started_at`、`finished_at`、`duration_ms` | 入站时间、完成时间及耗时；在途后两项为 null |
| `status`、`outcome` | status 为 `running` / `completed`；outcome 在完成时为 `success` / `failed` / `canceled`，进行中为 null。此处 completed 仅表示追踪生命周期结束，不等于请求成功 |
| `request` | 认证后的调用方类型、有效工作空间（仅管理投影）、已匹配配置的请求模型或 null、`route_kind`（`direct` / `alias` / `task` / `unified`）、是否流式。对应事实尚未知时为 null |
| `events[]` | 单请求内从 1 递增的 `seq`、相对开始的 `offset_ms`、事件种类、该时刻的有限枚举原因及有限安全字段；按产生顺序返回 |
| `final` | 下游已提交 HTTP 状态码或 null、产生最终下游结果的**已实际发送**业务目标或 null、对应上游 HTTP 状态码或 null、流终态；发生过发送不代表请求成功。最终结果若为本地生成则目标为 null；若返回先前错误则指向该错误对应的 attempt，不能简单取最后发送的目标 |
| `truncated`、`omitted_events` | 达到事件/字节上限时为 true 和省略条数；不能声称事件完整 |
| 内部 `owner_type`、`owner_tag` | 只供权限判定，不返回 API；计算方式见 §7，绝不是上游 Key 名、工作空间请求头或指标表自增 ID |

管理投影中的目标可含配置内的 `model_id`、`provider_id`、上游 Key **显示名** `key_name` 与 `upstream_model_id`，不保存 Key 明文、尾号、指纹、上游 URL 或请求参数。只从已经成功匹配的配置获取这些受限长度的标签；未匹配的调用方输入一律置 null，绝不把任意 `model` 原文、任务参数、路径/query、请求头或错误原文塞进记录。配置标签也要检查长度、控制字符和与已知完整凭据相同的值；不安全则替换为 null，不能把脱敏失败时原样保留作为兜底。

### 4.3 事件和原因

在真实分支中记录，而非附加一次“模拟选路”：

| 事件 | 原因/细节（全部为拟定稳定枚举） |
| --- | --- |
| `resolve` | `direct`、`alias`、`task`、`unified`、`not_found`、`model_not_allowed`；计划只记录已实际解析/读取的主备，不承诺未来计划不变 |
| `candidate` | `disabled`、`provider_not_allowed`、`quota_frozen`、`excluded_after_attempt`、`cooldown`、`balance_limited`、`concurrency_limited`、`eligible_not_selected`、`selected`；只记录实际评估到的首个阻挡原因，不宣称列出所有同时存在的原因 |
| `selection` | 模式 `specified` / `only_first` / `priority` / `round_robin`，以及实际分支如 `sticky`、`lowest_active_round_robin`、`cooldown_soft_fallback`。冷却软回退后候选可先因冷却跳过又被重新选中，必须分别显示阶段，不能把冷却称为永久排除；`only_first` 和点名不得虚构未评估候选 |
| `probe` | `supported` / `unsupported` / `error` / `not_sent`，可选已知状态码；只采集有限原因，不持久化探测体或网络错误文本 |
| `attempt` | `attempt_no`、实际模型/渠道、协议族、状态码或 null、耗时、结果（`response` / `transport_error` / `not_sent`）。标记是否已真实发送；`not_sent` 的 `attempt_no` 为 null，不占发送序号。区分可重试 HTTP、传输/首字节超时、下游取消和预算耗尽，不复制上游 body |
| `fallback` | `native_to_compatible`、`model_fallback`；记录实际触发原因及前后 target，不将“配置了备选”写成“实际执行了备选” |
| `finish` | 下游状态与 `complete` / `upstream_error` / `incomplete` / `client_canceled` / `not_stream`；不能把已发出的 HTTP 200 改写成流最终成功 |

候选采集需要调整 keypool 内部调用接缝：在已有选路锁内只将有限、无敏感字段的判定结果复制到本次调用的**有界局部缓冲**，解锁后再汇入本请求 collector，不能再次调用会修改游标、获取配额或改变健康状态的选路方法。`prepare` 中的前置计数、全部冻结与点名路径也需在各自真实判定点采集，否则没有进入 `NextKey` 的拒绝将被遗漏；没有事实就显示未知。不得在池锁中写库、发网络请求、等待队列或调用任意外部回调。

### 4.4 状态、响应头和流结束

`认证成功 → 分配 ID/登记 running → 解析/筛选 → 0..N 次尝试（含必要回退）→ 写响应或流 → Finish 一次 → completed 排队落库`。前置校验失败也有 0 次上游尝试的完成记录；认证失败不建立归属追踪。发生 panic 时由现有恢复边界或新增 defer 将可安全观察到的终态记为 failed，再遵守原有 panic/HTTP 行为；进程被强杀不保证执行收尾。

响应头应由统一的代理响应写出边界在第一次 `WriteHeader` 或隐式写入前设置，并过滤上游同名头，覆盖 buffered、流式、备选及认证后的早退；流开始后不可补头。`running` 的快照可能只包含截至查询时已发生的事件。已发送响应头后，流的 EOF、上游错误、协议不完整、客户端断开通过现有流判定和 `Finish` 收尾，不再重放已经向下游输出的内容；HTTP 200 与 `outcome=failed` 可以并存。最终状态捕获以一次性收尾为准，不能让额外追踪收尾改变现有健康判定、Key 释放或指标次数。

## 5. 查询 API（拟新增）

`GET /api/route-traces/{request_id}`，认证沿用 `Authorization: Bearer <credential>` 或 `x-api-key`；不接受 query、URL fragment 或自定义工作空间头作为主体。只读，无列表、写入、重放和删除。响应均带 `Cache-Control: no-store`；服务及反向代理访问日志不得记录 Authorization，ID 只能用于定位，不能单独作为认证凭据。

**管理员 200 示例**（示意值，不是实际记录；`final.target` 为产生最终下游结果的目标）：

```json
{
  "schema_version": 1,
  "request_id": "rt_0123456789abcdef0123456789abcdef",
  "started_at": "2026-01-01T00:00:00Z",
  "finished_at": "2026-01-01T00:00:02Z",
  "duration_ms": 2000,
  "status": "completed",
  "outcome": "success",
  "request": { "model": "chat-model", "route_kind": "direct", "stream": false, "caller_type": "access_key", "workspace": "default" },
  "events": [
    { "seq": 1, "offset_ms": 1, "kind": "candidate", "stage": "primary", "reason": "quota_frozen", "target": { "model_id": "chat-model", "provider_id": "p1", "key_name": "account-a", "upstream_model_id": null } },
    { "seq": 2, "offset_ms": 2, "kind": "selection", "mode": "priority", "reason": "selected", "target": { "model_id": "chat-model", "provider_id": "p2", "key_name": "account-b", "upstream_model_id": "vendor-chat" } },
    { "seq": 3, "offset_ms": 10, "kind": "attempt", "attempt_no": 1, "sent": true, "result": "response", "target": { "model_id": "chat-model", "provider_id": "p2", "key_name": "account-b", "upstream_model_id": "vendor-chat" }, "upstream_status": 200, "duration_ms": 1990 }
  ],
  "final": { "http_status": 200, "upstream_status": 200, "target": { "model_id": "chat-model", "provider_id": "p2", "key_name": "account-b", "upstream_model_id": "vendor-chat" }, "stream_end": "not_stream" },
  "truncated": false,
  "omitted_events": 0
}
```

**受限凭据 200 示例**：只返回归属本人、与本次调用相关的粗粒度结果；不得返回 provider、上游 Key 显示名、内部模型/计划、候选数量或排除细节。其查询投影不是将管理员完整 JSON 原样返回再由浏览器隐藏。

```json
{
  "schema_version": 1,
  "request_id": "rt_0123456789abcdef0123456789abcdef",
  "started_at": "2026-01-01T00:00:00Z",
  "finished_at": "2026-01-01T00:00:02Z",
  "status": "completed",
  "outcome": "success",
  "request": { "model": "chat-model", "stream": false },
  "final": { "http_status": 200, "stream_end": "not_stream" },
  "truncated": false
}
```

进行中仍为 HTTP 200，`status=running`，`outcome`、`finished_at`、最终结果中尚不知的字段均为 null，数组仅为已观测快照；已写响应头与流结束分开表示。查询不存在、过期、被淘汰、未落库或非本人统一 404 `{"detail":"追踪不存在或不可用"}`；未认证、被停用或面板 Key 使用统一 401 `{"detail":"凭据无效"}`；已认证后 ID 格式不合法为 422 `{"detail":"请求 ID 格式无效"}`；存储当前不可读或受限查询必需的 owner key 不可用为 503 `{"detail":"追踪暂不可用"}`。先认证再校验 ID，错误不回显输入；以上是新接口契约，管理错误信封依据 `internal/api/server.go:367-389` 风格设计。只支持 GET，其他方法 405。

## 6. 存储、容量与故障降级（拟新增）

- 默认独立 SQLite：`<DefaultCacheDir>/route-traces.sqlite3`，与现有指标库同目录但**不共表、不共事务、不跟随 metrics_db_path 切换**（默认缓存路径证据见 `internal/config/coerce.go:179-194`）。包含 `route_traces(request_id PRIMARY KEY, owner_type, owner_tag, started_at, finished_at, expires_at, document_json)`，给过期时间建索引；仅存上面白名单字段，时间 UTC。由单写者在单事务内插入完成快照和按容量删除最老已完成记录；同 ID 唯一约束禁止覆盖成另一个请求。
- 入站的进行中快照仅在本进程有界内存，完成后非阻塞入队，由后台单写者持久化；不每个事件落库，也不等 SQLite IO。完成快照排队/写入期间仍保留在内存供查询，落库成功后移除；入队满或写失败则丢弃并移除、计数，不得阻塞、回滚或改变业务响应。进程重启仅恢复已提交记录，在途/队列可能丢失；这不是审计日志或可靠投递队列。
- 已完成记录默认自结束起保留 24 小时，可设 1–168 小时；到期时间为完成时刻加该请求入站时捕获的保留期，查询时按记录的 `expires_at` 拒绝过期记录，后台至少每 5 分钟分批清理。单库最多 10,000 条完成记录，超过时先淘汰最早完成记录，即使未满保留期也可提前消失。数据库事务回滚时保持旧记录不受影响。数据库、WAL/SHM、备份中的已删数据可能仍可恢复，TTL 是**在线可查询与逻辑删除**，不是物理安全擦除；管理员控制备份和清理周期。
- 初始硬上限：在途 collector 1,024 个，队列 2,048 个，单条最多 64 个事件、序列化安全数据最多 16 KiB，字符串上限 128 UTF-8 字节（按字符边界截断）；溢出保留起始、最终状态及实际目标，丢中间事件并递增省略数。以上是工程上界，不是已实测性能承诺；压测时确认 CPU、尾延迟、WAL 与磁盘水位。查询每次仅按主键读取一条，不进行全表扫描或无界排序。
- 在途追踪自开始最多保留 6 小时；超时丢弃追踪并释放容量，不取消/重试业务流，不再恢复该 ID。并发爆满、新追踪不可准入、随机数失败时不发 ID；其他未认证/关闭分支同样不发 ID。已发 ID 后存储失败可以查不到，UI 必须明确。
- 指标：仅内部聚合计数 `trace_admitted`、`trace_dropped{reason}`、`trace_store_errors`、`trace_truncated`、队列/在途数量；reason 必须有限枚举，不以 request ID、模型、凭据作高基数标签。错误日志仅限通用类型和计数，不记录任意错误原文、路径、请求体或密钥。

## 7. 权限、隐私与主体生命周期（拟新增）

1. 完整权限经现有 full 鉴权，可读任意未过期记录的管理投影；自定义鉴权只有明确返回 full 才能读管理投影。工作空间**面板 Key**不因本接口得到推理或全局诊断权限。访问密钥或工作空间**推理 Key**只可读本人当次有效凭据发起的受限投影；不能使用 `X-AMKR-Workspace` 声明归属。自定义鉴权若不能提供经验证的稳定受限主体，默认拒绝受限查询，不将其自动提升为 full。
2. 现有 Access Key ID 在轮换时稳定（`internal/config/model.go:148-160`、`internal/configops/accesskeys.go:138-161`），但删除后同名 ID 可重建；工作空间也可能改名或重建。v1 **不单靠 ID/空间名绑定历史**：在认证成功时对实际入站推理凭据计算 `HMAC-SHA-256(server_owner_key, owner_type || 0x00 || credential)`，记录固定长度 tag；查询时重新认证当前启用的同类凭据，再计算并恒定时间比较 tag。tag 不公开、不作为 bearer token，不存原始凭据或未经密钥保护的摘要。
3. 首次启用且追踪库不存在时，由服务在默认缓存目录内原子创建独立 32 字节随机 `route-trace-owner.key`（同步落盘后再创建库）；已有库时密钥缺失绝不能自动再生。权限必须限制为服务账户和明确授权的管理员，无法确认安全权限则关闭新追踪及受限查询，严禁写入配置导出、API、日志或追踪库。多进程不能同时以不同密钥访问同一追踪库；v1 单实例独占。密钥缺失/损坏或随机源不可用时禁用受限查询和新追踪，业务照常；不能自动换新密钥后假称历史仍可受限查询。管理员修复文件权限后恢复；主动轮换将使旧记录仅能由 full 管理员在保留期内查看。
4. 凭据停用、删除后立即无法再通过认证；轮换后新密钥不会认领旧密钥的追踪，旧密钥也立即失效（工作空间推理 Key 轮换见 `internal/configops/tasks.go:487-490`）。若管理员**主动恢复完全相同的凭据值**，v1 会视为同一凭据重新获得其未过期记录；这是按凭据而非逻辑实体的明确边界，运维不得把旧凭据复用于不同主体。若产品要求“删除后即使重用原凭据也永不继承”或轮换继承历史，必须另增不可复用的持久主体代次及其迁移/备份方案，不能仅使用名称或 ID。工作空间改名但推理凭据不变时，该凭据仍可读自己的记录，响应不暴露旧空间名。
5. 区分访问密钥和工作空间推理 Key 的 owner_type；本地 full 请求即使带任意空间头也不会变成该空间凭据的记录。主 Key 与上游 Key 均不得在对象、队列、库、日志、JSON 或错误信封中出现；禁止提示词、响应正文、工具参数、完整请求/响应头、来源 IP、User-Agent、上游 URL、affinity 哈希、原始上游错误文本。白名单之外丢弃，不能依赖正则在保存后再脱敏。
6. 所有查询先校验当前权限，再按 ID/owner 精确读取并服务端生成投影。无权与不存在采用相同 404，避免枚举。可在接口层施加有界并发和每主体查询速率限制，限流 429；不能向未授权请求返回是否为 running。运维部署应限制本地库/备份文件和该 API 的网络访问；持有 full 密钥或读盘权限者本来就是高权限主体。

## 8. 并发、热重载与兼容边界

- 每条请求拥有 collector，主备上下文复制时共享指针；序号、事件数组、在途快照由短锁保护，读取时复制后解锁序列化。`Finish` 独立使用一次性原语，重复取消/EOF/错误回调只产生一次最终快照；不能拿着 collector 锁进入 keypool、写 DB、阻塞队列或等待客户端。池的现有互斥顺序不反转；从池内部采集时只操作无锁/有界局部副本，在离开池锁后再合并到 collector。
- 一个事件来自哪个配置代和哪个实际池判定，以执行点快照为准；不要事后按当前配置补齐或回算。运行期冻结、并发与热重载会随时间变化，分别按每次实际选择时记录。主备计划若现有实现是在需要时重新读取，追踪如实记录读取结果，**不额外冻结计划或改变选路**。
- 配置热重载切换追踪开关不影响已准入请求；追踪库固定在独立默认路径，避免现有指标库的 `switchableSink` 在长流期间换库导致追踪分裂。关闭、新值、重启、SQLite 读写锁定/满盘均不影响代理请求成功、重试或 Key 释放。多副本或负载均衡部署下，在途记录仅位于接收该请求的实例；需要粘性查询同一实例。跨节点检索、共享 SQLite 并发写入和全局唯一查询不在 v1 承诺，待独立设计。
- 旧指标行无追踪 ID，追踪关闭时不创建记录；新追踪的 ID 不回填历史，现有 `/metrics` 和监控返回口径不变。配置保存需要覆盖旧配置、校验和导入导出；数据库创建/升级采用幂等事务，失败只禁追踪，不得阻止启动或改写已有指标。

## 9. UI 表达（拟新增）

- 在现有「状态监控」页新增「按请求 ID 查看选路」输入框和查询按钮；复用完整权限登录，结果以抽屉展示「请求概览 → 计划/筛选 → 实际发送时间线 → 最终 HTTP 与流状态」。候选与每次发送分开，清楚标出软冷却放行、重试、模型备选和协议降级。对没有观察到的阶段显示「未知/未执行」，不显示预测原因。
- 显示进行中/完成、HTTP 状态与流失败分离、截断数量和保留上限；空状态为「记录不存在、已过期、未保存或无权访问」。查询存储不可用给可重试提示。历史指标行不出现无效的追踪跳转；可从响应头复制 ID 手动查询。页面不显示密钥尾号，不把 ID 自动写进 URL、浏览器长期存储或分析事件。
- 设置页明确「默认关闭、仅保存路由元数据、尽力而为、最多保留所设时长且可能提前淘汰」；保存冲突保留用户输入。受限方调用 API 的能力不等于给予现有状态监控页完整权限，v1 不新增受限面板。

## 10. 端到端验收矩阵（实施后的目标，不是已执行结果）

| 场景 | 操作与断言 |
| --- | --- |
| 关闭/老配置 | 旧配置启动并调用；AMKR 不生成追踪头和记录，既有头透传、路由、响应、指标、旧设置不变；开启后新请求有格式正确且唯一的随机 ID |
| 前置错误 | 有效凭据发送缺模型、无权模型、全冻结等请求；若产生 ID，查询显示 0 次业务上游调用及真实拒绝阶段，HTTP 结果不变；无效凭据不产生可查询记录 |
| 候选与软回退 | 构造未启用、作用域外、冻结、欠费、并发满、冷却候选，按实际阶段展示原因；全部冷却时标注软回退，冻结/欠费/并发不因软回退放行；only_first 和点名不虚构筛选 |
| 重试与备选 | 假上游先 429/传输失败再成功，以及主模型失败转备选；同一个 ID 的发送序号单调递增，最终目标与真实接收最终响应的 attempt 一致；不改变现有重试或调用预算 |
| 原生探测 | 首次原生探测、端点不支持后兼容发送；probe 独立可见且不计入业务 attempt 与调用上限；禁记录探测载荷；探测缓存命中不伪造一次探测 |
| 流式 | 读首响应头后暂停流并查询 running，再完成查询 completed；中途客户端断开、上游错误、空/不完整流分别区分，HTTP 200 不冒充最终成功；并发竞态只完成一次、不泄漏 Key 并发名额 |
| 权限 | full 能读全量；Access Key A 不能查询 B，工作空间推理 Key A 不能读 B；伪造 workspace 头、上游 Key 名或 ID 无法越权；面板 Key 401；无权和不存在同为 404；自定义鉴权无法解析身份时安全拒绝 |
| 身份轮换与重建 | 轮换前凭据成功查询；轮换、停用或删除后旧凭据 401，新凭据不能读取旧记录；同名 ID 重建但不同凭据不可继承；显式复用完全相同凭据时按 §7 的已知边界验证 |
| 响应头与隐私 | 请求注入、上游伪造同名头均不得覆盖 AMKR ID；正常、出错和 SSE 能在写头前返回；对提示词、工具参数、Key 和自定义 header 设置哨兵，在完整响应、受限响应、追踪库/WAL 和追踪日志中均搜不到原值；已有业务日志不因本设计被宣称已清理 |
| 热重载 | 流进行时切配置、开关、指标库路径；在途 trace 跨主备仍同一 ID，历史库不分裂；开关仅影响新接纳请求，业务原有行为不变 |
| 容量与故障 | 写队列满、超过事件/大小上限、达到在途上限、长流超时、SQLite 锁定/满盘及随机源失败；业务不阻塞/改道，ID 缺失或 404/503 符合契约，截断和丢弃可观测 |
| 保留与恢复 | 按完成时间和入站时捕获的保留期推进时钟验证查询端即时过期、后台删除与 10,000 条提前淘汰；调整设置只影响新请求；重启后已提交可查，在途/队列可能丢失；缺失 owner key 时受限查询失败关闭而推理不受影响 |
| UI/旧监控 | 管理员粘贴 ID、查看进行中/完成/不存在/故障；失败明细仍按上游尝试计数，不把自增 ID 当请求 ID；校验键盘操作和隐藏受限信息 |

## 11. 建议实施顺序与文件清单（均为后续工作）

1. **核心记录和身份安全**：在 `internal/config/model.go`、`internal/config/migrate.go`、`internal/configops`、`internal/api/handlers_meta.go` 接入可选配置及校验；新增内部追踪包实现安全 collector、内存容量、独立 SQLite、owner key、TTL、失败计数与单元测试。
2. **真实链路采集**：`internal/proxy/handler.go`、`internal/proxy/context.go`、`internal/proxy/retry.go`、`internal/proxy/probe.go`、`internal/proxy/stream.go`、`internal/proxy/lifecycle.go`、`internal/keypool/pool.go` 与 `internal/proxysupport/support.go` 修改实际分支和响应头边界；对应测试覆盖流程、锁顺序、流结束及伪造头。不要把追踪事件写到原指标表或把既有上游尝试计数改为入站请求数。
3. **接入与展示**：`internal/api/router.go`、新增查询处理器与 `internal/server/server.go` 装配服务；鉴权明确区分 full、推理凭据及宿主自定义身份，增加接口/安全/容量测试。`web/src/lib/api.ts`、`web/src/lib/types.ts`、`web/src/pages/status/index.tsx` 和设置页接入，新增实际用户交互测试；更新使用及 API 文档。
4. **验证**：完成后由独立测试角色使用假上游模拟真实 HTTP 客户端、SSE、并发、注入和故障，执行上表矩阵、仓库 Go/前端检查及性能测试；本次设计阶段没有执行这些验证。

### 待确认

- 产品是否接受「轮换后受限凭据不能读旧追踪」「主动复用相同凭据将继承未过期追踪」；若不接受，实施前必须设计持久主体代次。
- 默认保留 24 小时、10,000 条和 6 小时在途上限是否符合部署容量；跨实例/反向代理粘性与 CORS 是否由部署保证。
- 可承受的吞吐、P95/P99 额外延迟、数据库/备份磁盘预算尚无实测，实施前须以代表性负载确认，不能从本文的硬上限推导性能保证。
