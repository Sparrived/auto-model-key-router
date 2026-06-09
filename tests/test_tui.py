from __future__ import annotations

from rich.text import Text

from auto_model_key_router import logs_tui
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
