package quota

import (
	"math"
	"net/http"
	"testing"
	"time"
)

// 本文件钉住额度窗口的解析口径：上游信号怎么变成 Window、什么样算硬性耗尽。
//
// 这些断言是「账号资源页 / 选路冻结 / 代理响应头」三处共用的同一份解析，口径改了会
// 同时影响看板与选路，所以每个分支都要有能真正回归的用例。

// closeTo 比较比例。
//
// 必须带容差：实现算的是 1-0.42，二进制浮点给出的是 0.5800000000000001，精确比较会把
// 正确的实现判成错的。
func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

// TestLabel 钉住窗口名的人话映射：认得的归一，认不得的原样保留。
func TestLabel(t *testing.T) {
	cases := map[string]string{
		"5h":            "5 小时",
		"5hours":        "5 小时",
		"five_hour":     "5 小时",
		"weekly":        "7 天",
		"7D":            "7 天",
		"monthly":       "30 天",
		"30days":        "30 天",
		"primary":       "primary",
		"":              "",
		"  weekly  ":    "7 天",
		"quarterly-90d": "quarterly-90d",
	}
	for input, want := range cases {
		if got := Label(input); got != want {
			t.Errorf("Label(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestParseResetText 钉住三种重置时间写法。
//
// 只认一种就会静默丢掉重置时间；而毫秒被当成秒会得到公元五万年——界面上就是一条
// 永远不重置的额度。空 / 非法 / <= 0 必须回零值，与「1970 年重置」分开。
func TestParseResetText(t *testing.T) {
	unixSeconds := time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		raw  string
		want time.Time
	}{
		{name: "Unix 秒", raw: "1800000000", want: unixSeconds},
		{name: "毫秒时间戳", raw: "1800000000000", want: unixSeconds},
		{name: "RFC3339", raw: "2027-01-15T08:00:00Z", want: unixSeconds},
		{name: "RFC3339 小数秒", raw: "2027-01-15T08:00:00.5Z", want: unixSeconds.Add(500 * time.Millisecond)},
		{name: "RFC3339 带偏移", raw: "2027-01-15T16:00:00+08:00", want: unixSeconds},
		{name: "前后空白", raw: " 1800000000 ", want: unixSeconds},
		{name: "空串", raw: "", want: time.Time{}},
		{name: "零", raw: "0", want: time.Time{}},
		{name: "负数", raw: "-1", want: time.Time{}},
		{name: "非法文本", raw: "not-a-time", want: time.Time{}},
		{name: "浮点文本不猜", raw: "1800000000.5", want: time.Time{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ParseResetText(testCase.raw)
			if !got.Equal(testCase.want) {
				t.Fatalf("ParseResetText(%q) = %v，期望 %v", testCase.raw, got, testCase.want)
			}
			if testCase.want.IsZero() && !got.IsZero() {
				t.Errorf("解析失败必须回零值，实际 %v", got)
			}
		})
	}

	if got := ResetText(time.Time{}); got != "" {
		t.Errorf("零值时间应写成空串，实际 %q", got)
	}
	if got := ResetText(unixSeconds); got != "2027-01-15T08:00:00Z" {
		t.Errorf("ResetText = %q，期望 RFC3339", got)
	}
	// 小数秒落成 RFC3339 时按秒截断（界面只显示到分钟，多出来的精度没有意义）。
	if got := ResetText(unixSeconds.Add(500 * time.Millisecond)); got != "2027-01-15T08:00:00Z" {
		t.Errorf("带小数秒的 ResetText = %q，期望按秒截断", got)
	}
}

// TestPassiveWindowsClaude 钉住 Anthropic 统一额度头的解析口径。
//
// 关键点：utilization 是 0..1 的**已用**比例（界面要剩余，得减）；`-Reset` 是
// Unix 秒；头名大小写不敏感；缺 utilization 的窗口丢掉；utilization 越界（>1 或
// 负数 / NaN / Inf）丢弃而不是当 0 或 100% 用。
func TestPassiveWindowsClaude(t *testing.T) {
	signals := map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "0.42",
		"anthropic-ratelimit-unified-5h-reset":       "1800000000",
		"anthropic-ratelimit-unified-5h-status":      "ALLOWED_WARNING",
		"Anthropic-Ratelimit-Unified-7d-Utilization": "1",
		"Anthropic-Ratelimit-Unified-7d-Reset":       "2027-01-15T08:00:00Z",
	}

	windows := PassiveWindows("Claude", signals)
	if len(windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %d（%+v）", len(windows), windows)
	}
	fiveHour := windows[0]
	if fiveHour.Key != "claude/5h" || fiveHour.Label != "5 小时" {
		t.Errorf("5 小时窗口标识不对: %+v", fiveHour)
	}
	if !closeTo(fiveHour.Remaining, 0.58) {
		t.Errorf("剩余比例应为 1-0.42=0.58，实际 %v", fiveHour.Remaining)
	}
	wantReset := time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)
	if !fiveHour.ResetAt.Equal(wantReset) {
		t.Errorf("Unix 秒应解析成 %v，实际 %v", wantReset, fiveHour.ResetAt)
	}
	if fiveHour.Status != "allowed_warning" {
		t.Errorf("状态应小写归一，实际 %q", fiveHour.Status)
	}
	// 7d 用满（utilization=1）时剩余是 0，而不是被当成「没有这个窗口」丢掉。
	sevenDay := windows[1]
	if sevenDay.Key != "claude/7d" || sevenDay.Remaining != 0 {
		t.Errorf("7 天窗口应是剩余 0: %+v", sevenDay)
	}
	if !sevenDay.ResetAt.Equal(wantReset) {
		t.Errorf("RFC3339 时间戳应原样归一，实际 %v", sevenDay.ResetAt)
	}

	// 三个窗口的固定顺序：5h、7d、7d_oi（与上游头族的定义顺序一致）。
	withOverage := map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization":    "0.5",
		"Anthropic-Ratelimit-Unified-7d-Utilization":    "0.6",
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization": "0.7",
	}
	if got := PassiveWindows("claude", withOverage); len(got) != 3 ||
		got[2].Key != "claude/7d_oi" || got[2].Label != "7 天（含超额）" {
		t.Errorf("7d_oi 应作为独立窗口排在最后: %+v", got)
	}
}

