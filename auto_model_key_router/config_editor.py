from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

from rich import box
from rich.console import Group
from rich.live import Live
from rich.prompt import Prompt
from rich.table import Table

from .clipboard import paste_from_clipboard
from .config import RouterConfig, empty_config_dict, generate_local_api_key
from .formatting import compact_url, key_fingerprint, short_text
from .service import restart_service_after_config_change
from .tui import ResultPage, clear_terminal_history, confirm_choice, console, mouse_wheel_mode, page_title, read_key, run_submodule, section_panel, select_option, shortcut_text, should_handle_wheel, show_result_page, terminal_frame


def manage_model_keys_interactively(path: Path) -> None:
    while True:
        choice = select_option("模型 Key", [("1", "添加 Key"), ("2", "管理 Key"), ("3", "Key 排序"), ("4", "路由模式"), ("5", "推理强度"), ("0", "返回")])
        if choice == "0":
            return
        if choice == "1":
            clear_terminal_history()
            result = add_config_interactively(path, ask_continue=False)
            if result is not None:
                show_result_page("添加 Key", result)
        elif choice == "2":
            run_submodule(lambda: manage_selected_key_interactively(path))
        elif choice == "3":
            clear_terminal_history()
            result = reorder_api_keys_interactively(path)
            if result is not None:
                show_result_page("Key 排序", result)
        elif choice == "4":
            clear_terminal_history()
            result = set_model_routing_mode_interactively(path)
            if result is not None:
                show_result_page("路由模式", result)
        elif choice == "5":
            clear_terminal_history()
            result = set_model_reasoning_effort_interactively(path)
            if result is not None:
                show_result_page("推理强度", result)


def manage_selected_key_interactively(path: Path) -> None:
    while True:
        selection = select_api_key(path, "选择要管理的 Key")
        if selection is None:
            return
        data, model, key_index = selection
        key = model["keys"][key_index]
        key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
        enabled = key.get("enabled", True)
        status_text = "[green]启用[/green]" if enabled else "[red]禁用[/red]"

        while True:
            choice = select_option(
                f"管理 Key · {short_text(key_name, 24)}",
                [("1", "编辑"), ("2", "删除"), ("3", "复制 API key"), ("4", "禁用" if enabled else "启用"), ("0", "返回")],
                content=section_panel(
                    f"模型: [bold]{short_text(model['id'], 32)}[/bold]\n"
                    f"Key: [bold]{short_text(key_name, 32)}[/bold]\n"
                    f"上游: [bold]{compact_url(key.get('base_url') or '-', 48)}[/bold]\n"
                    f"状态: {status_text}",
                    "Key 信息", "cyan"
                )
            )
            if choice == "0":
                break
            clear_terminal_history()
            if choice == "1":
                result = edit_selected_key_interactively(path, data, model, key_index)
            elif choice == "2":
                result = delete_selected_key_interactively(path, data, model, key_index)
                if result is not None:
                    show_result_page("删除 API key", result)
                    return  # key已删除，返回选择列表
                continue
            elif choice == "3":
                result = copy_selected_key_interactively(data, model, key_index)
            elif choice == "4":
                result = toggle_selected_key_interactively(path, data, model, key_index)
            else:
                continue
            if result is not None:
                show_result_page("编辑" if choice == "1" else ("复制 API key" if choice == "3" else "Key 开关"), result)
            if choice in ("1", "4"):
                # 编辑或切换状态后刷新数据
                data = load_config_data(path)
                model = find_model(data.get("models", []), model["id"])
                if model is None or key_index >= len(model.get("keys", [])):
                    return
                key = model["keys"][key_index]
                key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
                enabled = key.get("enabled", True)
                status_text = "[green]启用[/green]" if enabled else "[red]禁用[/red]"


