from __future__ import annotations

from io import StringIO
import json
from pathlib import Path
import subprocess

from rich.console import Console, ConsoleDimensions
from rich.text import Text

from auto_model_key_router import clipboard
from auto_model_key_router import config_editor
from auto_model_key_router import dashboard
from auto_model_key_router import log_files
from auto_model_key_router import logs_tui
from auto_model_key_router import service
from auto_model_key_router import tui


def render_plain(renderable) -> str:
    buffer = StringIO()
    console = Console(file=buffer, force_terminal=False, width=180)
    console.print(renderable)
    return buffer.getvalue()


def test_parse_sgr_mouse_sequence_reads_wheel_events() -> None:
    assert tui.parse_sgr_mouse_sequence("64;20;10M") == "scroll_up"
    assert tui.parse_sgr_mouse_sequence("65;20;10M") == "scroll_down"
    assert tui.parse_sgr_mouse_sequence("0;20;10M") is None


def test_scrollable_content_state_clamps_offset(monkeypatch) -> None:
    monkeypatch.setattr(tui, "content_viewport_height", lambda option_count: 3)
    content = Text("\n".join(str(index) for index in range(10)))

    _, offset, max_offset, viewport_height = tui.scrollable_content_state(content, 99, 1)

    assert offset == max_offset
    assert max_offset >= 7
    assert viewport_height == 3


def test_should_handle_wheel_debounces_same_direction(monkeypatch) -> None:
    moments = iter([10.0, 10.05, 10.20, 10.25])
    monkeypatch.setattr(tui.time, "monotonic", lambda: next(moments))

    handled, last_key, last_at = tui.should_handle_wheel("scroll_down", None, 0.0)
    assert handled
    handled, last_key, last_at = tui.should_handle_wheel("scroll_down", last_key, last_at)
    assert not handled
    handled, last_key, last_at = tui.should_handle_wheel("scroll_down", last_key, last_at)
    assert handled
    handled, _, _ = tui.should_handle_wheel("scroll_up", last_key, last_at)
    assert handled


def test_content_scroll_offset_uses_single_line_wheel_step() -> None:
    assert tui.content_scroll_offset("scroll_down", 0, 10, 6) == 1
    assert tui.content_scroll_offset("scroll_up", 5, 10, 6) == 4
    assert tui.content_scroll_offset("page_down", 0, 10, 6) == 6
    assert tui.content_scroll_offset("page_up", 5, 10, 6) == 0


def test_mouse_wheel_mode_is_enabled_by_default(monkeypatch) -> None:
    output = StringIO()
    monkeypatch.setattr(tui.sys, "stdout", output)
    monkeypatch.setattr(tui, "enable_windows_virtual_terminal_input", lambda: lambda: None)

    with tui.mouse_wheel_mode():
        pass

    assert output.getvalue() == f"{tui.MOUSE_MODE_ENABLE}{tui.MOUSE_MODE_DISABLE}"


def test_copy_to_clipboard_uses_available_command(monkeypatch) -> None:
    calls: list[dict[str, object]] = []

    class Result:
        returncode = 0
        stdout = ""
        stderr = ""

    def run(command, input, text, capture_output, timeout, check):
        calls.append({"command": command, "input": input, "text": text, "capture_output": capture_output, "timeout": timeout, "check": check})
        return Result()

    monkeypatch.setattr(clipboard, "clipboard_commands", lambda: [["clip"]])
    monkeypatch.setattr(clipboard.subprocess, "run", run)

    copied, message = clipboard.copy_to_clipboard("secret-key")

    assert copied
    assert message == "已复制到剪贴板。"
    assert calls == [{"command": ["clip"], "input": "secret-key", "text": True, "capture_output": True, "timeout": 5, "check": False}]


def test_show_result_page_can_copy_text(monkeypatch) -> None:
    choices = iter(["c", "0"])
    copied: list[str] = []
    monkeypatch.setattr(tui, "select_option", lambda title, options, selected=0, content=None: next(choices))
    monkeypatch.setattr(tui, "copy_to_clipboard", lambda text: copied.append(text) or (True, "已复制到剪贴板。"))

    tui.show_result_page("结果", tui.ResultPage(Text("done"), copy_text="secret-key"))

    assert copied == ["secret-key"]


