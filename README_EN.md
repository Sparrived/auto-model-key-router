# Auto Model Key Router (AMKR)

<p align="center">
  <img src="webui/favicon.svg" alt="AMKR Logo" width="84" height="84" />
</p>

<p align="center">
  <b>Lightweight, High-Performance Local Multi-Model & Multi-API-Key Intelligent Routing Gateway</b><br>
  <i>Single-binary OpenAI / Anthropic / Codex-compatible local router & intelligent proxy gateway.</i>
</p>

<p align="center">
  <a href="README.md"><b>简体中文</b></a> | <a href="README_EN.md"><b>English</b></a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go Version" />
  <img src="https://img.shields.io/badge/Release-v6.8.1-blue?style=flat-square" alt="Version" />
  <img src="https://img.shields.io/badge/Platform-Windows%20|%20macOS%20|%20Linux-lightgrey?style=flat-square" alt="Platform" />
  <img src="https://img.shields.io/badge/Docker-GHCR%20Ready-2496ED?style=flat-square&logo=docker" alt="Docker" />
  <img src="https://img.shields.io/badge/License-MIT-green?style=flat-square" alt="License" />
</p>

---

## 📸 Dashboard Overview

![AMKR Operational Dashboard](<docs/screenshots/overview.png>)

---

## 💡 Why AMKR?

When working with Large Language Models (LLMs) across various projects and developer tools, common obstacles arise:
- **Rate Limits & Key Rotation**: Balancing multiple API keys across accounts or providers manually is tedious; encountering `429 Too Many Requests` or quota exhaustion disrupts developer workflow.
- **Protocol Fragmentation**: Claude Code expects Anthropic's `/v1/messages` protocol, Codex utilizes `/v1/responses`, while standard apps require OpenAI's `/v1/chat/completions`.
- **Team Collaboration & Credential Security**: Sharing a master API key with coworkers or scripts poses severe security risks; restricting access per provider/model is essential.
- **Prompt & Sampling Governance**: Enforcing parameters (e.g., `temperature`, `top_p`, `reasoning_effort`) server-side ensures consistent outputs across automated pipelines without client-side tampering.

**Auto Model Key Router (AMKR)** solves these challenges as a high-performance local AI gateway built in pure Go. Shipped as a **single standalone binary**, it embeds a pure-Go SQLite metrics database and a responsive modern WebUI (via `//go:embed`), requiring **zero runtime dependencies** (no Python, Node.js, or external database).

---

## ✨ Key Highlights

- 🚀 **Single Binary & Zero Dependencies**: Written in Go with minimal memory footprint and sub-second startup. Embedded WebUI and SQLite storage require no runtime setup.
- 🔄 **Intelligent Failover & Key Balancing**:
  - Load balancing strategies: `round_robin`, `priority` (fallback chain), and `only_first`.
  - Automatic retry and key rotation on `401`, `403`, `429`, or `5xx` transient errors.
  - **Smart Balance Exhaustion Circuit Breaker**: Identifies `402 Payment Required`, `insufficient_quota`, `Insufficient Balance`, or `credit balance is too low`, putting the account into an exponential cooldown (starting at 300s up to 30m). Automatically performs half-open recovery when the window expires and fails fast with 503 when all keys are exhausted.
  - **Concurrency Limits (provider + key)**: Cap in-flight upstream requests with `providers.<id>.max_concurrency` (counted across every key and every model of that provider) and `providers.<id>.keys.<key>.max_concurrency` (per upstream key; all models referencing the same key share one counter). Omitted, `null`, or `0` means unlimited. Keys and providers at their limit are excluded hard (no cooldown-style soft fallback), and when every candidate for a model is blocked by concurrency the proxy answers `429` instead of sending a request that would only queue up or fail upstream.
