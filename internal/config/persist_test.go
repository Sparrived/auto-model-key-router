package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// TestSaveConfigDataMatchesPythonIndent 断言落盘文本与 Python 的
// “json.dumps(data, indent=2, ensure_ascii=False) + "\n"“ 逐字节一致。
//
// 这是「用户直接换二进制」承诺的核心：配置文件必须能被两种实现互相读写而不产生
// 差异。期望文本由 Python 实测得到（gen_config_model_corpus.py（已随 Python 退役移除） 同源）。
func TestSaveConfigDataMatchesPythonIndent(t *testing.T) {
	// 键序与嵌套刻意打乱，用来验证保存路径**不排序**（排序是 canonical.Dumps
	// 的行为，落盘走的是顺序保留路径，两者不能混用）。
	raw := `{"config_version":4,"port":8000,"host":"127.0.0.1","nested":{"z":1,"a":[1,2,{"k":"v"}]},"empty_obj":{},"empty_arr":[],"unicode":"中文","float":60.0,"int":60}`
	data, err := canonical.ParseString(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")
	if err := SaveConfigData(path, data); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}

	// Python: json.dumps(json.loads(raw), indent=2, ensure_ascii=False) + "\n"
	expected := `{
  "config_version": 4,
  "port": 8000,
  "host": "127.0.0.1",
  "nested": {
    "z": 1,
    "a": [
      1,
      2,
      {
        "k": "v"
      }
    ]
  },
  "empty_obj": {},
  "empty_arr": [],
  "unicode": "中文",
  "float": 60.0,
  "int": 60
}
`
	// 这里把落盘内容归一成 LF 再比对，专注于**结构**（缩进、键序、空容器、
	// 浮点写法）；平台换行翻译由 TestSaveNewlineMatchesPythonTextMode 与
	// TestPythonWrittenConfigRoundTripsByteIdentical 负责断言。
	normalized := strings.ReplaceAll(string(written), "\r\n", "\n")
	if normalized != expected {
		t.Errorf("落盘内容与 Python 不一致\n期望:\n%s\n实际:\n%s", expected, normalized)
	}
}

// TestSaveConfigDataRefusesToLeaveTempFiles 断言临时文件被清理。
//
// 保存路径会在配置目录写 .<name>.<hex>.tmp，失败或成功后都必须清掉；否则配置
// 目录会随每次保存堆积垃圾文件。
func TestSaveConfigDataRefusesToLeaveTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")
	data := mustParse(t, `{"config_version":4}`)
	if err := SaveConfigData(path, data); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("残留临时文件: %s", entry.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("目录内文件数 %d，期望 1（只有配置文件）", len(entries))
	}
}

// TestSaveConfigDataOverwriteIsAtomic 断言覆盖写入后内容完整。
func TestSaveConfigDataOverwriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")

	first := mustParse(t, `{"config_version":4,"note":"first"}`)
	if err := SaveConfigData(path, first); err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	second := mustParse(t, `{"config_version":4,"note":"second","extra":[1,2,3]}`)
	if err := SaveConfigData(path, second); err != nil {
		t.Fatalf("覆盖保存失败: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !strings.Contains(string(content), `"second"`) {
		t.Errorf("覆盖后内容不正确:\n%s", string(content))
	}
	if strings.Contains(string(content), `"first"`) {
		t.Errorf("覆盖后仍含旧内容:\n%s", string(content))
	}
	// 重新解析应得到与写入等价的结构。
	reloaded, err := LoadConfigData(path)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if got := canonical.Dumps(reloaded); got != canonical.Dumps(second) {
		t.Errorf("往返不一致\n期望: %s\n实际: %s", canonical.Dumps(second), got)
	}
}

// TestLoadConfigDataCreatesDefaults 断言首次加载会创建含默认值的配置文件。
func TestLoadConfigDataCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "router-config.json")

	data, err := LoadConfigData(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if version, err := configVersionOf(data); err != nil || version != CONFIG_VERSION {
		t.Fatalf("默认 config_version 应为 %d，实际 %d (err=%v)", CONFIG_VERSION, version, err)
	}
	localAPIKey, _ := data.Lookup("local_api_key").AsString()
	if !strings.HasPrefix(localAPIKey, "amkr_") {
		t.Errorf("local_api_key 应以 amkr_ 开头，实际 %q", localAPIKey)
	}
	// 43 字符 base64url + "amkr_" 前缀 = 48。
	if len(localAPIKey) != 48 {
		t.Errorf("local_api_key 长度 %d，期望 48（amkr_ + 43）", len(localAPIKey))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("配置文件未被创建: %v", err)
	}
	// 默认配置必须能通过自身校验，否则首次启动即失败。
	if _, err := FromDict(data); err != nil {
		t.Errorf("默认配置未通过校验: %v", err)
	}
}

