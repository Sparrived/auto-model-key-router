package configeditor

import (
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
	"github.com/Sparrived/auto-model-key-router/internal/tui"
)

// AddModelRouteInteractively 对应 config_editor.py:876 的
// add_model_route_interactively：把一个已有的供应商 Key 绑到模型上（新增 target）。
//
// modelID 为空串表示先让用户选/输入本地模型 id。
func (e *Editor) AddModelRouteInteractively(modelID string) (any, error) {
	draft := e.formDraft("add_model_route")
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	providers, err := rawProviders(data)
	if err != nil {
		return nil, err
	}
	if providers.Obj.Len() == 0 {
		return tui.SectionPanel("[yellow]请先添加供应商 Key。[/yellow]", "添加模型 Key", "yellow"), nil
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	models, err := rawModels(data)
	if err != nil {
		return nil, err
	}
	if modelID == "" {
		selected, err := e.SelectOrEnterModelID("添加模型 Key", "本地模型 ID", models, draft["model_id"])
		if err != nil {
			return nil, err
		}
		modelID = selected
		draft["model_id"] = modelID
	}
	if modelID == "" {
		return tui.SectionPanel("[red]模型 ID 不能为空[/red]", "添加模型 Key", "red"), nil
	}
	if !models.Obj.Has(modelID) {
		if _, err := configops.CreateModel(data, modelID, configops.CreateModelOptions{}); err != nil {
			return nil, err
		}
	}
	providerID, found, err := e.SelectProvider(data, "选择供应商")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	providers, err = rawProviders(data)
	if err != nil {
		return nil, err
	}
	provider := providers.Lookup(providerID)
	keys, err := providerKeys(provider)
	if err != nil {
		return nil, err
	}
	if keys.Obj.Len() == 0 {
		return tui.SectionPanel("[yellow]该供应商暂无 Key。[/yellow]", "添加模型 Key", "yellow"), nil
	}
	keyNames := sortedKeysOf(keys)
	keyOptions := make([]tui.Option, 0, len(keyNames)+1)
	for _, keyName := range keyNames {
		keyOptions = append(keyOptions, tui.Option{
			Value: keyName,
			Label: keyName + " · " + keyFingerprint(stringOrEmpty(keys.Lookup(keyName), "api_key")),
		})
	}
	keyOptions = append(keyOptions, tui.Option{Value: "0", Label: "返回"})
	keyChoice := e.selectOption("选择 Key", keyOptions, tui.SelectOptions{})
	if keyChoice == "0" {
		return nil, nil
	}
	keyName := keyChoice
	key := keys.Lookup(keyName)
	keyModels := []string{}
	if capabilities := key.Lookup("capabilities"); capabilities.IsObject() {
		keyModels = stringItems(capabilities.Lookup("models"))
	}
	// Python 的 `str(a or b if key_models else model_id)`：有探测结果时用草稿里的上游
	// 模型名，否则用第一个探测到的模型；完全没有探测结果时回落到本地模型 id。
	defaultUpstream := modelID
	if len(keyModels) > 0 {
		if draft["upstream_model"] != "" {
			defaultUpstream = draft["upstream_model"]
		} else {
			defaultUpstream = keyModels[0]
		}
	}
	promptDefault := ""
	if defaultUpstream != modelID {
		promptDefault = defaultUpstream
	}
	text, err := e.promptText("添加模型 Key", "上游模型 ID（默认同本地模型）", tui.PromptOptions{Default: &promptDefault})
	if err != nil {
		return nil, err
	}
	upstreamModel := strings.TrimSpace(text)
	if upstreamModel == "" {
		upstreamModel = modelID
	}
	target := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "provider", Value: canonical.NewString(providerID)},
		canonical.ObjectPair{Key: "key", Value: canonical.NewString(keyName)},
		canonical.ObjectPair{Key: "upstream_model", Value: canonical.NewString(upstreamModel)},
	)
	if err := configops.AddModelTarget(data, modelID, target); err != nil {
		if isConfigOperationError(err) {
			return tui.SectionPanel("[yellow]"+err.Error()+"[/yellow]", "添加模型 Key", "yellow"), nil
		}
		return nil, err
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	e.dropFormDraft("add_model_route")
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(
			"本地模型: [bold]"+modelID+"[/bold]\n供应商 Key: [bold]"+providerID+"/"+keyName+"[/bold]\n"+
				"上游模型: [bold]"+upstreamModel+"[/bold]",
			"添加完成", "green",
		),
		asRenderable(restart),
	}}, nil
}

