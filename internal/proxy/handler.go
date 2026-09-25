package proxy

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/auth"
	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/protocol"
	"github.com/Sparrived/auto-model-key-router/internal/proxysupport"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// Handle 处理一条 /v1/{path} 代理请求。
//
// path 是 `{path}` 部分（不含前导 `/v1/`），与参照实现的 handle_proxy_request
// 入参一致。函数是同步的：调用方在自己的 goroutine 里等它返回，流式响应也在这里
// 写完后才返回——这正是「租约覆盖整条流」的落点（见 HandleResource 的 defer）。
func (h *Handler) Handle(w http.ResponseWriter, request *http.Request, path string) {
	h.handle(w, request, path)
}

// handle 是 Handle 的实现。
func (h *Handler) handle(w http.ResponseWriter, request *http.Request, path string) {
	lease, err := h.manager.Acquire()
	if err != nil {
		// runtime.ErrManagerClosed：关停中不再接受新请求。参照实现在关停时靠
		// lifespan 取消，没有对应状态码，503 是最接近的语义。
		writeJSON(w, http.StatusServiceUnavailable,
			jsonErrorResponse("服务正在关停，不接受新请求"))
		return
	}
	// defer 覆盖全部退出路径：流式响应也必须留到写完再释放，否则
	// RuntimeManager.Close() 会在租约归零后关掉仍在使用的连接池。
	defer lease.Release()

	prepared := h.prepare(w, request, path, lease.Resources)
	if prepared == nil {
		return
	}
	result := h.handleSingleTarget(prepared)
	status := result.statusCode()
	if !IsRetryableStatus(status) {
		result.writeTo(w)
		return
	}
	if prepared.RequestedModelName == config.UNIFIED_MODEL_ID {
		h.writeUnifiedFallback(w, prepared, result)
		return
	}
	if prepared.TaskName != nil {
		// 任务备选：首选重试失败后切到任务的备选模型，参数保持任务固定值。
		h.writeTaskFallback(w, prepared, result)
		return
	}
	result.writeTo(w)
}

// writeUnifiedFallback 在 unified-model 首选失败后切到备选计划。
func (h *Handler) writeUnifiedFallback(w http.ResponseWriter, prepared *RequestContext, result attempt) {
	plan, err := prepared.pool().ResolveUnifiedPlan(
		proxysupport.RequestRouteKind(prepared.Path), prepared.RequestedKeyName)
	if err != nil || plan.Fallback == nil {
		result.writeTo(w)
		return
	}
	h.runFallback(w, prepared, result, *plan.Fallback)
}

// writeTaskFallback 在任务首选失败后切到任务的备选模型。
//
// 用请求自己的工作空间查表：备选必须是同一个空间里那个任务的备选，不能串到别的
// 空间同名任务的备选上去。
func (h *Handler) writeTaskFallback(w http.ResponseWriter, prepared *RequestContext, result attempt) {
	plan, found := prepared.pool().TaskPlanIn(prepared.Workspace, *prepared.TaskName)
	if !found || plan.Fallback == nil {
		result.writeTo(w)
		return
	}
	h.runFallback(w, prepared, result, *plan.Fallback)
}

