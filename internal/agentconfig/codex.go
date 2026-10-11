package agentconfig

import (
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// configureCodex 改写 ~/.codex/config.toml（agent_config.py:310）。
//
// 这是整个模块唯一需要**格式保真**的地方：config.toml 由用户手工维护，里面
// 通常有注释与自定义表，而 tomlkit 会把这些原样保留。Go 侧用 tomlkit.go 里的
// 迷你 tomlkit 复刻同一套 body/trivia 模型，只动 AMKR 拥有的键：
//
//	model_provider / model_catalog_json
//	model_providers.OpenAI.{name,base_url,wire_api,requires_openai_auth}
//
// 除了上面这些键，其余内容（注释、空行、`=` 两侧空白、行尾注释、CRLF）逐字节不变。
// 唯一会动的结构性细节是 **TOML 表的顺序**：写入 [model_providers.OpenAI] 时
// go-toml 会把同属 model_providers 的表归并到一起，因此它可能挪到父表组的末尾
// （实测：夹在两个 [model_providers.*] 之间的自定义表会被排到 OpenAI 之后）。语义
// 完全等价，且回退恢复的是原始文件本身，不会留下痕迹。
//
// model / review_model 只在「缺失或指向 AMKR 不认识的名字」时才改写：`model` 是
// **新会话的默认模型**（它不阻止 `/model` 切换），但如果它指向一个 AMKR 不提供的
// 名字，新会话就会打到一个必然失败的模型上，因此这种失效默认值必须修掉；用户选的、
// AMKR 也能提供的模型则原样保留。model_reasoning_effort 完全不动：它是用户对自己
// 模型的偏好，与路由无关。
//
// 模型清单由 catalogPath 指向的目录文件给出（见 codexCatalogJSON）。注意目录文件要
// 生效，客户端必须**真的支持 model_catalog_json**：该键首次出现在 Codex 0.105.0
// （官方 PR#12101），更老的版本会直接忽略它。要求与自查方式见 README。
//
// 注意 provider 名是大小写敏感的 "OpenAI"：用户自己的
// `[model_providers.openai]` 不会被覆盖，两者在 Codex 里是两个 provider。
func configureCodex(current []byte, cfg *config.RouterConfig, catalogPath string, legacy AgentLegacyPlan) ([]byte, error) {
	// 空文档对应 tomlkit.document()。**实测**它的根容器 _parsed 是 false，
	// 因此新增键会走「插入到最后一个非表项之后」，新建表也会拿到一个前导换行
	// （这正是 `model = "v"\n\n[model_providers.OpenAI]` 里空行的来源）。
	root := &tkContainer{}
	if len(current) > 0 {
		text, err := decodeUTF8Sig("Codex", current)
		if err != nil {
			return nil, err
		}
		// 先用 go-toml/v2 的 unstable parser 走一遍做校验：迷你 tomlkit 为了
		// 保真而刻意宽松（值只做原文扫描），不校验就可能把 Python 会拒绝的
		// 坏文件「修好」再回写。
		if err := validateTOML(text); err != nil {
			return nil, errf("Codex 配置不是有效的 UTF-8 TOML: %v", err)
		}
		parsed, parseErr := parseTOMLDocument(text)
		if parseErr != nil {
			return nil, errf("Codex 配置不是有效的 UTF-8 TOML: %v", parseErr)
		}
		root = parsed
	}

	document := &codexTable{container: root}
	document.setScalar("model_provider", quoteTOMLString(codexProviderID))
	// 模型清单交给目录文件（Codex 的 model_catalog_json）。
	document.setScalar("model_catalog_json", quoteTOMLString(filepath.ToSlash(catalogPath)))
	// model / review_model / model_reasoning_effort：按 legacy 计划处置——集成前
	// 就有的键恢复用户原值，集成前没有的（AMKR 自己写进去的）删除。无条件删除会
	// 伤到用户自设的模型与推理档位，无条件保留又会把 AMKR 的旧钉死值留下来。
	for _, key := range legacy.Remove {
		document.remove(key)
	}
	for _, item := range legacy.Restore {
		document.setScalar(item.Name, quoteTOMLString(item.Value))
	}
	// model / review_model 还要保证可用：它们是**新会话的默认模型**，指向 AMKR
	// 不认识的名字会让新会话直接打到不存在的模型上（`/model` 的切换不受它们影响）。
	names := availableModelNames(cfg)
	for _, key := range []string{"model", "review_model"} {
		if currentValue, ok := document.scalarString(key); ok && containsName(names, currentValue) {
			continue
		}
		document.setScalar(key, quoteTOMLString(preferredModelName(names)))
	}

	providers, err := document.subTable("model_providers", "model_providers")
	if err != nil {
		return nil, err
	}
	provider, err := providers.subTable(codexProviderID, "model_providers."+codexProviderID)
	if err != nil {
		return nil, err
	}
	provider.setScalar("name", quoteTOMLString(codexProviderID))
	provider.setScalar("base_url", quoteTOMLString(RouterOrigin(cfg)+"/v1"))
	provider.setScalar("wire_api", quoteTOMLString("responses"))
	provider.setScalar("requires_openai_auth", "true")
	return []byte(root.Render()), nil
}

// codexCatalogJSON 生成 Codex 的模型目录文件内容（model_catalog_json 指向它）。
//
// schema 是 codex-rs/protocol/src/openai_models.rs::ModelInfo。**按官方源码核对**
// （rust-v0.162.1、rust-v0.105.0 与 main 三个版本对比）以下 10 个字段既不是 Option、
// 也没有 serde default，缺一个就无法反序列化：slug / display_name /
// supported_reasoning_levels / shell_type / visibility / supported_in_api / priority /
// support_verbosity / truncation_policy / experimental_supported_tools。因此这里逐字
// 写全，而不是只写 slug 与 display_name（Vec 字段写 `[]`，非 Option 的标量字段不能
// 用 null 顶替）。
//
// 另外两条硬约束（都会让 Codex **启动失败**，不是降级）：
//
//  1. models 数组不能为空。调用方（Configure）在没有任何可用模型时直接报错，
//     绝不写出空目录——那会让用户连 codex 都起不来。
//  2. 自定义 slug 不会继承内置提示词：base_instructions 与
//     model_messages.instructions_template 至少要有一个非空字符串，否则该模型的
//     指令区是空的。这里给一段中性的通用说明，用户要改可以自己编辑目录文件
//     （它由 AMKR 独占，重新应用集成会被覆盖）。
//
// 刻意**不写** context_window / max_context_window：目录里的上下文窗口决定 Codex
// 的截断预算，AMKR 无法知道上游真实窗口，编一个数会静默截断或溢出。省略后由 Codex
// 的默认值兜底。
//
// 兼容性：catalog 最初支持的 0.105.0 与 0.148-alpha 把 supports_reasoning_summaries
// 与 supports_parallel_tool_calls 也当作必填，因此这里补上 false；新 schema 已移除这
// 两个字段，但解析时没有 deny_unknown_fields，未知键被忽略。supports_reasoning_summary_parameter
// 与 input_modalities 显式写死，避免 AMKR 的通用模型被当成支持 reasoning summary /
// 图片输入。
//
// 注意「schema 兼容」不等于「客户端支持这个功能」：model_catalog_json 本身要较新的
// Codex 才会读，旧版本会直接忽略它。README 里已写明版本要求。
func codexCatalogJSON(cfg *config.RouterConfig) []byte {
	names := availableModelNames(cfg)
	models := make([]*canonical.Value, 0, len(names))
	for _, name := range names {
		models = append(models, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "slug", Value: canonical.NewString(name)},
			canonical.ObjectPair{Key: "display_name", Value: canonical.NewString(name)},
			canonical.ObjectPair{Key: "description", Value: canonical.NewString("由本机 AMKR 路由：" + name)},
			canonical.ObjectPair{Key: "base_instructions", Value: canonical.NewString(codexBaseInstructions)},
			canonical.ObjectPair{Key: "supported_reasoning_levels", Value: canonical.NewArray()},
			canonical.ObjectPair{Key: "shell_type", Value: canonical.NewString("unified_exec")},
			// list + supported_in_api 才能出现在 picker 里。
			canonical.ObjectPair{Key: "visibility", Value: canonical.NewString("list")},
			canonical.ObjectPair{Key: "supported_in_api", Value: canonical.NewBool(true)},
			canonical.ObjectPair{Key: "priority", Value: canonical.NewIntValue(0)},
			canonical.ObjectPair{Key: "support_verbosity", Value: canonical.NewBool(false)},
			canonical.ObjectPair{Key: "truncation_policy", Value: canonical.NewObjectOf(
				canonical.ObjectPair{Key: "mode", Value: canonical.NewString("tokens")},
				canonical.ObjectPair{Key: "limit", Value: canonical.NewIntValue(codexTruncationLimit)},
			)},
			canonical.ObjectPair{Key: "experimental_supported_tools", Value: canonical.NewArray()},
			// 老客户端必填、新客户端忽略的两个能力开关：一律 false（不谎报能力）。
			canonical.ObjectPair{Key: "supports_reasoning_summaries", Value: canonical.NewBool(false)},
			canonical.ObjectPair{Key: "supports_parallel_tool_calls", Value: canonical.NewBool(false)},
			canonical.ObjectPair{Key: "supports_reasoning_summary_parameter", Value: canonical.NewBool(false)},
			canonical.ObjectPair{Key: "input_modalities", Value: canonical.NewArray(canonical.NewString("text"))},
		))
	}
	return []byte(canonical.DumpsIndent(canonical.NewObjectOf(
		canonical.ObjectPair{Key: "models", Value: canonical.NewArray(models...)},
	), 2) + "\n")
}

