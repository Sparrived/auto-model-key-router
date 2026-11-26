package proxy

import (
	"context"
	"errors"
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/auth"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/routetrace"
)

const traceHeader = "X-AMKR-Request-ID"

type routeTraceContextKey struct{}

type routeTraceWriter struct {
	http.ResponseWriter
	collector        *routetrace.Collector
	status           int
	writeFailed      bool
	returned         bool
	attemptNo        int
	lastSend         *routetrace.Final
	upstreamProtocol string
	final            *routetrace.Final
	streamEnd        string
	request          routetrace.Request
	// 只有已认证的宿主会话在配置外拥有凭据，供本次请求额外排除，不进入 collector。
	credential string
}

// HandleHTTP 接入 HTTP 追踪。WebSocket 的帧转发继续调用 Handle，不产生 HTTP 请求 ID。
func (h *Handler) HandleHTTP(w http.ResponseWriter, r *http.Request, path string) {
	h.handle(w, r, path, true)
}

func wrapRouteTrace(w http.ResponseWriter, r *http.Request) (*routeTraceWriter, *http.Request) {
	tw := &routeTraceWriter{ResponseWriter: w}
	return tw, r.WithContext(context.WithValue(r.Context(), routeTraceContextKey{}, tw))
}

func markTraceReturned(r *http.Request) {
	if trace := activeRouteTrace(r); trace != nil {
		trace.returned = true
	}
}

func (w *routeTraceWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *routeTraceWriter) WriteHeader(status int) {
	if w.collector != nil {
		w.Header().Set(traceHeader, w.collector.ID())
	}
	if w.status == 0 && (status >= 200 || status == 101) {
		w.status = status
		if w.collector != nil {
			w.collector.SetHTTPStatus(status)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *routeTraceWriter) Write(p []byte) (int, error) {
	if w.collector != nil && w.status == 0 {
		w.Header().Set(traceHeader, w.collector.ID())
		w.status = http.StatusOK
		w.collector.SetHTTPStatus(w.status)
	}
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		w.writeFailed = true
	}
	return n, err
}

func (w *routeTraceWriter) Flush() { _ = w.FlushError() }
func (w *routeTraceWriter) FlushError() error {
	if w.collector != nil && w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		w.writeFailed = true
	}
	return err
}

func (w *routeTraceWriter) finish(r *http.Request) {
	if w.collector == nil {
		return
	}
	final := routetrace.Final{}
	if w.final != nil {
		final = *w.final
	}
	if w.status != 0 {
		final.HTTPStatus = tracePtr(w.status)
	}
	end := w.streamEnd
	if end == "" {
		end = "not_stream"
	}
	outcome := "failed"
	switch {
	case !w.returned:
		if w.streamEnd == "" && w.request.Stream != nil && *w.request.Stream {
			end = "incomplete"
		}
	case w.writeFailed || end == "client_canceled" || (r.Context().Err() != nil && end != "complete"):
		outcome = "canceled"
		if w.request.Stream != nil && *w.request.Stream {
			end = "client_canceled"
		}
	case w.status >= 200 && w.status < 400 && (end == "complete" || end == "not_stream"):
		outcome = "success"
	}
	final.StreamEnd = tracePtr(end)
	w.collector.Add(routetrace.Event{Kind: "finish", HTTPStatus: final.HTTPStatus, StreamEnd: final.StreamEnd})
	w.collector.Finish(outcome, final)
}

func activeRouteTrace(r *http.Request) *routeTraceWriter {
	if r == nil {
		return nil
	}
	w, _ := r.Context().Value(routeTraceContextKey{}).(*routeTraceWriter)
	if w == nil || w.collector == nil {
		return nil
	}
	return w
}

