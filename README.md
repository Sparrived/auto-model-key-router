# Auto Model Key Router

一个本地 OpenAI-compatible API 路由器：把多个模型和多个上游 API Key 统一收口到本地服务，自动分流、失败切换、统计调用，并可一键接入 Claude Code / Codex。

AMKR 是单个 Go 二进制：配置、WebUI 静态资产（`//go:embed`）与 SQLite 指标库驱动全部编在里面，运行时不需要 Python、node 或任何额外依赖。浏览器里的 WebUI 是唯一界面；随 Python 版发布的终端交互界面已一并退役。

## 主要能力

- **按模型管理 Key**：Key 属于供应商（`providers.*.keys`），模型通过 `targets[]`（`{provider, key, upstream_model}`）绑定一个或多个 Key；同一 Key 可服务多个模型，支持 `round_robin`、`priority`、`only_first`。
- **失败切换与冷却**：遇到 `401/403/429/5xx` 等可重试错误时自动重试或切换 Key，并在进程内临时冷却异常 Key。
- **统一模型名**：客户端固定请求 `unified-model`，真实模型和固定 Key 可在路由器侧随时切换。
- **任务路由**：把任务名（`TASK_XXXXXX`）直接当模型名传，由路由器决定用哪个模型（首选 + 备选）和哪组采样参数；调用方改不了这些参数。任务名只在工作空间内唯一，用 `X-AMKR-Workspace` 头隔离不同团队的任务集合。
- **每个 Key 独立探测**：模型清单按 Key 缓存（同一供应商不同 Key 可见模型可能不同），添加 Key 时自动探测该 Key，也可在 WebUI 的供应商页或管理 API 手动刷新。
- **OpenAI-compatible 代理**：支持 `/v1/chat/completions`、`/v1/models`、`/v1/embeddings`、图像生成与编辑，并兼容 Claude Code 的 `/v1/messages` 与 Codex 的 `/v1/responses`；升级到 `/v1/*` 的 WebSocket 连接会被转发到上游。可为不同协议模式配置上游额外路径。
- **WebUI 管理**：浏览器里配置供应商与 Key、模型路由、统一模型、任务路由、客户端接入、服务注册和运行设置。
- **访问密钥**：给外部使用者分发受限凭据，每把密钥各自限定可用哪些供应商、哪些模型；可随时停用、收窄与轮换。
- **统计与日志**：记录本机/工作空间/访问密钥调用、模型、Key、状态码、token、重试、延迟等指标，经 `/metrics*`、`/api/logs` 与 WebUI 的概览、用量统计、服务日志页查看。
- **成本估算**：用 models.dev 的公开单价折算本机 token 用量（USD），在 WebUI 的概览、用量统计与「成本」页查看。金额是派生读数、不落库，且拿不到单价时显示 `—` 而不是 `$0`。

## 安装

推荐从 GitHub Releases 下载预编译二进制（Windows / macOS / Linux，amd64 + arm64），安装脚本会校验 sha256 后再落盘。

### 一行安装（Linux / macOS）

```bash
curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | sh
```

装到 `/usr/local/bin`（没有写权限时退回 `~/.local/bin`）。指定版本或安装目录：

```bash
# 安装指定版本
curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | sh -s -- --version 5.0.0

# 换一个安装目录
curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | INSTALL_DIR="$HOME/bin" sh
```

脚本会打印它做了什么、装到了哪里；平台或架构没有对应的发布物时会明确报错。

### 一行安装（Windows / PowerShell）

```powershell
irm https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.ps1 -OutFile "$env:TEMP\amkr-install.ps1"; & "$env:TEMP\amkr-install.ps1"
```

装到 `%LOCALAPPDATA%\Programs\AutoModelKeyRouter\amkr.exe`，**不需要管理员权限**。指定版本：

```powershell
irm https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.ps1 -OutFile install.ps1
.\install.ps1 -Version 5.0.0
```

### Docker

