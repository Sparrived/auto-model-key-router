package logfiles

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件是**参照实现的 uvicorn_log_config 的 Go 侧落点**：Python 用 logging 的
// dictConfig 把日志交给 uvicorn 的 FileHandler（service.py 的 uvicorn_log_config），
// Go 没有 logging，因此这里用 slog.Handler 直接实现同一个落点与同一套行格式。
//
// 行格式对齐参照实现的 formatter
// `%(asctime)s %(levelname)s %(name)s %(message)s`：
//
//	2026-09-18 11:32:41,533 WARNING auto_model_key_router.app upstream stream error {...}
//
// 与 Python 的四处已记录差异：
//
//   - **handler 本身不做 status_code 等级提升**。参照实现给 file handler 挂了
//     AccessLogLevelFilter，但它读 `record.status_code`，而 uvicorn 的访问日志用
//     `logger.info('%s - "%s %s HTTP/%s" %d', ...)` 的位置参数记录，
//     `getattr(record, "status_code", None)` 恒为 None，过滤器从不生效。历史日志
//     实测：1,618,926 行 uvicorn.access **全部 INFO**，WARNING/ERROR 的
//     uvicorn.access 为 0 行（503/404 也不例外），Python 侧因此事实上就是「一律
//     INFO」。Go 侧**不再**照抄这个结果：这个过滤器没做到的事改成由调用方在记录时
//     选级别来完成——访问日志现在由 internal/server/accesslog.go 按状态码分级
//     （<400 Debug、400-499 Info、>=500 Warn），因此在 log_level=debug 下能看到
//     2xx/3xx 行，默认级别下 5xx 落盘为 WARNING。handler 只认 record 的级别，
//     换算不在这里做。
//   - **属性渲染成 JSON 对象追加在消息之后**。参照实现是调用方自己
//     `json.dumps(payload, ensure_ascii=False)` 拼进消息；Go 侧由 handler 统一渲染，
//     因此行的可读性一致，但键顺序按调用方传参顺序，不保证与 Python 逐字相同。
//   - **WithGroup 被忽略**（与仓库内其它 handler 一致）；本仓库没有 WithGroup
//     调用点。
//   - **日志只落文件，不再同时写 stderr**。与参照实现相同：uvicorn 拿到
//     log_config 后其 logger 只剩 file handler，控制台不再出现这些行。

// pythonTimeLayout 是 Python logging 默认的 asctime：`%Y-%m-%d %H:%M:%S,mmm`。
const pythonTimeLayout = "2006-01-02 15:04:05,000"

// AppLoggerName 对应 `logging.getLogger("auto_model_key_router.app")`
// （proxy_handler.py 的 LOGGER），也是本服务全部应用侧日志的 logger 名。
const AppLoggerName = "auto_model_key_router.app"

// AccessLoggerName 对应 uvicorn 的访问日志 logger。
const AccessLoggerName = "uvicorn.access"

// ServerLoggerName 对应 uvicorn 的服务器日志 logger。参照实现里启动/关停与
// WebSocket 握手结果都记在这里（uvicorn 的 `self.logger`），实测历史日志形如
// `INFO uvicorn.error Started server process [4732]`、`INFO uvicorn.error 127.0.0.1:54468
// - "WebSocket /ws/events" 403`。
const ServerLoggerName = "uvicorn.error"

