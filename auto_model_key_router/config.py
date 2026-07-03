from __future__ import annotations

import json
import os
import platform
import secrets
from copy import deepcopy
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


def default_key_state_path() -> str:
    return str(default_cache_dir() / "key-state.json")


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
        "max_retries": 2,
        "key_failure_threshold": 2,
        "key_cooldown_seconds": 60,
        "key_state_path": default_key_state_path(),
        "upstream_health_check_interval": 30,
        "metrics_db_path": default_metrics_db_path(),
        "log_file_path": default_log_file_path(),
        "local_api_key": generate_local_api_key(),
        "providers": {},
        "models": [],
    }


def is_legacy_config_data(data: dict[str, Any]) -> bool:
    if int(data.get("config_version") or 1) < CONFIG_VERSION:
        return True
    models = data.get("models")
    return isinstance(models, list)


def migrate_config_data(data: dict[str, Any]) -> dict[str, Any]:
    if not is_legacy_config_data(data):
        return data
    migrated = dict(data)
    providers: dict[str, Any] = {}
    models: dict[str, Any] = {}
    default_base_url = str(data.get("default_base_url") or "https://api.openai.com")
    top_routes = normalize_upstream_url_routes(data.get("upstream_routes"))
    for base_url in top_routes:
        if not base_url.startswith(("http://", "https://")):
            raise ValueError(
                f"upstream_routes 的上游URL {base_url} 必须以 http:// 或 https:// 开头"
            )
    provider_names_by_base_url: dict[str, str] = {}

    def provider_name_for(base_url: str) -> str:
        normalized_base_url = normalize_upstream_base_url(base_url)
        existing = provider_names_by_base_url.get(normalized_base_url)
        if existing:
            return existing
        if normalized_base_url == normalize_upstream_base_url(default_base_url):
            base_name = "default"
        else:
            host = normalized_base_url.split("://", 1)[-1].split("/", 1)[0]
            base_name = "".join(
                ch.lower() if ch.isalnum() else "-" for ch in host
            ).strip("-") or "provider"
        name = base_name
        suffix = 2
        while name in providers:
            name = f"{base_name}-{suffix}"
            suffix += 1
        providers[name] = {
            "base_url": normalized_base_url,
            "routes": dict(top_routes.get(normalized_base_url, {})),
            "keys": {},
            "pools": {},
        }
        provider_names_by_base_url[normalized_base_url] = name
        return name

    def migrated_key_name(
        provider: dict[str, Any], desired_name: str, key_data: dict[str, Any], model_id: str
    ) -> str:
        keys = provider["keys"]
        existing = keys.get(desired_name)
        if existing is None or existing == key_data:
            return desired_name
        base_name = f"{model_id}-{desired_name}".strip("-")
        name = base_name
        suffix = 2
        while name in keys and keys[name] != key_data:
            name = f"{base_name}-{suffix}"
            suffix += 1
        return name

    def merge_provider_routes(provider: dict[str, Any], routes: dict[str, str]) -> None:
        provider_routes = provider["routes"]
        for mode, route_path in routes.items():
            existing = provider_routes.get(mode)
            if existing is not None and existing != route_path:
                raise ValueError(
                    f"供应商 {provider['base_url']} 的 {mode} 路由配置冲突"
                )
            provider_routes[mode] = route_path

    raw_models = data.get("models")
    if isinstance(raw_models, dict):
        providers = deepcopy(data.get("providers")) if isinstance(data.get("providers"), dict) else {}
        models = deepcopy(raw_models)
        for provider_id, provider in providers.items():
            if not isinstance(provider, dict):
                continue
            keys = provider.get("keys") if isinstance(provider.get("keys"), dict) else {}
            pools = provider.setdefault("pools", {})
            if not isinstance(pools, dict):
                pools = {}
                provider["pools"] = pools
            if keys and not pools:
                pools["default"] = {"keys": list(keys)}
        for model in models.values():
            if not isinstance(model, dict):
                continue
            targets = model.get("targets")
            if not isinstance(targets, list):
                continue
            for target in targets:
                if not isinstance(target, dict):
                    continue
                provider_id = str(target.get("provider") or "")
                key_name = str(target.get("key") or "")
                if target.get("pool") or not provider_id or not key_name:
                    continue
                provider = providers.get(provider_id)
                if not isinstance(provider, dict):
                    continue
                pools = provider.setdefault("pools", {})
                pool_name = key_name
                existing = pools.get(pool_name)
                if isinstance(existing, dict):
                    pool_keys = existing.setdefault("keys", [])
                    if key_name not in pool_keys:
                        pool_keys.append(key_name)
                else:
                    pools[pool_name] = {"keys": [key_name]}
                target["pool"] = pool_name
                target.pop("key", None)
        migrated["providers"] = providers
        migrated["models"] = models
        migrated["config_version"] = CONFIG_VERSION
        return migrated
    if not isinstance(raw_models, list):
        raw_models = []

    for raw_model in raw_models:
        if not isinstance(raw_model, dict):
            continue
        model_id = str(raw_model.get("id") or "").strip()
        if not model_id:
            continue
        model_data: dict[str, Any] = {
            "targets": [],
        }
        for field_name in ("aliases", "routing_mode", "reasoning_effort", "native_first"):
            if field_name in raw_model:
                model_data[field_name] = raw_model[field_name]
        raw_keys = raw_model.get("keys")
        if not isinstance(raw_keys, list):
            raw_keys = []
        model_key_names: set[str] = set()
        for index, raw_key in enumerate(raw_keys):
            if not isinstance(raw_key, dict):
                continue
            base_url = str(raw_key.get("base_url") or default_base_url)
            provider_name = provider_name_for(base_url)
            provider = providers[provider_name]
            key_name = str(raw_key.get("name") or f"{model_id}-{index + 1}").strip()
            if key_name in model_key_names:
                raise ValueError(f"模型 {model_id} 的 key name 重复: {key_name}")
            model_key_names.add(key_name)
            key_data = {
                "api_key": raw_key.get("api_key"),
                "enabled": bool(raw_key.get("enabled", True)),
                "allow_visitor": bool(raw_key.get("allow_visitor", False)),
            }
            key_name = migrated_key_name(provider, key_name, key_data, model_id)
            provider["keys"][key_name] = key_data
            pools = provider.setdefault("pools", {})
            pool_name = str(raw_key.get("pool") or "default")
            pool = pools.setdefault(pool_name, {"keys": []})
            if key_name not in pool["keys"]:
                pool["keys"].append(key_name)
            key_routes = normalize_upstream_routes(raw_key.get("upstream_routes"))
            if key_routes:
                merge_provider_routes(provider, key_routes)
            model_data["targets"].append(
                {
                    "provider": provider_name,
                    "pool": str(raw_key.get("pool") or "default"),
                    "upstream_model": str(raw_key.get("upstream_model") or model_id),
                }
            )
        models[model_id] = model_data

    migrated["config_version"] = CONFIG_VERSION
    migrated["providers"] = providers
    migrated["models"] = models
    migrated.pop("default_base_url", None)
    migrated.pop("upstream_routes", None)
    return migrated


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