def test_fit_terminal_lines_preserves_bottom_and_pads() -> None:
    lines = [[tui.Segment(str(index))] for index in range(5)]

    fitted = tui.fit_terminal_lines(lines, 3)

    assert len(fitted) == 3
    assert fitted[0][0].text == tui.FOLDED_CONTENT_MARKER
    assert fitted[1][0].text == "3"
    assert fitted[2][0].text == "4"

    padded = tui.fit_terminal_lines([[tui.Segment("content")]], 3)

    assert len(padded) == 3
    assert all(isinstance(segment, tui.Segment) for line in padded for segment in line)


def test_terminal_frame_keeps_footer_at_bottom(monkeypatch) -> None:
    monkeypatch.setattr(tui.console.__class__, "size", property(lambda _: ConsoleDimensions(20, 5)))

    lines = tui.renderable_line_segments(tui.terminal_frame([Text("a\nb\nc\nd\ne")], Text("footer")), 20)

    assert len(lines) == 5
    assert "footer" in "".join(segment.text for segment in lines[-1])


def test_open_log_file_reports_missing_file(tmp_path) -> None:
    message = logs_tui.open_log_file(str(tmp_path / "missing.log"))

    assert message.startswith("运行日志不存在:")


def test_open_log_file_uses_windows_default_editor(tmp_path, monkeypatch) -> None:
    log_file = tmp_path / "server.log"
    log_file.write_text("started", encoding="utf-8")
    opened: list[str] = []
    monkeypatch.setattr(logs_tui.sys, "platform", "win32")
    monkeypatch.setattr(logs_tui.os, "startfile", lambda path: opened.append(path), raising=False)

    message = logs_tui.open_log_file(str(log_file))

    assert opened == [str(log_file)]
    assert message.startswith("已使用默认文本编辑器打开:")


def test_open_log_file_uses_xdg_open_on_linux(tmp_path, monkeypatch) -> None:
    log_file = tmp_path / "server.log"
    log_file.write_text("started", encoding="utf-8")
    commands: list[list[str]] = []
    monkeypatch.setattr(logs_tui.sys, "platform", "linux")
    monkeypatch.setattr(logs_tui.subprocess, "Popen", lambda command, stdout, stderr: commands.append(command))

    message = logs_tui.open_log_file(str(log_file))

    assert commands == [["xdg-open", str(log_file)]]
    assert message.startswith("已使用默认文本编辑器打开:")


def test_log_line_text_colors_level_logger_and_status() -> None:
    line = '2026-06-10 12:00:00 INFO uvicorn.access 127.0.0.1:1 - "GET /health HTTP/1.1" 200'

    text = logs_tui.log_line_text(line)

    assert text.plain == line
    styles = {str(span.style) for span in text.spans}
    assert "bold green" in styles
    assert "blue" in styles


def test_log_line_text_colors_error_fallback() -> None:
    text = logs_tui.log_line_text("Traceback (most recent call last):")

    assert text.plain == "Traceback (most recent call last):"
    assert str(text.style) == "red"


def test_archive_current_log_moves_existing_log_and_creates_new_file(tmp_path) -> None:
    log_file = tmp_path / "server.log"
    log_file.write_text("old log", encoding="utf-8")

    archive_path = log_files.archive_current_log(str(log_file))

    assert archive_path is not None
    assert archive_path.name.startswith("server.")
    assert archive_path.name.endswith(".log")
    assert archive_path.read_text(encoding="utf-8") == "old log"
    assert log_file.exists()
    assert log_file.read_text(encoding="utf-8") == ""


def test_archived_log_paths_orders_newest_first(tmp_path) -> None:
    log_file = tmp_path / "server.log"
    log_file.write_text("current", encoding="utf-8")
    older = tmp_path / "server.20260610-120000.log"
    newer = tmp_path / "server.20260610-130000.log"
    older.write_text("older", encoding="utf-8")
    newer.write_text("newer", encoding="utf-8")

    assert log_files.archived_log_paths(str(log_file)) == [newer, older]


