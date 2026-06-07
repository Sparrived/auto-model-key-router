from __future__ import annotations

import asyncio
from collections import defaultdict

from .config import KeyConfig, RouterConfig


class KeyPool:
    def __init__(self, config: RouterConfig):
        self._keys = {model.id: model.keys for model in config.models}
        self._routing_modes = {model.id: model.routing_mode for model in config.models}
        self._aliases = {
            name: model.id
            for model in config.models
            for name in (model.id, *model.aliases)
        }
        self._cursors = defaultdict(int)
        self._lock = asyncio.Lock()

    @property
    def model_ids(self) -> list[str]:
        return sorted(self._keys)

    @property
    def public_model_ids(self) -> list[str]:
        return sorted(self._aliases)

    def resolve_model_id(self, model_id: str) -> str:
        return self._aliases.get(model_id, model_id)

    def key_count(self, model_id: str) -> int:
        return len(self._keys.get(self.resolve_model_id(model_id), ()))

    def routing_mode(self, model_id: str) -> str:
        return self._routing_modes.get(self.resolve_model_id(model_id), "round_robin")

    async def next_key(self, model_id: str, excluded: set[str] | None = None) -> KeyConfig:
        model_id = self.resolve_model_id(model_id)
        excluded = excluded or set()
        keys = self._keys.get(model_id)
        if not keys:
            raise KeyError(model_id)

        if self.routing_mode(model_id) == "priority":
            for candidate in keys:
                if candidate.name not in excluded:
                    return candidate
            raise RuntimeError(f"模型 {model_id} 没有可用 key")

        async with self._lock:
            for _ in range(len(keys)):
                cursor = self._cursors[model_id] % len(keys)
                self._cursors[model_id] += 1
                candidate = keys[cursor]
                if candidate.name not in excluded:
                    return candidate

        raise RuntimeError(f"模型 {model_id} 没有可用 key")