def migrate_config_file(path: Path) -> dict[str, Any]:
    data = load_config_data(path)
    migrated = migrate_config_data(data)
    if migrated != data:
        save_config_data(path, migrated)
    return migrated


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
class UnifiedModelConfig:
    model: str
    key: str | None = None
    image_model: str | None = None
    image_key: str | None = None


@dataclass(frozen=True)
class RouterConfig:
    host: str
    port: int
    request_timeout: float
    max_retries: int
    key_failure_threshold: int
    key_cooldown_seconds: float
    key_state_path: str
    upstream_health_check_interval: float
    metrics_db_path: str
    log_file_path: str
    local_api_key: str
    models: tuple[ModelConfig, ...]
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
        if not raw.get("local_api_key"):
            raw["local_api_key"] = generate_local_api_key()
            save_config_data(config_path, raw)
        return cls.from_dict(raw)

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "RouterConfig":
        raw = migrate_config_data(raw)
        models: list[ModelConfig] = []
        providers: list[ProviderConfig] = []
        default_routing_mode = str(raw.get("routing_mode") or "round_robin")
        upstream_routes: dict[str, dict[str, str]] = {}
        provider_keys: dict[tuple[str, str], tuple[ProviderConfig, ProviderKeyConfig]] = {}
        provider_pools: dict[tuple[str, str], tuple[ProviderConfig, tuple[ProviderKeyConfig, ...]]] = {}
        raw_providers = raw.get("providers")
        if isinstance(raw_providers, dict):
            for provider_id, provider in raw_providers.items():
                if not isinstance(provider, dict):
                    continue
                keys: list[ProviderKeyConfig] = []
                raw_keys = provider.get("keys")
                if isinstance(raw_keys, dict):
                    raw_key_items = raw_keys.items()
                elif isinstance(raw_keys, list):
                    raw_key_items = (
                        (str(item.get("name") or index + 1), item)
                        for index, item in enumerate(raw_keys)
                        if isinstance(item, dict)
                    )
                else:
                    raw_key_items = ()
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
                if isinstance(raw_pools, dict):
                    raw_pool_items = raw_pools.items()
                else:
                    raw_pool_items = (("default", {"keys": list(key_configs_by_name)}),)
                for pool_name, pool in raw_pool_items:
                    if isinstance(pool, dict):
                        pool_keys = pool.get("keys")
                    else:
                        pool_keys = pool
                    if not isinstance(pool_keys, list):
                        raise ValueError(f"供应商 {provider_id} 的 pool {pool_name} keys 必须是数组")
                    pool_key_names = tuple(str(key_name) for key_name in pool_keys)
                    missing_keys = [key_name for key_name in pool_key_names if key_name not in key_configs_by_name]
                    if missing_keys:
                        raise ValueError(
                            f"供应商 {provider_id} 的 pool {pool_name} 引用了未配置的 key: {', '.join(missing_keys)}"
                        )
                    pools.append(ProviderPoolConfig(str(pool_name), pool_key_names))
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
                    )

        raw_models = raw.get("models", [])
        if isinstance(raw_models, dict):
            raw_model_items = raw_models.items()
        else:
            raw_model_items = (
                (str(model.get("id") or ""), model)
                for model in raw_models
                if isinstance(model, dict)
            )
            upstream_routes = normalize_upstream_url_routes(raw.get("upstream_routes"))
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
                        provider_config, pool_keys = provider_pool
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
                                    enabled=key_config.enabled and target_enabled,
                                    allow_visitor=key_config.allow_visitor,
                                )
                            )
                        continue
                    provider_key = provider_keys.get((provider_id, key_name))
                    if provider_key is None:
                        raise ValueError(
                            f"模型 {raw_model_id} 引用了未配置的供应商 key: {provider_id}/{key_name}"
                        )
                    provider_config, key_config = provider_key
                    model_keys.append(
                        KeyConfig(
                            name=model_key_name(str(target.get("name") or key_name)),
                            api_key=key_config.api_key,
                            base_url=provider_config.base_url,
                            provider=provider_id,
                            upstream_model=upstream_model,
                            enabled=key_config.enabled and target_enabled,
                            allow_visitor=key_config.allow_visitor,
                        )
                    )
            for index, key in enumerate(model.get("keys", [])):
                base_url = str(
                    key.get("base_url")
                    or raw.get("default_base_url")
                    or "https://api.openai.com"
                )
                merge_upstream_routes_for_url(
                    upstream_routes,
                    base_url,
                    normalize_upstream_routes(key.get("upstream_routes")),
                )
                model_keys.append(
                    KeyConfig(
                        name=model_key_name(str(key.get("name") or f"{raw_model_id}-{index + 1}")),
                        api_key=str(key["api_key"]),
                        base_url=base_url,
                        upstream_model=str(key.get("upstream_model") or raw_model_id),
                        enabled=bool(key.get("enabled", True)),
                        allow_visitor=bool(key.get("allow_visitor", False)),
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
            target_model = str(raw_unified_model.get("model") or "").strip()
            target_key = str(raw_unified_model.get("key") or "").strip() or None
            if not target_model:
                raise ValueError("unified_model.model 不能为空")
            target_image_model = str(raw_unified_model.get("image_model") or "").strip() or None
            target_image_key = str(raw_unified_model.get("image_key") or "").strip() or None
            unified_model = UnifiedModelConfig(
                model=target_model,
                key=target_key,
                image_model=target_image_model,
                image_key=target_image_key,
            )

        config = cls(
            host=str(raw.get("host", "127.0.0.1")),
            port=int(raw.get("port", 8000)),
            request_timeout=float(raw.get("request_timeout", 60)),
            max_retries=int(raw.get("max_retries", 2)),
            key_failure_threshold=max(1, int(raw.get("key_failure_threshold", 2))),
            key_cooldown_seconds=max(0.0, float(raw.get("key_cooldown_seconds", 60))),
            key_state_path=str(raw.get("key_state_path") or default_key_state_path()),
            upstream_health_check_interval=max(0.0, float(raw.get("upstream_health_check_interval", 30))),
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
            if not model.keys:
                raise ValueError(f"模型 {model.id} 至少需要配置一个 key")
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
                    raise ValueError(f"模型 {model.id} 的 base_url 必须以 http:// 或 https:// 开头")

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
        target_model_id = model_ids_by_name.get(self.unified_model.model)
        if target_model_id is None:
            raise ValueError(f"unified_model 引用了未配置的模型: {self.unified_model.model}")
        if self.unified_model.key is None:
            return
        target_model = models_by_id[target_model_id]
        if not any(key.name == self.unified_model.key and key.enabled for key in target_model.keys):
            raise ValueError(f"模型 {target_model_id} 未配置可用 key: {self.unified_model.key}")

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
