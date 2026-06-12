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


LOGGER = logging.getLogger("auto_model_key_router.app")


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
    app.state.http_client = httpx.AsyncClient(
        timeout=config.request_timeout,
        limits=httpx.Limits(max_connections=100, max_keepalive_connections=30, keepalive_expiry=30),
    )
    app.state.health_probe_task = None

    @app.head("/", include_in_schema=False)
    async def root_probe() -> Response:
        return Response(status_code=204)

    @app.get("/health")
    async def health() -> dict[str, Any]:
        await _reload_config_if_changed(app.state)
        local_api_key = app.state.config.local_api_key
        return {
            "status": "ok",
            "models": app.state.key_pool.public_model_ids,
            "config_path": app.state.config_path,
            "local_auth_enabled": bool(local_api_key),
            "local_api_key_fingerprint": _key_fingerprint(local_api_key),
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
        if not _is_authorized(request, app.state.config.local_api_key):
            return JSONResponse({"error": {"message": "本地 API key 验证失败"}}, status_code=401)
        return await app.state.metrics.snapshot()

    @app.api_route("/v1/{path:path}", methods=["GET", "POST", "PUT", "PATCH", "DELETE"])
    async def proxy(path: str, request: Request) -> Response:
        await _reload_config_if_changed(app.state)
        if not _is_authorized(request, app.state.config.local_api_key):
            return JSONResponse({"error": {"message": "本地 API key 验证失败"}}, status_code=401)

        body = await request.body()
        payload = _json_body(body)
        requested_model_id = _resolve_model_id(path, payload)
        _debug_report("proxy-entry", {"path": path, "method": request.method, "requested_model_id": requested_model_id, "stream": _is_stream_request(payload), "body_bytes": len(body)})
        if requested_model_id is None:
            return JSONResponse({"error": {"message": "请求体中缺少 model 字段"}}, status_code=400)
        requested_model_name, requested_key_name = _split_requested_model_key(requested_model_id)
        model_id, requested_key_name = app.state.key_pool.resolve_route(requested_model_name, requested_key_name)
        upstream_body = _upstream_body(body, payload, model_id, app.state.config, stream=_is_stream_request(payload))

        excluded: set[str] = set()
        last_error: JSONResponse | None = None
        key_count = app.state.key_pool.key_count(model_id)
        if key_count == 0:
            return JSONResponse({"error": {"message": f"未配置模型: {model_id}"}}, status_code=404)

        only_first = app.state.key_pool.routing_mode(model_id) == "only_first"
        attempts = app.state.config.max_retries + 1 if requested_key_name or only_first or key_count == 1 else key_count

        for attempt in range(attempts):
            try:
                if requested_key_name:
                    key = app.state.key_pool.key_by_name(model_id, requested_key_name)
                    await app.state.key_pool.acquire_key(model_id, key.name)
                else:
                    key = await app.state.key_pool.next_key(model_id, excluded)
            except KeyError:
                return JSONResponse({"error": {"message": f"未配置模型: {model_id}"}}, status_code=404)
            except RuntimeError as exc:
                return JSONResponse({"error": {"message": str(exc)}}, status_code=503)

            release_key = True
            try:
                if key_count > 1 and not requested_key_name and not only_first:
                    excluded.add(key.name)
                upstream = _join_url(key.base_url, f"/v1/{_upstream_path(path, payload)}")
                headers = _upstream_headers(request, key.api_key)
                _debug_report("upstream-attempt", {"upstream": upstream, "model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "stream": _is_stream_request(payload)})

                started = perf_counter()
                try:
                    response = await _send_upstream(
                        app.state.http_client,
                        request,
                        upstream,
                        headers,
                        upstream_body,
                        stream=_is_stream_request(payload),
                        timeout=_upstream_timeout(app.state.config, _is_stream_request(payload)),
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
                    )
                    await app.state.key_pool.mark_failure(model_id, key.name)
                    continue

                if response.status_code in {401, 403, 429, 500, 502, 503, 504} and attempt + 1 < attempts:
                    content = await response.aread()
                    duration_ms = _elapsed_ms(started)
                    _debug_report("upstream-retryable-response", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "status_code": response.status_code, "duration_ms": duration_ms, "content_preview": content[:500].decode("utf-8", errors="replace")})
                    last_error = _json_error_response_from_content(response, content)
                    await app.state.metrics.record(
                        model_id,
                        key.name,
                        response.status_code,
                        extract_usage(_json_bytes(content)),
                        retried=True,
                        failed=True,
                        duration_ms=duration_ms,
                        first_token_ms=duration_ms,
                        requested_model_id=requested_model_id,
                    )
                    await app.state.key_pool.mark_failure(model_id, key.name, response.status_code, _retry_after_seconds(response))
                    await _close_upstream_response(response)
                    continue

                if _is_stream_request(payload) and response.status_code >= 400:
                    content = await response.aread()
                    duration_ms = _elapsed_ms(started)
                    _debug_report("upstream-stream-error-response", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "status_code": response.status_code, "duration_ms": duration_ms, "content_type": response.headers.get("content-type"), "content_preview": content[:500].decode("utf-8", errors="replace")})
                    await response.aclose()
                    await app.state.metrics.record(
                        model_id,
                        key.name,
                        response.status_code,
                        extract_usage(_json_bytes(content)),
                        failed=True,
                        duration_ms=duration_ms,
                        first_token_ms=duration_ms,
                        requested_model_id=requested_model_id,
                    )
                    if response.status_code in {401, 403, 429, 500, 502, 503, 504}:
                        await app.state.key_pool.mark_failure(model_id, key.name, response.status_code, _retry_after_seconds(response))
                    return _json_error_response_from_content(response, content, anthropic=path == "messages")

                if _is_stream_request(payload):
                    _debug_report("upstream-stream-response", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "status_code": response.status_code, "duration_ms": duration_ms, "content_type": response.headers.get("content-type")})
                    stream = _stream_upstream(response, app.state.metrics, app.state.key_pool, model_id, key.name, requested_model_id, upstream, started)
                    media_type = response.headers.get("content-type")
                    response_headers = _response_headers(response)
                    if path == "messages":
                        stream = _stream_anthropic_messages(response, app.state.metrics, app.state.key_pool, model_id, key.name, requested_model_id, upstream, started)
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
                await app.state.metrics.record(
                    model_id,
                    key.name,
                    response.status_code,
                    extract_usage(_json_bytes(content)),
                    duration_ms=duration_ms,
                    first_token_ms=duration_ms,
                    requested_model_id=requested_model_id,
                )
                if response.status_code < 400:
                    await app.state.key_pool.mark_success(model_id, key.name)
                elif response.status_code in {401, 403, 429, 500, 502, 503, 504}:
                    await app.state.key_pool.mark_failure(model_id, key.name, response.status_code, _retry_after_seconds(response))

                if response.status_code >= 400:
                    return _json_error_response_from_content(response, content, anthropic=path == "messages")
                if path == "messages":
                    anthropic_response = _anthropic_message_response(_json_bytes(content), requested_model_id)
                    if anthropic_response is None:
                        return JSONResponse({"type": "error", "error": {"type": "api_error", "message": "上游返回了非 JSON 响应，无法转换为 Anthropic Messages 响应"}}, status_code=502)
                    return JSONResponse(anthropic_response, status_code=response.status_code, headers=_response_headers(response))
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

    return app


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


