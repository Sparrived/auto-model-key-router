package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configservice"
	"github.com/Sparrived/auto-model-key-router/internal/health"
	"github.com/Sparrived/auto-model-key-router/internal/updatecheck"
)

// 本文件移植 auto_model_key_router/ops_api.py（276 行、7 条路由）——「运维面」。
//
// 为什么落在 internal/api 而不是单独包：参照实现把运维面注册到**同一个** FastAPI
// 应用上（app.py:144-145），复用同一套鉴权门、错误形状与 JSON 写出；Go 侧全-auth
// 门（authorizedConfig）、错误→状态映射（writeError）与 JSON 序列化都是本包的私有
// 实现，另起一个包只会逼着把这些内部件导出或复制一遍。因此运维面与管理面共用同一个
// Server 与同一个 ServeMux，与 Python 的结构一一对应。
//
// 与参照实现的三条结构化差异（都刻意保留，且在本文件对应位置单独说明）：
//
//  1. **依赖未移植的接缝**。service.py 与 agent_config.py 尚未移植，对应路由的状态
//     与文案由 Server 上的注入接缝决定；接缝为 nil 时相关 handler 返回 500 + 明确
//     文案，绝不静默成功（见各 handler 的说明与 handlers_ops_test.go 的具名用例）。
//  2. **文件系统错误的文本**。ops_api.py:153 把 `str(exc)` 塞进 error 字段；CPython
//     的 errno 文案（"[Errno 13] Permission denied: ..."）无法在 Go 里逐字复刻，因此
//     Server.LogTail 接缝让测试注入同一段文本，真实运行时的文案是 Go 的
//     `*os.PathError` 文本。状态码、字段形状与「不 500」的契约不变。
//  3. **rich 渲染的纯文本**。参照用 Console(record=True) 把 Panel/Group 渲染成纯文本
//     （ops_api.py:73-90）；迁移方案已把「富文本降级为等价纯文本」定为不透明字符串，
//     由 RunServiceAction 接缝的实现方决定。

// opsLogTailBytes 对应 ops_api.py:46 的 LOG_TAIL_BYTES。
const opsLogTailBytes = 65536

// webUIPath 是 WebUI 的固定挂载前缀（webui.py:19）。
//
// 与 internal/health、internal/webui 各有一份同值常量：它在三处是**冻结契约**的一部分
// （/health 与 /api/tool 都要汇报同一个路径），不引入跨包依赖以免装配顺序影响取值。
const webUIPath = "/ui"

// OpsServiceTarget 描述一个服务动作在 amkr 内部的分派目标，对应 ops_api.py:50 的
// _SERVICE_TARGETS 的值。
type OpsServiceTarget struct {
	// Kind 是动作类别："background-start" / "background-stop" /
	// "background-restart" / "system"。
	Kind string
	// Argument 是 Kind 为 "system" 时传给 manage_system_service 的子命令。
	Argument string
}

// OpsServiceTargets 是桌面端服务动作名到内部分派目标的映射，逐条对应
// ops_api.py:50-62 的 _SERVICE_TARGETS。
//
// api 包只用它的**键集合**做 422 校验（ops_api.py:187-188，且发生在鉴权之前）；
// 值部分供装配层实现 Server.RunServiceAction 时按同一份映射分派。导出是为了让
// service.py 移植完成后不必再从 Python 抄一遍表格。调用方只读，不要修改。
var OpsServiceTargets = map[string]OpsServiceTarget{
	"start_amkr":            {Kind: "background-start"},
	"stop_amkr":             {Kind: "background-stop"},
	"restart_amkr":          {Kind: "background-restart"},
	"status_amkr":           {Kind: "system", Argument: "status"},
	"install_user_amkr":     {Kind: "system", Argument: "install-user"},
	"uninstall_amkr":        {Kind: "system", Argument: "uninstall"},
	"install_system_amkr":   {Kind: "system", Argument: "install"},
	"uninstall_system_amkr": {Kind: "system", Argument: "uninstall"},
	"start_system_amkr":     {Kind: "system", Argument: "start"},
	"stop_system_amkr":      {Kind: "system", Argument: "stop"},
	"restart_system_amkr":   {Kind: "system", Argument: "restart"},
}

