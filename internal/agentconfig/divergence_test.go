package agentconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// 刻意差异的具名测试
//
// doc.go 里列的每一条差异（D1-D7）在这里都有一条同名测试。这样「差异」不是
// 注释里的说法，而是可执行、可失败的契约；将来若有人改动行为，会先撞到这些
// 测试，被迫回来更新 doc.go。
// ─────────────────────────────────────────────────────────────────────────────

// pinBaseEnv 把 home 与各平台缓存目录钉进 root。
//
// 这是「绝不写进开发者真实用户目录」的硬保证：任何一条用例忘了钉，都会在
// 备份默认路径这类用例上暴露出来。
func pinBaseEnv(t *testing.T, root string) {
	t.Helper()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "cache"))
	t.Setenv("APPDATA", filepath.Join(root, "cache-roaming"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg-cache"))
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "PI_CODING_AGENT_DIR", "CODEX_HOME"} {
		t.Setenv(name, "")
	}
}

// testConfigs 是各用例需要的路由配置（内联字面量）。
//
// 刻意走 config.FromDict：这样配置解析的一致性仍在覆盖范围内。
var testConfigs = map[string]string{
	"alpha": `{"config_version":4,"host":"127.0.0.1","port":8123,` +
		`"local_api_key":"amkr_corpus_local_key",` +
		`"providers":{"openai":{"base_url":"https://api.openai.com","keys":{` +
		`"k1":{"api_key":"s1","enabled":true},"disabled":{"api_key":"s2","enabled":false}}}},` +
		`"models":{"alpha":{"aliases":["alpha-alias"],"reasoning_effort":"high","targets":[` +
		`{"provider":"openai","key":"k1","upstream_model":"gpt-4o"},` +
		`{"provider":"openai","key":"disabled","upstream_model":"gpt-4o-mini"}]},` +
		`"beta":{"aliases":["beta-alias","alpha-alias-dupe"],"targets":[` +
		`{"provider":"openai","key":"k1","upstream_model":"gpt-5"}]},` +
		`"gamma":{"targets":[{"provider":"openai","key":"disabled","upstream_model":"gpt-3.5"}]}},` +
		`"unified_model":{"default":{"primary":{"model":"alpha","key":"k1"}}}}`,
	"no_unified": `{"config_version":4,"host":"127.0.0.1","port":8123,` +
		`"local_api_key":"amkr_corpus_local_key",` +
		`"providers":{"openai":{"base_url":"https://api.openai.com","keys":{` +
		`"k1":{"api_key":"s1","enabled":true}}}},` +
		`"models":{"alpha":{"targets":[{"provider":"openai","key":"k1","upstream_model":"gpt-4o"}]}}}`,
	"empty_key": `{"config_version":4,"host":"127.0.0.1","port":8123,"local_api_key":"",` +
		`"providers":{"openai":{"base_url":"https://api.openai.com","keys":{` +
		`"k1":{"api_key":"s1","enabled":true}}}},` +
		`"models":{"alpha":{"targets":[{"provider":"openai","key":"k1","upstream_model":"gpt-4o"}]}},` +
		`"unified_model":{"default":{"primary":{"model":"alpha","key":"k1"}}}}`,
}

// routerConfig 解析一份内联配置。
func routerConfig(t *testing.T, name string) *config.RouterConfig {
	t.Helper()
	raw, ok := testConfigs[name]
	if !ok {
		t.Fatalf("没有配置 %q", name)
	}
	value, err := canonical.ParseString(raw)
	if err != nil {
		t.Fatalf("解析配置 %q 失败: %v", name, err)
	}
	parsed, err := config.FromDict(value)
	if err != nil {
		t.Fatalf("配置 %q 无法通过 Go 侧校验: %v", name, err)
	}
	return parsed
}

