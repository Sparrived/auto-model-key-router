package proxy

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// balanceConfig 构造「同一模型两把 key」的配置，用于验证欠费账号被自动限流后流量
// 会不会落到另一把 key 上。
func balanceConfig() *config.RouterConfig {
	return &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("vendor-model", []config.KeyConfig{
				testKey("k1", "https://upstream.test"),
				testKey("k2", "https://upstream.test"),
			}),
		},
		MaxRetries: 1,
	}
}

// capturingLogger 把日志收进缓冲区，便于断言「限流 / 恢复」两类事件真的留痕。
//
// 这两条日志是这项机制唯一的对外观测面（Key 健康状态不通过 /health 或管理 API
// 暴露），因此它们不是附带的调试输出，而是契约的一部分。
func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	buffer := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})), buffer
}

// TestInsufficientBalanceLimitsAccountAndRoutesElsewhere 固化端到端行为：
//
//  1. 上游回「余额不足」时，调用方拿到的就是上游的错误（402 原样透传，不改写成
//     503——这一条刻意不动，见下方说明）；
//  2. 该**账号**随即被自动限流：下一个请求不再落到它头上，改用同模型的另一把 key；
//  3. 限流动作在日志里留痕（含上游原话，供人判断是上游真欠费还是判定误伤）。
//
// 关于「为什么不顺手把 402 加进可重试状态码」：RetryableStatusCodes 是参照实现的
// 既定契约（streaming.py:17），改它同时会改掉备选模型/任务的回落时机。代价只是
// **这一次**请求失败——下一个请求已经绕开这个账号，因此刻意保持在本次改动之外。
func TestInsufficientBalanceLimitsAccountAndRoutesElsewhere(t *testing.T) {
	logger, logs := capturingLogger()
	env := newTestEnv(t, balanceConfig(), Options{Logger: logger})
	env.route("/v1/chat/completions",
		jsonStep(402, `{"error":{"message":"Insufficient Balance","type":"invalid_request_error"}}`),
		jsonStep(200, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`),
	)

	first := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if first.Code != http.StatusPaymentRequired {
		t.Fatalf("首次状态码: got %d want 402（上游错误原样透传）", first.Code)
	}
	if !strings.Contains(first.Body.String(), "Insufficient Balance") {
		t.Fatalf("响应体应保留上游原文，实得 %s", first.Body.String())
	}
	if !env.pool.IsBalanceExhausted("vendor-model", "k1") {
		t.Fatal("402 之后 k1 所属账号应已被自动限流")
	}

	second := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("第二次状态码: got %d want 200（应绕开欠费账号，实得 %s）",
			second.Code, second.Body.String())
	}
	if len(env.transport.calls) != 2 {
		t.Fatalf("上游调用数: got %d want 2", len(env.transport.calls))
	}
	if got := env.transport.calls[1].headers["authorization"]; got != "Bearer sk-k2" {
		t.Fatalf("第二次请求应落到 k2，实得 %q", got)
	}
	if !strings.Contains(logs.String(), "账号余额不足，已自动限流") {
		t.Fatalf("限流动作必须留痕，实得日志:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "Insufficient Balance") {
		t.Fatalf("限流日志应带上游原话（判定误伤时唯一的线索），实得:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "cooldown_seconds=300") {
		t.Fatalf("限流日志应写明窗口长度，实得:\n%s", logs.String())
	}
}

// TestInsufficientBalanceOnStreamingRequestAlsoLimits 固化流式请求走的是同一条回写。
//
// 流式尝试的 >=400 分支与非流式的 bufferedResponse 都汇到 recordUpstreamResponse，
// 这里钉住「汇合点」这个不变量：漏掉任何一条分支的表现都是「欠费账号在流式流量下
// 不被限流」，而流式恰恰是客户端最常走的那条。
func TestInsufficientBalanceOnStreamingRequestAlsoLimits(t *testing.T) {
	logger, logs := capturingLogger()
	env := newTestEnv(t, balanceConfig(), Options{Logger: logger})
	env.route("/v1/chat/completions",
		jsonStep(402, `{"error":{"message":"余额不足，请充值","type":"invalid_request_error"}}`),
		sseStep("data: {\"choices\":[]}\n\n"),
	)

	first := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[],"stream":true}`, nil)
	if first.Code != http.StatusPaymentRequired {
		t.Fatalf("流式首次状态码: got %d want 402（实得 %s）", first.Code, first.Body.String())
	}
	if !env.pool.IsBalanceExhausted("vendor-model", "k1") {
		t.Fatal("流式的 402 也应把该账号限流")
	}

	second := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[],"stream":true}`, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("流式第二次状态码: got %d want 200（实得 %s）", second.Code, second.Body.String())
	}
	if got := env.transport.calls[1].headers["authorization"]; got != "Bearer sk-k2" {
		t.Fatalf("流式第二次请求应落到 k2，实得 %q", got)
	}
	if !strings.Contains(logs.String(), "账号余额不足，已自动限流") {
		t.Fatalf("流式路径同样要留痕，实得:\n%s", logs.String())
	}
}

// TestAllAccountsLimitedReturns503WithoutUpstreamCall 固化「全部账号欠费」时的行为：
//
// 不再向上游发注定失败的请求，而是立刻回 503（文案是既定契约，一字不改），并在日志
// 里说明原因是余额不足——否则读日志的人只能看到「没有可用 key」，查不到该去充值。
func TestAllAccountsLimitedReturns503WithoutUpstreamCall(t *testing.T) {
	logger, logs := capturingLogger()
	cfg := &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("vendor-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
		},
	}
	env := newTestEnv(t, cfg, Options{Logger: logger})
	env.route("/v1/chat/completions",
		jsonStep(402, `{"error":{"message":"Insufficient Balance"}}`))

	first := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if first.Code != http.StatusPaymentRequired {
		t.Fatalf("首次状态码: got %d want 402", first.Code)
	}

	second := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("全部账号欠费时状态码: got %d want 503（实得 %s）",
			second.Code, second.Body.String())
	}
	// 文案是参照实现的对外契约，不能为了塞进「余额不足」就改它。
	want := `{"error":{"message":"模型 vendor-model 没有可用 key"}}`
	if got := second.Body.String(); got != want {
		t.Fatalf("503 文案:\n got=%s\nwant=%s", got, want)
	}
	// 关键：不再向上游发一次注定失败的请求。
	if len(env.transport.calls) != 1 {
		t.Fatalf("限流后不该再打上游，实际上游调用数 %d", len(env.transport.calls))
	}
	if !strings.Contains(logs.String(), "balance_limited=1") {
		t.Fatalf("503 的日志应指出是余额不足导致，实得:\n%s", logs.String())
	}
}

// TestDisabledBalanceCooldownFallsBackToNormalFailure 固化「关掉这项机制 = 回到旧行为」。
//
// 关掉（`key_balance_cooldown_seconds` 取 0）时**不能**因为「认出了欠费」反而比改动前更
// 宽松：一个带欠费文案的 429 本来就该进普通冷却，识别出欠费却把它从失败计数里摘出去，
// 等于悄悄关掉了限流之外的降温路径。这里用 -1 表达关闭档：配置解析会把负数夹到 0，而
// 测试脚手架只在字段为 0 时才补默认值。
func TestDisabledBalanceCooldownFallsBackToNormalFailure(t *testing.T) {
	cfg := balanceConfig()
	cfg.KeyBalanceCooldownSeconds = -1
	env := newTestEnv(t, cfg, Options{})
	env.route("/v1/chat/completions",
		jsonStep(429, `{"error":{"message":"Insufficient Balance","type":"invalid_request_error"}}`))

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码: got %d want 429（实得 %s）", recorder.Code, recorder.Body.String())
	}
	if env.pool.IsBalanceExhausted("vendor-model", "k1") {
		t.Fatal("关闭档不该产生欠费限流状态")
	}
	if !env.pool.IsCoolingDown("vendor-model", "k1") {
		t.Fatal("关闭档应按普通 429 冷却该 key（不能因为识别出欠费就不降温）")
	}
}

// TestRetryableBalanceSignalStillReturnsUpstreamError 固化「自己造成的 503 不许顶掉上游原文」。
//
// 单 key（或候选全被限流）部署 + **可重试**的欠费信号（OpenAI 的 429 `insufficient_quota`、
// Moonshot 的 403 "Insufficient Balance"）会走出一条很容易忽略的链：
//
//	第 0 轮：上游 429 → 记下限流 → 返回可重试错误
//	第 1 轮：候选集合已空（欠费不参与软回退）→ ErrNoUsableKey → 503「模型 X 没有可用 key」
//
// 503 是**我们自己**造出来的，不是上游的事实；它会让调用方与按状态码告警的监控同时失去
// 上游原文与重试语义。因此这一档必须把第 0 轮的上游响应原样交出去。
func TestRetryableBalanceSignalStillReturnsUpstreamError(t *testing.T) {
	logger, logs := capturingLogger()
	cfg := &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("vendor-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
		},
		MaxRetries: 2, // 单 key 时 Attempts = max_retries+1 = 3，足够触发第二轮选路失败
	}
	env := newTestEnv(t, cfg, Options{Logger: logger})
	env.route("/v1/chat/completions", jsonStep(429, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`))

	recorder := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码: got %d want 429（实得 %s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "insufficient_quota") {
		t.Fatalf("响应体应保留上游原文，实得 %s", recorder.Body.String())
	}
	// 关键：重试轮次不再去打一个已经确定欠费的账号。
	if len(env.transport.calls) != 1 {
		t.Fatalf("上游调用数: got %d want 1（重试轮不该再打欠费账号）", len(env.transport.calls))
	}
	if !env.pool.IsBalanceExhausted("vendor-model", "k1") {
		t.Fatal("429 + insufficient_quota 也应把该账号限流")
	}
	// 一次请求只确认一次，窗口不该因为重试预算被翻倍。
	if !strings.Contains(logs.String(), "cooldown_seconds=300") {
		t.Fatalf("单次确认的窗口应是 300 秒，实得日志:\n%s", logs.String())
	}
}

