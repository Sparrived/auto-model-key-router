package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/api"
	"github.com/Sparrived/auto-model-key-router/internal/auth"
	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/eventbus"
	"github.com/Sparrived/auto-model-key-router/internal/health"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/pricing"
	"github.com/Sparrived/auto-model-key-router/internal/proxy"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
	"github.com/Sparrived/auto-model-key-router/internal/selfupdate"
	"github.com/Sparrived/auto-model-key-router/internal/upstream"
	"github.com/Sparrived/auto-model-key-router/internal/webui"
	"github.com/Sparrived/auto-model-key-router/internal/wsproxy"
)

// Options 是装配一个服务所需的全部输入。
//
// 所有可选字段的 nil 语义都与参照实现对齐：nil 表示「跟随配置 / 用默认实现」。
type Options struct {
	// ConfigPath 是配置文件路径，对应 create_app 的 config_path 参数
	// （app.py:52）。空串表示不做热重载、/health 也不报 config_path
	// （app.py:85-87）。
	ConfigPath string
	// Config 是启动时载入的配置快照，对应 create_app 的 config 参数。
	Config *config.RouterConfig
	// Version 是进程版本号，对应 `from . import __version__`（app.py:15）。
	// 进入 /health 与版本检查。
	Version string
	// WebUIAssets 是**以 WebUI 资产目录为根**的文件系统。
	//
	// 做成注入参数而不是在这里 embed：`//go:embed` 的模式不能引用父目录，而资产在
	// webui/ 下（见根包的 webui_assets.go）。nil 表示资产缺失
	// （webui.Available 为假，/health 的 webui_available 报 false）。
	WebUIAssets fs.FS
	// WebUIEnabled 覆盖配置里的 webui_enabled；nil 表示跟随配置
	// （app.py:82、app.py:147 的 `webui=None` 语义）。
	WebUIEnabled *bool
	// OpsEnabled 覆盖配置里的 ops_enabled；nil 表示跟随配置（app.py:142-143）。
	//
	// 它同时决定 /health 报出的 ops_enabled 与**运维面 7 条路由是否注册**：装配时
	// 原样传给 api.Server.OpsEnabled，由它在自己的 mux 上注册 /api/logs、/api/tool、
	// /api/tool/webui、/api/service/{action} 与三条 /api/integrations*。
	// 与 Python 相同：运维路由与管理路由注册在同一个应用上，共用同一套鉴权与错误形状。
	OpsEnabled *bool
	// Authorizer 覆盖鉴权实现，对应 create_app(authenticator=...)（app.py:84）。
	// nil 表示 internal/auth 的默认实现（本地 key = full）。
	//
	// 注入自定义 Authorizer 会**一并停用**访问密钥与工作空间推理凭据的识别：那两者
	// 由 proxy 在默认鉴权之后自行匹配配置，而自定义鉴权器接管后这条路径不再走到。
	// 这是有意的取舍——注入者要的就是「鉴权全由我说了算」。
	//
	// 同一个鉴权器会同时作用于 app 面、proxy 与管理 API：参照实现只有
	// app.state.authenticator 一份。
	Authorizer auth.Authorizer
	// CheckUpdate 覆盖版本检查，对应 update.check_latest_version。
	//
	// nil 表示用 internal/updatecheck 的真实实现（会发网络请求）。注入它主要是为了
	// 让测试不联网。**UpdateAvailable 由适配器按 latest/current 推导**，
	// 见 updatecheck.go。
	CheckUpdate func(timeout float64) api.UpdateCheckResult
	// PricingFetch 覆盖 models.dev 价格目录的取回。
	//
	// **nil 表示完全不出网**，而不是"用真实实现"。价格目录的刷新是进程启动时就起的
	// 后台循环（用户要求"定期更新"），若默认联网，每个装配 App 的测试都会去打
	// models.dev——既慢，又让 CI 依赖外网。因此真实性由调用方显式选择：
	// cmd/amkr 传 pricing.HTTPFetcher(nil)。
	//
	// 为 nil 时 /ui/pricing.json 仍然注册（路由不随开关增删），但因为没有目录而回
	// 503，界面显示"无定价"——这正是"不静默算成 $0"的要求。
	PricingFetch pricing.Fetcher
	// Logger 交给 internal/proxy 做结构化日志；nil 时按 AMKR_LOG_FORMAT 选择格式。
	Logger *slog.Logger
	// AccessLogger 接收每个 HTTP 请求的访问日志，对应参照实现里 uvicorn 的
	// `uvicorn.access`（uvicorn_log_config 把它接到 log_file_path）。nil 表示不记访问
	// 日志，此时路由树不做任何包装。
	//
	// 单独一个字段而不是复用 Logger：参照实现里两者是**不同的 logger**（access 走
	// uvicorn.access、应用侧走 auto_model_key_router.app），落到文件后 logger 名要能
	// 区分，因此不能共用同一个 *slog.Logger。
	AccessLogger *slog.Logger
	// MountPrefix 是嵌入到宿主时的挂载前缀（独立运行时为空串）。它只影响 /health
	// 报出的 webui_path（app.py:418 的 mount_path 语义）。
	MountPrefix string
	// SelfUpdate 执行自更新（下载新版 → 校验 → 就地替换 → 启动收尾助手）。
	//
	// 与 PricingFetch 一样，**nil 表示不出网、也不提供该能力**，而不是"用真实实现"：
	// 自更新会真的替换磁盘上的可执行文件并重启服务，绝不能在测试里默认开启。为 nil 时
	// /ui/update/apply 回 501（响亮失败），/ui/update/status 报 available=false。
	//
	// 参数是当前可执行文件路径，返回更新结果。
	SelfUpdate func(executable string) (selfupdate.Result, error)
	// RequestShutdown 请求本进程优雅关停（自更新换完文件后由收尾助手接管重启）。
	//
	// nil 表示不关停：此时更新只换文件，界面会提示需要手动重启。抽成接缝是为了让
	// internal/server 不依赖 signal/os.Exit 这类进程级动作。
	RequestShutdown func()
}

