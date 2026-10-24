package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 本文件钉住重置卡这条新通道：读哪些上游接口、怎么归一化、以及「最快过期优先」到底
// 选中了哪一张。用假 CPA 而不是真上游，测试不依赖任何外部网络。

type resetCreditStub struct {
	mu        sync.Mutex
	files     []map[string]any
	responses map[string]resetCreditStubResponse
	calls     []resetCreditCall
}

type resetCreditStubResponse struct {
	status int
	body   string
}

type resetCreditCall struct {
	AuthIndex string            `json:"auth_index"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header"`
	Data      string            `json:"data"`
}

func newResetCreditStub(t *testing.T, files []map[string]any) (*resetCreditStub, *httptest.Server) {
	t.Helper()
	stub := &resetCreditStub{files: files, responses: map[string]resetCreditStubResponse{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/management/auth-files", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mk-1" {
			stubJSON(t, w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		stubJSON(t, w, http.StatusOK, map[string]any{"observed_at": "2026-01-01T00:00:00Z", "files": stub.files})
	})
	mux.HandleFunc("/v0/management/api-call", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mk-1" {
			stubJSON(t, w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		var call resetCreditCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			stubJSON(t, w, http.StatusBadRequest, map[string]any{"error": "bad body"})
			return
		}
		stub.mu.Lock()
		stub.calls = append(stub.calls, call)
		response, ok := stub.responses[call.Method+" "+call.URL]
		stub.mu.Unlock()
		if !ok {
			stubJSON(t, w, http.StatusBadGateway, map[string]any{"error": "unexpected upstream call: " + call.Method + " " + call.URL})
			return
		}
		stubJSON(t, w, http.StatusOK, map[string]any{"status_code": response.status, "body": response.body})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return stub, server
}

func (s *resetCreditStub) add(method, url string, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[method+" "+url] = resetCreditStubResponse{status: status, body: body}
}

func (s *resetCreditStub) lastCall(t *testing.T) resetCreditCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("没有收到 api-call")
	}
	return s.calls[len(s.calls)-1]
}

func decodeResetCreditsReport(t *testing.T, body []byte) cpaResetCreditsReport {
	t.Helper()
	var report cpaResetCreditsReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, body)
	}
	return report
}

func TestChooseResetCreditPicksEarliestExpiry(t *testing.T) {
	credits := []cpaResetCredit{
		{ID: "later", Usable: true, RemainingCount: 1, ExpiresAt: "2030-02-01T00:00:00Z"},
		{ID: "no-expiry", Usable: true, RemainingCount: 1},
		{ID: "sooner", Usable: true, RemainingCount: 1, ExpiresAt: "2030-01-01T00:00:00Z"},
		{ID: "unusable", Usable: false, RemainingCount: 1, ExpiresAt: "2020-01-01T00:00:00Z"},
	}
	picked := chooseResetCredit(credits, "")
	if picked == nil || picked.ID != "sooner" {
		t.Fatalf("应选最快过期的可用卡，实际 %+v", picked)
	}
	if picked := chooseResetCredit(credits, "later"); picked == nil || picked.ID != "later" {
		t.Fatalf("指定 credit_id 时应只认指定的卡，实际 %+v", picked)
	}
	if picked := chooseResetCredit(credits, "unusable"); picked != nil {
		t.Fatalf("不可用的卡不应被选中: %+v", picked)
	}
}

