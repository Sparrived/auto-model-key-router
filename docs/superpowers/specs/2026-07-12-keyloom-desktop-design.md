# Keyloom 桌面端设计规格

**状态：已确认设计，待实现**  
**日期：2026-07-12**  
**目标平台：Windows 10/11，架构保留 macOS 扩展路径**

## 1. 产品定义

Keyloom 是 Auto Model Key Router（AMKR）的桌面控制面。它为现有本地模型路由服务提供一个 Windows 原生可用、低依赖、托盘常驻的图形界面。

Keyloom 不重写 AMKR 的路由、重试、协议转换、Key 冷却或指标计算逻辑。AMKR 继续作为独立后端服务运行；现有 CLI 命令、配置文件、日志、SQLite 指标库、Windows 任务名和服务 API 保持兼容。

产品名称与后端名称分离：

- 桌面产品：**Keyloom**
- 后端：**Auto Model Key Router / AMKR**
- CLI：`amkr`、`auto-model-key-router`
- Windows 任务名：`AutoModelKeyRouter`

### 1.1 已确认范围

- Windows 优先，后续低成本支持 macOS。
- 首版只管理本机 AMKR 实例；连接层保留 Base URL 抽象，暂不开放远程实例管理。
- 优先兼容已有 CLI 配置和服务；不存在时引导静默安装 AMKR。
- 默认用户级登录自启动；系统级开机服务作为可选高级操作，需要 UAC。
- 关闭主窗口默认隐藏到系统托盘，AMKR 服务继续运行。
- 现有 AMKR 后端不改名；桌面端使用独立品牌和安装目录。

### 1.2 非目标

- 首版不实现远程 AMKR 多实例管理。
- 首版不提供直接编辑 JSON 作为主要配置路径。
- 不在桌面端复制路由核心、协议转换或健康调度逻辑。
- 不为桌面端重新设计一套与 AMKR 不兼容的配置存储。

## 2. 技术路线

采用 **Tauri 2 + React + TypeScript + Rust**：

- React/TypeScript：实现已确认的侧边栏、图表、表单、加载/错误/空状态和悬浮详情。
- Tauri 2：提供窗口、托盘、通知、剪贴板、安装器集成和前端 IPC。
- Rust 宿主：负责服务发现、进程生命周期、Windows 计划任务、UAC、安装/升级/回滚、文件读取和安全边界。
- AMKR：作为独立 Python 服务，通过 HTTP 管理 API 和运行 API 提供能力。

没有选择 Electron，因为首版安装包和常驻内存开销更大。没有选择 WinUI 3，因为 macOS 路径成本高。没有选择 Avalonia，因为动态图表和高保真 Apple 式交互需要额外控件与样式工作。

### 2.1 运行时与安装依赖

正式安装包包含 Tauri 宿主、前端资源和隔离 Python Runtime。用户不需要手动安装 Python、pip、uv、pipx 或 Node.js。

Windows 现代版本通常自带 WebView2；安装器检测缺失状态，按网络可用性提供自动安装或明确引导。AMKR 的私有运行时位于 Keyloom 目录内，不修改系统 Python。

建议目录：

```text
%LOCALAPPDATA%\Programs\Keyloom\
%LOCALAPPDATA%\Programs\Keyloom\runtime\
%LOCALAPPDATA%\Keyloom\
  app-state.json
  install-state.json
  logs\
```

## 3. 系统架构

