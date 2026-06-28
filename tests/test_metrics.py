from __future__ import annotations

from pathlib import Path

import anyio

from auto_model_key_router.metrics import MetricsStore, extract_usage


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
        )
        snapshot = await store.snapshot()
        await store.close()
        return snapshot

    snapshot = anyio.run(run)
    assert snapshot["rate_window_seconds"] == 60
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
