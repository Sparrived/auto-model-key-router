package proxy

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件锁定两件事（docs/design/quota-freeze.md 第 1、2、5 节）：
//
//  1. **L3 观察点**：每一次拿到上游响应（非流式、流式、可重试分支、工具过滤重试、
//     能力探测）都要把响应头原样投递给 QuotaObserver —— 它是额度冻结唯一的被动信号源，
//     漏一条分支的现象只是「冻结偶尔不生效」，极难从日志里发现；
//  2. **冻结的对外错误语义**：候选全被冻结时回 503，文案用错误原文（能看出是额度冻结、
//     哪把 Key、什么原因），且不再向上游发注定失败的请求。

// quotaSignalHeader 模拟上游带回窗口用量的额度头。
//
// observer 是 opaque 的：代理层只负责原样投递，解析在 observer 侧完成，因此这里用
// 一个真实形态的头名即可，不依赖任何解析规则。
const quotaSignalHeader = "anthropic-ratelimit-unified-5h-remaining"

// observedQuota 是一次「上游响应 → 观察点」的投递记录。
type observedQuota struct {
	providerID string
	keyName    string
	headers    http.Header
}

// recordingQuotaObserver 是本包 QuotaObserver 的假实现。
type recordingQuotaObserver struct {
	mu    sync.Mutex
	calls []observedQuota
}

// ObserveUpstreamQuota 记录一次投递（拷贝响应头，避免断言时上游已复用 Header）。
func (o *recordingQuotaObserver) ObserveUpstreamQuota(providerID, keyName string, headers http.Header) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, observedQuota{
		providerID: providerID,
		keyName:    keyName,
		headers:    headers.Clone(),
	})
}

// take 返回并清空已记录的投递。
func (o *recordingQuotaObserver) take() []observedQuota {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := o.calls
	o.calls = nil
	return result
}

// frozenQuotaKeys 是 keypool.QuotaFreezes 的最小假实现（proxy 侧只用来把选路推到
// 「候选全被冻结」的失败上；冻结判定的行为由 internal/keypool 的用例覆盖）。
type frozenQuotaKeys map[[2]string]string

// IsFrozen 报告 (provider, key) 是否在冻结表里。
func (f frozenQuotaKeys) IsFrozen(providerID, keyName string) bool {
	_, found := f[[2]string{providerID, keyName}]
	return found
}

// FrozenReason 返回冻结原因。
func (f frozenQuotaKeys) FrozenReason(providerID, keyName string) string {
	return f[[2]string{providerID, keyName}]
}

// quotaObserveConfig 构造「单模型单 key、带 provider 标识」的配置。
//
// provider 必须非空：观察点收到的 providerID 来自 Key.Provider，它决定了 L3 信号
// 落到哪把上游 Key 上，用空串断言等于没断言。
func quotaObserveConfig() *config.RouterConfig {
	key := testKey("k1", "https://upstream.test")
	key.Provider = "cpa-claude"
	return &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("vendor-model", []config.KeyConfig{key}),
		},
	}
}

// assertQuotaObservation 断言一条投递记录的身份与响应头。
func assertQuotaObservation(t *testing.T, call observedQuota, wantProvider, wantKey string) {
	t.Helper()
	if call.providerID != wantProvider || call.keyName != wantKey {
		t.Fatalf("投递身份: got %s/%s want %s/%s",
			call.providerID, call.keyName, wantProvider, wantKey)
	}
	if got := call.headers.Get(quotaSignalHeader); got == "" {
		t.Fatalf("响应头应原样投递，实得 %v", call.headers)
	}
}

// TestQuotaObserverReceivesNonStreamingResponse 固化非流式的成功响应也会投递响应头。
func TestQuotaObserverReceivesNonStreamingResponse(t *testing.T) {
	observer := &recordingQuotaObserver{}
	env := newTestEnv(t, quotaObserveConfig(), Options{QuotaObserver: observer})
	env.route("/v1/chat/completions", upstreamStep{
		Status: 200,
		Headers: map[string]string{
			"content-type":    "application/json",
			quotaSignalHeader: "0",
		},
		Body: `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`,
	})

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200（实得 %s）", recorder.Code, recorder.Body.String())
	}
	calls := observer.take()
	if len(calls) != 1 {
		t.Fatalf("非流式成功响应应投递一次，实得 %d 次", len(calls))
	}
	assertQuotaObservation(t, calls[0], "cpa-claude", "k1")
	if got := calls[0].headers.Get(quotaSignalHeader); got != "0" {
		t.Fatalf("头部取值: got %q want %q", got, "0")
	}
}

