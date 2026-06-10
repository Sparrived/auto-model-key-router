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
from xml.etree import ElementTree

import uvicorn
from rich.console import Group
from rich.markup import escape
from rich.panel import Panel
from rich.table import Table

from .app import create_app
from .config import RouterConfig
from .formatting import key_fingerprint
from .log_files import archive_current_log
from .tui import section_panel


_SERVICE_STATUS_CACHE_TTL = 2.0
_service_status_cache: dict[tuple[str, int], tuple[float, bool]] = {}
WINDOWS_TASK_NAME = "AutoModelKeyRouter"
SYSTEMD_USER_SERVICE_NAME = "auto-model-key-router.service"


def start_service_background(config_path: Path, config: RouterConfig) -> Panel:
    if is_service_healthy(config.host, config.port, use_cache=False):
        return section_panel(f"后台服务已在运行。\n地址: [bold]http://{config.host}:{config.port}[/bold]", "后台服务", "yellow")

    pid_file = pid_file_path(config)
    existing_pid = read_pid(pid_file)
    if existing_pid and is_process_running(existing_pid):
        pid_file.unlink(missing_ok=True)

    archived_log_path = archive_current_log(config.log_file_path)
    pid_file.parent.mkdir(parents=True, exist_ok=True)
    log_file = open(config.log_file_path, "a", encoding="utf-8")
    command = [str(background_python_executable()), "-m", "auto_model_key_router.main", "--config", str(config_path), "--serve-foreground"]
    env = {**os.environ, "AMKR_LOG_ARCHIVED": "1"}
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
        env=env,
        creationflags=creationflags,
        start_new_session=start_new_session,
        close_fds=os.name != "nt",
    )
    log_file.close()
    pid_file.write_text(str(process.pid), encoding="utf-8")
    lines = ["后台服务已启动。", f"PID: [bold]{process.pid}[/bold]", f"地址: [bold]http://{config.host}:{config.port}[/bold]", f"日志: [bold]{config.log_file_path}[/bold]"]
    if archived_log_path is not None:
        lines.append(f"旧日志已归档: [bold]{archived_log_path}[/bold]")
    return section_panel("\n".join(lines), "后台服务", "green")


def start_service_foreground(config_path: Path, config: RouterConfig) -> None:
    if is_service_healthy(config.host, config.port, use_cache=False):
        return

    if os.environ.get("AMKR_LOG_ARCHIVED") != "1":
        archive_current_log(config.log_file_path)
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


def service_status_panel(config: RouterConfig, config_path: Path) -> Group:
    return Group(background_status_panel(config, config_path), system_service_status_panel(config_path))


def system_service_status_panel(config_path: Path) -> Any:
    absolute_config = config_path.resolve()
    python = background_python_executable().resolve()
    system = platform.system().lower()
    if system == "windows":
        return windows_task_status_panel(python, absolute_config)
    if system == "linux":
        return systemd_user_service_status_panel(python, absolute_config)
    return section_panel(f"暂不支持当前系统自动注册: {platform.system()}", "系统服务注册", "yellow")


def is_system_service_registered(config_path: Path) -> bool:
    system = platform.system().lower()
    if system == "windows":
        return is_windows_task_registered()
    if system == "linux":
        return is_systemd_user_service_registered()
    return False


def is_windows_task_registered() -> bool:
    list_result = run_status_command(["schtasks", "/Query", "/TN", WINDOWS_TASK_NAME, "/V", "/FO", "LIST"])
    xml_result = run_status_command(["schtasks", "/Query", "/TN", WINDOWS_TASK_NAME, "/XML"])
    return list_result.returncode == 0 or xml_result.returncode == 0


def is_systemd_user_service_registered() -> bool:
    service_path = Path.home() / ".config" / "systemd" / "user" / SYSTEMD_USER_SERVICE_NAME
    show_result = run_status_command(["systemctl", "--user", "show", SYSTEMD_USER_SERVICE_NAME, "--property=LoadState", "--no-pager"])
    show_values = parse_systemctl_properties(show_result.stdout)
    return service_path.exists() or show_values.get("LoadState") not in {None, "", "not-found"}