// opsSupportedAgents 对应 agent_config.py:27 的 SUPPORTED_AGENTS。
//
// 它必须与 internal/agentconfig 手工同步：agent_config.py 才是新 Agent 的唯一来源，
// 而这里要在没有装配 agentconfig 的情况下也能给出参照实现的 404/422——非法 Agent
// 是 404（与参照实现一致）。OpsIntegrations.SupportedAgents 非空时以接缝为准，
// 见 opsAgentSupportedAt。
var opsSupportedAgents = []string{"claude-code", "codex", "pi-agent"}

// specOpsWebUIRequest 对应 ops_api.py:69 的 WebUIRequest（继承 APIModel，因此带
// config_revision；运维面并不用它做乐观并发校验）。
var specOpsWebUIRequest = newModelSpec("WebUIRequest",
	nul("config_revision", kindStr).minLenOf(1),
	req("enabled", kindBool),
)

// specOpsIntegrationRequest 对应 ops_api.py:65 的 IntegrationRequest。
//
// 历史上它带一个枚举字段 `mode`（native / unified-model）。路由模式取消后该字段
// **不再有语义**，但仍然接受：请求体里带着它的旧客户端/脚本不会被拒，值被忽略——
// 集成现在一律注入 AMKR 当前可用的全部模型。
var specOpsIntegrationRequest = newModelSpec("IntegrationRequest",
	nul("config_revision", kindStr).minLenOf(1),
	def("mode", kindStr),
)

// OpsAgentStatus 对应 agent_config.AgentConfigStatus 里路由用到的字段。
type OpsAgentStatus struct {
	// TargetPath 是 Agent 配置文件路径。
	TargetPath string
	// BackupAvailable 表示存在可回退的备份。
	BackupAvailable bool
	// CurrentIsApplied 表示当前文件就是本工具写入的那份。
	CurrentIsApplied bool
}

// OpsAgentConfigResult 对应 agent_config.AgentConfigResult 的字段子集。
type OpsAgentConfigResult struct {
	TargetPath       string
	BackupPath       string
	RouterURL        string
	ExtraTargetPaths []string
}

// OpsAgentRollbackResult 对应 rollback_agent 返回值的字段子集。
type OpsAgentRollbackResult struct {
	TargetPath string
	Restored   bool
}

// OpsIntegrations 是 agent_config.py 的注入接缝。
//
// internal/agentconfig 由另一个 agent 移植，本包**不依赖**它：装配层把它接上之后，
// 三条集成路由的 URL 与响应形状就按参照实现工作。nil 表示未接线——相关 handler 会
// 响亮失败（500 + 明确文案），不会静默返回空列表或假装成功。
type OpsIntegrations struct {
	// SupportedAgents 是接缝方（internal/agentconfig）认为受支持的 Agent 列表，
	// 对应 agent_config.py:27。非空时它**覆盖** opsSupportedAgents 这份镜像，让
	// agentconfig 新增 Agent 后不必回头改本包；为空时用镜像，保证未接线也能给出
	// 与参照实现一致的 404/422。
	SupportedAgents []string
	// DisplayName 对应 agent_config.py:61 的 agent_display_name。nil 时退化为
	// agent 本身（参照实现也是 `{...}.get(agent, agent)`）。
	DisplayName func(agent string) string
	// Status 对应 get_agent_config_status。返回的 error 按「单个 Agent 隔离」
	// 处理（ops_api.py:253-266）。
	Status func(agent string) (OpsAgentStatus, error)
	// Configure 对应 configure_agent：把 AMKR 当前可用的全部模型写进该 Agent 的配置。
	Configure func(agent string, cfg *config.RouterConfig) (OpsAgentConfigResult, error)
	// Rollback 对应 rollback_agent。
	Rollback func(agent string) (OpsAgentRollbackResult, error)
}

// pyErrorNamer 让接缝的错误类型提供 Python 风格的类名。
//
// 用于复刻 ops_api.py:265 的 `f"{type(exc).__name__}: {exc}"`：Go 的 %T 会给出
// "*agentconfig.AgentConfigError" 这种带包路径的形式，与参照实现不同。装配层在
// 自己的错误类型上实现 PyErrorName() 即可得到逐字一致的 error 字段；未实现时退化为
// err.Error()（该分歧由 TestOpsIntegrationEntryErrorText 锁定）。
type pyErrorNamer interface{ PyErrorName() string }

