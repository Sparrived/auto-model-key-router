# Auto Model Key Router

一个本地 OpenAI-compatible API 路由器：把多个模型和多个上游 API Key 统一收口到本地服务，自动分流、失败切换、统计调用，并可一键接入 Claude Code / Codex。

## 主要能力

- **多 Key 路由**：同一模型可配置多个 Key，支持 `round_robin`、`priority`、`only_first`。
- **失败切换与冷却**：遇到 `401/403/429/5xx` 等可重试错误时自动重试或切换 Key，并持久化冷却状态。
- **统一模型名**：客户端固定请求 `unified-model`，真实模型和固定 Key 可在路由器侧随时切换。
- **OpenAI-compatible 代理**：支持 `/v1/chat/completions`、`/v1/models`，并兼容 Claude Code 的 `/v1/messages` 与 Codex 的 `/v1/responses`；可为不同协议模式配置上游额外路径。
- **Terminal UI 管理**：在 TUI 中配置模型、Key、统一模型、服务注册和客户端接入。
- **访客 Key**：安装 `visitor` extra 后，可用固定访客 Key 暴露受限公共模型。
- **统计与日志**：记录本地/访客调用、模型、Key、状态码、token、重试、延迟等指标。

## 安装

需要 Python `>=3.12`。

```bash
pipx install auto-model-key-router
# 或
uv tool install auto-model-key-router
```

启用访客 Key 功能：

```bash
pipx install "auto-model-key-router[visitor]"
# 或
uv tool install "auto-model-key-router[visitor]"
```

安装后可使用两个等价命令：

```bash
amkr --version
auto-model-key-router --version
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

### 2. 配置模型与 Key

在 TUI 中进入：

1. **模型 Key**：添加真实模型和上游 API Key，并可在管理 Key 中对当前 Key 或所有 Key 探测 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 可用性。
2. **统一模型**：把 `unified-model` 指向一个真实模型，必要时固定到某个 Key。
3. **一键配置 → 路由服务**：启动或注册本地代理服务。
4. **一键配置 → Claude Code / Codex**：按需自动写入客户端配置。

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
  "host": "127.0.0.1",
  "port": 8000,
  "default_base_url": "https://api.openai.com",
  "upstream_routes": {
    "https://example.com/tokenplan": {
      "anthropic": "anthropic/"
    }
  },
  "request_timeout": 60,
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
      "keys": [
        {"name": "openai-main", "api_key": "sk-your-first-upstream-key"},
        {"name": "openai-backup", "api_key": "sk-your-second-upstream-key"},
        {
          "name": "mimo-tokenplan",
          "api_key": "sk-your-third-upstream-key",
          "base_url": "https://example.com/tokenplan"
        }
      ]
    }
  ]
}
```

> `local_api_key` 是客户端访问本地 AMKR 的 Key；`keys[].api_key` 是 AMKR 转发到上游模型服务时使用的真实供应商 Key。

## 文档

- [完整使用教程](docs/USAGE.md)：从安装、配置、启动、请求到 Claude Code / Codex 接入的完整流程。
- [CLI 参考](docs/CLI.md)：所有命令行参数与示例。
- [HTTP API 参考](docs/API.md)：代理、健康检查、统计和管理接口。
- [更新日志](docs/CHANGELOG.md)：版本变更记录。
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
- 如果监听 `0.0.0.0` 或暴露到局域网/公网，请务必启用本地鉴权并配置防火墙。

## License

MIT
