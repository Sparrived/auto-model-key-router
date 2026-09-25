// Package configeditor 移植 auto_model_key_router/config_editor.py：交互式终端配置
// 编辑器，以及 internal/api 的三条探测路由所依赖的 key 探测逻辑。
//
// 与 config_editor.py 的对应关系（模块级函数 → Editor 方法）：
//
//	form_draft                          → Editor.formDraft
//	service_management_base_url         → ServiceManagementBaseURL
//	load_native_endpoint_states_from_file → LoadNativeEndpointStatesFromFile
//	fetch_native_endpoint_states        → FetchNativeEndpointStates
//	format_visitor_status_text          → FormatVisitorStatusText
//	upstream_routes_for_base_url        → UpstreamRoutesForBaseURL
//	native_endpoint_support_text        → NativeEndpointSupportText
//	discover_upstream_models_result     → Prober.DiscoverUpstreamModelsResult
//	probe_payload_for_mode              → ProbePayloadForMode
//	_probe_error_text                   → ProbeErrorText
//	probe_key_availability              → Prober.ProbeKeyAvailability
//	probe_key_capability                → Prober.ProbeKeyCapability
//	probe_provider_key_capabilities     → Prober.ProbeProviderKeyCapabilities
//	KeyProbeResult                      → KeyProbeResult
//	v2_summary_panel                    → Editor.V2SummaryPanel
//	provider_capabilities_panel         → ProviderCapabilitiesPanel
//	model_key_targets_panel             → ModelKeyTargetsPanel
//	select_provider / select_provider_key / select_v2_model → Editor 上的同名方法
//	*_interactively                     → Editor 上的同名导出方法
//	commit_v2_config                    → Editor.commitV2Config
//
// # 注入接缝
//
// 参照实现靠 monkeypatch 与模块级导入达到两件事，Go 侧都做成显式接缝：
//
//  1. **交互原语**（select_option / prompt_text / confirm_choice / show_result_page
//     / select_multiple / clear_terminal_history / open_config_file / console.status）
//     通过 UI 结构体的函数字段注入，nil 字段回落到 internal/tui 的真实实现。
//     测试据此脚本化输入，与 Python 测试里 `monkeypatch.setattr(config_editor,
//     "select_option", ...)` 等价。
//  2. **探测用的 HTTP 客户端与时钟**通过 Prober 注入，nil 回落到真实网络与进程时钟。
//
// service.py 的 restart_service_after_config_change 由另一个 agent 移植，这里只留
// 接缝：Options.RestartServiceAfterConfigChange 为 nil 时调用点**响亮失败**（返回
// 错误），而不是伪造一个成功面板——与 internal/api 对未接线接缝的处理一致。
//
// # 刻意保留的差异（每条都有具名测试）
//
//   - **访客功能已整体删除**。参照实现用「能否 import itsdangerous」当运行期标记
//     （visitor.py:9-17）并在界面上挂「访客」列与「访客访问」菜单项；访客模式连同
//     那把固定 key 一并取消，改由访问密钥（internal/config 的 AccessKeyConfig）
//     按人分发。上游 key 不再有 allow_visitor 开关，FormatVisitorStatusText 与
//     visitorAvailable 都已删除。
//   - **渲染不追求与 rich 逐格一致**。所有面板/表格都用 internal/tui 的等价节点，
//     信息等价、间距与颜色不同（迁移方案已把富文本降级定为非目标）。其中表格的**表头**
//     用一行普通文本行实现（rich 的 add_column(header) 带样式与分隔线），列名信息保留；
//     panels 的用例比较的正是「信息」（渲染后抽出的词），不是版式。
//   - **异常 → 错误值**。Python 的 AttributeError/KeyError 这类「输入形状不对」的崩溃，
//     Go 侧折算成错误值或按「无此结构」处理（逐处注释）。
//   - **探测请求体是紧凑 JSON**。httpx 的 `json=` 用 `json.dumps` 默认分隔符
//     （`", "` / `": "`）与 ensure_ascii=True；Go 用 canonical 的有序紧凑输出（键顺序
//     一致，分隔符更紧凑）。上游只看语义，故比较的是**解析后的 JSON**，不是字节。
//   - **网络错误文本**。httpx 的 `str(exc)`（如 "timed out"）在 Go 侧是 net/http 的
//     错误文本，两者不可能逐字一致；可依赖的只有错误的存在性、长度上限与中文前缀。
//     脚本化错误（httpx.ReadError(msg) ↔ errors.New(msg)）两边文本相同，可以逐字比。
//   - **表单草稿的作用域**。参照实现的 FORM_DRAFTS 是进程级全局（跨 TUI 会话累积），
//     Go 侧挂在 Editor 上、一个实例一份；同一会话内的行为一致。
//   - **重启接缝少一个参数**。参照实现的 restart_service_after_config_change(path, …)
//     里 path 从未被使用，Go 侧对齐 internal/service 的现有签名（两参数），装配层可直接
//     赋值，见 RestartServiceFunc。
//   - **数字格式化按 Python 的规则复刻**。超时配置的提示用 pythonFloatStr（str(float)），
//     结果页用 formatPythonG（format(v, "g")）——都不是 Go 的 %v/'g'；两者都由具名测试
//     锁定（与 Python 实测逐值一致）。
package configeditor
