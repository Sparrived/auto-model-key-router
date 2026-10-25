package server

import (
	"log/slog"

	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/keypool"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// reload 是 _reload_config_if_changed 的移植（app.py:443-475）。
//
// 触发条件是**配置文件 mtime 变化**，而不是每次请求都读盘：这是参照实现的核心
// 设计——配置文件会被管理 API 与用户手工编辑器同时改写，靠 mtime 才能让两边都
// 生效，又不必每个请求都解析一遍 JSON。
//
// 四条必须照抄的语义：
//
//  1. **拿不到 mtime 就什么都不做**（app.py:446 的 `if not mtime`）：配置文件被
//     删除后，运行时保留最后一份可用配置，接口不会因为文件消失而 500。
//  2. **解析失败静默保留旧配置**（app.py:452-455 的 `except (OSError, ValueError):
//     return`）。这一点可观测：用户把配置改坏之后，管理 API 仍然能读到旧配置并
//     用新配置修正它。
//  3. **只有 request_timeout 变化才换 HTTP 客户端**（app.py:460-461），否则沿用
//     同一个 *http.Client——连接池必须被复用，否则每次热重载都会丢掉所有 keepalive
//     连接。Go 侧多一个条件：stream_first_byte_timeout 也被烘进了客户端
//     （见 newUpstreamClient），所以两个超时任一变化都必须重建。
//  4. **metrics_db_path 变化才换指标库**（app.py:462-463）。换库时旧库继续服务
//     到最后一个租约归还（RuntimeManager.closeUnused 负责关它）。
//
// 与参照实现的一处差异：
//
//   - app.py:469 的 `await state.runtime_manager.replace(...)` 在 Go 侧是同步的
//     （内部用互斥锁而不是 await），行为一致。
//
// 指标库打不开时本函数放弃本次重载并保留旧一代：参照实现会把这个异常抛给请求
// （变成 500），而 Go 侧的同一函数也被 api.Server.Reload 调用（那个接缝没有错误
// 返回），因此只能降级成「记录一条日志、下次请求再试」。mtime 未更新，所以下一个
// 请求会重试，与参照实现的「第二个请求再次失败」不同——这是**有意的取舍**，
// 触发条件是「metrics_db_path 被改成不可写路径」，不影响正常路径。
func (a *App) reload() {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	if a.configPath == "" {
		return
	}
	mtime := configMtime(a.configPath)
	if mtime.IsZero() || mtime.Equal(a.configMtime) {
		return
	}
	runtimeConfig, err := config.Load(a.configPath)
	if err != nil {
		return
	}

	current := a.manager.Current()
	if current == nil {
		return
	}
	old := current.Config
	client := current.HTTPClient
	store := current.Metrics

	if old.RequestTimeout != runtimeConfig.RequestTimeout ||
		old.StreamFirstByteTimeout != runtimeConfig.StreamFirstByteTimeout {
		client = newUpstreamClient(runtimeConfig)
	}
	if old.MetricsDBPath != runtimeConfig.MetricsDBPath {
		opened, openErr := metrics.Open(runtimeConfig.MetricsDBPath)
		if openErr != nil {
			slog.Warn("配置热重载：指标库打不开，保留旧配置",
				"path", runtimeConfig.MetricsDBPath, "error", openErr)
			return
		}
		store = newMetricsAdapter(opened)
		// app.py:468 的 `metrics.on_record = old_runtime.metrics.on_record`：把「写指标后
		// 唤醒广播」的回调带到新库上。少了这一步，换库之后写入不再产生 dirty 信号，
		// metrics_snapshot 会静默退化成 30 秒一次的空闲心跳（只有 /ws/events 能观察到，
		// 所以由 websocket_test.go 的 TestMetricsBroadcastSurvivesStoreSwap 钉住）。
		a.bindMetricsDirty(opened)
	}

	// 参照实现把 KeyPool 的构造放在 try 里（app.py:464-467），因为它会读端点探测
	// 缓存文件；Go 侧 keypool.New 不返回错误（缓存损坏时按空状态处理），所以这里
	// 没有对应的错误分支。
	pool := keypool.New(runtimeConfig,
		keypool.NewCapabilityStore(runtimeConfig.EndpointCapabilitiesPath), nil)
	// 冻结表不随配置重建（那是持久化状态），但新 pool 必须重新注入同一个：
	// SetQuotaFreezes **不会**从旧 pool 继承，漏了这一步热重载会让所有冻结失效。
	pool.SetQuotaFreezes(a.freezeStore)

	if err := a.manager.Replace(
		runtime.NewRuntimeResources(runtimeConfig, pool, store, client)); err != nil {
		return
	}
	// 用重新读到的 mtime 而不是上面那次 stat 的结果（app.py:472）：读盘期间文件可能
	// 又被改了一次，用旧 mtime 会让那次改动被漏掉。
	if refreshed := configMtime(a.configPath); !refreshed.IsZero() {
		a.configMtime = refreshed
	} else {
		a.configMtime = mtime
	}
	// app.py:473-475：换完之后（且**只有真的换了**）向订阅者广播一次 config_change。
	// 参照实现在调用点自己判了 `if event_bus.client_count > 0`，Go 侧把这个门禁收进
	// eventbus.Bus.BroadcastConfigChange 里——没人订阅时不广播，也**不构建帧**。
	a.eventBus.BroadcastConfigChange(a.broadcastContext())
	// 配置变了就尽快重评一次端到端（间隔、供应商、CPA 实例、冻结阈值都可能变了）。
	// 只是请求尽快评估：Trigger 非阻塞，后台循环会把同一时刻的多次触发合并成一次。
	if a.guard != nil {
		a.guard.Trigger()
	}
}
