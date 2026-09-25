package server

import (
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// keyUsagePath 是 Key 用量读数在 WebUI 挂载下的文件名。
//
// **为什么挂在 /ui/ 而不是塞进 /metrics**：与 workspaces.go、pricing.go、
// workspace_usage.go、accesskey_usage.go 同一条理由——/metrics 是**对照参照实现**的
// 读数，形状已经发布，不能加字段；「按供应商 + Key 名」这个组合分组不是参照实现的
// 口径，因此单独一条本项目自有的路由，落在所有已发布清单之外。
//
// 与 /ui/workspace-usage.json 的分工：那份按**工作空间**拆（嵌入方面板的读数），
// 这份按**Key** 拆（哪把上游 Key 出去了、哪把访问密钥发起的）。两者都读同一个指标
// 库、共用同一套时间窗口语义，但服务的问题不同，所以不合并成一份读数。
const keyUsagePath = "/key-usage.json"

// handleKeyUsage 返回按上游 Key、模型 × 上游 Key 与访问密钥拆分的用量。
//
// 鉴权要求**完整权限**：内容会暴露全实例的 Key 使用情况（配置结构的投影），与
// /metrics 同级，访问密钥与工作空间凭据一律拒绝。要「看自己那一把」的读数各有归属：
// 访问密钥走 /ui/access-key-usage.json，工作空间面板走 /ui/workspace-panel.json。
//
// 只支持 GET：与 workspaces/workspace-usage 一致，其余方法回 405 + Allow 头。
func (a *App) handleKeyUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	// hours 与 /metrics、workspace-usage 同一套取值方式（含 all_history），前端的时间
	// 选择器对三者语义一致。
	result, errs := validateQuery(r.URL.Query(), keyUsageParams)
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
		body, err := store.KeyUsage(metrics.KeyUsageParams{
			Hours: result.hoursOrNil("hours", "all_history"),
		})
		if err != nil {
			// 参数已由 validateQuery 挡住，这里只可能是数据库错误；与 /metrics 一致
			// 冒到 500。
			writeInternalError(w)
			return
		}
		attachAccessKeyNames(body, resources.Config)
		writeJSON(w, http.StatusOK, body)
	})
}

// attachAccessKeyNames 给访问密钥那一组补上配置里的显示名。
//
// 指标层只存 key_id（显示名可改，且不该进指标库），显示名是**配置**的属性，因此只能
// 在装配层补。与 accesskey_usage.go 把 access_key_name 放进响应是同一条理由：让页面
// 不必自己再查一次配置去猜「这个 id 是哪把 key」。
//
// 配置里已经没有这把 key 时**不编造名字**（回 null，界面回落到 id）：把 id 当名字回填
// 会让「这把 key 还配着」看起来像真的，而它其实已经被删掉了——这正是需要看出来的事。
func attachAccessKeyNames(body *canonical.Value, cfg *config.RouterConfig) {
	entries, ok := body.Obj.Get("access_keys")
	if !ok || entries == nil || entries.Kind != canonical.KindArray {
		return
	}
	names := map[string]string{}
	if cfg != nil {
		for _, key := range cfg.AccessKeys {
			if key.Name != "" {
				names[key.ID] = key.Name
			}
		}
	}
	for _, entry := range entries.Arr {
		id, ok := entry.Obj.Get("access_key_id")
		if !ok {
			continue
		}
		if name, found := names[id.Str]; found {
			entry.SetKey("access_key_name", canonical.NewString(name))
			continue
		}
		entry.SetKey("access_key_name", canonical.NewNull())
	}
}
