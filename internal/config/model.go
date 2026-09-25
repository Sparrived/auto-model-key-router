package config

import (
	"crypto/hmac"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// KeyConfig 是模型下的一个可用 key（由 provider key + target 组合而成）。
type KeyConfig struct {
	Name string
	// APIKey 是真实密钥。
	APIKey  string
	BaseURL string
	Enabled bool
	// UpstreamRoutes 在 v4 里恒为空：见 RouterConfig 的说明。
	UpstreamRoutes map[string]string
	Provider       string
	UpstreamModel  string
}

// ModelConfig 是一个可路由的模型。
//
// 可调用名恰好是 ID 与 Aliases：两者都会出现在 /v1/models 里。target 的
// upstream_model 是**上游**叫法，只用于发给上游，不会成为本地可调用名。
type ModelConfig struct {
	ID              string
	Keys            []KeyConfig
	Aliases         []string
	RoutingMode     string
	ReasoningEffort string
	NativeFirst     bool
}

// ProviderKeyConfig 是 provider 下配置的一个 key。
type ProviderKeyConfig struct {
	Name    string
	APIKey  string
	Enabled bool
	// Capabilities 是探测缓存（模型列表、各路由可用性等）。
	Capabilities *canonical.Value
}

// ProviderConfig 是一个上游供应商。
type ProviderConfig struct {
	ID           string
	BaseURL      string
	Keys         []ProviderKeyConfig
	Routes       map[string]string
	Capabilities *canonical.Value
}

// RouteTarget 是 unified_model / task 的一个目标（模型 + 可选 key）。
type RouteTarget struct {
	Model string
	Key   string
}

// RoutePlan 是 primary + 可选 fallback。
type RoutePlan struct {
	Primary  RouteTarget
	Fallback *RouteTarget
}

// DefaultWorkspace 是默认工作空间的名称，对应配置顶层的 `tasks` 段。
//
// 它**不出现在**配置的 `workspaces` 里（那段装的是其它工作空间），只是给管理面与
// 调用方一个统一的名字：请求不带 X-AMKR-Workspace 头时解析到的就是它。这样
// 「工作空间」在运行时始终是一个具体名字，不必到处判断空串。
const DefaultWorkspace = "default"

// WorkspaceHeader 是调用方选择工作空间的请求头。
//
// 用请求头而不是「模型名前缀」或「URL 路径」：任务名是 model 字段的值，加前缀会
// 让它与真实模型名混在一个命名空间里（那样还得处理前缀与模型名的冲突）；路径则与
// 参照实现固定的 /v1/... 形状冲突。请求头是唯一既不动 body 也不动路径的位置。
const WorkspaceHeader = "X-AMKR-Workspace"

// NormalizeWorkspace 归一化工作空间名：去空白，空名落到默认工作空间。
//
// 运行时的规范化只做这一件事——不校验名字是否存在。不存在的空间自然查不到任何
// 任务，随后按普通模型名解析，与「不带这个头」的失败方式完全一致。
func NormalizeWorkspace(workspace string) string {
	if name := strings.TrimSpace(workspace); name != "" {
		return name
	}
	return DefaultWorkspace
}

// WorkspaceConfig 是一个命名工作空间的声明。
//
// 它**不只是**任务的容器：从有了面板 key 起，一个工作空间可以「有 key、没任务」地
// 独立存在（应用侧先建空间拿 key、再慢慢填任务）。这是对「空分组不存在」的一处
// 有意放宽，放宽的边界很窄——只有带 key 的分组才留得住，`{"teamB": {}}` 这种纯空
// 壳仍然会被清掉（见 WorkspaceNames 与 configops.writeWorkspaceTasks）。
type WorkspaceConfig struct {
	Name string
	// APIKey 是给嵌入方面板用的凭据；空串表示这个空间没有面板（调用方只能用
	// 本地 key 从管理面操作它）。
	//
	// 与 local_api_key 一样是**入站凭据**，因此不随 /api/config/export 迁移
	// （workspace 有自己的迁移通道，见 configops 的 WorkspaceBundle）。
	APIKey string
	// InferenceKey 是这个空间的**推理凭据**；空串表示没有。
	//
	// 为什么不复用 APIKey：面板 key 的存在形式是浏览器 URL fragment（`#k=`），
	// 是最容易泄漏的位置。让同一把 key 还能消耗上游额度，等于把「嵌进第三方后台
	// 的只读面板」升级成「能刷你的上游账单」。两把 key 的泄漏后果差一个数量级，
	// 因此刻意分开。
	//
	// 有了它，持有 key 的项目就能用自己的任务名调 /v1/*，而不必拿到 local_api_key
	// ——后者是完整管理权限（能读全部上游 key、能改全部配置）。
	InferenceKey string
	// Models 是本空间**允许使用**的模型名（真实 id 或别名）；空表示不限制。
	//
	// 为什么需要它：models 在配置里是全局的，只有 tasks 天然按空间隔离。若不加
	// 限制，一个只该用某个小模型的项目可以直呼任意真实模型名绕过任务路由，隔离
	// 就只剩任务那一层。运维侧因此要能按空间收窄可用的模型集合。
	//
	// 只影响**直呼真实模型名**这条路；任务名始终可用（任务自己固定了模型，它
	// 引用的模型就是该空间被授权使用的）。/v1/models 也按这份清单收窄。
	Models []string
}

// AccessKeyConfig 是一把**访问密钥**：分发给外部使用者的受限推理凭据。
//
// 它取代了原先的固定访客 key（`amkr-visitor`）：那把 key 是全局共享、权限由每个上游
// key 上的 allow_visitor 开关拼出来的，既不能一人一把，也不能按人收窄。访问密钥把
// 这两件事都变成显式配置——每把 key 一份清单，它能把请求发到哪些供应商与哪些模型。
type AccessKeyConfig struct {
	// ID 是配置里的键（`access_keys.<id>`），是更新与轮换时的稳定定位符。
	ID string
	// Name 是给人看的标识（WebUI 列表与日志用），不参与鉴权。
	Name string
	// Key 是密钥明文，鉴权时按恒定时间比较。
	Key string
	// Enabled 为假时该 key 立即失效，但保留配置（便于临时停用而不丢清单）。
	Enabled bool
	// Providers 是本 key 允许使用的供应商 ID 清单；空表示不限制。
	//
	// nil 与空切片**语义不同**（与 workspaces.models 同一约定）：nil = 配置里没写这个
	// 字段 = 不限制；非 nil 的空切片 = 显式写了 `[]` = 一个供应商都不许。
	Providers []string
	// Models 是本 key 允许使用的模型名（真实 ID 或别名）清单；空表示不限制。
	// nil 与空切片的区别同 Providers。
	Models []string
}

// AllowsProvider 报告本 key 是否可以使用某供应商。
func (a AccessKeyConfig) AllowsProvider(providerID string) bool {
	if a.Providers == nil {
		return true
	}
	for _, allowed := range a.Providers {
		if allowed == providerID {
			return true
		}
	}
	return false
}

// AllowsModel 报告本 key 是否可以使用某模型名（调用方传入的原始名字）。
//
// 比对发生在**别名解析之前**：调用方写什么名字就按什么名字授权。这既让清单里可以
// 写别名（运维更愿意写自己认得的名字），也让「同一模型换个写法」不会绕过清单——
// 因为只要清单限制了模型，未列出的写法一律拒绝。
func (a AccessKeyConfig) AllowsModel(modelName string) bool {
	if a.Models == nil {
		return true
	}
	for _, allowed := range a.Models {
		if allowed == modelName {
			return true
		}
	}
	return false
}

// TaskConfig 是任务名路由：model 传任务名时改用这里的模型与固定参数。
type TaskConfig struct {
	Name string
	// DisplayName 是给**人**看的中文显示名（WebUI 列表与编辑页用）。
	//
	// 它只影响展示，不参与路由：调用方仍然传 Name。留空表示没有取名，界面回落到
	// Name。放在配置里而不是浏览器本地，是为了让别名随导出/导入一起走——它描述的
	// 是「这个任务是什么」，属于配置本身，不是某台机器的视图偏好。
	DisplayName string
	// Workspace 是任务所属的工作空间；顶层 tasks 的任务归属 DefaultWorkspace。
	//
	// 任务名只在工作空间内唯一，因此运行时的查表键是 (Workspace, Name) 两元组。
	Workspace string
	// Model 是首选模型 ID；**空串表示尚未指定模型**，这样的任务可以正常存在与
	// 编辑，但被请求时明确报 404（见 proxy 的「尚未指定模型」分支），而不是静默
	// 落到别的模型上。
	Model         string
	FallbackModel string
	Params        *canonical.Value
}

// Plan 把任务转成路由计划。
//
// Model 为空时 Primary.Model 也是空串：调用方据此判定「任务还没指定模型」并给出
// 明确错误，而不是把它当成一个真实的模型名去查表。
func (t TaskConfig) Plan() RoutePlan {
	plan := RoutePlan{Primary: RouteTarget{Model: t.Model}}
	if t.FallbackModel != "" {
		plan.Fallback = &RouteTarget{Model: t.FallbackModel}
	}
	return plan
}

// UnifiedModelConfig 是 unified-model 伪模型的三个路由计划。
type UnifiedModelConfig struct {
	Default RoutePlan
	Image   *RoutePlan
	// Embeddings 对应 /v1/embeddings 的默认模型。
	Embeddings *RoutePlan
}

// RouterConfig 是解析并校验后的完整运行配置。
type RouterConfig struct {
	Host                     string
	Port                     int
	RequestTimeout           float64
	MaxRetries               int
	KeyFailureThreshold      int
	KeyCooldownSeconds       float64
	EndpointCapabilitiesPath string
	MetricsDBPath            string
	LogFilePath              string
	LocalAPIKey              string
	Models                   []ModelConfig
	StreamFirstByteTimeout   float64
	StreamIdleTimeout        float64
	Providers                []ProviderConfig
	// UpstreamRoutes 是「上游 URL -> {模式: 路径}」。
	//
	// 重要：它**只**由 providers[].routes 汇总而来。配置文件顶层的
	// upstream_routes 与 provider key 级的 upstream_routes 都会被忽略——
	// 这是参照实现的历史行为（config.py:877 从空字典起步，而 KeyConfig 在
	// config.py:963 构造时未传 upstream_routes），刻意保留：改动它会让已有配置的
	// 上游请求路径发生变化。详见 internal/config/doc.go 的说明。
	UpstreamRoutes map[string]map[string]string
	UnifiedModel   *UnifiedModelConfig
	// Tasks 是**扁平**的全部任务，每项带自己的 Workspace（顶层 `tasks` 段的任务
	// 归属 DefaultWorkspace）。工作空间名从这份列表反推，见 WorkspaceNames。
	Tasks []TaskConfig
	// Workspaces 只装**声明了凭据**的命名工作空间。
	//
	// 不带 key 的空间不需要在这里留记录：它们完全由 Tasks 反映（见 WorkspaceNames）。
	// 只收带 key 的那些，是为了让这份列表恰好等于「需要参与鉴权匹配的空间」，不多
	// 不少——否则每次鉴权都要把一堆没有 key 的名字过一遍。
	Workspaces []WorkspaceConfig
	// AccessKeys 是分发给外部使用者的受限推理凭据。
	//
	// 它取代了原先固定的访客 key：每把 key 自带供应商与模型清单，因此「谁能用哪些
	// 上游」是一份显式、可审计的配置，而不是所有 key 共用一组 allow_visitor 开关。
	AccessKeys   []AccessKeyConfig
	WebUIEnabled bool
	OpsEnabled   bool
	// ReasoningEffortByModel 是 model.id -> reasoning_effort（仅非空项）。
	ReasoningEffortByModel map[string]string
}

// FromDict 解析并校验原始配置。
//
// 对齐 config.py:869：先执行迁移，因此调用方可以直接传入任意版本的配置。
func FromDict(raw *canonical.Value) (*RouterConfig, error) {
	migrated, err := MigrateConfigData(raw)
	if err != nil {
		return nil, err
	}
	version, err := configVersionOf(migrated)
	if err != nil {
		return nil, err
	}
	if version != CONFIG_VERSION {
		return nil, errf("配置文件必须是 config_version %d", CONFIG_VERSION)
	}
	return fromMigrated(migrated)
}

// fromMigrated 解析已经迁移到 v4 的配置。
func fromMigrated(raw *canonical.Value) (*RouterConfig, error) {
	var err error
	config := &RouterConfig{
		UpstreamRoutes:         map[string]map[string]string{},
		ReasoningEffortByModel: map[string]string{},
	}
	// host 走 ``str(raw.get("host", "127.0.0.1"))``：键存在时即便内容是空串也
	// 照用（不回落默认值），与 Python 的 dict.get(key, default) 语义一致。
	config.Host = "127.0.0.1"
	if rawHost, present := raw.LookupOK("host"); present {
		config.Host = rawHost.PyStr()
	}

	if config.Port, err = intOr(raw.Lookup("port"), 8000); err != nil {
		return nil, err
	}
	if config.RequestTimeout, err = floatOr(raw.Lookup("request_timeout"), 60); err != nil {
		return nil, err
	}
	if config.StreamFirstByteTimeout, err = floatOr(raw.Lookup("stream_first_byte_timeout"), 60); err != nil {
		return nil, err
	}
	if config.StreamIdleTimeout, err = floatOr(raw.Lookup("stream_idle_timeout"), 60); err != nil {
		return nil, err
	}
	if config.MaxRetries, err = intOr(raw.Lookup("max_retries"), 2); err != nil {
		return nil, err
	}
	threshold, err := intOr(raw.Lookup("key_failure_threshold"), 2)
	if err != nil {
		return nil, err
	}
	if threshold < 1 {
		threshold = 1
	}
	config.KeyFailureThreshold = threshold

	cooldown, err := floatOr(raw.Lookup("key_cooldown_seconds"), 60)
	if err != nil {
		return nil, err
	}
	if cooldown < 0 {
		cooldown = 0
	}
	config.KeyCooldownSeconds = cooldown

	config.EndpointCapabilitiesPath = pathOr(raw, "endpoint_capabilities_path", "key_state_path", "")
	if config.EndpointCapabilitiesPath == "" {
		path, err := DefaultEndpointCapabilitiesPath()
		if err != nil {
			return nil, err
		}
		config.EndpointCapabilitiesPath = path
	}
	config.MetricsDBPath = pathOr(raw, "metrics_db_path", "", "")
	if config.MetricsDBPath == "" {
		path, err := DefaultMetricsDBPath()
		if err != nil {
			return nil, err
		}
		config.MetricsDBPath = path
	}
	config.LogFilePath = pathOr(raw, "log_file_path", "", "")
	if config.LogFilePath == "" {
		path, err := DefaultLogFilePath()
		if err != nil {
			return nil, err
		}
		config.LogFilePath = path
	}
	config.LocalAPIKey = stringOr(raw.Lookup("local_api_key"), "")
	config.WebUIEnabled = boolOr(raw.Lookup("webui_enabled"), false)
	config.OpsEnabled = boolOr(raw.Lookup("ops_enabled"), true)

	providers, providerKeys, err := parseProviders(raw)
	if err != nil {
		return nil, err
	}
	config.Providers = providers

	models, err := parseModels(raw, providerKeys)
	if err != nil {
		return nil, err
	}
	config.Models = models

	if config.UnifiedModel, err = parseUnifiedModel(raw, models); err != nil {
		return nil, err
	}
	tasks, workspaces, err := parseTasks(raw, models)
	if err != nil {
		return nil, err
	}
	config.Tasks = tasks
	config.Workspaces = workspaces
	accessKeys, err := parseAccessKeys(raw, models)
	if err != nil {
		return nil, err
	}
	config.AccessKeys = accessKeys

	// 顶层 upstream_routes 刻意不参与：参照实现从空字典起步。
	for _, provider := range config.Providers {
		if err := mergeUpstreamRoutesForURL(config.UpstreamRoutes, provider.BaseURL, provider.Routes); err != nil {
			return nil, err
		}
	}
	for _, model := range config.Models {
		for _, key := range model.Keys {
			if err := mergeUpstreamRoutesForURL(config.UpstreamRoutes, key.BaseURL, key.UpstreamRoutes); err != nil {
				return nil, err
			}
		}
	}

	for _, model := range config.Models {
		if model.ReasoningEffort != "" {
			config.ReasoningEffortByModel[model.ID] = model.ReasoningEffort
		}
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

// providerKeyRef 是 (providerID, keyName) 索引的条目。
//
// 带上 BaseURL 是因为构造模型 KeyConfig 时需要 provider 的 base_url，而 key 自身
// 不存储它（参照实现是保留 provider 对象引用，Go 侧用值索引更清晰）。
type providerKeyRef struct {
	Key     ProviderKeyConfig
	BaseURL string
}

// parseProviders 解析 providers 段，并返回 (providerID, keyName) -> key 的索引。
func parseProviders(raw *canonical.Value) ([]ProviderConfig, map[[2]string]providerKeyRef, error) {
	var providers []ProviderConfig
	providerKeys := map[[2]string]providerKeyRef{}

	rawProviders := raw.Lookup("providers")
	if !rawProviders.IsObject() {
		return providers, providerKeys, nil
	}
	defaultBaseURL := stringOr(raw.Lookup("default_base_url"), "")
	if defaultBaseURL == "" {
		defaultBaseURL = "https://api.openai.com"
	}

	for _, providerID := range rawProviders.Obj.Keys() {
		provider := rawProviders.Lookup(providerID)
		if !provider.IsObject() {
			continue
		}
		var keys []ProviderKeyConfig
		rawKeys := provider.Lookup("keys")
		if !rawKeys.IsObject() {
			rawKeys = canonical.NewObject()
		}
		for _, keyName := range rawKeys.Obj.Keys() {
			key := rawKeys.Lookup(keyName)
			if !key.IsObject() {
				return nil, nil, errf("供应商 %s 的 key %s 必须是对象", providerID, keyName)
			}
			// Python 用 key["api_key"] 直接索引：缺失即 KeyError，不是空串。
			rawAPIKey, present := key.LookupOK("api_key")
			if !present {
				return nil, nil, errInternal("'api_key'")
			}
			var capabilities *canonical.Value
			if caps := key.Lookup("capabilities"); caps.IsObject() {
				capabilities = caps.Clone()
			}
			keys = append(keys, ProviderKeyConfig{
				Name:         keyName,
				APIKey:       rawAPIKey.PyStr(),
				Enabled:      boolOr(key.Lookup("enabled"), true),
				Capabilities: capabilities,
			})
		}

		baseURL := stringOr(provider.Lookup("base_url"), "")
		if baseURL == "" {
			baseURL = defaultBaseURL
		}
		normalizedBaseURL, err := NormalizeUpstreamBaseURL(canonical.NewString(baseURL))
		if err != nil {
			return nil, nil, err
		}
		routes, err := NormalizeUpstreamRoutes(provider.Lookup("routes"))
		if err != nil {
			return nil, nil, err
		}
		var providerCapabilities *canonical.Value
		if caps := provider.Lookup("capabilities"); caps.IsObject() {
			providerCapabilities = caps.Clone()
		}
		providerConfig := ProviderConfig{
			ID:           providerID,
			BaseURL:      normalizedBaseURL,
			Keys:         keys,
			Routes:       routes,
			Capabilities: providerCapabilities,
		}
		providers = append(providers, providerConfig)
		for _, keyConfig := range providerConfig.Keys {
			providerKeys[[2]string{providerConfig.ID, keyConfig.Name}] = providerKeyRef{
				Key:     keyConfig,
				BaseURL: providerConfig.BaseURL,
			}
		}
	}
	return providers, providerKeys, nil
}

// parseModels 解析 models 段。
func parseModels(raw *canonical.Value, providerKeys map[[2]string]providerKeyRef) ([]ModelConfig, error) {
	rawModels := raw.Lookup("models")
	if rawModels == nil {
		rawModels = canonical.NewObject()
	}
	if !rawModels.IsObject() {
		return nil, errf("config_version 4 的 models 必须是对象")
	}
	defaultRoutingMode := stringOr(raw.Lookup("routing_mode"), "")
	if defaultRoutingMode == "" {
		defaultRoutingMode = "round_robin"
	}

	var models []ModelConfig
	for _, rawModelID := range rawModels.Obj.Keys() {
		model := rawModels.Lookup(rawModelID)
		if !model.IsObject() {
			model = canonical.NewObject()
		}

		var keys []KeyConfig
		usedKeyNames := map[string]bool{}
		// modelKeyName 复刻 config.py:934 的去重闭包：同名时先加 provider 前缀，
		// 仍冲突则追加 -2、-3…… 该状态在整个模型的 targets 间共享。
		modelKeyName := func(baseName, qualifier string) string {
			name := baseName
			if !usedKeyNames[name] {
				usedKeyNames[name] = true
				return name
			}
			qualified := baseName
			if qualifier != "" {
				qualified = qualifier + "-" + baseName
			}
			name = qualified
			suffix := 2
			for usedKeyNames[name] {
				name = qualified + "-" + itoa(suffix)
				suffix++
			}
			usedKeyNames[name] = true
			return name
		}

		for _, target := range model.Lookup("targets").Items() {
			if !target.IsObject() {
				continue
			}
			providerID := strings.TrimSpace(target.Lookup("provider").StringValue())
			keyName := strings.TrimSpace(target.Lookup("key").StringValue())
			upstreamModel := strings.TrimSpace(target.Lookup("upstream_model").StringValue())
			if upstreamModel == "" {
				upstreamModel = rawModelID
			}
			targetEnabled := boolOr(target.Lookup("enabled"), true)

			providerKey, found := providerKeys[[2]string{providerID, keyName}]
			if !found {
				return nil, errf("模型 %s 引用了供应商 %s 不存在的 key: %s", rawModelID, providerID, keyName)
			}
			baseName := strings.TrimSpace(target.Lookup("name").StringValue())
			if baseName == "" {
				baseName = providerKey.Key.Name
			}
			keys = append(keys, KeyConfig{
				Name:    modelKeyName(baseName, providerID),
				APIKey:  providerKey.Key.APIKey,
				BaseURL: providerKey.BaseURL,
				// target 的 enabled 与 provider key 的 enabled 是「与」关系：
				// 任一方禁用该 key 都不可用。
				Enabled:       providerKey.Key.Enabled && targetEnabled,
				Provider:      providerID,
				UpstreamModel: upstreamModel,
				// 参照实现在此未传 upstream_routes（config.py:963），故恒为空。
				UpstreamRoutes: map[string]string{},
			})
		}

		aliases := []string{}
		for _, alias := range model.Lookup("aliases").Items() {
			if rendered := alias.PyStr(); rendered != "" {
				aliases = append(aliases, rendered)
			}
		}
		routingMode := strings.TrimSpace(model.Lookup("routing_mode").StringValue())
		if routingMode == "" {
			routingMode = defaultRoutingMode
		}
		reasoningEffort := strings.TrimSpace(model.Lookup("reasoning_effort").StringValue())
		if reasoningEffort == "default" || reasoningEffort == "downstream" {
			reasoningEffort = ""
		}
		modelID := strings.TrimSpace(model.Lookup("id").StringValue())
		if modelID == "" {
			modelID = rawModelID
		}

		models = append(models, ModelConfig{
			ID:              modelID,
			Keys:            keys,
			Aliases:         aliases,
			RoutingMode:     routingMode,
			ReasoningEffort: reasoningEffort,
			NativeFirst:     boolOr(model.Lookup("native_first"), true),
		})
	}
	return models, nil
}

// parseUnifiedModel 解析 unified_model 段。
func parseUnifiedModel(raw *canonical.Value, models []ModelConfig) (*UnifiedModelConfig, error) {
	rawUnified := raw.Lookup("unified_model")
	if rawUnified == nil || rawUnified.IsNull() {
		return nil, nil
	}
	if !rawUnified.IsObject() {
		return nil, errf("unified_model 必须是对象")
	}
	idsByName := modelIDsByName(models)

	parseTarget := func(value *canonical.Value, fieldName string) (RouteTarget, error) {
		if !value.IsObject() {
			return RouteTarget{}, errf("%s 必须是对象", fieldName)
		}
		modelName := strings.TrimSpace(value.Lookup("model").StringValue())
		modelID, found := idsByName[modelName]
		if !found {
			return RouteTarget{}, errf("%s 引用了未配置的模型: %s", fieldName, modelName)
		}
		return RouteTarget{
			Model: modelID,
			Key:   strings.TrimSpace(value.Lookup("key").StringValue()),
		}, nil
	}
	parsePlan := func(value *canonical.Value, fieldName string) (*RoutePlan, error) {
		if !value.IsObject() {
			return nil, errf("%s 必须是对象", fieldName)
		}
		primary, err := parseTarget(value.Lookup("primary"), fieldName+".primary")
		if err != nil {
			return nil, err
		}
		plan := &RoutePlan{Primary: primary}
		if rawFallback, present := value.LookupOK("fallback"); present && !rawFallback.IsNull() {
			fallback, err := parseTarget(rawFallback, fieldName+".fallback")
			if err != nil {
				return nil, err
			}
			plan.Fallback = &fallback
		}
		return plan, nil
	}

	defaultPlan, err := parsePlan(rawUnified.Lookup("default"), "unified_model.default")
	if err != nil {
		return nil, err
	}
	unified := &UnifiedModelConfig{Default: *defaultPlan}
	if rawImage, present := rawUnified.LookupOK("image"); present && !rawImage.IsNull() {
		if unified.Image, err = parsePlan(rawImage, "unified_model.image"); err != nil {
			return nil, err
		}
	}
	if rawEmbeddings, present := rawUnified.LookupOK("embeddings"); present && !rawEmbeddings.IsNull() {
		if unified.Embeddings, err = parsePlan(rawEmbeddings, "unified_model.embeddings"); err != nil {
			return nil, err
		}
	}
	return unified, nil
}

// parseTasks 解析顶层的 tasks 段（默认工作空间）与可选的 workspaces 段。
//
// 对齐 config.py:869 的 tasks 语义，并在其上叠加工作空间：顶层 `tasks` 就是
// DefaultWorkspace，`workspaces.<名字>.tasks` 是其余工作空间。返回的任务是**扁平**
// 的一份列表，每项带自己的 Workspace，方便运行时按 (工作空间, 任务名) 查表。
//
// 第二个返回值只收**带 api_key** 的命名工作空间（见 RouterConfig.Workspaces）：
// 面板 key 必须能在没有任务时也把空间留住，因此它不能像任务那样反推。
func parseTasks(raw *canonical.Value, models []ModelConfig) ([]TaskConfig, []WorkspaceConfig, error) {
	idsByName := modelIDsByName(models)

	tasks, err := parseTaskGroup(raw.Lookup("tasks"), DefaultWorkspace, "tasks", idsByName)
	if err != nil {
		return nil, nil, err
	}

	rawWorkspaces := raw.Lookup("workspaces")
	if rawWorkspaces == nil || rawWorkspaces.IsNull() {
		return tasks, nil, nil
	}
	if !rawWorkspaces.IsObject() {
		return nil, nil, errf("workspaces 必须是对象")
	}

	var workspaces []WorkspaceConfig
	seen := map[string]bool{DefaultWorkspace: true}
	for _, rawName := range rawWorkspaces.Obj.Keys() {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, nil, errf("工作空间名不能为空")
		}
		if seen[name] {
			return nil, nil, errf("工作空间名重复: %s", name)
		}
		seen[name] = true
		workspace := rawWorkspaces.Lookup(rawName)
		if !workspace.IsObject() {
			return nil, nil, errf("工作空间 %s 必须是对象", name)
		}
		group, err := parseTaskGroup(workspace.Lookup("tasks"), name, "workspaces."+name+".tasks", idsByName)
		if err != nil {
			return nil, nil, err
		}
		tasks = append(tasks, group...)
		apiKey := strings.TrimSpace(workspace.Lookup("api_key").StringValue())
		inferenceKey := strings.TrimSpace(workspace.Lookup("inference_key").StringValue())
		models, err := parseWorkspaceModels(workspace.Lookup("models"), name, idsByName)
		if err != nil {
			return nil, nil, err
		}
		// 留下这个分组的条件：有凭据，或者声明了模型清单。
		//
		// 只带 models 的分组**必须留**：最常见的情形正是「一个已经有任务的空间（靠任务
		// 反推即存在）被运维加上了 models 限制」，此时它两把 key 都没有。若在这里丢掉，
		// models 会在热重载后静默消失——限制看起来配了，实际没生效。
		//
		// 三者皆无的空壳仍然不留，与既有「空分组不进配置」一致。
		if apiKey != "" || inferenceKey != "" || models != nil {
			workspaces = append(workspaces, WorkspaceConfig{
				Name:         name,
				APIKey:       apiKey,
				InferenceKey: inferenceKey,
				Models:       models,
			})
		}
	}
	return tasks, workspaces, nil
}

// parseWorkspaceModels 解析 `workspaces.<空间>.models`：本空间允许直呼的模型名清单。
//
// 逐个校验名字确实指向一个已配置的模型（id 或别名）。写错一个名字就让整个空间
// 静默少一个可用模型，排查成本远高于在这里直接报错——错误文本带上空间名与字段
// 路径，与 tasks 的引用校验保持同一种措辞风格。
func parseWorkspaceModels(raw *canonical.Value, workspace string, idsByName map[string]string) ([]string, error) {
	if raw == nil || raw.IsNull() {
		return nil, nil
	}
	if !raw.IsArray() {
		return nil, errf("workspaces.%s.models 必须是数组", workspace)
	}
	models := make([]string, 0, len(raw.Arr))
	for index, item := range raw.Arr {
		name := strings.TrimSpace(item.StringValue())
		if name == "" {
			return nil, errf("workspaces.%s.models[%d] 不能为空", workspace, index)
		}
		if _, ok := idsByName[name]; !ok {
			return nil, errf("workspaces.%s.models[%d] 引用了未配置的模型: %s", workspace, index, name)
		}
		models = append(models, name)
	}
	return models, nil
}

// parseTaskGroup 解析一段 `{任务名: {...}}`，workspace 是它归属的工作空间。
//
// prefix 只影响错误文本里的字段路径：默认工作空间保持 `tasks.<名字>.model` 这一
// 既有措辞（对外契约，不得改写），命名工作空间用 `workspaces.<空间>.tasks.<名字>.model`。
func parseTaskGroup(
	rawTasks *canonical.Value,
	workspace, prefix string,
	idsByName map[string]string,
) ([]TaskConfig, error) {
	if rawTasks == nil || rawTasks.IsNull() {
		return nil, nil
	}
	if !rawTasks.IsObject() {
		if prefix == "tasks" {
			return nil, errf("tasks 必须是对象")
		}
		return nil, errf("%s 必须是对象", prefix)
	}

	var tasks []TaskConfig
	for _, rawTaskName := range rawTasks.Obj.Keys() {
		task := rawTasks.Lookup(rawTaskName)
		taskName := strings.TrimSpace(rawTaskName)
		if taskName == "" {
			return nil, errf("任务名不能为空")
		}
		if !task.IsObject() {
			return nil, errf("任务 %s 必须是对象", taskName)
		}
		resolve := func(value *canonical.Value, fieldName string) (string, error) {
			modelName := strings.TrimSpace(value.StringValue())
			modelID, found := idsByName[modelName]
			if !found {
				return "", errf("%s 引用了未配置的模型: %s", fieldName, modelName)
			}
			return modelID, nil
		}

		// model 可省略：任务可以先建出来占位（例如先把名字与固定参数定下来，
		// 模型稍后再选）。缺失、null 与空白串都表示「尚未指定」，请求它时明确报错
		// 而不是静默落到别的模型上（见 proxy 的「尚未指定模型」分支）。
		//
		// 填了却引用了未配置的模型仍然报错——那是写错了，不是还没填；否则错字会
		// 静默退化成一个空任务。
		modelID := ""
		if rawModel, present := task.LookupOK("model"); present && !rawModel.IsNull() {
			if strings.TrimSpace(rawModel.StringValue()) != "" {
				resolvedModel, resolveErr := resolve(rawModel, prefix+"."+taskName+".model")
				if resolveErr != nil {
					return nil, resolveErr
				}
				modelID = resolvedModel
			}
		}
		fallbackModel := ""
		if rawFallback, present := task.LookupOK("fallback_model"); present && !rawFallback.IsNull() {
			resolvedFallback, resolveErr := resolve(rawFallback, prefix+"."+taskName+".fallback_model")
			if resolveErr != nil {
				return nil, resolveErr
			}
			fallbackModel = resolvedFallback
		}
		params, err := NormalizeTaskParams(task.Lookup("params"), taskName)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, TaskConfig{
			Name:          taskName,
			DisplayName:   strings.TrimSpace(task.Lookup("display_name").StringValue()),
			Workspace:     workspace,
			Model:         modelID,
			FallbackModel: fallbackModel,
			Params:        params,
		})
	}
	return tasks, nil
}

// modelIDsByName 建立「模型 id 或别名 -> 真实 id」的映射。
func modelIDsByName(models []ModelConfig) map[string]string {
	out := map[string]string{}
	for _, model := range models {
		out[model.ID] = model.ID
		for _, alias := range model.Aliases {
			out[alias] = model.ID
		}
	}
	return out
}

// parseAccessKeys 解析顶层的 access_keys 段。
//
// 形状是 `{key_id: {name?, key, enabled?, providers?, models?}}`——用对象而不是数组，
// 与 providers/models/workspaces 一致：key_id 是更新与轮换时的稳定定位符，而数组元素
// 没有稳定身份（改个名就得靠下标，那是拿位置当 ID）。
//
// providers 与 models 都在这里逐个校验引用的目标确实存在。写错一个名字就让某把已经
// 分发出去的 key 静默少一项权限，排查要同时翻配置与调用方两侧；宁可在这里直接报错，
// 错误文本带上 key_id 与字段路径。
func parseAccessKeys(raw *canonical.Value, models []ModelConfig) ([]AccessKeyConfig, error) {
	rawKeys := raw.Lookup("access_keys")
	if rawKeys == nil || rawKeys.IsNull() {
		return nil, nil
	}
	if !rawKeys.IsObject() {
		return nil, errf("access_keys 必须是对象")
	}
	idsByName := modelIDsByName(models)
	providerIDs := map[string]bool{}
	if rawProviders := raw.Lookup("providers"); rawProviders.IsObject() {
		for _, providerID := range rawProviders.Obj.Keys() {
			providerIDs[providerID] = true
		}
	}

	var accessKeys []AccessKeyConfig
	for _, rawID := range rawKeys.Obj.Keys() {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, errf("访问密钥的 key_id 不能为空")
		}
		entry := rawKeys.Lookup(rawID)
		if !entry.IsObject() {
			return nil, errf("访问密钥 %s 必须是对象", id)
		}
		secret, present := entry.LookupOK("key")
		if !present {
			return nil, errInternal("'key'")
		}
		config := AccessKeyConfig{
			ID:      id,
			Name:    strings.TrimSpace(entry.Lookup("name").StringValue()),
			Key:     secret.PyStr(),
			Enabled: boolOr(entry.Lookup("enabled"), true),
		}
		if config.Name == "" {
			config.Name = id
		}
		providers, err := parseAccessKeyProviders(entry.Lookup("providers"), id, providerIDs)
		if err != nil {
			return nil, err
		}
		config.Providers = providers
		allowedModels, err := parseAccessKeyModels(entry.Lookup("models"), id, idsByName)
		if err != nil {
			return nil, err
		}
		config.Models = allowedModels
		accessKeys = append(accessKeys, config)
	}
	return accessKeys, nil
}

