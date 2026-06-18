from __future__ import annotations

from copy import deepcopy
import json
import sys
from pathlib import Path
from typing import Any

import httpx
from rich.console import Group
from rich.live import Live
from rich.table import Table

from .config import (
    UPSTREAM_ROUTE_DEFAULT_PATHS,
    UPSTREAM_ROUTE_LABELS,
    UPSTREAM_ROUTE_MODES,
    RouterConfig,
    generate_local_api_key,
    load_config_data,
    normalize_upstream_route_path,
    upstream_route_path,
)
from .config_service import commit_config_data
from .formatting import compact_url, key_fingerprint, short_text
from .proxy_support import _join_url
from .service import restart_service_after_config_change
from .tui import (
    ResultPage,
    clear_terminal_history,
    confirm_choice,
    console,
    content_scroll_offset,
    mouse_wheel_mode,
    open_config_file,
    page_title,
    posix_input_mode,
    prompt_text,
    read_key_responsive,
    run_submodule,
    section_panel,
    select_multiple,
    select_option,
    shortcut_text,
    should_handle_wheel,
    show_result_page,
    terminal_frame_state,
)
from .visitor import visitor_feature_available


VISITOR_KEY_STYLE = "bold bright_magenta"


def key_display_name(key: dict[str, Any], fallback: str, width: int = 28) -> str:
    name = str(key.get("name") or fallback)
    if key.get("allow_visitor", False):
        return f"[{VISITOR_KEY_STYLE}]{short_text(name, width)}[/]"
    return short_text(name, width)


def format_visitor_status_text(visitor_allowed: bool, visitor_installed: bool) -> str:
    if visitor_installed:
        return (
            f"[{VISITOR_KEY_STYLE}]允许[/]" if visitor_allowed else "[dim]禁止[/dim]"
        )
    if visitor_allowed:
        return f"[{VISITOR_KEY_STYLE}]已配置，但 visitor extra 未安装[/]"
    return "[dim]功能未安装[/dim]"


def key_upstream_routes(key: dict[str, Any]) -> dict[str, str]:
    routes = key.get("upstream_routes")
    return dict(routes) if isinstance(routes, dict) else {}


def upstream_route_support_text(
    key: dict[str, Any], mode: str, *, native_first: bool = True
) -> str:
    routes = key_upstream_routes(key)
    path = upstream_route_path(routes, mode)
    if mode in routes:
        return f"[green]自定义原生[/green] {path}"
    if mode == "openai":
        return f"[green]原生[/green] {path}"
    if mode == "anthropic" and native_first:
        return f"[cyan]自动探测[/cyan] {path}"
    return (
        f"[yellow]转换[/yellow] {UPSTREAM_ROUTE_DEFAULT_PATHS['openai']}"
    )


def upstream_routes_panel(
    data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    native_first = bool(model.get("native_first", True))
    table = Table(show_header=True, box=None, expand=True)
    table.add_column("模式", style="cyan", ratio=1)
    table.add_column("原生支持/上游路径", ratio=3)
    for mode in UPSTREAM_ROUTE_MODES:
        table.add_row(
            UPSTREAM_ROUTE_LABELS[mode],
            upstream_route_support_text(key, mode, native_first=native_first),
        )
    base_url = compact_url(
        key.get("base_url") or data.get("default_base_url") or "-", 56
    )
    return Group(
        section_panel(
            f"模型: [bold]{short_text(model['id'], 48)}[/bold]\n"
            f"Key: {key_display_name(key, key_name, 48)}\n"
            f"上游: [bold]{base_url}[/bold]",
            "Key 信息",
            "cyan",
        ),
        section_panel(
            table,
            "三种模式原生支持",
            "magenta",
            "[dim]自定义值可填路径前缀，如 anthropic/，或完整路径 anthropic/v1/messages[/dim]",
        ),
    )


def _open_config_on_key(path: Path, key: str) -> str | None:
    if key in {"o", "O"}:
        open_config_file(path)
    return None


def manage_model_keys_interactively(path: Path) -> None:
    on_key = lambda key: _open_config_on_key(path, key)
    while True:
        choice = select_option(
            "模型 Key",
            [
                ("1", "添加 Key"),
                ("2", "管理 Key"),
                ("3", "Key 排序"),
                ("4", "路由模式"),
                ("5", "推理强度"),
                ("6", "模型别称"),
                ("7", "上游路由"),
                ("0", "返回"),
            ],
            on_key=on_key,
        )
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
        elif choice == "6":
            run_submodule(lambda: manage_model_aliases_interactively(path))
        elif choice == "7":
            run_submodule(lambda: manage_upstream_routes_interactively(path))


def manage_model_aliases_interactively(path: Path) -> None:
    on_key = lambda key: _open_config_on_key(path, key)
    while True:
        data = load_config_data(path)
        models = data.get("models", [])
        if not models:
            show_result_page(
                "模型别称",
                section_panel(
                    "[yellow]暂无可设置别称的模型。[/yellow]", "模型别称", "yellow"
                ),
            )
            return

        options = []
        for index, model in enumerate(models):
            aliases = model.get("aliases", [])
            alias_summary = (
                short_text(", ".join(str(alias) for alias in aliases), 28)
                if aliases
                else "无别称"
            )
            options.append(
                (
                    str(index + 1),
                    f"{short_text(model.get('id') or '-', 28)} · {alias_summary}",
                )
            )
        options.append(("0", "返回"))
        choice = select_option("选择要设置别称的模型", options, on_key=on_key)
        if choice == "0":
            return
        model_id = str(models[int(choice) - 1].get("id") or "")
        run_submodule(
            lambda: manage_selected_model_aliases_interactively(path, model_id)
        )


def manage_selected_model_aliases_interactively(path: Path, model_id: str) -> None:
    on_key = lambda key: _open_config_on_key(path, key)
    while True:
        data = load_config_data(path)
        model = find_model(data.get("models", []), model_id)
        if model is None:
            show_result_page(
                "模型别称",
                section_panel(f"[red]模型不存在: {model_id}[/red]", "模型别称", "red"),
            )
            return
        aliases = model.setdefault("aliases", [])
        options = [("1", "添加别称")]
        if aliases:
            options.extend([("2", "编辑别称"), ("3", "删除别称")])
        options.append(("0", "返回"))
        choice = select_option(
            f"模型别称 · {short_text(model_id, 28)}",
            options,
            content=model_aliases_panel(model),
            on_key=on_key,
        )
        if choice == "0":
            return

        clear_terminal_history()
        if choice == "1":
            result = add_model_alias_interactively(path, data, model)
            title = "添加别称"
        else:
            alias_index = select_model_alias(
                model, "选择要编辑的别称" if choice == "2" else "选择要删除的别称"
            )
            if alias_index is None:
                continue
            if choice == "2":
                result = edit_model_alias_interactively(path, data, model, alias_index)
                title = "编辑别称"
            else:
                result = delete_model_alias_interactively(
                    path, data, model, alias_index
                )
                title = "删除别称"
        if result is not None:
            show_result_page(title, result)


def model_aliases_panel(model: dict[str, Any]) -> Any:
    aliases = model.get("aliases", [])
    aliases_text = "\n".join(
        f"{index}. [bold]{short_text(str(alias), 64)}[/bold]"
        for index, alias in enumerate(aliases, 1)
    )
    if not aliases_text:
        aliases_text = "[dim]暂无别称[/dim]"
    return section_panel(
        f"模型 ID: [bold cyan]{short_text(model.get('id') or '-', 48)}[/bold cyan]\n\n{aliases_text}",
        "当前别称",
        "cyan",
    )


def select_model_alias(model: dict[str, Any], title: str) -> int | None:
    aliases = model.get("aliases", [])
    options = [
        (str(index + 1), short_text(str(alias), 48))
        for index, alias in enumerate(aliases)
    ]
    options.append(("0", "返回"))
    choice = select_option(title, options, content=model_aliases_panel(model))
    if choice == "0":
        return None
    return int(choice) - 1


def add_model_alias_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any]
) -> Any:
    alias = prompt_text("添加别称", "新别称").strip()
    if not alias:
        return section_panel("[red]模型别称不能为空。[/red]", "添加失败", "red")
    old_config = RouterConfig.from_dict(data)
    model.setdefault("aliases", []).append(alias)
    return save_model_alias_change(
        path, data, old_config, model, f"已添加别称: [bold]{alias}[/bold]", "添加完成"
    )


