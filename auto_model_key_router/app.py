from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import re
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from datetime import datetime
from email.utils import parsedate_to_datetime
from pathlib import Path
from time import perf_counter
from typing import Any
from urllib.request import Request as UrlRequest, urlopen

import httpx
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse

from . import __version__
from .config import RouterConfig
from .key_pool import KeyPool
from .metrics import MetricsStore, extract_usage
from .protocol_compat import (
    _adapt_message_payload,
    _anthropic_message_response,
    _anthropic_sse,
    _anthropic_stream_events,
    _anthropic_stream_usage,
    _estimate_anthropic_input_tokens,
    _finish_anthropic_stream_tools,
    _flush_stream_event,
    _responses_response,
    _responses_sse,
    _responses_stream_events,
    _responses_stream_output_items,
    _responses_usage,
    _stream_usage,
)
from .visitor import is_visitor_api_key, visitor_feature_available
from .websocket_proxy import register_websocket_proxy


LOGGER = logging.getLogger("auto_model_key_router.app")
RETRYABLE_STATUS_CODES = frozenset({401, 403, 429, 500, 502, 503, 504})


def _log_model_not_configured(path: str, requested_model_id: str, model_id: str, reason: str) -> None:
    LOGGER.warning(
        "model routing rejected: path=/v1/%s requested_model=%s resolved_model=%s reason=%s",
        path,
        requested_model_id,
        model_id,
        reason,
    )


def _new_http_client(timeout: float) -> httpx.AsyncClient:
    return httpx.AsyncClient(
        timeout=timeout,
        limits=httpx.Limits(max_connections=100, max_keepalive_connections=30, keepalive_expiry=30),
    )


