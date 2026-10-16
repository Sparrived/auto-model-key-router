package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/eventbus"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
)

// 本文件覆盖两个 WebSocket 路由的**装配**（HTTP 层由 routes_test.go 覆盖，纯决策由
// internal/eventbus 与 internal/wsproxy 自己的用例覆盖）：
//
//	1. /ws/events 的握手帧序、4001/4003、dirty 驱动的 metrics_snapshot、config_change；
//	2. /v1/{path} 升级到 internal/wsproxy 并真的打到上游。
//
// 全部用真实 socket（httptest.NewServer + coder/websocket 客户端），因为
// httptest.ResponseRecorder 不支持 Hijack，任何握手断言在它上面都没有意义。

// wsTimeout 是读一帧的上限。它比节流窗口（1 秒）宽得多，因此不会掩盖「广播没发生」，
// 只会把失败暴露成超时。
const wsTimeout = 5 * time.Second

// dialWebSocket 建立一条到测试服务器的 WebSocket 连接。
func dialWebSocket(t *testing.T, server *httptest.Server, path string, header http.Header) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wsTimeout)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, webSocketURL(server.URL)+path,
		&websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		status := "无响应"
		if response != nil {
			status = response.Status
		}
		t.Fatalf("连接 %s 失败（HTTP %s）: %v", path, status, err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// readWebSocketFrame 读一帧文本，返回帧内容；超时或连接关闭时返回 error。
func readWebSocketFrame(t *testing.T, conn *websocket.Conn) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wsTimeout)
	defer cancel()
	messageType, data, err := conn.Read(ctx)
	if err != nil {
		return "", err
	}
	if messageType != websocket.MessageText {
		t.Fatalf("期望文本帧，收到 messageType=%v", messageType)
	}
	return string(data), nil
}

// writeWebSocketFrame 发一帧文本。
func writeWebSocketFrame(t *testing.T, conn *websocket.Conn, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wsTimeout)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(text)); err != nil {
		t.Fatalf("发送帧失败: %v", err)
	}
}

// frameEventType 取出帧里的 "type" 字段（这里只比较类型与少数帧文本，不做整帧比对）。
func frameEventType(t *testing.T, frame string) string {
	t.Helper()
	value, err := canonical.ParseString(frame)
	if err != nil {
		t.Fatalf("帧不是合法 JSON: %q (%v)", frame, err)
	}
	return value.Lookup("type").StringValue()
}

// readUntilConnected 依次读出帧的类型，直到 connected 为止（与参照实现的观测方式
// 一致：服务端认证成功后不会主动关闭，读下去会挂住）。
func readUntilConnected(t *testing.T, conn *websocket.Conn) []string {
	t.Helper()
	var types []string
	for {
		frame, err := readWebSocketFrame(t, conn)
		if err != nil {
			t.Fatalf("等 connected 时出错: %v（已收到 %v）", err, types)
		}
		eventType := frameEventType(t, frame)
		types = append(types, eventType)
		if eventType == eventbus.EventConnected {
			return types
		}
	}
}

// authenticateWebSocket 连接 /ws/events 并用给定 token 完成握手，返回连接与帧序。
func authenticateWebSocket(t *testing.T, server *httptest.Server, token string) (*websocket.Conn, []string) {
	t.Helper()
	conn := dialWebSocket(t, server, "/ws/events", nil)
	writeWebSocketFrame(t, conn, fmt.Sprintf(`{"type": "auth", "token": %q}`, token))
	return conn, readUntilConnected(t, conn)
}

// closeInfo 把读错误折算成关闭码与原因；不是关闭帧时返回 (-1, "")。
func closeInfo(err error) (int, string) {
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return int(closeErr.Code), closeErr.Reason
	}
	return -1, ""
}

