# API 文档

本文档描述 Auto Model Key Router 当前提供的 HTTP、SSE 和 WebSocket 接口。

默认服务地址：

```text
http://127.0.0.1:8000
```

## 鉴权

除 `HEAD /` 与 `GET /health` 外，业务接口通常需要本地 API key。

```http
Authorization: Bearer your-local-api-key
```

也支持：

```http
x-api-key: your-local-api-key
```

鉴权规则：

| 调用方 | 可访问接口 | 说明 |
| --- | --- | --- |
| 本地 API key | `/v1/models`、`/v1/*`、`/metrics`、`/api/*` | 完整权限 |
| **访问密钥**（`amkr_ak_…`） | `/v1/models`、`/v1/*`、`/ui/access-key-usage.json` | 每把密钥自带 `providers` / `models` 两份清单；不绑定工作空间，随 `X-AMKR-Workspace` 头走。**不能**用 `unified-model` 与任务名，拿不到 `/metrics` 与管理接口。看板端点只回**这把 key 自己**的用量 |
| **工作空间推理 key**（`amkr_ik_…`） | `/v1/models`、`/v1/*` | 空间由 key **钉死**（`X-AMKR-Workspace` 被忽略）；只能用任务名，或 `workspaces.<空间>.models` 清单内的模型 |
| 工作空间面板 key（`amkr_ws_…`） | `/api/tasks*`、`/ui/workspace-panel.json` | 空间由 key 钉死；**不能**用 `/v1/*` |
| 无 key | `HEAD /`、`GET /health` | 当运行时 `local_api_key` 为空时，其他接口也按本地完整权限处理 |

访问密钥取代了原先的固定 visitor key `amkr-visitor`（已整体移除，该字符串现在与任何错误凭据一样被 `401` 拒绝）。真实模型名直接暴露，不再有 `amkr-{真实模型ID}` 这层前缀。它以配置的 `access_keys.<key_id>` 为资源，管理走 `/api/access-keys` 系列：

- `providers` 是供应商 ID 清单，`models` 是模型名清单（真实模型 ID **或**别名，按调用方**写的原始名字**比对，发生在别名解析之前）。两份清单都是**省略 = 不限制**、**显式 `[]` = 一个都不许**，两者是不同的状态。
- 被停用的密钥回 `403`（`访问密钥已被停用: <name>`），与「凭据不认识」的 `401` 刻意区分开：停用是可恢复的，认错凭据不是。
- 明文只在新建与轮换的响应里出现一次，其余任何接口只回指纹。
- 访问密钥**不绑定工作空间**，用法与本地主凭据一致：`X-AMKR-Workspace` 照常生效。

推理 key 是「AMKR 作为多个项目共用网关」的凭据：一把 key 对应一个工作空间，被配进各个项目的环境变量。它不能用 `unified-model`（那是运维为整台实例挑的全局计划，不属于任何空间），也不能用别的空间的模型或任务名。

## 接口总览

