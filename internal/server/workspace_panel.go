package server

import (
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/auth"
	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// workspacePanelPath 是嵌入方面板的用量读数端点。
//
// 与 /ui/workspace-usage.json 的分工：那个给**完整权限**看全部空间（管理面的工作
// 空间页用它），这个给**面板 key** 看它自己那一个空间。两者形状相同，差别只在数据
// 范围——前端因此可以复用同一套渲染代码。
//
// 为什么不让 /ui/workspace-usage.json 自己按 key 收窄：那个端点不认面板 key（它要求
// 完整权限），把两种权限混进一条路由会让「谁能看到多少」取决于一连串分支，而这里是
// 一条路由一个范围，读代码就能确定边界。
const workspacePanelPath = "/workspace-panel.json"

// handleWorkspacePanel 返回**面板 key 所属那一个**工作空间的用量与流向。
//
// 鉴权只认面板 key，完整权限与访问密钥都被拒：
//
//   - 完整权限走 /ui/workspace-usage.json（那里能看全部空间），没必要也不应该从这条
//     受限路径拿数据——否则「完整权限能看什么」会取决于它恰好也满足面板判定，语义
//     重叠且难以推理；
//   - 访问密钥是推理凭据（能调哪些模型由它自己的清单决定），与工作空间面板无关。
//
// 空间归属**完全由 key 决定**，不接受任何请求参数或请求头指定空间：这正是这个模式
// 存在的意义（见 internal/api 的 authorizedTaskConfig）。
func (a *App) handleWorkspacePanel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	// hours 与 workspace-usage 同一套取值方式，前端的时间选择器对两者语义一致。
	result, errs := validateQuery(r.URL.Query(), workspaceUsageParams)
	if len(errs) > 0 {
		writeQueryErrors(w, errs)
		return
	}
	a.withLease(w, func(resources *runtime.RuntimeResources) {
		workspace := workspaceForPanelKey(resources.Config, r)
		if workspace == "" {
			writeErrorEnvelope(w, http.StatusUnauthorized, authFailureMessage)
			return
		}
		store := metricsStoreOf(resources)
		if store == nil {
			writeInternalError(w)
			return
		}
		body, err := store.WorkspaceUsage(metrics.WorkspaceUsageParams{
			Hours:     result.hoursOrNil("hours", "all_history"),
			Workspace: workspace,
		})
		if err != nil {
			writeInternalError(w)
			return
		}
		// 面板页需要知道自己在哪个空间（标题、空状态文案），而 key 是它唯一的输入。
		// 把空间名放在响应里，前端就不必再猜或另开一个接口。
		body.SetKey("workspace", canonical.NewString(workspace))
		// 建/改任务要选模型，而面板 key 不是调用方凭据（进不了 /v1/models），管理面的
		// GET /api/models 又只认完整权限。因此把**可选择的名字**随读数一起给它：
		// 就是配置里的模型 id 与别名，不多不少——两者正是可调用名的全部，也是
		// /v1/models 会列出的清单。
		body.SetKey("models", workspacePanelModels(resources.Config))
		writeJSON(w, http.StatusOK, body)
	})
}

// workspacePanelModels 列出面板可用于建任务的名字（模型 id 与别名，按配置顺序）。
//
// 恰好就是可调用名的全集（模型 ID + aliases，见 config.ModelConfig）：面板是给人挑
// 模型的界面，清单与 /v1/models 同口径才不会让人挑到一个调不通的名字。
func workspacePanelModels(cfg *config.RouterConfig) *canonical.Value {
	items := canonical.NewArray()
	if cfg == nil {
		return items
	}
	for _, model := range cfg.Models {
		items.Arr = append(items.Arr, canonical.NewString(model.ID))
		for _, alias := range model.Aliases {
			items.Arr = append(items.Arr, canonical.NewString(alias))
		}
	}
	return items
}

// workspaceForPanelKey 解析请求凭据对应的面板工作空间，没有则返回空串。
//
// 与 internal/api 的 authorizedTaskConfig 用**同一个**查表（config.WorkspaceForAPIKey），
// 否则同一个 key 在两处的判定可能不一致。
func workspaceForPanelKey(cfg *config.RouterConfig, r *http.Request) string {
	if cfg == nil {
		return ""
	}
	key := auth.RequestAPIKey(r.Header)
	if key == "" {
		return ""
	}
	return cfg.WorkspaceForAPIKey(key)
}