// Sink 是写到同一个日志文件的一组 logger，对应 uvicorn_log_config 的 file handler
// 在 Go 侧的等价物。
//
// 三个 logger 共用同一个文件、同一把写锁与**同一个级别变量**：参照实现里所有 logger
// 的 handler 都指向同一个 FileHandler，行与行之间不会交错；而 uvicorn_log_config 给
// 三个 logger 设的是同一个 level（历史配置为 "INFO"），因此 Go 侧也让它们共用一个
// *slog.LevelVar。
type Sink struct {
	// App 用于应用侧结构化日志（交给 internal/proxy、internal/wsproxy 等），
	// 对应 `logging.getLogger("auto_model_key_router.app")`。
	App *slog.Logger
	// Access 用于 HTTP 访问日志（见 internal/server 的 accessLog）。
	Access *slog.Logger
	// Server 用于服务器自身的生命周期与握手日志（启动、关停、升级被拒）。
	Server *slog.Logger

	// level 是三个 handler 的落盘级别（Enabled 都读它），新开 Sink 时为 Info。
	//
	// 必须是 *slog.LevelVar 而不是 slog.Level：fileHandler.WithAttrs 是**结构体
	// 拷贝**，值类型会被冻结在派生那一刻，之后 SetLevel 对派生出来的 handler 就
	// 失效了；指针则让副本与原 handler 看到同一级别。也正因为共用一个变量，改一次
	// 即对 App/Access/Server 同时生效，热重载（internal/server/reload.go 直接
	// 写 LevelVar）与 Sink.SetLevel 才不会打架。
	level *slog.LevelVar

	file *os.File
}

// Open 打开（按需创建）日志文件并返回 Sink。
//
// 对应 uvicorn_log_config 的两处副作用：父目录按需创建
// （`path.parent.mkdir(parents=True, exist_ok=True)`），以及以**追加**方式打开文件
// （logging.FileHandler 的默认 mode="a"）。追加而非截断不可省：前台启动前已经归档过
// 旧日志（ArchiveLogForForeground），截断会把归档后新写入的内容抹掉。
func Open(path string) (*Sink, error) {
	if err := os.MkdirAll(pyParent(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}
	mu := &sync.Mutex{}
	// 默认 Info，与参照实现 uvicorn_log_config 给三个 logger 设的 level="INFO"
	// 对齐。运行期由 SetLevel 改写（cmd/amkr 在打开文件后立刻按 config.log_level
	// 设置，热重载则经 LevelVar 改同一个变量），因此这里不能在 handler 里写死常量。
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	return &Sink{
		App:    slog.New(&fileHandler{name: AppLoggerName, file: file, mu: mu, level: level}),
		Access: slog.New(&fileHandler{name: AccessLoggerName, file: file, mu: mu, level: level}),
		Server: slog.New(&fileHandler{name: ServerLoggerName, file: file, mu: mu, level: level}),
		level:  level,
		file:   file,
	}, nil
}

// Close 关闭底层文件。
func (s *Sink) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	return s.file.Close()
}

// SetLevel 运行时调整落盘级别，三个 logger 同时生效。
//
// 与 Close 一样容忍 nil 接收者：cmd/amkr 在日志文件打不开时会把 sink 置为 nil
// （日志写不进去不该拦住服务），接线方因此不必到处判空。
func (s *Sink) SetLevel(level slog.Level) {
	if s == nil || s.level == nil {
		return
	}
	// LevelVar.Set 内部是 atomic，已经在写日志的 goroutine 立刻看到新值。
	s.level.Set(level)
}

// Level 返回当前落盘级别。nil 或未经 Open 的 Sink 返回默认的 Info。
func (s *Sink) Level() slog.Level {
	if s == nil || s.level == nil {
		return slog.LevelInfo
	}
	return s.level.Level()
}

// LevelVar 交出内部的级别变量，供 internal/server 做配置热重载。
//
// 它必须就是三个 handler 读的那一个变量：reload 里直接 `LogLevel.Set(...)`，
// 不经过 Sink 也不会漏掉任何一个 handler。nil Sink 返回 nil。
func (s *Sink) LevelVar() *slog.LevelVar {
	if s == nil {
		return nil
	}
	return s.level
}

// fileHandler 把 slog 记录按 Python logging 的行格式写进一个文件。
type fileHandler struct {
	name  string
	file  io.Writer
	mu    *sync.Mutex
	level *slog.LevelVar
	attrs []slog.Attr
}

