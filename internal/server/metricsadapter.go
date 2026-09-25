package server

import (
	"context"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/proxy"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// metricsAdapter 把 internal/metrics 的 Store 适配成装配层需要的两个指标接缝。
//
// 为什么需要适配器：两个包各自声明了**窄接口**，谁都不愿意依赖对方的类型。
//
//   - `proxy.MetricsSink`（proxy/proxy.go:125-134）只要求
//     `Record(ctx, proxy.MetricRecord) error`；
//   - `runtime.MetricsSink`（runtime/manager.go:35-43）只要求
//     `RecordStream(runtime.StreamOutcome) error` 与 `Close() error`。
//
// 而 `metrics.Store` 只有 `Record(metrics.RecordParams) error` 与 `Close() error`，
// 所以两个接缝都差一层字段搬运。顺带补上参照实现里由 app 层承担的
// acquire_active / release_active（app.py:354、359、367）。
type metricsAdapter struct{ store *metrics.Store }

// newMetricsAdapter 包装一个已打开的指标库。
func newMetricsAdapter(store *metrics.Store) *metricsAdapter {
	return &metricsAdapter{store: store}
}

// 编译期断言：两个接缝都必须被满足。写在这里而不是各自的测试里，是为了让「接口变了」
// 在编译期就暴露，而不是等到某个测试挂掉。
var (
	_ proxy.MetricsSink   = (*metricsAdapter)(nil)
	_ runtime.MetricsSink = (*metricsAdapter)(nil)
)

// Record 写入一行（非流式路径，proxy/retry.go:712 的调用点）。
func (a *metricsAdapter) Record(_ context.Context, record proxy.MetricRecord) error {
	return a.store.Record(recordParams(record))
}

// RecordStream 写入一行（流式路径，runtime/streaming.go:385 的调用点）。
//
// 注意：internal/proxy 有自己的 streamLifecycle（proxy/lifecycle.go），生产路径上
// 走的是上面的 Record；本方法是为 runtime 侧的接缝补齐的，行为与 record() 一致。
func (a *metricsAdapter) RecordStream(outcome runtime.StreamOutcome) error {
	return a.store.Record(streamRecordParams(outcome))
}

// Close 关闭指标库，对应 lifespan 关停时的 metrics 释放（由 RuntimeManager 按代调用）。
func (a *metricsAdapter) Close() error { return a.store.Close() }

// AcquireActive 对应 metrics.acquire_active（app.py:354）。
func (a *metricsAdapter) AcquireActive() { a.store.AcquireActive() }

// ReleaseActive 对应 metrics.release_active（app.py:359、367）。
func (a *metricsAdapter) ReleaseActive() { a.store.ReleaseActive() }

// recordParams 是 proxy.MetricRecord -> metrics.RecordParams 的逐字段搬运。
//
// 字段名几乎一致，但有三处需要显式转换，都是「Python 的可选参数」在 Go 侧的落点：
//
//   - StatusCode *int -> *int64：Python 传 None 表示「上游请求直接失败、没有响应」，
//     必须保持 nil，不能变成 0（0 会被算成一次 4xx 失败，落库的 status_code 也不同）；
//   - RequestedModelID string -> *string：空串映射成 nil。两者在 metrics.Store.Record
//     里等价（`if requested != nil && *requested != ""` 才覆盖，且写库用的
//     request_model_id 在为空时回退到 ModelID），因此这个选择不影响落库字节；
//   - ProviderID / UpstreamModelID 原样传递，PoolName 在 v4 里恒为 nil（没有 pool
//     概念），保留传递是为了将来接上 pool 时不需要改装配层。
func recordParams(record proxy.MetricRecord) metrics.RecordParams {
	var statusCode *int64
	if record.StatusCode != nil {
		value := int64(*record.StatusCode)
		statusCode = &value
	}
	return metrics.RecordParams{
		ModelID:          record.ModelID,
		KeyName:          record.KeyName,
		StatusCode:       statusCode,
		Usage:            record.Usage,
		Retried:          record.Retried,
		Failed:           record.Failed,
		DurationMS:       record.DurationMS,
		FirstTokenMS:     record.FirstTokenMS,
		RequestedModelID: optionalText(record.RequestedModelID),
		CallerType:       record.CallerType,
		ProviderID:       record.ProviderID,
		PoolName:         record.PoolName,
		UpstreamModelID:  record.UpstreamModelID,
		// Workspace 原样传递（有意增补，见 metrics.RecordParams）：
		// **不能**走 optionalText——那是给 request_metrics 的列准备的，那里的空串与
		// None 落库等价。工作空间不落 request_metrics，空串在这里有独立含义：
		// 「这次请求没有归属」，落库时表现为不写旁挂表。
		Workspace: record.Workspace,
		// AccessKeyID 同理原样传递（有意增补，见 metrics.RecordParams）：空串表示
		// 这次请求不出自访问密钥，落库时不写 request_access_key 旁挂表。
		AccessKeyID: record.AccessKeyID,
		// ClientAddr / UserAgent 是请求来源（有意增补，见 metrics.RecordParams）：
		// 同样不落 request_metrics，空串表示没有来源可记，落库时不写 request_source。
		ClientAddr: record.ClientAddr,
		UserAgent:  record.UserAgent,
	}
}

// streamRecordParams 是 runtime.StreamOutcome -> metrics.RecordParams 的搬运。
//
// 与上面的差别只有 StatusCode：StreamOutcome 用 int（流一定已经拿到响应头，不存在
// 「没有响应」的状态），因此直接取地址。
func streamRecordParams(outcome runtime.StreamOutcome) metrics.RecordParams {
	statusCode := int64(outcome.StatusCode)
	usage, _ := outcome.Usage.(*canonical.Value)
	return metrics.RecordParams{
		ModelID:          outcome.ModelID,
		KeyName:          outcome.KeyName,
		StatusCode:       &statusCode,
		Usage:            usage,
		Retried:          false,
		Failed:           outcome.Failed,
		DurationMS:       int64(outcome.DurationMS),
		FirstTokenMS:     int64(outcome.FirstTokenMS),
		RequestedModelID: optionalText(outcome.RequestedModelID),
		CallerType:       outcome.CallerType,
		ProviderID:       optionalText(outcome.ProviderID),
		PoolName:         optionalText(outcome.PoolName),
		UpstreamModelID:  optionalText(outcome.UpstreamModelID),
	}
}

// optionalText 把空串映射成 nil。
//
// 这些字段在 Python 侧是 `str | None`：空串与 None 在 record() 里的落库结果相同
// （见 recordParams 的说明），因此统一成 nil，让「缺省」只有一种表示。
func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
