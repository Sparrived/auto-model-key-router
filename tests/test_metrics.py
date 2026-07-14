from __future__ import annotations

from datetime import datetime
from pathlib import Path

import anyio

from auto_model_key_router import metrics as metrics_module
from auto_model_key_router.metrics import BEIJING_TZ, MetricsStore, extract_usage


def test_extract_usage_supports_responses_completed_event() -> None:
    usage = {
        "input_tokens": 7,
        "output_tokens": 3,
        "total_tokens": 10,
    }

    assert extract_usage(
        {"type": "response.completed", "response": {"usage": usage}}
    ) == usage


def test_snapshot_aggregates_dimensions_in_sql(tmp_path: Path) -> None:
    async def run() -> dict[str, object]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        await store.record(
            "model-a",
            "key-a",
            200,
            {"prompt_tokens": 4, "completion_tokens": 2, "cached_tokens": 1},
            duration_ms=30,
            first_token_ms=10,
            requested_model_id="alias-a",
            provider_id="provider-a",
            pool_name="pool-a",
            upstream_model_id="upstream-a",
        )
        await store.record(
            "model-a",
            "key-b",
            429,
            {"input_tokens": 3, "output_tokens": 1},
            retried=True,
            duration_ms=50,
            first_token_ms=20,
            requested_model_id="alias-a",
            caller_type="visitor",
            provider_id="provider-b",
            pool_name="pool-b",
            upstream_model_id="upstream-b",
        )
        snapshot = await store.snapshot()
        await store.close()
        return snapshot

    snapshot = anyio.run(run)
    assert snapshot["rate_window_seconds"] == 60
    assert snapshot["count_semantics"] == "upstream_attempt"
    assert snapshot["current_rpm"] == 2
    assert snapshot["current_tpm"] == 10
    total = snapshot["total"]
    assert total["requests"] == 2
    assert total["successes"] == 1
    assert total["failures"] == 1
    assert total["retries"] == 1
    assert total["prompt_tokens"] == 7
    assert total["completion_tokens"] == 3
    assert total["status_codes"] == {"200": 1, "429": 1}
    assert snapshot["caller_types"]["local"]["requests"] == 1
    assert snapshot["caller_types"]["visitor"]["requests"] == 1
    assert snapshot["model_requested_models"]["model-a"]["alias-a"]["requests"] == 2
    assert snapshot["keys"]["model-a"]["key-b"]["failures"] == 1
    assert snapshot["providers"]["provider-a"]["requests"] == 1
    assert snapshot["provider_pools"]["provider-b"]["pool-b"]["requests"] == 1
    assert snapshot["upstream_models"]["upstream-b"]["failures"] == 1


def test_request_history_returns_filtered_cursor_page(
    tmp_path: Path, monkeypatch
) -> None:
    clock = [datetime(2026, 7, 14, 11, 58, tzinfo=BEIJING_TZ)]
    monkeypatch.setattr(metrics_module, "_now_beijing", lambda: clock[0])

    async def run() -> tuple[dict[str, object], dict[str, object], dict[str, object]]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        for minute, key_name, status_code in (
            (59, "key-a", 200),
            (59, "key-b", 429),
        ):
            clock[0] = datetime(2026, 7, 14, 11, minute, 30, tzinfo=BEIJING_TZ)
            await store.record(
                "model-a",
                key_name,
                status_code,
                {
                    "prompt_tokens": 10,
                    "completion_tokens": 5,
                    "cached_tokens": 4,
                },
                retried=status_code == 429,
                requested_model_id="alias-a",
                caller_type="visitor",
                provider_id="provider-a",
                pool_name="pool-a",
                upstream_model_id="upstream-a",
            )
        await store.record("model-legacy", "key-legacy", 200)
        clock[0] = datetime(2026, 7, 14, 12, 0, tzinfo=BEIJING_TZ)
        first = await store.request_history(
            hours=1, caller_type="visitor", limit=1
        )
        second = await store.request_history(
            hours=1,
            caller_type="visitor",
            limit=1,
            before_id=first["next_before_id"],
        )
        unattributed = await store.request_history(hours=1, attributed=False)
        await store.close()
        return first, second, unattributed

    first, second, unattributed = anyio.run(run)

    assert first["total_items"] == 2
    assert first["count_semantics"] == "upstream_attempt"
    assert first["summary"]["requests"] == 2
    assert first["summary"]["status_codes"] == {"200": 1, "429": 1}
    assert first["next_before_id"] is not None
    item = first["items"][0]
    assert item["provider_id"] == "provider-a"
    assert item["pool_name"] == "pool-a"
    assert item["upstream_model_id"] == "upstream-a"
    assert item["uncached_prompt_tokens"] == 6
    assert isinstance(item["success"], bool)
    assert second["items"][0]["id"] < item["id"]
    assert unattributed["total_items"] == 1
    assert unattributed["items"][0]["provider_id"] is None


