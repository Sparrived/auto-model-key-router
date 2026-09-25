package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件固化 CPA 账号资源的管理面语义：实例清单的读写与校验、以及账号扇出的归一化。
//
// 扇出部分用**假的 CPA 实例**（httptest）测：真实 CPA 的行为已经在 docs/
// SUBSCRIPTION-QUOTA.md 里查证过，这里要钉住的是 AMKR 自己这一侧——请求打到哪、
// 带什么凭据、拿回来的东西怎么归一化、以及某个实例挂掉时看板还剩多少。

// —— 被动额度信号（纯函数）——

// closeTo 比较比例。
//
// 必须带容差：实现算的是 1-0.42，二进制浮点给出的是 0.5800000000000001，精确比较会把
// 正确的实现判成错的（这正是本文件第一版犯的错）。
func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

// TestPassiveQuotaWindowsClaude 钉住 Anthropic 统一额度头的解析口径。
//
// 关键点有三条：utilization 是 0..1 的**已用**比例（界面要的是剩余，得减）；
// `-Reset` 是 Unix 秒；头名大小写不敏感（信号表的键由 CPA 归一，不能写死一种大小写）。
func TestPassiveQuotaWindowsClaude(t *testing.T) {
	signals := map[string]any{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.42",
		"Anthropic-Ratelimit-Unified-5h-Reset":       "1800000000",
		"Anthropic-Ratelimit-Unified-5h-Status":      "allowed_warning",
		"Anthropic-Ratelimit-Unified-7d-utilization": "1",
		"Anthropic-Ratelimit-Unified-7d-Reset":       "2027-01-15T08:00:00Z",
	}

	windows := passiveQuotaWindows("claude", signals)
	if len(windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %d（%+v）", len(windows), windows)
	}
	fiveHour := windows[0]
	if fiveHour.Key != "claude/5h" || fiveHour.Label != "5 小时" {
		t.Errorf("5 小时窗口标识不对: %+v", fiveHour)
	}
	if !closeTo(fiveHour.Remaining, 0.58) {
		t.Errorf("剩余比例应为 1-0.42=0.58，实际 %v", fiveHour.Remaining)
	}
	if fiveHour.ResetAt != "2027-01-15T08:00:00Z" {
		t.Errorf("Unix 秒应解析成 2027-01-15T08:00:00Z，实际 %q", fiveHour.ResetAt)
	}
	if fiveHour.Status != "allowed_warning" || fiveHour.Source != "passive" {
		t.Errorf("状态或来源不对: %+v", fiveHour)
	}
	// 7d 用满（utilization=1）时剩余是 0，而不是被当成「没有这个窗口」丢掉。
	sevenDay := windows[1]
	if sevenDay.Key != "claude/7d" || sevenDay.Remaining != 0 {
		t.Errorf("7 天窗口应是剩余 0: %+v", sevenDay)
	}
	if sevenDay.ResetAt != "2027-01-15T08:00:00Z" {
		t.Errorf("RFC3339 时间戳应原样归一，实际 %q", sevenDay.ResetAt)
	}

	// 其它 provider 没有被动信号可解析，必须回空而不是硬套 Anthropic 的头。
	// 判空而不是判 nil：函数返回空数组而非 nil（nil 会被 JSON 写成 null，见
	// TestListCPAAccountsWindowsAreAlwaysArrays）。
	if got := passiveQuotaWindows("gemini", signals); len(got) != 0 {
		t.Errorf("gemini 不应解析出窗口: %+v", got)
	}
}

