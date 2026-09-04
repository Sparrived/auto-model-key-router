# Auto Model Key Router

一个本地 OpenAI-compatible API 路由器：把多个模型和多个上游 API Key 统一收口到本地服务，自动分流、失败切换、统计调用，并可一键接入 Claude Code / Codex。

## 主要能力

- **按模型管理 Key**：Key 属于供应商（`providers.*.keys`），模型通过 `targets[]`（`{provider, key, upstream_model}`）绑定一个或多个 Key；同一 Key 可服务多个模型，支持 `round_robin`、`priority`、`only_first`。
- **失败切换与冷却**：遇到 `401/403/429/5xx` 等可重试错误时自动重试或切换 Key，并在进程内临时冷却异常 Key。
- **统一模型名**：客户端固定请求 `unified-model`，真实模型和固定 Key 可在路由器侧随时切换。
- **供应商级能力探测**：首次给供应商添加 Key 时自动探测一次（模型列表 + 各路由最小请求），结果缓存为供应商能力，可在 TUI 或管理 API 手动刷新。
- **OpenAI-compatible 代理**：支持 `/v1/chat/completions`、`/v1/models`，并兼容 Claude Code 的 `/v1/messages` 与 Codex 的 `/v1/responses`；可为不同协议模式配置上游额外路径。
- **Terminal UI 管理**：在 TUI 中配置供应商与 Key、模型、统一模型、服务注册和客户端接入。
- **访客 Key**：安装 `visitor` extra 后，可用固定访客 Key 暴露受限公共模型。
- **统计与日志**：记录本地/访客调用、模型、Key、状态码、token、重试、延迟等指标。

## 安装

需要 Python `>=3.12`。

使用 pipx：

```bash
pipx install auto-model-key-router
pipx ensurepath
```

或使用 uv：

```bash
uv tool install auto-model-key-router
uv tool update-shell
```

`pipx ensurepath` 和 `uv tool update-shell` 会把命令所在目录加入 PATH。执行后请关闭并重新打开终端（IDE、VS Code、Windows Terminal 也要重新启动），再验证：

```bash
amkr --version
# 备用命令
auto-model-key-router --version
```

Windows PowerShell 如果仍然提示“无法将 amkr 识别为命令”，先用下面的命令确认实际安装目录：

```powershell
uv tool dir --bin
pipx environment --value PIPX_BIN_DIR
```

然后把输出目录加入当前用户的 PATH，并重新打开终端。也可以直接使用 PATH 无关的临时方式验证安装是否成功：

```powershell
uvx --from auto-model-key-router amkr --version
```

如果 `uvx` 能运行而 `amkr` 不能运行，说明安装和 console script 没问题，缺的是 PATH。安装状态可分别用 `pipx list` 或 `uv tool list` 查看。

启用访客 Key 功能：

```bash
pipx install "auto-model-key-router[visitor]"
# 或
uv tool install "auto-model-key-router[visitor]"
```

## 快速开始

### 1. 启动 Terminal UI

```bash
amkr
```

首次启动会在系统缓存目录自动创建配置文件和本地鉴权 Key。你也可以复制示例配置到当前目录：

```bash
cp router-config.example.json router-config.json
amkr --config router-config.json
```

Windows PowerShell 可使用：

```powershell
copy router-config.example.json router-config.json
amkr --config router-config.json
```

### 2. 配置供应商、Key 与模型

在 TUI 中进入：

1. **供应商 → 添加供应商**：输入供应商 ID、Base URL 与第一个 Key 的 API Key。添加时自动探测一次供应商能力（可用模型列表 + 各路由可用性）并建好可服务模型；同一供应商的后续 Key 直接绑定到模型即可。
2. **模型设置**：管理模型别名、路由模式、绑定/解绑 Key（绑定 Key 时可指定上游模型名）。
3. **统一模型**：把 `unified-model` 指向一个真实模型，必要时固定到某个 Key。
4. **一键配置 → 路由服务**：启动或注册本地代理服务。
5. **一键配置 → Claude Code / Codex / Pi Agent**：按需自动写入客户端配置。

### 3. 调用本地代理

默认服务地址是：

```text
http://127.0.0.1:8000
```

