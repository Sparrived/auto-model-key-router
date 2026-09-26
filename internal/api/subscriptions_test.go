package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 订阅账号资源（subscriptions.go）的用例。
//
// 这一层的判据集中在三件事上，因为它们的错误都不报错、只显示成"看起来合法的假数据"：
//
//  1. **谁会被派生出来**。多派生一条 = 拿别家端点的 key 去问，401 会被读成"凭据坏了"；
//     少派生一条 = 用户明明配了订阅却看不见。
//  2. **已用百分数怎么翻成剩余比例**。翻反了会把"快用尽"显示成"几乎没用"，
//     而这是这一页存在的唯一理由。
//  3. **缺字段时不编数**。上游没给比例/上限时，少一个窗口好过画一个 0% 或 100%。

// —— 桩件 ——

// requestLog 并发安全地记下每次请求的 (方法, 路径)。
type requestLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *requestLog) add(request *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, request.Method+" "+request.URL.Path)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

func (l *requestLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// stubSubscriptions 把订阅 HTTP 客户端换成一个本地桩。
//
// 按**路径**分发而不是按主机：厂商是认 base_url 的主机名认出来的（opencode.ai /
// api.commandcode.ai），所以用例里的 base_url 必须是真主机名；这里把请求重定向到
// httptest，于是识别逻辑与请求逻辑各测各的，互不迁就。
func stubSubscriptions(t *testing.T, handler http.HandlerFunc) *requestLog {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	log := &requestLog{}
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("解析桩地址失败: %v", err)
	}
	original := subscriptionHTTPClient
	t.Cleanup(func() { subscriptionHTTPClient = original })
	subscriptionHTTPClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			log.add(request)
			clone := request.Clone(request.Context())
			clone.URL.Scheme = target.Scheme
			clone.URL.Host = target.Host
			clone.Host = ""
			return http.DefaultTransport.RoundTrip(clone)
		}),
	}
	return log
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// noSleep 让重试用例不必真的等够退避时间。
func noSleep(t *testing.T) {
	t.Helper()
	original := subscriptionSleep
	t.Cleanup(func() { subscriptionSleep = original })
	subscriptionSleep = func(time.Duration) {}
}

// accountsServer 装配一个带供应商配置的 Server。
func accountsServer(t *testing.T, providersJSON string) *Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")
	fixture := `{"config_version":4,"local_api_key":"local-key","ops_enabled":false,` +
		`"webui_enabled":false,"cpa_instances":{},"providers":` + providersJSON + `}`
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return &Server{ConfigPath: path}
}

// readAccounts 调一次 /api/cpa-accounts 并解析成结构化结果。
func readAccounts(t *testing.T, server *Server) cpaAccountsReport {
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

// —— 派生规则 ——

// TestSubscriptionTargetsDerivesFromProviders 钉住"哪些供应商会产生订阅条目"。
//
// 每一条挡住的都是一个会被误读的失败：Zen 的 key 拿去问 Go 会得到 403（被读成"订阅过期"），
// 停用的 key 会产生一批"有额度但根本没在用"的条目。
func TestSubscriptionTargetsDerivesFromProviders(t *testing.T) {
	server := accountsServer(t, `{
		"go-a": {"base_url": "https://opencode.ai/zen/go/v1", "keys": {
			"key-1": {"api_key": "sk-1"},
			"key-2": {"api_key": "sk-2"},
			"key-off": {"api_key": "sk-3", "enabled": false},
			"key-empty": {"api_key": ""}
		}},
		"zen": {"base_url": "https://opencode.ai/zen/v1", "keys": {"key-1": {"api_key": "sk-z"}}},
		"cc": {"base_url": "https://api.commandcode.ai/provider", "keys": {"key-1": {"api_key": "user_1"}}},
		"deepseek": {"base_url": "https://api.deepseek.com", "keys": {"key-1": {"api_key": "sk-d"}}}
	}`)
	data := readConfigData(t, server.ConfigPath)
	cfg, err := config.FromDict(data)
	if err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}

	targets := subscriptionTargets(cfg)
	got := make([]string, 0, len(targets))
	for _, target := range targets {
		got = append(got, target.Vendor.Kind+"|"+target.ID)
	}
	want := []string{"opencode-go|go-a/key-1", "opencode-go|go-a/key-2", "commandcode|cc/key-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("派生的订阅条目不对\n实际 %v\n期望 %v", got, want)
	}
	// 端点必须是"主机根"那个：Command Code 的 /alpha/* 挂在根下，供应商 base_url 里
	// 的 /provider 是推理路径，拼上去就问错地方了。
	for _, target := range targets {
		if target.Vendor.Kind == "commandcode" && target.Endpoint.origin != "https://api.commandcode.ai" {
			t.Fatalf("Command Code 应取主机根，实际 %s", target.Endpoint.origin)
		}
	}
}

