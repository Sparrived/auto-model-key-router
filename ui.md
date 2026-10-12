STYLEKIT_STYLE_REFERENCE
style_name: 暖色仪表盘
style_slug: warm-dashboard
style_source: /styles/warm-dashboard

# Hard Prompt

## 什么时候用
当你为 AMKR（Auto Model Key Router）维护 WebUI、登录页、工作空间面板或访问密钥看板时使用。它是一套面向本地运维工具的高密度暖色数据工作台规范，不是营销站点。

## 核心方向
- 采用「A 赤陶配色 × B 独立卡片布局」：赤陶画布承载开放空间，奶油色卡片承载数据，导航作为独立浮动面板。
- 先保证数据口径、权限、可读性和键盘操作，再追求视觉表达；禁止为了改样式删除指标、接口、错误信息或配置影响确认。
- 保留原生浏览器 ES modules、`h()`/`svg()` DOM 构建方式和现有 API；不要引入 React、Tailwind、打包器或新的运行时依赖。

## 绝对禁止
- 不使用 Material 紫/青配色、蓝紫渐变、霓虹色、纯黑大面积背景或营销式 hero 区。
- 不使用全站粗重边框（边框最多 1px；键盘 focus outline 可为 2px）。
- 不把所有信息塞进一张巨型面板，不使用固定底部导航，不用横向标签栏替代主导航。
- 不用颜色单独表达成功、警告或失败；必须同时有文字、图标、状态条或形状差异。
- 不把未知成本显示成 0；未知值使用 `—` 并说明目录/匹配原因。
- 不打印或展示完整 API key、推理密钥或 Authorization；页面与日志只显示脱敏尾部。
- 不为了适配窄屏隐藏不可逆的重要字段；允许折叠，但必须有 title、详情或可滚动替代。

## 色彩令牌
```css
--ui-canvas: #c9967a;          /* 页面开放画布 */
--ui-surface: #faf8f5;         /* 主卡片 */
--ui-surface-soft: #f2eae2;    /* 次级背景、输入区 */
--ui-surface-emphasis: #edc5ae;/* 选中/强调卡片 */
--ui-text: #49392f;            /* 主文字 */
--ui-text-muted: #7b6558;      /* 辅助文字 */
--ui-text-on-canvas: #423127;  /* 赤陶画布上的文字 */
--ui-text-selected: #854332;   /* 选中/链接文字 */
--ui-focus: #423127;          /* 键盘焦点环 */
--ui-action: #a34f3d;          /* 主操作 */
--ui-action-hover: #8e4434;
--ui-on-action: #fff8f2;
--ui-divider: #e8ddd4;
--ui-input-outline: #977b68;
--tone-good: #596044; --tone-good-bg: #e7eadf;
--tone-warn: #866022; --tone-warn-bg: #f3e6c9;
--tone-bad: #923f38;  --tone-bad-bg: #f5ddd6;
--tone-info: #854332; --tone-info-bg: #f2eae2;
--chart-primary: #ba725c; --chart-secondary: #717859;
--chart-tertiary: #9b754f; --chart-neutral: #8c6854;
```

色彩要求：主文字与奶油表面对比度至少 4.5:1；画布上的文字使用 `--ui-text-on-canvas`；图表使用分类色板而不是操作色；成功、警告、失败使用对应语义令牌。

## 布局与间距
- 页面外壳在宽屏为 `导航 232px + 开放工作区`，两者间距 24px；导航为独立奶油色圆角面板，视口内滚动。
- 工作区顶部保留轻量面包屑、服务状态和刷新操作；不要做厚重固定顶栏。
- 卡片使用 20–24px 圆角、20–24px 内边距和柔和暖色阴影；只有可进入/可点击卡片在 hover 时提高阴影。
- 统一使用 8/20/24/32/40/48px 节奏；12 列网格用于分析卡片，KPI 网格明确指定列数，禁止孤儿卡片。
- 概览信息层级：四张主 KPI → 8:4 主趋势区 → 六项次级读数带 → 全宽请求流 → 深入分析。
- 断点至少验证 360、768、1024、1440、1920px；<=860px 主导航改为左侧抽屉，不能变成横向主导航。

## 交互与无障碍
- 所有按钮有可见 hover/active/focus 状态；focus-visible 使用高对比 outline，不能依赖颜色变化 alone。
- 抽屉导航支持菜单按钮、Escape、遮罩点击、Tab 循环、焦点恢复和 `aria-expanded`；打开抽屉时工作区 inert。
- 对话框使用 `role=dialog`、`aria-modal`、初始焦点、Tab 循环、Escape 和关闭后焦点恢复。
- 刷新轮询只更新状态槽位或页面数据，不重建正在编辑的表单，不丢失展开、焦点、草稿、滚动和筛选状态。
- 尊重 `prefers-reduced-motion`；动画只用于状态变化和轻量反馈，不使用循环装饰动画。

## 页面要求
- 概览保留 RPM、TPM、请求、成功率、重试率、Token、缓存、耗时、首字、成本、趋势、热力图、请求流、结果/Token 构成、状态码、排行、成本和运行详情。
- 供应商、路由（左栏第一项固定是 `unified-model`，统一模型的配置就在这里，不再有单独页面）、任务、访问密钥、设置页保留现有 API、版本修订检查、影响预演和确认流程。
- 登录页只负责本地鉴权，不能在主界面渲染失效凭据表单。
- `panel.html` 是工作空间凭据面板，`guest.html` 是访问密钥使用看板；两者都不是已删除的旧 visitor UI，必须继续可达。
- 密钥、面板和访问密钥页只展示脱敏凭据；空数据、无权限、服务未连接和后端版本不兼容都要有明确说明。

## 图表与数据口径
- 图表轴标签至少 11px；数据缺口、未完成尾桶和零值必须可区分。
- 热力图用同一赤陶明度阶表达强度；缺覆盖窗口使用斜纹，不能伪装成 0。
- 图例、tooltip 和辅助文字说明窗口、桶宽、单位和是否为估算值。
- 费用来自外部价格目录时明确目录状态和匹配数量；未知费用显示 `—`。

## 交付前检查
1. `node scripts/webui_module_check.mjs` 通过，且不能只把 FakeNode 检查当作用户验收。
2. `node webui/probes/webui_layout_probe.mjs` 通过，所有断点没有空轨/孤儿 KPI。
3. 用临时配置、临时 SQLite、localhost 假上游和独立浏览器 profile 派发子代理模拟真实用户操作；不得读取生产凭据或调用真实上游。
4. 实际浏览器至少检查登录/鉴权、主导航切页、概览筛选、抽屉键盘操作、配置对话框关闭与焦点恢复、panel/guest 入口和 360px/1440px 画面。
5. 提交采用 Conventional Commits：类型与 scope 使用英文，描述、正文和脚注使用中文，例如 `feat(webui): 重构暖色数据工作台`。
