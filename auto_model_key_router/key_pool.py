from __future__ import annotations

import asyncio
import json
from collections import defaultdict
from dataclasses import asdict, dataclass
from pathlib import Path
from time import time
from typing import Any

from .config import UNIFIED_MODEL_ID, KeyConfig, RouterConfig


@dataclass
class KeyState:
    failures: int = 0
    cooldown_until: float = 0.0
    last_status_code: int | None = None


class KeyPool:
    def __init__(self, config: RouterConfig):
        self._apply_config(config)
        self._cursors = defaultdict(int)
        self._active_requests = defaultdict(int)
        self._states = defaultdict(KeyState)
        self._lock = asyncio.Lock()
        self._load_states()

    async def reconfigure(self, config: RouterConfig) -> None:
        async with self._lock:
            old_state_path = self._state_path
            self._apply_config(config)
            self._cursors = defaultdict(int, {model_id: cursor for model_id, cursor in self._cursors.items() if model_id in self._keys})
            valid_keys = self._valid_state_keys()
            self._active_requests = defaultdict(int, {key: count for key, count in self._active_requests.items() if key in valid_keys and count > 0})
            self._states = defaultdict(KeyState, {key: state for key, state in self._states.items() if key in valid_keys})
            if self._state_path != old_state_path:
                self._states = defaultdict(KeyState)
                self._load_states()

    def _apply_config(self, config: RouterConfig) -> None:
        self._keys = {model.id: model.keys for model in config.models}
        self._routing_modes = {model.id: model.routing_mode for model in config.models}
        self._reasoning_efforts = {model.id: model.reasoning_effort for model in config.models}
        self._failure_threshold = config.key_failure_threshold
        self._cooldown_seconds = config.key_cooldown_seconds
        self._state_path = Path(config.key_state_path)
        self._aliases = {
            name: model.id
            for model in config.models
            for name in (model.id, *model.aliases)
        }
        self._unified_model_id = None
        self._unified_key_name = None
        if config.unified_model is not None:
            self._unified_model_id = self._aliases[config.unified_model.model]
            self._unified_key_name = config.unified_model.key
            self._aliases[UNIFIED_MODEL_ID] = self._unified_model_id

    @property
    def model_ids(self) -> list[str]:
        return sorted(self._keys)

    @property
    def public_model_ids(self) -> list[str]:
        return sorted(self._aliases)

    def resolve_model_id(self, model_id: str) -> str:
        return self._aliases.get(model_id, model_id)

    def resolve_route(self, model_id: str, key_name: str | None = None) -> tuple[str, str | None]:
        if model_id == UNIFIED_MODEL_ID and key_name is None:
            key_name = self._unified_key_name
        return self.resolve_model_id(model_id), key_name

    @property
    def unified_route(self) -> dict[str, str | None] | None:
        if self._unified_model_id is None:
            return None
        return {"model": self._unified_model_id, "key": self._unified_key_name}

    def key_count(self, model_id: str) -> int:
        return len(self.keys_for_model(model_id))

    def visitor_key_count(self, model_id: str) -> int:
        return len(self.keys_for_model(model_id, visitor_only=True))

    def routing_mode(self, model_id: str) -> str:
        return self._routing_modes.get(self.resolve_model_id(model_id), "round_robin")

    def reasoning_effort(self, model_id: str) -> str | None:
        return self._reasoning_efforts.get(self.resolve_model_id(model_id))

    def keys_for_model(self, model_id: str, *, visitor_only: bool = False) -> tuple[KeyConfig, ...]:
        return tuple(
            key
            for key in self._keys.get(self.resolve_model_id(model_id), ())
            if key.enabled and (not visitor_only or key.allow_visitor)
        )

    def key_by_name(self, model_id: str, key_name: str, *, visitor_only: bool = False) -> KeyConfig:
        model_id = self.resolve_model_id(model_id)
        keys = self._keys.get(model_id)
        if not keys:
            raise KeyError(model_id)
        for key in keys:
            if key.name == key_name and key.enabled and (not visitor_only or key.allow_visitor):
                return key
        raise RuntimeError(f"模型 {model_id} 未配置 key: {key_name}")

    async def next_key(self, model_id: str, excluded: set[str] | None = None, *, visitor_only: bool = False) -> KeyConfig:
        model_id = self.resolve_model_id(model_id)
        excluded = excluded or set()
        keys = list(self.keys_for_model(model_id, visitor_only=visitor_only))
        if not keys:
            raise KeyError(model_id)

        if self.routing_mode(model_id) == "only_first":
            first_key = keys[0]
            if first_key.name not in excluded:
                await self.acquire_key(model_id, first_key.name)
                return first_key
            raise RuntimeError(f"模型 {model_id} 没有可用 key")

        available = [key for key in keys if key.name not in excluded and not self._is_cooling_down(model_id, key.name)]
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

        async with self._lock:
            lowest_active = min(self._active_requests.get((model_id, key.name), 0) for key in available)
            for _ in range(len(keys)):
                cursor = self._cursors[model_id] % len(keys)
                self._cursors[model_id] += 1
                candidate = keys[cursor]
                if candidate in available and self._active_requests.get((model_id, candidate.name), 0) == lowest_active:
                    self._active_requests[(model_id, candidate.name)] += 1
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
        async with self._lock:
            self._states[(self.resolve_model_id(model_id), key_name)] = KeyState()
            self._save_states()

    async def mark_failure(self, model_id: str, key_name: str, status_code: int | None = None, retry_after: float | None = None) -> None:
        model_id = self.resolve_model_id(model_id)
        async with self._lock:
            state = self._states[(model_id, key_name)]
            state.failures += 1
            state.last_status_code = status_code
            cooldown_seconds = retry_after if retry_after is not None else self._cooldown_seconds
            if cooldown_seconds > 0 and (status_code == 429 or state.failures >= self._failure_threshold):
                state.cooldown_until = max(state.cooldown_until, time() + cooldown_seconds)
            self._save_states()

    def key_states(self) -> dict[str, dict[str, Any]]:
        now = time()
        return {
            f"{model_id}:{key_name}": {
                "failures": state.failures,
                "cooldown_remaining_seconds": max(0, round(state.cooldown_until - now)),
                "last_status_code": state.last_status_code,
            }
            for (model_id, key_name), state in self._states.items()
        }

    def _is_cooling_down(self, model_id: str, key_name: str) -> bool:
        return self._states[(model_id, key_name)].cooldown_until > time()

    def _valid_state_keys(self) -> set[tuple[str, str]]:
        return {
            (model_id, key.name)
            for model_id, keys in self._keys.items()
            for key in keys
        }

    def _load_states(self) -> None:
        try:
            raw = json.loads(self._state_path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return
        valid_keys = self._valid_state_keys()
        for item in raw.get("keys", []):
            model_id = str(item.get("model_id") or "")
            key_name = str(item.get("key_name") or "")
            if (model_id, key_name) not in valid_keys:
                continue
            self._states[(model_id, key_name)] = KeyState(
                failures=max(0, int(item.get("failures") or 0)),
                cooldown_until=max(0.0, float(item.get("cooldown_until") or 0.0)),
                last_status_code=item.get("last_status_code"),
            )

    def _save_states(self) -> None:
        try:
            self._state_path.parent.mkdir(parents=True, exist_ok=True)
            payload = {
                "version": 1,
                "keys": [
                    {"model_id": model_id, "key_name": key_name, **asdict(state)}
                    for (model_id, key_name), state in sorted(self._states.items())
                    if state.failures or state.cooldown_until or state.last_status_code is not None
                ],
            }
            self._state_path.write_text(json.dumps(payload, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        except OSError:
            return
