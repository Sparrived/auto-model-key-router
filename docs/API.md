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
| `GET/PUT` | `/api/settings` | 仅本地 | 查询或更新监听、超时和重试设置 |
| `POST` | `/api/settings/local-api-key` | 仅本地 | 重置本地鉴权 Key；新 Key 仅在本次响应返回 |
| `POST` | `/api/update/check` | 仅本地 | 复用 CLI 的 GitHub Releases 版本检查 |
| `POST` | `/api/probes/keys` | 仅本地 | 异步探测指定 Provider 下 Key 的模型列表与各端点可用性（逐 Key 兼容接口） |
| `GET` | `/api/probes/{probe_id}` | 仅本地 | 查询探测进度和结果 |
| `POST` | `/api/probes/{probe_id}/cancel` | 仅本地 | 取消探测 |
| `POST` | `/api/config/export`、`/api/config/import` | 仅本地 | 导出或导入可迁移配置 |
| `GET` | `/ui/` | 无 | 内置 WebUI（需 `webui_enabled`，未启用或资产缺失时返回 `404`） |
| `GET` | `/ui/pricing.json` | 无 | models.dev 价格目录快照，用于 WebUI 估算成本（需 `webui_enabled`） |
| `GET` | `/ui/workspace-usage.json` | 仅本地 | 按工作空间拆分的用量读数与请求流向，供 WebUI 的「工作空间」页（需 `webui_enabled`） |
| `GET` | `/ui/access-key-usage.json` | 访问密钥 | 只回**这把**访问密钥自己的用量、按模型/供应商/上游模型的拆分与最近调用，供访客看板 `/ui/guest.html`（需 `webui_enabled`） |
| `GET` | `/ui/update/status` | 无 | 报告本构建是否具备自更新能力及当前版本，供 WebUI 决定是否显示「立即更新」 |
| `POST` | `/ui/update/apply` | 仅本地 | 执行自更新：下载并校验新版、就地替换、启动收尾助手重启服务 |
| `GET` | `/api/logs` | 仅本地 | 读取日志文件尾部（默认最后 64 KiB） |
| `GET` | `/api/tool` | 仅本地 | 查询版本、可用更新与 WebUI 状态 |
| `POST` | `/api/tool/webui` | 仅本地 | 启用或关闭 WebUI（写入配置，需重启服务生效） |
| `POST` | `/api/service/{action}` | 仅本地 | 执行服务的启动/停止/重启/注册/注销 |
| `GET` | `/api/integrations` | 仅本地 | 查询 Claude Code / Codex / Pi Agent 的集成状态 |
| `POST` | `/api/integrations/{agent}` | 仅本地 | 以 `unified-model` 或 `native` 模式接管该 Agent 配置 |
| `POST` | `/api/integrations/{agent}/rollback` | 仅本地 | 回退该 Agent 到备份配置 |

### 新增能力挂在 `/api/` 还是 `/ui/`

管理面的 47 条与运维面的 7 条路由列在 `routePatterns()` / `opsRoutePatterns()` 里，它们是
**已发布接口**的清单，不能随意增减：这些响应的形状已经对外承诺，改动会破坏既有调用方。
新增能力因此分两类：

- **管理面的正式资源**（工作空间自身的读/改/删与整包迁移、访问密钥、CPA 实例与账号资源）
  注册在 `/api` 之下，但列在**另一份**清单（`workspacePatterns()`）里。它们没有历史版本可
  对照，塞进那 47 条会让「这 47 条就是已发布行为」这句话失去意义。两批注册在同一棵 mux 上，
  因此错方法的 `405` / `Allow` 判定要同时看两份清单。