// opsRoutePatterns 列出 7 条运维路由的「方法 + 模式」，供测试断言 URL 空间与控制开关。
//
// 与 router.go 的 routePatterns 同样的理由：ServeMux 不暴露已注册模式，而「7 条都
// 注册了 / OpsEnabled=false 时一条都不注册」都需要一条明确可断言的清单。
func opsRoutePatterns() []string {
	return []string{
		"GET /api/logs",
		"GET /api/tool",
		"POST /api/tool/webui",
		"POST /api/service/{action}",
		"GET /api/integrations",
		"POST /api/integrations/{agent}",
		"POST /api/integrations/{agent}/rollback",
	}
}

// RegisterOps 在给定多路复用器上注册 7 条运维路由。
//
// 对应 ops_api.py:119 的 register_ops_api(app, reload_config)：注册到与管理面**同一个**
// 宿主上。它自身不检查 OpsEnabled（Handler 负责按开关调用）；单独导出是为了让装配层
// 在多路复用器布局不同时（例如把 /api 挂到别的路径）也能自行注册。
func (s *Server) RegisterOps(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/logs", s.handleOpsLogs)
	mux.HandleFunc("GET /api/tool", s.handleOpsTool)
	mux.HandleFunc("POST /api/tool/webui", s.handleOpsSetWebUI)
	mux.HandleFunc("POST /api/service/{action}", s.handleOpsService)
	mux.HandleFunc("GET /api/integrations", s.handleOpsListIntegrations)
	mux.HandleFunc("POST /api/integrations/{agent}", s.handleOpsApplyIntegration)
	mux.HandleFunc("POST /api/integrations/{agent}/rollback", s.handleOpsRollbackIntegration)
}

// —— GET /api/logs ——

// handleOpsLogs 对应 ops_api.py:144 的 read_logs。
//
// 三条容易写错的语义（都刻意与参照实现保持一致）：
//
//  1. 日志文件不存在、或路径不是普通文件时返回 **200**，text 为空、error 为 null
//     （ops_api.py:148-149）；
//  2. 读取过程抛 OSError 时同样是 **200**，只是把错误文本放进 error 字段
//     （ops_api.py:152-153）——它**不是** 500；
//  3. 配置文件不在磁盘上不影响本路由：配置取自 runtime 快照（authorizedConfig），
//     而不是现场读盘。
func (s *Server) handleOpsLogs(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		path := cfg.LogFilePath
		if !isRegularFile(path) {
			return opsLogsBody("", false, path, canonical.NewNull()), nil
		}
		text, truncated, tailErr := s.tailLog(path, opsLogTailBytes)
		if tailErr != nil {
			// 与参照实现一致：出错时 text 为空、truncated 为假（ops_api.py:153）。
			return opsLogsBody("", false, path, canonical.NewString(tailErr.Error())), nil
		}
		return opsLogsBody(text, truncated, path, canonical.NewNull()), nil
	})
}

// opsLogsBody 构造 /api/logs 的响应体；键序对齐 ops_api.py:149 的字典字面量。
func opsLogsBody(text string, truncated bool, path string, errorValue *canonical.Value) *canonical.Value {
	return objectOf(
		canonical.ObjectPair{Key: "text", Value: canonical.NewString(text)},
		canonical.ObjectPair{Key: "truncated", Value: canonical.NewBool(truncated)},
		canonical.ObjectPair{Key: "path", Value: canonical.NewString(path)},
		canonical.ObjectPair{Key: "error", Value: errorValue},
	)
}

// tailLog 读取日志尾部；LogTail 接缝为 nil 时用内置实现。
func (s *Server) tailLog(path string, limit int) (string, bool, error) {
	if s.LogTail != nil {
		return s.LogTail(path, limit)
	}
	return tailLogFile(path, limit)
}

