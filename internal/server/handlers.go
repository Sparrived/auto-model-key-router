package server

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"

	"github.com/Sparrived/auto-model-key-router/internal/auth"
	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/health"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// authFailureMessage 是三处鉴权失败共用的文案（app.py:192、224、259）。
const authFailureMessage = "本地 API key 验证失败"

// handleHealth 对应 app.py:153-181 的 GET /health。
//
// 响应体由 internal/health 构造（它已经是逐字节锁定的冻结契约），这里只负责取数：
// 热重载、租约、当前配置与 key pool 读数。
func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	a.withLease(w, func(resources *runtime.RuntimeResources) {
		writeJSON(w, http.StatusOK, health.Build(health.Inputs{
			Version:     a.options.Version,
			ConfigPath:  a.configPath,
			LocalAPIKey: resources.Config.LocalAPIKey,
			OpsEnabled:  a.opsEnabled,
			KeyPool:     keyPoolOf(resources),
			// WebUI 状态与 /api/tool 共用同一个来源（webui_status(app) 读的是
			// runtime_manager.current，而不是本租约）。
			WebUI: a.webUIStatus(),
		}))
	})
}

// handleModels 对应 app.py:183-210 的 GET /v1/models。
//
// 需要鉴权，且**按凭据的授权范围收窄**：
//   - 本地主凭据：全部可用模型 + unified-model；
//   - 访问密钥：只列它 providers 清单能覆盖、且模型名在它 models 清单内的模型；
//   - 工作空间推理凭据：按该空间的 models 白名单，或退化为任务名。
//
// 这份清单必须与 proxy 的判定一致——列了却调不动、或调得动却不在清单里，都会让
// 接入方以为自己配错了。
func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	a.withLease(w, func(resources *runtime.RuntimeResources) {
		apiKey := auth.RequestAPIKey(r.Header)
		context := auth.Authenticate(a.options.Authorizer, r, resources.Config.LocalAPIKey)
		var accessKey *config.AccessKeyConfig
		scoped := ""
		if context == nil {
			// 完整权限没通过，再看是不是某把访问密钥或某个空间的推理 key。判定顺序与
			// proxy.Handler.authorize 一致。
			if key := resources.Config.AccessKeyFor(apiKey); key != nil {
				if !key.Enabled {
					writeErrorEnvelope(w, http.StatusForbidden, "访问密钥已被停用: "+key.Name)
					return
				}
				accessKey = key
			} else if scoped = resources.Config.WorkspaceForInferenceKey(apiKey); scoped == "" {
				writeErrorEnvelope(w, http.StatusUnauthorized, authFailureMessage)
				return
			}
		}
		pool := keyPoolOf(resources)
		if pool == nil {
			// 装配错误：宁可失败关闭，也不要带着 nil 继续（会 panic）。
			writeInternalError(w)
			return
		}
		var names []string
		switch {
		case accessKey != nil:
			names = accessKeyModelNames(pool, accessKey)
		case scoped != "":
			names = scopedModelNames(resources.Config, scoped)
		default:
			names = pool.AvailableModelIDs(nil)
		}
		items := make([]*canonical.Value, 0, len(names))
		for _, modelID := range names {
			items = append(items, canonical.NewObjectOf(
				canonical.ObjectPair{Key: "id", Value: canonical.NewString(modelID)},
				canonical.ObjectPair{Key: "object", Value: canonical.NewString("model")},
				canonical.ObjectPair{Key: "owned_by", Value: canonical.NewString("auto-model-key-router")},
			))
		}
		writeJSON(w, http.StatusOK, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "object", Value: canonical.NewString("list")},
			canonical.ObjectPair{Key: "data", Value: canonical.NewArray(items...)},
		))
	})
}