// runFallback 用备选目标重跑一次 _handle_single_target。
//
// 三处必须重算而不是沿用 primary 的值：
//   - attempts：备选模型可能有不同的 key 数量与 none/only_first 路由模式
//     （proxy_handler.py:148）；
//   - cache_affinity_key：粘滞哈希的 basis 里含 model_id（proxy_handler.py:153）；
//   - use_native：备选模型可能有自己的 native_first 设置（proxy_handler.py:156）。
//
// 备选的 key 计数也**按凭据作用域重算**（KeyCountFor 而不是 KeyCount）：访问密钥走不到
// 这里（它既不能用 unified-model 也不能用任务，见 prepare 的两处 403），但把口径留成
// 「总数」会在将来放开任一入口时静默绕过供应商清单。这里与主路径用同一个判据。
func (h *Handler) runFallback(w http.ResponseWriter, prepared *RequestContext, result attempt, target config.RouteTarget) {
	pool := prepared.pool()
	fallbackKeyCount := pool.KeyCountFor(target.Model, prepared.AccessKey)
	if fallbackKeyCount == 0 {
		result.writeTo(w)
		return
	}
	fallbackOnlyFirst := pool.RoutingMode(target.Model) == "only_first"
	fallbackKey := target.Key
	attempts := runtime.RetryPolicy{MaxRetries: prepared.Config.MaxRetries}.Attempts(
		fallbackKeyCount, &fallbackKey, fallbackOnlyFirst)

	fallback := *prepared
	fallback.ModelID = target.Model
	fallback.RequestedKeyName = &fallbackKey
	fallback.KeyCount = fallbackKeyCount
	fallback.OnlyFirst = fallbackOnlyFirst
	fallback.Attempts = attempts
	fallback.CacheAffinityKey = cacheAffinityKey(prepared.Path, prepared.Payload, target.Model)
	fallback.UseNative = prepared.Path == "messages" &&
		prepared.Config.NativeFirstForModel(target.Model)
	fallback.FallbackTarget = target.Model
	// 上游调用预算是**整次下游请求**的口径（有意增补的观测项），因此备选沿用同一个
	// 计数器，而不是重新计数——重置会让放大倍数被低估。
	fallback.upstreamCalls = prepared.upstreamCalls

	fallbackResult := h.handleSingleTarget(&fallback)
	if fallbackResult.statusCode() < 400 {
		fallbackResult.markFallback()
	}
	fallbackResult.writeTo(w)
}

// _rotates_keys 的对等实现：本次请求是否可能在重试时换到另一个 Key。
//
// 多 Key 且调用方没有指定 Key、也不是 only_first 时，_handle_single_target 才会
// 把已失败的 Key 排除掉，下一次选择才可能落到别的 Key 上（proxy_handler.py:103）。
func rotatesKeys(context *RequestContext) bool {
	return context.KeyCount > 1 &&
		(context.RequestedKeyName == nil || *context.RequestedKeyName == "") &&
		!context.OnlyFirst
}

// effectiveReasoningEffort 取本次请求**最终生效**的推理强度（thinking effort）。
//
// 直接复用构造上游请求体那条路径上的 proxysupport.ApplyReasoningEffort，读它写进上游体
// 的 reasoning_effort。三级优先级（模型级配置覆盖一切 → 载荷顶层 reasoning_effort →
// 载荷 reasoning.effort）只有那一份实现：抄一遍迟早会与真正发出去的体不一致，而看板上
// 显示的正是「上游实际收到的强度」。
//
// 返回空串表示没有可读的强度，落库为 NULL。载荷不是对象（multipart 表单）或该字段不是
// 字符串时同样返回空串——数字/布尔形态的强度既不是配置值也不是合法取值，照 str() 展示成
// "true" 只会让看板多一个读不懂的词。
//
// 两处刻意的留白，都不是遗漏：
//   - Anthropic 的 thinking（{type, budget_tokens}）不参与：AMKR 不改写也不解释它，
//     折算成 reasoning_effort 会造出一个上游并不认识的取值。原生透传时上游收到的是
//     调用方自己写的 thinking，看板因此可能显示模型级配置值或空——那是 AMKR 侧的决策，
//     不是上游体里的值。
//   - 备选模型路径沿用首选 context 的这份读数：备选的强度由同一份配置与同一份载荷决定，
//     重算一遍只会在两次克隆之间引入不一致。
func effectiveReasoningEffort(payload *canonical.Value, modelID string, cfg *config.RouterConfig) string {
	if !payload.IsObject() {
		return ""
	}
	effort := proxysupport.ApplyReasoningEffort(payload, modelID, cfg).Lookup("reasoning_effort")
	if !effort.IsString() {
		return ""
	}
	return effort.Str
}