// tailLogFile 对应 ops_api.py:93 的 _tail：读末尾 limit 字节并做 UTF-8 宽容解码。
//
// 截断判据是 size > limit——**相等不截断**（ops_api.py:97-100）。stat 与 open 分成
// 两步，与参照实现一致：先取 size 决定是否 seek 到 size-limit，再整段读到底。
func tailLogFile(path string, limit int) (string, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	size := info.Size()
	handle, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer handle.Close()

	truncated := size > int64(limit)
	if truncated {
		if _, err := handle.Seek(size-int64(limit), io.SeekStart); err != nil {
			return "", false, err
		}
	}
	raw, err := io.ReadAll(handle)
	if err != nil {
		return "", false, err
	}
	return decodeUTF8Replacing(raw), truncated, nil
}

// —— GET /api/tool ——

// handleOpsTool 对应 ops_api.py:156 的 get_tool。
//
// 响应 = 版本检查结果 + webui_status(app) 展开，键序与 ops_api.py:162-170 的字典
// 字面量一致。注意 `update_available` 是 Python 的 `bool(result.update_available)`
// 属性（update.py:52-54）：latest_version 为空时恒为 false。
func (s *Server) handleOpsTool(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		// 参照实现固定 timeout=3.0（ops_api.py:161），与管理面的 10.0 不同。
		result := s.checkLatestVersion(3.0)
		body := objectOf(
			canonical.ObjectPair{Key: "version", Value: canonical.NewString(s.Version)},
			canonical.ObjectPair{Key: "latest_version", Value: nullableString(result.LatestVersion)},
			canonical.ObjectPair{Key: "update_available", Value: canonical.NewBool(opsUpdateAvailable(result))},
			canonical.ObjectPair{Key: "release_url", Value: nullableString(result.ReleaseURL)},
			canonical.ObjectPair{Key: "source", Value: nullableString(result.Source)},
			canonical.ObjectPair{Key: "error", Value: nullableString(result.Error)},
		)
		return appendWebUIStatus(body, s.webUIStatus()), nil
	})
}

// opsUpdateAvailable 复刻 update.py:52-54 的
// `bool(self.latest_version and is_newer_version(self.latest_version, self.current_version))`。
//
// 接缝带来的 UpdateCheckResult 已经把比较算好（UpdateAvailable），但 Python 的
// `latest_version and ...` 短路必须先判：没有最新版本号时，无论调用方填了什么都是
// false。真实路径（internal/updatecheck）本身也满足这条不变式，这里只是兜住接缝。
func opsUpdateAvailable(result UpdateCheckResult) bool {
	return result.LatestVersion != "" && result.UpdateAvailable
}

// checkLatestVersion 对应 update.check_latest_version（update.py:159）。
//
// CheckUpdate 接缝优先（管理面 POST /api/update/check 也用它）；未接缝时直接调
// internal/updatecheck.CheckLatestVersion，即**真实实现**（会发网络请求，可用
// UpdateFetcher 注入假取回器）。
func (s *Server) checkLatestVersion(timeoutSeconds float64) UpdateCheckResult {
	if s.CheckUpdate != nil {
		return s.CheckUpdate(timeoutSeconds)
	}
	fetch := s.UpdateFetcher
	if fetch == nil {
		fetch = updatecheck.HTTPFetcher(nil)
	}
	result := updatecheck.CheckLatestVersion(fetch, s.Version, time.Duration(timeoutSeconds*float64(time.Second)))
	return UpdateCheckResult{
		CurrentVersion:  result.CurrentVersion,
		LatestVersion:   derefOrEmpty(result.LatestVersion),
		ReleaseURL:      derefOrEmpty(result.ReleaseURL),
		Source:          derefOrEmpty(result.Source),
		ArtifactURL:     derefOrEmpty(result.ArtifactURL),
		ArtifactSHA256:  derefOrEmpty(result.ArtifactSHA256),
		UpdateAvailable: result.UpdateAvailable(),
		Error:           derefOrEmpty(result.Error),
	}
}

// derefOrEmpty 把 updatecheck 的可选字符串收敛成本包的「空串表示没有」。
func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// webUIStatus 返回 webui_status(app) 的输入。
//
// 接缝为 nil 时返回零值：Python 在 app.state 上没有 webui_mounted、
// runtime_manager 也没有 current 时（webui.py:59-70）得到的正是全 false + null。
func (s *Server) webUIStatus() health.WebUI {
	if s.WebUIStatus != nil {
		return s.WebUIStatus()
	}
	return health.WebUI{}
}