def _adapt_message_payload(payload: dict[str, Any]) -> dict[str, Any]:
    if "messages" in payload:
        return _normalize_chat_compat_parameters(_adapt_anthropic_messages_payload(payload))
    if "input" in payload:
        return _normalize_chat_compat_parameters(_adapt_responses_input_payload(payload))
    return _normalize_chat_compat_parameters(payload)


def _adapt_anthropic_messages_payload(payload: dict[str, Any]) -> dict[str, Any]:
    messages = payload.get("messages")
    if not isinstance(messages, list):
        return payload
    adapted = dict(payload)
    adapted_messages: list[dict[str, Any]] = []
    for message in messages:
        if isinstance(message, dict):
            adapted_messages.extend(_adapt_anthropic_message(message))
    system = adapted.pop("system", None)
    if system:
        adapted_messages = [{"role": "system", "content": _adapt_content(system)}, *adapted_messages]
    adapted["messages"] = adapted_messages
    tools = adapted.get("tools")
    if isinstance(tools, list):
        adapted["tools"] = [_adapt_anthropic_tool(tool) for tool in tools if isinstance(tool, dict)]
    tool_choice = adapted.get("tool_choice")
    if isinstance(tool_choice, dict):
        adapted["tool_choice"] = _adapt_anthropic_tool_choice(tool_choice)
        if "disable_parallel_tool_use" in tool_choice:
            adapted["parallel_tool_calls"] = not bool(tool_choice["disable_parallel_tool_use"])
    return adapted


