package api

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cpaQuotaWindow 是拍平后的一个额度窗口，界面按它画进度条。
//
// 词汇向 CPA 的归一化额度（pluginapi.QuotaBucket）对齐：那边叫 remainingFraction 的，
// 这里就叫 remaining——「被动信号算出来的条」与「额度插件回出来的条」在界面上因此是
// 同一种东西，不需要两套渲染。
//
// source 标出窗口的来路，看板上必须能分辨：
//   - passive —— CPA 从上游响应头上抄下来的（不额外发请求，但可能已经是几小时前的）；
//   - quota   —— 现场问来的（新，但要为每个账号多发一次请求）。
//
// 除了画进度条要的 label/remaining，窗口还带上它的**来路**：window 是上游的原始窗口名
// （`5h`/`weekly`/`primary`），group 是 CPA 归一化额度里的模型组（`Gemini Models`、
// `Claude and GPT models`），description 是上游附的说明文本。看板因此能按组分区显示，
// 也能把「还有多少」与「为什么是这么多」分开呈现——Antigravity 的双组双窗口正是靠
// group 才说得清：Gemini 与 Claude/GPT 各有一套 5 小时 + 周期额度。
type cpaQuotaWindow struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Window      string  `json:"window,omitempty"`
	Group       string  `json:"group,omitempty"`
	Description string  `json:"description,omitempty"`
	Remaining   float64 `json:"remaining"`
	ResetAt     string  `json:"reset_at,omitempty"`
	Status      string  `json:"status,omitempty"`
	Source      string  `json:"source"`
}

// quotaWindowLabel 把上游的窗口名归一成界面上那套话术。
//
// 被动窗口（claude/codex 的响应头）早就在说「5 小时」「7 天」，而现场额度（quota/fetch）
// 回的是 `5h`/`weekly` 这种机器名。同一页上两种写法会让人以为是两回事，所以这里统一一次。
//
// 认不出来的原样返回，**不做猜测**：把 `weekly` 猜成「7 天」是对的，但把月窗、季度窗也
// 套上同一个词就是编数据了；宁可显示上游原文，让看板保留「这是我没见过的东西」这个信息。
func quotaWindowLabel(window string) string {
	trimmed := strings.TrimSpace(window)
	switch strings.ToLower(trimmed) {
	case "5h", "5hr", "5hrs", "5hour", "5hours", "five_hour", "fivehour":
		return "5 小时"
	case "weekly", "week", "7d", "7day", "7days":
		return "7 天"
	case "monthly", "month", "30d", "30day", "30days":
		return "30 天"
	default:
		return trimmed
	}
}

// claudeWindowSpecs 是 Anthropic 统一额度头的三个窗口。
//
// 头名与语义取自 CLIProxyAPI 自己的解析器（internal/runtime/executor/helps/
// claude_ratelimit.go:24-190）：`-Utilization` 是 0..1 的**已用**比例，`-Reset` 是
// Unix 秒，`-Status` 取 allowed / allowed_warning / rejected。7d_oi 是 Anthropic 把
// 超额（overage）并进来的那个 7 天窗口，与 7d 并列而不是它的别名，因此单独一档。
var claudeWindowSpecs = []struct{ key, prefix, label string }{
	{key: "5h", prefix: "Anthropic-Ratelimit-Unified-5h-", label: "5 小时"},
	{key: "7d", prefix: "Anthropic-Ratelimit-Unified-7d-", label: "7 天"},
	{key: "7d_oi", prefix: "Anthropic-Ratelimit-Unified-7d_oi-", label: "7 天（含超额）"},
}

// passiveQuotaWindows 把 CPA 采到的被动信号翻译成额度窗口。
//
// 只覆盖 claude 与 codex：CPA 的 ProviderSupportsQuotaObservation 也只认这两个
// （外加 devin，但 devin 一侧没有任何头会被白名单收下，采出来永远是空表）。其余
// provider 的上游额度必须走主动探测，不是这里能补的。
func passiveQuotaWindows(provider string, signals map[string]any) []cpaQuotaWindow {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return claudePassiveWindows(signals)
	case "codex":
		return codexPassiveWindows(signals)
	default:
		// 空数组而不是 nil：nil 会被 encoding/json 写成 null，于是同一份响应里会同时
		// 出现 "windows":[]（认识的 provider 但没有信号）与 "windows":null（不认识的
		// provider）两种形状，客户端少一次判空就会炸。
		return []cpaQuotaWindow{}
	}
}

