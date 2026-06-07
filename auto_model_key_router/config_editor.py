from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from rich import box
from rich.console import Group
from rich.live import Live
from rich.prompt import Prompt
from rich.table import Table

from .config import RouterConfig, empty_config_dict, generate_local_api_key
from .formatting import compact_url, short_text
from .service import restart_service_after_config_change
from .tui import clear_terminal_history, confirm_choice, console, page_title, read_key, section_panel, select_option, shortcut_text, show_result_page


def manage_model_keys_interactively(path: Path) -> None:
    while True:
        choice = select_option("模型 Key", [("1", "添加 Key"), ("2", "编辑 API key"), ("3", "删除 API key"), ("4", "Key 排序"), ("5", "路由模式"), ("0", "返回")])
        if choice == "0":
            return
        actions = {
            "1": ("添加 Key", lambda: add_config_interactively(path, ask_continue=False)),
            "2": ("编辑 API key", lambda: edit_api_key_interactively(path)),
            "3": ("删除 API key", lambda: delete_api_key_interactively(path)),
            "4": ("Key 排序", lambda: reorder_api_keys_interactively(path)),
            "5": ("路由模式", lambda: set_model_routing_mode_interactively(path)),
        }
        title, action = actions[choice]
        clear_terminal_history()
        result = action()
        if result is not None:
            show_result_page(title, result)


def add_config_interactively(path: Path, ask_continue: bool = True) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    model_id = Prompt.ask("模型 ID").strip()
    if not model_id:
        return section_panel("[red]模型 ID 不能为空[/red]", "添加失败", "red")

    models = data.setdefault("models", [])
    model = find_model(models, model_id)
    if model is None:
        model = {"id": model_id, "aliases": [], "routing_mode": "round_robin", "keys": []}
        models.append(model)
        console.print(f"[green]已创建模型配置:[/green] {model_id}")

    aliases_text = Prompt.ask("显示名称/别名，多个用逗号分隔", default=",".join(model.get("aliases", []))).strip()
    model["aliases"] = [alias.strip() for alias in aliases_text.split(",") if alias.strip()] if aliases_text else []
    model["routing_mode"] = Prompt.ask("路由模式：priority=优先级，round_robin=分流", choices=["priority", "round_robin"], default=str(model.get("routing_mode") or "round_robin")).strip()
    keys = model.setdefault("keys", [])
    default_key_name = f"{model_id}-key-{len(keys) + 1}"
    key_name = Prompt.ask("Key 名称", default=default_key_name).strip() or default_key_name
    base_url = Prompt.ask("上游 base_url", default=str(data.get("default_base_url") or "https://api.openai.com")).strip()
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


def select_reorder_key_action(model: dict[str, Any], selected: int = 0) -> tuple[str, int]:
    with Live(render_key_order_menu(model, selected), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key()
            if key == "cancel":
                return "cancel", selected
            if key == "up":
                selected = max(0, selected - 1)
                live.update(render_key_order_menu(model, selected), refresh=True)
            if key == "down":
                selected = min(len(model.get("keys", [])) - 1, selected + 1)
                live.update(render_key_order_menu(model, selected), refresh=True)
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
    return Group(page_title("Key 排序", f"模型 · {short_text(model['id'], 24)}"), section_panel(table, "Key 顺序", "cyan", "[dim]选择 Key 后用 W/S 调整优先级[/dim]"), shortcut_text("↑/↓ 选择  ·  W/S 移动  ·  Enter 保存  ·  Esc 取消"))


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
        routing_mode_text = "优先级" if routing_mode == "priority" else "分流"
        model_options.append((str(index + 1), f"{short_text(model['id'], 28)} · {routing_mode_text}"))
    model_options.append(("0", "返回"))
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    old_config = RouterConfig.from_dict(data)
    model = models[int(model_choice) - 1]
    current_mode = str(model.get("routing_mode") or data.get("routing_mode") or "round_robin")
    mode_choice = select_option("选择路由模式", [("1", "分流：轮询"), ("2", "优先级：按顺序"), ("0", "返回")], selected=1 if current_mode == "priority" else 0)
    if mode_choice == "0":
        return section_panel("[yellow]配置未变化。[/yellow]", "路由模式", "yellow")
    new_mode = "priority" if mode_choice == "2" else "round_robin"
    if new_mode == current_mode:
        mode_text = "优先级" if new_mode == "priority" else "分流"
        return section_panel(f"模型 [bold]{short_text(model['id'], 32)}[/bold] 已是 [bold]{mode_text}[/bold]。", "路由模式", "yellow")
    model["routing_mode"] = new_mode
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    old_mode_text = "优先级" if current_mode == "priority" else "分流"
    new_mode_text = "优先级" if new_mode == "priority" else "分流"
    return Group(section_panel(f"已更新路由模式。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n原模式: [bold]{old_mode_text}[/bold]\n新模式: [bold]{new_mode_text}[/bold]", "路由模式", "green"), restart_service_after_config_change(path, old_config, new_config))


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
        key_options.append((str(index + 1), f"{name} · {base_url}"))
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
    return Group(section_panel(f"已生成新密钥。\n\n[bold]{local_api_key}[/bold]\n\n请求时添加：\nAuthorization: Bearer <key>", "本地鉴权", "green"), restart_service_after_config_change(path, old_config, new_config))


def set_port_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    current_port = int(data.get("port") or 8000)
    port_text = Prompt.ask("监听端口", default=str(current_port)).strip()
    try:
        port = int(port_text)
    except ValueError:
        return section_panel("[red]端口必须是数字。[/red]", "监听端口", "red")
    if port < 1 or port > 65535:
        return section_panel("[red]端口范围必须是 1-65535。[/red]", "监听端口", "red")
    if port == current_port:
        return section_panel(f"监听端口未变化: [bold]{port}[/bold]", "监听端口", "yellow")
    data["port"] = port
    new_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    return Group(section_panel(f"已更新监听端口。\n配置文件: [bold]{path}[/bold]\n旧端口: [bold]{current_port}[/bold]\n新端口: [bold]{port}[/bold]", "监听端口", "green"), restart_service_after_config_change(path, old_config, new_config))


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