def create_app(config: RouterConfig, config_path: str | Path | None = None) -> FastAPI:
    @asynccontextmanager
    async def lifespan(lifespan_app: FastAPI) -> AsyncIterator[None]:
        if lifespan_app.state.config.upstream_health_check_interval > 0:
            lifespan_app.state.health_probe_task = asyncio.create_task(_health_probe_loop(lifespan_app.state))
        try:
            yield
        finally:
            task = getattr(lifespan_app.state, "health_probe_task", None)
            if task is not None:
                task.cancel()
                try:
                    await task
                except asyncio.CancelledError:
                    pass
            await lifespan_app.state.http_client.aclose()
            await lifespan_app.state.metrics.close()

    app = FastAPI(title="Auto Model Key Router", version=__version__, lifespan=lifespan)
    app.state.config = config
    app.state.config_path = str(Path(config_path).resolve()) if config_path is not None else ""
    app.state.config_mtime = _config_mtime(app.state.config_path)
    app.state.config_reload_lock = asyncio.Lock()
    app.state.key_pool = KeyPool(config)
    app.state.metrics = MetricsStore(config.metrics_db_path)
    app.state.http_client = _new_http_client(config.request_timeout)
    app.state.health_probe_task = None

    @app.head("/", include_in_schema=False)
    async def root_probe() -> Response:
        return Response(status_code=204)

    @app.get("/health")
    async def health() -> dict[str, Any]:
        await _reload_config_if_changed(app.state)
        local_api_key = app.state.config.local_api_key
        visitor_key_count = sum(
            app.state.key_pool.visitor_key_count(model_id)
            for model_id in app.state.key_pool.model_ids
        )
        visitor_installed = visitor_feature_available()
        return {
            "status": "ok",
            "models": app.state.key_pool.public_model_ids,
            "config_path": app.state.config_path,
            "local_auth_enabled": bool(local_api_key),
            "local_api_key_fingerprint": _key_fingerprint(local_api_key),
            "visitor_feature_installed": visitor_installed,
            "visitor_access_enabled": visitor_installed and visitor_key_count > 0,
            "visitor_key_count": visitor_key_count if visitor_installed else 0,
            "unified_model": app.state.key_pool.unified_route,
            "key_states": app.state.key_pool.key_states(),
        }

    @app.get("/v1/models")
    async def models() -> dict[str, Any]:
        await _reload_config_if_changed(app.state)
        return {
            "object": "list",
            "data": [
                {"id": model_id, "object": "model", "owned_by": "auto-model-key-router"}
                for model_id in app.state.key_pool.public_model_ids
            ],
        }

    @app.get("/metrics")
    async def metrics(request: Request):
        await _reload_config_if_changed(app.state)
        if _authorization_mode(request, app.state.config.local_api_key) != "full":
            return JSONResponse({"error": {"message": "本地 API key 验证失败"}}, status_code=401)
        return await app.state.metrics.snapshot()

    @app.api_route("/v1/{path:path}", methods=["GET", "POST", "PUT", "PATCH", "DELETE"])
    async def proxy(path: str, request: Request) -> Response:
        await _reload_config_if_changed(app.state)
        authorization_mode = _authorization_mode(request, app.state.config.local_api_key)
        if authorization_mode is None:
            return JSONResponse({"error": {"message": "本地 API key 验证失败"}}, status_code=401)
        visitor_only = authorization_mode == "visitor"
        caller_type = "visitor" if visitor_only else "local"

        body = await request.body()
        payload = _json_body(body)
        is_stream = _is_stream_request(payload)
        requested_model_id = _resolve_model_id(path, payload)
        _debug_report("proxy-entry", {"path": path, "method": request.method, "requested_model_id": requested_model_id, "stream": is_stream, "body_bytes": len(body)})
        if requested_model_id is None:
            return JSONResponse({"error": {"message": "请求体中缺少 model 字段"}}, status_code=400)
        requested_model_name, requested_key_name = _split_requested_model_key(requested_model_id)
        model_id, requested_key_name = app.state.key_pool.resolve_route(requested_model_name, requested_key_name)
        upstream_body = _upstream_body(body, payload, model_id, app.state.config, stream=is_stream)

        excluded: set[str] = set()
        last_error: JSONResponse | None = None
        configured_key_count = app.state.key_pool.key_count(model_id)
        key_count = app.state.key_pool.visitor_key_count(model_id) if visitor_only else configured_key_count
        if configured_key_count == 0:
            _log_model_not_configured(path, requested_model_id, model_id, "no_configured_keys")
            return JSONResponse({"error": {"message": f"未配置模型: {model_id}"}}, status_code=404)
        if key_count == 0:
            return JSONResponse({"error": {"message": f"访客 key 无权访问模型: {requested_model_name}"}}, status_code=403)
        if path == "messages/count_tokens":
            return JSONResponse({"input_tokens": _estimate_anthropic_input_tokens(payload)})

        only_first = app.state.key_pool.routing_mode(model_id) == "only_first"
        attempts = app.state.config.max_retries + 1 if requested_key_name or only_first or key_count == 1 else key_count

        for attempt in range(attempts):
            try:
                if requested_key_name:
                    key = app.state.key_pool.key_by_name(model_id, requested_key_name, visitor_only=visitor_only)
                    await app.state.key_pool.acquire_key(model_id, key.name)
                else:
                    key = await app.state.key_pool.next_key(model_id, excluded, visitor_only=visitor_only)
            except KeyError:
                _log_model_not_configured(path, requested_model_id, model_id, "key_selection_failed")
                return JSONResponse({"error": {"message": f"未配置模型: {model_id}"}}, status_code=404)
            except RuntimeError as exc:
                if visitor_only and requested_key_name:
                    return JSONResponse({"error": {"message": f"访客 key 无权访问模型 key: {requested_model_name}[{requested_key_name}]"}}, status_code=403)
                return JSONResponse({"error": {"message": str(exc)}}, status_code=503)

            release_key = True
            try:
                if key_count > 1 and not requested_key_name and not only_first:
                    excluded.add(key.name)
                upstream = _join_url(key.base_url, f"/v1/{_upstream_path(path, payload)}")
                headers = _upstream_headers(request, key.api_key)
                _debug_report("upstream-attempt", {"upstream": upstream, "model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "stream": is_stream})

                started = perf_counter()
                try:
                    response = await _send_upstream(
                        app.state.http_client,
                        request,
                        upstream,
                        headers,
                        upstream_body,
                        stream=is_stream,
                        timeout=_upstream_timeout(app.state.config, is_stream),
                    )
                    duration_ms = _elapsed_ms(started)
                except httpx.RequestError as exc:
                    duration_ms = _elapsed_ms(started)
                    _debug_report("upstream-request-error", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "error_type": exc.__class__.__name__, "error": str(exc), "duration_ms": duration_ms})
                    last_error = JSONResponse({"error": {"message": f"上游请求失败: {exc.__class__.__name__}"}}, status_code=502)
                    await app.state.metrics.record(
                        model_id,
                        key.name,
                        None,
                        retried=True,
                        failed=True,
                        duration_ms=duration_ms,
                        first_token_ms=duration_ms,
                        requested_model_id=requested_model_id,
                        caller_type=caller_type,
                    )
                    await app.state.key_pool.mark_failure(model_id, key.name)
                    continue

                if response.status_code in RETRYABLE_STATUS_CODES and attempt + 1 < attempts:
                    content = await response.aread()
                    duration_ms = _elapsed_ms(started)
                    _debug_report("upstream-retryable-response", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "status_code": response.status_code, "duration_ms": duration_ms, "content_preview": content[:500].decode("utf-8", errors="replace")})
                    last_error = _json_error_response_from_content(response, content)
                    await _record_upstream_response(app.state, model_id, key.name, requested_model_id, response, content, duration_ms, retried=True, caller_type=caller_type)
                    await response.aclose()
                    continue

                if is_stream and response.status_code >= 400:
                    content = await response.aread()
                    duration_ms = _elapsed_ms(started)
                    _debug_report("upstream-stream-error-response", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "status_code": response.status_code, "duration_ms": duration_ms, "content_type": response.headers.get("content-type"), "content_preview": content[:500].decode("utf-8", errors="replace")})
                    await response.aclose()
                    await _record_upstream_response(app.state, model_id, key.name, requested_model_id, response, content, duration_ms, caller_type=caller_type)
                    return _json_error_response_from_content(response, content, anthropic=path == "messages")

                if is_stream:
                    _debug_report("upstream-stream-response", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "status_code": response.status_code, "duration_ms": duration_ms, "content_type": response.headers.get("content-type")})
                    stream = _stream_upstream(response, app.state.metrics, app.state.key_pool, model_id, key.name, requested_model_id, upstream, started, caller_type)
                    media_type = response.headers.get("content-type")
                    response_headers = _response_headers(response)
                    if _is_sse_media_type(media_type):
                        response_headers["cache-control"] = "no-cache"
                        response_headers["x-accel-buffering"] = "no"
                    if path == "messages":
                        stream = _stream_anthropic_messages(response, app.state.metrics, app.state.key_pool, model_id, key.name, requested_model_id, upstream, started, caller_type)
                        media_type = "text/event-stream"
                        response_headers["cache-control"] = "no-cache"
                        response_headers["x-accel-buffering"] = "no"
                    elif path == "responses":
                        stream = _stream_responses(response, app.state.metrics, app.state.key_pool, model_id, key.name, requested_model_id, upstream, started, caller_type)
                        media_type = "text/event-stream"
                        response_headers["cache-control"] = "no-cache"
                        response_headers["x-accel-buffering"] = "no"
                    stream_response = StreamingResponse(
                        stream,
                        status_code=response.status_code,
                        headers=response_headers,
                        media_type=media_type,
                    )
                    release_key = False
                    return stream_response

                content = await response.aread()
                duration_ms = _elapsed_ms(started)
                _debug_report("upstream-buffered-response", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "status_code": response.status_code, "duration_ms": duration_ms, "content_type": response.headers.get("content-type"), "content_preview": content[:500].decode("utf-8", errors="replace")})
                await response.aclose()
                await _record_upstream_response(app.state, model_id, key.name, requested_model_id, response, content, duration_ms, caller_type=caller_type)

                if response.status_code >= 400:
                    return _json_error_response_from_content(response, content, anthropic=path == "messages")
                if path == "messages":
                    anthropic_response = _anthropic_message_response(_json_bytes(content), requested_model_id)
                    if anthropic_response is None:
                        return JSONResponse({"type": "error", "error": {"type": "api_error", "message": "上游返回了非 JSON 响应，无法转换为 Anthropic Messages 响应"}}, status_code=502)
                    return JSONResponse(anthropic_response, status_code=response.status_code, headers=_response_headers(response))
                if path == "responses":
                    responses_response = _responses_response(_json_bytes(content), requested_model_id)
                    if responses_response is None:
                        return JSONResponse({"error": {"message": "上游返回了非 JSON 响应，无法转换为 Responses 响应"}}, status_code=502)
                    return JSONResponse(responses_response, status_code=response.status_code, headers=_response_headers(response))
                return Response(
                    content=content,
                    status_code=response.status_code,
                    headers=_response_headers(response),
                    media_type=response.headers.get("content-type"),
                )
            finally:
                if release_key:
                    await app.state.key_pool.release_key(model_id, key.name)

        return last_error or JSONResponse({"error": {"message": "没有可用 key"}}, status_code=503)

    register_websocket_proxy(app, proxy)
    return app