def test_attribution_keeps_real_unknown_id_separate_from_legacy_rows(
    tmp_path: Path,
) -> None:
    async def run() -> tuple[dict[str, object], dict[str, object], dict[str, object]]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        await store.record("legacy-model", "legacy-key", 200)
        await store.record(
            "model-a",
            "key-a",
            200,
            provider_id="unknown",
            pool_name="unknown",
            upstream_model_id="unknown",
        )
        snapshot = await store.snapshot()
        real_unknown = await store.request_history(provider_id="unknown")
        unattributed = await store.request_history(attributed=False)
        await store.close()
        return snapshot, real_unknown, unattributed

    snapshot, real_unknown, unattributed = anyio.run(run)

    assert snapshot["providers"]["unknown"]["requests"] == 1
    assert snapshot["provider_pools"]["unknown"]["unknown"]["requests"] == 1
    assert snapshot["upstream_models"]["unknown"]["requests"] == 1
    assert snapshot["unattributed"]["requests"] == 1
    assert real_unknown["total_items"] == 1
    assert real_unknown["items"][0]["model_id"] == "model-a"
    assert unattributed["total_items"] == 1
    assert unattributed["items"][0]["model_id"] == "legacy-model"


def test_time_series_returns_aligned_zero_filled_buckets(
    tmp_path: Path, monkeypatch
) -> None:
    clock = [datetime(2026, 7, 14, 12, 1, 10, tzinfo=BEIJING_TZ)]
    monkeypatch.setattr(metrics_module, "_now_beijing", lambda: clock[0])

    async def run() -> dict[str, object]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        await store.record(
            "model-a",
            "key-a",
            200,
            {"prompt_tokens": 10, "completion_tokens": 5, "cached_tokens": 4},
            duration_ms=90,
            first_token_ms=30,
        )
        clock[0] = datetime(2026, 7, 14, 12, 3, 30, tzinfo=BEIJING_TZ)
        result = await store.time_series(
            hours=0.05, bucket_seconds=60, model_id="model-a"
        )
        await store.close()
        return result

    result = anyio.run(run)
    points = result["points"]

    assert result["bucket_seconds"] == 60
    assert result["count_semantics"] == "upstream_attempt"
    assert result["window"]["from"] == "2026-07-14T12:00:00+08:00"
    assert [point["requests"] for point in points] == [0, 1, 0, 0]
    assert points[1]["total_tokens"] == 15
    assert points[1]["cached_token_rate"] == 0.4
    assert points[1]["avg_duration_ms"] == 90
    assert points[-1]["complete"] is False


def test_daily_series_aligns_to_beijing_midnight(tmp_path: Path, monkeypatch) -> None:
    clock = [datetime(2026, 7, 14, 12, 0, tzinfo=BEIJING_TZ)]
    monkeypatch.setattr(metrics_module, "_now_beijing", lambda: clock[0])

    async def run() -> dict[str, object]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        result = await store.time_series(hours=24, bucket_seconds=86400)
        await store.close()
        return result

    result = anyio.run(run)

    assert result["window"]["from"] == "2026-07-13T00:00:00+08:00"
    assert [point["started_at"] for point in result["points"]] == [
        "2026-07-13T00:00:00+08:00",
        "2026-07-14T00:00:00+08:00",
    ]


