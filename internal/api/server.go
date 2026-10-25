package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/Sparrived/auto-model-key-router/internal/auth"
	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
	"github.com/Sparrived/auto-model-key-router/internal/configservice"
	"github.com/Sparrived/auto-model-key-router/internal/health"
	"github.com/Sparrived/auto-model-key-router/internal/quota"
	"github.com/Sparrived/auto-model-key-router/internal/updatecheck"
)

// httpError 复刻 FastAPI 的 HTTPException：响应体固定是 {"detail": "..."}。
//
// 与 ManagementAPIError 的区别是**它会被翻译成 HTTP 响应**，而 ManagementAPIError
// 只有被 _update_config 捕获时才被翻译（management_api.py:1164）。原样冒到顶层的
// ManagementAPIError 在参照实现里没有任何 exception handler，于是 Starlette 的
// ServerErrorMiddleware 返回 500 纯文本 "Internal Server Error"——见 writeError。
type httpError struct {
	status int
	detail string
}

func (e *httpError) Error() string { return e.detail }

// httpErrorf 构造带格式化文本的 httpError。
func httpErrorf(status int, format string, args ...any) *httpError {
	return &httpError{status: status, detail: fmt.Sprintf(format, args...)}
}

// managementAPIError 对应 management_api.py:201 的 ManagementAPIError。
//
// 它只在 _update_config 的 mutation 之内被翻译成 HTTPException；在别处（例如
// _require_provider）它一路冒到顶层，对外表现为 500。Go 侧因此不能把它当作
// httpError 处理：两者的状态码映射不同，故意保留为独立类型。
type managementAPIError struct {
	status  int
	message string
}

func (e *managementAPIError) Error() string { return e.message }

// pyValueError 复刻参照实现里「非 ConfigOperationError 的 ValueError」。
//
// 这类错误在 _update_config 内是 400「配置校验失败: ...」，在 _update_config 外
// （_providers/_routes 直接调用）是 500。Go 侧用同一个类型表达，由调用位置决定
// 映射，绝不折叠成 ConfigOperationError。
type pyValueError struct{ message string }

func (e *pyValueError) Error() string { return e.message }

