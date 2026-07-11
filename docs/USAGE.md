# Auto Model Key Router 使用教程

本文是一份从零开始的完整使用教程，覆盖安装、配置模型与 Key、启动服务、发送请求、查看统计、访客 Key、统一模型切换，以及 Claude Code / Codex 接入。

如果只想查 CLI 参数或 HTTP API 字段，请参考 [`CLI.md`](CLI.md) 和 [`API.md`](API.md)。

---

## 1. 适用场景

Auto Model Key Router（简称 AMKR）是一个本地 OpenAI-compatible API 路由服务。它适合：

- 给同一个模型配置多个上游 API key，并自动分流。
- 在某个 key 限流、鉴权失败或上游异常时自动切换到其他 key。
- 给 Claude Code、Codex 或其他 OpenAI-compatible 客户端提供一个稳定的本地入口。
- 使用固定模型名 `unified-model`，在路由器里随时切换真实模型或指定 key，避免反复改客户端配置。
- 统计本地调用、访客调用、重试、状态码、token 和耗时。

---

## 2. 安装

AMKR 需要 Python `>=3.12`。

推荐用 `pipx` 或 `uv tool` 安装成独立命令行工具：

```bash
pipx install auto-model-key-router
# 或
uv tool install auto-model-key-router
```

如需启用访客 Key 功能，请安装 `visitor` extra：

```bash
pipx install "auto-model-key-router[visitor]"
# 或
uv tool install "auto-model-key-router[visitor]"
```

临时试用可以使用：

```bash
uvx --from auto-model-key-router amkr --version
```

安装后有两个等价命令：

```bash
amkr --version
auto-model-key-router --version
```

---

## 3. 准备配置文件

### 3.1 默认配置路径

不传 `--config` 时，AMKR 默认读写系统缓存目录中的 `router-config.json`：

| 系统 | 默认目录 |
| --- | --- |
| Windows | `%LOCALAPPDATA%\AutoModelKeyRouter\` |
| macOS | `~/Library/Caches/AutoModelKeyRouter/` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/auto-model-key-router/` |

首次启动时，如果配置文件不存在，程序会自动创建空配置，并生成本地鉴权 Key。

### 3.2 项目目录配置

如果你想把配置放在当前目录，复制示例配置即可：

```bash
# Windows PowerShell / CMD
copy router-config.example.json router-config.json

# macOS / Linux
cp router-config.example.json router-config.json
```

后续命令统一加上：

```bash
--config router-config.json
```

### 3.3 最小可用配置

一个可工作的配置至少需要：

- `host` / `port`：本地监听地址和端口。
- `local_api_key`：客户端访问本地代理时使用的鉴权 Key；留空表示不启用本地鉴权，不推荐暴露到非可信网络。
- `models`：真实模型列表。
- 每个模型至少一个 `keys[]`。

示例：

```json
{
  "host": "127.0.0.1",
  "port": 8000,
  "default_base_url": "https://api.openai.com",
  "request_timeout": 60,
  "stream_first_byte_timeout": 90,
  "stream_idle_timeout": 180,
  "max_retries": 2,
  "key_failure_threshold": 2,
  "key_cooldown_seconds": 60,
  "local_api_key": "amkr_your-local-api-key",
  "unified_model": {
    "model": "gpt-4o-mini",
    "key": null
  },
  "models": [
    {
      "id": "gpt-4o-mini",
      "aliases": ["fast-mini"],
      "routing_mode": "round_robin",
      "reasoning_effort": "medium",
      "keys": [
        {
          "name": "openai-main",
          "api_key": "sk-your-first-upstream-key"
        },
        {
          "name": "openai-backup",
          "api_key": "sk-your-second-upstream-key",
          "base_url": "https://api.openai.com"
        }
      ]
    }
  ]
}
```

> 注意：`local_api_key` 是调用 AMKR 本地服务的 Key；`keys[].api_key` 是 AMKR 转发到上游时使用的真实模型供应商 Key。

### 3.4 请求与流式超时

