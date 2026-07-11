from __future__ import annotations

import asyncio
import json
import logging
import sqlite3
import tempfile
from collections.abc import Awaitable, Callable
from dataclasses import replace
from pathlib import Path
from time import perf_counter
from typing import TypeVar

import anyio
import httpx
import pytest
from fastapi import FastAPI, WebSocketDisconnect
from fastapi.testclient import TestClient

from auto_model_key_router.app import create_app
from auto_model_key_router.config import (
    UNIFIED_MODEL_ID,
    VISITOR_API_KEY,
    KeyConfig,
    ModelConfig,
    RouterConfig,
    UnifiedModelConfig,
)
from auto_model_key_router.key_pool import KeyPool
from auto_model_key_router.metrics import MetricsStore
from auto_model_key_router.proxy_handler import (
    _stream_anthropic_messages,
    _stream_upstream,
)
from auto_model_key_router.unified_model import switch_unified_model


T = TypeVar("T")


class BrokenStream(httpx.AsyncByteStream):
    async def __aiter__(self):
        yield b'data: {"id":"chunk"}\n'
        raise httpx.DecodingError("broken body")


class ChunkedStream(httpx.AsyncByteStream):
    def __init__(self, chunks: tuple[bytes, ...]) -> None:
        self.chunks = chunks

    async def __aiter__(self):
        for chunk in self.chunks:
            yield chunk


class HangingStream(httpx.AsyncByteStream):
    def __init__(self, *, delay_first: bool = False) -> None:
        self.delay_first = delay_first

    async def __aiter__(self):
        if self.delay_first:
            await anyio.sleep(0.05)
        yield b'data: {"choices":[{"delta":{"content":"one"}}]}\n\n'
        await anyio.sleep(0.05)
        yield b"data: [DONE]\n\n"


def run_client(app: FastAPI, action: Callable[[httpx.AsyncClient], Awaitable[T]]) -> T:
    return anyio.run(_run_client, app, action)


async def _run_client(
    app: FastAPI, action: Callable[[httpx.AsyncClient], Awaitable[T]]
) -> T:
    async with app.router.lifespan_context(app):
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app), base_url="http://testserver"
        ) as client:
            return await action(client)


def make_config(
    tmp_path: Path,
    keys: tuple[KeyConfig, ...],
    local_api_key: str = "local-key",
    reasoning_effort: str | None = None,
    routing_mode: str = "round_robin",
    max_retries: int = 1,
    stream_first_byte_timeout: float = 90,
    stream_idle_timeout: float = 180,
    unified_model: UnifiedModelConfig | None = None,
    upstream_routes: dict[str, dict[str, str]] | None = None,
) -> RouterConfig:
    return RouterConfig(
        host="127.0.0.1",
        port=8000,
        request_timeout=10,
        stream_first_byte_timeout=stream_first_byte_timeout,
        stream_idle_timeout=stream_idle_timeout,
        max_retries=max_retries,
        key_failure_threshold=1,
        key_cooldown_seconds=60,
        endpoint_capabilities_path=str(tmp_path / "endpoint-capabilities.json"),
        metrics_db_path=str(tmp_path / "metrics.sqlite3"),
        log_file_path=str(tmp_path / "server.log"),
        local_api_key=local_api_key,
        models=(
            ModelConfig(
                id="test-model",
                aliases=("alias-model",),
                routing_mode=routing_mode,
                reasoning_effort=reasoning_effort,
                native_first=False,
                keys=keys,
            ),
        ),
        upstream_routes=upstream_routes or {},
        unified_model=unified_model,
    )


@pytest.fixture
def visitor_feature(monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.visitor.VISITOR_FEATURE_AVAILABLE", True)


def test_root_head_probe_returns_no_content(tmp_path: Path) -> None:
    config = make_config(
        tmp_path, (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
    )
    app = create_app(config)

    response = run_client(app, lambda client: client.head("/"))

    assert response.status_code == 204
    assert response.content == b""


def test_models_requires_valid_api_key(tmp_path: Path) -> None:
    config = make_config(
        tmp_path, (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
    )
    app = create_app(config)

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, httpx.Response, httpx.Response]:
        missing = await client.get("/v1/models")
        invalid = await client.get(
            "/v1/models", headers={"Authorization": "Bearer invalid"}
        )
        authorized = await client.get(
            "/v1/models", headers={"Authorization": "Bearer local-key"}
        )
        return missing, invalid, authorized

    missing, invalid, authorized = run_client(app, requests)

    assert missing.status_code == 401
    assert invalid.status_code == 401
    assert authorized.status_code == 200
    assert {model["id"] for model in authorized.json()["data"]} == {
        "alias-model",
        "test-model",
    }


def test_provider_target_uses_upstream_model_in_request_body(tmp_path: Path) -> None:
    config = RouterConfig.from_dict(
        {
            "config_version": 3,
            "local_api_key": "local-key",
            "providers": {
                "vendor": {
                    "base_url": "https://upstream.test",
                    "keys": {"main": {"api_key": "sk-main"}},
                    "pools": {"premium": {"keys": ["main"], "models": ["vendor-model"]}},
                }
            },
            "models": {
                "local-model": {
                    "targets": [
                        {
                            "provider": "vendor",
                            "pool": "premium",
                            "upstream_model": "vendor-model",
                        }
                    ]
                }
            },
            "metrics_db_path": str(tmp_path / "metrics.sqlite3"),
            "endpoint_capabilities_path": str(tmp_path / "endpoint-capabilities.json"),
            "log_file_path": str(tmp_path / "server.log"),
        }
    )
    app = create_app(config)
    upstream_payloads: list[dict[str, object]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        upstream_payloads.append(json.loads(request.content.decode("utf-8")))
        return httpx.Response(200, json={"id": "ok"})

    app.state.runtime_manager.current.http_client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler)
    )

    async def request(client: httpx.AsyncClient) -> httpx.Response:
        return await client.post(
            "/v1/chat/completions",
            headers={"Authorization": "Bearer local-key"},
            json={"model": "local-model", "messages": []},
        )

    response = run_client(app, request)

    assert response.status_code == 200
    assert upstream_payloads == [{"model": "vendor-model", "messages": []}]


def test_models_are_filtered_for_visitor_and_unified_model_is_rejected(
    tmp_path: Path, visitor_feature
) -> None:
    config = replace(
        make_config(
            tmp_path,
            (
                KeyConfig(
                    "visitor-key",
                    "sk-visitor",
                    "https://visitor.test",
                    allow_visitor=True,
                ),
            ),
            unified_model=UnifiedModelConfig(model="gpt-5.5"),
        ),
        models=(
            ModelConfig(
                id="gpt-5.5",
                aliases=("internal-gpt-latest",),
                keys=(
                    KeyConfig(
                        "visitor-key",
                        "sk-visitor",
                        "https://visitor.test",
                        allow_visitor=True,
                    ),
                ),
            ),
            ModelConfig(
                id="private-model",
                aliases=("internal-private-alias",),
                keys=(KeyConfig("private-key", "sk-private", "https://private.test"),),
            ),
            ModelConfig(
                id="disabled-model",
                keys=(
                    KeyConfig(
                        "disabled-key",
                        "sk-disabled",
                        "https://disabled.test",
                        enabled=False,
                        allow_visitor=True,
                    ),
                ),
            ),
            ModelConfig(
                id="external-public-model",
                aliases=("internal-external-alias",),
                keys=(
                    KeyConfig(
                        "external-visitor-key",
                        "sk-external",
                        "https://external.test",
                        allow_visitor=True,
                    ),
                ),
            ),
        ),
    )
    app = create_app(config)
    upstream_calls: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        upstream_calls.append(request)
        return httpx.Response(200, json={"id": "ok"})

    app.state.runtime_manager.current.http_client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler)
    )

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[
        httpx.Response,
        httpx.Response,
        httpx.Response,
        httpx.Response,
        httpx.Response,
    ]:
        local_models = await client.get(
            "/v1/models", headers={"Authorization": "Bearer local-key"}
        )
        visitor_models = await client.get(
            "/v1/models",
            headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
        )
        visitor_unified = await client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
            json={"model": UNIFIED_MODEL_ID, "messages": []},
        )
        visitor_public_model = await client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
            json={"model": "amkr-gpt-5.5", "messages": []},
        )
        visitor_internal_alias = await client.post(
            "/v1/chat/completions",
            headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
            json={"model": "internal-gpt-latest", "messages": []},
        )
        return (
            local_models,
            visitor_models,
            visitor_unified,
            visitor_public_model,
            visitor_internal_alias,
        )

    (
        local_models,
        visitor_models,
        visitor_unified,
        visitor_public_model,
        visitor_internal_alias,
    ) = run_client(app, requests)

    assert [model["id"] for model in local_models.json()["data"]] == [
        UNIFIED_MODEL_ID,
    ]
    assert [model["id"] for model in visitor_models.json()["data"]] == [
        "amkr-external-public-model",
        "amkr-gpt-5.5",
    ]
    assert visitor_unified.status_code == 403
    assert (
        visitor_unified.json()["error"]["message"]
        == f"访客 key 无权访问模型: {UNIFIED_MODEL_ID}"
    )
    assert visitor_public_model.status_code == 200
    assert visitor_internal_alias.status_code == 403
    assert json.loads(upstream_calls[0].content)["model"] == "gpt-5.5"
    assert upstream_calls[0].headers["authorization"] == "Bearer sk-visitor"
    assert len(upstream_calls) == 1