def _adapt_responses_input_payload(payload: dict[str, Any]) -> dict[str, Any]:
    adapted = dict(payload)
    messages = _responses_input_to_messages(adapted.pop("input"))
    instructions = adapted.pop("instructions", None)
    if instructions:
        messages = [{"role": "system", "content": _adapt_content(instructions)}, *messages]
    adapted["messages"] = messages
    return adapted


def _normalize_chat_compat_parameters(payload: dict[str, Any]) -> dict[str, Any]:
    adapted = dict(payload)
    if "max_output_tokens" in adapted and "max_tokens" not in adapted:
        adapted["max_tokens"] = adapted.pop("max_output_tokens")
    else:
        adapted.pop("max_output_tokens", None)
    if "stop_sequences" in adapted and "stop" not in adapted:
        adapted["stop"] = adapted.pop("stop_sequences")
    else:
        adapted.pop("stop_sequences", None)
    for key in ("anthropic_version", "metadata", "reasoning", "text", "truncation", "previous_response_id"):
        adapted.pop(key, None)
    return adapted


def _responses_input_to_messages(value: Any) -> list[dict[str, Any]]:
    if isinstance(value, str):
        return [{"role": "user", "content": value}]
    if not isinstance(value, list):
        return [{"role": "user", "content": _adapt_content(value)}]
    messages: list[dict[str, Any]] = []
    for item in value:
        if isinstance(item, dict):
            role = str(item.get("role") or "user")
            content = item.get("content", item.get("text", ""))
            if item.get("type") == "message" or "role" in item or "content" in item:
                messages.append({"role": role, "content": _adapt_content(content)})
            else:
                messages.append({"role": "user", "content": _adapt_content(item)})
        else:
            messages.append({"role": "user", "content": _adapt_content(item)})
    return messages


def _adapt_anthropic_message(message: dict[str, Any]) -> list[dict[str, Any]]:
    role = str(message.get("role") or "user")
    content = message.get("content", "")
    if not isinstance(content, list):
        return [{**message, "role": role, "content": _adapt_content(content)}]

    if role == "assistant":
        text_parts: list[Any] = []
        tool_calls: list[dict[str, Any]] = []
        for index, part in enumerate(content):
            if isinstance(part, dict) and part.get("type") == "tool_use":
                tool_input = part.get("input")
                tool_calls.append(
                    {
                        "id": str(part.get("id") or f"toolu_amkr_{index}"),
                        "type": "function",
                        "function": {
                            "name": str(part.get("name") or ""),
                            "arguments": json.dumps(tool_input if tool_input is not None else {}, ensure_ascii=False, separators=(",", ":")),
                        },
                    }
                )
            else:
                text_parts.append(part)
        adapted = {key: value for key, value in message.items() if key != "content"}
        adapted["role"] = role
        adapted["content"] = _adapt_content(text_parts) if text_parts else None
        if tool_calls:
            adapted["tool_calls"] = tool_calls
        return [adapted]

    if role == "user" and any(isinstance(part, dict) and part.get("type") == "tool_result" for part in content):
        adapted_messages: list[dict[str, Any]] = []
        pending_parts: list[Any] = []
        for part in content:
            if not isinstance(part, dict) or part.get("type") != "tool_result":
                pending_parts.append(part)
                continue
            if pending_parts:
                adapted_messages.append({"role": "user", "content": _adapt_content(pending_parts)})
                pending_parts = []
            adapted_messages.append(
                {
                    "role": "tool",
                    "tool_call_id": str(part.get("tool_use_id") or ""),
                    "content": _tool_result_text(part.get("content", "")),
                }
            )
        if pending_parts:
            adapted_messages.append({"role": "user", "content": _adapt_content(pending_parts)})
        return adapted_messages

    adapted = dict(message)
    adapted["role"] = role
    adapted["content"] = _adapt_content(content)
    return [adapted]


