from __future__ import annotations

import sqlite3
import time
from pathlib import Path

from rich import box
from rich.align import Align
from rich.console import Group
from rich.panel import Panel
from rich.table import Table
from rich.text import Text

from .formatting import percent, short_text
from .tui import console, key_pressed, page_title, section_panel, shortcut_text


def render_logs(database_path: str, log_file_path: str, limit: int) -> None:
    console.print(service_logs_renderable(log_file_path, max(limit, 1), 0))
    console.print(request_stats_renderable(database_path, 1, max(limit, 1)))


def watch_logs(database_path: str, log_file_path: str, limit: int) -> None:
    page = "logs"
    log_offset = 0
    stats_page = 1
    with console.screen():
        from rich.live import Live

        with Live(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), console=console, screen=True, auto_refresh=False) as live:
            while True:
                started = time.monotonic()
                while time.monotonic() - started < 1:
                    key = key_pressed()
                    if key in {"q", "Q", "0", "cancel"}:
                        return
                    if key in {"1", "l", "L"}:
                        page = "logs"
                        live.update(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), refresh=True)
                        continue
                    if key in {"2", "s", "S"}:
                        page = "stats"
                        live.update(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), refresh=True)
                        continue
                    if page == "logs" and key in {"up", "page_up", "k", "K"}:
                        log_offset += max(limit, 1) if key == "page_up" else 1
                        live.update(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), refresh=True)
                        continue
                    if page == "logs" and key in {"down", "page_down", "j", "J"}:
                        log_offset = max(0, log_offset - (max(limit, 1) if key == "page_down" else 1))
                        live.update(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), refresh=True)
                        continue
                    if page == "logs" and key in {"home", "g", "G"}:
                        log_offset = 0
                        live.update(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), refresh=True)
                        continue
                    if page == "stats" and key in {"left", "page_up", "up", "p", "P"}:
                        stats_page = max(1, stats_page - 1)
                        live.update(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), refresh=True)
                        continue
                    if page == "stats" and key in {"right", "page_down", "down", "n", "N"}:
                        stats_page += 1
                        live.update(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), refresh=True)
                        continue
                    time.sleep(0.05)
                live.update(render_live_logs(database_path, log_file_path, limit, page, log_offset, stats_page), refresh=True)


def render_live_logs(database_path: str, log_file_path: str, limit: int, page: str, log_offset: int, stats_page: int) -> Group:
    page_size = log_page_size(limit) if page == "logs" else max(limit, 1)
    content = service_logs_renderable(log_file_path, page_size, log_offset) if page == "logs" else request_stats_renderable(database_path, stats_page, page_size)
    return Group(page_title("日志板块", "运行日志与调用统计"), log_tabs_renderable(page), content, log_help_text(page))


def log_page_size(limit: int) -> int:
    reserved_rows = 10
    return max(1, min(max(limit, 1), console.size.height - reserved_rows))


def log_tabs_renderable(page: str) -> Panel:
    logs_label = "[bold black on cyan] 1 运行日志 [/bold black on cyan]" if page == "logs" else "[dim]1[/dim] 运行日志"
    stats_label = "[bold black on cyan] 2 调用统计 [/bold black on cyan]" if page == "stats" else "[dim]2[/dim] 调用统计"
    return section_panel(Align.center(f"{logs_label}    {stats_label}"), "页面", "cyan")


def log_help_text(page: str) -> Align:
    if page == "logs":
        return shortcut_text("1 日志  ·  2 统计  ·  ↑/↓ 滚动  ·  Pg 翻页  ·  q 返回")
    return shortcut_text("1 日志  ·  2 统计  ·  ←/→ 翻页  ·  q 返回")


def service_logs_renderable(log_file_path: str, limit: int, offset: int = 0) -> Panel:
    path = Path(log_file_path)
    if not path.exists():
        return section_panel(f"[yellow]运行日志不存在: {path}[/yellow]", "运行日志", "yellow")
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    total = len(lines)
    page_size = max(limit, 1)
    max_offset = max(total - page_size, 0)
    offset = min(max(offset, 0), max_offset)
    end = total - offset
    start = max(end - page_size, 0)
    content = Text("\n".join(lines[start:end]) if lines[start:end] else "暂无运行日志", no_wrap=True, overflow="ellipsis")
    title = "运行日志" if total == 0 else f"运行日志 第 {start + 1}-{end} 行 / 共 {total} 行"
    return section_panel(content, title, "blue")