async def _record_upstream_response(
    state: Any,
    model_id: str,
    key_name: str,
    requested_model_id: str,
    response: httpx.Response,
    content: bytes,
    duration_ms: int,
    *,
    retried: bool = False,
    caller_type: str = "local",
) -> None:
    await state.metrics.record(
        model_id,
        key_name,
        response.status_code,
        extract_usage(_json_bytes(content)),
        retried=retried,
        duration_ms=duration_ms,
        first_token_ms=duration_ms,
        requested_model_id=requested_model_id,
        caller_type=caller_type,
    )
    if response.status_code < 400:
        await state.key_pool.mark_success(model_id, key_name)
    elif response.status_code in RETRYABLE_STATUS_CODES:
        await state.key_pool.mark_failure(model_id, key_name, response.status_code, _retry_after_seconds(response))


def _elapsed_ms(started: float) -> int:
    return round((perf_counter() - started) * 1000)


def _json_body(body: bytes) -> dict[str, Any]:
    if not body:
        return {}
    try:
        import json

        data = json.loads(body.decode("utf-8"))
        return data if isinstance(data, dict) else {}
    except ValueError:
        return {}


def _resolve_model_id(path: str, payload: dict[str, Any]) -> str | None:
    if path == "models":
        return ""
    model = payload.get("model")
    return str(model) if model else None


