package server

import (
	"errors"
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// filteredMetricsPath 是「按一组维度（供应商、访问密钥、模型、上游 Key、工作空间、调用方
// 档位、成败）筛选的整页读数」在 WebUI 挂载下的文件名。
//
// **为什么挂在 /ui/ 而不是给 /metrics 加参数**：/metrics、/metrics/series、
// /metrics/requests 是**对照参照实现**的读数，形状与查询参数都已发布（连 filters 回显的
// 键序都是发布字节），加一个参数就改了契约。因此过滤能力只对本项目自有的读数开放，单独
// 一条 /ui 路由——与 key_usage.go、workspace_usage.go、accesskey_usage.go 同一条理由。
//
// 与 /ui/access-key-usage.json 的区别在**谁能看**：那条是访客看板，只认访问密钥凭据、
// 只回它自己那把；这条是管理面读数，要求完整权限，筛选值由参数指定。管理面本来就能从
// /metrics 看到全实例的量，这里只是把「其中符合条件的那些」单独取出来。
const filteredMetricsPath = "/filtered-metrics.json"

// handleFilteredMetrics 返回这组筛选条件下的概览、趋势与（可选的）最近明细。
//
// 鉴权要求**完整权限**：与 /metrics、key-usage 同级，访问密钥与工作空间的面板 key /
// 推理 key 一律 401。要「看自己那一把」的读数请走 /ui/access-key-usage.json。
//
// 只支持 GET：与其它 /ui 读数一致，其余方法回 405 + Allow 头。
func (a *App) handleFilteredMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	// 参数校验必须排在鉴权**之前**（与 /metrics、key-usage 同序）：FastAPI 在调用路由
	// 函数前完成参数解析，所以 `hours=0` 不带凭据是 422 而不是 401。
	result, errs := validateQuery(r.URL.Query(), filteredMetricsParams)
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
		body, err := store.FilteredMetrics(metrics.FilteredMetricsParams{
			Hours:         result.hoursOrNil("hours", "all_history"),
			BucketSeconds: result.intValue("bucket_seconds"),
			Limit:         result.intValue("limit"),
			// 七个维度都可省：都不给就是全实例读数（见 metrics/filteredmetrics.go）。
			Dimensions: result.dimensions(),
		})
		if err != nil {
			// 点数超限之类是**参数**问题（与 /metrics/series 同一套文本），翻译成 422；
			// 其余只可能是数据库错误，与 /metrics 一致冒到 500。
			var validation *metrics.ValidationError
			if errors.As(err, &validation) {
				writeErrorEnvelope(w, http.StatusUnprocessableEntity, validation.Message)
				return
			}
			writeInternalError(w)
			return
		}
		// 只给**明细**补访问密钥显示名（key_usage.go 的 attachItemAccessKeyNames）：
		// items 是直接画到概览页请求流上的行，「这次请求是谁发的」必须能读出来，而指标库
		// 只存 key_id。筛选值本身仍只回 id——`filters` 回显的是调用方传进来的条件，与
		// 显示名无关，页面上的筛选器本来就渲染着名字。
		attachItemAccessKeyNames(body.Lookup("requests").Lookup("items"), resources.Config)
		writeJSON(w, http.StatusOK, body)
	})
}