// claudePassiveWindows 解析 anthropic-ratelimit-unified-* 那族头。
func claudePassiveWindows(signals map[string]any) []cpaQuotaWindow {
	windows := make([]cpaQuotaWindow, 0, len(claudeWindowSpecs))
	for _, spec := range claudeWindowSpecs {
		used, ok := parseUnitFraction(signalValue(signals, spec.prefix+"Utilization"))
		if !ok {
			continue
		}
		window := cpaQuotaWindow{
			Key:       "claude/" + spec.key,
			Label:     spec.label,
			Remaining: clampFraction(1 - used),
			Status:    strings.ToLower(signalValue(signals, spec.prefix+"Status")),
			Source:    "passive",
		}
		window.ResetAt = quotaTimeText(signalValue(signals, spec.prefix+"Reset"))
		windows = append(windows, window)
	}
	return windows
}

// codexPassiveWindows 解析 x-codex-* 那族头。
//
// 不能写死枚举窗口名：Codex 把每个额外限额命名成一个 limit id，HTTP 路径上是
// `x-codex-bengalfox-primary-used-percent`，websocket 上是
// `x-codex-additional-<limit>-primary-used-percent`。因此这里反过来从**头名**剥出
// limit id 与窗口名——只有以 `-used-percent` 结尾的头才算窗口，`-over-secondary-
// limit-percent` 这类同族但含义不同的头自然被排除在外。
func codexPassiveWindows(signals map[string]any) []cpaQuotaWindow {
	names := make([]string, 0, len(signals))
	for name := range signals {
		names = append(names, name)
	}
	// 信号是 map，遍历顺序随机；排序后再产出，同一份快照每次给出的窗口顺序都一样。
	sort.Strings(names)

	windows := make([]cpaQuotaWindow, 0, len(names))
	for _, name := range names {
		lower := strings.ToLower(strings.TrimSpace(name))
		if !strings.HasPrefix(lower, "x-codex-") || !strings.HasSuffix(lower, "-used-percent") {
			continue
		}
		scope := strings.TrimPrefix(lower[len("x-codex-"):len(lower)-len("-used-percent")], "additional-")
		limitID, windowName := splitCodexScope(scope)
		if windowName == "" {
			continue
		}
		used, ok := parsePercentUsed(signalValue(signals, name))
		if !ok {
			continue
		}
		prefix := "X-Codex-" + scope + "-"
		window := cpaQuotaWindow{
			Key:       "codex/" + scope,
			Label:     codexWindowLabel(limitID, windowName, signalValue(signals, prefix+"Window-Minutes")),
			Remaining: clampFraction(1 - used),
			Source:    "passive",
		}
		window.ResetAt = quotaTimeText(signalValue(signals, prefix+"Reset-At"))
		if truthySignal(signalValue(signals, prefix+"Limit-Reached")) {
			window.Status = "rejected"
		}
		windows = append(windows, window)
	}
	return windows
}

// splitCodexScope 把 `-used-percent` 之前那段拆成 limit id 与窗口名。
//
// 无 limit id 时是 `primary` / `secondary`；有则是 `<limit>-primary`。
func splitCodexScope(scope string) (limitID, window string) {
	for _, name := range []string{"primary", "secondary"} {
		if scope == name {
			return "", name
		}
		if strings.HasSuffix(scope, "-"+name) {
			return strings.TrimSuffix(scope, "-"+name), name
		}
	}
	return "", ""
}