| 方法 | 路径 | 鉴权 | 用途 |
| --- | --- | --- | --- |
| `HEAD` | `/` | 无 | 存活探针，返回 `204` |
| `GET` | `/health` | 无 | 服务、配置和端点能力状态 |
| `GET` | `/v1/models` | 本地或访问密钥 | 查询当前调用方可用模型 |
| `POST` | `/v1/chat/completions` | 本地或访问密钥 | OpenAI Chat Completions 兼容接口 |
| `POST` | `/v1/messages` | 本地或访问密钥 | Anthropic Messages 兼容接口 |
| `POST` | `/v1/messages/count_tokens` | 本地或访问密钥 | 本地估算 Anthropic 输入 token |
| `POST` | `/v1/responses` | 本地或访问密钥 | OpenAI Responses 兼容接口 |
| `POST` | `/v1/embeddings` | 本地或访问密钥 | OpenAI Embeddings 兼容接口 |
| 多种 | `/v1/{path}` | 本地或访问密钥 | 其他 OpenAI-compatible 接口透传 |
| `GET` | `/metrics` | 仅本地 | 查询 SQLite 聚合调用统计 |
| `GET` | `/metrics/requests` | 仅本地 | 分页查询持久化上游调用明细 |
| `GET` | `/metrics/series` | 仅本地 | 查询补零的持久化统计时间桶 |
| `GET/POST` | `/api/models` | 仅本地 | 查询或创建模型 |
| `GET/PUT/DELETE` | `/api/models/{model_id}` | 仅本地 | 查询、更新或删除模型 |
| `GET/POST` | `/api/models/{model_id}/keys` | 仅本地 | 查询或创建模型 Key |
| `GET/PUT/DELETE` | `/api/models/{model_id}/keys/{key_name}` | 仅本地 | 查询、更新或删除 Key |
| `GET/POST` | `/api/providers` | 仅本地 | 查询或创建 Provider |
| `GET/PUT/DELETE` | `/api/providers/{provider_id}` | 仅本地 | 查询、更新或删除 Provider |
| `GET/POST` | `/api/providers/{provider_id}/keys` | 仅本地 | 查询或创建 Provider Key |
| `GET/PUT/DELETE` | `/api/providers/{provider_id}/keys/{key_name}` | 仅本地 | 查询、更新或删除 Provider Key |
| `GET/PUT` | `/api/providers/{provider_id}/keys/{key_name}/models` | 仅本地 | 查询或设定该 Key 对外提供的模型（取消勾选只绑在它上面的最后一个模型会连带删除那个模型；被编辑的这把 Key 本身保留，即使它不再服务任何模型，见「消失：引用被一并清理」） |
| `POST` | `/api/providers/{provider_id}/probe` | 仅本地 | 同步刷新该 Provider 全部启用 Key 的能力探测（各 Key 的模型列表 + 路由可用性） |
| `POST` | `/api/providers/{provider_id}/keys/{key_name}/probe` | 仅本地 | 同步刷新指定 Key 的能力探测，可用 `modes` 限定路由检查范围 |
| `GET/POST` | `/api/routes` | 仅本地 | 查询或创建模型路由 |
| `GET/PUT/DELETE` | `/api/routes/{route_id}` | 仅本地 | 查询、更新或删除模型路由 |
| `GET/POST` | `/api/tasks` | 仅本地 | 查询或创建任务路由（`TASK_XXXXXX` → 模型 + 固定参数）；可用 `X-AMKR-Workspace` 头指定工作空间 |
| `GET/PUT/DELETE` | `/api/tasks/{task_name}` | 仅本地 | 查询、更新或删除任务路由；可用 `X-AMKR-Workspace` 头指定工作空间 |
| `GET/POST` | `/api/workspaces` | 仅本地 | 列出工作空间、各自任务数与可直呼模型清单；`POST` 显式创建（可指定面板 `api_key`，不传则生成；`inference_key` 总是生成；**两把 key 都仅本次响应返回**） |
| `PUT/DELETE` | `/api/workspaces/{workspace}` | 仅本地 | 重命名或删除工作空间（组内任务与两把 key 一并搬走/删除）；默认空间 `default` 不可改删 |
| `POST` | `/api/workspaces/{workspace}/inference-key` | 仅本地 | 轮换该空间的**推理 key**（不动面板 key）；新 key 仅在本次响应返回 |
| `PUT` | `/api/workspaces/{workspace}/models` | 仅本地 | 设定该空间**允许直呼**的模型清单（`null` 清除限制，`[]` 一个都不许） |
| `POST` | `/api/workspaces/export`、`/api/workspaces/import` | 仅本地 | 导出或导入工作空间**整包**（**带** `api_key`，不含 providers/models），与配置导入导出相互独立 |
| `GET/POST` | `/api/access-keys` | 仅本地 | 列出访问密钥（只给指纹）或新建一把（响应里含**明文 `key`**，仅此一次） |
| `PUT/DELETE` | `/api/access-keys/{key_id}` | 仅本地 | 改名、启停与两份清单（**不换 key**），或删除 |
| `POST` | `/api/access-keys/{key_id}/rotate` | 仅本地 | 换掉该访问密钥的明文（旧 key 立即失效）；新 key 仅在本次响应返回 |
| `GET/PUT` | `/api/cpa-instances` | 仅本地 | 列出或**整体替换** CPA 实例清单（配置键 `cpa_instances`；响应含管理密钥明文，故要求完整权限） |
| `GET` | `/api/cpa-accounts` | 仅本地 | 服务端扇出各 CPA 实例的账号与额度读数（只读、不缓存） |
| `GET` | `/api/cpa-reset-credits` | 仅本地 | 服务端扇出各 CPA 实例的 Claude / Codex 重置卡读数（只读、不缓存） |
| `POST` | `/api/cpa-reset-credits/use` | 仅本地 | 消费一张重置卡；默认选**最快过期**的可用卡（会真实改变上游账号状态） |
| `GET/PUT` | `/api/settings` | 仅本地 | 查询或更新监听、超时和重试设置 |
| `POST` | `/api/settings/local-api-key` | 仅本地 | 重置本地鉴权 Key；新 Key 仅在本次响应返回 |
| `POST` | `/api/update/check` | 仅本地 | 复用 CLI 的 GitHub Releases 版本检查 |
| `POST` | `/api/probes/keys` | 仅本地 | 异步探测指定 Provider 下 Key 的模型列表与各端点可用性（逐 Key 兼容接口） |
| `GET` | `/api/probes/{probe_id}` | 仅本地 | 查询探测进度和结果 |
| `POST` | `/api/probes/{probe_id}/cancel` | 仅本地 | 取消探测 |
| `POST` | `/api/config/export`、`/api/config/import` | 仅本地 | 导出或导入可迁移配置 |
| `GET` | `/ui/` | 无 | 内置 WebUI（需 `webui_enabled`，未启用或资产缺失时返回 `404`） |
| `GET` | `/ui/pricing.json` | 无 | models.dev 价格目录快照，用于 WebUI 估算成本（需 `webui_enabled`） |
| `GET` | `/ui/model-kinds.json` | 无 | 逐个模型回答「它是什么类型」（文本/图像/视频/语音/嵌入/重排），供 WebUI 把服务模型卡片分组（需 `webui_enabled`） |
| `GET` | `/ui/workspace-usage.json` | 仅本地 | 按工作空间拆分的用量读数与请求流向，供 WebUI 的「工作空间」页（需 `webui_enabled`） |
| `GET` | `/ui/access-key-usage.json` | 访问密钥 | 只回**这把**访问密钥自己的用量、按模型/供应商/上游模型的拆分与最近调用，供访客看板 `/ui/guest.html`（需 `webui_enabled`） |
| `GET` | `/ui/key-usage.json` | 仅本地 | 按**上游 Key**（供应商 + Key 名）、模型 × 上游 Key 与**访问密钥**拆分的用量，供 WebUI 的「用量统计」页（需 `webui_enabled`）；可用七个[筛选维度参数](#ui-读数的筛选维度参数)收窄（维度内 OR、维度间 AND） |
| `GET` | `/ui/filtered-metrics.json` | 仅本地 | **按一组维度筛选**的整页读数：概览（与 `/metrics` 同形）+ 趋势 + 明细，三份共用同一时间窗口（需 `webui_enabled`；七个[筛选维度参数](#ui-读数的筛选维度参数)） |
| `GET` | `/ui/status-monitor.json` | 仅本地 | **渠道健康矩阵**：概览（与 `/metrics` 同形）+ 趋势 + 按渠道维度 × 时间片分组的请求矩阵，三份共用同一时间窗口（需 `webui_enabled`；七个[筛选维度参数](#ui-读数的筛选维度参数)，`group_by` 选行维度） |
| `GET` | `/ui/metric-dimensions.json` | 仅本地 | **筛选器选项**：窗口内指标库里实际出现过的供应商、模型、请求模型名、上游 Key、访问密钥、工作空间与调用方档位及各自请求数（需 `webui_enabled`；**不**随筛选收窄） |
| `GET` | `/ui/usage-breakdown.json` | 仅本地 | **按一个维度拆分的趋势**：前 N 组各自的总量与逐桶序列 + 其余并成的 `other`，与 `/ui/filtered-metrics.json` 同一套筛选与同一张桶网格（需 `webui_enabled`；`group_by` 选拆分维度） |
| `GET` | `/ui/update/status` | 无 | 报告本构建是否具备自更新能力及当前版本，供 WebUI 决定是否显示「立即更新」 |
| `POST` | `/ui/update/apply` | 仅本地 | 执行自更新：下载并校验新版、就地替换、启动收尾助手重启服务 |
| `GET` | `/api/logs` | 仅本地 | 读取日志文件尾部（默认最后 64 KiB） |
| `GET` | `/api/tool` | 仅本地 | 查询版本、可用更新与 WebUI 状态 |
| `POST` | `/api/tool/webui` | 仅本地 | 启用或关闭 WebUI（写入配置，需重启服务生效） |
| `POST` | `/api/service/{action}` | 仅本地 | 执行服务的启动/停止/重启/注册/注销 |
| `GET` | `/api/integrations` | 仅本地 | 查询 Claude Code / Codex / Pi Agent 的集成状态 |
| `POST` | `/api/integrations/{agent}` | 仅本地 | 接管该 Agent 配置：写入路由、本地鉴权与 AMKR 当前可用的全部模型（含已配置的 `unified-model`） |
| `POST` | `/api/integrations/{agent}/rollback` | 仅本地 | 回退该 Agent 到备份配置 |

### 新增能力挂在 `/api/` 还是 `/ui/`

管理面的 47 条与运维面的 7 条路由列在 `routePatterns()` / `opsRoutePatterns()` 里，它们是
**已发布接口**的清单，不能随意增减：这些响应的形状已经对外承诺，改动会破坏既有调用方。
新增能力因此分两类：

- **管理面的正式资源**（工作空间自身的读/改/删与整包迁移、访问密钥、CPA 实例与账号资源）
  注册在 `/api` 之下，但列在**另一份**清单（`workspacePatterns()`）里。它们没有历史版本可
  对照，塞进那 47 条会让「这 47 条就是已发布行为」这句话失去意义。两批注册在同一棵 mux 上，
  因此错方法的 `405` / `Allow` 判定要同时看两份清单。
- **本项目自有的读数**（价格目录 `/ui/pricing.json`、模型类型 `/ui/model-kinds.json`、自更新入口、
  工作空间用量
  `/ui/workspace-usage.json`、访问密钥用量 `/ui/access-key-usage.json`、按 Key 用量
  `/ui/key-usage.json`、按一组维度筛选的整页读数 `/ui/filtered-metrics.json`、
  状态监控的渠道健康矩阵 `/ui/status-monitor.json`、筛选器选项 `/ui/metric-dimensions.json`、
  按维度拆分的趋势 `/ui/usage-breakdown.json`）挂在 `/ui/` 前缀下：它们与
  `/api` 面在语义上不连续，挂 `/ui/` 既落在那份已发布清单之外，也让「不开 WebUI 就没有这些读数」
  顺理成章。其中「按 Key 用量」与「按供应商 / 访问密钥筛选」尤其不能并进 `/metrics`——那份读数的
  **形状与查询参数都已发布**，而「按供应商 + Key 名」这个组合分组不是参照实现的口径，
  `provider_id` 的多值形态与 `access_key_id` 也不是它声明过的参数（见 `/metrics` 一节）。

判断标准是**它是不是管理面的正式资源**，而不是「能不能挂到 `/ui/` 躲开清单」。

工作空间对**既有**代理与管理路由的影响只体现为新增的 `X-AMKR-Workspace` 请求头：
不带该头的请求行为逐字节不变，因此 `tasks/list` 这类既有响应体不需要（也不允许）新增
`workspace` 字段。

与 `/ui/pricing.json` 的区别是**鉴权**：价格目录是公开只读数据，自更新会替换磁盘上的
可执行文件并重启服务，因此 `/ui/update/apply` 要求完整权限（访问密钥一律 401），
只有 `/ui/update/status` 不鉴权（它只回答「这个构建有没有自更新能力」）。
`/ui/workspace-usage.json` 与自更新同类，要求完整权限。

`/api/update/check` 保持不变，仍用于「只检查、不安装」。

## 代理接口通用参数

代理型 `/v1/{path}` 接口读取 JSON 请求体，并使用其中的 `model` 选择模型和上游 Key。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | string | 是（`/v1/decide`、`/v1/classify` 与视频族各路径除外） | 模型 ID、模型别名、`unified-model`、任务名（`TASK_XXXXXX`）或 `模型[Key名称]`。上游模型名（各 target 的 `upstream_model`）**不是**可调用名。表单请求（`audio/transcriptions` 等）从表单字段 `model` 取值 |
| `stream` | boolean | 否 | 为 `true` 时使用流式响应，并自动向上游补充 `stream_options.include_usage=true` |
| `stream_options` | object | 否 | 流式选项；服务会保留已有字段并强制加入 `include_usage=true` |
| `reasoning_effort` | string | 否 | 推理强度；模型配置中的非空值优先级更高 |
| `reasoning.effort` | string | 否 | Responses 风格推理强度，没有顶层覆盖时转成 `reasoning_effort` |

模型选择示例：

```json
{"model": "gpt-5.5"}
```

```json
{"model": "gpt"}
```

```json
{"model": "gpt-5.5[main]"}
```

```json
{"model": "unified-model"}
```

```json
{"model": "TASK_000001"}
```

配置文件中的字段名仍为 `unified_model`；请求中的虚拟模型 ID 为 `unified-model`。

聊天 SSE 的 HTTP `2xx` 只代表上游已接受请求，不保证生成完成。AMKR 会检查上游错误事件、
空流与未完整结束；响应头发出后不能改成 `502` 或重放已生成内容，改为发送对应协议的错误事件，
不会补造成功收尾。原生透传保留原始错误事件、不重复注入；转换路径返回固定安全错误，不回显上游敏感详情。
下游取消后停止重试与备选调用，且不惩罚上游 Key；
已经发生的调用仍可能在指标中记作失败，`status_code` 始终是上游状态码，不是最终客户端状态码。
Responses 的预算耗尽或内容过滤导致的受控 `response.incomplete` 保留未完成状态，但不惩罚 Key。
流式对话请求若收到显式 JSON 响应，会原样透传并检查错误/空输出，不混入 SSE；此兼容检查最多观察 1 MiB，
超限会保守记为失败但不惩罚 Key，音频等非对话二进制透传不受影响。

### 结构化决策端点：`model` 可省略

`/v1/decide` 与 `/v1/classify`（Laya / Jev 等结构化决策模型）的规范调用**不带 `model`**：用哪个模型由上游按 Key 决定。这两个端点上：

- 带了可用的 `model` → 按它路由，与其它端点一致；
- 没有 `model`（或为 `null` / 空串）→ 按名为 **`laya`** 的路由路由，因此配置里需要有一条叫 `laya` 的模型路由来指定走哪些 Key；
- 上游路径为同名的 `v1/decide` / `v1/classify`，且这类请求的**请求体逐字节原样转发**（不带 `model` 的体不参与 AMKR 的改写）；
- 没配 `laya` 路由时是 `404`「模型 laya 未配置」，不是 `400`「请求体中缺少 model 字段」。

详见 [`docs/USAGE.md`](USAGE.md#结构化决策端点v1decide-与-v1classify)。

### 任务路由

任务名（`TASK_XXXXXX`）是配置里定义的任务。请求它时，真实模型和采样参数都由 AMKR 侧决定：

```json
{"model": "TASK_000001", "messages": [{"role": "user", "content": "hi"}]}
```

- 传给上游的 `model` 会是任务的 `model`；若该模型重试后仍返回可重试状态码，会再尝试任务的 `fallback_model`，此时响应带 `X-AMKR-Fallback: true`。
- 任务 `params` 里固定了的采样参数（`temperature`、`top_p`、`top_k`、`frequency_penalty`、`presence_penalty`、`seed`、`stop`、`max_tokens`）会覆盖请求体中的同名参数。
- 请求里**显式传了**这些参数会直接被拒绝（`400`），而不是被静默覆盖 —— 静默覆盖会让调用方以为自己的值生效了。`reasoning_effort` 也一样会被拒绝：任务路由是给别的 AI 服务用的，不是给 Agent 用的，调用方显式传了任务已固定的参数就该收到明确的 `400`。
- 不在任务 `params` 里的参数照常透传。`max_tokens` 与 Anthropic 的 `stop` 一样有跨方言同义字段：任务固定了 `max_tokens` 时，Responses 方言的 `max_output_tokens` 同样视为冲突（`400`），不会绕过后被静默丢掉。
- 任务不接受调用方指定 Key（`TASK_000001[main]` 返回 `400`），Key 仍由目标模型自身的路由模式决定。
- 任务可以**尚未指定模型**（创建时不传 `model`，先把名字与固定参数定下来）：此时请求它会得到 `404` 与「任务 TASK_000001 尚未指定模型…」，而**不是**回落到别的模型。见「任务路由接口」里的 TaskCreate。
- 访问密钥不能访问任务名：任务自己固定的模型可能不在这把密钥的 `models` 清单里，绕过去等于清单失效。

#### 工作空间

任务名只在**工作空间**内唯一：不同工作空间可以有同名任务，各自指向不同的模型与参数。工作空间由请求头选择：

| 头 | 说明 |
| --- | --- |
| `X-AMKR-Workspace` | 工作空间名。缺省或为空串即默认工作空间（配置的顶层 `tasks` 段）；超过首尾空白的部分会被裁掉 |

```bash
# 命中 workspaces.teamA.tasks 里的 TASK_000001
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -H "X-AMKR-Workspace: teamA" \
  -d '{"model": "TASK_000001", "messages": [{"role": "user", "content": "hi"}]}'
```

- 该头**不会**转发给上游：它是 AMKR 自己的路由状态，上游既看不懂也不该看到，因此与 `Authorization`、`X-Api-Key`、`Host` 等同属转发前剔除的请求头。
- 未配置的工作空间名不是错误：任务查表落空后会按普通模型名继续解析，因此最终和「模型未配置」是同一个 `404`。
- 工作空间只隔离任务：模型的 ID、别名与 `unified-model` 仍然全局唯一，任务名也不能与它们撞名。
- 访问密钥不能使用任务（它根本不进任务路由），带上该头也一样。

工作空间本身的管理（列出/改名/删除）走 `/api/workspaces*`，见下方「任务路由接口」一节。

成功选中路由后，服务会把传给上游的 `model` 改为真实模型 ID，并用选中 Key 的密钥替换鉴权头。其他兼容参数通常会继续传给上游。

### Chat Completions

`POST /v1/chat/completions`

常用请求参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | string | 是 | 路由模型 |
| `messages` | array | 是 | Chat Completions 消息数组 |
| `stream` | boolean | 否 | 是否返回 SSE |
| `tools` | array | 否 | Function tools |
| `tool_choice` | string/object | 否 | 工具选择策略 |
| `max_tokens` | integer | 否 | 最大输出 token |
| `max_output_tokens` | integer | 否 | 当没有 `max_tokens` 时转换为 `max_tokens` |
| `stop` | string/array | 否 | 停止序列 |
| `stop_sequences` | array | 否 | 当没有 `stop` 时转换为 `stop` |

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.5",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### Anthropic Messages

`POST /v1/messages`

请求会优先以原生 Anthropic 格式发送到上游 `/v1/messages`，保留所有原生字段（包括 `cache_control`、`prompt_cache_key` 等）。如果上游不支持原生端点（返回 404/405/501），自动回退到转换为 `/v1/chat/completions` 格式。

**原生优先模式**（默认启用）：
- 首次请求时自动测试上游是否支持 `/v1/messages` 端点
- 测试结果按“上游 URL + 实际原生路径”缓存在端点能力缓存中，避免重复测试
- 支持原生端点时保留所有 Anthropic 原生字段，提高缓存命中率
- 可通过配置 `native_first: false` 禁用

如果上游的 Anthropic 入口需要额外路径前缀，可按上游 URL 配置 `upstream_routes[base_url].anthropic`，例如 `"anthropic": "anthropic/"` 会转发到 `base_url/anthropic/v1/messages`。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | string | 是 | 路由模型 |
| `messages` | array | 是 | 支持文本、`tool_use` 和 `tool_result` |
| `system` | string/array | 否 | 转换为首条 system 消息 |
| `max_tokens` | integer | 通常是 | 原样作为 Chat Completions 最大输出 token |
| `tools` | array | 否 | Anthropic tool 定义会转换为 function tool |
| `tool_choice` | object | 否 | 支持 `auto`、`none`、`any` 和指定 `tool` |
| `stop_sequences` | array | 否 | 转换为 `stop` |
| `stream` | boolean | 否 | 返回 Anthropic 风格 SSE |
| `prompt_cache_key` | string | 否 | 原生模式下保留，用于缓存路由 |
| `cache_control` | object | 否 | 原生模式下保留在 content block 中 |

以下字段仅在回退到 chat/completions 模式时移除：

`anthropic_version`、`metadata`、`reasoning`、`text`、`truncation`、`previous_response_id`、`include`、`store`、`safety_identifier`。

### Token 估算

`POST /v1/messages/count_tokens`

请求体与 Messages 接口类似，但不会请求上游。返回：

```json
{"input_tokens": 123}
```

该结果按请求内容的 UTF-8 JSON 字节长度估算，不等同于模型 tokenizer 的精确结果。

### Responses

`POST /v1/responses`

请求默认会探测上游 `/v1/responses`，不支持时回退到 `/v1/chat/completions` 并转换响应。配置 URL 级 `upstream_routes[base_url].responses` 后会改为对应原生 Responses 路径透传，不支持时仍会回退。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | string | 是 | 路由模型 |
| `input` | string/array | 是 | 文本、message、`function_call` 或 `function_call_output` |
| `instructions` | string/array | 否 | 转换为首条 system 消息 |
| `tools` | array | 否 | Function tools |
| `tool_choice` | string/object | 否 | Responses function 选择会转换为 Chat Completions 格式 |
| `max_output_tokens` | integer | 否 | 转换为 `max_tokens` |
| `reasoning.effort` | string | 否 | 转换为 `reasoning_effort` |
| `stream` | boolean | 否 | 返回 Responses 风格 SSE |

### 其他代理路径

`/v1/{path}` 支持 `GET`、`POST`、`PUT`、`PATCH`、`DELETE`。除上述特殊转换接口外，请求路径、方法、查询参数和响应主体会尽量保持上游兼容格式。

代理请求从 JSON 请求体（或表单字段，见下）里的 `model` 选择模型与上游 Key。缺少该字段会返回 `400`，只有三处例外：`/v1/decide`、`/v1/classify`（见上一节）与**视频族的读取端点**（`GET /v1/videos`、`GET /v1/videos/{id}`、`GET /v1/videos/{id}/content`——它们的规范调用没有请求体），后者按 `unified-model` 路由，因此会用到 `unified_model.video` 计划；未配置该计划时按 unified 的回落规则用 `unified_model.default` 的模型与 Key 发出去（上游会用自己的错误说明问题，比 AMKR 凭空报「请求体中缺少 model 字段」更好排查）。

#### 端点族与请求体改写

入站路径分成两类。**对话三方言**（`chat/completions`、`messages`、`responses`）的请求体是同一件事的三种写法，AMKR 会做方言转换；**其余端点族**的请求体是它们自己的规范形态，AMKR **只替换 `model`**，其余字节原样转发（遥测字段如 `metadata`、`store` 也不会被删掉）。

| 入站路径 | 上游路由模式 | 说明 |
| --- | --- | --- |
| `chat/completions` | `openai` | 对话补全，见「Chat Completions」 |
| `messages` | `anthropic` | Anthropic Messages，见该节 |
| `responses` | `responses` | Responses，见该节 |
| `embeddings` | `embeddings` | 文本嵌入。`input` 不会被改写成 chat 的 `messages`。别名 `embedding` / `embed` |
| `images/generations` | `images` | 图像生成 |
| `images/edits` | `images` | 图像编辑。**表单请求**默认原样转发；显式配置了 `images` 路由时用它，否则用字面 `v1/images/edits`（**不会**套用 `images` 的默认路径 `v1/images/generations`——那会把「编辑」发到「生成」端点） |
| `images/variations` | `images` | 图像变体，与 `images/edits` 同规则 |
| `audio/speech` | `speech` | 语音合成（TTS）。`input` 是待朗读文本，不会被改写 |
| `audio/transcriptions` | `transcriptions` | 语音转写（STT），**表单请求**（见下） |
| `audio/translations` | `translations` | 语音翻译，**表单请求**。与转写是两条上游路由，但共用 `unified_model.transcriptions` 计划 |
| `videos` 及其子路径 | `video` | 视频生成与任务读取。子路径（`/{id}`、`/{id}/content`、`/{id}/remix`）拼在配置路由之后：配了 `video: "gateway/video"` 时 `POST /v1/videos/vid_1/remix` 会转发到 `base_url/gateway/video/vid_1/remix` |
| `rerank` | `rerank` | 重排 |
| 其它任意路径 | 无（`v1/` + 原路径） | 例如 `decide` / `classify`，见「结构化决策端点」 |

上游路径默认取该族的默认值，可按上游 URL 配置 `upstream_routes[base_url].<模式>` 覆盖，例如 `"embeddings": "gateway/embed"` 会转发到 `base_url/gateway/embed/v1/embeddings`；`"video": "gateway/video"` 会把视频族（含子路径）整体移到该前缀下。别名（如 `tts` / `stt` / `whisper` / `videos` / `reranking`）在配置里会被规范化成上表的模式名。

请求 `unified-model` 时使用对应族的计划（`unified_model.embeddings`、`.speech`、`.video`…）；未配置该族计划则继承 `default.primary`。

#### 表单请求（multipart/form-data）

图像编辑、图像变体、语音转写、语音翻译的上游规范形态就是表单（文件 + 字段）。这几条路径上默认**原样转发**：AMKR 只从表单字段 `model` 取值用于路由，其余字节与 `Content-Type` 一并送出（文件内容对 AMKR 不透明，不做任何改写）。请求体上限 64 MiB，超过返回 `413`。

其它路径上的表单请求返回 `415`，而不是被悄悄改写成空 JSON——未知端点的表单没法保证改对，明确拒绝比「看起来转发成功、上游却收到空体」好得多。语音转写/翻译这类没有等价 JSON 形态的端点上，`415` 的文案会说明这是策略问题。

### WebSocket

可连接 `ws://HOST:PORT/v1/{path}`。服务读取客户端发送的第一帧，将其按对应路径的 HTTP `POST` 请求处理，把响应内容逐块发回，然后关闭连接。

## 基础接口

### `HEAD /`

无参数。成功时返回 `204 No Content`。

### `GET /health`

无参数、无需鉴权。主要响应字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `status` | string | 当前为 `ok` |
| `models` | array | 已配置的模型 ID 与别名（可调用名的全集） |
| `config_path` | string | 当前配置文件绝对路径；嵌入式应用可能为空 |
| `local_auth_enabled` | boolean | 是否设置本地鉴权 |
| `local_api_key_fingerprint` | string | 本地 key 的 SHA-256 前 12 位 |
| `unified_model` | object/null | 当前真实模型和可选固定 Key；含 `default` 与可选的 `image` / `embeddings` / `speech` / `transcriptions` / `video` / `rerank` 计划。**只列出已配置的计划**——没配的族不出现，界面据此能分清「这族没配」与「这族配了空值」 |
| `native_endpoint_states` | object | 上游原生端点能力缓存 |
| `ops_enabled` | boolean | 运维接口是否注册（见 `--no-ops` / `enable_ops`） |

Key 的失败次数和冷却属于内部调度细节，不通过 `/health` 或管理 API 暴露。长期启停 Key 请更新配置中的 `enabled`。

随访客模式整体移除，`/health` 不再有 `visitor_feature_installed`、`visitor_access_enabled` 与 `visitor_key_count` 三个字段（这是破坏性变更，按字段名取值的老调用方需要同步去掉）。访问密钥的数量与管理走 `/api/access-keys`，不属于无鉴权的存活探针该报的内容。

### `GET /v1/models`

无查询参数。响应采用 OpenAI 模型列表格式：

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-5.5",
      "object": "model",
      "owned_by": "auto-model-key-router"
    }
  ]
}
```

本地调用只返回当前有可用 Key 的真实模型、别名和已配置的 `unified-model`。

访问密钥返回的是**按它自己两份清单收窄后**的清单：先按 `providers` 清单排掉一个上游都用不了的模型，再按 `models` 清单排掉未授权的名字，排序后返回。它看不到 `unified-model`（访问密钥用不了它，列出来就是一个必然 `403` 的名字）。这份清单与代理面的判定同源——列了却调不动、或调得动却不在清单里，都会让接入方以为自己配错了。

工作空间推理 key 返回**第三份**清单：该空间配了 `models` 时就是它，没配时退化成该空间的**任务名**（那是它天然被授权调用的东西）。两份清单都必须与代理面的判定一致——列了却调不动、或调得动却不在清单里，都会让接入方以为自己配错了。`unified-model` 不出现在这份清单里（它是全局计划，作用域凭据用不了）。

上游模型名（target 的 `upstream_model`）不会出现在这里，也**不能**用来调用：它只是"发给那个上游的名字"，同一个模型在各上游叫法不同时各写各的，对外仍只有模型 ID 与别名。见 [`docs/USAGE.md`](USAGE.md) 的「对外名称与上游名称」。

任务名（`TASK_XXXXXX`）同样不出现在这里：它是一整组路由与参数的别名，而不是某个模型的名字。如果客户端需要从 `/v1/models` 里看到可调用的名字，请给模型加一个别名。

## 调用统计

### `GET /metrics`

仅接受本地完整权限，不接受访问密钥与工作空间凭据。

可选查询参数：

| 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `hours` | number | `24` | 仅聚合最近若干小时；必须大于 `0` 且不超过 `8760` |
| `all_history` | boolean | `false` | 为 `true` 时忽略 `hours` 并聚合全部历史 |

`hours` 的默认值有界，是因为全量聚合要扫描整张 `request_metrics` 表（16 万行实测约 3–5 秒），而统计查询与写入共用同一把锁，全量查询会把代理请求路径一起卡住。确需全量时显式传 `all_history=true`。

顶层响应字段：

| 字段 | 说明 |
| --- | --- |
| `count_semantics` | 固定为 `upstream_attempt`，表示请求数按上游调用尝试计数 |
| `window` | 本次聚合的 `from`、`to` 和 `hours`；全量查询的 `from` 为 `null` |
| `started_at` | 当前统计存储实例启动时间 |
| `database_path` | SQLite 文件路径 |
| `rate_window_seconds` | 当前 RPM/TPM 统计窗口秒数，默认 `60` |
| `current_rpm` | 当前窗口内请求数，即近 1 分钟 RPM |
| `current_tpm` | 当前窗口内 token 总数，即近 1 分钟 TPM |
| `total` | 全局累计统计 |
| `caller_types` | 按 `local`、`workspace`、`access_key` 拆分 |
| `models` | 按真实模型 ID 拆分 |
| `requested_models` | 按请求中的模型名或别名拆分 |
| `model_requested_models` | 真实模型到请求模型名的嵌套统计 |
| `keys` | 真实模型到 Key 名称的嵌套统计 |
| `providers` | 按请求发生时的供应商 ID 拆分 |
| `provider_pools` | 按供应商 + 模型池拆分的嵌套统计；v4 已删除模型池概念，该维度只含 v3 及更早写入的历史行（v4 新行无 pool 归因），新部署通常为空 |
| `upstream_models` | 按实际发送给上游的模型 ID 拆分 |
| `unattributed` | 缺少供应商、上游模型归因字段，或只有历史模型池归因的调用汇总 |

> `keys` 的 Key 只作为模型的子项出现，且不含供应商——因此它答不出「这一把 Key 一共出去了多少流量」，也分不清两家的同名 Key。要按 Key 看用量请用 [`GET /ui/key-usage.json`](#get-uikey-usagejson)；那个端点是本项目自有的读数，这里**不新增字段**（形状已发布）。

> 同理，`/metrics`、`/metrics/series`、`/metrics/requests` **没有 `access_key_id` 参数**，`provider_id` 也只在 `/metrics/requests` 那条上作为**单值**已发布参数存在：查询参数与响应形状一样属于已发布契约，多一个参数、或者把 `provider_id` 从等值改成 `IN`，都是改契约。按供应商 / 访问密钥筛选的读数在 [`GET /ui/filtered-metrics.json`](#get-uifiltered-metricsjson)（本项目自有的端点），`/ui/key-usage.json` 也接受同一对参数（两个都可重复出现）。给这三条路由传 `access_key_id` 会被当作未知参数**忽略**（FastAPI 不校验未声明的查询参数，Go 侧保持同一行为）。

每组统计包含：

```text
requests, successes, failures, retries
prompt_tokens, completion_tokens, total_tokens
cached_tokens, cache_creation_input_tokens, cache_read_input_tokens
cached_token_rate
total_duration_ms, avg_duration_ms, min_duration_ms, max_duration_ms
total_first_token_ms, avg_first_token_ms, min_first_token_ms, max_first_token_ms
status_codes
```

v4 起新写入的调用只按供应商与上游模型归因（模型池维度已随 v3 移除，pool 归因为空）。v3 及更早写入的历史行可能带有供应商/模型池归因，统一计入 `unattributed`；供应商、池或模型的实际 ID 即使为 `unknown`，也仍按字面 ID 查询和聚合，不会与未归因数据混淆。AMKR 不会根据当前配置反推旧数据，避免配置改名后改变历史含义。

### `GET /metrics/requests`

返回持久化的上游调用明细、所选范围汇总和相同筛选条件下的近 60 秒速率。仅接受本地完整权限。

查询参数：

| 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `hours` | number | `24` | 最近小时数，必须大于 `0` 且不超过 `720` |
| `all_history` | boolean | `false` | 为 `true` 时忽略 `hours` 并查询全部历史 |
| `caller_type` | string | 无 | `local`、`workspace` 或 `access_key` |
| `model_id` | string | 无 | 真实路由模型 ID |
| `requested_model_id` | string | 无 | 客户端请求中的模型名或别名 |
| `provider_id` | string | 无 | 请求发生时的供应商 ID |
| `pool_name` | string | 无 | 请求发生时的模型池名称（v3 历史数据筛选用；v4 新调用该字段为空） |
| `upstream_model_id` | string | 无 | 实际发送给上游的模型 ID |
| `key_name` | string | 无 | 路由使用的 Key 名称 |
| `status_code` | integer | 无 | `100..599` |
| `success` | boolean | 无 | 是否成功 |
| `attributed` | boolean | 无 | `true` 仅返回三个归因字段完整的调用；`false` 返回缺少任一字段的调用 |
| `limit` | integer | `50` | 每页 `1..200` 条 |
| `before_id` | integer | 无 | 仅返回 ID 小于该值的行，用于稳定加载下一页 |

响应示例：

```json
{
  "count_semantics": "upstream_attempt",
  "window": {
    "from": "2026-07-13T12:00:00+08:00",
    "to": "2026-07-14T12:00:00+08:00",
    "hours": 24
  },
  "filters": {
    "caller_type": "local"
  },
  "rate_window_seconds": 60,
  "current_rpm": 4,
  "current_tpm": 18200,
  "summary": {},
  "latest_request_at": "2026-07-14T11:59:52+08:00",
  "total_items": 1284,
  "items": [
    {
      "id": 1284,
      "created_at": "2026-07-14T11:59:52+08:00",
      "caller_type": "local",
      "model_id": "gpt-5.5",
      "requested_model_id": "default",
      "provider_id": "openai",
      "pool_name": null,
      "upstream_model_id": "gpt-5.5-2026-05-01",
      "key_name": "main",
      "status_code": 200,
      "success": true,
      "retried": false,
      "prompt_tokens": 1200,
      "uncached_prompt_tokens": 300,
      "completion_tokens": 80,
      "total_tokens": 1280,
      "cached_tokens": 900,
      "cache_creation_input_tokens": 0,
      "cache_read_input_tokens": 900,
      "first_token_ms": 420,
      "duration_ms": 3100,
      "workspace": "default",
      "client_addr": "127.0.0.1:50874",
      "user_agent": "claude-cli/1.0",
      "stream": false,
      "api_format": "chat/completions",
      "reasoning_effort": "high",
      "access_key_id": "trial",
      "access_key_name": "试用账号 A"
    }
  ],
  "next_before_id": 1235
}
```

`items` 末尾的 `workspace` / `client_addr` / `user_agent` 是**本项目增补的请求来源**（参照实现的
`_request_item` 没有这三项，既有字段一个未改）：`workspace` 取自 `request_workspace` 旁挂表，
后两项取自 `request_source` 旁挂表。没有来源记录的行（升级前的历史行、不走 HTTP 的写入路径）
渲染成 `null`，**不会**兜底成空串或默认地址。

`client_addr` 是入站请求的 `RemoteAddr`（`host:port`，IPv6 形如 `[::1]:50874`），与访问日志取的是
同一个值（见 `internal/server/accesslog.go`），**不读 `X-Forwarded-For`**：那个头由调用方自带、
可伪造，当真来源用会让看板显示一个攻击者选定的 IP。`user_agent` 允许为 `null`（客户端可以不带
这个头）。两者存放在旁挂表而不是 `request_metrics` 的新列，理由见
[`docs/WORKSPACE.md`](WORKSPACE.md) 的「存储：旁挂表而不是新列」。

再往后的 `stream` / `api_format` / `reasoning_effort` 是同样增补的**请求形态**（取自
`request_shape` 旁挂表）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `stream` | boolean \| null | 这次请求是否流式（请求体 `stream: true`）。没有形态记录时为 `null`，**不是** `false` —— 「不知道」与「非流式」是两件事 |
| `api_format` | string \| null | **入站路径**：`chat/completions`、`messages`、`responses`、`embeddings`、`images/generations`、`images/edits`。刻意不折算成方言名：同一条路径既可能原生透传、也可能被改写成 chat 形态，归一必然丢掉这层区别。未知路径原样记下 |
| `reasoning_effort` | string \| null | 这次请求**最终生效**的推理强度：模型级 `reasoning_effort` 覆盖载荷里已有的值，否则取载荷顶层 `reasoning_effort`，再否则取载荷 `reasoning.effort`。都没给时为 `null`（没有生效的强度，而不是某个默认档） |

Anthropic 的 `thinking`（`{type, budget_tokens}`）**不参与** `reasoning_effort`：AMKR 不改写也不
解释它，折算过去会造出一个上游并不认识的取值。

最后两项 `access_key_id` / `access_key_name` 是增补的**调用方身份**——「这次请求是谁发的」：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `access_key_id` | string \| null | 发起这次请求的那把访问密钥在配置里的 `key_id`（取自 `request_access_key` 旁挂表）。完整权限、工作空间推理凭据写入的行以及升级前的历史行都是 `null`：它们不属于任何一把 key，**不会**兜底成某一把——那等于把别人的用量记到这个人头上 |
| `access_key_name` | string \| null | 那把 key 在配置里的显示名，由**服务端**补上（指标库只存 `key_id`，显示名可改、属于配置）。配置里已经没有这把 key 时为 `null`，读端应回落到 `access_key_id` 显示：把 id 当名字回填会让「这把 key 已经被删掉」看不出来 |

`access_key_id` 与同一条目里的 `key_name` 是两件不同的事：前者是**调用方**身份，后者是这次
请求**打到上游用的那把 Provider Key** 的名字，两者互不嵌套（一把访问密钥的流量会打到多把
上游 Key 上，反之亦然）。WebUI 的概览请求流用这两项显示「谁在用这个实例」。

当 `next_before_id` 为 `null` 时没有下一页。刷新第一页时不要携带 `before_id`。

### `GET /metrics/series`

返回 SQLite 历史数据生成的非重叠时间桶，用于趋势图。服务端按北京时间自然边界对齐起点并补齐没有调用的空桶，因此响应 `window.from` 可能略早于精确的 `hours` 起点；最后一个桶可能尚未结束。仅接受本地完整权限。

查询参数：

| 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `hours` | number | `1` | 最近小时数，必须大于 `0` 且不超过 `8760`（1 年） |
| `bucket_seconds` | integer | `60` | 桶宽 `15..86400` 秒；一次查询最多返回 `500` 个桶 |
| 其他筛选参数 | - | 无 | 与 `/metrics/requests` 的来源、模型、供应商、Key、状态、成功和归因筛选一致 |

`hours` 的上界是 **8760**（1 年），比 `/metrics`、`/metrics/requests` 的 720 宽：长窗口只
能配粗桶，点数上限（500）才是真正的约束——`hours=8760&bucket_seconds=86400` 是 366 个点、合法，
而 `hours=8760&bucket_seconds=60` 会因超过 500 点被拒（422）。

响应示例：

```json
{
  "count_semantics": "upstream_attempt",
  "window": {
    "from": "2026-07-14T11:00:00+08:00",
    "to": "2026-07-14T12:00:00+08:00",
    "hours": 1
  },
  "filters": {},
  "bucket_seconds": 60,
  "points": [
    {
      "started_at": "2026-07-14T11:00:00+08:00",
      "ended_at": "2026-07-14T11:01:00+08:00",
      "complete": true,
      "requests": 3,
      "successes": 3,
      "failures": 0,
      "retries": 0,
      "prompt_tokens": 12000,
      "completion_tokens": 900,
      "total_tokens": 12900,
      "cached_tokens": 8600,
      "cached_token_rate": 0.716667,
      "avg_duration_ms": 2400,
      "avg_first_token_ms": 380,
      "status_codes": {
        "200": 3
      }
    }
  ]
}
```

每个统计行代表一次上游调用尝试。发生自动重试时，同一个客户端请求会产生多行；在没有持久化请求关联 ID 前，API 不会把这些行错误合并成一个请求。

## 模型与 Key 管理 API

所有管理接口只接受本地完整权限。写操作会原子更新当前配置文件，并在完成后热重载运行时配置。资源读取响应包含 `config_revision`；写请求可携带读取到的 `config_revision`，版本冲突会在 mutation 前返回 `409`，避免覆盖其他端的修改。

查询响应不会返回上游 `api_key` 明文，只返回 SHA-256 前 12 位的 `api_key_fingerprint`。

### 改模型会连带改掉别处的引用

模型不是孤立资源：`tasks.*.model` / `fallback_model`、`workspaces.<空间>.models`、
`access_keys.<id>.models`、`access_keys.<id>.providers`、`unified_model.*` 里都可能写着它的
名字。这些引用有两种命运，取决于被引用的目标**没了**还是**改了名**。

#### 改名：引用跟着一起改

模型或供应商改名时（`PUT /api/models/{model_id}`、`PUT /api/routes/{route_id}` 的 `id`，
以及 `PUT /api/providers/{provider_id}` 的 `id`），上表里的引用会**跟着改成新名字**：

- 模型改名：`tasks.*.model` / `fallback_model`、`workspaces.<空间>.models`、
  `access_keys.<id>.models`、`unified_model` 与 `models.*.endpoints.*` 里指向它的名字一起改。
- 供应商改名：模型 `targets[].provider` 与 `access_keys.<id>.providers` 一起改。

跟的是**与旧 ID 完全相同**的那些名字。清单与任务里允许写别名（调用方就是用那些名字请求
的），而别名不随 ID 改名失效，跟着改反而会把一个仍然有效的别名改没。

刻意不是「顺手把旧名字摘掉」：模型只是换了个名字，引用它的那把访问密钥并没有失去权限。
摘掉等于把一次改名变成一次静默减权，而留着旧名字会让下一次保存被校验拒绝——两种都不对。

#### 消失：引用被一并清理

删掉一个模型（或让某个模型**失去全部 `targets`**，例如取消勾选 Key 的最后一个服务模型、
删掉那把 Key、删掉那个供应商）时，这些引用会被**一并自动清理**，而不是让整次保存失败：

| 引用 | 清理方式 |
| --- | --- |
| `tasks.<任务>.model` / `fallback_model` | 引用失效的任务被删除 |
| `workspaces.<空间>.models` | 摘掉失效的名字 |
| `access_keys.<id>.models` | 摘掉失效的名字 |
| `access_keys.<id>.providers` | 删除供应商时摘掉失效的名字 |
| `unified_model.<计划>.<primary\|fallback>` | 指向被删模型的计划被改写（回落另一个模型）或移除 |
| `models.<模型>.endpoints.<族>.<primary\|fallback>` | 指向被删模型的端点计划被改写（回落另一个模型）或移除 |
| `providers.<供应商>.keys.<Key>` | 该模型 `targets[]` 引用过、且此后**不再被任何模型绑定**的 Key 被删除；仍被其他模型路由绑定的 Key 原样保留 |

删除模型时会顺带清理供应商 Key 的只有这几条路径：`DELETE /api/models/{model_id}`、
`DELETE /api/routes/{route_id}` 与 `PUT /api/routes/{route_id}` 传 `targets: []`（以及「按下标解绑
最后一条 target」）。它们删掉的是该模型 `targets[]` 引用过、此后不再被任何模型绑定的 Key。
明确**不做**这件事的有两条：其一，供应商页「Key 服务模型」取消勾选
（`PUT /api/providers/{provider_id}/keys/{key_name}/models`）——那条路径被编辑的主体就是这把 Key
自己，它必须留下；其二，只改一条路由 `targets[]` 里的部分条目（路由仍在，同一条路由换成少了某条
目标的清单、或按下标解绑其中一条）——那条路径只改绑定关系，即使某把 Key 就此失去最后一个绑定也
不动它：编辑一次目标清单就删掉一把手输的凭据，代价比配置里多一把没人引用的 Key 大得多，要删
凭据请显式删（`DELETE /api/providers/{provider_id}/keys/{key_name}`）。`DELETE
/api/models/{model_id}/keys/{key_name}` 是另一条语义独立的路径——它本身就是「解绑这把 Key」，
清理规则见下文该端点。供应商**本身不删**——哪怕它一个 Key 都不剩。

指向被删模型的**端点计划**与 `unified_model` 是同一套修复：被删模型不再是那个端点族的分派
目标，那条计划要么改指回退模型，要么整条移除；移除后该族回落到模型自己的 `targets`。
`?dry_run=1` 的结果里这份变动记为 `endpoint_plans: true`。

清单被摘空后**保留空数组**而不是删掉字段：这两份清单是三态的，字段缺失或 `null` 表示
「不限制」，`[]` 表示「一个都不许」。删字段等于把禁令松开，那是扩权。因此一把访问密钥的
模型清单可能被摘成 `[]`——它此后调不动任何模型，需要重新授权。

#### 预演：`?dry_run=1`

上表里的连带变动不该等落盘之后才发现，因此**会让模型或供应商消失的写接口**都支持
`?dry_run=1`：

| 方法 | 路径 |
| --- | --- |
| `PUT` | `/api/providers/{provider_id}/keys/{key_name}/models` |
| `DELETE` | `/api/providers/{provider_id}/keys/{key_name}` |
| `DELETE` | `/api/providers/{provider_id}` |
| `PUT` | `/api/routes/{route_id}`（仅 `targets: []` 走预演，即删路由；传非空 `targets` 是**立即写入**，`?dry_run=1` 在这个分支上不生效） |
| `DELETE` | `/api/routes/{route_id}` |
| `DELETE` | `/api/models/{model_id}` |
| `DELETE` | `/api/models/{model_id}/keys/{key_name}` |

带该参数时接口跑一遍**完全相同**的改动、回报它连带改掉哪些引用，但**一个字节都不落盘**，
状态码一律 `200`（原本回 `204` 的删除接口在预演时回响应体）。请求体与真写完全一致，差别
只有这一个查询参数。

```json
{
  "dry_run": true,
  "removed_models": ["gemini-3-flash"],
  "access_keys": [
    {"id": "public", "name": "公开", "models": ["gemini-3-flash"], "providers": [],
     "models_cleared": false, "providers_cleared": false}
  ],
  "workspaces": [
    {"id": "teamA", "name": "teamA", "models": ["gemini-3-flash"], "providers": [],
     "models_cleared": false, "providers_cleared": false}
  ],
  "removed_tasks": ["shared", "teamA/only-a"],
  "unified_model": true,
  "endpoint_plans": true,
  "removed_keys": [{"provider": "openai", "key": "backup"}],
  "config_revision": "..."
}
```

`removed_models` 是失去全部目标而被删除的模型；`models_cleared` / `providers_cleared` 报告
那份清单是否被摘空。命名工作空间的任务用 `空间/任务名` 限定，默认空间按原名。

`removed_keys` 是**追加在响应末尾**的字段（为兼容既有调用方而排在最后）：本次写入里**消失、
且其供应商仍然存在**的供应商 Key，形如 `[{"provider": "openai", "key": "backup"}]`，按
`provider`、`key` 升序。删路由时它列出的就是「失去最后一个模型绑定」而被清理的那些 Key——
仍被其他模型路由绑定的 Key 不在其中；整个供应商被删除时不逐把罗列（那种情况已经体现在
`access_keys` 的供应商清单里）。

预演不改动配置，因此响应里的 `config_revision` 仍是**当前**版本号，真写照常用它做并发校验。
预演本身**不比对**该版本号：它不写盘，用不上防丢改动；并发写只会让预演结果过期（确认框里
多列或少列一条），不会让配置受损。

### 数据结构

#### ModelCreate

| 字段 | 类型 | 必填 | 默认值/约束 |
| --- | --- | --- | --- |
| `id` | string | 是 | 非空；不能与其他 ID 或别名重复 |
| `aliases` | string[] | 否 | `[]`；额外的可调用名，会出现在 `/v1/models`；所有模型名称必须全局唯一 |
| `routing_mode` | string | 否 | `round_robin`；可选 `round_robin`、`priority`、`only_first` |
| `reasoning_effort` | string/null | 否 | 可选 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max` |
| `keys` | KeyCreate[] | 否 | `[]`；兼容写法：每个 Key 会在其 `base_url` 对应的供应商下创建（不存在则自动建供应商）并绑定为模型的 target。也可先创建无 Key 的模型，再通过模型 Key 接口或 `/api/routes` 补充绑定 |

#### ModelUpdate

字段与 ModelCreate 的模型字段相同，全部可省略，但请求中至少需要出现一个字段。`id`、`aliases`、`routing_mode` 不能为 `null`（`aliases: []` 表示清空别名）；`reasoning_effort: null` 用于清除模型级覆盖；`endpoints` 省略表示不改动这张端点计划表，`null` 或 `{}` 表示清空（见「模型路由接口」）。不能通过该接口更新 `keys` 或 `targets`（使用模型 Key 接口或 `/api/routes`）。改 `id` 即改名，引用这个模型的名字会跟着改（见「改模型会连带改掉别处的引用」）。

#### KeyCreate

| 字段 | 类型 | 必填 | 默认值/约束 |
| --- | --- | --- | --- |
| `name` | string | 是 | 非空；同一供应商内唯一（创建时若供应商已存在同名 Key 返回 `409`） |
| `api_key` | string | 是 | 非空 |
| `base_url` | string/null | 否 | 决定 Key 落在哪个供应商：匹配已存在供应商的 `base_url`，否则自动创建供应商；缺省用配置的 `default_base_url`，否则为 `https://api.openai.com` |
| `enabled` | boolean | 否 | `true` |
| `upstream_routes` | object/null | 否 | 兼容字段；会写入该 Key 的 `base_url` 对应的 URL 级路由，而不是保存到 Key 上 |

`base_url` 最终必须以 `http://` 或 `https://` 开头。KeyCreate/KeyUpdate 中的 `upstream_routes` 仅用于兼容旧客户端；值只能是相对路径或路径前缀，例如 `{"anthropic": "anthropic/"}` 会规范化为 URL 级配置 `upstream_routes[base_url].anthropic = "anthropic/v1/messages"`。

> v4 中 Key 存储在 `providers.<id>.keys` 下，模型通过 `targets[]` 引用 `{provider, key, upstream_model}`。本组「模型 Key 接口」与 KeyCreate/KeyUpdate 是面向模型的操作：`POST /api/models/{model_id}/keys` 会在供应商下创建（或定位）Key 并把它绑定为该模型的一个 target；`PUT/DELETE` 只影响当前模型的绑定（详见下文）。如需直接管理供应商 Key（改名、启停、删除），使用 `/api/providers/{provider_id}/keys` 系列接口。

#### KeyUpdate

字段与 KeyCreate 相同，全部可省略，但请求中至少需要出现一个字段。省略 `api_key` 会保留原密钥；`name`、`api_key`、`enabled` 不能为 `null`。`base_url: null` 会恢复为配置的默认上游地址；`upstream_routes: null` 或 `{}` 会清空自定义路由。

#### ModelResponse

```json
{
  "id": "gpt-5.5",
  "aliases": ["gpt"],
  "routing_mode": "round_robin",
  "reasoning_effort": "medium",
  "keys": [
    {
      "name": "main",
      "base_url": "https://api.openai.com",
      "enabled": true,
      "api_key_fingerprint": "0123456789ab"
    }
  ]
}
```

`keys` 是该模型当前绑定的供应商 Key 展开结果（来自 `models.<id>.targets[]` 与 `providers.*.keys`）。同一供应商 Key 同时被多个模型绑定时，模型内的 `name` 可能与供应商 Key 名不同（自动加 `供应商ID-` 前缀去重），`base_url` 为该 Key 所属供应商的地址。

#### KeyResponse

```json
{
  "name": "main",
  "base_url": "https://api.openai.com",
  "enabled": true,
  "max_concurrency": 4,
  "api_key_fingerprint": "0123456789ab"
}
```

模型 Key 接口与 `/api/providers/{provider_id}/keys` 系列均返回此结构；provider Key 响应额外在顶层带 `config_revision`。`max_concurrency` 是这**把上游 Key** 同时在途请求的上限（正整数；`null` 表示不限制），两处都会返回，但只有 `/api/providers/{provider_id}/keys` 系列接口能写它；同一把 Key 被多个模型引用时共用一个计数，写入约束见「供应商接口与能力探测」。

#### RouteTarget

`/api/routes` 的 target 对象（即磁盘格式 `models.<id>.targets[]` 的元素）：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `provider` | string | 是 | 供应商 ID，必须已存在 |
| `key` | string | 是 | 该供应商下已声明的 Key 名称 |
| `upstream_model` | string | 是 | 发送给上游的真实模型名（创建模型 Key 等交互流程默认填本地模型 ID）。它只是"发给这个上游的名字"，**不会**变成可调用名：同一个模型在各上游叫法不同时，就在这里各写各的 |

示例：

```json
[
  {"provider": "openai", "key": "main", "upstream_model": "gpt-5.5"},
  {"provider": "openai", "key": "backup", "upstream_model": "gpt-5.5"}
]
```

#### ProviderResponse

`GET /api/providers`、`GET/PUT /api/providers/{provider_id}` 等接口返回的 provider 对象：

```json
{
  "id": "openai",
  "base_url": "https://api.openai.com",
  "max_concurrency": 10,
  "keys": [
    {
      "name": "main",
      "enabled": true,
      "api_key_fingerprint": "0123456789ab",
      "max_concurrency": 4,
      "capabilities": {
        "models": ["gpt-5.5"],
        "route_status": {"openai": "ok", "anthropic": "ok", "responses": "ok"},
        "errors": {},
        "checked_at": "2026-09-01T00:00:00+00:00"
      }
    }
  ],
  "routes": {"openai": "v1/chat/completions", "responses": "v1/responses"}
}
```

provider 对象不再含顶层 `capabilities`；探测缓存按 Key 存于 `keys[]` 中每个元素的 `capabilities`（该 Key 通过 `GET /v1/models` 看到的可服务模型清单与 openai/anthropic/responses 路由的可用性，`errors` / `checked_at` 记录探测错误与时间），未探测时为 `null`。同一供应商的不同 Key 能访问的模型集可能不同（如免费/付费额度、不同订阅），因此各 Key 独立探测、独立缓存，互不复用。v3 的 `pools` 字段已移除。

`max_concurrency` 在 provider 与每个 Key 上都**总是出现**（整数或 `null`）：provider 上的值统计该供应商**所有 Key、所有模型**上同时在途的上游请求数，Key 上的值统计该把上游 Key 同时在途的请求数。写入语义与两级上限之间的约束见「供应商接口与能力探测」。

#### TaskCreate

| 字段 | 类型 | 必填 | 默认值/约束 |
| --- | --- | --- | --- |
| `name` | string | 是 | 非空；即客户端传的 `model`。不能与已有任务名、任何模型 ID、`aliases` 或 `unified-model` 重复 |
| `model` | string/null | 否 | 首选模型，可写模型 ID 或别名；写回时规范化为模型 ID。**省略或传 `null` 表示尚未指定模型**，任务先作为占位存在（见下） |
| `display_name` | string/null | 否 | 给**人**看的中文显示名，只用于 WebUI 辨认任务；不影响调用，两端空白会被去掉 |
| `fallback_model` | string/null | 否 | `null`；备选模型，与首选引用同一模型时忽略。**没有首选时不能填备选**，否则 `422` |
| `params` | object | 否 | `{}`；固定采样参数，见下节。留空表示全部透传 |
| `config_revision` | string | 是 | 并发校验版本号 |

`model` 是否必填是本项目相对参照实现的一处**有意放宽**（见 CHANGELOG）：任务可以先建出来占位（例如先把名字与固定参数定下来，模型稍后再选）。这种任务被请求时会得到一个明确的 `404`：

```json
{"detail": "任务 TASK_000001 尚未指定模型；请先在 AMKR 的任务路由中为该任务选择模型"}
```

刻意**不做任何回落**——既不会退到 `unified_model.default`，也不会退到第一个已配置的模型。静默回落会让一个忘记选模型的任务照常服务，调用方既看不到问题、也无从知道自己实际用的是另一个模型。注意「填了但填错」仍是错误：`model` 引用了未配置的模型照旧报错，否则错字会静默退化成一个空任务。

`params` 只接受白名单内的键，写错键名返回 `422`（不会静默忽略）：

| 键 | 类型 | 约束 |
| --- | --- | --- |
| `temperature`、`top_p`、`frequency_penalty`、`presence_penalty` | number | 任意数值 |
| `top_k`、`seed`、`max_tokens` | integer | 必须是整数，`1.5` 这类值返回 `422` |
| `stop` | string[] | 非空字符串数组；传字符串返回 `422`（配置文件路径会包成单元素数组，管理 API 不会） |
| `reasoning_effort` | string | `none`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max`；传 `""`、`default`、`downstream` 视为不固定 |

#### TaskUpdate

字段与 TaskCreate 的模型字段相同（`model`、`display_name`、`fallback_model`、`params`），全部可省略，但请求中至少需要出现一个，否则返回 `422`。`model: null` 把任务退回「尚未指定模型」的占位状态（备选会一并清掉），`display_name: null` 清除显示名，`fallback_model: null` 清除备选，`params: {}` 清空全部固定参数。`name` 不可改 —— 它是调用方使用的 `model` 名，改名等于换了个任务。

#### TaskResponse

```json
{
  "name": "TASK_000001",
  "display_name": "长文摘要",
  "model": "gpt-4o-mini",
  "fallback_model": "claude-sonnet",
  "params": {"temperature": 0.2, "reasoning_effort": "high"}
}
```

`display_name` **只在任务取了名时才出现**：没有显示名的任务响应与新增该字段之前逐字节相同，既有调用方看不到任何变化。未指定模型的任务回 `"model": ""`。

### 模型接口

#### `GET /api/models`

返回：

```json
{"models": [ModelResponse]}
```

#### `POST /api/models`

请求体为 ModelCreate，成功返回 `201` 和 ModelResponse。

```bash
curl -X POST http://127.0.0.1:8000/api/models \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "gpt-5.5",
    "aliases": ["gpt"],
    "routing_mode": "round_robin",
    "keys": [{
      "name": "main",
      "api_key": "sk-upstream",
      "base_url": "https://api.openai.com"
    }]
  }'