// parseAccessKeyProviders 解析 `access_keys.<id>.providers`。
//
// 与 models 一样用 nil 表示「没有这个字段」（不限制）——空数组是「一个都不许」。
func parseAccessKeyProviders(raw *canonical.Value, keyID string, providerIDs map[string]bool) ([]string, error) {
	if raw == nil || raw.IsNull() {
		return nil, nil
	}
	if !raw.IsArray() {
		return nil, errf("access_keys.%s.providers 必须是数组", keyID)
	}
	out := make([]string, 0, len(raw.Arr))
	for index, item := range raw.Arr {
		providerID := strings.TrimSpace(item.StringValue())
		if providerID == "" {
			return nil, errf("access_keys.%s.providers[%d] 不能为空", keyID, index)
		}
		if !providerIDs[providerID] {
			return nil, errf("access_keys.%s.providers[%d] 引用了未配置的供应商: %s", keyID, index, providerID)
		}
		out = append(out, providerID)
	}
	return out, nil
}

// parseAccessKeyModels 解析 `access_keys.<id>.models`。
//
// 允许写真实 ID 或别名（与 workspaces.models 同一口径），因为调用方就是用这些名字
// 请求的，清单里写别名更贴合分发时的说法。这里只做存在性校验，不走别名归一——归一
// 发生在运行时（keypool），配置保留调用方写的原样，编辑界面上回显的才是他自己写的东西。
func parseAccessKeyModels(raw *canonical.Value, keyID string, idsByName map[string]string) ([]string, error) {
	if raw == nil || raw.IsNull() {
		return nil, nil
	}
	if !raw.IsArray() {
		return nil, errf("access_keys.%s.models 必须是数组", keyID)
	}
	out := make([]string, 0, len(raw.Arr))
	for index, item := range raw.Arr {
		name := strings.TrimSpace(item.StringValue())
		if name == "" {
			return nil, errf("access_keys.%s.models[%d] 不能为空", keyID, index)
		}
		if _, ok := idsByName[name]; !ok {
			return nil, errf("access_keys.%s.models[%d] 引用了未配置的模型: %s", keyID, index, name)
		}
		out = append(out, name)
	}
	return out, nil
}