def request_stats_renderable(database_path: str, page: int, page_size: int) -> Group | Panel:
    path = Path(database_path)
    if not path.exists():
        return section_panel(f"[yellow]统计数据库不存在: {path}[/yellow]", "调用统计", "yellow")
    page_size = max(page_size, 1)
    page = max(page, 1)
    offset = (page - 1) * page_size
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
        total = connection.execute("SELECT COUNT(*) AS total FROM request_metrics").fetchone()["total"]
        summary = connection.execute(f"""
            SELECT COUNT(*) AS requests, COALESCE(SUM(success), 0) AS successes, COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) AS failures,
            COALESCE(SUM(retried), 0) AS retries, COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
            COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM({cached_tokens_sum_expr}), 0) AS cached_tokens, COALESCE(SUM({cache_hit_sum_expr}), 0) AS cache_hits,
            COALESCE(SUM({first_token_ms_sum_expr}), 0) AS total_first_token_ms, COALESCE(ROUND(AVG({first_token_ms_sum_expr})), 0) AS avg_first_token_ms, COALESCE(MAX({first_token_ms_sum_expr}), 0) AS max_first_token_ms,
            COALESCE(SUM({duration_ms_sum_expr}), 0) AS total_duration_ms, COALESCE(ROUND(AVG({duration_ms_sum_expr})), 0) AS avg_duration_ms, COALESCE(MAX({duration_ms_sum_expr}), 0) AS max_duration_ms,
            COUNT(DISTINCT model_id) AS model_count, COUNT(DISTINCT key_name) AS key_count, MIN(created_at) AS first_request_at, MAX(created_at) AS latest_request_at
            FROM request_metrics
        """).fetchone()
        status_rows = connection.execute("SELECT status_code, COUNT(*) AS total FROM request_metrics WHERE status_code IS NOT NULL GROUP BY status_code ORDER BY status_code").fetchall()
        max_page = max((total + page_size - 1) // page_size, 1)
        page = min(page, max_page)
        offset = (page - 1) * page_size
        rows = connection.execute(f"SELECT created_at, model_id, key_name, status_code, success, retried, prompt_tokens, completion_tokens, total_tokens, {cached_tokens_expr}, {first_token_ms_expr}, {duration_ms_expr} FROM request_metrics ORDER BY id DESC LIMIT ? OFFSET ?", (page_size, offset)).fetchall()
    for row in rows:
        table.add_row(short_text(row["created_at"], 19), short_text(row["model_id"], 22), short_text(row["key_name"], 18), "-" if row["status_code"] is None else str(row["status_code"]), "是" if row["success"] else "否", "是" if row["retried"] else "否", str(row["prompt_tokens"]), str(row["completion_tokens"]), str(row["total_tokens"]), str(row["cached_tokens"]), str(row["first_token_ms"]), str(row["duration_ms"]))
    if not rows:
        table.add_row("暂无", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-")
    details_title = f"请求明细 · 第 {page}/{max_page} 页 · {page_size}/页 · 共 {total} 条"
    return Group(section_panel(request_stats_summary_renderable(summary, status_rows), "总览", "cyan"), section_panel(table, details_title, "blue"))


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
    total_first_token_ms = int(summary["total_first_token_ms"])
    avg_first_token_ms = int(summary["avg_first_token_ms"])
    max_first_token_ms = int(summary["max_first_token_ms"])
    total_duration_ms = int(summary["total_duration_ms"])
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
    table.add_row("缓存 Tok", f"{cached_tokens} ({percent(cached_tokens, prompt_tokens)})", "缓存命中", f"{cache_hits} ({percent(cache_hits, requests)})", "模型/Key 数", f"{summary['model_count']}/{summary['key_count']}")
    table.add_row("平均/最长首字", f"{avg_first_token_ms}/{max_first_token_ms} ms", "首字总耗时", f"{total_first_token_ms} ms", "状态码", short_text(status_codes, 36))
    table.add_row("平均/最长耗时", f"{avg_duration_ms}/{max_duration_ms} ms", "总耗时", f"{total_duration_ms} ms", "首条请求", short_text(summary["first_request_at"] or "-", 19))
    table.add_row("最新请求", short_text(summary["latest_request_at"] or "-", 19), "", "", "", "")
    return table
