package configeditor

import (
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/tui"
)

// stringItems 返回数组里的字符串成员（非数组返回空）。
//
// 参照实现写 `for alias in model.get("aliases", [])`，对字符串会逐字符迭代；那属于
// 配置损坏场景，Go 侧不模仿。
func stringItems(value *canonical.Value) []string {
	if !value.IsArray() {
		return nil
	}
	out := make([]string, 0, len(value.Arr))
	for _, item := range value.Arr {
		out = append(out, item.PyStr())
	}
	return out
}

// V2SummaryPanel 对应 config_editor.py:314 的 v2_summary_panel：供应商表 + 模型表。
//
// 版式差异（有意，见 doc.go）：rich 的 Table(expand=True) 按 ratio 分配列宽，Go 侧
// 用 internal/tui 的固定列宽表格——信息等价、间距不同。
func (e *Editor) V2SummaryPanel(data *canonical.Value) (tui.Renderable, error) {
	providers, err := rawProviders(data)
	if err != nil {
		return nil, err
	}
	models, err := rawModels(data)
	if err != nil {
		return nil, err
	}
	// 「访客」列已随访客模式删除（见 doc.go）：上游 key 不再有 allow_visitor 开关。
	providerTable := tui.Table{Columns: []tui.TableColumn{
		{Width: 18},
		{Width: 42},
		{Width: 6, Align: "right"},
		{Width: 28},
	}}
	// rich 的表头是 add_column(header)+样式+分隔线；internal/tui 的 Table 没有表头概念，
	// 这里用一行普通文本表头保住**信息**（列名），样式与分隔线不复刻（见 doc.go）。
	providerTable.Rows = append(providerTable.Rows, []string{"供应商", "Base URL", "Keys", "路由"})
	providerRowCount := 0

	for _, providerID := range sortedKeysOf(providers) {
		provider := providers.Lookup(providerID)
		keys, err := providerKeys(provider)
		if err != nil {
			return nil, err
		}
		routeText := "默认"
		if routes := provider.Lookup("routes"); routes.IsObject() && routes.Obj.Len() > 0 {
			routeText = strings.Join(sortedKeysOf(routes), ", ")
		}
		row := []string{
			shortText(providerID, 18),
			compactURL(stringOrEmpty(provider, "base_url"), 42),
			strconv.Itoa(keys.Obj.Len()),
			shortText(routeText, 28),
		}
		providerTable.Rows = append(providerTable.Rows, row)
		providerRowCount++
	}
	if providerRowCount == 0 {
		providerTable.Rows = append(providerTable.Rows, []string{"-", "[yellow]暂无供应商[/yellow]", "0", "-"})
	}

	modelTable := tui.Table{Columns: []tui.TableColumn{
		{Width: 28},
		{Width: 44},
		{Width: 14},
		{Width: 6, Align: "right"},
	}}
	modelTable.Rows = append(modelTable.Rows, []string{"本地模型", "别名", "路由模式", "Keys"})
	modelRowCount := 0
	for _, modelID := range sortedKeysOf(models) {
		model := models.Lookup(modelID)
		aliases := make([]string, 0)
		for _, alias := range stringItems(model.Lookup("aliases")) {
			if alias != "" {
				aliases = append(aliases, alias)
			}
		}
		targets, err := modelTargets(model)
		if err != nil {
			return nil, err
		}
		routingMode := stringOrEmpty(model, "routing_mode")
		if routingMode == "" {
			routingMode = "round_robin"
		}
		aliasText := strings.Join(aliases, ", ")
		if aliasText == "" {
			aliasText = "-"
		}
		modelTable.Rows = append(modelTable.Rows, []string{
			shortText(modelID, 28),
			shortText(aliasText, 44),
			routingMode,
			strconv.Itoa(len(targets.Arr)),
		})
		modelRowCount++
	}
	if modelRowCount == 0 {
		modelTable.Rows = append(modelTable.Rows, []string{"-", "-", "-", "0"})
	}

	return tui.Group{Items: []tui.Renderable{
		tui.SectionPanel(providerTable, "供应商 Key", "cyan"),
		tui.SectionPanel(modelTable, "模型设置", "magenta"),
	}}, nil
}

