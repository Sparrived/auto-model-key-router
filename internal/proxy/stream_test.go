package proxy

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// breakingReader 先产出给定的字节，然后返回一个非 EOF 的错误。
type breakingReader struct {
	data []byte
	done bool
}

func (r *breakingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("上游连接中断")
	}
	r.done = true
	return copy(p, r.data), nil
}

func (r *breakingReader) Close() error { return nil }

// 已开始的流不重放，但必须告诉客户端失败，不能静默变成一个正常 HTTP EOF。
func TestMidStreamErrorEmitsErrorFrame(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", upstreamStep{
		Status:  200,
		Headers: map[string]string{"content-type": "text/event-stream"},
		Reader:  &breakingReader{data: []byte("data: {\"i\":1}\n\n")},
	})

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[],"stream":true}`, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200（响应头已写出，不能再改）", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.HasPrefix(body, "data: {\"i\":1}\n\n") || strings.Count(body, "upstream_stream_error") != 1 {
		t.Fatalf("应保留已到达事件并仅发一次安全错误帧，实得 %q", body)
	}

	records := env.metrics.take()
	if len(records) != 1 {
		t.Fatalf("指标行数: got %d want 1", len(records))
	}
	if !records[0].Failed {
		t.Fatalf("中途错误应记 failed=true，实得 %+v", records[0])
	}
	// 注意不断言 first_token_ms != 0：参照实现的哨兵就是 `if first_token_ms == 0`，
	// 因此一次亚毫秒就结束的流**合法地**记 0（下一个块才会真正写入）。断言
	// duration_ms 非负即可，不把「毫秒精度以下的流」误判成缺陷。
	if records[0].DurationMS < 0 {
		t.Fatalf("duration_ms 不应为负，实得 %d", records[0].DurationMS)
	}
	if got := env.pool.ActiveCount("vendor-model", "k1"); got != 0 {
		t.Fatalf("key 在途计数应归零，实得 %d", got)
	}
	if got := env.resources.ActiveLeases(); got != 0 {
		t.Fatalf("租约应释放，实得 %d", got)
	}
}

// TestMidStreamErrorDoesNotRetryOnAnotherKey 固化「一旦开始写下游就不再换 key 重试」。
//
// 多 key 配置下最容易被误实现的一点：上游中途断了，直觉上「还有别的 key，换一个
// 重试」——但那会把已经发给下游的半条 SSE 流接上另一条流，客户端拿到的是两条流
// 拼接后的垃圾。参照实现不重试。
func TestMidStreamErrorDoesNotRetryOnAnotherKey(t *testing.T) {
	cfg := simpleChatConfig()
	cfg.Models[0].Keys = append(cfg.Models[0].Keys, testKey("k2", "https://upstream.test"))
	cfg.MaxRetries = 1
	env := newTestEnv(t, cfg, Options{BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", upstreamStep{
		Status:  200,
		Headers: map[string]string{"content-type": "text/event-stream"},
		Reader:  &breakingReader{data: []byte("data: {\"i\":1}\n\n")},
	})

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[],"stream":true}`, nil)

	if !strings.HasPrefix(recorder.Body.String(), "data: {\"i\":1}\n\n") || !strings.Contains(recorder.Body.String(), "upstream_stream_error") {
		t.Fatalf("下游字节: %q", recorder.Body.String())
	}
	if len(env.transport.calls) != 1 {
		t.Fatalf("下游已收到字节后不得重试，上游调用数应为 1，实得 %v",
			describeUpstreams(env.transport.calls))
	}
}

