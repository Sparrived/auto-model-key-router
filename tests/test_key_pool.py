from __future__ import annotations

from pathlib import Path

import anyio
import pytest

from auto_model_key_router.config import RouterConfig
from auto_model_key_router.key_pool import KeyPool


def make_pool(tmp_path: Path) -> KeyPool:
    return KeyPool(
        RouterConfig.from_dict(
            {
                "key_state_path": str(tmp_path / "key-state.json"),
                "models": [
                    {
                        "id": "model-a",
                        "keys": [
                            {
                                "name": "key-a",
                                "api_key": "sk-a",
                                "base_url": "https://example.test",
                            }
                        ],
                    }
                ],
            }
        )
    )


def make_multi_key_pool(tmp_path: Path) -> KeyPool:
    return KeyPool(
        RouterConfig.from_dict(
            {
                "key_state_path": str(tmp_path / "key-state.json"),
                "models": [
                    {
                        "id": "model-a",
                        "keys": [
                            {
                                "name": "key-a",
                                "api_key": "sk-a",
                                "base_url": "https://a.example.test",
                            },
                            {
                                "name": "key-b",
                                "api_key": "sk-b",
                                "base_url": "https://b.example.test",
                            },
                        ],
                    }
                ],
            }
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