// AccessKeyFor 按密钥找出访问密钥，没有则返回 nil。
//
// 比较用恒定时间（与 WorkspaceForInferenceKey 同一理由：避免按字节提前返回泄漏 key
// 内容）。空 key 一律不匹配。
//
// 禁用的 key 也参与匹配并由调用方判 enabled：把它当成「不存在」会让「停用」与「删除」
// 在错误文本上无法区分，而这两件事对调用方的含义完全不同（停用是可以恢复的）。
func (c *RouterConfig) AccessKeyFor(apiKey string) *AccessKeyConfig {
	if apiKey == "" {
		return nil
	}
	for i := range c.AccessKeys {
		if hmac.Equal([]byte(apiKey), []byte(c.AccessKeys[i].Key)) {
			return &c.AccessKeys[i]
		}
	}
	return nil
}

// Validate 校验配置的自洽性。
//
// 新增检查请追加在**末尾**：既有检查的顺序与措辞是对外契约（见 doc.go）。
func (c *RouterConfig) Validate() error {
	if c.StreamFirstByteTimeout <= 0 {
		return errf("stream_first_byte_timeout 必须大于 0")
	}
	if c.StreamIdleTimeout <= 0 {
		return errf("stream_idle_timeout 必须大于 0")
	}

	// 工作空间面板 key 是**入站凭据**，因此要满足与 local_api_key 同一组底线：不能与
	// 本地 key 相同。
	//
	// 与 local_api_key 相同是必须挡的：本地 key 给的是全量权限（含配置与 /v1 代理），
	// 若某个空间的面板 key 恰好等于它，那么「嵌出去的 key」与「主凭据」就是同一个，
	// 一旦嵌进第三方页面就等于交出了整个实例。这不可能是有意为之。
	//
	// 推理 key 适用同一组底线，且**两种 key 共用一张占用表**：它们的判定发生在不同
	// 调用点（面板面 vs /v1 面），若允许同一个字符串两处都命中，同一个 key 会在一条
	// 路径上是面板、另一条上是推理，权限边界取决于走到哪条路由——这正是要避免的。
	//
	// 访问密钥（AccessKeys）同样并入这张表：它也走 /v1 面，与推理 key 的判定彼此独立，
	// 撞车会让同一把 key 的权限取决于先命中哪张清单。
	keyOwner := map[string]string{}
	// ownerLabel 是同一张表的可读描述，只用于跨类型冲突时的错误文本——既有那三条
	// 工作空间错误（`工作空间 a 的 api_key 不能…` / `工作空间 a 与 b 的 api_key 重复`）
	// 因此逐字不变。kind 区分同一空间的两把 key。
	ownerLabel := map[string]string{}
	claim := func(secret, kind, name string) error {
		if secret == "" {
			return nil
		}
		if c.LocalAPIKey != "" && secret == c.LocalAPIKey {
			return errf("工作空间 %s 的 %s 不能与 local_api_key 相同", name, kind)
		}
		// 两处同 key 时判定结果会取决于遍历顺序，等于随机给其中一个开门，
		// 因此直接判非法而不是「取第一个」。
		if previous, exists := keyOwner[secret]; exists {
			return errf("工作空间 %s 与 %s 的 %s 重复", previous, name, kind)
		}
		keyOwner[secret] = name
		ownerLabel[secret] = "工作空间 " + name + " 的 " + kind
		return nil
	}
	for _, workspace := range c.Workspaces {
		if err := claim(workspace.APIKey, "api_key", workspace.Name); err != nil {
			return err
		}
		if err := claim(workspace.InferenceKey, "inference_key", workspace.Name); err != nil {
			return err
		}
	}
	for i := range c.AccessKeys {
		accessKey := &c.AccessKeys[i]
		label := "访问密钥 " + accessKey.Name
		if accessKey.Key == "" {
			return errf("%s 的 key 不能为空", label)
		}
		if c.LocalAPIKey != "" && accessKey.Key == c.LocalAPIKey {
			return errf("%s 的 key 不能与 local_api_key 相同", label)
		}
		if previous, exists := ownerLabel[accessKey.Key]; exists {
			// previous 恒为「哪种资源的哪一把」的完整描述（工作空间那一圈写进去的），
			// 因此这里与被写入方用同一种句式，读者不必猜占用者是什么资源。
			return errf("%s 的 key 与%s 重复", label, previous)
		}
		// 只写 ownerLabel：访问密钥这一圈后面没有别的消费者了，keyOwner 里的名字
		// 没人读。（与工作空间那一圈不同，那边两处冲突都要报裸空间名。）
		ownerLabel[accessKey.Key] = label
	}

	modelNames := map[string]bool{}
	modelsByID := map[string]ModelConfig{}
	for _, model := range c.Models {
		if model.ID == "" {
			return errf("模型 id 不能为空")
		}
		if !isValidRoutingMode(model.RoutingMode) {
			return errf("模型 %s 的 routing_mode 必须是 priority、round_robin 或 only_first", model.ID)
		}
		if model.ReasoningEffort != "" && !containsString(reasoningEfforts, model.ReasoningEffort) {
			return errf("模型 %s 的 reasoning_effort 必须是 none、minimal、low、medium、high、xhigh 或 max", model.ID)
		}
		for _, name := range append([]string{model.ID}, model.Aliases...) {
			if modelNames[name] {
				return errf("模型名称重复: %s", name)
			}
			modelNames[name] = true
		}
		modelsByID[model.ID] = model

		keyNames := map[string]bool{}
		for _, key := range model.Keys {
			if key.Name == "" {
				return errf("模型 %s 存在空 key name", model.ID)
			}
			if keyNames[key.Name] {
				return errf("模型 %s 的 key name 重复: %s", model.ID, key.Name)
			}
			keyNames[key.Name] = true
			if key.APIKey == "" {
				return errf("模型 %s 存在空 api_key", model.ID)
			}
			if !hasHTTPScheme(key.BaseURL) {
				return errf("模型 %s 的 base_url %s 必须以 http:// 或 https:// 开头", model.ID, key.BaseURL)
			}
		}
	}

	for _, baseURL := range sortedKeys(c.UpstreamRoutes) {
		if !hasHTTPScheme(baseURL) {
			return errf("upstream_routes 的上游URL %s 必须以 http:// 或 https:// 开头", baseURL)
		}
		for _, routeMode := range sortedKeys(c.UpstreamRoutes[baseURL]) {
			if _, err := NormalizeUpstreamRoutePath(routeMode, canonical.NewString(c.UpstreamRoutes[baseURL][routeMode])); err != nil {
				return err
			}
		}
	}

	// 任务名必须能被唯一解析：与模型 ID/别名撞名会让 resolve_route 的语义变得
	// 取决于查表顺序，因此直接禁止。这条冲突检查是**全局**的——工作空间只隔离
	// 任务之间，不能用来遮蔽模型名。
	//
	// 任务名本身只在**同一个工作空间内**唯一：这正是工作空间的意义所在，
	// 两个空间各有一个 `summarize` 是合法配置。
	taskNames := map[[2]string]bool{}
	for i := range c.Tasks {
		task := &c.Tasks[i]
		if task.Name == "" {
			return errf("任务名不能为空")
		}
		// 零值 Workspace 视作默认工作空间：手工构造 RouterConfig 的调用方（测试、
		// 内部装配）不必知道工作空间的存在。
		if task.Workspace == "" {
			task.Workspace = DefaultWorkspace
		}
		key := [2]string{task.Workspace, task.Name}
		if taskNames[key] {
			return errf("任务名重复: %s", task.Name)
		}
		if modelNames[task.Name] {
			return errf("任务名与模型名称冲突: %s", task.Name)
		}
		if task.Name == UNIFIED_MODEL_ID {
			return errf("任务名不能使用保留名称: %s", UNIFIED_MODEL_ID)
		}
		taskNames[key] = true
		// 空 model 是「尚未指定模型」这个合法状态，不是引用错误：任务可以先建出来
		// 占位，请求它时由 proxy 明确报 404。只有非空才需要校验存在性——写了却
		// 写错必须是错误，否则错字会静默退化成一个空任务。
		if task.Model != "" {
			if _, found := modelsByID[task.Model]; !found {
				return errf("任务 %s 引用了未配置的模型: %s", task.Name, task.Model)
			}
		}
		if task.FallbackModel != "" {
			if _, found := modelsByID[task.FallbackModel]; !found {
				return errf("任务 %s 的备选引用了未配置的模型: %s", task.Name, task.FallbackModel)
			}
			if task.FallbackModel == task.Model {
				return errf("任务 %s 的首选和备选不能引用同一模型", task.Name)
			}
		}
	}

	if c.UnifiedModel == nil {
		return nil
	}
	if modelNames[UNIFIED_MODEL_ID] {
		return errf("启用 unified_model 时，模型 ID 和别名不能使用保留名称: %s", UNIFIED_MODEL_ID)
	}
	plans := []struct {
		name string
		plan *RoutePlan
	}{
		{"default", &c.UnifiedModel.Default},
		{"image", c.UnifiedModel.Image},
		{"embeddings", c.UnifiedModel.Embeddings},
	}
	for _, entry := range plans {
		if entry.plan == nil {
			continue
		}
		if entry.plan.Fallback != nil && entry.plan.Primary.Model == entry.plan.Fallback.Model {
			return errf("unified_model.%s 的 primary 和 fallback 不能引用同一模型", entry.name)
		}
		targets := []struct {
			name   string
			target *RouteTarget
		}{
			{"primary", &entry.plan.Primary},
			{"fallback", entry.plan.Fallback},
		}
		for _, target := range targets {
			if target.target == nil {
				continue
			}
			targetModel, found := modelsByID[target.target.Model]
			if !found {
				return errf("unified_model.%s.%s 引用了未配置的模型: %s", entry.name, target.name, target.target.Model)
			}
			if target.target.Key != "" && !targetModel.hasEnabledKey(target.target.Key) {
				return errf("模型 %s 未配置可用 key: %s", target.target.Model, target.target.Key)
			}
		}
	}
	return nil
}

