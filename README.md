# Auto Model Key Router

Auto Model Key Router 是一个轻量的本地 API key 路由服务。它在本机提供 OpenAI-compatible 接口，为同一个模型 ID 管理多个上游 API key，并按模型配置自动分流、失败切换、冷却恢复和统计调用数据。

## 功能

- 支持 OpenAI-compatible `/v1/*` 本地代理，并按请求体 `model` 自动解析真实模型 ID 或别名
- 支持同一模型配置多个 key，并按模型选择 `round_robin` 分流或 `priority` 优先级路由
- 支持认证、限流和服务错误后的自动切换，单 key 配置会按 `max_retries` 重试同一个 key
- 支持 key 失败次数、`Retry-After` 冷却、冷却状态持久化和上游 `/v1/models` 健康探测恢复
- 支持 Anthropic Messages 与 OpenAI Responses 风格输入兼容，统一转发到上游 Chat Completions
- 支持流式响应转发、首 token 耗时、Token 用量、缓存命中、状态码和请求耗时统计
- 支持 Rich Terminal UI 管理系统服务、模型 key、本地鉴权、监听配置、调用日志和版本更新
- 支持通过 PyPI/GitHub 检查新版本、显示更新提示和手动更新
- 支持 `/health`、`/v1/models` 和 `/metrics` 本地接口

## 安装

需要 Python `>=3.12`。

```bash
python -m pip install auto-model-key-router
```

从源码开发或本地安装：

```bash
python -m pip install -e ".[test]"
```

安装后可使用两个等价命令：

```bash
amkr --version
auto-model-key-router --version
```

## 配置

CLI 默认读取系统应用缓存目录中的 `router-config.json`。如果使用 `--config router-config.json`，则会读取或创建当前目录下的配置文件。目标配置文件不存在时，程序会自动生成一个空配置；也可以复制示例配置后编辑：

```bash
copy router-config.example.json router-config.json
```

示例配置：

```json
{
  "host": "127.0.0.1",
  "port": 8000,
  "default_base_url": "https://api.openai.com",
  "request_timeout": 60,
  "max_retries": 2,
  "key_failure_threshold": 2,
  "key_cooldown_seconds": 60,
  "key_state_path": "",
  "upstream_health_check_interval": 30,
  "metrics_db_path": "",
  "log_file_path": "",
  "local_api_key": "amkr-generated-local-api-key",
  "models": [
    {
      "id": "gpt-4o-mini",
      "aliases": ["gpt-4o-mini-display", "fast-mini"],
      "routing_mode": "round_robin",
      "reasoning_effort": "medium",
      "keys": [
        {
          "name": "gpt-4o-mini-key-1",
          "api_key": "sk-your-first-key"
        },
        {
          "name": "gpt-4o-mini-key-2",
          "api_key": "sk-your-second-key",
          "base_url": "https://api.openai.com"
        }
      ]
    }
  ]
}
```

顶层配置项：

| 字段 | 说明 |
| --- | --- |
| `host` / `port` | 本地服务监听地址和端口，默认 `127.0.0.1:8000` |
| `default_base_url` | key 未单独设置 `base_url` 时使用的默认上游地址 |
| `request_timeout` | 上游请求超时时间，单位秒 |
| `max_retries` | 单 key 模型的最大重试次数；多 key 模型会按 key 数尝试不同 key |
| `key_failure_threshold` | key 连续失败达到该次数后进入冷却，最小值为 `1` |
| `key_cooldown_seconds` | 默认冷却时长，单位秒；上游返回 `Retry-After` 时优先使用该值 |
| `key_state_path` | key 失败和冷却状态持久化路径，留空使用默认缓存路径 |
| `upstream_health_check_interval` | 冷却 key 的上游健康探测间隔，单位秒；设为 `0` 可关闭探测 |
| `metrics_db_path` | SQLite 计量存档路径，留空使用默认缓存路径 |
| `log_file_path` | 服务运行日志路径，留空使用默认缓存路径 |
| `local_api_key` | 本地代理鉴权 key，留空则不启用本地鉴权 |
| `models` | 模型、别名、路由模式、推理强度和上游 key 列表 |

模型配置项：

