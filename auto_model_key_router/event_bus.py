from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import Awaitable, Callable
from typing import Any

from fastapi import WebSocket

LOGGER = logging.getLogger("auto_model_key_router.event_bus")


class EventBus:
    """管理 WebSocket 事件推送的总线。

    客户端连接后需先发送认证消息：
        {"type": "auth", "token": "<api_key>"}
    认证通过后才会收到广播事件。
    """

    def __init__(self) -> None:
        self._clients: set[WebSocket] = set()
        self._lock = asyncio.Lock()
        self.on_client_count_change: Callable[[int], Awaitable[None]] | None = None

    async def authenticate(
        self, websocket: WebSocket, token: str, expected_key: str
    ) -> bool:
        """等待客户端发送 auth 消息并验证 token。

        返回 True 表示认证成功，已加入广播列表。
        """
        try:
            raw = await asyncio.wait_for(websocket.receive_text(), timeout=10.0)
            message = json.loads(raw)
        except (asyncio.TimeoutError, json.JSONDecodeError, Exception):
            await websocket.close(code=4001, reason="auth timeout or invalid message")
            return False

        if message.get("type") != "auth" or message.get("token") != expected_key:
            await websocket.close(code=4003, reason="auth failed")
            return False

        async with self._lock:
            self._clients.add(websocket)
        total = len(self._clients)
        LOGGER.debug("event bus client connected, total=%d", total)
        if self.on_client_count_change is not None:
            await self.on_client_count_change(total)
        return True

    async def disconnect(self, websocket: WebSocket) -> None:
        async with self._lock:
            self._clients.discard(websocket)
        total = len(self._clients)
        LOGGER.debug("event bus client disconnected, total=%d", total)
        if self.on_client_count_change is not None:
            await self.on_client_count_change(total)

    async def broadcast(self, event_type: str, data: Any) -> None:
        """向所有已认证客户端推送事件。"""
        message = json.dumps(
            {"type": event_type, "data": data}, ensure_ascii=False, default=str
        )
        async with self._lock:
            clients = list(self._clients)
        if not clients:
            return
        stale: list[WebSocket] = []
        for ws in clients:
            try:
                await ws.send_text(message)
            except Exception:
                stale.append(ws)
        if stale:
            async with self._lock:
                for ws in stale:
                    self._clients.discard(ws)

    @property
    def client_count(self) -> int:
        return len(self._clients)