每个正式版本都会构建并推送镜像到 GHCR（包已公开，可匿名拉取；`:<版本号>` 与 `:latest` 都有）：

```bash
docker run -d --name amkr -p 8000:8000 -v amkr-data:/data \
  ghcr.io/sparrived/auto-model-key-router:6.0.1
```

也可以自己从仓库构建镜像：

```bash
docker build -t amkr .
docker run -d --name amkr -p 8000:8000 -v amkr-data:/data amkr
```

> 自建镜像时想打进正确版本号就传 `--build-arg VERSION=`。注意**不带 `v` 前缀**，与发布流程
> 一致（它取的是 `refs/tags/v*` 去掉 `v` 的部分），所以要对 `git describe --tags --abbrev=0`
> 的输出再 `sed 's/^v//'`。不传的话 `amkr --version` 显示源码里的默认值（`Dockerfile` 里那句
> 注释写了原因）。

镜像里配置、指标库与日志都落在 `/data`（`XDG_CACHE_HOME`），挂一个卷即可整体持久化。容器内以 `--host 0.0.0.0` 启动（配置默认监听 `127.0.0.1`，不改的话端口映射进不来），并且用 `--serve-foreground` 前台运行，因此不会尝试打开浏览器。

容器里需要 WebUI 时，把命令换成带上 `--webui` 的那条 —— 它会把开关写进配置并立即生效：

```bash
docker run -d --name amkr -p 8000:8000 -v amkr-data:/data \
  amkr amkr --host 0.0.0.0 --serve-foreground --webui
```

首次启动会在卷里自动生成配置和本地授权 Key，路径是 `/data/auto-model-key-router/router-config.json`。取本地授权 Key：

```bash
docker exec amkr amkr --config /data/auto-model-key-router/router-config.json --get-key
```

删容器不丢数据。**容器内固定监听 `0.0.0.0`，务必保留 `local_api_key`，不要把端口直接暴露到公网**；需要改端口时改配置里的 `port`，再同步调整 `-p`。

容器里建议关掉运维接口：`/api/logs`、`/api/tool`、`/api/service/*`、`/api/integrations/*` 作用于「服务所在的这台机器」（读日志文件、启停进程、注册系统服务、改写本机 Claude Code / Codex 配置），在容器或反向代理后面语义不成立，逐个路径拉黑又容易漏。在启动命令后加 `--no-ops` 即可（写入配置字段 `ops_enabled`，随后启动的服务进程即已关闭）：

```bash
docker run -d --name amkr -p 8000:8000 -v amkr-data:/data \
  amkr amkr --host 0.0.0.0 --serve-foreground --webui --no-ops
```

关闭后这些路径返回 `404`，`/health` 的 `ops_enabled` 字段也会变成 `false`，便于部署时断言。代理、`/health`、`/metrics`、WebSocket 与 `/api/settings` 等配置管理接口不受影响。

### 从源码构建

需要 Go 1.24+。

```bash
git clone https://github.com/Sparrived/auto-model-key-router.git
cd auto-model-key-router
go build ./cmd/amkr          # 产出 amkr（Windows 上是 amkr.exe）
./amkr --version
```

## 快速开始

### 1. 启动服务与 WebUI

```bash
amkr                 # 前台启动服务，并自动打开浏览器里的 WebUI
amkr --no-open       # 不自动打开浏览器（SSH、容器等无桌面环境）
amkr --serve         # 后台启动
```

不带参数时：首次启动会在系统缓存目录自动创建配置文件和本地鉴权 Key，服务起来后用系统默认浏览器打开 WebUI；**服务已经在运行**（例如已注册为系统服务）时只打开 WebUI 并退出 `0`，不会去抢端口。`--serve-foreground`（服务注册调用的那条命令）始终不打开浏览器。打开浏览器失败不算错误，终端会打印地址供手动访问。

默认监听 `127.0.0.1:8000`，所以 WebUI 地址是 `http://127.0.0.1:8000/ui`（端口以配置为准，不限于 8000）。自动生成的配置**默认启用 WebUI**。