// TestWSEventsHandshakeSequence 钉住首帧顺序（app.py:129-135 + app.py:342）。
//
// 参照实现里客户端看到的顺序是 client_count、metrics_snapshot、connected——前两帧
// 来自 authenticate() 内部 await 的计数回调，第三帧才是路由补发的。internal/eventbus
// 的 e2e.jsonl（真实 TestClient 观测）已经钉过一遍，这里钉的是**装配层**（回调有没有
// 接上、connected 有没有补发）。
func TestWSEventsHandshakeSequence(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn, types := authenticateWebSocket(t, server, "local-key")
	want := []string{
		eventbus.EventClientCount,
		eventbus.EventMetricsSnapshot,
		eventbus.EventConnected,
	}
	if len(types) != len(want) {
		t.Fatalf("帧序 = %v，期望 %v", types, want)
	}
	for index := range want {
		if types[index] != want[index] {
			t.Fatalf("帧序 = %v，期望 %v", types, want)
		}
	}
	_ = conn
}

// TestWSEventsConnectedFrameSeparators 钉住 connected 帧的**分隔符**。
//
// 参照实现里有两种 JSON 写法，极易混淆：event_bus.py:71 的 broadcast 走 json.dumps
// 默认分隔符（", " / ": "），而 app.py:342 的 send_json 走 Starlette 的 JSONResponse
// （紧凑）。逐字节差别由 eventbus.ConnectedFrame 负责，这里确认装配层用的就是它。
func TestWSEventsConnectedFrameSeparators(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn := dialWebSocket(t, server, "/ws/events", nil)
	writeWebSocketFrame(t, conn, `{"type": "auth", "token": "local-key"}`)
	var connected string
	for {
		frame, err := readWebSocketFrame(t, conn)
		if err != nil {
			t.Fatalf("等 connected 时出错: %v", err)
		}
		if frameEventType(t, frame) == eventbus.EventConnected {
			connected = frame
			break
		}
		// 反证：同一批广播帧必须是**带空格**的那种写法，与 connected 不同形。
		if !strings.Contains(frame, `"type": "`) {
			t.Errorf("广播帧用了紧凑分隔符（应带空格）: %s", frame)
		}
	}
	if connected != eventbus.ConnectedFrame() {
		t.Errorf("connected 帧 = %q，期望 %q", connected, eventbus.ConnectedFrame())
	}
	if connected != `{"type":"connected","data":{}}` {
		t.Errorf("connected 帧不再是紧凑写法: %q", connected)
	}
}

// TestWSEventsAuthFailures 覆盖首帧失败的两条关闭路径（app.py:339 + event_bus.py:40/:50）。
func TestWSEventsAuthFailures(t *testing.T) {
	cases := []struct {
		name   string
		frame  string
		code   int
		reason string
	}{
		{"坏 JSON 走 4001", `{oops`, eventbus.CloseCodeAuthTimeoutOrInvalidMessage,
			eventbus.AuthCloseReasonTimeoutOrInvalidMessage},
		{"空帧走 4001", ``, eventbus.CloseCodeAuthTimeoutOrInvalidMessage,
			eventbus.AuthCloseReasonTimeoutOrInvalidMessage},
		{"token 不对走 4003", `{"type": "auth", "token": "nope"}`, eventbus.CloseCodeAuthFailed,
			eventbus.AuthCloseReasonFailed},
		// 受限凭据能过 HTTP 的 /v1/models，但事件流只对**完整权限**开放
		// （app.py:337 的 auth.is_full）——这条断言是那个收窄的唯一落点。
		// 用已取消的访客 key 字面量：它现在就是一把不存在的凭据，因此必然被拒。
		{"受限凭据走 4003", `{"type": "auth", "token": "amkr-visitor"}`, eventbus.CloseCodeAuthFailed,
			eventbus.AuthCloseReasonFailed},
		{"type 不是 auth 走 4003", `{"type": "hello", "token": "local-key"}`,
			eventbus.CloseCodeAuthFailed, eventbus.AuthCloseReasonFailed},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app := newTestApp(t, t.TempDir(), nil)
			server := httptest.NewServer(app.Handler())
			defer server.Close()

			conn := dialWebSocket(t, server, "/ws/events", nil)
			writeWebSocketFrame(t, conn, testCase.frame)
			_, err := readWebSocketFrame(t, conn)
			if err == nil {
				t.Fatal("鉴权失败时不该有帧到达")
			}
			code, reason := closeInfo(err)
			if code != testCase.code {
				t.Errorf("关闭码 = %d（%q），期望 %d", code, reason, testCase.code)
			}
			if reason != testCase.reason {
				t.Errorf("关闭原因 = %q，期望 %q", reason, testCase.reason)
			}
		})
	}
}

