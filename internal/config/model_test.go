package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// mustParse 解析一段 canonical JSON 文本，失败即终止测试。
func mustParse(t *testing.T, text string) *canonical.Value {
	t.Helper()
	value, err := canonical.ParseString(text)
	if err != nil {
		t.Fatalf("解析 %q 失败: %v", text, err)
	}
	return value
}

// TestParseDroppedRoutesDocumented 固化两个已知的参照实现行为，防止 Go 侧
// 「顺手修好」而与 Python 产生差异。
//
// 背景（均已实测确认）：
//
//  1. 配置文件顶层的 upstream_routes 被 from_dict 完全忽略（config.py:877 从空
//     字典起步，其后只从 providers 与 model key 汇总）。v1/v2 迁移会写出这个
//     字段，因此那些版本升级上来的路由配置实际不生效。
//  2. KeyConfig 在 config.py:963 构造时未传 upstream_routes，导致
//     key.upstream_routes 恒为 {}，令 config.py:840 的合并循环成为死代码。
//
// Go 侧刻意保持同样行为：迁移期间不得让同一个配置文件在两种实现下产生不同的
// 上游请求路径。修复应当作为独立变更，并在两种实现上同步进行。
func TestParseDroppedRoutesDocumented(t *testing.T) {
	// 情况 1：顶层 upstream_routes 被忽略。
	raw := mustParse(t, `{"config_version":4,"local_api_key":"k",
		"upstream_routes":{"https://api.openai.com":{"anthropic":"top-prefix"}},
		"providers":{"openai":{"base_url":"https://api.openai.com","keys":{"main":{"api_key":"a"}}}},
		"models":{"m":{"targets":[{"provider":"openai","key":"main","upstream_model":"u"}]}}}`)
	cfg, err := FromDict(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if routes := cfg.UpstreamRoutesForBaseURL("https://api.openai.com"); len(routes) != 0 {
		t.Errorf("顶层 upstream_routes 应被忽略，实际得到 %v", routes)
	}

	// 情况 2：provider key 级 upstream_routes 被忽略。
	raw = mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"openai":{"base_url":"https://api.openai.com","keys":{"main":{"api_key":"a","upstream_routes":{"anthropic":"key-prefix"}}}}},
		"models":{"m":{"targets":[{"provider":"openai","key":"main","upstream_model":"u"}]}}}`)
	cfg, err = FromDict(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(cfg.Models) != 1 || len(cfg.Models[0].Keys) != 1 {
		t.Fatalf("解析结果结构异常: %+v", cfg.Models)
	}
	if routes := cfg.Models[0].Keys[0].UpstreamRoutes; len(routes) != 0 {
		t.Errorf("key 级 upstream_routes 应恒为空，实际得到 %v", routes)
	}

	// 对照组：provider 级 routes 确实生效（证明上面的空值不是「都没生效」）。
	raw = mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"openai":{"base_url":"https://api.openai.com","keys":{"main":{"api_key":"a"}},"routes":{"anthropic":"provider-prefix"}}},
		"models":{"m":{"targets":[{"provider":"openai","key":"main","upstream_model":"u"}]}}}`)
	cfg, err = FromDict(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	routes := cfg.UpstreamRoutesForBaseURL("https://api.openai.com")
	if routes["anthropic"] != "provider-prefix/v1/messages" {
		t.Errorf("provider 级 routes 应生效，实际得到 %v", routes)
	}
}

// workspaceConfig 是一份同时带顶层 tasks 与 workspaces 的最小可用配置。
const workspaceConfig = `{"config_version":4,"local_api_key":"k",
	"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
	"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]},"model-b":{"targets":[{"provider":"p","key":"k"}]}},
	"tasks":{"shared":{"model":"model-a"}},
	"workspaces":{"teamA":{"tasks":{"shared":{"model":"model-b"},"only-a":{"model":"model-a"}}},"empty":{}}}`

