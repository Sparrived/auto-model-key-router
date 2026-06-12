from __future__ import annotations

import sys
from pathlib import Path
from typing import Any

from rich import box
from rich.console import Group
from rich.live import Live
from rich.table import Table

from .config import UNIFIED_MODEL_ID, RouterConfig, generate_local_api_key
from .config_editor import load_config_data, manage_config_transfer_interactively, manage_model_keys_interactively, reasoning_effort_text, save_config_data, set_listen_interactively, set_local_api_key_interactively
from .formatting import compact_url, short_text
from . import __version__
from .logs_tui import watch_logs
from .service import is_service_healthy, is_system_service_registered, manage_system_service, service_status_panel, system_service_status_panel
from .tui import ResultPage, app_flag_title, clear_terminal_history, confirm_choice, console, menu_table, mouse_wheel_mode, page_title, read_key, run_submodule, section_panel, select_option, shortcut_text, should_handle_wheel, show_result_page, terminal_frame
from .unified_model import switch_unified_model
from .update import UpdateInstallOutcome, VersionCheckResult, check_latest_version, install_latest_version_outcome, render_update_notice, render_version_check_result, update_target_label


MENU_OPTIONS = [("1", "一键配置"), ("2", "模型 Key"), ("3", "统一模型"), ("4", "调用日志"), ("5", "CLI 设置"), ("0", "退出")]
SETTINGS_OPTIONS = [("1", "模型服务"), ("2", "本地鉴权"), ("3", "监听配置"), ("4", "配置迁移"), ("5", "版本更新"), ("0", "返回")]


def run_terminal_ui(config_path: Path, config: RouterConfig) -> None:
    selected = 0
    update_result = check_latest_version(timeout=1.2)
    while True:
        choice, selected = select_menu_option(config_path, config, selected, update_result)
        if choice == "0":
            return
        if choice == "1":
            result = run_submodule(lambda: configure_cli_interactively(config_path))
            if result is not None:
                show_result_page("一键配置", result)
            config = RouterConfig.load(config_path)
            continue
        if choice == "2":
            run_submodule(lambda: manage_model_keys_interactively(config_path))
            config = RouterConfig.load(config_path)
            continue
        if choice == "3":
            run_submodule(lambda: manage_unified_model_interactively(config_path))
            config = RouterConfig.load(config_path)
            continue
        if choice == "4":
            config = RouterConfig.load(config_path)
            run_submodule(lambda: watch_logs(config.metrics_db_path, config.log_file_path, 20))
            config = RouterConfig.load(config_path)
            continue
        if choice == "5":
            result = run_submodule(lambda: manage_cli_settings_interactively(config_path, update_result))
            if isinstance(result, UpdateInstallOutcome):
                if result.handoff:
                    return
                show_result_page("手动更新", result.content)
                if result.updated:
                    return
                continue
            if isinstance(result, VersionCheckResult):
                update_result = result
            config = RouterConfig.load(config_path)


def manage_unified_model_interactively(config_path: Path) -> None:
    while True:
        config = RouterConfig.load(config_path)
        choice = select_option(
            "统一模型",
            [("1", "切换模型和 Key"), ("2", "仅切换 Key"), ("0", "返回")],
            content=unified_model_status_panel(config),
        )
        if choice == "0":
            return
        result = run_submodule(lambda: switch_unified_model_interactively(config_path, choose_model=choice == "1"))
        if result is not None:
            show_result_page("统一模型", result)