// appendWebUIStatus 把 webui_status(app) 的四个字段接到 body 之后。
//
// 字段与顺序对齐 webui.py:58-63 的字典字面量：webui_available、webui_enabled、
// webui_mounted、webui_path；webui_path 只在「当前进程真的挂载了」时是字符串，否则
// 是 null（`webui_path(app) if mounted else None`）。复用 health.WebUI 作为输入类型，
// 保证 /health 与 /api/tool 对同一份状态用同一套字段语义。
func appendWebUIStatus(body *canonical.Value, status health.WebUI) *canonical.Value {
	pathValue := canonical.NewNull()
	if status.Mounted {
		pathValue = canonical.NewString(strings.TrimRight(status.MountPrefix, "/") + webUIPath)
	}
	body.SetKey("webui_available", canonical.NewBool(status.Available))
	body.SetKey("webui_enabled", canonical.NewBool(status.Enabled))
	body.SetKey("webui_mounted", canonical.NewBool(status.Mounted))
	body.SetKey("webui_path", pathValue)
	return body
}

// —— POST /api/tool/webui ——

// handleOpsSetWebUI 对应 ops_api.py:172 的 set_webui。
//
// 顺序照抄参照实现：请求体校验（FastAPI 依赖）-> 鉴权 -> 配置文件路径存在性 ->
// 落盘 -> 热重载 -> 组装响应。
//
// 两处刻意的忠实：
//   - 落盘失败**不做**管理 API 的错误映射。参照把 ConfigService.update 直接丢进
//     asyncio.to_thread，没有任何 try/except，因此异常一路冒到 Starlette 的兜底 500
//     （纯文本 "Internal Server Error"），而不是「配置保存失败: ...」。这里原样返回
//     error，让 writeError 走同一条路。
//   - 配置落盘后必须 reload：`webui_enabled` 取自 runtime 快照，而 `webui_mounted`
//     是进程启动时的状态，改开关不会让 /ui 立刻出现（ops_api.py:181-182 的注释）。
func (s *Server) handleOpsSetWebUI(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specOpsWebUIRequest, false)
		if err != nil {
			return nil, err
		}
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		path, err := s.opsConfigPath()
		if err != nil {
			return nil, err
		}
		enabledValue := payload.Lookup("enabled")
		enabled := enabledValue != nil && enabledValue.Truthy()
		if _, err := configservice.New(path).Update(func(data *canonical.Value) error {
			data.SetKey("webui_enabled", canonical.NewBool(enabled))
			return nil
		}); err != nil {
			return nil, err
		}
		s.reload()
		body := objectOf(canonical.ObjectPair{Key: "enabled", Value: canonical.NewBool(enabled)})
		return appendWebUIStatus(body, s.webUIStatus()), nil
	})
}

// opsConfigPath 对应 ops_api.py:133 的 config_path()。
//
// 与 managementConfigData 同样读 app.state.config_path，但错误文案不同（这里是
// 「无法执行本机操作」），且**不**读盘校验内容，只要求存在普通文件。
func (s *Server) opsConfigPath() (string, error) {
	if s.ConfigPath == "" {
		return "", httpErrorf(http.StatusConflict, "未设置配置文件路径，无法执行本机操作")
	}
	if !isRegularFile(s.ConfigPath) {
		return "", httpErrorf(http.StatusConflict, "配置文件不存在: %s", s.ConfigPath)
	}
	return s.ConfigPath, nil
}

// —— POST /api/service/{action} ——

