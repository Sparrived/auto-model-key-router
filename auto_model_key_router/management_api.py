from __future__ import annotations

import asyncio
import hashlib
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Any

from fastapi import FastAPI, HTTPException, Request, Response, status
from pydantic import BaseModel, Field

if hasattr(BaseModel, "model_fields"):
    from pydantic import ConfigDict

from .config import KeyConfig, ModelConfig, RouterConfig, normalize_upstream_routes
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


class ManagementAPIError(Exception):
    def __init__(self, status_code: int, message: str) -> None:
        super().__init__(message)
        self.status_code = status_code
        self.message = message


def register_management_api(app: FastAPI, reload_config: ReloadConfig) -> None:
    @app.get("/api/models", tags=["management"])
    async def list_models(request: Request) -> dict[str, Any]:
        config = await _authorized_config(request, reload_config)
        return {"models": [_model_response(model) for model in config.models]}

    @app.post(
        "/api/models",
        tags=["management"],
        status_code=status.HTTP_201_CREATED,
    )
    async def create_model(request: Request, payload: ModelCreate) -> dict[str, Any]:
        model_data = _model_create_data(payload)

        def mutation(data: dict[str, Any]) -> None:
            models = _raw_models(data)
            model_id = model_data["id"]
            if _find_raw_model(models, model_id) is not None:
                raise ManagementAPIError(409, f"模型已存在: {model_id}")
            models.append(model_data)

        config = await _update_config(request, reload_config, mutation)
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

        def mutation(data: dict[str, Any]) -> None:
            model = _require_raw_model(_raw_models(data), model_id)
            keys = _raw_keys(model)
            key_name = key_data["name"]
            if _find_raw_key(keys, model_id, key_name) is not None:
                raise ManagementAPIError(
                    409, f"模型 {model_id} 的 key 已存在: {key_name}"
                )
            keys.append(key_data)

        config = await _update_config(request, reload_config, mutation)
        model = _find_model(config, model_id)
        return _key_response(_find_key(model, key_data["name"]))

    @app.get("/api/models/{model_id}/keys/{key_name}", tags=["management"])
    async def get_key(request: Request, model_id: str, key_name: str) -> dict[str, Any]:
        config = await _authorized_config(request, reload_config)
        return _key_response(_find_key(_find_model(config, model_id), key_name))

    @app.put("/api/models/{model_id}/keys/{key_name}", tags=["management"])
    async def update_key(
        request: Request, model_id: str, key_name: str, payload: KeyUpdate
    ) -> dict[str, Any]:
        updates = _payload_dict(payload)
        if not updates:
            raise HTTPException(status_code=400, detail="至少需要提供一个要更新的字段")
        _normalize_key_updates(updates)
        updated_key_name = str(updates.get("name") or key_name)

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
            change = await asyncio.to_thread(ConfigService(path).update, mutation)
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


def _payload_dict(payload: BaseModel) -> dict[str, Any]:
    if hasattr(payload, "model_dump"):
        return payload.model_dump(exclude_unset=True)
    return payload.dict(exclude_unset=True)


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
    if key.upstream_routes:
        response["upstream_routes"] = dict(key.upstream_routes)
    return response


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
