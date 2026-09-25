# Changelog

## [Unreleased]

### 修复

- **Antigravity 的额度要问 `daily-cloudcode-pa`，问 prod 会永远显示满额。** 账号资源看板
  给 Antigravity 配的那条 `quota_probe` 原先指向 `cloudcode-pa.googleapis.com`（Code Assist
  的生产端点）。实测发现它与 IDE 实际连的 `daily-cloudcode-pa` **不是同一份额度**：同一个
  账号、同一时刻，prod 回 Gemini weekly/5h 各剩 `1.000`（重置窗口也不同），daily 回
  `0.679` / `0.865`。于是出现了自相矛盾的读数——该凭据累计 861 次请求、97.1M prompt tokens，
  看板上却是满额，用户据此会以为额度没被消耗。`quota_probe` 只支持单个 URL、没有回退，
  所以配方固定写 daily，并把三个后端的实测对照写进 `docs/SUBSCRIPTION-QUOTA.md`。

## [6.2.0] - 2026-09-25

### 新增

- **版本检查与自更新在直连不可达时回退镜像。** `--check-update` / `--update` 原先只认
  `github.com` 与 `api.github.com`，在两者都被阻断的网络里，更新只会以一句
  `dial tcp …: connectex: …` 结束——域名被阻断是网络策略而不是抖动，重试再多次也没用。
  现在两条链路都按「直连优先、失败后依次回退镜像前缀」取同一份发布物：内置 `gh.llkk.cc` /
  `ghfast.top` / `gh-proxy.com` / `ghproxy.net` 四个前缀（顺序按覆盖面排，只有 `gh.llkk.cc`
  代理 `api.github.com`），可用 `AMKR_GITHUB_MIRROR` 三态控制（不设=内置表、设为空=只用直连、
  设为非空=替换内置表）。产物与校验和各自独立回退，可能来自不同前缀——安装判定不变：两者
  仍必须是同一个 Release 的内容，SHA-256 对不上就拒绝安装。回退发生后错误提示会说明「已试过
  N 个镜像」，不再只剩一句 dial timeout。
- **账号资源看板：额度按模型组分区，并补上逐模型额度、最近请求与订阅档位。** CPA 的归一化
  额度本来就带着「模型组」这一维（Antigravity 的 Gemini 与 Claude/GPT 各有**一套** 5 小时 +
  周期额度，窗口名一模一样），`auth-files` 也一直随响应带回 `model_quotas`、
  `recent_requests`、`project_id`、`account_type`——此前这些要么被拍平、要么被整块丢掉，
  于是看板上四条窗口看起来是重名的重复项，也看不出哪个模型先吃紧。现在窗口带
  `group` / `window` / `description`：分组做小标题，标签用窗口名归一后的话术（`5h` →
  「5 小时」、`weekly` → 「7 天」），上游那句说明（"You have used some of your weekly limit,
  it will fully refresh in 5 days, 9 hours."）单独呈现而不再顶替窗口名；账号行补 `plan` /
  `tier_id` 与账号类型、项目 ID；新增 `summary[]`（余额、积分这类**不成窗口**的数值项，值与
  单位一起显示）与逐模型额度折叠区——现场探测只要成功就采纳整份结果，只回数值项、不回窗口的
  计费类插件也算数；重置倒计时按 CPA 报来的 `serverTimeOffsetMs` 校正。
- **额度改用圆环 + 百分比。** 账号级四个窗口原先各占一条满宽横条，两个账号就撑满一屏；现在
  每个窗口一个圆环、百分比写在环里、环下是窗口名与极短的「↻ 剩余时间到重置」，同一组的窗口
  排成一行。百分比取整改用 `floor`（并加 `1e-9` 台阶避开 `0.29*100 = 28.999…` 这类浮点
  误差）：`round` 会把「只剩 99.6%」显示成 100%，等于替上游保证一个它没说的满额。
- **概览请求流独占整行，并补上 Token、来源与结果。** 原先 5 列小字把调用方、上游 Key、
  供应商、状态码挤在一行，Token 只给一个合计，**来源地址根本没显示**——排障时看不出「谁从
  哪台机器发过来的」。现在独占一整行、扩到 7 列（状态条 / 时间 / 模型路由 / 来源 / Token /
  结果 / 成本）：来源给出调用方档位、工作空间与来源 IP（悬停补完整 `RemoteAddr`、
  `User-Agent` 与请求 ID），Token 给出输入/输出与缓存合计，结果给出耗时与首字。来源在请求
  处理时记入新的 `request_source` 旁挂表（取 `RemoteAddr` 而不读可被伪造的
  `X-Forwarded-For`，与访问日志同源），`/metrics/requests` 的 items 末尾新增 workspace /
  client_addr / user_agent 三项。用旁挂表而不是给 `request_metrics` 加列，是为了不动建表
  原文——旧二进制还要继续读写同一个库。
- **用量统计页按「哪把 Key 出去的」拆开。** 原先这一页只有 `模型 / Key 名` 一行名字，它答不出
  两件事：一把 Key 一共出去了多少流量（量被拆在多个模型行里，要自己加总），以及这是**哪一家**
  的 Key（`key_name` 只在同一个供应商内唯一，`/metrics` 的 `keys` 会把两家的同名 Key 并成一行）。
  访问密钥的流量更是完全没有拆分，只有 `caller_type=access_key` 一档。现在页面各有四张表：

  - **按上游 Key 用量**：按 `(供应商, Key 名)`，即转发时实际用的那把上游 Key；
  - **按访问密钥用量**：按访问密钥，行名给出配置里的显示名 + `key_id`（显示名可重名，id 才是
    稳定标识符）；
  - **模型 / 上游 Key 用量**：原先那张表改用三段行名 `模型 / 供应商 / Key`，同名 Key 不再混行；
  - 「上游分布」卡补第三段**按上游 Key**（按请求数排行）。

  两张按 Key 的表都带一行脚注说明"合计为什么可能不等于页面顶部的总量"：没有供应商归因的请求
  （升级前的历史行）不在表内，只有访问密钥发起的调用才进访问密钥那一维。

  - **读数走新的 `GET /ui/key-usage.json`，而不是给 `/metrics` 加字段。** `/metrics` 是**对照
    参照实现**的读数，形状已经发布、不能加字段；「按供应商 + Key 名」这个组合分组也不是参照
    实现的口径。端点只认完整权限（内容会暴露全实例的 Key 使用情况），参数与 `/metrics`、
    `workspace-usage` 同一套（`hours` + `all_history`，且参数校验先于鉴权）。三份拆分与未归属
    汇总**一次最细粒度扫描 + Go 侧上卷**算出：逐个维度各扫一遍窗口在「全部历史」档上要付三次
    全表扫描。
  - **流向图补上「上游 Key」这一层**（五层 → 六层：工作空间 → 请求模型 → 实际模型 → 供应商 →
    上游 Key → 上游模型）。同一家供应商可以配多把 Key，而 v4 起「模型 → target」的选择就是
    选 Key；只画到供应商时，两把 Key 承担的流量在同一段流带里并成一条。数据早已在指标里
    （`key_name` 恒非空），因此与其余五层一样直接取列，不需要额外埋点。工作空间页、嵌入面板与
    `/ui/workspace-usage.json` 同时生效。

### 修复

- **统一模型页保存一次之后就再也存不进去。** 成功保存后没有复位 `state.saving`，而它同时
  控制整张表单的 disabled 与按钮文案——再点「编辑」拿到的是一整个禁用、按钮写着「正在保存」
  的表单；失败原因又写进了重画前那个已脱离文档的节点，页面上不留任何痕迹。现在成功分支同样
  复位，失败原因改由 `state.saveError` 承载并画进当前这棵 DOM；同时每次进入页面都重新取数
  （别的页面会改掉模型与 `config_revision`，缓存下来的旧值既选不中、又会撞 409），unified
  指向已删除的模型时回落成可用模型并丢掉挂在它上面的固定 Key。
- **新增供应商 Key 后页面不刷新。** 新建流程只给编辑器状态写了 `{provider, key}` 两个字段，
  而重画要读 `editor.models` / `editor.selected`，缺字段直接抛 TypeError——`draw()` 抛错的
  后果是**整页一个字都不更新**：Key 早已写进服务端（探测跑完、提示条也弹了），界面却还停在
  加之前的样子，只能手动刷新浏览器。现在编辑器状态的字段一次给全，建状态的入口也只留一个。
- **面板的「上游模型用量」卡不再把 Key 名当成上游模型名。** 那张卡原先写死取第 3→4 段连边
  （供应商 → 上游模型），而流向图插入「上游 Key」层之后那段变成了「供应商 → Key」，卡片会
  安静地列出一串 Key 名。现在改为**按层名定位**（`layerIndexOf(usage, "upstream_model_id") - 1`），
  以后再加层也不会静默错位；`webui_panel_probe.mjs` 新增断言直接在那张卡的节点里找上游模型名，
  把旧写法改回去实测会让它失败（任务用量的取段同样改成按层名定位）。

### 文档

- `docs/CLI.md` 说明自更新的镜像回退范围与 `AMKR_GITHUB_MIRROR` 的三态、`HTTPS_PROXY` 这条
  替代路径，以及「校验和也走镜像时只保完整性、不保来源」这条取舍；`README.md` 补一句直连
  优先的回退行为。
- `docs/SUBSCRIPTION-QUOTA.md` 更正 Antigravity 的额度口径（`loadCodeAssist` 并非「只有套餐、
  没有额度数字」——CPA 正是从 `paidTier.availableCredits` 读 Google One AI 积分，只是压成
  布尔用于 credits fallback），写明 CPA 的被动采集白名单只有 claude/codex（main 加 devin）、
  Antigravity 必须靠对端的额度插件或**声明式 `quota_probe`**，并附上实测可用的 probe 配方
  （URL、body、`$TOKEN$` 注入、User-Agent，以及为何不需要 `mapping`）；`docs/USAGE.md` 的
  账号资源一节同步补上这些读数与 501 的新文案。
- `docs/API.md` 补上 `/api/cpa-accounts` 的字段清单（分组、说明、订阅档位、`summary` 数值项、
  逐模型额度、最近请求、时钟偏移）。
- `docs/API.md` 新增 `GET /ui/key-usage.json` 一节（含三份拆分的口径与"为什么不能并进
  `/metrics`"），并说明「挂在 `/api/` 还是 `/ui/`」的判据里多了一条判例；`/metrics` 一节补上
  指向该端点的说明。
- `docs/API.md`、`docs/WORKSPACE.md`、`docs/USAGE.md` 与 `README.md` 的流向图层数由五层改为
  六层，并写明「按层名取数、不要写死下标」这条陷阱（`docs/WORKSPACE.md` 第 8 节、`README.md`
  的功能清单与 WebUI 页面清单）。

### 工程

- 新增 `webui/probes/webui_unified_probe.mjs`：用最小 DOM 垫片 + 假 fetch 驱动真实页面模块，
  锁住四条都不会报错的静默故障（保存后 `saving` 未复位、失败原因画在旧节点、进页面不重新
  取数、unified 指向已删模型），并接入 CI 与发布门禁的探针清单——那份清单自己写着「漏掉一个
  等于发布门禁比 CI 松」。
- 供应商页探针扩到 56 项断言，锁住「新增 Key 后整页自动刷新」；账号资源探针补上圆环与额度
  细节的断言（每窗口一环、环角度与百分比同源、告警配色分界、上游说明只在悬停提示里）。

## [6.1.0] - 2026-09-25

### 重大变更

- **隐藏别名下线：可调用名就是模型 ID 与 `aliases`，上游模型名只作上游名。** 原先每个 target
  的 `upstream_model` 会自动变成"能调用但不出现在 `/v1/models`"的隐藏别名，模型还能再写一份
  `hidden_aliases`。这套设计把"上游叫什么"和"本地能调什么"搅在一起：同一个模型在各上游的不同
  叫法会凭空多出一批可调用名（配置与 `/v1/models` 对不上），也没法把某个上游叫法指到另一条
  路由上。现在只剩两件事——**对外名称**（模型 ID + `aliases`，都列在 `/v1/models`）与
  **上游名称**（target 的 `upstream_model`，只在转发前替换 `model`，不可调用）：

  - 同一个模型在各上游叫法不同时，把那些 Key 都收进同一条路由的 `targets` 即可：外部只看到
    一个名字，轮询到哪个 Key 就用它自己的上游名。WebUI 模型路由页因此重做：每条目标的上游名
    可就地编辑，「添加目标」按供应商 / Key + 上游名挑选（候选来自该 Key 的探测结果，不再要求
    上游名等于路由 ID），并支持把一条目标整体「移到其它路由」。
  - **管理 API 有破坏性变化**：`/api/models`、`/api/routes` 的请求体不再接受 `hidden_aliases`
    ——带上它返回 `422 extra_forbidden`，而不是静默忽略（静默忽略会让一份还写着该字段的请求
    看起来生效了，而那些名字其实已经调不通）；模型响应不再返回 `hidden_aliases` 与
    `auto_hidden_aliases`。配置文件里的 `hidden_aliases` 不报错：`parseModels` 忽略未知键，
    需要保留的名字请挪进 `aliases`。
  - TUI 的模型设置菜单去掉「隐藏别名」项，CLI 配置总览去掉该列。
  - 顺带把"路由下没有 target 就不该存在"这条不变式收到服务端统一保证：`PUT /api/routes/{id}`
    传 `targets: []` 现在直接删除该路由并返回 `204`（原先要调用方自己先清空再 `DELETE`），
    按索引删除最后一条目标同样连路由一起删，两条路径都会修复 `unified_model` 与任务引用。
    这样"整理目标的归属"与"清理空路由"是同一个动作。

  回归由新增的 `internal/api/routes_api_test.go`、`internal/configops/modelroutes_test.go` 与
  重写后的 `webui/probes/webui_routing_probe.mjs`（55 项断言）守着，锁的是：上游名与路由名
  解耦、请求体不再含 `hidden_aliases`、清空目标即删路由并修复引用，以及迁移目标时"先追加后
  移除"的写入顺序。

### 新增

