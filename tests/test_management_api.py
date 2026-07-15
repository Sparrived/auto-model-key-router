from __future__ import annotations

import hashlib
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
        "endpoint_capabilities_path": str(tmp_path / "endpoint-capabilities.json"),
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


def test_settings_crud_is_revision_protected_and_redacts_local_key(
    tmp_path: Path,
) -> None:
    app, path = create_file_backed_app(tmp_path)

    async def requests(client: httpx.AsyncClient):
        current = await client.get("/api/settings", headers=AUTH_HEADERS)
        stale = await client.put(
            "/api/settings",
            headers=AUTH_HEADERS,
            json={"config_revision": "stale", "port": 19000},
        )
        updated = await client.put(
            "/api/settings",
            headers=AUTH_HEADERS,
            json={
                "config_revision": current.json()["config_revision"],
                "host": "127.0.0.2",
                "port": 19000,
                "request_timeout": 30,
                "stream_first_byte_timeout": 45,
                "stream_idle_timeout": 90,
                "max_retries": 3,
            },
        )
        return current, stale, updated

    current, stale, updated = run_client(app, requests)

    assert current.status_code == 200
    assert current.json()["settings"]["local_api_key_fingerprint"] == hashlib.sha256(
        b"local-key"
    ).hexdigest()[:12]
    assert "local-key" not in current.text
    assert stale.status_code == 409
    assert updated.status_code == 200
    assert updated.json()["settings"] == {
        "host": "127.0.0.2",
        "port": 19000,
        "request_timeout": 30.0,
        "stream_first_byte_timeout": 45.0,
        "stream_idle_timeout": 90.0,
        "max_retries": 3,
        "local_auth_enabled": True,
        "local_api_key_fingerprint": hashlib.sha256(b"local-key").hexdigest()[:12],
    }
    saved = json.loads(path.read_text(encoding="utf-8"))
    assert saved["host"] == "127.0.0.2"
    assert saved["port"] == 19000
    assert saved["local_api_key"] == "local-key"
    assert saved["providers"]["a.example.test"]["keys"]["key-a"]["api_key"] == "sk-secret-a"


def test_settings_reject_invalid_host_and_regenerate_local_key_once(
    tmp_path: Path, monkeypatch
) -> None:
    app, path = create_file_backed_app(tmp_path)
    monkeypatch.setattr(
        "auto_model_key_router.management_api.generate_local_api_key",
        lambda: "replacement-local-key",
    )

    async def requests(client: httpx.AsyncClient):
        current = await client.get("/api/settings", headers=AUTH_HEADERS)
        invalid = await client.put(
            "/api/settings",
            headers=AUTH_HEADERS,
            json={
                "config_revision": current.json()["config_revision"],
                "host": "http://127.0.0.1",
            },
        )
        regenerated = await client.post(
            "/api/settings/local-api-key",
            headers=AUTH_HEADERS,
            json={"config_revision": current.json()["config_revision"]},
        )
        return invalid, regenerated

    invalid, regenerated = run_client(app, requests)

    assert invalid.status_code == 422
    assert regenerated.status_code == 200
    assert regenerated.json()["local_api_key"] == "replacement-local-key"
    assert regenerated.json()["local_api_key_fingerprint"] == hashlib.sha256(
        b"replacement-local-key"
    ).hexdigest()[:12]
    assert json.loads(path.read_text(encoding="utf-8"))["local_api_key"] == (
        "replacement-local-key"
    )