// hasEnabledKey 报告模型是否存在指定名称且启用的 key。
func (m ModelConfig) hasEnabledKey(name string) bool {
	for _, key := range m.Keys {
		if key.Name == name && key.Enabled {
			return true
		}
	}
	return false
}

// ConfiguredModelID 返回模型名（id 或别名）对应的真实 id。
func (c *RouterConfig) ConfiguredModelID(modelName string) (string, bool) {
	for _, model := range c.Models {
		if modelName == model.ID || containsString(model.Aliases, modelName) {
			return model.ID, true
		}
	}
	return "", false
}

// TaskFor 在默认工作空间里按名称查找任务。
func (c *RouterConfig) TaskFor(name string) (TaskConfig, bool) {
	return c.TaskForWorkspace(DefaultWorkspace, name)
}

// TaskForWorkspace 在指定工作空间里按名称查找任务。
func (c *RouterConfig) TaskForWorkspace(workspace, name string) (TaskConfig, bool) {
	if workspace == "" {
		workspace = DefaultWorkspace
	}
	for _, task := range c.Tasks {
		if task.Name == name && task.Workspace == workspace {
			return task, true
		}
	}
	return TaskConfig{}, false
}

// WorkspaceNames 返回全部工作空间名：默认工作空间始终在首位，其余按「配置里出现的
// 顺序」——先是有任务的空间，再是只声明了 api_key 的空间。
//
// 有任务的空间刻意由任务反推而不是直接回放配置里的 `workspaces` 键：一个既没有任务
// 也没有 api_key 的分组既不可观测也没有意义（调用方按名字取不到任何东西，面板也进
// 不去）。手写的这种空分组因此不会出现在这里，与 configops 写回时「删空即删分组」
// 的语义一致——两边口径必须相同，否则界面上会列出一个删不掉的幽灵分组。
//
// 带 api_key 的空间是**例外**：它是应用侧先建空间、后填任务的落脚点，没有任务时也
// 必须存在（否则 key 换来的面板会指向一个不存在的空间）。这部分见 RouterConfig 的
// Workspaces 字段与 configops.writeWorkspaceTasks。
func (c *RouterConfig) WorkspaceNames() []string {
	names := []string{DefaultWorkspace}
	seen := map[string]bool{DefaultWorkspace: true}
	for _, task := range c.Tasks {
		workspace := task.Workspace
		if workspace == "" {
			workspace = DefaultWorkspace
		}
		if seen[workspace] {
			continue
		}
		seen[workspace] = true
		names = append(names, workspace)
	}
	for _, workspace := range c.Workspaces {
		if seen[workspace.Name] {
			continue
		}
		seen[workspace.Name] = true
		names = append(names, workspace.Name)
	}
	return names
}

