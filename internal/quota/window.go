package quota

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Window 是一个额度窗口的归一读法。
//
// 字段口径与账号资源页的 cpaQuotaWindow 一致（那边是「展示层」的窗口，多了分组、
// 说明、来源）：同一份上游信号在两处必须给出同一个 remaining / reset_at，因此解析
// 只有这一份实现。
type Window struct {
	// Key 是窗口的稳定标识，如 claude/5h、codex/primary、codex/bengalfox-primary。
	Key string
	// Raw 是上游原始窗口名（如 5h / weekly / primary），供底限判定按窗口语义分档。
	Raw string
	// Label 是给人看的窗口名，如「5 小时」「7 天」；认不出的窗口保留上游原文。
	Label string
	// Remaining 是剩余比例，已夹进 0..1。
	Remaining float64
	// ResetAt 是窗口重置时间；零值表示上游没给（与「1970 年重置」必须分开）。
	ResetAt time.Time
	// Status 是上游原话（小写），如 rejected / allowed_warning。
	Status string
}

// HardStatuses 是表示「上游已拒绝」的状态词（小写）。
//
// 只收明确的拒绝档：allowed_warning 只是预警，不参与冻结。
var HardStatuses = map[string]bool{
	"rejected":      true,
	"exceeded":      true,
	"rate_limited":  true,
	"rate-limited":  true,
	"limit_reached": true,
	"limit-reached": true,
}

// HardExhausted 报告窗口是否已硬性耗尽：上游明确拒绝，或剩余比例已归零。
//
// 「接近用完」（0.01）不算：冻结是硬约束，宁可晚一次请求也不能误伤还有额度的 Key。
func HardExhausted(window Window) bool {
	if HardStatuses[strings.ToLower(strings.TrimSpace(window.Status))] {
		return true
	}
	return window.Remaining <= 0
}

// Thresholds 是 5h / 7d 窗口的剩余比例底限；0 表示关闭该档的提前冻结。
type Thresholds struct {
	FiveHour float64
	SevenDay float64
}

// Normalize 把底限夹进 0..1：配置写错（负数 / 超过 1 / NaN）时收敛到确定口径，
// +Inf / -Inf 分别落到 1 / 0。
func (t Thresholds) Normalize() Thresholds {
	return Thresholds{FiveHour: clampThreshold(t.FiveHour), SevenDay: clampThreshold(t.SevenDay)}
}

// clampThreshold 把单个底限夹进 0..1；NaN 视为未配置。
func clampThreshold(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	return math.Min(1, math.Max(0, value))
}

// WindowKind 把窗口归到 5h / 7d 两档；认不出返回空串。
//
// 依次看 Raw（上游原话）、Key（稳定标识）、Label（人话标签）。codex 的 Key 形如
// codex/<limit>-primary，其中的 limit id 可能是任意词（甚至叫 weekly），不能当窗口
// 长度，因此 codex 的 Key 不参与识别，只看它的 Raw / Label。
func WindowKind(window Window) string {
	raw := strings.TrimSpace(window.Raw)
	if !isCodexWindowKey(raw) {
		if kind := matchWindowKind(raw, true); kind != "" {
			return kind
		}
	}
	key := strings.TrimSpace(window.Key)
	if !isCodexWindowKey(key) {
		if kind := matchWindowKind(key, true); kind != "" {
			return kind
		}
	}
	return matchWindowKind(window.Label, false)
}

// isCodexWindowKey 判断文本是不是 codex 的窗口 Key；调用方可能把 Key 兜底填进 Raw，
// 这种带 limit id 的文本同样不能按词面猜窗口长度。
func isCodexWindowKey(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "codex/")
}

// matchWindowKind 从窗口名里识别档位；aliases 控制是否把 weekly/week 也算作 7d。
//
// 标签里只认显式时长（「5 小时」「7 天」），因为 codex 的标签可能是
// 「weekly · 2 小时」——那个 weekly 是 limit id，不是窗口长度。
func matchWindowKind(raw string, aliases bool) string {
	text := compactWindowText(raw)
	if text == "" {
		return ""
	}
	if hasWindowToken(text, "5h") || hasWindowToken(text, "5hour") ||
		hasWindowToken(text, "fivehour") || hasWindowToken(text, "5小时") {
		return "5h"
	}
	if hasWindowToken(text, "7d") || hasWindowToken(text, "7day") ||
		hasWindowToken(text, "sevenday") || hasWindowToken(text, "7天") {
		return "7d"
	}
	if aliases && (hasWindowToken(text, "weekly") || hasWindowToken(text, "week")) {
		return "7d"
	}
	return ""
}

// compactWindowText 归一大小写与常见分隔符：5 小时 / five_hour / 7-day 落到同一种写法。
func compactWindowText(raw string) string {
	text := strings.ToLower(strings.TrimSpace(raw))
	if text == "" {
		return ""
	}
	return strings.NewReplacer(" ", "", "\t", "", "\n", "", "_", "", "-", "", "·", "").Replace(text)
}