请求示例：

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{
    "model": "unified-model",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

也可以把 `model` 写成真实模型 ID、模型 alias，或 `模型ID[key name]` 来显式指定某个 Key。

## 常用命令

```bash
# 打开 TUI
amkr

# 使用指定配置文件打开 TUI
amkr --config router-config.json

# 后台启动 / 查看状态 / 停止
auto-model-key-router --config router-config.json --serve
auto-model-key-router --config router-config.json --status
auto-model-key-router --config router-config.json --stop

# 注册、管理系统服务
auto-model-key-router --config router-config.json --install-service
auto-model-key-router --config router-config.json --service status
auto-model-key-router --config router-config.json --service restart

# 查询 AMKR 监听 IP 和端口
auto-model-key-router --config router-config.json --show-address

# 获取当前 AMKR 的本地授权 Key（也可使用 --show-api-key）
auto-model-key-router --config router-config.json --get-key

# 查看配置摘要、日志与统计
auto-model-key-router --config router-config.json --show-config
auto-model-key-router --config router-config.json --show-logs 50

# 管理 unified-model
auto-model-key-router --config router-config.json --show-unified-model
auto-model-key-router --config router-config.json --switch-model gpt-4o-mini
auto-model-key-router --config router-config.json --switch-key auto
```

## 配置示例

```json
{
  "config_version": 4,
  "host": "127.0.0.1",
  "port": 8000,
  "request_timeout": 60,
  "stream_first_byte_timeout": 90,
  "stream_idle_timeout": 180,
  "max_retries": 2,
  "key_failure_threshold": 2,
  "key_cooldown_seconds": 60,
  "local_api_key": "amkr_your-local-api-key",
  "providers": {
    "openai": {
      "base_url": "https://api.openai.com",
      "routes": {
        "openai": "v1/chat/completions",
        "responses": "v1/responses",
        "images": "v1/images/generations"
      },
      "keys": {
        "main": {"api_key": "sk-your-first-upstream-key"},
        "backup": {
          "api_key": "sk-your-second-upstream-key",
          "allow_visitor": true
        }
      },
      "capabilities": {
        "models": ["gpt-4o-mini"],
        "route_status": {"openai": "ok", "responses": "ok", "images": "ok"},
        "errors": {},
        "checked_at": "2026-01-01T00:00:00+00:00"
      }
    },
    "tokenplan": {
      "base_url": "https://example.com/tokenplan",
      "routes": {"anthropic": "anthropic/"},
      "keys": {
        "mimo": {"api_key": "sk-your-third-upstream-key"}
      }
    }
  },
  "models": {
    "gpt-4o-mini": {
      "aliases": ["fast-mini"],
      "routing_mode": "round_robin",
      "reasoning_effort": "medium",
      "targets": [
        {"provider": "openai", "key": "main", "upstream_model": "gpt-4o-mini"},
        {"provider": "tokenplan", "key": "mimo", "upstream_model": "gpt-4o-mini"}
      ]
    }
  },
  "unified_model": {
    "default": {
      "primary": {"model": "gpt-4o-mini", "key": null}
    }
  }
}
```

> `local_api_key` 是客户端访问本地 AMKR 的 Key；`providers.*.keys.*.api_key` 是真实供应商 Key；模型通过 `models.*.targets[]` 按 `{provider, key, upstream_model}` 粒度绑定供应商 Key，`upstream_model` 是发给上游的真实模型名（默认同本地模型 ID）。`providers.*.capabilities` 是供应商级探测缓存：`models` 为探测到的可服务模型清单，`route_status` 为各协议路由的可用性，`errors` / `checked_at` 记录探测错误与时间；首次添加 Key 时自动探测一次，之后可在 TUI「供应商 → 刷新能力探测」或管理 API 手动刷新。旧版 v1/v2/v3 配置会在加载时自动迁移为 v4 并写回，无需手工修改。

流式请求使用分段超时：`stream_first_byte_timeout`（默认 90 秒）覆盖等待上游响应头和第一块响应体的总时间，`stream_idle_timeout`（默认 180 秒）限制首块之后相邻响应块的等待时间，两者都必须大于 0。响应头返回前超时会按现有重试策略切换 Key；下游流建立后超时只结束当前流，不会自动重放请求。可在 TUI 的 **CLI 设置 → 超时配置** 中统一调整普通请求和两个流式超时。

## 文档

- [完整使用教程](docs/USAGE.md)：从安装、配置、启动、请求到 Claude Code / Codex 接入的完整流程。
- [CLI 参考](docs/CLI.md)：所有命令行参数与示例。
- [HTTP API 参考](docs/API.md)：代理、健康检查、统计和管理接口。
- [更新日志](CHANGELOG.md)：版本变更记录。
- [配置示例](router-config.example.json)：可复制修改的完整 JSON 示例。

## 访客 Key 简介

安装 `auto-model-key-router[visitor]` 后，可以用固定 Key `amkr-visitor` 暴露受限公共模型。只有设置了 `allow_visitor: true` 的上游 Key 才能被访客使用，访客看到的模型名格式为 `amkr-{真实模型ID}`。

详细限制和示例见 [完整使用教程：使用访客 Key](docs/USAGE.md#13-使用访客-key)。

## 开发

```bash
git clone https://github.com/sparr68/auto-model-key-router.git
cd auto-model-key-router
pip install -e ".[test]"
pytest
```

## 安全提示

- 不要把真实上游 API Key 提交到 Git。
- `local_api_key` 为空会关闭本地鉴权；仅建议在可信本机环境使用。
- `amkr --get-key` / `--show-api-key` 会直接输出本地授权 Key，请勿在共享终端、日志或 CI 输出中执行。
- 如果监听 `0.0.0.0` 或暴露到局域网/公网，请务必启用本地鉴权并配置防火墙。

## License

MIT
