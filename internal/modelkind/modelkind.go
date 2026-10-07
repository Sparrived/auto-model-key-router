// Package modelkind 判定一个模型名属于哪一类（文本 / 图像 / 视频 / 语音合成 /
// 语音识别 / 嵌入 / 重排），供 WebUI 分组、筛选与"这个模型该怎么调"的提示使用。
//
// # 为什么需要它
//
// 上游的 `GET /v1/models` 只回 id：OpenAI 兼容接口里那个 `object: "model"` 是
// **对象种类**（这条 JSON 是什么东西），不是模型类型；Anthropic 的 `type` 同理恒为
// `"model"`。因此没有一家会告诉你"这是图像模型"，分类只能从证据里拼出来。本包把
// 证据分成两层，每层都保留来源（见 Info.Grounds）：
//
//  1. models.dev 目录的 `modalities`（见 BuildCatalog）——覆盖面最广，且价格目录
//     本来就在拉这份 4.7 MB 的文档，**零额外网络成本**；
//  2. 名字规则（见 DefaultRules）——兜底，永远能给出答案，但可能错。
//
// 用户显式配置是第三层、且优先级最高，但它由调用方在拿到本包的判定后覆盖即可，
// 本包不读配置。
//
// # 为什么是"证据的并集"而不是"优先级覆盖"
//
// 两层各自都可能只说对一半：目录知道 `gpt-4o-audio-preview` 的模态是
// text+audio → text+audio，规则知道 `text-embedding-3-small` 是嵌入模型（目录里
// 嵌入模型与聊天模型**同形**：都是 text → text）。任选一层"覆盖"另一层都会丢掉
// 对的信息，因此这里求并集，并把每条证据记进 Grounds，让界面能说清"这个结论是
// 谁给的"。
//
// # 这是派生读数，不是路由判据
//
// 判错了最多是分组难看。**不要**用它拒绝请求或改写路由：自建网关、中转站改名的
// 模型必然落进 unknown（Kinds 为空），把 unknown 挡掉等于把能用的模型判死。
package modelkind