// Enabled 按 Sink 的 LevelVar 过滤：默认 Info，可运行期调整。
//
// 参照实现给 file logger 设的是 level="INFO"，但那个级别是启动期常量；Go 侧要由
// config.log_level 决定、并且能在热重载时改，因此读共享的 LevelVar 而不是写死常量。
// handler 没拿到 LevelVar（只在包内直接构造且没走 Open 时出现）按 Info 处理，与
// Sink 的默认值一致。
func (h *fileHandler) Enabled(_ context.Context, level slog.Level) bool {
	if h.level == nil {
		return level >= slog.LevelInfo
	}
	return level >= h.level.Level()
}

// Handle 写出一行。写入串行化：slog 允许并发调用 Handle，一行应当原子落盘。
func (h *fileHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString(record.Time.Format(pythonTimeLayout))
	line.WriteByte(' ')
	line.WriteString(pythonLevelName(record.Level))
	line.WriteByte(' ')
	line.WriteString(h.name)
	line.WriteByte(' ')
	line.WriteString(record.Message)
	if attrs := formatAttrs(h.attrs, &record); attrs != "" {
		line.WriteByte(' ')
		line.WriteString(attrs)
	}
	line.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.file, line.String())
	return err
}

// WithAttrs 累积属性。mu 与 level 都是指针，因此这里的结构体拷贝不会复制锁，也不会
// 把级别拷成一份互不相干的值——派生 handler 仍与原 handler 共享同一级别。
func (h *fileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

// WithGroup 被忽略，见文件头说明。
func (h *fileHandler) WithGroup(string) slog.Handler { return h }

// pythonLevelName 复刻 Python 的 levelname（WARN 在 Python 里叫 WARNING）。
func pythonLevelName(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARNING"
	default:
		return "ERROR"
	}
}

// formatAttrs 把属性渲染成 json.dumps 风格的 JSON 对象；没有属性时返回空串。
//
// 分隔符用 json.dumps 的默认值（", " 与 ": "），非 ASCII 不转义
// （对应 `ensure_ascii=False`）。
func formatAttrs(extra []slog.Attr, record *slog.Record) string {
	if len(extra)+record.NumAttrs() == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteByte('{')
	index := 0
	writeAttr := func(attr slog.Attr) {
		if index > 0 {
			out.WriteString(", ")
		}
		index++
		out.WriteString(strconv.Quote(attr.Key))
		out.WriteString(": ")
		writeJSONValue(&out, attr.Value)
	}
	for _, attr := range extra {
		writeAttr(attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		writeAttr(attr)
		return true
	})
	out.WriteByte('}')
	return out.String()
}

// writeJSONValue 按 json.dumps 的取值规则渲染单个属性值。
func writeJSONValue(out *strings.Builder, value slog.Value) {
	switch value.Kind() {
	case slog.KindString:
		out.WriteString(strconv.Quote(value.String()))
	case slog.KindInt64:
		out.WriteString(strconv.FormatInt(value.Int64(), 10))
	case slog.KindUint64:
		out.WriteString(strconv.FormatUint(value.Uint64(), 10))
	case slog.KindFloat64:
		out.WriteString(strconv.FormatFloat(value.Float64(), 'g', -1, 64))
	case slog.KindBool:
		out.WriteString(strconv.FormatBool(value.Bool()))
	case slog.KindDuration:
		out.WriteString(strconv.Quote(value.Duration().String()))
	case slog.KindTime:
		out.WriteString(strconv.Quote(value.Time().Format(time.RFC3339Nano)))
	default:
		writeJSONAny(out, value.Any())
	}
}

// writeJSONAny 渲染 KindAny。参照实现的 payload 是 dict[str, Any]，数值与布尔要落成
// JSON 字面量（`"status_code": 200`），不能一律加引号。
func writeJSONAny(out *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(typed))
	case int:
		out.WriteString(strconv.Itoa(typed))
	case int64:
		out.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		out.WriteString(strconv.FormatFloat(typed, 'g', -1, 64))
	case error:
		out.WriteString(strconv.Quote(typed.Error()))
	default:
		out.WriteString(strconv.Quote(fmt.Sprint(typed)))
	}
}