```

上例的 `keys` 会在供应商 `openai`（按 `base_url` 匹配或自动创建）下写入 `main`，并把它绑定为该模型的一个 target。

#### `GET /api/models/{model_id}`

路径参数 `model_id` 为真实模型 ID。成功返回 ModelResponse，并在顶层附带当前 `config_revision`。

#### `PUT /api/models/{model_id}`

请求体为 ModelUpdate，成功返回更新后的 ModelResponse。

#### `DELETE /api/models/{model_id}`

成功返回 `204 No Content`；请求体可带 `config_revision` 进行并发校验。删除模型本身后，它 `targets[]` 引用过的供应商 Key 中，**不再被任何模型绑定**的会被一并删除；仍被其他模型路由绑定的 Key 原样保留。供应商本身不删——即使它一个 Key 都不剩。引用该模型的任务、工作空间与访问密钥清单会被一并清理（见「改模型会连带改掉别处的引用」）；带 `?dry_run=1` 可先拿到这份清理清单（含 `removed_keys`）而不落盘。

### Key 接口

Key 接口操作的是「模型绑定的 Key」（v4 中即该模型 `targets[]` 对应的供应商 Key）。路径参数 `model_id` 为真实模型 ID；`key_name` 是该模型内的 Key 名称（解析时兼容供应商原始 Key 名与 `供应商ID-Key名` 限定名）。这些操作与 WebUI 模型路由页的 Key 管理等价：供应商 Key 被其他模型绑定时，写操作会先把该模型解耦到独立的供应商 Key 克隆，再应用修改，避免影响其他模型。

#### `GET /api/models/{model_id}/keys`

返回：

```json
{"keys": [KeyResponse]}
```

#### `POST /api/models/{model_id}/keys`

请求体为 KeyCreate，成功返回 `201` 和 KeyResponse。该 Key 会写入对应供应商（`base_url` 匹配或自动创建），并为当前模型追加一条 `target` 绑定。

#### `GET /api/models/{model_id}/keys/{key_name}`

成功返回 KeyResponse。

#### `PUT /api/models/{model_id}/keys/{key_name}`

请求体为 KeyUpdate，成功返回更新后的 KeyResponse。该 Key 同时被其他模型绑定时，会先为当前模型克隆一个独立的供应商 Key 再应用修改，因此不会影响其他模型对该供应商 Key 的使用。

```bash
curl -X PUT http://127.0.0.1:8000/api/models/gpt-5.5/keys/main \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{"enabled": false}'
```

#### `DELETE /api/models/{model_id}/keys/{key_name}`

成功返回 `204 No Content`。该接口与 WebUI 模型路由页的 Key 操作等价：解绑当前模型的这条 Key 绑定。若该 Key 不再被任何模型绑定，会连带删除供应商下的这个 Key；供应商随后没有 Key 时也会一并删除。若这是模型的最后一条绑定，模型会被自动删除，引用它的任务、工作空间与访问密钥清单同样会被清理（见「改模型会连带改掉别处的引用」）；带 `?dry_run=1` 可先拿到这份清理清单而不落盘。

### 任务路由接口

任务路由把「模型 + 固定采样参数」打包成一个可直接当 `model` 传的名字。任务名不能与模型 ID、别名或 `unified-model` 撞名（否则路由语义会取决于查表顺序），也不能指定 Key。

这五个端点都认 `X-AMKR-Workspace` 头，语义与代理面完全一致：缺省即默认工作空间（顶层 `tasks`），带 `X-AMKR-Workspace: teamA` 即读写 `workspaces.teamA.tasks`。任务名只在工作空间内唯一，因此**同一空间内**重名报 `409`、跨空间同名合法；`GET/PUT/DELETE /api/tasks/{task_name}` 取的是该空间里的那个任务，别的空间的同名任务不会被误改。工作空间名不需要事先声明：在它下面建第一个任务即存在，删掉最后一个任务即消失。

响应体没有 `workspace` 字段（`tasks/list` 的字节形状已对外承诺），当前空间由请求头决定。`display_name` 是唯一新增的字段，且只在任务取了名时才出现——没有显示名的任务响应与新增该字段之前逐字节相同。

#### `GET /api/tasks`

返回 `{"tasks": [TaskResponse]}`（只含该空间的任务），另含 `config_revision`。

#### `POST /api/tasks`

请求体为 TaskCreate，成功返回 `201` 与 TaskResponse（含 `config_revision`）。该空间已有同名任务时返回 `409`（`{"detail": "任务已存在: <name>"}`）。

`model` 可省略：不传即创建「尚未指定模型」的占位任务，之后再 `PUT` 补上即可（见 TaskCreate 一节）。

#### `GET/PUT/DELETE /api/tasks/{task_name}`

查询、更新或删除单个任务。`PUT` 请求体为 TaskUpdate + `config_revision`，成功返回 `200` 与更新后的 TaskResponse；`GET` 返回 TaskResponse；`DELETE` 请求体只需 `config_revision`，成功返回 `204`。该空间里任务不存在时返回 `404`（`{"detail": "任务不存在: <name>"}`）——**不会**回落到别的空间的同名任务。

任务随 `/api/config/export`、`/api/config/import` 一同迁移（命名工作空间也在其中，但那条通道的导出会**剥掉** `api_key`）；导入时引用不到模型的任务会被跳过（与「该模型从未配置」一致）。删除模型时会一并清理引用它的任务：首选模型没了则删除整个任务，只有备选没了则退化为单模型任务。

工作空间**自己**的整包迁移是另一组端点（`/api/workspaces/export|import`），那条通道**带** `api_key` 且不含 providers/models，见下。

#### `GET /api/workspaces`

列出工作空间及各自任务数，供 WebUI 填充切换下拉：

```json
{"workspaces": [
  {"name": "default", "task_count": 2, "has_inference_key": false},
  {"name": "teamA", "task_count": 1, "has_inference_key": true, "models": ["gpt-4o-mini"]}
]}
```

默认工作空间 `default` 固定排在首位。没有任务的工作空间通常不会出现——空间由「在它里面
建任务」隐式产生（删空的也会从配置里消失）；**例外**是带 `api_key`、`inference_key` 或
`models` 的空间，即使没有任务也会列出，因为这三者都是任务之外的可观测内容（见下）。

`models` 字段只在配置里**显式写了**这个清单时才出现：省略表示「不限制」，空数组表示
「一个都不许直呼」。两者是有区别的状态，因此不能都渲染成空数组。

`has_inference_key` 只报告「这个空间发过推理凭据」，让界面能提示可以轮换。

这个目录是必要的：`GET /api/tasks` 是**按空间过滤**的，因此从它推不出「还有哪些空间存在」。

**响应里没有任何 key**：目录会被列表页反复轮询，把凭据挂在上面等于每次刷新都重新分发
一遍。key 明文只出现在 `POST /api/workspaces` 的 201 响应与配置文件里。

#### `POST /api/workspaces`

显式创建一个工作空间，供应用侧「先建空间拿到凭据、之后才陆续填任务」。

请求体为 WorkspaceCreate（`config_revision` 与 `name` 必填，`api_key` 可选），成功返回
`201`：

```json
{"name": "teamA", "task_count": 0, "api_key": "amkr_ws_…",
 "inference_key": "amkr_ik_…", "config_revision": "…"}
