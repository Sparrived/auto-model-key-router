package proxy

import (
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/keypool"
)

// balanceReason 给限流日志凑一句「凭什么判它欠费」。
//
// 两种来路分别交代：
//   - 正文命中了信号（`insufficient_quota`、`余额不足`……）：信号本身就是最准的理由，
//     直接用它——它是判定表里的成员，读日志的人可以拿它去对照那张表；
//   - 只能靠 402 判定（正文里没有可识别的信号：网关错误页、HTML、空体）：附上原文摘要。
//     这是最可能误伤的一类判定（上游把 402 用作别的「需要付款」语义时正文才有线索），
//     所以宁可多带一句原文；正文确实为空时退回状态码本身。
//
// 摘要复用 readableReason（上游非 200 日志那套归一：折叠空白 + 按码点截断），不另写一份：
// 两处的用途与上限（WebUI 服务日志面板只取最近 64 KiB）完全相同。
//
// 上游原文保留在日志里，是为了让「真欠费」与「判定误伤」能一眼分开——判定是保守的
// 字符串匹配，误伤概率不为零，而这条日志是事后唯一能查的证据。
func balanceReason(statusCode int, content []byte) string {
	if signal := keypool.BalanceBodySignal(content); signal != "" {
		return signal
	}
	if excerpt := readableReason(strings.ToValidUTF8(string(content), "\uFFFD")); excerpt != "" {
		return strconv.Itoa(statusCode) + "：" + excerpt
	}
	return strconv.Itoa(statusCode)
}