- `request_timeout` 控制连接建立、请求写入和非流式请求。
- `stream_first_byte_timeout` 默认 90 秒，从发起流式上游请求开始，覆盖等待响应头和第一块响应体的总时间。
- `stream_idle_timeout` 默认 180 秒，控制收到第一块后相邻响应块的最大等待时间。

三个值都应大于 0。流式响应头返回前超时时，下游响应尚未建立，AMKR 会按现有重试策略切换 Key；下游流建立后发生首块或空闲超时时，只结束当前流，不会自动重放请求，以免产生重复事件、重复计费或非幂等工具调用。可在 TUI 的 **CLI 设置 → 超时配置** 中统一修改这三个值。

---

## 4. 用 Terminal UI 配置和管理

启动 Terminal UI：

```bash
amkr --config router-config.json
```

不加 `--config` 时会管理默认缓存目录中的配置。

TUI 里最常用的入口：

| 菜单 | 主要用途 |
| --- | --- |
| 一键配置 | 注册路由服务，或自动配置 Claude Code / Codex 使用 AMKR |
| 模型 Key | 新增、编辑、删除、排序模型和上游 Key，设置路由模式、推理强度、访客权限 |
| 统一模型 | 设置 `unified-model` 当前指向的真实模型，并选择自动路由或固定 Key |
| CLI 设置 | 管理监听地址、端口、本地鉴权、请求超时、配置迁移、版本更新等 |

推荐的新手流程：

1. 进入 **模型 Key**，添加一个真实模型，例如 `gpt-4o-mini`。
2. 给该模型添加一个或多个上游 Key。
3. 进入 **统一模型**，把 `unified-model` 指向该模型。
4. 进入 **一键配置 → 路由服务**，启动或注册本地路由服务。
5. 用客户端请求 `http://127.0.0.1:8000/v1/...`，模型名可以写真实模型、别名或 `unified-model`。

---

## 5. 启动本地代理

### 5.1 后台启动

```bash
auto-model-key-router --config router-config.json --serve
```

查看状态和停止：

```bash
auto-model-key-router --config router-config.json --status
auto-model-key-router --config router-config.json --stop
```

后台服务会写入 `server.pid`，默认和日志文件在同一个缓存目录。

### 5.2 开机自启 / 系统服务

一键注册：

```bash
auto-model-key-router --config router-config.json --install-service
```

或使用统一服务命令：

```bash
auto-model-key-router --config router-config.json --service install
auto-model-key-router --config router-config.json --service install-user
auto-model-key-router --config router-config.json --service status
auto-model-key-router --config router-config.json --service start
auto-model-key-router --config router-config.json --service stop
auto-model-key-router --config router-config.json --service restart
auto-model-key-router --config router-config.json --service uninstall
```

- Windows：默认注册为计划任务 `AutoModelKeyRouter`；管理员权限不足时会尝试弹出 UAC。
- Linux：注册为 systemd user service `auto-model-key-router.service`。

---

## 6. 发送第一个请求

启动服务后，请求 OpenAI-compatible Chat Completions：

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "hello"}
    ]
  }'
