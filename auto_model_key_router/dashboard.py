from __future__ import annotations

from pathlib import Path
from typing import Any

from rich import box
from rich.console import Group
from rich.live import Live
from rich.table import Table

from .config import RouterConfig
from .config_editor import manage_model_keys_interactively, set_local_api_key_interactively, set_port_interactively
from .formatting import compact_url, short_text
from .logs_tui import watch_logs
from .service import background_status_panel, is_service_healthy, manage_system_service
from .tui import clear_terminal_history, console, menu_table, page_title, read_key, run_submodule, section_panel, select_option, shortcut_text, show_result_page


MENU_OPTIONS = [("1", "系统服务"), ("2", "模型 Key"), ("3", "本地鉴权"), ("4", "监听端口"), ("5", "日志板块"), ("0", "退出")]


def run_terminal_ui(config_path: Path, config: RouterConfig) -> None:
    selected = 0
    while True:
        choice, selected = select_menu_option(config_path, config, selected)
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
            result = run_submodule(lambda: set_port_interactively(config_path))
            if result is not None:
                show_result_page("监听端口", result)
            config = RouterConfig.load(config_path)
            continue
        if choice == "5":
            run_submodule(lambda: watch_logs(config.metrics_db_path, config.log_file_path, 20))


def select_menu_option(config_path: Path, config: RouterConfig, selected: int = 0) -> tuple[str, int]:
    with Live(render_terminal_ui(config_path, config, selected), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key()
            if key == "cancel":
                return "0", next(index for index, option in enumerate(MENU_OPTIONS) if option[0] == "0")
            if key == "up":
                selected = (selected - 1) % len(MENU_OPTIONS)
                live.update(render_terminal_ui(config_path, config, selected), refresh=True)
                continue
            if key == "down":
                selected = (selected + 1) % len(MENU_OPTIONS)
                live.update(render_terminal_ui(config_path, config, selected), refresh=True)
                continue
            if key == "enter":
                return MENU_OPTIONS[selected][0], selected
            if key in {option[0] for option in MENU_OPTIONS}:
                return key, next(index for index, option in enumerate(MENU_OPTIONS) if option[0] == key)


def render_terminal_ui(config_path: Path, config: RouterConfig, selected: int) -> Group:
    return Group(page_title("Auto Model Key Router", "本地 OpenAI-Compatible 模型密钥路由器"), *config_renderables(config, config_path), section_panel(menu_table(MENU_OPTIONS, selected), "主菜单", "cyan", "[dim]选择要管理的模块[/dim]"), shortcut_text("↑/↓ 选择  ·  Enter 确认  ·  数字快捷键  ·  Ctrl+C 退出"))


def manage_system_service_interactively(config_path: Path) -> None:
    while True:
        choice = select_option("系统服务", [("1", "安装自启"), ("2", "启动服务"), ("3", "停止服务"), ("4", "重启服务"), ("5", "服务状态"), ("6", "卸载自启"), ("0", "返回")], selected=4)
        actions = {"1": "install", "2": "start", "3": "stop", "4": "restart", "5": "status", "6": "uninstall"}
        if choice == "0":
            return
        clear_terminal_history()
        result = background_status_panel(RouterConfig.load(config_path), config_path) if choice == "5" else manage_system_service(config_path, actions[choice])
        show_result_page("系统服务", result)


def render_config(config: RouterConfig, path: Path) -> None:
    for renderable in config_renderables(config, path):
        console.print(renderable)


def config_renderables(config: RouterConfig, path: Path) -> tuple[Any, Any]:
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
    summary.add_row(f"[dim]配置文件[/dim]\n[bold]{short_text(path, 48)}[/bold]", "[dim]路由入口[/dim]\n[bold]/v1/{path}[/bold]", "[dim]健康检查[/dim]\n[bold]/health[/bold]")
    table = Table(title="路由配置", show_lines=False, box=box.SIMPLE_HEAVY, expand=True)
    table.add_column("模型 ID", style="cyan", ratio=2)
    table.add_column("名称")
    table.add_column("路由模式")
    table.add_column("Keys", justify="right", style="green")
    table.add_column("上游", ratio=2)
    for model in config.models:
        upstreams = sorted({compact_url(key.base_url) for key in model.keys})
        display_names = "\n".join(model.aliases) if model.aliases else "-"
        routing_mode = "优先级" if model.routing_mode == "priority" else "分流"
        table.add_row(short_text(model.id, 28), short_text(display_names, 24), routing_mode, str(len(model.keys)), "\n".join(upstreams))
    if not config.models:
        table.add_row("未配置", "-", "-", "0", "-")
    return section_panel(summary, "运行概览", "cyan"), section_panel(table, "模型路由", "blue")
