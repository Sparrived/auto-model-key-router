# Auto Model Key Router (AMKR)

<p align="center">
  <img src="webui/favicon.svg" alt="AMKR Logo" width="84" height="84" />
</p>

<p align="center">
  <b>轻量、高性能的本地多模型与多 API Key 智能路由网关</b><br>
  <i>Single-binary OpenAI / Anthropic / Codex-compatible local router & intelligent proxy gateway.</i>
</p>

<p align="center">
  <a href="README.md"><b>简体中文</b></a> | <a href="README_EN.md"><b>English</b></a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go Version" />
  <img src="https://img.shields.io/badge/Release-v6.9.0-blue?style=flat-square" alt="Version" />
  <img src="https://img.shields.io/badge/Platform-Windows%20|%20macOS%20|%20Linux-lightgrey?style=flat-square" alt="Platform" />
  <img src="https://img.shields.io/badge/Docker-GHCR%20Ready-2496ED?style=flat-square&logo=docker" alt="Docker" />
  <img src="https://img.shields.io/badge/License-MIT-green?style=flat-square" alt="License" />
</p>

---

## 📸 运行概览

![AMKR 运行概览控制台](<docs/screenshots/overview.png>)

---

## 💡 为什么选择 AMKR？

在大模型开发与日常使用中，开发者常常面临以下痛点：
- **多 Key 轮询与限流**：手头拥有多个供应商或账号的 API Key，手动切换繁琐，遇到上游 `429 Too Many Requests` 或欠费时容易导致业务或客户端中断。
- **协议方言割裂**：Claude Code 使用 Anthropic `/v1/messages` 协议，Codex 使用 `/v1/responses` 协议，标准应用使用 OpenAI `/v1/chat/completions`。
- **团队协作与凭据安全**：不想把完整权限的主 API Key 直接发给同事或测试脚本，需要精细化控制每个人、每个项目能访问的供应商与模型范围。
- **参数控制与治理**：企业或项目中需要固定某些 Prompt 场景的采样温度（Temperature）、Top-P 或思考强度，避免调用方随意覆盖导致结果劣化。

**Auto Model Key Router (AMKR)** 是一个用纯 Go 编写的高性能本地 API 路由器。它是**单个二进制文件**，内置纯 Go SQLite 指标库与完整响应式 WebUI（经 `//go:embed` 打包），运行时**零第三方依赖**（无需 Python、Node.js 或外部数据库）。

---

## ✨ 核心特性

- 🚀 **极简单二进制交付**：Go 编写，内存占用极低、启动毫秒级，配置、WebUI 资产与 SQLite 指标引擎全内置。
- 🔄 **智能轮询与故障转移**：
  - 支持 `round_robin`（轮询）、`priority`（主备优先级）、`only_first` 等路由策略。
  - 遇到 `401/403/429/5xx` 自动重试并切换候选 Key，并在进程内临时冷却异常节点。
  - **欠费智能熔断与恢复**：精准识别 `402`、`insufficient_quota`、`Insufficient Balance`、`欠费` 等余额不足信号，按阶梯窗口（初始 300s，翻倍退避至 30 分钟）自动限流该账号，窗口期满自动半开探测并在恢复时立即重入池，候选全被熔断时安全返回 503 避免无效重试。
  - **并发上限（供应商级 + Key 级）**：用 `providers.<id>.max_concurrency` 限制该供应商所有 Key、所有模型上同时在途的请求数，用 `providers.<id>.keys.<key>.max_concurrency` 限制单把上游 Key 的在途请求数（同一把 Key 被多个模型引用时共用一个计数）；缺省、`null` 或 `0` 表示不限制。达到上限的 Key 与供应商会被硬性排除（不做冷却式软回退），某个模型的所有候选都被挡下时直接返回 `429`，不向上游发出注定失败或排队的请求。