// TestSubscriptionTargetsDisappearWithProvider 钉住"随供应商删除而消失"。
//
// 派生条目不落任何配置键，所以删除供应商与看板上消失是同一件事的两个面——这里断言
// 删掉配置里的供应商之后，条目（以及它的 key）一个都不剩，且不需要任何清理钩子。
func TestSubscriptionTargetsDisappearWithProvider(t *testing.T) {
	withProvider := accountsServer(t, `{
		"go-a": {"base_url": "https://opencode.ai/zen/go/v1", "keys": {"key-1": {"api_key": "sk-1"}}}
	}`)
	removed := accountsServer(t, `{}`)

	for name, server := range map[string]*Server{"配了供应商": withProvider, "删掉供应商": removed} {
		data := readConfigData(t, server.ConfigPath)
		cfg, err := config.FromDict(data)
		if err != nil {
			t.Fatalf("%s：解析配置失败: %v", name, err)
		}
		count := len(subscriptionTargets(cfg))
		want := 0
		if name == "配了供应商" {
			want = 1
		}
		if count != want {
			t.Fatalf("%s：期望 %d 条订阅，实际 %d", name, want, count)
		}
	}
}

// TestParseSubscriptionEndpoint 钉住 base_url 拆解：主机小写、忽略端口、保留路径。
func TestParseSubscriptionEndpoint(t *testing.T) {
	cases := []struct {
		baseURL string
		host    string
		origin  string
		path    string
		wantErr bool
	}{
		{baseURL: "https://opencode.ai/zen/go/v1", host: "opencode.ai", origin: "https://opencode.ai", path: "/zen/go/v1"},
		{baseURL: "https://opencode.ai/zen/go/v1/", host: "opencode.ai", origin: "https://opencode.ai", path: "/zen/go/v1"},
		{baseURL: "HTTPS://OpenCode.AI:443/zen/go/v1", host: "opencode.ai", origin: "https://opencode.ai:443", path: "/zen/go/v1"},
		{baseURL: "opencode.ai/zen/go", wantErr: true},
		{baseURL: "https://", wantErr: true},
	}
	for _, testCase := range cases {
		endpoint, err := parseSubscriptionEndpoint(testCase.baseURL)
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("%s：期望报错，实际 %+v", testCase.baseURL, endpoint)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s：不期望报错: %v", testCase.baseURL, err)
		}
		if endpoint.host != testCase.host || endpoint.origin != testCase.origin || endpoint.path != testCase.path {
			t.Fatalf("%s：拆解结果 %+v", testCase.baseURL, endpoint)
		}
	}
}

// TestSubscriptionVendorMatching 钉住厂商识别：Go 认路径、Command Code 只认主机。
func TestSubscriptionVendorMatching(t *testing.T) {
	cases := []struct {
		baseURL string
		kind    string
	}{
		{baseURL: "https://opencode.ai/zen/go/v1", kind: "opencode-go"},
		{baseURL: "https://opencode.ai/zen/go", kind: "opencode-go"},
		// Zen 与别的路径都不出条目：这里认错等于拿错的 key 去问对的端点。
		{baseURL: "https://opencode.ai/zen/v1", kind: ""},
		{baseURL: "https://opencode.ai", kind: ""},
		{baseURL: "https://api.commandcode.ai", kind: "commandcode"},
		{baseURL: "https://api.commandcode.ai/provider", kind: "commandcode"},
		{baseURL: "https://api.commandcode.ai/v1", kind: "commandcode"},
		// 只按主机后缀匹配会误伤：commandcode.ai.example.com 不是它家。
		{baseURL: "https://api.commandcode.ai.example.com", kind: ""},
		{baseURL: "https://opencode.ai.example.com/zen/go/v1", kind: ""},
	}
	for _, testCase := range cases {
		endpoint, err := parseSubscriptionEndpoint(testCase.baseURL)
		if err != nil {
			t.Fatalf("%s：解析失败: %v", testCase.baseURL, err)
		}
		vendor, ok := subscriptionVendorFor(endpoint)
		got := ""
		if ok {
			got = vendor.Kind
		}
		if got != testCase.kind {
			t.Fatalf("%s：期望 %q，实际 %q", testCase.baseURL, testCase.kind, got)
		}
	}
}

// —— OpenCode Go ——

