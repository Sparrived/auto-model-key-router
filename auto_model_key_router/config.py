from __future__ import annotations

import json
import os
import platform
import secrets
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .visitor import VISITOR_API_KEY


CONFIG_VERSION = 3


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
        temporary_path.replace(path)
    finally:
        try:
            temporary_path.unlink()
        except OSError:
            pass


def migrate_config_data(raw: dict[str, Any]) -> dict[str, Any]:
    normalized = dict(raw)
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
    if int(normalized.get("config_version") or 0) == CONFIG_VERSION and isinstance(
        normalized.get("models"), dict
    ):
        _merge_legacy_model_named_pools(normalized)
        return normalized
    models = normalized.get("models")
    if not isinstance(models, list):
        return normalized

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
                {"base_url": base_url, "keys": {}, "pools": {}},
            )
            provider.setdefault("base_url", base_url)
            provider_keys = provider.setdefault("keys", {})
            provider_keys[key_name] = {
                "api_key": api_key,
                "enabled": bool(raw_key.get("enabled", True)),
                "allow_visitor": bool(raw_key.get("allow_visitor", False)),
            }
            provider_pools = provider.setdefault("pools", {})
            pool_name = model_id
            pool = provider_pools.setdefault(pool_name, {"keys": []})
            pool_keys = pool.setdefault("keys", [])
            if key_name not in pool_keys:
                pool_keys.append(key_name)
            upstream_model = str(raw_key.get("upstream_model") or model_id)
            pool_models = pool.setdefault("models", [])
            if upstream_model not in pool_models:
                pool_models.append(upstream_model)
            merge_upstream_routes_for_url(
                upstream_routes,
                base_url,
                normalize_upstream_routes(raw_key.get("upstream_routes")),
            )
            targets.append(
                {
                    "provider": provider_id,
                    "pool": pool_name,
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


def _merge_legacy_model_named_pools(data: dict[str, Any]) -> None:
    """Repair v3 data from clients that created one pool per model."""
    providers = data.get("providers")
    models = data.get("models")
    if not isinstance(providers, dict) or not isinstance(models, dict):
        return

    model_ids = {str(model_id) for model_id in models}
    for provider in providers.values():
        pools = provider.get("pools") if isinstance(provider, dict) else None
        if not isinstance(pools, dict):
            continue
        legacy_pools = {}
        for name, pool in pools.items():
            pool_models = pool.get("models") if isinstance(pool, dict) else None
            if (
                str(name) in model_ids
                and isinstance(pool, dict)
                and isinstance(pool.get("keys"), list)
                and isinstance(pool_models, list)
                and set(map(str, pool_models)) <= {str(name)}
            ):
                legacy_pools[str(name)] = pool

        key_to_pool: dict[str, str] = {}
        merged: dict[str, str] = {}
        for pool_name, pool in legacy_pools.items():
            keys = [str(key) for key in pool["keys"]]
            existing_pool = next((key_to_pool[key] for key in keys if key in key_to_pool), None)
            if existing_pool is None:
                for key in keys:
                    key_to_pool[key] = pool_name
                continue
            merged[pool_name] = existing_pool
            kept = pools[existing_pool]
            kept["keys"] = list(dict.fromkeys([*kept["keys"], *pool["keys"]]))
            kept["models"] = list(dict.fromkeys([*kept.get("models", []), *pool.get("models", [])]))
            for key in keys:
                key_to_pool[key] = existing_pool
            pools.pop(pool_name, None)

        if not merged:
            continue
        for model in models.values():
            targets = model.get("targets") if isinstance(model, dict) else None
            if not isinstance(targets, list):
                continue
            for target in targets:
                if isinstance(target, dict) and target.get("pool") in merged:
                    target["pool"] = merged[target["pool"]]


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
    pools: tuple[ProviderPoolConfig, ...] = ()
    routes: dict[str, str] = field(default_factory=dict)

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
        provider_pools: dict[
            tuple[str, str],
            tuple[ProviderConfig, tuple[ProviderKeyConfig, ...], frozenset[str]],
        ] = {}
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
                    key_config = ProviderKeyConfig(
                        name=str(key_name),
                        api_key=str(key["api_key"]),
                        enabled=bool(key.get("enabled", True)),
                        allow_visitor=bool(key.get("allow_visitor", False)),
                    )
                    keys.append(key_config)
                key_configs_by_name = {key_config.name: key_config for key_config in keys}
                pools: list[ProviderPoolConfig] = []
                raw_pools = provider.get("pools")
                if not isinstance(raw_pools, dict):
                    raw_pools = {}
                raw_pool_items = raw_pools.items()
                key_pool_names: dict[str, str] = {}
                for pool_name, pool in raw_pool_items:
                    if isinstance(pool, dict):
                        pool_keys = pool.get("keys")
                        pool_models = pool.get("models", [])
                    else:
                        pool_keys = pool
                        pool_models = []
                    if not isinstance(pool_keys, list):
                        raise ValueError(f"供应商 {provider_id} 的 pool {pool_name} keys 必须是数组")
                    if not isinstance(pool_models, list):
                        raise ValueError(f"供应商 {provider_id} 的 pool {pool_name} models 必须是数组")
                    pool_key_names = tuple(str(key_name) for key_name in pool_keys)
                    missing_keys = [key_name for key_name in pool_key_names if key_name not in key_configs_by_name]
                    if missing_keys:
                        raise ValueError(
                            f"供应商 {provider_id} 的 pool {pool_name} 引用了未配置的 key: {', '.join(missing_keys)}"
                        )
                    for key_name in pool_key_names:
                        previous_pool = key_pool_names.get(key_name)
                        if previous_pool is not None:
                            raise ValueError(
                                f"供应商 {provider_id} 的 key {key_name} 同时属于多个模型池: "
                                f"{previous_pool}, {pool_name}。请运行 amkr 交互修复"
                            )
                        key_pool_names[key_name] = str(pool_name)
                    pools.append(
                        ProviderPoolConfig(
                            str(pool_name),
                            pool_key_names,
                            tuple(str(model_id) for model_id in pool_models if str(model_id)),
                        )
                    )
                missing_pool_keys = [
                    key_name
                    for key_name in key_configs_by_name
                    if key_name not in key_pool_names
                ]
                if missing_pool_keys:
                    raise ValueError(
                        f"供应商 {provider_id} 的 key {missing_pool_keys[0]} 未加入模型池。"
                        "请运行 amkr 交互修复"
                    )
                provider_config = ProviderConfig(
                    id=str(provider_id),
                    base_url=str(provider.get("base_url") or raw.get("default_base_url") or "https://api.openai.com"),
                    keys=tuple(keys),
                    pools=tuple(pools),
                    routes=normalize_upstream_routes(provider.get("routes")),
                )
                providers.append(provider_config)
                for key_config in provider_config.keys:
                    provider_keys[(provider_config.id, key_config.name)] = (
                        provider_config,
                        key_config,
                    )
                for pool_config in provider_config.pools:
                    provider_pools[(provider_config.id, pool_config.name)] = (
                        provider_config,
                        tuple(key_configs_by_name[key_name] for key_name in pool_config.keys),
                        frozenset(pool_config.models),
                    )

        raw_models = raw.get("models", {})
        if not isinstance(raw_models, dict):
            raise ValueError("config_version 3 的 models 必须是对象")
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
                    pool_name = str(target.get("pool") or "").strip()
                    key_name = str(target.get("key") or "").strip()
                    upstream_model = str(target.get("upstream_model") or raw_model_id)
                    target_enabled = bool(target.get("enabled", True))
                    if pool_name:
                        provider_pool = provider_pools.get((provider_id, pool_name))
                        if provider_pool is None:
                            raise ValueError(
                                f"模型 {raw_model_id} 引用了未配置的供应商 pool: {provider_id}/{pool_name}"
                            )
                        provider_config, pool_keys, pool_models = provider_pool
                        if upstream_model not in pool_models:
                            continue
                        for key_config in pool_keys:
                            model_keys.append(
                                KeyConfig(
                                    name=model_key_name(
                                        str(target.get("name") or key_config.name),
                                        pool_name,
                                    ),
                                    api_key=key_config.api_key,
                                    base_url=provider_config.base_url,
                                    provider=provider_id,
                                    upstream_model=upstream_model,
                                    pool=pool_name,
                                    enabled=key_config.enabled and target_enabled,
                                    allow_visitor=key_config.allow_visitor,
                                )
                            )
                        continue
                    raise ValueError(
                        f"模型 {raw_model_id} target 必须引用 provider/pool，不再支持 provider/key: {provider_id}/{key_name}"
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
            if model.reasoning_effort is not None and model.reasoning_effort not in {"none", "minimal", "low", "medium", "high", "xhigh"}:
                raise ValueError(f"模型 {model.id} 的 reasoning_effort 必须是 none、minimal、low、medium、high 或 xhigh")
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


