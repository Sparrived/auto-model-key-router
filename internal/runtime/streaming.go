package runtime

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 流式超时的两阶段语义不同，必须分开实现：
//
//   - **响应头阶段**由 request_timeout 约束，交由 http.Client 的 Timeout 或调用方
//     的 context 处理（本包不涉及）；
//   - **响应体阶段**的首块沿用同一个绝对截止时间（firstByteDeadline），其后每块
//     重置 idleTimeout 且**没有总时长上限**。
//
// 关键：这两个超时都**不能**用同一个 context.WithCancel 实现。在 Go 里取消
// context 会同时终止整条请求，而参照实现的语义是「等下一块超时就报错」，连接由
// 外层收尾逻辑关闭。因此这里对每一块单独派生带超时的子 context：子 context 到期
// 只影响这次等待，父 context 仍然有效——父 context 被取消（下游断开）时才真正
// 终止整个流。

// ErrStreamTimeout 表示等待上游数据块超时。
//
// 对应参照实现的 TimeoutError（它由 asyncio.timeout 抛出，再被包装成带说明文本
// 的 TimeoutError）。用哨兵错误而非裸 errors.New，便于调用方区分「超时」与
// 「上游断开」。
var ErrStreamTimeout = errors.New("流式读取超时")

// StreamReader 逐个产出上游响应体数据块。
//
// 返回 io.EOF 表示流正常结束。实现通常是 http.Response.Body 的包装。
type StreamReader interface {
	Next(ctx context.Context) ([]byte, error)
}

// BodyStreamReader 把 http.Response.Body 适配成 StreamReader。
//
// 单块缓冲复用同一个切片，因此**调用方必须在下一轮调用前消费完当前块**。这是
// 流式转发的常态（读到就写下游），但需要明确写出来，避免有人把它当队列用。
type BodyStreamReader struct {
	Body   io.ReadCloser
	buffer []byte
}

// NewBodyStreamReader 构造适配器。
func NewBodyStreamReader(body io.ReadCloser) *BodyStreamReader {
	return &BodyStreamReader{Body: body, buffer: make([]byte, 32*1024)}
}

// Next 读取下一块。
func (r *BodyStreamReader) Next(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	count, err := r.Body.Read(r.buffer)
	if count > 0 {
		return r.buffer[:count], nil
	}
	if err != nil {
		return nil, err
	}
	// Read 返回 (0, nil) 是允许的，但对流式无意义；继续读避免调用方空转。
	return r.Next(ctx)
}

