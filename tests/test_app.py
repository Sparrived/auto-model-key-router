from __future__ import annotations

import json
import tempfile
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import TypeVar

import anyio
import httpx
from fastapi import FastAPI

from auto_model_key_router.app import _probe_cooling_keys, create_app
from auto_model_key_router.config import KeyConfig, ModelConfig, RouterConfig


T = TypeVar("T")


def run_client(app: FastAPI, action: Callable[[httpx.AsyncClient], Awaitable[T]]) -> T:
    return anyio.run(_run_client, app, action)


async def _run_client(app: FastAPI, action: Callable[[httpx.AsyncClient], Awaitable[T]]) -> T:
    async with app.router.lifespan_context(app):
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://testserver") as client:
            return await action(client)


def make_config(tmp_path: Path, keys: tuple[KeyConfig, ...], local_api_key: str = "local-key", reasoning_effort: str | None = None) -> RouterConfig:
    return RouterConfig(
        host="127.0.0.1",
        port=8000,
        request_timeout=10,
        max_retries=1,
        key_failure_threshold=1,
        key_cooldown_seconds=60,
        key_state_path=str(tmp_path / "key-state.json"),
        upstream_health_check_interval=0,
        metrics_db_path=str(tmp_path / "metrics.sqlite3"),
        log_file_path=str(tmp_path / "server.log"),
        local_api_key=local_api_key,
        models=(
            ModelConfig(
                id="test-model",
                aliases=("alias-model",),
                routing_mode="round_robin",
                reasoning_effort=reasoning_effort,
                keys=keys,
            ),
        ),
    )


def test_metrics_requires_local_auth() -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),))

        async def requests(client: httpx.AsyncClient) -> tuple[httpx.Response, httpx.Response]:
            unauthorized = await client.get("/metrics")
            authorized = await client.get("/metrics", headers={"Authorization": "Bearer local-key"})
            return unauthorized, authorized

        unauthorized, authorized = run_client(create_app(config), requests)

        assert unauthorized.status_code == 401
        assert authorized.status_code == 200
        assert authorized.json()["total"]["requests"] == 0


def test_retryable_response_cools_down_failed_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        calls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            calls.append(str(request.url))
            if len(calls) == 1:
                return httpx.Response(429, json={"error": {"message": "rate limited"}}, headers={"Retry-After": "120"})
            return httpx.Response(200, json={"id": "ok", "usage": {"total_tokens": 1}})

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
        )
        app = create_app(config)
        app.state.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        async def requests(client: httpx.AsyncClient) -> tuple[httpx.Response, httpx.Response]:
            response = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "alias-model", "messages": [{"role": "user", "content": "hi"}]},
            )
            health = await client.get("/health")
            return response, health

        response, health = run_client(app, requests)

        assert response.status_code == 200
        assert calls == ["https://upstream-one.test/v1/chat/completions", "https://upstream-two.test/v1/chat/completions"]
        assert health.json()["key_states"]["test-model:key-1"]["cooldown_remaining_seconds"] > 0


def test_cooled_down_key_is_skipped_on_next_request() -> None:
    with tempfile.TemporaryDirectory() as directory:
        calls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            calls.append(str(request.url))
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
        )
        app = create_app(config)
        app.state.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            await app.state.key_pool.mark_failure("test-model", "key-1", 429, 120)
            return await client.post(
                "/v1/chat/completions",
                headers={"x-api-key": "local-key"},
                json={"model": "test-model", "messages": [{"role": "user", "content": "hi"}]},
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert calls == ["https://upstream-two.test/v1/chat/completions"]


def test_key_state_is_persisted_and_restored() -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),))
        first_app = create_app(config)

        anyio.run(first_app.state.key_pool.mark_failure, "test-model", "key-1", 429, 120)
        second_app = create_app(config)

        state = second_app.state.key_pool.key_states()["test-model:key-1"]

        anyio.run(first_app.state.metrics.close)
        anyio.run(first_app.state.http_client.aclose)
        anyio.run(second_app.state.metrics.close)
        anyio.run(second_app.state.http_client.aclose)

        assert state["failures"] == 1
        assert state["cooldown_remaining_seconds"] > 0


def test_health_probe_clears_recovered_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        calls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            calls.append(str(request.url))
            return httpx.Response(200, json={"object": "list", "data": []})

        config = make_config(Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),))
        app = create_app(config)
        app.state.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        anyio.run(app.state.key_pool.mark_failure, "test-model", "key-1", 429, 120)
        anyio.run(_probe_cooling_keys, app.state)
        state = app.state.key_pool.key_states()["test-model:key-1"]

        anyio.run(app.state.metrics.close)
        anyio.run(app.state.http_client.aclose)

        assert calls == ["https://upstream.test/v1/models"]
        assert state["failures"] == 0
        assert state["cooldown_remaining_seconds"] == 0


def test_config_reasoning_effort_is_forwarded() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),), reasoning_effort="xhigh")
        app = create_app(config)
        app.state.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "alias-model", "messages": [{"role": "user", "content": "hi"}]},
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_bodies[0]["model"] == "test-model"
        assert upstream_bodies[0]["reasoning_effort"] == "xhigh"


def test_config_reasoning_effort_overrides_request_reasoning() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),), reasoning_effort="high")
        app = create_app(config)
        app.state.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "input": "hi", "reasoning": {"effort": "low"}},
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_bodies[0]["reasoning_effort"] == "high"
        assert "reasoning" not in upstream_bodies[0]


def test_downstream_reasoning_effort_is_forwarded_without_config_override() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),))
        app = create_app(config)
        app.state.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "input": "hi", "reasoning": {"effort": "low"}},
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_bodies[0]["reasoning_effort"] == "low"
        assert "reasoning" not in upstream_bodies[0]


def test_none_reasoning_effort_disables_request_reasoning() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),), reasoning_effort="none")
        app = create_app(config)
        app.state.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "messages": [{"role": "user", "content": "hi"}], "reasoning_effort": "high", "reasoning": {"effort": "low"}},
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_bodies[0]["reasoning_effort"] == "none"
        assert "reasoning" not in upstream_bodies[0]
