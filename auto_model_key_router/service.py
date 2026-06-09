from __future__ import annotations

import json
import os
import platform
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any
from urllib.error import URLError
from urllib.request import urlopen

import uvicorn
from rich.console import Group
from rich.panel import Panel

from .app import create_app
from .config import RouterConfig
from .formatting import key_fingerprint
from .tui import section_panel


_SERVICE_STATUS_CACHE_TTL = 2.0
_service_status_cache: dict[tuple[str, int], tuple[float, bool]] = {}


def start_service_background(config_path: Path, config: RouterConfig) -> Panel:
    if is_service_healthy(config.host, config.port, use_cache=False):
        return section_panel(f"后台服务已在运行。\n地址: [bold]http://{config.host}:{config.port}[/bold]", "后台服务", "yellow")

    pid_file = pid_file_path(config)
    existing_pid = read_pid(pid_file)
    if existing_pid and is_process_running(existing_pid):
        pid_file.unlink(missing_ok=True)

    Path(config.log_file_path).parent.mkdir(parents=True, exist_ok=True)
    pid_file.parent.mkdir(parents=True, exist_ok=True)
    log_file = open(config.log_file_path, "a", encoding="utf-8")
    command = [str(background_python_executable()), "-m", "auto_model_key_router.main", "--config", str(config_path), "--serve-foreground"]
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
    return section_panel(f"后台服务已启动。\nPID: [bold]{process.pid}[/bold]\n地址: [bold]http://{config.host}:{config.port}[/bold]\n日志: [bold]{config.log_file_path}[/bold]", "后台服务", "green")


def start_service_foreground(config_path: Path, config: RouterConfig) -> None:
    if is_service_healthy(config.host, config.port, use_cache=False):
        return

    app = create_app(config, config_path)
    pid_file_path(config).parent.mkdir(parents=True, exist_ok=True)
    pid_file_path(config).write_text(str(os.getpid()), encoding="utf-8")
    uvicorn.run(app, host=config.host, port=config.port, log_config=uvicorn_log_config(config.log_file_path), access_log=True)


def stop_background_service(config: RouterConfig) -> Panel:
    pid_file = pid_file_path(config)
    pid = read_pid(pid_file)
    if not pid:
        return section_panel("没有找到后台服务 PID 文件。", "后台服务", "yellow")
    if not is_process_running(pid):
        pid_file.unlink(missing_ok=True)
        return section_panel(f"PID {pid} 已不存在，已清理 PID 文件。", "后台服务", "yellow")
    if os.name == "nt":
        subprocess.run(["taskkill", "/PID", str(pid), "/T", "/F"], capture_output=True, text=True)
    else:
        os.kill(pid, signal.SIGTERM)
    for _ in range(20):
        if not is_process_running(pid):
            pid_file.unlink(missing_ok=True)
            return section_panel(f"后台服务已停止。\nPID: [bold]{pid}[/bold]", "后台服务", "green")
        time.sleep(0.1)
    return section_panel(f"已发送停止信号，进程退出中。\nPID: [bold]{pid}[/bold]", "后台服务", "yellow")


def restart_service_after_config_change(path: Path, old_config: RouterConfig, new_config: RouterConfig) -> Any:
    if old_config.host != new_config.host or old_config.port != new_config.port:
        return section_panel(f"配置已保存。当前进程将继续监听旧地址；新监听地址 [bold]http://{new_config.host}:{new_config.port}[/bold] 会在下次启动时生效。", "配置热重载", "yellow")
    return section_panel("配置已保存，运行中的服务会在下一次请求时自动热重载。", "配置热重载", "green")


def background_status_panel(config: RouterConfig, config_path: Path | None = None) -> Panel:
    health = service_health(config.host, config.port, use_cache=False)
    address = f"http://{config.host}:{config.port}"
    lines = [f"地址: [bold]{address}[/bold]", f"健康检查: [bold]{address}/health[/bold]"]
    if not health:
        lines.insert(0, "状态: [yellow]未运行[/yellow]")
        return section_panel("\n".join(lines), "后台服务", "yellow")

    lines.insert(0, "状态: [green]运行中[/green]")
    expected_config_path = str(config_path.resolve()) if config_path is not None else ""
    running_config_path = str(health.get("config_path") or "")
    if running_config_path:
        lines.append(f"运行配置: [bold]{running_config_path}[/bold]")
    if expected_config_path and running_config_path and Path(running_config_path) != Path(expected_config_path):
        lines.append(f"[yellow]当前 TUI 配置: {expected_config_path}[/yellow]")
        lines.append("[yellow]服务配置不同，Key 可能不一致。[/yellow]")

    running_fingerprint = str(health.get("local_api_key_fingerprint") or "")
    expected_fingerprint = key_fingerprint(config.local_api_key)
    if running_fingerprint:
        lines.append(f"运行中本地 key 指纹: [bold]{running_fingerprint}[/bold]")
    if expected_fingerprint:
        lines.append(f"当前配置本地 key 指纹: [bold]{expected_fingerprint}[/bold]")
    return section_panel("\n".join(lines), "后台服务", "green")


def pid_file_path(config: RouterConfig) -> Path:
    return Path(config.log_file_path).with_name("server.pid")


def read_pid(path: Path) -> int | None:
    try:
        return int(path.read_text(encoding="utf-8").strip())
    except (OSError, ValueError):
        return None


def is_process_running(pid: int) -> bool:
    if os.name == "nt":
        result = subprocess.run(["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"], capture_output=True, text=True)
        return f'"{pid}"' in result.stdout or f',{pid},' in result.stdout
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def manage_system_service(config_path: Path, action: str) -> Any:
    absolute_config = config_path.resolve()
    python = background_python_executable().resolve()
    system = platform.system().lower()
    if system == "windows":
        return manage_windows_task(python, absolute_config, action)
    if system == "linux":
        return manage_systemd_user_service(python, absolute_config, action)
    return section_panel(f"暂不支持当前系统自动注册: {platform.system()}", "系统服务", "yellow")


