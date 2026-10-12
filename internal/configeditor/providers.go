package configeditor

import (
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
	"github.com/Sparrived/auto-model-key-router/internal/tui"
)

// AddProviderKeyInteractively 对应 config_editor.py:616 的
// add_provider_key_interactively：新建（或复用）供应商并添加一个 Key。
//
// providerID 为空串表示让用户先选供应商；createProvider 为真时跳过选择、直接新建
// （参照实现的 `provider_id=None, create_provider=True`）。
func (e *Editor) AddProviderKeyInteractively(providerID string, createProvider bool) (any, error) {
	draft := e.formDraft("add_provider_key")
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	providers, err := rawProviders(data)
	if err != nil {
		return nil, err
	}
	if providerID == "" {
		existing := sortedKeysOf(providers)
		providerChoice := "n"
		if !createProvider {
			options := []tui.Option{{Value: "n", Label: "新供应商"}}
			for index, currentID := range existing {
				options = append(options, tui.Option{Value: strconv.Itoa(index + 1), Label: currentID})
			}
			options = append(options, tui.Option{Value: "0", Label: "返回"})
			content, err := e.V2SummaryPanel(data)
			if err != nil {
				return nil, err
			}
			providerChoice = e.selectOption("供应商", options, tui.SelectOptions{Content: content})
		}
		if providerChoice == "0" {
			return nil, nil
		}
		if providerChoice == "n" {
			defaultProviderID := draft["provider_id"]
			if defaultProviderID == "" {
				defaultProviderID = "openai"
			}
			text, err := e.promptText("添加供应商", "供应商 ID", tui.PromptOptions{Default: &defaultProviderID})
			if err != nil {
				return nil, err
			}
			providerID = strings.TrimSpace(text)
			draft["provider_id"] = providerID
			if providerID == "" {
				return tui.SectionPanel("[red]供应商 ID 不能为空[/red]", "添加失败", "red"), nil
			}
			if providers.Obj.Has(providerID) {
				return tui.SectionPanel("[red]供应商已存在: "+providerID+"[/red]", "添加失败", "red"), nil
			}
			defaultBaseURL := draft["base_url"]
			if defaultBaseURL == "" {
				defaultBaseURL = "https://api.openai.com"
			}
			baseURLText, err := e.promptText("添加供应商", "Base URL", tui.PromptOptions{Default: &defaultBaseURL})
			if err != nil {
				return nil, err
			}
			baseURL := strings.TrimSpace(baseURLText)
			draft["base_url"] = baseURL
			if _, err := configops.CreateProvider(data, providerID, baseURL, configops.CreateProviderOptions{}); err != nil {
				if isConfigOperationError(err) {
					return tui.SectionPanel("[red]"+err.Error()+"[/red]", "添加失败", "red"), nil
				}
				return nil, err
			}
		} else {
			index, ok := optionIndex(providerChoice)
			if !ok || index >= len(existing) {
				return nil, nil
			}
			providerID = existing[index]
		}
	} else if !providers.Obj.Has(providerID) {
		return tui.SectionPanel("[red]供应商不存在: "+providerID+"[/red]", "添加失败", "red"), nil
	}

	provider := providers.Lookup(providerID)
	keys, err := providerKeys(provider)
	if err != nil {
		return nil, err
	}
	defaultKeyName := draft["key_name"]
	if defaultKeyName == "" {
		defaultKeyName = "key-" + strconv.Itoa(keys.Obj.Len()+1)
	}
	keyNameText, err := e.promptText("添加供应商 Key", "Key 名称", tui.PromptOptions{Default: &defaultKeyName})
	if err != nil {
		return nil, err
	}
	keyName := strings.TrimSpace(keyNameText)
	draft["key_name"] = keyName
	if keyName == "" {
		return tui.SectionPanel("[red]Key 名称不能为空[/red]", "添加失败", "red"), nil
	}
	if keys.Obj.Has(keyName) {
		return tui.SectionPanel("[red]Key 已存在: "+keyName+"[/red]", "添加失败", "red"), nil
	}
	apiKeyText, err := e.promptText("添加供应商 Key", "API key", tui.PromptOptions{Password: true})
	if err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(apiKeyText)
	draft["api_key"] = apiKey
	if apiKey == "" {
		return tui.SectionPanel("[red]API key 不能为空[/red]", "添加失败", "red"), nil
	}
	if _, err := configops.CreateProviderKey(data, providerID, keyName, apiKey, configops.CreateProviderKeyOptions{
		Enabled: configops.BoolPtr(true),
	}); err != nil {
		if isConfigOperationError(err) {
			return tui.SectionPanel("[red]"+err.Error()+"[/red]", "添加失败", "red"), nil
		}
		return nil, err
	}

	// 每个 Key 单独探测：不同 Key 能访问的模型集合可能不同（如免费/付费额度、
	// 不同订阅），探测结果按 Key 缓存，互不复用（config_editor.py:680-686）。
	var probe *canonical.Value
	e.withStatus("正在探测 Key "+providerID+"/"+keyName+" 的可用能力...", func() {
		probe, err = e.Prober.ProbeKeyCapability(provider, keyName, nil, 15.0)
	})
	if err != nil {
		return nil, err
	}
	createdKey := keys.Lookup(keyName)
	createdKey.SetKey("capabilities", probe)
	models := stringItems(probe.Lookup("models"))
	errorsValue := probe.Lookup("errors")
	if len(models) == 0 && errorsValue.IsObject() && errorsValue.Obj.Len() > 0 {
		parts := make([]string, 0, errorsValue.Obj.Len())
		for _, key := range errorsValue.Obj.Keys() {
			parts = append(parts, errorsValue.Lookup(key).PyStr())
		}
		e.showResultPage("添加 Key", tui.SectionPanel(
			"探测失败，仍将保存该 Key；可稍后在供应商菜单手动刷新探测。\n错误: "+strings.Join(parts, "; "),
			"探测失败", "yellow",
		))
	}
	probeFailed := len(models) == 0 && errorsValue.IsObject() && errorsValue.Obj.Len() > 0
	var selectedModels []string
	if !probeFailed {
		selectedModels, err = e.SelectModelsToServe(providerID, keyName, models, "该 Key 服务哪些模型")
		if err != nil {
			return nil, err
		}
	}
	if selectedModels == nil {
		// 用户在模型多选界面放弃：保存 Key（不绑定模型）后返回。
		if _, err := loadRouterConfig(data); err != nil {
			return nil, err
		}
		restart, err := e.commitV2Config(data, oldConfig)
		if err != nil {
			return nil, err
		}
		e.dropFormDraft("add_provider_key")
		return tui.Group{Items: []tui.Renderable{
			tui.SectionPanel(
				"供应商: [bold]"+providerID+"[/bold]\nKey: [bold]"+keyName+"[/bold]\n"+
					"已保存 Key，未绑定任何模型。可在模型设置中绑定。",
				"Key 已保存", "green",
			),
			asRenderable(restart),
		}}, nil
	}
	bound := 0
	for _, upstreamModel := range selectedModels {
		localModels, err := rawModels(data)
		if err != nil {
			return nil, err
		}
		if !localModels.Obj.Has(upstreamModel) {
			if _, err := configops.CreateModel(data, upstreamModel, configops.CreateModelOptions{}); err != nil {
				if isConfigOperationError(err) {
					continue
				}
				return nil, err
			}
		}
		target := canonical.NewObjectOf(
			canonical.ObjectPair{Key: "provider", Value: canonical.NewString(providerID)},
			canonical.ObjectPair{Key: "key", Value: canonical.NewString(keyName)},
			canonical.ObjectPair{Key: "upstream_model", Value: canonical.NewString(upstreamModel)},
		)
		if err := configops.AddModelTarget(data, upstreamModel, target); err != nil {
			if isConfigOperationError(err) {
				continue
			}
			return nil, err
		}
		bound++
	}
	if _, err := loadRouterConfig(data); err != nil {
		return nil, err
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	e.dropFormDraft("add_provider_key")
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(
			"供应商: [bold]"+providerID+"[/bold]\nKey: [bold]"+keyName+"[/bold]\n"+
				"上游: [bold]"+compactURL(stringOrEmpty(provider, "base_url"), 56)+"[/bold]\n"+
				"已绑定模型: [bold]"+strconv.Itoa(bound)+"[/bold]",
			"添加完成", "green",
		),
		asRenderable(restart),
	}}, nil
}