// TestOpenCodeGoReading 钉住"已用百分数 → 剩余比例"以及缺字段时不编数。
func TestOpenCodeGoReading(t *testing.T) {
	resetAt := time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339)
	payload := openCodeGoUsage{}
	body := fmt.Sprintf(`{"usage":{
		"rolling":{"status":"ok","percent":42,"resetsAt":%q},
		"weekly":{"status":"rate-limited","percent":100,"resetsAt":%q},
		"monthly":{"status":"ok","percent":0}
	}}`, resetAt, resetAt)
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析桩响应失败: %v", err)
	}

	reading := openCodeGoReading(payload)
	if len(reading.Windows) != 3 {
		t.Fatalf("期望 3 个窗口，实际 %d（%+v）", len(reading.Windows), reading.Windows)
	}
	rolling := reading.Windows[0]
	if rolling.Key != "opencode-go/rolling" || rolling.Label != "5 小时" || rolling.Window != "5h" {
		t.Fatalf("rolling 窗口的键/标签不对: %+v", rolling)
	}
	// 42% 已用 → 58% 剩余。这是整页最要紧的一次翻转。
	if !closeTo(rolling.Remaining, 0.58) {
		t.Fatalf("remaining 应为 0.58（已用 42%%），实际 %v", rolling.Remaining)
	}
	if rolling.Source != "quota" {
		t.Fatalf("来源应为现场查询 quota，实际 %q", rolling.Source)
	}
	if rolling.ResetAt != resetAt {
		t.Fatalf("重置时间应为 %q，实际 %q", resetAt, rolling.ResetAt)
	}
	// rate-limited 是"已用尽"的另一种说法，要能一眼看出来。
	if reading.Windows[1].Status != "rejected" || !closeTo(reading.Windows[1].Remaining, 0) {
		t.Fatalf("rate-limited 窗口应为 rejected 且剩 0: %+v", reading.Windows[1])
	}
	// 0 是合法读数（这个窗口一点没用），不能被当成缺失丢掉。
	if !closeTo(reading.Windows[2].Remaining, 1) {
		t.Fatalf("percent 为 0 的窗口应剩 100%%，实际 %v", reading.Windows[2].Remaining)
	}
}

// TestOpenCodeGoReadingSkipsMissingWindows 钉住"上游没给就不画"。
//
// 编一个 100% 的窗口比少画一个更糟：那等于替上游保证额度还在。
func TestOpenCodeGoReadingSkipsMissingWindows(t *testing.T) {
	payload := openCodeGoUsage{}
	if err := json.Unmarshal([]byte(`{"usage":{"weekly":{"status":"ok","percent":10}}}`), &payload); err != nil {
		t.Fatalf("解析桩响应失败: %v", err)
	}
	reading := openCodeGoReading(payload)
	if len(reading.Windows) != 1 || reading.Windows[0].Key != "opencode-go/weekly" {
		t.Fatalf("只应产出 weekly 一个窗口，实际 %+v", reading.Windows)
	}

	// 完全没有 usage 段：一个窗口都不该产出（而不是三个 0%）。
	empty := openCodeGoReading(openCodeGoUsage{})
	if len(empty.Windows) != 0 {
		t.Fatalf("空响应不应产出窗口，实际 %+v", empty.Windows)
	}
}

// TestOpenCodeGoAcceptsResetInSecAndAliases 钉住窗口读数的别名容错。
func TestOpenCodeGoAcceptsResetInSecAndAliases(t *testing.T) {
	payload := openCodeGoUsage{}
	body := `{"rollingUsage":{"usagePercent":25,"resetInSec":600},"usage":{"monthly":{"percent":50,"resetAt":"2030-01-02T03:04:05Z"}}}`
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析桩响应失败: %v", err)
	}
	reading := openCodeGoReading(payload)
	if len(reading.Windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %+v", reading.Windows)
	}
	if !closeTo(reading.Windows[0].Remaining, 0.75) {
		t.Fatalf("rollingUsage 别名应剩 0.75，实际 %v", reading.Windows[0].Remaining)
	}
	if reading.Windows[0].ResetAt == "" {
		t.Fatalf("resetInSec 应折算成绝对时间，实际为空")
	}
	if reading.Windows[0].ResetAt != "" && !strings.HasPrefix(reading.Windows[0].ResetAt, "20") {
		t.Fatalf("resetInSec 折算结果不像 RFC3339: %q", reading.Windows[0].ResetAt)
	}
	if reading.Windows[1].ResetAt != "2030-01-02T03:04:05Z" {
		t.Fatalf("monthly 的重置时间不对: %q", reading.Windows[1].ResetAt)
	}
}

