package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/protocol"
	"github.com/Sparrived/auto-model-key-router/internal/proxysupport"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

const geminiMaxEvent = 1 << 20
const geminiMaxUpstreamJSON = 20 << 20
const geminiSSE = "text/event-stream"

func geminiResult(code int) *attemptResult { return jsonResult(code, protocol.GeminiError(code, "")) }

func geminiUpstreamStatus(status int) int {
	switch status {
	case 400, 413, 415, 429, 503, 504:
		return status
	default:
		return http.StatusBadGateway
	}
}

func (h *Handler) geminiAttempt(c *RequestContext, key config.KeyConfig, resp *http.Response, started time.Time, index int) attemptOutcome {
	if resp.StatusCode == http.StatusOK && c.IsStream {
		return h.geminiStreamAttempt(c, key, resp, started, index)
	}
	defer resp.Body.Close()
	content, readErr := readAllLimited(resp.Body, geminiMaxUpstreamJSON)
	duration := elapsedMS(started, c.now())
	if downstreamCanceled(c) {
		h.recordDownstreamCancellation(c, key, &resp.StatusCode, content, duration)
		return attemptOutcome{response: geminiResult(http.StatusBadGateway)}
	}
	if resp.StatusCode != http.StatusOK {
		status := geminiUpstreamStatus(resp.StatusCode)
		result := geminiResult(status)
		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			if value := resp.Header.Get("Retry-After"); safeGeminiRetryAfter(value) {
				result.headers["Retry-After"] = value
			}
		}
		retry := IsRetryableStatus(resp.StatusCode) || resp.StatusCode < 400
		retried := retry && geminiCanRetry(c, index, true)
		h.recordGeminiHTTPError(c, key, resp, content, duration, retried)
		if retry {
			return attemptOutcome{retryErr: result, retrySameKey: true}
		}
		return attemptOutcome{response: result}
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	converted, usage, err := protocol.GeminiResponse(content, c.geminiThoughts, geminiReasoningIncluded(c, key))
	if readErr != nil || mediaType != "application/json" || err != nil {
		status := http.StatusBadGateway
		if upstreamClassify(readErr) == causeTimeout {
			status = http.StatusGatewayTimeout
		}
		return h.geminiBadResponse(c, key, resp, usage, started, index, status, duration)
	}
	h.recordMetric(c, key, MetricRecord{StatusCode: &resp.StatusCode, Usage: usage, DurationMS: duration, FirstTokenMS: duration})
	contextPool := c.pool()
	contextPool.MarkSuccess(c.ModelID, key.Name)
	return attemptOutcome{response: jsonResult(http.StatusOK, converted)}
}

func safeGeminiRetryAfter(value string) bool {
	if n, err := strconv.ParseUint(value, 10, 32); err == nil && strconv.FormatUint(n, 10) == value {
		return true
	}
	date, err := http.ParseTime(value)
	return err == nil && date.UTC().Format(http.TimeFormat) == value
}

func (h *Handler) recordGeminiHTTPError(c *RequestContext, key config.KeyConfig, resp *http.Response, data []byte, duration int64, retried bool) {
	status := resp.StatusCode
	h.recordMetric(c, key, MetricRecord{StatusCode: &status, Usage: h.extractUsage(data), Failed: true, Retried: retried, DurationMS: duration, FirstTokenMS: duration})
	if keypool.InsufficientBalance(status, data) {
		if seconds, _ := c.pool().MarkBalanceExhausted(c.ModelID, key.Name, "上游余额不足"); seconds > 0 {
			return
		}
	}
	if IsRetryableStatus(status) || status < 400 {
		c.pool().MarkFailure(c.ModelID, key.Name, &status, runtime.RetryAfterSeconds(resp.Header.Get("Retry-After"), c.now()))
	}
}