// accessKeyModelNames 列出某把访问密钥可用且**在它清单内**的模型名（升序）。
//
// 两步过滤缺一不可，否则清单会与实际能力脱节：
//   - pool.AvailableModelIDs 按 providers 清单收窄，排除「有配置但一个上游都用不了」
//     的模型；
//   - AllowsModel 再按 models 清单收窄到这把 key 被明确授权的名字。
//
// unified-model 由 AvailableModelIDs 追加在末尾，这里必须**剔掉**：访问密钥用不了
// 它（见 proxy 的同名判断），列出来就是给了个必然 403 的名字。
func accessKeyModelNames(pool *keypool.KeyPool, accessKey *config.AccessKeyConfig) []string {
	available := pool.AvailableModelIDs(accessKey)
	names := make([]string, 0, len(available))
	for _, name := range available {
		if name == config.UNIFIED_MODEL_ID {
			continue
		}
		if !accessKey.AllowsModel(name) {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// scopedModelNames 列出某个工作空间的作用域凭据**可直呼**的名字（升序）。
//
// 配置了 models 白名单时就是它（按名字升序，与 AvailableModelIDs 的排序口径一致）；
// 没配置时退化成该空间的**任务名**——任务名是该空间唯一天然隔离的东西，也是作用域
// 凭据本来最该用的调用方式。
//
// unified-model 刻意不进这份清单：它是全局计划，作用域凭据用不了（见 proxy 的同名
// 判断）。任务名里即便有人起了这个名也不会出现——任务名与保留模型名同名会在配置层
// 就被判非法。
func scopedModelNames(cfg *config.RouterConfig, workspace string) []string {
	if allowed, restricted := cfg.WorkspaceAllowedModels(workspace); restricted {
		names := make([]string, 0, len(allowed))
		for name := range allowed {
			names = append(names, name)
		}
		slices.Sort(names)
		return names
	}
	names := make([]string, 0)
	for i := range cfg.Tasks {
		if config.NormalizeWorkspace(cfg.Tasks[i].Workspace) == workspace {
			names = append(names, cfg.Tasks[i].Name)
		}
	}
	slices.Sort(names)
	return names
}

// handleMetrics 对应 app.py:212-233 的 GET /metrics。
//
// 参数校验必须排在鉴权**之前**：FastAPI 在调用路由函数前完成参数解析，所以
// `GET /metrics?hours=0`（不带凭据）是 422 而不是 401。装配层必须保持这个顺序
// （同样的要求在 workspace_usage.go 那几条新增读数上也有对应用例）。
func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	result, errs := validateQuery(r.URL.Query(), metricsSnapshotParams)
	if len(errs) > 0 {
		writeQueryErrors(w, errs)
		return
	}
	a.withFullAuth(w, r, func(resources *runtime.RuntimeResources) {
		store := metricsStoreOf(resources)
		if store == nil {
			writeInternalError(w)
			return
		}
		body, err := store.Snapshot(result.hoursOrNil("hours", "all_history"), nil)
		if err != nil {
			// 参照实现没有 try/except，异常由 Starlette 兜底成 500。
			writeInternalError(w)
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
}

// handleMetricsRequests 对应 app.py:235-277 的 GET /metrics/requests。
func (a *App) handleMetricsRequests(w http.ResponseWriter, r *http.Request) {
	result, errs := validateQuery(r.URL.Query(), requestHistoryParams)
	if len(errs) > 0 {
		writeQueryErrors(w, errs)
		return
	}
	a.withFullAuth(w, r, func(resources *runtime.RuntimeResources) {
		store := metricsStoreOf(resources)
		if store == nil {
			writeInternalError(w)
			return
		}
		body, err := store.RequestHistory(metrics.RequestHistoryParams{
			Hours:            result.hoursOrNil("hours", "all_history"),
			CallerType:       result.optionalString("caller_type"),
			ModelID:          result.optionalString("model_id"),
			RequestedModelID: result.optionalString("requested_model_id"),
			ProviderID:       result.optionalString("provider_id"),
			PoolName:         result.optionalString("pool_name"),
			UpstreamModelID:  result.optionalString("upstream_model_id"),
			KeyName:          result.optionalString("key_name"),
			StatusCode:       result.optionalInt("status_code"),
			Success:          result.optionalBool("success"),
			Attributed:       result.optionalBool("attributed"),
			Limit:            result.intValue("limit"),
			BeforeID:         result.optionalInt("before_id"),
		})
		if err != nil {
			// FastAPI 已经挡住了 hours/limit/before_id 的非法取值，因此这里只可能是
			// 数据库错误；参照实现同样会冒到 Starlette 变成 500。
			writeInternalError(w)
			return
		}
		// 明细要显示「这次请求是谁发的」，而指标库只存访问密钥的 key_id；显示名是配置
		// 的属性，在这里补（与 /ui/key-usage.json 同一套做法，见 key_usage.go）。
		attachItemAccessKeyNames(body.Lookup("items"), resources.Config)
		writeJSON(w, http.StatusOK, body)
	})
}

// handleMetricsSeries 对应 app.py:279-323 的 GET /metrics/series。
//
// 这条路由**唯一**把存储在内部的 ValueError 翻译成 422
// （`{"error":{"message":"time series cannot exceed 500 points"}}`，app.py:318-321），
// 其余 metrics 路由的存储错误一律冒到 500。
func (a *App) handleMetricsSeries(w http.ResponseWriter, r *http.Request) {
	result, errs := validateQuery(r.URL.Query(), metricsSeriesParams)
	if len(errs) > 0 {
		writeQueryErrors(w, errs)
		return
	}
	a.withFullAuth(w, r, func(resources *runtime.RuntimeResources) {
		store := metricsStoreOf(resources)
		if store == nil {
			writeInternalError(w)
			return
		}
		body, err := store.TimeSeries(metrics.TimeSeriesParams{
			Hours:            result.floatValue("hours"),
			BucketSeconds:    result.intValue("bucket_seconds"),
			CallerType:       result.optionalString("caller_type"),
			ModelID:          result.optionalString("model_id"),
			RequestedModelID: result.optionalString("requested_model_id"),
			ProviderID:       result.optionalString("provider_id"),
			PoolName:         result.optionalString("pool_name"),
			UpstreamModelID:  result.optionalString("upstream_model_id"),
			KeyName:          result.optionalString("key_name"),
			StatusCode:       result.optionalInt("status_code"),
			Success:          result.optionalBool("success"),
			Attributed:       result.optionalBool("attributed"),
		})
		if err != nil {
			var validation *metrics.ValidationError
			if errors.As(err, &validation) {
				writeErrorEnvelope(w, http.StatusUnprocessableEntity, validation.Message)
				return
			}
			writeInternalError(w)
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
}

// withLease 是每条 app 面路由的公共前缀：热重载 + 取得一代运行时资源。
//
// 对应 app.py 里成对出现的 `await _reload_config_if_changed(state)` 与
// `lease = await _acquire_runtime(state)`（app.py:478-480）。
//
// 取得租约是必须的：热重载只能把旧一代标记为 retired，租约归零后才真正关闭它的
// 连接池与指标库（runtime/manager.go）。少了租约，正在读的 sqlite 连接会被关掉。
//
// 唯一的错误路径是「管理器已关闭」（关停中）；参照实现在那种情况下由 uvicorn 停止
// 接受连接，没有对应状态码，这里用 503。
func (a *App) withLease(w http.ResponseWriter, next func(*runtime.RuntimeResources)) {
	a.reload()
	lease, err := a.manager.Acquire()
	if err != nil {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, "服务正在关停，不接受新请求")
		return
	}
	defer lease.Release()
	next(lease.Resources)
}

// withFullAuth 是三条 metrics 路由的公共前缀：租约 + 「必须是完整权限」。
//
// 与 handleModels 的区别在判定条件：/v1/models 对访问密钥开放（按它自己的清单收窄），
// metrics 只允许本地 key。
func (a *App) withFullAuth(w http.ResponseWriter, r *http.Request, next func(*runtime.RuntimeResources)) {
	a.withLease(w, func(resources *runtime.RuntimeResources) {
		context := auth.Authenticate(a.options.Authorizer, r, resources.Config.LocalAPIKey)
		if context == nil || !context.IsFull() {
			writeErrorEnvelope(w, http.StatusUnauthorized, authFailureMessage)
			return
		}
		next(resources)
	})
}

// keyPoolOf 取回本包需要的 key pool。
//
// runtime.RuntimeResources.KeyPool 的类型是 runtime.KeyHealth（只有健康回写三个
// 方法），而 app 面需要 available_model_ids / public_model_ids 等读数，因此做一次
// 类型断言——internal/proxy 的 keyPoolOf（proxy/context.go）是同样的写法。
func keyPoolOf(resources *runtime.RuntimeResources) *keypool.KeyPool {
	if resources == nil || resources.KeyPool == nil {
		return nil
	}
	pool, _ := resources.KeyPool.(*keypool.KeyPool)
	return pool
}

// metricsStoreOf 取回某一代运行时的指标库。
//
// 从 Metrics 接缝反查适配器而不是直接读配置重建：热重载会换库，而 snapshot 必须读
// 当前租约那一代的库（app.py:229 的 `lease.resources.metrics`）。
func metricsStoreOf(resources *runtime.RuntimeResources) *metrics.Store {
	if resources == nil || resources.Metrics == nil {
		return nil
	}
	adapter, _ := resources.Metrics.(*metricsAdapter)
	if adapter == nil {
		return nil
	}
	return adapter.store
}

// —— 响应写出 ——
//
// 这里三个 helper 与 internal/api 的同名函数逐字同形，但那边没有导出。刻意重复而
// 不是把 api 的内部实现改成导出的：api 的对外形状已经发布，改动它的编写面属于无谓
// 风险；这三个函数一共 20 行。

// writeJSON 写出 canonical 序列化的 JSON 响应。
//
// content-type 固定 "application/json"（Starlette 的 JSONResponse 不带 charset），
// content-length 显式写出（Starlette 也会带），响应体不带末尾换行。
func writeJSON(w http.ResponseWriter, status int, body *canonical.Value) {
	payload := canonical.DumpsOrdered(body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, payload)
}

// writeErrorEnvelope 写出 `{"error":{"message":...}}`（proxy 与 app 面共用的错误信封）。
func writeErrorEnvelope(w http.ResponseWriter, status int, message string) {
	inner := canonical.NewObject()
	inner.SetKey("message", canonical.NewString(message))
	outer := canonical.NewObject()
	outer.SetKey("error", inner)
	writeJSON(w, status, outer)
}

// writeNotFound 写出 Starlette 的默认 404。
func writeNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, canonical.NewObjectOf(
		canonical.ObjectPair{Key: "detail", Value: canonical.NewString("Not Found")},
	))
}

// writeMethodNotAllowed 写出 FastAPI 的 405（含决定性的 Allow 头）。
func writeMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeJSON(w, http.StatusMethodNotAllowed, canonical.NewObjectOf(
		canonical.ObjectPair{Key: "detail", Value: canonical.NewString("Method Not Allowed")},
	))
}

// writeQueryErrors 写出查询参数校验失败（422 + `{"detail":[...]}`）。
func writeQueryErrors(w http.ResponseWriter, errs []queryError) {
	items := make([]*canonical.Value, 0, len(errs))
	for _, queryErr := range errs {
		items = append(items, queryErr.toValue())
	}
	writeJSON(w, http.StatusUnprocessableEntity, canonical.NewObjectOf(
		canonical.ObjectPair{Key: "detail", Value: canonical.NewArray(items...)},
	))
}

// writeInternalError 复刻 Starlette 兜底 500。
//
// 参照实现没有给 ValueError / 其它异常注册 handler，它们逃出路由函数后由
// ServerErrorMiddleware 处理：状态 500、content-type text/plain; charset=utf-8、
// 体是固定的 "Internal Server Error"。
func writeInternalError(w http.ResponseWriter) {
	const body = "Internal Server Error"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, body)
}

// 编译期断言：internal/health 的 KeyPool 接口必须被 *keypool.KeyPool 满足
// （/health 不启动服务就能构造响应体，靠的就是这个接缝）。
var _ health.KeyPool = (*keypool.KeyPool)(nil)