// TestWSEventsSnapshotAfterMetricsWrite 是 dirty 信号链路的端到端断言。
//
// 链路：metrics.Store.Record -> SetOnRecord 回调（app.py:126-127）-> capacity 1 的
// dirty 通道 -> runMetricsBroadcast 的 select -> Broadcaster.Tick -> 快照 -> 广播。
// 断开任何一环，这条断言都会以「等帧超时」失败。
func TestWSEventsSnapshotAfterMetricsWrite(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn, _ := authenticateWebSocket(t, server, "local-key")
	recordOneRequest(t, app)

	for {
		frame, err := readWebSocketFrame(t, conn)
		if err != nil {
			t.Fatalf("等 metrics_snapshot 时出错: %v", err)
		}
		value, parseErr := canonical.ParseString(frame)
		if parseErr != nil {
			t.Fatalf("帧不是合法 JSON: %q", frame)
		}
		if value.Lookup("type").StringValue() != eventbus.EventMetricsSnapshot {
			continue
		}
		// 快照里必须能看到刚刚写进去的那一行（total.requests == 1），否则「有帧」
		// 可能只是空闲心跳，证明不了 dirty 真的驱动了广播。
		requests, ok := value.Lookup("data").Lookup("total").Lookup("requests").AsInt()
		if !ok || requests != 1 {
			t.Errorf("快照 total.requests = %d（ok=%v），期望 1（dirty 信号应触发一次真实快照）",
				requests, ok)
		}
		return
	}
}

// TestNoMetricsSnapshotWithoutClients 断言「没有订阅者时不构建快照」在装配后依然成立。
//
// 为什么需要它：internal/eventbus 的用例已经用虚拟时钟钉住了**纯决策**
// （Loop.Advance 在 clientCount == 0 时不把时刻放进 fired），但那不能防止装配层接错
// ——例如把快照构建直接挂在 dirty 信号上。那时线上没有 WebUI 订阅者，服务每秒白跑一次
// SQLite 聚合查询。
//
// 判据是替换 Broadcaster 真正会调用的那个函数并计数。替换必须发生在**循环启动之前**，
// 否则与循环里的读构成数据竞争；因此这里先停掉 New 启的循环，替换后再启动一个新的
// （两个方法都是装配层自己的，属于同包测试可用的接缝）。
func TestNoMetricsSnapshotWithoutClients(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	app.stopMetricsBroadcast()

	var builds atomic.Int64
	app.broadcaster.Snapshot = func(context.Context) (*canonical.Value, error) {
		builds.Add(1)
		return canonical.NewObject(), nil
	}
	app.startMetricsBroadcast()

	// 阶段一：没有任何订阅者。写一行指标会真的唤醒循环（dirty 是真实信号），但
	// clientCount == 0 ⇒ Tick 一次都不构建快照。等足一轮 sleep(1.0) 再判定。
	recordOneRequest(t, app)
	time.Sleep(1500 * time.Millisecond)
	if got := builds.Load(); got != 0 {
		t.Fatalf("没有订阅者时构建了 %d 次快照，期望 0", got)
	}

	// 阶段二（正向对照）：接一个订阅者，握手本身就会广播一次快照
	// （app.py:131-132 的 count > 0 分支）。这一阶段同时证明「循环在跑、替换确实
	// 生效」，否则阶段一的 0 也可能只是循环根本没启动。
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	authenticateWebSocket(t, server, "local-key")
	if got := builds.Load(); got == 0 {
		t.Fatal("有订阅者时必须构建快照（否则阶段一的 0 说明不了问题）")
	}
}

