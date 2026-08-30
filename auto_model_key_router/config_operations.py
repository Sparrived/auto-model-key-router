from __future__ import annotations

from copy import deepcopy
from typing import Any
from urllib.parse import urlparse

from .config import (
    CONFIG_VERSION,
    RouterConfig,
    migrate_config_data,
    normalize_upstream_base_url,
    normalize_upstream_routes,
)


class ConfigOperationError(ValueError):
    """A presentation-neutral configuration operation failure."""

    def __init__(self, message: str, *, status_code: int = 400) -> None:
        super().__init__(message)
        self.status_code = status_code


def providers(data: dict[str, Any]) -> dict[str, Any]:
    value = data.setdefault("providers", {})
    if not isinstance(value, dict):
        raise ConfigOperationError("providers 必须是对象")
    return value


def models(data: dict[str, Any]) -> dict[str, Any]:
    value = data.setdefault("models", {})
    if not isinstance(value, dict):
        raise ConfigOperationError("models 必须是对象")
    return value


def provider_keys(provider: dict[str, Any]) -> dict[str, Any]:
    value = provider.setdefault("keys", {})
    if not isinstance(value, dict):
        raise ConfigOperationError("provider.keys 必须是对象")
    return value


def provider_pools(provider: dict[str, Any]) -> dict[str, Any]:
    value = provider.setdefault("pools", {})
    if not isinstance(value, dict):
        raise ConfigOperationError("provider.pools 必须是对象")
    return value


def model_targets(model: dict[str, Any]) -> list[dict[str, Any]]:
    value = model.setdefault("targets", [])
    if not isinstance(value, list):
        raise ConfigOperationError("model.targets 必须是数组")
    return value


def pool_key_names(pool: Any) -> list[str]:
    value = pool.get("keys", []) if isinstance(pool, dict) else pool
    return [str(item) for item in value] if isinstance(value, list) else []


def pool_models(pool: Any) -> list[str]:
    if not isinstance(pool, dict):
        return []
    value = pool.get("models", [])
    return [str(item) for item in value if str(item)] if isinstance(value, list) else []


def require_provider(data: dict[str, Any], provider_id: str) -> dict[str, Any]:
    provider = providers(data).get(provider_id)
    if not isinstance(provider, dict):
        raise ConfigOperationError(f"供应商不存在: {provider_id}", status_code=404)
    return provider


def require_key(provider: dict[str, Any], key_name: str) -> dict[str, Any]:
    key = provider_keys(provider).get(key_name)
    if not isinstance(key, dict):
        raise ConfigOperationError(f"Key 不存在: {key_name}", status_code=404)
    return key


def require_pool(provider: dict[str, Any], pool_name: str) -> dict[str, Any]:
    pool = provider_pools(provider).get(pool_name)
    if not isinstance(pool, dict):
        raise ConfigOperationError(f"Pool 不存在: {pool_name}", status_code=404)
    return pool


def require_model(data: dict[str, Any], model_id: str) -> dict[str, Any]:
    model = models(data).get(model_id)
    if not isinstance(model, dict):
        raise ConfigOperationError(f"模型不存在: {model_id}", status_code=404)
    return model


def _non_empty(value: Any, label: str) -> str:
    result = str(value or "").strip()
    if not result:
        raise ConfigOperationError(f"{label}不能为空", status_code=422)
    return result


def normalize_base_url(value: Any) -> str:
    result = _non_empty(value, "base_url").rstrip("/")
    parsed = urlparse(result)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ConfigOperationError("base_url 必须是 http 或 https URL", status_code=422)
    return result


def create_provider(data: dict[str, Any], provider_id: str, base_url: str) -> dict[str, Any]:
    provider_id = _non_empty(provider_id, "供应商 ID")
    all_providers = providers(data)
    if provider_id in all_providers:
        raise ConfigOperationError(f"供应商已存在: {provider_id}", status_code=409)
    provider = {"base_url": normalize_base_url(base_url), "keys": {}, "pools": {}}
    all_providers[provider_id] = provider
    return provider


def update_provider(
    data: dict[str, Any],
    provider_id: str,
    *,
    new_id: str | None = None,
    base_url: str | None = None,
    routes: dict[str, str | None] | None = None,
    update_routes: bool = False,
) -> str:
    all_providers = providers(data)
    provider = require_provider(data, provider_id)
    target_id = _non_empty(new_id, "供应商 ID") if new_id is not None else provider_id
    if target_id != provider_id and target_id in all_providers:
        raise ConfigOperationError(f"供应商已存在: {target_id}", status_code=409)
    if base_url is not None:
        old_url = normalize_upstream_base_url(provider.get("base_url"))
        new_url = normalize_base_url(base_url)
        provider["base_url"] = new_url
        route_map = data.get("upstream_routes")
        if isinstance(route_map, dict) and old_url != new_url and old_url in route_map:
            if new_url not in route_map:
                route_map[new_url] = route_map.pop(old_url)
            else:
                route_map.pop(old_url, None)
    if update_routes:
        try:
            normalized = normalize_upstream_routes(routes)
        except ValueError as exc:
            raise ConfigOperationError(str(exc), status_code=422) from exc
        if normalized:
            provider["routes"] = normalized
        else:
            provider.pop("routes", None)
    if target_id != provider_id:
        all_providers[target_id] = all_providers.pop(provider_id)
        for model in models(data).values():
            if isinstance(model, dict):
                for target in model_targets(model):
                    if target.get("provider") == provider_id:
                        target["provider"] = target_id
    return target_id