// prepare 完成鉴权、body 解析、路由解析与预算计算。
//
// 返回 nil 表示已经把错误响应写给下游。
func (h *Handler) prepare(w http.ResponseWriter, request *http.Request, path string, resources *runtime.RuntimeResources) *RequestContext {
	pool := keyPoolOf(resources)
	if pool == nil {
		// 装配错误：runtime.KeyPool 不是本包认识的 KeyPool 实现。宁可失败关闭，
		// 也不能带着一个 nil 接口继续跑（那会在选 key 时 panic）。
		h.logger.Error("装配错误：runtime 的 KeyPool 不满足 proxy.KeyPool 接缝",
			"path", path)
		writeJSON(w, http.StatusServiceUnavailable,
			jsonErrorResponse("服务装配错误：key pool 不可用"))
		return nil
	}

	authorization := h.authorize(request, resources.Config)
	if authorization == nil {
		writeJSON(w, http.StatusUnauthorized, jsonErrorResponse("本地 API key 验证失败"))
		return nil
	}
	if authorization.Denied != "" {
		writeJSON(w, http.StatusForbidden, jsonErrorResponse(authorization.Denied))
		return nil
	}
	// accessKey 非 nil 表示本次请求由一把访问密钥发起：它的两份清单分别在下面
	// 「模型名」与「挑选上游 key」两处生效。
	accessKey := authorization.AccessKey
	// scopedWorkspace 非空表示凭据被钉死在某个工作空间上，据此**忽略请求头**。
	scopedWorkspace := authorization.Workspace
	callerType := "local"
	switch {
	case accessKey != nil:
		callerType = "access_key"
	case scopedWorkspace != "":
		callerType = "workspace"
	}

	payload, body, flat, bodyErr := h.readRequestBody(request, request.Header.Get("Content-Type"))
	if bodyErr != nil {
		if coded, ok := bodyErr.(*bodyError); ok {
			writeJSON(w, coded.statusCode, jsonErrorResponse(coded.message))
			return nil
		}
		writeJSON(w, http.StatusBadRequest, jsonErrorResponse(bodyErr.Error()))
		return nil
	}
	isStream := false
	if !flat {
		isStream = proxysupport.IsStreamRequest(payload)
	}
	requestedModelID, found := proxysupport.ResolveModelID(path, payload)
	if !found {
		writeJSON(w, http.StatusBadRequest,
			jsonErrorResponse("请求体中缺少 model 字段"))
		return nil
	}

	requestedModelName, requestedKeyName, hasKey := proxysupport.SplitRequestedModelKey(requestedModelID)
	var requestedKey *string
	if hasKey {
		requestedKey = &requestedKeyName
	}
	if accessKey != nil && requestedModelName == config.UNIFIED_MODEL_ID {
		writeJSON(w, http.StatusForbidden,
			jsonErrorResponse("访问密钥 "+accessKey.Name+" 无权访问模型: "+config.UNIFIED_MODEL_ID))
		return nil
	}
	// 作用域凭据同样不能用 unified-model：它是一份**全局**计划（由运维为整台实例挑的
	// 默认首选/备选），不属于任何工作空间，因此绕过按空间收窄的模型白名单。要让它
	// 可用，运维就把具体模型名写进该空间的 models 清单——那是显式的授权。
	if scopedWorkspace != "" && requestedModelName == config.UNIFIED_MODEL_ID {
		writeJSON(w, http.StatusForbidden,
			jsonErrorResponse("工作空间 "+scopedWorkspace+" 无权访问模型: "+config.UNIFIED_MODEL_ID))
		return nil
	}

	// 任务名路由：模型与采样参数都由任务固定，调用方只能传任务名。必须在
	// resolve_route 之前判断，否则任务的 key=None 会把调用方指定的 Key 冲掉。
	// （proxy_handler.py:228）
	//
	// 工作空间来自 X-AMKR-Workspace 头，缺省即默认工作空间；任务名只在所选空间
	// 内查表。访问密钥仍然不能使用任务：任务的模型由运维预先固定，那把模型可能不在
	// 这把 key 的授权清单里，绕过去等于清单失效。工作空间头也不例外——访问密钥无权
	// 访问任何任务，多一个维度只会扩大面。
	//
	// 作用域推理凭据（scopedWorkspace 非空）**忽略请求头**：空间由 key 决定。这是
	// 这个模式要防的核心事情，与面板 key 在管理面的做法一致（api 的
	// authorizedTaskConfig）。若请求头能换空间，一把配进项目环境变量的 key 泄漏后
	// 就等于所有空间的推理权限。
	workspace := scopedWorkspace
	if workspace == "" {
		workspace = config.NormalizeWorkspace(request.Header.Get(config.WorkspaceHeader))
	}
	var taskParams *canonical.Value
	var taskName *string
	if plan, isTask := pool.TaskPlanIn(workspace, requestedModelName); isTask {
		// 访问密钥一律不能用任务名，且**必须在这里挡**：下面的 ResolveRouteIn 自带
		// 任务查表，若只靠「参数固定」那一支拦（原先是 `accessKey == nil` 才进入），
		// 清单不受限的访问密钥就会经由 ResolveRouteIn 拿到任务指向的模型，同时因为
		// taskParams 仍为 nil 而**绕过任务的固定参数**——既不施加也不报冲突。那是
		// 一条静默的越权路径，不是清单收窄。
		if accessKey != nil {
			h.logModelNotConfigured(path, requestedModelID, "", workspace, "access_key_task_not_allowed")
			writeJSON(w, http.StatusForbidden, jsonErrorResponse(
				"访问密钥 "+accessKey.Name+" 无权使用任务: "+requestedModelName))
			return nil
		}
		if requestedKey != nil {
			writeJSON(w, http.StatusBadRequest, jsonErrorResponse(
				"任务 "+requestedModelName+" 的参数由 AMKR 固定，不能指定 Key"))
			return nil
		}
		// 尚未指定模型的任务（允许先建出来占位）在这里就止住：继续往下走只会
		// 拿到一个空模型名，最终报出「模型  未配置」这种既看不出是任务、也指不
		// 出哪个任务的错。明确说清是哪个任务没选模型，用户才知道去哪里修。
		if plan.Primary.Model == "" {
			h.logModelNotConfigured(path, requestedModelName, "", workspace, "task_model_unset")
			writeJSON(w, http.StatusNotFound, jsonErrorResponse(
				"任务 "+requestedModelName+" 尚未指定模型；请先在 AMKR 的任务路由中为该任务选择模型"))
			return nil
		}
		taskParams = pool.TaskParamsIn(workspace, requestedModelName)
		name := requestedModelName
		taskName = &name
		if conflicts := proxysupport.TaskParamConflicts(payload, taskParams); len(conflicts) > 0 {
			message := "任务 " + requestedModelName + " 已固定参数 " +
				joinChineseEnumeration(conflicts) + "，调用方不能再传这些参数"
			writeJSON(w, http.StatusBadRequest, jsonErrorResponse(message))
			return nil
		}
	}

	// 访问密钥的**模型名清单**在这里生效，且刻意比在别名解析**之前**：调用方写什么
	// 名字就按什么名字授权（清单里可以写别名），而只要清单限制了模型，未列出的写法
	// 一律拒绝。任务名不走这条路——任务由访问密钥禁用，见上面的分支。
	if accessKey != nil && !accessKey.AllowsModel(requestedModelName) {
		h.logModelNotConfigured(path, requestedModelID, "", workspace, "access_key_model_not_allowed")
		writeJSON(w, http.StatusForbidden, jsonErrorResponse(
			"访问密钥 "+accessKey.Name+" 无权访问模型: "+requestedModelName))
		return nil
	}

	modelID, key, err := pool.ResolveRouteIn(workspace, requestedModelName, requestedKey, path)
	if err != nil {
		h.writeRouteError(w, path, requestedModelID, workspace, err)
		return nil
	}
	// 作用域凭据的模型白名单：任务名不查（任务自己固定的模型就是该空间被授权用的），
	// 直呼真实模型名时才判。放在解析**之后**，这样别名也按它解析到的真实模型算，
	// 而不是拿别名去比对清单——否则同一个模型写成别名就绕过了。
	if scopedWorkspace != "" && taskName == nil {
		if allowed, restricted := resources.Config.WorkspaceAllowedModels(scopedWorkspace); restricted && !allowed[modelID] {
			h.logModelNotConfigured(path, requestedModelID, modelID, workspace, "workspace_model_not_allowed")
			writeJSON(w, http.StatusForbidden, jsonErrorResponse(
				"工作空间 "+scopedWorkspace+" 无权访问模型: "+requestedModelName))
			return nil
		}
	}
	if requestedKey != nil {
		value := key
		requestedKey = &value
	} else {
		requestedKey = nil
	}

	configuredKeyCount := pool.KeyCount(modelID)
	// keyCount 是**本请求作用域内**可用的 key 数。访问密钥的供应商清单在选 key 之前
	// 就把无关的上游 key 排除了，因此它的可用数可能小于模型的总数——那正是「这把 key
	// 能用哪些供应商」的落点。完整权限与工作空间凭据不受影响（两者都为总数）。
	keyCount := configuredKeyCount
	if accessKey != nil {
		keyCount = pool.KeyCountFor(modelID, accessKey)
	}
	if configuredKeyCount == 0 {
		h.logModelNotConfigured(path, requestedModelID, modelID, workspace, "no_configured_keys")
		if taskParams != nil {
			writeJSON(w, http.StatusNotFound, jsonErrorResponse(
				"任务 "+requestedModelName+" 指向的模型 "+modelID+
					" 未配置；请先在 AMKR 的任务路由中修正该任务"))
			return nil
		}
		writeJSON(w, http.StatusNotFound, jsonErrorResponse(
			"模型 "+requestedModelID+" 未配置；请先在 AMKR 的模型设置中配置该模型"))
		return nil
	}
	if keyCount == 0 {
		// 走到这里只可能是访问密钥：它的供应商清单与这个模型的上游全都不相交。
		// 说清是**供应商**维度被拒（而不是笼统的「无权」），运维才知道该改哪一份清单。
		//
		// accessKey 为 nil 时上面那个分支已经返回（keyCount 恒等于 configuredKeyCount），
		// 因此这里的兜底措辞只是防御：将来若有人改动 KeyCountFor 的语义，也不至于
		// 在一条 403 上 panic。
		h.logModelNotConfigured(path, requestedModelID, modelID, workspace, "access_key_provider_not_allowed")
		if accessKey != nil {
			writeJSON(w, http.StatusForbidden, jsonErrorResponse(
				"访问密钥 "+accessKey.Name+" 无权访问模型: "+requestedModelName))
			return nil
		}
		writeJSON(w, http.StatusForbidden, jsonErrorResponse(
			"无权访问模型: "+requestedModelName))
		return nil
	}
	if path == "messages/count_tokens" {
		// 本地估算，不转发上游、也不写指标行（proxy_handler.py:311）。它位于鉴权与
		// 模型校验之后，因此未配置的模型仍然 404——顺序不可前移。
		writeJSON(w, http.StatusOK, countTokensResponse(payload))
		return nil
	}

	onlyFirst := pool.RoutingMode(modelID) == "only_first"
	attempts := runtime.RetryPolicy{MaxRetries: resources.Config.MaxRetries}.Attempts(
		keyCount, requestedKey, onlyFirst)
	useNative := path == "messages" && resources.Config.NativeFirstForModel(modelID)
	affinity := cacheAffinityKey(path, payload, modelID)

	return &RequestContext{
		Path:               path,
		Request:            request,
		Runtime:            resources,
		AccessKey:          accessKey,
		CallerType:         callerType,
		Payload:            payload,
		IsStream:           isStream,
		OriginalRaw:        body,
		RequestedModelID:   requestedModelID,
		RequestedModelName: requestedModelName,
		RequestedKeyName:   requestedKey,
		ModelID:            modelID,
		Config:             resources.Config,
		KeyCount:           keyCount,
		OnlyFirst:          onlyFirst,
		Attempts:           attempts,
		CacheAffinityKey:   affinity,
		UseNative:          useNative,
		Workspace:          workspace,
		TaskName:           taskName,
		TaskParams:         taskParams,
		ReasoningEffort:    effectiveReasoningEffort(payload, modelID, resources.Config),
		FlatPayload:        flat,
		now:                h.now,
		upstreamCalls:      &upstreamCallCounter{max: h.maxCalls, logger: h.logger},
	}
}

