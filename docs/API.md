# API 文档

本文档描述 Auto Model Key Router 当前提供的 HTTP、SSE 和 WebSocket 接口。

默认服务地址：

```text
http://127.0.0.1:8000
```

FastAPI 同时提供自动生成的接口页面：

- Swagger UI：`/docs`
- ReDoc：`/redoc`
- OpenAPI JSON：`/openapi.json`

## 鉴权

除 `HEAD /`、`GET /health` 和 FastAPI 文档页面外，业务接口通常需要本地 API key。

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
| 固定 visitor key `amkr-visitor` | `/v1/models`、`/v1/*` | 需要安装 `visitor` 扩展，只能使用允许访客访问的 Key |
| 无 key | `HEAD /`、`GET /health` | 当运行时 `local_api_key` 为空时，其他接口也按本地完整权限处理 |

visitor 模型使用 `amkr-{真实模型ID}` 形式，例如 `amkr-gpt-5.5`。visitor 不能使用内部别名、真实模型 ID、`unified-model` 或没有开启 `allow_visitor` 的 Key。

## 接口总览

| 方法 | 路径 | 鉴权 | 用途 |
| --- | --- | --- | --- |
| `HEAD` | `/` | 无 | 存活探针，返回 `204` |
| `GET` | `/health` | 无 | 服务、配置和端点能力状态 |
| `GET` | `/v1/models` | 本地或 visitor | 查询当前调用方可用模型 |
| `POST` | `/v1/chat/completions` | 本地或 visitor | OpenAI Chat Completions 兼容接口 |
| `POST` | `/v1/messages` | 本地或 visitor | Anthropic Messages 兼容接口 |
| `POST` | `/v1/messages/count_tokens` | 本地或 visitor | 本地估算 Anthropic 输入 token |
| `POST` | `/v1/responses` | 本地或 visitor | OpenAI Responses 兼容接口 |
| 多种 | `/v1/{path}` | 本地或 visitor | 其他 OpenAI-compatible 接口透传 |
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
| `GET/POST` | `/api/providers/{provider_id}/pools` | 仅本地 | 查询或创建 Pool |
| `GET/PUT/DELETE` | `/api/providers/{provider_id}/pools/{pool_name}` | 仅本地 | 查询、更新或删除 Pool |
| `GET/POST` | `/api/routes` | 仅本地 | 查询或创建模型路由 |
| `GET/PUT/DELETE` | `/api/routes/{route_id}` | 仅本地 | 查询、更新或删除模型路由 |
| `GET/PUT` | `/api/settings` | 仅本地 | 查询或更新监听、超时和重试设置 |
| `POST` | `/api/settings/local-api-key` | 仅本地 | 重置本地鉴权 Key；新 Key 仅在本次响应返回 |
| `POST` | `/api/update/check` | 仅本地 | 复用 CLI 的 PyPI/GitHub 版本检查 |
| `POST` | `/api/probes/keys`、`/api/probes/pools` | 仅本地 | 异步探测 Key 或 Pool |
| `GET` | `/api/probes/{probe_id}` | 仅本地 | 查询探测进度和结果 |
| `POST` | `/api/probes/{probe_id}/cancel` | 仅本地 | 取消探测 |
| `POST` | `/api/config/export`、`/api/config/import` | 仅本地 | 导出或导入可迁移配置 |

## 代理接口通用参数

代理型 `/v1/{path}` 接口读取 JSON 请求体，并使用其中的 `model` 选择模型和上游 Key。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | string | 是 | 真实模型 ID、模型别名、`unified-model`、`模型[Key名称]` 或 visitor 公共模型 ID |
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

配置文件中的字段名仍为 `unified_model`；请求中的虚拟模型 ID 为 `unified-model`。

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
| `models` | array | 已配置的真实模型 ID 和别名 |
| `config_path` | string | 当前配置文件绝对路径；嵌入式应用可能为空 |
| `local_auth_enabled` | boolean | 是否设置本地鉴权 |
| `local_api_key_fingerprint` | string | 本地 key 的 SHA-256 前 12 位 |
| `visitor_feature_installed` | boolean | 是否安装 visitor 扩展 |
| `visitor_access_enabled` | boolean | 是否存在启用且允许 visitor 的 Key |
| `visitor_key_count` | integer | visitor 可用 Key 总数 |
| `unified_model` | object/null | 当前真实模型和可选固定 Key |
| `native_endpoint_states` | object | 上游原生端点能力缓存 |