func (h *Handler) beginRouteTrace(r *http.Request, authHeaders http.Header, cfg *config.RouterConfig, authz *AuthorizerResult, format string) *routeTraceWriter {
	if h.routeTraces == nil || !cfg.RouteTrace.Enabled {
		return nil
	}
	w, ok := r.Context().Value(routeTraceContextKey{}).(*routeTraceWriter)
	if !ok {
		return nil
	}
	credential := auth.RequestAPIKey(authHeaders)
	var subject routetrace.Subject
	switch {
	case authz.AccessKey != nil:
		subject = routetrace.Subject{Type: routetrace.OwnerAccessKey, Credential: credential}
	case authz.Workspace != "":
		subject = routetrace.Subject{Type: routetrace.OwnerWorkspaceInference, Credential: credential}
	case (h.authorizer == nil && cfg.LocalAPIKey != "") || authz.TraceFull:
		subject = routetrace.Subject{Type: routetrace.OwnerFull}
	default:
		return nil
	}
	w.collector = h.routeTraces.Begin(subject, cfg.RouteTrace.RetentionHours)
	if w.collector == nil {
		return nil
	}
	w.credential = credential
	w.event(routetrace.Event{Kind: "resolve", Stage: tracePtr("request"), Protocol: traceProtocolName(format)})
	return w
}

func traceProtocolName(format string) *string {
	switch format {
	case "gemini/generateContent", "gemini/streamGenerateContent", "chat/completions", "messages", "responses", "messages/count_tokens", "embeddings", "images/generations", "images/edits", "audio/speech", "audio/transcriptions", "audio/translations", "videos", "rerank":
		return tracePtr(format)
	}
	return nil
}

func tracePtr[T any](v T) *T { return &v }

func (w *routeTraceWriter) event(event routetrace.Event) {
	if w != nil {
		w.collector.Add(event)
	}
}

func (w *routeTraceWriter) safeLabel(cfg *config.RouterConfig, value string) *string {
	if routetrace.Label(value, w.credential) == nil {
		return nil
	}
	return routetrace.ConfigLabel(cfg, value)
}

func (w *routeTraceWriter) target(cfg *config.RouterConfig, model, provider, key, upstream string) *routetrace.Target {
	return &routetrace.Target{ModelID: w.safeLabel(cfg, model), ProviderID: w.safeLabel(cfg, provider), KeyName: w.safeLabel(cfg, key), UpstreamModelID: w.safeLabel(cfg, upstream)}
}

func (w *routeTraceWriter) preflight(reason string) {
	w.event(routetrace.Event{Kind: "resolve", Stage: tracePtr("prepare"), Reason: &reason})
}

func (w *routeTraceWriter) setStream(stream bool) {
	if w != nil {
		w.request.Stream = &stream
		w.collector.SetRequest(w.request)
	}
}

func (w *routeTraceWriter) resolve(cfg *config.RouterConfig, workspace, name, model, key string, kind, reason string) {
	if w == nil {
		return
	}
	if workspace == config.DefaultWorkspace {
		w.request.Workspace = w.safeLabel(cfg, workspace)
	} else {
		for _, known := range cfg.WorkspaceNames() {
			if known == workspace {
				w.request.Workspace = w.safeLabel(cfg, known)
				break
			}
		}
	}
	if kind != "" {
		w.request.RouteKind = &kind
	}
	w.request.Model = w.safeLabel(cfg, name)
	w.collector.SetRequest(w.request)
	event := routetrace.Event{Kind: "resolve", Reason: tracePtr(reason)}
	if model != "" {
		// 点名 Key 是来访输入，只有真正命中配置后才可进入日志。
		var configuredKey string
		for _, route := range cfg.Models {
			if route.ID == model {
				for _, target := range route.Keys {
					if target.Name == key {
						configuredKey = target.Name
						break
					}
				}
				break
			}
		}
		event.Target = w.target(cfg, model, "", configuredKey, "")
	}
	w.event(event)
}

