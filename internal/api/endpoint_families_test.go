package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 本文件固化「各个 OpenAI 端点族在管理接口这一层是通的」。
//
// 上游转发链路由 internal/proxysupport 与 internal/proxy 的用例覆盖；这里只回答一个
// 更前置的问题：用户能不能把 embedding / image / tts / stt / video / rerank 这些端点
// 的配置**存进去**。存不进去的话，后面的转发逻辑再对也没用——管理接口正是 WebUI 写配置
// 的唯一入口。

// TestUpdateUnifiedModelAcceptsEveryEndpointFamily 固化：
// PUT /api/unified-model 能一次写入全部分族计划，GET 按固定顺序读回。
func TestUpdateUnifiedModelAcceptsEveryEndpointFamily(t *testing.T) {
	server, path := cascadeServer(t)

	body := `{"config_revision":"` + currentRevision(t, path) + `",
		"default":{"primary":{"model":"model-a","key":null}},
		"image":{"primary":{"model":"model-b","key":null}},
		"embeddings":{"primary":{"model":"model-b","key":null}},
		"speech":{"primary":{"model":"model-b","key":null}},
		"transcriptions":{"primary":{"model":"model-b","key":null}},
		"video":{"primary":{"model":"model-b","key":null}},
		"rerank":{"primary":{"model":"model-b","key":null}}}`
	recorder := callCascade(t, server, http.MethodPut, "/api/unified-model", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("保存状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}

	// 落盘：七个族全部在配置里（顺序即 config.UnifiedPlanNames）。
	data := readConfig(t, path)
	unified, _ := data["unified_model"].(map[string]any)
	for _, planName := range []string{"default", "image", "embeddings", "speech", "transcriptions", "video", "rerank"} {
		if _, found := unified[planName]; !found {
			t.Errorf("落盘配置缺少 unified_model.%s", planName)
		}
	}

	// 读回：响应体与落盘一致，且模型引用被规范化成模型 ID。
	recorder = callCascade(t, server, http.MethodGet, "/api/unified-model", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("读取状态码 = %d，期望 200", recorder.Code)
	}
	var response struct {
		UnifiedModel map[string]struct {
			Primary struct {
				Model string `json:"model"`
			} `json:"primary"`
		} `json:"unified_model"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v\nbody=%s", err, recorder.Body.String())
	}
	for _, planName := range []string{"image", "speech", "video", "rerank"} {
		plan, found := response.UnifiedModel[planName]
		if !found {
			t.Fatalf("响应缺少 %s 计划: %s", planName, recorder.Body.String())
		}
		if plan.Primary.Model != "model-b" {
			t.Errorf("%s 计划的主模型 = %q，期望 model-b", planName, plan.Primary.Model)
		}
	}
}

// TestUpdateUnifiedModelRejectsUnknownPlan 固化：未知计划名被显式拒绝。
//
// 静默忽略的后果很糟：用户在 WebUI 里配好「语音合成」却因为字段名拼错（speach）而
// 什么都没存下，而请求会安静地回落到对话模型。
func TestUpdateUnifiedModelRejectsUnknownPlan(t *testing.T) {
	server, path := cascadeServer(t)
	body := `{"config_revision":"` + currentRevision(t, path) + `",
		"default":{"primary":{"model":"model-a","key":null}},
		"speach":{"primary":{"model":"model-b","key":null}}}`
	recorder := callCascade(t, server, http.MethodPut, "/api/unified-model", body)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 = %d，期望 422（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "speach") {
		t.Errorf("错误信息应指出字段名，实际: %s", recorder.Body.String())
	}
}

// TestUpdateProviderAcceptsEveryRouteMode 固化：
// 供应商路由能写下全部端点族的模式，未知模式被拒且错误列出可选集合。
func TestUpdateProviderAcceptsEveryRouteMode(t *testing.T) {
	server, path := cascadeServer(t)
	body := `{"config_revision":"` + currentRevision(t, path) + `",
		"base_url":"https://a.example.test",
		"routes":{
			"speech":"v1/audio/speech",
			"transcriptions":"v1/audio/transcriptions",
			"translations":"v1/audio/translations",
			"video":"v1/videos",
			"rerank":"v1/rerank"}}`
	recorder := callCascade(t, server, http.MethodPut, "/api/providers/prov-a", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("保存状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	data := readConfig(t, path)
	providers, _ := data["providers"].(map[string]any)
	prov, _ := providers["prov-a"].(map[string]any)
	routes, _ := prov["routes"].(map[string]any)
	for _, mode := range []string{"speech", "transcriptions", "translations", "video", "rerank"} {
		if _, found := routes[mode]; !found {
			t.Errorf("落盘配置缺少 routes.%s", mode)
		}
	}

	// 未知模式：422，且错误信息要把可选值都列出来，否则用户只能靠猜。
	server2, path2 := cascadeServer(t)
	bad := `{"config_revision":"` + currentRevision(t, path2) + `",
		"base_url":"https://a.example.test",
		"routes":{"tts_azure":"v1/audio/speech"}}`
	recorder = callCascade(t, server2, http.MethodPut, "/api/providers/prov-a", bad)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("未知模式的状态码 = %d，期望 422（body=%s）", recorder.Code, recorder.Body.String())
	}
	for _, mode := range []string{"speech", "transcriptions", "video", "rerank"} {
		if !strings.Contains(recorder.Body.String(), mode) {
			t.Errorf("错误信息未列出可选模式 %q: %s", mode, recorder.Body.String())
		}
	}
}
