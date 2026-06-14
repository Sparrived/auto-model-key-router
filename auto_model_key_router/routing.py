from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class RetryPolicy:
    max_retries: int

    def attempts(
        self,
        *,
        key_count: int,
        requested_key_name: str | None,
        only_first: bool,
    ) -> int:
        if requested_key_name or only_first or key_count == 1:
            return self.max_retries + 1
        return key_count