// TestPassiveWindowsClaudeDropsInvalid 钉住「异常值丢弃而不是硬凑」。
func TestPassiveWindowsClaudeDropsInvalid(t *testing.T) {
	invalid := []string{"1.5", "-0.1", "NaN", "Inf", "+Inf", "abc", ""}
	for _, value := range invalid {
		signals := map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": value,
			"Anthropic-Ratelimit-Unified-5h-Reset":       "1800000000",
		}
		if got := PassiveWindows("claude", signals); len(got) != 0 {
			t.Errorf("utilization=%q 应丢弃整个窗口，实际 %+v", value, got)
		}
	}

	// 缺 utilization 的窗口同样不产出。
	missing := map[string]string{"Anthropic-Ratelimit-Unified-5h-Reset": "1800000000"}
	if got := PassiveWindows("claude", missing); len(got) != 0 {
		t.Errorf("缺 utilization 不应产出窗口: %+v", got)
	}
}

// TestPassiveWindowsUnknownProvider 钉住「不认识就不解析」。
func TestPassiveWindowsUnknownProvider(t *testing.T) {
	signals := map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.42"}
	for _, provider := range []string{"gemini", "", "  devin  "} {
		windows := PassiveWindows(provider, signals)
		if len(windows) != 0 {
			t.Errorf("provider=%q 不应解析出窗口: %+v", provider, windows)
		}
		if windows == nil {
			t.Errorf("provider=%q 应返回空数组而不是 nil（JSON 形状不同）", provider)
		}
	}
	if got := PassiveWindows("claude", nil); len(got) != 0 {
		t.Errorf("nil 信号表不应产出窗口: %+v", got)
	}
}

