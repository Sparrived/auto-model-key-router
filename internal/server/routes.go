package server

import (
	"net/http"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/webui"
)

// buildHandler 组装整棵路由树。
//
// 与参照实现的对应关系：
//
//	register_management_api(app, ...)   app.py:138  -> "/api/" 交给 api.Server
//	register_ops_api(app, ...)          app.py:145  -> api.Server.OpsEnabled（同一棵 mux）
//	register_webui(app, ...)            app.py:147  -> "/ui/" 交给 webui.Handler
//	@app.head("/")                      app.py:149  -> "/"（只接受 HEAD）
//	@app.get("/health")                 app.py:153  -> "/health"
//	@app.get("/v1/models")              app.py:183  -> "/v1/models"
//	@app.get("/metrics")                app.py:212  -> "/metrics"
//	@app.get("/metrics/requests")       app.py:235  -> "/metrics/requests"
//	@app.get("/metrics/series")         app.py:279  -> "/metrics/series"
//	app.add_api_route("/v1/{path:path}") app.py:371 -> "/v1/" 交给 proxy
//	@app.websocket("/ws/events")        app.py:325  -> "/ws/events" 交给 eventbus
//	register_websocket_proxy(app, ...)  app.py:374  -> "/v1/" 的升级请求交给 wsproxy
//
// 运维面**没有**独立的前缀分支：与参照实现一样，7 条 /api/* 运维路由注册在管理 API
// 的同一棵 mux 上（api.Server.Handler 在 OpsEnabled 为真时调用 RegisterOps），所以
// 下面只有一条 "/api/" 分支。同一批模式注册两遍会互相遮蔽（外层优先），必须避免。
//
// # 为什么都注册成「方法无关」的模式
//
// Go 1.22 的 ServeMux 有两处与 FastAPI 不同的默认行为，都会改变对外响应：
//
//   - `GET /health` 这个模式**也**匹配 HEAD 请求（实测），而 FastAPI 只注册 GET；
//     参照实现里 `HEAD /health` 是 405 + allow: GET。若用 "GET /health"，Go 会返回
//     200 空体，health 探针会得到相反的结论。
//   - 方法不匹配时 ServeMux 自动回 405 纯文本 "Method Not Allowed"（并带上它自己
//     算出的 Allow），而 FastAPI 回 {"detail":"Method Not Allowed"}。
//
// 因此这里注册路径模式（无方法），在处理器里自行判定方法：两种差异同时消失，
// 而且 405 的 Allow 头由我们显式给出，与参照实现一致（GET 路由 -> "GET",
// `HEAD /` -> "HEAD"）。
func (a *App) buildHandler() http.Handler {
	mux := http.NewServeMux()

	// 管理 API 的 47 条路由（OpsEnabled 为真时 api 会在同一棵 mux 上再注册 7 条运维
	// 路由）：内部 mux 自带 "/" 兜底（404 {"detail":"Not Found"}），与参照实现的
	// FastAPI 兜底一致。
	apiHandler := a.api.Handler()
	mux.Handle("/api/", apiHandler)
	// "/api"（无尾斜杠）：FastAPI 里这是 404（路由是逐个注册的，没有挂载前缀），而
	// ServeMux 会把子树模式的 "/api" 请求 301 重定向到 "/api/"。显式注册精确模式
	// 挡掉重定向，让内层兜底给出同样的 404。
	mux.Handle("/api", apiHandler)

	if handler, mounted := webui.Handler(a.options.WebUIAssets, a.webuiEnabled); mounted {
		path := webui.Path(a.options.MountPrefix)
		// webui.Handler 期望收到**已去掉挂载前缀**的路径（见 internal/webui 的测试
		// 说明），所以这里显式 StripPrefix。
		mux.Handle(path+"/", http.StripPrefix(path, handler))
		// 价格目录挂在同一个前缀下（理由见 pricing.go：这里在所有冻结路由清单之外）。
		// 它比 "/ui/" 更精确，因此 ServeMux 优先选中它，不会落到静态文件处理器上
		// 去找一个磁盘上并不存在的 pricing.json。
		mux.HandleFunc(path+pricingPath, a.handlePricing)
		// 自更新入口（理由见 update.go）：同样挂在 /ui/ 之下，避开那 47+7 条
		// 已发布 /api 路由。
		mux.HandleFunc(path+updateStatusPath, a.handleUpdateStatus)
		mux.HandleFunc(path+updateApplyPath, a.handleUpdateApply)
		// 工作空间用量读数（理由见 workspace_usage.go）：这是本项目自己的响应形状，
		// 不能混进 /metrics 系列，因此同样挂在 /ui/ 之下。
		mux.HandleFunc(path+workspaceUsagePath, a.handleWorkspaceUsage)
		// 嵌入方面板的读数（理由见 workspace_panel.go）：与上一条形状相同，但只认
		// 面板 key，且只回 key 所属那**一个**空间的用量。
		mux.HandleFunc(path+workspacePanelPath, a.handleWorkspacePanel)
		// 访客看板的读数（理由见 accesskey_usage.go）：只认访问密钥，且只回**这一把
		// key** 自己的用量。身份完全由凭据决定，不接受任何参数指定密钥。
		mux.HandleFunc(path+accessKeyUsagePath, a.handleAccessKeyUsage)
		// Key 用量读数（理由见 key_usage.go）：回答「这些流量是哪把 Key 出去的」。
		// 与 workspace-usage 同样挂 /ui/，因为 /metrics 的形状已发布、不能加字段。
		mux.HandleFunc(path+keyUsagePath, a.handleKeyUsage)
		a.webuiMounted = true
	}

	a.getRoute(mux, "/health", a.handleHealth)
	// /v1/models 是**混合路径**：GET 归 app 面，POST/PUT/PATCH/DELETE 落进
	// /v1/{path:path} 通配路由，HEAD/OPTIONS 等才是 405。参照实现里
	// `@app.get("/v1/models")` 比通配路由更精确，只吃掉 GET；通配路由的方法集合是
	// ["GET","POST","PUT","PATCH","DELETE"]（app.py:371-373），因此其余四个方法会走到
	// proxy 并得到 proxy 的错误（实测：POST /v1/models -> 404「模型  未配置」）。
	// 若这里简单写成「非 GET 一律 405」，POST /v1/models 就会从 404 变成 405——
	// 这是对外可观测的行为变化，不能改。
	mux.HandleFunc("/v1/models", a.handleModelsRoute)
	a.getRoute(mux, "/metrics", a.handleMetrics)
	a.getRoute(mux, "/metrics/requests", a.handleMetricsRequests)
	a.getRoute(mux, "/metrics/series", a.handleMetricsSeries)

	// 代理通配：只认 /v1/ 之下，path 由处理器去掉前缀后传给 proxy。
	mux.HandleFunc("/v1/", a.handleProxyRoute)
	// "/v1"（无尾斜杠）：参照实现里 404（Starlette 的 "/v1/{path:path}" 不匹配
	// "/v1"），而 ServeMux 会 307 重定向到 "/v1/"。显式注册精确模式挡掉重定向。
	mux.HandleFunc("/v1", a.handleRoot)

	// GET /ws/events（app.py:325）。**没有开关**：参照实现把它无条件注册在
	// create_app 里，webui_enabled / ops_enabled 都不影响它（app.py:325-349 不在任何
	// if 里，实测：webui 与 ops 全关时 /ws/events 的升级仍然被接受）。因此这里也不
	// 引入 Options 上的新开关。普通 HTTP 请求不匹配 websocket 路由（Starlette 的
	// WebSocketRoute 只在 scope["type"]=="websocket" 时匹配），落到兜底 404——由
	// handleWSEvents 自己给出，与参照实现的 ws_events_http_* 行为一致。
	mux.HandleFunc("/ws/events", a.handleWSEvents)

	mux.HandleFunc("/", a.handleRoot)
	return mux
}

