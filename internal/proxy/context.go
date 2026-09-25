package proxy

import (
	"net/http"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// RequestContext 是一次代理请求的全部解析结果。
//
// 对应参照实现的 ProxyRequestContext（proxy_handler.py:71），字段一一对应，只多
// 了 upstreamCalls（上游调用计数，见 DefaultMaxUpstreamCallsPerRequest）。
//
// 值语义与参照实现一致：备选模型路径上参照实现用 dataclasses.replace 造一个
// **新** context（proxy_handler.py:142），Go 侧同样是拷贝后改字段，因此 primary
// 的 attempts / key_count 不会被备选覆盖。
type RequestContext struct {
	Path    string
	Request *http.Request
	Runtime *runtime.RuntimeResources

	// AccessKey 非空表示本请求由一把访问密钥发起，它的两份清单（供应商、模型名）
	// 分别在选 key 与解析模型名时生效。nil 表示完整权限或工作空间推理凭据。
	AccessKey  *config.AccessKeyConfig
	CallerType string

	Payload     *canonical.Value
	IsStream    bool
	OriginalRaw []byte

	RequestedModelID   string
	RequestedModelName string
	RequestedKeyName   *string

	ModelID string

	// Config 是本代运行时资源的配置快照。
	//
	// 单独存一份而不是每次从 Runtime 读：热重载会换 RuntimeResources，而一次
	// 请求必须自始至终用同一份配置（尤其是 max_retries 与两个流式超时）。
	Config *config.RouterConfig

	KeyCount  int
	OnlyFirst bool
	Attempts  int

	CacheAffinityKey *string
	UseNative        bool
	// Workspace 是本请求使用的工作空间（来自 X-AMKR-Workspace 头）。
	//
	// 归一化后总是具体名字：不带请求头时是 config.DefaultWorkspace。
	Workspace  string
	TaskName   *string
	TaskParams *canonical.Value

	// ReasoningEffort 是本次请求最终生效的推理强度（thinking effort），空串表示没有
	// 可读的强度。它在 prepare 里一次算好（见 effectiveReasoningEffort）。
	//
	// 纯观测项：不参与任何决策，只进 request_shape 旁挂表。放在 context 上而不是写
	// 指标时现算，是因为算它要克隆整份载荷，而写指标是**每次尝试**一次。
	ReasoningEffort string

	// FallbackTarget 非空表示这是个备选 context（仅用于日志区分）。
	FallbackTarget string

	// FlatPayload 表示请求体不是 JSON 对象（multipart 表单）。
	//
	// 参照实现里 payload 恒为 dict，因此所有 `payload.get(...)` 都安全；Go 侧为
	// multipart 保留 `{}` 作为 payload，同时用这个标志表明「body 不是 JSON」，
	// 以免 `_is_stream_request` 之类的判断误读。
	FlatPayload bool

	// now 是本请求使用的时钟，与 Handler 一致（注入便于测试断言耗时字段）。
	now           func() time.Time
	upstreamCalls *upstreamCallCounter
}

// pool 取回本请求使用的 KeyPool。
//
// 不把 KeyPool 直接放进 RequestContext 的导出字段，是为了让「解类型断言」只有
// 一个来源（keyPoolOf），避免两处断言在装配错误时行为分叉。
func (c *RequestContext) pool() KeyPool { return keyPoolOf(c.Runtime) }

// keyPoolOf 把 runtime 资源的 KeyPool 接缝折算成本包需要的 KeyPool。
//
// runtime.RuntimeResources.KeyPool 的静态类型是 runtime.KeyHealth（只有健康回写
// 的三个方法），而本包需要完整的 key 选择能力。生产装配时放进去的就是
// *keypool.KeyPool，走类型断言即可；断言失败说明装配错误，此时宁可失败关闭也不
// 能带着一个不完整的实现继续跑。
func keyPoolOf(resources *runtime.RuntimeResources) KeyPool {
	if resources == nil || resources.KeyPool == nil {
		return nil
	}
	pool, _ := resources.KeyPool.(KeyPool)
	return pool
}

// upstreamClientOf 取回上游客户端。
//
// 同样的理由：runtime 只要求 HTTPClient 实现 CloseIdleConnections，而本包需要
// Do / DoStream。
func upstreamClientOf(resources *runtime.RuntimeResources) UpstreamClient {
	if resources == nil || resources.HTTPClient == nil {
		return nil
	}
	client, _ := resources.HTTPClient.(UpstreamClient)
	return client
}

// metricsOf 取回本包的指标接缝。
func (h *Handler) metricsOf(*runtime.RuntimeResources) MetricsSink { return h.metrics }