// 转换流遇上游错误发入站协议错误，不补成功收尾。
func TestAnthropicStreamMidStreamErrorEmitsErrorFrame(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", upstreamStep{
		Status:  200,
		Headers: map[string]string{"content-type": "text/event-stream"},
		Reader: &breakingReader{data: []byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")},
	})

	recorder := env.request(http.MethodPost, "messages",
		`{"model":"vendor-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
		nil)

	body := recorder.Body.String()
	if !strings.HasPrefix(body, "event: message_start\n") {
		t.Fatalf("应先发出 message_start，实得 %q", body)
	}
	if !strings.Contains(body, "text_delta") {
		t.Fatalf("应转发已收到的文本增量，实得 %q", body)
	}
	for _, forbidden := range []string{"message_stop", "message_delta", "content_block_stop"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("中途错误后不得补 %s，实得 %q", forbidden, body)
		}
	}
	if got := env.resources.ActiveLeases(); got != 0 {
		t.Fatalf("租约应释放，实得 %d", got)
	}
}

// chunkedReader 按给定分片逐次产出，不制造错误。
//
// scriptedTransport 会把 Chunks 拼接成一个 strings.Reader（分片边界因此丢失），
// 需要真正跨块切分的用例得自己给 Reader。
type chunkedReader struct {
	chunks []string
	index  int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	r.index++
	return copy(p, chunk), nil
}

// TestRawStreamExtractsUsageFromSSE 锁定「原样转发流同样要抽取 usage」。
//
// 参照实现的 _stream_upstream 在转发每块之前调用 _stream_usage 并合并进
// lifecycle.usage（proxy_handler.py:1123-1125），与 Anthropic / Responses 两条重建流
// 走的是同一套记账。Go 侧的 rawStream 只做字节转发、完全没解析 usage，于是
// `/v1/chat/completions` 的**流式**请求（它恒走原样转发）token 用量恒为 0。
func TestRawStreamExtractsUsageFromSSE(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", sseStep(
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":22}}\n\n",
		"data: [DONE]\n\n",
	))

	env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[],"stream":true}`,
		map[string]string{"User-Agent": "claude-cli/1.0"})

	records := env.metrics.take()
	if len(records) != 1 {
		t.Fatalf("指标行数: got %d want 1", len(records))
	}
	// 流式路径的收尾走 streamLifecycle.onFinish -> recordMetric，来源字段必须与
	// 非流式一致地填上（漏填会出现"流式请求没有来源"）。
	if records[0].ClientAddr != "192.0.2.1:1234" || records[0].UserAgent != "claude-cli/1.0" {
		t.Fatalf("流式指标行应带来源，实得 %q / %q", records[0].ClientAddr, records[0].UserAgent)
	}
	usage := records[0].Usage
	if usage == nil {
		t.Fatal("原样转发流必须抽取 usage，实得 nil")
	}
	if got := usage.Lookup("prompt_tokens").PyStr(); got != "11" {
		t.Fatalf("prompt_tokens: got %s want 11", got)
	}
	if got := usage.Lookup("completion_tokens").PyStr(); got != "22" {
		t.Fatalf("completion_tokens: got %s want 22", got)
	}
}

