// Package selfupdate 实现从 GitHub Releases 自更新：挑选产物、下载、校验、就地替换。
//
// 为什么 Go 版可以做得比 Python 版小得多：Python 的 update.py 有约 573 行，绝大部分
// 在处理「运行中的可执行文件被锁住」。Windows 其实允许**重命名**正在运行的 exe
// （只是不允许覆盖或删除它），因此可以先把旧的改名挪开、再把新的放到原位——旧进程
// 继续服务到重启为止。整个替换就是两次 os.Rename，没有平台分支。
//
// 本包只做**纯逻辑与文件操作**，不碰服务重启：那需要 internal/service 的平台知识，
// 由调用方（cmd/amkr）编排。这样下载、校验与替换都能脱离进程管理单独测试。
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/updatecheck"
)

// ReleasesBaseURL 是发布物的下载根地址（不含标签）。
//
// 是变量而不是常量：测试要把它指向 httptest 假服务，否则测一次成功路径就得真的去
// GitHub 下载十几兆。生产代码从不改写它。
var releasesBaseURL = "https://github.com/" + updatecheck.GitHubRepository + "/releases/download"

// ReleasesBaseURL 返回当前生效的下载根地址。
func ReleasesBaseURL() string { return releasesBaseURL }

// AssetName 返回某平台的产物文件名，与 release.yml:125 的命名逐字一致：
//
//	amkr_<version>_<goos>_<goarch>[.exe]
//
// 两边必须一致，否则自更新会去下一个不存在的文件（表现为 404）。该断言由测试锁定。
func AssetName(version, goos, goarch string) string {
	name := fmt.Sprintf("amkr_%s_%s_%s", version, goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// DownloadURL 返回产物地址；ChecksumsURL 返回同标签下的校验和文件地址。
func DownloadURL(version, asset string) string {
	return ReleasesBaseURL() + "/v" + version + "/" + asset
}

func ChecksumsURL(version string) string {
	return ReleasesBaseURL() + "/v" + version + "/checksums.txt"
}

// ParseChecksums 从 checksums.txt 里取出某个文件的 sha256。
//
// 行格式与 `sha256sum` 一致：`<hash>  <name>`（二进制模式为 `<hash> *<name>`）。
// 找不到返回 false——调用方必须据此**拒绝安装**，绝不能跳过校验。
func ParseChecksums(text, asset string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// VerifySHA256 校验文件的 sha256，不匹配返回错误。
func VerifySHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if expected = strings.ToLower(expected); actual != expected {
		return fmt.Errorf("sha256 校验失败：期望 %s，实际 %s", expected, actual)
	}
	return nil
}

// 下载路径的重试参数。
//
// 为什么必须有重试：实测一次 `amkr --update` 就死在
// `TLS handshake timeout` 上——单个 `client.Get` 没有任何重试，一次传输层抖动就
// 让整个更新失败，而用户看到的是"更新失败"，重跑一次往往就好了。这类失败是**瞬时**的
// （TLS 握手超时、连接重置、DNS 抖动、GitHub 偶发 5xx/429），正是重试能解决的。
//
// 与 internal/config/persist.go 的 replaceWithRetry 同形（4 次尝试、延迟倍增），
// 不另造抽象。
//
// **重试救不了"整条链路被阻断"**：实测 `amkr --update` 的另一类失败是
// `dial tcp 20.205.243.166:443: connectex: ...` —— 版本检查（api.github.com）通得过，
// 但 github.com 这条分发链路不通，重试 4 次只是把同一个超时等 4 遍。因此每一轮还按
// updatecheck.GitHubCandidates 的顺序走过**镜像加速地址**（见下面 fetchWithRetry）。
const (
	downloadAttempts   = 4
	downloadRetryDelay = 500 * time.Millisecond
)

// retrySleep 是重试之间的等待，抽成变量让测试把它换成空操作（否则每个失败用例都要
// 真等 3.5 秒）。生产代码从不改写它。
var retrySleep = time.Sleep

// httpStatusError 是非 2xx 响应。
//
// 单独成类型而不是直接 fmt.Errorf：重试决策必须能分辨「5xx/429 值得再试」与
// 「404 再试一百次也是 404」，靠错误文本做不了这个判断。
type httpStatusError struct {
	Code int
	URL  string
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.URL) }

// retryable 报告这个状态码是否值得重试。
//
// 404/403 这类是**确定性**失败：产物名字与 release.yml 漂移、标签还没发布、或校验和
// 文件里就没有这个平台。重试只会让用户多等几秒再看到同一个结论，因此立刻返回。
func (e *httpStatusError) retryable() bool {
	return e.Code == http.StatusRequestTimeout ||
		e.Code == http.StatusTooManyRequests ||
		e.Code >= 500
}

// permanentError 标记「重试也不会变好」的本地失败（磁盘写不进、目录不可写）。
//
// 存在的理由：消费响应体时两类错误混在一起——**读到一半断流**（网络抖动，必须重试）
// 与**写不进本地文件**（磁盘满，重试无用）。不区分的话，前者（下载 15MB 时连接被重置）
// 会被当成后者直接放弃，而那正是最该重试的情形。
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// candidatesFor 是候选地址的来源（直连 + 镜像加速）。
//
// 抽成变量而不是直接调 updatecheck.GitHubCandidates：测试要注入两个**本地**地址
// （一个必然失败、一个必然成功）来验证回退，而 GitHubCandidates 只对 github.com 展开，
// 注入不进去——真去连 GitHub 的测试既慢又要求联网。生产代码从不改写它。
var candidatesFor = updatecheck.GitHubCandidates

// fetchWithRetry 带重试地取回 url，把响应体交给 consume 消费。
//
// 重试规则：
//   - 连接层失败（TLS 握手超时、连接重置、DNS）→ 换下一个候选地址，本轮不再回头试它。
//   - 5xx / 429 / 408 → 重试；其余状态码立刻返回。
//   - consume 返回 permanentError（本地写失败）→ 立刻返回。
//   - consume 返回其它错误（读到一半断流）→ 重试。
//
// 地址回退：候选顺序是「直连 + 镜像加速」（updatecheck.GitHubCandidates），直连能通时
// 永远用直连，镜像只在直连失败后被用到。**产物与校验和各自独立地走这条回退**，因此
// 只要 GitHub 直连有一条通，校验和就拿的是 GitHub 的原件；两边都只能走镜像时，校验和
// 退化为「防传输损坏」而不再是「防中转方替换」——这是可达性与信任之间无法两全的取舍，
// 想完全避免就把 AMKR_GITHUB_MIRROR 指向自建反代（或设成空值只走直连）。
//
// 「连不上」与「连上了但这次没成」被区别对待，这是有意为之：一次 TCP 拨号超时在
// **整条链路被阻断**时是确定性结论（用户实测的失败就是它），此时唯一有意义的事是赶紧
// 换下一条路；而它只出现在**没有别的路可走**时（例如非 GitHub 地址、或被用户显式关掉了
// 镜像），就是一次普通的瞬时抖动，值得照旧重试到 downloadAttempts 次。
func fetchWithRetry(client *http.Client, url string, consume func(io.Reader) error) error {
	if client == nil {
		client = http.DefaultClient
	}
	candidates := candidatesFor(url)
	var lastErr error
	var directErr error
	delay := downloadRetryDelay
	for round := 0; round < downloadAttempts && len(candidates) > 0; round++ {
		if round > 0 {
			retrySleep(delay)
			delay *= 2
		}
		// 本轮结束时仍值得再试的地址。连不上的地址在**还有别的候选**时被剔除：把它留在
		// 池子里，每一轮都要白等一次 TCP 超时（实测那正是用户遇到的 20 秒级卡顿）。
		survivors := make([]string, 0, len(candidates))
		for index, candidate := range candidates {
			err, unreachable := attemptFetch(client, candidate, consume)
			if err == nil {
				return nil
			}
			lastErr = err
			if round == 0 && index == 0 {
				directErr = err
			}
			var permanent *permanentError
			if errors.As(err, &permanent) {
				return permanent.err
			}
			var status *httpStatusError
			if errors.As(err, &status) && !status.retryable() {
				return status
			}
			if unreachable && len(candidates) > 1 {
				continue
			}
			survivors = append(survivors, candidate)
		}
		candidates = survivors
	}
	if directErr != nil {
		// 报直连的错误 + 还能做什么；中间那一串镜像错误对用户没有价值。
		return fmt.Errorf("%w %s", directErr, updatecheck.MirrorHint(len(candidatesFor(url))-1))
	}
	return lastErr
}

// attemptFetch 对**单个**候选地址做一次取回。
//
// 返回值 unreachable 表示 `client.Get` 自己失败了（拨号/TLS/DNS，没拿到任何响应）。
// 它必须与「拿到了响应但这次没成」（5xx、读到一半断流）分开：前者的重试预算应当先花在
// **换一条路**上，后者才是原地重试的对象。
func attemptFetch(client *http.Client, url string, consume func(io.Reader) error) (err error, unreachable bool) {
	response, err := client.Get(url)
	if err != nil {
		return err, true
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// 先排空再关闭，让这条 keep-alive 连接能被下一次尝试复用。不读就走的话
		// Go 会直接丢弃连接，于是每次重试都要重做一次 TLS 握手——而我们要处理的
		// 失败恰恰经常就是 TLS 握手超时，那等于把最贵的部分重做一遍。
		// 只读前 4KB：错误响应体没有价值，但要足够让连接回到池子里。
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		_ = response.Body.Close()
		return &httpStatusError{Code: response.StatusCode, URL: url}, false
	}
	if err := consume(response.Body); err != nil {
		_ = response.Body.Close()
		return err, false
	}
	if err := response.Body.Close(); err != nil {
		return err, false
	}
	return nil, false
}

// Download 把 url 取回并写入 path；client 为 nil 时用 http.DefaultClient。
//
// 瞬时网络失败会重试（见 fetchWithRetry）。每次尝试都用 O_TRUNC 重开文件，因此重试
// 不会把上一次的半截内容接在后面。
func Download(client *http.Client, url, path string) error {
	return fetchWithRetry(client, url, func(body io.Reader) error {
		file, err := os.Create(path)
		if err != nil {
			// 本地写不进去（磁盘满、目录不可写）：重试无用，直接上报。
			return &permanentError{err: err}
		}
		if _, err := io.Copy(file, body); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			// 刻意**不**标记为永久失败：十几兆的产物下到一半被断流是最典型的瞬时
			// 故障，正是重试要救的情形。（真·磁盘写满时多试几次只是浪费几秒。）
			return err
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return &permanentError{err: err}
		}
		return nil
	})
}