```

也可以使用别名：

```json
{
  "model": "fast-mini",
  "messages": [{"role": "user", "content": "hello"}]
}
```

如果已经配置 `unified_model`，推荐客户端固定使用：

```json
{
  "model": "unified-model",
  "messages": [{"role": "user", "content": "hello"}]
}
```

AMKR 会在转发给上游前把请求体里的模型名改写为真实模型 ID，并把 `Authorization` 替换成选中的上游 `api_key`。

---

## 7. 路由模式与失败切换

每个模型可以设置 `routing_mode`：

| 模式 | 适合场景 | 行为 |
| --- | --- | --- |
| `round_robin` | 多 Key 均衡分流 | 按配置顺序轮询可用 Key |
| `priority` | 主备、成本优先 | 优先使用靠前 Key，失败后再尝试后面的 Key |
| `only_first` | 只允许第一个 Key | 只使用第一个 Key；可重试错误按 `max_retries` 重试 |

以下状态码会触发重试或切换：

```text
401, 403, 429, 500, 502, 503, 504
```

冷却规则：

- `429` 会立即让当前 Key 进入冷却。
- 其他可重试错误达到 `key_failure_threshold` 后进入冷却。
- 上游返回 `Retry-After` 时严格使用该冷却时间；否则按连续失败次数递增 `key_cooldown_seconds`，最长 300 秒。
- 自动失败只产生临时冷却，不会自动永久禁用 Key。
- 冷却和失败计数仅保存在内存中；冷却到期后请求会自然恢复，任意成功请求会立即清空失败状态。
- Key 健康状态不对外暴露，也不接受人工清除；长期启停请修改配置中的 `enabled`。
- 旧配置字段 `upstream_health_check_interval` 仍可读取，但已弃用且不再启动后台健康探测。

### 原生 Anthropic 端点优先

对于 Anthropic Messages 请求（`/v1/messages`），可设置 `native_first` 控制是否优先使用原生格式：

```json
{
  "id": "claude-3-opus",
  "native_first": true,
  "keys": [...]
}
```

| 设置 | 行为 |
| --- | --- |
| `true`（默认） | 优先以原生格式发送到上游 `/v1/messages`，保留所有 Anthropic 字段（`cache_control`、`prompt_cache_key` 等），提高缓存命中率 |
| `false` | 直接转换为 `/v1/chat/completions` 格式 |

原生优先模式工作流程：
1. 首次请求时自动测试上游是否支持 `/v1/messages` 端点
2. 测试结果按“上游 URL + 实际原生路径”记录在 `endpoint-capabilities.json` 的 `endpoint_capabilities` 中
3. 如果上游返回 404/405/501，自动回退到 `chat/completions` 格式并记录结果
4. 如需重新测试，可删除 `endpoint-capabilities.json` 中对应的 `endpoint_capabilities` 条目

如果某个上游的 Anthropic 入口不是 `base_url/v1/messages`，可以按上游 URL 配置额外路由：

```json
{
  "upstream_routes": {
    "https://example.com/tokenplan": {
      "anthropic": "anthropic/"
    }
  }
}
```

`anthropic/` 会被规范化为 `anthropic/v1/messages`，请求会发到 `https://example.com/tokenplan/anthropic/v1/messages`。也可以直接写完整相对路径，例如 `anthropic/v1/messages`。

---

## 8. 统一模型 `unified-model`

`unified-model` 是 AMKR 的固定虚拟模型名。客户端一直请求它，真实模型和 Key 在 AMKR 侧切换。

查看当前指向：

```bash
auto-model-key-router --config router-config.json --show-unified-model
```

切换目标模型：

```bash
auto-model-key-router --config router-config.json --switch-model gpt-4o-mini
```

切换到某个模型并固定 Key：

```bash
auto-model-key-router --config router-config.json --switch-model gpt-4o-mini --switch-key openai-backup
```

只切换当前目标模型使用的 Key：

```bash
auto-model-key-router --config router-config.json --switch-key openai-main
```

恢复自动路由：

```bash
auto-model-key-router --config router-config.json --switch-key auto
```

说明：

- `--switch-model` 接受真实模型 ID 或 alias，写回配置时会规范化为真实模型 ID。
- 如果切换到另一个模型且未传 `--switch-key`，旧的固定 Key 会自动清空，避免误用。
- `unified_model` 只引用现有模型和 Key，不会复制或新增上游 Key。
- 配置中不能把真实模型 ID 或 alias 命名为保留名 `unified-model`。

---

## 9. 显式指定某个 Key

如果只想让单次请求使用某个 Key，可以把 `model` 写成：

```text
模型ID[key name]
别名[key name]
unified-model[key name]
```

示例：

```json
{
  "model": "fast-mini[openai-backup]",
  "messages": [{"role": "user", "content": "hello"}]
}
```

这次请求会：

1. 先把 `fast-mini` 解析到真实模型 `gpt-4o-mini`。
2. 在该模型的 `keys[]` 中找到 `name` 为 `openai-backup` 的 Key。
3. 转发给上游时仍使用真实模型 ID `gpt-4o-mini`。