def service_registration_note_panel() -> Panel:
    system = platform.system().lower()
    if system == "windows":
        content = "当前使用 Windows 当前用户计划任务注册，权限级别为 LIMITED，通常不需要管理员权限；任务会在该用户登录时启动。"
    elif system == "linux":
        content = "当前使用 systemd user service 注册，通常不需要 sudo；loginctl enable-linger 可能需要管理员授权，失败时服务仍可在用户登录后自启。"
    else:
        content = "当前系统暂不支持自动注册为用户级系统服务。"
    return section_panel(content, "权限说明", "blue")


def manage_windows_task(python: Path, config_path: Path, action: str) -> Any:
    task_name = "AutoModelKeyRouter"
    if action == "install":
        command = ["schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", task_name, "/RL", "LIMITED", "/IT", "/TR", f'"{python}" -m auto_model_key_router.main --config "{config_path}" --serve-foreground']
        return Group(registration_result(command, "Windows 自启"), registration_result(["schtasks", "/Run", "/TN", task_name], "Windows 启动"), service_registration_note_panel())
    if action == "uninstall":
        config = RouterConfig.load(config_path)
        return Group(stop_background_service(config), registration_result(["schtasks", "/End", "/TN", task_name], "Windows 停止"), registration_result(["schtasks", "/Delete", "/F", "/TN", task_name], "Windows 自启"))
    commands = {
        "start": ["schtasks", "/Run", "/TN", task_name],
        "stop": ["schtasks", "/End", "/TN", task_name],
        "status": ["schtasks", "/Query", "/TN", task_name, "/V", "/FO", "LIST"],
    }
    if action == "restart":
        return Group(registration_result(["schtasks", "/End", "/TN", task_name], "Windows 停止"), registration_result(["schtasks", "/Run", "/TN", task_name], "Windows 启动"))
    return registration_result(commands[action], "Windows 自启")


def background_python_executable() -> Path:
    python = Path(sys.executable).resolve()
    if os.name == "nt" and python.name.lower() == "python.exe":
        pythonw = python.with_name("pythonw.exe")
        if pythonw.exists():
            return pythonw
    return python


def manage_systemd_user_service(python: Path, config_path: Path, action: str) -> Any:
    service_name = "auto-model-key-router.service"
    service_dir = Path.home() / ".config" / "systemd" / "user"
    service_path = service_dir / service_name
    if action == "install":
        service_dir.mkdir(parents=True, exist_ok=True)
        service_path.write_text("\n".join(["[Unit]", "Description=Auto Model Key Router", "After=network-online.target", "Wants=network-online.target", "", "[Service]", f"WorkingDirectory={Path.cwd()}", f"ExecStart={python} -m auto_model_key_router.main --config {config_path} --serve-foreground", "Restart=always", "RestartSec=3", "", "[Install]", "WantedBy=default.target", ""]), encoding="utf-8")
        return Group(registration_result(["systemctl", "--user", "daemon-reload"], "systemd user"), registration_result(["systemctl", "--user", "enable", "--now", service_name], "systemd user"), registration_result(["loginctl", "enable-linger", os.getlogin()], "systemd linger"), section_panel(f"systemd 用户服务文件已写入:\n[bold]{service_path}[/bold]", "系统服务", "green"), service_registration_note_panel())
    commands = {
        "start": ["systemctl", "--user", "start", service_name],
        "stop": ["systemctl", "--user", "stop", service_name],
        "restart": ["systemctl", "--user", "restart", service_name],
        "status": ["systemctl", "--user", "status", service_name, "--no-pager"],
    }
    if action == "uninstall":
        config = RouterConfig.load(config_path)
        result = registration_result(["systemctl", "--user", "disable", "--now", service_name], "systemd user")
        service_path.unlink(missing_ok=True)
        return Group(result, stop_background_service(config), registration_result(["systemctl", "--user", "daemon-reload"], "systemd user"))
    return registration_result(commands[action], "systemd user")


def registration_result(command: list[str], title: str) -> Panel:
    result = subprocess.run(command, capture_output=True, text=True)
    content = result.stdout.strip() or "注册命令执行成功。"
    if result.returncode == 0:
        return section_panel(content, title, "green")
    return section_panel((result.stderr or result.stdout or "注册命令执行失败。").strip(), title, "red")


def is_service_healthy(host: str, port: int, use_cache: bool = True) -> bool:
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


def service_health(host: str, port: int, use_cache: bool = True) -> dict[str, Any] | None:
    if not is_service_healthy(host, port, use_cache):
        return None
    connect_host = "127.0.0.1" if host in {"0.0.0.0", "::"} else host
    try:
        with urlopen(f"http://{connect_host}:{port}/health", timeout=0.5) as response:
            data = json.loads(response.read().decode("utf-8"))
            return data if isinstance(data, dict) else {"status": "ok"}
    except (OSError, URLError, ValueError):
        return {"status": "ok"}


def uvicorn_log_config(log_file_path: str) -> dict[str, Any]:
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
            "auto_model_key_router": {"handlers": ["file"], "level": "INFO", "propagate": False},
            "uvicorn": {"handlers": ["file"], "level": "INFO", "propagate": False},
            "uvicorn.error": {"handlers": ["file"], "level": "INFO", "propagate": False},
            "uvicorn.access": {"handlers": ["file"], "level": "INFO", "propagate": False},
        },
    }
