# 按任务难度路由（difficulty routing）调研与设计

> 状态：**设计稿，尚未实现**。本文回答两个问题：市面上的「按难度选模型」一般怎么做，
> 以及这套能力怎么长在 `unified-model` 上而不破坏现有语义。
>
> 完整调研（每节的信号 / 判定 / 代价与出处）见同目录
> [`llm-routing-research.md`](llm-routing-research.md)；本文第 1 节是它的浓缩版。
>
> 结论先行：**稳定的起始任务启发式 + 三档计划 + 自动对话前缀亲和**。
> 不要求任意客户端传入 AMKR 会话 ID；利用请求已有历史与上游 response 句柄继承实际目标。
> 有相同且可验证的对话祖先就保持模型；没有可用关联信息时保守使用 standard/default，不伪造会话身份。
> 自动亲和与难度路由一起交付，不每轮按新分数切换。v1 不做训练分类器、embedding、LLM-as-judge
> 或连续多轮升降档状态机。本稿于 2026-10-13 根据“多来源且不能依赖会话 ID”的约束修订，尚无实现。

---

## 1. 业界做法与代价

按「判定依据」分成五类，从便宜到贵：

| 类别 | 代表 | 判定依据 | 代价 |
| --- | --- | --- | --- |
| 规则/启发式 | Kong AI Gateway、Bifrost 复杂度路由器 | token 估算、关键词、代码、工具、多轮深度 | <1ms，确定性，无外部依赖 |
| 显式声明 | Cursor 模式、Claude Code `/model`、各家 `model` 参数 | 由调用方说清用哪档 | 0 |
| 语义路由 | semantic-router、vLLM Semantic Router | embedding 相似度匹配意图 | 需 embedding 服务；5–50ms |
| Judge 模型 | Martian、NotDiamond、部分网关 | 小模型读 prompt 输出档位 | 每次多一次上游调用；+50–300ms |
| 训练分类器 | RouteLLM、Bedrock Intelligent Prompt Routing | 训练过的路由器预测「哪个模型能答对」 | 需偏好/质量数据与模型服务 |
| 级联升级 | FrugalGPT、BEST-Route | 先便宜模型，低置信再升级 | 需 verifier / 多次采样 |

关键事实与出处：

