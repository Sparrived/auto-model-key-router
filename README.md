Auto Model Key Router 是一个轻量的本地 API key 路由服务。它支持为同一个模型 ID 配置多个 API key，并以轮询/优先级的方式把请求自动分配到可用 key；当上游返回认证、限流或服务错误时，会依次切换到已配置的其他 key，只有单 key 配置才会按重试次数重复尝试同一个 key。

## 功能

- 支持 OpenAI-compatible `/v1/*` 请求转发
- 支持同一模型 ID 配置多个 key
- 支持按模型维度选择“分流”或“优先级”路由模式
- 支持 key 失败后自动尝试下一个 key
- 支持请求量、成功/失败、重试、状态码、Token 使用量和缓存命中统计
- 支持 Rich CLI 展示配置摘要、交互式添加配置和日志板块
- 支持通过 GitHub 检查新版本、更新提示和手动更新
- 支持 `/health`、`/metrics` 和 `/v1/models` 本地接口

## 安装

```bash
pip install -e .
```

## 配置

首次运行时，如果 `router-config.json` 不存在，程序会自动生成一个空配置文件。你也可以复制示例配置：

```bash
copy router-config.example.json router-config.json
```

编辑 `router-config.json`：

```json
{
  "host": "127.0.0.1",
  "port": 8000,
  "default_base_url": "https://api.openai.com",
  "request_timeout": 60,
  "max_retries": 2,
  "metrics_db_path": "",
  "log_file_path": "",
  "local_api_key": "amkr-generated-local-api-key",
  "models": [
    {
      "id": "gpt-4o-mini",
      "aliases": ["gpt-4o-mini-display", "fast-mini"],
      "routing_mode": "round_robin",
      "keys": [
        { "name": "key-1", "api_key": "sk-your-first-key" },
        { "name": "key-2", "api_key": "sk-your-second-key" }
      ]
    }
  ]
}
```

也可以为某个 key 单独配置 `base_url`，用于接入其他 OpenAI-compatible 上游。

每个模型配置中的 `id` 是统一路由用的真实模型 ID；`aliases` 是额外显示名称或别名。客户端请求中使用 `id` 或任意 `aliases` 都会映射到同一个模型配置和同一组 key。

`routing_mode` 支持两种模式：

- `round_robin`：分流模式，按配置顺序轮询多个 key，把请求均匀分配到不同 key，这是默认模式。
- `priority`：优先级模式，每次请求优先使用配置中排在最前面的 key；只有当前 key 请求失败且状态码可重试，才会按顺序尝试后面的 key。

默认配置文件也会保存在同一个系统应用缓存目录中，文件名为 `router-config.json`。如果当前目录存在旧的 `router-config.json`，首次未指定 `--config` 启动时会自动复制到默认缓存目录。

`metrics_db_path` 和 `log_file_path` 为空时会使用系统默认应用缓存目录：

- Windows：`%LOCALAPPDATA%\\AutoModelKeyRouter\\`
- macOS：`~/Library/Caches/AutoModelKeyRouter/`
- Linux：`${XDG_CACHE_HOME:-~/.cache}/auto-model-key-router/`

默认文件名为：

- 配置文件：`router-config.json`
- SQLite 计量存档：`metrics.sqlite3`
- 服务运行日志：`server.log`
- 后台服务 PID：`server.pid`，与日志文件同目录

如果需要，也可以把 `metrics_db_path` 和 `log_file_path` 配成绝对路径。

## 运行

启动默认进入 Terminal UI：

```bash
amkr --config router-config.json
```

也可以使用完整命令名：

```bash
auto-model-key-router --config router-config.json
```

主菜单包含：

- 管理系统服务 / 开机自启动
- 交互式添加模型 / API key
- 生成 / 重置本地 API key
- 配置监听地址与端口
- 查看日志板块
- 检查版本更新
- 退出

Terminal UI 只负责配置与服务管理，不直接承载一次性的启动逻辑。需要直接启动本地服务时使用命令行入口：

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

Terminal UI 中的“管理系统服务 / 开机自启动”会进入服务管理子菜单，可直接安装、卸载、启动、停止、重启和查看状态。

只查看配置摘要：

```bash
auto-model-key-router --config router-config.json --show-config
```

覆盖监听地址：

```bash
auto-model-key-router --config router-config.json --host 0.0.0.0 --port 8000
```

