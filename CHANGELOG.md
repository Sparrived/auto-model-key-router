# Changelog

## [Unreleased]

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
