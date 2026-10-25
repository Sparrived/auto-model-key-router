package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/quota"
)

// 额度冻结的管理接口（quota_freeze.go）与账号资源响应增强（handlers_cpa.go）的用例。
//
// 接口形状是前端契约（设计稿 6.1 / 6.2）：字段名、时间格式、空数组 vs null 都钉死；
// 手工解冻的抑制窗口语义单独测（它是「解除冻结」按钮真正有用的前提）。

// —— GET /api/quota-freezes ——

// TestListQuotaFreezesShape 钉住冻结列表的形状：字段齐全、时间 RFC3339 UTC、
// remaining 保留原值、状态平铺在同一层。
func TestListQuotaFreezesShape(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	resetAt := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)
	if _, err := fixture.Store.Upsert(quota.Freeze{
		ProviderID:  "go-a",
		KeyName:     "key-1",
		Vendor:      "OpenCode Go",
		Scope:       "subscription",
		Source:      "account_resource",
		Account:     "ops@example.com",
		WindowKey:   "claude/5h",
		WindowLabel: "5 小时",
		Remaining:   0,
		Status:      "rejected",
		ResetAt:     resetAt,
		FrozenAt:    fixture.Clock.Now(),
		ExpiresAt:   resetAt.Add(quotaFreezeResetGrace),
		Reason:      "5 小时窗口已用尽（剩余 0%）",
	}); err != nil {
		t.Fatalf("写入冻结失败: %v", err)
	}

	recorder := callCPA(t, fixture.Server, http.MethodGet, "/api/quota-freezes", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}

	// 先按原始 JSON 判字段存在性：结构体解码会把「缺失」与「零值」抹成一样。
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应不是合法 JSON: %v（%s）", err, recorder.Body.String())
	}
	for _, key := range []string{"fetched_at", "enabled", "interval_seconds", "last_evaluated_at", "last_error", "freezes"} {
		if _, present := raw[key]; !present {
			t.Errorf("响应缺少字段 %q: %s", key, recorder.Body.String())
		}
	}

	var body struct {
		FetchedAt       string            `json:"fetched_at"`
		Enabled         bool              `json:"enabled"`
		IntervalSeconds float64           `json:"interval_seconds"`
		LastEvaluatedAt string            `json:"last_evaluated_at"`
		LastError       string            `json:"last_error"`
		Freezes         []QuotaFreezeView `json:"freezes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if !body.Enabled || body.IntervalSeconds != 60 {
		t.Errorf("默认配置下应 enabled=true、interval=60: %+v", body)
	}
	if _, err := time.Parse(time.RFC3339, body.FetchedAt); err != nil {
		t.Errorf("fetched_at 不是 RFC3339: %q", body.FetchedAt)
	}
	if len(body.Freezes) != 1 {
		t.Fatalf("期望 1 条冻结，实际 %d（%s）", len(body.Freezes), recorder.Body.String())
	}
	freeze := body.Freezes[0]
	if freeze.ProviderID != "go-a" || freeze.KeyName != "key-1" {
		t.Errorf("冻结坐标不对: %+v", freeze)
	}
	if freeze.Scope != "subscription" || freeze.Source != "account_resource" {
		t.Errorf("scope/source 不对: %+v", freeze)
	}
	if freeze.Vendor != "OpenCode Go" || freeze.Account != "ops@example.com" {
		t.Errorf("展示字段不对: %+v", freeze)
	}
	if freeze.WindowKey != "claude/5h" || freeze.WindowLabel != "5 小时" || freeze.Remaining != 0 {
		t.Errorf("窗口字段不对（remaining 应保留原值 0）: %+v", freeze)
	}
	if freeze.Status != "rejected" || freeze.Reason == "" {
		t.Errorf("状态或原因缺失: %+v", freeze)
	}
	// 时间字段一律 RFC3339 UTC，且与写入值一致（不能出现本地时区偏移）。
	for name, value := range map[string]string{"reset_at": freeze.ResetAt, "frozen_at": freeze.FrozenAt, "expires_at": freeze.ExpiresAt} {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Errorf("%s 不是 RFC3339: %q", name, value)
			continue
		}
		if parsed.Location() != time.UTC {
			t.Errorf("%s 应是 UTC: %q", name, value)
		}
	}
	if want := resetAt.Format(time.RFC3339); freeze.ResetAt != want {
		t.Errorf("reset_at = %q，期望 %q", freeze.ResetAt, want)
	}
}

// TestListQuotaFreezesEmptyArrays 钉住「没有冻结」是空数组而不是 null。
func TestListQuotaFreezesEmptyArrays(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	recorder := callCPA(t, fixture.Server, http.MethodGet, "/api/quota-freezes", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"freezes":[]`) {
		t.Fatalf("没有冻结时 freezes 应为空数组: %s", body)
	}
}

