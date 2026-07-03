from __future__ import annotations

from collections.abc import Callable, Iterator
from contextlib import contextmanager
from dataclasses import dataclass
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

if sys.platform == "win32":
    import msvcrt
else:
    import select
    import termios
    import tty

from rich import box
from rich.align import Align
from rich.console import Console, Group
from rich.live import Live
from rich.panel import Panel
from rich.segment import Segment, Segments
from rich.style import Style
from rich.table import Table
from rich.text import Text

from .clipboard import copy_to_clipboard


console = Console()

APP_ASCII_FLAG = (
    "   ___    __  ___  __ __  ___ ",
    "  / _ |  /  |/  / / //_/ / _ \\",
    " / __ | / /|_/ / / ,<   / , _/",
    "/_/ |_|/_/  /_/ /_/|_| /_/|_| ",
)
APP_ASCII_FLAG_STYLES = ("bold bright_cyan", "bold cyan", "bold bright_blue", "bold bright_magenta")
MOUSE_MODE_ENABLE = "\033[?1000h\033[?1006h"
MOUSE_MODE_DISABLE = "\033[?1000l\033[?1006l"
WHEEL_EVENT_INTERVAL_SECONDS = 0.16
ESC_SEQUENCE_TIMEOUT_SECONDS = 0.2
WHEEL_CONTENT_STEP = 1
WHEEL_KEYS = {"scroll_up", "scroll_down"}
MIN_RENDER_WIDTH = 4
FOLDED_CONTENT_MARKER = "… 上方内容已折叠"
WINDOW_TITLE = " Auto Model Key Router "
WINDOW_BORDER_STYLE = "bright_magenta"
SELECTED_ROW_MARKER = "▶"


@dataclass(frozen=True)
class ResultPage:
    content: Any
    copy_text: str | None = None
    copy_label: str = "复制 key"


@dataclass(frozen=True)
class TerminalFrameState:
    renderable: Any
    offset: int
    max_offset: int
    viewport_height: int


def app_flag_title(title: str, subtitle: str, version: str) -> Panel:
    text = Text()
    detail_lines = ((title, "bold bright_cyan"), (subtitle, "dim"), (f"v{version}", "dim"), ("", "dim"))
    for index, line in enumerate(APP_ASCII_FLAG):
        if index:
            text.append("\n")
        text.append(line, style=APP_ASCII_FLAG_STYLES[index])
        text.append("   ")
        detail, style = detail_lines[index]
        text.append(detail, style=style)
    return Panel(Align.center(text), border_style="bright_magenta", box=box.ROUNDED)


def page_title(title: str, subtitle: str | None = None) -> Panel:
    text = f"[bold cyan]{title}[/bold cyan]"
    if subtitle:
        text = f"{text}\n[dim]{subtitle}[/dim]"
    return Panel(Align.center(text), border_style="cyan", box=box.ROUNDED)


def menu_table(options: list[tuple[str, str]], selected: int) -> Table:
    table = Table(show_header=False, box=None, padding=(0, 1), expand=True)
    table.add_column("指示", justify="center", width=3)
    table.add_column("编号", justify="center", width=5)
    table.add_column("操作", ratio=1)
    for index, (value, label) in enumerate(options):
        if index == selected:
            table.add_row("[bold cyan]▶[/bold cyan]", f"[bold black on cyan] {value} [/bold black on cyan]", f"[bold cyan]{label}[/bold cyan]")
        else:
            table.add_row("", f"[dim]{value}[/dim]", label)
    return table


def section_panel(content: Any, title: str, border_style: str = "cyan", subtitle: str | None = None) -> Panel:
    return Panel(content, title=f"[bold]{title}[/bold]", subtitle=subtitle, border_style=border_style, box=box.ROUNDED)


def shortcut_text(text: str) -> Align:
    return Align.center(Text(text, style="dim", no_wrap=True, overflow="ellipsis"))


def content_viewport_height(option_count: int) -> int:
    return max(1, console.size.height - option_count - 8)


def renderable_line_segments(content: Any, width: int) -> list[list[Segment]]:
    return console.render_lines(content, console.options.update(width=max(width, MIN_RENDER_WIDTH)), pad=False)