// TestRawStreamExtractsUsageAcrossChunks 覆盖 usage 行被 TCP 分片切开的情形。
//
// 断言的是「按行缓冲」而不是「按块解析」：只在本块内找 data: 行会漏掉这条 usage。
func TestRawStreamExtractsUsageAcrossChunks(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/chat/completions", upstreamStep{
		Status:  200,
		Headers: map[string]string{"content-type": "text/event-stream"},
		Reader: &chunkedReader{chunks: []string{
			"data: {\"choices\":[],\"usa",
			"ge\":{\"prompt_tokens\":7,",
			"\"completion_tokens\":8}}\n\n",
		}},
	})

	env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[],"stream":true}`, nil)

	records := env.metrics.take()
	if len(records) != 1 {
		t.Fatalf("指标行数: got %d want 1", len(records))
	}
	usage := records[0].Usage
	if usage == nil {
		t.Fatal("跨块切分的 usage 行必须被拼回，实得 nil")
	}
	if got := usage.Lookup("prompt_tokens").PyStr(); got != "7" {
		t.Fatalf("prompt_tokens: got %s want 7", got)
	}
	if got := usage.Lookup("completion_tokens").PyStr(); got != "8" {
		t.Fatalf("completion_tokens: got %s want 8", got)
	}
}

// 一个协议表覆盖真实客户端需要区分的成功、错误与非完整结束，不增加临时测试文件。
func TestStreamBusinessOutcomes(t *testing.T) {
	text := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	finish := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	done := "data: [DONE]\n\n"
	errFrame := "event: error\ndata: {\"error\":{\"message\":\"private-detail\"},\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n"
	for _, tc := range []struct {
		name, path, body, media string
		native, failed          bool
		want                    string
	}{
		{"empty", "chat/completions", "", "", false, true, "upstream_stream_error"},
		{"heartbeat_malformed", "chat/completions", ": ping\n\nevent: ping\n\ndata: nope\n\n", "", false, true, "upstream_stream_error"},
		{"missing_terminal", "chat/completions", text, "", false, true, "upstream_stream_error"},
		{"done_no_output", "chat/completions", done, "", false, true, "upstream_stream_error"},
		{"normal_finish_usage_done", "chat/completions", text + finish + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" + done, "", false, false, "hi"},
		{"normal_finish_only", "chat/completions", text + finish, "", false, false, "hi"},
		{"raw_error_once", "chat/completions", errFrame + done, "", false, true, "private-detail"},
		{"convert_error", "messages", errFrame + done, "", false, true, "upstream_stream_error"},
		{"convert_multidata", "messages", "data: {\"choices\": [\ndata: {\"delta\":{\"content\":\"hi\"}}]}\n\n" + done, "", false, false, "hi"},
		{"convert_no_repeat_done", "responses", text + done, "", false, false, "hi"},
		{"convert_refusal", "responses", "data: {\"choices\":[{\"delta\":{\"refusal\":\"cannot help\"},\"finish_reason\":\"stop\"}]}\n\n", "", false, false, "cannot help"},
		{"convert_old_tool", "responses", "data: {\"choices\":[{\"delta\":{\"function_call\":{\"name\":\"lookup\",\"arguments\":\"{}\"}},\"finish_reason\":\"function_call\"}]}\n\n", "", false, false, "lookup"},
		{"unsupported_reasoning_conversion", "messages", "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thought\"},\"finish_reason\":\"stop\"}]}\n\n", "", false, true, "upstream_stream_error"},
		{"content_filter", "messages", "data: {\"choices\":[{\"finish_reason\":\"content_filter\"}]}\n\n", "", false, false, "message_stop"},
		{"native_anthropic_tool", "messages", "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"name\":\"lookup\",\"input\":{}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "", true, false, "tool_use"},
		{"native_responses_final_output", "responses", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n", "", true, false, "response.completed"},
		{"native_responses_failed", "responses", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", "", true, true, "response.failed"},
		{"raw_multidata_usage", "chat/completions", text + finish + "data: {\"choices\":[],\ndata: \"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" + done, "", false, false, "hi"},
		{"json_error", "chat/completions", "{\"error\":{\"message\":\"private-detail\"},\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}", "application/json", false, true, "private-detail"},
		{"json_empty", "chat/completions", "", "application/json", false, true, ""},
		{"json_normal_no_terminal", "chat/completions", "{\"choices\":[{\"message\":{\"content\":\"hi\"}}]}", "application/json", false, false, "hi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := simpleChatConfig()
			cfg.Models[0].NativeFirst = tc.native
			env := newTestEnv(t, cfg, Options{})
			path := "/v1/chat/completions"
			if tc.path == "responses" {
				_ = env.pool.UpdateNativeEndpoint("https://upstream.test", tc.native, "v1/responses", "test")
			}
			if tc.native {
				path = "/v1/" + tc.path
				_ = env.pool.UpdateNativeEndpoint("https://upstream.test", true, strings.TrimPrefix(path, "/"), "test")
			}
			media := tc.media
			if media == "" {
				media = "text/event-stream"
			}
			env.route(path, upstreamStep{Status: 200, Headers: map[string]string{"content-type": media}, Reader: &chunkedReader{chunks: []string{tc.body[:len(tc.body)/2], tc.body[len(tc.body)/2:]}}})
			result := env.request(http.MethodPost, tc.path, `{"model":"vendor-model","messages":[{"role":"user","content":"hi"}],"stream":true}`, nil)
			body := result.Body.String()
			records := env.metrics.take()
			if result.Code != 200 || len(records) != 1 || records[0].Failed != tc.failed || records[0].Retried || *records[0].StatusCode != 200 {
				t.Fatalf("status=%d records=%+v body=%s", result.Code, records, body)
			}
			if !strings.Contains(body, tc.want) || len(env.transport.calls) != 1 {
				t.Fatalf("body=%q calls=%d", body, len(env.transport.calls))
			}
			if tc.failed && !tc.native && tc.path != "chat/completions" && (strings.Contains(body, "message_stop") || strings.Contains(body, "response.completed") || strings.Contains(body, "private-detail")) {
				t.Fatalf("转换失败不得成功收尾或泄露上游detail: %s", body)
			}
			if tc.name == "raw_error_once" && (strings.Count(body, "event: error") != 1 || strings.Contains(body, "upstream_stream_error") || strings.Contains(body, "[DONE]")) {
				t.Fatalf("错误必须仅转发一次: %s", body)
			}
			if tc.name == "convert_no_repeat_done" && strings.Count(body, `"delta":"hi"`) != 1 {
				t.Fatalf("DONE不得重复上一delta: %s", body)
			}
			if strings.HasPrefix(media, "application/json") && body != tc.body {
				t.Fatalf("JSON必须保持原始体: %s", body)
			}
			if strings.Contains(tc.body, `"prompt_tokens":3`) && (records[0].Usage.Lookup("prompt_tokens").PyStr() != "3" || records[0].Usage.Lookup("completion_tokens").PyStr() != "1") {
				t.Fatalf("错误事件、多行data和JSON也应保留已知usage: %+v", records[0])
			}
		})
	}
	if checkStreamOutcome("audio/speech", "text/event-stream") || checkStreamOutcome("chat/completions", "application/octet-stream") {
		t.Fatal("非聊天/非SSE不应套用终止判定")
	}
}

type streamHealthProbe struct {
	KeyPool
	failures, successes, releases int
}

func (p *streamHealthProbe) MarkFailure(string, string, *int, *float64) { p.failures++ }
func (p *streamHealthProbe) MarkSuccess(string, string)                 { p.successes++ }
func (p *streamHealthProbe) ReleaseKey(model, key string) {
	p.releases++
	p.KeyPool.ReleaseKey(model, key)
}

type rejectingTerminalWriter struct{ *httptest.ResponseRecorder }

func (w rejectingTerminalWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), "finish_reason") {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(b)
}

func TestTerminalWriteFailureIsNotSuccessOrKeyFailure(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{})
	probe := &streamHealthProbe{KeyPool: env.pool}
	env.resources.KeyPool = probe
	env.route("/v1/chat/completions", sseStep("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n"))
	w := rejectingTerminalWriter{httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, "http://testserver/v1/chat/completions", strings.NewReader(`{"model":"vendor-model","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer local-key")
	env.handler.Handle(w, req, "chat/completions")
	records := env.metrics.take()
	if len(records) != 1 || !records[0].Failed || probe.failures != 0 || probe.successes != 0 || probe.releases != 1 {
		t.Fatalf("records=%+v health=%+v", records, probe)
	}
}