// jsonEscapePath 把路径转成它出现在 JSON 字符串里的样子。
func jsonEscapePath(path string) string {
	replaced := strings.ReplaceAll(path, `\`, `\\`)
	return strings.ReplaceAll(replaced, `"`, `\"`)
}

// TestDivergenceHomeIsInjected 固定 D1：目标路径完全由 Options.BaseDir 推导，
// 绝不读开发者真实的 home。
//
// 这是本包最重要的一条安全属性：任何一次漏注入都会把用户的 ~/.claude 等目录
// 交给测试改写。
func TestDivergenceHomeIsInjected(t *testing.T) {
	root := t.TempDir()
	pinBaseEnv(t, root)

	cases := []struct {
		agent string
		want  string
	}{
		{ClaudeCode, filepath.Join(root, ".claude", "settings.json")},
		{Codex, filepath.Join(root, ".codex", "config.toml")},
		{PiAgent, filepath.Join(root, ".pi", "agent", "models.json")},
	}
	for _, c := range cases {
		got, err := TargetPath(root, c.agent)
		if err != nil {
			t.Fatalf("TargetPath(%s) 失败: %v", c.agent, err)
		}
		if got != c.want {
			t.Errorf("%s 的目标路径期望 %q，实际 %q", c.agent, c.want, got)
		}
		if !strings.HasPrefix(got, root) {
			t.Errorf("%s 的目标路径 %q 逃出了注入的 BaseDir %q", c.agent, got, root)
		}
	}

	backup, err := BackupPath(Options{BaseDir: root}, Codex)
	if err != nil {
		t.Fatalf("BackupPath 失败: %v", err)
	}
	if !strings.HasPrefix(backup, root) {
		t.Errorf("默认备份路径 %q 逃出了注入的 BaseDir（LOCALAPPDATA 没被钉住？）", backup)
	}
}

// D2（Status.Mode 用空串表示 None）已随「路由模式」的取消而失效：Status 不再有
// Mode 字段，三个客户端一律注入全部可用模型。

// TestDivergenceErrorTexts 固定 D3：模块自身的错误文本逐字一致；底层解析器的
// 错误只保证前缀一致（不同解析器的措辞必然不同）。
//
// 与模式相关的两条错误（不支持的 Agent 路由模式 / Pi Agent 仅支持 unified-model
// 模式）以及「请先配置 unified-model」已随模式一并移除，不再有用例。
func TestDivergenceErrorTexts(t *testing.T) {
	exact := []struct {
		name string
		run  func(root string) error
		want string
	}{
		{
			name: "unsupported_agent",
			run: func(root string) error {
				_, err := TargetPath(root, "nope")
				return err
			},
			want: "不支持的 Agent: nope",
		},
		{
			name: "empty_local_key",
			run: func(root string) error {
				_, err := Configure(ClaudeCode, routerConfig(t, "empty_key"), Options{
					BaseDir: root, TargetPath: filepath.Join(root, "s.json"),
					BackupPath: filepath.Join(root, "b.json"),
				})
				return err
			},
			want: "本地鉴权 key 为空，无法配置 Agent",
		},
	}
	for _, c := range exact {
		t.Run("exact/"+c.name, func(t *testing.T) {
			root := t.TempDir()
			pinBaseEnv(t, root)
			err := c.run(root)
			var configErr *ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("期望 *ConfigError %q，实际 %v", c.want, err)
			}
			if configErr.Message != c.want {
				t.Errorf("错误文本期望 %q，实际 %q", c.want, configErr.Message)
			}
		})
	}

	// 底层解析器错误：前缀逐字一致，剩余部分是各自解析器的措辞。
	prefixCases := []struct {
		name   string
		agent  string
		path   string
		init   string
		prefix string
	}{
		{"claude_invalid_json", ClaudeCode, "settings.json", "{ 不是 json\n",
			"Claude Code 配置不是有效的 UTF-8 JSON: "},
		{"pi_invalid_json", PiAgent, "models.json", "{ 不是 json\n",
			"Pi Agent 配置不是有效的 UTF-8 JSON: "},
		{"codex_invalid_toml", Codex, "config.toml", "model = \"未闭合\n",
			"Codex 配置不是有效的 UTF-8 TOML: "},
		{"claude_invalid_utf8", ClaudeCode, "settings.json", string([]byte{0xff, 0xfe}) + "\n",
			"Claude Code 配置不是有效的 UTF-8 JSON: "},
	}
	for _, c := range prefixCases {
		t.Run("prefix/"+c.name, func(t *testing.T) {
			root := t.TempDir()
			pinBaseEnv(t, root)
			target := filepath.Join(root, c.path)
			writeFile(t, target, c.init)
			_, err := Configure(c.agent, routerConfig(t, "alpha"), Options{
				BaseDir: root, TargetPath: target, BackupPath: filepath.Join(root, "b.json"),
			})
			var configErr *ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("期望 *ConfigError，实际 %v", err)
			}
			if !strings.HasPrefix(configErr.Message, c.prefix) {
				t.Errorf("错误前缀期望 %q，实际 %q", c.prefix, configErr.Message)
			}
		})
	}
}