// Server 是一组管理 API 路由的宿主状态。
//
// 对照 Python 的 app.state + 注入的 reload_config 回调：配置读写都落在 ConfigPath
// 指向的文件上，鉴权用「当前生效配置」里的 local_api_key。
type Server struct {
	// ConfigPath 是配置文件路径，对应 app.state.config_path。
	ConfigPath string
	// Reload 对应 create_app 注入的 reload_config 回调（_reload_config_if_changed）。
	// 为 nil 表示不做热重载，仅测试与嵌入场景使用。
	//
	// 实现方必须复刻 app.py:443 的 mtime 门：**文件不存在（mtime 为 0）时直接返回**。
	// 不能图省事在这里无条件调 config.Load——它在文件缺失时会创建一份空配置并返回
	// 成功，会让后续鉴权拿着空 local_api_key 失败（这是参照实现的历史行为，刻意保留）。
	Reload func()
	// CurrentConfig 返回「当前生效」的配置，对应
	// `lease.resources.config`。为 nil 时回落到从 ConfigPath 现场加载。
	//
	// 参照实现在 _authorized_config 里 acquire 一个 runtime 租约再读
	// lease.resources.config；Go 侧只需要一个配置快照——管理 API 从不使用租约里的
	// 连接池/指标，而租约存在的意义是保护流式请求，那是 proxy 的职责。真实装配时
	// 宿主把这份回调接到 runtime.RuntimeManager.Current()。
	CurrentConfig func() *config.RouterConfig
	// Authorizer 对应 app.state.authenticator；nil 表示默认实现。
	Authorizer auth.Authorizer
	// Logger 是管理面自己写结构化日志的出口（手工解除冻结、巡检失败等）。
	//
	// nil 时退回 slog.Default()：这些日志不是可选项——「谁在什么时候解除了哪把 Key
	// 的冻结」必须留痕，静默失败比多打一行糟得多。
	Logger *slog.Logger
	// QuotaFreezes 是额度冻结表；nil 表示这套机制未装配（嵌入/测试场景）。
	//
	// 冻结的写操作全部由 QuotaGuard 负责，管理面只读它、并允许手工解除一条。
	QuotaFreezes *quota.Store
	// QuotaGuard 是额度冻结评估器；nil 时账号资源页不做即时评估、L3 信号不落地。
	//
	// 它与 QuotaFreezes 分开注入：嵌入方可能只想要冻结表（读状态）却不要后台巡检，
	// 两者的 nil 语义因此不同，不能用一个字段兼表。
	QuotaGuard *QuotaGuard

	// ProbeKeyCapability 对应 config_editor.probe_key_capability。
	//
	// config_editor.py 尚未移植，故这里留接缝：nil 时相关路由按「探测失败」处理，
	// 与 Python 里探测抛异常时把 error 写进响应体的行为一致。
	ProbeKeyCapability func(provider *canonical.Value, keyName string, modes []string, timeout float64) (*canonical.Value, error)
	// ProbeProviderKeyCapabilities 对应 config_editor.probe_provider_key_capabilities。
	ProbeProviderKeyCapabilities func(provider *canonical.Value, keyNames []string, timeout float64) (*canonical.Value, error)
	// ProbeKeyAvailability 对应 config_editor.probe_key_availability。
	ProbeKeyAvailability func(ctx context.Context, routes *canonical.Value, model string, key *canonical.Value, timeout float64) ([]ProbeAvailability, error)
	// CheckUpdate 对应 update.check_latest_version（会发网络请求，故做成接缝）。
	CheckUpdate func(timeout float64) UpdateCheckResult
	// KeyStats 对应 metrics.key_stats。internal/metrics 由另一个 agent 在写，本包
	// 不依赖它，只留这个接缝。
	KeyStats func(modelID, keyName string, hours *float64) (*canonical.Value, error)
	// AccessKeyLastUsed 读取访问密钥的最近调用时间；nil 表示未装配指标读数。
	AccessKeyLastUsed func() (map[string]string, error)
	// UUIDHex 生成 32 位十六进制，对应 `uuid.uuid4().hex`（探测 id 与导入备份
	// 文件名后缀都用它）。nil 表示用密码学随机数；注入固定值是为了让测试能对 id
	// 做断言。
	UUIDHex func() string
	// GenerateLocalAPIKey 对应 config.generate_local_api_key。nil 表示用
	// config 包的实现；注入固定值是为了让测试能对生成的 key 做断言。
	GenerateLocalAPIKey func() (string, error)

	// —— 运维面（ops_api.py）需要的开关与接缝，全部只在 OpsEnabled 为真时生效 ——

	// OpsEnabled 控制是否注册 7 条运维路由，对应 app.py:142-146 的 enable_ops
	// （None 时跟随 config.ops_enabled，而配置里该项默认 true）。
	//
	// 为 false 时这些 URL **完全不注册**，落进兜底 404（{"detail":"Not Found"}），
	// 与 Python 的行为一致（迁移方案：运维接口在 ops_enabled=false 时整体 404）。
	// 零值是 false，因此既不改变既有管理路由的行为，也要求装配层显式打开。
	OpsEnabled bool
	// Version 是 `GET /api/tool` 汇报的进程版本，对应 ops_api.py:163 的 __version__。
	// 装配层用 ldflags 注入的版本号填充（迁移方案「版本单一来源」）；为空时响应里
	// version 是空串——不会静默冒充某个版本。
	//
	// 它是**独立**来源：真实路径下 CheckUpdate 接缝的 CurrentVersion 恰好等于它
	// （check_latest_version 的默认 current_version 就是 __version__ 且不返回），但
	// 两者语义不同，因此不互相回退——装配层漏填时 version 会是空串，一眼可见。
	Version string
	// LogTail 读取日志尾部，对应 ops_api.py:93 的 _tail。nil 表示用内置实现。
	//
	// 留接缝是为了确定性地覆盖「读取失败仍返回 200 + error 文本」这条分支：真实文件
	// 系统上「is_file() 为真、随后 stat/open 失败」只能靠竞态或权限构造，测试里复现
	// 不稳定。
	LogTail func(path string, limit int) (string, bool, error)
	// WebUIStatus 返回 webui_status(app) 的输入，对应 webui.py:48。nil 表示
	// 「未挂载、不可用、未启用」的零值。复用 health.WebUI 以与 /health 同一套语义。
	WebUIStatus func() health.WebUI
	// UpdateFetcher 是版本检查的 HTTP 取回器；仅当 CheckUpdate 为 nil 时用于
	// internal/updatecheck。nil 表示真实网络。
	UpdateFetcher updatecheck.Fetcher
	// RunServiceAction 对应 ops_api.py:103 的 _run_service_action：返回已经渲染成
	// 纯文本的动作输出。service.py 尚未移植，nil 表示未接线（handler 返回 500 而
	// 不是假装成功）。实现方按 OpsServiceTargets 分派。
	RunServiceAction func(action string, configPath string, cfg *config.RouterConfig) (string, error)
	// Integrations 是 agent_config.py 的接缝（见 OpsIntegrations）。nil 表示未接线，
	// 三条集成路由会响亮失败而不是返回空列表。
	Integrations *OpsIntegrations

	// writeMu 对应 app.state.config_write_lock：串行化「读-改-写」。
	writeMu sync.Mutex
	// probesMu 保护 probes 表；Python 靠单线程事件循环天然串行。
	probesMu sync.Mutex
	probes   map[string]*probeRecord
}

