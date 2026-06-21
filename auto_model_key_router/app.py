from __future__ import annotations

import asyncio
import hashlib
import logging
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

import httpx
from fastapi import FastAPI, Request, WebSocket
from fastapi.responses import JSONResponse, Response, StreamingResponse

from . import __version__
from .config import RouterConfig
from .event_bus import EventBus
from .key_pool import KeyPool
from .management_api import register_management_api
from .metrics import MetricsStore
from .proxy_handler import handle_proxy_request
from .proxy_support import (
    _authorization_mode,
    _join_url,
)
from .runtime import (
    RuntimeLease,
    RuntimeManager,
    RuntimeResources,
    sync_runtime_from_state,
)
from .visitor import visitor_feature_available
from .websocket_proxy import register_websocket_proxy


LOGGER = logging.getLogger("auto_model_key_router.app")


def _new_http_client(timeout: float) -> httpx.AsyncClient:
    return httpx.AsyncClient(
        timeout=timeout,
        limits=httpx.Limits(
            max_connections=100, max_keepalive_connections=30, keepalive_expiry=30
        ),
    )


def create_app(config: RouterConfig, config_path: str | Path | None = None) -> FastAPI:
    @asynccontextmanager
    async def lifespan(lifespan_app: FastAPI) -> AsyncIterator[None]:
        await sync_runtime_from_state(lifespan_app.state)
        if lifespan_app.state.config.upstream_health_check_interval > 0:
            lifespan_app.state.health_probe_task = asyncio.create_task(
                _health_probe_loop(lifespan_app.state)
            )
        lifespan_app.state._metrics_broadcast_task = asyncio.create_task(
            _broadcast_metrics_loop()
        )
        try:
            yield
        finally:
            for attr in ("_metrics_broadcast_task", "health_probe_task"):
                task = getattr(lifespan_app.state, attr, None)
                if task is not None:
                    task.cancel()
                    try:
                        await task
                    except asyncio.CancelledError:
                        pass
            await lifespan_app.state.runtime_manager.close()

    app = FastAPI(title="Auto Model Key Router", version=__version__, lifespan=lifespan)
    app.state.config = config
    app.state.config_path = (
        str(Path(config_path).resolve()) if config_path is not None else ""
    )
    app.state.config_mtime = _config_mtime(app.state.config_path)
    app.state.config_reload_lock = asyncio.Lock()
    app.state.config_write_lock = asyncio.Lock()
    app.state.key_pool = KeyPool(config)
    app.state.metrics = MetricsStore(config.metrics_db_path)
    app.state.http_client = _new_http_client(config.request_timeout)
    app.state.runtime_manager = RuntimeManager(
        RuntimeResources(
            config, app.state.key_pool, app.state.metrics, app.state.http_client
        )
    )
    app.state.health_probe_task = None
    app.state.event_bus = EventBus()

    async def _on_key_state_change(model_id: str, key_name: str, info: dict) -> None:
        await app.state.event_bus.broadcast(
            "key_state_change",
            {"model_id": model_id, "key_name": key_name, "state": info},
        )

    app.state.key_pool.on_key_state_change = _on_key_state_change
    app.state._on_key_state_change = _on_key_state_change

    # metrics_snapshot 节流广播
    _metrics_dirty = asyncio.Event()
    _IDLE_BROADCAST_INTERVAL = 30.0

    async def _broadcast_metrics_snapshot() -> None:
        try:
            snapshot = await app.state.metrics.snapshot()
            await app.state.event_bus.broadcast("metrics_snapshot", snapshot)
        except Exception:
            LOGGER.debug("metrics_snapshot broadcast failed", exc_info=True)

    async def _broadcast_metrics_loop() -> None:
        while True:
            try:
                await asyncio.wait_for(
                    _metrics_dirty.wait(), timeout=_IDLE_BROADCAST_INTERVAL
                )
                _metrics_dirty.clear()
            except asyncio.TimeoutError:
                pass  # 空闲心跳，刷新 RPM/TPM 衰减
            if app.state.event_bus.client_count > 0:
                await _broadcast_metrics_snapshot()
            await asyncio.sleep(1.0)

    async def _on_metrics_recorded() -> None:
        _metrics_dirty.set()

    async def _on_client_count_change(count: int) -> None:
        app.state.event_bus.broadcast(
            "client_count", {"count": count}
        )
        if count > 0:
            await _broadcast_metrics_snapshot()

    app.state.metrics.on_record = _on_metrics_recorded
    app.state.event_bus.on_client_count_change = _on_client_count_change
    app.state._metrics_broadcast_task: asyncio.Task | None = None

    register_management_api(app, _reload_config_if_changed)

    @app.head("/", include_in_schema=False)
    async def root_probe() -> Response:
        return Response(status_code=204)

    @app.get("/health")
    async def health() -> dict[str, Any]:
        await _reload_config_if_changed(app.state)
        lease = await _acquire_runtime(app.state)
        runtime = lease.resources
        try:
            local_api_key = runtime.config.local_api_key
            visitor_key_count = sum(
                runtime.key_pool.visitor_key_count(model_id)
                for model_id in runtime.key_pool.model_ids
            )
            visitor_installed = visitor_feature_available()
            return {
                "status": "ok",
                "models": runtime.key_pool.public_model_ids,
                "config_path": app.state.config_path,
                "local_auth_enabled": bool(local_api_key),
                "local_api_key_fingerprint": _key_fingerprint(local_api_key),
                "visitor_feature_installed": visitor_installed,
                "visitor_access_enabled": visitor_installed and visitor_key_count > 0,
                "visitor_key_count": visitor_key_count if visitor_installed else 0,
                "unified_model": runtime.key_pool.unified_route,
                "key_states": runtime.key_pool.key_states(),
            }
        finally:
            await lease.release()

    @app.get("/v1/models")
    async def models(request: Request) -> Response:
        await _reload_config_if_changed(app.state)
        lease = await _acquire_runtime(app.state)
        try:
            runtime = lease.resources
            authorization_mode = _authorization_mode(
                request, runtime.config.local_api_key
            )
            if authorization_mode is None:
                return JSONResponse(
                    {"error": {"message": "本地 API key 验证失败"}}, status_code=401
                )
            return JSONResponse(
                {
                    "object": "list",
                    "data": [
                        {
                            "id": model_id,
                            "object": "model",
                            "owned_by": "auto-model-key-router",
                        }
                        for model_id in runtime.key_pool.available_model_ids(
                            visitor_only=authorization_mode == "visitor"
                        )
                    ],
                }
            )
        finally:
            await lease.release()

    @app.get("/metrics")
    async def metrics(request: Request):
        await _reload_config_if_changed(app.state)
        lease = await _acquire_runtime(app.state)
        try:
            if (
                _authorization_mode(request, lease.resources.config.local_api_key)
                != "full"
            ):
                return JSONResponse(
                    {"error": {"message": "本地 API key 验证失败"}}, status_code=401
                )
            hours: float | None = None
            hours_param = request.query_params.get("hours")
            if hours_param:
                try:
                    hours = float(hours_param)
                except ValueError:
                    pass
            return await lease.resources.metrics.snapshot(hours=hours)
        finally:
            await lease.release()

    @app.websocket("/ws/events")
    async def ws_events(websocket: WebSocket) -> None:
        await websocket.accept()
        token = websocket.query_params.get("token", "")
        event_bus: EventBus = app.state.event_bus
        if not await event_bus.authenticate(websocket, token, app.state.config.local_api_key):
            return
        try:
            await websocket.send_json({"type": "connected", "data": {}})
            while True:
                try:
                    await websocket.receive_text()
                except Exception:
                    break
        finally:
            await event_bus.disconnect(websocket)

    async def proxy(path: str, request: Request) -> Response:
        await _reload_config_if_changed(app.state)
        lease = await _acquire_runtime(app.state)
        lease.resources.metrics.acquire_active()
        try:
            response = await handle_proxy_request(path, request, lease.resources)
        except BaseException:
            lease.resources.metrics.release_active()
            await lease.release()
            raise
        if isinstance(response, StreamingResponse):
            response.body_iterator = lease.wrap_stream(
                _wrap_active_stream(response.body_iterator, lease.resources.metrics)
            )
        else:
            lease.resources.metrics.release_active()
            await lease.release()
        return response

    app.add_api_route(
        "/v1/{path:path}", proxy, methods=["GET", "POST", "PUT", "PATCH", "DELETE"]
    )
    register_websocket_proxy(app, proxy)
    return app