- 🎯 **统一模型名 (`unified-model`)**：客户端固定请求 `unified-model`，路由器按入站端点族（对话、图像、嵌入、语音合成、语音识别、视频、重排）动态分流到预设模型，换模型无需改动客户端代码。
- 📋 **任务路由 (`TASK_XXXXXX`)**：将业务任务抽象为模型名。服务端统一固化主选模型、备选模型与采样参数（`temperature`、`top_p`、`reasoning_effort`、`max_tokens` 等），调用方不可篡改，实现提示词与推理参数集中治理。
- 🏢 **多租户工作空间 (Workspaces)**：
  - 使用 `X-AMKR-Workspace` 请求头彻底隔离任务集合与指标统计。
  - **嵌入式面板 Key**（`amkr_ws_...`）：可将独立空间的任务管理与流向面板通过 `<iframe>` 安全无缝嵌入自研运维后台。
  - **项目专属推理 Key**（`amkr_ik_...`）：直接面向项目代码分发，空间强绑定且支持模型白名单限制，单 Key 泄漏不波及全局。
- 🔑 **受限访问密钥 (Access Keys)**：对外分发细粒度凭据（`amkr_ak_...`），可绑定允许使用的供应商列表与模型白名单；持有者可访问专属的只读用量看板（`/ui/guest.html`）。
- 🌐 **全协议栈与多模态兼容**：
  - OpenAI 兼容：`/v1/chat/completions`、`/v1/models`、`/v1/embeddings`、图像生成/编辑（`/v1/images/*`）、语音合成/转写/翻译（`/v1/audio/*`）、视频（`/v1/videos/*`）、文本重排（`/v1/rerank`）。
  - Claude Code 兼容：Anthropic 原生 `/v1/messages` 接口直通与格式映射。
  - OpenAI Codex 兼容：`/v1/responses` 接口原生转发。
  - 全链路 WebSocket 原生升级代理。