func geminiCanRetry(c *RequestContext, index int, sameKey bool) bool {
	if index+1 >= c.Attempts || !(sameKey || rotatesKeys(c)) || downstreamCanceled(c) {
		return false
	}
	if c.upstreamCalls.hasRemaining() {
		return true
	}
	c.upstreamCalls.take()
	return false
}

func (h *Handler) geminiBadResponse(c *RequestContext, key config.KeyConfig, resp *http.Response, usage *canonical.Value, started time.Time, index, status int, firstMS int64) attemptOutcome {
	sameKey := status != http.StatusGatewayTimeout
	retried := geminiCanRetry(c, index, sameKey)
	canceled := downstreamCanceled(c)
	h.recordMetric(c, key, MetricRecord{StatusCode: &resp.StatusCode, Usage: usage, Failed: true, Retried: retried, DurationMS: elapsedMS(started, c.now()), FirstTokenMS: firstMS})
	if !canceled {
		c.pool().MarkFailure(c.ModelID, key.Name, &status, nil)
	}
	return attemptOutcome{retryErr: geminiResult(status), retrySameKey: sameKey}
}

func geminiReasoningIncluded(c *RequestContext, key config.KeyConfig) bool {
	// Only the documented first-party Chat endpoint establishes this relation.
	upstreamPath := upstreamPathFor(c.Path, c.Payload, false, c.Config.UpstreamRoutesForBaseURL(key.BaseURL))
	u, err := url.Parse(proxysupport.JoinURL(key.BaseURL, upstreamPath))
	return err == nil && u.Scheme == "https" && u.Host == "api.openai.com" && u.User == nil && u.EscapedPath() == "/v1/chat/completions"
}

func (h *Handler) geminiStreamAttempt(c *RequestContext, key config.KeyConfig, resp *http.Response, started time.Time, index int) attemptOutcome {
	typ, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	state := protocol.GeminiStream{IncludeThoughts: c.geminiThoughts, ReasoningIncluded: geminiReasoningIncluded(c, key)}
	life := h.newLifecycle(c, key, resp, "", geminiSSE, started)
	src := &geminiTimedReader{ctx: contextOf(c.Request), body: resp.Body, deadline: started.Add(secondsToDuration(c.Config.StreamFirstByteTimeout)), idle: secondsToDuration(c.Config.StreamIdleTimeout), observe: life.ObserveChunk}
	reader := bufio.NewReaderSize(src, 32<<10)
	var frame *canonical.Value
	ready := false
	if err != nil || typ != geminiSSE || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		err = protocol.ErrGeminiUpstream
	} else {
		for err == nil && !ready {
			if !time.Now().Before(src.deadline) {
				err = runtime.ErrStreamTimeout
				break
			}
			var data []byte
			data, err = readGeminiSSE(reader)
			if err == nil && bytes.Equal(data, []byte("[DONE]")) {
				err = errIncompleteStream
			}
			if err == nil && len(data) > 0 {
				frame, ready, err = state.Update(data)
			}
		}
	}
	if err != nil || downstreamCanceled(c) {
		_ = resp.Body.Close()
		status := http.StatusBadGateway
		if errors.Is(err, runtime.ErrStreamTimeout) || upstreamClassify(err) == causeTimeout {
			status = http.StatusGatewayTimeout
		}
		return h.geminiBadResponse(c, key, resp, state.Usage(), started, index, status, elapsedMS(started, c.now()))
	}
	src.deadline = time.Time{}
	headers := map[string]string{}
	setStreamingHeaders(headers)
	return attemptOutcome{stream: &streamResponse{code: http.StatusOK, headers: headers, mediaType: geminiSSE, lifecycle: life,
		write: func(w http.ResponseWriter) { h.writeGeminiStream(w, c, resp, life, reader, &state, frame) },
	}}
}

