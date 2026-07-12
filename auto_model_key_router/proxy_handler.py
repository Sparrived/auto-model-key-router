from __future__ import annotations

import asyncio
import hashlib
import json
import logging
from dataclasses import dataclass, replace
from time import perf_counter
from typing import Any

import httpx
from fastapi import Request
from fastapi.responses import JSONResponse, Response, StreamingResponse

from .config import UNIFIED_MODEL_ID, KeyConfig
from .key_pool import KeyPool
from .metrics import MetricsStore, extract_usage
from .protocol_compat import (
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
from .proxy_support import (
    UNSUPPORTED_ENDPOINT_STATUS_CODES,
    _authorization_mode,
    _debug_report,
    _is_stream_request,
    _is_tool_error,
    _join_url,
    _json_body,
    _json_bytes,
    _json_error_response_from_content,
    _log_model_not_configured,
    _resolve_model_id,
    _response_headers,
    _send_upstream,
    _split_requested_model_key,
    _upstream_body,
    _upstream_body_with_filtered_tools,
    _upstream_headers,
    _upstream_path,
    _upstream_timeout,
    test_native_messages_support,
    test_native_responses_support,
)
from .runtime import RuntimeResources
from .routing import RetryPolicy
from .streaming import (
    RETRYABLE_STATUS_CODES,
    StreamLifecycle,
    iter_stream_bytes,
    retry_after_seconds,
)


LOGGER = logging.getLogger("auto_model_key_router.app")


@dataclass(frozen=True)
class ProxyRequestContext:
    path: str
    request: Request
    runtime: RuntimeResources
    visitor_only: bool
    caller_type: str
    payload: dict[str, Any]
    is_stream: bool
    requested_model_id: str
    requested_model_name: str
    requested_key_name: str | None
    model_id: str
    original_body: bytes  # 原始请求体，用于原生模式
    key_count: int
    only_first: bool
    attempts: int
    cache_affinity_key: str | None = None
    use_native: bool = False  # 是否使用原生 Anthropic 格式


@dataclass(frozen=True)
class AttemptOutcome:
    response: Response | None = None
    retry_error: JSONResponse | None = None
    stream_owns_key: bool = False


async def handle_proxy_request(
    path: str, request: Request, runtime: RuntimeResources
) -> Response:
    prepared = await _prepare_proxy_request(path, request, runtime)
    if isinstance(prepared, Response):
        return prepared
    response = await _handle_single_target(prepared)
    if prepared.requested_model_name != UNIFIED_MODEL_ID or response.status_code not in RETRYABLE_STATUS_CODES:
        return response
    plan = runtime.key_pool.resolve_unified_plan(
        "image" if path in ("images/generations", "images/edits") else "default",
        prepared.requested_key_name,
    )
    if plan.fallback is None:
        return response
    fallback_key_count = runtime.key_pool.key_count(plan.fallback.model)
    if fallback_key_count == 0:
        return response
    fallback_context = replace(
        prepared,
        model_id=plan.fallback.model,
        requested_key_name=plan.fallback.key,
        key_count=fallback_key_count,
        only_first=runtime.key_pool.routing_mode(plan.fallback.model) == "only_first",
        attempts=RetryPolicy(runtime.config.max_retries).attempts(
            key_count=fallback_key_count,
            requested_key_name=plan.fallback.key,
            only_first=runtime.key_pool.routing_mode(plan.fallback.model) == "only_first",
        ),
        cache_affinity_key=_cache_affinity_key(path, prepared.payload, plan.fallback.model),
    )
    response = await _handle_single_target(fallback_context)
    if response.status_code < 400:
        response.headers["X-AMKR-Fallback"] = "true"
    return response


async def _handle_single_target(context: ProxyRequestContext) -> Response:
    runtime = context.runtime
    excluded: set[str] = set()
    last_error: JSONResponse | None = None

    for attempt in range(context.attempts):
        selected = await _select_key(context, excluded)
        if isinstance(selected, Response):
            return selected
        key = selected
        release_key = True
        try:
            if (
                context.key_count > 1
                and not context.requested_key_name
                and not context.only_first
            ):
                excluded.add(key.name)
            outcome = await _execute_attempt(context, key, attempt)
            if outcome.retry_error is not None:
                last_error = outcome.retry_error
                continue
            release_key = not outcome.stream_owns_key
            if outcome.response is None:
                raise RuntimeError("proxy attempt completed without a response")
            return outcome.response
        finally:
            if release_key:
                await runtime.key_pool.release_key(context.model_id, key.name)

    return last_error or JSONResponse(
        {"error": {"message": "没有可用 key"}}, status_code=503
    )


async def _prepare_proxy_request(
    path: str, request: Request, runtime: RuntimeResources
) -> ProxyRequestContext | Response:
    authorization_mode = _authorization_mode(request, runtime.config.local_api_key)
    if authorization_mode is None:
        return JSONResponse(
            {"error": {"message": "本地 API key 验证失败"}}, status_code=401
        )
    visitor_only = authorization_mode == "visitor"
    caller_type = "visitor" if visitor_only else "local"
    body = await request.body()
    payload = _json_body(body)
    is_stream = _is_stream_request(payload)
    requested_model_id = _resolve_model_id(path, payload)
    _debug_report(
        "proxy-entry",
        {
            "path": path,
            "method": request.method,
            "requested_model_id": requested_model_id,
            "stream": is_stream,
            "body_bytes": len(body),
        },
    )
    if requested_model_id is None:
        return JSONResponse(
            {"error": {"message": "请求体中缺少 model 字段"}}, status_code=400
        )

    requested_model_name, requested_key_name = _split_requested_model_key(
        requested_model_id
    )
    if visitor_only and requested_model_name == UNIFIED_MODEL_ID:
        return JSONResponse(
            {"error": {"message": f"访客 key 无权访问模型: {UNIFIED_MODEL_ID}"}},
            status_code=403,
        )
    if visitor_only:
        model_id = runtime.key_pool.resolve_visitor_model_id(requested_model_name)
        if model_id is None:
            return JSONResponse(
                {
                    "error": {
                        "message": f"访客 key 无权访问模型: {requested_model_name}"
                    }
                },
                status_code=403,
            )
    else:
        model_id, requested_key_name = runtime.key_pool.resolve_route(
            requested_model_name, requested_key_name, path=path
        )
    configured_key_count = runtime.key_pool.key_count(model_id)
    key_count = (
        runtime.key_pool.visitor_key_count(model_id)
        if visitor_only
        else configured_key_count
    )
    if configured_key_count == 0:
        _log_model_not_configured(
            path, requested_model_id, model_id, "no_configured_keys"
        )
        return JSONResponse(
            {
                "error": {
                    "message": (
                        f"模型 {requested_model_id} 未配置；"
                        "请先在 AMKR 的模型设置中配置该模型"
                    )
                }
            },
            status_code=404,
        )
    if key_count == 0:
        return JSONResponse(
            {"error": {"message": f"访客 key 无权访问模型: {requested_model_name}"}},
            status_code=403,
        )
    if path == "messages/count_tokens":
        return JSONResponse({"input_tokens": _estimate_anthropic_input_tokens(payload)})

    only_first = runtime.key_pool.routing_mode(model_id) == "only_first"
    attempts = RetryPolicy(runtime.config.max_retries).attempts(
        key_count=key_count,
        requested_key_name=requested_key_name,
        only_first=only_first,
    )
    # 判断是否使用原生 Anthropic 格式
    use_native = (
        path == "messages"
        and runtime.config.native_first_for_model(model_id)
    )
    return ProxyRequestContext(
        path=path,
        request=request,
        runtime=runtime,
        visitor_only=visitor_only,
        caller_type=caller_type,
        payload=payload,
        is_stream=is_stream,
        requested_model_id=requested_model_id,
        requested_model_name=requested_model_name,
        requested_key_name=requested_key_name,
        model_id=model_id,
        original_body=body,
        key_count=key_count,
        only_first=only_first,
        attempts=attempts,
        cache_affinity_key=_cache_affinity_key(path, payload, model_id),
        use_native=use_native,
    )


def _cache_affinity_key(
    path: str, payload: dict[str, Any], model_id: str
) -> str | None:
    if path != "messages" or not isinstance(payload, dict):
        return None
    prompt_cache_key = payload.get("prompt_cache_key")
    if isinstance(prompt_cache_key, str) and prompt_cache_key.strip():
        return f"prompt_cache_key:{prompt_cache_key.strip()}"

    basis = {
        "path": path,
        "model": model_id,
        "system": payload.get("system"),
        "tools": payload.get("tools"),
        "tool_choice": payload.get("tool_choice"),
        "messages": _cache_affinity_messages(payload.get("messages")),
    }
    encoded = json.dumps(
        basis, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode("utf-8")
    return f"messages:{hashlib.sha256(encoded).hexdigest()}"


def _cache_affinity_messages(messages: Any) -> list[dict[str, Any]]:
    if not isinstance(messages, list):
        return []
    affinity_messages: list[dict[str, Any]] = []
    for message in messages:
        if not isinstance(message, dict):
            continue
        affinity_messages.append(
            {
                "role": message.get("role"),
                "content": message.get("content"),
            }
        )
    return affinity_messages


async def _select_key(
    context: ProxyRequestContext, excluded: set[str]
) -> KeyConfig | Response:
    try:
        if context.requested_key_name:
            key = context.runtime.key_pool.key_by_name(
                context.model_id,
                context.requested_key_name,
                visitor_only=context.visitor_only,
            )
            await context.runtime.key_pool.acquire_key(context.model_id, key.name)
            return key
        return await context.runtime.key_pool.next_key(
            context.model_id,
            excluded,
            visitor_only=context.visitor_only,
            affinity_key=context.cache_affinity_key,
        )
    except KeyError:
        _log_model_not_configured(
            context.path,
            context.requested_model_id,
            context.model_id,
            "key_selection_failed",
        )
        return JSONResponse(
            {
                "error": {
                    "message": (
                        f"模型 {context.requested_model_id} 未配置；"
                        "请先在 AMKR 的模型设置中配置该模型"
                    )
                }
            },
            status_code=404,
        )
    except RuntimeError as exc:
        if context.visitor_only and context.requested_key_name:
            return JSONResponse(
                {
                    "error": {
                        "message": (
                            "访客 key 无权访问模型 key: "
                            f"{context.requested_model_name}[{context.requested_key_name}]"
                        )
                    }
                },
                status_code=403,
            )
        return JSONResponse({"error": {"message": str(exc)}}, status_code=503)


async def _execute_attempt(
    context: ProxyRequestContext, key: KeyConfig, attempt: int
) -> AttemptOutcome:
    runtime = context.runtime
    upstream_routes = runtime.config.upstream_routes_for_base_url(key.base_url)
    upstream_model_id = key.upstream_model or context.model_id

    custom_native_route = (
        (context.path == "messages" and "anthropic" in upstream_routes)
        or (context.path == "responses" and "responses" in upstream_routes)
    )
    use_native = context.use_native or custom_native_route
    native_route_path = _upstream_path(
        context.path,
        context.payload,
        native=True,
        upstream_routes=upstream_routes,
    )
    if context.path == "messages" and use_native:
        native_support = runtime.key_pool.supports_native_endpoint(
            key.base_url, native_route_path
        )
        if native_support is None:
            native_support, native_reason = await test_native_messages_support(
                runtime.http_client,
                key.base_url,
                key.api_key,
                upstream_model_id,
                native_route_path,
            )
            await runtime.key_pool.update_native_endpoint(
                key.base_url, native_support, native_route_path, native_reason
            )
        use_native = native_support
    elif context.path == "responses":
        native_support = runtime.key_pool.supports_native_endpoint(
            key.base_url, native_route_path
        )
        if native_support is None:
            native_support, native_reason = await test_native_responses_support(
                runtime.http_client,
                key.base_url,
                key.api_key,
                upstream_model_id,
                native_route_path,
            )
            await runtime.key_pool.update_native_endpoint(
                key.base_url, native_support, native_route_path, native_reason
            )
        use_native = native_support

    upstream_path = _upstream_path(
        context.path,
        context.payload,
        native=use_native,
        upstream_routes=upstream_routes,
    )
    upstream = _join_url(
        key.base_url,
        upstream_path,
    )
    if use_native:
        upstream_body = _upstream_body(
            context.original_body, context.payload, upstream_model_id, native=True
        )
    else:
        upstream_body = _upstream_body(
            context.original_body,
            context.payload,
            upstream_model_id,
            runtime.config,
            stream=context.is_stream,
            reasoning_model_id=context.model_id,
        )

    headers = _upstream_headers(context.request, key.api_key)
    if use_native and context.path == "messages":
        anthropic_version = context.request.headers.get("anthropic-version", "2023-06-01")
        headers["anthropic-version"] = anthropic_version
        anthropic_beta = context.request.headers.get("anthropic-beta")
        if anthropic_beta:
            headers["anthropic-beta"] = anthropic_beta

    _debug_report(
        "upstream-attempt",
        {
            "upstream": upstream,
            "model_id": context.model_id,
            "upstream_model_id": upstream_model_id,
            "requested_model_id": context.requested_model_id,
            "key_name": key.name,
            "stream": context.is_stream,
            "native": use_native,
        },
    )
    started = perf_counter()
    first_byte_deadline = (
        asyncio.get_running_loop().time()
        + runtime.config.stream_first_byte_timeout
        if context.is_stream
        else None
    )
    try:
        response = await _send_upstream(
            runtime.http_client,
            context.request,
            upstream,
            headers,
            upstream_body,
            stream=context.is_stream,
            timeout=_upstream_timeout(runtime.config, context.is_stream),
            first_byte_deadline=first_byte_deadline,
        )
    except httpx.RequestError as exc:
        duration_ms = _elapsed_ms(started)
        _debug_report(
            "upstream-request-error",
            {
                "model_id": context.model_id,
                "requested_model_id": context.requested_model_id,
                "key_name": key.name,
                "error_type": exc.__class__.__name__,
                "error": str(exc),
                "duration_ms": duration_ms,
                "native": use_native,
            },
        )
        await runtime.metrics.record(
            context.model_id,
            key.name,
            None,
            retried=True,
            failed=True,
            duration_ms=duration_ms,
            first_token_ms=duration_ms,
            requested_model_id=context.requested_model_id,
            caller_type=context.caller_type,
        )
        await runtime.key_pool.mark_failure(context.model_id, key.name)
        await _broadcast_metrics(runtime)
        return AttemptOutcome(
            retry_error=JSONResponse(
                {"error": {"message": f"上游请求失败: {exc.__class__.__name__}"}},
                status_code=502,
            )
        )

    duration_ms = _elapsed_ms(started)
    
    if use_native and response.status_code in UNSUPPORTED_ENDPOINT_STATUS_CODES:
        content = await response.aread()
        await response.aclose()
        LOGGER.info(
            "native endpoint returned %d for %s/%s, falling back to chat/completions",
            response.status_code,
            key.base_url,
            upstream_path,
        )
        if context.path in {"messages", "responses"}:
            await runtime.key_pool.update_native_endpoint(
                key.base_url, False, native_route_path, "unsupported"
            )
        fallback_path = _upstream_path(
            context.path,
            context.payload,
            native=False,
            upstream_routes=upstream_routes,
        )
        fallback_upstream = _join_url(key.base_url, fallback_path)
        fallback_body = _upstream_body(
            context.original_body,
            context.payload,
            upstream_model_id,
            runtime.config,
            stream=context.is_stream,
            reasoning_model_id=context.model_id,
        )
        fallback_headers = _upstream_headers(context.request, key.api_key)
        _debug_report(
            "upstream-fallback",
            {
                "upstream": fallback_upstream,
                "model_id": context.model_id,
                "key_name": key.name,
                "original_status": response.status_code,
            },
        )
        fallback_started = perf_counter()
        first_byte_deadline = (
            asyncio.get_running_loop().time()
            + runtime.config.stream_first_byte_timeout
            if context.is_stream
            else None
        )
        try:
            fallback_response = await _send_upstream(
                runtime.http_client,
                context.request,
                fallback_upstream,
                fallback_headers,
                fallback_body,
                stream=context.is_stream,
                timeout=_upstream_timeout(runtime.config, context.is_stream),
                first_byte_deadline=first_byte_deadline,
            )
        except httpx.RequestError as exc:
            fallback_duration_ms = _elapsed_ms(fallback_started)
            await runtime.metrics.record(
                context.model_id,
                key.name,
                None,
                retried=True,
                failed=True,
                duration_ms=fallback_duration_ms,
                first_token_ms=fallback_duration_ms,
                requested_model_id=context.requested_model_id,
                caller_type=context.caller_type,
            )
            await runtime.key_pool.mark_failure(context.model_id, key.name)
            await _broadcast_metrics(runtime)
            return AttemptOutcome(
                retry_error=JSONResponse(
                    {"error": {"message": f"上游请求失败: {exc.__class__.__name__}"}},
                    status_code=502,
                )
            )
        # 使用回退响应继续处理
        response = fallback_response
        duration_ms = _elapsed_ms(started)
    
    if (
        response.status_code in RETRYABLE_STATUS_CODES
        and attempt + 1 < context.attempts
    ):
        content = await response.aread()
        _debug_report(
            "upstream-retryable-response",
            _response_debug_payload(context, key, response, duration_ms, content),
        )
        error = _json_error_response_from_content(response, content)
        await _record_upstream_response(
            runtime,
            context.model_id,
            key.name,
            context.requested_model_id,
            response,
            content,
            duration_ms,
            retried=True,
            caller_type=context.caller_type,
        )
        await response.aclose()
        return AttemptOutcome(retry_error=error)

    if context.is_stream and response.status_code >= 400:
        content = await response.aread()
        _debug_report(
            "upstream-stream-error-response",
            _response_debug_payload(context, key, response, duration_ms, content),
        )
        await response.aclose()
        await _record_upstream_response(
            runtime,
            context.model_id,
            key.name,
            context.requested_model_id,
            response,
            content,
            duration_ms,
            caller_type=context.caller_type,
        )
        return AttemptOutcome(
            response=_json_error_response_from_content(
                response, content, anthropic=context.path == "messages"
            )
        )

    # 400 错误且与工具有关时，尝试过滤非 function 工具重试
    if response.status_code == 400:
        content = await response.aread()
        if _is_tool_error(content):
            _debug_report(
                "upstream-tool-error-retry",
                _response_debug_payload(context, key, response, duration_ms, content),
            )
            await response.aclose()
            # 创建过滤后的请求体
            filtered_body = _upstream_body_with_filtered_tools(
                context.original_body,
                context.payload,
                upstream_model_id,
                runtime.config,
                stream=context.is_stream,
                reasoning_model_id=context.model_id,
            )
            retry_started = perf_counter()
            first_byte_deadline = (
                asyncio.get_running_loop().time()
                + runtime.config.stream_first_byte_timeout
                if context.is_stream
                else None
            )
            try:
                retry_response = await _send_upstream(
                    runtime.http_client,
                    context.request,
                    upstream,
                    _upstream_headers(context.request, key.api_key),
                    filtered_body,
                    stream=context.is_stream,
                    timeout=_upstream_timeout(runtime.config, context.is_stream),
                    first_byte_deadline=first_byte_deadline,
                )
                retry_duration_ms = _elapsed_ms(retry_started)
                # 如果重试成功，使用重试的响应
                if retry_response.status_code < 400:
                    _debug_report(
                        "upstream-tool-filter-success",
                        {
                            "model_id": context.model_id,
                            "key_name": key.name,
                            "original_status": 400,
                            "retry_status": retry_response.status_code,
                        },
                    )
                    response = retry_response
                    duration_ms = retry_duration_ms
                else:
                    # 重试也失败，返回原始错误
                    await retry_response.aclose()
                    await _record_upstream_response(
                        runtime,
                        context.model_id,
                        key.name,
                        context.requested_model_id,
                        response,
                        content,
                        duration_ms,
                        caller_type=context.caller_type,
                    )
                    return AttemptOutcome(
                        response=_json_error_response_from_content(
                            response, content, anthropic=context.path == "messages"
                        )
                    )
            except httpx.RequestError:
                # 重试请求失败，返回原始错误
                await _record_upstream_response(
                    runtime,
                    context.model_id,
                    key.name,
                    context.requested_model_id,
                    response,
                    content,
                    duration_ms,
                    caller_type=context.caller_type,
                )
                return AttemptOutcome(
                    response=_json_error_response_from_content(
                        response, content, anthropic=context.path == "messages"
                    )
                )
        else:
            await response.aclose()
            await _record_upstream_response(
                runtime,
                context.model_id,
                key.name,
                context.requested_model_id,
                response,
                content,
                duration_ms,
                caller_type=context.caller_type,
            )
            return AttemptOutcome(
                response=_json_error_response_from_content(
                    response, content, anthropic=context.path == "messages"
                )
            )

    if context.is_stream:
        assert first_byte_deadline is not None
        return AttemptOutcome(
            response=_streaming_response(
                context,
                key,
                response,
                upstream,
                started,
                first_byte_deadline,
                native_upstream=use_native,
            ),
            stream_owns_key=True,
        )
    return AttemptOutcome(
        response=await _buffered_response(
            context, key, response, duration_ms, native_upstream=use_native
        )
    )


def _streaming_response(
    context: ProxyRequestContext,
    key: KeyConfig,
    response: httpx.Response,
    upstream: str,
    started: float,
    first_byte_deadline: float,
    *,
    native_upstream: bool = False,
) -> StreamingResponse:
    runtime = context.runtime
    _debug_report(
        "upstream-stream-response",
        {
            "model_id": context.model_id,
            "requested_model_id": context.requested_model_id,
            "key_name": key.name,
            "status_code": response.status_code,
            "duration_ms": _elapsed_ms(started),
            "content_type": response.headers.get("content-type"),
        },
    )
    stream = _stream_upstream(
        response,
        runtime.metrics,
        runtime.key_pool,
        context.model_id,
        key.name,
        context.requested_model_id,
        upstream,
        started,
        context.caller_type,
        first_byte_deadline=first_byte_deadline,
        idle_timeout=runtime.config.stream_idle_timeout,
    )
    media_type = response.headers.get("content-type")
    response_headers = _response_headers(response)
    if _is_sse_media_type(media_type):
        _set_streaming_headers(response_headers)
    if context.path == "messages" and not native_upstream:
        stream = _stream_anthropic_messages(
            response,
            runtime.metrics,
            runtime.key_pool,
            context.model_id,
            key.name,
            context.requested_model_id,
            upstream,
            started,
            context.caller_type,
            first_byte_deadline=first_byte_deadline,
            idle_timeout=runtime.config.stream_idle_timeout,
        )
        media_type = "text/event-stream"
        _set_streaming_headers(response_headers)
    elif context.path == "responses" and not native_upstream:
        stream = _stream_responses(
            response,
            runtime.metrics,
            runtime.key_pool,
            context.model_id,
            key.name,
            context.requested_model_id,
            upstream,
            started,
            context.caller_type,
            first_byte_deadline=first_byte_deadline,
            idle_timeout=runtime.config.stream_idle_timeout,
        )
        media_type = "text/event-stream"
        _set_streaming_headers(response_headers)
    return StreamingResponse(
        stream,
        status_code=response.status_code,
        headers=response_headers,
        media_type=media_type,
    )


async def _buffered_response(
    context: ProxyRequestContext,
    key: KeyConfig,
    response: httpx.Response,
    duration_ms: int,
    *,
    native_upstream: bool = False,
) -> Response:
    content = await response.aread()
    _debug_report(
        "upstream-buffered-response",
        _response_debug_payload(context, key, response, duration_ms, content),
    )
    await response.aclose()
    await _record_upstream_response(
        context.runtime,
        context.model_id,
        key.name,
        context.requested_model_id,
        response,
        content,
        duration_ms,
        caller_type=context.caller_type,
    )
    if response.status_code >= 400:
        return _json_error_response_from_content(
            response, content, anthropic=context.path == "messages"
        )
    if context.path == "messages":
        converted = _anthropic_message_response(
            _json_bytes(content), context.requested_model_id
        )
        if converted is None:
            return JSONResponse(
                {
                    "type": "error",
                    "error": {
                        "type": "api_error",
                        "message": (
                            "上游返回了非 JSON 响应，无法转换为 Anthropic Messages 响应"
                        ),
                    },
                },
                status_code=502,
            )
        return JSONResponse(
            converted,
            status_code=response.status_code,
            headers=_response_headers(response),
        )
    if context.path == "responses" and not native_upstream:
        converted = _responses_response(
            _json_bytes(content), context.requested_model_id
        )
        if converted is None:
            return JSONResponse(
                {
                    "error": {
                        "message": "上游返回了非 JSON 响应，无法转换为 Responses 响应"
                    }
                },
                status_code=502,
            )
        return JSONResponse(
            converted,
            status_code=response.status_code,
            headers=_response_headers(response),
        )
    return Response(
        content=content,
        status_code=response.status_code,
        headers=_response_headers(response),
        media_type=response.headers.get("content-type"),
    )


def _response_debug_payload(
    context: ProxyRequestContext,
    key: KeyConfig,
    response: httpx.Response,
    duration_ms: int,
    content: bytes,
) -> dict[str, Any]:
    return {
        "model_id": context.model_id,
        "requested_model_id": context.requested_model_id,
        "key_name": key.name,
        "status_code": response.status_code,
        "duration_ms": duration_ms,
        "content_type": response.headers.get("content-type"),
        "content_preview": content[:500].decode("utf-8", errors="replace"),
    }


def _set_streaming_headers(headers: dict[str, str]) -> None:
    headers["cache-control"] = "no-cache"
    headers["x-accel-buffering"] = "no"


def _merge_usage(
    current: dict[str, Any] | None, update: dict[str, Any]
) -> dict[str, Any]:
    if current is None:
        return dict(update)
    merged = dict(current)
    merged.update(update)
    return merged


async def _record_upstream_response(
    state: RuntimeResources,
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
        await state.key_pool.mark_failure(
            model_id, key_name, response.status_code, retry_after_seconds(response)
        )
    await _broadcast_metrics(state)


async def _broadcast_metrics(state: RuntimeResources) -> None:
    """向 WebSocket 客户端推送最新 metrics 快照（仅当有客户端连接时）。"""
    event_bus = getattr(state, "event_bus", None)
    if event_bus is not None and event_bus.client_count > 0:
        snapshot = await state.metrics.snapshot()
        await event_bus.broadcast("metrics_snapshot", snapshot)


def _elapsed_ms(started: float) -> int:
    return round((perf_counter() - started) * 1000)


async def _stream_upstream(
    response: httpx.Response,
    metrics: MetricsStore,
    key_pool: KeyPool,
    model_id: str,
    key_name: str,
    requested_model_id: str,
    upstream: str,
    started: float,
    caller_type: str = "local",
    *,
    first_byte_deadline: float,
    idle_timeout: float,
):
    lifecycle = StreamLifecycle(
        response,
        metrics,
        key_pool,
        model_id,
        key_name,
        requested_model_id,
        upstream,
        started,
        caller_type,
    )
    buffer = ""
    sse_buffer = b""
    is_sse = _is_sse_media_type(response.headers.get("content-type"))
    failed = False
    try:
        async for chunk in iter_stream_bytes(
            response.aiter_bytes(),
            first_byte_deadline=first_byte_deadline,
            idle_timeout=idle_timeout,
        ):
            lifecycle.observe_chunk(chunk)
            buffer, chunk_usage = _stream_usage(buffer, chunk)
            if chunk_usage is not None:
                lifecycle.usage = _merge_usage(lifecycle.usage, chunk_usage)
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
        payload = lifecycle.error_payload(exc)
        LOGGER.warning(
            "upstream stream error %s", json.dumps(payload, ensure_ascii=False)
        )
        _debug_report("upstream-stream-error", payload)
    finally:
        await lifecycle.finish(failed=failed)
        _debug_report("upstream-stream-close", lifecycle.close_report())


def _is_sse_media_type(media_type: str | None) -> bool:
    return bool(
        media_type
        and media_type.split(";", 1)[0].strip().lower() == "text/event-stream"
    )


def _split_sse_events(buffer: bytes, chunk: bytes) -> tuple[bytes, list[bytes]]:
    buffer += chunk
    events: list[bytes] = []
    while True:
        separators = [
            (index, separator)
            for separator in (b"\n\n", b"\r\n\r\n")
            if (index := buffer.find(separator)) >= 0
        ]
        if not separators:
            return buffer, events
        index, separator = min(separators, key=lambda item: item[0])
        end = index + len(separator)
        events.append(buffer[:end])
        buffer = buffer[end:]


async def _stream_anthropic_messages(
    response: httpx.Response,
    metrics: MetricsStore,
    key_pool: KeyPool,
    model_id: str,
    key_name: str,
    requested_model_id: str,
    upstream: str,
    started: float,
    caller_type: str = "local",
    *,
    first_byte_deadline: float,
    idle_timeout: float,
):
    lifecycle = StreamLifecycle(
        response,
        metrics,
        key_pool,
        model_id,
        key_name,
        requested_model_id,
        upstream,
        started,
        caller_type,
    )
    buffer = ""
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
        yield _anthropic_sse(
            "message_start",
            {
                "type": "message_start",
                "message": {
                    "id": "msg_amkr",
                    "type": "message",
                    "role": "assistant",
                    "model": requested_model_id,
                    "content": [],
                    "stop_reason": None,
                    "stop_sequence": None,
                    "usage": {"input_tokens": 0, "output_tokens": 0},
                },
            },
        )
        async for chunk in iter_stream_bytes(
            response.aiter_bytes(),
            first_byte_deadline=first_byte_deadline,
            idle_timeout=idle_timeout,
        ):
            lifecycle.observe_chunk(chunk)
            buffer, events, chunk_usage = _anthropic_stream_events(
                buffer, chunk, stream_state
            )
            if chunk_usage is not None:
                lifecycle.usage = _merge_usage(lifecycle.usage, chunk_usage)
            for event in events:
                yield event
                await _flush_stream_event()
        if buffer.strip():
            buffer, events, chunk_usage = _anthropic_stream_events(
                buffer, b"\n", stream_state
            )
            if chunk_usage is not None:
                lifecycle.usage = _merge_usage(lifecycle.usage, chunk_usage)
            for event in events:
                yield event
                await _flush_stream_event()

        text_index = stream_state.get("text_index")
        if isinstance(text_index, int) and not stream_state["text_stopped"]:
            yield _anthropic_sse(
                "content_block_stop",
                {"type": "content_block_stop", "index": text_index},
            )
            await _flush_stream_event()
            stream_state["text_stopped"] = True

        tool_calls = stream_state["tool_calls"]
        for event in _finish_anthropic_stream_tools(stream_state):
            yield event
            await _flush_stream_event()

        if stream_state["next_content_index"] == 0:
            yield _anthropic_sse(
                "content_block_start",
                {
                    "type": "content_block_start",
                    "index": 0,
                    "content_block": {"type": "text", "text": ""},
                },
            )
            await _flush_stream_event()
            yield _anthropic_sse(
                "content_block_stop", {"type": "content_block_stop", "index": 0}
            )
            await _flush_stream_event()

        stop_reason = stream_state.get("stop_reason")
        if not stop_reason:
            stop_reason = "tool_use" if tool_calls else "end_turn"
        yield _anthropic_sse(
            "message_delta",
            {
                "type": "message_delta",
                "delta": {"stop_reason": stop_reason, "stop_sequence": None},
                "usage": _anthropic_stream_usage(lifecycle.usage),
            },
        )
        await _flush_stream_event()
        yield _anthropic_sse("message_stop", {"type": "message_stop"})
    except Exception as exc:
        failed = True
        payload = lifecycle.error_payload(exc)
        LOGGER.warning(
            "upstream anthropic stream error %s",
            json.dumps(payload, ensure_ascii=False),
        )
        _debug_report("upstream-anthropic-stream-error", payload)
    finally:
        await lifecycle.finish(failed=failed)
        _debug_report("upstream-anthropic-stream-close", lifecycle.close_report())


async def _stream_responses(
    response: httpx.Response,
    metrics: MetricsStore,
    key_pool: KeyPool,
    model_id: str,
    key_name: str,
    requested_model_id: str,
    upstream: str,
    started: float,
    caller_type: str = "local",
    *,
    first_byte_deadline: float,
    idle_timeout: float,
):
    lifecycle = StreamLifecycle(
        response,
        metrics,
        key_pool,
        model_id,
        key_name,
        requested_model_id,
        upstream,
        started,
        caller_type,
    )
    buffer = ""
    failed = False
    stream_state: dict[str, Any] = {"text": [], "text_started": False, "tool_calls": {}}
    try:
        async for chunk in iter_stream_bytes(
            response.aiter_bytes(),
            first_byte_deadline=first_byte_deadline,
            idle_timeout=idle_timeout,
        ):
            lifecycle.observe_chunk(chunk)
            buffer, events, chunk_usage = _responses_stream_events(
                buffer, chunk, stream_state
            )
            if chunk_usage is not None:
                lifecycle.usage = _merge_usage(lifecycle.usage, chunk_usage)
            for event in events:
                yield event
                await _flush_stream_event()
        if buffer.strip():
            buffer, events, chunk_usage = _responses_stream_events(
                buffer, b"\n", stream_state
            )
            if chunk_usage is not None:
                lifecycle.usage = _merge_usage(lifecycle.usage, chunk_usage)
            for event in events:
                yield event
                await _flush_stream_event()

        for output_index, item in enumerate(
            _responses_stream_output_items(stream_state)
        ):
            yield _responses_sse(
                "response.output_item.done",
                {
                    "type": "response.output_item.done",
                    "output_index": output_index,
                    "item": item,
                },
            )
            await _flush_stream_event()
        yield _responses_sse(
            "response.completed",
            {
                "type": "response.completed",
                "response": {
                    "id": "resp_amkr",
                    "model": requested_model_id,
                    "usage": _responses_usage(lifecycle.usage),
                },
            },
        )
    except Exception as exc:
        failed = True
        payload = lifecycle.error_payload(exc)
        LOGGER.warning(
            "upstream responses stream error %s",
            json.dumps(payload, ensure_ascii=False),
        )
        _debug_report("upstream-responses-stream-error", payload)
    finally:
        await lifecycle.finish(failed=failed)
        _debug_report("upstream-responses-stream-close", lifecycle.close_report())
