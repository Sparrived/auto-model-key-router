package agentconfig

import (
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// ─────────────────────────────────────────────────────────────────────────────
// 格式保真：Codex 的 config.toml 是用户手工维护的文件
//
// 本文件用三种互补的方式锁定「注释与不相关键序必须原样保留」：
//
//  1. TestCodexWriteMatchesTomlkitOracleForCommentedConfig —— 逐字节比对真实
//     tomlkit 的输出（oracle 由 tomlkit 0.15.0 生成，见常量注释）；
//  2. TestTomlInputsPreserveComments —— 对一份覆盖多种排版形状的 TOML 输入集合
//     断言注释行序列逐字节不变；
//  3. TestTomlkitRoundTripWouldLoseComments —— 反向证据：证明 go-toml/v2 的
//     Unmarshal→Marshal 会丢注释，从而说明为什么必须自己实现保真回写。
// ─────────────────────────────────────────────────────────────────────────────

// codexCommentPreserveInput 是一份「像真人写的」~/.codex/config.toml。
const codexCommentPreserveInput = `# ~/.codex/config.toml — 由我手工维护，请勿删除注释
# 第二行注释

model = "gpt-5-codex"            # 我平时用的模型
model_provider = "openai"
approval_policy = "on-request"
sandbox_mode = "workspace-write"

[model_providers.openai]
name = "OpenAI"
base_url = "https://api.openai.com/v1"
wire_api = "responses"

# 下面是我自己的工作流配置，AMKR 不该动它
[profiles.deep]
model = "gpt-5-codex"
model_reasoning_effort = "high"

[projects."/home/me/work"]
trust_level = "trusted"
`

// codexCommentPreserveOracle 是真实 tomlkit 对上面这份输入的输出。
//
// 生成方式（tomlkit 0.15.0，与 Go 侧 configureCodex 完全相同的写入序列）：
//
//	tomlkit.parse(input) → 设 model_provider /
//	model_catalog_json → model、review_model（仅当缺失或不在可用清单里时改写）
//	→ 建 model_providers.OpenAI 并设 name/base_url/wire_api/requires_openai_auth
//	→ tomlkit.dumps()
//
// 注意它是**参照实现的真实输出**，不是人手推演的期望值：注释、空行、行尾注释
// 全部保留；新键按 tomlkit 的插入规则落位（顶层新键插到「最后一个非表项之后」，
// 新的 [model_providers.OpenAI] 挂到 model_providers 这张隐式父表的末尾）。
//
// 取消路由模式后 `model` 不再被无条件删掉或改写：输入里的 "gpt-5-codex" 不在
// AMKR 的可用清单里（清单为 alpha / alpha-alias / alpha-alias-dupe / beta /
// beta-alias / unified-model，升序且 unified-model 居末），因此它在**原位**被替换成
// unified-model，用户的行尾注释与对齐空白一起保留——这正是「不得丢失用户数据」的关键断言。
//
// {{CATALOG_PATH}} 是目录文件路径的占位符：它由 t.TempDir() 决定，测试里替换。
const codexCommentPreserveOracle = `# ~/.codex/config.toml — 由我手工维护，请勿删除注释
# 第二行注释

model = "unified-model"            # 我平时用的模型
model_provider = "OpenAI"
approval_policy = "on-request"
sandbox_mode = "workspace-write"
model_catalog_json = "{{CATALOG_PATH}}"
review_model = "unified-model"

[model_providers.openai]
name = "OpenAI"
base_url = "https://api.openai.com/v1"
wire_api = "responses"

# 下面是我自己的工作流配置，AMKR 不该动它

[model_providers.OpenAI]
name = "OpenAI"
base_url = "http://127.0.0.1:8123/v1"
wire_api = "responses"
requires_openai_auth = true
[profiles.deep]
model = "gpt-5-codex"
model_reasoning_effort = "high"

[projects."/home/me/work"]
trust_level = "trusted"
`

// TestCodexWriteMatchesTomlkitOracleForCommentedConfig 是本次迁移最关键的一条
// 断言：Go 的 Codex 写路径必须与真实 tomlkit 逐字节一致。
//
// 失败意味着用户手写的注释、空行、键序或空白排版会漂移——正是「不得丢失用户
// 数据」这条硬约束要防的事。
func TestCodexWriteMatchesTomlkitOracleForCommentedConfig(t *testing.T) {
	cfg := routerConfig(t, "alpha")

	target := t.TempDir() + "/config.toml"
	writeFile(t, target, codexCommentPreserveInput)

	if _, err := Configure(Codex, cfg, Options{
		BaseDir:    t.TempDir(),
		TargetPath: target,
		BackupPath: t.TempDir() + "/backup.json",
	}); err != nil {
		t.Fatalf("Configure 失败: %v", err)
	}
	got := readFile(t, target)
	catalogPath := filepath.ToSlash(filepath.Join(filepath.Dir(target), codexCatalogFileName))
	wantOracle := strings.ReplaceAll(codexCommentPreserveOracle, "{{CATALOG_PATH}}", catalogPath)
	if got != wantOracle {
		t.Errorf("输出与 tomlkit oracle 不一致\n--- 期望 ---\n%s\n--- 实际 ---\n%s", wantOracle, got)
	}

	// 直白的可读性断言：这几条最能说明「数据没丢」。
	if comment := "# ~/.codex/config.toml — 由我手工维护，请勿删除注释"; !strings.Contains(got, comment) {
		t.Error("首行注释丢失")
	}
	if !strings.Contains(got, "model = \"unified-model\"            # 我平时用的模型") {
		t.Error("行尾注释或值之前的空白排版丢失")
	}
	if !strings.Contains(got, "# 下面是我自己的工作流配置，AMKR 不该动它") {
		t.Error("用户自定义段落前的注释丢失")
	}
	if !strings.Contains(got, "trust_level = \"trusted\"") {
		t.Error("用户自定义的表被破坏")
	}
	if !strings.Contains(got, "[projects.\"/home/me/work\"]") {
		t.Error("带引号的表名被破坏")
	}
}

// commentLines 提取注释行（保留原文，去掉行尾 CR），用于「注释序列不变」断言。
//
// 先剥掉开头的一个 BOM：参照实现用 utf-8-sig 解码，BOM 在解析前就没了，因此
// 输入带 BOM 时首行注释只有在剥掉之后才能被认出（codex_bom 用例）。
func commentLines(text string) []string {
	text = strings.TrimPrefix(text, "\ufeff")
	var out []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(strings.TrimSpace(trimmed), "#") {
			out = append(out, trimmed)
		}
	}
	return out
}

