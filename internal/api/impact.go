package api

import (
	"net/http"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// 本文件是「先预演、再确认」这条写路径。
//
// 删一个模型会连带改掉别处的引用：某把访问密钥的模型清单少一项，某个工作空间的直呼清单
// 少一项，引用它的任务被删掉，unified_model 被改写。这些连带变动原先只能靠事后翻配置
// 发现——而现在配置层会因此拒绝整份配置，用户干脆连保存都做不成。
//
// 带 `dry_run=1` 的写请求会跑一遍**完全相同**的 mutation，把连带变动算出来返回，但不落盘。
// 客户端据此弹二次确认，用户点了头再发一次不带该参数的请求。

// dryRunRequested 报告本次请求是否只要预演。
//
// 用查询参数而不是请求体字段：需要预演的写接口既有 PUT 又有 DELETE，后者里头
// （DELETE /api/models/{model_id}）的请求体本身还是可选的。塞进请求体就得为它们各造一份
// 「多一个字段」的载荷模型，而那些模型是对外契约（docs/API.md 逐个列字段）。预演与落盘
// 共用同一份请求体，差别只有这一个开关。
func dryRunRequested(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("dry_run")))
	return value == "1" || value == "true"
}

// dryRunImpact 在一份**不落盘**的副本上施加 mutation，返回它连带摘掉的引用。
//
// 与真写共用同一段 mutation 是关键：预演与落盘走同一条逻辑，确认框里列出的影响才等于
// 最终结果——重写一遍「哪些引用会失效」必然与写路径漂移。
//
// 预演不取写锁、也不比对 config_revision：它不改任何东西，用不上防丢改动。版本号在真写
// 那一步照常比对，并发写只会让预演结果过期（确认框里多写一条或少写一条），不会让配置受损。
// 响应里仍带上**当前**版本号：中间没有任何写入，所以它就是真写该拿的那个。
func (s *Server) dryRunImpact(r *http.Request, mutation func(data *canonical.Value) error) (*canonical.Value, error) {
	if _, err := s.authorizedConfig(r); err != nil {
		return nil, err
	}
	// 与写路径一样从盘上取当前配置：预演必须基于「真写会读到的那份数据」。
	data, err := s.managementConfigData()
	if err != nil {
		return nil, err
	}
	after := data.Clone()
	if err := mutation(after); err != nil {
		return nil, translateUpdateError(err)
	}
	return withRevision(data, modelImpactValue(configops.ModelEditImpactOf(data, after)))
}

// modelImpactValue 把预演结果渲染成响应体。
//
// 空清单也要给出（`[]` / false），而不是省略字段：客户端只按这一个形状读，少一个键
// 就得在每一处补兜底。
func modelImpactValue(impact configops.ModelEditImpact) *canonical.Value {
	accessKeys := canonical.NewArray()
	for _, change := range impact.AccessKeys {
		accessKeys.Arr = append(accessKeys.Arr, referenceListChangeValue(change))
	}
	workspaces := canonical.NewArray()
	for _, change := range impact.Workspaces {
		workspaces.Arr = append(workspaces.Arr, referenceListChangeValue(change))
	}
	return objectOf(
		canonical.ObjectPair{Key: "dry_run", Value: canonical.NewBool(true)},
		canonical.ObjectPair{Key: "removed_models", Value: stringArray(impact.RemovedModels)},
		canonical.ObjectPair{Key: "access_keys", Value: accessKeys},
		canonical.ObjectPair{Key: "workspaces", Value: workspaces},
		canonical.ObjectPair{Key: "removed_tasks", Value: stringArray(impact.RemovedTasks)},
		canonical.ObjectPair{Key: "unified_model", Value: canonical.NewBool(impact.UnifiedModel)},
	)
}

// referenceListChangeValue 渲染一份被摘掉条目的清单。
func referenceListChangeValue(change configops.ReferenceListChange) *canonical.Value {
	return objectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString(change.ID)},
		canonical.ObjectPair{Key: "name", Value: canonical.NewString(change.Name)},
		canonical.ObjectPair{Key: "models", Value: stringArray(change.Models)},
		canonical.ObjectPair{Key: "providers", Value: stringArray(change.Providers)},
		canonical.ObjectPair{Key: "models_cleared", Value: canonical.NewBool(change.ModelsCleared)},
		canonical.ObjectPair{Key: "providers_cleared", Value: canonical.NewBool(change.ProvidersCleared)},
	)
}