def create_provider_key(
    data: dict[str, Any], provider_id: str, key_name: str, api_key: str, *,
    enabled: bool = True, allow_visitor: bool = False, pool_name: str | None = "default",
) -> dict[str, Any]:
    provider = require_provider(data, provider_id)
    key_name = _non_empty(key_name, "Key 名称")
    keys = provider_keys(provider)
    if key_name in keys:
        raise ConfigOperationError(f"Key 已存在: {key_name}", status_code=409)
    key = {
        "api_key": _non_empty(api_key, "API key"),
        "enabled": bool(enabled),
        "allow_visitor": bool(allow_visitor),
    }
    keys[key_name] = key
    if pool_name is not None:
        pool_name = _non_empty(pool_name, "Pool 名称")
        pool = provider_pools(provider).setdefault(pool_name, {"keys": [], "models": []})
        if key_name not in pool.setdefault("keys", []):
            pool["keys"].append(key_name)
    return key


def _unified_targets(unified: dict[str, Any]) -> list[dict[str, Any]]:
    result: list[dict[str, Any]] = []
    for plan_name in ("default", "image"):
        plan = unified.get(plan_name)
        if not isinstance(plan, dict):
            continue
        for role in ("primary", "fallback"):
            target = plan.get(role)
            if isinstance(target, dict):
                result.append(target)
    return result


def replace_unified_model_name(data: dict[str, Any], old_name: str, new_name: str) -> None:
    unified = migrate_config_data(data).get("unified_model")
    if not isinstance(unified, dict):
        return
    for target in _unified_targets(unified):
        if target.get("model") == old_name:
            target["model"] = new_name
    data["unified_model"] = unified


def _clear_unified_keys_from_provider(
    data: dict[str, Any], provider_id: str, key_name: str | None = None
) -> None:
    unified = migrate_config_data(data).get("unified_model")
    if not isinstance(unified, dict):
        return
    candidate = deepcopy(data)
    candidate.pop("unified_model", None)
    try:
        config = RouterConfig.from_dict(candidate)
    except (KeyError, TypeError, ValueError):
        return
    configured = {model.id: model for model in config.models}
    for target in _unified_targets(unified):
        selected = str(target.get("key") or "")
        model = configured.get(str(target.get("model") or ""))
        if not selected or model is None:
            continue
        if any(
            (key.name == selected or key.name.endswith(f"-{selected}"))
            and key.provider == provider_id
            and (key_name is None or key.name == key_name or key.name.endswith(f"-{key_name}"))
            for key in model.keys
        ):
            target["key"] = None
    data["unified_model"] = unified


def _rename_unified_key(data: dict[str, Any], provider_id: str, old_name: str, new_name: str) -> None:
    unified = migrate_config_data(data).get("unified_model")
    if not isinstance(unified, dict):
        return
    candidate = deepcopy(data)
    candidate.pop("unified_model", None)
    try:
        config = RouterConfig.from_dict(candidate)
    except (KeyError, TypeError, ValueError):
        config = None
    for target in _unified_targets(unified):
        selected = str(target.get("key") or "")
        if selected != old_name and not selected.endswith(f"-{old_name}"):
            continue
        model = next((item for item in config.models if item.id == str(target.get("model") or "")), None) if config else None
        if model is None or any(
            (key.name == old_name or key.name.endswith(f"-{old_name}"))
            and key.provider == provider_id
            for key in model.keys
        ):
            target["key"] = (
                new_name
                if selected == old_name
                else f"{selected[:-len(old_name)]}{new_name}"
            )
    data["unified_model"] = unified


def update_provider_key(
    data: dict[str, Any], provider_id: str, key_name: str, *,
    new_name: str | None = None, api_key: str | None = None,
    enabled: bool | None = None, allow_visitor: bool | None = None,
) -> str:
    provider = require_provider(data, provider_id)
    keys = provider_keys(provider)
    key = require_key(provider, key_name)
    target_name = _non_empty(new_name, "Key 名称") if new_name is not None else key_name
    if target_name != key_name and target_name in keys:
        raise ConfigOperationError(f"Key 已存在: {target_name}", status_code=409)
    if api_key is not None:
        key["api_key"] = _non_empty(api_key, "API key")
    if enabled is not None:
        key["enabled"] = bool(enabled)
    if allow_visitor is not None:
        key["allow_visitor"] = bool(allow_visitor)
    if enabled is False:
        _clear_unified_keys_from_provider(data, provider_id, key_name)
    if target_name != key_name:
        _rename_unified_key(data, provider_id, key_name, target_name)
        keys[target_name] = keys.pop(key_name)
        for pool in provider_pools(provider).values():
            if not isinstance(pool, dict):
                continue
            pool["keys"] = [target_name if item == key_name else item for item in pool_key_names(pool)]
            for field in ("key_models", "routes", "errors"):
                value = pool.get(field)
                if isinstance(value, dict) and key_name in value:
                    value[target_name] = value.pop(key_name)
        for model in models(data).values():
            if isinstance(model, dict):
                for target in model_targets(model):
                    if target.get("provider") == provider_id and target.get("key") == key_name:
                        target["key"] = target_name
    return target_name


def fallback_model_id(data: dict[str, Any]) -> str | None:
    for model_id, model in sorted(models(data).items()):
        if isinstance(model, dict) and model_targets(model):
            return str(model_id)
    return None