// ManageModelRoutesInteractively 对应 config_editor.py:954 的
// manage_model_routes_interactively：管理某个模型绑定了哪些 Key。
func (e *Editor) ManageModelRoutesInteractively(selectedModelID string) error {
	for {
		data, err := e.loadConfigData()
		if err != nil {
			return err
		}
		modelID := selectedModelID
		if modelID == "" {
			chosen, found, err := e.SelectV2Model(data, "选择模型 Key")
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			modelID = chosen
		}
		models, err := rawModels(data)
		if err != nil {
			return err
		}
		targets, err := modelTargets(models.Lookup(modelID))
		if err != nil {
			return err
		}
		options := []tui.Option{
			{Value: "a", Label: "绑定 Key"},
			{Value: "d", Label: "解绑 Key"},
		}
		if len(targets.Arr) > 0 {
			options = append(options, tui.Option{Value: "u", Label: "改上游模型"})
		}
		options = append(options, tui.Option{Value: "0", Label: "返回"})
		panel, err := e.ModelKeyTargetsPanel(data, modelID)
		if err != nil {
			return err
		}
		choice := e.selectOption(
			"模型 Key · "+shortText(modelID, 28),
			options,
			tui.SelectOptions{Content: panel},
		)
		if choice == "0" {
			if selectedModelID != "" {
				return nil
			}
			continue
		}
		e.clearTerminalHistory()
		switch choice {
		case "a":
			result, err := e.AddModelRouteInteractively(modelID)
			if err != nil {
				return err
			}
			if result != nil {
				e.showResultPage("模型 Key", result)
			}
		case "u":
			if len(targets.Arr) == 0 {
				continue
			}
			result, err := e.UpdateModelTargetUpstreamInteractively(modelID)
			if err != nil {
				return err
			}
			if result != nil {
				e.showResultPage("模型 Key", result)
			}
		case "d":
			result, err := e.DeleteModelKeyBindingInteractively(modelID)
			if err != nil {
				return err
			}
			if result != nil {
				e.showResultPage("模型 Key", result)
			}
		}
	}
}

// UpdateModelTargetUpstreamInteractively 对应 config_editor.py:991 的
// update_model_target_upstream_interactively。
func (e *Editor) UpdateModelTargetUpstreamInteractively(modelID string) (any, error) {
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	models, err := rawModels(data)
	if err != nil {
		return nil, err
	}
	targets, err := modelTargets(models.Lookup(modelID))
	if err != nil {
		return nil, err
	}
	options := make([]tui.Option, 0, len(targets.Arr)+1)
	for index, target := range targets.Arr {
		upstream := stringOrEmpty(target, "upstream_model")
		if upstream == "" {
			upstream = modelID
		}
		options = append(options, tui.Option{
			Value: strconv.Itoa(index + 1),
			Label: stringOrEmpty(target, "provider") + "/" + stringOrEmpty(target, "key") + " → " + upstream,
		})
	}
	options = append(options, tui.Option{Value: "0", Label: "返回"})
	choice := e.selectOption("选择 Key · "+shortText(modelID, 28), options, tui.SelectOptions{})
	if choice == "0" {
		return nil, nil
	}
	targetIndex, ok := optionIndex(choice)
	if !ok || targetIndex >= len(targets.Arr) {
		return nil, nil
	}
	current := stringOrEmpty(targets.Arr[targetIndex], "upstream_model")
	if current == "" {
		current = modelID
	}
	text, err := e.promptText("改上游模型", "上游模型 ID", tui.PromptOptions{Default: &current})
	if err != nil {
		return nil, err
	}
	upstreamModel := strings.TrimSpace(text)
	if upstreamModel == "" || upstreamModel == current {
		return tui.SectionPanel("[yellow]配置未变化。[/yellow]", "模型 Key", "yellow"), nil
	}
	if err := configops.UpdateModelTarget(data, modelID, targetIndex, upstreamModel); err != nil {
		return nil, err
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(
			"已更新 "+modelID+" 的上游模型: "+current+" → "+upstreamModel+"。",
			"模型 Key", "green",
		),
		asRenderable(restart),
	}}, nil
}

// DeleteModelKeyBindingInteractively 对应 config_editor.py:1026 的
// delete_model_key_binding_interactively。
func (e *Editor) DeleteModelKeyBindingInteractively(modelID string) (any, error) {
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	models, err := rawModels(data)
	if err != nil {
		return nil, err
	}
	targets, err := modelTargets(models.Lookup(modelID))
	if err != nil {
		return nil, err
	}
	options := make([]tui.Option, 0, len(targets.Arr)+1)
	for index, target := range targets.Arr {
		upstream := stringOrEmpty(target, "upstream_model")
		if upstream == "" {
			upstream = modelID
		}
		options = append(options, tui.Option{
			Value: strconv.Itoa(index + 1),
			Label: stringOrEmpty(target, "provider") + "/" + stringOrEmpty(target, "key") + " → " + upstream,
		})
	}
	options = append(options, tui.Option{Value: "0", Label: "返回"})
	choice := e.selectOption("解绑 Key · "+shortText(modelID, 28), options, tui.SelectOptions{})
	if choice == "0" {
		return nil, nil
	}
	targetIndex, ok := optionIndex(choice)
	if !ok || targetIndex >= len(targets.Arr) {
		return nil, nil
	}
	if len(targets.Arr) == 1 && !e.confirmChoice("这是该模型最后一个 Key，解绑后模型将不可用。继续？", false) {
		return tui.SectionPanel("[yellow]配置未变化。[/yellow]", "模型 Key", "yellow"), nil
	}
	removed, err := configops.DeleteModelTarget(data, modelID, targetIndex)
	if err != nil {
		return nil, err
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(
			"已解绑 Key: "+stringOrEmpty(removed, "provider")+"/"+stringOrEmpty(removed, "key")+"。",
			"模型 Key", "green",
		),
		asRenderable(restart),
	}}, nil
}