// TestCollectOpenCodeGoRetriesUnavailable 钉住那个 503 抖动会被重试。
//
// 端点会无规律地回 503，与凭据无关；不重试的话看板会经常显示"读取失败"，
// 而那并不是用户的订阅出了问题。
func TestCollectOpenCodeGoRetriesUnavailable(t *testing.T) {
	noSleep(t)
	// log 要先声明再赋值：handler 闭包里要读它，而 stubSubscriptions 的返回值
	// 在同一个语句里还不可见。
	var log *requestLog
	log = stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-1" {
			t.Errorf("Authorization 头不对: %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != subscriptionUserAgent {
			t.Errorf("User-Agent 头不对: %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept 头不对: %q", got)
		}
		if r.URL.Path != "/zen/go/v1/usage" {
			t.Errorf("用量路径不对: %q", r.URL.Path)
		}
		// 前两次回 503（端点会无规律地这样），第三次给结果。
		if log.count() <= 2 {
			stubJSON(t, w, http.StatusServiceUnavailable, map[string]any{"message": "Go usage is unavailable"})
			return
		}
		stubJSON(t, w, http.StatusOK, map[string]any{"usage": map[string]any{
			"rolling": map[string]any{"status": "ok", "percent": 10},
		}})
	})

	entry := collectSubscription(t.Context(), openCodeGoTarget())
	if !entry.OK {
		t.Fatalf("重试后应成功，实际失败: %s", entry.Error)
	}
	if log.count() != 3 {
		t.Fatalf("期望尝试 3 次（两次 503 后成功），实际 %d", log.count())
	}
	if len(entry.Windows) != 1 || !closeTo(entry.Windows[0].Remaining, 0.9) {
		t.Fatalf("窗口不对: %+v", entry.Windows)
	}
}

// TestCollectOpenCodeGoAuthErrors 钉住两种"凭据类"失败各自的说法。
//
// 401 与 403 的含义完全不同（key 不对 / 工作区没订阅），折叠成"HTTP 401/403"等于把
// 最有用的一句话丢掉；而这两种都不该重试。
func TestCollectOpenCodeGoAuthErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		payload any
		want    string
	}{
		{
			name:   "401",
			status: http.StatusUnauthorized,
			payload: map[string]any{"type": "error", "error": map[string]any{
				"type": "AuthError", "message": "Unauthorized",
			}},
			want: "Unauthorized",
		},
		{
			name:   "403",
			status: http.StatusForbidden,
			payload: map[string]any{"type": "error", "error": map[string]any{
				"type": "EntitlementError", "message": "OpenCode Go subscription required.",
			}},
			want: "OpenCode Go subscription required.",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			noSleep(t)
			log := stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
				stubJSON(t, w, testCase.status, testCase.payload)
			})
			entry := collectSubscription(t.Context(), openCodeGoTarget())
			if entry.OK {
				t.Fatalf("凭据类失败不该算成功")
			}
			if !strings.Contains(entry.Error, testCase.want) {
				t.Fatalf("错误文本应含 %q，实际 %q", testCase.want, entry.Error)
			}
			// 401/403 重试没有意义：结果不会变，只白等退避时间。
			if log.count() != 1 {
				t.Fatalf("凭据类失败不应重试，实际请求 %d 次", log.count())
			}
			if len(entry.Windows) != 0 {
				t.Fatalf("失败条目不应有窗口: %+v", entry.Windows)
			}
		})
	}
}

// openCodeGoTarget 构造一个 OpenCode Go 的待采集目标。
func openCodeGoTarget() subscriptionTarget {
	endpoint, _ := parseSubscriptionEndpoint("https://opencode.ai/zen/go/v1")
	vendor, _ := subscriptionVendorFor(endpoint)
	return subscriptionTarget{
		ID: "go-a/key-1", Vendor: vendor, ProviderID: "go-a", KeyName: "key-1",
		Endpoint: endpoint, APIKey: "sk-1",
	}
}

// —— Command Code ——

// TestCommandCodePlanLongestPrefixWins 钉住档位映射：长前缀优先。
//
// 手写顺序错了不会有任何报错，只会把 80 的档显示成 30 的档。
func TestCommandCodePlanLongestPrefixWins(t *testing.T) {
	cases := map[string]string{
		"individual-pro-v1":   "Pro",
		"individual-pro":      "Pro",
		"individual-go":       "Go",
		"individual-goat":     "GOAT",
		"individual-max":      "Max",
		"individual-ultra":    "Ultra",
		"individual-provider": "Provider",
		"teams-pro":           "Teams Pro",
		"individual_pro":      "Pro",
		"unknown-plan":        "",
	}
	for planID, want := range cases {
		got, _ := commandCodePlan(planID)
		if got != want {
			t.Fatalf("%s：期望 %q，实际 %q", planID, want, got)
		}
	}
	// 一条容易写错的具体断言：goat 不能被 go 吃掉。
	if got, _ := commandCodePlan("individual-goat"); got != "GOAT" {
		t.Fatalf("individual-goat 被更短的前缀吃掉了：%q", got)
	}
}