def repair_unified_model(data: dict[str, Any]) -> None:
    unified = migrate_config_data(data).get("unified_model")
    if not isinstance(unified, dict):
        data.pop("unified_model", None)
        return
    candidate = deepcopy(data)
    candidate.pop("unified_model", None)
    try:
        config = RouterConfig.from_dict(candidate)
    except (KeyError, TypeError, ValueError):
        return
    configured = {model.id: model for model in config.models}
    names = {name: model.id for model in config.models for name in (model.id, *model.aliases)}
    default = unified.get("default")
    primary = default.get("primary") if isinstance(default, dict) else None
    if not isinstance(primary, dict) or names.get(str(primary.get("model") or "")) is None:
        replacement = fallback_model_id(data)
        if replacement is None:
            data.pop("unified_model", None)
            return
        unified["default"] = {"primary": {"model": replacement, "key": None}}
    for plan_name in ("default", "image"):
        plan = unified.get(plan_name)
        if not isinstance(plan, dict):
            if plan_name == "image":
                unified.pop(plan_name, None)
            continue
        for role in ("primary", "fallback"):
            target = plan.get(role)
            if not isinstance(target, dict):
                if role == "fallback":
                    plan.pop(role, None)
                continue
            model_id = names.get(str(target.get("model") or ""))
            if model_id is None:
                if plan_name == "default" and role == "primary":
                    continue
                if role == "primary":
                    unified.pop(plan_name, None)
                    break
                plan.pop(role, None)
                continue
            target["model"] = model_id
            selected = str(target.get("key") or "").strip()
            if selected and not any(
                key.enabled and (key.name == selected or key.name.endswith(f"-{selected}"))
                for key in configured[model_id].keys
            ):
                target["key"] = None
        primary = plan.get("primary")
        fallback = plan.get("fallback")
        if isinstance(primary, dict) and isinstance(fallback, dict) and primary.get("model") == fallback.get("model"):
            plan.pop("fallback", None)
    data["unified_model"] = unified


def delete_provider_key(data: dict[str, Any], provider_id: str, key_name: str) -> set[str]:
    provider = require_provider(data, provider_id)
    keys = provider_keys(provider)
    require_key(provider, key_name)
    _clear_unified_keys_from_provider(data, provider_id, key_name)
    keys.pop(key_name)
    empty_pools: set[str] = set()
    for pool_name, pool in list(provider_pools(provider).items()):
        if not isinstance(pool, dict):
            continue
        pool["keys"] = [item for item in pool_key_names(pool) if item != key_name]
        for field in ("key_models", "routes", "errors"):
            value = pool.get(field)
            if isinstance(value, dict):
                value.pop(key_name, None)
        if not pool["keys"]:
            empty_pools.add(str(pool_name))
    for pool_name in empty_pools:
        provider_pools(provider).pop(pool_name, None)
    removed: set[str] = set()
    for model_id, model in list(models(data).items()):
        if not isinstance(model, dict):
            continue
        targets = model_targets(model)
        targets[:] = [
            target for target in targets
            if not (target.get("provider") == provider_id and (target.get("key") == key_name or target.get("pool") in empty_pools))
        ]
        if not targets:
            models(data).pop(model_id, None)
            removed.add(str(model_id))
    if not keys:
        providers(data).pop(provider_id, None)
    repair_unified_model(data)
    return removed


def delete_provider(data: dict[str, Any], provider_id: str) -> set[str]:
    require_provider(data, provider_id)
    _clear_unified_keys_from_provider(data, provider_id)
    providers(data).pop(provider_id, None)
    removed: set[str] = set()
    for model_id, model in list(models(data).items()):
        if not isinstance(model, dict):
            continue
        targets = model_targets(model)
        targets[:] = [target for target in targets if target.get("provider") != provider_id]
        if not targets:
            models(data).pop(model_id, None)
            removed.add(str(model_id))
    repair_unified_model(data)
    return removed


def assign_pool_keys(data: dict[str, Any], provider_id: str, pool_name: str, key_names: list[str], *, retain_existing: bool = False) -> dict[str, Any]:
    provider = require_provider(data, provider_id)
    pool_name = _non_empty(pool_name, "Pool 名称")
    key_names = [str(item) for item in key_names]
    if len(key_names) != len(set(key_names)):
        raise ConfigOperationError("Pool keys 不能重复", status_code=422)
    missing = [name for name in key_names if name not in provider_keys(provider)]
    if missing:
        raise ConfigOperationError(f"Pool 引用了不存在的 key: {missing[0]}", status_code=422)
    pools = provider_pools(provider)
    pool = pools.setdefault(pool_name, {"keys": [], "models": []})
    selected = list(dict.fromkeys([*(pool_key_names(pool) if retain_existing else []), *key_names]))
    for other_name, other in pools.items():
        if other_name != pool_name and isinstance(other, dict):
            other["keys"] = [item for item in pool_key_names(other) if item not in selected]
    pool["keys"] = selected
    pool.setdefault("models", [])
    return pool


def enable_pool_models(data: dict[str, Any], provider_id: str, pool_name: str, enabled_models: list[str]) -> None:
    provider = require_provider(data, provider_id)
    pool = require_pool(provider, pool_name)
    enabled_models = list(dict.fromkeys(str(item) for item in enabled_models if str(item)))
    pool["models"] = enabled_models
    all_models = models(data)
    enabled = set(enabled_models)
    for model_id in enabled_models:
        model = all_models.setdefault(model_id, {"targets": []})
        targets = model_targets(model)
        if not any(target.get("provider") == provider_id and target.get("pool") == pool_name and str(target.get("upstream_model") or model_id) == model_id for target in targets):
            targets.append({"provider": provider_id, "pool": pool_name, "upstream_model": model_id})
    for model_id, model in list(all_models.items()):
        if not isinstance(model, dict):
            continue
        targets = model_targets(model)
        targets[:] = [
            target
            for target in targets
            if not (
                target.get("provider") == provider_id
                and target.get("pool") == pool_name
                and str(target.get("upstream_model") or model_id) not in enabled
            )
        ]
        if not targets:
            all_models.pop(model_id, None)
    repair_unified_model(data)