def test_update_check_reuses_cli_version_source_and_requires_local_auth(
    tmp_path: Path, monkeypatch
) -> None:
    from auto_model_key_router.update import VersionCheckResult

    app, _ = create_file_backed_app(tmp_path)
    monkeypatch.setattr(
        "auto_model_key_router.update.check_latest_version",
        lambda timeout: VersionCheckResult(
            current_version="3.1.0",
            latest_version="3.2.0",
            release_url="https://example.test/amkr/3.2.0",
            source="PyPI",
            artifact_url="https://files.pythonhosted.org/packages/amkr.whl",
            artifact_sha256="a" * 64,
        ),
    )

    async def requests(client: httpx.AsyncClient):
        denied = await client.post("/api/update/check")
        checked = await client.post("/api/update/check", headers=AUTH_HEADERS)
        return denied, checked

    denied, checked = run_client(app, requests)

    assert denied.status_code == 401
    assert checked.status_code == 200
    assert checked.json() == {
        "current_version": "3.1.0",
        "latest_version": "3.2.0",
        "release_url": "https://example.test/amkr/3.2.0",
        "source": "PyPI",
        "artifact_url": "https://files.pythonhosted.org/packages/amkr.whl",
        "artifact_sha256": "a" * 64,
        "update_available": True,
        "error": None,
    }


