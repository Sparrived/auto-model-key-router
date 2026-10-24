package server

import (
	"errors"
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// usageBreakdownPath 是「按一个维度拆分的趋势」在 WebUI 挂载下的文件名。
//
// **为什么挂在 /ui/**：与 filtered_metrics.go、status_monitor.go 同一条理由——/metrics 系列
// 的形状与参数都已发布，按维度拆分的时间序列是本项目自有的口径。
//
// 与 /ui/filtered-metrics.json 的关系：同一套筛选参数、同一张桶网格（同一组参数下，这里
// 全部分组与 other 的序列逐桶相加等于那边的 series），只是把那条总趋势按 group_by 拆成
// 前 N 组 + other，供堆叠面积图与每行的迷你折线用。两者分开是因为载荷量级不同：总趋势
// 每次刷新都取，拆分只在打开拆分图时取。
const usageBreakdownPath = "/usage-breakdown.json"

// handleUsageBreakdown 返回按 group_by 拆分的趋势：前 limit 组各自的总量与序列，其余并进 other。
//
// 鉴权要求**完整权限**：拆分会暴露全实例的模型、上游 Key 与访问密钥用量，与 /metrics、
// filtered-metrics 同级；访问密钥与工作空间凭据一律 401。
//
// 只支持 GET：与其它 /ui 读数一致，其余方法回 405 + Allow 头。
func (a *App) handleUsageBreakdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	// 参数校验排在鉴权**之前**（与 /metrics、filtered-metrics 同序）。
	result, errs := validateQuery(r.URL.Query(), usageBreakdownParams)
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
		body, err := store.UsageBreakdown(metrics.UsageBreakdownParams{
			Hours:         result.hoursOrNil("hours", "all_history"),
			BucketSeconds: result.intValue("bucket_seconds"),
			GroupBy:       result.stringValue("group_by"),
			RankBy:        result.stringValue("rank_by"),
			Limit:         result.intValue("limit"),
			Dimensions:    result.dimensions(),
		})
		if err != nil {
			// 点数超限之类是**参数**问题（与 /metrics/series、filtered-metrics 同一套文本），
			// 翻译成 422；其余只可能是数据库错误，与 /metrics 一致冒到 500。
			var validation *metrics.ValidationError
			if errors.As(err, &validation) {
				writeErrorEnvelope(w, http.StatusUnprocessableEntity, validation.Message)
				return
			}
			writeInternalError(w)
			return
		}
		attachBreakdownAccessKeyNames(body, resources.Config)
		writeJSON(w, http.StatusOK, body)
	})
}

// attachBreakdownAccessKeyNames 在 group_by=access_key 时给每组的 key 补上显示名。
//
// 与 attachAccessKeyNames 同一条规则：只有配置里还在、且写了名字的 key 才有名字，否则
// null（不编造）；access_key_id 为 null 的那一组（不是访问密钥发起的请求）名字也是 null。
// access_key_name 由指标层预先占位，这里原位覆盖，key 对象的键序不变。
func attachBreakdownAccessKeyNames(body *canonical.Value, cfg *config.RouterConfig) {
	if groupBy := body.Lookup("group_by"); groupBy == nil || groupBy.Str != metrics.UsageBreakdownGroupByAccessKey {
		return
	}
	groups := body.Lookup("groups")
	if groups == nil || groups.Kind != canonical.KindArray {
		return
	}
	names := accessKeyNames(cfg)
	for _, group := range groups.Arr {
		key := group.Lookup("key")
		if key == nil {
			continue
		}
		id := key.Lookup("access_key_id")
		if id == nil || id.Kind != canonical.KindString {
			key.SetKey("access_key_name", canonical.NewNull())
			continue
		}
		key.SetKey("access_key_name", accessKeyName(names, id.Str))
	}
}