// ProviderCapabilitiesPanel 对应 config_editor.py:526 的 provider_capabilities_panel：
// 每个 Key 的能力摘要，用作供应商详情正文。
//
// 与参照实现的差异：key 不是对象时 Python 会在 `key.get("enabled")` 上崩溃，Go 侧
// 按「启用」渲染（配置损坏不该让整个面板打不开）。
func (e *Editor) ProviderCapabilitiesPanel(provider *canonical.Value) (tui.Renderable, error) {
	keys, err := providerKeys(provider)
	if err != nil {
		return nil, err
	}
	if keys.Obj.Len() == 0 {
		return tui.SectionPanel(
			"[yellow]该供应商暂无 Key。添加 Key 时会自动探测其可用模型。[/yellow]",
			"供应商能力", "yellow",
		), nil
	}
	blocks := make([]tui.Renderable, 0, keys.Obj.Len()+1)
	anyProbed := false
	for _, keyName := range sortedKeysOf(keys) {
		key := keys.Lookup(keyName)
		enabledText := "启用"
		if key.IsObject() && !boolOr(key, "enabled", true) {
			enabledText = "禁用"
		}
		capabilities := key.Lookup("capabilities")
		if !capabilities.IsObject() {
			blocks = append(blocks, tui.SectionPanel(
				"[yellow]尚未探测。[/yellow]",
				"Key · "+keyName+" · "+enabledText, "yellow",
			))
			continue
		}
		anyProbed = true
		models := capabilities.Lookup("models")
		routeStatus := capabilities.Lookup("route_status")
		checkedAt := stringOrEmpty(capabilities, "checked_at")
		if checkedAt == "" {
			checkedAt = "-"
		}
		errorsValue := capabilities.Lookup("errors")
		lines := []string{"探测时间: [bold]" + checkedAt + "[/bold]"}
		if errorsValue.IsObject() && errorsValue.Obj.Len() > 0 {
			parts := make([]string, 0, errorsValue.Obj.Len())
			for _, key := range errorsValue.Obj.Keys() {
				parts = append(parts, errorsValue.Lookup(key).PyStr())
			}
			lines = append(lines, "[red]探测错误: "+strings.Join(parts, "; ")+"[/red]")
		}
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
		if models.IsArray() && len(models.Arr) > 0 {
			shown := models.Arr
			if len(shown) > 8 {
				shown = shown[:8]
			}
			rendered := make([]string, 0, len(shown))
			for _, model := range shown {
				rendered = append(rendered, shortText(model.PyStr(), 40))
			}
			lines = append(lines, "模型（"+strconv.Itoa(len(models.Arr))+"）: "+strings.Join(rendered, ", "))
			if len(models.Arr) > 8 {
				lines = append(lines, "[dim]… 共 "+strconv.Itoa(len(models.Arr))+" 个模型[/dim]")
			}
		} else {
			lines = append(lines, "[dim]未发现模型[/dim]")
		}
		blocks = append(blocks, tui.SectionPanel(
			strings.Join(lines, "\n"),
			"Key · "+keyName+" · "+enabledText, "cyan",
		))
	}
	if !anyProbed {
		hint := tui.SectionPanel(
			"[yellow]全部 Key 尚未探测。添加 Key 时自动探测该 Key，或选「刷新能力探测」。[/yellow]",
			"提示", "yellow",
		)
		blocks = append([]tui.Renderable{hint}, blocks...)
	}
	if len(blocks) == 1 {
		return blocks[0], nil
	}
	return tui.Group{Items: blocks}, nil
}

// ModelKeyTargetsPanel 对应 config_editor.py:857 的 model_key_targets_panel：
// 列出某个模型绑定的 Key 及其启用状态。
func (e *Editor) ModelKeyTargetsPanel(data *canonical.Value, modelID string) (tui.Renderable, error) {
	models, err := rawModels(data)
	if err != nil {
		return nil, err
	}
	providers, err := rawProviders(data)
	if err != nil {
		return nil, err
	}
	targets := canonical.NewArray()
	if model := models.Lookup(modelID); model.IsObject() {
		resolved, err := modelTargets(model)
		if err != nil {
			return nil, err
		}
		targets = resolved
	}
	rows := make([]string, 0, len(targets.Arr))
	for index, target := range targets.Arr {
		providerID := stringOrEmpty(target, "provider")
		keyName := stringOrEmpty(target, "key")
		upstream := stringOrEmpty(target, "upstream_model")
		if upstream == "" {
			upstream = modelID
		}
		status := "禁用"
		provider := providers.Lookup(providerID)
		if provider.IsObject() {
			keys, err := providerKeys(provider)
			if err != nil {
				return nil, err
			}
			if key := keys.Lookup(keyName); key.IsObject() && boolOr(key, "enabled", true) {
				status = "启用"
			}
		}
		rows = append(rows, strconv.Itoa(index+1)+". [bold]"+providerID+"[/bold]/"+keyName+
			" → [bold]"+upstream+"[/bold] ("+status+")")
	}
	if len(rows) == 0 {
		rows = append(rows, "[yellow]该模型尚未绑定任何 Key[/yellow]")
	}
	return tui.SectionPanel(strings.Join(rows, "\n"), "模型 Key · "+shortText(modelID, 32), "cyan"), nil
}

// boolOr 等价于 Python 的 `value.get(key, default)` 后再做真值判断。
func boolOr(value *canonical.Value, key string, fallback bool) bool {
	child, ok := value.LookupOK(key)
	if !ok {
		return fallback
	}
	return child.Truthy()
}