- **账号资源看板：一处管理多个 CPA 实例的账号与额度。** AMKR 只知道自己上游 Key 的成功与
  失败，不知道那些账号还剩多少额度。新增的「账号资源」页（监控分组）可以把任意多个
  [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 实例汇总到一块看板上：逐账号给
  供应商、状态（可用 / 冷却中 / 已停用）、成功与失败次数，以及**每个额度窗口的剩余比例**与
  重置倒计时；顶部四张瓦片给实例数、账号数、可用账号数与额度告警数。

  - **取数由服务端转一手。** CPA 的管理接口与 AMKR 不同源、也没有 CORS 头，浏览器直接请求
    必被拦；而 `management_key` 是能改 CPA 配置的凭据，不该长期放进浏览器。新增
    `GET /api/cpa-instances`、`PUT /api/cpa-instances`、`GET /api/cpa-accounts` 三条路由
    （列在 `workspacePatterns()` 里，不动那 47 条已发布契约），实例清单落在新的顶层配置键
    `cpa_instances` 上——`config_version` 仍是 4，旧版本读写这份配置不会丢字段。
  - **额度优先取现场值，退回到被动快照。** `passive` 是 CPA 从上游响应头抄下来的
    （Anthropic 的 `anthropic-ratelimit-unified-*`、Codex 的 `x-codex-*`），不额外发请求；
    `quota` 是 CPA 装了额度提供者时的 `quota/fetch` 归一化结果，同一账号两者都有时以现场值为
    准——它们算的是同一件事，而现场值更新，混起来会出现一半新一半旧的进度条。因此**不是每个
    provider 都有额度可读**：被动采集只覆盖 Claude 与 Codex，其余会显示「对端未配置额度查询」
    （对端回的 `501`）或「上游未提供额度信号」。
  - **这一页不轮询**，也不缓存读数：一次刷新要替每个实例问一遍账号、有额度插件的还要逐账号问
    额度，数据只在进入页面与点「刷新」时取，看板上的数字与 CPA 里的一致。扇出有 60 秒总预算、
    并发上限 4，且**失败是逐实例的**：某个实例连不上、密钥不对或条目残缺，只让它那块卡片报错。
  - **只看不拦**：这些读数不参与 AMKR 的 Key 选择、冷却与失败切换。AMKR 路由的是它自己的上游
    Key，与 CPA 侧的账号额度没有耦合关系；额度耗尽该由 CPA 自己的冷却机制处理。

  回归由 `webui/probes/webui_accounts_probe.mjs` 守着（已接进 CI），锁的是三件坏了很难发现的
  事：请求只落在 AMKR 自己的 `/api/` 下、进页面不把管理密钥拉下来（只在打开实例编辑框时才取）、
  一次刷新只发一次请求，以及进度条画的是**剩余**比例（20% / 5% 两档颜色）——画成已用会把 18%
  剩余读成「还早」，整页结论反过来。后端由 `internal/api/cpa_test.go` 与
  `internal/configops/cpa_test.go` 覆盖（假 CPA 服务、密钥校验、整体替换的原子性、逐实例故障
  隔离）。

- **访问密钥的权限清单改为直接勾选。** 访问密钥页的「允许的供应商」与「允许的模型」不再是逗号
  分隔的文本框，而是从当前配置里已有的名字中勾选：供应商取自 `/api/providers`，模型取自
  `/api/models`（真实 ID 与别名各占一项，别名带「别名 · 模型」提示），候选超过 8 项时可关键字
  筛选。手写清单只有坏处：服务端按调用方写的**原始名字逐字**比对，拼错一个字符就等于某把已经
  发出去的 key 静默少一项权限（调用方只看到 `403`），而服务端写盘时又会逐个校验存在性，手写能
  带来的只有 `422`。

  - **三态显式化**：`[]`（一个都不许）与"没有这份清单"（不限制）是两个相反的授权，而空的勾选
    状态同时长得像这两者——把"全部取消勾选"这个明显的收紧动作解读成放开一切，是这类界面里最
    危险的一种默认。因此新增了显式的「不限制（允许全部）」开关，新建对话框与编辑行共用。
  - 清单里若留有当前配置已找不到的名字（模型被改名或删除），并进候选并用黄色卡片标出，不会在
    用户没看见的情况下被丢掉。
  - 由 `webui/probes/webui_accesskeys_probe.mjs`（29 项断言，已接进 CI 与发布门禁）守着。

- **结构化决策端点 `/v1/decide` 与 `/v1/classify` 可直接接入**（社区贡献，PR #1，作者
  @LiveSirius）。Laya / Jev 这类决策模型的规范调用**不带 `model`**（用哪个模型由上游按 Key
  决定），而 AMKR 此前对所有 `/v1/*` 一视同仁地要求 `model` 字段，这类端点无法直接接入：

  - 请求体里带了可用的 `model` → 按它路由，与其它端点一致；没有（或为 `null` / 空串）→ 按名为
    **`laya`** 的路由路由。因此"哪些 Key 服务 /v1/decide"的表达方式就是**在模型设置里建一条叫
    `laya` 的路由、把这些供应商 Key 绑上去**。
  - 请求体逐字节原样转发到同名的上游路径（`v1/decide`、`v1/classify`）：这类体不是对话体，
    AMKR 不改写它，也不会给上游补一个它没写过的 `model` 字段。
  - 配置里没有 `laya` 路由时是 `404`「模型 laya 未配置；请先在 AMKR 的模型设置中配置该模型」，
    而不是 `400`「请求体中缺少 model 字段」——后者会让调用方以为自己的请求写错了。
  - 这是**与参照实现的有意分叉**（Python 版在这两个端点上必然 400），由
    `TestResolveModelIDDefaultsDecisionEndpoints` 与 `internal/proxy/decide_test.go` 锁定。

### 文档

- 新增 `docs/SUBSCRIPTION-QUOTA.md`：订阅制账号额度查询调研，含 CPA 对外额度接口的源码级细节。
  它是「账号资源看板」里额度读数到底从哪来的依据。

## [6.0.1] - 2026-09-24

### 新增

- **管理面登录改为独立页面 `/ui/login.html`。** 原先未授权时 `index.html` 会就地画一张
  登录卡，于是同一个地址既是主界面又是登录框：深链、前进后退与「会话中途失效」三条路径
  都得在这一页里各自证明「没有绕过鉴权的入口」。现在拆成两个页面，各自只有一件事：
  `index.html` 只画主界面，发现没有可用凭据就整页 `location.replace` 到登录页；`login.html`
  只收凭据，验通了再回到原本要去的地址。

  - 跳转用 `replace` 而非 `href`：登录页不该留在历史记录里，否则登录成功后按「后退」会
    退回一个已经有凭据的表单，很容易被读成「又掉线了」。
  - 深链不丢：当前地址作为 `?next=` 带过去，登录成功后回到原页（未授权时 `index.html`
    一个节点都不渲染，因此不存在「带着无效 Key 进入主界面」的入口）。
  - 三种提示分开：`?reason=expired`（会话中途失效，如重置了本地鉴权 Key）、Key 无效、
    服务连不上。最后一种**保留**已填内容并给出「重试连接」——那可能只是服务还没起来。
  - Key 只写 `localStorage`（`amkr.apiKey`），**绝不进 URL**：登录页地址会落进历史记录、
    Referer 与截图。
  - `?next=` 只接受同源相对路径，拒绝 `//host` 与 `/\host` 这类协议相对地址，否则
    `…/login.html?next=//evil.example` 就是一个开放重定向。校验前先按浏览器的方式归一化
    （剥掉制表符与换行）再判断，顺序不能反：浏览器在解析 URL 前就会剥掉它们，于是
    `"/\t/evil.example"` 在它眼里正是 `"//evil.example"`，而直接校验原始串会看到第二个
    字符是 `\t` 而非 `/`、看着人畜无害，交给 `location.replace` 却会跳到站外。
  - 服务端未启用本地鉴权时登录页直接放行（`/health` 的 `local_auth_enabled` 为假），
    不会卡在一个永远填不对的表单上。

  回归由 `webui/probes/webui_auth_probe.mjs` 守着：原先的登录场景按「主界面就地画表单」
  写，现改为「主界面必须跳转、只跳一次、且跳转目标在磁盘上真的存在」+ 9 个登录页自身场景
  （收凭据回到 `next`、Key 打错不落盘、三种站外 `next` 变体、失效说明、连不上给重试、
  轮询不重建输入框、未启用鉴权直接放行）。

### 修复

- **自更新不再「一次网络抖动就失败」**（`internal/selfupdate`）。原先下载产物与取校验和
  各只有一次 `client.Get`，没有任何重试：实测本机 `amkr --update` 直接死在
  `获取校验和失败: … net/http: TLS handshake timeout`，而重跑一次往往就好了。现在连接层
  失败与 5xx/429 会重试（4 次尝试、500ms 起倍增），且**只重试值得重试的**——404/403
  （产物名与 release 工作流漂移、tag 未发布）立刻返回，不空等。

  重试的边界补完整后覆盖到传输中途：十几兆的产物下到一半被断是最典型的瞬时故障，
  而它此前会被当成「本地错误」直接放弃；本地写失败（磁盘满、目录不可写）仍不重试。
  重试前会先排空错误响应体（限 4KB）再关闭，让 keep-alive 连接能被下一次尝试复用——
  不读就走会让 Go 丢弃连接，于是每次重试都要重做一次 TLS 握手，而握手超时恰恰是这次
  要修的故障，那等于把最贵的部分重做一遍。

- **自更新不再对停不掉的高权限实例空口承诺自动重启**（`internal/service`）。本机实测的
  失败链：PID 文件里写着 3756，但那个进程属于会话 0（SYSTEM 计划任务接管了原先的后台
  服务），`taskkill` 一律 `Access is denied`。旧代码因而把「进程还在」当成了「我杀得掉」，
  连出三个症状：报「能自动重启」却做不到；停不掉时跑满 20 次轮询后谎称「已发送停止信号」；
  `.old` 因映像仍被映射而删不掉。

  现在用 `OpenProcess(PROCESS_TERMINATE)`（Windows）与 `kill(pid, 0)`（POSIX，`EPERM`
  即不可终止）真实探测可终止性，不可终止时直接说明权限不足并给出 `amkr --service stop`。

- **可重启判据收紧：残留 PID 文件不再掩盖仍在服务的实例**。上一条只处理了「进程在跑但
  杀不掉」，还漏了一种组合：PID 文件是**残留**（进程早已退出）而实例正由查不到的 SYSTEM
  计划任务在服务。此时注册信息查不到、PID 文件里的进程又已不存在，两条都以为「没人在跑」，
  于是报「能自动重启」——助手去停一个不存在的进程、以为停完了，然后把新版本拉起来，而端口
  还被那个 SYSTEM 任务占着，启动必然失败。这正是「更新完服务没起来」的另一半原因。
  PID 文件只说明「曾经以后台形态启动过」，与「现在谁在服务」是两件事；`/health` 探针是
  非提权视角下唯一能看见 SYSTEM 实例的证据。

- **访问密钥页不再整页显示 `[object Promise]`**（WebUI）。该页的 `renderAccessKeys` 是本
  仓库唯一把 `page.render` 写成 `async` 的页面，而 `app.js` 直接 `mount(page.render(ctx))`
  的返回值、`dom.js` 的 `append` 遇到非 Node 会 `String()` 成文本节点，于是整个内容区只剩
  一行 `[object Promise]`。改为同步函数、取数在后台完成后自行重绘。

  `scripts/webui_module_check.mjs` 同时加上这条契约的守卫：遍历 `app.js` 导出的 `PAGES`，
  凡 `render` 是 async 函数的页面即报错退出非 0。ES 模块加载检查抓不到这类错误——它只证明
  模块能被求值，不证明页面的渲染契约成立。

## [6.0.0] - 2026-09-24

### 重大变更

- **访客模式整体移除，改为「访问密钥」资源。** 原先固定的共享凭据 `amkr-visitor`（权限由
  每个上游 key 上的 `allow_visitor` 开关拼出来）连同它的一整套模型级规则一起删除：
  `amkr-` 模型名前缀、`visitor` 权限档、`ModeVisitor` / `VisitorModelPrefix`、`visitor_test.go`
  全部不再存在。`amkr-visitor` 现在与任何错误凭据一样被 `401` 拒绝。

  原因是固定访客 key 的两处硬伤：**所有人共用一把**，且权限只能靠逐个改上游 key 的开关来
  拼——既做不到一人一把，也做不到按人收窄，更回答不了「这把 key 是谁的、它本该能调什么」。

  取代它的是配置顶层的 `access_keys`：形状 `{"<key_id>": {"name"?, "key", "enabled"?,
  "providers"?, "models"?}}`，用对象而不是数组（`key_id` 是更新与轮换时的稳定定位符，数组
  下标不是身份）。生成的 key 前缀是 `amkr_ak_` + 43 位 base64url（32 字节随机数，共 50
  字符），与既有的 `amkr_`、`amkr_ws_`、`amkr_ik_` 并列。

  `providers`（供应商 ID 清单）与 `models`（模型名清单，真实 ID 或别名）都是**三态**：
  **省略 = 不限制**，**`[]` = 一个都不许**，有内容 = 只许这些。「省略」与「空数组」是两种
  不同的授权状态，配置、管理 API 与 WebUI 三处都保住这个区别（用 `nil` 判定而不是
  `len(...) == 0`，后者会把运维写下的禁令当成放开）。`models` 按调用方**写的原始名字**比对，
  发生在**别名解析之前**——清单里因此可以写别名，而只要限制了模型，同一个模型换个写法也绕过
  不去。清单里每个名字都会在写盘时逐个校验存在性，写错一个直接 `422`，而不是让某把已经分发
  出去的 key 静默少一项权限。

  访问密钥的权限边界：**不能用 `unified-model`**（全局计划不属于任何清单能收窄的范畴）；
  **不能用任务名**（任务自己固定的模型可能不在清单里，绕过去等于清单失效）；拿不到 `/metrics`
  与 `/api/*` 管理接口。它**不绑定工作空间**，跟着 `X-AMKR-Workspace` 头走，与本地主凭据一致。
  被停用的 key 回 `403`（`访问密钥已被停用: <name>`）而不是 `401`——停用是可恢复的已知身份，
  错误凭据不是，两者的排查方向完全不同。

  解析落在 `internal/proxy/handler.go` 的 `authorize` 与 app 面（`/v1/models`），顺序是
  **完整权限 → 访问密钥 → 工作空间推理 key**；本地主凭据必须**先**判，否则一把受限 key 可能
  被当成管理员凭据用。与工作空间推理 key 一样刻意不进 `internal/auth`——那包是不依赖 `config`
  的纯函数，而这两条通道要读配置清单。

  凭据唯一性也随之扩成**跨类型统一检查**：`local_api_key`、`workspaces.*.api_key`、
  `workspaces.*.inference_key` 与 `access_keys.*.key` 共用一张占用表，任意两处相同都是错误
  （写盘时 `422`，加载时是配置错误），而不是「取第一个」——撞车会让同一把 key 的权限取决于先
  命中哪张清单。

  验证：`internal/config/model_test.go`（`TestAccessKeyParseErrors` 等，含三态与引用校验）、
  `internal/api/accesskeys_test.go`（明文出现时机、三态、404/422、撞车）、
  `internal/server/handlers_test.go` 与 `internal/proxy` 的访问密钥收窄用例。

- **`/health` 删除 `visitor_feature_installed`、`visitor_access_enabled`、`visitor_key_count`
  三个字段**（破坏性变更）。固定访客 key 不再存在；访问密钥的数量与管理走 `/api/access-keys`，
  不属于无鉴权的存活探针该报的内容。按字段名取值的老调用方（CLI、运维脚本、agent 配置工具）
  需要同步去掉对这三个字段的处理。

- **`caller_type` 的 `visitor` 档改成 `access_key`。** 指标库里请求来源现在只有三档：
  `local`（主凭据）、`workspace`（工作空间推理凭据）、`access_key`（访问密钥）。
  `internal/metrics/store.go` 的默认分组键、`schema.go` 回填 UPDATE 的合法值表与
  `server/query.go` 的字面量过滤三处同步改齐；老库里残留的 `visitor` 行会在下一次缺列升级时
  被收敛到 `local`（那些流量来自一把已不存在的凭据，没有更强的归属可还原）。
  `/metrics/requests`、`/metrics/series` 的 `caller_type` 过滤器与 WebUI 的标签查表一并更新。

- **`allow_visitor` 不再被读取，也不再出现在管理 API 的请求体与响应里。**
  `ProviderKeyCreate` / `KeyCreate` / `KeyUpdate` 请求体不再声明该字段（APIModel 是
  `extra="forbid"`，继续传会得到 `422 extra_forbidden`），`KeyResponse` /
  `ProviderResponse` / `ModelResponse` 也不再回显它，模型响应里由它派生的
  `visitor_available` 同样消失（响应体因此变短：以字段名取值的老调用方需同步）。配置导出
  原先的 `include_visitor` 参数随之取消——导出的形状从此与配置无关。已在配置文件里写下的
  `allow_visitor` **不会导致加载失败**（未知字段照旧原样保留、原样写回），只是从此不再有
  任何效果：受限凭据的授权改由访问密钥自己的两份清单表达。

- **全部冻结的差分语料与回放测试退役。** 迁移期仓库里有 23 份由 Python 参照实现产出、
  逐字节冻结的语料（`internal/*/testdata/*.jsonl`、`*_corpus.json`）与对应的回放测试
  （`*_corpus_test.go`），加上生成脚本 `gen_*_corpus.py`。语言迁移已经完成，这些语料不再
  承担「迁移正确性凭证」的角色，其中一部分还**阻碍了正常演进**：例如 `request_metrics`
  的建表原文被逐字节锁住，任何加列都必须改语料，而生成器已随 Python 退役、无法重生成，
  于是只能手工改「冻结证据」——那恰好破坏了「没被手工改过」这个前提。

  它们已整体删除（`9cd3a40`）。留下的行为契约改由各包自己的用例与「不破坏既有配置与既有
  调用方」承担；三份被多个用例真正需要的测试桩从语料文件里抽出，成为常驻的
  `scaffold_test.go`（`internal/proxy`、`internal/configeditor`、`internal/service`）。

  注意：语料里的**历史信息**（参照实现当时的行为、行号出处）仍散落在代码注释里，那些是
  「这段代码为什么长这样」的解释，没有一并删掉；但注释中「由语料锁定」这类**声称有活的
  oracle** 的说法已全部订正。

### 新增

- **访问密钥的管理 API**（Go 侧新增，登记在 `workspacePatterns()` 与 `internal/api/server.go`
  的 `patterns` 里，与既有的 47 条 Python 路由清单不重叠）：

  - `GET /api/access-keys` → `{config_revision, access_keys: [{id, name, enabled,
    key_fingerprint, providers?, models?}]}`。`providers` / `models` **只在配置里显式写了**
    该字段时才出现（省略 = 不限制），空数组原样返回（一个都不许）；**明文永不出现**。
  - `POST /api/access-keys` → `201`，请求体 `{config_revision, name, key?, enabled?,
    providers?, models?}`；`key` 不传由服务端生成。响应是这条记录**外加明文 `key`**——明文
    只在新建与轮换时出现一次，凭据随列表散出去等于每次打开管理页都重新泄漏一遍。
  - `PUT /api/access-keys/{key_id}` → 改名字、启停与两份清单，**不换 key**。`providers` /
    `models` **必填但可为 `null`**（`[...]` 限定、`[]` 一个都不许、`null` 清除回到不限制）：
    必填是为了不让一次漏传字段被当成「清除限制」，那是把授权悄悄放宽。
  - `POST /api/access-keys/{key_id}/rotate` → 换掉明文并返回新值，**旧 key 立即失效**。独立
    端点而不是塞进「更新」：换 key 会让调用方手里那把立刻失效，是必须单独告知的动作。
  - `DELETE /api/access-keys/{key_id}` → `204`，删掉最后一把时 `access_keys` 段一并移除。
  - 404 文案 `访问密钥不存在: <id>`；422 文案与配置层逐字一致
    （`access_keys.<id>.providers[<n>] 引用了未配置的供应商: <name>` 等四类）。

  访问密钥**不随** `/api/config/export|import` 迁移（`TransferableConfig` 刻意不带它）：与
  `local_api_key` 同类，是本实例的入站凭据，而导出文件常被贴进工单与聊天记录。

- **WebUI 新增「访问密钥」页**（导航 id `access-keys`，配置分组下，`webui/pages/accesskeys.js`）：
  列表、新建（明文只显示一次并可复制）、改名、启停、编辑两份清单、轮换、删除。供应商页的
  「访客访问」勾选框与概览运行卡片的「访客访问」行随功能删除一并移除。

- 供应商删除/迁移的凭据占用表把访问密钥也纳进来：工作空间整包导入时，包里的 `api_key`
  撞上任意一把访问密钥同样**报错**而不是悄悄换一个（`configops.secretOwnerOutside`）。

- **访客看板：访问密钥的持有者能看自己的用量**。原先的访客模式是一把全局共享的固定
  key，因此「这个人用了多少」根本无从谈起；访问密钥一人一把之后它才成为一个能回答的问题。

  - **用量归属**：新增 `request_access_key` 旁挂表（`access_key_id` 主键即
    `request_id`，与既有的 `request_workspace` 同一形状，`request_metrics` 的建表 SQL
    因此保持逐字节不变）。`metrics.RecordParams` 与 `proxy.MetricRecord` 各增一个
    `AccessKeyID`，由 `internal/proxy/retry.go` 的 `recordMetric` 从
    `RequestContext.AccessKey` 带上——流式与非流式共用这一条路径，因此两条都覆盖到了。
  - **读数端点** `GET /ui/access-key-usage.json`（`internal/server/accesskey_usage.go`）：
    **认访问密钥本身**，本地管理 key / 面板 key / 推理 key 一律 `401`。它与「访问密钥拿不到
    `/metrics`」不矛盾——`/metrics` 回的是整台实例的读数（管理面信息），这里回的是调用方
    **自己的**流量。**没有 `key_id` 参数**：可见范围完全由凭据决定，加一个可传的标识只会让人
    以为换个值能看别人的。返回 `stats`（与 `/metrics` 的 `total` 同形）、三个拆分维度
    （`model_id` / `provider_id` / `upstream_model_id`）与最近 50 条调用明细。
  - **页面** `/ui/guest.html`（`webui/pages/guest.js`、`webui/guest-api.js`）：KPI 瓦片、
    模型与供应商请求排行、按上游模型的花费估算（对 models.dev 单价，标注「几项匹配到单价」，
    匹配不上的不计入）、最近调用明细（失败标红、无响应与 4xx/5xx 分开、重试打标）。
    `/ui/` 因此有三个凭据面互不相同的页面：管理面（`amkr.apiKey`）、工作空间面板
    （URL fragment 的面板 key）、访客看板（访问密钥）。
  - 访客凭据存在**独立键名** `amkr.guestAccessKey` 下，与 WebUI 的 `amkr.apiKey` 分开：
    同一个浏览器里既登录管理面又看看板时，两边不会互相覆盖。**页头只显示密钥名与尾号 6
    位**，不回显明文——这一页会被投屏、截图、随手转发。

  成本估算必须按 `upstream_model_id` 分组：`model_id` 是调用方自取的**本地路由名**
  （可能叫 `gpt-4o` 而实际打到 `gpt-4o-2024-07-18`），上游名才是唯一能与价格目录对上的字段。

  回归由 `internal/metrics/accesskey_test.go`、`internal/server/accesskey_usage_test.go`
  与 `webui/probes/webui_guest_probe.mjs`（5 个场景）三方守着。前端探针锁的是安全边界而非
  外观：绝不读写管理面的 `localStorage` 键、绝不自己指定密钥或工作空间、绝不发写请求、
  明文 key 绝不上页，以及 `401`（请重填）与 `403`（已被停用，去找管理员）给出不同指引。
  看板页也纳入了 `webui_layout_probe.mjs` 的栅格检查（KPI 固定四张，不随数据增减，否则窄屏
  会甩出孤儿瓦片）。

### 修复

- **`/ui/probes/` 不再对外提供**。`webui/probes/*.mjs` 是 WebUI 的开发期回归测试，浏览器
  从不加载，但发布物是整个 `webui/` 目录，而 `//go:embed webui` 无法按子目录排除，于是它们
  被编进二进制并随 `/ui/` 公开可取（实测线上 `GET /ui/probes/webui_auth_probe.mjs` 返回
  `200`）。探针里逐条写着鉴权流程、面板 key 的存放规则与各页面内部结构——静态资产本身已公开，
  探针额外给出的是「我们怎么测它」，对匿名访问者没有用途，对攻击者却是现成的实现说明。
  现在按**路径段**判定（`path.Clean` 之后比较），因此 `/js/../probes/x.mjs` 这类绕路同样被挡，
  而 `probesomething.js` 不会误伤。

- **用量统计的长窗口查询改走一次扫描上卷，`/metrics` 提速约 7 倍**。原先按「每个目标分组各
  查一次」实现，窗口拉长时查询次数随维度爆炸。改为一次扫描后在内存里按各目标分组重排
  （`internal/metrics/rollup.go`）。分组内的键序仍是字典序——那是响应里 JSON 对象的键序
  （canonical 按插入顺序输出），因此逐分组重排不能省。

- **WebUI 看板栅格在窄档不再出现空轨与孤儿瓦片**。概览、活动、成本、工作空间等页的卡片与
  KPI 瓦片重新分栏（如成本页改 5+7、概览 KPI 改一行五个并补「重试率」），桑基图的节点标签
  按列间走廊收窄，窄窗口下不再互相压字。新增 `webui/probes/webui_layout_probe.mjs` 守住这条：
  DOM 垫片没有 CSS 引擎量不到布局，因此它直接读 `styles.css` 的断点定义与页面声明的列数，
  按 `max-width` 级联算出每一档的真实布局再断言。

## [5.2.1] - 2026-09-19

### 新增

- **工作空间推理 key：让一台 AMKR 作为多个项目共用的网关**。此前工作空间只有一把**面板
  key**（给嵌入的管理面板用），而它**不能调 `/v1`**——因此「把 AMKR 共用给多个 AI 项目」
  这件事一直没有凭据可用：shared `local_api_key` 是完整权限，泄漏一把就等于交出整个实例。

  新增 `workspaces.<空间>.inference_key`（`amkr_ik_` + 43 位 base64url，共 50 字符），
  与面板 key **一起**在 `POST /api/workspaces` 的 201 响应里发放——建空间是唯一能拿到明文
  key 的时刻，AMKR 没有任何端点会再回一次已有 key。轮换走
  `POST /api/workspaces/{空间}/inference-key`，**只换这一把**：面板 key 换掉会让已嵌入的
  页面立刻失效，两者的轮换节奏不同（推理 key 进了各项目的环境变量，泄漏面更宽）。

  它的权限是「被钉死在某个空间上的**推理面**权限」，与面板 key 同构而非第三档全权：

  - **空间由 key 决定，`X-AMKR-Workspace` 被忽略**。这是这个模式存在的全部意义——key 会
    被配进项目的环境变量，若能用一个请求头换空间，一把泄漏的 key 就等于所有空间的推理权限。
  - **不能用 `unified-model`**：那是运维为整台实例挑的全局计划，不属于任何工作空间。
  - **管理面一律 401**，包括本空间的任务 CRUD；`/v1/models` 给的是按空间收窄后的清单。
  - 解析放在 `internal/proxy/handler.go` 的 `authorize`，先照常走完整权限与访客判定，
    **失败之后**才查 `WorkspaceForInferenceKey`。顺序反了会让一把空间 key 变成管理员凭据。
    与面板 key 一样刻意不进 `internal/auth`（那包是被逐字节语料锁定的纯函数）。

- **每个工作空间可配置「允许直呼」的模型清单**（`workspaces.<空间>.models`）。任务名天然
  按空间隔离，但**真实模型名在配置里是全局的**——没有这份清单，任何一把推理 key 都能直呼
  全部模型，而「共用网关」在模型维度的隔离正缺这一环。

  三态：省略 = 不限制（既有配置行为一字不变）；`[]` = 一个都不许直呼（只走任务名）；
  有内容 = 只许这些。**空数组与省略必须可区分**，因此解析后用 `Models != nil` 判断而不是
  `len(...) == 0`——后者会把运维写下的禁令当成放开。判定发生在**别名解析之后**（拿真实
  模型 id 比对），否则同一个模型写成别名就绕过了白名单。

  管理接口为 `PUT /api/workspaces/{空间}/models`（`models` 必填但可为 `null`，`null` 表示
  清除限制；必填是为了不让漏传字段被当成放宽授权），WebUI 的任务页新增「模型授权」入口。

- **`caller_type` 增加 `workspace` 档**。指标库原本只有 `local` / `visitor`，工作空间推理
  key 的流量会被 `store.Record` 的白名单**静默改写成 `local`**——那是权限最高的一档，看板上
  混淆两者会让人误判谁在用实例。同步改了 `schema.go` 的回填合法值表（那条 UPDATE 只在
  `caller_type` 列不存在时执行，因此不会碰到新写入的行；但漏一档会让一次老库升级把这种行
  改写成 `local`，指标永久失真）、`server/query.go` 的字面量过滤与 WebUI 标签。

  界面标签由三元表达式改成查表：原先写的是 `=== "visitor" ? "访客" : "本机"`，多一档之后
  工作空间流量会被显示成「本机」。兜底回显原始值，将来再加档时宁可看到生词。

- **工作空间目录新增 `models` 与 `has_inference_key` 两个字段**（不含任何 key 本身）。
  `models` 只在配置里显式写了清单时才出现（区分「不限制」与「空清单」）；
  `has_inference_key` 是布尔，让界面能提示「发过推理凭据，可以轮换」，而目录接口**仍然
  一个 key 都不返回**——它会被列表页反复轮询。

  另新增 `POST /api/workspaces/{空间}/inference-key` 与
  `PUT /api/workspaces/{空间}/models` 两条路由，已同步 `workspacePatterns()` 与
  `internal/api/server.go` 的 `patterns`（错方法的 405 判定），并保持与冻结的 47 条
  Python 路由清单**不重叠**。

  两条 key 的占用表现在**跨类型统一检查**：一个空间的面板 key 不得等于另一个空间的推理
  key。两把 key 的判定发生在不同调用点（面板面 vs `/v1` 面），同一个字符串两处都命中会让
  权限边界取决于走到哪条路由。既有三条错误文本（`工作空间 a 的 api_key 不能…` /
  `工作空间 a 与 b 的 api_key 重复`）逐字不变。

  空分组的保留判据相应放宽为「有凭据**或**有 `models`」：最常见的形态正是「一个已经有任务
  的空间被加上模型限制」，此时它两把 key 都没有，若按「没有凭据就丢掉」处理，限制会在热
  重载后静默消失。`config.parseTasks` 与 `configops.hasWorkspaceCredential` 两处必须一致。

  验证：`internal/config/model_test.go`（`TestWorkspaceInferenceKeyValidation` 含跨类型撞车
  六例、`TestWorkspaceForInferenceKey`、`TestWorkspaceAllowedModels`、
  `TestWorkspaceModelsParseErrors`、`TestWorkspaceModelsKeepsGroupWithoutCredentials`）、
  `internal/proxy/workspace_test.go`（`TestScopedKeyPinsWorkspaceIgnoringHeader` 为核心，
  另有别名绕过、`unified-model`、拒绝后无回落、空 key 不命中、无清单不限制等）、
  `internal/api/workspaces_api_test.go`（两个新端点、面板 key 不能自行扩权、轮换不动面板 key）、
  `internal/server/handlers_test.go`（`TestModelsNarrowedForScopedInferenceKey`）、
  `internal/metrics/workspace_test.go`（`TestRecordKeepsWorkspaceCallerType` 与未知值仍收敛）。

  `caller_type` 的第三档让 `internal/server/testdata/server_corpus.json` 里两条 422 用例的
  Pydantic `literal_error` 文本必然不同。语料**没有被改动**——它是真实 Python 应用产出的
  冻结证据，逐字节对拍的价值全建立在「没被手工改过」之上。改的是回放侧：新增
  `amendCallerTypeLiteralBody` 只替换取值集合那一段，其余字节仍逐一比对；语料里找不到旧
  文本时**直接失败**，提示对拍前提已变。

## [5.2.0] - 2026-09-19

### 重大变更

- **创建任务不再必填 `model`，但请求一个没指定模型的任务会明确报错**。任务可以先建出来
  占位——例如先把名字、显示名与固定采样参数定下来，模型稍后再选——调用被拒时给出
  `404`「任务 TASK_000001 尚未指定模型；请先在 AMKR 的任务路由中为该任务选择模型」。

  刻意**不做任何回落**：既不会退到 `unified_model.default`，也不会退到第一个已配置的模型。
  静默回落会让一个忘记选模型的任务照常服务，调用方既看不到问题，也无从知道自己实际用的是
  另一个模型——那是最难排查的一类问题，而占位任务本就允许存在，所以在请求时挑明才诚实。

  边界要分清：**「填了但填错」仍然是错误**。`model` 引用了未配置的模型照旧报「引用了未配置
  的模型」，否则一个错字会静默退化成一个空任务——用户在界面上看到任务存在，调用时才发现
  它没有模型。同理，**有备选却没有首选**是非法组合（备选是首选用不了时的退路，没有首选就
  无处可退）：`config.Validate` 不查这一条，因为它的错误顺序与文案是对外契约、不能插入新
  分支，因此闸门放在 `configops.CreateTask`/`UpdateTask`（`422`）与 `RepairTasks`（清掉备选）
  里。

  这是一处**有意的契约放宽**，已在 `internal/config/model_test.go`
  （`TestTaskModelIsOptional`、`TestTaskModelTypoStillFails`）、
  `internal/configops/workspace_test.go`（`TestCreateTaskWithoutModel`、
  `TestCreateTaskFallbackWithoutModelRejected`、`TestRepairTasksKeepsTaskWithoutModel`、
  `TestRepairTasksDropsFallbackWithoutModel`）、`internal/api/workspace_test.go`
  （`TestCreateTaskWithoutModelIsAllowed`）与 `internal/proxy/workspace_test.go`
  （`TestTaskWithoutModelIsRejectedExplicitly`）里显式记录。

- **手写改动了对拍语料 `tasks/create-missing-all` 一条**。生成脚本已随 Python 退役移除，
  因此这条语料是**手工**改的：`model` 移出必填后，空请求体的 `422` 不再列出 `model`
  （`content_length` 249 → 173，`body_text` 去掉最后一个 `missing` 项）。其余 46 条路由语料
  一个字节都没动——`display_name` 只在任务真的取了名时才出现（见下），既有任务响应因此
  逐字节不变。

- **任务路由不再对 `reasoning_effort` 网开一面**：调用方显式传了任务已固定的
  `reasoning_effort` 现在会和其他采样参数一样收到 `400`，而不是被静默覆盖。

  参照实现（`proxy_support.py:147`）刻意把它排除在冲突检查之外，理由是「客户端框架常自动
  带上，且与模型级设置一致」。Go 侧不再沿用：**任务路由是给别的 AI 服务用的，不是给 Agent
  用的**，调用方显式传了一个由任务固定的参数，就该收到明确的拒绝，而不是一个「我传的值没
  生效」的静默结果——后者只在排查时才会暴露，而且看起来像上游的问题。

  这是一处**有意的兼容性分叉**，已在 `internal/proxysupport/support_test.go` 的
  `TestTaskParamConflictsRejectsCallerReasoningEffort` 与 `internal/proxy/proxy_test.go` 的
  `TestTaskFixedReasoningEffortRejectsCaller` 里显式记录。对拍语料不受影响：现有语料里没有
  任何一条任务用例由调用方传 `reasoning_effort`（`internal/upstream/testdata/` 里那条
  `test_task_allows_caller_reasoning_effort_but_still_overrides_it` 是 Python 侧录的，Go 不做
  回放），因此这是一处纯语义增补，不是语料回归。

### 修复

- **图表每次重绘都会"往中间挤一下再复原"**。图表的首帧是在节点还**没挂进文档**时画的
  （页面都是先 `h()` 建好节点、之后才交给 `app.js` 挂载），此刻 `clientWidth` 为 0，
  `sizing()` 只能退回 `|| 720` 的兜底宽度，于是画出一张 `width="720"` 的 `<svg>`；
  挂载后 CSS 的 `.chart { width: 100% }` 把 `<svg>` 元素拉满容器（实测 1200px），
  但 `viewBox` 仍是 `0 0 720 …`，`preserveAspectRatio` 默认居中等比缩放，整幅图就被缩到
  中间——网格从 x=60..1184 缩成 300..944；紧接着 `ResizeObserver` 回调按真实宽度重画，
  图"啪"地弹回满宽。每次指标轮询（概览 10 秒）都会整块重建图表，这个内缩动画于是反复出现。

  修法只动了 `sizing()` 一处：有 `ResizeObserver` 时**不在构造期画首帧**，改由 RO 回调绘制
  ——它在布局之后、绘制之前触发，第一帧拿到的就是真实宽度，不存在"先画错再纠正"。
  无 `ResizeObserver` 的降级分支（探针环境）保持按测量值同步画一次的旧行为。

  验证：新增 `webui/probes/webui_chart_sizing_probe.mjs` 并接入 `ci.yml` 与 `release.yml`。
  它的垫片忠实还原了两件事——脱文档节点 `clientWidth` 为 0、RO 回调异步送达——正是旧写法
  出错的前提；断言构造期一次都不许绘制、挂载后按容器宽度绘制且 `viewBox` 同宽、容器缩放
  仍跟随重画、节点被整块替换后观察者断开。该探针在修复前会失败（复现出那帧 `width="720"`），
  修复后 16 项全过；另在真实 Chrome 中逐帧复核，修复后首次绘制即等于容器宽度。

- **概览「结果构成随时间」卡下方空出近 300 像素**。该卡是 `col-7`，与 `col-5` 的请求流同排；
  同排等高（`align-items: stretch`）时行高由请求流决定，而请求流限高 520px、内部滚动，
  于是这一排固定被撑到约 652px，而「结果构成」自己的内容是定高 220px 的图加一行页脚，
  中间那 297px 就空在卡内。

  修法**不是**把图拉高填满：`chart-host` 是像素定高，拉长只会让图内多出空白，这正是
  `styles.css` 里记着的取舍。改成把本来单独占 `col-12` 的「Token 构成随时间」搬进同列叠放
  ——两张图用的是同一份 `points`、同一个桶宽，横轴逐像素对齐，并排看"结果结构"与
  "Token 结构"才读得出同步异动；同时概览少一个整行卡片。列容器补上 `gap: var(--sp-2)`，
  否则同列两张卡会贴在一起，与栅格其余处恒为 `gap` 的节奏不一致（单卡列不受影响）。

  验证：用真实 Chrome（CDP + 假 fetch）量实际几何，修复前该卡内图底到页脚为 297px、
  修复后为 24px（即卡内正常留白），两张卡间距 16px 与栅格一致；1100px 与 820px 断点下
  该列塌缩为整行、自身高度 355+342+16=713px，不再产生新空白。`webui_auth_probe.mjs` 的
  `reentryHasCharts`（断言 `.chart-host` 恰为 3）与 `overview_shows_request_stream` 均未改动
  即通过——搬迁不增删图表，只有把两张图并成一张时才需要动这条断言。

### 新增

- **任务可以取中文显示名（`display_name`）**。任务路由页的任务卡片与编辑页现在用人取的
  名字（如「长文摘要」）作标题，任务名（`TASK_000001`）退到旁边的徽标与详情里——这个页面上
  人认的是业务名，而调用方仍然只能传任务名，因此它必须一直可见。显示名可选、纯展示、不参与
  路由；两端空白在解析时会被去掉。

  存放位置选的是**配置字段**而不是浏览器本地：显示名描述的是「这个任务是什么」，属于配置
  本身，因此随导出/导入一起走，换台机器或分享给同事都不会丢。代价是它出现在 `TaskResponse`
  里，而这几个响应体由对拍语料逐字节锁定。

  兼容性用**条件包含**解决：`display_name` 只在任务真的取了名时才写进响应，没取名的任务响应
  因此与新增该字段之前逐字节相同。这不是取巧——`tasks/list` 那条语料里的任务本就没有显示名，
  它原封不动地通过了（`internal/api/testdata/management_api_corpus.json` 47 条路由里只有
  `tasks/create-missing-all` 因 `model` 放宽而变动，见「重大变更」）。同样的取舍也写进了
  `internal/config/doc.go` 的兼容性清单：被锁定的响应体不允许新增「当前空间」那种请求上下文
  字段，而显示名属于任务本身。

  管理 API 侧：`TaskCreate`/`TaskUpdate` 接受 `display_name`（传 `null` 清除），
  `TaskUpdate` 的「至少需要提供一个要更新的字段」判定把它与 `model`、`fallback_model`、
  `params` 同等看待——否则界面上只改中文名会被 `422` 顶回来。任务对象的键序刻意把
  `display_name` 放在最后，既有任务的落盘字节因此一个都不变。

  验证：`internal/api/workspace_test.go` 的 `TestUpdateTaskDisplayNameCountsAsUpdate` 与
  `TestTaskResponseOmitsDisplayNameWhenUnset`（后者逐字节断言未取名任务的响应前缀）、
  `internal/configops/workspace_test.go` 的 `TestUpdateTaskDisplayName`、
  `internal/config/model_test.go` 的 `TestTaskDisplayNameIsParsed`；WebUI 侧由
  `webui/probes/webui_auth_probe.mjs` 的任务场景覆盖（首选下拉新增的「尚未指定」选项使该
  探针的 `primarySelectListsAllModels` 断言由 2 项改为 3 项，已同步）。

- **工作空间的用量统计与请求流向图**。WebUI 新增「工作空间」页：各空间的用量读数、
  请求排行与空间明细表，以及一张表达请求流向的**桑基图**（工作空间 → 请求模型 → 实际
  模型 → 供应商 → 上游模型），流带粗细即承载量，可在请求数与 Token 之间切换。对应读数
  也可通过 `GET /ui/workspace-usage.json` 取（需鉴权，`hours` / `all_history` 与 `/metrics`
  同口径）。

  归属是**写入时落库**的，不能查询期反推：同一个任务名可以合法地同时存在于多个工作空间
  （`router-config.example.json` 里 `TASK_000001` 就在顶层与 `workspaces.teamA` 各有一份），
  而历史指标只记了调用方传的模型名，无从判断当时带的是哪个 `X-AMKR-Workspace`。

  存储用**旁挂表** `request_workspace`（`request_id INTEGER PRIMARY KEY` + `workspace`），
  而不是给 `request_metrics` 加列：后者的建表原文被 `schema.jsonl` 逐字节锁定，而
  `ALTER TABLE ADD COLUMN` 实测会重写 `sqlite_master.sql`，加列必然打破那份语料——生成器
  已随 Python 退役，无法重生成。旁挂表让 `request_metrics` 逐字节不变，差异面缩小到
  「多一张表」；该差异以显式白名单（`extraMasterEntries`）列在 schema 差分测试里，再加
  第二个库对象会立刻失败。旁挂表用 rowid 别名表达一对一，因此**不新增索引条目**。

  没有归属的行（升级前的历史）单独统计为 `unattributed`，**不兜底成 default**——那会把
  旧账算到默认工作空间头上，凭空造出一段并不存在的用量。流向图里某一端为空的请求同样
  留缺口而不补占位节点，否则分不清哪条是数据、哪条是兜底。

  桑基布局只用**一把纵向尺子**（全局 scale），不让每层各自缩放到满高：后者会让同一节点的
  入边与出边拿到不同厚度，流带溢出节点、读数失真。节点量取 `max(流入合计, 流出合计)`。

  验证：`internal/metrics/workspace_test.go`（分组计数、Token 汇总、未归属隔离、五层相邻
  连边、窗口裁剪、参数校验）、`internal/server/workspace_usage_test.go`（形状与分组、访客
  拒绝、非 GET 405、参数校验先于鉴权、`all_history` 的 `window.from` 为 null）、
  `webui_chart_probe.mjs` 新增 11 条桑基断言（层数、厚度与请求数成正比、流带不溢出节点、
  同节点出边不重叠、退化输入不产生 NaN），全套 162 条通过。

- **工作空间可以带面板 key，并提供一个可被其它应用嵌入的控制面板**。应用侧可以在创建
  工作空间时提供自己的 `api_key`（`POST /api/workspaces`）；在 WebUI 里创建则由服务端
  生成（`amkr_ws_` + 43 位 base64url）。这把 key 让应用把一个**只看得到自己那个空间**的
  面板（`/ui/panel.html`）嵌进自己的后台，读自己空间的用量与流向、并管自己的任务。

  权限刻意**不是**第三档角色，而是「被钉死在某个空间上的任务面权限」。实现上它不进
  `internal/auth`（那个包是被逐字节语料锁定的纯函数，加分支会把它作为兼容性凭证的价值弄
  糊）：`authorizedTaskConfig` 先照常调 `auth.Authenticate`，**失败之后**才用请求头里的
  key 查 `config.WorkspaceForAPIKey`，因此面板 key 永远走不到 `IsFull()`。

  生效时 `X-AMKR-Workspace` **被忽略**——空间由 key 决定。这是该模式要防的核心事情：面板
  key 会出现在被嵌入页面的 URL 里，正是最可能泄漏的位置，若请求头能换空间，一把泄漏的 key
  就等于所有空间的任务面权限。同理它不能钉在 `default` 上（配置里没有那个槽位）。

  配套三条安全规则，由 `webui/probes/webui_panel_probe.mjs` 断言（它把 `localStorage`
  做成了**毒药记录器**）：面板与后台 WebUI **同源**，因此它**绝不读写 `localStorage`**
  （那里存着后台的管理 key）、**绝不发送 `X-AMKR-Workspace`**、**只从 URL fragment 取凭据**
  （fragment 不进 `Referer` 也不进访问日志；换成 query string 会两头都留下明文 key）。

  新增 `GET /ui/workspace-panel.json`（只认面板 key，完整权限与访客一律 401），响应在
  `/ui/workspace-usage.json` 基础上多一个 `workspace` 字段（key 钉死的空间名）与 `models`
  数组（模型 ID + **可见**别名，供任务表单选模型；隐藏别名刻意不含），且 `unattributed`
  **恒为零**——没有归属的请求不属于任何空间，给面板看既无意义也泄漏别的空间的规模。

  面板 key 明文**只出现在** `POST /api/workspaces` 的 201 响应与配置文件里：目录接口
  （`GET /api/workspaces`）刻意不返回它（那个接口会被列表页轮询），配置导出也照旧**剥掉**
  它。WebUI 因此在校验空间建好后立刻弹出「复制 key / 复制嵌入片段」并说明只显示这一次。

  顺带放宽一条既有不变量：**带 `api_key` 的工作空间即使没有任务也保留**（无 key 的空分组
  照旧被清掉）。带 key 的空间正是「应用先建空间拿 key、之后才陆续填任务」这个时序的产物，
  若因没有任务被清掉，应用手里的 key 会在下一次配置写回时无声失效。两个口径
  （`writeWorkspaceTasks` 与 `WorkspaceNames`）同步改成「看 key」。

  验证：`internal/api/workspaces_api_test.go`（建空间返回 key / 接受调用方 key / 拒绝非法
  输入且配置不变 / **面板 key 钉死空间**——伪造 `X-AMKR-Workspace` 后任务仍落在 key 对应的
  空间、被挡住的全局面逐一 401、建空间与代理面不可用 / 删空间后 key 失效）、
  `internal/server/workspace_panel_test.go`（按 key 收窄、别的凭据被拒、空空间仍响应且
  `unattributed` 为 0、非 GET 405、`hours` 校验）、`webui_panel_probe.mjs`（5 个场景）、
  `webui_auth_probe.mjs`（显式建空间流程）。

  接入方文档见新增的 [`docs/PANEL.md`](docs/PANEL.md)：取 key、iframe 嵌入、权限边界、
  凭据为什么走 fragment、跨源限制（本项目**不发** CORS 头，因此 `api=` 覆盖只在同源可用）、
  自定义 UI 要调哪些接口、key 的生命周期与排查表。

- **工作空间的整包迁移（带面板 key），与配置迁移相互独立**。新增
  `POST /api/workspaces/export` 与 `POST /api/workspaces/import`，把一个或多个工作空间连
  任务带 `api_key` 一起搬走。与 `/api/config/export|import` **刻意分成两条通道**，因为语义
  恰好相反：

  | | `/api/config/*` | `/api/workspaces/*` |
  | --- | --- | --- |
  | `api_key` | 剥掉（导出文件会被贴进工单与聊天记录） | **带上**（有意的凭据搬迁） |
  | `providers` / `models` | 核心内容 | **不含**（搬的是命名空间，不是模型库） |
  | 合并规则 | 按 `base_url` / key secret 去重、按模型 ID 合并 | 同空间整包覆盖，或加前缀改名 |

  因为不带模型库，包里的任务引用不到目标实例上的模型时由 `RepairTasks` 的既有规则清掉并
  **如实回报**（`removed_tasks`），而不是带进来一批请求时必然 404 的僵尸任务。

  冲突策略由 `prefix` 决定：留空 = 同名空间整包覆盖（恢复备份；旧 key 立即失效），非空 =
  改名为 `前缀+原名`（搬别人的空间、不丢自己已有的）。响应把 `added` 与 `replaced` **分开
  报**，因为覆盖会让某个面板 key 失效，调用方必须能告知用户，否则嵌入方的面板会毫无征兆地
  开始 401。导入前先备份配置，且只认完整权限（内容里有明文 key）。

  包格式用独立的 `spaces` 键而不是套一层 `config`：`providers` / `models` 是**合法的空间
  名**，套包装层会让「空间叫 providers」与「包里混进了配置导出的段」变成同一种输入，只能
  二选一地误判（有专门用例钉住）。

  key 冲突分两种情况。撞上目标实例上**别处**的凭据（另一个空间或 `local_api_key`）、或用了
  保留的 `amkr-visitor` 时**报错**并指出与哪个空间撞了，而不是悄悄换一个：换掉看似"让导入
  成功"，实际藏起了用户必须知道的事——那把 key 通常已经嵌在别人的页面里，换掉之后旧 key
  会指向别人**别的**空间，而响应里没有任何字段能说明这件事。**例外是加前缀克隆**：原空间
  仍在并占着原 key，换 key 是必然的，因此由响应里的 `rekeyed`（`空间名 -> 新 key`）明确
  回报，嵌入方据此更新嵌入片段。

  验证：`internal/api/workspace_migration_api_test.go`（导出带 key 且不含 providers/models、
  按名导出、不存在的空间 404、默认空间 400、同名覆盖与加前缀改名、引用失效任务被清并回报、
  误导入配置包被拒、以 providers/models 命名的工作空间可用、key 撞车报错且配置不变、
  保留 key 报错、**克隆后两个空间各自可用且 key 不同**、只认完整权限）。

- **任务路由可以固定 `max_tokens`**（输出上限）。白名单、管理 API 的 JSON schema 与 WebUI
  的任务编辑器同步支持；和 `top_k`、`seed` 一样按**整数**校验，落盘渲染成 `32` 而不是
  `32.0`。

  跨方言同义字段一并处理：固定 `max_tokens` 后，Responses 方言的 `max_output_tokens` 也算
  冲突（`400`）。这条不能省——请求体的参数归一化（`max_output_tokens` → `max_tokens`）发生在
  冲突检查**之后**，漏掉别名会让调用方用 Responses 方言传的上限被静默丢掉。这与既有的
  `stop` / `stop_sequences` 是同一套规则。

  任务参数白名单出现在 6 处冻结语料的报错文本里（`taskparams.jsonl` ×3、`model.jsonl`、
  `management_api_corpus.json`、`configops_corpus.json` ×2），按新顺序
  `…, seed, stop, max_tokens, reasoning_effort` 逐字更新，其中管理 API 语料的
  `content_length` 由 183 改为 195。生成器已随 Python 退役，这批语料是手工改的。

- **供应商页的 Key 行只列出该 Key 正在服务的模型**。Key 名称下方原来显示的是探测结果
  （上游 `/v1/models` 广告出来的全部模型），动辄几百个，既读不完，也不代表这个 Key 真的
  对外提供它们——每个模型要显式绑定到 Key 才生效。现在这行改为从模型 `targets[]` 反查
  出的绑定模型（升序，逗号分隔）；一个都没绑定时整行不显示。探测错误仍照常显示，否则
  「这个 Key 探测炸了」会因为恰好没绑定模型而看不出来。点「管理模型」展开的芯片列表
  不变，那里本来就要同时看到探测到的与已绑定的。

- **模型路由页改用与供应商页相同的左侧竖向导航**。模型列表固定在左栏（窄屏退化为横向
  滚动条），右侧详情的位置不再随模型数量漂移；原来那条「模型 · xxx」横排标签页条已移除。

  一个模型库动辄几十个模型——比供应商多得多，横排标签页要么换行、要么横向滚动，把页头
  撑成好几行，点开哪个模型还得先在一排标签里找。左侧竖排每项带模型 ID 与目标数（**0 个
  目标**的模型其实是坏的，这个读数在选模型时最该看见）。与供应商栏刻意的差异是**不带
  图标**：那边的品牌标志有信息量（一眼分辨是哪家），而这里每一行都会是同一个「路由」
  图标，重复几十次只是占宽，列只有 180px。

  排版规则抽成共用类（`.rail-split` / `.rail-nav` / `.rail-item` / `.rail-detail`），两页
  各写一份迟早会漂成两种间距。配套新增 `webui/probes/webui_routing_probe.mjs`（33 项断言）
  并接入两个 CI 工作流，锁住：横排标签页条不能被改回来、切换模型时详情要跟着换、展开
  编辑器后左栏不能被整块替换掉；顺带钉住候选目标去重（已绑定的 Key 不得再出现在候选里，
  这条曾因模板字符串里的 `${target.key}` 被转义成字面量而失效）。

- **任务工作空间**：任务集合可以按工作空间隔离，**任务名只在工作空间内唯一** —— 不同
  空间可以有同名任务，各指向自己的模型与采样参数。调用方用 `X-AMKR-Workspace` 头选择
  （不带即默认工作空间，也就是配置的顶层 `tasks`），该头不会被转发给上游。

  几个刻意的取舍：
  - **`config_version` 仍是 4**，`workspaces` 是可选新增字段，顶层 `tasks` 即默认工作
    空间。既有配置与既有调用方（不带该头）行为逐字节不变，因此 `tasks/list` 这类被语料
    锁定的响应体没有、也不允许新增 `workspace` 字段——当前空间由请求头决定。
  - **工作空间只隔离任务**。模型 ID、别名、隐藏别名与 `unified-model` 仍全局唯一，任务名
    也不能与它们撞名：否则 `resolve_route` 的语义会取决于查表顺序，那是全局唯一的判断。
  - **工作空间由「在它里面建任务」隐式产生**，没有独立的创建接口，配置里的空分组也会被
    清掉。工作空间只是「一组任务」的容器，一个没有成员的分组既不可观测也没有意义，
    因此 `WorkspaceNames` 由任务反推而不是回放配置键，界面上的下拉也就不会列出空分组。
  - **未知工作空间名不是错误**：那里没有任务，按普通模型名继续解析，最终和「模型未配置」
    是同一个 `404`。单加一条「空间不存在」的错误既没有信息量，也多一种要维护的状态。
  - **访客 Key 不能使用任务**，带上该头也一样（与不带时一致）。
  - **工作空间自身可以改名与删除**（`PUT` / `DELETE /api/workspaces/{workspace}`），
    目录读数是 `GET /api/workspaces`。这三条是 Go 侧新增的管理面资源，注册在 `/api`
    之下但列在独立清单里（`workspacePatterns()`），不混进那份被逐字节语料锁定的 47 条
    ——它们没有历史版本可对照，塞进去会让「这 47 条等于已发布行为」这句话失效。
    **没有 `POST`**：空分组不进配置，空间由「在里面建第一个任务」隐式产生。

    改名就是把整组任务搬到新键下（先写新键再清旧键，顺序反了两处共用的任务对象会被
    误删）；删除等价于把该空间的任务一次清光。两条动作都不合并同名空间——合并会在中间
    态造出重名任务，而重名在配置层是非法的，等于用必然失败的中间态做有损操作，因此报
    `409`。默认空间不可改删（`400`），不存在的空间报 `404`（空间由任务反推）。

    原 `/ui/workspaces.json` 已随之下线：目录挂在 `/ui/` 的唯一理由是绕开语料冻结，代价
    是「不开 WebUI 就拿不到这个接口」，而 WebUI 是否启用与「能不能管理工作空间」本无关。

  管理 API 的五个任务端点（`GET/POST /api/tasks`、`GET/PUT/DELETE /api/tasks/{name}`）
  都认该头，语义与代理面一致；同一空间内重名报 `409`、跨空间同名合法，取单个任务时不会
  回落到别的空间的同名任务。WebUI 任务路由页顶部新增空间下拉（带任务数，选择记进
  `localStorage`），并可从下拉里输入新空间名。任务随 `/api/config/export`、
  `/api/config/import` 一起迁移，命名工作空间也在其中。

- **自更新（`amkr --update` 与 WebUI）**：**推翻了产品决策 8「取消自更新」**。当初退役
  Python 实现时，约 573 行的 `update.py` 连同 `--update` 一起被删掉，理由是「更新
  exe 太麻烦」；而 Go 版实测发现 Windows 允许**重命名**正在运行的 exe（只是不允许覆盖或
  删除），于是整个替换退化成两次 `os.Rename`，不再需要独立的更新器程序。
  现在 `amkr --update` 会：查 GitHub Releases → 下载对应平台产物与 `checksums.txt` →
  **校验 SHA-256 通过后才安装** → 就地换文件 → 启动一个分离的收尾助手进程，由它等旧服务
  让出端口后清理旧文件并按当前注册状态把服务拉起来。WebUI 设置页的「检查更新」发现新版本
  后会多出一个「立即更新」按钮（`POST /ui/update/apply`）。

  几个刻意的取舍：
  - **不留备份、不自动回滚**（按需求确认）：`<exe>.old` 只是替换过程中不得不短暂存在的
    中间态——旧进程还在映射它，此刻删不掉——由助手在旧进程退出后清理，不作为回退点。
  - **校验失败绝不安装**：拿不到校验和、或校验和不符时一律拒绝，已安装的二进制原封不动。
    替换的回滚也做了：第二次改名失败会把旧文件改回来，不会把用户留在「没有可执行文件」的状态。
  - **助手不等 PID，等端口释放**。SYSTEM 计划任务形态下拿不到 PID（查询注册信息需要提权），
    而端口是两种形态共有、且是启动新版本真正需要的那个资源。也不用 `/health` 判断「已停止」：
    旧进程刚开始关停、仍在处理在途请求时它就失败了，会把「还没停完」误判成「已经停完」。
  - **CLI 与服务端两条路的收尾方式不同**。CLI 触发的更新里，发起者不是那个在服务的进程，
    所以由助手去停旧服务；服务端触发时发起者**就是**服务进程，它自己优雅退出即可——若让
    助手去停，后台服务形态走 `taskkill /T /F`，实测会把助手自己也一起杀掉。
  - **权限不足时如实说明，不谎称自动重启**。实测：服务以 SYSTEM 计划任务运行时，非提权
    进程连 `schtasks /Query` 都返回「拒绝访问」，既查不到也停不掉它。这时 `--update` 仍会
    完成替换，但不启动助手，并明确提示需要以管理员身份执行 `amkr --service restart`。
    可以自动重启的判据（`CanRestartService`）也因此显式化了，而不是「尽力而为」。
  - **不新增 `/api` 路由**：管理面 47 条与运维面 7 条路由被逐字节语料锁定，而自更新是
    Python 侧没有的新能力，手写一条语料等于伪造兼容性证据。因此与价格目录一样挂在 `/ui/`
    前缀下；区别是它要求完整权限（访客 key 一律 401），只有 status 不鉴权。

  语料同步：`cmd/amkr/testdata/cli_corpus.json` 的 `dropped_flags` 去掉 `--update`（只剩
  `--show-logs`、`--restart-service-after-update`），对应条目的动作由 `update-dropped`
  改回 `update`；`TestDroppedFlagsAreNotDefined` 同步收窄。这批语料是手工改的——生成器
  已随 Python 退役删除，与 `d459ea3` 那次同样的处理方式。

### 工程

- **发布流程开始产出容器镜像**。此前 `release.yml` 只交叉编译二进制，GHCR 上的
  `ghcr.io/sparrived/auto-model-key-router` 是 Python 时代的遗留包（且为私有），于是
  「用镜像部署」只能靠本机 `docker build` 再手工推送——没人做，部署机上就一直跑着旧版本。
  新增独立的 `image` 作业（`needs: verify`，与二进制发布并行）：tag 触发时构建并推送
  `:<版本号>` 与 `:latest`，手动触发只构建不推送（用于演练 Dockerfile）。

  - `Dockerfile` 新增 `ARG VERSION`，经 `-ldflags "-X main.version=..."` 注入。容器里没有
    仓库可查，`amkr --version` 只能靠这个值；不给参数则退回仓库默认值，与本地
    `go build` 一致。
  - 只构建 `linux/amd64`：加 arm64 会让**构建阶段**跑在 QEMU 模拟里（Go 能交叉编译，
    但镜像内的 `go build` 要在模拟器里执行），每次发布多等数分钟。有 arm 部署需求再加。
  - 尝试用 `GITHUB_TOKEN` 把包设为 public（`continue-on-error`）：包默认私有会让部署机
    匿名 `docker pull` 被拒；该 API 属用户级、可能要 PAT，因此失败只留提示不拖垮发布。
  - `verify` 与 `ci.yml` 都补上了 `webui_panel_probe.mjs`——它此前只写进了 README 的
    本地检查清单，CI 里是缺的，等于面板的三条安全规则（凭据只从 fragment 取、不碰
    localStorage、不发 `X-AMKR-Workspace`）没有门禁。

  验证：两个工作流经 `yaml.safe_load` 解析（`release.yml` 的 jobs 为
  `verify/image/release`）；`permissions` 增列 `packages: write`；`--version` 实测
  `-X main.version=5.2.0` 得 `5.2.0`、不传 `-X` 得仓库默认值。

  发布后实测：`docker logout ghcr.io` 后匿名 `docker pull` 成功（`:5.2.0` 与 `:latest`
  摘要一致，`sha256:f24e7bdb…`），即设为 public 那步确实生效；容器内 `amkr --version`
  返回 `5.2.0`，版本注入确认可用。`README.md` 的 Docker 一节此前只写了「自己构建」，
  没提这个已公开的镜像，一并补上，并说明自建时 `--build-arg VERSION` 不要带 `v` 前缀
  （与发布流程取 `refs/tags/v*` 去前缀的规则保持一致）。

## [5.1.1] - 2026-09-18

### 修复

- **耗时/首字趋势的线是断开的**：耗时与首字延迟是均值型指标，无请求的桶上算不出
  均值（后端返回 `null`，前端也按缺失处理——这与「空闲 = 0 次/分」是两件事）。原先折线只按
  「连续非空」分段，于是 15 秒一桶的稀疏流量下曲线被切成几十段外加一地孤立圆点：真实 1 小时
  窗口（241 点、90 个空桶）断成 26 段，看上去像图表坏了而不是「这些时刻没有请求」。现在缺口
  两侧的实测点之间补一条灰色疏虚线（`.series-gap`），示意走势延续；实测段仍是实线紫色，
  与「累加中」尾桶的密虚线可区分。迷你趋势图（KPI 瓦片内联）同一口径。空桶仍不参与加权
  平均，只是不再把线剪断。
- **价格目录可能被并发重复下载（每次约 4.7 MB）**：取回此前有两条触发路径——目录过期时的
  后台刷新（走单飞闸门）与常驻循环的预热 / 定期刷新（**直接调 `Refresh`，绕过了闸门**），
  两者可同时打向上游，不仅重复下载，先返回的旧结果还可能覆盖后返回的新结果，让目录无故回退。
  现在新增 `refreshOnce` 作为唯一取回入口，两条路径都走它，已在途时直接返回。
  实测去掉闸门后并发触发 8 次就是 8 次真实取回，加了闸门恒为 1 次。
  闸门之后又补了一层：只挡「同时在途」仍不够——一次过期会踢出**多个**后台 goroutine
  （连续几次 `/ui/pricing.json` 读就会），第一个取回成功、闸门落下后，其余 goroutine 各自
  又下载一遍，**读几次就下载几次**（实测 6 次，期望 2 次）。因此拿到闸门后要再确认一次
  新鲜度，不新鲜才取回；`DefaultRetry` 的失败退避也靠这一步才生效，否则一次网络抖动会被
  同一批 goroutine 立刻穿透重试。新增两条**同步可判**的断言覆盖这两个不变式
  （新鲜期内不得重复取回、退避期内不得重试），去掉这一步即稳定失败（不依赖 goroutine
  调度巧合）；原先的突发计数断言靠调度撞运气，已改为等所有在途 goroutine 收敛后再读数。
- **Windows 一行安装命令从未成功过**（README 原先首推的两种写法都有问题，属连续两层坑）：
  - `install.ps1` 是含中文的无 BOM UTF-8，而 Windows PowerShell 5.1 对 `.ps1` 默认按
    ANSI/GBK 读，中文被解成乱码后变成非法 token，脚本**根本跑不起来**（报
    `Unexpected token`）。已补 UTF-8 BOM（`switch-to-go.ps1` 同样处理）。
  - 但带 BOM 的文件无法用 `irm <url> | iex` 执行：iex 会把 BOM 当成标识符的一部分，
    导致 `<#` 不再被识别为注释块。两者不可兼得，故 README 与脚本示例统一改为
    `irm <url> -OutFile ...; & ...`（PowerShell 按文件读取，能正确处理 BOM）。
  - 实测新命令：下载、**sha256 校验通过**（与发布页 `checksums.txt` 一致）、安装成功、
    自检输出正确版本号。

### 新增

- **供应商切换器从顶部横排改为左侧竖排，并在「添加供应商」里加入常见供应商预置**。
  供应商数量随使用增长，十几家很常见；横排标签页要么换行、要么横向滚动，把页头撑成两三行，
  右侧详情还会随标签数量横向漂移。现在切换器固定在左侧一列（窄屏自动退化为横向滚动条），
  右侧详情位置始终不变。

  每项带对应品牌图标（OpenAI / Anthropic / DeepSeek / Moonshot / 通义千问 / xAI / Mistral /
  Groq / OpenRouter / 硅基流动 / Ollama / LM Studio / vLLM），认不出品牌时回退到通用机架图标；
  图标是内联 SVG，不引入图标字体或 CDN 请求（WebUI 必须离线可用）。识别先按 `base_url` 的
  host（含非默认端口，本地部署正是靠端口区分 Ollama / LM Studio / vLLM），再按供应商 id
  子串兜底，因此 `my-deepseek` 这类自定义命名也能拿到对的图标。

  「添加供应商」对话框新增常见供应商九宫格，点一下即填好名称与地址（仍可手改）。**只收录
  「base_url + 默认路由路径」能拼出正确上游端点的供应商**：Gemini（`/v1beta/openai`）、
  Perplexity（`/chat/completions`）、智谱（`/api/paas/v4`）的兼容端点不在 `/v1` 之下，预置
  它们会拼出错误路径，而用户很难看出是地址错了——因此刻意留空给手填。

  配套新增 `webui/probes/webui_providers_probe.mjs`（47 项断言）并接入两个 CI 工作流，
  锁住易回归的三点：横排标签页条不能被改回来、预置地址不得带 `/v1` 后缀（否则拼成
  `/v1/v1/...`）、品牌识别不能被单字符品牌名（xAI 的 `x`）误伤 `my-box` 这类无关命名。
- **监控信息密度重做：概览加实时内容，「实时活动」改为「用量统计」专注历史，日志独立成页**。
  原来的分工是「概览看窗口汇总、实时活动看实时速率 + 请求流 + 日志」，于是同一页里挤了
  三种时间尺度：分钟级的速率、逐条的请求流、以及跟用量无关的日志。现在按问题分页：

  - **概览**（实时）：KPI 瓦片之后新增「实时脉搏」——RPM / TPM / 耗时 / 成功率四格小倍数
    迷你图共用一条时间轴，用来回答「这次耗时抬升是不是伴随了成功率下滑」（分开看四张图要
    上下滚动，摆在一起才能看出同步异动）；新增「结果构成随时间」按成功 / 失败 / 重试堆叠，
    总量平稳但失败占比抬升是上游开始抖动的早期信号；新增「状态码明细」，分组只说「有 5xx」，
    明细才能区分 502 与 504。**请求流从用量统计移到这里**（它回答的正是「此刻正在发生
    什么」），逐条成本列一同搬过来。
  - **用量统计**（原「实时活动」，历史）：窗口在 1h/6h/24h/3d/7d 之上新增 1 个月 / 3 个月 /
    6 个月 / 1 年 / 全部历史，重心从「此刻多快」移到「这些时间用了多少」——KPI 换成窗口
    请求、Token 用量、活跃天数、日均请求（附峰值日）与成功率等；新增「累计用量」曲线
    （长窗口下逐桶曲线会被日周期噪声填满，"累计涨到多少"更能说明规模）、「按天用量」堆叠柱、
    「日内时段分布」（长窗口用热力图会糊成一团，时段分布才是能读的粒度）。维度明细表保留。
  - **服务日志**（新页）：独立成页，带 2/5/15 秒与暂停的刷新档位、按级别（全部 / 警告以上 /
    仅错误）过滤、关键字搜索与「自动跟随最新」开关。拆出来的理由是日志属排查工具，
    2 秒轮询与用量重绘互相牵制；同时原先日志只在轮询时被动刷新，级别过滤这类操作没有位置。

  配套改动：`/metrics/series` 的 `hours` 上界由 720（30 天）**有意放宽到 8760（1 年）**，
  否则 3 个月以上的窗口会被 422 挡掉。放宽是安全的——点数上限（500）由
  `seriesPointLimitExceeded` 独立把关，长窗口只能配粗桶（1 年 = 366 个日桶），
  `hours=8760&bucket_seconds=60` 依然是 422；`/metrics/requests` 仍保持 720。
  「全部历史」不用新增后端参数实现（`/metrics/series` 没有 `all_history`，且该参数在差分
  语料里被钉为「忽略」）：改由 `/metrics/requests?all_history=true` 读回最早一条记录的时间
  推导跨度，超出 1 年时夹到上限并在界面上如实标注「只覆盖最近 365 天」，不假装查到了全部。
  时间轴标签按整段跨度选格式（≤2 天用 `HH:MM`，≤60 天用月/日，更长用年/月），长窗口下
  固定 `HH:MM` 会退化成每个点都是 `00:00` 的噪声。

- **成本估算（基于 models.dev）**：AMKR 此前只记 token、不算钱。现在服务进程会取回
  models.dev 的公开单价目录（约 4.7 MB），编译成「模型 id → 单价」后只驻内存，此后每
  6 小时条件复验一次（命中 `304` 时不重复下载），并通过 `GET /ui/pricing.json` 提供给
  界面。WebUI 新增「成本」页（估算成本、模型成本排行、成本构成、供应商成本、最近请求
  成本与可核对的单价明细表），概览页新增「估算成本」KPI 与「模型成本排行」卡，实时活动
  页的请求流新增逐条成本列。
  按 `upstream_model` 名称匹配（大小写不敏感，可穿透 `vendor/` 前缀与 `-YYYY-MM-DD`
  日期后缀），按 token 类别分别计价（普通输入 / 缓存读 / 缓存写 / 输出），缓存价缺失时
  回退输入价。
  三条刻意的取舍：**金额是派生读数、不落库**（目录更新后历史金额会跟着变）；**同一模型
  被多家供应商标成全 `0` 时先排除这些挂名条目再取最便宜**（实测有 465 个模型受此影响，
  不做排除会让 `claude-sonnet-4-6`、`qwen3-max`、`glm-4.6` 等显示成 `$0`）；**拿不到
  单价时显示 `—` 而不是 `$0`**（`0` 会被读成「这次请求免费」，而事实是「不知道」）。
  当前实现只取目录里的基础单价，忽略阶梯定价（`cost.tiers`），也不做汇率换算。

### 工程

- **清理版本库中的过程稿与本地运行态文件**：移出 `.claude/settings.local.json`、
  `.superpowers/brainstorm/**`（含 `server.pid` 等运行态）、`.trae/skills/**`，
  以及 `docs/superpowers/` 下的 23 篇实施计划与设计草案。这些是编辑器/Agent 工具的本地状态
  与会话过程稿，不随项目演进维护，却和 `docs/API.md`、`docs/CLI.md`、`docs/USAGE.md`
  混在同一目录里，读者难以分辨哪份仍有效。仍在维护的三份文档不受影响。

## [5.1.0] - 2026-09-18

### 修复

- **流式请求的 token 用量恒为 0**：原样转发流（`/v1/chat/completions` 的流式响应走这一条）
  虽然不改写下游字节，但**必须**顺带抽取 `usage`——少了这一步，所有 chat 流式请求的 token
  用量都会记成 0，指标页看起来像"没有消耗"。非流式、以及会重建流的 messages / responses
  两条路径不受影响，所以这个 bug 只在流式 chat 上表现，容易漏掉。
- **WebUI 概览页二次进入会先白屏再出内容**：改为同步首绘，消除空白等待。

### 新增

- **`amkr` 不带参数现在直接进入 WebUI**：启动服务并在就绪后用系统默认浏览器打开 `/ui`；
  若服务已在运行，则只打开 WebUI 并退出（不再因端口被占而报错）。`--no-open` 可关闭自动打开。
  `--serve-foreground`（服务注册调用）永不打开浏览器。
- **新建配置默认启用 WebUI**（`webui_enabled: true`）。此前默认是关闭的，而终端界面已随 Python
  版退役——也就是说全新安装敲 `amkr` 会打开一个 404 页面。破坏性变更：显式设置了
  `webui_enabled: false` 的配置不受影响，`--no-webui` 仍可关闭。
- **一行安装脚本**：`scripts/install.sh`（Linux / macOS）与 `scripts/install.ps1`（Windows）。
  自动识别系统与架构、从 Releases 下载、**校验 sha256**、装到合适位置并自检版本。
  Windows 无需管理员；目标文件被占用时会给出可执行的提示而不是失败堆栈。

### 工程

- **新增发布流水线**（`.github/workflows/release.yml`）：打 `v*` tag 即走
  「门禁（gofmt / vet / go test / WebUI 探针）→ 六平台交叉编译（CGO_ENABLED=0）→
  sha256 校验和 → GitHub Release」。此前发布是本地手工构建再上传，既容易漏平台，也没有
  门禁前置。另有 `workflow_dispatch` 供演练（只构建、不建 Release）。
- **测试套件现在真正跨平台**：此前有 5 个包的断言里写死了 Windows 平台特征
  （语料内嵌生成机的绝对路径、缓存目录布局、路径分隔符、`shlex` 引号规则），
  在 Windows 上全绿、在 Linux CI 上全红。已改为「语料占位符 + 回放时按平台替换」，
  断言强度不变（篡改期望值仍会失败）。
- README 按当前实现重写。

## [5.0.0] - 2026-09-18

### 重大变更：实现语言从 Python 迁移到 Go

整个项目已用 Go 重写，**Python 实现已退役并从仓库移除**（含 41 个模块与全部 Python 测试）。
发布物由 Python 包（PyPI）改为**单个 Go 二进制**（GitHub Releases）。

- **部署形态**：单个二进制，配置、WebUI 静态资产（`//go:embed`）与 SQLite 指标库驱动全部编在里面，
  运行时不需要 Python、node 或任何额外依赖。
- **数据兼容**：SQLite 指标库 schema 与参照实现逐字节对齐（19 列、7 索引、无 `user_version`），
  可直接沿用既有 `metrics.sqlite3`；已用真实生产库（70 MB）验证 Go 与 Python 读取结果逐项一致。
- **配置兼容**：v4 配置 JSON 直接可用，无需迁移。

#### 破坏性变更

- **终端交互界面（TUI）入口移除**。`amkr` 不带参数时的行为由「进入终端界面」改为
  **前台启动服务**；配置与查看改用 WebUI（`http://127.0.0.1:<port>/ui`）或管理 API。
- **`--show-logs` 移除**。日志查看改由 `GET /api/logs` 与 WebUI 承担。
- **`--update` 与 `--restart-service-after-update` 移除**（自更新功能取消）。
  `--check-update` 保留，但**改为只查 GitHub Releases**。
- **访客 Key 不再需要 `[visitor]` 额外安装**：访客功能现为常驻，`amkr-visitor` 固定 Key 直接可用。
- **安装方式变更**：不再有 `pip install` / `pipx` / `uv tool` 安装路径。改为从源码构建
  （`go build ./cmd/amkr`，需 Go 1.24+）或使用 Docker 镜像。
- **`/health` 的 `version` 字段**会反映新版本号，按版本断言的自建监控需同步。

#### 兼容性保障

迁移采用差分对拍：23 份语料由 Python 参照实现产出并逐字节冻结在 `internal/*/testdata/`
与 `cmd/amkr/testdata/` 中，Go 测试逐条回放。语言退役不等于丢掉凭证——这些冻结语料仍是
兼容性的回归锁。


### Added

- 新增 **embeddings（嵌入）路由**：`POST /v1/embeddings` 走与图像同构的第三种 unified 目标 —— 请求 `unified-model` 时命中 `unified_model.embeddings` 计划（`primary` + 可选 `fallback`），未配置该计划则继承 `default.primary`（不继承 `default.fallback`），其余 Key 路由、重试与熔断语义与既有目标完全一致。上游路径默认 `v1/embeddings`，可按上游 URL 配置 `upstream_routes[base_url].embeddings` 覆盖（别名 `embedding` / `embed`），TUI / WebUI / 管理 API / `--unified-target` 都已能读写该计划。
  - **关键修复**：嵌入请求体此前会被当成 chat 体做协议适配，`{"model": ..., "input": "..."}` 被改写成 `{"model": ..., "messages": [...]}` 发给上游 —— 上游收到一个没有 `input` 的 chat 请求，必然失败。现在嵌入路径只替换 `model`，`input` / `encoding_format` / `dimensions` 等字段原样转发。
  - 原因值得记一笔：路径 → 目标类型的分类原本在 `KeyPool`、`proxy_handler` 里各写了一份 `"image" if path in ("images/generations", "images/edits") else "default"`，加第三类时必然漏一处。现已收敛为 `proxy_support.request_route_kind()` 一份。
- 新增**任务路由**：把任务名（`TASK_XXXXXX`）直接当 `model` 传给 AMKR，由配置里的任务表决定真实模型（首选 + 一个备选）和一组固定采样参数（`temperature`、`top_p`、`top_k`、`frequency_penalty`、`presence_penalty`、`seed`、`stop`，以及 `reasoning_effort`）。调用方无需知道任何模型名，也拿不到改参数的余地：
  - 任务固定的采样参数由 AMKR 覆写；调用方**再传同名参数会被 400 拒绝**，而不是被静默覆盖 —— 静默覆盖会让调用方以为自己传的值生效了。`max_tokens` 等不在白名单内的参数照常透传。
  - `reasoning_effort` 是唯一的例外：它同样被任务覆写，但不拒绝调用方传入（Claude Code / Codex 这类客户端框架会自动带上它，拒绝等于让任务路由不可用）。
  - 首选模型重试后仍返回可重试状态码时，自动切到任务的备选模型，响应带 `X-AMKR-Fallback: true`，与统一模型回退语义一致。
  - 任务名不能与模型 ID、别名、隐藏别名或 `unified-model` 撞名：否则 `resolve_route` 的语义会取决于查表顺序。任务也不接受调用方指定 Key（`TASK_XXXXXX[main]` 会被 400 拒绝），Key 仍由模型自身的路由模式决定。
  - 配置写在顶层可选的 `tasks` 键下（`config_version` 仍为 4，旧配置照常加载）。参数名写错（如 `temprature`）在**保存时**就会报错，不必等到请求时才发现。
  - WebUI 新增「任务路由」页（配置组）用于增删改查；管理 API 新增 `GET/POST /api/tasks` 与 `GET/PUT/DELETE /api/tasks/{task_name}`，带与其它写接口一致的 `config_revision` 乐观并发控制。任务随 `/api/config/export`、`/api/config/import` 一起迁移（导入时引用不到模型的任务会被跳过，与「模型从未配置」一致）。
  - 删除模型时会一并清理引用它的任务：首选模型没了则整个任务删除，只有备选没了则退化为单模型任务。
  - 走原生 Anthropic（`/v1/messages`）时，任务固定的 `stop` 以 `stop_sequences` 发给上游（Anthropic 的叫法，发 `stop` 会被静默忽略）；`reasoning_effort` 在原生路径上不发，与模型级设置一致。
- 新增 `mount_app(host, "/amkr", config, config_path)`：把 AMKR 挂进已有的 FastAPI/Starlette 服务，作为子路径提供整套 OpenAI 兼容接口与管理 API，不必单独起进程。它会额外处理一件容易被忽略的事 —— Starlette **不会**为 `Mount` 的子应用运行 lifespan，直接 `host.mount()` 会静默跳过 AMKR 的启动与收尾（指标广播任务不启动、退出时 httpx client 与 SQLite 连接不关闭）；`mount_app` 把子应用 lifespan 链进宿主，同时保留宿主自己的 lifespan。详见 `docs/API.md`。
- `create_app` 新增 `enable_ops`（默认 `None` 表示跟随配置字段 `ops_enabled`，即 `True`，保持 CLI 行为不变）。运维接口（`/api/logs`、`/api/service/*`、`/api/integrations/*`、`/api/tool`）作用于「服务所在的这台机器」，嵌入到别人的进程里语义不成立，`mount_app` 默认将其关闭。
- 新增 `--no-ops` 与配置字段 `ops_enabled`（默认 `true`），用于**整体**关闭运维接口：容器与反向代理后面，这些接口会读日志文件、启停本机进程、注册系统服务、改写本机 Claude Code / Codex 配置，语义不成立；逐个路径拉黑容易漏（漏一条就等于宿主机被接管），整体关掉才是可断言的做法。关闭后这些路径返回 `404`，`/health` 新增 `ops_enabled` 字段供部署校验。与 `--webui` 一样写入配置文件 —— 后台启动与系统服务的子进程只带 `--config`，开关不落盘就会静默失效。
- `auto_model_key_router` 顶层导出 `create_app` / `mount_app` / `RouterConfig` / `KeyPool`，并附带 `py.typed` 标记。
- 新增容器镜像，发布流程（`.github/workflows/release.yml`）在打完 wheel 后构建镜像并推送到 GHCR（`ghcr.io/sparrived/auto-model-key-router`，标签为版本号，仅正式版额外更新 `latest`）；推送前先在容器里起一次服务、请求 `/health` 校验状态码与版本号 —— 本机不装 Docker 也能发布，但「镜像起不来」必须在发布时而非用户拉取时发现。
  - 镜像步骤排在 `gh release create` **之前**：tag 是那一步才创建的，推送失败时 tag 与 release 都不存在，重跑不会被 `existing_release` 的跳过条件挡住。
  - 配置、指标库、日志与 PID 文件都经 `XDG_CACHE_HOME` 落在 `/data`（已声明为卷），删容器不丢配置；构建上下文由 `.dockerignore` 排除 `router-config.json`、`.env`、`*.sqlite3`、`*.log`，避免把真实上游 Key 打进镜像推到 registry。
  - 容器内以非 root 用户运行，并固定用 `--host 0.0.0.0` 覆盖默认的 `127.0.0.1` —— 不覆盖的话 `-p 8000:8000` 映射不进容器。
  - 构建时用 `--label org.opencontainers.image.source` 把包关联到仓库（值由 `GITHUB_REPOSITORY` 推出，不在 Dockerfile 里写死，换仓库或改名时无需改动）：GHCR 页面会带上仓库信息，也避免「命名空间下已有同名包但未关联仓库」时 `GITHUB_TOKEN` 无权推送。**可见性不随仓库继承** —— GHCR 容器包默认私有，本仓库虽是公开仓库也不代表镜像能匿名拉取，而且没有 API 能改，只能在包页面手动改一次（README 已写明步骤）。
- `create_app` / `mount_app` 新增 `authenticator` 参数（**可插拔鉴权**）：嵌入宿主时，宿主通常已有自己的身份体系（session cookie、JWT、网关身份头），而此前的选择只有两个 —— 把 `local_api_key` 留空等于**整体关闭鉴权**，否则就得让调用方额外再持有一套 AMKR 的 key。钩子为 `async (request, config) -> AuthContext | None`，返回 `None` 即拒绝（401）。`AuthContext.mode` 沿用 `"full"` / `"visitor"` 词表：visitor 不是「权限更小的 full」，而是一套**模型级**规则（只能用 `allow_visitor` 的 Key 与 `amkr-{模型ID}`，不能用内部别名/真实模型 ID/`unified-model`，拿不到 `/metrics` 与管理接口），因此必须显式选择而不能用布尔值表达。钩子同时覆盖 WebSocket：`/ws/events` 的首帧 token 会被折算成 `Authorization` 头交给同一个钩子（宿主的 cookie 本就在握手头里，用 session 鉴权时握手即通过）。不传该参数时行为完全不变。
- WebUI 概览新增**用量热力图**（星期 × 半小时矩阵，紧贴 KPI 瓦片下方整行铺开）：按「请求数量 / Token 数量」切换，一眼看出周内节律（工作日 vs 周末、白天 vs 夜间）与峰值时段。数据固定取最近 7 天、**半小时一格**（7 × 48 = 336 格；不跟随页头的 1h–7d 窗口，否则切到 1h 就失去周内可比性），并给出每格读数与峰值。粒度取半小时而非整点：**列数翻倍后格子边长腰斩**（宽屏下约 55px → 约 25px），整块矩阵的高度也随之减半，不再是一堵占满首屏的色墙；同时半天级的峰（如 10:00 起峰与 10:30 起峰）不再被并进同一格。三种状态刻意区分：**斜纹格**是窗口未覆盖（没数据）、**最浅格**是这半小时确实是 0（没流量）、**描边虚框**是最新那个还在累加的尾桶（天然偏低，不能被读成流量骤降）。落格按 `Asia/Shanghai` 取星期与半小时索引，浏览器时区不同也不会让矩阵平移。热力图走独立请求与 60 秒 TTL，不跟着 10 秒轮询重算 337 个桶。

### Changed

- 流式超时默认值由 `stream_first_byte_timeout=90` / `stream_idle_timeout=180` 下调为 **60 / 60**：单 Key 模型下 `max_retries=2` 意味着最坏要串行等 3 次首字节，按 90 秒计约 270 秒才返回失败，而实测上游健康请求的首字节 p95 已达 64 秒、p99 达 82.5 秒 —— 等待窗口长到既拖慢失败反馈、又让请求堆积。下调到 60 秒后失败能在约 3 分钟内给出（3 × 60），代价是个别本就慢于 60 秒的成功请求会被判超时，可按上游实际情况在 TUI 的 **CLI 设置 → 超时配置** 中调回。
- 依赖补上主版本上界（`fastapi`、`httpx`、`rich`、`tomlkit`、`uvicorn`、`websockets`）：作为别人的依赖项时，这些库的 minor 升级会改行为，不设上限迟早会把宿主一起弄坏。
- WebUI 前端的 API 基址改为按当前页面路径推导，不再写死绝对路径。此前页面能挂在 `/ui/` 只是因为独立运行时恰好同源同前缀，挂到 `/amkr/ui/` 后每个请求都会打到宿主根路径。
- WebUI 看板卡片改为**同排等高铺满**，消除卡片之间的大片空隙：栅格原先用 `align-items: start`，行高由该行最高的卡片决定，矮卡片只占自身那点高度、下方留出一整块空白（例如「响应状态分布」是空态、约 200px，与约 390px 的「请求结果」同排时，下方空出近 200px）。现在列容器撑满行高、卡片再撑满列容器，排内间距恒为间隙值、整排底边天然平齐；卡片内部的富余高度也有处可去 —— 列表/请求流/表格滚动区吸收，竖向内容块居中，页脚用 `margin-top: auto` 钉到底部。
- 概览与活动页的多段栅格合并为一个：分开写时排内间距是 16px、排间却是 `.content` 的 24px，横纵节奏对不上、整体显得松散；`.content` 的间距也改为与栅格同值，页面级纵向节奏与卡片横向间距一致。
- 请求流限高 520px 并内部滚动：它条数随流量增长，不限高会成为整排的高度上限，把旁边的「延迟趋势」一起撑到近千像素、两张图被挤到底部（这也是"间隙突然变大"的实际来源）。
- 卡片内空态不再自带背景与海拔：卡片本身已经是那个「面」，里面再套一层白底描边会像卡片里又嵌了一张卡片。

### Fixed

- 修复 `GET /metrics` 省略 `hours` 时的**无界全表聚合**：默认会聚合全部历史，在本机 16 万行 / 65 MiB 的统计库上实测 3.1–5.2 秒，而 `snapshot()` 与 `record()` 共用 `MetricsStore._lock`，于是这个只读接口会把代理请求路径（每次落库）一起卡住 —— 实测无界查询进行中，一次 `record()` 要等 4.1 秒才拿到锁。现在默认窗口改为最近 `24` 小时（与 `/metrics/requests` 一致），全量改由显式的 `all_history=true` 触发。
- 删除 `proxy_handler._broadcast_metrics()`：它从 `RuntimeResources` 上 `getattr(state, "event_bus", None)`，而 `event_bus` 只挂在 `app.state` 上，该函数因此恒为空操作；真正的节流广播在 `app.py`。留着它是个隐患 —— 一旦有人把 `event_bus` 挂到 `RuntimeResources`，每次上游失败都会在请求路径上同步跑一次无界 `snapshot()`，也就是一次 3–5 秒的全表扫描。
- 修复 `EventBus.broadcast("client_count", ...)` 漏掉 `await`：`/ws/events` 的客户端数量事件从未真正发出（只在日志里留下 `RuntimeWarning: coroutine ... was never awaited`）。
- 修复服务退出时的 sqlite **原生崩溃**（`Windows fatal exception: access violation`，进程直接挂掉）。`MetricsStore` 用 `asyncio.Lock` 串行化连接访问，再用 `asyncio.to_thread` 执行查询；但 `to_thread` 无法取消 —— 任务被 cancel 时 await 立刻抛出、锁随之释放，**工作线程仍在用同一个连接执行 SQL**，随后 `close()` 拿到刚释放的锁并关闭连接，正在查询的线程就踩到已失效的 sqlite 句柄。现在互斥下沉到工作线程（`threading.Lock`）：取消只能中断 await、中断不了线程，而 `close()` 同样要抢这把锁，于是自然排在所有在途查询之后；取消路径也会先等线程收尾再抛出。这个缺陷此前被上面那个漏掉的 `await` 掩盖着（时序恰好错开），修好 `await` 后立即暴露。
- 修复 WebUI 图表读数气泡里多出一行 `null`：原生 `replaceChildren` 会把 `null` 子项字符串化成文本节点 `"null"`，与 `dom.js` 里会过滤空值的 `append()` 行为不同，于是每个**已完结**的点都在读数下多显示一行 `null`（只有「累加中」的尾桶才看不到）。折线图与 Token 堆叠柱两处气泡、以及统一模型编辑表单（路由方式非「固定 Key」时）都改用会过滤空值的 `mount()`。同时补上 `tests/webui_tip_probe.mjs`：用忠实还原 `replaceChildren` 语义的 DOM 垫片驱动真实的 `charts.js`，此前的垫片一律复用会过滤空值的 `append()`，恰好把这个 bug 藏了过去。
- 修复发布只更新 `pyproject.toml` 而不更新 `uv.lock`：uv 把根项目也写进 `uv.lock`，于是打出的 tag 上 `pyproject.toml` 是 4.1.0、`uv.lock` 仍写着 4.0.3，`uv sync --locked` 会直接报 lock 过期；且此后任何 `uv run` 都会把它改回去，工作区永远脏一块。现在改版本号时同步 `uv.lock` 中根项目的 `version`（只改根项目那一处，不碰依赖），并且不调用 `uv lock` —— 只有根项目版本变化、依赖解析结果不变，联网跑 lock 反而可能因索引不可达而失败。
- 修复携带**非 ASCII 凭据**的请求返回 500 而不是 401：HTTP 头是字节、由 Starlette 按 latin-1 解码，因此构造一个非 ASCII 的 `Authorization` 就能把字符串送到 `hmac.compare_digest` —— 它对含非 ASCII 的 `str` 直接抛 `TypeError`（实测可复现）。现在一律比较 UTF-8 字节，非 ASCII 凭据自然地判为不匹配。原先该缺陷被 `_authorization_mode` 的 `==` 掩盖（`==` 不会抛异常），改用 `compare_digest` 时暴露。
- 修复推送失败的处理只认「错误文本里出现 `proxy` / `127.0.0.1`」：`schannel: failed to receive handshake, SSL/TLS connection failed` 这句话两者都不含，于是既不绕过代理、也不重试，一次瞬时网络抖动就把整个发布卡在最后一步，而提交和标签已经建好，留下「已提交已打标签、但没推上去」的半成品状态。现在把连接类失败（代理、TLS 握手、连接被拒/重置、超时、域名解析失败）统一识别：先临时绕过代理试一次，仍失败则退避重试，共 3 轮，并在最终报错里保留原始正文；鉴权被拒这类非连接错误仍不重试。

## [4.1.0] - 2026-09-16

### Changed

- WebUI 从「能看的配置页」重做定位为**监控看板 + 工作台**：新增「概览」与「实时活动」两个监控页，并在应用栏加入实时读数（服务状态、60 秒窗口 RPM/TPM、进行中请求数）。界面仍遵循 `ui.md` 的材料设计 token 与交互物理，但不再沿用它的落地页骨架（Hero、三等分卡片、页脚），改为 12 列栅格 + 卡片构成的密集看板布局；窄屏逐级塌缩为单列。
  - 「概览」：8 张 KPI 瓦片（当前 RPM/TPM、窗口请求、成功率、Token 用量、缓存命中率、平均耗时、平均首字，带迷你趋势与环比）、主流量折线图（可切请求速率/Token 速率/Token 用量/耗时/成功率）、统一模型入口、Token 构成环形图、响应状态分布、请求结果、模型/调用方/上游排行、Token 构成随时间堆叠柱、运行状态。
  - 「实时活动」：实时速率、延迟趋势（耗时与首字两段各自成图，量级差一个数量级时不互相压平）、逐条请求流（真实请求记录，可按成功/失败/重试过滤）、模型与模型/Key 维度的用量分解表（可排序）、上游分布与服务日志。
  - 统计窗口可选 1h/6h/24h/3d/7d，桶宽自动选取（保证不超过后端 500 点上限）。
- 前端引入 `chart-math.js`（纯函数，无 DOM 依赖）承载全部读数口径，使图表数字可以被单元测试锁住：桶内计数一律按 `bucket_seconds` 归一化成「每分钟」（不再假设桶宽是 60 秒）；比率与均值用分子分母分别求和再相除（不是对每桶比率取平均）；`0` 视为真实读数（空闲）而只有 `null` 视为缺口；未完成的尾桶标记 `partial` 并用虚线绘制，避免被读成流量骤降；百分比轴固定 0–100；分母为 0 显示 `-` 而非 `0%`；所有时间戳固定按 `Asia/Shanghai` 渲染，与后端统计口径一致。
- 新增 `icons.js`（内联 SVG 图标）替换此前混用的 `◎ ≣ ⛁` 等字符：这些字符在不同系统上字形差异极大，且无法随文字颜色继承。卡片、表格、徽标、表单控件统一为同一套类词汇（`stat-grid`、`segmented`、`table`、`notice`、`skeleton`、`empty`、`page-head` 等），并提供加载骨架、空态、Toast、对话框与 `prefers-reduced-motion` 兜底。
- 指标轮询按页面分层（监控页 10s/5s，配置页 30s）且不再整块重建页面 DOM：重建会丢掉展开的详情、输入框焦点与滚动位置，对供应商/设置这类工作台页面尤其致命；改由 `onTick` 通知当前页自行重绘。

### Fixed

- 修复发布脚本针对 Windows 文件占用的重试从未生效：`run_command` 靠 `result.stdout/stderr` 里的 `[WinError 5]` 判断是否该重试，但只有 `capture=True` 时这两项才有值，而真正会撞上该错误的两步（`pip install -e .`、`python -m build`）用的都是默认的 `capture=False` —— 于是 stdout 恒为 `None`，标记永远匹配不到，首次失败就直接放弃，`transient_retries=3` 形同虚设，发布卡在「构建分发产物」并留下「版本号已改、但未构建未提交未打标签」的半成品状态（4.0.3 记录的那次修复因此实际从未生效）。现 `capture=False` 且有重试次数时改走 teed 执行：把 stderr 并入 stdout 逐行读取，一边实时回显一边留存输出，标记检测与重试判定都能正常工作；并设置 `PYTHONUNBUFFERED=1` 保持输出顺序（否则 pip 的 stdout 块缓冲、stderr 无缓冲，错误会跑到逻辑上在它之前的那几行前面）。三个既有重试测试全都传了 `capture=True`，恰好只覆盖了唯一能工作的配置，故补上 `capture=False` 路径的回归测试。
- 修复发布脚本构建/可编辑安装前不清理旧 `*.egg-info`：setuptools 用 `NamedTemporaryFile` + `os.replace` 写 `PKG-INFO`，**目标文件已存在**且被杀软或索引器扫到时会抛 `WinError 5`。现在构建前先删掉 `*.egg-info`（构建产物，随时重建），目标不存在时 `os.replace` 只做创建，从源头消除该竞态，而不是仅依赖失败后重试。
- 修复 WebUI 图表的 X 轴时间标签全部渲染为 `-`：`timeTicks()` 只读取原始数据点的 `started_at`，而 `lineChart` 传入的是 `series()` 的产物（时间字段名为 `at`），导致整条时间轴丢失。
- 修复 WebUI 整页白屏：`svg()` 为 SVG 元素赋值 `className` 会抛 `TypeError`（`SVGElement.className` 是只读的 `SVGAnimatedString`，ES 模块处于严格模式），异常在渲染首屏时中断，页面只剩空壳。现 SVG 一律走 `setAttribute`。
- 修复 SVG 轴标签字体未生效：`font-family="var(--font)"` 作为 SVG 表现属性不会解析 CSS 变量，会被当成字面字族名而静默失效；改为由 CSS 统一设置。
- 修复 WebUI 模型路由页「添加目标」时已绑定的 Key 仍出现在候选列表里：模板字符串里的 `${target.key}` 被转义成字面量，去重集合的键与候选键永远不相等，导致重复项可被反复添加。
- 修复 KPI 环比颜色语义：吞吐量类指标（窗口请求）的涨跌本身不分好坏，原先一律「涨=红」会把一次正常高峰标成告警。现由 `trendPolarity` 明确区分「越高越好」「越低越好」「中性」。
- 修复看板同屏数字口径不一致：概览与活动页的 KPI、Token 构成、请求结果、分解表原先一律读全局的「最近 1 小时」快照，而折线图跟随用户选择的统计窗口（可选 1h–7d）。把窗口切到 7 天时，同一屏会出现 7 天的曲线配 1 小时的总量；即使不切窗口，同一屏内也有两个来源的「1 小时总量」并排显示（快照与序列求和），小数位对不上会被当成读数不准。现在两个页面的窗口相关数字统一取自同一次请求取回的窗口快照，序列只负责画曲线与时序图。
- 修复 uv tool 安装方式的自动更新永远失败：`uv tool install "auto-model-key-router==<版本>"` 会把版本锁写进 `uv-receipt.toml`，此后 `uv tool upgrade` 认为当前环境已满足该锁，只更新依赖、不动本体，并**以退出码 0 报「Nothing to upgrade」**。更新器因此把命令误判为成功，却在版本校验中发现仍是旧版本，重试 6 次后报「更新命令在 6 次尝试后仍失败」。现更新前读取 `uv-receipt.toml`：带版本锁时改用 `uv tool install --force`（保留 `[visitor]` 等 extras）重装以清掉锁，未锁版本时仍走 `uv tool upgrade`。
- 修复 WebUI 输入本地鉴权 Key 后仍停在「读取设置失败: AMKR 请求失败（HTTP 401）: 本地 API key 验证失败」：进入页面时只检查 localStorage 里「有没有 Key」就当作已授权，Key 失效（被重置或来自旧版本）时各页面仍带着错误 Key 加载，并把 401 当作业务错误缓存进模块级 `state`，此后每次重绘都重复显示同一条旧报错，且没有任何回到登录卡的入口。现在进入页面会先用一次真实请求校验 Key（只把 401 视为 Key 无效并清除，网络不通则保留 Key 并提示服务未运行），未授权时不加载页面模块；提交的 Key 会先校验成功才保存并整页重载，校验失败会就地提示且不写入 localStorage；会话中途 Key 失效（如重置本地鉴权 Key）时会回到验证页并说明原因。健康轮询不再重建输入框节点，避免清空已粘贴一半的 Key。
- 本地鉴权改为独立的整页验证页：未通过鉴权时不再渲染应用栏、导航与任何页面，整页只有验证页。此前验证卡是渲染在主界面内容区里的，「带着无效 Key 进入主界面」仍有入口——地址栏深链（如 `#/providers`）或浏览器前进/后退可直接落到内页。现在 `authorized` 只在确认服务可访问后置真，任何未通过的路径（含深链、切页、会话中途 401、服务连不上而无法判断是否需要鉴权）都整页回到验证页；连通性也归到这一页处理——连不上时保留已填的 Key 并给出「重试连接」，不再与「Key 无效」混为一谈。验证页页脚显示版本与入口地址，便于确认连的是哪个实例。

## [4.0.3] - 2026-09-15

### Added

- 模型隐藏别名：同一个模型可以在多个名字下调用，但只有本地模型 ID 和 `aliases` 会出现在 `/v1/models` 与 `/health` 中。两种来源：① 自动——每个 target 的 `upstream_model`（上游叫法）自动成为可直接调用的名字，无需逐个登记；② 手动——模型可配置 `hidden_aliases` 列表。隐藏名与真实 ID/别名冲突时以真实名优先；手写的隐藏别名参与重名校验（与模型 ID、`aliases` 及其他模型的隐藏别名冲突都会报错）。管理 API（`/api/models`、`/api/routes`）新增 `hidden_aliases` 字段并在模型响应中额外返回 `auto_hidden_aliases`（自动推导的名字，便于排查）；TUI 模型设置新增「隐藏别名」菜单项，WebUI 模型路由页新增隐藏别名输入框。
- 可选启用的内置 WebUI：资产随软件包一起发布（无构建步骤、无额外依赖），通过配置字段 `webui_enabled` 或 `--webui` / `--no-webui` 启用，也可在 TUI「CLI 设置 → WebUI」中切换；启用后访问 `http://<host>:<port>/ui/`。未启用或资产缺失时 `/ui` 返回 `404`，不影响其他接口。
- 运维 API（均需本地鉴权）：`GET /api/logs`（读取日志尾部）、`GET /api/tool` 与 `POST /api/tool/webui`（版本检查与 WebUI 开关）、`POST /api/service/{action}`（服务启停与自启注册）、`GET /api/integrations`、`POST /api/integrations/{agent}` 与 `POST /api/integrations/{agent}/rollback`（Claude Code / Codex / Pi Agent 接管与回退）。`/health` 新增 `webui_available`、`webui_enabled`、`webui_mounted`、`webui_path` 四个字段。
- WebUI 是预构建的静态资产（原生 ES 模块 + 手写 Material Design 样式），随 wheel 通过 `package-data` 发布，运行时不需要 Node.js。

### Fixed

- 修复发布脚本在「准备发布环境」一步偶发失败：`pip install -e .` / `python -m build` 走到 setuptools 写 `egg-info/PKG-INFO` 时，`NamedTemporaryFile` + `os.replace` 可能被 Windows 上短暂占用的句柄或杀软扫描拒绝，抛 `PermissionError: [WinError 5]` 并以 `subprocess-exited-with-error` 中止发布（与 Agent 配置写入那条同属 Windows 文件占用噪声，区别是这次发生在 pip / build 子进程内部，脚本自身无法重试那次 `os.replace`）。现由发布脚本检测该错误码后小退避重试整条命令（`[WinError 5]` / `[WinError 32]`，用带方括号的 ASCII 片段匹配以适配本地化错误正文，并避免误命中 `WinError 50` 等其它码）。
- 修复发布脚本在中文 Windows 控制台（GBK）下中断：Rich 打印 `✓` / `ℹ` / `⚠` 会抛 `UnicodeEncodeError`，而此时版本号已写入、提交与标签尚未创建，会留下「已改版本但未发布」的半成品状态。现放开发布脚本输出流的编码错误处理（仅 `errors="replace"`，不改变终端实际编码）。
- 修复 Agent 配置写入（Claude Code / Codex / Pi Agent）在 Windows 上偶发 `PermissionError: [WinError 5]` 失败：`os.replace` 可能被杀软扫描或未释放的句柄短暂拒绝，现与配置写入一致地做小退避重试。
- 修复源码树内运行时报出「假版本号」：`__version__` 原先优先读取 `parents[1]/pyproject.toml`，会把发布中断遗留的「已改版本但未发布」状态当成真实版本（并在升级检查中误判为已是最新）。现在已安装的包一律以安装元数据为准，仅未安装（直接从源码运行）时才回退读取 `pyproject.toml`。

## [4.0.2] - 2026-09-05

### Added

- 管理 API 新增按 Key 设置服务模型集：`GET/PUT /api/providers/{provider_id}/keys/{key_name}/models`。PUT 原子同步该 Key 在所有模型上的绑定：按需创建模型并绑定（upstream_model = 模型 ID），取消绑定的模型会移除引用该 Key 的全部 target，空模型级联删除。桌面端可据此复用旧的「勾选模型卡片」交互。

## [4.0.1] - 2026-09-05

### Fixed

- 修复 v3 配置迁移：存在但没有 Key 的空模型池不应被误判为不存在，现会按 v3 白名单语义过滤对应目标并正常完成 v4 迁移。

## [4.0.0] - 2026-09-05

### v4（config_version=4）

- 彻底移除「模型池 (Pool)」抽象：Key 归 `providers.<id>.keys` 管理，模型 `targets[]` 改为 `{provider, key, upstream_model}` 直接绑定供应商 Key（同一 Key 可被多个模型引用，一个模型可绑定多个 Key）。
- 磁盘格式升级到 v4，`unified_model` 改为嵌套结构（`default` / 可选 `image`，各含 `primary` 与可选 `fallback`）；v3/v2/v1 配置在加载时自动、幂等迁移到 v4 并写回；v3 池级探测元数据与旧 v4 供应商级 `capabilities` 会折进该供应商每个 Key 的 `capabilities`（迁移期保守共享同一份探测快照，随后逐 Key 刷新会各自更新）。
- 探测改为每个 Key 独立进行并缓存：添加 Key（无论是否该供应商第一个 Key）都会只探测这个新 Key（`GET /v1/models` 模型清单 + 对 openai/anthropic/responses 路由模式各做一次最小请求），结果写入 `providers.<id>.keys.<key>.capabilities`，同一供应商的不同 Key 可见模型可能不同，探测结果互不复用；手动刷新可在 TUI「供应商 → 刷新能力探测」选择全部 Key 或指定 Key（指定 Key 还可限定端点范围），或调用 `POST /api/providers/{provider_id}/probe` 刷新全部启用 Key。
- 管理 API：删除 pools 系列端点（`/api/providers/{id}/pools/*`）与 `/api/probes/pools`；新增同步的 `POST /api/providers/{provider_id}/probe` 与单 Key 的 `POST /api/providers/{provider_id}/keys/{key_name}/probe`（请求体可带 `modes` 限定路由检查）；provider 响应不再含顶层 `capabilities`，探测缓存移至其 `keys[]` 各项的 `capabilities`（可为 `null`），单 Key probe 响应额外返回 `key` 对象。
- 删除语义：删除 Key 只清理引用它的模型绑定（模型无 target 自动删除；供应商无 Key 自动删除）。
- TUI 改版：供应商菜单（添加 Key / 管理 Key / 刷新能力探测 / Base URL 与路由 / 删除供应商）与模型设置菜单（别名 / 路由模式 / 管理 Key / 绑定 Key / 删除模型）取代「模型池」入口。
- 统计：v4 新调用不再写入模型池归因（`pool_name` 为空），历史池归因仅作为 v3 及更早数据保留。

### Fixed
- v3 → v4 迁移对池白名单的语义与 v3 解析器保持一致：`pool.models` 键存在（含空数组）即按白名单过滤 target 引用，避免 v3 中被静默丢弃的死引用在升级后意外复活（线上 v3 配置实测逐模型等价）。

## [3.3.0] - 2026-08-30

### Added
- align management mutations with TUI

### Changed
- use shared config operations
- centralize mutation operations

## [3.2.9] - 2026-08-21

### Fixed
- link to version-specific PyPI release

## [3.2.8] - 2026-08-20

### Added
- add command to print local authorization key

## [3.2.7] - 2026-08-20

### Changed
- 补充 pipx 与 uv tool 安装后的 PATH 配置、命令定位和临时验证说明，明确“找不到 amkr”通常是终端未刷新或工具 bin 目录未加入 PATH。
- 增加打包元数据回归测试，确保 `amkr` 与 `auto-model-key-router` 两个 console script 始终指向同一个入口。

## [3.2.6] - 2026-08-20

### Added
- 新增 `--show-address` CLI 指令，用于查询 AMKR 的监听 IP、端口和服务地址。
- 推理强度设置新增 `max` 选项，并支持通过配置、管理 API 和 TUI 传递。

## [3.2.5] - 2026-08-09

### Changed
- 支持 Pi Agent unified-model 一键配置，并将模型上下文上限设置为 256k。

## [3.2.4] - 2026-07-19

### Fixed
- disable upstream response compression

## [3.2.3] - 2026-07-19

### Fixed
- 同步模型池与模型路由

## [3.2.2] - 2026-07-18

### Fixed
- 明确模型池 Key 归属错误提示
- 修复模型名模型池迁移冲突

## [3.2.1] - 2026-07-16

### Fixed
- 调整非成功响应日志等级
- 保留 v3 供应商模型池

## [3.2.0] - 2026-07-14

### Added
- 提供持久化统计时间序列和调用明细 API
- 持久化供应商、模型池和上游模型统计归因

### Changed
- 为统计响应补充明确时间窗口并严格校验查询参数
- 内部端点回退和工具过滤重试按实际上游调用分别记账

## [3.1.1] - 2026-07-11

### Added
- 交互修复重复模型池归属
- 添加 Key 时指定唯一模型池
- 保留模型池启用状态并准确回显
- 支持多选项初始勾选状态
- 支持配置流式分段超时
- 在协议流中应用分段超时
- 限制流式首字节与空闲等待
- 添加流式分段超时配置

### Changed
- 更新模型池严格路由夹具
- 添加模型池路由实施计划
- 补充流式超时配置说明
- 补充模型池归属与选择交互
- 明确模型池模型约束路由设计
- 添加流式分段超时实施计划
- 添加流式分段超时设计
- 修正 Key 冷却状态说明

### Fixed
- 完善模型池唯一归属约束
- 按模型池启用模型筛选 Key

## [3.1.0] - 2026-07-11

### Changed
- 更新 Key 内部状态与端点缓存说明
- 内收 Key 健康状态并简化运行时资源

### Fixed
- 仅更新模型调用相关配置

## [3.0.4] - 2026-07-10

### Changed
- y

## [3.0.3] - 2026-07-04

### Fixed
- 校验Windows更新后的版本
- 同步模型池路由并管理Key运行态

## [3.0.2] - 2026-07-04

### Added
- 优化配置项选择交互

### Changed
- 移除旧配置运行时兼容迁移
- 收敛模型配置职责展示

### Fixed
- 优化原生端点探测缓存与旧配置迁移
- 未安装访客扩展时隐藏访客内容

## [3.0.1] - 2026-07-04

### Fixed
- 统一返回语义并保留添加草稿
- 完善模型池启用与删除清理
- 捕获子模块异常并返回主页

## [3.0.0] - 2026-07-03

### Added
- 支持模型池探测与手动模型
- 支持模型池配置与迁移
- 重构供应商模型管理界面
- 支持供应商 Key 配置自动迁移

## [2.2.6] - 2026-07-03

### Changed
- Add key availability probes to TUI

### Fixed
- fix some problems

## [2.2.5.post1] - 2026-06-28

### Changed
- 简化Codex鉴权处理，移除现有令牌保留逻辑

## [2.2.5] - 2026-06-28

### Added
- 重构Codex配置，拆分鉴权到独立auth.json文件
- 完善usage提取逻辑并新增统一模型ID支持

## [2.2.4] - 2026-06-27

### Added
- 完善 Codex 配置支持
- 添加OpenAI图像生成支持

### Changed
- 调整指标快照逻辑，以服务启动时间为起始点

### Fixed
- 正确过滤工具适配中的非函数类型无效工具

## [2.2.3.post1] - 2026-06-21

### Added
- 新增工具错误自动重试，过滤非function工具

## [2.2.3] - 2026-06-21

### Added
- 为Windows更新助手添加可配置的初始等待和重试基础时长
- 为metrics快照添加24小时时间范围参数

## [2.2.2.post3] - 2026-06-21

### Added
- 新增实时指标广播并优化工具适配逻辑

## [2.2.2.post2] - 2026-06-21

### Fixed
- 处理function字典缺失name的情况

## [2.2.2.post1] - 2026-06-21

### Added
- 新增活跃请求数统计并完善流式响应处理

## [2.2.2] - 2026-06-20

### Added
- 新增实时监控与WebSocket事件推送功能

### Fixed
- 为subprocess调用添加显式编码与错误处理

## [2.2.1] - 2026-06-19

### Added
- 新增 `GET/PUT/DELETE /api/unified-model` REST API 端点，支持通过 API 查询、设置和移除 unified-model 配置，`PUT` 支持按模型 ID 或别名指定目标模型及可选 key。

## [2.2.0] - 2026-06-19

### Added
- 新增单个 Key 统计页面，TUI 管理 Key 菜单中可查看指定 Key 的请求量、成功率、Token 用量、延迟等指标，支持时间范围切换和请求明细翻页。
- 新增 `GET /api/models/{model_id}/keys/{key_name}/stats` REST API 端点，返回指定 Key 的统计数据，支持 `hours` 参数过滤时间范围。

### Changed
- 移除缓存命中次数统计（`cache_hits`、`cache_misses`、`cache_hit_rate`），仅保留 token 维度的缓存统计（`cached_tokens`、`cached_token_rate`）；TUI 总览面板「缓存命中」改为「缓存 Tok 比例」。

## [2.1.6] - 2026-06-19

### Added
- `/metrics` 接口新增 `hours` 参数，支持获取指定时间段的监控指标。

### Changed
- Token 数量显示改用 K/M/B 缩写，优化大数值可读性。

## [2.1.5.post1] - 2026-06-19

### Changed
- 修复一些问题。

## [2.1.5] - 2026-06-19

### Added
- 首页新增运行统计面板，显示总请求数、成功率、总 Token、RPM 和 TPM 等运营指标。

### Changed
- 优化首页布局：运行概览与运行统计合并为紧凑两行显示，统一模型信息合并到概览面板，上游原生支持合并到模型路由表格。
- 请求明细表格列顺序调整，缓存列移至输入列后面。
- 请求总览输入 Token 改为显示总量（含缓存），移除总 Tok 行，合并 RPM 和 TPM 为一行。
- 未安装 visitor 时不显示 visitor 相关内容。

## [2.1.4] - 2026-06-19

### Changed
- 将上游路由管理页面的英文文本翻译为中文，统一界面语言。

### Fixed
- 修复 Anthropic 格式输入 token 统计为负数的问题，`prompt_tokens` 现正确包含缓存 token。

## [2.1.3] - 2026-06-19

### Changed
- 根据 `9b129a0`，将 `upstream_routes` 从单个 Key 级配置重构为按上游 `base_url` 分组的全局配置；旧版 Key 级配置仍会兼容读取并提升到对应上游 URL。

### Fixed
- 修复 `upstream_routes` 上游 URL 格式校验错误信息缺少具体无效 `base_url` 的问题，便于定位配置错误。

## [2.1.2] - 2026-06-19

### Added
- 新增上游路由自定义配置 `upstream_routes`，支持分别配置 Anthropic Messages、OpenAI Chat Completions 和 OpenAI Responses 的上游请求路径，并在管理 API、Terminal UI 与 Dashboard 中查看和维护。
- 新增请求缓存亲和路由，轮询 Key 模式可基于 `prompt_cache_key` 或请求内容哈希将同一缓存会话绑定到同一上游 Key，提升 prompt cache 命中稳定性。
- 新增 OpenAI Responses 原生接口探测与失败回退处理，支持按自定义路由缓存原生支持状态并在不支持时回退到兼容转发。

### Changed
- 上游路由配置会自动规范化并补全标准路径前缀；Key 的原生支持状态缓存改为按“上游 URL + 路由路径”维度存储，避免不同自定义路由状态互相污染。
- 优化令牌使用统计，兼容 Anthropic 缓存读取和缓存创建 token 的多种返回格式，并调整日志 TUI 统计表布局。

### Fixed
- 修复 Anthropic 请求转发头处理，改为保留客户端传入的 `anthropic-version`，并透传 `anthropic-beta`。
- 修复请求统计中输入 token 未扣除缓存 token 导致统计偏差的问题。

## [2.1.1] - 2026-06-17

### Added
- 新增 Anthropic 原生 `/v1/messages` 端点自动探测与回退功能，首次请求自动测试上游支持情况，不支持则自动回退到 `/v1/chat/completions` 格式。
- 新增模型配置项 `native_first`，控制是否启用原生优先模式，默认开启；支持持久化存储上游端点支持状态，减少重复探测开销。
- 保留 Anthropic 原生请求字段（如 `prompt_cache_key`、`cache_control`）转发至上游，提升缓存命中率。
- Terminal UI 模型管理新增 `O` 快捷键快速打开配置文件。

### Changed
- Claude Code 配置生成改为在 `env` 中自动添加 `CLAUDE_CODE_ATTRIBUTION_HEADER: false`，禁用 CCH 以避免第三方 API 缓存失效。
- 上游模型探测结果不再自动过滤已存在的模型，批量添加菜单新增跳过选项，避免误覆盖已有配置。
- 更新 API 与使用文档，补充原生优先模式的配置说明。

## [2.1.0] - 2026-06-17

### Added
- 新增上游模型自动探测功能，通过调用兼容 OpenAI 格式的 `/v1/models` 接口获取可用模型列表，支持批量多选添加探测到的新模型。
- 新增 TUI 多选菜单组件，支持带复选框的表格展示、完整的快捷键操作（空格切换选中、A 键全选/取消、上下/翻页导航等）。
- 新增近 1 分钟 RPM 和 TPM 实时统计功能，在 TUI 总览界面展示当前 RPM 和 TPM 数据，默认统计窗口为 60 秒。

### Changed
- Claude Code 配置生成自动添加 `anthropic_attribution_header: false`，禁用 CCH（Claude Code Attribution Header）以避免第三方 API 服务的缓存失效问题。

## [2.0.2] - 2026-06-15

### Added
- 新增模型与上游 key 的 REST 管理 API，支持增删改查、配置 `allow_visitor` 访客可用性、原子持久化和运行时热重载；查询结果仅返回 key 指纹，不暴露上游密钥明文。
- 新增 Key 连续失败自动禁用机制：同一上游 Key 连续 5 次请求失败后会自动标记为禁用并持久化状态，后续请求分发会排除已禁用 Key。
- 新增 Cloudflare 521 上游错误识别，将 521 纳入可重试状态码，并为 OpenAI/Anthropic 兼容错误响应返回结构化错误信息。
- 新增官方 CLI 使用文档、API 接口文档和完整使用指南，覆盖命令行参数、管理接口、安装配置、路由、访客访问、WebSocket、统计与维护流程。

### Changed
- 配置迁移的“粘贴并应用”改为追加模型 Key，不再覆盖目标端已有模型；重复 Key 会跳过，同名的新 Key 会自动生成唯一名称。
- Key 失败冷却时间会随连续失败次数放大，多 Key 路由会优先避开冷却或已禁用的 Key，提升上游故障时的自动切换能力。
- Terminal UI 的模型 Key 列表、管理、复制和排序界面会高亮展示允许访客访问的 Key，并统一访客访问状态展示。
- 官方文档迁移到 `docs/` 目录，README 改为项目概览与文档入口，避免在首页重复维护完整使用说明。

## [2.0.0.post1] - 2026-06-14

### Fixed
- 修复 visitor `/v1/models` 返回 `amkr-{真实模型ID}` 后，代理请求无法将该公共 ID 映射回真实模型而返回 `404` 的问题；visitor 公共路由现在直接基于真实模型 ID 构建，不经过内部别名索引。

## [2.0.0] - 2026-06-14

### Changed
- `/v1/models` 现在要求提供本地或 visitor API key，并按该 Key 的访问权限返回实际可用模型；visitor 列表只包含有权限的 `amkr-` 原始模型 ID，不再暴露内部别名或支持调用 `unified-model`。
- 重构代理请求处理，将请求准备、Key 选择、重试策略、上游调用、流式响应生命周期和错误转换拆分为独立模块，降低 `app.py` 的职责和复杂度。
- 按 Anthropic Messages、OpenAI Responses 和通用请求转换拆分协议兼容层，同时保留原有 `protocol_compat.py` 兼容入口。
- 重构配置写入流程，统一执行校验和原子提交；将系统服务状态采集与 Terminal UI 渲染解耦。
- 将调用指标和 Key 状态持久化移出异步锁与事件循环，减少磁盘和 SQLite 操作对并发请求的阻塞。

### Fixed
- 修复配置热重载期间旧 HTTP 客户端、指标存储和 KeyPool 可能在进行中的请求结束前被关闭的问题；运行时资源现在按代际管理，并在最后一个使用者释放后关闭。
- 修复流式请求在重试、异常或客户端提前断开时可能未统一释放上游响应和所占用 Key 的问题。

## [1.7.0] - 2026-06-14

### Added
- 新增 `/v1/{path}` WebSocket 入口，支持 Trae 等客户端通过 WebSocket 提交 OpenAI-compatible 请求；复用现有鉴权、模型与 Key 路由、失败重试、协议转换及调用统计，并支持流式 SSE 事件和非流式 JSON 响应。
- 增加 `websockets` 运行时依赖，确保 Uvicorn 可以处理 WebSocket 协议升级。

### Changed
- 重构 FastAPI 应用模块，将 Anthropic Messages、OpenAI Responses 请求/响应及 SSE 事件转换迁移到 `protocol_compat.py`，将 WebSocket 握手和帧适配迁移到 `websocket_proxy.py`，精简 `app.py` 并保持原有代理行为不变。

## [1.6.1.post2] - 2026-06-14

### Added
- Terminal UI 的“模型 Key”中新增“模型别称”管理，可查看并添加、编辑、删除模型别称。

## [1.6.1.post1] - 2026-06-14

### Fixed
- 修复跨机器配置迁移时“粘贴并应用”读取运行端系统剪贴板、无法获取本机复制内容的问题；现在导出单行 JSON，并在目标终端中手动粘贴后解析应用。

## [1.6.1] - 2026-06-14

### Added
- 调用统计新增 `local`（本地鉴权）与 `visitor`（访客鉴权）来源分类；`/metrics` 新增 `caller_types` 聚合结果，Terminal UI 调用日志新增“全部调用”“本地调用”和“访客调用”统计页面。旧版 SQLite 统计库会自动补充来源字段，已有记录按本地调用处理。

### Changed
- 配置迁移改为仅复制和应用模型 Key 配置，保留目标端的本地鉴权、监听地址、端口、超时、重试、文件路径及其他 CLI 设置；安装 `visitor` 扩展时会同时迁移各 Key 的访客访问权限，未安装时则忽略该权限。

## [1.6.0] - 2026-06-13

### Added
- 主页“一键配置”新增路由服务、Claude Code 和 Codex 子菜单；可增量写入 Agent 配置，使其通过本项目的 `unified-model` 路由，并缓存应用前的完整配置用于精确回退。
- 新增 Codex Responses 协议兼容，将 Responses 消息、function call、function output 和 tools 转换为 Chat Completions，并把普通及流式文本、工具调用和 usage 转回 Responses 风格。
- 新增 Claude Code `/v1/messages/count_tokens` 本地兼容响应，避免 OpenAI-compatible 上游不支持 Anthropic token 计数接口时中断。

### Fixed
- 修复 Windows 独立更新器将 `uv` 写入标准错误流的成功摘要误判为 `NativeCommandError`，导致升级实际完成却显示失败的问题；现在通过独立进程重定向输出，并以真实进程退出码判断更新结果。
- 修复 Windows 独立更新器接管后父进程已提前退出时，`Wait-Process` 抛出异常并在执行升级命令前中止的问题；现在仅在父进程仍存在时等待其退出。

## [1.5.0] - 2026-06-13

### Changed
- 重构 Terminal UI 为固定窗体式布局，主菜单、选项菜单、Key 排序、运行日志和调用统计统一在备用屏幕中重绘，不再通过追加输出展示交互内容。
- TUI 内容区域支持根据终端尺寸自动调整和滚动，长菜单会自动保持当前选中项可见，并可使用 PgUp/PgDn、Home/End 或 Windows 鼠标滚轮查看被折叠内容。
- 配置编辑中的文本和密码输入改为窗体内输入控件，避免连续操作时终端历史不断累积；终端窗口缩放后会自动重新计算布局。

### Fixed
- 修复终端高度或宽度不足时，TUI 内容被直接截断、选中项移出可视区域以及窄窗口横向超界的问题。

## [1.4.3] - 2026-06-13

### Fixed
- 修复 Linux/POSIX 下调用日志页的单键快捷键和方向键可能需要按 Enter 才生效，以及 raw 模式关闭终端输出处理后可能引发的 TUI 重绘异常；现在首页、选项菜单、Key 排序和日志页会在交互期间统一使用 cbreak 模式，并在退出时恢复终端设置。
- 修复 Windows 独立更新器直接调用更新命令时可能无法稳定记录退出码、错误输出和后续重试的问题；现在通过独立进程等待更新命令完成并读取实际退出码，同时保留标准输出和错误日志。

## [1.4.2] - 2026-06-13

### Fixed
- 修复 `/v1/chat/completions` 等非 Anthropic 转换路径直接按上游网络 chunk 转发 SSE，导致一个 chunk 内多个 `data:` 事件在客户端一次性显示的问题；现在所有 `text/event-stream` 响应都会按完整 SSE event 拆分并逐事件刷新。

## [1.4.1] - 2026-06-13

### Fixed
- 修复 `/v1/messages` 将 OpenAI 流式 `tool_calls` 缓存到消息结束后才转换为 Anthropic `tool_use`，导致 Claude Code 延迟显示工具调用的问题；现在会在首个工具 delta 到达时关闭文本块、立即开始工具块，并逐段转发 JSON 参数。
- 修复同一个上游网络块包含多个 SSE 事件时，下游可能合并发送连续事件、导致 Claude Code 长时间无输出后一次性显示整段内容的问题；现在会在每个转换后的 Anthropic SSE 事件之间主动让出执行权。

## [1.4.0] - 2026-06-13

### Added
- 新增固定虚拟模型 `unified-model`，可引用已有模型和可选 key；调用端无需修改请求模型名，即可通过 `--switch-model`、`--switch-key` 和 `--show-unified-model` 快速切换或查看当前路由。
- `unified_model` 配置变更支持原子写入和服务热加载，并可在 TUI 首页的“统一模型”中选择模型、自动路由或指定已启用 key，同时在 `/health`、`/v1/models` 和配置摘要中展示。
- 新增根路径 `HEAD /` 探活接口，返回 `204 No Content`，便于负载均衡器和托管平台执行轻量健康检查。

### Changed
- 优化 TUI 添加 Key 流程：可直接选择已有模型，并从当前模型、其他模型及默认配置中复用已有上游 URL，仍可按需新建模型或输入自定义 URL。

### Fixed
- 修复 Windows PowerShell 5.1 按本地代码页读取无 BOM UTF-8 更新脚本，导致包含中文提示的脚本可能解析失败、延后更新实际未执行的问题。
- 重构 Windows 自更新流程：不再依赖无确认的隐藏延迟脚本，改为由独立更新器窗口握手接管；更新器会等待文件锁释放、自动重试失败命令、持续写入日志，并在失败时保留窗口显示错误。

## [1.3.7] - 2026-06-13

### Fixed
- 修复 Claude Code 通过 `/v1/messages` 使用工具时，Anthropic `tools`、`tool_use`、`tool_result` 未转换为 OpenAI tool calling，且上游 `tool_calls` 未转换回 Anthropic `tool_use`，导致工具调用被当成文本一次性打印、实际文件未修改的问题。
- 修复 Linux/POSIX 终端下方向键无法用于菜单选择的问题，原因是 Python `BufferedReader` 预读了 ESC 序列的后续字节，导致 `select.select` 检查底层 fd 时超时，将方向键误判为 `ignore`；改为使用 `os.read(fd, 1)` 直接从文件描述符读取，绕过 Python 缓冲层。

### Changed
- Linux/POSIX 平台禁用鼠标滚轮支持，避免部分终端因鼠标模式与键盘输入冲突导致交互异常；相应移除 Linux 下 UI 中的滚轮操作提示。

## [1.3.6] - 2026-06-12

### Added
- 新增 MIT License 文件，并补充 PyPI 包元数据、项目链接、分类器和 README 许可证入口。
- 新增更新后服务重启和 Windows 延迟更新后置命令相关回归测试。

### Changed
- 优化手动更新流程，更新成功后会按当前运行状态自动重启后台/系统服务；从 TUI 发起更新时会退出当前界面，并在 Windows 延迟更新完成后自动重新打开 Terminal UI。
- 调整 Linux/POSIX TUI 返回提示，不再把单独 Esc 作为返回键，改为提示使用 Ctrl+C、q 或 0 等明确按键返回或退出。

## [1.3.5] - 2026-06-12

### Added
- 新增远程终端剪贴板复制支持，检测 SSH 等远程会话时优先通过 OSC 52 向终端发送复制请求，改善无本地图形剪贴板命令的环境体验。
- 新增远程终端剪贴板复制和 POSIX 不完整转义序列相关回归测试。

### Fixed
- 修复 Linux/POSIX 终端下滚轮、方向键、翻页键等 Esc 开头序列在慢终端或不完整输入时可能被误判为返回/退出的问题；Linux TUI 改为使用 Ctrl+C/q/0 等明确按键返回或退出。

## [1.3.4] - 2026-06-12

### Added
- 新增配置迁移 TUI 功能，可一键复制当前配置文件到剪贴板，并在另一个 TUI 中从剪贴板粘贴校验后应用。

### Fixed
- 修复 Windows 下从正在运行的 `amkr.exe` 内执行 `uv tool upgrade` 时，因入口文件被当前进程锁定导致更新失败的问题；现在会等待当前进程退出后继续执行更新。

## [1.3.3] - 2026-06-12

### Fixed

- 修复上游流处理异常时错误被重复抛出的问题，移除 `_stream_upstream` 和 `_stream_anthropic_messages` 中记录错误日志后多余的 `raise`。

## [1.3.3a1] - 2026-06-12

### Added
- 新增 `/v1/messages` 响应适配，将常见 OpenAI Chat Completions 文本响应转换为 Anthropic Messages 风格 JSON/SSE，提升 Claude Code 兼容性。
- 新增 Claude Code 兼容相关测试，覆盖非流式响应转换、流式 SSE 转换、非 JSON 错误包装和 Anthropic 请求头过滤。

### Changed
- 更新请求兼容说明，明确 `/v1/messages` 已支持 Anthropic Messages 风格响应转换，`/v1/responses` 仍为输入兼容。

### Fixed
- 修复 Claude Code 访问 `/v1/messages` 时因收到 OpenAI SSE、`data: [DONE]` 或非 JSON 上游错误页而触发 `API Error: Failed to parse JSON` 的问题。
- 修复转发上游时 `x-api-key`、`anthropic-version`、`anthropic-beta` 等 Anthropic/本地鉴权请求头污染 OpenAI-compatible 上游的问题。

## [1.3.2] - 2026-06-12

### Added
- 新增 POSIX 终端非阻塞字符读取辅助逻辑，并补充终端按键读取与 systemd 服务命令生成相关测试。

### Changed
- 优化 Linux systemd user service 启动命令，优先使用已安装的 `amkr` 控制台脚本，并通过 shell 安全拼接支持包含空格的路径。
- 重构 Terminal UI 按键读取流程，简化 POSIX 终端输入读取与解析逻辑。

### Fixed
- 修复 Terminal UI 对转义序列、鼠标事件和未知输入的处理，避免无效输入被误判为有效按键。

## [1.3.1] - 2026-06-11

### FIXED

- 修复 uv tool 默认安装目录未设置 `UV_TOOL_DIR` 时被误判为普通 pip 环境，导致手动更新调用缺失 pip 的工具环境失败的问题。
- 修复鼠标点击被作为Esc按键处理的问题。

## [1.3.0] - 2026-06-11

### Added
- 新增跨平台剪贴板复制模块，支持自动检测 Windows、macOS、Linux 可用复制命令。
- 新增 Terminal UI 结果页复制能力，可一键复制本地鉴权 key、模型 API key 等指定内容。
- 新增调用日志主菜单入口，便于从 Terminal UI 首页直接查看调用日志。
- 新增系统服务注册状态检测能力，并在自启动管理中展示服务注册与配置状态。
- 新增基于活跃请求数的 key 负载均衡调度，降低多 key 并发请求集中到同一 key 的概率。
- 新增剪贴板、Esc 按键、key 调度、更新命令与服务状态相关测试覆盖。

### Changed
- 优化 Windows 自启动计划任务设置，允许电池模式启动、不因切换电池停止、错过启动后尽快补启，并取消后台服务执行时限。
- 优化安装与更新说明，补充 pipx、uv tool 和 uvx 用法，并在手动更新时按 pipx/uv tool 环境选择对应更新命令。
- 优化手动更新命令生成逻辑，根据当前 pipx 或 uv tool 安装环境自动选择对应更新指令。
- 拆分系统自启管理菜单，优化服务管理交互流程。
- 优化 Terminal UI 主菜单、设置菜单和调用日志页面布局，并完善 Esc 退出提示。
- 优化配置交互流程，仅在新建模型时询问别名、路由模式等初始化配置项。

### Fixed
- 修复 Windows 开机自启动可能受计划任务默认电源策略或执行时限影响而未启动的问题。
- 修复 Windows 终端下 Esc 按键处理逻辑，单独按 Esc 可返回或取消，同时正确处理方向键与翻页键序列。
- 修复 Terminal UI 选项小写快捷键匹配问题。
- 修复 key 资源未正确释放导致负载统计不准确的问题。

## [1.2.4] - 2026-06-10

### Added
- 新增服务日志归档与历史日志列表，启动服务前自动归档非空旧日志，调用日志界面可切换查看历史日志并用默认文本编辑器打开日志文件。
- 新增 Windows 计划任务和 Linux systemd user service 状态详情展示，覆盖注册状态、启动状态、启动命令、原始状态与服务文件。
- 新增 Windows 当前用户登录自启入口，支持非管理员场景下注册 LIMITED 计划任务。

### Changed
- 重构 Terminal UI 菜单，将模型服务、本地鉴权、监听配置、调用日志和版本更新统一收敛到 CLI 设置。
- 优化一键配置与服务管理流程，自动注册系统服务、生成本地鉴权 key，并在结果页展示访问方式和服务地址。
- 优化调用日志界面，支持运行日志/调用统计分页、时间范围切换、日志级别与 HTTP 状态码高亮。

### Fixed
- 调整 Terminal UI 菜单结构、默认选中项与快捷键逻辑，并同步更新相关测试断言。

## [1.2.3rc3] - 2026-06-10

### Added
- 新增 `only_first` 路由模式，仅使用首个 key 并按 `max_retries` 对可重试错误进行重试。
- 新增通过 `模型ID[key name]` 或 `别名[key name]` 显式指定 key 的调用方式。
- 新增交互式维护者发布脚本，支持版本计算、CHANGELOG 归档、敏感文件检查、构建、上传与 GitHub Release 发布流程。
- 新增路由模式、显式 key、发布脚本、请求头过滤和超时策略相关测试。

### Changed
- 增强 Terminal UI 鼠标滚轮支持，并优化菜单、长内容视窗与调用日志滚动体验。
- 优化 README 配置、路由模式、显式指定 key、服务管理和维护者发布流程说明。
- 优化发布脚本对预览版本、稳定版、自定义版本、敏感文件和 Git 代理配置的处理。
- 流式请求超时策略调整为不限制读取阶段，避免长时间流式响应被读超时中断。

### Fixed
- 修复转发上游时 `destination-addr` 请求头导致部分上游拒绝的问题。
- 修复请求兼容转换、超时处理和 Terminal UI 布局相关问题。

## [1.2.2] - 2026-06-09

### Added
- 新增 Terminal UI 长内容滚动视窗，支持 PgUp/PgDn、Home/End 和鼠标滚轮翻阅。
- 新增调用日志鼠标滚轮滚动支持。
- 新增 Terminal UI 滚轮解析与内容滚动测试。

### Changed
- 优化 Terminal UI 标题展示与 README 使用说明。

## [1.2.1] - 2026-06-09

### Added
- 新增关闭 reasoning 选项，并将未设置状态展示为“由下游决定”。

### Changed
- 模型级推理强度配置在非“由下游决定”时会覆盖下游请求中的 reasoning 设置。
- 版本检查调整为优先查询 PyPI JSON API，失败时回退到 GitHub Release。

## [1.2.0] - 2026-06-09

### Added
- 新增 GitHub Release 版本检查、Terminal UI 更新提示和手动更新入口。
- 推理强度配置补充支持 `xhigh`。

## [1.1.1] - 2026-06-09

### Changed
- 优化模型推理强度查找逻辑，避免每次请求遍历模型配置。

### Fixed
- 修复 Linux 发布环境中 Terminal UI 顶层导入 Windows-only `msvcrt` 导致构建失败的问题。

## [1.1.0] - 2026-06-09

### Added
- 新增模型级推理强度配置，支持 `minimal`、`low`、`medium`、`high`、`xhigh`。
- 新增请求级推理强度透传与 Responses 风格 `reasoning.effort` 兼容转换。
- 新增 Terminal UI 推理强度设置入口，并在配置概览中展示推理强度。
- 新增 key 冷却状态持久化与上游健康探测恢复机制。
- 新增监听地址与端口的 Terminal UI 配置能力。
- 新增发布工作流 wheel 烟测与 PyPI 发布联动。
- 新增路由、key 冷却、健康探测和推理强度转发测试。

### Changed
- 多 key 请求失败时优先切换其他 key，单 key 模型才按重试次数重复尝试同一 key。
- 完善 Windows 时区依赖、测试依赖与打包文件查找配置。

### Fixed
- 修复命令行覆盖 host/port 时 RouterConfig 参数不完整的问题。
- 修复后台服务 PID 文件残留时无法重新启动的问题。
