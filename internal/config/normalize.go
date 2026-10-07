package config

import (
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// CONFIG_VERSION 是当前配置文件版本。
const CONFIG_VERSION = 4

// UNIFIED_MODEL_ID 是 unified_model 伪模型的保留名称。
const UNIFIED_MODEL_ID = "unified-model"

// AccessKeyPrefix 是访问密钥的前缀。
//
// 与工作空间的两把 key 同一套形状（前缀 + 43 字符 base64url），只换前缀：访问密钥
// 会被到处分发（贴进项目环境变量、交给外部协作者），必须一眼能与「本机主凭据」区分
// 开——它们泄漏的后果差一个数量级。
const AccessKeyPrefix = "amkr_ak_"

// DefaultAccessKeyName 是未指定名称时给访问密钥的兜底名。
const DefaultAccessKeyName = "访问密钥"

// reasoningEfforts 是允许的推理强度取值。
var reasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// taskSamplingParams 是任务可固定的采样参数（含 max_tokens 这个输出上限）。
var taskSamplingParams = []string{
	"temperature", "top_p", "top_k", "frequency_penalty", "presence_penalty", "seed", "stop",
	"max_tokens",
}

// TaskParamKeys 是任务参数的完整白名单（采样参数 + reasoning_effort）。
var TaskParamKeys = append(append([]string{}, taskSamplingParams...), "reasoning_effort")

// upstreamRouteModes 是允许的上游路由模式。
//
// 前 5 个是**对话方言**（openai / anthropic / responses 会在 AMKR 侧做请求体改写）
// 与两条最早支持的独立端点族（images / embeddings）；后 5 个是**透传端点族**：
// 它们的请求体对 AMKR 不透明，只替换 model 后原样转发（见
// proxysupport.IsPassthroughEndpoint），否则 tts 的 `input`、视频的 `prompt` 会被
// 当场改写成 chat 形态。
//
// 顺序即管理界面的展示顺序：先对话，再按「图像 → 嵌入 → 语音 → 视频 → 重排」排。
var upstreamRouteModes = []string{
	"openai", "anthropic", "responses",
	"images", "embeddings",
	"speech", "transcriptions", "translations", "video", "rerank",
}

// upstreamRouteLabels 是模式的可读名称（管理界面用）。
var upstreamRouteLabels = map[string]string{
	"openai":         "OpenAI Chat",
	"anthropic":      "Anthropic Messages",
	"responses":      "OpenAI Responses",
	"images":         "OpenAI Images",
	"embeddings":     "OpenAI Embeddings",
	"speech":         "OpenAI Speech（语音合成）",
	"transcriptions": "OpenAI Transcriptions（语音转写）",
	"translations":   "OpenAI Translations（语音翻译）",
	"video":          "OpenAI Videos（视频生成）",
	"rerank":         "Rerank（重排）",
}

// upstreamRouteDefaultPaths 是各模式的标准相对路径。
//
// 语音三兄弟与视频走的都是 OpenAI 现行路径；重排没有跨家标准路径，`v1/rerank`
// 是社区（Cohere/Jina/vLLM 等）最通用的一种写法，用户可在路由里改。
var upstreamRouteDefaultPaths = map[string]string{
	"openai":         "v1/chat/completions",
	"anthropic":      "v1/messages",
	"responses":      "v1/responses",
	"images":         "v1/images/generations",
	"embeddings":     "v1/embeddings",
	"speech":         "v1/audio/speech",
	"transcriptions": "v1/audio/transcriptions",
	"translations":   "v1/audio/translations",
	"video":          "v1/videos",
	"rerank":         "v1/rerank",
}

// upstreamRouteModeAliases 把用户写法折叠到标准模式名。
var upstreamRouteModeAliases = map[string]string{
	"chat":               "openai",
	"chat_completions":   "openai",
	"chat-completions":   "openai",
	"openai_chat":        "openai",
	"openai-chat":        "openai",
	"messages":           "anthropic",
	"anthropic_messages": "anthropic",
	"anthropic-messages": "anthropic",
	"codex":              "responses",
	"image":              "images",
	"img":                "images",
	"dall-e":             "images",
	"dalle":              "images",
	"images/generations": "images",
	"image_generation":   "images",
	"image-generation":   "images",
	"embedding":          "embeddings",
	"embed":              "embeddings",
	"embeddings":         "embeddings",
	// 语音合成：tts / speech 是同一条端点的两种叫法。
	"tts":            "speech",
	"audio_speech":   "speech",
	"audio-speech":   "speech",
	"audio/speech":   "speech",
	"speech":         "speech",
	"voice":          "speech",
	"text_to_speech": "speech",
	// 语音转写：whisper / stt / asr 都是它。
	"stt":                  "transcriptions",
	"asr":                  "transcriptions",
	"whisper":              "transcriptions",
	"transcribe":           "transcriptions",
	"audio_transcriptions": "transcriptions",
	"audio-transcriptions": "transcriptions",
	"audio/transcriptions": "transcriptions",
	// 语音翻译：与转写同形（multipart 进、JSON 出），但是独立端点。
	"translation":        "translations",
	"translate":          "translations",
	"audio_translations": "translations",
	"audio-translations": "translations",
	"audio/translations": "translations",
	// 视频生成。
	"videos":             "video",
	"sora":               "video",
	"video_generation":   "video",
	"video-generation":   "video",
	"video/generations":  "video",
	"videos/generations": "video",
	// 重排。
	"reranking": "rerank",
	"rerank_v1": "rerank",
	"rerank/v1": "rerank",
}

// UpstreamRouteModes 返回允许的路由模式列表（只读副本）。
func UpstreamRouteModes() []string {
	out := make([]string, len(upstreamRouteModes))
	copy(out, upstreamRouteModes)
	return out
}

// UpstreamRouteDefaultPath 返回某模式的默认路径。
func UpstreamRouteDefaultPath(mode string) string { return upstreamRouteDefaultPaths[mode] }

// NormalizeUpstreamRouteMode 规范化路由模式名。
//
// 对齐 config.py:93：先小写去空白，再查别名表，最后校验。
func NormalizeUpstreamRouteMode(value *canonical.Value) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value.StringValue()))
	if alias, ok := upstreamRouteModeAliases[mode]; ok {
		mode = alias
	}
	for _, allowed := range upstreamRouteModes {
		if mode == allowed {
			return mode, nil
		}
	}
	// 模式清单从 upstreamRouteModes 现算：手写一遍枚举文案的写法在新增端点族时
	// 必然漂移（用户看到的可选值与实际接受的集合不一致），而这条错误是运维唯一
	// 的线索。
	return "", errf("upstream_routes 模式必须是 %s", joinModeChoices())
}