// ProbeAvailability 是一条探测结果，对应 config_editor 的可用性探测返回值。
type ProbeAvailability struct {
	Available bool
	URL       string
	// DurationMS 是**整数**毫秒：参照实现的 config_editor.py:1665 用
	// `int((monotonic() - started) * 1000)` 取值，字段本身也标注为 `duration_ms: int`
	// （config_editor.py:80）。因此必须渲染成 JSON 整数——用 float 会得到 `250.0`
	// 而参照实现是 `250`。这个 int/float 之别原先在接缝类型里丢失了。
	DurationMS int64
	Error      string
}

// UpdateCheckResult 是版本检查结果，对应 update.VersionCheckResult 的字段子集。
type UpdateCheckResult struct {
	CurrentVersion  string
	LatestVersion   string
	ReleaseURL      string
	Source          string
	ArtifactURL     string
	ArtifactSHA256  string
	UpdateAvailable bool
	Error           string
}

// Handler 返回注册了全部管理路由的 http.Handler；OpsEnabled 为真时额外注册 7 条
// 运维路由（ops_api.py 的 register_ops_api），因此 URL 空间与 Python 完全一致。
//
// 管理路由分两批（见 router.go）：参照实现留下的 47 条（已发布接口，响应字节不能再
// 变）与 Go 侧新增的工作空间/访问密钥那批。两批都算「已知路径」，否则错方法会从 405
// 退化成 404。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.register(mux)
	// patterns 是兜底 405 判定的依据，因此必须与实际注册的模式集合一致：
	// OpsEnabled 为 false 时运维面的 URL **一条都不算已知路径**，`PUT /api/logs`
	// 在参照实现里同样是 404（不是 405）。
	patterns := append(routePatterns(), workspacePatterns()...)
	if s.OpsEnabled {
		// 与 Python 相同：运维面注册在同一个应用上，复用同一套鉴权与错误形状。
		// OpsEnabled 为 false 时一条都不注册——参照实现里 /api/logs 等会落进
		// Starlette 的 404 {"detail":"Not Found"}，这里由下面的兜底处理器复刻。
		s.RegisterOps(mux)
		patterns = append(patterns, opsRoutePatterns()...)
	}
	// 未注册路径在 FastAPI 下是 {"detail":"Not Found"}（404 JSON），而 ServeMux
	// 默认输出纯文本 "404 page not found"。注册一个兜底模式以对齐常见路径。
	//
	// 兜底模式同时承担「路径存在但方法不对」的 405。不能依赖 ServeMux 自己的 405：
	//
	//   - 无方法的兜底模式匹配**任意**方法，ServeMux 根本走不到它的 405 分支
	//     （这正是 `PUT /api/logs` 曾经回 404 的原因）；
	//   - 就算走到，ServeMux 算出的 Allow 也与参照实现不同——它会把 GET 与合成的
	//     HEAD 一起列出来（"GET, HEAD"），而参照实现是 "GET"（实测）；
	//   - 它的 405 体是纯文本，参照实现是 JSON。
	//
	// 所以这里自己判定：路径能匹配已注册模式 -> 405 + Allow，否则 404。
	//
	// 遗留分歧：ServeMux 会清理/重定向路径，参照实现不会。两类都不在 47 条路由的
	// 契约内，见 doc.go 的说明。
	//
	//   - 带尾斜杠：参照实现由 Starlette 的 redirect_slashes 先 307 到无尾斜杠的路径
	//     （实测 `/api/logs/`：GET 跟随重定向后 200、PUT 405 + Allow: GET），而
	//     ServeMux 不做「去掉尾斜杠」的重定向，`/api/logs/` 直接落进兜底 404。
	//   - 不干净路径（`//api/logs`、`/api/./logs`）：ServeMux 先 cleanPath 再 307
	//     重定向（server.go:2689-2696），客户端跟随之后 `PUT //api/logs` 实际是
	//     `PUT /api/logs`（实测 405 + Allow: GET），而参照实现对不干净路径直接 404
	//     （实测）。重定向本身是 ServeMux 既有行为，本次只是让重定向**之后**的错
	//     方法从 404 变成 405。
	//
	// 上面的判定按精确路径匹配，因此 `/api/logs/` 仍旧是 404（与「正确方法也 404」
	// 保持一致），不会变成 405。
	notFoundOrMethodNotAllowed := func(w http.ResponseWriter, r *http.Request) {
		writeNotFoundOrMethodNotAllowed(w, r.URL.Path, patterns)
	}
	mux.HandleFunc("/", notFoundOrMethodNotAllowed)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ServeMux 把 GET 模式也当作 HEAD 匹配（实测），而参照实现里**没有**任何
		// 一条路由注册 HEAD：FastAPI 的 APIRoute 不给 GET 路由自动加 HEAD，实测
		// `HEAD /api/logs` 是 405 + Allow: GET（不是 200）。因此 HEAD 必须在进 mux
		// 之前分流，否则会被 GET 处理器吃掉、拿到 200——这是内部 mux 做不到的事
		// （internal/server/routes.go 对自己的路由用了同样的办法）。
		// 未知路径的 HEAD 在这里也是 404，与兜底一致。
		if r.Method == http.MethodHead {
			notFoundOrMethodNotAllowed(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// writeNotFoundOrMethodNotAllowed 复刻 Starlette 的两条路由级兜底。
//
// 「路径能匹配某条已注册模式」时是 405：FastAPI/Starlette 把这种路由当作部分匹配
// （Match.PARTIAL），由 Route.handle 抛 HTTPException(405)。路径完全不匹配才是 404。
// 判定发生在鉴权之前，所以无凭据的错方法请求同样是 405（实测）。
func writeNotFoundOrMethodNotAllowed(w http.ResponseWriter, path string, patterns []string) {
	if allow := allowedMethod(path, patterns); allow != "" {
		w.Header().Set("Allow", allow)
		writeJSON(w, http.StatusMethodNotAllowed, objectOf(canonical.ObjectPair{Key: "detail", Value: canonical.NewString("Method Not Allowed")}))
		return
	}
	writeJSON(w, http.StatusNotFound, objectOf(canonical.ObjectPair{Key: "detail", Value: canonical.NewString("Not Found")}))
}

// allowedMethod 返回路径对应的 Allow；没有任何模式匹配该路径时返回空串。
//
// Allow 只报**一个**方法，不是逗号分隔的列表——这是参照实现的可观察行为，不是简化：
// FastAPI 给每条路由只注册一个方法（management_api.py / ops_api.py 里没有
// api_route(methods=[...]) 形式的多方法路由），而 Starlette 的部分匹配只保留
// **第一个**路径匹配的路由（starlette/routing.py: `elif match == Match.PARTIAL and
// partial is None`），于是 Route.handle 的 `", ".join(self.methods)` 永远只有一个
// 元素。实测：`PUT /api/models`（GET+POST）回 "GET"，`POST /api/models/{id}`
// （GET+PUT+DELETE）也回 "GET"。因此遍历顺序必须与注册顺序（即 Python 的装饰器
// 顺序）一致，同一路径有多个模式时取第一个。
func allowedMethod(path string, patterns []string) string {
	for _, pattern := range patterns {
		method, patternPath, ok := strings.Cut(pattern, " ")
		if ok && matchesPath(patternPath, path) {
			return method
		}
	}
	return ""
}

// matchesPath 报告请求路径是否匹配一条模式路径，{name} 段匹配任意单个非空段。
//
// 只处理本包实际注册的 {name} 形式（routePatterns/opsRoutePatterns 的清单被测试
// 逐条锁定，也因此不会出现 Go 1.22 的 {name...}/{$}）：wildcard 段必须是非空段，
// 与 Starlette 的 `[^/]+` 一致。
//
// ponytail: 不支持 {name...} 与 {$}。本包没有这种模式，真加进来会退化成
// 「漏判 -> 404」而不是误报 405；到那时按 Go 的通配规则补齐即可。
func matchesPath(patternPath, path string) bool {
	patternSegments := strings.Split(patternPath, "/")
	pathSegments := strings.Split(path, "/")
	if len(patternSegments) != len(pathSegments) {
		return false
	}
	for i, segment := range patternSegments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			if pathSegments[i] == "" {
				return false
			}
			continue
		}
		if segment != pathSegments[i] {
			return false
		}
	}
	return true
}