def _split_requested_model_key(model_id: str) -> tuple[str, str | None]:
    match = re.fullmatch(r"(.+)\[([^\[\]]+)\]", model_id)
    if not match:
        return model_id, None
    return match.group(1), match.group(2).strip()


def _is_stream_request(payload: dict[str, Any]) -> bool:
    return payload.get("stream") is True


def _upstream_body(body: bytes, payload: dict[str, Any], model_id: str, config: RouterConfig | None = None, stream: bool = False) -> bytes:
    if not payload or "model" not in payload:
        return body
    upstream_payload = dict(payload)
    upstream_payload["model"] = model_id
    upstream_payload = _apply_reasoning_effort(upstream_payload, model_id, config)
    upstream_payload = _adapt_message_payload(upstream_payload)
    if stream:
        stream_options = upstream_payload.get("stream_options")
        if not isinstance(stream_options, dict):
            stream_options = {}
        stream_options["include_usage"] = True
        upstream_payload["stream_options"] = stream_options
    return json.dumps(upstream_payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def _apply_reasoning_effort(payload: dict[str, Any], model_id: str, config: RouterConfig | None) -> dict[str, Any]:
    adapted = dict(payload)
    reasoning_effort = config.reasoning_effort_by_model.get(model_id) if config is not None else None
    if reasoning_effort:
        adapted["reasoning_effort"] = reasoning_effort
        return adapted
    reasoning = adapted.get("reasoning")
    if "reasoning_effort" not in adapted and isinstance(reasoning, dict) and reasoning.get("effort"):
        adapted["reasoning_effort"] = reasoning["effort"]
    return adapted


def _upstream_path(path: str, payload: dict[str, Any]) -> str:
    if path in {"messages", "responses"} and isinstance(payload, dict) and payload.get("model"):
        return "chat/completions"
    return path


























def _join_url(base_url: str, path: str) -> str:
    return f"{base_url.rstrip('/')}/{path.lstrip('/')}"


def _upstream_headers(request: Request, api_key: str) -> dict[str, str]:
    blocked = {"authorization", "host", "content-length", "destination-addr", "x-api-key", "anthropic-version", "anthropic-beta"}
    headers = {key: value for key, value in request.headers.items() if key.lower() not in blocked}
    headers["Authorization"] = f"Bearer {api_key}"
    return headers


def _authorization_mode(request: Request, local_api_key: str) -> str | None:
    if not local_api_key:
        return "full"
    authorization = request.headers.get("authorization", "")
    if authorization.lower().startswith("bearer "):
        api_key = authorization[7:].strip()
    else:
        api_key = request.headers.get("x-api-key", "")
    if api_key == local_api_key:
        return "full"
    if is_visitor_api_key(api_key):
        return "visitor"
    return None


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
        mtime = _config_mtime(config_path)
        if not mtime or mtime == getattr(state, "config_mtime", 0.0):
            return
        try:
            config = RouterConfig.load(config_path)
        except (OSError, ValueError):
            return
        old_config = state.config
        if old_config.request_timeout != config.request_timeout:
            old_client = state.http_client
            state.http_client = _new_http_client(config.request_timeout)
            await old_client.aclose()
        if old_config.metrics_db_path != config.metrics_db_path:
            old_metrics = state.metrics
            state.metrics = MetricsStore(config.metrics_db_path)
            await old_metrics.close()
        if old_config.upstream_health_check_interval <= 0 < config.upstream_health_check_interval:
            state.health_probe_task = asyncio.create_task(_health_probe_loop(state))
        if old_config.upstream_health_check_interval > 0 and config.upstream_health_check_interval <= 0:
            task = getattr(state, "health_probe_task", None)
            if task is not None:
                task.cancel()
                try:
                    await task
                except asyncio.CancelledError:
                    pass
                state.health_probe_task = None
        state.config = config
        await state.key_pool.reconfigure(config)
        state.config_mtime = _config_mtime(config_path) or mtime


def _key_fingerprint(api_key: str) -> str:
    if not api_key:
        return ""
    return hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:12]