import (
	"regexp"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// Kind 是模型的类型标签。
//
// 一个模型可以同时有多个：`gpt-4o-audio-preview` 既走 chat 端点（文本），又能做
// 语音合成与识别，三者都是真的。因此判定结果是**集合**，不是单选。
type Kind string

const (
	// KindText 是文本模型：走 chat / responses / messages 端点。
	KindText Kind = "text"
	// KindImage 是图像生成/编辑模型。
	KindImage Kind = "image"
	// KindVideo 是视频生成模型。
	KindVideo Kind = "video"
	// KindTTS 是语音合成（文本转语音）模型。
	KindTTS Kind = "tts"
	// KindSTT 是语音识别（语音转文本）模型。
	KindSTT Kind = "stt"
	// KindEmbedding 是嵌入模型。
	KindEmbedding Kind = "embedding"
	// KindRerank 是重排模型。
	KindRerank Kind = "rerank"
	// KindUnknown 只出现在 Primary 的返回值里，**不会**出现在 Info.Kinds 中：
	// Kinds 为空就表示"没有任何证据"。
	KindUnknown Kind = "unknown"
)

// kindOrder 同时决定两件事：Kinds 的排列顺序，以及多能力时 Primary 取哪一个。
//
// 为什么 text 排最前：能走 chat 端点的模型，用户最常关心的是"能不能当聊天模型
// 用"，一个 chat+tts 的模型主标签应当是文本而不是语音。这不会让嵌入/重排模型
// 被误标成文本——目录里那两类与聊天模型同形，finish 会把它们的 text 摘掉（见
// dropDerivedText）。
var kindOrder = []Kind{
	KindText, KindImage, KindVideo, KindTTS, KindSTT, KindEmbedding, KindRerank,
}

// Endpoint 是"该用哪个端点调这个模型"。
//
// 它与 Kind 不是一一对应：文本模型走 chat，而 chat 在本项目里对应三种上游路由
// 模式（openai / anthropic / responses），这里只给出**端点族**，具体模式由
// upstream_routes 决定，不在本包职责内。
type Endpoint string

const (
	// EndpointChat 是对话补全端点（chat/completions、messages、responses）。
	EndpointChat Endpoint = "chat"
	// EndpointImages 是图像生成端点（images/generations、images/edits）。
	EndpointImages Endpoint = "images"
	// EndpointEmbeddings 是嵌入端点。
	EndpointEmbeddings Endpoint = "embeddings"
	// EndpointSpeech 是语音合成端点（audio/speech）。
	EndpointSpeech Endpoint = "speech"
	// EndpointTranscriptions 是语音识别端点（audio/transcriptions）。
	EndpointTranscriptions Endpoint = "transcriptions"
	// EndpointVideo 是视频生成端点。目前没有跨家标准路径，仅作标签。
	EndpointVideo Endpoint = "video"
	// EndpointRerank 是重排端点。同样没有跨家标准路径，仅作标签。
	EndpointRerank Endpoint = "rerank"
)

// endpointsForKind 把类型映射到端点族。
func endpointsForKind(kind Kind) []Endpoint {
	switch kind {
	case KindText:
		return []Endpoint{EndpointChat}
	case KindImage:
		return []Endpoint{EndpointImages}
	case KindVideo:
		return []Endpoint{EndpointVideo}
	case KindTTS:
		return []Endpoint{EndpointSpeech}
	case KindSTT:
		return []Endpoint{EndpointTranscriptions}
	case KindEmbedding:
		return []Endpoint{EndpointEmbeddings}
	case KindRerank:
		return []Endpoint{EndpointRerank}
	}
	return nil
}

// Modalities 是模型的输入输出模态，取值是 models.dev 的词汇：
// text / image / audio / video / pdf。
type Modalities struct {
	Input  []string
	Output []string
}

// IsZero 报告模态是否有任何内容。
func (m Modalities) IsZero() bool { return len(m.Input) == 0 && len(m.Output) == 0 }

// Ground 是一条判定依据。
//
// 保留它是为了让界面能表达"这条标签是谁给的"：目录给的可以信，只有名字规则命中的
// 就该让用户能改。丢掉来源的判定在排障时无法解释。
type Ground struct {
	// Source 是证据来源："catalog" 或 "rule"。
	Source string
	// Detail 是来源里的具体位置：目录命中的归一化 id，或规则 id。
	Detail string
	// Kinds 是这条证据支持的标签。
	Kinds []Kind
	// Modalities 只有目录层能给（规则层不知道模态），零值表示没这条信息。
	Modalities Modalities
}

// Info 是对一个模型名的完整判定。
type Info struct {
	// Kinds 是去重排序后的标签集合；**为空表示没有任何证据**（不是"文本"）。
	Kinds []Kind
	// Endpoints 是由 Kinds 推出的端点族（去重排序）。
	Endpoints []Endpoint
	// Modalities 是所有证据里模态的并集；零值表示没有任何模态信息。
	Modalities Modalities
	// Grounds 是判定依据，按证据加入顺序排列。
	Grounds []Ground
}

// Has 报告判定里是否含某个标签。
func (i Info) Has(kind Kind) bool {
	for _, candidate := range i.Kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

// Primary 返回多能力时的主标签；没有任何证据时返回 KindUnknown。
//
// **判断"能不能当聊天模型用"要用 Has(KindText)，不要看 Primary**：一个
// chat+tts 的模型 Primary 可能是 text，也可能因为规则的先后顺序带上别的标签，
// 只有 Has 是确定的。
func (i Info) Primary() Kind {
	if len(i.Kinds) == 0 {
		return KindUnknown
	}
	return i.Kinds[0]
}

// builder 在判定过程中累积证据。
//
// 用一个未导出的中间结构而不是直接写 Info：Info 是对外形状，不该带"只在一半的判定
// 里有意义"的字段（Kinds 要摘掉被覆盖的 text、Endpoints 要从 Kinds 反推）。
type builder struct {
	grounds    []Ground
	kinds      []Kind
	modalities Modalities
}

func (b *builder) add(ground Ground) {
	b.grounds = append(b.grounds, ground)
	b.kinds = append(b.kinds, ground.Kinds...)
	if !ground.Modalities.IsZero() {
		b.modalities.Input = unionStrings(b.modalities.Input, ground.Modalities.Input)
		b.modalities.Output = unionStrings(b.modalities.Output, ground.Modalities.Output)
	}
}

// info 收口：摘掉被专用结论覆盖的 text、去重排序，再推出端点集合。
func (b *builder) info() Info {
	kinds := sortKinds(dropDerivedText(b.kinds))
	return Info{
		Kinds:      kinds,
		Endpoints:  endpointsOf(kinds),
		Modalities: b.modalities,
		Grounds:    b.grounds,
	}
}

// Catalog 是 models.dev 目录里与类型有关的那部分索引：归一化模型 id → 证据。
//
// 键是 NormalizeID 的结果（不是原始 id），因此查询方也必须先归一化——这一步由
// Resolve 负责，调用方不需要自己处理。
type Catalog map[string]Entry

// Entry 是一个模型在目录里的类型证据。
type Entry struct {
	// Kinds 是从 modalities 推出的类型。
	Kinds []Kind
	// Modalities 是目录里记的输入输出模态。
	Modalities Modalities
}

// BuildCatalog 从 models.dev 的原始文档里抽出类型索引。
//
// 形状是 `{供应商: {models: {模型 id: {...}}}}`（与 internal/pricing 消费的是同一份
// 文档）。两处刻意的做法：
//
//   - **同一个 id 在多家供应商下出现时取并集**，而不是像价格那样"挑一条"。价格必须
//     挑一条（否则你不知道按谁计费），类型不必：任一家说它是图像模型，它就是图像
//     模型。抽到缺 modalities 的那条会让类型凭空消失，而并集不会。
//   - 没有任何 modalities 的模型**不进索引**：那说明目录对它一无所知，此时"没有
//     证据"才是诚实的，凭空造一条 text 会让 unknown 消失，界面再也分不出
//     "目录说是文本"与"目录没见过它"。
func BuildCatalog(document *canonical.Value) Catalog {
	catalog := Catalog{}
	if !document.IsObject() {
		return catalog
	}
	for _, providerID := range document.Obj.Keys() {
		provider, ok := document.Obj.Get(providerID)
		if !ok {
			continue
		}
		models, ok := provider.LookupOK("models")
		if !ok || !models.IsObject() {
			continue
		}
		for _, modelID := range models.Obj.Keys() {
			model, ok := models.Obj.Get(modelID)
			if !ok {
				continue
			}
			modalities, ok := readModalities(model)
			if !ok {
				continue
			}
			kinds := kindsFromModalities(modalities)
			if len(kinds) == 0 {
				continue
			}
			key := NormalizeID(modelID)
			if key == "" {
				continue
			}
			current := catalog[key]
			current.Kinds = mergeKinds(current.Kinds, kinds)
			current.Modalities.Input = unionStrings(current.Modalities.Input, modalities.Input)
			current.Modalities.Output = unionStrings(current.Modalities.Output, modalities.Output)
			catalog[key] = current
		}
	}
	return catalog
}

// readModalities 读一个目录条目的 modalities；缺字段或形状不对时报告 false。
func readModalities(model *canonical.Value) (Modalities, bool) {
	raw, ok := model.LookupOK("modalities")
	if !ok || !raw.IsObject() {
		return Modalities{}, false
	}
	modalities := Modalities{
		Input:  stringItems(raw.Lookup("input")),
		Output: stringItems(raw.Lookup("output")),
	}
	if modalities.IsZero() {
		return Modalities{}, false
	}
	return modalities, true
}

// kindsFromModalities 从模态推类型。
//
// 三条判定按"输出决定产品形态"来写，唯一的例外是语音识别（它是**输入**含音频）：
//
//   - 输出含 image → 图像；输出含 video → 视频；输出含 audio → 语音合成；
//   - 输入含音频、输出含文本 → 语音识别；
//   - 纯文本输出且输入不含音频 → 文本（嵌入/重排也落进这一条，见 dropDerivedText）。
func kindsFromModalities(modalities Modalities) []Kind {
	kinds := []Kind{}
	has := func(list []string, want string) bool {
		for _, item := range list {
			if strings.EqualFold(item, want) {
				return true
			}
		}
		return false
	}
	switch {
	case has(modalities.Output, "video"):
		kinds = append(kinds, KindVideo)
	case has(modalities.Output, "image"):
		kinds = append(kinds, KindImage)
	case has(modalities.Output, "audio"):
		kinds = append(kinds, KindTTS)
	}
	if has(modalities.Input, "audio") && has(modalities.Output, "text") {
		kinds = append(kinds, KindSTT)
	}
	if has(modalities.Output, "text") && !has(modalities.Output, "audio") &&
		!has(modalities.Output, "image") && !has(modalities.Output, "video") &&
		!has(modalities.Input, "audio") {
		kinds = append(kinds, KindText)
	}
	return kinds
}

// stringItems 把字符串数组读成切片；非数组返回 nil。
//
// 与 canonical.StringList 的区别：这里要求每一项都是字符串，数字项直接丢掉，
// 而不是渲染成 "1"——模态里出现数字只说明上游形状不对。
func stringItems(value *canonical.Value) []string {
	if !value.IsArray() {
		return nil
	}
	out := make([]string, 0, len(value.Arr))
	for _, item := range value.Arr {
		if text, ok := item.AsString(); ok {
			out = append(out, text)
		}
	}
	return out
}

// RuleSpec 是一条名字规则的**声明**（编译前）。
type RuleSpec struct {
	// ID 是规则名，会进 Ground.Detail，界面用它解释判定来源。
	ID string
	// Pattern 是对**归一化之后**的模型名匹配的正则（大小写不敏感，见 CompileRules）。
	//
	// 用 Go 的 RE2：不支持环视，因此"先判 A 再排除 B"只能靠规则顺序表达。
	Pattern string
	// Kinds 是命中后给出的标签。
	Kinds []Kind
}

// Rule 是编译后的名字规则。
type Rule struct {
	ID      string
	Kinds   []Kind
	pattern *regexp.Regexp
}

// CompileRules 编译规则表。模式非法时返回错误（默认规则表是常量，编译失败会 panic）。
func CompileRules(specs []RuleSpec) ([]Rule, error) {
	rules := make([]Rule, 0, len(specs))
	for _, spec := range specs {
		pattern, err := regexp.Compile("(?i)" + spec.Pattern)
		if err != nil {
			return nil, err
		}
		rules = append(rules, Rule{ID: spec.ID, Kinds: spec.Kinds, pattern: pattern})
	}
	return rules, nil
}

// DefaultRules 返回内置规则表。
//
// **顺序就是优先级**，且顺序是规则表里最容易改错的地方：
//
//  1. 重排、嵌入、语音识别、语音合成在前——它们是最专用的形态，被后置就会被宽泛的
//     家族规则吃掉（`gpt-4o-mini-tts` 与 `gpt-4o-mini` 共享前缀）；
//  2. `audio-preview` / `realtime` / `omni` 这类**多模态聊天**模型刻意排在 tts 之后：
//     它们的名字里有 audio 但那是输入输出模态，不是"语音合成模型"；
//  3. 图像、视频在家族规则之前（`gpt-image-1` 与 `gpt-` 共享前缀）；
//  4. 文本家族规则最后。
//
// 刻意**没有兜底规则**：一条匹配一切 → text 的规则会让 unknown 永远不出现，把一个
// 没人见过的自建模型标成"文本模型"，而那是猜的。unknown 是诚实的结果，界面应当
// 把它显示成"未分类"而不是"文本"。
func DefaultRules() []Rule {
	rules, err := CompileRules(defaultRuleSpecs())
	if err != nil {
		// 模式是包内常量，编译不过属于程序员错误（与 webui_assets.go 的 mustSub 同理）。
		panic("modelkind: 内置规则表编译失败: " + err.Error())
	}
	return rules
}

// defaultRuleSpecs 是内置规则表本身。拆出来是为了让测试能直接断言"哪条规则命中"。
func defaultRuleSpecs() []RuleSpec {
	return []RuleSpec{
		// 重排：最专用，先判。
		{ID: "rerank", Pattern: `rerank`, Kinds: []Kind{KindRerank}},
		// 嵌入：目录里嵌入模型与聊天模型同形（text → text），只能靠名字。
		{ID: "embedding", Pattern: `embed|(^|[-_/])bge[-_]|(^|[-_/])gte[-_]|(^|[-_/])e5([-_]|$)|(^|[-_/])m3e([-_]|$)|voyage[-_]|jina[-_]embed|nomic[-_]embed`, Kinds: []Kind{KindEmbedding}},
		// 语音识别：whisper 与各家的 transcribe 命名。
		{ID: "stt", Pattern: `whisper|transcri|speech[-_]?to[-_]?text|(^|[-_/])stt([-_]|$)|(^|[-_/])asr([-_]|$)|sensevoice|paraformer|funasr|voxtral`, Kinds: []Kind{KindSTT}},
		// 语音合成：tts-1 / gpt-4o-mini-tts / cosyvoice 等。
		//
		// `speech` 只作为独立词元匹配：`speech-to-text` 已经在前一条被吃掉，
		// 而 `-speech` 作为后缀确实是合成的常见命名。
		{ID: "tts", Pattern: `(^|[-_/])tts([-_]|$)|text[-_]?to[-_]?speech|(^|[-_/])speech([-_]|$)|speech[-_]|cosyvoice|sovits|(^|[-_/])voice[-_]?(clone|synthesis)`, Kinds: []Kind{KindTTS}},
		// 多模态聊天（文本 + 语音输入输出）：名字里有 audio 但它是**聊天模型**，
		// 走 chat 端点，同时能做合成与识别。
		{ID: "chat-audio", Pattern: `audio[-_]?preview|realtime|(^|[-_/])audio([-_/]|$)|(^|[-_/])omni([-_]|$)`, Kinds: []Kind{KindText, KindTTS, KindSTT}},
		// 视频生成。
		{ID: "video", Pattern: `(^|[-_/])veo([-_]|$)|(^|[-_/])sora([-_]|$)|(^|[-_/])kling|seedance|runway|hailuo|(^|[-_/])vidu|cogvideo|hunyuanvideo|(^|[-_/])wan2|ltx[-_]?video|(^|[-_/])pika|text[-_]?to[-_]?video|video[-_]?generation|(^|[-_/])video([-_]|$)`, Kinds: []Kind{KindVideo}},
		// 图像生成/编辑。`-image` 作为独立词元：`gemini-2.5-flash-image` 确实是图像
		// 模型（nano banana），而 `vision` 是聊天模型的视觉输入，刻意不在这里。
		{ID: "image", Pattern: `dall[-_]?e|gpt[-_]?image|(^|[-_/])imagen|flux|stable[-_]?diffusion|(^|[-_/])sd[-_]?[0-9x]|(^|[-_/])sdxl|seedream|nano[-_]?banana|midjourney|(^|[-_/])mj([-_]|$)|ideogram|recraft|(^|[-_/])kolors|hunyuan[-_]?image|qwen[-_]?image|(^|[-_/])wanx|(^|[-_/])z[-_]?image|text[-_]?to[-_]?image|image[-_]?generation|(^|[-_/])image([-_]|$)`, Kinds: []Kind{KindImage}},
		// 文本家族：兜底式地认出主流对话模型。放在最后，因此只会认领前面没人要的名字。
		{ID: "text-family", Pattern: `^gpt-|^o[1-4]([-_]|$)|^chatgpt|claude|gemini|gemma|qwen|deepseek|llama|mistral|mixtral|codestral|magistral|phi-|(^|[-_/])yi[-_]|internlm|baichuan|(^|[-_/])glm|moonshot|kimi|grok|command[-_]?[ar]|(^|[-_/])nova([-_]|$)|titan|sonnet|opus|haiku|(^|[-_/])vl([-_]|$)|vision|instruct|(^|[-_/])chat([-_]|$)|(^|[-_/])turbo([-_]|$)|(^|[-_/])flash([-_]|$)|(^|[-_/])mini([-_]|$)|(^|[-_/])pro([-_]|$)|^text-|ernie|hunyuan|doubao|(^|[-_/])step[-_]|minimax|abab|spark`, Kinds: []Kind{KindText}},
	}
}

// Resolve 给出一个模型名的判定：目录证据 + 规则证据（并集）。
//
// catalog 为 nil 时只剩规则层，仍然可用——目录不可达不该让界面变成一片空白。
// rules 为 nil 时只剩目录层。两者都为空时结果是 Kinds 为空的 Info（unknown）。
func Resolve(id string, catalog Catalog, rules []Rule) Info {
	normalized := NormalizeID(id)
	if normalized == "" {
		return Info{}
	}
	var b builder
	if entry, ok := catalog[normalized]; ok {
		b.add(Ground{Source: "catalog", Detail: normalized, Kinds: entry.Kinds, Modalities: entry.Modalities})
	}
	for _, rule := range rules {
		if rule.pattern.MatchString(normalized) {
			// **首个命中的规则即规则层结论**：规则顺序就是优先级，再往后匹配只会
			// 让"先判专用、后判通用"这条设计失效。
			b.add(Ground{Source: "rule", Detail: rule.ID, Kinds: rule.Kinds})
			break
		}
	}
	return b.info()
}

// dropDerivedText 在结论里含嵌入或重排时摘掉 text。
//
// 为什么需要它：models.dev 里嵌入模型的模态与聊天模型**逐字相同**（text → text），
// 于是目录层会说"这是文本模型"，而规则层说"这是嵌入模型"。两条都对，但把它们并列
// 会得出一个错结论——"这个模型能当聊天模型用"，而这正是界面筛选"文本模型"的依据。
// 嵌入与重排是**替代**文本能力而不是叠加，因此这里以专用结论为准。
//
// 图像/视频/语音三类不需要这一步：它们的模态本来就与纯文本不同
// （text → image / audio），目录层不会额外给出 text。
func dropDerivedText(kinds []Kind) []Kind {
	specialized := false
	for _, kind := range kinds {
		if kind == KindEmbedding || kind == KindRerank {
			specialized = true
			break
		}
	}
	if !specialized {
		return kinds
	}
	out := make([]Kind, 0, len(kinds))
	for _, kind := range kinds {
		if kind != KindText {
			out = append(out, kind)
		}
	}
	return out
}

// sortKinds 去重并按 kindOrder 排序。未在 kindOrder 里的取值排在最后（保持稳定）。
func sortKinds(kinds []Kind) []Kind {
	seen := map[Kind]bool{}
	out := make([]Kind, 0, len(kinds))
	for _, kind := range kinds {
		if seen[kind] {
			continue
		}
		seen[kind] = true
		out = append(out, kind)
	}
	rank := func(kind Kind) int {
		for index, candidate := range kindOrder {
			if candidate == kind {
				return index
			}
		}
		return len(kindOrder)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && rank(out[j]) < rank(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// endpointsOf 把标签集合映射成端点集合（保持 kindOrder 的顺序，天然去重）。
func endpointsOf(kinds []Kind) []Endpoint {
	out := []Endpoint{}
	seen := map[Endpoint]bool{}
	for _, kind := range kinds {
		for _, endpoint := range endpointsForKind(kind) {
			if seen[endpoint] {
				continue
			}
			seen[endpoint] = true
			out = append(out, endpoint)
		}
	}
	return out
}

// mergeKinds 求并集、去重并按 kindOrder 排序。
func mergeKinds(left, right []Kind) []Kind {
	return sortKinds(append(append([]Kind{}, left...), right...))
}

// unionStrings 求并集，忽略大小写重复，保持 left 在前。
func unionStrings(left, right []string) []string {
	out := append([]string{}, left...)
	for _, candidate := range right {
		found := false
		for _, existing := range out {
			if strings.EqualFold(existing, candidate) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, candidate)
		}
	}
	return out
}

// dateSuffixes 是常见的版本/日期后缀，匹配到就从名字尾部反复摘掉。
//
// 为什么必须反复摘：`claude-sonnet-4-5-20250929-v1` 需要先摘 `-v1` 再摘日期，一次
// 只能摘一个的写法会留下 `-20250929`，于是目录永远匹配不上。
var dateSuffixes = []*regexp.Regexp{
	regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`),
	regexp.MustCompile(`-\d{8}$`),
	regexp.MustCompile(`-\d{4}$`),
	regexp.MustCompile(`-v\d+(\.\d+)*$`),
	regexp.MustCompile(`-latest$`),
	regexp.MustCompile(`-preview$`),
}

// cloudPrefixes 是 Bedrock / Vertex 一类"区域.厂商.模型"写法的前置段。
//
// 只认白名单，**不能**按最后一个点切：`gpt-3.5-turbo` 的点后面是 `5-turbo`，按点切
// 会把模型名切成一半。
var cloudPrefixes = map[string]bool{
	// Bedrock 的区域前缀。
	"us": true, "eu": true, "apac": true, "ap": true, "ca": true, "sa": true,
	"us-gov": true, "global": true,
	// 供应商前缀。
	"anthropic": true, "amazon": true, "meta": true, "mistral": true, "cohere": true,
	"ai21": true, "stability": true, "deepseek": true, "qwen": true, "writer": true,
	"google": true, "openai": true, "nvidia": true, "moonshotai": true, "zai": true,
	"minimax": true, "xai": true, "microsoft": true,
}

// NormalizeID 把上游五花八门的模型 id 折成可匹配的键。
//
// 依次做四件事（每一件都有真实来源，不是为了好看）：
//
//  1. 去掉 `:tag` 之后的部分——OpenRouter 的 `:free` / `:nitro`，Ollama 的 `llama3:8b`；
//  2. 取最后一个 `/` 之后的部分——`openai/gpt-4o`、`accounts/fireworks/models/llama-v3p1`；
//  3. 反复摘掉云厂商的前缀段（`us.anthropic.`）与尾部的日期/版本后缀
//     （`-20250929`、`-v1`、`-latest`、`-preview`）；
//  4. 折叠成小写并去掉空白。
//
// 归一化是**有损**的，这是刻意的：它的唯一用途是"尽量匹配上目录与规则"。原始 id 不会
// 被改写——发给上游的模型名始终是原始值，本函数只服务于判定。
func NormalizeID(id string) string {
	normalized := strings.ToLower(strings.TrimSpace(id))
	if normalized == "" {
		return ""
	}
	// 1. `:tag`
	if index := strings.Index(normalized, ":"); index >= 0 {
		normalized = normalized[:index]
	}
	// 2. 最后一段路径
	if index := strings.LastIndex(normalized, "/"); index >= 0 {
		normalized = normalized[index+1:]
	}
	// 3. 云厂商前缀段（可能有多段：us.anthropic.claude-...）。
	for {
		index := strings.Index(normalized, ".")
		if index <= 0 {
			break
		}
		if !cloudPrefixes[normalized[:index]] {
			break
		}
		normalized = normalized[index+1:]
	}
	// 3'. 尾部后缀，反复摘到稳定为止（次数有上界：每轮必须变短）。
	for {
		trimmed := normalized
		for _, suffix := range dateSuffixes {
			trimmed = suffix.ReplaceAllString(trimmed, "")
		}
		if trimmed == normalized || trimmed == "" {
			break
		}
		normalized = trimmed
	}
	// 4. 折叠空白：上游偶有 `gemini 2.5 pro` 这种写法。
	normalized = strings.Join(strings.Fields(normalized), "")
	return normalized
}
