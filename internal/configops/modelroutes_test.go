package configops

import "testing"

// 本文件固化「路由下没有 target 就不该存在」这条写路径不变式。
//
// 不变式的意义：一条没有目标的模型路由既不可路由（不会出现在 /v1/models 的可用清单
// 里）、又会永远挂在管理面的路由列表上。取消 Key 勾选、解绑 Key、删 Key 三条路径原本
// 就会顺手删掉它，但「按下标删除目标」（TUI 的解绑）与「整体替换 targets」漏着——
// 同一个不变式由每个调用方各自维持，迟早漂移出一条谁都调不动、也删不干净的空路由。
//
// 路由消失还有第二个后果：它绑定的供应商 Key 可能也就没人用了。哪些 Key 该留、哪些该清
// 见下面的 TestDeleteModelPrunesOnlyOrphanProviderKeys。

// targetlessFixture 是一条「alpha 有一个上游名叫 vendor-flash 的目标」的配置。
//
// 上游名刻意不等于路由 ID：这正是上游名与可调用名解耦之后要支持的用法。
const targetlessFixture = `{
	"models": {
		"alpha": {
			"aliases": [],
			"routing_mode": "round_robin",
			"targets": [{"provider": "p", "key": "k1", "upstream_model": "vendor-flash"}]
		},
		"beta": {
			"aliases": [],
			"routing_mode": "round_robin",
			"targets": [{"provider": "p", "key": "k1", "upstream_model": "beta"}]
		}
	},
	"tasks": {"use-alpha": {"model": "alpha"}, "use-beta": {"model": "beta"}}
}`

// TestDeleteModelTargetRemovesEmptiedRoute 断言删掉最后一条 target 会连路由一起删。
func TestDeleteModelTargetRemovesEmptiedRoute(t *testing.T) {
	data := mustParse(t, targetlessFixture)

	removed, err := DeleteModelTarget(data, "alpha", 0)
	if err != nil {
		t.Fatalf("删除目标失败: %v", err)
	}
	if removed == nil || removed.Lookup("upstream_model").StringValue() != "vendor-flash" {
		t.Fatalf("返回值应是刚被删掉的那条目标，实际 %v", removed)
	}

	models := mustLookup(t, data, "models")
	if models.Obj.Has("alpha") {
		t.Errorf("删掉最后一条目标后路由 alpha 应被整体删除")
	}
	if !models.Obj.Has("beta") {
		t.Errorf("无关的路由不应受影响")
	}
	// 引用被删路由的任务一并修掉（与 DeleteModel 同一条修复路径）。
	tasks := mustLookup(t, data, "tasks")
	if tasks.Obj.Has("use-alpha") {
		t.Errorf("指向已删路由的任务应被清理")
	}
	if !tasks.Obj.Has("use-beta") {
		t.Errorf("指向其它路由的任务不应被清理")
	}
}

// TestDeleteModelPrunesOnlyOrphanProviderKeys 固化删模型/删路由对供应商 Key 的清理口径。
//
// 判据是逐 **Key** 的「还有没有模型引用它」，不是逐路由的「这条路由用过它」：
//
//   - shared 还挂在 beta 的 targets 里 → 必须留下。它是用户手输的上游凭据，顺手删掉
//     另一条路由还在用的凭据，代价远大于配置里多一条没人用的引用；
//   - orphan 只被 alpha 用过 → 跟着 alpha 一起清掉，否则每删一条路由就多一把再也调不动、
//     界面上却还看得见的孤儿凭据；
//   - 供应商 p 本身不动，哪怕它一把 Key 都不剩（空 keys 是合法配置）。
func TestDeleteModelPrunesOnlyOrphanProviderKeys(t *testing.T) {
	data := mustParse(t, `{
		"providers": {"p": {"base_url": "https://p.example", "keys": {
			"shared": {"api_key": "sk-shared"},
			"orphan": {"api_key": "sk-orphan"}
		}}},
		"models": {
			"alpha": {
				"aliases": [],
				"routing_mode": "round_robin",
				"targets": [
					{"provider": "p", "key": "shared", "upstream_model": "vendor-flash"},
					{"provider": "p", "key": "orphan", "upstream_model": "vendor-flash"}
				]
			},
			"beta": {
				"aliases": [],
				"routing_mode": "round_robin",
				"targets": [{"provider": "p", "key": "shared", "upstream_model": "beta"}]
			}
		}
	}`)
	before := data.Clone()

	if err := DeleteModel(data, "alpha"); err != nil {
		t.Fatalf("删除路由失败: %v", err)
	}

	providers := mustLookup(t, data, "providers")
	keys := mustLookup(t, mustLookup(t, providers, "p"), "keys")
	if !keys.Obj.Has("shared") {
		t.Errorf("仍被路由 beta 绑定的 Key 不应被删除")
	}
	if keys.Obj.Has("orphan") {
		t.Errorf("失去最后一个模型绑定的 Key 应被一并删除")
	}
	if !providers.Obj.Has("p") {
		t.Errorf("Key 被清理后供应商本身仍应保留")
	}
	if !mustLookup(t, data, "models").Obj.Has("beta") {
		t.Errorf("无关的路由不应受影响")
	}

	// 预演与真写共用同一段清理逻辑，所以对比前后两份配置就该报出这把 Key——
	// 确认框里说不出来的清理，等于悄悄删掉一把凭据。
	impact := ModelEditImpactOf(before, data)
	if len(impact.RemovedKeys) != 1 || impact.RemovedKeys[0].Provider != "p" ||
		impact.RemovedKeys[0].Key != "orphan" {
		t.Errorf("预演应报出被清理的 Key p/orphan，实际 %+v", impact.RemovedKeys)
	}
}

