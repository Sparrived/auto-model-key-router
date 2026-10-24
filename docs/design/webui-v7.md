# WebUI v7：React 重写设计规格

> 本文是新版 WebUI 的唯一设计真相源。实现者（人或代理）在动手前通读第 0–6 节，再读自己负责的页面小节。
> 旧版原生 ES 模块实现保留在 `web/legacy/`（只读参照，不入库），其中的业务口径与注释是**必须继承的约束**——本文只重做交互与视觉，不改业务语义。

## 0. 背景与目标

用户对旧版的三点不满：

1. **交互感弱**：配置页（访问密钥、任务路由、供应商、模型路由）全靠「整页重绘 + 行内展开大表单 + 一排按钮」，状态反馈只有 toast，改名还在用 `window.prompt`。
2. **图表堆砌**：概览一页 Token 出现 4 次、成功/重试出现 5 次；成功率在 5 个页面重复；同一个「成功」一会儿青色一会儿绿色。图表要「美观且直观，适当创新、精致」。
3. **统计筛选单一且有错**：只能按供应商/访问密钥筛；筛选时按 Key 的明细卡直接消失；成本页、工作空间页不认筛选；快照里 `upstream_models`/`providers` 在 v4 数据上恒为空（`pool_name` 恒为 NULL），导致成本排行永远是空的。

同时修掉审查时发现的旧缺陷：成功率/缓存率曲线把 0–1 比值画在 0–100 轴上；成本页从不自动刷新；工作空间页轮询空转；轮询整页重建 DOM 导致悬停提示与滚动位置丢失。

## 1. 技术栈与工程约定

| 项 | 选择 |
|---|---|
| 框架 | React 19 + TypeScript（strict）+ Vite（多页面构建） |
| 服务端状态 | `@tanstack/react-query` v5（轮询、缓存、`placeholderData: keepPreviousData`） |
| 无障碍原语 | `radix-ui`（统一包：Dialog、Popover、DropdownMenu、Tooltip、Tabs、Switch、Checkbox、ScrollArea…） |
| 样式 | Tailwind CSS v4（`@tailwindcss/vite`），设计令牌全部是 CSS 变量（第 2 节） |
| 图标 | `lucide-react`（UI 16px、导航 18px，stroke 1.75）；品牌图标沿用 `brand-icons`（移植为 TSX） |
| 图表 | 自绘 SVG：`d3-scale` / `d3-shape` / `d3-array` 只做数学，渲染全部是 React |
| 命令面板 | `cmdk` |
| Toast | `sonner` |
| 字体 | `@fontsource/ibm-plex-sans`（400/500/600，仅 latin 子集）、`@fontsource/ibm-plex-mono`（400/500，latin）；中文回落系统字体 |
| 测试 | Vitest + jsdom + `@testing-library/react` + `@testing-library/user-event` |

工程布局：

```
web/
  package.json  vite.config.ts  tsconfig.json  vitest.config.ts(可并入 vite.config)
  index.html  login.html  panel.html  guest.html     # 四个入口，路径与旧版一致
  public/favicon.svg
  legacy/                                            # 旧版参照（gitignore，只读）
  src/
    entries/      main.tsx login.tsx panel.tsx guest.tsx
    app/          App.tsx router.ts shell/ command-palette.tsx query-client.ts
    components/ui/        # 原语：button input select switch dialog sheet popover menu tooltip tabs segmented badge empty skeleton kbd copy-button combobox token-input secret-dialog impact-dialog …
    components/charts/    # 第 4 节
    components/filters/   # 第 5 节
    lib/          api.ts types.ts format.ts chart-math.ts status-math.ts pricing.ts model-kinds.ts
                  model-impact.ts endpoint-plans.ts brand-icons.tsx theme.ts storage.ts hooks/
    pages/<page-id>/      # 每页一个目录，index.tsx 导出页面组件，其余拆分子组件
    styles/       index.css tokens.css
    test/         setup.ts + 各 *.test.ts(x)（也可就近放在被测文件旁）
```

构建与发布契约（**不可改**）：

- `vite build` 输出到仓库根的 `webui/`（`emptyOutDir: true`），Go 侧 `//go:embed webui` 原样嵌入；构建产物**提交入库**，因此 `go build` / Docker / `go install` 都不需要 Node。
- `base: "./"`：WebUI 可能挂在 `/ui/` 或 `/amkr/ui/`，资源一律相对路径。四个入口文件名不变：`index.html`（主控制台）、`login.html`（独立鉴权页）、`panel.html`（工作空间可嵌入面板）、`guest.html`（访问密钥使用看板）。
- 静态资源放 `webui/assets/`，文件名带内容哈希；不得生成以 `.` 或 `_` 开头的文件（`go:embed` 会跳过）。
- 路由用 hash（`#/providers?x=y`），服务端没有 SPA 回退。
- 开发：`npm run dev` 起 Vite（5173），把 `/api`、`/metrics`、`/health`、`/v1`、`/ws`、`/ui` 代理到 `AMKR_DEV_TARGET`（默认 `http://127.0.0.1:18900`）。
- 脚本：`dev`、`build`（先 `tsc --noEmit` 再 `vite build`）、`typecheck`、`test`（`vitest run`）、`lint` 可选。

代码约定：

- 函数组件 + hooks；单文件尽量 < 400 行，页面拆子组件。不用 `any`（接口边界可用 `unknown` 再收窄）。
- 注释用中文，**解释为什么**而不是复述代码；从 legacy 移植的口径注释要一并带过来（尤其是三态清单、`null` 与 `0` 的区别、写入顺序、预演确认）。
- 文案：中文、句式自然、主动语态；按钮写清楚会发生什么（「保存任务」「删除供应商」），同一动作在按钮、确认框、toast 里用同一个词。错误说清哪里错、怎么改，不道歉。不要用全大写英文标签，不要在每个标题上方加小标签，**不要用「A · B · C」把一串元信息拼成一行**——元信息用结构化字段或分开的小块呈现。
- 标识符（模型 ID、Key 名、路径、指纹）用等宽字体，其余一律用无衬线。
- 所有交互元素可键盘操作、有可见 focus（2px 环 + 2px offset，颜色 `--focus`）；尊重 `prefers-reduced-motion`。

