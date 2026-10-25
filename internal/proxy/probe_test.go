package proxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// nativeFirstConfig 是「单个 key、开启 native_first」的配置。
func nativeFirstConfig() *config.RouterConfig {
	cfg := simpleChatConfig()
	cfg.Models[0].NativeFirst = true
	return cfg
}

// TestNativeProbeCachesPositiveResult 固化能力探测的缓存语义：正结果**永久**缓存，
// 因此第二次请求不再付出额外的（计费的）探测调用。
func TestNativeProbeCachesPositiveResult(t *testing.T) {
	env := newTestEnv(t, nativeFirstConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	// 探测打 /v1/messages（用 401 表示「端点存在」），真实请求也走 /v1/messages。
	env.route("/v1/messages",
		jsonStep(401, `{"error":"unauthorized"}`),
		jsonStep(200, `{"type":"message","content":[]}`))

	body := `{"model":"vendor-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	first := env.request(http.MethodPost, "messages", body, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("第一次状态码: got %d（%s）", first.Code, first.Body.String())
	}
	if len(env.transport.calls) != 2 {
		t.Fatalf("第一次应有「探测 + 真实请求」两次上游调用，实得 %d",
			len(env.transport.calls))
	}

	second := env.request(http.MethodPost, "messages", body, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("第二次状态码: got %d（%s）", second.Code, second.Body.String())
	}
	if len(env.transport.calls) != 3 {
		t.Fatalf("正结果应永久缓存，第二次只多一次上游调用，实得 %d",
			len(env.transport.calls))
	}
	// 第三次、第四次仍不应重新探测。
	for range 3 {
		env.route("/v1/messages", jsonStep(200, `{"type":"message","content":[]}`))
		env.request(http.MethodPost, "messages", body, nil)
	}
	if got := len(env.transport.calls); got != 6 {
		t.Fatalf("正结果永久有效：4 次请求应为 1 次探测 + 4 次真实调用 = 5..6，实得 %d", got)
	}
}

// TestNativeProbe404FallsBackToChat 固化「只有 404/405/501 判不支持」以及回退到
// chat/completions 时请求体与路径都会被改写。
func TestNativeProbe404FallsBackToChat(t *testing.T) {
	env := newTestEnv(t, nativeFirstConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/messages", jsonStep(404, `{"error":"not found"}`))
	env.route("/v1/chat/completions", jsonStep(200, `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))

	recorder := env.request(http.MethodPost, "messages",
		`{"model":"vendor-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d（%s）", recorder.Code, recorder.Body.String())
	}
	if len(env.transport.calls) != 2 {
		t.Fatalf("应有「探测 404 + 回退 chat」两次调用，实得 %v",
			describeUpstreams(env.transport.calls))
	}
	if env.transport.calls[1].path != "/v1/chat/completions" {
		t.Fatalf("回退路径: got %s", env.transport.calls[1].path)
	}
	// 回退后的体必须走 OpenAI 方言的适配路径。注意 `max_tokens` 会**保留**——
	// 实测参照实现在回退体里原样留下 `max_tokens`，只有 messages 会被适配成
	// chat 形态。这里按实测值断言，不按直觉。
	upstreamBody := env.transport.calls[1].body
	if !strings.Contains(upstreamBody, `"messages"`) {
		t.Fatalf("回退体应保留 messages: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"max_tokens":8`) {
		t.Fatalf("回退体应保留 max_tokens（与参照实现实测一致）: %s", upstreamBody)
	}
}

