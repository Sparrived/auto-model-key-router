package agentconfig

import (
	"bytes"
	"slices"
	"unicode/utf8"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// configureClaudeCode 改写 ~/.claude/settings.json（agent_config.py:260）。
//
// 与 Codex / Pi 不同，这里**可以**用整份 JSON 重写：JSON 不允许注释，参照实现
// 本身就是 `json.loads` → 改 dict → `json.dumps(indent=2, ensure_ascii=False)`，
// 因此不存在需要保真的格式。canonical 的 DumpsIndent 正是这个形式
// （保留键插入顺序、不排序、ensure_ascii=False），所以直接复用。
//
// 取消路由模式后，Claude Code 侧不再有「统一模型 / 原生模型」之分：env 里只写
// 连接信息，模型清单交给 settings.json 的 modelPicker（v2.1.242+）。
//
//   - 旧实现写入的 ANTHROPIC_MODEL 与三个 ANTHROPIC_DEFAULT_*_MODEL 按 legacy 计划
//     处置：**集成前就有**的键恢复用户原值，**集成前没有**的键（即 AMKR 自己塞进去
//     的）删除。留着这些键会把每个会话（含 haiku 后台任务）压到一个模型上，让选择器
//     形同虚设；但无条件删除又会伤到用户自己设的值，所以判据只能来自原始快照。
//   - 不能用 gateway model discovery（CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY）
//     代替静态清单：官方会按 id 过滤，只接受含 claude / anthropic 的模型名，
//     AMKR 的 gpt-*/unified-model 之类进不了清单。
//   - replaceBuiltInOptions=true 让 picker 只列 AMKR 的模型：base_url 已经指向
//     AMKR，内置的 Anthropic 行在这里必然调不通，列出来只会误导。
func configureClaudeCode(current []byte, cfg *config.RouterConfig, legacy AgentLegacyPlan) ([]byte, error) {
	var data *canonical.Value
	if len(current) > 0 {
		text, err := decodeUTF8Sig("Claude Code", current)
		if err != nil {
			return nil, err
		}
		parsed, parseErr := canonical.ParseString(text)
		if parseErr != nil {
			return nil, errf("Claude Code 配置不是有效的 UTF-8 JSON: %v", parseErr)
		}
		if !parsed.IsObject() {
			return nil, errf("Claude Code 配置根节点必须是 JSON 对象")
		}
		data = parsed
	} else {
		data = canonical.NewObject()
	}

	env := data.Lookup("env")
	if env == nil || env.IsNull() {
		env = canonical.NewObject()
		data.SetKey("env", env)
	}
	if !env.IsObject() {
		return nil, errf("Claude Code 配置中的 env 必须是 JSON 对象")
	}

	// env.update({...})：已有键保持位置只换值，新键追加到末尾。
	env.SetKey("ANTHROPIC_BASE_URL", canonical.NewString(RouterOrigin(cfg)))
	env.SetKey("ANTHROPIC_AUTH_TOKEN", canonical.NewString(cfg.LocalAPIKey))
	env.SetKey("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", canonical.NewString("1"))
	env.SetKey("CLAUDE_CODE_ATTRIBUTION_HEADER", canonical.NewString("false"))
	for _, name := range legacy.Remove {
		env.DeleteKey(name)
	}
	for _, item := range legacy.Restore {
		env.SetKey(item.Name, canonical.NewString(item.Value))
	}

	// 模型清单：AMKR 当前可用的全部对外名（ID + 别名），外加配置了统一模型时的
	// unified-model。用户也可以直接用 `/model <名字>` 敲一个不在清单里的 AMKR 模型。
	names := availableModelNames(cfg)
	options := make([]*canonical.Value, 0, len(names))
	for _, name := range names {
		pairs := []canonical.ObjectPair{{Key: "model", Value: canonical.NewString(name)}}
		if name == config.UNIFIED_MODEL_ID {
			pairs = append(pairs, canonical.ObjectPair{
				Key:   "description",
				Value: canonical.NewString("AMKR 统一模型：按路径与任务自动路由"),
			})
		}
		options = append(options, canonical.NewObjectOf(pairs...))
	}
	data.SetKey("modelPicker", canonical.NewObjectOf(
		canonical.ObjectPair{Key: "options", Value: canonical.NewArray(options...)},
		canonical.ObjectPair{Key: "replaceBuiltInOptions", Value: canonical.NewBool(true)},
	))
	// `model` 是**新会话的默认模型**（/model 的选择优先于它，见官方 model-config
	// 的优先级说明）。缺失或指向 AMKR 不认识的名字时改成首选模型：picker 只影响
	// 用户主动挑选，不设一个可用默认值的话，新会话会直接打到一个 AMKR 不提供的
	// 模型上。用户自己选的、AMKR 也能提供的模型原样保留。
	existing := ""
	if value := data.Lookup("model"); value != nil && !value.IsNull() {
		existing = value.StringValue()
	}
	if !containsName(names, existing) {
		data.SetKey("model", canonical.NewString(preferredModelName(names)))
	}
	// 关闭 Claude Code 的 Git 归属署名。整体替换（而不是 update），因此用户
	// 写在 attribution 里的其它字段会被丢弃——这是参照实现的既有行为。
	data.SetKey("attribution", canonical.NewObjectOf(
		canonical.ObjectPair{Key: "commit", Value: canonical.NewString("")},
		canonical.ObjectPair{Key: "pr", Value: canonical.NewString("")},
	))
	return []byte(canonical.DumpsIndent(data, 2) + "\n"), nil
}

// configurePiAgent 改写 ~/.pi/agent/models.json（agent_config.py:355）。
//
// 与 Claude Code 同理，整份 JSON 重写。providers.amkr 是**整体覆盖**：用户写在
// 该 provider 下的自定义字段会丢，这是参照实现的既有行为（Pi agent 的 provider
// 由 AMKR 独占）。
func configurePiAgent(current []byte, cfg *config.RouterConfig) ([]byte, error) {
	var data *canonical.Value
	if len(current) > 0 {
		text, err := decodeUTF8Sig("Pi Agent", current)
		if err != nil {
			return nil, err
		}
		parsed, parseErr := canonical.ParseString(text)
		if parseErr != nil {
			return nil, errf("Pi Agent 配置不是有效的 UTF-8 JSON: %v", parseErr)
		}
		if !parsed.IsObject() {
			return nil, errf("Pi Agent 配置根节点必须是 JSON 对象")
		}
		data = parsed
	} else {
		data = canonical.NewObject()
	}

	providers := data.Lookup("providers")
	if providers == nil || providers.IsNull() {
		providers = canonical.NewObject()
		data.SetKey("providers", providers)
	}
	if !providers.IsObject() {
		return nil, errf("Pi Agent 配置中的 providers 必须是 JSON 对象")
	}

	// 模型清单 = 所有「至少有一个启用 key」的模型 id 与别名（+ 配置了统一模型时的
	// unified-model）。禁用全部 key 的模型不进清单：Pi agent 侧看不到它，用户也就
	// 不会选中一个必然失败的模型（agent_config.py:372-378）。
	modelNames := availableModelNames(cfg)
	models := make([]*canonical.Value, 0, len(modelNames))
	for _, name := range modelNames {
		models = append(models, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "id", Value: canonical.NewString(name)},
			canonical.ObjectPair{Key: "contextWindow", Value: canonical.NewIntValue(piContextWindow)},
		))
	}
	providers.SetKey(piProviderID, canonical.NewObjectOf(
		canonical.ObjectPair{Key: "baseUrl", Value: canonical.NewString(RouterOrigin(cfg) + "/v1")},
		canonical.ObjectPair{Key: "api", Value: canonical.NewString("openai-completions")},
		canonical.ObjectPair{Key: "apiKey", Value: canonical.NewString(cfg.LocalAPIKey)},
		canonical.ObjectPair{Key: "authHeader", Value: canonical.NewBool(true)},
		canonical.ObjectPair{Key: "models", Value: canonical.NewArray(models...)},
	))
	return []byte(canonical.DumpsIndent(data, 2) + "\n"), nil
}