def segment_lines_renderable(lines: list[list[Segment]]) -> Segments:
    segments: list[Segment] = []
    for index, line in enumerate(lines or [[Segment("")]]):
        if index:
            segments.append(Segment.line())
        segments.extend(line)
    return Segments(segments)


def folded_marker_line() -> list[Segment]:
    return [Segment(FOLDED_CONTENT_MARKER, Style.parse("dim"))]


def fit_terminal_lines(lines: list[list[Segment]], height: int, preserve_bottom: bool = True) -> list[list[Segment]]:
    if height <= 0:
        return []
    if len(lines) > height:
        if not preserve_bottom:
            return lines[:height]
        if height == 1:
            return [folded_marker_line()]
        return [folded_marker_line(), *lines[-(height - 1):]]
    return [*lines, *([Segment("")] for _ in range(height - len(lines)))]


def terminal_frame_state(
    renderables: list[Any],
    footer: Any | None = None,
    *,
    offset: int = 0,
    focus_text: str | None = None,
    preserve_bottom: bool = False,
    frame_title: str = WINDOW_TITLE,
) -> TerminalFrameState:
    width = max(console.size.width, MIN_RENDER_WIDTH)
    height = max(console.size.height, 1)
    if height < 3:
        lines = renderable_line_segments(Group(*renderables), width) if renderables else []
        visible = fit_terminal_lines(lines, height, preserve_bottom)
        return TerminalFrameState(segment_lines_renderable(visible), 0, max(len(lines) - height, 0), height)

    content_width = max(width - 4, 1)
    inner_height = height - 2
    footer_lines = renderable_line_segments(footer, content_width) if footer is not None else []
    if len(footer_lines) >= inner_height:
        visible_footer = footer_lines[-inner_height:]
        panel = Panel(
            segment_lines_renderable(visible_footer),
            title=f"[bold bright_cyan]{frame_title}[/bold bright_cyan]",
            border_style=WINDOW_BORDER_STYLE,
            box=box.ROUNDED,
            padding=(0, 1),
            width=width,
            height=height,
        )
        return TerminalFrameState(panel, 0, 0, 0)

    separator_height = 1 if footer_lines else 0
    viewport_height = max(inner_height - len(footer_lines) - separator_height, 0)
    body_lines = renderable_line_segments(Group(*renderables), content_width) if renderables else []
    max_offset = max(len(body_lines) - viewport_height, 0)
    offset = max_offset if preserve_bottom else min(max(offset, 0), max_offset)

    if focus_text and viewport_height:
        focus_line = next(
            (index for index, line in enumerate(body_lines) if focus_text in "".join(segment.text for segment in line)),
            None,
        )
        if focus_line is not None:
            if focus_line < offset:
                offset = focus_line
            elif focus_line >= offset + viewport_height:
                offset = min(focus_line - viewport_height + 1, max_offset)

    end = min(offset + viewport_height, len(body_lines))
    visible_body = fit_terminal_lines(body_lines[offset:end], viewport_height, preserve_bottom=False)
    window_lines = list(visible_body)
    if footer_lines:
        window_lines.append([Segment("─" * content_width, Style.parse("dim cyan"))])
        window_lines.extend(footer_lines)
    subtitle = None
    if max_offset:
        subtitle = f"[dim] 第 {offset + 1}-{end} 行 / 共 {len(body_lines)} 行 · PgUp/PgDn 滚动 [/dim]"
    panel = Panel(
        segment_lines_renderable(window_lines),
        title=f"[bold bright_cyan]{frame_title}[/bold bright_cyan]",
        subtitle=subtitle,
        border_style=WINDOW_BORDER_STYLE,
        box=box.ROUNDED,
        padding=(0, 1),
        width=width,
        height=height,
    )
    return TerminalFrameState(panel, offset, max_offset, viewport_height)


def terminal_frame(
    renderables: list[Any],
    footer: Any | None = None,
    preserve_bottom: bool = False,
    *,
    offset: int = 0,
    focus_text: str | None = None,
    frame_title: str = WINDOW_TITLE,
) -> Any:
    return terminal_frame_state(
        renderables,
        footer,
        offset=offset,
        focus_text=focus_text,
        preserve_bottom=preserve_bottom,
        frame_title=frame_title,
    ).renderable