func (h *Handler) writeGeminiStream(w http.ResponseWriter, c *RequestContext, resp *http.Response, life *streamLifecycle, reader *bufio.Reader, state *protocol.GeminiStream, frame *canonical.Value) {
	controller := http.NewResponseController(w)
	emit := func(value *canonical.Value) error {
		if downstreamCanceled(c) {
			return contextOf(c.Request).Err()
		}
		if value != nil {
			if _, err := io.WriteString(w, "data: "+canonical.DumpsOrdered(value)+"\n\n"); err != nil {
				return errDownstreamWrite
			}
		}
		if err := controller.Flush(); err != nil {
			return errDownstreamWrite
		}
		return nil
	}
	err := emit(frame)
	for err == nil {
		data, readErr := readGeminiSSE(reader)
		if errors.Is(readErr, io.EOF) || (readErr == nil && bytes.Equal(data, []byte("[DONE]"))) {
			var final *canonical.Value
			final, err = state.Finish()
			if errors.Is(err, protocol.ErrGeminiIncomplete) {
				err = errIncompleteStream
			}
			if err == nil {
				err = emit(final)
			}
			break
		}
		if readErr != nil {
			err = readErr
			break
		}
		if len(data) == 0 {
			continue
		}
		var frame *canonical.Value
		frame, _, err = state.Update(data)
		if err == nil && frame != nil {
			err = emit(frame)
		}
	}
	canceled := downstreamCanceled(c) || errors.Is(err, errDownstreamWrite)
	if err != nil && !canceled {
		status := http.StatusBadGateway
		if errors.Is(err, runtime.ErrStreamTimeout) || upstreamClassify(err) == causeTimeout {
			status = http.StatusGatewayTimeout
		}
		if emitErr := emit(protocol.GeminiError(status, "")); emitErr != nil {
			canceled = true
		}
	}
	life.SetUsage(state.Usage())
	traceStreamEnd(c, err, canceled)
	life.Finish(err != nil || canceled, resp.Body, canceled)
}

// geminiTimedReader closes the upstream body to interrupt a blocked read. Its initial deadline
// applies until an entire valid event is ready, then the normal per-read idle timeout takes over.
type geminiTimedReader struct {
	ctx      context.Context
	body     io.ReadCloser
	deadline time.Time
	idle     time.Duration
	observe  func(int)
}

func (r *geminiTimedReader) Read(p []byte) (int, error) {
	deadline := r.deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(r.idle)
	}
	ctx, cancel := context.WithDeadline(r.ctx, deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		if r.ctx.Err() != nil {
			return 0, r.ctx.Err()
		}
		return 0, runtime.ErrStreamTimeout
	}
	stop := context.AfterFunc(ctx, func() { _ = r.body.Close() })
	n, err := r.body.Read(p)
	stop()
	if r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	if ctx.Err() != nil {
		return 0, runtime.ErrStreamTimeout
	}
	if n > 0 {
		r.observe(n)
	}
	return n, err
}

func readGeminiSSE(r *bufio.Reader) ([]byte, error) {
	var event, line []byte
	size := 0
	for {
		frag, err := r.ReadSlice('\n')
		size += len(frag)
		if size > geminiMaxEvent {
			return nil, protocol.ErrGeminiUpstream
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			line = append(line, frag...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && size > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if len(line) > 0 {
			line = append(line, frag...)
		} else {
			line = frag
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !utf8.Valid(line) {
			return nil, protocol.ErrGeminiUpstream
		}
		if len(line) == 0 {
			return event, nil
		}
		if line[0] != ':' {
			field, data, _ := bytes.Cut(line, []byte(":"))
			switch string(field) {
			case "data":
				if len(event) != 0 {
					event = append(event, '\n')
				}
				event = append(event, bytes.TrimPrefix(data, []byte(" "))...)
			case "id", "retry":
			case "event":
				if string(bytes.TrimSpace(data)) != "message" {
					return nil, protocol.ErrGeminiUpstream
				}
			default:
				return nil, protocol.ErrGeminiUpstream
			}
		}
		line = nil
	}
}
