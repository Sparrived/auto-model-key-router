from __future__ import annotations

import hmac


VISITOR_API_KEY = "amkr-visitor"

try:
    import itsdangerous as _visitor_dependency
except ModuleNotFoundError:
    _visitor_dependency = None


# Python installers do not record which extras were requested. The dependency
# installed only by the visitor extra is therefore the runtime feature marker.
VISITOR_FEATURE_AVAILABLE = _visitor_dependency is not None


def visitor_feature_available() -> bool:
    return VISITOR_FEATURE_AVAILABLE


def is_visitor_api_key(api_key: str) -> bool:
    return VISITOR_FEATURE_AVAILABLE and hmac.compare_digest(api_key, VISITOR_API_KEY)