## 2. 视觉系统：「调度台」

AMKR 是把请求从调用方调度到上游 Key 的路由器。界面像一张安静、精确的调度台：骨架是墨色单色，**颜色只表达数据与状态**。

原则：

1. **色彩即信息。** 按钮、选中、边框、导航一律墨色系；彩色只出现在数据系列与状态灯上，且状态永远配文字或图标，不单靠颜色。
2. **一处醒目。** 每个监控页只有一个主角（概览：路由流向图；用量：拆分趋势；状态：健康矩阵），其余安静。
3. **编辑不离场。** 编辑在侧滑抽屉（Sheet）或行内完成，列表上下文始终可见；轮询只更新数据，不重建正在编辑的表单。
4. **一行筛选管全页。** 筛选条在页首一行，作用于下方全部内容；可从图表/表格一键「仅看此项」。
5. **写入有预演。** 删除与连带改动先预演（`dry_run=1`）再确认；密钥明文只出现一次。

### 2.1 令牌（写入 `styles/tokens.css`，`:root` 为浅色，`[data-theme="dark"]` 与 `@media (prefers-color-scheme: dark)` 下未显式选浅色时为深色）

| 令牌 | 浅色 | 深色 | 用途 |
|---|---|---|---|
| `--paper` | `#f2f3f1` | `#121518` | 页面底 |
| `--panel` | `#fcfcfb` | `#1a1d21` | 面板 / 图表底 |
| `--sunken` | `#f6f7f5` | `#15181b` | 输入框、表头、凹陷区 |
| `--raised` | `#ffffff` | `#22262b` | 弹层、抽屉、对话框 |
| `--rule` | `#e0e2de` | `#2b3035` | 发丝分隔线、网格线 |
| `--rule-strong` | `#c9ccc6` | `#3a4047` | 坐标基线、输入框边框 |
| `--ink` | `#16191d` | `#eceeea` | 主文字 |
| `--ink-2` | `#4f555c` | `#b4b9be` | 次级文字 |
| `--ink-3` | `#6f757c` | `#8a9097` | 弱化文字、坐标轴 |
| `--accent` | `#1f2328` | `#eceeea` | 主操作按钮底、选中条 |
| `--on-accent` | `#fcfcfb` | `#121518` | 主操作按钮字 |
| `--accent-hover` | `#33393f` | `#ffffff` | |
| `--select` | `rgba(22,25,29,.06)` | `rgba(236,238,234,.07)` | 选中行/项底色 |
| `--hover` | `rgba(22,25,29,.04)` | `rgba(236,238,234,.05)` | 悬停底色 |
| `--focus` | `#2a78d6` | `#5598e7` | 焦点环 |

主色可在设置里换（存 `localStorage` 的 `amkr.accent`）：墨（默认，上表）、钴蓝（`#2a5bd7`/`#6d93f0`）、松绿（`#17795e`/`#3fb58f`）、自定义 hex（自动推导 hover 与 on-accent，保证对比度 ≥ 4.5）。主题偏好仍是 `system`/`light`/`dark`，存 `amkr.theme`（与旧版同键，旧值 `custom` 迁移为 `light` + 自定义主色）。`index.html` 内联首屏脚本在 React 挂载前设好 `data-theme`，避免闪烁。

状态色（固定，不随主题换）——「标记」用于点、条、格；「文字」用于文字与图标：

| 状态 | 标记 | 浅色文字 | 深色文字 | 浅色底 | 深色底 |
|---|---|---|---|---|---|
| good 健康/成功 | `#0ca30c` | `#1d6b1d` | `#5fd05f` | `#e6f4e6` | `rgba(12,163,12,.14)` |
| warn 注意 | `#fab219` | `#8a5a00` | `#f5c04a` | `#fdf3dc` | `rgba(250,178,25,.14)` |
| serious 严重 | `#ec835a` | `#a4461f` | `#f09a76` | `#fcebe3` | `rgba(236,131,90,.14)` |
| critical 故障 | `#d03b3b` | `#b42f2f` | `#ec7171` | `#f9e3e3` | `rgba(208,59,59,.16)` |
| neutral 无流量/未知 | `#a2a8ae` | `#6f757c` | `#8a9097` | `#eef0ee` | `rgba(255,255,255,.06)` |

分类色（数据系列，**按固定顺序分配、按实体绑定、不随排名重排**，第 9 个以后并入「其他」）：

| 槽 | 浅色 | 深色 |
|---|---|---|
| 1 | `#2a78d6` | `#3987e5` |
| 2 | `#eb6834` | `#d95926` |
| 3 | `#1baf7a` | `#199e70` |
| 4 | `#eda100` | `#c98500` |
| 5 | `#e87ba4` | `#d55181` |
| 6 | `#008300` | `#008300` |
| 7 | `#6250d6` | `#9085e9` |
| 8 | `#e34948` | `#e66767` |
| 其他 | `#a2a8ae` | `#5f666d` |

（该顺序经 dataviz 校验器验证：相邻对 CVD ΔE ≥ 8。超过 3 个系列时必须有图例并在悬停中直标数值。）

顺序色阶（热力图，单色蓝）：浅色 `#eef4fc → #cde2fb → #86b6ef → #3987e5 → #1c5cab → #0d366b`；深色 `#1b2430 → #184f95 → #256abf → #3987e5 → #86b6ef → #cde2fb`。0 用 `--sunken`，「窗口未覆盖」用 45° 细斜纹，二者必须可区分。

### 2.2 字体与字号

- `--font-sans: "IBM Plex Sans", "Microsoft YaHei UI", "PingFang SC", "Noto Sans SC", system-ui, sans-serif`
- `--font-mono: "IBM Plex Mono", "Cascadia Mono", Consolas, monospace`
- 字号阶：11（坐标轴下限）/ 12（元信息）/ 13（密集表格）/ 14（正文，默认）/ 16（面板标题）/ 20（页标题）/ 28（KPI 值）/ 44（概览主读数，全站唯一）。
- 字重：正文 400、标签 500、标题与数值 600。行高：正文 1.5、标题 1.25。
- 大号独立数字用比例数字；表格列、坐标轴刻度用 `tabular-nums`。