// WorkspaceForAPIKey 找出持有该 key 的工作空间，没有则返回空串。
//
// 空 key 一律不匹配：没有面板的空间不该被空 key 命中（调用方随便发个空 Authorization
// 就能进别人的面板是荒谬的）。比较用恒定时间，与 auth 包同一理由——避免按字节提前
// 返回泄漏 key 内容。
//
// 同一个 key 配给两个空间是配置错误（见 Validate），因此这里不需要「取第一个」的
// 兜底语义：合法的配置最多命中一个。
func (c *RouterConfig) WorkspaceForAPIKey(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	for _, workspace := range c.Workspaces {
		if hmac.Equal([]byte(apiKey), []byte(workspace.APIKey)) {
			return workspace.Name
		}
	}
	return ""
}

// WorkspaceForInferenceKey 找出持有该推理凭据的工作空间，没有则返回空串。
//
// 与 WorkspaceForAPIKey **刻意分开**，而不是合成一个「任意凭据 -> 空间」的查表：
// 两把 key 的权限不同（面板 key 只能管任务，推理 key 只能调 /v1），因此调用点必须
// 知道自己匹配上的是哪一种。合成一个函数会让「这请求到底该按哪套权限走」取决于
// 返回值的用法，而调用点本来就知道自己要哪一种。
//
// 空 key 同样不匹配，理由与 WorkspaceForAPIKey 一致。比较走恒定时间。
func (c *RouterConfig) WorkspaceForInferenceKey(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	for _, workspace := range c.Workspaces {
		if hmac.Equal([]byte(apiKey), []byte(workspace.InferenceKey)) {
			return workspace.Name
		}
	}
	return ""
}

