from __future__ import annotations

from contextlib import contextmanager
from io import StringIO
import json
from datetime import datetime, timedelta
from pathlib import Path, PurePosixPath
import sqlite3
import subprocess

import httpx
import pytest
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



def v3_config(models: dict[str, list[dict[str, object]]], **extra: object) -> dict[str, object]:
    providers = {}
    model_data = {}
    for model_id, keys in models.items():
        targets = []
        aliases = []
        for index, key in enumerate(keys):
            key_name = str(key.get("name") or f"{model_id}-key-{index + 1}")
            provider_id = str(key.get("provider") or f"{model_id}-{key_name}")
            upstream_model = str(key.get("upstream_model") or model_id)
            providers[provider_id] = {
                "base_url": str(key.get("base_url") or "https://example.test"),
                "keys": {
                    key_name: {
                        name: value
                        for name, value in {
                            "api_key": key.get("api_key"),
                            "enabled": key.get("enabled", True),
                            "allow_visitor": key.get("allow_visitor"),
                        }.items()
                        if value is not None
                    }
                },
                "pools": {key_name: {"keys": [key_name], "models": [upstream_model]}},
            }
            targets.append({"provider": provider_id, "pool": key_name, "upstream_model": upstream_model})
            aliases.extend(str(alias) for alias in key.get("aliases", []) if str(alias))
        model_data[model_id] = {"targets": targets}
        if aliases:
            model_data[model_id]["aliases"] = aliases
    data: dict[str, object] = {"config_version": 3, "providers": providers, "models": model_data}
    data.update(extra)
    return data