// App 是一个装配完成、可以开始处理请求的服务实例。
//
// 它可以并发使用：Handler 返回的 http.Handler 是并发安全的，热重载由 reloadMu
// 串行化，共享的 RuntimeManager 自带锁。
type App struct {
	options Options

	proxy   *proxy.Handler
	manager *runtime.RuntimeManager
	api     *api.Server
	handler http.Handler
	// wsProxy 处理升级到 /v1/{path} 的连接（app.py:374）。它是**无状态**的转发器，
	// 每条连接的生命周期都在它内部，因此整个 App 共用一个实例。
	wsProxy *wsproxy.Handler
	// eventBus 是 /ws/events 的订阅者集合（app.py:99）。
	eventBus *eventbus.Bus
	// broadcaster 把 dirty 信号、节流状态机与快照构建接起来（throttle.go:145）。
	broadcaster *eventbus.Broadcaster
	// broadcastStart 是节流虚拟时钟的原点（throttle.go 的 Loop 文档要求它对齐循环
	// 启动时刻）。这里取在 New 里，比循环真正启动略早——方向是安全的，理由见
	// startMetricsBroadcast：早一点只会让第一次超时判定更容易成立。
	broadcastStart time.Time
	// metricsDirty 是「有写入」的电平信号，容量 1、非阻塞发送，见 markMetricsDirty。
	metricsDirty chan time.Duration
	// broadcastCtx/Cancel/Done 管理广播 goroutine 的生命周期，对应 lifespan 里
	// `_metrics_broadcast_task` 的创建与 cancel（app.py:64-77）。
	broadcastCtx    context.Context
	broadcastCancel context.CancelFunc
	broadcastDone   chan struct{}

	// configPath 是**绝对化之后**的配置路径，对应 app.state.config_path
	// （app.py:85-87 的 `str(Path(config_path).resolve())`）。
	configPath string
	// webuiAvailable / webuiMounted / webuiEnabled 对应 webui_status 的三个读数
	// （webui.py:48-70）。mounted 与 enabled 的区别是刻意的：改了开关需要重启才生效。
	webuiAvailable bool
	webuiMounted   bool
	webuiEnabled   bool
	// opsEnabled 对应 app.state.ops_enabled（app.py:146）。
	opsEnabled bool
	// pricing 是 models.dev 价格目录（供 WebUI 估算成本）。
	//
	// 它是**本项目的扩展**，参照实现没有对应物（Python 版只记 token、不算钱）。
	// 目录只驻内存，与指标库无关，因此热重载不重建它。
	pricing *pricing.Catalog
	// pricingStop 停掉价格目录的定期刷新循环（见 Close）。
	pricingStop func()

	// reloadMu 对应 app.state.config_reload_lock（app.py:89）。
	reloadMu sync.Mutex
	// configMtime 对应 app.state.config_mtime；零值等价 Python 的 0.0（「拿不到
	// mtime」），此时不做热重载（app.py:446）。
	configMtime time.Time

	closeOnce sync.Once
}