Key 的失败次数和冷却属于内部调度细节，不通过 `/health` 或管理 API 暴露。长期启停 Key 请更新配置中的 `enabled`。

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

本地调用只返回当前有可用 Key 的真实模型、别名和已配置的 `unified-model`。visitor 只返回 `amkr-*` 公共模型 ID。

## 调用统计

### `GET /metrics`

仅接受本地完整权限，不接受 visitor key。

可选查询参数：

| 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `hours` | number | 无 | 仅聚合最近若干小时；必须大于 `0` 且不超过 `8760`。省略时聚合全部历史。 |

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
| `caller_types` | 按 `local`、`visitor` 拆分 |
| `models` | 按真实模型 ID 拆分 |
| `requested_models` | 按请求中的模型名或别名拆分 |
| `model_requested_models` | 真实模型到请求模型名的嵌套统计 |
| `keys` | 真实模型到 Key 名称的嵌套统计 |
| `providers` | 按请求发生时的供应商 ID 拆分 |
| `provider_pools` | 按请求发生时的供应商和模型池拆分 |
| `upstream_models` | 按实际发送给上游的模型 ID 拆分 |
| `unattributed` | 缺少供应商、模型池或上游模型任一归因字段的调用汇总 |

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

升级前产生的历史行没有供应商归因，统一计入 `unattributed`；三个归因维度聚合只包含字段完整的调用。AMKR 不会根据当前配置反推旧数据，避免配置改名后改变历史含义。供应商、池或模型的实际 ID 即使为 `unknown`，也仍按字面 ID 查询和聚合，不会与未归因数据混淆。

### `GET /metrics/requests`

返回持久化的上游调用明细、所选范围汇总和相同筛选条件下的近 60 秒速率。仅接受本地完整权限。

查询参数：

| 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `hours` | number | `24` | 最近小时数，必须大于 `0` 且不超过 `720` |
| `all_history` | boolean | `false` | 为 `true` 时忽略 `hours` 并查询全部历史 |
| `caller_type` | string | 无 | `local` 或 `visitor` |
| `model_id` | string | 无 | 真实路由模型 ID |
| `requested_model_id` | string | 无 | 客户端请求中的模型名或别名 |
| `provider_id` | string | 无 | 请求发生时的供应商 ID |
| `pool_name` | string | 无 | 请求发生时的模型池名称 |
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
      "pool_name": "primary",
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
      "duration_ms": 3100
    }
  ],
  "next_before_id": 1235
}
```

当 `next_before_id` 为 `null` 时没有下一页。刷新第一页时不要携带 `before_id`。

### `GET /metrics/series`

返回 SQLite 历史数据生成的非重叠时间桶，用于趋势图。服务端按北京时间自然边界对齐起点并补齐没有调用的空桶，因此响应 `window.from` 可能略早于精确的 `hours` 起点；最后一个桶可能尚未结束。仅接受本地完整权限。

查询参数：

| 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `hours` | number | `1` | 最近小时数，必须大于 `0` 且不超过 `720` |
| `bucket_seconds` | integer | `60` | 桶宽 `15..86400` 秒；一次查询最多返回 `500` 个桶 |
| 其他筛选参数 | - | 无 | 与 `/metrics/requests` 的来源、模型、供应商、Key、状态、成功和归因筛选一致 |

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

所有管理接口只接受本地完整权限。写操作会原子更新当前配置文件，并在完成后热重载运行时配置。

查询响应不会返回上游 `api_key` 明文，只返回 SHA-256 前 12 位的 `api_key_fingerprint`。

### 数据结构

#### ModelCreate

| 字段 | 类型 | 必填 | 默认值/约束 |
| --- | --- | --- | --- |
| `id` | string | 是 | 非空；不能与其他 ID 或别名重复 |
| `aliases` | string[] | 否 | `[]`；所有模型名称必须全局唯一 |
| `routing_mode` | string | 否 | `round_robin`；可选 `round_robin`、`priority`、`only_first` |
| `reasoning_effort` | string/null | 否 | 可选 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max` |
| `keys` | KeyCreate[] | 是 | 至少一个 |