// TestCollectCommandCodeFull 钉住完整响应下的归一化。
func TestCollectCommandCodeFull(t *testing.T) {
	resetResetAt := time.Now().Add(4 * time.Hour).UnixMilli()
	stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/whoami":
			stubJSON(t, w, http.StatusOK, map[string]any{
				"user": map[string]any{"id": "u1", "name": "小明", "userName": "xiaoming"},
			})
		case "/alpha/billing/credits":
			stubJSON(t, w, http.StatusOK, map[string]any{
				"credits": map[string]any{
					"monthlyCredits": 7.5, "purchasedCredits": 3, "freeCredits": 1, "planId": "individual-goat",
				},
				"windowLimits": map[string]any{
					"limited":  true,
					"fiveHour": map[string]any{"used": 2, "cap": 8, "resetAt": resetResetAt},
					"weekly":   map[string]any{"used": 10, "cap": 10, "exceeded": true},
				},
			})
		case "/alpha/billing/subscriptions":
			stubJSON(t, w, http.StatusOK, map[string]any{
				"success": true,
				"data": map[string]any{
					"planId": "individual-goat", "status": "active",
					"currentPeriodEnd": "2030-01-02T03:04:05Z", "cancelAtPeriodEnd": true,
				},
			})
		case "/alpha/usage/summary":
			stubJSON(t, w, http.StatusOK, map[string]any{
				"totalCount": 120, "failedCount": 3, "totalTokens": 456789, "totalCost": 12.25,
			})
		default:
			stubJSON(t, w, http.StatusNotFound, map[string]any{"error": "not found"})
		}
	})

	entry := collectSubscription(t.Context(), commandCodeTarget())
	if !entry.OK {
		t.Fatalf("完整响应应成功，实际失败: %s", entry.Error)
	}
	if entry.Vendor != "Command Code" || entry.Kind != "commandcode" {
		t.Fatalf("厂商标识不对: %q / %q", entry.Vendor, entry.Kind)
	}
	if entry.Account != "xiaoming" {
		t.Fatalf("账号名应为 xiaoming，实际 %q", entry.Account)
	}
	if entry.Plan != "GOAT" || entry.TierID != "individual-goat" {
		t.Fatalf("档位不对: plan=%q tier=%q", entry.Plan, entry.TierID)
	}

	if len(entry.Windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %+v", entry.Windows)
	}
	// 2/8 已用 → 75% 剩余。
	if !closeTo(entry.Windows[0].Remaining, 0.75) {
		t.Fatalf("5 小时窗口应剩 0.75，实际 %v", entry.Windows[0].Remaining)
	}
	// 毫秒级 resetAt 必须认出来：当成秒会得到公元五万年，界面上成了一条永不重置的额度。
	if entry.Windows[0].ResetAt == "" || !strings.HasPrefix(entry.Windows[0].ResetAt, "20") {
		t.Fatalf("毫秒 resetAt 没被正确解析: %q", entry.Windows[0].ResetAt)
	}
	// 用尽的那一档要能一眼看出来。
	if entry.Windows[1].Status != "rejected" || !closeTo(entry.Windows[1].Remaining, 0) {
		t.Fatalf("weekly 应已用尽: %+v", entry.Windows[1])
	}

	// 三项积分分开显示：合成一个总额就分不出哪一份先没了。
	// 按 key 断言而不是按单位筛：总花费的单位也是 credit，用单位筛会把两项混在一起。
	byKey := map[string]cpaQuotaMetric{}
	for _, metric := range entry.Summary {
		byKey[metric.Key] = metric
	}
	for key, want := range map[string]struct {
		label string
		value float64
		unit  string
	}{
		"monthlyCredits":   {label: "订阅积分", value: 7.5, unit: "credit"},
		"purchasedCredits": {label: "已购积分", value: 3, unit: "credit"},
		"freeCredits":      {label: "赠送积分", value: 1, unit: "credit"},
		"totalCount":       {label: "请求数", value: 120, unit: "次"},
		"totalTokens":      {label: "总 token", value: 456789, unit: "token"},
	} {
		metric, ok := byKey[key]
		if !ok {
			t.Fatalf("缺少数值项 %s：%+v", key, entry.Summary)
		}
		if metric.Label != want.label || metric.Unit != want.unit || !closeTo(metric.Value, want.value) {
			t.Fatalf("%s 的读数不对: %+v", key, metric)
		}
	}
	if entry.Signals["status"] != "active" || entry.Signals["cancelAtPeriodEnd"] != "true" {
		t.Fatalf("signals 不对: %+v", entry.Signals)
	}
}