def windows_task_status_panel(python: Path, config_path: Path) -> Any:
    list_result = run_status_command(["schtasks", "/Query", "/TN", WINDOWS_TASK_NAME, "/V", "/FO", "LIST"])
    xml_result = run_status_command(["schtasks", "/Query", "/TN", WINDOWS_TASK_NAME, "/XML"])
    registered = list_result.returncode == 0 or xml_result.returncode == 0
    list_values = parse_key_value_lines(list_result.stdout)
    task_values = parse_windows_task_xml(xml_result.stdout) if xml_result.returncode == 0 else {}
    rows = [
        ("平台", "Windows 计划任务"),
        ("任务名", WINDOWS_TASK_NAME),
        ("注册状态", "[green]已注册[/green]" if registered else "[yellow]未注册[/yellow]"),
        ("运行状态", first_value(list_values, "Status", "状态", "Scheduled Task State", "任务状态") or "-"),
        ("触发器", task_values.get("Triggers", "-")),
        ("账户", task_values.get("UserId", first_value(list_values, "Run As User", "运行身份") or "-")),
        ("权限级别", task_values.get("RunLevel", "-")),
        ("执行程序", task_values.get("Command", "-")),
        ("启动参数", task_values.get("Arguments", "-")),
        ("工作目录", task_values.get("WorkingDirectory", "-")),
        ("电池启动限制", task_values.get("DisallowStartIfOnBatteries", "-")),
        ("电池停止", task_values.get("StopIfGoingOnBatteries", "-")),
        ("错过后尽快启动", task_values.get("StartWhenAvailable", "-")),
        ("执行时限", task_values.get("ExecutionTimeLimit", "-")),
        ("上次运行", first_value(list_values, "Last Run Time", "上次运行时间") or "-"),
        ("下次运行", first_value(list_values, "Next Run Time", "下次运行时间") or "-"),
        ("上次结果", first_value(list_values, "Last Result", "上次结果") or "-"),
        ("期望配置", str(config_path)),
        ("期望 Python", str(python)),
    ]
    panels: list[Any] = [section_panel(status_table(rows), "系统服务注册", "green" if registered else "yellow")]
    raw = status_command_text(list_result)
    if raw:
        panels.append(section_panel(raw, "Windows 原始状态", "blue" if list_result.returncode == 0 else "red"))
    elif not registered:
        panels.append(section_panel("未找到计划任务，或 schtasks 不可用。", "Windows 原始状态", "yellow"))
    return Group(*panels)


def systemd_user_service_status_panel(python: Path, config_path: Path) -> Any:
    service_dir = Path.home() / ".config" / "systemd" / "user"
    service_path = service_dir / SYSTEMD_USER_SERVICE_NAME
    show_result = run_status_command(["systemctl", "--user", "show", SYSTEMD_USER_SERVICE_NAME, "--property=LoadState,ActiveState,SubState,UnitFileState,FragmentPath,ExecMainPID,ExecMainStatus,Result,MainPID,NRestarts,NeedDaemonReload", "--no-pager"])
    status_result = run_status_command(["systemctl", "--user", "status", SYSTEMD_USER_SERVICE_NAME, "--no-pager"])
    show_values = parse_systemctl_properties(show_result.stdout)
    unit_text = service_path.read_text(encoding="utf-8") if service_path.exists() else ""
    unit_values = parse_systemd_unit_file(unit_text)
    registered = service_path.exists() or show_values.get("LoadState") not in {None, "", "not-found"}
    rows = [
        ("平台", "Linux systemd user service"),
        ("服务名", SYSTEMD_USER_SERVICE_NAME),
        ("注册状态", "[green]已注册[/green]" if registered else "[yellow]未注册[/yellow]"),
        ("Unit 文件", str(service_path) if service_path.exists() else f"未找到: {service_path}"),
        ("加载状态", show_values.get("LoadState", "-")),
        ("启用状态", show_values.get("UnitFileState", "-")),
        ("运行状态", "/".join(value for value in (show_values.get("ActiveState"), show_values.get("SubState")) if value) or "-"),
        ("Main PID", show_values.get("MainPID") or show_values.get("ExecMainPID") or "-"),
        ("最近结果", show_values.get("Result", "-")),
        ("退出码", show_values.get("ExecMainStatus", "-")),
        ("重启次数", show_values.get("NRestarts", "-")),
        ("需 daemon-reload", show_values.get("NeedDaemonReload", "-")),
        ("工作目录", unit_values.get("WorkingDirectory", "-")),
        ("启动命令", unit_values.get("ExecStart", "-")),
        ("重启策略", unit_values.get("Restart", "-")),
        ("安装目标", unit_values.get("WantedBy", "-")),
        ("期望配置", str(config_path)),
        ("期望 Python", str(python)),
    ]
    panels: list[Any] = [section_panel(status_table(rows), "系统服务注册", "green" if registered else "yellow")]
    raw_status = status_command_text(status_result)
    if raw_status:
        panels.append(section_panel(raw_status, "systemd 原始状态", "blue" if status_result.returncode == 0 else "red"))
    if unit_text:
        panels.append(section_panel(unit_text.strip(), "systemd 服务文件", "blue"))
    return Group(*panels)


