package server

import (
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// metricDimensionsPath 是 WebUI 筛选器选项读数在 WebUI 挂载下的文件名。
//
// **为什么挂在 /ui/**：与 filtered_metrics.go、key_usage.go 同一条理由——/metrics 系列是
// 对照参照实现的读数，形状已发布；筛选器是本项目自有的界面能力，它的选项读数也只能是
// 本项目自有的路由。
//
// 与 /ui/filtered-metrics.json 的分工：那条按条件**取数**，这条告诉界面**有哪些条件可选**
// （窗口内指标库里实际出现过的取值）。选项刻意**不随当前筛选收窄**，否则选了一个模型
// 以后别的模型就从列表里消失了（见 metrics/metricdimensions.go）。
const metricDimensionsPath = "/metric-dimensions.json"

// handleMetricDimensions 返回窗口内各维度实际出现过的取值与请求数。
//
// 鉴权要求**完整权限**：选项本身就是全实例的配置投影（有哪些供应商、哪些上游 Key、哪些
// 访问密钥在用），与 /metrics、filtered-metrics 同级；访问密钥与工作空间凭据一律 401。
//
// 只支持 GET：与其它 /ui 读数一致，其余方法回 405 + Allow 头。
func (a *App) handleMetricDimensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	// 参数校验排在鉴权**之前**（与 /metrics、key-usage 同序）：`hours=0` 不带凭据是 422
	// 而不是 401。
	result, errs := validateQuery(r.URL.Query(), metricDimensionsParams)
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
		body, err := store.MetricDimensions(metrics.MetricDimensionsParams{
			Hours: result.hoursOrNil("hours", "all_history"),
		})
		if err != nil {
			// 参数已由 validateQuery 挡住，这里只可能是数据库错误；与 /metrics 一致冒到 500。
			writeInternalError(w)
			return
		}
		attachOptionAccessKeyNames(body, resources.Config)
		writeJSON(w, http.StatusOK, body)
	})
}

// attachOptionAccessKeyNames 给筛选器选项里的访问密钥补上配置里的显示名。
//
// 与 attachAccessKeyNames 同一条规则：指标层只存 key_id，显示名是配置的属性，只能在装配
// 层补；配置里已经没有（或没写名字）的 key 回 null 而不是编一个名字——界面据此把它渲染
// 成「已删除的 key」，那正是需要看出来的事。name 键由指标层预先占好位置，这里原位覆盖，
// 键序（id, name, requests）不变。
func attachOptionAccessKeyNames(body *canonical.Value, cfg *config.RouterConfig) {
	entries := body.Lookup("access_keys")
	if entries == nil || entries.Kind != canonical.KindArray {
		return
	}
	names := accessKeyNames(cfg)
	for _, entry := range entries.Arr {
		id := entry.Lookup("id")
		if id == nil || id.Kind != canonical.KindString {
			continue
		}
		entry.SetKey("name", accessKeyName(names, id.Str))
	}
}
