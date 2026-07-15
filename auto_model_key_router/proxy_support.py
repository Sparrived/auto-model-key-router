from __future__ import annotations

import asyncio
import json
import logging
import re
from pathlib import Path
from typing import Any
from urllib.request import Request as UrlRequest, urlopen

import httpx
from fastapi import Request
from fastapi.responses import JSONResponse

from .config import (
    UPSTREAM_ROUTE_DEFAULT_PATHS,
    RouterConfig,
    upstream_route_path,
)
from .protocol_compat import _adapt_message_payload
from .visitor import is_visitor_api_key


LOGGER = logging.getLogger("auto_model_key_router.app")


CLOUDFLARE_UPSTREAM_ERROR_REASONS = {
    521: {
        "code": "cloudflare_521",
        "type": "upstream_cloudflare_error",
        "message": "上游服务不可用：Cloudflare 521 Web Server Is Down",
        "reason": "Cloudflare 无法连接到上游源站，通常表示源站服务离线、端口未监听或防火墙拒绝 Cloudflare 连接。",
    }
}


def _log_model_not_configured(
    path: str, requested_model_id: str, model_id: str, reason: str
) -> None:
    LOGGER.warning(
        "model routing rejected: path=/v1/%s requested_model=%s resolved_model=%s reason=%s",
        path,
        requested_model_id,
        model_id,
        reason,
    )


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


