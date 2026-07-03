from __future__ import annotations

import json
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import TypeVar

import anyio
import httpx
from fastapi import FastAPI

from auto_model_key_router.app import create_app
from auto_model_key_router.config import (
    VISITOR_API_KEY,
    KeyConfig,
    ModelConfig,
    RouterConfig,
)


T = TypeVar("T")
AUTH_HEADERS = {"Authorization": "Bearer local-key"}


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


def config_data(tmp_path: Path) -> dict[str, object]:
    return {
        "host": "127.0.0.1",
        "port": 8000,
        "request_timeout": 10,
        "max_retries": 1,
        "key_failure_threshold": 1,
        "key_cooldown_seconds": 60,
        "key_state_path": str(tmp_path / "key-state.json"),
        "upstream_health_check_interval": 0,
        "metrics_db_path": str(tmp_path / "metrics.sqlite3"),
        "log_file_path": str(tmp_path / "server.log"),
        "local_api_key": "local-key",
        "models": [
            {
                "id": "model-a",
                "aliases": ["alias-a"],
                "keys": [
                    {
                        "name": "key-a",
                        "api_key": "sk-secret-a",
                        "base_url": "https://a.example.test",
                    }
                ],
            }
        ],
    }


def create_file_backed_app(tmp_path: Path) -> tuple[FastAPI, Path]:
    path = tmp_path / "router-config.json"
    path.write_text(json.dumps(config_data(tmp_path)), encoding="utf-8")
    return create_app(RouterConfig.load(path), path), path


def test_management_reads_require_local_key_and_redact_upstream_secrets(
    tmp_path: Path, monkeypatch
) -> None:
    monkeypatch.setattr("auto_model_key_router.visitor.VISITOR_FEATURE_AVAILABLE", True)
    app, _ = create_file_backed_app(tmp_path)

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, httpx.Response, httpx.Response]:
        unauthorized = await client.get("/api/models")
        visitor = await client.get(
            "/api/models",
            headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
        )
        authorized = await client.get("/api/models", headers=AUTH_HEADERS)
        return unauthorized, visitor, authorized

    unauthorized, visitor, authorized = run_client(app, requests)

    assert unauthorized.status_code == 401
    assert visitor.status_code == 401
    assert authorized.status_code == 200
    model = authorized.json()["models"][0]
    assert model["id"] == "model-a"
    assert model["visitor_available"] is False
    assert model["keys"][0] == {
        "name": "key-a",
        "base_url": "https://a.example.test",
        "enabled": True,
        "allow_visitor": False,
        "api_key_fingerprint": "65bbff9a6cb9",
    }
    assert "api_key" not in model["keys"][0]
    assert "sk-secret-a" not in authorized.text