// FetchChecksums 取回并解析给定版本的校验和文件。
//
// 网络失败同样重试；但「文件里没有这个产物」是确定性结论，不重试（由 Apply 折成
// 拒绝安装）。
func FetchChecksums(client *http.Client, version, asset string) (string, error) {
	url := ChecksumsURL(version)
	var body []byte
	if err := fetchWithRetry(client, url, func(reader io.Reader) error {
		text, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		body = text
		return nil
	}); err != nil {
		return "", err
	}
	digest, ok := ParseChecksums(string(body), asset)
	if !ok {
		return "", fmt.Errorf("checksums.txt 里没有 %s", asset)
	}
	return digest, nil
}

// StaleSuffix 是替换时旧二进制被挪去的后缀。
//
// 旧文件在**旧进程退出前无法删除**（Windows 不允许删除正在运行的映像），只能改名。
// 因此替换后必然短暂留下一个 <exe>.old，由收尾的助手进程在旧进程退出后清掉。
// 这就是「不留备份」的落地方式：该文件只是替换过程的中间态，不是可回退的备份。
const StaleSuffix = ".old"

// StalePath 返回 target 对应的旧文件暂存路径。
func StalePath(target string) string { return target + StaleSuffix }

// exeName 是暂存文件的名字。
//
// **必须**以 .exe 结尾（Windows）：否则连 `amkr-new --version` 都跑不起来，
// 报「无法在管道中间运行文档」。这一点是实测出来的，不要改成 .new。
func exeName() string {
	if runtime.GOOS == "windows" {
		return "amkr-new.exe"
	}
	return "amkr-new"
}