def _upstream_body(
    body: bytes,
    payload: dict[str, Any],
    model_id: str,
    config: RouterConfig | None = None,
    stream: bool = False,
    native: bool = False,
    reasoning_model_id: str | None = None,
) -> bytes:
    if not payload or "model" not in payload:
        return body
    upstream_payload = dict(payload)
    upstream_payload["model"] = model_id
    if native:
        return json.dumps(
            upstream_payload, ensure_ascii=False, separators=(",", ":")
        ).encode("utf-8")
    upstream_payload = _apply_reasoning_effort(
        upstream_payload, reasoning_model_id or model_id, config
    )
    upstream_payload = _adapt_message_payload(upstream_payload)
    if stream:
        stream_options = upstream_payload.get("stream_options")
        if not isinstance(stream_options, dict):
            stream_options = {}
        stream_options["include_usage"] = True
        upstream_payload["stream_options"] = stream_options
    return json.dumps(
        upstream_payload, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8")


def _is_tool_error(content: bytes) -> bool:
    """检查错误响应是否与工具有关。"""
    try:
        data = json.loads(content)
        if isinstance(data, dict):
            error = data.get("error", {})
            if isinstance(error, dict):
                message = str(error.get("message", "")).lower()
                param = str(error.get("param", "")).lower()
                # 检查是否是工具相关的错误
                if "tool" in message or "function" in message:
                    return True
                if "tool" in param or "function" in param:
                    return True
    except (json.JSONDecodeError, UnicodeDecodeError):
        pass
    return False


def _filter_function_tools(payload: dict[str, Any]) -> dict[str, Any]:
    """过滤工具，只保留 function 类型的工具。"""
    adapted = dict(payload)
    tools = adapted.get("tools")
    if isinstance(tools, list):
        adapted["tools"] = [
            tool for tool in tools
            if isinstance(tool, dict) and tool.get("type") == "function"
            and isinstance(tool.get("function"), dict)
            and tool["function"].get("name")
        ]
    return adapted


def _upstream_body_with_filtered_tools(
    body: bytes,
    payload: dict[str, Any],
    model_id: str,
    config: RouterConfig | None = None,
    stream: bool = False,
    reasoning_model_id: str | None = None,
) -> bytes:
    """创建过滤掉非 function 工具的请求体。"""
    if not payload or "model" not in payload:
        return body
    upstream_payload = _filter_function_tools(dict(payload))
    upstream_payload["model"] = model_id
    upstream_payload = _apply_reasoning_effort(
        upstream_payload, reasoning_model_id or model_id, config
    )
    upstream_payload = _adapt_message_payload(upstream_payload)
    if stream:
        stream_options = upstream_payload.get("stream_options")
        if not isinstance(stream_options, dict):
            stream_options = {}
        stream_options["include_usage"] = True
        upstream_payload["stream_options"] = stream_options
    return json.dumps(
        upstream_payload, ensure_ascii=False, separators=(",", ":")
    ).encode("utf-8")


def _apply_reasoning_effort(
    payload: dict[str, Any], model_id: str, config: RouterConfig | None
) -> dict[str, Any]:
    adapted = dict(payload)
    reasoning_effort = (
        config.reasoning_effort_by_model.get(model_id) if config is not None else None
    )
    if reasoning_effort:
        adapted["reasoning_effort"] = reasoning_effort
        return adapted
    reasoning = adapted.get("reasoning")
    if (
        "reasoning_effort" not in adapted
        and isinstance(reasoning, dict)
        and reasoning.get("effort")
    ):
        adapted["reasoning_effort"] = reasoning["effort"]
    return adapted


def _upstream_mode(path: str) -> str | None:
    if path == "chat/completions":
        return "openai"
    if path == "messages":
        return "anthropic"
    if path == "responses":
        return "responses"
    if path in ("images/generations", "images/edits"):
        return "images"
    return None


def _upstream_path(
    path: str,
    payload: dict[str, Any],
    native: bool = False,
    upstream_routes: dict[str, str] | None = None,
) -> str:
    mode = _upstream_mode(path)
    if path == "images/generations":
        return upstream_route_path(upstream_routes, "images")
    if native and mode is not None:
        return upstream_route_path(upstream_routes, mode)
    if (
        path in {"messages", "responses"}
        and isinstance(payload, dict)
        and payload.get("model")
    ):
        return upstream_route_path(upstream_routes, "openai")
    if mode == "openai":
        return upstream_route_path(upstream_routes, "openai")
    return f"v1/{path}"


def _join_url(base_url: str, path: str) -> str:
    return f"{base_url.rstrip('/')}/{path.lstrip('/')}"


def _upstream_headers(request: Request, api_key: str) -> dict[str, str]:
    blocked = {
        "authorization",
        "host",
        "content-length",
        "destination-addr",
        "x-api-key",
        "anthropic-version",
        "anthropic-beta",
    }
    headers = {
        key: value
        for key, value in request.headers.items()
        if key.lower() not in blocked
    }
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


def _response_headers(response: httpx.Response) -> dict[str, str]:
    blocked = {"content-encoding", "content-length", "transfer-encoding", "connection"}
    return {
        key: value
        for key, value in response.headers.items()
        if key.lower() not in blocked
    }


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
    first_byte_deadline: float | None = None,
) -> httpx.Response:
    request_kwargs: dict[str, Any] = {
        "params": request.query_params,
        "headers": headers,
        "content": body,
    }
    if timeout is not None:
        request_kwargs["timeout"] = timeout
    upstream_request = client.build_request(
        request.method,
        upstream,
        **request_kwargs,
    )
    try:
        if first_byte_deadline is None:
            response = await client.send(upstream_request, stream=True)
        else:
            remaining = max(
                0, first_byte_deadline - asyncio.get_running_loop().time()
            )
            async with asyncio.timeout(remaining):
                response = await client.send(upstream_request, stream=True)
    except TimeoutError as exc:
        raise httpx.ReadTimeout(
            "timed out waiting for upstream response headers",
            request=upstream_request,
        ) from exc
    try:
        if not stream:
            await response.aread()
    except Exception:
        await response.aclose()
        raise
    return response


def _debug_report(event: str, payload: dict[str, Any]) -> None:
    env_path = Path.cwd() / ".dbg" / "upstream-request-failed.env"
    try:
        values = dict(
            line.strip().split("=", 1)
            for line in env_path.read_text(encoding="utf-8").splitlines()
            if "=" in line
        )
        url = values.get("DEBUG_SERVER_URL")
        if not url:
            return
        body = json.dumps(
            {"event": event, "runId": "pre", "payload": payload}, ensure_ascii=False
        ).encode("utf-8")
        request = UrlRequest(
            url, data=body, headers={"Content-Type": "application/json"}, method="POST"
        )
        with urlopen(request, timeout=0.2):
            pass
    except Exception:
        return


def _json_error_response_from_content(
    response: httpx.Response, content: bytes, anthropic: bool = False
) -> JSONResponse:
    data = _json_bytes(content)
    if data is None:
        reason = CLOUDFLARE_UPSTREAM_ERROR_REASONS.get(response.status_code)
        if reason is not None:
            data = _structured_upstream_error(reason, response.status_code, anthropic)
        else:
            message = (
                content.decode("utf-8", errors="replace")
                or f"上游返回 HTTP {response.status_code}，且响应体为空"
            )
            if anthropic:
                data = {
                    "type": "error",
                    "error": {"type": "api_error", "message": message},
                }
            else:
                data = {"error": {"message": message}}
    elif anthropic:
        data = _anthropic_error_response(data)
    return JSONResponse(data, status_code=response.status_code)