// traceRouteName 检查本代配置的入口名，而不是用改发目标的 ID 判断是否是别名。
func traceRouteName(cfg *config.RouterConfig, requested string, task bool) (name, kind string) {
	switch {
	case task:
		return requested, "task"
	case requested == config.UNIFIED_MODEL_ID && cfg.UnifiedModel != nil:
		return config.UNIFIED_MODEL_ID, "unified"
	}
	for _, model := range cfg.Models {
		if model.ID == requested {
			return model.ID, "direct"
		}
		for _, alias := range model.Aliases {
			if alias == requested {
				return alias, "alias"
			}
		}
	}
	return "", ""
}

func (w *routeTraceWriter) decisions(cfg *config.RouterConfig, model, stage string, d *keypool.Decisions) {
	for _, item := range d.Items[:d.Count] {
		event := routetrace.Event{Kind: item.Kind, Reason: tracePtr(item.Reason), Stage: tracePtr(stage),
			Target: w.target(cfg, model, item.Provider, item.KeyName, item.UpstreamModel)}
		if item.Mode != "" {
			event.Mode = tracePtr(item.Mode)
		}
		if item.Phase != "" {
			event.Stage = tracePtr(stage + "/" + item.Phase)
		}
		w.event(event)
	}
	w.collector.Omit(d.Omitted)
}

func traceCount(tr *routeTraceWriter, pool KeyPool, cfg *config.RouterConfig, model string, access *config.AccessKeyConfig, stage string) int {
	if tr != nil {
		if real, ok := pool.(*keypool.KeyPool); ok {
			var decisions keypool.Decisions
			count := real.CountWithDecisions(model, access, &decisions)
			tr.decisions(cfg, model, stage, &decisions)
			return count
		}
	}
	return pool.KeyCountFor(model, access)
}

func traceStage(c *RequestContext) string {
	if c.FallbackTarget != "" {
		return "fallback"
	}
	return "primary"
}

func traceKeyTarget(w *routeTraceWriter, c *RequestContext, key config.KeyConfig) *routetrace.Target {
	name, provider, upstream := key.Name, key.Provider, upstreamModelOr(c.ModelID, key.UpstreamModel)
	if name == key.APIKey {
		name = ""
	}
	if provider == key.APIKey {
		provider = ""
	}
	if upstream == key.APIKey {
		upstream = ""
	}
	return w.target(c.Config, c.ModelID, provider, name, upstream)
}

func traceStreamEnd(c *RequestContext, err error, canceled bool) {
	if w := activeRouteTrace(c.Request); w != nil {
		switch {
		case err == nil && !canceled:
			w.streamEnd = "complete"
		case canceled || errors.Is(err, errDownstreamWrite):
			w.streamEnd = "client_canceled"
		case errors.Is(err, errIncompleteStream) || errors.Is(err, errEmptyStream):
			w.streamEnd = "incomplete"
		default:
			w.streamEnd = "upstream_error"
		}
	}
}

func traceNamedKey(w *routeTraceWriter, c *RequestContext, pool KeyPool, name string) (config.KeyConfig, error) {
	if w != nil {
		if real, ok := pool.(*keypool.KeyPool); ok {
			var decisions keypool.Decisions
			key, err := real.NamedWithDecisions(c.ModelID, name, c.AccessKey, &decisions)
			w.decisions(c.Config, c.ModelID, traceStage(c)+"/specified", &decisions)
			return key, err
		}
	}
	return pool.KeyByName(c.ModelID, name, c.AccessKey)
}

func traceSpecified(w *routeTraceWriter, c *RequestContext, key config.KeyConfig, err error) {
	if w == nil {
		return
	}
	reason := "selected"
	switch {
	case errors.Is(err, keypool.ErrConcurrencyLimited):
		reason = "concurrency_limited"
	case errors.Is(err, keypool.ErrQuotaFrozen):
		reason = "quota_frozen"
	case err != nil:
		reason = "unavailable"
	}
	w.event(routetrace.Event{Kind: "selection", Stage: tracePtr(traceStage(c)), Mode: tracePtr("specified"), Reason: &reason, Target: traceKeyTarget(w, c, key)})
}