// TestEmptyConfigDictKeyOrder 锁住默认配置的键序。
//
// 顺序决定新建配置文件的字节内容；改动顺序会让「新建默认配置」在不同版本间
// 产生差异，因此显式固化。
func TestEmptyConfigDictKeyOrder(t *testing.T) {
	data, err := EmptyConfigDict()
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	expected := []string{
		"config_version", "host", "port", "default_base_url", "upstream_routes",
		"request_timeout", "stream_first_byte_timeout", "stream_idle_timeout",
		"max_retries", "key_failure_threshold", "key_cooldown_seconds",
		"key_balance_cooldown_seconds", "quota_freeze_interval_seconds",
		"quota_freeze_threshold_5h", "quota_freeze_threshold_7d",
		"quota_freeze_threshold_whitelist",
		"endpoint_capabilities_path", "metrics_db_path", "log_file_path",
		"local_api_key", "webui_enabled", "ops_enabled", "providers", "models",
	}
	got := data.Obj.Keys()
	if len(got) != len(expected) {
		t.Fatalf("字段数 %d，期望 %d\n实际顺序: %v", len(got), len(expected), got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("第 %d 个字段是 %q，期望 %q\n完整顺序: %v", i, got[i], expected[i], got)
		}
	}
	// 各标量字段的默认值属对外契约。
	checks := map[string]string{
		"host":                             `"127.0.0.1"`,
		"port":                             "8000",
		"default_base_url":                 `"https://api.openai.com"`,
		"request_timeout":                  "60",
		"stream_first_byte_timeout":        "60",
		"stream_idle_timeout":              "60",
		"max_retries":                      "2",
		"key_failure_threshold":            "2",
		"key_cooldown_seconds":             "60",
		"key_balance_cooldown_seconds":     "300",
		"quota_freeze_interval_seconds":    "60",
		"quota_freeze_threshold_5h":        "0.0",
		"quota_freeze_threshold_7d":        "0.0",
		"quota_freeze_threshold_whitelist": "[]",
		"webui_enabled":                    "true",
		"ops_enabled":                      "true",
		"config_version":                   "4",
	}
	for key, want := range checks {
		got := canonical.Dumps(data.Lookup(key))
		if got != want {
			t.Errorf("默认值 %s 期望 %s，实际 %s", key, want, got)
		}
	}
}

// TestGenerateLocalAPIKeyFormat 断言 key 格式与长度分布。
//
// token_urlsafe(32) 固定产出 43 个 URL 安全字符（无 '=' 填充），前缀 amkr_。
// 部署脚本按该形状识别本地 key，因此长度是契约的一部分。
func TestGenerateLocalAPIKeyFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		key, err := GenerateLocalAPIKey()
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if !strings.HasPrefix(key, "amkr_") {
			t.Fatalf("缺少 amkr_ 前缀: %q", key)
		}
		body := key[len("amkr_"):]
		if len(body) != 43 {
			t.Fatalf("主体长度 %d，期望 43: %q", len(body), key)
		}
		if strings.ContainsAny(body, "+/=") {
			t.Fatalf("主体含非 URL 安全字符: %q", key)
		}
		if seen[key] {
			t.Fatalf("出现重复 key: %q", key)
		}
		seen[key] = true
	}
}

// TestResolveConfigPathPrecedence 断言路径解析优先级。
func TestResolveConfigPathPrecedence(t *testing.T) {
	// 显式路径优先于环境变量。
	t.Setenv(ConfigPathEnv, filepath.Join(t.TempDir(), "from-env.json"))
	explicit := filepath.Join(t.TempDir(), "explicit.json")
	got, err := ResolveConfigPath(explicit)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got != explicit {
		t.Errorf("显式路径应优先，期望 %s，实际 %s", explicit, got)
	}

	// 无显式路径时用环境变量。
	envPath := filepath.Join(t.TempDir(), "from-env.json")
	t.Setenv(ConfigPathEnv, envPath)
	got, err = ResolveConfigPath("")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got != envPath {
		t.Errorf("应使用环境变量路径，期望 %s，实际 %s", envPath, got)
	}
}

// TestLoadMigratesLegacyVersionInPlace 断言旧版本配置被就地升级并写回。
//
// 这是对外可见行为：用新版本启动会改写磁盘上的配置文件。
func TestLoadMigratesLegacyVersionInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router-config.json")
	legacy := `{"config_version":3,"local_api_key":"k","providers":{"p":{"base_url":"https://a.example","keys":{"k1":{"api_key":"1"}},"pools":{"pool":{"keys":["k1"]}}}},"models":{"m":{"targets":[{"provider":"p","pool":"pool","upstream_model":"u"}]}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("写入旧配置失败: %v", err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatalf("加载旧配置失败: %v", err)
	}
	if len(config.Models) != 1 || len(config.Models[0].Keys) != 1 {
		t.Fatalf("迁移后结构异常: %+v", config.Models)
	}
	if config.Models[0].Keys[0].UpstreamModel != "u" {
		t.Errorf("upstream_model 应为 u，实际 %q", config.Models[0].Keys[0].UpstreamModel)
	}

	// 磁盘上的文件应已被升级为 v4 并写成整数。
	reloaded, err := LoadConfigData(path)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if got := canonical.Dumps(reloaded.Lookup("config_version")); got != "4" {
		t.Errorf("写回后 config_version 应为整数 4，实际 %s", got)
	}
}