def scrollable_content_state(content: Any, offset: int, option_count: int) -> tuple[Any, int, int, int]:
    viewport_height = content_viewport_height(option_count)
    lines = renderable_line_segments(content, console.size.width - 4)
    max_offset = max(len(lines) - viewport_height, 0)
    offset = min(max(offset, 0), max_offset)
    if max_offset == 0:
        return content, offset, max_offset, viewport_height
    end = min(offset + viewport_height, len(lines))
    viewport = segment_lines_renderable(lines[offset:end])
    title = f"内容 第 {offset + 1}-{end} 行 / 共 {len(lines)} 行"
    return section_panel(viewport, title, "blue", "[dim]滚轮或 PgUp/PgDn 翻阅[/dim]" if sys.platform == "win32" else "[dim]PgUp/PgDn 翻阅[/dim]"), offset, max_offset, viewport_height


def render_option_menu_state(
    title: str,
    options: list[tuple[str, str]],
    selected: int,
    content: Any | None = None,
    frame_offset: int = 0,
    *,
    ensure_selected_visible: bool = True,
) -> TerminalFrameState:
    renderables = [page_title(title)]
    if content is not None:
        renderables.append(content)
    renderables.append(section_panel(menu_table(options, selected), "操作菜单", "cyan", "[dim]选择下一步操作[/dim]"))
    shortcuts = "↑/↓ 选择  ·  Enter 确认  ·  PgUp/PgDn 翻阅窗体  ·  数字快捷键  ·  Ctrl+C 返回"
    if sys.platform == "win32" and content is not None:
        shortcuts = "↑/↓ 选择  ·  Enter 确认  ·  PgUp/PgDn/滚轮 翻阅窗体  ·  数字快捷键  ·  Ctrl+C 返回"
    return terminal_frame_state(
        renderables,
        shortcut_text(shortcuts),
        offset=frame_offset,
        focus_text=SELECTED_ROW_MARKER if ensure_selected_visible else None,
    )


def render_option_menu(title: str, options: list[tuple[str, str]], selected: int, content: Any | None = None, content_offset: int = 0) -> Any:
    return render_option_menu_state(title, options, selected, content, content_offset).renderable


def parse_sgr_mouse_sequence(sequence: str) -> str | None:
    if not sequence or sequence[-1] not in {"M", "m"}:
        return None
    try:
        button = int(sequence[:-1].split(";", 1)[0])
    except ValueError:
        return None
    if button < 64:
        return None
    return "scroll_down" if button & 1 else "scroll_up"


def should_handle_wheel(key: str | None, last_key: str | None, last_at: float) -> tuple[bool, str | None, float]:
    if key not in WHEEL_KEYS:
        return True, last_key, last_at
    now = time.monotonic()
    if key == last_key and now - last_at < WHEEL_EVENT_INTERVAL_SECONDS:
        return False, last_key, last_at
    return True, key, now


def content_scroll_offset(key: str, offset: int, max_offset: int, viewport_height: int) -> int:
    if key == "scroll_up":
        return max(0, offset - WHEEL_CONTENT_STEP)
    if key == "scroll_down":
        return min(max_offset, offset + WHEEL_CONTENT_STEP)
    if key == "page_up":
        return max(0, offset - viewport_height)
    if key == "page_down":
        return min(max_offset, offset + viewport_height)
    if key == "home":
        return 0
    if key == "end":
        return max_offset
    return offset


def read_windows_until(end_chars: set[str], limit: int = 64) -> str:
    chars = []
    while len(chars) < limit:
        char = msvcrt.getwch()
        chars.append(char)
        if char in end_chars:
            break
    return "".join(chars)


def read_windows_char_if_available(timeout: float = ESC_SEQUENCE_TIMEOUT_SECONDS) -> str | None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if msvcrt.kbhit():
            return msvcrt.getwch()
        time.sleep(0.005)
    if msvcrt.kbhit():
        return msvcrt.getwch()
    return None


