// Package updatecheck 移植 update.py 中**与分发方式无关**的版本检查部分。
//
// 产品决策 8 砍掉了 update.py 的自更新机制（约 573 行：下载、替换自身、Windows 更新
// 助手脚本、uv 工具目录探测等），理由是 Go 版改由 `go install` / 包管理器分发。
// 但同一个文件里的**版本检查**（约 140 行）与分发方式无关，且被两条路由与 WebUI 使用：
//
//   - `POST /api/update/check`（management 面，management_api.py:493）
//   - `GET /api/tool`（ops 面，ops_api.py:156）
//
// 因此这里只移植 version_numbers / comparable_version / is_newer_version /
// check_latest_pypi / check_latest_release / check_latest_version 及其结果类型。
//
// 关于 PyPI：Go 版发布到 GitHub Releases，PyPI 那一支在 Python 退役后会变成死代码。
// 但迁移期两种实现并存，**保持与参照实现逐字段一致**优先，故照原样保留 PyPI 优先、
// GitHub 兜底的顺序；退役 Python 时再单独删掉 PyPI 分支。
package updatecheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 常量。
//
// **只保留 GitHub 一组**：Python 版已退役，PyPI 上那个包停在最后一个 Python 版本、不会
// 再更新，因此版本检查只查 GitHub Releases。原先"先 PyPI、失败再 GitHub"的顺序在退役后
// 变成错误行为——PyPI 返回的是陈旧但**成功**的响应，于是永远不回退 GitHub，用户跑着 Go
// 新版却一直被告知"已是最新"（详见 CheckLatestVersion）。
const (
	// GitHubRepository 是源码仓库。
	GitHubRepository = "Sparrived/auto-model-key-router"
	// GitHubReleasesURL 是发布列表页（拿不到单次发布页时的兜底）。
	GitHubReleasesURL = "https://github.com/" + GitHubRepository + "/releases"
	// GitHubLatestReleaseAPI 是「最新发布」接口。
	GitHubLatestReleaseAPI = "https://api.github.com/repos/" + GitHubRepository + "/releases/latest"
)

// numberPattern 对应 Python 的 re.findall(r"\d+", ...)。
//
// ponytail: Go 正则的 \d 是 **ASCII 限定**的，而 Python 3 的 \d 匹配 Unicode 十进制
// 数字（`int("４５")` == 45）。版本号里出现全角/其它文种数字不是现实场景，故不收窄到
// 复刻 Python 的 Unicode 语义；该差异由 TestVersionNumbersUnicodeDigitsDiverge 锁定，
// 将来若真需要，改用一个逐码点判断的扫描器即可（internal/metrics 已有同类 helper）。
var numberPattern = regexp.MustCompile(`\d+`)

// Result 对应 update.py:40 的 VersionCheckResult。
//
// 除 CurrentVersion 外全部用指针：参照实现用 None 表示「没有」，而 None 与空串在
// 返回给调用方的 JSON 里是 `null` 与 `""` 两种不同结果（路由直接把它拼进响应体）。
type Result struct {
	CurrentVersion string
	LatestVersion  *string
	LatestTag      *string
	ReleaseURL     *string
	Source         *string
	ArtifactURL    *string
	ArtifactSHA256 *string
	Error          *string
}

// UpdateAvailable 对应 VersionCheckResult.update_available 属性。
func (r Result) UpdateAvailable() bool {
	return r.LatestVersion != nil && IsNewerVersion(*r.LatestVersion, r.CurrentVersion)
}

// VersionNumbers 把版本串拆成数字元组，对应 update.py:64。
//
// 先按 "+" 切掉 build 元数据（只切一次，取前半），再取全部数字段。
func VersionNumbers(version string) []int64 {
	head, _, _ := strings.Cut(version, "+")
	parts := numberPattern.FindAllString(head, -1)
	// 参照实现返回元组；没有数字时是空元组，因此这里返回**空切片而非 nil**，
	// 与参照实现的形状一致。
	numbers := make([]int64, 0, len(parts))
	for _, part := range parts {
		// ponytail: Python 的 int 无上限，这里收窄到 int64。版本号分量不可能达到
		// 64 位上限；溢出时按 0 处理并保持可比较，不会 panic。
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			value = 0
		}
		numbers = append(numbers, value)
	}
	return numbers
}