// authorize 走可替换的鉴权判定。
//
// 默认实现直接转 internal/auth（hmac.Equal 的恒定时间比较）；Options.Authorizer
// 非空时整体替换，对应参照实现的 create_app(authenticator=...)。
//
// 受限凭据（访问密钥、工作空间推理 key）的识别刻意放在这里而不是塞进 internal/auth：
// auth 是不读配置的纯函数，而这两条通道要读配置清单。顺序也不能反——本地主凭据
// 必须**先**判，否则一把受限 key 可能被当成管理员凭据用。
func (h *Handler) authorize(request *http.Request, cfg *config.RouterConfig) *AuthorizerResult {
	if h.authorizer != nil {
		return h.authorizer(request, cfg.LocalAPIKey)
	}
	if context := auth.Authenticate(nil, request, cfg.LocalAPIKey); context != nil {
		return &AuthorizerResult{}
	}
	apiKey := auth.RequestAPIKey(request.Header)
	// 访问密钥：命中但被停用时回报 Denied（403 + 原因），而不是当成错误凭据。
	if accessKey := cfg.AccessKeyFor(apiKey); accessKey != nil {
		if !accessKey.Enabled {
			return &AuthorizerResult{Denied: "访问密钥已被停用: " + accessKey.Name}
		}
		return &AuthorizerResult{AccessKey: accessKey}
	}
	// 完整权限与访问密钥都没通过，再看是不是某个空间的推理 key。
	if workspace := cfg.WorkspaceForInferenceKey(apiKey); workspace != "" {
		return &AuthorizerResult{Workspace: workspace}
	}
	return nil
}

