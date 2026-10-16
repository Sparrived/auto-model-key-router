package proxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件固化「2xx 空响应（无 choices）」这条产品决策：
//   - 它被按上游故障处理：记失败、告警，并在重试预算内重试（同一目标、同一 key）；
//   - 重试耗尽后返回 502，而不是把空壳继续透传给调用方；
//   - 带 choices 的正常响应不受影响（防误伤）。

// emptyResponseConfig 是「单模型单 key、max_retries=1」的配置：
// 单 key ⇒ attempts = max_retries+1 = 2，恰好覆盖「一次重试」的编排。
func emptyResponseConfig() *config.RouterConfig {
	return &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("vendor-model", []config.KeyConfig{
				testKey("k1", "https://upstream.test"),
			}),
		},
		MaxRetries: 1,
	}
}

// TestEmptyChatCompletionRetriesAndSucceeds 固化：第一次空响应不会透传给调用方，
// AMKR 在同一目标上重试，第二次拿到正常响应——调用方只看到一次成功。
func TestEmptyChatCompletionRetriesAndSucceeds(t *testing.T) {
	logger, logs := capturingLogger()
	env := newTestEnv(t, emptyResponseConfig(), Options{Logger: logger})
	env.route("/v1/chat/completions",
		jsonStep(200, `{"id":"empty"}`),
		jsonStep(200, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`),
	)

	response := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200（重试后应成功，实得 %s）",
			response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"hi"`) {
		t.Fatalf("响应体应是第二次的正常响应，实得 %s", response.Body.String())
	}
	if len(env.transport.calls) != 2 {
		t.Fatalf("上游调用数: got %d want 2（空响应后必须重试一次）",
			len(env.transport.calls))
	}

	records := env.metrics.take()
	if len(records) != 2 {
		t.Fatalf("指标行数: got %d want 2", len(records))
	}
	first := records[0]
	if !first.Failed || !first.Retried {
		t.Fatalf("第一次空响应必须记成「失败 + 已重试」，实得 failed=%v retried=%v",
			first.Failed, first.Retried)
	}
	if first.StatusCode == nil || *first.StatusCode != http.StatusOK {
		t.Fatalf("空响应行的状态码应保留上游的 200（这是留痕的关键），实得 %v",
			first.StatusCode)
	}
	if !first.Usage.IsNull() {
		t.Fatalf("空响应行不应凭空有 usage，实得 %v", first.Usage)
	}
	if records[1].Failed {
		t.Fatalf("第二次正常响应不应记失败，实得 %+v", records[1])
	}
	if !strings.Contains(logs.String(), "上游返回空响应") {
		t.Fatalf("空响应必须留痕（这是它唯一的日志观测面），实得日志:\n%s", logs.String())
	}
}

// TestEmptyChatCompletionExhaustedReturnsBadGateway 固化：重试预算内全部是空响应
// 时，调用方拿到明确的 502，而不是一个「200 但没有 choices」的空壳（空壳会被调用
// 方记成不可重试错误，回复静默消失）。第二步用「200 + 空体」顺带覆盖另一种形态。
func TestEmptyChatCompletionExhaustedReturnsBadGateway(t *testing.T) {
	env := newTestEnv(t, emptyResponseConfig(), Options{})
	env.route("/v1/chat/completions",
		jsonStep(200, `{"id":"empty"}`),
		jsonStep(200, ``),
	)

	response := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("状态码: got %d want 502（空响应不应透传，实得 %s）",
			response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "choices") {
		t.Fatalf("502 文案应说明原因（无 choices），实得 %s", response.Body.String())
	}
	if len(env.transport.calls) != 2 {
		t.Fatalf("上游调用数: got %d want 2（预算耗尽后不再重试）",
			len(env.transport.calls))
	}

	records := env.metrics.take()
	if len(records) != 2 {
		t.Fatalf("指标行数: got %d want 2", len(records))
	}
	if !records[0].Failed || !records[0].Retried {
		t.Fatalf("第一轮应记「失败 + 已重试」，实得 %+v", records[0])
	}
	if !records[1].Failed || records[1].Retried {
		t.Fatalf("最后一轮应记「失败 + 未再重试」，实得 %+v", records[1])
	}
}

// TestChatCompletionWithChoicesPassesThrough 防误伤：带 choices 的正常响应原样透传，
// 不多记行、不重试。
func TestChatCompletionWithChoicesPassesThrough(t *testing.T) {
	env := newTestEnv(t, emptyResponseConfig(), Options{})
	body := `{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"}}]}`
	env.route("/v1/chat/completions", jsonStep(200, body))

	response := env.request(http.MethodPost, "chat/completions",
		`{"model":"vendor-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200，实得 %s", response.Code, response.Body.String())
	}
	if response.Body.String() != body {
		t.Fatalf("正常响应应逐字节透传，实得 %s", response.Body.String())
	}
	if len(env.transport.calls) != 1 {
		t.Fatalf("上游调用数: got %d want 1（正常响应不应重试）", len(env.transport.calls))
	}
	records := env.metrics.take()
	if len(records) != 1 || records[0].Failed {
		t.Fatalf("正常响应应记一行成功，实得 %+v", records)
	}
}