def _adapt_anthropic_tool(tool: dict[str, Any]) -> dict[str, Any]:
    if tool.get("type") == "function" and isinstance(tool.get("function"), dict):
        return tool
    function: dict[str, Any] = {"name": str(tool.get("name") or "")}
    if tool.get("description") is not None:
        function["description"] = str(tool["description"])
    parameters = tool.get("input_schema", tool.get("parameters"))
    function["parameters"] = parameters if isinstance(parameters, dict) else {"type": "object", "properties": {}}
    return {"type": "function", "function": function}


def _adapt_anthropic_tool_choice(tool_choice: dict[str, Any]) -> Any:
    choice_type = tool_choice.get("type")
    if choice_type == "any":
        return "required"
    if choice_type in {"auto", "none"}:
        return choice_type
    if choice_type == "tool":
        return {"type": "function", "function": {"name": str(tool_choice.get("name") or "")}}
    return tool_choice


def _tool_result_text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for part in content:
            if isinstance(part, dict) and part.get("text") is not None:
                parts.append(str(part["text"]))
            elif isinstance(part, str):
                parts.append(part)
            else:
                parts.append(json.dumps(part, ensure_ascii=False, separators=(",", ":")))
        return "".join(parts)
    if content is None:
        return ""
    if isinstance(content, dict) and content.get("text") is not None:
        return str(content["text"])
    return json.dumps(content, ensure_ascii=False, separators=(",", ":"))


def _adapt_content(content: Any) -> Any:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return [_adapt_content_part(part) for part in content]
    if isinstance(content, dict):
        return [_adapt_content_part(content)]
    return str(content)


def _adapt_content_part(part: Any) -> Any:
    if not isinstance(part, dict):
        return {"type": "text", "text": str(part)}
    part_type = part.get("type")
    if part_type in {"text", "image_url"}:
        return part
    if part_type in {"input_text", "output_text"}:
        return {"type": "text", "text": str(part.get("text", ""))}
    if part_type == "image":
        source = part.get("source")
        if isinstance(source, dict) and source.get("type") == "base64":
            media_type = source.get("media_type") or "image/png"
            data = source.get("data") or ""
            return {"type": "image_url", "image_url": {"url": f"data:{media_type};base64,{data}"}}
    if part_type == "input_image":
        image_url = part.get("image_url") or part.get("file_id") or ""
        return {"type": "image_url", "image_url": {"url": str(image_url)}}
    text = part.get("text")
    if text is not None:
        return {"type": "text", "text": str(text)}
    return {"type": "text", "text": json.dumps(part, ensure_ascii=False, separators=(",", ":"))}


def _join_url(base_url: str, path: str) -> str:
    return f"{base_url.rstrip('/')}/{path.lstrip('/')}"


def _upstream_headers(request: Request, api_key: str) -> dict[str, str]:
    blocked = {"authorization", "host", "content-length", "destination-addr", "x-api-key", "anthropic-version", "anthropic-beta"}
    headers = {key: value for key, value in request.headers.items() if key.lower() not in blocked}
    headers["Authorization"] = f"Bearer {api_key}"
    return headers


def _is_authorized(request: Request, local_api_key: str) -> bool:
    if not local_api_key:
        return True
    authorization = request.headers.get("authorization", "")
    if authorization.lower().startswith("bearer "):
        return authorization[7:].strip() == local_api_key
    return request.headers.get("x-api-key") == local_api_key


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
            state.http_client = httpx.AsyncClient(
                timeout=config.request_timeout,
                limits=httpx.Limits(max_connections=100, max_keepalive_connections=30, keepalive_expiry=30),
            )
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