def rename_pool(data: dict[str, Any], provider_id: str, pool_name: str, new_name: str) -> str:
    provider = require_provider(data, provider_id)
    pools = provider_pools(provider)
    require_pool(provider, pool_name)
    new_name = _non_empty(new_name, "Pool 名称")
    if new_name != pool_name and new_name in pools:
        raise ConfigOperationError(f"Pool 已存在: {new_name}", status_code=409)
    if new_name != pool_name:
        pools[new_name] = pools.pop(pool_name)
        for model in models(data).values():
            if isinstance(model, dict):
                for target in model_targets(model):
                    if target.get("provider") == provider_id and target.get("pool") == pool_name:
                        target["pool"] = new_name
    return new_name


def delete_pool(data: dict[str, Any], provider_id: str, pool_name: str) -> None:
    provider = require_provider(data, provider_id)
    pool = require_pool(provider, pool_name)
    keys = pool_key_names(pool)
    enable_pool_models(data, provider_id, pool_name, [])
    provider_pools(provider).pop(pool_name, None)
    if keys:
        default = provider_pools(provider).setdefault("default", {"keys": [], "models": []})
        default["keys"] = list(dict.fromkeys([*pool_key_names(default), *keys]))
    repair_unified_model(data)


def validate_targets(data: dict[str, Any], targets: list[dict[str, Any]]) -> None:
    if not targets:
        raise ConfigOperationError("targets 不能为空", status_code=422)
    for target in targets:
        if not isinstance(target, dict):
            raise ConfigOperationError("targets 必须是对象数组", status_code=422)
        provider_id = _non_empty(target.get("provider"), "target.provider")
        pool_name = _non_empty(target.get("pool"), "target.pool")
        upstream = _non_empty(target.get("upstream_model"), "target.upstream_model")
        pool = require_pool(require_provider(data, provider_id), pool_name)
        if upstream not in pool_models(pool):
            raise ConfigOperationError(f"模型池 {provider_id}/{pool_name} 未启用模型: {upstream}", status_code=422)


def add_model_target(data: dict[str, Any], model_id: str, target: dict[str, Any]) -> None:
    validate_targets(data, [target])
    targets = model_targets(require_model(data, model_id))
    identity = (str(target.get("provider")), str(target.get("pool")), str(target.get("upstream_model")))
    if any((str(item.get("provider")), str(item.get("pool")), str(item.get("upstream_model"))) == identity for item in targets):
        raise ConfigOperationError("该路由已存在", status_code=409)
    targets.append(deepcopy(target))


def update_model_target(data: dict[str, Any], model_id: str, target_index: int, upstream_model: str) -> None:
    targets = model_targets(require_model(data, model_id))
    if target_index < 0 or target_index >= len(targets):
        raise ConfigOperationError("模型路由不存在", status_code=404)
    updated = deepcopy(targets[target_index])
    updated["upstream_model"] = _non_empty(upstream_model, "target.upstream_model")
    validate_targets(data, [updated])
    targets[target_index] = updated


def delete_model_target(data: dict[str, Any], model_id: str, target_index: int) -> dict[str, Any]:
    targets = model_targets(require_model(data, model_id))
    if target_index < 0 or target_index >= len(targets):
        raise ConfigOperationError("模型路由不存在", status_code=404)
    removed = targets.pop(target_index)
    repair_unified_model(data)
    return removed


def regenerate_local_api_key(data: dict[str, Any], api_key: str) -> None:
    data["local_api_key"] = _non_empty(api_key, "本地鉴权 key")


def update_settings(data: dict[str, Any], **updates: Any) -> None:
    if updates.get("host") is not None:
        host = _non_empty(updates["host"], "监听地址")
        if "://" in host or "/" in host:
            raise ConfigOperationError("监听地址只填写 IP 或主机名，不要包含协议或路径", status_code=422)
        updates["host"] = host
    if updates.get("port") is not None and not 1 <= int(updates["port"]) <= 65535:
        raise ConfigOperationError("端口范围必须是 1-65535", status_code=422)
    for field in ("request_timeout", "stream_first_byte_timeout", "stream_idle_timeout"):
        if updates.get(field) is not None and float(updates[field]) <= 0:
            raise ConfigOperationError("超时必须大于 0", status_code=422)
    if updates.get("max_retries") is not None and int(updates["max_retries"]) < 0:
        raise ConfigOperationError("max_retries 不能小于 0", status_code=422)
    data.update(updates)


def _normalize_aliases(aliases: list[str]) -> list[str]:
    result = [str(alias).strip() for alias in aliases if str(alias).strip()]
    if len(result) != len(set(result)):
        raise ConfigOperationError("模型别称不能重复", status_code=422)
    return result


def _validate_model_names(data: dict[str, Any], model_id: str, aliases: list[str], *, exclude: str | None = None) -> None:
    names: set[str] = set()
    for current_id, model in models(data).items():
        if str(current_id) == exclude or not isinstance(model, dict):
            continue
        names.add(str(current_id))
        names.update(str(alias) for alias in model.get("aliases", []) if str(alias))
    if model_id in names:
        raise ConfigOperationError(f"模型名称重复: {model_id}", status_code=409)
    for alias in aliases:
        if alias == model_id or alias in names:
            raise ConfigOperationError(f"模型名称重复: {alias}", status_code=409)