// ServeHTTP 让 Server 直接当 http.Handler 用。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

// objectOf 构造一个有序对象。
func objectOf(pairs ...canonical.ObjectPair) *canonical.Value {
	return canonical.NewObjectOf(pairs...)
}

// writeJSON 用 canonical 序列化并写出 JSON 响应。
//
// content-type 固定 "application/json"（FastAPI 的 JSONResponse 不带 charset）；
// content-length 显式写入：Starlette 会带这个头，而 Go 只在体量较小时自行补齐，
// 显式写才能让响应头集合稳定。
func writeJSON(w http.ResponseWriter, status int, body *canonical.Value) {
	payload := canonical.DumpsOrdered(body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, payload)
}

// writeNoContent 对应 FastAPI 的 Response(status_code=204)。
//
// 204 不能带 content-length/content-type：Starlette 对无实体状态码不写这两个头
// （STATUS_CODES_NO_BODY），Go 的 net/http 同样会剥掉。
func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// writeInternalError 复刻 Starlette 的兜底 500。
//
// 参照实现没有给 ManagementAPIError / 裸 ValueError 注册 exception handler，它们
// 逃出路由函数后由 ServerErrorMiddleware 处理：状态 500、content-type
// text/plain; charset=utf-8、体是固定的 "Internal Server Error"。
func writeInternalError(w http.ResponseWriter) {
	const body = "Internal Server Error"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, body)
}