// TestPassiveQuotaWindowsCodex 钉住 x-codex-* 的解析口径。
//
// Codex 的窗口名不能枚举：额外限额是 `x-codex-<limit>-primary-*`。这里同时钉住
// 「同族但含义不同的头不能被当成窗口」（-over-secondary-limit-percent）与
// 「超过 100% 夹到 100 而不是丢窗口」。
func TestPassiveQuotaWindowsCodex(t *testing.T) {
	signals := map[string]any{
		"X-Codex-Primary-Used-Percent":             "58",
		"X-Codex-Primary-Window-Minutes":           "300",
		"X-Codex-Primary-Reset-At":                 "1800000000",
		"X-Codex-Secondary-Used-Percent":           "101",
		"X-Codex-Bengalfox-Primary-Used-Percent":   "12",
		"X-Codex-Bengalfox-Primary-Window-Minutes": "10080",
		"X-Codex-Bengalfox-Primary-Limit-Reached":  "true",
		"X-Codex-Over-Secondary-Limit-Percent":     "30",
	}

	windows := passiveQuotaWindows("codex", signals)
	if len(windows) != 3 {
		t.Fatalf("期望 3 个窗口，实际 %d（%+v）", len(windows), windows)
	}
	// 顺序按头名排序，因此 limit id 在前的先出现——同一份快照每次都给同一个顺序。
	byKey := map[string]cpaQuotaWindow{}
	for _, window := range windows {
		byKey[window.Key] = window
	}
	bengalfox, ok := byKey["codex/bengalfox-primary"]
	if !ok {
		t.Fatalf("缺少 bengalfox 窗口: %+v", windows)
	}
	if bengalfox.Label != "bengalfox · 7 天" {
		t.Errorf("额外限额应带 limit id 与窗口长度，实际 %q", bengalfox.Label)
	}
	if bengalfox.Status != "rejected" {
		t.Errorf("limit-reached=true 应标成 rejected，实际 %q", bengalfox.Status)
	}
	primary := byKey["codex/primary"]
	if primary.Label != "5 小时" || !closeTo(primary.Remaining, 0.42) || primary.ResetAt != "2027-01-15T08:00:00Z" {
		t.Errorf("primary 窗口解析不对: %+v", primary)
	}
	secondary := byKey["codex/secondary"]
	if secondary.Label != "次要窗口" || secondary.Remaining != 0 {
		t.Errorf("超过 100%% 应夹到剩余 0 并保留窗口: %+v", secondary)
	}
}

// —— 实例清单接口 ——

type cpaInstancesResponse struct {
	ConfigRevision string `json:"config_revision"`
	Instances      map[string]struct {
		Label         string `json:"label"`
		BaseURL       string `json:"base_url"`
		ManagementKey string `json:"management_key"`
	} `json:"instances"`
}

// cpaServer 装配一个指向临时配置文件的 Server，配置里带给定的实例清单。
func cpaServer(t *testing.T, instancesJSON string) *Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")
	fixture := `{"config_version":4,"local_api_key":"local-key","ops_enabled":false,` +
		`"webui_enabled":false,"cpa_instances":` + instancesJSON + `}`
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return &Server{ConfigPath: path}
}

// callCPA 向 CPA 相关路由发一条请求（以本地主凭据鉴权）。
func callCPA(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer local-key")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func decodeCPAInstances(t *testing.T, recorder *httptest.ResponseRecorder) cpaInstancesResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var parsed cpaInstancesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	return parsed
}

// TestCPAInstancesRoundTrip 钉住实例清单的读、写、规范化与版本号。
func TestCPAInstancesRoundTrip(t *testing.T) {
	server := cpaServer(t, `{}`)

	// 空配置：读回空对象，且 config_revision 一定在（写接口要它防并发覆盖）。
	listed := decodeCPAInstances(t, callCPA(t, server, http.MethodGet, "/api/cpa-instances", ""))
	if len(listed.Instances) != 0 {
		t.Fatalf("期望空清单，实际 %+v", listed.Instances)
	}
	if listed.ConfigRevision == "" {
		t.Fatal("响应缺少 config_revision")
	}

	body := `{"config_revision":"` + listed.ConfigRevision + `","instances":{` +
		`"cpa-a":{"label":"主力","base_url":"http://127.0.0.1:8317/","management_key":"mk-1"},` +
		`"cpa-b":{"base_url":"https://cpa-b.example","management_key":"mk-2"}}}`
	saved := decodeCPAInstances(t, callCPA(t, server, http.MethodPut, "/api/cpa-instances", body))
	if len(saved.Instances) != 2 {
		t.Fatalf("期望写入 2 个实例，实际 %+v", saved.Instances)
	}
	if got := saved.Instances["cpa-a"]; got.BaseURL != "http://127.0.0.1:8317" || got.Label != "主力" {
		t.Errorf("写入时未规范化 base_url 或丢了 label: %+v", got)
	}
	// 没起名字的实例用 ID 兜底：界面上总得有个可读标题。
	if got := saved.Instances["cpa-b"]; got.Label != "cpa-b" {
		t.Errorf("缺 label 时应用 ID 兜底: %+v", got)
	}

	// 再读一次：写接口的返回值与后续读取必须一致（否则界面会闪一下旧值）。
	relisted := decodeCPAInstances(t, callCPA(t, server, http.MethodGet, "/api/cpa-instances", ""))
	if len(relisted.Instances) != 2 || relisted.Instances["cpa-b"].ManagementKey != "mk-2" {
		t.Errorf("读回的清单与写入不一致: %+v", relisted.Instances)
	}
}