def create_model(data: dict[str, Any], model_id: str, *, aliases: list[str] | None = None, routing_mode: str = "round_robin", reasoning_effort: str | None = None, targets: list[dict[str, Any]] | None = None) -> dict[str, Any]:
    model_id = _non_empty(model_id, "模型 ID")
    if model_id in models(data):
        raise ConfigOperationError(f"模型已存在: {model_id}", status_code=409)
    aliases = _normalize_aliases(list(aliases or []))
    _validate_model_names(data, model_id, aliases)
    routing_mode = str(routing_mode or "round_robin").strip()
    if routing_mode not in {"priority", "round_robin", "only_first"}:
        raise ConfigOperationError("routing_mode 无效", status_code=422)
    if targets is not None:
        validate_targets(data, targets)
    model: dict[str, Any] = {"aliases": aliases, "routing_mode": routing_mode, "targets": deepcopy(targets or [])}
    if reasoning_effort is not None and str(reasoning_effort).strip() not in {"", "default", "downstream"}:
        model["reasoning_effort"] = str(reasoning_effort).strip()
    models(data)[model_id] = model
    return model


def update_model(data: dict[str, Any], model_id: str, *, new_id: str | None = None, aliases: list[str] | None = None, routing_mode: str | None = None, reasoning_effort: str | None = None, update_reasoning_effort: bool = False, targets: list[dict[str, Any]] | None = None) -> str:
    all_models = models(data)
    model = require_model(data, model_id)
    target_id = _non_empty(new_id, "模型 ID") if new_id is not None else model_id
    if target_id != model_id and target_id in all_models:
        raise ConfigOperationError(f"模型已存在: {target_id}", status_code=409)
    if target_id != model_id:
        if target_id in {
            str(alias) for alias in model.get("aliases", []) if str(alias)
        }:
            raise ConfigOperationError(
                f"模型名称重复: {target_id}", status_code=409
            )
        _validate_model_names(
            data, target_id, list(model.get("aliases", [])), exclude=model_id
        )
    if aliases is not None:
        aliases = _normalize_aliases(list(aliases))
        _validate_model_names(data, target_id, aliases, exclude=model_id)
        model["aliases"] = aliases
    if routing_mode is not None:
        routing_mode = str(routing_mode).strip()
        if routing_mode not in {"priority", "round_robin", "only_first"}:
            raise ConfigOperationError("routing_mode 无效", status_code=422)
        model["routing_mode"] = routing_mode
    if update_reasoning_effort:
        if reasoning_effort is None or str(reasoning_effort).strip() in {"", "default", "downstream"}:
            model.pop("reasoning_effort", None)
        else:
            model["reasoning_effort"] = str(reasoning_effort).strip()
    if targets is not None:
        validate_targets(data, targets)
        model["targets"] = deepcopy(targets)
    if target_id != model_id:
        all_models[target_id] = all_models.pop(model_id)
        replace_unified_model_name(data, model_id, target_id)
    return target_id


def delete_model(data: dict[str, Any], model_id: str) -> None:
    require_model(data, model_id)
    models(data).pop(model_id, None)
    repair_unified_model(data)


def provider_id_for_base_url(data: dict[str, Any], base_url: str) -> str:
    normalized = normalize_base_url(base_url)
    for provider_id, provider in providers(data).items():
        if isinstance(provider, dict) and str(provider.get("base_url") or "").rstrip("/") == normalized:
            return str(provider_id)
    candidate = normalized.split("://", 1)[-1].strip("/").replace("/", "-").replace(":", "-") or "default"
    base_candidate = candidate
    suffix = 2
    while candidate in providers(data):
        candidate = f"{base_candidate}-{suffix}"
        suffix += 1
    create_provider(data, candidate, normalized)
    return candidate


def set_upstream_routes_for_base_url(data: dict[str, Any], base_url: str, routes: dict[str, str | None] | None) -> None:
    try:
        normalized_url = normalize_upstream_base_url(base_url)
        normalized_routes = normalize_upstream_routes(routes)
    except ValueError as exc:
        raise ConfigOperationError(str(exc), status_code=422) from exc
    route_map = data.get("upstream_routes")
    if not isinstance(route_map, dict):
        route_map = {}
    if normalized_routes:
        route_map[normalized_url] = normalized_routes
        data["upstream_routes"] = route_map
    else:
        route_map.pop(normalized_url, None)
        if route_map:
            data["upstream_routes"] = route_map
        else:
            data.pop("upstream_routes", None)


def _unique_pool_name(provider: dict[str, Any], preferred: str) -> str:
    pools = provider_pools(provider)
    candidate = preferred
    suffix = 2
    while candidate in pools:
        candidate = f"{preferred}-{suffix}"
        suffix += 1
    return candidate