```

- `api_key` 不传就由服务端生成：`amkr_ws_` + 43 位 base64url（共 50 字符）。传了就用
  调用方给的（应用侧通常已有既定凭据）。这是**面板 key**，用于嵌入工作空间面板。
- `inference_key` 由服务端生成：`amkr_ik_` + 43 位 base64url（共 50 字符）。这是**推理
  key**，用于调用 `/v1`；空间由它决定，调用方无法用 `X-AMKR-Workspace` 换空间。
- **两把 key 都只在这条响应里返回**，之后任何接口都不再给出明文（配置导出也会剥掉它们，
  见下）。请在这一步保存。
- `409`：名字已被占用（`{"detail": "工作空间已存在: <name>"}`），或名字是 `default`
  （`{"detail": "工作空间名重复: default"}`）。
- `422`：名字为空，或某把 key 撞上本地 `local_api_key` / 别的空间的**任何一把** key
  （跨类型也算撞车）/ 任意一把**访问密钥**。冲突时**报错**而不是取第一个：同一个字符串
  在两处都命中会让权限边界取决于先命中哪张清单。

#### `PUT /api/workspaces/{workspace}`

把工作空间改名为请求体里的 `name`，组内任务与两把 key（若有）整体跟着搬到新键下。请求体为 `{"config_revision": "...", "name": "..."}`，成功返回 `200` 与 `{"name": "<新名>", "task_count": <任务数>, "config_revision": "..."}`。

- `400`：目标是默认工作空间（`default` 不可改名）。
- `404`：源空间既没有任务也没有任何凭据或 `models`（空间由这几者之一反推，都没有就是不存在）。
- `409`：目标名已被占用，或目标名就是 `default`。**不会合并**两个空间——合并会瞬间造出重名任务，而任务名在同一空间内唯一是配置层的硬校验。

#### `DELETE /api/workspaces/{workspace}`

删除工作空间**连同其中的全部任务**（两把 key 也一并删掉，**它们立即失效**）。请求体只需 `config_revision`（可省略），成功返回 `204`。`400` 删除默认空间，`404` 空间不存在。

#### `POST /api/workspaces/{workspace}/inference-key`

给工作空间**换一把**推理 key，成功返回 `200`：

```json
{"name": "teamA", "inference_key": "amkr_ik_…", "config_revision": "…"}
```

- 只换推理 key，**不动**面板 key：面板 key 换掉会让已嵌入的页面立刻失效，两者的轮换
  节奏不同（推理 key 进了各项目的环境变量，泄漏面更宽、轮换更频繁）。
- 旧 key **立即失效**（配置里只留新值）。明文只在这次响应里出现。
- `400` 目标是默认空间，`404` 空间不存在。

#### `PUT /api/workspaces/{workspace}/models`

设定这个空间**允许直呼**的模型清单。请求体为 WorkspaceModels：

```json
{"config_revision": "…", "models": ["gpt-4o-mini"]}
```

`models` **必填但可为 null**，三种取值各有含义：

| 取值 | 含义 |
| --- | --- |
| `["gpt-4o-mini"]` | 只允许直呼这些模型（id 或别名都行，逐个校验可解析） |
| `[]` | 一个都不许直呼（只走任务名） |
| `null` | 清除清单，回到「不限制」 |

必填是为了不让一次漏传字段被当成「清除限制」——那是把一条授权悄悄放宽。

- **任务名不受清单限制**：任务自己固定的模型就是该空间被授权用的。
- 判定发生在**别名解析之后**：同一个模型写成别名不会绕过白名单。
- 清单里的名字是**引用**：模型被删掉时会被服务端自动摘掉（摘空则留下 `[]`，该空间此后
  不能直呼任何模型，任务名照常可用）。机制与预演方式见「改模型会连带改掉别处的引用」。
- `422`：清单里有名字解析不到任何已配置的模型。
- `400` 目标是默认空间（它没有作用域凭据，清单配了也没有对象生效），`404` 空间不存在。

#### `POST /api/workspaces/export`

导出工作空间**整包**，供搬到另一个实例。请求体可省略（导出全部命名工作空间），或给出要导出的名字：

```json
{"workspaces": ["teamA"]}
```

成功返回 `200`：

```json
{
  "bundle": {
    "version": 1,
    "workspaces": ["teamA"],
    "spaces": {"teamA": {"api_key": "amkr_ws_…", "tasks": {"summarize": {"model": "gpt-4o-mini"}}}}
  },
  "config_revision": "…"
}
```

与 `/api/config/export` 的三条关键差别：

| | `/api/config/export` | `/api/workspaces/export` |
| --- | --- | --- |
| `api_key` | **剥掉** | **带上**（有意的凭据搬迁） |
| `providers` / `models` | 核心内容 | **不含**（搬的是命名空间，不是模型库） |
| 范围 | 整台实例 | 指定或全部命名工作空间 |

默认工作空间**不在**包里：它没有名字也没有 key，搬过去等于覆盖对方的默认空间。因此
`{"workspaces": ["default"]}` 返回 `400`，不存在的名字返回 `404`（不静默跳过——那会让
人以为搬走了、实际漏了）。

#### `POST /api/workspaces/import`

把一份工作空间整包并入当前配置。请求体为 `{"config_revision": "...", "bundle": {...}, "prefix": "..."}`（`prefix` 可选），成功返回 `200`：

```json
{"imported": true, "added": ["teamA"], "replaced": [], "renamed": {}, "rekeyed": {}, "removed_tasks": [], "config_revision": "…"}
```

冲突策略由 `prefix` 决定：

- **留空** = 同名空间被整包覆盖（含 `api_key`，旧 key 立即失效）。恢复备份的语义。
- **非空**（如 `teamA-`）= 同名空间改名为 `前缀+原名`，一个都不覆盖，新名冲突时再退让为
  `…-2`、`…-3`。

| 字段 | 说明 |
| --- | --- |
| `added` | 新建的空间 |
| `replaced` | 被覆盖的已存在空间（**旧面板 key 失效**，必须告知用户） |
| `renamed` | 改过名的空间（`原名 -> 新名`） |
| `rekeyed` | **换了 key** 的空间（`空间名 -> 新 key`），只在加前缀克隆时出现 |
| `removed_tasks` | 因引用不到目标实例上的模型而被清掉的任务（`空间/任务名`） |

关于 `rekeyed`：加前缀克隆时，原空间**仍然存在**并占着原 key，克隆体不可能也用它，因此
必然换一把新 key。响应把它回报出来是必须的——嵌入方手里那把对应的是原空间，克隆出来的
空间没有可用的面板。

- `422`：包里没有 `spaces`、空间内容不是对象、含 `default`、或看起来是配置导出的整包
  （带 `providers` / `models`）——这种情况明确报错，而不是静默丢掉那两段。
- `422`：包里某个 `api_key` 撞上了目标实例上**别处**的凭据（另一个空间、
  `local_api_key`，或任意一把**访问密钥**）。这里**报错**而不是悄悄换一个新 key：
  换掉会藏起一件用户必须知道的事——那把 key 通常已经嵌在别人的页面里，换掉之后旧 key
  会指向别人**别的**空间，而响应里没有任何字段能说明这件事。错误信息会指出与哪个空间撞了。
  （唯一的例外就是 `rekeyed`：克隆造成的冲突是必然的，因此换 key 并明确回报。）
- 导入前会备份当前配置（与配置导入一致）。
- 这条通道只认**完整权限**：内容里有明文面板 key，比配置导出更敏感。

> 任务随 `/api/config/export`、`/api/config/import` 一同迁移（命名工作空间也在其中，
> 但那条通道的导出会**剥掉** `api_key`）；导入时引用不到模型的任务会被跳过（与「该模型
> 从未配置」一致）。删除模型时会一并清理引用它的任务：首选模型没了则删除整个任务，只有
> 备选没了则退化为单模型任务。

### 访问密钥接口

访问密钥（`amkr_ak_` + 43 位 base64url，共 50 字符）是分发给外部使用者的受限推理凭据，
取代了原先固定的 `amkr-visitor`。它是配置里 `access_keys.<key_id>` 这个**对象**的一段
（用 `key_id` 而不是数组下标：更新、轮换、删除都要一个稳定定位符），五个端点覆盖目录、
新建、改清单、轮换与删除。

与工作空间的两把 key 不同，访问密钥**不绑定工作空间**：它跟着请求里的
`X-AMKR-Workspace` 头走，与本地主凭据的用法一致。清单已经表达了要限制的东西（能碰哪些
上游、能调哪些模型），再绑一个空间只会让「发给外部试用者」多一道没必要的配置。

**明文只在新建与轮换的响应里出现一次**，之后任何 GET 都只回指纹。凭据随列表散出去，
等于每次打开管理页都重新泄漏一遍；要看已有 key 只能翻配置文件或轮换。

#### `GET /api/access-keys`

返回目录（只读，不含任何明文）：

```json
{
  "access_keys": [
    {
      "id": "3f2a…",
      "name": "试用账号 A",
      "enabled": true,
      "key_fingerprint": "0123456789ab",
      "providers": ["openai"],
      "models": ["gpt-4o-mini", "fast-mini"],
      "last_used_at": "2026-10-14T15:20:00+08:00"
    }
  ],
  "config_revision": "…"
}
```

顺序是配置里的插入顺序（刚建的那把在末尾），而不是字典序。

`last_used_at` 是该密钥在当前指标库中最后一条上游尝试的记录时间（带 `+08:00` 的 ISO 字符串），
包含成功与失败，不受 WebUI 统计窗口或筛选影响；没有记录时为 `null`。访问模型列表、登录看板与
在转发前被拒绝的请求不产生上游尝试记录，因此不更新此值。改名、启停或轮换不会清除同一 `id`
的调用历史；更换或清空指标库后只反映新库中的记录。仅目录响应增补此字段，未装配指标读数的
嵌入场景省略该字段（WebUI 显示「后端未提供」，不误报为从未调用）。

`providers` / `models` 只在配置里**显式写了**该字段时才出现：**省略表示「不限制」**，
**空数组表示「一个都不许」**。两者是有区别的授权状态，因此不能让空数组顶替省略——那会把
运维写下的禁令显示成「未限制」。这与 `/api/workspaces` 里 `models` 的处理是同一约定。

两份清单里的名字都是**引用**，所以模型或供应商被删掉时会被服务端自动摘掉（摘空则留下
`[]`，即该密钥此后调不动任何模型）。这是写模型/供应商那一侧的连带清理，不是本组接口的
行为；机制与预演方式见「改模型会连带改掉别处的引用」。本组接口自己的校验仍然是严格的：
在这里写下一个不存在的名字照旧 `422`。

#### `POST /api/access-keys`

新建一把密钥，成功返回 `201`。请求体：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 是 | 非空；给人看的标识，不参与鉴权 |
| `key` | string/null | 否 | 明文密钥；不传或为 `null` 时由服务端生成（`amkr_ak_` 前缀）。传了就用调用方给的（应用侧通常已有既定凭据） |
| `enabled` | boolean | 否 | 默认 `true`；为假时这把密钥立即失效，但配置保留 |
| `providers` | string[]/null | 否 | 允许使用的供应商 ID 清单；省略与 `null` 都表示本次不设这份清单（不限制），`[]` 表示一个都不许 |
| `models` | string[]/null | 否 | 允许使用的模型名清单（真实 ID 或别名）；三态同 `providers` |
| `config_revision` | string | 是 | 并发校验版本号 |

`id` 由服务端生成（32 位十六进制，与 `uuid4().hex` 同形）：它是配置里的稳定定位符，
让调用方自己取 id 只会引入「重名怎么办」这种没有意义的问题。

成功响应是新建的那一条，**外加一个 `key` 字段**（明文，仅此一次）：

```json
{
  "id": "3f2a…",
  "name": "试用账号 A",
  "enabled": true,
  "key_fingerprint": "0123456789ab",
  "providers": ["openai"],
  "models": [],
  "key": "amkr_ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "config_revision": "…"
}
```

- 请求里显式传 `[]` 会原样落成空数组（一个都不许），不会退化成「不限制」。
- 新建接口的 `providers` / `models` **没有**「清除」这一态：清除限制只出现在更新接口上。

#### `PUT /api/access-keys/{key_id}`

改名字、启停与两份清单。**不换 key** —— 轮换是独立端点，它会让调用方手里那把立刻失效，
是必须单独告知的动作；塞进「更新」里就等于一次改名顺手换掉别人正在用的凭据。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string/null | 否 | 非空；`null` 表示本次不改 |
| `enabled` | boolean/null | 否 | `null` 表示本次不改 |
| `providers` | string[]/null | **是** | `[...]` 限定为这些；`[]` 一个都不许；`null` 清除清单，回到「不限制」 |
| `models` | string[]/null | **是** | 三态同 `providers` |
| `config_revision` | string | 是 | 并发校验版本号 |

`providers` 与 `models` **必填但可为 `null`**，与 `PUT /api/workspaces/{workspace}/models`
同一套三态。必填是为了不让一次漏传字段被当成「清除限制」——那是把一条授权悄悄放宽；要清除
就显式写 `null`。

成功返回该条目的当前形状（与目录里的元素相同，**没有** `key` 字段）。

```bash
curl -X PUT http://127.0.0.1:8000/api/access-keys/3f2a… \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{"providers": ["openai"], "models": null}'
```

#### `POST /api/access-keys/{key_id}/rotate`

换掉这把密钥的明文并返回新值，成功返回 `200`：

```json
{
  "id": "3f2a…",
  "name": "试用账号 A",
  "enabled": true,
  "key_fingerprint": "…",
  "key": "amkr_ak_yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy",
  "config_revision": "…"
}
```

- 旧 key **立即失效**（配置里只留新值）——这正是轮换的意义：泄漏的那把在写盘那一刻就不再
  被任何判定命中。
- 明文只在这次响应里出现。请求体可省略，或只带 `config_revision`。

#### `DELETE /api/access-keys/{key_id}`

删除该密钥，成功返回 `204`，该 key 立即失效。请求体可省略，或只带 `config_revision`。删掉
最后一把时 `access_keys` 段会一并从配置里移除（空段留着会让「有没有配过访问密钥」看起来是真）。

#### 访问密钥的错误

| 状态码 | 场景 |
| --- | --- |
| `404` | `{"detail": "访问密钥不存在: <id>"}`；更新、轮换、删除一个不存在的 id |
| `422` | `{"detail": "访问密钥 ID 不能为空"}`；路径里的 id 去掉空白后为空 |
| `422` | `access_keys.<id>.providers[<n>] 引用了未配置的供应商: <name>` / `access_keys.<id>.models[<n>] 引用了未配置的模型: <name>`；清单里写错目标。写错一个名字会让某把已经分发出去的 key 静默少一项权限，而调用方只看到 `403`，因此宁可在这里明确报错 |
| `422` | `access_keys.<id>.providers[<n>] 不能为空` / `access_keys.<id>.models[<n>] 不能为空`；清单里出现空串 |
| `422` | `访问密钥的 key 不能与 local_api_key 相同` / `访问密钥 <id> 与 <占用者> 的 key 重复`；同一把凭据在实例内出现两次 |

凭据唯一性是**跨类型**的：`local_api_key`、`workspaces.*.api_key`、
`workspaces.*.inference_key` 与 `access_keys.*.key` 共用一张占用表，任意两处相同都是配置
错误（写盘时 `422`，加载时是配置错误），而不是「取第一个」。撞车会让同一把 key 的权限取决
于先命中哪张清单——那是把权限边界交给判定顺序。

> 访问密钥**不随** `/api/config/export` 导出，也不接受 `/api/config/import` 导入。它与
> `local_api_key` 同类：是本实例的入站凭据，而导出文件常被贴进工单与聊天记录。想迁移请
> 在目标实例上重新建一把。

### 供应商接口与能力探测

v4 中 Key 与探测都以 Key 为单元：`providers.<id>` 保存 `base_url`、`max_concurrency`、各协议 `routes` 与 `keys`（Key 集合），探测缓存按 Key 存放在 `providers.<id>.keys.<key>.capabilities`。同一供应商的不同 Key 能访问的模型集可能不同，因此每个 Key 独立探测、独立缓存，互不复用。删除供应商或其 Key 时会清理所有引用它们的模型绑定。

#### `GET /api/providers`

返回：

```json
{"providers": [ProviderResponse]}
```

#### `POST /api/providers`

请求体：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 供应商 ID，非空且不能与已有 ID 重复 |
| `base_url` | string | 是 | `http://` 或 `https://` 开头的上游地址 |
| `max_concurrency` | integer/null | 否 | 正整数表示该供应商**所有 Key、所有模型**上同时在途的上游请求上限；`null` 或 `0` 表示不限制（默认） |
| `config_revision` | string | 是 | 并发校验版本号 |

成功返回 `201` 和 ProviderResponse。新供应商没有 Key，添加 Key 请用 `/api/providers/{provider_id}/keys` 或模型 Key 接口（按 `base_url` 自动归并）。

#### `GET/PUT/DELETE /api/providers/{provider_id}`

查询、更新（`id`、`base_url`、`routes`、`max_concurrency`）或删除供应商。`PUT` 请求体为 `ProviderUpdate`（`id`/`base_url`/`routes`/`max_concurrency` 可省略）+ `config_revision`；`max_concurrency` 传 `null` 或 `0` 表示清除该供应商的并发上限。改 `id` 即改名，模型 `targets[].provider` 与访问密钥的供应商清单会跟着改（见「改模型会连带改掉别处的引用」）。删除供应商会移除其所有 Key，并删除所有引用它的模型 target；因此失去全部 target 的模型会被一并删除（响应不含被删模型列表，删除前请自行确认）。引用这些模型的访问密钥、工作空间与任务同样会被清理（见「改模型会连带改掉别处的引用」）；带 `?dry_run=1` 可先拿到这份清理清单而不落盘。

#### `GET/POST /api/providers/{provider_id}/keys`

列出该供应商的 Key（`{"keys": [KeyResponse]}`）或创建新 Key。创建请求体为 ProviderKeyCreate（`name`、`api_key`、`enabled`、`max_concurrency`、`config_revision`），成功返回 `201` 和 KeyResponse。`max_concurrency` 可选，正整数表示该把上游 Key 同时在途的请求上限，`null` 或 `0` 表示不限制。新 Key 默认不绑定任何模型；需要按模型绑定请使用模型 Key 接口或 `/api/routes`。

#### `GET/PUT/DELETE /api/providers/{provider_id}/keys/{key_name}`

查询、更新或删除单个供应商 Key。`PUT` 请求体为 ProviderKeyUpdate（`name`/`api_key`/`enabled`/`max_concurrency` 可省略，`config_revision` 必填；`max_concurrency` 传 `null` 或 `0` 表示清除上限）。删除会从所有模型的 `targets[]` 中移除对该 Key 的引用，因而失去全部 target 的模型会被自动删除；若这是供应商最后一个 Key，供应商也会被删除。被模型 Key 接口解绑到只剩本模型时，同样会走到这里（删除模型 Key 的最后引用）。

> **并发上限的两条口径与一条不变式**：`POST /api/providers`、`PUT /api/providers/{provider_id}`、`POST /api/providers/{provider_id}/keys`、`PUT /api/providers/{provider_id}/keys/{key_name}` 都可选地接受 `max_concurrency`，正整数为同时在途请求上限，`null` 或 `0` 清除上限（缺省即不限制）。供应商上限统计该供应商**所有 Key、所有模型**上同时在途的上游请求；Key 上限按**上游 Key**（按密钥/账号识别）统计，同一把 Key 被多个模型引用时共用一个计数。**同时启用两者时**，该供应商**启用中**的 Key 的 `max_concurrency` 之和不得大于供应商的 `max_concurrency`，否则写入被拒绝并返回 `400`，`detail` 以 `配置校验失败: ` 开头，例如 `配置校验失败: 供应商 openai 的 Key 并发上限之和 12 不能大于供应商并发上限 10`；负数同样是配置错误（`供应商 X 的 max_concurrency 不能为负数`、`供应商 X 的 Key Y 的 max_concurrency 不能为负数`）。选路时达到上限的 Key 与达到上限的供应商会被**硬性排除**（不做冷却式软回退），某个模型的候选全部因此不可用时，代理返回 `429`（见「状态码与错误格式」）。

#### `POST /api/providers/{provider_id}/probe`

同步刷新该供应商**全部启用 Key** 的能力探测：每个 Key 分别执行 `GET /v1/models` 拉取该 Key 的可服务模型清单，并对该 Key 在 `openai` / `anthropic` / `responses` 三个路由模式下各做一次最小请求，结果分别写入 `providers.<id>.keys.<key>.capabilities` 后随响应返回。请求体只需携带 `config_revision`。供应商尚无 Key 时返回 `422`。探测结果按 Key 缓存，不跨 Key 共享或折叠。

```bash
curl -X POST http://127.0.0.1:8000/api/providers/openai/probe \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{"config_revision": "..."}'
```

#### `POST /api/providers/{provider_id}/keys/{key_name}/probe`

同步刷新**指定单个 Key** 的能力探测：同样先执行 `GET /v1/models` 拉取该 Key 的可服务模型清单，再按 `modes` 做路由最小请求，结果写入 `providers.<id>.keys.<key>.capabilities` 后随响应返回。请求体为：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `config_revision` | string | 是 | 并发校验版本号 |
| `modes` | string[] | 否 | 限定做最小请求的路由模式，可选 `openai`、`anthropic`、`responses`；省略时检查全部路由模式。模型清单探测总是执行 |

```bash
curl -X POST http://127.0.0.1:8000/api/providers/openai/keys/main/probe \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{"config_revision": "...", "modes": ["openai", "responses"]}'
```

响应额外包含 `key` 对象（含 `capabilities`，未探测时为 `null`）。