- `id` 是转发给上游的真实模型 ID；客户端请求中的 `model` 会被替换为该值。
- `aliases` 是额外公开的模型名或显示名；客户端请求 `id` 或任意别名都会使用同一组 key。
- `routing_mode` 支持 `round_robin` 和 `priority`，未设置时默认 `round_robin`。
- `reasoning_effort` 支持 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`；为空、`default` 或 `downstream` 表示由下游请求决定。
- `keys` 中每个 key 包含 `name`、`api_key` 和可选 `base_url`；`name` 缺省时会自动生成，`base_url` 缺省时使用 `default_base_url`。

路由模式：

- `round_robin`：分流模式，按配置顺序轮询多个 key，把请求分配到不同 key。
- `priority`：优先级模式，每次请求优先使用配置中排在前面的 key；当前 key 失败且错误可重试时，再按顺序尝试后面的 key。

可重试状态码为 `401`、`403`、`429`、`500`、`502`、`503`、`504`。发生可重试错误或请求异常时会记录失败；`429` 会立即进入冷却，其他错误在达到 `key_failure_threshold` 后进入冷却。冷却中的 key 会被优先跳过；如果所有候选 key 都处于冷却中，服务仍会尝试剩余 key，避免因为冷却状态导致完全不可用。

默认缓存目录：

- Windows：`%LOCALAPPDATA%\AutoModelKeyRouter\`
- macOS：`~/Library/Caches/AutoModelKeyRouter/`
- Linux：`${XDG_CACHE_HOME:-~/.cache}/auto-model-key-router/`

默认文件名：

- 配置文件：`router-config.json`
- SQLite 计量存档：`metrics.sqlite3`
- key 状态存档：`key-state.json`
- 服务运行日志：`server.log`
- 后台服务 PID：`server.pid`，与日志文件同目录

## 运行

默认进入 Terminal UI：

```bash
amkr --config router-config.json
```

也可以使用完整命令名：

```bash
auto-model-key-router --config router-config.json
```

主菜单包含：

- 系统服务：安装自启、启动、停止、重启、查看状态和卸载自启
- 模型 Key：添加、编辑、删除、排序模型和 key，并配置路由模式与推理强度
- 本地鉴权：生成、重置或清空本地 API key
- 监听配置：修改监听地址与端口
- 调用日志：查看运行日志和 SQLite 调用统计
- 版本更新：检查 PyPI/GitHub 最新版本并手动更新

跳过 Terminal UI，启动后台服务：

```bash
auto-model-key-router --config router-config.json --serve
```

查看后台服务状态：

```bash
auto-model-key-router --config router-config.json --status
```

停止后台服务：

```bash
auto-model-key-router --config router-config.json --stop
```

后台服务会写入 PID 文件，默认与运行日志同目录，例如系统缓存目录下的 `server.pid`。

注册为系统服务并启用开机自启动：

```bash
auto-model-key-router --config router-config.json --install-service
```

Windows 下会注册为用户登录时启动的计划任务 `AutoModelKeyRouter` 并立即启动。Linux 下会注册为 systemd user service：`auto-model-key-router.service` 并立即启动，同时尝试启用 linger 以支持用户未登录时启动。

也可以使用统一服务管理命令：

```bash
auto-model-key-router --config router-config.json --service install
auto-model-key-router --config router-config.json --service status
auto-model-key-router --config router-config.json --service start
auto-model-key-router --config router-config.json --service stop
auto-model-key-router --config router-config.json --service restart
auto-model-key-router --config router-config.json --service uninstall
```

只查看配置摘要：

```bash
auto-model-key-router --config router-config.json --show-config
```

临时覆盖本次运行的监听地址和端口，不会写回配置文件：

```bash
auto-model-key-router --config router-config.json --host 0.0.0.0 --port 8000
```

默认只监听 `127.0.0.1`。配置为 `0.0.0.0` 时会接受所有可达网络的连接；如果机器暴露在公网或未受信任网络中，请务必启用本地鉴权、限制防火墙访问，并避免泄露上游 API key。Terminal UI 首页会在检测到 `0.0.0.0` 时显示风险提示。

查看调用日志，默认显示最近 20 行运行日志，调用统计明细固定 10 行/页：

```bash
auto-model-key-router --config router-config.json --show-logs
```

指定运行日志行数：

```bash
auto-model-key-router --config router-config.json --show-logs 50
```

调用统计支持按 `24小时`、`3天`、`7天`、`30天`、`全部` 查询，Terminal UI 中可用 Tab 切换范围，总览和请求明细会同步按当前范围刷新。

检查 PyPI/GitHub 最新版本：

```bash
auto-model-key-router --check-update
```

手动更新到 PyPI/GitHub 最新版本：

```bash
auto-model-key-router --update
```

Terminal UI 启动时会优先快速检查 PyPI JSON API，PyPI 不可用时回退到 GitHub Release。如果发现新版本，首页会显示更新提示，也可以进入“版本更新”菜单重新检查或确认手动更新。PyPI 可用时会执行 `pip install --upgrade auto-model-key-router`，回退到 GitHub 时会安装对应 Release 源码包。更新完成后需要重启当前终端和正在运行的后台/系统服务，让新版本生效。

## 本地接口

| 接口 | 鉴权 | 说明 |
| --- | --- | --- |
| `GET /health` | 不需要 | 返回服务状态、公开模型列表、配置路径、本地鉴权状态、key 指纹和 key 冷却状态 |
| `GET /v1/models` | 不需要 | 返回 OpenAI 风格模型列表，包含真实模型 ID 和 aliases |
| `GET /metrics` | 启用 `local_api_key` 时需要 | 返回 SQLite 聚合统计快照 |
| `/v1/{path}` | 启用 `local_api_key` 时需要 | 代理 OpenAI-compatible 请求，支持 `GET`、`POST`、`PUT`、`PATCH`、`DELETE` |

代理型 `/v1/{path}` 请求需要在 JSON 请求体中提供 `model`。缺少 `model` 会返回 `400`，模型未配置会返回 `404`，没有可用 key 会返回 `503`。

## 请求示例

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-local-api-key" \
  -d "{\"model\":\"gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
```

