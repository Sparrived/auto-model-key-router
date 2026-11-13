package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/logfiles"
)

// messageRecorder 是只收集消息文本与级别的 slog.Handler。
//
// 不用 slog.NewTextHandler：它会把消息里的双引号转义成 \"，断言访问日志的引号形态
// 会变得又脆又难读。这里直接拿 record.Message 原文与 record.Level。
type messageRecorder struct {
	mu       sync.Mutex
	messages []string
	// levels 与 messages 一一对应：访问日志按状态码分级（见 accessLevel），用例既要
	// 断言文本，也要断言这行落在哪个级别。
	levels []slog.Level
}

func (r *messageRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *messageRecorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *messageRecorder) WithGroup(string) slog.Handler            { return r }

func (r *messageRecorder) Handle(_ context.Context, record slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, record.Message)
	r.levels = append(r.levels, record.Level)
	return nil
}

func (r *messageRecorder) collected() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.messages...)
}

func (r *messageRecorder) collectedLevels() []slog.Level {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]slog.Level(nil), r.levels...)
}

// newAccessLogApp 装配一个注入访问日志收集器的 App。
func newAccessLogApp(t *testing.T, recorder *messageRecorder) *App {
	t.Helper()
	return newTestApp(t, t.TempDir(), func(options *Options) {
		options.AccessLogger = slog.New(recorder)
	})
}

// TestAccessLogRecordsRequestLine 钉住访问日志的形态与记录范围。参照实现里
// uvicorn 在响应开始时记一行（httptools_impl.py:483-491），404/405 也照样记。
func TestAccessLogRecordsRequestLine(t *testing.T) {
	cases := []struct {
		name       string
		target     string
		authorize  bool
		wantStatus int
		wantLine   string
	}{
		{"命中路由", "/health", false, http.StatusOK,
			`127.0.0.1:50874 - "GET /health HTTP/1.1" 200`},
		{"带查询串", "/metrics?hours=1", true, http.StatusOK,
			`127.0.0.1:50874 - "GET /metrics?hours=1 HTTP/1.1" 200`},
		{"未鉴权 401", "/metrics", false, http.StatusUnauthorized,
			`127.0.0.1:50874 - "GET /metrics HTTP/1.1" 401`},
		{"兜底 404", "/does-not-exist", false, http.StatusNotFound,
			`127.0.0.1:50874 - "GET /does-not-exist HTTP/1.1" 404`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := &messageRecorder{}
			app := newAccessLogApp(t, recorder)

			request := httptest.NewRequest(http.MethodGet, testCase.target, nil)
			request.RemoteAddr = "127.0.0.1:50874"
			if testCase.authorize {
				// 夹具里的 local_api_key 是 "local-key"（见 ops_test.go 的 fixtureTemplate）。
				request.Header.Set("Authorization", "Bearer local-key")
			}
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			if response.Code != testCase.wantStatus {
				t.Fatalf("状态码 = %d, 期望 %d", response.Code, testCase.wantStatus)
			}
			messages := recorder.collected()
			if len(messages) != 1 {
				t.Fatalf("期望 1 行访问日志，得到 %d 行: %q", len(messages), messages)
			}
			if messages[0] != testCase.wantLine {
				t.Errorf("访问日志不符\n期望: %s\n实际: %s", testCase.wantLine, messages[0])
			}
		})
	}
}