// New 装配服务。返回的 App 已经可以处理请求；调用方负责在退出时调用 Close。
//
// 构造顺序与 create_app 一致（app.py:82-147）：先解析开关与鉴权，再建 key pool、
// 指标库、上游客户端与运行时管理器，最后注册路由。
//
// 与参照实现的差别只有错误处理：Python 在构造期抛异常会直接让进程退出，而 Go 侧
// 只有指标库打不开（磁盘不可写）会返回 error，其余构造步骤不会失败。
func New(options Options) (*App, error) {
	if options.Config == nil {
		return nil, errors.New("server: Options.Config 不能为空")
	}
	app := &App{options: options}
	app.configPath = absoluteConfigPath(options.ConfigPath)
	app.configMtime = configMtime(app.configPath)
	app.opsEnabled = options.Config.OpsEnabled
	if options.OpsEnabled != nil {
		app.opsEnabled = *options.OpsEnabled
	}
	app.webuiEnabled = options.Config.WebUIEnabled
	if options.WebUIEnabled != nil {
		app.webuiEnabled = *options.WebUIEnabled
	}
	app.webuiAvailable = webui.Available(options.WebUIAssets)

	keyPool := keypool.New(options.Config,
		keypool.NewCapabilityStore(options.Config.EndpointCapabilitiesPath), nil)
	store, err := metrics.Open(options.Config.MetricsDBPath)
	if err != nil {
		return nil, err
	}
	adapter := newMetricsAdapter(store)
	// 事件总线与「写指标后唤醒广播」的回调（app.py:99、:134）。store 在这里先挂上
	// 回调，新库在 reload 里同样要挂（app.py:468，见 bindMetricsDirty）。
	app.eventBus = eventbus.New()
	app.eventBus.Logger = options.Logger
	app.broadcaster = &eventbus.Broadcaster{
		Bus:      app.eventBus,
		Snapshot: app.broadcastMetricsSnapshot,
		Logger:   options.Logger,
	}
	// dirty 通道与虚拟时钟原点必须在**注册回调之前**就位：回调可能在任意 goroutine
	// 上触发（metrics.Store.Record），先建后挂才没有并发窗口。原点的取法见
	// startMetricsBroadcast 的说明（取早不取晚）。
	app.metricsDirty = make(chan time.Duration, 1)
	app.broadcastStart = time.Now()
	app.bindMetricsDirty(store)
	client := newUpstreamClient(options.Config)
	app.manager = runtime.NewRuntimeManager(
		runtime.NewRuntimeResources(options.Config, keyPool, adapter, client),
	)

	proxyOptions := proxy.Options{Logger: options.Logger}
	if options.Authorizer != nil {
		// 参照实现只有一个 authenticator（app.state.authenticator），proxy 也走它
		// （proxy_handler.py 的 authorize 读 app.state）。宿主的钩子只回答「是不是
		// 完整权限」，受限凭据（访问密钥、工作空间推理 key）是 AMKR 自己的配置能力，
		// 宿主体系里没有对应概念，因此这里不透传。
		//
		// 宿主自带的身份体系里也没有「工作空间凭据」这个概念，因此 Workspace 留空：
		// 空间仍由 X-AMKR-Workspace 头选择。
		//
		// **已知取舍**：注入自定义 Authorizer 时，访问密钥与工作空间推理凭据都不再被
		// 识别（宿主接管了整条判定链）。这是有意的——宿主既然自带身份体系，AMKR 就不该
		// 在旁边再开两条它管不到的通道。
		authorizer := options.Authorizer
		proxyOptions.Authorizer = func(r *http.Request, localAPIKey string) *proxy.AuthorizerResult {
			if authorizer(r, localAPIKey) == nil {
				return nil
			}
			return &proxy.AuthorizerResult{}
		}
	}
	app.proxy = proxy.New(app.manager, &switchableSink{app: app}, proxyOptions)
	// WS /v1/{path}：注入点直接绑定 proxy.Handle，签名逐字相同，不需要适配层
	// （app.py:374 的 register_websocket_proxy(app, proxy)）。
	app.wsProxy = &wsproxy.Handler{Proxy: app.proxy.Handle, Logger: options.Logger}

	app.api = &api.Server{
		ConfigPath:        app.configPath,
		Reload:            app.reload,
		CurrentConfig:     func() *config.RouterConfig { return app.manager.Current().Config },
		Authorizer:        options.Authorizer,
		CheckUpdate:       options.CheckUpdate,
		KeyStats:          app.keyStats,
		AccessKeyLastUsed: app.accessKeyLastUsed,
		// 运维面（7 条路由）与 /api/tool 的读数。OpsEnabled 为零值时 api 一条 ops
		// 路由都不注册（落进兜底 404），因此**必须**显式传入，否则 /api/logs 等
		// URL 在装配后的服务上完全不可达。
		OpsEnabled:  app.opsEnabled,
		Version:     options.Version,
		WebUIStatus: app.webUIStatus,
		// 以下五处是 internal/api 预留的实现接缝。不接的话对应路由会响亮失败（500 并点名
		// 接缝），而不是静默成功——但那样这些功能就真的不可用，所以必须在此显式接上。
		// 全部集中在 ops_seams.go 里说明与做签名断言。
		RunServiceAction:             runServiceActionForSeam,
		ProbeKeyCapability:           proberForSeam.ProbeKeyCapability,
		ProbeProviderKeyCapabilities: proberForSeam.ProbeProviderKeyCapabilities,
		ProbeKeyAvailability:         proberForSeam.ProbeKeyAvailability,
		Integrations:                 opsIntegrations(),
	}
	if app.api.CheckUpdate == nil {
		app.api.CheckUpdate = defaultCheckUpdate(options.Version)
	}

	// 价格目录（models.dev）：只驻内存，独立于指标库，因此不受热重载影响。
	// PricingFetch 为 nil 时 Catalog 仍然可用，但没有任何目录 → /ui/pricing.json
	// 回 503（不联网，见 Options.PricingFetch）。
	app.pricing = newPricingCatalog(options.PricingFetch)
	// 定期刷新循环（用户要求"缓存可以，但是需要定期更新"）：立刻预热一次、此后每
	// TTL 复验一次。**不依赖有人打开界面**，所以冷启动后的首个请求就有价格。
	// 它在 handler 装配之前起，保证路由生效时目录已经在路上。
	app.pricingStop = app.pricing.Start()

	// 访问日志包在**整棵树**外面，对应 uvicorn 在协议层为每个请求记一行——它不关心
	// 请求最终落到哪条路由，404/405 也照样记（历史日志里 404、503 都有 access 行）。
	app.handler = app.accessLog(app.buildHandler())
	// 广播循环最后启动：它要读 app.eventBus 与 app.broadcaster，而且必须在 Handler
	// 可用之前就绪（第一个 /ws/events 连接会立刻触发一次 metrics_snapshot 广播）。
	// 对应 lifespan 启动时创建 _metrics_broadcast_task（app.py:64-66）。
	app.startMetricsBroadcast()
	return app, nil
}