本地服务会根据请求体中的 `model` 字段选择模型配置和可用 key，并把请求转发到该 key 配置的上游 `/v1/chat/completions`。

也兼容 Anthropic Messages 和 OpenAI Responses 风格的消息输入：

- 请求 `/v1/messages` 时，会把 Anthropic 顶层 `system` 转为 system message，并把 `content` 中的 `text`、base64 `image` 块转为 OpenAI-compatible 消息块后转发到上游 `/v1/chat/completions`。
- 请求 `/v1/responses` 时，会把 `instructions` 转为 system message，把 `input` 字符串或消息数组转为 `messages` 后转发到上游 `/v1/chat/completions`。
- 请求 `/v1/chat/completions` 时，也会兼容 Anthropic 风格的顶层 `system` 和 Responses 风格的 content part 类型。
- `max_output_tokens` 会转换为 `max_tokens`，`stop_sequences` 会转换为 `stop`，不适合 Chat Completions 的字段会在转发前移除。
- 如果请求是 `stream: true`，会自动补充 `stream_options.include_usage=true`，并从 SSE `data:` chunk 中提取 `usage` 用于统计。

`/v1/messages` 和 `/v1/responses` 当前提供的是输入兼容；上游响应体会按原样返回，不会反向转换为 Anthropic Messages 或 OpenAI Responses 的响应 schema。

模型级 `reasoning_effort` 非空时会覆盖请求中的推理强度；没有模型级覆盖时，Responses 风格的 `reasoning.effort` 会转换为 OpenAI-compatible `reasoning_effort` 后转发。

## 本地鉴权

首次生成配置文件时会自动生成 `local_api_key`。如果旧配置中该字段为空，程序加载配置时也会自动补齐。也可以在 Terminal UI 中通过“本地鉴权”生成、重置或清空本地 API key。

设置后，客户端访问 `/metrics` 和代理型 `/v1/{path}` 接口时需要传入：

```bash
Authorization: Bearer your-local-api-key
```

也支持使用：

```bash
x-api-key: your-local-api-key
```

`/health` 和 `/v1/models` 不需要本地鉴权。如果 `local_api_key` 为空，则所有本地接口都不启用本地鉴权。

## 计量统计

服务会把计量数据写入 SQLite 存档。`metrics_db_path` 为空时默认写入系统应用缓存目录下的 `metrics.sqlite3`，也可以通过配置项 `metrics_db_path` 修改存档路径。

启用本地鉴权时，通过 `/metrics` 查看聚合统计需要携带本地 API key：

```bash
curl http://127.0.0.1:8000/metrics \
  -H "Authorization: Bearer your-local-api-key"
```

返回数据包含：

- `started_at`：当前服务进程启动时间
- `database_path`：当前 SQLite 存档路径
- `total`：全局累计统计
- `models`：按真实模型 ID 汇总的统计
- `requested_models`：按客户端请求使用的模型名或别名汇总的统计
- `model_requested_models`：在真实模型 ID 下按请求模型名或别名拆分的统计
- `keys`：按真实模型 ID 和 key 名称拆分的统计

每组统计包含：

- `requests`、`successes`、`failures`、`retries`：请求、成功、失败和重试次数
- `prompt_tokens`、`completion_tokens`、`total_tokens`：从上游 `usage` 提取的 Token 用量
- `cached_tokens`、`cache_creation_input_tokens`、`cache_read_input_tokens`：OpenAI-compatible 与 Anthropic-compatible 缓存 Token 用量
- `cache_hits`、`cache_misses`、`cache_hit_rate`、`cached_token_rate`：缓存命中和缓存 Token 比例
- `total_duration_ms`、`avg_duration_ms`、`min_duration_ms`、`max_duration_ms`：上游响应耗时统计，单位毫秒
- `total_first_token_ms`、`avg_first_token_ms`、`min_first_token_ms`、`max_first_token_ms`：首 token 耗时统计，单位毫秒
- `status_codes`：上游响应状态码分布

统计记录会持久化保存，服务重启后 `/metrics` 会继续基于同一个 SQLite 文件聚合历史数据。Terminal UI 的调用日志可以按 `24小时`、`3天`、`7天`、`30天` 和 `全部` 查看明细；`/metrics` 当前返回 SQLite 中的全量聚合快照。

## 开发与测试

安装测试依赖并运行测试：

```bash
python -m pip install -e ".[test]"
python -m pytest
```

如果使用 `uv`：

```bash
uv sync --extra test
uv run pytest
```