// ManageProviderKeysInteractively 对应 config_editor.py:751 的
// manage_provider_keys_interactively。
func (e *Editor) ManageProviderKeysInteractively(providerID string) error {
	for {
		data, err := e.loadConfigData()
		if err != nil {
			return err
		}
		if providerID != "" {
			providers, err := rawProviders(data)
			if err != nil {
				return err
			}
			if !providers.Obj.Has(providerID) {
				return nil
			}
		}
		selectedProviderID, keyName, found, err := e.SelectProviderKey(data, "选择供应商 Key", providerID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		providerID = selectedProviderID
		providers, err := rawProviders(data)
		if err != nil {
			return err
		}
		provider := providers.Lookup(providerID)
		keys, err := providerKeys(provider)
		if err != nil {
			return err
		}
		key := keys.Lookup(keyName)
		// 「访客访问」选项已随访客模式删除：上游 key 不再有 allow_visitor 开关，受限
		// 凭据的授权改由访问密钥自己的 providers/models 清单决定（管理面在 WebUI 的
		// 访问密钥页，TUI 不提供编辑入口）。
		options := []tui.Option{
			{Value: "1", Label: "开关"},
			{Value: "2", Label: "重命名"},
			{Value: "3", Label: "删除"},
			{Value: "0", Label: "返回"},
		}
		boundModels, err := boundModelIDs(data, providerID, keyName)
		if err != nil {
			return err
		}
		enabledText := "禁用"
		if boolOr(key, "enabled", true) {
			enabledText = "启用"
		}
		serviceModels := "未绑定模型"
		if len(boundModels) > 0 {
			serviceModels = strings.Join(boundModels, ", ")
		}
		lines := []string{
			"供应商: [bold]" + providerID + "[/bold]",
			"Key: [bold]" + keyName + "[/bold]",
			"状态: [bold]" + enabledText + "[/bold]",
			"服务模型: [bold]" + serviceModels + "[/bold]",
			"指纹: [bold]" + keyFingerprint(stringOrEmpty(key, "api_key")) + "[/bold]",
		}
		choice := e.selectOption(
			providerID+"/"+keyName,
			options,
			tui.SelectOptions{Content: tui.SectionPanel(strings.Join(lines, "\n"), "Key 信息", "cyan")},
		)
		if choice == "0" {
			continue
		}
		e.clearTerminalHistory()
		result, err := e.UpdateProviderKeyInteractively(providerID, keyName, choice)
		if err != nil {
			return err
		}
		if result != nil {
			e.showResultPage("供应商 Key", result)
		}
	}
}

// UpdateProviderKeyInteractively 对应 config_editor.py:806 的
// update_provider_key_interactively：按菜单选择改动一个 Key。
func (e *Editor) UpdateProviderKeyInteractively(providerID, keyName, choice string) (any, error) {
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	provider, err := configops.RequireProvider(data, providerID)
	if err != nil {
		return nil, err
	}
	key, err := configops.RequireKey(provider, keyName)
	if err != nil {
		return nil, err
	}
	message := ""
	switch {
	case choice == "1":
		enabled := !boolOr(key, "enabled", true)
		if _, err := configops.UpdateProviderKey(data, providerID, keyName, configops.UpdateProviderKeyOptions{
			Enabled: configops.BoolPtr(enabled),
		}); err != nil {
			return nil, err
		}
		state := "禁用"
		if enabled {
			state = "启用"
		}
		message = "已" + state + " " + providerID + "/" + keyName + "。"
	case choice == "2":
		text, err := e.promptText("重命名 Key", "新名称", tui.PromptOptions{Default: &keyName})
		if err != nil {
			return nil, err
		}
		newName := strings.TrimSpace(text)
		if newName == "" || newName == keyName {
			return tui.SectionPanel("[yellow]配置未变化。[/yellow]", "重命名 Key", "yellow"), nil
		}
		if _, err := configops.UpdateProviderKey(data, providerID, keyName, configops.UpdateProviderKeyOptions{
			NewName: &newName,
		}); err != nil {
			if isConfigOperationError(err) {
				return tui.SectionPanel("[red]"+err.Error()+"[/red]", "重命名 Key", "red"), nil
			}
			return nil, err
		}
		message = "已重命名 " + providerID + "/" + keyName + " → " + newName + "。"
	case choice == "3":
		usedBy, err := boundModelIDs(data, providerID, keyName)
		if err != nil {
			return nil, err
		}
		if len(usedBy) > 0 && !e.confirmChoice(
			"该 Key 被 "+strconv.Itoa(len(usedBy))+" 个模型使用，删除会一并移除这些绑定。继续？",
			false,
		) {
			return tui.SectionPanel("[yellow]配置未变化。[/yellow]", "删除 Key", "yellow"), nil
		}
		removedModels, err := configops.DeleteProviderKey(data, providerID, keyName)
		if err != nil {
			return nil, err
		}
		suffix := ""
		if len(removedModels) > 0 {
			suffix = "\n已移除空模型: [bold]" + strconv.Itoa(len(removedModels)) + "[/bold]"
		}
		message = "已删除 " + providerID + "/" + keyName + "。" + suffix
	default:
		return nil, nil
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(message, "供应商 Key", "green"),
		asRenderable(restart),
	}}, nil
}

