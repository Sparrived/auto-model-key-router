# Changelog

## [Unreleased]

## [1.4.2] - 2026-06-13

### Fixed
- 修复 `/v1/chat/completions` 等非 Anthropic 转换路径直接按上游网络 chunk 转发 SSE，导致一个 chunk 内多个 `data:` 事件在客户端一次性显示的问题；现在所有 `text/event-stream` 响应都会按完整 SSE event 拆分并逐事件刷新。

## [1.4.1] - 2026-06-13

### Fixed
- 修复 `/v1/messages` 将 OpenAI 流式 `tool_calls` 缓存到消息结束后才转换为 Anthropic `tool_use`，导致 Claude Code 延迟显示工具调用的问题；现在会在首个工具 delta 到达时关闭文本块、立即开始工具块，并逐段转发 JSON 参数。
- 修复同一个上游网络块包含多个 SSE 事件时，下游可能合并发送连续事件、导致 Claude Code 长时间无输出后一次性显示整段内容的问题；现在会在每个转换后的 Anthropic SSE 事件之间主动让出执行权。

## [1.4.0] - 2026-06-13

### Added
- 新增固定虚拟模型 `unified_model`，可引用已有模型和可选 key；调用端无需修改请求模型名，即可通过 `--switch-model`、`--switch-key` 和 `--show-unified-model` 快速切换或查看当前路由。
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
