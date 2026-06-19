from __future__ import annotations

import os
import re
import sqlite3
import subprocess
import sys
import time
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

from rich import box
from rich.align import Align
from rich.console import Group
from rich.live import Live
from rich.panel import Panel
from rich.table import Table
from rich.text import Text

from .formatting import abbreviate_number, percent, short_text
from .log_files import archived_log_paths
from .metrics import BEIJING_TZ, RATE_WINDOW_SECONDS
from .tui import console, content_scroll_offset, key_pressed, mouse_wheel_mode, posix_input_mode, section_panel, shortcut_text, should_handle_wheel, terminal_frame_state


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
STATS_CALLER_PAGES: tuple[tuple[str, str, str | None], ...] = (
    ("stats", "全部调用", None),
    ("stats_local", "本地调用", "local"),
    ("stats_visitor", "访客调用", "visitor"),
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
    frame_offset = 0
    status_message: str | None = None
    last_wheel_key: str | None = None
    last_wheel_at = 0.0

    frame_state = render_live_logs_state(database_path, log_file_path, limit, page, log_offset, stats_page, stats_range_index, log_index, status_message, frame_offset)

    def refresh() -> None:
        nonlocal frame_offset, frame_state
        frame_state = render_live_logs_state(
            database_path,
            log_file_path,
            limit,
            page,
            log_offset,
            stats_page,
            stats_range_index,
            log_index,
            status_message,
            frame_offset,
        )
        frame_offset = frame_state.offset
        live.update(frame_state.renderable, refresh=True)

    with posix_input_mode(), mouse_wheel_mode(), Live(frame_state.renderable, console=console, screen=True, auto_refresh=False) as live:
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
                    frame_offset = 0
                    refresh()
                    continue
                if key in {"2", "s", "S"}:
                    page = "stats"
                    frame_offset = 0
                    refresh()
                    continue
                if key == "3":
                    page = "stats_local"
                    stats_page = 1
                    frame_offset = 0
                    refresh()
                    continue
                if key == "4":
                    page = "stats_visitor"
                    stats_page = 1
                    frame_offset = 0
                    refresh()
                    continue
                if page == "logs" and key in {"up", "page_up", "scroll_up", "k", "K"}:
                    log_offset += log_page_size(limit) if key == "page_up" else 1
                    refresh()
                    continue
                if page == "logs" and key in {"down", "page_down", "scroll_down", "j", "J"}:
                    log_offset = max(0, log_offset - (log_page_size(limit) if key == "page_down" else 1))
                    refresh()
                    continue
                if page == "logs" and key in {"home", "g", "G"}:
                    log_offset = 0
                    refresh()
                    continue
                if page == "logs" and key in {"left", "["}:
                    log_index = max(0, log_index - 1)
                    log_offset = 0
                    frame_offset = 0
                    status_message = None
                    refresh()
                    continue
                if page == "logs" and key in {"right", "]"}:
                    log_index = min(log_index + 1, len(log_file_choices(log_file_path)) - 1)
                    log_offset = 0
                    frame_offset = 0
                    status_message = None
                    refresh()
                    continue
                if page == "logs" and key in {"o", "O"}:
                    status_message = open_log_file(str(selected_log_file(log_file_path, log_index)[0]))
                    refresh()
                    continue
                if page != "logs" and key in {"\t", "tab"}:
                    stats_range_index = (stats_range_index + 1) % len(STATS_TIME_RANGES)
                    stats_page = 1
                    frame_offset = 0
                    refresh()
                    continue
                if page != "logs" and key in {"up", "scroll_up", "down", "scroll_down", "page_up", "page_down", "home", "end"} and frame_state.max_offset:
                    frame_offset = content_scroll_offset(key, frame_offset, frame_state.max_offset, frame_state.viewport_height)
                    refresh()
                    continue
                if page != "logs" and key in {"left", "p", "P"}:
                    stats_page = max(1, stats_page - 1)
                    frame_offset = 0
                    refresh()
                    continue
                if page != "logs" and key in {"right", "n", "N"}:
                    stats_page += 1
                    frame_offset = 0
                    refresh()
                    continue
                time.sleep(0.05)
            refresh()


def render_live_logs_state(
    database_path: str,
    log_file_path: str,
    limit: int,
    page: str,
    log_offset: int,
    stats_page: int,
    stats_range_index: int,
    log_index: int = 0,
    status_message: str | None = None,
    frame_offset: int = 0,
) -> Any:
    if page == "logs":
        selected_path, selected_index, choices = selected_log_file(log_file_path, log_index)
        content = service_logs_renderable(str(selected_path), log_page_size(limit), log_offset, log_file_title(selected_path, selected_index, len(choices)))
    else:
        content = request_stats_renderable(
            database_path,
            stats_page,
            REQUEST_STATS_PAGE_SIZE,
            stats_range_index,
            caller_type=stats_caller_type(page),
        )
    renderables = [log_header_renderable(page), content]
    if status_message:
        renderables.append(section_panel(status_message, "提示", "green" if status_message.startswith("已") else "yellow"))
    return terminal_frame_state(renderables, log_help_text(page), offset=frame_offset)


def render_live_logs(database_path: str, log_file_path: str, limit: int, page: str, log_offset: int, stats_page: int, stats_range_index: int, log_index: int = 0, status_message: str | None = None) -> Any:
    return render_live_logs_state(database_path, log_file_path, limit, page, log_offset, stats_page, stats_range_index, log_index, status_message).renderable


def log_page_size(limit: int) -> int:
    reserved_rows = 12
    return max(1, min(max(limit, 1), console.size.height - reserved_rows))


def log_header_renderable(page: str) -> Panel:
    logs_label = "[bold black on cyan] 1 运行日志 [/bold black on cyan]" if page == "logs" else "[dim]1[/dim] 运行日志"
    caller_labels = []
    for index, (page_id, label, _) in enumerate(STATS_CALLER_PAGES, start=2):
        caller_labels.append(
            f"[bold black on cyan] {index} {label} [/bold black on cyan]"
            if page == page_id
            else f"[dim]{index}[/dim] {label}"
        )
    return section_panel(Align.center("    ".join([logs_label, *caller_labels])), "调用日志", "cyan", "[dim]运行日志与分类调用统计[/dim]")


def log_help_text(page: str) -> Align:
    if page == "logs":
        return shortcut_text("1 日志  ·  2 全部  ·  3 本地  ·  4 访客  ·  ←/→ 切换日志  ·  O 打开  ·  ↑/↓/Pg 滚动  ·  q 返回" if sys.platform != "win32" else "1 日志  ·  2 全部  ·  3 本地  ·  4 访客  ·  ←/→ 切换日志  ·  O 打开  ·  ↑/↓/滚轮/Pg 滚动  ·  q 返回")
    return shortcut_text("1 日志  ·  2 全部  ·  3 本地  ·  4 访客  ·  Tab 时间范围  ·  ↑/↓/Pg 滚动  ·  ←/→ 翻页  ·  q 返回" if sys.platform != "win32" else "1 日志  ·  2 全部  ·  3 本地  ·  4 访客  ·  Tab 时间范围  ·  ↑/↓/滚轮/Pg 滚动  ·  ←/→ 翻页  ·  q 返回")


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


def request_stats_renderable(database_path: str, page: int, page_size: int, stats_range_index: int = 0, caller_type: str | None = None) -> Group | Panel:
    path = Path(database_path)
    if not path.exists():
        return section_panel(f"[yellow]统计数据库不存在: {path}[/yellow]", "调用统计", "yellow")
    page_size = max(page_size, 1)
    page = max(page, 1)
    offset = (page - 1) * page_size
    stats_range_index, range_label, range_parameters = stats_range_query(stats_range_index)
    table = Table(show_lines=False, box=box.SIMPLE_HEAVY, expand=True)
    for name in ["时间", "来源", "模型", "Key", "状态", "成功", "重试", "输入", "缓存", "输出", "总Tok", "首字", "耗时"]:
        table.add_column(name)
    with sqlite3.connect(path) as connection:
        connection.row_factory = sqlite3.Row
        columns = {row["name"] for row in connection.execute("PRAGMA table_info(request_metrics)").fetchall()}
        has_caller_type = "caller_type" in columns
        where_clause, query_parameters = stats_filter_query(range_parameters, caller_type, has_caller_type)
        status_where_clause = f"{where_clause} AND status_code IS NOT NULL" if where_clause else "WHERE status_code IS NOT NULL"
        caller_type_expr = "caller_type" if has_caller_type else "'local' AS caller_type"
        cached_tokens_expr = "cached_tokens" if "cached_tokens" in columns else "0 AS cached_tokens"
        cached_tokens_sum_expr = "cached_tokens" if "cached_tokens" in columns else "0"
        first_token_ms_expr = "first_token_ms" if "first_token_ms" in columns else "0 AS first_token_ms"
        first_token_ms_sum_expr = "first_token_ms" if "first_token_ms" in columns else "0"
        duration_ms_expr = "duration_ms" if "duration_ms" in columns else "0 AS duration_ms"
        duration_ms_sum_expr = "duration_ms" if "duration_ms" in columns else "0"
        total = connection.execute(f"SELECT COUNT(*) AS total FROM request_metrics {where_clause}", query_parameters).fetchone()["total"]
        summary = connection.execute(f"""
            SELECT COUNT(*) AS requests, COALESCE(SUM(success), 0) AS successes, COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) AS failures,
            COALESCE(SUM(retried), 0) AS retries, COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
            COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM({cached_tokens_sum_expr}), 0) AS cached_tokens,
            COALESCE(ROUND(AVG({first_token_ms_sum_expr})), 0) AS avg_first_token_ms, COALESCE(MAX({first_token_ms_sum_expr}), 0) AS max_first_token_ms,
            COALESCE(ROUND(AVG({duration_ms_sum_expr})), 0) AS avg_duration_ms, COALESCE(MAX({duration_ms_sum_expr}), 0) AS max_duration_ms, MAX(created_at) AS latest_request_at
            FROM request_metrics
            {where_clause}
        """, query_parameters).fetchone()
        rate_since = (datetime.now(BEIJING_TZ) - timedelta(seconds=RATE_WINDOW_SECONDS)).isoformat()
        rate_where_clause, rate_query_parameters = stats_filter_query((rate_since,), caller_type, has_caller_type)
        rate_summary = connection.execute(f"""
            SELECT COUNT(*) AS rpm, COALESCE(SUM(total_tokens), 0) AS tpm
            FROM request_metrics
            {rate_where_clause}
        """, rate_query_parameters).fetchone()
        status_rows = connection.execute(f"SELECT status_code, COUNT(*) AS total FROM request_metrics {status_where_clause} GROUP BY status_code ORDER BY status_code", query_parameters).fetchall()
        max_page = max((total + page_size - 1) // page_size, 1)
        page = min(page, max_page)
        offset = (page - 1) * page_size
        rows = connection.execute(f"SELECT created_at, {caller_type_expr}, model_id, key_name, status_code, success, retried, prompt_tokens, completion_tokens, total_tokens, {cached_tokens_expr}, {first_token_ms_expr}, {duration_ms_expr} FROM request_metrics {where_clause} ORDER BY id DESC LIMIT ? OFFSET ?", (*query_parameters, page_size, offset)).fetchall()
    for row in rows:
        table.add_row(short_text(row["created_at"], 19), caller_type_text(row["caller_type"]), short_text(row["model_id"], 22), short_text(row["key_name"], 18), "-" if row["status_code"] is None else str(row["status_code"]), "是" if row["success"] else "否", "是" if row["retried"] else "否", abbreviate_number(row["prompt_tokens"] - row["cached_tokens"]), abbreviate_number(row["cached_tokens"]), abbreviate_number(row["completion_tokens"]), abbreviate_number(row["total_tokens"]), str(row["first_token_ms"]), str(row["duration_ms"]))
    if not rows:
        table.add_row("暂无", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-")
    caller_label = caller_type_title(caller_type)
    details_title = f"请求明细 · {caller_label} · {range_label} · 第 {page}/{max_page} 页 · {page_size}/页 · 共 {total} 条"
    return Group(section_panel(stats_range_tabs_renderable(stats_range_index), "查询范围", "cyan", "[dim]按 Tab 切换[/dim]"), section_panel(request_stats_summary_renderable(summary, status_rows, rate_summary), f"总览 · {caller_label} · {range_label}", "cyan"), section_panel(table, details_title, "blue"))


def stats_caller_type(page: str) -> str | None:
    return next((caller_type for page_id, _, caller_type in STATS_CALLER_PAGES if page_id == page), None)


def key_stats_renderable(database_path: str, model_id: str, key_name: str, page: int, page_size: int, stats_range_index: int = 0) -> Group | Panel:
    path = Path(database_path)
    if not path.exists():
        return section_panel(f"[yellow]统计数据库不存在: {path}[/yellow]", "Key 统计", "yellow")
    page_size = max(page_size, 1)
    page = max(page, 1)
    offset = (page - 1) * page_size
    stats_range_index, range_label, range_parameters = stats_range_query(stats_range_index)
    table = Table(show_lines=False, box=box.SIMPLE_HEAVY, expand=True)
    for name in ["时间", "状态", "成功", "重试", "输入", "缓存", "输出", "总Tok", "首字", "耗时"]:
        table.add_column(name)
    with sqlite3.connect(path) as connection:
        connection.row_factory = sqlite3.Row
        columns = {row["name"] for row in connection.execute("PRAGMA table_info(request_metrics)").fetchall()}
        cached_tokens_expr = "cached_tokens" if "cached_tokens" in columns else "0 AS cached_tokens"
        cached_tokens_sum_expr = "cached_tokens" if "cached_tokens" in columns else "0"
        first_token_ms_expr = "first_token_ms" if "first_token_ms" in columns else "0 AS first_token_ms"
        first_token_ms_sum_expr = "first_token_ms" if "first_token_ms" in columns else "0"
        duration_ms_expr = "duration_ms" if "duration_ms" in columns else "0 AS duration_ms"
        duration_ms_sum_expr = "duration_ms" if "duration_ms" in columns else "0"
        key_where_parts = ["model_id = ?", "key_name = ?"]
        key_params: list[str] = [model_id, key_name]
        if range_parameters:
            key_where_parts.append("created_at >= ?")
            key_params.extend(range_parameters)
        key_where = " AND ".join(key_where_parts)
        total = connection.execute(f"SELECT COUNT(*) AS total FROM request_metrics WHERE {key_where}", key_params).fetchone()["total"]
        summary = connection.execute(f"""
            SELECT COUNT(*) AS requests, COALESCE(SUM(success), 0) AS successes, COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) AS failures,
            COALESCE(SUM(retried), 0) AS retries, COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
            COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM({cached_tokens_sum_expr}), 0) AS cached_tokens,
            COALESCE(ROUND(AVG({first_token_ms_sum_expr})), 0) AS avg_first_token_ms, COALESCE(MAX({first_token_ms_sum_expr}), 0) AS max_first_token_ms,
            COALESCE(ROUND(AVG({duration_ms_sum_expr})), 0) AS avg_duration_ms, COALESCE(MAX({duration_ms_sum_expr}), 0) AS max_duration_ms, MAX(created_at) AS latest_request_at
            FROM request_metrics
            WHERE {key_where}
        """, key_params).fetchone()
        rate_since = (datetime.now(BEIJING_TZ) - timedelta(seconds=RATE_WINDOW_SECONDS)).isoformat()
        rate_where = f"model_id = ? AND key_name = ? AND created_at >= ?"
        rate_params: list[str] = [model_id, key_name, rate_since]
        rate_summary = connection.execute(f"""
            SELECT COUNT(*) AS rpm, COALESCE(SUM(total_tokens), 0) AS tpm
            FROM request_metrics
            WHERE {rate_where}
        """, rate_params).fetchone()
        status_where = f"{key_where} AND status_code IS NOT NULL"
        status_rows = connection.execute(f"SELECT status_code, COUNT(*) AS total FROM request_metrics WHERE {status_where} GROUP BY status_code ORDER BY status_code", key_params).fetchall()
        max_page = max((total + page_size - 1) // page_size, 1)
        page = min(page, max_page)
        offset = (page - 1) * page_size
        rows = connection.execute(f"SELECT created_at, status_code, success, retried, prompt_tokens, completion_tokens, total_tokens, {cached_tokens_expr}, {first_token_ms_expr}, {duration_ms_expr} FROM request_metrics WHERE {key_where} ORDER BY id DESC LIMIT ? OFFSET ?", (*key_params, page_size, offset)).fetchall()
    for row in rows:
        table.add_row(short_text(row["created_at"], 19), "-" if row["status_code"] is None else str(row["status_code"]), "是" if row["success"] else "否", "是" if row["retried"] else "否", abbreviate_number(row["prompt_tokens"] - row["cached_tokens"]), abbreviate_number(row["cached_tokens"]), abbreviate_number(row["completion_tokens"]), abbreviate_number(row["total_tokens"]), str(row["first_token_ms"]), str(row["duration_ms"]))
    if not rows:
        table.add_row("暂无", "-", "-", "-", "-", "-", "-", "-", "-", "-")
    key_label = f"{short_text(model_id, 24)} / {short_text(key_name, 20)}"
    details_title = f"请求明细 · {key_label} · {range_label} · 第 {page}/{max_page} 页 · 共 {total} 条"
    return Group(section_panel(stats_range_tabs_renderable(stats_range_index), "查询范围", "cyan", "[dim]按 Tab 切换[/dim]"), section_panel(request_stats_summary_renderable(summary, status_rows, rate_summary), f"总览 · {key_label} · {range_label}", "cyan"), section_panel(table, details_title, "blue"))


def watch_key_stats(database_path: str, model_id: str, key_name: str) -> None:
    page = 1
    stats_range_index = 0
    frame_offset = 0
    last_wheel_key: str | None = None
    last_wheel_at = 0.0

    frame_state = terminal_frame_state(
        [key_stats_header_renderable(model_id, key_name), key_stats_renderable(database_path, model_id, key_name, page, REQUEST_STATS_PAGE_SIZE, stats_range_index)],
        key_stats_help_text(),
        offset=frame_offset,
    )

    def refresh() -> None:
        nonlocal frame_offset, frame_state
        frame_state = terminal_frame_state(
            [key_stats_header_renderable(model_id, key_name), key_stats_renderable(database_path, model_id, key_name, page, REQUEST_STATS_PAGE_SIZE, stats_range_index)],
            key_stats_help_text(),
            offset=frame_offset,
        )
        frame_offset = frame_state.offset
        live.update(frame_state.renderable, refresh=True)

    with posix_input_mode(), mouse_wheel_mode(), Live(frame_state.renderable, console=console, screen=True, auto_refresh=False) as live:
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
                if key in {"\t", "tab"}:
                    stats_range_index = (stats_range_index + 1) % len(STATS_TIME_RANGES)
                    page = 1
                    frame_offset = 0
                    refresh()
                    continue
                if key in {"up", "scroll_up", "down", "scroll_down", "page_up", "page_down", "home", "end"} and frame_state.max_offset:
                    frame_offset = content_scroll_offset(key, frame_offset, frame_state.max_offset, frame_state.viewport_height)
                    refresh()
                    continue
                if key in {"left", "p", "P"}:
                    page = max(1, page - 1)
                    frame_offset = 0
                    refresh()
                    continue
                if key in {"right", "n", "N"}:
                    page += 1
                    frame_offset = 0
                    refresh()
                    continue
                time.sleep(0.05)
            refresh()


def key_stats_header_renderable(model_id: str, key_name: str) -> Panel:
    return section_panel(
        Align.center(f"模型: [bold]{short_text(model_id, 32)}[/bold]    Key: [bold]{short_text(key_name, 32)}[/bold]"),
        "Key 统计",
        "cyan",
        "[dim]查看单个 Key 的调用统计[/dim]",
    )


def key_stats_help_text() -> Align:
    if sys.platform != "win32":
        return shortcut_text("Tab 时间范围  ·  ↑/↓/Pg 滚动  ·  ←/→ 翻页  ·  q 返回")
    return shortcut_text("Tab 时间范围  ·  ↑/↓/滚轮/Pg 滚动  ·  ←/→ 翻页  ·  q 返回")
    return next((caller_type for page_id, _, caller_type in STATS_CALLER_PAGES if page_id == page), None)


def stats_filter_query(range_parameters: tuple[str, ...], caller_type: str | None, has_caller_type: bool) -> tuple[str, tuple[str, ...]]:
    filters: list[str] = []
    parameters: list[str] = []
    if range_parameters:
        filters.append("created_at >= ?")
        parameters.extend(range_parameters)
    if caller_type and has_caller_type:
        filters.append("caller_type = ?")
        parameters.append(caller_type)
    elif caller_type == "visitor":
        filters.append("1 = 0")
    return (f"WHERE {' AND '.join(filters)}" if filters else ""), tuple(parameters)


def caller_type_title(caller_type: str | None) -> str:
    return {"local": "本地调用", "visitor": "访客调用"}.get(caller_type, "全部调用")


def caller_type_text(caller_type: str) -> str:
    return {"local": "本地", "visitor": "访客"}.get(caller_type, caller_type or "本地")


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


def request_stats_summary_renderable(summary: sqlite3.Row, status_rows: list[sqlite3.Row], rate_summary: sqlite3.Row | None = None) -> Table:
    requests = int(summary["requests"])
    successes = int(summary["successes"])
    failures = int(summary["failures"])
    retries = int(summary["retries"])
    prompt_tokens = int(summary["prompt_tokens"])
    completion_tokens = int(summary["completion_tokens"])
    total_tokens = int(summary["total_tokens"])
    cached_tokens = int(summary["cached_tokens"])
    avg_first_token_ms = int(summary["avg_first_token_ms"])
    max_first_token_ms = int(summary["max_first_token_ms"])
    avg_duration_ms = int(summary["avg_duration_ms"])
    max_duration_ms = int(summary["max_duration_ms"])
    current_rpm = int(rate_summary["rpm"]) if rate_summary is not None else 0
    current_tpm = int(rate_summary["tpm"]) if rate_summary is not None else 0
    rate_window_label = f"近{RATE_WINDOW_SECONDS // 60}分钟" if RATE_WINDOW_SECONDS % 60 == 0 else f"近{RATE_WINDOW_SECONDS}秒"
    status_codes = ", ".join(f"{row['status_code']}:{row['total']}" for row in status_rows) or "-"
    table = Table(show_header=False, show_lines=False, box=None, expand=True)
    table.add_column("指标", style="cyan")
    table.add_column("值", justify="right")
    table.add_column("指标", style="cyan")
    table.add_column("值", justify="right")
    table.add_column("指标", style="cyan")
    table.add_column("值", justify="right")
    table.add_row("总请求", str(requests), "成功率", percent(successes, requests), "成功/失败", f"{successes}/{failures}")
    table.add_row("重试", f"{retries} ({percent(retries, requests)})", "输入/输出 Tok", f"{abbreviate_number(prompt_tokens)}/{abbreviate_number(completion_tokens)}", "缓存 Tok", f"{abbreviate_number(cached_tokens)} ({percent(cached_tokens, prompt_tokens)})")
    table.add_row(f"{rate_window_label} 流量", f"{current_rpm} RPM / {abbreviate_number(current_tpm)} TPM", "缓存 Tok 比例", percent(cached_tokens, prompt_tokens), "状态码", short_text(status_codes, 36))
    table.add_row("平均/最长首字", f"{avg_first_token_ms}/{max_first_token_ms} ms", "平均/最长耗时", f"{avg_duration_ms}/{max_duration_ms} ms", "最新请求", short_text(summary["latest_request_at"] or "-", 19))
    return table
