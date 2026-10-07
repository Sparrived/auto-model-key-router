package config

import (
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// newStringValue 是 canonical.NewString 的短别名，只为让上面的用例表读起来短一些。
func newStringValue(value string) *canonical.Value { return canonical.NewString(value) }

// TestUpstreamRouteModesCoverEveryEndpointFamily 锁定可选路由模式集合。
//
// 这个集合是对外契约的一部分：管理界面据此渲染输入框，校验据此接受/拒绝配置。
// 少一个模式 = 用户无法为那一族配路径（请求只能走默认路径，中转站换个前缀就调不通）。
func TestUpstreamRouteModesCoverEveryEndpointFamily(t *testing.T) {
	want := []string{
		"openai", "anthropic", "responses",
		"images", "embeddings",
		"speech", "transcriptions", "translations", "video", "rerank",
	}
	got := UpstreamRouteModes()
	if len(got) != len(want) {
		t.Fatalf("模式数量 = %d，期望 %d：%v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("模式[%d] = %q，期望 %q（顺序即界面展示顺序）", index, got[index], want[index])
		}
	}
	// 每个模式都必须有默认路径与可读名：缺默认路径会让 NormalizeUpstreamRoutePath
	// 拼出空后缀，缺可读名会让界面显示原始英文 id。
	for _, mode := range got {
		if UpstreamRouteDefaultPath(mode) == "" {
			t.Errorf("模式 %q 缺少默认路径", mode)
		}
		if upstreamRouteLabels[mode] == "" {
			t.Errorf("模式 %q 缺少可读名", mode)
		}
	}
}

// TestNormalizeUpstreamRouteModeAliases 锁定新增端点族的别名与默认路径。
func TestNormalizeUpstreamRouteModeAliases(t *testing.T) {
	cases := []struct {
		raw      string
		wantMode string
		wantPath string
	}{
		// 语音合成：tts / speech / voice 都是同一条端点。
		{"tts", "speech", "v1/audio/speech"},
		{"TTS", "speech", "v1/audio/speech"},
		{"audio/speech", "speech", "v1/audio/speech"},
		// 语音转写：whisper / stt / asr。
		{"whisper", "transcriptions", "v1/audio/transcriptions"},
		{"stt", "transcriptions", "v1/audio/transcriptions"},
		{"audio/transcriptions", "transcriptions", "v1/audio/transcriptions"},
		// 语音翻译是独立端点。
		{"audio/translations", "translations", "v1/audio/translations"},
		// 视频：videos / sora。
		{"videos", "video", "v1/videos"},
		{"sora", "video", "v1/videos"},
		// 重排。
		{"reranking", "rerank", "v1/rerank"},
	}
	for _, item := range cases {
		mode, err := NormalizeUpstreamRouteMode(newStringValue(item.raw))
		if err != nil {
			t.Fatalf("NormalizeUpstreamRouteMode(%q) 报错: %v", item.raw, err)
		}
		if mode != item.wantMode {
			t.Errorf("NormalizeUpstreamRouteMode(%q) = %q，期望 %q", item.raw, mode, item.wantMode)
		}
		path, err := NormalizeUpstreamRoutePath(mode, newStringValue("v1"))
		if err != nil {
			t.Fatalf("NormalizeUpstreamRoutePath(%q) 报错: %v", mode, err)
		}
		if path != item.wantPath {
			t.Errorf("模式 %q 的前缀 v1 应展开成 %q，实际 %q", mode, item.wantPath, path)
		}
	}
}

// TestNormalizeUpstreamRouteModeRejectsUnknown 验证未知模式被拒且错误信息列出全集。
//
// 错误文案从模式清单现算：手写枚举文案的写法在新增端点族时必然漂移，而这条错误是
// 运维唯一的线索。
func TestNormalizeUpstreamRouteModeRejectsUnknown(t *testing.T) {
	_, err := NormalizeUpstreamRouteMode(newStringValue("video-generation-2"))
	if err == nil {
		t.Fatal("未知模式应报错")
	}
	for _, mode := range UpstreamRouteModes() {
		if !strings.Contains(err.Error(), mode) {
			t.Errorf("错误信息 %q 未列出可选项 %q", err.Error(), mode)
		}
	}
}

// TestParseUnifiedModelEveryEndpointFamily 验证新增族都能被解析。
func TestParseUnifiedModelEveryEndpointFamily(t *testing.T) {
	cfg, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{
			"chat":{"targets":[{"provider":"p","key":"k"}]},
			"img":{"targets":[{"provider":"p","key":"k"}]},
			"emb":{"targets":[{"provider":"p","key":"k"}]},
			"tts":{"targets":[{"provider":"p","key":"k"}]},
			"stt":{"targets":[{"provider":"p","key":"k"}]},
			"vid":{"targets":[{"provider":"p","key":"k"}]},
			"rank":{"targets":[{"provider":"p","key":"k"}]}},
		"unified_model":{
			"default":{"primary":{"model":"chat"}},
			"image":{"primary":{"model":"img"}},
			"embeddings":{"primary":{"model":"emb"}},
			"speech":{"primary":{"model":"tts"}},
			"transcriptions":{"primary":{"model":"stt"}},
			"video":{"primary":{"model":"vid"}},
			"rerank":{"primary":{"model":"rank"}}}}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := map[string]string{
		"default":        "chat",
		"image":          "img",
		"embeddings":     "emb",
		"speech":         "tts",
		"transcriptions": "stt",
		"video":          "vid",
		"rerank":         "rank",
	}
	for planName, modelID := range want {
		plan := cfg.UnifiedModel.Plan(planName)
		if plan == nil {
			t.Errorf("计划 %q 未被解析", planName)
			continue
		}
		if plan.Primary.Model != modelID {
			t.Errorf("计划 %q 的主模型 = %q，期望 %q", planName, plan.Primary.Model, modelID)
		}
	}
}

