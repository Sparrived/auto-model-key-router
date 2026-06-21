from __future__ import annotations

import asyncio
import sqlite3
from collections import Counter
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo
from typing import Any


BEIJING_TZ = ZoneInfo("Asia/Shanghai")
RATE_WINDOW_SECONDS = 60


@dataclass
class UsageStats:
    requests: int = 0
    successes: int = 0
    failures: int = 0
    retries: int = 0
    prompt_tokens: int = 0
    completion_tokens: int = 0
    total_tokens: int = 0
    cached_tokens: int = 0
    cache_creation_input_tokens: int = 0
    cache_read_input_tokens: int = 0
    total_duration_ms: int = 0
    min_duration_ms: int | None = None
    max_duration_ms: int = 0
    total_first_token_ms: int = 0
    min_first_token_ms: int | None = None
    max_first_token_ms: int = 0
    status_codes: Counter[str] = field(default_factory=Counter)

    def to_dict(self) -> dict[str, Any]:
        return {
            "requests": self.requests,
            "successes": self.successes,
            "failures": self.failures,
            "retries": self.retries,
            "prompt_tokens": self.prompt_tokens,
            "completion_tokens": self.completion_tokens,
            "total_tokens": self.total_tokens,
            "cached_tokens": self.cached_tokens,
            "cache_creation_input_tokens": self.cache_creation_input_tokens,
            "cache_read_input_tokens": self.cache_read_input_tokens,
            "cached_token_rate": _rate(self.cached_tokens, self.prompt_tokens),
            "total_duration_ms": self.total_duration_ms,
            "avg_duration_ms": round(self.total_duration_ms / self.requests)
            if self.requests
            else 0,
            "min_duration_ms": self.min_duration_ms or 0,
            "max_duration_ms": self.max_duration_ms,
            "total_first_token_ms": self.total_first_token_ms,
            "avg_first_token_ms": round(self.total_first_token_ms / self.requests)
            if self.requests
            else 0,
            "min_first_token_ms": self.min_first_token_ms or 0,
            "max_first_token_ms": self.max_first_token_ms,
            "status_codes": dict(sorted(self.status_codes.items())),
        }