- **本项目自有的读数**（价格目录 `/ui/pricing.json`、自更新入口、工作空间用量
  `/ui/workspace-usage.json`、访问密钥用量 `/ui/access-key-usage.json`）挂在 `/ui/` 前缀下：
  它们与 `/api` 面在语义上不连续，挂 `/ui/` 既落在那份已发布清单之外，也让「不开 WebUI
  就没有这些读数」顺理成章。

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
| `model` | string | 是（`/v1/decide` 与 `/v1/classify` 除外） | 模型 ID、模型别名、`unified-model`、任务名（`TASK_XXXXXX`）或 `模型[Key名称]`。上游模型名（各 target 的 `upstream_model`）**不是**可调用名 |
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

所有通用代理请求仍需在 JSON 请求体中提供 `model`。缺少该字段会返回 `400`。

`POST /v1/embeddings` 不做协议转换：请求体本来就是 OpenAI 形状（`model`、`input`、`encoding_format` 等），AMKR 只替换 `model` 为上游真实模型名后原样转发，因此 `input` 不会被改写成 chat 的 `messages`。上游路径默认 `v1/embeddings`，可按上游 URL 配置 `upstream_routes[base_url].embeddings` 覆盖（别名 `embedding` / `embed`），例如 `"embeddings": "gateway/embed"` 会转发到 `base_url/gateway/embed/v1/embeddings`。请求 `unified-model` 时使用 `unified_model.embeddings` 计划；未配置该计划则继承 `default.primary`。

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
| `unified_model` | object/null | 当前真实模型和可选固定 Key；含 `default` 与可选的 `image` / `embeddings` 计划 |
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
      "user_agent": "claude-cli/1.0"
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

字段与 ModelCreate 的模型字段相同，全部可省略，但请求中至少需要出现一个字段。`id`、`aliases`、`routing_mode` 不能为 `null`（`aliases: []` 表示清空别名）；`reasoning_effort: null` 用于清除模型级覆盖。不能通过该接口更新 `keys` 或 `targets`（使用模型 Key 接口或 `/api/routes`）。

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
  "api_key_fingerprint": "0123456789ab"
}
```

模型 Key 接口与 `/api/providers/{provider_id}/keys` 系列均返回此结构；provider Key 响应额外在顶层带 `config_revision`。

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
  "keys": [
    {
      "name": "main",
      "enabled": true,
      "api_key_fingerprint": "0123456789ab",
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

成功返回 `204 No Content`；请求体可带 `config_revision` 进行并发校验。只删除模型本身及其绑定关系，被删除模型绑定的供应商 Key 与供应商会保留（供应商 Key 被模型引用不算配置错误）。若引用该模型的 Key 不再被任何模型使用，可另行通过 `/api/providers/{provider_id}/keys/{key_name}` 删除。

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

成功返回 `204 No Content`。该接口与 WebUI 模型路由页的 Key 操作等价：解绑当前模型的这条 Key 绑定。若该 Key 不再被任何模型绑定，会连带删除供应商下的这个 Key；供应商随后没有 Key 时也会一并删除。若这是模型的最后一条绑定，模型会被自动删除。

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
      "models": ["gpt-4o-mini", "fast-mini"]
    }
  ],
  "config_revision": "…"
}
```

顺序是配置里的插入顺序（刚建的那把在末尾），而不是字典序。

`providers` / `models` 只在配置里**显式写了**该字段时才出现：**省略表示「不限制」**，
**空数组表示「一个都不许」**。两者是有区别的授权状态，因此不能让空数组顶替省略——那会把
运维写下的禁令显示成「未限制」。这与 `/api/workspaces` 里 `models` 的处理是同一约定。

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

v4 中 Key 与探测都以 Key 为单元：`providers.<id>` 保存 `base_url`、各协议 `routes` 与 `keys`（Key 集合），探测缓存按 Key 存放在 `providers.<id>.keys.<key>.capabilities`。同一供应商的不同 Key 能访问的模型集可能不同，因此每个 Key 独立探测、独立缓存，互不复用。删除供应商或其 Key 时会清理所有引用它们的模型绑定。

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
| `config_revision` | string | 是 | 并发校验版本号 |

