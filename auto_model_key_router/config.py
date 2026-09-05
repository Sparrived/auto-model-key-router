from __future__ import annotations

import json
import os
import platform
import secrets
import time
from copy import deepcopy
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .visitor import VISITOR_API_KEY


CONFIG_VERSION = 4


def default_cache_dir() -> Path:
    system = platform.system().lower()
    if system == "windows":
        root = os.environ.get("LOCALAPPDATA") or os.environ.get("APPDATA")
        if root:
            return Path(root) / "AutoModelKeyRouter"
        return Path.home() / "AppData" / "Local" / "AutoModelKeyRouter"
    if system == "darwin":
        return Path.home() / "Library" / "Caches" / "AutoModelKeyRouter"
    root = os.environ.get("XDG_CACHE_HOME")
    if root:
        return Path(root) / "auto-model-key-router"
    return Path.home() / ".cache" / "auto-model-key-router"


def default_config_path() -> Path:
    return default_cache_dir() / "router-config.json"


LEGACY_CONFIG_PATH = Path("router-config.json")
DEFAULT_CONFIG_PATH = default_config_path()
UNIFIED_MODEL_ID = "unified-model"
UPSTREAM_ROUTE_MODES = ("openai", "anthropic", "responses", "images")
UPSTREAM_ROUTE_LABELS = {
    "openai": "OpenAI Chat",
    "anthropic": "Anthropic Messages",
    "responses": "OpenAI Responses",
    "images": "OpenAI Images",
}
UPSTREAM_ROUTE_DEFAULT_PATHS = {
    "openai": "v1/chat/completions",
    "anthropic": "v1/messages",
    "responses": "v1/responses",
    "images": "v1/images/generations",
}
UPSTREAM_ROUTE_MODE_ALIASES = {
    "chat": "openai",
    "chat_completions": "openai",
    "chat-completions": "openai",
    "openai_chat": "openai",
    "openai-chat": "openai",
    "messages": "anthropic",
    "anthropic_messages": "anthropic",
    "anthropic-messages": "anthropic",
    "codex": "responses",
    "image": "images",
    "img": "images",
    "dall-e": "images",
    "dalle": "images",
    "images/generations": "images",
    "image_generation": "images",
    "image-generation": "images",
}


def normalize_upstream_route_mode(value: Any) -> str:
    mode = str(value or "").strip().lower()
    mode = UPSTREAM_ROUTE_MODE_ALIASES.get(mode, mode)
    if mode not in UPSTREAM_ROUTE_MODES:
        raise ValueError(
            "upstream_routes 模式必须是 openai、anthropic、responses 或 images"
        )
    return mode


def normalize_upstream_route_path(mode: str, value: Any) -> str:
    mode = normalize_upstream_route_mode(mode)
    route = str(value or "").strip().replace("\\", "/")
    if not route:
        raise ValueError("upstream_routes 路径不能为空")
    if "://" in route:
        raise ValueError("upstream_routes 只能配置相对路径或路径前缀")
    if "?" in route or "#" in route:
        raise ValueError("upstream_routes 不能包含 query string 或 fragment")
    while "//" in route:
        route = route.replace("//", "/")
    route = route.strip("/")
    if not route:
        raise ValueError("upstream_routes 路径不能为空")
    endpoint = UPSTREAM_ROUTE_DEFAULT_PATHS[mode]
    if route == endpoint or route.endswith(f"/{endpoint}"):
        return route
    if route == "v1" or route.endswith("/v1"):
        return f"{route}/{endpoint.removeprefix('v1/')}"
    return f"{route}/{endpoint}"


def normalize_upstream_routes(raw: Any) -> dict[str, str]:
    if raw is None:
        return {}
    if not isinstance(raw, dict):
        raise ValueError("upstream_routes 必须是对象")
    routes: dict[str, str] = {}
    for raw_mode, raw_route in raw.items():
        if raw_route is None or not str(raw_route).strip():
            continue
        mode = normalize_upstream_route_mode(raw_mode)
        routes[mode] = normalize_upstream_route_path(mode, raw_route)
    return routes


def normalize_upstream_base_url(value: Any) -> str:
    base_url = str(value or "").strip().rstrip("/")
    if not base_url:
        raise ValueError("upstream_routes 上游 URL 不能为空")
    return base_url


def merge_upstream_routes_for_url(
    routes_by_url: dict[str, dict[str, str]],
    base_url: str,
    routes: dict[str, str],
) -> None:
    if not routes:
        return
    normalized_base_url = normalize_upstream_base_url(base_url)
    target = routes_by_url.setdefault(normalized_base_url, {})
    for mode, route_path in routes.items():
        existing = target.get(mode)
        if existing is not None and existing != route_path:
            raise ValueError(
                f"上游 URL {normalized_base_url} 的 {mode} 路由配置冲突"
            )
        target[mode] = route_path