// TestConfigChangeBroadcastOnReload 断言配置热重载会广播 config_change（app.py:473-475）。
func TestConfigChangeBroadcastOnReload(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, true, false)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn, _ := authenticateWebSocket(t, server, "local-key")

	// 外部改写配置（mtime 必须真的变化），再发一条请求触发热重载。
	rewriteConfig(t, path, emptyModelsConfig(dir))
	serve(app, http.MethodGet, "/health", "")

	frame, err := readWebSocketFrame(t, conn)
	if err != nil {
		t.Fatalf("等 config_change 时出错: %v", err)
	}
	if got := frameEventType(t, frame); got != eventbus.EventConfigChange {
		t.Fatalf("事件类型 = %s，期望 %s", got, eventbus.EventConfigChange)
	}
	// 逐字节对齐 app.py:475 的 {"reloaded": True}（broadcast 用默认分隔符）。
	if !strings.Contains(frame, `"reloaded": true`) {
		t.Errorf("config_change 帧形状不对: %s", frame)
	}
}

// TestMetricsBroadcastSurvivesStoreSwap 断言换库之后 dirty 信号仍然有效。
//
// app.py:468 的 `metrics.on_record = old_runtime.metrics.on_record` 是把回调带到新库
// 上的那一行；漏掉它，metrics_db_path 一经热重载，metrics_snapshot 就只剩 30 秒心跳，
// 而这在任何 HTTP 层的断言里都看不出来。
func TestMetricsBroadcastSurvivesStoreSwap(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, true, false)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn, _ := authenticateWebSocket(t, server, "local-key")

	moved := strings.Replace(
		emptyModelsConfig(dir),
		jsonPath(filepath.Join(dir, "metrics.sqlite3")),
		jsonPath(filepath.Join(dir, "moved.sqlite3")),
		1,
	)
	rewriteConfig(t, path, moved)
	serve(app, http.MethodGet, "/health", "")

	// 换库后的当前代必须就是新库（同时排除了「重载没发生」这种假阴性）。
	store := app.currentMetricsAdapter().store
	if !strings.HasSuffix(store.Path(), "moved.sqlite3") {
		t.Fatalf("当前指标库 = %s，期望 moved.sqlite3", store.Path())
	}

	recordOneRequest(t, app)
	for {
		frame, err := readWebSocketFrame(t, conn)
		if err != nil {
			t.Fatalf("等换库后的 metrics_snapshot 时出错: %v", err)
		}
		if frameEventType(t, frame) == eventbus.EventMetricsSnapshot {
			return
		}
	}
}

// recordOneRequest 往当前代的指标库里写一行（走的是真实 Store.Record，因此会触发
// on_record 回调）。
func recordOneRequest(t *testing.T, app *App) {
	t.Helper()
	adapter := app.currentMetricsAdapter()
	if adapter == nil {
		t.Fatal("指标库未装配")
	}
	status := int64(200)
	if err := adapter.store.Record(metrics.RecordParams{
		ModelID:    "model-a",
		KeyName:    "key-a",
		StatusCode: &status,
	}); err != nil {
		t.Fatalf("写入指标失败: %v", err)
	}
}