// handleOpsService 对应 ops_api.py:185 的 run_service。
//
// 关键顺序：动作名白名单校验**先于鉴权**（ops_api.py:187-188 在 authorized_config
// 之前），所以无凭据请求一个不支持的动作得到的是 422 而不是 401。
//
// RunServiceAction 接缝为 nil 时返回 500 + 明确文案（service.py 尚未移植）。这不是
// 参照实现的状态——参照总能真正执行动作——而是「未接线」的响亮失败；状态码选了参照
// 自己的失败状态码 500（ops_api.py:193-194 把 OSError/ValueError/KeyError 收敛成
// 「服务操作失败: ...」）。
func (s *Server) handleOpsService(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		action := r.PathValue("action")
		if _, supported := OpsServiceTargets[action]; !supported {
			return nil, httpErrorf(http.StatusUnprocessableEntity, "不支持的服务动作: %s", action)
		}
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		path, err := s.opsConfigPath()
		if err != nil {
			return nil, err
		}
		if s.RunServiceAction == nil {
			return nil, httpErrorf(
				http.StatusInternalServerError,
				"服务操作失败: 服务动作接缝（Server.RunServiceAction）未接线：service.py 尚未移植",
			)
		}
		text, err := s.RunServiceAction(action, path, cfg)
		if err != nil {
			// 参照只捕获 OSError/ValueError/KeyError，其它异常是纯文本 500；接缝只回
			// 一个 error，无法区分，故一律走这条映射——状态码与参照一致，只有响应体
			// 在多一类异常上从纯文本变成了 JSON detail。
			return nil, httpErrorf(http.StatusInternalServerError, "服务操作失败: %s", err.Error())
		}
		return objectOf(
			canonical.ObjectPair{Key: "action", Value: canonical.NewString(action)},
			canonical.ObjectPair{Key: "text", Value: canonical.NewString(text)},
		), nil
	})
}

// —— /api/integrations ——

// opsIntegrationsNotWired 是「agent_config 未装配」的统一错误。
//
// 参照实现没有对应状态（它总能 import agent_config）。这里选 500：agent_config
// 缺失属于内部装配错误，参照对非 AgentConfigError/OSError/ValueError 的异常也走
// Starlette 兜底 500。文案里点名接缝，保证「不静默成功」且可直接定位。
func opsIntegrationsNotWired() error {
	return httpErrorf(
		http.StatusInternalServerError,
		"Agent 集成接口未接入: 接缝 Server.Integrations 未装配（internal/agentconfig 尚未接线）",
	)
}

// handleOpsListIntegrations 对应 ops_api.py:197 的 list_integrations。
func (s *Server) handleOpsListIntegrations(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		src := s.Integrations
		if src == nil || src.Status == nil {
			return nil, opsIntegrationsNotWired()
		}
		agents := s.supportedAgents()
		entries := make([]*canonical.Value, 0, len(agents))
		for _, agent := range agents {
			entries = append(entries, opsIntegrationEntry(agent, src))
		}
		return objectOf(canonical.ObjectPair{Key: "integrations", Value: canonical.NewArray(entries...)}), nil
	})
}

// handleOpsApplyIntegration 对应 ops_api.py:206 的 apply_integration。
//
// 顺序：请求体校验 -> Agent 白名单（404）-> 鉴权 -> 执行。前两步都在鉴权之前，
// 因此无凭据请求一个未知 Agent 得到 404 而不是 401。
//
// 请求体里的 `mode` 已被忽略（见 specOpsIntegrationRequest）：集成不再有路由模式。
// 请求体因此整体可省（`optional=true`）：空体与 `{}` 等价，只有给旧客户端保留的
// `mode` 字段仍然被接受。
func (s *Server) handleOpsApplyIntegration(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		agent := r.PathValue("agent")
		if _, _, err := decodePayload(r, specOpsIntegrationRequest, true); err != nil {
			return nil, err
		}
		if !s.opsAgentSupportedAt(agent) {
			return nil, httpErrorf(http.StatusNotFound, "不支持的集成: %s", agent)
		}
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		src := s.Integrations
		if src == nil || src.Configure == nil {
			return nil, opsIntegrationsNotWired()
		}
		result, err := src.Configure(agent, cfg)
		if err != nil {
			// ops_api.py:221-222：OSError / AgentConfigError / ValueError -> 409，
			// 错误文本原样交给前端。
			return nil, httpErrorf(http.StatusConflict, "%s", err.Error())
		}
		return objectOf(
			canonical.ObjectPair{Key: "integration", Value: opsIntegrationEntry(agent, src)},
			canonical.ObjectPair{Key: "target_path", Value: canonical.NewString(result.TargetPath)},
			canonical.ObjectPair{Key: "backup_path", Value: canonical.NewString(result.BackupPath)},
			canonical.ObjectPair{Key: "router_url", Value: canonical.NewString(result.RouterURL)},
			canonical.ObjectPair{Key: "extra_target_paths", Value: stringArray(result.ExtraTargetPaths)},
		), nil
	})
}

