from __future__ import annotations

import asyncio
from collections import defaultdict
from collections.abc import Awaitable, Callable
from dataclasses import asdict, dataclass
from time import time
from typing import Any

from .config import UNIFIED_MODEL_ID, KeyConfig, RouterConfig
from .key_state_store import KeyStateStore
from .visitor import VISITOR_MODEL_PREFIX


MAX_CONSECUTIVE_KEY_FAILURES = 5
NATIVE_NEGATIVE_TTL_SECONDS = 600
NATIVE_ERROR_TTL_SECONDS = 60


@dataclass
class KeyState:
    failures: int = 0
    cooldown_until: float = 0.0
    last_status_code: int | None = None
    disabled: bool = False


@dataclass
class NativeSupportState:
    supported: bool
    checked_at: float
    reason: str = "ok"
    ttl_seconds: int = 0


class KeyPool:
    def __init__(self, config: RouterConfig):
        self._apply_config(config)
        self._cursors = defaultdict(int)
        self._active_requests = defaultdict(int)
        self._states = defaultdict(KeyState)
        self._sticky_keys: dict[tuple[str, bool, str], str] = {}
        self._url_native_support: dict[str, NativeSupportState] = {}
        self._lock = asyncio.Lock()
        self._persist_lock = asyncio.Lock()
        self._load_states()
        self.on_key_state_change: Callable[[str, str, dict[str, Any]], Awaitable[None]] | None = None

    async def reconfigure(self, config: RouterConfig) -> None:
        async with self._lock:
            old_state_path = self._state_path
            self._apply_config(config)
            self._cursors = defaultdict(
                int,
                {
                    model_id: cursor
                    for model_id, cursor in self._cursors.items()
                    if model_id in self._keys
                },
            )
            valid_keys = self._valid_state_keys()
            self._active_requests = defaultdict(
                int,
                {
                    key: count
                    for key, count in self._active_requests.items()
                    if key in valid_keys and count > 0
                },
            )
            self._states = defaultdict(
                KeyState,
                {
                    key: state
                    for key, state in self._states.items()
                    if key in valid_keys
                },
            )
            self._sticky_keys = {
                sticky_key: key_name
                for sticky_key, key_name in self._sticky_keys.items()
                if sticky_key[0] in self._keys
                and (sticky_key[0], key_name) in valid_keys
            }
            if self._state_path != old_state_path:
                self._states = defaultdict(KeyState)
                self._load_states()

    def _apply_config(self, config: RouterConfig) -> None:
        self._keys = {model.id: model.keys for model in config.models}
        self._routing_modes = {model.id: model.routing_mode for model in config.models}
        self._reasoning_efforts = {
            model.id: model.reasoning_effort for model in config.models
        }
        self._failure_threshold = config.key_failure_threshold
        self._cooldown_seconds = config.key_cooldown_seconds
        self._state_path = config.key_state_path
        self._state_store = KeyStateStore(config.key_state_path)
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
                self._unified_image_model_id = self._aliases[config.unified_model.image_model]
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
            if path in ("images/generations", "images/edits") and self._unified_image_model_id is not None:
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
            and not self._is_disabled(model_id, key.name)
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
                and not self._is_disabled(model_id, key.name)
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
        changed = False
        async with self._lock:
            state_key = (self.resolve_model_id(model_id), key_name)
            state = self._states.get(state_key)
            if state is not None and (
                state.failures
                or state.cooldown_until
                or state.last_status_code is not None
                or state.disabled
            ):
                self._states[state_key] = KeyState()
                changed = True
        if changed:
            await self._persist_states()
            await self._notify_state_change(model_id, key_name)

    async def mark_failure(
        self,
        model_id: str,
        key_name: str,
        status_code: int | None = None,
        retry_after: float | None = None,
    ) -> None:
        model_id = self.resolve_model_id(model_id)
        async with self._lock:
            state = self._states[(model_id, key_name)]
            state.failures += 1
            state.last_status_code = status_code
            if state.failures >= MAX_CONSECUTIVE_KEY_FAILURES:
                state.disabled = True
                state.cooldown_until = 0.0
            cooldown_seconds = self._failure_cooldown_seconds(state, retry_after)
            if cooldown_seconds > 0 and (
                not state.disabled
                and (status_code == 429 or state.failures >= self._failure_threshold)
            ):
                state.cooldown_until = max(
                    state.cooldown_until, time() + cooldown_seconds
                )
        await self._persist_states()
        await self._notify_state_change(model_id, key_name)

    async def _notify_state_change(self, model_id: str, key_name: str) -> None:
        if self.on_key_state_change is None:
            return
        state_key = (self.resolve_model_id(model_id), key_name)
        state = self._states.get(state_key)
        now = time()
        info = {
            "failures": state.failures if state else 0,
            "cooldown_remaining_seconds": max(0, round(state.cooldown_until - now)) if state else 0,
            "last_status_code": state.last_status_code if state else None,
            "disabled": state.disabled if state else False,
        }
        try:
            await self.on_key_state_change(model_id, key_name, info)
        except Exception:
            pass

    def key_states(self) -> dict[str, dict[str, Any]]:
        now = time()
        return {
            f"{model_id}:{key_name}": {
                "failures": state.failures,
                "cooldown_remaining_seconds": max(0, round(state.cooldown_until - now)),
                "last_status_code": state.last_status_code,
                "disabled": state.disabled,
            }
            for (model_id, key_name), state in self._states.items()
        }

    def key_state(self, model_id: str, key_name: str) -> dict[str, Any]:
        model_id = self.resolve_model_id(model_id)
        state = self._states.get((model_id, key_name))
        now = time()
        return {
            "failures": state.failures if state else 0,
            "cooldown_remaining_seconds": max(0, round(state.cooldown_until - now)) if state else 0,
            "last_status_code": state.last_status_code if state else None,
            "disabled": state.disabled if state else False,
        }

    async def set_key_usage_state(
        self,
        model_id: str,
        key_name: str,
        *,
        disabled: bool | None = None,
        clear_cooldown: bool = False,
    ) -> dict[str, Any]:
        model_id = self.resolve_model_id(model_id)
        async with self._lock:
            state_key = (model_id, key_name)
            state = self._states[state_key]
            if disabled is not None:
                state.disabled = disabled
                if disabled:
                    state.cooldown_until = 0.0
            if clear_cooldown:
                state.failures = 0
                state.cooldown_until = 0.0
                state.last_status_code = None
                if disabled is None:
                    state.disabled = False
        await self._persist_states()
        await self._notify_state_change(model_id, key_name)
        return self.key_state(model_id, key_name)

    def url_native_support_states(self) -> dict[str, dict[str, Any]]:
        """返回所有 URL 的原生端点支持状态"""
        now = time()
        return {
            key: self._native_support_state_payload(state, now)
            for key, state in self._url_native_support.items()
        }

    def supports_native_messages(
        self, base_url: str, route_path: str = "v1/messages"
    ) -> bool | None:
        """返回 None=未测试, True/False=已测试结果"""
        key = self._native_support_key(base_url, route_path)
        state = self._url_native_support.get(key)
        if state is None and route_path.strip("/") == "v1/messages":
            state = self._url_native_support.get(base_url.rstrip("/"))
        return self._fresh_native_support(state)

    def supports_native_endpoint(
        self, base_url: str, route_path: str
    ) -> bool | None:
        """Return None for untested, otherwise the cached native endpoint support."""
        return self.supports_native_messages(base_url, route_path)

    async def update_native_support(
        self,
        base_url: str,
        supported: bool,
        route_path: str = "v1/messages",
        reason: str = "ok",
    ) -> None:
        key = self._native_support_key(base_url, route_path)
        ttl_seconds = 0 if supported else _native_negative_ttl(reason)
        async with self._lock:
            self._url_native_support[key] = NativeSupportState(
                supported=supported,
                checked_at=time(),
                reason=reason,
                ttl_seconds=ttl_seconds,
            )
        await self._persist_states()

    async def update_native_endpoint(
        self, base_url: str, supported: bool, route_path: str, reason: str = "ok"
    ) -> None:
        await self.update_native_support(base_url, supported, route_path, reason)

    def _native_support_key(self, base_url: str, route_path: str) -> str:
        route = route_path.strip("/") or "v1/messages"
        return f"{base_url.rstrip('/')}|{route}"

    def _fresh_native_support(self, state: NativeSupportState | None) -> bool | None:
        if state is None:
            return None
        if state.ttl_seconds > 0 and time() - state.checked_at >= state.ttl_seconds:
            return None
        return state.supported

    def _native_support_state_payload(
        self, state: NativeSupportState, now: float
    ) -> dict[str, Any]:
        expires_at = (
            state.checked_at + state.ttl_seconds if state.ttl_seconds > 0 else None
        )
        return {
            "supported": state.supported,
            "checked_at": state.checked_at,
            "reason": state.reason,
            "ttl_seconds": state.ttl_seconds,
            "expires_at": expires_at,
            "expires_in_seconds": (
                max(0, int(expires_at - now)) if expires_at is not None else None
            ),
        }

    def _failure_cooldown_seconds(
        self, state: KeyState, retry_after: float | None
    ) -> float:
        cooldown_seconds = (
            retry_after if retry_after is not None else self._cooldown_seconds
        )
        if cooldown_seconds <= 0:
            return 0.0
        return cooldown_seconds * max(1, state.failures)

    def _is_disabled(self, model_id: str, key_name: str) -> bool:
        state = self._states.get((model_id, key_name))
        return state.disabled if state is not None else False

    def _is_cooling_down(self, model_id: str, key_name: str) -> bool:
        return self._states[(model_id, key_name)].cooldown_until > time()

    def _valid_state_keys(self) -> set[tuple[str, str]]:
        return {
            (model_id, key.name)
            for model_id, keys in self._keys.items()
            for key in keys
        }

    def _load_states(self) -> None:
        valid_keys = self._valid_state_keys()
        keys_data, url_native_support = self._state_store.load()
        for item in keys_data:
            if not isinstance(item, dict):
                continue
            model_id = str(item.get("model_id") or "")
            key_name = str(item.get("key_name") or "")
            if (model_id, key_name) not in valid_keys:
                continue
            self._states[(model_id, key_name)] = KeyState(
                failures=max(0, int(item.get("failures") or 0)),
                cooldown_until=max(0.0, float(item.get("cooldown_until") or 0.0)),
                last_status_code=item.get("last_status_code"),
                disabled=bool(item.get("disabled", False)),
            )
        # 加载 URL 级别的原生支持数据
        for url, item in url_native_support.items():
            state = _native_support_state_from_raw(item)
            if state is not None:
                self._url_native_support[url] = state

    async def _persist_states(self) -> None:
        async with self._persist_lock:
            async with self._lock:
                states = {
                    key: KeyState(
                        failures=state.failures,
                        cooldown_until=state.cooldown_until,
                        last_status_code=state.last_status_code,
                        disabled=state.disabled,
                    )
                    for key, state in self._states.items()
                }
                url_native_support = {
                    key: asdict(state)
                    for key, state in self._url_native_support.items()
                }
            await self._state_store.save(states, url_native_support)


def _native_negative_ttl(reason: str) -> int:
    if reason == "error":
        return NATIVE_ERROR_TTL_SECONDS
    return NATIVE_NEGATIVE_TTL_SECONDS


def _native_support_state_from_raw(item: Any) -> NativeSupportState | None:
    if isinstance(item, bool):
        return NativeSupportState(
            supported=item,
            checked_at=time() if item else 0.0,
            reason="legacy",
            ttl_seconds=0 if item else NATIVE_NEGATIVE_TTL_SECONDS,
        )
    if not isinstance(item, dict) or not isinstance(item.get("supported"), bool):
        return None
    reason = str(item.get("reason") or "ok")
    ttl_seconds = int(item.get("ttl_seconds") or 0)
    if not item["supported"] and ttl_seconds <= 0:
        ttl_seconds = _native_negative_ttl(reason)
    return NativeSupportState(
        supported=item["supported"],
        checked_at=float(item.get("checked_at") or time()),
        reason=reason,
        ttl_seconds=ttl_seconds,
    )
