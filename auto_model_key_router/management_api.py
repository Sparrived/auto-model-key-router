from __future__ import annotations

import asyncio
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
import re
import uuid
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from fastapi import FastAPI, HTTPException, Request, Response, status
from pydantic import BaseModel, Field

if hasattr(BaseModel, "model_fields"):
    from pydantic import ConfigDict

from .config import (
    KeyConfig,
    ModelConfig,
    RouterConfig,
    generate_local_api_key,
    load_config_data,
    migrate_config_data,
    normalize_upstream_base_url,
    normalize_upstream_routes,
    save_config_data,
)
from .config_service import ConfigService
from .proxy_support import _authorization_mode


ReloadConfig = Callable[[Any], Awaitable[None]]


if hasattr(BaseModel, "model_fields"):

    class APIModel(BaseModel):
        model_config = ConfigDict(extra="forbid")

else:

    class APIModel(BaseModel):
        class Config:
            extra = "forbid"


class KeyCreate(APIModel):
    name: str = Field(min_length=1)
    api_key: str = Field(min_length=1)
    base_url: str | None = None
    enabled: bool = True
    allow_visitor: bool = False
    upstream_routes: dict[str, str | None] | None = None


class KeyUpdate(APIModel):
    name: str | None = Field(default=None, min_length=1)
    api_key: str | None = Field(default=None, min_length=1)
    base_url: str | None = None
    enabled: bool | None = None
    allow_visitor: bool | None = None
    upstream_routes: dict[str, str | None] | None = None


class ModelCreate(APIModel):
    config_revision: str | None = Field(default=None, min_length=1)
    id: str = Field(min_length=1)
    aliases: list[str] = Field(default_factory=list)
    routing_mode: str = "round_robin"
    reasoning_effort: str | None = None
    keys: list[KeyCreate] = Field(default_factory=list)


class ModelUpdate(APIModel):
    id: str | None = Field(default=None, min_length=1)
    aliases: list[str] | None = None
    routing_mode: str | None = None
    reasoning_effort: str | None = None


class UnifiedModelUpdate(APIModel):
    model: str | None = Field(default=None, min_length=1)
    key: str | None = None
    image_model: str | None = None
    image_key: str | None = None
    default: dict[str, Any] | None = None
    image: dict[str, Any] | None = None


class ProbeKeysRequest(APIModel):
    provider_id: str = Field(min_length=1)
    keys: list[str] = Field(default_factory=list)
    timeout_seconds: float = Field(default=15, gt=0, le=120)


class ProbePoolsRequest(APIModel):
    provider_id: str = Field(min_length=1)
    pools: list[str] = Field(default_factory=list)
    timeout_seconds: float = Field(default=15, gt=0, le=120)


class ConfigImportRequest(APIModel):
    config_revision: str = Field(min_length=1)
    config: dict[str, Any]


class RevisionPayload(APIModel):
    config_revision: str = Field(min_length=1)


class ProviderCreate(RevisionPayload):
    id: str = Field(min_length=1)
    base_url: str = Field(min_length=1)


class ProviderUpdate(RevisionPayload):
    id: str | None = Field(default=None, min_length=1)
    base_url: str | None = Field(default=None, min_length=1)
    routes: dict[str, str | None] | None = None


class ProviderKeyCreate(RevisionPayload):
    name: str = Field(min_length=1)
    api_key: str = Field(min_length=1)
    enabled: bool = True
    allow_visitor: bool = False


class ProviderKeyUpdate(RevisionPayload):
    name: str | None = Field(default=None, min_length=1)
    api_key: str | None = Field(default=None, min_length=1)
    enabled: bool | None = None
    allow_visitor: bool | None = None


class PoolCreate(RevisionPayload):
    name: str = Field(min_length=1)
    keys: list[str] = Field(default_factory=list)
    models: list[str] = Field(default_factory=list)


class PoolUpdate(RevisionPayload):
    name: str | None = Field(default=None, min_length=1)
    keys: list[str] | None = None
    models: list[str] | None = None


class RouteCreate(RevisionPayload):
    id: str = Field(min_length=1)
    targets: list[dict[str, str]] = Field(min_length=1)
    aliases: list[str] = Field(default_factory=list)
    routing_mode: str | None = None


class RouteUpdate(RevisionPayload):
    id: str | None = Field(default=None, min_length=1)
    targets: list[dict[str, str]] | None = None
    aliases: list[str] | None = None
    routing_mode: str | None = None


class SettingsUpdate(RevisionPayload):
    host: str | None = Field(default=None, min_length=1)
    port: int | None = Field(default=None, ge=1, le=65535)
    request_timeout: float | None = Field(default=None, gt=0)
    stream_first_byte_timeout: float | None = Field(default=None, gt=0)
    stream_idle_timeout: float | None = Field(default=None, gt=0)
    max_retries: int | None = Field(default=None, ge=0)


class ManagementAPIError(Exception):
    def __init__(self, status_code: int, message: str) -> None:
        super().__init__(message)
        self.status_code = status_code
        self.message = message


