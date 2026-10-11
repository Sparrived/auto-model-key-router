package server

import (
	"context"

	"github.com/Sparrived/auto-model-key-router/internal/agentconfig"
	"github.com/Sparrived/auto-model-key-router/internal/api"
	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configeditor"
	"github.com/Sparrived/auto-model-key-router/internal/service"
)

// 本文件把各模块提供的实现接到 internal/api 预留的接缝上。
//
// 这些接缝是**有意**留成 nil 的：运行时未接线时相关路由一律**响亮失败**（500 + 点名接缝），
// 而不是静默返回空列表或假装成功。因此"接线"这件事本身必须有人显式做，且集中在一处——
// 分散在各模块里就容易出现"以为接上了其实没接"的黑洞（本会话确实踩过一次：
// api.Server.OpsEnabled 没被传入，导致 7 条运维路由与 4 个前端端点静默 404）。
//
// 接缝与实现的对应关系：
//
//	api.Server.RunServiceAction            <- service.RunServiceAction        （POST /api/service/{action}）
//	api.Server.ProbeKeyCapability          <- configeditor.Prober             （POST /api/providers/{id}/keys/{name}/probe）
//	api.Server.ProbeProviderKeyCapabilities<- configeditor.Prober             （POST /api/providers/{id}/probe）
//	api.Server.ProbeKeyAvailability        <- configeditor.Prober             （POST /api/probes/keys）
//	api.Server.Integrations                <- agentconfig                     （GET/POST /api/integrations*）
//
// 前三者的签名与接缝字段**逐字相同**（configeditor 为此提供了无 ctx 的变体），所以是直接
// 赋值；只有 Integrations 需要一次字段映射，见 opsIntegrations。

// opsIntegrations 把 internal/agentconfig 适配成 api 的集成接缝。
//
// 与参照实现的对应：ops_api.py 的三条集成路由直接调用 agent_config 的
// get_agent_config_status / configure_agent / rollback_agent，并逐字段组装响应。这里做的是
// 同一件事——**只做字段搬运，不做语义加工**。
//
// Options 用零值：BaseDir 为空即"真实用户主目录"，这正是生产要的。**测试必须自己传
// BaseDir 指向临时目录**，否则会改写开发者机器上真实的 ~/.claude、~/.codex、~/.pi。
func opsIntegrations() *api.OpsIntegrations {
	options := agentconfig.Options{}
	return &api.OpsIntegrations{
		SupportedAgents: []string{agentconfig.ClaudeCode, agentconfig.Codex, agentconfig.PiAgent},
		DisplayName:     agentconfig.DisplayName,
		Status: func(agent string) (api.OpsAgentStatus, error) {
			status, err := agentconfig.GetStatus(agent, options)
			if err != nil {
				return api.OpsAgentStatus{}, err
			}
			return api.OpsAgentStatus{
				TargetPath:       status.TargetPath,
				BackupAvailable:  status.BackupAvailable,
				CurrentIsApplied: status.CurrentIsApplied,
			}, nil
		},
		Configure: func(agent string, cfg *config.RouterConfig) (api.OpsAgentConfigResult, error) {
			result, err := agentconfig.Configure(agent, cfg, options)
			if err != nil {
				return api.OpsAgentConfigResult{}, err
			}
			return api.OpsAgentConfigResult{
				TargetPath:       result.TargetPath,
				BackupPath:       result.BackupPath,
				RouterURL:        result.RouterURL,
				ExtraTargetPaths: result.ExtraTargetPaths,
			}, nil
		},
		Rollback: func(agent string) (api.OpsAgentRollbackResult, error) {
			result, err := agentconfig.Rollback(agent, options)
			if err != nil {
				return api.OpsAgentRollbackResult{}, err
			}
			return api.OpsAgentRollbackResult{
				TargetPath: result.TargetPath,
				Restored:   result.Restored,
			}, nil
		},
	}
}

// 编译期断言：接缝字段能直接吃下这些方法值，签名一旦漂移就在这里报错，而不是等到接线处
// 才由运行时发现。api 的接缝字段类型就是下面这些函数类型。
var (
	proberForSeam = configeditor.Prober{}

	// runServiceActionForSeam 让 server.go 不必为此多一个 import。
	runServiceActionForSeam = service.RunServiceAction

	_ func(string, string, *config.RouterConfig) (string, error) = service.RunServiceAction

	_ func(*canonical.Value, string, []string, float64) (*canonical.Value, error)                                 = proberForSeam.ProbeKeyCapability
	_ func(*canonical.Value, []string, float64) (*canonical.Value, error)                                         = proberForSeam.ProbeProviderKeyCapabilities
	_ func(context.Context, *canonical.Value, string, *canonical.Value, float64) ([]api.ProbeAvailability, error) = proberForSeam.ProbeKeyAvailability
)