// TestWorkspacesIsolateTaskNames 固化工作空间的核心语义：任务名只在空间内唯一。
//
// 三件事必须同时成立，缺任何一条「隔离」都是假的：
//  1. 同名任务在两个空间里都能解析，且各自指向自己的模型；
//  2. 顶层 tasks 归属 DefaultWorkspace（既有配置不带 workspaces 也照常工作）；
//  3. 空工作空间被保留——否则新建空工作空间落盘后立刻消失。
func TestWorkspacesIsolateTaskNames(t *testing.T) {
	cfg, err := FromDict(mustParse(t, workspaceConfig))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if len(cfg.Tasks) != 3 {
		t.Fatalf("任务数应为 3，实际 %d: %+v", len(cfg.Tasks), cfg.Tasks)
	}
	defaultTask, found := cfg.TaskForWorkspace(DefaultWorkspace, "shared")
	if !found || defaultTask.Model != "model-a" {
		t.Errorf("默认空间的 shared 应指向 model-a，实际 %+v（found=%v）", defaultTask, found)
	}
	teamTask, found := cfg.TaskForWorkspace("teamA", "shared")
	if !found || teamTask.Model != "model-b" {
		t.Errorf("teamA 的 shared 应指向 model-b，实际 %+v（found=%v）", teamTask, found)
	}
	if _, found := cfg.TaskForWorkspace("teamA", "shared-only-missing"); found {
		t.Error("不存在的工作空间任务不应被找到")
	}
	// 跨空间不可见：only-a 只在 teamA 里。
	if _, found := cfg.TaskForWorkspace(DefaultWorkspace, "only-a"); found {
		t.Error("teamA 的任务不应出现在默认空间")
	}

	// 工作空间清单由任务反推：默认空间在首位，其余按任务出现顺序；`empty` 是个
	// 空分组（里面没有任务），因此**不出现**——没有任务的分组既不可观测也没有意义。
	want := []string{"default", "teamA"}
	got := cfg.WorkspaceNames()
	if len(got) != len(want) {
		t.Fatalf("工作空间清单 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("工作空间清单 %v，期望 %v", got, want)
		}
	}
}

// TestWorkspaceWithAPIKeySurvivesWithoutTasks 固化：带 api_key 的空空间仍然存在。
//
// 这是对「空分组不存在」的一处**有意放宽**：应用侧先建空间拿 key、之后才填任务，
// 中间这段时间空间必须留得住，否则嵌入方手上的 key 会指向一个不存在的空间。
//
// 放宽的边界很窄：同一份配置里的 `empty`（既没任务也没 key）依然不出现。
func TestWorkspaceWithAPIKeySurvivesWithoutTasks(t *testing.T) {
	cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"tasks":{"t":{"model":"model-a"}},
		"workspaces":{"teamA":{"tasks":{"only-a":{"model":"model-a"}}},
			"panel":{"api_key":"amkr_ws_panel"},"empty":{}}}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := []string{"default", "teamA", "panel"}
	got := cfg.WorkspaceNames()
	if len(got) != len(want) {
		t.Fatalf("工作空间清单 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("工作空间清单 %v，期望 %v", got, want)
		}
	}
	if owner := cfg.WorkspaceForAPIKey("amkr_ws_panel"); owner != "panel" {
		t.Errorf("amkr_ws_panel 应属于 panel，实际 %q", owner)
	}
	if owner := cfg.WorkspaceForAPIKey(""); owner != "" {
		t.Errorf("空 key 不应命中任何空间，实际 %q", owner)
	}
	if owner := cfg.WorkspaceForAPIKey("amkr_ws_nope"); owner != "" {
		t.Errorf("未知 key 不应命中任何空间，实际 %q", owner)
	}
}