func traceNextKey(w *routeTraceWriter, c *RequestContext, pool KeyPool, excluded []string, affinity string) (config.KeyConfig, error) {
	if w != nil {
		if real, ok := pool.(*keypool.KeyPool); ok {
			var decisions keypool.Decisions
			key, err := real.NextWithDecisions(c.ModelID, excluded, c.AccessKey, affinity, &decisions)
			w.decisions(c.Config, c.ModelID, traceStage(c), &decisions)
			return key, err
		}
	}
	return pool.NextKey(c.ModelID, excluded, c.AccessKey, affinity)
}

func (w *routeTraceWriter) beginSend(c *RequestContext, key config.KeyConfig, native bool) {
	w.attemptNo++
	// 保存这次发送的独立元数据；retry 或备选决定最终写回哪一个 attempt 后才绑定它。
	w.lastSend = &routetrace.Final{Target: traceKeyTarget(w, c, key)}
	w.upstreamProtocol = "openai"
	if native && c.Path == "messages" {
		w.upstreamProtocol = "anthropic"
	}
	if native && c.Path == "responses" {
		w.upstreamProtocol = "responses"
	}
	if !native && traceProtocolName(c.Path) != nil && c.Path != "messages" && c.Path != "responses" && c.Path != "chat/completions" {
		w.upstreamProtocol = c.Path
	}
	w.collector.Add(w.attemptEvent(c))
}

func (w *routeTraceWriter) attemptEvent(c *RequestContext) routetrace.Event {
	return routetrace.Event{Kind: "attempt", Stage: tracePtr(traceStage(c)), AttemptNo: tracePtr(w.attemptNo), Sent: tracePtr(true),
		Protocol: tracePtr(w.upstreamProtocol), Target: w.lastSend.Target, UpstreamStatus: w.lastSend.UpstreamStatus}
}

func (w *routeTraceWriter) received(c *RequestContext, status int) {
	w.lastSend.UpstreamStatus = tracePtr(status)
	_ = w.collector.UpdateAttempt(w.attemptEvent(c))
}

func (w *routeTraceWriter) recordAttempt(c *RequestContext, r MetricRecord) {
	if w.lastSend == nil {
		return
	}
	result, reason := "response", "response"
	switch {
	case downstreamCanceled(c):
		reason = "client_canceled"
	case r.StatusCode == nil:
		result, reason = "transport_error", "transport_error"
	case IsRetryableStatus(*r.StatusCode):
		reason = "retryable_http"
	case r.Failed && w.streamEnd != "":
		reason = w.streamEnd
	case r.Failed:
		reason = "upstream_error"
	}
	if r.StatusCode != nil {
		w.lastSend.UpstreamStatus = tracePtr(*r.StatusCode)
	}
	event := w.attemptEvent(c)
	event.Reason, event.Result, event.DurationMS = &reason, &result, tracePtr(r.DurationMS)
	_ = w.collector.UpdateAttempt(event)
}

type tracedAttempt struct {
	attempt
	trace *routeTraceWriter
	final *routetrace.Final
}

func (w *routeTraceWriter) bind(a attempt) attempt {
	if a == nil || w.lastSend == nil || w.lastSend.UpstreamStatus == nil || a.statusCode() != *w.lastSend.UpstreamStatus {
		return a
	}
	return &tracedAttempt{attempt: a, trace: w, final: w.lastSend}
}

func (a *tracedAttempt) writeTo(w http.ResponseWriter) {
	a.trace.final = a.final
	a.attempt.writeTo(w)
}

func traceProbe(c *RequestContext, key config.KeyConfig, reason string, status *int) {
	if w := activeRouteTrace(c.Request); w != nil {
		switch reason {
		case "supported", "unsupported", "error", "not_sent":
		default:
			reason = "error"
		}
		w.event(routetrace.Event{Kind: "probe", Stage: tracePtr(traceStage(c)), Reason: &reason,
			Target: traceKeyTarget(w, c, key), UpstreamStatus: status})
	}
}
