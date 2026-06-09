from __future__ import annotations

from pathlib import Path
from typing import Any

from rich import box
from rich.console import Group
from rich.live import Live
from rich.table import Table

from .config import RouterConfig
from .config_editor import manage_model_keys_interactively, reasoning_effort_text, set_listen_interactively, set_local_api_key_interactively
from .formatting import compact_url, short_text
from . import __version__
from .logs_tui import watch_logs
from .service import background_status_panel, is_service_healthy, manage_system_service
from .tui import app_flag_title, clear_terminal_history, confirm_choice, console, menu_table, mouse_wheel_mode, page_title, read_key, run_submodule, section_panel, select_option, shortcut_text, should_handle_wheel, show_result_page
from .update import VersionCheckResult, check_latest_version, install_latest_version, render_update_notice, render_version_check_result, update_target_label


MENU_OPTIONS = [("1", "系统服务"), ("2", "模型 Key"), ("3", "本地鉴权"), ("4", "监听配置"), ("5", "调用日志"), ("6", "版本更新"), ("0", "退出")]


def run_terminal_ui(config_path: Path, config: RouterConfig) -> None:
    selected = 0
    update_result = check_latest_version(timeout=1.2)
    while True:
        choice, selected = select_menu_option(config_path, config, selected, update_result)
        if choice == "0":
            return
        if choice == "1":
            run_submodule(lambda: manage_system_service_interactively(config_path))
            continue
        if choice == "2":
            run_submodule(lambda: manage_model_keys_interactively(config_path))
            config = RouterConfig.load(config_path)
            continue
        if choice == "3":
            result = run_submodule(lambda: set_local_api_key_interactively(config_path))
            if result is not None:
                show_result_page("本地鉴权", result)
            config = RouterConfig.load(config_path)
            continue
        if choice == "4":
            result = run_submodule(lambda: set_listen_interactively(config_path))
            if result is not None:
                show_result_page("监听配置", result)
            config = RouterConfig.load(config_path)
            continue
        if choice == "5":
            run_submodule(lambda: watch_logs(config.metrics_db_path, config.log_file_path, 20))
            continue
        if choice == "6":
            result = run_submodule(lambda: manage_version_update_interactively(update_result))
            if isinstance(result, VersionCheckResult):
                update_result = result


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
    renderables.extend([section_panel(menu_table(MENU_OPTIONS, selected), "主菜单", "cyan", "[dim]选择要管理的模块[/dim]"), shortcut_text("↑/↓/滚轮 选择  ·  Enter 确认  ·  数字快捷键  ·  Ctrl+C 退出")])
    return Group(*renderables)


def manage_system_service_interactively(config_path: Path) -> None:
    while True:
        choice = select_option("系统服务", [("1", "安装自启"), ("2", "启动服务"), ("3", "停止服务"), ("4", "重启服务"), ("5", "服务状态"), ("6", "卸载自启"), ("0", "返回")], selected=4)
        actions = {"1": "install", "2": "start", "3": "stop", "4": "restart", "5": "status", "6": "uninstall"}
        if choice == "0":
            return
        clear_terminal_history()
        result = background_status_panel(RouterConfig.load(config_path), config_path) if choice == "5" else manage_system_service(config_path, actions[choice])
        show_result_page("系统服务", result)


def manage_version_update_interactively(update_result: VersionCheckResult | None = None) -> VersionCheckResult:
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
            if confirm_choice(f"将通过 {latest_result.source or '可用来源'} 安装 {update_target_label(latest_result)}，更新完成后需要重启终端和服务。是否继续？"):
                clear_terminal_history()
                show_result_page("手动更新", install_latest_version(latest_result))
                latest_result = check_latest_version(timeout=10.0)


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
    renderables.append(section_panel(table, "模型路由", "blue"))
    return tuple(renderables)