### 2.3 形状、间距、层次、动效

- 圆角按层级区分：芯片/标签 6、控件 8、面板 12、抽屉/对话框 16；筛选芯片 999。
- 面板 = `--panel` 底 + 1px `--rule` 边框，**无阴影**；只有浮层（popover/menu/tooltip）、抽屉、对话框有阴影：`0 12px 32px -12px rgba(16,20,24,.28), 0 0 0 1px var(--rule)`。
- 监控页的 KPI 不是一排独立卡片，而是**一条仪表带**：一个面板内用竖向发丝线分隔若干读数格。
- 间距用 Tailwind 4px 体系；页面内边距 24（≥1280）/ 16（<768）；面板内边距 16–20；面板间距 12。
- 动效：悬停/按压 120ms；抽屉与对话框进出 200ms（位移 + 淡入）；数据刷新时旧图保持并降到 60% 不透明度，不闪骨架；无循环装饰动画。实时流新行进入用 150ms 淡入。

## 3. 应用外壳与数据层

### 3.1 外壳

```
┌──────────────┬─────────────────────────────────────────────────────────────┐
│ ◆ AMKR       │ 概览                    ● 运行中  RPM 12  TPM 4.1K  进行中 2  ⌘K ◐ ⟳ │
│              ├─────────────────────────────────────────────────────────────┤
│ 监控          │ [筛选条（仅监控页）]                                             │
│  ▸ 概览       │                                                             │
│    状态监控    │   页面内容（max-width 1600，左对齐）                             │
│    用量统计    │                                                             │
│    成本       │                                                             │
│    工作空间    │                                                             │
│    账号资源    │                                                             │
│    服务日志    │                                                             │
│ 配置          │                                                             │
│    供应商      │                                                             │
│    模型路由    │                                                             │
│    任务路由    │                                                             │
│    访问密钥    │                                                             │
│    集成       │                                                             │
│ 工具  试验场   │                                                             │
│ 系统  设置     │                                                             │
│ ● 服务运行中 v6.9.1  [«] │                                                   │
└──────────────┴─────────────────────────────────────────────────────────────┘
```

- 侧栏 224px，可折叠为 64px 图标栏（`amkr.ui.sidebar`）；<1024 默认折叠；<768 变为抽屉（菜单按钮、Esc、遮罩、焦点约束、关闭后焦点回到触发按钮）。当前项：`--select` 底 + 左侧 2px `--accent` 竖条 + `aria-current="page"`。
- 页 id 与旧版深链兼容：`overview status activity cost workspaces accounts logs providers routing tasks access-keys integrations playground settings`（`activity` 页标题改叫「用量统计」，同时接受 `#/usage` 作为别名）。未知 id 回落 `overview`。
- 顶栏：页标题（h1）、实时状态带（服务灯 + 文案「运行中/正在连接/服务未连接/服务未运行」+ RPM/TPM/进行中，数据未就绪时不显示数字——不许谎报 0）、⌘K、主题切换（循环 system→light→dark，toast 告知）、立即刷新（invalidate 全部查询）。
- 命令面板（Ctrl/⌘+K）：跳转页面；按名字跳到具体供应商 / 模型路由 / 任务 / 访问密钥（数据取自已缓存的查询）；动作（添加供应商、新建访问密钥、新建任务、切换主题、复制 API 入口地址）。
- 全局快捷键：`g` 后接 `o/s/u/c/p/r/t/k` 跳页（输入框聚焦时不触发）；抽屉内 `Ctrl/⌘+S` 保存、`Esc` 关闭（有未保存改动时先确认）。
- 每页包一层 ErrorBoundary：页面崩溃只显示该页错误与「重新加载此页」，不白屏。
- 版本门槛：`health.version` 低于 4.0.0 时只开放设置页，其余页显示说明（沿用 legacy `app.js` 的 `versionCompatible`）。

### 3.2 鉴权

沿用 legacy `app.js` / `pages/login.js` 的口径：

- 本地 Key 存 `localStorage["amkr.apiKey"]`，请求带 `Authorization: Bearer`。
- 启动：先取 `/health`；若 `local_auth_enabled` 则用 `GET /api/settings` 验 Key。未授权或连不上 → `location.replace(<apiBase>/ui/login.html?next=<当前地址>&reason=…)`，主界面一个节点都不渲染；401 时清掉本地 Key，连不上时保留。
- 会话中途任一接口 401 → 跳登录页并带 `reason=expired`（只跳一次）。
- `apiBase()` 与 legacy `api.js` 完全一致（从 `location.pathname` 里 `/ui/` 之前的部分推出前缀）。

### 3.3 数据层

- `lib/api.ts` 逐个移植 legacy `api.js` 的全部方法（保持 URL、方法、请求体形状与注释，尤其：`updateProvider` 总是带 `max_concurrency`、`updateProviderKey` 原样透传 `null`、`createAccessKey` 不传「不限制」的字段、`updateUnified` 只透传给了的键、`dryRunQuery`、`modelKinds` 分块），再加第 5 节的新接口。导出 `ApiError`（`status`、`detail`、`isConflict`、`isUnauthorized`）。为每个响应写 TypeScript 类型（`lib/types.ts`），字段以 `docs/API.md` 为准。
- 查询键集中在 `lib/query-keys.ts`。健康 5s 轮询；顶栏实时读数 `/metrics?hours=1` 10s；监控页按页面设定 `refetchInterval`（概览 10s、状态 15s、用量 30s、成本 60s、工作空间 30s），**只在页面可见时轮询**（`refetchIntervalInBackground: false`）。切页时由 React Query 自然停止，无需手写清理。
- 刷新保持画面：所有窗口型查询用 `placeholderData: keepPreviousData`；`isFetching && isPlaceholderData` 时面板降透明度，并在面板角落显示细小的进度指示，不出骨架。首次加载才用骨架（形状贴近真实内容）。
- 配置写入：每个配置页从自己的主查询拿 `config_revision`，写接口一律带上。成功后 `invalidateQueries` 相关键并 toast（动词与按钮一致）。**409（版本冲突）**：保留用户的草稿不关抽屉，顶部显示提示条「配置已在别处更新：已重新读取最新配置，请核对后再保存」，同时重新取数并用新 revision 重试由用户点击触发。
- 连带影响：`lib/model-impact.ts` 移植 `hasImpact` 与 `writeWithImpactConfirm`（预演与真写必须是同一调用只差 `dry_run`；`alwaysConfirm` 用于删除；取消 = 一个字节不落盘），确认框用 `ImpactDialog` 组件分节渲染（模型 / 供应商 Key / 访问密钥 / 工作空间 / 任务 / 统一模型 / 端点计划，文案沿用 legacy `model-impact.js`）。
- 一次性明文：`SecretDialog` 组件——醒目说明「只显示这一次」、等宽显示、复制按钮、需勾选「我已保存」才能关闭（Esc 与遮罩不关）。用于新建/轮换访问密钥、新建工作空间（面板 key + 推理 key + 嵌入片段）、轮换推理 key。