也可以在 Terminal UI 中通过“监听配置”统一配置监听地址与端口。默认只监听 `127.0.0.1`，配置为 `0.0.0.0` 时会接受所有可达网络的连接；如果机器暴露在公网或未受信任网络中，请务必启用本地鉴权、限制防火墙访问，并避免泄露上游 API Key。Terminal UI 首页会在检测到 `0.0.0.0` 时显示显眼风险提示。

如果配置文件没有模型，直接启动服务时会自动进入交互式配置，依次输入模型 ID、Key 名称、上游 `base_url` 和 API key。

也可以通过命令直接查看日志板块，默认显示最近 20 行运行日志和最近 20 条请求记录：

```bash
auto-model-key-router --config router-config.json --show-logs
```

指定日志行数和请求记录条数：

```bash
auto-model-key-router --config router-config.json --show-logs 50
```

检查 GitHub Release 最新版本：

```bash
auto-model-key-router --check-update
```

手动更新到 GitHub Release 最新版本：

```bash
auto-model-key-router --update
```

Terminal UI 启动时会快速检查 GitHub Release。如果发现新版本，首页会显示更新提示，也可以进入“版本更新”菜单重新检查或确认手动更新。更新完成后需要重启当前终端和正在运行的后台/系统服务，让新版本生效。

## 请求示例

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-local-api-key" \
  -d "{\"model\":\"gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
```

本地服务会根据请求体中的 `model` 字段选择对应 key，并把请求转发到该 key 配置的上游 `/v1/chat/completions`。

也兼容 Anthropic Messages 和 OpenAI Responses 风格的消息输入：

- 请求 `/v1/messages` 时会把 Anthropic 顶层 `system` 转为 system message，并把 `content` 中的 `text`、base64 `image` 块转为 OpenAI-compatible 消息块后转发到上游 `/v1/chat/completions`。
- 请求 `/v1/responses` 时会把 `instructions` 转为 system message，把 `input` 字符串或消息数组转为 `messages` 后转发到上游 `/v1/chat/completions`。
- 请求 `/v1/chat/completions` 时也会兼容 Anthropic 风格的顶层 `system` 和 Responses 风格的 content part 类型。

## 本地鉴权

首次生成配置文件时会自动生成 `local_api_key`。如果旧配置中该字段为空，程序加载配置时也会自动补齐。也可以在 Terminal UI 中选择“生成 / 重置本地 API key”重新生成。

设置后，客户端访问本地 `/v1/*` 接口时需要传入：

```bash
Authorization: Bearer your-local-api-key
```

也支持使用：

```bash
x-api-key: your-local-api-key
```

如果 `local_api_key` 为空，则不启用本地鉴权。

## 计量统计

服务会把计量数据写入 SQLite 存档。`metrics_db_path` 为空时默认写入系统应用缓存目录下的 `metrics.sqlite3`，也可以通过配置项 `metrics_db_path` 修改存档路径，并通过 `/metrics` 查看聚合统计：

```bash
curl http://127.0.0.1:8000/metrics
```

返回数据包含：

- `total`：全局累计统计
- `models`：按真实模型 ID 汇总的统计
- `requested_models`：按客户端请求使用的模型名/别名汇总的统计
- `model_requested_models`：在真实模型 ID 下按请求模型名/别名拆分的统计
- `keys`：按真实模型 ID 和 key 名称拆分的统计
- `requests`：实际尝试上游 key 的次数
- `successes` / `failures`：成功与失败次数
- `retries`：因上游错误或请求异常触发切换 key 的次数
- `prompt_tokens` / `completion_tokens` / `total_tokens`：从上游响应 `usage` 字段提取的 Token 用量
- `cached_tokens`：OpenAI-compatible `prompt_tokens_details.cached_tokens` 或顶层 `cached_tokens` 的缓存 Token 数
- `cache_creation_input_tokens` / `cache_read_input_tokens`：Anthropic-compatible 缓存写入与读取 Token 数
- `cache_hits` / `cache_misses` / `cache_hit_rate`：按请求维度统计缓存命中、未命中和命中率
- `cached_token_rate`：缓存 Token 占输入 Token 的比例
- `total_duration_ms` / `avg_duration_ms` / `min_duration_ms` / `max_duration_ms`：上游响应耗时统计，单位毫秒
- `status_codes`：上游响应状态码分布
- `database_path`：当前 SQLite 存档路径

统计记录会持久化保存，服务重启后 `/metrics` 会继续基于同一个 SQLite 文件聚合历史数据。