同一模型下 `keys[].name` 必须非空且唯一。

---

## 10. 本地鉴权

配置里有 `local_api_key` 时，下列接口需要鉴权：

- `/v1/models`
- `/v1/{path}` 代理接口
- `/metrics`
- `/api/*` 管理接口

支持两种传法：

```http
Authorization: Bearer amkr_your-local-api-key
```

或：

```http
x-api-key: amkr_your-local-api-key
```

`/health` 不需要鉴权。

如果 `local_api_key` 为空，则本地接口不启用鉴权。只有在完全可信的本机环境中才建议这样做；如果监听地址改成 `0.0.0.0`，请务必启用鉴权并配置防火墙。

---

## 11. 查看健康状态和模型列表

健康检查：

```bash
curl http://127.0.0.1:8000/health
```

模型列表：

```bash
curl http://127.0.0.1:8000/v1/models \
  -H "Authorization: Bearer amkr_your-local-api-key"
```

`/health` 会返回服务状态、配置路径、本地鉴权状态、公开模型、Key 指纹、冷却状态等信息。

---

## 12. 查看统计和日志

命令行查看配置摘要：

```bash
auto-model-key-router --config router-config.json --show-config
```

查看最近运行日志和调用统计：

```bash
auto-model-key-router --config router-config.json --show-logs
# 指定最近 50 行日志
auto-model-key-router --config router-config.json --show-logs 50
```

HTTP 查看聚合统计：

```bash
curl http://127.0.0.1:8000/metrics \
  -H "Authorization: Bearer amkr_your-local-api-key"
```

统计会持久化写入 SQLite，默认文件为缓存目录下的 `metrics.sqlite3`。返回数据包含：

- 总请求数、成功、失败、重试。
- prompt / completion / total tokens。
- 缓存命中和缓存 token 统计。
- 总耗时、平均耗时、首 token 耗时。
- 状态码分布。
- 按真实模型、请求模型名、Key、本地调用和访客调用拆分的聚合。

---

## 13. 使用访客 Key

访客功能需要安装：

```bash
pipx install "auto-model-key-router[visitor]"
```

访客固定 Key 是：

```text
amkr-visitor
```

它不能在配置里修改，也不能作为 `local_api_key` 使用。

给某个上游 Key 开放访客权限：

```json
{
  "name": "openai-backup",
  "api_key": "sk-your-second-upstream-key",
  "allow_visitor": true
}
```

访客请求示例：

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr-visitor" \
  -d '{
    "model": "amkr-gpt-4o-mini",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

访客限制：

- 只能使用 `allow_visitor: true` 且启用的上游 Key。
- `/v1/models` 中只会看到公共模型 ID，格式为 `amkr-{真实模型ID}`。
- 不能访问内部 alias、真实模型 ID、`unified-model` 或显式 `模型[key]`。
- 不能访问 `/metrics` 和 `/api/*` 管理接口。
- 未安装 `visitor` extra 时，`amkr-visitor` 不会被接受；配置里的 `allow_visitor` 可以保留但不会生效。

---

## 14. 接入 Claude Code

AMKR 支持 Anthropic Messages 风格入口 `/v1/messages`，可供 Claude Code 使用。

推荐在 TUI 中操作：

1. 先在 **统一模型** 中设置 `unified-model`。
2. 确保 `local_api_key` 不为空。
3. 进入 **一键配置 → Claude Code**。
4. 应用配置后，必要时进入 **一键配置 → 路由服务** 启动服务。

AMKR 会更新：

```text
~/.claude/settings.json
# 或 CLAUDE_CONFIG_DIR/settings.json
```