// TestUnifiedModelValidatesEveryFamily 验证新增族里的失效引用会被校验拦下。
//
// 只校验 default/image/embeddings 的写法会让 speech/video/rerank 族里的错字一路留到
// 运行时——表现是「unified-model 配好了，但调 /v1/video 报未配置模型」。
func TestUnifiedModelValidatesEveryFamily(t *testing.T) {
	_, err := FromDict(mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example.test","keys":{"k":{"api_key":"a"}}}},
		"models":{"chat":{"targets":[{"provider":"p","key":"k"}]}},
		"unified_model":{
			"default":{"primary":{"model":"chat"}},
			"video":{"primary":{"model":"不存在的模型"}}}}`))
	if err == nil {
		t.Fatal("video 计划引用未配置的模型应当报错")
	}
	if !strings.Contains(err.Error(), "unified_model.video.primary") {
		t.Errorf("错误信息应指出具体路径，实际: %v", err)
	}
}

// TestUnifiedPlanNamesOrderIsStable 锁定计划名清单与顺序。
//
// 顺序不是审美问题：configops 的修复逻辑按这个顺序挑替代模型，管理界面按这个顺序
// 渲染分族区块，已发布配置里的键也按这个顺序写盘。
func TestUnifiedPlanNamesOrderIsStable(t *testing.T) {
	want := []string{"default", "image", "embeddings", "speech", "transcriptions", "video", "rerank"}
	if len(UnifiedPlanNames) != len(want) {
		t.Fatalf("计划数 = %d，期望 %d：%v", len(UnifiedPlanNames), len(want), UnifiedPlanNames)
	}
	for index := range want {
		if UnifiedPlanNames[index] != want[index] {
			t.Errorf("计划[%d] = %q，期望 %q", index, UnifiedPlanNames[index], want[index])
		}
	}
	// Plan/SetPlan 必须对清单里的每个名字都有效：两个 switch 漂移的表现是
	// 「解析进去了，但取不出来（或写不回去）」。
	for _, name := range UnifiedPlanNames {
		unified := &UnifiedModelConfig{}
		plan := &RoutePlan{Primary: RouteTarget{Model: "m"}}
		unified.SetPlan(name, plan)
		if name == "default" {
			// Default 由字段本身承载，不走 SetPlan（它没有 default 分支）。
			continue
		}
		if got := unified.Plan(name); got == nil || got.Primary.Model != "m" {
			t.Errorf("计划 %q 写入后取不回来", name)
		}
	}
	if (&UnifiedModelConfig{}).Plan("unknown-plan") != nil {
		t.Error("未知计划名应返回 nil")
	}
}
