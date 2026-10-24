# LLM 网关按任务难度选模型：业界做法调研

> 目标：为「纯 Go 单二进制、无 ML 运行时、无 GPU、无训练数据、配置驱动」的 OpenAI-compatible 网关筛选可复刻方案，每节统一给出「信号 / 判定 / 代价」。

## 摘要

五个层次，代价递增：**纯启发式打分**（亚毫秒、零依赖）→ **廉价模型当分类器**（+1 次上游调用）→ **embedding 语义路由**（需嵌入模型）→ **训练型路由器**（需训练数据与推理运行时）；**级联升级**是正交维度，不需预判难度但每请求先付一次便宜模型 token。前三层对本项目有意义。

## 1. RouteLLM（LMSYS）

**信号**：只用 query 本身。以 Chatbot Arena 8 万条偏好对战为底，按榜单把模型聚成 10 个 tier，top-2 为「强」、第 3 tier 为「弱」；再用 MMLU 验证集（约 1500 题）golden label 与 GPT-4-as-judge（约 12 万条，采集约 700 美元）增强（[论文](https://arxiv.org/html/2406.18665v4)、[博客](https://www.lmsys.org/blog/2024-07-01-routellm/)）。

**四种路由器**：相似度加权排序（SW，按 embedding 余弦相似度加权 Elo，无需 GPU 但每请求算相似度、单位成本最高）；矩阵分解（MF，学 query×模型的打分函数）；BERT 分类器；causal LLM 分类器（8B 推理判断，泛化最好、开销最大）。

**判定**：**胜率预测 + 代价阈值 α** —— `P(win_s|q) ≥ α → 强，否则弱`。α 越高越省钱，由 `calibrate_threshold` 按「目标强模型调用比例」离线标定：MF 在 50% 调用率下得 **α=0.11593**；**必须用贴近真实流量的数据标定**，否则实际分流比例会偏离（[README](https://github.com/lm-sys/routellm)）。

**数字**：相比全量跑 GPT-4，MT Bench 省 **85%**、MMLU 45%、GSM8K 35%，同时保持 95% 质量；MT Bench 上 MF 仅用 **26% 的 GPT-4 调用**达到 95% 性能（较随机基线便宜 48%），加 judge 增强后降至 **14% 调用**；MMLU 上 causal LLM 路由器最强、需 54% 调用。换成训练中未见的模型对（Claude 3 Opus + Llama 3 8B）**无需重训**仍有效。

**代价**：MF 可纯 CPU；BERT 约 0.3B／约 600MB 显存；causal LLM 8B／约 16GB 显存（[资源对比](https://arxiv.org/html/2603.12646v1)）。路由延迟规则 <1ms、embedding/ML 5–50ms，而 LLM 响应本身 500–2000ms（[汇总](https://gingerlabs.ai/blog/routellm-vs-vllm-semantic-router)）。

## 2. OpenRouter `openrouter/auto`

**新版（2026-08-10）机制**（[博客](https://openrouter.ai/blog/announcements/introducing-the-new-auto-router)、[文档](https://openrouter.ai/docs/guides/routing/routers/auto-router)）：① 「快速、轻量」的分类器把 prompt 分到**约 30 个细粒度任务类型**（code debugging、multi-step agent planning、knowledge Q&A、math 等），传输中完成、不留存 prompt；② 按该任务类型下社区**过去 7 天**的匿名花费份额排序候选，而非静态榜单；③ `cost_tier` 取 `low|medium|high|xhigh|max`，默认 **`low`**；④ **多轮粘性**——同会话记住上次落点并优先复用，只有掉出候选前列才换，以减少重建 input cache 的浪费；分类或排序不可用时回退默认模型集，**路由失败不让请求失败**。

**启发式信号**：新版只披露「约 30 类任务」，**prompt 长度、代码、工具调用等具体细节未找到可靠出处**。

**NotDiamond**：属**旧版**。2025 年 1 月 OpenRouter 公告升级为「路由到 19 个模型、由 Not Diamond 的 meta model 驱动」（[公告](https://openrouter.ai/blog/announcements/happy-new-year-introducing-a-new-auto-router)）；2026 年 8 月新版已换成市场信号（[第三方](https://aireiter.com/blog/openrouter-auto-router-guide)称旧引擎被替换）。**曾经结合，现在不再。**

**可借鉴**：[Custom Classifiers](https://openrouter.ai/docs/guides/features/classifiers.md) 用另一个廉价模型**在请求完成后异步**打标（零额外延迟），`Agent complexity` 预设为 `difficulty_tier`（**7 档**）+ `task_family`（**10 类**）；输入是标签化 transcript（`SYSTEM / TOOLS / USER / ASSISTANT / TOOL_CALL / TOOL_RESULT`），**只发工具名不发完整 schema**，每轮截断 **5000 字符**。**代价**：分类不额外收费，**分类延迟官方未公布**。

## 3. Martian / NotDiamond / Bedrock Intelligent Prompt Routing

**NotDiamond**：**元模型（meta-model）**，需用户提供 prompts、候选回答与评测分数训练自有路由器，另有跨 60+ 模型的预训练路由器；**最少 15 条样本**，单次训练上限 10,000 条或 5MB。调用时用 `tradeoff="cost"|"latency"|省略（默认质量）` 表达偏好，**只返回推荐模型、由调用方自己发起**（[文档](https://docs.notdiamond.ai/docs/quickstart-routing)）。其 code 版每轮前考虑会话状态、消息与 token 数、任务复杂度、隐式开发者反馈、**KV cache 状态**，且**cache 感知**——cache 热时可能刻意留在更贵模型上以免重建（[博客](https://www.notdiamond.ai/blog/not-diamond-code-intelligent-model-routing-for-coding-agents)）。「决策 <60ms」只见于二手页，**未找到官方出处**。

**Martian**：以 **Model Mapping** 把模型转成更易分析的表示，读出成本、速度、准确率、bias 等属性，用户设定判据后实时改道（[报道](https://diginomica.com/martian-model-router-jumpstarts-ai-cost-optimization)）。「最高省 98%」是厂商宣称，学术评测指出这类商业路由器的逻辑**未公开**（[Rerouting LLM Routers](https://arxiv.org/html/2501.01818v1)）。**不可复刻，数字不宜采信。**

**Bedrock IPR**：**同模型家族内**二选一（Claude 3.5 Sonnet vs Haiku、Llama 3.1 70B vs 8B），官方称「预测各模型对该请求的响应质量，再在质量与成本间择优」；GA 后支持任选同家族两模型并自定义判据，但预测模型与判据**未公开**（[AWS 博客](https://aws.amazon.com/blogs/machine-learning/use-amazon-bedrock-intelligent-prompt-routing-for-cost-and-latency-benefits)）。**代价（本节唯一完整的可信数据）**：路由开销约 **85ms（P90）**；内部测试在维持基线准确率下**把 87% 的请求路由到更便宜模型、节省最高 63.6% 成本**（[GA 公告](https://www.linkedin.com/posts/vasi-philomin_amazonbedrock-generativeai-intelligentpromptrouting-activity-7320995122999918592-JYDU)）。这三个指标正是评估任何路由方案应要求的完整口径。

## 4. 语义路由（semantic-router / LiteLLM auto_router）

**信号**：把 query 与每条路由的**预置示例话术（utterances）**编码成向量，比余弦相似度。**判定**：取最高分路由；**无任何路由超过置信阈值则回退默认模型**（`auto_router_default_model`）。LiteLLM 底层就是 `semantic_router` 包（[实操](https://medium.com/@araujomatheusgabriel1/semantic-auto-routing-in-litellm-route-requests-between-models-based-on-intent-using-local-e0b8c1b7a052)）。

**依赖（关键）**：必须有嵌入模型——本地（示例如 Ollama 跑 `nomic-embed-text`）或远程 API（OpenAI／Cohere／HF）。轻量实现里模型仅 **22MB、跑在 CPU 上**，除 NumPy 外只依赖 sentence-transformers，启动时预计算 utterance 向量、每请求只编码一次 query，**不需要向量数据库**（[实现](https://pub.towardsai.net/semantic-router-the-ai-traffic-controller-4a55f7fe7c35)）。

**代价**：常见量级是「路由延迟从 5000ms 级降到约 100ms」（[Deepchecks](https://deepchecks.com/glossary/semantic-router)），但那是相对笨重方案的对比。两个警告：**不要把 distance 当置信百分比**（0.35 ≠ 35%），且**阈值必须按具体嵌入模型用标注样本标定**，换模型通常要重建索引（[Azure 教程](https://learn.microsoft.com/en-us/azure/redis/tutorial-semantic-router)）。

## 5. 级联 / 升级（FrugalGPT、自一致性、verifier escalation）

**信号**：不看难不难，**先答再验**。**FrugalGPT（Stanford，TMLR 2024）**用生成打分函数 `g(q,a) ∈ [0,1]` 给回答打可靠性分，并在验证集上学出「查哪几个模型、什么顺序」的最优序列；便宜模型过关即返回，否则升级（[论文](https://ar5iv.labs.arxiv.org/html/2305.05176)、[拆解](https://portkey.ai/blog/implementing-frugalgpt-smarter-llm-usage-for-lower-costs)）。**数字**：匹配 GPT-4 性能下**最高省 98%**，或同成本下准确率高 4%；但**98% 依赖该领域训练过的质量评估器，未经训练的朴素实现通常只有 60–80% 节省**（[实施指南](https://getnadir.com/blog/frugalgpt-cascade-routing-implementation-tutorial)）。

**「失败」怎么判定**（本节最有复刻价值）：便宜模型输出的 **logprob 置信度**（response token 平均，配阈值如 0.8）；**自一致性**（多次采样比对）；**verifier 探针不一致**（先 1 条廉价探针与锚点比对，一致即接受，不一致才扩到 N 条路径）；结构校验（JSON/schema 解析失败）；拒答或长度截断。AutoMix 默认参数可借用：**自验证置信阈值 0.7、最多升级 2 次、采样 5 条、温度 0.7**（[配置](https://vllm-sr.ai/docs/v0.3/tutorials/algorithm/selection/automix)、[论文](https://arxiv.org/html/2310.12963v5)）。

**代价**：每请求都要先花一次便宜模型 token（含最终会升级的），节省取决于升级率，示例性盈亏平衡点为升级率 **<85%**（[分析](https://www.komos.studio/blog/frugalgpt-llm-cascades)）；**升级路径延迟叠加两次调用**，时延敏感场景应用预判式路由；设计差的级联会掩盖失败、放大 prompt injection（[对比](https://www.resumelens.org/blog/ai/llm-routing-cascades)）。

## 6. 产品侧：固定档位 vs 真难度打分

| 产品 | 实际是什么 | 依据 |
| --- | --- | --- |
| **Cursor Auto** | **真分类器**：Cursor Router 用 60 万+ 真实编码请求训练，读 query、周边上下文、任务复杂度、领域四信号 | [说明](https://www.vibecodingacademy.ai/blog/cursor-auto-mode-router-explained) |
| **Claude Code** | **按角色固定档位**：`opusplan` 在 plan 模式用 Opus 规划、切 Sonnet 执行；Haiku 用于后台功能；子代理默认模型由 `CLAUDE_CODE_SUBAGENT_MODEL` 指定 | [配置](https://code.claude.com/docs/en/model-config) |
| **GitHub Copilot auto** | **按容量/可用性，不按难度**：目标是快响应与降低限流，且**一旦选定整会话都用同一模型**；官方称「按任务复杂度选模型」属**未来迭代** | [VS 博客](https://devblogs.microsoft.com/visualstudio/introducing-copilot-auto-model-selection-preview)、[changelog](https://github.blog/changelog/2026-04-17-github-copilot-cli-now-supports-copilot-auto-model-selection) |
| **GPT-5 / 5.1** | **真路由、信号半公开**：实时路由器依据对话类型、复杂度、工具需求、显式意图（如写 `think hard about this`）在快模型与 thinking 模型间选择 | [发布文](https://openai.com/index/introducing-gpt-5) |

**结论**：Cursor Auto 与 GPT-5 是真按难度/复杂度打分；Copilot auto 与 Claude Code 档位是按任务类型/角色/容量的固定分工——**后者才是零依赖可复刻的形态**。

## 7. 纯启发式的常见具体阈值

| 做法 | 具体数值 | 出处 |
| --- | --- | --- |
| **Kong AI Gateway** | 对 `messages[].content` 的 UTF-8 字节求和，`tokens = ceil(bytes/4)`，**阈值 80**：≥80 → `large`，否则 `small`；回写 `X-Prompt-Est-Tokens`、`X-LLM-Tier` | [文档](https://developer.konghq.com/cookbooks/custom-llm-routing) |
| **Node.js 成本感知路由** | 英文按 **字符数 / 3.5** 估 token（作者注明对非英文与代码密集 prompt 不可靠）；**分档阈值 60** | [教程](https://www.sitepoint.com/cost-aware-llm-router-nodejs-claude-opus-5-5-gpt-6-sol) |
| **vLLM Semantic Router** | 启发式信号（关键词/上下文长度/语言/授权）**亚毫秒**，手段为正则、BM25、字符 n-gram Jaccard、**token 数区间** `[l,u]`；ML 信号（嵌入相似度、领域分类、复杂度估计）需神经推理 **10–120ms** | [论文](https://arxiv.org/html/2603.04444v4)、[分析](https://pub.towardsai.net/the-llm-router-problem-is-not-what-you-think-it-is-42ed061b28c8) |
| **Bifrost 复杂度路由器** | **确定性打分、不需额外模型调用**：精确关键词匹配 + 代码内容 + 推理标记 + 技术术语 + token 数，分 `simple/medium/complex/reasoning` 四档，边界与词表可调 | [博客](https://www.getmaxim.ai/bifrost/blog/llm-cost-optimization-in-bifrost-part-1)、[文档](https://docs.getbifrost.ai/features/governance/complexity-router) |
| **OpenRouter 分类器输入规范** | `difficulty_tier` **7 档** + `task_family` **10 类**；只发工具名不发 schema；每轮截断 **5000 字符** | [文档](https://openrouter.ai/docs/guides/features/classifiers.md) |
| **RouteLLM 阈值** | 不是长度阈值，而是**胜率 α**：按目标强模型调用比例标定，50% 对应 α=0.11593 | [README](https://github.com/lm-sys/routellm) |
| **AutoMix 升级阈值** | 自验证置信 **0.7**、最多升级 **2** 次、采样 **5** 条、温度 **0.7** | [配置](https://vllm-sr.ai/docs/v0.3/tutorials/algorithm/selection/automix) |

**未找到可靠出处**：被广泛引用的「prompt 长度 500／2000 token 分档」这类行业标准数值；「工具数量 ≥ N 升级」「代码块数量」的具体阈值（Bifrost 只把「代码内容」作布尔信号）。上表 80 与 60 均来自具体实现，非行业共识。

## 8. 对无 ML 运行时的本地 Go 网关的可复刻性排序

### A. 零依赖可做（建议落地）

1. **启发式难度打分**：token 估算（`ceil(UTF-8 字节/4)` 或字符数/3.5，都只是估算）、消息条数（多轮程度）、代码围栏次数、`tools` 数组长度与 schema 体积、关键词表（推理/算法/调试）、显式意图词（`think hard`、逐步、证明）加权成分数，按配置档位映射到 `unified-model` 的计划。**代价**：亚毫秒、零上游成本、可解释，与 Kong／Bifrost／vLLM-SR 启发式层同构。
2. **固定档位分工（按任务类型而非难度）**：入站路径（已有 `/v1/embeddings`、`/v1/images/...`）+ 显式 model 别名（`unified-model-fast` / `unified-model-deep`）映射不同计划。**与现有 `unified-model` 设计最贴合，几乎只是配置扩展。**
3. **显式覆盖优先**：请求中的强制强/弱 header 或参数必须短路，并以最高优先级参与打分。
4. **多轮粘性**：记住会话上次档位并优先复用，避免同会话内反复换模型；纯 Go 用内存 map + LRU 即可。
5. **失败兜底不让请求失败**：打分器不可用时回退默认计划，且永不抛错。

### B. 可行但需额外上游调用（需开关与预算）

6. **廉价模型当难度分类器**：额外调用最便宜模型，要求输出结构化难度分（照抄 7 档）或任务类型（30 类可裁到 10 类内）。**代价**：每请求 +1 次上游调用、+几十至几百毫秒、+token 费；参考量级 Bedrock 约 85ms（P90）、ML 类路由 10–120ms。**约束**：硬超时 + 失败回退到 A 组分数；结论不要跨用户缓存；异步化只适用于「打标」而非「改道」。
7. **级联升级**：不需分类器，难点在失败判据，可复刻顺序为上游 **logprob 置信度**（需透传）→ **结构化校验失败** → **自一致性**（采样 2 次，成本翻倍）→ **显式拒答/截断检测**（`finish_reason=length`、空响应）；阈值直接用 AutoMix 的 **0.7 / 最多 2 次**。**代价**：每请求先付便宜模型 token、延迟叠加、须监控升级率（盈亏平衡 <85%）。**更务实的替代**：只做「**校验失败才走备选模型**」——与网关**已有的「主选 + 备选」机制天然吻合**，只需把触发条件从「上游报错」扩展到「校验失败/截断/空响应」。
8. **embedding 语义路由**：仅在愿意引入嵌入提供方时做。要么增加一个 embedding 端点调用（等同「多一次上游调用」，但额外需要向量预计算与阈值标定），要么在二进制内嵌小型嵌入模型（**打破「无 ML 运行时」前提**）。**优先级低于第 6、7 条。**

### C. 不可行

9. **训练型路由器（RouteLLM 的 MF／SW／BERT／causal LLM）**：需训练数据（8 万偏好对战 + 12 万 judge 数据，约 700 美元）、训练流程，BERT 约 600MB 显存、causal LLM 约 16GB。**在现有约束下不可复刻。** 唯一可借用的是**阈值标定方法论**：用几百条标注样本离线标定 A 组分档边界（vLLM-SR 用 805 条把路由准确率从 80.39% 提到 98.53%），不需要 ML 运行时。
10. **Martian 的 Model Mapping / NotDiamond 元模型**：逻辑与数据均不公开，无法复刻；其「98% 节省」「60ms 决策」等数字**未找到可核验的官方出处**，不应作为设计依据。
11. **依赖厂商侧路由**（Bedrock IPR / Azure Model Router / OpenRouter auto）：对自建网关是「把问题外包」，非可复刻机制；可借用的仅是产品形态——**同家族二选一 + 成本档位 + 显式回退**。

### 一句话建议

**先做 A 组（启发式打分 + 固定档位 + 多轮粘性），把级联降级为「校验失败才走备选」，把 LLM-as-judge 做成可配置的可选层，明确放弃训练型路由器；所有阈值走配置，用一小批真实流量离线标定边界，而不是照抄任何厂商公布的数值。**
