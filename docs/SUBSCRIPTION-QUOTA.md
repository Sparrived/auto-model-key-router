# 订阅制账号的额度查询调研

面向**路由器维护者**的调研笔记：把 ChatGPT / Claude / Antigravity / Copilot 这类**订阅制账号**
转成 API 的项目是怎么知道「这个账号还剩多少额度」的，其中哪些做法更接近官方，以及哪些
可以直接搬到 AMKR 的按 Key 模型上。

结论先行：**面向个人订阅的官方公开配额 API 不存在**。能拿到真实额度数字的只有三条路——
官方客户端自己在用的接口（未写进公开文档）、上游响应头里的限额信号、以及本地/代理侧统计。
官方文档化的 Usage/Cost API 全部要求 Console 组织或 Admin key，订阅账号用不上。

## 1. 「CPA」指什么

中文社区说的 **CPA 就是 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)**
（`router-for-me/CLIProxyAPI`，Go，53k stars）。它用 OAuth 把 Antigravity、ChatGPT Codex、
Claude Code、Grok Build、Muse Code、Devin 的订阅账号包装成 OpenAI / Gemini / Claude / Codex
兼容接口。生态里几乎所有周边工具都以 CPA 命名（CPA Usage Keeper、CPA-Manager-Plus、
CPA-XX Panel、cpa-usage-keeper…），CLIProxyAPI 的 README 自己也把这些项目归在
「based on CLIProxyAPI」之下。

其它同类主项目（按订阅来源分，star 数为抓取时的读数）：