async def _stream_upstream(response: httpx.Response, metrics: MetricsStore, key_pool: KeyPool, model_id: str, key_name: str, requested_model_id: str, upstream: str, started: float):
    chunk_count = 0
    byte_count = 0
    buffer = ""
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
            yield chunk
    except Exception as exc:
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
        )
        if failed or response.status_code in {401, 403, 429, 500, 502, 503, 504}:
            await key_pool.mark_failure(model_id, key_name, response.status_code, _retry_after_seconds(response))
        elif response.status_code < 400:
            await key_pool.mark_success(model_id, key_name)
        await key_pool.release_key(model_id, key_name)
        _debug_report("upstream-stream-close", {"status_code": response.status_code, "chunks": chunk_count, "bytes": byte_count, "first_token_ms": first_token_ms, "usage": usage})
        await response.aclose()


async def _stream_anthropic_messages(response: httpx.Response, metrics: MetricsStore, key_pool: KeyPool, model_id: str, key_name: str, requested_model_id: str, upstream: str, started: float):
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
        )
        if failed or response.status_code in {401, 403, 429, 500, 502, 503, 504}:
            await key_pool.mark_failure(model_id, key_name, response.status_code, _retry_after_seconds(response))
        elif response.status_code < 400:
            await key_pool.mark_success(model_id, key_name)
        await key_pool.release_key(model_id, key_name)
        _debug_report("upstream-anthropic-stream-close", {"status_code": response.status_code, "chunks": chunk_count, "bytes": byte_count, "first_token_ms": first_token_ms, "usage": usage})
        await response.aclose()


def _anthropic_stream_events(buffer: str, chunk: bytes, stream_state: dict[str, Any]) -> tuple[str, list[bytes], dict[str, Any] | None]:
    buffer += chunk.decode("utf-8", errors="replace")
    events: list[bytes] = []
    usage = None
    while "\n" in buffer:
        line, buffer = buffer.split("\n", 1)
        line = line.strip()
        if not line.startswith("data:"):
            continue
        data = line[5:].strip()
        if not data or data == "[DONE]":
            continue
        payload = _json_text(data)
        if not isinstance(payload, dict):
            continue
        chunk_usage = extract_usage(payload)
        if chunk_usage is not None:
            usage = chunk_usage
        for choice in _openai_choices(payload):
            delta = choice.get("delta") if isinstance(choice.get("delta"), dict) else {}
            text = _delta_text(delta)
            if text:
                text_index = stream_state.get("text_index")
                if not isinstance(text_index, int) or stream_state["text_stopped"]:
                    text_index = int(stream_state["next_content_index"])
                    stream_state["next_content_index"] = text_index + 1
                    stream_state["text_index"] = text_index
                    stream_state["text_stopped"] = False
                    events.append(_anthropic_sse("content_block_start", {"type": "content_block_start", "index": text_index, "content_block": {"type": "text", "text": ""}}))
                events.append(_anthropic_sse("content_block_delta", {"type": "content_block_delta", "index": text_index, "delta": {"type": "text_delta", "text": text}}))
            tool_calls = delta.get("tool_calls")
            if isinstance(tool_calls, list):
                for fallback_index, tool_call in enumerate(tool_calls):
                    if isinstance(tool_call, dict):
                        events.extend(_anthropic_stream_tool_events(stream_state, tool_call, fallback_index))
            function_call = delta.get("function_call")
            if isinstance(function_call, dict):
                events.extend(_anthropic_stream_tool_events(stream_state, {"index": 0, "function": function_call}, 0))
            if choice.get("finish_reason") is not None:
                stream_state["stop_reason"] = _anthropic_stop_reason(choice.get("finish_reason"))
    return buffer, events, usage


