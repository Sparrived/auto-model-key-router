from __future__ import annotations

import json
from pathlib import Path

from rich.console import ConsoleDimensions
from rich.text import Text

from auto_model_key_router import dashboard
from auto_model_key_router import logs_tui
from auto_model_key_router import service
from auto_model_key_router import tui


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

    dashboard.configure_cli_interactively(config_path)

    data = json.loads(config_path.read_text(encoding="utf-8"))
    assert data["local_api_key"].startswith("amkr_")
    assert actions == [(str(config_path), "install")]


def test_windows_service_registration_is_user_limited() -> None:
    result = service.manage_windows_task.__globals__["registration_result"]
    commands: list[list[str]] = []
    service.manage_windows_task.__globals__["registration_result"] = lambda command, title: commands.append(command) or tui.section_panel("ok", title, "green")
    try:
        service.manage_windows_task(Path("pythonw.exe"), Path("router-config.json"), "install")
    finally:
        service.manage_windows_task.__globals__["registration_result"] = result

    assert "/RL" in commands[0]
    assert "LIMITED" in commands[0]
    assert "/IT" in commands[0]