// proxyMethods 是参照实现给 /v1/{path:path} 注册的方法集合（app.py:371-373）。
var proxyMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// proxyMethodList 是通配路由 405 的 Allow 取值。
//
// **已知分歧（有实测证据）**：参照实现的 Allow 由 Starlette 用一个集合拼出来，顺序随
// PYTHONHASHSEED 变化——同一个 HEAD /v1/chat/completions 连续三次运行分别是
// "PATCH, DELETE, GET, PUT, POST"、"PUT, PATCH, GET, POST, DELETE"、
// "PUT, POST, GET, DELETE, PATCH"。那串字节因此不是稳定契约，不该被别人当作契约依赖。
// 这里固定成一个有序列表。
//
// 多方法 405 只出现在通配路由上；/v1/models、/health、/metrics* 与 "/" 的 Allow
// 都是单元素（Python 侧同样稳定），它们是对外契约。
const proxyMethodList = "GET, POST, PUT, PATCH, DELETE"

// handleProxyRoute 处理 /v1/{path}：WebSocket 升级交给 wsproxy，其余只有参照实现
// 注册的那五个方法才进 proxy，其它方法（HEAD/OPTIONS/TRACE）是 405。
//
// 升级判定必须排在方法判定**之前**：参照实现的两条路由在 ASGI 上是互斥的——
// websocket scope 只匹配 websocket 路由（它没有方法集合），http scope 才匹配
// `methods=["GET","POST","PUT","PATCH","DELETE"]` 的那条。实测（真实 uvicorn）：
// 带升级头的 `GET /v1/does-not-exist` 返回 101（走 websocket 路由）。
func (a *App) handleProxyRoute(w http.ResponseWriter, r *http.Request) {
	if isWebSocketUpgrade(r) {
		a.handleWSProxy(w, r)
		return
	}
	if !proxyMethods[r.Method] {
		writeMethodNotAllowed(w, proxyMethodList)
		return
	}
	a.handleProxy(w, r)
}

