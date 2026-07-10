from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from dataclasses import dataclass, field

import httpx

from .config import RouterConfig
from .key_pool import KeyPool
from .metrics import MetricsStore


@dataclass(eq=False)
class RuntimeResources:
    config: RouterConfig
    key_pool: KeyPool
    metrics: MetricsStore
    http_client: httpx.AsyncClient
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