// TestCPAInstancesRejectsBadWrites 钉住写路径上的两类拒绝：
// 请求体形状不对（422，由 spec 挡）与实例内容不合法（422/409，由 configops 挡），
// 以及版本号过期（409，防两个标签页互相覆盖）。
func TestCPAInstancesRejectsBadWrites(t *testing.T) {
	server := cpaServer(t, `{"cpa-a":{"base_url":"https://a.example","management_key":"mk-1"}}`)
	revision := decodeCPAInstances(t, callCPA(t, server, http.MethodGet, "/api/cpa-instances", "")).ConfigRevision

	cases := []struct {
		name string
		body string
		want int
	}{
		{"缺 instances", `{"config_revision":"` + revision + `"}`, http.StatusUnprocessableEntity},
		{"instances 不是对象", `{"config_revision":"` + revision + `","instances":[]}`, http.StatusUnprocessableEntity},
		{"base_url 不是字符串", `{"config_revision":"` + revision + `","instances":{"x":{"base_url":1,"management_key":"mk"}}}`, http.StatusUnprocessableEntity},
		{"management_key 为空", `{"config_revision":"` + revision + `","instances":{"x":{"base_url":"https://x.example","management_key":" "}}}`, http.StatusUnprocessableEntity},
		{"版本号过期", `{"config_revision":"stale-revision","instances":{}}`, http.StatusConflict},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := callCPA(t, server, http.MethodPut, "/api/cpa-instances", testCase.body)
			if recorder.Code != testCase.want {
				t.Errorf("期望 %d，实际 %d（%s）", testCase.want, recorder.Code, recorder.Body.String())
			}
		})
	}

	// 全部失败之后，原清单必须一个字节都没动。
	final := decodeCPAInstances(t, callCPA(t, server, http.MethodGet, "/api/cpa-instances", ""))
	if len(final.Instances) != 1 || final.Instances["cpa-a"].ManagementKey != "mk-1" {
		t.Errorf("失败的写入不应改动配置: %+v", final.Instances)
	}
}

// —— 账号资源扇出 ——

// cpaStubSpec 描述一个假 CPA 实例要回什么。
type cpaStubSpec struct {
	// files 是 auth-files 的条目；nil 表示该端点回 500。
	files []map[string]any
	// quota 按 auth_index 给 quota/fetch 的响应；没有对应项就回 501
	// （真实 CPA 在没有额度提供者时正是这么回的）。
	quota map[string]any
}

