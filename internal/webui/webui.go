// Package webui 提供可选 WebUI 的静态资源服务。
//
// 移植 auto_model_key_router/webui.py（79 行）中与 HTTP 服务相关的部分。该文件的
// webui_status（给 /health 用的状态摘要）已由 internal/health 承担，这里只管
// 「是否需要挂载」与「怎么把文件发出去」。
//
// 三条与参照实现一致的语义：
//
//  1. **是否可用**由磁盘上是否存在 index.html 决定（webui.py:25-27），
//     **是否启用**由配置项 webui_enabled 决定，两者独立。
//  2. 未启用或不可用时**不注册任何路由**，因此对外完全不暴露 /ui（返回 404），
//     也就不会出现 SPA 首页被代理接口误伤的问题（webui.py:37-39）。
//  3. 挂载发生在进程启动阶段；运行中改配置需要重启才会生效。
package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// MountPath 是 WebUI 的固定挂载前缀（webui.py:19）。
//
// 它必须排在代理通配路由 `/v1/{path:path}` 之前，否则会被 /v1 吞掉——参照实现里
// 这是一条注释约定；Go 的 ServeMux 按模式特异性排序，`/ui/` 与 `/v1/` 互不影响，
// 但调用方在自建多路复用时仍应让 /ui 先注册。
const MountPath = "/ui"

// IndexFile 是「可用性」的判据，也是目录请求的默认文档。
const IndexFile = "index.html"

// probesDir 是只供开发/CI 使用的探针目录（webui/probes/*.mjs），**不对外服务**。
//
// v7 起 webui/ 是 web/ 的 Vite 构建产物（每次构建清空重建），测试改在 web/ 下用
// Vitest 跑，产物里已经没有 probes/。这道闸仍然保留：资产的发布物是整个 webui/
// 目录，//go:embed webui 又无法按子目录排除（embed 只支持「全部」或「跳过点/下划线
// 开头的文件」），哪天有人把开发期脚本放回 webui/，它会被编进二进制并随 /ui/ 一起
// 公开可取。
//
// 这是纯粹的信息暴露：探针里逐条写着 WebUI 的鉴权流程、面板 key 的存放与不存放规则
// 以及各页面的内部结构（静态资产本身已公开，探针额外给出的是「我们怎么测它」）。对
// 匿名访问者没有用途，对攻击者却是现成的实现说明。按目录整体挡掉没有功能代价，故在
// 这里明确排除，而不是指望 embed 模式能表达排除。
const probesDir = "probes"

// Available 报告静态资产是否随当前安装一起发布。
//
// 对应 webui.py:25 的 webui_available()：只看 index.html 是否存在。
func Available(assets fs.FS) bool {
	if assets == nil {
		return false
	}
	info, err := fs.Stat(assets, IndexFile)
	return err == nil && !info.IsDir()
}

// Path 返回 WebUI 的实际访问路径，含嵌入时的挂载前缀，无尾斜杠。
//
// 对应 webui.py:67-70：前缀先 rstrip("/") 再拼 /ui，因此 "" 得 "/ui"、
// "/amkr" 与 "/amkr/" 都得 "/amkr/ui"。
func Path(mountPrefix string) string {
	return strings.TrimRight(mountPrefix, "/") + MountPath
}

// Handler 按需返回静态资源处理器；未启用或资产缺失时返回 nil, false。
//
// 调用方拿到 handler 后应把它挂到 Path 返回的路径下（通常再加一个尾斜杠模式）。
// assets 必须是**以 WebUI 资产目录为根**的文件系统（用 fs.Sub 或 embed 的子目录取得）。
//
// 与 Starlette 的 StaticFiles(directory=..., html=True) 对齐，但**刻意不做目录列表**：
// Starlette 在 html=True 时遇到目录只找 index.html，找不到就 404；而 Go 的
// http.FileServer 会把目录内容列出来。资源目录里只有前端文件，列出不算漏洞，但那是
// 与参照实现不同的对外行为，因此这里自己实现，不给目录列表留口子。
func Handler(assets fs.FS, enabled bool) (http.Handler, bool) {
	if !enabled || !Available(assets) {
		return nil, false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, assets)
	}), true
}

// serve 发送一个静态文件；目录请求落到该目录下的 index.html。
func serve(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// 静态资源只读：Starlette 的 StaticFiles 同样不接受写方法。
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "方法不被允许", http.StatusMethodNotAllowed)
		return
	}

	name := resolveName(r.URL.Path)
	if name == "" {
		// 路径逃出资产根（含 ..）——按不存在处理，不泄漏任何信息。
		http.NotFound(w, r)
		return
	}
	info, err := fs.Stat(assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		// 目录：只认 index.html，不列目录。
		candidate := path.Join(name, IndexFile)
		candidateInfo, candidateErr := fs.Stat(assets, candidate)
		if candidateErr != nil || candidateInfo.IsDir() {
			http.NotFound(w, r)
			return
		}
		name = candidate
	}
	// http.ServeFileFS 会处理 Range/If-Modified-Since 等条件请求与 Content-Type。
	http.ServeFileFS(w, r, assets, name)
}

// resolveName 把请求路径转成资产根内的相对名；非法、越界或落在不对外服务的目录里时
// 返回空串（调用方据此返回 404，与「文件不存在」不可区分）。
func resolveName(urlPath string) string {
	trimmed := strings.TrimPrefix(urlPath, "/")
	// path.Clean 会折叠 . 与 ..；若结果以 .. 开头说明试图越界。
	cleaned := path.Clean("/" + trimmed)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		// 根路径（"" 或 "."）映射到 index.html。
		if trimmed == "" || trimmed == "." {
			return IndexFile
		}
		return ""
	}
	// 开发/CI 探针目录不对外服务（理由见 probesDir）。判**首段**而不是前缀字符串：
	// "probes" 与 "probesomething" 是两回事，前者是探针目录，后者该正常发送。
	// 放在 Clean 之后，因此 "/js/../probes/x.mjs" 这类绕路同样被折叠到这里挡住。
	if cleaned == probesDir || strings.HasPrefix(cleaned, probesDir+"/") {
		return ""
	}
	return cleaned
}
