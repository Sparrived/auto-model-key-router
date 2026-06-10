from __future__ import annotations

import os
import re
import sqlite3
import subprocess
import sys
import time
from datetime import datetime, timedelta
from pathlib import Path

from rich import box
from rich.align import Align
from rich.console import Group
from rich.panel import Panel
from rich.table import Table
from rich.text import Text

from .formatting import percent, short_text
from .log_files import archived_log_paths
from .metrics import BEIJING_TZ
from .tui import console, key_pressed, mouse_wheel_mode, section_panel, shortcut_text, should_handle_wheel, terminal_frame


REQUEST_STATS_PAGE_SIZE = 10
LOG_LINE_PATTERN = re.compile(r"^(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}(?:,\d{3})?)\s+(CRITICAL|ERROR|WARNING|WARN|INFO|DEBUG)\s+([^\s]+)\s*(.*)$")
LOG_LEVEL_PATTERN = re.compile(r"\b(CRITICAL|ERROR|WARNING|WARN|INFO|DEBUG)\b")
ACCESS_STATUS_PATTERN = re.compile(r'("\s)([1-5]\d\d)(?=\s|$)')
LOG_LEVEL_STYLES = {
    "CRITICAL": "bold white on red",
    "ERROR": "bold red",
    "WARNING": "bold yellow",
    "WARN": "bold yellow",
    "INFO": "bold green",
    "DEBUG": "dim cyan",
}
STATS_TIME_RANGES: tuple[tuple[str, timedelta | None], ...] = (
    ("24小时", timedelta(hours=24)),
    ("3天", timedelta(days=3)),
    ("7天", timedelta(days=7)),
    ("30天", timedelta(days=30)),
    ("全部", None),
)


def render_logs(database_path: str, log_file_path: str, limit: int) -> None:
    console.print(service_logs_renderable(log_file_path, max(limit, 1), 0))
    console.print(request_stats_renderable(database_path, 1, REQUEST_STATS_PAGE_SIZE, 0))


def watch_logs(database_path: str, log_file_path: str, limit: int) -> None:
    page = "logs"
    log_offset = 0
    stats_page = 1
    stats_range_index = 0
    log_index = 0
    status_message: str | None = None
    last_wheel_key: str | None = None
    last_wheel_at = 0.0

    def render() -> Group:
        return render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page, stats_range_index, log_index, status_message)

    with console.screen():
        from rich.live import Live

        with mouse_wheel_mode(), Live(render(), console=console, screen=True, auto_refresh=False) as live:
            while True:
                started = time.monotonic()
                while time.monotonic() - started < 1:
                    key = key_pressed()
                    if key in {"scroll_up", "scroll_down"}:
                        handle_wheel, last_wheel_key, last_wheel_at = should_handle_wheel(key, last_wheel_key, last_wheel_at)
                        if not handle_wheel:
                            continue
                    if key in {"q", "Q", "0", "cancel"}:
                        return
                    if key in {"1", "l", "L"}:
                        page = "logs"
                        live.update(render(), refresh=True)
                        continue
                    if key in {"2", "s", "S"}:
                        page = "stats"
                        live.update(render(), refresh=True)
                        continue
                    if page == "logs" and key in {"up", "page_up", "scroll_up", "k", "K"}:
                        log_offset += max(limit, 1) if key == "page_up" else 1
                        live.update(render(), refresh=True)
                        continue
                    if page == "logs" and key in {"down", "page_down", "scroll_down", "j", "J"}:
                        log_offset = max(0, log_offset - (max(limit, 1) if key == "page_down" else 1))
                        live.update(render(), refresh=True)
                        continue
                    if page == "logs" and key in {"home", "g", "G"}:
                        log_offset = 0
                        live.update(render(), refresh=True)
                        continue
                    if page == "logs" and key in {"left", "["}:
                        log_index = max(0, log_index - 1)
                        log_offset = 0
                        status_message = None
                        live.update(render(), refresh=True)
                        continue
                    if page == "logs" and key in {"right", "]"}:
                        log_index = min(log_index + 1, len(log_file_choices(log_file_path)) - 1)
                        log_offset = 0
                        status_message = None
                        live.update(render(), refresh=True)
                        continue
                    if page == "logs" and key in {"o", "O"}:
                        status_message = open_log_file(str(selected_log_file(log_file_path, log_index)[0]))
                        live.update(render(), refresh=True)
                        continue
                    if page == "stats" and key in {"\t", "tab"}:
                        stats_range_index = (stats_range_index + 1) % len(STATS_TIME_RANGES)
                        stats_page = 1
                        live.update(render(), refresh=True)
                        continue
                    if page == "stats" and key in {"left", "page_up", "up", "scroll_up", "p", "P"}:
                        stats_page = max(1, stats_page - 1)
                        live.update(render(), refresh=True)
                        continue
                    if page == "stats" and key in {"right", "page_down", "down", "scroll_down", "n", "N"}:
                        stats_page += 1
                        live.update(render(), refresh=True)
                        continue
                    time.sleep(0.05)
                live.update(render(), refresh=True)