## 4. 图表组件（`components/charts/`）

通用规范（全部组件遵守）：

- 尺寸：容器宽度用 `ResizeObserver` 测量，SVG 按真实像素宽绘制（`viewBox` = 实际宽高），首帧在测到宽度后再画，避免缩放闪烁。高度由调用方传入，**含 x 轴标签区**。
- 坐标轴：网格线与基线为 1px 实线（`--rule` / `--rule-strong`），不用虚线；刻度 11px `--ink-3`，`tabular-nums`；y 刻度用 `chart-math.axisScale` 的友好值，≥1000 用紧凑格式（1K/2.5M）；**百分比指标的值域是 0–1，刻度显示为 0–100%**（修复旧版把比值画在 0–100 轴上的缺陷）；x 轴时间标签用 `format.formatAxisTime(value, span)`（北京时区）。
- 标记：线 2px 圆角端点；面积是系列色 10% 不透明度；柱宽 ≤ 24px、离基线端直角、数据端 4px 圆角；堆叠段之间、相邻柱之间留 2px 面板色缝隙（不要描边）；标记点 r=4 带 2px 面板色外环。
- 缺口与尾桶：`null` 是缺读数（断线，两侧都有读数时用更淡更细的虚线「缺口桥」连接，与实测线明显不同），`0` 是真实的 0；最后一个 `complete === false` 的桶用虚线段/斜纹标「累加中」，tooltip 里写明。
- 悬停层（默认就有）：线/面积图用竖向十字线吸附最近桶，一个 tooltip 列出该时刻全部系列（值在前、加粗；系列名在后、弱化；系列用短色线标识）；柱/格/节点以标记本身为命中区（命中区 ≥ 24px），悬停时其余标记降亮度。键盘：图表容器可聚焦，左右键移动游标，tooltip 同步。
- 图例：≥2 个系列必有图例（面积/柱用方块、线用短线），图例项可点击切换系列显隐（仅显示层，不改筛选）；单系列不放图例，标题说明即可。文字永远用文字令牌，不用系列色。
- 每个图表面板右上角有「表格」切换按钮，切到同数据的表格视图（无障碍等价物）。
- 空态：区分「窗口内没有请求」与「接口失败」，文案指出下一步（换窗口、去配置）。

组件清单：

| 组件 | 说明 |
|---|---|
| `TimeSeriesChart` | 单/多系列折线或面积（`variant: "line" \| "area" \| "stacked-area" \| "stacked-percent"`），支持 `cumulative`、参考线（如成功率 95%）、点击某时刻回调 |
| `StackedColumns` | 按时间的堆叠柱（结果构成、按天 Token），段间 2px 缝 |
| `SmallMultiples` | 共享 x 轴的多张小图（如耗时 + 首字），十字线联动 |
| `Sparkline` | 行内迷你趋势（无轴，末点标记，悬停显示值） |
| `RankBars` | 横向排行条（名字 + 值 + 占比），行可点击「仅看此项」 |
| `ShareBar` | 单条 100% 构成条（如 输入/缓存读/输出 Token、成本构成），段内放得下才写标签 |
| `Heatmap` | 星期 × 小时（7×24）或日历热力（长窗口），顺序蓝阶 + 未覆盖斜纹 + 尾桶描边 |
| `HealthMatrix` | 状态页矩阵（行 × 49 格），状态色 + 自定义 tooltip（内容沿用 `status-math.formatHealthTooltip`） |
| `FlowMap` | 分层流向图（桑基），节点 + 流带，悬停一个节点高亮其上下游路径、其余淡出；布局用 `chart-math.sankeyLayout` |
| `Meter` | 额度/剩余比例横条（同色阶浅一档作为轨道） |
| `Legend`、`ChartTooltip`、`ChartFrame`（标题、副标题、右上工具、表格切换、加载态） | 公共件 |

## 5. 全局筛选与新读数接口

### 5.1 筛选模型

- 状态存在 URL：`#/<page>?range=24h&provider=openai&model=gpt-4o&uk=%5B%22openai%22%2C%22main%22%5D&ak=ak-alice&ws=teamA&caller=access_key&outcome=failure`。在监控页之间切换时保留同一组查询参数（「全局」）。
- 维度（维度内 OR、维度间 AND）：时间窗 `range`；供应商 `provider`；模型 `model`；上游 Key `uk`（值为 JSON 数组 `["<provider>","<key>"]`）；访问密钥 `ak`；工作空间 `ws`；调用方类型 `caller`（local/workspace/access_key，显示为 本机 / 工作空间凭据 / 访问密钥）；结果 `outcome`（all/success/failure → `success=true/false`）。
- 时间窗预设：1 小时、6 小时、24 小时、3 天、7 天、30 天、90 天、180 天、1 年、全部历史；自定义「最近 N 小时 / 天」（上限 8760 小时）。各页默认：概览 6 小时、状态 24 小时、用量 7 天、成本 7 天（URL 没有 `range` 时用页面默认）。「全部历史」超过 1 年时标注「仅覆盖最近 1 年」。
- 筛选条（`components/filters/FilterBar`）：`[⏱ 最近 24 小时 ▾] [+ 添加筛选] [模型：gpt-4o、deepseek-chat ×] [结果：仅失败 ×] … [清除全部]`。「添加筛选」弹出维度列表 → 选维度后是可搜索的多选清单，每项显示窗口内请求数（来自 `/ui/metric-dimensions.json`，与配置里的名字合并；配置里已删除但历史上有流量的值照常列出并标「已不在配置中」）；结果维度用三段切换；调用方类型用复选。芯片可点击重新编辑。
- 某页不支持的维度：芯片显示为删除线 + 提示「本页不支持按 X 筛选」，而不是静默忽略。工作空间页只认时间窗；账号资源、服务日志不显示筛选条。
- 「仅看此项」：排行条、拆分表格行、流向图节点、健康矩阵行都提供该动作，把对应维度值加进 URL。

