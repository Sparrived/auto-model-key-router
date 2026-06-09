from __future__ import annotations

from collections.abc import Callable, Iterator
from contextlib import contextmanager
import sys
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
from rich.table import Table
from rich.text import Text


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
    return Align.center(f"[dim]{text}[/dim]")


def content_viewport_height(option_count: int) -> int:
    return max(1, console.size.height - option_count - 8)


def renderable_line_segments(content: Any, width: int) -> list[list[Segment]]:
    return console.render_lines(content, console.options.update(width=max(width, 20)), pad=False)


def segment_lines_renderable(lines: list[list[Segment]]) -> Group:
    return Group(*(Segments(line) for line in (lines or [[Segment("")]])))


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
    shortcuts = "↑/↓ 选择  ·  Enter 确认  ·  数字快捷键  ·  Esc 返回"
    if max_content_offset:
        shortcuts = "↑/↓ 选择  ·  Enter 确认  ·  PgUp/PgDn/滚轮 翻阅内容  ·  数字快捷键  ·  Esc 返回"
    renderables.extend([
        section_panel(menu_table(options, selected), "操作菜单", "cyan", "[dim]选择下一步操作[/dim]"),
        shortcut_text(shortcuts),
    ])
    return Group(*renderables)


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


def read_windows_until(end_chars: set[str], limit: int = 64) -> str:
    chars = []
    while len(chars) < limit:
        char = msvcrt.getwch()
        chars.append(char)
        if char in end_chars:
            break
    return "".join(chars)


def read_posix_until(end_chars: set[str], limit: int = 64) -> str:
    chars = []
    while len(chars) < limit and select.select([sys.stdin], [], [], 0.05)[0]:
        char = sys.stdin.read(1)
        chars.append(char)
        if char in end_chars:
            break
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
        second = msvcrt.getwch()
        third = msvcrt.getwch() if second == "[" else ""
        if third == "<":
            mouse_key = parse_sgr_mouse_sequence(read_windows_until({"M", "m"}))
            if mouse_key:
                return mouse_key
        if third == "A":
            return "up"
        if third == "B":
            return "down"
        if third == "D":
            return "left"
        if third == "C":
            return "right"
        if third == "5":
            msvcrt.getwch()
            return "page_up"
        if third == "6":
            msvcrt.getwch()
            return "page_down"
        if third == "H":
            return "home"
        if third == "F":
            return "end"
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
            if select.select([sys.stdin], [], [], 0.05)[0]:
                second = sys.stdin.read(1)
                third = sys.stdin.read(1) if second == "[" and select.select([sys.stdin], [], [], 0.05)[0] else ""
                if third == "<":
                    mouse_key = parse_sgr_mouse_sequence(read_posix_until({"M", "m"}))
                    if mouse_key:
                        return mouse_key
                if third == "A":
                    return "up"
                if third == "B":
                    return "down"
                if third == "D":
                    return "left"
                if third == "C":
                    return "right"
                if third == "5":
                    sys.stdin.read(1)
                    return "page_up"
                if third == "6":
                    sys.stdin.read(1)
                    return "page_down"
                if third == "H":
                    return "home"
                if third == "F":
                    return "end"
            return "cancel"
        return key
    except KeyboardInterrupt:
        return "cancel"
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def select_option(title: str, options: list[tuple[str, str]], selected: int = 0, content: Any | None = None) -> str:
    content_offset = 0

    def render() -> Group:
        return render_option_menu(title, options, selected, content, content_offset)

    with mouse_wheel_mode(content is not None), Live(render(), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key()
            if key == "cancel":
                return options[-1][0]
            if key == "up":
                selected = (selected - 1) % len(options)
                live.update(render(), refresh=True)
                continue
            if key == "down":
                selected = (selected + 1) % len(options)
                live.update(render(), refresh=True)
                continue
            if content is not None and key in {"scroll_up", "scroll_down", "page_up", "page_down", "home", "end"}:
                _, content_offset, max_content_offset, viewport_height = scrollable_content_state(content, content_offset, len(options))
                if max_content_offset:
                    step = max(1, viewport_height // 3)
                    if key == "scroll_up":
                        content_offset = max(0, content_offset - step)
                    if key == "scroll_down":
                        content_offset = min(max_content_offset, content_offset + step)
                    if key == "page_up":
                        content_offset = max(0, content_offset - viewport_height)
                    if key == "page_down":
                        content_offset = min(max_content_offset, content_offset + viewport_height)
                    if key == "home":
                        content_offset = 0
                    if key == "end":
                        content_offset = max_content_offset
                    live.update(render(), refresh=True)
                continue
            if key == "enter":
                return options[selected][0]
            if key in {option[0] for option in options}:
                return key


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
    select_option(title, [("0", "返回")], selected=0, content=content)
