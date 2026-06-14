from __future__ import annotations

import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Callable
from xml.etree import ElementTree


CommandRunner = Callable[[list[str]], subprocess.CompletedProcess[str]]


@dataclass(frozen=True)
class StatusDetail:
    title: str
    content: str
    style: str


@dataclass(frozen=True)
class SystemServiceStatus:
    registered: bool
    rows: tuple[tuple[str, str], ...]
    details: tuple[StatusDetail, ...] = ()


def collect_windows_task_status(
    python: Path,
    config_path: Path,
    task_name: str,
    run_command: CommandRunner,
) -> SystemServiceStatus:
    list_result = run_command(
        ["schtasks", "/Query", "/TN", task_name, "/V", "/FO", "LIST"]
    )
    xml_result = run_command(["schtasks", "/Query", "/TN", task_name, "/XML"])
    registered = list_result.returncode == 0 or xml_result.returncode == 0
    list_values = parse_key_value_lines(list_result.stdout)
    task_values = (
        parse_windows_task_xml(xml_result.stdout) if xml_result.returncode == 0 else {}
    )
    rows = (
        ("平台", "Windows 计划任务"),
        ("任务名", task_name),
        ("注册状态", ""),
        (
            "运行状态",
            first_value(
                list_values, "Status", "状态", "Scheduled Task State", "任务状态"
            )
            or "-",
        ),
        ("触发器", task_values.get("Triggers", "-")),
        (
            "账户",
            task_values.get(
                "UserId", first_value(list_values, "Run As User", "运行身份") or "-"
            ),
        ),
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
    )
    raw = command_output(list_result)
    details: list[StatusDetail] = []
    if raw:
        details.append(
            StatusDetail(
                "Windows 原始状态",
                raw,
                "blue" if list_result.returncode == 0 else "red",
            )
        )
    elif not registered:
        details.append(
            StatusDetail(
                "Windows 原始状态", "未找到计划任务，或 schtasks 不可用。", "yellow"
            )
        )
    return SystemServiceStatus(registered, rows, tuple(details))


def collect_systemd_user_status(
    python: Path,
    config_path: Path,
    service_path: Path,
    service_name: str,
    run_command: CommandRunner,
) -> SystemServiceStatus:
    show_result = run_command(
        [
            "systemctl",
            "--user",
            "show",
            service_name,
            "--property=LoadState,ActiveState,SubState,UnitFileState,FragmentPath,ExecMainPID,ExecMainStatus,Result,MainPID,NRestarts,NeedDaemonReload",
            "--no-pager",
        ]
    )
    status_result = run_command(
        ["systemctl", "--user", "status", service_name, "--no-pager"]
    )
    show_values = parse_systemctl_properties(show_result.stdout)
    unit_text = (
        service_path.read_text(encoding="utf-8") if service_path.exists() else ""
    )
    unit_values = parse_systemd_unit_file(unit_text)
    registered = service_path.exists() or show_values.get("LoadState") not in {
        None,
        "",
        "not-found",
    }
    rows = (
        ("平台", "Linux systemd user service"),
        ("服务名", service_name),
        ("注册状态", ""),
        (
            "Unit 文件",
            str(service_path) if service_path.exists() else f"未找到: {service_path}",
        ),
        ("加载状态", show_values.get("LoadState", "-")),
        ("启用状态", show_values.get("UnitFileState", "-")),
        (
            "运行状态",
            "/".join(
                value
                for value in (
                    show_values.get("ActiveState"),
                    show_values.get("SubState"),
                )
                if value
            )
            or "-",
        ),
        (
            "Main PID",
            show_values.get("MainPID") or show_values.get("ExecMainPID") or "-",
        ),
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
    )
    details: list[StatusDetail] = []
    raw_status = command_output(status_result)
    if raw_status:
        details.append(
            StatusDetail(
                "systemd 原始状态",
                raw_status,
                "blue" if status_result.returncode == 0 else "red",
            )
        )
    if unit_text:
        details.append(StatusDetail("systemd 服务文件", unit_text.strip(), "blue"))
    return SystemServiceStatus(registered, rows, tuple(details))


def command_output(result: subprocess.CompletedProcess[str]) -> str:
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
    tracked = {
        "UserId",
        "RunLevel",
        "Command",
        "Arguments",
        "WorkingDirectory",
        "Enabled",
        "Hidden",
        "MultipleInstancesPolicy",
        "DisallowStartIfOnBatteries",
        "StopIfGoingOnBatteries",
        "StartWhenAvailable",
        "ExecutionTimeLimit",
    }
    for element in root.iter():
        name = local_xml_name(element.tag)
        if name.endswith("Trigger"):
            triggers.append(name)
        if name in tracked and element.text:
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
