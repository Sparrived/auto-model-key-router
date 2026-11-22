# Gemini 原生协议入口设计

> 状态：**设计稿，尚未实现**。本文的“拟”均为待开发契约，不代表当前 AMKR 已支持 Gemini 入口、调用方周期预算或单请求选路解释。基线仅依据本地源码静态核对，未运行服务、构建或调用真实上游。目标是以 Gemini 的入站格式调用已配置的 **OpenAI Chat Completions 兼容上游**，不是实现 Google Gemini 原生上游的全功能透传。

## 1. 目标、范围与用户流程

管理员沿用现有配置创建模型、Provider 和上游 Provider Key，给调用方授予下游客户端 Access Key 或工作空间推理 Key；调用方把 Gemini 客户端的服务地址设为 AMKR 实例根地址、模型名设为 AMKR 的模型 ID/别名，使用下游凭据发起普通或 SSE 调用。AMKR 在同一治理链内鉴权、检查模型与供应商范围、解析工作空间及路由、检查预算、选择上游、转换请求/响应，最后统一结算和记录。调用方可在诊断能力交付后按请求 ID 查看被授权的选路解释。

**首版拟支持**：两个指定 POST 入口；单候选文本和多轮历史；system instruction；受限的采样参数；函数声明、工具调用与结果回填（AMKR 不执行工具）；PNG/JPEG 内联图片输入；可识别的思考文本和用量；无内容泄漏的错误、指标和追踪。标准库加现有包实现，不增加运行时依赖。

**不做**：Gemini 原生上游、自动探测并切换原生端点、Files/上传或远程 URI 抓取、图片/音频/视频生成与输入（除上述图片）、Google Search/代码执行/内置工具、结构化输出 schema、缓存资源、批处理、计数端点、Live、工具执行、多候选、服务端会话，以及完整的 Gemini 安全设置和思考签名语义。本文不是对所有 Google SDK/模型/字段的兼容承诺。未列出的语义字段不能静默丢弃，应明确拒绝；未来扩展须单独评审。

## 2. 当前基线与证据

| 事实 | 代码位置（仓库相对路径:行号） |
| --- | --- |
| server 注册的是 `/v1/models` 和 `/v1/` 通配代理，未见 `/v1beta/` 注册；代理活跃请求计数覆盖整次响应。 | `internal/server/routes.go:101-118`、`internal/server/routes.go:228-246` |
| 端点族表将 Chat、Messages、Responses 作为对话族；其他已登记多模态族区分透传。Gemini 不在表内。 | `internal/endpoint/endpoint.go:33-54`、`internal/endpoint/endpoint.go:78-105`、`internal/endpoint/endpoint.go:114-120` |
| 当前由请求体 `model` 和布尔 `stream` 解析模型与流模式；路径中的模型不会自动参与该过程。 | `internal/proxysupport/support.go:295-303`、`internal/proxysupport/support.go:326-333` |
| Messages/Responses 请求按字段转换为 Chat；默认上游路径和请求体的选择、模型替换及流式 usage 请求在既有支持层完成；Messages/Responses 可使用各自的原生探测路径。 | `internal/protocol/request.go:25-43`、`internal/proxysupport/support.go:107-137`、`internal/proxysupport/support.go:454-535`、`internal/proxy/retry.go:203-247` |
| Chat 响应可转换为 Messages/Responses；流有对应状态机、writer 和 SSE 分片/结果检查，而非已有 Gemini 转换器。 | `internal/proxy/stream.go:128-156`、`internal/proxy/stream.go:243-255`、`internal/proxy/stream_outcome.go:21-38`、`internal/protocol/anthropic.go:404-455` |
| 认证先判本地主凭据，再判 Access Key 和工作空间推理 Key；Access Key 模型清单按请求名、工作空间清单按解析后模型判定，供应商范围进入选 Key；受限凭据不可使用全局 unified-model。 | `internal/proxy/handler.go:199-219`、`internal/proxy/handler.go:241-258`、`internal/proxy/handler.go:316-355`、`internal/proxy/handler.go:452-471` |
| 当前默认凭据提取仅识别 Bearer 与 `x-api-key`；上游头过滤也尚未列入 `x-goog-api-key`，并把上游 Provider Key 以 Bearer 发出。 | `internal/auth/auth.go:53-63`、`internal/proxysupport/support.go:165-189` |
| 每请求取得 runtime 租约并覆盖流写入；流收尾有 `sync.Once`，记录指标、更新 Key 状态、释放名额和响应体；现有 usage 提取只识别三处 `usage`，指标归一化为六项计数。 | `internal/proxy/handler.go:27-44`、`internal/proxy/lifecycle.go:46-52`、`internal/proxy/lifecycle.go:130-187`、`internal/protocol/common.go:21-44`、`internal/metrics/usage.go:10-18`、`internal/metrics/usage.go:110-160` |
| 现有 JSON 请求体默认不限；代理默认允许单请求最多 32 次上游调用；访问日志包含原始 query，上游请求亦会重编码并转发 query。 | `internal/proxy/proxy.go:50-70`、`internal/proxy/proxy.go:269-271`、`internal/server/accesslog.go:97-115`、`internal/proxy/retry.go:752-764` |
| 总体方案把调用方周期预算和单请求追踪列为待设计的独立 P0，要求所有新协议复用治理，不能把它们当成当前已有功能。 | 总体设计稿（本地文档，未入库） |

