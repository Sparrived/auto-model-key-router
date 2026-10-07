// Package endpoint 判定一条入站路径（`/v1/` 之后的部分）属于哪个上游端点族，
// 以及它的请求体对 AMKR 是否**不透明**。
//
// # 为什么要有这个包
//
// 「这条路径是哪个端点」原本散在两个包里各写一遍：internal/proxysupport 用它挑
// upstream_routes 的配置路径，internal/keypool 用它挑 unified_model 的分族计划。
// keypool 不能反向 import proxysupport（proxysupport 已经 import keypool），于是
// 两处 switch 只能靠人记住同步。新增一族（embedding / 图像 / 视频 / TTS / 重排）
// 时漏改一处的表现极难发现——路径走对了，但 unified 计划挑错、或请求体被当成对话
// 请求改写。这里把分类收敛成一份表，两个包都从它派生。
//
// # 为什么要区分「透传」
//
// 对话三方言（chat/completions、messages、responses）的请求体是**同一件事的三种
// 写法**，AMKR 会做方言转换；其余端点族的请求体是它们自己的规范形态（图像是
// prompt、语音合成是 input+voice、视频是 prompt、重排是 query+documents），只该
// 替换 model。IsPassthrough 就是这条界线，它的判错代价是真实的：语音合成的
// `input` 若被按 Responses 方言改写成 messages，上游会收到一个没有 input 的 chat
// 请求。
//
// # 大小写敏感
//
// 与参照实现一致：`IMAGES/GENERATIONS` 是未知路径而不是图像生成。上游路径按字面
// 拼接，AMKR 不该替上游猜大小写。
package endpoint

import "strings"

// Family 是一条入站路径所属的端点族，取值即 upstream_routes 的模式名。
type Family string

const (
	// FamilyOpenAI 是 OpenAI 对话补全（chat/completions）。
	FamilyOpenAI Family = "openai"
	// FamilyAnthropic 是 Anthropic Messages。
	FamilyAnthropic Family = "anthropic"
	// FamilyResponses 是 OpenAI Responses。
	FamilyResponses Family = "responses"
	// FamilyImages 是图像生成 / 编辑 / 变体。
	FamilyImages Family = "images"
	// FamilyEmbeddings 是文本嵌入。
	FamilyEmbeddings Family = "embeddings"
	// FamilySpeech 是语音合成（tts）。
	FamilySpeech Family = "speech"
	// FamilyTranscriptions 是语音转写。
	FamilyTranscriptions Family = "transcriptions"
	// FamilyTranslations 是语音翻译。
	FamilyTranslations Family = "translations"
	// FamilyVideo 是视频生成及其子资源。
	FamilyVideo Family = "video"
	// FamilyRerank 是重排。
	FamilyRerank Family = "rerank"
)

// Route 是入站路径到端点族的映射项。
type Route struct {
	// Root 是入站路径的族根（不含前导 `/v1/`）。
	Root string
	// Family 是该路径对应的端点族。
	Family Family
	// Subpaths 表示族根之后还允许跟子路径。
	//
	// 只有视频需要它（`/v1/videos/{id}`、`/v1/videos/{id}/content`、
	// `/v1/videos/{id}/remix`）。把打开子路径当成默认会让自有路径被误判：
	// `messages/count_tokens` 是 AMKR 本地实现的端点，若前缀匹配生效就会被当成
	// Anthropic 方言。
	Subpaths bool
	// FormUpload 表示该端点的**规范请求体**就是 multipart/form-data。
	//
	// 图像编辑 / 变体与语音转写 / 翻译的上游规范形态都是表单（文件 + prompt）。
	// AMKR 对这类请求只能做不透明转发：路由只需要表单里的 model，其余字段
	// （image、mask、file）原样送出。把它们按「不是 JSON 就拒绝」处理等于这几个
	// 端点从来不可用，因此 proxy 的默认策略按这张表放行（见 endpoint.IsFormUpload）。
	FormUpload bool
}

