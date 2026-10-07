package configops

import (
	"slices"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件固化 unified_model 的分族计划在 configops 这一层是完整的。
//
// 计划名清单同时驱动解析、校验、修复、切换与展示。任何一处自己再写一份清单，
// 漂移的表现都是「配置写进去了，但某个族的引用被静默丢掉或永远改不动」——升级时
// 直接丢用户数据，比崩溃更难查。因此这里既锁清单本身，也锁每个族的真实行为。

// TestUnifiedPlanNamesMatchConfig 钉住计划名清单只有一份来源。
func TestUnifiedPlanNamesMatchConfig(t *testing.T) {
	if !slices.Equal(UNIFIEDPlanNames, config.UnifiedPlanNames) {
		t.Fatalf("configops 的计划清单 = %v，config 的 = %v，两者必须逐项一致",
			UNIFIEDPlanNames, config.UnifiedPlanNames)
	}
	if UNIFIEDPlanNames[0] != "default" {
		t.Fatalf("default 必须排在最前，实际 %q", UNIFIEDPlanNames[0])
	}
}

// TestUnifiedTargetsCoverEveryPlanAndRole 钉住目标名集合由清单展开而来。
func TestUnifiedTargetsCoverEveryPlanAndRole(t *testing.T) {
	want := make([]string, 0, len(UNIFIEDPlanNames)*2)
	for _, planName := range UNIFIEDPlanNames {
		want = append(want, planName+".primary", planName+".fallback")
	}
	if !slices.Equal(UnifiedTargets, want) {
		t.Fatalf("目标集合 = %v，期望 %v", UnifiedTargets, want)
	}
	// CLI 的 --unified-target 直接用这个集合做校验：少一项 = 命令行改不了那一族。
	for _, target := range []string{"speech.primary", "transcriptions.fallback", "video.primary", "rerank.primary"} {
		if !slices.Contains(UnifiedTargets, target) {
			t.Errorf("目标 %q 不在 UnifiedTargets 里", target)
		}
	}
}

// TestRepairUnifiedModelDropsDanglingEndpointFamilies 固化：
// 分族计划引用了已删除的模型时，整条计划被清掉。
//
// 只清理 default/image/embeddings 的旧写法会把 speech/video/rerank 里的悬空引用一直
// 留在配置里：写盘成功、校验（如果只看存在性）也可能通过，直到用户真的调用那个端点
// 才报「模型未配置」。
func TestRepairUnifiedModelDropsDanglingEndpointFamilies(t *testing.T) {
	data := mustParse(t, `{
		"config_version": 4, "local_api_key": "k",
		"providers": {"p": {"base_url": "https://a.example.test", "keys": {"k": {"api_key": "a"}}}},
		"models": {"alive": {"targets": [{"provider": "p", "key": "k"}]}},
		"unified_model": {
			"default": {"primary": {"model": "alive"}},
			"image": {"primary": {"model": "gone"}},
			"speech": {"primary": {"model": "gone"}},
			"transcriptions": {"primary": {"model": "alive"}, "fallback": {"model": "gone"}},
			"video": {"primary": {"model": "gone"}},
			"rerank": {"primary": {"model": "gone"}}}}`)

	if err := RepairUnifiedModel(data); err != nil {
		t.Fatalf("修复失败: %v", err)
	}
	unified := mustLookup(t, data, "unified_model")
	for _, planName := range []string{"image", "speech", "video", "rerank"} {
		if _, found := unified.LookupOK(planName); found {
			t.Errorf("引用了已删除模型的 %s 计划应被清掉: %s", planName, canonical.Dumps(unified))
		}
	}
	// primary 有效、fallback 悬空时只摘 fallback，保留计划本身。
	transcriptions := mustLookup(t, unified, "transcriptions")
	if _, found := transcriptions.LookupOK("fallback"); found {
		t.Errorf("悬空的 fallback 应被摘掉: %s", canonical.Dumps(transcriptions))
	}
	if transcriptions.Lookup("primary").Lookup("model").StringValue() != "alive" {
		t.Errorf("有效的 primary 不应被牵连: %s", canonical.Dumps(transcriptions))
	}
	// default 的主模型即使悬空也保留（与既有行为一致：它是唯一必需的计划）。
	if _, found := mustLookup(t, unified, "default").LookupOK("primary"); !found {
		t.Errorf("default.primary 不应被清掉: %s", canonical.Dumps(unified))
	}
}

// TestSetUnifiedModelRoundTripsEveryEndpointFamily 固化：写入即规范化，且族顺序稳定。
func TestSetUnifiedModelRoundTripsEveryEndpointFamily(t *testing.T) {
	data := mustParse(t, `{
		"config_version": 4, "local_api_key": "k",
		"providers": {"p": {"base_url": "https://a.example.test", "keys": {"k": {"api_key": "a"}}}},
		"models": {"chat": {"targets": [{"provider": "p", "key": "k"}]},
		           "media": {"targets": [{"provider": "p", "key": "k"}]}}}`)

	unified := mustParse(t, `{
		"default": {"primary": {"model": "chat"}},
		"video": {"primary": {"model": "media"}},
		"speech": {"primary": {"model": "media"}},
		"rerank": {"primary": {"model": "media"}}}`)
	if err := SetUnifiedModel(data, unified); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	stored := mustLookup(t, data, "unified_model")
	// 顺序必须按 UNIFIEDPlanNames：default、speech、video、rerank（image/embeddings 未配）。
	keys := stored.Obj.Keys()
	want := []string{"default", "speech", "video", "rerank"}
	if !slices.Equal(keys, want) {
		t.Errorf("落盘键序 = %v，期望 %v", keys, want)
	}
	for _, planName := range want {
		plan := mustLookup(t, stored, planName)
		if !plan.Lookup("primary").IsObject() {
			t.Errorf("%s.primary 丢失: %s", planName, canonical.Dumps(stored))
		}
	}
}

// TestSwitchUnifiedTargetReachesEndpointFamilies 固化：切换接口对新族同样可用。
func TestSwitchUnifiedTargetReachesEndpointFamilies(t *testing.T) {
	data := mustParse(t, `{
		"config_version": 4, "local_api_key": "k",
		"providers": {"p": {"base_url": "https://a.example.test", "keys": {"k": {"api_key": "a"}}}},
		"models": {"media": {"targets": [{"provider": "p", "key": "k"}]}},
		"unified_model": {"default": {"primary": {"model": "media"}}}}`)

	for _, target := range []string{"speech.primary", "transcriptions.primary", "video.primary", "rerank.primary"} {
		if err := SwitchUnifiedTarget(data, target, StringPtr("media"), nil, false); err != nil {
			t.Fatalf("切换 %s 失败: %v", target, err)
		}
	}
	stored := mustLookup(t, data, "unified_model")
	for _, planName := range []string{"speech", "transcriptions", "video", "rerank"} {
		plan := mustLookup(t, stored, planName)
		if got := plan.Lookup("primary").Lookup("model").StringValue(); got != "media" {
			t.Errorf("%s.primary.model = %q，期望 media", planName, got)
		}
	}
	// 未配置的族里 fallback 仍要求先有 primary。
	if err := SwitchUnifiedTarget(data, "image.fallback", StringPtr("media"), nil, false); err == nil {
		t.Error("image 尚未配置 primary 时，写 fallback 应当报错")
	}
}

// TestDeleteModelClearsEndpointFamilyReferences 固化：
// 删掉模型时，新族计划里的引用也会被清理，而不是留下悬空引用。
func TestDeleteModelClearsEndpointFamilyReferences(t *testing.T) {
	data := mustParse(t, `{
		"config_version": 4, "local_api_key": "k",
		"providers": {"p": {"base_url": "https://a.example.test", "keys": {"k": {"api_key": "a"}}}},
		"models": {"keep": {"targets": [{"provider": "p", "key": "k"}]},
		           "drop": {"targets": [{"provider": "p", "key": "k"}]}},
		"unified_model": {
			"default": {"primary": {"model": "keep"}},
			"video": {"primary": {"model": "drop"}},
			"rerank": {"primary": {"model": "keep"}, "fallback": {"model": "drop"}}}}`)

	if err := DeleteModel(data, "drop"); err != nil {
		t.Fatalf("删除模型失败: %v", err)
	}
	if err := RepairUnifiedModel(data); err != nil {
		t.Fatalf("修复失败: %v", err)
	}
	stored := mustLookup(t, data, "unified_model")
	if _, found := stored.LookupOK("video"); found {
		t.Errorf("video 计划引用的模型已删除，应被清掉: %s", canonical.Dumps(stored))
	}
	rerank := mustLookup(t, stored, "rerank")
	if _, found := rerank.LookupOK("fallback"); found {
		t.Errorf("rerank.fallback 引用的模型已删除，应被摘掉: %s", canonical.Dumps(rerank))
	}
	if rerank.Lookup("primary").Lookup("model").StringValue() != "keep" {
		t.Errorf("rerank.primary 不应被牵连: %s", canonical.Dumps(rerank))
	}
}