// hasWindowToken 判断归一文本里是否出现 token，且 token 前不是数字。
//
// 这条边界是为了不把「15 小时」认成「5 小时」：多认一档会提前冻结还有额度的 Key。
func hasWindowToken(text, token string) bool {
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], token)
		if index < 0 {
			return false
		}
		start := offset + index
		if start == 0 || text[start-1] < '0' || text[start-1] > '9' {
			return true
		}
		offset = start + len(token)
	}
	return false
}

// Floor 返回窗口对应的底限；不是 5h / 7d 窗口或底限为 0 时返回 0。
func (t Thresholds) Floor(window Window) float64 {
	normalized := t.Normalize()
	switch WindowKind(window) {
	case "5h":
		return normalized.FiveHour
	case "7d":
		return normalized.SevenDay
	default:
		return 0
	}
}

// BelowFloor 报告窗口是否已低于底限。
//
// 剩余归零也返回 true（此时 TriggerKind 会先判为 hard）；底限为 0 时该档关闭。
func BelowFloor(window Window, thresholds Thresholds) bool {
	if window.Remaining <= 0 {
		return true
	}
	floor := thresholds.Floor(window)
	return floor > 0 && window.Remaining <= floor
}

// TriggerKind 返回窗口应触发的冻结类型：hard（硬性耗尽）/ threshold（低于底限）/ 空。
func TriggerKind(window Window, thresholds Thresholds) string {
	if HardExhausted(window) {
		return "hard"
	}
	if BelowFloor(window, thresholds) {
		return "threshold"
	}
	return ""
}

// TriggerWindows 按原顺序筛出会触发冻结的窗口。
func TriggerWindows(windows []Window, thresholds Thresholds) []Window {
	result := make([]Window, 0, len(windows))
	for _, window := range windows {
		if TriggerKind(window, thresholds) != "" {
			result = append(result, window)
		}
	}
	return result
}

// Label 把上游窗口名归一成人话（5 小时 / 7 天 / 30 天），认不出的原样返回。
//
// 与 api 侧原来的 quotaWindowLabel 口径逐字一致（迁移过来后 api 改为调用这里）：
// 不做猜测，月窗/季度窗保留上游原文，免得「编数据」。
func Label(window string) string {
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

// anthropicQuotaPrefix / codexQuotaPrefix 是两族额度响应头的前缀（小写）。
//
// 代理请求路径每个上游响应都会过一次 HasQuotaHeaders，所以这里只做前缀判断，
// 命中之后才建信号表。
const (
	anthropicQuotaPrefix = "anthropic-ratelimit-unified-"
	codexQuotaPrefix     = "x-codex-"
)

// PassiveWindows 把 CPA 风格的被动信号表（头名 → 值，头名大小写不敏感）解析成窗口。
//
// provider 取 claude / codex；其余返回空（CPA 的被动采集也只覆盖这两个）。
func PassiveWindows(provider string, signals map[string]string) []Window {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return claudePassiveWindows(signals)
	case "codex":
		return codexPassiveWindows(signals)
	default:
		// 空数组而不是 nil：调用方可能把结果直接当列表序列化/渲染，nil 在 JSON 里是
		// 另一种形状（与 api 侧原有口径一致）。
		return []Window{}
	}
}

