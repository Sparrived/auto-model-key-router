package updatecheck

import (
	"os"
	"strconv"
	"strings"
)

// 本文件只解决一件事：**GitHub 直连不可达时怎么还能取到版本与产物**。
//
// 起因是一次真实的失败：`amkr --update` 报
//
//	下载失败: Get "https://github.com/.../amkr_6.1.0_windows_amd64.exe":
//	dial tcp 20.205.243.166:443: connectex: A connection attempt failed ...
//
// 版本检查（api.github.com）是通的，所以问题不在「GitHub 整体封禁」，而是
// github.com / objects.githubusercontent.com 这条分发链路被阻断——重试（4 次）救不了
// 这种**确定性**失败，只能换一条路走。
//
// 做法是 gh-proxy 形态的**前缀代理**：把原始 GitHub 地址原样拼在一个前缀之后
// （`https://ghfast.top/https://github.com/...`）。公共实例对用户是零配置，但把二进制
// 交给第三方中转是有代价的，因此两件事必须同时成立：
//
//   - 用户可以用 GitHubMirrorEnv **替换**这份列表（指向自建反代），或设成空值彻底关闭；
//   - 校验和与产物各自独立地走候选列表，而不是「产物来自镜像就连校验和也从镜像拿」——
//     直连能通时它们都会落在 GitHub 上（见 selfupdate.fetchWithRetry 的说明）。

// GitHubMirrorEnv 是覆盖内置镜像前缀的环境变量。
//
// 取值是**逗号分隔的前缀列表**，每个前缀后直接拼原始 GitHub URL。语义刻意分成三态，
// 因为「没设置」和「设成空」是两种不同的意图：
//
//	未设置        -> 用 DefaultGitHubMirrorPrefixes（直连失败后自动回退）
//	设置但为空串  -> 关闭镜像回退，只走直连（例如企业内网只允许白名单出口）
//	设置了非空值  -> 只用这些前缀，**替换**内置列表（例如自建反代）
//
// 与 Python 版的差异：参照实现没有镜像概念，这是 Go 版新增的可达性兜底。
const GitHubMirrorEnv = "AMKR_GITHUB_MIRROR"

// DefaultGitHubMirrorPrefixes 是内置的公共加速前缀，按「先试谁」排序。
//
// 顺序不是随手排的：公共实例的**覆盖范围**并不一致——实测同一个 api.github.com 地址，
// gh.llkk.cc 返回 200，而 ghfast.top / ghproxy.net 返回 403、gh-proxy.com 返回 502
// （它们只代理 github.com 的下载路径）。版本检查走的是 api.github.com，因此把能覆盖
// 两边的实例排在前面，其余实例作为下载路径的冗余。
//
// 这些是**第三方**服务，随时可能限流、改址或下线；候选列表整体失败只是退回「更新失败」
// 的既有行为，不会让程序变坏。用户要完全掌控时可以按 GitHubMirrorEnv 换成自建前缀。
var DefaultGitHubMirrorPrefixes = []string{
	"https://gh.llkk.cc/",
	"https://ghfast.top/",
	"https://gh-proxy.com/",
	"https://ghproxy.net/",
}

// GitHubMirrorPrefixes 返回当前生效的镜像前缀列表（见 GitHubMirrorEnv 的三态语义）。
func GitHubMirrorPrefixes() []string {
	raw, configured := os.LookupEnv(GitHubMirrorEnv)
	if !configured {
		return DefaultGitHubMirrorPrefixes
	}
	prefixes := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		// 前缀与原始 URL 是直接拼接的，缺尾斜杠会拼出
		// `https://mirror.examplehttps://github.com/...` 这种畸形地址。
		if !strings.HasSuffix(trimmed, "/") {
			trimmed += "/"
		}
		prefixes = append(prefixes, trimmed)
	}
	return prefixes
}

// GitHubCandidates 返回取回 rawURL 的候选地址：**直连永远排在第一位**，镜像按序在后。
//
// 直连优先而不是镜像优先：多一跳中转就多一次被篡改、被缓存陈旧内容的机会，能直连时
// 不该走它。镜像只在直连失败之后才被用到。
//
// 非 GitHub 地址原样返回（只有一个候选）：镜像前缀是 GitHub 专用的，套到别的地址上
// 只会造出一个必然失败的 URL。
func GitHubCandidates(rawURL string) []string {
	candidates := []string{rawURL}
	if !isGitHubURL(rawURL) {
		return candidates
	}
	return append(candidates, mirrorURLs(rawURL)...)
}

// mirrorURLs 把原始地址逐个套上前缀。
func mirrorURLs(rawURL string) []string {
	prefixes := GitHubMirrorPrefixes()
	mirrored := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		mirrored = append(mirrored, prefix+rawURL)
	}
	return mirrored
}

// isGitHubURL 判断地址是否属于 GitHub 的直连域名。
//
// api.github.com 与 github.com 都要认：前者是版本检查，后者是产物与校验和下载。
func isGitHubURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://github.com/") ||
		strings.HasPrefix(rawURL, "https://api.github.com/")
}

// MirrorHint 是全部候选都失败时追加给用户的排查提示。
//
// 只加在**最终**错误上（每个候选各自的失败照原样返回）：用户一条条看中间错误没用，
// 需要的是「还能做什么」。导出是因为 selfupdate 的下载路径要拼同一条提示。
func MirrorHint(mirrorCount int) string {
	hint := "（直连与 " + strconv.Itoa(mirrorCount) + " 个镜像加速地址均失败"
	if mirrorCount == 0 {
		hint = "（未配置镜像加速地址"
	}
	return hint + "；可用 " + GitHubMirrorEnv + " 指定自建镜像前缀，或设置 HTTPS_PROXY 让请求走代理）"
}