// availableModelNames 返回 AMKR 当前可用的全部对外模型名（去重、排序）。
//
// 判定与 internal/keypool.AvailableModelIDs 保持一致：**至少有一个启用 Key** 的
// 模型才算可用——列进客户端却调不动是最坏的体验。模型 ID 与每个别名都是独立的
// 可调用名；配置了统一模型时把 unified-model 一并列上（未配置时它调不动，不列）。
//
// 排序也与 /v1/models 一致：名字升序，unified-model 追加在末尾（它不是一个真实
// 模型，排进去只会让「统一入口」淹没在模型堆里）。集成写出的清单因此与用户在
// /v1/models 里看到的是同一份东西、同一个顺序。
func availableModelNames(cfg *config.RouterConfig) []string {
	names := make([]string, 0, len(cfg.Models)*2+1)
	seen := map[string]bool{}
	for _, model := range cfg.Models {
		enabled := false
		for _, key := range model.Keys {
			if key.Enabled {
				enabled = true
				break
			}
		}
		if !enabled {
			continue
		}
		for _, name := range append([]string{model.ID}, model.Aliases...) {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	slices.Sort(names)
	// unified-model 追加在排序之后，居于末尾：keypool.AvailableModelIDs 也是
	// 「先排序、再 append unified-model」，两边的清单顺序因此逐项相同。
	if cfg.UnifiedModel != nil && !containsName(names, config.UNIFIED_MODEL_ID) {
		names = append(names, config.UNIFIED_MODEL_ID)
	}
	return names
}

// containsName 报告清单里是否含某个模型名。
func containsName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

// preferredModelName 选一个默认模型名：优先 unified-model（它按路径与任务自动路由，
// 是 AMKR 最稳的入口），否则用清单里的第一个（清单已排序，即字典序最小的可用名）。
// 空清单在 Configure 入口就被拦住了，这里返回 unified-model 只是让取值可预期。
func preferredModelName(names []string) string {
	for _, name := range names {
		if name == config.UNIFIED_MODEL_ID {
			return name
		}
	}
	if len(names) == 0 {
		return config.UNIFIED_MODEL_ID
	}
	return names[0]
}

// configureCodexAuth 改写 ~/.codex/auth.json（agent_config.py:398）。
//
// Codex 的 OpenAI provider 需要 requires_openai_auth=true，此时它从 auth.json
// 读 OPENAI_API_KEY；只改 config.toml 而不改这里，Codex 会拿旧 key 去打 AMKR
// 并被本地鉴权拒绝。
func configureCodexAuth(current []byte, cfg *config.RouterConfig) ([]byte, error) {
	var data *canonical.Value
	if len(current) > 0 {
		text, err := decodeUTF8Sig("Codex 鉴权", current)
		if err != nil {
			return nil, err
		}
		parsed, parseErr := canonical.ParseString(text)
		if parseErr != nil {
			return nil, errf("Codex 鉴权配置不是有效的 UTF-8 JSON: %v", parseErr)
		}
		data = parsed
	} else {
		data = canonical.NewObject()
	}
	// 空文件走 {} 分支（data 是新建对象，必定通过）；非空文件解析出的根节点
	// 必须是对象，否则对应参照实现的 "必须是 JSON 对象" 报错。
	if !data.IsObject() {
		return nil, errf("Codex 鉴权配置必须是 JSON 对象")
	}
	data.SetKey("OPENAI_API_KEY", canonical.NewString(cfg.LocalAPIKey))
	return []byte(canonical.DumpsIndent(data, 2) + "\n"), nil
}

// decodeUTF8Sig 复刻 Python 的 `content.decode("utf-8-sig")`。
//
// 两件事：剥掉开头的一个 UTF-8 BOM，以及拒绝非法 UTF-8（Python 抛
// UnicodeDecodeError，Go 的 string 可以承载非法字节，不校验就会把坏字节原样
// 写回去）。label 用于拼出与参照实现同形的错误前缀。
func decodeUTF8Sig(label string, content []byte) (string, error) {
	trimmed := bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(trimmed) {
		return "", errf("%s 配置不是有效的 UTF-8 JSON: 输入不是合法的 UTF-8", label)
	}
	return string(trimmed), nil
}
