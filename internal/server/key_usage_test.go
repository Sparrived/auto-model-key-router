package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
)

// keyUsageFixtureConfig 在默认夹具之上补一个 `access_keys` 段。
//
// 端点要补的 `access_key_name` 来自**配置**（指标库只存 key_id），所以夹具必须真的
// 配上一把——否则「补名字」这条路径永远只走到回 null 那一支，测不出东西。顺带把
// fixtureTemplate 的模型形状原样保留（默认夹具的模型与供应商都要能用）。
const keyUsageFixtureConfig = `{
  "config_version": 4,
  "endpoint_capabilities_path": "<CAP>",
  "metrics_db_path": "<DB>",
  "log_file_path": "<LOG>",
  "local_api_key": "local-key",
  "ops_enabled": true,
  "webui_enabled": true,
  "providers": {
    "prov-a": {
      "base_url": "https://a.example.test",
      "keys": {"key-a": {"api_key": "sk-secret-a", "enabled": true}},
      "routes": {}
    }
  },
  "models": {
    "model-a": {
      "targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}],
      "aliases": ["alias-a"]
    }
  },
  "access_keys": {
    "ak1": {"key": "amkr-ak-1", "name": "试用账号 A", "enabled": true}
  }
}`

// newKeyUsageApp 装配一个 /ui 真的挂载、且配置里带访问密钥的 App。
//
// /ui 下的路由只有在 index.html 存在时才注册（webui.Available），所以要给一个桩资产，
// 否则拿到的是 404 而不是被测行为。
func newKeyUsageApp(t *testing.T) *App {
	t.Helper()
	return newTestAppWith(t, t.TempDir(), appFixture{
		opsEnabled:   true,
		webUIEnabled: true,
		configText:   keyUsageFixtureConfig,
		mutate: func(options *Options) {
			options.WebUIAssets = fs.FS(fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")},
			})
		},
	})
}

// seedKeyUsage 往当前租约那一代的指标库里写几行 Key 归属不同的记录。
//
// 用 currentMetricsAdapter 而不是另开一个 store：热重载会换库，写进另一个库的文件
// 端点读不到。
func seedKeyUsage(t *testing.T, app *App) {
	t.Helper()
	adapter := app.currentMetricsAdapter()
	if adapter == nil || adapter.store == nil {
		t.Fatal("指标不可用，无法准备测试数据")
	}
	store := adapter.store
	provA, provB := "prov-a", "prov-b"
	ok := int64(200)
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
	)
	records := []metrics.RecordParams{
		// 完整权限，prov-a/key-a。
		{ModelID: "model-a", KeyName: "key-a", Usage: usage, StatusCode: &ok,
			CallerType: "local", ProviderID: &provA},
		// 访问密钥 ak1，同一把上游 Key（两份拆分必须各归各的）。
		{ModelID: "model-a", KeyName: "key-a", Usage: usage, StatusCode: &ok,
			CallerType: "access_key", AccessKeyID: "ak1", ProviderID: &provA},
		// 另一家的同名 Key：/metrics 的 keys 会把两者并成一行，这里不能。
		{ModelID: "model-a", KeyName: "key-a", Usage: usage, StatusCode: &ok,
			CallerType: "local", ProviderID: &provB},
	}
	for _, record := range records {
		if err := store.Record(record); err != nil {
			t.Fatalf("写入 Key 指标失败: %v", err)
		}
	}
}

