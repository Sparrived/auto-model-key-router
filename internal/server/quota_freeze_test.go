package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/quota"
)

// 额度冻结在装配层的接线（internal/server）：
//
//   - 路由可达（内部 api 的 mux 与 routePatterns 清单都对得上）；
//   - 冻结表与配置同目录、进程重启后仍在（持久化路径由装配层决定）；
//   - 热重载换 KeyPool 之后新 pool 仍然拿到同一张冻结表（漏了注入 = 冻结静默失效）。

// quotaFreezeFixture 造一条未过期的冻结记录。
func quotaFreezeFixture(now time.Time, providerID, keyName string) quota.Freeze {
	return quota.Freeze{
		ProviderID:  providerID,
		KeyName:     keyName,
		Vendor:      "Claude",
		Scope:       "subscription",
		Source:      "account_resource",
		WindowKey:   "claude/5h",
		WindowLabel: "5 小时",
		Remaining:   0,
		Status:      "rejected",
		FrozenAt:    now,
		ExpiresAt:   now.Add(time.Hour),
		Reason:      "5 小时窗口已用尽（剩余 0%）",
	}
}

// quotaFreezesBody 是 GET /api/quota-freezes 的最小解码形状（装配层只关心接线，
// 字段形状由 internal/api 的用例钉住）。
type quotaFreezesBody struct {
	Enabled bool              `json:"enabled"`
	Freezes []json.RawMessage `json:"freezes"`
}

// TestQuotaFreezesRoutesReachable 断言装配之后两条冻结路由可达，不是兜底 404。
func TestQuotaFreezesRoutesReachable(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)

	recorder := serve(app, http.MethodGet, "/api/quota-freezes", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/quota-freezes = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var body quotaFreezesBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if !body.Enabled {
		t.Errorf("默认配置（interval=60）下应 enabled=true: %s", recorder.Body.String())
	}
	if body.Freezes == nil {
		t.Errorf("freezes 应是空数组而不是缺失/null: %s", recorder.Body.String())
	}

	// DELETE 命中真实路由：没有冻结时回 404「该 Key 当前没有额度冻结」，
	// 而不是兜底 404 的 {"detail":"Not Found"}——两者状态码相同，靠响应体区分。
	released := serve(app, http.MethodDelete, "/api/quota-freezes/prov-a/key-a", fullAuthorization)
	if released.Code != http.StatusNotFound {
		t.Fatalf("DELETE 无冻结的 Key = %d，期望 404（body=%s）", released.Code, released.Body.String())
	}
	if !strings.Contains(released.Body.String(), "没有额度冻结") {
		t.Errorf("DELETE 请求应命中真实处理器: %s", released.Body.String())
	}

	// 错方法：路径已知 → 405（routePatterns 清单里必须有这两条，否则会退化成 404）。
	wrong := serve(app, http.MethodPost, "/api/quota-freezes", fullAuthorization)
	if wrong.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/quota-freezes = %d，期望 405（body=%s）", wrong.Code, wrong.Body.String())
	}
}

// TestQuotaFreezePersistsAcrossRestart 断言状态文件落在配置同目录，且重启后冻结还在。
func TestQuotaFreezePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, true, false)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if app.freezeStore == nil {
		t.Fatal("App 应持有冻结表")
	}
	if _, err := app.freezeStore.Upsert(quotaFreezeFixture(time.Now().UTC(), "prov-a", "key-a")); err != nil {
		t.Fatalf("写入冻结失败: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关停失败: %v", err)
	}

	// 状态文件固定在 <配置目录>/quota-freezes.json（设计稿第 8 节）。
	statePath := filepath.Join(dir, "quota-freezes.json")
	if _, statErr := os.Stat(statePath); statErr != nil {
		t.Fatalf("冻结状态文件应写在配置同目录: %v", statErr)
	}

	// 重启：同一个配置目录 → 冻结表从磁盘恢复，GET 能读到。
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("重新载入配置失败: %v", err)
	}
	restarted, err := New(Options{ConfigPath: path, Config: reloaded, Version: testVersion})
	if err != nil {
		t.Fatalf("重新装配失败: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	recorder := serve(restarted, http.MethodGet, "/api/quota-freezes", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var body quotaFreezesBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是期望形状: %v（%s）", err, recorder.Body.String())
	}
	if len(body.Freezes) != 1 || !strings.Contains(string(body.Freezes[0]), `"key_name":"key-a"`) {
		t.Fatalf("重启后冻结应仍在: %s", recorder.Body.String())
	}
}

