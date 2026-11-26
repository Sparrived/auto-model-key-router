package proxy

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/endpoint"
	"github.com/Sparrived/auto-model-key-router/internal/protocol"
)

func geminiAuthRequest(request *http.Request) (*http.Request, bool) {
	clone := request.Clone(request.Context())
	var source, value string
	for k, values := range clone.Header {
		name := strings.ToLower(k)
		switch name {
		case "x-api-key", "x-goog-api-key", "authorization":
			if source != "" || len(values) != 1 {
				return nil, false
			}
			source, value = name, strings.TrimSpace(values[0])
			if name == "authorization" {
				scheme, token, ok := strings.Cut(value, " ")
				if !ok || !strings.EqualFold(scheme, "bearer") {
					return nil, false
				}
				value = strings.TrimSpace(token)
			}
			if value == "" || strings.ContainsAny(value, ", \t") {
				return nil, false
			}
			delete(clone.Header, k)
		}
	}
	if source == "authorization" {
		clone.Header.Set("Authorization", "Bearer "+value)
	} else if source != "" {
		clone.Header.Set("X-Api-Key", value)
	}
	return clone, true
}

func validGeminiQuery(u *url.URL, stream bool) bool {
	if u.RawQuery == "" {
		return !u.ForceQuery
	}
	if !stream {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	return err == nil && !strings.Contains(u.RawQuery, "&") && len(query) == 1 && len(query["alt"]) == 1 && query["alt"][0] == "sse"
}

func (h *Handler) readGeminiRequest(r *http.Request, path string) (*canonical.Value, bool, error) {
	typ, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		return nil, false, &bodyError{http.StatusUnsupportedMediaType, "请求体必须为 UTF-8 application/json"}
	}
	limit := int64(protocol.GeminiMaxJSONBytes)
	if h.maxJSONBytes > 0 {
		limit = min(limit, h.maxJSONBytes)
	}
	if r.ContentLength > limit {
		return nil, false, bodyTooLargeError(limit)
	}
	raw, err := readAllLimited(r.Body, limit)
	if err != nil {
		return nil, false, err
	}
	parsedPath, model := endpoint.ParseGeminiPath(r.URL)
	if parsedPath != path {
		return nil, false, &bodyError{http.StatusNotFound, ""}
	}
	return protocol.GeminiRequest(raw, model, path == endpoint.GeminiStream)
}

// Shared prepare errors may include model, Key or authorizer-supplied messages; Gemini only exposes fixed text.
func writePrepareJSON(w http.ResponseWriter, path string, status int, payload *canonical.Value) {
	if endpoint.IsGemini(path) && status >= 400 {
		protocol.WriteGeminiError(w, status, "")
		return
	}
	writeJSON(w, status, payload)
}