def _response_headers(response: httpx.Response) -> dict[str, str]:
    blocked = {"content-encoding", "content-length", "transfer-encoding", "connection"}
    return {key: value for key, value in response.headers.items() if key.lower() not in blocked}


def _retry_after_seconds(response: httpx.Response) -> float | None:
    value = response.headers.get("retry-after")
    if not value:
        return None
    try:
        return max(0.0, float(value))
    except ValueError:
        try:
            retry_at = parsedate_to_datetime(value)
            if retry_at.tzinfo is None:
                retry_at = retry_at.astimezone()
            return max(0.0, retry_at.timestamp() - datetime.now(retry_at.tzinfo).timestamp())
        except (TypeError, ValueError, OSError):
            return None


def _upstream_timeout(config: RouterConfig, stream: bool) -> httpx.Timeout | None:
    if not stream:
        return None
    return httpx.Timeout(config.request_timeout, read=None)


async def _send_upstream(
    client: httpx.AsyncClient,
    request: Request,
    upstream: str,
    headers: dict[str, str],
    body: bytes,
    stream: bool = False,
    timeout: httpx.Timeout | None = None,
) -> httpx.Response:
    request_kwargs: dict[str, Any] = {
        "params": request.query_params,
        "headers": headers,
        "content": body,
    }
    if timeout is not None:
        request_kwargs["timeout"] = timeout
    response = await client.send(
        client.build_request(
            request.method,
            upstream,
            **request_kwargs,
        ),
        stream=True,
    )
    try:
        if not stream:
            await response.aread()
    except Exception:
        await response.aclose()
        raise
    return response


async def _health_probe_loop(state: Any) -> None:
    while True:
        await asyncio.sleep(state.config.upstream_health_check_interval)
        await _probe_cooling_keys(state)