#### ModelUpdate

字段与 ModelCreate 的模型字段相同，全部可省略，但请求中至少需要出现一个字段。`id`、`aliases`、`routing_mode` 不能为 `null`；`reasoning_effort: null` 用于清除模型级覆盖。不能通过该接口更新 `keys`。

#### KeyCreate

| 字段 | 类型 | 必填 | 默认值/约束 |
| --- | --- | --- | --- |
| `name` | string | 是 | 非空；在同一模型内唯一 |
| `api_key` | string | 是 | 非空 |
| `base_url` | string/null | 否 | 使用配置的 `default_base_url`，否则为 `https://api.openai.com` |
| `enabled` | boolean | 否 | `true` |
| `allow_visitor` | boolean | 否 | `false` |
| `upstream_routes` | object/null | 否 | 兼容字段；会写入该 Key 的 `base_url` 对应的 URL 级路由，而不是保存到 Key 上 |

`base_url` 最终必须以 `http://` 或 `https://` 开头。KeyCreate/KeyUpdate 中的 `upstream_routes` 仅用于兼容旧客户端；值只能是相对路径或路径前缀，例如 `{"anthropic": "anthropic/"}` 会规范化为 URL 级配置 `upstream_routes[base_url].anthropic = "anthropic/v1/messages"`。

#### KeyUpdate

字段与 KeyCreate 相同，全部可省略，但请求中至少需要出现一个字段。省略 `api_key` 会保留原密钥；`name`、`api_key`、`enabled`、`allow_visitor` 不能为 `null`。`base_url: null` 会恢复为配置的默认上游地址；`upstream_routes: null` 或 `{}` 会清空自定义路由。

#### ModelResponse

```json
{
  "id": "gpt-5.5",
  "aliases": ["gpt"],
  "routing_mode": "round_robin",
  "reasoning_effort": "medium",
  "visitor_available": true,
  "keys": []
}
```

#### KeyResponse

```json
{
  "name": "main",
  "base_url": "https://api.openai.com",
  "enabled": true,
  "allow_visitor": true,
  "api_key_fingerprint": "0123456789ab"
}
```

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

#### `GET /api/models/{model_id}`

路径参数 `model_id` 为真实模型 ID。成功返回 ModelResponse。

#### `PUT /api/models/{model_id}`

请求体为 ModelUpdate，成功返回更新后的 ModelResponse。

#### `DELETE /api/models/{model_id}`

成功返回 `204 No Content`。

### Key 接口

#### `GET /api/models/{model_id}/keys`

返回：

```json
{"keys": [KeyResponse]}
```

#### `POST /api/models/{model_id}/keys`

请求体为 KeyCreate，成功返回 `201` 和 KeyResponse。

#### `GET /api/models/{model_id}/keys/{key_name}`

成功返回 KeyResponse。

#### `PUT /api/models/{model_id}/keys/{key_name}`

请求体为 KeyUpdate，成功返回更新后的 KeyResponse。

```bash
curl -X PUT http://127.0.0.1:8000/api/models/gpt-5.5/keys/main \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{"allow_visitor": true}'
```

#### `DELETE /api/models/{model_id}/keys/{key_name}`

成功返回 `204 No Content`。不能删除模型的最后一个 Key，此时返回 `409`，应删除整个模型。

## 状态码与错误格式

常见状态码：

| 状态码 | 场景 |
| --- | --- |
| `204` | 探针成功或删除成功 |
| `400` | 缺少 `model`、更新体为空或配置校验失败 |
| `401` | 本地 API key 验证失败 |
| `403` | visitor 无权访问模型或 Key |
| `404` | 模型或 Key 不存在 |
| `409` | 名称冲突、删除最后一个 Key、无法持久化嵌入式配置 |
| `422` | 管理 API 请求字段类型错误、缺少必填字段或包含未知字段 |
| `500` | 配置保存失败 |
| `502` | 上游连接或响应转换失败 |
| `503` | 没有可用 Key |

代理接口错误通常使用：

```json
{"error": {"message": "错误信息"}}
```

管理 API 的 FastAPI 错误通常使用：

```json
{"detail": "错误信息"}
```

Anthropic Messages 错误可能使用 Anthropic 风格的 `type` 和 `error` 对象。