// TestMetricsDirtyOnProxyRequestStart 断言请求**一进来**就置 dirty（app.py:355）。
//
// 这一条与 Record 的写后回调不同：写库发生在请求结束时，而 app.py:355 的
// `_metrics_dirty.set()` 在 acquire_active 之后立刻执行，好让订阅者马上看到
// active_requests 变了——长流式请求期间这是唯一的观测手段。判据是：上游被卡住
// （active 尚未释放）时，客户端必须收到一个 active_requests == 1 的快照。
func TestMetricsDirtyOnProxyRequestStart(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	// 释放桩上游要在 httptest 的 server.Close() 之前发生：Close 会等所有在途请求收尾，
	// 而这条请求正卡在桩上游上——不先放行就要等到 request_timeout（10 秒）。
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	// started 只在第一次上游命中时关闭：重试语义下（见 internal/proxy 的空响应拦截）
	// 桩上游理论上可能被访问多次，裸 close 会 panic。
	var startedOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		// 带 choices 的正常响应：空 choices 会被 proxy 按「空响应」故障重试。
		_, _ = io.WriteString(w,
			`{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	}))
	defer upstream.Close()
	defer releaseUpstream()

	dir := t.TempDir()
	path := writeProxyFixture(t, dir, upstream.URL)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn, _ := authenticateWebSocket(t, server, "local-key")

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"model-a","messages":[]}`))
	request.Header.Set("Authorization", fullAuthorization)
	go app.Handler().ServeHTTP(httptest.NewRecorder(), request)

	select {
	case <-started:
	case <-time.After(wsTimeout):
		t.Fatal("上游没有被访问到，测试前提不成立")
	}
	for {
		frame, readErr := readWebSocketFrame(t, conn)
		if readErr != nil {
			t.Fatalf("等 active_requests 快照时出错: %v", readErr)
		}
		value, parseErr := canonical.ParseString(frame)
		if parseErr != nil {
			t.Fatalf("帧不是合法 JSON: %q", frame)
		}
		if value.Lookup("type").StringValue() != eventbus.EventMetricsSnapshot {
			continue
		}
		active, ok := value.Lookup("data").Lookup("active_requests").AsInt()
		if !ok {
			t.Fatalf("快照缺少 active_requests: %s", frame)
		}
		if active == 1 {
			releaseUpstream()
			return
		}
		// 可能是请求开始之前构建的旧快照，继续等下一帧。
	}
}

// —— WS /v1/{path} ——

// upstreamCapture 记录桩上游收到的请求（handler 与断言分处两个 goroutine，必须加锁）。
type upstreamCapture struct {
	mu      sync.Mutex
	method  string
	path    string
	query   string
	header  http.Header
	body    string
	request int
	// response/contentType 是桩上游要回的内容；contentType 为空表示不回 content-type
	// （wsproxy 会因此发**二进制**帧，与参照实现一致）。
	response    string
	contentType string
	status      int
}