| 订阅来源 | 项目 | 作用 | 额度能力 |
| --- | --- | --- | --- |
| Copilot | [ericc-ch/copilot-api](https://github.com/ericc-ch/copilot-api) 4.1k★ | Copilot → OpenAI + Anthropic 兼容 | ✅ `GET /usage` 路由 + `check-usage` 命令 + 看板 |
| Antigravity | [lbjlaq/Antigravity-Manager](https://github.com/lbjlaq/Antigravity-Manager) 31.7k★ | 账号池管理与切换 | ✅ 平均剩余配额、按配额重置频率分级路由 |
| Antigravity | [liuw1535/antigravity2api-nodejs](https://github.com/liuw1535/antigravity2api-nodejs) 811★ | Antigravity → API | ✅ 逐 Token 的模型额度与重置时间（`QUOTA_FEATURE.md`） |
| Antigravity | [wusimpl/AntigravityQuotaWatcher](https://github.com/wusimpl/AntigravityQuotaWatcher) | 纯额度查看器（不转 API） | ✅ 本地语言服务器 `GetUserStatus` |
| Kiro | [Quorinex/Kiro-Go](https://github.com/Quorinex/Kiro-Go) 1.2k★ | Kiro 号池 → OpenAI/Anthropic | ✅ Usage tracking + 管理面板 |
| Kiro | [jwadow/kiro-gateway](https://github.com/jwadow/kiro-gateway) 2.3k★ | Kiro IDE/CLI → API | 未能证实 |
| Cursor | [7836246/cursor2api](https://github.com/7836246/cursor2api) 1.9k★ | Cursor → OpenAI/Anthropic | 未能证实 |
| 多来源 | [justlovemaki/AIClient2API](https://github.com/justlovemaki/AIClient2API) 8.8k★ | Antigravity/Codex/Grok/Kiro/Claude 号池 | 未能证实 |
| CPA 增强 | [kittors/CliRelay](https://github.com/kittors/CliRelay) 1.0k★ | CLIProxyAPI 增强 fork | ✅ 每 Key 配额 + 周期重置、账号配额窗口、自助用量查询页 |

**专门做额度展示的周边**（都读 CPA 的 Management API）：

| 项目 | 形态 | 额度能力 |
| --- | --- | --- |
| [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus) | Web | 按账号/模型/渠道统计请求与成本；Codex 账号池批量巡检、**配额检测**、异常账号发现 |
| [cpa-usage-keeper](https://github.com/Willxup/cpa-usage-keeper) | 服务 | 定期同步 CPA 用量、SQLite 落库、聚合 API、内置看板 |
| [cc-status-line](https://github.com/kinka/cc-status-line) | Claude Code 状态栏 | 逐账号 Codex / Grok / Antigravity / Claude 的 5h、7d 额度与重置倒计时 |
| [Quotio](https://github.com/nguyenphutrong/quotio) | macOS 菜单栏 | Claude / Gemini / OpenAI / Antigravity 订阅的实时额度与自动故障转移 |
| [ZeroLimit](https://github.com/0xtbug/zero-limit) | Windows 桌面 | Gemini / Claude / Codex / Antigravity 账号额度实时看板 |
| [CLIProxyAPI-Quota-Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector) | 跨平台 | 逐账号 Codex 5h/7d 窗口、按套餐排序 |

### CPA 自己怎么把额度暴露出去

CPA 从 v6.10.0 起把内置用量统计拆了出去，但**额度的读取通道留在 Management API**：

- `GET /v0/management/auth-files`：列出认证文件条目，**Claude 的额度就缓存在条目的
  `quota.signals` 里**——CPA 把上游响应头 `Anthropic-Ratelimit-Unified-*` 的 5h/7d
  `Utilization`（0~1）与 `Reset`（epoch 秒）存了下来，读它**不需要回源请求**。
- `POST /v0/management/api-call`：让 CPA 用**指定账号的 OAuth token** 代打上游额度接口。
  配套工具靠它拿 Codex 的 `chatgpt.com/backend-api/wham/usage`、Antigravity 的
  `cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary`、Grok 的
  `cli-chat-proxy.grok.com/v1/billing?format=credits`。
- `internal/api/handlers/management/quota.go` 里的 `ResetQuota`：按 `auth_index` 清掉该账号的
  额度/冷却路由状态——说明 CPA 内部确实在按账号跟踪额度，并把它接进了选路。

（证据：[cc-status-line README「额度来源」](https://github.com/kinka/cc-status-line)、
CLIProxyAPI `internal/api/handlers/management/quota.go`。）

## 2. 官方程度分四层

| 层 | 做法 | 是否官方 | 适用订阅 | 成本 |
| --- | --- | --- | --- | --- |
| L1 | 官方文档化的 Usage / Cost / Analytics API | **官方文档** | ❌ 仅 Console 组织、Enterprise、Admin key | 需 Admin 凭据 |
| L2 | 官方客户端自己在调的额度接口 | 官方客户端调用，未公开文档 | ✅ | 一次额外 HTTP 请求 |
| L3 | 上游响应头里的限额信号 | 官方协议的一部分，但未文档化 | ✅ | **零额外请求** |
| L4 | 本地文件 / OTel / 代理侧统计 | 官方（OTel、usage queue）或本地推导 | ✅ | 拿不到「剩余百分比」 |

L1 的证据（这些**不能**用于个人订阅，写在这里是为了避免走弯路）：

- Anthropic：`/v1/organizations/usage_report/messages`、`/v1/organizations/cost_report`、
  Claude Code Analytics API、Enterprise Analytics API（`read:analytics` scope）。
  见 [Usage and Cost API](https://platform.claude.com/docs/en/manage-claude/usage-cost-api)、
  [Claude Code Analytics API](https://platform.claude.com/docs/en/manage-claude/claude-code-analytics-api)。
- OpenAI：platform 的 organization usage / costs 接口（Admin key），与 ChatGPT 订阅无关。
- Claude Code 官方文档明确：`/usage` 在 Pro/Max/Team/Enterprise 上显示**计划额度条**，
  并会提示「usage endpoint 被限流」；程序化方案官方只给了 OTel。
  见 [Manage costs effectively](https://code.claude.com/docs/en/costs)、
  [Monitoring](https://code.claude.com/docs/en/monitoring-usage)。

## 3. 逐家明细

### 3.1 Anthropic（Claude Pro / Max）

**L2 主动查询**：`GET https://api.anthropic.com/api/oauth/usage`

| 项 | 值 |
| --- | --- |
| 请求头 | `Authorization: Bearer <OAuth access token>`、`anthropic-beta: oauth-2025-04-20`、`User-Agent`（第三方实现会伪装 Claude Code 版本） |
| 返回 | `five_hour` / `seven_day` / `seven_day_opus` / `seven_day_sonnet` / `seven_day_oauth_apps`，每个窗口 `{utilization, resets_at}`；`extra_usage {is_enabled, monthly_limit, used_credits, utilization, currency}`；新版还有 `limits[] {kind, group, percent, resets_at, scope.model.display_name, is_active}` |
| 同族端点 | `GET https://api.anthropic.com/api/oauth/profile`（账号身份） |
| 官方程度 | 社区逆向（Anthropic 未公开文档），但被多个独立项目一致实现 |
| 证据 | [claude-swap oauth.py](https://raw.githubusercontent.com/realiti4/claude-swap/main/src/claude_swap/oauth.py)、[CodexBar ClaudeOAuthUsageFetcher.swift](https://raw.githubusercontent.com/steipete/CodexBar/main/Sources/CodexBarCore/Providers/Claude/ClaudeOAuth/ClaudeOAuthUsageFetcher.swift) |

**L3 被动信号（推荐）**：上游响应里带 `anthropic-ratelimit-unified-*` 系列响应头（5h/7d 的
utilization 与 reset）。CLIProxyAPI 把这份快照缓存进 auth 文件条目的 `quota.signals`，
cc-status-line 直接读它、不再回源。这是**最省钱**的官方信号：不额外发请求。

**L4**：`~/.claude/projects/**/*.jsonl` 只能算 token 与成本（[ccusage](https://github.com/ryoppippi/ccusage)
即此路线），拿不到官方额度百分比。官方 OTel 可导出 `claude_code.token.usage`、
`claude_code.cost.usage`（含 `model`、`query_source`、`user.account_id` 等属性），是官方文档化的
组织级观测手段，但同样是「用了多少」而非「还剩多少」。

### 3.2 OpenAI Codex（ChatGPT Plus / Pro）

**L2 主动查询**：`GET https://chatgpt.com/backend-api/wham/usage`

| 项 | 值 |
| --- | --- |
| 请求头 | `Authorization: Bearer <access_token>`、`ChatGPT-Account-Id`、`User-Agent: codex-cli`（可选 `X-OpenAI-Fedramp`） |
| 返回 | `plan_type`；`rate_limit.{primary,secondary}.{used_percent, window_minutes, reset_at}`；`credits.{has_credits, unlimited, balance}`；`spend_control`；`additional_rate_limits[]`；`rate_limit_reset_credits` |
| 官方程度 | 官方客户端调用（[openai/codex](https://github.com/openai/codex) 开源，`backend-client/src/client/rate_limit_resets.rs` 里 `PathStyle::ChatGptApi => "{base}/wham/usage"`） |
| 注意 | `chatgpt.com/backend-api/codex/usage` **不存在**；`api.openai.com` 那套是 `PathStyle::CodexApi => "{base}/api/codex/usage"`，走 API key 而非订阅 |

**L3 被动信号**：`/backend-api/codex/responses` 的响应头，由 `codex-rs/codex-api/src/rate_limits.rs`
解析：

- `x-codex-primary-used-percent`、`x-codex-primary-window-minutes`、`x-codex-primary-reset-at`
- `x-codex-secondary-used-percent` / `-window-minutes` / `-reset-at`
- `x-codex-credits-has-credits` / `-unlimited` / `-balance`
- `x-codex-limit-name`、`x-codex-rate-limit-reached-type`、`x-codex-active-limit`
- 前缀随 limit id 变化（例如 `x-codex-bengalfox-*`），解析要按 `x-{limit_id}-*` 通配

**注意**：字段是 `window_minutes` / `reset_at`；社区常见的 `limit_window_seconds` /
`reset_after_seconds` 在官方源码里查无实据。也没有 `x-codex-plan-type`——`plan_type` 只在
JSON 端点或 SSE 事件里。

### 3.3 Google Antigravity / Gemini（Google AI Pro / Ultra）

Antigravity 有两条互不相通的额度通道：**本地语言服务器**（无需 OAuth，但要摸端口和 CSRF
token）与 **Google 后端**（要 OAuth token）。

**L2a 本地语言服务器**：`POST /exa.language_server_pb.LanguageServerService/GetUserStatus`

| 项 | 值 |
| --- | --- |
| 请求体 | `{"metadata": {"ideName":"antigravity","extensionName":"antigravity","ideVersion":...,"locale":"en"}}` |
| 鉴权 | 本地端口 + `X-Codeium-Csrf-Token`（缺失直接报 `Missing CSRF token`） |
| 发现方式 | 读 `language_server_*.exe` 命令行里的 `--extension_server_port=<n>` 与 `--csrf_token=<hex>`，再探测监听端口 |
| 返回 | `userStatus.planStatus.planInfo.{planName, teamsTier, monthlyPromptCredits, monthlyFlowCredits}`、`userStatus.planStatus.{availablePromptCredits, availableFlowCredits}`、`userStatus.cascadeModelConfigData.clientModelConfigs[].quotaInfo.{remainingFraction, resetTime}` |
| 官方程度 | 社区逆向（[AntigravityQuotaWatcher](https://github.com/wusimpl/AntigravityQuotaWatcher)） |

注意：**没有** `usedPromptCredits` 字段——已用量要用 `monthly - available` 自己算；
`GetCommandModelConfigs` 里没有额度，模型级额度实际来自上面 `cascadeModelConfigData`。

**L2b Google 后端**（`https://cloudcode-pa.googleapis.com/v1internal:*`，OAuth Bearer，
project 放 body）：

| 端点 | 给什么 | 关键字段 |
| --- | --- | --- |
| `loadCodeAssist` | 只有套餐，**没有额度数字** | `currentTier` / `paidTier` / `allowedTiers` / `cloudaicompanionProject` |
| `fetchAvailableModels` | **模型级**剩余额度 | `models["<model>"].quotaInfo.{remainingFraction, resetTime}`；有 daily-sandbox → daily → prod 三级回退 |
| `retrieveUserQuotaSummary` | **5h / 7d 分组**额度 | `groups[].buckets[] = {bucketId, window, remainingFraction, resetTime, displayName, description}`，`bucketId` 形如 `gemini-5h` / `gemini-weekly` / `3p-5h` / `3p-weekly` |

CPA 就是走第三条：`/v0/management/api-call` 用账号 OAuth token 代打
`retrieveUserQuotaSummary`，对外给出 5h + 7d 两条额度条。

**Gemini CLI 的额度接口不一样**：`retrieveUserQuota`（`v1internal`）返回
`buckets[] {remainingAmount, remainingFraction, resetTime, tokenType, modelId}`，
官方 CLI 的 `/stats` 正是调它显示 pooled 额度。**别把两套字段混用**：Antigravity 侧是
`remainingFraction`，Gemini CLI 侧才有 `remainingAmount` / `tokenType`。

**L1 官方文档只给上限，不给实时剩余**：Gemini CLI 官方文档
[quota-and-pricing](https://github.com/google-gemini/gemini-cli/blob/main/docs/resources/quota-and-pricing.md)
披露 Google AI Pro 1500 请求/用户/天、Ultra 2000/天、Code Assist 个人版 1000/天，
并说明 `/stats` 可看当前额度；实时剩余量与重置时间**没有官方 API 文档**。

### 3.4 GitHub Copilot

**L2 主动查询**：`GET https://api.github.com/copilot_internal/user`

| 项 | 值 |
| --- | --- |
| 鉴权 | GitHub token（`githubHeaders(state)`，含 `Authorization`、`Editor-Version` 等） |
| 返回 | `quota_snapshots.{chat, completions, premium_interactions}`，每项 `{entitlement, remaining, quota_remaining, percent_remaining, unlimited, overage_count, overage_permitted, quota_id}`；顶层还有 `copilot_plan`、`quota_reset_date`、`chat_enabled`、`access_type_sku` |
| 官方程度 | Copilot 客户端内部 API（逆向）；无公开文档 |
| 证据 | [get-copilot-usage.ts](https://raw.githubusercontent.com/ericc-ch/copilot-api/master/src/services/github/get-copilot-usage.ts) |

### 3.5 AWS Kiro

**L2 主动查询**：`GET https://q.<region>.amazonaws.com/getUsageLimits`（Kiro IDE 走
`codewhisperer.<region>.amazonaws.com`）

| 项 | 值 |
| --- | --- |
| 查询参数 | `origin=AI_EDITOR`、`resourceType=AGENTIC_REQUEST`、`isEmailRequired=true`（外部 IdP 还要 `profileArn`） |
| 鉴权 | `Authorization: Bearer <accessToken>`（外部 IdP 另加 `TokenType: EXTERNAL_IDP`，API Key 走 `tokentype: API_KEY`）；SDK 风格头 `amz-sdk-invocation-id`、`amz-sdk-request: attempt=1; max=1`、`user-agent: aws-sdk-js/1.0.0 … KiroIDE-<ver>-<machineId>` |
| 返回 | `usageBreakdownList[] {resourceType, unit, usageLimit(WithPrecision), currentUsage(WithPrecision), overageCharges, overageRate, overageCap, nextDateReset, freeTrialInfo{...}}`、`daysUntilReset`、`nextDateReset`、`subscriptionInfo{type, subscriptionTitle, ...}`、`overageConfiguration.overageStatus` |
| 剩余量算法 | `usageLimitWithPrecision - currentUsageWithPrecision`（再叠加仍 ACTIVE 的 `freeTrialInfo`） |
| 写接口 | `POST https://q.<region>.amazonaws.com/setUserPreference`，body `{"overageConfiguration":{"overageStatus":"ENABLED"|"DISABLED"},"profileArn":"..."}` |
| 官方程度 | 社区逆向（[kiro2api](https://github.com/caidaoli/kiro2api) `auth/usage_checker.go`、[Kiro-Go](https://github.com/Quorinex/Kiro-Go) `proxy/kiro_overage.go`） |

### 3.6 其它订阅与国产 Coding Plan

| 供应商 | 接口 | 鉴权 | 官方程度 |
| --- | --- | --- | --- |
| Moonshot Kimi | `GET https://api.moonshot.cn/v1/users/me/balance` → `data.{available_balance, voucher_balance, cash_balance}` | `Authorization: Bearer <API key>` | **官方文档**（[platform.kimi.com/docs/api/balance](https://platform.kimi.com/docs/api/balance)）。注意国内 `platform.kimi.com` 与国际 `platform.kimi.ai` 的 Key 与端点必须同站 |
| MiniMax | `GET https://www.minimax.cn/v1/token_plan/remains` → Token Plan 套餐额度与用量 | `Authorization: Bearer <订阅 key>` | **官方文档**（[platform.minimaxi.com/docs/token-plan/faq](https://platform.minimaxi.com/docs/token-plan/faq)「如何查看 Token Plan 用量」） |
| Z.ai / 智谱 GLM Coding | `GET https://api.z.ai/api/monitor/usage/quota/limit`（国内 `https://open.bigmodel.cn/api/monitor/usage/quota/limit`） | 需 `Authorization`（不带会回 `code:1001 Authentication parameter not received in Header`） | **端点实测存在，但未收录进官方文档**（docs.z.ai 全量目录里没有），按社区逆向对待 |
| Cursor | 社区常引用的 `api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage` 等 | — | **未能证实**（本项目调研未取到任何源码或文档证据） |
| Windsurf / Zed / Trae / Qoder | — | — | **未能证实** |

阿里云百炼、火山方舟的配额查询 API：未能证实。

## 4. 给 AMKR 的落地建议

现状：AMKR 的按 Key 探测缓存是
`providers.<id>.keys.<key>.capabilities = {models, route_status, errors, checked_at}`，
只解析上游 `Retry-After`，不采集任何限额信号；文档里也明确写着「统计不是配额」。
要做「每个 Key 还剩多少」，建议按 L3 → L2 的顺序推进：

1. **先做 L3 被动采集（零额外请求）**。转发响应时顺手抓
   `anthropic-ratelimit-unified-*`、`x-codex-*` 等限额头，按 Key 落到
   `providers.<id>.keys.<key>.quota`（形如 `{windows: {5h: {used_percent, resets_at}, 7d: {...}}, source: "header", observed_at}`）。
   与现有 capabilities 一样**按 Key 独立**，绝不跨 Key 合并。
   落点现成：`internal/proxy/lifecycle.go` 的 `streamLifecycle` 已经在收尾时解析上游
   `Retry-After`（字段 `retryAfter`，随 `onFinish(MetricRecord, failed)` 出去），
   限额头可以挂在同一条链路上，不必新开一条响应读取路径。
2. **再做 L2 主动探测**。按供应商类型调用 3.1–3.6 的接口，复用
   `ProbeKeyCapability` 那套接缝（`Prober` + 15s 超时 + `POST /api/providers/{id}/keys/{name}/probe`），
   但**单独缓存**（`quota` 与 `fetched_at` / `error`），不要污染 `capabilities`——前者是
   「能不能用」，后者是「还剩多少」，刷新节奏也不同。
3. **最后接冷却**。额度耗尽的 Key 直接进入冷却，而不是等一次 `429`。

实现时必须守住的边界：

- 这些接口都没有公开文档，随客户端版本变化；解析要**容错 + 记录原始响应**，字段缺失时显示
  `—` 而不是 `0`（与成本估算页现在的取舍一致）。
- 主动查询要限流自保：Claude 的 usage 端点会返回 `429`（Claude Code 自己都会退回 60 分钟内的
  本地快照），建议按 Key 设最小刷新间隔。
- OAuth token 的刷新与存储是新的凭据面，别和现有 `api_key` 混在一个字段里。
- 有一类供应商**已经有官方文档化的余额接口**（Kimi 的 `users/me/balance`、MiniMax 的
  `token_plan/remains`），它们本来就用 API Key 鉴权，接入成本最低，可以先拿它们把
  「按 Key 显示额度」的链路和 UI 跑通，再扩到需要 OAuth 的订阅账号。
- 订阅额度只用于**观测与选路**，不要拿它当计费依据。

## 5. 参考

- CLIProxyAPI：<https://github.com/router-for-me/CLIProxyAPI> ／ Management API 文档
  <https://help.router-for.me/management/api>
- Claude Code 官方文档：[Manage costs](https://code.claude.com/docs/en/costs)、
  [Monitoring（OTel）](https://code.claude.com/docs/en/monitoring-usage)
- Anthropic Admin API：[Usage and Cost API](https://platform.claude.com/docs/en/manage-claude/usage-cost-api)
- OpenAI Codex 源码：<https://github.com/openai/codex>
- Copilot：<https://github.com/ericc-ch/copilot-api>
