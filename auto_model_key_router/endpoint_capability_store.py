from __future__ import annotations

import asyncio
import json
import secrets
from pathlib import Path
from typing import Any


class EndpointCapabilityStore:
    def __init__(self, path: str | Path) -> None:
        self.path = Path(path)
        self._write_lock = asyncio.Lock()

    def load(self) -> dict[str, Any]:
        try:
            raw = json.loads(self.path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return {}
        states = raw.get("endpoint_capabilities", raw.get("url_native_support", {}))
        return states if isinstance(states, dict) else {}

    async def save(self, states: dict[str, Any]) -> None:
        payload = {
            "version": 1,
            "endpoint_capabilities": dict(sorted(states.items())),
        }
        async with self._write_lock:
            await asyncio.to_thread(self._write_atomic, payload)

    def _write_atomic(self, payload: dict[str, Any]) -> None:
        temporary_path = self.path.with_name(
            f".{self.path.name}.{secrets.token_hex(6)}.tmp"
        )
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            temporary_path.write_text(
                json.dumps(payload, indent=2, ensure_ascii=False) + "\n",
                encoding="utf-8",
            )
            temporary_path.replace(self.path)
        except OSError:
            return
        finally:
            try:
                temporary_path.unlink()
            except OSError:
                pass