// Handler 返回注册了全部路由的 http.Handler。
func (a *App) Handler() http.Handler { return a.handler }

// Manager 暴露运行时管理器，供嵌入方与测试观测（生产代码不需要它）。
func (a *App) Manager() *runtime.RuntimeManager { return a.manager }

// Close 关停服务：先挡住新请求并等待在途请求（含流式）结束，再关闭各代独占的
// 连接池与指标库。
//
// 对应 lifespan 的 finally（app.py:69-78）：先取消指标广播任务，再
// `await runtime_manager.close()`。顺序不可交换的理由见 runtime.RuntimeManager.Close
// 与 stopMetricsBroadcast 的说明。
func (a *App) Close() error {
	var err error
	a.closeOnce.Do(func() {
		a.stopMetricsBroadcast()
		// 停掉价格目录的定期刷新并等它退出：它可能正在读一个 4.7 MB 的响应体，
		// 不等它结束就返回会让测试进程在 goroutine 仍持有连接时退出。
		if a.pricingStop != nil {
			a.pricingStop()
		}
		err = a.manager.Close()
	})
	return err
}

// keyStats 对应 management_api 里的 `metrics.key_stats(...)` 调用。
//
// 用**当前代**的指标库：参照实现的管理路由拿的是运行时快照
// （`state.runtime_manager.current.metrics`），热重载换库后应当读新库。
func (a *App) keyStats(modelID, keyName string, hours *float64) (*canonical.Value, error) {
	adapter := a.currentMetricsAdapter()
	if adapter == nil {
		return nil, errors.New("server: 指标库未装配")
	}
	return adapter.store.KeyStats(modelID, keyName, hours)
}