// codexWindowLabel 给窗口起个人能看懂的名字：优先用上游给的窗口长度，
// 没有就退回 primary/secondary 的位置说法。
func codexWindowLabel(limitID, window, minutesRaw string) string {
	base := map[string]string{"primary": "主要窗口", "secondary": "次要窗口"}[window]
	if minutes, err := strconv.Atoi(strings.TrimSpace(minutesRaw)); err == nil && minutes > 0 {
		base = humanMinutes(minutes)
	}
	// limit id 保留原文而不翻译：它是用户在上游那边认得出的名字（bengalfox 之类），
	// 翻成中文就没人知道对应哪个限额了。
	if limitID != "" {
		return limitID + " · " + base
	}
	return base
}

// humanMinutes 把分钟数写成人话。
func humanMinutes(minutes int) string {
	switch {
	case minutes%1440 == 0:
		return strconv.Itoa(minutes/1440) + " 天"
	case minutes%60 == 0:
		return strconv.Itoa(minutes/60) + " 小时"
	default:
		return strconv.Itoa(minutes) + " 分钟"
	}
}

// signalValue 按头名取被动信号，大小写不敏感。
//
// 大小写不敏感是必要的而不是保险：信号表的键由 CPA 用 http.CanonicalHeaderKey 归一
// （见 sdk/cliproxy/auth/quota_signals.go:99），写死一种大小写就会有一半取不到。
func signalValue(signals map[string]any, name string) string {
	if signals == nil {
		return ""
	}
	if raw, ok := signals[name]; ok {
		return signalText(raw)
	}
	for key, raw := range signals {
		if strings.EqualFold(key, name) {
			return signalText(raw)
		}
	}
	return ""
}

// signalText 把信号值渲染成文本。
//
// 表在 CPA 那边是 map[string]string，但这里不假设类型：一个数字类型的值不值得让
// 整份账号清单解析失败（JSON 解码会在类型不符时直接报错，那会连累同一实例下所有
// 正常的账号）。认不出的类型当成没有。
func signalText(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

// signalTexts 把信号表渲染成字符串表，供界面按原样列出。
//
// 保留原始头名与原始值、不做翻译：这一块是兜底——任何 AMKR 还不认识的额度头都会原封
// 不动地出现在这里，不必等我们适配就能看见上游到底发了什么。
func signalTexts(signals map[string]any) map[string]string {
	if len(signals) == 0 {
		return nil
	}
	out := make(map[string]string, len(signals))
	for name, raw := range signals {
		if text := signalText(raw); text != "" {
			out[name] = text
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseUnitFraction 解析 0..1 的比例（Anthropic 的 utilization）。
func parseUnitFraction(raw string) (float64, bool) {
	value, ok := finiteFloat(raw)
	if !ok || value < 0 || value > 1 {
		return 0, false
	}
	return value, true
}

// parsePercentUsed 解析 0..100 的百分数（Codex 的 used-percent），返回 0..1 的已用比例。
//
// 超过 100 的值夹到 100 而不是丢弃：上游给 101 说明「已超」，丢窗口会让看板以为这个
// 额度不存在，而「超了」恰恰是最该显示的一档。
func parsePercentUsed(raw string) (float64, bool) {
	value, ok := finiteFloat(raw)
	if !ok || value < 0 {
		return 0, false
	}
	return math.Min(value, 100) / 100, true
}

// finiteFloat 解析有限浮点数；NaN / Inf 视为无效（它们会让后续的百分比计算与
// JSON 序列化都出问题）。
func finiteFloat(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// clampFraction 把比例夹进 0..1：进度条宽度、百分比文本都按这个前提算。
func clampFraction(value float64) float64 {
	return math.Min(1, math.Max(0, value))
}

// quotaTimeText 解析额度重置时间并归一成 RFC3339。
//
// 三种写法都要认：Unix 秒（Anthropic 的 `-Reset` 与 Codex 的 `-Reset-At` 都是它）、
// RFC3339 时间戳（部分插件实现这么写），以及带小数秒的变体。只认一种就会静默丢掉
// 重置时间——那是看板上最该看见的一列。
//
// 解析不出来时返回空串而不是零值时间：界面上「没有重置时间」与「1970 年重置」
// 必须能分开。
func quotaTimeText(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if seconds, err := strconv.ParseInt(text, 10, 64); err == nil {
		if seconds <= 0 {
			return ""
		}
		return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// truthySignal 判断一个信号值是否表示「已触发」。
func truthySignal(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