// TestDivergenceBackupJSONUsesEnsureASCII 固定 D4：备份文件是
// json.dumps(indent=2, ensure_ascii=True) + "\n"，与写 Agent 配置时的
// ensure_ascii=False 相反。
func TestDivergenceBackupJSONUsesEnsureASCII(t *testing.T) {
	root := t.TempDir()
	pinBaseEnv(t, root)
	// 目标路径里带非 ASCII，逼出 ensure_ascii=True 的差别。
	target := filepath.Join(root, "中文目录", "config.toml")
	backup := filepath.Join(root, "备份", "codex.json")
	writeFile(t, target, "model = \"gpt-5\"\n")
	if _, err := Configure(Codex, routerConfig(t, "alpha"), Options{
		BaseDir: root, TargetPath: target, BackupPath: backup,
	}); err != nil {
		t.Fatalf("Configure 失败: %v", err)
	}
	raw := readFile(t, backup)
	if !strings.Contains(raw, `\u4e2d\u6587\u76ee\u5f55`) {
		t.Errorf("备份里的中文路径应被 \\uXXXX 转义（ensure_ascii=True），实际:\n%s", raw)
	}
	if !strings.HasSuffix(raw, "}\n") {
		t.Error("备份缺少末尾换行")
	}
	// version 3 起不再写 mode（集成没有路由模式），键序仍是插入顺序、indent=2。
	if !strings.Contains(raw, "\n  \"version\": 3,\n") {
		t.Error("备份不是 indent=2 且键序为插入顺序")
	}
	if strings.Contains(raw, `"mode"`) {
		t.Error("备份不应再写 mode 字段")
	}

	// 对照：写 Agent 配置时是 ensure_ascii=False，中文必须原样出现。
	piTarget := filepath.Join(root, "models.json")
	if _, err := Configure(PiAgent, routerConfig(t, "alpha"), Options{
		BaseDir: root, TargetPath: piTarget, BackupPath: filepath.Join(root, "b2.json"),
	}); err != nil {
		t.Fatalf("Configure(Pi) 失败: %v", err)
	}
	if strings.Contains(readFile(t, piTarget), `\u`) {
		t.Error("Agent 配置里出现了 \\u 转义，说明没有用 ensure_ascii=False")
	}
}