func TestControlledIncompleteDoesNotPunishKey(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{})
	_ = env.pool.UpdateNativeEndpoint("https://upstream.test", true, "v1/responses", "test")
	probe := &streamHealthProbe{KeyPool: env.pool}
	env.resources.KeyPool = probe
	frame := "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"
	env.route("/v1/responses", sseStep(frame))
	result := env.request(http.MethodPost, "responses", `{"model":"vendor-model","input":"hi","stream":true}`, nil)
	records := env.metrics.take()
	if result.Body.String() != frame || len(records) != 1 || !records[0].Failed || probe.failures != 0 || probe.successes != 0 || probe.releases != 1 {
		t.Fatalf("body=%s records=%+v health=%+v", result.Body.String(), records, probe)
	}
}

func TestStreamJSONObservationLimitLeavesKeyNeutral(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{})
	probe := &streamHealthProbe{KeyPool: env.pool}
	env.resources.KeyPool = probe
	body := `{"choices":[{"message":{"content":"` + strings.Repeat("x", 1<<20) + `"}}]}`
	env.route("/v1/chat/completions", upstreamStep{
		Status: 200, Headers: map[string]string{"content-type": "application/json"}, Body: body,
	})
	result := env.request(http.MethodPost, "chat/completions", `{"model":"vendor-model","stream":true}`, nil)
	records := env.metrics.take()
	if result.Body.String() != body || len(records) != 1 || !records[0].Failed || probe.failures != 0 || probe.successes != 0 || probe.releases != 1 || env.resources.ActiveLeases() != 0 {
		t.Fatalf("JSON观察超限应原样透传、健康中性并释放资源: records=%+v health=%+v", records, probe)
	}
}

// 编译期哨兵：确保 io 的引用不被误删（Reader 字段的类型）。
var _ io.Reader = (*breakingReader)(nil)

// 编译期哨兵：确保 httptest 的引用不被误删（本文件只用它的请求构造器）。
var _ = httptest.NewRequest