// writeError 把 handler 返回的 error 翻译成 HTTP 响应。
//
// 映射规则来自 management_api.py 的 _update_config 与 FastAPI 的默认行为：
//
//	*httpError                    -> 该状态码 + {"detail": ...}
//	*configops.ConfigOperationError 顶层冒泡 -> Starlette 兜底 500（不是它的 status_code！）
//	其它（ManagementAPIError、裸 ValueError、任何未预期错误）-> 500 纯文本
//
// ConfigOperationError 顶层是 500 这点最容易写错：只有 _update_config 内的
// `except operations.ConfigOperationError` 才会把 status_code 变成 HTTP 状态码。
func writeError(w http.ResponseWriter, err error) {
	var validationErr *payloadValidationError
	if errors.As(err, &validationErr) {
		// Pydantic 校验失败：422 + {"detail": [ {type, loc, msg, input, ctx}, ... ]}。
		writeJSON(w, http.StatusUnprocessableEntity, validationErrorsValue(validationErr.errs))
		return
	}
	var httpErr *httpError
	if errors.As(err, &httpErr) {
		writeJSON(w, httpErr.status, objectOf(canonical.ObjectPair{Key: "detail", Value: canonical.NewString(httpErr.detail)}))
		return
	}
	writeInternalError(w)
}

// reload 触发一次热重载，对应 `await reload_config(state)`。
func (s *Server) reload() {
	if s.Reload != nil {
		s.Reload()
	}
}