// handleOpsRollbackIntegration 对应 ops_api.py:231 的 rollback_integration。
//
// 它没有请求体参数（对比 apply 的 IntegrationRequest），因此这里不解析 body。
func (s *Server) handleOpsRollbackIntegration(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		agent := r.PathValue("agent")
		if !s.opsAgentSupportedAt(agent) {
			return nil, httpErrorf(http.StatusNotFound, "不支持的集成: %s", agent)
		}
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		src := s.Integrations
		if src == nil || src.Rollback == nil {
			return nil, opsIntegrationsNotWired()
		}
		result, err := src.Rollback(agent)
		if err != nil {
			return nil, httpErrorf(http.StatusConflict, "%s", err.Error())
		}
		return objectOf(
			canonical.ObjectPair{Key: "integration", Value: opsIntegrationEntry(agent, src)},
			canonical.ObjectPair{Key: "target_path", Value: canonical.NewString(result.TargetPath)},
			canonical.ObjectPair{Key: "restored", Value: canonical.NewBool(result.Restored)},
		), nil
	})
}

// opsAgentSupportedAt 复刻 `agent in SUPPORTED_AGENTS`：接缝给了白名单就以它为准，
// 否则用本包镜像的常量（未接线时也必须能给出正确的 404）。
func (s *Server) opsAgentSupportedAt(agent string) bool {
	return containsString(s.supportedAgents(), agent)
}

// supportedAgents 返回受支持的 Agent 列表：接缝给了就以它为准，否则用本包镜像的常量。
//
// **列表与白名单必须同源**：否则接缝方新增一个 Agent 后，POST /api/integrations/<agent>
// 会被接受，而 GET /api/integrations 的列表里看不到它。参照实现只有一个 SUPPORTED_AGENTS
// 常量，`agent in SUPPORTED_AGENTS` 与列表天然一致，这里必须自己保证这一点。
func (s *Server) supportedAgents() []string {
	if src := s.Integrations; src != nil && len(src.SupportedAgents) > 0 {
		return src.SupportedAgents
	}
	return opsSupportedAgents
}

// containsString 报告切片里是否含目标字符串。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// opsIntegrationEntry 对应 ops_api.py:247 的 _integration_entry。
//
// 读取失败只影响这一个 Agent（ops_api.py:250-266 刻意捕获 Exception）：/api/integrations
// 是三个相互独立的只读状态汇总，任何一个探测异常都不应该让另外两个也显示不出来，
// 失败原因原样放进 error 字段。返回的 error 只用于判断成败，不用于写响应体。
func opsIntegrationEntry(agent string, src *OpsIntegrations) *canonical.Value {
	displayName := agent
	if src.DisplayName != nil {
		displayName = src.DisplayName(agent)
	}
	status, err := src.Status(agent)
	if err != nil {
		return objectOf(
			canonical.ObjectPair{Key: "agent", Value: canonical.NewString(agent)},
			canonical.ObjectPair{Key: "display_name", Value: canonical.NewString(displayName)},
			canonical.ObjectPair{Key: "target_path", Value: canonical.NewString("")},
			canonical.ObjectPair{Key: "target_exists", Value: canonical.NewBool(false)},
			canonical.ObjectPair{Key: "backup_available", Value: canonical.NewBool(false)},
			canonical.ObjectPair{Key: "current_is_applied", Value: canonical.NewBool(false)},
			canonical.ObjectPair{Key: "error", Value: canonical.NewString(opsErrorText(err))},
		)
	}
	return objectOf(
		canonical.ObjectPair{Key: "agent", Value: canonical.NewString(agent)},
		canonical.ObjectPair{Key: "display_name", Value: canonical.NewString(displayName)},
		canonical.ObjectPair{Key: "target_path", Value: canonical.NewString(status.TargetPath)},
		// Python 的 Path(target_path).exists() 跟随符号链接，且对任何 stat 错误都返回
		// False；Go 用 os.Stat 一一对应（目录也算存在，与 Path.exists 一致）。
		canonical.ObjectPair{Key: "target_exists", Value: canonical.NewBool(pathExists(status.TargetPath))},
		canonical.ObjectPair{Key: "backup_available", Value: canonical.NewBool(status.BackupAvailable)},
		canonical.ObjectPair{Key: "current_is_applied", Value: canonical.NewBool(status.CurrentIsApplied)},
		canonical.ObjectPair{Key: "error", Value: canonical.NewNull()},
	)
}

