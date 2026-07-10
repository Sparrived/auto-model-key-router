from __future__ import annotations

from pathlib import Path
import json

import anyio

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
            endpoint_capabilities_path=str(tmp_path / "endpoint-capabilities.json"),
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
            endpoint_capabilities_path=str(tmp_path / "endpoint-capabilities.json"),
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


def test_cooling_key_is_skipped_when_another_key_is_available(
    tmp_path: Path, monkeypatch
) -> None:
    monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1000.0)
    pool = make_multi_key_pool(tmp_path)

    async def run() -> str:
        await pool.mark_failure("model-a", "key-a", 429, 10)
        selected = await pool.next_key("model-a")
        await pool.release_key("model-a", selected.name)
        return selected.name

    assert anyio.run(run) == "key-b"


def test_expired_cooldown_returns_key_to_rotation(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1000.0)
    pool = make_multi_key_pool(tmp_path)

    async def run() -> str:
        await pool.mark_failure("model-a", "key-a", 429, 10)
        pool._health._clock = lambda: 1011.0
        selected = await pool.next_key("model-a")
        await pool.release_key("model-a", selected.name)
        return selected.name

    assert anyio.run(run) == "key-a"


def test_all_cooling_keys_still_allow_degraded_selection(
    tmp_path: Path, monkeypatch
) -> None:
    monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1000.0)
    pool = make_multi_key_pool(tmp_path)

    async def run() -> str:
        await pool.mark_failure("model-a", "key-a", 429, 10)
        await pool.mark_failure("model-a", "key-b", 429, 10)
        selected = await pool.next_key("model-a")
        await pool.release_key("model-a", selected.name)
        return selected.name

    assert anyio.run(run) == "key-a"


def test_native_negative_support_expires_and_is_visible(
    tmp_path: Path, monkeypatch
) -> None:
    monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1000.0)
    pool = make_pool(tmp_path)

    async def run() -> None:
        await pool.update_native_endpoint("https://example.test", False, "v1/responses")

    anyio.run(run)

    assert (
        pool.supports_native_endpoint("https://example.test", "v1/responses") is False
    )
    state = pool.endpoint_capability_states()["https://example.test|v1/responses"]
    assert state["supported"] is False
    assert state["expires_in_seconds"] == 600

    monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1601.0)
    pool._endpoint_capabilities._clock = lambda: 1601.0

    assert pool.supports_native_endpoint("https://example.test", "v1/responses") is None
    persisted = json.loads(
        (tmp_path / "endpoint-capabilities.json").read_text(encoding="utf-8")
    )
    assert (
        persisted["endpoint_capabilities"]["https://example.test|v1/responses"][
            "supported"
        ]
        is False
    )