// TestQuotaObserverReceivesStreamingResponseHeaders 固化流式响应头同样投递。
//
// 流式的响应体要转发给下游，代理层**不会**读它；响应头是这条路径上唯一的额度信号，
// 漏掉它就等于流式流量永远看不到额度耗尽。
func TestQuotaObserverReceivesStreamingResponseHeaders(t *testing.T) {
	observer := &recordingQuotaObserver{}
	env := newTestEnv(t, quotaObserveConfig(), Options{QuotaObserver: observer})
	env.route("/v1/chat/completions", upstreamStep{
		Status: 200,
		Headers: map[string]string{
			"content-type":    "text/event-stream",
			quotaSignalHeader: "0.0",
		},
		Chunks: []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
			"data: [DONE]\n\n",
		},
	})

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[],"stream":true}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200（实得 %s）", recorder.Code, recorder.Body.String())
	}
	calls := observer.take()
	if len(calls) != 1 {
		t.Fatalf("流式响应应投递一次，实得 %d 次", len(calls))
	}
	assertQuotaObservation(t, calls[0], "cpa-claude", "k1")
	if got := calls[0].headers.Get(quotaSignalHeader); got != "0.0" {
		t.Fatalf("头部取值: got %q want %q", got, "0.0")
	}
}

// TestQuotaObserverReceivesRetryableAttemptHeaders 固化可重试分支上每一次响应的投递。
//
// 单 key + 429 + 重试预算 1：第 0 次的 429 响应头（往往正是额度耗尽的那次）与第 1 次
// 的成功响应头都要投递。只看最终结果的话，最关键的 429 信号就丢了。
func TestQuotaObserverReceivesRetryableAttemptHeaders(t *testing.T) {
	observer := &recordingQuotaObserver{}
	env := newTestEnv(t, quotaObserveConfig(), Options{QuotaObserver: observer})
	env.route("/v1/chat/completions",
		upstreamStep{
			Status: 429,
			Headers: map[string]string{
				"content-type":    "application/json",
				quotaSignalHeader: "0",
			},
			Body: `{"error":{"message":"rate limited"}}`,
		},
		jsonStep(200, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`),
	)

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200（实得 %s）", recorder.Code, recorder.Body.String())
	}
	calls := observer.take()
	if len(calls) != 2 {
		t.Fatalf("两次上游尝试应各投递一次，实得 %d 次", len(calls))
	}
	for index, call := range calls {
		if call.keyName != "k1" || call.providerID != "cpa-claude" {
			t.Fatalf("第 %d 次投递身份: got %s/%s want cpa-claude/k1",
				index+1, call.providerID, call.keyName)
		}
	}
	if got := calls[0].headers.Get(quotaSignalHeader); got != "0" {
		t.Fatalf("429 响应头应被投递（额度耗尽的关键信号），实得 %q", got)
	}
}

// TestQuotaObserverReceivesToolRetryHeaders 固化「400 工具错误 → 过滤工具重试」也是
// 一次独立的上游调用，它的响应头同样要投递。
func TestQuotaObserverReceivesToolRetryHeaders(t *testing.T) {
	observer := &recordingQuotaObserver{}
	env := newTestEnv(t, quotaObserveConfig(), Options{QuotaObserver: observer})
	env.route("/v1/chat/completions",
		upstreamStep{
			Status: 400,
			Headers: map[string]string{
				"content-type":    "application/json",
				quotaSignalHeader: "0",
			},
			Body: `{"error":{"message":"invalid tools schema","param":"tools"}}`,
		},
		jsonStep(200, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`),
	)

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}],`+
			`"tools":[{"type":"function","function":{"name":"f","parameters":{}}}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200（实得 %s）", recorder.Code, recorder.Body.String())
	}
	calls := observer.take()
	if len(calls) != 2 {
		t.Fatalf("工具错误重试的两次上游调用都应投递，实得 %d 次", len(calls))
	}
	if got := calls[0].headers.Get(quotaSignalHeader); got != "0" {
		t.Fatalf("首次 400 的响应头应被投递，实得 %q", got)
	}
}

