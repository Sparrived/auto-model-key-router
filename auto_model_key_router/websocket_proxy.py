from __future__ import annotations

import logging
from collections.abc import Awaitable, Callable
from typing import Any

from fastapi import FastAPI, Request, WebSocket, WebSocketDisconnect
from fastapi.responses import Response, StreamingResponse


LOGGER = logging.getLogger("auto_model_key_router.websocket_proxy")
ProxyHandler = Callable[[str, Request], Awaitable[Response]]
WEBSOCKET_HANDSHAKE_HEADERS = frozenset(
    {
        b"connection",
        b"sec-websocket-extensions",
        b"sec-websocket-key",
        b"sec-websocket-protocol",
        b"sec-websocket-version",
        b"upgrade",
    }
)


def register_websocket_proxy(app: FastAPI, proxy_handler: ProxyHandler) -> None:
    @app.websocket("/v1/{path:path}")
    async def websocket_proxy(websocket: WebSocket, path: str) -> None:
        await websocket.accept()
        try:
            message = await websocket.receive()
            if message["type"] == "websocket.disconnect":
                return
            body = message.get("bytes")
            if body is None:
                body = str(message.get("text") or "").encode("utf-8")

            request = _websocket_http_request(websocket, body)
            response = await proxy_handler(path, request)
            await _send_websocket_response(websocket, response)
            await websocket.close(code=_websocket_close_code(response.status_code))
        except WebSocketDisconnect:
            return
        except Exception:
            LOGGER.exception("websocket proxy failed: path=/v1/%s", path)
            try:
                await websocket.close(code=1011)
            except RuntimeError:
                pass


def _websocket_http_request(websocket: WebSocket, body: bytes) -> Request:
    scope = dict(websocket.scope)
    scope["type"] = "http"
    scope["http_version"] = "1.1"
    scope["method"] = "POST"
    scope["scheme"] = "https" if websocket.url.scheme == "wss" else "http"
    scope["headers"] = [(name, value) for name, value in websocket.scope.get("headers", []) if name.lower() not in WEBSOCKET_HANDSHAKE_HEADERS]
    consumed = False

    async def receive() -> dict[str, Any]:
        nonlocal consumed
        if consumed:
            return {"type": "http.disconnect"}
        consumed = True
        return {"type": "http.request", "body": body, "more_body": False}

    return Request(scope, receive)


async def _send_websocket_response(websocket: WebSocket, response: Response) -> None:
    content_type = response.headers.get("content-type", "")
    if isinstance(response, StreamingResponse):
        iterator = response.body_iterator
        try:
            async for chunk in iterator:
                await _send_websocket_chunk(websocket, chunk, content_type)
        finally:
            close = getattr(iterator, "aclose", None)
            if close is not None:
                await close()
        return
    if response.body:
        await _send_websocket_chunk(websocket, response.body, content_type)


async def _send_websocket_chunk(websocket: WebSocket, chunk: Any, content_type: str) -> None:
    if isinstance(chunk, str):
        await websocket.send_text(chunk)
        return
    content = bytes(chunk)
    if content_type.startswith("text/") or "json" in content_type:
        await websocket.send_text(content.decode("utf-8", errors="replace"))
    else:
        await websocket.send_bytes(content)


def _websocket_close_code(status_code: int) -> int:
    if status_code < 400:
        return 1000
    if status_code < 500:
        return 1008
    return 1011
