from __future__ import annotations

import asyncio
import sqlite3
from collections import Counter
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo
from typing import Any


BEIJING_TZ = ZoneInfo("Asia/Shanghai")


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
            "cache_hit_rate": _rate(self.cache_hits, self.cache_hits + self.cache_misses),
            "cached_token_rate": _rate(self.cached_tokens, self.prompt_tokens),
            "total_duration_ms": self.total_duration_ms,
            "avg_duration_ms": round(self.total_duration_ms / self.requests) if self.requests else 0,
            "min_duration_ms": self.min_duration_ms or 0,
            "max_duration_ms": self.max_duration_ms,
            "total_first_token_ms": self.total_first_token_ms,
            "avg_first_token_ms": round(self.total_first_token_ms / self.requests) if self.requests else 0,
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
    ) -> None:
        usage = _normalize_usage(usage or {})
        request_model_id = requested_model_id or model_id
        failure = failed or status_code is None or status_code >= 400
        has_cache_hit = usage["cached_tokens"] > 0 or usage["cache_read_input_tokens"] > 0
        async with self._lock:
            self._connection.execute(
                """
                INSERT INTO request_metrics (
                    created_at,
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
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    _now_beijing().isoformat(),
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
            rows = self._connection.execute(
                """
                SELECT model_id, requested_model_id, key_name, status_code, success, retried, prompt_tokens, completion_tokens, total_tokens,
                    cached_tokens, cache_creation_input_tokens, cache_read_input_tokens, cache_hit, first_token_ms, duration_ms
                FROM request_metrics
                ORDER BY id ASC
                """
            ).fetchall()

        total = UsageStats()
        models: dict[str, UsageStats] = {}
        requested_models: dict[str, UsageStats] = {}
        model_requested: dict[str, dict[str, UsageStats]] = {}
        keys: dict[str, dict[str, UsageStats]] = {}

        for row in rows:
            model_id = row["model_id"]
            requested_model_id = row["requested_model_id"]
            key_name = row["key_name"]
            model_stats = models.setdefault(model_id, UsageStats())
            requested_stats = requested_models.setdefault(requested_model_id, UsageStats())
            model_requested_stats = model_requested.setdefault(model_id, {}).setdefault(requested_model_id, UsageStats())
            key_stats = keys.setdefault(model_id, {}).setdefault(key_name, UsageStats())
            for stats in (total, model_stats, requested_stats, model_requested_stats, key_stats):
                _add_row(stats, row)

        return {
            "started_at": self._started_at.isoformat(),
            "database_path": str(self.database_path),
            "total": total.to_dict(),
            "models": {model_id: stats.to_dict() for model_id, stats in models.items()},
            "requested_models": {model_id: stats.to_dict() for model_id, stats in requested_models.items()},
            "model_requested_models": {
                model_id: {requested_id: stats.to_dict() for requested_id, stats in requested_stats.items()}
                for model_id, requested_stats in model_requested.items()
            },
            "keys": {
                model_id: {key_name: stats.to_dict() for key_name, stats in key_stats.items()}
                for model_id, key_stats in keys.items()
            },
        }

    async def close(self) -> None:
        async with self._lock:
            if self._closed:
                return
            self._connection.close()
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
        self._ensure_column("requested_model_id", "TEXT NOT NULL DEFAULT ''")
        self._connection.execute("UPDATE request_metrics SET requested_model_id = model_id WHERE requested_model_id = ''")
        self._ensure_column("cached_tokens", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("cache_creation_input_tokens", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("cache_read_input_tokens", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("cache_hit", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("first_token_ms", "INTEGER NOT NULL DEFAULT 0")
        self._ensure_column("duration_ms", "INTEGER NOT NULL DEFAULT 0")
        self._migrate_created_at_to_beijing()
        self._connection.execute("CREATE INDEX IF NOT EXISTS idx_request_metrics_model ON request_metrics(model_id)")
        self._connection.execute("CREATE INDEX IF NOT EXISTS idx_request_metrics_requested_model ON request_metrics(requested_model_id)")
        self._connection.execute("CREATE INDEX IF NOT EXISTS idx_request_metrics_key ON request_metrics(model_id, key_name)")
        self._connection.execute("CREATE INDEX IF NOT EXISTS idx_request_metrics_created ON request_metrics(created_at)")
        self._connection.commit()

    def _ensure_column(self, name: str, definition: str) -> None:
        columns = {row["name"] for row in self._connection.execute("PRAGMA table_info(request_metrics)").fetchall()}
        if name not in columns:
            self._connection.execute(f"ALTER TABLE request_metrics ADD COLUMN {name} {definition}")

    def _migrate_created_at_to_beijing(self) -> None:
        rows = self._connection.execute("SELECT id, created_at FROM request_metrics WHERE created_at LIKE '%+00:00' OR created_at LIKE '%Z'").fetchall()
        for row in rows:
            beijing_created_at = _to_beijing_iso(row["created_at"])
            if beijing_created_at != row["created_at"]:
                self._connection.execute("UPDATE request_metrics SET created_at = ? WHERE id = ?", (beijing_created_at, row["id"]))


def extract_usage(data: Any) -> dict[str, Any] | None:
    if not isinstance(data, dict):
        return None
    usage = data.get("usage")
    return usage if isinstance(usage, dict) else None


def _now_beijing() -> datetime:
    return datetime.now(BEIJING_TZ)


def _to_beijing_iso(value: str) -> str:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return value
    if parsed.tzinfo is None:
        return value
    return parsed.astimezone(BEIJING_TZ).isoformat()


def _add_row(stats: UsageStats, row: sqlite3.Row) -> None:
    stats.requests += 1
    stats.successes += int(row["success"])
    stats.failures += 0 if row["success"] else 1
    stats.retries += int(row["retried"])
    stats.prompt_tokens += int(row["prompt_tokens"])
    stats.completion_tokens += int(row["completion_tokens"])
    stats.total_tokens += int(row["total_tokens"])
    stats.cached_tokens += int(row["cached_tokens"])
    stats.cache_creation_input_tokens += int(row["cache_creation_input_tokens"])
    stats.cache_read_input_tokens += int(row["cache_read_input_tokens"])
    stats.cache_hits += int(row["cache_hit"])
    stats.cache_misses += 0 if row["cache_hit"] else 1
    duration_ms = int(row["duration_ms"])
    stats.total_duration_ms += duration_ms
    if stats.min_duration_ms is None or duration_ms < stats.min_duration_ms:
        stats.min_duration_ms = duration_ms
    if duration_ms > stats.max_duration_ms:
        stats.max_duration_ms = duration_ms
    first_token_ms = int(row["first_token_ms"])
    stats.total_first_token_ms += first_token_ms
    if stats.min_first_token_ms is None or first_token_ms < stats.min_first_token_ms:
        stats.min_first_token_ms = first_token_ms
    if first_token_ms > stats.max_first_token_ms:
        stats.max_first_token_ms = first_token_ms
    if row["status_code"] is not None:
        stats.status_codes[str(row["status_code"])] += 1


def _normalize_usage(usage: dict[str, Any]) -> dict[str, int]:
    prompt_tokens = _int_value(usage.get("prompt_tokens")) or _int_value(usage.get("input_tokens"))
    completion_tokens = _int_value(usage.get("completion_tokens")) or _int_value(usage.get("output_tokens"))
    total_tokens = _int_value(usage.get("total_tokens")) or prompt_tokens + completion_tokens
    prompt_details = usage.get("prompt_tokens_details")
    if not isinstance(prompt_details, dict):
        prompt_details = {}
    cached_tokens = _int_value(usage.get("cached_tokens")) or _int_value(prompt_details.get("cached_tokens"))
    cache_read_input_tokens = _int_value(usage.get("cache_read_input_tokens"))
    return {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": total_tokens,
        "cached_tokens": cached_tokens,
        "cache_creation_input_tokens": _int_value(usage.get("cache_creation_input_tokens")),
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