def _anthropic_stream_tool_events(stream_state: dict[str, Any], tool_call: dict[str, Any], fallback_index: int) -> list[bytes]:
    events: list[bytes] = []
    text_index = stream_state.get("text_index")
    if isinstance(text_index, int) and not stream_state["text_stopped"]:
        events.append(_anthropic_sse("content_block_stop", {"type": "content_block_stop", "index": text_index}))
        stream_state["text_stopped"] = True

    try:
        tool_index = int(tool_call.get("index", fallback_index))
    except (TypeError, ValueError):
        tool_index = fallback_index
    accumulated = stream_state["tool_calls"].setdefault(
        tool_index,
        {"id": "", "name": "", "arguments": "", "emitted_arguments": 0, "content_index": None, "started": False, "stopped": False},
    )
    if tool_call.get("id") and not accumulated["id"]:
        accumulated["id"] = str(tool_call["id"])
    function = tool_call.get("function")
    if isinstance(function, dict):
        if function.get("name") and not accumulated["name"]:
            accumulated["name"] = str(function["name"])
        arguments = function.get("arguments")
        if isinstance(arguments, str):
            accumulated["arguments"] += arguments
        elif arguments is not None:
            accumulated["arguments"] += json.dumps(arguments, ensure_ascii=False, separators=(",", ":"))

    if stream_state["active_tool_index"] is None and accumulated["id"] and accumulated["name"]:
        stream_state["active_tool_index"] = tool_index
        events.extend(_start_anthropic_stream_tool(stream_state, tool_index))
    if stream_state["active_tool_index"] == tool_index:
        events.extend(_anthropic_stream_tool_argument_events(accumulated))
    return events


def _start_anthropic_stream_tool(stream_state: dict[str, Any], tool_index: int) -> list[bytes]:
    tool_call = stream_state["tool_calls"][tool_index]
    if tool_call["started"]:
        return []
    content_index = int(stream_state["next_content_index"])
    stream_state["next_content_index"] = content_index + 1
    tool_call["content_index"] = content_index
    tool_call["started"] = True
    return [
        _anthropic_sse(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": content_index,
                "content_block": {
                    "type": "tool_use",
                    "id": str(tool_call["id"] or f"call_amkr_{tool_index}"),
                    "name": str(tool_call["name"] or ""),
                    "input": {},
                },
            },
        )
    ]


def _anthropic_stream_tool_argument_events(tool_call: dict[str, Any]) -> list[bytes]:
    arguments = str(tool_call["arguments"])
    emitted_arguments = int(tool_call["emitted_arguments"])
    if not tool_call["started"] or emitted_arguments >= len(arguments):
        return []
    tool_call["emitted_arguments"] = len(arguments)
    return [
        _anthropic_sse(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": int(tool_call["content_index"]),
                "delta": {"type": "input_json_delta", "partial_json": arguments[emitted_arguments:]},
            },
        )
    ]


def _finish_anthropic_stream_tools(stream_state: dict[str, Any]) -> list[bytes]:
    events: list[bytes] = []
    active_tool_index = stream_state.get("active_tool_index")
    ordered_indexes = []
    if isinstance(active_tool_index, int):
        ordered_indexes.append(active_tool_index)
    ordered_indexes.extend(index for index in sorted(stream_state["tool_calls"]) if index != active_tool_index)

    for tool_index in ordered_indexes:
        tool_call = stream_state["tool_calls"][tool_index]
        if tool_call["stopped"]:
            continue
        if not tool_call["started"]:
            if not tool_call["id"]:
                tool_call["id"] = f"call_amkr_{tool_index}"
            events.extend(_start_anthropic_stream_tool(stream_state, tool_index))
        events.extend(_anthropic_stream_tool_argument_events(tool_call))
        events.append(_anthropic_sse("content_block_stop", {"type": "content_block_stop", "index": int(tool_call["content_index"])}))
        tool_call["stopped"] = True
    stream_state["active_tool_index"] = None
    return events


def _anthropic_sse(event: str, payload: dict[str, Any]) -> bytes:
    return f"event: {event}\ndata: {json.dumps(payload, ensure_ascii=False, separators=(',', ':'))}\n\n".encode("utf-8")


async def _flush_stream_event() -> None:
    # Let the ASGI server flush each SSE event instead of coalescing a burst
    # from one upstream network chunk into a single downstream TCP write.
    await asyncio.sleep(0)