func TestListCPAResetCreditsCodexAndClaude(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "1", "name": "codex.json", "provider": "codex", "email": "gpt@example.com",
			"status": "active", "id_token": map[string]any{"chatgpt_account_id": "acc-1"}},
		{"auth_index": "2", "name": "claude.json", "provider": "claude", "email": "claude@example.com",
			"status": "active"},
		{"auth_index": "3", "name": "gemini.json", "provider": "gemini", "status": "active"},
	})
	stub.add("GET", cpaCodexResetCreditsURL, http.StatusOK, `{
		"available_count": 2,
		"credits": [
			{"id":"codex-late","reset_type":"codex_rate_limits","status":"available","granted_at":"2029-12-01T00:00:00Z","expires_at":"2030-02-01T00:00:00Z"},
			{"id":"codex-soon","reset_type":"codex_rate_limits","status":"available","granted_at":"2029-12-01T00:00:00Z","expires_at":"2030-01-01T00:00:00Z"}
		]}`)
	stub.add("GET", cpaClaudeOAuthUsageURL+"?cedar_ember=1&skip_spend=1", http.StatusOK, `{
		"cedar_ember": {"eligible": true, "next_grant_id": "claude-soon",
			"grants": [
				{"id":"claude-late","label":"Claude 重置卡","resets_left":1,"ends_at":"2030-03-01T00:00:00Z","starts_at":"2029-12-01T00:00:00Z","usable_now":true},
				{"id":"claude-soon","label":"Claude 重置卡","resets_left":2,"ends_at":"2030-01-15T00:00:00Z","starts_at":"2029-12-01T00:00:00Z","usable_now":true,"clears":["five_hour"]}
			]}}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodGet, "/api/cpa-reset-credits", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	report := decodeResetCreditsReport(t, recorder.Body.Bytes())
	if len(report.Instances) != 1 || !report.Instances[0].OK {
		t.Fatalf("实例读数不对: %+v", report.Instances)
	}
	accounts := report.Instances[0].Accounts
	if len(accounts) != 2 {
		t.Fatalf("只应返回 claude/codex 两个账号，实际 %+v", accounts)
	}
	codex := accounts[0]
	if codex.Provider != "codex" || codex.Reset.AvailableCount != 2 || len(codex.Reset.Credits) != 2 {
		t.Fatalf("Codex 读数不对: %+v", codex)
	}
	if codex.Reset.Credits[0].ID != "codex-soon" {
		t.Errorf("Codex 应按最快过期排序，实际首条 %+v", codex.Reset.Credits[0])
	}
	claude := accounts[1]
	if claude.Provider != "claude" || claude.Reset.AvailableCount != 3 || len(claude.Reset.Credits) != 2 {
		t.Fatalf("Claude 读数不对: %+v", claude)
	}
	if claude.Reset.Credits[0].ID != "claude-soon" || claude.Reset.Credits[0].RemainingCount != 2 {
		t.Errorf("Claude 应按最快过期排序并保留次数，实际 %+v", claude.Reset.Credits[0])
	}
}

func TestUseCPAResetCreditCodexUsesEarliest(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "1", "name": "codex.json", "provider": "codex", "status": "active",
			"id_token": map[string]any{"chatgpt_account_id": "acc-1"}},
	})
	stub.add("GET", cpaCodexResetCreditsURL, http.StatusOK, `{
		"available_count": 2,
		"credits": [
			{"id":"codex-late","reset_type":"codex_rate_limits","status":"available","expires_at":"2030-02-01T00:00:00Z"},
			{"id":"codex-soon","reset_type":"codex_rate_limits","status":"available","expires_at":"2030-01-01T00:00:00Z"}
		]}`)
	stub.add("POST", cpaCodexResetCreditsURL+"/consume", http.StatusOK, `{"outcome":"reset"}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodPost, "/api/cpa-reset-credits/use",
		`{"instance":"cpa-a","auth_index":"1"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var result cpaResetCreditUseResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if result.Outcome != "reset" || result.Credit == nil || result.Credit.ID != "codex-soon" {
		t.Fatalf("应消费最快过期的 Codex 卡，实际 %+v", result)
	}
	call := stub.lastCall(t)
	if call.Method != http.MethodPost || call.URL != cpaCodexResetCreditsURL+"/consume" {
		t.Fatalf("消费请求打错了地址: %+v", call)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(call.Data), &body); err != nil {
		t.Fatalf("消费请求体不是 JSON: %v（%s）", err, call.Data)
	}
	if body["credit_id"] != "codex-soon" || strings.TrimSpace(body["redeem_request_id"]) == "" {
		t.Errorf("消费请求体不对: %+v", body)
	}
	if call.Header["Authorization"] != "Bearer $TOKEN$" || call.Header["chatgpt-account-id"] != "acc-1" {
		t.Errorf("消费请求头不对: %+v", call.Header)
	}
}

func TestUseCPAResetCreditClaudeCedar(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "2", "name": "claude.json", "provider": "claude", "status": "active"},
	})
	stub.add("GET", cpaClaudeOAuthUsageURL+"?cedar_ember=1&skip_spend=1", http.StatusOK, `{
		"cedar_ember": {"eligible": true, "next_grant_id": "claude-soon",
			"grants": [
				{"id":"claude-late","resets_left":1,"ends_at":"2030-02-01T00:00:00Z","usable_now":true},
				{"id":"claude-soon","resets_left":1,"ends_at":"2030-01-01T00:00:00Z","usable_now":true}
			]}}`)
	stub.add("GET", cpaClaudeProfileURL, http.StatusOK, `{"organization":{"uuid":"org-1"}}`)
	stub.add("POST", "https://api.anthropic.com/api/organizations/org-1/reset_rate_limits", http.StatusOK,
		`{"result":"reset","cleared":["five_hour"]}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodPost, "/api/cpa-reset-credits/use",
		`{"instance":"cpa-a","auth_index":"2","request_id":"retry-1"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var result cpaResetCreditUseResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if result.Outcome != "reset" || result.Credit == nil || result.Credit.ID != "claude-soon" {
		t.Fatalf("应消费最快过期的 Claude 卡，实际 %+v", result)
	}
	if len(result.Cleared) != 1 || result.Cleared[0] != "five_hour" {
		t.Errorf("cleared 应原样回传: %+v", result.Cleared)
	}
	call := stub.lastCall(t)
	if call.URL != "https://api.anthropic.com/api/organizations/org-1/reset_rate_limits" {
		t.Fatalf("消费请求打错了地址: %+v", call)
	}
	if !strings.Contains(call.Data, `"grant_id":"claude-soon"`) || !strings.Contains(call.Data, `"request_id":"retry-1"`) {
		t.Errorf("消费请求体不对: %s", call.Data)
	}
	if call.Header["Authorization"] != "Bearer $TOKEN$" || call.Header["anthropic-beta"] != cpaClaudeOAuthBeta {
		t.Errorf("消费请求头不对: %+v", call.Header)
	}
}

func TestListCPAResetCreditsCodexExpiredIsNotUsable(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "1", "name": "codex.json", "provider": "codex", "status": "active",
			"id_token": map[string]any{"chatgpt_account_id": "acc-1"}},
	})
	stub.add("GET", cpaCodexResetCreditsURL, http.StatusOK, `{
		"available_count": 2,
		"credits": [
			{"id":"codex-active","reset_type":"codex_rate_limits","status":"available","expires_at":"2030-01-01T00:00:00Z"},
			{"id":"codex-expired","reset_type":"codex_rate_limits","status":"available","expires_at":"2020-01-01T00:00:00Z"}
		]}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodGet, "/api/cpa-reset-credits", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	report := decodeResetCreditsReport(t, recorder.Body.Bytes())
	reset := report.Instances[0].Accounts[0].Reset
	if reset.AvailableCount != 1 || reset.RedeemableCount != 1 {
		t.Errorf("过期卡不应计入可用次数: %+v", reset)
	}
	if len(reset.Credits) != 2 || reset.Credits[0].Usable || reset.Credits[0].Status != "expired" {
		t.Errorf("过期卡应标成 expired 且不可用: %+v", reset.Credits)
	}
}