def test_websocket_proxy_forwards_complete_sse_events(tmp_path: Path) -> None:
    config = make_config(
        tmp_path, (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
    )
    app = create_app(config)
    upstream_requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        upstream_requests.append(request)
        return httpx.Response(
            200,
            headers={"content-type": "text/event-stream"},
            stream=ChunkedStream(
                (
                    b'data: {"id":"first"}\n',
                    b'\ndata: {"id":"second"}\n\n',
                    b"data: [DONE]\n\n",
                )
            ),
        )

    app.state.runtime_manager.current.http_client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler)
    )

    with TestClient(app) as client:
        with client.websocket_connect(
            "/v1/chat/completions?trace=1",
            headers={"Authorization": "Bearer local-key"},
        ) as websocket:
            websocket.send_json(
                {"model": "alias-model", "stream": True, "messages": []}
            )
            assert websocket.receive_text() == 'data: {"id":"first"}\n\n'
            assert websocket.receive_text() == 'data: {"id":"second"}\n\n'
            assert websocket.receive_text() == "data: [DONE]\n\n"
            with pytest.raises(WebSocketDisconnect) as exc_info:
                websocket.receive_text()

    assert exc_info.value.code == 1000
    assert len(upstream_requests) == 1
    request = upstream_requests[0]
    assert request.method == "POST"
    assert request.url == "https://upstream.test/v1/chat/completions?trace=1"
    assert request.headers["authorization"] == "Bearer sk-1"
    assert "upgrade" not in request.headers
    assert "sec-websocket-key" not in request.headers
    assert json.loads(request.content) == {
        "model": "test-model",
        "stream": True,
        "stream_options": {"include_usage": True},
        "messages": [],
    }


def test_websocket_proxy_preserves_non_stream_request(tmp_path: Path) -> None:
    config = make_config(
        tmp_path, (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
    )
    app = create_app(config)
    upstream_payloads: list[dict[str, object]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        upstream_payloads.append(json.loads(request.content))
        return httpx.Response(200, json={"id": "ok"})

    app.state.runtime_manager.current.http_client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler)
    )

    with TestClient(app) as client:
        with client.websocket_connect(
            "/v1/chat/completions",
            headers={"x-api-key": "local-key"},
        ) as websocket:
            websocket.send_json({"model": "test-model", "messages": []})
            assert websocket.receive_json() == {"id": "ok"}
            with pytest.raises(WebSocketDisconnect) as exc_info:
                websocket.receive_text()

    assert exc_info.value.code == 1000
    assert upstream_payloads == [{"model": "test-model", "messages": []}]


def test_websocket_proxy_rejects_invalid_local_key(tmp_path: Path) -> None:
    config = make_config(
        tmp_path, (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
    )
    app = create_app(config)

    with TestClient(app) as client:
        with client.websocket_connect("/v1/chat/completions") as websocket:
            websocket.send_json({"model": "test-model", "stream": True, "messages": []})
            assert websocket.receive_json() == {
                "error": {"message": "本地 API key 验证失败"}
            }
            with pytest.raises(WebSocketDisconnect) as exc_info:
                websocket.receive_text()

    assert exc_info.value.code == 1008


def test_unknown_anthropic_model_logs_requested_model(tmp_path: Path, caplog) -> None:
    config = make_config(
        tmp_path, (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
    )
    app = create_app(config)
    caplog.set_level("WARNING", logger="auto_model_key_router.app")

    response = run_client(
        app,
        lambda client: client.post(
            "/v1/messages?beta=true",
            headers={"x-api-key": "local-key"},
            json={
                "model": "claude-haiku-test",
                "messages": [{"role": "user", "content": "hi"}],
            },
        ),
    )

    assert response.status_code == 404
    assert response.json()["error"]["message"] == "未配置模型: claude-haiku-test"
    assert "path=/v1/messages" in caplog.text
    assert "requested_model=claude-haiku-test" in caplog.text
    assert "resolved_model=claude-haiku-test" in caplog.text


def test_anthropic_count_tokens_is_served_locally(tmp_path: Path) -> None:
    upstream_calls: list[str] = []

    def handler(request: httpx.Request) -> httpx.Response:
        upstream_calls.append(str(request.url))
        return httpx.Response(500)

    config = make_config(
        tmp_path, (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
    )
    app = create_app(config)
    app.state.runtime_manager.current.http_client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler)
    )

    response = run_client(
        app,
        lambda client: client.post(
            "/v1/messages/count_tokens",
            headers={"x-api-key": "local-key"},
            json={
                "model": "test-model",
                "messages": [{"role": "user", "content": "hello"}],
            },
        ),
    )

    assert response.status_code == 200
    assert response.json()["input_tokens"] > 0
    assert upstream_calls == []


def test_metrics_requires_local_auth() -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, httpx.Response]:
            unauthorized = await client.get("/metrics")
            authorized = await client.get(
                "/metrics", headers={"Authorization": "Bearer local-key"}
            )
            return unauthorized, authorized

        unauthorized, authorized = run_client(create_app(config), requests)

        assert unauthorized.status_code == 401
        assert authorized.status_code == 200
        assert authorized.json()["total"]["requests"] == 0


def test_metrics_group_local_and_visitor_calls(visitor_feature) -> None:
    with tempfile.TemporaryDirectory() as directory:

        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                json={
                    "id": "ok",
                    "usage": {"prompt_tokens": 3, "completion_tokens": 2},
                },
            )

        config = make_config(
            Path(directory),
            (
                KeyConfig(
                    "shared-key",
                    "sk-shared",
                    "https://upstream.test",
                    allow_visitor=True,
                ),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> dict[str, object]:
            local = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "local"}],
                },
            )
            visitor = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
                json={
                    "model": "amkr-test-model",
                    "messages": [{"role": "user", "content": "visitor"}],
                },
            )
            assert local.status_code == 200
            assert visitor.status_code == 200
            metrics = await client.get(
                "/metrics", headers={"Authorization": "Bearer local-key"}
            )
            return metrics.json()

        metrics = run_client(app, requests)

        assert metrics["total"]["requests"] == 2
        assert metrics["caller_types"]["local"]["requests"] == 1
        assert metrics["caller_types"]["visitor"]["requests"] == 1
        assert metrics["caller_types"]["local"]["total_tokens"] == 5
        assert metrics["caller_types"]["visitor"]["total_tokens"] == 5


