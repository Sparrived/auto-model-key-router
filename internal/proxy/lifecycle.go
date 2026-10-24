package proxy

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// streamLifecycle 记录一次流式响应的收尾状态。
//
// 移植 streaming.py:50 的 StreamLifecycle。**为什么不直接用 internal/runtime 里
// 那份**：那份把指标写入接缝定义成 runtime.MetricsSink（另一个包为它自己的测试
// 定义的形状），而本包刻意用更窄的 proxy.MetricsSink 与 in-flight 的 metrics 包
// 解耦；同时本包还需要在收尾时自己解析上游响应头里的 retry-after。两处都无法靠
// 拼接满足，因此在包内实现一份等价语义——这也符合「只能改 internal/proxy/」的
// 约束（无需改动 internal/runtime）。
//
// finish 必须**恰好执行一次**：参照实现靠调用点的控制流保证，这里用 sync.Once
// 兜底，因为重复执行会重复写指标行并把 key 的在途计数减两次。
type streamLifecycle struct {
	modelID          string
	keyName          string
	statusCode       int
	requestedModelID string
	upstream         string
	callerType       string
	providerID       string
	poolName         string
	upstreamModelID  string
	mediaType        string
	started          time.Time
	ctx              context.Context
	now              func() time.Time
	keyPool          KeyPool
	// retryAfter 是上游 Retry-After 解析出的秒数；nil 表示没有该头或无法解析。
	retryAfter *float64
	onFinish   func(record MetricRecord, failed bool)

	mu           sync.Mutex
	chunkCount   int
	byteCount    int
	firstTokenMS int64
	usage        *canonical.Value
	finishOnce   sync.Once
}

// ObserveChunk 记录一块已转发给下游的数据。
//
// first_token_ms 只在第一块时取一次，因此它记录的是**上游首字节**时间
// （streaming.py:69）。
func (l *streamLifecycle) ObserveChunk(size int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.chunkCount++
	l.byteCount += size
	if l.firstTokenMS == 0 {
		l.firstTokenMS = l.elapsedMSLocked()
	}
}

// SetUsage 记录从流中解析出的 usage。
func (l *streamLifecycle) SetUsage(usage *canonical.Value) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.usage = usage
}

// Usage 返回已累积的 usage。
func (l *streamLifecycle) Usage() *canonical.Value {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.usage
}

// ElapsedMS 返回从开始到现在的毫秒数。
//
// 用 roundHalfEven 而非 time.Duration.Milliseconds()：Python 的 round 是**银行家
// 舍入**，而 .Milliseconds() 是截断。这是会写进指标库的数值，必须一致。
func (l *streamLifecycle) ElapsedMS() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.elapsedMSLocked()
}

// elapsedMSLocked 是无锁版本，调用方须持锁。
func (l *streamLifecycle) elapsedMSLocked() int64 {
	elapsed := l.now().Sub(l.started).Seconds() * 1000
	if elapsed < 0 {
		elapsed = 0
	}
	return roundHalfEven(elapsed)
}

// ErrorPayload 组装流式错误的诊断信息。
//
// 字段与参照实现 streaming.py:78 的 error_payload 一一对应。status_code 来自
// **上游响应**（可能是 200），因此不能用来判断是否失败。
func (l *streamLifecycle) ErrorPayload(err error, contentType string) map[string]any {
	l.mu.Lock()
	chunks := l.chunkCount
	bytes := l.byteCount
	l.mu.Unlock()

	payload := map[string]any{
		"model_id":           l.modelID,
		"requested_model_id": l.requestedModelID,
		"key_name":           l.keyName,
		"upstream":           l.upstream,
		"status_code":        l.statusCode,
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

// Finish 收尾：写指标、回写 key 状态、释放并发计数、关闭响应体。
//
// 执行顺序与参照实现一致且不可交换（streaming.py:93）：
//  1. 写指标行（此时并发计数尚未释放，指标里的时间点更贴近真实结束时刻）；
//  2. 按状态码回写 key 健康：`failed || 重试码` → 记失败；`<400` → 记成功；
//  3. **释放 key 的并发计数**——这是流式尝试唯一的释放路径，漏掉会让该 key 永久
//     占用一个在途名额，并让 RuntimeManager.Close() 永久阻塞；
//  4. 最后关闭响应体。
//
// 幂等：sync.Once 保证只执行一次。
func (l *streamLifecycle) Finish(failed bool, responseBody io.Closer, neutralHealth bool) {
	l.finishOnce.Do(func() {
		l.mu.Lock()
		usage := l.usage
		firstTokenMS := l.firstTokenMS
		l.mu.Unlock()

		statusCode := l.statusCode
		record := MetricRecord{
			ModelID:    l.modelID,
			KeyName:    l.keyName,
			StatusCode: &statusCode,
			Usage:      usage,
			// 参照实现的 StreamLifecycle.finish 不传 retried（streaming.py:94），
			// 而 record 的默认值是 False——这里显式写出来以免被误改。
			Retried:          false,
			Failed:           failed,
			DurationMS:       l.ElapsedMS(),
			FirstTokenMS:     firstTokenMS,
			RequestedModelID: l.requestedModelID,
			CallerType:       l.callerType,
			ProviderID:       stringPtr(l.providerID),
			PoolName:         stringPtr(l.poolName),
			UpstreamModelID:  stringPtr(l.upstreamModelID),
		}
		if l.onFinish != nil {
			l.onFinish(record, failed)
		}

		if l.keyPool != nil {
			switch {
			case neutralHealth:
				// 下游结束或受控预算/策略终止不能证明 key 好坏，也不能抹掉已有冷却。
			case failed || IsRetryableStatus(l.statusCode):
				// retry_after 交给调用方解析：流式收尾时上游响应体可能已被关闭，
				// 但响应头仍在（参照实现读的是 response.headers）。
				l.keyPool.MarkFailure(l.modelID, l.keyName, &statusCode, l.retryAfter)
			case l.statusCode < 400:
				l.keyPool.MarkSuccess(l.modelID, l.keyName)
			}
			// 无条件释放：即便状态码既不重试也不成功（如 400），也必须归还名额。
			l.keyPool.ReleaseKey(l.modelID, l.keyName)
		}

		if responseBody != nil {
			_ = responseBody.Close()
		}
	})
}

// stringPtr 返回字符串指针。
//
// 空串仍然返回指针：Python 的 provider_id 可能是空串，与 None 不是一回事
// （metrics.RecordParams 的 *string 正是为了区分这两者）。
func stringPtr(value string) *string { return &value }

// errorTypeName 返回错误类型名，用作 Python 异常类名的近似替代。
//
// 参照实现用 exc.__class__.__name__（如 "ReadTimeout"）。Go 的 *url.Error 等类型
// 名不同，因此这里只保证「可诊断」，不追求与 Python 字面一致——该字段进的是日志
// 而非持久化契约。
func errorTypeName(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, runtime.ErrStreamTimeout) {
		return "ReadTimeout"
	}
	switch {
	case errors.Is(err, errUpstreamStream):
		return "UpstreamStreamError"
	case errors.Is(err, errEmptyStream), errors.Is(err, errIncompleteStream), errors.Is(err, errStreamConversion):
		return "RemoteProtocolError"
	case errors.Is(err, errDownstreamWrite):
		return "DownstreamWriteError"
	case errors.Is(err, context.Canceled):
		return "CancelledError"
	case errors.Is(err, context.DeadlineExceeded):
		return "ReadTimeout"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "RemoteProtocolError"
	}
	name := reflect.TypeOf(err).String()
	return strings.TrimPrefix(name, "*")
}