// codexBaseInstructions 是写进目录文件的中性基础指令。
//
// Codex 对自定义 slug 不会套用内置提示词，instructions 为空会让模型在没有系统
// 指令的情况下工作。这段文本只做最低限度的工作区协作说明，不模仿任何厂商提示词。
const codexBaseInstructions = "You are a coding agent working in the user's workspace through a local API router. " +
	"Follow the user's request, inspect the repository before changing it, and keep changes minimal and verifiable."

// codexTruncationLimit 是目录里的 tokens 截断阈值。
//
// 取一个保守值：目录里的 truncation_policy 是 Codex 自己的截断依据，AMKR 不知道上游
// 真实上下文窗口，保守值只会让 Codex 更早压缩历史，不会溢出。
const codexTruncationLimit = 10000

// validateTOML 用 pelletier/go-toml/v2 的 unstable parser 校验文档。
//
// 只做遍历与取错误，不消费 AST：保真回写靠迷你 tomlkit，而 go-toml/v2 的
// Unmarshal→Marshal 往返会丢掉全部注释（见 doc.go 的实测数据），不能用于写入。
func validateTOML(text string) error {
	parser := &unstable.Parser{}
	parser.Reset([]byte(text))
	for parser.NextExpression() {
		// 遍历即校验：任何语法错误都会让 NextExpression 返回 false 并使
		// parser.Error() 非空。
	}
	return parser.Error()
}