def test_stream_metrics_preserve_visitor_caller_type(visitor_feature) -> None:
    with tempfile.TemporaryDirectory() as directory:

        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                headers={"content-type": "text/event-stream"},
                content=(
                    b'data: {"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}],'
                    b'"usage":{"prompt_tokens":2,"completion_tokens":1}}\n\n'
                    b"data: [DONE]\n\n"
                ),
            )

        config = make_config(
            Path(directory),
            (
                KeyConfig(
                    "visitor-key",
                    "sk-visitor",
                    "https://upstream.test",
                    allow_visitor=True,
                ),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> dict[str, object]:
            headers = {"Authorization": f"Bearer {VISITOR_API_KEY}"}
            for path in ("chat/completions", "messages", "responses"):
                response = await client.post(
                    f"/v1/{path}",
                    headers=headers,
                    json={
                        "model": "amkr-test-model",
                        "messages": [{"role": "user", "content": "hi"}],
                        "stream": True,
                    },
                )
                assert response.status_code == 200
            metrics = await client.get(
                "/metrics", headers={"Authorization": "Bearer local-key"}
            )
            return metrics.json()

        metrics = run_client(app, requests)

        assert metrics["caller_types"]["visitor"]["requests"] == 3
        assert metrics["caller_types"]["local"]["requests"] == 0


def test_metrics_migrates_existing_rows_to_local_calls(tmp_path: Path) -> None:
    database_path = tmp_path / "legacy-metrics.sqlite3"
    with sqlite3.connect(database_path) as connection:
        connection.execute(
            """
            CREATE TABLE request_metrics (
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
                total_tokens INTEGER NOT NULL DEFAULT 0
            )
            """
        )
        connection.execute(
            """
            INSERT INTO request_metrics (
                created_at, model_id, requested_model_id, key_name, status_code,
                success, retried, prompt_tokens, completion_tokens, total_tokens
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                "2026-06-13T12:00:00+08:00",
                "test-model",
                "alias-model",
                "key-1",
                200,
                1,
                0,
                2,
                1,
                3,
            ),
        )

    store = MetricsStore(database_path)
    snapshot = anyio.run(store.snapshot)
    anyio.run(store.close)

    with sqlite3.connect(database_path) as connection:
        caller_type = connection.execute(
            "SELECT caller_type FROM request_metrics"
        ).fetchone()[0]

    assert caller_type == "local"
    assert snapshot["caller_types"]["local"]["requests"] == 1
    assert snapshot["caller_types"]["visitor"]["requests"] == 0


def test_visitor_key_routes_only_to_allowed_upstream_keys(visitor_feature) -> None:
    with tempfile.TemporaryDirectory() as directory:
        authorization_headers: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            authorization_headers.append(request.headers["Authorization"])
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory),
            (
                KeyConfig("private-key", "sk-private", "https://private.test"),
                KeyConfig(
                    "visitor-key",
                    "sk-visitor",
                    "https://visitor.test",
                    allow_visitor=True,
                ),
            ),
            routing_mode="only_first",
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
                json={
                    "model": "amkr-test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert authorization_headers == ["Bearer sk-visitor"]


def test_visitor_key_cannot_select_private_upstream_key(visitor_feature) -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory),
            (
                KeyConfig("private-key", "sk-private", "https://private.test"),
                KeyConfig(
                    "visitor-key",
                    "sk-visitor",
                    "https://visitor.test",
                    allow_visitor=True,
                ),
            ),
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"x-api-key": VISITOR_API_KEY},
                json={
                    "model": "amkr-test-model[private-key]",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(create_app(config), request)

        assert response.status_code == 403
        assert (
            response.json()["error"]["message"]
            == "访客 key 无权访问模型 key: amkr-test-model[private-key]"
        )


def test_visitor_key_cannot_access_model_without_allowed_keys(visitor_feature) -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory),
            (KeyConfig("private-key", "sk-private", "https://private.test"),),
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
                json={
                    "model": "amkr-test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(create_app(config), request)

        assert response.status_code == 403
        assert (
            response.json()["error"]["message"]
            == "访客 key 无权访问模型: amkr-test-model"
        )


def test_full_local_key_can_still_access_private_upstream_key(visitor_feature) -> None:
    with tempfile.TemporaryDirectory() as directory:
        authorization_headers: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            authorization_headers.append(request.headers["Authorization"])
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory),
            (
                KeyConfig("private-key", "sk-private", "https://private.test"),
                KeyConfig(
                    "visitor-key",
                    "sk-visitor",
                    "https://visitor.test",
                    allow_visitor=True,
                ),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model[private-key]",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert authorization_headers == ["Bearer sk-private"]


def test_visitor_key_cannot_read_metrics(visitor_feature) -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory),
            (
                KeyConfig(
                    "visitor-key",
                    "sk-visitor",
                    "https://visitor.test",
                    allow_visitor=True,
                ),
            ),
        )

        response = run_client(
            create_app(config),
            lambda client: client.get(
                "/metrics", headers={"Authorization": f"Bearer {VISITOR_API_KEY}"}
            ),
        )

        assert response.status_code == 401


def test_health_reports_visitor_access(visitor_feature) -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory),
            (
                KeyConfig("private-key", "sk-private", "https://private.test"),
                KeyConfig(
                    "visitor-key",
                    "sk-visitor",
                    "https://visitor.test",
                    allow_visitor=True,
                ),
                KeyConfig(
                    "disabled-visitor-key",
                    "sk-disabled",
                    "https://disabled.test",
                    enabled=False,
                    allow_visitor=True,
                ),
            ),
        )

        response = run_client(create_app(config), lambda client: client.get("/health"))

        assert response.json()["visitor_feature_installed"] is True
        assert response.json()["visitor_access_enabled"] is True
        assert response.json()["visitor_key_count"] == 1


def test_visitor_key_is_rejected_when_optional_feature_is_not_installed(
    monkeypatch,
) -> None:
    with tempfile.TemporaryDirectory() as directory:
        monkeypatch.setattr(
            "auto_model_key_router.visitor.VISITOR_FEATURE_AVAILABLE", False
        )
        config = make_config(
            Path(directory),
            (
                KeyConfig(
                    "visitor-key",
                    "sk-visitor",
                    "https://visitor.test",
                    allow_visitor=True,
                ),
            ),
        )
        app = create_app(config)

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, httpx.Response]:
            proxy = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
                json={
                    "model": "amkr-test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            health = await client.get("/health")
            return proxy, health

        proxy, health = run_client(app, requests)

        assert proxy.status_code == 401
        assert health.json()["visitor_feature_installed"] is False
        assert health.json()["visitor_access_enabled"] is False
        assert health.json()["visitor_key_count"] == 0


def test_config_parses_allow_visitor_and_rejects_reserved_local_key() -> None:
    config = RouterConfig.from_dict(
        {
            "local_api_key": "local-key",
            "models": [
                {
                    "id": "test-model",
                    "keys": [
                        {"name": "private", "api_key": "sk-private"},
                        {
                            "name": "visitor",
                            "api_key": "sk-visitor",
                            "allow_visitor": True,
                        },
                    ],
                }
            ],
        }
    )

    assert config.models[0].keys[0].allow_visitor is False
    assert config.models[0].keys[1].allow_visitor is True

    with pytest.raises(ValueError, match="保留的访客 key"):
        RouterConfig.from_dict({"local_api_key": VISITOR_API_KEY, "models": []})


def test_config_file_changes_are_hot_reloaded() -> None:
    with tempfile.TemporaryDirectory() as directory:
        tmp_path = Path(directory)
        config_path = tmp_path / "router-config.json"
        config_data = {
            "host": "127.0.0.1",
            "port": 8000,
            "request_timeout": 10,
            "max_retries": 1,
            "key_failure_threshold": 1,
            "key_cooldown_seconds": 60,
            "endpoint_capabilities_path": str(tmp_path / "endpoint-capabilities.json"),
            "upstream_health_check_interval": 0,
            "metrics_db_path": str(tmp_path / "metrics.sqlite3"),
            "log_file_path": str(tmp_path / "server.log"),
            "local_api_key": "local-key",
            "models": [
                {
                    "id": "test-model",
                    "aliases": ["alias-model"],
                    "routing_mode": "round_robin",
                    "reasoning_effort": "low",
                    "keys": [
                        {
                            "name": "key-1",
                            "api_key": "sk-1",
                            "base_url": "https://upstream-one.test",
                        }
                    ],
                }
            ],
        }
        config_path.write_text(json.dumps(config_data, indent=2), encoding="utf-8")
        config = RouterConfig.load(config_path)
        upstream_bodies: list[dict[str, object]] = []
        authorization_headers: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            authorization_headers.append(request.headers["Authorization"])
            return httpx.Response(200, json={"id": "ok"})

        app = create_app(config, config_path)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, httpx.Response]:
            first = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "alias-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            config_data["local_api_key"] = "new-local-key"
            config_data["models"][0]["reasoning_effort"] = "high"
            config_data["models"][0]["keys"][0]["api_key"] = "sk-2"
            config_path.write_text(json.dumps(config_data, indent=2), encoding="utf-8")
            await anyio.sleep(0.01)
            second = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer new-local-key"},
                json={
                    "model": "alias-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            return first, second

        first, second = run_client(app, requests)

        assert first.status_code == 200
        assert second.status_code == 200
        assert upstream_bodies[0]["reasoning_effort"] == "low"
        assert upstream_bodies[1]["reasoning_effort"] == "high"
        assert authorization_headers == ["Bearer sk-1", "Bearer sk-2"]


def test_retryable_response_cools_down_failed_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        calls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            calls.append(str(request.url))
            if len(calls) == 1:
                return httpx.Response(
                    429,
                    json={"error": {"message": "rate limited"}},
                    headers={"Retry-After": "120"},
                )
            return httpx.Response(200, json={"id": "ok", "usage": {"total_tokens": 1}})

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, httpx.Response]:
            response = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "alias-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            health = await client.get("/health")
            return response, health

        response, health = run_client(app, requests)

        assert response.status_code == 200
        assert calls == [
            "https://upstream-one.test/v1/chat/completions",
            "https://upstream-two.test/v1/chat/completions",
        ]
        assert "key_states" not in health.json()


def test_cloudflare_521_retries_and_returns_structured_error() -> None:
    with tempfile.TemporaryDirectory() as directory:
        calls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            calls.append(str(request.url))
            return httpx.Response(
                521,
                content=b"<html><title>Web server is down</title></html>",
                headers={"content-type": "text/html"},
            )

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "alias-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 521
        assert response.headers["content-type"].startswith("application/json")
        assert calls == [
            "https://upstream-one.test/v1/chat/completions",
            "https://upstream-two.test/v1/chat/completions",
        ]
        assert response.json() == {
            "error": {
                "message": "上游服务不可用：Cloudflare 521 Web Server Is Down：Cloudflare 无法连接到上游源站，通常表示源站服务离线、端口未监听或防火墙拒绝 Cloudflare 连接。",
                "type": "upstream_cloudflare_error",
                "code": "cloudflare_521",
                "status_code": 521,
                "reason": "Cloudflare 无法连接到上游源站，通常表示源站服务离线、端口未监听或防火墙拒绝 Cloudflare 连接。",
            }
        }


def test_stream_request_disables_upstream_read_timeout() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_timeouts: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_timeouts.append(request.extensions["timeout"])
            return httpx.Response(
                200, content=b'data: {"usage":{"total_tokens":1}}\n\ndata: [DONE]\n'
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                    "stream": True,
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert response.text == 'data: {"usage":{"total_tokens":1}}\n\ndata: [DONE]\n'
        assert upstream_timeouts[0]["read"] is None
        assert upstream_timeouts[0]["connect"] == config.request_timeout


def test_stream_response_header_timeout_retries_next_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        calls: list[str] = []

        async def handler(request: httpx.Request) -> httpx.Response:
            authorization = request.headers["authorization"]
            calls.append(authorization)
            if authorization == "Bearer sk-1":
                await anyio.sleep(0.05)
            return httpx.Response(200, content=b"data: [DONE]\n\n")

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
            stream_first_byte_timeout=0.01,
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "messages": [], "stream": True},
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert response.text == "data: [DONE]\n\n"
        assert calls == ["Bearer sk-1", "Bearer sk-2"]


@pytest.mark.parametrize(
    ("path", "payload"),
    [
        ("chat/completions", {"model": "test-model", "messages": [], "stream": True}),
        (
            "messages",
            {"model": "test-model", "messages": [], "max_tokens": 16, "stream": True},
        ),
        ("responses", {"model": "test-model", "input": "hi", "stream": True}),
    ],
)
def test_protocol_stream_idle_timeout_records_failure_and_releases_key(
    path: str, payload: dict[str, object]
) -> None:
    with tempfile.TemporaryDirectory() as directory:
        stream_calls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            body = json.loads(request.content)
            if request.url.path.endswith("/v1/responses") and not body.get("stream"):
                return httpx.Response(404, json={"error": "unsupported"})
            stream_calls.append(str(request.url))
            return httpx.Response(
                200,
                headers={"content-type": "text/event-stream"},
                stream=HangingStream(),
            )

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            stream_idle_timeout=0.01,
        )
        app = create_app(config)
        runtime = app.state.runtime_manager.current
        runtime.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, dict[str, object], dict[tuple[str, str], int]]:
            response = await client.post(
                f"/v1/{path}",
                headers={"Authorization": "Bearer local-key"},
                json=payload,
            )
            snapshot = await runtime.metrics.snapshot()
            return response, snapshot, dict(runtime.key_pool._active_requests)

        response, snapshot, active_requests = run_client(app, requests)

        assert response.status_code == 200
        assert len(stream_calls) == 1
        assert snapshot["keys"]["test-model"]["key-1"]["failures"] == 1
        assert active_requests == {}


def test_stream_body_first_byte_timeout_does_not_replay_request() -> None:
    with tempfile.TemporaryDirectory() as directory:
        calls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            calls.append(request.headers["authorization"])
            return httpx.Response(
                200,
                headers={"content-type": "text/event-stream"},
                stream=HangingStream(delay_first=True),
            )

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
            stream_first_byte_timeout=0.01,
        )
        app = create_app(config)
        runtime = app.state.runtime_manager.current
        runtime.http_client = httpx.AsyncClient(transport=httpx.MockTransport(handler))

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, dict[str, object], dict[tuple[str, str], int]]:
            response = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "messages": [], "stream": True},
            )
            snapshot = await runtime.metrics.snapshot()
            return response, snapshot, dict(runtime.key_pool._active_requests)

        response, snapshot, active_requests = run_client(app, requests)

        assert response.status_code == 200
        assert calls == ["Bearer sk-1"]
        assert snapshot["keys"]["test-model"]["key-1"]["failures"] == 1
        assert active_requests == {}


def test_chat_completions_stream_splits_and_flushes_sse_events(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    with tempfile.TemporaryDirectory() as directory:
        flushes: list[None] = []

        async def record_flush() -> None:
            flushes.append(None)

        monkeypatch.setattr(
            "auto_model_key_router.proxy_handler._flush_stream_event", record_flush
        )

        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                headers={"content-type": "text/event-stream"},
                stream=ChunkedStream(
                    (
                        b'data: {"choices":[{"delta":{"content":"one"}}]}\n\ndata: {"choices":',
                        b'[{"delta":{"content":"two"}}]}\n\ndata: [DONE]\n\n',
                    )
                ),
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                    "stream": True,
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert response.headers["cache-control"] == "no-cache"
        assert response.headers["x-accel-buffering"] == "no"
        assert response.content == (
            b'data: {"choices":[{"delta":{"content":"one"}}]}\n\n'
            b'data: {"choices":[{"delta":{"content":"two"}}]}\n\n'
            b"data: [DONE]\n\n"
        )
        assert len(flushes) == 3


def test_messages_response_is_converted_to_anthropic_schema() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(
                200,
                json={
                    "id": "chatcmpl-1",
                    "choices": [
                        {
                            "message": {"role": "assistant", "content": "hello"},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {"prompt_tokens": 3, "completion_tokens": 2},
                },
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/messages",
                headers={"x-api-key": "local-key", "anthropic-version": "2023-06-01"},
                json={
                    "model": "alias-model",
                    "system": "be brief",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)
        body = response.json()

        assert response.status_code == 200
        assert upstream_bodies[0]["model"] == "test-model"
        assert upstream_bodies[0]["messages"] == [
            {"role": "system", "content": "be brief"},
            {"role": "user", "content": "hi"},
        ]
        assert body == {
            "id": "chatcmpl-1",
            "type": "message",
            "role": "assistant",
            "model": "alias-model",
            "content": [{"type": "text", "text": "hello"}],
            "stop_reason": "end_turn",
            "stop_sequence": None,
            "usage": {"input_tokens": 3, "output_tokens": 2},
        }


def test_messages_tools_and_tool_history_are_converted_both_ways() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(
                200,
                json={
                    "id": "chatcmpl-tool",
                    "choices": [
                        {
                            "message": {
                                "role": "assistant",
                                "content": None,
                                "tool_calls": [
                                    {
                                        "id": "call-edit",
                                        "type": "function",
                                        "function": {
                                            "name": "Edit",
                                            "arguments": '{"path":"app.py","old":"a","new":"b"}',
                                        },
                                    }
                                ],
                            },
                            "finish_reason": "tool_calls",
                        }
                    ],
                    "usage": {"prompt_tokens": 8, "completion_tokens": 4},
                },
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/messages",
                headers={"x-api-key": "local-key"},
                json={
                    "model": "test-model",
                    "messages": [
                        {
                            "role": "assistant",
                            "content": [
                                {"type": "text", "text": "Checking the file."},
                                {
                                    "type": "tool_use",
                                    "id": "toolu-grep",
                                    "name": "Grep",
                                    "input": {"pattern": "needle"},
                                },
                            ],
                        },
                        {
                            "role": "user",
                            "content": [
                                {
                                    "type": "tool_result",
                                    "tool_use_id": "toolu-grep",
                                    "content": [{"type": "text", "text": "app.py:10"}],
                                }
                            ],
                        },
                    ],
                    "tools": [
                        {
                            "name": "Grep",
                            "description": "Search files",
                            "input_schema": {
                                "type": "object",
                                "properties": {"pattern": {"type": "string"}},
                                "required": ["pattern"],
                            },
                        }
                    ],
                    "tool_choice": {
                        "type": "tool",
                        "name": "Grep",
                        "disable_parallel_tool_use": True,
                    },
                },
            )

        response = run_client(app, requests)
        upstream = upstream_bodies[0]

        assert upstream["tools"] == [
            {
                "type": "function",
                "function": {
                    "name": "Grep",
                    "description": "Search files",
                    "parameters": {
                        "type": "object",
                        "properties": {"pattern": {"type": "string"}},
                        "required": ["pattern"],
                    },
                },
            }
        ]
        assert upstream["tool_choice"] == {
            "type": "function",
            "function": {"name": "Grep"},
        }
        assert upstream["parallel_tool_calls"] is False
        assert upstream["messages"] == [
            {
                "role": "assistant",
                "content": [{"type": "text", "text": "Checking the file."}],
                "tool_calls": [
                    {
                        "id": "toolu-grep",
                        "type": "function",
                        "function": {
                            "name": "Grep",
                            "arguments": '{"pattern":"needle"}',
                        },
                    }
                ],
            },
            {"role": "tool", "tool_call_id": "toolu-grep", "content": "app.py:10"},
        ]
        assert response.json() == {
            "id": "chatcmpl-tool",
            "type": "message",
            "role": "assistant",
            "model": "test-model",
            "content": [
                {
                    "type": "tool_use",
                    "id": "call-edit",
                    "name": "Edit",
                    "input": {"path": "app.py", "old": "a", "new": "b"},
                }
            ],
            "stop_reason": "tool_use",
            "stop_sequence": None,
            "usage": {"input_tokens": 8, "output_tokens": 4},
        }


def test_messages_round_robin_sticks_by_prompt_cache_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_authorizations: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_authorizations.append(request.headers["authorization"])
            return httpx.Response(
                200,
                json={
                    "id": "chatcmpl-1",
                    "choices": [
                        {
                            "message": {"role": "assistant", "content": "ok"},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {"prompt_tokens": 3, "completion_tokens": 1},
                },
            )

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-a", "sk-a", "https://upstream.test"),
                KeyConfig("key-b", "sk-b", "https://upstream.test"),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> list[int]:
            statuses = []
            for cache_key in ("session-1", "session-1", "session-2"):
                response = await client.post(
                    "/v1/messages",
                    headers={"Authorization": "Bearer local-key"},
                    json={
                        "model": "test-model",
                        "prompt_cache_key": cache_key,
                        "messages": [{"role": "user", "content": "hi"}],
                    },
                )
                statuses.append(response.status_code)
            return statuses

        assert run_client(app, requests) == [200, 200, 200]
        assert upstream_authorizations == [
            "Bearer sk-a",
            "Bearer sk-a",
            "Bearer sk-b",
        ]


def test_messages_round_robin_affinity_uses_full_message_list() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_authorizations: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_authorizations.append(request.headers["authorization"])
            return httpx.Response(
                200,
                json={
                    "id": "chatcmpl-1",
                    "choices": [
                        {
                            "message": {"role": "assistant", "content": "ok"},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {"prompt_tokens": 3, "completion_tokens": 1},
                },
            )

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-a", "sk-a", "https://upstream.test"),
                KeyConfig("key-b", "sk-b", "https://upstream.test"),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> list[int]:
            statuses = []
            for branch in ("branch a", "branch b"):
                response = await client.post(
                    "/v1/messages",
                    headers={"Authorization": "Bearer local-key"},
                    json={
                        "model": "test-model",
                        "messages": [
                            {"role": "user", "content": "same opening"},
                            {"role": "assistant", "content": branch},
                        ],
                    },
                )
                statuses.append(response.status_code)
            return statuses

        assert run_client(app, requests) == [200, 200]
        assert upstream_authorizations == [
            "Bearer sk-a",
            "Bearer sk-b",
        ]


def test_messages_stream_is_converted_to_anthropic_sse() -> None:
    with tempfile.TemporaryDirectory() as directory:

        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                headers={"content-type": "text/event-stream"},
                content=(
                    b'data: {"id":"chatcmpl-1","choices":[{"delta":{"role":"assistant"}}]}\n\n'
                    b'data: {"choices":[{"delta":{"content":"hel"}}]}\n\n'
                    b'data: {"choices":[{"delta":{"content":"lo"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}\n\n'
                    b"data: [DONE]\n\n"
                ),
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/messages",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                    "stream": True,
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert "data: [DONE]" not in response.text
        assert "event: message_start" in response.text
        assert "event: content_block_start" in response.text
        assert '"text":"hel"' in response.text
        assert '"text":"lo"' in response.text
        assert "event: message_stop" in response.text


def test_messages_stream_converts_openai_tool_calls_to_anthropic_tool_use() -> None:
    with tempfile.TemporaryDirectory() as directory:

        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                headers={"content-type": "text/event-stream"},
                stream=ChunkedStream(
                    (
                        b'data: {"choices":[{"delta":{"content":"I will inspect it."}}]}\n\n',
                        b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-grep","type":"function","function":{"name":"Grep","arguments":"{\\"pattern\\":"}}]}}]}\n\n',
                        b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\\"needle\\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}\n\n',
                        b"data: [DONE]\n\n",
                    )
                ),
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/messages",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "find it"}],
                    "tools": [{"name": "Grep", "input_schema": {"type": "object"}}],
                    "stream": True,
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert response.headers["cache-control"] == "no-cache"
        assert response.headers["x-accel-buffering"] == "no"
        assert '"type":"text_delta","text":"I will inspect it."' in response.text
        assert (
            '"type":"tool_use","id":"call-grep","name":"Grep","input":{}'
            in response.text
        )
        assert (
            '"type":"input_json_delta","partial_json":"{\\"pattern\\":"}'
            in response.text
        )
        assert (
            '"type":"input_json_delta","partial_json":"\\"needle\\"}"}' in response.text
        )
        assert '"stop_reason":"tool_use"' in response.text
        text_delta = response.text.index('"type":"text_delta"')
        text_stop = response.text.index("event: content_block_stop", text_delta)
        tool_start = response.text.index('"type":"tool_use"')
        first_tool_delta = response.text.index('"type":"input_json_delta"')
        second_tool_delta = response.text.index(
            '"type":"input_json_delta"', first_tool_delta + 1
        )
        tool_stop = response.text.index("event: content_block_stop", tool_start)
        assert (
            text_delta
            < text_stop
            < tool_start
            < first_tool_delta
            < second_tool_delta
            < tool_stop
        )


def test_anthropic_stream_yields_between_sse_events(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    with tempfile.TemporaryDirectory() as directory:
        flushes: list[None] = []

        async def record_flush() -> None:
            flushes.append(None)

        monkeypatch.setattr(
            "auto_model_key_router.proxy_handler._flush_stream_event", record_flush
        )
        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        response = httpx.Response(
            200,
            headers={"content-type": "text/event-stream"},
            stream=ChunkedStream(
                (
                    b'data: {"choices":[{"delta":{"content":"one"}}]}\n\n'
                    b'data: {"choices":[{"delta":{"content":"two"},"finish_reason":"stop"}]}\n\n'
                    b"data: [DONE]\n\n",
                )
            ),
        )

        async def consume_stream() -> list[bytes]:
            chunks = []
            async for chunk in _stream_anthropic_messages(
                response,
                app.state.runtime_manager.current.metrics,
                app.state.runtime_manager.current.key_pool,
                "test-model",
                "key-1",
                "test-model",
                "https://upstream.test/v1/chat/completions",
                perf_counter(),
                first_byte_deadline=asyncio.get_running_loop().time() + 1,
                idle_timeout=1,
            ):
                chunks.append(chunk)
            await app.state.runtime_manager.current.metrics.close()
            await app.state.runtime_manager.current.http_client.aclose()
            return chunks

        chunks = anyio.run(consume_stream)
        body = b"".join(chunks)

        assert body.count(b'"type":"text_delta"') == 2
        assert len(flushes) == 5


def test_messages_non_json_upstream_error_returns_anthropic_json() -> None:
    with tempfile.TemporaryDirectory() as directory:

        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                502,
                content=b"<html>bad gateway</html>",
                headers={"content-type": "text/html"},
            )

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            max_retries=0,
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/messages",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 502
        assert response.headers["content-type"].startswith("application/json")
        assert response.json() == {
            "type": "error",
            "error": {"type": "api_error", "message": "<html>bad gateway</html>"},
        }


def test_messages_cloudflare_521_returns_structured_anthropic_json() -> None:
    with tempfile.TemporaryDirectory() as directory:

        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                521,
                content=b"<html><title>Web server is down</title></html>",
                headers={"content-type": "text/html"},
            )

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            max_retries=0,
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/messages",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 521
        assert response.headers["content-type"].startswith("application/json")
        assert response.json() == {
            "type": "error",
            "error": {
                "type": "api_error",
                "message": "上游服务不可用：Cloudflare 521 Web Server Is Down：Cloudflare 无法连接到上游源站，通常表示源站服务离线、端口未监听或防火墙拒绝 Cloudflare 连接。",
                "code": "cloudflare_521",
                "status_code": 521,
                "reason": "Cloudflare 无法连接到上游源站，通常表示源站服务离线、端口未监听或防火墙拒绝 Cloudflare 连接。",
            },
        }


def test_keys_for_model_excludes_disabled_keys() -> None:
    with tempfile.TemporaryDirectory() as directory:
        enabled_key = KeyConfig("enabled-key", "sk-enabled", "https://enabled.test")
        disabled_key = KeyConfig(
            "disabled-key", "sk-disabled", "https://disabled.test", enabled=False
        )
        key_pool = KeyPool(make_config(Path(directory), (enabled_key, disabled_key)))

        assert key_pool.keys_for_model("alias-model") == (enabled_key,)


def test_key_by_name_rejects_disabled_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        disabled_key = KeyConfig(
            "disabled-key", "sk-disabled", "https://disabled.test", enabled=False
        )
        key_pool = KeyPool(make_config(Path(directory), (disabled_key,)))

        with pytest.raises(RuntimeError):
            key_pool.key_by_name("alias-model", "disabled-key")


def test_round_robin_prefers_key_with_lower_active_load() -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
        )
        key_pool = KeyPool(config)

        async def choose_keys() -> tuple[str, str, str]:
            first = await key_pool.next_key("test-model")
            second = await key_pool.next_key("test-model")
            await key_pool.release_key("test-model", second.name)
            third = await key_pool.next_key("test-model")
            return first.name, second.name, third.name

        assert anyio.run(choose_keys) == ("key-1", "key-2", "key-2")


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
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            await app.state.runtime_manager.current.key_pool.mark_failure(
                "test-model", "key-1", 429, 120
            )
            return await client.post(
                "/v1/chat/completions",
                headers={"x-api-key": "local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert calls == ["https://upstream-two.test/v1/chat/completions"]


def test_cooled_key_is_skipped_without_being_disabled() -> None:
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
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, httpx.Response]:
            for _ in range(5):
                await app.state.runtime_manager.current.key_pool.mark_failure(
                    "test-model", "key-1", 429, 10
                )
            response = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            health = await client.get("/health")
            return response, health

        response, health = run_client(app, requests)

        assert response.status_code == 200
        assert calls == ["https://upstream-two.test/v1/chat/completions"]
        assert "key_states" not in health.json()


def test_only_first_retries_only_first_key_until_max_retries() -> None:
    with tempfile.TemporaryDirectory() as directory:
        calls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            calls.append(str(request.url))
            return httpx.Response(429, json={"error": {"message": "rate limited"}})

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
            routing_mode="only_first",
            max_retries=2,
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 429
        assert calls == ["https://upstream-one.test/v1/chat/completions"] * 3


def test_model_suffix_selects_explicit_key_by_alias() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []
        authorization_headers: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            authorization_headers.append(request.headers["Authorization"])
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "alias-model[key-2]",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_bodies[0]["model"] == "test-model"
        assert authorization_headers == ["Bearer sk-2"]


def test_unified_model_routes_to_configured_model_and_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []
        authorization_headers: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            authorization_headers.append(request.headers["Authorization"])
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory),
            (
                KeyConfig("key-1", "sk-1", "https://upstream-one.test"),
                KeyConfig("key-2", "sk-2", "https://upstream-two.test"),
            ),
            unified_model=UnifiedModelConfig(model="alias-model", key="key-2"),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, httpx.Response, httpx.Response]:
            models = await client.get(
                "/v1/models", headers={"Authorization": "Bearer local-key"}
            )
            health = await client.get("/health")
            response = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": UNIFIED_MODEL_ID,
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            return models, health, response

        models, health, response = run_client(app, requests)

        assert response.status_code == 200
        assert UNIFIED_MODEL_ID in {model["id"] for model in models.json()["data"]}
        assert health.json()["unified_model"] == {"model": "test-model", "key": "key-2"}
        assert upstream_bodies[0]["model"] == "test-model"
        assert authorization_headers == ["Bearer sk-2"]


def test_switch_unified_model_hot_reloads_model_and_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        tmp_path = Path(directory)
        config_path = tmp_path / "router-config.json"
        config_data = {
            "request_timeout": 10,
            "max_retries": 1,
            "endpoint_capabilities_path": str(tmp_path / "endpoint-capabilities.json"),
            "upstream_health_check_interval": 0,
            "metrics_db_path": str(tmp_path / "metrics.sqlite3"),
            "log_file_path": str(tmp_path / "server.log"),
            "local_api_key": "local-key",
            "unified_model": {"model": "model-one", "key": "one"},
            "models": [
                {
                    "id": "model-one",
                    "aliases": ["first"],
                    "keys": [
                        {
                            "name": "one",
                            "api_key": "sk-one",
                            "base_url": "https://one.test",
                        }
                    ],
                },
                {
                    "id": "model-two",
                    "aliases": ["second"],
                    "keys": [
                        {
                            "name": "two-a",
                            "api_key": "sk-two-a",
                            "base_url": "https://two-a.test",
                        },
                        {
                            "name": "two-b",
                            "api_key": "sk-two-b",
                            "base_url": "https://two-b.test",
                        },
                    ],
                },
            ],
        }
        config_path.write_text(json.dumps(config_data, indent=2), encoding="utf-8")
        app = create_app(RouterConfig.load(config_path), config_path)
        upstream_models: list[str] = []
        authorization_headers: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_models.append(json.loads(request.content.decode("utf-8"))["model"])
            authorization_headers.append(request.headers["Authorization"])
            return httpx.Response(200, json={"id": "ok"})

        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, httpx.Response]:
            first = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": UNIFIED_MODEL_ID,
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            switched = switch_unified_model(
                config_path, "second", "two-b", update_key=True
            )
            assert switched.unified_model == UnifiedModelConfig(
                model="model-two", key="two-b"
            )
            await anyio.sleep(0.01)
            second = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": UNIFIED_MODEL_ID,
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            return first, second

        first, second = run_client(app, requests)

        assert first.status_code == 200
        assert second.status_code == 200
        assert upstream_models == ["model-one", "model-two"]
        assert authorization_headers == ["Bearer sk-one", "Bearer sk-two-b"]
        assert json.loads(config_path.read_text(encoding="utf-8"))["unified_model"] == {
            "model": "model-two",
            "key": "two-b",
        }


def test_switching_unified_model_clears_old_key_and_can_restore_auto_routing(
    tmp_path: Path,
) -> None:
    config_path = tmp_path / "router-config.json"
    config_path.write_text(
        json.dumps(
            {
                "local_api_key": "local-key",
                "unified_model": {"model": "model-one", "key": "shared"},
                "models": [
                    {
                        "id": "model-one",
                        "keys": [
                            {
                                "name": "shared",
                                "api_key": "sk-one",
                                "base_url": "https://one.test",
                            }
                        ],
                    },
                    {
                        "id": "model-two",
                        "aliases": ["second"],
                        "keys": [
                            {
                                "name": "shared",
                                "api_key": "sk-two",
                                "base_url": "https://two.test",
                            }
                        ],
                    },
                ],
            },
            indent=2,
        ),
        encoding="utf-8",
    )

    switched_model = switch_unified_model(config_path, "second")
    switched_key = switch_unified_model(config_path, key_name="shared", update_key=True)
    automatic = switch_unified_model(config_path, key_name=None, update_key=True)

    assert switched_model.unified_model == UnifiedModelConfig(model="model-two")
    assert switched_key.unified_model == UnifiedModelConfig(
        model="model-two", key="shared"
    )
    assert automatic.unified_model == UnifiedModelConfig(model="model-two")


def test_unified_model_rejects_unknown_or_disabled_key() -> None:
    with pytest.raises(ValueError, match="未配置可用 key"):
        RouterConfig.from_dict(
            {
                "unified_model": {"model": "test-model", "key": "disabled"},
                "models": [
                    {
                        "id": "test-model",
                        "keys": [
                            {
                                "name": "disabled",
                                "api_key": "sk-disabled",
                                "base_url": "https://upstream.test",
                                "enabled": False,
                            }
                        ],
                    }
                ],
            }
        )


def test_acquired_key_is_released_when_proxy_raises() -> None:
    class FailingMetrics:
        _active_count: int = 0

        async def record(self, *args: object, **kwargs: object) -> None:
            raise RuntimeError("metrics unavailable")

        def acquire_active(self) -> None:
            self._active_count += 1

        def release_active(self) -> None:
            self._active_count = max(self._active_count - 1, 0)

        @property
        def active_count(self) -> int:
            return self._active_count

        async def close(self) -> None:
            pass

    with tempfile.TemporaryDirectory() as directory:

        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        anyio.run(app.state.runtime_manager.current.metrics.close)
        app.state.runtime_manager.current.metrics = FailingMetrics()
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> None:
            with pytest.raises(RuntimeError, match="metrics unavailable"):
                await client.post(
                    "/v1/chat/completions",
                    headers={"Authorization": "Bearer local-key"},
                    json={
                        "model": "test-model",
                        "messages": [{"role": "user", "content": "hi"}],
                    },
                )

        run_client(app, requests)

        assert dict(app.state.runtime_manager.current.key_pool._active_requests) == {}


def test_destination_addr_header_is_not_forwarded() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_headers: list[httpx.Headers] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_headers.append(request.headers)
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={
                    "Authorization": "Bearer local-key",
                    "destination-addr": "127.0.0.1:28881",
                },
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert "destination-addr" not in upstream_headers[0]


def test_anthropic_auth_headers_are_not_forwarded() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_headers: list[httpx.Headers] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_headers.append(request.headers)
            return httpx.Response(
                200, json={"id": "ok", "choices": [{"message": {"content": "ok"}}]}
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/messages",
                headers={
                    "x-api-key": "local-key",
                    "anthropic-version": "2023-06-01",
                    "anthropic-beta": "test-beta",
                },
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_headers[0]["authorization"] == "Bearer sk-1"
        assert "x-api-key" not in upstream_headers[0]
        assert "anthropic-version" not in upstream_headers[0]
        assert "anthropic-beta" not in upstream_headers[0]


def test_custom_anthropic_upstream_route_uses_native_messages_path() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_calls: list[tuple[str, dict[str, object], httpx.Headers]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_calls.append(
                (
                    str(request.url),
                    json.loads(request.content.decode("utf-8")),
                    request.headers,
                )
            )
            return httpx.Response(
                200,
                json={
                    "id": "msg-native",
                    "type": "message",
                    "role": "assistant",
                    "model": "test-model",
                    "content": [{"type": "text", "text": "hello"}],
                    "stop_reason": "end_turn",
                    "stop_sequence": None,
                    "usage": {"input_tokens": 1, "output_tokens": 1},
                },
            )

        key = KeyConfig("key-1", "sk-1", "https://upstream.test")
        config = make_config(
            Path(directory),
            (key,),
            upstream_routes={"https://upstream.test": {"anthropic": "anthropic/"}},
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/messages",
                headers={
                    "x-api-key": "local-key",
                    "anthropic-version": "2023-06-01",
                },
                json={
                    "model": "alias-model",
                    "system": "be brief",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert [url for url, _, _ in upstream_calls] == [
            "https://upstream.test/anthropic/v1/messages",
            "https://upstream.test/anthropic/v1/messages",
        ]
        assert upstream_calls[1][1]["model"] == "test-model"
        assert upstream_calls[1][1]["system"] == "be brief"
        assert upstream_calls[1][2]["anthropic-version"] == "2023-06-01"


def test_stream_error_logs_upstream_context(caplog: pytest.LogCaptureFixture) -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        response = httpx.Response(
            200, headers={"content-type": "text/event-stream"}, stream=BrokenStream()
        )

        async def consume_stream() -> list[bytes]:
            chunks = []
            async for chunk in _stream_upstream(
                response,
                app.state.runtime_manager.current.metrics,
                app.state.runtime_manager.current.key_pool,
                "test-model",
                "key-1",
                "test-model",
                "https://upstream.test/v1/chat/completions",
                perf_counter(),
                first_byte_deadline=asyncio.get_running_loop().time() + 1,
                idle_timeout=1,
            ):
                chunks.append(chunk)
            await app.state.runtime_manager.current.metrics.close()
            await app.state.runtime_manager.current.http_client.aclose()
            return chunks

        caplog.set_level(logging.WARNING, logger="auto_model_key_router.app")
        chunks = anyio.run(consume_stream)

        assert chunks == [b'data: {"id":"chunk"}\n']
        message = next(
            record.message
            for record in caplog.records
            if record.name == "auto_model_key_router.app"
        )
        assert "upstream stream error" in message
        assert "test-model" in message
        assert "key-1" in message
        assert "https://upstream.test/v1/chat/completions" in message
        assert "DecodingError" in message


def test_anthropic_stream_error_logs_upstream_context(
    caplog: pytest.LogCaptureFixture,
) -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        response = httpx.Response(
            200, headers={"content-type": "text/event-stream"}, stream=BrokenStream()
        )

        async def consume_stream() -> list[bytes]:
            chunks = []
            async for chunk in _stream_anthropic_messages(
                response,
                app.state.runtime_manager.current.metrics,
                app.state.runtime_manager.current.key_pool,
                "test-model",
                "key-1",
                "test-model",
                "https://upstream.test/v1/chat/completions",
                perf_counter(),
                first_byte_deadline=asyncio.get_running_loop().time() + 1,
                idle_timeout=1,
            ):
                chunks.append(chunk)
            await app.state.runtime_manager.current.metrics.close()
            await app.state.runtime_manager.current.http_client.aclose()
            return chunks

        caplog.set_level(logging.WARNING, logger="auto_model_key_router.app")
        chunks = anyio.run(consume_stream)

        assert chunks == [
            b'event: message_start\ndata: {"type":"message_start","message":{"id":"msg_amkr","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}\n\n'
        ]
        message = next(
            record.message
            for record in caplog.records
            if record.name == "auto_model_key_router.app"
        )
        assert "upstream anthropic stream error" in message
        assert "test-model" in message
        assert "key-1" in message
        assert "https://upstream.test/v1/chat/completions" in message
        assert "DecodingError" in message


def test_model_suffix_returns_error_for_unknown_key() -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model[missing-key]",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 503
        assert (
            response.json()["error"]["message"]
            == "模型 test-model 未配置 key: missing-key"
        )


def test_duplicate_key_name_is_rejected() -> None:
    with pytest.raises(ValueError, match="key name 重复"):
        RouterConfig.from_dict(
            {
                "models": [
                    {
                        "id": "test-model",
                        "keys": [
                            {"name": "same-key", "api_key": "sk-1"},
                            {"name": "same-key", "api_key": "sk-2"},
                        ],
                    }
                ]
            }
        )


def test_expired_cooldown_allows_key_selection(monkeypatch) -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        monkeypatch.setattr("auto_model_key_router.key_pool.time", lambda: 1000.0)
        app = create_app(config)
        pool = app.state.runtime_manager.current.key_pool

        anyio.run(pool.mark_failure, "test-model", "key-1", 429, 120)
        pool._health._clock = lambda: 1121.0
        selected = anyio.run(pool.next_key, "test-model")
        anyio.run(pool.release_key, "test-model", selected.name)

        anyio.run(app.state.runtime_manager.current.metrics.close)
        anyio.run(app.state.runtime_manager.current.http_client.aclose)

        assert selected.name == "key-1"


def test_config_reasoning_effort_is_forwarded() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            reasoning_effort="xhigh",
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "alias-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_bodies[0]["model"] == "test-model"
        assert upstream_bodies[0]["reasoning_effort"] == "xhigh"


def test_config_reasoning_effort_overrides_request_reasoning() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            if str(request.url) == "https://upstream.test/v1/responses":
                return httpx.Response(404, json={"error": {"message": "missing"}})
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            reasoning_effort="high",
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "input": "hi",
                    "reasoning": {"effort": "low"},
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_bodies[0]["reasoning_effort"] == "high"
        assert "reasoning" not in upstream_bodies[0]


def test_responses_request_and_response_are_converted_for_codex() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            if str(request.url) == "https://upstream.test/v1/responses":
                return httpx.Response(404, json={"error": {"message": "missing"}})
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(
                200,
                json={
                    "id": "chatcmpl-test",
                    "choices": [
                        {
                            "finish_reason": "tool_calls",
                            "message": {
                                "role": "assistant",
                                "content": "checking",
                                "tool_calls": [
                                    {
                                        "id": "call-2",
                                        "type": "function",
                                        "function": {
                                            "name": "read_file",
                                            "arguments": '{"path":"README.md"}',
                                        },
                                    }
                                ],
                            },
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 10,
                        "completion_tokens": 4,
                        "total_tokens": 14,
                    },
                },
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "instructions": "be concise",
                    "input": [
                        {
                            "type": "message",
                            "role": "user",
                            "content": [{"type": "input_text", "text": "inspect"}],
                        },
                        {
                            "type": "function_call",
                            "call_id": "call-1",
                            "name": "shell",
                            "arguments": '{"command":"pwd"}',
                        },
                        {
                            "type": "function_call_output",
                            "call_id": "call-1",
                            "output": "D:/Code",
                        },
                    ],
                    "tools": [
                        {
                            "type": "function",
                            "name": "read_file",
                            "description": "Read a file",
                            "parameters": {
                                "type": "object",
                                "properties": {"path": {"type": "string"}},
                            },
                        }
                    ],
                    "tool_choice": {"type": "function", "name": "read_file"},
                },
            )

        response = run_client(app, request)

        assert response.status_code == 200
        upstream = upstream_bodies[0]
        assert upstream["messages"][0] == {"role": "system", "content": "be concise"}
        assert upstream["messages"][2]["tool_calls"][0]["id"] == "call-1"
        assert upstream["messages"][3] == {
            "role": "tool",
            "tool_call_id": "call-1",
            "content": "D:/Code",
        }
        assert upstream["tools"][0]["function"]["name"] == "read_file"
        assert upstream["tool_choice"] == {
            "type": "function",
            "function": {"name": "read_file"},
        }
        data = response.json()
        assert data["object"] == "response"
        assert data["model"] == "test-model"
        assert data["output"][0]["content"][0]["text"] == "checking"
        assert data["output"][1]["type"] == "function_call"
        assert data["output"][1]["call_id"] == "call-2"
        assert data["usage"]["total_tokens"] == 14


def test_responses_default_route_probes_native_before_fallback() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_calls: list[tuple[str, dict[str, object]]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            url = str(request.url)
            body = json.loads(request.content.decode("utf-8"))
            upstream_calls.append((url, body))
            if url == "https://upstream.test/v1/responses":
                return httpx.Response(404, json={"error": {"message": "missing"}})
            return httpx.Response(
                200,
                json={
                    "id": "chatcmpl-fallback",
                    "choices": [
                        {
                            "finish_reason": "stop",
                            "message": {
                                "role": "assistant",
                                "content": "fallback",
                            },
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 2,
                        "completion_tokens": 1,
                        "total_tokens": 3,
                    },
                },
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "alias-model", "input": "inspect"},
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert upstream_calls == [
            (
                "https://upstream.test/v1/responses",
                {
                    "model": "test-model",
                    "input": "test",
                    "max_output_tokens": 1,
                },
            ),
            (
                "https://upstream.test/v1/chat/completions",
                {
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "inspect"}],
                },
            ),
        ]
        assert response.json()["output"][0]["content"][0]["text"] == "fallback"


def test_custom_responses_upstream_route_uses_native_responses_path() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_calls: list[tuple[str, dict[str, object]]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_calls.append(
                (str(request.url), json.loads(request.content.decode("utf-8")))
            )
            return httpx.Response(
                200,
                json={
                    "id": "resp-native",
                    "object": "response",
                    "model": "test-model",
                    "output": [],
                    "usage": {
                        "input_tokens": 1,
                        "output_tokens": 0,
                        "total_tokens": 1,
                    },
                },
            )

        key = KeyConfig("key-1", "sk-1", "https://upstream.test")
        config = make_config(
            Path(directory),
            (key,),
            upstream_routes={"https://upstream.test": {"responses": "responses/"}},
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "alias-model", "input": "inspect"},
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert upstream_calls == [
            (
                "https://upstream.test/responses/v1/responses",
                {
                    "model": "test-model",
                    "input": "test",
                    "max_output_tokens": 1,
                },
            ),
            (
                "https://upstream.test/responses/v1/responses",
                {"model": "test-model", "input": "inspect"},
            ),
        ]
        assert response.json()["object"] == "response"
        assert response.json()["id"] == "resp-native"


def test_custom_responses_upstream_route_falls_back_when_native_probe_fails() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_calls: list[tuple[str, dict[str, object]]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            url = str(request.url)
            body = json.loads(request.content.decode("utf-8"))
            upstream_calls.append((url, body))
            if url == "https://upstream.test/responses/v1/responses":
                return httpx.Response(404, json={"error": {"message": "missing"}})
            return httpx.Response(
                200,
                json={
                    "id": "chatcmpl-fallback",
                    "choices": [
                        {
                            "finish_reason": "stop",
                            "message": {
                                "role": "assistant",
                                "content": "fallback",
                            },
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 2,
                        "completion_tokens": 1,
                        "total_tokens": 3,
                    },
                },
            )

        key = KeyConfig("key-1", "sk-1", "https://upstream.test")
        config = make_config(
            Path(directory),
            (key,),
            upstream_routes={"https://upstream.test": {"responses": "responses/"}},
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "alias-model", "input": "inspect"},
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert upstream_calls == [
            (
                "https://upstream.test/responses/v1/responses",
                {
                    "model": "test-model",
                    "input": "test",
                    "max_output_tokens": 1,
                },
            ),
            (
                "https://upstream.test/v1/chat/completions",
                {
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "inspect"}],
                },
            ),
        ]
        data = response.json()
        assert data["object"] == "response"
        assert data["id"] == "chatcmpl-fallback"
        assert data["output"][0]["content"][0]["text"] == "fallback"


def test_responses_stream_is_converted_to_codex_sse() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream = "\n\n".join(
            [
                'data: {"choices":[{"delta":{"content":"hello "}}]}',
                'data: {"choices":[{"delta":{"content":"world","tool_calls":[{"index":0,"id":"call-1","function":{"name":"shell","arguments":"{\\"command\\":\\"pwd\\"}"}}]},"finish_reason":"tool_calls"}]}',
                'data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}',
                "data: [DONE]",
                "",
            ]
        ).encode("utf-8")

        def handler(request: httpx.Request) -> httpx.Response:
            if str(request.url) == "https://upstream.test/v1/responses":
                return httpx.Response(404, json={"error": {"message": "missing"}})
            return httpx.Response(
                200, content=upstream, headers={"content-type": "text/event-stream"}
            )

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "input": "hi", "stream": True},
            )

        response = run_client(app, request)
        body = response.text

        assert response.status_code == 200
        assert "event: response.output_text.delta" in body
        assert '"delta":"hello "' in body
        assert '"type":"function_call"' in body
        assert '"call_id":"call-1"' in body
        assert "event: response.completed" in body
        assert '"total_tokens":5' in body


def test_downstream_reasoning_effort_is_forwarded_without_config_override() -> None:
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict[str, object]] = []

        def handler(request: httpx.Request) -> httpx.Response:
            if str(request.url) == "https://upstream.test/v1/responses":
                return httpx.Response(404, json={"error": {"message": "missing"}})
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(200, json={"id": "ok"})

        config = make_config(
            Path(directory), (KeyConfig("key-1", "sk-1", "https://upstream.test"),)
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/responses",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "input": "hi",
                    "reasoning": {"effort": "low"},
                },
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

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            reasoning_effort="none",
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "messages": [{"role": "user", "content": "hi"}],
                    "reasoning_effort": "high",
                    "reasoning": {"effort": "low"},
                },
            )

        response = run_client(app, requests)

        assert response.status_code == 200
        assert upstream_bodies[0]["reasoning_effort"] == "none"


def test_images_generations_proxies_to_upstream() -> None:
    """图像生成请求应正确代理到上游，model 字段被替换为解析后的模型 ID。"""
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict] = []
        upstream_urls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_urls.append(str(request.url))
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(
                200,
                json={
                    "created": 1700000000,
                    "data": [{"url": "https://example.com/image.png"}],
                },
            )

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/images/generations",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "test-model",
                    "prompt": "a white cat",
                    "n": 1,
                    "size": "1024x1024",
                },
            )

        response = run_client(app, request)

        assert response.status_code == 200
        data = response.json()
        assert data["created"] == 1700000000
        assert data["data"][0]["url"] == "https://example.com/image.png"
        assert upstream_urls[0] == "https://upstream.test/v1/images/generations"
        assert upstream_bodies[0]["model"] == "test-model"
        assert upstream_bodies[0]["prompt"] == "a white cat"
        assert upstream_bodies[0]["n"] == 1
        assert upstream_bodies[0]["size"] == "1024x1024"


def test_images_generations_unified_model_resolves_correctly() -> None:
    """unified-model 应正确解析为图像模型。"""
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(
                200,
                json={"created": 1, "data": [{"url": "https://example.com/img.png"}]},
            )

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            unified_model=UnifiedModelConfig(model="test-model"),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/images/generations",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "unified-model", "prompt": "a sunset"},
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert upstream_bodies[0]["model"] == "test-model"


def test_images_generations_failover_across_keys() -> None:
    """图像生成请求在第一个 key 失败时应自动切换到第二个 key。"""
    with tempfile.TemporaryDirectory() as directory:
        call_count = 0

        def handler(request: httpx.Request) -> httpx.Response:
            nonlocal call_count
            call_count += 1
            auth = request.headers.get("authorization", "")
            if auth == "Bearer sk-bad":
                return httpx.Response(403, json={"error": "forbidden"})
            return httpx.Response(
                200,
                json={"created": 1, "data": [{"url": "https://example.com/img.png"}]},
            )

        config = make_config(
            Path(directory),
            (
                KeyConfig("bad-key", "sk-bad", "https://upstream.test"),
                KeyConfig("good-key", "sk-good", "https://upstream.test"),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/images/generations",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "prompt": "a dog"},
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert call_count == 2


def test_images_generations_custom_upstream_route() -> None:
    """自定义 upstream_routes 中的 images 路径应被正确使用。"""
    with tempfile.TemporaryDirectory() as directory:
        upstream_urls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_urls.append(str(request.url))
            return httpx.Response(
                200,
                json={"created": 1, "data": [{"url": "https://example.com/img.png"}]},
            )

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            upstream_routes={"https://upstream.test": {"images": "custom/images/v2"}},
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/images/generations",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "prompt": "a mountain"},
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert (
            upstream_urls[0]
            == "https://upstream.test/custom/images/v2/v1/images/generations"
        )


def test_unified_model_routes_chat_and_image_to_different_models() -> None:
    """unified-model 应根据请求路径将 chat 和 image 请求路由到不同模型。"""
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(
                200,
                json={
                    "created": 1,
                    "data": [{"url": "https://example.com/img.png"}],
                },
            )

        config = replace(
            make_config(
                Path(directory),
                (
                    KeyConfig("chat-key", "sk-chat", "https://upstream.test"),
                    KeyConfig("img-key", "sk-img", "https://upstream.test"),
                ),
                unified_model=UnifiedModelConfig(
                    model="test-model",
                    image_model="alias-model",
                ),
            ),
            models=(
                ModelConfig(
                    id="test-model",
                    aliases=("chat-alias",),
                    keys=(KeyConfig("chat-key", "sk-chat", "https://upstream.test"),),
                ),
                ModelConfig(
                    id="alias-model",
                    aliases=("img-alias",),
                    keys=(KeyConfig("img-key", "sk-img", "https://upstream.test"),),
                ),
            ),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def requests(
            client: httpx.AsyncClient,
        ) -> tuple[httpx.Response, httpx.Response]:
            chat_resp = await client.post(
                "/v1/chat/completions",
                headers={"Authorization": "Bearer local-key"},
                json={
                    "model": "unified-model",
                    "messages": [{"role": "user", "content": "hi"}],
                },
            )
            img_resp = await client.post(
                "/v1/images/generations",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "unified-model", "prompt": "a cat"},
            )
            return chat_resp, img_resp

        chat_resp, img_resp = run_client(app, requests)

        assert chat_resp.status_code == 200
        assert img_resp.status_code == 200
        assert upstream_bodies[0]["model"] == "test-model"
        assert upstream_bodies[1]["model"] == "alias-model"


def test_unified_model_image_request_without_image_model_uses_default() -> None:
    """未配置 image_model 时，image 请求应使用默认 model。"""
    with tempfile.TemporaryDirectory() as directory:
        upstream_bodies: list[dict] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_bodies.append(json.loads(request.content.decode("utf-8")))
            return httpx.Response(
                200,
                json={"created": 1, "data": [{"url": "https://example.com/img.png"}]},
            )

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
            unified_model=UnifiedModelConfig(model="test-model"),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/images/generations",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "unified-model", "prompt": "a dog"},
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert upstream_bodies[0]["model"] == "test-model"


def test_images_edits_proxies_to_correct_upstream_path() -> None:
    """images/edits 请求应代理到 v1/images/edits 而非 v1/images/generations。"""
    with tempfile.TemporaryDirectory() as directory:
        upstream_urls: list[str] = []

        def handler(request: httpx.Request) -> httpx.Response:
            upstream_urls.append(str(request.url))
            return httpx.Response(
                200,
                json={
                    "created": 1,
                    "data": [{"url": "https://example.com/edited.png"}],
                },
            )

        config = make_config(
            Path(directory),
            (KeyConfig("key-1", "sk-1", "https://upstream.test"),),
        )
        app = create_app(config)
        app.state.runtime_manager.current.http_client = httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        )

        async def request(client: httpx.AsyncClient) -> httpx.Response:
            return await client.post(
                "/v1/images/edits",
                headers={"Authorization": "Bearer local-key"},
                json={"model": "test-model", "prompt": "add sunglasses"},
            )

        response = run_client(app, request)

        assert response.status_code == 200
        assert upstream_urls[0] == "https://upstream.test/v1/images/edits"
