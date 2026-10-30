package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

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
	} {
		if _, ok := current.Settings[key]; !ok {
			t.Errorf("GET /api/settings 缺少 %s: %s", key, get.Body.String())
		}
	}

	payload := fmt.Sprintf(`{"config_revision":%q,"quota_freeze_threshold_5h":0.05,"quota_freeze_threshold_7d":0.1,"quota_freeze_threshold_whitelist":[]}`, current.ConfigRevision)
	put := opsRequest(t, server, http.MethodPut, "/api/settings", []byte(payload), "full")
	if put.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d，期望 200（body=%s）", put.Code, put.Body.String())
	}
	var updated struct {
		Settings struct {
			Threshold5h float64  `json:"quota_freeze_threshold_5h"`
			Threshold7d float64  `json:"quota_freeze_threshold_7d"`
			Whitelist   []string `json:"quota_freeze_threshold_whitelist"`
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
