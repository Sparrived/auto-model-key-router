package modelkind

import (
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// catalogFixture 是 models.dev 形状的类型夹具（与 internal/pricing 的夹具同源形状）。
//
// 刻意包含三种容易被写错的情况：
//   - 同一个 id 在两家供应商下重复，其中一家**没有** modalities（并集不能因此丢类型）；
//   - 没有任何 modalities 的模型（不该进索引，否则 unknown 会消失）；
//   - 形状不对的供应商段（不能 panic，也不能污染索引）。
const catalogFixture = `{
  "provider-a": {
    "models": {
      "gpt-image-1": {"modalities": {"input": ["text"], "output": ["image"]}},
      "shared-text-model": {"modalities": {"input": ["text"], "output": ["text"]}},
      "text-embedding-3-small": {"modalities": {"input": ["text"], "output": ["text"]}},
      "no-modalities": {"id": "no-modalities"}
    }
  },
  "provider-b": {
    "models": {
      "gpt-image-1": {"id": "gpt-image-1"},
      "shared-text-model": {"modalities": {"input": ["image", "text"], "output": ["text"]}},
      "whisper-large-v3": {"modalities": {"input": ["audio"], "output": ["text"]}},
      "gpt-4o-mini-tts": {"modalities": {"input": ["text"], "output": ["audio"]}}
    }
  },
  "broken-provider": {"models": "not-an-object"},
  "empty-provider": {"id": "empty-provider"}
}`

func fixtureCatalog(t *testing.T) Catalog {
	t.Helper()
	parsed, err := canonical.ParseString(catalogFixture)
	if err != nil {
		t.Fatalf("解析夹具失败: %v", err)
	}
	return BuildCatalog(parsed)
}

// TestNormalizeID 锁定归一化的每一类真实写法。
//
// 归一化是**有损**的，因此每一条断言都在描述"允许丢什么"：供应商/区域前缀、`:tag`、
// 日期与版本后缀可以丢；模型名本身一个字符都不能丢。
func TestNormalizeID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{"去空白折小写", "  GPT-4O  ", "gpt-4o"},
		{"去供应商前缀", "openai/gpt-4o", "gpt-4o"},
		{"去多段路径前缀", "accounts/fireworks/models/llama-v3p1-70b-instruct", "llama-v3p1-70b-instruct"},
		{"去 Cloudflare 前缀", "@cf/meta/llama-3-8b-instruct", "llama-3-8b-instruct"},
		{"去 OpenRouter 标签", "x-ai/grok-4:free", "grok-4"},
		{"去 Ollama 标签", "llama3:8b", "llama3"},
		{"去 ISO 日期后缀", "gpt-4o-mini-2024-07-18", "gpt-4o-mini"},
		{"去紧凑日期后缀", "gpt-4o-mini-20240718", "gpt-4o-mini"},
		{"去 latest 后缀", "claude-3-5-sonnet-latest", "claude-3-5-sonnet"},
		{"去 preview 后缀", "gpt-4o-audio-preview", "gpt-4o-audio"},
		{"去版本后缀", "claude-3-5-sonnet-v2", "claude-3-5-sonnet"},
		{"Bedrock 区域+厂商前缀与日期版本一起摘", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "claude-sonnet-4-5"},
		{"YYYYMM 后缀", "mistral-large-2411", "mistral-large"},
		// 反向：点号不是分隔符，按最后一个点切会把 gpt-3.5-turbo 切成 5-turbo。
		{"带点的模型名不被截断", "gpt-3.5-turbo", "gpt-3.5-turbo"},
		{"非白名单的点前缀不动", "meta-llama/Llama-3.1-70B-Instruct", "llama-3.1-70b-instruct"},
		{"空格折叠", "gemini 2.5 pro", "gemini2.5pro"},
		{"空串", "", ""},
		{"只有空白", "   ", ""},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := NormalizeID(item.id); got != item.want {
				t.Errorf("NormalizeID(%q) = %q，期望 %q", item.id, got, item.want)
			}
		})
	}
}