// TestBalanceWindowExpiryLogsRecovery 固化**主要恢复路径**的留痕：窗口到期 → 半开试成功。
//
// 这条路径最容易漏：恢复日志若用「窗口还没到期」当判据，窗口一过就永远为假，于是日志里
// 只剩限流那一条，读的人会以为账号一直没恢复——而自动恢复恰恰发生在这里。
// 窗口用 0.2 秒以便真实地跨过它（此前那条只覆盖了 `model[key]` 在窗口内成功的旁路）。
func TestBalanceWindowExpiryLogsRecovery(t *testing.T) {
	logger, logs := capturingLogger()
	cfg := &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("vendor-model", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
		},
		KeyBalanceCooldownSeconds: 0.2,
	}
	env := newTestEnv(t, cfg, Options{Logger: logger})
	env.route("/v1/chat/completions",
		jsonStep(402, `{"error":{"message":"Insufficient Balance"}}`),
		jsonStep(200, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`),
	)

	first := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if first.Code != http.StatusPaymentRequired {
		t.Fatalf("首次状态码: got %d want 402（实得 %s）", first.Code, first.Body.String())
	}
	if !env.pool.IsBalanceExhausted("vendor-model", "k1") {
		t.Fatal("402 之后应处于限流窗口内")
	}

	time.Sleep(300 * time.Millisecond)

	second := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("窗口到期后状态码: got %d want 200（实得 %s）", second.Code, second.Body.String())
	}
	if !strings.Contains(logs.String(), "账号余额恢复，已重新参与路由") {
		t.Fatalf("窗口到期后半开试成功必须留痕，实得日志:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "attempts=1") {
		t.Fatalf("恢复日志应带上欠费记录的次数，实得:\n%s", logs.String())
	}
	// 状态在成功那一刻就被清掉，不必等下一次窗口过期判断。
	if _, found := env.pool.BalanceState("vendor-model", "k1"); found {
		t.Fatal("成功请求应立刻清空欠费记录")
	}
}

// TestSuccessClearsBalanceLimit 固化「在可用时自动恢复」：
//
// 显式指定 key 的请求（`model[key]`）不受健康判定约束（与既有「显式指定 key 不参与
// 冷却」同源），因此它可以在窗口到期之前就把一个欠费账号试出来；一旦成功，账号立刻
// 回到路由，并且照样留一条恢复日志。
func TestSuccessClearsBalanceLimit(t *testing.T) {
	logger, logs := capturingLogger()
	env := newTestEnv(t, balanceConfig(), Options{Logger: logger})
	env.route("/v1/chat/completions",
		jsonStep(402, `{"error":{"message":"Insufficient Balance"}}`),
		jsonStep(200, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`),
	)

	limited := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if limited.Code != http.StatusPaymentRequired {
		t.Fatalf("首次状态码: got %d want 402", limited.Code)
	}

	// 显式点名 k1：绕过限流判定，用它换一次真实试探。
	recovered := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model[k1]","messages":[{"role":"user","content":"hi"}]}`, nil)
	if recovered.Code != http.StatusOK {
		t.Fatalf("试探状态码: got %d want 200（实得 %s）",
			recovered.Code, recovered.Body.String())
	}
	if env.pool.IsBalanceExhausted("vendor-model", "k1") {
		t.Fatal("成功请求应立刻清空欠费状态")
	}
	if !strings.Contains(logs.String(), "账号余额恢复，已重新参与路由") {
		t.Fatalf("恢复动作必须留痕，实得:\n%s", logs.String())
	}

	// 恢复之后自动选路又能用上 k1（不再被排除）。
	seen := map[string]bool{}
	for range 4 {
		env.request(http.MethodPost, "chat/completions",
			`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	}
	for _, call := range env.transport.calls {
		seen[call.headers["authorization"]] = true
	}
	if !seen["Bearer sk-k1"] {
		t.Fatalf("恢复后 k1 应重新参与路由，实际用到的 key: %v", seen)
	}
}