// ManageProviderRoutesInteractively 对应 config_editor.py:1158 的
// manage_provider_routes_interactively。
func (e *Editor) ManageProviderRoutesInteractively(providerID string) error {
	for {
		data, err := e.loadConfigData()
		if err != nil {
			return err
		}
		selectedProviderID := providerID
		if selectedProviderID == "" {
			chosen, found, err := e.SelectProvider(data, "选择供应商路径")
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			selectedProviderID = chosen
		} else {
			providers, err := rawProviders(data)
			if err != nil {
				return err
			}
			if !providers.Obj.Has(selectedProviderID) {
				return nil
			}
		}
		providers, err := rawProviders(data)
		if err != nil {
			return err
		}
		provider := providers.Lookup(selectedProviderID)
		routes, ok := provider.LookupOK("routes")
		if !ok || !routes.IsObject() {
			// Python 的 `provider.setdefault("routes", {})`：读一次就把空容器写进 data。
			routes = canonical.NewObject()
			provider.SetKey("routes", routes)
		}
		rows := make([]string, 0, len(config.UpstreamRouteModes()))
		modes := config.UpstreamRouteModes()
		for _, mode := range modes {
			rows = append(rows, upstreamRouteLabel(mode)+": [bold]"+upstreamRoutePath(routes, mode)+"[/bold]")
		}
		options := make([]tui.Option, 0, len(modes)+2)
		for index, mode := range modes {
			options = append(options, tui.Option{
				Value: strconv.Itoa(index + 1),
				Label: upstreamRouteLabel(mode),
			})
		}
		options = append(options,
			tui.Option{Value: "c", Label: "清空自定义路径"},
			tui.Option{Value: "0", Label: "返回"},
		)
		choice := e.selectOption(
			"供应商路径 · "+selectedProviderID,
			options,
			tui.SelectOptions{Content: tui.SectionPanel(strings.Join(rows, "\n"), "当前路径", "cyan")},
		)
		if choice == "0" {
			if providerID != "" {
				return nil
			}
			continue
		}
		e.clearTerminalHistory()
		result, err := e.UpdateProviderRoutesInteractively(selectedProviderID, choice)
		if err != nil {
			return err
		}
		if result != nil {
			e.showResultPage("供应商路径", result)
		}
	}
}