// TestWorkspaceAPIKeyValidation 固化面板 key 的底线。
//
// 三种情况都必须挡：与本地主 key 相同（等于把全量权限的凭据嵌进第三方页面）、
// 两个空间同 key（判定结果会取决于遍历顺序）、以及撞上访问密钥的凭据
// （两者都走 /v1 面，判定彼此独立，撞车会让权限边界取决于先命中哪张清单）。
func TestWorkspaceAPIKeyValidation(t *testing.T) {
	build := func(workspaces string) string {
		return `{"config_version":4,"local_api_key":"k",
			"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
			"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
			"access_keys":{"trial":{"key":"amkr_ak_x"}},
			"workspaces":` + workspaces + `}`
	}
	// 合法：与本地 key 不同、彼此也不同、也不同于访问密钥。
	if _, err := FromDict(mustParse(t, build(`{"a":{"api_key":"ka"},"b":{"api_key":"kb"}}`))); err != nil {
		t.Errorf("合法配置不应报错: %v", err)
	}
	cases := []struct {
		name       string
		workspaces string
		want       string
	}{
		{"与本地 key 相同", `{"a":{"api_key":"k"}}`, "工作空间 a 的 api_key 不能与 local_api_key 相同"},
		{"两空间同 key", `{"a":{"api_key":"same"},"b":{"api_key":"same"}}`, "工作空间 a 与 b 的 api_key 重复"},
		// 访问密钥并入同一张占用表。工作空间先声明，因此这条在工作空间那一圈通过、
		// 在访问密钥那一圈被拒，报出的是访问密钥侧的措辞（占用者是工作空间）。
		{"撞上访问密钥", `{"a":{"api_key":"amkr_ak_x"}}`, "访问密钥 trial 的 key 与工作空间 a 的 api_key 重复"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := FromDict(mustParse(t, build(testCase.workspaces)))
			if err == nil {
				t.Fatalf("应报错: %s", testCase.want)
			}
			if err.Error() != testCase.want {
				t.Errorf("错误文本 = %q，期望 %q", err.Error(), testCase.want)
			}
		})
	}
}

// TestWorkspaceInferenceKeyValidation 固化推理 key 的底线。
//
// 与面板 key 同一组底线，外加**跨类型**撞车也必须挡：同一个字符串若能同时是某个空间
// 的面板 key 与另一个空间的推理 key，权限边界就取决于走到哪条路由（面板面还是 /v1 面），
// 而这正是不可接受的。
func TestWorkspaceInferenceKeyValidation(t *testing.T) {
	build := func(workspaces string) string {
		return `{"config_version":4,"local_api_key":"k",
			"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
			"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
			"workspaces":` + workspaces + `}`
	}
	// 合法：两把 key 各自不冲突。
	if _, err := FromDict(mustParse(t, build(
		`{"a":{"api_key":"ka","inference_key":"ia"},"b":{"inference_key":"ib"}}`))); err != nil {
		t.Errorf("合法配置不应报错: %v", err)
	}
	cases := []struct {
		name       string
		workspaces string
		want       string
	}{
		{"与本地 key 相同", `{"a":{"inference_key":"k"}}`,
			"工作空间 a 的 inference_key 不能与 local_api_key 相同"},
		{"两空间同推理 key", `{"a":{"inference_key":"same"},"b":{"inference_key":"same"}}`,
			"工作空间 a 与 b 的 inference_key 重复"},
		// 跨类型：同空间的两把 key 也不能是同一个字符串。
		{"同空间两把 key 相同", `{"a":{"api_key":"same","inference_key":"same"}}`,
			"工作空间 a 与 a 的 inference_key 重复"},
		// 跨类型：一个空间的面板 key 撞上另一个空间的推理 key。
		{"面板 key 撞推理 key", `{"a":{"api_key":"same"},"b":{"inference_key":"same"}}`,
			"工作空间 a 与 b 的 inference_key 重复"},
		// 反向：推理 key 先声明，面板 key 后撞上。
		{"推理 key 撞面板 key", `{"a":{"inference_key":"same"},"b":{"api_key":"same"}}`,
			"工作空间 a 与 b 的 api_key 重复"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := FromDict(mustParse(t, build(testCase.workspaces)))
			if err == nil {
				t.Fatalf("应报错: %s", testCase.want)
			}
			if err.Error() != testCase.want {
				t.Errorf("错误文本 = %q，期望 %q", err.Error(), testCase.want)
			}
		})
	}
}