// joinModeChoices 把模式清单渲染成「a、b 或 c」。
func joinModeChoices() string {
	if len(upstreamRouteModes) == 0 {
		return ""
	}
	if len(upstreamRouteModes) == 1 {
		return upstreamRouteModes[0]
	}
	return strings.Join(upstreamRouteModes[:len(upstreamRouteModes)-1], "、") +
		" 或 " + upstreamRouteModes[len(upstreamRouteModes)-1]
}

// NormalizeUpstreamRoutePath 规范化单个路由路径。
//
// 对齐 config.py:103。注意 `str(value or "")` 的语义：数字 0、布尔 false、
// 空容器都被当作空串，但 True 会渲染成 "True"（Python 的 str(True)）。
func NormalizeUpstreamRoutePath(mode string, value *canonical.Value) (string, error) {
	mode, err := NormalizeUpstreamRouteMode(canonical.NewString(mode))
	if err != nil {
		return "", err
	}
	route := strings.ReplaceAll(strings.TrimSpace(value.StringValue()), "\\", "/")
	if route == "" {
		return "", errf("upstream_routes 路径不能为空")
	}
	if strings.Contains(route, "://") {
		return "", errf("upstream_routes 只能配置相对路径或路径前缀")
	}
	if strings.ContainsAny(route, "?#") {
		return "", errf("upstream_routes 不能包含 query string 或 fragment")
	}
	for strings.Contains(route, "//") {
		route = strings.ReplaceAll(route, "//", "/")
	}
	route = strings.Trim(route, "/")
	if route == "" {
		return "", errf("upstream_routes 路径不能为空")
	}

	endpoint := upstreamRouteDefaultPaths[mode]
	if route == endpoint || strings.HasSuffix(route, "/"+endpoint) {
		return route, nil
	}
	if route == "v1" || strings.HasSuffix(route, "/v1") {
		return route + "/" + strings.TrimPrefix(endpoint, "v1/"), nil
	}
	return route + "/" + endpoint, nil
}