// cpaStub 起一个假的 CPA 管理面。
//
// 顺带把凭据钉住：两个端点都要求 `Bearer mk-1`，收到别的就回 401——AMKR 用错头或漏
// 带头时，这个桩会立刻让用例失败，而不是让「凭据没发出去」这种问题溜到线上。
func cpaStub(t *testing.T, spec cpaStubSpec) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/management/auth-files", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mk-1" {
			stubJSON(t, w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if spec.files == nil {
			stubJSON(t, w, http.StatusInternalServerError, map[string]any{"error": "boom"})
			return
		}
		stubJSON(t, w, http.StatusOK, map[string]any{
			"observed_at": "2026-01-01T00:00:00Z",
			"files":       spec.files,
		})
	})
	mux.HandleFunc("/v0/management/quota/fetch", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mk-1" {
			stubJSON(t, w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		var body struct {
			AuthIndex string `json:"auth_index"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		payload, ok := spec.quota[body.AuthIndex]
		if !ok {
			stubJSON(t, w, http.StatusNotImplemented, map[string]any{
				"error": "no quota provider available for credential",
			})
			return
		}
		stubJSON(t, w, http.StatusOK, payload)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func stubJSON(t *testing.T, w http.ResponseWriter, status int, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("写桩响应失败: %v", err)
	}
}

// listCPAAccounts 调一次账号资源接口并解析成结构化结果。
func listCPAAccounts(t *testing.T, server *Server) cpaAccountsReport {
	t.Helper()
	recorder := callCPA(t, server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var report cpaAccountsReport
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	return report
}

// TestListCPAAccountsAggregates 钉住扇出的归一化：被动窗口、现场额度、以及两者谁覆盖谁。
func TestListCPAAccountsAggregates(t *testing.T) {
	stub := cpaStub(t, cpaStubSpec{
		files: []map[string]any{
			{
				"auth_index": "0", "name": "claude-1.json", "provider": "claude", "email": "a@example.com",
				"status": "active", "success": 12, "failed": 1,
				"quota": map[string]any{"signals": map[string]any{
					"Anthropic-Ratelimit-Unified-5h-Utilization": "0.42",
					"Anthropic-Ratelimit-Unified-5h-Reset":       "1800000000",
				}},
			},
			{
				"auth_index": "1", "name": "codex-1.json", "provider": "codex",
				"status": "active", "supports_quota": true, "quota_provider": "codex",
				"account_type": "oauth", "project_id": "proj-1",
				"quota": map[string]any{"signals": map[string]any{
					"X-Codex-Primary-Used-Percent": "58",
				}},
				// 逐模型额度：CPA 只在支持按模型观测的 provider 上给这段，看板据此展开。
				"model_quotas": map[string]any{
					"gpt-6-luna": map[string]any{
						"observed_at": "2026-01-01T00:00:00Z",
						"signals": map[string]any{
							"X-Codex-Primary-Used-Percent":   "90",
							"X-Codex-Primary-Window-Minutes": "300",
						},
					},
				},
				"recent_requests": []map[string]any{
					{"time": "13:20-13:30", "success": 3, "failed": 1},
					{"time": "13:30-13:40", "success": 0, "failed": 0},
				},
			},
			{"auth_index": "2", "name": "gemini-1.json", "provider": "gemini", "status": "active"},
		},
		quota: map[string]any{
			"1": map[string]any{
				"subscription":       map[string]any{"plan": "Pro", "tierId": "pro-tier"},
				"serverTimeOffsetMs": -674,
				"summary": []map[string]any{
					{"key": "credits", "label": "剩余积分", "value": 12.5, "unit": "credit"},
				},
				"groups": []any{map[string]any{
					"displayName": "ChatGPT",
					"buckets": []any{map[string]any{
						"window":            "primary",
						"remainingFraction": 0.25,
						"resetTime":         "2027-01-15T08:00:00Z",
						"description":       "5 小时窗口",
					}},
				}},
			},
		},
	})

	server := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+stub.URL+`","management_key":"mk-1"}}`)
	report := listCPAAccounts(t, server)

	if len(report.Instances) != 1 {
		t.Fatalf("期望 1 个实例，实际 %d", len(report.Instances))
	}
	instance := report.Instances[0]
	if !instance.OK || instance.Error != "" {
		t.Fatalf("实例应读取成功: %+v", instance)
	}
	if instance.ObservedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("应透传 CPA 的观测时间，实际 %q", instance.ObservedAt)
	}
	if len(instance.Accounts) != 3 {
		t.Fatalf("期望 3 个账号，实际 %d", len(instance.Accounts))
	}
	if report.FetchedAt == "" {
		t.Error("响应缺少 fetched_at")
	}

	byName := map[string]cpaAccount{}
	for _, account := range instance.Accounts {
		byName[account.Name] = account
	}

	// claude：没有额度提供者，只有被动信号——这正是多数 CPA 安装的真实状态。
	claude := byName["claude-1.json"]
	if len(claude.Windows) != 1 || claude.Windows[0].Source != "passive" || !closeTo(claude.Windows[0].Remaining, 0.58) {
		t.Errorf("claude 应只有被动窗口: %+v", claude.Windows)
	}
	if claude.Email != "a@example.com" || claude.Success != 12 || claude.Failed != 1 {
		t.Errorf("账号读数未透传: %+v", claude)
	}
	if claude.QuotaError != "" {
		t.Errorf("有被动窗口时不该报额度错误: %q", claude.QuotaError)
	}

	// codex：现场额度覆盖被动信号，且窗口来自归一化结果而不是头。
	codex := byName["codex-1.json"]
	if len(codex.Windows) != 1 {
		t.Fatalf("codex 应只有一个现场窗口: %+v", codex.Windows)
	}
	window := codex.Windows[0]
	if window.Source != "quota" || window.Remaining != 0.25 {
		t.Errorf("现场额度应覆盖被动信号: %+v", window)
	}
	// label 取窗口名、上游那句说明单独落在 description。以前 label 优先取 description，
	// 于是同一个东西在上一条账号上显示「5h」、在这一条上显示整句英文说明。
	if window.Label != "primary" || window.Window != "primary" || window.Group != "ChatGPT" {
		t.Errorf("现场窗口应带窗口名与所属分组: %+v", window)
	}
	if window.Description != "5 小时窗口" {
		t.Errorf("上游说明应落在 description 而不是 label: %+v", window)
	}
	if window.ResetAt != "2027-01-15T08:00:00Z" {
		t.Errorf("重置时间未透传: %q", window.ResetAt)
	}
	if codex.Plan != "Pro" || codex.TierID != "pro-tier" {
		t.Errorf("订阅档位未透传: plan=%q tier=%q", codex.Plan, codex.TierID)
	}
	if codex.ServerTimeOffsetMs != -674 {
		t.Errorf("对端时钟偏移未透传: %d", codex.ServerTimeOffsetMs)
	}
	if len(codex.Summary) != 1 || codex.Summary[0].Key != "credits" ||
		!closeTo(codex.Summary[0].Value, 12.5) || codex.Summary[0].Unit != "credit" {
		t.Errorf("额度数值项未透传（含单位）: %+v", codex.Summary)
	}
	if codex.AccountType != "oauth" || codex.ProjectID != "proj-1" {
		t.Errorf("账号类型与项目未透传: %+v", codex)
	}
	if len(codex.RecentRequests) != 2 || codex.RecentRequests[0].Success != 3 || codex.RecentRequests[0].Failed != 1 {
		t.Errorf("最近请求桶未透传: %+v", codex.RecentRequests)
	}
	// 逐模型额度：解析口径与汇总行共用一份代码，因此 90% 已用 → 剩 0.10、标签同样是「5 小时」。
	if len(codex.ModelQuotas) != 1 {
		t.Fatalf("逐模型额度未透传: %+v", codex.ModelQuotas)
	}
	modelQuota, ok := codex.ModelQuotas["gpt-6-luna"]
	if !ok {
		t.Fatalf("逐模型额度缺 gpt-6-luna: %+v", codex.ModelQuotas)
	}
	if modelQuota.ObservedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("逐模型额度应带观测时间: %+v", modelQuota)
	}
	if len(modelQuota.Windows) != 1 || !closeTo(modelQuota.Windows[0].Remaining, 0.10) ||
		modelQuota.Windows[0].Label != "5 小时" || modelQuota.Windows[0].Source != "passive" {
		t.Errorf("逐模型窗口的解析口径应与汇总一致: %+v", modelQuota.Windows)
	}

	// gemini：两条通道都没有，看板要如实显示「没有窗口」，而不是编一个。
	gemini := byName["gemini-1.json"]
	if len(gemini.Windows) != 0 || gemini.QuotaError != "" {
		t.Errorf("既没被动信号也不支持额度探测时不该报错: %+v", gemini)
	}
}