// TestBuildCatalogUnionsProviders 钉住"同 id 多供应商取并集"。
//
// 与价格目录的挑选口径**刻意相反**：价格必须挑一条（否则不知道按谁计费），类型必须
// 并集——抽到缺 modalities 的那条会让类型凭空消失。
func TestBuildCatalogUnionsProviders(t *testing.T) {
	catalog := fixtureCatalog(t)

	image, ok := catalog["gpt-image-1"]
	if !ok {
		t.Fatal("gpt-image-1 未进索引")
	}
	if len(image.Kinds) != 1 || image.Kinds[0] != KindImage {
		t.Errorf("gpt-image-1.Kinds = %v，期望 [image]（缺 modalities 的重复条目不该抹掉结论）", image.Kinds)
	}

	shared, ok := catalog["shared-text-model"]
	if !ok {
		t.Fatal("shared-text-model 未进索引")
	}
	if len(shared.Kinds) != 1 || shared.Kinds[0] != KindText {
		t.Errorf("shared-text-model.Kinds = %v，期望 [text]", shared.Kinds)
	}
	if len(shared.Modalities.Input) != 2 {
		t.Errorf("shared-text-model 的输入模态 = %v，期望两家的并集 [text image]", shared.Modalities.Input)
	}

	// 没有任何 modalities 的模型不进索引：目录对它一无所知，"没有证据"才是诚实的。
	if _, ok := catalog["no-modalities"]; ok {
		t.Error("no-modalities 不该进索引（它没有任何 modalities 证据）")
	}

	// 语音两类：合成是 text→audio，识别是 audio→text。
	//
	// 这里走 Resolve 而不是直接查 map：索引的键是**归一化之后**的 id
	// （`whisper-large-v3` 的键是 `whisper-large`），调用方不该自己拼这个键。
	if got := Resolve("gpt-4o-mini-tts", catalog, nil).Kinds; len(got) != 1 || got[0] != KindTTS {
		t.Errorf("gpt-4o-mini-tts.Kinds = %v，期望 [tts]", got)
	}
	if got := Resolve("whisper-large-v3", catalog, nil).Kinds; len(got) != 1 || got[0] != KindSTT {
		t.Errorf("whisper-large-v3.Kinds = %v，期望 [stt]", got)
	}
	if _, ok := catalog["whisper-large"]; !ok {
		t.Error("索引的键应当是归一化之后的 id（whisper-large-v3 → whisper-large）")
	}
}

// TestBuildCatalogRejectsBadShapes 锁定形状不对时的静默降级（不 panic、不污染）。
func TestBuildCatalogRejectsBadShapes(t *testing.T) {
	if catalog := BuildCatalog(canonical.NewNull()); len(catalog) != 0 {
		t.Errorf("非对象文档应得空索引，实得 %d 项", len(catalog))
	}
	catalog := fixtureCatalog(t)
	if _, ok := catalog["broken-provider"]; ok {
		t.Error("models 不是对象的供应商不该进索引")
	}
}

// TestKindsFromModalities 是目录层唯一的推导规则，逐条钉住。
func TestKindsFromModalities(t *testing.T) {
	cases := []struct {
		name       string
		input      []string
		output     []string
		want       []Kind
		wantAbsent []Kind
	}{
		{name: "纯文本对话", input: []string{"text"}, output: []string{"text"}, want: []Kind{KindText}},
		{name: "视觉输入仍是文本模型", input: []string{"text", "image"}, output: []string{"text"}, want: []Kind{KindText}},
		{name: "文生图", input: []string{"text"}, output: []string{"image"}, want: []Kind{KindImage}},
		{name: "文生视频", input: []string{"text"}, output: []string{"video"}, want: []Kind{KindVideo}},
		{name: "语音合成", input: []string{"text"}, output: []string{"audio"}, want: []Kind{KindTTS}},
		{name: "语音识别", input: []string{"audio"}, output: []string{"text"}, want: []Kind{KindSTT}},
		// 音频进音频出：既是合成也是识别，但**不是**文本模型（输出里没有 text）。
		{name: "音频对话", input: []string{"text", "audio"}, output: []string{"text", "audio"}, want: []Kind{KindTTS, KindSTT}, wantAbsent: []Kind{KindText}},
		{name: "PDF 输入不影响结论", input: []string{"text", "pdf"}, output: []string{"text"}, want: []Kind{KindText}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got := kindsFromModalities(Modalities{Input: item.input, Output: item.output})
			for _, kind := range item.want {
				found := false
				for _, candidate := range got {
					if candidate == kind {
						found = true
					}
				}
				if !found {
					t.Fatalf("kindsFromModalities(%v → %v) = %v，缺少 %s", item.input, item.output, got, kind)
				}
			}
			for _, kind := range item.wantAbsent {
				for _, candidate := range got {
					if candidate == kind {
						t.Errorf("kindsFromModalities(%v → %v) = %v，不该含 %s", item.input, item.output, got, kind)
					}
				}
			}
		})
	}
}