// TestCollectCommandCodePartialFailure 钉住"各自容错"：一条路由挂了不影响其它。
//
// 这些是各自独立的内部接口，一起挂掉的概率远低于其中一个漂移；把局部失败升级成整条
// 失败，会让一次无关紧要的字段改名把整个看板区块打掉。
func TestCollectCommandCodePartialFailure(t *testing.T) {
	stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/alpha/usage/summary" {
			stubJSON(t, w, http.StatusInternalServerError, map[string]any{"error": "boom"})
			return
		}
		switch r.URL.Path {
		case "/alpha/whoami":
			stubJSON(t, w, http.StatusOK, map[string]any{"user": map[string]any{"userName": "xiaoming"}})
		case "/alpha/billing/credits":
			stubJSON(t, w, http.StatusOK, map[string]any{
				"credits":      map[string]any{"monthlyCredits": 5},
				"windowLimits": map[string]any{"fiveHour": map[string]any{"used": 1, "cap": 4}},
			})
		case "/alpha/billing/subscriptions":
			stubJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{"planId": "individual-go", "status": "active"}})
		default:
			stubJSON(t, w, http.StatusNotFound, map[string]any{"error": "not found"})
		}
	})

	entry := collectSubscription(t.Context(), commandCodeTarget())
	if !entry.OK {
		t.Fatalf("只有一条路由挂掉时条目仍应成功，实际失败: %s", entry.Error)
	}
	if entry.Plan != "Go" {
		t.Fatalf("档位应为 Go，实际 %q", entry.Plan)
	}
	if len(entry.Windows) != 1 || !closeTo(entry.Windows[0].Remaining, 0.75) {
		t.Fatalf("窗口不对: %+v", entry.Windows)
	}
	// 失败的那一条要留下来，否则"少了一部分读数"看起来会像"上游本来就没有"。
	if !strings.Contains(entry.Signals["读取失败"], "usage/summary") {
		t.Fatalf("局部失败应记进 signals: %+v", entry.Signals)
	}
}

// TestCollectCommandCodeAllFailures 钉住"四样全挂才算失败"，且错误里能看出是网络不通。
func TestCollectCommandCodeAllFailures(t *testing.T) {
	log := stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
		stubJSON(t, w, http.StatusInternalServerError, map[string]any{"error": "boom"})
	})
	entry := collectSubscription(t.Context(), commandCodeTarget())
	if entry.OK {
		t.Fatalf("全部失败时不该算成功")
	}
	for _, path := range []string{"/alpha/whoami", "/alpha/billing/credits", "/alpha/billing/subscriptions", "/alpha/usage/summary"} {
		if !strings.Contains(entry.Error, strings.TrimPrefix(path, "/alpha/")) {
			t.Fatalf("错误里应逐条列出失败的端点（缺 %s）：%s", path, entry.Error)
		}
	}
	// 5xx 值得重试，所以请求数会多于 4 次。
	if log.count() < 4 {
		t.Fatalf("期望至少问到 4 个端点，实际 %d", log.count())
	}
}

// TestCollectCommandCodeRejectsInvalidKey 钉住"whoami 就 401 时不再问其余三条"。
func TestCollectCommandCodeRejectsInvalidKey(t *testing.T) {
	noSleep(t)
	log := stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
		stubJSON(t, w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
	})
	entry := collectSubscription(t.Context(), commandCodeTarget())
	if entry.OK {
		t.Fatalf("凭据无效不该算成功")
	}
	if !strings.Contains(entry.Error, "commandcode.ai 的账号 key") {
		t.Fatalf("错误文本应给出下一步，实际 %q", entry.Error)
	}
	if log.count() != 1 {
		t.Fatalf("凭据无效时只应问一次 whoami，实际 %d 次", log.count())
	}
}

// TestCommandCodeWindowsSkipUnusableCaps 钉住"算不出剩余就不画窗口"。
//
// cap 为 0 时 used/cap 会算出 +Inf 或负数，画到环上不是 0% 就是 100%，两个都是编数据。
func TestCommandCodeWindowsSkipUnusableCaps(t *testing.T) {
	cases := []struct {
		name   string
		limits map[string]any
		want   int
	}{
		{name: "cap 为 0", limits: map[string]any{"fiveHour": map[string]any{"used": 3, "cap": 0}}, want: 0},
		{name: "缺 cap", limits: map[string]any{"fiveHour": map[string]any{"used": 3}}, want: 0},
		{name: "缺 used", limits: map[string]any{"fiveHour": map[string]any{"cap": 4}}, want: 0},
		{name: "只有 weekly", limits: map[string]any{"weekly": map[string]any{"used": 1, "cap": 2}}, want: 1},
		{name: "snake_case 别名", limits: map[string]any{"five_hour": map[string]any{"used_credits": 1, "cap_credits": 4}}, want: 1},
		{
			name:   "used 超过 cap 夹到 0",
			limits: map[string]any{"fiveHour": map[string]any{"used": 9, "cap": 4}},
			want:   1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			windows := commandCodeWindows(testCase.limits)
			if len(windows) != testCase.want {
				t.Fatalf("期望 %d 个窗口，实际 %+v", testCase.want, windows)
			}
			// 超过 cap 时剩余必须夹在 0：进度条宽度与告警分界都按 0..1 算。
			if testCase.name == "used 超过 cap 夹到 0" && !closeTo(windows[0].Remaining, 0) {
				t.Fatalf("remaining 应夹到 0，实际 %v", windows[0].Remaining)
			}
		})
	}
}