写入的核心环境变量包括：

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8000",
    "ANTHROPIC_AUTH_TOKEN": "amkr_your-local-api-key",
    "ANTHROPIC_MODEL": "unified-model",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "unified-model",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "unified-model",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "unified-model"
  }
}
```

应用前的原始配置会备份到 AMKR 缓存目录，可在 TUI 中回退。

---

## 15. 接入 Codex

AMKR 支持 OpenAI Responses 风格入口 `/v1/responses`，可供 Codex 使用。

推荐在 TUI 中操作：

1. 先在 **统一模型** 中设置 `unified-model`。
2. 确保 `local_api_key` 不为空。
3. 进入 **一键配置 → Codex**。
4. 应用配置后，必要时进入 **一键配置 → 路由服务** 启动服务。

AMKR 会更新：

```text
~/.codex/config.toml
~/.codex/auth.json
# 或 CODEX_HOME/config.toml 与 CODEX_HOME/auth.json
```

写入的核心配置类似：

```toml
model_provider = "OpenAI"
model = "unified-model"
review_model = "unified-model"
model_reasoning_effort = "xhigh"

[model_providers.OpenAI]
name = "OpenAI"
base_url = "http://127.0.0.1:8000/v1"
wire_api = "responses"
requires_openai_auth = true
```

`auth.json` 会更新本地鉴权 key：

```json
{
  "OPENAI_API_KEY": "amkr_your-local-api-key"
}
```

一键配置只会更新上述模型调用字段，以及 `auth.json` 中的 `OPENAI_API_KEY`。现有的其他 Codex 设置、注释、OpenAI Provider 自定义字段和其他鉴权字段都会保留；旧版本已经写入的非模型字段也不会被主动删除。

应用前的原始配置同样会备份，可在 TUI 中回退。

---

## 16. 请求兼容说明

AMKR 的代理入口是 `/v1/{path}`，主要兼容：

| 客户端入口 | 实际转发 | 说明 |
| --- | --- | --- |
| `/v1/chat/completions` | `/v1/chat/completions` | OpenAI-compatible 主路径 |
| `/v1/messages` | 默认原生 `/v1/messages`，不支持时回退 `/v1/chat/completions` | Anthropic Messages 原生优先；可用 URL 级 `upstream_routes[base_url].anthropic` 改原生路径 |
| `/v1/messages/count_tokens` | 本地处理 | 返回 token 估算，不访问上游 |
| `/v1/responses` | 默认探测 `/v1/responses`，不支持时回退 `/v1/chat/completions`；配置 URL 级 `upstream_routes[base_url].responses` 时改原生 Responses 路径 | Responses 原生透传或转 Chat Completions |

兼容转换包括：

- `max_output_tokens` → `max_tokens`
- `stop_sequences` → `stop`
- Responses 的 `instructions`、function call、function output 和 tools 转换
- Anthropic 的 `system`、`tools`、`tool_use`、`tool_result` 转换
- `stream: true` 时自动补充 `stream_options.include_usage=true`，并从 SSE chunk 中提取 usage 用于统计

高级多模态、托管工具等能力仍取决于上游 OpenAI-compatible 服务的兼容程度。

---

## 17. 常见问题

### 请求返回 401 / 403

检查两层 Key：

1. 请求 AMKR 时传的本地 Key 是否等于 `local_api_key`。
2. 配置里的上游 `keys[].api_key` 是否有效。

### 请求返回 404 模型不存在

检查请求体里的 `model` 是否是：

- 真实模型 ID；或
- 该模型的 `aliases[]`；或
- 已配置的 `unified-model`；或
- 访客模式下 `/v1/models` 返回的 `amkr-{真实模型ID}`。

### 请求返回 503 没有可用 Key

可能原因：

- 该模型没有启用的 Key。
- Key 都在冷却中。
- 访客请求的模型没有任何 `allow_visitor: true` 的 Key。

### 修改配置后是否需要重启？

配置文件和管理 API 写入后，运行中的服务会热加载。系统服务、监听地址/端口等运行参数变化时，建议重启服务。

### 如何迁移配置到另一台机器？

使用 TUI：

1. 源机器进入 **CLI 设置 → 配置迁移 → 复制 Key 配置**。
2. 目标机器进入 **CLI 设置 → 配置迁移 → 粘贴并应用**。

迁移内容包含模型与上游 Key；安装 `visitor` extra 时还会包含访客权限。目标端已有监听、本地鉴权、路径等设置会保留。