def read_posix_until(end_chars: set[str], limit: int = 64) -> str:
    fd = sys.stdin.fileno()
    chars = []
    while len(chars) < limit and select.select([fd], [], [], ESC_SEQUENCE_TIMEOUT_SECONDS)[0]:
        ch = os.read(fd, 1)
        if not ch:
            break
        char = ch.decode("utf-8", errors="ignore")
        chars.append(char)
        if char in end_chars:
            break
    return "".join(chars)


def read_posix_char_if_available(timeout: float = ESC_SEQUENCE_TIMEOUT_SECONDS) -> str | None:
    fd = sys.stdin.fileno()
    if select.select([fd], [], [], timeout)[0]:
        ch = os.read(fd, 1)
        if not ch:
            return None
        return ch.decode("utf-8", errors="ignore")
    return None


def read_posix_chars_if_available(count: int) -> str:
    chars = []
    while len(chars) < count:
        char = read_posix_char_if_available()
        if char is None:
            break
        chars.append(char)
    return "".join(chars)


def enable_windows_virtual_terminal_input() -> Callable[[], None]:
    if sys.platform != "win32":
        return lambda: None
    try:
        import ctypes

        kernel32 = ctypes.windll.kernel32
        handle = kernel32.GetStdHandle(-10)
        mode = ctypes.c_uint()
        if not kernel32.GetConsoleMode(handle, ctypes.byref(mode)):
            return lambda: None
        original_mode = mode.value
        if not kernel32.SetConsoleMode(handle, original_mode | 0x0200):
            return lambda: None
    except (AttributeError, OSError, ValueError):
        return lambda: None

    def restore() -> None:
        try:
            kernel32.SetConsoleMode(handle, original_mode)
        except (OSError, ValueError):
            return

    return restore


@contextmanager
def mouse_wheel_mode(enabled: bool = True) -> Iterator[None]:
    if not enabled or sys.stdout is None or sys.platform != "win32":
        yield
        return
    restore_input_mode = enable_windows_virtual_terminal_input()
    try:
        sys.stdout.write(MOUSE_MODE_ENABLE)
        sys.stdout.flush()
        yield
    finally:
        try:
            sys.stdout.write(MOUSE_MODE_DISABLE)
            sys.stdout.flush()
        except (OSError, ValueError):
            pass
        restore_input_mode()


_posix_input_mode_active = False


@contextmanager
def posix_input_mode() -> Iterator[None]:
    global _posix_input_mode_active
    if sys.platform == "win32" or _posix_input_mode_active:
        yield
        return
    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)
    tty.setcbreak(fd)
    _posix_input_mode_active = True
    try:
        yield
    finally:
        _posix_input_mode_active = False
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def key_pressed() -> str | None:
    if sys.platform == "win32" and msvcrt.kbhit():
        return read_key()
    if sys.platform != "win32" and select.select([sys.stdin.fileno()], [], [], 0)[0]:
        return read_key()
    return None


def read_key_responsive(on_resize: Callable[[], None] | None = None) -> str:
    terminal_size = console.size
    while True:
        key = key_pressed()
        if key is not None:
            return key
        current_size = console.size
        if current_size != terminal_size:
            terminal_size = current_size
            if on_resize is not None:
                on_resize()
        time.sleep(0.05)


def confirm_choice(message: str, default: bool = False) -> bool:
    options = [("y", "是"), ("n", "否")]
    selected = 0 if default else 1
    return select_option("确认操作", options, selected, section_panel(f"[bold]{message}[/bold]", "请确认", "yellow")) == "y"


def read_key() -> str:
    if sys.platform != "win32":
        return read_posix_key()
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
        if code == "K":
            return "left"
        if code == "M":
            return "right"
        if code == "I":
            return "page_up"
        if code == "Q":
            return "page_down"
        if code == "G":
            return "home"
        if code == "O":
            return "end"
    if key == "\x1b":
        second = read_windows_char_if_available()
        if second is None:
            return "cancel"
        third = read_windows_char_if_available() if second in {"[", "O"} else ""
        if third == "<" and second == "[":
            mouse_key = parse_sgr_mouse_sequence(read_windows_until({"M", "m"}))
            if mouse_key:
                return mouse_key
            return "ignore"
        if third == "A":
            return "up"
        if third == "B":
            return "down"
        if third == "D":
            return "left"
        if third == "C":
            return "right"
        if third == "5" and second == "[":
            read_windows_char_if_available()
            return "page_up"
        if third == "6" and second == "[":
            read_windows_char_if_available()
            return "page_down"
        if third == "H":
            return "home"
        if third == "F":
            return "end"
        return "cancel"
    return key