class MetricsStore:
    def __init__(self, database_path: str | Path) -> None:
        self.database_path = Path(database_path)
        if self.database_path.parent != Path(""):
            self.database_path.parent.mkdir(parents=True, exist_ok=True)
        self._started_at = _now_beijing()
        self._lock = asyncio.Lock()
        self._active_count = 0
        self._connection = sqlite3.connect(self.database_path, check_same_thread=False)
        self._connection.row_factory = sqlite3.Row
        self._closed = False
        self.on_record: Callable[[], Awaitable[None]] | None = None
        self._configure_connection()
        self._init_schema()

    def acquire_active(self) -> None:
        self._active_count += 1

    def release_active(self) -> None:
        self._active_count = max(self._active_count - 1, 0)

    @property
    def active_count(self) -> int:
        return self._active_count

    async def record(
        self,
        model_id: str,
        key_name: str,
        status_code: int | None,
        usage: dict[str, Any] | None = None,
        retried: bool = False,
        failed: bool = False,
        duration_ms: int = 0,
        first_token_ms: int = 0,
        requested_model_id: str | None = None,
        caller_type: str = "local",
    ) -> None:
        usage = _normalize_usage(usage or {})
        request_model_id = requested_model_id or model_id
        caller_type = caller_type if caller_type in {"local", "visitor"} else "local"
        failure = failed or status_code is None or status_code >= 400
        async with self._lock:
            await asyncio.to_thread(
                self._record_sync,
                model_id,
                key_name,
                status_code,
                usage,
                retried,
                failure,
                duration_ms,
                first_token_ms,
                request_model_id,
                caller_type,
            )
        if self.on_record is not None:
            await self.on_record()

    def _record_sync(
        self,
        model_id: str,
        key_name: str,
        status_code: int | None,
        usage: dict[str, int],
        retried: bool,
        failure: bool,
        duration_ms: int,
        first_token_ms: int,
        request_model_id: str,
        caller_type: str,
    ) -> None:
        self._connection.execute(
            """
                INSERT INTO request_metrics (
                    created_at,
                    caller_type,
                    model_id,
                    requested_model_id,
                    key_name,
                    status_code,
                    success,
                    retried,
                    prompt_tokens,
                    completion_tokens,
                    total_tokens,
                    cached_tokens,
                    cache_creation_input_tokens,
                    cache_read_input_tokens,
                    first_token_ms,
                    duration_ms
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
            (
                _now_beijing().isoformat(),
                caller_type,
                model_id,
                request_model_id,
                key_name,
                status_code,
                0 if failure else 1,
                1 if retried else 0,
                usage["prompt_tokens"],
                usage["completion_tokens"],
                usage["total_tokens"],
                usage["cached_tokens"],
                usage["cache_creation_input_tokens"],
                usage["cache_read_input_tokens"],
                max(first_token_ms, 0),
                max(duration_ms, 0),
            ),
        )
        self._connection.commit()

    async def snapshot(self, hours: float | None = None) -> dict[str, Any]:
        async with self._lock:
            return await asyncio.to_thread(self._snapshot_sync, hours)

    async def key_stats(
        self,
        model_id: str,
        key_name: str,
        hours: float | None = None,
    ) -> dict[str, Any]:
        async with self._lock:
            return await asyncio.to_thread(
                self._key_stats_sync, model_id, key_name, hours
            )

    def _key_stats_sync(
        self,
        model_id: str,
        key_name: str,
        hours: float | None = None,
    ) -> dict[str, Any]:
        since: str | None = None
        if hours is not None:
            since = (_now_beijing() - timedelta(hours=hours)).isoformat()

        stats = self._query_key_stats(model_id, key_name, since)

        current_window_started_at = (
            _now_beijing() - timedelta(seconds=RATE_WINDOW_SECONDS)
        ).isoformat()
        rate = self._query_key_stats(model_id, key_name, current_window_started_at)

        where_parts = ["model_id = ?", "key_name = ?"]
        params: list[str | None] = [model_id, key_name]
        if since is not None:
            where_parts.append("created_at >= ?")
            params.append(since)
        where_sql = " AND ".join(where_parts)
        recent_rows = self._connection.execute(
            f"""
            SELECT created_at, status_code, success, retried,
                   prompt_tokens, completion_tokens, total_tokens,
                   cached_tokens, first_token_ms, duration_ms
            FROM request_metrics
            WHERE {where_sql}
            ORDER BY id DESC LIMIT 50
            """,
            params,
        ).fetchall()

        return {
            "model_id": model_id,
            "key_name": key_name,
            "stats": stats.to_dict(),
            "current_rpm": rate.requests,
            "current_tpm": rate.total_tokens,
            "recent_requests": [
                {
                    "created_at": row["created_at"],
                    "status_code": row["status_code"],
                    "success": row["success"],
                    "retried": row["retried"],
                    "prompt_tokens": row["prompt_tokens"],
                    "completion_tokens": row["completion_tokens"],
                    "total_tokens": row["total_tokens"],
                    "cached_tokens": row["cached_tokens"],
                    "first_token_ms": row["first_token_ms"],
                    "duration_ms": row["duration_ms"],
                }
                for row in recent_rows
            ],
        }

    def _query_key_stats(
        self,
        model_id: str,
        key_name: str,
        since_created_at: str | None = None,
    ) -> UsageStats:
        where_parts = ["model_id = ?", "key_name = ?"]
        params: list[str | None] = [model_id, key_name]
        if since_created_at is not None:
            where_parts.append("created_at >= ?")
            params.append(since_created_at)
        where_sql = " AND ".join(where_parts)
        row = self._connection.execute(
            f"""
            SELECT
                COUNT(*) AS requests,
                COALESCE(SUM(success), 0) AS successes,
                COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) AS failures,
                COALESCE(SUM(retried), 0) AS retries,
                COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
                COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
                COALESCE(SUM(total_tokens), 0) AS total_tokens,
                COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
                COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
                COALESCE(SUM(cache_read_input_tokens), 0) AS cache_read_input_tokens,
                COALESCE(SUM(duration_ms), 0) AS total_duration_ms,
                MIN(duration_ms) AS min_duration_ms,
                COALESCE(MAX(duration_ms), 0) AS max_duration_ms,
                COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms,
                MIN(first_token_ms) AS min_first_token_ms,
                COALESCE(MAX(first_token_ms), 0) AS max_first_token_ms
            FROM request_metrics
            WHERE {where_sql}
            """,
            params,
        ).fetchone()
        stats = _stats_from_aggregate(row)

        status_params: list[str | None] = [model_id, key_name]
        status_where = "model_id = ? AND key_name = ? AND status_code IS NOT NULL"
        if since_created_at is not None:
            status_where += " AND created_at >= ?"
            status_params.append(since_created_at)
        status_rows = self._connection.execute(
            f"""
            SELECT status_code, COUNT(*) AS total
            FROM request_metrics
            WHERE {status_where}
            GROUP BY status_code ORDER BY status_code
            """,
            status_params,
        ).fetchall()
        for status_row in status_rows:
            stats.status_codes[str(status_row["status_code"])] = int(
                status_row["total"]
            )
        return stats

    def _snapshot_sync(self, hours: float | None = None) -> dict[str, Any]:
        since: str | None = None
        if hours is not None:
            since = (_now_beijing() - timedelta(hours=hours)).isoformat()

        total = self._query_stats((), since_created_at=since)[()]
        current_window_started_at = (
            _now_beijing() - timedelta(seconds=RATE_WINDOW_SECONDS)
        ).isoformat()
        recent = self._query_stats((), since_created_at=current_window_started_at)[()]
        caller_types = self._query_stats(("caller_type",), since_created_at=since)
        caller_types.setdefault(("local",), UsageStats())
        caller_types.setdefault(("visitor",), UsageStats())
        models = self._query_stats(("model_id",), since_created_at=since)
        requested_models = self._query_stats(("requested_model_id",), since_created_at=since)
        model_requested = self._query_stats(("model_id", "requested_model_id"), since_created_at=since)
        keys = self._query_stats(("model_id", "key_name"), since_created_at=since)

        return {
            "started_at": self._started_at.isoformat(),
            "database_path": str(self.database_path),
            "rate_window_seconds": RATE_WINDOW_SECONDS,
            "current_rpm": recent.requests,
            "current_tpm": recent.total_tokens,
            "router_status": _router_status(recent),
            "active_requests": self._active_count,
            "total": total.to_dict(),
            "caller_types": {
                key[0]: stats.to_dict() for key, stats in caller_types.items()
            },
            "models": {key[0]: stats.to_dict() for key, stats in models.items()},
            "requested_models": {
                key[0]: stats.to_dict() for key, stats in requested_models.items()
            },
            "model_requested_models": _nested_stats(model_requested),
            "keys": _nested_stats(keys),
        }

    def _query_stats(
        self, dimensions: tuple[str, ...], since_created_at: str | None = None
    ) -> dict[tuple[str, ...], UsageStats]:
        dimension_sql = ", ".join(dimensions)
        select_prefix = f"{dimension_sql}, " if dimension_sql else ""
        where_sql = " WHERE created_at >= ?" if since_created_at is not None else ""
        parameters = (since_created_at,) if since_created_at is not None else ()
        group_sql = f" GROUP BY {dimension_sql}" if dimension_sql else ""
        order_sql = f" ORDER BY {dimension_sql}" if dimension_sql else ""
        rows = self._connection.execute(
            f"""
            SELECT {select_prefix}
                COUNT(*) AS requests,
                COALESCE(SUM(success), 0) AS successes,
                COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) AS failures,
                COALESCE(SUM(retried), 0) AS retries,
                COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
                COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
                COALESCE(SUM(total_tokens), 0) AS total_tokens,
                COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
                COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
                COALESCE(SUM(cache_read_input_tokens), 0) AS cache_read_input_tokens,
                COALESCE(SUM(duration_ms), 0) AS total_duration_ms,
                MIN(duration_ms) AS min_duration_ms,
                COALESCE(MAX(duration_ms), 0) AS max_duration_ms,
                COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms,
                MIN(first_token_ms) AS min_first_token_ms,
                COALESCE(MAX(first_token_ms), 0) AS max_first_token_ms
            FROM request_metrics{where_sql}{group_sql}{order_sql}
            """,
            parameters,
        ).fetchall()
        result: dict[tuple[str, ...], UsageStats] = {}
        for row in rows:
            key = tuple(str(row[dimension]) for dimension in dimensions)
            result[key] = _stats_from_aggregate(row)
        if not dimensions and not result:
            result[()] = UsageStats()

        status_where_sql = (
            " WHERE status_code IS NOT NULL"
            if since_created_at is None
            else " WHERE created_at >= ? AND status_code IS NOT NULL"
        )
        status_rows = self._connection.execute(
            f"""
            SELECT {select_prefix}status_code, COUNT(*) AS total
            FROM request_metrics
            {status_where_sql}
            {group_sql + (", status_code" if group_sql else " GROUP BY status_code")}
            {order_sql + (", status_code" if order_sql else " ORDER BY status_code")}
            """,
            parameters,
        ).fetchall()
        for row in status_rows:
            key = tuple(str(row[dimension]) for dimension in dimensions)
            result.setdefault(key, UsageStats()).status_codes[
                str(row["status_code"])
            ] = int(row["total"])
        return result

    async def close(self) -> None:
        async with self._lock:
            if self._closed:
                return
            await asyncio.to_thread(self._connection.close)
            self._closed = True

    def _configure_connection(self) -> None:
        self._connection.execute("PRAGMA journal_mode=WAL")
        self._connection.execute("PRAGMA synchronous=NORMAL")
        self._connection.execute("PRAGMA busy_timeout=5000")

    def _init_schema(self) -> None:
        self._connection.execute(
            """
            CREATE TABLE IF NOT EXISTS request_metrics (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                created_at TEXT NOT NULL,
                caller_type TEXT NOT NULL DEFAULT 'local',
                model_id TEXT NOT NULL,
                requested_model_id TEXT NOT NULL,
                key_name TEXT NOT NULL,
                status_code INTEGER,
                success INTEGER NOT NULL,
                retried INTEGER NOT NULL,
                prompt_tokens INTEGER NOT NULL DEFAULT 0,
                completion_tokens INTEGER NOT NULL DEFAULT 0,
                total_tokens INTEGER NOT NULL DEFAULT 0,
                cached_tokens INTEGER NOT NULL DEFAULT 0,
                cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
                cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
                first_token_ms INTEGER NOT NULL DEFAULT 0,
                duration_ms INTEGER NOT NULL DEFAULT 0
            )
            """
        )
        self._ensure_column("caller_type", "TEXT NOT NULL DEFAULT 'local'")
        self._connection.execute(
            "UPDATE request_metrics SET caller_type = 'local' WHERE caller_type IS NULL OR caller_type NOT IN ('local', 'visitor')"
        )
        self._ensure_column("requested_model_id", "TEXT NOT NULL DEFAULT ''")
        self._connection.execute(
            "UPDATE request_metrics SET requested_model_id = model_id WHERE requested_model_id = ''"
        )
        self._ensure_column("cached_tokens", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("cache_creation_input_tokens", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("cache_read_input_tokens", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("first_token_ms", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("duration_ms", "INTEGER NOT NULL DEFAULT 0")
        self._connection.execute(
            "CREATE INDEX IF NOT EXISTS idx_request_metrics_model ON request_metrics(model_id)"
        )
        self._connection.execute(
            "CREATE INDEX IF NOT EXISTS idx_request_metrics_requested_model ON request_metrics(requested_model_id)"
        )
        self._connection.execute(
            "CREATE INDEX IF NOT EXISTS idx_request_metrics_key ON request_metrics(model_id, key_name)"
        )
        self._connection.execute(
            "CREATE INDEX IF NOT EXISTS idx_request_metrics_created ON request_metrics(created_at)"
        )
        self._connection.execute(
            "CREATE INDEX IF NOT EXISTS idx_request_metrics_caller ON request_metrics(caller_type, created_at)"
        )
        self._connection.commit()

    def _ensure_column(self, name: str, definition: str) -> None:
        columns = {
            row["name"]
            for row in self._connection.execute(
                "PRAGMA table_info(request_metrics)"
            ).fetchall()
        }
        if name not in columns:
            self._connection.execute(
                f"ALTER TABLE request_metrics ADD COLUMN {name} {definition}"
            )


def extract_usage(data: Any) -> dict[str, Any] | None:
    if not isinstance(data, dict):
        return None
    usage = data.get("usage")
    if isinstance(usage, dict):
        return usage
    message = data.get("message")
    if isinstance(message, dict) and isinstance(message.get("usage"), dict):
        return message["usage"]
    return None


def _now_beijing() -> datetime:
    return datetime.now(BEIJING_TZ)


def _stats_from_aggregate(row: sqlite3.Row) -> UsageStats:
    requests = int(row["requests"])
    return UsageStats(
        requests=requests,
        successes=int(row["successes"]),
        failures=int(row["failures"]),
        retries=int(row["retries"]),
        prompt_tokens=int(row["prompt_tokens"]),
        completion_tokens=int(row["completion_tokens"]),
        total_tokens=int(row["total_tokens"]),
        cached_tokens=int(row["cached_tokens"]),
        cache_creation_input_tokens=int(row["cache_creation_input_tokens"]),
        cache_read_input_tokens=int(row["cache_read_input_tokens"]),
        total_duration_ms=int(row["total_duration_ms"]),
        min_duration_ms=None
        if row["min_duration_ms"] is None
        else int(row["min_duration_ms"]),
        max_duration_ms=int(row["max_duration_ms"]),
        total_first_token_ms=int(row["total_first_token_ms"]),
        min_first_token_ms=None
        if row["min_first_token_ms"] is None
        else int(row["min_first_token_ms"]),
        max_first_token_ms=int(row["max_first_token_ms"]),
    )


def _nested_stats(
    stats: dict[tuple[str, ...], UsageStats],
) -> dict[str, dict[str, dict[str, Any]]]:
    nested: dict[str, dict[str, dict[str, Any]]] = {}
    for (parent, child), value in stats.items():
        nested.setdefault(parent, {})[child] = value.to_dict()
    return nested


def _normalize_usage(usage: dict[str, Any]) -> dict[str, int]:
    prompt_tokens = _int_value(usage.get("prompt_tokens"))
    input_tokens = _int_value(usage.get("input_tokens"))
    completion_tokens = _int_value(usage.get("completion_tokens")) or _int_value(
        usage.get("output_tokens")
    )
    prompt_details = usage.get("prompt_tokens_details")
    if not isinstance(prompt_details, dict):
        prompt_details = {}
    input_details = usage.get("input_tokens_details")
    if not isinstance(input_details, dict):
        input_details = {}
    cache_read_input_tokens = _int_value(
        usage.get("cache_read_input_tokens")
    ) or _int_value(input_details.get("cache_read_input_tokens"))
    cache_creation_input_tokens = _int_value(
        usage.get("cache_creation_input_tokens")
    ) or _int_value(input_details.get("cache_creation_input_tokens"))
    cached_tokens = (
        _int_value(usage.get("cached_tokens"))
        or _int_value(prompt_details.get("cached_tokens"))
        or _int_value(input_details.get("cached_tokens"))
        or cache_read_input_tokens
    )
    # Anthropic: input_tokens excludes cached tokens, add them back
    if input_tokens and not prompt_tokens:
        prompt_tokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens
    total_tokens = (
        _int_value(usage.get("total_tokens")) or prompt_tokens + completion_tokens
    )
    return {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": total_tokens,
        "cached_tokens": cached_tokens,
        "cache_creation_input_tokens": cache_creation_input_tokens,
        "cache_read_input_tokens": cache_read_input_tokens,
    }


def _int_value(value: Any) -> int:
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str) and value.isdigit():
        return int(value)
    return 0


def _rate(numerator: int, denominator: int) -> float:
    if denominator <= 0:
        return 0.0
    return round(numerator / denominator, 6)


def _router_status(recent: UsageStats) -> str:
    """从最近 60 秒指标推导路由器当前健康状态：green / yellow / red。"""
    if recent.requests == 0:
        return "green"
    success_rate = recent.successes / recent.requests
    retry_rate = recent.retries / recent.requests
    if success_rate < 0.80:
        return "red"
    if success_rate < 0.95 or retry_rate > 0.5:
        return "yellow"
    return "green"