- **RouteLLM**（LMSYS）：矩阵分解 / BERT / 相似度加权 / 因果 LLM 四种路由器，用 Chatbot
  Arena 偏好数据训练；阈值按「强模型调用比例」标定（如 50% 对应 `mf` 阈值 0.11593）。
  官方评测口径是 MT-Bench 降本 85%、MMLU 45%、GSM8K 35%（均以保留 95% GPT-4 质量为前提）。
  [GitHub](https://github.com/lm-sys/routellm) · [复现说明](https://www.pondhouse-data.com/blog/saving-costs-with-llm-routing)
- **路由本身的开销**：规则 <1ms、embedding/ML 5–50ms，相对 500–2000ms 的模型推理可忽略
  —— 这正是「启发式够用」的量化依据。[对比](https://gingerlabs.ai/blog/routellm-vs-vllm-semantic-router)
- **OpenRouter `openrouter/auto`**：一个轻量分类器把 prompt 分到约 30 个细粒度任务类型，再按
  社区**近 7 天**的匿名花费份额排序候选（外加 `cost_tier`，默认 `low`）。两个可直接借用的产品
  决策：**多轮粘性**（同会话优先复用上次落点，避免重建 input cache）与**路由永不让请求失败**
  （分类/排序不可用时回退默认模型集）。[新版说明](https://openrouter.ai/blog/announcements/introducing-the-new-auto-router) · [Auto Router 文档](https://openrouter.ai/docs/guides/routing/routers/auto-router)
  —— 注意它与 NotDiamond 的关系**已经变了**：2025-01 的旧版由 Not Diamond 的 meta model 驱动，
  2026-08 的新版换成了市场花费信号。
- **Kong AI Gateway（纯启发式的现成先例）**：对 `messages[].content` 的 UTF-8 字节求和，
  `tokens = ceil(bytes/4)`，**阈值 80** 切 `large`/`small`，并把 `X-Prompt-Est-Tokens`、
  `X-LLM-Tier` 回写给调用方。[文档](https://developer.konghq.com/cookbooks/custom-llm-routing)
  —— 本设计的「估算 token + 阈值分档 + 响应头回显」与它同构。
- **Bifrost 复杂度路由器（最接近的竞品形态）**：确定性打分（精确关键词 + 代码内容 + 推理标记 +
  技术术语 + token 数）分 `simple/medium/complex/reasoning` 四档，**不额外调用任何模型**，边界与
  词表可调。[文档](https://docs.getbifrost.ai/features/governance/complexity-router)
- **Bedrock Intelligent Prompt Routing**：在**同一模型族内**（如 Haiku vs Sonnet）用训练好的
  路由器预测各候选模型的质量分，按质量/成本权衡分流，支持自定义路由器并线性调节偏保守/偏省钱。
  它也是唯一公布了完整三件套数据的方案：**路由开销约 85ms（P90）**、把 **87% 的请求分流到更便宜
  模型**、最高**省 63.6% 成本**。[GA 公告](https://www.investing.com/news/stock-market-news/amazon-bedrock-intelligent-prompt-routing-now-generally-available-93CH-3996691) · [AWS 博客](https://aws.amazon.com/blogs/machine-learning/use-amazon-bedrock-intelligent-prompt-routing-for-cost-and-latency-benefits)
  —— 85ms 说明「多一次判定」的真实代价量级，也说明 v1 不做 judge 分类器的取舍是诚实的。
- **Martian / NotDiamond**：专有路由模型，按 prompt 内容、所需能力与成本约束选模型，宣称可观的
  成本节省（Martian 自称极端场景最高 98%），但都是黑盒服务。[对比](https://www.respan.ai/market-map/compare/martian-vs-openrouter)
- **Judge 模型的隐性成本**：分类本身就是一次推理，受 prefill 延迟、排队与供应商抖动影响，
  实测量级 +50–300ms/请求，且多一条失败面。[GMI Cloud](https://www.gmicloud.ai/en/blog/routing-without-a-judge-model-why-sub-200ms-model-selection-needs-a-different-architecture)
- **级联（FrugalGPT）**：先便宜模型、按置信度升级，论文报告最高 98% 降本；但要 verifier 或多次
  采样，等于把一次调用变成 1–N 次，与本项目「一次请求尽量一次上游调用」的取向相反。
  [实现说明](https://getnadir.com/blog/frugalgpt-cascade-routing-implementation-tutorial)
- **产品侧的「固定档位」不是难度路由**：Claude Code 的 haiku/sonnet/opus 分工靠 subagent 定义里
  写死 `model`、或用 `/model` 手动切换；社区 issue 据此主张**没有自动路由**，并给出「93.8% 的
  token 流向 Opus」（该数字来自 issue 作者的统计，非官方数据）——这是「按难度路由」最现实的动机，
  也说明「按任务类型分档」比「按难度打分」更容易落地。
  [anthropics/claude-code#27665](https://github.com/anthropics/claude-code/issues/27665)

### 1.1 对 AMKR 的可复刻性排序

| 做法 | 结论 | 理由 |
| --- | --- | --- |
| 启发式打分 | ✅ **v1 采用** | 纯 Go、无依赖、<1ms、确定性，不改变「一次请求一次上游调用」的既有承诺 |
| 显式声明（请求头覆盖） | ✅ **v1 采用** | 成本 0；启发式误判时的逃生门，也是 agent 场景的正解 |
| Judge 模型 | ⏳ 后续可选 | 每次 +50–300ms、多一份 token 成本与失败面；还需要为分类器单独配模型与 key，并防止分类请求自身递归走 `unified-model` |
| Embedding 语义路由 | ❌ | 需要 embedding 服务或本地模型运行时，与「单一静态二进制、无运行时依赖」的定位冲突 |
| 训练分类器（RouteLLM/Bedrock） | ❌ | 需要标注数据与训练/服务链路；本项目没有质量反馈数据，且训练产物的可复现性与交付形态都不匹配 |
| 级联升级 | ❌（已有弱化版） | 现有 `fallback` 是**失败后**改发，不是**质量不足后**升级；真正的级联要 verifier，成本模型完全不同 |

启发式的已知短板要写在明处：它测量的是**体量与形态**（长度、工具、代码、多轮），不是真实难度。
「短而难」（如一句数学证明）会落到便宜档，「长而浅」（如长文档摘要）会落到贵档。这靠阈值、
显式覆盖与日志校准来收敛，不靠算法本身。

阈值不该照抄任何厂商数字：广为流传的「prompt 长度 500 / 2000 token 分档」查不到权威出处，
能查到的具体实现是 Kong 的 **80**、某 Node.js 教程的 **60**——彼此差一倍，都来自各自流量。
可移植的是**标定方法论**：RouteLLM 按「目标强模型调用比例」反解阈值。本项目可以先用
`observe_only` 收集分数，再用少量代表性任务检验分档边界，不需要训练神经模型。
调研中 vLLM Semantic Router 的 805 样本/准确率数字涉及模型微调，不能当作“只调阈值就能复现”
的收益，也不构成 AMKR 启发式准确率或省钱比例的保证。

---

## 2. 推荐设计

### 2.1 配置形状

放在 `unified_model` 下，新增一个**可选**的 `difficulty` 段；缺席即功能关闭，行为与今天逐字节相同。

```json
{
  "unified_model": {
    "default": { "primary": { "model": "gpt-4o" }, "fallback": { "model": "gpt-4o-mini" } },

    "difficulty": {
      "observe_only": false,
      "simple_max_score": 30,
      "complex_min_score": 65,
      "simple":   { "primary": { "model": "gpt-4o-mini" } },
      "standard": { "primary": { "model": "gpt-4o" } },
      "complex":  { "primary": { "model": "o3", "key": "main" } }
    }
  }
}
```

规则：

1. **三档固定命名**：`simple` / `standard` / `complex`，每档是与现有分族计划**逐字同构**的
   `{primary, fallback}`（模型可写别名、可固定 key，落盘时归一成模型 ID）。
2. **阈值决定档位**：`score < simple_max_score` → `simple`；`score ≥ complex_min_score` →
   `complex`；其余 → `standard`。两个阈值都可省，默认 30 / 65；取值域 `0..100` 且
   `simple_max_score < complex_min_score`。
3. **档位可缺**：只配 `complex` 就是「只有难题升级」，轻松请求落回 `default`——这正是最常见的用法。
   某档计划缺失 → 用 `unified_model.default`（含它自己的 fallback）。
4. **`observe_only`（默认 false）**：为真时照常打分、照常写日志与响应头，但**不改道**（一律走
   `default` 档）。它是给「没有自家流量数据、不敢直接上线」准备的校准档：先跑一周拿到分数分布，
   再定阈值、关掉它。四态都合法且各有明确含义：

   | `observe_only` | 有档位 | 行为 |
   | --- | --- | --- |
   | `true` | 无 | 纯采样：只记分数（**推荐的上线第一步**） |
   | `true` | 有 | 预演：记分数，路由不变 |
   | `false` | 有 | 正常按难度改道 |
   | `false` | 无 | 422：配置写了却不会有任何行为，明确报错而不是静默忽略 |
5. **难度只细化对话路径**：`routeKind(path) == "default"`（`/v1/chat/completions`、`/v1/messages`、
   `/v1/responses` 与未登记路径）才参与分档；`image` / `embeddings` / `speech` / `video` / `rerank`
   等分族计划照旧优先，难度不参与（嵌入与图像没有「难度」这一维）。
6. **不新增 `UnifiedPlanNames` 成员**（这是本设计的关键取舍，见 §2.6）。
7. **无 `enabled` 开关**：开关就是这一段在不在。WebUI 的开关落盘为「加/删这一段」。

### 2.2 初始难度判断：稳定的起始任务，而不是每轮最后一句

选择**显式提示优先 + 本地启发式 + 自动前缀亲和**：没有额外推理调用、不部署 ML 运行时，也不
要求四面八方的客户端传入一个新会话 ID。难度主要决定初始模型；已有前缀绑定优先于每轮新分数。

| 信息 | 作用 |
| --- | --- |
| 开场指令 + 第一条完整 user 任务（稳定锚点） | 初次选档；固定配置下，每次带相同起点的完整历史都能重算相同候选模型 |
| 最新任务/近期内容 | 观测任务变化与兼容性，不因为“继续”或新增历史自动升降档 |
| 工具定义 | 作为能力要求，不把 schema 大小视为难度；工具结果不是新的用户任务 |
| 全量输入大小、图像与工具需求 | 筛选可承载请求的模型、检查兼容性；不因历史增长就累加难度分 |
| 起始信息不足（例如“你好”） | 默认 standard/default，不把“短”自动等同“简单” |

这个取舍明确牺牲“每一轮都选理论最省钱的模型”，换取对话与工具循环稳定；起始信息不足的会话
可能一直用标准档。权重与阈值是待标定的工程参数，不是质量概率。先运行 `observe_only` 校准，
不承诺任意任务都有可靠的难度估计。

复杂度扫描限制字节数/递归深度，跳过 base64、大工具 schema；不能用这个截断扫描结果证明上下文足够。
全量上下文能力筛选须单独估计且有可靠模型元数据；目前 `ModelConfig` 没有 context window 字段，
因此不把这项未经实现的能力作为稳定性保证。

### 2.3 覆盖与观测

- **请求头覆盖**：`X-AMKR-Difficulty: auto | simple | standard | complex`（默认 `auto`）。
  非法值 → 400（与「畸形请求体显式 400」同一取向，不静默忽略）。仅对 `unified-model` 请求生效；
  指定的档位没配 → 按缺档规则回落 `default`。命名与既有 `X-AMKR-Workspace`、`X-AMKR-Fallback` 一致。
- **响应头回显**：`X-AMKR-Difficulty: <tier>` 与 `X-AMKR-Difficulty-Score: <n>`（难度段生效时，
  含 `observe_only` 档）。与既有 `X-AMKR-Fallback` 一样必须在写响应头**之前**设置；形态与 Kong 的
  `X-LLM-Tier` / `X-Prompt-Est-Tokens` 一致。
- **结构化日志**：每次算出档位就记一行（tier、score、命中了哪几个信号＋各自分值）。这是**调阈值的
  唯一数据来源**——没有它，默认值只能靠猜；`observe_only` 的价值也全靠它兑现。
- **不让分类失败阻断可安全处理的请求**：打分无 IO、无额外上游调用；不确定时采用 standard/default，
  缺档也回落 default。但不能笼统保证“新增路由永不失败”：非法覆盖头（400）、绑定目标失效且不能
  安全迁移、有状态续接无法确认 owner 时，必须清晰报错，不能以“可用性”为理由静默丢掉历史。
- **指标库**：**v1 不改 `request_metrics`**。它的建表原文与列序是对外兼容契约（见
  `internal/metrics/schema.go` 里 `request_workspace` / `request_access_key` 两段注释的既定做法）。
  要在看板上按档位统计，正确做法是**新增旁挂表** `request_difficulty(request_id INTEGER PRIMARY KEY,
  tier TEXT NOT NULL, score INTEGER NOT NULL)`，与 `request_workspace` 同构、同一次 `record()` 写入；
  这是可独立评审的第二阶段，不进 v1。

### 2.4 运行时流程

```text
鉴权 → 校验入站路径与请求
  → 如有 previous_response_id：查已记录的 response ID → 实际目标归属，优先保持原生续接
  → 非对话端点：现有端点族计划（不参与难度分档）
  → observe_only：仅观测，保留既有路由
  → 从完整对话计算稳定起点与连续前缀摘要：
       命中最长已知前缀 → 继承它的实际目标
       未命中但保留起点 → 根据相同起始任务确定性选档，不根据最后一句重新选
       起点/关联信息缺失 → standard/default，不用相似度猜会话
  → 检查目标授权与兼容性 → 既有 Key 选择、重试和流式转发
  → 成功后记录本条对话路径的前缀 → 实际目标
```

请求开始时保存实际路由计划快照（含本次 fallback），而不是在失败后重新按最新分数选计划。
`unified-model` 的任务名、端点族与受限凭据规则不变；每档仍复用 `RoutePlan`。

### 2.5 无客户端会话 ID 的稳定策略：自动前缀亲和

**不再要求 `X-AMKR-Session-ID`。** 四面八方的请求来源无需改客户端：利用标准请求已有的
`messages` / `system` / `instructions` / `input` / 上游 response 句柄，建立内容路由亲和。

#### 稳定起点 + 最长已知连续前缀

1. 为路由比较抽取消息角色、内容和必要的工具调用结构；保留消息顺序和真实文本，只规范 JSON
   表示差异，不能为提高命中率擅自删掉语义内容。只做比较，不改写转发给上游的原文。
2. 起点是开场 system/developer/instructions 与第一条完整 user 任务。按它计算初始候选档位，
   不把后续轮数与全文长度加进难度分；普通“继续”不会降档，历史增长不会自动升档。
3. 对连续消息前缀做链式摘要，在进程内路由表中找**最长精确匹配的已知前缀**，从它继承实际目标。
   不能只 hash 整份最新历史再查一张表——追加消息就无法命中上一轮。
4. 成功后把当前路径的摘要与实际目标记入有界 TTL/LRU；需要区分分支时，记录包含已完成助手响应的
   后继前缀（SSE 必须等完整结束，客户端重排/改写响应可能导致失配）。分支故障切换写入本分支的
   后继记录，不覆盖大家共享的短起点；不记录失败/取消/不完整流为一次成功迁移。
5. 表项过期时，若请求仍保留相同起点，可在固定规则/配置下重算相同候选档位。若要真实模型稳定，
   同一档必须对应明确的真实模型；不能在“一个本地别名对应多个不同 upstream_model”的池里任意轮询。
   历史 fallback、账号亲和及配置变更后的完全恢复则不保证。

示例：

```text
请求 A：[user: 排查这段并发死锁]                          → complex / 模型 C
请求 B：[user: 排查这段并发死锁, assistant: …, user: 继续] → 继承 A 的模型 C
请求 C：[user: 排查这段并发死锁, assistant: …, tool: …]   → 继承已有前缀，不因工具结果重分档
```

#### 内容亲和不是身份识别

相同首句可能来自不同真实会话。这里允许它们共享**路由选择**，不是声称它们是同一会话；它们的
请求、上下文与 response 状态必须彼此独立，不能复用另一条对话的输出或上游状态句柄。
相同内容选到同一个模型是允许的；跨用户共享私有对话状态不允许。

- 摘要表按 `(授权身份, workspace, 入站对话方言)` 隔离；命中后仍使用当前授权检查目标。
  只保存摘要/目标标识，不保存 prompt、凭据明文或响应内容。
- 不用 IP、User-Agent、单独的 system prompt、`prompt_cache_key` 推断身份，不用模糊/embedding
  相似度拼接两段会话。相似度只能用于任务分类，不应作为状态继承证据。
- 现有 cacheAffinityKey 的完整历史哈希只服务 Key 亲和，不能原样复用为连续对话关联。
- 前缀摘要必须覆盖被比较的实际内容，不能仅 hash 截断的前 256 KiB 就认定精确命中。打分扫描预算
  与路由摘要预算是两回事；处理上限不足时保守不匹配，不用截断摘要伪造唯一身份。

#### 无法关联的请求

| 请求形态 | 策略与保证 |
| --- | --- |
| 完整历史、起点/前缀未改变 | 自动继承目标，追加消息不会主动导致换模型 |
| response ID 续接 | 网关记录自己发出的 response ID 与准确供应商/账号/模型/协议，后续直接按 owner 路由，无需客户端新增 ID |
| 已截断/压缩历史，起点也消失 | 不承诺能恢复同一会话；未找到精确关联时落 standard/default，不用语义相似度猜回旧状态 |
| 只有“继续”等增量，既无历史也无句柄 | 缺少信息，任何分类器也无法判断它接在哪段对话；固定 default 可避免难度引起的来回切换，但无法重建上下文 |
| 两条会话有相同起点但随后分支 | 允许起点路由一致，优先最长前缀区分后续路径；不能把某分支 fallback 写回共享祖先 |

#### 固定对象、故障切换和生命周期

记录实际目标（本地模型、供应商、upstream_model、必要账号及协议），而不只记录档位或本地别名。
普通完整历史可安全重放时优先在同真实模型的兼容目标间换 Key；必要的跨模型 fallback 成功后
固定到本路径后继，不下轮无条件回探主选。任何已经开始输出的 SSE 不在中途换模型。

带 `previous_response_id` 或其它供应商私有状态的请求必须确认 owner；owner 缺失/失效时明确续接失败，
不能静默迁移、丢掉句柄或 native→chat 降级。上游 response ID 是请求本身已有的状态关联，
不是新增的客户端 session ID；网关未见过的句柄不能靠内容亲和猜归属。

store 属于长生命周期 Handler/App，跨 KeyPool 热重载保留，有界 TTL/LRU；目标删除/停用或授权变化
都须重新验证。并发初始化和写回版本要保护，不在全局锁中发网络请求；分支记录只更新自己的路径。
重启/淘汰会丢失非持久化的故障切换与句柄 owner；不承诺跨重启恢复有状态续接。

v1 不做每轮自动升降档：这就是稳定性取舍。若后来要自动升级，应单独加入保守、只升不降的安全策略，
不能用降档和短期回探把会话变成不同模型间的来回切换。

### 2.6 为什么不把三档并进 `UnifiedPlanNames`

最省事的做法是把 `simple`/`standard`/`complex` 直接追加进 `config.UnifiedPlanNames`——解析、校验、
修复、切换、序列化、WebUI 计划表会「自动」覆盖到。**但它把两个正交的维度塞进了同一个命名空间**：

- `UnifiedPlanNames` 同时驱动**模型的端点计划**（`models.<id>.endpoints.<族>`，见
  `config.Validate()` 的校验循环与 WebUI 的端点计划编辑器）。追加三档等于允许每个模型配
  `endpoints.simple`，而那个字段没有任何运行时含义——「配置写进去了但行为没变」的教科书案例。
- 十处消费点（解析、校验、`/health`、CLI 摘要、WebUI 的 `FAMILIES`、`UnifiedTargets`…）都假定
  计划名 = 端点族，界面会把它渲染成「一族一行 + 该族服务的入站路径」，而难度档位没有入站路径。
- 反而更难写对：`resolveUnifiedPlanLocked` 仍需要对 `routeKind == "default"` 做特判，省下的只是
  configops 里几行遍历。

代价是 `difficulty` 这条支路要自己接上四处既有机制（引用修复、修复、序列化、接口白名单），
这四处**必须一次性做完**，漏一处就是「升级时静默丢配置」或「删模型后整份配置解析失败」。

---

## 3. 改动清单

### 3.1 服务端（核心，约 350–450 行含注释）

| 文件 | 改动 |
| --- | --- |
| `internal/config/model.go` | 新增 `DifficultyConfig{ObserveOnly bool; SimpleMaxScore, ComplexMinScore int; Simple, Standard, Complex *RoutePlan}`；`UnifiedModelConfig` 加 `Difficulty *DifficultyConfig`；`parseUnifiedModel` 读阈值与三档（复用 `parseRoutePlan`，报错文案与分族一致：`unified_model.difficulty.complex.primary 引用了未配置的模型: x`）；`Validate()` 把三档并入 unified 那段现有的计划校验（fallback≠primary、key 必须启用），并校验阈值域、次序与「非 observe_only 时必须至少一档」；`UnifiedPlanNames` **不动** |
| `internal/proxysupport/difficulty.go`（新） | 对稳定起始任务做纯函数复杂度判断；近期任务只作观测；全文大小不累加成难度；扫描/递归有界，未知任务默认 standard；权重通过实际流量标定 |
| `internal/keypool/pool.go` | 保留 `unifiedTiers` 配置解析并提供计划解析；支持已绑定实际 target 的验证与选择，而不只接受 tier；`UnifiedRoute()` 展示配置而不声称能解释单次请求 |
| `internal/proxy/proxy.go` | 接口/Options 接入长期存活的路由亲和表；首次选择、实际 target 选择、成功确认三个边界传递快照 |
| `internal/proxy/handler.go` `context.go` | 记录起点摘要、最长已知前缀、候选档位、实际 target 和路由原因；命中后继承目标，fallback 使用本次快照 |
| `internal/proxy/affinity.go`（新）及 `internal/server/server.go` | 消息连续前缀链式摘要 + 有界 TTL/LRU；作用域隔离、并发初始化/版本保护；分支写回不覆盖共享祖先；跨 KeyPool 热重载保留 |
| `internal/proxy/retry.go`、`internal/protocol/request.go` 及响应处理 | 记录本网关返回的 response ID → owner；状态续接禁止跨 owner 重试/fallback 与 native→chat 适配；完整响应后确认路径，失败/不完整 SSE 不确认迁移 |
| `internal/proxysupport/support.go` | 共享 `UpstreamHeaders` 边界剥除可选 `X-AMKR-Difficulty`，不新增必需的 Session-ID 请求头 |
| `internal/api/validate.go` | `specUnifiedModelUpdate` 加 `nul("difficulty", kindAnyDict)`——漏了它整段替换会被 `extra_forbidden` 挡掉，WebUI 保存直接 422 |
| `internal/api/handlers_meta.go` | 整段替换分支把 `difficulty` 一起写进 `unifiedData`（现在只从 payload 里挑计划名） |
| `internal/api/response.go` | `serializeUnified` 输出 `difficulty`（已配置档位 + 阈值），GET `/api/unified-model` 才能读回 |
| `internal/configops/unified.go` `endpoints.go` | ① `planTargets`/`unifiedTargets` 扩到难度档位：改名与清 key 时必须一起改，否则留下悬空引用、下一次解析直接失败；② `RepairUnifiedModel` 对 difficulty 容器跑 `repairPlanContainer`（档位 primary 失效 → 删该档，回落 default）；③ `SetUnifiedModel` 序列化时带回 `difficulty`（否则 WebUI 每保存一次就丢一次） |
| `cmd/amkr/cli.go` | `--show-unified-model` 摘要加一行（档位 → 模型），沿用现有的「不静默跳过」文案策略 |

### 3.2 WebUI（约 120–180 行）

| 文件 | 改动 |
| --- | --- |
| `webui/pages/endpoint-plans.js` | 新增 `DIFFICULTY_TIERS`（id/label/说明），**不并入 `FAMILIES`**（它没有入站路径与 kind） |
| `webui/pages/routing.js` | unified 编辑器加「按难度改道」区块：总开关 + `observe_only` 复选 + 三档（模型下拉 + Key 下拉）+ 两个阈值输入；只读摘要加一行；提交体只在开关打开时才带 `difficulty`，且「非纯观测却没有档位」由前端拦在保存前（服务端仍会 422 兜底） |
| `webui/api.js` | `updateUnified` 透传 `difficulty`（`UNIFIED_PLAN_KEYS` 不变——它专指计划键） |
| `webui/pages/overview.js` | 统一模型卡片可选显示档位 |

### 3.3 文档与示例

`docs/USAGE.md` 第 8 节、`docs/API.md`（`unified_model` 字段表 + `/api/unified-model` 请求体、
代理响应头表）、`README.md` / `README_EN.md` 的统一模型段落、`router-config.example.json`、
`CHANGELOG.md`。

---

## 4. 兼容性与数据安全

- **纯新增**：不 bump `config_version`（保持 4）。旧二进制读新配置 → 忽略 `difficulty`，行为等于今天
  （`TestUnknownFieldsSurviveRoundTrip` 的口径）；新二进制读旧配置 → 功能关闭，逐字节等价。
  *已实测*：拿一份带 `unified_model.difficulty` 的 v4 配置跑今天的 `config.FromDict`，解析成功、
  该段被忽略、往返序列化原样保留（临时探针，跑完即删）。
- **不新增 `UnifiedPlanNames` 成员** → `/v1/models`、`/health` 的既有键、CLI 摘要的分族行、
  模型的 `endpoints` 语义、`UnifiedTargets`（切换目标的合法目标名）全部不变。
- **访问面不变**：访问密钥与工作空间推理凭据仍然不能用 `unified-model`（403），因此难度路由只对
  完整权限凭据生效——与 unified-model 现状一致，不需要新的授权规则。
- **唯一的数据风险点是四处「引用维护」**：改名、删供应商 key、删模型、WebUI 保存。它们都在 §3.1 里
  逐条点名，且每一处都必须配一条测试（删掉被 `difficulty.complex` 引用的模型后，配置仍能解析）。
- **`request_metrics` 一字不改**（列序即对外契约）；要统计档位就加旁挂表（§2.3）。

---

## 5. 测试与验收

单元/集成（与实现同一次提交）：

1. `proxysupport`：起始任务的分档边界；chat/messages/responses 的锚点提取；历史增长不提高
   任务难度；“继续”不改变初始候选档；工具 schema/base64 不污染分数；扫描/递归有界；未知任务
   用 standard；路由摘要不使用截断内容假装精确匹配。
2. `config`：`difficulty` 解析、别名归一、缺档、阈值默认值与非法值（负、>100、次序颠倒、
   非 `observe_only` 却一档都没配）逐个报错；`SetUnifiedModel` 往返不丢 `difficulty`；未知字段仍被保留。
3. `keypool`：`routeKind != default` 时难度不参与；档位缺失回落 `default`；档位 fallback 生效；
   `observe_only` 下即使配了档位也一律走 `default`。
4. `proxy`（假上游端到端）：不带会话头也能继承连续历史前缀；追加“继续”/tools 不改目标；
   相同起点的独立对话只共享路由、不共享响应/状态；分支 fallback 不污染共享祖先；最长前缀优先；
   授权/workspace/方言隔离；并发初始化和晚到写回；热重载保留；TTL 淘汰后相同起点确定性重选；
   截断/压缩历史不猜测 owner；纯增量用 default；`observe_only` 不改道；response ID 续接正确保持
   owner，未知句柄明确失败而不降级 chat；失败/取消/未完整结束 SSE 不写入成功迁移。
5. `configops`：模型改名 / 删除 / 供应商 key 清理后，`difficulty` 里没有悬空引用；修复后配置仍可解析。
6. `api`：`GET/PUT /api/unified-model` 往返带 `difficulty`；`difficulty` 出现在 GET 响应里。
7. WebUI 探针：`webui/probes/` 下按既有风格补一条（编辑器保存后重新读取、只读摘要显示档位）。
8. **端到端用户模拟**（按本仓测试规范，派子代理以真实用户操作方式跑）：构建二进制 → 临时配置 +
   假上游 → `curl` 三类请求验证实际落到哪个上游模型 → 改阈值/删档位后行为随之变化 → 清理临时文件。

验收口径：默认（不配 `difficulty`）时既有测试与行为不变；启用时，精确前缀关联有效且目标可用的
连续历史沿用实际目标，不随消息追加和分数波动改道。日志记候选档位、目标与继承/回退/切换原因。
锚点被删或改写后的回退本身可能换模型，不能宣称任何来源的会话都绝对不换。
完全没有关联信息时，唯一普适的模型稳定保证是所有这类请求固定同一真实模型，而非动态难度分流。

---

## 6. 风险

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| 初始任务误判 | 会话锁定会放大首选失误；fallback 只处理上游错误，不能修复成功但质量差的回答 | 先跑 `observe_only`；模糊任务默认 standard；保留安全的手动重选 |
| 启发式 ≠ 难度 | 词表与请求形态只是代理信号 | 分开当前任务与历史体量，用实际任务标注校准，不宣称学习型路由 |
| 对话内任务变化 | 起始任务模型不一定最适合后续复杂任务 | 明确这是稳定性换效率；必要时安全显式重选，不默认每轮降档 |
| 前缀变化或关联缺失 | 动态 system、截断、压缩和纯增量都会丢掉关联信息 | 精确匹配失败时保守回退；不声称“无 ID 能识别所有会话” |
| 相同起点的分支 | 两条真实会话可能有完全相同开场 | 只共享路由；最长前缀区分路径，分支写回不污染祖先；状态句柄严格隔离 |
| 上游缓存/状态 | 固定模型不保证缓存命中；Responses 句柄不能跨不共享状态的目标 | 记录准确 target/账号/协议，禁用状态续接的跨目标与 chat 降级 |
| 没有反馈闭环 | 无法验证「升档是否真的避免了错误」，与 RouteLLM 的偏好数据口径不同 | 明确 v1 是「成本可控的启发式」，不是学习型路由；后续要 A/B 才有依据 |
| 扫描成本 | 默认 JSON 体无上限 | 256 KiB 扫描封顶 + 跳过 tools 定义 |
| 引用维护漏一处 | 改名/删除后悬空引用会让整份配置解析失败 | §3.1 逐点落实 + 第 5 节第 5 条测试 |
| 阈值调错 | 用户可能把两档阈值写反或写满 0–100 | 解析期校验并给可读报错 |

## 7. 明确不做

1. **训练/学习型路由器**（无数据、无服务链路）。
2. **Judge 模型分类**（v1 不做；要做得先解决分类请求自身不许走 `unified-model` 的递归、独立的
   模型与 key 配置、超时与失败降级、以及「分类失败时按哪档走」的默认值）。
3. **级联升级**（需要 verifier，收益与成本模型与现有 `fallback` 完全不同）。
4. **每档额外的请求头/查询参数覆盖**（`X-AMKR-Difficulty` 一个头够了）。
5. **`request_difficulty` 旁挂表与看板档位统计**（第二阶段，机制见 §2.3）。
6. **每轮自动升降档 / 必需的客户端会话 ID**：前缀亲和与初始任务选档是 v1 必需项（§2.5），
   不要求客户端改造。完整历史能关联则继承目标；内容变化丢失锚点时不猜测身份。迟滞/冷却状态机
   和模糊会话匹配不做，默认接受“首次判断可能不是后续每一轮最省钱”的稳定性取舍。
7. **把备选触发条件从「上游报错」扩到「校验失败 / `finish_reason=length` / 空响应」**：这是与
   难度路由正交的另一件事（把现有 primary+fallback 变成真正的级联），同样零依赖、性价比很高，
   但**不要混在同一次改动里**——两者的失败语义完全不同，混在一起会让「为什么这次走了备选」
   变成两个原因的叠加。

## 8. 工作量

上一稿仅启发式的约 2 人日估计不能用于当前方案：自动前缀关联、分支写回、实际 target 固定及
有状态 Responses owner 记录都需要真实客户端样本验证，但不要求请求方新增会话 ID。
分三块评审：配置/观测、前缀亲和与续接保护、WebUI/文档。重点验证完整历史、工具循环、动态 system、
截断/压缩和增量 Responses 五类请求；本稿不提供未经验证的工期保证。