async def _probe_cooling_keys(state: Any) -> None:
    for model_id in state.key_pool.model_ids:
        for key in state.key_pool.keys_for_model(model_id):
            key_state = state.key_pool.key_states().get(f"{model_id}:{key.name}", {})
            if int(key_state.get("cooldown_remaining_seconds") or 0) <= 0:
                continue
            if await _probe_key(state.http_client, key):
                await state.key_pool.mark_success(model_id, key.name)


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


async def _stream_upstream(response: httpx.Response, metrics: MetricsStore, key_pool: KeyPool, model_id: str, key_name: str, requested_model_id: str, upstream: str, started: float, caller_type: str = "local"):
    chunk_count = 0
    byte_count = 0
    buffer = ""
    sse_buffer = b""
    is_sse = _is_sse_media_type(response.headers.get("content-type"))
    usage: dict[str, Any] | None = None
    first_token_ms = 0
    failed = False
    try:
        async for chunk in response.aiter_bytes():
            chunk_count += 1
            byte_count += len(chunk)
            if first_token_ms == 0:
                first_token_ms = _elapsed_ms(started)
            buffer, chunk_usage = _stream_usage(buffer, chunk)
            if chunk_usage is not None:
                usage = chunk_usage
            if is_sse:
                sse_buffer, events = _split_sse_events(sse_buffer, chunk)
                for event in events:
                    yield event
                    await _flush_stream_event()
            else:
                yield chunk
        if sse_buffer:
            yield sse_buffer
            await _flush_stream_event()
            sse_buffer = b""
    except Exception as exc:
        if sse_buffer:
            yield sse_buffer
            await _flush_stream_event()
            sse_buffer = b""
        failed = True
        payload = {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key_name, "upstream": upstream, "status_code": response.status_code, "content_type": response.headers.get("content-type"), "error_type": exc.__class__.__name__, "error": str(exc), "chunks": chunk_count, "bytes": byte_count, "duration_ms": _elapsed_ms(started)}
        LOGGER.warning("upstream stream error %s", json.dumps(payload, ensure_ascii=False))
        _debug_report("upstream-stream-error", payload)
    finally:
        await metrics.record(
            model_id,
            key_name,
            response.status_code,
            usage,
            failed=failed,
            duration_ms=_elapsed_ms(started),
            first_token_ms=first_token_ms,
            requested_model_id=requested_model_id,
            caller_type=caller_type,
        )
        if failed or response.status_code in RETRYABLE_STATUS_CODES:
            await key_pool.mark_failure(model_id, key_name, response.status_code, _retry_after_seconds(response))
        elif response.status_code < 400:
            await key_pool.mark_success(model_id, key_name)
        await key_pool.release_key(model_id, key_name)
        _debug_report("upstream-stream-close", {"status_code": response.status_code, "chunks": chunk_count, "bytes": byte_count, "first_token_ms": first_token_ms, "usage": usage})
        await response.aclose()


def _is_sse_media_type(media_type: str | None) -> bool:
    return bool(media_type and media_type.split(";", 1)[0].strip().lower() == "text/event-stream")


def _split_sse_events(buffer: bytes, chunk: bytes) -> tuple[bytes, list[bytes]]:
    buffer += chunk
    events: list[bytes] = []
    while True:
        separators = [(index, separator) for separator in (b"\n\n", b"\r\n\r\n") if (index := buffer.find(separator)) >= 0]
        if not separators:
            return buffer, events
        index, separator = min(separators, key=lambda item: item[0])
        end = index + len(separator)
        events.append(buffer[:end])
        buffer = buffer[end:]