// TestWorkspaceForInferenceKey 固化推理 key 到空间的解析，且**不与面板 key 混淆**。
func TestWorkspaceForInferenceKey(t *testing.T) {
	cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"workspaces":{"a":{"api_key":"ka","inference_key":"ia"},"b":{"inference_key":"ib"}}}`))
	if err != nil {
		t.Fatalf("配置应合法: %v", err)
	}
	if got := cfg.WorkspaceForInferenceKey("ia"); got != "a" {
		t.Errorf("ia 应解析到 a，实得 %q", got)
	}
	if got := cfg.WorkspaceForInferenceKey("ib"); got != "b" {
		t.Errorf("ib 应解析到 b，实得 %q", got)
	}
	// 两把 key 的解析必须互不相通：面板 key 不该被推理侧认出来，反之亦然。
	// 否则 proxy 与面板两条路径会用同一个字符串打开不同的空间。
	if got := cfg.WorkspaceForInferenceKey("ka"); got != "" {
		t.Errorf("面板 key 不应被推理侧命中，实得 %q", got)
	}
	if got := cfg.WorkspaceForAPIKey("ia"); got != "" {
		t.Errorf("推理 key 不应被面板侧命中，实得 %q", got)
	}
	if got := cfg.WorkspaceForInferenceKey(""); got != "" {
		t.Errorf("空 key 不应命中任何空间，实得 %q", got)
	}
	if got := cfg.WorkspaceForInferenceKey("nope"); got != "" {
		t.Errorf("未知 key 不应命中，实得 %q", got)
	}
}

// TestWorkspaceAllowedModels 固化模型清单的三态：省略=不限制，空数组=一个都不许，
// 有内容=只许这些。
//
// 空数组与省略必须可区分：用 len == 0 判断会把运维写下的「一个都不许」当成「不限制」,
// 也就是把一条禁令悄悄失效——而这个字段存在的意义就是限制。
func TestWorkspaceAllowedModels(t *testing.T) {
	cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]},
			"model-b":{"targets":[{"provider":"p","key":"k"}]}},
		"tasks":{"t1":{"model":"model-a"}},
		"workspaces":{"open":{"api_key":"ko"},"none":{"inference_key":"in","models":[]},
			"some":{"inference_key":"is","models":["model-b"]}}}`))
	if err != nil {
		t.Fatalf("配置应合法: %v", err)
	}
	cases := []struct {
		workspace  string
		restricted bool
		allowed    []string
	}{
		{"open", false, nil},
		{"none", true, nil},
		{"some", true, []string{"model-b"}},
		// 没有 models 字段的空间（只靠任务存在）同样是不限制。
		{"t1", false, nil},
	}
	for _, testCase := range cases {
		allowed, restricted := cfg.WorkspaceAllowedModels(testCase.workspace)
		if restricted != testCase.restricted {
			t.Errorf("%s: restricted = %v，期望 %v", testCase.workspace, restricted, testCase.restricted)
		}
		if len(allowed) != len(testCase.allowed) {
			t.Errorf("%s: 清单 = %v，期望 %v", testCase.workspace, allowed, testCase.allowed)
		}
		for _, name := range testCase.allowed {
			if !allowed[name] {
				t.Errorf("%s: 应允许 %s", testCase.workspace, name)
			}
		}
	}
}

// TestWorkspaceModelsParseErrors 固化模型清单的引用校验。
//
// 写错一个名字会让该空间静默少一个可用模型，排查要翻两边配置，因此在解析期直接报错。
func TestWorkspaceModelsParseErrors(t *testing.T) {
	build := func(models string) string {
		return `{"config_version":4,"local_api_key":"k",
			"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
			"models":{"model-a":{"targets":[{"provider":"p","key":"k"}],"aliases":["alias-a"]}},
			"workspaces":{"a":{"api_key":"ka","models":` + models + `}}}`
	}
	// 别名也算合法引用：调用方可以直呼别名，解析后按真实模型算权限。
	if _, err := FromDict(mustParse(t, build(`["alias-a"]`))); err != nil {
		t.Errorf("别名应可作为引用: %v", err)
	}
	cases := []struct{ name, models, want string }{
		{"引用未配置的模型", `["nope"]`, "workspaces.a.models[0] 引用了未配置的模型: nope"},
		{"元素为空串", `[""]`, "workspaces.a.models[0] 不能为空"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := FromDict(mustParse(t, build(testCase.models)))
			if err == nil {
				t.Fatalf("应报错: %s", testCase.want)
			}
			if err.Error() != testCase.want {
				t.Errorf("错误文本 = %q，期望 %q", err.Error(), testCase.want)
			}
		})
	}
}