func (a *App) accessKeyLastUsed() (map[string]string, error) {
	// 持有租约直到查询结束，避免并发热换库关闭正在读取的指标库。
	lease, err := a.manager.Acquire()
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	store := metricsStoreOf(lease.Resources)
	if store == nil {
		return nil, errors.New("server: 指标库未装配")
	}
	return store.AccessKeyLastUsed()
}

// currentMetricsAdapter 返回**当前代**的指标适配器。
//
// proxy.New 只接受一次性的 MetricsSink（见 internal/proxy/lifecycle.go 的说明），
// 而热重载会换掉指标库；switchableSink 通过这里在每次写入时重新取当前代，行为与
// 参照实现的「从租约里取 metrics」一致。
func (a *App) currentMetricsAdapter() *metricsAdapter {
	if a.manager == nil {
		return nil
	}
	resources := a.manager.Current()
	if resources == nil {
		return nil
	}
	adapter, _ := resources.Metrics.(*metricsAdapter)
	return adapter
}

// webUIStatus 返回 webui_status(app) 的输入（webui.py:48-70）。
//
// `/health` 与 `/api/tool` 共用它，保证两处对同一份状态用同一套语义。
//
// enabled 的取值与参照实现一致：**优先读当前运行时配置**里的 webui_enabled
// （webui.py:73-78 的 _configured），只有拿不到 runtime 快照时才回退到启动时的开关。
// 因此「改了开关但没重启」会表现为 enabled 变了、mounted 没变——这正是运维要找的
// 信号（app.py:82 的注释）。
func (a *App) webUIStatus() health.WebUI {
	enabled := a.webuiEnabled
	if a.manager != nil {
		if resources := a.manager.Current(); resources != nil && resources.Config != nil {
			enabled = resources.Config.WebUIEnabled
		}
	}
	return health.WebUI{
		Available:   a.webuiAvailable,
		Enabled:     enabled,
		Mounted:     a.webuiMounted,
		MountPrefix: a.options.MountPrefix,
	}
}

// switchableSink 把 proxy 的指标写入转发到当前代。
//
// 为什么需要它，而不是直接传某一代的适配器：RuntimeManager 在热重载时会比较
// 「这一代的 Metrics 与其它代是否是同一个对象」来决定能否关闭它
// （runtime/manager.go 的 closeUnused）。如果所有代共用同一个 sink，指标库就永远
// 不会被关闭。因此每一代各自持有 metricsAdapter（可被正确关闭），proxy 侧则拿一个
// 稳定对象在写入时转发。
type switchableSink struct{ app *App }

// Record 实现 proxy.MetricsSink。
func (s *switchableSink) Record(ctx context.Context, record proxy.MetricRecord) error {
	adapter := s.app.currentMetricsAdapter()
	if adapter == nil {
		return nil
	}
	return adapter.Record(ctx, record)
}

// newUpstreamClient 构造访问上游的 HTTP 客户端。
//
// 两个超时的来源不同（见 proxy/retry.go:503-511）：
//   - RequestTimeout（config.request_timeout）是**非流式**整请求超时；
//   - FirstByteTimeout（config.stream_first_byte_timeout）是**流式**等响应头的窗口。
//
// 参照实现把后者的窗口放在 asyncio.timeout 里（proxy_handler.py:530-535），Go 侧
// 由 upstream.Client 承担，因此装配时必须把它一起传进去——否则流式的首字节窗口
// 会退化成「没有窗口」。
func newUpstreamClient(cfg *config.RouterConfig) *upstream.Client {
	return upstream.New(upstream.Config{
		RequestTimeout:   secondsDuration(cfg.RequestTimeout),
		FirstByteTimeout: secondsDuration(cfg.StreamFirstByteTimeout),
	})
}

// secondsDuration 把浮点秒折算成 time.Duration。
//
// 不能直接乘 time.Second：那会把 0.5 秒截断成 0，从而让超时立即触发（同一个坑在
// proxy.secondsToDuration 里有详细说明）。
func secondsDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

// absoluteConfigPath 复刻 app.py:85-87 的路径归一。
//
// 空路径保持空串（表示不做热重载）；否则转成绝对路径。filepath.Abs 与
// `Path(...).resolve()` 一样会清掉 "." / ".." 与多余的斜杠。
func absoluteConfigPath(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}

// configMtime 对应 app.py:434-440 的 _config_mtime：拿不到时返回零值（等价 0.0）。
func configMtime(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