// TestQuotaWindowLabel 钉住现场窗口的标签话术。
//
// 与被动窗口（claude/codex 的响应头）用同一套词：同一页上一条写「5 小时」、另一条写
// 「5h」会让人以为是两种东西。认不出的窗口名原样保留——把季度窗猜成「7 天」比不猜更糟。
func TestQuotaWindowLabel(t *testing.T) {
	cases := map[string]string{
		"5h":            "5 小时",
		"5hours":        "5 小时",
		"weekly":        "7 天",
		"7D":            "7 天",
		"monthly":       "30 天",
		"primary":       "primary",
		"":              "",
		"quarterly-90d": "quarterly-90d",
	}
	for input, want := range cases {
		if got := quotaWindowLabel(input); got != want {
			t.Errorf("quotaWindowLabel(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestListCPAAccountsSummaryWithoutWindows 钉住「只回数值项、不回窗口」的响应也会被采纳。
//
// 计费类额度插件常常只给余额、积分这种不成窗口的量。如果只在「有窗口」时才收下结果，
// 这些读数会被静默丢掉——而它们往往正是那个插件存在的唯一理由。
func TestListCPAAccountsSummaryWithoutWindows(t *testing.T) {
	stub := cpaStub(t, cpaStubSpec{
		files: []map[string]any{
			{
				"auth_index": "1", "name": "claude-1.json", "provider": "claude",
				"status": "active", "supports_quota": true, "quota_provider": "claude",
			},
		},
		quota: map[string]any{
			"1": map[string]any{
				"subscription": map[string]any{"plan": "Team"},
				"summary": []map[string]any{
					{"key": "balance", "label": "余额", "value": 3.5, "unit": "USD", "currency": "USD"},
				},
			},
		},
	})
	server := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+stub.URL+`","management_key":"mk-1"}}`)
	report := listCPAAccounts(t, server)

	if len(report.Instances) != 1 || len(report.Instances[0].Accounts) != 1 {
		t.Fatalf("扇出形状不对: %+v", report.Instances)
	}
	account := report.Instances[0].Accounts[0]
	if account.QuotaError != "" {
		t.Errorf("探到数值项就不该报错: %q", account.QuotaError)
	}
	if len(account.Summary) != 1 || account.Summary[0].Currency != "USD" {
		t.Errorf("只回数值项的响应也应被采纳: %+v", account.Summary)
	}
	if account.Plan != "Team" {
		t.Errorf("数值项响应里的订阅档位也该透传: %q", account.Plan)
	}
	if len(account.Windows) != 0 {
		t.Errorf("没有窗口时不该编一个: %+v", account.Windows)
	}
}

// TestListCPAAccountsWindowsAreAlwaysArrays 钉住「没有窗口」在 JSON 里的形状。
//
// 这个形状必须逐字节固定：encoding/json 把 nil 切片写成 null、把空切片写成 []，而
// windows 由三条不同的路径产出（claude 被动、codex 被动、现场探测），只要有一条返回
// nil，同一份响应里就会同时出现 "windows":[] 与 "windows":null——客户端少一次判空
// 就会炸。本文件第一版正是如此：不认识的 provider 给了 null。
func TestListCPAAccountsWindowsAreAlwaysArrays(t *testing.T) {
	stub := cpaStub(t, cpaStubSpec{
		files: []map[string]any{
			{"auth_index": "0", "name": "gemini-1.json", "provider": "gemini", "status": "active"},
			{"auth_index": "1", "name": "claude-1.json", "provider": "claude", "status": "active",
				"quota": map[string]any{"signals": map[string]any{}}},
		},
	})
	server := cpaServer(t, `{"cpa-a":{"label":"主力","base_url":"`+stub.URL+`","management_key":"mk-1"}}`)

	recorder := callCPA(t, server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, `"windows":null`) {
		t.Errorf("没有额度的账号要给空数组而不是 null：%s", body)
	}
	if got := strings.Count(body, `"windows":[]`); got != 2 {
		t.Errorf("期望两个账号各给一个空数组，实际 %d 个：%s", got, body)
	}
}

// TestListCPAAccountsQuotaErrorOnlyWhenEmpty 钉住额度探测失败时的报错时机：
// 只有「什么都没拿到」才报（对端没装额度插件会回 501，那是常态而不是故障）。
func TestListCPAAccountsQuotaErrorOnlyWhenEmpty(t *testing.T) {
	stub := cpaStub(t, cpaStubSpec{
		files: []map[string]any{
			{
				"auth_index": "0", "name": "codex-with-signals.json", "provider": "codex",
				"supports_quota": true,
				"quota": map[string]any{"signals": map[string]any{
					"X-Codex-Primary-Used-Percent": "58",
				}},
			},
			{"auth_index": "1", "name": "codex-bare.json", "provider": "codex", "supports_quota": true},
		},
		quota: map[string]any{},
	})

	server := cpaServer(t, `{"cpa-a":{"base_url":"`+stub.URL+`","management_key":"mk-1"}}`)
	report := listCPAAccounts(t, server)
	if len(report.Instances) != 1 || len(report.Instances[0].Accounts) != 2 {
		t.Fatalf("扇出结果不完整: %+v", report)
	}
	byName := map[string]cpaAccount{}
	for _, account := range report.Instances[0].Accounts {
		byName[account.Name] = account
	}
	if got := byName["codex-with-signals.json"]; got.QuotaError != "" || len(got.Windows) != 1 {
		t.Errorf("有被动窗口时不该报错: %+v", got)
	}
	bare := byName["codex-bare.json"]
	if len(bare.Windows) != 0 {
		t.Fatalf("没有信号也没有额度插件时不该凭空造窗口: %+v", bare.Windows)
	}
	if !strings.Contains(bare.QuotaError, "501") {
		t.Errorf("应把 501 如实报给用户，实际 %q", bare.QuotaError)
	}
}

// TestListCPAAccountsKeepsInstanceFailuresLocal 钉住故障隔离：一个实例连不上，
// 不能把另一个实例的读数一起带走——这正是同时管多个 CPA 时最需要的行为。
func TestListCPAAccountsKeepsInstanceFailuresLocal(t *testing.T) {
	broken := cpaStub(t, cpaStubSpec{}) // files 为 nil -> auth-files 回 500
	healthy := cpaStub(t, cpaStubSpec{
		files: []map[string]any{{"auth_index": "0", "name": "ok.json", "provider": "claude"}},
	})

	server := cpaServer(t, `{
		"broken":{"label":"坏掉的","base_url":"`+broken.URL+`","management_key":"mk-1"},
		"healthy":{"label":"好的","base_url":"`+healthy.URL+`","management_key":"mk-1"},
		"incomplete":{"label":"没填完","base_url":"","management_key":""}
	}`)
	report := listCPAAccounts(t, server)

	byID := map[string]cpaInstanceAccountReport{}
	for _, instance := range report.Instances {
		byID[instance.ID] = instance
	}
	if len(byID) != 3 {
		t.Fatalf("期望 3 个实例，实际 %d", len(byID))
	}
	if got := byID["broken"]; got.OK || !strings.Contains(got.Error, "HTTP 500") {
		t.Errorf("坏实例应报出对端状态码: %+v", got)
	}
	if got := byID["incomplete"]; got.OK || !strings.Contains(got.Error, "management_key") {
		t.Errorf("残缺实例应就地报错: %+v", got)
	}
	healthyReport := byID["healthy"]
	if !healthyReport.OK || len(healthyReport.Accounts) != 1 || healthyReport.Accounts[0].Name != "ok.json" {
		t.Errorf("好实例的读数不该受牵连: %+v", healthyReport)
	}
}

// TestListCPAAccountsRejectsWrongCredential 钉住凭据确实发出去了：桩只认
// `Bearer mk-1`，配置里写错 key 时实例块必须报 401，而不是静默回一份空清单。
func TestListCPAAccountsRejectsWrongCredential(t *testing.T) {
	stub := cpaStub(t, cpaStubSpec{
		files: []map[string]any{{"auth_index": "0", "name": "ok.json", "provider": "claude"}},
	})
	server := cpaServer(t, `{"cpa-a":{"base_url":"`+stub.URL+`","management_key":"wrong-key"}}`)

	instance := listCPAAccounts(t, server).Instances[0]
	if instance.OK || !strings.Contains(instance.Error, "HTTP 401") {
		t.Errorf("凭据不对时应报 401: %+v", instance)
	}
}