def _read_posix_key_impl() -> str:
    fd = sys.stdin.fileno()
    ch = os.read(fd, 1)
    if not ch:
        return "ignore"
    key = ch.decode("utf-8", errors="ignore")
    if key == "\x03":
        return "cancel"
    if key in {"\r", "\n"}:
        return "enter"
    if key == "\x1b":
        second = read_posix_char_if_available()
        if second is None:
            return "ignore"
        third = read_posix_char_if_available() if second in {"[", "O"} else ""
        if second in {"[", "O"}:
            if third is None:
                return "ignore"
            if third == "<":
                mouse_key = parse_sgr_mouse_sequence(read_posix_until({"M", "m"}))
                if mouse_key:
                    return mouse_key
                return "ignore"
            if third == "M" and second == "[":
                read_posix_chars_if_available(3)
                return "ignore"
            if third == "A":
                return "up"
            if third == "B":
                return "down"
            if third == "D":
                return "left"
            if third == "C":
                return "right"
            if third == "5":
                return "page_up" if read_posix_char_if_available() == "~" else "ignore"
            if third == "6":
                return "page_down" if read_posix_char_if_available() == "~" else "ignore"
            if third == "H":
                return "home"
            if third == "F":
                return "end"
        return "ignore"
    return key


def read_posix_key() -> str:
    if _posix_input_mode_active:
        try:
            return _read_posix_key_impl()
        except KeyboardInterrupt:
            return "cancel"
    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)
    try:
        tty.setraw(fd)
        return _read_posix_key_impl()
    except KeyboardInterrupt:
        return "cancel"
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def prompt_text(
    title: str,
    prompt: str,
    *,
    default: str | None = None,
    password: bool = False,
    choices: list[str] | None = None,
) -> str:
    value: list[str] = []
    status: str | None = None

    def render() -> Any:
        visible_value = "*" * len(value) if password else "".join(value)
        max_visible = max(console.size.width - 14, 8)
        if len(visible_value) > max_visible:
            visible_value = "…" + visible_value[-(max_visible - 1):]
        input_line = Text()
        input_line.append(f"{prompt}\n", style="bold cyan")
        input_line.append(visible_value, style="bold")
        input_line.append("▌", style="bold bright_cyan")
        if not value and default is not None:
            input_line.append(f"\n默认值: {default}", style="dim")
        if choices:
            input_line.append(f"\n可选值: {' / '.join(choices)}", style="dim")
        renderables: list[Any] = [page_title(title), section_panel(input_line, "输入", "cyan")]
        if status:
            renderables.append(section_panel(status, "提示", "yellow"))
        return terminal_frame(
            renderables,
            shortcut_text("输入内容  ·  Backspace 删除  ·  Ctrl+U 清空  ·  Enter 确认  ·  Esc/Ctrl+C 取消"),
            preserve_bottom=True,
        )

    def refresh() -> None:
        live.update(render(), refresh=True)

    with posix_input_mode(), Live(render(), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key_responsive(refresh)
            if key == "cancel":
                raise KeyboardInterrupt
            if key == "enter":
                result = "".join(value) if value else (default or "")
                if choices and result not in choices:
                    status = f"请输入以下值之一: {' / '.join(choices)}"
                    refresh()
                    continue
                return result
            if key in {"\b", "\x7f"}:
                if value:
                    value.pop()
                    status = None
                    refresh()
                continue
            if key == "\x15":
                value.clear()
                status = None
                refresh()
                continue
            if key == "\x17":
                while value and value[-1].isspace():
                    value.pop()
                while value and not value[-1].isspace():
                    value.pop()
                status = None
                refresh()
                continue
            if isinstance(key, str) and len(key) == 1 and key.isprintable():
                value.append(key)
                status = None
                refresh()


def select_option(
    title: str,
    options: list[tuple[str, str]],
    selected: int = 0,
    content: Any | None = None,
    *,
    on_key: Callable[[str], str | None] | None = None,
) -> str:
    frame_offset = 0
    frame_state = render_option_menu_state(title, options, selected, content, frame_offset)
    last_wheel_key: str | None = None
    last_wheel_at = 0.0

    def refresh(*, ensure_selected_visible: bool) -> None:
        nonlocal frame_offset, frame_state
        frame_state = render_option_menu_state(
            title,
            options,
            selected,
            content,
            frame_offset,
            ensure_selected_visible=ensure_selected_visible,
        )
        frame_offset = frame_state.offset
        live.update(frame_state.renderable, refresh=True)

    with posix_input_mode(), mouse_wheel_mode(), Live(frame_state.renderable, console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key_responsive(lambda: refresh(ensure_selected_visible=True))
            if on_key is not None:
                result = on_key(key)
                if result is not None:
                    return result
            if key == "cancel":
                return options[-1][0]
            if key in WHEEL_KEYS:
                handle_wheel, last_wheel_key, last_wheel_at = should_handle_wheel(key, last_wheel_key, last_wheel_at)
                if not handle_wheel:
                    continue
            scroll_keys = {"page_up", "page_down", "home", "end"}
            if content is not None:
                scroll_keys.update(WHEEL_KEYS)
            if key in scroll_keys and frame_state.max_offset:
                frame_offset = content_scroll_offset(key, frame_offset, frame_state.max_offset, frame_state.viewport_height)
                refresh(ensure_selected_visible=False)
                continue
            if key in {"up", "scroll_up"}:
                selected = (selected - 1) % len(options)
                refresh(ensure_selected_visible=True)
                continue
            if key in {"down", "scroll_down"}:
                selected = (selected + 1) % len(options)
                refresh(ensure_selected_visible=True)
                continue
            if key == "enter":
                return options[selected][0]
            option_values = {option[0] for option in options}
            if key in option_values:
                return key
            if isinstance(key, str) and key.lower() in option_values:
                return key.lower()


def checkbox_menu_table(options: list[tuple[str, str]], selected: int, checked: set[int]) -> Table:
    table = Table(show_header=False, box=None, padding=(0, 1), expand=True)
    table.add_column("指示", justify="center", width=3)
    table.add_column("✓", justify="center", width=3)
    table.add_column("编号", justify="center", width=5)
    table.add_column("操作", ratio=1)
    for index, (value, label) in enumerate(options):
        check_mark = "[bold green]✓[/bold green]" if index in checked else "[dim] [/dim]"
        if index == selected:
            table.add_row(
                "[bold cyan]▶[/bold cyan]",
                check_mark,
                f"[bold black on cyan] {value} [/bold black on cyan]",
                f"[bold cyan]{label}[/bold cyan]",
            )
        else:
            table.add_row("", check_mark, f"[dim]{value}[/dim]", label)
    return table


def render_multi_select_state(
    title: str,
    options: list[tuple[str, str]],
    selected: int,
    checked: set[int],
    content: Any | None = None,
    frame_offset: int = 0,
    *,
    ensure_selected_visible: bool = True,
) -> TerminalFrameState:
    renderables = [page_title(title)]
    if content is not None:
        renderables.append(content)
    renderables.append(section_panel(checkbox_menu_table(options, selected, checked), "操作菜单", "cyan", "[dim]Space 切换 · A 全选/取消[/dim]"))
    shortcuts = "↑/↓ 选择  ·  Space 切换  ·  A 全选/取消  ·  Enter 确认  ·  PgUp/PgDn 翻阅  ·  Ctrl+C 跳过"
    if sys.platform == "win32" and content is not None:
        shortcuts = "↑/↓ 选择  ·  Space 切换  ·  A 全选/取消  ·  Enter 确认  ·  PgUp/PgDn/滚轮 翻阅  ·  Ctrl+C 跳过"
    return terminal_frame_state(
        renderables,
        shortcut_text(shortcuts),
        offset=frame_offset,
        focus_text=SELECTED_ROW_MARKER if ensure_selected_visible else None,
    )


def select_multiple(
    title: str,
    options: list[tuple[str, str]],
    content: Any | None = None,
) -> list[str]:
    selected = 0
    checked: set[int] = set()
    frame_offset = 0
    frame_state = render_multi_select_state(title, options, selected, checked, content, frame_offset)
    last_wheel_key: str | None = None
    last_wheel_at = 0.0

    def refresh(*, ensure_selected_visible: bool) -> None:
        nonlocal frame_offset, frame_state
        frame_state = render_multi_select_state(
            title,
            options,
            selected,
            checked,
            content,
            frame_offset,
            ensure_selected_visible=ensure_selected_visible,
        )
        frame_offset = frame_state.offset
        live.update(frame_state.renderable, refresh=True)

    with posix_input_mode(), mouse_wheel_mode(), Live(frame_state.renderable, console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key_responsive(lambda: refresh(ensure_selected_visible=True))
            if key == "cancel":
                return []
            if key in WHEEL_KEYS:
                handle_wheel, last_wheel_key, last_wheel_at = should_handle_wheel(key, last_wheel_key, last_wheel_at)
                if not handle_wheel:
                    continue
            scroll_keys = {"page_up", "page_down", "home", "end"}
            if content is not None:
                scroll_keys.update(WHEEL_KEYS)
            if key in scroll_keys and frame_state.max_offset:
                frame_offset = content_scroll_offset(key, frame_offset, frame_state.max_offset, frame_state.viewport_height)
                refresh(ensure_selected_visible=False)
                continue
            if key in {"up", "scroll_up"}:
                selected = (selected - 1) % len(options)
                refresh(ensure_selected_visible=True)
                continue
            if key in {"down", "scroll_down"}:
                selected = (selected + 1) % len(options)
                refresh(ensure_selected_visible=True)
                continue
            if key == " ":
                if selected in checked:
                    checked.discard(selected)
                else:
                    checked.add(selected)
                refresh(ensure_selected_visible=False)
                continue
            if key in {"a", "A"}:
                if len(checked) == len(options):
                    checked.clear()
                else:
                    checked = set(range(len(options)))
                refresh(ensure_selected_visible=False)
                continue
            if key == "enter":
                return [options[i][0] for i in sorted(checked)]


def clear_terminal_history() -> None:
    if sys.stdout is None:
        return
    try:
        sys.stdout.write("\033[2J\033[3J\033[H")
        sys.stdout.flush()
        console.clear()
    except (OSError, ValueError):
        return


def run_submodule(action: Any) -> Any:
    clear_terminal_history()
    try:
        return action()
    except KeyboardInterrupt:
        return section_panel("已取消，返回上一级。", "取消", "yellow")
    except Exception as exc:
        content = section_panel(
            f"[red]{exc.__class__.__name__}: {exc}[/red]\n\n[dim]已捕获错误，按返回键回到主页。[/dim]",
            "操作出错",
            "red",
        )
        show_result_page("操作出错", content)
        return content


def show_result_page(title: str, content: Any) -> None:
    result = content if isinstance(content, ResultPage) else ResultPage(content)
    status: Any | None = None
    options = [("0", "返回")]
    if result.copy_text:
        options.insert(0, ("c", result.copy_label))
    while True:
        page_content = Group(result.content, status) if status is not None else result.content
        choice = select_option(title, options, selected=0, content=page_content)
        if choice == "c" and result.copy_text:
            copied, message = copy_to_clipboard(result.copy_text)
            status = section_panel(message, "复制结果", "green" if copied else "yellow")
            continue
        return


def open_config_file(config_path: Path) -> str:
    path = Path(config_path)
    if not path.exists():
        return f"配置文件不存在: {path}"
    try:
        if sys.platform == "win32":
            os.startfile(str(path))
        elif sys.platform == "darwin":
            subprocess.Popen(["open", "-t", str(path)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        else:
            subprocess.Popen(["xdg-open", str(path)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except (AttributeError, OSError, ValueError) as exc:
        return f"无法打开配置文件: {exc}"
    return f"已使用默认文本编辑器打开: {path}"