// WorkspaceAllowedModels 返回该空间允许直呼的模型名；第二个返回值报告是否存在清单。
//
// **没有配置清单**（返回 false）表示不限制——这是既有配置的默认，保持它们行为一字不变。
// 配置了空数组则是「一个都不许直呼」，与「不限制」是两回事。
//
// 区分靠的是 nil 而不是长度：Models 为 nil 表示配置里没有这个字段（不限制），非 nil
// 的**空**切片表示显式写了 `[]`（一个都不许）。用 len == 0 判断会把后者误当成不限制，
// 也就是把运维下的禁令悄悄失效——这正是这个字段存在的意义。
func (c *RouterConfig) WorkspaceAllowedModels(workspace string) (map[string]bool, bool) {
	if workspace == "" {
		workspace = DefaultWorkspace
	}
	for i := range c.Workspaces {
		config := c.Workspaces[i]
		if config.Name != workspace {
			continue
		}
		if config.Models == nil {
			return nil, false
		}
		allowed := make(map[string]bool, len(config.Models))
		for _, name := range config.Models {
			allowed[name] = true
		}
		return allowed, true
	}
	return nil, false
}

// NativeFirstForModel 返回模型的 native_first 设置（缺省 true）。
func (c *RouterConfig) NativeFirstForModel(modelID string) bool {
	for _, model := range c.Models {
		if model.ID == modelID {
			return model.NativeFirst
		}
	}
	return true
}

// UpstreamRoutesForBaseURL 返回某上游 URL 的路由副本。
func (c *RouterConfig) UpstreamRoutesForBaseURL(baseURL string) map[string]string {
	normalized, err := NormalizeUpstreamBaseURL(canonical.NewString(baseURL))
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for mode, path := range c.UpstreamRoutes[normalized] {
		out[mode] = path
	}
	return out
}

// isValidRoutingMode 报告路由模式是否合法。
func isValidRoutingMode(mode string) bool {
	return mode == "priority" || mode == "round_robin" || mode == "only_first"
}

// hasHTTPScheme 报告 URL 是否以 http:// 或 https:// 开头。
func hasHTTPScheme(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}
