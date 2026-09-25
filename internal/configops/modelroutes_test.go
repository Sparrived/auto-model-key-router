package configops

import "testing"

// 本文件固化「路由下没有 target 就不该存在」这条写路径不变式。
//
// 不变式的意义：一条没有目标的模型路由既不可路由（不会出现在 /v1/models 的可用清单
// 里）、又会永远挂在管理面的路由列表上。取消 Key 勾选、解绑 Key、删 Key 三条路径原本
// 就会顺手删掉它，但「按下标删除目标」（TUI 的解绑）与「整体替换 targets」漏着——
// 同一个不变式由每个调用方各自维持，迟早漂移出一条谁都调不动、也删不干净的空路由。

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
