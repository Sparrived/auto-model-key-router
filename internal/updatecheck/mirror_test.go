package updatecheck

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖「GitHub 直连不可达时怎么还能取到东西」这一层：候选地址的生成规则、
// 环境变量的三态语义、以及真正取回时的回退顺序。

// TestGitHubCandidatesPutsDirectFirst 锁定候选顺序：直连在前，镜像按序在后。
//
// 顺序是**行为**而不是实现细节：直连能通时永远不该走第三方中转，否则等于白白把一个
// 二进制交给别人中转。
func TestGitHubCandidatesPutsDirectFirst(t *testing.T) {
	t.Setenv(GitHubMirrorEnv, "https://mirror.one/,https://mirror.two/")
	url := "https://github.com/Sparrived/auto-model-key-router/releases/download/v6.1.0/checksums.txt"

	got := GitHubCandidates(url)
	want := []string{
		url,
		"https://mirror.one/" + url,
		"https://mirror.two/" + url,
	}
	if len(got) != len(want) {
		t.Fatalf("候选数量 = %d，期望 %d（%v）", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("候选[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}
}

// TestGitHubCandidatesCoversAPIHost 锁定 api.github.com 也会被展开。
//
// 版本检查走的就是这个域名：只展开 github.com 的话，api 被墙的用户会在「查不到新版本」
// 这一步就停下，根本走不到下载。
func TestGitHubCandidatesCoversAPIHost(t *testing.T) {
	t.Setenv(GitHubMirrorEnv, "https://mirror.one/")
	got := GitHubCandidates(GitHubLatestReleaseAPI)
	if len(got) != 2 || got[1] != "https://mirror.one/"+GitHubLatestReleaseAPI {
		t.Fatalf("api.github.com 应被展开为镜像地址，实际 %v", got)
	}
}

// TestGitHubCandidatesLeavesForeignURLAlone 锁定「非 GitHub 地址不套前缀」。
//
// 套了只会造出一个必然失败的 URL，还会让错误信息变得莫名其妙。
func TestGitHubCandidatesLeavesForeignURLAlone(t *testing.T) {
	t.Setenv(GitHubMirrorEnv, "https://mirror.one/")
	url := "https://pypi.org/pypi/auto-model-key-router/json"
	if got := GitHubCandidates(url); len(got) != 1 || got[0] != url {
		t.Fatalf("非 GitHub 地址不应有镜像候选，实际 %v", got)
	}
}

// TestGitHubMirrorEnvThreeStates 锁定环境变量的三态语义。
//
// 「没设置」与「设成空」必须是两件事：前者要内置兜底（普通用户的默认体验），后者是
// 显式关闭（企业内网只允许白名单出口时，多试几个外网地址就是浪费时间）。
func TestGitHubMirrorEnvThreeStates(t *testing.T) {
	t.Run("未设置时用内置列表", func(t *testing.T) {
		// 显式清掉，避免开发机上恰好设置过而让断言随环境漂移。
		original, existed := os.LookupEnv(GitHubMirrorEnv)
		if err := os.Unsetenv(GitHubMirrorEnv); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if existed {
				_ = os.Setenv(GitHubMirrorEnv, original)
			}
		}()
		if got := GitHubMirrorPrefixes(); len(got) != len(DefaultGitHubMirrorPrefixes) {
			t.Errorf("未设置时应返回内置列表，实际 %v", got)
		}
	})

	t.Run("设为空串则关闭镜像", func(t *testing.T) {
		t.Setenv(GitHubMirrorEnv, "")
		if got := GitHubMirrorPrefixes(); len(got) != 0 {
			t.Errorf("空值应当关闭镜像回退，实际 %v", got)
		}
		if got := GitHubCandidates(GitHubLatestReleaseAPI); len(got) != 1 {
			t.Errorf("关闭镜像后应只剩直连，实际 %v", got)
		}
	})

	t.Run("设了值则替换内置列表并补尾斜杠", func(t *testing.T) {
		// 用户写 "https://my.mirror" 这种没有尾斜杠的形式很常见，直接拼接会得到
		// `https://my.mirrorhttps://github.com/...`，因此必须补上。
		t.Setenv(GitHubMirrorEnv, " https://my.mirror , ")
		got := GitHubMirrorPrefixes()
		if len(got) != 1 || got[0] != "https://my.mirror/" {
			t.Fatalf("应替换为单个补全尾斜杠的前缀，实际 %v", got)
		}
	})
}

// withCandidates 把候选地址换成注入列表，返回恢复函数（配合 defer 使用）。
func withCandidates(candidates []string) func() {
	original := candidatesFor
	candidatesFor = func(string) []string { return candidates }
	return func() { candidatesFor = original }
}

// TestHTTPFetcherFallsBackToMirror 锁定版本检查的回退：直连不可达时用镜像拿到 JSON。
//
// 用「已经关闭的假服务」充当直连：它的端口没人监听，连接会被立刻拒绝——这正是被阻断的
// GitHub 在用户机器上的表现（拨号失败）。
func TestHTTPFetcherFallsBackToMirror(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	var mirrorHits int
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits++
		_, _ = w.Write([]byte(`{"tag_name":"v6.1.0","html_url":"https://example.invalid"}`))
	}))
	defer mirror.Close()

	defer withCandidates([]string{deadURL, mirror.URL})()

	got := CheckLatestVersion(HTTPFetcher(mirror.Client()), "6.0.0", 2*time.Second)
	if got.Error != nil {
		t.Fatalf("直连失败时应回退到镜像，实际报错 %s", *got.Error)
	}
	if got.LatestVersion == nil || *got.LatestVersion != "6.1.0" {
		t.Fatalf("应解析出镜像返回的版本，实际 %v", renderPtr(got.LatestVersion))
	}
	if mirrorHits != 1 {
		t.Errorf("镜像应被请求一次，实际 %d 次", mirrorHits)
	}
}