def test_log_file_choices_include_current_and_archives(tmp_path) -> None:
    log_file = tmp_path / "server.log"
    log_file.write_text("current", encoding="utf-8")
    archive = tmp_path / "server.20260610-120000.log"
    archive.write_text("old", encoding="utf-8")

    assert logs_tui.log_file_choices(str(log_file)) == [log_file, archive]


def test_service_logs_renderable_can_show_archived_log(tmp_path) -> None:
    log_file = tmp_path / "server.20260610-120000.log"
    log_file.write_text("2026-06-10 12:00:00 INFO uvicorn started", encoding="utf-8")

    output = render_plain(logs_tui.service_logs_renderable(str(log_file), 10, 0, "历史日志 1/1 · server.20260610-120000.log"))

    assert "历史日志 1/1" in output
    assert "server.20260610-120000.log" in output
    assert "uvicorn started" in output


def test_main_menu_keeps_one_click_config_on_homepage() -> None:
    assert dashboard.MENU_OPTIONS == [("1", "一键配置"), ("2", "模型 Key"), ("3", "CLI 设置"), ("0", "退出")]
    assert ("1", "模型服务") in dashboard.SETTINGS_OPTIONS
    assert ("2", "本地鉴权") in dashboard.SETTINGS_OPTIONS
    assert ("3", "监听配置") in dashboard.SETTINGS_OPTIONS
    assert ("4", "调用日志") in dashboard.SETTINGS_OPTIONS
    assert ("5", "版本更新") in dashboard.SETTINGS_OPTIONS


