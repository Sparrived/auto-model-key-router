package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/pricing"
)

// endpointCatalog 是一份足以让 /ui/pricing.json 有内容的目录。
const endpointCatalog = `{
  "p": {"models": {
    "gpt-4o": {"cost": {"input": 2.5, "output": 10, "cache_read": 1.25}},
    "cheap": {"cost": {"input": 0, "output": 0}}
  }}
}`

// stubPricingFetch 返回一个按脚本作答的取回接缝，并记录调用次数。
func stubPricingFetch(t *testing.T, bodies ...string) (pricing.Fetcher, *int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	return func(url, etag string, timeout time.Duration) (pricing.FetchResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		body := bodies[0]
		if calls-1 < len(bodies) {
			body = bodies[calls-1]
		}
		return pricing.FetchResult{Body: []byte(body), ETag: `W/"stub"`}, nil
	}, &calls
}

// newPricingApp 装配一个注入了价格目录的 App（WebUI 资产用桩，使 /ui 真的挂载）。
func newPricingApp(t *testing.T, fetch pricing.Fetcher) *App {
	t.Helper()
	return newTestAppWith(t, t.TempDir(), appFixture{
		webUIEnabled: true,
		mutate: func(options *Options) {
			// /ui 只有在 index.html 存在时才挂载（webui.Available）。
			options.WebUIAssets = fs.FS(fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")},
			})
			options.PricingFetch = fetch
		},
	})
}

// waitForPricing 等目录预热完成（Start 在后台 goroutine 里取回）。
func waitForPricing(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := app.pricing.Payload(); ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("价格目录预热超时")
}

// TestPricingEndpointServesCatalog 断言 GET /ui/pricing.json 真的从挂载下发出目录。
//
// 这条同时钉住路由的**精确性**：/ui/ 下本来只有一个静态文件处理器，若价格路由没有
// 比它更精确，请求会去磁盘上找一个并不存在的 pricing.json 而得到 404。
func TestPricingEndpointServesCatalog(t *testing.T) {
	fetch, _ := stubPricingFetch(t, endpointCatalog)
	app := newPricingApp(t, fetch)
	waitForPricing(t, app)

	recorder := serve(app, http.MethodGet, "/ui/pricing.json", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /ui/pricing.json 状态码 = %d，期望 200（body=%s）",
			recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", got)
	}
	body := recorder.Body.String()
	// 内容不鉴权：价格目录是 models.dev 的公开数据（上面 serve 没带任何凭据）。
	for _, want := range []string{`"version":1`, `"source":"https://models.dev/api.json"`, `"gpt-4o"`, `"input":2.5`} {
		if !strings.Contains(body, want) {
			t.Errorf("响应体缺少 %s: %s", want, body)
		}
	}
	// 缺失的 cache_write 不能补 0（前端据此回退到输入价）。
	if strings.Contains(body, "cache_write") {
		t.Errorf("缺失的缓存价不应出现在响应里: %s", body)
	}
}