// TestNativeProbeTransportErrorIsUnsupported 固化「传输层失败也算不支持」，且原因
// 记为 error（负结果 TTL 只有 60s，避免一次临时故障长期禁用端点）。
func TestNativeProbeTransportErrorIsUnsupported(t *testing.T) {
	env := newTestEnv(t, nativeFirstConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	// 探测失败（拨号被拒），随后回退到 chat 成功。
	env.route("/v1/messages", upstreamStep{Fail: true})
	env.route("/v1/chat/completions", jsonStep(200, `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))

	recorder := env.request(http.MethodPost, "messages",
		`{"model":"vendor-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d（%s）", recorder.Code, recorder.Body.String())
	}
	supported := env.pool.SupportsNativeEndpoint("https://upstream.test", "v1/messages")
	if supported == nil || *supported {
		t.Fatalf("传输失败应缓存为「不支持」，实得 %v", supported)
	}
	states := env.pool.EndpointCapabilityStates()
	entry := states.Lookup("https://upstream.test|v1/messages")
	if entry == nil {
		t.Fatalf("能力缓存里应有该条目，实得 %s", canonicalText(states))
	}
	if reason := entry.Lookup("reason").StringValue(); reason != "error" {
		t.Fatalf("原因应为 error（对应 60s TTL），实得 %q", reason)
	}
	if ttl := entry.Lookup("ttl_seconds").StringValue(); ttl != "60" {
		t.Fatalf("error 负结果的 TTL 应为 60s，实得 %s", ttl)
	}
}

// TestUnsupportedNativeResponseRewritesCache 固化「已缓存支持、但请求时返回 501」
// 时把缓存改写成不支持并回退 chat。
func TestUnsupportedNativeResponseRewritesCache(t *testing.T) {
	env := newTestEnv(t, nativeFirstConfig(), Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
	env.route("/v1/messages",
		jsonStep(400, `{"type":"message","content":[]}`), // 探测：端点存在
		jsonStep(501, `{"error":"not implemented"}`),     // 真实请求：端点其实不支持
	)
	env.route("/v1/chat/completions", jsonStep(200, `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))

	recorder := env.request(http.MethodPost, "messages",
		`{"model":"vendor-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d（%s）", recorder.Code, recorder.Body.String())
	}
	if len(env.transport.calls) != 3 {
		t.Fatalf("应为「探测 + 原生 501 + 回退 chat」三次调用，实得 %v",
			describeUpstreams(env.transport.calls))
	}
	supported := env.pool.SupportsNativeEndpoint("https://upstream.test", "v1/messages")
	if supported == nil || *supported {
		t.Fatalf("501 之后缓存应被改写成「不支持」，实得 %v", supported)
	}
}

// TestQuotaObserverReceivesProbeResponseHeaders 固化「能力探测也是上游响应」。
//
// 探测发生在新 base_url 的首次原生请求上（用 401 表示端点存在），它拿到的响应头是
// 这条路径上唯一可能带额度信号的位置；漏掉它，一次「首次请求即额度耗尽」的情形就
// 完全看不到 L3 信号。这里断言探测与真实请求各投递一次。
func TestQuotaObserverReceivesProbeResponseHeaders(t *testing.T) {
	observer := &recordingQuotaObserver{}
	cfg := nativeFirstConfig()
	cfg.Models[0].Keys[0].Provider = "cpa-claude"
	env := newTestEnv(t, cfg, Options{
		BodyPolicy: BodyPolicyPython, Multipart: MultipartPython, QuotaObserver: observer})
	env.route("/v1/messages",
		upstreamStep{
			Status: 401,
			Headers: map[string]string{
				"content-type":    "application/json",
				quotaSignalHeader: "1",
			},
			Body: `{"error":"unauthorized"}`,
		},
		jsonStep(200, `{"type":"message","content":[]}`),
	)

	recorder := env.request(http.MethodPost, "messages",
		`{"model":"vendor-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d（%s）", recorder.Code, recorder.Body.String())
	}
	calls := observer.take()
	if len(calls) != 2 {
		t.Fatalf("探测与真实请求应各投递一次，实得 %d 次", len(calls))
	}
	for index, call := range calls {
		if call.providerID != "cpa-claude" || call.keyName != "k1" {
			t.Fatalf("第 %d 次投递身份: got %s/%s want cpa-claude/k1",
				index+1, call.providerID, call.keyName)
		}
	}
	if got := calls[0].headers.Get(quotaSignalHeader); got != "1" {
		t.Fatalf("探测响应头应被投递，实得 %q", got)
	}
}

// canonicalText 把能力状态渲染成可读文本（诊断用）。
func canonicalText(value interface{ StringValue() string }) string {
	return value.StringValue()
}