// TestHTTPFetcherPrefersDirect 锁定「直连可用时不碰镜像」。
func TestHTTPFetcherPrefersDirect(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v6.1.0"}`))
	}))
	defer direct.Close()

	var mirrorHits int
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits++
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.1"}`))
	}))
	defer mirror.Close()

	defer withCandidates([]string{direct.URL, mirror.URL})()

	got := CheckLatestVersion(HTTPFetcher(direct.Client()), "6.0.0", 2*time.Second)
	if got.Error != nil {
		t.Fatalf("直连可用时不该报错，实际 %s", *got.Error)
	}
	if got.LatestVersion == nil || *got.LatestVersion != "6.1.0" {
		t.Fatalf("应取直连的结果，实际 %v", renderPtr(got.LatestVersion))
	}
	if mirrorHits != 0 {
		t.Errorf("直连可用时不该请求镜像，实际 %d 次", mirrorHits)
	}
}

// TestHTTPFetcherReportsDirectErrorWithHint 锁定「全部失败时报直连的错误 + 排查提示」。
//
// 报镜像的错误（比如 403/502）会把用户引到错误的方向：他要修的是自己这边的网络。
func TestHTTPFetcherReportsDirectErrorWithHint(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	defer withCandidates([]string{deadURL, "http://127.0.0.1:1/mirror"})()

	got := CheckLatestVersion(HTTPFetcher(nil), "6.0.0", 2*time.Second)
	if got.Error == nil {
		t.Fatal("全部候选失败时必须报错")
	}
	if !strings.Contains(*got.Error, GitHubMirrorEnv) {
		t.Errorf("错误应提示可用 %s 指定镜像，实际 %q", GitHubMirrorEnv, *got.Error)
	}
	if !strings.Contains(*got.Error, "镜像加速地址均失败") {
		t.Errorf("错误应说明镜像也失败了，实际 %q", *got.Error)
	}
}
