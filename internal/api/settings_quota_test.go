package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 设置接口是账号资源页编辑提前冻结规则的写入通道：GET 必须回显三个字段，
// PUT 必须真正落盘并由配置解析器接受。字段漏进 spec 会直接 422，漏进响应则页面
// 每次打开都显示默认值，都是用户可见的回归。
func TestSettingsQuotaFreezeConfig(t *testing.T) {
	server, configPath := newOpsTestServer(t, nil)

	get := opsRequest(t, server, http.MethodGet, "/api/settings", nil, "full")
	if get.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d，期望 200（body=%s）", get.Code, get.Body.String())
	}
	var current struct {
		ConfigRevision string                     `json:"config_revision"`
		Settings       map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &current); err != nil {
		t.Fatalf("解析 GET 响应失败: %v（%s）", err, get.Body.String())
	}
	if current.ConfigRevision == "" {
		t.Fatal("GET /api/settings 缺少 config_revision")
	}
	for _, key := range []string{
		"quota_freeze_threshold_5h",
		"quota_freeze_threshold_7d",
		"quota_freeze_threshold_whitelist",
		"quota_freeze_policies",
	} {
		if _, ok := current.Settings[key]; !ok {
			t.Errorf("GET /api/settings 缺少 %s: %s", key, get.Body.String())
		}
	}

	payload := fmt.Sprintf(`{"config_revision":%q,"quota_freeze_threshold_5h":0.05,"quota_freeze_threshold_7d":0.1,"quota_freeze_threshold_whitelist":[],"quota_freeze_policies":[]}`, current.ConfigRevision)
	put := opsRequest(t, server, http.MethodPut, "/api/settings", []byte(payload), "full")
	if put.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d，期望 200（body=%s）", put.Code, put.Body.String())
	}
	var updated struct {
		Settings struct {
			Threshold5h float64         `json:"quota_freeze_threshold_5h"`
			Threshold7d float64         `json:"quota_freeze_threshold_7d"`
			Whitelist   []string        `json:"quota_freeze_threshold_whitelist"`
			Policies    json.RawMessage `json:"quota_freeze_policies"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &updated); err != nil {
		t.Fatalf("解析 PUT 响应失败: %v（%s）", err, put.Body.String())
	}
	if updated.Settings.Threshold5h != 0.05 || updated.Settings.Threshold7d != 0.1 {
		t.Errorf("PUT 响应阈值 = %v/%v，期望 0.05/0.1", updated.Settings.Threshold5h, updated.Settings.Threshold7d)
	}
	if updated.Settings.Whitelist == nil || len(updated.Settings.Whitelist) != 0 {
		t.Errorf("PUT 响应白名单 = %#v，期望空数组", updated.Settings.Whitelist)
	}
	if string(updated.Settings.Policies) != "[]" {
		t.Errorf("PUT 响应账号策略 = %s，期望 []", updated.Settings.Policies)
	}

	data, err := config.LoadConfigData(configPath)
	if err != nil {
		t.Fatalf("读回配置失败: %v", err)
	}
	cfg, err := config.FromDict(data)
	if err != nil {
		t.Fatalf("解析落盘配置失败: %v", err)
	}
	if cfg.QuotaFreezeThreshold5h != 0.05 || cfg.QuotaFreezeThreshold7d != 0.1 {
		t.Errorf("落盘阈值 = %v/%v，期望 0.05/0.1", cfg.QuotaFreezeThreshold5h, cfg.QuotaFreezeThreshold7d)
	}
}

// TestSettingsQuotaPoliciesRoundTrip 钉住账号级冻结策略确实能经 PUT /api/settings
// 落盘并由配置解析器接受；空数组、null 字段与显式空白名单的语义都要保留。
func TestSettingsQuotaPoliciesRoundTrip(t *testing.T) {
	server, configPath := newOpsTestServer(t, nil)
	data, err := config.LoadConfigData(configPath)
	if err != nil {
		t.Fatalf("读取测试配置失败: %v", err)
	}
	providers, err := canonical.ParseString(`{
		"p": {"base_url": "https://p.example", "keys": {"k": {"api_key": "x"}}}
	}`)
	if err != nil {
		t.Fatalf("解析供应商夹具失败: %v", err)
	}
	models, err := canonical.ParseString(`{
		"m": {"aliases": ["alias-m"], "targets": [{"provider": "p", "key": "k", "upstream_model": "u"}]}
	}`)
	if err != nil {
		t.Fatalf("解析模型夹具失败: %v", err)
	}
	instances, err := canonical.ParseString(`{
		"jp": {"base_url": "http://127.0.0.1:8317", "management_key": "mk"}
	}`)
	if err != nil {
		t.Fatalf("解析实例夹具失败: %v", err)
	}
	data.SetKey("providers", providers)
	data.SetKey("models", models)
	data.SetKey("cpa_instances", instances)
	if err := config.SaveConfigData(configPath, data); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	get := opsRequest(t, server, http.MethodGet, "/api/settings", nil, "full")
	if get.Code != http.StatusOK {
		t.Fatalf("GET = %d（body=%s）", get.Code, get.Body.String())
	}
	var current struct {
		ConfigRevision string `json:"config_revision"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &current); err != nil || current.ConfigRevision == "" {
		t.Fatalf("读取 config_revision 失败: %v（%s）", err, get.Body.String())
	}

	payload := fmt.Sprintf(`{"config_revision":%q,"quota_freeze_policies":[`+
		`{"scope":"cpa_instance","target":"jp","enabled":true,"threshold_5h":0.08,"whitelist":["alias-m"]},`+
		`{"scope":"subscription","target":"p/k","enabled":false,"whitelist":[]}]}`, current.ConfigRevision)
	put := opsRequest(t, server, http.MethodPut, "/api/settings", []byte(payload), "full")
	if put.Code != http.StatusOK {
		t.Fatalf("PUT = %d，期望 200（body=%s）", put.Code, put.Body.String())
	}
	var updated struct {
		Settings struct {
			Policies []struct {
				Scope      string    `json:"scope"`
				Target     string    `json:"target"`
				Enabled    bool      `json:"enabled"`
				Threshold5 *float64  `json:"threshold_5h"`
				Whitelist  *[]string `json:"whitelist"`
			} `json:"quota_freeze_policies"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &updated); err != nil {
		t.Fatalf("解析 PUT 响应失败: %v（%s）", err, put.Body.String())
	}
	if len(updated.Settings.Policies) != 2 {
		t.Fatalf("响应策略数量 = %d，期望 2（%s）", len(updated.Settings.Policies), put.Body.String())
	}
	if got := updated.Settings.Policies[0]; got.Scope != "cpa_instance" || got.Target != "jp" ||
		!got.Enabled || got.Threshold5 == nil || *got.Threshold5 != 0.08 || got.Whitelist == nil || len(*got.Whitelist) != 1 {
		t.Errorf("CPA 策略回显不对: %+v", got)
	}
	if got := updated.Settings.Policies[1]; got.Scope != "subscription" || got.Target != "p/k" ||
		got.Enabled || got.Whitelist == nil || len(*got.Whitelist) != 0 {
		t.Errorf("订阅策略回显不对: %+v", got)
	}

	saved, err := config.LoadConfigData(configPath)
	if err != nil {
		t.Fatalf("读回配置失败: %v", err)
	}
	cfg, err := config.FromDict(saved)
	if err != nil {
		t.Fatalf("落盘配置解析失败: %v", err)
	}
	if len(cfg.QuotaFreezePolicies) != 2 {
		t.Fatalf("落盘策略数量 = %d，期望 2", len(cfg.QuotaFreezePolicies))
	}
	if p := cfg.QuotaFreezePolicies[0]; p.Threshold5h == nil || *p.Threshold5h != 0.08 ||
		!p.WhitelistSet || len(p.Whitelist) != 1 || p.Whitelist[0] != "alias-m" {
		t.Errorf("落盘 CPA 策略不对: %+v", p)
	}
	if p := cfg.QuotaFreezePolicies[1]; p.Enabled || !p.WhitelistSet || len(p.Whitelist) != 0 {
		t.Errorf("落盘订阅策略不对: %+v", p)
	}
}