def switch_unified_model_interactively(config_path: Path, *, choose_model: bool) -> Any:
    config = RouterConfig.load(config_path)
    selectable_models = [model for model in config.models if any(key.enabled for key in model.keys)]
    if not selectable_models:
        return section_panel("[yellow]还没有包含已启用 Key 的模型配置。[/yellow]", "统一模型", "yellow")

    current = config.unified_model
    if choose_model or current is None:
        model_options = []
        for index, model in enumerate(selectable_models):
            aliases = f" · {short_text(', '.join(model.aliases), 24)}" if model.aliases else ""
            model_options.append((str(index + 1), f"{short_text(model.id, 28)}{aliases}"))
        model_options.append(("0", "返回"))
        current_model_id = config.configured_model_id(current.model) if current is not None else None
        selected_model = next((index for index, model in enumerate(selectable_models) if model.id == current_model_id), 0)
        model_choice = select_option("选择统一模型", model_options, selected=selected_model)
        if model_choice == "0":
            return None
        target_model = selectable_models[int(model_choice) - 1]
    else:
        target_model_id = config.configured_model_id(current.model)
        target_model = next((model for model in selectable_models if model.id == target_model_id), None)
        if target_model is None:
            return section_panel("[red]当前统一模型目标不存在或没有已启用 Key，请重新选择模型。[/red]", "统一模型", "red")

    enabled_keys = [key for key in target_model.keys if key.enabled]
    key_options = [("a", "自动路由")]
    for index, key in enumerate(enabled_keys):
        key_options.append((str(index + 1), f"{short_text(key.name, 28)} · {compact_url(key.base_url, 32)}"))
    key_options.append(("0", "返回"))
    current_key = current.key if current is not None and target_model.id == config.configured_model_id(current.model) else None
    selected_key = next((index + 1 for index, key in enumerate(enabled_keys) if key.name == current_key), 0)
    key_choice = select_option("选择统一模型 Key", key_options, selected=selected_key)
    if key_choice == "0":
        return None
    key_name = None if key_choice == "a" else enabled_keys[int(key_choice) - 1].name

    updated = switch_unified_model(config_path, target_model.id, key_name, update_key=True)
    return unified_model_status_panel(updated, title="统一模型已切换", color="green")


def unified_model_status_panel(config: RouterConfig, title: str = "当前路由", color: str = "cyan") -> Any:
    if config.unified_model is None:
        content = f"[yellow]尚未配置 {UNIFIED_MODEL_ID}。[/yellow]\n请选择已有模型和 Key。"
    else:
        content = (
            f"请求模型: [bold cyan]{UNIFIED_MODEL_ID}[/bold cyan]\n"
            f"目标模型: [bold]{config.unified_model.model}[/bold]\n"
            f"使用 Key: [bold green]{config.unified_model.key or '自动路由'}[/bold green]"
        )
    return section_panel(content, title, color)


def manage_cli_settings_interactively(config_path: Path, update_result: VersionCheckResult | None = None) -> VersionCheckResult | UpdateInstallOutcome | None:
    latest_result = update_result
    while True:
        choice = select_option("CLI 设置", SETTINGS_OPTIONS)
        if choice == "0":
            return latest_result
        if choice == "1":
            run_submodule(lambda: manage_system_service_interactively(config_path))
            continue
        if choice == "2":
            result = run_submodule(lambda: set_local_api_key_interactively(config_path))
            if result is not None:
                show_result_page("本地鉴权", result)
            continue
        if choice == "3":
            result = run_submodule(lambda: set_listen_interactively(config_path))
            if result is not None:
                show_result_page("监听配置", result)
            continue
        if choice == "4":
            run_submodule(lambda: manage_config_transfer_interactively(config_path))
            continue
        if choice == "5":
            result = run_submodule(lambda: manage_version_update_interactively(config_path, latest_result))
            if isinstance(result, UpdateInstallOutcome):
                return result
            if isinstance(result, VersionCheckResult):
                latest_result = result


def configure_cli_interactively(config_path: Path) -> Any:
    config_exists = config_path.exists()
    data = load_config_data(config_path)
    local_api_key = str(data.get("local_api_key") or "").strip()
    if not local_api_key:
        local_api_key = generate_local_api_key()
        data["local_api_key"] = local_api_key
        save_config_data(config_path, data)
        auth_message = "已生成本地鉴权 key。"
    else:
        auth_message = "已使用现有本地鉴权 key。" if config_exists else "已生成本地鉴权 key。"
    config = RouterConfig.from_dict(data)
    auth_panel = section_panel(f"{auth_message}\n\n[bold]{local_api_key}[/bold]\n\n请求时添加：\nAuthorization: Bearer {local_api_key}\n或：\nx-api-key: {local_api_key}", "本地鉴权", "green")
    endpoint_panel = section_panel(f"配置文件: [bold]{config_path.resolve()}[/bold]\n服务地址: [bold]http://{config.host}:{config.port}[/bold]", "CLI 设置", "green")
    return ResultPage(Group(auth_panel, manage_system_service(config_path, "install"), endpoint_panel), copy_text=local_api_key, copy_label="复制本地鉴权 key")