async def _stream_anthropic_messages(response: httpx.Response, metrics: MetricsStore, key_pool: KeyPool, model_id: str, key_name: str, requested_model_id: str, upstream: str, started: float, caller_type: str = "local"):
    chunk_count = 0
    byte_count = 0
    buffer = ""
    usage: dict[str, Any] | None = None
    first_token_ms = 0
    failed = False
    stream_state: dict[str, Any] = {
        "next_content_index": 0,
        "text_index": None,
        "text_stopped": False,
        "tool_calls": {},
        "active_tool_index": None,
        "stop_reason": None,
    }
    try:
        yield _anthropic_sse("message_start", {"type": "message_start", "message": {"id": "msg_amkr", "type": "message", "role": "assistant", "model": requested_model_id, "content": [], "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 0, "output_tokens": 0}}})
        async for chunk in response.aiter_bytes():
            chunk_count += 1
            byte_count += len(chunk)
            if first_token_ms == 0:
                first_token_ms = _elapsed_ms(started)
            buffer, events, chunk_usage = _anthropic_stream_events(buffer, chunk, stream_state)
            if chunk_usage is not None:
                usage = chunk_usage
            for event in events:
                yield event
                await _flush_stream_event()
        if buffer.strip():
            buffer, events, chunk_usage = _anthropic_stream_events(buffer, b"\n", stream_state)
            if chunk_usage is not None:
                usage = chunk_usage
            for event in events:
                yield event
                await _flush_stream_event()

        text_index = stream_state.get("text_index")
        if isinstance(text_index, int) and not stream_state["text_stopped"]:
            yield _anthropic_sse("content_block_stop", {"type": "content_block_stop", "index": text_index})
            await _flush_stream_event()
            stream_state["text_stopped"] = True

        tool_calls = stream_state["tool_calls"]
        for event in _finish_anthropic_stream_tools(stream_state):
            yield event
            await _flush_stream_event()

        if stream_state["next_content_index"] == 0:
            yield _anthropic_sse("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}})
            await _flush_stream_event()
            yield _anthropic_sse("content_block_stop", {"type": "content_block_stop", "index": 0})
            await _flush_stream_event()

        stop_reason = stream_state.get("stop_reason")
        if not stop_reason:
            stop_reason = "tool_use" if tool_calls else "end_turn"
        yield _anthropic_sse("message_delta", {"type": "message_delta", "delta": {"stop_reason": stop_reason, "stop_sequence": None}, "usage": _anthropic_stream_usage(usage)})
        await _flush_stream_event()
        yield _anthropic_sse("message_stop", {"type": "message_stop"})
    except Exception as exc:
        failed = True
        payload = {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key_name, "upstream": upstream, "status_code": response.status_code, "content_type": response.headers.get("content-type"), "error_type": exc.__class__.__name__, "error": str(exc), "chunks": chunk_count, "bytes": byte_count, "duration_ms": _elapsed_ms(started)}
        LOGGER.warning("upstream anthropic stream error %s", json.dumps(payload, ensure_ascii=False))
        _debug_report("upstream-anthropic-stream-error", payload)
    finally:
        await metrics.record(
            model_id,
            key_name,
            response.status_code,
            usage,
            failed=failed,
            duration_ms=_elapsed_ms(started),
            first_token_ms=first_token_ms,
            requested_model_id=requested_model_id,
            caller_type=caller_type,
        )
        if failed or response.status_code in RETRYABLE_STATUS_CODES:
            await key_pool.mark_failure(model_id, key_name, response.status_code, _retry_after_seconds(response))
        elif response.status_code < 400:
            await key_pool.mark_success(model_id, key_name)
        await key_pool.release_key(model_id, key_name)
        _debug_report("upstream-anthropic-stream-close", {"status_code": response.status_code, "chunks": chunk_count, "bytes": byte_count, "first_token_ms": first_token_ms, "usage": usage})
        await response.aclose()


async def _stream_responses(response: httpx.Response, metrics: MetricsStore, key_pool: KeyPool, model_id: str, key_name: str, requested_model_id: str, upstream: str, started: float, caller_type: str = "local"):
    chunk_count = 0
    byte_count = 0
    buffer = ""
    usage: dict[str, Any] | None = None
    first_token_ms = 0
    failed = False
    stream_state: dict[str, Any] = {"text": [], "text_started": False, "tool_calls": {}}
    try:
        async for chunk in response.aiter_bytes():
            chunk_count += 1
            byte_count += len(chunk)
            if first_token_ms == 0:
                first_token_ms = _elapsed_ms(started)
            buffer, events, chunk_usage = _responses_stream_events(buffer, chunk, stream_state)
            if chunk_usage is not None:
                usage = chunk_usage
            for event in events:
                yield event
                await _flush_stream_event()
        if buffer.strip():
            buffer, events, chunk_usage = _responses_stream_events(buffer, b"\n", stream_state)
            if chunk_usage is not None:
                usage = chunk_usage
            for event in events:
                yield event
                await _flush_stream_event()

        for output_index, item in enumerate(_responses_stream_output_items(stream_state)):
            yield _responses_sse("response.output_item.done", {"type": "response.output_item.done", "output_index": output_index, "item": item})
            await _flush_stream_event()
        yield _responses_sse(
            "response.completed",
            {
                "type": "response.completed",
                "response": {
                    "id": "resp_amkr",
                    "model": requested_model_id,
                    "usage": _responses_usage(usage),
                },
            },
        )
    except Exception as exc:
        failed = True
        payload = {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key_name, "upstream": upstream, "status_code": response.status_code, "content_type": response.headers.get("content-type"), "error_type": exc.__class__.__name__, "error": str(exc), "chunks": chunk_count, "bytes": byte_count, "duration_ms": _elapsed_ms(started)}
        LOGGER.warning("upstream responses stream error %s", json.dumps(payload, ensure_ascii=False))
        _debug_report("upstream-responses-stream-error", payload)
    finally:
        await metrics.record(
            model_id,
            key_name,
            response.status_code,
            usage,
            failed=failed,
            duration_ms=_elapsed_ms(started),
            first_token_ms=first_token_ms,
            requested_model_id=requested_model_id,
            caller_type=caller_type,
        )
        if failed or response.status_code in RETRYABLE_STATUS_CODES:
            await key_pool.mark_failure(model_id, key_name, response.status_code, _retry_after_seconds(response))
        elif response.status_code < 400:
            await key_pool.mark_success(model_id, key_name)
        await key_pool.release_key(model_id, key_name)
        _debug_report("upstream-responses-stream-close", {"status_code": response.status_code, "chunks": chunk_count, "bytes": byte_count, "first_token_ms": first_token_ms, "usage": usage})
        await response.aclose()












































#region debug-point upstream-request-failed
def _debug_report(event: str, payload: dict[str, Any]) -> None:
    env_path = Path.cwd() / ".dbg" / "upstream-request-failed.env"
    try:
        values = dict(line.strip().split("=", 1) for line in env_path.read_text(encoding="utf-8").splitlines() if "=" in line)
        url = values.get("DEBUG_SERVER_URL")
        if not url:
            return
        body = json.dumps({"event": event, "runId": "pre", "payload": payload}, ensure_ascii=False).encode("utf-8")
        request = UrlRequest(url, data=body, headers={"Content-Type": "application/json"}, method="POST")
        with urlopen(request, timeout=0.2):
            pass
    except Exception:
        return
#endregion debug-point upstream-request-failed


def _json_error_response_from_content(response: httpx.Response, content: bytes, anthropic: bool = False) -> JSONResponse:
    data = _json_bytes(content)
    if data is None:
        message = content.decode("utf-8", errors="replace") or f"上游返回 HTTP {response.status_code}，且响应体为空"
        if anthropic:
            data = {"type": "error", "error": {"type": "api_error", "message": message}}
        else:
            data = {"error": {"message": message}}
    elif anthropic:
        data = _anthropic_error_response(data)
    return JSONResponse(data, status_code=response.status_code)


def _anthropic_error_response(data: Any) -> dict[str, Any]:
    if isinstance(data, dict) and data.get("type") == "error" and isinstance(data.get("error"), dict):
        return data
    message = "上游请求失败"
    if isinstance(data, dict):
        error = data.get("error")
        if isinstance(error, dict):
            message = str(error.get("message") or message)
        elif isinstance(error, str):
            message = error
        elif data.get("message"):
            message = str(data["message"])
    return {"type": "error", "error": {"type": "api_error", "message": message}}


def _json_bytes(content: bytes) -> Any:
    if not content:
        return None
    try:
        import json

        return json.loads(content.decode("utf-8"))
    except ValueError:
        return None
