package amkr

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/webui"
)

// relativeAssetPattern 只认入口 HTML 里的相对资源引用：
// src="./…" 与 href="./…"。新版 Vite 产物、旧版原生入口都遵守这条约定。
var relativeAssetPattern = regexp.MustCompile(`(?:src|href)="\./([^"]+)"`)

// TestEmbeddedEntriesAndAssetsAreServed 锁定四个公开入口及它们引用的相对资源都可取。
//
// 为什么按 HTML 里的引用逐个取，而不是写死 main.js / styles.css：新版是 Vite 构建，
// 文件名带内容哈希，且以后还可能增加字体、图标等资源；写死清单会在产物换代时误报，
// 也会漏掉入口新增的依赖。这里只依赖「HTML 里的 ./ 相对引用」这条稳定契约，因此对
// 当前旧版 webui/ 与新构建产物同样成立。
func TestEmbeddedEntriesAndAssetsAreServed(t *testing.T) {
	handler, mounted := webui.Handler(WebUIAssets, true)
	if !mounted {
		t.Fatal("/ui 未挂载（index.html 不在嵌入资产里？）")
	}

	for _, entry := range []string{"index.html", "login.html", "panel.html", "guest.html"} {
		t.Run(entry, func(t *testing.T) {
			page := serveAsset(t, handler, "/"+entry)
			if page.Code != http.StatusOK {
				t.Fatalf("入口 %s 应 200，实际 %d（body=%q）", entry, page.Code, page.Body.String())
			}

			assets := relativeAssets(page.Body.String())
			if len(assets) == 0 {
				t.Fatalf("入口 %s 没有解析到 ./ 相对资源，检查 HTML 引用格式", entry)
			}
			for _, asset := range assets {
				asset = trimAssetQuery(asset)
				if asset == "" {
					continue
				}
				response := serveAsset(t, handler, "/"+asset)
				if response.Code != http.StatusOK {
					t.Errorf("入口 %s 引用的资源 %s 应 200，实际 %d", entry, asset, response.Code)
				}
			}
		})
	}
}

// TestEmbeddedIndexIsAvailable 锁定可用性判据仍然成立。
func TestEmbeddedIndexIsAvailable(t *testing.T) {
	if !webui.Available(WebUIAssets) {
		t.Fatal("嵌入资产应可用（index.html 存在）")
	}
}

// relativeAssets 返回 HTML 中所有 ./ 相对引用的原始路径（可能带 ?query）。
func relativeAssets(html string) []string {
	matches := relativeAssetPattern.FindAllStringSubmatch(html, -1)
	assets := make([]string, 0, len(matches))
	for _, match := range matches {
		assets = append(assets, match[1])
	}
	return assets
}

// trimAssetQuery 只取实际文件名；入口引用若带版本 query，静态处理器不应收到它。
func trimAssetQuery(asset string) string {
	if index := strings.IndexAny(asset, "?#"); index >= 0 {
		return asset[:index]
	}
	return asset
}

// serveAsset 直接调用处理器（调用方已用 StripPrefix 去掉挂载前缀，见 routes.go）。
//
// http.ServeFileFS 会把 /index.html 规范化重定向到 ./（浏览器随后取到首页），
// 所以测试跟随同源相对 Location，再断言最终响应是 200。
func serveAsset(t *testing.T, handler http.Handler, urlPath string) *httptest.ResponseRecorder {
	t.Helper()
	for attempt := 0; attempt < 4; attempt++ {
		request := httptest.NewRequest(http.MethodGet, urlPath, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		switch recorder.Code {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
			http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := recorder.Header().Get("Location")
			if location == "" {
				t.Fatalf("重定向响应 %d 缺少 Location", recorder.Code)
			}
			base, err := url.Parse("http://amkr" + urlPath)
			if err != nil {
				t.Fatalf("解析请求路径失败: %v", err)
			}
			ref, err := url.Parse(location)
			if err != nil {
				t.Fatalf("解析重定向地址失败: %v", err)
			}
			urlPath = base.ResolveReference(ref).Path
			continue
		default:
			return recorder
		}
	}
	t.Fatalf("资源 %s 重定向次数过多", urlPath)
	return nil
}