def normalize_upstream_url_routes(raw: Any) -> dict[str, dict[str, str]]:
    if raw is None:
        return {}
    if not isinstance(raw, dict):
        raise ValueError("upstream_routes 必须是按上游 URL 分组的对象")
    routes_by_url: dict[str, dict[str, str]] = {}
    for raw_base_url, raw_routes in raw.items():
        routes = normalize_upstream_routes(raw_routes)
        if routes:
            routes_by_url[normalize_upstream_base_url(raw_base_url)] = routes
    return routes_by_url


def upstream_route_path(
    upstream_routes: dict[str, str] | None, mode: str
) -> str:
    mode = normalize_upstream_route_mode(mode)
    routes = upstream_routes or {}
    return routes.get(mode) or UPSTREAM_ROUTE_DEFAULT_PATHS[mode]


def default_metrics_db_path() -> str:
    return str(default_cache_dir() / "metrics.sqlite3")


def default_endpoint_capabilities_path() -> str:
    return str(default_cache_dir() / "endpoint-capabilities.json")


def default_log_file_path() -> str:
    return str(default_cache_dir() / "server.log")


def generate_local_api_key() -> str:
    return f"amkr_{secrets.token_urlsafe(32)}"


def empty_config_dict() -> dict[str, Any]:
    return {
        "config_version": CONFIG_VERSION,
        "host": "127.0.0.1",
        "port": 8000,
        "default_base_url": "https://api.openai.com",
        "upstream_routes": {},
        "request_timeout": 60,
        "stream_first_byte_timeout": 90,
        "stream_idle_timeout": 180,
        "max_retries": 2,
        "key_failure_threshold": 2,
        "key_cooldown_seconds": 60,
        "endpoint_capabilities_path": default_endpoint_capabilities_path(),
        "metrics_db_path": default_metrics_db_path(),
        "log_file_path": default_log_file_path(),
        "local_api_key": generate_local_api_key(),
        "providers": {},
        "models": {},
    }


def load_config_data(path: Path) -> dict[str, Any]:
    if path.exists():
        return json.loads(path.read_text(encoding="utf-8"))
    data = empty_config_dict()
    save_config_data(path, data)
    return data


