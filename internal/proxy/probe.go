package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/proxysupport"
	"github.com/Sparrived/auto-model-key-router/internal/upstream"
)

// probeTimeout 是原生端点能力探测的单次超时（对齐 proxy_support.py:504 的
// `httpx.Timeout(10.0)`：httpx 的单个 float 表示「所有阶段都是这个值」，不设
// 总时长上限）。
const probeTimeout = 10 * time.Second

// probeNativeMessages 探测上游是否支持原生 /v1/messages 端点。
//
// 移植 proxy_support.py:474。返回值是 (是否支持, 原因)：原因进能力缓存，负结果的
// TTL 按原因选 600s 或 60s（keypool/capabilities.go 的 negativeTTL）。
//
// 三条必须对齐的判定：
//   - 只有 404/405/501 是「不支持」，其余状态码（含 401/403/429/5xx）都说明端点
//     存在——上游可能只是拒绝了这次最小请求；
//   - **传输层失败也算「不支持」**（参照实现捕获 httpx.RequestError ⇒ False,
//     "error"），而不是「未知」；这条负结果的 TTL 只有 60s，以免一次临时故障长期
//     禁用端点；
//   - 探测**必然发生**在新 base_url 的首次原生请求上，因此它是一次额外的（计费
//     的）上游调用。这是既定行为，不是缺陷。
func (h *Handler) probeNativeMessages(
	source *RequestContext,
	key config.KeyConfig,
	modelID string,
	routePath string,
) (bool, string) {
	path := routePath
	if path == "" {
		path = upstreamRouteFor(source.Config, key.BaseURL)["anthropic"]
	}
	if path == "" {
		path = config.UpstreamRouteDefaultPath("anthropic")
	}
	body := proxysupport.NativeEndpointProbeBody("anthropic", modelID)
	return h.runProbe(source, key, path, body,
		proxysupport.NativeEndpointProbeHeaders("anthropic", key.APIKey))
}

// probeNativeResponses 探测上游是否支持原生 Responses 端点。
//
// 移植 proxy_support.py:533。与 messages 版本只差请求体与头部（Responses 不需要
// anthropic-version）。
func (h *Handler) probeNativeResponses(
	source *RequestContext,
	key config.KeyConfig,
	modelID string,
	routePath string,
) (bool, string) {
	path := routePath
	if path == "" {
		path = upstreamRouteFor(source.Config, key.BaseURL)["responses"]
	}
	if path == "" {
		path = config.UpstreamRouteDefaultPath("responses")
	}
	body := proxysupport.NativeEndpointProbeBody("responses", modelID)
	return h.runProbe(source, key, path, body,
		proxysupport.NativeEndpointProbeHeaders("responses", key.APIKey))
}

// runProbe 发出一次探测请求并按状态码判定支持情况。
//
// 探测**不**计入上游调用上限：它受 keypool 的能力缓存约束（一个 base_url 一个
// 路由路径最多探一次），因此不存在被无限放大的风险；把它算进上限反而会让「首次
// 请求」在低上限配置下直接失败。
func (h *Handler) runProbe(
	source *RequestContext,
	key config.KeyConfig,
	path string,
	body *canonical.Value,
	headers map[string]string,
) (bool, string) {
	client := upstreamClientOf(source.Runtime)
	if client == nil {
		return false, "error"
	}
	target := proxysupport.JoinURL(key.BaseURL, path)
	ctx, cancel := context.WithTimeout(contextOf(source.Request), probeTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target,
		bytes.NewReader([]byte(canonical.DumpsOrdered(body))))
	if err != nil {
		return false, "error"
	}
	for header, value := range headers {
		request.Header.Set(header, value)
	}
	// 探测走 Do（非流式）：参照实现用 client.post，响应体被整体读走。
	response, err := client.Do(request)
	if err != nil {
		h.logger.Warn("原生端点探测失败，按不支持处理",
			"upstream_url", target, "error", err.Error())
		return false, "error"
	}
	// 探测也是一次真实的上游调用：响应头里的额度信号同样要交给观察点。探测请求量
	// 极小，但它往往是新 base_url 上第一次拿到真实响应的位置，漏掉就等于「首次
	// 请求即额度耗尽」这种情形完全没有 L3 信号。
	h.observeUpstreamQuota(key, response)
	statusCode := response.StatusCode
	drainAndClose(response)

	supported, reason := proxysupport.NativeEndpointSupported(statusCode)
	if !supported {
		h.logger.Warn("原生端点不存在，按不支持处理",
			"upstream_url", target, "status", statusCode)
		return false, reason
	}
	h.logger.Info("原生端点存在", "upstream_url", target, "status", statusCode)
	return true, reason
}

// upstreamRouteFor 是读取 provider 路由的小包装，纯为可读性。
func upstreamRouteFor(cfg *config.RouterConfig, baseURL string) map[string]string {
	if cfg == nil {
		return nil
	}
	return cfg.UpstreamRoutesForBaseURL(baseURL)
}

// upstreamPathFor 是 proxysupport.UpstreamPath 的小包装。
//
// proxysupport.UpstreamPath 的 error 只在路由未配置且模式非法时出现，而本包的
// 调用点拿到的 mode 都由路径推导，因此错误分支实际不可达；为不引入一个新的失败
// 面，这里退回「零值路径」由调用方继续，与参照实现「配置损坏时用默认路径」的
// 宽容一致（config.UpstreamRoutePath 本身就会回落到默认路径）。
func upstreamPathFor(path string, payload *canonical.Value, native bool, routes map[string]string) string {
	result, _ := proxysupport.UpstreamPath(path, payload, native, routes)
	return result
}

// upstreamClassify 把 upstream.Classify 的结果折算成本包的 cause。
func upstreamClassify(err error) cause {
	switch upstream.Classify(err) {
	case upstream.CauseTimeout:
		return causeTimeout
	case upstream.CauseConnection:
		return causeConnection
	case upstream.CauseDNS:
		return causeDNS
	case upstream.CauseTLS:
		return causeTLS
	}
	return causeOther
}

// cause 是本包内部的上游失败类别（镜像 upstream.Cause，避免跨包分支）。
type cause int

const (
	causeOther cause = iota
	causeTimeout
	causeConnection
	causeDNS
	causeTLS
)

// contextOf 返回请求的 context；请求为 nil 时用 Background。
func contextOf(request *http.Request) context.Context {
	if request == nil {
		return context.Background()
	}
	return request.Context()
}

// drainAndClose 读空并关闭响应体。
//
// 存在的理由是连接复用：Go 的 http.Transport 只有读到 EOF 才会把连接放回池子，
// 直接 Close 会丢弃连接。参照实现靠 httpx 的内部缓冲隐式做到同样的事。
func drainAndClose(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}
