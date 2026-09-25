# Auto Model Key Router 使用教程

本文是一份从零开始的完整使用教程，覆盖安装、配置模型与 Key、启动服务、发送请求、查看统计与成本估算、访问密钥、统一模型切换、任务路由，以及 Claude Code / Codex / Pi Agent 接入。

如果只想查 CLI 参数或 HTTP API 字段，请参考 [`CLI.md`](CLI.md) 和 [`API.md`](API.md)。

---

## 1. 适用场景

Auto Model Key Router（简称 AMKR）是一个本地 OpenAI-compatible API 路由服务。它适合：

- 给同一个模型配置多个上游 API key，并自动分流。
- 在某个 key 限流、鉴权失败或上游异常时自动切换到其他 key。
- 给 Claude Code、Codex、Pi Agent 或其他 OpenAI-compatible 客户端提供一个稳定的本地入口。
- 使用固定模型名 `unified-model`，在路由器里随时切换真实模型或指定 key，避免反复改客户端配置。
- 为不同任务固化模型与采样参数：客户端传 `TASK_XXXXXX`，由路由器决定用哪个模型、用什么参数。
- 统计本地调用、工作空间调用、访问密钥调用、重试、状态码、token 和耗时。

---

## 2. 安装

`amkr` 是单一静态二进制（Windows / macOS / Linux，amd64 + arm64），没有运行时依赖。
推荐从 GitHub Releases 下载预编译产物，安装脚本会校验 sha256 后再落盘；也可以本地
`go build ./cmd/amkr` 自行构建。完整步骤见 [README 的「安装」一节](../README.md#安装)。

一行安装（Linux / macOS）：

```bash
curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.ps1 -OutFile "$env:TEMP\amkr-install.ps1"; & "$env:TEMP\amkr-install.ps1"
```

装完确认：

```bash
amkr --version
auto-model-key-router --version
```

两个名字指向同一份程序。一行脚本默认装到 `/usr/local/bin`，没有写权限时退回
`~/.local/bin`；Windows 上装到 `%LOCALAPPDATA%\Programs\AutoModelKeyRouter\amkr.exe`，
不需要管理员权限。若提示找不到命令，把该目录加进当前用户 PATH 并**重新打开终端**——
PATH 变化不会自动刷新已经打开的终端、IDE 或系统服务进程。

给外部使用者分发凭据不需要任何额外安装步骤或可选依赖：在配置里加一段 `access_keys`，或在
WebUI 的「访问密钥」页新建一把即可（见 [第 15 节](#15-使用访问密钥)）。

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
- `providers`：供应商（含 `base_url`）及至少一个 `keys`（供应商级 API Key）。
- `models`：真实模型列表；每个模型至少有一个 `targets[]`，通过 `{provider, key, upstream_model}` 绑定一个供应商 Key。可选 `aliases`（会出现在 `/v1/models`）与 `hidden_aliases`（可调用但不列出，见 [3.x 同一个模型的多个名字](#同一个模型的多个名字隐藏别名)）。

示例：

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
      "keys": {
        "main": {"api_key": "sk-your-first-upstream-key"},
        "backup": {"api_key": "sk-your-second-upstream-key"}
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
        {"provider": "openai", "key": "backup", "upstream_model": "gpt-4o-mini"}
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

> 注意：`local_api_key` 是调用 AMKR 本地服务的 Key；`providers.*.keys.*.api_key` 是 AMKR 转发到上游时使用的真实供应商 Key；`targets[]` 的 `key` 引用同供应商下已声明的 Key，`upstream_model` 是发送给上游的模型名（不写则默认为本地模型 ID）。v1/v2/v3 的旧格式配置会在加载时自动迁移到 v4 并写回。

### 3.4 请求与流式超时

- `request_timeout` 控制连接建立、请求写入和非流式请求。
- `stream_first_byte_timeout` 默认 60 秒，从发起流式上游请求开始，覆盖等待响应头和第一块响应体的总时间。
- `stream_idle_timeout` 默认 60 秒，控制收到第一块后相邻响应块的最大等待时间。

三个值都应大于 0。流式响应头返回前超时时，下游响应尚未建立，AMKR 会按现有重试策略切换 Key；下游流建立后发生首块或空闲超时时，只结束当前流，不会自动重放请求，以免产生重复事件、重复计费或非幂等工具调用。可在 WebUI 的 **设置 → 超时配置** 中统一修改这三个值。

---

## 4. 用 WebUI 配置和管理

启动服务后会自动打开 WebUI：

```bash
amkr --config router-config.json
```

不加 `--config` 时会管理默认缓存目录中的配置。也可以手动访问
`http://127.0.0.1:8000/ui/`。终端交互界面已随 Python 版退役，WebUI 是唯一的界面。

WebUI 里最常用的页面：

| 页面 | 主要用途 |
| --- | --- |
| 概览 / 用量统计 / 工作空间 / 服务日志 / 成本 | 运行状态、请求明细、各空间用量与流向图、日志、基于 models.dev 的成本估算 |
| 账号资源 | 把多个 CLIProxyAPI 实例的账号与额度汇总到一块看板（[4.1](#41-多实例账号资源看板)） |
| 供应商 | 添加供应商与 Key、管理 Key、刷新能力探测（可按 Key 与端点范围）、设置 Base URL / 路由 |
| 模型路由 | 管理模型别名、隐藏别名、路由模式，把供应商 Key 绑定/解绑到模型、修改上游模型名 |
| 统一模型 | 设置 `unified-model` 当前指向的真实模型，并选择自动路由或固定 Key |
| 任务路由 | 维护任务路由，以及任务工作空间的切换、改名与删除（[第 9 节](#9-任务路由)） |
| 访问密钥 | 给外部使用者分发受限凭据：新建（明文只显示一次）、改名、启停、改供应商/模型清单、轮换与删除（[第 15 节](#15-使用访问密钥)） |
| 集成 | 让本机的 Claude Code / Codex / Pi Agent 通过 AMKR 统一入口发请求，可应用与回退 |
| 设置 | 监听地址、端口、本地鉴权、请求超时、服务控制与注册、配置迁移、WebUI 开关、版本更新 |

推荐的新手流程：

1. 进入 **供应商**，添加供应商 ID、Base URL 和第一个 API Key；AMKR 会自动探测这个新 Key 可服务的模型，多选后自动建立本地模型并完成绑定（同一供应商以后再添加 Key 也会只探测该新 Key，互不复用探测结果）。
2. 给同一模型继续添加 Key，或给模型绑定其他供应商的 Key：进入 **模型路由** 选择模型，调整 Key 绑定。
3. 进入 **统一模型**，把 `unified-model` 指向该模型。
4. 进入 **设置 → 服务控制**，启动或注册本地路由服务。
5. 用客户端请求 `http://127.0.0.1:8000/v1/...`，模型名可以写真实模型、别名、`unified-model` 或任务名 `TASK_XXXXXX`。

> **能力探测与缓存（按 Key 独立）**：探测缓存按 Key 保存（磁盘为 `providers.<id>.keys.<key>.capabilities`，含该 Key 的模型清单 `models`、各路由可用性 `route_status`、`errors` 与 `checked_at`）。同一供应商的不同 Key 能访问的模型集可能不同（如免费/付费额度、不同订阅），所以每次添加 Key 时只探测这个新 Key，结果不复用、不折叠。
>
> 手动刷新：在 **供应商** 页选择刷新能力探测，可刷新全部 Key 或指定 Key（指定后可再选端点范围：全部路由模式 / 仅 Chat (openai) / 仅 Messages (anthropic) / 仅 Responses）。刷新只更新探测缓存，不会改动模型与绑定；管理 API 对应入口见 [`docs/API.md`](API.md) 的 probe 接口。

### 4.1 多实例账号资源看板

AMKR 只知道自己上游 Key 的成功与失败，不知道那些账号**还剩多少额度**。如果你另外跑着
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）——把订阅制账号转成 API 的
那个项目——**账号资源**页可以把任意多个 CPA 实例的账号与额度汇总到一块看板上。

**怎么配**：进入 **账号资源 → 管理实例**，为每个 CPA 填实例 ID、名称、地址（如
`http://127.0.0.1:8317`）与**管理密钥**。管理密钥是 CPA 管理面（`/v0/management/*`）的密钥，
不是模型调用 key——两者混用会拿到 `401`。清单也可以直接写在配置文件的顶层 `cpa_instances`
键里，管理 API 见 [`docs/API.md`](API.md) 的「CPA 账号资源接口」。

**看什么**：每个实例一块卡片，逐账号列出供应商、状态（可用 / 冷却中 / 已停用）、成功与失败
次数，以及**每个额度窗口还剩多少**（进度条 + 重置倒计时）。顶部四张瓦片给实例数、账号数、
可用账号数与额度告警数（剩余不足 20%，其中已用尽的单独标出）。剩余 5% 以下的条会转成红色——
这是"该去加号了"与"已经不好使了"的分界。

**额度从哪来**：AMKR 不自己请求上游，只问 CPA：

| 来源 | 说明 |
| --- | --- |
| `CPA 采集`（被动） | CPA 从上游响应头抄下来的快照（Anthropic 的 `anthropic-ratelimit-unified-*`、Codex 的 `x-codex-*`），不额外发请求。**多数 CPA 安装只有这一条**：它不需要装任何插件，只要账号真的发过请求 |
| `现场查询` | CPA 装了额度提供者时，AMKR 为每个账号单独问一次 `quota/fetch`，拿到归一化结果。同一账号两者都有时以现场值为准 |

因此**不是每个 provider 都有额度可读**：CPA 的被动采集只覆盖 Claude 与 Codex（外加 Devin，
但 Devin 一侧没有任何头会被采下）。Antigravity / Gemini / Copilot / Kiro 这类要在上游侧主动
查询，CPA 没有内置通道，看板上会显示「对端未配置额度查询」（对端回的 `501`）或「上游未提供
额度信号」。哪些通道存在、官方程度如何，见
[`docs/SUBSCRIPTION-QUOTA.md`](SUBSCRIPTION-QUOTA.md)。

**几个约定**：

- **这一页不轮询**。一次刷新要替每个实例问一遍账号、有额度插件的还要逐账号问额度，所以数据
  只在进入页面与点「刷新」时取。需要新的读数就再点一次。
- **浏览器不直接访问 CPA**：所有读数都走 AMKR 服务端转一手（CPA 的管理接口不同源、没有 CORS
  头，而管理密钥是能改 CPA 配置的凭据，不该长期留在浏览器里）。
- **失败是逐实例的**：某个实例连不上只会让它那块卡片报错，其余实例照常显示。
- **只看不拦**：这些额度读数**不参与** AMKR 的 Key 选择、冷却与失败切换。AMKR 路由的是它自己
  的上游 Key，与 CPA 侧的账号额度没有耦合关系——额度耗尽该由 CPA 自己的冷却机制处理。

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

如果配置了任务路由，`model` 也可以直接写任务名（见 [第 9 节](#9-任务路由)）：

```json
{
  "model": "TASK_000001",
  "messages": [{"role": "user", "content": "hello"}]
}
```

AMKR 会在转发给上游前把请求体里的模型名改写为真实模型 ID，并把 `Authorization` 替换成选中的上游 `api_key`。

### 同一个模型的多个名字（隐藏别名）

同一模型在不同地方经常叫不同名字（例如本地叫 `deepseek-flash`，别处叫 `deepseek-v4.1-flash`）。除了会出现在 `/v1/models` 里的 `aliases`，还有一类「隐藏别名」：**可以直接调用，但不会出现在 `/v1/models` 与 `/health` 中**。两种来源：

1. **自动（通常不需要任何配置）**：每个 target 的 `upstream_model` 会自动成为隐藏别名。上面 `gpt-4o-mini` 的 target 上游名是 `gpt-4o-mini-2024-07-18`，于是客户端可以直接用这个名字调用，但 `/v1/models` 只列出 `gpt-4o-mini` 及其 `aliases`。绑定 Key 时填的上游模型名即是这个用途，不需要再手工登记一遍。
2. **手动**：模型级 `hidden_aliases` 列表，用于上游名之外还想额外接受的叫法。

```json
{
  "models": {
    "gpt-4o-mini": {
      "aliases": ["fast-mini"],
      "hidden_aliases": ["mini-latest"],
      "targets": [
        {
          "provider": "openai",
          "key": "main",
          "upstream_model": "gpt-4o-mini-2024-07-18"
        }
      ]
    }
  }
}
```

上例中 `fast-mini`（别名）会出现在 `/v1/models`；`mini-latest`（手写隐藏别名）与 `gpt-4o-mini-2024-07-18`（上游名，自动获得隐藏别名待遇）都只可调用、不列出。

规则：

- 隐藏别名只在**本地调用**（以及持有完整权限的调用方）时生效。访问密钥按调用方**写的原始名字**授权，因此它只能用自己 `models` 清单里列出的名字，看不到也用不了未列出的隐藏别名。
- 名字冲突时真实模型 ID 与 `aliases` 优先；多个模型指向同一上游名时按 `models` 顺序取第一个匹配。
- 手写的 `hidden_aliases` 参与重名校验：与任何模型 ID、`aliases` 或其他模型的手写隐藏别名冲突都会报错。
- 在 WebUI 的模型路由页可以编辑手写隐藏别名；自动推导的部分无需维护（同步调整绑定 Key 时的上游模型名即可）。

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
  "targets": [
    {"provider": "anthropic", "key": "main", "upstream_model": "claude-3-opus"}
  ]
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
- `unified_model` 下可选的 `image` 与 `embeddings` 计划把图像、嵌入请求指向各自的模型：`/v1/images/*` 用 `image`，`/v1/embeddings` 用 `embeddings`，其余请求用 `default`。未配置对应计划时该路径继承 `default.primary`（不继承 `default.fallback`）。用 `--unified-target` 指定要改的计划，例如把嵌入切到另一个模型：

```bash
auto-model-key-router --config router-config.json --switch-model text-embedding-3-small --unified-target embeddings.primary
```

---

## 9. 任务路由

`unified-model` 适合「客户端固定一个入口、模型在 AMKR 侧切换」；任务路由解决的是另一个问题：**不同任务该用不同模型和不同的采样参数**。把模型名与参数一起固化在服务端，客户端只要传任务名，就不必（也不能）关心这些细节。

在 WebUI 的 **任务路由** 页新建一个任务：

| 字段 | 说明 |
| --- | --- |
| 任务名 | 客户端要传的 `model`，如 `TASK_000001`；不能与模型 ID、别名、隐藏别名或 `unified-model` 撞名 |
| 显示名称 | 可选；给**人**看的中文名（如「长文摘要」），只用于在任务页上辨认任务，不影响调用 |
| 首选模型 | 任务实际调用的模型。**可以先留空**，任务会先作为占位存在（见下） |
| 备选模型 | 可选；首选模型重试失败后自动切换，响应带 `X-AMKR-Fallback: true`。没有首选时不能填 |
| 推理强度 | 可选，`none`…`max` |
| 固定采样参数 | 可选，`temperature`、`top_p`、`top_k`、`frequency_penalty`、`presence_penalty`、`seed`、`stop`、`max_tokens` |

调用时把 `model` 写成任务名：

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{
    "model": "TASK_000001",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

说明：

- 任务名是**虚拟模型名**，不会出现在 `/v1/models` 里。这与「隐藏别名」不同：隐藏别名仍是一个模型的名字，任务名则是一整组路由与参数的别名；如果需要客户端直接调用的名字能在 `/v1/models` 里看到，请改用模型的别名或隐藏别名。
- 任务可以先**不选模型**（新建时首选模型留空）：先把名字、显示名与固定参数定下来，模型稍后再补。这样的任务被请求时会得到 `404`「任务 TASK_000001 尚未指定模型…」，**不会**退到 `unified-model` 或任何一个模型上——静默回落会让一个忘记选模型的任务照常服务，而调用方无从知道自己实际用的是别的模型。
- 任务 `params` 里固定了的采样参数会覆盖请求体中的同名参数；请求里**显式传了**这些参数会直接返回 `400`，而不是被静默忽略。不在列表里的参数照常透传。
- `reasoning_effort` 不再例外：任务固定了它就同样拒绝调用方传入。任务路由面向的是**其他 AI 服务**，不是 Agent 客户端 —— 让调用方拿到一个「我传的值没生效」的静默结果，比明确报错更难排查。固定 `max_tokens` 时，Responses 方言的 `max_output_tokens` 也按同一个参数处理（同样 `400`）。
- 任务不接受调用方指定 Key（`TASK_000001[main]` 返回 `400`）。Key 仍由目标模型自身的路由模式（`round_robin` / `priority` / `only_first`）决定，与第 7 节一致。
- 参数名写错（如 `temprature`）会在**保存时**就报错，不必等到请求时才发现。
- 走原生 Anthropic（`/v1/messages`）时，固定的 `stop` 会以 `stop_sequences` 发给上游 —— 那是 Anthropic 的叫法，发 `stop` 会被静默忽略。`reasoning_effort` 在原生路径上不发（与模型级设置一致）。
- 访问密钥不能访问任务名：任务自己固定的模型可能不在这把密钥的 `models` 清单里，绕过去等于清单失效。
- 删掉某个模型时，引用它的任务会被自动清理（首选模型没了删整个任务，只有备选没了则退化为单模型任务）。

### 9.1 工作空间：把任务集合分开

任务名只在**工作空间**内唯一。一个人或一个团队维护自己的任务时，不必再为了避让别人的名字而给任务名加前缀 —— 不同工作空间可以有同名任务，各指向自己的模型与参数。

> 本节讲怎么用。设计取舍、边界与兼容性契约见 [`WORKSPACE.md`](WORKSPACE.md)。

在 WebUI 的任务路由页，页面顶部有一个空间下拉：切换即换一组任务，选择会记住（刷新、切页回来都还在）。下拉里的「＋ 新建工作空间…」会弹出一个输入名字的对话框，建好后页面切过去，并把**面板 key** 交给你（见 [9.3](#93-把工作空间面板嵌进你自己的后台)）。

工作空间有两条产生的路：

- **建第一个任务时隐式产生**：不建 key 的空间就是这样来的，配置里不会留下空分组。
- **显式创建**（`POST /api/workspaces`，也就是上面那个对话框）：建出一个**带面板 key** 的空空间。带 key 的空间即使一个任务都没有也会保留 —— 应用侧「先建空间拿 key、之后才陆续填任务」正是这个时序，若因没有任务被清掉，那把 key 会在下一次配置写回时无声失效。

反过来说，删掉某个空间的最后一个任务：**不带 key** 的空间就消失了（空分组不留）；**带 key** 的空间留下。

下拉旁边的「改名」与「删除」作用于**空间自身**（不是当前任务）：

- **改名**把整组内容（任务与 `api_key`）一起搬到新名字下，任务内容一个字都不用动。目标名字已经有任务时报 `409`，不会合并两个空间。
- **删除**连同组内任务一起删除，**带 `api_key` 的空间连 key 一起失效**（嵌了面板的应用会立刻开始 401）。删完这个名字就不存在了，不会留下空壳；操作前会先弹确认。
- 两个按钮在**默认工作空间**上不可用：默认空间是不带 `X-AMKR-Workspace` 头的调用方命中的那个，改名会让全部缺省调用落空；它也没有放 key 的槽位。当前空间还没写进配置时（只是切过去了、还没建任务）也同样不可用，因为那时没有可改可删的分组。

对应的接口是 `GET|POST /api/workspaces`、`PUT /api/workspaces/{workspace}`、`DELETE /api/workspaces/{workspace}`（见 [`API.md`](API.md#get-apiworkspaces)）。

调用方用 `X-AMKR-Workspace` 头选择工作空间：

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -H "X-AMKR-Workspace: teamA" \
  -d '{
    "model": "TASK_000001",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

- **不带这个头就是默认工作空间**，也就是配置文件的顶层 `tasks` 键。既有调用方因此无需任何改动。
- 头里的首尾空白会被裁掉；空串等价于默认工作空间。
- 该头**不会**转发给上游，不用担心它泄漏给供应商。
- 写了一个没建过的工作空间名**不是错误**：那里没有任务，于是按普通模型名继续解析，最终和「模型未配置」是同一个 `404`。
- 工作空间**只隔离任务**。模型 ID、别名、隐藏别名与 `unified-model` 仍然全局唯一，任务名也不能与它们撞名 —— 否则路由语义会取决于查表顺序，这是全局唯一的判断。
- 访问密钥不能使用任务，带上这个头也一样（访问密钥不绑定工作空间，该头只用来选任务所在的空间，而它一律进不了任务路由）。

配置文件里，命名工作空间写在 `workspaces` 键下，每个空间一个 `tasks` 段（形状与顶层 `tasks` 相同），可以再带凭据与模型授权：

```json
{
  "config_version": 4,
  "tasks": {
    "TASK_000001": {"model": "gpt-4o", "params": {"temperature": 0.2}}
  },
  "workspaces": {
    "teamA": {
      "tasks": {
        "TASK_000001": {"model": "claude-sonnet-4", "params": {"temperature": 0.7}}
      }
    },
    "teamB-insight": {
      "api_key": "amkr_ws_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
    },
    "worker": {
      "inference_key": "amkr_ik_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
      "models": ["gpt-4o"]
    }
  }
}
```

`workspaces` 是**可选**的新增字段，`config_version` 仍是 `4`：没有这个键的既有配置行为完全不变。工作空间随 `/api/config/export`、`/api/config/import` 一起迁移（但那条通道的导出会**剥掉** `api_key`）。

三个可选字段都让这个空间即使没有任务也保留：

- `api_key`：**面板 key**，用于[嵌入面板](#93-把工作空间面板嵌进你自己的后台)。
- `inference_key`：**推理 key**，用于调 `/v1`（见 [9.4](#94-用推理-key-把一个项目接进来)）。
- `models`：允许**直呼**的模型清单。省略 = 不限制，`[]` = 一个都不许直呼（只走任务名），
  有内容 = 只许这些。任务名不受它限制。

> `teamB-insight` 就是「带 key 的空空间」：它没有 `tasks` 也不消失。手写 `"teamB-insight": {}`（什么都不带）照旧会被清掉。`api_key` 与 `inference_key` 顶格写在这一个空间下，与供应商 Key 无关 —— 它们分别是嵌入面板与调用 `/v1` 的凭据。

#### 看各空间的用量与流向

WebUI 的「工作空间」页给出每个空间的用量读数，以及一张请求流向图（桑基图）：

```text
工作空间 → 请求模型 → 实际模型 → 供应商 → 上游模型
```

流带越粗表示该段承载的请求越多，因此「哪个团队的任务、打到了哪个模型的哪家供应商」一眼可读。宽度可以在**请求数**与 **Token** 之间切换：前者看调用次数，后者看实际消耗（长上下文的空间在 Token 视图下会明显更粗）。时间窗口与「用量统计」页共用同一套（含「全部历史」）。

同一份读数也可以通过接口取（`GET /ui/workspace-usage.json`，详见 [`API.md`](API.md#get-uiworkspace-usagejson)）。

有两点值得留意：

- **统计不是配额。** 归属只用于观测，不会用来拒绝任何请求。带不带头、带什么空间名，仍然只由任务与模型解析决定。
- **升级前的历史算「未归属」。** 工作空间归属从本版本才开始记录，因此旧数据的请求会显示为「未归属」，既不计入任何空间，也不出现在流向图里。页面会在图的上方说明这一点。这是刻意的：把这些历史摊到默认空间头上会凭空造出一段并不存在的用量。

### 9.2 迁移工作空间到另一台机器

工作空间有一套**独立的**导出/导入，与整台实例的配置迁移分开：

```bash
# 导出全部命名工作空间（带面板 key）
curl -X POST http://127.0.0.1:8000/api/workspaces/export \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{}'

# 只导出其中两个
curl -X POST http://127.0.0.1:8000/api/workspaces/export \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{"workspaces": ["teamA", "teamB-insight"]}'
```

拿到 `bundle` 后，在目标实例上导入：

```bash
curl -X POST http://127.0.0.1:8000/api/workspaces/import \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{"config_revision": "<目标实例的版本号>", "bundle": { ... }}'
```

它和 `/api/config/export|import` 的差别正是它存在的理由：

| | `/api/config/*` | `/api/workspaces/*` |
| --- | --- | --- |
| 面板 `api_key` | **剥掉**（导出文件会被贴进工单与聊天记录） | **带上**（有意的凭据搬迁） |
| 供应商 / 模型 | 核心内容 | **不含**（搬的是命名空间，不是模型库） |
| 冲突时 | 按 `base_url` / key 去重、按模型 ID 合并 | 同空间整包覆盖，或加前缀改名 |

导入时**同名空间默认被整包覆盖**（含 `api_key`，旧 key 立即失效）。想保留自己已有的空间就加前缀：

```bash
curl -X POST http://127.0.0.1:8000/api/workspaces/import \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -d '{"config_revision": "…", "prefix": "imported-", "bundle": { ... }}'
```

于是导入的 `teamA` 变成 `imported-teamA`，一个都不覆盖。注意这时原空间还在、原 key 还在它手里，因此**克隆体必然拿到一把新 key**，响应里的 `rekeyed` 会给出它（`{"imported-teamA": "amkr_ws_…"}`）——嵌了面板的话记得把嵌入片段换成新的。

三点要有心理准备：

- **任务可能被清掉。** 因为包里不含模型库，任务引用的模型在目标实例上不存在时会被清掉，并在响应的 `removed_tasks` 里逐个列出（形如 `teamA/summarize`）。先把模型建好再导入，就能一个不丢。
- **`replaced` 里的空间，面板 key 已经换了。** 覆盖会带上包里那把 key，原来那把立即失效。响应把 `added` 与 `replaced` 分开报就是为了让你能去通知嵌入方更新嵌入片段。
- **`rekeyed` 里的空间是新 key。** 见上，克隆体的 key 与原空间不同。

如果包里某把 key 撞上了目标实例上**别处**的凭据（另一个空间、`local_api_key`，或任意一把访问密钥），导入会直接报错而不是悄悄换一个 —— 那把 key 多半已经嵌在别人的页面里，换掉之后旧 key 会指向别的空间，这种事不该被藏起来。错误信息会指出与哪个空间撞了，改掉重试即可。

导入前会自动备份当前配置（与配置导入一致）。

### 9.3 把工作空间面板嵌进你自己的后台

> 这一节是操作步骤。**接入方开发者**要看完整的集成指南（权限边界、凭据为什么走 fragment、
> 跨源限制、自定义 UI 要调哪些接口、排查表），见 [`PANEL.md`](PANEL.md)。

给工作空间一把**面板 key**，就能把一个只看得到这个空间的控制面板嵌进你自己的应用：

```html
<iframe src="http://127.0.0.1:8000/ui/panel.html#k=amkr_ws_你的面板key"
        width="100%" height="720" style="border:0"></iframe>
```

面板里能做的事：看本空间的用量与请求流向、列出/新建/修改/删除本空间的任务。别的一概不能 —— 看不了别的空间，改不了供应商与模型，也用不了 `/v1/*` 代理面。

**怎么拿到 key**

- 在 WebUI 的任务路由页点「＋ 新建工作空间…」，建好后会弹出 key，并提供「复制 key」与「复制嵌入片段」。**key 只显示这一次**，之后任何界面与接口都不再给出明文（工作空间目录刻意不返回它，配置导出也剥掉它），请当场存好。
- 应用侧可以自己在创建时指定（适合已有既定凭据的场景）：

  ```bash
  curl -X POST http://127.0.0.1:8000/api/workspaces \
    -H "Authorization: Bearer amkr_your-local-api-key" \
    -d '{"config_revision": "…", "name": "teamA", "api_key": "my-own-panel-key"}'
  ```

  不传 `api_key` 就由服务端生成（`amkr_ws_` + 43 位随机字符）。

**几条要注意的**

- **面板 key 会钉死在这个空间上。** 请求头 `X-AMKR-Workspace` 被**忽略** —— 就算有人改了嵌入片段里的头，也换不到别的空间。这正是这个模式要防的事：key 会出现在被嵌入页面的 URL 里，是最可能泄漏的位置。
- **凭据走 URL 的 `#` 片段，不是查询串。** 片段不会进 `Referer`，也不会进服务端访问日志；写成 `?k=…` 就会两头都留下明文。
- **面板绝不碰浏览器存储。** 它和后台 WebUI **同源**，而 WebUI 把本地管理 key 存在 `localStorage` 里 —— 面板一旦读写存储，一个嵌进第三方后台的页面就能拿到完整管理凭据。这是硬规则，探针会拦。
- 面板和 AMKR **不同源**时，用片段里的 `api` 指定基地址：`#k=amkr_ws_…&api=https://amkr.example.com`。
- 面板读的是 `GET /ui/workspace-panel.json`（只认面板 key），返回内容里的「未归属」恒为零 —— 没有归属的请求不属于任何空间，面板看不到也不该看到。
- 本项目没有设置 `X-Frame-Options` 或 CSP `frame-ancestors`，因此面板默认可被任意来源嵌入。这是有意的：嵌入是它的全部用途，安全性由 key 的范围承担，不由来源承担。

### 9.4 用推理 key 把一个项目接进来

把 AMKR 当作**多个项目共用的网关**时，给每个项目一把**推理 key**：它只能调 `/v1`，且**只属于配给它的那个工作空间**。

在 WebUI 的任务路由页点「＋ 新建工作空间…」，弹窗里会同时给出**面板 key 与推理 key**（两者都只显示这一次）；事后想换推理 key，点该空间的「模型授权」→「轮换推理 key」。用接口的话：

```bash
# 建空间：一次拿到两把 key
curl -X POST http://127.0.0.1:8000/api/workspaces \
  -H "Authorization: Bearer $AMKR_LOCAL_KEY" -H "Content-Type: application/json" \
  -d '{"config_revision": "…", "name": "my-project"}'

# 换一把推理 key（不动面板 key）
curl -X POST http://127.0.0.1:8000/api/workspaces/my-project/inference-key \
  -H "Authorization: Bearer $AMKR_LOCAL_KEY" -H "Content-Type: application/json" \
  -d '{"config_revision": "…"}'
```

项目侧把它当普通 OpenAI Key 用：

```bash
export OPENAI_BASE_URL=http://127.0.0.1:8000/v1
export OPENAI_API_KEY=amkr_ik_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

**几条要注意的**

- **推理 key 会钉死在这个空间上。** `X-AMKR-Workspace` 被**忽略**。这比面板 key 更要紧：推理 key 会被写进各个项目的**环境变量**，泄漏面比 URL 片段更宽；如果请求头能换空间，一把泄漏的 key 就等于所有空间的推理权限。
- **它调不了管理面**（`/api/*` 一律 `401`），包括本空间的任务增删改 —— 那些是运维的事。它也不能用 `unified-model`（全局计划，不属于任何空间）。
- **默认只能直呼「允许」的模型。** 空间的 `models` 没配就是不限制；配了 `[]` 就一个都不许直呼，只能用**任务名**。任务名不受清单限制 —— 任务自己固定的模型就是该空间被授权用的。想收窄直呼范围，在「模型授权」里勾选（清单按解析后的真实模型判定，写别名不会绕过）。
- **`/v1/models` 看到的就是它真正能用的那份清单**，可以直接用它做客户端侧的模型下拉：配了 `models` 就是它，没配就是该空间的任务名。
- **面板 key 不能调 `/v1`，推理 key 也不能读面板。** 两把凭据各管一面，用错会拿到 `401`。
- 配置导出（`/api/config/export`）会**剥掉**两把 key；工作空间整包迁移（[9.2](#92-迁移工作空间到另一台机器)）则**带上** `api_key`。系统提示：导出文件常被贴进工单与聊天记录，凭据不适合随它走。

---

## 10. 显式指定某个 Key

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
2. 在该模型绑定的 Key（`targets[]` 展开后的 Key）中找到 `name` 为 `openai-backup` 的那个，并转发给对应的供应商。
3. 转发给上游时仍使用真实模型 ID `gpt-4o-mini`（`upstream_model` 与本地模型 ID 不同时使用前者）。

同一模型下 Key 名称必须非空且唯一；Key 由供应商管理，多个模型可以共用同一个供应商 Key（解析后可能显示为 `供应商ID-Key名` 的限定名称）。

---

## 11. 本地鉴权

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

### 在 WebUI 中登录

启用 WebUI（`--webui`）后访问 `/ui/`：未授权时管理面**不会**就地弹出表单，而是整页跳转到登录页 `/ui/login.html`，在那里粘贴本地鉴权 Key。验证通过后回到你原本要打开的页面（深链不丢）。

Key 只保存在浏览器 `localStorage`（`amkr.apiKey`），**不会写进 URL**——URL 会进历史记录、Referer 与截图。会话中途失效（例如在设置页重置了本地鉴权 Key）会跳回登录页并提示"已失效"；服务连不上时则保留已填内容并提供"重试连接"。登录页会拒绝站外的 `?next=` 地址，避免被用作开放重定向。

如果 `local_api_key` 为空（未启用本地鉴权），登录页会直接放行，不会拦人。

如果 `local_api_key` 为空，则本地接口不启用鉴权。只有在完全可信的本机环境中才建议这样做；如果监听地址改成 `0.0.0.0`，请务必启用鉴权并配置防火墙。

---

## 12. 查看健康状态和模型列表

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

## 13. 查看统计和日志

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

查看最近一小时的分钟时间桶和最近 24 小时调用明细：

```bash
curl "http://127.0.0.1:8000/metrics/series?hours=1&bucket_seconds=60" \
  -H "Authorization: Bearer amkr_your-local-api-key"

curl "http://127.0.0.1:8000/metrics/requests?hours=24&limit=50" \
  -H "Authorization: Bearer amkr_your-local-api-key"
```

调用明细传入 `all_history=true` 可与 CLI 的“全部”时间范围保持一致。

统计会持久化写入 SQLite，默认文件为缓存目录下的 `metrics.sqlite3`。返回数据包含：

- 总请求数、成功、失败、重试。
- prompt / completion / total tokens。
- 缓存命中和缓存 token 统计。
- 总耗时、平均耗时、首 token 耗时。
- 状态码分布。
- 按真实模型、请求模型名、Key、本地调用、工作空间调用和访问密钥调用拆分的聚合。
- 按供应商、上游模型和 Key 拆分的调用明细与聚合。
- 补零的服务端时间桶，以及带稳定游标的逐次上游调用明细。

升级前（v3 及更早）写入的历史统计行可能带有模型池（pool）归因，v4 不再产生该字段；这类历史数据会与无归因数据一起按 `unattributed` 汇总，不会根据当前配置反推，也不会参与当前配置的模型池概念。调用明细和时间桶支持 `attributed=true|false` 筛选。

---

## 14. 成本估算（基于 models.dev）

AMKR 本身**只记 token 用量，不记账**。成本是 WebUI 读出来的派生数字：拿本机的 token 用量乘以 models.dev 的公开单价。金额单位是 **USD**，单价单位是 **USD / 100 万 token**。

### 在哪里看

| 位置 | 内容 |
| --- | --- |
| 「成本」页 | 估算成本、模型成本排行、成本构成（输入 / 缓存读 / 缓存写 / 输出）、供应商成本、最近请求成本，以及可核对的单价明细表 |
| 「概览」页 | 「估算成本」KPI、请求流里每条请求的估算成本（悬停可见匹配到的单价），以及「模型成本排行」卡 |
| 「用量统计」页 | 按模型 / 调用方 / 模型-Key 维度的历史用量明细表（含 Token 与缓存率），成本明细见「成本」页 |

### 价格目录怎么来的

服务进程启动时从 `https://models.dev/api.json`（约 4.7 MB）取回一次，编译成「模型 id → 单价」的索引后**只驻内存**，此后每 6 小时复验一次。复验带 `If-None-Match`，目录没变时上游回 `304`，不会重复下载。

WebUI 通过 `GET /ui/pricing.json` 读取这份快照（与 `/ui/` 同级、不需要鉴权）。想让界面拿到价格，`webui_enabled` 必须为真。

目录取回失败**不会清空**已有价格：界面继续显示上一次成功的价格，并提示"当前显示的是上次成功获取的价格"。服务刚启动、尚未取到时，成本一律显示 `—`。

### 匹配规则

按 `upstream_model`（真正发给上游的模型名）匹配，**大小写不敏感**，依次尝试：

1. 原名，例如 `gpt-4o-mini`；
2. 去掉供应商前缀，例如 `openai/gpt-4o` → `gpt-4o`；
3. 去掉日期后缀，例如 `gpt-4o-mini-2024-07-18` → `gpt-4o-mini`。

用 `upstream_model` 而不是 `model_id`：后者是你自取的本地路由名，价格表里不可能有。因此**没有 upstream 归因的历史请求无法计价**，显示 `—`。

同一模型 id 在 models.dev 上常被多家供应商列出，其中不少把它标成 `input`/`output` 全为 `0`。服务端会先排除这类挂名条目，再取最便宜的一条；只有所有供应商都报 `0` 时才承认免费。不做这一步，`claude-sonnet-4-6`、`qwen3-max`、`glm-4.6` 等会被算成 `$0`，成本页会显示一张看起来很真、实则全零的假账。

### 计费口径

按 token **类别**分别计价：

| 类别 | 取哪个字段 | 用哪个单价 |
| --- | --- | --- |
| 普通输入 | `prompt_tokens` 扣除下面两类缓存量 | `input` |
| 缓存读 | `cache_read_input_tokens`，为 `0` 时退回 `cached_tokens` | `cache_read` |
| 缓存写 | `cache_creation_input_tokens` | `cache_write` |
| 输出 | `completion_tokens` | `output` |

缓存价缺失时**回退到输入价**，绝不当成 `0`。

举例：某次请求 prompt 1,000,000 token（其中缓存读 400,000、缓存写 100,000）、输出 200,000 token，模型单价 `input=3 / output=15 / cache_read=0.3 / cache_write=3.75`：

```
(500000×3 + 400000×0.3 + 100000×3.75 + 200000×15) ÷ 1,000,000 = $4.995
```

### 必须知道的偏差

- **这是估算，不是账单。** 单价来自 models.dev 的公开目录，与上游实际计费可能有出入（区域价、合约价、促销）。
- **金额会变。** 成本不落库，是按当前目录现算的；目录更新后，历史请求的估算金额会跟着变。
- **未匹配的部分不计入，也不当成 0。** 界面上会标出「x/y 项有定价」与「计价覆盖率」。覆盖率明显不足时，合计金额低于真实开销是正常的——**不要把它当成完整账单**。
- **忽略阶梯定价。** 当前只取目录里的基础单价，不看 `cost.tiers`（如超长上下文加价，约 460 个模型带此字段）。
- **不做汇率换算。** 金额固定是 USD。

---

## 15. 使用访问密钥

给外部使用者（同事、试用方、第三方应用）分发凭据时，**不要把自己的 `local_api_key` 交出去** —— 它是完整权限，泄漏一把就等于交出整个实例。改用**访问密钥**：每把密钥自带两份清单，限定它能用哪些供应商、能调哪些模型，并且可以一人一把、随时停用与轮换。

访问密钥取代了原先固定的 `amkr-visitor`（已整体移除，那个字符串现在和其它错误凭据一样被 `401` 拒绝）。它不需要额外安装，也没有可选依赖。

### 15.1 在配置里定义

`access_keys` 是配置的顶层字段，形状是 `{"<key_id>": {...}}` —— 用对象而不是数组，因为 `key_id` 是更新与轮换时的稳定定位符：

```json
{
  "access_keys": {
    "trial-a": {
      "name": "试用账号 A",
      "key": "amkr_ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
      "enabled": true,
      "providers": ["openai"],
      "models": ["gpt-4o-mini", "fast-mini"]
    }
  }
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 否 | 给人看的标识（WebUI 列表与日志用），不参与鉴权；省略时回落到 `key_id` |
| `key` | **是** | 密钥明文，鉴权时按恒定时间比较 |
| `enabled` | 否 | 默认 `true`；为假时立即失效，但配置保留（便于临时停用而不丢清单） |
| `providers` | 否 | 允许使用的**供应商 ID** 清单 |
| `models` | 否 | 允许使用的**模型名**清单（真实模型 ID **或**别名） |

两份清单都是**三态**：

| 取值 | 含义 |
| --- | --- |
| 省略字段 | **不限制**（能用所有供应商 / 所有模型） |
| `[]` | 一个都不许 |
| `["openai"]` / `["gpt-4o-mini"]` | 只许这些 |

「省略」与「`[]`」是**两种不同的授权状态**，必须能区分：把空数组当成「不限制」，运维写下的禁令就会静默失效；把它显示成「未限制」，看板上就会以为这把 key 什么都能用。

`models` 按调用方**写的原始名字**比对，比对发生在**别名解析之前** —— 因此清单里可以写别名（上面例子里 `fast-mini` 就是别名），而只要清单限制了模型，同一个模型换成别的写法一样会被拒。

写错一个名字会让某把已经分发出去的 key 静默少一项权限，而调用方只看到 `403`，所以清单里的每个供应商与模型名都会在**写盘时**逐个校验存在性，写错直接报错。

### 15.2 用 WebUI 或管理 API 创建（推荐）

手写明文 key 不是必需的：WebUI 的**访问密钥**页（配置分组下）可以新建、改名、启停、改清单、轮换与删除。新建时服务端会生成 `amkr_ak_` + 43 位 base64url（共 50 字符）的密钥，**明文只显示这一次** —— 列表页只回指纹，之后任何界面与接口都不再给出。请当场存好。

页面上两份清单是**勾选**而不是手写：候选来自当前配置里已有的供应商 ID 与模型名，每个选择器带一个「不限制（允许全部）」开关（对应上表的「省略字段」），关掉它再勾选就是限定为这些，**一个都不勾 = `[]`（一个都不许）** —— 三态在界面上互不混淆。模型候选里**真实 ID 与别名是各自独立的一项**：清单按调用方写的原始名字逐字比对（见上），调用方会写别名时要把别名也勾上，只勾真实 ID 并不放行别名写法。清单里若还留着当前配置已找不到的名字（模型被改名或删掉了），会以高亮卡片显示并标出，不会悄悄丢掉。

集成到自己的系统里时走管理 API（[`API.md`](API.md#访问密钥接口)）：

```bash
curl -X POST http://127.0.0.1:8000/api/access-keys \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{"name": "试用账号 A", "providers": ["openai"], "models": ["gpt-4o-mini"]}'
```

响应里带明文 `key`（仅此一次）。轮换用 `POST /api/access-keys/{key_id}/rotate`，旧 key **立即失效**。

### 15.3 调用示例

访问密钥**不绑定工作空间**：它和本地主凭据一样跟着 `X-AMKR-Workspace` 头走。

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer amkr_ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

### 15.4 限制

- 请求的模型名必须在这把 key 的 `models` 清单里（省略即不限制），且该模型至少要有一把属于 `providers` 清单的上游 Key。两处任一不满足都是 `403` 与「访问密钥 \<名字\> 无权访问模型: \<名字\>」。
- **不能用 `unified-model`**：那是运维为整台实例挑的全局计划，不属于任何清单能收窄的范畴。
- **不能用任务名**：任务自己固定的模型可能不在这把 key 的 `models` 清单里，绕过去等于清单失效。
- 不能访问 `/metrics`、`/metrics/*` 与 `/api/*` 管理接口 —— 它是推理凭据，不是管理凭据。
- `/v1/models` 返回的是**按这把 key 两份清单收窄后**的清单（先按供应商排掉一个上游都用不了的模型，再按模型名排掉未授权的），不会列出 `unified-model`。任务名也不在其中。
- 被**停用**（`enabled: false`）时返回 `403` 与「访问密钥已被停用: \<名字\>」，而不是 `401`：停用是可恢复的已知身份，认错凭据不是，两者的排查方向完全不同。
- 明文只在**新建**与**轮换**的响应里出现；已有的 key 一律只回指纹。

### 15.5 与工作空间推理 key 的区别

两把都是受限的 `/v1` 凭据，取舍不同：

| | 访问密钥（`amkr_ak_`） | 工作空间推理 key（`amkr_ik_`） |
| --- | --- | --- |
| 绑定工作空间 | 否，跟随 `X-AMKR-Workspace` 头 | 是，空间由 key 钉死，请求头被忽略 |
| 能否用任务名 | 不能 | 能（任务名是该空间天然隔离的调用方式） |
| 收窄维度 | 供应商 + 模型名 | 该空间的 `models` 清单（或退化为任务名） |
| 适合 | 发给外部使用者、按人收窄与轮换 | 配进各项目的环境变量，做多项目共用网关 |

详见 [9.4 用推理 key 把一个项目接进来](#94-用推理-key-把一个项目接进来)。

### 15.6 让持有者看自己的用量（访客看板）

访问密钥的持有者可以打开 **`/ui/guest.html`**，用自己的 key 登录，看到**这把 key 自己**的用量：

- 四张汇总瓦片：请求数、成功率、Token、平均耗时；
- 按**模型**与按**供应商**的请求排行；
- 按**上游模型**的**花费估算**（对 models.dev 公开单价，明确标注"几项匹配到单价"——匹配不上的条目不计入，金额不是账单）；
- **最近调用明细**：时间、模型、供应商、状态码、Token、耗时。失败标红，"没拿到响应"与 4xx/5xx 分开显示，重试过的请求额外打标。

拿到 key 的步骤：管理员在 **访问密钥** 页创建后，把明文（只在新建/轮换响应里出现一次）发给使用者，并告诉他看板地址 `/ui/guest.html`。

几条边界：

- **只读**。看板一个写接口都不调；清单的调整仍然要在管理面的「访问密钥」页做。
- **看不到明文 key**。页头只显示配置里的**名字**与 key 的尾号 6 位，够确认"看的是哪一把"，不足以被拿去调用——这一页常被投屏或截图。
- 凭据存在浏览器的 `localStorage`，键名 `amkr.guestAccessKey`。与 WebUI 管理面的 `amkr.apiKey` **分开**：同一个浏览器里既登录管理面又看看板时，两边不会互相覆盖。
- key **无效**（被删/被轮换）时看板说"无效"并请人重新填写；key 被**停用**时说"已被停用"并指向管理员。两者的处理方式不同，因此文案也不同。

---

## 16. 接入 Claude Code

AMKR 支持 Anthropic Messages 风格入口 `/v1/messages`，可供 Claude Code 使用。

在 **集成 → Claude Code** 中可选择三种状态：

- **AMKR unified-model 模式**：先设置 `unified-model`，再由 AMKR 写入路由、本地鉴权和 `unified-model` 的 Claude 模型环境变量。
- **AMKR 原生模式**：只写入路由和本地鉴权；首次接管会保留已有的 Claude 模型配置。从 unified-model 模式切换时会移除 AMKR 写入的模型变量，由用户手动配置 Claude Code 默认模型。
- **未接管**：选择回退原配置，AMKR 会恢复首次应用前的完整文件内容。

两种 AMKR 接管模式都要求 `local_api_key` 不为空。原生模式不要求设置 `unified-model`，但 Claude Code 使用的每个模型名必须先在 AMKR 的**模型路由**页配置；否则请求会明确提示缺失的模型名。

AMKR 会更新：

```text
~/.claude/settings.json
# 或 CLAUDE_CONFIG_DIR/settings.json
```

unified-model 模式写入的核心环境变量包括：

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

原生模式保留 `ANTHROPIC_BASE_URL`、`ANTHROPIC_AUTH_TOKEN` 和 AMKR 流量控制变量，但不会注入模型名。应用前的原始配置会备份到 AMKR 缓存目录，可在 WebUI 的集成页回退。

---

## 17. 接入 Codex

AMKR 支持 OpenAI Responses 风格入口 `/v1/responses`，可供 Codex 使用。

在 **集成 → Codex** 中可选择 unified-model 模式、原生模式或回退原配置。两种接管模式都写入 AMKR OpenAI Provider 和 `auth.json` 的本地鉴权 key；只有 unified-model 模式需要预先设置 `unified-model`。

原生模式不会注入模型：首次接管会保留用户已有的 `model`、`review_model` 和 `model_reasoning_effort`；从 unified-model 模式切换时会移除这些 AMKR 写入字段。用户需要手动配置 Codex 的 `model`、`review_model` 等模型名，并先将每个名称添加到 AMKR 的**模型路由**页；模型不存在时路由器会返回包含该名称的配置提示。

AMKR 会更新：

```text
~/.codex/config.toml
~/.codex/auth.json
# 或 CODEX_HOME/config.toml 与 CODEX_HOME/auth.json
```

unified-model 模式写入的核心配置类似：

```toml
model_provider = "OpenAI"
model = "unified-model"
review_model = "unified-model"
model_reasoning_effort = "max"

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

集成应用只会更新上述模型调用字段，以及 `auth.json` 中的 `OPENAI_API_KEY`。原生模式始终保留 Provider 与本地鉴权配置。现有的其他 Codex 设置、注释、OpenAI Provider 自定义字段和其他鉴权字段都会保留；旧版本已经写入的非模型字段也不会被主动删除。

应用前的原始配置同样会备份，可在 WebUI 的集成页回退。

---

## 18. 接入 Pi Agent

在 **集成 → Pi Agent** 中，AMKR 只提供 `unified-model` 模式，不提供原生模型模式。应用前需要先设置 `unified-model` 和本地鉴权 key；回退会恢复首次应用前的完整配置文件。

AMKR 会更新：

```text
~/.pi/agent/models.json
# 或 PI_CODING_AGENT_DIR/models.json
```

它会保留其他自定义提供商，并写入 `amkr` 提供商，其中包含 `unified-model` 以及当前有启用 Key 的 AMKR 模型和别名：

```json
{
  "providers": {
    "amkr": {
      "baseUrl": "http://127.0.0.1:8000/v1",
      "api": "openai-completions",
      "apiKey": "amkr_your-local-api-key",
      "authHeader": true,
      "models": [
        { "id": "unified-model", "contextWindow": 262144 },
        { "id": "gpt-5.5", "contextWindow": 262144 },
        { "id": "my-gpt", "contextWindow": 262144 }
      ]
    }
  }
}
```

Pi 的 `/model` 会继续显示其内置及其他自定义提供商的模型；选择 `amkr/unified-model` 会经由 AMKR 的统一路由，选择其他 `amkr/<模型或别名>` 则直接请求对应的 AMKR 模型。重新在集成页应用即可同步新增或删除的模型。

---

## 19. 请求兼容说明

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

## 20. 常见问题

### 请求返回 401 / 403

检查两层 Key：

1. 请求 AMKR 时传的凭据是否有效：本地 Key 需等于 `local_api_key`；访问密钥需是配置里 `access_keys.*.key` 之一且 `enabled` 不为假。`403` 且文案是「访问密钥已被停用」时把它重新启用即可；`401` 说明凭据本身就认不出来。
2. `403` 且文案是「访问密钥 \<名字\> 无权访问模型」时，检查该模型的供应商是否在这把 key 的 `providers` 清单里、请求写的模型名是否在 `models` 清单里（见 [第 15 节](#15-使用访问密钥)）。
3. 配置里模型绑定的供应商 Key（`providers.*.keys.*.api_key`）是否有效。

### 请求返回 404 模型不存在

检查请求体里的 `model` 是否是：

- 真实模型 ID；或
- 该模型的 `aliases[]`；或
- 已配置的 `unified-model`；或
- 已配置的任务名（`TASK_XXXXXX`，见 [第 9 节](#9-任务路由)）；或
- 访问密钥的 `models` 清单里列出的真实模型 ID 或别名（清单外的名字一律 `403`，见 [第 15 节](#15-使用访问密钥)）。

### 请求任务名返回 400

任务路由会拒绝两类请求（见 [第 9 节](#9-任务路由)）：

- **显式传了任务已固定的采样参数**：错误信息会列出冲突的参数名。任务固定了什么就不能再传什么，去掉这些字段即可。
- **写了 `TASK_XXXXXX[key]`**：任务不接受调用方指定 Key，Key 由目标模型自身的路由模式决定。

`reasoning_effort` 也在这条规则之内（不再是例外）：任务固定了它就会报 `400`。固定 `max_tokens` 时，Anthropic 方言的 `stop_sequences` 与 Responses 方言的 `max_output_tokens` 都算同一个参数。

### 请求任务名返回 404

两种原因，错误文本会区分开：

- **「任务 TASK_XXXXXX 尚未指定模型」**：这个任务建的时候没选首选模型（可以先建占位）。到**任务路由**页给它选一个模型即可，选完立刻生效，不必重启。
- **「任务 TASK_XXXXXX 指向的模型 xxx 未配置」**：任务指向的模型当前没有**启用**的 Key（一条都没绑定，或绑定的都被禁用了）。先在**模型路由**页给该模型绑定或启用 Key，或在**任务路由**页把任务改指向一个可用模型。删掉模型时引用它的任务会被自动清理，所以这个错误通常出现在「模型被解绑或禁用了所有 Key」的情况下。

### 请求返回 503 没有可用 Key

可能原因：

- 该模型没有启用的 Key。
- Key 都在冷却中。
- 用的是访问密钥，而该模型的全部上游 Key 都不在这把 key 的 `providers` 清单里（此时错误文案是「无权访问模型」，不是 `503`）。

### 修改配置后是否需要重启？

配置文件和管理 API 写入后，运行中的服务会热加载。系统服务、监听地址/端口等运行参数变化时，建议重启服务。

### 如何迁移配置到另一台机器？

使用 WebUI 的**设置 → 配置迁移**：

1. 源机器点「导出」，得到一份可迁移配置 JSON。
2. 目标机器把它粘进文本框，点「导入配置」。

迁移内容包含模型、上游 Key、任务与工作空间；本机的**入站凭据**（`local_api_key`、各空间的
`api_key` / `inference_key` 与全部访问密钥）**不随配置走** —— 导出文件常被贴进工单与聊天
记录，带上凭据等于把它们散出去。目标端已有的监听地址、本地鉴权、路径等本机设置会保留。

> 这条通道**不含面板 `api_key`**（导出文件常被贴进工单与聊天记录，不适合带凭据）。只想
> 搬工作空间、并且要把面板 key 一起带走时，用 [9.2](#92-迁移工作空间到另一台机器) 那条
> 独立通道。访问密钥没有任何带凭据的迁移通道：在目标实例上重新建一把即可。