// IterStreamBytes 按两阶段超时语义逐块读取上游数据。
//
// firstByteDeadline 是**绝对时间**（与参照实现传入 loop.time() 基准的绝对
// deadline 一致），只有首块受它约束；之后每块各自获得 idleTimeout。首块读取
// 完成后不再检查总时长——这与「无总时长上限」的既定行为一致。
//
// 整条流只启动**一个**后台读协程并复用同一个 time.Timer，通过 results/ack 握手确保
// 调用方在 yield 消费完当前切片之前，BodyStreamReader 不会覆盖底层缓冲，避免按块启协程
// 带来的调度与 GC 开销。
//
// yield 返回错误时立即停止并返回该错误（下游写失败）。
func IterStreamBytes(
	ctx context.Context,
	source StreamReader,
	firstByteDeadline time.Time,
	idleTimeout time.Duration,
	yield func(chunk []byte) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	type outcome struct {
		chunk []byte
		err   error
	}

	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()

	// results 缓冲为 1：即使主协程因超时或下游失败提前退出，读协程完成最后一次
	// Next 后也能无阻塞写入并退出，不发生协程泄漏。
	results := make(chan outcome, 1)
	ack := make(chan struct{})
	go func() {
		for {
			chunk, err := source.Next(readCtx)
			results <- outcome{chunk: chunk, err: err}
			if err != nil {
				return
			}
			select {
			case <-ack:
			case <-readCtx.Done():
				return
			}
		}
	}()

	// 首块：剩余时间取 max(0, deadline - now)。
	remaining := time.Until(firstByteDeadline)
	if remaining < 0 {
		remaining = 0
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()

	firstChunk := true
	for {
		var result outcome
		select {
		case result = <-results:
			if err := ctx.Err(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			if firstChunk {
				return &streamTimeoutError{message: "timed out waiting for first stream byte"}
			}
			return &streamTimeoutError{message: "timed out waiting for next stream byte"}
		}

		if result.err != nil {
			if errors.Is(result.err, io.EOF) {
				// 上游一个字节都没发就结束了——参照实现直接 return，不报错。
				return nil
			}
			if errors.Is(result.err, ErrStreamTimeout) {
				if firstChunk {
					return &streamTimeoutError{message: "timed out waiting for first stream byte"}
				}
				return &streamTimeoutError{message: "timed out waiting for next stream byte"}
			}
			return result.err
		}

		if err := yield(result.chunk); err != nil {
			return err
		}
		firstChunk = false

		// yield 已消费完当前块：放行读协程读取下一块，并把定时器重置为空闲超时窗口。
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(idleTimeout)

		select {
		case ack <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// streamTimeoutError 携带参照实现的说明文本。
type streamTimeoutError struct{ message string }

func (e *streamTimeoutError) Error() string { return e.message }
func (e *streamTimeoutError) Is(target error) bool {
	return target == ErrStreamTimeout
}

// StreamOutcome 是一次流式请求的可观测结果，交给指标层落库。
//
// 字段与参照实现 MetricsStore.record 的实参一一对应。用结构体而非长参数列表，
// 是因为这里字段多且同名类型容易传错位（尤其 model_id 与 requested_model_id）。
type StreamOutcome struct {
	ModelID          string
	KeyName          string
	StatusCode       int
	Usage            any
	Failed           bool
	DurationMS       int
	FirstTokenMS     int
	RequestedModelID string
	CallerType       string
	ProviderID       string
	PoolName         string
	UpstreamModelID  string
}

// StreamLifecycle 记录的是一次流式响应的收尾状态。
//
// finish 必须**恰好执行一次**：参照实现靠调用点的控制流保证，Go 侧用 sync.Once
// 兜底，因为重复执行会重复写指标行并把 key 的在途计数减两次。
type StreamLifecycle struct {
	StatusCode       int
	KeyPool          KeyHealth
	Metrics          MetricsSink
	ModelID          string
	KeyName          string
	RequestedModelID string
	Upstream         string
	Started          time.Time
	CallerType       string

	ProviderID      string
	PoolName        string
	UpstreamModelID string

	now func() time.Time

	mu           sync.Mutex
	chunkCount   int
	byteCount    int
	firstTokenMS int
	usage        any
	finishOnce   sync.Once
	finishErr    error
}

// NewStreamLifecycle 构造流式生命周期记录器。
func NewStreamLifecycle(
	statusCode int,
	keyPool KeyHealth,
	metrics MetricsSink,
	modelID, keyName, requestedModelID, upstream string,
	now func() time.Time,
) *StreamLifecycle {
	if now == nil {
		now = time.Now
	}
	return &StreamLifecycle{
		StatusCode:       statusCode,
		KeyPool:          keyPool,
		Metrics:          metrics,
		ModelID:          modelID,
		KeyName:          keyName,
		RequestedModelID: requestedModelID,
		Upstream:         upstream,
		Started:          now(),
		CallerType:       "local",
		now:              now,
	}
}

// ObserveChunk 记录一块已转发给下游的数据。
func (l *StreamLifecycle) ObserveChunk(size int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.chunkCount++
	l.byteCount += size
	if l.firstTokenMS == 0 {
		l.firstTokenMS = l.elapsedMSLocked()
	}
}

// SetUsage 记录从流中解析出的 usage。
func (l *StreamLifecycle) SetUsage(usage any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.usage = usage
}

// ChunkCount 返回已转发的块数。
func (l *StreamLifecycle) ChunkCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.chunkCount
}

// ByteCount 返回已转发的字节数。
func (l *StreamLifecycle) ByteCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byteCount
}

// FirstTokenMS 返回首块耗时。
func (l *StreamLifecycle) FirstTokenMS() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.firstTokenMS
}

// ElapsedMS 返回从开始到现在的毫秒数。
//
// 用 math.RoundToEven 而非 math.Round：Python 的 round() 是**银行家舍入**，
// 恰好落在 .5 时向偶数取整。这是会写进指标库的数值，必须一致。
func (l *StreamLifecycle) ElapsedMS() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.elapsedMSLocked()
}

// elapsedMSLocked 是无锁版本，调用方须持锁。
func (l *StreamLifecycle) elapsedMSLocked() int {
	elapsed := float64(l.now().Sub(l.Started)) / float64(time.Millisecond)
	if elapsed < 0 {
		elapsed = 0
	}
	return int(math.RoundToEven(elapsed))
}

// ErrorPayload 组装流式错误的诊断信息。
//
// 注意 status_code 来自**上游响应**（可能是 200），因此不能用来判断是否失败；
// failed 由调用方显式传入。
func (l *StreamLifecycle) ErrorPayload(err error, contentType string) map[string]any {
	l.mu.Lock()
	chunks := l.chunkCount
	bytes := l.byteCount
	l.mu.Unlock()

	payload := map[string]any{
		"model_id":           l.ModelID,
		"requested_model_id": l.RequestedModelID,
		"key_name":           l.KeyName,
		"upstream":           l.Upstream,
		"status_code":        l.StatusCode,
		"content_type":       nil,
		"error_type":         errorTypeName(err),
		"error":              err.Error(),
		"chunks":             chunks,
		"bytes":              bytes,
		"duration_ms":        l.ElapsedMS(),
	}
	if contentType != "" {
		payload["content_type"] = contentType
	}
	return payload
}

// errorTypeName 返回错误类型名，用作 Python 异常类名的近似替代。
//
// 参照实现用 exc.__class__.__name__（如 "ReadTimeout"）。Go 的 *url.Error 等
// 类型名不同，因此这里只保证「可诊断」，不追求与 Python 字面一致——该字段进的是
// 日志而非持久化契约。
func errorTypeName(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrStreamTimeout) {
		return "ReadTimeout"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "CancelledError"
	case errors.Is(err, context.DeadlineExceeded):
		return "ReadTimeout"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "RemoteProtocolError"
	}
	return typeName(err)
}