func TestListCPAResetCreditsClaudeSurfaceFallsBackToJuniper(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "2", "name": "claude.json", "provider": "claude", "status": "active"},
	})
	stub.add("GET", cpaClaudeOAuthUsageURL+"?cedar_ember=1&skip_spend=1", http.StatusOK, `{
		"cedar_ember": {"eligible": false, "ineligible_reason": "surface",
			"grants": [{"id":"surface-card","resets_left":2,"ends_at":"2030-01-01T00:00:00Z","usable_now":false}]}}`)
	stub.add("GET", cpaClaudeOAuthUsageURL+"?at_wall=1&skip_spend=1", http.StatusOK, `{
		"juniper_tide": {"eligible": true, "arm": "reset", "available": true,
			"weekly_resets_at": "2030-02-01T00:00:00Z"}}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodGet, "/api/cpa-reset-credits", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	reset := decodeResetCreditsReport(t, recorder.Body.Bytes()).Instances[0].Accounts[0].Reset
	if reset.AvailableCount != 1 || reset.RedeemableCount != 1 || reset.NextCreditID != "juniper_tide" {
		t.Fatalf("surface 后应继续探测到 Juniper: %+v", reset)
	}
	if len(reset.Credits) != 1 || reset.Credits[0].Program != "juniper_tide" || !reset.Credits[0].Usable {
		t.Errorf("应返回可用的 Juniper 卡: %+v", reset.Credits)
	}
	if call := stub.lastCall(t); call.URL != cpaClaudeOAuthUsageURL+"?at_wall=1&skip_spend=1" {
		t.Errorf("最后应请求 Juniper 探测: %+v", call)
	}
}

func TestListCPAResetCreditsClaudeErrorIsNotMasked(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "2", "name": "claude.json", "provider": "claude", "status": "active"},
	})
	stub.add("GET", cpaClaudeOAuthUsageURL+"?cedar_ember=1&skip_spend=1", http.StatusUnauthorized,
		`{"error":"expired token"}`)
	stub.add("GET", cpaClaudeOAuthUsageURL+"?at_wall=1&skip_spend=1", http.StatusOK, `{}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodGet, "/api/cpa-reset-credits", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	reset := decodeResetCreditsReport(t, recorder.Body.Bytes()).Instances[0].Accounts[0].Reset
	if !strings.Contains(reset.Error, "401") || reset.Reason == "not_offered" {
		t.Fatalf("上游 401 不能被伪装成没有卡: %+v", reset)
	}
}