def register_management_api(app: FastAPI, reload_config: ReloadConfig) -> None:
    def serialize_unified(config: RouterConfig) -> dict[str, Any] | None:
        unified = config.unified_model
        if unified is None:
            return None
        def serialize_plan(plan: Any) -> dict[str, Any]:
            result = {"primary": {"model": plan.primary.model, "key": plan.primary.key}}
            if plan.fallback:
                result["fallback"] = {"model": plan.fallback.model, "key": plan.fallback.key}
            return result
        result = {"default": serialize_plan(unified.default)}
        if unified.image:
            result["image"] = serialize_plan(unified.image)
        return result

    @app.get("/api/unified-model", tags=["management"])
    async def get_unified_model(request: Request) -> dict[str, Any]:
        config = await _authorized_config(request, reload_config)
        return {"unified_model": serialize_unified(config)}

    @app.put("/api/unified-model", tags=["management"])
    async def update_unified_model(
        request: Request, payload: UnifiedModelUpdate
    ) -> dict[str, Any]:
        fields = _payload_dict(payload)
        if fields.get("default") is not None:
            def nested_mutation(data: dict[str, Any]) -> None:
                unified_data: dict[str, Any] = {"default": fields["default"]}
                if fields.get("image") is not None:
                    unified_data["image"] = fields["image"]
                data["unified_model"] = unified_data

            config = await _update_config(request, reload_config, nested_mutation)
            return {"unified_model": serialize_unified(config)}
        if fields.get("model") is None:
            raise ManagementAPIError(422, "必须提供 model 或 default")
        target_model = str(fields["model"]).strip()
        key_provided = "key" in fields
        target_key = str(fields["key"]).strip() if fields.get("key") else None
        image_model_provided = "image_model" in fields
        target_image_model = str(fields["image_model"]).strip() if fields.get("image_model") else None
        image_key_provided = "image_key" in fields
        target_image_key = str(fields["image_key"]).strip() if fields.get("image_key") else None

        def mutation(data: dict[str, Any]) -> None:
            models = _raw_models(data)
            resolved_id = _resolve_model_id(models, target_model)
            if resolved_id is None:
                raise ManagementAPIError(404, f"未配置模型或别名: {target_model}")
            if target_key is not None:
                model = _find_raw_model(models, resolved_id)
                if model is None:
                    raise ManagementAPIError(404, f"模型不存在: {resolved_id}")
                keys = _raw_keys(model)
                if _find_raw_key(keys, resolved_id, target_key) is None:
                    raise ManagementAPIError(
                        404, f"模型 {resolved_id} 的 key 不存在: {target_key}"
                    )
            existing = data.get("unified_model")
            existing_primary = (
                existing.get("default", {}).get("primary", {})
                if isinstance(existing, dict)
                else {}
            )
            existing_key = (
                existing_primary.get("key")
                if isinstance(existing_primary, dict)
                else (existing.get("key") if isinstance(existing, dict) else None)
            )
            unified_data: dict[str, Any] = {"model": resolved_id}
            if key_provided:
                if target_key:
                    unified_data["key"] = target_key
            else:
                if existing_key:
                    unified_data["key"] = existing_key
            # 图像模型映射
            existing_image_model = None
            if isinstance(existing, dict):
                existing_image_model = existing.get("image_model") or existing.get("image", {}).get("primary", {}).get("model")
            if image_model_provided:
                if target_image_model:
                    resolved_image_id = _resolve_model_id(models, target_image_model)
                    if resolved_image_id is None:
                        raise ManagementAPIError(404, f"未配置模型或别名: {target_image_model}")
                    unified_data["image_model"] = resolved_image_id
                    if image_key_provided and target_image_key:
                        unified_data["image_key"] = target_image_key
                    elif not image_key_provided and isinstance(existing, dict) and existing.get("image_key") and existing.get("image_model") == resolved_image_id:
                        unified_data["image_key"] = existing["image_key"]
            elif existing_image_model:
                unified_data["image_model"] = existing_image_model
                if isinstance(existing, dict) and existing.get("image_key"):
                    unified_data["image_key"] = existing["image_key"]
            data["unified_model"] = unified_data

        config = await _update_config(request, reload_config, mutation)
        return {"unified_model": serialize_unified(config)}

    @app.delete(
        "/api/unified-model",
        tags=["management"],
        status_code=status.HTTP_204_NO_CONTENT,
        response_class=Response,
    )
    async def delete_unified_model(request: Request) -> Response:
        def mutation(data: dict[str, Any]) -> None:
            data.pop("unified_model", None)

        await _update_config(request, reload_config, mutation)
        return Response(status_code=status.HTTP_204_NO_CONTENT)

    @app.get("/api/models", tags=["management"])
    async def list_models(request: Request) -> dict[str, Any]:
        config = await _authorized_config(request, reload_config)
        return {"models": [_model_response(model) for model in config.models]}

    @app.get("/api/settings", tags=["management"])
    async def get_settings(request: Request) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        data = _management_config_data(request)
        return _with_revision(data, settings=_settings_response(data))

    @app.put("/api/settings", tags=["management"])
    async def update_settings(
        request: Request, payload: SettingsUpdate
    ) -> dict[str, Any]:
        updates = _payload_dict(payload)
        updates.pop("config_revision")
        if not updates:
            raise HTTPException(status_code=422, detail="至少提供一个设置字段")
        host = updates.get("host")
        if host is not None:
            updates["host"] = host.strip()
            if not updates["host"] or "://" in updates["host"] or "/" in updates["host"]:
                raise HTTPException(
                    status_code=422,
                    detail="监听地址只填写 IP 或主机名，不要包含协议或路径",
                )

        def mutation(data: dict[str, Any]) -> None:
            data.update(updates)

        await _update_config(
            request,
            reload_config,
            mutation,
            config_revision=payload.config_revision,
            v3=True,
        )
        data = _management_config_data(request)
        return _with_revision(data, settings=_settings_response(data))

    @app.post("/api/settings/local-api-key", tags=["management"])
    async def regenerate_local_api_key(
        request: Request, payload: RevisionPayload
    ) -> dict[str, Any]:
        local_api_key = generate_local_api_key()

        def mutation(data: dict[str, Any]) -> None:
            data["local_api_key"] = local_api_key

        await _update_config(
            request,
            reload_config,
            mutation,
            config_revision=payload.config_revision,
            v3=True,
        )
        data = _management_config_data(request)
        return _with_revision(
            data,
            local_api_key=local_api_key,
            local_api_key_fingerprint=hashlib.sha256(
                local_api_key.encode("utf-8")
            ).hexdigest()[:12],
        )

    @app.post("/api/update/check", tags=["management"])
    async def check_update(request: Request) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        from .update import check_latest_version

        result = await asyncio.to_thread(check_latest_version, timeout=10.0)
        return {
            "current_version": result.current_version,
            "latest_version": result.latest_version,
            "release_url": result.release_url,
            "source": result.source,
            "update_available": result.update_available,
            "error": result.error,
        }

    @app.get("/api/providers", tags=["management"])
    async def list_providers(request: Request) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        data = _management_config_data(request)
        return _with_revision(
            data,
            providers=[
                _provider_response(provider)
                for provider in RouterConfig.from_dict(data).providers
            ],
        )

    async def v3_update(request: Request, revision: str, mutation: Callable[[dict[str, Any]], None]) -> dict[str, Any]:
        await _update_config(request, reload_config, mutation, config_revision=revision, v3=True)
        return _management_config_data(request)

    @app.post("/api/providers", status_code=201, tags=["management"])
    async def create_provider(request: Request, payload: ProviderCreate) -> dict[str, Any]:
        def mutation(data: dict[str, Any]) -> None:
            providers = _providers(data)
            if payload.id in providers:
                raise ManagementAPIError(409, f"供应商已存在: {payload.id}")
            _valid_url(payload.base_url)
            providers[payload.id] = {"base_url": payload.base_url.rstrip("/"), "keys": {}, "pools": {}}
        data = await v3_update(request, payload.config_revision, mutation)
        return _with_revision(data, provider=_raw_provider_response(payload.id, _require_provider(_providers(data), payload.id)))

    @app.get("/api/providers/{provider_id}", tags=["management"])
    async def get_provider(request: Request, provider_id: str) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        data = _management_config_data(request)
        return _with_revision(data, provider=_raw_provider_response(provider_id, _require_provider(_providers(data), provider_id)))

    @app.put("/api/providers/{provider_id}", tags=["management"])
    async def update_provider(request: Request, provider_id: str, payload: ProviderUpdate) -> dict[str, Any]:
        updates = _payload_dict(payload); updates.pop("config_revision")
        if "routes" in updates:
            try:
                updates["routes"] = normalize_upstream_routes(updates["routes"])
            except ValueError as exc:
                raise HTTPException(status_code=422, detail=str(exc)) from exc
        def mutation(data: dict[str, Any]) -> None:
            providers = _providers(data); provider = _require_provider(providers, provider_id)
            new_id = updates.get("id", provider_id)
            if new_id != provider_id and new_id in providers: raise ManagementAPIError(409, f"供应商已存在: {new_id}")
            if "base_url" in updates: _valid_url(updates["base_url"]); provider["base_url"] = updates["base_url"].rstrip("/")
            if "routes" in updates:
                if updates["routes"]: provider["routes"] = updates["routes"]
                else: provider.pop("routes", None)
            if new_id != provider_id:
                providers[new_id] = providers.pop(provider_id)
                for route in _routes(data).values():
                    for target in route.get("targets", []):
                        if target.get("provider") == provider_id: target["provider"] = new_id
        data = await v3_update(request, payload.config_revision, mutation); new_id = updates.get("id", provider_id)
        return _with_revision(data, provider=_raw_provider_response(new_id, _require_provider(_providers(data), new_id)))

    @app.delete("/api/providers/{provider_id}", status_code=204, response_class=Response, tags=["management"])
    async def delete_provider(request: Request, provider_id: str, payload: RevisionPayload) -> Response:
        def mutation(data: dict[str, Any]) -> None: _providers(data).pop(provider_id) if _require_provider(_providers(data), provider_id) else None
        await v3_update(request, payload.config_revision, mutation); return Response(status_code=204)

    @app.get("/api/providers/{provider_id}/keys", tags=["management"])
    async def list_provider_keys(request: Request, provider_id: str) -> dict[str, Any]:
        await _authorized_config(request, reload_config); data = _management_config_data(request); provider = _require_provider(_providers(data), provider_id)
        return _with_revision(data, keys=[_raw_key_response(name, key) for name, key in provider.get("keys", {}).items()])

    @app.post("/api/providers/{provider_id}/keys", status_code=201, tags=["management"])
    async def create_provider_key(request: Request, provider_id: str, payload: ProviderKeyCreate) -> dict[str, Any]:
        def mutation(data: dict[str, Any]) -> None:
            keys = _require_provider(_providers(data), provider_id).setdefault("keys", {})
            if payload.name in keys: raise ManagementAPIError(409, f"Key 已存在: {payload.name}")
            keys[payload.name] = {"api_key": payload.api_key, "enabled": payload.enabled, "allow_visitor": payload.allow_visitor}
            _require_provider(_providers(data), provider_id).setdefault("pools", {}).setdefault("default", {"keys": [], "models": []})["keys"].append(payload.name)
        data = await v3_update(request, payload.config_revision, mutation); key = _require_provider(_providers(data), provider_id)["keys"][payload.name]
        return _with_revision(data, **_raw_key_response(payload.name, key))

    @app.get("/api/providers/{provider_id}/keys/{key_name}", tags=["management"])
    async def get_provider_key(request: Request, provider_id: str, key_name: str) -> dict[str, Any]:
        await _authorized_config(request, reload_config); data = _management_config_data(request); key = _require_key(_require_provider(_providers(data), provider_id), key_name)
        return _with_revision(data, **_raw_key_response(key_name, key))

    @app.put("/api/providers/{provider_id}/keys/{key_name}", tags=["management"])
    async def update_provider_key(request: Request, provider_id: str, key_name: str, payload: ProviderKeyUpdate) -> dict[str, Any]:
        updates = _payload_dict(payload); updates.pop("config_revision")
        def mutation(data: dict[str, Any]) -> None:
            provider = _require_provider(_providers(data), provider_id); keys = provider.setdefault("keys", {}); key = _require_key(provider, key_name); new_name = updates.get("name", key_name)
            if new_name != key_name and new_name in keys: raise ManagementAPIError(409, f"Key 已存在: {new_name}")
            if key.get("enabled", True) and updates.get("enabled") is False and _enabled_keys(provider) == 1: raise ManagementAPIError(409, "供应商至少需要一个启用的 key")
            key.update(updates)
            if new_name != key_name:
                keys[new_name] = keys.pop(key_name)
                for pool in provider.get("pools", {}).values(): pool["keys"] = [new_name if name == key_name else name for name in pool.get("keys", [])]
        data = await v3_update(request, payload.config_revision, mutation); name = updates.get("name", key_name); key = _require_key(_require_provider(_providers(data), provider_id), name)
        return _with_revision(data, **_raw_key_response(name, key))

    @app.delete("/api/providers/{provider_id}/keys/{key_name}", status_code=204, response_class=Response, tags=["management"])
    async def delete_provider_key(request: Request, provider_id: str, key_name: str, payload: RevisionPayload) -> Response:
        def mutation(data: dict[str, Any]) -> None:
            provider = _require_provider(_providers(data), provider_id); key = _require_key(provider, key_name)
            if key.get("enabled", True) and _enabled_keys(provider) == 1: raise ManagementAPIError(409, "供应商至少需要一个启用的 key")
            provider["keys"].pop(key_name)
            for pool in provider.get("pools", {}).values(): pool["keys"] = [name for name in pool.get("keys", []) if name != key_name]
        await v3_update(request, payload.config_revision, mutation); return Response(status_code=204)

    @app.get("/api/providers/{provider_id}/pools", tags=["management"])
    async def list_pools(request: Request, provider_id: str) -> dict[str, Any]:
        await _authorized_config(request, reload_config); data = _management_config_data(request); provider = _require_provider(_providers(data), provider_id)
        return _with_revision(data, pools=[{"name": name, **pool} for name, pool in provider.get("pools", {}).items()])

    @app.post("/api/providers/{provider_id}/pools", status_code=201, tags=["management"])
    async def create_pool(request: Request, provider_id: str, payload: PoolCreate) -> dict[str, Any]:
        def mutation(data: dict[str, Any]) -> None:
            provider = _require_provider(_providers(data), provider_id); pools = provider.setdefault("pools", {})
            if payload.name in pools: raise ManagementAPIError(409, f"Pool 已存在: {payload.name}")
            default_pool = pools.get("default")
            if isinstance(default_pool, dict):
                default_pool["keys"] = [key for key in default_pool.get("keys", []) if key not in payload.keys]
            _validate_pool(provider, payload.keys, exclude=None); pools[payload.name] = {"keys": payload.keys, "models": payload.models}
        data = await v3_update(request, payload.config_revision, mutation); pool = _require_provider(_providers(data), provider_id)["pools"][payload.name]
        return _with_revision(data, pool={"name": payload.name, **pool})

    @app.get("/api/providers/{provider_id}/pools/{pool_name}", tags=["management"])
    async def get_pool(request: Request, provider_id: str, pool_name: str) -> dict[str, Any]:
        await _authorized_config(request, reload_config); data = _management_config_data(request); pool = _require_pool(_require_provider(_providers(data), provider_id), pool_name)
        return _with_revision(data, pool={"name": pool_name, **pool})

    @app.put("/api/providers/{provider_id}/pools/{pool_name}", tags=["management"])
    async def update_pool(request: Request, provider_id: str, pool_name: str, payload: PoolUpdate) -> dict[str, Any]:
        updates = _payload_dict(payload); updates.pop("config_revision")
        def mutation(data: dict[str, Any]) -> None:
            provider = _require_provider(_providers(data), provider_id); pools = provider.setdefault("pools", {}); pool = _require_pool(provider, pool_name); new_name = updates.get("name", pool_name)
            if new_name != pool_name and new_name in pools: raise ManagementAPIError(409, f"Pool 已存在: {new_name}")
            if "keys" in updates: _validate_pool(provider, updates["keys"], exclude=pool_name)
            pool.update(updates)
            if new_name != pool_name:
                pools[new_name] = pools.pop(pool_name)
                for route in _routes(data).values():
                    for target in route.get("targets", []):
                        if target.get("provider") == provider_id and target.get("pool") == pool_name: target["pool"] = new_name
        data = await v3_update(request, payload.config_revision, mutation); name = updates.get("name", pool_name); pool = _require_pool(_require_provider(_providers(data), provider_id), name)
        return _with_revision(data, pool={"name": name, **pool})

    @app.delete("/api/providers/{provider_id}/pools/{pool_name}", status_code=204, response_class=Response, tags=["management"])
    async def delete_pool(request: Request, provider_id: str, pool_name: str, payload: RevisionPayload) -> Response:
        def mutation(data: dict[str, Any]) -> None: _require_provider(_providers(data), provider_id).setdefault("pools", {}).pop(pool_name) if _require_pool(_require_provider(_providers(data), provider_id), pool_name) else None
        await v3_update(request, payload.config_revision, mutation); return Response(status_code=204)

    @app.get("/api/routes", tags=["management"])
    async def list_routes(request: Request) -> dict[str, Any]:
        await _authorized_config(request, reload_config); data = _management_config_data(request)
        return _with_revision(data, routes=[{"id": name, **route} for name, route in _routes(data).items()])

    @app.post("/api/routes", status_code=201, tags=["management"])
    async def create_route(request: Request, payload: RouteCreate) -> dict[str, Any]:
        route = _payload_dict(payload); route.pop("config_revision")
        def mutation(data: dict[str, Any]) -> None:
            routes = _routes(data)
            if payload.id in routes: raise ManagementAPIError(409, f"路由已存在: {payload.id}")
            _validate_targets(data, route["targets"]); routes[payload.id] = {key: value for key, value in route.items() if key != "id" and value is not None}
        data = await v3_update(request, payload.config_revision, mutation); return _with_revision(data, route={"id": payload.id, **_routes(data)[payload.id]})

    @app.get("/api/routes/{route_id}", tags=["management"])
    async def get_route(request: Request, route_id: str) -> dict[str, Any]:
        await _authorized_config(request, reload_config); data = _management_config_data(request); route = _require_route(_routes(data), route_id)
        return _with_revision(data, route={"id": route_id, **route})

    @app.put("/api/routes/{route_id}", tags=["management"])
    async def update_route(request: Request, route_id: str, payload: RouteUpdate) -> dict[str, Any]:
        updates = _payload_dict(payload); updates.pop("config_revision")
        def mutation(data: dict[str, Any]) -> None:
            routes = _routes(data); route = _require_route(routes, route_id); new_id = updates.get("id", route_id)
            if new_id != route_id and new_id in routes: raise ManagementAPIError(409, f"路由已存在: {new_id}")
            if "targets" in updates: _validate_targets(data, updates["targets"])
            route.update({key: value for key, value in updates.items() if key != "id"})
            if new_id != route_id: routes[new_id] = routes.pop(route_id)
        data = await v3_update(request, payload.config_revision, mutation); name = updates.get("id", route_id); return _with_revision(data, route={"id": name, **_routes(data)[name]})

    @app.delete("/api/routes/{route_id}", status_code=204, response_class=Response, tags=["management"])
    async def delete_route(request: Request, route_id: str, payload: RevisionPayload) -> Response:
        def mutation(data: dict[str, Any]) -> None: _routes(data).pop(route_id) if _require_route(_routes(data), route_id) else None
        await v3_update(request, payload.config_revision, mutation); return Response(status_code=204)

    async def start_probe(
        request: Request, provider_id: str, key_names: list[str], timeout: float
    ) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        data = _management_config_data(request)
        provider = _require_provider(_providers(data), provider_id)
        available = provider.get("keys", {})
        names = key_names or list(available)
        missing = [name for name in names if name not in available]
        if missing:
            raise HTTPException(status_code=404, detail=f"Key 不存在: {missing[0]}")
        probe_id = uuid.uuid4().hex
        record: dict[str, Any] = {
            "probe_id": probe_id,
            "status": "pending",
            "provider": provider_id,
            "results": [],
            "error": None,
            "cancel_requested": False,
        }
        request.app.state.management_probes[probe_id] = record

        async def run() -> None:
            record["status"] = "running"
            secrets = [str(available[name].get("api_key") or "") for name in names]
            try:
                results = await asyncio.to_thread(
                    _run_key_probe, provider_id, provider, names, timeout
                )
                record["results"] = _redact_probe_data(results, secrets)
                record["status"] = "cancelled" if record["cancel_requested"] else "complete"
            except asyncio.CancelledError:
                record["status"] = "cancelled"
            except Exception as exc:
                record["status"] = "failed"
                record["error"] = _redact_probe_text(str(exc), secrets)

        request.app.state.management_probe_tasks[probe_id] = asyncio.create_task(run())
        return {"probe_id": probe_id, "status": "pending"}

    @app.post("/api/probes/keys", status_code=202, tags=["management"])
    async def probe_keys(request: Request, payload: ProbeKeysRequest) -> dict[str, Any]:
        return await start_probe(
            request, payload.provider_id, payload.keys, payload.timeout_seconds
        )

    @app.post("/api/probes/pools", status_code=202, tags=["management"])
    async def probe_pools(request: Request, payload: ProbePoolsRequest) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        data = _management_config_data(request)
        provider = _require_provider(_providers(data), payload.provider_id)
        pools = provider.get("pools", {})
        names = payload.pools or list(pools)
        keys: list[str] = []
        for name in names:
            pool = _require_pool(provider, name)
            keys.extend(str(key) for key in pool.get("keys", []))
        return await start_probe(
            request, payload.provider_id, list(dict.fromkeys(keys)), payload.timeout_seconds
        )

    @app.get("/api/probes/{probe_id}", tags=["management"])
    async def get_probe(request: Request, probe_id: str) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        record = request.app.state.management_probes.get(probe_id)
        if record is None:
            raise HTTPException(status_code=404, detail="探测不存在")
        return _probe_response(record)

    @app.post("/api/probes/{probe_id}/cancel", tags=["management"])
    async def cancel_probe(request: Request, probe_id: str) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        record = request.app.state.management_probes.get(probe_id)
        if record is None:
            raise HTTPException(status_code=404, detail="探测不存在")
        record["cancel_requested"] = True
        task = request.app.state.management_probe_tasks.get(probe_id)
        if task is not None and not task.done():
            task.cancel()
        return _probe_response(record)

    @app.post("/api/config/export", tags=["management"])
    async def export_config(request: Request) -> dict[str, Any]:
        await _authorized_config(request, reload_config)
        data = _management_config_data(request)
        return _with_revision(data, config=_portable_config(data))

    @app.post("/api/config/import", tags=["management"])
    async def import_config(
        request: Request, payload: ConfigImportRequest
    ) -> dict[str, Any]:
        imported = _portable_config(payload.config)

        def mutation(data: dict[str, Any]) -> None:
            candidate = dict(data)
            candidate.update(deepcopy(imported))
            candidate = migrate_config_data(candidate)
            RouterConfig.from_dict(candidate)
            config_path = Path(str(request.app.state.config_path))
            stamp = datetime.now(timezone.utc).strftime("%Y%m%d%H%M%S")
            save_config_data(config_path.with_name(f"{config_path.name}.{stamp}.bak"), data)
            data.clear()
            data.update(candidate)

        data = await v3_update(request, payload.config_revision, mutation)
        return _with_revision(data, imported=True)

    @app.post(
        "/api/models",
        tags=["management"],
        status_code=status.HTTP_201_CREATED,
    )
    async def create_model(request: Request, payload: ModelCreate) -> dict[str, Any]:
        model_data = _model_create_data(payload)
        config_revision = model_data.pop("config_revision", None)

        def mutation(data: dict[str, Any]) -> None:
            models = _raw_models(data)
            model_id = model_data["id"]
            if _find_raw_model(models, model_id) is not None:
                raise ManagementAPIError(409, f"模型已存在: {model_id}")
            models.append(model_data)

        config = await _update_config(
            request, reload_config, mutation, config_revision=config_revision
        )
        return _model_response(_find_model(config, model_data["id"]))

    @app.get("/api/models/{model_id}", tags=["management"])
    async def get_model(request: Request, model_id: str) -> dict[str, Any]:
        config = await _authorized_config(request, reload_config)
        return _model_response(_find_model(config, model_id))

    @app.put("/api/models/{model_id}", tags=["management"])
    async def update_model(
        request: Request, model_id: str, payload: ModelUpdate
    ) -> dict[str, Any]:
        updates = _payload_dict(payload)
        if not updates:
            raise HTTPException(status_code=400, detail="至少需要提供一个要更新的字段")
        _normalize_model_updates(updates)
        updated_model_id = str(updates.get("id") or model_id)

        def mutation(data: dict[str, Any]) -> None:
            models = _raw_models(data)
            model = _require_raw_model(models, model_id)
            if (
                updated_model_id != model_id
                and _find_raw_model(models, updated_model_id) is not None
            ):
                raise ManagementAPIError(409, f"模型已存在: {updated_model_id}")
            model.update(updates)

        config = await _update_config(request, reload_config, mutation)
        return _model_response(_find_model(config, updated_model_id))

    @app.delete(
        "/api/models/{model_id}",
        tags=["management"],
        status_code=status.HTTP_204_NO_CONTENT,
        response_class=Response,
    )
    async def delete_model(request: Request, model_id: str) -> Response:
        def mutation(data: dict[str, Any]) -> None:
            models = _raw_models(data)
            model = _require_raw_model(models, model_id)
            models.remove(model)

        await _update_config(request, reload_config, mutation)
        return Response(status_code=status.HTTP_204_NO_CONTENT)

    @app.get("/api/models/{model_id}/keys", tags=["management"])
    async def list_keys(request: Request, model_id: str) -> dict[str, Any]:
        config = await _authorized_config(request, reload_config)
        model = _find_model(config, model_id)
        return {"keys": [_key_response(key) for key in model.keys]}

    @app.post(
        "/api/models/{model_id}/keys",
        tags=["management"],
        status_code=status.HTTP_201_CREATED,
    )
    async def create_key(
        request: Request, model_id: str, payload: KeyCreate
    ) -> dict[str, Any]:
        key_data = _key_create_data(payload)
        upstream_routes = key_data.pop("upstream_routes", None)

        def mutation(data: dict[str, Any]) -> None:
            model = _require_raw_model(_raw_models(data), model_id)
            keys = _raw_keys(model)
            key_name = key_data["name"]
            if _find_raw_key(keys, model_id, key_name) is not None:
                raise ManagementAPIError(
                    409, f"模型 {model_id} 的 key 已存在: {key_name}"
                )
            if upstream_routes is not None:
                _set_upstream_routes_for_base_url(
                    data,
                    str(
                        key_data.get("base_url")
                        or data.get("default_base_url")
                        or "https://api.openai.com"
                    ),
                    upstream_routes,
                )
            keys.append(key_data)

        config = await _update_config(request, reload_config, mutation)
        model = _find_model(config, model_id)
        return _key_response(_find_key(model, key_data["name"]))

    @app.get("/api/models/{model_id}/keys/{key_name}", tags=["management"])
    async def get_key(request: Request, model_id: str, key_name: str) -> dict[str, Any]:
        config = await _authorized_config(request, reload_config)
        return _key_response(_find_key(_find_model(config, model_id), key_name))

    @app.get("/api/models/{model_id}/keys/{key_name}/stats", tags=["management"])
    async def get_key_stats(request: Request, model_id: str, key_name: str) -> dict[str, Any]:
        state = request.app.state
        await reload_config(state)
        lease = await state.runtime_manager.acquire()
        try:
            config = lease.resources.config
            if _authorization_mode(request, config.local_api_key) != "full":
                raise HTTPException(status_code=401, detail="本地 API key 验证失败")
            _find_key(_find_model(config, model_id), key_name)
            hours: float | None = None
            hours_param = request.query_params.get("hours")
            if hours_param:
                try:
                    hours = float(hours_param)
                except ValueError:
                    pass
            return await lease.resources.metrics.key_stats(model_id, key_name, hours=hours)
        finally:
            await lease.release()

    @app.put("/api/models/{model_id}/keys/{key_name}", tags=["management"])
    async def update_key(
        request: Request, model_id: str, key_name: str, payload: KeyUpdate
    ) -> dict[str, Any]:
        updates = _payload_dict(payload)
        if not updates:
            raise HTTPException(status_code=400, detail="至少需要提供一个要更新的字段")
        _normalize_key_updates(updates)
        updated_key_name = str(updates.get("name") or key_name)
        upstream_routes = updates.pop("upstream_routes", None)

        def mutation(data: dict[str, Any]) -> None:
            model = _require_raw_model(_raw_models(data), model_id)
            keys = _raw_keys(model)
            key = _require_raw_key(keys, model_id, key_name)
            if (
                updated_key_name != key_name
                and _find_raw_key(keys, model_id, updated_key_name) is not None
            ):
                raise ManagementAPIError(
                    409, f"模型 {model_id} 的 key 已存在: {updated_key_name}"
                )
            if upstream_routes is not None:
                _set_upstream_routes_for_base_url(
                    data,
                    str(
                        updates.get("base_url")
                        or key.get("base_url")
                        or data.get("default_base_url")
                        or "https://api.openai.com"
                    ),
                    upstream_routes,
                )
            key.update(updates)

        config = await _update_config(request, reload_config, mutation)
        model = _find_model(config, model_id)
        return _key_response(_find_key(model, updated_key_name))

    @app.delete(
        "/api/models/{model_id}/keys/{key_name}",
        tags=["management"],
        status_code=status.HTTP_204_NO_CONTENT,
        response_class=Response,
    )
    async def delete_key(request: Request, model_id: str, key_name: str) -> Response:
        def mutation(data: dict[str, Any]) -> None:
            model = _require_raw_model(_raw_models(data), model_id)
            keys = _raw_keys(model)
            key = _require_raw_key(keys, model_id, key_name)
            if len(keys) == 1:
                raise ManagementAPIError(
                    409, f"模型 {model_id} 至少需要一个 key，请直接删除模型"
                )
            keys.remove(key)

        await _update_config(request, reload_config, mutation)
        return Response(status_code=status.HTTP_204_NO_CONTENT)