// TestResolveRuleOrderIsPriority 是本包最重要的一条断言：规则顺序就是优先级。
//
// 三条都曾经是真实存在的误判：
//   - `gpt-4o-mini-tts` 被 `^gpt-` 家族规则吃成文本模型（合成的命名与聊天模型共享前缀）；
//   - `gpt-image-1` 同样被 `^gpt-` 吃掉（它是图像模型，走 images 端点）；
//   - `text-embedding-3-small` 因为名字里有 text 被家族规则吃成文本模型。
func TestResolveRuleOrderIsPriority(t *testing.T) {
	rules := DefaultRules()
	cases := []struct {
		id   string
		want []Kind
	}{
		{"gpt-4o-mini-tts", []Kind{KindTTS}},
		{"gpt-image-1", []Kind{KindImage}},
		{"dall-e-3", []Kind{KindImage}},
		{"text-embedding-3-small", []Kind{KindEmbedding}},
		{"bge-m3", []Kind{KindEmbedding}},
		{"voyage-3-large", []Kind{KindEmbedding}},
		{"whisper-1", []Kind{KindSTT}},
		{"gpt-4o-transcribe", []Kind{KindSTT}},
		{"tts-1-hd", []Kind{KindTTS}},
		{"veo-3.0-generate-001", []Kind{KindVideo}},
		{"sora-2", []Kind{KindVideo}},
		{"rerank-v1", []Kind{KindRerank}},
		{"gemini-2.5-flash-image", []Kind{KindImage}},
		// 多模态聊天模型：名字里有 audio，但它是**聊天模型**，同时能做合成与识别。
		{"gpt-4o-audio-preview", []Kind{KindText, KindTTS, KindSTT}},
		{"qwen2.5-omni-7b", []Kind{KindText, KindTTS, KindSTT}},
		// 视觉输入仍是文本模型，不能被当成图像模型。
		{"qwen2.5-vl-72b-instruct", []Kind{KindText}},
		{"gpt-4-vision-preview", []Kind{KindText}},
		{"claude-sonnet-4-5", []Kind{KindText}},
		{"gpt-4o", []Kind{KindText}},
	}
	for _, item := range cases {
		t.Run(item.id, func(t *testing.T) {
			got := Resolve(item.id, nil, rules)
			if !equalKinds(got.Kinds, item.want) {
				t.Errorf("Resolve(%q).Kinds = %v，期望 %v", item.id, got.Kinds, item.want)
			}
		})
	}
}

// TestResolveUnknownStaysUnknown 钉住"没有兜底规则"这条设计。
//
// 一条匹配一切 → text 的规则会让 unknown 永远不出现，把没人见过的自建/中转模型
// 标成"文本模型"，而那是猜的。
func TestResolveUnknownStaysUnknown(t *testing.T) {
	info := Resolve("my-gateway-model-x", nil, DefaultRules())
	if len(info.Kinds) != 0 {
		t.Errorf("未命中任何证据时 Kinds 应为空，实得 %v", info.Kinds)
	}
	if info.Primary() != KindUnknown {
		t.Errorf("Primary() = %q，期望 %q", info.Primary(), KindUnknown)
	}
	if len(info.Endpoints) != 0 {
		t.Errorf("未分类模型不该推出端点，实得 %v", info.Endpoints)
	}
}

// TestResolveDropsDerivedTextForEmbedding 钉住嵌入/重排的 text 摘除。
//
// models.dev 里嵌入模型的模态与聊天模型逐字相同（text → text），因此目录层会说
// "这是文本模型"。两条证据并列会得出一个错结论——"这个模型能当聊天模型用"，
// 而界面正是按这个结论筛选"文本模型"的。
func TestResolveDropsDerivedTextForEmbedding(t *testing.T) {
	catalog := fixtureCatalog(t)
	info := Resolve("text-embedding-3-small", catalog, DefaultRules())

	if !equalKinds(info.Kinds, []Kind{KindEmbedding}) {
		t.Fatalf("Kinds = %v，期望 [embedding]（目录推出的 text 应被摘掉）", info.Kinds)
	}
	if info.Has(KindText) {
		t.Error("嵌入模型不该同时被标成文本模型（它替代而不是叠加文本能力）")
	}
	if !equalEndpoints(info.Endpoints, []Endpoint{EndpointEmbeddings}) {
		t.Errorf("Endpoints = %v，期望 [embeddings]", info.Endpoints)
	}
}