// TestAccessKeyParseErrors 固化 access_keys 段的解析与引用校验。
//
// 两份清单都在写盘时逐个校验引用的目标确实存在：写错一个名字就让某把已经分发出去的
// key 静默少一项权限，排查要同时翻配置与调用方两侧，因此宁可在这里直接报错，且错误
// 文本要带上 key_id 与字段下标。
func TestAccessKeyParseErrors(t *testing.T) {
	build := func(accessKeys string) string {
		return `{"config_version":4,"local_api_key":"k",
			"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
			"models":{"model-a":{"targets":[{"provider":"p","key":"k"}],"aliases":["alias-a"]}},
			"access_keys":` + accessKeys + `}`
	}
	// 别名也算合法引用：调用方写什么名字就按什么名字授权，清单里可以写别名。
	assertOK := func(t *testing.T, accessKeys string) {
		t.Helper()
		if _, err := FromDict(mustParse(t, build(accessKeys))); err != nil {
			t.Errorf("配置应合法: %v", err)
		}
	}
	t.Run("别名与供应商 ID 均可引用", func(t *testing.T) {
		assertOK(t, `{"trial":{"key":"ak","providers":["p"],"models":["alias-a"]}}`)
	})
	// 三态里「省略」与「空数组」是两种不同的授权状态，解析必须原样保留。
	t.Run("三态被原样保留", func(t *testing.T) {
		cfg, err := FromDict(mustParse(t, build(
			`{"open":{"key":"a1"},"none":{"key":"a2","providers":[],"models":[]},
			  "some":{"key":"a3","providers":["p"],"models":["model-a"]}}`)))
		if err != nil {
			t.Fatalf("配置应合法: %v", err)
		}
		byID := map[string]AccessKeyConfig{}
		for _, key := range cfg.AccessKeys {
			byID[key.ID] = key
		}
		if open := byID["open"]; open.Providers != nil || open.Models != nil {
			t.Errorf("省略字段应是 nil（不限制），实得 %v / %v", open.Providers, open.Models)
		}
		none := byID["none"]
		if none.Providers == nil || none.Models == nil {
			t.Fatalf("显式空数组应是非 nil 空切片（一个都不许），实得 %v / %v",
				none.Providers, none.Models)
		}
		if none.AllowsProvider("p") || none.AllowsModel("model-a") {
			t.Error("空清单应什么都不许")
		}
		some := byID["some"]
		if !some.AllowsProvider("p") || !some.AllowsModel("model-a") {
			t.Error("清单内应放行")
		}
		if some.AllowsProvider("other") || some.AllowsModel("alias-a") {
			t.Error("清单外应拒绝")
		}
	})
	cases := []struct {
		name       string
		accessKeys string
		want       string
	}{
		{"引用未配置的供应商", `{"trial":{"key":"ak","providers":["nope"]}}`,
			"access_keys.trial.providers[0] 引用了未配置的供应商: nope"},
		{"引用未配置的模型", `{"trial":{"key":"ak","models":["nope"]}}`,
			"access_keys.trial.models[0] 引用了未配置的模型: nope"},
		{"供应商元素为空串", `{"trial":{"key":"ak","providers":[""]}}`,
			"access_keys.trial.providers[0] 不能为空"},
		{"模型元素为空串", `{"trial":{"key":"ak","models":[""]}}`,
			"access_keys.trial.models[0] 不能为空"},
		{"缺少 key", `{"trial":{"name":"试用"}}`, "'key'"},
		{"key 为空串", `{"trial":{"key":""}}`, "访问密钥 trial 的 key 不能为空"},
		{"整个 access_keys 不是对象", `[]`, "access_keys 必须是对象"},
		{"条目不是对象", `{"trial":"ak"}`, "访问密钥 trial 必须是对象"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := FromDict(mustParse(t, build(testCase.accessKeys)))
			if err == nil {
				t.Fatalf("应报错: %s", testCase.want)
			}
			if err.Error() != testCase.want {
				t.Errorf("错误文本 = %q，期望 %q", err.Error(), testCase.want)
			}
		})
	}
}

