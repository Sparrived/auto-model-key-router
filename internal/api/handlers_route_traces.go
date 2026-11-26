package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/auth"
	"github.com/Sparrived/auto-model-key-router/internal/routetrace"
)

// RouteTraceHandler 只查询单个请求，使用当前有效凭据与专用的服务端投影。
func (s *Server) RouteTraceHandler(store *routetrace.Store) http.HandlerFunc {
	queries := make(chan struct{}, 64)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, httpErrorf(http.StatusMethodNotAllowed, "仅支持 GET"))
			return
		}
		s.reload()
		cfg, err := s.currentConfig()
		if err != nil || cfg == nil {
			writeError(w, httpErrorf(http.StatusUnauthorized, "%s", routetrace.ErrUnauthorized.Error()))
			return
		}
		var subject routetrace.Subject
		credential := auth.RequestAPIKey(r.Header)
		switch {
		case s.Authorizer != nil:
			// 宿主接管整条鉴权链，未显式 full 时不能回落成内置身份。
			if ctx := auth.Authenticate(s.Authorizer, r, cfg.LocalAPIKey); ctx != nil && ctx.IsFull() {
				subject.Type = routetrace.OwnerFull
			}
		case cfg.LocalAPIKey != "" && auth.Authenticate(nil, r, cfg.LocalAPIKey) != nil:
			subject.Type = routetrace.OwnerFull
		case credential != "" && cfg.WorkspaceForAPIKey(credential) == "":
			if key := cfg.AccessKeyFor(credential); key != nil {
				if key.Enabled {
					subject = routetrace.Subject{Type: routetrace.OwnerAccessKey, Credential: credential}
				}
			} else if cfg.WorkspaceForInferenceKey(credential) != "" {
				subject = routetrace.Subject{Type: routetrace.OwnerWorkspaceInference, Credential: credential}
			}
		}
		if subject.Type == "" {
			writeError(w, httpErrorf(http.StatusUnauthorized, "%s", routetrace.ErrUnauthorized.Error()))
			return
		}
		id := r.PathValue("request_id")
		if id == "" {
			id, _ = strings.CutPrefix(r.URL.Path, "/api/route-traces/")
		}
		if !routetrace.ValidID(id) {
			writeError(w, httpErrorf(http.StatusUnprocessableEntity, "%s", routetrace.ErrInvalidID.Error()))
			return
		}
		select {
		case queries <- struct{}{}:
			defer func() { <-queries }()
		default:
			writeError(w, httpErrorf(http.StatusTooManyRequests, "追踪查询过于频繁"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		body, err := store.Lookup(ctx, subject, id)
		switch err {
		case nil:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case routetrace.ErrNotFound:
			writeError(w, httpErrorf(http.StatusNotFound, "%s", routetrace.ErrNotFound.Error()))
		default:
			writeError(w, httpErrorf(http.StatusServiceUnavailable, "%s", routetrace.ErrUnavailable.Error()))
		}
	}
}
