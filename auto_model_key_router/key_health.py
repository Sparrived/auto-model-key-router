from __future__ import annotations

from collections import defaultdict
from dataclasses import dataclass
from time import time
from collections.abc import Callable


MAX_COOLDOWN_SECONDS = 300.0


@dataclass
class KeyHealth:
    failures: int = 0
    cooldown_until: float = 0.0


class KeyHealthStore:
    def __init__(
        self,
        clock: Callable[[], float] = time,
    ) -> None:
        self._clock = clock
        self._states: defaultdict[tuple[str, str], KeyHealth] = defaultdict(KeyHealth)

    def is_cooling_down(self, key: tuple[str, str]) -> bool:
        state = self._states.get(key)
        return state is not None and state.cooldown_until > self._clock()

    def mark_success(self, key: tuple[str, str]) -> None:
        state = self._states.get(key)
        if state is None or not (state.failures or state.cooldown_until):
            return
        self._states.pop(key, None)

    def mark_failure(
        self,
        key: tuple[str, str],
        *,
        status_code: int | None,
        retry_after: float | None,
        failure_threshold: int,
        cooldown_seconds: float,
    ) -> None:
        state = self._states[key]
        state.failures += 1
        if status_code != 429 and state.failures < failure_threshold:
            return
        if retry_after is not None:
            cooldown = min(MAX_COOLDOWN_SECONDS, max(0.0, retry_after))
        else:
            cooldown = min(
                MAX_COOLDOWN_SECONDS,
                max(0.0, cooldown_seconds) * max(1, state.failures),
            )
        if cooldown <= 0:
            return
        state.cooldown_until = max(state.cooldown_until, self._clock() + cooldown)
