from __future__ import annotations

import asyncio
import sqlite3
from collections import Counter
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
    cache_hits: int = 0
    cache_misses: int = 0
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
            "cache_hits": self.cache_hits,
            "cache_misses": self.cache_misses,
            "cache_hit_rate": _rate(
                self.cache_hits, self.cache_hits + self.cache_misses
            ),
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
        self._connection = sqlite3.connect(self.database_path, check_same_thread=False)
        self._connection.row_factory = sqlite3.Row
        self._closed = False
        self._configure_connection()
        self._init_schema()

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
        has_cache_hit = (
            usage["cached_tokens"] > 0 or usage["cache_read_input_tokens"] > 0
        )
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
                has_cache_hit,
            )

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
        has_cache_hit: bool,
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
                    cache_hit,
                    first_token_ms,
                    duration_ms
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
                1 if has_cache_hit else 0,
                max(first_token_ms, 0),
                max(duration_ms, 0),
            ),
        )
        self._connection.commit()

    async def snapshot(self) -> dict[str, Any]:
        async with self._lock:
            return await asyncio.to_thread(self._snapshot_sync)

    def _snapshot_sync(self) -> dict[str, Any]:
        total = self._query_stats(())[()]
        current_window_started_at = (
            _now_beijing() - timedelta(seconds=RATE_WINDOW_SECONDS)
        ).isoformat()
        recent = self._query_stats((), since_created_at=current_window_started_at)[()]
        caller_types = self._query_stats(("caller_type",))
        caller_types.setdefault(("local",), UsageStats())
        caller_types.setdefault(("visitor",), UsageStats())
        models = self._query_stats(("model_id",))
        requested_models = self._query_stats(("requested_model_id",))
        model_requested = self._query_stats(("model_id", "requested_model_id"))
        keys = self._query_stats(("model_id", "key_name"))

        return {
            "started_at": self._started_at.isoformat(),
            "database_path": str(self.database_path),
            "rate_window_seconds": RATE_WINDOW_SECONDS,
            "current_rpm": recent.requests,
            "current_tpm": recent.total_tokens,
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
                COALESCE(SUM(cache_hit), 0) AS cache_hits,
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
                cache_hit INTEGER NOT NULL DEFAULT 0,
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
        self._ensure_column("cache_hit", "INTEGER NOT NULL DEFAULT 0")
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
        cache_hits=int(row["cache_hits"]),
        cache_misses=requests - int(row["cache_hits"]),
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
    prompt_tokens = _int_value(usage.get("prompt_tokens")) or _int_value(
        usage.get("input_tokens")
    )
    completion_tokens = _int_value(usage.get("completion_tokens")) or _int_value(
        usage.get("output_tokens")
    )
    total_tokens = (
        _int_value(usage.get("total_tokens")) or prompt_tokens + completion_tokens
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