// TestAccessLogLevelByStatus 钉住访问日志的**状态码分级**（见 accessLevel）：
//
//	< 400（2xx / 3xx）→ Debug：轮询噪声，默认 info 级别下不落盘
//	400–499          → Info ：调用方配错，默认级别下必须看得见
//	>= 500           → Warn ：服务端故障（落盘时由 logfiles 的 pythonLevelName
//	                          渲染成 WARNING，与 Python 的 levelname 一致）
//
// 走真实 handler 断言，而不是只测 accessLevel：分级若没在 onStart 里接上，纯函数
// 测试是发现不了的。表里同时覆盖两条记日志的路径——处理器显式写头，以及一个字都不
// 写、由结尾兜底的 report(http.StatusOK) 补记的 200。
func TestAccessLogLevelByStatus(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		writeHeader bool // false 表示处理器什么都不写，走兜底的 report(200)
		wantLevel   slog.Level
	}{
		{"200 显式写头", http.StatusOK, true, slog.LevelDebug},
		{"200 兜底", http.StatusOK, false, slog.LevelDebug},
		{"301 重定向", http.StatusMovedPermanently, true, slog.LevelDebug},
		{"400 参数错误", http.StatusBadRequest, true, slog.LevelInfo},
		{"404 路由不存在", http.StatusNotFound, true, slog.LevelInfo},
		{"499 客户端断开", 499, true, slog.LevelInfo},
		{"500 服务端错误", http.StatusInternalServerError, true, slog.LevelWarn},
		{"503 无可用 Key", http.StatusServiceUnavailable, true, slog.LevelWarn},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := &messageRecorder{}
			app := newAccessLogApp(t, recorder)
			status := testCase.status
			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if testCase.writeHeader {
					w.WriteHeader(status)
				}
			})

			response := httptest.NewRecorder()
			app.accessLog(inner).ServeHTTP(response,
				httptest.NewRequest(http.MethodGet, "/metrics", nil))

			if response.Code != testCase.status {
				t.Fatalf("状态码 = %d, 期望 %d", response.Code, testCase.status)
			}
			levels := recorder.collectedLevels()
			if len(levels) != 1 {
				t.Fatalf("期望 1 行访问日志，得到 %d 行: levels=%v messages=%q",
					len(levels), levels, recorder.collected())
			}
			if levels[0] != testCase.wantLevel {
				t.Errorf("状态码 %d 的访问日志级别 = %v, 期望 %v",
					testCase.status, levels[0], testCase.wantLevel)
			}
		})
	}
}

// TestAccessLogSkipsWebSocketUpgrade 钉住升级请求不记 uvicorn.access 行：
// uvicorn 的访问日志只写在 http.response.start 上，升级走 websocket 协议实现，
// 用的是 uvicorn.error（实测历史日志里 uvicorn.access 带 WebSocket 的行数为 0）。
func TestAccessLogSkipsWebSocketUpgrade(t *testing.T) {
	recorder := &messageRecorder{}
	app := newAccessLogApp(t, recorder)

	request := httptest.NewRequest(http.MethodGet, "/ws/events", nil)
	request.RemoteAddr = "127.0.0.1:50874"
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if messages := recorder.collected(); len(messages) != 0 {
		t.Fatalf("升级请求不该产生 uvicorn.access 行，得到: %q", messages)
	}
}

// TestAccessLogDisabledByDefault 钉住 AccessLogger 为 nil 时**完全不包装**：
// 嵌入方与既有测试不受影响（类型断言行为一字不变）。
func TestAccessLogDisabledByDefault(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	inner := app.buildHandler()
	if wrapped := app.accessLog(inner); !sameHandler(wrapped, inner) {
		t.Error("AccessLogger 为 nil 时不该包装 handler")
	}
}

// TestAccessRecorderPreservesFlushAndUnwrap 钉住包装层的两个关键能力：Flush
// （流式响应靠它把字推给客户端）与 Unwrap（coder/websocket 靠 http.Hijacker 或
// Unwrap 拿到底层 ResponseWriter 去 Hijack，少了它升级会 501）。
func TestAccessRecorderPreservesFlushAndUnwrap(t *testing.T) {
	base := httptest.NewRecorder()
	var writer http.ResponseWriter = &accessRecorder{ResponseWriter: base}

	flusher, ok := writer.(http.Flusher)
	if !ok {
		t.Fatal("accessRecorder 必须实现 http.Flusher")
	}
	flusher.Flush()
	if !base.Flushed {
		t.Error("Flush 未透传到底层 ResponseWriter")
	}

	unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
	if !ok {
		t.Fatal("accessRecorder 必须实现 Unwrap() http.ResponseWriter")
	}
	if unwrapper.Unwrap() != http.ResponseWriter(base) {
		t.Error("Unwrap 未返回底层 ResponseWriter")
	}
}

