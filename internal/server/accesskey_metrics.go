package server

import (
	"errors"
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// accessKeyMetricsPath 是「一把访问密钥的整页读数」在 WebUI 挂载下的文件名。
//
// **为什么挂在 /ui/ 而不是给 /metrics 加 access_key_id 参数**：/metrics、
// /metrics/series、/metrics/requests 是**对照参照实现**的读数，形状与查询参数都已
// 发布（连 filters 回显的键序都是发布字节），加一个参数就改了契约。因此过滤能力只对
// 本项目自有的读数开放，单独一条 /ui 路由——与 key_usage.go、workspace_usage.go、
// accesskey_usage.go 同一条理由。
//
// 与 /ui/access-key-usage.json 的区别在**谁能看**：那条是访客看板，只认访问密钥凭据、
// 只回它自己那把；这条是管理面读数，要求完整权限，key_id 由参数指定。管理面本来就能
// 从 /metrics 看到全实例的量，这里只是把「其中一把 key 的那部分」单独取出来。
const accessKeyMetricsPath = "/access-key-metrics.json"

// handleAccessKeyMetrics 返回一把访问密钥的概览、趋势与（可选的）最近明细。
//
// 鉴权要求**完整权限**：与 /metrics、key-usage 同级，访问密钥与工作空间的面板 key /
// 推理 key 一律 401。要「看自己那一把」的读数请走 /ui/access-key-usage.json。
//
// 只支持 GET：与其它 /ui 读数一致，其余方法回 405 + Allow 头。
func (a *App) handleAccessKeyMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	// 参数校验必须排在鉴权**之前**（与 /metrics、key-usage 同序）：FastAPI 在调用路由
	// 函数前完成参数解析，所以 `hours=0` 不带凭据是 422 而不是 401。
	result, errs := validateQuery(r.URL.Query(), accessKeyMetricsParams)
	if len(errs) > 0 {
		writeQueryErrors(w, errs)
		return
	}
	// access_key_id 在签名里标了 required，validateQuery 会先报 missing；这里再挡一次
	// 只是让「改了签名却忘了改处理」不会变成 nil 解引用。
	accessKeyID := result.optionalString("access_key_id")
	if accessKeyID == nil {
		writeQueryErrors(w, []queryError{{
			name: "access_key_id", typ: "missing", msg: "Field required", inputNull: true,
		}})
		return
	}
	a.withFullAuth(w, r, func(resources *runtime.RuntimeResources) {
		store := metricsStoreOf(resources)
		if store == nil {
			writeInternalError(w)
			return
		}
		body, err := store.AccessKeyMetrics(metrics.AccessKeyMetricsParams{
			Hours:         result.hoursOrNil("hours", "all_history"),
			BucketSeconds: result.intValue("bucket_seconds"),
			Limit:         result.intValue("limit"),
			AccessKeyID:   *accessKeyID,
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
		// 刻意**不**补 access_key_name：显示名是配置信息（指标库只存 key_id），
		// 这条响应只回指标侧的事实，界面自己去 /api/access-keys 拿名字——
		// 与 key_usage.go 那边「服务端补名字」相反，因为那边是给人看的榜单，
		// 这里是一个筛选条件的读数，页面上本来就同时渲染着筛选器。
		writeJSON(w, http.StatusOK, body)
	})
}
