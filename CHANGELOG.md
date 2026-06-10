# Changelog

## [Unreleased]

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