// logger 返回管理面的日志出口；未注入时退回 slog.Default()。
func (s *Server) logger() *slog.Logger {
	if s != nil && s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// currentConfig 返回当前生效的配置。
//
// nil 的 CurrentConfig 回落到现场读盘：宿主还没接上 runtime 时也能工作，且读盘
// 与 reload 后的结果一致。
func (s *Server) currentConfig() (*config.RouterConfig, error) {
	if s.CurrentConfig != nil {
		if cfg := s.CurrentConfig(); cfg != nil {
			return cfg, nil
		}
	}
	if s.ConfigPath == "" {
		return nil, httpErrorf(http.StatusConflict, "未设置配置文件路径，无法读取配置版本")
	}
	return config.Load(s.ConfigPath)
}

// authorizedTaskConfig 解析本请求对**任务面**的权限，返回配置与「真正生效的工作空间」。
//
// 任务面比其余管理面宽一档：除完整权限外，还接受被钉死在某个工作空间的面板 key。
// 钉死的含义是**忽略请求头**——空间由 key 决定，绝不能让调用方用 X-AMKR-Workspace
// 换一个空间，那正是这个模式要防的事。
//
// 面板 key 的识别刻意放在这里而不是塞进 auth 包：auth 是不依赖 config 的纯函数，而
// 工作空间是本项目新增能力。往 auth 里加分支会让它的职责变模糊；在这里做则只需一次
// 配置查表，且「工作空间 key 给不了完整权限」是结构性成立的——这条路径**从不构造**
// ModeFull。
//
// 访问密钥在这里同样被拒绝：它是分发给外部使用者的推理凭据，能调哪些模型由它自己的
// 清单决定，与「管理某个空间的任务」无关。
func (s *Server) authorizedTaskConfig(r *http.Request) (*config.RouterConfig, string, error) {
	s.reload()
	cfg, err := s.currentConfig()
	if err != nil {
		return nil, "", err
	}
	if ctx := auth.Authenticate(s.Authorizer, r, cfg.LocalAPIKey); ctx != nil && ctx.IsFull() {
		return cfg, taskWorkspace(r), nil
	}
	// 完整权限没通过，再看是不是某个空间的面板 key。顺序不能反：面板 key 必须先排除
	// 在完整权限之外（它永远不满足上面的 IsFull），否则一个空间的面板就能改全局配置。
	if workspace := cfg.WorkspaceForAPIKey(auth.RequestAPIKey(r.Header)); workspace != "" {
		return cfg, workspace, nil
	}
	return nil, "", &httpError{status: http.StatusUnauthorized, detail: "本地 API key 验证失败"}
}

// authorizedConfig 对应 management_api.py:1117 的 _authorized_config。
//
// 全部 47 条路由都要求 full 权限（受限的推理凭据没有可用的管理路由），所以判定就是
// 「authorize 成功且 is_full」。
func (s *Server) authorizedConfig(r *http.Request) (*config.RouterConfig, error) {
	s.reload()
	cfg, err := s.currentConfig()
	if err != nil {
		return nil, err
	}
	ctx := auth.Authenticate(s.Authorizer, r, cfg.LocalAPIKey)
	if ctx == nil || !ctx.IsFull() {
		return nil, &httpError{status: http.StatusUnauthorized, detail: "本地 API key 验证失败"}
	}
	return cfg, nil
}

// configRevision 对应 management_api.py:1187 的 _config_revision。
//
// 先迁移再对 canonical（sort_keys=True、紧凑分隔符、ensure_ascii=False）形式取
// sha256：迁移是必须的，否则同一份配置在不同 config_version 下会算出不同摘要。
func configRevision(data *canonical.Value) (string, error) {
	migrated, err := config.MigrateConfigData(data)
	if err != nil {
		return "", err
	}
	return canonical.RevisionHash(migrated), nil
}

// managementConfigData 对应 management_api.py:1201 的 _management_config_data。
//
// 它**总是**从盘上重新读并迁移，而不是用运行时快照：config_revision 必须是磁盘
// 内容的指纹，否则并发写会算出过期版本号。
func (s *Server) managementConfigData() (*canonical.Value, error) {
	if s.ConfigPath == "" {
		return nil, httpErrorf(http.StatusConflict, "未设置配置文件路径，无法读取配置版本")
	}
	if !isRegularFile(s.ConfigPath) {
		return nil, httpErrorf(http.StatusConflict, "配置文件不存在: %s", s.ConfigPath)
	}
	data, err := config.LoadConfigData(s.ConfigPath)
	if err != nil {
		return nil, err
	}
	return config.MigrateConfigData(data)
}

// withRevision 对应 management_api.py:1197 的 _with_revision：把 body 各字段放在
// config_revision 之前。Python 用 `{**body, "config_revision": ...}`，字典展开保留
// 传入顺序，所以 config_revision 永远在最后。
func withRevision(data *canonical.Value, body *canonical.Value) (*canonical.Value, error) {
	revision, err := configRevision(data)
	if err != nil {
		return nil, err
	}
	out := canonical.NewObject()
	for _, key := range body.Obj.Keys() {
		child, _ := body.Obj.Get(key)
		out.SetKey(key, child)
	}
	out.SetKey("config_revision", canonical.NewString(revision))
	return out, nil
}

// updateWorkspaceTaskConfig 与 updateConfig 同形，只是鉴权放宽到「完整权限或某个
// 工作空间的面板 key」，并把真正生效的工作空间一并返回。
//
// 返回 workspace 是必要的，不只是顺手：面板 key 的空间由 key 决定、**忽略请求头**，
// 因此调用方在写完之后要按「实际写到哪个空间」去回读任务；用请求头里那个值会读错
// 空间（面板 key 配上伪造的 X-AMKR-Workspace 时尤其明显）。
//
// 与 updateConfig 的差别**只有鉴权那一步**：revision 比对、迁移副本、原子落盘、
// 错误映射全部复用同一段实现——两处各写一份是这类代码最容易漂移的地方，而漂移的
// 后果是「面板改任务丢了防并发保护」这种很难发现的问题。
func (s *Server) updateWorkspaceTaskConfig(
	r *http.Request,
	mutation func(data *canonical.Value, workspace string) error,
	revision *string,
) (*config.RouterConfig, string, error) {
	workspace := ""
	cfg, err := s.updateConfigCore(r, revision, func() (string, error) {
		_, resolved, err := s.authorizedTaskConfig(r)
		workspace = resolved
		return resolved, err
	}, mutation)
	return cfg, workspace, err
}

// updateConfig 对应 management_api.py:1133 的 _update_config。
//
// 顺序是关键，全部照抄参照实现：
//
//  1. 取 config_write_lock；
//  2. 先鉴权（未通过时磁盘一个字节都不动）；
//  3. 校验配置文件路径存在（否则 409）；
//  4. 在自定义的 edit 里**先比 config_revision，再迁移、再施加 mutation**——
//     版本不符时直接抛 409，mutation 根本不执行，这是防丢改动的核心；
//  5. 把错误按类型映射成 HTTP 状态码。
//
// revision 为 nil 表示请求没带版本号（老客户端），跳过比对。
func (s *Server) updateConfig(r *http.Request, mutation func(data *canonical.Value) error, revision *string) (*config.RouterConfig, error) {
	return s.updateConfigCore(r, revision, func() (string, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return "", err
		}
		return "", nil
	}, func(data *canonical.Value, _ string) error {
		return mutation(data)
	})
}