### 5.2 后端接口（以 `docs/API.md` 为准，下列为契约摘要）

三条既有读数 `/ui/filtered-metrics.json`、`/ui/key-usage.json`、`/ui/status-monitor.json` 都接受可重复参数：`provider_id`、`access_key_id`、`model_id`、`upstream_key`（每个值是 JSON 数组 `["provider","key"]`）、`workspace`、`caller_type`，以及可选布尔 `success`。filtered-metrics 与 status-monitor 的 `filters` 回显增加 `model_ids`、`upstream_keys`（`[{provider_id,key_name}]`）、`workspaces`、`caller_types`、`success`。**不传任何维度 = 全实例读数**，因此监控页一律走这些 `/ui/*` 读数，不再分「筛选/未筛选」两条路径。

新增：

- `GET /ui/metric-dimensions.json?hours=|all_history=`：窗口内实际出现过的维度取值（不受筛选影响），`providers/models/requested_models/upstream_keys/access_keys(含配置里的 name)/workspaces/caller_types`，每项带 `requests`。
- `GET /ui/usage-breakdown.json?group_by=&rank_by=requests|tokens&limit=8&hours=|all_history=&bucket_seconds=0&<维度筛选>`：按一个维度拆分的时间序列。`group_by` ∈ `model requested_model provider upstream_model upstream_key access_key workspace caller_type status api_format`。返回 `buckets[{started_at,complete}]`、`groups[{key,stats,series{requests,successes,failures,retries,prompt_tokens,completion_tokens,total_tokens,cached_tokens,duration_ms,first_token_ms}}]`（series 为与 buckets 对齐的**求和**数组）、`other`（前 N 之外合并，可能为 null）、`total_groups`。`upstream_model` 维度按 `(provider_id, upstream_model_id)` 聚合，是成本计算的正确来源（快照里的 `upstream_models` 在 v4 数据上恒为空，**不要再用它算成本**）。

## 6. 页面规格

每页先写「这页回答什么问题」，所有面板必须服务于这个问题；同一指标不要在同一页出现两次。

### 6.1 概览 `overview` —— 现在怎么样？

```
[筛选条]
┌ 实时仪表带 ───────────────────────────────────────────────────────────────┐
│ 请求速率（44px 主读数 + 最近 60 分钟 Sparkline） │ Token 速率 │ 进行中 │ 成功率 │ 平均首字 │ 估算成本 │
└──────────────────────────────────────────────────────────────────────────┘
┌ 路由流向（8/12） ─────────────────────────┐ ┌ 需要关注（4/12） ─────────────┐
│ 调用名 → 路由模型 → 上游 Key 的 FlowMap     │ │ 健康分最低的渠道（≤5 条）、失败   │
│ 线宽 = 窗口内请求数，悬停高亮整条路径         │ │ 原因前三、冷却/停用中的 Key 提示   │
└────────────────────────────────────────┘ └──────────────────────────────┘
┌ 流量与结果（12/12）：StackedColumns（成功/失败，按桶）＋ 下方共享 x 轴的平均耗时小图 ┐
┌ 实时请求流（12/12）：最近 40 条，分段（全部/成功/失败/重试，带计数），悬停暂停自动滚入；行点击 → 请求详情抽屉 ┐
```

- 实时仪表带：RPM/TPM/进行中来自 snapshot 的 `current_rpm/current_tpm/active_requests`（`active_requests` 是全局量，筛选时加注说明）；成功率/首字/成本是**窗口**读数（标注窗口）。成本 = `usage-breakdown?group_by=upstream_model` 各组按 `pricing.ts` 计价后求和，显示已计价比例；无单价显示 `—`，绝不显示 `$0`。
- 路由流向：数据 = `filtered-metrics` 快照的 `model_requested_models`（调用名→路由模型）+ `key-usage` 的 `model_keys`（路由模型→供应商/Key），同一组筛选。节点过多时每层只保留前 8，其余并为「其他」。节点点击 = 「仅看此项」。
- 需要关注：来自 `status-monitor`（同窗口，`group_by=provider_key`），列出 band 为 critical/serious/warn 的行（状态色 + 文字），无问题时显示「所有渠道健康」及检查范围。
- 请求流：`filtered-metrics` 的 `requests.items`（`limit=40`）。行内容沿用 legacy `overview.js` 的 `streamRow`：时间、调用名→路由模型、供应商/Key、流式/非流式与入站格式、来源（本机 / 访问密钥「名称」/ 工作空间）、Token（输入、输出、缓存读写、合计与 tok/s）、结果（状态码、耗时、首字、重试）、成本（无单价 `—`）。失败行左侧 critical 竖条、重试行 warn 竖条，成功不着色。详情抽屉显示全部字段与原始 JSON。
- 删掉旧版的：Token 环形图、四个排行榜、实时脉搏、热力图、状态码卡、统一模型入口卡、运行状态卡（它们分别迁到用量 / 状态 / 模型路由 / 设置）。

### 6.2 状态监控 `status` —— 哪里出问题？