// TestAccessRecorderReportsOnce 钉住上报语义：首次确定的状态码为准并只上报一次，
// 隐式 200（只 Write 不 WriteHeader）也要上报。
func TestAccessRecorderReportsOnce(t *testing.T) {
	var reported []int
	explicit := &accessRecorder{
		ResponseWriter: httptest.NewRecorder(),
		onStart:        func(status int) { reported = append(reported, status) },
	}
	explicit.WriteHeader(http.StatusTeapot)
	explicit.WriteHeader(http.StatusInternalServerError)
	if len(reported) != 1 || reported[0] != http.StatusTeapot {
		t.Errorf("上报 = %v, 期望恰好 [%d]（首次写出的状态码）", reported, http.StatusTeapot)
	}

	var implicitReported []int
	implicit := &accessRecorder{
		ResponseWriter: httptest.NewRecorder(),
		onStart:        func(status int) { implicitReported = append(implicitReported, status) },
	}
	if _, err := implicit.Write([]byte("ok")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if len(implicitReported) != 1 || implicitReported[0] != http.StatusOK {
		t.Errorf("隐式 200 上报 = %v, 期望 [200]", implicitReported)
	}
}

// TestAccessLogRecordedAtResponseStart 钉住记录**时机**：uvicorn 在
// http.response.start 就写访问日志，而不是等响应结束。对流式响应这不是细节——一条
// 跑几分钟的 SSE 请求，参照实现里访问日志在开头就出现了。
func TestAccessLogRecordedAtResponseStart(t *testing.T) {
	recorder := &messageRecorder{}
	app := newAccessLogApp(t, recorder)

	release := make(chan struct{})
	started := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-release
	})
	handler := app.accessLog(inner)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))
	}()

	<-started
	// 处理器还没返回，但访问日志应当已经写下。
	if messages := recorder.collected(); len(messages) != 1 {
		t.Errorf("响应开始时就该记一行，实际 %q", messages)
	}
	close(release)
	<-done
	if messages := recorder.collected(); len(messages) != 1 {
		t.Errorf("不该重复记录，实际 %q", messages)
	}
}

// TestAccessLineShape 直接钉住拼接细节，尤其是双引号是**原样插入**而不是转义
// （Python 只是把值插进双引号之间）。
func TestAccessLineShape(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?x=1&y=2", nil)
	request.Proto = "HTTP/1.1"
	request.RemoteAddr = "[::1]:5555"
	got := accessLine(request, http.StatusServiceUnavailable)
	want := `[::1]:5555 - "POST /v1/chat/completions?x=1&y=2 HTTP/1.1" 503`
	if got != want {
		t.Errorf("accessLine 不符\n期望: %s\n实际: %s", want, got)
	}
	if strings.Contains(got, `\"`) {
		t.Errorf("双引号不该被转义: %s", got)
	}
}

// sameHandler 判断两个 http.Handler 是否指向同一个值（函数值不可比较，用底层指针）。
func sameHandler(left, right http.Handler) bool {
	return reflect.ValueOf(left).Pointer() == reflect.ValueOf(right).Pointer()
}