// TestDivergenceBackupFieldsAreZeroValued 固定 D5：备份字段缺失时 Go 一律当
// 空值 / 假值处理，不抛 KeyError。
func TestDivergenceBackupFieldsAreZeroValued(t *testing.T) {
	root := t.TempDir()
	pinBaseEnv(t, root)
	target := filepath.Join(root, "config.toml")
	backup := filepath.Join(root, "backup.json")
	writeFile(t, target, "model = \"gpt-5\"\n")
	// 一份「结构合法但字段残缺」的备份：缺 original_exists / original_content。
	// 它同时是一份**旧格式**备份（version 2 + mode），用来锁定「旧备份仍能读」。
	writeFile(t, backup, `{
  "version": 2,
  "agent": "codex",
  "mode": "unified-model",
  "target_path": "`+jsonEscapePath(target)+`",
  "applied_sha256": "0"
}`+"\n")

	status, err := GetStatus(Codex, Options{BaseDir: root, TargetPath: target, BackupPath: backup})
	if err != nil {
		t.Fatalf("GetStatus 失败: %v", err)
	}
	// backup_available 只看 (agent, target_path)，与 applied_sha256 无关
	// （agent_config.py:103）；applied_sha256 只影响 current_is_applied。
	if !status.BackupAvailable {
		t.Error("备份属于该 Agent 且目标路径一致，backup_available 应为 true")
	}
	if status.CurrentIsApplied {
		t.Error("applied_sha256 与磁盘不符，current_is_applied 应为 false")
	}
	// 磁盘内容与 applied_sha256 不符，等价于「用户又改过」，因此回退必须成功，
	// 且 original_exists 缺失按 false 处理 —— 结果是把目标文件删掉。
	result, err := Rollback(Codex, Options{BaseDir: root, TargetPath: target, BackupPath: backup})
	if err != nil {
		t.Fatalf("Rollback 失败: %v", err)
	}
	if !result.Restored {
		t.Error("Rollback 应报告 restored=true")
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Error("original_exists 缺失应按 false 处理：目标文件应被删除")
	}
}

// TestDivergenceUnsupportedTomlShapesError 固定 D6：Go 侧不支持的 TOML 形状
// 一律显式报错、绝不写出任何文件。
//
// 这一类是「宁可报错也不丢数据」的取舍：Python tomlkit 在这些形状上要么抛未
// 包装的 TypeError，要么产出「部分行内、部分多行」的古怪结果，Go 都不复刻。
func TestDivergenceUnsupportedTomlShapesError(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"dotted_key", "model_providers.OpenAI.name = \"dotted\"\n"},
		{"inline_table", "model_providers = { OpenAI = { name = \"inline\" } }\n"},
		{"array_of_tables", "[[model_providers.OpenAI]]\nname = \"aot\"\n"},
		{"array_value", "model_providers = [1, 2]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			pinBaseEnv(t, root)
			target := filepath.Join(root, "config.toml")
			backup := filepath.Join(root, "backup.json")
			writeFile(t, target, c.input)

			_, err := Configure(Codex, routerConfig(t, "alpha"), Options{
				BaseDir: root, TargetPath: target, BackupPath: backup,
			})
			var configErr *ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("期望显式 *ConfigError，实际 %v", err)
			}
			// 硬约束：报错时绝不能留下半成品。
			if got := readFile(t, target); got != c.input {
				t.Errorf("报错后目标文件被改动\n期望 %q\n实际 %q", c.input, got)
			}
			if _, statErr := os.Stat(backup); statErr == nil {
				t.Error("报错时不应写出备份文件")
			}
		})
	}
}

// TestDivergenceResolvePathIsLexical 固定 D7：路径规范化只做词法处理
// （filepath.Abs + Clean），与 Python 的 Path.resolve(strict=False) 给出同样的
// 字符串——".."、"." 与 "~" 都按词法展开，不做符号链接解析。
func TestDivergenceResolvePathIsLexical(t *testing.T) {
	root := t.TempDir()
	pinBaseEnv(t, root)
	cases := []struct {
		input string
		want  string
	}{
		{"<root>/a/../b/c", "<root>/b/c"},
		{"<root>/x/y/../../z.toml", "<root>/z.toml"},
		{"~/nested/./file.toml", "<root>/nested/file.toml"},
	}
	for _, c := range cases {
		// 直接拼接而不是 filepath.Join：用例刻意保留 ".." 与 "~"，
		// Join 会先做词法规范化而抹掉被测量的行为。
		input := c.input
		if strings.HasPrefix(input, "<root>") {
			input = root + filepath.FromSlash(strings.TrimPrefix(input, "<root>"))
		}
		got, err := resolvePath(root, input)
		if err != nil {
			t.Fatalf("resolvePath(%q) 失败: %v", c.input, err)
		}
		want := root + filepath.FromSlash(strings.TrimPrefix(c.want, "<root>"))
		if got != want {
			t.Errorf("resolvePath(%q) 期望 %q，实际 %q", c.input, want, got)
		}
	}
}