- 仪表带：成功率（<95% critical、≥99% good）、失败次数、平均首字、缓存命中率（≥30% good）、窗口平均 RPM。
- 渠道健康矩阵（主角，12/12）：沿用用户最近确定的口径——**固定最近 12 小时、15 分钟一格、49 列、无横向滚动**，独立请求 `status-monitor?hours=12&bucket_seconds=900`；工具栏两个分段控件：维度（供应商 / 供应商·模型 / 供应商·Key，切换会重新请求）、着色口径（综合 / 错误率 / 首Token / 缓存率，只重新着色不重取）。行左侧：名称（供应商品牌图标 + 文字）+ 行汇总（请求数、成功率、健康分）。格子 tooltip 用 `formatHealthTooltip` 的内容但排版成结构化小卡。行点击 → 渠道详情抽屉：该渠道在当前窗口的成功率/首字趋势（`usage-breakdown` 或 `filtered-metrics` 加该维度筛选）、失败原因、最近失败请求列表、「仅看此项」。图例写明阈值（≥80 健康、50–79 注意、<50 异常、<3 次样本不足、无流量）。
- 健康趋势（7/12）：`TimeSeriesChart`，指标切换（成功率 / 首字 / 缓存率），成功率带 95% 参考线。
- 失败原因（5/12）：`errorReasons(total, failure_status_codes)` 的固定 7 档（顺序固定不按次数排），每档：状态色标记 + 名称 + 次数 + 占失败比 + 细条；底部列出具体状态码分布（迁自旧概览）。
- 模型明细（12/12）：可排序表格（模型、请求、成功率、平均首字、平均耗时、缓存率、RPM），行内 Sparkline（来自 `usage-breakdown?group_by=model`）。
- `status-math.ts` 原样移植（阈值、权重、样本数下限、文案）。

### 6.3 用量统计 `activity`（标题「用量统计」）—— 用了多少、谁在用？

- 仪表带：请求数、Token（下方一条 `ShareBar`：输入 / 缓存读 / 输出）、成功率、日均请求（附峰值日）、平均耗时、平均首字。
- **拆分趋势（主角，12/12）**：`usage-breakdown`。工具栏：拆分维度（模型、调用名、供应商、上游模型、上游 Key、访问密钥、工作空间、调用方、状态码、入站端点）、指标（请求数 / Token / 输入 Token / 输出 Token / 失败数）、形态（堆叠面积 / 堆叠柱 / 100% 占比）。前 8 组 + 其他，颜色按实体固定（同一模型在任何图里同色：在本页内按该维度的稳定排序分配槽位并缓存映射）。下方紧接拆分表：每行 = 一组，列：色标 + 名称、请求、占比条、Token、成功率、平均耗时、行内 Sparkline、「仅看此项」。这一个面板取代旧版的模型用量表、调用方表、按上游 Key 表、按访问密钥表、模型×Key 表与上游分布卡——**筛选时也照常显示**（数据已按筛选收窄）。
- 累计用量（6/12）：`TimeSeriesChart cumulative`（请求 / Token 切换）。
- 用量节律（6/12）：窗口 ≤ 30 天用 星期×小时 `Heatmap`（7×24），更长用日历热力（每格一天）；请求 / Token 切换。
- 按天用量（12/12，窗口 ≥ 3 天）：`StackedColumns` 输入/输出 Token 每天一根，尾日标「累加中」；窗口 < 3 天时改为按小时。
- 性能趋势（12/12）：`SmallMultiples` 平均耗时 + 平均首字（加权，缺口桥说明）。
- 「全部历史」：沿用 legacy `activity.js` 的跨度推导（先取最早记录再折算小时、上限 8760 并标注）——或直接用新接口的 `all_history=true`（服务端按这组筛选最早一条折算），二者择一但同屏所有读数必须同一窗口。

### 6.4 成本 `cost` —— 钱花在哪？

- 仪表带：估算成本（窗口）、日均成本、单次请求平均成本、计价覆盖率（有单价的 Token 占比，附「x/y 个上游模型有定价」）。
- 成本趋势（12/12）：`usage-breakdown?group_by=upstream_model` 每桶按单价计价，堆叠面积按上游模型；无单价的模型不进图，单独在下方列出「未计价模型」。
- 成本构成（5/12）：`ShareBar` 输入 / 缓存读 / 缓存写 / 输出 + 各项金额（同一份 `costParts` 算术）。
- 模型成本排行（7/12）：表格（上游模型、供应商、输入/输出/缓存读单价 $/1M、Token、成本、占比条），可排序。
- 最近请求成本（12/12）：`filtered-metrics` 明细 `limit=50`，同概览请求流的紧凑版，只看钱。
- 页脚：价格来源 models.dev、目录更新时间、过期/失败提示（`pricing.ts` 的 `pricingStatus`）。**估算不是账单**这句话必须出现。
- 成本页要随轮询刷新（旧版从不刷新）。

### 6.5 工作空间 `workspaces`

- 只认时间窗。仪表带 5 格：工作空间数（附最忙）、归属请求、归属 Token、成功率、未归属请求（附说明）。
- 请求流向（12/12）：`FlowMap`（6 层，数据 = `/ui/workspace-usage.json` 的 `links/layers`），请求数 / Token 切换，悬停高亮路径。
- 空间明细（12/12）：可排序表格 + 行内占比条；行点击跳到任务路由页对应空间（`#/tasks?ws=<name>`）。

### 6.6 账号资源 `accounts`

移植 legacy `accounts.js` 的全部口径（服务端代问 CPA、进页不拉管理密钥、一次刷新一次请求、不轮询、额度画**剩余**比例、20%/5% 两档、实例故障隔离），视觉改为：仪表带（实例、订阅、账号、额度告警）+ 每实例一个面板（摘要条 + 工具栏 + 账号卡片网格）。修掉旧缺陷：搜索/筛选状态对所有实例一致生效；刷新不丢搜索框焦点；最近调用条按时间等距（空桶也占位）。实例管理用抽屉表单（行编辑、增删实例），409 提示沿用旧文案。

### 6.7 服务日志 `logs`

日志查看器：级别分段（全部 / 警告以上 / 仅错误）、关键字搜索（高亮）、刷新节奏（2 秒 / 5 秒 / 15 秒 / 暂停）、自动跟随底部（用户向上滚动时自动暂停跟随，出现「跳到最新」按钮）、换行切换、复制单行。等宽字体、级别用状态色左竖条 + 文字标签。离开页面停止轮询。