// handleModelsRoute 按方法把 /v1/models 分派到 app 面或 proxy（见 buildHandler 的说明）。
//
// 升级请求同样要先拦：参照实现里 `/v1/models` 只注册了 HTTP 方法，websocket 升级会落到
// 通配的 websocket 路由 `/v1/{path:path}` 上，path = "models"。Go 的 ServeMux 里
// "/v1/models" 比 "/v1/" 更精确，不在这里拦就会走进 app 面。
func (a *App) handleModelsRoute(w http.ResponseWriter, r *http.Request) {
	if isWebSocketUpgrade(r) {
		a.handleWSProxy(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet:
		a.handleModels(w, r)
	case proxyMethods[r.Method]:
		a.handleProxy(w, r)
	default:
		// 参照实现在这里报的 Allow 只有 "GET"：/v1/models 比通配路由更精确，
		// Starlette 的部分匹配只列它自己的方法。
		writeMethodNotAllowed(w, http.MethodGet)
	}
}

// getRoute 注册一条只接受 GET 的路由，并复刻 FastAPI 对其它方法的 405。
func (a *App) getRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		handler(w, r)
	})
}

// handleRoot 处理没有匹配到具体路由的请求。
//
// 对应 FastAPI/Starlette 的两条兜底：
//
//   - "/" 只注册了 HEAD（app.py:149），其它方法是 405 + allow: HEAD；
//   - 其余路径是 404 {"detail":"Not Found"}（Starlette 的默认 404，与 api 包内部的
//     兜底同形）。
//
// "/v1" 也走这里（见 buildHandler），从而与参照实现的 404 对齐。
func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeNotFound(w)
		return
	}
	if r.Method == http.MethodHead {
		// app.py:149-151 的 `Response(status_code=204)`：204 不带 content-type 与
		// content-length。
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeMethodNotAllowed(w, http.MethodHead)
}

// handleProxy 把 /v1/{path} 交给 internal/proxy，path 不含 "/v1/" 前缀
// （对应 app.py:371-373 的 `"/v1/{path:path}"` 与 handle_proxy_request(path, ...)）。
func (a *App) handleProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	// app.py:351-369 的包装层：进入时记一次活跃请求，退出时释放。
	//
	// Go 侧比 Python 简单：proxy.Handle **同步**写完整个响应（流式也写完才返回），
	// 所以 defer 释放天然覆盖整条流，不需要 _wrap_active_stream 那样给流式响应换
	// 迭代器。
	if adapter := a.currentMetricsAdapter(); adapter != nil {
		adapter.AcquireActive()
		// app.py:355 的 `_metrics_dirty.set()`：**请求一进来**就置位，而不是等写库。
		// 它让订阅者马上看到 active_requests 变了（长流式请求期间唯一的观测手段）。
		// 没有客户端时这次置位不产生任何快照（Broadcaster.Tick 的门禁），代价只是一次
		// 非阻塞的通道发送。
		a.markMetricsDirty()
		defer adapter.ReleaseActive()
	}
	a.proxy.Handle(w, r, path)
}