func (c *upstreamCapture) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.request++
	c.method = r.Method
	c.path = r.URL.Path
	c.query = r.URL.RawQuery
	c.header = r.Header.Clone()
	c.body = string(body)
	status := c.status
	response := c.response
	contentType := c.contentType
	c.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if response == "" {
		// 默认回一条带 choices 的正常 chat completion：空 choices 的 2xx 会被
		// internal/proxy 的空响应拦截按上游故障重试（见 empty_completion_test.go），
		// 与本组用例要断言的帧/路由行为无关。
		response = `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, response)
}

func (c *upstreamCapture) snapshot() upstreamCapture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return upstreamCapture{
		method: c.method, path: c.path, query: c.query,
		header: c.header, body: c.body, request: c.request,
	}
}

// writeProxyFixture 写一份把 prov-a 指向桩上游的配置。
func writeProxyFixture(t *testing.T, dir, upstreamURL string) string {
	t.Helper()
	text := fmt.Sprintf(fixtureTemplate,
		jsonPath(filepath.Join(dir, "endpoint-capabilities.json")),
		jsonPath(filepath.Join(dir, "metrics.sqlite3")),
		jsonPath(filepath.Join(dir, "server.log")),
		true, false,
	)
	text = strings.Replace(text, "https://a.example.test", upstreamURL, 1)
	path := filepath.Join(dir, "router-config.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	return path
}

// TestWebSocketProxyRoundTrip 是 WS /v1/{path} 的端到端断言：握手 -> 首帧 -> 真实
// 上游往返 -> 帧 + 关闭码。
//
// 它证明的接线有三处容易漏的地方：
//
//   - 升级请求被 handleProxyRoute 分流到 wsproxy（而不是当成 GET 走 HTTP proxy）；
//   - wsproxy 把主机的 proxy.Handle 注入了（ProxyHandler 的注入点不是 nil）；
//   - 传给 proxy 的 path 是去掉 "/v1/" 前缀后的值，且查询串被保留
//     （websocket_proxy.py:51-67 复制整个 scope，因此 ?trace=1 会带到上游）。
func TestWebSocketProxyRoundTrip(t *testing.T) {
	capture := &upstreamCapture{contentType: "application/json"}
	upstream := httptest.NewServer(http.HandlerFunc(capture.handler))
	defer upstream.Close()

	dir := t.TempDir()
	path := writeProxyFixture(t, dir, upstream.URL)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn := dialWebSocket(t, server, "/v1/chat/completions?trace=1",
		http.Header{"Authorization": []string{fullAuthorization}})
	writeWebSocketFrame(t, conn, `{"model":"model-a","messages":[]}`)

	frame, err := readWebSocketFrame(t, conn)
	if err != nil {
		t.Fatalf("读响应帧失败: %v", err)
	}
	parsed, parseErr := canonical.ParseString(frame)
	if parseErr != nil {
		t.Fatalf("响应帧不是合法 JSON: %q", frame)
	}
	if got := parsed.Lookup("id").StringValue(); got != "ok" {
		t.Errorf("响应帧 id = %q，期望 ok（帧=%s）", got, frame)
	}
	// 200 -> 1000（websocket_proxy.py:97-102 的第一段）。
	_, closeErr := readWebSocketFrame(t, conn)
	if closeErr == nil {
		t.Fatal("响应之后应当关闭连接")
	}
	if code, _ := closeInfo(closeErr); code != 1000 {
		t.Errorf("关闭码 = %d，期望 1000", code)
	}

	got := capture.snapshot()
	if got.request != 1 {
		t.Fatalf("上游请求数 = %d，期望 1", got.request)
	}
	if got.method != http.MethodPost {
		t.Errorf("上游方法 = %s，期望 POST", got.method)
	}
	if got.path != "/v1/chat/completions" {
		t.Errorf("上游路径 = %s，期望 /v1/chat/completions", got.path)
	}
	if got.query != "trace=1" {
		t.Errorf("上游查询串 = %q，期望 trace=1（websocket_proxy.py:51-67 保留原 URL）", got.query)
	}
	if auth := got.header.Get("Authorization"); auth != "Bearer sk-secret-a" {
		t.Errorf("上游 Authorization = %q，期望被换成 provider key", auth)
	}
	// 六个握手头必须被剔除（websocket_proxy.py:13-22）：把它们带给上游会撒谎。
	for _, name := range []string{"Upgrade", "Connection", "Sec-Websocket-Key",
		"Sec-Websocket-Version", "Sec-Websocket-Protocol", "Sec-Websocket-Extensions"} {
		if value := got.header.Get(name); value != "" {
			t.Errorf("上游不应收到握手头 %s=%q", name, value)
		}
	}
	if !strings.Contains(got.body, `"model":"model-a"`) {
		t.Errorf("上游请求体 = %s，期望保留 model-a", got.body)
	}
}

// TestWebSocketProxyAuthFailureCloseCode 断言鉴权失败的关闭码是 1008 而不是 1000。
//
// 这是状态码 -> 关闭码映射在装配路径上的落点：wsproxy 拿到的 401 来自真实的
// internal/proxy（而不是测试替身假 ProxyHandler）。
func TestWebSocketProxyAuthFailureCloseCode(t *testing.T) {
	capture := &upstreamCapture{contentType: "application/json"}
	upstream := httptest.NewServer(http.HandlerFunc(capture.handler))
	defer upstream.Close()

	dir := t.TempDir()
	path := writeProxyFixture(t, dir, upstream.URL)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() { _ = app.Close() }()
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn := dialWebSocket(t, server, "/v1/chat/completions", nil)
	writeWebSocketFrame(t, conn, `{"model":"model-a","messages":[]}`)

	frame, err := readWebSocketFrame(t, conn)
	if err != nil {
		t.Fatalf("读错误帧失败: %v", err)
	}
	if !strings.Contains(frame, authFailureMessage) {
		t.Errorf("错误帧 = %s，期望包含 %q", frame, authFailureMessage)
	}
	_, closeErr := readWebSocketFrame(t, conn)
	if closeErr == nil {
		t.Fatal("错误响应之后应当关闭连接")
	}
	if code, _ := closeInfo(closeErr); code != 1008 {
		t.Errorf("关闭码 = %d，期望 1008（4xx -> 1008）", code)
	}
	if got := capture.snapshot().request; got != 0 {
		t.Errorf("鉴权失败时不应访问上游，实际 %d 次", got)
	}
}
