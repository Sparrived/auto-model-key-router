package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件固化访问密钥的管理面语义。
//
// 访问密钥是 Go 侧新增的能力（参照实现只有一把固定的 amkr-visitor），因此没有
// 历史版本可对照。这里钉住的是容易被后续改动破坏的部分：两份清单的三态、明文的
// 出现时机、以及「同一把凭据不许在实例里出现两次」这条底线。

const accessKeyFixture = `{
  "config_version": 4,
  "local_api_key": "local-key",
  "ops_enabled": false,
  "webui_enabled": false,
  "host": "127.0.0.1",
  "port": 8000,
  "endpoint_capabilities_path": "caps.json",
  "metrics_db_path": "metrics.sqlite3",
  "log_file_path": "server.log",
  "providers": {
    "prov-a": {"base_url": "https://a.example.test", "keys": {"key-a": {"api_key": "sk-a"}}},
    "prov-b": {"base_url": "https://b.example.test", "keys": {"key-b": {"api_key": "sk-b"}}}
  },
  "models": {
    "model-a": {"targets": [{"provider": "prov-a", "key": "key-a", "upstream_model": "model-a"}], "aliases": ["alias-a"]},
    "model-b": {"targets": [{"provider": "prov-b", "key": "key-b", "upstream_model": "model-b"}]}
  },
  "access_keys": {
    "trial": {"name": "试用账号", "key": "amkr_ak_trial", "enabled": true, "providers": ["prov-a"], "models": ["model-a"]},
    "open": {"name": "不限制", "key": "amkr_ak_open"}
  }
}`

// accessKeyServer 装配一个指向临时配置文件的 Server，返回它与配置文件路径。
func accessKeyServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")
	if err := os.WriteFile(path, []byte(accessKeyFixture), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return &Server{ConfigPath: path}, path
}

// callAccessKeys 向 /api/access-keys* 发一条请求（以本地主凭据鉴权）。
//
// 写操作需要 config_revision（防并发覆盖），而夹具文件在测试开始时没有 revision
// 字段——首次落盘后才有。因此这里让调用方传完整 body，revision 由 callAccessKeysWrite
// 补齐。
func callAccessKeys(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
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

// callAccessKeysWrite 与 callAccessKeys 相同，但把当前 config_revision 并进 body。
//
// 夹具里没有 revision 字段，而写接口要求它（防并发覆盖）。先读一次配置版本，再把它
// 拼成 JSON 对象的第一个字段。
func callAccessKeysWrite(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	revision := accessKeyRevision(t, server)
	if body == "" || body == "{}" {
		body = `{"config_revision":"` + revision + `"}`
	} else {
		body = `{"config_revision":"` + revision + `",` + strings.TrimPrefix(body, "{")
	}
	return callAccessKeys(t, server, method, path, body)
}

// accessKeyRevision 读取当前配置版本（写接口用它防并发覆盖）。
func accessKeyRevision(t *testing.T, server *Server) string {
	t.Helper()
	data, err := config.LoadConfigData(server.ConfigPath)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	revision, err := configRevision(data)
	if err != nil {
		t.Fatalf("读取配置版本失败: %v", err)
	}
	return revision
}

// decodeObject 把响应体解析成 map，失败即终止。
func decodeObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v (%s)", err, recorder.Body.String())
	}
	return parsed
}

// TestAccessKeyListNeverLeaksPlaintext 断言列表接口只回指纹。
//
// 这是整组接口最重要的一条：明文随列表散出去，等于每次打开管理页都重新泄漏一遍。
func TestAccessKeyListNeverLeaksPlaintext(t *testing.T) {
	server, _ := accessKeyServer(t)
	recorder := callAccessKeys(t, server, http.MethodGet, "/api/access-keys", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "amkr_ak_trial") {
		t.Fatal("列表响应里出现了明文 key")
	}
	if !strings.Contains(recorder.Body.String(), "key_fingerprint") {
		t.Fatal("列表响应里没有指纹")
	}
	body := decodeObject(t, recorder)
	keys, _ := body["access_keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("access_keys 长度 = %d，期望 2", len(keys))
	}
}

