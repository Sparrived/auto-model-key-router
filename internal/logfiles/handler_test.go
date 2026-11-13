package logfiles

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOpenWritesPythonLogFormat 钉住落盘行格式：参照实现的 formatter 是
// `%(asctime)s %(levelname)s %(name)s %(message)s`，属性按 json.dumps 追加。
//
// 期望值取自历史日志的真实行（proxy_handler.py 的 "upstream stream error"），
// 逐字对齐（除时间戳）。
func TestOpenWritesPythonLogFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "server.log")
	sink, err := Open(path)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}

	// 时间戳由 handler 从 record.Time 取，这里无法注入时钟，因此按前缀断言。
	sink.App.Warn("upstream stream error",
		"model_id", "deepseek-v4.1-flash",
		"status_code", 200,
		"error_type", "ReadError",
		"chunks", 154,
	)
	sink.Access.Info(`127.0.0.1:50874 - "GET /metrics?hours=1 HTTP/1.1" 200`)
	if err := sink.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 行，得到 %d 行: %q", len(lines), lines)
	}

	// 第 1 行：应用日志。前缀是 `YYYY-MM-DD HH:MM:SS,mmm WARNING auto_model_key_router.app `。
	if !pythonTimestamp.MatchString(lines[0]) {
		t.Errorf("应用日志时间戳格式不符: %q", lines[0])
	}
	const appPrefix = " WARNING auto_model_key_router.app upstream stream error "
	if !strings.Contains(lines[0], appPrefix) {
		t.Errorf("应用日志前缀不符，期望包含 %q，得到 %q", appPrefix, lines[0])
	}
	// 属性是 json.dumps(ensure_ascii=False) 形态：数值不加引号，分隔符是 ", " 与 ": "。
	const attrs = `{"model_id": "deepseek-v4.1-flash", "status_code": 200, ` +
		`"error_type": "ReadError", "chunks": 154}`
	if !strings.HasSuffix(lines[0], attrs) {
		t.Errorf("应用日志属性不符\n期望后缀: %s\n实际整行: %s", attrs, lines[0])
	}

	// 第 2 行：访问日志，logger 名是 uvicorn.access，消息原样保留双引号。
	expected := ` INFO uvicorn.access 127.0.0.1:50874 - "GET /metrics?hours=1 HTTP/1.1" 200`
	if !strings.HasSuffix(lines[1], expected) {
		t.Errorf("访问日志不符\n期望后缀: %s\n实际整行: %s", expected, lines[1])
	}
}

// TestOpenAppendsAndLevelFilters 钉住两件容易改坏的事：以追加方式打开（前台启动前
// 已归档过旧日志，截断会抹掉归档后的新内容），以及默认级别 Info 的下界。级别本身由
// Sink 的 LevelVar 决定，相关用例见下面的 TestSink*。
func TestOpenAppendsAndLevelFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	if err := os.WriteFile(path, []byte("既有内容\n"), 0o644); err != nil {
		t.Fatalf("预写日志失败: %v", err)
	}

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	sink.App.Debug("这条不该出现")
	sink.App.Info("这条该出现")
	if err := sink.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "既有内容\n") {
		t.Errorf("旧内容被截断: %q", text)
	}
	if strings.Contains(text, "这条不该出现") {
		t.Errorf("DEBUG 不该落盘: %q", text)
	}
	if !strings.Contains(text, "INFO auto_model_key_router.app 这条该出现") {
		t.Errorf("INFO 未落盘: %q", text)
	}
}

// TestSinkDefaultLevelKeepsInfoDropsDebug 钉住默认级别是 Info：Open 之后什么都不设，
// Debug 也不落盘、Info 落盘（对应 uvicorn_log_config 给三个 logger 设的 level="INFO"）。
func TestSinkDefaultLevelKeepsInfoDropsDebug(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	sink, err := Open(path)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	if got := sink.Level(); got != slog.LevelInfo {
		t.Errorf("新建 Sink 的 Level() = %v, 期望默认 %v", got, slog.LevelInfo)
	}
	sink.App.Debug("默认级别下不该出现的调试行")
	sink.App.Info("默认级别下该出现的信息行")
	if err := sink.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	lines := readLogLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("期望只落 1 行（Debug 被丢弃），得到 %d 行: %q", len(lines), lines)
	}
	const want = " INFO auto_model_key_router.app 默认级别下该出现的信息行"
	if !strings.Contains(lines[0], want) {
		t.Errorf("Info 行不符，期望包含 %q，得到 %q", want, lines[0])
	}
}

// TestSinkSetLevelDebugWritesDebugLines 钉住 SetLevel(LevelDebug) 之后 Debug 落盘，
// 且行里的级别名是 Python 的 DEBUG。
func TestSinkSetLevelDebugWritesDebugLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	sink, err := Open(path)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	sink.SetLevel(slog.LevelDebug)
	if got := sink.Level(); got != slog.LevelDebug {
		t.Errorf("SetLevel 之后 Level() = %v, 期望 %v", got, slog.LevelDebug)
	}
	// 三路各写一行：任何一路没接上共享的 LevelVar，这里就会少行。
	sink.App.Debug("应用侧调试行")
	sink.Access.Debug(`127.0.0.1:50874 - "GET /ui/ HTTP/1.1" 200`)
	sink.Server.Debug("服务器调试行")
	if err := sink.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	lines := readLogLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("期望 3 行，得到 %d 行: %q", len(lines), lines)
	}
	wants := []string{
		" DEBUG auto_model_key_router.app 应用侧调试行",
		` DEBUG uvicorn.access 127.0.0.1:50874 - "GET /ui/ HTTP/1.1" 200`,
		" DEBUG uvicorn.error 服务器调试行",
	}
	for index, want := range wants {
		if !strings.Contains(lines[index], want) {
			t.Errorf("第 %d 行不符，期望包含 %q，得到 %q", index+1, want, lines[index])
		}
	}
}