def edit_selected_key_interactively(path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    old_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    key_name = Prompt.ask("Key 名称", default=old_name).strip() or old_name
    key["name"] = key_name
    key["base_url"] = Prompt.ask("上游 base_url", default=str(key.get("base_url") or data.get("default_base_url") or "https://api.openai.com")).strip()
    api_key = Prompt.ask("新 API key（留空则不修改）", default="", password=True).strip()
    if api_key:
        key["api_key"] = api_key
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    return Group(section_panel(f"已更新配置文件: [bold]{path}[/bold]\n模型: [bold]{model['id']}[/bold]\nKey: [bold]{key_name}[/bold]", "编辑完成", "green"), restart_service_after_config_change(path, old_config, new_config))


def delete_selected_key_interactively(path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    if not confirm_choice(f"确认删除模型 {model['id']} 的 Key {key_name}？", default=False):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除取消", "yellow")
    del model["keys"][key_index]
    if not model["keys"]:
        data["models"].remove(model)
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型 [bold]{model['id']}[/bold] 已无 API key，已一并移除。"
    else:
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型: [bold]{model['id']}[/bold]"
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    return Group(section_panel(message, "删除完成", "green"), restart_service_after_config_change(path, old_config, new_config))


def copy_selected_key_interactively(data: dict[str, Any], model: dict[str, Any], key_index: int) -> ResultPage:
    key = model["keys"][key_index]
    api_key = str(key.get("api_key") or "")
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    base_url = compact_url(key.get("base_url") or data.get("default_base_url") or "-", 48)
    content = section_panel(f"模型: [bold]{short_text(model['id'], 48)}[/bold]\nKey: [bold]{short_text(key_name, 48)}[/bold]\n上游: [bold]{base_url}[/bold]\n指纹: [bold]{key_fingerprint(api_key)}[/bold]\n\n选择“复制 API key”即可写入剪贴板。", "复制 API key", "green")
    return ResultPage(content, copy_text=api_key, copy_label="复制 API key")


def toggle_selected_key_interactively(path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    current_enabled = key.get("enabled", True)
    new_enabled = not current_enabled
    key["enabled"] = new_enabled
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    old_status = "启用" if current_enabled else "禁用"
    new_status = "启用" if new_enabled else "禁用"
    return Group(section_panel(f"已切换 Key 状态。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\nKey: [bold]{key_name}[/bold]\n原状态: [bold]{old_status}[/bold]\n新状态: [bold]{new_status}[/bold]", "Key 开关", "green"), restart_service_after_config_change(path, old_config, new_config))


def manage_config_transfer_interactively(path: Path) -> None:
    while True:
        choice = select_option("配置迁移", [("1", "复制配置文件"), ("2", "粘贴并应用"), ("0", "返回")])
        if choice == "0":
            return
        clear_terminal_history()
        result = export_config_interactively(path) if choice == "1" else paste_config_interactively(path)
        if result is not None:
            show_result_page("配置迁移", result)


def export_config_interactively(path: Path) -> ResultPage:
    data = load_config_data(path)
    config_text = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
    model_count = len(data.get("models", []))
    key_count = sum(len(model.get("keys", [])) for model in data.get("models", []))
    content = section_panel(f"配置文件: [bold]{path.resolve()}[/bold]\n模型数量: [bold]{model_count}[/bold]\nKey 数量: [bold]{key_count}[/bold]\n\n[bold yellow]复制内容包含本地鉴权 key 和上游 API key，请仅粘贴到可信终端。[/bold yellow]\n\n在另一台机器或另一个 TUI 中进入“CLI 设置 → 配置迁移 → 粘贴并应用”即可覆盖应用。", "复制配置文件", "green")
    return ResultPage(content, copy_text=config_text, copy_label="复制配置文件")


def paste_config_interactively(path: Path) -> Any:
    pasted, text = paste_from_clipboard()
    if not pasted:
        return section_panel(text, "粘贴失败", "yellow")
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        return section_panel(f"剪贴板内容不是有效 JSON: {exc.msg}", "应用失败", "red")
    if not isinstance(data, dict):
        return section_panel("剪贴板内容必须是配置对象。", "应用失败", "red")
    try:
        new_config = RouterConfig.from_dict(data)
    except (KeyError, TypeError, ValueError) as exc:
        return section_panel(f"配置校验失败: {exc}", "应用失败", "red")
    if not confirm_choice(f"将用剪贴板配置覆盖当前配置文件：{path.resolve()}，是否继续？", default=False):
        return section_panel("配置未变化。", "应用取消", "yellow")
    old_config = RouterConfig.from_dict(load_config_data(path))
    save_config_data(path, data)
    model_count = len(new_config.models)
    key_count = sum(len(model.keys) for model in new_config.models)
    content = section_panel(f"已应用剪贴板配置。\n配置文件: [bold]{path.resolve()}[/bold]\n模型数量: [bold]{model_count}[/bold]\nKey 数量: [bold]{key_count}[/bold]", "应用完成", "green")
    return Group(content, restart_service_after_config_change(path, old_config, new_config))


def select_model_id_for_new_key(models: list[dict[str, Any]]) -> str | None:
    if not models:
        return Prompt.ask("新模型 ID").strip()

    options = [
        (str(index + 1), f"{short_text(model.get('id') or '-', 32)} · {len(model.get('keys', []))} Key")
        for index, model in enumerate(models)
    ]
    options.extend([("n", "自定义添加新的模型 ID"), ("0", "返回")])
    choice = select_option("选择模型", options)
    if choice == "0":
        return None
    if choice == "n":
        return Prompt.ask("新模型 ID").strip()
    return str(models[int(choice) - 1].get("id") or "").strip()


def configured_base_urls(data: dict[str, Any], selected_model: dict[str, Any]) -> list[str]:
    base_urls: list[str] = []

    def append(value: Any) -> None:
        base_url = str(value or "").strip()
        if base_url and base_url not in base_urls:
            base_urls.append(base_url)

    for key in selected_model.get("keys", []):
        append(key.get("base_url"))
    for model in data.get("models", []):
        for key in model.get("keys", []):
            append(key.get("base_url"))
    append(data.get("default_base_url") or "https://api.openai.com")
    return base_urls


def select_base_url_for_new_key(data: dict[str, Any], selected_model: dict[str, Any]) -> str | None:
    base_urls = configured_base_urls(data, selected_model)
    options = [(str(index + 1), compact_url(base_url, 48)) for index, base_url in enumerate(base_urls)]
    options.extend([("n", "自定义添加新的上游 URL"), ("0", "返回")])
    choice = select_option("选择上游 URL", options)
    if choice == "0":
        return None
    if choice == "n":
        default_base_url = str(data.get("default_base_url") or "https://api.openai.com")
        return Prompt.ask("新的上游 base_url", default=default_base_url).strip()
    return base_urls[int(choice) - 1]


def add_config_interactively(path: Path, ask_continue: bool = True) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    models = data.setdefault("models", [])
    model_id = select_model_id_for_new_key(models)
    if model_id is None:
        return None
    if not model_id:
        return section_panel("[red]模型 ID 不能为空[/red]", "添加失败", "red")

    model = find_model(models, model_id)
    is_new_model = model is None
    if model is None:
        model = {"id": model_id, "aliases": [], "routing_mode": "round_robin", "keys": []}
        models.append(model)
        console.print(f"[green]已创建模型配置:[/green] {model_id}")

    if is_new_model:
        aliases_text = Prompt.ask("显示名称/别名，多个用逗号分隔", default=",".join(model.get("aliases", []))).strip()
        model["aliases"] = [alias.strip() for alias in aliases_text.split(",") if alias.strip()] if aliases_text else []
        model["routing_mode"] = Prompt.ask("路由模式：priority=优先级，round_robin=分流，only_first=仅首个", choices=["priority", "round_robin", "only_first"], default=str(model.get("routing_mode") or "round_robin")).strip()
        reasoning_effort = Prompt.ask("推理强度：downstream=由下游决定，none=关闭 reasoning，minimal/low/medium/high/xhigh", choices=["downstream", "none", "minimal", "low", "medium", "high", "xhigh"], default=normalize_reasoning_effort_choice(model.get("reasoning_effort"))).strip()
        if reasoning_effort == "downstream":
            model.pop("reasoning_effort", None)
        else:
            model["reasoning_effort"] = reasoning_effort
    keys = model.setdefault("keys", [])
    default_key_name = f"{model_id}-key-{len(keys) + 1}"
    key_name = Prompt.ask("Key 名称", default=default_key_name).strip() or default_key_name
    base_url = select_base_url_for_new_key(data, model)
    if base_url is None:
        return None
    if not base_url:
        return section_panel("[red]上游 base_url 不能为空[/red]", "添加失败", "red")
    api_key = Prompt.ask("API key", password=True).strip()
    if not api_key:
        return section_panel("[red]API key 不能为空[/red]", "添加失败", "red")

    keys.append({"name": key_name, "api_key": api_key, "base_url": base_url})
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    result = Group(section_panel(f"已写入配置文件: [bold]{path}[/bold]\n模型: [bold]{model_id}[/bold]\nKey: [bold]{key_name}[/bold]", "添加完成", "green"), restart_service_after_config_change(path, old_config, new_config))
    if ask_continue and not confirm_choice("继续启动服务？", default=False):
        raise SystemExit(0)
    return result


def edit_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要编辑的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    old_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    key_name = Prompt.ask("Key 名称", default=old_name).strip() or old_name
    key["name"] = key_name
    key["base_url"] = Prompt.ask("上游 base_url", default=str(key.get("base_url") or data.get("default_base_url") or "https://api.openai.com")).strip()
    api_key = Prompt.ask("新 API key（留空则不修改）", default="", password=True).strip()
    if api_key:
        key["api_key"] = api_key
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    return Group(section_panel(f"已更新配置文件: [bold]{path}[/bold]\n模型: [bold]{model['id']}[/bold]\nKey: [bold]{key_name}[/bold]", "编辑完成", "green"), restart_service_after_config_change(path, old_config, new_config))


def delete_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要删除的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    if not confirm_choice(f"确认删除模型 {model['id']} 的 Key {key_name}？", default=False):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除取消", "yellow")
    del model["keys"][key_index]
    if not model["keys"]:
        data["models"].remove(model)
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型 [bold]{model['id']}[/bold] 已无 API key，已一并移除。"
    else:
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型: [bold]{model['id']}[/bold]"
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    return Group(section_panel(message, "删除完成", "green"), restart_service_after_config_change(path, old_config, new_config))


def copy_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要复制的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    key = model["keys"][key_index]
    api_key = str(key.get("api_key") or "")
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    base_url = compact_url(key.get("base_url") or data.get("default_base_url") or "-", 48)
    content = section_panel(f"模型: [bold]{short_text(model['id'], 48)}[/bold]\nKey: [bold]{short_text(key_name, 48)}[/bold]\n上游: [bold]{base_url}[/bold]\n指纹: [bold]{key_fingerprint(api_key)}[/bold]\n\n选择“复制 API key”即可写入剪贴板。", "复制 API key", "green")
    return ResultPage(content, copy_text=api_key, copy_label="复制 API key")


def reorder_api_keys_interactively(path: Path) -> Any:
    data = load_config_data(path)
    selectable_models = [model for model in data.get("models", []) if len(model.get("keys", [])) > 1]
    if not selectable_models:
        return section_panel("[yellow]暂无可排序模型，需至少 2 个 Key。[/yellow]", "Key 排序", "yellow")
    model_options = [(str(index + 1), f"{short_text(model['id'], 28)} · {len(model.get('keys', []))} Key") for index, model in enumerate(selectable_models)] + [("0", "返回")]
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    model = selectable_models[int(model_choice) - 1]
    old_config = RouterConfig.from_dict(data)
    keys = model.get("keys", [])
    selected = 0
    while True:
        action, selected = select_reorder_key_action(model, selected)
        if action == "cancel":
            return section_panel("[yellow]配置未变化。[/yellow]", "Key 排序", "yellow")
        if action == "save":
            new_config = RouterConfig.from_dict(data)
            save_config_data(path, data)
            return Group(section_panel(key_order_text(model), "顺序已保存", "green"), restart_service_after_config_change(path, old_config, new_config))
        if action == "up" and selected > 0:
            keys[selected - 1], keys[selected] = keys[selected], keys[selected - 1]
            selected -= 1
        if action == "down" and selected < len(keys) - 1:
            keys[selected + 1], keys[selected] = keys[selected], keys[selected + 1]
            selected += 1


def toggle_key_enabled_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要切换状态的 Key")
    if selection is None:
        return None
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    current_enabled = key.get("enabled", True)
    new_enabled = not current_enabled
    key["enabled"] = new_enabled
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    old_status = "启用" if current_enabled else "禁用"
    new_status = "启用" if new_enabled else "禁用"
    return Group(section_panel(f"已切换 Key 状态。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\nKey: [bold]{key_name}[/bold]\n原状态: [bold]{old_status}[/bold]\n新状态: [bold]{new_status}[/bold]", "Key 开关", "green"), restart_service_after_config_change(path, old_config, new_config))


def select_reorder_key_action(model: dict[str, Any], selected: int = 0) -> tuple[str, int]:
    last_wheel_key: str | None = None
    last_wheel_at = 0.0
    with mouse_wheel_mode(), Live(render_key_order_menu(model, selected), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key()
            if key == "cancel":
                return "cancel", selected
            if key in {"scroll_up", "scroll_down"}:
                handle_wheel, last_wheel_key, last_wheel_at = should_handle_wheel(key, last_wheel_key, last_wheel_at)
                if not handle_wheel:
                    continue
            if key in {"up", "scroll_up"}:
                selected = max(0, selected - 1)
                live.update(render_key_order_menu(model, selected), refresh=True)
                continue
            if key in {"down", "scroll_down"}:
                selected = min(len(model.get("keys", [])) - 1, selected + 1)
                live.update(render_key_order_menu(model, selected), refresh=True)
                continue
            if key in {"w", "W"}:
                return "up", selected
            if key in {"s", "S"}:
                return "down", selected
            if key == "enter":
                return "save", selected


def render_key_order_menu(model: dict[str, Any], selected: int) -> Group:
    table = Table(show_header=False, box=None, padding=(0, 1), expand=True)
    table.add_column("指示", justify="center", width=3)
    table.add_column("顺序", justify="center", width=5)
    table.add_column("Key", ratio=1)
    table.add_column("上游", ratio=1)
    for index, key in enumerate(model.get("keys", [])):
        name = short_text(key.get("name") or f"{model['id']}-{index + 1}", 28)
        base_url = compact_url(key.get("base_url") or "-", 28)
        if index == selected:
            table.add_row("[bold cyan]▶[/bold cyan]", f"[bold black on cyan] {index + 1} [/bold black on cyan]", f"[bold cyan]{name}[/bold cyan]", f"[bold cyan]{base_url}[/bold cyan]")
        else:
            table.add_row("", f"[dim]{index + 1}[/dim]", name, base_url)
    return terminal_frame([page_title("Key 排序", f"模型 · {short_text(model['id'], 24)}"), section_panel(table, "Key 顺序", "cyan", "[dim]选择 Key 后用 W/S 调整优先级[/dim]")], shortcut_text("↑/↓ 选择  ·  W/S 移动  ·  Enter 保存  ·  Ctrl+C 取消" if sys.platform != "win32" else "↑/↓/滚轮 选择  ·  W/S 移动  ·  Enter 保存  ·  Ctrl+C 取消"))


def key_order_text(model: dict[str, Any]) -> str:
    lines = [f"模型: [bold]{short_text(model['id'], 32)}[/bold]", "当前顺序:"]
    for index, key in enumerate(model.get("keys", [])):
        name = short_text(key.get("name") or f"{model['id']}-{index + 1}", 32)
        base_url = compact_url(key.get("base_url") or "-", 32)
        lines.append(f"{index + 1}. {name} · {base_url}")
    return "\n".join(lines)


def set_model_routing_mode_interactively(path: Path) -> Any:
    data = load_config_data(path)
    models = data.get("models", [])
    if not models:
        return section_panel("[yellow]还没有模型配置。[/yellow]", "路由模式", "yellow")
    model_options = []
    for index, model in enumerate(models):
        routing_mode = str(model.get("routing_mode") or data.get("routing_mode") or "round_robin")
        routing_mode_text = routing_mode_display_text(routing_mode)
        model_options.append((str(index + 1), f"{short_text(model['id'], 28)} · {routing_mode_text}"))
    model_options.append(("0", "返回"))
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    old_config = RouterConfig.from_dict(data)
    model = models[int(model_choice) - 1]
    current_mode = str(model.get("routing_mode") or data.get("routing_mode") or "round_robin")
    mode_choice = select_option("选择路由模式", [("1", "分流：轮询"), ("2", "优先级：按顺序"), ("3", "仅首个：只重试第一个 Key"), ("0", "返回")], selected={"round_robin": 0, "priority": 1, "only_first": 2}.get(current_mode, 0))
    if mode_choice == "0":
        return section_panel("[yellow]配置未变化。[/yellow]", "路由模式", "yellow")
    new_mode = {"1": "round_robin", "2": "priority", "3": "only_first"}[mode_choice]
    if new_mode == current_mode:
        mode_text = routing_mode_display_text(new_mode)
        return section_panel(f"模型 [bold]{short_text(model['id'], 32)}[/bold] 已是 [bold]{mode_text}[/bold]。", "路由模式", "yellow")
    model["routing_mode"] = new_mode
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    old_mode_text = routing_mode_display_text(current_mode)
    new_mode_text = routing_mode_display_text(new_mode)
    return Group(section_panel(f"已更新路由模式。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n原模式: [bold]{old_mode_text}[/bold]\n新模式: [bold]{new_mode_text}[/bold]", "路由模式", "green"), restart_service_after_config_change(path, old_config, new_config))


def routing_mode_display_text(value: str | None) -> str:
    return {"round_robin": "分流", "priority": "优先级", "only_first": "仅首个"}.get(str(value or "round_robin"), "分流")


def set_model_reasoning_effort_interactively(path: Path) -> Any:
    data = load_config_data(path)
    models = data.get("models", [])
    if not models:
        return section_panel("[yellow]还没有模型配置。[/yellow]", "推理强度", "yellow")
    model_options = []
    for index, model in enumerate(models):
        effort = normalize_reasoning_effort_choice(model.get("reasoning_effort"))
        model_options.append((str(index + 1), f"{short_text(model['id'], 28)} · {reasoning_effort_text(effort)}"))
    model_options.append(("0", "返回"))
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    old_config = RouterConfig.from_dict(data)
    model = models[int(model_choice) - 1]
    current_effort = normalize_reasoning_effort_choice(model.get("reasoning_effort"))
    effort_choice = select_option("选择推理强度", [("1", "由下游决定"), ("2", "关闭 reasoning"), ("3", "minimal"), ("4", "low"), ("5", "medium"), ("6", "high"), ("7", "xhigh"), ("0", "返回")], selected={"downstream": 0, "none": 1, "minimal": 2, "low": 3, "medium": 4, "high": 5, "xhigh": 6}.get(current_effort, 0))
    if effort_choice == "0":
        return section_panel("[yellow]配置未变化。[/yellow]", "推理强度", "yellow")
    new_effort = {"1": "downstream", "2": "none", "3": "minimal", "4": "low", "5": "medium", "6": "high", "7": "xhigh"}[effort_choice]
    if new_effort == current_effort:
        return section_panel(f"模型 [bold]{short_text(model['id'], 32)}[/bold] 已是 [bold]{reasoning_effort_text(new_effort)}[/bold]。", "推理强度", "yellow")
    if new_effort == "downstream":
        model.pop("reasoning_effort", None)
    else:
        model["reasoning_effort"] = new_effort
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    return Group(section_panel(f"已更新推理强度。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n原强度: [bold]{reasoning_effort_text(current_effort)}[/bold]\n新强度: [bold]{reasoning_effort_text(new_effort)}[/bold]", "推理强度", "green"), restart_service_after_config_change(path, old_config, new_config))


def reasoning_effort_text(value: str | None) -> str:
    effort = normalize_reasoning_effort_choice(value)
    return {"downstream": "由下游决定", "none": "关闭 reasoning", "minimal": "minimal", "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh"}.get(effort, effort)


def normalize_reasoning_effort_choice(value: Any) -> str:
    effort = str(value or "").strip()
    return "downstream" if effort in {"", "default", "downstream"} else effort


def select_api_key(path: Path, title: str) -> tuple[dict[str, Any], dict[str, Any], int] | None:
    data = load_config_data(path)
    selectable_models = [model for model in data.get("models", []) if model.get("keys")]
    if not selectable_models:
        return None
    model_options = [(str(index + 1), f"{short_text(model['id'], 28)} · {len(model.get('keys', []))} Key") for index, model in enumerate(selectable_models)] + [("0", "返回")]
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    model = selectable_models[int(model_choice) - 1]
    key_options = []
    for index, key in enumerate(model.get("keys", [])):
        name = short_text(key.get("name") or f"{model['id']}-{index + 1}", 28)
        base_url = compact_url(key.get("base_url") or data.get("default_base_url") or "-", 28)
        enabled = key.get("enabled", True)
        status = "[green]启用[/green]" if enabled else "[red]禁用[/red]"
        key_options.append((str(index + 1), f"{name} · {base_url} · {status}"))
    key_options.append(("0", "返回"))
    key_choice = select_option(title, key_options)
    if key_choice == "0":
        return None
    return data, model, int(key_choice) - 1


def set_local_api_key_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    if data.get("local_api_key") and not confirm_choice("是否重置本地鉴权密钥？", default=True):
        return section_panel("[yellow]配置未变化。[/yellow]", "本地鉴权", "yellow")
    local_api_key = generate_local_api_key()
    data["local_api_key"] = local_api_key
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    content = Group(section_panel(f"已生成新密钥。\n\n[bold]{local_api_key}[/bold]\n\n请求时添加：\nAuthorization: Bearer <key>", "本地鉴权", "green"), restart_service_after_config_change(path, old_config, new_config))
    return ResultPage(content, copy_text=local_api_key, copy_label="复制本地鉴权 key")


def set_listen_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    current_host = str(data.get("host") or "127.0.0.1")
    current_port = int(data.get("port") or 8000)
    host = Prompt.ask("监听 IP/地址", default=current_host).strip()
    if not host:
        return section_panel("[red]监听 IP/地址不能为空。[/red]", "监听配置", "red")
    if "://" in host or "/" in host:
        return section_panel("[red]监听地址只填写 IP 或主机名，不要包含协议或路径。[/red]", "监听配置", "red")
    port_text = Prompt.ask("监听端口", default=str(current_port)).strip()
    try:
        port = int(port_text)
    except ValueError:
        return section_panel("[red]端口必须是数字。[/red]", "监听配置", "red")
    if port < 1 or port > 65535:
        return section_panel("[red]端口范围必须是 1-65535。[/red]", "监听配置", "red")
    if host == current_host and port == current_port:
        return section_panel(f"监听配置未变化: [bold]{host}:{port}[/bold]", "监听配置", "yellow")
    if host == "0.0.0.0" and host != current_host and not confirm_choice("0.0.0.0 会允许局域网/公网访问，确认继续？", default=False):
        return section_panel("[yellow]配置未变化。[/yellow]", "监听配置", "yellow")
    data["host"] = host
    data["port"] = port
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    warning = "\n[bold red]风险提示: 0.0.0.0 会暴露到所有可达网络，请确保防火墙和本地鉴权已正确配置。[/bold red]" if host == "0.0.0.0" else ""
    return Group(section_panel(f"已更新监听配置。\n配置文件: [bold]{path}[/bold]\n旧配置: [bold]{current_host}:{current_port}[/bold]\n新配置: [bold]{host}:{port}[/bold]{warning}", "监听配置", "green"), restart_service_after_config_change(path, old_config, new_config))

def load_config_data(path: Path) -> dict[str, Any]:
    if not path.exists():
        data = empty_config_dict()
        save_config_data(path, data)
        return data
    return json.loads(path.read_text(encoding="utf-8"))


def save_config_data(path: Path, data: dict[str, Any]) -> None:
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def find_model(models: list[dict[str, Any]], model_id: str) -> dict[str, Any] | None:
    for model in models:
        if model.get("id") == model_id:
            return model
    return None