// TestDeleteModelTargetPrunesOrphanKeyOfRemovedRoute 固化「按下标删掉最后一条 target」这条
// 路径的 Key 清理口径。
//
// 它和 WebUI 的删路由是同一件事（路由随最后一条 target 一起消失），但实现上是先清空 targets
// 再删模型——孤儿判定必须显式拿到刚被解绑的那一条。少了这一步，删路由会清 Key、解绑却不会：
// 同一个后果由两条路径各自维护，正是这个仓库反复吃亏的那种漂移。
func TestDeleteModelTargetPrunesOrphanKeyOfRemovedRoute(t *testing.T) {
	data := mustParse(t, `{
		"providers": {"p": {"base_url": "https://p.example", "keys": {
			"shared": {"api_key": "sk-shared"},
			"orphan": {"api_key": "sk-orphan"}
		}}},
		"models": {
			"alpha": {"targets": [{"provider": "p", "key": "orphan", "upstream_model": "alpha"}]},
			"beta": {"targets": [{"provider": "p", "key": "shared", "upstream_model": "beta"}]}
		}
	}`)

	removed, err := DeleteModelTarget(data, "alpha", 0)
	if err != nil {
		t.Fatalf("删除目标失败: %v", err)
	}
	if removed == nil {
		t.Fatalf("应返回刚被删掉的那条目标")
	}
	if mustLookup(t, data, "models").Obj.Has("alpha") {
		t.Errorf("删掉最后一条目标后路由 alpha 应被整体删除")
	}
	keys := mustLookup(t, mustLookup(t, mustLookup(t, data, "providers"), "p"), "keys")
	if keys.Obj.Has("orphan") {
		t.Errorf("随路由一起消失、且不再被任何模型绑定的 Key 应被清理")
	}
	if !keys.Obj.Has("shared") {
		t.Errorf("仍被路由 beta 绑定的 Key 不应被清理")
	}
}

// TestDeleteModelTargetKeepsRouteWithRemainingTargets 断言还有目标时路由必须留着。
func TestDeleteModelTargetKeepsRouteWithRemainingTargets(t *testing.T) {
	data := mustParse(t, `{
		"models": {
			"alpha": {
				"aliases": [],
				"routing_mode": "round_robin",
				"targets": [
					{"provider": "p", "key": "k1", "upstream_model": "vendor-flash"},
					{"provider": "p", "key": "k2", "upstream_model": "alpha"}
				]
			}
		}
	}`)

	if _, err := DeleteModelTarget(data, "alpha", 0); err != nil {
		t.Fatalf("删除目标失败: %v", err)
	}
	model := mustLookup(t, mustLookup(t, data, "models"), "alpha")
	targets := mustLookup(t, model, "targets")
	if len(targets.Arr) != 1 {
		t.Fatalf("应剩一条目标，实际 %d", len(targets.Arr))
	}
	if got := targets.Arr[0].Lookup("upstream_model").StringValue(); got != "alpha" {
		t.Errorf("剩下的目标应是第二个，实际上游名 %q", got)
	}
}
