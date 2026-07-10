from __future__ import annotations

from dataclasses import asdict, dataclass
from time import time
from collections.abc import Callable
from typing import Any


NATIVE_NEGATIVE_TTL_SECONDS = 600
NATIVE_ERROR_TTL_SECONDS = 60


@dataclass
class EndpointCapability:
    supported: bool
    checked_at: float
    reason: str = "ok"
    ttl_seconds: int = 0


class EndpointCapabilityCache:
    def __init__(
        self, raw_states: dict[str, Any], clock: Callable[[], float] = time
    ) -> None:
        self._clock = clock
        self._states: dict[str, EndpointCapability] = {}
        for key, raw in raw_states.items():
            state = _from_raw(raw)
            if state is not None:
                self._states[key] = state

    def get(self, base_url: str, route_path: str) -> bool | None:
        key = self._key(base_url, route_path)
        state = self._states.get(key)
        if state is None and route_path.strip("/") == "v1/messages":
            state = self._states.get(base_url.rstrip("/"))
        if state is None:
            return None
        if (
            state.ttl_seconds > 0
            and self._clock() - state.checked_at >= state.ttl_seconds
        ):
            return None
        return state.supported

    def update(
        self, base_url: str, supported: bool, route_path: str, reason: str = "ok"
    ) -> None:
        self._states[self._key(base_url, route_path)] = EndpointCapability(
            supported=supported,
            checked_at=self._clock(),
            reason=reason,
            ttl_seconds=0 if supported else _negative_ttl(reason),
        )

    def payloads(self) -> dict[str, dict[str, Any]]:
        now = self._clock()
        result: dict[str, dict[str, Any]] = {}
        for key, state in self._states.items():
            expires_at = (
                state.checked_at + state.ttl_seconds if state.ttl_seconds > 0 else None
            )
            result[key] = {
                **asdict(state),
                "expires_at": expires_at,
                "expires_in_seconds": max(0, int(expires_at - now))
                if expires_at is not None
                else None,
            }
        return result

    def persisted(self) -> dict[str, dict[str, Any]]:
        return {key: asdict(state) for key, state in self._states.items()}

    @staticmethod
    def _key(base_url: str, route_path: str) -> str:
        route = route_path.strip("/") or "v1/messages"
        return f"{base_url.rstrip('/')}|{route}"


def _negative_ttl(reason: str) -> int:
    return (
        NATIVE_ERROR_TTL_SECONDS if reason == "error" else NATIVE_NEGATIVE_TTL_SECONDS
    )


def _from_raw(raw: Any) -> EndpointCapability | None:
    if isinstance(raw, bool):
        return EndpointCapability(
            supported=raw,
            checked_at=time() if raw else 0.0,
            reason="legacy",
            ttl_seconds=0 if raw else NATIVE_NEGATIVE_TTL_SECONDS,
        )
    if not isinstance(raw, dict) or not isinstance(raw.get("supported"), bool):
        return None
    reason = str(raw.get("reason") or "ok")
    ttl_seconds = int(raw.get("ttl_seconds") or 0)
    if not raw["supported"] and ttl_seconds <= 0:
        ttl_seconds = _negative_ttl(reason)
    return EndpointCapability(
        supported=raw["supported"],
        checked_at=float(raw.get("checked_at") or time()),
        reason=reason,
        ttl_seconds=ttl_seconds,
    )