// ComparableVersion 把版本串补齐到至少三段，对应 update.py:68。
//
// 只在**不足**三段时补零，超过三段原样保留——因此 "1.2.3.4" 与 "1.2.3" 不相等。
func ComparableVersion(version string) []int64 {
	numbers := VersionNumbers(version)
	if missing := 3 - len(numbers); missing > 0 {
		numbers = append(numbers, make([]int64, missing)...)
	}
	return numbers
}

// IsNewerVersion 对应 update.py:73 的元组字典序比较。
//
// 注意 Python 元组比较在「一方是另一方前缀」时按长度决胜：(1,2,3) < (1,2,3,4)。
func IsNewerVersion(latest, current string) bool {
	return compareNumbers(ComparableVersion(latest), ComparableVersion(current)) > 0
}

// compareNumbers 复刻 Python 元组比较：逐位比较，全等时短者更小。
func compareNumbers(left, right []int64) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		if left[i] != right[i] {
			if left[i] < right[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	}
	return 0
}

// GitHubSourceArchiveURL 对应 update.py:170。自更新虽已砍掉，但该 URL 仍被展示层使用。
func GitHubSourceArchiveURL(tag string) string {
	return "https://github.com/" + GitHubRepository + "/archive/refs/tags/" + tag + ".zip"
}

// Fetcher 是一次 JSON 取回。注入它是为了让测试**不联网**。
//
// 返回的 *canonical.Value 必须是 JSON 对象；非对象由调用方判为错误（对应 Python
// fetch_json 里的 `if not isinstance(data, dict)`）。
type Fetcher func(url string, headers map[string]string, timeout time.Duration) (*canonical.Value, error)

// HTTPFetcher 用给定的 http.Client 做真实请求，对应 update.py:77 的 fetch_json。
//
// client 为 nil 时用 http.DefaultClient。超时通过请求 context 施加，与 Python 的
// `urlopen(request, timeout=...)` 语义一致。
//
// **与参照实现的不同之处**：直连不可达时按 GitHubCandidates 自动回退到镜像前缀
// （见 mirror.go）。超时在候选间**均分**，因此整体耗时与候选数量无关——否则「GitHub
// 全被阻断」的用户会从等 10 秒变成等 50 秒，而他要的那个「查不到」结论一秒都没提前。
//
// 失败时返回**直连**的错误（附排查提示），而不是最后一个镜像的次生错误：用户要修的是
// 自己这边的网络，镜像报的 403/502 只是症状的中转。
func HTTPFetcher(client *http.Client) Fetcher {
	if client == nil {
		client = http.DefaultClient
	}
	return func(url string, headers map[string]string, timeout time.Duration) (*canonical.Value, error) {
		candidates := candidatesFor(url)
		perAttempt := timeout
		if timeout > 0 && len(candidates) > 1 {
			perAttempt = timeout / time.Duration(len(candidates))
		}
		var directErr error
		for index, candidate := range candidates {
			value, err := fetchJSON(client, candidate, headers, perAttempt)
			if err == nil {
				return value, nil
			}
			if index == 0 {
				directErr = err
			}
		}
		if directErr == nil {
			// 只可能出现在候选为空列表时；保留一条可读的兜底错误。
			return nil, errors.New("没有可用的下载地址 " + MirrorHint(0))
		}
		return nil, fmt.Errorf("%w %s", directErr, MirrorHint(len(candidates)-1))
	}
}

// candidatesFor 是候选地址的来源。
//
// 抽成变量而不是直接调 GitHubCandidates：测试要注入两个**本地**地址（一个必然失败、
// 一个必然成功）来验证回退，而 GitHubCandidates 只对 github.com/api.github.com 展开，
// 注入不进去——真去连 GitHub 的测试既慢又要求联网。生产代码从不改写它。
var candidatesFor = GitHubCandidates

