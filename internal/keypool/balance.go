package keypool

import (
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// paymentRequiredStatus 是 HTTP 的 402（Payment Required）。
//
// 它是「欠费」这一类判定的强信号：402 的语义就是「先付款」，上游用它表达
// 「账号余额不足」，与「限流」是两件必须分开处理的事——限流会自己好，欠费不会。
const paymentRequiredStatus = 402

// balanceCodeTokens 是「机器可读」的欠费标记（`error.code` / `error.type` /
// `error.status` 等字段），已归一成小写下划线形式。
//
// 这份表刻意**只收**明确指向「余额/账单」的取值，把限流类的取值（`rate_limit_exceeded`、
// `resource_exhausted`、`too_many_requests`）留给原有冷却：认错了的代价不是少一次重试，
// 而是把一个正常账号拦在路由之外。
//
// 一个已知的边界：`insufficient_quota` 在 OpenAI 上是「额度/账单额度用尽」（得处理账单，
// 不会自己好），在 Azure OpenAI 上却可能是「部署的 TPM/RPM 配额这一分钟用完了」（几十秒
// 后自愈）。两者在响应体上无法区分，这里按 OpenAI 的语义采信；用 Azure 部署时若被误限流，
// 把 `key_balance_cooldown_seconds` 调小或设 0 即可（见 docs/USAGE.md）。
var balanceCodeTokens = map[string]bool{
	"insufficient_quota":         true, // OpenAI（Azure 上是另一回事，见上）
	"insufficient_balance":       true, // DeepSeek / Moonshot 等
	"insufficient_fund":          true,
	"insufficient_funds":         true,
	"insufficient_credit":        true,
	"insufficient_credits":       true,
	"credit_balance_too_low":     true,
	"credit_balance_exhausted":   true, // OpenAI 文档里的另一种写法
	"billing_hard_limit":         true,
	"billing_hard_limit_reached": true, // OpenAI
	"payment_required":           true,
}

// balanceMessagePhrases 是「只有人话」时的兜底信号，按小写子串匹配。
//
// 上游错误体并不总有结构：网关会换成自己的错误页，国内供应商用中文文案。这里收的
// 每一条都同时交代了「钱」，而不是只说「超了」——只说「超了」的文案在限流里到处都是，
// 认错的代价是把这个账号挡在门外最长 30 分钟。
//
// 两个**不能**进来的反例（都有真实上游这么回）：
//   - `You exceeded your current quota, please check your plan and billing details.`
//     它是 OpenAI 的 insufficient_quota 正文，但 **Google 的 Gemini 免费档 429 用的是
//     同一句**，而后者 `status` 是 `RESOURCE_EXHAUSTED`、几十秒后自愈；
//   - `Quota exceeded for quota metric ...`（Google 的每分钟/每日配额）。
//
// 代价是「只回文案、不带标记」的 OpenAI 兼容中转会被漏判。漏判只是回到改动前的行为
// （普通冷却 + 重试），误判却会让一个正常账号被挡 5~30 分钟，两者不对称。
var balanceMessagePhrases = []string{
	"insufficient balance",
	"insufficient credit",
	"insufficient fund",
	"insufficient quota",
	"credit balance is too low", // Anthropic
	"credit balance too low",
	"billing hard limit",
	"out of credits",
	"used all available credits", // xAI：额度用光或触到月度消费上限
	"please top up",
	"余额不足",
	"欠费",
	"请充值",
}

// InsufficientBalance 报告一次上游响应是否表示「账号余额不足（欠费）」。
//
// 判定顺序与理由：
//   - 状态码 < 400 直接为假：成功响应体里出现这些字样多半是内容而非错误（例如
//     用户让模型解释「余额不足」是什么意思），拿它去限流一个正常账号是灾难；
//   - 402 直接为真，不看响应体：这是该状态码唯一的产品语义；
//   - 其余状态码看响应体里的信号（先机器可读标记、后整份文本扫人话，见
//     BalanceBodySignal）。两道都做是因为上游行为不一致：DeepSeek 的 402 带
//     `"message": "Insufficient Balance"`，OpenAI 的 429 把人话和 `code` 分开写，
//     而网关转译过的错误页可能只剩一句中文。
//
// 这条判定只回答「该不该把账号按欠费处理」，不参与「这次请求要不要重试」——后者仍由
// RetryableStatusCodes 决定，两者的取舍见 proxy 包 recordUpstreamResponse 的说明。
func InsufficientBalance(statusCode int, content []byte) bool {
	if statusCode < 400 {
		return false
	}
	if statusCode == paymentRequiredStatus {
		return true
	}
	return BalanceBodySignal(content) != ""
}

// BalanceBodySignal 返回响应体里命中的欠费信号，没有命中返回空串。
//
// **它只回答「正文说了什么」，不等于判定结论**：结论由 InsufficientBalance 给，并且额外
// 受状态码门槛约束（<400 一律不为真）。两者分开是有意的——判定是一件事，把判定的依据
// 写进日志是另一件事，中间隔着一个状态码门槛，读数的人要能看出是哪一层拦下的。
//
// 之所以要导出「依据」，是因为「是/否」不足以支撑事后追责：一条限流日志若只写「已自动
// 限流」，读的人分不清是 `insufficient_quota` 命中了还是网关的 402 命中了，也就无从判断
// 这次判定有没有误伤。返回值就是依据本身：命中的机器可读标记（归一后的形式，如
// `insufficient_quota`）或命中的文案（按表里的小写形式，如 `余额不足`）。
//
// 机器可读标记优先：两样都在时（OpenAI 的 429 既写 `"code": "insufficient_quota"` 又写
// 一句 billing 文案），标记更短、更明确，也更适合做日志字段与后续统计的取值。
//
// 402 那一路的判定依据是状态码本身，正文里未必有信号，因此由调用方补（见 proxy 包的
// balanceReason）。
func BalanceBodySignal(content []byte) string {
	if len(content) == 0 {
		return ""
	}
	if data, err := canonical.Parse(content); err == nil {
		// 取值口径与 proxysupport 的错误体解析同源：不同供应商把标记放在
		// `error.code` / `error.type` / `error.status`，也有的直接放在顶层。
		for _, field := range []*canonical.Value{
			data.Lookup("error").Lookup("code"),
			data.Lookup("error").Lookup("type"),
			data.Lookup("error").Lookup("status"),
			data.Lookup("code"),
			data.Lookup("type"),
			data.Lookup("status"),
		} {
			token := normalizeBalanceToken(field.StringValue())
			if balanceCodeTokens[token] {
				return token
			}
		}
	}
	if phrase, found := findBalancePhrase(string(content)); found {
		return phrase
	}
	return ""
}

// findBalancePhrase 报告文本里是否出现欠费文案（大小写不敏感），并返回命中的那一条。
//
// 直接在小写后的原文里找子串，不做空白折叠：折叠要先复制整份响应体（网关的错误页动辄
// 几十 KB），而收益只是多认出「insufficient␣␣balance」这类上游把空格写歪的情况——表里
// 这几条在真实上游都是单空格写法。代价明确记在这里：那种写法会漏判，而漏判只是回到
// 改动前的行为（普通冷却 + 重试），比误判轻得多。
func findBalancePhrase(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	lower := strings.ToLower(text)
	for _, phrase := range balanceMessagePhrases {
		if strings.Contains(lower, phrase) {
			return phrase, true
		}
	}
	return "", false
}

// normalizeBalanceToken 把标记字段归一成小写下划线形式。
//
// `Insufficient Quota`、`insufficient-quota`、`INSUFFICIENT_QUOTA` 都是同一个
// 东西，而上游这三种写法都存在；归一之后表里每个取值只需要写一遍。
func normalizeBalanceToken(token string) string {
	trimmed := strings.ToLower(strings.TrimSpace(token))
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(trimmed))
	pendingUnderscore := false
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			if pendingUnderscore && builder.Len() > 0 {
				builder.WriteByte('_')
			}
			pendingUnderscore = false
			builder.WriteRune(char)
		default:
			pendingUnderscore = true
		}
	}
	return builder.String()
}
