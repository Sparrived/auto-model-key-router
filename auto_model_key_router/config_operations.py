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


def model_targets(model: dict[str, Any]) -> list[dict[str, Any]]:
    value = model.setdefault("targets", [])
    if not isinstance(value, list):
        raise ConfigOperationError("model.targets 必须是数组")
    return value


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
    provider = {"base_url": normalize_base_url(base_url), "keys": {}}
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
    enabled: bool = True, allow_visitor: bool = False, pool_name: str | None = None,
) -> dict[str, Any]:
    """Create a provider-level key.

    ``pool_name`` is accepted for API compatibility with earlier callers but is
    ignored: v4 has no pools; a key is used by whichever model binds it through
    a model target.
    """
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


def _model_has_provider_key(
    config: RouterConfig | None, model_id: str, provider_id: str, key_name: str | None
) -> bool:
    """True when unified target key resolves to a key of this provider."""
    if config is None:
        return False
    model = next((item for item in config.models if item.id == model_id), None)
    if model is None or not key_name:
        return False
    for key in model.keys:
        name_matches = key.name == key_name or key.name.endswith(f"-{key_name}")
        if not name_matches or key.provider != provider_id:
            continue
        return True
    return False


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
    for target in _unified_targets(unified):
        if _model_has_provider_key(
            config,
            str(target.get("model") or ""),
            provider_id,
            str(target.get("key") or "") or None,
        ):
            if key_name is None or _model_has_provider_key(
                config,
                str(target.get("model") or ""),
                provider_id,
                key_name,
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
        if not selected:
            continue
        model_id = str(target.get("model") or "")
        if not _model_has_provider_key(config, model_id, provider_id, old_name):
            continue
        # Rename the local key suffix if the selected name embeds it.
        if selected == old_name or selected.endswith(f"-{old_name}"):
            prefix = selected[: -len(old_name)] if selected.endswith(f"-{old_name}") else ""
            target["key"] = f"{prefix}{new_name}"
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
        # 探测缓存属于旧凭据：更换 Key 后失效，下次刷新重新探测。
        key.pop("capabilities", None)
    if enabled is not None:
        key["enabled"] = bool(enabled)
    if allow_visitor is not None:
        key["allow_visitor"] = bool(allow_visitor)
    if enabled is False:
        _clear_unified_keys_from_provider(data, provider_id, key_name)
    if target_name != key_name:
        _rename_unified_key(data, provider_id, key_name, target_name)
        keys[target_name] = keys.pop(key_name)
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


def key_service_models(
    data: dict[str, Any], provider_id: str, key_name: str
) -> set[str]:
    """Model ids whose targets currently bind this provider key."""
    provider = require_provider(data, provider_id)
    require_key(provider, key_name)
    result: set[str] = set()
    for model_id, model in models(data).items():
        if not isinstance(model, dict):
            continue
        for target in model_targets(model):
            if (
                target.get("provider") == provider_id
                and target.get("key") == key_name
            ):
                result.add(str(model_id))
    return result


def set_key_service_models(
    data: dict[str, Any],
    provider_id: str,
    key_name: str,
    model_ids: list[str] | None,
) -> dict[str, Any]:
    """Atomically set which models a provider key serves (v4, key-centric).

    Mirrors the TUI model multi-select: every requested model is created when
    missing and gets a target binding this key (upstream_model = model id);
    models that previously bound this key but are no longer requested lose
    every target that uses this key, and models left without targets are
    removed (same empty-model semantics as deleting the provider key).
    Binding state is tracked per model container (any target using this key
    counts), so an unchecked model is fully detached from this key even when
    its target used a custom upstream model name.

    Returns {"added": [...], "removed": [...], "models_removed": [...]}.
    """
    provider = require_provider(data, provider_id)
    require_key(provider, key_name)
    desired = {_non_empty(str(item), "模型 ID") for item in (model_ids or [])}
    bound_by_model: dict[str, list[dict[str, Any]]] = {}
    for current_id, model in models(data).items():
        if not isinstance(model, dict):
            continue
        current_id = str(current_id)
        hits = [
            target
            for target in model_targets(model)
            if target.get("provider") == provider_id and target.get("key") == key_name
        ]
        if hits:
            bound_by_model[current_id] = hits
    added: list[str] = []
    removed: list[str] = []
    removed_models: list[str] = []
    all_models = models(data)
    for model_id in desired:
        model = all_models.get(model_id)
        if model is None:
            create_model(data, model_id)
        model = require_model(data, model_id)
        exists = any(
            target.get("provider") == provider_id
            and target.get("key") == key_name
            for target in model_targets(model)
        )
        if not exists:
            add_model_target(
                data,
                model_id,
                {"provider": provider_id, "key": key_name, "upstream_model": model_id},
            )
            added.append(model_id)
    for model_id, hits in bound_by_model.items():
        if model_id in desired:
            continue
        model = all_models.get(model_id)
        if not isinstance(model, dict):
            continue
        targets = model_targets(model)
        targets[:] = [
            target
            for target in targets
            if not (
                target.get("provider") == provider_id
                and target.get("key") == key_name
            )
        ]
        removed.append(model_id)
        if not targets:
            all_models.pop(model_id, None)
            removed_models.append(model_id)
    if removed:
        repair_unified_model(data)
    return {
        "added": sorted(added),
        "removed": sorted(removed),
        "models_removed": sorted(removed_models),
    }


def delete_provider_key(data: dict[str, Any], provider_id: str, key_name: str) -> set[str]:
    provider = require_provider(data, provider_id)
    keys = provider_keys(provider)
    require_key(provider, key_name)
    _clear_unified_keys_from_provider(data, provider_id, key_name)
    keys.pop(key_name)
    removed: set[str] = set()
    for model_id, model in list(models(data).items()):
        if not isinstance(model, dict):
            continue
        targets = model_targets(model)
        targets[:] = [
            target for target in targets
            if not (
                target.get("provider") == provider_id
                and target.get("key") == key_name
            )
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


def validate_targets(data: dict[str, Any], targets: list[dict[str, Any]]) -> None:
    if not targets:
        raise ConfigOperationError("targets 不能为空", status_code=422)
    for target in targets:
        if not isinstance(target, dict):
            raise ConfigOperationError("targets 必须是对象数组", status_code=422)
        provider_id = _non_empty(target.get("provider"), "target.provider")
        key_name = _non_empty(target.get("key"), "target.key")
        _non_empty(target.get("upstream_model"), "target.upstream_model")
        provider = require_provider(data, provider_id)
        require_key(provider, key_name)


def add_model_target(data: dict[str, Any], model_id: str, target: dict[str, Any]) -> None:
    validate_targets(data, [target])
    targets = model_targets(require_model(data, model_id))
    identity = (str(target.get("provider")), str(target.get("key")), str(target.get("upstream_model")))
    if any((str(item.get("provider")), str(item.get("key")), str(item.get("upstream_model"))) == identity for item in targets):
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


def _unique_provider_id(data: dict[str, Any], preferred: str) -> str:
    candidate = preferred
    suffix = 2
    while candidate in providers(data):
        candidate = f"{preferred}-{suffix}"
        suffix += 1
    return candidate


def create_model_key(data: dict[str, Any], model_id: str, key_name: str, api_key: str, *, base_url: str | None = None, enabled: bool = True, allow_visitor: bool = False, upstream_model: str | None = None, upstream_routes: dict[str, str | None] | None = None, update_upstream_routes: bool = False) -> None:
    """Bind a provider key to a model as one of its targets (v4)."""
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
    if key_name in provider_keys(provider):
        raise ConfigOperationError(
            f"供应商 {provider_id} 的 Key 已存在: {key_name}", status_code=409
        )
    create_provider_key(data, provider_id, key_name, api_key, enabled=enabled, allow_visitor=allow_visitor)
    upstream = _non_empty(upstream_model or model_id, "target.upstream_model")
    model_targets(model).append({"provider": provider_id, "key": key_name, "upstream_model": upstream})
    if update_upstream_routes:
        set_upstream_routes_for_base_url(data, target_url, upstream_routes)


def create_model_with_keys(data: dict[str, Any], model_id: str, *, aliases: list[str] | None = None, routing_mode: str = "round_robin", reasoning_effort: str | None = None, keys: list[dict[str, Any]] | None = None) -> None:
    create_model(data, model_id, aliases=aliases, routing_mode=routing_mode, reasoning_effort=reasoning_effort)
    for key in keys or []:
        create_model_key(data, model_id, str(key.get("name") or ""), str(key.get("api_key") or ""), base_url=key.get("base_url"), enabled=bool(key.get("enabled", True)), allow_visitor=bool(key.get("allow_visitor", False)), upstream_model=str(key.get("upstream_model") or model_id), upstream_routes=key.get("upstream_routes"), update_upstream_routes="upstream_routes" in key)


def _locate_model_target(data: dict[str, Any], model_id: str, key_name: str) -> tuple[dict[str, Any], dict[str, Any], str]:
    """Find the model target whose provider key matches ``key_name``.

    Returns (target, provider, provider_key_name). The key name may be the raw
    provider key name or the parsed model-level name that embeds the provider
    qualifier (``{provider}-{key}``).
    """
    model = require_model(data, model_id)
    for target in model_targets(model):
        provider_id = str(target.get("provider") or "")
        provider = require_provider(data, provider_id)
        raw_name = str(target.get("key") or "")
        if not raw_name:
            continue
        if key_name in {raw_name, f"{provider_id}-{raw_name}"}:
            return target, provider, raw_name
    raise ConfigOperationError(f"模型 {model_id} 的 key 不存在: {key_name}", status_code=404)


def _provider_key_referenced_by_other_model(
    data: dict[str, Any], model_id: str, provider_id: str, key_name: str
) -> bool:
    for current_id, model in models(data).items():
        if str(current_id) == model_id or not isinstance(model, dict):
            continue
        if any(
            target.get("provider") == provider_id and target.get("key") == key_name
            for target in model_targets(model)
        ):
            return True
    return False


def _provider_key_is_shared(
    data: dict[str, Any], model_id: str, provider_id: str, key_name: str
) -> bool:
    """Whether the provider key is referenced by more than this model."""
    return _provider_key_referenced_by_other_model(
        data, model_id, provider_id, key_name
    )


def _model_target_is_shared(data: dict[str, Any], model_id: str, provider_id: str, key_name: str) -> bool:
    return _provider_key_is_shared(data, model_id, provider_id, key_name)


def _clone_provider_key_target(
    data: dict[str, Any], model_id: str, target: dict[str, Any], provider: dict[str, Any], key_name: str, *, base_url: str | None = None
) -> tuple[dict[str, Any], dict[str, Any], str]:
    """Detach this model's target onto its own provider key clone when the
    provider key is shared with other models or the base_url changes."""
    source_url = normalize_base_url(provider.get("base_url"))
    target_url = normalize_base_url(base_url or source_url)
    current_provider_id = str(target.get("provider") or "")
    if target_url == source_url and not _provider_key_is_shared(
        data, model_id, current_provider_id, key_name
    ):
        return target, provider, key_name
    if target_url != source_url:
        preferred = target_url.split("://", 1)[-1].replace("/", "-").replace(":", "-") or "provider"
    else:
        preferred = f"{current_provider_id}-{model_id}"
    provider_id = _unique_provider_id(data, preferred)
    clone = deepcopy(provider)
    clone["base_url"] = target_url
    clone["keys"] = {}
    clone.pop("capabilities", None)
    clone["_amkr_model_key_clone"] = True
    providers(data)[provider_id] = clone
    clone["keys"][key_name] = deepcopy(provider_keys(provider)[key_name])
    if target_url == source_url and key_name in provider_keys(provider):
        # Copy sibling keys only when this is a pure share-detach (same URL).
        for sibling_name, sibling in provider_keys(provider).items():
            if sibling_name != key_name:
                clone["keys"].setdefault(sibling_name, deepcopy(sibling))
    target["provider"] = provider_id
    return target, clone, key_name


def update_model_key_local(data: dict[str, Any], model_id: str, key_name: str, *, new_name: str | None = None, api_key: str | None = None, base_url: str | None = None, update_base_url: bool = False, enabled: bool | None = None, allow_visitor: bool | None = None, upstream_routes: dict[str, str | None] | None = None, update_upstream_routes: bool = False) -> str:
    target, provider, raw_name = _locate_model_target(data, model_id, key_name)
    target, provider, raw_name = _clone_provider_key_target(
        data, model_id, target, provider, raw_name,
        base_url=(base_url if base_url is not None else (data.get("default_base_url") if update_base_url else None)),
    )
    key = require_key(provider, raw_name)
    actual = _non_empty(new_name, "Key 名称") if new_name is not None else key_name
    if actual != raw_name and actual in provider_keys(provider):
        raise ConfigOperationError(f"Key 已存在: {actual}", status_code=409)
    if api_key is not None:
        key["api_key"] = _non_empty(api_key, "API key")
        # 探测缓存属于旧凭据：更换 Key 后失效，下次刷新重新探测。
        key.pop("capabilities", None)
    if enabled is not None:
        key["enabled"] = bool(enabled)
    if allow_visitor is not None:
        key["allow_visitor"] = bool(allow_visitor)
    if actual != raw_name:
        provider_keys(provider)[actual] = provider_keys(provider).pop(raw_name)
        target["key"] = actual
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
    target, provider, raw_name = _locate_model_target(data, model_id, key_name)
    model_targets(model).remove(target)
    provider_id = str(target.get("provider") or "")
    if not any(
        isinstance(current, dict)
        and any(t.get("provider") == provider_id and t.get("key") == raw_name for t in model_targets(current))
        for current in models(data).values()
    ):
        # Provider key no longer used by any model -> remove provider key,
        # then provider when empty (except clones may keep siblings).
        keys = provider_keys(provider)
        keys.pop(raw_name, None)
        if not keys or provider.get("_amkr_model_key_clone"):
            if provider.get("_amkr_model_key_clone"):
                # Clone that is no longer referenced by any model disappears.
                if not any(
                    isinstance(current, dict)
                    and any(t.get("provider") == provider_id for t in model_targets(current))
                    for current in models(data).values()
                ):
                    providers(data).pop(provider_id, None)
            elif not keys:
                providers(data).pop(provider_id, None)
    if not model_targets(model):
        models(data).pop(model_id, None)
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
            # 探测缓存是机器本地信息（各 Key 看到的模型清单），不随 Key 配置迁移。
            for key in provider_keys(provider).values():
                if isinstance(key, dict):
                    key.pop("capabilities", None)
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
    provider_map: dict[str, str] = {}
    key_map: dict[tuple[str, str], str | None] = {}
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
                current_provider["keys"] = {}
                current_provider.pop("capabilities", None)
                current_providers[target_id] = current_provider
        else:
            current_provider = deepcopy(source_provider)
            current_provider["keys"] = {}
            current_provider.pop("capabilities", None)
            current_providers[target_id] = current_provider
        target_keys = provider_keys(current_provider)
        existing_secrets = {str(key.get("api_key") or "") for key in target_keys.values() if isinstance(key, dict)}
        for source_name, source_key in provider_keys(source_provider).items():
            secret = str(source_key.get("api_key") or "") if isinstance(source_key, dict) else ""
            if secret in existing_secrets:
                skipped_keys += 1
                key_map[(str(source_id), str(source_name))] = None
                continue
            destination, base = str(source_name), str(source_name)
            suffix = 2
            while destination in target_keys:
                destination = f"{base}-{suffix}"
                suffix += 1
            target_keys[destination] = deepcopy(source_key)
            key_map[(str(source_id), str(source_name))] = destination
            existing_secrets.add(secret)
            added_keys += 1
        provider_map[str(source_id)] = target_id
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
        identities = {(str(t.get("provider") or ""), str(t.get("key") or ""), str(t.get("upstream_model") or model_id)) for t in targets}
        for source_target in model_targets(source_model):
            source_provider_id = str(source_target.get("provider") or "")
            mapped = provider_map.get(source_provider_id)
            if not mapped:
                continue
            source_key = str(source_target.get("key") or "")
            mapped_key = key_map.get((source_provider_id, source_key))
            if mapped_key is None:
                # 对应 Key 因 secret 重复被跳过或未迁移：该模型绑定无法合并。
                continue
            new_target = {"provider": mapped, "key": mapped_key, "upstream_model": str(source_target.get("upstream_model") or model_id)}
            identity = (new_target["provider"], new_target["key"], new_target["upstream_model"])
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