// routes 是**唯一一份**入站路径分类表，顺序即匹配优先级。
var routes = []Route{
	{Root: "chat/completions", Family: FamilyOpenAI},
	{Root: "messages", Family: FamilyAnthropic},
	{Root: "responses", Family: FamilyResponses},
	{Root: "images/generations", Family: FamilyImages},
	{Root: "images/edits", Family: FamilyImages, FormUpload: true},
	{Root: "images/variations", Family: FamilyImages, FormUpload: true},
	{Root: "embeddings", Family: FamilyEmbeddings},
	{Root: "audio/speech", Family: FamilySpeech},
	{Root: "audio/transcriptions", Family: FamilyTranscriptions, FormUpload: true},
	{Root: "audio/translations", Family: FamilyTranslations, FormUpload: true},
	{Root: "videos", Family: FamilyVideo, Subpaths: true},
	{Root: "rerank", Family: FamilyRerank},
}

// passthrough 是请求体对 AMKR 不透明的端点族。
//
// 对话三方言不在其中：它们的请求体本来就要被改写成上游能吃的形态。
var passthrough = map[Family]bool{
	FamilyImages:         true,
	FamilyEmbeddings:     true,
	FamilySpeech:         true,
	FamilyTranscriptions: true,
	FamilyTranslations:   true,
	FamilyVideo:          true,
	FamilyRerank:         true,
}

// Routes 返回分类表的只读副本（测试与展示用）。
func Routes() []Route {
	out := make([]Route, len(routes))
	copy(out, routes)
	return out
}

// IsChatFamily 报告该族是否为对话方言（请求体会被方言转换）。
func IsChatFamily(family Family) bool {
	switch family {
	case FamilyOpenAI, FamilyAnthropic, FamilyResponses:
		return true
	}
	return false
}

// Lookup 把入站路径拆成（端点族, 子路径, 是否已知）。
//
// 返回的子路径带前导 `/`（族根本身返回空串），供调用方拼在配置基址之后：
// 基址 `v1/videos` + 子路径 `/abc/content` = `v1/videos/abc/content`。
func Lookup(path string) (Family, string, bool) {
	trimmed := strings.Trim(path, "/")
	for _, route := range routes {
		if trimmed == route.Root {
			return route.Family, "", true
		}
		if route.Subpaths && strings.HasPrefix(trimmed, route.Root+"/") {
			return route.Family, "/" + strings.TrimPrefix(trimmed, route.Root+"/"), true
		}
	}
	return "", "", false
}

// Family 返回入站路径所属的端点族；未知路径返回空串。
//
// 「未知」不是错误：AMKR 允许把任意路径透传给上游（如 decide / classify），
// 它们不属于任何一个已登记的族。
func FamilyOf(path string) Family {
	family, _, _ := Lookup(path)
	return family
}

// IsPassthrough 报告该入站路径的请求体是否应原样透传（只替换 model）。
//
// 未知路径**不是**透传：它们沿用「带上 model 就走对话改写」的既有行为，改动它
// 会影响既有部署。
func IsPassthrough(path string) bool {
	family, _, known := Lookup(path)
	return known && passthrough[family]
}

// IsFormUpload 报告该入站路径的规范请求体是否为 multipart/form-data。
//
// 用于 proxy 的默认表单策略：这些端点没有等价的 JSON 形态（语音转写的规范调用就是
// 上传音频文件），拒绝表单等于把它们彻底关掉，因此默认对它们做不透明转发。
func IsFormUpload(path string) bool {
	return formUploads[strings.Trim(path, "/")]
}

// formUploads 由 routes 表派生，避免「加了族但忘了同步表单集合」。
var formUploads = func() map[string]bool {
	out := map[string]bool{}
	for _, route := range routes {
		if route.FormUpload {
			out[route.Root] = true
		}
	}
	return out
}()

// FormUploadRoots 返回规范请求体为表单的入站路径（按分类表顺序，测试与展示用）。
func FormUploadRoots() []string {
	out := make([]string, 0, len(formUploads))
	for _, route := range routes {
		if route.FormUpload {
			out = append(out, route.Root)
		}
	}
	return out
}