def select_menu_option(config_path: Path, config: RouterConfig, selected: int = 0, update_result: VersionCheckResult | None = None) -> tuple[str, int]:
    last_wheel_key: str | None = None
    last_wheel_at = 0.0
    with mouse_wheel_mode(), Live(render_terminal_ui(config_path, config, selected, update_result), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key()
            if key == "cancel":
                return "0", next(index for index, option in enumerate(MENU_OPTIONS) if option[0] == "0")
            if key in {"scroll_up", "scroll_down"}:
                handle_wheel, last_wheel_key, last_wheel_at = should_handle_wheel(key, last_wheel_key, last_wheel_at)
                if not handle_wheel:
                    continue
            if key in {"up", "scroll_up"}:
                selected = (selected - 1) % len(MENU_OPTIONS)
                live.update(render_terminal_ui(config_path, config, selected, update_result), refresh=True)
                continue
            if key in {"down", "scroll_down"}:
                selected = (selected + 1) % len(MENU_OPTIONS)
                live.update(render_terminal_ui(config_path, config, selected, update_result), refresh=True)
                continue
            if key == "enter":
                return MENU_OPTIONS[selected][0], selected
            if key in {option[0] for option in MENU_OPTIONS}:
                return key, next(index for index, option in enumerate(MENU_OPTIONS) if option[0] == key)


def render_terminal_ui(config_path: Path, config: RouterConfig, selected: int, update_result: VersionCheckResult | None = None) -> Group:
    update_notice = render_update_notice(update_result)
    renderables = [app_flag_title("Auto-Model-Key-Router", "OpenAI-Compatible 模型 API Key 路由控制台", __version__), *config_renderables(config, config_path)]
    if update_notice is not None:
        renderables.append(update_notice)
    renderables.append(section_panel(menu_table(MENU_OPTIONS, selected), "主菜单", "cyan", "[dim]选择要管理的模块[/dim]"))
    return terminal_frame(renderables, shortcut_text("↑/↓ 选择  ·  Enter 确认  ·  数字快捷键  ·  Ctrl+C 退出" if sys.platform != "win32" else "↑/↓/滚轮 选择  ·  Enter 确认  ·  数字快捷键  ·  Ctrl+C 退出"))


def manage_system_service_interactively(config_path: Path) -> None:
    while True:
        choice = select_option("模型服务", [("1", "开机自启"), ("2", "启动服务"), ("3", "停止服务"), ("4", "重启服务"), ("5", "服务状态"), ("0", "返回")], selected=4)
        actions = {"2": "start", "3": "stop", "4": "restart", "5": "status"}
        if choice == "0":
            return
        if choice == "1":
            run_submodule(lambda: manage_autostart_interactively(config_path))
            continue
        clear_terminal_history()
        result = service_status_panel(RouterConfig.load(config_path), config_path) if choice == "5" else manage_system_service(config_path, actions[choice])
        show_result_page("模型服务", result)


def manage_autostart_interactively(config_path: Path) -> None:
    while True:
        registered = is_system_service_registered(config_path)
        options = [("1", "卸载自启"), ("0", "返回")] if registered else [("1", "安装开机自启"), ("2", "安装登录自启"), ("0", "返回")]
        choice = select_option("开机自启", options, selected=0, content=system_service_status_panel(config_path))
        if choice == "0":
            return
        clear_terminal_history()
        action = "uninstall" if registered else {"1": "install", "2": "install-user"}[choice]
        show_result_page("开机自启", manage_system_service(config_path, action))


def manage_version_update_interactively(config_path: Path, update_result: VersionCheckResult | None = None) -> VersionCheckResult | UpdateInstallOutcome:
    latest_result = update_result if update_result is not None and not update_result.error else check_latest_version(timeout=10.0)
    while True:
        choice = select_option("版本更新", [("1", "重新检查"), ("2", "手动更新"), ("0", "返回")], selected=0, content=render_version_check_result(latest_result))
        if choice == "0":
            return latest_result
        if choice == "1":
            latest_result = check_latest_version(timeout=10.0)
            continue
        if choice == "2":
            if latest_result.error or not latest_result.update_available:
                show_result_page("版本更新", render_version_check_result(latest_result))
                continue
            if confirm_choice(f"将通过 {latest_result.source or '可用来源'} 安装 {update_target_label(latest_result)}。Windows 会交由独立更新器接管并退出当前界面，更新成功后自动重启服务和 Terminal UI。是否继续？"):
                clear_terminal_history()
                return install_latest_version_outcome(latest_result, config_path, restart_tui=True)


def render_config(config: RouterConfig, path: Path) -> None:
    for renderable in config_renderables(config, path):
        console.print(renderable)


def config_renderables(config: RouterConfig, path: Path) -> tuple[Any, ...]:
    model_count = len(config.models)
    key_count = sum(len(model.keys) for model in config.models)
    upstream_count = len({key.base_url for model in config.models for key in model.keys})
    service_status = "[green]● 运行中[/green]" if is_service_healthy(config.host, config.port) else "[yellow]● 未运行[/yellow]"
    auth_status = "[green]已启用[/green]" if config.local_api_key else "[yellow]未设置[/yellow]"
    summary = Table.grid(expand=True)
    summary.add_column(ratio=1)
    summary.add_column(ratio=1)
    summary.add_column(ratio=1)
    summary.add_row(f"[dim]监听地址[/dim]\n[bold]{config.host}:{config.port}[/bold]", f"[dim]服务状态[/dim]\n[bold]{service_status}[/bold]", f"[dim]本地鉴权[/dim]\n[bold]{auth_status}[/bold]")
    summary.add_row(f"[dim]模型数量[/dim]\n[bold cyan]{model_count}[/bold cyan]", f"[dim]Key 数量[/dim]\n[bold green]{key_count}[/bold green]", f"[dim]上游数量[/dim]\n[bold magenta]{upstream_count}[/bold magenta]")
    warning = section_panel("[bold red]⚠ 当前监听地址为 0.0.0.0，服务会接受所有可达网络的连接。[/bold red]\n[red]如果机器暴露在公网或未受信任网络中，请务必启用本地鉴权、限制防火墙访问，并避免泄露上游 API Key。[/red]", "公网开放风险", "red") if config.host == "0.0.0.0" else None
    table = Table(show_lines=False, box=box.SIMPLE_HEAVY, expand=True)
    table.add_column("模型 ID", style="cyan", ratio=2)
    table.add_column("名称")
    table.add_column("路由模式")
    table.add_column("推理强度")
    table.add_column("Keys", justify="right", style="green")
    table.add_column("上游", ratio=2)
    for model in config.models:
        upstreams = sorted({compact_url(key.base_url) for key in model.keys})
        display_names = "\n".join(model.aliases) if model.aliases else "-"
        routing_mode = {"round_robin": "分流", "priority": "优先级", "only_first": "仅首个"}.get(model.routing_mode, "分流")
        table.add_row(short_text(model.id, 28), short_text(display_names, 24), routing_mode, reasoning_effort_text(model.reasoning_effort), str(len(model.keys)), "\n".join(upstreams))
    if not config.models:
        table.add_row("未配置", "-", "-", "-", "0", "-")
    renderables = [section_panel(summary, "运行概览", "cyan")]
    if warning is not None:
        renderables.append(warning)
    if config.unified_model is not None:
        key_text = config.unified_model.key or "自动路由"
        renderables.append(section_panel(f"[bold cyan]{UNIFIED_MODEL_ID}[/bold cyan] → [bold]{config.unified_model.model}[/bold]\nKey: [bold green]{key_text}[/bold green]", "统一模型", "cyan"))
    renderables.append(section_panel(table, "模型路由", "blue"))
    return tuple(renderables)