### 2. 在 WebUI 里配置

WebUI 左侧导航覆盖日常全部操作：

1. **供应商**：添加供应商（ID、Base URL）与 Key。添加时会自动探测这个新 Key（可用模型列表 + 各路由可用性）并据此建立可服务模型；以后每加一个 Key 只探测该新 Key（不同 Key 可见模型可能不同），也可随时手动刷新探测。
2. **模型路由**：管理模型别名（对外名称）、路由模式、绑定/解绑 Key，并逐条指定发给上游的模型名（同一个模型在各上游叫法不同时各写各的）。
3. **统一模型**：把 `unified-model` 指向一个真实模型，必要时固定到某个 Key。
4. **任务路由**：为 `TASK_XXXXXX` 指定模型与固定采样参数。
5. **集成**：按需把配置写入 Claude Code / Codex / Pi Agent，并可回退。
6. **设置**：请求超时与两条流式超时、本地鉴权 Key、服务启动/停止/系统服务注册、配置导入导出、WebUI 开关、版本检查。
7. **概览 / 用量统计 / 工作空间 / 服务日志**：概览看实时（RPM/TPM、脉搏、趋势、热力图、请求流），用量统计看历史（累计、按天、日内时段、维度明细，窗口可到 1 年或全部），工作空间按空间拆用量并用桑基图给出请求流向（工作空间 → 请求模型 → 实际模型 → 供应商 → 上游模型），服务日志单独成页用于排查。

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

鉴权凭据可以放在 `Authorization: Bearer <key>` 或 `x-api-key` 头里。也可以把 `model` 写成真实模型 ID、模型 alias，或 `模型ID[key name]` 来显式指定某个 Key。