// TestPricingEndpointUnavailableBeforeFetch 断言目录不可用时回 503 而不是空目录。
//
// 这是"绝不静默算成 $0"的接口面：给一个 200 + 空 models 会让前端把每个模型都当免费。
func TestPricingEndpointUnavailableBeforeFetch(t *testing.T) {
	failing := func(url, etag string, timeout time.Duration) (pricing.FetchResult, error) {
		return pricing.FetchResult{}, http.ErrHandlerTimeout
	}
	app := newPricingApp(t, failing)

	recorder := serve(app, http.MethodGet, "/ui/pricing.json", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("目录不可用时状态码 = %d，期望 503（body=%s）",
			recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"models"`) {
		t.Errorf("不可用时不应给出任何目录内容: %s", recorder.Body.String())
	}
}

// TestPricingEndpointETagRevalidation 断言 ETag 能走 304，且目录变化后 ETag 跟着变。
//
// 目录有 230 KB，页面每十秒轮询一次；没有 304 就是每秒几十 KB 的无谓流量。
func TestPricingEndpointETagRevalidation(t *testing.T) {
	fetch, _ := stubPricingFetch(t, endpointCatalog)
	app := newPricingApp(t, fetch)
	waitForPricing(t, app)

	first := serve(app, http.MethodGet, "/ui/pricing.json", "")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("响应缺少 ETag")
	}
	if got := first.Header().Get("Cache-Control"); got != pricingCacheControl {
		t.Errorf("Cache-Control = %q，期望 %q", got, pricingCacheControl)
	}

	// 带上 ETag：必须是 304 且没有响应体。
	request := httptest.NewRequest(http.MethodGet, "/ui/pricing.json", nil)
	request.Header.Set("If-None-Match", etag)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotModified {
		t.Errorf("带 If-None-Match 的状态码 = %d，期望 304", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("304 不应带响应体，实得 %d 字节", recorder.Body.Len())
	}

	// 不匹配的 ETag 仍然回完整内容。
	request = httptest.NewRequest(http.MethodGet, "/ui/pricing.json", nil)
	request.Header.Set("If-None-Match", `"stale"`)
	recorder = httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("不匹配的 ETag 状态码 = %d，期望 200", recorder.Code)
	}
}

// TestPricingEndpointRejectsNonGet 断言只有 GET/HEAD 被接受。
func TestPricingEndpointRejectsNonGet(t *testing.T) {
	fetch, _ := stubPricingFetch(t, endpointCatalog)
	app := newPricingApp(t, fetch)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/pricing.json", "")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /ui/pricing.json 状态码 = %d，期望 405", method, recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s 的 Allow = %q，期望 GET, HEAD", method, got)
		}
	}
	// HEAD 与 GET 同形（net/http 丢弃响应体，但头必须一致）。
	head := serve(app, http.MethodHead, "/ui/pricing.json", "")
	if head.Code != http.StatusOK && head.Code != http.StatusServiceUnavailable {
		t.Errorf("HEAD /ui/pricing.json 状态码 = %d，期望 200 或 503", head.Code)
	}
}

// TestPricingEndpointAbsentWithoutWebUI 断言 WebUI 未挂载时价格路由也不存在。
//
// 它挂在 /ui/ 之下，因此与静态资源同生共死：这是刻意的取舍（见 pricing.go），
// 这条测试把这个取舍钉住，避免有人日后误以为它是一条独立路由。
func TestPricingEndpointAbsentWithoutWebUI(t *testing.T) {
	app := newTestApp(t, t.TempDir(), func(options *Options) {
		// 刻意既不给资产也不给 PricingFetch：webui_enabled 在夹具里是 false。
	})
	recorder := serve(app, http.MethodGet, "/ui/pricing.json", "")
	if recorder.Code != http.StatusNotFound {
		t.Errorf("未挂载 WebUI 时状态码 = %d，期望 404", recorder.Code)
	}
	if recorder.Body.String() != notFoundBody {
		t.Errorf("响应体 = %s，期望兜底 404 %s", recorder.Body.String(), notFoundBody)
	}
}

// TestPricingEndpointFollowsMountPrefix 断言挂了嵌入前缀时价格目录也跟着走。
//
// 路由用的是 webui.Path(MountPrefix) 拼出来的前缀（嵌入宿主时是 /amkr/ui），若这里写死
// "/ui/" 就会只在独立运行时可用、嵌入后 404——而 WebUI 自己的请求是按 location.pathname
// 反推前缀的（web/src/lib/api.ts 的 apiBase），两边必须一致。
func TestPricingEndpointFollowsMountPrefix(t *testing.T) {
	fetch, _ := stubPricingFetch(t, endpointCatalog)
	app := newTestAppWith(t, t.TempDir(), appFixture{
		webUIEnabled: true,
		mutate: func(options *Options) {
			options.MountPrefix = "/amkr"
			options.WebUIAssets = fs.FS(fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")},
			})
			options.PricingFetch = fetch
		},
	})
	waitForPricing(t, app)

	// 带前缀时可用。
	recorder := serve(app, http.MethodGet, "/amkr/ui/pricing.json", "")
	if recorder.Code != http.StatusOK {
		t.Errorf("GET /amkr/ui/pricing.json 状态码 = %d，期望 200（body=%s）",
			recorder.Code, recorder.Body.String())
	}
	// 不带前缀时**不应**可达：前缀是挂载契约，绕过它就等于映射到了别的位置。
	plain := serve(app, http.MethodGet, "/ui/pricing.json", "")
	if plain.Code != http.StatusNotFound {
		t.Errorf("无前缀的 /ui/pricing.json 状态码 = %d，期望 404", plain.Code)
	}
}

// TestPricingCatalogStartsEvenWhenDisabled 断言缺省的 App 不会因为 PricingFetch 为 nil
// 而出网，也不会在 Close 时卡住。
//
// 这是装配面的安全网：几十处测试都走 newTestApp，任何一处让 nil 退化成真实 HTTP
// 实现，整套测试就会开始打 models.dev。
func TestPricingCatalogStartsEvenWhenDisabled(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	// 没有取回实现时目录必然不可用（预热立即失败）。
	if _, ok := app.pricing.Payload(); ok {
		t.Fatal("没有取回实现时不应该有目录")
	}
	// Close 由 t.Cleanup 调用；若 pricingStop 没接上，这里会卡住或泄漏 goroutine。
	if app.pricingStop == nil {
		t.Fatal("pricingStop 未接线")
	}
}