def test_configure_cli_generates_auth_key_and_installs_service(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    actions: list[tuple[str, str]] = []
    monkeypatch.setattr(dashboard, "manage_system_service", lambda path, action: actions.append((str(path), action)) or tui.section_panel("installed", "服务", "green"))

    result = dashboard.configure_cli_interactively(config_path)

    data = json.loads(config_path.read_text(encoding="utf-8"))
    assert data["local_api_key"].startswith("amkr_")
    assert isinstance(result, tui.ResultPage)
    assert result.copy_text == data["local_api_key"]
    assert actions == [(str(config_path), "install")]


def test_copy_api_key_interactively_returns_copyable_result(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(json.dumps({"models": [{"id": "test-model", "keys": [{"name": "main", "api_key": "sk-secret", "base_url": "https://example.com/v1"}]}]}, ensure_ascii=False), encoding="utf-8")
    choices = iter(["1", "1"])
    monkeypatch.setattr(config_editor, "select_option", lambda title, options: next(choices))

    result = config_editor.copy_api_key_interactively(config_path)

    assert isinstance(result, tui.ResultPage)
    assert result.copy_text == "sk-secret"
    output = render_plain(result.content)
    assert "test-model" in output
    assert "main" in output
    assert "sk-secret" not in output


def test_windows_user_service_registration_is_user_limited() -> None:
    result = service.manage_windows_task.__globals__["registration_result"]
    commands: list[list[str]] = []
    service.manage_windows_task.__globals__["registration_result"] = lambda command, title: commands.append(command) or tui.section_panel("ok", title, "green")
    try:
        service.manage_windows_task(Path("pythonw.exe"), Path("router-config.json"), "install-user")
    finally:
        service.manage_windows_task.__globals__["registration_result"] = result

    assert "/RL" in commands[0]
    assert "LIMITED" in commands[0]
    assert "/IT" in commands[0]
    assert any("AllowStartIfOnBatteries" in " ".join(command) for command in commands)
    assert any("DontStopIfGoingOnBatteries" in " ".join(command) for command in commands)


def test_windows_service_registration_uses_onstart_when_admin(monkeypatch) -> None:
    result = service.manage_windows_task.__globals__["registration_result"]
    commands: list[list[str]] = []
    monkeypatch.setattr(service, "is_windows_admin", lambda: True)
    service.manage_windows_task.__globals__["registration_result"] = lambda command, title: commands.append(command) or tui.section_panel("ok", title, "green")
    try:
        service.manage_windows_task(Path("pythonw.exe"), Path("router-config.json"), "install")
    finally:
        service.manage_windows_task.__globals__["registration_result"] = result

    assert "/SC" in commands[0]
    assert "ONSTART" in commands[0]
    assert "/RU" in commands[0]
    assert "SYSTEM" in commands[0]
    assert "HIGHEST" in commands[0]
    assert any("StartWhenAvailable" in " ".join(command) for command in commands)
    assert any("ExecutionTimeLimit" in " ".join(command) for command in commands)


def test_windows_service_registration_requests_uac_when_not_admin(monkeypatch) -> None:
    requested: list[tuple[str, str]] = []
    monkeypatch.setattr(service, "is_windows_admin", lambda: False)
    monkeypatch.setattr(service, "elevate_windows_service_action", lambda path, action: requested.append((str(path), action)) or tui.section_panel("uac", "Windows UAC", "green"))

    service.manage_windows_task(Path("pythonw.exe"), Path("router-config.json"), "install")

    assert requested == [("router-config.json", "install")]


def test_windows_service_status_panel_shows_registration_details(monkeypatch) -> None:
    xml = """<?xml version="1.0" encoding="UTF-16"?>
<Task xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><BootTrigger><Enabled>true</Enabled></BootTrigger></Triggers>
  <Principals><Principal><UserId>SYSTEM</UserId><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><StartWhenAvailable>true</StartWhenAvailable><ExecutionTimeLimit>PT0S</ExecutionTimeLimit></Settings>
  <Actions><Exec><Command>C:\\Python\\pythonw.exe</Command><Arguments>-m auto_model_key_router.main --config C:\\config.json --serve-foreground</Arguments><WorkingDirectory>C:\\app</WorkingDirectory></Exec></Actions>
</Task>"""
    status = """TaskName: \\AutoModelKeyRouter
Status: Running
Last Run Time: 2026/6/10 12:00:00
Next Run Time: N/A
Last Result: 0x0"""

    def fake_run(command: list[str]) -> subprocess.CompletedProcess[str]:
        if "/XML" in command:
            return subprocess.CompletedProcess(command, 0, xml, "")
        return subprocess.CompletedProcess(command, 0, status, "")

    monkeypatch.setattr(service, "run_status_command", fake_run)

    output = render_plain(service.windows_task_status_panel(Path("C:/Python/pythonw.exe"), Path("C:/config.json")))

    assert "Windows 计划任务" in output
    assert "已注册" in output
    assert "BootTrigger" in output
    assert "SYSTEM" in output
    assert "HighestAvailable" in output
    assert "--serve-foreground" in output
    assert "PT0S" in output
    assert "0x0" in output
    assert "config.json" in output


def test_systemd_service_status_panel_shows_registration_details(tmp_path, monkeypatch) -> None:
    service_dir = tmp_path / ".config" / "systemd" / "user"
    service_dir.mkdir(parents=True)
    (service_dir / "auto-model-key-router.service").write_text(
        "\n".join(
            [
                "[Service]",
                "WorkingDirectory=/opt/amkr",
                "ExecStart=/usr/bin/python -m auto_model_key_router.main --config /etc/amkr.json --serve-foreground",
                "Restart=always",
                "[Install]",
                "WantedBy=default.target",
            ]
        ),
        encoding="utf-8",
    )
    show = """LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
MainPID=1234
Result=success
ExecMainStatus=0
NRestarts=2
NeedDaemonReload=no"""
    status = "● auto-model-key-router.service - Auto Model Key Router\n     Active: active (running)"

    def fake_run(command: list[str]) -> subprocess.CompletedProcess[str]:
        if "show" in command:
            return subprocess.CompletedProcess(command, 0, show, "")
        return subprocess.CompletedProcess(command, 0, status, "")

    monkeypatch.setattr(service.Path, "home", lambda: tmp_path)
    monkeypatch.setattr(service, "run_status_command", fake_run)

    output = render_plain(service.systemd_user_service_status_panel(Path("/usr/bin/python"), Path("/etc/amkr.json")))

    assert "Linux systemd user service" in output
    assert "已注册" in output
    assert "active/running" in output
    assert "enabled" in output
    assert "1234" in output
    assert "/opt/amkr" in output
    assert "--serve-foreground" in output
    assert "default.target" in output