def render_live_logs(database_path: str, log_file_path: str, limit: int, page: str, log_offset: int, stats_page: int, stats_range_index: int, log_index: int = 0, status_message: str | None = None) -> Group:
    if page == "logs":
        selected_path, selected_index, choices = selected_log_file(log_file_path, log_index)
        content = service_logs_renderable(str(selected_path), log_page_size(limit), log_offset, log_file_title(selected_path, selected_index, len(choices)))
    else:
        content = request_stats_renderable(database_path, stats_page, REQUEST_STATS_PAGE_SIZE, stats_range_index)
    renderables = [log_header_renderable(page), content]
    if status_message:
        renderables.append(section_panel(status_message, "提示", "green" if status_message.startswith("已") else "yellow"))
    return terminal_frame(renderables, log_help_text(page))


def log_page_size(limit: int) -> int:
    reserved_rows = 10
    return max(1, min(max(limit, 1), console.size.height - reserved_rows))


def log_header_renderable(page: str) -> Panel:
    logs_label = "[bold black on cyan] 1 运行日志 [/bold black on cyan]" if page == "logs" else "[dim]1[/dim] 运行日志"
    stats_label = "[bold black on cyan] 2 调用统计 [/bold black on cyan]" if page == "stats" else "[dim]2[/dim] 调用统计"
    return section_panel(Align.center(f"{logs_label}    {stats_label}"), "调用日志", "cyan", "[dim]运行日志与调用统计[/dim]")


def log_help_text(page: str) -> Align:
    if page == "logs":
        return shortcut_text("1 运行日志  ·  2 统计  ·  ←/→ 切换日志  ·  O 打开日志  ·  ↑/↓/滚轮/Pg 滚动  ·  Esc/q 返回")
    return shortcut_text("1 运行日志  ·  2 统计  ·  Tab 查询范围  ·  ←/→/滚轮 翻页  ·  Esc/q 返回")