- 🎯 **Unified Model (`unified-model`)**: Point client requests to a single model name. AMKR resolves requests based on endpoint family (chat, image, embedding, speech synthesis, transcription, video, rerank) to preconfigured upstream models.
- 📋 **Task-Based Routing (`TASK_XXXXXX`)**: Pass semantic task IDs as model names. AMKR locks primary models, fallback models, and sampling hyperparameters (`temperature`, `top_p`, `reasoning_effort`, etc.), preventing client overrides.
- 🏢 **Multi-Tenant Workspaces**:
  - Complete workspace isolation via the `X-AMKR-Workspace` header.
  - **Embedded Panel Key** (`amkr_ws_...`): Embed an isolated, tenant-scoped task and analytics panel into external portals via `<iframe>`.
  - **Project Inference Key** (`amkr_ik_...`): Scoped credentials for client code with optional model whitelists.
- 🔑 **Restricted Access Keys**: Issue scoped tokens (`amkr_ak_...`) with provider and model whitelists. Key holders can inspect their usage on a dedicated read-only dashboard (`/ui/guest.html`).
- 🌐 **Comprehensive Protocol Coverage**:
  - OpenAI-compatible: `/v1/chat/completions`, `/v1/models`, `/v1/embeddings`, images (`/v1/images/*`), audio (`/v1/audio/*`), video (`/v1/videos/*`), rerank (`/v1/rerank`).
  - Claude Code compatible: Native Anthropic `/v1/messages` endpoint forwarding.
  - OpenAI Codex compatible: Native `/v1/responses` forwarding.
  - Native WebSocket upgrade proxying.