def save_config_data(path: Path, data: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_path = path.with_name(f".{path.name}.{secrets.token_hex(6)}.tmp")
    try:
        temporary_path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        _replace_with_retry(temporary_path, path)
    finally:
        try:
            temporary_path.unlink()
        except OSError:
            pass


def _replace_with_retry(source: Path, target: Path, attempts: int = 4) -> None:
    """os.replace 偶发被 Windows 短暂占用（杀软扫描/句柄未释放）拒绝，
    属平台噪声：小退避重试后仍失败才向上抛。"""
    delay = 0.02
    for attempt in range(attempts):
        try:
            source.replace(target)
            return
        except OSError:
            if attempt == attempts - 1:
                raise
            time.sleep(delay)
            delay *= 2


def migrate_config_data(raw: dict[str, Any]) -> dict[str, Any]:
    # Migrations can restructure deeply nested provider/model blocks; always
    # operate on a private copy so callers keep their raw payload untouched.
    normalized = deepcopy(raw)
    unified_model = normalized.get("unified_model")
    if isinstance(unified_model, dict) and "default" not in unified_model:
        model = str(unified_model.get("model") or "").strip()
        if model:
            default: dict[str, Any] = {
                "primary": {"model": model, "key": unified_model.get("key")}
            }
            migrated_unified: dict[str, Any] = {"default": default}
            image_model = str(unified_model.get("image_model") or "").strip()
            if image_model:
                migrated_unified["image"] = {
                    "primary": {
                        "model": image_model,
                        "key": unified_model.get("image_key"),
                    }
                }
            normalized["unified_model"] = migrated_unified
    if "endpoint_capabilities_path" not in normalized and normalized.get(
        "key_state_path"
    ):
        normalized["endpoint_capabilities_path"] = normalized["key_state_path"]
    normalized.pop("key_state_path", None)
    version = int(normalized.get("config_version") or 0)
    if version == CONFIG_VERSION:
        if isinstance(normalized.get("models"), dict):
            _drop_legacy_pool_remnants(normalized)
        return normalized
    if version > CONFIG_VERSION:
        raise ValueError(
            f"配置文件版本 {version} 高于当前支持的 {CONFIG_VERSION}，请升级软件"
        )
    if version == 3:
        if not isinstance(normalized.get("models"), dict):
            return normalized
        migrated = _migrate_v3_to_v4(normalized)
        migrated.setdefault("config_version", CONFIG_VERSION)
        migrated["config_version"] = CONFIG_VERSION
        return migrated
    models = normalized.get("models")
    if not isinstance(models, list):
        return normalized
    if version not in {0, 1, 2}:
        return normalized
    # v1/v2 list layout -> v3 first, then flatten pools into key-targets (v4).
    migrated = normalized
    migrated["config_version"] = CONFIG_VERSION
    providers: dict[str, Any] = {}
    migrated_models: dict[str, Any] = {}
    upstream_routes = normalize_upstream_url_routes(normalized.get("upstream_routes"))

    for raw_model in models:
        if not isinstance(raw_model, dict):
            continue
        model_id = str(raw_model.get("id") or "").strip()
        if not model_id:
            continue
        raw_keys = raw_model.get("keys") if isinstance(raw_model.get("keys"), list) else []
        targets: list[dict[str, Any]] = []
        used_key_names: set[str] = set()
        for index, raw_key in enumerate(raw_keys, start=1):
            if not isinstance(raw_key, dict):
                continue
            key_name = str(raw_key.get("name") or f"{model_id}-{index}").strip()
            api_key = str(raw_key.get("api_key") or "").strip()
            if not key_name or not api_key:
                continue
            if key_name in used_key_names:
                raise ValueError(f"模型 {model_id} 的 key name 重复: {key_name}")
            used_key_names.add(key_name)
            base_url = normalize_upstream_base_url(
                raw_key.get("base_url")
                or raw.get("default_base_url")
                or "https://api.openai.com"
            )
            provider_id = _legacy_provider_id(base_url)
            provider = providers.setdefault(
                provider_id,
                {"base_url": base_url, "keys": {}},
            )
            provider.setdefault("base_url", base_url)
            provider_keys = provider.setdefault("keys", {})
            provider_keys[key_name] = {
                "api_key": api_key,
                "enabled": bool(raw_key.get("enabled", True)),
                "allow_visitor": bool(raw_key.get("allow_visitor", False)),
            }
            upstream_model = str(raw_key.get("upstream_model") or model_id)
            merge_upstream_routes_for_url(
                upstream_routes,
                base_url,
                normalize_upstream_routes(raw_key.get("upstream_routes")),
            )
            targets.append(
                {
                    "provider": provider_id,
                    "key": key_name,
                    "upstream_model": upstream_model,
                }
            )

        migrated_model: dict[str, Any] = {"targets": targets}
        for field_name in ("aliases", "routing_mode", "reasoning_effort", "native_first"):
            if field_name in raw_model:
                migrated_model[field_name] = raw_model[field_name]
        migrated_models[model_id] = migrated_model

    migrated["providers"] = providers
    migrated["models"] = migrated_models
    if upstream_routes:
        migrated["upstream_routes"] = upstream_routes
    else:
        migrated.pop("upstream_routes", None)
    return migrated


def _migrate_v3_to_v4(data: dict[str, Any]) -> dict[str, Any]:
    """Flatten v3 provider pools into key-level targets (v4).

    v3 kept one pool per provider as the container that grouped keys and held
    an enabled-model whitelist; model targets referenced ``provider/pool``.
    v4 drops the pool abstraction entirely: a model target references exactly
    one provider key, and a key serves whatever models bind it. Migration is
    idempotent and never fails on probe metadata that is only informational.
    """
    migrated = dict(data)
    migrated["config_version"] = CONFIG_VERSION
    providers_raw = migrated.get("providers")
    models_raw = migrated.get("models")
    if not isinstance(providers_raw, dict) or not isinstance(models_raw, dict):
        # Leave scalar/list leftovers for from_dict to reject clearly.
        migrated.pop("pools", None)
        return migrated

    keys_by_provider: dict[str, dict[str, Any]] = {}
    for provider in providers_raw.values():
        if not isinstance(provider, dict):
            continue
        raw_keys = provider.get("keys")
        if isinstance(raw_keys, dict):
            keys_by_provider.setdefault(str(provider.get("base_url") or ""), {})
            for key_name, key in raw_keys.items():
                if isinstance(key, dict):
                    keys_by_provider[str(provider.get("base_url") or "")].setdefault(
                        str(key_name), key
                    )
        # v3 probe metadata is per pool (a group of keys sharing a whitelist).
        # v4 caches probes per key; fold the legacy pool models into every key
        # of the pool conservatively so the TUI can offer bindings immediately.
        # A later per-key refresh replaces these with key-specific results.
        legacy = _legacy_probe_capabilities(provider)
        if legacy:
            for key in raw_keys.values():
                if isinstance(key, dict) and not isinstance(
                    key.get("capabilities"), dict
                ):
                    key["capabilities"] = legacy
        provider.pop("capabilities", None)
        provider.pop("available_models", None)
        provider.pop("key_models", None)
        provider.pop("routes_by_key", None)
        provider.pop("_probe_cache", None)

    for model_id, model in models_raw.items():
        if not isinstance(model, dict):
            continue
        targets = model.get("targets")
        if not isinstance(targets, list):
            model["targets"] = []
            continue
        rewritten: list[dict[str, Any]] = []
        for target in targets:
            if not isinstance(target, dict):
                continue
            provider_id = str(target.get("provider") or "").strip()
            pool_name = str(target.get("pool") or "").strip()
            upstream_model = str(target.get("upstream_model") or model_id)
            provider = providers_raw.get(provider_id)
            if not isinstance(provider, dict):
                # v3 parser would reject referencing an unknown provider; keep
                # that strictness during migration so nothing silently drops.
                raise ValueError(
                    f"模型 {model_id} 引用了未配置的供应商: {provider_id}"
                )
            pool_keys = _v3_pool_keys(provider, pool_name)
            # An existing v3 pool may intentionally have no keys (for example
            # after a key was removed).  It is a valid empty target and must be
            # filtered out by the whitelist semantics below; only a genuinely
            # missing pool is an invalid reference.
            if pool_name and not _v3_pool_exists(provider, pool_name):
                raise ValueError(
                    f"模型 {model_id} 引用了供应商 {provider_id} 不存在的 pool: {pool_name}"
                )
            # v3 解析器按池白名单过滤 target（upstream_model 必须在
            # pool.models 里，空数组 = 未启用任何模型、同样过滤）。迁移必须
            # 复刻该语义：白名单键存在即过滤（含空），键缺失才不设限。
            # 不这样做会让 v3 中被静默丢弃的死引用在升级后意外复活。
            if _v3_pool_has_whitelist(provider, pool_name):
                pool_models = _v3_pool_enabled_models(provider, pool_name)
                if upstream_model not in pool_models:
                    continue
            for key_name in pool_keys:
                rewritten.append(
                    {
                        "provider": provider_id,
                        "key": key_name,
                        "upstream_model": upstream_model,
                    }
                )
        model["targets"] = rewritten

    for provider in providers_raw.values():
        if isinstance(provider, dict):
            provider.pop("pools", None)
    return migrated


def _v3_pool_exists(provider: dict[str, Any], pool_name: str) -> bool:
    pools = provider.get("pools") if isinstance(provider, dict) else None
    return isinstance(pools, dict) and bool(pool_name) and pool_name in pools


def _v3_pool_keys(provider: dict[str, Any], pool_name: str) -> list[str]:
    pools = provider.get("pools") if isinstance(provider, dict) else None
    if not isinstance(pools, dict) or not pool_name:
        return []
    pool = pools.get(pool_name)
    keys = pool.get("keys") if isinstance(pool, dict) else None
    if not isinstance(keys, list):
        return []
    return [str(key_name) for key_name in keys]


def _v3_pool_has_whitelist(provider: dict[str, Any], pool_name: str) -> bool:
    """True when the v3 pool carries an explicit ``models`` whitelist key.

    An explicit whitelist -- even an empty one -- means the pool only serves
    the listed upstream models; v3 dropped every other target reference.
    """
    pools = provider.get("pools") if isinstance(provider, dict) else None
    if not isinstance(pools, dict) or not pool_name:
        return False
    pool = pools.get(pool_name)
    return isinstance(pool, dict) and isinstance(pool.get("models"), list)


def _v3_pool_enabled_models(provider: dict[str, Any], pool_name: str) -> set[str]:
    pools = provider.get("pools") if isinstance(provider, dict) else None
    if not isinstance(pools, dict) or not pool_name:
        return set()
    pool = pools.get(pool_name)
    models = pool.get("models") if isinstance(pool, dict) else None
    if not isinstance(models, list):
        return set()
    return {str(model_id) for model_id in models if str(model_id)}


def _legacy_probe_capabilities(provider: dict[str, Any]) -> dict[str, Any] | None:
    """Extract legacy (v3 pool / early v4 provider-level) probe metadata.

    v3 kept probe info per pool; early v4 wrote it at provider level. Both are
    folded into a single conservative {models, checked_at} block that gets
    copied onto every key of the provider during migration. Returns None when
    there is nothing to preserve.
    """
    merged_models: set[str] = set()
    merged_checked: Any = None
    capabilities = provider.get("capabilities")
    if isinstance(capabilities, dict):
        for model_id in capabilities.get("models", []) or []:
            merged_models.add(str(model_id))
        merged_checked = capabilities.get("checked_at")
    pools = provider.get("pools")
    if isinstance(pools, dict):
        for pool in pools.values():
            if not isinstance(pool, dict):
                continue
            for model_id in pool.get("available_models", []) or []:
                merged_models.add(str(model_id))
            for model_id in pool.get("all_available_models", []) or []:
                merged_models.add(str(model_id))
            for model_id in pool.get("models", []) or []:
                merged_models.add(str(model_id))
            checked_at = pool.get("checked_at")
            if checked_at and not merged_checked:
                merged_checked = checked_at
    if not merged_models:
        return None
    result: dict[str, Any] = {"models": sorted(merged_models)}
    if merged_checked:
        result["checked_at"] = merged_checked
    return result


def _drop_legacy_pool_remnants(data: dict[str, Any]) -> None:
    """Idempotent safety net: never let pools reappear in v4 payloads.

    Also promotes any leftover provider-level capabilities block (written by
    early v4 builds) onto every key that has no per-key probe cache yet, then
    drops the provider-level field so the disk layout stays key-scoped.
    """
    providers = data.get("providers")
    if not isinstance(providers, dict):
        return
    for provider in providers.values():
        if not isinstance(provider, dict):
            continue
        provider.pop("pools", None)
        provider.pop("available_models", None)
        capabilities = provider.pop("capabilities", None)
        if not isinstance(capabilities, dict) or not capabilities.get("models"):
            continue
        raw_keys = provider.get("keys")
        if not isinstance(raw_keys, dict):
            continue
        folded: dict[str, Any] = {"models": capabilities.get("models")}
        if capabilities.get("checked_at"):
            folded["checked_at"] = capabilities["checked_at"]
        if capabilities.get("errors"):
            folded["errors"] = capabilities["errors"]
        if capabilities.get("route_status"):
            folded["route_status"] = capabilities["route_status"]
        for key in raw_keys.values():
            if isinstance(key, dict) and not isinstance(key.get("capabilities"), dict):
                key["capabilities"] = dict(folded)


def _legacy_provider_id(base_url: str) -> str:
    provider_id = base_url.replace("https://", "").replace("http://", "")
    provider_id = provider_id.strip("/").replace("/", "-").replace(":", "-")
    return provider_id or "default"


def _resolve_config_path(path: str | Path | None = None) -> Path:
    if path is not None:
        return Path(path)
    env_path = os.environ.get("AMKR_CONFIG")
    if env_path:
        return Path(env_path)
    default_path = default_config_path()
    if default_path.exists():
        return default_path
    if LEGACY_CONFIG_PATH.exists():
        default_path.parent.mkdir(parents=True, exist_ok=True)
        default_path.write_text(LEGACY_CONFIG_PATH.read_text(encoding="utf-8"), encoding="utf-8")
        return default_path
    return default_path


@dataclass(frozen=True)
class KeyConfig:
    name: str
    api_key: str
    base_url: str
    enabled: bool = True
    allow_visitor: bool = False
    upstream_routes: dict[str, str] = field(default_factory=dict, repr=False, compare=False)
    provider: str | None = None
    upstream_model: str | None = None
    pool: str | None = None

    def __post_init__(self) -> None:
        object.__setattr__(
            self,
            "upstream_routes",
            normalize_upstream_routes(self.upstream_routes),
        )


@dataclass(frozen=True)
class ModelConfig:
    id: str
    keys: tuple[KeyConfig, ...]
    aliases: tuple[str, ...] = ()
    routing_mode: str = "round_robin"
    reasoning_effort: str | None = None
    native_first: bool = True


@dataclass(frozen=True)
class ProviderKeyConfig:
    name: str
    api_key: str
    enabled: bool = True
    allow_visitor: bool = False
    capabilities: dict[str, Any] | None = None


@dataclass(frozen=True)
class ProviderPoolConfig:
    name: str
    keys: tuple[str, ...]
    models: tuple[str, ...] = ()


@dataclass(frozen=True)
class ProviderConfig:
    id: str
    base_url: str
    keys: tuple[ProviderKeyConfig, ...]
    routes: dict[str, str] = field(default_factory=dict)
    capabilities: dict[str, Any] | None = None

    def __post_init__(self) -> None:
        object.__setattr__(self, "base_url", normalize_upstream_base_url(self.base_url))
        object.__setattr__(self, "routes", normalize_upstream_routes(self.routes))


@dataclass(frozen=True)
class RouteTarget:
    model: str
    key: str | None = None


@dataclass(frozen=True)
class RoutePlan:
    primary: RouteTarget
    fallback: RouteTarget | None = None


@dataclass(frozen=True, init=False)
class UnifiedModelConfig:
    default: RoutePlan
    image: RoutePlan | None = None

    def __init__(
        self,
        default: RoutePlan | None = None,
        image: RoutePlan | None = None,
        *,
        model: str | None = None,
        key: str | None = None,
        image_model: str | None = None,
        image_key: str | None = None,
    ) -> None:
        # Legacy constructor compatibility; parsed configs always use RoutePlan.
        if default is None:
            if not model:
                raise ValueError("unified_model.default.primary.model 不能为空")
            default = RoutePlan(RouteTarget(model, key))
        if image is None and image_model:
            image = RoutePlan(RouteTarget(image_model, image_key))
        object.__setattr__(self, "default", default)
        object.__setattr__(self, "image", image)

    @property
    def model(self) -> str:
        return self.default.primary.model

    @property
    def key(self) -> str | None:
        return self.default.primary.key

    @property
    def image_model(self) -> str | None:
        return self.image.primary.model if self.image else None

    @property
    def image_key(self) -> str | None:
        return self.image.primary.key if self.image else None


@dataclass(frozen=True)
class RouterConfig:
    host: str
    port: int
    request_timeout: float
    max_retries: int
    key_failure_threshold: int
    key_cooldown_seconds: float
    endpoint_capabilities_path: str
    metrics_db_path: str
    log_file_path: str
    local_api_key: str
    models: tuple[ModelConfig, ...]
    stream_first_byte_timeout: float = 90
    stream_idle_timeout: float = 180
    providers: tuple[ProviderConfig, ...] = ()
    upstream_routes: dict[str, dict[str, str]] = field(default_factory=dict)
    unified_model: UnifiedModelConfig | None = None
    reasoning_effort_by_model: dict[str, str] = field(init=False, repr=False)

    def __post_init__(self) -> None:
        upstream_routes = normalize_upstream_url_routes(self.upstream_routes)
        for provider in self.providers:
            merge_upstream_routes_for_url(
                upstream_routes, provider.base_url, provider.routes
            )
        for model in self.models:
            for key in model.keys:
                merge_upstream_routes_for_url(
                    upstream_routes, key.base_url, key.upstream_routes
                )
        object.__setattr__(
            self,
            "upstream_routes",
            upstream_routes,
        )
        object.__setattr__(
            self,
            "reasoning_effort_by_model",
            {model.id: model.reasoning_effort for model in self.models if model.reasoning_effort},
        )

    @classmethod
    def load(cls, path: str | Path | None = None) -> "RouterConfig":
        config_path = _resolve_config_path(path)
        raw = load_config_data(config_path)
        migrated = migrate_config_data(raw)
        if migrated != raw:
            raw = migrated
            save_config_data(config_path, raw)
        if not raw.get("local_api_key"):
            raw["local_api_key"] = generate_local_api_key()
            save_config_data(config_path, raw)
        return cls.from_dict(raw)

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "RouterConfig":
        raw = migrate_config_data(raw)
        if int(raw.get("config_version") or 0) != CONFIG_VERSION:
            raise ValueError(f"配置文件必须是 config_version {CONFIG_VERSION}")
        models: list[ModelConfig] = []
        providers: list[ProviderConfig] = []
        default_routing_mode = str(raw.get("routing_mode") or "round_robin")
        upstream_routes: dict[str, dict[str, str]] = {}
        provider_keys: dict[tuple[str, str], tuple[ProviderConfig, ProviderKeyConfig]] = {}
        raw_providers = raw.get("providers")
        if isinstance(raw_providers, dict):
            for provider_id, provider in raw_providers.items():
                if not isinstance(provider, dict):
                    continue
                keys: list[ProviderKeyConfig] = []
                raw_keys = provider.get("keys")
                if not isinstance(raw_keys, dict):
                    raw_keys = {}
                raw_key_items = raw_keys.items()
                for key_name, key in raw_key_items:
                    if not isinstance(key, dict):
                        raise ValueError(
                            f"供应商 {provider_id} 的 key {key_name} 必须是对象"
                        )
                    raw_key_capabilities = key.get("capabilities")
                    key_config = ProviderKeyConfig(
                        name=str(key_name),
                        api_key=str(key["api_key"]),
                        enabled=bool(key.get("enabled", True)),
                        allow_visitor=bool(key.get("allow_visitor", False)),
                        capabilities=(
                            raw_key_capabilities
                            if isinstance(raw_key_capabilities, dict)
                            else None
                        ),
                    )
                    keys.append(key_config)
                raw_capabilities = provider.get("capabilities")
                provider_config = ProviderConfig(
                    id=str(provider_id),
                    base_url=str(provider.get("base_url") or raw.get("default_base_url") or "https://api.openai.com"),
                    keys=tuple(keys),
                    routes=normalize_upstream_routes(provider.get("routes")),
                    capabilities=(
                        raw_capabilities
                        if isinstance(raw_capabilities, dict)
                        else None
                    ),
                )
                providers.append(provider_config)
                for key_config in provider_config.keys:
                    provider_keys[(provider_config.id, key_config.name)] = (
                        provider_config,
                        key_config,
                    )

        raw_models = raw.get("models", {})
        if not isinstance(raw_models, dict):
            raise ValueError("config_version 4 的 models 必须是对象")
        raw_model_items = raw_models.items()
        for raw_model_id, model in raw_model_items:
            model_keys: list[KeyConfig] = []
            used_model_key_names: set[str] = set()

            def model_key_name(base_name: str, qualifier: str | None = None) -> str:
                name = base_name
                if name not in used_model_key_names:
                    used_model_key_names.add(name)
                    return name
                qualified = f"{qualifier}-{base_name}" if qualifier else base_name
                name = qualified
                suffix = 2
                while name in used_model_key_names:
                    name = f"{qualified}-{suffix}"
                    suffix += 1
                used_model_key_names.add(name)
                return name

            raw_targets = model.get("targets")
            if isinstance(raw_targets, list):
                for target in raw_targets:
                    if not isinstance(target, dict):
                        continue
                    provider_id = str(target.get("provider") or "").strip()
                    key_name = str(target.get("key") or "").strip()
                    upstream_model = str(target.get("upstream_model") or raw_model_id)
                    target_enabled = bool(target.get("enabled", True))
                    provider_key = provider_keys.get((provider_id, key_name))
                    if provider_key is None:
                        raise ValueError(
                            f"模型 {raw_model_id} 引用了供应商 {provider_id} 不存在的 key: {key_name}"
                        )
                    provider_config, key_config = provider_key
                    model_keys.append(
                        KeyConfig(
                            name=model_key_name(
                                str(target.get("name") or key_config.name),
                                provider_id,
                            ),
                            api_key=key_config.api_key,
                            base_url=provider_config.base_url,
                            provider=provider_id,
                            upstream_model=upstream_model,
                            enabled=key_config.enabled and target_enabled,
                            allow_visitor=key_config.allow_visitor,
                        )
                    )
            keys = tuple(model_keys)
            aliases = tuple(str(alias) for alias in model.get("aliases", []) if str(alias))
            routing_mode = str(model.get("routing_mode") or default_routing_mode)
            reasoning_effort = str(model.get("reasoning_effort") or "").strip() or None
            if reasoning_effort in {"default", "downstream"}:
                reasoning_effort = None
            native_first = bool(model.get("native_first", True))
            model_id = str(model.get("id") or raw_model_id)
            models.append(ModelConfig(id=model_id, keys=keys, aliases=aliases, routing_mode=routing_mode, reasoning_effort=reasoning_effort, native_first=native_first))

        unified_model = None
        raw_unified_model = raw.get("unified_model")
        if raw_unified_model is not None:
            if not isinstance(raw_unified_model, dict):
                raise ValueError("unified_model 必须是对象")
            model_ids_by_name = {
                name: model.id
                for model in models
                for name in (model.id, *model.aliases)
            }

            def parse_target(value: Any, field_name: str) -> RouteTarget:
                if not isinstance(value, dict):
                    raise ValueError(f"{field_name} 必须是对象")
                model_name = str(value.get("model") or "").strip()
                model_id = model_ids_by_name.get(model_name)
                if model_id is None:
                    raise ValueError(f"{field_name} 引用了未配置的模型: {model_name}")
                key_name = str(value.get("key") or "").strip() or None
                return RouteTarget(model_id, key_name)

            def parse_plan(value: Any, field_name: str) -> RoutePlan:
                if not isinstance(value, dict):
                    raise ValueError(f"{field_name} 必须是对象")
                primary = parse_target(value.get("primary"), f"{field_name}.primary")
                fallback = (
                    parse_target(value["fallback"], f"{field_name}.fallback")
                    if value.get("fallback") is not None
                    else None
                )
                return RoutePlan(primary, fallback)

            default_plan = parse_plan(raw_unified_model.get("default"), "unified_model.default")
            image_plan = (
                parse_plan(raw_unified_model["image"], "unified_model.image")
                if raw_unified_model.get("image") is not None
                else None
            )
            unified_model = UnifiedModelConfig(default_plan, image_plan)

        config = cls(
            host=str(raw.get("host", "127.0.0.1")),
            port=int(raw.get("port", 8000)),
            request_timeout=float(raw.get("request_timeout", 60)),
            stream_first_byte_timeout=float(
                raw.get("stream_first_byte_timeout", 90)
            ),
            stream_idle_timeout=float(raw.get("stream_idle_timeout", 180)),
            max_retries=int(raw.get("max_retries", 2)),
            key_failure_threshold=max(1, int(raw.get("key_failure_threshold", 2))),
            key_cooldown_seconds=max(0.0, float(raw.get("key_cooldown_seconds", 60))),
            endpoint_capabilities_path=str(
                raw.get("endpoint_capabilities_path")
                or raw.get("key_state_path")
                or default_endpoint_capabilities_path()
            ),
            metrics_db_path=str(raw.get("metrics_db_path") or default_metrics_db_path()),
            log_file_path=str(raw.get("log_file_path") or default_log_file_path()),
            local_api_key=str(raw.get("local_api_key", "")),
            models=tuple(models),
            providers=tuple(providers),
            upstream_routes=upstream_routes,
            unified_model=unified_model,
        )
        config.validate()
        return config

    def validate(self) -> None:
        if self.stream_first_byte_timeout <= 0:
            raise ValueError("stream_first_byte_timeout 必须大于 0")
        if self.stream_idle_timeout <= 0:
            raise ValueError("stream_idle_timeout 必须大于 0")
        if self.local_api_key == VISITOR_API_KEY:
            raise ValueError(f"local_api_key 不能使用保留的访客 key: {VISITOR_API_KEY}")

        model_names: set[str] = set()
        model_ids_by_name: dict[str, str] = {}
        models_by_id: dict[str, ModelConfig] = {}
        for model in self.models:
            if not model.id:
                raise ValueError("模型 id 不能为空")
            if model.routing_mode not in {"priority", "round_robin", "only_first"}:
                raise ValueError(f"模型 {model.id} 的 routing_mode 必须是 priority、round_robin 或 only_first")
            if model.reasoning_effort is not None and model.reasoning_effort not in {"none", "minimal", "low", "medium", "high", "xhigh", "max"}:
                raise ValueError(f"模型 {model.id} 的 reasoning_effort 必须是 none、minimal、low、medium、high、xhigh 或 max")
            for name in (model.id, *model.aliases):
                if name in model_names:
                    raise ValueError(f"模型名称重复: {name}")
                model_names.add(name)
                model_ids_by_name[name] = model.id
            models_by_id[model.id] = model
            key_names: set[str] = set()
            for key in model.keys:
                if not key.name:
                    raise ValueError(f"模型 {model.id} 存在空 key name")
                if key.name in key_names:
                    raise ValueError(f"模型 {model.id} 的 key name 重复: {key.name}")
                key_names.add(key.name)
                if not key.api_key:
                    raise ValueError(f"模型 {model.id} 存在空 api_key")
                if not key.base_url.startswith(("http://", "https://")):
                    raise ValueError(f"模型 {model.id} 的 base_url {key.base_url} 必须以 http:// 或 https:// 开头")

        for base_url, routes in self.upstream_routes.items():
            if not base_url.startswith(("http://", "https://")):
                raise ValueError(
                    f"upstream_routes 的上游URL {base_url} 必须以 http:// 或 https:// 开头"
                )
            for route_mode, route_path in routes.items():
                normalize_upstream_route_path(route_mode, route_path)

        if self.unified_model is None:
            return
        if UNIFIED_MODEL_ID in model_names:
            raise ValueError(f"启用 unified_model 时，模型 ID 和别名不能使用保留名称: {UNIFIED_MODEL_ID}")
        for plan_name, plan in (("default", self.unified_model.default), ("image", self.unified_model.image)):
            if plan is None:
                continue
            if plan.fallback and plan.primary.model == plan.fallback.model:
                raise ValueError(f"unified_model.{plan_name} 的 primary 和 fallback 不能引用同一模型")
            for target_name, target in (("primary", plan.primary), ("fallback", plan.fallback)):
                if target is None:
                    continue
                target_model = models_by_id.get(target.model)
                if target_model is None:
                    raise ValueError(f"unified_model.{plan_name}.{target_name} 引用了未配置的模型: {target.model}")
                if target.key is not None and not any(
                    key.name == target.key and key.enabled for key in target_model.keys
                ):
                    raise ValueError(f"模型 {target.model} 未配置可用 key: {target.key}")

    def configured_model_id(self, model_name: str) -> str | None:
        for model in self.models:
            if model_name == model.id or model_name in model.aliases:
                return model.id
        return None

    def native_first_for_model(self, model_id: str) -> bool:
        for model in self.models:
            if model.id == model_id:
                return model.native_first
        return True

    def upstream_routes_for_base_url(self, base_url: str) -> dict[str, str]:
        return dict(self.upstream_routes.get(normalize_upstream_base_url(base_url), {}))