// TestListQuotaFreezesUnavailable 钉住未装配时 503：机制没开与「一切正常」必须能分开。
func TestListQuotaFreezesUnavailable(t *testing.T) {
	server := accountsServer(t, `{}`)
	recorder := callCPA(t, server, http.MethodGet, "/api/quota-freezes", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配冻结表时应回 503，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
}

// TestListQuotaFreezesRequiresAuth 钉住鉴权：没有凭据时 401，且不泄漏冻结内容。
func TestListQuotaFreezesRequiresAuth(t *testing.T) {
	fixture := newQuotaGuardFixture(t, `{}`, `{}`, "")
	request := httptest.NewRequest(http.MethodGet, "/api/quota-freezes", nil)
	recorder := httptest.NewRecorder()
	fixture.Server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("无凭据时应回 401，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
}

// —— DELETE /api/quota-freezes/{provider_id}/{key_name} ——

// TestDeleteQuotaFreeze 钉住手工解冻：200 形状、抑制窗口取「原到期」与「现在+10min」的较大者。
func TestDeleteQuotaFreeze(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")
	fixture.Guard.ApplyReport(subscriptionReport(true, exhaustedWindow()))
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("前置条件：应已冻结")
	}

	recorder := callCPA(t, fixture.Server, http.MethodDelete, "/api/quota-freezes/go-a/key-1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Released        bool   `json:"released"`
		ProviderID      string `json:"provider_id"`
		KeyName         string `json:"key_name"`
		SuppressedUntil string `json:"suppressed_until"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if !body.Released || body.ProviderID != "go-a" || body.KeyName != "key-1" {
		t.Errorf("解除响应不对: %+v", body)
	}
	wantUntil := fixture.Clock.Now().Add(quota.SuppressionFloor())
	if body.SuppressedUntil != wantUntil.Format(time.RFC3339) {
		t.Errorf("suppressed_until = %q，期望 %q", body.SuppressedUntil, wantUntil.Format(time.RFC3339))
	}
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("解除后不该再冻结")
	}
	if !fixture.Store.Suppressed("go-a", "key-1") {
		t.Fatal("解除后应进入抑制窗口")
	}

	// 再解一次：已经没有冻结了 → 404（状态已变，与前端的提示话术对应）。
	if again := callCPA(t, fixture.Server, http.MethodDelete, "/api/quota-freezes/go-a/key-1", ""); again.Code != http.StatusNotFound {
		t.Fatalf("重复解除应回 404，实际 %d（%s）", again.Code, again.Body.String())
	}
}

// TestDeleteQuotaFreezeUsesLaterExpiry 钉住抑制窗口取较大者：冻结原本离到期更远时
// 不能被「现在 + 10 分钟」缩短。
func TestDeleteQuotaFreezeUsesLaterExpiry(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")
	// 一条还剩 40 分钟到期的冻结（比 10 分钟抑制底线更远）。
	expiresAt := fixture.Clock.Now().Add(40 * time.Minute)
	if _, err := fixture.Store.Upsert(quota.Freeze{
		ProviderID: "go-a", KeyName: "key-1", Scope: "subscription", Source: "account_resource",
		WindowKey: "claude/5h", WindowLabel: "5 小时",
		FrozenAt: fixture.Clock.Now(), ExpiresAt: expiresAt, Reason: "测试",
	}); err != nil {
		t.Fatalf("写入冻结失败: %v", err)
	}
	body := callCPA(t, fixture.Server, http.MethodDelete, "/api/quota-freezes/go-a/key-1", "").Body.String()
	if !strings.Contains(body, expiresAt.Format(time.RFC3339)) {
		t.Fatalf("suppressed_until 应取原到期时间 %q: %s", expiresAt.Format(time.RFC3339), body)
	}
}

// TestDeleteQuotaFreezeNotFound 钉住两种 404：配置里没有这把 Key、以及没有冻结。
func TestDeleteQuotaFreezeNotFound(t *testing.T) {
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	// Key 不在配置里：请求本身写错了。
	missing := callCPA(t, fixture.Server, http.MethodDelete, "/api/quota-freezes/go-a/key-none", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("配置里没有该 Key 时应回 404，实际 %d（%s）", missing.Code, missing.Body.String())
	}
	if !strings.Contains(missing.Body.String(), "配置里没有该 Key") {
		t.Errorf("404 应说明是配置里没有该 Key: %s", missing.Body.String())
	}

	// Key 存在但当前没冻结：状态已变（过期或被别处解除）。
	unfrozen := callCPA(t, fixture.Server, http.MethodDelete, "/api/quota-freezes/go-a/key-1", "")
	if unfrozen.Code != http.StatusNotFound {
		t.Fatalf("没有冻结时应回 404，实际 %d（%s）", unfrozen.Code, unfrozen.Body.String())
	}
	if !strings.Contains(unfrozen.Body.String(), "没有额度冻结") {
		t.Errorf("404 应说明当前没有冻结: %s", unfrozen.Body.String())
	}
}

// TestDeleteQuotaFreezeUnavailable 钉住未装配时 503（与 GET 同一取舍）。
func TestDeleteQuotaFreezeUnavailable(t *testing.T) {
	server := accountsServer(t, `{}`)
	recorder := callCPA(t, server, http.MethodDelete, "/api/quota-freezes/go-a/key-1", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配冻结表时应回 503，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
}

// —— 账号资源响应增强（设计稿 6.2）——

// TestAccountsReportInstanceQuotaFreeze 钉住实例级联动在页面上的读数：
// linked_providers / all_accounts_exhausted / frozen_keys / accounts[].quota_exhausted
// 与顶层 freezes / guard 一起出现，且既有字段不变。
func TestAccountsReportInstanceQuotaFreeze(t *testing.T) {
	stub := cpaStub(t, cpaStubSpec{files: []map[string]any{{
		"auth_index": "idx-1",
		"name":       "claude-a.json",
		"provider":   "claude",
		"email":      "ops@example.com",
		"quota": map[string]any{
			"observed_at": "2026-01-01T00:00:00Z",
			"signals": map[string]any{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "1",
				"Anthropic-Ratelimit-Unified-5h-Status":      "rejected",
			},
		},
	}}})
	providers := fmt.Sprintf(`{"cpa-a":{"base_url":%q,"keys":{"key-1":{"api_key":"sk-1"}}}}`, stub.URL)
	instances := fmt.Sprintf(`{"jp":{"label":"日本 CPA","base_url":%q,"management_key":"mk-1"}}`, stub.URL)
	fixture := newQuotaGuardFixture(t, providers, instances, "")

	recorder := callCPA(t, fixture.Server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var response cpaAccountsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}

	// 顶层冻结与评估器状态。
	if len(response.Freezes) != 1 || response.Freezes[0].ProviderID != "cpa-a" || response.Freezes[0].KeyName != "key-1" {
		t.Fatalf("顶层 freezes 不对: %+v", response.Freezes)
	}
	if response.Freezes[0].Scope != "cpa_instance" || response.Freezes[0].InstanceID != "jp" {
		t.Errorf("实例级冻结应带 scope/instance_id: %+v", response.Freezes[0])
	}
	if !response.Guard.Enabled || response.Guard.IntervalSeconds != 60 || response.Guard.LastEvaluatedAt == "" {
		t.Errorf("guard 状态不对: %+v", response.Guard)
	}

	// 实例块的三项增强字段。
	if len(response.Instances) != 1 {
		t.Fatalf("期望 1 个实例，实际 %d", len(response.Instances))
	}
	instance := response.Instances[0]
	if !instance.OK {
		t.Fatalf("实例应读取成功: %+v", instance)
	}
	if instance.AllAccountsExhausted != true {
		t.Errorf("唯一启用账号硬性耗尽时 all_accounts_exhausted 应为 true: %+v", instance)
	}
	if len(instance.LinkedProviders) != 1 || instance.LinkedProviders[0] != "cpa-a" {
		t.Errorf("linked_providers 不对: %+v", instance.LinkedProviders)
	}
	if len(instance.FrozenKeys) != 1 ||
		instance.FrozenKeys[0].ProviderID != "cpa-a" || instance.FrozenKeys[0].KeyName != "key-1" {
		t.Errorf("frozen_keys 不对: %+v", instance.FrozenKeys)
	}

	// 账号块：quota_exhausted 与既有字段并存。
	if len(instance.Accounts) != 1 {
		t.Fatalf("期望 1 个账号，实际 %d", len(instance.Accounts))
	}
	account := instance.Accounts[0]
	if !account.QuotaExhausted {
		t.Errorf("账号应有 quota_exhausted=true: %+v", account)
	}
	if account.Email != "ops@example.com" || len(account.Windows) == 0 {
		t.Errorf("既有账号字段不该被吞掉: %+v", account)
	}

	// 空数组而不是 null：前端把 frozen_keys / linked_providers 当列表渲染。
	body := recorder.Body.String()
	if strings.Contains(body, `"frozen_keys":null`) || strings.Contains(body, `"linked_providers":null`) {
		t.Fatalf("增强字段应是数组而不是 null: %s", body)
	}
}

// TestAccountsReportSubscriptionFreeze 钉住订阅卡片上的 freeze 字段：冻结时出现、
// 恢复后消失，且既有字段保持原样。
func TestAccountsReportSubscriptionFreeze(t *testing.T) {
	usedPercent := 100
	stubSubscriptions(t, func(w http.ResponseWriter, _ *http.Request) {
		stubJSON(t, w, http.StatusOK, map[string]any{"usage": map[string]any{
			"rolling": map[string]any{"status": "ok", "percent": usedPercent},
		}})
	})
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, "")

	recorder := callCPA(t, fixture.Server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var response cpaAccountsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if len(response.Subscriptions) != 1 {
		t.Fatalf("期望 1 条订阅，实际 %d", len(response.Subscriptions))
	}
	entry := response.Subscriptions[0]
	if !entry.OK || len(entry.Windows) == 0 {
		t.Fatalf("订阅读数应正常: %+v", entry)
	}
	if entry.Freeze == nil {
		t.Fatalf("用尽 100%% 后订阅条目应带 freeze: %+v", entry)
	}
	if entry.Freeze.ProviderID != "go-a" || entry.Freeze.KeyName != "key-1" || entry.Freeze.Scope != "subscription" {
		t.Errorf("订阅 freeze 不对: %+v", entry.Freeze)
	}
	if !fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("页面刷新应触发即时评估并冻结")
	}

	// 读数恢复：再刷一次页面，freeze 消失、冻结表也清空。
	//
	// 用一个新的响应变量：freeze 是 omitempty，往同一个结构体里再解一次会把上次的
	// 指针留在原地（字段缺省 = 不解码），从而看起来「恢复后还冻着」。
	usedPercent = 10
	recorder = callCPA(t, fixture.Server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var refreshed cpaAccountsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &refreshed); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if refreshed.Subscriptions[0].Freeze != nil {
		t.Fatalf("恢复后订阅条目不该再带 freeze: %+v", refreshed.Subscriptions[0].Freeze)
	}
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("页面刷新应触发解冻")
	}
}

// TestAccountsReportQuotaFreezeDisabled 钉住 interval=0 时页面不进评估：
// 读数照常显示，但不会冻结，guard 状态如实报告关闭。
func TestAccountsReportQuotaFreezeDisabled(t *testing.T) {
	stubSubscriptions(t, func(w http.ResponseWriter, _ *http.Request) {
		stubJSON(t, w, http.StatusOK, map[string]any{"usage": map[string]any{
			"rolling": map[string]any{"status": "ok", "percent": 100},
		}})
	})
	fixture := newQuotaGuardFixture(t,
		`{"go-a":{"base_url":"https://opencode.ai/zen/go/v1","keys":{"key-1":{"api_key":"sk-1"}}}}`,
		`{}`, `"quota_freeze_interval_seconds":0,`)

	recorder := callCPA(t, fixture.Server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	var response cpaAccountsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if response.Guard.Enabled {
		t.Errorf("interval=0 时 guard.enabled 应为 false: %+v", response.Guard)
	}
	if len(response.Freezes) != 0 {
		t.Errorf("机制关闭时不应冻结: %+v", response.Freezes)
	}
	if len(response.Subscriptions) != 1 || response.Subscriptions[0].Freeze != nil {
		t.Errorf("机制关闭时订阅条目不该带 freeze: %+v", response.Subscriptions)
	}
	if fixture.Store.IsFrozen("go-a", "key-1") {
		t.Fatal("机制关闭时冻结表不该被写入")
	}
}

// TestAccountsReportWithoutQuotaGuard 钉住未装配评估器时响应形状仍然完整
// （freezes 空数组、guard 关闭），既有字段一个不少。
func TestAccountsReportWithoutQuotaGuard(t *testing.T) {
	server := accountsServer(t,
		`{"deepseek":{"base_url":"https://api.deepseek.com","keys":{"key-1":{"api_key":"sk-d"}}}}`)
	recorder := callCPA(t, server, http.MethodGet, "/api/cpa-accounts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"freezes":[]`) {
		t.Fatalf("未装配时 freezes 应为空数组: %s", body)
	}
	if !strings.Contains(body, `"guard":{"enabled":false`) {
		t.Fatalf("未装配时 guard.enabled 应为 false: %s", body)
	}
	if !strings.Contains(body, `"instances":[]`) || !strings.Contains(body, `"subscriptions":[]`) {
		t.Fatalf("既有字段形状不该变: %s", body)
	}
}
