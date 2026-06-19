from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from dataclasses import dataclass, field
from time import monotonic
from typing import Any

import httpx

from .config import RouterConfig
from .key_pool import KeyPool
from .metrics import MetricsStore

# 端点可用性缓存过期时间（秒）
ENDPOINT_CACHE_TTL = 3600  # 1 小时后重试原生端点


class EndpointAvailabilityCache:
    """记录不支持原生 Anthropic 端点的上游地址"""

    def __init__(self, ttl: float = ENDPOINT_CACHE_TTL) -> None:
        self._unsupported: dict[str, float] = {}
        self._ttl = ttl
        self._lock = asyncio.Lock()

    async def is_unsupported(self, base_url: str, path: str) -> bool:
        key = f"{base_url.rstrip('/')}:{path}"
        async with self._lock:
            expires_at = self._unsupported.get(key)
            if expires_at is None:
                return False
            if monotonic() > expires_at:
                del self._unsupported[key]
                return False
            return True

    async def mark_unsupported(self, base_url: str, path: str) -> None:
        key = f"{base_url.rstrip('/')}:{path}"
        async with self._lock:
            self._unsupported[key] = monotonic() + self._ttl

    async def clear(self, base_url: str | None = None) -> None:
        async with self._lock:
            if base_url is None:
                self._unsupported.clear()
            else:
                prefix = base_url.rstrip("/")
                self._unsupported = {
                    k: v for k, v in self._unsupported.items()
                    if not k.startswith(prefix + ":")
                }


@dataclass(eq=False)
class RuntimeResources:
    config: RouterConfig
    key_pool: KeyPool
    metrics: MetricsStore
    http_client: httpx.AsyncClient
    endpoint_cache: EndpointAvailabilityCache = field(default_factory=EndpointAvailabilityCache)
    event_bus: Any = None
    active_leases: int = 0
    retired: bool = False
    detached: bool = False
    drained: asyncio.Event = field(default_factory=asyncio.Event)

    def __post_init__(self) -> None:
        self.drained.set()


class RuntimeLease:
    def __init__(self, manager: RuntimeManager, resources: RuntimeResources) -> None:
        self._manager = manager
        self.resources = resources
        self._released = False

    async def release(self) -> None:
        if self._released:
            return
        self._released = True
        await self._manager.release(self.resources)

    async def wrap_stream(self, stream: AsyncIterator[bytes]) -> AsyncIterator[bytes]:
        try:
            async for chunk in stream:
                yield chunk
        finally:
            await self.release()


class RuntimeManager:
    def __init__(self, resources: RuntimeResources) -> None:
        self._current = resources
        self._generations: set[RuntimeResources] = {resources}
        self._lock = asyncio.Lock()
        self._closed = False

    @property
    def current(self) -> RuntimeResources:
        return self._current

    async def acquire(self) -> RuntimeLease:
        async with self._lock:
            if self._closed:
                raise RuntimeError("runtime manager is closed")
            resources = self._current
            resources.active_leases += 1
            resources.drained.clear()
        return RuntimeLease(self, resources)

    async def replace(self, resources: RuntimeResources) -> None:
        to_detach: RuntimeResources | None = None
        async with self._lock:
            if self._closed:
                raise RuntimeError("runtime manager is closed")
            previous = self._current
            if previous is resources:
                return
            previous.retired = True
            self._current = resources
            self._generations.add(resources)
            if previous.active_leases == 0:
                to_detach = self._detach_locked(previous)
        if to_detach is not None:
            await self._close_unused_resources(to_detach)

    async def release(self, resources: RuntimeResources) -> None:
        to_detach: RuntimeResources | None = None
        async with self._lock:
            if resources.detached:
                return
            resources.active_leases = max(resources.active_leases - 1, 0)
            if resources.active_leases == 0:
                resources.drained.set()
                if resources.retired:
                    to_detach = self._detach_locked(resources)
        if to_detach is not None:
            await self._close_unused_resources(to_detach)

    async def close(self) -> None:
        async with self._lock:
            if self._closed:
                return
            self._closed = True
            generations = tuple(self._generations)
            for resources in generations:
                resources.retired = True

        await asyncio.gather(*(resources.drained.wait() for resources in generations))
        for resources in generations:
            async with self._lock:
                detached = self._detach_locked(resources)
            if detached is not None:
                await self._close_unused_resources(detached)

    def _detach_locked(self, resources: RuntimeResources) -> RuntimeResources | None:
        if resources.detached or resources.active_leases:
            return None
        resources.detached = True
        self._generations.discard(resources)
        return resources

    async def _close_unused_resources(self, resources: RuntimeResources) -> None:
        async with self._lock:
            close_client = not any(
                generation.http_client is resources.http_client
                for generation in self._generations
            )
            close_metrics = not any(
                generation.metrics is resources.metrics
                for generation in self._generations
            )
        if close_client:
            await resources.http_client.aclose()
        if close_metrics:
            await resources.metrics.close()


def resources_from_state(state: Any, previous: RuntimeResources | None = None) -> RuntimeResources:
    return RuntimeResources(
        config=state.config,
        key_pool=state.key_pool,
        metrics=state.metrics,
        http_client=state.http_client,
        endpoint_cache=previous.endpoint_cache if previous else EndpointAvailabilityCache(),
        event_bus=getattr(state, "event_bus", None),
    )


async def sync_runtime_from_state(state: Any) -> None:
    manager: RuntimeManager = state.runtime_manager
    current = manager.current
    if (
        current.config is state.config
        and current.key_pool is state.key_pool
        and current.metrics is state.metrics
        and current.http_client is state.http_client
    ):
        return
    await manager.replace(resources_from_state(state, previous=current))