### 6.8 供应商 `providers`

```
┌ 供应商列表（280） ────┐ ┌ 详情 ────────────────────────────────────────────────┐
│ [搜索]  [+ 添加]      │ │ [品牌图标] openai   https://api.openai.com [复制]  并发 12   [编辑] [⋯] │
│ ◉ openai   3 Key     │ │ ┌ Key ───────────────────────────── [批量探测] [+ 添加 Key] ┐ │
│   deepseek 2 Key     │ │ │ 名称/指纹 │ 启用 │ 服务模型 │ 探测 │ 并发 │ 操作            │ │
│   anthropic …        │ │ └──────────────────────────────────────────────────────┘ │
└──────────────────────┘ │ ┌ 上游路径（折叠摘要：已自定义 2 / 10，点开编辑） ┐         │
                         └──────────────────────────────────────────────────────┘
```

- 列表项：品牌图标、名称、`n Key · m 启用` 不要用中点拼接——写成两个小字段；选中态同导航。深链 `#/providers?id=openai`。
- Key 表：名称（等宽）+ 指纹（复制芯片）、启用开关（行内即时生效，失败回滚并提示）、服务模型（前 3 个模型芯片 + `+n`，点击打开模型绑定抽屉）、探测（上次探测时间与发现模型数，或错误摘要；「探测」按钮带进行中状态）、并发徽标（`并发 5` / `并发不限`）、行菜单（编辑、删除）。
- 添加 Key：抽屉向导 ① 填名称 / API Key / 并发上限 → ② 保存后自动探测（步骤条显示保存 ✓、探测中…）→ ③ 直接进入「选择服务模型」（同模型绑定抽屉的内容）。探测失败不回滚 Key，停在第 ③ 步并显示错误，可手动添加模型。
- 模型绑定抽屉：搜索框、类型标签页（全部 / 文本 / 图像 / … 带计数，来自 `/ui/model-kinds.json`，取不到就不分组）、「全选当前可见 / 清空」、芯片网格（已选高亮；已绑定但本次探测未发现的标 warn 色描边并解释）、行内「添加自定义模型」输入框、底部固定栏显示改动摘要「新增 2 · 移除 1」与「保存模型」→ 走预演确认。端点提示规则沿用 legacy（目录不可用时不显示端点提示）。
- 编辑供应商抽屉：名称、地址、并发上限，折叠区「上游路径」10 个模式（占位符显示默认路径，说明文字沿用 legacy）；删除供应商在行菜单 → 预演确认。
- 添加供应商对话框：预置网格（品牌图标 + 名称，选中态 `aria-pressed`）+ 名称 + 地址；预置只填不提交。
- 批量探测：抽屉或可展开面板，超时输入、开始/取消、进度（已完成/总数）、结果表（Key、端点（去掉 query）、模型数、延迟、错误）。轮询与代次（generation）防串沿用 legacy。
- 并发上限口径与提示文案（`CONCURRENCY_SUM_HINT`、`CONCURRENCY_INVALID`、留空=不限制、`concurrencyLimit` 只认十进制正整数）原样保留。

### 6.9 模型路由 `routing`

- 左列：搜索 + 「unified-model」固定在最上（统一入口徽标、已配端点计划数）+ 路由列表（名称、目标数、路由模式）。深链 `#/routing?id=`。
- 详情（只读）用一张「路由图」表达：左边对外名称（路由 ID + 别名芯片）→ 中间路由模式节点（轮询 / 优先级 / 首 Key / 默认）→ 右边**按优先级编号**的目标列表（供应商图标、供应商 / Key、上游模型名；目标是有序序列，编号有意义）。下方「端点计划」7 族网格：已配置的族显示目标模型与固定 Key，未配置的族淡显「用本路由的目标」。目标行菜单：移到其它路由（对话框选目标路由；先给接收方追加再从来源移除，均不带 endpoints；搬空即删来源，提示文案沿用 legacy）。
- 编辑抽屉：名称、别名（Token 输入框，回车/逗号成芯片）、路由模式（分段控件）、轮询目标（拖拽排序 + 上移/下移按钮 + 移除；「添加目标」= Key 下拉（含探测到的上游模型数）+ 上游模型名输入（候选来自该 Key 的探测结果，留空 = 路由名）；「探测到本路由名称的 Key」快捷添加芯片）、端点计划表（族 → 模型下拉（按类型分组，见 legacy `endpoint-plans.js` 的 `modelSelect`）→ Key 下拉（含「自动路由」））。目标清空后保存 = 删除路由，走预演确认。改名提示会同步改写引用。
- unified-model：只读视图是 7 族计划表 + 默认族的路由方式（自动 / 固定 Key）+ 回退模型 + 推理强度；编辑抽屉按 legacy `routing.js` 的 `unifiedEditor` 全部口径（knownModel 折叠、回退互换、Key 随模型丢弃、七个键每次写全、推理强度先写模型再写计划、saving 复位、错误画在当前抽屉）。「停用统一模型」危险确认。
- 添加模型名称对话框：名称、别名、路由模式、第一条目标（必填）；建好后直接打开编辑抽屉。

### 6.10 任务路由 `tasks`