// writeRouteError 把 ResolveRoute / ResolveUnifiedPlan 的错误折算成下游响应。
//
// 参照实现（proxy_handler.py:270）没有捕获这里的异常，它由 FastAPI 的异常处理器
// 兜底成 500。而 `resolve_route` 只在「unified-model 未配置 unified_model」时抛
// `KeyError(UNIFIED_MODEL_ID)`——这是配置缺失，不是服务故障。用户看到
// 「模型 unified-model 未配置；请先在 AMKR 的模型设置中配置该模型」比看到 500
// 有用得多，因此这里**刻意分叉**为 404 + 该文案。该分叉只在「unified_model
// 未配置」时生效，其余路径的响应一字未变。
func (h *Handler) writeRouteError(w http.ResponseWriter, path, requestedModelID, workspace string, _ error) {
	h.logModelNotConfigured(path, requestedModelID, "", workspace, "key_selection_failed")
	writeJSON(w, http.StatusNotFound, jsonErrorResponse(
		"模型 "+requestedModelID+" 未配置；请先在 AMKR 的模型设置中配置该模型"))
}

// logModelNotConfigured 记录模型路由被拒的原因。
//
// 文案与参照实现逐字一致（proxy_support.py:45），因为它是运维排查的主要线索。
//
// workspace 是**有意增补**的字段：两个工作空间可以有同名任务，不带上空间名就
// 分不清是哪一个被拒了。默认工作空间也照常记录，字段恒定存在比「有时有有时没有」
// 更好查。
func (h *Handler) logModelNotConfigured(path, requestedModelID, modelID, workspace, reason string) {
	h.logger.Warn("model routing rejected",
		"path", "/v1/"+path,
		"requested_model", requestedModelID,
		"resolved_model", modelID,
		"workspace", workspace,
		"reason", reason)
}