// TestLogFileFeedsOpsLogsEndpoint 是本修复的验收测试：把 internal/logfiles 的 sink
// 接到 App 上、制造几个请求，再从 /api/logs 读回来，断言**内容非空**。
//
// 它精确复现用户报的现象——「WebUI 的服务日志不显示」。根因是迁移时漏了
// uvicorn_log_config 的 file handler：log_file_path 没有任何写入方，于是
// /api/logs 恒返回 text=""，前端渲染成「日志为空。」。因此这里断言的不是「日志里有
// 某一行」，而是「这个文件确实被写入了」——后者才是回归真正会打破的性质。
//
// 访问日志按状态码分级后（见 accessLevel），/health 的 200 落到 DEBUG，被 sink 的
// 默认 info 级别滤掉；能落盘的是 404 那行 INFO。注意「面板不空」如今主要由
// internal/proxy 的上游调用日志（Info）保证，本测试守的是「访问日志仍有一条落盘
// 路径、/api/logs 仍交得出内容」。
func TestLogFileFeedsOpsLogsEndpoint(t *testing.T) {
	dir := t.TempDir()
	configPath := writeFixture(t, dir, true, false)
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}

	sink, err := logfiles.Open(loaded.LogFilePath)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	defer func() { _ = sink.Close() }()

	app, err := New(Options{
		ConfigPath:   configPath,
		Config:       loaded,
		Version:      testVersion,
		Logger:       sink.App,
		AccessLogger: sink.Access,
	})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()

	// 制造流量：一条成功（200 → DEBUG，默认 info 级别下被滤掉）、一条 404
	// （INFO，落盘），再读日志本身。
	for _, target := range []string{"/health", "/does-not-exist"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.RemoteAddr = "127.0.0.1:50874"
		app.Handler().ServeHTTP(httptest.NewRecorder(), request)
	}
	// 再造一条 503：真实路由要触发 5xx 得先有上游，这里直接把一个写 503 的处理器
	// 挂到同一套访问日志包装（AccessLogger 是同一个 sink）上，验证 Warn 档确实以
	// WARNING 落盘。
	boom := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	boomRequest := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	boomRequest.RemoteAddr = "127.0.0.1:50874"
	app.accessLog(boom).ServeHTTP(httptest.NewRecorder(), boomRequest)

	// 直接读文件：这就是 /api/logs 的数据来源（api.handleOpsLogs 读同一路径）。
	data, err := os.ReadFile(loaded.LogFilePath)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	text := string(data)
	if strings.TrimSpace(text) == "" {
		t.Fatal("日志文件为空：这正是用户报的『服务日志不显示』")
	}
	// 404 是 Info：默认级别下必须落盘。
	if want := `INFO uvicorn.access 127.0.0.1:50874 - "GET /does-not-exist HTTP/1.1" 404`; !strings.Contains(text, want) {
		t.Errorf("日志缺少 %q\n实际内容:\n%s", want, text)
	}
	// 503 是 Warn：落盘时按 Python 的 levelname 渲染成 WARNING（slog 自己没有
	// LevelWarning，其 WARN 档的文本名是 WARN，见 logfiles 的 pythonLevelName）。
	if want := `WARNING uvicorn.access 127.0.0.1:50874 - "GET /v1/chat/completions HTTP/1.1" 503`; !strings.Contains(text, want) {
		t.Errorf("日志缺少 %q\n实际内容:\n%s", want, text)
	}
	// 200 是 Debug：不该再以 INFO 出现（被 sink 滤掉，或将来调成 debug 后写成
	// DEBUG，两者都算符合分级）。
	if unwanted := `INFO uvicorn.access 127.0.0.1:50874 - "GET /health HTTP/1.1" 200`; strings.Contains(text, unwanted) {
		t.Errorf("200 的访问日志应降到 DEBUG，不该以 INFO 落盘（%q）:\n%s", unwanted, text)
	}

	// 再从 /api/logs 取一次，确认运维接口真的把内容交出来（端到端闭环）。
	request := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	request.Header.Set("Authorization", "Bearer local-key")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("/api/logs 状态码 = %d", response.Code)
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析 /api/logs 响应失败: %v（原体 %s）", err, response.Body.String())
	}
	if strings.TrimSpace(body.Text) == "" {
		t.Error("/api/logs 的 text 为空，WebUI 会显示「日志为空。」")
	}
	if !strings.Contains(body.Text, `"GET /does-not-exist HTTP/1.1" 404`) {
		t.Errorf("/api/logs 的 text 不含刚发生的访问日志:\n%s", body.Text)
	}
}