// updateConfigCore 是 updateConfig 与 updateWorkspaceTaskConfig 的共同实现。
//
// authorize 返回「本次请求真正生效的工作空间」（完整权限时为调用方请求头指定的那个，
// 面板 key 时为 key 钉死的那个）；它必须**在写锁与任何磁盘改动之前**跑完，否则未通过
// 鉴权的请求就已经触碰了配置。
func (s *Server) updateConfigCore(
	r *http.Request,
	revision *string,
	authorize func() (string, error),
	mutation func(data *canonical.Value, workspace string) error,
) (*config.RouterConfig, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	workspace, err := authorize()
	if err != nil {
		return nil, err
	}
	if s.ConfigPath == "" {
		return nil, httpErrorf(http.StatusConflict, "未设置配置文件路径，无法持久化修改")
	}
	if !isRegularFile(s.ConfigPath) {
		return nil, httpErrorf(http.StatusConflict, "配置文件不存在: %s", s.ConfigPath)
	}

	change, err := configservice.New(s.ConfigPath).Update(func(data *canonical.Value) error {
		if revision != nil {
			current, err := configRevision(data)
			if err != nil {
				return err
			}
			if *revision != current {
				return &managementAPIError{status: http.StatusConflict, message: "配置版本已变更，请刷新后重试"}
			}
		}
		// 与参照实现一致：在一个**独立**的迁移副本上施加 mutation，全部成功后再
		// 整体替换回 data。mutation 中途失败时 data 保持原样，配合 ConfigService
		// 的「失败不落盘」实现真正的原子性。
		editable, err := config.MigrateConfigData(data)
		if err != nil {
			return err
		}
		if err := mutation(editable, workspace); err != nil {
			return err
		}
		replaceObject(data, editable)
		return nil
	})
	if err != nil {
		return nil, translateUpdateError(err)
	}

	// 配置已落盘：令 mtime 缓存失效并重载运行时。
	s.reload()
	return change.NewConfig, nil
}