// TestPassiveWindowsCodex 钉住 x-codex-* 的解析口径。
//
// 窗口名不能枚举：额外限额是 `x-codex-<limit>-primary-*`（websocket 上还多一层
// `additional-`）。这里同时钉住「同族但含义不同的头不能当窗口」「超过 100% 夹到
// 100 而不是丢窗口」「limit-reached 真值 → rejected」「window-minutes 出人话」。
func TestPassiveWindowsCodex(t *testing.T) {
	signals := map[string]string{
		"X-Codex-Primary-Used-Percent":                       "58",
		"X-Codex-Primary-Window-Minutes":                     "300",
		"X-Codex-Primary-Reset-At":                           "1800000000",
		"X-Codex-Secondary-Used-Percent":                     "101",
		"X-Codex-Bengalfox-Primary-Used-Percent":             "12",
		"X-Codex-Bengalfox-Primary-Window-Minutes":           "10080",
		"X-Codex-Bengalfox-Primary-Limit-Reached":            "TRUE",
		"X-Codex-Over-Secondary-Limit-Percent":               "30",
		"x-codex-additional-weekly-secondary-used-percent":   "0",
		"x-codex-additional-weekly-secondary-window-minutes": "120",
		"x-codex-additional-weekly-secondary-reset-at":       "1800000000",
		"x-codex-additional-weekly-secondary-limit-reached":  "yes",
	}

	windows := PassiveWindows("codex", signals)
	if len(windows) != 4 {
		t.Fatalf("期望 4 个窗口，实际 %d（%+v）", len(windows), windows)
	}
	byKey := map[string]Window{}
	for _, window := range windows {
		byKey[window.Key] = window
	}
	wantReset := time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)

	bengalfox, ok := byKey["codex/bengalfox-primary"]
	if !ok {
		t.Fatalf("缺少 bengalfox 窗口: %+v", windows)
	}
	if bengalfox.Label != "bengalfox · 7 天" || bengalfox.Status != "rejected" {
		t.Errorf("额外限额应带 limit id 与窗口长度、limit-reached 标 rejected: %+v", bengalfox)
	}
	primary := byKey["codex/primary"]
	if primary.Label != "5 小时" || !closeTo(primary.Remaining, 0.42) || !primary.ResetAt.Equal(wantReset) {
		t.Errorf("primary 窗口解析不对: %+v", primary)
	}
	if primary.Status != "" {
		t.Errorf("没有 limit-reached 时 Status 应为空，实际 %q", primary.Status)
	}
	secondary := byKey["codex/secondary"]
	if secondary.Label != "次要窗口" || secondary.Remaining != 0 {
		t.Errorf("超过 100%% 应夹到剩余 0 并保留窗口: %+v", secondary)
	}
	// websocket 的 additional- 只是命名空间：窗口标识剥掉它，与 HTTP 路径上的同名限额
	// 归一成同一个 key；伴随字段按 original 方言优先查（带 additional- 前缀的头）。
	additional := byKey["codex/weekly-secondary"]
	if additional.Label != "weekly · 2 小时" || additional.Remaining != 1 {
		t.Errorf("additional- 前缀的窗口长度应被读到: %+v", additional)
	}
	if !additional.ResetAt.Equal(wantReset) || additional.Status != "rejected" {
		t.Errorf("additional- 前缀的 reset-at / limit-reached 应被读到: %+v", additional)
	}
	if _, ok := byKey["codex/over-secondary-limit"]; ok {
		t.Errorf("-over-secondary-limit-percent 不是窗口头: %+v", windows)
	}

	// 同一份信号每次给出的顺序必须一致（map 遍历顺序随机，实现里要先排序）。
	second := PassiveWindows("codex", signals)
	for index := range windows {
		if windows[index].Key != second[index].Key {
			t.Fatalf("同一次输入两次解析顺序不同: %v vs %v", windows, second)
		}
	}

	// 缺 used-percent 的头不产出窗口；负数 / 非数字丢弃。
	for _, signals := range []map[string]string{
		{"X-Codex-Primary-Window-Minutes": "300"},
		{"X-Codex-Primary-Used-Percent": "-5"},
		{"X-Codex-Primary-Used-Percent": "abc"},
	} {
		if got := PassiveWindows("codex", signals); len(got) != 0 {
			t.Errorf("无效 used-percent 不应产出窗口: %+v", got)
		}
	}
}

// TestCodexWindowMinutesLabel 钉住 window-minutes 的人话换算。
func TestCodexWindowMinutesLabel(t *testing.T) {
	cases := map[string]string{
		"1440": "1 天",
		"300":  "5 小时",
		"120":  "2 小时",
		"90":   "90 分钟",
		"0":    "主要窗口",
		"-30":  "主要窗口",
		"bad":  "主要窗口",
	}
	for minutes, want := range cases {
		signals := map[string]string{
			"X-Codex-Primary-Used-Percent":   "10",
			"X-Codex-Primary-Window-Minutes": minutes,
		}
		windows := PassiveWindows("codex", signals)
		if len(windows) != 1 {
			t.Fatalf("window-minutes=%q 应产出 1 个窗口: %+v", minutes, windows)
		}
		if windows[0].Label != want {
			t.Errorf("window-minutes=%q 标签 = %q，期望 %q", minutes, windows[0].Label, want)
		}
	}
}