// TestAccessKeyForIsConstantTimeAndSkipsEmpty 固化 AccessKeyFor 的匹配边界。
//
// 空 key 一律不匹配（否则任何不带凭据的请求都会命中一把空 key）；停用的 key
// **仍然匹配**并由调用方判 enabled——把它当「不存在」会让「停用」与「删除」在错误
// 文本上无法区分，而这两件事对调用方的含义完全不同（停用是可恢复的）。
func TestAccessKeyForIsConstantTimeAndSkipsEmpty(t *testing.T) {
	cfg := &RouterConfig{AccessKeys: []AccessKeyConfig{
		{ID: "on", Name: "启用", Key: "ak-on", Enabled: true},
		{ID: "off", Name: "停用", Key: "ak-off", Enabled: false},
	}}
	if got := cfg.AccessKeyFor(""); got != nil {
		t.Errorf("空 key 不应匹配，实得 %v", got)
	}
	if got := cfg.AccessKeyFor("nope"); got != nil {
		t.Errorf("未知 key 不应匹配，实得 %v", got)
	}
	if got := cfg.AccessKeyFor("ak-on"); got == nil || got.ID != "on" {
		t.Errorf("ak-on 应匹配到 on，实得 %v", got)
	}
	if got := cfg.AccessKeyFor("ak-off"); got == nil || got.ID != "off" {
		t.Errorf("停用的 key 应仍可匹配（由调用方判 enabled），实得 %v", got)
	}
	// 前缀不能被当成匹配：比较必须整串相等。
	if got := cfg.AccessKeyFor("ak-o"); got != nil {
		t.Errorf("前缀不应匹配，实得 %v", got)
	}
}

// TestWorkspaceModelsKeepsGroupWithoutCredentials 固化：只带 models（无任何 key）的
// 空间分组不会被解析丢掉。
//
// 这是最常见的配置形态——「一个已经有任务的空间被加上了模型限制」，此时它两把 key
// 都没有。若因为「没有凭据」把它丢了，models 会在热重载后静默消失：限制看起来配了，
// 实际完全没生效。
func TestWorkspaceModelsKeepsGroupWithoutCredentials(t *testing.T) {
	cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"tasks":{"t1":{"model":"model-a","workspace":"teamA"}},
		"workspaces":{"teamA":{"models":["model-a"]}}}`))
	if err != nil {
		t.Fatalf("配置应合法: %v", err)
	}
	allowed, restricted := cfg.WorkspaceAllowedModels("teamA")
	if !restricted || !allowed["model-a"] {
		t.Fatalf("teamA 的模型清单应被保留，实得 restricted=%v allowed=%v", restricted, allowed)
	}
	// models 为空数组也留：那是「一个都不许直呼」这条禁令本身。
	empty, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"workspaces":{"teamA":{"models":[]}}}`))
	if err != nil {
		t.Fatalf("配置应合法: %v", err)
	}
	if _, restricted := empty.WorkspaceAllowedModels("teamA"); !restricted {
		t.Error("空清单也应被视为「已配置」（一个都不许直呼）")
	}
}

// TestWorkspacesOptional 固化兼容性：没有 workspaces 段的既有配置行为不变。
func TestWorkspacesOptional(t *testing.T) {
	cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"tasks":{"t":{"model":"model-a"}}}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if names := cfg.WorkspaceNames(); len(names) != 1 || names[0] != DefaultWorkspace {
		t.Errorf("应只有默认工作空间，实际 %v", names)
	}
	task, found := cfg.TaskFor("t")
	if !found || task.Workspace != DefaultWorkspace {
		t.Errorf("顶层任务应归属默认空间，实际 %+v（found=%v）", task, found)
	}
}

