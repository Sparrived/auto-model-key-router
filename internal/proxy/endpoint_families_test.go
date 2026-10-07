package proxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件从**下游 HTTP 请求**这一端固化各 OpenAI 端点族的透传行为。
//
// internal/proxysupport 的用例只看请求体构造这一层；这里再看一步：请求确实被送到了
// 正确的上游路径，且响应原样回给调用方。两组用例合起来才能说明「这个端点通了」。

// endpointFamilyConfig 是一份覆盖各端点族的配置：每个族一个模型、一个 key。
func endpointFamilyConfig() *config.RouterConfig {
	return &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("chat-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
			testModel("embed-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
			testModel("image-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
			testModel("tts-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
			testModel("stt-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
			testModel("video-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
			testModel("rank-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
		},
	}
}

// TestEndpointFamiliesForwardToTheirOwnPath 逐族验证：请求打到该族的规范上游路径，
// 且请求体只被换了 model。
func TestEndpointFamiliesForwardToTheirOwnPath(t *testing.T) {
	cases := []struct {
		name        string
		inbound     string
		body        string
		upstream    string
		wantBody    string
		wantStatus  int
		upstreamRes string
	}{
		{
			// 语音合成：input 是待朗读文本。若被方言改写，「input」会消失并冒出
			// messages，上游只会报参数错误。
			name:     "语音合成",
			inbound:  "audio/speech",
			body:     `{"model":"tts-model","input":"hello","voice":"alloy","response_format":"mp3"}`,
			upstream: "/v1/audio/speech",
			// 响应是音频字节流，不透传就会被当 JSON 解析失败。
			upstreamRes: "ID3\x04\x00\x00",
			wantBody:    `{"model":"tts-model","input":"hello","voice":"alloy","response_format":"mp3"}`,
			wantStatus:  http.StatusOK,
		},
		{
			name:        "文本嵌入",
			inbound:     "embeddings",
			body:        `{"model":"embed-model","input":["a"],"encoding_format":"float"}`,
			upstream:    "/v1/embeddings",
			upstreamRes: `{"object":"list","data":[]}`,
			wantBody:    `{"model":"embed-model","input":["a"],"encoding_format":"float"}`,
			wantStatus:  http.StatusOK,
		},
		{
			name:        "图像生成",
			inbound:     "images/generations",
			body:        `{"model":"image-model","prompt":"a cat","n":1}`,
			upstream:    "/v1/images/generations",
			upstreamRes: `{"created":1,"data":[]}`,
			wantBody:    `{"model":"image-model","prompt":"a cat","n":1}`,
			wantStatus:  http.StatusOK,
		},
		{
			name:        "重排",
			inbound:     "rerank",
			body:        `{"model":"rank-model","query":"q","documents":["a","b"],"top_n":1}`,
			upstream:    "/v1/rerank",
			upstreamRes: `{"results":[]}`,
			wantBody:    `{"model":"rank-model","query":"q","documents":["a","b"],"top_n":1}`,
			wantStatus:  http.StatusOK,
		},
		{
			// 视频子路径：取内容 / 重制挂在任务路径之后，配置路由（若有）也要接得上。
			name:        "视频子路径",
			inbound:     "videos/vid_1/content",
			body:        `{"model":"video-model"}`,
			upstream:    "/v1/videos/vid_1/content",
			upstreamRes: "video-bytes",
			wantBody:    `{"model":"video-model"}`,
			wantStatus:  http.StatusOK,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			cfg := endpointFamilyConfig()
			env := newTestEnv(t, cfg, Options{})
			step := jsonStep(item.wantStatus, item.upstreamRes)
			step.Headers["content-type"] = "application/octet-stream"
			env.route(item.upstream, step)

			recorder := env.request(http.MethodPost, item.inbound, item.body, nil)
			if recorder.Code != item.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d（body=%s）", recorder.Code, item.wantStatus, recorder.Body.String())
			}
			if recorder.Body.String() != item.upstreamRes {
				t.Errorf("响应体未被原样透传: got=%q want=%q", recorder.Body.String(), item.upstreamRes)
			}
			if len(env.transport.calls) != 1 {
				t.Fatalf("上游调用数 = %d，期望 1（%v）", len(env.transport.calls), describeUpstreams(env.transport.calls))
			}
			call := env.transport.calls[0]
			if call.path != item.upstream {
				t.Errorf("上游路径 = %q，期望 %q", call.path, item.upstream)
			}
			if call.body != item.wantBody {
				t.Errorf("上游请求体 = %s\n期望 %s", call.body, item.wantBody)
			}
		})
	}
}

// TestImagesGenerationDoesNotInjectReasoningEffort 固化一处**有意的行为变更**。
//
// 改动前 AMKR 把 images/generations 当对话路径处理，会给它注入模型级的
// reasoning_effort。图像端点并不认识这个字段，上游可能直接 400；而它又没有任何地方
// 用到（图像端点不做推理强度协商）。透传之后该字段不再出现。
func TestImagesGenerationDoesNotInjectReasoningEffort(t *testing.T) {
	cfg := endpointFamilyConfig()
	cfg.ReasoningEffortByModel = map[string]string{"image-model": "medium", "chat-model": "medium"}
	env := newTestEnv(t, cfg, Options{})
	env.route("/v1/images/generations", jsonStep(200, `{"created":1,"data":[]}`))

	recorder := env.request(http.MethodPost, "images/generations",
		`{"model":"image-model","prompt":"a cat"}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	body := env.transport.calls[0].body
	if strings.Contains(body, "reasoning_effort") {
		t.Errorf("图像端点不应被注入 reasoning_effort: %s", body)
	}
	// 反证：同一份配置下对话端点**仍然**会被注入，说明失效的只是图像这条路径，
	// 而不是整个 reasoning_effort 能力被改坏。
	env.route("/v1/chat/completions", jsonStep(200, `{"choices":[]}`))
	env.request(http.MethodPost, "chat/completions",
		`{"model":"chat-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	chatBody := env.transport.calls[len(env.transport.calls)-1].body
	if !strings.Contains(chatBody, "reasoning_effort") {
		t.Errorf("对话端点仍应被注入 reasoning_effort: %s", chatBody)
	}
}

// TestFormUploadEndpointsPassThroughByDefault 固化：规范形态是表单的端点默认放行。
//
// 语音转写没有等价的 JSON 形态——规范调用就是上传音频文件。默认 415 等于把这个端点
// 从功能上关掉，而用户得到的提示（"请改用 JSON 形式"）对它根本不成立。
func TestFormUploadEndpointsPassThroughByDefault(t *testing.T) {
	cases := []struct {
		inbound  string
		upstream string
	}{
		{"audio/transcriptions", "/v1/audio/transcriptions"},
		{"audio/translations", "/v1/audio/translations"},
		{"images/edits", "/v1/images/edits"},
		{"images/variations", "/v1/images/variations"},
	}
	for _, item := range cases {
		t.Run(item.inbound, func(t *testing.T) {
			env := newTestEnv(t, endpointFamilyConfig(), Options{})
			env.route(item.upstream, jsonStep(200, `{"text":"ok"}`))
			body, contentType := buildMultipart(t, map[string]string{
				"model": "stt-model",
				"file":  "音频字节",
			})
			recorder := env.request(http.MethodPost, item.inbound, body,
				map[string]string{"Content-Type": contentType})
			if recorder.Code != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
			}
			if len(env.transport.calls) != 1 {
				t.Fatalf("上游调用数 = %d，期望 1", len(env.transport.calls))
			}
			call := env.transport.calls[0]
			if call.path != item.upstream {
				t.Errorf("上游路径 = %q，期望 %q", call.path, item.upstream)
			}
			// 表单字节必须原样转发：文件内容对 AMKR 不透明。
			if call.body != body {
				t.Errorf("表单字节被改写:\n got=%q\nwant=%q", call.body, body)
			}
			if got := call.headers["content-type"]; got != contentType {
				t.Errorf("Content-Type 被改写: got=%q want=%q", got, contentType)
			}
		})
	}
}

// TestUnknownPathMultipartIsStillRejected 固化安全边界：不在规范表单名单里的路径仍然
// 明确 415，而不是被"自动放行"悄悄改写。
func TestUnknownPathMultipartIsStillRejected(t *testing.T) {
	env := newTestEnv(t, endpointFamilyConfig(), Options{})
	body, contentType := buildMultipart(t, map[string]string{"model": "chat-model"})
	recorder := env.request(http.MethodPost, "decide", body, map[string]string{"Content-Type": contentType})
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("状态码 = %d，期望 415（body=%s）", recorder.Code, recorder.Body.String())
	}
	if len(env.transport.calls) != 0 {
		t.Fatalf("拒绝时不应触达上游，实得 %v", describeUpstreams(env.transport.calls))
	}
}

// TestVideoRetrievalWithoutModelUsesUnifiedVideoPlan 固化：免 model 的视频轮询请求
// 落到 unified_model.video 上。
//
// 视频族的三个读取端点（轮询任务、取内容、列任务）规范调用都没有请求体，因而没有
// model。若不回落，"创建得了任务、查不了结果"。
//
// 断言落在**上游 base_url 与凭据**上而不是路径上：轮询必须用创建任务那个供应商的
// Key，用错模型就算路径对了也会 404——这才是这条回落真正要保证的事。
func TestVideoRetrievalWithoutModelUsesUnifiedVideoPlan(t *testing.T) {
	cfg := &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("chat-model", []config.KeyConfig{testKey("chat-key", "https://chat.upstream.test")}),
			testModel("video-model", []config.KeyConfig{testKey("video-key", "https://video.upstream.test")}),
		},
		UnifiedModel: &config.UnifiedModelConfig{
			Default: config.RoutePlan{Primary: config.RouteTarget{Model: "chat-model"}},
			Video:   &config.RoutePlan{Primary: config.RouteTarget{Model: "video-model"}},
		},
	}
	env := newTestEnv(t, cfg, Options{})
	env.route("/v1/videos/vid_1", jsonStep(200, `{"id":"vid_1","status":"completed"}`))

	// 没有请求体，也没有 model 字段。
	recorder := env.request(http.MethodGet, "videos/vid_1", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if len(env.transport.calls) != 1 {
		t.Fatalf("上游调用数 = %d，期望 1", len(env.transport.calls))
	}
	call := env.transport.calls[0]
	if call.path != "/v1/videos/vid_1" {
		t.Errorf("上游路径 = %q，期望 /v1/videos/vid_1", call.path)
	}
	if call.headers["authorization"] != "Bearer sk-video-key" {
		t.Errorf("上游凭据 = %q，期望 video 计划的 sk-video-key（用对话模型的凭据查不到任务）",
			call.headers["authorization"])
	}
}

// TestVideoPollingWithoutVideoPlanFallsBackToDefault 固化未配置 video 计划时的行为。
//
// unified-model 的分族回落规则是"该族没配就用 default"，视频族不例外。因此未配置
// unified_model.video 时轮询仍然会发出去（用 default 计划的模型与 Key），由上游给出
// 它自己的错误——比在 AMKR 这里凭空 400「请求体中缺少 model 字段」更容易排查。
// 这条用例把"用错 Key"这个后果显式钉住，提醒用户为视频族配置计划。
func TestVideoPollingWithoutVideoPlanFallsBackToDefault(t *testing.T) {
	cfg := &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("chat-model", []config.KeyConfig{testKey("chat-key", "https://chat.upstream.test")}),
		},
		UnifiedModel: &config.UnifiedModelConfig{
			Default: config.RoutePlan{Primary: config.RouteTarget{Model: "chat-model"}},
		},
	}
	env := newTestEnv(t, cfg, Options{})
	env.route("/v1/videos/vid_1", jsonStep(200, `{}`))

	recorder := env.request(http.MethodGet, "videos/vid_1", "", nil)
	if recorder.Code == http.StatusBadRequest &&
		strings.Contains(recorder.Body.String(), "缺少 model 字段") {
		t.Fatalf("不应报「请求体中缺少 model 字段」: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(env.transport.calls) != 1 {
		t.Fatalf("上游调用数 = %d，期望 1", len(env.transport.calls))
	}
	call := env.transport.calls[0]
	if call.path != "/v1/videos/vid_1" {
		t.Errorf("上游路径 = %q，期望 /v1/videos/vid_1", call.path)
	}
	if call.headers["authorization"] != "Bearer sk-chat-key" {
		t.Errorf("回落应使用 default 计划的凭据，实际 %q", call.headers["authorization"])
	}
}
