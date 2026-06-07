from __future__ import annotations

import hashlib
import json
from pathlib import Path
from time import perf_counter
from typing import Any
from urllib.request import Request as UrlRequest, urlopen

import httpx
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse

from .config import RouterConfig
from .key_pool import KeyPool
from .metrics import MetricsStore, extract_usage


def create_app(config: RouterConfig, config_path: str | Path | None = None) -> FastAPI:
    app = FastAPI(title="Auto Model Key Router", version="0.1.0")
    app.state.config = config
    app.state.config_path = str(Path(config_path).resolve()) if config_path is not None else ""
    app.state.local_api_key = config.local_api_key
    app.state.local_api_key_mtime = _config_mtime(app.state.config_path)
    app.state.key_pool = KeyPool(config)
    app.state.metrics = MetricsStore(config.metrics_db_path)
    app.state.http_client = httpx.AsyncClient(
        timeout=config.request_timeout,
        limits=httpx.Limits(max_connections=100, max_keepalive_connections=30, keepalive_expiry=30),
    )

    @app.on_event("shutdown")
    async def shutdown() -> None:
        await app.state.http_client.aclose()
        await app.state.metrics.close()

    @app.get("/health")
    async def health() -> dict[str, Any]:
        local_api_key = _current_local_api_key(app.state)
        return {
            "status": "ok",
            "models": app.state.key_pool.public_model_ids,
            "config_path": app.state.config_path,
            "local_auth_enabled": bool(local_api_key),
            "local_api_key_fingerprint": _key_fingerprint(local_api_key),
        }

    @app.get("/v1/models")
    async def models() -> dict[str, Any]:
        return {
            "object": "list",
            "data": [
                {"id": model_id, "object": "model", "owned_by": "auto-model-key-router"}
                for model_id in app.state.key_pool.public_model_ids
            ],
        }

    @app.get("/metrics")
    async def metrics() -> dict[str, Any]:
        return await app.state.metrics.snapshot()

    @app.api_route("/v1/{path:path}", methods=["GET", "POST", "PUT", "PATCH", "DELETE"])
    async def proxy(path: str, request: Request) -> Response:
        if not _is_authorized(request, _current_local_api_key(app.state)):
            return JSONResponse({"error": {"message": "本地 API key 验证失败"}}, status_code=401)

        body = await request.body()
        payload = _json_body(body)
        requested_model_id = _resolve_model_id(path, payload)
        _debug_report("proxy-entry", {"path": path, "method": request.method, "requested_model_id": requested_model_id, "stream": _is_stream_request(payload), "body_bytes": len(body)})
        if requested_model_id is None:
            return JSONResponse({"error": {"message": "请求体中缺少 model 字段"}}, status_code=400)
        model_id = app.state.key_pool.resolve_model_id(requested_model_id)
        upstream_body = _upstream_body(body, payload, model_id, stream=_is_stream_request(payload))

        excluded: set[str] = set()
        last_error: JSONResponse | None = None
        key_count = app.state.key_pool.key_count(model_id)
        if key_count == 0:
            return JSONResponse({"error": {"message": f"未配置模型: {model_id}"}}, status_code=404)

        attempts = min(app.state.config.max_retries + 1, key_count)

        for _ in range(attempts):
            try:
                key = await app.state.key_pool.next_key(model_id, excluded)
            except KeyError:
                return JSONResponse({"error": {"message": f"未配置模型: {model_id}"}}, status_code=404)
            except RuntimeError as exc:
                return JSONResponse({"error": {"message": str(exc)}}, status_code=503)

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
                continue

            if response.status_code in {401, 403, 429, 500, 502, 503, 504} and len(excluded) < attempts:
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
                await _close_upstream_response(response)
                continue

            if _is_stream_request(payload):
                _debug_report("upstream-stream-response", {"model_id": model_id, "requested_model_id": requested_model_id, "key_name": key.name, "status_code": response.status_code, "duration_ms": duration_ms, "content_type": response.headers.get("content-type")})
                return StreamingResponse(
                    _stream_upstream(response, app.state.metrics, model_id, key.name, requested_model_id, started),
                    status_code=response.status_code,
                    headers=_response_headers(response),
                    media_type=response.headers.get("content-type"),
                )

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

            return Response(
                content=content,
                status_code=response.status_code,
                headers=_response_headers(response),
                media_type=response.headers.get("content-type"),
            )

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


def _is_stream_request(payload: dict[str, Any]) -> bool:
    return payload.get("stream") is True


def _upstream_body(body: bytes, payload: dict[str, Any], model_id: str, stream: bool = False) -> bytes:
    if not payload or "model" not in payload:
        return body
    upstream_payload = dict(payload)
    upstream_payload["model"] = model_id
    upstream_payload = _adapt_message_payload(upstream_payload)
    if stream:
        stream_options = upstream_payload.get("stream_options")
        if not isinstance(stream_options, dict):
            stream_options = {}
        stream_options["include_usage"] = True
        upstream_payload["stream_options"] = stream_options
    return json.dumps(upstream_payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


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
    adapted_messages = [_adapt_message(message) for message in messages if isinstance(message, dict)]
    system = adapted.pop("system", None)
    if system:
        adapted_messages = [{"role": "system", "content": _adapt_content(system)}, *adapted_messages]
    adapted["messages"] = adapted_messages
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


def _adapt_message(message: dict[str, Any]) -> dict[str, Any]:
    adapted = dict(message)
    adapted["content"] = _adapt_content(adapted.get("content", ""))
    return adapted


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
    blocked = {"authorization", "host", "content-length"}
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


def _current_local_api_key(state: Any) -> str:
    config_path = getattr(state, "config_path", "")
    mtime = _config_mtime(config_path)
    if mtime and mtime != getattr(state, "local_api_key_mtime", 0.0):
        try:
            raw = json.loads(Path(config_path).read_text(encoding="utf-8"))
            state.local_api_key = str(raw.get("local_api_key") or "")
            state.local_api_key_mtime = mtime
        except (OSError, ValueError):
            pass
    return str(getattr(state, "local_api_key", ""))


def _key_fingerprint(api_key: str) -> str:
    if not api_key:
        return ""
    return hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:12]


def _response_headers(response: httpx.Response) -> dict[str, str]:
    blocked = {"content-encoding", "content-length", "transfer-encoding", "connection"}
    return {key: value for key, value in response.headers.items() if key.lower() not in blocked}


async def _send_upstream(
    client: httpx.AsyncClient,
    request: Request,
    upstream: str,
    headers: dict[str, str],
    body: bytes,
    stream: bool = False,
) -> httpx.Response:
    response = await client.send(
        client.build_request(
            request.method,
            upstream,
            params=request.query_params,
            headers=headers,
            content=body,
        ),
        stream=True,
    )
    if not stream:
        await response.aread()
    return response


async def _stream_upstream(response: httpx.Response, metrics: MetricsStore, model_id: str, key_name: str, requested_model_id: str, started: float):
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
        _debug_report("upstream-stream-error", {"status_code": response.status_code, "error_type": exc.__class__.__name__, "error": str(exc), "chunks": chunk_count, "bytes": byte_count})
        raise
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
        _debug_report("upstream-stream-close", {"status_code": response.status_code, "chunks": chunk_count, "bytes": byte_count, "first_token_ms": first_token_ms, "usage": usage})
        await response.aclose()


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


def _json_error_response_from_content(response: httpx.Response, content: bytes) -> JSONResponse:
    data = _json_bytes(content)
    if data is None:
        data = {"error": {"message": content.decode("utf-8", errors="replace")}}
    return JSONResponse(data, status_code=response.status_code)


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
