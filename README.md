<div align="center">

# Auto Model Key Router

**本地 OpenAI-compatible API Key 路由器：多 Key 分流、失败切换、冷却恢复、调用统计一站式管理。**

[![PyPI](https://img.shields.io/pypi/v/auto-model-key-router?color=3776ab)](https://pypi.org/project/auto-model-key-router/)
![Python](https://img.shields.io/badge/python-%3E%3D3.12-blue)
![FastAPI](https://img.shields.io/badge/FastAPI-0.115%2B-009688)
![Rich](https://img.shields.io/badge/TUI-Rich-8A2BE2)
![OpenAI Compatible](https://img.shields.io/badge/API-OpenAI--compatible-111827)

[快速开始](#-快速开始) · [配置](#-配置) · [运行](#-运行) · [接口](#-本地接口) · [统计](#-计量统计) · [许可证](#-许可证)

</div>

---

## ✨ 特性一览

| 能力 | 说明 |
| --- | --- |
| 🔁 多 Key 路由 | 同一模型可配置多个 API key，支持轮询分流与优先级路由 |
| 🛡️ 失败切换 | 认证、限流、服务错误或请求异常时自动尝试下一个可用 key |
| ❄️ 冷却恢复 | 支持失败阈值、`Retry-After`、状态持久化和上游健康探测恢复 |
| 🔌 输入兼容 | 支持 OpenAI Chat Completions，并兼容 Anthropic Messages / OpenAI Responses 风格输入 |
| 📊 调用统计 | 记录本地/访客来源、成功/失败、重试、状态码、Token、缓存命中、耗时与首 token 耗时 |
| 🖥️ Terminal UI | 使用 Rich 管理系统服务、模型 key、本地鉴权、监听配置、配置迁移、调用日志和版本更新 |
| 🔐 本地鉴权 | 支持 `Authorization: Bearer` 与 `x-api-key` 两种本地鉴权方式 |
| 👤 访客访问 | 可选安装；固定访客 key `amkr-visitor` 只能使用显式授权的模型上游 key |
| 🚀 服务管理 | 支持后台进程、Windows 计划任务和 Linux systemd user service |

## 🚀 快速开始

### 1. 安装

需要 Python `>=3.12`。

推荐使用隔离的命令行工具环境安装：

```bash
pipx install auto-model-key-router
```

或使用 uv 长期安装：

```bash
uv tool install auto-model-key-router
```

如需访客 key 功能，安装 `visitor` extra：

```bash
pipx install "auto-model-key-router[visitor]"
# 或
uv tool install "auto-model-key-router[visitor]"
```

如果只是临时试用，可以使用 uvx：

```bash
uvx --from auto-model-key-router amkr --version
```

> [!NOTE]
> `uvx` 适合临时运行；如果需要后台服务、开机自启或在 TUI 中手动更新，建议使用 `pipx install` 或 `uv tool install`。

也可以安装到当前 Python 环境：

```bash
python -m pip install auto-model-key-router
```

使用 pip 安装访客功能：

```bash
python -m pip install "auto-model-key-router[visitor]"
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
> 配置 `unified_model` 后，调用端还可以始终使用固定模型名 `unified_model`，再通过 CLI 即时切换实际模型或 key。

## 🧭 工作方式

| 阶段 | 行为 |
| --- | --- |
| 模型解析 | 从请求体读取 `model`，匹配真实模型 ID 或别名 |
| 统一模型 | `unified_model` 会解析到 CLI 当前选择的真实模型和可选固定 key |
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
  "unified_model": {
    "model": "gpt-4o-mini",
    "key": "gpt-4o-mini-key-1"
  },
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
          "base_url": "https://api.openai.com",
          "allow_visitor": true
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
| `local_api_key` | 自动生成 | 本地代理完整权限鉴权 key，不能设置为保留值 `amkr-visitor`；留空则不启用本地鉴权 |
| `unified_model` | 未配置 | 固定虚拟模型 `unified_model` 当前指向的已有模型和可选 key |
| `models` | `[]` | 模型、别名、路由模式、推理强度和上游 key 列表 |

### 模型配置

| 字段 | 说明 |
| --- | --- |
| `id` | 转发给上游的真实模型 ID；客户端请求中的 `model` 会被替换为该值 |
| `aliases` | 额外公开的模型名或显示名；客户端使用别名时仍会落到同一组 key |
| `routing_mode` | 支持 `round_robin`、`priority` 和 `only_first`，未设置时默认 `round_robin` |
| `reasoning_effort` | 支持 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`；为空、`default` 或 `downstream` 表示由下游请求决定 |
| `keys` | 每个 key 包含 `name`、`api_key`、可选 `base_url` 和 `allow_visitor`；`allow_visitor` 默认为 `false` |

### 访客 key

访客功能不包含在默认安装中，需要使用 `auto-model-key-router[visitor]` 安装。访客鉴权 key 固定为 `amkr-visitor`，不需要也不能在配置中修改。要允许访客访问某个模型下的特定上游 key，在对应 key 上设置：

```json
{
  "name": "gpt-4o-mini-key-2",
  "api_key": "sk-your-second-key",
  "allow_visitor": true
}
```

访客请求仍可使用模型 ID、别名和 `unified_model`，但自动路由、重试和显式 `模型[key name]` 选择都只会使用 `allow_visitor: true` 且已启用的 key。未授权模型或 key 返回 `403`，访客不能访问 `/metrics`。也可以在 Terminal UI 的“模型 Key → 管理 Key”中切换访客访问权限。

默认安装不会接受 `amkr-visitor`。配置文件中的 `allow_visitor` 可以保留，但只有安装了 `visitor` extra 后才会生效；这样同一份配置可以在启用和未启用访客功能的环境之间迁移。

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr-visitor" \
  -d "{\"model\":\"fast-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
```

### 统一模型快速切换

启用后，调用端的请求体可以始终使用固定模型名：

```json
{
  "model": "unified_model",
  "messages": [{"role": "user", "content": "hello"}]
}
```

通过 CLI 切换实际模型或 key，配置文件会原子更新，运行中的服务会自动热加载：

```bash
# 切换模型，自动使用该模型自身的 routing_mode 选择 key
auto-model-key-router --config router-config.json --switch-model gpt-4o-mini

# 同时固定到该模型下的已有 key
auto-model-key-router --config router-config.json --switch-model gpt-4o-mini --switch-key gpt-4o-mini-key-2

# 只切换当前目标模型使用的 key
auto-model-key-router --config router-config.json --switch-key gpt-4o-mini-key-1

# 取消固定 key，恢复自动路由
auto-model-key-router --config router-config.json --switch-key auto

# 查看当前选择
auto-model-key-router --config router-config.json --show-unified-model
```

`--switch-model` 接受真实模型 ID 或别名，写回配置时会规范化为真实模型 ID。切换到另一模型且未同时指定 `--switch-key` 时，会自动清除旧 key，避免误用同名 key。`unified_model` 只引用 `models` 中已有的配置，不会复制或新增 API key。

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
| 一键配置 | 配置路由服务，或让 Claude Code / Codex 使用本项目作为 API 路由中转，并支持回退 Agent 原配置 |
| 模型 Key | 添加、编辑、删除、排序模型和 key，并配置路由模式与推理强度 |
| 统一模型 | 查看或切换 `unified_model` 指向的已有模型，并选择自动路由或指定已启用 key |
| CLI 设置 | 集中管理模型服务、本地鉴权、监听配置、配置迁移和版本更新 |

首页中的“一键配置”包含：

| 子菜单 | 行为 |
| --- | --- |
| 路由服务 | 自动注册系统服务、确保本地鉴权 key 已生成，并在结果页显示本地鉴权 key |
| Claude Code | 更新 `~/.claude/settings.json`（或 `CLAUDE_CONFIG_DIR/settings.json`），配置 Anthropic 网关地址、鉴权 token 和 `unified_model` |
| Codex | 更新 `~/.codex/config.toml`（或 `CODEX_HOME/config.toml`），注册 Responses API provider 并使用 `unified_model` |

配置 Claude Code 或 Codex 前，需要先在主页“统一模型”中设置 `unified_model`。Agent 配置只覆盖路由所需字段，其他设置会保留；应用前的完整文件内容会缓存到 Auto Model Key Router 的系统缓存目录，可在对应 Agent 子菜单中选择“回退原配置”精确恢复。重复应用当前路由配置不会覆盖最初的回退快照；如果应用后手动修改了 Agent 配置，再次应用时会以当前内容创建新的回退快照。

Agent 配置文件和备份可能包含鉴权 token，请不要共享这些文件。配置完成后，如果路由服务尚未运行，结果页会提示先执行“一键配置 → 路由服务”。

配置迁移可在“CLI 设置 → 配置迁移”中使用：先在当前 TUI 选择“复制 Key 配置”，再到另一台机器或另一个 TUI 选择“粘贴并应用”。迁移内容仅包含模型与上游 API key；安装了 `visitor` 扩展时还会包含各 Key 的访客访问权限。粘贴时会向目标端追加模型和 Key，不覆盖已有模型设置；完全相同的上游 Key 会跳过，同名但内容不同的 Key 会自动添加数字后缀。目标端的本地鉴权、监听地址、端口、超时、重试、文件路径及其他 CLI 设置也会保留。上游 API key 属于敏感信息，请只在可信终端之间传递。

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
| `auto-model-key-router --config router-config.json --show-unified-model` | 查看 `unified_model` 当前指向 |
| `auto-model-key-router --config router-config.json --switch-model MODEL` | 切换 `unified_model` 的实际模型 |
| `auto-model-key-router --config router-config.json --switch-key KEY` | 切换 `unified_model` 的固定 key；`auto` 恢复自动路由 |
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
| `GET /health` | 不需要 | 返回服务状态、公开模型列表、配置路径、本地鉴权状态、访客授权 key 数、key 指纹和 key 冷却状态 |
| `GET /v1/models` | 需要本地或 visitor API key | 返回该 API key 可访问的 OpenAI 风格模型列表；visitor 只看到按 `amkr-{真实模型ID}` 生成的公共模型 ID，不暴露内部 aliases 或 `unified_model` |
| `GET /metrics` | 启用 `local_api_key` 时需要 | 返回 SQLite 聚合统计快照，包含 `caller_types.local` 和 `caller_types.visitor` 分类 |
| `GET/POST /api/models` | 仅本地 API key | 查询或新增模型配置 |
| `GET/PUT/DELETE /api/models/{model_id}` | 仅本地 API key | 查询、修改或删除指定模型 |
| `GET/POST /api/models/{model_id}/keys` | 仅本地 API key | 查询或新增指定模型的上游 key |
| `GET/PUT/DELETE /api/models/{model_id}/keys/{key_name}` | 仅本地 API key | 查询、修改或删除指定上游 key |
| `/v1/{path}` | 启用 `local_api_key` 时需要 | 代理 OpenAI-compatible 请求，支持 `GET`、`POST`、`PUT`、`PATCH`、`DELETE` |

代理型 `/v1/{path}` 请求需要在 JSON 请求体中提供 `model`。缺少 `model` 会返回 `400`，模型未配置会返回 `404`，没有可用 key 会返回 `503`。

### 模型与 Key 管理 API

管理 API 使用当前配置文件作为持久化存储，写入后立即热重载。创建模型时至少需要提供一个 key；更新 key 时可以省略 `api_key` 以保留原密钥。查询响应不会返回上游密钥明文，只返回 `api_key_fingerprint`。

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
      "base_url": "https://api.openai.com",
      "allow_visitor": false
    }]
  }'
```

通过更新 key 的 `allow_visitor` 字段配置访客可用性：

```bash
curl -X PUT http://127.0.0.1:8000/api/models/gpt-5.5/keys/main \
  -H "Authorization: Bearer your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{"allow_visitor": true}'
```

visitor key 不能访问管理 API。删除模型的最后一个 key 会返回 `409`，应直接删除该模型；未通过配置文件路径启动的嵌入式应用可以查询配置，但写操作会返回 `409`，避免重启后丢失修改。

## 🧩 请求兼容

| 请求入口 | 转发目标 | 兼容行为 |
| --- | --- | --- |
| `/v1/chat/completions` | `/v1/chat/completions` | 兼容 Anthropic 顶层 `system` 和 Responses 风格 content part 类型 |
| `/v1/messages` | `/v1/chat/completions` | Anthropic `system`、`tools`、`tool_use`、`tool_result` 转 OpenAI-compatible 请求；响应文本和工具调用转换为 Anthropic Messages 风格 JSON/SSE |
| `/v1/messages/count_tokens` | 本地处理 | 为 Claude Code 返回输入 token 估算值，不访问上游 |
| `/v1/responses` | `/v1/chat/completions` | Responses `instructions`、消息、function call、function output 和 tools 转 Chat Completions；响应文本、工具调用和 SSE 转回 Responses 风格 |

参数兼容：

- `max_output_tokens` 会转换为 `max_tokens`。
- `stop_sequences` 会转换为 `stop`。
- 不适合 Chat Completions 的字段会在转发前移除。
- `stream: true` 会自动补充 `stream_options.include_usage=true`，并从 SSE `data:` chunk 中提取 `usage` 用于统计。

> [!IMPORTANT]
> `/v1/messages` 和 `/v1/responses` 支持标准文本与工具调用的双向转换，分别供 Claude Code 和 Codex 使用；多模态输出、托管工具等高级能力仍取决于上游兼容程度。`/v1/messages/count_tokens` 使用 UTF-8 内容长度进行本地估算，不等同于上游模型的精确 tokenizer 结果。

模型级 `reasoning_effort` 非空时会覆盖请求中的推理强度；没有模型级覆盖时，Responses 风格的 `reasoning.effort` 会转换为 OpenAI-compatible `reasoning_effort` 后转发。

## 🔐 本地鉴权

首次生成配置文件时会自动生成 `local_api_key`。如果旧配置中该字段为空，程序加载配置时也会自动补齐。也可以在 Terminal UI 中通过“本地鉴权”生成、重置或清空本地 API key。

设置后，客户端访问 `/v1/models`、`/metrics`、`/api/*` 和代理型 `/v1/{path}` 接口时需要传入：

```bash
Authorization: Bearer your-local-api-key
```

也支持使用：

```bash
x-api-key: your-local-api-key
```

`/health` 不需要本地鉴权。如果 `local_api_key` 为空，则所有本地接口都不启用本地鉴权。固定 visitor key `amkr-visitor` 可调用 `/v1/models` 和代理型 `/v1/{path}`：模型列表直接从真实模型配置中筛选，并将有 visitor 权限的真实模型 ID 转换为 `amkr-{真实模型ID}`，例如 `gpt-5.5` 对外显示为 `amkr-gpt-5.5`。这些公共 ID 会直接映射回真实模型，不经过或暴露内部 aliases；visitor 也不能使用真实模型 ID、内部 aliases 或 `unified_model`。

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
| `caller_types` | 按 `local`（本地鉴权）和 `visitor`（访客鉴权）拆分的统计，两类始终返回 |
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

统计记录会持久化保存，服务重启后 `/metrics` 会继续基于同一个 SQLite 文件聚合历史数据。Terminal UI 的调用日志提供“全部调用”“本地调用”“访客调用”三个统计页面，每个页面都可以按 `24小时`、`3天`、`7天`、`30天` 和 `全部` 查看明细；`/metrics` 返回 SQLite 中的全量聚合快照，并在 `caller_types` 下按调用来源拆分。

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

Terminal UI 启动时会优先快速检查 PyPI JSON API，PyPI 不可用时回退到 GitHub Release。如果发现新版本，首页会显示更新提示，也可以进入“版本更新”菜单重新检查或确认手动更新。PyPI 可用时会执行 `pip install --upgrade auto-model-key-router`，回退到 GitHub 时会安装对应 Release 源码包。Windows 从正在运行的 `amkr.exe` 发起更新时，会打开独立更新器窗口，确认接管后退出当前界面，等待文件锁释放并自动重试；更新成功后会按原运行状态重启服务和 Terminal UI，失败原因会显示在更新器窗口并写入更新日志。

## 📄 许可证

本项目基于 [MIT License](LICENSE) 开源发布。