- 📊 **Observability & Live Cost Estimation**:
  - Track real-time RPM, TPM, success rates, latency percentiles, and Time-To-First-Token (TTFT).
  - Sankey traffic flow visualization (Workspace → Requested Model → Real Model → Provider → Upstream Key → Upstream Model).
  - Status monitoring page: a health heatmap matrix by provider / upstream model / upstream key (success rate, TTFT and cache hit rate), with error-reason breakdown and per-model details.
  - Dynamic token cost estimation in USD powered by [models.dev](https://models.dev).
- 🔌 **One-Click Client Integrations**: Automatic configuration generation and rollback for Claude Code, Codex, and Pi Agent.

---

## 🏛️ System Architecture

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

## 📦 Installation

Pre-compiled binary releases for Windows, macOS, and Linux (amd64 and arm64) are available on [GitHub Releases](https://github.com/Sparrived/auto-model-key-router/releases).

### Method 1: One-Line Installer Script

#### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | sh
```

> Installs to `/usr/local/bin` (falls back to `~/.local/bin` without root privileges). You can specify a version or directory:
> ```bash
> curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | sh -s -- --version 6.8.1
> ```

#### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.ps1 -OutFile "$env:TEMP\amkr-install.ps1"; & "$env:TEMP\amkr-install.ps1"
```

> Installs to `%LOCALAPPDATA%\Programs\AutoModelKeyRouter\amkr.exe` (**no administrator privileges required**) and adds AMKR to your user `PATH`.

---

### Method 2: Docker Container

AMKR provides multi-arch container images published to GitHub Container Registry (GHCR):

```bash
docker run -d \
  --name amkr \
  -p 8000:8000 \
  -v amkr-data:/data \
  ghcr.io/sparrived/auto-model-key-router:latest \
  amkr --host 0.0.0.0 --serve-foreground --webui
```

> [!TIP]
> In container environments, pass `--no-ops` to disable host-level operations (host log viewing, system service registration, and host CLI config writes):
> ```bash
> docker run -d --name amkr -p 8000:8000 -v amkr-data:/data \
>   ghcr.io/sparrived/auto-model-key-router:latest \
>   amkr --host 0.0.0.0 --serve-foreground --webui --no-ops
> ```
> Retrieve the auto-generated master API key from the container:
> ```bash
> docker exec amkr amkr --config /data/auto-model-key-router/router-config.json --get-key
> ```

---

### Method 3: Build from Source

Requires **Go 1.24+**:

```bash
git clone https://github.com/Sparrived/auto-model-key-router.git
cd auto-model-key-router
go build ./cmd/amkr          # Produces amkr (or amkr.exe on Windows)
./amkr --version
```

---

## 🚀 Quick Start

### 1. Start the Gateway & Web Console

```bash
amkr                 # Runs in foreground and opens the WebUI in default browser
amkr --no-open       # Runs in foreground without opening a browser
amkr --serve         # Runs as a detached background daemon
```

On first launch, AMKR automatically generates a configuration file ([router-config.json](<router-config.example.json>)) and a local master API key. The default server listens on `http://127.0.0.1:8000`, with the WebUI located at `http://127.0.0.1:8000/ui/`.

Retrieve your master API key:
```bash
amkr --show-api-key
```

---

### 2. Configure via the WebUI

Navigate to the WebUI to manage routing topologies:

| Navigation Item | Functionality |
| :--- | :--- |
| **Providers** | Add upstream providers (OpenAI, Anthropic, DeepSeek, etc.), register keys with concurrency limits, and run automatic route & model discovery |
| **Model Routing** | Configure public model IDs (e.g., `gpt-4o`, `claude-3-7-sonnet`), create new model names, bind multiple upstream keys, and choose routing modes; each name can also dispatch per endpoint family (text / image / embedding / speech / transcription / video / rerank). The list's first entry is always `unified-model` — **the unified model is configured right here**, mapping `unified-model` to an active model with per-family overrides |
| **Task Routing** | Define `TASK_XXXXXX` aliases with server-locked models and fixed sampling parameters (`temperature`, `top_p`, etc.) |
| **Workspaces** | Manage tenant workspaces, generate embeddable panel tokens, and issue project-scoped inference keys |
| **Access Keys** | Issue scoped credentials for team members with provider and model whitelisting |
| **Integrations** | One-click configuration injection for Claude Code, Codex, and Pi Agent |

---

### 3. Send Inference Requests

#### Standard OpenAI Chat Completions

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{
    "model": "unified-model",
    "messages": [{"role": "user", "content": "Hello via AMKR!"}]
  }'
```

#### Calling Parameter-Locked Task Routes (`TASK_XXXXXX`)

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -H "X-AMKR-Workspace: core-platform" \
  -d '{
    "model": "TASK_CODE_REVIEW",
    "messages": [{"role": "user", "content": "Review this Go function for concurrency safety..."}]
  }'
```

> [!NOTE]
> AMKR guarantees that server-configured hyperparameters (temperature, top_p, reasoning effort) take precedence, filtering out client overrides to maintain prompt fidelity.

#### Native Claude Code Requests (`/v1/messages`)

```bash
curl http://127.0.0.1:8000/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: amkr_your-local-api-key" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-3-7-sonnet",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello from Claude Code protocol!"}]
  }'
```

---

## 🖼️ Feature Showcase

### 1. Model Routing & Multi-Key Failover

![Model Routing Management](<docs/screenshots/routing.png>)

- **Multiple Upstream Targets**: Expose a single model name backed by multiple keys across different providers (OpenAI direct, Azure, relay providers).
- **Flexible Load Balancing**:
  - `round_robin`: Evenly distribute requests across healthy keys.
  - `priority`: Route to preferred keys first, seamlessly switching to fallbacks upon failure.
  - `only_first`: Strictly route to the first active key.
- **Upstream Model Remapping**: Map public aliases to provider-specific model IDs (e.g., `claude-3-7-sonnet-20250219`).

---

### 2. Provider Management & Per-Key Capability Probing

![Providers and Key Capability Probing](<docs/screenshots/providers.png>)

- **Independent Key Probing**: Different keys under the same provider often have distinct model allowances and quotas. AMKR tests each key independently and caches real-time route statuses (OpenAI Chat, Anthropic Messages, Responses, etc.).
- **Route Customization**: Define custom path templates for third-party or enterprise reverse proxies.

---

### 3. Multi-Tenant Workspaces & Sankey Traffic Flow

![Workspaces and Sankey Flow Diagram](<docs/screenshots/workspaces.png>)

- **Interactive Sankey Diagram**: Visualize live traffic paths: `Workspace` → `Requested Model` → `Actual Model` → `Provider` → `Upstream Key` → `Upstream Model`.
- **Embeddable Workspace Panel (Panel Key)**:
  Embed an isolated management view into your internal tools via iframe using `amkr_ws_...`:
  ```html
  <iframe src="http://127.0.0.1:8000/ui/panel.html#k=amkr_ws_your-panel-key"
          width="100%" height="760" style="border:0; border-radius:12px;"></iframe>
  ```
  See documentation in [docs/PANEL.md](<docs/PANEL.md>) and [docs/WORKSPACE.md](<docs/WORKSPACE.md>).
- **Project-Specific Inference Keys (Inference Key)**:
  Distribute `amkr_ik_...` keys to applications without exposing the master key, enforcing strict model whitelists.

---

### 4. Seamless One-Click Client Integrations

![Client Integrations](<docs/screenshots/integrations.png>)

- **Claude Code**: Inject AMKR routing, authentication, and model lists directly into `~/.claude/settings.json`.
- **Codex & Pi Agent**: Automated configuration writing and safe, one-click rollbacks.

---

### 5. Interactive Multi-Model Playground

![Interactive Model Playground](<docs/screenshots/playground.png>)

- Test prompts, streaming responses, multimodal image inputs, and hyperparameter variations against any configured model or workspace.

---

## ⚙️ Configuration Reference

AMKR uses a JSON configuration file (with automatic backward-compatible schema migration). See the complete sample in [router-config.example.json](<router-config.example.json>):

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
      "display_name": "Code Review & Architecture Audit",
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
<summary><b>🔍 View Configuration Field Specifications</b></summary>

| Field | Type | Description |
| :--- | :--- | :--- |
| `host` / `port` | string / int | Listening host and port (default `127.0.0.1:8000`) |
| `local_api_key` | string | Local gateway master key (prefixed with `amkr_`) |
| `request_timeout` | int | Standard request timeout in seconds (default 60) |
| `stream_first_byte_timeout` | int | Maximum wait time for the first chunk in SSE streams (seconds, default 60) |
| `stream_idle_timeout` | int | Maximum idle gap between subsequent chunks (seconds, default 60) |
| `key_cooldown_seconds` | int | Cooldown duration for keys after transient failures (seconds, default 60) |
| `key_balance_cooldown_seconds` | int | Initial cooldown duration for balance exhaustion (seconds, default 300, doubles up to 30m) |
| `ops_enabled` | bool | Toggle for host management APIs (log viewing, system service control, client config writes; disable in Docker) |
| `webui_enabled` | bool | Mount status of the embedded WebUI |
| `providers.<id>.max_concurrency` | int / null | Maximum in-flight upstream requests for **every key and every model** of this provider; omitted, `null`, or `0` means unlimited |
| `providers.<id>.keys.<key>.max_concurrency` | int / null | Maximum in-flight requests for this upstream key, shared by all models referencing the same key; omitted, `null`, or `0` means unlimited. With both levels enabled, the limits of this provider's **enabled** keys must not sum to more than the provider limit, or config validation fails |

</details>

---

## 🛠️ Command-Line Interface (CLI)

| Category | Command | Description |
| :--- | :--- | :--- |
| **Server Operations** | `amkr` | Run in foreground and open the WebUI in your default browser |
| | `amkr --no-open` | Run in foreground without opening a browser |
| | `amkr --serve` | Start as a background daemon |
| | `amkr --status` | Check background daemon status and process ID |
| | `amkr --stop` | Gracefully shut down the running AMKR daemon |
| **Credentials & Info** | `amkr --show-api-key` | Print the current master local API key (alias: `--get-key`) |
| | `amkr --show-address` | Output the current server base URL and port |
| | `amkr --show-config` | Output configuration summary (providers, models, routes) |
| **System Service** | `amkr --install-service` | Install as an auto-start system service (Windows Scheduled Task / Linux systemd) |
| | `amkr --service start` | Start the installed system service |
| | `amkr --service status` | View system service status |
| | `amkr --service restart` | Restart the system service |
| | `amkr --service uninstall`| Remove and unregister the system service |
| **Unified Model** | `amkr --show-unified-model` | Inspect the active target of `unified-model` |
| | `amkr --switch-model <id>` | Switch `unified-model` to another real model |
| | `amkr --switch-key auto` | Reset `unified-model` key binding to automatic load balancing |
| **Updates & Version** | `amkr --check-update` | Check for updates on GitHub Releases (with built-in mirror fallback) |
| | `amkr --version` | Print binary version and commit information |

> See [docs/CLI.md](<docs/CLI.md>) for complete command-line specifications.

---

## 📡 HTTP & WebSocket API Reference

| Category | Endpoint | Method | Description |
| :--- | :--- | :--- | :--- |
| **Health** | `/health` | `GET` | Health status, version, and feature flags (unauthenticated) |
| **Models** | `/v1/models` | `GET` | List all available routed models with active keys |
| **OpenAI Proxy** | `/v1/chat/completions` | `POST` | Chat completions with streaming, tool calling, and reasoning tokens |
| | `/v1/embeddings` | `POST` | Text embeddings generation |
| | `/v1/images/*` | `POST` | Image generation and editing pass-through |
| | `/v1/audio/*` | `POST` | Speech synthesis (TTS), transcription (STT), and translation |
| | `/v1/videos/*` | `POST` | Video generation endpoints |
| | `/v1/rerank` | `POST` | Document reranking endpoint |
| **Claude Code** | `/v1/messages` | `POST` | Anthropic Messages protocol forwarding with cache token accounting |
| **Codex** | `/v1/responses` | `POST` | OpenAI Responses protocol pass-through |
| **Metrics & Events** | `/metrics` | `GET` | Real-time rates (RPM/TPM) and sliding window statistics |
| | `/metrics/series` | `GET` | Time-series data for metric visualization |
| | `/metrics/requests` | `GET` | Recent request stream with latency, tokens, status codes, and callers |
| | `/ws/events` | `GET` | WebSocket live operational event stream |
| **Management API** | `/api/*` | Various | RESTful management endpoints for providers, keys, models, tasks, and workspaces |

> Detailed endpoint schemas are documented in [docs/API.md](<docs/API.md>).

---

## 🔒 Security & Best Practices

- **Protect Master Keys**: `local_api_key` has unrestricted administrative and proxy access. Never commit real credentials to public repositories.
- **Hardening for Public Networks & Docker**:
  - When binding to `0.0.0.0`, ensure `local_api_key` is configured and firewall rules are active.
  - When running in Docker or behind reverse proxies, always pass `--no-ops` to disable host-level process control and configuration rewriting.
- **Least-Privilege Principle for Tenants**:
  - Issue **Access Keys** (`amkr_ak_...`) or **Inference Keys** (`amkr_ik_...`) rather than sharing the master key, scoping allowed providers and models.
- **Safe Panel Embedding**:
  - Always use the dedicated **Panel Key** (`amkr_ws_...`) for embedding into internal portals; panel keys restrict operations strictly to the designated workspace.
- **Cost Estimation Privacy**:
  - Queries to `https://models.dev/api.json` strictly fetch public pricing data. AMKR **never transmits** local configurations, keys, prompts, or metrics to external services. If offline, costs display as `—` while routing continues normally.

---

## 💻 Development & Contribution

```bash
# Clone repository and build
git clone https://github.com/Sparrived/auto-model-key-router.git
cd auto-model-key-router
go build ./cmd/amkr

# Run all unit tests
go test ./...

# Validate WebUI ES modules and probe test suite
node scripts/webui_module_check.mjs
for p in webui/probes/*.mjs; do node "$p" || break; done
```

---

## 📚 Documentation Index

- [Comprehensive User Guide (USAGE.md)](<docs/USAGE.md>)
- [CLI Reference Manual (CLI.md)](<docs/CLI.md>)
- [HTTP API Reference (API.md)](<docs/API.md>)
- [Workspace Design Architecture (WORKSPACE.md)](<docs/WORKSPACE.md>)
- [Embedded Workspace Panel Guide (PANEL.md)](<docs/PANEL.md>)
- [Subscription & CPA Quota Research (SUBSCRIPTION-QUOTA.md)](<docs/SUBSCRIPTION-QUOTA.md>)
- [Release Changelog (CHANGELOG.md)](<CHANGELOG.md>)

---

## 📄 License

This project is licensed under the [MIT License](<LICENSE>).