def test_model_and_key_crud_persist_and_hot_reload_visitor_access(
    tmp_path: Path, monkeypatch
) -> None:
    monkeypatch.setattr("auto_model_key_router.visitor.VISITOR_FEATURE_AVAILABLE", True)
    app, path = create_file_backed_app(tmp_path)

    async def requests(client: httpx.AsyncClient) -> list[httpx.Response]:
        responses = [
            await client.post(
                "/api/models",
                headers=AUTH_HEADERS,
                json={
                    "id": "model-b",
                    "aliases": ["alias-b"],
                    "routing_mode": "priority",
                    "reasoning_effort": "high",
                    "keys": [
                        {
                            "name": "main",
                            "api_key": "sk-secret-b",
                            "base_url": "https://b.example.test",
                            "allow_visitor": True,
                        }
                    ],
                },
            )
        ]
        responses.append(
            await client.get(
                "/v1/models",
                headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
            )
        )
        responses.append(
            await client.put(
                "/api/models/model-b",
                headers=AUTH_HEADERS,
                json={
                    "id": "model-c",
                    "aliases": ["alias-c"],
                    "routing_mode": "only_first",
                    "reasoning_effort": None,
                },
            )
        )
        responses.append(
            await client.post(
                "/api/models/model-c/keys",
                headers=AUTH_HEADERS,
                json={
                    "name": "backup",
                    "api_key": "sk-backup",
                    "base_url": "https://backup.example.test",
                },
            )
        )
        responses.append(
            await client.put(
                "/api/models/model-c/keys/main",
                headers=AUTH_HEADERS,
                json={"name": "primary", "allow_visitor": False},
            )
        )
        responses.append(
            await client.get("/api/models/model-c/keys/primary", headers=AUTH_HEADERS)
        )
        responses.append(
            await client.delete("/api/models/model-c/keys/backup", headers=AUTH_HEADERS)
        )
        responses.append(
            await client.delete("/api/models/model-c", headers=AUTH_HEADERS)
        )
        responses.append(await client.get("/api/models/model-c", headers=AUTH_HEADERS))
        return responses

    (
        created,
        visitor_models,
        updated_model,
        created_key,
        updated_key,
        read_key,
        deleted_key,
        deleted_model,
        missing_model,
    ) = run_client(app, requests)

    assert created.status_code == 201
    assert created.json()["visitor_available"] is True
    assert "api_key" not in created.json()["keys"][0]
    assert [item["id"] for item in visitor_models.json()["data"]] == ["amkr-model-b"]
    assert updated_model.status_code == 200
    assert updated_model.json()["id"] == "model-c"
    assert updated_model.json()["aliases"] == ["alias-c"]
    assert updated_model.json()["routing_mode"] == "only_first"
    assert updated_model.json()["reasoning_effort"] is None
    assert created_key.status_code == 201
    assert updated_key.status_code == 200
    assert read_key.json()["name"] == "primary"
    assert read_key.json()["allow_visitor"] is False
    assert deleted_key.status_code == 204
    assert deleted_model.status_code == 204
    assert missing_model.status_code == 404

    saved = json.loads(path.read_text(encoding="utf-8"))
    assert [model["id"] for model in saved["models"]] == ["model-a"]
    assert "sk-secret-b" not in created.text
    assert "sk-secret-b" not in updated_key.text


def test_key_update_preserves_secret_and_last_key_cannot_be_deleted(
    tmp_path: Path,
) -> None:
    app, path = create_file_backed_app(tmp_path)

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, httpx.Response]:
        updated = await client.put(
            "/api/models/model-a/keys/key-a",
            headers=AUTH_HEADERS,
            json={"base_url": "https://new.example.test", "allow_visitor": True},
        )
        deleted = await client.delete(
            "/api/models/model-a/keys/key-a", headers=AUTH_HEADERS
        )
        return updated, deleted

    updated, deleted = run_client(app, requests)

    assert updated.status_code == 200
    assert updated.json()["allow_visitor"] is True
    assert deleted.status_code == 409
    saved_key = json.loads(path.read_text(encoding="utf-8"))["models"][0]["keys"][0]
    assert saved_key["api_key"] == "sk-secret-a"
    assert saved_key["base_url"] == "https://new.example.test"
    assert saved_key["allow_visitor"] is True


def test_key_update_persists_and_clears_base_url_upstream_routes(tmp_path: Path) -> None:
    app, path = create_file_backed_app(tmp_path)

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, dict[str, object], httpx.Response]:
        updated = await client.put(
            "/api/models/model-a/keys/key-a",
            headers=AUTH_HEADERS,
            json={"upstream_routes": {"anthropic": "anthropic/"}},
        )
        after_update = json.loads(path.read_text(encoding="utf-8"))
        cleared = await client.put(
            "/api/models/model-a/keys/key-a",
            headers=AUTH_HEADERS,
            json={"upstream_routes": None},
        )
        return updated, after_update, cleared

    updated, after_update, cleared = run_client(app, requests)

    assert updated.status_code == 200
    assert "upstream_routes" not in updated.json()
    assert after_update["upstream_routes"] == {
        "https://a.example.test": {"anthropic": "anthropic/v1/messages"}
    }
    assert "upstream_routes" not in after_update["models"][0]["keys"][0]
    assert cleared.status_code == 200
    assert "upstream_routes" not in cleared.json()
    saved_data = json.loads(path.read_text(encoding="utf-8"))
    saved_key = saved_data["models"][0]["keys"][0]
    assert "upstream_routes" not in saved_key
    assert "upstream_routes" not in saved_data