func TestListCPAResetCreditsClaudeNoBlockIsNotOffered(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "2", "name": "claude.json", "provider": "claude", "status": "active"},
	})
	stub.add("GET", cpaClaudeOAuthUsageURL+"?cedar_ember=1&skip_spend=1", http.StatusOK, `{}`)
	stub.add("GET", cpaClaudeOAuthUsageURL+"?at_wall=1&skip_spend=1", http.StatusOK, `{}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodGet, "/api/cpa-reset-credits", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	reset := decodeResetCreditsReport(t, recorder.Body.Bytes()).Instances[0].Accounts[0].Reset
	if reset.Error != "" || reset.Reason != "not_offered" {
		t.Fatalf("两个探测都成功但无块时才是 not_offered: %+v", reset)
	}
}

func TestListCPAResetCreditsCodexAbsentEndpointIsNotSupported(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "1", "name": "codex.json", "provider": "codex", "status": "active",
			"id_token": map[string]any{"chatgpt_account_id": "acc-1"}},
	})
	stub.add("GET", cpaCodexResetCreditsURL, http.StatusMethodNotAllowed, `{}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodGet, "/api/cpa-reset-credits", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	reset := decodeResetCreditsReport(t, recorder.Body.Bytes()).Instances[0].Accounts[0].Reset
	if reset.Reason != "not_supported" || reset.Error != "" {
		t.Fatalf("405 应视为旧版无此接口: %+v", reset)
	}
}

func TestUseCPAResetCreditJuniperOmitsRequestID(t *testing.T) {
	stub, server := newResetCreditStub(t, []map[string]any{
		{"auth_index": "2", "name": "claude.json", "provider": "claude", "status": "active"},
	})
	stub.add("GET", cpaClaudeOAuthUsageURL+"?cedar_ember=1&skip_spend=1", http.StatusOK, `{}`)
	stub.add("GET", cpaClaudeOAuthUsageURL+"?at_wall=1&skip_spend=1", http.StatusOK, `{
		"juniper_tide": {"eligible": true, "arm": "reset", "available": true,
			"weekly_resets_at": "2030-02-01T00:00:00Z"}}`)
	stub.add("GET", cpaClaudeProfileURL, http.StatusOK, `{"organization":{"uuid":"org-1"}}`)
	stub.add("POST", "https://api.anthropic.com/api/organizations/org-1/reset_rate_limits", http.StatusOK,
		`{"result":"reset","cleared":["five_hour"]}`)

	serverConfig := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+server.URL+`","management_key":"mk-1"}}`)
	recorder := callCPA(t, serverConfig, http.MethodPost, "/api/cpa-reset-credits/use",
		`{"instance":"cpa-a","auth_index":"2","request_id":"ignored-by-juniper"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var result cpaResetCreditUseResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if result.Outcome != "reset" || result.Credit == nil || result.Credit.Program != "juniper_tide" {
		t.Fatalf("应消费 Juniper 会话重置: %+v", result)
	}
	if result.RequestID != "" {
		t.Errorf("Juniper 不支持 request_id，响应不应回显: %+v", result)
	}
	call := stub.lastCall(t)
	if call.Data != `{"program":"juniper_tide"}` {
		t.Errorf("Juniper 请求体不应带 request_id: %s", call.Data)
	}
}