// TestTomlkitRoundTripWouldLoseComments 是选型的反向证据。
//
// 它把 doc.go 里记录的实测结论写成可执行断言：go-toml/v2 的 Unmarshal→Marshal
// 往返会把注释全部删掉，因此**不能**用它做写入路径。若未来某个版本开始保留
// 注释，本测试会失败——那时才值得重新评估是否还需要自研保真回写。
func TestTomlkitRoundTripWouldLoseComments(t *testing.T) {
	lossy, err := goTomlRoundTrip(codexCommentPreserveInput)
	if err != nil {
		t.Fatalf("go-toml/v2 往返失败: %v", err)
	}
	if len(commentLines(codexCommentPreserveInput)) == 0 {
		t.Fatal("输入里没有注释，本测试失去意义")
	}
	if len(commentLines(lossy)) != 0 {
		t.Errorf("go-toml/v2 竟然保留了注释，选型结论需要重新评估:\n%s", lossy)
	}
	if !strings.Contains(codexCommentPreserveOracle, "# 我平时用的模型") {
		t.Fatal("oracle 里没有行尾注释，本测试失去意义")
	}
	if strings.Contains(lossy, "#") {
		t.Errorf("go-toml/v2 输出里出现了 '#'，与「注释全丢」的实测结论矛盾:\n%s", lossy)
	}
}

// goTomlRoundTrip 复现「go-toml/v2 的 Unmarshal → 改 7 个键 → Marshal」这条
// 被否决的写入方案，供 TestTomlkitRoundTripWouldLoseComments 作为反向证据。
func goTomlRoundTrip(text string) (string, error) {
	var value map[string]any
	if err := toml.Unmarshal([]byte(text), &value); err != nil {
		return "", err
	}
	value["model_provider"] = "OpenAI"
	value["model"] = "unified-model"
	value["review_model"] = "unified-model"
	value["model_reasoning_effort"] = "high"
	providers, _ := value["model_providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
		value["model_providers"] = providers
	}
	provider, _ := providers["OpenAI"].(map[string]any)
	if provider == nil {
		provider = map[string]any{}
		providers["OpenAI"] = provider
	}
	provider["name"] = "OpenAI"
	provider["base_url"] = "http://127.0.0.1:8123/v1"
	provider["wire_api"] = "responses"
	provider["requires_openai_auth"] = true
	out, err := toml.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// readFile 读文本文件，失败即 Fatal。
func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := readFileOrEmpty(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(content)
}

// writeFile 写入文本文件（自动建目录）。
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := writeAtomic(path, []byte(text)); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}
