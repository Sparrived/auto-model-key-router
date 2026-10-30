package api

import (
	"math"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/quota"
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
// 解析只有 quota.Label 一份实现（被动窗口与现场额度的窗口名必须说同一套话）；
// 保留这个函数名是因为 api 内还有若干处在用。
func quotaWindowLabel(window string) string {
	return quota.Label(window)
}

// passiveQuotaWindows 把 CPA 采到的被动信号翻译成额度窗口。
//
// 解析住在 internal/quota（代理请求路径的响应头解析与这里共用同一份），本函数只做
// 两件事：把 map[string]any 归一成字符串表，再把 quota.Window 套上看板字段。
// 只覆盖 claude 与 codex：CPA 的 ProviderSupportsQuotaObservation 也只认这两个
// （外加 devin，但 devin 一侧没有任何头会被白名单收下，采出来永远是空表）。其余
// provider 的上游额度必须走主动探测，不是这里能补的。
func passiveQuotaWindows(provider string, signals map[string]any) []cpaQuotaWindow {
	windows := quota.PassiveWindows(provider, stringSignals(signals))
	out := make([]cpaQuotaWindow, 0, len(windows))
	for _, window := range windows {
		out = append(out, cpaQuotaWindow{
			Key: window.Key,
			// Window 是上游原始窗口名，底限判定靠它识别 5h/7d；不能只靠 Key，
			// codex 的 Key 带 limit id（可能叫 weekly），按词面猜会认错档位。
			Window:    window.Raw,
			Label:     window.Label,
			Remaining: window.Remaining,
			ResetAt:   quota.ResetText(window.ResetAt),
			Status:    window.Status,
			Source:    "passive",
		})
	}
	return out
}

// stringSignals 把信号表渲染成字符串表，供 quota 包解析。
//
// 表在 CPA 那边是 map[string]string，但这里不假设类型：一个数字类型的值不值得让
// 整份账号清单解析失败（JSON 解码会在类型不符时直接报错，那会连累同一实例下所有
// 正常的账号）。认不出的类型当成没有。
func stringSignals(signals map[string]any) map[string]string {
	if len(signals) == 0 {
		return nil
	}
	out := make(map[string]string, len(signals))
	for name, raw := range signals {
		if text := signalText(raw); text != "" {
			out[name] = text
		}
	}
	return out
}

// signalText 把信号值渲染成文本。
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
// 解析本身住在 quota.ParseResetText（与被动窗口共用）：Unix 秒、毫秒时间戳、
// RFC3339（可带小数秒）都认。只认一种就会静默丢掉重置时间——那是看板上最该看见的
// 一列。解析不出来时返回空串而不是零值时间：界面上「没有重置时间」与「1970 年重置」
// 必须能分开。
func quotaTimeText(raw string) string {
	return quota.ResetText(quota.ParseResetText(raw))
}