def status_table(rows: list[tuple[str, str]]) -> Table:
    table = Table(show_header=False, box=None, padding=(0, 1), expand=True)
    table.add_column("项目", style="dim", no_wrap=True)
    table.add_column("值", ratio=1)
    for key, value in rows:
        table.add_row(key, escape_status_value(value))
    return table


def escape_status_value(value: str) -> str:
    if value.startswith(("[green]", "[yellow]", "[red]", "[blue]", "[cyan]")):
        return value
    return escape(value)


def run_status_command(command: list[str]) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(command, capture_output=True, text=True)
    except FileNotFoundError as exc:
        return subprocess.CompletedProcess(command, 127, "", str(exc))


def status_command_text(result: subprocess.CompletedProcess[str]) -> str:
    output = (result.stdout or result.stderr or "").strip()
    if output:
        return output
    if result.returncode == 0:
        return "命令执行成功，但没有返回详细内容。"
    return ""


def parse_key_value_lines(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for line in text.splitlines():
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        key = key.strip()
        if key:
            values[key] = value.strip()
    return values


def first_value(values: dict[str, str], *keys: str) -> str | None:
    for key in keys:
        value = values.get(key)
        if value:
            return value
    return None


def parse_windows_task_xml(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    try:
        root = ElementTree.fromstring(text.lstrip("\ufeff"))
    except ElementTree.ParseError:
        return values
    triggers: list[str] = []
    for element in root.iter():
        name = local_xml_name(element.tag)
        if name.endswith("Trigger"):
            triggers.append(name)
        if name in {"UserId", "RunLevel", "Command", "Arguments", "WorkingDirectory", "Enabled", "Hidden", "MultipleInstancesPolicy", "DisallowStartIfOnBatteries", "StopIfGoingOnBatteries", "StartWhenAvailable", "ExecutionTimeLimit"} and element.text:
            values.setdefault(name, element.text.strip())
    if triggers:
        values["Triggers"] = ", ".join(triggers)
    return values


def local_xml_name(tag: str) -> str:
    return tag.rsplit("}", 1)[-1]


def parse_systemctl_properties(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for line in text.splitlines():
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip()
    return values


def parse_systemd_unit_file(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or "=" not in stripped:
            continue
        key, value = stripped.split("=", 1)
        values[key.strip()] = value.strip()
    return values


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
        content = "当前使用 Windows 开机启动计划任务注册，权限级别为 SYSTEM/HIGHEST；非管理员运行时会自动弹出 UAC 授权窗口。"
    elif system == "linux":
        content = "当前使用 systemd user service 注册，通常不需要 sudo；loginctl enable-linger 可能需要管理员授权，失败时服务仍可在用户登录后自启。"
    else:
        content = "当前系统暂不支持自动注册为用户级系统服务。"
    return section_panel(content, "权限说明", "blue")


def user_service_registration_note_panel() -> Panel:
    return section_panel("当前使用 Windows 当前用户计划任务注册，权限级别为 LIMITED；任务会在该用户登录时启动。", "权限说明", "blue")


def manage_windows_task(python: Path, config_path: Path, action: str) -> Any:
    task_name = WINDOWS_TASK_NAME
    if action == "install-user":
        command = ["schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", task_name, "/RL", "LIMITED", "/IT", "/TR", f'"{python}" -m auto_model_key_router.main --config "{config_path}" --serve-foreground']
        return Group(registration_result(command, "Windows 登录自启"), registration_result(windows_task_settings_command(), "Windows 自启设置"), registration_result(["schtasks", "/Run", "/TN", task_name], "Windows 启动"), user_service_registration_note_panel())
    elevated = action.removesuffix("-elevated") if action.endswith("-elevated") else ""
    if elevated:
        if not is_windows_admin():
            return section_panel("未获得管理员权限，无法注册或管理开机启动任务。", "Windows UAC", "red")
        action = elevated
    elif action in {"install", "uninstall", "start", "stop", "restart"} and not is_windows_admin():
        return elevate_windows_service_action(config_path, action)
    if action == "install":
        command = ["schtasks", "/Create", "/F", "/SC", "ONSTART", "/TN", task_name, "/RU", "SYSTEM", "/RL", "HIGHEST", "/TR", f'"{python}" -m auto_model_key_router.main --config "{config_path}" --serve-foreground']
        return Group(registration_result(command, "Windows 开机自启"), registration_result(windows_task_settings_command(), "Windows 自启设置"), registration_result(["schtasks", "/Run", "/TN", task_name], "Windows 启动"), service_registration_note_panel())
    if action == "uninstall":
        config = RouterConfig.load(config_path)
        return Group(registration_result(["schtasks", "/End", "/TN", task_name], "Windows 停止"), stop_background_service(config), registration_result(["schtasks", "/Delete", "/F", "/TN", task_name], "Windows 自启"))
    commands = {
        "start": ["schtasks", "/Run", "/TN", task_name],
        "stop": ["schtasks", "/End", "/TN", task_name],
        "status": ["schtasks", "/Query", "/TN", task_name, "/V", "/FO", "LIST"],
    }
    if action == "restart":
        return Group(registration_result(["schtasks", "/End", "/TN", task_name], "Windows 停止"), registration_result(["schtasks", "/Run", "/TN", task_name], "Windows 启动"))
    return registration_result(commands[action], "Windows 自启")


def is_windows_admin() -> bool:
    if platform.system().lower() != "windows":
        return False
    try:
        import ctypes

        return bool(ctypes.windll.shell32.IsUserAnAdmin())
    except Exception:
        return False


def elevate_windows_service_action(config_path: Path, action: str) -> Panel:
    elevated_action = f"{action}-elevated"
    arguments = ["-m", "auto_model_key_router.main", "--config", str(config_path), "--service", elevated_action]
    script = "$process = Start-Process -FilePath " + powershell_quote(str(sys.executable)) + " -ArgumentList @(" + ",".join(powershell_quote(argument) for argument in arguments) + ") -Verb RunAs -Wait -PassThru; exit $process.ExitCode"
    result = subprocess.run(["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script], capture_output=True, text=True)
    if result.returncode == 0:
        return section_panel("已通过 UAC 管理员权限完成 Windows 开机自启任务管理。", "Windows UAC", "green")
    message = (result.stderr or result.stdout or "管理员授权被取消或执行失败。").strip()
    return section_panel(message, "Windows UAC", "red")


def windows_task_settings_command() -> list[str]:
    script = "$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Seconds 0); Set-ScheduledTask -TaskName " + powershell_quote(WINDOWS_TASK_NAME) + " -Settings $settings | Out-Null"
    return ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script]


def powershell_quote(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def background_python_executable() -> Path:
    python = Path(sys.executable).resolve()
    if os.name == "nt" and python.name.lower() == "python.exe":
        pythonw = python.with_name("pythonw.exe")
        if pythonw.exists():
            return pythonw
    return python


def manage_systemd_user_service(python: Path, config_path: Path, action: str) -> Any:
    service_name = SYSTEMD_USER_SERVICE_NAME
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