def _structured_upstream_error(
    reason: dict[str, str], status_code: int, anthropic: bool
) -> dict[str, Any]:
    message = f"{reason['message']}：{reason['reason']}"
    if anthropic:
        return {
            "type": "error",
            "error": {
                "type": "api_error",
                "message": message,
                "code": reason["code"],
                "status_code": status_code,
                "reason": reason["reason"],
            },
        }
    return {
        "error": {
            "message": message,
            "type": reason["type"],
            "code": reason["code"],
            "status_code": status_code,
            "reason": reason["reason"],
        }
    }


def _anthropic_error_response(data: Any) -> dict[str, Any]:
    if (
        isinstance(data, dict)
        and data.get("type") == "error"
        and isinstance(data.get("error"), dict)
    ):
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


# 不支持原生 Anthropic 端点的状态码
UNSUPPORTED_ENDPOINT_STATUS_CODES = {404, 405, 501}


async def test_native_messages_support(
    client: httpx.AsyncClient,
    base_url: str,
    api_key: str,
    model_id: str,
    route_path: str | None = None,
) -> tuple[bool, str]:
    """测试上游是否支持原生 /v1/messages 端点
    
    发送一个最小化的测试请求，根据响应判断是否支持。
    返回 True 表示支持，False 表示不支持。
    """
    test_url = _join_url(
        base_url, route_path or UPSTREAM_ROUTE_DEFAULT_PATHS["anthropic"]
    )
    test_body = {
        "model": model_id,
        "max_tokens": 1,
        "messages": [{"role": "user", "content": "test"}],
    }
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "anthropic-version": "2023-06-01",
    }
    try:
        response = await client.post(
            test_url,
            json=test_body,
            headers=headers,
            timeout=httpx.Timeout(10.0),
        )
        # 如果返回这些状态码，说明端点不存在或不支持
        if response.status_code in UNSUPPORTED_ENDPOINT_STATUS_CODES:
            LOGGER.warning(
                "endpoint %s returned %d, native messages not supported",
                test_url,
                response.status_code,
            )
            return False, "unsupported"
        # 其他状态码（包括认证错误、参数错误等）说明端点存在
        LOGGER.log(
            logging.ERROR if response.status_code >= 500 else logging.WARNING
            if response.status_code >= 300
            else logging.INFO,
            "endpoint %s returned %d, native messages supported",
            test_url,
            response.status_code,
        )
        return True, "ok"
    except httpx.RequestError as exc:
        LOGGER.warning(
            "endpoint %s test failed: %s, assuming native messages not supported",
            test_url,
            exc,
        )
        return False, "error"


async def test_native_responses_support(
    client: httpx.AsyncClient,
    base_url: str,
    api_key: str,
    model_id: str,
    route_path: str | None = None,
) -> tuple[bool, str]:
    """Test whether the upstream supports a native Responses endpoint."""
    test_url = _join_url(
        base_url, route_path or UPSTREAM_ROUTE_DEFAULT_PATHS["responses"]
    )
    test_body = {
        "model": model_id,
        "input": "test",
        "max_output_tokens": 1,
    }
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
    }
    try:
        response = await client.post(
            test_url,
            json=test_body,
            headers=headers,
            timeout=httpx.Timeout(10.0),
        )
        if response.status_code in UNSUPPORTED_ENDPOINT_STATUS_CODES:
            LOGGER.warning(
                "endpoint %s returned %d, native responses not supported",
                test_url,
                response.status_code,
            )
            return False, "unsupported"
        LOGGER.log(
            logging.ERROR if response.status_code >= 500 else logging.WARNING
            if response.status_code >= 300
            else logging.INFO,
            "endpoint %s returned %d, native responses supported",
            test_url,
            response.status_code,
        )
        return True, "ok"
    except httpx.RequestError as exc:
        LOGGER.warning(
            "endpoint %s test failed: %s, assuming native responses not supported",
            test_url,
            exc,
        )
        return False, "error"