// UpdateProviderRoutesInteractively 对应 config_editor.py:1196 的
// update_provider_routes_interactively。
func (e *Editor) UpdateProviderRoutesInteractively(providerID, choice string) (any, error) {
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	providers, err := rawProviders(data)
	if err != nil {
		return nil, err
	}
	provider := providers.Lookup(providerID)
	routes, ok := provider.LookupOK("routes")
	if !ok || !routes.IsObject() {
		routes = canonical.NewObject()
		provider.SetKey("routes", routes)
	}
	message := ""
	modes := config.UpstreamRouteModes()
	if choice == "c" {
		if _, err := configops.UpdateProvider(data, providerID, configops.UpdateProviderOptions{
			Routes:       canonical.NewObject(),
			UpdateRoutes: true,
		}); err != nil {
			return nil, err
		}
		message = "已清空 " + providerID + " 的自定义路径。"
	} else {
		index, ok := optionIndex(choice)
		if !ok || index >= len(modes) {
			return nil, nil
		}
		mode := modes[index]
		current := upstreamRoutePath(routes, mode)
		text, err := e.promptText(
			"供应商路径",
			upstreamRouteLabel(mode)+" 路径或前缀",
			tui.PromptOptions{Default: &current},
		)
		if err != nil {
			return nil, err
		}
		route := strings.TrimSpace(text)
		if route == "" {
			routes.DeleteKey(mode)
			message = "已恢复 " + upstreamRouteLabel(mode) + " 默认路径。"
		} else {
			normalized, err := config.NormalizeUpstreamRoutePath(mode, canonical.NewString(route))
			if err != nil {
				return nil, err
			}
			routes.SetKey(mode, canonical.NewString(normalized))
			message = "已更新 " + providerID + " 的 " + upstreamRouteLabel(mode) + " 路径。"
		}
		if _, err := configops.UpdateProvider(data, providerID, configops.UpdateProviderOptions{
			Routes:       routes,
			UpdateRoutes: true,
		}); err != nil {
			return nil, err
		}
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(message, "供应商路径", "green"),
		asRenderable(restart),
	}}, nil
}