// TestWorkspacesRejectsBadShapes 固化工作空间段的错误文本与触发条件。
func TestWorkspacesRejectsBadShapes(t *testing.T) {
	base := `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},"tasks":{},`
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"非对象", `"workspaces":[]}`, "workspaces 必须是对象"},
		{"空间非对象", `"workspaces":{"teamA":[]}}`, "工作空间 teamA 必须是对象"},
		{"空间名为空", `"workspaces":{"  ":{"tasks":{}}}}`, "工作空间名不能为空"},
		{"与默认空间重名", `"workspaces":{"default":{"tasks":{}}}}`, "工作空间名重复: default"},
		{"tasks 非对象", `"workspaces":{"teamA":{"tasks":[]}}}`, "workspaces.teamA.tasks 必须是对象"},
		{
			"模型未配置",
			`"workspaces":{"teamA":{"tasks":{"t":{"model":"nope"}}}}}`,
			"workspaces.teamA.tasks.t.model 引用了未配置的模型: nope",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := FromDict(mustParse(t, base+testCase.payload))
			if err == nil {
				t.Fatalf("应当报错 %q", testCase.want)
			}
			var configErr *ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("期望 ConfigError，实际 %T: %v", err, err)
			}
			if configErr.Message != testCase.want {
				t.Errorf("错误文本不一致\n期望: %s\n实际: %s", testCase.want, configErr.Message)
			}
		})
	}
}

// TestTaskNameConflictsWithModelGlobally 固化：工作空间不能用来遮蔽模型名。
//
// 否则「任务名解析成哪个模型」会取决于调用方是否带了工作空间头，同一个名字在
// 不同入口指向不同东西。
func TestTaskNameConflictsWithModelGlobally(t *testing.T) {
	_, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"workspaces":{"teamA":{"tasks":{"model-a":{"model":"model-a"}}}}}`))
	if err == nil {
		t.Fatal("工作空间内的任务名撞模型名时应当报错")
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Message != "任务名与模型名称冲突: model-a" {
		t.Fatalf("错误文本不一致: %v", err)
	}
}

// TestTaskModelIsOptional 固化：任务可以先不指定模型。
//
// 缺失、null、空白串三种写法都表示「尚未指定」——它们在校验层是等价的，若只放行
// 其中一种，另外两种就会报出「引用了未配置的模型」这种驴唇不对马嘴的错。
func TestTaskModelIsOptional(t *testing.T) {
	base := `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},"tasks":%s}`
	cases := map[string]string{
		"缺失":   `{"t":{"params":{"temperature":0.5}}}`,
		"null": `{"t":{"model":null}}`,
		"空白串":  `{"t":{"model":"   "}}`,
	}
	for name, tasks := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := FromDict(mustParse(t, strings.Replace(base, "%s", tasks, 1)))
			if err != nil {
				t.Fatalf("未指定模型的任务应当合法: %v", err)
			}
			if len(cfg.Tasks) != 1 {
				t.Fatalf("任务数应为 1，实际 %d", len(cfg.Tasks))
			}
			if cfg.Tasks[0].Model != "" {
				t.Errorf("未指定模型时应归一成空串，实际 %q", cfg.Tasks[0].Model)
			}
		})
	}
}

// TestTaskModelTypoStillFails 固化放宽的边界：**填了但填错**仍然是错误。
//
// 放宽「可以没有模型」与「可以写错模型名」只差一步，混起来就会让错字静默退化成一个
// 空任务——用户在界面上看到任务存在，调用时才发现它没有模型。
func TestTaskModelTypoStillFails(t *testing.T) {
	_, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"tasks":{"t":{"model":"model-typo"}}}`))
	if err == nil {
		t.Fatal("写错模型名时应当报错")
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Message != "tasks.t.model 引用了未配置的模型: model-typo" {
		t.Fatalf("错误文本不一致: %v", err)
	}
}