def _anthropic_stream_usage(usage: dict[str, Any] | None) -> dict[str, int]:
    return {"output_tokens": int((usage or {}).get("completion_tokens") or (usage or {}).get("output_tokens") or 0)}


def _anthropic_message_response(data: Any, requested_model_id: str) -> dict[str, Any] | None:
    if not isinstance(data, dict):
        return None
    if data.get("type") == "message" and isinstance(data.get("content"), list):
        return data
    choice = next(iter(_openai_choices(data)), None)
    if choice is None:
        return None
    message = choice.get("message") if isinstance(choice.get("message"), dict) else {}
    text = _message_text(message.get("content", ""))
    content: list[dict[str, Any]] = []
    if text:
        content.append({"type": "text", "text": text})
    content.extend(_anthropic_tool_use_blocks(message))
    if not content:
        content.append({"type": "text", "text": ""})
    usage = data.get("usage") if isinstance(data.get("usage"), dict) else {}
    return {
        "id": str(data.get("id") or "msg_amkr"),
        "type": "message",
        "role": "assistant",
        "model": requested_model_id,
        "content": content,
        "stop_reason": _anthropic_stop_reason(choice.get("finish_reason"), any(block.get("type") == "tool_use" for block in content)),
        "stop_sequence": None,
        "usage": {
            "input_tokens": int(usage.get("prompt_tokens") or usage.get("input_tokens") or 0),
            "output_tokens": int(usage.get("completion_tokens") or usage.get("output_tokens") or 0),
        },
    }


def _openai_choices(data: dict[str, Any]) -> list[dict[str, Any]]:
    choices = data.get("choices")
    if not isinstance(choices, list):
        return []
    return [choice for choice in choices if isinstance(choice, dict)]


def _delta_text(delta: dict[str, Any]) -> str:
    content = delta.get("content", "")
    return _message_text(content)


def _message_text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for part in content:
            if isinstance(part, dict) and part.get("text") is not None:
                parts.append(str(part["text"]))
            elif isinstance(part, str):
                parts.append(part)
        return "".join(parts)
    if content is None:
        return ""
    return str(content)


def _anthropic_tool_use_blocks(message: dict[str, Any]) -> list[dict[str, Any]]:
    tool_calls = message.get("tool_calls")
    if not isinstance(tool_calls, list):
        function_call = message.get("function_call")
        tool_calls = [{"id": "call_amkr_0", "function": function_call}] if isinstance(function_call, dict) else []
    blocks: list[dict[str, Any]] = []
    for index, tool_call in enumerate(tool_calls):
        if not isinstance(tool_call, dict):
            continue
        function = tool_call.get("function")
        if not isinstance(function, dict):
            continue
        arguments = function.get("arguments", {})
        if isinstance(arguments, str):
            parsed_arguments = _json_text(arguments)
            arguments = parsed_arguments if parsed_arguments is not None else {}
        blocks.append(
            {
                "type": "tool_use",
                "id": str(tool_call.get("id") or f"call_amkr_{index}"),
                "name": str(function.get("name") or ""),
                "input": arguments if arguments is not None else {},
            }
        )
    return blocks


def _anthropic_stop_reason(reason: Any, has_tool_use: bool = False) -> str:
    if reason == "length":
        return "max_tokens"
    if has_tool_use or reason in {"tool_calls", "function_call"}:
        return "tool_use"
    return "end_turn"


def _stream_usage(buffer: str, chunk: bytes) -> tuple[str, dict[str, Any] | None]:
    buffer += chunk.decode("utf-8", errors="replace")
    usage = None
    while "\n" in buffer:
        line, buffer = buffer.split("\n", 1)
        line = line.strip()
        if not line.startswith("data:"):
            continue
        data = line[5:].strip()
        if not data or data == "[DONE]":
            continue
        chunk_usage = extract_usage(_json_text(data))
        if chunk_usage is not None:
            usage = chunk_usage
    return buffer, usage


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


async def _close_upstream_response(response: httpx.Response) -> None:
    await response.aclose()


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


def _json_text(content: str) -> Any:
    try:
        return json.loads(content)
    except ValueError:
        return None