// commandCodeTarget 构造一个 Command Code 的待采集目标。
func commandCodeTarget() subscriptionTarget {
	endpoint, _ := parseSubscriptionEndpoint("https://api.commandcode.ai/provider")
	vendor, _ := subscriptionVendorFor(endpoint)
	return subscriptionTarget{
		ID: "cc/key-1", Vendor: vendor, ProviderID: "cc", KeyName: "key-1",
		Endpoint: endpoint, APIKey: "user_1",
	}
}

// —— 整块看板 ——

// TestAccountsReportIncludesSubscriptions 钉住订阅进了同一份响应。
//
// 与 Instances 同一个 fetched_at、同一次扇出：分两个接口取会出现两个时间戳，
// 而用户看到的是同一屏数字。
func TestAccountsReportIncludesSubscriptions(t *testing.T) {
	stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
		stubJSON(t, w, http.StatusOK, map[string]any{"usage": map[string]any{
			"rolling": map[string]any{"status": "ok", "percent": 30},
		}})
	})
	server := accountsServer(t, `{
		"go-a": {"base_url": "https://opencode.ai/zen/go/v1", "keys": {"key-1": {"api_key": "sk-1"}}}
	}`)
	report := readAccounts(t, server)

	if report.FetchedAt == "" {
		t.Fatalf("fetched_at 不应为空")
	}
	if len(report.Subscriptions) != 1 {
		t.Fatalf("期望 1 条订阅，实际 %+v", report.Subscriptions)
	}
	entry := report.Subscriptions[0]
	if entry.ID != "go-a/key-1" || entry.ProviderID != "go-a" || entry.KeyName != "key-1" {
		t.Fatalf("条目的出处不对: %+v", entry)
	}
	if !entry.OK || !closeTo(entry.Windows[0].Remaining, 0.7) {
		t.Fatalf("读数不对: %+v", entry)
	}
	// CPA 那半边是空的，但必须是空数组而不是 null。
	if report.Instances == nil {
		t.Fatalf("instances 应为空数组，实际 null")
	}
}

// TestAccountsReportSubscriptionsAlwaysArrays 钉住订阅条目的数组形状。
//
// nil 会被写成 null，同一份响应里出现 `"windows":[]` 与 `"windows":null` 两种形状时，
// 客户端少一次判空就会炸。
func TestAccountsReportSubscriptionsAlwaysArrays(t *testing.T) {
	stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
		stubJSON(t, w, http.StatusOK, map[string]any{"usage": map[string]any{}})
	})
	server := accountsServer(t, `{
		"go-a": {"base_url": "https://opencode.ai/zen/go/v1", "keys": {"key-1": {"api_key": "sk-1"}}},
		"deepseek": {"base_url": "https://api.deepseek.com", "keys": {"key-1": {"api_key": "sk-d"}}}
	}`)
	recorder := callCPA(t, server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, `"windows":null`) {
		t.Fatalf("windows 不该是 null: %s", body)
	}
	if !strings.Contains(body, `"subscriptions":[`) {
		t.Fatalf("subscriptions 应为数组: %s", body)
	}
	// 没有订阅的配置也要给空数组：界面上"没有订阅"与"字段缺失"必须能分开。
	plain := accountsServer(t, `{"deepseek": {"base_url": "https://api.deepseek.com", "keys": {"key-1": {"api_key": "sk-d"}}}}`)
	plainBody := callCPA(t, plain, http.MethodGet, "/api/cpa-accounts", "").Body.String()
	if !strings.Contains(plainBody, `"subscriptions":[]`) {
		t.Fatalf("无订阅时 subscriptions 应为空数组: %s", plainBody)
	}
}

