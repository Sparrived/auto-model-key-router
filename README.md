<div align="center">

# Auto Model Key Router

**本地 OpenAI-compatible API Key 路由器：多 Key 分流、失败切换、冷却恢复、调用统计一站式管理。**

[![PyPI](https://img.shields.io/pypi/v/auto-model-key-router?color=3776ab)](https://pypi.org/project/auto-model-key-router/)
![Python](https://img.shields.io/badge/python-%3E%3D3.12-blue)
![FastAPI](https://img.shields.io/badge/FastAPI-0.115%2B-009688)
![Rich](https://img.shields.io/badge/TUI-Rich-8A2BE2)
![OpenAI Compatible](https://img.shields.io/badge/API-OpenAI--compatible-111827)

[快速开始](#-快速开始) · [配置](#-配置) · [运行](#-运行) · [接口](#-本地接口) · [统计](#-计量统计)

</div>

---

## ✨ 特性一览

| 能力 | 说明 |
| --- | --- |
| 🔁 多 Key 路由 | 同一模型可配置多个 API key，支持轮询分流与优先级路由 |
| 🛡️ 失败切换 | 认证、限流、服务错误或请求异常时自动尝试下一个可用 key |
| ❄️ 冷却恢复 | 支持失败阈值、`Retry-After`、状态持久化和上游健康探测恢复 |
| 🔌 输入兼容 | 支持 OpenAI Chat Completions，并兼容 Anthropic Messages / OpenAI Responses 风格输入 |
| 📊 调用统计 | 记录请求、成功/失败、重试、状态码、Token、缓存命中、耗时与首 token 耗时 |
| 🖥️ Terminal UI | 使用 Rich 管理系统服务、模型 key、本地鉴权、监听配置、调用日志和版本更新 |
| 🔐 本地鉴权 | 支持 `Authorization: Bearer` 与 `x-api-key` 两种本地鉴权方式 |
| 🚀 服务管理 | 支持后台进程、Windows 计划任务和 Linux systemd user service |

## 🚀 快速开始

### 1. 安装

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

### 2. 创建配置

CLI 默认读取系统应用缓存目录中的 `router-config.json`。如果使用 `--config router-config.json`，则会读取或创建当前目录下的配置文件。

```bash
copy router-config.example.json router-config.json
```

### 3. 启动控制台

```bash
amkr --config router-config.json
```

### 4. 启动本地代理

```bash
auto-model-key-router --config router-config.json --serve
```

### 5. 发送请求

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-local-api-key" \
  -d "{\"model\":\"gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
```

> [!TIP]
> 客户端请求中的 `model` 可以使用真实模型 ID，也可以使用配置中的任意 `aliases`。转发上游前会统一替换为真实模型 ID。

## 🧭 工作方式

| 阶段 | 行为 |
| --- | --- |
| 模型解析 | 从请求体读取 `model`，匹配真实模型 ID 或别名 |
| Key 选择 | 根据模型的 `routing_mode` 选择可用 key，也可通过 `模型ID/别名[key name]` 显式指定 key |
| 请求转发 | 重写上游 `Authorization`，转发到对应 `base_url` 的 `/v1/{path}` |
| 失败处理 | 可重试错误触发换 key；单 key 模型和 `only_first` 模式按 `max_retries` 重试 |
| 状态维护 | key 失败、冷却、恢复状态写入 `key-state.json` |
| 指标记录 | 每次上游尝试都会写入 SQLite 统计存档 |

可重试状态码为 `401`、`403`、`429`、`500`、`502`、`503`、`504`。

## ⚙️ 配置

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

### 顶层配置

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `host` | `127.0.0.1` | 本地服务监听地址 |
| `port` | `8000` | 本地服务监听端口 |
| `default_base_url` | `https://api.openai.com` | key 未单独设置 `base_url` 时使用的默认上游地址 |
| `request_timeout` | `60` | 上游请求超时时间，单位秒 |
| `max_retries` | `2` | 单 key 模型和 `only_first` 模式最大重试次数；其他多 key 模型会按 key 数尝试不同 key |
| `key_failure_threshold` | `2` | key 连续失败达到该次数后进入冷却，最小值为 `1` |
| `key_cooldown_seconds` | `60` | 默认冷却时长，单位秒；上游返回 `Retry-After` 时优先使用该值 |
| `key_state_path` | 缓存目录 | key 失败和冷却状态持久化路径 |
| `upstream_health_check_interval` | `30` | 冷却 key 的上游健康探测间隔，设为 `0` 可关闭探测 |
| `metrics_db_path` | 缓存目录 | SQLite 计量存档路径 |
| `log_file_path` | 缓存目录 | 服务运行日志路径 |
| `local_api_key` | 自动生成 | 本地代理鉴权 key，留空则不启用本地鉴权 |
| `models` | `[]` | 模型、别名、路由模式、推理强度和上游 key 列表 |

### 模型配置

| 字段 | 说明 |
| --- | --- |
| `id` | 转发给上游的真实模型 ID；客户端请求中的 `model` 会被替换为该值 |
| `aliases` | 额外公开的模型名或显示名；客户端使用别名时仍会落到同一组 key |
| `routing_mode` | 支持 `round_robin`、`priority` 和 `only_first`，未设置时默认 `round_robin` |
| `reasoning_effort` | 支持 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`；为空、`default` 或 `downstream` 表示由下游请求决定 |
| `keys` | 每个 key 包含 `name`、`api_key` 和可选 `base_url`；`base_url` 缺省时使用 `default_base_url` |

### 显式指定 key

外部调用时可以把请求体中的 `model` 写成 `模型ID[key name]` 或 `别名[key name]`，强制本次请求使用同一模型下指定名称的 key。例如：

```json
{
  "model": "fast-mini[gpt-4o-mini-key-2]",
  "messages": [{"role": "user", "content": "hello"}]
}
```

该请求会匹配别名 `fast-mini` 对应的真实模型 `gpt-4o-mini`，上游请求体仍会被改写为 `"model":"gpt-4o-mini"`，但 `Authorization` 使用 `gpt-4o-mini-key-2` 对应的 `api_key`。同一模型下的 `keys[].name` 必须非空且唯一。

### 路由模式

| 模式 | 适合场景 | 行为 |
| --- | --- | --- |
| `round_robin` | 多 key 均衡分流 | 按配置顺序轮询多个 key，把请求分配到不同 key |
| `priority` | 主备 key 或成本优先 | 优先使用靠前 key，失败且错误可重试时再尝试后面的 key |
| `only_first` | 只希望使用首个 key | 只尝试配置中的第一个 key，可重试错误按 `max_retries` 重试，超过次数后失败 |

> [!NOTE]
> `429` 会立即进入冷却；其他可重试错误在达到 `key_failure_threshold` 后进入冷却。冷却中的 key 会被优先跳过；如果所有候选 key 都处于冷却中，服务仍会尝试剩余 key，避免完全不可用。

### 默认缓存路径

| 系统 | 缓存目录 |
| --- | --- |
| Windows | `%LOCALAPPDATA%\AutoModelKeyRouter\` |
| macOS | `~/Library/Caches/AutoModelKeyRouter/` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/auto-model-key-router/` |

| 文件 | 说明 |
| --- | --- |
| `router-config.json` | 配置文件 |
| `metrics.sqlite3` | SQLite 计量存档 |
| `key-state.json` | key 状态存档 |
| `server.log` | 服务运行日志 |
| `server.pid` | 后台服务 PID，与日志文件同目录 |

## 🖥️ 运行

### Terminal UI

```bash
amkr --config router-config.json
```

也可以使用完整命令名：

```bash
auto-model-key-router --config router-config.json
```

主菜单：

| 菜单 | 能力 |
| --- | --- |
| 一键配置 | 自动注册系统服务、确保本地鉴权 key 已生成，并在结果页显示本地鉴权 key |
| 模型 Key | 添加、编辑、删除、排序模型和 key，并配置路由模式与推理强度 |
| CLI 设置 | 集中管理模型服务、本地鉴权、监听配置、调用日志和版本更新 |

首页中的“一键配置”会自动注册系统服务、确保本地鉴权 key 已生成，并在结果页显示本地鉴权 key。

### 后台服务

```bash
auto-model-key-router --config router-config.json --serve
auto-model-key-router --config router-config.json --status
auto-model-key-router --config router-config.json --stop
```

后台服务会写入 PID 文件，默认与运行日志同目录，例如系统缓存目录下的 `server.pid`。

### 系统服务

注册为系统服务并启用开机自启动：

```bash
auto-model-key-router --config router-config.json --install-service
```

统一服务管理命令：

```bash
auto-model-key-router --config router-config.json --service install
auto-model-key-router --config router-config.json --service install-user
auto-model-key-router --config router-config.json --service status
auto-model-key-router --config router-config.json --service start
auto-model-key-router --config router-config.json --service stop
auto-model-key-router --config router-config.json --service restart
auto-model-key-router --config router-config.json --service uninstall
```

Windows 下默认会注册为开机启动的计划任务 `AutoModelKeyRouter`，使用 `SYSTEM` 账户和 `HIGHEST` 权限级别；如果当前终端不是管理员，会自动弹出 UAC 授权窗口。仍可使用 `--service install-user` 注册为当前用户登录时启动的 `LIMITED` 计划任务，该模式通常不需要管理员权限。Linux 下会注册为 systemd user service：`auto-model-key-router.service` 并立即启动，通常不需要 `sudo`；同时会尝试启用 linger 以支持用户未登录时启动，该步骤可能需要管理员授权，失败时服务仍可在用户登录后自启。

### 常用命令

| 命令 | 用途 |
| --- | --- |
| `auto-model-key-router --config router-config.json --show-config` | 只查看配置摘要 |
| `auto-model-key-router --config router-config.json --show-logs` | 查看最近 20 行运行日志和调用统计 |
| `auto-model-key-router --config router-config.json --show-logs 50` | 查看最近 50 行运行日志 |
| `auto-model-key-router --check-update` | 检查 PyPI/GitHub 最新版本 |
| `auto-model-key-router --update` | 手动更新到 PyPI/GitHub 最新版本 |

临时覆盖本次运行的监听地址和端口，不会写回配置文件：

```bash
auto-model-key-router --config router-config.json --host 0.0.0.0 --port 8000
```

> [!WARNING]
> 默认只监听 `127.0.0.1`。配置为 `0.0.0.0` 时会接受所有可达网络的连接；如果机器暴露在公网或未受信任网络中，请务必启用本地鉴权、限制防火墙访问，并避免泄露上游 API key。

## 🔌 本地接口

| 接口 | 鉴权 | 说明 |
| --- | --- | --- |
| `GET /health` | 不需要 | 返回服务状态、公开模型列表、配置路径、本地鉴权状态、key 指纹和 key 冷却状态 |
| `GET /v1/models` | 不需要 | 返回 OpenAI 风格模型列表，包含真实模型 ID 和 aliases |
| `GET /metrics` | 启用 `local_api_key` 时需要 | 返回 SQLite 聚合统计快照 |
| `/v1/{path}` | 启用 `local_api_key` 时需要 | 代理 OpenAI-compatible 请求，支持 `GET`、`POST`、`PUT`、`PATCH`、`DELETE` |

代理型 `/v1/{path}` 请求需要在 JSON 请求体中提供 `model`。缺少 `model` 会返回 `400`，模型未配置会返回 `404`，没有可用 key 会返回 `503`。

## 🧩 请求兼容

| 请求入口 | 转发目标 | 兼容行为 |
| --- | --- | --- |
| `/v1/chat/completions` | `/v1/chat/completions` | 兼容 Anthropic 顶层 `system` 和 Responses 风格 content part 类型 |
| `/v1/messages` | `/v1/chat/completions` | Anthropic `system` 转 system message，`text` 和 base64 `image` 转 OpenAI-compatible 消息块 |
| `/v1/responses` | `/v1/chat/completions` | `instructions` 转 system message，`input` 字符串或消息数组转 `messages` |

参数兼容：

- `max_output_tokens` 会转换为 `max_tokens`。
- `stop_sequences` 会转换为 `stop`。
- 不适合 Chat Completions 的字段会在转发前移除。
- `stream: true` 会自动补充 `stream_options.include_usage=true`，并从 SSE `data:` chunk 中提取 `usage` 用于统计。

> [!IMPORTANT]
> `/v1/messages` 和 `/v1/responses` 当前提供的是输入兼容；上游响应体会按原样返回，不会反向转换为 Anthropic Messages 或 OpenAI Responses 的响应 schema。

模型级 `reasoning_effort` 非空时会覆盖请求中的推理强度；没有模型级覆盖时，Responses 风格的 `reasoning.effort` 会转换为 OpenAI-compatible `reasoning_effort` 后转发。

## 🔐 本地鉴权

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

## 📊 计量统计

服务会把计量数据写入 SQLite 存档。`metrics_db_path` 为空时默认写入系统应用缓存目录下的 `metrics.sqlite3`，也可以通过配置项 `metrics_db_path` 修改存档路径。

启用本地鉴权时，通过 `/metrics` 查看聚合统计需要携带本地 API key：

```bash
curl http://127.0.0.1:8000/metrics \
  -H "Authorization: Bearer your-local-api-key"
```

返回结构：

| 字段 | 说明 |
| --- | --- |
| `started_at` | 当前服务进程启动时间 |
| `database_path` | 当前 SQLite 存档路径 |
| `total` | 全局累计统计 |
| `models` | 按真实模型 ID 汇总的统计 |
| `requested_models` | 按客户端请求使用的模型名或别名汇总的统计 |
| `model_requested_models` | 在真实模型 ID 下按请求模型名或别名拆分的统计 |
| `keys` | 按真实模型 ID 和 key 名称拆分的统计 |

每组统计包含：

| 分类 | 字段 |
| --- | --- |
| 请求结果 | `requests`、`successes`、`failures`、`retries` |
| Token 用量 | `prompt_tokens`、`completion_tokens`、`total_tokens` |
| 缓存统计 | `cached_tokens`、`cache_creation_input_tokens`、`cache_read_input_tokens`、`cache_hits`、`cache_misses`、`cache_hit_rate`、`cached_token_rate` |
| 响应耗时 | `total_duration_ms`、`avg_duration_ms`、`min_duration_ms`、`max_duration_ms` |
| 首 token | `total_first_token_ms`、`avg_first_token_ms`、`min_first_token_ms`、`max_first_token_ms` |
| 状态码 | `status_codes` |

统计记录会持久化保存，服务重启后 `/metrics` 会继续基于同一个 SQLite 文件聚合历史数据。Terminal UI 的调用日志可以按 `24小时`、`3天`、`7天`、`30天` 和 `全部` 查看明细；`/metrics` 当前返回 SQLite 中的全量聚合快照。

## 🧪 开发与测试

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

## � 维护者发布

交互式发布脚本会自动读取 `pyproject.toml` 当前版本，选择发布类型后计算新版本号，自动安装开发发布依赖，更新版本与 `CHANGELOG.md`，运行测试、构建和 `twine check`，随后提交、打 tag 并推送到远端。

```bash
python scripts/release.py
```

支持的发布类型包含 `patch` 小版本、`minor` 中版本、`major` 大版本、`post` 版本、`preview`/`alpha`/`beta` 预览版本、`dev` 开发版本、`stable` 预览转正式版和 `custom` 自定义版本。

常用非交互命令：

```bash
python scripts/release.py --type patch --yes
python scripts/release.py --type minor --notes "新增核心功能" --yes
python scripts/release.py --type custom --version 2.0.0rc1 --yes
python scripts/release.py --type patch --dry-run
```

如果本机 Git 全局代理不可用，可以使用 `--no-proxy` 临时绕过代理推送；如果只想完成本地提交和标签，可以使用 `--no-push`。

## �� 版本更新

```bash
auto-model-key-router --check-update
auto-model-key-router --update
```

Terminal UI 启动时会优先快速检查 PyPI JSON API，PyPI 不可用时回退到 GitHub Release。如果发现新版本，首页会显示更新提示，也可以进入“版本更新”菜单重新检查或确认手动更新。PyPI 可用时会执行 `pip install --upgrade auto-model-key-router`，回退到 GitHub 时会安装对应 Release 源码包。更新完成后需要重启当前终端和正在运行的后台/系统服务，让新版本生效。
