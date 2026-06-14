from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from email.utils import parsedate_to_datetime
from time import perf_counter
from typing import Any

import httpx

from .key_pool import KeyPool
from .metrics import MetricsStore


RETRYABLE_STATUS_CODES = frozenset({401, 403, 429, 500, 502, 503, 504, 521})


@dataclass
class StreamLifecycle:
    response: httpx.Response
    metrics: MetricsStore
    key_pool: KeyPool
    model_id: str
    key_name: str
    requested_model_id: str
    upstream: str
    started: float
    caller_type: str = "local"
    chunk_count: int = 0
    byte_count: int = 0
    first_token_ms: int = 0
    usage: dict[str, Any] | None = None

    def observe_chunk(self, chunk: bytes) -> None:
        self.chunk_count += 1
        self.byte_count += len(chunk)
        if self.first_token_ms == 0:
            self.first_token_ms = self.elapsed_ms()

    def elapsed_ms(self) -> int:
        return max(0, round((perf_counter() - self.started) * 1000))

    def error_payload(self, exc: BaseException) -> dict[str, Any]:
        return {
            "model_id": self.model_id,
            "requested_model_id": self.requested_model_id,
            "key_name": self.key_name,
            "upstream": self.upstream,
            "status_code": self.response.status_code,
            "content_type": self.response.headers.get("content-type"),
            "error_type": exc.__class__.__name__,
            "error": str(exc),
            "chunks": self.chunk_count,
            "bytes": self.byte_count,
            "duration_ms": self.elapsed_ms(),
        }

    async def finish(self, *, failed: bool) -> None:
        await self.metrics.record(
            self.model_id,
            self.key_name,
            self.response.status_code,
            self.usage,
            failed=failed,
            duration_ms=self.elapsed_ms(),
            first_token_ms=self.first_token_ms,
            requested_model_id=self.requested_model_id,
            caller_type=self.caller_type,
        )
        if failed or self.response.status_code in RETRYABLE_STATUS_CODES:
            await self.key_pool.mark_failure(
                self.model_id,
                self.key_name,
                self.response.status_code,
                retry_after_seconds(self.response),
            )
        elif self.response.status_code < 400:
            await self.key_pool.mark_success(self.model_id, self.key_name)
        await self.key_pool.release_key(self.model_id, self.key_name)
        await self.response.aclose()

    def close_report(self) -> dict[str, Any]:
        return {
            "status_code": self.response.status_code,
            "chunks": self.chunk_count,
            "bytes": self.byte_count,
            "first_token_ms": self.first_token_ms,
            "usage": self.usage,
        }


def retry_after_seconds(response: httpx.Response) -> float | None:
    value = response.headers.get("retry-after")
    if not value:
        return None
    try:
        return max(0.0, float(value))
    except ValueError:
        try:
            retry_at = parsedate_to_datetime(value)
            if retry_at.tzinfo is None:
                retry_at = retry_at.astimezone()
            return max(
                0.0, retry_at.timestamp() - datetime.now(retry_at.tzinfo).timestamp()
            )
        except (TypeError, ValueError, OSError):
            return None