// TestFrozenKeyLeavesModelList 端到端断言装配链真的通了：冻结一把 Key 之后，
// 只绑这把 Key 的模型从 /v1/models 里消失，点名请求直接 503 并说明是额度冻结。
//
// 这一条跨了三层（App 装配 → KeyPool 硬过滤 → proxy 错误映射），是本任务里唯一能
// 证明「冻结表真的接进了选路」的用例；只测 GET /api/quota-freezes 是不够的——接口
// 能读表不代表选路在读它。
func TestFrozenKeyLeavesModelList(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)

	ids := modelIDs(t, serve(app, http.MethodGet, "/v1/models", fullAuthorization))
	if !contains(ids, "model-a") {
		t.Fatalf("前置条件：初始清单应含 model-a，实际 %v", ids)
	}

	if _, err := app.freezeStore.Upsert(quotaFreezeFixture(time.Now().UTC(), "prov-a", "key-a")); err != nil {
		t.Fatalf("写入冻结失败: %v", err)
	}

	ids = modelIDs(t, serve(app, http.MethodGet, "/v1/models", fullAuthorization))
	if contains(ids, "model-a") {
		t.Fatalf("只能由冻结 Key 提供的模型应从清单里消失，实际 %v", ids)
	}
	// 别的模型不受牵连。
	if !contains(ids, "model-nv") {
		t.Errorf("未被冻结的模型不该消失，实际 %v", ids)
	}

	// 点名请求：全部候选被冻结 → 503，且文案能看出是额度冻结而不是「没有可用 key」。
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", fullAuthorization)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("冻结模型的请求应回 503，实际 %d（%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "额度冻结") {
		t.Errorf("503 文案应点名额度冻结: %s", recorder.Body.String())
	}
}