如果配置了任务路由，还可以直接传任务名：

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{
    "model": "TASK_000001",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

任务名对应的模型、备选模型和采样参数都在 AMKR 侧固定，调用方不需要知道真实模型名。详见 [`docs/USAGE.md`](docs/USAGE.md#9-任务路由)。

任务名只在**工作空间**内唯一，用 `X-AMKR-Workspace` 头选择（不带即默认工作空间，也就是配置的顶层 `tasks`）：不同工作空间可以有同名任务，各自指向自己的模型与参数。

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -H "X-AMKR-Workspace: teamA" \
  -d '{"model": "TASK_000001", "messages": [{"role": "user", "content": "hello"}]}'
```

该头不会转发给上游，也不会影响既有调用方（不带头就是原来的行为）。详见 [工作空间](docs/USAGE.md#91-工作空间把任务集合分开)。

工作空间还可以带一把**面板 key**，用来把一个只看得到自己那个空间的控制面板嵌进别的应用：

```html
<iframe src="http://127.0.0.1:8000/ui/panel.html#k=amkr_ws_你的面板key"
        width="100%" height="720" style="border:0"></iframe>
```

这把 key 在创建空间时交付（WebUI 里创建会弹出「复制嵌入片段」，应用侧可以在
`POST /api/workspaces` 里自带）。它只能读写该空间的任务与读数，别的接口一律 `401`，且
**不能**用于 `/v1/*` 代理面。详见 [嵌入工作空间面板](docs/USAGE.md#93-把工作空间面板嵌进你自己的后台)。

同一次创建还会交付一把**推理 key**（`amkr_ik_…`）：它专用于 `/v1` 推理，是 AMKR 作为
**多个项目共用网关**时的凭据。空间由 key 决定（`X-AMKR-Workspace` 被忽略），因此一把
泄漏的 key 不会换来别的空间的推理权限；它调不了任何管理接口，也不能用 `unified-model`。
想让某个项目只能直呼少数模型，给这个空间配 `models` 清单即可（任务名不受清单限制）。
详见 [用推理 key 把一个项目接进来](docs/USAGE.md#94-用推理-key-把一个项目接进来)。

除代理路径外，服务还提供 `GET /health`（无需鉴权）、`GET /v1/models`、`GET /metrics`、`GET /metrics/requests`、`GET /metrics/series`、`GET /ws/events`（事件流，WebSocket），以及 `/api/*` 管理接口（供应商、Key、模型、路由、统一模型、任务、探测、配置导入导出、设置）。完整清单见 [`docs/API.md`](docs/API.md)。

## WebUI

WebUI 是 AMKR 的界面：静态资产随二进制发布（`//go:embed`），没有单独的安装步骤，也没有额外依赖。页面对应上文的供应商、模型路由、统一模型、任务路由、集成与设置。

`/ui` 是否挂载由配置字段 `webui_enabled` 决定，用 `--webui` / `--no-webui` 写入配置（也可以直接改配置文件）：

```bash
amkr --webui        # 启用，并写入配置
amkr --no-webui     # 关闭，并写入配置
```

自动生成的新配置里 `webui_enabled` 是 `true`，`amkr` 起来就能直接用 WebUI。若把它关掉（`--no-webui`），`/ui` 会返回 `404`，而 `/health` 的 `webui_available` 仍是 `true`——那表示资产在二进制里，只是没挂载。挂载与否在服务启动时决定，改动对随后启动的进程生效，已在运行的进程需要重启。

WebUI 的页面：概览、用量统计、**工作空间**、服务日志、**成本**、**账号资源**、供应商、模型路由、统一模型、任务路由、集成、设置。其中「工作空间」页给出各空间的用量读数与请求流向（桑基图，经 `GET /ui/workspace-usage.json`）；「成本」页按 models.dev 的公开单价估算开销——服务端启动时取回一次目录、此后每 6 小时复验（命中 `304` 时不重复下载），通过 `GET /ui/pricing.json` 提供给界面。**金额只能用来比较量级**：它是现算的派生读数（目录更新后历史金额也会变），未匹配到单价的用量不计入合计，也不会被当成 `0`。详见 [成本估算](docs/USAGE.md#14-成本估算基于-modelsdev) 与 [`docs/API.md`](docs/API.md) 的「内置 WebUI 与运维 API」。

「账号资源」页把多个 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 实例的账号与额度汇总到一块看板上：逐账号显示供应商、状态与每个额度窗口的剩余比例，支持同时管多个实例。读数由 AMKR 的服务端代问（浏览器不直接访问 CPA，管理密钥也不常驻页面），额度优先取 CPA 现场查询的结果、退回到它从上游响应头采到的快照；**这些读数只看不拦**，不参与 AMKR 的 Key 冷却与失败切换。见 [多实例账号资源看板](docs/USAGE.md#41-多实例账号资源看板)。

管理面的登录是**独立一页** `login.html`：未授权时 `index.html` 不画任何内容，直接整页跳过去，验通后回到原本要打开的页面。Key 只存 `localStorage`，**不进 URL**（URL 会落进历史记录、Referer 与截图）；`?next=` 只接受同源相对路径，否则就是一个开放重定向。

除此之外，`/ui/` 下还有两个凭据面不同的独立页面：`panel.html` 给嵌入第三方后台的工作空间面板（用面板 key，走 URL fragment），`guest.html` 给访问密钥的持有者看自己的用量（用访问密钥本身）。它们不进管理面，因为管理面是完整权限的界面，配一个受限凭据会让每个链接都 `401`。见 [`docs/PANEL.md`](docs/PANEL.md) 与 [访客看板](docs/USAGE.md#156-让持有者看自己的用量访客看板)。

## 常用命令

```bash
# 前台启动服务并打开 WebUI（已在运行时只打开 WebUI 并退出 0）
amkr
amkr --no-open                                 # 不自动打开浏览器
amkr --config router-config.json               # 使用指定配置文件

# 后台启动 / 查看状态 / 停止
amkr --serve
amkr --status
amkr --stop

# 查询监听地址
amkr --show-address

# 获取本地授权 Key（别名：--get-key、--get-api-key）
amkr --show-api-key

# 查看配置摘要
amkr --show-config

# 注册、管理系统服务（Windows 计划任务 / systemd user unit）
amkr --install-service                         # 等价于 --service install
amkr --service install-user
amkr --service status
amkr --service restart

# 管理 unified-model
amkr --show-unified-model
amkr --switch-model gpt-4o-mini
amkr --switch-key auto                         # 传 auto 恢复自动路由
amkr --unified-target default.primary

# 覆盖监听地址与端口（只对本次运行生效，不写回配置）
amkr --host 0.0.0.0 --port 8000

# 检查新版本（只查 GitHub Releases）
amkr --check-update

# 版本号
amkr --version
```

`--service` 的取值为 `install`、`install-user`、`uninstall`、`start`、`stop`、`restart`、`status`，以及对应的 `*-elevated` 变体（只在 Windows 上有意义，用于 UAC 提权）。`amkr --help` 会打印用法摘要；从源码构建、没有把二进制放进 PATH 时，把上面的 `amkr` 换成 `./amkr`。

## 配置示例

```json
{
  "config_version": 4,
  "host": "127.0.0.1",
  "port": 8000,
  "request_timeout": 60,
  "stream_first_byte_timeout": 60,
  "stream_idle_timeout": 60,
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
        "images": "v1/images/generations",
        "embeddings": "v1/embeddings"
      },
      "keys": {
        "main": {
          "api_key": "sk-your-first-upstream-key",
          "capabilities": {
            "models": ["gpt-4o-mini"],
            "route_status": {"openai": "ok", "anthropic": "ok", "responses": "ok"},
            "errors": {},
            "checked_at": "2026-01-01T00:00:00+00:00"
          }
        },
        "backup": {
          "api_key": "sk-your-second-upstream-key",
          "capabilities": {
            "models": ["gpt-4o-mini"],
            "route_status": {"openai": "ok", "anthropic": "ok", "responses": "ok"},
            "errors": {},
            "checked_at": "2026-01-01T00:00:00+00:00"
          }
        }
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
    },
    "text-embedding-3-small": {
      "targets": [
        {"provider": "openai", "key": "main", "upstream_model": "text-embedding-3-small"}
      ]
    }
  },
  "unified_model": {
    "default": {
      "primary": {"model": "gpt-4o-mini", "key": null}
    },
    "embeddings": {
      "primary": {"model": "text-embedding-3-small", "key": null}
    }
  },
  "tasks": {
    "TASK_000001": {
      "display_name": "长文摘要",
      "model": "gpt-4o-mini",
      "fallback_model": null,
      "params": {"temperature": 0.2, "top_p": 0.9}
    }
  },
  "workspaces": {
    "teamA": {
      "tasks": {
        "TASK_000001": {"model": "claude-sonnet-4", "params": {"temperature": 0.7}}
      }
    },
    "worker": {
      "inference_key": "amkr_ik_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
      "models": ["claude-sonnet-4"]
    }
  }
}
```

> `local_api_key` 是客户端访问本地 AMKR 的 Key；`providers.*.keys.*.api_key` 是真实供应商 Key；模型通过 `models.*.targets[]` 按 `{provider, key, upstream_model}` 粒度绑定供应商 Key，`upstream_model` 是发给上游的真实模型名（默认同本地模型 ID）。`tasks` 是可选的任务路由表，键即客户端传的 `model` 名（如 `TASK_000001`），值为 `{model?, display_name?, fallback_model?, params?}`；`model` 可以省略（任务先作为占位存在，调用时明确报「尚未指定模型」而不是回落到别的模型），`display_name` 是给人在 WebUI 上辨认任务用的中文名、不影响调用；`params` 支持的键为 `temperature`、`top_p`、`top_k`、`frequency_penalty`、`presence_penalty`、`seed`、`stop`、`max_tokens`、`reasoning_effort`，写错键名会在保存时报错。`workspaces` 同样可选，每个键是一个工作空间名，值为 `{api_key?, inference_key?, models?, tasks?}`，`tasks` 的形状与顶层 `tasks` 完全相同；顶层 `tasks` 就是默认工作空间（不带 `X-AMKR-Workspace` 头时命中的那个），任务名只在同一工作空间内需要唯一。`api_key` 是该工作空间的**面板 key**：应用侧把它嵌进自己的后台就能只看管这一个空间（读写本空间任务 + 读本空间用量）。`inference_key` 是**推理 key**：项目代码拿它调 `/v1`，空间由它决定，可直接直呼的模型由 `models` 限定（省略即不限制，空数组即一个都不许直呼）。三者任一存在，该空间即使没有任务也会保留。`access_keys` 是另一类凭据（[见下节](#访问密钥简介)）：每把密钥带 `{name?, key, enabled?, providers?, models?}`，不绑定工作空间，`providers` / `models` 同样用省略表示不限制、空数组表示一个都不许。探测缓存按 Key 存放在 `providers.*.keys.<key>.capabilities`（`models` 为该 Key 探测到的可服务模型清单，`route_status` 为各协议路由的可用性，`errors` / `checked_at` 记录探测错误与时间）；同一供应商的不同 Key 可见模型可能不同，因此每个 Key 独立探测、缓存互不复用。添加 Key 时自动探测该新 Key（探测失败仍会保存 Key，可稍后手动刷新），之后可在 WebUI 的供应商页刷新探测（单个 Key 或批量、可限端点范围），或调用管理 API 的 probe 接口。探测缓存是机器本地信息，配置导出/粘贴（transferable_config）不会携带。旧版 v1/v2/v3 配置会在加载时自动迁移为 v4 并写回，无需手工修改；v3 池级探测元数据与旧 v4 供应商级缓存会折进各 Key 的 capabilities。

工作空间有一套**独立的**导出/导入（`POST /api/workspaces/export|import`）：把一个或多个工作空间连任务带面板 key 一起搬到另一个实例。它与配置导出/导入刻意分成两条通道——那条**剥掉**两把 key、也不含 providers/models 的相反语义，见 [工作空间设计说明](docs/WORKSPACE.md#11-整包迁移一条独立通道)。

流式请求使用分段超时：`stream_first_byte_timeout`（默认 60 秒）覆盖等待上游响应头和第一块响应体的总时间，`stream_idle_timeout`（默认 60 秒）限制首块之后相邻响应块的等待时间，两者都必须大于 0。响应头返回前超时会按现有重试策略切换 Key；下游流建立后超时只结束当前流，不会自动重放请求。普通请求超时与这两个流式超时都可以在 WebUI 的**设置**页统一调整。

## 访问密钥简介

给外部使用者（同事、试用方、第三方应用）分发凭据时，不要把自己的 `local_api_key` 交出去——它是完整权限。改用**访问密钥**：`access_keys` 段里每把密钥带两份清单，限定它能用哪些供应商、能调哪些模型。

```json
{
  "access_keys": {
    "trial-a": {
      "name": "试用账号 A",
      "key": "amkr_ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
      "enabled": true,
      "providers": ["openai"],
      "models": ["gpt-4o-mini", "gpt-4o"]
    }
  }
}
```

`providers` 与 `models` 都可以省略（表示不限制），也可以配成空数组（表示一个都不许）。`models` 按调用方**写的名字**比对，因此别名也能填。访问密钥不能用 `unified-model`、不能调用任务路由，也拿不到 `/metrics` 与管理接口。

用 WebUI 的「访问密钥」页新建最省事：明文只在创建与轮换时显示一次，之后列表只给指纹。也可以走管理 API（`/api/access-keys`）集成到自己的系统里。

持有者可以打开 **`/ui/guest.html`** 用自己的密钥登录，看**这把密钥自己**的用量：请求数/成功率/Token/耗时、按模型与供应商的排行、按上游模型的花费估算，以及最近调用明细（失败与重试都有标记）。看板是只读的，页头只显示密钥名与尾号，不回显明文。

详细限制和示例见 [完整使用教程：使用访问密钥](docs/USAGE.md#15-使用访问密钥)。

## 数据与配置

- **配置**：`config_version` 4 的 JSON 直接可用，升级不需要迁移；默认配置在首次启动时自动生成，路径按「`--config` > `$AMKR_CONFIG` > 系统缓存目录」解析。默认位置还没有配置时，工作目录下遗留的 `router-config.json` 会被复制到默认位置沿用。
- **指标库**：SQLite（纯 Go 驱动，无 cgo），建表语句、7 个索引与 `user_version` 都与 Python 版逐字节一致，已有的 `metrics.sqlite3` 可以直接沿用，不需要重建或导出导入。
- **日志与探测缓存**：默认落在系统缓存目录（Windows 是 `%LOCALAPPDATA%\AutoModelKeyRouter`，macOS 是 `~/Library/Caches/AutoModelKeyRouter`，其余是 `$XDG_CACHE_HOME/auto-model-key-router` 或 `~/.cache/auto-model-key-router`），可在配置里分别用 `log_file_path`、`endpoint_capabilities_path`、`metrics_db_path` 覆盖。

## 开发

需要 Go 1.24+（前端资产用 `//go:embed` 编进二进制，无需 node 即可构建）。

```bash
git clone https://github.com/Sparrived/auto-model-key-router.git
cd auto-model-key-router
go build ./cmd/amkr     # 产出 amkr（Windows 上是 amkr.exe）
go test ./...           # 全部包
```

前端资产的检查（可选，只在改动 `webui/` 时需要）：

```bash
node scripts/webui_module_check.mjs   # 逐个 import webui/ 下的全部 ES 模块
for p in webui/probes/*.mjs; do node "$p" || break; done   # 全部口径探针
```

探针各自只管一个口径（图表、成本、鉴权、面板、栅格、访客看板等），失败时自己退出非 0。
多数探针不带参数时会为每个场景各起一个子进程跑全量，也可以只给一个场景名单跑。
CI 的 `webui` 作业逐个点名运行它们，清单见 [`.github/workflows/ci.yml`](.github/workflows/ci.yml)。

## 文档

- [完整使用教程](docs/USAGE.md)：安装、配置、启动、请求与 Claude Code / Codex 接入。
- [CLI 参考](docs/CLI.md)：命令行参数与示例。
- [HTTP API 参考](docs/API.md)：代理、健康检查、统计和管理接口。
- [工作空间设计说明](docs/WORKSPACE.md)：任务集合隔离的取舍、边界与兼容性契约。
- [嵌入工作空间面板](docs/PANEL.md)：把只看得到自己空间的用量与任务面板嵌进你的后台。
- [更新日志](CHANGELOG.md)：版本变更记录。
- [配置示例](router-config.example.json)：可复制修改的完整 JSON 示例。

> `docs/` 下的 `USAGE.md` / `CLI.md` / `API.md` / `WORKSPACE.md` / `PANEL.md` 描述当前 Go 实现：安装方式为预编译二进制 + 一行脚本，界面为 WebUI，命令行参数以 `amkr --help` 与实际行为为准。

## 安全提示

- 不要把真实上游 API Key 提交到 Git。
- `local_api_key` 为空会关闭本地鉴权；仅建议在可信本机环境使用。
- `amkr --show-api-key`（别名 `--get-key` / `--get-api-key`）会直接输出本地授权 Key，请勿在共享终端、日志或 CI 输出中执行。
- 如果监听 `0.0.0.0` 或暴露到局域网/公网，请务必启用本地鉴权并配置防火墙。
- 部署到容器或反向代理后面时用 `--no-ops` 关闭运维接口，避免把宿主机的服务控制与客户端配置改写能力暴露出去。
- 服务进程会访问 `https://models.dev/api.json` 以获取成本估算用的模型单价（只发送 `User-Agent` 与 `If-None-Match`，**不发送**任何本机配置、Key 或用量数据；价格目录只驻内存，不落盘）。完全离线部署时成本相关读数会显示 `—`，代理与管理功能不受影响。

## License

MIT