// Finish 收尾：写指标、回写 key 状态、释放并发计数、关闭响应体。
//
// 执行顺序与参照实现一致且不可交换：
//  1. 写指标行（此时并发计数尚未释放，指标里的时间点更贴近真实结束时刻）；
//  2. 按状态码回写 key 健康（重试码或 failed → 记失败；<400 → 记成功）；
//  3. **释放 key 的并发计数**——这是唯一的释放路径，漏掉会让该 key 永久占用一个
//     在途名额；
//  4. 最后关闭响应体。
//
// 幂等：sync.Once 保证只执行一次。
func (l *StreamLifecycle) Finish(failed bool, responseCloser io.Closer, retryAfter *float64) error {
	l.finishOnce.Do(func() {
		l.mu.Lock()
		usage := l.usage
		firstTokenMS := l.firstTokenMS
		l.mu.Unlock()

		outcome := StreamOutcome{
			ModelID:          l.ModelID,
			KeyName:          l.KeyName,
			StatusCode:       l.StatusCode,
			Usage:            usage,
			Failed:           failed,
			DurationMS:       l.ElapsedMS(),
			FirstTokenMS:     firstTokenMS,
			RequestedModelID: l.RequestedModelID,
			CallerType:       l.CallerType,
			ProviderID:       l.ProviderID,
			PoolName:         l.PoolName,
			UpstreamModelID:  l.UpstreamModelID,
		}
		if l.Metrics != nil {
			l.finishErr = l.Metrics.RecordStream(outcome)
		}

		if l.KeyPool != nil {
			statusCode := l.StatusCode
			switch {
			case failed || IsRetryableStatus(statusCode):
				l.KeyPool.MarkFailure(l.ModelID, l.KeyName, &statusCode, retryAfter)
			case statusCode < 400:
				l.KeyPool.MarkSuccess(l.ModelID, l.KeyName)
			}
			// 无条件释放：即便状态码既不重试也不成功（如 400），也必须归还名额。
			l.KeyPool.ReleaseKey(l.ModelID, l.KeyName)
		}

		if responseCloser != nil {
			_ = responseCloser.Close()
		}
	})
	return l.finishErr
}

// typeName 返回错误的 Go 类型名（去掉指针前缀），用于诊断输出。
func typeName(err error) string {
	name := reflect.TypeOf(err).String()
	return strings.TrimPrefix(name, "*")
}

// RetryAfterSeconds 解析 Retry-After 响应头，返回等待秒数。
//
// 先按数值解析（最常见的形态），失败再按 HTTP 日期解析。无法识别时返回 nil，
// 让上层退回到按 cooldown_seconds 计算。
//
// 与参照实现的一处差异：Python 的 email.utils.parsedate_to_datetime 比 Go 的
// http.ParseTime 宽松（能接受更多历史日期格式）。这里用标准库解析，罕见格式会
// 落到 nil，效果是「退回默认冷却时长」而非误用错误数值——属于可接受的收紧。
func RetryAfterSeconds(headerValue string, now time.Time) *float64 {
	if headerValue == "" {
		return nil
	}
	if seconds, err := strconv.ParseFloat(headerValue, 64); err == nil {
		value := math.Max(0, seconds)
		return &value
	}
	retryAt, err := http.ParseTime(headerValue)
	if err != nil {
		return nil
	}
	value := math.Max(0, retryAt.Sub(now).Seconds())
	return &value
}