def test_management_writes_require_a_config_file_path(tmp_path: Path) -> None:
    config = RouterConfig(
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
        local_api_key="local-key",
        models=(
            ModelConfig(
                id="model-a",
                keys=(KeyConfig("key-a", "sk-secret-a", "https://a.example.test"),),
            ),
        ),
    )

    response = run_client(
        create_app(config),
        lambda client: client.post(
            "/api/models/model-a/keys",
            headers=AUTH_HEADERS,
            json={
                "name": "key-b",
                "api_key": "sk-secret-b",
                "base_url": "https://b.example.test",
            },
        ),
    )

    assert response.status_code == 409
    assert response.json()["detail"] == "未设置配置文件路径，无法持久化修改"


def test_management_can_update_an_implicitly_named_key(tmp_path: Path) -> None:
    data = config_data(tmp_path)
    models = data["models"]
    assert isinstance(models, list)
    keys = models[0]["keys"]
    assert isinstance(keys, list)
    del keys[0]["name"]
    path = tmp_path / "router-config.json"
    path.write_text(json.dumps(data), encoding="utf-8")
    app = create_app(RouterConfig.load(path), path)

    response = run_client(
        app,
        lambda client: client.put(
            "/api/models/model-a/keys/model-a-1",
            headers=AUTH_HEADERS,
            json={"allow_visitor": True},
        ),
    )

    assert response.status_code == 200
    assert response.json()["name"] == "model-a-1"
    assert response.json()["allow_visitor"] is True
    saved_key = json.loads(path.read_text(encoding="utf-8"))["models"][0]["keys"][0]
    assert "name" not in saved_key
    assert saved_key["allow_visitor"] is True


def test_management_rejects_null_for_non_nullable_update_fields(
    tmp_path: Path,
) -> None:
    app, path = create_file_backed_app(tmp_path)
    original = path.read_text(encoding="utf-8")

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, httpx.Response]:
        null_key = await client.put(
            "/api/models/model-a/keys/key-a",
            headers=AUTH_HEADERS,
            json={"api_key": None},
        )
        blank_name = await client.put(
            "/api/models/model-a/keys/key-a",
            headers=AUTH_HEADERS,
            json={"name": "   "},
        )
        return null_key, blank_name

    null_key, blank_name = run_client(app, requests)

    assert null_key.status_code == 400
    assert null_key.json()["detail"] == "字段不能为 null: api_key"
    assert blank_name.status_code == 400
    assert blank_name.json()["detail"] == "key 名称不能为空"
    assert path.read_text(encoding="utf-8") == original


def test_get_key_stats_returns_key_specific_metrics(
    tmp_path: Path,
) -> None:
    app, path = create_file_backed_app(tmp_path)

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, httpx.Response, httpx.Response, httpx.Response]:
        # Record some metrics directly via the store
        state = app.state
        await state.runtime_manager.current.metrics.record(
            "model-a", "key-a", 200,
            {"prompt_tokens": 10, "completion_tokens": 5},
            duration_ms=30, first_token_ms=10,
        )
        await state.runtime_manager.current.metrics.record(
            "model-a", "key-a", 200,
            {"prompt_tokens": 20, "completion_tokens": 8},
            duration_ms=40, first_token_ms=15,
        )
        ok = await client.get(
            "/api/models/model-a/keys/key-a/stats", headers=AUTH_HEADERS
        )
        missing_key = await client.get(
            "/api/models/model-a/keys/nonexistent/stats", headers=AUTH_HEADERS
        )
        unauthorized = await client.get(
            "/api/models/model-a/keys/key-a/stats"
        )
        with_hours = await client.get(
            "/api/models/model-a/keys/key-a/stats?hours=24",
            headers=AUTH_HEADERS,
        )
        return ok, missing_key, unauthorized, with_hours

    ok, missing_key, unauthorized, with_hours = run_client(app, requests)

    assert ok.status_code == 200
    body = ok.json()
    assert body["model_id"] == "model-a"
    assert body["key_name"] == "key-a"
    assert body["stats"]["requests"] == 2
    assert body["stats"]["successes"] == 2
    assert body["stats"]["prompt_tokens"] == 30
    assert body["stats"]["completion_tokens"] == 13
    assert "recent_requests" in body
    assert len(body["recent_requests"]) == 2
    assert missing_key.status_code == 404
    assert unauthorized.status_code == 401
    assert with_hours.status_code == 200