def test_list_providers_redacts_keys_and_returns_migrated_config_revision(
    tmp_path: Path,
) -> None:
    app, path = create_file_backed_app(tmp_path)
    expected_data = json.loads(path.read_text(encoding="utf-8"))

    response = run_client(
        app,
        lambda client: client.get("/api/providers", headers=AUTH_HEADERS),
    )

    assert response.status_code == 200
    body = response.json()
    assert body["config_revision"]
    assert body["providers"] == [
        {
            "id": "a.example.test",
            "base_url": "https://a.example.test",
            "keys": [
                {
                    "name": "key-a",
                    "enabled": True,
                    "allow_visitor": False,
                    "api_key_fingerprint": "65bbff9a6cb9",
                }
            ],
            "pools": [{"name": "model-a", "keys": ["key-a"], "models": ["model-a"]}],
            "routes": {},
        }
    ]
    assert '"api_key":' not in response.text
    assert "sk-secret-a" not in response.text
    assert body["config_revision"] == hashlib.sha256(
        json.dumps(
            expected_data,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
    ).hexdigest()


def test_provider_read_uses_one_raw_config_snapshot(tmp_path: Path) -> None:
    app, path = create_file_backed_app(tmp_path)
    data = json.loads(path.read_text(encoding="utf-8"))
    data["providers"]["b.example.test"] = {
        "base_url": "https://b.example.test",
        "keys": {"key-b": {"api_key": "sk-secret-b"}},
        "pools": {"model-b": {"keys": ["key-b"], "models": ["model-b"]}},
    }
    data["models"]["model-b"] = {
        "targets": [
            {"provider": "b.example.test", "pool": "model-b", "upstream_model": "model-b"}
        ]
    }
    path.write_text(json.dumps(data), encoding="utf-8")
    app.state.config_mtime = path.stat().st_mtime

    response = run_client(
        app,
        lambda client: client.get("/api/providers", headers=AUTH_HEADERS),
    )

    assert response.status_code == 200
    body = response.json()
    assert [provider["id"] for provider in body["providers"]] == [
        "a.example.test",
        "b.example.test",
    ]
    assert body["config_revision"] == hashlib.sha256(
        json.dumps(data, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    ).hexdigest()


def test_provider_routes_are_normalized_updated_and_cleared(tmp_path: Path) -> None:
    app, _ = create_file_backed_app(tmp_path)

    async def requests(client: httpx.AsyncClient):
        current = await client.get("/api/providers", headers=AUTH_HEADERS)
        updated = await client.put(
            "/api/providers/a.example.test",
            headers=AUTH_HEADERS,
            json={
                "config_revision": current.json()["config_revision"],
                "routes": {
                    "openai": "proxy/v1",
                    "anthropic": "gateway",
                    "responses": "v1/responses",
                    "images": "images/v1",
                },
            },
        )
        invalid = await client.put(
            "/api/providers/a.example.test",
            headers=AUTH_HEADERS,
            json={
                "config_revision": updated.json()["config_revision"],
                "routes": {"openai": "https://bad.example/v1"},
            },
        )
        cleared = await client.put(
            "/api/providers/a.example.test",
            headers=AUTH_HEADERS,
            json={
                "config_revision": updated.json()["config_revision"],
                "routes": {},
            },
        )
        return updated, invalid, cleared

    updated, invalid, cleared = run_client(app, requests)

    assert updated.status_code == 200
    assert updated.json()["provider"]["routes"] == {
        "openai": "proxy/v1/chat/completions",
        "anthropic": "gateway/v1/messages",
        "responses": "v1/responses",
        "images": "images/v1/images/generations",
    }
    assert invalid.status_code == 422
    assert cleared.status_code == 200
    assert cleared.json()["provider"]["routes"] == {}


def test_model_write_rejects_stale_revision_before_persisting(tmp_path: Path) -> None:
    app, path = create_file_backed_app(tmp_path)
    original = path.read_text(encoding="utf-8")

    response = run_client(
        app,
        lambda client: client.post(
            "/api/models",
            headers=AUTH_HEADERS,
            json={
                "config_revision": "stale",
                "id": "model-b",
                "keys": [{"name": "key-b", "api_key": "sk-secret-b"}],
            },
        ),
    )

    assert response.status_code == 409
    assert path.read_text(encoding="utf-8") == original


def test_provider_key_pool_and_route_crud_uses_v3_config(tmp_path: Path) -> None:
    app, path = create_file_backed_app(tmp_path)

    responses = run_client(app, lambda client: _provider_crud_requests(client))

    created_provider, duplicate, created_key, created_pool, created_route, disabled, created_backup, deleted_key, deleted_route, deleted_pool, deleted_provider = responses
    assert created_provider.status_code == 201
    assert duplicate.status_code == 409
    assert created_key.status_code == 201
    assert created_key.json()["api_key_fingerprint"] == hashlib.sha256(
        b"sk-secret-b"
    ).hexdigest()[:12]
    assert "sk-secret-b" not in created_key.text
    assert created_pool.status_code == 201
    assert created_route.status_code == 201
    assert disabled.status_code == 409
    assert created_backup.status_code == 201
    assert deleted_key.status_code == 204
    assert deleted_pool.status_code == 204
    assert deleted_route.status_code == 204
    assert deleted_provider.status_code == 204

    saved = json.loads(path.read_text(encoding="utf-8"))
    assert saved["config_version"] == 3
    assert "b.example.test" not in saved["providers"]
    assert "model-b" not in saved["models"]


def test_key_probe_is_async_redacted_and_requires_full_access(
    tmp_path: Path, monkeypatch
) -> None:
    app, _ = create_file_backed_app(tmp_path)

    def fake_capabilities(provider, names, timeout):
        return {"key_models": {name: ["model-a"] for name in names}}

    class Result:
        available = False
        url = "https://a.example.test/v1/chat/completions"
        duration_ms = 12
        error = "Bearer sk-secret-a failed"

    monkeypatch.setattr(
        "auto_model_key_router.config_editor.probe_provider_key_capabilities",
        fake_capabilities,
    )
    monkeypatch.setattr(
        "auto_model_key_router.config_editor.probe_key_availability",
        lambda *args, **kwargs: [Result()],
    )

    async def requests(client: httpx.AsyncClient):
        denied = await client.post(
            "/api/probes/keys",
            headers={"Authorization": f"Bearer {VISITOR_API_KEY}"},
            json={"provider_id": "a.example.test"},
        )
        started = await client.post(
            "/api/probes/keys",
            headers=AUTH_HEADERS,
            json={"provider_id": "a.example.test", "keys": ["key-a"]},
        )
        completed = None
        for _ in range(20):
            await anyio.sleep(0.01)
            completed = await client.get(
                f"/api/probes/{started.json()['probe_id']}", headers=AUTH_HEADERS
            )
            if completed.json()["status"] == "complete":
                break
        assert completed is not None
        return denied, started, completed

    denied, started, completed = run_client(app, requests)
    assert denied.status_code == 401
    assert started.status_code == 202
    assert completed.status_code == 200
    result = completed.json()["results"][0]
    assert result["provider"] == "a.example.test"
    assert result["key"] == "key-a"
    assert result["latency_ms"] == 12
    assert "sk-secret-a" not in completed.text


def test_config_transfer_excludes_machine_settings_and_keeps_them_on_import(
    tmp_path: Path,
) -> None:
    app, path = create_file_backed_app(tmp_path)

    async def requests(client: httpx.AsyncClient):
        exported = await client.post("/api/config/export", headers=AUTH_HEADERS)
        exported_config = exported.json()["config"]
        exported_config["providers"] = {}
        exported_config["models"] = {}
        imported = await client.post(
            "/api/config/import",
            headers=AUTH_HEADERS,
            json={
                "config_revision": exported.json()["config_revision"],
                "config": exported_config | {"host": "0.0.0.0", "local_api_key": "bad"},
            },
        )
        return exported, imported

    exported, imported = run_client(app, requests)
    assert exported.status_code == 200
    assert set(exported.json()["config"]).issubset(
        {"config_version", "providers", "models", "upstream_routes", "routing_mode", "unified_model"}
    )
    assert imported.status_code == 200
    saved = json.loads(path.read_text(encoding="utf-8"))
    assert saved["host"] == "127.0.0.1"
    assert saved["local_api_key"] == "local-key"
    assert saved["providers"] == {}
    assert list(tmp_path.glob("router-config.json.*.bak"))


async def _provider_crud_requests(client: httpx.AsyncClient) -> list[httpx.Response]:
    async def write(method: str, url: str, payload: dict[str, object]) -> httpx.Response:
        revision = (await client.get("/api/providers", headers=AUTH_HEADERS)).json()[
            "config_revision"
        ]
        return await client.request(
            method.upper(),
            url,
            headers=AUTH_HEADERS,
            json={"config_revision": revision, **payload},
        )

    responses = [
        await write(
            "post",
            "/api/providers",
            {"id": "b.example.test", "base_url": "https://b.example.test"},
        ),
        await write(
            "post",
            "/api/providers",
            {"id": "b.example.test", "base_url": "https://b.example.test"},
        ),
        await write(
            "post",
            "/api/providers/b.example.test/keys",
            {"name": "key-b", "api_key": "sk-secret-b"},
        ),
        await write(
            "post",
            "/api/providers/b.example.test/pools",
            {"name": "pool-b", "keys": ["key-b"], "models": ["upstream-b"]},
        ),
        await write(
            "post",
            "/api/routes",
            {
                "id": "model-b",
                "targets": [
                    {
                        "provider": "b.example.test",
                        "pool": "pool-b",
                        "upstream_model": "upstream-b",
                    }
                ],
            },
        ),
        await write(
            "put",
            "/api/providers/b.example.test/keys/key-b",
            {"enabled": False},
        ),
        await write(
            "post",
            "/api/providers/b.example.test/keys",
            {"name": "key-c", "api_key": "sk-secret-c"},
        ),
        await write(
            "delete",
            "/api/providers/b.example.test/keys/key-b",
            {},
        ),
        await write("delete", "/api/routes/model-b", {}),
        await write(
            "delete",
            "/api/providers/b.example.test/pools/pool-b",
            {},
        ),
        await write("delete", "/api/providers/b.example.test", {}),
    ]
    return responses


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
    assert list(saved["models"]) == ["model-a"]
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
    saved = json.loads(path.read_text(encoding="utf-8"))
    saved_key = saved["providers"]["new.example.test"]["keys"]["key-a"]
    assert saved_key["api_key"] == "sk-secret-a"
    assert saved_key["allow_visitor"] is True
    assert saved["providers"]["new.example.test"]["base_url"] == "https://new.example.test"


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
    assert "upstream_routes" not in after_update["models"]["model-a"]
    assert cleared.status_code == 200
    assert "upstream_routes" not in cleared.json()
    saved_data = json.loads(path.read_text(encoding="utf-8"))
    saved_key = saved_data["providers"]["a.example.test"]["keys"]["key-a"]
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
        endpoint_capabilities_path=str(tmp_path / "endpoint-capabilities.json"),
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
    saved_key = json.loads(path.read_text(encoding="utf-8"))["providers"]["a.example.test"]["keys"]["model-a-1"]
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


def test_key_state_endpoint_is_not_exposed(tmp_path: Path) -> None:
    app, _ = create_file_backed_app(tmp_path)

    async def requests(
        client: httpx.AsyncClient,
    ) -> tuple[httpx.Response, httpx.Response]:
        get_response = await client.get(
            "/api/models/model-a/keys/key-a/state", headers=AUTH_HEADERS
        )
        put_response = await client.put(
            "/api/models/model-a/keys/key-a/state",
            headers=AUTH_HEADERS,
            json={"disabled": True},
        )
        return get_response, put_response

    get_response, put_response = run_client(app, requests)

    assert get_response.status_code == 404
    assert put_response.status_code == 404


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
    assert set_ok.json()["unified_model"]["default"]["primary"] == {"model": "model-a", "key": None}
    assert get_after_set.status_code == 200
    assert get_after_set.json()["unified_model"]["default"]["primary"]["model"] == "model-a"
    assert update_with_key.status_code == 200
    assert update_with_key.json()["unified_model"]["default"]["primary"]["key"] == "key-a"
    # Same model without key → keeps existing key
    assert change_model.status_code == 200
    assert change_model.json()["unified_model"]["default"]["primary"]["key"] == "key-a"
    # Explicit key then null → auto routing
    assert set_key_again.status_code == 200
    assert set_key_again.json()["unified_model"]["default"]["primary"]["key"] == "key-a"
    assert clear_key.status_code == 200
    assert clear_key.json()["unified_model"]["default"]["primary"]["key"] is None


def test_unified_model_accepts_nested_api_payload(tmp_path: Path) -> None:
    app, _ = create_file_backed_app(tmp_path)

    response = run_client(
        app,
        lambda client: client.put(
            "/api/unified-model",
            headers=AUTH_HEADERS,
            json={
                "default": {
                    "primary": {"model": "model-a"},
                }
            },
        ),
    )

    assert response.status_code == 200
    assert response.json()["unified_model"]["default"]["primary"]["model"] == "model-a"


def test_unified_model_update_preserves_shared_v3_provider_pool(tmp_path: Path) -> None:
    path = tmp_path / "router-config.json"
    data = config_data(tmp_path)
    data["config_version"] = 3
    data["providers"] = {
        "otokapi.com": {
            "base_url": "https://otokapi.com",
            "keys": {"Discount": {"api_key": "sk-discount"}},
            "pools": {
                "Discount": {
                    "keys": ["Discount"],
                    "models": ["gpt-5.5", "gpt-5.4-mini"],
                }
            },
        }
    }
    data["models"] = {
        "gpt-5.5": {
            "targets": [
                {
                    "provider": "otokapi.com",
                    "pool": "Discount",
                    "upstream_model": "gpt-5.5",
                }
            ]
        },
        "gpt-5.4-mini": {
            "targets": [
                {
                    "provider": "otokapi.com",
                    "pool": "Discount",
                    "upstream_model": "gpt-5.4-mini",
                }
            ]
        },
    }
    data["unified_model"] = {"default": {"primary": {"model": "gpt-5.5"}}}
    path.write_text(json.dumps(data), encoding="utf-8")
    app = create_app(RouterConfig.load(path), path)

    response = run_client(
        app,
        lambda client: client.put(
            "/api/unified-model",
            headers=AUTH_HEADERS,
            json={"model": "gpt-5.4-mini"},
        ),
    )

    assert response.status_code == 200
    assert response.json()["unified_model"]["default"]["primary"] == {
        "model": "gpt-5.4-mini",
        "key": None,
    }
    saved = json.loads(path.read_text(encoding="utf-8"))
    assert saved["providers"]["otokapi.com"]["pools"]["Discount"] == {
        "keys": ["Discount"],
        "models": ["gpt-5.5", "gpt-5.4-mini"],
    }
    assert saved["models"]["gpt-5.5"]["targets"][0]["pool"] == "Discount"
    assert saved["models"]["gpt-5.4-mini"]["targets"][0]["pool"] == "Discount"


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