// joinChineseEnumeration 用「、」连接参数名，对齐参照实现的 `'、'.join(...)`。
func joinChineseEnumeration(items []string) string {
	result := ""
	for index, item := range items {
		if index > 0 {
			result += "、"
		}
		result += item
	}
	return result
}

// countTokensResponse 构造 count_tokens 的响应体。
//
// 参照实现返回 JSONResponse({"input_tokens": ...})，只有一个字段。
func countTokensResponse(payload *canonical.Value) *canonical.Value {
	tokens := protocol.EstimateAnthropicInputTokens(payload)
	result := canonical.NewObject()
	result.Obj.Set("input_tokens", canonical.NewIntValue(int64(tokens)))
	return result
}

// cacheAffinityKey 计算 round-robin 粘滞用的哈希键。
//
// 移植 proxy_handler.py:348。只有 messages 路径参与粘滞；显式的
// `prompt_cache_key` 直接作为键（前缀 `prompt_cache_key:`），否则对
// {path, model, system, tools, tool_choice, messages[{role,content}]} 做
// **canonical JSON 的 sha256**。任何序列化偏差都会让粘滞静默失效
// （canonical 包顶部注释里的「两个消费者」之一）。
func cacheAffinityKey(path string, payload *canonical.Value, modelID string) *string {
	if path != "messages" || !payload.IsObject() {
		return nil
	}
	promptCacheKey := payload.Lookup("prompt_cache_key")
	if promptCacheKey.IsString() && strings.TrimSpace(promptCacheKey.Str) != "" {
		value := "prompt_cache_key:" + strings.TrimSpace(promptCacheKey.Str)
		return &value
	}

	basis := canonical.NewObject()
	basis.Obj.Set("path", canonical.NewString(path))
	basis.Obj.Set("model", canonical.NewString(modelID))
	basis.Obj.Set("system", nullIfAbsent(payload.Lookup("system")))
	basis.Obj.Set("tools", nullIfAbsent(payload.Lookup("tools")))
	basis.Obj.Set("tool_choice", nullIfAbsent(payload.Lookup("tool_choice")))
	basis.Obj.Set("messages", cacheAffinityMessages(payload.Lookup("messages")))
	value := "messages:" + canonical.RevisionHash(basis)
	return &value
}