// NormalizeUpstreamRoutes 规范化 {模式: 路径} 映射。
//
// 对齐 config.py:125：值为 None 或空白字符串的条目被静默跳过（而不是报错）。
func NormalizeUpstreamRoutes(raw *canonical.Value) (map[string]string, error) {
	if raw == nil || raw.IsNull() {
		return map[string]string{}, nil
	}
	if !raw.IsObject() {
		return nil, errf("upstream_routes 必须是对象")
	}
	routes := map[string]string{}
	for _, rawMode := range raw.Obj.Keys() {
		rawRoute := raw.Lookup(rawMode)
		// 跳过 None 与空白：与 Python 的 `if raw_route is None or not str(raw_route).strip()`
		// 一致。注意这里用 PyStr 而非 StringValue——判断条件只排除 None 与空白，
		// 数字 0、False 等假值会走到下面的路径校验并报错。
		if rawRoute == nil || rawRoute.IsNull() || strings.TrimSpace(rawRoute.PyStr()) == "" {
			continue
		}
		mode, err := NormalizeUpstreamRouteMode(canonical.NewString(rawMode))
		if err != nil {
			return nil, err
		}
		path, err := NormalizeUpstreamRoutePath(mode, rawRoute)
		if err != nil {
			return nil, err
		}
		routes[mode] = path
	}
	return routes, nil
}

// NormalizeTaskParams 校验并规整任务的固定参数。
//
// 对齐 config.py:139。只认白名单里的键：写错的名字（比如 temprature）立即报错，
// 而不是等请求打过来才静默地不生效。
func NormalizeTaskParams(raw *canonical.Value, taskName string) (*canonical.Value, error) {
	if raw == nil || raw.IsNull() {
		return canonical.NewObject(), nil
	}
	if !raw.IsObject() {
		return nil, errf("任务 %s 的 params 必须是对象", taskName)
	}
	result := canonical.NewObject()
	for _, rawKey := range raw.Obj.Keys() {
		value := raw.Lookup(rawKey)
		key := strings.TrimSpace(rawKey)
		if !containsString(TaskParamKeys, key) {
			return nil, errf("任务 %s 的 params 不支持的参数: %s（可用: %s）",
				taskName, key, strings.Join(TaskParamKeys, ", "))
		}
		if value == nil || value.IsNull() {
			continue
		}
		if key == "reasoning_effort" {
			effort := strings.TrimSpace(value.StringValue())
			if effort == "" || effort == "default" || effort == "downstream" {
				continue
			}
			if !containsString(reasoningEfforts, effort) {
				return nil, errf("任务 %s 的 reasoning_effort 必须是 %s",
					taskName, strings.Join(reasoningEfforts, "、"))
			}
			result.SetKey(key, canonical.NewString(effort))
			continue
		}
		if key == "stop" {
			values := []*canonical.Value{value}
			if value.IsArray() {
				values = value.Items()
			}
			stops := make([]string, 0, len(values))
			for _, item := range values {
				// str(item)：None 得到 "None"、False 得到 "False"，均为真值字符串，
				// 因此用 PyStr 而非 StringValue。
				if rendered := item.PyStr(); rendered != "" {
					stops = append(stops, rendered)
				}
			}
			if len(stops) > 0 {
				result.SetKey(key, canonical.NewStringArray(stops))
			}
			continue
		}
		if key == "top_k" || key == "seed" || key == "max_tokens" {
			number, ok := value.AsFloat()
			if !ok {
				return nil, errf("任务 %s 的 %s 必须是整数", taskName, key)
			}
			// int(1.5) 会静默截断成 1，那是在替调用方改参数值，宁可报错。
			if number != float64(int64(number)) {
				return nil, errf("任务 %s 的 %s 必须是整数", taskName, key)
			}
			result.SetKey(key, canonical.NewIntValue(int64(number)))
			continue
		}
		number, ok := value.AsFloat()
		if !ok {
			return nil, errf("任务 %s 的 %s 必须是数字", taskName, key)
		}
		result.SetKey(key, canonical.NewFloat(number))
	}
	return result, nil
}