// TestAccessLogWrapperKeepsWebSocketUpgradeWorking 是最关键的一条回归：启用访问
// 日志之后，真实 socket 上的 WebSocket 握手必须照常成功。
//
// 它守的其实是 accessLog 里的**升级短路**：升级请求被 isWebSocketUpgrade 拦下、
// 原样交给内层 handler，因此 ResponseWriter 没有被包装。httptest.ResponseRecorder
// 测不出这件事（它本来就不支持 Hijack），必须走真 socket。
func TestAccessLogWrapperKeepsWebSocketUpgradeWorking(t *testing.T) {
	recorder := &messageRecorder{}
	dir := t.TempDir()
	path := writeFixture(t, dir, true, false)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{
		ConfigPath:   path,
		Config:       loaded,
		Version:      testVersion,
		AccessLogger: slog.New(recorder),
	})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()

	server := httptest.NewServer(app.Handler())
	defer server.Close()

	// 这条辅助函数在握手失败时会 Fatal，因此能连上就说明 Hijack 没被包装层挡住。
	authenticateWebSocket(t, server, "local-key")

	// 升级请求本身不记 uvicorn.access（见 accessLog 的说明），因此这里应当是空的；
	// 这条断言同时排除了「包装层把升级请求也当普通请求记了一行」。
	if messages := recorder.collected(); len(messages) != 0 {
		t.Errorf("升级请求不该产生 uvicorn.access 行: %q", messages)
	}
}

// TestAccessLogWrapperKeepsStreamingFlush 在真实 socket 上验证 Flush 仍然透传：
// 流式响应的字节必须在处理器返回之前就到达客户端。
//
// 用 /metrics 之外的最小可控路径不好构造，因此这里直接对包装层做一次真实 HTTP
// 往返：处理器写一段、Flush、再阻塞，客户端应当能立刻读到第一段。
func TestAccessLogWrapperKeepsStreamingFlush(t *testing.T) {
	recorder := &messageRecorder{}
	app := newAccessLogApp(t, recorder)

	release := make(chan struct{})
	streamed := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		close(streamed)
		<-release
		_, _ = w.Write([]byte("second"))
	})

	server := httptest.NewServer(app.accessLog(inner))
	defer server.Close()

	// 断言失败时也必须放行处理器：它阻塞在 <-release，而 server.Close() 会等它结束。
	// 少了这一层，任何一条 t.Fatal 都会变成「测试卡满 -timeout 后 panic」——CI 上确实
	// 这样红过一次（10 分钟超时，栈顶就是下面那条断言）。
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHandler()

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	buffer := make([]byte, len("first"))
	if _, err := io.ReadFull(response.Body, buffer); err != nil {
		t.Fatalf("读取第一段失败（Flush 没透传？）: %v", err)
	}
	if string(buffer) != "first" {
		t.Fatalf("第一段 = %q, 期望 %q", buffer, "first")
	}
	// 这里必须**等待**处理器走到 close(streamed)，不能非阻塞探测：客户端读到 "first"
	// 只证明 Flush 已经发生，而 close(streamed) 紧随其后、中间没有阻塞点——但客户端
	// 协程可能先被调度，于是非阻塞探测会把「客户端先跑」误判成「处理器没写第一段」。
	// 等待是有界的（正常是微秒级），超时仍然算失败。
	select {
	case <-streamed:
	case <-time.After(5 * time.Second):
		t.Fatal("处理器写了第一段，但 5 秒内没有走到 Flush 之后")
	}
	releaseHandler()
	rest, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取剩余内容失败: %v", err)
	}
	if string(rest) != "second" {
		t.Errorf("第二段 = %q, 期望 %q", rest, "second")
	}
}
