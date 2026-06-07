from __future__ import annotations

import msvcrt
import sys
from typing import Any

from rich import box
from rich.align import Align
from rich.console import Console, Group
from rich.live import Live
from rich.panel import Panel
from rich.table import Table


console = Console()


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


def render_option_menu(title: str, options: list[tuple[str, str]], selected: int, content: Any | None = None) -> Group:
    renderables = [page_title(title)]
    if content is not None:
        renderables.append(content)
    renderables.extend([
        section_panel(menu_table(options, selected), "操作菜单", "cyan", "[dim]选择下一步操作[/dim]"),
        shortcut_text("↑/↓ 选择  ·  Enter 确认  ·  数字快捷键  ·  Esc 返回"),
    ])
    return Group(*renderables)


def key_pressed() -> str | None:
    if msvcrt.kbhit():
        return read_key()
    return None


def confirm_choice(message: str, default: bool = False) -> bool:
    options = [("y", "是"), ("n", "否")]
    selected = 0 if default else 1
    return select_option("确认操作", options, selected, section_panel(f"[bold]{message}[/bold]", "请确认", "yellow")) == "y"


def read_key() -> str:
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
        if third == "A":
            return "up"
        if third == "B":
            return "down"
        if third == "D":
            return "left"
        if third == "C":
            return "right"
    return key


def select_option(title: str, options: list[tuple[str, str]], selected: int = 0, content: Any | None = None) -> str:
    with Live(render_option_menu(title, options, selected, content), console=console, screen=True, auto_refresh=False) as live:
        while True:
            key = read_key()
            if key == "cancel":
                return options[-1][0]
            if key == "up":
                selected = (selected - 1) % len(options)
                live.update(render_option_menu(title, options, selected, content), refresh=True)
                continue
            if key == "down":
                selected = (selected + 1) % len(options)
                live.update(render_option_menu(title, options, selected, content), refresh=True)
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