// TestUpstreamQuotaHeaderFreezesKey 端到端断言 L3 观察点真的接上了：一次真实代理请求
// 拿到带上限头的上游响应后，后台把该 Key 冻结，下一次请求直接被挡成 503。
//
// 这里跑的是完整的装配（proxy.Options.QuotaObserver → api.QuotaGuard →
// keypool.SetQuotaFreezes），任何一处漏接都会让第二次请求照常成功。
func TestUpstreamQuotaHeaderFreezesKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "1")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,` +
			`"model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},` +
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	app := newTestAppWith(t, t.TempDir(), appFixture{opsEnabled: true, configText: `{
  "config_version": 4,
  "host": "127.0.0.1",
  "port": 8000,
  "request_timeout": 10,
  "stream_first_byte_timeout": 30,
  "stream_idle_timeout": 60,
  "max_retries": 1,
  "key_failure_threshold": 1,
  "key_cooldown_seconds": 60,
  "endpoint_capabilities_path": "<CAP>",
  "metrics_db_path": "<DB>",
  "log_file_path": "<LOG>",
  "local_api_key": "local-key",
  "ops_enabled": true,
  "webui_enabled": false,
  "providers": {
    "prov-a": {
      "base_url": "` + upstream.URL + `",
      "keys": {"key-a": {"api_key": "sk-secret-a", "enabled": true}},
      "routes": {"openai": "v1/chat/completions"}
    }
  },
  "models": {
    "model-a": {
      "targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}],
      "routing_mode": "round_robin"
    }
  }
}`})

	// 等启动评估完成：关联表就绪之前，L3 信号只会触发重评估、不直接冻结。
	deadline := time.Now().Add(3 * time.Second)
	for app.guard.Status().LastEvaluatedAt == "" {
		if time.Now().After(deadline) {
			t.Fatal("启动评估没有完成")
		}
		time.Sleep(10 * time.Millisecond)
	}

	call := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`))
		request.Header.Set("Authorization", fullAuthorization)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	if first := call(); first.Code != http.StatusOK {
		t.Fatalf("第一次请求应成功到达上游，实际 %d（%s）", first.Code, first.Body.String())
	}
	// 观察点只投递信号，落地在后台循环里：等冻结表出现这条记录。
	deadline = time.Now().Add(3 * time.Second)
	for !app.freezeStore.IsFrozen("prov-a", "key-a") {
		if time.Now().After(deadline) {
			t.Fatal("上游响应头里的额度耗尽没有落地成冻结")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if freeze, _ := app.freezeStore.Get("prov-a", "key-a"); freeze.Scope != "upstream" {
		t.Errorf("L3 冻结的 scope 应为 upstream: %+v", freeze)
	}

	if second := call(); second.Code != http.StatusServiceUnavailable {
		t.Fatalf("冻结后的请求应回 503，实际 %d（%s）", second.Code, second.Body.String())
	}

	// 手工解冻之后恢复：抑制窗口内不会立刻被同一读数冻回去。
	if released := serve(app, http.MethodDelete, "/api/quota-freezes/prov-a/key-a", fullAuthorization); released.Code != http.StatusOK {
		t.Fatalf("手工解冻应回 200，实际 %d（%s）", released.Code, released.Body.String())
	}
	if third := call(); third.Code != http.StatusOK {
		t.Fatalf("解冻后请求应恢复，实际 %d（%s）", third.Code, third.Body.String())
	}
}

// TestQuotaFreezeSurvivesReload 断言热重载换掉 KeyPool 之后，新 pool 重新拿到同一张
// 冻结表：漏了 SetQuotaFreezes，冻结就会「配置一改就全失效」，而界面上还显示着冻结。
func TestQuotaFreezeSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, true, false)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()

	// 冻结 model-a 的候选 Key prov-a/key-a。
	if _, err := app.freezeStore.Upsert(quotaFreezeFixture(time.Now().UTC(), "prov-a", "key-a")); err != nil {
		t.Fatalf("写入冻结失败: %v", err)
	}

	before, ok := app.Manager().Current().KeyPool.(*keypool.KeyPool)
	if !ok {
		t.Fatalf("当前代 KeyPool 类型不对: %T", app.Manager().Current().KeyPool)
	}
	if count := before.FrozenKeyCount("model-a", nil); count != 1 {
		t.Fatalf("初始 pool 应看到 1 把冻结 Key，实际 %d", count)
	}

	// 改一个与冻结无关的配置项（request_timeout）触发重载：providers/models 原样保留，
	// 因此冻结记录不会因为「Key 已不存在」被清理。
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读配置失败: %v", err)
	}
	rewritten := strings.Replace(string(original), `"request_timeout": 10`, `"request_timeout": 12`, 1)
	if rewritten == string(original) {
		t.Fatal("夹具里没有 request_timeout，重载触发方式需要更新")
	}
	rewriteConfig(t, path, rewritten)

	// 一次请求就会触发重载（reload 发生在 handler 之前）。
	if recorder := serve(app, http.MethodGet, "/api/quota-freezes", fullAuthorization); recorder.Code != http.StatusOK {
		t.Fatalf("重载后 GET = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	after, ok := app.Manager().Current().KeyPool.(*keypool.KeyPool)
	if !ok {
		t.Fatalf("重载后 KeyPool 类型不对: %T", app.Manager().Current().KeyPool)
	}
	if after == before {
		t.Fatal("改 request_timeout 后应当换一代 KeyPool（否则这条用例没测到注入）")
	}
	if count := after.FrozenKeyCount("model-a", nil); count != 1 {
		t.Errorf("重载后新 pool 应仍看到 1 把冻结 Key，实际 %d", count)
	}
	if body := serve(app, http.MethodGet, "/api/quota-freezes", fullAuthorization).Body.String(); !strings.Contains(body, `"key_name":"key-a"`) {
		t.Errorf("重载后冻结记录不该消失: %s", body)
	}
}