// fetchJSON 对**单个**地址完成一次取回。非 2xx、非 JSON、非对象都算失败。
func fetchJSON(client *http.Client, url string, headers map[string]string, timeout time.Duration) (*canonical.Value, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	// 与 Python 一致：整体超时覆盖取回过程。用 Client.Timeout 会污染共享 client，
	// 因此这里只在本请求上施加。
	if timeout > 0 {
		ctx, cancel := context.WithTimeout(request.Context(), timeout)
		defer cancel()
		request = request.WithContext(ctx)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Python 的 urllib 会对非 2xx 抛 HTTPError；这里给出等价的可读错误。
		return nil, &HTTPStatusError{StatusCode: response.StatusCode, URL: url}
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	parsed, err := canonical.Parse(body)
	if err != nil {
		// 错误文本与 Python 的 json.JSONDecodeError 不同（见 CheckLatestPyPI 的说明）。
		return nil, err
	}
	if !parsed.IsObject() {
		return nil, errNotJSONObject
	}
	return parsed, nil
}

// HTTPStatusError 表示上游返回非 2xx。
type HTTPStatusError struct {
	StatusCode int
	URL        string
}

// Error 实现 error。
func (e *HTTPStatusError) Error() string {
	return "HTTP " + strconv.Itoa(e.StatusCode) + ": " + e.URL
}

// errNotJSONObject 对应 update.py:85 的「响应不是 JSON 对象。」。
type notJSONObjectError struct{}

func (notJSONObjectError) Error() string { return "响应不是 JSON 对象。" }

var errNotJSONObject = notJSONObjectError{}

// CheckLatestRelease 对应 update.py:137。
func CheckLatestRelease(fetch Fetcher, currentVersion string, timeout time.Duration) Result {
	result := Result{CurrentVersion: currentVersion}
	data, err := fetch(GitHubLatestReleaseAPI, map[string]string{
		"Accept":     "application/vnd.github+json",
		"User-Agent": "auto-model-key-router/" + currentVersion,
	}, timeout)
	if err != nil {
		result.Error = stringPtr(err.Error())
		return result
	}

	tag := strings.TrimSpace(pyStringOrEmpty(data.Lookup("tag_name")))
	if tag == "" {
		result.Error = stringPtr("GitHub Release 响应中缺少 tag_name。")
		return result
	}
	// 只去**一个**前缀 v/V，且大小写各判一次（update.py:154 连续两次 removeprefix）。
	latestVersion := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
	releaseURL := pyStringOrEmpty(data.Lookup("html_url"))
	if releaseURL == "" {
		releaseURL = GitHubReleasesURL
	}
	result.LatestVersion = stringPtr(latestVersion)
	result.LatestTag = stringPtr(tag)
	result.ReleaseURL = stringPtr(releaseURL)
	result.Source = stringPtr("GitHub")
	return result
}

// CheckLatestVersion 检查最新版本：**只查 GitHub Releases**。
//
// 这里与参照实现（update.py:159 的"先 PyPI、失败再 GitHub"）**故意不同**，原因不是偏好：
// Python 版退役后 PyPI 上那个包会冻结在最后一个 Python 版本，而发布改在 GitHub。
// 若沿用"PyPI 优先"，PyPI 会返回一个陈旧但**成功**的响应，于是永远不回退 GitHub——
// 用户跑着 Go 新版本，界面上却一直显示"当前已是最新版本"，且完全静默（无报错、无日志）。
// 因此退役时删掉 PyPI 分支，只以 GitHub Releases 为准。
//
// 直接委托 CheckLatestRelease：不再有"兜底"的概念，故 Result.FallbackError 也已移除。
func CheckLatestVersion(fetch Fetcher, currentVersion string, timeout time.Duration) Result {
	return CheckLatestRelease(fetch, currentVersion, timeout)
}

// pyStringOrEmpty 复刻 Python 的 `str(value or "")`：假值一律得到空串。
//
// 不能直接 PyStr：Python 的 `or` 会把 0、空串、空列表、空对象、false、null 都当假，
// 而 PyStr 会把它们渲染成 "0"、"[]"、"{}"、"False"、"None"。
func pyStringOrEmpty(value *canonical.Value) string {
	if value == nil || !value.Truthy() {
		return ""
	}
	return value.PyStr()
}

// firstTruthyString 返回第一个真值参数的字符串形式，对应 Python 的 `a or b or c`。
func firstTruthyString(values ...*canonical.Value) string {
	for _, value := range values {
		if text := pyStringOrEmpty(value); text != "" {
			return text
		}
	}
	return ""
}

// stringPtr 返回字符串指针的便捷函数。
func stringPtr(value string) *string { return &value }