def test_queries_exclude_rows_newer_than_response_window(
    tmp_path: Path, monkeypatch
) -> None:
    clock = [datetime(2026, 7, 14, 13, 0, tzinfo=BEIJING_TZ)]
    monkeypatch.setattr(metrics_module, "_now_beijing", lambda: clock[0])

    async def run() -> tuple[dict[str, object], dict[str, object], dict[str, object]]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        await store.record("future-model", "future-key", 200)
        clock[0] = datetime(2026, 7, 14, 12, 0, tzinfo=BEIJING_TZ)
        snapshot = await store.snapshot()
        history = await store.request_history(hours=1)
        series = await store.time_series(hours=1, bucket_seconds=60)
        await store.close()
        return snapshot, history, series

    snapshot, history, series = anyio.run(run)

    assert snapshot["total"]["requests"] == 0
    assert history["total_items"] == 0
    assert sum(point["requests"] for point in series["points"]) == 0


def test_snapshot_counts_anthropic_cache_read_tokens_as_cached(
    tmp_path: Path,
) -> None:
    async def run() -> dict[str, object]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        await store.record(
            "model-a",
            "key-a",
            200,
            {
                "input_tokens": 100,
                "output_tokens": 5,
                "cache_creation_input_tokens": 20,
                "cache_read_input_tokens": 80,
            },
        )
        snapshot = await store.snapshot()
        await store.close()
        return snapshot

    total = anyio.run(run)["total"]

    assert total["prompt_tokens"] == 200
    assert total["cached_tokens"] == 80
    assert total["cache_creation_input_tokens"] == 20
    assert total["cache_read_input_tokens"] == 80


def test_key_stats_returns_key_specific_data(tmp_path: Path) -> None:
    async def run() -> dict[str, object]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        await store.record(
            "model-a",
            "key-a",
            200,
            {"prompt_tokens": 10, "completion_tokens": 5, "cached_tokens": 2},
            duration_ms=30,
            first_token_ms=10,
        )
        await store.record(
            "model-a",
            "key-a",
            200,
            {"prompt_tokens": 20, "completion_tokens": 8},
            duration_ms=40,
            first_token_ms=15,
        )
        await store.record(
            "model-a",
            "key-b",
            429,
            {"prompt_tokens": 100, "completion_tokens": 50},
            retried=True,
            duration_ms=50,
            first_token_ms=20,
        )
        result = await store.key_stats("model-a", "key-a")
        await store.close()
        return result

    result = anyio.run(run)
    assert result["model_id"] == "model-a"
    assert result["key_name"] == "key-a"
    stats = result["stats"]
    assert stats["requests"] == 2
    assert stats["successes"] == 2
    assert stats["failures"] == 0
    assert stats["prompt_tokens"] == 30
    assert stats["completion_tokens"] == 13
    assert stats["cached_tokens"] == 2
    assert stats["status_codes"] == {"200": 2}
    assert len(result["recent_requests"]) == 2


def test_key_stats_respects_hours_filter(tmp_path: Path) -> None:
    async def run() -> tuple[dict[str, object], dict[str, object]]:
        store = MetricsStore(tmp_path / "metrics.sqlite3")
        await store.record(
            "model-a",
            "key-a",
            200,
            {"prompt_tokens": 10, "completion_tokens": 5},
        )
        all_result = await store.key_stats("model-a", "key-a")
        none_result = await store.key_stats("model-a", "key-a", hours=0.0)
        await store.close()
        return all_result, none_result

    all_result, none_result = anyio.run(run)
    assert all_result["stats"]["requests"] == 1
    assert none_result["stats"]["requests"] == 0