- 📊 **多维监控与成本可视化**：
  - 实时监控 RPM、TPM、成功率、P95/平均延迟、流式首字耗时（TTFT）。
  - 全链路请求桑基流向图（工作空间 → 请求模型 → 真实模型 → 供应商 → 上游 Key → 上游模型）。
  - 状态监控页：按供应商 / 上游模型 / 上游 Key 铺开的健康色块矩阵（固定最近 12 小时、每格 15 分钟，成功率 / 首字延迟 / 缓存率三口径着色），配错误原因归因与模型明细。
  - 基于 [models.dev](https://models.dev) 公开单价自动核算 Token 开销（USD）。
- 🔌 **一键客户端集成**：在 WebUI 中一键将本地 Claude Code、Codex、Pi Agent 接驳至 AMKR，支持无损自动回退。

---

## 🏛️ 系统架构

```text
┌────────────────────────────────────────────────────────────────────────┐
│                              API Clients                               │
│   Claude Code CLI  │  OpenAI SDK / LangChain  │  Codex  │  Web / Apps  │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │ HTTP / WebSocket (Bearer / x-api-key)
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│                     Auto Model Key Router (AMKR)                       │
│                                                                        │
│   ┌───────────────────────┐  ┌──────────────────────────────────────┐  │
│   │   Auth & Workspaces   │  │           Protocol Gateway           │  │
│   │  - Local Master Key   │  │  - /v1/chat/completions (OpenAI)     │  │
│   │  - Project Inf Keys   │  │  - /v1/messages (Claude Code)        │  │
│   │  - Guest Access Keys  │  │  - /v1/responses (Codex)             │  │
│   │  - Workspace Header   │  │  - /v1/embeddings, /v1/audio, etc.   │  │
│   └───────────┬───────────┘  └──────────────────┬───────────────────┘  │
│               │                                 │                      │
│               ▼                                 ▼                      │
│   ┌─────────────────────────────────────────────────────────────────┐  │
│   │                      Smart Routing Engine                       │  │
│   │  - Unified Model Resolver       - Task Router (Params Lock)     │  │
│   │  - Round-Robin / Priority       - Error Cooldown & 402 Breaker  │  │
│   └─────────────────────────────────┬───────────────────────────────┘  │
│                                     │                                  │
│         ┌───────────────────────────┴───────────────────────────┐      │
│         ▼                                                       ▼      │
│   ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌───────────┐  │
│   │ Embedded UI  │  │ SQLite Store │  │ Cost Engine  │  │ Health &  │  │
│   │  (WebUI:8000)│  │ (Zero-cgo DB)│  │ (models.dev) │  │ AutoProbe │  │
│   └──────────────┘  └──────────────┘  └──────────────┘  └───────────┘  │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │ Upstream API Requests
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│                           Upstream Providers                           │
│   Anthropic API   │   OpenAI API   │   DeepSeek API   │  SiliconFlow   │
└────────────────────────────────────────────────────────────────────────┘
```

---

## 📦 安装指南

推荐从 [GitHub Releases](https://github.com/Sparrived/auto-model-key-router/releases) 获取经过预编译签名的二进制产物（支持 Windows / macOS / Linux，涵盖 amd64 与 arm64）。

### 方式一：一键脚本安装

#### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | sh
```

> 默认安装至 `/usr/local/bin`（若无 root 权限自动降级至 `~/.local/bin`）。支持通过环境变量指定版本与路径：
> ```bash
> curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | sh -s -- --version 6.9.0
> ```

#### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.ps1 -OutFile "$env:TEMP\amkr-install.ps1"; & "$env:TEMP\amkr-install.ps1"
```

> 安装至 `%LOCALAPPDATA%\Programs\AutoModelKeyRouter\amkr.exe`，**无需管理员权限**并自动写入用户 PATH 环境变量。

---

### 方式二：Docker 容器化部署

AMKR 在 GitHub Container Registry (GHCR) 上提供多架构官方镜像，支持直接拉取：

```bash
docker run -d \
  --name amkr \
  -p 8000:8000 \
  -v amkr-data:/data \
  ghcr.io/sparrived/auto-model-key-router:latest \
  amkr --host 0.0.0.0 --serve-foreground --webui
```

> [!TIP]
> 容器化部署建议附加 `--no-ops` 参数以禁用宿主机级别的服务控制和配置覆写接口：
> ```bash
> docker run -d --name amkr -p 8000:8000 -v amkr-data:/data \
>   ghcr.io/sparrived/auto-model-key-router:latest \
>   amkr --host 0.0.0.0 --serve-foreground --webui --no-ops
> ```
> 获取容器内生成的本地鉴权 Key：
> ```bash
> docker exec amkr amkr --config /data/auto-model-key-router/router-config.json --get-key
> ```

---

### 方式三：从源码构建

构建需安装 **Go 1.24+**：

```bash
git clone https://github.com/Sparrived/auto-model-key-router.git
cd auto-model-key-router
go build ./cmd/amkr          # 编译产出 amkr (Windows 下为 amkr.exe)
./amkr --version
```

---

## 🚀 快速上手

### 1. 启动服务与 Web 控制台

```bash
amkr                 # 前台启动并自动在默认浏览器中打开 WebUI
amkr --no-open       # 启动服务但不自动弹出浏览器（适合无桌面或服务器环境）
amkr --serve         # 作为后台服务守护运行
```

首次运行会在系统应用缓存目录下自动生成配置文件 [router-config.json](<router-config.example.json>) 与初始本地管理 Key。默认服务监听地址为 `http://127.0.0.1:8000`，WebUI 入口为 `http://127.0.0.1:8000/ui/`。

获取本地管理凭据：
```bash
amkr --show-api-key
```

---

### 2. 通过 WebUI 进行可视化配置

打开 WebUI 即可直观进行全部路由策略管理：

| 模块 | 核心功能 |
| :--- | :--- |
| **供应商** | 添加 OpenAI、Anthropic、DeepSeek、SiliconFlow 等上游，配置每个 Key 与并发上限，并自动探测模型清单与可用路由 |
| **模型路由** | 配置统一对外别名（如 `gpt-4o`、`claude-3-7-sonnet`）、新建模型名称、绑定多供应商 Key 并指定 `round_robin` 或 `priority`；同一个名称下还能按端点族（文本 / 图像 / 嵌入 / 语音合成 / 语音识别 / 视频 / 重排）分别指定模型。列表第一项固定是 `unified-model`——**统一模型也在这里配置**：把 `unified-model` 映射到主选模型，并给各端点族单独分流 |
| **任务路由** | 为业务场景定义 `TASK_XXXXXX` 别名，并固化模型与采样超参数（温度、Top-P、思考强度等） |
| **工作空间** | 创建多租户工作空间，派发嵌入式面板 Key 与专属推理 Key |
| **访问密钥** | 为团队成员或测试系统生成受限 API Key，精确限定可调用的供应商与模型范围 |
| **客户端集成** | 一键自动写入 Claude Code、Codex、Pi Agent 的配置文件，即刻开始本地智能编程 |

---

### 3. 发送推理请求

#### 标准 OpenAI 兼容调用 (Chat Completions)

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{
    "model": "unified-model",
    "messages": [{"role": "user", "content": "你好，请用一句话介绍你自己！"}]
  }'
```

#### 调用固化参数的任务路由 (`TASK_XXXXXX`)

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -H "X-AMKR-Workspace: core-platform" \
  -d '{
    "model": "TASK_CODE_REVIEW",
    "messages": [{"role": "user", "content": "审查以下 Go 函数的并发安全性..."}]
  }'
```

> [!NOTE]
> 任务路由会强制使用服务端预设的 `temperature`、`top_p` 与 `reasoning_effort`，调用端传入的采样参数将被安全过滤，保证生成一致性。

#### Claude Code 原生协议调用 (`/v1/messages`)

```bash
curl http://127.0.0.1:8000/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: amkr_your-local-api-key" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-3-7-sonnet",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello via AMKR!"}]
  }'
```

---

## 🖼️ 图文全景展示

### 1. 模型路由与多 Key 故障转移

![模型路由管理](<docs/screenshots/routing.png>)

- **多目标绑定**：一个模型对外统一暴露，底层可绑定来自不同供应商（如 OpenAI 原厂、Azure、中转商）的多个 Key。
- **多种分流模式**：
  - `round_robin`：在健康节点间轮询分流负载。
  - `priority`：严格按照设定的优先级顺序请求，首选节点不可用时平滑切换备选。
  - `only_first`：固定仅使用首个可用 Key。
- **多模态名称映射**：上游不同服务商对同一模型的命名存在差异（如 `claude-3-7-sonnet-20250219` vs `claude-3-7-sonnet`），AMKR 允许对每个目标单独设定 `upstream_model`。

---

### 2. 供应商管理与独立 Key 能力探测

![供应商与 Key 能力探测](<docs/screenshots/providers.png>)

- **按 Key 细粒度探测**：即便属于同一供应商，不同账户 Key 的可用模型权限往往不同。AMKR 在添加或刷新时对每个 Key 发起独立探测，缓存各路由（OpenAI Chat、Anthropic Messages、Responses 等）的真实连通状态。
- **协议路由自定义**：针对非标准中转接口，可自由自定义入站路由映射（如 `v1/chat/completions`、`anthropic/` 等）。

---

### 3. 多租户工作空间与全链路流向图

![工作空间与桑基流向图](<docs/screenshots/workspaces.png>)

- **清晰的流量流向桑基图**：直观展示从 `工作空间` → `请求模型` → `实际模型` → `供应商` → `上游 Key` → `上游模型` 的完整链路分布，调用拓扑一目了然。
- **工作空间面板嵌入 (Panel Key)**：
  通过 `amkr_ws_...` 面板密钥，支持将独立工作空间的任务与用量控制台以 iframe 形式无缝嵌入至企业内部管控平台：
  ```html
  <iframe src="http://127.0.0.1:8000/ui/panel.html#k=amkr_ws_your-panel-key"
          width="100%" height="760" style="border:0; border-radius:12px;"></iframe>
  ```
  详情参考文档 [docs/PANEL.md](<docs/PANEL.md>) 与 [docs/WORKSPACE.md](<docs/WORKSPACE.md>)。
- **项目专属推理凭据 (Inference Key)**：
  交付 `amkr_ik_...` 给具体项目工程，无需暴露全局主 Key，支持配置只允许调用的模型白名单。

---

### 4. 客户端一键无缝接入

![客户端集成页面](<docs/screenshots/integrations.png>)

- **零配置接入 Claude Code**：一键将 AMKR 路由、鉴权与可用模型写入 `~/.claude/settings.json`，在终端输入 `claude` 即可无缝享受本地负载均衡与模型自由切换。
- **支持 Codex & Pi Agent**：自动识别本机环境，提供配置变更对比与一键安全回退机制。

---

### 5. 交互式多模型试验场 (Playground)

![模型试验场](<docs/screenshots/playground.png>)

- 内置全功能对话试验场，支持实时切换空间、模型别名与统一模型。
- 可调节系统提示词、温度、Top-P、流式开关与多模态输入，快速验证各 Key 的可用性与响应质量。

---

## ⚙️ 配置文件说明

AMKR 的核心配置文件为 JSON 格式（自动兼容迁移历史版本配置），完整参考见示例文件 [router-config.example.json](<router-config.example.json>)：

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
  "key_balance_cooldown_seconds": 300,
  "local_api_key": "amkr_sec_prod_gateway_key",
  "webui_enabled": true,
  "ops_enabled": true,
  "providers": {
    "openai": {
      "base_url": "https://api.openai.com",
      "max_concurrency": 10,
      "keys": {
        "primary": { "api_key": "sk-proj-...", "max_concurrency": 4 },
        "backup": { "api_key": "sk-proj-...", "max_concurrency": 4 }
      }
    },
    "anthropic": {
      "base_url": "https://api.anthropic.com",
      "keys": {
        "production": { "api_key": "sk-ant-..." }
      }
    }
  },
  "models": {
    "claude-3-7-sonnet": {
      "aliases": ["claude-sonnet-latest"],
      "routing_mode": "priority",
      "targets": [
        { "provider": "anthropic", "key": "production", "upstream_model": "claude-3-7-sonnet-20250219" }
      ]
    },
    "gpt-4o": {
      "routing_mode": "round_robin",
      "targets": [
        { "provider": "openai", "key": "primary", "upstream_model": "gpt-4o" },
        { "provider": "openai", "key": "backup", "upstream_model": "gpt-4o" }
      ]
    }
  },
  "unified_model": {
    "default": { "primary": { "model": "claude-3-7-sonnet", "key": null } },
    "embeddings": { "primary": { "model": "text-embedding-3-small", "key": null } }
  },
  "tasks": {
    "TASK_CODE_REVIEW": {
      "display_name": "代码审查与架构分析",
      "model": "claude-3-7-sonnet",
      "fallback_model": "gpt-4o",
      "params": { "temperature": 0.2, "top_p": 0.95, "reasoning_effort": "high" }
    }
  },
  "workspaces": {
    "core-platform": {
      "api_key": "amkr_ws_...",
      "inference_key": "amkr_ik_...",
      "models": ["claude-3-7-sonnet", "gpt-4o"]
    }
  }
}
```

<details>
<summary><b>🔍 查看配置核心字段说明表</b></summary>

| 字段名 | 类型 | 说明 |
| :--- | :--- | :--- |
| `host` / `port` | string / int | 服务监听的 IP 与端口（默认 `127.0.0.1:8000`） |
| `local_api_key` | string | 本地网关管理与主代理凭据（以 `amkr_` 开头） |
| `request_timeout` | int | 普通非流式请求超时时间（秒，默认 60） |
| `stream_first_byte_timeout` | int | 流式响应首包接收等待超时（秒，默认 60） |
| `stream_idle_timeout` | int | 流式数据块之间最大空闲间隔（秒，默认 60） |
| `key_cooldown_seconds` | int | 常规网络或可重试错误导致 Key 冷却的持续时间（秒，默认 60） |
| `key_balance_cooldown_seconds` | int | 欠费/余额耗尽时的初始熔断冷却时长（秒，默认 300，阶梯翻倍最高至 30 分钟） |
| `ops_enabled` | bool | 是否启用宿主机运维接口（日志读取、服务注册、客户端配置改写；容器环境建议设为 `false`） |
| `webui_enabled` | bool | 是否挂载内置 Web 管理控制台 |
| `providers.<id>.max_concurrency` | int / null | 该供应商**所有 Key、所有模型**上同时在途的上游请求上限；缺省、`null` 或 `0` 表示不限制 |
| `providers.<id>.keys.<key>.max_concurrency` | int / null | 该上游 Key 同时在途的请求上限，同一把 Key 被多个模型引用时共用一个计数；缺省、`null` 或 `0` 表示不限制。同时启用两级上限时，该供应商**启用中**的 Key 的上限之和不得大于供应商上限，否则配置校验失败 |

</details>

---

## 🛠️ CLI 常用命令参考

AMKR 提供强大的命令行工具集，可在无桌面终端或脚本环境中完成全部管理工作：

| 操作分类 | 常用命令 | 详细说明 |
| :--- | :--- | :--- |
| **服务运行** | `amkr` | 前台运行并唤起浏览器 WebUI（若已在运行则直接打开页面） |
| | `amkr --no-open` | 前台运行，不自动打开浏览器 |
| | `amkr --serve` | 以后台守护进程模式启动服务 |
| | `amkr --status` | 检查后台守护进程运行状态与 PID |
| | `amkr --stop` | 优雅停止后台运行的 AMKR 进程 |
| **凭据与地址** | `amkr --show-api-key` | 打印当前生效的本地鉴权 Key（别名：`--get-key`） |
| | `amkr --show-address` | 输出当前监听的实际基地址与端口 |
| | `amkr --show-config` | 输出当前配置摘要（供应商、模型与路由模式） |
| **系统服务管理** | `amkr --install-service` | 一键注册为开机自启系统服务（Windows 任务计划 / Linux systemd） |
| | `amkr --service start` | 启动系统服务 |
| | `amkr --service status` | 查看系统服务运行状态 |
| | `amkr --service restart` | 重启系统服务 |
| | `amkr --service uninstall`| 卸载并注销系统服务 |
| **统一模型操控** | `amkr --show-unified-model` | 查询当前 `unified-model` 指向的目标与状态 |
| | `amkr --switch-model <id>` | 命令行快速将 `unified-model` 切换至指定真实模型 |
| | `amkr --switch-key auto` | 将 `unified-model` 的绑定制切回自动轮询模式 |
| **版本与维护** | `amkr --check-update` | 检查 GitHub Releases 最新版本（内置镜像回退加速） |
| | `amkr --version` | 打印当前构建版本号与 commit 信息 |

> 完整参数选项与高级用法请参阅 [docs/CLI.md](<docs/CLI.md>)。

---

## 📡 HTTP & WebSocket API 概览

| 类别 | 端点路径 | 请求方法 | 说明 |
| :--- | :--- | :--- | :--- |
| **健康状态** | `/health` | `GET` | 检查服务可用性、版本与特性开关（无需鉴权） |
| **模型清单** | `/v1/models` | `GET` | 获取当前所有已配置且具备可用 Key 的对外模型清单 |
| **OpenAI 兼容** | `/v1/chat/completions` | `POST` | 对话补全，支持流式 SSE、工具调用与推理思维链 |
| | `/v1/embeddings` | `POST` | 向量嵌入生成 |
| | `/v1/images/*` | `POST` | 图像生成与编辑接口透传 |
| | `/v1/audio/*` | `POST` | 语音合成（TTS）、转写（STT）与翻译 |
| | `/v1/videos/*` | `POST` | 视频生成与状态轮询 |
| | `/v1/rerank` | `POST` | 文档相关性重排序 |
| **Claude Code** | `/v1/messages` | `POST` | Anthropic Messages 协议，支持流式与缓存 Token 统计 |
| **Codex 兼容** | `/v1/responses` | `POST` | OpenAI Responses 协议直通 |
| **指标与事件** | `/metrics` | `GET` | 实时速率（RPM/TPM）与窗口统计指标 |
| | `/metrics/series` | `GET` | 时间序列数据（支持时序图表聚合） |
| | `/metrics/requests` | `GET` | 最近请求调用明细（包含延迟、Token、来源与状态码） |
| | `/ws/events` | `GET` | WebSocket 实时运行事件流订阅 |
| **管理接口** | `/api/*` | 各种 | 供应商、Key、模型、任务路由、工作空间及设置的 RESTful CRUD |

> 详细的请求体契约与字段定义请参阅 [docs/API.md](<docs/API.md>)。

---

## 🔒 安全与生产部署建议

- **严格保护本地凭据**：`local_api_key` 是 AMKR 的最高管理凭据。切勿将包含真实凭据的配置文件提交至公共代码仓库。
- **容器与公共网络加固**：
  - 监听 `0.0.0.0` 时，必须确保 `local_api_key` 不为空，并配置防火墙策略。
  - 在 Docker 容器或反向代理后部署时，请附带 `--no-ops` 参数，关闭本机日志读取、服务控制及客户端配置改写能力。
- **外部租户最小权限原则**：
  - 严禁将主凭据分发给外部使用者或普通脚本，应当通过 WebUI 创建专门的 **访问密钥 (Access Keys)** 或 **项目推理 Key (Inference Keys)**，并限定允许使用的模型与供应商。
- **第三方后台嵌入安全**：
  - 嵌入工作空间控制台必须使用专用 **面板 Key (`amkr_ws_...`)**，该 Key 仅允许查看和修改对应空间的任务与统计，杜绝越权访问 `/v1/*` 推理或全局配置。
- **单价估算隐私保证**：
  - AMKR 定期访问 `https://models.dev/api.json` 仅用于拉取模型公开单价以计算 Token 预估开销，**绝不上传**任何本地配置、Key、请求内容或用量统计。离线部署时若无法连接，费用将显示为 `—`，所有核心路由与代理功能均不受影响。

---

## 💻 开发者指南

### 本地编译与运行测试

```bash
# 克隆仓库并构建
git clone https://github.com/Sparrived/auto-model-key-router.git
cd auto-model-key-router
go build ./cmd/amkr

# 运行完整单元测试套件
go test ./...

# 前端 ES 模块与探针校验（改动 webui/ 时推荐）
node scripts/webui_module_check.mjs
for p in webui/probes/*.mjs; do node "$p" || break; done
```

---

## 📚 延伸阅读与开发文档

- [完整使用教程 (USAGE.md)](<docs/USAGE.md>)：详细的按步指引、高级路由规则与场景实战
- [CLI 命令行参考 (CLI.md)](<docs/CLI.md>)：完整的命令行参数列表与示例
- [HTTP API 接口参考 (API.md)](<docs/API.md>)：RESTful 管理接口与数据模型定义
- [多租户工作空间设计说明 (WORKSPACE.md)](<docs/WORKSPACE.md>)：多租户隔离与架构设计原委
- [嵌入式工作空间面板说明 (PANEL.md)](<docs/PANEL.md>)：iframe 集成规范与消息契约
- [订阅制账号额度调研笔记 (SUBSCRIPTION-QUOTA.md)](<docs/SUBSCRIPTION-QUOTA.md>)：CPA 与订阅型账号配额机制分析
- [更新历史记录 (CHANGELOG.md)](<CHANGELOG.md>)：各版本发布详情与变更日志

---

## 📄 开源许可证

本项目基于 [MIT 许可证](<LICENSE>) 开源。欢迎提交 Issue 与 Pull Request！