// TestTaskDisplayNameIsParsed 固化显示名的解析与归一。
//
// 显示名是给**人**看的，因此两端空白要清掉（否则界面上会显示成「 长文摘要 」）；
// 没配就是空串，与「没取名字」这个状态一致。
func TestTaskDisplayNameIsParsed(t *testing.T) {
	cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"tasks":{"named":{"model":"model-a","display_name":"  长文摘要  "},"plain":{"model":"model-a"}}}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	named, found := cfg.TaskFor("named")
	if !found || named.DisplayName != "长文摘要" {
		t.Errorf("显示名应为去空白的 长文摘要，实际 %+v（found=%v）", named, found)
	}
	plain, found := cfg.TaskFor("plain")
	if !found {
		t.Fatal("plain 任务应存在")
	}
	if plain.DisplayName != "" {
		t.Errorf("未配置显示名时应为空串，实际 %q", plain.DisplayName)
	}
}

// TestTaskPlanWithEmptyModelIsEmpty 固化：空模型的任务产出空计划。
//
// proxy 正是靠 Primary.Model == "" 判定「尚未指定模型」并给出明确错误。若这里悄悄
// 填进别的模型名，那条判定就永远不会触发，回落会变成静默行为。
func TestTaskPlanWithEmptyModelIsEmpty(t *testing.T) {
	cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"model-a":{"targets":[{"provider":"p","key":"k"}]}},
		"tasks":{"placeholder":{}}}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	task, found := cfg.TaskFor("placeholder")
	if !found {
		t.Fatal("placeholder 任务应存在")
	}
	plan := task.Plan()
	if plan.Primary.Model != "" || plan.Fallback != nil {
		t.Errorf("未指定模型的任务不应产生任何目标，实际 %+v", plan)
	}
}

// TestQuotaFreezeThresholds 钉住 5h/7d 提前冻结底限的解析口径。
//
// 缺省与显式 0 都表示关闭；负值与超过 1 的误配按边界夹住而不是让启动失败——
// 一个阈值写错不该让整份配置不可用。
func TestQuotaFreezeThresholds(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
		key  string
	}{
		{name: "缺省关闭", body: `{}`, want: 0, key: "quota_freeze_threshold_5h"},
		{name: "解析 5h", body: `"quota_freeze_threshold_5h":0.05`, want: 0.05, key: "quota_freeze_threshold_5h"},
		{name: "解析 7d", body: `"quota_freeze_threshold_7d":0.1`, want: 0.1, key: "quota_freeze_threshold_7d"},
		{name: "负值夹到 0", body: `"quota_freeze_threshold_5h":-0.2`, want: 0, key: "quota_freeze_threshold_5h"},
		{name: "超过 1 夹到 1", body: `"quota_freeze_threshold_7d":1.5`, want: 1, key: "quota_freeze_threshold_7d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := ""
			if tc.body != "{}" {
				extra = "," + tc.body
			}
			cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k"`+extra+`}`))
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			got := cfg.QuotaFreezeThreshold5h
			if tc.key == "quota_freeze_threshold_7d" {
				got = cfg.QuotaFreezeThreshold7d
			}
			if got != tc.want {
				t.Errorf("%s = %v，期望 %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestQuotaFreezeThresholdWhitelist 钉住提前冻结白名单：允许模型 ID 或别名，未知名字
// 在配置层直接报错，而不是等额度触底时静默漏掉一个模型。
func TestQuotaFreezeThresholdWhitelist(t *testing.T) {
	base := `{"config_version":4,"local_api_key":"k",` +
		`"providers":{"p":{"base_url":"https://p.example","keys":{"k":{"api_key":"x"}}}},` +
		`"models":{"m":{"targets":[{"provider":"p","key":"k","upstream_model":"u"}],` +
		`"aliases":["alias-m"]}}`
	cfg, err := FromDict(mustParse(t, base+`,"quota_freeze_threshold_whitelist":["m","alias-m"]}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := cfg.QuotaFreezeThresholdWhitelist; len(got) != 2 || got[0] != "m" || got[1] != "alias-m" {
		t.Fatalf("白名单解析结果不符: %v", got)
	}

	if _, err := FromDict(mustParse(t, base+`,"quota_freeze_threshold_whitelist":["missing"]}`)); err == nil {
		t.Fatal("引用未配置模型的白名单应报错")
	} else if !strings.Contains(err.Error(), "quota_freeze_threshold_whitelist[0]") {
		t.Fatalf("错误文案应带字段与下标: %v", err)
	}
}