def edit_model_alias_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], alias_index: int
) -> Any:
    aliases = model.setdefault("aliases", [])
    old_alias = str(aliases[alias_index])
    alias = prompt_text("编辑别称", "模型别称", default=old_alias).strip()
    if not alias:
        return section_panel("[red]模型别称不能为空。[/red]", "编辑失败", "red")
    if alias == old_alias:
        return section_panel("[yellow]别称未变化。[/yellow]", "编辑别称", "yellow")
    old_config = RouterConfig.from_dict(data)
    aliases[alias_index] = alias
    replace_unified_model_alias(data, model, old_alias)
    return save_model_alias_change(
        path,
        data,
        old_config,
        model,
        f"原别称: [bold]{old_alias}[/bold]\n新别称: [bold]{alias}[/bold]",
        "编辑完成",
    )


def delete_model_alias_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], alias_index: int
) -> Any:
    aliases = model.setdefault("aliases", [])
    alias = str(aliases[alias_index])
    if not confirm_choice(
        f"确认删除模型 {model['id']} 的别称 {alias}？", default=False
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除取消", "yellow")
    old_config = RouterConfig.from_dict(data)
    del aliases[alias_index]
    replace_unified_model_alias(data, model, alias)
    return save_model_alias_change(
        path, data, old_config, model, f"已删除别称: [bold]{alias}[/bold]", "删除完成"
    )


def replace_unified_model_alias(
    data: dict[str, Any], model: dict[str, Any], alias: str
) -> None:
    unified_model = data.get("unified_model")
    if isinstance(unified_model, dict) and unified_model.get("model") == alias:
        unified_model["model"] = model["id"]


def save_model_alias_change(
    path: Path,
    data: dict[str, Any],
    old_config: RouterConfig,
    model: dict[str, Any],
    message: str,
    title: str,
) -> Any:
    try:
        new_config = commit_config_data(path, data, old_config).new_config
    except ValueError as exc:
        return section_panel(f"[red]{exc}[/red]", "别称设置失败", "red")
    return Group(
        section_panel(
            f"{message}\n模型: [bold]{model['id']}[/bold]\n配置文件: [bold]{path}[/bold]",
            title,
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def manage_selected_key_interactively(path: Path) -> None:
    on_key = lambda key: _open_config_on_key(path, key)
    while True:
        selection = select_api_key(path, "选择要管理的 Key")
        if selection is None:
            return
        data, model, key_index = selection
        key = model["keys"][key_index]
        key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
        enabled = key.get("enabled", True)
        status_text = "[green]启用[/green]" if enabled else "[red]禁用[/red]"
        visitor_allowed = bool(key.get("allow_visitor", False))
        visitor_installed = visitor_feature_available()
        visitor_status_text = format_visitor_status_text(
            visitor_allowed, visitor_installed
        )

        while True:
            options = [
                ("1", "编辑"),
                ("2", "删除"),
                ("3", "复制 API key"),
                ("4", "禁用" if enabled else "启用"),
            ]
            if visitor_installed:
                options.append(
                    ("5", "禁止访客访问" if visitor_allowed else "允许访客访问")
                )
            options.append(("0", "返回"))
            choice = select_option(
                f"管理 Key · {short_text(key_name, 24)}",
                options,
                content=section_panel(
                    f"模型: [bold]{short_text(model['id'], 32)}[/bold]\n"
                    f"Key: {key_display_name(key, key_name, 32)}\n"
                    f"上游: [bold]{compact_url(key.get('base_url') or '-', 48)}[/bold]\n"
                    f"状态: {status_text}\n"
                    f"访客访问: {visitor_status_text}",
                    "Key 信息",
                    "cyan",
                ),
                on_key=on_key,
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
            elif choice == "5":
                result = toggle_visitor_access_interactively(
                    path, data, model, key_index
                )
            else:
                continue
            if result is not None:
                result_title = {
                    "1": "编辑",
                    "3": "复制 API key",
                    "4": "Key 开关",
                    "5": "访客访问",
                }.get(choice, "Key 管理")
                show_result_page(result_title, result)
            if choice in ("1", "4", "5"):
                # 编辑或切换状态后刷新数据
                data = load_config_data(path)
                model = find_model(data.get("models", []), model["id"])
                if model is None or key_index >= len(model.get("keys", [])):
                    return
                key = model["keys"][key_index]
                key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
                enabled = key.get("enabled", True)
                status_text = "[green]启用[/green]" if enabled else "[red]禁用[/red]"
                visitor_allowed = bool(key.get("allow_visitor", False))
                visitor_installed = visitor_feature_available()
                visitor_status_text = format_visitor_status_text(
                    visitor_allowed, visitor_installed
                )


def manage_upstream_routes_interactively(path: Path) -> None:
    selection = select_api_key(path, "选择要配置上游路由的 Key")
    if selection is None:
        return
    data, model, key_index = selection
    while True:
        key = model["keys"][key_index]
        key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
        options = [
            (
                str(index + 1),
                f"{UPSTREAM_ROUTE_LABELS[mode]} · {short_text(upstream_route_path(key_upstream_routes(key), mode), 36)}",
            )
            for index, mode in enumerate(UPSTREAM_ROUTE_MODES)
        ]
        options.extend([("c", "清空自定义路由"), ("0", "返回")])
        choice = select_option(
            f"上游路由 · {short_text(key_name, 24)}",
            options,
            content=upstream_routes_panel(data, model, key_index),
        )
        if choice == "0":
            return

        old_config = RouterConfig.from_dict(data)
        routes = key_upstream_routes(key)
        try:
            if choice == "c":
                routes = {}
                message = "已清空自定义上游路由，恢复默认路径。"
            else:
                mode = UPSTREAM_ROUTE_MODES[int(choice) - 1]
                current_path = routes.get(mode, "")
                raw_path = prompt_text(
                    "上游路由",
                    f"{UPSTREAM_ROUTE_LABELS[mode]} 路径/前缀（留空恢复默认）",
                    default=current_path,
                ).strip()
                if raw_path:
                    routes[mode] = normalize_upstream_route_path(mode, raw_path)
                    message = (
                        f"已设置 {UPSTREAM_ROUTE_LABELS[mode]}: "
                        f"[bold]{routes[mode]}[/bold]"
                    )
                else:
                    routes.pop(mode, None)
                    message = f"已恢复 {UPSTREAM_ROUTE_LABELS[mode]} 默认路径。"
        except (IndexError, ValueError) as exc:
            show_result_page(
                "上游路由",
                section_panel(f"[red]{exc}[/red]", "配置失败", "red"),
            )
            continue

        if routes:
            key["upstream_routes"] = routes
        else:
            key.pop("upstream_routes", None)
        try:
            new_config = commit_config_data(path, data, old_config).new_config
        except (KeyError, TypeError, ValueError) as exc:
            show_result_page(
                "上游路由",
                section_panel(f"[red]{exc}[/red]", "配置失败", "red"),
            )
            continue
        show_result_page(
            "上游路由",
            Group(
                section_panel(
                    f"已更新配置文件: [bold]{path}[/bold]\n"
                    f"模型: [bold]{model['id']}[/bold]\n"
                    f"Key: [bold]{key_name}[/bold]\n{message}",
                    "配置完成",
                    "green",
                ),
                restart_service_after_config_change(path, old_config, new_config),
            ),
        )
        data = load_config_data(path)
        model = find_model(data.get("models", []), model["id"])
        if model is None or key_index >= len(model.get("keys", [])):
            return


def edit_selected_key_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    old_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    key_name = prompt_text("编辑 Key", "Key 名称", default=old_name).strip() or old_name
    key["name"] = key_name
    key["base_url"] = prompt_text(
        "编辑 Key",
        "上游 base_url",
        default=str(
            key.get("base_url")
            or data.get("default_base_url")
            or "https://api.openai.com"
        ),
    ).strip()
    api_key = prompt_text(
        "编辑 Key", "新 API key（留空则不修改）", default="", password=True
    ).strip()
    if api_key:
        key["api_key"] = api_key
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            f"已更新配置文件: [bold]{path}[/bold]\n模型: [bold]{model['id']}[/bold]\nKey: [bold]{key_name}[/bold]",
            "编辑完成",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def delete_selected_key_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    if not confirm_choice(
        f"确认删除模型 {model['id']} 的 Key {key_name}？", default=False
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除取消", "yellow")
    del model["keys"][key_index]
    if not model["keys"]:
        data["models"].remove(model)
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型 [bold]{model['id']}[/bold] 已无 API key，已一并移除。"
    else:
        message = (
            f"已删除 Key: [bold]{key_name}[/bold]\n模型: [bold]{model['id']}[/bold]"
        )
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(message, "删除完成", "green"),
        restart_service_after_config_change(path, old_config, new_config),
    )


def copy_selected_key_interactively(
    data: dict[str, Any], model: dict[str, Any], key_index: int
) -> ResultPage:
    key = model["keys"][key_index]
    api_key = str(key.get("api_key") or "")
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    base_url = compact_url(
        key.get("base_url") or data.get("default_base_url") or "-", 48
    )
    content = section_panel(
        f"模型: [bold]{short_text(model['id'], 48)}[/bold]\nKey: {key_display_name(key, key_name, 48)}\n上游: [bold]{base_url}[/bold]\n指纹: [bold]{key_fingerprint(api_key)}[/bold]\n\n选择“复制 API key”即可写入剪贴板。",
        "复制 API key",
        "green",
    )
    return ResultPage(content, copy_text=api_key, copy_label="复制 API key")


def toggle_selected_key_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    current_enabled = key.get("enabled", True)
    new_enabled = not current_enabled
    key["enabled"] = new_enabled
    new_config = commit_config_data(path, data, old_config).new_config
    old_status = "启用" if current_enabled else "禁用"
    new_status = "启用" if new_enabled else "禁用"
    return Group(
        section_panel(
            f"已切换 Key 状态。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\nKey: {key_display_name(key, key_name, 32)}\n原状态: [bold]{old_status}[/bold]\n新状态: [bold]{new_status}[/bold]",
            "Key 开关",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def toggle_visitor_access_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    if not visitor_feature_available():
        return section_panel(
            "访客功能未安装。请使用 auto-model-key-router[visitor] 重新安装或升级。",
            "访客访问",
            "yellow",
        )
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    visitor_allowed = not bool(key.get("allow_visitor", False))
    key["allow_visitor"] = visitor_allowed
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            f"已更新访客访问权限。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n"
            f"Key: {key_display_name(key, key_name, 32)}\n访客访问: {format_visitor_status_text(visitor_allowed, True)}",
            "访客访问",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def manage_config_transfer_interactively(path: Path) -> None:
    on_key = lambda key: _open_config_on_key(path, key)
    while True:
        choice = select_option(
            "配置迁移", [("1", "复制 Key 配置"), ("2", "粘贴并应用"), ("0", "返回")],
            on_key=on_key,
        )
        if choice == "0":
            return
        clear_terminal_history()
        result = (
            export_config_interactively(path)
            if choice == "1"
            else paste_config_interactively(path)
        )
        if result is not None:
            show_result_page("配置迁移", result)


def transferable_key_config(
    data: dict[str, Any], *, include_visitor: bool
) -> dict[str, Any]:
    default_base_url = str(data.get("default_base_url") or "https://api.openai.com")
    default_routing_mode = str(data.get("routing_mode") or "round_robin")
    raw_models = data.get("models", [])
    if not isinstance(raw_models, list):
        raise ValueError("models 必须是数组")
    models = deepcopy(raw_models)
    for model in models:
        if not isinstance(model, dict):
            raise ValueError("models 中的每一项必须是对象")
        if not model.get("routing_mode"):
            model["routing_mode"] = default_routing_mode
        keys = model.get("keys", [])
        if not isinstance(keys, list):
            raise ValueError("模型 keys 必须是数组")
        for key in keys:
            if not isinstance(key, dict):
                raise ValueError("模型 keys 中的每一项必须是对象")
            if not key.get("base_url"):
                key["base_url"] = default_base_url
            if not include_visitor:
                key.pop("allow_visitor", None)
    return {"models": models}


def merge_transferable_key_config(
    current_data: dict[str, Any], transfer_data: dict[str, Any]
) -> tuple[dict[str, Any], int, int, int]:
    merged_data = deepcopy(current_data)
    models = merged_data.setdefault("models", [])
    default_base_url = str(
        current_data.get("default_base_url") or "https://api.openai.com"
    )
    added_models = 0
    added_keys = 0
    skipped_keys = 0

    for transferred_model in transfer_data["models"]:
        model_id = str(transferred_model["id"])
        current_model = find_model(models, model_id)
        if current_model is None:
            models.append(deepcopy(transferred_model))
            added_models += 1
            added_keys += len(transferred_model.get("keys", []))
            continue

        current_keys = current_model.setdefault("keys", [])
        used_names = {
            str(key.get("name") or f"{model_id}-{index + 1}")
            for index, key in enumerate(current_keys)
        }
        existing_key_targets = {
            (
                str(key.get("api_key") or ""),
                str(key.get("base_url") or default_base_url),
                json.dumps(key.get("upstream_routes") or {}, sort_keys=True),
            )
            for key in current_keys
        }

        for index, transferred_key in enumerate(transferred_model.get("keys", [])):
            key_target = (
                str(transferred_key.get("api_key") or ""),
                str(transferred_key.get("base_url") or default_base_url),
                json.dumps(
                    transferred_key.get("upstream_routes") or {}, sort_keys=True
                ),
            )
            if key_target in existing_key_targets:
                skipped_keys += 1
                continue

            key = deepcopy(transferred_key)
            base_name = str(key.get("name") or f"{model_id}-{index + 1}")
            key_name = base_name
            suffix = 2
            while key_name in used_names:
                key_name = f"{base_name}-{suffix}"
                suffix += 1
            key["name"] = key_name
            current_keys.append(key)
            used_names.add(key_name)
            existing_key_targets.add(key_target)
            added_keys += 1

    return merged_data, added_models, added_keys, skipped_keys


def export_config_interactively(path: Path) -> ResultPage:
    data = load_config_data(path)
    visitor_installed = visitor_feature_available()
    transfer_data = transferable_key_config(data, include_visitor=visitor_installed)
    config_text = json.dumps(transfer_data, ensure_ascii=False, separators=(",", ":"))
    model_count = len(transfer_data["models"])
    key_count = sum(len(model.get("keys", [])) for model in transfer_data["models"])
    visitor_message = (
        "包含访客访问权限。"
        if visitor_installed
        else "visitor 扩展未安装，不包含访客访问权限。"
    )
    content = section_panel(
        f"配置文件: [bold]{path.resolve()}[/bold]\n模型数量: [bold]{model_count}[/bold]\n"
        f"Key 数量: [bold]{key_count}[/bold]\n\n"
        f"[bold yellow]复制内容仅包含模型与上游 API key，{visitor_message}请仅粘贴到可信终端。[/bold yellow]\n\n"
        "复制内容为单行 JSON。本地鉴权、监听地址、端口及其他 CLI 设置不会复制。\n\n"
        "在另一台机器或另一个 TUI 中进入“CLI 设置 → 配置迁移 → 粘贴并应用”即可导入。",
        "复制 Key 配置",
        "green",
    )
    return ResultPage(content, copy_text=config_text, copy_label="复制 Key 配置")


def paste_config_interactively(path: Path) -> Any:
    text = prompt_text("粘贴并应用", "请粘贴单行 Key 配置 JSON，然后按 Enter").strip()
    if not text:
        return section_panel("未输入配置内容。", "应用取消", "yellow")
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        return section_panel(f"粘贴内容不是有效 JSON: {exc.msg}", "应用失败", "red")
    if not isinstance(data, dict):
        return section_panel("粘贴内容必须是配置对象。", "应用失败", "red")
    try:
        transfer_data = transferable_key_config(
            data, include_visitor=visitor_feature_available()
        )
        RouterConfig.from_dict(transfer_data)
        current_data = load_config_data(path)
        old_config = RouterConfig.from_dict(current_data)
        merged_data, added_models, added_keys, skipped_keys = (
            merge_transferable_key_config(current_data, transfer_data)
        )
        RouterConfig.from_dict(merged_data)
    except (KeyError, TypeError, ValueError) as exc:
        return section_panel(f"配置校验失败: {exc}", "应用失败", "red")
    if not confirm_choice(
        f"将追加粘贴的 Key 配置，并保留现有模型和本机 CLI 设置：{path.resolve()}，是否继续？",
        default=False,
    ):
        return section_panel("配置未变化。", "应用取消", "yellow")
    try:
        new_config = commit_config_data(path, merged_data, old_config).new_config
    except (KeyError, TypeError, ValueError) as exc:
        return section_panel(f"配置校验失败: {exc}", "应用失败", "red")
    model_count = len(new_config.models)
    key_count = sum(len(model.keys) for model in new_config.models)
    content = section_panel(
        f"已追加粘贴的 Key 配置，并保留现有模型和本机 CLI 设置。\n配置文件: [bold]{path.resolve()}[/bold]\n"
        f"新增模型: [bold]{added_models}[/bold]\n新增 Key: [bold]{added_keys}[/bold]\n"
        f"跳过重复 Key: [bold]{skipped_keys}[/bold]\n"
        f"当前模型数量: [bold]{model_count}[/bold]\n当前 Key 数量: [bold]{key_count}[/bold]",
        "应用完成",
        "green",
    )
    return Group(
        content, restart_service_after_config_change(path, old_config, new_config)
    )


def select_model_id_for_new_key(models: list[dict[str, Any]]) -> str | None:
    if not models:
        return prompt_text("添加 Key", "新模型 ID").strip()

    options = [
        (
            str(index + 1),
            f"{short_text(model.get('id') or '-', 32)} · {len(model.get('keys', []))} Key",
        )
        for index, model in enumerate(models)
    ]
    options.extend([("n", "自定义添加新的模型 ID"), ("0", "返回")])
    choice = select_option("选择模型", options)
    if choice == "0":
        return None
    if choice == "n":
        return prompt_text("添加 Key", "新模型 ID").strip()
    return str(models[int(choice) - 1].get("id") or "").strip()


def configured_base_urls(
    data: dict[str, Any], selected_model: dict[str, Any]
) -> list[str]:
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


def select_base_url_for_new_key(
    data: dict[str, Any], selected_model: dict[str, Any]
) -> str | None:
    base_urls = configured_base_urls(data, selected_model)
    options = [
        (str(index + 1), compact_url(base_url, 48))
        for index, base_url in enumerate(base_urls)
    ]
    options.extend([("n", "自定义添加新的上游 URL"), ("0", "返回")])
    choice = select_option("选择上游 URL", options)
    if choice == "0":
        return None
    if choice == "n":
        default_base_url = str(data.get("default_base_url") or "https://api.openai.com")
        return prompt_text(
            "添加 Key", "新的上游 base_url", default=default_base_url
        ).strip()
    return base_urls[int(choice) - 1]


def discover_upstream_models(
    base_url: str,
    api_key: str,
    existing_model_ids: set[str],
    timeout: float = 15.0,
) -> list[str]:
    try:
        with httpx.Client(timeout=timeout) as client:
            response = client.get(
                _join_url(base_url, "/v1/models"),
                headers={"Authorization": f"Bearer {api_key}"},
            )
            response.raise_for_status()
            data = response.json()
        model_ids = [item["id"] for item in data.get("data", [])]
        return sorted(model_ids)
    except (httpx.RequestError, httpx.HTTPStatusError, KeyError, TypeError, ValueError):
        return []


def _select_model_with_discovery(
    models: list[dict[str, Any]],
    base_url: str,
    api_key: str,
) -> tuple[str | None, list[str]]:
    existing_ids = {str(model.get("id") or "") for model in models}
    with console.status("[cyan]正在探测上游可用模型...[/cyan]", spinner="dots"):
        discovered = discover_upstream_models(base_url, api_key, existing_ids)
    available = [mid for mid in discovered if find_model(models, mid) is not None]
    new_models = [mid for mid in discovered if mid not in existing_ids]
    manual_entry = ("n", "自定义添加新的模型 ID")
    return_entry = ("0", "返回")

    if available or new_models:
        options: list[tuple[str, str]] = []
        for mid in available:
            model = find_model(models, mid)
            options.append((mid, f"{short_text(mid, 32)} · 已有 · {len(model.get('keys', []))} Key"))
        for mid in new_models:
            options.append((mid, f"{short_text(mid, 32)} · 新模型"))
        options.extend([manual_entry, return_entry])
        content = section_panel(
            f"已探测到 [bold]{len(discovered)}[/bold] 个上游模型，直接选择即可为已有模型添加 Key，或选择新模型。",
            "发现上游模型",
            "cyan",
        )
        choice = select_option("选择模型", options, content=content)
    else:
        return select_model_id_for_new_key(models), []

    if choice == "0":
        return None, []
    if choice == "n":
        return prompt_text("添加 Key", "新模型 ID").strip(), new_models
    return choice, new_models


def add_config_interactively(path: Path, ask_continue: bool = True) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    models = data.setdefault("models", [])

    base_url = select_base_url_for_new_key(data, {"keys": []})
    if base_url is None:
        return None
    if not base_url:
        return section_panel("[red]上游 base_url 不能为空[/red]", "添加失败", "red")
    api_key = prompt_text("添加 Key", "API key", password=True).strip()
    if not api_key:
        return section_panel("[red]API key 不能为空[/red]", "添加失败", "red")

    model_id, discovered_new_models = _select_model_with_discovery(models, base_url, api_key)
    if model_id is None:
        return None
    if not model_id:
        return section_panel("[red]模型 ID 不能为空[/red]", "添加失败", "red")

    model = find_model(models, model_id)
    is_new_model = model is None
    if model is None:
        model = {
            "id": model_id,
            "aliases": [],
            "routing_mode": "round_robin",
            "keys": [],
        }
        models.append(model)

    if is_new_model:
        aliases_text = prompt_text(
            "添加 Key",
            "显示名称/别名，多个用逗号分隔",
            default=",".join(model.get("aliases", [])),
        ).strip()
        model["aliases"] = (
            [alias.strip() for alias in aliases_text.split(",") if alias.strip()]
            if aliases_text
            else []
        )
        model["routing_mode"] = prompt_text(
            "添加 Key",
            "路由模式",
            choices=["priority", "round_robin", "only_first"],
            default=str(model.get("routing_mode") or "round_robin"),
        ).strip()
        reasoning_effort = prompt_text(
            "添加 Key",
            "推理强度",
            choices=["downstream", "none", "minimal", "low", "medium", "high", "xhigh"],
            default=normalize_reasoning_effort_choice(model.get("reasoning_effort")),
        ).strip()
        if reasoning_effort == "downstream":
            model.pop("reasoning_effort", None)
        else:
            model["reasoning_effort"] = reasoning_effort
    keys = model.setdefault("keys", [])
    default_key_name = f"{model_id}-key-{len(keys) + 1}"
    key_name = (
        prompt_text("添加 Key", "Key 名称", default=default_key_name).strip()
        or default_key_name
    )

    keys.append({"name": key_name, "api_key": api_key, "base_url": base_url})
    new_config = commit_config_data(path, data, old_config).new_config

    added_by_discovery = 0
    if discovered_new_models:
        skip_marker = "__skip__"
        multi_options = [(mid, mid) for mid in discovered_new_models]
        multi_options.append((skip_marker, "跳过，不批量添加"))
        selected_multi = select_multiple(
            "快速添加其他模型",
            multi_options,
            content=section_panel(
                f"探测到 [bold]{len(discovered_new_models)}[/bold] 个其他模型，选择要一同添加的模型。",
                "批量添加",
                "cyan",
            ),
        )
        selected_multi = [mid for mid in selected_multi if mid != skip_marker]
        if selected_multi:
            added_by_discovery = _add_discovered_models(path, selected_multi, api_key, base_url)
            if added_by_discovery > 0:
                new_config = RouterConfig.load(path)

    discovery_note = ""
    if added_by_discovery > 0:
        discovery_note = f"\n上游自动发现并添加: [bold]{added_by_discovery}[/bold] 个模型"
    result = Group(
        section_panel(
            f"已写入配置文件: [bold]{path}[/bold]\n模型: [bold]{model_id}[/bold]\nKey: [bold]{key_name}[/bold]{discovery_note}",
            "添加完成",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )
    if ask_continue and not confirm_choice("继续启动服务？", default=False):
        raise SystemExit(0)
    return result


def _add_discovered_models(path: Path, model_ids: list[str], api_key: str, base_url: str) -> int:
    added = 0
    for mid in model_ids:
        data = load_config_data(path)
        old_config = RouterConfig.from_dict(data)
        models = data.setdefault("models", [])
        if find_model(models, mid) is not None:
            continue
        new_model = {
            "id": mid,
            "aliases": [],
            "routing_mode": "round_robin",
            "keys": [{"name": f"{mid}-key-1", "api_key": api_key, "base_url": base_url}],
        }
        models.append(new_model)
        try:
            commit_config_data(path, data, old_config)
            added += 1
        except (KeyError, TypeError, ValueError):
            continue
    return added


def edit_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要编辑的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    old_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    key_name = prompt_text("编辑 Key", "Key 名称", default=old_name).strip() or old_name
    key["name"] = key_name
    key["base_url"] = prompt_text(
        "编辑 Key",
        "上游 base_url",
        default=str(
            key.get("base_url")
            or data.get("default_base_url")
            or "https://api.openai.com"
        ),
    ).strip()
    api_key = prompt_text(
        "编辑 Key", "新 API key（留空则不修改）", default="", password=True
    ).strip()
    if api_key:
        key["api_key"] = api_key
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            f"已更新配置文件: [bold]{path}[/bold]\n模型: [bold]{model['id']}[/bold]\nKey: [bold]{key_name}[/bold]",
            "编辑完成",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def delete_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要删除的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    if not confirm_choice(
        f"确认删除模型 {model['id']} 的 Key {key_name}？", default=False
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除取消", "yellow")
    del model["keys"][key_index]
    if not model["keys"]:
        data["models"].remove(model)
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型 [bold]{model['id']}[/bold] 已无 API key，已一并移除。"
    else:
        message = (
            f"已删除 Key: [bold]{key_name}[/bold]\n模型: [bold]{model['id']}[/bold]"
        )
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(message, "删除完成", "green"),
        restart_service_after_config_change(path, old_config, new_config),
    )


def copy_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要复制的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    key = model["keys"][key_index]
    api_key = str(key.get("api_key") or "")
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    base_url = compact_url(
        key.get("base_url") or data.get("default_base_url") or "-", 48
    )
    content = section_panel(
        f"模型: [bold]{short_text(model['id'], 48)}[/bold]\nKey: [bold]{short_text(key_name, 48)}[/bold]\n上游: [bold]{base_url}[/bold]\n指纹: [bold]{key_fingerprint(api_key)}[/bold]\n\n选择“复制 API key”即可写入剪贴板。",
        "复制 API key",
        "green",
    )
    return ResultPage(content, copy_text=api_key, copy_label="复制 API key")


def reorder_api_keys_interactively(path: Path) -> Any:
    data = load_config_data(path)
    selectable_models = [
        model for model in data.get("models", []) if len(model.get("keys", [])) > 1
    ]
    if not selectable_models:
        return section_panel(
            "[yellow]暂无可排序模型，需至少 2 个 Key。[/yellow]", "Key 排序", "yellow"
        )
    model_options = [
        (
            str(index + 1),
            f"{short_text(model['id'], 28)} · {len(model.get('keys', []))} Key",
        )
        for index, model in enumerate(selectable_models)
    ] + [("0", "返回")]
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
            new_config = commit_config_data(path, data, old_config).new_config
            return Group(
                section_panel(key_order_text(model), "顺序已保存", "green"),
                restart_service_after_config_change(path, old_config, new_config),
            )
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
    new_config = commit_config_data(path, data, old_config).new_config
    old_status = "启用" if current_enabled else "禁用"
    new_status = "启用" if new_enabled else "禁用"
    return Group(
        section_panel(
            f"已切换 Key 状态。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\nKey: [bold]{key_name}[/bold]\n原状态: [bold]{old_status}[/bold]\n新状态: [bold]{new_status}[/bold]",
            "Key 开关",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def select_reorder_key_action(
    model: dict[str, Any], selected: int = 0
) -> tuple[str, int]:
    frame_offset = 0
    frame_state = render_key_order_menu_state(model, selected, frame_offset)
    last_wheel_key: str | None = None
    last_wheel_at = 0.0

    def refresh(*, ensure_selected_visible: bool) -> None:
        nonlocal frame_offset, frame_state
        frame_state = render_key_order_menu_state(
            model,
            selected,
            frame_offset,
            ensure_selected_visible=ensure_selected_visible,
        )
        frame_offset = frame_state.offset
        live.update(frame_state.renderable, refresh=True)

    with (
        posix_input_mode(),
        mouse_wheel_mode(),
        Live(
            frame_state.renderable, console=console, screen=True, auto_refresh=False
        ) as live,
    ):
        while True:
            key = read_key_responsive(lambda: refresh(ensure_selected_visible=True))
            if key == "cancel":
                return "cancel", selected
            if key in {"scroll_up", "scroll_down"}:
                handle_wheel, last_wheel_key, last_wheel_at = should_handle_wheel(
                    key, last_wheel_key, last_wheel_at
                )
                if not handle_wheel:
                    continue
            if (
                key in {"page_up", "page_down", "home", "end"}
                and frame_state.max_offset
            ):
                frame_offset = content_scroll_offset(
                    key,
                    frame_offset,
                    frame_state.max_offset,
                    frame_state.viewport_height,
                )
                refresh(ensure_selected_visible=False)
                continue
            if key in {"up", "scroll_up"}:
                selected = max(0, selected - 1)
                refresh(ensure_selected_visible=True)
                continue
            if key in {"down", "scroll_down"}:
                selected = min(len(model.get("keys", [])) - 1, selected + 1)
                refresh(ensure_selected_visible=True)
                continue
            if key in {"w", "W"}:
                return "up", selected
            if key in {"s", "S"}:
                return "down", selected
            if key == "enter":
                return "save", selected


def render_key_order_menu_state(
    model: dict[str, Any],
    selected: int,
    frame_offset: int = 0,
    *,
    ensure_selected_visible: bool = True,
) -> Any:
    table = Table(show_header=False, box=None, padding=(0, 1), expand=True)
    table.add_column("指示", justify="center", width=3)
    table.add_column("顺序", justify="center", width=5)
    table.add_column("Key", ratio=1)
    table.add_column("上游", ratio=1)
    for index, key in enumerate(model.get("keys", [])):
        name = key_display_name(key, f"{model['id']}-{index + 1}")
        base_url = compact_url(key.get("base_url") or "-", 28)
        if index == selected:
            selected_name = (
                name
                if key.get("allow_visitor", False)
                else f"[bold cyan]{name}[/bold cyan]"
            )
            table.add_row(
                "[bold cyan]▶[/bold cyan]",
                f"[bold black on cyan] {index + 1} [/bold black on cyan]",
                selected_name,
                f"[bold cyan]{base_url}[/bold cyan]",
            )
        else:
            table.add_row("", f"[dim]{index + 1}[/dim]", name, base_url)
    shortcuts = (
        "↑/↓ 选择  ·  W/S 移动  ·  PgUp/PgDn 翻阅  ·  Enter 保存  ·  Ctrl+C 取消"
    )
    if sys.platform == "win32":
        shortcuts = "↑/↓/滚轮 选择  ·  W/S 移动  ·  PgUp/PgDn 翻阅  ·  Enter 保存  ·  Ctrl+C 取消"
    return terminal_frame_state(
        [
            page_title("Key 排序", f"模型 · {short_text(model['id'], 24)}"),
            section_panel(
                table, "Key 顺序", "cyan", "[dim]选择 Key 后用 W/S 调整优先级[/dim]"
            ),
        ],
        shortcut_text(shortcuts),
        offset=frame_offset,
        focus_text="▶" if ensure_selected_visible else None,
    )


def render_key_order_menu(model: dict[str, Any], selected: int) -> Any:
    return render_key_order_menu_state(model, selected).renderable


def key_order_text(model: dict[str, Any]) -> str:
    lines = [f"模型: [bold]{short_text(model['id'], 32)}[/bold]", "当前顺序:"]
    for index, key in enumerate(model.get("keys", [])):
        name = key_display_name(key, f"{model['id']}-{index + 1}", 32)
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
        routing_mode = str(
            model.get("routing_mode") or data.get("routing_mode") or "round_robin"
        )
        routing_mode_text = routing_mode_display_text(routing_mode)
        model_options.append(
            (str(index + 1), f"{short_text(model['id'], 28)} · {routing_mode_text}")
        )
    model_options.append(("0", "返回"))
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    old_config = RouterConfig.from_dict(data)
    model = models[int(model_choice) - 1]
    current_mode = str(
        model.get("routing_mode") or data.get("routing_mode") or "round_robin"
    )
    mode_choice = select_option(
        "选择路由模式",
        [
            ("1", "分流：轮询"),
            ("2", "优先级：按顺序"),
            ("3", "仅首个：只重试第一个 Key"),
            ("0", "返回"),
        ],
        selected={"round_robin": 0, "priority": 1, "only_first": 2}.get(
            current_mode, 0
        ),
    )
    if mode_choice == "0":
        return section_panel("[yellow]配置未变化。[/yellow]", "路由模式", "yellow")
    new_mode = {"1": "round_robin", "2": "priority", "3": "only_first"}[mode_choice]
    if new_mode == current_mode:
        mode_text = routing_mode_display_text(new_mode)
        return section_panel(
            f"模型 [bold]{short_text(model['id'], 32)}[/bold] 已是 [bold]{mode_text}[/bold]。",
            "路由模式",
            "yellow",
        )
    model["routing_mode"] = new_mode
    new_config = commit_config_data(path, data, old_config).new_config
    old_mode_text = routing_mode_display_text(current_mode)
    new_mode_text = routing_mode_display_text(new_mode)
    return Group(
        section_panel(
            f"已更新路由模式。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n原模式: [bold]{old_mode_text}[/bold]\n新模式: [bold]{new_mode_text}[/bold]",
            "路由模式",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def routing_mode_display_text(value: str | None) -> str:
    return {"round_robin": "分流", "priority": "优先级", "only_first": "仅首个"}.get(
        str(value or "round_robin"), "分流"
    )


def set_model_reasoning_effort_interactively(path: Path) -> Any:
    data = load_config_data(path)
    models = data.get("models", [])
    if not models:
        return section_panel("[yellow]还没有模型配置。[/yellow]", "推理强度", "yellow")
    model_options = []
    for index, model in enumerate(models):
        effort = normalize_reasoning_effort_choice(model.get("reasoning_effort"))
        model_options.append(
            (
                str(index + 1),
                f"{short_text(model['id'], 28)} · {reasoning_effort_text(effort)}",
            )
        )
    model_options.append(("0", "返回"))
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    old_config = RouterConfig.from_dict(data)
    model = models[int(model_choice) - 1]
    current_effort = normalize_reasoning_effort_choice(model.get("reasoning_effort"))
    effort_choice = select_option(
        "选择推理强度",
        [
            ("1", "由下游决定"),
            ("2", "关闭 reasoning"),
            ("3", "minimal"),
            ("4", "low"),
            ("5", "medium"),
            ("6", "high"),
            ("7", "xhigh"),
            ("0", "返回"),
        ],
        selected={
            "downstream": 0,
            "none": 1,
            "minimal": 2,
            "low": 3,
            "medium": 4,
            "high": 5,
            "xhigh": 6,
        }.get(current_effort, 0),
    )
    if effort_choice == "0":
        return section_panel("[yellow]配置未变化。[/yellow]", "推理强度", "yellow")
    new_effort = {
        "1": "downstream",
        "2": "none",
        "3": "minimal",
        "4": "low",
        "5": "medium",
        "6": "high",
        "7": "xhigh",
    }[effort_choice]
    if new_effort == current_effort:
        return section_panel(
            f"模型 [bold]{short_text(model['id'], 32)}[/bold] 已是 [bold]{reasoning_effort_text(new_effort)}[/bold]。",
            "推理强度",
            "yellow",
        )
    if new_effort == "downstream":
        model.pop("reasoning_effort", None)
    else:
        model["reasoning_effort"] = new_effort
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            f"已更新推理强度。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n原强度: [bold]{reasoning_effort_text(current_effort)}[/bold]\n新强度: [bold]{reasoning_effort_text(new_effort)}[/bold]",
            "推理强度",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def reasoning_effort_text(value: str | None) -> str:
    effort = normalize_reasoning_effort_choice(value)
    return {
        "downstream": "由下游决定",
        "none": "关闭 reasoning",
        "minimal": "minimal",
        "low": "low",
        "medium": "medium",
        "high": "high",
        "xhigh": "xhigh",
    }.get(effort, effort)


def normalize_reasoning_effort_choice(value: Any) -> str:
    effort = str(value or "").strip()
    return "downstream" if effort in {"", "default", "downstream"} else effort


def select_api_key(
    path: Path, title: str
) -> tuple[dict[str, Any], dict[str, Any], int] | None:
    data = load_config_data(path)
    selectable_models = [model for model in data.get("models", []) if model.get("keys")]
    if not selectable_models:
        return None
    model_options = [
        (
            str(index + 1),
            f"{short_text(model['id'], 28)} · {len(model.get('keys', []))} Key",
        )
        for index, model in enumerate(selectable_models)
    ] + [("0", "返回")]
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    model = selectable_models[int(model_choice) - 1]
    key_options = []
    for index, key in enumerate(model.get("keys", [])):
        name = key_display_name(key, f"{model['id']}-{index + 1}")
        base_url = compact_url(
            key.get("base_url") or data.get("default_base_url") or "-", 28
        )
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
    if data.get("local_api_key") and not confirm_choice(
        "是否重置本地鉴权密钥？", default=True
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "本地鉴权", "yellow")
    local_api_key = generate_local_api_key()
    data["local_api_key"] = local_api_key
    new_config = commit_config_data(path, data, old_config).new_config
    content = Group(
        section_panel(
            f"已生成新密钥。\n\n[bold]{local_api_key}[/bold]\n\n请求时添加：\nAuthorization: Bearer <key>",
            "本地鉴权",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )
    return ResultPage(content, copy_text=local_api_key, copy_label="复制本地鉴权 key")


def set_listen_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    current_host = str(data.get("host") or "127.0.0.1")
    current_port = int(data.get("port") or 8000)
    host = prompt_text("监听配置", "监听 IP/地址", default=current_host).strip()
    if not host:
        return section_panel("[red]监听 IP/地址不能为空。[/red]", "监听配置", "red")
    if "://" in host or "/" in host:
        return section_panel(
            "[red]监听地址只填写 IP 或主机名，不要包含协议或路径。[/red]",
            "监听配置",
            "red",
        )
    port_text = prompt_text("监听配置", "监听端口", default=str(current_port)).strip()
    try:
        port = int(port_text)
    except ValueError:
        return section_panel("[red]端口必须是数字。[/red]", "监听配置", "red")
    if port < 1 or port > 65535:
        return section_panel("[red]端口范围必须是 1-65535。[/red]", "监听配置", "red")
    if host == current_host and port == current_port:
        return section_panel(
            f"监听配置未变化: [bold]{host}:{port}[/bold]", "监听配置", "yellow"
        )
    if (
        host == "0.0.0.0"
        and host != current_host
        and not confirm_choice(
            "0.0.0.0 会允许局域网/公网访问，确认继续？", default=False
        )
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "监听配置", "yellow")
    data["host"] = host
    data["port"] = port
    new_config = commit_config_data(path, data, old_config).new_config
    warning = (
        "\n[bold red]风险提示: 0.0.0.0 会暴露到所有可达网络，请确保防火墙和本地鉴权已正确配置。[/bold red]"
        if host == "0.0.0.0"
        else ""
    )
    return Group(
        section_panel(
            f"已更新监听配置。\n配置文件: [bold]{path}[/bold]\n旧配置: [bold]{current_host}:{current_port}[/bold]\n新配置: [bold]{host}:{port}[/bold]{warning}",
            "监听配置",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def find_model(models: list[dict[str, Any]], model_id: str) -> dict[str, Any] | None:
    for model in models:
        if model.get("id") == model_id:
            return model
    return None