上述是静态证据，不代表经真实供应商验证；额度冻结等既有上游 Key 状态也不等于调用方周期预算。Google 的公开 [GenerateContent API](https://ai.google.dev/api/generate-content) 和 [thought signatures](https://ai.google.dev/gemini-api/docs/thought-signatures) 是接口命名和限制的参考；本环境正文抓取失败，仅检索摘要，正式实现前须复核完整官方文档及 SDK 实际行为。

## 3. 配置、路由与内部数据（拟新增）

- 不新增配置开关、持久化对象或管理 API；入口随实现交付而开放，旧配置无需迁移。其模型/别名、Provider、Key、工作空间、任务及模型路由均用现有配置，旧 `/v1/` 行为不变。管理员不必配置一个名为 Gemini 的上游；仅可选能消费 Chat Completions 的上游。UI 和文档必须称之为“Gemini 入站兼容”，不称“Gemini 原生上游”。
- server 为两个动作新增精确受控分派，仍使用活跃请求计数并进入同一个 proxy `Handle` 及 runtime 租约；不能先向内部 HTTP 地址再请求一次，不能新建绕过鉴权/指标的代理。入站解析只负责建立类型化元数据：`family=gemini`、原始安全路径、解码后的请求模型名、动作、流标志、干净的 query。不要把动态模型路径直接作为既有端点表前缀，否则模型名会污染族分类、指标及路由计划。
- 路径形式为 `/v1beta/models/{model}:generateContent` 和 `/v1beta/models/{model}:streamGenerateContent`。从转义路径按单个原始路径段与固定动作解析，仅解码一次并限制 1–256 UTF-8 字节，拒绝空值、非法转义、控制字符和未转义斜杠；编码斜杠可成为模型名字符，但不得再次解码或拼接到上游 URL。以解析后的完整名称复用现有模型/别名、`model[key]` 和权限语义。为避免 Go `ServeMux` 规范化路径造成歧义，须测试 `EscapedPath`、`RawPath`、`%2F`、`%252F`、冒号和重复斜杠；不能在规范化重定向前暴露凭据或改变授权模型。未知动作/多段路径返回 404；非 POST 405，`Allow: POST`。
- 转换层创建**独立副本**作为内部 Chat 请求：模型来自路径，`stream` 只由动作决定；不改原请求体与其他协议的解析方式。请求中的顶层 `model` 或 `stream` 一律 400，避免两个权威来源。将 Gemini 当成对话族参与默认对话路由计划，但上游使用已配置的 `openai` 路径及选中的 `upstream_model`；不把 `v1beta/models/...` 或 Gemini query 当上游地址。任务固定参数在转换后的 Chat 字段上做既有冲突检查和覆盖，不得与转换顺序互相绕过；Access Key 对任务的既有限制保持不变。
- 请求记录中的 API 格式拟固定为 `gemini/generateContent` 或 `gemini/streamGenerateContent`，不要把模型名拼进格式维度；请求模型仍单独存储。新增常量和分派时同时覆盖端点族、计划选择、指标分类及历史展示，不复用静态路径字符串假装来自 Chat。

## 4. HTTP 与鉴权契约（拟新增）

### 4.1 入口、头和查询

| 项 | 首版契约 |
| --- | --- |
| 方法/类型 | POST；请求体必须是 UTF-8 `application/json` 对象；非法 JSON 400，不支持的媒体类型 415。非流返回 `application/json`。 |
| 凭据 | 支持 `x-goog-api-key`、`Authorization: Bearer ...`、`x-api-key` 三种**下游**凭据；对应本地主凭据、Access Key 或工作空间推理 Key。多个不同来源同时出现即使值相同也返回 400；同名重复或空值也 400，避免优先级歧义。凭据缺失/无效在启用鉴权时为 401；停用或无权限 403。现有默认 authorizer 的本地主凭据为空时仍关闭鉴权，见下行。 |
| 接入方式 | 仅对 Gemini 请求建立认证用克隆，把 `x-goog-api-key` 规范化为既有认证可识别形式；保留原有可替换 authorizer 钩子并明确传入含原始非凭据头和规范化凭据的克隆，不修改来访请求或改变旧路径优先级。无本地主凭据时既有配置是关闭鉴权，部署需明确风险，不以新协议悄悄改变。 |
| query | 普通动作不接受 query；流动作只接受零个参数或唯一的 `alt=sse`，二者均返回 SSE。重复、未知值或其他 query（包括 `key`）400，**不支持 URL 携带凭据**。这有意缩小官方协议范围。 |
| 安全过滤 | 凭据和 `alt` 一律不得进入上游 query；上游只用选中 Provider Key 的 Bearer。仅对 Gemini 请求的上游头副本剔除 `x-goog-api-key`，不得把下游凭据透传；其他协议原有头转发行为不变。Gemini 所有路径的访问日志在记录前仅保留路径、不记录任何 query，连非法请求也生效；必要时移除反向代理、错误与诊断日志中的 query，不能依赖只在 handler 拒绝后再脱敏。 |

上游地址来自已配置 Provider，不依据请求体 URI 或模型名构造目标。调用方不得借模型、路径、头或 query 指定任意上游；工作空间推理 Key 忽略工作空间头、面板 Key 不获得推理权。自定义 authorizer、头规范化和受限 Key 仍必须共享相同授权路径；禁止认证克隆上游发送。

### 4.2 文本、系统指令与参数

以下为新增契约示例，模型 `assistant-model` 必须预先存在且已授权：

```http
POST /v1beta/models/assistant-model:generateContent
Content-Type: application/json
x-goog-api-key: <下游客户端 Access Key>

{
  "systemInstruction": {"parts": [{"text": "回答简短"}]},
  "contents": [
    {"role": "user", "parts": [{"text": "你好"}]}
  ],
  "generationConfig": {"temperature": 0.4, "maxOutputTokens": 128}
}
```

| Gemini 字段 | Chat 转换及校验 |
| --- | --- |
| `contents` | 必需的非空数组，依次转换为 `messages`；角色仅 `user`、`model`，缺省为 `user`；`model` → `assistant`。每个 content 须有非空 parts；按照原序保留可表示的文本/图片块。 |
| `systemInstruction.parts[].text` | 合并为一条位于最前的 `system` 消息，各段按顺序拼接；非文本系统块或不支持的角色 400。 |
| `generationConfig.temperature`、`topP`、`maxOutputTokens`、`stopSequences` | 分别映射 `temperature`、`top_p`、`max_tokens`、`stop`；`temperature` 为 0–2 的有限数，`topP` 为 0–1 的有限数，`maxOutputTokens` 为 1–2147483647 的整数，`stopSequences` 为 1–5 个非空字符串（每项最多 256 个 UTF-8 字节）；违反 400。先转换再按既有任务固定参数冲突规则处理。`candidateCount` 仅允许 1（缺省同 1），其他值 400。 |
| JSON 字段命名 | 接受列出的 lowerCamelCase 及对应 protobuf snake_case 别名，如 `system_instruction`、`generation_config`、`inline_data`、`mime_type`、`function_call`；同一对象同时出现同义名即 400，不由顺序决定。其他字段/part 类型按不支持返回 400。 |

正常返回示例（上游未知的可选字段不伪造）：

```json
{
  "candidates": [{"index": 0, "content": {"role": "model", "parts": [{"text": "你好！"}]}, "finishReason": "STOP"}],
  "usageMetadata": {"promptTokenCount": 12, "totalTokenCount": 17}
}
```

### 4.3 工具调用和回填

只接受 `tools[].functionDeclarations`，每项包含唯一 `name`、可选 `description` 和 JSON Schema 对象 `parameters`，转换为 Chat 的 `tools[{type:"function",function:{...}}]`；schema 必须是对象，按原结构传递，不声称不同上游支持所有 schema 关键字。`toolConfig.functionCallingConfig.mode` 缺省/`AUTO` → `auto`，`NONE` → `none`，`ANY` → `required`。`ANY` 有非空 `allowedFunctionNames` 时，必须是已声明且无重复的名称：发送被允许子集并保持 `required`；其余模式携带该清单、空/未知清单、其他工具类别或配置 400，不静默忽略。

`model` 的 `functionCall` → Chat assistant `tool_calls`：JSON 对象 `args` 序列化为 `function.arguments`；`user` 的 `functionResponse` → Chat `role:tool`，`response` 对象序列化为内容。函数名和 ID 严格配对；有 ID 时按 ID 关联且名称一致，无 ID 时仅在同一待完成回合内同名未匹配调用**恰好一个**才按名称匹配，否则 400。对历史调用缺失 ID 的情形按内容和 part 序号生成请求内确定性 ID；重名并行但无 ID 不猜测。每个调用恰好配一次结果，未知/重复结果、缺失结果或顺序不能映射到 Chat 时 400。一个 model 回合仅允许先文本、后调用，工具响应须紧随该回合且先于后续普通用户文本；不悄悄重排内容。上游返回的 ID 可用时原样返回；缺失则分配唯一、对该响应稳定的 ID，客户端回填时带回。上游参数必须能解析为 JSON **对象**，否则转换失败，不能用 `{}` 伪造成功。为避免改变工具语义，Gemini 请求不得使用既有“过滤工具后重试”的降级分支。

```json
{
  "contents": [
    {"role": "user", "parts": [{"text": "查询北京天气"}]},
    {"role": "model", "parts": [{"functionCall": {"id": "call_1", "name": "weather", "args": {"city": "北京"}}}]},
    {"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "weather", "response": {"temperature": 20}}}]}
  ],
  "tools": [{"functionDeclarations": [{"name": "weather", "description": "查询天气", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}]}]
}
```

非流式输出用 `candidates[0].content.parts[].functionCall` 返回完整 name、id、args；工具调用完成是 `STOP`，不运行工具。客户端自行执行函数，携带 model 调用回合和 user 结果发起下一次请求。

### 4.4 图片、思考及明确拒绝

- 仅 `user` 的 `inlineData:{mimeType,data}`，类型限 `image/png`、`image/jpeg`；`data` 是标准 base64。校验编码、MIME、文件头及宽高后映射 Chat `image_url.url = data:<mime>;base64,<data>`，不下载 URL、不落盘、不记录图片内容。每张解码后最多 8 MiB、宽高乘积最多 1600 万像素，每次最多 4 张，整个编码 JSON 不超过 20 MiB；若宿主 `MaxJSONBytes` 更小，取更小上限。超请求体/图片大小 413，格式、MIME、尺寸或数量错误 400。并发内存预算须按解码和编码副本的放大倍数评估，不能只限单图。
- `generationConfig.thinkingConfig.includeThoughts` 为 false/缺省时不输出上游推理文本；true 时**仅当** Chat 上游明确返回字符串 `reasoning_content`，映射为独立 `{"text":"...","thought":true}` part，不能把其混进最终答案；上游无该字段则不虚构。每次只认这一种已约定的非标准上游字段，其他推理字段不等价、不猜测。
- `thinkingBudget`、`thinkingLevel` 无可靠跨协议等价项，一律 400；不把数字预算硬映射为 `reasoning_effort`。现有模型级推理强度仍按既有规则应用，回显只显示实际可知值。
- Chat 无 Gemini 签名语义：任何入站 `thoughtSignature` 或历史 `thought:true` 明确 400，不生成、丢弃或伪造签名。故首版 `includeThoughts:true` 返回的思考块是展示用途，客户端重放历史前必须移除这些块；依赖签名的 Gemini 原生多轮/工具调用不在兼容承诺中。此限制必须显示在接入提示和示例旁，不得暗示可无损代理 Gemini 3。
- `fileData`、远程 URL、非图片媒体、输出图片、搜索/代码工具、`safetySettings`、JSON schema 输出、未知生成参数或未知 part 一律 400，错误只指出字段路径，不回显内容。避免现有通用适配器把未知 part 序列化成普通文本。

## 5. 输出、流、用量与错误（拟新增）

### 5.1 响应与流状态机

非流将 Chat 的首个 choice 中文本和完整工具调用按可表示顺序转成 Gemini candidates；只支持一个 choice。`stop` / `tool_calls` → `STOP`，`length` → `MAX_TOKENS`，`content_filter` → `SAFETY`；其他已收到且不认识的 finish reason → `OTHER`，不伪造安全评分或反馈。不可解析、缺 choice、空输出且无可识别终止原因、工具参数损坏返回 502。上游拒绝或过滤仅表达可证实结果。

流动作返回 `Content-Type: text/event-stream; charset=utf-8`。每个 SSE 事件只包含一行 `data: <单个 GenerateContentResponse JSON>` 和空行，文本为增量；**不输出** OpenAI 的 `[DONE]`、Responses/Anthropic 事件名。示意：

```text
data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"你"}]}}]}

data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"好！"}]}}]}

data: {"candidates":[{"index":0,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"totalTokenCount":17}}

```

状态按 `准备 → 等待首个有效上游事件 → 输出中 → 成功终止/失败/客户端取消 → 一次收尾` 变化。首版转换器为**每个请求**单独维护 SSE 字节分帧、UTF-8、choice index、文本/思考、工具 index/id/name/分片参数、finish reason 和累计 usage；不得共享可变状态。允许跨 TCP chunk、LF/CRLF 事件边界、usage 尾帧；文本及时发送，工具按 index 暂存并在上游完成、JSON 校验通过后以完整 `functionCall` part 发出，不把半个 JSON 当对象。单个事件最多 1 MiB、单个工具参数最多 256 KiB、每回合最多 32 个工具；超限立即失败并释放资源。上游类型、choice、流帧、终止标记无效或 EOF 前无有效 finish reason 是失败，不补造成功。

必须调整当前 `streamResponse.writeTo` 先写头的实现（`internal/proxy/stream.go:45-55`）：Gemini 在可重试的上游 HTTP 状态判断后，使用现有首字节超时读取并校验至少一个完整事件（允许仅有角色的有效事件），最长等待该超时，单事件受 1 MiB 限制；首事件非法、提前 EOF 或超时时，在仍未提交下游响应头的情况下依既有重试、回退和上游调用次数上限处理。有效事件到达后才提交响应头并输出，不无限缓冲整个响应。下游一旦已提交头或内容，绝不能换 Key 重放或伪造最终成功；上游超时、损坏、取消等以 `data: {"error":...}` 项结束并关闭连接（这是 AMKR 流内错误约定，SDK 不一定能识别，需客户端集成验收），不发送成功 finishReason。客户端断开则停止读取/取消上游，不再写错误；依一次收尾释放租约、Key 和资源。

### 5.2 用量与预算

- 本设计只接 Chat 兼容上游；从原始 Chat usage 的字段和值构造下游 `usageMetadata`，并将同一次上游 usage 送入**现有**指标归一化链一次，不能从丢失字段存在性和推理详情的归一化结果反推：明确给出的 `prompt_tokens` → `promptTokenCount`，`total_tokens` → `totalTokenCount`，`prompt_tokens_details.cached_tokens` → `cachedContentTokenCount`。仅当所选上游的契约明确 `completion_tokens_details.reasoning_tokens` 属于 `completion_tokens` 的子集且两项为非负整数、前者不大于后者时，该值 → `thoughtsTokenCount`，`completion_tokens - reasoning_tokens` → `candidatesTokenCount`；无法确认包含关系或字段缺失时省略后二者。不能把未知推理 token 当 0，不能虚构 `modelVersion`、`responseId`、安全评分或费用。
- 流每次 usage 为累计快照，按字段更新而非累加；尾帧可在 finish reason 后到达，成功终止事件在最终 usage 已处理后发出。无上游 usage 就省略 `usageMetadata`；中断时记录实际可确认的部分及“未知/不完整”，不能通过多个响应块重复扣费。重试时每次真实上游尝试的成本均按实际已知用量记录；一次下游请求的预算预留/结算/释放不得重复。若只有失败尝试的部分用量，同样不可当免费。
- 现有 `metrics.normalizeUsage` 对缺失值会落成 0（`internal/metrics/usage.go:117-126`），不足以向预算区分“未知”与“真实零”。实施时须在预算对接的计量边界保留 presence/完整性标志，并保证存储或旁路结算可区分；不能仅读取现有聚合 0。对外可省略字段，不把内部缺失转成精确 0。
- **依赖而非现状**：调用方预算的周期、预留、并发超调、缺价/未知用量策略由周期预算专题确定。实现时在路由解析、上游发送之前调用同一预算准入，流结束/失败/取消时一次结算；若启用预算但准入或结算依赖不可用、未知用量无法按专题策略处置，则失败关闭，不能作为无预算放行。预算未配置时维持其他协议的默认行为，不在 Gemini 旁另建预算账本。既有额度冻结照常参与选 Key。

### 5.3 错误形状及状态

未提交响应头时所有 Gemini 入口错误，包括鉴权、解析、路由、预算和上游错误，统一使用：

```json
{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"不支持字段 generationConfig.thinkingConfig.thinkingBudget"}}
```

| 情况 | HTTP / status | 行为 |
| --- | --- | --- |
| 格式、参数、冲突凭据、不支持字段 | 400 / `INVALID_ARGUMENT` | 不访问上游；只报告字段或安全原因。 |
| 凭据缺失/错误；模型/供应商/空间无权 | 401 / `UNAUTHENTICATED`；403 / `PERMISSION_DENIED` | 权限检查与既有协议顺序一致，不泄露其他模型和 Key。 |
| 不存在的模型/动作；非 POST | 404 / `NOT_FOUND`；405 / `METHOD_NOT_ALLOWED` | 405 带 `Allow: POST`；后者是本服务约定，非宣称 Google 原样。 |
| 请求过大/媒体类型错误 | 413 / `RESOURCE_EXHAUSTED`；415 / `UNSUPPORTED_MEDIA_TYPE` | 发送前拒绝；后者为本服务约定。 |
| 预算拒绝或上游限流 | 429 / `RESOURCE_EXHAUSTED` | 预算无上游调用；已知安全的 `Retry-After` 可转发，不能伪造重置时间。 |
| 上游异常/不可解析、不可用、超时 | 502 / `UNAVAILABLE`、503 / `UNAVAILABLE`、504 / `DEADLINE_EXCEEDED` | 可重试仅在未提交下游响应前；限制重试次数与无效 Key 健康更新。 |
| 内部故障 | 500 / `INTERNAL` | 通用安全文案；不返回堆栈、密钥、请求/响应正文或上游原文。 |

上游其他 4xx 按不泄漏内容的等价 HTTP 状态映射，无法安全判定时用 502；不盲目把任意上游错误消息或请求 URL 回传。服务器关闭、无候选、权限拒绝等早退也要经过 Gemini 错误封装，而不是直接写现有 OpenAI 样式。流内错误遵循 5.1，不再更改已提交的 HTTP 状态。

## 6. 追踪、并发、兼容和 UI（拟新增）

- **追踪依赖**：单请求选路解释属于独立 P0。接入统一请求 ID/追踪上下文，记录协议动作、请求模型/解析模型、空间或受限调用方标识、候选排除/选择、尝试、状态、耗时、预算准入和终态；权限沿追踪专题执行，受限调用方只能查自己的记录。只记录必要元数据和上游 Key 显示名/脱敏标识，不记录正文、函数参数、图片、思考文本、凭据、完整 URL/query。相关功能未实现前，不宣称可以按 ID 查询；其完成和联调是本入口与预算/追踪共同上线的门禁。
- 请求读取同一 runtime 租约和配置快照，热更新只影响新请求，旧流使用旧资源直到结束；不在全局对象复用解码器或工具缓冲。流生命周期的成功、失败、取消、超时及写失败要幂等结算并释放一次；预留/提交失败也要有确定的失败关闭和恢复路径。重试按每次上游尝试分别记事实、按一个下游请求关联，预算不重复预留或结算。预算专题的并发边界不由本协议另行定义。
- 现有认证、`/v1/models`、三种对话方言、端点路由、任务、Key 轮换和配置格式保持兼容；不新增 Gemini 模型列表 API。客户端若依赖 `/v1beta/models` 发现、无 `alt=sse` 时的其他 Google 输出形态、URL 参数 key、原生思考签名或不支持的字段，需明确提示无法使用，不以“兼容”掩盖。
- 界面只在现有接入说明/向导（若该专题已交付）增加“Gemini 入站兼容”选项，展示实例根地址、经授权模型名、`x-goog-api-key`、可复制示例和限制；不要展示完整上游 Provider Key，也不新增管理开关。默认只显示安全占位符；错误显示状态、字段及下一步。指标按固定 API 格式展示；追踪/预算页面以各自权限和实现为准，未知用量显示“未知/不完整”，不显示为 0。UI 属于拟实施，不是现有页面事实。

## 7. 端到端验收矩阵（实施后执行，非本次结果）

使用隔离配置、临时数据库和可控本地假 Chat 上游；记录上游调用数、请求头/体、流事件与指标，禁止用真实密钥或付费账号。由独立 QA 按用户操作复核。

| 场景 | 必须断言 |
| --- | --- |
| 普通调用 | 用 Access Key 及模型别名发文本/系统指令，假上游仅收到正确 Chat 模型、消息和参数；下游为 Gemini JSON；指标记协议格式、请求名与真实上游。 |
| 流式文本/思考 | 两种 query 形态均为 SSE，字节切碎含 UTF-8/CRLF，增量有序；`includeThoughts` true 才输出可识别思考 part；无签名伪造；完成仅一次。 |
| 工具非流和流 | 声明、调用（分片 arguments）、id 与 JSON 对象完整，客户端回填得到下一轮回答；双工具/重复名/缺 id 的唯一匹配与歧义拒绝均覆盖；上游非法 JSON 或超限不产生假成功，也不触发删工具重试。 |
| 图片 | PNG/JPEG 先验证再作为 data URL 到假上游，内容顺序正确；无效 base64、伪造 MIME、超体积/像素/张数及 fileData 均在发送前拒绝，不出现图片内容日志。 |
| 权限与凭据 | Bearer、`x-api-key`、`x-goog-api-key` 分别复用现有权限；冲突/重复/空凭据、禁用 Key、面板 Key、模型/Provider 拒绝、工作空间头伪造、unified/任务限制均有断言；非法含 `?key=` 请求的各级日志和上游均无密钥。 |
| 路径/query | 空模型、非法/双重转义、编码斜杠、重复斜杠、未知动作/方法、重复 alt、非法 query 按契约失败；授权按最终模型名，绝不出现额外上游 URL/query。 |
| 预算/追踪 | 未配置时既有行为不变；配置后超限/权限拒绝**上游调用数为 0**，并发、重试、断流时预留/结算一次；未知 usage/价格按预算专题处理、非 0 元放行；请求 ID 仅可查询本人授权的元数据。前提是两专题能力已完成。 |
| 状态与失败 | 上游 429/5xx、超时、非法 JSON、空 choice、过滤、长度终止、首包前失败、首包后中断、客户端取消分别检查状态/错误信封、Key 健康、次数与资源释放；已提交后不重试、不出现成功终止帧。 |
| 用量/兼容 | 已知 prompt、total、cached、reasoning 字段逐项核对；缺失与真实 0 区分，尾帧不重复计数；旧配置及 Chat/Messages/Responses 回归不变，热更新进行中流不混用快照。 |
| SDK 边界 | 在本地假上游和隔离环境用选定版本 Gemini SDK 验证 base URL、头、解析与中流错误；记录 SDK 版本和不兼容项，不以 REST 样例替代客户端实测。 |

## 8. 实施顺序、文件清单与发布门禁（拟新增）

1. **协议边界**：`internal/server/routes.go`、`internal/server/accesslog.go` 接入安全路由与 query 脱敏；`internal/auth/auth.go` 或仅 Gemini 的认证克隆接缝处理新头；`internal/endpoint/endpoint.go` 定义对话族与计划映射（动态模型另存）；`internal/proxysupport/support.go` 针对 Gemini 清除下游头、保留上游安全路径和参数边界。
2. **转换与共享治理**：新增 `internal/protocol/gemini.go`，扩展 `internal/proxy/context.go`、`internal/proxy/handler.go`、`internal/proxy/retry.go`、`internal/proxy/query.go` 对路径模型、请求构造、错误、鉴权和工具重试的处理；保持同一租约、Key 选择及上游调用限制。预算与追踪接缝等其专题明确后集成，不复制实现。
3. **流与计量**：扩展 `internal/proxy/stream.go`、`internal/proxy/stream_outcome.go`、`internal/proxy/stream_writers.go`、`internal/proxy/lifecycle.go`，增加每流有界状态、延迟提交、终态/取消与一次收尾；按需要扩展 `internal/metrics/usage.go` 及存储边界以保留未知标志，不破坏历史口径。
4. **文档与界面**：实现阶段更新现有接入文档和 `web/src/` 中实际的接入说明/指标显示；具体组件按届时代码确认。测试优先扩展各包现有测试套件，补协议解析、权限、真实用户流程与假上游 E2E；不修改 `webui/` 产物。
5. **发布门禁**：完整复核 Google 官方协议与 SDK；满足矩阵；独立 QA 以隔离环境验证；预算/追踪专题的统一接入、未知用量语义及同一请求多次上游尝试的结算契约完成后，方可声称预算与追踪已经覆盖本入口。此文档任务不启动服务、不运行构建或测试，不等同已实现或已验收。