成功返回 `201` 和 ProviderResponse。新供应商没有 Key，添加 Key 请用 `/api/providers/{provider_id}/keys` 或模型 Key 接口（按 `base_url` 自动归并）。

#### `GET/PUT/DELETE /api/providers/{provider_id}`

查询、更新（`id`、`base_url`、`routes`）或删除供应商。`PUT` 请求体为 `ProviderUpdate`（`id`/`base_url`/`routes` 可省略）+ `config_revision`。删除供应商会移除其所有 Key，并删除所有引用它的模型 target；因此失去全部 target 的模型会被一并删除（响应不含被删模型列表，删除前请自行确认）。

#### `GET/POST /api/providers/{provider_id}/keys`

列出该供应商的 Key（`{"keys": [KeyResponse]}`）或创建新 Key。创建请求体为 ProviderKeyCreate（`name`、`api_key`、`enabled`、`config_revision`），成功返回 `201` 和 KeyResponse。新 Key 默认不绑定任何模型；需要按模型绑定请使用模型 Key 接口或 `/api/routes`。

#### `GET/PUT/DELETE /api/providers/{provider_id}/keys/{key_name}`

查询、更新或删除单个供应商 Key。删除会从所有模型的 `targets[]` 中移除对该 Key 的引用，因而失去全部 target 的模型会被自动删除；若这是供应商最后一个 Key，供应商也会被删除。被模型 Key 接口解绑到只剩本模型时，同样会走到这里（删除模型 Key 的最后引用）。

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

`/api/routes` 系列是模型路由（targets）的管理入口，与 `/api/models` 操作同一份模型数据：`POST /api/routes` 创建模型并写入 targets，`GET/PUT/DELETE /api/routes/{route_id}` 读取、整体替换或删除某模型的 targets。请求体中的 `targets` 为 RouteTarget 数组（`{provider, key, upstream_model}`），target 引用的供应商与 Key 必须已存在。v3 的 `pool` 引用已不存在于 target 中。请求体还认 `id`（改名，会一并改写 `unified_model` 与任务里的引用）与 `aliases`（额外的可调用名）；`hidden_aliases` 已移除，请求里带上它会被当成未知字段返回 `422`。

一条路由下没有 target 就不该存在，这条不变式由服务端在各条写路径上保证：

- `PUT /api/routes/{route_id}` 传 `targets: []` 会**删除该路由**并返回 `204 No Content`（没有响应体，也就没有 `config_revision`）。省略 `targets` 字段则表示不改动目标，与传空数组是两回事。
- 解绑 Key、取消 Key 勾选、删除 Key 等路径删掉最后一条 target 时，同样会连带删除该路由。

被删掉的路由会从 `models` 里消失，引用它的 `unified_model` 与任务引用会被一并清理（与 `DELETE /api/routes/{route_id}` 同一套修复）。

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
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（下称 CPA）实例的账号与额度汇总到
一块看板上（WebUI 的「账号资源」页）。实例清单存在配置文件的顶层键 `cpa_instances` 里：

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

```bash
# 读实例清单：响应含 config_revision 与各实例的 management_key（要求完整权限）
curl http://127.0.0.1:8000/api/cpa-instances -H "Authorization: Bearer your-local-api-key"

# 整体替换清单（不是逐条增删；config_revision 必填，防并发覆盖）
curl -X PUT http://127.0.0.1:8000/api/cpa-instances \
  -H "Authorization: Bearer your-local-api-key" -H "Content-Type: application/json" \
  -d '{"config_revision":"<上一条响应里的值>","instances":{
        "cpa-a":{"label":"主力 CPA","base_url":"http://127.0.0.1:8317","management_key":"..."}}}'
```

