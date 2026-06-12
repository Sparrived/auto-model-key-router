from __future__ import annotations

from collections.abc import Callable, Iterator
from contextlib import contextmanager
from dataclasses import dataclass
import sys
import time
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
MIN_RENDER_WIDTH = 20
FOLDED_CONTENT_MARKER = "… 上方内容已折叠"


@dataclass(frozen=True)
class ResultPage:
    content: Any
    copy_text: str | None = None
    copy_label: str = "复制 key"


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


def terminal_frame(renderables: list[Any], footer: Any | None = None, preserve_bottom: bool = True) -> Group:
    width = max(console.size.width, MIN_RENDER_WIDTH)
    height = max(console.size.height, 1)
    footer_lines = renderable_line_segments(footer, width) if footer is not None else []
    if len(footer_lines) >= height:
        return segment_lines_renderable(footer_lines[-height:])
    body_height = height - len(footer_lines)
    body_lines = renderable_line_segments(Group(*renderables), width) if renderables else []
    return segment_lines_renderable([*fit_terminal_lines(body_lines, body_height, preserve_bottom), *footer_lines])


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
    return section_panel(viewport, title, "blue", "[dim]滚轮或 PgUp/PgDn 翻阅[/dim]"), offset, max_offset, viewport_height


def render_option_menu(title: str, options: list[tuple[str, str]], selected: int, content: Any | None = None, content_offset: int = 0) -> Group:
    renderables = [page_title(title)]
    max_content_offset = 0
    if content is not None:
        content, _, max_content_offset, _ = scrollable_content_state(content, content_offset, len(options))
        renderables.append(content)
    shortcuts = "↑/↓ 选择  ·  Enter 确认  ·  数字快捷键  ·  Ctrl+C 返回"
    if max_content_offset:
        shortcuts = "↑/↓ 选择  ·  Enter 确认  ·  PgUp/PgDn/滚轮 翻阅内容  ·  数字快捷键  ·  Ctrl+C 返回"
    renderables.append(section_panel(menu_table(options, selected), "操作菜单", "cyan", "[dim]选择下一步操作[/dim]"))
    return terminal_frame(renderables, shortcut_text(shortcuts))


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
    chars = []
    while len(chars) < limit and select.select([sys.stdin], [], [], ESC_SEQUENCE_TIMEOUT_SECONDS)[0]:
        char = sys.stdin.read(1)
        chars.append(char)
        if char in end_chars:
            break
    return "".join(chars)


def read_posix_char_if_available(timeout: float = ESC_SEQUENCE_TIMEOUT_SECONDS) -> str | None:
    if select.select([sys.stdin], [], [], timeout)[0]:
        return sys.stdin.read(1)
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
    if not enabled or sys.stdout is None:
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


def key_pressed() -> str | None:
    if sys.platform == "win32" and msvcrt.kbhit():
        return read_key()
    if sys.platform != "win32" and select.select([sys.stdin], [], [], 0)[0]:
        return read_key()
    return None


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


def read_posix_key() -> str:
    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)
    try:
        tty.setraw(fd)
        key = sys.stdin.read(1)
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
    except KeyboardInterrupt:
        return "cancel"
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def select_option(title: str, options: list[tuple[str, str]], selected: int = 0, content: Any | None = None) -> str:
    content_offset = 0
    last_wheel_key: str | None = None
    last_wheel_at = 0.0

    def render() -> Group:
        return render_option_menu(title, options, selected, content, content_offset)

    with mouse_wheel_mode(), Live(render(), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key()
            if key == "cancel":
                return options[-1][0]
            if key in WHEEL_KEYS:
                handle_wheel, last_wheel_key, last_wheel_at = should_handle_wheel(key, last_wheel_key, last_wheel_at)
                if not handle_wheel:
                    continue
            if content is not None and key in {"scroll_up", "scroll_down", "page_up", "page_down", "home", "end"}:
                _, content_offset, max_content_offset, viewport_height = scrollable_content_state(content, content_offset, len(options))
                if max_content_offset:
                    content_offset = content_scroll_offset(key, content_offset, max_content_offset, viewport_height)
                    live.update(render(), refresh=True)
                continue
            if key in {"up", "scroll_up"}:
                selected = (selected - 1) % len(options)
                live.update(render(), refresh=True)
                continue
            if key in {"down", "scroll_down"}:
                selected = (selected + 1) % len(options)
                live.update(render(), refresh=True)
                continue
            if key == "enter":
                return options[selected][0]
            option_values = {option[0] for option in options}
            if key in option_values:
                return key
            if isinstance(key, str) and key.lower() in option_values:
                return key.lower()


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