// cacheAffinityMessages 只保留每条消息的 role 与 content。
//
// 移植 proxy_handler.py:371：非列表返回空列表；列表里的非对象项被跳过。
func cacheAffinityMessages(messages *canonical.Value) *canonical.Value {
	result := canonical.NewArray()
	if !messages.IsArray() {
		return result
	}
	items := make([]*canonical.Value, 0, messages.Len())
	for _, message := range messages.Items() {
		if !message.IsObject() {
			continue
		}
		entry := canonical.NewObject()
		entry.Obj.Set("role", nullIfAbsent(message.Lookup("role")))
		entry.Obj.Set("content", nullIfAbsent(message.Lookup("content")))
		items = append(items, entry)
	}
	return canonical.NewArray(items...)
}

// nullIfAbsent 把缺失字段表示为 JSON null。
//
// Python 的 `payload.get(k)` 在键缺失时给 None，而 dict 字面量里的 None 会序列化
// 成 null——两者在 canonical JSON 里是同一个字节序列，所以这里显式补 null。
func nullIfAbsent(value *canonical.Value) *canonical.Value {
	if value == nil {
		return canonical.NewNull()
	}
	return value
}

// upstreamCallCounter 统计单次下游请求发起的上游调用次数。
//
// **有意增补**（见 DefaultMaxUpstreamCallsPerRequest）：参照实现没有这个上限，
// 也无从看出放大倍数。计数器挂在 RequestContext 上，因此备选路径共享同一个预算。
type upstreamCallCounter struct {
	max    int
	count  int
	logger *slog.Logger
}

// take 记一次上游调用；返回 false 表示已达上限、不应再发起调用。
//
// 达到上限时打一条 Warn：这是唯一能看出「一次下游请求放大了多少次上游调用」的
// 信号，也是本增补项的观测面。
func (c *upstreamCallCounter) take() bool {
	if c == nil {
		return true
	}
	c.count++
	if c.count > c.max {
		if c.logger != nil {
			c.logger.Warn("上游调用次数达到上限，停止继续重试",
				"count", c.count, "limit", c.max)
		}
		return false
	}
	return true
}

// exhaustedMessage 是达到上限时的错误文案。
func (c *upstreamCallCounter) exhaustedMessage() string {
	return "上游调用次数达到上限（本次请求已发起 " + strconv.Itoa(c.count) +
		" 次，上限 " + strconv.Itoa(c.max) + "），已停止继续重试"
}
