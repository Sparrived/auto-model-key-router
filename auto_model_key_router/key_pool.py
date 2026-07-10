from __future__ import annotations

import asyncio
from collections import defaultdict
from time import time
from typing import Any

from .config import UNIFIED_MODEL_ID, KeyConfig, RouterConfig
from .endpoint_capabilities import EndpointCapabilityCache
from .endpoint_capability_store import EndpointCapabilityStore
from .key_health import KeyHealthStore
from .visitor import VISITOR_MODEL_PREFIX


class KeyPool:
    def __init__(self, config: RouterConfig):
        self._apply_config(config)
        self._cursors = defaultdict(int)
        self._active_requests = defaultdict(int)
        self._sticky_keys: dict[tuple[str, bool, str], str] = {}
        self._lock = asyncio.Lock()
        endpoint_states = self._capability_store.load()
        self._health = KeyHealthStore(clock=time)
        self._endpoint_capabilities = EndpointCapabilityCache(
            endpoint_states, clock=time
        )

    def _apply_config(self, config: RouterConfig) -> None:
        self._keys = {model.id: model.keys for model in config.models}
        self._routing_modes = {model.id: model.routing_mode for model in config.models}
        self._reasoning_efforts = {
            model.id: model.reasoning_effort for model in config.models
        }
        self._failure_threshold = config.key_failure_threshold
        self._cooldown_seconds = config.key_cooldown_seconds
        self._capability_store = EndpointCapabilityStore(
            config.endpoint_capabilities_path
        )
        self._aliases = {
            name: model.id
            for model in config.models
            for name in (model.id, *model.aliases)
        }
        self._visitor_routes = {
            f"{VISITOR_MODEL_PREFIX}{model_id}": model_id
            for model_id in self._keys
            if not model_id.startswith(VISITOR_MODEL_PREFIX)
        }
        self._visitor_routes.update(
            {
                model_id: model_id
                for model_id in self._keys
                if model_id.startswith(VISITOR_MODEL_PREFIX)
            }
        )
        self._unified_model_id = None
        self._unified_key_name = None
        self._unified_image_model_id = None
        self._unified_image_key_name = None
        if config.unified_model is not None:
            self._unified_model_id = self._aliases[config.unified_model.model]
            self._unified_key_name = config.unified_model.key
            self._aliases[UNIFIED_MODEL_ID] = self._unified_model_id
            if config.unified_model.image_model:
                self._unified_image_model_id = self._aliases[
                    config.unified_model.image_model
                ]
                self._unified_image_key_name = config.unified_model.image_key

    @property
    def model_ids(self) -> list[str]:
        return sorted(self._keys)

    @property
    def public_model_ids(self) -> list[str]:
        return sorted(self._aliases)

    def available_model_ids(self, *, visitor_only: bool = False) -> list[str]:
        if visitor_only:
            return sorted(
                public_model_id
                for public_model_id, model_id in self._visitor_routes.items()
                if self.keys_for_model(model_id, visitor_only=True)
            )
        if self._unified_model_id is not None:
            return [UNIFIED_MODEL_ID]
        return sorted(
            name
            for name, model_id in self._aliases.items()
            if self.keys_for_model(model_id)
        )

    def resolve_model_id(self, model_id: str) -> str:
        return self._aliases.get(model_id, model_id)

    def resolve_visitor_model_id(self, public_model_id: str) -> str | None:
        return self._visitor_routes.get(public_model_id)

    def resolve_route(
        self, model_id: str, key_name: str | None = None, *, path: str | None = None
    ) -> tuple[str, str | None]:
        if model_id == UNIFIED_MODEL_ID and key_name is None:
            if (
                path in ("images/generations", "images/edits")
                and self._unified_image_model_id is not None
            ):
                return self._unified_image_model_id, self._unified_image_key_name
            key_name = self._unified_key_name
        return self.resolve_model_id(model_id), key_name

    @property
    def unified_route(self) -> dict[str, str | None] | None:
        if self._unified_model_id is None:
            return None
        result: dict[str, str | None] = {
            "model": self._unified_model_id,
            "key": self._unified_key_name,
        }
        if self._unified_image_model_id is not None:
            result["image_model"] = self._unified_image_model_id
            result["image_key"] = self._unified_image_key_name
        return result

    def key_count(self, model_id: str) -> int:
        return len(
            tuple(
                key
                for key in self._keys.get(self.resolve_model_id(model_id), ())
                if key.enabled
            )
        )

    def visitor_key_count(self, model_id: str) -> int:
        return len(
            tuple(
                key
                for key in self._keys.get(self.resolve_model_id(model_id), ())
                if key.enabled and key.allow_visitor
            )
        )

    def routing_mode(self, model_id: str) -> str:
        return self._routing_modes.get(self.resolve_model_id(model_id), "round_robin")

    def reasoning_effort(self, model_id: str) -> str | None:
        return self._reasoning_efforts.get(self.resolve_model_id(model_id))

    def keys_for_model(
        self, model_id: str, *, visitor_only: bool = False
    ) -> tuple[KeyConfig, ...]:
        model_id = self.resolve_model_id(model_id)
        return tuple(
            key
            for key in self._keys.get(model_id, ())
            if key.enabled
            and (not visitor_only or key.allow_visitor)
        )

    def key_by_name(
        self, model_id: str, key_name: str, *, visitor_only: bool = False
    ) -> KeyConfig:
        model_id = self.resolve_model_id(model_id)
        keys = self._keys.get(model_id)
        if not keys:
            raise KeyError(model_id)
        for key in keys:
            if (
                key.name == key_name
                and key.enabled
                and (not visitor_only or key.allow_visitor)
            ):
                return key
        raise RuntimeError(f"模型 {model_id} 未配置 key: {key_name}")

    async def next_key(
        self,
        model_id: str,
        excluded: set[str] | None = None,
        *,
        visitor_only: bool = False,
        affinity_key: str | None = None,
    ) -> KeyConfig:
        model_id = self.resolve_model_id(model_id)
        excluded = excluded or set()
        keys = list(self.keys_for_model(model_id, visitor_only=visitor_only))
        if not keys:
            if self._keys.get(model_id):
                raise RuntimeError(f"模型 {model_id} 没有可用 key")
            raise KeyError(model_id)

        if self.routing_mode(model_id) == "only_first":
            first_key = keys[0]
            if first_key.name not in excluded:
                await self.acquire_key(model_id, first_key.name)
                return first_key
            raise RuntimeError(f"模型 {model_id} 没有可用 key")

        available = [
            key
            for key in keys
            if key.name not in excluded
            and not self._is_cooling_down(model_id, key.name)
        ]
        if not available:
            available = [key for key in keys if key.name not in excluded]
        if not available:
            raise RuntimeError(f"模型 {model_id} 没有可用 key")

        if self.routing_mode(model_id) == "priority":
            async with self._lock:
                for candidate in keys:
                    if candidate in available:
                        self._active_requests[(model_id, candidate.name)] += 1
                        return candidate
            raise RuntimeError(f"模型 {model_id} 没有可用 key")

        sticky_key = None
        if affinity_key and self.routing_mode(model_id) == "round_robin":
            sticky_key = (model_id, visitor_only, affinity_key)

        async with self._lock:
            if sticky_key is not None:
                sticky_key_name = self._sticky_keys.get(sticky_key)
                for candidate in available:
                    if candidate.name == sticky_key_name:
                        self._active_requests[(model_id, candidate.name)] += 1
                        return candidate
                self._sticky_keys.pop(sticky_key, None)

            lowest_active = min(
                self._active_requests.get((model_id, key.name), 0) for key in available
            )
            for _ in range(len(keys)):
                cursor = self._cursors[model_id] % len(keys)
                self._cursors[model_id] += 1
                candidate = keys[cursor]
                if (
                    candidate in available
                    and self._active_requests.get((model_id, candidate.name), 0)
                    == lowest_active
                ):
                    self._active_requests[(model_id, candidate.name)] += 1
                    if sticky_key is not None:
                        self._sticky_keys[sticky_key] = candidate.name
                    return candidate

        raise RuntimeError(f"模型 {model_id} 没有可用 key")

    async def acquire_key(self, model_id: str, key_name: str) -> None:
        model_id = self.resolve_model_id(model_id)
        async with self._lock:
            self._active_requests[(model_id, key_name)] += 1

    async def release_key(self, model_id: str, key_name: str) -> None:
        model_id = self.resolve_model_id(model_id)
        async with self._lock:
            active_key = (model_id, key_name)
            count = self._active_requests.get(active_key, 0)
            if count <= 1:
                self._active_requests.pop(active_key, None)
            else:
                self._active_requests[active_key] = count - 1

    async def mark_success(self, model_id: str, key_name: str) -> None:
        model_id = self.resolve_model_id(model_id)
        async with self._lock:
            self._health.mark_success((model_id, key_name))

    async def mark_failure(
        self,
        model_id: str,
        key_name: str,
        status_code: int | None = None,
        retry_after: float | None = None,
    ) -> None:
        model_id = self.resolve_model_id(model_id)
        async with self._lock:
            self._health.mark_failure(
                (model_id, key_name),
                status_code=status_code,
                retry_after=retry_after,
                failure_threshold=self._failure_threshold,
                cooldown_seconds=self._cooldown_seconds,
            )

    def endpoint_capability_states(self) -> dict[str, dict[str, Any]]:
        """返回所有 URL 的原生端点支持状态"""
        return self._endpoint_capabilities.payloads()

    def supports_native_endpoint(
        self, base_url: str, route_path: str = "v1/messages"
    ) -> bool | None:
        """返回 None=未测试, True/False=已测试结果"""
        return self._endpoint_capabilities.get(base_url, route_path)

    async def update_native_endpoint(
        self,
        base_url: str,
        supported: bool,
        route_path: str = "v1/messages",
        reason: str = "ok",
    ) -> None:
        async with self._lock:
            self._endpoint_capabilities.update(base_url, supported, route_path, reason)
        await self._capability_store.save(self._endpoint_capabilities.persisted())

    def _is_cooling_down(self, model_id: str, key_name: str) -> bool:
        return self._health.is_cooling_down((model_id, key_name))