// TestKeyUsageEndpoint 断言 /ui/key-usage.json 的形状、拆分与显示名回填。
func TestKeyUsageEndpoint(t *testing.T) {
	app := newKeyUsageApp(t)
	seedKeyUsage(t, app)

	recorder := serve(app, http.MethodGet, "/ui/key-usage.json", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", got)
	}

	var body struct {
		UpstreamKeys []struct {
			ProviderID string `json:"provider_id"`
			KeyName    string `json:"key_name"`
			Stats      struct {
				Requests int64 `json:"requests"`
			} `json:"stats"`
		} `json:"upstream_keys"`
		ModelKeys []struct {
			ModelID    string `json:"model_id"`
			ProviderID string `json:"provider_id"`
			KeyName    string `json:"key_name"`
		} `json:"model_keys"`
		AccessKeys []struct {
			AccessKeyID   string `json:"access_key_id"`
			AccessKeyName string `json:"access_key_name"`
			Stats         struct {
				Requests int64 `json:"requests"`
			} `json:"stats"`
		} `json:"access_keys"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, recorder.Body.String())
	}

	// 两家的同名 key-a 必须各成一行；若被并成一行会只剩 1 行、requests 变成 3
	// —— 那正是这次要修的问题。
	if len(body.UpstreamKeys) != 2 {
		t.Fatalf("upstream_keys 行数 = %d，期望 2（body=%s）", len(body.UpstreamKeys), recorder.Body.String())
	}
	// prov-a 那一行是完整权限与访问密钥**共用同一把上游 Key**的合计（2 次），
	// prov-b 只有完整权限那一次。两个数字都不等于总行数 3，正好说明分组键生效了。
	requestsByProvider := map[string]int64{}
	for _, row := range body.UpstreamKeys {
		if row.KeyName != "key-a" {
			t.Errorf("upstream_keys 出现意外 Key 名 %q", row.KeyName)
		}
		requestsByProvider[row.ProviderID] = row.Stats.Requests
	}
	if got := requestsByProvider["prov-a"]; got != 2 {
		t.Errorf("prov-a/key-a requests = %d，期望 2（完整权限 + 访问密钥共用同一把 Key）", got)
	}
	if got := requestsByProvider["prov-b"]; got != 1 {
		t.Errorf("prov-b/key-a requests = %d，期望 1（同名但是另一家的 Key）", got)
	}

	if len(body.ModelKeys) != 2 {
		t.Errorf("model_keys 行数 = %d，期望 2", len(body.ModelKeys))
	}

	// 访问密钥那一份只有 ak1（1 次请求），且显示名由服务端从配置补上。
	if len(body.AccessKeys) != 1 {
		t.Fatalf("access_keys 行数 = %d，期望 1（body=%s）", len(body.AccessKeys), recorder.Body.String())
	}
	if body.AccessKeys[0].AccessKeyID != "ak1" {
		t.Errorf("access_key_id = %q，期望 ak1", body.AccessKeys[0].AccessKeyID)
	}
	if body.AccessKeys[0].AccessKeyName != "试用账号 A" {
		t.Errorf("access_key_name = %q，期望配置里的名字", body.AccessKeys[0].AccessKeyName)
	}
	if body.AccessKeys[0].Stats.Requests != 1 {
		t.Errorf("ak1 requests = %d，期望 1", body.AccessKeys[0].Stats.Requests)
	}
}

// TestKeyUsageEndpointRequiresFullAuth 断言受限凭据被拒。
//
// 内容会暴露全实例的 Key 使用情况（配置结构的投影），与 /metrics 同级。
func TestKeyUsageEndpointRequiresFullAuth(t *testing.T) {
	app := newKeyUsageApp(t)

	if recorder := serve(app, http.MethodGet, "/ui/key-usage.json", ""); recorder.Code != http.StatusUnauthorized {
		t.Errorf("无凭据状态码 = %d，期望 401", recorder.Code)
	}
	if recorder := serve(app, http.MethodGet, "/ui/key-usage.json", otherAuthorization); recorder.Code != http.StatusUnauthorized {
		t.Errorf("非完整权限状态码 = %d，期望 401", recorder.Code)
	}
}

// TestKeyUsageEndpointRejectsNonGet 断言非 GET 回 405 并带 Allow 头。
func TestKeyUsageEndpointRejectsNonGet(t *testing.T) {
	app := newKeyUsageApp(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/key-usage.json", fullAuthorization)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s 状态码 = %d，期望 405", method, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf("%s 的 Allow = %q，期望 GET", method, allow)
		}
	}
}

// TestKeyUsageEndpointValidatesHours 断言参数校验先于鉴权（与 /metrics 同序）。
func TestKeyUsageEndpointValidatesHours(t *testing.T) {
	app := newKeyUsageApp(t)

	// hours=0 越界：不带凭据也应当是 422 而不是 401。
	recorder := serve(app, http.MethodGet, "/ui/key-usage.json?hours=0", "")
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hours=0 状态码 = %d，期望 422（body=%s）", recorder.Code, recorder.Body.String())
	}
}

// TestAttachAccessKeyNamesLeavesUnknownIDsNull 断言配置里已不存在的 key 不会被编造名字。
//
// 这条路径真实存在：访问密钥被删掉之后，历史指标行仍然按 key_id 分组返回。把 id 当
// 名字回填会让「这把 key 还配着」看起来像真的，而它其实已经删了——那正是要一眼看出来的事。
func TestAttachAccessKeyNamesLeavesUnknownIDsNull(t *testing.T) {
	app := newKeyUsageApp(t)
	seedKeyUsage(t, app)
	// 删掉配置里的那把访问密钥：指标行还在，配置里已经没有它了。
	app.options.Config.AccessKeys = nil

	recorder := serve(app, http.MethodGet, "/ui/key-usage.json", fullAuthorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var body struct {
		AccessKeys []struct {
			AccessKeyID   string  `json:"access_key_id"`
			AccessKeyName *string `json:"access_key_name"`
		} `json:"access_keys"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.AccessKeys) != 1 {
		t.Fatalf("access_keys 行数 = %d，期望 1", len(body.AccessKeys))
	}
	if body.AccessKeys[0].AccessKeyName != nil {
		t.Errorf("access_key_name = %v，期望 null（配置里已无此 key）", *body.AccessKeys[0].AccessKeyName)
	}
}
