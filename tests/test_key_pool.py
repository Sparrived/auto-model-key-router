from __future__ import annotations

from pathlib import Path
import json

import anyio
import pytest

from auto_model_key_router.config import KeyConfig, ModelConfig, RouterConfig
from auto_model_key_router.key_pool import KeyPool


def make_pool(tmp_path: Path) -> KeyPool:
    return KeyPool(
        RouterConfig(
            host="127.0.0.1",
            port=8000,
            request_timeout=10,
            max_retries=1,
            key_failure_threshold=5,
            key_cooldown_seconds=10,
            key_state_path=str(tmp_path / "key-state.json"),
            upstream_health_check_interval=0,
            metrics_db_path=str(tmp_path / "metrics.sqlite3"),
            log_file_path=str(tmp_path / "server.log"),
            local_api_key="local-key",
            models=(
                ModelConfig(
                    id="model-a",
                    keys=(KeyConfig("key-a", "sk-a", "https://example.test"),),
                ),
            ),
        )
    )


def make_multi_key_pool(tmp_path: Path) -> KeyPool:
    return KeyPool(
        RouterConfig(
            host="127.0.0.1",
            port=8000,
            request_timeout=10,
            max_retries=1,
            key_failure_threshold=5,
            key_cooldown_seconds=10,
            key_state_path=str(tmp_path / "key-state.json"),
            upstream_health_check_interval=0,
            metrics_db_path=str(tmp_path / "metrics.sqlite3"),
            log_file_path=str(tmp_path / "server.log"),
            local_api_key="local-key",
            models=(
                ModelConfig(
                    id="model-a",
                    keys=(
                        KeyConfig("key-a", "sk-a", "https://a.example.test"),
                        KeyConfig("key-b", "sk-b", "https://b.example.test"),
                    ),
                ),
            ),
        )
    )


def test_round_robin_sticks_same_affinity_to_selected_key(tmp_path: Path) -> None:
    pool = make_multi_key_pool(tmp_path)

    async def run() -> tuple[str, str, str]:
        first = await pool.next_key("model-a", affinity_key="session-a")
        await pool.release_key("model-a", first.name)
        second = await pool.next_key("model-a", affinity_key="session-a")
        await pool.release_key("model-a", second.name)
        third = await pool.next_key("model-a", affinity_key="session-b")
        await pool.release_key("model-a", third.name)
        return first.name, second.name, third.name

    assert anyio.run(run) == ("key-a", "key-a", "key-b")


def test_round_robin_affinity_remaps_when_sticky_key_is_excluded(
    tmp_path: Path,
) -> None:
    pool = make_multi_key_pool(tmp_path)

    async def run() -> tuple[str, str, str]:
        first = await pool.next_key("model-a", affinity_key="session-a")
        await pool.release_key("model-a", first.name)
        second = await pool.next_key(
            "model-a", excluded={first.name}, affinity_key="session-a"
        )
        await pool.release_key("model-a", second.name)
        third = await pool.next_key("model-a", affinity_key="session-a")
        await pool.release_key("model-a", third.name)
        return first.name, second.name, third.name

    assert anyio.run(run) == ("key-a", "key-b", "key-b")


def test_success_without_failure_does_not_persist_empty_state(
    tmp_path: Path, monkeypatch
) -> None:
    pool = make_pool(tmp_path)
    writes: list[dict[tuple[str, str], object]] = []

    async def save(states, url_native_support=None):
        writes.append(states)

    monkeypatch.setattr(pool._state_store, "save", save)

    anyio.run(pool.mark_success, "model-a", "key-a")

    assert writes == []


def test_failure_and_recovery_persist_state_transitions(
    tmp_path: Path, monkeypatch
) -> None:
    pool = make_pool(tmp_path)
    writes: list[dict[tuple[str, str], object]] = []

    async def save(states, url_native_support=None):
        writes.append(states)

    monkeypatch.setattr(pool._state_store, "save", save)

    async def run() -> None:
        await pool.mark_failure("model-a", "key-a", 429, 60)
        await pool.mark_success("model-a", "key-a")

    anyio.run(run)

    assert len(writes) == 2
    assert writes[0][("model-a", "key-a")].failures == 1
    assert writes[1][("model-a", "key-a")].failures == 0


def test_failure_cooldown_increases_with_consecutive_failures(
    tmp_path: Path, monkeypatch
) -> None:
    monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1000.0)
    pool = make_pool(tmp_path)

    async def run() -> tuple[int, int]:
        await pool.mark_failure("model-a", "key-a", 429, 10)
        first = pool.key_states()["model-a:key-a"]["cooldown_remaining_seconds"]
        await pool.mark_failure("model-a", "key-a", 429, 10)
        second = pool.key_states()["model-a:key-a"]["cooldown_remaining_seconds"]
        return first, second

    assert anyio.run(run) == (10, 20)


def test_key_is_disabled_after_five_consecutive_failures(tmp_path: Path) -> None:
    pool = make_pool(tmp_path)

    async def run() -> dict[str, object]:
        for _ in range(5):
            await pool.mark_failure("model-a", "key-a", 429, 10)
        with pytest.raises(RuntimeError):
            await pool.next_key("model-a")
        return pool.key_states()["model-a:key-a"]

    state = anyio.run(run)
    reloaded = make_pool(tmp_path)

    assert state["failures"] == 5
    assert state["cooldown_remaining_seconds"] == 0
    assert state["disabled"] is True
    assert reloaded.key_states()["model-a:key-a"]["disabled"] is True


def test_native_negative_support_expires_and_is_visible(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1000.0)
    pool = make_pool(tmp_path)

    async def run() -> None:
        await pool.update_native_endpoint("https://example.test", False, "v1/responses")

    anyio.run(run)

    assert pool.supports_native_endpoint("https://example.test", "v1/responses") is False
    state = pool.url_native_support_states()["https://example.test|v1/responses"]
    assert state["supported"] is False
    assert state["expires_in_seconds"] == 600

    monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1601.0)

    assert pool.supports_native_endpoint("https://example.test", "v1/responses") is None
    persisted = json.loads((tmp_path / "key-state.json").read_text(encoding="utf-8"))
    assert persisted["url_native_support"]["https://example.test|v1/responses"]["supported"] is False