// DeleteProviderInteractively 对应 config_editor.py:1227 的
// delete_provider_interactively。
func (e *Editor) DeleteProviderInteractively(providerID string) (any, error) {
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	providers, err := rawProviders(data)
	if err != nil {
		return nil, err
	}
	if !providers.Obj.Has(providerID) {
		return tui.SectionPanel("[red]供应商不存在: "+providerID+"[/red]", "删除供应商", "red"), nil
	}
	if !e.confirmChoice("确认删除供应商 "+providerID+"？", false) {
		return tui.SectionPanel("[yellow]配置未变化。[/yellow]", "删除供应商", "yellow"), nil
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	removedModels, err := configops.DeleteProvider(data, providerID)
	if err != nil {
		return nil, err
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(
			"已删除供应商 "+providerID+"。\n已移除空模型: [bold]"+strconv.Itoa(len(removedModels))+"[/bold]",
			"删除供应商", "green",
		),
		asRenderable(restart),
	}}, nil
}

// RefreshProviderCapabilityInteractively 对应 config_editor.py:1247 的
// refresh_provider_capability_interactively：手动重探一个或多个 Key。
//
// 探测是按 Key 的（每个 Key 可能看到不同的模型列表）；单个 Key 还可以只检查某一个
// 端点模式（openai / anthropic / responses）。
func (e *Editor) RefreshProviderCapabilityInteractively(providerID string) (any, error) {
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	providers, err := rawProviders(data)
	if err != nil {
		return nil, err
	}
	provider := providers.Lookup(providerID)
	keys, err := providerKeys(provider)
	if err != nil {
		return nil, err
	}
	if keys.Obj.Len() == 0 {
		return tui.SectionPanel("[yellow]该供应商暂无 Key，无法探测。[/yellow]", "刷新能力", "yellow"), nil
	}
	panel, err := e.ProviderCapabilitiesPanel(provider)
	if err != nil {
		return nil, err
	}
	scopeChoice := e.selectOption("刷新能力探测", []tui.Option{
		{Value: "1", Label: "刷新全部 Key"},
		{Value: "2", Label: "指定 Key"},
		{Value: "0", Label: "返回"},
	}, tui.SelectOptions{Content: panel})
	if scopeChoice == "0" {
		return nil, nil
	}
	keyNames := sortedKeysOf(keys)
	var modes []string
	if scopeChoice == "2" {
		keyOptions := make([]tui.Option, 0, len(keyNames)+1)
		for _, keyName := range keyNames {
			keyOptions = append(keyOptions, tui.Option{Value: keyName, Label: keyName})
		}
		keyOptions = append(keyOptions, tui.Option{Value: "0", Label: "返回"})
		keyChoice := e.selectOption("选择要刷新的 Key", keyOptions, tui.SelectOptions{})
		if keyChoice == "0" {
			return nil, nil
		}
		keyNames = []string{keyChoice}
		modeChoice := e.selectOption("端点检查范围", []tui.Option{
			{Value: "1", Label: "全部路由模式"},
			{Value: "2", Label: "仅 Chat (openai)"},
			{Value: "3", Label: "仅 Messages (anthropic)"},
			{Value: "4", Label: "仅 Responses"},
		}, tui.SelectOptions{})
		if modeChoice == "0" {
			return nil, nil
		}
		switch modeChoice {
		case "1":
			modes = nil
		case "2":
			modes = []string{"openai"}
		case "3":
			modes = []string{"anthropic"}
		case "4":
			modes = []string{"responses"}
		default:
			return nil, nil
		}
	}
	refreshed := make([]string, 0, len(keyNames))
	e.withStatus("正在探测 "+providerID+" 的 "+strconv.Itoa(len(keyNames))+" 个 Key...", func() {
		for _, keyName := range keyNames {
			probe, probeErr := e.Prober.ProbeKeyCapability(provider, keyName, modes, 15.0)
			if probeErr != nil {
				err = probeErr
				return
			}
			if key := keys.Lookup(keyName); key.IsObject() {
				key.SetKey("capabilities", probe)
			}
			refreshed = append(refreshed, keyName)
		}
	})
	if err != nil {
		return nil, err
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	messageLines := []string{"已刷新 Key: [bold]" + strings.Join(refreshed, ", ") + "[/bold]"}
	for _, keyName := range refreshed {
		probe := keys.Lookup(keyName).Lookup("capabilities")
		if !probe.IsObject() {
			continue
		}
		models := probe.Lookup("models")
		errorsValue := probe.Lookup("errors")
		routeStatus := probe.Lookup("route_status")
		lines := []string{"[bold]" + keyName + "[/bold]: " + strconv.Itoa(models.Len()) + " 个模型"}
		if routeStatus.IsObject() && routeStatus.Obj.Len() > 0 {
			parts := make([]string, 0, routeStatus.Obj.Len())
			for _, mode := range sortedKeysOf(routeStatus) {
				status := routeStatus.Lookup(mode).PyStr()
				color := "red"
				if status == "ok" {
					color = "green"
				}
				parts = append(parts, upstreamRouteLabel(mode)+": ["+color+"]"+status+"[/"+color+"]")
			}
			lines = append(lines, "路由: "+strings.Join(parts, " · "))
		}
		if errorsValue.IsObject() && errorsValue.Obj.Len() > 0 {
			parts := make([]string, 0, errorsValue.Obj.Len())
			for _, key := range errorsValue.Obj.Keys() {
				parts = append(parts, errorsValue.Lookup(key).PyStr())
			}
			lines = append(lines, "[red]错误: "+strings.Join(parts, "; ")+"[/red]")
		}
		messageLines = append(messageLines, strings.Join(lines, "\n"))
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(strings.Join(messageLines, "\n"), "刷新完成", "green"),
		asRenderable(restart),
	}}, nil
}