// TestSubscriptionFailureIsLocal 钉住一条订阅的失败不影响别的条目，也不影响 CPA 那一半。
func TestSubscriptionFailureIsLocal(t *testing.T) {
	stubSubscriptions(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "sk-bad") {
			stubJSON(t, w, http.StatusUnauthorized, map[string]any{
				"error": map[string]any{"type": "AuthError", "message": "Unauthorized"},
			})
			return
		}
		stubJSON(t, w, http.StatusOK, map[string]any{"usage": map[string]any{
			"rolling": map[string]any{"status": "ok", "percent": 10},
		}})
	})
	server := accountsServer(t, `{
		"go-a": {"base_url": "https://opencode.ai/zen/go/v1", "keys": {
			"key-good": {"api_key": "sk-good"},
			"key-bad": {"api_key": "sk-bad"}
		}}
	}`)
	report := readAccounts(t, server)
	if len(report.Subscriptions) != 2 {
		t.Fatalf("期望 2 条订阅，实际 %+v", report.Subscriptions)
	}
	byID := map[string]subscriptionEntry{}
	for _, entry := range report.Subscriptions {
		byID[entry.ID] = entry
	}
	good, bad := byID["go-a/key-good"], byID["go-a/key-bad"]
	if !good.OK {
		t.Fatalf("好 key 应成功: %+v", good)
	}
	if bad.OK || !strings.Contains(bad.Error, "Unauthorized") {
		t.Fatalf("坏 key 的错应只落在自己身上: %+v", bad)
	}
	if len(bad.Windows) != 0 {
		t.Fatalf("坏 key 不该有窗口: %+v", bad.Windows)
	}
}

// TestAccountsReportRejectsBrokenProvidersSection 钉住供应商段坏掉时整块看板一起失败。
//
// 这里刻意**不**做「订阅区降级、CPA 照常显示」：配置是鉴权的输入，供应商段解析不了时
// authorizedConfig 就已经失败了——能走到扇出这一步，说明配置是好的。真要做降级，
// 前提是绕开鉴权去读一份坏配置，那等于把「配置坏了」这条最该被看见的失败藏起来。
func TestAccountsReportRejectsBrokenProvidersSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")
	// keys 里的 api_key 缺失：config.FromDict 会报错（Python 侧是 KeyError）。
	fixture := `{"config_version":4,"local_api_key":"local-key","ops_enabled":false,"webui_enabled":false,` +
		`"cpa_instances":{},"providers":{"p":{"base_url":"https://api.openai.com","keys":{"k":{}}}}}`
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	recorder := callCPA(t, &Server{ConfigPath: path}, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code == http.StatusOK {
		t.Fatalf("配置坏掉时不该回 200: %s", recorder.Body.String())
	}
}

// TestSubscriptionTimeTextEpochUnits 钉住秒/毫秒/ISO 三种时间写法。
func TestSubscriptionTimeTextEpochUnits(t *testing.T) {
	want := "2030-01-02T03:04:05Z"
	seconds := float64(1893553445)
	cases := map[string]any{
		"秒":       seconds,
		"毫秒":      seconds * 1000,
		"整数秒":     int64(1893553445),
		"字符串毫秒":   "1893553445000",
		"ISO":     want,
		"空":       nil,
		"零":       float64(0),
		"负数":      float64(-1),
		"认不出的字符串": "not-a-time",
	}
	for name, raw := range cases {
		got := subscriptionTimeText(raw)
		switch name {
		case "秒", "毫秒", "整数秒", "字符串毫秒", "ISO":
			if got != want {
				t.Fatalf("%s：期望 %q，实际 %q", name, want, got)
			}
		default:
			if got != "" {
				t.Fatalf("%s：应给出空串，实际 %q", name, got)
			}
		}
	}
}

// TestSubscriptionErrorTextShapes 钉住几种错误体都能取出可读文本。
func TestSubscriptionErrorTextShapes(t *testing.T) {
	cases := map[string]string{
		`{"error":{"message":"Unauthorized"}}`:  "Unauthorized",
		`{"message":"Go usage is unavailable"}`: "Go usage is unavailable",
		`{"error":"unauthorized"}`:              "unauthorized",
		`{"detail":"boom"}`:                     "boom",
		`<html>502 Bad Gateway</html>`:          "<html>502 Bad Gateway</html>",
		``:                                      "空响应",
	}
	for body, want := range cases {
		if got := subscriptionErrorText([]byte(body)); got != want {
			t.Fatalf("%q：期望 %q，实际 %q", body, want, got)
		}
	}
}

// TestSnakeCase 钉住字段别名的转换（Command Code 的字段名两种写法都见过）。
func TestSnakeCase(t *testing.T) {
	cases := map[string]string{
		"monthlyCredits": "monthly_credits",
		"totalCount":     "total_count",
		"used":           "used",
		"planID":         "plan_i_d",
	}
	for input, want := range cases {
		if got := snakeCase(input); got != want {
			t.Fatalf("%s：期望 %q，实际 %q", input, want, got)
		}
	}
}

// —— 取配置的小工具 ——
//
// readConfigData 已在 workspace_test.go 里定义（读回配置文件并解析成 canonical.Value），
// 这里直接复用。