def open_log_file(log_file_path: str) -> str:
    path = Path(log_file_path)
    if not path.exists():
        return f"运行日志不存在: {path}"
    try:
        if sys.platform == "win32":
            os.startfile(str(path))
        elif sys.platform == "darwin":
            subprocess.Popen(["open", "-t", str(path)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        else:
            subprocess.Popen(["xdg-open", str(path)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except (AttributeError, OSError, ValueError) as exc:
        return f"无法打开运行日志: {exc}"
    return f"已使用默认文本编辑器打开: {path}"


def service_logs_renderable(log_file_path: str, limit: int, offset: int = 0, title_prefix: str = "运行日志") -> Panel:
    path = Path(log_file_path)
    if not path.exists():
        return section_panel(f"[yellow]运行日志不存在: {path}[/yellow]", title_prefix, "yellow")
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    total = len(lines)
    page_size = max(limit, 1)
    max_offset = max(total - page_size, 0)
    offset = min(max(offset, 0), max_offset)
    end = total - offset
    start = max(end - page_size, 0)
    content = log_lines_text(lines[start:end])
    title = title_prefix if total == 0 else f"{title_prefix} 第 {start + 1}-{end} 行 / 共 {total} 行"
    return section_panel(content, title, "blue", "[dim]最新日志在底部 · 按级别与状态码染色[/dim]")


def log_file_choices(log_file_path: str) -> list[Path]:
    return [Path(log_file_path), *archived_log_paths(log_file_path)]


def selected_log_file(log_file_path: str, index: int) -> tuple[Path, int, list[Path]]:
    choices = log_file_choices(log_file_path)
    selected_index = min(max(index, 0), len(choices) - 1)
    return choices[selected_index], selected_index, choices


def log_file_title(path: Path, index: int, total: int) -> str:
    if index == 0:
        return f"当前日志 1/{total} · {path.name}"
    return f"历史日志 {index}/{max(total - 1, 1)} · {path.name}"


def log_lines_text(lines: list[str]) -> Text:
    if not lines:
        return Text("暂无运行日志", style="dim", no_wrap=True, overflow="ellipsis")
    content = Text(no_wrap=True, overflow="ellipsis")
    for index, line in enumerate(lines):
        if index:
            content.append("\n")
        content.append_text(log_line_text(line))
    return content


def log_line_text(line: str) -> Text:
    match = LOG_LINE_PATTERN.match(line)
    if not match:
        return fallback_log_line_text(line)
    timestamp, level, logger_name, message = match.groups()
    text = Text()
    text.append(timestamp, style="dim")
    text.append(" ")
    text.append(level, style=LOG_LEVEL_STYLES.get(level, "bold"))
    text.append(" ")
    text.append(logger_name, style=logger_style(logger_name))
    if message:
        text.append(" ")
        text.append_text(log_message_text(message))
    return text


def fallback_log_line_text(line: str) -> Text:
    level_match = LOG_LEVEL_PATTERN.search(line)
    if level_match:
        style = LOG_LEVEL_STYLES.get(level_match.group(1), "")
    elif "Traceback" in line or "Exception" in line:
        style = "red"
    else:
        style = "dim" if line.startswith((" ", "\t")) else ""
    return Text(line, style=style)


def log_message_text(message: str) -> Text:
    text = Text()
    position = 0
    for match in ACCESS_STATUS_PATTERN.finditer(message):
        text.append(message[position : match.start(2)])
        text.append(match.group(2), style=status_code_style(match.group(2)))
        position = match.end(2)
    text.append(message[position:])
    return text


def logger_style(logger_name: str) -> str:
    if logger_name.startswith("auto_model_key_router"):
        return "bright_cyan"
    if logger_name.startswith("uvicorn.access"):
        return "blue"
    if logger_name.startswith("uvicorn"):
        return "cyan"
    return "magenta"


def status_code_style(status_code: str) -> str:
    if status_code.startswith("2"):
        return "bold green"
    if status_code.startswith("3"):
        return "bold cyan"
    if status_code.startswith("4"):
        return "bold yellow"
    return "bold red"


def request_stats_renderable(database_path: str, page: int, page_size: int, stats_range_index: int = 0) -> Group | Panel:
    path = Path(database_path)
    if not path.exists():
        return section_panel(f"[yellow]统计数据库不存在: {path}[/yellow]", "调用统计", "yellow")
    page_size = max(page_size, 1)
    page = max(page, 1)
    offset = (page - 1) * page_size
    stats_range_index, range_label, range_parameters = stats_range_query(stats_range_index)
    where_clause = "WHERE created_at >= ?" if range_parameters else ""
    status_where_clause = f"{where_clause} AND status_code IS NOT NULL" if where_clause else "WHERE status_code IS NOT NULL"
    table = Table(show_lines=False, box=box.SIMPLE_HEAVY, expand=True)
    for name in ["时间", "模型", "Key", "状态", "成功", "重试", "输入", "输出", "总Tok", "缓存", "首字", "耗时"]:
        table.add_column(name)
    with sqlite3.connect(path) as connection:
        connection.row_factory = sqlite3.Row
        columns = {row["name"] for row in connection.execute("PRAGMA table_info(request_metrics)").fetchall()}
        cached_tokens_expr = "cached_tokens" if "cached_tokens" in columns else "0 AS cached_tokens"
        cached_tokens_sum_expr = "cached_tokens" if "cached_tokens" in columns else "0"
        cache_hit_sum_expr = "cache_hit" if "cache_hit" in columns else "0"
        first_token_ms_expr = "first_token_ms" if "first_token_ms" in columns else "0 AS first_token_ms"
        first_token_ms_sum_expr = "first_token_ms" if "first_token_ms" in columns else "0"
        duration_ms_expr = "duration_ms" if "duration_ms" in columns else "0 AS duration_ms"
        duration_ms_sum_expr = "duration_ms" if "duration_ms" in columns else "0"
        total = connection.execute(f"SELECT COUNT(*) AS total FROM request_metrics {where_clause}", range_parameters).fetchone()["total"]
        summary = connection.execute(f"""
            SELECT COUNT(*) AS requests, COALESCE(SUM(success), 0) AS successes, COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) AS failures,
            COALESCE(SUM(retried), 0) AS retries, COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
            COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM({cached_tokens_sum_expr}), 0) AS cached_tokens, COALESCE(SUM({cache_hit_sum_expr}), 0) AS cache_hits,
            COALESCE(ROUND(AVG({first_token_ms_sum_expr})), 0) AS avg_first_token_ms, COALESCE(MAX({first_token_ms_sum_expr}), 0) AS max_first_token_ms,
            COALESCE(ROUND(AVG({duration_ms_sum_expr})), 0) AS avg_duration_ms, COALESCE(MAX({duration_ms_sum_expr}), 0) AS max_duration_ms, MAX(created_at) AS latest_request_at
            FROM request_metrics
            {where_clause}
        """, range_parameters).fetchone()
        status_rows = connection.execute(f"SELECT status_code, COUNT(*) AS total FROM request_metrics {status_where_clause} GROUP BY status_code ORDER BY status_code", range_parameters).fetchall()
        max_page = max((total + page_size - 1) // page_size, 1)
        page = min(page, max_page)
        offset = (page - 1) * page_size
        rows = connection.execute(f"SELECT created_at, model_id, key_name, status_code, success, retried, prompt_tokens, completion_tokens, total_tokens, {cached_tokens_expr}, {first_token_ms_expr}, {duration_ms_expr} FROM request_metrics {where_clause} ORDER BY id DESC LIMIT ? OFFSET ?", (*range_parameters, page_size, offset)).fetchall()
    for row in rows:
        table.add_row(short_text(row["created_at"], 19), short_text(row["model_id"], 22), short_text(row["key_name"], 18), "-" if row["status_code"] is None else str(row["status_code"]), "是" if row["success"] else "否", "是" if row["retried"] else "否", str(row["prompt_tokens"]), str(row["completion_tokens"]), str(row["total_tokens"]), str(row["cached_tokens"]), str(row["first_token_ms"]), str(row["duration_ms"]))
    if not rows:
        table.add_row("暂无", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-")
    details_title = f"请求明细 · {range_label} · 第 {page}/{max_page} 页 · {page_size}/页 · 共 {total} 条"
    return Group(section_panel(stats_range_tabs_renderable(stats_range_index), "查询范围", "cyan", "[dim]按 Tab 切换[/dim]"), section_panel(request_stats_summary_renderable(summary, status_rows), f"总览 · {range_label}", "cyan"), section_panel(table, details_title, "blue"))


def stats_range_query(stats_range_index: int) -> tuple[int, str, tuple[str, ...]]:
    index = stats_range_index if 0 <= stats_range_index < len(STATS_TIME_RANGES) else 0
    label, delta = STATS_TIME_RANGES[index]
    if delta is None:
        return index, label, ()
    return index, label, ((datetime.now(BEIJING_TZ) - delta).isoformat(),)


def stats_range_tabs_renderable(active_index: int) -> Align:
    labels = []
    for index, (label, _) in enumerate(STATS_TIME_RANGES):
        labels.append(f"[bold black on cyan] {label} [/bold black on cyan]" if index == active_index else f"[dim]{label}[/dim]")
    return Align.center("    ".join(labels))


def request_stats_summary_renderable(summary: sqlite3.Row, status_rows: list[sqlite3.Row]) -> Table:
    requests = int(summary["requests"])
    successes = int(summary["successes"])
    failures = int(summary["failures"])
    retries = int(summary["retries"])
    prompt_tokens = int(summary["prompt_tokens"])
    completion_tokens = int(summary["completion_tokens"])
    total_tokens = int(summary["total_tokens"])
    cached_tokens = int(summary["cached_tokens"])
    cache_hits = int(summary["cache_hits"])
    avg_first_token_ms = int(summary["avg_first_token_ms"])
    max_first_token_ms = int(summary["max_first_token_ms"])
    avg_duration_ms = int(summary["avg_duration_ms"])
    max_duration_ms = int(summary["max_duration_ms"])
    status_codes = ", ".join(f"{row['status_code']}:{row['total']}" for row in status_rows) or "-"
    table = Table(show_header=False, show_lines=False, box=None, expand=True)
    table.add_column("指标", style="cyan")
    table.add_column("值", justify="right")
    table.add_column("指标", style="cyan")
    table.add_column("值", justify="right")
    table.add_column("指标", style="cyan")
    table.add_column("值", justify="right")
    table.add_row("总请求", str(requests), "成功率", percent(successes, requests), "成功/失败", f"{successes}/{failures}")
    table.add_row("重试", f"{retries} ({percent(retries, requests)})", "输入/输出 Tok", f"{prompt_tokens}/{completion_tokens}", "总 Tok", str(total_tokens))
    table.add_row("缓存 Tok", f"{cached_tokens} ({percent(cached_tokens, prompt_tokens)})", "缓存命中", f"{cache_hits} ({percent(cache_hits, requests)})", "状态码", short_text(status_codes, 36))
    table.add_row("平均/最长首字", f"{avg_first_token_ms}/{max_first_token_ms} ms", "平均/最长耗时", f"{avg_duration_ms}/{max_duration_ms} ms", "最新请求", short_text(summary["latest_request_at"] or "-", 19))
    return table