// ManageProvidersInteractively 对应 config_editor.py:1328 的
// manage_providers_interactively：供应商主菜单。
func (e *Editor) ManageProvidersInteractively() error {
	for {
		data, err := e.loadConfigData()
		if err != nil {
			return err
		}
		providers, err := rawProviders(data)
		if err != nil {
			return err
		}
		providerIDs := sortedKeysOf(providers)
		panel, err := e.V2SummaryPanel(data)
		if err != nil {
			return err
		}
		options := []tui.Option{{Value: "n", Label: "添加供应商"}}
		for index, providerID := range providerIDs {
			provider := providers.Lookup(providerID)
			keys, err := providerKeys(provider)
			if err != nil {
				return err
			}
			options = append(options, tui.Option{
				Value: strconv.Itoa(index + 1),
				Label: shortText(providerID, 22) + " · " +
					compactURL(stringOrEmpty(provider, "base_url"), 42) + " · " +
					strconv.Itoa(keys.Obj.Len()) + " Key",
			})
		}
		options = append(options, tui.Option{Value: "0", Label: "返回"})
		choice := e.selectOption("供应商", options, tui.SelectOptions{Content: panel})
		if choice == "0" {
			return nil
		}
		if choice == "n" {
			result := e.runSubmodule(func() (any, error) {
				return e.AddProviderKeyInteractively("", true)
			})
			if result != nil {
				e.showResultPage("添加供应商", result)
			}
			continue
		}
		index, ok := optionIndex(choice)
		if !ok || index >= len(providerIDs) {
			continue
		}
		providerID := providerIDs[index]
		for {
			data, err := e.loadConfigData()
			if err != nil {
				return err
			}
			providers, err := rawProviders(data)
			if err != nil {
				return err
			}
			if !providers.Obj.Has(providerID) {
				break
			}
			panel, err := e.ProviderCapabilitiesPanel(providers.Lookup(providerID))
			if err != nil {
				return err
			}
			choice := e.selectOption("供应商 · "+providerID, []tui.Option{
				{Value: "1", Label: "添加 Key"},
				{Value: "2", Label: "管理 Key"},
				{Value: "3", Label: "刷新能力探测"},
				{Value: "4", Label: "Base URL / 路由设置"},
				{Value: "5", Label: "删除供应商"},
				{Value: "0", Label: "返回"},
			}, tui.SelectOptions{Content: panel})
			if choice == "0" {
				break
			}
			switch choice {
			case "1":
				result := e.runSubmodule(func() (any, error) {
					return e.AddProviderKeyInteractively(providerID, false)
				})
				if result != nil {
					e.showResultPage("添加供应商 Key", result)
				}
			case "2":
				e.runSubmodule(func() (any, error) {
					return nil, e.ManageProviderKeysInteractively(providerID)
				})
			case "3":
				result, err := e.RefreshProviderCapabilityInteractively(providerID)
				if err != nil {
					return err
				}
				if result != nil {
					e.showResultPage("刷新能力", result)
				}
			case "4":
				e.runSubmodule(func() (any, error) {
					return nil, e.ManageProviderRoutesInteractively(providerID)
				})
			case "5":
				result, err := e.DeleteProviderInteractively(providerID)
				if err != nil {
					return err
				}
				if result != nil {
					e.showResultPage("删除供应商", result)
				}
			}
			// 删除供应商后内层循环结束（Python 的 `break`）。
			if choice == "5" {
				break
			}
		}
	}
}

// boundModelIDs 返回绑定了 (providerID, keyName) 的模型 id，顺序即 models 的插入顺序。
func boundModelIDs(data *canonical.Value, providerID, keyName string) ([]string, error) {
	models, err := rawModels(data)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, modelID := range models.Obj.Keys() {
		targets, err := modelTargets(models.Lookup(modelID))
		if err != nil {
			return nil, err
		}
		for _, target := range targets.Arr {
			if stringOrEmpty(target, "provider") == providerID && stringOrEmpty(target, "key") == keyName {
				out = append(out, modelID)
			}
		}
	}
	return out, nil
}

// asRenderable 把「可能是 Renderable 的结果」折成 Renderable。
//
// commitV2Config 的返回值来自注入接缝，参照实现保证它是可渲染对象；接缝实现若返回
// nil，这里给一个空文本节点，避免结果页出现 nil。
func asRenderable(value any) tui.Renderable {
	if renderable, ok := value.(tui.Renderable); ok && renderable != nil {
		return renderable
	}
	return tui.NewText("")
}