// TestSinkSetLevelErrorDropsInfo 钉住反向调整同样生效：级别抬到 Error 之后 Info 与
// Warn 都不落盘，只有 Error 落盘。
func TestSinkSetLevelErrorDropsInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	sink, err := Open(path)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	sink.SetLevel(slog.LevelError)
	sink.App.Info("抬到 Error 之后不该出现的 Info 行")
	sink.App.Warn("抬到 Error 之后不该出现的 Warn 行")
	sink.App.Error("抬到 Error 之后该出现的 Error 行")
	if err := sink.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	lines := readLogLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("期望只落 1 行，得到 %d 行: %q", len(lines), lines)
	}
	const want = " ERROR auto_model_key_router.app 抬到 Error 之后该出现的 Error 行"
	if !strings.Contains(lines[0], want) {
		t.Errorf("Error 行不符，期望包含 %q，得到 %q", want, lines[0])
	}
}

// TestSinkLevelAppliesToWithAttrsHandler 防止把 LevelVar 拷成值：WithAttrs 是结构体
// 拷贝，若级别是 slog.Level（值），派生 handler 会把级别冻结在派生那一刻。因此这里
// 刻意**先派生、再改级别**——只有共享同一个 *slog.LevelVar 的实现才通得过。
func TestSinkLevelAppliesToWithAttrsHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	sink, err := Open(path)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	derived := sink.App.With("component", "proxy")
	// 派生发生在 SetLevel 之前：级别若是值拷贝，下面的 Debug 会一直停在 Info 上而丢失。
	sink.SetLevel(slog.LevelDebug)
	derived.Debug("派生 handler 的调试行")
	// 再抬回 Error：值拷贝实现会漏掉这次调整，Info 会意外落盘。
	sink.SetLevel(slog.LevelError)
	derived.Info("抬回 Error 之后不该出现的 Info 行")
	if err := sink.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	lines := readLogLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("期望只落 1 行，得到 %d 行: %q", len(lines), lines)
	}
	const want = " DEBUG auto_model_key_router.app 派生 handler 的调试行 "
	if !strings.Contains(lines[0], want) {
		t.Errorf("派生 handler 的 Debug 行不符，期望包含 %q，得到 %q", want, lines[0])
	}
	// 派生时累积的属性照旧渲染在行尾。
	if !strings.HasSuffix(lines[0], `{"component": "proxy"}`) {
		t.Errorf("派生属性不符，期望行尾是 JSON 对象，得到 %q", lines[0])
	}
}

// TestSinkLevelVarDrivesHandlers 钉住热重载路径：internal/server 的 reload 直接写
// LevelVar（不经过 Sink），因此它必须就是三个 handler 读的那个变量。
func TestSinkLevelVarDrivesHandlers(t *testing.T) {
	sink, err := Open(filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	defer func() { _ = sink.Close() }()

	levelVar := sink.LevelVar()
	if levelVar == nil {
		t.Fatal("LevelVar() 返回 nil")
	}
	levelVar.Set(slog.LevelDebug)
	if !sink.App.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("写 LevelVar 后 Debug 应启用")
	}
	levelVar.Set(slog.LevelError)
	loggers := map[string]*slog.Logger{"App": sink.App, "Access": sink.Access, "Server": sink.Server}
	for name, logger := range loggers {
		if logger.Enabled(context.Background(), slog.LevelInfo) {
			t.Errorf("写 LevelVar 后 %s 的 Info 应被过滤", name)
		}
	}
}

// TestNilSinkLevelMethods 钉住 nil 接收者安全：cmd/amkr 在日志文件打不开时会把 sink
// 置为 nil（日志写不进去不该拦住服务），接线路径上因此不能 panic（与 Close 一致）。
func TestNilSinkLevelMethods(t *testing.T) {
	var sink *Sink
	sink.SetLevel(slog.LevelDebug) // 不 panic 即可：nil Sink 没有级别可改
	if got := sink.Level(); got != slog.LevelInfo {
		t.Errorf("nil Sink 的 Level() = %v, 期望默认 %v", got, slog.LevelInfo)
	}
	if levelVar := sink.LevelVar(); levelVar != nil {
		t.Errorf("nil Sink 的 LevelVar() = %v, 期望 nil", levelVar)
	}

	// 未经 Open 的零值 Sink 同理：按默认 Info 处理，而不是 panic。
	empty := &Sink{}
	empty.SetLevel(slog.LevelError)
	if got := empty.Level(); got != slog.LevelInfo {
		t.Errorf("零值 Sink 的 Level() = %v, 期望默认 %v", got, slog.LevelInfo)
	}
}

// TestPythonLevelName 复刻 Python 的 levelname（WARN 在 Python 里叫 WARNING）。
func TestPythonLevelName(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARNING"},
		{slog.LevelError, "ERROR"},
	}
	for _, testCase := range cases {
		if got := pythonLevelName(testCase.level); got != testCase.want {
			t.Errorf("pythonLevelName(%v) = %q, 期望 %q", testCase.level, got, testCase.want)
		}
	}
}

// pythonTimestamp 匹配 Python logging 的 asctime：`%Y-%m-%d %H:%M:%S,mmm`。
var pythonTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3} `)

// readLogLines 读取落盘日志并按行切分；空文件返回 nil。
//
// 必须在关闭 Sink 之后再调用：Open 是追加写，关掉句柄才不会漏掉最后几行。
func readLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestOpenCreatesParentDirectories(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "b", "server.log")
	sink, err := Open(path)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	defer func() { _ = sink.Close() }()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("日志文件未创建: %v", err)
	}
}