`GET /api/cpa-accounts` 由 AMKR 的**服务端**替调用方去问各实例，返回归一化后的账号与额度：

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
          "email": "a@example.com", "status": "active", "disabled": false, "unavailable": false,
          "success": 12, "failed": 1, "supports_quota": false,
          "windows": [
            {"key": "claude/5h", "label": "5 小时", "remaining": 0.58,
             "reset_at": "2027-01-15T08:00:00Z", "source": "passive"}
          ],
          "signals": {"Anthropic-Ratelimit-Unified-5h-Utilization": "0.42"}
        }
      ]
    },
    {"id": "cpa-b", "label": "备用 CPA", "base_url": "http://10.0.0.9:8317",
     "ok": false, "error": "无法连接 CPA: ... connection refused", "accounts": []}
  ]
}
```

口径与边界：

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
- **只有 `supports_quota` 的账号会被额外问一次额度**：CPA 没有额度提供者时回
  `501 no quota provider available for credential`。此时若该账号已有被动窗口，`quota_error`
  为空（不构成故障，报出来只是噪音）；一个窗口都没有时才会带上 `quota_error`。
- **失败是逐实例的**：某个实例连不上、管理密钥不对或条目残缺，只让它的 `error` 有值，其余实例
  照常返回。整个扇出有 60 秒总预算，到点未回的账号按无额度处理。
- **只看不拦**：这些读数**不参与** AMKR 的 Key 冷却与失败切换。AMKR 路由的是它自己的上游 Key，
  与 CPA 侧的账号额度没有耦合关系；额度耗尽该由 CPA 自己的冷却机制处理。

额度通道的来路与官方程度（哪些 provider 有得读、哪些只能靠本地语言服务器）见
[`docs/SUBSCRIPTION-QUOTA.md`](SUBSCRIPTION-QUOTA.md)。

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
| `422` | 管理 API 请求字段类型错误、缺少必填字段或包含未知字段；供应商暂无 Key 时探测；工作空间迁移包畸形、含 `default` 或看起来是配置导出的整包；访问密钥的 `providers` / `models` 引用了未配置的供应商或模型，或凭据与实例内其它凭据重复 |
| `500` | 配置保存失败 |
| `502` | 上游连接或响应转换失败 |
| `503` | 没有可用 Key |

代理接口错误通常使用：

```json
{"error": {"message": "错误信息"}}
```

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
  "layers": ["workspace", "requested_model_id", "model_id", "provider_id", "upstream_model_id"],
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
> 三条都由 `webui/probes/webui_panel_probe.mjs` 断言。

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
  "layers": ["workspace", "requested_model_id", "model_id", "provider_id", "upstream_model_id"],
  "links": [
    {"source_layer": 0, "target_layer": 1, "source": "teamA", "target": "TASK_000001", "requests": 12, "total_tokens": 3400}
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `workspaces[].stats` | 与 `/metrics` 的 `total` 同形（同一套聚合口径） |
| `unattributed` | **没有**工作空间归属的请求（升级前的历史行、以及不走代理的写入路径） |
| `layers` | 流向图的层顺序，与 `links` 的 `source_layer` / `target_layer` 对应 |
| `links[].source` / `target` | 相邻两层之间的连边；`requests` 与 `total_tokens` 都给出，供前端切换宽度口径 |

关于 `unattributed`：**不会**被并进 `default`。把它算到默认工作空间头上会凭空造出一段并不存在的用量。工作空间归属从记录该字段的版本起才开始写入，升级前的历史行永远落在这里，不会追溯回填。

关于 `links`：某一端为空的请求（`provider_id` / `upstream_model_id` 可空）**不成边**，在图上留出缺口，而不是补一个占位节点——否则无法区分哪条是数据、哪条是兜底。

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
      "mode": "unified-model",
      "error": null
    }
  ]
}
```

### `POST /api/integrations/{agent}`

```json
{"mode": "unified-model"}
```

`agent` 取 `claude-code`、`codex`、`pi-agent`；`mode` 取 `unified-model` 或 `native`（Pi Agent 固定使用 `unified-model`）。写入前会先备份原配置，未启用本地鉴权时返回 `409`。

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