def create_model_key(data: dict[str, Any], model_id: str, key_name: str, api_key: str, *, base_url: str | None = None, enabled: bool = True, allow_visitor: bool = False, upstream_model: str | None = None, upstream_routes: dict[str, str | None] | None = None, update_upstream_routes: bool = False) -> None:
    model = require_model(data, model_id)
    key_name = _non_empty(key_name, "Key 名称")
    try:
        parsed = RouterConfig.from_dict(data)
        parsed_model = next((item for item in parsed.models if item.id == model_id), None)
    except (KeyError, TypeError, ValueError):
        parsed_model = None
    if parsed_model is not None and any(key.name == key_name or key.name.endswith(f"-{key_name}") for key in parsed_model.keys):
        raise ConfigOperationError(f"模型 {model_id} 的 key 已存在: {key_name}", status_code=409)
    target_url = normalize_base_url(base_url or data.get("default_base_url") or "https://api.openai.com")
    provider_id = provider_id_for_base_url(data, target_url)
    provider = require_provider(data, provider_id)
    pool_name = _unique_pool_name(provider, key_name)
    create_provider_key(data, provider_id, key_name, api_key, enabled=enabled, allow_visitor=allow_visitor, pool_name=pool_name)
    upstream = _non_empty(upstream_model or model_id, "target.upstream_model")
    provider_pools(provider)[pool_name]["models"] = [upstream]
    model_targets(model).append({"provider": provider_id, "pool": pool_name, "upstream_model": upstream})
    if update_upstream_routes:
        set_upstream_routes_for_base_url(data, target_url, upstream_routes)


def create_model_with_keys(data: dict[str, Any], model_id: str, *, aliases: list[str] | None = None, routing_mode: str = "round_robin", reasoning_effort: str | None = None, keys: list[dict[str, Any]] | None = None) -> None:
    create_model(data, model_id, aliases=aliases, routing_mode=routing_mode, reasoning_effort=reasoning_effort)
    for key in keys or []:
        create_model_key(data, model_id, str(key.get("name") or ""), str(key.get("api_key") or ""), base_url=key.get("base_url"), enabled=bool(key.get("enabled", True)), allow_visitor=bool(key.get("allow_visitor", False)), upstream_model=str(key.get("upstream_model") or model_id), upstream_routes=key.get("upstream_routes"), update_upstream_routes="upstream_routes" in key)


def _locate_model_target(data: dict[str, Any], model_id: str, key_name: str) -> tuple[dict[str, Any], dict[str, Any], str, str]:
    model = require_model(data, model_id)
    for target in model_targets(model):
        provider_id = str(target.get("provider") or "")
        pool_name = str(target.get("pool") or "")
        provider = require_provider(data, provider_id)
        pool = require_pool(provider, pool_name)
        names = pool_key_names(pool)
        for raw_name in names:
            if key_name in {raw_name, f"{pool_name}-{raw_name}"}:
                return target, provider, pool_name, raw_name
        if key_name == str(target.get("key") or "") and len(names) == 1:
            return target, provider, pool_name, names[0]
    raise ConfigOperationError(f"模型 {model_id} 的 key 不存在: {key_name}", status_code=404)


def _unique_provider_id(data: dict[str, Any], preferred: str) -> str:
    candidate = preferred
    suffix = 2
    while candidate in providers(data):
        candidate = f"{preferred}-{suffix}"
        suffix += 1
    return candidate


def _provider_key_is_shared(
    data: dict[str, Any], model_id: str, provider_id: str, key_name: str
) -> bool:
    provider = providers(data).get(provider_id)
    if not isinstance(provider, dict):
        return False
    for current_id, model in models(data).items():
        if str(current_id) == model_id or not isinstance(model, dict):
            continue
        for target in model_targets(model):
            if target.get("provider") != provider_id:
                continue
            pool = provider_pools(provider).get(str(target.get("pool") or ""))
            if key_name in pool_key_names(pool):
                return True
    return False


def _model_target_is_shared(data: dict[str, Any], model_id: str, provider_id: str, pool_name: str) -> bool:
    for current_id, model in models(data).items():
        if str(current_id) == model_id or not isinstance(model, dict):
            continue
        if any(target.get("provider") == provider_id and target.get("pool") == pool_name for target in model_targets(model)):
            return True
    return False


def _clone_model_target(data: dict[str, Any], model_id: str, target: dict[str, Any], provider: dict[str, Any], pool_name: str, raw_name: str, *, base_url: str | None = None) -> tuple[dict[str, Any], dict[str, Any], str]:
    source_url = normalize_base_url(provider.get("base_url"))
    target_url = normalize_base_url(base_url or source_url)
    source_pool = require_pool(provider, pool_name)
    current_provider_id = str(target.get("provider") or "")
    if target_url == source_url and not _model_target_is_shared(
        data, model_id, current_provider_id, pool_name
    ) and not _provider_key_is_shared(
        data, model_id, current_provider_id, raw_name
    ) and len(pool_key_names(source_pool)) == 1:
        return target, provider, raw_name
    if target_url != source_url:
        preferred = target_url.split("://", 1)[-1].replace("/", "-").replace(":", "-") or "provider"
    else:
        preferred = f"{target.get('provider')}-{model_id}"
    provider_id = _unique_provider_id(data, preferred)
    clone = deepcopy(provider)
    clone["base_url"] = target_url
    clone["keys"] = {}
    clone["pools"] = {}
    clone["_amkr_model_key_clone"] = True
    providers(data)[provider_id] = clone
    clone["keys"][raw_name] = deepcopy(provider_keys(provider)[raw_name])
    new_pool = _unique_pool_name(clone, pool_name)
    clone["pools"][new_pool] = deepcopy(source_pool)
    clone["pools"][new_pool]["keys"] = [raw_name]
    target["provider"] = provider_id
    target["pool"] = new_pool
    return target, clone, raw_name