// TestFrozenCandidatesReturn503 固化冻结的对外错误语义。
//
// 三条断言：状态码 503（调用方确实拿不到这次响应）、文案用错误原文（指出额度冻结 +
// provider/key + 原因，而不是「模型 X 没有可用 key」）、**不再向上游发注定失败的请求**。
func TestFrozenCandidatesReturn503(t *testing.T) {
	reason := "5 小时窗口已用尽（剩余 0%），15:00 后重置"
	env := newTestEnv(t, quotaObserveConfig(), Options{})
	env.pool.SetQuotaFreezes(frozenQuotaKeys{
		{"cpa-claude", "k1"}: reason,
	})

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("全部候选冻结时状态码: got %d want 503（实得 %s）",
			recorder.Code, recorder.Body.String())
	}
	want := `{"error":{"message":"模型 vendor-model 的 Key 因额度冻结停用：cpa-claude/k1：` + reason + `"}}`
	if got := recorder.Body.String(); got != want {
		t.Fatalf("503 文案:\n got=%s\nwant=%s", got, want)
	}
	// 关键：冻结后不再打上游一次注定失败的请求。
	if len(env.transport.calls) != 0 {
		t.Fatalf("冻结后不该再打上游，实际上游调用数 %d", len(env.transport.calls))
	}
	if strings.Contains(recorder.Body.String(), "没有可用 key") {
		t.Fatalf("冻结不应说成「没有可用 key」: %s", recorder.Body.String())
	}
}

// quotaObserveTwoKeyConfig 构造「一个模型两把 key、同供应商」的配置。
//
// 两把 Key 是为了让「点名路径」能被走到：只有部分候选被冻结时模型校验仍通过
// （KeyCount 非 0），selectKey 才会拿被冻结的那把去点名并失败。
func quotaObserveTwoKeyConfig() *config.RouterConfig {
	first := testKey("k1", "https://upstream.test")
	first.Provider = "cpa-claude"
	second := testKey("k2", "https://upstream.test")
	second.Provider = "cpa-claude"
	return &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("vendor-model", []config.KeyConfig{first, second}),
		},
	}
}

// TestFrozenNamedKeyReturns503WithoutUpstreamCall 固化点名路径（`model[key]` 语法）的
// 冻结语义：模型校验阶段能通过（还有另一把可用 Key），但点名的这把被冻结时必须回
// 503 + 冻结原因，而不是 403「无权访问」（那是权限问题的说法）。
func TestFrozenNamedKeyReturns503WithoutUpstreamCall(t *testing.T) {
	reason := "5 小时窗口已用尽（剩余 0%），15:00 后重置"
	env := newTestEnv(t, quotaObserveTwoKeyConfig(), Options{})
	env.pool.SetQuotaFreezes(frozenQuotaKeys{
		{"cpa-claude", "k1"}: reason,
	})

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model[k1]","messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("点名冻结 Key 时状态码: got %d want 503（实得 %s）",
			recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"额度冻结", "cpa-claude/k1", reason} {
		if !strings.Contains(body, want) {
			t.Fatalf("503 文案缺少 %q，实得 %s", want, body)
		}
	}
	if strings.Contains(body, "无权访问") {
		t.Fatalf("冻结不该说成权限问题: %s", body)
	}
	if len(env.transport.calls) != 0 {
		t.Fatalf("点名被冻结的 Key 后不该打上游，实际上游调用数 %d", len(env.transport.calls))
	}

	// 未冻结的那把照旧可用：点名 k2 应正常打到上游。
	env.route("/v1/chat/completions", jsonStep(200,
		`{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	healthy := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model[k2]","messages":[{"role":"user","content":"hi"}]}`, nil)
	if healthy.Code != http.StatusOK {
		t.Fatalf("点名未冻结的 k2 应成功，实得 %d（%s）", healthy.Code, healthy.Body.String())
	}
}

// TestNilQuotaObserverKeepsUpstreamBehavior 固化「observer 为 nil 时行为逐字一致」。
//
// 这是装配契约：未启用该机制（嵌入场景、测试、或配置里关掉）时，代理路径上不该多出
// 任何一次调用或任何一处状态。
func TestNilQuotaObserverKeepsUpstreamBehavior(t *testing.T) {
	env := newTestEnv(t, quotaObserveConfig(), Options{})
	env.route("/v1/chat/completions", jsonStep(200,
		`{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`))

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("未装配 observer 时状态码: got %d want 200（实得 %s）",
			recorder.Code, recorder.Body.String())
	}
	if len(env.transport.calls) != 1 {
		t.Fatalf("未装配 observer 时上游调用数: got %d want 1", len(env.transport.calls))
	}
}