func TestAccessKeyListLastUsed(t *testing.T) {
	server, path := accessKeyServer(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server.AccessKeyLastUsed = func() (map[string]string, error) {
		return map[string]string{"trial": "2026-10-14T15:20:30+08:00", "deleted": "2026-10-14T16:00:00+08:00"}, nil
	}
	response := callAccessKeys(t, server, http.MethodGet, "/api/access-keys", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", response.Code, response.Body.String())
	}
	keys := decodeObject(t, response)["access_keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("目录不应包含已删除的密钥: %v", keys)
	}
	if at := keys[0].(map[string]any)["last_used_at"]; at != "2026-10-14T15:20:30+08:00" {
		t.Errorf("最近调用时间 = %v", at)
	}
	if at, present := keys[1].(map[string]any)["last_used_at"]; !present || at != nil {
		t.Errorf("无调用记录应显式返回 null: %v", keys[1])
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("读取调用时间不应修改配置, err=%v", err)
	}
	server.AccessKeyLastUsed = func() (map[string]string, error) { return nil, os.ErrClosed }
	if response := callAccessKeys(t, server, http.MethodGet, "/api/access-keys", ""); response.Code != http.StatusInternalServerError {
		t.Fatalf("指标读取失败不能伪装成无调用记录: %d %s", response.Code, response.Body.String())
	}
}

// TestAccessKeyScopeTriState 断言两份清单的三态在响应里可区分。
//
// 「省略」（不限制）与「空数组」（一个都不许）是**不同**的授权状态，列表与单条响应都
// 必须保住这个区别：让空数组顶替省略，会把一条禁令显示成「未限制」。
func TestAccessKeyScopeTriState(t *testing.T) {
	server, _ := accessKeyServer(t)
	recorder := callAccessKeys(t, server, http.MethodGet, "/api/access-keys", "")
	body := decodeObject(t, recorder)
	keys, _ := body["access_keys"].([]any)
	byID := map[string]map[string]any{}
	for _, item := range keys {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		byID[id] = entry
	}

	// 配了清单的那把：字段存在且是数组。
	trial := byID["trial"]
	if trial == nil {
		t.Fatal("响应里没有 trial 这把 key")
	}
	if _, present := trial["providers"]; !present {
		t.Error("trial 配了 providers，响应里应当出现该字段")
	}
	// 没配清单的那把：字段**缺席**（而不是空数组）。
	open := byID["open"]
	if open == nil {
		t.Fatal("响应里没有 open 这把 key")
	}
	if _, present := open["providers"]; present {
		t.Error("open 没配 providers，响应里不应出现该字段（省略 = 不限制）")
	}
	if _, present := open["models"]; present {
		t.Error("open 没配 models，响应里不应出现该字段（省略 = 不限制）")
	}
}

// TestAccessKeyCreateReturnsPlaintextOnce 断言明文只在创建响应里出现。
func TestAccessKeyCreateReturnsPlaintextOnce(t *testing.T) {
	server, _ := accessKeyServer(t)
	recorder := callAccessKeysWrite(t, server, http.MethodPost, "/api/access-keys",
		`{"name":"新的","providers":["prov-a"],"models":[]}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d，期望 201: %s", recorder.Code, recorder.Body.String())
	}
	created := decodeObject(t, recorder)
	plaintext, _ := created["key"].(string)
	if !strings.HasPrefix(plaintext, "amkr_ak_") {
		t.Fatalf("生成的 key = %q，期望 amkr_ak_ 前缀", plaintext)
	}
	// 空数组必须原样落成空数组（一个都不许），不能退化成「不限制」。
	models, present := created["models"].([]any)
	if !present || len(models) != 0 {
		t.Fatalf("models = %v，期望显式空数组", created["models"])
	}

	// 再读一次列表：新 key 的明文不该出现在里面。
	list := callAccessKeys(t, server, http.MethodGet, "/api/access-keys", "")
	if strings.Contains(list.Body.String(), plaintext) {
		t.Fatal("创建之后列表响应里出现了明文 key")
	}
}

// TestAccessKeyUpdateRequiresScopeFields 断言更新接口**必须**收到两份清单。
//
// 省略字段被当成「清除限制」是把授权悄悄放宽，因此校验层要直接拒掉。
func TestAccessKeyUpdateRequiresScopeFields(t *testing.T) {
	server, _ := accessKeyServer(t)
	recorder := callAccessKeysWrite(t, server, http.MethodPut, "/api/access-keys/trial",
		`{"name":"改了名字"}`)
	if recorder.Code == http.StatusOK {
		t.Fatalf("省略 providers/models 竟然成功了: %s", recorder.Body.String())
	}
}

// TestAccessKeyUpdateClearsScopeWithNull 断言显式 null 才是「清除限制」。
func TestAccessKeyUpdateClearsScopeWithNull(t *testing.T) {
	server, _ := accessKeyServer(t)
	recorder := callAccessKeysWrite(t, server, http.MethodPut, "/api/access-keys/trial",
		`{"providers":null,"models":null}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200: %s", recorder.Code, recorder.Body.String())
	}
	updated := decodeObject(t, recorder)
	if _, present := updated["providers"]; present {
		t.Error("providers 传 null 后不应再出现该字段")
	}
	if _, present := updated["models"]; present {
		t.Error("models 传 null 后不应再出现该字段")
	}
}

// TestAccessKeyRejectsUnknownReferences 断言清单里写错目标时明确报错。
//
// 静默接受会让某把已经分发出去的 key 少一项权限，而调用方只看到 403。
func TestAccessKeyRejectsUnknownReferences(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"未知供应商", `{"providers":["nope"],"models":null}`},
		{"未知模型", `{"providers":null,"models":["nope"]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server, _ := accessKeyServer(t)
			recorder := callAccessKeysWrite(t, server, http.MethodPut, "/api/access-keys/trial", testCase.body)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("状态码 = %d，期望 422: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// TestAccessKeyRotateReturnsNewPlaintext 断言轮换换掉明文且旧值失效。
func TestAccessKeyRotateReturnsNewPlaintext(t *testing.T) {
	server, _ := accessKeyServer(t)
	recorder := callAccessKeysWrite(t, server, http.MethodPost, "/api/access-keys/trial/rotate", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200: %s", recorder.Code, recorder.Body.String())
	}
	rotated := decodeObject(t, recorder)
	plaintext, _ := rotated["key"].(string)
	if plaintext == "" || plaintext == "amkr_ak_trial" {
		t.Fatalf("轮换后的 key = %q，期望一把新 key", plaintext)
	}
	// 旧明文必须从配置里消失。
	data, err := os.ReadFile(server.ConfigPath)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if strings.Contains(string(data), "amkr_ak_trial") {
		t.Error("轮换后配置里仍留着旧明文")
	}
}

// TestAccessKeyDelete 断言删除后该键消失、其余保留。
func TestAccessKeyDelete(t *testing.T) {
	server, _ := accessKeyServer(t)
	recorder := callAccessKeysWrite(t, server, http.MethodDelete, "/api/access-keys/trial", "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d，期望 204: %s", recorder.Code, recorder.Body.String())
	}
	list := callAccessKeys(t, server, http.MethodGet, "/api/access-keys", "")
	body := decodeObject(t, list)
	keys, _ := body["access_keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("删除后剩 %d 把 key，期望 1", len(keys))
	}
}

// TestAccessKeyMissingReturns404 断言不存在的 id 报 404 而不是静默成功。
func TestAccessKeyMissingReturns404(t *testing.T) {
	server, _ := accessKeyServer(t)
	for _, path := range []string{"/api/access-keys/nope", "/api/access-keys/nope/rotate"} {
		method := http.MethodPut
		if strings.HasSuffix(path, "/rotate") {
			method = http.MethodPost
		}
		body := ""
		if method == http.MethodPut {
			body = `{"providers":null,"models":null}`
		}
		recorder := callAccessKeysWrite(t, server, method, path, body)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s 状态码 = %d，期望 404", method, path, recorder.Code)
		}
	}
}

// TestAccessKeyRejectsDuplicateSecret 断言同一把凭据不许在实例里出现两次。
//
// 撞车会让同一把 key 的权限取决于先命中哪张清单——那是把权限边界交给判定顺序。
func TestAccessKeyRejectsDuplicateSecret(t *testing.T) {
	server, _ := accessKeyServer(t)
	// amkr_ak_open 已经被 open 那把占用。
	recorder := callAccessKeysWrite(t, server, http.MethodPost, "/api/access-keys",
		`{"name":"撞车","key":"amkr_ak_open"}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 = %d，期望 422: %s", recorder.Code, recorder.Body.String())
	}
}

// TestAccessKeyRejectsLocalAPIKey 断言访问密钥不能与本地主凭据同值。
func TestAccessKeyRejectsLocalAPIKey(t *testing.T) {
	server, _ := accessKeyServer(t)
	recorder := callAccessKeysWrite(t, server, http.MethodPost, "/api/access-keys",
		`{"name":"撞主凭据","key":"local-key"}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 = %d，期望 422: %s", recorder.Code, recorder.Body.String())
	}
}