async def _authorized_config(
    request: Request, reload_config: ReloadConfig
) -> RouterConfig:
    state = request.app.state
    await reload_config(state)
    lease = await state.runtime_manager.acquire()
    try:
        config = lease.resources.config
        if _authorization_mode(request, config.local_api_key) != "full":
            raise HTTPException(status_code=401, detail="本地 API key 验证失败")
        return config
    finally:
        await lease.release()


async def _update_config(
    request: Request,
    reload_config: ReloadConfig,
    mutation: Callable[[dict[str, Any]], None],
    *,
    config_revision: str | None = None,
    v3: bool = False,
) -> RouterConfig:
    state = request.app.state
    async with state.config_write_lock:
        await _authorized_config(request, reload_config)
        config_path = str(getattr(state, "config_path", "") or "")
        if not config_path:
            raise HTTPException(
                status_code=409, detail="未设置配置文件路径，无法持久化修改"
            )
        path = Path(config_path)
        if not path.is_file():
            raise HTTPException(status_code=409, detail=f"配置文件不存在: {path}")
        try:
            def edit(data: dict[str, Any]) -> None:
                if (
                    config_revision is not None
                    and config_revision != _config_revision(data)
                ):
                    raise ManagementAPIError(409, "配置版本已变更，请刷新后重试")
                editable = migrate_config_data(data) if v3 else _management_editable_config_data(data)
                mutation(editable)
                data.clear()
                data.update(editable)

            change = await asyncio.to_thread(ConfigService(path).update, edit)
        except ManagementAPIError as exc:
            raise HTTPException(
                status_code=exc.status_code, detail=exc.message
            ) from exc
        except (KeyError, TypeError, ValueError) as exc:
            raise HTTPException(status_code=400, detail=f"配置校验失败: {exc}") from exc
        except OSError as exc:
            raise HTTPException(status_code=500, detail=f"配置保存失败: {exc}") from exc

        state.config_mtime = -1.0
        await reload_config(state)
        return change.new_config