// TestPassiveWindowsCodexAdditionalCompanions 钉住 additional- 方言的伴随字段查找。
//
// Codex 头名有两种方言：HTTP 是 `x-codex-<scope>-window-minutes`，websocket 是
// `x-codex-additional-<scope>-window-minutes`。窗口 Key/Label 口径统一，但伴随字段
// 要按**原方言**优先：additional 窗口先查带前缀的头，取不到再退回无前缀。
func TestPassiveWindowsCodexAdditionalCompanions(t *testing.T) {
	wantReset := time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)

	single := func(t *testing.T, signals map[string]string) Window {
		t.Helper()
		windows := PassiveWindows("codex", signals)
		if len(windows) != 1 {
			t.Fatalf("期望 1 个窗口，实际 %d（%+v）", len(windows), windows)
		}
		return windows[0]
	}

	// 1) additional 方言：伴随字段全套带 additional- 前缀。
	full := single(t, map[string]string{
		"x-codex-additional-weekly-secondary-used-percent":   "25",
		"x-codex-additional-weekly-secondary-window-minutes": "120",
		"x-codex-additional-weekly-secondary-reset-at":       "1800000000",
		"x-codex-additional-weekly-secondary-limit-reached":  "true",
	})
	if full.Key != "codex/weekly-secondary" || full.Label != "weekly · 2 小时" ||
		!closeTo(full.Remaining, 0.75) || !full.ResetAt.Equal(wantReset) || full.Status != "rejected" {
		t.Errorf("additional 方言的伴随字段未全部读到: %+v", full)
	}

	// 2) 只有无前缀伴随头（HTTP 方言混进同一张表）：窗口标识仍归一，伴随字段走回退。
	fallback := single(t, map[string]string{
		"x-codex-additional-weekly-secondary-used-percent": "25",
		"X-Codex-weekly-secondary-window-minutes":          "120",
		"X-Codex-weekly-secondary-reset-at":                "1800000000",
		"X-Codex-weekly-secondary-limit-reached":           "true",
	})
	if fallback.Label != "weekly · 2 小时" || !fallback.ResetAt.Equal(wantReset) || fallback.Status != "rejected" {
		t.Errorf("无前缀伴随头应作为回退读到: %+v", fallback)
	}

	// 3) 两套都发：additional- 优先（值刻意不同，否则无法分辨读了哪一套）。
	preferred := single(t, map[string]string{
		"x-codex-additional-weekly-secondary-used-percent":   "25",
		"x-codex-additional-weekly-secondary-window-minutes": "120",
		"x-codex-additional-weekly-secondary-reset-at":       "1800000000",
		"x-codex-additional-weekly-secondary-limit-reached":  "true",
		"X-Codex-weekly-secondary-window-minutes":            "10080",
		"X-Codex-weekly-secondary-reset-at":                  "1900000000",
		"X-Codex-weekly-secondary-limit-reached":             "false",
	})
	if preferred.Label != "weekly · 2 小时" || !preferred.ResetAt.Equal(wantReset) || preferred.Status != "rejected" {
		t.Errorf("两套伴随头同时存在时应优先 additional-: %+v", preferred)
	}

	// 4) 两套都没有：退回位置说法（与修复前的输出一致）。
	bare := single(t, map[string]string{
		"x-codex-additional-weekly-secondary-used-percent": "25",
	})
	if bare.Label != "weekly · 次要窗口" || !bare.ResetAt.IsZero() || bare.Status != "" {
		t.Errorf("缺伴随头时应退回位置说法: %+v", bare)
	}

	// 5) 普通窗口不会被 additional- 前缀的伴随头串味。
	plain := single(t, map[string]string{
		"X-Codex-Primary-Used-Percent":              "10",
		"X-Codex-additional-primary-window-minutes": "999",
		"X-Codex-additional-primary-reset-at":       "1800000000",
		"X-Codex-additional-primary-limit-reached":  "true",
	})
	if plain.Key != "codex/primary" || plain.Label != "主要窗口" || !plain.ResetAt.IsZero() || plain.Status != "" {
		t.Errorf("普通窗口不应读取 additional- 前缀的伴随头: %+v", plain)
	}
}