- 左列：工作空间列表（default 固定在最上并标「默认」、每项任务数、「＋ 新建工作空间」），选中空间存 `localStorage["amkr.workspace"]`（沿用 legacy 的恢复与校正逻辑）。深链 `#/tasks?ws=`。
- 空间头部：名称、凭据说明、操作：模型授权（抽屉，三态：不限制 / 指定清单 / 全部禁止）、轮换推理 key（确认 → SecretDialog）、改名（对话框，替代 `window.prompt`）、删除（确认框写明连带删除的任务数）。默认空间禁用改名/删除/模型授权并在 tooltip 说明原因。
- 任务列表：表格（显示名、任务名（等宽+复制）、首选模型 → 备选模型、固定参数芯片、操作），支持搜索；空态区分默认空间与新空间（文案沿用 legacy）。
- 任务编辑抽屉：任务名（仅新建可填，默认建议下一个 `TASK_XXXXXX` 编号）、显示名称、首选模型（可搜索下拉，含「尚未指定」）、备选模型（排除首选；首选为空时禁用）、推理强度（分段：不固定 none minimal low medium high xhigh max）；「固定采样参数」每个参数一行：开关「固定」+ 数值输入（整数参数校验整数），stop 用 Token 输入；右侧（或下方）实时「调用示例」：curl 与 OpenAI Python SDK 两个标签，带 `X-AMKR-Workspace`（非默认空间时）与任务名，可复制。保存时清空的字段发 `null`（语义见 legacy `tasks.js`）。
- 新建工作空间对话框：名称校验（非空、不能与 default 重名）→ 创建 → SecretDialog 显示面板 key、推理 key、面板嵌入地址（`<origin><webuiBase>/panel.html#k=<key>`）与 iframe 片段，各自复制按钮，并说明两把 key 的分工（沿用 legacy 文案）。

### 6.11 访问密钥 `access-keys`

- 表格：名称（+ ID 等宽小字）、启用开关（行内，乐观更新，失败回滚）、指纹（复制芯片）、供应商范围、模型范围（三态分别显示为「不限制」中性徽标 / 「全部禁止」critical 徽标 / 前 3 项芯片 + `+n`）、24 小时请求数（来自 `metric-dimensions` 的 `access_keys`）、上一次调用（相对时间，悬停显示北京时间绝对值；`null` = 「暂无调用记录」，字段缺失 = 「后端未提供」）、操作（编辑、轮换、查看用量 → `#/activity?ak=<id>`、行菜单删除）。表头上方：搜索 + 状态过滤（全部 / 启用 / 停用）。
- 编辑/新建抽屉：名称、启用开关、（新建时）自定义密钥（留空自动生成）；「权限范围」两段，每段一个三段控件 **[不限制 | 指定清单 | 全部禁止]**（取代旧版的复选框，三态一目了然）；选「指定清单」时出现可搜索清单：供应商清单带品牌图标与模型数；模型清单按「服务它的供应商」分组（组头可全选），别名项显示「别名 · 原模型」，配置里已不存在的名字标 warn 并保留。右侧/下方实时「生效权限预览」：列出此密钥实际能调用的模型及经由的供应商；存在失配（勾了模型但没授权它的供应商）时 warn 提示并提供「一并授权缺失的供应商」按钮。
- 提交体三态必须逐字节正确：POST 时「不限制」不传该字段；PUT 时「不限制」= `null`、「全部禁止」= `[]`、指定 = 排序后的数组。名称为空不可提交。
- 新建成功 / 轮换成功 → SecretDialog。删除 → 危险确认（写明调用方会立刻 401）。

### 6.12 集成 `integrations`、设置 `settings`

- 集成：每个代理工具一张面板（状态徽标、检测到的配置路径、AMKR 入口地址与鉴权状态、应用 / 回滚按钮带确认与进行中状态）。口径沿用 legacy `integrations.js`。
- 设置：左侧子导航 + 右侧分节表单：运行设置（字段与校验沿用 legacy `settings.js`，脏状态时底部出现固定保存条「有 n 项未保存的更改 [放弃] [保存运行设置]」）、本地鉴权（指纹、重置 Key → 确认 → SecretDialog）、服务控制（启动/停止/注册系统服务等，带确认与进行中）、配置迁移（导出 / 导入，导入前预览摘要再确认）、版本与更新（检查更新、自更新）、外观（主题 + 主色，即时预览）、关于（运行状态：启动时间、数据库路径、计数口径——迁自旧概览「运行状态」卡）。

### 6.13 试验场 `playground`

功能完整移植 legacy `pages/playground.js`（文本对话流式/非流式、思考内容、参数面板、示例提示、工作空间选择、图像生成与历史、请求/响应载荷检视与复制）。布局三栏：左参数（可折叠）、中对话/画廊、右载荷检视（可折叠）。流式输出逐字渲染不卡顿，可中止。

### 6.14 独立入口

- `login.html`：标题「连接到 AMKR」，Key 输入（密码型、可显示）、「连接」按钮、连接状态（服务未连接时说明并自动重试）、`reason=expired` 时提示原 Key 已失效；成功后写 `amkr.apiKey` 并 `location.replace(next)`（`next` 只接受同源路径）。口径沿用 legacy `pages/login.js`。
- `panel.html`（工作空间可嵌入面板）：安全边界不可动——凭据**只**从 URL fragment（`#k=`）取，绝不读写 localStorage，绝不发 `X-AMKR-Workspace`（空间由 key 决定）。功能移植 legacy `panel.js` / `panel-api.js`（任务列表与编辑、用量流向、任务用量、上游模型用量、复制面板地址）。视觉用同一套令牌，适配 iframe（无侧栏）。
- `guest.html`（访问密钥看板）：安全边界不可动——只用自己的存储键（见 legacy `guest-api.js`），绝不读写 `amkr.apiKey`，绝不发写请求、绝不展示明文 key；401（请重新输入密钥）与 403（已被停用，联系管理员）给不同指引。功能移植 legacy `pages/guest.js`。

## 7. 测试与验收

- 纯函数：把 legacy 探针里对 `chart-math`、`status-math`、`pricing`、`model-kinds` 的断言改写为 Vitest 单测（同样的输入输出）。
- 契约：`api.ts` 的请求形状（tri-state、`max_concurrency: null` 透传、`updateUnified` 键集合、筛选参数重复出现与 `upstream_key` JSON、`dry_run`）；`writeWithImpactConfirm` 的顺序（先预演、取消不落盘、无影响不弹框）；访问密钥提交体三态；panel/guest 的凭据与请求头边界；登录跳转（`next`、`reason`）。
- 组件：关键交互用 Testing Library 驱动（访问密钥三态切换 → 提交体；任务编辑保存 → 请求；模型绑定改动摘要）。
- 验收：`npm run typecheck && npm test && npm run build` 全绿；用临时配置 + localhost 假上游在真实浏览器检查 360 / 768 / 1440 / 1920 宽度、深浅色、键盘操作抽屉与对话框、各页刷新不丢编辑状态。