def _config_revision(data: dict[str, Any]) -> str:
    payload = json.dumps(
        migrate_config_data(data),
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def _with_revision(data: dict[str, Any], **body: Any) -> dict[str, Any]:
    return {**body, "config_revision": _config_revision(data)}


def _management_config_data(request: Request) -> dict[str, Any]:
    config_path = str(getattr(request.app.state, "config_path", "") or "")
    if not config_path:
        raise HTTPException(status_code=409, detail="未设置配置文件路径，无法读取配置版本")
    path = Path(config_path)
    if not path.is_file():
        raise HTTPException(status_code=409, detail=f"配置文件不存在: {path}")
    return migrate_config_data(load_config_data(path))


def _run_key_probe(
    provider_id: str,
    provider: dict[str, Any],
    key_names: list[str],
    timeout: float,
) -> list[dict[str, Any]]:
    from .config_editor import probe_key_availability, probe_provider_key_capabilities

    capabilities = probe_provider_key_capabilities(provider, key_names, timeout=timeout)
    models_by_key = capabilities.get("key_models", {})
    rows: list[dict[str, Any]] = []
    base_url = str(provider.get("base_url") or "")
    for key_name in key_names:
        key = provider.get("keys", {}).get(key_name)
        if not isinstance(key, dict):
            continue
        probe_key = {**key, "name": key_name, "base_url": base_url}
        for result in probe_key_availability(
            {"upstream_routes": {base_url: provider.get("routes", {})}},
            (models_by_key.get(key_name) or [""])[0],
            probe_key,
            timeout=timeout,
        ):
            rows.append(
                {
                    "status": "ok" if result.available else "failed",
                    "provider": provider_id,
                    "key": key_name,
                    "endpoint": result.url,
                    "models": list(models_by_key.get(key_name, [])),
                    "latency_ms": result.duration_ms,
                    "error": result.error or None,
                }
            )
    return rows


def _redact_probe_text(value: str, secrets: list[str]) -> str:
    clean = re.sub(r"(?i)authorization\s*[:=]\s*[^\s,;]+", "Authorization: [redacted]", value)
    for secret in secrets:
        if secret:
            clean = clean.replace(secret, "[redacted]")
    return clean[:160]


def _redact_probe_data(rows: list[dict[str, Any]], secrets: list[str]) -> list[dict[str, Any]]:
    return [{**row, "error": _redact_probe_text(str(row["error"] or ""), secrets) or None} for row in rows]


def _probe_response(record: dict[str, Any]) -> dict[str, Any]:
    return {
        "probe_id": record["probe_id"],
        "status": record["status"],
        "provider": record["provider"],
        "results": list(record["results"]),
        "error": record["error"],
    }


_PORTABLE_CONFIG_FIELDS = {
    "config_version",
    "providers",
    "models",
    "upstream_routes",
    "routing_mode",
    "unified_model",
}


def _portable_config(data: dict[str, Any]) -> dict[str, Any]:
    return {
        name: deepcopy(data[name])
        for name in _PORTABLE_CONFIG_FIELDS
        if name in data
    }


def _settings_response(data: dict[str, Any]) -> dict[str, Any]:
    config = RouterConfig.from_dict(data)
    return {
        "host": config.host,
        "port": config.port,
        "request_timeout": config.request_timeout,
        "stream_first_byte_timeout": config.stream_first_byte_timeout,
        "stream_idle_timeout": config.stream_idle_timeout,
        "max_retries": config.max_retries,
        "local_auth_enabled": bool(config.local_api_key),
        "local_api_key_fingerprint": hashlib.sha256(
            config.local_api_key.encode("utf-8")
        ).hexdigest()[:12]
        if config.local_api_key
        else None,
    }


def _model_create_data(payload: ModelCreate) -> dict[str, Any]:
    data = _payload_dict(payload)
    _normalize_model_updates(data)
    keys = data.get("keys", [])
    if not keys:
        raise HTTPException(status_code=400, detail="新模型至少需要一个 key")
    data["keys"] = [_key_create_data(key) for key in payload.keys]
    return data


def _key_create_data(payload: KeyCreate) -> dict[str, Any]:
    data = _payload_dict(payload)
    _normalize_key_updates(data)
    return data


def _normalize_model_updates(data: dict[str, Any]) -> None:
    _reject_null_fields(data, "id", "aliases", "routing_mode")
    if "id" in data and data["id"] is not None:
        data["id"] = str(data["id"]).strip()
    if "aliases" in data and data["aliases"] is not None:
        data["aliases"] = [str(alias).strip() for alias in data["aliases"]]
    if "routing_mode" in data and data["routing_mode"] is not None:
        data["routing_mode"] = str(data["routing_mode"]).strip()
    if "reasoning_effort" in data and data["reasoning_effort"] is not None:
        data["reasoning_effort"] = str(data["reasoning_effort"]).strip() or None


def _normalize_key_updates(data: dict[str, Any]) -> None:
    _reject_null_fields(data, "name", "api_key", "enabled", "allow_visitor")
    if "name" in data and data["name"] is not None:
        data["name"] = str(data["name"]).strip()
        if not data["name"]:
            raise HTTPException(status_code=400, detail="key 名称不能为空")
    if "api_key" in data and not str(data["api_key"]).strip():
        raise HTTPException(status_code=400, detail="api_key 不能为空")
    if "base_url" in data and data["base_url"] is not None:
        data["base_url"] = str(data["base_url"]).strip()
    if "upstream_routes" in data:
        routes = normalize_upstream_routes(data["upstream_routes"])
        if routes:
            data["upstream_routes"] = routes
        else:
            data["upstream_routes"] = {}


def _set_upstream_routes_for_base_url(
    data: dict[str, Any], base_url: str, routes: dict[str, str]
) -> None:
    normalized_base_url = normalize_upstream_base_url(base_url)
    routes_by_url = data.get("upstream_routes")
    if not isinstance(routes_by_url, dict):
        routes_by_url = {}
    if routes:
        routes_by_url[normalized_base_url] = routes
        data["upstream_routes"] = routes_by_url
    else:
        routes_by_url.pop(normalized_base_url, None)
        if routes_by_url:
            data["upstream_routes"] = routes_by_url
        else:
            data.pop("upstream_routes", None)


def _payload_dict(payload: BaseModel) -> dict[str, Any]:
    if hasattr(payload, "model_dump"):
        return payload.model_dump(exclude_unset=True)
    return payload.dict(exclude_unset=True)


def _management_editable_config_data(data: dict[str, Any]) -> dict[str, Any]:
    config = RouterConfig.from_dict(data)
    editable = dict(data)
    editable["models"] = [
        {
            "id": model.id,
            "aliases": list(model.aliases),
            "routing_mode": model.routing_mode,
            "reasoning_effort": model.reasoning_effort,
            "native_first": model.native_first,
            "keys": [
                {
                    "name": key.name,
                    "api_key": key.api_key,
                    "base_url": key.base_url,
                    "enabled": key.enabled,
                    "allow_visitor": key.allow_visitor,
                    "upstream_model": key.upstream_model or model.id,
                }
                for key in model.keys
            ],
        }
        for model in config.models
    ]
    editable["upstream_routes"] = dict(config.upstream_routes)
    editable.pop("providers", None)
    return editable


def _reject_null_fields(data: dict[str, Any], *fields: str) -> None:
    null_fields = [field for field in fields if field in data and data[field] is None]
    if null_fields:
        raise HTTPException(
            status_code=400,
            detail=f"字段不能为 null: {', '.join(null_fields)}",
        )


def _model_response(model: ModelConfig) -> dict[str, Any]:
    return {
        "id": model.id,
        "aliases": list(model.aliases),
        "routing_mode": model.routing_mode,
        "reasoning_effort": model.reasoning_effort,
        "visitor_available": any(
            key.enabled and key.allow_visitor for key in model.keys
        ),
        "keys": [_key_response(key) for key in model.keys],
    }


def _key_response(key: KeyConfig) -> dict[str, Any]:
    response = {
        "name": key.name,
        "base_url": key.base_url,
        "enabled": key.enabled,
        "allow_visitor": key.allow_visitor,
        "api_key_fingerprint": hashlib.sha256(key.api_key.encode("utf-8")).hexdigest()[
            :12
        ],
    }
    return response


def _provider_response(provider: Any) -> dict[str, Any]:
    return {
        "id": provider.id,
        "base_url": provider.base_url,
        "keys": [
            {
                "name": key.name,
                "enabled": key.enabled,
                "allow_visitor": key.allow_visitor,
                "api_key_fingerprint": hashlib.sha256(
                    key.api_key.encode("utf-8")
                ).hexdigest()[:12],
            }
            for key in provider.keys
        ],
        "pools": [
            {"name": pool.name, "keys": list(pool.keys), "models": list(pool.models)}
            for pool in provider.pools
        ],
        "routes": dict(provider.routes),
    }


def _providers(data: dict[str, Any]) -> dict[str, dict[str, Any]]:
    providers = data.get("providers")
    if not isinstance(providers, dict): raise ValueError("providers 必须是对象")
    return providers


def _routes(data: dict[str, Any]) -> dict[str, dict[str, Any]]:
    routes = data.get("models")
    if not isinstance(routes, dict): raise ValueError("models 必须是对象")
    return routes


def _require_provider(providers: dict[str, Any], provider_id: str) -> dict[str, Any]:
    provider = providers.get(provider_id)
    if not isinstance(provider, dict): raise ManagementAPIError(404, f"供应商不存在: {provider_id}")
    return provider


def _require_key(provider: dict[str, Any], name: str) -> dict[str, Any]:
    key = provider.get("keys", {}).get(name)
    if not isinstance(key, dict): raise ManagementAPIError(404, f"Key 不存在: {name}")
    return key


def _require_pool(provider: dict[str, Any], name: str) -> dict[str, Any]:
    pool = provider.get("pools", {}).get(name)
    if not isinstance(pool, dict): raise ManagementAPIError(404, f"Pool 不存在: {name}")
    return pool


def _require_route(routes: dict[str, Any], name: str) -> dict[str, Any]:
    route = routes.get(name)
    if not isinstance(route, dict): raise ManagementAPIError(404, f"路由不存在: {name}")
    return route


def _valid_url(value: str) -> None:
    parsed = urlparse(value)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc: raise ManagementAPIError(422, "base_url 必须是 http 或 https URL")


def _enabled_keys(provider: dict[str, Any]) -> int:
    return sum(bool(key.get("enabled", True)) for key in provider.get("keys", {}).values() if isinstance(key, dict))


def _validate_pool(provider: dict[str, Any], keys: list[str], exclude: str | None) -> None:
    if len(keys) != len(set(keys)): raise ManagementAPIError(422, "Pool keys 不能重复")
    existing = provider.get("keys", {})
    if any(name not in existing for name in keys): raise ManagementAPIError(422, "Pool 引用了不存在的 key")
    for name, pool in provider.get("pools", {}).items():
        if name != exclude and any(key in pool.get("keys", []) for key in keys): raise ManagementAPIError(422, "Key 只能属于一个 Pool")


def _validate_targets(data: dict[str, Any], targets: list[dict[str, str]]) -> None:
    for target in targets:
        if not isinstance(target, dict): raise ManagementAPIError(422, "targets 必须是对象数组")
        provider = _require_provider(_providers(data), str(target.get("provider") or ""))
        _require_pool(provider, str(target.get("pool") or ""))
        if not str(target.get("upstream_model") or "").strip(): raise ManagementAPIError(422, "target.upstream_model 不能为空")


def _raw_key_response(name: str, key: dict[str, Any]) -> dict[str, Any]:
    return {"name": name, "enabled": bool(key.get("enabled", True)), "allow_visitor": bool(key.get("allow_visitor", False)), "api_key_fingerprint": hashlib.sha256(str(key.get("api_key", "")).encode()).hexdigest()[:12]}


def _raw_provider_response(name: str, provider: dict[str, Any]) -> dict[str, Any]:
    return {"id": name, "base_url": provider.get("base_url"), "keys": [_raw_key_response(key_name, key) for key_name, key in provider.get("keys", {}).items()], "pools": [{"name": pool_name, **pool} for pool_name, pool in provider.get("pools", {}).items()], "routes": dict(provider.get("routes", {}))}


def _find_model(config: RouterConfig, model_id: str) -> ModelConfig:
    for model in config.models:
        if model.id == model_id:
            return model
    raise HTTPException(status_code=404, detail=f"模型不存在: {model_id}")


def _find_key(model: ModelConfig, key_name: str) -> KeyConfig:
    for key in model.keys:
        if key.name == key_name:
            return key
    raise HTTPException(
        status_code=404, detail=f"模型 {model.id} 的 key 不存在: {key_name}"
    )


def _raw_models(data: dict[str, Any]) -> list[dict[str, Any]]:
    models = data.get("models")
    if not isinstance(models, list):
        raise ValueError("models 必须是数组")
    return models


def _raw_keys(model: dict[str, Any]) -> list[dict[str, Any]]:
    keys = model.get("keys")
    if not isinstance(keys, list):
        raise ValueError(f"模型 {model.get('id', '')} 的 keys 必须是数组")
    return keys


def _resolve_model_id(
    models: list[dict[str, Any]], model_name: str
) -> str | None:
    for model in models:
        model_id = str(model.get("id") or "")
        if model_name == model_id:
            return model_id
        aliases = model.get("aliases")
        if isinstance(aliases, list) and model_name in aliases:
            return model_id
    return None


def _find_raw_model(
    models: list[dict[str, Any]], model_id: str
) -> dict[str, Any] | None:
    return next(
        (model for model in models if str(model.get("id") or "") == model_id),
        None,
    )


def _require_raw_model(models: list[dict[str, Any]], model_id: str) -> dict[str, Any]:
    model = _find_raw_model(models, model_id)
    if model is None:
        raise ManagementAPIError(404, f"模型不存在: {model_id}")
    return model


def _find_raw_key(
    keys: list[dict[str, Any]], model_id: str, key_name: str
) -> dict[str, Any] | None:
    for index, key in enumerate(keys):
        effective_name = str(key.get("name") or f"{model_id}-{index + 1}")
        if effective_name == key_name:
            return key
    return None


def _require_raw_key(
    keys: list[dict[str, Any]], model_id: str, key_name: str
) -> dict[str, Any]:
    key = _find_raw_key(keys, model_id, key_name)
    if key is None:
        raise ManagementAPIError(404, f"模型 {model_id} 的 key 不存在: {key_name}")
    return key