// claudePassiveWindows 解析 anthropic-ratelimit-unified-* 那族头。
func claudePassiveWindows(signals map[string]string) []Window {
	windows := make([]Window, 0, len(claudeWindowSpecs))
	for _, spec := range claudeWindowSpecs {
		used, ok := parseUnitFraction(signalValue(signals, spec.prefix+"Utilization"))
		if !ok {
			continue
		}
		windows = append(windows, Window{
			Key:       "claude/" + spec.key,
			Raw:       spec.key,
			Label:     spec.label,
			Remaining: clampFraction(1 - used),
			ResetAt:   ParseResetText(signalValue(signals, spec.prefix+"Reset")),
			Status:    strings.ToLower(signalValue(signals, spec.prefix+"Status")),
		})
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
func codexPassiveWindows(signals map[string]string) []Window {
	names := make([]string, 0, len(signals))
	for name := range signals {
		names = append(names, name)
	}
	// 信号是 map，遍历顺序随机；排序后再产出，同一份快照每次给出的窗口顺序都一样。
	sort.Strings(names)

	windows := make([]Window, 0, len(names))
	for _, name := range names {
		lower := strings.ToLower(strings.TrimSpace(name))
		if !strings.HasPrefix(lower, codexQuotaPrefix) || !strings.HasSuffix(lower, "-used-percent") {
			continue
		}
		rawScope := lower[len(codexQuotaPrefix) : len(lower)-len("-used-percent")]
		// websocket 形态在头名里多一层 additional-：它只是命名空间，窗口标识要剥掉，
		// 但伴随字段（-Window-Minutes / -Reset-At / -Limit-Reached）也可能带这层前缀，
		// 因此记下「有没有剥」交给 codexCompanion 按原方言优先查找。
		additional := strings.HasPrefix(rawScope, "additional-")
		scope := strings.TrimPrefix(rawScope, "additional-")
		limitID, windowName := splitCodexScope(scope)
		if windowName == "" {
			continue
		}
		used, ok := parsePercentUsed(signalValue(signals, name))
		if !ok {
			continue
		}
		window := Window{
			Key:       "codex/" + scope,
			Raw:       windowName,
			Label:     codexWindowLabel(limitID, windowName, codexCompanion(signals, additional, scope, "Window-Minutes")),
			Remaining: clampFraction(1 - used),
			ResetAt:   ParseResetText(codexCompanion(signals, additional, scope, "Reset-At")),
		}
		if truthySignal(codexCompanion(signals, additional, scope, "Limit-Reached")) {
			window.Status = "rejected"
		}
		windows = append(windows, window)
	}
	return windows
}

// codexCompanion 取窗口的伴随字段（窗口长度 / 重置时间 / 是否触顶）。
//
// 头名有两种方言：HTTP 路径是 `x-codex-<limit>-primary-window-minutes`，websocket
// 路径是 `x-codex-additional-<limit>-primary-window-minutes`。窗口标识统一剥掉
// `additional-`，但伴随字段要按**原方言**优先找：从 additional- 形态解析出来的窗口
// 先查带前缀的头，取不到再退回无前缀（同一份信号表里两种方言混着也能读到）。
// 反过来，普通窗口只查无前缀的头，不会被 additional- 前缀的伴随字段串味。
func codexCompanion(signals map[string]string, additional bool, scope, suffix string) string {
	if additional {
		if value := signalValue(signals, "X-Codex-additional-"+scope+"-"+suffix); value != "" {
			return value
		}
	}
	return signalValue(signals, "X-Codex-"+scope+"-"+suffix)
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
func signalValue(signals map[string]string, name string) string {
	if signals == nil {
		return ""
	}
	if raw, ok := signals[name]; ok {
		return strings.TrimSpace(raw)
	}
	for key, raw := range signals {
		if strings.EqualFold(key, name) {
			return strings.TrimSpace(raw)
		}
	}
	return ""
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
// 序列化都出问题）。
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

// truthySignal 判断一个信号值是否表示「已触发」。
func truthySignal(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// HasQuotaHeaders 快速判断这组响应头里有没有额度信号。
//
// 代理请求路径每次上游响应都会调一次，必须先做前缀判断再解析；返回 false 时调用方
// 直接跳过，不能为此付出解析代价。
func HasQuotaHeaders(headers http.Header) bool {
	for name := range headers {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, anthropicQuotaPrefix) || strings.HasPrefix(lower, codexQuotaPrefix) {
			return true
		}
	}
	return false
}

// HeaderWindows 从上游响应头解析额度窗口（自动识别 anthropic 与 codex 两族）。
//
// 只解析，不判定：是否硬性耗尽是 HardExhausted 的事。两族头同时出现时按
// claude 在前、codex 在后的顺序拼接（正常上游不会混发，混发时也不丢任何一族）。
func HeaderWindows(headers http.Header) []Window {
	anthropic, codex := false, false
	for name := range headers {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, anthropicQuotaPrefix) {
			anthropic = true
			continue
		}
		if strings.HasPrefix(lower, codexQuotaPrefix) {
			codex = true
		}
	}
	if !anthropic && !codex {
		return nil
	}
	signals := headerSignals(headers)
	var windows []Window
	if anthropic {
		windows = append(windows, claudePassiveWindows(signals)...)
	}
	if codex {
		windows = append(windows, codexPassiveWindows(signals)...)
	}
	return windows
}

// headerSignals 把响应头渲染成大小写不敏感的信号表。
//
// 只取每项的第一个值：额度头都是单值头，重复出现时第一份才是上游真正写下的那份。
func headerSignals(headers http.Header) map[string]string {
	signals := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		signals[name] = values[0]
	}
	return signals
}

// ResetText 把重置时间写成 RFC3339（零值返回空串）。
func ResetText(resetAt time.Time) string {
	if resetAt.IsZero() {
		return ""
	}
	return resetAt.UTC().Format(time.RFC3339)
}

// ParseResetText 解析上游的重置时间文本：Unix 秒、毫秒时间戳、RFC3339（可带小数秒）。
//
// 解析不出来返回零值而不是 Unix 零时刻；三种写法都要认（Anthropic 的 -Reset 与
// Codex 的 -Reset-At 是 Unix 秒，部分插件实现写 RFC3339 或毫秒）。
func ParseResetText(raw string) time.Time {
	text := strings.TrimSpace(raw)
	if text == "" {
		return time.Time{}
	}
	if stamp, err := strconv.ParseInt(text, 10, 64); err == nil {
		if stamp <= 0 {
			return time.Time{}
		}
		// 1e12 是秒/毫秒的分界线：秒级时间戳要到公元 33658 年才会超过它
		// （与 api 侧 epochText 同一口径）。毫秒被当成秒会得到公元五万年，
		// 界面上就成了一条永远不重置的额度。
		if stamp >= 1e12 {
			return time.UnixMilli(stamp).UTC()
		}
		return time.Unix(stamp, 0).UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
