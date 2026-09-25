package proxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件覆盖结构化决策端点（Laya / Jev 的 /v1/decide 与 /v1/classify）的端到端行为。
//
// 为什么单测不够：proxysupport 的用例只证明"解析出了默认模型名"，而这条链路真正容易
// 出错的地方在下游两处——上游路径是不是 /v1/decide（而不是被当成对话端点），以及请求体
// 有没有被改写（这类调用不带 model，上游也不该收到一个凭空出现的 model 字段）。这两件事
// 只有把请求真的打到假上游上才看得见。

// decisionEndpointConfig 是一条 laya 路由 + 一个普通模型，用来验证两条分支不会串。
func decisionEndpointConfig() *config.RouterConfig {
	return &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel("laya", []config.KeyConfig{testKey("k1", "https://upstream.test")}),
			testModel("vendor-model", []config.KeyConfig{testKey("k2", "https://upstream.test")}),
		},
	}
}

// TestDecisionEndpointWithoutModelIsForwardedVerbatim 固化免 model 调用的透传语义。
//
// 这是这两个端点的**规范调用形态**：不带 model，用哪家模型由上游按 Key 决定。AMKR 用
// 默认模型名选 Key，但请求体必须逐字节原样转发——一旦顺手把 model 补进体里，上游看到的
// 就是它没写过的字段。
func TestDecisionEndpointWithoutModelIsForwardedVerbatim(t *testing.T) {
	for _, path := range []string{"decide", "classify"} {
		env := newTestEnv(t, decisionEndpointConfig(), Options{})
		env.route("/v1/"+path, jsonStep(200, `{"ok":true}`))

		recorder := env.request(http.MethodPost, path, `{"input":"x"}`, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("[%s] 状态码 = %d，期望 200（body=%s）", path, recorder.Code, recorder.Body.String())
		}
		if len(env.transport.calls) != 1 {
			t.Fatalf("[%s] 上游调用 = %v，期望恰好一次", path, describeUpstreams(env.transport.calls))
		}
		call := env.transport.calls[0]
		// 上游路径：这两个路径在 UpstreamMode 里没有方言，按 "v1/" + path 拼接。
		if call.path != "/v1/"+path {
			t.Errorf("[%s] 上游路径 = %s，期望 /v1/%s", path, call.path, path)
		}
		// 请求体：载荷没有 model 时 UpstreamBody 原样返回入参体（support.go:407），
		// "透传"二字的全部含义就在这一条断言里。
		if call.body != `{"input":"x"}` {
			t.Errorf("[%s] 上游请求体 = %s，期望逐字节原样 %s", path, call.body, `{"input":"x"}`)
		}
	}
}

// TestDecisionEndpointWithModelKeepsRoutingByThatModel 固化"带了 model 就按它走"。
//
// 默认模型名只在**没有**可用 model 时兜底，不能让一个显式指定了模型的请求被改道到
// laya 上——那会让同一个端点在有/无 model 时路由到两套 Key 而不自知。
func TestDecisionEndpointWithModelKeepsRoutingByThatModel(t *testing.T) {
	env := newTestEnv(t, decisionEndpointConfig(), Options{})
	env.route("/v1/decide", jsonStep(200, `{"ok":true}`))

	recorder := env.request(http.MethodPost, "decide", `{"model":"vendor-model","input":"y"}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if len(env.transport.calls) != 1 {
		t.Fatalf("上游调用 = %v，期望恰好一次", describeUpstreams(env.transport.calls))
	}
	if got := env.transport.calls[0].body; !strings.Contains(got, `"model":"vendor-model"`) {
		t.Errorf("上游请求体 = %s，期望带上 vendor-model", got)
	}
}

// TestDecisionEndpointWithoutDefaultModelIsNotFound 固化没配默认路由时的失败面。
//
// 400「请求体中缺少 model 字段」会让调用方以为自己的请求写错了（按这两个端点的规范，
// 不写 model 才是对的）；404「模型 laya 未配置」才指向真正要做的事：建一条叫 laya 的路由
// 并把这些 Key 绑上去。这条差异是 Go 侧新增行为的一部分，与解析逻辑一起锁住。
func TestDecisionEndpointWithoutDefaultModelIsNotFound(t *testing.T) {
	env := newTestEnv(t, simpleChatConfig(), Options{}) // 只有 vendor-model，没有 laya
	recorder := env.request(http.MethodPost, "decide", `{}`, nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404（body=%s）", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "laya") {
		t.Errorf("404 的文案应指出缺的是哪个模型，实际 %s", body)
	}
	if len(env.transport.calls) != 0 {
		t.Errorf("不该打到上游: %v", describeUpstreams(env.transport.calls))
	}
}