// NormalizeUpstreamBaseURL 规范化上游 URL。
//
// 对齐 config.py:193：去空白并去掉末尾斜杠。
func NormalizeUpstreamBaseURL(value *canonical.Value) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(value.StringValue()), "/")
	if baseURL == "" {
		return "", errf("upstream_routes 上游 URL 不能为空")
	}
	return baseURL, nil
}

// mergeUpstreamRoutesForURL 把某 URL 的路由并入总表，冲突时报错。
//
// 对齐 config.py:200。冲突检测是必要的：同一上游 URL 的同一模式若有两套路径，
// 实际用哪套取决于遍历顺序，静默覆盖会产生难以排查的行为差异。
func mergeUpstreamRoutesForURL(routesByURL map[string]map[string]string, baseURL string, routes map[string]string) error {
	if len(routes) == 0 {
		return nil
	}
	normalized, err := NormalizeUpstreamBaseURL(canonical.NewString(baseURL))
	if err != nil {
		return err
	}
	target, ok := routesByURL[normalized]
	if !ok {
		target = map[string]string{}
		routesByURL[normalized] = target
	}
	for mode, path := range routes {
		if existing, present := target[mode]; present && existing != path {
			return errf("上游 URL %s 的 %s 路由配置冲突", normalized, mode)
		}
		target[mode] = path
	}
	return nil
}

// NormalizeUpstreamURLRoutes 规范化按上游 URL 分组的路由表。
//
// 对齐 config.py:218：空路由组会被整组丢弃。
func NormalizeUpstreamURLRoutes(raw *canonical.Value) (map[string]map[string]string, error) {
	if raw == nil || raw.IsNull() {
		return map[string]map[string]string{}, nil
	}
	if !raw.IsObject() {
		return nil, errf("upstream_routes 必须是按上游 URL 分组的对象")
	}
	routesByURL := map[string]map[string]string{}
	for _, rawBaseURL := range raw.Obj.Keys() {
		routes, err := NormalizeUpstreamRoutes(raw.Lookup(rawBaseURL))
		if err != nil {
			return nil, err
		}
		if len(routes) == 0 {
			continue
		}
		baseURL, err := NormalizeUpstreamBaseURL(canonical.NewString(rawBaseURL))
		if err != nil {
			return nil, err
		}
		routesByURL[baseURL] = routes
	}
	return routesByURL, nil
}

// UpstreamRoutePath 返回某模式的实际路径，缺省时回落到标准路径。
func UpstreamRoutePath(routes map[string]string, mode string) (string, error) {
	normalized, err := NormalizeUpstreamRouteMode(canonical.NewString(mode))
	if err != nil {
		return "", err
	}
	if path, ok := routes[normalized]; ok && path != "" {
		return path, nil
	}
	return upstreamRouteDefaultPaths[normalized], nil
}

// containsString 报告切片是否包含目标值。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