// v3Update 对应 management_api.py:522 的局部辅助函数 v3_update。
//
// 它与 updateConfig 的差别只有返回值：v3_update **重新读盘**取 config_revision，
// 而不是用 _update_config 返回的 change.new_config。两者正常情况下一致，但
// 「响应里的版本号来自磁盘」这一点是可观察契约，不能顺手合并。
func (s *Server) v3Update(r *http.Request, mutation func(data *canonical.Value) error, revision *string) (*canonical.Value, error) {
	if _, err := s.updateConfig(r, mutation, revision); err != nil {
		return nil, err
	}
	return s.managementConfigData()
}

// replaceObject 把 data 的内容整体替换成 replacement，等价于
// `data.clear(); data.update(replacement)`。
//
// 必须就地替换而不是让调用方换引用：ConfigService 持有的是同一个 *Value，只有就地
// 改写才能让落盘内容与 mutation 结果一致。
func replaceObject(data, replacement *canonical.Value) {
	if data == nil || !data.IsObject() || replacement == nil || !replacement.IsObject() {
		return
	}
	for _, key := range data.Obj.Keys() {
		data.DeleteKey(key)
	}
	for _, key := range replacement.Obj.Keys() {
		child, _ := replacement.Obj.Get(key)
		data.SetKey(key, child)
	}
}

// translateUpdateError 把 ConfigService.Update 返回的错误映射成对外错误。
//
// 逐条对应 management_api.py:1164-1175 的 except 链，顺序不能改：
//
//	ManagementAPIError             -> 原状态码
//	ConfigOperationError           -> 它的 status_code（默认 400）
//	KeyError/TypeError/ValueError  -> 400「配置校验失败: ...」
//	OSError                        -> 500「配置保存失败: ...」
//
// ConfigOperationError 在 Python 里是 ValueError 的子类却先被更窄的 except 捕获，
// 所以 Go 也必须先判它，否则会退化成「配置校验失败」前缀。
func translateUpdateError(err error) error {
	var httpErr *httpError
	if errors.As(err, &httpErr) {
		// mutation 里直接抛的 HTTPException 不被任何 except 捕获，原样变成响应。
		return err
	}
	var apiErr *managementAPIError
	if errors.As(err, &apiErr) {
		return &httpError{status: apiErr.status, detail: apiErr.message}
	}
	var opErr *configops.ConfigOperationError
	if errors.As(err, &opErr) {
		return &httpError{status: opErr.StatusCode, detail: opErr.Error()}
	}
	var pyErr *configops.PyError
	if errors.As(err, &pyErr) {
		return httpErrorf(http.StatusBadRequest, "配置校验失败: %s", pyErr.Error())
	}
	var cfgErr *config.ConfigError
	if errors.As(err, &cfgErr) {
		return httpErrorf(http.StatusBadRequest, "配置校验失败: %s", cfgErr.Error())
	}
	var internalErr *config.InternalError
	if errors.As(err, &internalErr) {
		return httpErrorf(http.StatusBadRequest, "配置校验失败: %s", internalErr.Error())
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return httpErrorf(http.StatusInternalServerError, "配置保存失败: %s", pathErr.Error())
	}
	if errors.Is(err, os.ErrPermission) {
		return httpErrorf(http.StatusInternalServerError, "配置保存失败: %s", err.Error())
	}
	return err
}

// isRegularFile 报告路径是否是一个存在的普通文件（Python 的 Path.is_file()）。
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}
