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
			// 七个维度都可选：同名参数可以重复出现（维度内 OR、维度间 AND），三份拆分与
			// unattributed 一并收窄；都不给则与加这些参数之前完全一致。
			Dimensions: result.dimensions(),
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
	names := accessKeyNames(cfg)
	for _, entry := range entries.Arr {
		id, ok := entry.Obj.Get("access_key_id")
		if !ok {
			continue
		}
		entry.SetKey("access_key_name", accessKeyName(names, id.Str))
	}
}

// attachItemAccessKeyNames 给一组请求明细（items 数组）逐条补上配置里的显示名。
//
// 与 attachAccessKeyNames 同一件事，只是作用在 /metrics/requests 与 /ui/filtered-metrics.json
// 的 **items** 上：概览页的请求流要显示「这次请求是谁发的」，而指标库只存 key_id。
// 没带归属的行（完整权限、工作空间凭据、历史行）照常补一个 null——两项读数里的条目
// 形状必须一致，读端才不用为「有些行有这个键、有些行没有」再写一次判空。
//
// 收的是 items 数组本身而不是整个响应：两边的 items 藏在不同的位置（一个在顶层，
// 一个在 requests 之下），并且 `limit=0` 时那份是 null——形状不对就直接不补。
func attachItemAccessKeyNames(items *canonical.Value, cfg *config.RouterConfig) {
	if items == nil || items.Kind != canonical.KindArray {
		return
	}
	names := accessKeyNames(cfg)
	for _, item := range items.Arr {
		id, ok := item.Obj.Get("access_key_id")
		if !ok || id == nil || id.Kind != canonical.KindString {
			// 没有归属（null）：同样写成 null，而不是把 key_id 回填成名字。
			item.SetKey("access_key_name", canonical.NewNull())
			continue
		}
		item.SetKey("access_key_name", accessKeyName(names, id.Str))
	}
}

// accessKeyNames 把配置里的访问密钥渲染成 id -> 显示名。
//
// 没有显示名的 key 不进表：调用方据此回落到 key_id 本身，而不是拿到一个空串。
func accessKeyNames(cfg *config.RouterConfig) map[string]string {
	names := map[string]string{}
	if cfg == nil {
		return names
	}
	for _, key := range cfg.AccessKeys {
		if key.Name != "" {
			names[key.ID] = key.Name
		}
	}
	return names
}

// accessKeyName 取一个 id 的显示名；配置里没有（或没写名字）时回 null。
func accessKeyName(names map[string]string, id string) *canonical.Value {
	if name, found := names[id]; found {
		return canonical.NewString(name)
	}
	return canonical.NewNull()
}