def update_model_key_local(data: dict[str, Any], model_id: str, key_name: str, *, new_name: str | None = None, api_key: str | None = None, base_url: str | None = None, update_base_url: bool = False, enabled: bool | None = None, allow_visitor: bool | None = None, upstream_routes: dict[str, str | None] | None = None, update_upstream_routes: bool = False) -> str:
    target, provider, pool_name, raw_name = _locate_model_target(data, model_id, key_name)
    target, provider, raw_name = _clone_model_target(data, model_id, target, provider, pool_name, raw_name, base_url=(base_url if base_url is not None else (data.get("default_base_url") if update_base_url else None)))
    key = require_key(provider, raw_name)
    actual = _non_empty(new_name, "Key 名称") if new_name is not None else key_name
    if actual != raw_name and actual in provider_keys(provider):
        raise ConfigOperationError(f"Key 已存在: {actual}", status_code=409)
    if api_key is not None:
        key["api_key"] = _non_empty(api_key, "API key")
    if enabled is not None:
        key["enabled"] = bool(enabled)
    if allow_visitor is not None:
        key["allow_visitor"] = bool(allow_visitor)
    if actual != raw_name:
        provider_keys(provider)[actual] = provider_keys(provider).pop(raw_name)
        pool = require_pool(provider, str(target.get("pool") or pool_name))
        pool["keys"] = [actual if item == raw_name else item for item in pool_key_names(pool)]
    if enabled is False:
        _clear_unified_keys_from_provider(
            data,
            str(target.get("provider") or ""),
            actual,
        )
    if update_upstream_routes:
        set_upstream_routes_for_base_url(
            data, str(provider.get("base_url") or ""), upstream_routes
        )
    repair_unified_model(data)
    return actual


def delete_model_key_local(data: dict[str, Any], model_id: str, key_name: str) -> None:
    model = require_model(data, model_id)
    target, provider, pool_name, raw_name = _locate_model_target(data, model_id, key_name)
    pool = require_pool(provider, pool_name)
    remaining = [item for item in pool_key_names(pool) if item != raw_name]
    if remaining:
        target, clone, _ = _clone_model_target(data, model_id, target, provider, pool_name, raw_name)
        for name in remaining:
            clone["keys"][name] = deepcopy(provider_keys(provider)[name])
        clone["keys"].pop(raw_name, None)
        clone_pool = require_pool(clone, str(target.get("pool") or ""))
        clone_pool["keys"] = remaining
        clone_pool["models"] = pool_models(pool)
    else:
        model_targets(model).remove(target)
        if not model_targets(model):
            models(data).pop(model_id, None)
        provider_id = str(target.get("provider") or "")
        candidate = providers(data).get(provider_id)
        if isinstance(candidate, dict) and candidate.get("_amkr_model_key_clone"):
            if not any(isinstance(current, dict) and any(t.get("provider") == provider_id for t in model_targets(current)) for current in models(data).values()):
                providers(data).pop(provider_id, None)
    repair_unified_model(data)


def delete_model_key(data: dict[str, Any], model_id: str, key_name: str) -> None:
    delete_model_key_local(data, model_id, key_name)


# Compatibility name: model-facing Key mutations are always local to one model.
update_model_key = update_model_key_local


def transferable_config(data: dict[str, Any], *, include_visitor: bool) -> dict[str, Any]:
    result_providers = deepcopy(providers(data))
    for provider in result_providers.values():
        if isinstance(provider, dict):
            provider.pop("_amkr_model_key_clone", None)
    result_models = deepcopy(models(data))
    if not include_visitor:
        for provider in result_providers.values():
            if isinstance(provider, dict):
                for key in provider_keys(provider).values():
                    if isinstance(key, dict):
                        key.pop("allow_visitor", None)
    return {"config_version": CONFIG_VERSION, "providers": result_providers, "models": result_models}


