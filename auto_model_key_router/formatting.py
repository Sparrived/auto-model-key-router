from __future__ import annotations

import hashlib
from typing import Any
from urllib.parse import urlparse


def key_fingerprint(api_key: str) -> str:
    if not api_key:
        return ""
    return hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:12]


def short_text(value: Any, limit: int = 32) -> str:
    text = str(value)
    if len(text) <= limit:
        return text
    return f"{text[:max(limit - 1, 1)]}…"


def compact_url(value: Any, limit: int = 32) -> str:
    text = str(value or "-")
    try:
        parsed = urlparse(text)
    except ValueError:
        return short_text(text, limit)
    if parsed.netloc:
        compact = parsed.netloc
        if parsed.path and parsed.path != "/":
            compact = f"{compact}{parsed.path.rstrip('/')}"
        return short_text(compact, limit)
    return short_text(text, limit)


def percent(numerator: int, denominator: int) -> str:
    if denominator <= 0:
        return "0%"
    return f"{numerator / denominator:.1%}"
