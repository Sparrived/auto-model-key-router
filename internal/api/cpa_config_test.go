package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestCPARuntimeConfigV8Contract 用本地 Mock CPA 钉住 v8 配置读写边界。
func TestCPARuntimeConfigV8Contract(t *testing.T) {
	type recordedCall struct {
		method  string
		path    string
		ifMatch string
		body    map[string]any
	}

	var calls []recordedCall
	conflict := false
	etag := `"v1"`
	currentConfig := map[string]any{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mk-1" {
			stubJSON(t, w, http.StatusUnauthorized, map[string]any{"error": "bad management key"})
			return
		}
		call := recordedCall{method: r.Method, path: r.URL.Path, ifMatch: r.Header.Get("If-Match")}
		if r.Method == http.MethodPatch {
			if err := json.NewDecoder(r.Body).Decode(&call.body); err != nil {
				t.Errorf("PATCH body 不是 JSON 对象: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		calls = append(calls, call)
		if r.URL.Path != "/v8/management/config" {
			stubJSON(t, w, http.StatusNotFound, map[string]any{"error": "unexpected path"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", etag)
			stubJSON(t, w, http.StatusOK, currentConfig)
		case http.MethodPatch:
			if conflict {
				stubJSON(t, w, http.StatusPreconditionFailed, map[string]any{"error": "etag mismatch"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			stubJSON(t, w, http.StatusMethodNotAllowed, map[string]any{"error": "unexpected method"})
		}
	}))
	t.Cleanup(upstream.Close)
	server := cpaServer(t, `{"main":{"base_url":"`+upstream.URL+`","management_key":"mk-1"}}`)

	t.Run("GET 保留 false 0 空数组并且只返回白名单视图", func(t *testing.T) {
		calls = nil
		conflict = false
		etag = `"v1"`
		currentConfig = map[string]any{
			"observability":  map[string]any{"logs": map[string]any{"debug": false}},
			"routing":        map[string]any{"retry": map[string]any{"request-retry": 0}, "strategy": "fill-first"},
			"access":         map[string]any{"api-keys": []any{}},
			"management_key": "must-not-leak",
		}
		recorder := callCPA(t, server, http.MethodGet, "/api/cpa-instances/main/runtime-config", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET 期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
		}
		var response cpaRuntimeConfigResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("GET 响应无法解码: %v", err)
		}
		if response.Instance != "main" || response.ETag != `"v1"` {
			t.Errorf("实例或 ETag 不对: %+v", response)
		}
		if response.Debug == nil || *response.Debug || response.RequestRetry == nil || *response.RequestRetry != 0 {
			t.Errorf("false/0 被丢弃或改值: %+v", response)
		}
		if response.Strategy == nil || *response.Strategy != "fill-first" || response.ClientAPIKeys == nil || len(response.ClientAPIKeys) != 0 {
			t.Errorf("strategy 或空数组不对: %+v", response)
		}
		if strings.Contains(recorder.Body.String(), "management_key") || strings.Contains(recorder.Body.String(), "must-not-leak") {
			t.Errorf("白名单视图泄漏了管理配置: %s", recorder.Body.String())
		}
		if len(calls) != 1 || calls[0].method != http.MethodGet || calls[0].path != "/v8/management/config" {
			t.Fatalf("GET 上游请求不对: %+v", calls)
		}
	})

	t.Run("PATCH 使用 If-Match 与嵌套白名单 body 并在成功后重读", func(t *testing.T) {
		calls = nil
		conflict = false
		etag = `"v2"`
		// 重读值刻意不同于请求草稿，防止把请求体原样回显冒充持久化结果。
		currentConfig = map[string]any{
			"observability": map[string]any{"logs": map[string]any{"debug": true}},
			"routing":       map[string]any{"retry": map[string]any{"request-retry": 3}, "strategy": "weighted-round-robin"},
			"access":        map[string]any{"api-keys": []any{"fresh-key"}},
		}
		payload := `{"etag":"\"v1\"","debug":false,"request_retry":0,"strategy":"round-robin","client_api_keys":[]}`
		recorder := callCPA(t, server, http.MethodPatch, "/api/cpa-instances/main/runtime-config", payload)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH 期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
		}
		if len(calls) != 2 || calls[0].method != http.MethodPatch || calls[1].method != http.MethodGet {
			t.Fatalf("成功后必须 PATCH 再 GET，实际 %+v", calls)
		}
		for _, call := range calls {
			if call.path != "/v8/management/config" {
				t.Errorf("上游路径不是固定 v8 配置路径: %+v", call)
			}
		}
		if calls[0].ifMatch != `"v1"` {
			t.Errorf("If-Match 应原样携带强 ETag，实际 %q", calls[0].ifMatch)
		}
		wantPatch := map[string]any{
			"observability": map[string]any{"logs": map[string]any{"debug": false}},
			"routing": map[string]any{
				"retry":    map[string]any{"request-retry": float64(0)},
				"strategy": "round-robin",
			},
			"access": map[string]any{"api-keys": []any{}},
		}
		if !reflect.DeepEqual(calls[0].body, wantPatch) {
			t.Errorf("PATCH body 不对: 得到 %#v；期望 %#v", calls[0].body, wantPatch)
		}
		var response cpaRuntimeConfigResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("PATCH 重读响应无法解码: %v", err)
		}
		if response.ETag != `"v2"` || response.Debug == nil || !*response.Debug ||
			response.RequestRetry == nil || *response.RequestRetry != 3 ||
			response.Strategy == nil || *response.Strategy != "weighted-round-robin" ||
			!reflect.DeepEqual(response.ClientAPIKeys, []string{"fresh-key"}) {
			t.Errorf("返回的不是成功后的重读快照: %+v", response)
		}
	})

	t.Run("上游 412 映射为 409 且不继续重读", func(t *testing.T) {
		calls = nil
		conflict = true
		payload := `{"etag":"\"stale\"","debug":false}`
		recorder := callCPA(t, server, http.MethodPatch, "/api/cpa-instances/main/runtime-config", payload)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("412 应映射为 409，实际 %d（%s）", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "重新读取") {
			t.Errorf("409 应提示重新读取: %s", recorder.Body.String())
		}
		if len(calls) != 1 || calls[0].method != http.MethodPatch || calls[0].ifMatch != `"stale"` {
			t.Errorf("冲突后不应继续 GET，且 If-Match 应保留: %+v", calls)
		}
	})
}