def merge_transferable_config(current_data: dict[str, Any], transfer_data: dict[str, Any]) -> tuple[dict[str, Any], int, int, int]:
    merged = deepcopy(current_data)
    merged["config_version"] = CONFIG_VERSION
    current_providers, current_models = providers(merged), models(merged)
    transfer_providers, transfer_models = providers(transfer_data), models(transfer_data)
    added_models = added_keys = skipped_keys = 0
    pool_map: dict[tuple[str, str], tuple[str, str]] = {}
    for source_id, source_provider in transfer_providers.items():
        if not isinstance(source_provider, dict):
            continue
        target_id = str(source_id)
        if target_id in current_providers:
            current_provider = current_providers[target_id]
            if normalize_upstream_base_url(current_provider.get("base_url")) != normalize_upstream_base_url(source_provider.get("base_url")):
                base, suffix = target_id, 2
                while f"{base}-{suffix}" in current_providers:
                    suffix += 1
                target_id = f"{base}-{suffix}"
                current_provider = deepcopy(source_provider)
                current_provider["keys"], current_provider["pools"] = {}, {}
                current_providers[target_id] = current_provider
        else:
            current_provider = deepcopy(source_provider)
            current_provider["keys"], current_provider["pools"] = {}, {}
            current_providers[target_id] = current_provider
        target_keys, target_pools = provider_keys(current_provider), provider_pools(current_provider)
        existing_secrets = {str(key.get("api_key") or "") for key in target_keys.values() if isinstance(key, dict)}
        key_map: dict[str, str | None] = {}
        for source_name, source_key in provider_keys(source_provider).items():
            secret = str(source_key.get("api_key") or "") if isinstance(source_key, dict) else ""
            if secret in existing_secrets:
                skipped_keys += 1
                key_map[str(source_name)] = None
                continue
            destination, base = str(source_name), str(source_name)
            suffix = 2
            while destination in target_keys:
                destination = f"{base}-{suffix}"
                suffix += 1
            target_keys[destination] = deepcopy(source_key)
            key_map[str(source_name)] = destination
            existing_secrets.add(secret)
            added_keys += 1
        for source_pool_name, source_pool in provider_pools(source_provider).items():
            mapped_keys = [key_map[name] for name in pool_key_names(source_pool) if key_map.get(name)]
            if not mapped_keys:
                pool_map[(str(source_id), str(source_pool_name))] = (target_id, "")
                continue
            destination, base = str(source_pool_name), str(source_pool_name)
            suffix = 2
            while destination in target_pools:
                destination = f"{base}-{suffix}"
                suffix += 1
            copied = deepcopy(source_pool) if isinstance(source_pool, dict) else {}
            copied["keys"] = mapped_keys
            copied.setdefault("models", [])
            target_pools[destination] = copied
            pool_map[(str(source_id), str(source_pool_name))] = (target_id, destination)
    for model_id, source_model in transfer_models.items():
        if not isinstance(source_model, dict):
            continue
        destination = current_models.get(str(model_id))
        if not isinstance(destination, dict):
            destination = deepcopy(source_model)
            destination["targets"] = []
            current_models[str(model_id)] = destination
            added_models += 1
        targets = model_targets(destination)
        identities = {(str(t.get("provider") or ""), str(t.get("pool") or ""), str(t.get("upstream_model") or model_id)) for t in targets}
        for source_target in model_targets(source_model):
            mapped = pool_map.get((str(source_target.get("provider") or ""), str(source_target.get("pool") or "")))
            if not mapped or not mapped[1]:
                continue
            new_target = {"provider": mapped[0], "pool": mapped[1], "upstream_model": str(source_target.get("upstream_model") or model_id)}
            identity = (new_target["provider"], new_target["pool"], new_target["upstream_model"])
            if identity not in identities:
                targets.append(new_target)
                identities.add(identity)
    try:
        RouterConfig.from_dict(merged)
    except (KeyError, TypeError, ValueError) as exc:
        raise ConfigOperationError(str(exc), status_code=422) from exc
    return merged, added_models, added_keys, skipped_keys


def set_unified_model(data: dict[str, Any], unified: dict[str, Any] | None) -> None:
    if unified is None:
        data.pop("unified_model", None)
        return
    candidate = deepcopy(data)
    candidate["unified_model"] = deepcopy(unified)
    try:
        parsed = RouterConfig.from_dict(candidate)
    except (KeyError, TypeError, ValueError) as exc:
        raise ConfigOperationError(str(exc), status_code=422) from exc
    canonical = {"default": _serialize_plan(parsed.unified_model.default)}
    if parsed.unified_model.image:
        canonical["image"] = _serialize_plan(parsed.unified_model.image)
    data["unified_model"] = canonical


def _serialize_plan(plan: Any) -> dict[str, Any]:
    primary: dict[str, Any] = {"model": plan.primary.model}
    if plan.primary.key is not None:
        primary["key"] = plan.primary.key
    result: dict[str, Any] = {"primary": primary}
    if plan.fallback:
        fallback: dict[str, Any] = {"model": plan.fallback.model}
        if plan.fallback.key is not None:
            fallback["key"] = plan.fallback.key
        result["fallback"] = fallback
    return result


def _validate_unified_key(config: RouterConfig, model_id: str, key_name: str) -> None:
    model = next((item for item in config.models if item.id == model_id), None)
    if model is None or not any(key.enabled and (key.name == key_name or key.name.endswith(f"-{key_name}")) for key in model.keys):
        raise ConfigOperationError(f"模型 {model_id} 的 key 不存在或未启用: {key_name}", status_code=404)


def switch_unified_target(data: dict[str, Any], target: str, model_name: str | None = None, key_name: str | None = None, *, update_key: bool = False) -> None:
    if target not in {"default.primary", "default.fallback", "image.primary", "image.fallback"}:
        raise ConfigOperationError(f"无效 unified 目标: {target}", status_code=422)
    config = RouterConfig.from_dict(data)
    current = config.unified_model
    plan_name, role = target.split(".")
    current_plan = current.default if current and plan_name == "default" else (current.image if current else None)
    current_target = getattr(current_plan, role) if current_plan else None
    if model_name is None:
        if current_target is None:
            raise ConfigOperationError(f"尚未配置 {target}，请先选择模型", status_code=422)
        model_id = current_target.model
    else:
        model_id = config.configured_model_id(model_name.strip())
        if model_id is None:
            raise ConfigOperationError(f"未配置模型或别名: {model_name}", status_code=404)
    selected_key = current_target.key if current_target else None
    if current_target is None or model_id != current_target.model:
        selected_key = None
    if update_key:
        selected_key = key_name.strip() if key_name else None
        if selected_key:
            _validate_unified_key(config, model_id, selected_key)
    canonical = migrate_config_data(data).get("unified_model")
    if not isinstance(canonical, dict):
        canonical = {"default": {}}
    plan = canonical.setdefault(plan_name, {})
    if not isinstance(plan, dict):
        raise ConfigOperationError(f"unified_model.{plan_name} 必须是对象", status_code=422)
    plan[role] = {"model": model_id, "key": selected_key}
    if plan_name == "image" and role == "fallback" and "primary" not in plan:
        raise ConfigOperationError("配置 image.fallback 前必须先配置 image.primary", status_code=422)
    set_unified_model(data, canonical)
