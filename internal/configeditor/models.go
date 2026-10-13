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
	// 先在一份副本上跑一遍同一段删除，再问用户：解绑最后一条 target 会连模型一起删掉，
	// 而它绑定的 Key 可能就此失去最后一个使用者——那是用户手输的凭据，删了不复原，必须
	// 在确认框里说清楚（WebUI 那边靠 dry_run 预演，这里用的是同一份 ModelEditImpactOf）。
	// 用户点头之后写的也是这份副本，所以「预演里说的」与「落盘的」天生一致。
	removed := targets.Arr[targetIndex]
	preview, removedKeys, err := previewModelWrite(data, func(target *canonical.Value) error {
		_, err := configops.DeleteModelTarget(target, modelID, targetIndex)
		return err
	})
	if err != nil {
		return nil, err
	}
	prompt := "解绑 Key " + stringOrEmpty(removed, "provider") + "/" + stringOrEmpty(removed, "key") + "？"
	if len(targets.Arr) == 1 {
		prompt = "这是该模型最后一个 Key，解绑后模型将不可用。"
	}
	// 没有 Key 要消失、也不是最后一条绑定时不提任何问题：与改动前的交互次数完全一样。
	if len(targets.Arr) == 1 || len(removedKeys) > 0 {
		if !e.confirmChoice(prompt+removedKeysNotice(removedKeys)+"继续？", false) {
			return tui.SectionPanel("[yellow]配置未变化。[/yellow]", "模型 Key", "yellow"), nil
		}
	}
	restart, err := e.commitV2Config(preview, oldConfig)
	if err != nil {
		return nil, err
	}
	message := "已解绑 Key: " + stringOrEmpty(removed, "provider") + "/" + stringOrEmpty(removed, "key") + "。"
	if len(removedKeys) > 0 {
		message += "并删除失去全部模型绑定的 Key: " + formatRemovedKeys(removedKeys) + "。"
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(message, "模型 Key", "green"),
		asRenderable(restart),
	}}, nil
}

// previewModelWrite 在一份副本上先跑一遍 mutation，返回改动后的配置与「会一起消失的
// 供应商 Key」。
//
// 删模型与解绑最后一条绑定都会把某些 Key 的最后一条绑定带走，而 Key 是用户手输的凭据。
// WebUI 用 dry_run 预演把这件事说清楚，TUI 这里复用同一份 ModelEditImpactOf：确认框里
// 说的就是落盘会发生的，落盘的也是那份被看过的东西。
func previewModelWrite(data *canonical.Value, mutation func(*canonical.Value) error) (*canonical.Value, []configops.RemovedKey, error) {
	preview := data.Clone()
	if err := mutation(preview); err != nil {
		return nil, nil, err
	}
	return preview, configops.ModelEditImpactOf(data, preview).RemovedKeys, nil
}

// removedKeysNotice 生成确认框里那句「还有哪些 Key 会一起消失」；没有则为空串。
//
// 没有 Key 要消失时返回空串，是为了让原有提示逐字不变——只有真会删掉凭据时才多这一句。
func removedKeysNotice(keys []configops.RemovedKey) string {
	if len(keys) == 0 {
		return ""
	}
	return "\n这些 Key 不再被任何模型使用，会被一并删除：" + formatRemovedKeys(keys) + "。"
}

// formatRemovedKeys 把会被清理的供应商 Key 排成一行（服务端已按 provider、key 升序给出）。
func formatRemovedKeys(keys []configops.RemovedKey) string {
	rendered := make([]string, 0, len(keys))
	for _, key := range keys {
		rendered = append(rendered, key.Provider+"/"+key.Key)
	}
	return strings.Join(rendered, "、")
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
	oldConfig, err := loadRouterConfig(data)
	if err != nil {
		return nil, err
	}
	// 删模型会顺手清掉不再被任何模型绑定的 Key（仍被别的路由绑定的原样保留），所以先在
	// 副本上算清楚会少哪把凭据，再连它一起问一次——没有 Key 要消失时这句提示是空的，
	// 确认框与改动前逐字一样。用户点头后写的就是那份被看过的副本。
	preview, removedKeys, err := previewModelWrite(data, func(target *canonical.Value) error {
		return configops.DeleteModel(target, modelID)
	})
	if err != nil {
		return nil, err
	}
	if !e.confirmChoice("确认删除模型 "+modelID+"？"+removedKeysNotice(removedKeys), false) {
		return tui.SectionPanel("[yellow]配置未变化。[/yellow]", "模型设置", "yellow"), nil
	}
	restart, err := e.commitV2Config(preview, oldConfig)
	if err != nil {
		return nil, err
	}
	message := "已删除模型 " + modelID + "。"
	if len(removedKeys) > 0 {
		message += "并删除失去全部模型绑定的 Key: " + formatRemovedKeys(removedKeys) + "。"
	}
	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(message, "模型设置", "green"),
		asRenderable(restart),
	}}, nil
}
