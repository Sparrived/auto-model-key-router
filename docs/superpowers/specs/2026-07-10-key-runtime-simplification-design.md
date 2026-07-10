# Key 运行态简化设计

## 目标

消除自动永久禁用，避免 Codex 高频重试在短时间内将 Key 踢出路由；同时收敛运行态边界，使配置、资源、Key 健康和端点能力各自只有一个职责。

## 状态语义

- 请求失败只能产生临时冷却，不能自动设置禁用。
- 429 优先使用 `Retry-After`；其他可重试错误使用递增且有上限的冷却。
- 任意成功请求立即清空连续失败与冷却。
- 冷却是软降级：有其他 Key 时跳过；所有 Key 都冷却时仍允许选择，避免服务完全不可用。
- Key 健康状态只存在于进程内，不公开查询、修改或持久化。
- 长期启停 Key 只使用配置中的 `enabled`。

## 运行态边界

- `AppRuntime` 是应用资源的唯一真相源，持有配置、Key 路由器、指标和 HTTP client。
- `app.state` 不再镜像 `config`、`key_pool`、`metrics`、`http_client` 四份资源。
- 配置热重载原子替换当前 `AppRuntime`；旧流式请求通过 lease 使用旧资源直到结束。
- 保留 generation/lease 仅用于旧 HTTP client 和 MetricsStore 的安全释放，不再提供 state 同步层。

## Key 状态拆分

- `KeyPool` 负责模型解析、路由与活跃请求计数。
- `KeyHealthStore` 只负责进程内失败次数和冷却。
- `EndpointCapabilityCache` 负责原生端点支持状态及其 TTL，并单独持久化。
- 删除 `KeyPool.reconfigure()`、`sync_runtime_from_state()`、`resources_from_state()` 和未使用的旧端点缓存。

## 健康恢复

- 删除后台 `/v1/models` 健康探测任务及 `upstream_health_check_interval` 的运行逻辑。
- 冷却到期自然恢复；成功请求清空失败状态。
- 保留旧配置字段解析兼容，但标记为弃用且不再启动后台任务。

## 兼容性

- `/health` 不再返回 `key_states`，Key `/state` 管理 API 被删除。
- TUI 不再显示或调控 Key 运行态，只管理配置中的 `enabled`。
- 原生端点能力使用独立的 `endpoint-capabilities.json` 和 `endpoint_capabilities_path`。
- 旧 `key_state_path` 作为 `endpoint_capabilities_path` 的读取兼容别名，保存配置时改写为新字段。

## 验证

- 高频连续失败不会自动禁用。
- 冷却递增并受上限约束。
- Key 健康状态不出现在文件、健康接口、管理 API 或 TUI 中。
- 热重载期间普通请求和流式请求继续使用一致资源快照。
- 管理 API、健康接口、代理路由和完整测试集通过。