def test_key_state_endpoint_controls_runtime_usage(tmp_path: Path) -> None:
    app, _ = create_file_backed_app(tmp_path)

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, httpx.Response, httpx.Response, httpx.Response]:
        await app.state.runtime_manager.current.key_pool.mark_failure(
            "model-a", "key-a", status_code=503
        )
        cooling = await client.get(
            "/api/models/model-a/keys/key-a/state", headers=AUTH_HEADERS
        )
        paused = await client.put(
            "/api/models/model-a/keys/key-a/state",
            headers=AUTH_HEADERS,
            json={"disabled": True},
        )
        restored = await client.put(
            "/api/models/model-a/keys/key-a/state",
            headers=AUTH_HEADERS,
            json={"clear_cooldown": True},
        )
        unauthorized = await client.get("/api/models/model-a/keys/key-a/state")
        return cooling, paused, restored, unauthorized

    cooling, paused, restored, unauthorized = run_client(app, requests)

    assert cooling.status_code == 200
    assert cooling.json()["cooldown_remaining_seconds"] > 0
    assert cooling.json()["last_status_code"] == 503
    assert paused.status_code == 200
    assert paused.json()["disabled"] is True
    assert restored.status_code == 200
    assert restored.json() == {
        "failures": 0,
        "cooldown_remaining_seconds": 0,
        "last_status_code": None,
        "disabled": False,
    }
    assert unauthorized.status_code == 401


def test_unified_model_crud_via_api(tmp_path: Path) -> None:
    app, path = create_file_backed_app(tmp_path)

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, httpx.Response, httpx.Response, httpx.Response, httpx.Response, httpx.Response, httpx.Response]:
        # Initially no unified model
        get_empty = await client.get("/api/unified-model", headers=AUTH_HEADERS)
        # Set unified model (auto routing)
        set_ok = await client.put(
            "/api/unified-model",
            headers=AUTH_HEADERS,
            json={"model": "model-a"},
        )
        # Read back
        get_after_set = await client.get("/api/unified-model", headers=AUTH_HEADERS)
        # Update with key
        update_with_key = await client.put(
            "/api/unified-model",
            headers=AUTH_HEADERS,
            json={"model": "model-a", "key": "key-a"},
        )
        # Change model without key → should clear key (different model)
        change_model = await client.put(
            "/api/unified-model",
            headers=AUTH_HEADERS,
            json={"model": "model-a"},
        )
        # Set key then explicitly null → auto routing
        set_key_again = await client.put(
            "/api/unified-model",
            headers=AUTH_HEADERS,
            json={"model": "model-a", "key": "key-a"},
        )
        clear_key = await client.put(
            "/api/unified-model",
            headers=AUTH_HEADERS,
            json={"model": "model-a", "key": None},
        )
        return get_empty, set_ok, get_after_set, update_with_key, change_model, set_key_again, clear_key

    get_empty, set_ok, get_after_set, update_with_key, change_model, set_key_again, clear_key = run_client(
        app, requests
    )

    assert get_empty.status_code == 200
    assert get_empty.json()["unified_model"] is None
    assert set_ok.status_code == 200
    assert set_ok.json()["unified_model"]["model"] == "model-a"
    assert set_ok.json()["unified_model"]["key"] is None
    assert get_after_set.status_code == 200
    assert get_after_set.json()["unified_model"]["model"] == "model-a"
    assert update_with_key.status_code == 200
    assert update_with_key.json()["unified_model"]["key"] == "key-a"
    # Same model without key → keeps existing key
    assert change_model.status_code == 200
    assert change_model.json()["unified_model"]["key"] == "key-a"
    # Explicit key then null → auto routing
    assert set_key_again.status_code == 200
    assert set_key_again.json()["unified_model"]["key"] == "key-a"
    assert clear_key.status_code == 200
    assert clear_key.json()["unified_model"]["key"] is None


def test_unified_model_rejects_unknown_model(tmp_path: Path) -> None:
    app, _ = create_file_backed_app(tmp_path)

    response = run_client(
        app,
        lambda client: client.put(
            "/api/unified-model",
            headers=AUTH_HEADERS,
            json={"model": "nonexistent"},
        ),
    )

    assert response.status_code == 404
    assert "未配置模型" in response.json()["detail"]