def test_request_stats_renderable_shows_current_rpm_and_tpm(tmp_path: Path, monkeypatch) -> None:
    database_path = tmp_path / "metrics.db"
    now = datetime(2026, 1, 2, 3, 4, 5, tzinfo=logs_tui.BEIJING_TZ)
    monkeypatch.setattr(logs_tui, "datetime", type("FrozenDateTime", (), {"now": staticmethod(lambda tz=None: now)}))
    with sqlite3.connect(database_path) as connection:
        connection.execute(
            """
            CREATE TABLE request_metrics (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                created_at TEXT NOT NULL,
                caller_type TEXT NOT NULL DEFAULT 'local',
                model_id TEXT NOT NULL,
                key_name TEXT NOT NULL,
                status_code INTEGER,
                success INTEGER NOT NULL,
                retried INTEGER NOT NULL,
                prompt_tokens INTEGER NOT NULL DEFAULT 0,
                completion_tokens INTEGER NOT NULL DEFAULT 0,
                total_tokens INTEGER NOT NULL DEFAULT 0,
                cached_tokens INTEGER NOT NULL DEFAULT 0,
                first_token_ms INTEGER NOT NULL DEFAULT 0,
                duration_ms INTEGER NOT NULL DEFAULT 0
            )
            """
        )
        rows = [
            ((now - timedelta(seconds=30)).isoformat(), "local", "model-a", "key-a", 200, 1, 0, 3, 7, 10, 0, 100, 500),
            ((now - timedelta(seconds=90)).isoformat(), "local", "model-a", "key-a", 200, 1, 0, 5, 5, 10, 0, 120, 550),
        ]
        connection.executemany(
            """
            INSERT INTO request_metrics (
                created_at, caller_type, model_id, key_name, status_code, success, retried,
                prompt_tokens, completion_tokens, total_tokens, cached_tokens,
                first_token_ms, duration_ms
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            rows,
        )
        connection.commit()

    output = render_plain(logs_tui.request_stats_renderable(str(database_path), 1, 10))

    assert "近1分钟 流量" in output
    assert "1 RPM" in output
    assert "10 TPM" in output


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
    monkeypatch.setattr(tui.sys, "platform", "win32")
    monkeypatch.setattr(tui, "enable_windows_virtual_terminal_input", lambda: lambda: None)

    with tui.mouse_wheel_mode():
        pass

    assert output.getvalue() == f"{tui.MOUSE_MODE_ENABLE}{tui.MOUSE_MODE_DISABLE}"


def test_enable_pool_models_adds_matching_model_routes() -> None:
    data = {
        "config_version": 3,
        "providers": {
            "vendor": {
                "base_url": "https://example.test",
                "keys": {"main": {"api_key": "sk-main"}},
                "pools": {
                    "default": {
                        "keys": ["main"],
                        "available_models": ["model-a", "model-b"],
                    }
                },
            }
        },
        "models": {},
    }

    config_editor.enable_pool_models(data, "vendor", "default", ["model-a", "model-b"])

    assert data["providers"]["vendor"]["pools"]["default"]["models"] == [
        "model-a",
        "model-b",
    ]
    assert data["models"] == {
        "model-a": {"targets": [{"provider": "vendor", "pool": "default", "upstream_model": "model-a"}]},
        "model-b": {"targets": [{"provider": "vendor", "pool": "default", "upstream_model": "model-b"}]},
    }


def test_mouse_wheel_mode_is_disabled_on_linux(monkeypatch) -> None:
    output = StringIO()
    monkeypatch.setattr(tui.sys, "stdout", output)
    monkeypatch.setattr(tui.sys, "platform", "linux")

    with tui.mouse_wheel_mode():
        pass

    assert output.getvalue() == ""


def test_posix_input_mode_uses_cbreak_and_restores_terminal(monkeypatch) -> None:
    original_settings = object()
    calls: list[tuple] = []

    class FakeStdin:
        def fileno(self):
            return 7

    class FakeTermios:
        TCSADRAIN = "drain"

        def tcgetattr(self, fd):
            calls.append(("get", fd))
            return original_settings

        def tcsetattr(self, fd, when, settings):
            calls.append(("restore", fd, when, settings))

    class FakeTty:
        def setcbreak(self, fd):
            calls.append(("cbreak", fd))

        def setraw(self, fd):
            raise AssertionError("persistent TUI input must not disable output processing")

    monkeypatch.setattr(tui.sys, "platform", "linux")
    monkeypatch.setattr(tui.sys, "stdin", FakeStdin())
    monkeypatch.setattr(tui, "termios", FakeTermios(), raising=False)
    monkeypatch.setattr(tui, "tty", FakeTty(), raising=False)
    monkeypatch.setattr(tui, "_posix_input_mode_active", False)

    with tui.posix_input_mode():
        assert tui._posix_input_mode_active

    assert calls == [
        ("get", 7),
        ("cbreak", 7),
        ("restore", 7, "drain", original_settings),
    ]
    assert not tui._posix_input_mode_active


def test_read_key_windows_single_escape_returns_cancel(monkeypatch) -> None:
    class FakeMsvcrt:
        def getwch(self):
            return "\x1b"

    monkeypatch.setattr(tui.sys, "platform", "win32")
    monkeypatch.setattr(tui, "msvcrt", FakeMsvcrt(), raising=False)
    monkeypatch.setattr(tui, "read_windows_char_if_available", lambda: None)

    assert tui.read_key() == "cancel"


def test_read_key_windows_escape_sequence_still_handles_arrows(monkeypatch) -> None:
    class FakeMsvcrt:
        def getwch(self):
            return "\x1b"

    chars = iter(["[", "A"])
    monkeypatch.setattr(tui.sys, "platform", "win32")
    monkeypatch.setattr(tui, "msvcrt", FakeMsvcrt(), raising=False)
    monkeypatch.setattr(tui, "read_windows_char_if_available", lambda: next(chars, None))

    assert tui.read_key() == "up"


def test_read_key_windows_ignores_sgr_mouse_click(monkeypatch) -> None:
    class FakeMsvcrt:
        def __init__(self):
            self.chars = iter(["\x1b", "0", ";", "2", "0", ";", "1", "0", "M"])

        def getwch(self):
            return next(self.chars)

    chars = iter(["[", "<"])
    monkeypatch.setattr(tui.sys, "platform", "win32")
    monkeypatch.setattr(tui, "msvcrt", FakeMsvcrt(), raising=False)
    monkeypatch.setattr(tui, "read_windows_char_if_available", lambda: next(chars, None))

    assert tui.read_key() == "ignore"


def test_read_posix_key_handles_ss3_arrows(monkeypatch) -> None:
    class FakeStdin:
        def __init__(self):
            self.chars = list("\x1bOA")

        def fileno(self):
            return 0

    stdin = FakeStdin()
    monkeypatch.setattr(tui.sys, "stdin", stdin)
    monkeypatch.setattr(tui.os, "read", lambda fd, size: stdin.chars.pop(0).encode("utf-8") if stdin.chars else b"")
    monkeypatch.setattr(tui, "termios", type("Termios", (), {"TCSADRAIN": object(), "tcgetattr": lambda self, fd: object(), "tcsetattr": lambda self, fd, when, settings: None})(), raising=False)
    monkeypatch.setattr(tui, "tty", type("Tty", (), {"setraw": lambda self, fd: None})(), raising=False)
    monkeypatch.setattr(tui, "select", type("Select", (), {"select": lambda self, readers, writers, errors, timeout: (readers, writers, errors) if stdin.chars else ([], [], [])})(), raising=False)

    assert tui.read_posix_key() == "up"


def test_read_posix_key_ignores_sgr_mouse_click(monkeypatch) -> None:
    class FakeStdin:
        def __init__(self):
            self.chars = list("\x1b[<0;20;10M")

        def fileno(self):
            return 0

    stdin = FakeStdin()
    monkeypatch.setattr(tui.sys, "stdin", stdin)
    monkeypatch.setattr(tui.os, "read", lambda fd, size: stdin.chars.pop(0).encode("utf-8") if stdin.chars else b"")
    monkeypatch.setattr(tui, "termios", type("Termios", (), {"TCSADRAIN": object(), "tcgetattr": lambda self, fd: object(), "tcsetattr": lambda self, fd, when, settings: None})(), raising=False)
    monkeypatch.setattr(tui, "tty", type("Tty", (), {"setraw": lambda self, fd: None})(), raising=False)
    monkeypatch.setattr(tui, "select", type("Select", (), {"select": lambda self, readers, writers, errors, timeout: (readers, writers, errors) if stdin.chars else ([], [], [])})(), raising=False)

    assert tui.read_posix_key() == "ignore"


def test_read_posix_key_ignores_unknown_escape_sequence(monkeypatch) -> None:
    class FakeStdin:
        def __init__(self):
            self.chars = list("\x1bZ")

        def fileno(self):
            return 0

    stdin = FakeStdin()
    monkeypatch.setattr(tui.sys, "stdin", stdin)
    monkeypatch.setattr(tui.os, "read", lambda fd, size: stdin.chars.pop(0).encode("utf-8") if stdin.chars else b"")
    monkeypatch.setattr(tui, "termios", type("Termios", (), {"TCSADRAIN": object(), "tcgetattr": lambda self, fd: object(), "tcsetattr": lambda self, fd, when, settings: None})(), raising=False)
    monkeypatch.setattr(tui, "tty", type("Tty", (), {"setraw": lambda self, fd: None})(), raising=False)
    monkeypatch.setattr(tui, "select", type("Select", (), {"select": lambda self, readers, writers, errors, timeout: (readers, writers, errors) if stdin.chars else ([], [], [])})(), raising=False)

    assert tui.read_posix_key() == "ignore"


def test_read_posix_key_ignores_single_escape(monkeypatch) -> None:
    class FakeStdin:
        def __init__(self):
            self.chars = list("\x1b")

        def fileno(self):
            return 0

    stdin = FakeStdin()
    monkeypatch.setattr(tui.sys, "stdin", stdin)
    monkeypatch.setattr(tui.os, "read", lambda fd, size: stdin.chars.pop(0).encode("utf-8") if stdin.chars else b"")
    monkeypatch.setattr(tui, "termios", type("Termios", (), {"TCSADRAIN": object(), "tcgetattr": lambda self, fd: object(), "tcsetattr": lambda self, fd, when, settings: None})(), raising=False)
    monkeypatch.setattr(tui, "tty", type("Tty", (), {"setraw": lambda self, fd: None})(), raising=False)
    monkeypatch.setattr(tui, "select", type("Select", (), {"select": lambda self, readers, writers, errors, timeout: (readers, writers, errors) if stdin.chars else ([], [], [])})(), raising=False)

    assert tui.read_posix_key() == "ignore"


def test_read_posix_key_ignores_incomplete_csi_sequence(monkeypatch) -> None:
    class FakeStdin:
        def __init__(self):
            self.chars = list("\x1b[")

        def fileno(self):
            return 0

    stdin = FakeStdin()
    monkeypatch.setattr(tui.sys, "stdin", stdin)
    monkeypatch.setattr(tui.os, "read", lambda fd, size: stdin.chars.pop(0).encode("utf-8") if stdin.chars else b"")
    monkeypatch.setattr(tui, "termios", type("Termios", (), {"TCSADRAIN": object(), "tcgetattr": lambda self, fd: object(), "tcsetattr": lambda self, fd, when, settings: None})(), raising=False)
    monkeypatch.setattr(tui, "tty", type("Tty", (), {"setraw": lambda self, fd: None})(), raising=False)
    monkeypatch.setattr(tui, "select", type("Select", (), {"select": lambda self, readers, writers, errors, timeout: (readers, writers, errors) if stdin.chars else ([], [], [])})(), raising=False)

    assert tui.read_posix_key() == "ignore"


def test_read_posix_key_ignores_incomplete_page_sequence(monkeypatch) -> None:
    class FakeStdin:
        def __init__(self):
            self.chars = list("\x1b[5")

        def fileno(self):
            return 0

    stdin = FakeStdin()
    monkeypatch.setattr(tui.sys, "stdin", stdin)
    monkeypatch.setattr(tui.os, "read", lambda fd, size: stdin.chars.pop(0).encode("utf-8") if stdin.chars else b"")
    monkeypatch.setattr(tui, "termios", type("Termios", (), {"TCSADRAIN": object(), "tcgetattr": lambda self, fd: object(), "tcsetattr": lambda self, fd, when, settings: None})(), raising=False)
    monkeypatch.setattr(tui, "tty", type("Tty", (), {"setraw": lambda self, fd: None})(), raising=False)
    monkeypatch.setattr(tui, "select", type("Select", (), {"select": lambda self, readers, writers, errors, timeout: (readers, writers, errors) if stdin.chars else ([], [], [])})(), raising=False)

    assert tui.read_posix_key() == "ignore"


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


def test_copy_to_clipboard_uses_terminal_clipboard_for_remote_session(monkeypatch) -> None:
    output = StringIO()
    commands_called = False

    def clipboard_commands():
        nonlocal commands_called
        commands_called = True
        return [["clip"]]

    monkeypatch.setenv("SSH_CONNECTION", "client server")
    monkeypatch.setattr(clipboard.sys, "stdout", output)
    monkeypatch.setattr(clipboard, "clipboard_commands", clipboard_commands)

    copied, message = clipboard.copy_to_clipboard("secret-key")

    assert copied
    assert message == "已发送复制请求到终端剪贴板。"
    assert output.getvalue() == "\033]52;c;c2VjcmV0LWtleQ==\a"
    assert not commands_called


def test_paste_from_clipboard_rejects_whitespace_only(monkeypatch) -> None:
    class Result:
        returncode = 0
        stdout = " \t\n"
        stderr = ""

    monkeypatch.setattr(clipboard, "paste_commands", lambda: [["fake-paste"]])
    monkeypatch.setattr(clipboard.subprocess, "run", lambda command, text, capture_output, timeout, check: Result())

    pasted, message = clipboard.paste_from_clipboard()

    assert not pasted
    assert message == "剪贴板没有可粘贴内容。"


def test_show_result_page_can_copy_text(monkeypatch) -> None:
    choices = iter(["c", "0"])
    copied: list[str] = []
    monkeypatch.setattr(tui, "select_option", lambda title, options, selected=0, content=None: next(choices))
    monkeypatch.setattr(tui, "copy_to_clipboard", lambda text: copied.append(text) or (True, "已复制到剪贴板。"))

    tui.show_result_page("结果", tui.ResultPage(Text("done"), copy_text="secret-key"))

    assert copied == ["secret-key"]


def test_run_submodule_catches_unhandled_exception(monkeypatch) -> None:
    shown: list[tuple[str, str]] = []
    monkeypatch.setattr(tui, "clear_terminal_history", lambda: None)
    monkeypatch.setattr(tui, "show_result_page", lambda title, content: shown.append((title, render_plain(content))))

    result = tui.run_submodule(lambda: (_ for _ in ()).throw(RuntimeError("boom")))

    assert shown and shown[0][0] == "操作出错"
    assert "RuntimeError: boom" in shown[0][1]
    assert "RuntimeError: boom" in render_plain(result)


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
    assert "footer" in "".join(segment.text for segment in lines[-2])
    assert "footer" not in "".join(segment.text for segment in lines[-1])


def test_terminal_frame_scrolls_without_losing_window_chrome(monkeypatch) -> None:
    monkeypatch.setattr(tui.console.__class__, "size", property(lambda _: ConsoleDimensions(32, 8)))

    first = tui.terminal_frame_state([Text("\n".join(f"line-{index}" for index in range(12)))], Text("footer"))
    last = tui.terminal_frame_state(
        [Text("\n".join(f"line-{index}" for index in range(12)))],
        Text("footer"),
        offset=first.max_offset,
    )
    first_text = "\n".join("".join(segment.text for segment in line) for line in tui.renderable_line_segments(first.renderable, 32))
    last_text = "\n".join("".join(segment.text for segment in line) for line in tui.renderable_line_segments(last.renderable, 32))

    assert first.max_offset > 0
    assert "line-0" in first_text
    assert "line-11" in last_text
    assert "footer" in first_text
    assert len(tui.renderable_line_segments(first.renderable, 32)) == 8


def test_terminal_frame_never_exceeds_narrow_terminal_width(monkeypatch) -> None:
    monkeypatch.setattr(tui.console.__class__, "size", property(lambda _: ConsoleDimensions(12, 8)))

    lines = tui.renderable_line_segments(
        tui.terminal_frame([tui.section_panel("a very long value", "content")], Text("footer")),
        12,
    )

    assert len(lines) == 8
    assert all(sum(segment.cell_length for segment in line) <= 12 for line in lines)


def test_long_option_menu_keeps_selected_row_visible(monkeypatch) -> None:
    monkeypatch.setattr(tui.console.__class__, "size", property(lambda _: ConsoleDimensions(40, 10)))
    options = [(str(index), f"item-{index}") for index in range(20)]

    state = tui.render_option_menu_state("menu", options, selected=18)
    text = "\n".join("".join(segment.text for segment in line) for line in tui.renderable_line_segments(state.renderable, 40))

    assert state.max_offset > 0
    assert "item-18" in text
    assert tui.SELECTED_ROW_MARKER in text


def test_prompt_text_edits_inside_live_window(monkeypatch) -> None:
    keys = iter(["s", "e", "c", "r", "e", "t", "enter"])
    live_instances = []

    @contextmanager
    def input_mode():
        yield

    class FakeLive:
        def __init__(self, renderable, **kwargs):
            self.renderable = renderable
            live_instances.append(self)

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, traceback):
            return False

        def update(self, renderable, refresh=False):
            self.renderable = renderable

    monkeypatch.setattr(tui, "posix_input_mode", input_mode)
    monkeypatch.setattr(tui, "Live", FakeLive)
    monkeypatch.setattr(tui, "read_key_responsive", lambda on_resize=None: next(keys))

    result = tui.prompt_text("edit", "API key", password=True)

    assert result == "secret"
    assert "secret" not in render_plain(live_instances[0].renderable)
    assert "******" in render_plain(live_instances[0].renderable)


def test_prompt_text_q_on_empty_value_cancels(monkeypatch) -> None:
    @contextmanager
    def input_mode():
        yield

    class FakeLive:
        def __init__(self, renderable, **kwargs):
            self.renderable = renderable

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, traceback):
            return False

        def update(self, renderable, refresh=False):
            self.renderable = renderable

    monkeypatch.setattr(tui, "posix_input_mode", input_mode)
    monkeypatch.setattr(tui, "Live", FakeLive)
    monkeypatch.setattr(tui, "read_key_responsive", lambda on_resize=None: "q")

    cancelled = False
    try:
        tui.prompt_text("edit", "API key")
    except KeyboardInterrupt:
        cancelled = True

    assert cancelled


def test_prompt_text_choices_use_select_option(monkeypatch) -> None:
    calls = []

    def choose(title, options, selected=0, content=None, **kwargs):
        calls.append((title, options, selected))
        return "round_robin"

    monkeypatch.setattr(tui, "select_option", choose)
    monkeypatch.setattr(
        tui,
        "read_key_responsive",
        lambda on_resize=None: (_ for _ in ()).throw(AssertionError("manual input should not be used")),
    )

    result = tui.prompt_text(
        "路由模式",
        "路由模式",
        choices=["priority", "round_robin", "only_first"],
        default="round_robin",
    )

    assert result == "round_robin"
    assert calls == [
        (
            "路由模式",
            [
                ("priority", "priority"),
                ("round_robin", "round_robin"),
                ("only_first", "only_first"),
                ("0", "返回"),
            ],
            1,
        )
    ]


def test_select_option_q_returns_back_option(monkeypatch) -> None:
    @contextmanager
    def input_mode():
        yield

    class FakeLive:
        def __init__(self, renderable, **kwargs):
            self.renderable = renderable

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, traceback):
            return False

        def update(self, renderable, refresh=False):
            self.renderable = renderable

    monkeypatch.setattr(tui, "posix_input_mode", input_mode)
    monkeypatch.setattr(tui, "mouse_wheel_mode", input_mode)
    monkeypatch.setattr(tui, "Live", FakeLive)
    monkeypatch.setattr(tui, "read_key_responsive", lambda on_resize=None: "q")

    assert tui.select_option("menu", [("1", "one"), ("0", "返回")]) == "0"


def test_select_multiple_returns_initial_checked_values(monkeypatch) -> None:
    @contextmanager
    def input_mode():
        yield

    class FakeLive:
        def __init__(self, renderable, **kwargs):
            self.renderable = renderable

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, traceback):
            return False

        def update(self, renderable, refresh=False):
            self.renderable = renderable

    monkeypatch.setattr(tui, "posix_input_mode", input_mode)
    monkeypatch.setattr(tui, "mouse_wheel_mode", input_mode)
    monkeypatch.setattr(tui, "Live", FakeLive)
    monkeypatch.setattr(tui, "read_key_responsive", lambda on_resize=None: "enter")

    selected = tui.select_multiple(
        "模型池",
        [("model-a", "model-a"), ("model-b", "model-b")],
        checked_values={"model-b"},
    )

    assert selected == ["model-b"]


def test_watch_logs_reads_keys_inside_posix_input_mode(monkeypatch) -> None:
    events: list[str] = []

    @contextmanager
    def input_mode():
        events.append("enter")
        try:
            yield
        finally:
            events.append("exit")

    @contextmanager
    def mouse_mode():
        yield

    class FakeLive:
        def __init__(self, renderable, **kwargs):
            self.renderable = renderable

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, traceback):
            return False

        def update(self, renderable, refresh=False):
            self.renderable = renderable

    def key_pressed():
        assert events == ["enter"]
        return "q"

    monkeypatch.setattr(logs_tui, "posix_input_mode", input_mode)
    monkeypatch.setattr(logs_tui, "mouse_wheel_mode", mouse_mode)
    monkeypatch.setattr(logs_tui, "Live", FakeLive)
    monkeypatch.setattr(logs_tui, "key_pressed", key_pressed)
    monkeypatch.setattr(logs_tui, "render_live_logs", lambda *args, **kwargs: Text("logs"))

    logs_tui.watch_logs("requests.db", "service.log", 10)

    assert events == ["enter", "exit"]


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


def test_request_stats_pages_filter_local_and_visitor_calls(tmp_path) -> None:
    database_path = tmp_path / "metrics.sqlite3"
    with sqlite3.connect(database_path) as connection:
        connection.execute(
            """
            CREATE TABLE request_metrics (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                created_at TEXT NOT NULL,
                caller_type TEXT NOT NULL,
                model_id TEXT NOT NULL,
                key_name TEXT NOT NULL,
                status_code INTEGER,
                success INTEGER NOT NULL,
                retried INTEGER NOT NULL,
                prompt_tokens INTEGER NOT NULL,
                completion_tokens INTEGER NOT NULL,
                total_tokens INTEGER NOT NULL,
                cached_tokens INTEGER NOT NULL,
                first_token_ms INTEGER NOT NULL,
                duration_ms INTEGER NOT NULL
            )
            """
        )
        connection.executemany(
            """
            INSERT INTO request_metrics (
                created_at, caller_type, model_id, key_name, status_code, success,
                retried, prompt_tokens, completion_tokens, total_tokens,
                cached_tokens, first_token_ms, duration_ms
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            [
                ("2026-06-13T12:00:00+08:00", "local", "local-model", "local-key", 200, 1, 0, 2, 1, 3, 0, 10, 20),
                ("2026-06-13T12:01:00+08:00", "visitor", "visitor-model", "visitor-key", 200, 1, 0, 4, 2, 6, 1, 15, 30),
            ],
        )

    local_output = render_plain(logs_tui.request_stats_renderable(str(database_path), 1, 10, 4, caller_type="local"))
    visitor_output = render_plain(logs_tui.request_stats_renderable(str(database_path), 1, 10, 4, caller_type="visitor"))

    assert "本地调用" in local_output
    assert "local-model" in local_output
    assert "visitor-model" not in local_output
    assert "访客调用" in visitor_output
    assert "visitor-model" in visitor_output
    assert "local-model" not in visitor_output


def test_logs_hide_visitor_navigation_when_feature_missing(monkeypatch) -> None:
    monkeypatch.setattr(logs_tui, "visitor_feature_available", lambda: False)

    header = render_plain(logs_tui.log_header_renderable("stats"))
    help_text = render_plain(logs_tui.log_help_text("logs"))

    assert "访客" not in header
    assert "访客" not in help_text


def test_request_stats_pages_support_legacy_database_without_caller_type(tmp_path) -> None:
    database_path = tmp_path / "legacy-metrics.sqlite3"
    with sqlite3.connect(database_path) as connection:
        connection.execute(
            """
            CREATE TABLE request_metrics (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                created_at TEXT NOT NULL,
                model_id TEXT NOT NULL,
                key_name TEXT NOT NULL,
                status_code INTEGER,
                success INTEGER NOT NULL,
                retried INTEGER NOT NULL,
                prompt_tokens INTEGER NOT NULL,
                completion_tokens INTEGER NOT NULL,
                total_tokens INTEGER NOT NULL
            )
            """
        )
        connection.execute(
            """
            INSERT INTO request_metrics (
                created_at, model_id, key_name, status_code, success, retried,
                prompt_tokens, completion_tokens, total_tokens
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            ("2026-06-13T12:00:00+08:00", "legacy-model", "legacy-key", 200, 1, 0, 1, 1, 2),
        )

    local_output = render_plain(logs_tui.request_stats_renderable(str(database_path), 1, 10, 4, caller_type="local"))
    visitor_output = render_plain(logs_tui.request_stats_renderable(str(database_path), 1, 10, 4, caller_type="visitor"))

    assert "legacy-model" in local_output
    assert "共 0 条" in visitor_output


def test_main_menu_keeps_one_click_config_on_homepage() -> None:
    assert dashboard.MENU_OPTIONS == [("1", "一键配置"), ("2", "供应商模型"), ("3", "统一模型"), ("4", "调用日志"), ("5", "CLI 设置"), ("0", "退出")]
    assert dashboard.ONE_CLICK_OPTIONS == [("1", "路由服务"), ("2", "Claude Code"), ("3", "Codex"), ("0", "返回")]
    assert ("1", "模型服务") in dashboard.SETTINGS_OPTIONS
    assert ("2", "本地鉴权") in dashboard.SETTINGS_OPTIONS
    assert ("3", "监听配置") in dashboard.SETTINGS_OPTIONS
    assert ("4", "超时配置") in dashboard.SETTINGS_OPTIONS
    assert ("5", "配置迁移") in dashboard.SETTINGS_OPTIONS
    assert ("6", "版本更新") in dashboard.SETTINGS_OPTIONS
    assert all(label != "调用日志" for _, label in dashboard.SETTINGS_OPTIONS)


def test_set_timeouts_interactively_updates_all_timeouts(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            v3_config(
                {},
                request_timeout=60,
                stream_first_byte_timeout=90,
                stream_idle_timeout=180,
            )
        ),
        encoding="utf-8",
    )
    answers = iter(("75", "95", "185"))
    monkeypatch.setattr(
        config_editor, "prompt_text", lambda *args, **kwargs: next(answers)
    )
    monkeypatch.setattr(
        config_editor,
        "restart_service_after_config_change",
        lambda *args: Text("reloaded"),
    )

    result = config_editor.set_timeouts_interactively(config_path)

    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert saved["request_timeout"] == 75.0
    assert saved["stream_first_byte_timeout"] == 95.0
    assert saved["stream_idle_timeout"] == 185.0
    assert "已更新超时配置" in render_plain(result)


@pytest.mark.parametrize("answers", [("invalid",), ("60", "0")])
def test_set_timeouts_interactively_rejects_invalid_values(
    tmp_path, monkeypatch, answers: tuple[str, ...]
) -> None:
    config_path = tmp_path / "router-config.json"
    original = v3_config(
        {},
        request_timeout=60,
        stream_first_byte_timeout=90,
        stream_idle_timeout=180,
    )
    config_path.write_text(json.dumps(original), encoding="utf-8")
    responses = iter(answers)
    monkeypatch.setattr(
        config_editor, "prompt_text", lambda *args, **kwargs: next(responses)
    )

    result = config_editor.set_timeouts_interactively(config_path)

    assert json.loads(config_path.read_text(encoding="utf-8")) == original
    assert "必须" in render_plain(result)


def test_dashboard_model_table_focuses_on_model_mapping() -> None:
    config = dashboard.RouterConfig.from_dict(
        {
            "config_version": 3,
            "models": {
                "local-model": {
                    "aliases": ["alias-model"],
                    "routing_mode": "priority",
                    "reasoning_effort": "high",
                    "targets": [
                        {
                            "provider": "gateway",
                            "pool": "default",
                            "upstream_model": "upstream-model",
                        }
                    ],
                }
            },
            "providers": {
                "gateway": {
                    "base_url": "https://gateway.example.test",
                    "keys": {"main": {"api_key": "sk-main"}},
                    "pools": {"default": {"keys": ["main"]}},
                }
            },
        }
    )

    output = render_plain(dashboard.config_renderables(config, Path("router-config.json"))[-1])

    assert "模型配置" in output
    assert "模型路由" not in output
    assert "别名" in output
    assert "路由模式" in output
    assert "Keys" in output
    assert "推理强度" not in output
    assert "上游" not in output
    assert "原生支持" not in output
    assert "gateway.example.test" not in output
    assert "high" not in output


def test_tui_switches_unified_model_and_key(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            v3_config(
                {
                    "model-one": [{"name": "one", "api_key": "sk-one", "base_url": "https://one.test"}],
                    "model-two": [
                        {"name": "two-a", "api_key": "sk-two-a", "base_url": "https://two-a.test", "aliases": ["second"]},
                        {"name": "two-b", "api_key": "sk-two-b", "base_url": "https://two-b.test"},
                        {"name": "disabled", "api_key": "sk-disabled", "base_url": "https://disabled.test", "enabled": False},
                    ],
                },
                local_api_key="local-key",
                unified_model={"model": "model-one", "key": "one"},
            ),
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    choices = iter(["2", "2"])
    menus: list[list[tuple[str, str]]] = []

    def choose(title, options, selected=0, content=None, *, on_key=None):
        menus.append(options)
        return next(choices)

    monkeypatch.setattr(dashboard, "select_option", choose)

    result = dashboard.switch_unified_model_interactively(config_path, choose_model=True)

    data = json.loads(config_path.read_text(encoding="utf-8"))
    assert data["unified_model"] == {"model": "model-two", "key": "two-b"}
    assert "model-two" in render_plain(result)
    assert "two-b" in render_plain(result)
    assert all("disabled" not in label for _, label in menus[1])


def test_tui_can_restore_unified_model_auto_routing(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            v3_config(
                {
                    "test-model": [
                        {"name": "key-1", "api_key": "sk-one", "base_url": "https://one.test"},
                        {"name": "key-2", "api_key": "sk-two", "base_url": "https://two.test"},
                    ]
                },
                local_api_key="local-key",
                unified_model={"model": "test-model", "key": "key-1"},
            ),
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(dashboard, "select_option", lambda title, options, selected=0, content=None, **kw: "a")

    result = dashboard.switch_unified_model_interactively(config_path, choose_model=False)

    data = json.loads(config_path.read_text(encoding="utf-8"))
    assert data["unified_model"] == {"model": "test-model"}
    assert "自动路由" in render_plain(result)


def test_terminal_ui_exits_immediately_after_windows_update_handoff(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config = dashboard.RouterConfig.from_dict({"config_version": 3, "providers": {}, "models": {}})
    shown: list[object] = []
    monkeypatch.setattr(dashboard, "check_latest_version", lambda timeout: None)
    monkeypatch.setattr(dashboard, "select_menu_option", lambda *args: ("5", 0))
    monkeypatch.setattr(dashboard, "run_submodule", lambda action: dashboard.UpdateInstallOutcome(Text("updater"), updated=True, handoff=True))
    monkeypatch.setattr(dashboard, "show_result_page", lambda title, content: shown.append(content))

    dashboard.run_terminal_ui(config_path, config)

    assert shown == []


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


def test_one_click_config_menu_routes_to_agent_submenu(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    choices = iter(["2", "0"])
    agents: list[str] = []
    monkeypatch.setattr(dashboard, "select_option", lambda title, options, selected=0, content=None, **kw: next(choices))
    monkeypatch.setattr(dashboard, "run_submodule", lambda action: action())
    monkeypatch.setattr(dashboard, "manage_agent_config_interactively", lambda path, agent: agents.append(agent))

    dashboard.manage_one_click_config_interactively(config_path)

    assert agents == [dashboard.CLAUDE_CODE]


def test_model_service_menu_moves_autostart_into_single_entry(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(json.dumps({"config_version": 3, "providers": {}, "models": {}}, ensure_ascii=False), encoding="utf-8")
    menus: list[list[tuple[str, str]]] = []

    def choose(title, options, selected=0, content=None, *, on_key=None):
        menus.append(options)
        return "0"

    monkeypatch.setattr(dashboard, "select_option", choose)

    dashboard.manage_system_service_interactively(config_path)

    assert ("1", "开机自启") in menus[0]
    assert all(label not in {"安装开机自启", "安装登录自启", "卸载自启"} for _, label in menus[0])


def test_autostart_menu_installs_boot_or_login_when_unregistered(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    choices = iter(["2", "0"])
    menus: list[list[tuple[str, str]]] = []
    actions: list[str] = []

    def choose(title, options, selected=0, content=None, *, on_key=None):
        menus.append(options)
        return next(choices)

    monkeypatch.setattr(dashboard, "is_system_service_registered", lambda path: False)
    monkeypatch.setattr(dashboard, "system_service_status_panel", lambda path: Text("status"))
    monkeypatch.setattr(dashboard, "manage_system_service", lambda path, action: actions.append(action) or Text("done"))
    monkeypatch.setattr(dashboard, "show_result_page", lambda title, content: None)
    monkeypatch.setattr(dashboard, "clear_terminal_history", lambda: None)
    monkeypatch.setattr(dashboard, "select_option", choose)

    dashboard.manage_autostart_interactively(config_path)

    assert menus[0] == [("1", "安装开机自启"), ("2", "安装登录自启"), ("0", "返回")]
    assert actions == ["install-user"]


def test_autostart_menu_shows_uninstall_when_registered(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    choices = iter(["1", "0"])
    menus: list[list[tuple[str, str]]] = []
    actions: list[str] = []

    def choose(title, options, selected=0, content=None, *, on_key=None):
        menus.append(options)
        return next(choices)

    monkeypatch.setattr(dashboard, "is_system_service_registered", lambda path: True)
    monkeypatch.setattr(dashboard, "system_service_status_panel", lambda path: Text("status"))
    monkeypatch.setattr(dashboard, "manage_system_service", lambda path, action: actions.append(action) or Text("done"))
    monkeypatch.setattr(dashboard, "show_result_page", lambda title, content: None)
    monkeypatch.setattr(dashboard, "clear_terminal_history", lambda: None)
    monkeypatch.setattr(dashboard, "select_option", choose)

    dashboard.manage_autostart_interactively(config_path)

    assert menus[0] == [("1", "卸载自启"), ("0", "返回")]
    assert actions == ["uninstall"]


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


def test_select_api_key_highlights_visitor_enabled_key(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "models": [
                    {
                        "id": "test-model",
                        "keys": [
                            {"name": "local", "api_key": "sk-local"},
                            {"name": "guest", "api_key": "sk-guest", "allow_visitor": True},
                        ],
                    }
                ]
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    choices = iter(["1", "2"])
    menus: list[list[tuple[str, str]]] = []

    def choose(title, options, selected=0, content=None):
        menus.append(options)
        return next(choices)

    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: True)
    monkeypatch.setattr(config_editor, "select_option", choose)

    _, _, key_index = config_editor.select_api_key(config_path, "选择 Key")

    assert key_index == 1
    assert "[bold bright_magenta]guest[/]" in menus[1][1][1]
    assert "[bold bright_magenta]local[/]" not in menus[1][0][1]


def test_v2_summary_hides_visitor_column_when_feature_missing(monkeypatch) -> None:
    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: False)

    output = render_plain(
        config_editor.v2_summary_panel(
            {
                "config_version": 3,
                "providers": {
                    "gateway": {
                        "base_url": "https://gateway.example.test",
                        "keys": {"main": {"api_key": "sk-main", "allow_visitor": True}},
                        "pools": {},
                    }
                },
                "models": {},
            }
        )
    )

    assert "访客" not in output


def test_provider_key_menu_hides_visitor_when_feature_missing(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "gateway": {
                        "base_url": "https://gateway.example.test",
                        "keys": {"main": {"api_key": "sk-main", "allow_visitor": True}},
                        "pools": {"default": {"keys": ["main"]}},
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    choices = iter(["1", "1", "0", "0"])
    menus = []
    contents = []

    def choose(title, options, selected=0, content=None, **kwargs):
        menus.append(options)
        contents.append(render_plain(content))
        return next(choices)

    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: False)
    monkeypatch.setattr(config_editor, "select_option", choose)

    config_editor.manage_provider_keys_interactively(config_path)

    assert all("访客" not in label for _, label in menus[-1])
    assert "访客" not in contents[-1]


def test_export_config_interactively_only_copies_key_config_without_visitor(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "host": "0.0.0.0",
                "port": 9000,
                "local_api_key": "local-secret",
                "providers": {
                    "default": {
                        "base_url": "https://default.example.com",
                        "keys": {"main": {"api_key": "sk-secret", "allow_visitor": True}},
                        "pools": {"main": {"keys": ["main"]}},
                    }
                },
                "models": {
                    "test-model": {
                        "routing_mode": "priority",
                        "targets": [{"provider": "default", "pool": "main", "upstream_model": "test-model"}],
                    }
                },
                "unified_model": {"model": "test-model", "key": "main"},
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: False)

    result = config_editor.export_config_interactively(config_path)

    assert isinstance(result, tui.ResultPage)
    assert "\n" not in (result.copy_text or "")
    assert json.loads(result.copy_text or "") == {
        "config_version": 3,
        "providers": {
            "default": {
                "base_url": "https://default.example.com",
                "keys": {"main": {"api_key": "sk-secret"}},
                "pools": {"main": {"keys": ["main"]}},
            }
        },
        "models": {
            "test-model": {
                "routing_mode": "priority",
                "targets": [{"provider": "default", "pool": "main", "upstream_model": "test-model"}],
            }
        },
    }
    output = render_plain(result.content)
    assert "sk-secret" not in output
    assert "local-secret" not in output
    assert "本地鉴权、监听地址、端口及其他 CLI 设置不会复制" in output
    assert "visitor" not in output
    assert "访客" not in output


def test_export_config_interactively_includes_visitor_access_when_installed(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "default": {
                        "base_url": "https://example.com/v1",
                        "keys": {"main": {"api_key": "sk-secret", "allow_visitor": True}},
                        "pools": {"main": {"keys": ["main"]}},
                    }
                },
                "models": {
                    "test-model": {
                        "targets": [{"provider": "default", "pool": "main", "upstream_model": "test-model"}],
                    }
                },
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: True)

    result = config_editor.export_config_interactively(config_path)

    copied = json.loads(result.copy_text or "")
    assert copied["providers"]["default"]["keys"]["main"]["allow_visitor"] is True
    assert "包含访客访问权限" in render_plain(result.content)


def test_paste_config_interactively_appends_only_key_config(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    old_data = {
        "config_version": 3,
        "host": "127.0.0.1",
        "port": 8123,
        "request_timeout": 45,
        "local_api_key": "old-local",
        "providers": {
            "old": {
                "base_url": "https://old.example.com",
                "keys": {"old": {"api_key": "sk-old"}},
                "pools": {"old": {"keys": ["old"]}},
            }
        },
        "models": {
            "old-model": {
                "targets": [{"provider": "old", "pool": "old", "upstream_model": "old-model"}],
            }
        },
    }
    new_data = {
        "config_version": 3,
        "host": "0.0.0.0",
        "port": 9000,
        "request_timeout": 120,
        "local_api_key": "new-local",
        "providers": {
            "new": {
                "base_url": "https://new.example.com",
                "keys": {"new": {"api_key": "sk-new", "allow_visitor": True}},
                "pools": {"new": {"keys": ["new"], "models": ["new-model"]}},
            }
        },
        "models": {
            "new-model": {
                "targets": [{"provider": "new", "pool": "new", "upstream_model": "new-model"}],
            }
        },
    }
    config_path.write_text(json.dumps(old_data, ensure_ascii=False), encoding="utf-8")
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: json.dumps(new_data, ensure_ascii=False))
    monkeypatch.setattr(config_editor, "confirm_choice", lambda message, default=False: True)
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda path, old_config, new_config: Text("reloaded"))
    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: False)

    result = config_editor.paste_config_interactively(config_path)

    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert saved["host"] == "127.0.0.1"
    assert saved["port"] == 8123
    assert saved["request_timeout"] == 45
    assert saved["local_api_key"] == "old-local"
    assert saved["providers"]["new"]["keys"] == {"new": {"api_key": "sk-new"}}
    assert saved["providers"]["new"]["pools"]["new"]["models"] == ["new-model"]
    assert saved["models"]["new-model"]["targets"] == [
        {"provider": "new", "pool": "new", "upstream_model": "new-model"}
    ]
    output = render_plain(result)
    assert "new-model" not in output
    assert "sk-new" not in output
    assert "已追加粘贴的 Key 配置，并保留现有模型和本机 CLI 设置" in output
    assert "新增模型: 1" in output
    assert "新增 Key: 1" in output


def test_paste_config_interactively_appends_keys_without_overwriting_model_settings(
    tmp_path, monkeypatch
) -> None:
    config_path = tmp_path / "router-config.json"
    old_data = {
        "config_version": 3,
        "local_api_key": "old-local",
        "providers": {
            "old": {
                "base_url": "https://old.example.com",
                "keys": {"main": {"api_key": "sk-old"}},
                "pools": {"main": {"keys": ["main"]}},
            }
        },
        "models": {
            "shared-model": {
                "aliases": ["local-alias"],
                "routing_mode": "priority",
                "targets": [{"provider": "old", "pool": "main", "upstream_model": "shared-model"}],
            }
        },
    }
    pasted_data = {
        "config_version": 3,
        "providers": {
            "old": {
                "base_url": "https://old.example.com",
                "keys": {
                    "duplicate": {"api_key": "sk-old"},
                    "main": {"api_key": "sk-new"},
                },
                "pools": {
                    "duplicate": {"keys": ["duplicate"]},
                    "main": {"keys": ["main"]},
                },
            }
        },
        "models": {
            "shared-model": {
                "aliases": ["remote-alias"],
                "routing_mode": "only_first",
                "targets": [
                    {"provider": "old", "pool": "duplicate", "upstream_model": "shared-model"},
                    {"provider": "old", "pool": "main", "upstream_model": "shared-model"},
                ],
            }
        },
    }
    config_path.write_text(json.dumps(old_data, ensure_ascii=False), encoding="utf-8")
    monkeypatch.setattr(
        config_editor,
        "prompt_text",
        lambda *args, **kwargs: json.dumps(pasted_data, ensure_ascii=False),
    )
    monkeypatch.setattr(config_editor, "confirm_choice", lambda message, default=False: True)
    monkeypatch.setattr(
        config_editor,
        "restart_service_after_config_change",
        lambda path, old_config, new_config: Text("reloaded"),
    )
    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: False)

    result = config_editor.paste_config_interactively(config_path)

    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert saved["models"]["shared-model"]["aliases"] == ["local-alias"]
    assert saved["models"]["shared-model"]["routing_mode"] == "priority"
    assert saved["providers"]["old"]["keys"] == {
        "main": {"api_key": "sk-old"},
        "main-2": {"api_key": "sk-new"},
    }
    assert saved["providers"]["old"]["pools"]["main-2"] == {
        "keys": ["main-2"],
        "models": [],
    }
    assert saved["models"]["shared-model"]["targets"][-1] == {
        "provider": "old",
        "pool": "main-2",
        "upstream_model": "shared-model",
    }
    output = render_plain(result)
    assert "新增模型: 0" in output
    assert "新增 Key: 1" in output
    assert "跳过重复 Key: 1" in output


def test_paste_config_interactively_rejects_invalid_pasted_config(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    old_text = json.dumps({"local_api_key": "old-local", "models": []}, ensure_ascii=False)
    config_path.write_text(old_text, encoding="utf-8")
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: "not json")

    result = config_editor.paste_config_interactively(config_path)

    assert config_path.read_text(encoding="utf-8") == old_text
    assert "粘贴内容不是有效 JSON" in render_plain(result)


def test_paste_config_interactively_rejects_empty_input(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    old_text = json.dumps({"local_api_key": "old-local", "models": []}, ensure_ascii=False)
    config_path.write_text(old_text, encoding="utf-8")
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: "  ")

    result = config_editor.paste_config_interactively(config_path)

    assert config_path.read_text(encoding="utf-8") == old_text
    assert "未输入配置内容" in render_plain(result)


def test_add_config_interactively_only_prompts_model_options_for_new_model(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "default": {
                        "base_url": "https://upstream.example.com",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"main": {"keys": ["main"]}},
                    }
                },
                "models": {
                    "existing-model": {
                        "aliases": ["Existing"],
                        "routing_mode": "priority",
                        "reasoning_effort": "high",
                        "targets": [{"provider": "default", "pool": "main", "upstream_model": "existing-model"}],
                    }
                },
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    prompts: list[str] = []
    answers = iter(["sk-extra", "extra"])
    menus: list[tuple[str, list[tuple[str, str]]]] = []

    def ask(title: str, prompt: str, **kwargs):
        prompts.append(prompt)
        return next(answers)

    def choose(title, options, selected=0, content=None):
        menus.append((title, options))
        return "1"

    monkeypatch.setattr(config_editor, "prompt_text", ask)
    monkeypatch.setattr(config_editor, "select_option", choose)
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda path, old_config, new_config: Text("reloaded"))
    monkeypatch.setattr(config_editor, "_select_model_with_discovery", lambda models, base_url, api_key: ("existing-model", []))

    config_editor.add_config_interactively(config_path, ask_continue=False)

    data = json.loads(config_path.read_text(encoding="utf-8"))
    model = data["models"]["existing-model"]
    provider = data["providers"]["default"]
    assert prompts == ["API key", "Key 名称"]
    assert menus[0][0] == "选择上游 URL"
    assert menus[0][1][0][1] == "upstream.example.com"
    assert model["aliases"] == ["Existing"]
    assert model["routing_mode"] == "priority"
    assert model["reasoning_effort"] == "high"
    assert provider["keys"]["extra"] == {"api_key": "sk-extra", "enabled": True}
    assert provider["pools"]["extra"] == {"keys": ["extra"]}
    assert model["targets"][-1] == {"provider": "default", "pool": "extra", "upstream_model": "existing-model"}


def test_add_config_interactively_prompts_model_options_for_new_model(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "default": {
                        "base_url": "https://upstream.example.com",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"main": {"keys": ["main"]}},
                    }
                },
                "models": {
                    "existing-model": {
                        "targets": [{"provider": "default", "pool": "main", "upstream_model": "existing-model"}],
                    }
                },
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    prompts: list[str] = []
    answers = iter(["https://primary.example.com", "sk-primary", "New Alias", "only_first", "low", "primary"])

    def ask(title: str, prompt: str, **kwargs):
        prompts.append(prompt)
        return next(answers)

    monkeypatch.setattr(config_editor, "prompt_text", ask)
    monkeypatch.setattr(config_editor, "select_option", lambda title, options, selected=0, content=None: "n")
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda path, old_config, new_config: Text("reloaded"))
    monkeypatch.setattr(config_editor, "_select_model_with_discovery", lambda models, base_url, api_key: ("new-model", []))

    config_editor.add_config_interactively(config_path, ask_continue=False)

    data = json.loads(config_path.read_text(encoding="utf-8"))
    model = data["models"]["new-model"]
    provider = data["providers"]["default"]
    assert prompts == ["新的上游 base_url", "API key", "显示名称/别名，多个用逗号分隔", "路由模式", "推理强度", "Key 名称"]
    assert model["aliases"] == ["New Alias"]
    assert model["routing_mode"] == "only_first"
    assert model["reasoning_effort"] == "low"
    assert provider["keys"]["primary"] == {"api_key": "sk-primary", "enabled": True}
    assert provider["pools"]["primary"] == {"keys": ["primary"]}
    assert model["targets"] == [{"provider": "default", "pool": "primary", "upstream_model": "new-model"}]


def test_provider_model_menu_opens_model_settings(tmp_path, monkeypatch) -> None:
    menus: list[list[tuple[str, str]]] = []

    def choose(title, options, selected=0, content=None, *, on_key=None):
        menus.append(options)
        return "0"

    monkeypatch.setattr(config_editor, "select_option", choose)

    config_editor.manage_model_keys_interactively(tmp_path / "router-config.json")

    assert menus[0] == [
        ("1", "添加供应商 Key"),
        ("2", "管理供应商 Key"),
        ("3", "模型池"),
        ("4", "模型设置"),
        ("0", "返回"),
    ]


def test_model_settings_menu_includes_route_controls(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"default": {"keys": ["main"], "models": ["backup-model"]}},
                    }
                },
                "models": {
                    "local-model": {
                        "aliases": [],
                        "routing_mode": "round_robin",
                        "targets": [
                            {"provider": "vendor", "pool": "default", "upstream_model": "upstream-model"}
                        ],
                    }
                },
            }
        ),
        encoding="utf-8",
    )
    models = iter(["local-model", None])
    menus: list[list[tuple[str, str]]] = []

    monkeypatch.setattr(config_editor, "select_v2_model", lambda data, title: next(models))
    monkeypatch.setattr(
        config_editor,
        "select_option",
        lambda title, options, selected=0, content=None, **kwargs: menus.append(options) or "0",
    )

    config_editor.manage_v2_model_settings_interactively(config_path)

    assert menus[0] == [
        ("1", "别名"),
        ("2", "路由模式"),
        ("3", "路由目标"),
        ("4", "添加路由目标"),
        ("5", "删除模型"),
        ("0", "返回"),
    ]


def test_selected_model_routes_return_to_model_settings(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"default": {"keys": ["main"]}},
                    }
                },
                "models": {
                    "local-model": {
                        "targets": [{"provider": "vendor", "pool": "default"}]
                    }
                },
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: "0")

    config_editor.manage_model_routes_interactively(config_path, "local-model")


def test_add_model_route_to_selected_model_skips_local_model_prompt(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"default": {"keys": ["main"], "models": ["backup-model"]}},
                    }
                },
                "models": {
                    "local-model": {
                        "targets": [
                            {"provider": "vendor", "pool": "default", "upstream_model": "primary-model"}
                        ]
                    }
                },
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "select_provider_pool", lambda data, title: ("vendor", "default"))
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: "1")
    monkeypatch.setattr(
        config_editor,
        "select_or_enter_model_id",
        lambda *args, **kwargs: pytest.fail("不应再次选择本地模型"),
    )
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: None)

    config_editor.add_model_route_interactively(config_path, "local-model")

    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert saved["models"]["local-model"]["targets"] == [
        {"provider": "vendor", "pool": "default", "upstream_model": "primary-model"},
        {"provider": "vendor", "pool": "default", "upstream_model": "backup-model"},
    ]


def test_delete_model_updates_unified_model_fallback(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"default": {"keys": ["main"]}},
                    }
                },
                "models": {
                    "delete-me": {"targets": [{"provider": "vendor", "pool": "default"}]},
                    "keep-me": {"targets": [{"provider": "vendor", "pool": "default"}]},
                },
                "unified_model": {"model": "delete-me"},
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "confirm_choice", lambda *args, **kwargs: True)
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: None)

    config_editor.delete_v2_model_interactively(config_path, "delete-me")

    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert "delete-me" not in saved["models"]
    assert saved["unified_model"] == {"model": "keep-me"}


def test_v2_summary_model_section_focuses_on_mapping() -> None:
    output = render_plain(
        config_editor.v2_summary_panel(
            {
                "config_version": 3,
                "providers": {
                    "gateway": {
                        "base_url": "https://gateway.example.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"default": {"keys": ["main"]}},
                    }
                },
                "models": {
                    "local-model": {
                        "aliases": ["alias-model"],
                        "routing_mode": "priority",
                        "targets": [
                            {
                                "provider": "gateway",
                                "pool": "default",
                                "upstream_model": "upstream-model",
                            }
                        ],
                    }
                },
            }
        )
    )

    assert "模型设置" in output
    assert "模型路由" not in output
    assert "别名" in output
    assert "路由模式" in output
    assert "上游" not in output
    assert "gateway/default" not in output
    assert "upstream-model" not in output


def test_v2_tui_adds_provider_key_and_model_route(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps({"config_version": 3, "local_api_key": "local-key", "providers": {}, "models": {}}),
        encoding="utf-8",
    )
    prompts = iter(["openai", "https://api.openai.com", "main", "sk-main", "default"])
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: next(prompts))
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: "n")
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))
    monkeypatch.setattr(config_editor, "apply_pool_probe", lambda provider, pool_name, **kwargs: provider["pools"][pool_name].update({"available_models": ["upstream-model"], "all_available_models": ["upstream-model"], "models": []}) or {"models": ["upstream-model"], "all_models": ["upstream-model"], "routes": {}})
    monkeypatch.setattr(config_editor, "prompt_pool_enabled_models", lambda pool: ["upstream-model"])

    result = config_editor.add_provider_key_interactively(config_path)
    data = json.loads(config_path.read_text(encoding="utf-8"))

    assert "添加完成" in render_plain(result)
    assert data["config_version"] == 3
    assert data["providers"]["openai"]["keys"]["main"]["api_key"] == "sk-main"
    assert data["providers"]["openai"]["pools"]["default"]["keys"] == ["main"]
    data["providers"]["openai"]["pools"]["default"]["models"] = ["upstream-model"]
    config_path.write_text(json.dumps(data), encoding="utf-8")

    prompts = iter(["local-model"])
    choices = iter(["1", "1", "1", "__custom__"])
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: next(prompts))
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: next(choices))

    result = config_editor.add_model_route_interactively(config_path)
    data = json.loads(config_path.read_text(encoding="utf-8"))

    assert "添加完成" in render_plain(result)
    assert data["models"]["local-model"]["targets"] == [
        {"provider": "openai", "pool": "default", "upstream_model": "upstream-model"}
    ]


@pytest.mark.parametrize("pool_name", ["existing", "new-pool"])
def test_add_provider_key_assigns_selected_pool_only(tmp_path, monkeypatch, pool_name: str) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "local_api_key": "local-key",
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {},
                        "pools": {"existing": {"keys": [], "models": []}},
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    prompts = iter(["main", "sk-main"])
    probed: list[str] = []
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: "1")
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: next(prompts))
    monkeypatch.setattr(config_editor, "select_or_enter_pool_name", lambda *args, **kwargs: pool_name)
    monkeypatch.setattr(
        config_editor,
        "apply_pool_probe",
        lambda provider, selected_pool, **kwargs: probed.append(selected_pool)
        or provider["pools"][selected_pool].update(
            {"available_models": ["gpt-a"], "all_available_models": ["gpt-a"]}
        )
        or {"models": ["gpt-a"], "all_models": ["gpt-a"], "routes": {}},
    )
    monkeypatch.setattr(config_editor, "prompt_pool_enabled_models", lambda pool: ["gpt-a"])
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))

    config_editor.add_provider_key_interactively(config_path)
    data = json.loads(config_path.read_text(encoding="utf-8"))

    pools = data["providers"]["vendor"]["pools"]
    assert pools[pool_name]["keys"] == ["main"]
    assert [name for name, pool in pools.items() if "main" in pool["keys"]] == [pool_name]
    assert "default" not in pools
    assert pools[pool_name]["models"] == ["gpt-a"]
    assert probed == [pool_name]


def test_editing_pool_moves_selected_key_from_previous_pool(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {
                            "move-me": {"api_key": "sk-move"},
                            "stay": {"api_key": "sk-stay"},
                        },
                        "pools": {
                            "old": {"keys": ["move-me"], "models": []},
                            "current": {"keys": ["stay"], "models": []},
                        },
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "select_or_enter_pool_name", lambda *args, **kwargs: "current")
    monkeypatch.setattr(config_editor, "select_multiple", lambda *args, **kwargs: ["move-me"])
    monkeypatch.setattr(config_editor, "prompt_pool_enabled_models", lambda pool: [])
    monkeypatch.setattr(
        config_editor,
        "apply_pool_probe",
        lambda provider, pool_name, **kwargs: {"models": [], "all_models": [], "routes": {}},
    )
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))

    config_editor.update_provider_pool_interactively(config_path, "vendor", "1")
    pools = json.loads(config_path.read_text(encoding="utf-8"))["providers"]["vendor"]["pools"]

    assert pools["current"]["keys"] == ["stay", "move-me"]
    assert pools["old"]["keys"] == []


def test_duplicate_pool_memberships_are_repaired_interactively(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {
                            "pool-a": {"keys": ["main"], "models": ["gpt-a"]},
                            "pool-b": {"keys": ["main"], "models": ["gpt-b"]},
                        },
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    choices: list[tuple[list[str], str]] = []

    def choose(pools, *, default):
        choices.append((sorted(pools), default))
        return "new-pool"

    monkeypatch.setattr(config_editor, "select_or_enter_pool_name", choose)

    repaired = config_editor.repair_duplicate_pool_memberships_interactively(config_path)
    pools = json.loads(config_path.read_text(encoding="utf-8"))["providers"]["vendor"]["pools"]

    assert repaired is True
    assert choices == [(["pool-a", "pool-b"], "pool-a")]
    assert pools["pool-a"]["keys"] == []
    assert pools["pool-b"]["keys"] == []
    assert pools["new-pool"] == {"keys": ["main"], "models": []}


def test_valid_pool_memberships_do_not_prompt_or_rewrite(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    original = {
        "config_version": 3,
        "providers": {
            "vendor": {
                "base_url": "https://vendor.test",
                "keys": {"main": {"api_key": "sk-main"}},
                "pools": {"only": {"keys": ["main"], "models": []}},
            }
        },
        "models": {},
    }
    config_path.write_text(json.dumps(original), encoding="utf-8")
    monkeypatch.setattr(
        config_editor,
        "select_or_enter_pool_name",
        lambda *args, **kwargs: pytest.fail("valid config must not prompt"),
    )

    repaired = config_editor.repair_duplicate_pool_memberships_interactively(config_path)

    assert repaired is False
    assert json.loads(config_path.read_text(encoding="utf-8")) == original


def test_unassigned_key_is_repaired_interactively(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {},
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "select_or_enter_pool_name", lambda *args, **kwargs: "new-pool")

    assert config_editor.repair_duplicate_pool_memberships_interactively(config_path) is True
    pools = json.loads(config_path.read_text(encoding="utf-8"))["providers"]["vendor"]["pools"]

    assert pools == {"new-pool": {"keys": ["main"], "models": []}}


def test_legacy_list_pool_memberships_are_repaired_interactively(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"legacy": ["main"], "other": {"keys": ["main"], "models": []}},
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "select_or_enter_pool_name", lambda *args, **kwargs: "legacy")

    assert config_editor.repair_duplicate_pool_memberships_interactively(config_path) is True
    pools = json.loads(config_path.read_text(encoding="utf-8"))["providers"]["vendor"]["pools"]

    assert pools["legacy"] == {"keys": ["main"], "models": []}
    assert pools["other"]["keys"] == []


def test_nonempty_pool_cannot_be_deleted_until_keys_are_moved(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"only": {"keys": ["main"], "models": []}},
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: "1")

    result = config_editor.update_provider_pool_interactively(config_path, "vendor", "4")

    assert "请先将 Key 移入其他模型池" in render_plain(result)
    assert "only" in json.loads(config_path.read_text(encoding="utf-8"))["providers"]["vendor"]["pools"]


def test_add_model_route_selects_existing_local_model(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "openai": {
                        "base_url": "https://api.openai.com",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"default": {"keys": ["main"], "models": ["gpt-4o-mini"]}},
                    }
                },
                "models": {
                    "local-existing": {
                        "targets": [
                            {"provider": "openai", "pool": "default", "upstream_model": "old-model"}
                        ]
                    }
                },
            }
        ),
        encoding="utf-8",
    )
    choices = iter(["1", "1", "1", "local-existing"])
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: next(choices))
    monkeypatch.setattr(
        config_editor,
        "prompt_text",
        lambda *args, **kwargs: (_ for _ in ()).throw(AssertionError("existing model should be selected")),
    )
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))

    result = config_editor.add_model_route_interactively(config_path)
    data = json.loads(config_path.read_text(encoding="utf-8"))

    assert "添加完成" in render_plain(result)
    assert data["models"]["local-existing"]["targets"][-1] == {
        "provider": "openai",
        "pool": "default",
        "upstream_model": "gpt-4o-mini",
    }


def test_update_model_target_selects_pool_model(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "openai": {
                        "base_url": "https://api.openai.com",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"default": {"keys": ["main"], "models": ["gpt-4o-mini", "gpt-4o"]}},
                    }
                },
                "models": {
                    "local": {
                        "targets": [
                            {"provider": "openai", "pool": "default", "upstream_model": "gpt-4o-mini"}
                        ]
                    }
                },
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: "gpt-4o")
    monkeypatch.setattr(
        config_editor,
        "prompt_text",
        lambda *args, **kwargs: (_ for _ in ()).throw(AssertionError("upstream model should be selected")),
    )
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))

    result = config_editor.update_model_target_interactively(config_path, "local", 0, "1")
    data = json.loads(config_path.read_text(encoding="utf-8"))

    assert "gpt-4o-mini → gpt-4o" in render_plain(result)
    assert data["models"]["local"]["targets"][0]["upstream_model"] == "gpt-4o"


def test_add_provider_key_keeps_in_memory_draft_on_cancel(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps({"config_version": 3, "local_api_key": "local-key", "providers": {}, "models": {}}),
        encoding="utf-8",
    )
    config_editor.FORM_DRAFTS.clear()
    prompts = iter(["openai", "https://api.openai.com"])

    def ask(*args, **kwargs):
        try:
            return next(prompts)
        except StopIteration:
            raise KeyboardInterrupt

    monkeypatch.setattr(config_editor, "prompt_text", ask)
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: "n")

    cancelled = False
    try:
        config_editor.add_provider_key_interactively(config_path)
    except KeyboardInterrupt:
        cancelled = True

    assert cancelled
    assert config_editor.FORM_DRAFTS["add_provider_key"]["provider_id"] == "openai"
    assert config_editor.FORM_DRAFTS["add_provider_key"]["base_url"] == "https://api.openai.com"


def test_deleting_last_provider_key_removes_empty_pool_and_model(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "local_api_key": "local-key",
                "providers": {
                    "gateway": {
                        "base_url": "https://gateway.example.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"default": {"keys": ["main"], "models": ["gpt-a"]}},
                    }
                },
                "models": {
                    "local-model": {
                        "targets": [
                            {"provider": "gateway", "pool": "default", "upstream_model": "gpt-a"}
                        ]
                    }
                },
                "unified_model": {"model": "local-model"},
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "confirm_choice", lambda *args, **kwargs: True)
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))

    result = config_editor.update_provider_key_interactively(config_path, "gateway", "main", "5")
    data = json.loads(config_path.read_text(encoding="utf-8"))

    assert "已删除 gateway/main" in render_plain(result)
    assert data["providers"] == {}
    assert data["models"] == {}
    assert "unified_model" not in data


def test_deleting_unified_model_key_switches_to_available_model(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "local_api_key": "local-key",
                "providers": {
                    "gateway": {
                        "base_url": "https://gateway.example.test",
                        "keys": {
                            "main": {"api_key": "sk-main"},
                            "backup": {"api_key": "sk-backup"},
                        },
                        "pools": {
                            "main-pool": {"keys": ["main"], "models": ["gpt-a"]},
                            "backup-pool": {"keys": ["backup"], "models": ["gpt-b"]},
                        },
                    }
                },
                "models": {
                    "local-model": {
                        "targets": [
                            {"provider": "gateway", "pool": "main-pool", "upstream_model": "gpt-a"}
                        ]
                    },
                    "backup-model": {
                        "targets": [
                            {"provider": "gateway", "pool": "backup-pool", "upstream_model": "gpt-b"}
                        ]
                    },
                },
                "unified_model": {"model": "local-model", "key": "main"},
            }
        ),
        encoding="utf-8",
    )
    monkeypatch.setattr(config_editor, "confirm_choice", lambda *args, **kwargs: True)
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))

    config_editor.update_provider_key_interactively(config_path, "gateway", "main", "5")
    data = json.loads(config_path.read_text(encoding="utf-8"))

    assert "local-model" not in data["models"]
    assert data["unified_model"] == {"model": "backup-model"}


def test_v2_tui_adds_provider_pool(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "gateway": {
                        "base_url": "https://gateway.example.test",
                        "keys": {
                            "cheap-1": {"api_key": "sk-1"},
                            "cheap-2": {"api_key": "sk-2"},
                            "pro-1": {"api_key": "sk-3"},
                        },
                        "pools": {
                            "unassigned": {
                                "keys": ["cheap-1", "cheap-2", "pro-1"],
                                "models": [],
                            }
                        },
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    choices = iter(["1"])
    monkeypatch.setattr(config_editor, "select_option", lambda *args, **kwargs: next(choices))
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: "cheap")
    monkeypatch.setattr(config_editor, "select_or_enter_pool_name", lambda *args, **kwargs: "cheap")
    selections = iter([["cheap-1", "cheap-2"], ["gpt-a"]])
    monkeypatch.setattr(config_editor, "select_multiple", lambda *args, **kwargs: next(selections))
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))
    monkeypatch.setattr(config_editor, "apply_pool_probe", lambda provider, pool_name, **kwargs: provider["pools"][pool_name].update({"available_models": ["gpt-a"], "all_available_models": ["gpt-a", "gpt-b"], "routes": {}}) or {"models": ["gpt-a"], "all_models": ["gpt-a", "gpt-b"], "routes": {}})

    result = config_editor.update_provider_pool_interactively(config_path, "gateway", "1")
    data = json.loads(config_path.read_text(encoding="utf-8"))

    assert "模型池" in render_plain(result)
    assert data["providers"]["gateway"]["pools"]["cheap"] == {
        "keys": ["cheap-1", "cheap-2"],
        "models": ["gpt-a"],
        "available_models": ["gpt-a"],
        "all_available_models": ["gpt-a", "gpt-b"],
        "routes": {},
    }


def test_pool_probe_models_records_common_models_and_routes(monkeypatch) -> None:
    def fake_discover(base_url, api_key, existing_model_ids, timeout=15.0):
        return {
            "sk-1": ["gpt-a", "gpt-b"],
            "sk-2": ["gpt-a", "gpt-c"],
        }[api_key]

    def fake_probe(data, model_id, key, timeout=15.0):
        return [
            config_editor.KeyProbeResult(
                model_id=model_id,
                key_name=str(key["name"]),
                mode="openai",
                label="OpenAI Chat",
                path="v1/chat/completions",
                url="https://gateway.example.test/v1/chat/completions",
                available=True,
                status_code=200,
                duration_ms=1,
                error="",
            )
        ]

    monkeypatch.setattr(config_editor, "discover_upstream_models", fake_discover)
    monkeypatch.setattr(config_editor, "probe_key_availability", fake_probe)

    result = config_editor.pool_probe_models(
        {
            "base_url": "https://gateway.example.test",
            "keys": {"k1": {"api_key": "sk-1"}, "k2": {"api_key": "sk-2"}},
        },
        ["k1", "k2"],
    )

    assert result["models"] == ["gpt-a"]
    assert result["all_models"] == ["gpt-a", "gpt-b", "gpt-c"]
    assert result["key_models"] == {"k1": ["gpt-a", "gpt-b"], "k2": ["gpt-a", "gpt-c"]}
    assert result["routes"] == {"k1": {"openai": True}, "k2": {"openai": True}}
    assert result["checked_at"]


def test_apply_pool_probe_preserves_enabled_models(monkeypatch) -> None:
    monkeypatch.setattr(
        config_editor,
        "pool_probe_models",
        lambda *args, **kwargs: {
            "models": ["gpt-a", "gpt-b"],
            "all_models": ["gpt-a", "gpt-b"],
            "key_models": {"k1": ["gpt-a", "gpt-b"]},
            "routes": {},
            "manual_models": False,
            "checked_at": "2026-01-01T00:00:00+00:00",
        },
    )
    provider = {
        "base_url": "https://gateway.example.test",
        "keys": {"k1": {"api_key": "sk-1"}},
        "pools": {"default": {"keys": ["k1"], "models": ["gpt-old"]}},
    }

    config_editor.apply_pool_probe(provider, "default")

    assert provider["pools"]["default"]["available_models"] == ["gpt-a", "gpt-b"]
    assert provider["pools"]["default"]["models"] == ["gpt-old"]


def test_pool_model_selector_shows_all_models_and_current_checked_state(monkeypatch) -> None:
    pool = {
        "keys": ["k1"],
        "models": ["gpt-a", "gpt-old"],
        "available_models": ["gpt-a"],
        "all_available_models": ["gpt-a", "gpt-new"],
    }
    calls: list[tuple[list[tuple[str, str]], set[str]]] = []

    def select(title, options, content=None, *, checked_values=None):
        calls.append((options, checked_values))
        return ["gpt-a", "gpt-old"]

    monkeypatch.setattr(config_editor, "select_multiple", select)

    selected = config_editor.prompt_pool_enabled_models(pool)

    assert selected == ["gpt-a", "gpt-old"]
    assert calls[0][1] == {"gpt-a", "gpt-old"}
    assert [value for value, _ in calls[0][0]] == [
        "gpt-a",
        "gpt-new",
        "gpt-old",
        "__custom__",
    ]
    labels = dict(calls[0][0])
    assert "[yellow]" not in labels["gpt-a"]
    assert "[yellow]" in labels["gpt-new"]
    assert "[yellow]" in labels["gpt-old"]


def test_enabled_pool_models_are_usable_by_unified_model(monkeypatch) -> None:
    monkeypatch.setattr(
        config_editor,
        "pool_probe_models",
        lambda *args, **kwargs: {
            "models": ["gpt-a"],
            "all_models": ["gpt-a", "gpt-b"],
            "key_models": {"k1": ["gpt-a", "gpt-b"]},
            "routes": {},
            "manual_models": False,
            "checked_at": "2026-01-01T00:00:00+00:00",
        },
    )
    data = {
        "config_version": 3,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"k1": {"api_key": "sk-1"}},
                "pools": {"default": {"keys": ["k1"]}},
            }
        },
        "models": {},
    }

    config_editor.apply_pool_probe(data["providers"]["gateway"], "default")
    config_editor.enable_pool_models(data, "gateway", "default", ["gpt-a"])
    data["unified_model"] = {"model": "gpt-a"}

    config = config_editor.RouterConfig.from_dict(data)

    assert config.configured_model_id("gpt-a") == "gpt-a"
    assert config.unified_model is not None
    assert config.models[0].keys[0].upstream_model == "gpt-a"


def test_pool_probe_models_accepts_manual_models_when_discovery_empty(monkeypatch) -> None:
    monkeypatch.setattr(config_editor, "discover_upstream_models", lambda *args, **kwargs: [])
    monkeypatch.setattr(config_editor, "probe_key_availability", lambda *args, **kwargs: [])

    result = config_editor.pool_probe_models(
        {
            "base_url": "https://gateway.example.test",
            "keys": {"k1": {"api_key": "sk-1"}, "k2": {"api_key": "sk-2"}},
        },
        ["k1", "k2"],
        manual_models=["gpt-manual"],
    )

    assert result["models"] == ["gpt-manual"]
    assert result["all_models"] == ["gpt-manual"]
    assert result["key_models"] == {"k1": ["gpt-manual"], "k2": ["gpt-manual"]}
    assert result["manual_models"] is True


def test_pool_update_prompts_manual_models_when_discovery_empty(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {
                    "gateway": {
                        "base_url": "https://gateway.example.test",
                        "keys": {"k1": {"api_key": "sk-1"}},
                        "pools": {"unassigned": {"keys": ["k1"], "models": []}},
                    }
                },
                "models": {},
            }
        ),
        encoding="utf-8",
    )
    prompts = iter(["gpt-manual,gpt-extra"])
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: next(prompts))
    monkeypatch.setattr(config_editor, "select_or_enter_pool_name", lambda *args, **kwargs: "manual-pool")
    monkeypatch.setattr(config_editor, "select_multiple", lambda *args, **kwargs: ["k1"])
    monkeypatch.setattr(config_editor, "apply_pool_probe", lambda provider, pool_name, **kwargs: {"models": [], "all_models": [], "routes": {}} if "manual_models" not in kwargs else provider["pools"][pool_name].update({"models": kwargs["manual_models"], "all_models": kwargs["manual_models"], "key_models": {"k1": kwargs["manual_models"]}, "routes": {}, "manual_models": True}) or {"models": kwargs["manual_models"], "all_models": kwargs["manual_models"], "routes": {}})
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("restart"))

    result = config_editor.update_provider_pool_interactively(config_path, "gateway", "1")
    data = json.loads(config_path.read_text(encoding="utf-8"))

    assert "已启用模型" in render_plain(result)
    assert data["providers"]["gateway"]["pools"]["manual-pool"]["models"] == [
        "gpt-manual",
        "gpt-extra",
    ]


def test_upstream_routes_panel_shows_three_mode_support(monkeypatch) -> None:
    data = {
        "upstream_routes": {
            "https://upstream.example.com": {
                "anthropic": "anthropic/v1/messages",
            }
        },
        "models": [
            {
                "id": "test-model",
                "keys": [
                    {
                        "name": "main",
                        "api_key": "sk-main",
                        "base_url": "https://upstream.example.com",
                    }
                ],
            }
        ]
    }
    model = data["models"][0]
    monkeypatch.setattr(
        config_editor,
        "fetch_native_endpoint_states",
        lambda data: {
            "https://upstream.example.com|v1/responses": {
                "supported": False,
                "reason": "unsupported",
                "expires_in_seconds": 42,
            }
        },
    )

    output = render_plain(config_editor.upstream_routes_panel(data, model, 0))

    assert "OpenAI Chat" in output
    assert "Anthropic Messages" in output
    assert "OpenAI Responses" in output
    assert "anthropic/v1/messages" in output
    assert "探测: 回退缓存" in output
    assert "42s 后重试" in output


def test_model_aliases_panel_lists_current_aliases() -> None:
    panel = config_editor.model_aliases_panel({"id": "model-one", "aliases": ["first", "second"]})

    text = render_plain(panel)
    assert "model-one" in text
    assert "1. first" in text
    assert "2. second" in text


def test_model_alias_crud_updates_config_and_preserves_unified_model(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "local_api_key": "local-key",
                "providers": {
                    "gateway": {
                        "base_url": "https://upstream.example.com",
                        "keys": {"main": {"api_key": "sk-main"}},
                        "pools": {"main": {"keys": ["main"]}},
                    }
                },
                "models": {
                    "model-one": {
                        "aliases": ["old-alias"],
                        "targets": [{"provider": "gateway", "pool": "main", "upstream_model": "model-one"}],
                    }
                },
                "unified_model": {"model": "old-alias"},
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    answers = iter(["new-alias", "renamed-alias"])
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: next(answers))
    monkeypatch.setattr(config_editor, "confirm_choice", lambda *args, **kwargs: True)
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda path, old_config, new_config: Text("reloaded"))

    data = config_editor.load_config_data(config_path)
    model = data["models"]["model-one"]
    model["id"] = "model-one"
    config_editor.add_model_alias_interactively(config_path, data, model)

    data = config_editor.load_config_data(config_path)
    model = data["models"]["model-one"]
    model["id"] = "model-one"
    config_editor.edit_model_alias_interactively(config_path, data, model, 0)

    data = config_editor.load_config_data(config_path)
    model = data["models"]["model-one"]
    model["id"] = "model-one"
    config_editor.delete_model_alias_interactively(config_path, data, model, 1)

    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert saved["models"]["model-one"]["aliases"] == ["renamed-alias"]
    assert saved["unified_model"] == {"model": "model-one"}


def test_add_model_alias_rejects_name_collision_without_saving(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    original = v3_config(
        {
            "model-one": [{"name": "one", "api_key": "sk-one", "base_url": "https://one.example.com"}],
            "model-two": [{"name": "two", "api_key": "sk-two", "base_url": "https://two.example.com"}],
        },
        local_api_key="local-key",
    )
    config_path.write_text(json.dumps(original, ensure_ascii=False), encoding="utf-8")
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: "model-two")

    data = config_editor.load_config_data(config_path)
    result = config_editor.add_model_alias_interactively(config_path, data, data["models"]["model-one"])

    assert json.loads(config_path.read_text(encoding="utf-8")) == original
    assert "模型名称重复: model-two" in render_plain(result)


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


def test_systemd_service_registration_prefers_console_script(tmp_path, monkeypatch) -> None:
    commands: list[list[str]] = []
    monkeypatch.setattr(service.Path, "home", lambda: tmp_path)
    monkeypatch.setattr(service.Path, "cwd", lambda: PurePosixPath("/root"))
    monkeypatch.setattr(service, "console_script_executable", lambda: PurePosixPath("/opt/pipx/bin/auto-model-key-router"))
    monkeypatch.setattr(service, "registration_result", lambda command, title: commands.append(command) or tui.section_panel("ok", title, "green"))
    monkeypatch.setattr(service.os, "getlogin", lambda: "root")

    service.manage_systemd_user_service(PurePosixPath("/usr/bin/python3.12"), PurePosixPath("/root/.cache/auto-model-key-router/router-config.json"), "install")

    unit = (tmp_path / ".config" / "systemd" / "user" / "auto-model-key-router.service").read_text(encoding="utf-8")
    assert "ExecStart=/opt/pipx/bin/auto-model-key-router --config /root/.cache/auto-model-key-router/router-config.json --serve-foreground" in unit
    assert "python3.12 -m auto_model_key_router.main" not in unit
    assert commands[0] == ["systemctl", "--user", "daemon-reload"]
    assert commands[1] == ["systemctl", "--user", "enable", "--now", "auto-model-key-router.service"]


def test_systemd_service_registration_quotes_console_script_paths(tmp_path, monkeypatch) -> None:
    monkeypatch.setattr(service.Path, "home", lambda: tmp_path)
    monkeypatch.setattr(service.Path, "cwd", lambda: PurePosixPath("/root/app dir"))
    monkeypatch.setattr(service, "console_script_executable", lambda: PurePosixPath("/opt/amkr tools/auto-model-key-router"))
    monkeypatch.setattr(service, "registration_result", lambda command, title: tui.section_panel("ok", title, "green"))
    monkeypatch.setattr(service.os, "getlogin", lambda: "root")

    service.manage_systemd_user_service(PurePosixPath("/usr/bin/python3.12"), PurePosixPath("/root/config dir/router-config.json"), "install")

    unit = (tmp_path / ".config" / "systemd" / "user" / "auto-model-key-router.service").read_text(encoding="utf-8")
    assert "ExecStart='/opt/amkr tools/auto-model-key-router' --config '/root/config dir/router-config.json' --serve-foreground" in unit


def test_toggle_visitor_access_interactively_updates_selected_key(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    data = v3_config(
        {"test-model": [{"name": "shared", "api_key": "sk-shared", "base_url": "https://example.test"}]},
        local_api_key="local-key",
    )
    config_path.write_text(json.dumps(data, ensure_ascii=False), encoding="utf-8")
    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: True)
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda path, old, new: Text("reloaded"))

    result = config_editor.toggle_visitor_access_interactively(
        config_path,
        data,
        {"id": "test-model", "keys": [{"name": "shared", "api_key": "sk-shared", "base_url": "https://example.test"}]},
        0,
    )

    assert "允许" in render_plain(result)


def test_toggle_visitor_access_requires_optional_feature(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    data = {
        "local_api_key": "local-key",
        "models": [
            {
                "id": "test-model",
                "keys": [{"name": "shared", "api_key": "sk-shared", "base_url": "https://example.test"}],
            }
        ],
    }
    config_path.write_text(json.dumps(data, ensure_ascii=False), encoding="utf-8")
    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: False)

    result = config_editor.toggle_visitor_access_interactively(
        config_path,
        data,
        data["models"][0],
        0,
    )

    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert "allow_visitor" not in saved["models"][0]["keys"][0]
    assert "未安装" in render_plain(result)


def test_discover_upstream_models_success(monkeypatch) -> None:
    class FakeResponse:
        status_code = 200

        def raise_for_status(self):
            pass

        def json(self):
            return {
                "object": "list",
                "data": [
                    {"id": "gpt-4o", "object": "model"},
                    {"id": "gpt-3.5-turbo", "object": "model"},
                    {"id": "existing-model", "object": "model"},
                ],
            }

    class FakeClient:
        def __init__(self, **kwargs):
            pass

        def get(self, url, headers=None):
            return FakeResponse()

        def __enter__(self):
            return self

        def __exit__(self, *args):
            pass

    monkeypatch.setattr(config_editor.httpx, "Client", FakeClient)

    result = config_editor.discover_upstream_models(
        "https://api.example.com", "sk-test", {"existing-model"}
    )

    assert result == ["existing-model", "gpt-3.5-turbo", "gpt-4o"]


def test_discover_upstream_models_failure(monkeypatch) -> None:
    class FailingClient:
        def __init__(self, **kwargs):
            pass

        def get(self, url, headers=None):
            raise config_editor.httpx.ConnectError("connection refused")

        def __enter__(self):
            return self

        def __exit__(self, *args):
            pass

    monkeypatch.setattr(config_editor.httpx, "Client", FailingClient)

    result = config_editor.discover_upstream_models(
        "https://api.example.com", "sk-test", set()
    )

    assert result == []


def test_probe_key_availability_posts_all_text_routes(monkeypatch) -> None:
    requests = []

    class FakeResponse:
        status_code = 200

        @property
        def text(self):
            return "{}"

    class FakeClient:
        def __init__(self, **kwargs):
            pass

        def post(self, url, headers=None, json=None):
            requests.append((url, headers, json))
            return FakeResponse()

        def __enter__(self):
            return self

        def __exit__(self, *args):
            pass

    monkeypatch.setattr(config_editor.httpx, "Client", FakeClient)

    results = config_editor.probe_key_availability(
        {
            "upstream_routes": {
                "https://api.example.com": {"responses": "custom/responses"}
            }
        },
        "model-a",
        {"name": "main", "api_key": "sk-main", "base_url": "https://api.example.com"},
    )

    assert [result.mode for result in results] == ["openai", "anthropic", "responses"]
    assert [url for url, _, _ in requests] == [
        "https://api.example.com/v1/chat/completions",
        "https://api.example.com/v1/messages",
        "https://api.example.com/custom/responses",
    ]
    assert all(headers == {"Authorization": "Bearer sk-main"} for _, headers, _ in requests)
    assert requests[0][2]["messages"] == [{"role": "user", "content": "."}]
    assert requests[0][2]["max_tokens"] == 1
    assert requests[1][2]["max_tokens"] == 1
    assert requests[2][2]["input"] == "."
    assert requests[2][2]["max_output_tokens"] == 1
    assert all(result.available for result in results)


def test_probe_all_key_availability_checks_every_configured_key(monkeypatch) -> None:
    calls = []

    def fake_probe(data, model_id, key, timeout=15.0):
        calls.append((model_id, key["name"]))
        return [
            config_editor.KeyProbeResult(
                model_id=model_id,
                key_name=key["name"],
                mode="openai",
                label="OpenAI Chat",
                path="v1/chat/completions",
                url="https://api.example.com/v1/chat/completions",
                available=True,
                status_code=200,
                duration_ms=1,
                error="",
            )
        ]

    monkeypatch.setattr(config_editor, "probe_key_availability", fake_probe)

    results = config_editor.probe_all_key_availability(
        {
            "models": [
                {"id": "model-a", "keys": [{"name": "a1"}, {"name": "a2"}]},
                {"id": "model-b", "keys": [{"name": "b1", "enabled": False}]},
            ]
        }
    )

    assert calls == [("model-a", "a1"), ("model-a", "a2"), ("model-b", "b1")]
    assert [result.key_name for result in results] == ["a1", "a2", "b1"]


def test_manage_key_menu_includes_all_probe(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "models": [
                    {
                        "id": "model-a",
                        "keys": [
                            {"name": "main", "api_key": "sk-main", "base_url": "https://api.example.com"}
                        ],
                    }
                ]
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    choices = iter(["2", "0"])
    menus = []
    shown = []

    def choose(title, options, selected=0, content=None, *, on_key=None):
        menus.append((title, options))
        return next(choices)

    monkeypatch.setattr(config_editor, "select_option", choose)
    monkeypatch.setattr(config_editor, "visitor_feature_available", lambda: False)
    monkeypatch.setattr(config_editor, "clear_terminal_history", lambda: None)
    monkeypatch.setattr(config_editor, "probe_all_keys_interactively", lambda path: Text("all probe"))
    monkeypatch.setattr(config_editor, "show_result_page", lambda title, content: shown.append((title, render_plain(content))))

    config_editor.manage_selected_key_interactively(config_path)

    assert menus[0] == (
        "管理 Key",
        [("1", "管理单个 Key"), ("2", "探测所有 Key"), ("0", "返回")],
    )
    assert shown == [("探测所有 Key", "all probe\n")]


def test_select_api_key_shows_config_enabled_state(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "models": [
                    {
                        "id": "model-a",
                        "keys": [
                            {"name": "main", "api_key": "sk-main", "base_url": "https://api.example.com"}
                        ],
                    }
                ],
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    choices = iter(["1", "0"])
    menus = []

    def choose(title, options, selected=0, content=None, *, on_key=None):
        menus.append((title, options))
        return next(choices)

    monkeypatch.setattr(config_editor, "select_option", choose)
    assert config_editor.select_api_key(config_path, "选择 Key") is None

    assert any("启用" in option for _, options in menus for _, option in options)
    assert all("冷却" not in option for _, options in menus for _, option in options)


def test_add_config_with_discovery_adds_selected_models(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "config_version": 3,
                "providers": {},
                "models": {},
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    answers = iter(["https://api.example.com", "sk-test-key", "gpt-4o", "", "round_robin", "downstream", "gpt-4o-custom-key"])
    choices = iter(["n", "n"])

    def ask(title: str, prompt: str, **kwargs):
        return next(answers)

    def choose(title, options, selected=0, content=None):
        return next(choices)

    monkeypatch.setattr(config_editor, "prompt_text", ask)
    monkeypatch.setattr(config_editor, "select_option", choose)
    monkeypatch.setattr(config_editor, "confirm_choice", lambda *args, **kwargs: False)
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda path, old_config, new_config: Text("reloaded"))
    monkeypatch.setattr(config_editor, "discover_upstream_models", lambda base_url, api_key, existing_ids, timeout=15.0: ["gpt-4o", "gpt-4o-mini"])
    monkeypatch.setattr(config_editor, "select_multiple", lambda title, options, content=None: ["gpt-4o-mini"])

    config_editor.add_config_interactively(config_path, ask_continue=False)

    data = json.loads(config_path.read_text(encoding="utf-8"))
    assert "gpt-4o" in data["models"]
    assert "gpt-4o-mini" in data["models"]

    gpt4o_model = data["models"]["gpt-4o"]
    assert gpt4o_model["routing_mode"] == "round_robin"
    assert data["providers"]["default"]["keys"]["gpt-4o-custom-key"]["api_key"] == "sk-test-key"
    assert data["providers"]["default"]["base_url"] == "https://api.example.com"

    gpt4o_mini_model = data["models"]["gpt-4o-mini"]
    assert gpt4o_mini_model["routing_mode"] == "round_robin"
    assert data["providers"]["default"]["keys"]["gpt-4o-mini-key-1"]["api_key"] == "sk-test-key"