// opsErrorText 复刻 ops_api.py:265 的 `f"{type(exc).__name__}: {exc}"`。
//
// Go 没有稳定的「异常类短名」：%T 会带上包路径。装配层的错误类型实现
// PyErrorName() string 即可给出与参照实现逐字一致的类名；未实现时只用 err.Error()
// （名字前缀缺失，由 TestOpsIntegrationEntryErrorText 锁定为已知分歧）。
func opsErrorText(err error) string {
	var namer pyErrorNamer
	if errors.As(err, &namer) {
		return namer.PyErrorName() + ": " + err.Error()
	}
	return err.Error()
}

// pathExists 对应 Python 的 Path.exists()：跟随符号链接，任何 stat 错误都算不存在。
func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// —— UTF-8 宽容解码 ——

// decodeUTF8Replacing 复刻 Python 的 `bytes.decode("utf-8", errors="replace")`。
//
// 与 internal/proxysupport 里的同名函数逐字相同：那边是私有实现，而本次迁移约定
// 「只新增 internal/api」，不值得为了一个 50 行 helper 去导出别人的内部件（那还会让
// api 依赖 proxysupport）。两处各自被本包的用例守着：这里的边界取值由
// TestOpsLogsTailRule 覆盖，proxysupport 那份由 TestDecodeUTF8ReplacingIsValidUTF8
// 与 TestDecodeUTF8ReplacingCountsMaximalSubparts 覆盖，因此改动任何一份都会在
// 对应的包里失败。
//
// 为什么不能图省事：
//   - strings.ToValidUTF8 会把**连续**非法字节折叠成一个 U+FFFD；
//   - 逐字节用 utf8.DecodeRune 会把 `e4 b8`（截断的 中）拆成两个 U+FFFD。
//
// Python 按 Unicode 的「最大子部分」（maximal subpart, Unicode 3.9 D93）每段各产一个
// U+FFFD，而 /api/logs 的 64 KiB 截断边界恰好会切出这种半截序列（ops_api.py:100）。
func decodeUTF8Replacing(content []byte) string {
	const replacement = "\uFFFD"
	var sb strings.Builder
	sb.Grow(len(content))
	for i := 0; i < len(content); {
		b0 := content[i]
		if b0 < 0x80 {
			sb.WriteByte(b0)
			i++
			continue
		}
		// 首字节决定续字节个数，以及**第二个字节**的合法区间。区间约束不可省：它同时
		// 排除过长编码（C0/C1、E0 8x、F0 8x）、代理对（ED A0-BF）与超出 U+10FFFF
		//（F4 90-BF）。这些情形下最大子部分只有首字节本身，于是每个字节各算一个 U+FFFD。
		extra := 0
		var lo, hi byte = 0x80, 0xBF
		switch {
		case b0 >= 0xC2 && b0 <= 0xDF:
			extra = 1
		case b0 == 0xE0:
			extra, lo = 2, 0xA0
		case b0 >= 0xE1 && b0 <= 0xEC:
			extra = 2
		case b0 == 0xED:
			extra, hi = 2, 0x9F
		case b0 == 0xEE || b0 == 0xEF:
			extra = 2
		case b0 == 0xF0:
			extra, lo = 3, 0x90
		case b0 >= 0xF1 && b0 <= 0xF3:
			extra = 3
		case b0 == 0xF4:
			extra, hi = 3, 0x8F
		default:
			// 非法首字节：80-BF（续字节误作首字节）、C0/C1（过长）、F5-FF。
			sb.WriteString(replacement)
			i++
			continue
		}
		// 贪心吃掉最长的合法前缀：先校验第二个字节（区间收窄过），再校验其余续字节。
		j := i + 1
		if j < len(content) && content[j] >= lo && content[j] <= hi {
			j++
			for j < i+1+extra && j < len(content) && content[j] >= 0x80 && content[j] <= 0xBF {
				j++
			}
		}
		if j == i+1+extra {
			sb.Write(content[i:j])
		} else {
			// 一个最大子部分只产生一个 U+FFFD，然后从 j 继续（不吞掉后续子部分）。
			sb.WriteString(replacement)
		}
		i = j
	}
	return sb.String()
}
