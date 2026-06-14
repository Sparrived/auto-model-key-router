from __future__ import annotations

import asyncio
import json
import secrets
from dataclasses import asdict
from pathlib import Path
from typing import Any


class KeyStateStore:
    def __init__(self, path: str | Path) -> None:
        self.path = Path(path)
        self._write_lock = asyncio.Lock()

    def load(self) -> list[dict[str, Any]]:
        try:
            raw = json.loads(self.path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return []
        keys = raw.get("keys", [])
        return keys if isinstance(keys, list) else []

    async def save(self, states: dict[tuple[str, str], Any]) -> None:
        payload = {
            "version": 1,
            "keys": [
                {"model_id": model_id, "key_name": key_name, **asdict(state)}
                for (model_id, key_name), state in sorted(states.items())
                if state.failures
                or state.cooldown_until
                or state.last_status_code is not None
                or state.disabled
            ],
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