// Stage 把已下载的二进制复制到目标目录的暂存位置，返回暂存路径。
//
// 必须先落到目标目录再改名：下载用的临时目录可能与安装目录不同卷，跨卷 rename
// 会失败（且退化成整文件复制）。
func Stage(downloaded, targetDir string) (string, error) {
	staged := filepath.Join(targetDir, exeName())
	source, err := os.Open(downloaded)
	if err != nil {
		return "", err
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		_ = os.Remove(staged)
		return "", err
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(staged)
		return "", err
	}
	return staged, nil
}

// Replace 就地替换 target，返回被挪开的旧文件路径（供旧进程退出后删除）。
//
// 顺序不可交换：先把旧的改名让位，再把新的放到原位。反过来会因目标已存在而失败。
// 第二步失败时会把旧文件改回来，避免把用户留在「没有可执行文件」的状态。
func Replace(target, staged string) (string, error) {
	stale := StalePath(target)
	// 上一次更新可能留下未清理的 .old（例如助手被连坐杀掉）。尽力清掉；清不掉不阻断，
	// 下面的 rename 会覆盖它。
	_ = os.Remove(stale)
	if err := os.Rename(target, stale); err != nil {
		return "", fmt.Errorf("挪开旧版本失败: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Rename(stale, target)
		return "", fmt.Errorf("放入新版本失败: %w", err)
	}
	return stale, nil
}
