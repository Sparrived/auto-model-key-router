from __future__ import annotations

import argparse
import hashlib
import json
import msvcrt
import os
import platform
import signal
import sqlite3
import subprocess
import sys
import time
from pathlib import Path
from typing import Any
from urllib.error import URLError
from urllib.request import urlopen

import uvicorn
from rich.console import Console, Group
from rich.align import Align
from rich.live import Live
from rich.panel import Panel
from rich.prompt import Prompt
from rich.table import Table

from .app import create_app
from .config import DEFAULT_CONFIG_PATH, RouterConfig, empty_config_dict, generate_local_api_key


console = Console()
_SERVICE_STATUS_CACHE_TTL = 2.0
_service_status_cache: dict[tuple[str, int], tuple[float, bool]] = {}

MENU_OPTIONS = [
    ("1", "管理系统服务 / 开机自启动"),
    ("2", "管理模型 / API Key"),
    ("3", "管理本地鉴权"),
    ("4", "配置监听端口"),
    ("5", "查看日志板块"),
    ("6", "刷新配置"),
    ("0", "退出"),
]


def main() -> None:
    parser = argparse.ArgumentParser(prog=Path(sys.argv[0]).stem)
    parser.add_argument("--config", default=str(DEFAULT_CONFIG_PATH), help="配置文件路径")
    parser.add_argument("--host", help="覆盖配置中的监听地址")
    parser.add_argument("--port", type=int, help="覆盖配置中的监听端口")
    parser.add_argument("--show-config", action="store_true", help="只展示配置摘要，不启动服务")
    parser.add_argument("--show-logs", nargs="?", const=20, type=int, help="进入日志板块，显示最近 N 条请求记录")
    parser.add_argument("--serve", action="store_true", help="跳过 Terminal UI，后台启动服务")
    parser.add_argument("--serve-foreground", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--stop", action="store_true", help="停止后台服务")
    parser.add_argument("--status", action="store_true", help="查看后台服务状态")
    parser.add_argument("--install-service", action="store_true", help="注册为 Windows/Linux 内置服务")
    parser.add_argument("--service", choices=["install", "uninstall", "start", "stop", "status"], help="管理 Windows/Linux 内置服务")
    args = parser.parse_args()

    _clear_terminal_history()

    config_path = Path(args.config)

    try:
        try:
            config = RouterConfig.load(config_path)
        except Exception as exc:
            console.print(Panel(f"[red]{exc}[/red]", title="配置加载失败", border_style="red"))
            raise SystemExit(1) from exc

        if args.host:
            config = RouterConfig(args.host, config.port, config.request_timeout, config.max_retries, config.metrics_db_path, config.log_file_path, config.local_api_key, config.models)
        if args.port:
            config = RouterConfig(config.host, args.port, config.request_timeout, config.max_retries, config.metrics_db_path, config.log_file_path, config.local_api_key, config.models)

        if args.show_logs is not None:
            _render_config(config, config_path)
            _render_logs(config.metrics_db_path, config.log_file_path, args.show_logs)
            return
        if args.show_config:
            _render_config(config, config_path)
            return

        if args.stop:
            _stop_background_service(config)
            return
        if args.status:
            console.print(_background_status_panel(config, config_path))
            return
        if args.install_service:
            console.print(_manage_system_service(config_path, "install"))
            return
        if args.service:
            result = _background_status_panel(config, config_path) if args.service == "status" else _manage_system_service(config_path, args.service)
            console.print(result)
            return

        if args.serve_foreground:
            _start_service_foreground(config_path, config)
            return

        if not args.serve:
            _run_terminal_ui(config_path, config)
            return

        _start_service_background(config_path, config)
    except KeyboardInterrupt:
        _clear_terminal_history()
        raise SystemExit(130)


def _run_terminal_ui(config_path: Path, config: RouterConfig) -> None:
    selected = 0
    while True:
        choice, selected = _select_menu_option(config_path, config, selected)
        if choice == "0":
            return
        if choice == "1":
            _run_submodule(lambda: _manage_system_service_interactively(config_path))
            continue
        if choice == "2":
            _run_submodule(lambda: _manage_model_keys_interactively(config_path))
            config = RouterConfig.load(config_path)
            continue
        if choice == "3":
            result = _run_submodule(lambda: _set_local_api_key_interactively(config_path))
            if result is not None:
                _show_result_page("本地鉴权", result)
            config = RouterConfig.load(config_path)
            continue
        if choice == "4":
            result = _run_submodule(lambda: _set_port_interactively(config_path))
            if result is not None:
                _show_result_page("监听端口", result)
            config = RouterConfig.load(config_path)
            continue
        if choice == "5":
            _run_submodule(lambda: _watch_logs(config.metrics_db_path, config.log_file_path, 20))
            continue
        if choice == "6":
            config = RouterConfig.load(config_path)
            _run_submodule(lambda: _show_result_page("刷新配置", Panel("[green]配置已刷新[/green]", title="刷新配置", border_style="green")))


def _select_menu_option(config_path: Path, config: RouterConfig, selected: int = 0) -> tuple[str, int]:
    with Live(_render_terminal_ui(config_path, config, selected), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = _read_key()
            if key == "cancel":
                return "0", next(index for index, option in enumerate(MENU_OPTIONS) if option[0] == "0")
            if key == "up":
                selected = (selected - 1) % len(MENU_OPTIONS)
                live.update(_render_terminal_ui(config_path, config, selected), refresh=True)
                continue
            if key == "down":
                selected = (selected + 1) % len(MENU_OPTIONS)
                live.update(_render_terminal_ui(config_path, config, selected), refresh=True)
                continue
            if key == "enter":
                return MENU_OPTIONS[selected][0], selected
            if key in {option[0] for option in MENU_OPTIONS}:
                return key, next(index for index, option in enumerate(MENU_OPTIONS) if option[0] == key)


def _select_option(title: str, options: list[tuple[str, str]], selected: int = 0, content: Any | None = None) -> str:
    with Live(_render_option_menu(title, options, selected, content), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = _read_key()
            if key == "cancel":
                return options[-1][0]
            if key == "up":
                selected = (selected - 1) % len(options)
                live.update(_render_option_menu(title, options, selected, content), refresh=True)
                continue
            if key == "down":
                selected = (selected + 1) % len(options)
                live.update(_render_option_menu(title, options, selected, content), refresh=True)
                continue
            if key == "enter":
                return options[selected][0]
            if key in {option[0] for option in options}:
                return key


def _render_option_menu(title: str, options: list[tuple[str, str]], selected: int, content: Any | None = None) -> Group:
    table = Table(title=title, show_header=False, box=None)
    table.add_column("指示", justify="center")
    table.add_column("编号", style="cyan", justify="right")
    table.add_column("操作")
    for index, (value, label) in enumerate(options):
        if index == selected:
            table.add_row(">", f"[reverse]{value}[/reverse]", f"[reverse]{label}[/reverse]")
        else:
            table.add_row("", value, label)
    renderables = []
    if content is not None:
        renderables.append(content)
    renderables.extend([
        Panel(table, title=title),
        "[dim]使用 ↑/↓ 选择，Enter 确认；也可以按数字快捷键。[/dim]",
    ])
    return Group(*renderables)


def _key_pressed() -> str | None:
    if msvcrt.kbhit():
        return _read_key()
    return None


def _confirm_choice(message: str, default: bool = False) -> bool:
    options = [("y", "是"), ("n", "否")]
    selected = 0 if default else 1
    return _select_option("确认操作", options, selected, Panel(f"[bold]{message}[/bold]", title="请确认")) == "y"


def _render_terminal_ui(config_path: Path, config: RouterConfig, selected: int) -> Group:
    menu = Table(title="主菜单", show_header=False, box=None)
    menu.add_column("指示", justify="center")
    menu.add_column("编号", style="cyan", justify="right")
    menu.add_column("操作")
    for index, (value, label) in enumerate(MENU_OPTIONS):
        if index == selected:
            menu.add_row(">", f"[reverse]{value}[/reverse]", f"[reverse]{label}[/reverse]")
        else:
            menu.add_row("", value, label)
    return Group(
        Align.center("[bold cyan]Auto Model Key Router[/bold cyan]"),
        *_config_renderables(config, config_path),
        Panel(menu, title="Terminal UI"),
        "[dim]使用 ↑/↓ 选择，Enter 确认；也可以按数字快捷键。[/dim]",
    )


def _read_key() -> str:
    try:
        key = msvcrt.getwch()
    except KeyboardInterrupt:
        return "cancel"
    if key == "\x03":
        return "cancel"
    if key in {"\r", "\n"}:
        return "enter"
    if key in {"\x00", "\xe0"}:
        code = msvcrt.getwch()
        if code == "H":
            return "up"
        if code == "P":
            return "down"
    if key == "\x1b":
        second = msvcrt.getwch()
        third = msvcrt.getwch() if second == "[" else ""
        if third == "A":
            return "up"
        if third == "B":
            return "down"
    return key


def _run_submodule(action: Any) -> Any:
    _clear_terminal_history()
    try:
        return action()
    except KeyboardInterrupt:
        return Panel("已取消，返回上一级。", title="取消", border_style="yellow")


def _show_result_page(title: str, content: Any) -> None:
    _select_option(
        title,
        [("0", "返回")],
        selected=0,
        content=content,
    )


def _start_service_background(config_path: Path, config: RouterConfig) -> None:
    console.print(_start_service_background_result(config_path, config))


def _start_service_background_result(config_path: Path, config: RouterConfig) -> Panel:
    if _is_service_healthy(config.host, config.port, use_cache=False):
        return Panel(f"后台服务已在运行。\n地址: [bold]http://{config.host}:{config.port}[/bold]", title="后台服务", border_style="yellow")

    pid_file = _pid_file_path(config)
    existing_pid = _read_pid(pid_file)
    if existing_pid and _is_process_running(existing_pid):
        return Panel(f"后台服务已在运行。\nPID: [bold]{existing_pid}[/bold]", title="后台服务", border_style="yellow")

    Path(config.log_file_path).parent.mkdir(parents=True, exist_ok=True)
    pid_file.parent.mkdir(parents=True, exist_ok=True)
    log_file = open(config.log_file_path, "a", encoding="utf-8")
    command = [str(_background_python_executable()), "-m", "auto_model_key_router.main", "--config", str(config_path), "--serve-foreground"]
    creationflags = 0
    start_new_session = False
    if os.name == "nt":
        creationflags = subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP | subprocess.CREATE_NO_WINDOW
    else:
        start_new_session = True

    process = subprocess.Popen(
        command,
        cwd=str(Path.cwd()),
        stdin=subprocess.DEVNULL,
        stdout=log_file,
        stderr=log_file,
        creationflags=creationflags,
        start_new_session=start_new_session,
        close_fds=os.name != "nt",
    )
    log_file.close()
    pid_file.write_text(str(process.pid), encoding="utf-8")
    return Panel(f"后台服务已启动。\nPID: [bold]{process.pid}[/bold]\n地址: [bold]http://{config.host}:{config.port}[/bold]\n运行日志: [bold]{config.log_file_path}[/bold]", title="后台服务", border_style="green")


def _start_service_foreground(config_path: Path, config: RouterConfig) -> None:
    if _is_service_healthy(config.host, config.port, use_cache=False):
        return

    app = create_app(config, config_path)
    _pid_file_path(config).parent.mkdir(parents=True, exist_ok=True)
    _pid_file_path(config).write_text(str(os.getpid()), encoding="utf-8")
    uvicorn.run(app, host=config.host, port=config.port, log_config=_uvicorn_log_config(config.log_file_path), access_log=True)


def _stop_background_service(config: RouterConfig) -> None:
    console.print(_stop_background_service_result(config))


def _stop_background_service_result(config: RouterConfig) -> Panel:
    pid_file = _pid_file_path(config)
    pid = _read_pid(pid_file)
    if not pid:
        return Panel("没有找到后台服务 PID 文件。", title="后台服务", border_style="yellow")
    if not _is_process_running(pid):
        pid_file.unlink(missing_ok=True)
        return Panel(f"PID {pid} 已不存在，已清理 PID 文件。", title="后台服务", border_style="yellow")
    if os.name == "nt":
        subprocess.run(["taskkill", "/PID", str(pid), "/T", "/F"], capture_output=True, text=True)
    else:
        os.kill(pid, signal.SIGTERM)
    for _ in range(20):
        if not _is_process_running(pid):
            pid_file.unlink(missing_ok=True)
            return Panel(f"后台服务已停止。\nPID: [bold]{pid}[/bold]", title="后台服务", border_style="green")
        time.sleep(0.1)
    return Panel(f"已发送停止信号，但进程仍可能在退出中。\nPID: [bold]{pid}[/bold]", title="后台服务", border_style="yellow")


def _restart_service_after_config_change(path: Path, old_config: RouterConfig, new_config: RouterConfig) -> Any:
    pid = _read_pid(_pid_file_path(old_config))
    was_running = _is_service_healthy(old_config.host, old_config.port, use_cache=False) or bool(pid and _is_process_running(pid))
    if not was_running:
        return Panel("服务当前未运行，配置已保存；下次启动时生效。", title="服务重启", border_style="yellow")
    stop_result = _stop_background_service_result(old_config)
    start_result = _start_service_background_result(path, new_config)
    return Group(stop_result, start_result)


def _render_background_status(config: RouterConfig) -> None:
    console.print(_background_status_panel(config))


def _background_status_panel(config: RouterConfig, config_path: Path | None = None) -> Panel:
    health = _service_health(config.host, config.port, use_cache=False)
    address = f"http://{config.host}:{config.port}"
    lines = [f"地址: [bold]{address}[/bold]", f"健康检查: [bold]{address}/health[/bold]"]
    if not health:
        lines.insert(0, "状态: [yellow]未运行[/yellow]")
        return Panel("\n".join(lines), title="后台服务", border_style="yellow", expand=False)

    lines.insert(0, "状态: [green]运行中[/green]")
    expected_config_path = str(config_path.resolve()) if config_path is not None else ""
    running_config_path = str(health.get("config_path") or "")
    if running_config_path:
        lines.append(f"运行配置: [bold]{running_config_path}[/bold]")
    if expected_config_path and running_config_path and Path(running_config_path) != Path(expected_config_path):
        lines.append(f"[yellow]当前 TUI 配置: {expected_config_path}[/yellow]")
        lines.append("[yellow]运行中的服务使用了另一个配置文件，本地 API key 可能不一致。[/yellow]")

    running_fingerprint = str(health.get("local_api_key_fingerprint") or "")
    expected_fingerprint = _key_fingerprint(config.local_api_key)
    if running_fingerprint:
        lines.append(f"运行中本地 key 指纹: [bold]{running_fingerprint}[/bold]")
    if expected_fingerprint:
        lines.append(f"当前配置本地 key 指纹: [bold]{expected_fingerprint}[/bold]")
    if running_fingerprint and expected_fingerprint and running_fingerprint != expected_fingerprint:
        lines.append("[yellow]运行中的服务仍在使用旧本地 API key，请重启/启动已注册服务后再请求。[/yellow]")
    return Panel("\n".join(lines), title="后台服务", border_style="green", expand=False)


def _pid_file_path(config: RouterConfig) -> Path:
    return Path(config.log_file_path).with_name("server.pid")


def _read_pid(path: Path) -> int | None:
    try:
        return int(path.read_text(encoding="utf-8").strip())
    except (OSError, ValueError):
        return None


def _is_process_running(pid: int) -> bool:
    if os.name == "nt":
        result = subprocess.run(["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"], capture_output=True, text=True)
        return f'"{pid}"' in result.stdout or f',{pid},' in result.stdout
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def _manage_system_service_interactively(config_path: Path) -> None:
    while True:
        choice = _select_option(
            "系统服务 / 开机自启动",
            [
                ("1", "安装并启用开机自启动"),
                ("2", "启动已注册服务"),
                ("3", "停止已注册服务"),
                ("4", "查看服务状态"),
                ("5", "卸载并取消开机自启动"),
                ("0", "返回"),
            ],
            selected=3,
        )
        actions = {"1": "install", "2": "start", "3": "stop", "4": "status", "5": "uninstall"}
        if choice == "0":
            return
        _clear_terminal_history()
        result = _background_status_panel(RouterConfig.load(config_path), config_path) if choice == "4" else _manage_system_service(config_path, actions[choice])
        _show_result_page("系统服务", result)


def _manage_system_service(config_path: Path, action: str) -> Any:
    absolute_config = config_path.resolve()
    python = _background_python_executable().resolve()
    system = platform.system().lower()
    if system == "windows":
        return _manage_windows_task(python, absolute_config, action)
    if system == "linux":
        return _manage_systemd_user_service(python, absolute_config, action)
    return Panel(f"暂不支持当前系统自动注册: {platform.system()}", title="系统服务", border_style="yellow")


def _manage_windows_task(python: Path, config_path: Path, action: str) -> Any:
    task_name = "AutoModelKeyRouter"
    if action == "install":
        command = [
            "schtasks",
            "/Create",
            "/F",
            "/SC",
            "ONSTART",
            "/TN",
            task_name,
            "/TR",
            f'"{python}" -m auto_model_key_router.main --config "{config_path}" --serve-foreground',
        ]
        return _registration_result(command, "Windows 开机自启动任务")
    if action == "uninstall":
        config = RouterConfig.load(config_path)
        return Group(
            _stop_background_service_result(config),
            _registration_result(["schtasks", "/End", "/TN", task_name], "Windows 停止运行中任务"),
            _registration_result(["schtasks", "/Delete", "/F", "/TN", task_name], "Windows 开机自启动任务"),
        )
    commands = {
        "start": ["schtasks", "/Run", "/TN", task_name],
        "stop": ["schtasks", "/End", "/TN", task_name],
        "status": ["schtasks", "/Query", "/TN", task_name, "/V", "/FO", "LIST"],
    }
    return _registration_result(commands[action], "Windows 开机自启动任务")


def _background_python_executable() -> Path:
    python = Path(sys.executable).resolve()
    if os.name == "nt" and python.name.lower() == "python.exe":
        pythonw = python.with_name("pythonw.exe")
        if pythonw.exists():
            return pythonw
    return python


def _manage_systemd_user_service(python: Path, config_path: Path, action: str) -> Any:
    service_name = "auto-model-key-router.service"
    service_dir = Path.home() / ".config" / "systemd" / "user"
    service_path = service_dir / service_name
    if action == "install":
        service_dir.mkdir(parents=True, exist_ok=True)
        service_path.write_text(
            "\n".join(
                [
                    "[Unit]",
                    "Description=Auto Model Key Router",
                    "After=network-online.target",
                    "Wants=network-online.target",
                    "",
                    "[Service]",
                    f"WorkingDirectory={Path.cwd()}",
                    f"ExecStart={python} -m auto_model_key_router.main --config {config_path} --serve-foreground",
                    "Restart=always",
                    "RestartSec=3",
                    "",
                    "[Install]",
                    "WantedBy=default.target",
                    "",
                ]
            ),
            encoding="utf-8",
        )
        results = [
            _registration_result(["systemctl", "--user", "daemon-reload"], "systemd user"),
            _registration_result(["systemctl", "--user", "enable", "--now", service_name], "systemd user"),
            _registration_result(["loginctl", "enable-linger", os.getlogin()], "systemd linger"),
            Panel(f"systemd 用户服务文件已写入:\n[bold]{service_path}[/bold]", title="系统服务", border_style="green"),
        ]
        return Group(*results)
    commands = {
        "start": ["systemctl", "--user", "start", service_name],
        "stop": ["systemctl", "--user", "stop", service_name],
        "status": ["systemctl", "--user", "status", service_name, "--no-pager"],
    }
    if action == "uninstall":
        config = RouterConfig.load(config_path)
        result = _registration_result(["systemctl", "--user", "disable", "--now", service_name], "systemd user")
        service_path.unlink(missing_ok=True)
        return Group(result, _stop_background_service_result(config), _registration_result(["systemctl", "--user", "daemon-reload"], "systemd user"))
    result = _registration_result(commands[action], "systemd user")
    return result


def _run_registration_command(command: list[str], title: str) -> None:
    console.print(_registration_result(command, title))


def _registration_result(command: list[str], title: str) -> Panel:
    result = subprocess.run(command, capture_output=True, text=True)
    content = result.stdout.strip() or "注册命令执行成功。"
    if result.returncode == 0:
        return Panel(content, title=title, border_style="green", expand=False)
    return Panel((result.stderr or result.stdout or "注册命令执行失败。").strip(), title=title, border_style="red", expand=False)


def _clear_terminal_history() -> None:
    if sys.stdout is None:
        return
    try:
        sys.stdout.write("\033[2J\033[3J\033[H")
        sys.stdout.flush()
        console.clear()
    except (OSError, ValueError):
        return


def _render_config(config: RouterConfig, path: Path) -> None:
    for renderable in _config_renderables(config, path):
        console.print(renderable)


def _config_renderables(config: RouterConfig, path: Path) -> tuple[Panel, Table]:
    table = Table(title="模型路由配置", show_lines=False)
    table.add_column("模型 ID", style="cyan")
    table.add_column("显示名称")
    table.add_column("Key 数量", justify="right", style="green")
    table.add_column("上游地址")

    for model in config.models:
        upstreams = sorted({key.base_url for key in model.keys})
        display_names = "\n".join(model.aliases) if model.aliases else "-"
        table.add_row(model.id, display_names, str(len(model.keys)), "\n".join(upstreams))

    if not config.models:
        table.add_row("未配置", "-", "0", "-")

    local_auth = "已启用" if config.local_api_key else "未启用"
    service_status = _service_status_text(config)
    panel = Panel(f"监听地址: [bold]{config.host}:{config.port}[/bold]\n本地鉴权: [bold]{local_auth}[/bold]\n服务状态: [bold]{service_status}[/bold]", title="Auto Model Key Router")
    return panel, table


def _service_status_text(config: RouterConfig) -> str:
    if _is_service_healthy(config.host, config.port):
        return "运行中 (健康检查通过)"
    return "未运行"


def _is_service_healthy(host: str, port: int, use_cache: bool = True) -> bool:
    cache_key = (host, port)
    now = time.monotonic()
    if use_cache:
        cached = _service_status_cache.get(cache_key)
        if cached and now - cached[0] < _SERVICE_STATUS_CACHE_TTL:
            return cached[1]

    connect_host = "127.0.0.1" if host in {"0.0.0.0", "::"} else host
    try:
        with urlopen(f"http://{connect_host}:{port}/health", timeout=0.1) as response:
            healthy = response.status == 200
    except (OSError, URLError, ValueError):
        healthy = False
    _service_status_cache[cache_key] = (now, healthy)
    return healthy


def _service_health(host: str, port: int, use_cache: bool = True) -> dict[str, Any] | None:
    if not _is_service_healthy(host, port, use_cache):
        return None
    connect_host = "127.0.0.1" if host in {"0.0.0.0", "::"} else host
    try:
        with urlopen(f"http://{connect_host}:{port}/health", timeout=0.5) as response:
            data = json.loads(response.read().decode("utf-8"))
            return data if isinstance(data, dict) else {"status": "ok"}
    except (OSError, URLError, ValueError):
        return {"status": "ok"}


def _key_fingerprint(api_key: str) -> str:
    if not api_key:
        return ""
    return hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:12]


def _manage_model_keys_interactively(path: Path) -> None:
    while True:
        choice = _select_option(
            "模型 / API key 管理",
            [
                ("1", "添加模型 / API key"),
                ("2", "编辑 API key"),
                ("3", "删除 API key"),
                ("4", "调整 API key 顺序"),
                ("0", "返回"),
            ],
        )
        if choice == "0":
            return
        if choice == "1":
            _clear_terminal_history()
            result = _add_config_interactively(path, ask_continue=False)
            if result is not None:
                _show_result_page("添加模型 / API key", result)
            continue
        if choice == "2":
            _clear_terminal_history()
            result = _edit_api_key_interactively(path)
            if result is not None:
                _show_result_page("编辑 API key", result)
            continue
        if choice == "3":
            _clear_terminal_history()
            result = _delete_api_key_interactively(path)
            if result is not None:
                _show_result_page("删除 API key", result)
            continue
        if choice == "4":
            _clear_terminal_history()
            result = _reorder_api_keys_interactively(path)
            if result is not None:
                _show_result_page("调整 API key 顺序", result)
            continue


def _add_config_interactively(path: Path, ask_continue: bool = True) -> None:
    data = _load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    model_id = Prompt.ask("模型 ID").strip()
    if not model_id:
        return Panel("[red]模型 ID 不能为空[/red]", title="添加失败", border_style="red")

    models = data.setdefault("models", [])
    model = _find_model(models, model_id)
    if model is None:
        model = {"id": model_id, "aliases": [], "keys": []}
        models.append(model)
        console.print(f"[green]已创建模型配置:[/green] {model_id}")

    aliases_text = Prompt.ask("显示名称/别名，多个用逗号分隔", default=",".join(model.get("aliases", []))).strip()
    if aliases_text:
        model["aliases"] = [alias.strip() for alias in aliases_text.split(",") if alias.strip()]
    else:
        model["aliases"] = []

    keys = model.setdefault("keys", [])
    default_key_name = f"{model_id}-key-{len(keys) + 1}"
    key_name = Prompt.ask("Key 名称", default=default_key_name).strip() or default_key_name
    base_url = Prompt.ask("上游 base_url", default=str(data.get("default_base_url") or "https://api.openai.com")).strip()
    api_key = Prompt.ask("API key", password=True).strip()
    if not api_key:
        return Panel("[red]API key 不能为空[/red]", title="添加失败", border_style="red")

    keys.append({"name": key_name, "api_key": api_key, "base_url": base_url})
    new_config = RouterConfig.from_dict(data)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    result = Group(
        Panel(f"已写入配置文件: [bold]{path}[/bold]\n模型: [bold]{model_id}[/bold]\nKey: [bold]{key_name}[/bold]", title="添加完成", border_style="green"),
        _restart_service_after_config_change(path, old_config, new_config),
    )

    if ask_continue and not _confirm_choice("继续启动服务？", default=False):
        raise SystemExit(0)
    return result


def _edit_api_key_interactively(path: Path) -> None:
    selection = _select_api_key(path, "选择要编辑的 API key")
    if selection is None:
        return
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    old_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    key_name = Prompt.ask("Key 名称", default=old_name).strip() or old_name
    base_url = Prompt.ask("上游 base_url", default=str(key.get("base_url") or data.get("default_base_url") or "https://api.openai.com")).strip()
    api_key = Prompt.ask("新 API key（留空则不修改）", default="", password=True).strip()

    key["name"] = key_name
    key["base_url"] = base_url
    if api_key:
        key["api_key"] = api_key

    new_config = RouterConfig.from_dict(data)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return Group(
        Panel(f"已更新配置文件: [bold]{path}[/bold]\n模型: [bold]{model['id']}[/bold]\nKey: [bold]{key_name}[/bold]", title="编辑完成", border_style="green"),
        _restart_service_after_config_change(path, old_config, new_config),
    )


def _delete_api_key_interactively(path: Path) -> None:
    selection = _select_api_key(path, "选择要删除的 API key")
    if selection is None:
        return
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    if not _confirm_choice(f"确认删除模型 {model['id']} 的 Key {key_name}？", default=False):
        return Panel("[yellow]配置未变化。[/yellow]", title="删除取消", border_style="yellow")

    del model["keys"][key_index]
    if not model["keys"]:
        data["models"].remove(model)
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型 [bold]{model['id']}[/bold] 已无 API key，已一并移除。"
    else:
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型: [bold]{model['id']}[/bold]"

    new_config = RouterConfig.from_dict(data)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return Group(
        Panel(message, title="删除完成", border_style="green"),
        _restart_service_after_config_change(path, old_config, new_config),
    )


def _reorder_api_keys_interactively(path: Path) -> None:
    data = _load_config_data(path)
    models = data.get("models", [])
    selectable_models = [model for model in models if len(model.get("keys", [])) > 1]
    if not selectable_models:
        return Panel("[yellow]没有可调整顺序的模型。至少需要同一模型下配置 2 个 API key。[/yellow]", title="调整 API key 顺序", border_style="yellow")

    model_options = [(str(index + 1), f"{model['id']}（{len(model.get('keys', []))} 个 Key）") for index, model in enumerate(selectable_models)]
    model_options.append(("0", "返回"))
    model_choice = _select_option("选择模型", model_options)
    if model_choice == "0":
        return

    model = selectable_models[int(model_choice) - 1]
    old_config = RouterConfig.from_dict(data)
    keys = model.get("keys", [])
    selected = 0
    while True:
        action, selected = _select_reorder_key_action(model, selected)
        if action == "cancel":
            return Panel("[yellow]配置未变化。[/yellow]", title="调整 API key 顺序", border_style="yellow")
        if action == "save":
            new_config = RouterConfig.from_dict(data)
            path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
            return Group(
                Panel(_key_order_text(model), title="顺序已保存", border_style="green"),
                _restart_service_after_config_change(path, old_config, new_config),
            )
        if action == "up" and selected > 0:
            keys[selected - 1], keys[selected] = keys[selected], keys[selected - 1]
            selected -= 1
            continue
        if action == "down" and selected < len(keys) - 1:
            keys[selected + 1], keys[selected] = keys[selected], keys[selected + 1]
            selected += 1
            continue


def _select_reorder_key_action(model: dict[str, Any], selected: int = 0) -> tuple[str, int]:
    with Live(_render_key_order_menu(model, selected), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = _read_key()
            if key == "cancel":
                return "cancel", selected
            if key == "up":
                selected = max(0, selected - 1)
                live.update(_render_key_order_menu(model, selected), refresh=True)
                continue
            if key == "down":
                selected = min(len(model.get("keys", [])) - 1, selected + 1)
                live.update(_render_key_order_menu(model, selected), refresh=True)
                continue
            if key in {"w", "W"}:
                return "up", selected
            if key in {"s", "S"}:
                return "down", selected
            if key == "enter":
                return "save", selected


def _render_key_order_menu(model: dict[str, Any], selected: int) -> Group:
    table = Table(title=f"{model['id']} Key 顺序", show_header=False, box=None)
    table.add_column("指示", justify="center")
    table.add_column("顺序", style="cyan", justify="right")
    table.add_column("Key")
    table.add_column("上游地址")
    for index, key in enumerate(model.get("keys", [])):
        name = str(key.get("name") or f"{model['id']}-{index + 1}")
        base_url = str(key.get("base_url") or "-")
        if index == selected:
            table.add_row(">", f"[reverse]{index + 1}[/reverse]", f"[reverse]{name}[/reverse]", f"[reverse]{base_url}[/reverse]")
        else:
            table.add_row("", str(index + 1), name, base_url)
    return Group(
        Panel(table, title="调整 API key 顺序"),
        "[dim]↑/↓ 选择 Key；W 上移，S 下移；Enter 保存；Esc 取消。运行时会按该顺序轮询。[/dim]",
    )


def _key_order_text(model: dict[str, Any]) -> str:
    lines = [f"模型: [bold]{model['id']}[/bold]", "当前顺序:"]
    for index, key in enumerate(model.get("keys", [])):
        name = str(key.get("name") or f"{model['id']}-{index + 1}")
        base_url = str(key.get("base_url") or "-")
        lines.append(f"{index + 1}. {name}  ->  {base_url}")
    return "\n".join(lines)


def _select_api_key(path: Path, title: str) -> tuple[dict[str, Any], dict[str, Any], int] | None:
    data = _load_config_data(path)
    models = data.get("models", [])
    selectable_models = [model for model in models if model.get("keys")]
    if not selectable_models:
        return None

    model_options = [(str(index + 1), f"{model['id']}（{len(model.get('keys', []))} 个 Key）") for index, model in enumerate(selectable_models)]
    model_options.append(("0", "返回"))
    model_choice = _select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    model = selectable_models[int(model_choice) - 1]

    key_options = []
    for index, key in enumerate(model.get("keys", [])):
        name = str(key.get("name") or f"{model['id']}-{index + 1}")
        base_url = str(key.get("base_url") or data.get("default_base_url") or "-")
        key_options.append((str(index + 1), f"{name}  ->  {base_url}"))
    key_options.append(("0", "返回"))
    key_choice = _select_option(title, key_options)
    if key_choice == "0":
        return None
    return data, model, int(key_choice) - 1


def _set_local_api_key_interactively(path: Path) -> None:
    data = _load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    if data.get("local_api_key") and not _confirm_choice("是否重置本地鉴权密钥？", default=True):
        return Panel("[yellow]配置未变化。[/yellow]", title="本地鉴权", border_style="yellow")
    local_api_key = generate_local_api_key()
    data["local_api_key"] = local_api_key
    new_config = RouterConfig.from_dict(data)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return Group(
        Panel(f"已启用本地鉴权，并生成新密钥。\n\n[bold]{local_api_key}[/bold]\n\n客户端请求本地 v1 接口时需要传入：\nAuthorization: Bearer <key>", title="本地鉴权", border_style="green"),
        _restart_service_after_config_change(path, old_config, new_config),
    )


def _set_port_interactively(path: Path) -> None:
    data = _load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    current_port = int(data.get("port") or 8000)
    port_text = Prompt.ask("监听端口", default=str(current_port)).strip()
    try:
        port = int(port_text)
    except ValueError:
        return Panel("[red]端口必须是数字。[/red]", title="监听端口", border_style="red")
    if port < 1 or port > 65535:
        return Panel("[red]端口范围必须是 1-65535。[/red]", title="监听端口", border_style="red")
    if port == current_port:
        return Panel(f"监听端口未变化: [bold]{port}[/bold]", title="监听端口", border_style="yellow")

    data["port"] = port
    new_config = RouterConfig.from_dict(data)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return Group(
        Panel(f"已更新监听端口。\n配置文件: [bold]{path}[/bold]\n旧端口: [bold]{current_port}[/bold]\n新端口: [bold]{port}[/bold]", title="监听端口", border_style="green"),
        _restart_service_after_config_change(path, old_config, new_config),
    )


def _load_config_data(path: Path) -> dict[str, Any]:
    if not path.exists():
        data = empty_config_dict()
        path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        return data
    return json.loads(path.read_text(encoding="utf-8"))


def _find_model(models: list[dict[str, Any]], model_id: str) -> dict[str, Any] | None:
    for model in models:
        if model.get("id") == model_id:
            return model
    return None


def _render_logs(database_path: str, log_file_path: str, limit: int) -> None:
    for renderable in _log_renderables(database_path, log_file_path, limit).renderables:
        console.print(renderable)


def _watch_logs(database_path: str, log_file_path: str, limit: int) -> None:
    with Live(_render_live_logs(database_path, log_file_path, limit), console=console, screen=True, refresh_per_second=1) as live:
        while True:
            live.update(_render_live_logs(database_path, log_file_path, limit))
            started = time.monotonic()
            while time.monotonic() - started < 1:
                key = _key_pressed()
                if key in {"q", "Q", "0", "cancel"}:
                    return
                time.sleep(0.05)


def _render_live_logs(database_path: str, log_file_path: str, limit: int) -> Group:
    return Group(
        Align.center("[bold cyan]日志板块[/bold cyan]"),
        _log_renderables(database_path, log_file_path, limit),
        "[dim]日志每秒自动刷新；按 q / Esc / 0 返回。[/dim]",
    )


def _log_renderables(database_path: str, log_file_path: str, limit: int) -> Group:
    return Group(
        _service_logs_renderable(log_file_path, limit),
        _request_logs_renderable(database_path, limit),
    )


def _service_logs_renderable(log_file_path: str, limit: int) -> Panel:
    path = Path(log_file_path)
    if not path.exists():
        return Panel(f"[yellow]运行日志不存在: {path}[/yellow]", title="运行日志", border_style="yellow")

    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()[-max(limit, 1):]
    content = "\n".join(lines) if lines else "暂无运行日志"
    return Panel(content, title=f"运行日志 最近 {limit} 行")


def _request_logs_renderable(database_path: str, limit: int) -> Group | Panel:
    path = Path(database_path)
    if not path.exists():
        return Panel(f"[yellow]统计数据库不存在: {path}[/yellow]", title="请求日志", border_style="yellow")

    table = Table(title=f"最近 {limit} 条请求日志", show_lines=False)
    table.add_column("时间")
    table.add_column("模型", style="cyan")
    table.add_column("Key", style="green")
    table.add_column("状态", justify="right")
    table.add_column("成功", justify="center")
    table.add_column("重试", justify="center")
    table.add_column("输入", justify="right")
    table.add_column("输出", justify="right")
    table.add_column("总Tokens", justify="right")
    table.add_column("缓存Tokens", justify="right")
    table.add_column("首字(ms)", justify="right")
    table.add_column("耗时(ms)", justify="right")

    with sqlite3.connect(path) as connection:
        connection.row_factory = sqlite3.Row
        columns = {row["name"] for row in connection.execute("PRAGMA table_info(request_metrics)").fetchall()}
        cached_tokens_expr = "cached_tokens" if "cached_tokens" in columns else "0 AS cached_tokens"
        first_token_ms_expr = "first_token_ms" if "first_token_ms" in columns else "0 AS first_token_ms"
        duration_ms_expr = "duration_ms" if "duration_ms" in columns else "0 AS duration_ms"
        rows = connection.execute(
            f"""
            SELECT created_at, model_id, key_name, status_code, success, retried, prompt_tokens, completion_tokens, total_tokens, {cached_tokens_expr}, {first_token_ms_expr}, {duration_ms_expr}
            FROM request_metrics
            ORDER BY id DESC
            LIMIT ?
            """,
            (max(limit, 1),),
        ).fetchall()

    for row in rows:
        table.add_row(
            row["created_at"],
            row["model_id"],
            row["key_name"],
            "-" if row["status_code"] is None else str(row["status_code"]),
            "是" if row["success"] else "否",
            "是" if row["retried"] else "否",
            str(row["prompt_tokens"]),
            str(row["completion_tokens"]),
            str(row["total_tokens"]),
            str(row["cached_tokens"]),
            str(row["first_token_ms"]),
            str(row["duration_ms"]),
        )

    if not rows:
        table.add_row("暂无", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-")

    return Group(
        Panel(f"统计数据库: [bold]{path}[/bold]", title="请求日志"),
        table,
    )


def _uvicorn_log_config(log_file_path: str) -> dict[str, Any]:
    path = Path(log_file_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    return {
        "version": 1,
        "disable_existing_loggers": False,
        "formatters": {
            "default": {"format": "%(asctime)s %(levelname)s %(name)s %(message)s"},
            "access": {"format": "%(asctime)s %(levelname)s %(name)s %(message)s"},
        },
        "handlers": {
            "file": {
                "class": "logging.FileHandler",
                "formatter": "default",
                "filename": str(path),
                "encoding": "utf-8",
            }
        },
        "loggers": {
            "uvicorn": {"handlers": ["file"], "level": "INFO", "propagate": False},
            "uvicorn.error": {"handlers": ["file"], "level": "INFO", "propagate": False},
            "uvicorn.access": {"handlers": ["file"], "level": "INFO", "propagate": False},
        },
    }


if __name__ == "__main__":
    main()