// ManageV2ModelSettingsInteractively 对应 config_editor.py:1059 的
// manage_v2_model_settings_interactively：模型设置菜单。
func (e *Editor) ManageV2ModelSettingsInteractively() error {
	for {
		data, err := e.loadConfigData()
		if err != nil {
			return err
		}
		modelID, found, err := e.SelectV2Model(data, "选择模型")
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		panel, err := e.ModelKeyTargetsPanel(data, modelID)
		if err != nil {
			return err
		}
		choice := e.selectOption("模型设置 · "+shortText(modelID, 28), []tui.Option{
			{Value: "1", Label: "别名"},
			{Value: "2", Label: "路由模式"},
			{Value: "3", Label: "管理 Key"},
			{Value: "4", Label: "绑定 Key"},
			{Value: "5", Label: "删除模型"},
			{Value: "0", Label: "返回"},
		}, tui.SelectOptions{Content: panel})
		if choice == "0" {
			continue
		}
		e.clearTerminalHistory()
		var result any
		switch choice {
		case "1", "2":
			result, err = e.UpdateV2ModelSettingsInteractively(modelID, choice)
			if err != nil {
				return err
			}
		case "3":
			e.runSubmodule(func() (any, error) {
				return nil, e.ManageModelRoutesInteractively(modelID)
			})
			continue
		case "4":
			result = e.runSubmodule(func() (any, error) {
				return e.AddModelRouteInteractively(modelID)
			})
		case "5":
			result, err = e.DeleteV2ModelInteractively(modelID)
			if err != nil {
				return err
			}
		default:
			continue
		}
		if result != nil {
			e.showResultPage("模型设置", result)
		}
		if choice == "5" {
			return nil
		}
	}
}

// UpdateV2ModelSettingsInteractively 对应 config_editor.py:1099 的
// update_v2_model_settings_interactively。
func (e *Editor) UpdateV2ModelSettingsInteractively(modelID, choice string) (any, error) {
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	models, err := rawModels(data)
	if err != nil {
		return nil, err
	}
	model := models.Lookup(modelID)
	message := ""
	switch choice {
	case "1":
		aliases := stringItems(model.Lookup("aliases"))
		defaultValue := strings.Join(aliases, ", ")
		text, err := e.promptText("模型别名", "别名，多个用逗号分隔", tui.PromptOptions{Default: &defaultValue})
		if err != nil {
			return nil, err
		}
		parsed := make([]string, 0)
		for _, alias := range strings.Split(strings.TrimSpace(text), ",") {
			if trimmed := strings.TrimSpace(alias); trimmed != "" {
				parsed = append(parsed, trimmed)
			}
		}
		if _, err := configops.UpdateModel(data, modelID, configops.UpdateModelOptions{Aliases: parsed}); err != nil {
			return nil, err
		}
		message = "已更新 " + modelID + " 的别名。"
	case "2":
		current := stringOrEmpty(model, "routing_mode")
		if current == "" {
			current = "round_robin"
		}
		text, err := e.promptText("路由模式", "路由模式", tui.PromptOptions{
			Choices: []string{"priority", "round_robin", "only_first"},
			Default: &current,
		})
		if err != nil {
			return nil, err
		}
		routingMode := strings.TrimSpace(text)
		if _, err := configops.UpdateModel(data, modelID, configops.UpdateModelOptions{RoutingMode: &routingMode}); err != nil {
			return nil, err
		}
		message = "已更新 " + modelID + " 的路由模式: " + routingMode + "。"
	default:
		return nil, nil
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(message, "模型映射", "green"),
		asRenderable(restart),
	}}, nil
}

// DeleteV2ModelInteractively 对应 config_editor.py:1145 的
// delete_v2_model_interactively。
func (e *Editor) DeleteV2ModelInteractively(modelID string) (any, error) {
	data, err := e.loadConfigData()
	if err != nil {
		return nil, err
	}
	models, err := rawModels(data)
	if err != nil {
		return nil, err
	}
	if !models.Obj.Has(modelID) {
		return tui.SectionPanel("[red]模型不存在: "+modelID+"[/red]", "模型设置", "red"), nil
	}
	if !e.confirmChoice("确认删除模型 "+modelID+"？", false) {
		return tui.SectionPanel("[yellow]配置未变化。[/yellow]", "模型设置", "yellow"), nil
	}
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	if err := configops.DeleteModel(data, modelID); err != nil {
		return nil, err
	}
	restart, err := e.commitV2Config(data, oldConfig)
	if err != nil {
		return nil, err
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel("已删除模型 "+modelID+"。", "模型设置", "green"),
		asRenderable(restart),
	}}, nil
}
