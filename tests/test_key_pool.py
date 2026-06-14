from __future__ import annotations

from pathlib import Path

import anyio

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


def test_success_without_failure_does_not_persist_empty_state(
    tmp_path: Path, monkeypatch
) -> None:
    pool = make_pool(tmp_path)
    writes: list[dict[tuple[str, str], object]] = []

    async def save(states):
        writes.append(states)

    monkeypatch.setattr(pool._state_store, "save", save)

    anyio.run(pool.mark_success, "model-a", "key-a")

    assert writes == []


def test_failure_and_recovery_persist_state_transitions(
    tmp_path: Path, monkeypatch
) -> None:
    pool = make_pool(tmp_path)
    writes: list[dict[tuple[str, str], object]] = []

    async def save(states):
        writes.append(states)

    monkeypatch.setattr(pool._state_store, "save", save)

    async def run() -> None:
        await pool.mark_failure("model-a", "key-a", 429, 60)
        await pool.mark_success("model-a", "key-a")

    anyio.run(run)

    assert len(writes) == 2
    assert writes[0][("model-a", "key-a")].failures == 1
    assert writes[1][("model-a", "key-a")].failures == 0