// TestHasQuotaHeaders 钉住前缀快判：只有两族额度头才算有信号。
func TestHasQuotaHeaders(t *testing.T) {
	positive := []http.Header{
		{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.42"}},
		{"anthropic-ratelimit-unified-7d-status": {"rejected"}},
		{"X-Codex-Primary-Used-Percent": {"58"}},
		{"x-codex-additional-bengalfox-primary-used-percent": {"12"}},
	}
	for _, headers := range positive {
		if !HasQuotaHeaders(headers) {
			t.Errorf("应识别出额度头: %v", headers)
		}
	}
	negative := []http.Header{
		{},
		{"Content-Type": {"application/json"}},
		{"Anthropic-Ratelimit-Requests-Allowed": {"1"}},
		{"X-Codex": {"1"}},
		{"Anthropic-Ratelimit-Unified": {"1"}},
	}
	for _, headers := range negative {
		if HasQuotaHeaders(headers) {
			t.Errorf("不应识别出额度头: %v", headers)
		}
	}
}

// TestHeaderWindows 钉住从 http.Header 到 Window 的完整换算。
//
// 头名走 http.Header.Set 会归一成 Canonical 形式，另起一个用例用全小写字面量，
// 证明大小写归一不是我行我素的假设。两族头同时出现时不丢任何一族。
func TestHeaderWindows(t *testing.T) {
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1800000000")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
	headers.Set("X-Codex-Primary-Used-Percent", "58")
	headers.Set("X-Codex-Primary-Window-Minutes", "300")
	headers.Set("X-Codex-Primary-Reset-At", "1800000000")
	headers.Set("Content-Type", "application/json")

	windows := HeaderWindows(headers)
	if len(windows) != 2 {
		t.Fatalf("期望 2 个窗口（claude 在前、codex 在后），实际 %d（%+v）", len(windows), windows)
	}
	wantReset := time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)
	if windows[0].Key != "claude/5h" || !closeTo(windows[0].Remaining, 0.58) || windows[0].Status != "rejected" {
		t.Errorf("anthropic 头解析不对: %+v", windows[0])
	}
	if windows[1].Key != "codex/primary" || !closeTo(windows[1].Remaining, 0.42) || windows[1].Label != "5 小时" {
		t.Errorf("codex 头解析不对: %+v", windows[1])
	}
	for _, window := range windows {
		if !window.ResetAt.Equal(wantReset) {
			t.Errorf("重置时间应统一成 %v，实际 %v", wantReset, window.ResetAt)
		}
	}

	// 全小写字面量的键（不经过 Set）也必须能解析。
	lower := http.Header{"anthropic-ratelimit-unified-7d-utilization": {"0.5"}}
	if got := HeaderWindows(lower); len(got) != 1 || got[0].Key != "claude/7d" || !closeTo(got[0].Remaining, 0.5) {
		t.Errorf("小写头名解析不对: %+v", got)
	}

	// 不是额度头就返回 nil，且不进解析路径。
	if got := HeaderWindows(http.Header{"Content-Type": {"application/json"}}); got != nil {
		t.Errorf("无额度头应返回 nil，实际 %+v", got)
	}
	if got := HeaderWindows(nil); got != nil {
		t.Errorf("nil 头应返回 nil，实际 %+v", got)
	}

	// 重复头只认第一份值（额度头都是单值头）。
	repeated := http.Header{"X-Codex-Primary-Used-Percent": {"58", "90"}}
	if got := HeaderWindows(repeated); len(got) != 1 || !closeTo(got[0].Remaining, 0.42) {
		t.Errorf("重复头应取第一份值: %+v", got)
	}
}

// TestHardExhausted 钉住「硬性耗尽」的唯一判据：上游拒绝或剩余归零。
func TestHardExhausted(t *testing.T) {
	cases := []struct {
		name   string
		window Window
		want   bool
	}{
		{name: "rejected", window: Window{Remaining: 0.9, Status: "rejected"}, want: true},
		{name: "exceeded", window: Window{Remaining: 0.9, Status: "exceeded"}, want: true},
		{name: "rate_limited", window: Window{Remaining: 0.9, Status: "rate_limited"}, want: true},
		{name: "rate-limited", window: Window{Remaining: 0.9, Status: "rate-limited"}, want: true},
		{name: "limit_reached", window: Window{Remaining: 0.9, Status: "limit_reached"}, want: true},
		{name: "limit-reached", window: Window{Remaining: 0.9, Status: "limit-reached"}, want: true},
		{name: "状态词带空白与大写", window: Window{Remaining: 0.9, Status: "  Rejected "}, want: true},
		{name: "allowed_warning 只是预警", window: Window{Remaining: 0.5, Status: "allowed_warning"}, want: false},
		{name: "allowed", window: Window{Remaining: 1, Status: "allowed"}, want: false},
		{name: "未知状态不冻结", window: Window{Remaining: 0.3, Status: "whatever"}, want: false},
		{name: "剩余归零", window: Window{Remaining: 0}, want: true},
		{name: "剩余为负", window: Window{Remaining: -0.2}, want: true},
		{name: "剩余 1% 不算耗尽", window: Window{Remaining: 0.01}, want: false},
		{name: "满额", window: Window{Remaining: 1}, want: false},
		{name: "预警但已归零仍算耗尽", window: Window{Remaining: 0, Status: "allowed_warning"}, want: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := HardExhausted(testCase.window); got != testCase.want {
				t.Errorf("HardExhausted(%+v) = %v，期望 %v", testCase.window, got, testCase.want)
			}
		})
	}
}