> 说明：探测缓存是机器本地信息（不同机器、不同 Key 权限看到的模型可能不同），只由以上手动刷新入口更新；WebUI 供应商页的「刷新能力探测」语义与此一致（刷新全部 Key，或指定 Key 并可限定端点范围）。管理 API 的 Key 创建、模型绑定等写操作不会自动发起探测。

#### `POST /api/probes/keys`（兼容接口）

按 Key 粒度的异步探测，返回 `202` 与 `probe_id`。请求体为 `ProbeKeysRequest`（`provider_id`、`keys`、`timeout_seconds`），随后的 `GET /api/probes/{probe_id}` 轮询结果、`POST /api/probes/{probe_id}/cancel` 取消。该接口的模型列表结果同样按 Key 独立探测；日常手动刷新请优先使用上面的 `probe` 接口（同步写回并随响应返回结果）。

### 路由接口

`/api/routes` 系列是模型路由（targets）的管理入口，与 `/api/models` 操作同一份模型数据：`POST /api/routes` 创建模型并写入 targets，`GET/PUT/DELETE /api/routes/{route_id}` 读取、整体替换或删除某模型的 targets。请求体中的 `targets` 为 RouteTarget 数组（`{provider, key, upstream_model}`），target 引用的供应商与 Key 必须已存在。v3 的 `pool` 引用已不存在于 target 中。请求体还认 `id`（改名，会一并改写 `unified_model`、模型的端点计划、任务、工作空间与访问密钥清单里的引用，见「改模型会连带改掉别处的引用」）与 `aliases`（额外的可调用名）；`hidden_aliases` 已移除，请求里带上它会被当成未知字段返回 `422`。

请求体里的 `endpoints` 是这个模型名下的**端点计划**：形如 `{"<族>": {"primary": {"model", "key"}, "fallback": {...}}}`，与 `unified_model` 的分族计划逐字同构（族名、字段、校验规则都相同）。它在**直呼这个模型名**时生效：落在配置过的端点族上的请求改发给那一族的模型，未配置的族仍走该模型自己的 `targets`。分派只有一跳——被指到的模型自己的 `endpoints` 不再参与解析。三态与 `targets` 不同：

- **省略** `endpoints`：不改动这张表（与 `targets` 一样，省略 ≠ 清空）；
- **传 `null` 或 `{}`**：清空这张表，所有端点回到该模型自己的 `targets`；
- **传含有键的对象**：**整体替换**——没写到的族会被清掉。

```bash
curl -X PUT http://127.0.0.1:8000/api/routes/gpt-5.5 \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "config_revision": "rev-...",
    "targets": [{"provider": "openai", "key": "main", "upstream_model": "gpt-5.5"}],
    "endpoints": {"image": {"primary": {"model": "gpt-image-1"}}}
  }'
```

一条路由下没有 target 就不该存在，这条不变式由服务端在各条写路径上保证：

- `PUT /api/routes/{route_id}` 传 `targets: []` 会**删除该路由**并返回 `204 No Content`（没有响应体，也就没有 `config_revision`）。省略 `targets` 字段则表示不改动目标，与传空数组是两回事。
- 解绑 Key、取消 Key 勾选、删除 Key 等路径删掉最后一条 target 时，同样会连带删除该路由。其中「取消 Key 勾选」（`PUT /api/providers/{provider_id}/keys/{key_name}/models`）删掉最后一条 target 时**只删路由，不清理供应商 Key**（见「消失：引用被一并清理」）。

被删掉的路由会从 `models` 里消失，引用它的 `unified_model`、任务、工作空间与访问密钥清单会被一并清理（与 `DELETE /api/routes/{route_id}` 同一套修复，见「改模型会连带改掉别处的引用」）；这两条删除路径还会把该路由 `targets[]` 引用过、此后**不再被任何模型绑定**的供应商 Key 一并删掉，仍被其他路由绑定的原样保留，供应商本身不删（见「消失：引用被一并清理」）。它们都支持 `?dry_run=1` 先预演，`removed_keys` 就是这份将被删掉的 Key 清单。

不变式管的是「**失去**最后一个目标」：新模型仍然可以先不带 Key 建出来（`POST /api/models` 的 `keys` 可为空，见下），在它绑上第一个 Key 之前只是不可调用；`POST /api/routes` 则要求 `targets` 至少一项，不能用它建一条空路由。

```bash
curl -X POST http://127.0.0.1:8000/api/routes \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "gpt-5.5",
    "aliases": ["gpt"],
    "routing_mode": "round_robin",
    "targets": [
      {"provider": "openai", "key": "main", "upstream_model": "gpt-5.5"},
      {"provider": "tokenplan", "key": "mimo", "upstream_model": "gpt-5.5"}
    ]
  }'
```

### CPA 账号资源接口

`/api/cpa-instances` 与 `/api/cpa-accounts` 把若干个
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（下称 CPA）实例的账号、以及按订阅计费的
供应商（OpenCode Go、Command Code）的剩余额度，汇总到一块看板上（WebUI 的「账号资源」页）。
实例清单存在配置文件的顶层键 `cpa_instances` 里：

```json
{
  "config_version": 4,
  "cpa_instances": {
    "cpa-a": {
      "label": "主力 CPA",
      "base_url": "http://127.0.0.1:8317",
      "management_key": "your-cpa-management-key"
    }
  }
}
```

`management_key` 是 CPA **管理面**（`/v0/management/*`）的密钥，不是模型调用 key。写入时
`base_url` 去尾斜杠并要求 `http`/`https`，`management_key` 去空白且非空，`label` 缺省时用实例
ID 兜底；实例里未知的字段原样保留（与配置整体一致：旧版本写回不该丢新字段）。这个键不在
`/api/settings` 那批已发布字段里，因此它的增删不会牵动那份逐字对齐的契约。

> **CPA 为什么必须单独配管理密钥，而不能像订阅那样自动派生**（已核实到源码与实机验证，CPA
> commit `9bdde54`）：CPA 的**推理 key 与管理密钥是两个互不相通的命名空间**。管理面鉴权
> （`internal/api/handlers/management/handler.go` 的 `AuthenticateManagementKey`）只比对
> `remote-management.secret-key` 的 bcrypt 哈希、`MANAGEMENT_PASSWORD` 环境变量与本地
> `--password`，**从不查 `api-keys`**。实机验证：同一把推理 key 打 `/v1/models` 得到 200，
> 打 `/v0/management/auth-files` 得到 `401 {"error":"invalid management key"}`。管理密钥在
> 启动时被 bcrypt 写回 `config.yaml`，而 `api-keys` 一直是明文，存储方式也不同。
> 另外整个推理面（`/v1/*`、`/v1beta/*`…）**没有任何**列出账号或额度的路由——账号与额度的清单
> 只存在于管理面。所以能派生的最多是「这个 base_url 是个 CPA」（响应头带
> `X-CPA-VERSION` 等 `X-CPA-*` 指纹），**账号清单派生不出来**，管理密钥必须由用户提供。
> 订阅类供应商没有这层分割：它们的用量接口就用同一把 `api_key`，所以那半边才能自动派生。

> **运维提醒**：CPA 的管理面对鉴权失败有 **5 次 / 30 分钟的 IP 封禁**（按 client IP 计，
> **loopback 也照封**）。因此不要拿错误的密钥去反复试探 `/v0/management/*` —— 试满 5 次会让
> AMKR 连着它自己的看板一起被锁在外面半小时。AMKR 只在用户显式配了实例、点开看板时才去读，
> 不做后台轮询，也不做「猜密钥」式的探测。

```bash
# 读实例清单：响应含 config_revision 与各实例的 management_key（要求完整权限）
curl http://127.0.0.1:8000/api/cpa-instances -H "Authorization: Bearer your-local-api-key"

# 整体替换清单（不是逐条增删；config_revision 必填，防并发覆盖）
curl -X PUT http://127.0.0.1:8000/api/cpa-instances \
  -H "Authorization: Bearer your-local-api-key" -H "Content-Type: application/json" \
  -d '{"config_revision":"<上一条响应里的值>","instances":{
        "cpa-a":{"label":"主力 CPA","base_url":"http://127.0.0.1:8317","management_key":"..."}}}'
```

`GET /api/cpa-accounts` 由 AMKR 的**服务端**替调用方去问各实例与各订阅端点，返回归一化后的账号与
额度。`instances[]` 是 CPA 实例，`subscriptions[]` 是订阅类供应商：

```json
{
  "fetched_at": "2026-01-01T00:00:00Z",
  "instances": [
    {
      "id": "cpa-a", "label": "主力 CPA", "base_url": "http://127.0.0.1:8317",
      "ok": true, "observed_at": "2026-01-01T00:00:00Z",
      "accounts": [
        {
          "auth_index": "0", "name": "claude-1.json", "provider": "claude",
          "account_type": "oauth", "project_id": "proj-1",
          "email": "a@example.com", "status": "active", "disabled": false, "unavailable": false,
          "success": 12, "failed": 1, "supports_quota": false,
          "plan": "Pro", "tier_id": "pro-tier",
          "windows": [
            {"key": "claude/5h", "label": "5 小时", "window": "5h", "group": "Gemini Models",
             "remaining": 0.58, "reset_at": "2027-01-15T08:00:00Z", "source": "passive"}
          ],
          "summary": [{"key": "credits", "label": "剩余积分", "value": 12.5, "unit": "credit"}],
          "model_quotas": {
            "gpt-6-luna": {
              "observed_at": "2026-01-01T00:00:00Z",
              "windows": [{"key": "codex/primary", "label": "5 小时", "remaining": 0.1, "source": "passive"}]
            }
          },
          "recent_requests": [{"time": "13:20-13:30", "success": 3, "failed": 1}],
          "server_time_offset_ms": -485,
          "signals": {"Anthropic-Ratelimit-Unified-5h-Utilization": "0.42"}
        }
      ]
    },
    {"id": "cpa-b", "label": "备用 CPA", "base_url": "http://10.0.0.9:8317",
     "ok": false, "error": "无法连接 CPA: ... connection refused", "accounts": []}
  ],
  "subscriptions": [
    {
      "id": "opencode-go-a/key-1", "kind": "opencode-go", "vendor": "OpenCode Go",
      "provider_id": "opencode-go-a", "key_name": "key-1",
      "base_url": "https://opencode.ai/zen/go/v1",
      "ok": true, "observed_at": "2026-01-01T00:00:00Z",
      "plan": "Go", "tier_id": "individual-go", "account": "someone",
      "windows": [
        {"key": "opencode-go/rolling", "label": "5 小时", "window": "5h",
         "remaining": 0.58, "reset_at": "2026-01-01T05:00:00Z", "source": "quota"}
      ],
      "summary": [{"key": "monthlyCredits", "label": "订阅积分", "value": 7.5, "unit": "credit"}],
      "signals": {"status": "active"}
    }
  ]
}
```

#### 订阅条目是**派生**出来的

`subscriptions[]` 不来自任何配置键，而是从已配置的**供应商**推出来的：某个供应商的 `base_url`
落在已知的订阅端点上时，它的**每个启用 key** 就产生一条订阅。删掉供应商，条目随之消失；不存在
「配置里已经没有这个供应商、看板上还在读它的额度」这种漂移，也就不需要任何清理钩子。

因此这一块是**只读**的——没有「新增订阅」这个写接口，也没有对应的编辑器。要加一条订阅，就是
在供应商里加一个指向该端点的供应商（用哪把 key 由那个供应商的 `api_key` 决定）。AMKR 不为它
额外存任何凭据。

已被识别的端点（认不出就**不产生条目**，绝不猜）：

| `kind` | 匹配规则 | 用量请求 |
| --- | --- | --- |
| `opencode-go` | 主机 `opencode.ai` 且路径以 `/zen/go` 开头 | `GET {base_url}/usage` |
| `commandcode` | 主机 `api.commandcode.ai` | `GET /alpha/whoami`、`/alpha/billing/credits`、`/alpha/billing/subscriptions`、`/alpha/usage/summary` |

路径也参与 OpenCode 的判定（不只是主机）：`/zen/v1` 是 Zen 的推理端点，拿 Zen 的 key 去问 Go
的用量会得到 `403 subscription required`，那会被误读成「订阅过期」。这里认错就等于给出一个
看起来像事实的错误结论。

订阅条目的口径：

- **与 `instances[]` 同一次扇出、同一个 `fetched_at`**：两块并发去问，谁慢都不拖累对方。页面上
  这是一块看板，分两次取会出现两个时间戳，而用户看到的是同一屏数字。
- **`windows[]` 与 CPA 那半边**是同一套类型与同一套含义（`remaining` 是**剩余比例** 0..1、
  `reset_at` 是 RFC3339、`source` 为 `quota`）。上游给的是**已用**百分数，服务端翻成剩余；
  `rate-limited` 归一成 `status: "rejected"`。
- **认不出的窗口名原样保留**，与 CPA 侧同一个函数（`5h`/`weekly`/`monthly` → 「5 小时」/
  「7 天」/「30 天」，其余照抄）。
- **缺字段就不画**：上游没给比例的窗口整个跳过，cap 为 0 或缺失的窗口也跳过。少一个窗口好过替
  上游保证一个它没说的额度。
- **重试**：OpenCode Go 的用量端点会无规律地回 `503`（与凭据无关），所以会重试（最多 4 次，
  退避 250ms/500ms/1s，只重试 429/5xx）。`401`/`403` 是凭据语义，不重试，并给出可操作的说明
  （「凭据无效」/「该工作区没有 OpenCode Go 订阅」）。
- **Command Code 逐端点容错**：四条 GET 各自独立，只有**全部**失败才把条目判为失败（`error` 里
  逐条列出），局部失败进 `signals["读取失败"]`——这些是各自独立的内部接口，一起挂掉的概率远低于
  其中一个漂移，把局部失败升级成整条失败会让一次无关紧要的字段改名打掉整个区块。
- **失败是逐条目的**：某把 key 不对只让它自己的 `error` 有值，其余条目与 `instances[]` 照常返回。
- **`signals[]` 是原始信号兜底**：订阅侧把上游那些还没被归一化的状态字段原样列出（如
  `status`、`cancelAtPeriodEnd`），与 CPA 侧的 `signals` 同一个用途。
- **请求只带一种凭据头**：`Authorization: Bearer <该供应商的 api_key>`。AMKR 只读，不改对端状态。
- **只看不拦**：这些读数同样**不参与** AMKR 的 Key 冷却与失败切换，与 CPA 那半边一致。

CPA 侧的口径与边界：

- **为什么由服务端去问**：CPA 的管理接口与 AMKR 不同源、也没有 CORS 头，浏览器直接请求必被
  拦；而 `management_key` 是能改 CPA 配置的凭据，不该长期放进浏览器。请求只发
  `Authorization: Bearer <management_key>` 一种凭据头。
- **只读、不缓存**：只调 CPA 的 `GET /v0/management/auth-files` 与
  `POST /v0/management/quota/fetch`，不改 CPA 的任何状态，AMKR 侧也不留副本——看板上的数字
  与 CPA 里的一致，没有中间层会过期。因此**没有**轮询：每次调用都是一次真实的扇出。
- **额度的两个来源**（`windows[].source`）：`passive` 是 CPA 从上游响应头采到并缓存的快照
  （Anthropic 的 `anthropic-ratelimit-unified-*`、Codex 的 `x-codex-*`，白名单见 CPA 的
  `sdk/cliproxy/auth/quota_signals.go`）；`quota` 是现场 `quota/fetch` 的归一化结果。同一账号
  两者都有时**后者覆盖前者**——它们算的是同一件事，而现场值更新，混起来会出现一半新一半旧的
  进度条。两条通道都不需要 AMKR 自己去请求上游。
- **`remaining` 是剩余比例**（0..1），不是已用；`reset_at` 归一成 RFC3339。
- **窗口的四个字段各管一件事**：`label` 是给人看的窗口名（`5h` / `weekly` 归一成「5 小时」/
  「7 天」，认不出的窗口名原样保留、不猜），`window` 是上游的原始窗口名，`group` 是该窗口
  所属的**模型组**（Antigravity 的 Gemini 与 Claude/GPT 各有一套 5 小时 + 周期额度、窗口名
  一模一样，靠它区分），`description` 是上游附的那句说明（"You have used some of your weekly
  limit, it will fully refresh in 5 days, 9 hours."）——它是解释而不是名字，界面上不该拿它当
  标题，否则同一页会出现「一条写 `5h`、另一条写一整句英文」。
- **`summary[]` 是「不成窗口的量」**：余额、积分、计费系数这类数值项，`value` 与 `unit` /
  `currency` 一起给，AMKR 不归一也不换算（猜错单位比不显示更糟）。它可能为空而 `windows`
  非空，反之亦然——现场探测只要成功就采纳整份结果，只回数值项、不回窗口的计费类插件也算数。
- **`model_quotas` 与 `recent_requests` 来自 `auth-files`，不额外发请求**：前者是按模型维度的
  被动快照（同一套头的解析口径与 `windows` 共用一份代码），后者是 CPA 维护的十分钟请求桶，
  界面用它画迷你趋势和「谁在什么时候打的」。
- **`server_time_offset_ms` 是对端时钟与本地时钟的差**（CPA 的 `serverTimeOffsetMs`）：
  `reset_at` 按对端时钟写，客户端算「还有多久重置」时应当把它减掉，否则对端差几分钟、倒计时
  就偏几分钟。订阅条目**没有**这个字段：那两家不汇报时差，凭空造一个只会让倒计时更不准。
- **没有额度时的提示**：`501` 表示对端没有额度提供者（既没装额度插件、也没给该账号配声明式
  `quota_probe`），界面上要如实说「对端没有额度提供者」——既不能显示成 0，也不能编一条出来。
  Antigravity 这类 provider 属于「对端配了 probe 才有」的那类，配方见
  [`docs/SUBSCRIPTION-QUOTA.md`](SUBSCRIPTION-QUOTA.md) 的 3.3 节。
- **只有 `supports_quota` 的账号会被额外问一次额度**：CPA 没有额度提供者时回
  `501 no quota provider available for credential`。此时若该账号已有被动窗口，`quota_error`
  为空（不构成故障，报出来只是噪音）；一个窗口都没有时才会带上 `quota_error`。
- **失败是逐实例的**：某个实例连不上、管理密钥不对或条目残缺，只让它的 `error` 有值，其余实例
  照常返回。整个扇出有 60 秒总预算，到点未回的账号按无额度处理。
- **只看不拦**：这些读数**不参与** AMKR 的 Key 冷却与失败切换。AMKR 路由的是它自己的上游 Key，
  与 CPA 侧的账号额度没有耦合关系；额度耗尽该由 CPA 自己的冷却机制处理。

`subscriptions` 与 `instances` 一样**恒为数组**（没有条目时是 `[]`，不是 `null`），客户端少写一次
判空也不会炸。

额度通道的来路与官方程度（哪些 provider 有得读、哪些只能靠本地语言服务器、哪些订阅端的用量
接口是官方文档里的、哪些是从社区实现反推的）见
[`docs/SUBSCRIPTION-QUOTA.md`](SUBSCRIPTION-QUOTA.md)。

### CPA 重置卡接口

额度回答「还剩多少」，重置卡回答「还能手动恢复几次、每次什么时候过期」。CPA 自己不采集也不
暴露后者，所以 AMKR 借 CPA 的 `POST /v0/management/api-call`，用该账号的凭据代打上游的账号
接口。目前覆盖两家：

| 供应商 | 读取 | 消费 |
| --- | --- | --- |
| `codex` | `GET https://chatgpt.com/backend-api/wham/rate-limit-reset-credits` | `POST .../rate-limit-reset-credits/consume`，body 含 `credit_id` 与 `redeem_request_id` |
| `claude` | `GET https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1`，没有可兑换项时再问 `?at_wall=1&skip_spend=1` | `POST https://api.anthropic.com/api/organizations/{org}/reset_rate_limits`，`program` 为 `cedar_ember` / `juniper_tide` |

```bash
# 读取全部实例的重置卡（按最快过期排序）
curl http://127.0.0.1:8000/api/cpa-reset-credits \
  -H "Authorization: Bearer your-local-api-key"

# 使用一张：不传 credit_id 时自动选最快过期的可用卡
curl -X POST http://127.0.0.1:8000/api/cpa-reset-credits/use \
  -H "Authorization: Bearer your-local-api-key" -H "Content-Type: application/json" \
  -d '{"instance":"cpa-gpt","auth_index":"0"}'
```

`GET /api/cpa-reset-credits` 只返回 `claude` / `codex` 账号（其它 provider 没有这套接口），
形状如下：

```json
{
  "fetched_at": "2026-01-01T00:00:00Z",
  "instances": [
    {"id": "cpa-gpt", "label": "GPT", "base_url": "http://127.0.0.1:8320", "ok": true,
     "accounts": [
       {"auth_index": "0", "name": "codex.json", "provider": "codex", "email": "a@example.com",
        "status": "active", "disabled": false, "unavailable": false,
        "reset_credits": {
          "provider": "codex", "available_count": 2, "redeemable_count": 2, "eligible": true,
          "credits": [
            {"id": "RateLimitResetCredit_...", "program": "codex_rate_limits", "status": "available",
             "remaining_count": 1, "granted_at": "2026-10-01T00:00:00Z",
             "expires_at": "2026-10-31T00:00:00Z", "usable": true}
          ]
        }}
     ]},
    {"id": "cpa-b", "label": "备用 CPA", "ok": false, "error": "无法连接 CPA: ...", "accounts": []}
  ]
}
```

- **`available_count` 是未过期卡上的剩余次数，不是卡片张数**：Claude 的一张 grant 可能带多次
  `resets_left`。`credits[]` 恒为数组，按 `expires_at` 升序，没有到期时间的排在最后。
  `redeemable_count` 是**此刻就能消费**的次数（Claude Cedar 只有 `next_grant_id` 指向的那张算数，
  Juniper 可用时固定为 `juniper_tide`），`next_credit_id` 指向上游指定的下一张卡或程序名；界面
  若要显示「现在能点几次」，用前两个字段而不是 `available_count`。`cooldown_until` 是上游给出的
  冷却结束时间（Claude 的 `next_available_at` / `cooldown_until`），没有则为空。
- **`program`** 是上游的卡类型：Codex 恒为 `codex_rate_limits`；Claude 为 `cedar_ember`
  （保存在账号里的重置卡）或 `juniper_tide`（会话限额的周期性重置）。
- **`usable`** 表示服务端此刻允许消费；`status` 为 `available` / `redeemed` / `expired` /
  `paused` / `unavailable`。`eligible=false` 时 `reason` 带上游原话（例如 Claude 的
  `surface`，表示这张卡只在 Claude.ai 网页端可用，OAuth 接口不能兑换）。
- **Codex 账号必须有 `id_token.chatgpt_account_id`**（CPA 的 auth-files 会带）。缺这个字段的
  条目依然会出现在列表里，但该账号的 `reset_credits.error` 会说明原因，不影响其它账号。
- **失败逐实例、逐账号隔离**：实例连不上只让该实例 `ok=false`；单个账号读失败只写该账号的
  `reset_credits.error`。整次扇出共用 60 秒预算，单账号 20 秒超时。
- **不缓存、不轮询**：每次调用都是真实的上游请求。Codex 的这个端点本身会限流（429），频繁刷新
  会被上游拒绝；看板应在进入页面或用户点「刷新」时取一次。
- **只读**：这条 GET 不改变 CPA 或上游账号状态。