def _config_mtime(config_path: str) -> float:
    if not config_path:
        return 0.0
    try:
        return Path(config_path).stat().st_mtime
    except OSError:
        return 0.0


async def _reload_config_if_changed(state: Any) -> None:
    config_path = getattr(state, "config_path", "")
    mtime = _config_mtime(config_path)
    if not mtime or mtime == getattr(state, "config_mtime", 0.0):
        return
    async with state.config_reload_lock:
        await sync_runtime_from_state(state)
        mtime = _config_mtime(config_path)
        if not mtime or mtime == getattr(state, "config_mtime", 0.0):
            return
        try:
            config = await asyncio.to_thread(RouterConfig.load, config_path)
        except (OSError, ValueError):
            return
        old_runtime = state.runtime_manager.current
        old_config = old_runtime.config
        http_client = old_runtime.http_client
        metrics = old_runtime.metrics
        if old_config.request_timeout != config.request_timeout:
            http_client = _new_http_client(config.request_timeout)
        if old_config.metrics_db_path != config.metrics_db_path:
            metrics = await asyncio.to_thread(MetricsStore, config.metrics_db_path)
        if (
            old_config.upstream_health_check_interval
            <= 0
            < config.upstream_health_check_interval
        ):
            state.health_probe_task = asyncio.create_task(_health_probe_loop(state))
        if (
            old_config.upstream_health_check_interval > 0
            and config.upstream_health_check_interval <= 0
        ):
            task = getattr(state, "health_probe_task", None)
            if task is not None:
                task.cancel()
                try:
                    await task
                except asyncio.CancelledError:
                    pass
                state.health_probe_task = None
        state.config = config
        state.key_pool = await asyncio.to_thread(KeyPool, config)
        state.key_pool.on_key_state_change = getattr(state, "_on_key_state_change", None)
        state.http_client = http_client
        state.metrics = metrics
        await state.runtime_manager.replace(
            RuntimeResources(config, state.key_pool, metrics, http_client)
        )
        state.config_mtime = _config_mtime(config_path) or mtime
        event_bus: EventBus = state.event_bus
        if event_bus.client_count > 0:
            await event_bus.broadcast("config_change", {"reloaded": True})