```text
React/TypeScript UI
        |
        | Tauri IPC
        v
Rust Desktop Host
  |- 服务发现与配置读取
  |- AMKR 进程启动/停止/重启
  |- Windows 托盘、通知、开机启动
  |- 计划任务与 UAC
  |- AMKR 安装、升级、回滚
  `- DPAPI/系统凭据存储
        |
        |- HTTP 管理 API
        `- 本地 AMKR 进程
              |- /health
              |- /metrics
              |- /api/*
              `- /v1/*
```

### 3.1 进程边界

- React 只处理视图状态、用户交互和 API 响应展示。
- Rust 只处理本机资源、生命周期和安全敏感操作。
- AMKR 负责配置校验、路由、重试、Key 冷却、协议转换和指标写入。
- Keyloom 不把 AMKR 进程嵌入 UI 线程；服务启动、停止和日志读取均为异步任务。

## 4. 服务发现与静默安装

### 4.1 发现顺序

Keyloom 启动时按以下顺序发现 AMKR：

1. 读取用户显式选择的配置文件路径（如存在）。
2. 查找 AMKR 默认配置路径。
3. 从已注册 Windows 任务和现有进程参数中解析配置路径。
4. 读取配置中的 `host`、`port`、`local_api_key`、日志路径和指标数据库路径。
5. 使用配置拼出的地址请求 `/health`。
6. 若服务未运行但配置存在，提供启动/修复/注册操作。
7. 只有找不到可用配置时，才进入新建配置和安装向导。

Keyloom 需要一个只读 AMKR 配置解析器用于发现、路径解析和迁移。正常配置编辑必须通过 AMKR 管理 API 完成，避免桌面端复制后端写入逻辑。

### 4.2 状态模型

- `running`：服务健康，已建立 API 会话。
- `installed_stopped`：找到 AMKR 和配置，但服务未运行。
- `not_installed`：找不到可用 AMKR 安装和配置。
- `degraded`：服务可连接但 API/版本/鉴权不完整。
- `incompatible`：后端版本不满足 Keyloom 支持范围，只提供只读诊断和升级入口。

### 4.3 安装流程

用户确认后执行以下步骤：

1. 使用内置 Python Runtime 安装 AMKR wheel 到 Keyloom 私有目录。
2. 创建或复用配置；已有模型和 Key 不得被覆盖。
3. 生成本地鉴权 Key（配置为空时）并以遮罩形式显示。
4. 启动 AMKR 并验证 `/health`。
5. 默认注册当前用户登录启动任务，不请求管理员权限。
6. 可选地通过 UAC 注册现有 SYSTEM 计划任务。

安装、更新和回滚不弹出命令行窗口；界面显示阶段、进度、错误和重试入口。

## 5. CLI 功能映射

| 现有能力 | Keyloom 入口 |
|---|---|
| `--status`、`/health`、服务状态 | 概览页、托盘菜单 |
| `--serve`、`--stop`、`--service` | 服务页 |
| Provider、Pool、Key 管理 | 供应商页 |
| 模型路由、别名、路由模式 | 模型路由页 |
| unified model 切换 | 概览卡片、模型路由页 |
| 请求统计、Token、缓存、延迟 | 概览图表、活动页 |
| 运行日志和调用明细 | 活动页 |
| Claude Code / Codex 配置 | 集成页 |
| 本地 API Key、监听地址、超时 | 设置页 |
| 配置迁移、备份、恢复 | 设置页 |
| 版本检查、更新、更新后重启 | 设置页、托盘菜单 |
| 访客 Key | 供应商/Key 详情页 |
| Key 与原生端点探测 | Provider/Pool/Key 详情页 |
| `--show-config` | 设置页配置摘要 |
| 后台运行与退出 | 托盘菜单、服务页 |

高级诊断页面可显示等价 CLI 命令和 API 响应，但不要求用户使用命令行完成主流程。

## 6. AMKR API 合同

现有接口优先复用：

- `GET /health`
- `GET /metrics`
- `GET /v1/models`
- `GET/POST/PUT/DELETE /api/models...`
- `GET/PUT/DELETE /api/unified-model`

当前配置模型已包含 provider/pool/route 语义。为让桌面端完整覆盖 CLI/TUI，需要在 AMKR 中增加向后兼容的管理接口：

```text
GET/POST/PUT/DELETE /api/providers
GET/POST/PUT/DELETE /api/providers/{id}/keys
GET/POST/PUT/DELETE /api/providers/{id}/pools
GET/POST/PUT/DELETE /api/routes
POST /api/probes/keys
POST /api/probes/pools
POST /api/probes/{probe_id}/cancel
POST /api/config/export
POST /api/config/import
```

接口要求：

- 新接口为增量扩展，不改变现有接口响应。
- 写操作原子落盘并触发热重载。
- 响应永不返回上游 Key 明文，只返回指纹、遮罩值和状态。
- 写操作携带 `config_revision`；冲突时拒绝覆盖并要求重新读取。
- 导入/导出明确区分可迁移 Key 配置和本机监听、鉴权、路径设置。
- 探测操作返回逐 Key、逐端点结果，支持超时、取消和失败原因。

### 6.1 数据刷新

- `/health`：5 秒轮询。
- `/metrics`：15 秒轮询。
- 活动和日志：仅在活动页打开时每 2 秒刷新。
- 写操作成功后立即主动刷新相关查询。
- API 不可用时显示只读缓存和明确的“服务未连接”状态。

## 7. 界面规范

### 7.1 导航

```text
概览
供应商
模型路由
活动
集成
设置
────────
服务状态
```

### 7.2 概览页

- 紧凑统一模型卡片：模型名、自动/固定 Key、启用状态、切换入口。
- 数据总览卡片：请求数、Token、缓存命中率、平均延迟。
- 用量趋势图：请求、Token、缓存视图切换。
- 图表数据点悬浮显示时间、请求数、输入/输出/缓存 Token、成功率和平均延迟。
- 最近活动：请求成功、Key 冷却、探测结果、配置变更。
- 服务状态：运行、停止、启动中、异常、配置不一致。

### 7.3 交互原则

- 所有写操作使用原生表单和确认步骤。
- 破坏性操作二次确认并显示影响范围。
- API Key 默认遮罩；复制操作使用系统剪贴板并给出短暂反馈。
- 安装、探测、更新、迁移等长任务显示阶段、进度、错误和取消入口。
- 所有页面提供加载、空、错误和只读降级状态。
- 关闭主窗口默认隐藏到托盘；托盘提供打开、服务启停、状态和退出。
- 键盘导航、可见焦点、系统高对比度和 Windows 缩放必须可用。

### 7.4 视觉语言

- 浅色默认，深色跟随系统。
- 8px 以下圆角、弱边框、少量阴影。
- 蓝色表示主操作，绿色表示正常，橙色表示注意，红色表示错误。
- 颜色不是唯一信息来源；状态同时有文字或图标。
- 图表只展示真实数据；没有数据时使用明确空状态，不使用装饰性假数据。

## 8. 安全与权限

- 默认只连接 `127.0.0.1`。
- 上游 Key 由 AMKR API 管理；Keyloom 仅显示指纹/遮罩值。
- 本地鉴权 Key 不写入 Keyloom 普通日志。
- Windows 敏感状态使用 DPAPI 或系统凭据存储。
- 当前用户安装和登录自启动不要求管理员权限。
- 系统级服务操作显式触发 UAC；用户取消时保留当前服务状态。
- 配置写入前创建备份；失败时不替换有效配置。
- 远程 Base URL 抽象暂时只作为内部接口，不在首版 UI 暴露。

## 9. 测试与发布

### 9.1 后端兼容性

- 默认配置、非默认配置路径、非默认端口和本地鉴权。
- 旧配置迁移、provider/pool、unified model 和指标数据库。
- CLI 与 Keyloom 交替修改配置时的版本冲突。
- `/health`、`/metrics`、`/api/*`、`/v1/models` 的成功和错误状态。

### 9.2 桌面功能

- 首次安装、已有 AMKR 检测、服务未运行、服务异常、版本不兼容。
- 启动、停止、重启、用户级自启动、托盘操作。
- Provider、Pool、Key、模型路由、统一模型、探测和配置迁移。
- Claude Code/Codex 集成、更新、回滚、备份恢复。
- UAC 取消、网络中断、鉴权失败、配置写入失败。

### 9.3 系统与 UI

- Windows 10/11、100%/125%/150% DPI、深色模式。
- 键盘操作、焦点顺序、基础屏幕阅读器语义和高对比度。
- WebView2 缺失、安装器中断、卸载后配置保留策略。

### 9.4 发布

- 使用 NSIS 生成 Windows 安装包，以支持检测、静默安装、自定义回滚和用户级安装。
- 安装包包含 Tauri 宿主、前端资源和隔离 Python Runtime。
- Keyloom 与 AMKR 使用独立版本号，并维护兼容范围。
- 更新下载到临时目录，校验哈希后替换；失败自动回滚。
- CI 产出签名安装包、校验和、版本说明和 smoke test 报告。

## 10. 分阶段实施

### Phase 1：桌面壳与发现

- Tauri 项目、React 路由、侧边栏和托盘。
- AMKR 配置发现、`/health` 连接、服务状态。
- 连接已有 AMKR，不做安装器完整流程。

### Phase 2：概览与服务控制

- 概览卡片、趋势图、指标悬浮详情、活动摘要。
- 启动/停止/重启、用户级自启动和托盘操作。
- `/metrics` 与日志读取。

### Phase 3：完整配置控制面

- AMKR provider/pool/route API 扩展。
- 供应商、Key、模型路由、统一模型、探测和迁移页面。
- Claude Code/Codex 集成。

### Phase 4：安装、更新与发布

- 内置 Python Runtime 和 AMKR wheel。
- NSIS 安装、静默安装、UAC 系统服务、升级和回滚。
- 签名、CI、Windows 10/11 验收和正式发布。

## 11. 首版验收标准

1. 没有 Python、pip、uv、pipx 的干净 Windows 机器可以完成安装并运行 AMKR。
2. 已有 CLI 用户无需重建配置即可被 Keyloom 发现和管理。
3. CLI 与 Keyloom 可以交替使用，配置、日志和指标保持一致。
4. 所有现有 CLI 功能在桌面界面存在对应入口。
5. 关闭主窗口后服务继续运行，托盘可恢复、启停和退出。
6. 失败安装、更新、UAC 取消和配置写入错误不会损坏已有配置。

## 12. 主要风险与缓解

| 风险 | 缓解 |
|---|---|
| 现有管理 API 未覆盖 provider/pool/route | 先完成增量 API 合同，再实现对应 UI；保留只读诊断 |
| WebView2 缺失或受企业策略限制 | 安装器检测并提供自动安装/离线引导；核心服务不依赖 WebView2 |
| CLI 与桌面端同时写配置 | `config_revision` 乐观并发控制、原子写入和备份 |
| 用户误泄露上游 Key | API 不返回明文、UI 默认遮罩、复制操作短暂反馈 |
| 后端升级导致桌面端不兼容 | 健康检查返回版本，维护兼容范围并提供只读降级 |
| Windows 权限变化 | 默认用户级任务；系统级任务单独 UAC 流程 |
