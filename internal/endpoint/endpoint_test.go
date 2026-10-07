package endpoint

import (
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// TestLookupClassifiesEveryFamily 锁定入站路径到端点族的分类。
func TestLookupClassifiesEveryFamily(t *testing.T) {
	cases := []struct {
		path    string
		family  Family
		subpath string
		known   bool
	}{
		{path: "chat/completions", family: FamilyOpenAI, known: true},
		{path: "messages", family: FamilyAnthropic, known: true},
		{path: "responses", family: FamilyResponses, known: true},
		{path: "images/generations", family: FamilyImages, known: true},
		{path: "images/edits", family: FamilyImages, known: true},
		{path: "images/variations", family: FamilyImages, known: true},
		{path: "embeddings", family: FamilyEmbeddings, known: true},
		{path: "audio/speech", family: FamilySpeech, known: true},
		{path: "audio/transcriptions", family: FamilyTranscriptions, known: true},
		{path: "audio/translations", family: FamilyTranslations, known: true},
		{path: "videos", family: FamilyVideo, known: true},
		// 视频是唯一允许子路径的族。
		{path: "videos/vid_1", family: FamilyVideo, subpath: "/vid_1", known: true},
		{path: "videos/vid_1/content", family: FamilyVideo, subpath: "/vid_1/content", known: true},
		{path: "rerank", family: FamilyRerank, known: true},
		// 自有端点不能被前缀匹配吞掉：count_tokens 与 models 都不是上游族。
		{path: "messages/count_tokens"},
		{path: "models"},
		{path: "videosx"}, // 前缀不是边界
		{path: ""},
		// 大小写敏感：上游路径按字面拼，不替上游猜大小写。
		{path: "IMAGES/GENERATIONS"},
	}
	for _, item := range cases {
		family, subpath, known := Lookup(item.path)
		if family != item.family || subpath != item.subpath || known != item.known {
			t.Errorf("Lookup(%q) = (%q, %q, %v)，期望 (%q, %q, %v)",
				item.path, family, subpath, known, item.family, item.subpath, item.known)
		}
		if got := FamilyOf(item.path); got != item.family {
			t.Errorf("FamilyOf(%q) = %q，期望 %q", item.path, got, item.family)
		}
	}
}

// TestLookupToleratesSurroundingSlashes 验证前后斜杠不影响分类。
//
// /v1/{path:path} 取出的 path 本身不带前导斜杠，但手工构造的调用方（wsproxy、测试）
// 可能带上，因此这里按「两侧空白斜杠无意义」处理。
func TestLookupToleratesSurroundingSlashes(t *testing.T) {
	for _, path := range []string{"/audio/speech", "audio/speech/", "/videos/vid_1/"} {
		if family := FamilyOf(path); family == "" {
			t.Errorf("FamilyOf(%q) 不应为空", path)
		}
	}
	if family, subpath, _ := Lookup("/videos/vid_1/content/"); family != FamilyVideo || subpath != "/vid_1/content" {
		t.Errorf("带尾斜杠的视频子路径解析错误: (%q, %q)", family, subpath)
	}
}

// TestIsPassthroughOnlyForNonChatFamilies 锁定透传判据。
//
// 这条判据决定请求体是否被方言改写：对话三方言**不是**透传（它们本来就要被改写），
// 其余已登记的端点族都只换 model。
func TestIsPassthroughOnlyForNonChatFamilies(t *testing.T) {
	passthroughPaths := []string{
		"images/generations", "images/edits", "images/variations",
		"embeddings", "audio/speech", "audio/transcriptions", "audio/translations",
		"videos", "videos/vid_1/content", "rerank",
	}
	for _, path := range passthroughPaths {
		if !IsPassthrough(path) {
			t.Errorf("IsPassthrough(%q) = false，期望 true", path)
		}
	}
	notPassthrough := []string{"chat/completions", "messages", "responses", "models", "decide", "messages/count_tokens", ""}
	for _, path := range notPassthrough {
		if IsPassthrough(path) {
			t.Errorf("IsPassthrough(%q) = true，期望 false", path)
		}
	}
}

// TestRoutesAgreeWithConfiguredModes 是两个枚举之间的漂移门禁。
//
// 端点族表与 config.upstreamRouteModes 必须一一对应：
//   - 族存在但模式不在配置清单里 -> 用户无法为它配 upstream_routes；
//   - 模式在配置清单里但没有族 -> 用户能配一条对请求永不生效的路由。
//
// 两者都是「配置写得对，行为没变」的静默故障，因此在这里钉死。
func TestRoutesAgreeWithConfiguredModes(t *testing.T) {
	configured := map[string]bool{}
	for _, mode := range config.UpstreamRouteModes() {
		configured[mode] = true
	}
	seen := map[string]bool{}
	for _, route := range Routes() {
		seen[string(route.Family)] = true
		if !configured[string(route.Family)] {
			t.Errorf("端点族 %q 没有对应的 upstream_routes 模式", route.Family)
		}
	}
	for mode := range configured {
		if !seen[mode] {
			t.Errorf("upstream_routes 模式 %q 没有任何入站路径（配了也不会生效）", mode)
		}
	}
}

// TestRoutesReturnsCopy 验证 Routes 返回副本，调用方改不动内部表。
func TestRoutesReturnsCopy(t *testing.T) {
	first := Routes()
	if len(first) == 0 {
		t.Fatal("分类表不应为空")
	}
	first[0].Root = "mutated"
	if Routes()[0].Root == "mutated" {
		t.Fatal("Routes 返回的切片与内部表共享底层数组")
	}
}

// TestIsChatFamily 锁定对话方言集合。
func TestIsChatFamily(t *testing.T) {
	chatFamilies := []Family{FamilyOpenAI, FamilyAnthropic, FamilyResponses}
	for _, family := range chatFamilies {
		if !IsChatFamily(family) {
			t.Errorf("IsChatFamily(%q) = false，期望 true", family)
		}
	}
	for _, family := range []Family{FamilyImages, FamilyEmbeddings, FamilySpeech, FamilyTranscriptions, FamilyTranslations, FamilyVideo, FamilyRerank, ""} {
		if IsChatFamily(family) {
			t.Errorf("IsChatFamily(%q) = true，期望 false", family)
		}
	}
}