`POST /api/cpa-reset-credits/use` 的请求体：

```json
{"instance": "cpa-gpt", "auth_index": "0", "credit_id": "可选", "request_id": "可选"}
```

不传 `credit_id` 时，服务端重新读取该账号的卡，按 **`expires_at` 最早优先**（无到期时间的最后）
挑一张 `usable=true` 的卡消费。传 `credit_id` 只认指定的卡；传 `request_id` 会把同一个幂等键
带给上游，客户端在网络错误后重试时应同时带上**同一个 `credit_id` 与 `request_id`**，避免把另一张
卡再花一次。Claude 的 `juniper_tide` 消费接口不接受幂等键，这类响应不会回显 `request_id`，重试
前应重新读取卡片状态。同一实例、同一账号的读卡→选卡→消费由服务端串行处理，避免并发请求同时
挑中同一张卡。

```json
{"instance":"cpa-gpt","auth_index":"0","provider":"codex",
 "request_id":"4f2c...","credit":{"id":"RateLimitResetCredit_...","program":"codex_rate_limits",
 "status":"available","remaining_count":1,"expires_at":"2026-10-31T00:00:00Z","usable":true},
 "outcome":"reset","cleared":[]}
```

`outcome` 取值：`reset`（已重置）、`nothing_to_reset`（当前没有需要重置的限额）、`no_credit`
（没有可用卡，HTTP 仍为 200，`reason` 会带上游原因，例如 `surface`）、`already_redeemed`
（该卡已被用掉）。读卡或上游消费失败返回 `502`；实例或账号不存在、指定 `credit_id` 不存在
返回 `404`；实例配置不完整、供应商不支持，或指定的卡当前不可用返回 `409`。

> **Claude 的 surface 门禁**：Anthropic 可能把「Reset for free」限定在 Claude.ai 网页端，
> 同样的 OAuth 凭据通过 `api.anthropic.com` 查询会得到 `eligible=false, reason="surface"`。
> 这不是 AMKR 的解析问题，也不是可以绕过的客户端限制；接口会如实返回原因。若该账号的
> Juniper 会话重置仍可用，会继续返回 `juniper_tide` 的卡，否则不会伪造一张可兑换的卡。
> Codex 不受此限制，但要求账号的 `id_token` 里有 ChatGPT account id。

## 状态码与错误格式

常见状态码：

| 状态码 | 场景 |
| --- | --- |
| `200/201` | 管理接口读写成功（创建类返回 `201`，同步探测 `POST /api/providers/{id}/probe` 与 `POST /api/providers/{id}/keys/{key_name}/probe` 返回 `200`） |
| `202` | 异步探测任务已接受（`/api/probes/keys`） |
| `204` | 删除成功 |
| `400` | 请求体中缺少 `model`、更新体为空或配置校验失败；请求任务名时显式传了该任务已固定的采样参数，或指定了 Key（`TASK_XXXXXX[key]`） |
| `401` | 本地 API key 验证失败 |
| `403` | 访问密钥无权访问该模型（不在它的 `providers` 或 `models` 清单里），或该访问密钥已被停用 |
| `404` | 模型、Key、供应商、探测或访问密钥不存在；任务指向的模型没有启用的 Key；任务尚未指定模型；工作空间不存在（迁移导出按名取空间时） |
| `409` | 名称冲突、删除最后一个 Key、无法持久化嵌入式配置 |
| `422` | 管理 API 请求字段类型错误、缺少必填字段或包含未知字段；供应商暂无 Key 时探测；工作空间迁移包畸形、含 `default` 或看起来是配置导出的整包；**在访问密钥接口里**写下的 `providers` / `models` 引用了未配置的供应商或模型，或凭据与实例内其它凭据重复 |
| `429` | **仅 `/v1/*` 代理接口**：选路并发耗尽——该模型的候选 Key 或供应商已达 `max_concurrency` 上限，AMKR 直接拒绝，不请求上游（响应体形状见下） |
| `500` | 配置保存失败 |
| `502` | 上游连接或响应转换失败 |
| `503` | 没有可用 Key |

代理接口错误通常使用：

```json
{"error": {"message": "错误信息"}}
```

**选路并发耗尽**的 `429` 也走同一个信封（同样不来自上游，而是 AMKR 自己的选路判断），只有文案不同：供应商级为 `{"error": {"message": "模型 gpt-4o-mini 并发已达上限：供应商 openai 已达上限 10"}}`，Key 级为 `{"error": {"message": "模型 gpt-4o-mini 并发已达上限：候选 Key 均已达各自上限"}}`。

管理 API 的错误使用：

```json
{"detail": "错误信息"}
```

Anthropic Messages 错误可能使用 Anthropic 风格的 `type` 和 `error` 对象。
## 内置 WebUI 与运维 API

AMKR 自带一套可选的 WebUI，用于在浏览器中完成日常管理。**资产随二进制一起发布（`//go:embed`），没有单独的安装步骤，也没有额外依赖**；是否启用只由配置字段 `webui_enabled`（或启动参数 `--webui` / `--no-webui`）决定。

### 启用方式

```bash
# 启动时临时启用（同时写入配置文件）
amkr --config router-config.json --webui
# 显式关闭
amkr --config router-config.json --no-webui
```

也可以在 WebUI 的设置页切换，或调用 `POST /api/tool/webui`。

启用后访问：

```
http://127.0.0.1:8000/ui/
```

> WebUI 的挂载发生在服务进程启动时。通过 `POST /api/tool/webui` 修改开关会立即写入配置并让服务热重载配置，但 `/ui` 的挂载状态**要重启服务才会改变**。因此 `/health` 与 `/api/tool` 同时给出两个字段：`webui_enabled`（配置意图）与 `webui_mounted`（本进程实际状态）。

### 鉴权

`/ui/` 本身是静态资产，不需要鉴权；**它调用的管理接口都会照常校验本地鉴权 Key**。本地鉴权未启用时，管理接口对本机开放。

管理面的登录是**独立一页** `/ui/login.html`：未授权时 `index.html` 不渲染任何内容，直接整页跳转到登录页（当前地址作为 `?next=` 带上，验通后回到原本要去的页面），因此不存在"带着无效 Key 进入主界面"的入口。Key 保存在浏览器 `localStorage`（键名 `amkr.apiKey`），**绝不会出现在 URL 里**。会话中途失效（如重置了本地鉴权 Key）会带 `?reason=expired` 跳回登录页并说明原因；服务连不上时则保留已填的 Key 并给出"重试连接"，因为那可能只是服务还没起来。

四个页面各自的凭据面不同，不能互换：`index.html`（管理面）用本地鉴权 Key；`login.html` 是它的凭据入口；`panel.html`（工作空间面板）用工作空间的面板 key，走 URL fragment；`guest.html`（访客看板）用访问密钥，存在**独立键名** `amkr.guestAccessKey` 下——与 `amkr.apiKey` 分开，否则访客的访问密钥会覆盖管理员已登录的本地鉴权 Key。

`login.html` 的 `?next=` 只接受**同源相对路径**（拒绝 `//host` 与 `/\host` 这类协议相对地址），否则就是一个开放重定向：用户在本站输入真实 Key 之后被送去站外。

### `GET /api/workspaces`、`POST /api/workspaces`、`PUT|DELETE /api/workspaces/{workspace}`、`POST /api/workspaces/{workspace}/inference-key`、`PUT /api/workspaces/{workspace}/models`、`POST /api/workspaces/export|import`

工作空间自身的读/改/删、凭据轮换、模型授权与整包迁移。这些是管理面的正式资源，因此挂在 `/api` 之下（详见「任务路由接口」一节），而不是像价格目录那样挂 `/ui/`。全部只认完整权限——包括**空间自己的**面板 key 与推理 key：让被嵌入的面板给自己扩权，「只能读写这一个空间」这句承诺就没了。

### `GET /ui/workspace-panel.json`

