package server

import (
	"errors"
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// statusMonitorPath 是「状态监控」页的数据端点在 WebUI 挂载下的文件名。
//
// **为什么挂在 /ui/ 而不是给 /metrics 加字段**：/metrics 系列是**对照参照实现**的读数，
// 响应形状与查询参数都已发布（连 filters 回显的键序都是发布字节）。渠道健康矩阵是
// 本项目自有的口径（参照实现没有状态看板），因此单独一条 /ui 路由——与
// filtered_metrics.go、key_usage.go、workspace_usage.go 同一条理由。
//
// 与 /ui/filtered-metrics.json 的关系：那份回答「按这组条件筛选后整页读数长什么样」
// （概览 + 趋势 + 最近明细）；这份回答「哪些渠道在哪个时间片不健康」（概览 + 趋势 +
// 按渠道分组的桶矩阵）。两者共用同一套筛选语义与同窗口约定，但问题不同，因此不合并。
const statusMonitorPath = "/status-monitor.json"

// handleStatusMonitor 返回状态监控页的三份同窗口读数：snapshot / series / matrix。
//
// 鉴权要求**完整权限**：矩阵会暴露全实例每个渠道的健康状况（配置结构的投影），与
// /metrics、filtered-metrics 同级；访问密钥与工作空间的面板 key / 推理 key 一律 401。
//
// 只支持 GET：与其它 /ui 读数一致，其余方法回 405 + Allow 头。
func (a *App) handleStatusMonitor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	// 参数校验必须排在鉴权**之前**（与 /metrics、filtered-metrics 同序）：FastAPI 在
	// 调用路由函数前完成参数解析，所以 `hours=0` 不带凭据是 422 而不是 401。
	result, errs := validateQuery(r.URL.Query(), statusMonitorParams)
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
		body, err := store.StatusMonitor(metrics.StatusMonitorParams{
			Hours:         result.floatValue("hours"),
			BucketSeconds: result.intValue("bucket_seconds"),
			GroupBy:       result.stringValue("group_by"),
			// 七个维度都可省：都不给就是全实例读数（与 filtered-metrics 一致）。
			Dimensions: result.dimensions(),
		})
		if err != nil {
			// hours/bucket 之类的质量问题在存储层还有一道防御性校验：翻译成 422，
			// 与 /metrics/series 的点数上限同一套文本；其余只可能是数据库错误，
			// 与 /metrics 一致冒到 500。
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