// codexTable 是配置里的一张 TOML 表（顶层或嵌套）。
type codexTable struct {
	container *tkContainer
}

// setScalar 设置一个字符串 / 布尔字面量；raw 已经是渲染好的 TOML 值文本。
func (t *codexTable) setScalar(name, raw string) {
	t.container.setScalar(name, raw)
}

// remove 删除一个键（整行消失，含行尾注释），对应 tomlkit 的 dict.pop。
func (t *codexTable) remove(name string) {
	t.container.remove(name)
}

// scalarString 读取某个键当前的字符串值（去掉引号）。
//
// 键不存在、不是键值对、或值不是标量时 ok 为 false——这三种情况都当作「AMKR 需要
// 接管这个键」。行内表 / 数组一律不解析：那是 doc.go D6 里显式报错的形状。
func (t *codexTable) scalarString(name string) (string, bool) {
	entry := t.container.findEntry(name)
	if entry == nil || entry.kind != tkEntryKeyValue || entry.value == nil || entry.value.class != tkScalar {
		return "", false
	}
	raw := strings.TrimSpace(entry.value.raw)
	if len(raw) >= 2 && (raw[0] == '"' || raw[0] == '\'') && raw[len(raw)-1] == raw[0] {
		return raw[1 : len(raw)-1], true
	}
	return raw, true
}

// subTable 取出或创建一张子表，对应参照实现的
// `document.get(key)` 与 `providers[CODEX_PROVIDER_ID] = tomlkit.table()`。
// label 是报错时用的完整点号路径（参照实现的错误文本就是这个形状）。
func (t *codexTable) subTable(name, label string) (*codexTable, error) {
	entry := t.container.findEntry(name)
	if entry == nil {
		// 新建表的 key.original 用裸键名；表头的 sep 是空串（不渲染 sep）。
		table := newTable(&tkKey{original: name, name: name, sep: ""})
		appendTable(t.container, table.key, table)
		return &codexTable{container: table.body}, nil
	}
	switch entry.kind {
	case tkEntryTable:
		if entry.table.aot {
			return nil, errf("Codex 配置中的 %s 是数组表（[[...]]），本实现不支持", label)
		}
		return &codexTable{container: entry.table.body}, nil
	case tkEntryKeyValue:
		switch entry.value.class {
		case tkScalar:
			// 与参照实现逐字一致（agent_config.py:339 / :346）。
			return nil, errf("Codex 配置中的 %s 必须是 TOML 表", label)
		default:
			// 数组 / 行内表在 Python 侧会通过 isinstance(MutableMapping) 检查，
			// 然后在写键时抛 TypeError 或产出「部分行内、部分多行」的古怪结果。
			// Go 侧选择显式报错而不是默默改写（见 doc.go 的差异清单）。
			return nil, errf("Codex 配置中的 %s 是数组或行内表，本实现不支持", label)
		}
	}
	return nil, errf("Codex 配置中的 %s 必须是 TOML 表", label)
}