// TestResolveRecordsGrounds 钉住"每条判定都带来源"。
func TestResolveRecordsGrounds(t *testing.T) {
	catalog := fixtureCatalog(t)
	info := Resolve("gpt-image-1", catalog, DefaultRules())

	if len(info.Grounds) != 2 {
		t.Fatalf("Grounds 有 %d 条，期望 2 条（目录一条、规则一条）", len(info.Grounds))
	}
	if info.Grounds[0].Source != "catalog" || info.Grounds[0].Detail != "gpt-image-1" {
		t.Errorf("第一条证据 = %+v，期望 catalog/gpt-image-1", info.Grounds[0])
	}
	if info.Grounds[1].Source != "rule" || info.Grounds[1].Detail != "image" {
		t.Errorf("第二条证据 = %+v，期望 rule/image", info.Grounds[1])
	}
	if !equalEndpoints(info.Endpoints, []Endpoint{EndpointImages}) {
		t.Errorf("Endpoints = %v，期望 [images]", info.Endpoints)
	}
}

// TestResolveLayersDegradeIndependently 锁定"任一层缺了都还能用"。
//
// 目录不可达不该让界面变成一片空白；没有规则表时也不该把目录结论丢掉。
func TestResolveLayersDegradeIndependently(t *testing.T) {
	catalog := fixtureCatalog(t)

	// 只有目录：没有规则表时照样给出结论。
	onlyCatalog := Resolve("shared-text-model", catalog, nil)
	if !equalKinds(onlyCatalog.Kinds, []Kind{KindText}) {
		t.Errorf("只有目录时 Kinds = %v，期望 [text]", onlyCatalog.Kinds)
	}
	if len(onlyCatalog.Grounds) != 1 || onlyCatalog.Grounds[0].Source != "catalog" {
		t.Errorf("只有目录时 Grounds = %+v，期望仅一条 catalog", onlyCatalog.Grounds)
	}

	// 只有规则：目录为 nil 时不影响规则层。
	onlyRules := Resolve("gpt-image-1", nil, DefaultRules())
	if !equalKinds(onlyRules.Kinds, []Kind{KindImage}) {
		t.Errorf("只有规则时 Kinds = %v，期望 [image]", onlyRules.Kinds)
	}

	// 两层都空、以及空 id：一律 unknown，不 panic。
	if info := Resolve("gpt-4o", nil, nil); len(info.Kinds) != 0 {
		t.Errorf("两层都空时 Kinds = %v，期望空", info.Kinds)
	}
	if info := Resolve("", nil, DefaultRules()); len(info.Kinds) != 0 || len(info.Grounds) != 0 {
		t.Errorf("空 id 应得空判定，实得 %+v", info)
	}
}

// TestResolveEndpointsForMedia 锁定端点族推导（含"文本走 chat"）。
func TestResolveEndpointsForMedia(t *testing.T) {
	rules := DefaultRules()
	cases := []struct {
		id   string
		want []Endpoint
	}{
		{"gpt-4o", []Endpoint{EndpointChat}},
		{"gpt-image-1", []Endpoint{EndpointImages}},
		{"tts-1", []Endpoint{EndpointSpeech}},
		{"whisper-1", []Endpoint{EndpointTranscriptions}},
		{"text-embedding-3-small", []Endpoint{EndpointEmbeddings}},
		{"veo-3.0-generate-001", []Endpoint{EndpointVideo}},
		{"rerank-v1", []Endpoint{EndpointRerank}},
		{"gpt-4o-audio-preview", []Endpoint{EndpointChat, EndpointSpeech, EndpointTranscriptions}},
	}
	for _, item := range cases {
		t.Run(item.id, func(t *testing.T) {
			got := Resolve(item.id, nil, rules).Endpoints
			if !equalEndpoints(got, item.want) {
				t.Errorf("Resolve(%q).Endpoints = %v，期望 %v", item.id, got, item.want)
			}
		})
	}
}

// TestDefaultRulesAreWellFormed 锁定内置规则表本身：能编译、id 唯一、每条都给出标签。
func TestDefaultRulesAreWellFormed(t *testing.T) {
	specs := defaultRuleSpecs()
	seen := map[string]bool{}
	for _, spec := range specs {
		if spec.ID == "" {
			t.Error("规则缺少 id（Ground.Detail 会变成空串，界面无法解释来源）")
		}
		if seen[spec.ID] {
			t.Errorf("规则 id 重复: %s", spec.ID)
		}
		seen[spec.ID] = true
		if len(spec.Kinds) == 0 {
			t.Errorf("规则 %s 没有给出任何类型", spec.ID)
		}
	}
	if _, err := CompileRules(specs); err != nil {
		t.Fatalf("内置规则表编译失败: %v", err)
	}
}

// equalKinds 比较类型切片（顺序敏感）。
func equalKinds(left, right []Kind) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// equalEndpoints 比较端点切片（顺序敏感）。
func equalEndpoints(left, right []Endpoint) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