供**嵌入第三方后台的独立面板页**（`/ui/panel.html`，见 [`USAGE.md` 9.3](USAGE.md#93-把工作空间面板嵌进你自己的后台) 与接入方指南 [`PANEL.md`](PANEL.md)）读取本空间的用量与流向。

**鉴权方式与其它端点不同**：它认的是**工作空间的面板 key**（配置里的
`workspaces.<空间>.api_key`），本地管理 key、访问密钥与推理 key 一律 `401`。生效时空间由 key
**钉死**，请求头 `X-AMKR-Workspace` 被忽略。

> 面板 key 与推理 key 是**两把不同的凭据**，不能互换：面板 key 只用于 `/api/tasks*` 与
> `/ui/workspace-panel.json`，推理 key 只用于 `/v1/*`（含 `/v1/models`）。用错会拿到
> `401`，两者都只认自己那一面。

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `hours` | number | `24` | `> 0` 且 `<= 8760` | 统计窗口 |
| `all_history` | boolean | `false` | — | 为真时忽略 `hours`，统计全部历史 |

参数校验同样**先于**鉴权（`hours=0` 不带凭据返回 `422` 而不是 `401`）。

响应与 `/ui/workspace-usage.json` 同源（同一个 `metrics.WorkspaceUsage`），另有两个差别：

```json
{
  "count_semantics": "upstream_attempt",
  "workspace": "teamA",
  "window": {"from": "…", "to": "…", "hours": 24},
  "workspaces": [{"name": "teamA", "stats": {"…": "…"}}],
  "unattributed": {"requests": 0, "total_tokens": 0},
  "models": ["gpt-4o-mini", "fast", "claude-sonnet-4"],
  "layers": ["workspace", "requested_model_id", "model_id", "provider_id", "key_name", "upstream_model_id"],
  "links": [{"source_layer": 0, "target_layer": 1, "source": "teamA", "target": "TASK_000001", "requests": 12, "total_tokens": 3400}]
}
```

- `workspace` 是这把 key 钉死的空间名，面板据此标注自己看的是谁。
- `unattributed` **恒为零**：没有归属的请求不属于任何一个空间，给面板看既没有意义也
  泄漏了别的空间的规模。
- `models` 是**模型 ID 与别名**的字符串数组（按配置顺序），与 `/v1/models` 同口径，供面板的
  任务表单选择；上游模型名不在其中——它只是"发给那个上游的名字"，面板不需要知道。

> 面板页与后台 WebUI **同源**，因此它的凭据走 URL fragment（`#k=…`）而**绝不**碰
> `localStorage`（那里存着后台的管理 key），也**绝不**发送 `X-AMKR-Workspace`。
> 三条都由 `web/src/standalone/panel/panel.test.tsx` 断言。

### `GET /ui/workspace-usage.json`

按工作空间拆分的用量读数与请求流向，供 WebUI 的「工作空间」页使用。**需要鉴权**（内容反映各空间的用量与模型流向，不是公开数据）。

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `hours` | number | `24` | `> 0` 且 `<= 8760` | 统计窗口 |
| `all_history` | boolean | `false` | — | 为真时忽略 `hours`，统计全部历史（此时 `window.from` 为 `null`） |

参数校验与 `/metrics` 同序：**先校验参数、后校验凭据**，因此 `hours=0` 不带凭据返回 `422` 而不是 `401`。

响应（`Content-Type: application/json`）：

```json
{
  "count_semantics": "upstream_attempt",
  "window": {"from": "2026-01-01T00:00:00+08:00", "to": "2026-01-02T00:00:00+08:00", "hours": 24},
  "workspaces": [
    {"name": "teamA", "stats": {"requests": 12, "successes": 12, "total_tokens": 3400, "...": "..."}}
  ],
  "unattributed": {"requests": 3, "total_tokens": 800, "...": "..."},
  "layers": ["workspace", "requested_model_id", "model_id", "provider_id", "key_name", "upstream_model_id"],
  "links": [
    {"source_layer": 0, "target_layer": 1, "source": "teamA", "target": "TASK_000001", "requests": 12, "total_tokens": 3400}
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `workspaces[].stats` | 与 `/metrics` 的 `total` 同形（同一套聚合口径） |
| `unattributed` | **没有**工作空间归属的请求（升级前的历史行、以及不走代理的写入路径） |
| `layers` | 流向图的层顺序，与 `links` 的 `source_layer` / `target_layer` 对应。六层依次是：工作空间 → 请求模型 → 实际模型 → 供应商 → **上游 Key** → 上游模型 |
| `links[].source` / `target` | 相邻两层之间的连边；`requests` 与 `total_tokens` 都给出，供前端切换宽度口径 |

关于 `unattributed`：**不会**被并进 `default`。把它算到默认工作空间头上会凭空造出一段并不存在的用量。工作空间归属从记录该字段的版本起才开始写入，升级前的历史行永远落在这里，不会追溯回填。

关于 `links`：某一端为空的请求（`provider_id` / `upstream_model_id` 可空）**不成边**，在图上留出缺口，而不是补一个占位节点——否则无法区分哪条是数据、哪条是兜底。`key_name`（上游 Key 名）恒非空，因此「供应商 → 上游 Key」这一段只在 `provider_id` 为空时缺边。

> 层级顺序里**必须**按层名定位而不是写死下标：Key 这一层是从「供应商 → 上游模型」中间插进去的，写死「第 4 段是上游模型」的取数会在插入新层后静默取到 Key 名（`webui_panel_probe.mjs` 有专门断言锁这条）。

> 该端点挂在 `/ui/` 之下而非新增 `/api/metrics/*`：它是本项目自有的响应形状（参照实现没有工作空间，没有可比对的 oracle），不混进 `/metrics` 系列。

### `GET /ui/access-key-usage.json`

**这把访问密钥自己**的用量，供访客看板 `/ui/guest.html` 使用。

**鉴权方式与其它端点都不同**：它认的是**访问密钥**（`amkr_ak_…`）本身，本地管理 key、工作空间的面板 key 与推理 key 一律 `401`。这与「访问密钥拿不到 `/metrics`」并不矛盾：`/metrics` 回的是**整台实例**的读数，是管理面信息；这里回的是**调用方自己的**流量，等同于把「你用了多少」还给调用方。

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `hours` | number | `24` | `> 0` 且 `<= 8760` | 统计窗口 |
| `all_history` | boolean | `false` | — | 为真时忽略 `hours`，统计全部历史 |

**没有 `key_id` 参数**：可见范围完全由凭据决定。加一个可传的标识，读代码的人就会以为换个值能看别人的 key。

参数校验同样**先于**鉴权（`hours=0` 不带凭据返回 `422` 而不是 `401`）。

```json
{
  "count_semantics": "upstream_attempt",
  "window": {"from": "2026-01-01T00:00:00+08:00", "to": "2026-01-02T00:00:00+08:00", "hours": 24},
  "access_key_id": "9f2c…",
  "access_key_name": "试用账号 A",
  "stats": {"requests": 4, "successes": 3, "total_tokens": 1500, "...": "..."},
  "dimensions": {
    "model_id": {"gpt-4o-mini": {"requests": 4, "total_tokens": 1500, "...": "..."}},
    "provider_id": {"openai-main": {"...": "..."}},
    "upstream_model_id": {"gpt-4o-mini-2024-07-18": {"...": "..."}}
  },
  "recent_requests": [
    {"created_at": "…", "model_id": "gpt-4o-mini", "provider_id": "openai-main",
     "upstream_model_id": "gpt-4o-mini-2024-07-18", "status_code": 200, "success": true,
     "retried": false, "prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
     "cached_tokens": 0, "first_token_ms": 20, "duration_ms": 120}
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `access_key_name` | 配置里的密钥名，供页面标注「看的是哪一把」。页面**不显示**明文 key |
| `stats` | 与 `/metrics` 的 `total` 同形（同一套聚合口径） |
| `dimensions` | 三个拆分维度，键名是**原始列名**，与 `/ui/workspace-usage.json` 的 `layers` 同一约定 |
| `recent_requests` | 最近调用明细，默认 50 条、上限 200 条 |

关于 `upstream_model_id`：成本估算必须按这一维分组。`model_id` 是调用方自取的**本地路由名**（可能叫 `gpt-4o` 而实际打到 `gpt-4o-2024-07-18`），它是唯一能与 models.dev 价格目录对上的字段。

**明文 key 在任何响应里都不出现**：新建与轮换是仅有的两个例外。这把 key 的持有者本来就知道自己的凭据，服务端没有理由再回显一次——看板会被投屏、截图、随手转发。

被停用的密钥回 `403`（`访问密钥已被停用`），与「凭据不认识」的 `401` 刻意区分：停用是可恢复的，认错凭据不是。

### `/ui` 读数的筛选维度参数

`/ui/key-usage.json`、`/ui/filtered-metrics.json`、`/ui/status-monitor.json` 与 `/ui/usage-breakdown.json` 四条读数共用同一组**筛选维度**参数（服务端同一份声明、存储层同一个 `metrics.Dimensions`），语义与校验完全一致：

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `provider_id` | string，**可重复** | 无 | 每一项 `1..512` 字符 | 转发到的供应商（`provider_id IN (…)`）。`provider_id` 为空的历史行不会被选中 |
| `access_key_id` | string，**可重复** | 无 | 每一项 `1..512` 字符 | **发起**请求的访问密钥（配置里的 key_id，不是明文 key），归属取自 `request_access_key` 旁挂表 |
| `model_id` | string，**可重复** | 无 | 每一项 `1..512` 字符 | **解析后的本地路由模型**（别名、任务名都已归一），不是请求里写的名字（那是 `requested_model_id`） |
| `upstream_key` | JSON 数组字符串，**可重复** | 无 | 每一项恰好是两个非空字符串的 JSON 数组 `["<provider_id>","<key_name>"]`，每部分 `1..512` 字符 | 实际转发用的上游 Key。必须**成对**：`key_name` 只在同一个供应商内唯一；多对之间是 OR（`(provider_id = ? AND key_name = ?) OR …`），**不是**「供应商 IN × Key 名 IN」的笛卡尔积 |
| `workspace` | string，**可重复** | 无 | 每一项 `1..512` 字符 | 工作空间归属（`request_workspace` 旁挂表）；没有归属的行不属于任何空间，不会被兜底算进 `default` |
| `caller_type` | string，**可重复** | 无 | 每一项 `local` \| `workspace` \| `access_key` | 调用方档位（`caller_type IN (…)`），取值域与 `/metrics/requests` 的 `caller_type` 相同 |
| `success` | boolean | 无（不筛） | Pydantic 宽松布尔（`true`/`false`/`1`/`0`/`yes`/`no`…） | 只看成功（`success=1`）或只看失败（`success=0`）；不给 = 不按成败筛 |

- **维度内 OR、维度间 AND**：同一参数重复出现是「这几个值之一」，不同参数同时出现是「几组条件同时成立」。全部都不给 = 全实例读数，此时 SQL 与没有这些参数时逐字节相同。
- **为什么 `upstream_key` 用 JSON 数组**：供应商 ID 与 Key 名都是配置里的任意字符串，任何分隔符（`/`、`:`、`|`）都可能出现在其中；JSON 字符串的转义规则现成且无歧义。URL 里记得转义：`?upstream_key=%5B%22openai%22%2C%22main%22%5D` 即 `["openai","main"]`。
- **校验先于鉴权、逐项校验**：任一项非法就回 `422`，`detail` 只有一条，`loc` 是 `["query", <参数名>]`，`input` 是**出错那一项**的原文。空串是 `string_too_short`、超长是 `string_too_long`、`caller_type` 越界是 `literal_error`（`ctx.expected` 为 `'local', 'workspace' or 'access_key'`）、`success` 不是布尔是 `bool_parsing`；`upstream_key` 形状不对（不是 JSON、不是数组、不是两项、元素不是字符串、有空串或超长）一律是 `value_error`，不带 `ctx`：

```json
{"detail": [{"type": "value_error", "loc": ["query", "upstream_key"],
  "msg": "Value error, upstream_key must be a JSON array of exactly two non-empty strings [\"<provider_id>\",\"<key_name>\"], each at most 512 characters",
  "input": "openai/main"}]}
```

- **与参照实现那几个单值过滤参数的关系**：`/metrics/requests` 与 `/metrics/series` 的 `model_id` / `caller_type` / `success` 等是**已发布的单值参数**（重复出现取末位、生成 `= ?`），与这里的多值维度互不相干；`/metrics` 系列不认上表任何一个多值参数（传了按未知参数忽略，响应逐字节不变）。

`/ui/filtered-metrics.json`、`/ui/status-monitor.json` 与 `/ui/usage-breakdown.json` 在响应的 `filters` 里**回显**这组条件（`/ui/key-usage.json` 没有 `filters` 字段）。键序固定，未设的列表维度是 `[]`，未设的 `success` 是 `null`（它是三态，`[]` 表达不了）；`upstream_keys` 回成对象数组，按名字取值更不容易读反：

```json
"filters": {
  "provider_ids": ["openai"], "access_key_ids": [], "model_ids": ["gpt-4o-mini"],
  "upstream_keys": [{"provider_id": "openai", "key_name": "main"}],
  "workspaces": ["teamA"], "caller_types": ["access_key", "local"], "success": null
}
```

### `GET /ui/key-usage.json`

按**上游 Key**、模型 × 上游 Key 与**访问密钥**拆分的用量，供 WebUI 的「用量统计」页使用。**只认完整权限**：内容反映全实例的 Key 使用情况（配置结构的投影），与 `/metrics` 同级；访问密钥与工作空间的面板 key / 推理 key 一律 `401`。

要「看自己那一把」的读数各有归属：访问密钥走 `/ui/access-key-usage.json`，工作空间面板走 `/ui/workspace-panel.json`。

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `hours` | number | `24` | `> 0` 且 `<= 8760` | 统计窗口 |
| `all_history` | boolean | `false` | — | 为真时忽略 `hours`，统计全部历史（此时 `window.from` 为 `null`） |
| `provider_id` / `access_key_id` / `model_id` / `upstream_key` / `workspace` / `caller_type` / `success` | — | 无 | 见[筛选维度参数](#ui-读数的筛选维度参数) | 可选：只统计符合这组条件的请求 |

维度之间是 **AND**：`?provider_id=openai&access_key_id=ak1` 是「ak1 打到 openai 的那些请求」。三份拆分与 `unattributed` 一并收窄——`upstream_keys` / `model_keys` 因此回答「这组条件都打到哪些上游 Key 上」，`unattributed` 则是「这组条件里没有上游归属的那些行」。全部参数都不传时与加它们之前完全一致。

参数校验与 `/metrics` 同序：**先校验参数、后校验凭据**，因此 `hours=0` 不带凭据返回 `422` 而不是 `401`。多值参数逐项校验：任一项非法就回 `422`，`input` 是**那一项**的原文（空串是 `string_too_short`，`input` 为 `""`）。

`access_key_id` 的语义是「谁**发起**的这次请求」（`request_access_key` 旁挂表），不是「用了哪把上游 Key」，两者互不嵌套：一把访问密钥的流量会打到多把上游 Key 上，反之亦然。完整权限与工作空间凭据写入的行没有访问密钥归属，任何一次筛选都不会把它们算进去。

```json
{
  "count_semantics": "upstream_attempt",
  "window": {"from": "2026-01-01T00:00:00+08:00", "to": "2026-01-02T00:00:00+08:00", "hours": 24},
  "upstream_keys": [
    {"provider_id": "openai", "key_name": "main", "stats": {"requests": 12, "total_tokens": 3400, "...": "..."}}
  ],
  "model_keys": [
    {"model_id": "gpt-4o-mini", "provider_id": "openai", "key_name": "main", "stats": {"...": "..."}}
  ],
  "access_keys": [
    {"access_key_id": "ak1", "access_key_name": "试用账号 A", "stats": {"...": "..."}}
  ],
  "unattributed": {"requests": 3, "total_tokens": 800, "...": "..."}
}
```

| 字段 | 说明 |
| --- | --- |
| `upstream_keys` | 按 `(provider_id, key_name)` 拆分，即转发时**实际用的那把上游 Key** |
| `model_keys` | 按 `(model_id, provider_id, key_name)` 拆分，给「模型 / 上游 Key」明细表用 |
| `access_keys` | 按 `access_key_id` 拆分，即**调用方**凭据（来自 `request_access_key` 旁挂表） |
| `access_keys[].access_key_name` | 配置里的密钥名；配置里已没有这把 key 时为 `null`（**不回填 id**——那会让「这把 key 还配着」看起来像真的） |
| `unattributed` | `provider_id` 为空的请求汇总（升级前的历史行等），进不了上面按 Key 的行 |

每组 `stats` 与 `/metrics` 的 `total` 同形（同一套聚合口径）。

拆分口径上有三点是刻意的：

- **三份拆分都是数组而不是嵌套对象。** 每行的身份是 2~3 个字段（供应商 + Key 名、模型 + 供应商 + Key 名），挑一个当外层键就会把另一维压成子项——那正是 `/metrics` 的 `keys`（真实模型 → Key 名）答不出「这一把 Key 一共出去了多少流量」的原因。数组顺序是身份字段的字典序，界面自己按列再排一次。
- **上游 Key 与访问密钥互不嵌套。** 一把访问密钥的流量会打到多把上游 Key 上，反之亦然，把两者套成一层会答不出其中任何一个。
- **`key_name` 只在同一个供应商内唯一。** 两家的同名 Key 是两把 Key，因此供应商必须进分组键；这也是本端点不能退化成「按 Key 名分组」的原因。

> 该端点挂在 `/ui/` 之下而非 `/metrics` 新增字段：`/metrics` 是**对照参照实现**的读数，形状已经发布、不能加字段（见「新增能力挂在 `/api/` 还是 `/ui/`」）。

### `GET /ui/filtered-metrics.json`

**按一组维度筛选**的整页读数：概览、趋势与最近明细三份**共用同一时间窗口**，供 WebUI 的概览 / 用量统计页做全局筛选时一次取完。

**只认完整权限**：内容是全实例读数按这组条件筛选的结果（配置结构的投影），与 `/metrics` 同级；访问密钥与工作空间的面板 key / 推理 key 一律 `401`。要「看自己那一把」的调用方请用 `/ui/access-key-usage.json`——那条只认访问密钥凭据，也不接受任何指定别人 key 的参数。

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `provider_id` / `access_key_id` / `model_id` / `upstream_key` / `workspace` / `caller_type` / `success` | — | 无 | 见[筛选维度参数](#ui-读数的筛选维度参数) | 七个筛选维度，全部可省 |
| `hours` | number | `24` | `> 0` 且 `<= 8760` | 统计窗口 |
| `all_history` | boolean | `false` | — | 为真时忽略 `hours`（`window.from` 与 `window.hours` 为 `null`） |
| `bucket_seconds` | integer | `0`（自动） | 显式给值时 `15..86400` | 趋势图的桶宽 |
| `limit` | integer | `0` | `0..200` | 明细条数；`0` 表示不要明细（`requests` 为 `null`） |

维度之间是 **AND**：`?provider_id=openai&provider_id=azure&access_key_id=ak1` 是「ak1 打到这两家的量」，`?model_id=gpt-4o&success=false` 是「gpt-4o 的失败请求」。维度**都可省**：都不给就是全实例读数（与 `/metrics` 系列等价，前端不会这么发）——这条路径必须留着，因为「不筛」与「筛一个不存在的值」是两件不同的事：前者回全量，后者回空。

参数校验与其它读数同序：**先校验参数、后校验凭据**，因此 `hours=0` 不带凭据返回 `422` 而不是 `401`。多值参数逐项校验：任一项非法（超 512 字符或空串）就回 `422`，`input` 是**那一项**的原文（空串是 `string_too_short`，`input` 为 `""`）。

响应（`Content-Type: application/json`）：

```json
{
  "count_semantics": "upstream_attempt",
  "window": {"from": "2026-01-01T00:00:00+08:00", "to": "2026-01-02T00:00:00+08:00", "hours": 24},
  "filters": {"provider_ids": ["openai", "azure"], "access_key_ids": ["ak1"], "model_ids": [],
              "upstream_keys": [], "workspaces": [], "caller_types": [], "success": null},
  "snapshot": {"count_semantics": "upstream_attempt", "window": {"...": "..."}, "started_at": "…",
               "database_path": "…", "rate_window_seconds": 60, "current_rpm": 2, "current_tpm": 300,
               "active_requests": 0, "total": {"requests": 2, "...": "..."},
               "caller_types": {"...": "..."}, "models": {"...": "..."}, "unattributed": {"...": "..."}},
  "series": {
    "bucket_seconds": 180,
    "points": [{"started_at": "…", "ended_at": "…", "complete": false, "requests": 2, "...": "..."}]
  },
  "requests": {
    "items": [{"id": 2, "created_at": "…", "model_id": "model-a", "...": "..."}],
    "next_before_id": null,
    "total_items": 2
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `snapshot` | 与 `GET /metrics` **完全同形**（含 `count_semantics` / `window` / `started_at` / `database_path` / `rate_window_seconds` / `current_rpm` / `current_tpm` / 九个分组），但全部分组、总量与 rate 窗口都收窄到这组条件 |
| `snapshot.active_requests` | **唯一的例外，刻意保持全局**：进程内的在途请求数，按定义无法按筛选条件拆分 |
| `series` | 点与 `GET /metrics/series` 同形；窗口与 `snapshot` 相同（同一个 `now`），因此各桶 `requests` 合计与 `snapshot.total.requests` 对得上 |
| `requests` | `limit=0` 时为 `null`（「这次没要明细」与「要了但一条都没有」要分得开）；否则 `items` 与 `GET /metrics/requests` 的条目同形，含服务端补的 `access_key_id` / `access_key_name` |
| `filters` | 回显筛选值本身（键序与取值见[筛选维度参数](#ui-读数的筛选维度参数)：未筛的列表维度是空数组，未筛的 `success` 是 `null`）。**不含**显示名——筛选值是调用方传进来的条件，页面上的筛选器本来就渲染着名字；`items` 里的 `access_key_name` 是另一回事（那条明细要直接画到请求流上） |
| `window` | 与 `/metrics` 同义：`all_history` 时 `from` 为 `null` |

几点刻意的取舍：

- **为什么不给 `/metrics` 加参数**：那三条路由是对照参照实现的读数，**响应形状与查询参数都已发布**（连响应里 `filters` 回显的键序都是发布字节）。给它们加 `access_key_id`、或者把 `provider_id` / `model_id` 从等值改成 `IN`，都改了契约；而这些多值筛选维度是本项目自有的能力，因此单独一条 `/ui` 路由，落在所有已发布清单之外。给 `/metrics` 传 `access_key_id` 会被当作未知参数**忽略**（与参照实现的 FastAPI 行为一致）。
- **同名参数重复出现 = 维度内多选**：单值参数在查询串里只能表达一个值，「这几家的量之和」用重复参数表达最直接（`?provider_id=a&provider_id=b`），服务端收成 `IN (?, …)`、值走参数绑定。其它参数一律保持 `FastAPI` 的「取末位」语义——那是已发布行为。
- **三份读数一次取完**：分三次请求会让「概览说 12 次、趋势图加起来 11 次」无法归因——窗口边界各差一点，或者两次请求之间又落进来一行。服务端一次定下 `now` 与窗口起点，三份取数共用。
- **`bucket_seconds` 默认自动**：`/metrics/series` 要求调用方自己按跨度挑桶（`hours=24` 配 60 秒桶是 1441 点，超过 500 的上限直接 `422`）。整页读数默认由服务端挑最小的合适桶，真实桶宽在响应里回显；显式给值时仍走 `/metrics/series` 那一套，点数超限回 `422`（`{"error":{"message":"time series cannot exceed 500 points"}}`），**不**静默换一个更粗的桶。
- **`all_history` 下的趋势**：`series` 没有「不限窗口」的形态（点是从起点零填充出来的），因此按**这组条件自己的**最早一条记录折算跨度选桶，而不是用全库跨度——否则趋势图开头会多出一大段与这组条件无关的空桶。
- **归属口径**：只认「谁**发起**的这次请求」（`request_access_key` 旁挂表）。完整权限、工作空间凭据写入的行以及升级前的历史行没有访问密钥归属，任何按 key 的筛选都不会把它们算进去（与 `/ui/access-key-usage.json` 同一口径）；按供应商筛选则只看 `provider_id` 列，没有供应商归因的行同样不会被算进去。

### `GET /ui/status-monitor.json`

**渠道健康矩阵**：概览、趋势与「渠道 × 时间片」的请求矩阵三份**共用同一时间窗口**，供 WebUI 的「状态监控」页。`/metrics` 的 `providers` / `upstream_models` 只给窗口内各维度的**合计**——一家合计看着还行、最近 10 分钟却全是 500 的渠道，在合计里只是成功率低了一格；这里把每个渠道在每个时间片里的请求数、成功/失败数、token 与耗时铺成一张表，异常是**看得见的格子**。

**只认完整权限**：矩阵暴露全实例每个渠道的健康状况（配置结构的投影），与 `/metrics` 同级；访问密钥与工作空间的面板 key / 推理 key 一律 `401`。

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `provider_id` / `access_key_id` / `model_id` / `upstream_key` / `workspace` / `caller_type` / `success` | — | 无 | 见[筛选维度参数](#ui-读数的筛选维度参数) | 七个筛选维度，全部可省 |
| `hours` | number | `24` | `> 0` 且 `<= 8760` | 统计窗口。这条读数**没有** `all_history`：矩阵把窗口切成桶，没有起点就没有列 |
| `bucket_seconds` | integer | `0`（自动） | 显式给值时 `15..86400` | 桶宽。自动时由服务端按窗口跨度从 `[30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400]` 里挑最小的、使列数 `ceil(hours*3600/bucket)+1` 不超过 180 的那个；梯级不够（窗口超过约半年）时按整天继续放大。显式值点数超上限回 `422`（`time series cannot exceed 500 points`），不静默换更粗的桶 |
| `group_by` | string | `provider` | `provider` \| `provider_model` \| `provider_key` | 矩阵的行身份：只按供应商 / 按「供应商 + 上游模型」/ 按「供应商 + 上游 Key」 |

筛选维度之间是 **AND**，都与 `/ui/filtered-metrics.json` 完全同义：`?provider_id=openai&provider_id=azure&access_key_id=ak1` 是「ak1 打到这两家的量」，且四份读数（含矩阵的行集合与 `failure_status_codes`）一并收窄；维度都可省，都不给就是全实例读数。`failure_status_codes` 自带「只看失败」这个条件，与 `success` 维度**并存**：`?success=true` 时它是 `{}`——成功的请求里本来就没有失败状态码。参数校验与其它读数同序：**先校验参数、后校验凭据**，因此 `hours=0` 不带凭据返回 `422` 而不是 `401`。

响应（`Content-Type: application/json`）：

```json
{
  "count_semantics": "upstream_attempt",
  "window": {"from": "2026-01-01T00:00:00+08:00", "to": "2026-01-02T00:00:00+08:00", "hours": 24},
  "filters": {"provider_ids": ["openai"], "access_key_ids": ["ak1"], "model_ids": [],
              "upstream_keys": [], "workspaces": [], "caller_types": [], "success": null},
  "group_by": "provider",
  "bucket_seconds": 600,
  "snapshot": {"total": {"requests": 12, "...": "..."}, "current_rpm": 2, "...": "..."},
  "failure_status_codes": {"200": 2, "500": 1, "none": 1},
  "series": {
    "bucket_seconds": 600,
    "points": [{"started_at": "2026-01-01T00:00:00+08:00", "ended_at": "2026-01-01T00:10:00+08:00",
                "complete": true, "requests": 3, "...": "..."}]
  },
  "matrix": {
    "group_by": "provider",
    "rows": [
      {"provider_id": "openai", "upstream_model_id": null, "key_name": null,
       "cells": [{"started_at": "2026-01-01T00:00:00+08:00", "ended_at": "2026-01-01T00:10:00+08:00",
                  "complete": true, "requests": 2, "successes": 1, "failures": 1,
                  "prompt_tokens": 340, "cached_tokens": 120,
                  "total_first_token_ms": 80, "total_duration_ms": 900}]},
      {"provider_id": null, "upstream_model_id": null, "key_name": null, "cells": ["..."]}
    ],
    "rows_truncated": 0
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `snapshot` | 与 `GET /metrics` **完全同形**（含 `count_semantics` / `window` / `started_at` / `database_path` / `rate_window_seconds` / `current_rpm` / `current_tpm` / 九个分组），但全部分组、总量与 rate 窗口都收窄到这组条件 |
| `failure_status_codes` | 同一窗口与筛选下，仅 `success=0` 的上游状态码计数；`"none"` 表示真正未收到状态码，空集合为 `{}`。`"200"` 等 2xx 失败单列为响应异常，不由包含成功请求的 `snapshot.total.status_codes` 相减推算。下游取消也可能记失败，不等同于上游故障；历史数据不猜具体原因 |
| `series` | 点与 `GET /metrics/series` **完全同形**（直接复用同一个取数函数），桶宽在 `bucket_seconds` 里回显 |
| `matrix.group_by` | 回显行身份的选择 |
| `matrix.rows` | 按 `(维度…, bucket)` 分组后的矩阵；**行按窗口内总 `requests` 降序**，同量时按身份字典序（`null` 排最前）。最多 60 行 |
| `matrix.rows_truncated` | 被 60 行上限截掉的行数（`0` 表示没有截断） |
| `matrix.rows[].provider_id` / `upstream_model_id` / `key_name` | 行身份三键**恒在**；不属于当前 `group_by` 的字段是 `null`（不是缺键）。`provider_id` 为 `null` 的行是**未归属**的历史行，界面显示「未归属」而不是丢弃它们 |
| `matrix.rows[].cells` | 与 `series.points` **逐列对齐**的定长数组；每个格子只含 `started_at` / `ended_at` / `complete` / `requests` / `successes` / `failures` / `prompt_tokens` / `cached_tokens` / `total_first_token_ms` / `total_duration_ms` 十个字段（平均耗时、缓存率、成功率都可由界面从这些原始计数算出，回 20 键完整字典只会让约一万个格子的载荷翻倍）。空桶给零值而不是缺格，尾部未走完的桶 `complete` 为 `false` |

几点刻意的取舍：

- **与 `/metrics`、`/ui/filtered-metrics.json` 的关系**：`snapshot` 与 `/metrics` 同形、`series` 与 `/metrics/series` 同形，都是已发布形状的直接复用；`matrix` 是本项目自有的口径（参照实现没有状态看板）。与 `/ui/filtered-metrics.json` 的区别在**问题**：那份回答「按这组条件筛选后整页读数长什么样」（含最近明细，可选 `all_history`），这份回答「哪些渠道在哪个时间片不健康」（恒定窗口、按渠道分组）。两者筛选语义与同窗口约定一致，因此不合并。
- **三份读数一次取完**：服务端一次定下 `now` 与窗口起点，`snapshot` / `series` / `matrix` 共用；窗口起点恰好落在桶边界上时，`series` 各桶 `requests` 合计与 `snapshot.total.requests` 相等，矩阵某一列的合计与 `series` 对应点位相同（在 60 行截断之前）；从 `now` 回推的滚动窗口通常不对齐，首桶会多看至多一个桶宽的窗口前记录，因此 KPI（取自 `snapshot`）与矩阵/趋势合计可能相差一个首桶的量（见下一条）。`series` 始终统计**全部行**（含被截断行与未归属行）——否则趋势图的总量会随着矩阵截断悄悄变小。
- **桶网格从桶边界起算**：`series` / `matrix` 的首桶向前对齐到桶边界（与 `/metrics/series` 的行为一致），因此非对齐窗口的首桶可能包含窗口起点前不到一个桶的记录，而 `snapshot` 严格从 `hours` 起算。只有当窗口起点正好落在桶边界上时两者才逐条相等——`hours` 与桶宽整除并不够（对齐按绝对时间算，例如 24 小时配 600 秒桶在 19:56 起算仍会错开 4 分钟）；线上看到的差异通常就是首桶那一点窗口前流量。
- **为什么挂在 `/ui/` 而不是给 `/metrics` 加字段**：`/metrics` 系列的响应形状与查询参数都已发布，渠道健康矩阵是本项目自有的读数，因此单独一条 `/ui` 路由，落在所有已发布清单之外（与 `/ui/filtered-metrics.json`、`/ui/key-usage.json` 同一条理由）。
- **非 GET 一律 `405`**（`Allow: GET`），与其它 `/ui` 读数一致。

### `GET /ui/metric-dimensions.json`

**筛选器选项**：时间窗口内指标库里**实际出现过**的各维度取值与各自请求数，供 WebUI 渲染筛选器的下拉选项。**只认完整权限**（选项本身就是全实例的配置投影），与 `/metrics` 同级；访问密钥与工作空间凭据一律 `401`。

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `hours` | number | `24` | `> 0` 且 `<= 8760` | 统计窗口 |
| `all_history` | boolean | `false` | — | 为真时忽略 `hours`（`window.from` 与 `window.hours` 为 `null`） |

**刻意不接受任何筛选维度参数**（传了按未知参数忽略）：选项必须是不筛的读数——若按当前条件收窄，选了「模型 A」以后其它模型就从列表里消失，想改成「A 或 B」时 B 已经不在了，筛选器会把自己锁死。参数校验先于鉴权（`hours=0` 不带凭据是 `422`）。

```json
{
  "window": {"from": "2026-01-01T00:00:00+08:00", "to": "2026-01-02T00:00:00+08:00", "hours": 24.0},
  "providers": [{"id": "openai", "requests": 120}, {"id": "azure", "requests": 30}],
  "models": [{"id": "gpt-4o-mini", "requests": 100}, {"id": "gpt-4o", "requests": 50}],
  "requested_models": [{"id": "fast", "requests": 90}, {"id": "gpt-4o", "requests": 60}],
  "upstream_keys": [{"provider_id": "openai", "key_name": "main", "requests": 110},
                    {"provider_id": "azure", "key_name": "main", "requests": 30}],
  "access_keys": [{"id": "ak1", "name": "试用账号 A", "requests": 40}, {"id": "ak9", "name": null, "requests": 2}],
  "workspaces": [{"name": "default", "requests": 140}, {"name": "teamA", "requests": 10}],
  "caller_types": [{"id": "local", "requests": 108}, {"id": "access_key", "requests": 42}, {"id": "workspace", "requests": 0}]
}
```

| 字段 | 说明 |
| --- | --- |
| 各列表的顺序 | 按 `requests` **降序**、同量按 id（`upstream_keys` 是 `(provider_id, key_name)`）**升序**：流量大的选项排前面，同量时顺序稳定 |
| `providers` / `upstream_keys` | 不含 `provider_id` 为空的历史行——那种行筛不出来（`IN (…)` 对 NULL 恒不成立），列成选项只会让人筛出一张空图。`upstream_keys` 成对给出，直接对应 `upstream_key` 参数 |
| `models` / `requested_models` | 分别对应 `model_id`（解析后的本地路由模型）与请求里写的模型名；后者目前只作展示，不是筛选参数 |
| `access_keys[].name` | 服务端按**当前配置**补的显示名；配置里已经没有（或没写名字）的 key 为 `null`，不回填 id |
| `workspaces` | 只列有归属的行（`request_workspace` 旁挂表）；键名是 `name` 而不是 `id`，与工作空间在配置里的写法一致 |
| `caller_types` | **恒列出** `local` / `workspace` / `access_key` 三档（没有流量的档给 `0`），取值域就是 `caller_type` 参数的取值域；库里残留的其它取值（已删除的 `visitor` 档）不列——它们不是合法的筛选值 |

一次 SQL 扫描（窗口内按全部维度列组合 `GROUP BY`，两张旁挂表 `LEFT JOIN`）后在服务端上卷成七份列表，不是七次扫描。非 GET 一律 `405`（`Allow: GET`）。

### `GET /ui/usage-breakdown.json`

**按一个维度拆分的趋势**：窗口内按 `group_by` 分组，取前 `limit` 组各自的总量与逐桶序列，其余并成 `other`，供 WebUI 画堆叠面积图与每行的迷你折线（替代一排各自独立的排行卡片）。**只认完整权限**，与 `/metrics` 同级；访问密钥与工作空间凭据一律 `401`。

| 参数 | 类型 | 默认 | 约束 | 说明 |
| --- | --- | --- | --- | --- |
| `provider_id` / `access_key_id` / `model_id` / `upstream_key` / `workspace` / `caller_type` / `success` | — | 无 | 见[筛选维度参数](#ui-读数的筛选维度参数) | 七个筛选维度，全部可省 |
| `hours` | number | `24` | `> 0` 且 `<= 8760` | 统计窗口 |
| `all_history` | boolean | `false` | — | 为真时忽略 `hours`；桶网格按**这组条件自己的**最早一条记录折算跨度（与 `/ui/filtered-metrics.json` 相同） |
| `bucket_seconds` | integer | `0`（自动） | 显式给值时 `15..86400` | 桶宽；自动与点数上限的规则与 `/ui/filtered-metrics.json` 完全相同（显式值超 500 点回 `422` `time series cannot exceed 500 points`） |
| `group_by` | string | `model` | 见下表 | 按哪个维度拆 |
| `rank_by` | string | `requests` | `requests` \| `tokens` | 按窗口内总请求数还是总 `total_tokens` 排名 |
| `limit` | integer | `8` | `1..50` | 保留前几组，其余并进 `other` |

| `group_by` | 分组列 | `key` 对象 |
| --- | --- | --- |
| `model` | `model_id`（解析后的本地路由模型） | `{"model_id"}` |
| `requested_model` | `requested_model_id`（请求里写的名字） | `{"requested_model_id"}` |
| `provider` | `provider_id`；`null` = 未归属的历史行 | `{"provider_id"}` |
| `upstream_model` | `(provider_id, upstream_model_id)`，**不要求** `pool_name` | `{"provider_id", "upstream_model_id"}` |
| `upstream_key` | `(provider_id, key_name)` | `{"provider_id", "key_name"}` |
| `access_key` | `request_access_key.access_key_id`；`null` = 不是访问密钥发起的请求 | `{"access_key_id", "access_key_name"}`（名字由服务端按配置补，未知或 `null` id 时为 `null`） |
| `workspace` | `request_workspace.workspace`；`null` = 没有工作空间归属 | `{"workspace"}` |
| `caller_type` | `caller_type` | `{"caller_type"}` |
| `status` | `status_code`（整数）；`null` = 没拿到上游响应 | `{"status_code"}` |
| `api_format` | `request_shape.api_format`（入站 API 路径）；`null` = 没有形态记录 | `{"api_format"}` |

响应（`Content-Type: application/json`）：

```json
{
  "count_semantics": "upstream_attempt",
  "window": {"from": "2026-01-01T00:00:00+08:00", "to": "2026-01-02T00:00:00+08:00", "hours": 24.0},
  "group_by": "upstream_model",
  "rank_by": "requests",
  "bucket_seconds": 180,
  "filters": {"provider_ids": [], "access_key_ids": [], "model_ids": [], "upstream_keys": [],
              "workspaces": [], "caller_types": [], "success": null},
  "buckets": [{"started_at": "2026-01-01T00:00:00+08:00", "complete": true},
              {"started_at": "2026-01-01T00:03:00+08:00", "complete": true}, "…",
              {"started_at": "2026-01-02T00:00:00+08:00", "complete": false}],
  "groups": [
    {"key": {"provider_id": "openai", "upstream_model_id": "gpt-4o-mini"},
     "stats": {"requests": 100, "successes": 98, "failures": 2, "total_tokens": 52000, "...": "...",
               "status_codes": {"200": 98, "429": 2}},
     "series": {"requests": [3, 0, "…", 1], "successes": [3, 0, "…", 1], "failures": [0, 0, "…", 0],
                "retries": [0, 0, "…", 0], "prompt_tokens": [900, 0, "…", 300],
                "completion_tokens": [120, 0, "…", 40], "total_tokens": [1020, 0, "…", 340],
                "cached_tokens": [0, 0, "…", 0], "duration_ms": [2400, 0, "…", 700],
                "first_token_ms": [600, 0, "…", 150]}}
  ],
  "other": {"group_count": 3, "stats": {"requests": 12, "...": "..."}, "series": {"requests": [0, 1, "…", 0], "...": "..."}},
  "total_groups": 4
}
```

| 字段 | 说明 |
| --- | --- |
| `buckets` | 桶网格：与同一组参数下 `/ui/filtered-metrics.json` 的 `series.points` **逐个对应**（同一个 `started_at`、同一个 `complete`，同一套首桶对齐与零填充）；最后一个还在填充的桶 `complete` 为 `false` |
| `groups` | 前 `limit` 组，按 `rank_by` 降序；同量时按 `key` 升序决胜（逐列比较，`null` 排最前，`status_code` 按数值） |
| `groups[].stats` | 与 `/metrics` 各分组同形（`UsageStats` 的 20 个键，含 `status_codes`） |
| `groups[].series` | 十条与 `buckets` **等长**的稠密数组，空桶给 `0`。给的是**合计**：`duration_ms` / `first_token_ms` 是该桶耗时之和而不是平均——平均值不能跨桶、跨组相加，界面要画「按请求数加权的平均耗时」只能从合计除出来 |
| `other` | 被截掉的那些组并成一份（`stats` 逐组折叠、`status_codes` 逐码相加，`series` 逐桶相加），`group_count` 是并进来的组数；没有被截掉的组时为 `null` |
| `total_groups` | 截断之前的组数 |
| `filters` | 回显筛选条件，与 `/ui/filtered-metrics.json` 同形 |
| `window` | 与 `/ui/filtered-metrics.json` 同义：`from` 是 `now - hours`（`all_history` 时为 `null`） |

几点刻意的取舍：

- **与 `/ui/filtered-metrics.json` 逐桶可加**：两条读数用同一个函数定桶网格、用同一套筛选条件取数，因此同一组参数下，全部 `groups` 与 `other` 的序列逐桶相加**等于** `series.points` 对应点位。`stats` 与 `series` 都覆盖整张桶网格（从 `buckets[0].started_at` 起），所以每组 `stats.requests` 恒等于它 `series.requests` 的合计；首桶向前对齐到桶边界，因此可能比 `window.from` 多看不到一个桶宽的记录——与 `filtered-metrics` 的 `series` 完全同一口径（`snapshot` 则严格从 `window.from` 起算）。
- **`upstream_model` 不看 `pool_name`**：`/metrics` 的 `upstream_models` / `providers` 是参照实现的口径，要求 `provider_id`、`pool_name`、`upstream_model_id` 三列全非空；v4 配置下写入的 `pool_name` 恒为 `NULL`，于是那两个分组在新数据上**永远是空的**，按上游模型算成本无从下手。那两个分组是已发布形状、口径不能改，因此在这条新读数上按 `(provider_id, upstream_model_id)` 分组修掉这个缺口。
- **没有归属的行自成一组**（`null` 键）而不是被丢掉：否则各组加起来对不上总量，缺的那一截没有出处。
- **一次 SQL 扫描**：按「分组列 + 桶 + `status_code`」`GROUP BY` 一次，在服务端折成每组总量与每组每桶的格子；`status_code` 进分组键是为了让 `stats.status_codes` 不必再扫一遍。
- **参数校验先于鉴权**；`group_by` / `rank_by` 越界是 `literal_error`，`limit` 越界是 `greater_than_equal` / `less_than_equal`。非 GET 一律 `405`（`Allow: GET`）。

### `GET /ui/pricing.json`

返回 models.dev 价格目录的服务端缓存快照，供 WebUI 估算成本。**不需要鉴权**：内容是 models.dev 的公开数据，与 `/ui/` 下的静态资产同级。

价格目录由服务进程在启动时取回一次、此后每 6 小时复验一次（条件请求，命中时是 `304`，不重复传 230 KB）。它**只驻内存、不落盘**；取回失败不会清空已有目录，而是在载荷的 `error` 字段里说明，让界面显示"旧价格 + 告警"。

| 参数 | 说明 |
| --- | --- |
| 无 | 不接受任何查询参数 |

响应（`Content-Type: application/json`，并带 `ETag` 与 `Cache-Control: public, must-revalidate, max-age=0`）：

```json
{
  "version": 1,
  "source": "https://models.dev/api.json",
  "updated_at": "2026-01-02T03:04:05Z",
  "error": null,
  "models": {
    "gpt-4o": { "input": 2.5, "output": 10, "cache_read": 1.25 },
    "claude-sonnet-4-5": { "input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75 }
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `version` | 整数 | 载荷版本，字段形状有破坏性变化时递增 |
| `source` | 字符串 | 价格来源，固定为 models.dev 的 `api.json` |
| `updated_at` | 字符串 \| null | 目录取回时间（RFC3339 UTC）；从未成功取回时为 `null` |
| `error` | 字符串 \| null | 最近一次刷新失败的原因；非 `null` 表示当前价格是旧的 |
| `models` | 对象 | 小写模型 id → 单价，单位是 **USD / 100 万 token** |

`models` 里的条目只保证有 `input` 与 `output`；`cache_read` / `cache_write` **缺失即不存在**（不会补 `0`），客户端应回退到 `input` 价而不是当成免费。

同一模型 id 在 models.dev 上可能被多家供应商列出，服务端已按"排除 `input`/`output` 全为 `0` 的挂名条目后取最便宜"收敛成一条。这一步不能省：实测有数百个模型被部分供应商标成全 `0`，不做排除会让成本页显示一张全零的假账。

状态码：

| 状态码 | 场景 |
| --- | --- |
| `200` | 返回目录快照 |
| `304` | 请求带 `If-None-Match` 且与当前 `ETag` 一致 |
| `405` | 非 `GET` / `HEAD` |
| `503` | 目录尚不可用（服务刚启动、或一直取不到）。**不返回空目录**——那会让客户端把每个模型当成免费 |
| `404` | `webui_enabled` 未启用时，该路由与 `/ui/` 一起不存在 |

> 价格目录挂在 `/ui/` 前缀下，因此与 WebUI 同生共死：`webui_enabled` 为假时它也不可达。成本估算是 WebUI 的读数，没有界面时无人消费。

### `GET /ui/model-kinds.json`

按名字回答「这个模型是什么类型」（文本 / 图像 / 视频 / 语音合成 / 语音识别 / 嵌入 / 重排），供 WebUI 把供应商页的服务模型卡片按类型分组。**不需要鉴权**：证据来自 models.dev 的公开数据，与价格目录同级。

分类是**派生读数**（与价格同类）：它只用于显示，**不参与鉴权或路由判定**——`/v1/models` 的成员、某个模型能不能被调用，都由配置与绑定决定，与这里答什么无关。上游也不会告诉我们答案：OpenAI 兼容的 `/v1/models` 只有 `id` / `object` / `created` / `owned_by`，Anthropic 的 `type` 恒为 `"model"`（那是对象类型，不是模型类型）。因此这里给出的每一档都必须能指出**证据**。

判定分两层取**并集**（不是后者覆盖前者）：

| 层 | 依据 |
| --- | --- |
| 1 | models.dev 目录里该模型的 `modalities`（与价格来自同一份 `api.json`、同一次取回） |
| 2 | 名字规则（`-image` / `-tts` / `whisper` / `embedding` / `rerank` 等），**按固定顺序匹配，先命中者先算** |
| 3 | 都没有 → **未分类**：`kinds` 为空、`primary` 为 `"unknown"` |

第 3 层是**合法结果**而不是失败：自建网关、中转站改名的模型必然落到这里（上游从来没有告诉过我们它们是什么）。界面必须把它显示成「未分类」，而不是默认当成文本模型——那是在猜。

`endpoints` 是**推导值**：类型 → 端点族（`text→chat`、`image→images`、`tts→speech`、`stt→transcriptions`、`embedding→embeddings`、`video→video`、`rerank→rerank`）。它回答的是「这一档类型通常走哪个端点」，**不是可用性结论**：例如 models.dev 把 `gpt-4o` 的输入模态标为含 `audio`，于是它带上 `stt`（它确实能处理语音输入），但 `/v1/audio/transcriptions` 并不一定接受它。要判断某个模型走哪个端点，仍然以探测结果与配置里的绑定为准。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | 字符串 | 是 | 要查询的模型名，可重复（`?model=a&model=b`）。空值被忽略、重复值只算一次；单次最多 200 个，单个名字上限 512 字节 |

响应（`Content-Type: application/json`）：

```json
{
  "version": 1,
  "source": "https://models.dev/api.json",
  "catalog_available": true,
  "models": {
    "gpt-image-1": {
      "kinds": ["image"],
      "primary": "image",
      "endpoints": ["images"],
      "modalities": { "input": ["text"], "output": ["image"] },
      "grounds": [
        { "source": "catalog", "detail": "gpt-image-1", "kinds": ["image"] },
        { "source": "rule", "detail": "image", "kinds": ["image"] }
      ]
    },
    "my-gateway-renamed": { "kinds": [], "primary": "unknown", "endpoints": [], "grounds": [] }
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `version` | 整数 | 载荷版本，字段形状有破坏性变化时递增 |
| `source` | 字符串 | 证据来源，与 `/ui/pricing.json` 是同一份 models.dev `api.json` |
| `catalog_available` | 布尔 | 服务端是否已持有 models.dev 目录；为 `false` 时结论只来自名字规则 |
| `models` | 对象 | **请求里的原始模型名** → 判定。键带前缀/日期后缀也照原样回，去前缀与去日期是服务端内部的匹配手段 |

每个判定的字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `kinds` | 字符串数组 | 该模型的全部类型，顺序固定（text / image / video / tts / stt / embedding / rerank）；空数组表示未分类 |
| `primary` | 字符串 | 供**分组显示**用的单一类型。多能力模型（如 `gpt-4o-audio-preview` 同时是 tts 与 stt）也只归一类，否则同一个模型会在列表里出现两次，而它们背后是同一个绑定 |
| `endpoints` | 字符串数组 | 由类型推导的端点族 |
| `modalities` | 对象 \| 缺失 | 目录记录的输入/输出模态；**没有证据时不出现**（而不是空数组——空数组会被读成"上游说它没有模态"） |
| `grounds` | 数组 | 每条证据的 `source`（`catalog` / `rule`）与它给出的类型。界面据此解释「这个标签是谁给的」：只有名字规则命中的结论该让用户有机会改 |

状态码：

| 状态码 | 场景 |
| --- | --- |
| `200` | 返回判定。**目录不可用时也是 `200`**（`catalog_available: false`） |
| `400` | 超过 200 个名字，或单个名字超过 512 字节 |
| `405` | 非 `GET` / `HEAD` |
| `404` | `webui_enabled` 未启用时，该路由与 `/ui/` 一起不存在 |

> 与 `/ui/pricing.json` 的 `503` **刻意不同**：没有价格时算出来的成本是错的（会把一切算成免费），必须响亮失败；而没有目录时名字规则仍然给出有价值的判定，只是精度下降。把"精度下降"报成 503 会让界面在服务刚启动的那几秒里完全没有类型可用。

> 单次查询上限 200 个名字，是因为它服务的是「渲染前问一句」这个用法；WebUI 自己按 100 个一批发（见 `web/src/lib/model-kinds.ts` 的 `chunkModelIDs`），留出三倍余量给 URL 编码后的长度。

### 成本估算口径

金额是**派生读数，不落库**：指标库只存 token 用量，成本由 WebUI 按当前目录现算。因此目录更新后，历史请求的估算金额会跟着变——单价是外部事实，不是本项目的记账结果。

按 token **类别**分别计价（单位 USD / 100 万 token）：

| 类别 | token 字段 | 单价 |
| --- | --- | --- |
| 普通输入 | `prompt_tokens` 减去下面两类缓存量 | `input` |
| 缓存读 | `cache_read_input_tokens`，为 `0` 时退回 `cached_tokens` | `cache_read`，缺失时回退 `input` |
| 缓存写 | `cache_creation_input_tokens` | `cache_write`，缺失时回退 `input` |
| 输出 | `completion_tokens` | `output` |

不能直接用 `uncached_prompt_tokens` 乘 `input`：对 Anthropic，`prompt_tokens = input_tokens + cache_read + cache_creation`，而 `uncached_prompt_tokens` 等于 `input + cache_creation`，拿它乘输入价会把缓存写计费两次——一次按输入价、一次按 `cache_write`。

匹配按 `upstream_model` 名称（大小写不敏感），依次尝试：原名 → 去掉 `vendor/` 前缀 → 去掉 `-YYYY-MM-DD` / `-YYYYMMDD` 日期后缀。匹配不到时成本显示 `—`，**不显示 `$0`**；有部分条目未匹配时，界面会明确标出"x/y 项有定价"，避免局部金额被读成完整账单。

> 当前实现使用目录里的**基础单价**，忽略 models.dev 的阶梯定价（`cost.tiers`，如超长上下文加价，约 460 个模型带此字段）；也不做汇率换算，金额固定是 USD。

### `GET /api/logs`

读取日志文件尾部，用于 WebUI 的日志面板。

日志文件是 `log_file_path`（默认 `<配置目录>/server.log`），由服务进程自己写入：应用侧日志、每个 HTTP 请求的访问日志，以及启动/关停等服务器日志都会追加到这里，因此面板在服务正常运行、没有任何报错时也会有内容。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `tail` | 整数 | 否 | 读取的字节数，默认 65536（64 KiB），上限 1048576 |

响应：

```json
{
  "text": "最后若干行日志……",
  "truncated": true,
  "path": "C:/Users/me/AppData/Local/.../server.log",
  "error": null
}
```

日志文件不存在或不可读时返回 `200`，`text` 为空、`error` 为具体原因，便于前端区分「没有日志」与「读取失败」。

### `GET /api/tool`

汇总 CLI 的版本检查与 WebUI 状态，等价于 WebUI 的「设置 → 版本」卡片。

```json
{
  "version": "4.0.2",
  "latest_version": "4.0.2",
  "update_available": false,
  "release_url": null,
  "source": "pypi",
  "error": null,
  "webui_available": true,
  "webui_enabled": true,
  "webui_mounted": true,
  "webui_path": "/ui"
}
```

版本检查使用短超时并复用 CLI 的实现；网络失败不会让接口报错，而是把原因放进 `error`。

### `POST /api/tool/webui`

```json
{"enabled": true}
```

写入 `webui_enabled` 并热重载配置，响应为更新后的 WebUI 状态（字段同 `GET /api/tool` 的 `webui_*` 部分）。

### `POST /api/service/{action}`

在本机执行服务生命周期动作，响应体包含人类可读的执行输出：

```json
{"action": "restart_amkr", "text": "……命令输出……"}
```

可用动作：

| 动作 | 说明 |
| --- | --- |
| `start_amkr` / `stop_amkr` / `restart_amkr` / `status_amkr` | 后台服务控制 |
| `install_user_amkr` / `uninstall_amkr` | 当前用户登录自启 |
| `install_system_amkr` / `uninstall_system_amkr` | 系统级服务（需要管理员授权） |
| `start_system_amkr` / `stop_system_amkr` / `restart_system_amkr` | 系统级服务控制 |

未知动作返回 `422`。

### `GET /api/integrations`

查询三个受支持客户端的接管状态。每一项都是独立的，**单个 Agent 读取失败只会让该项带 `error`，不影响其他两项**：

```json
{
  "integrations": [
    {
      "agent": "claude-code",
      "display_name": "Claude Code",
      "target_path": "C:/Users/me/.claude/settings.json",
      "target_exists": true,
      "backup_available": false,
      "current_is_applied": true,
      "error": null
    }
  ]
}
```

### `POST /api/integrations/{agent}`

```json
{}
```

`agent` 取 `claude-code`、`codex`、`pi-agent`。接管时会写入该客户端的路由地址、本地鉴权
key，以及 AMKR **当前可用的全部模型**（至少有一把启用 Key 的模型 ID 与别名，外加已配置
的 `unified-model`）。请求体可省略。

历史上这里用 `{"mode": "unified-model"}` / `{"mode": "native"}` 选择路由模式；该字段
**已废弃且被忽略**（仍被接受，旧脚本不会报错），集成不再有模式之分。

写入前会先备份原配置，未启用本地鉴权时返回 `409`。Codex 在没有任何可用模型时返回 `409`
（它的模型目录文件不允许为空，空目录会让 Codex 启动失败）。

### `POST /api/integrations/{agent}/rollback`

从备份恢复该 Agent 的原配置；没有备份时返回 `409`。

## 嵌入宿主

AMKR 的装配入口是 `internal/server` 的 `Options` 与 `New`（没有单独的 `mount_app`
层）。`internal/server` 是 Go 的内部包，因此嵌入只在**本模块内**可用（例如
`cmd/amkr`）；外部程序请把 AMKR 当成独立进程用。

```go
app, err := server.New(server.Options{
    ConfigPath: "router-config.json",
    Config:     cfg,
    // 嵌入到宿主的某个前缀下时给出；独立运行时留空。
    MountPrefix: "/amkr",
})
if err != nil { /* ... */ }
hostMux.Handle("/amkr/", http.StripPrefix("/amkr", app.Handler()))
```

挂载后的路径：

| 独立运行 | 挂载到 `/amkr` 后 |
| --- | --- |
| `POST /v1/chat/completions` | `POST /amkr/v1/chat/completions` |
| `GET /health` | `GET /amkr/health` |
| `GET /ui/` | `GET /amkr/ui/` |
| `GET /api/settings` | `GET /amkr/api/settings` |
| `WS /ws/events` | `WS /amkr/ws/events` |

`MountPrefix` 只影响报出的路径（`/health` 的 `webui_path`）与 WebUI、价格目录、自更新
入口这些挂在 `/ui/` 之下的路由；它**不会**改写管理 API 的 `/api` 前缀。

### 嵌入时的行为差异

配置里的 `ops_enabled` 决定运维接口（`/api/logs`、`/api/service/*`、
`/api/integrations/*`、`/api/tool`）是否注册。这些接口作用于「服务所在的这台机器」——
启停后台进程、注册系统服务、改写 Claude Code / Codex 的本地配置——嵌入到别人的进程里
语义不成立，因此独立部署想整体关掉运维面时用 `amkr --no-ops`，比逐个路径拉黑可靠；
关闭后 `/health` 的 `ops_enabled` 为 `false`。

其余接口（代理、`/health`、`/metrics`、WebSocket、`/api/settings` 等配置管理接口）
在挂载下与独立运行完全一致。

### 配置持久化

管理接口写配置要求已知配置文件路径。`Options.ConfigPath` 为空时，`GET /api/settings`
等读写接口会返回 `409`：

> 传入 `ConfigPath` 即可获得完整的管理能力。

WebUI 的前端会根据当前页面路径（`.../ui/`）自动推导 API 基址，因此挂载到任意前缀下
都不需要额外配置。

### 复用宿主的身份体系（可插拔鉴权）

嵌入时宿主通常已经有自己的身份认证（session cookie、JWT、网关注入的身份头）。默认的
「本地 API key」在这种场景下很别扭：把 `local_api_key` 留空等于**整体关闭鉴权**，否则
就得让调用方额外再持有一套 AMKR 的 key。

`Options.Authorizer` 可以整体替换鉴权判定：

```go
app, err := server.New(server.Options{
    ConfigPath: "router-config.json",
    Config:     cfg,
    Authorizer: func(r *http.Request, localAPIKey string) *auth.Context {
        if hostUser(r) == nil {              // 宿主的身份体系
            return nil                       // nil = 拒绝，返回 401
        }
        return &auth.Context{Mode: auth.ModeFull}
    },
})
```

`auth.Context` 只有**一档**权限：

| 模式 | 权限 |
| --- | --- |
| `full` | 全部接口，全部模型 |

访客档已随访问密钥的引入整体删除：受限凭据不是「权限更小的 full」，而是由各自的清单在
**代理层**逐项判定（访问密钥看 `providers` / `models`，工作空间推理 key 看空间与
`workspaces.<空间>.models`），一个枚举表达不了。因此 `auth` 包里只剩 `ModeFull` 与
`IsFull()`，没有 `auth.Visitor()` 这样的构造函数——宿主需要「部分权限」时，正确做法是在
自己的钩子里判定，而不是把它塞回 AMKR 的权限枚举。

受限凭据的解析**不在 `internal/auth`**：那包是不依赖 `config` 的纯函数，而这两条通道要读
配置里的清单，因此 AMKR 自己在 `internal/proxy` 的 `authorize` 与 app 面（`/v1/models`）
里解析——顺序是「完整权限 → 访问密钥 → 工作空间推理 key」，本地主凭据必须**先**判，否则
一把受限 key 可能被当成管理员凭据用。被停用的访问密钥此时回报 `403` 与原因，而不是当成
错误凭据。

> **注入自定义 `Authorizer` 时的已知取舍**：宿主接管了整条判定链，因此**访问密钥与工作空间
> 推理 key 都不再被识别**（`internal/server` 不会把它们透传给宿主的钩子）。这是有意的——
> 宿主既然自带身份体系，AMKR 就不该在旁边再开两条它管不到的通道。需要这两条通道时不要注入
> `Authorizer`。

同时覆盖 WebSocket：`/ws/events` 只对 `full` 开放。WebSocket 握手无法携带自定义头，所以
AMKR 的凭据只能走首帧 `{"type":"auth","token":"..."}`；该 token 会被折算成
`Authorization: Bearer <token>` 后交给同一个钩子。宿主的 cookie 在握手头中，因此用
cookie / session 鉴权时握手即可通过，无需处理首帧。钩子拒绝时连接以 `4003` 关闭。

未传 `Authorizer` 时走默认实现：只有本地 API key 给完整权限，之后 AMKR 继续查访问密钥与
工作空间推理 key 两条通道（`local_api_key` 为空表示鉴权整体关闭，一律按完整权限处理）。