async def _acquire_runtime(state: Any) -> RuntimeLease:
    async with state.config_reload_lock:
        await sync_runtime_from_state(state)
        return await state.runtime_manager.acquire()


async def _wrap_active_stream(
    iterator: AsyncIterator[bytes], metrics: MetricsStore
) -> AsyncIterator[bytes]:
    """包装流式响应迭代器，流结束时释放活跃计数。"""
    try:
        async for chunk in iterator:
            yield chunk
    finally:
        metrics.release_active()


def _key_fingerprint(api_key: str) -> str:
    if not api_key:
        return ""
    return hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:12]


async def _health_probe_loop(state: Any) -> None:
    while True:
        interval = state.runtime_manager.current.config.upstream_health_check_interval
        await asyncio.sleep(interval)
        await _probe_cooling_keys(state)


async def _probe_cooling_keys(state: Any) -> None:
    lease = await _acquire_runtime(state)
    runtime = lease.resources
    try:
        for model_id in runtime.key_pool.model_ids:
            for key in runtime.key_pool.keys_for_model(model_id):
                key_state = runtime.key_pool.key_states().get(
                    f"{model_id}:{key.name}", {}
                )
                if int(key_state.get("cooldown_remaining_seconds") or 0) <= 0:
                    continue
                if await _probe_key(runtime.http_client, key):
                    await runtime.key_pool.mark_success(model_id, key.name)
    finally:
        await lease.release()


async def _probe_key(client: httpx.AsyncClient, key: Any) -> bool:
    try:
        response = await client.get(
            _join_url(key.base_url, "/v1/models"),
            headers={"Authorization": f"Bearer {key.api_key}"},
        )
        await response.aread()
        await response.aclose()
        return response.status_code < 400
    except httpx.RequestError:
        return False
