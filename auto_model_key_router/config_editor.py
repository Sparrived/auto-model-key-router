from __future__ import annotations

from copy import deepcopy
from dataclasses import dataclass
from datetime import datetime, timezone
import json
import sys
from time import monotonic, time
from pathlib import Path
from typing import Any

import httpx
from rich.console import Group
from rich.live import Live
from rich.table import Table

from .config import (
    UPSTREAM_ROUTE_DEFAULT_PATHS,
    UPSTREAM_ROUTE_LABELS,
    UPSTREAM_ROUTE_MODES,
    CONFIG_VERSION,
    RouterConfig,
    default_endpoint_capabilities_path,
    default_metrics_db_path,
    generate_local_api_key,
    load_config_data,
    normalize_upstream_base_url,
    normalize_upstream_route_path,
    save_config_data,
    upstream_route_path,
)
from .config_service import commit_config_data
from .formatting import compact_url, key_fingerprint, short_text
from .proxy_support import _join_url
from .service import restart_service_after_config_change
from .tui import (
    ResultPage,
    clear_terminal_history,
    confirm_choice,
    console,
    content_scroll_offset,
    mouse_wheel_mode,
    open_config_file,
    page_title,
    posix_input_mode,
    prompt_text,
    read_key_responsive,
    run_submodule,
    section_panel,
    select_multiple,
    select_option,
    shortcut_text,
    should_handle_wheel,
    show_result_page,
    terminal_frame_state,
)
from .visitor import visitor_feature_available


VISITOR_KEY_STYLE = "bold bright_magenta"
PROBE_ROUTE_MODES = ("openai", "anthropic", "responses")
FORM_DRAFTS: dict[str, dict[str, str]] = {}


def form_draft(name: str) -> dict[str, str]:
    return FORM_DRAFTS.setdefault(name, {})


@dataclass(frozen=True)
class KeyProbeResult:
    model_id: str
    key_name: str
    mode: str
    label: str
    path: str
    url: str
    available: bool
    status_code: int | None
    duration_ms: int
    error: str


def key_display_name(key: dict[str, Any], fallback: str, width: int = 28) -> str:
    name = str(key.get("name") or fallback)
    if visitor_feature_available() and key.get("allow_visitor", False):
        return f"[{VISITOR_KEY_STYLE}]{short_text(name, width)}[/]"
    return short_text(name, width)


def service_management_base_url(data: dict[str, Any]) -> str:
    host = str(data.get("host") or "127.0.0.1")
    port = int(data.get("port") or 8000)
    connect_host = "127.0.0.1" if host in {"0.0.0.0", "::"} else host
    if ":" in connect_host and not connect_host.startswith("["):
        connect_host = f"[{connect_host}]"
    return f"http://{connect_host}:{port}"


def management_headers(data: dict[str, Any]) -> dict[str, str] | None:
    local_api_key = str(data.get("local_api_key") or "").strip()
    if not local_api_key:
        return None
    return {"Authorization": f"Bearer {local_api_key}"}


def load_native_endpoint_states_from_file(data: dict[str, Any]) -> dict[str, dict[str, Any]]:
    path = Path(
        str(
            data.get("endpoint_capabilities_path")
            or data.get("key_state_path")
            or default_endpoint_capabilities_path()
        )
    )
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}
    states = raw.get("endpoint_capabilities", raw.get("url_native_support", {}))
    if not isinstance(states, dict):
        return {}
    return {
        str(key): _native_endpoint_state_payload(value)
        for key, value in states.items()
        if _native_endpoint_state_payload(value) is not None
    }


def fetch_native_endpoint_states(data: dict[str, Any], timeout: float = 0.5) -> dict[str, dict[str, Any]]:
    states = load_native_endpoint_states_from_file(data)
    try:
        response = httpx.get(f"{service_management_base_url(data)}/health", timeout=timeout)
        response.raise_for_status()
        service_states = response.json().get("native_endpoint_states", {})
    except (httpx.RequestError, httpx.HTTPStatusError, ValueError, TypeError):
        return states
    if isinstance(service_states, dict):
        states.update(
            {
                str(key): value
                for key, value in service_states.items()
                if isinstance(value, dict)
            }
        )
    return states


def _native_endpoint_state_payload(value: Any) -> dict[str, Any] | None:
    if isinstance(value, bool):
        return {"supported": value, "reason": "legacy", "expires_in_seconds": None}
    if isinstance(value, dict) and isinstance(value.get("supported"), bool):
        payload = dict(value)
        expires_at = payload.get("expires_at")
        if expires_at is None and payload.get("ttl_seconds"):
            expires_at = float(payload.get("checked_at") or 0) + float(
                payload.get("ttl_seconds") or 0
            )
        if expires_at is not None:
            payload["expires_in_seconds"] = max(0, int(float(expires_at) - time()))
        return payload
    return None


def format_visitor_status_text(visitor_allowed: bool, visitor_installed: bool) -> str:
    if visitor_installed:
        return (
            f"[{VISITOR_KEY_STYLE}]允许[/]" if visitor_allowed else "[dim]禁止[/dim]"
        )
    if visitor_allowed:
        return f"[{VISITOR_KEY_STYLE}]已配置，但 visitor extra 未安装[/]"
    return "[dim]功能未安装[/dim]"


def raw_upstream_routes_by_url(data: dict[str, Any]) -> dict[str, Any]:
    routes = data.get("upstream_routes")
    return routes if isinstance(routes, dict) else {}


def upstream_routes_for_base_url(data: dict[str, Any], base_url: str) -> dict[str, str]:
    normalized_base_url = normalize_upstream_base_url(base_url)
    routes: dict[str, str] = {}
    for raw_base_url, raw_routes in raw_upstream_routes_by_url(data).items():
        if (
            normalize_upstream_base_url(raw_base_url) == normalized_base_url
            and isinstance(raw_routes, dict)
        ):
            routes.update(raw_routes)
    for model in data.get("models", []):
        for key in model.get("keys", []):
            key_base_url = normalize_upstream_base_url(
                key.get("base_url") or data.get("default_base_url") or "https://api.openai.com"
            )
            legacy_routes = key.get("upstream_routes")
            if key_base_url == normalized_base_url and isinstance(legacy_routes, dict):
                routes.update(legacy_routes)
    return routes


def set_upstream_routes_for_base_url(
    data: dict[str, Any], base_url: str, routes: dict[str, str]
) -> None:
    normalized_base_url = normalize_upstream_base_url(base_url)
    routes_by_url = raw_upstream_routes_by_url(data)
    if routes:
        routes_by_url[normalized_base_url] = routes
        data["upstream_routes"] = routes_by_url
    else:
        routes_by_url.pop(normalized_base_url, None)
        if routes_by_url:
            data["upstream_routes"] = routes_by_url
        else:
            data.pop("upstream_routes", None)
    for model in data.get("models", []):
        for key in model.get("keys", []):
            key_base_url = normalize_upstream_base_url(
                key.get("base_url") or data.get("default_base_url") or "https://api.openai.com"
            )
            if key_base_url == normalized_base_url:
                key.pop("upstream_routes", None)


def upstream_route_support_text(
    routes: dict[str, str],
    mode: str,
    *,
    native_first: bool = True,
    native_state: dict[str, Any] | None = None,
) -> str:
    path = upstream_route_path(routes, mode)
    status = native_endpoint_support_text(native_state)
    if mode in routes:
        return f"[green]自定义原生[/green] {path}{status}"
    if mode == "openai":
        return f"[green]原生[/green] {path}"
    if mode == "anthropic" and native_first:
        return f"[cyan]自动探测[/cyan] {path}{status}"
    if mode == "responses":
        return f"[cyan]自动探测[/cyan] {path}{status}"
    return (
        f"[yellow]转换[/yellow] {UPSTREAM_ROUTE_DEFAULT_PATHS['openai']}"
    )


def native_endpoint_support_text(state: dict[str, Any] | None) -> str:
    if not state:
        return "\n[dim]探测: 未测试[/dim]"
    reason = str(state.get("reason") or "-")
    if state.get("supported") is True:
        return f"\n[green]探测: 支持[/green] [dim]{reason}[/dim]"
    expires = state.get("expires_in_seconds")
    retry = f"，{int(expires)}s 后重试" if isinstance(expires, int) else ""
    return f"\n[yellow]探测: 回退缓存[/yellow] [dim]{reason}{retry}[/dim]"


def upstream_routes_panel(
    data: dict[str, Any],
    model_or_base_url: dict[str, Any] | str,
    key_index: int | None = None,
) -> Any:
    if isinstance(model_or_base_url, dict):
        model = model_or_base_url
        if key_index is None:
            key_index = 0
        key = model["keys"][key_index]
        base_url = str(
            key.get("base_url")
            or data.get("default_base_url")
            or "https://api.openai.com"
        )
        key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    else:
        base_url = model_or_base_url
        model = {"id": "upstream"}
        key = {"name": str(base_url)}
        key_name = str(base_url)
    normalized_base_url = normalize_upstream_base_url(base_url)
    routes = upstream_routes_for_base_url(data, normalized_base_url)
    native_states = fetch_native_endpoint_states(data)
    native_first = any(
        bool(model.get("native_first", True))
        for model in data.get("models", [])
        for key in model.get("keys", [])
        if normalize_upstream_base_url(
            key.get("base_url")
            or data.get("default_base_url")
            or "https://api.openai.com"
        )
        == normalized_base_url
    )
    table = Table(show_header=True, box=None, expand=True)
    table.add_column("模式", style="cyan", ratio=1)
    table.add_column("原生支持/上游路径", ratio=3)
    for mode in UPSTREAM_ROUTE_MODES:
        table.add_row(
            UPSTREAM_ROUTE_LABELS[mode],
            upstream_route_support_text(
                routes,
                mode,
                native_first=native_first,
                native_state=native_states.get(
                    f"{normalized_base_url}|{upstream_route_path(routes, mode).strip('/')}"
                ),
            ),
        )
    base_url = compact_url(normalized_base_url or "-", 56)
    return Group(
        section_panel(
            f"模型: [bold]{short_text(model['id'], 48)}[/bold]\n"
            f"Key: {key_display_name(key, key_name, 48)}\n"
            f"上游: [bold]{base_url}[/bold]",
            "Key 信息",
            "cyan",
        ),
        section_panel(
            table,
            "三种模式原生支持",
            "magenta",
            "[dim]自定义值可填路径前缀，如 anthropic/，或完整路径 anthropic/v1/messages[/dim]",
        ),
    )


def _open_config_on_key(path: Path, key: str) -> str | None:
    if key in {"o", "O"}:
        open_config_file(path)
    return None


def load_v2_config_data(path: Path) -> dict[str, Any]:
    return load_config_data(path)


def raw_providers(data: dict[str, Any]) -> dict[str, Any]:
    providers = data.setdefault("providers", {})
    if not isinstance(providers, dict):
        raise ValueError("providers 必须是对象")
    return providers


def raw_v2_models(data: dict[str, Any]) -> dict[str, Any]:
    models = data.setdefault("models", {})
    if not isinstance(models, dict):
        raise ValueError("models 必须是对象")
    return models


def provider_keys(provider: dict[str, Any]) -> dict[str, Any]:
    keys = provider.setdefault("keys", {})
    if not isinstance(keys, dict):
        raise ValueError("provider.keys 必须是对象")
    return keys


def provider_pools(provider: dict[str, Any]) -> dict[str, Any]:
    pools = provider.setdefault("pools", {})
    if not isinstance(pools, dict):
        raise ValueError("provider.pools 必须是对象")
    return pools


def pool_key_names(pool: Any) -> list[str]:
    if isinstance(pool, dict):
        keys = pool.get("keys", [])
    else:
        keys = pool
    if not isinstance(keys, list):
        return []
    return [str(key_name) for key_name in keys]


def pool_available_models(pool: Any) -> list[str]:
    if not isinstance(pool, dict):
        return []
    models = pool.get("available_models", pool.get("models", []))
    return [str(model_id) for model_id in models if str(model_id)] if isinstance(models, list) else []


def pool_enabled_models(pool: Any) -> list[str]:
    if not isinstance(pool, dict):
        return []
    models = pool.get("models", [])
    return [str(model_id) for model_id in models if str(model_id)] if isinstance(models, list) else []


def ensure_default_pool(provider: dict[str, Any]) -> None:
    keys = provider_keys(provider)
    pools = provider_pools(provider)
    if keys and "default" not in pools:
        pools["default"] = {"keys": list(keys)}


def pool_probe_models(
    provider: dict[str, Any],
    key_names: list[str],
    timeout: float = 15.0,
    manual_models: list[str] | None = None,
) -> dict[str, Any]:
    base_url = str(provider.get("base_url") or "").strip()
    keys = provider_keys(provider)
    routes = provider.get("routes") if isinstance(provider.get("routes"), dict) else {}
    key_models: dict[str, list[str]] = {}
    model_counts: dict[str, int] = {}
    route_results: dict[str, dict[str, bool]] = {}
    for key_name in key_names:
        key = keys.get(key_name)
        if not isinstance(key, dict):
            continue
        api_key = str(key.get("api_key") or "")
        if not base_url or not api_key:
            key_models[key_name] = []
            continue
        discovered = discover_upstream_models(base_url, api_key, set(), timeout=timeout)
        key_models[key_name] = discovered
        for model_id in discovered:
            model_counts[model_id] = model_counts.get(model_id, 0) + 1
    common_models = sorted(
        model_id for model_id, count in model_counts.items() if count == len(key_names)
    )
    manual_models = sorted(dict.fromkeys(manual_models or []))
    if not model_counts and manual_models:
        common_models = manual_models
        model_counts = {model_id: len(key_names) for model_id in manual_models}
        for key_name in key_names:
            key_models[key_name] = manual_models
    candidate_models = common_models or sorted(model_counts)
    probe_model = candidate_models[0] if candidate_models else None
    if probe_model:
        probe_data = {"upstream_routes": {base_url: routes} if routes else {}}
        for key_name in key_names:
            key = keys.get(key_name)
            if not isinstance(key, dict):
                continue
            probe_key = dict(key)
            probe_key["name"] = key_name
            probe_key["base_url"] = base_url
            route_results[key_name] = {
                result.mode: result.available
                for result in probe_key_availability(
                    probe_data, probe_model, probe_key, timeout=timeout
                )
            }
    return {
        "models": common_models,
        "all_models": sorted(model_counts),
        "key_models": key_models,
        "routes": route_results,
        "manual_models": bool(manual_models and common_models == manual_models),
        "checked_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    }


def probe_provider_key_capabilities(
    provider: dict[str, Any],
    key_names: list[str],
    timeout: float = 15.0,
) -> dict[str, Any]:
    base_url = str(provider.get("base_url") or "").strip()
    keys = provider_keys(provider)
    key_models: dict[str, list[str]] = {}
    errors: dict[str, str] = {}
    successful: list[set[str]] = []
    for key_name in key_names:
        key = keys.get(key_name)
        api_key = str(key.get("api_key") or "") if isinstance(key, dict) else ""
        if not base_url:
            models, error = [], "缺少 Base URL"
        elif not isinstance(key, dict):
            models, error = [], "Key 不存在"
        elif not api_key:
            models, error = [], "API Key 为空"
        else:
            models, error = discover_upstream_models_result(
                base_url, api_key, set(), timeout=timeout
            )
        key_models[key_name] = models
        if error:
            errors[key_name] = error
        else:
            successful.append(set(models))
    return {
        "models": sorted(set.intersection(*successful)) if successful else [],
        "all_models": sorted(set().union(*successful)) if successful else [],
        "key_models": key_models,
        "errors": errors,
    }


def apply_pool_probe(
    provider: dict[str, Any],
    pool_name: str,
    timeout: float = 15.0,
    manual_models: list[str] | None = None,
) -> dict[str, Any]:
    pool = provider_pools(provider).setdefault(pool_name, {"keys": []})
    key_names = pool_key_names(pool)
    probe = pool_probe_models(
        provider, key_names, timeout=timeout, manual_models=manual_models
    )
    if isinstance(pool, dict):
        available_models = probe["models"]
        pool["available_models"] = available_models
        pool["all_available_models"] = probe["all_models"]
        pool["key_models"] = probe["key_models"]
        pool["routes"] = probe["routes"]
        pool["manual_models"] = probe["manual_models"]
        pool["checked_at"] = probe["checked_at"]
    return probe


def parse_model_id_list(value: str) -> list[str]:
    return [model_id.strip() for model_id in value.split(",") if model_id.strip()]


def prompt_pool_enabled_models(pool: dict[str, Any]) -> list[str]:
    enabled = set(pool_enabled_models(pool))
    raw_available = pool.get("available_models")
    available = (
        {str(model_id) for model_id in raw_available if str(model_id)}
        if isinstance(raw_available, list)
        else set(enabled)
    )
    raw_all_available = pool.get("all_available_models")
    all_available = (
        {str(model_id) for model_id in raw_all_available if str(model_id)}
        if isinstance(raw_all_available, list)
        else set(available)
    )
    model_ids = sorted(all_available | enabled)
    if model_ids:
        custom = "__custom__"
        options = []
        for model_id in model_ids:
            if model_id in available:
                label = model_id
            elif model_id in all_available:
                label = f"[yellow]{model_id} · 仅部分 Key 可用[/yellow]"
            else:
                label = f"[yellow]{model_id} · 本次未探测到[/yellow]"
            options.append((model_id, label))
        selected = select_multiple(
            "模型池",
            options + [(custom, "自定义输入")],
            content=section_panel(
                "选择该模型池启用的模型。探测状态仅供参考，不会自动取消已有选择。",
                "启用模型",
                "cyan",
            ),
            checked_values=enabled,
        )
        if custom not in selected:
            return selected
    text = prompt_text(
        "模型池",
        "手动可用模型，多个用逗号分隔",
        default=", ".join(sorted(enabled)),
    ).strip()
    return parse_model_id_list(text)


def pools_containing_key(provider: dict[str, Any], key_name: str) -> set[str]:
    return {
        str(pool_name)
        for pool_name, pool in provider_pools(provider).items()
        if key_name in pool_key_names(pool)
    }


def duplicate_pool_memberships(
    data: dict[str, Any],
) -> list[tuple[str, str, list[str]]]:
    duplicates: list[tuple[str, str, list[str]]] = []
    for provider_id, provider in sorted(raw_providers(data).items()):
        memberships: dict[str, list[str]] = {}
        for pool_name, pool in sorted(provider_pools(provider).items()):
            for key_name in pool_key_names(pool):
                memberships.setdefault(key_name, []).append(str(pool_name))
        duplicates.extend(
            (str(provider_id), key_name, memberships.get(key_name, []))
            for key_name in sorted(provider_keys(provider))
            if len(memberships.get(key_name, [])) != 1
        )
    return duplicates


def repair_duplicate_pool_memberships_interactively(path: Path) -> bool:
    data = load_config_data(path)
    duplicates = duplicate_pool_memberships(data)
    if not duplicates:
        return False
    providers = raw_providers(data)
    for provider_id, key_name, pool_names in duplicates:
        provider = providers[provider_id]
        pools = provider_pools(provider)
        for pool_name, pool in list(pools.items()):
            if not isinstance(pool, dict):
                pools[pool_name] = {
                    "keys": pool_key_names(pool),
                    "models": [],
                }
        console.print(
            section_panel(
                f"供应商: [bold]{provider_id}[/bold]\n"
                f"Key: [bold]{key_name}[/bold]\n"
                f"当前模型池: [yellow]{', '.join(pool_names) or '未分配'}[/yellow]\n"
                "请选择唯一保留的模型池，或新建模型池。",
                "修复重复模型池归属",
                "yellow",
            )
        )
        selected_pool = select_or_enter_pool_name(
            pools,
            default=pool_names[0] if pool_names else "default",
        )
        if not selected_pool:
            return False
        for pool in pools.values():
            if isinstance(pool, dict):
                pool["keys"] = [
                    current_key
                    for current_key in pool_key_names(pool)
                    if current_key != key_name
                ]
        target_pool = pools.setdefault(selected_pool, {"keys": [], "models": []})
        target_pool.setdefault("keys", []).append(key_name)
    save_config_data(path, data)
    return True


def fallback_unified_model(models: dict[str, Any]) -> str | None:
    for model_id, model in sorted(models.items()):
        if model_targets(model):
            return str(model_id)
    return None


def model_targets(model: dict[str, Any]) -> list[dict[str, Any]]:
    targets = model.setdefault("targets", [])
    if not isinstance(targets, list):
        raise ValueError("model.targets 必须是数组")
    return targets


def enable_pool_models(
    data: dict[str, Any], provider_id: str, pool_name: str, enabled_models: list[str]
) -> None:
    pool = provider_pools(raw_providers(data)[provider_id])[pool_name]
    if isinstance(pool, dict):
        pool["models"] = enabled_models
    models = raw_v2_models(data)
    enabled = set(enabled_models)
    for model_id in enabled_models:
        model = models.setdefault(model_id, {"targets": []})
        targets = model_targets(model)
        if not any(
            target.get("provider") == provider_id
            and target.get("pool") == pool_name
            and str(target.get("upstream_model") or model_id) == model_id
            for target in targets
        ):
            targets.append(
                {
                    "provider": provider_id,
                    "pool": pool_name,
                    "upstream_model": model_id,
                }
            )
    for model_id, model in list(models.items()):
        targets = model_targets(model)
        targets[:] = [
            target
            for target in targets
            if not (
                target.get("provider") == provider_id
                and target.get("pool") == pool_name
                and str(target.get("upstream_model") or model_id) == model_id
                and model_id not in enabled
            )
        ]
        if not targets and not model.get("aliases"):
            models.pop(model_id, None)


def v2_summary_panel(data: dict[str, Any]) -> Any:
    providers = raw_providers(data)
    models = raw_v2_models(data)
    visitor_installed = visitor_feature_available()

    provider_table = Table(show_header=True, header_style="bold cyan", expand=True)
    provider_table.add_column("供应商", ratio=1)
    provider_table.add_column("Base URL", ratio=2)
    provider_table.add_column("Keys", justify="right")
    provider_table.add_column("Pools", justify="right")
    if visitor_installed:
        provider_table.add_column("访客", justify="right")
    provider_table.add_column("路由", ratio=2)
    for provider_id, provider in sorted(providers.items()):
        keys = provider_keys(provider)
        pools = provider_pools(provider)
        visitor_keys = sum(1 for key in keys.values() if key.get("allow_visitor"))
        routes = provider.get("routes") if isinstance(provider.get("routes"), dict) else {}
        route_text = ", ".join(sorted(routes)) if routes else "默认"
        row = [
            short_text(str(provider_id), 18),
            compact_url(str(provider.get("base_url") or "-"), 42),
            str(len(keys)),
            str(len(pools)),
            short_text(route_text, 28),
        ]
        if visitor_installed:
            row.insert(4, format_visitor_status_text(visitor_keys > 0, visitor_installed))
        provider_table.add_row(*row)
    if not provider_table.rows:
        empty_row = ["-", "[yellow]暂无供应商[/yellow]", "0", "0", "-"]
        if visitor_installed:
            empty_row.insert(4, "-")
        provider_table.add_row(*empty_row)

    model_table = Table(show_header=True, header_style="bold cyan", expand=True)
    model_table.add_column("本地模型", ratio=2)
    model_table.add_column("别名", ratio=2)
    model_table.add_column("路由模式", ratio=1)
    model_table.add_column("Keys", justify="right")
    for model_id, model in sorted(models.items()):
        aliases = ", ".join(str(alias) for alias in model.get("aliases", []) if str(alias))
        key_count = 0
        for target in model_targets(model):
            provider = providers.get(str(target.get("provider") or ""), {})
            if target.get("pool"):
                pool = provider_pools(provider).get(str(target.get("pool") or ""), {})
                key_count += len(pool_key_names(pool))
            elif target.get("key"):
                key_count += 1
        model_table.add_row(
            short_text(str(model_id), 28),
            short_text(aliases or "-", 36),
            str(model.get("routing_mode") or "round_robin"),
            str(key_count),
        )
    if not model_table.rows:
        model_table.add_row("-", "-", "-", "0")
    return Group(
        section_panel(provider_table, "供应商 Key", "cyan"),
        section_panel(model_table, "模型设置", "magenta"),
    )


def commit_v2_config(path: Path, data: dict[str, Any], old_config: RouterConfig) -> Any:
    new_config = commit_config_data(path, data, old_config).new_config
    return restart_service_after_config_change(path, old_config, new_config)


def select_provider(data: dict[str, Any], title: str) -> str | None:
    providers = raw_providers(data)
    if not providers:
        return None
    options = [
        (
            str(index + 1),
            f"{short_text(provider_id, 22)} · {compact_url(str(provider.get('base_url') or '-'), 42)} · {len(provider_keys(provider))} Key",
        )
        for index, (provider_id, provider) in enumerate(sorted(providers.items()))
    ] + [("0", "返回")]
    choice = select_option(title, options)
    if choice == "0":
        return None
    return sorted(providers)[int(choice) - 1]


def select_provider_key(data: dict[str, Any], title: str) -> tuple[str, str] | None:
    provider_id = select_provider(data, "选择供应商")
    if provider_id is None:
        return None
    keys = provider_keys(raw_providers(data)[provider_id])
    if not keys:
        return None
    options = [
        (
            str(index + 1),
            f"{short_text(key_name, 26)} · {'启用' if key.get('enabled', True) else '禁用'} · {key_fingerprint(str(key.get('api_key') or ''))}",
        )
        for index, (key_name, key) in enumerate(sorted(keys.items()))
    ] + [("0", "返回")]
    choice = select_option(title, options)
    if choice == "0":
        return None
    return provider_id, sorted(keys)[int(choice) - 1]


def select_provider_pool(data: dict[str, Any], title: str) -> tuple[str, str] | None:
    provider_id = select_provider(data, "选择供应商")
    if provider_id is None:
        return None
    provider = raw_providers(data)[provider_id]
    ensure_default_pool(provider)
    pools = provider_pools(provider)
    if not pools:
        return None
    options = [
        (
            str(index + 1),
            f"{short_text(pool_name, 26)} · {len(pool_key_names(pool))} Key",
        )
        for index, (pool_name, pool) in enumerate(sorted(pools.items()))
    ] + [("0", "返回")]
    choice = select_option(title, options)
    if choice == "0":
        return None
    return provider_id, sorted(pools)[int(choice) - 1]


def select_v2_model(data: dict[str, Any], title: str) -> str | None:
    models = raw_v2_models(data)
    if not models:
        return None
    options = [
        (
            str(index + 1),
            f"{short_text(model_id, 30)} · {len(model_targets(model))} Target · {model.get('routing_mode') or 'round_robin'}",
        )
        for index, (model_id, model) in enumerate(sorted(models.items()))
    ] + [("0", "返回")]
    choice = select_option(title, options)
    if choice == "0":
        return None
    return sorted(models)[int(choice) - 1]


def select_or_enter_model_id(
    title: str,
    prompt: str,
    models: dict[str, Any],
    *,
    default: str,
) -> str:
    model_ids = sorted(models)
    if not model_ids:
        return prompt_text(title, prompt, default=default).strip()
    options = [(model_id, short_text(model_id, 48)) for model_id in model_ids]
    options.extend([("__custom__", "自定义输入"), ("0", "返回")])
    selected = model_ids.index(default) if default in model_ids else 0
    choice = select_option(title, options, selected=selected)
    if choice == "0":
        return ""
    if choice == "__custom__":
        return prompt_text(title, prompt, default=default).strip()
    return choice


def select_pool_model_id(
    title: str,
    prompt: str,
    provider: dict[str, Any],
    pool_name: str,
    *,
    default: str,
) -> str:
    pool = provider_pools(provider).get(pool_name, {})
    model_ids = sorted(dict.fromkeys(pool_enabled_models(pool) or pool_available_models(pool)))
    if not model_ids:
        return prompt_text(title, prompt, default=default).strip()
    options = [(model_id, short_text(model_id, 48)) for model_id in model_ids]
    options.extend([("__custom__", "自定义输入"), ("0", "返回")])
    selected = model_ids.index(default) if default in model_ids else 0
    choice = select_option(title, options, selected=selected)
    if choice == "0":
        return ""
    if choice == "__custom__":
        return prompt_text(title, prompt, default=default).strip()
    return choice


def select_or_enter_pool_name(
    pools: dict[str, Any],
    *,
    default: str,
) -> str:
    pool_names = sorted(pools)
    if not pool_names:
        return prompt_text("模型池", "Pool 名称", default=default).strip()
    options = [(pool_name, pool_name) for pool_name in pool_names]
    options.extend([("__custom__", "新建/自定义 Pool 名称"), ("0", "返回")])
    selected = pool_names.index(default) if default in pool_names else 0
    choice = select_option("模型池", options, selected=selected)
    if choice == "0":
        return ""
    if choice == "__custom__":
        return prompt_text("模型池", "Pool 名称", default=default).strip()
    return choice


def add_provider_key_interactively(path: Path) -> Any:
    draft = form_draft("add_provider_key")
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    providers = raw_providers(data)
    existing = sorted(providers)
    provider_choice = select_option(
        "供应商",
        [("n", "新供应商")]
        + [(str(index + 1), provider_id) for index, provider_id in enumerate(existing)]
        + [("0", "返回")],
        content=v2_summary_panel(data),
    )
    if provider_choice == "0":
        return None
    if provider_choice == "n":
        provider_id = prompt_text("添加供应商", "供应商 ID", default=draft.get("provider_id", "openai")).strip()
        draft["provider_id"] = provider_id
        if not provider_id:
            return section_panel("[red]供应商 ID 不能为空[/red]", "添加失败", "red")
        if provider_id in providers:
            return section_panel(f"[red]供应商已存在: {provider_id}[/red]", "添加失败", "red")
        base_url = prompt_text(
            "添加供应商", "Base URL", default=draft.get("base_url", "https://api.openai.com")
        ).strip()
        draft["base_url"] = base_url
        providers[provider_id] = {"base_url": base_url, "keys": {}, "pools": {}}
    else:
        provider_id = existing[int(provider_choice) - 1]
    provider = providers[provider_id]
    keys = provider_keys(provider)
    key_name = prompt_text(
        "添加供应商 Key", "Key 名称", default=draft.get("key_name", f"key-{len(keys) + 1}")
    ).strip()
    draft["key_name"] = key_name
    if not key_name:
        return section_panel("[red]Key 名称不能为空[/red]", "添加失败", "red")
    if key_name in keys:
        return section_panel(f"[red]Key 已存在: {key_name}[/red]", "添加失败", "red")
    api_key = prompt_text("添加供应商 Key", "API key", password=True).strip()
    draft["api_key"] = api_key
    if not api_key:
        return section_panel("[red]API key 不能为空[/red]", "添加失败", "red")
    pools = provider_pools(provider)
    pool_name = select_or_enter_pool_name(
        pools,
        default=draft.get("pool_name", "default"),
    )
    draft["pool_name"] = pool_name
    if not pool_name:
        return section_panel("[yellow]配置未变化。[/yellow]", "添加 Key", "yellow")
    keys[key_name] = {"api_key": api_key, "enabled": True}
    pool = pools.setdefault(pool_name, {"keys": [], "models": []})
    pool_keys = pool.setdefault("keys", [])
    if key_name not in pool_keys:
        pool_keys.append(key_name)
    with console.status(f"[cyan]正在探测 {pool_name} 模型池可用模型和路由...[/cyan]", spinner="dots"):
        probe = apply_pool_probe(provider, pool_name)
    enabled_models = prompt_pool_enabled_models(pool)
    enable_pool_models(data, provider_id, pool_name, enabled_models)
    restart = commit_v2_config(path, data, old_config)
    FORM_DRAFTS.pop("add_provider_key", None)
    return Group(
        section_panel(
            f"供应商: [bold]{provider_id}[/bold]\nKey: [bold]{key_name}[/bold]\n上游: [bold]{compact_url(str(provider.get('base_url') or '-'), 56)}[/bold]\n模型池: [bold]{pool_name}[/bold]\n已启用模型: [bold]{len(enabled_models)}[/bold]\n共同可用模型: [bold]{len(probe.get('models') or [])}[/bold]",
            "添加完成",
            "green",
        ),
        restart,
    )


def manage_provider_keys_interactively(path: Path) -> None:
    while True:
        data = load_v2_config_data(path)
        selected = select_provider_key(data, "选择供应商 Key")
        if selected is None:
            return
        provider_id, key_name = selected
        provider = raw_providers(data)[provider_id]
        key = provider_keys(provider)[key_name]
        visitor_installed = visitor_feature_available()
        options = [
            ("1", "开关"),
            ("2", "重命名"),
            ("3", "替换 API key"),
        ]
        if visitor_installed:
            options.append(("4", "访客访问"))
        options.extend([("5", "删除"), ("0", "返回")])
        lines = [
            f"供应商: [bold]{provider_id}[/bold]",
            f"Key: [bold]{key_name}[/bold]",
            f"状态: [bold]{'启用' if key.get('enabled', True) else '禁用'}[/bold]",
        ]
        if visitor_installed:
            lines.append(
                f"访客: {format_visitor_status_text(bool(key.get('allow_visitor')), True)}"
            )
        lines.append(f"指纹: [bold]{key_fingerprint(str(key.get('api_key') or ''))}[/bold]")
        choice = select_option(
            f"{provider_id}/{key_name}",
            options,
            content=section_panel(
                "\n".join(lines),
                "Key 信息",
                "cyan",
            ),
        )
        if choice == "0":
            continue
        clear_terminal_history()
        result = update_provider_key_interactively(path, provider_id, key_name, choice)
        if result is not None:
            show_result_page("供应商 Key", result)


def update_provider_key_interactively(
    path: Path, provider_id: str, key_name: str, choice: str
) -> Any:
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    provider = raw_providers(data)[provider_id]
    keys = provider_keys(provider)
    key = keys[key_name]
    if choice == "1":
        key["enabled"] = not bool(key.get("enabled", True))
        message = f"已{'启用' if key['enabled'] else '禁用'} {provider_id}/{key_name}。"
    elif choice == "2":
        new_name = prompt_text("重命名 Key", "新名称", default=key_name).strip()
        if not new_name or new_name == key_name:
            return section_panel("[yellow]配置未变化。[/yellow]", "重命名 Key", "yellow")
        if new_name in keys:
            return section_panel(f"[red]Key 已存在: {new_name}[/red]", "重命名 Key", "red")
        keys[new_name] = keys.pop(key_name)
        for pool in provider_pools(provider).values():
            pool_keys = pool_key_names(pool)
            if key_name in pool_keys and isinstance(pool, dict):
                pool["keys"] = [new_name if item == key_name else item for item in pool_keys]
        for model in raw_v2_models(data).values():
            for target in model_targets(model):
                if target.get("provider") == provider_id and target.get("key") == key_name:
                    target["key"] = new_name
        message = f"已重命名 {provider_id}/{key_name} → {new_name}。"
    elif choice == "3":
        api_key = prompt_text("替换 API key", "API key", password=True).strip()
        if not api_key:
            return section_panel("[red]API key 不能为空[/red]", "替换 API key", "red")
        key["api_key"] = api_key
        message = f"已替换 {provider_id}/{key_name} 的 API key。"
    elif choice == "4" and visitor_feature_available():
        key["allow_visitor"] = not bool(key.get("allow_visitor", False))
        message = f"已{'允许' if key['allow_visitor'] else '禁止'}访客访问 {provider_id}/{key_name}。"
    elif choice == "5":
        key_pools = pools_containing_key(provider, key_name)
        used_by = [
            model_id
            for model_id, model in raw_v2_models(data).items()
            for target in model_targets(model)
            if target.get("provider") == provider_id
            and (target.get("key") == key_name or target.get("pool") in key_pools)
        ]
        if used_by and not confirm_choice(
            f"该 Key 被 {len(used_by)} 个模型路由使用，删除会一并移除这些 target。继续？",
            default=False,
        ):
            return section_panel("[yellow]配置未变化。[/yellow]", "删除 Key", "yellow")
        keys.pop(key_name)
        empty_pools: set[str] = set()
        for pool_name, pool in list(provider_pools(provider).items()):
            if isinstance(pool, dict):
                pool["keys"] = [item for item in pool_key_names(pool) if item != key_name]
                if not pool["keys"]:
                    empty_pools.add(str(pool_name))
        for pool_name in empty_pools:
            provider_pools(provider).pop(pool_name, None)
        models = raw_v2_models(data)
        removed_models: set[str] = set()
        for model_id, model in list(models.items()):
            targets = model_targets(model)
            targets[:] = [
                target
                for target in targets
                if not (
                    target.get("provider") == provider_id
                    and (
                        target.get("key") == key_name
                        or target.get("pool") in empty_pools
                    )
                )
            ]
            if not targets:
                models.pop(model_id, None)
                removed_models.add(str(model_id))
        if not keys:
            raw_providers(data).pop(provider_id, None)
        unified = data.get("unified_model")
        if isinstance(unified, dict) and unified.get("model") in removed_models:
            fallback_model = fallback_unified_model(models)
            if fallback_model is not None:
                data["unified_model"] = {"model": fallback_model}
            else:
                data.pop("unified_model", None)
        suffix = f"\n已移除空模型: [bold]{len(removed_models)}[/bold]" if removed_models else ""
        message = f"已删除 {provider_id}/{key_name}。{suffix}"
    else:
        return None
    restart = commit_v2_config(path, data, old_config)
    return Group(section_panel(message, "供应商 Key", "green"), restart)


def manage_provider_pools_interactively(path: Path) -> None:
    while True:
        data = load_v2_config_data(path)
        provider_id = select_provider(data, "选择供应商模型池")
        if provider_id is None:
            return
        provider = raw_providers(data)[provider_id]
        ensure_default_pool(provider)
        pools = provider_pools(provider)
        rows = "\n".join(
            f"[bold]{pool_name}[/bold]: {', '.join(pool_key_names(pool)) or '空'}"
            f" · 启用 {len(pool_enabled_models(pool))}"
            f" / 可用 {len(pool_available_models(pool))}"
            f" · {pool.get('checked_at') or '未探测' if isinstance(pool, dict) else '未探测'}"
            for pool_name, pool in sorted(pools.items())
        ) or "[yellow]暂无模型池[/yellow]"
        choice = select_option(
            f"模型池 · {provider_id}",
            [
                ("1", "新增/编辑模型池"),
                ("2", "刷新可用性"),
                ("3", "手动设置可用模型"),
                ("4", "删除模型池"),
                ("0", "返回"),
            ],
            content=section_panel(rows, "模型池", "cyan"),
        )
        if choice == "0":
            continue
        clear_terminal_history()
        result = update_provider_pool_interactively(path, provider_id, choice)
        if result is not None:
            show_result_page("模型池", result)


def update_provider_pool_interactively(path: Path, provider_id: str, choice: str) -> Any:
    draft = form_draft(f"provider_pool:{provider_id}")
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    provider = raw_providers(data)[provider_id]
    keys = provider_keys(provider)
    pools = provider_pools(provider)
    if choice == "1":
        pool_name = select_or_enter_pool_name(
            pools,
            default=draft.get("pool_name", "default"),
        )
        draft["pool_name"] = pool_name
        if not pool_name:
            return section_panel("[red]Pool 名称不能为空[/red]", "模型池", "red")
        if not keys:
            return section_panel("[yellow]该供应商暂无 Key。[/yellow]", "模型池", "yellow")
        selected_keys = select_multiple(
            "选择 Pool Keys",
            [(key_name, key_name) for key_name in sorted(keys)],
            content=section_panel(
                "选择这个模型池可用的 Key。池代表同一批模型能力，而不是单个凭证。",
                "模型池",
                "cyan",
            ),
        )
        if not selected_keys:
            return section_panel("[yellow]配置未变化。[/yellow]", "模型池", "yellow")
        existing_keys = pool_key_names(pools.get(pool_name, {}))
        selected_keys = list(dict.fromkeys([*existing_keys, *selected_keys]))
        for other_pool_name, other_pool in pools.items():
            if other_pool_name == pool_name or not isinstance(other_pool, dict):
                continue
            other_pool["keys"] = [
                key_name
                for key_name in pool_key_names(other_pool)
                if key_name not in selected_keys
            ]
        pool = pools.setdefault(pool_name, {"keys": [], "models": []})
        pool["keys"] = selected_keys
        with console.status("[cyan]正在探测模型池可用模型和路由...[/cyan]", spinner="dots"):
            probe = apply_pool_probe(provider, pool_name)
        enabled_models = prompt_pool_enabled_models(pool)
        enable_pool_models(data, provider_id, pool_name, enabled_models)
        model_count = len(enabled_models)
        all_model_count = len(probe.get("all_models") or [])
        route_count = sum(
            1
            for route_map in (probe.get("routes") or {}).values()
            for available in route_map.values()
            if available
        )
        message = (
            f"已保存模型池 {provider_id}/{pool_name}，包含 {len(selected_keys)} 个 Key。\n"
            f"已启用模型: [bold]{model_count}[/bold]\n"
            f"任一 Key 可用模型: [bold]{all_model_count}[/bold]\n"
            f"可用路由探测: [bold]{route_count}[/bold]"
        )
    elif choice == "2":
        if not pools:
            return section_panel("[yellow]暂无模型池。[/yellow]", "模型池", "yellow")
        pool_choice = select_option(
            "刷新模型池",
            [(str(index + 1), pool_name) for index, pool_name in enumerate(sorted(pools))]
            + [("0", "返回")],
        )
        if pool_choice == "0":
            return None
        pool_name = sorted(pools)[int(pool_choice) - 1]
        with console.status("[cyan]正在刷新模型池可用性...[/cyan]", spinner="dots"):
            probe = apply_pool_probe(provider, pool_name)
        enabled_models = prompt_pool_enabled_models(pools[pool_name])
        enable_pool_models(data, provider_id, pool_name, enabled_models)
        message = (
            f"已刷新模型池 {provider_id}/{pool_name}。\n"
            f"已启用模型: [bold]{len(enabled_models)}[/bold]\n"
            f"任一 Key 可用模型: [bold]{len(probe.get('all_models') or [])}[/bold]"
        )
    elif choice == "3":
        if not pools:
            return section_panel("[yellow]暂无模型池。[/yellow]", "模型池", "yellow")
        pool_choice = select_option(
            "手动设置模型",
            [(str(index + 1), pool_name) for index, pool_name in enumerate(sorted(pools))]
            + [("0", "返回")],
        )
        if pool_choice == "0":
            return None
        pool_name = sorted(pools)[int(pool_choice) - 1]
        pool = pools[pool_name]
        enabled_models = prompt_pool_enabled_models(pool)
        enable_pool_models(data, provider_id, pool_name, enabled_models)
        message = (
            f"已设置模型池 {provider_id}/{pool_name} 的启用模型。\n"
            f"已启用模型: [bold]{len(enabled_models)}[/bold]"
        )
    elif choice == "4":
        if not pools:
            return section_panel("[yellow]暂无模型池。[/yellow]", "模型池", "yellow")
        pool_choice = select_option(
            "删除模型池",
            [(str(index + 1), pool_name) for index, pool_name in enumerate(sorted(pools))]
            + [("0", "返回")],
        )
        if pool_choice == "0":
            return None
        pool_name = sorted(pools)[int(pool_choice) - 1]
        if pool_key_names(pools[pool_name]):
            return section_panel(
                "[yellow]该模型池仍包含 Key，请先将 Key 移入其他模型池。[/yellow]",
                "模型池",
                "yellow",
            )
        used_by = [
            model_id
            for model_id, model in raw_v2_models(data).items()
            for target in model_targets(model)
            if target.get("provider") == provider_id and target.get("pool") == pool_name
        ]
        if used_by and not confirm_choice(
            f"该模型池被 {len(used_by)} 个模型路由使用，删除会一并移除这些 target。继续？",
            default=False,
        ):
            return section_panel("[yellow]配置未变化。[/yellow]", "模型池", "yellow")
        pools.pop(pool_name)
        for model in raw_v2_models(data).values():
            targets = model_targets(model)
            targets[:] = [
                target
                for target in targets
                if not (target.get("provider") == provider_id and target.get("pool") == pool_name)
            ]
        message = f"已删除模型池 {provider_id}/{pool_name}。"
    else:
        return None
    restart = commit_v2_config(path, data, old_config)
    if choice in {"1", "3", "4"}:
        FORM_DRAFTS.pop(f"provider_pool:{provider_id}", None)
    return Group(section_panel(message, "模型池", "green"), restart)


def add_model_route_interactively(path: Path, model_id: str | None = None) -> Any:
    draft = form_draft("add_model_route")
    data = load_v2_config_data(path)
    if not raw_providers(data):
        return section_panel("[yellow]请先添加供应商 Key。[/yellow]", "添加模型路由", "yellow")
    old_config = RouterConfig.from_dict(data)
    models = raw_v2_models(data)
    selected = select_provider_pool(data, "选择模型池")
    if selected is None:
        return None
    provider_id, pool_name = selected
    pool = provider_pools(raw_providers(data)[provider_id]).get(pool_name, {})
    enabled_models = pool_enabled_models(pool)
    if not enabled_models:
        return section_panel(
            "[yellow]该模型池没有启用模型。请先刷新模型池或手动设置可用模型。[/yellow]",
            "添加模型路由",
            "yellow",
        )
    upstream_choice = select_option(
        "选择启用模型",
        [(str(index + 1), available_model) for index, available_model in enumerate(enabled_models)]
        + [("0", "返回")],
    )
    if upstream_choice == "0":
        return None
    upstream_model = enabled_models[int(upstream_choice) - 1]
    if model_id is None:
        model_id = select_or_enter_model_id(
            "添加模型路由",
            "本地模型 ID",
            models,
            default=draft.get("model_id", upstream_model),
        )
        draft["model_id"] = model_id
    if not model_id:
        return section_panel("[red]模型 ID 不能为空[/red]", "添加模型路由", "red")
    model = models.setdefault(
        model_id,
        {"aliases": [], "routing_mode": "round_robin", "targets": []},
    )
    targets = model_targets(model)
    if any(
        target.get("provider") == provider_id
        and target.get("pool") == pool_name
        and str(target.get("upstream_model") or model_id) == upstream_model
        for target in targets
    ):
        return section_panel("[yellow]该路由已存在。[/yellow]", "添加模型路由", "yellow")
    targets.append(
        {"provider": provider_id, "pool": pool_name, "upstream_model": upstream_model}
    )
    restart = commit_v2_config(path, data, old_config)
    FORM_DRAFTS.pop("add_model_route", None)
    return Group(
        section_panel(
            f"本地模型: [bold]{model_id}[/bold]\n模型池: [bold]{provider_id}/{pool_name}[/bold]\n上游模型: [bold]{upstream_model}[/bold]",
            "添加完成",
            "green",
        ),
        restart,
    )


def manage_model_routes_interactively(path: Path, selected_model_id: str | None = None) -> None:
    while True:
        data = load_v2_config_data(path)
        model_id = selected_model_id or select_v2_model(data, "选择模型路由")
        if model_id is None:
            return
        model = raw_v2_models(data)[model_id]
        targets = model_targets(model)
        if not targets:
            show_result_page(
                "模型路由",
                section_panel("[yellow]该模型暂无 target。[/yellow]", "模型路由", "yellow"),
            )
            if selected_model_id is not None:
                return
            continue
        options = [
            (
                str(index + 1),
                f"{target.get('provider')}/{target.get('pool') or target.get('key')} → {target.get('upstream_model') or model_id}",
            )
            for index, target in enumerate(targets)
        ] + [("0", "返回")]
        target_choice = select_option(f"Target · {short_text(model_id, 28)}", options)
        if target_choice == "0":
            if selected_model_id is not None:
                return
            continue
        target_index = int(target_choice) - 1
        action = select_option(
            "管理 Target",
            [("1", "改上游模型"), ("2", "删除 Target"), ("0", "返回")],
        )
        if action == "0":
            continue
        clear_terminal_history()
        result = update_model_target_interactively(path, model_id, target_index, action)
        if result is not None:
            show_result_page("模型路由", result)


def update_model_target_interactively(
    path: Path, model_id: str, target_index: int, action: str
) -> Any:
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    model = raw_v2_models(data)[model_id]
    targets = model_targets(model)
    target = targets[target_index]
    if action == "1":
        current = str(target.get("upstream_model") or model_id)
        provider_id = str(target.get("provider") or "")
        pool_name = str(target.get("pool") or "")
        upstream_model = select_pool_model_id(
            "改上游模型",
            "上游模型 ID",
            raw_providers(data).get(provider_id, {}),
            pool_name,
            default=current,
        )
        if not upstream_model or upstream_model == current:
            return section_panel("[yellow]配置未变化。[/yellow]", "模型路由", "yellow")
        target["upstream_model"] = upstream_model
        message = f"已更新 {model_id} 的上游模型: {current} → {upstream_model}。"
    elif action == "2":
        if len(targets) == 1 and not confirm_choice(
            "这是该模型最后一个 target，删除后模型将不可用。继续？", default=False
        ):
            return section_panel("[yellow]配置未变化。[/yellow]", "模型路由", "yellow")
        removed = targets.pop(target_index)
        message = f"已删除 target: {removed.get('provider')}/{removed.get('pool') or removed.get('key')}。"
    else:
        return None
    restart = commit_v2_config(path, data, old_config)
    return Group(section_panel(message, "模型路由", "green"), restart)


def manage_v2_model_settings_interactively(path: Path) -> None:
    while True:
        data = load_v2_config_data(path)
        model_id = select_v2_model(data, "选择模型")
        if model_id is None:
            return
        model = raw_v2_models(data)[model_id]
        choice = select_option(
            f"模型设置 · {short_text(model_id, 28)}",
            [
                ("1", "别名"),
                ("2", "路由模式"),
                ("3", "路由目标"),
                ("4", "添加路由目标"),
                ("5", "删除模型"),
                ("0", "返回"),
            ],
            content=section_panel(
                f"别名: [bold]{', '.join(model.get('aliases') or []) or '无'}[/bold]\n路由模式: [bold]{model.get('routing_mode') or 'round_robin'}[/bold]",
                "模型设置",
                "cyan",
            ),
        )
        if choice == "0":
            continue
        clear_terminal_history()
        if choice in {"1", "2"}:
            result = update_v2_model_settings_interactively(path, model_id, choice)
        elif choice == "3":
            run_submodule(lambda: manage_model_routes_interactively(path, model_id))
            continue
        elif choice == "4":
            result = run_submodule(lambda: add_model_route_interactively(path, model_id))
        elif choice == "5":
            result = delete_v2_model_interactively(path, model_id)
        else:
            continue
        if result is not None:
            show_result_page("模型设置", result)
        if choice == "5":
            return


def update_v2_model_settings_interactively(path: Path, model_id: str, choice: str) -> Any:
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    model = raw_v2_models(data)[model_id]
    if choice == "1":
        aliases_text = prompt_text(
            "模型别名", "别名，多个用逗号分隔", default=", ".join(model.get("aliases") or [])
        ).strip()
        model["aliases"] = [alias.strip() for alias in aliases_text.split(",") if alias.strip()]
        message = f"已更新 {model_id} 的别名。"
    elif choice == "2":
        current = str(model.get("routing_mode") or "round_robin")
        routing_mode = prompt_text(
            "路由模式",
            "路由模式",
            choices=["priority", "round_robin", "only_first"],
            default=current,
        ).strip()
        model["routing_mode"] = routing_mode
        message = f"已更新 {model_id} 的路由模式: {routing_mode}。"
    else:
        return None
    restart = commit_v2_config(path, data, old_config)
    return Group(section_panel(message, "模型映射", "green"), restart)


def delete_v2_model_interactively(path: Path, model_id: str) -> Any:
    data = load_v2_config_data(path)
    models = raw_v2_models(data)
    if model_id not in models:
        return section_panel(f"[red]模型不存在: {model_id}[/red]", "模型设置", "red")
    if not confirm_choice(f"确认删除模型 {model_id}？", default=False):
        return section_panel("[yellow]配置未变化。[/yellow]", "模型设置", "yellow")
    old_config = RouterConfig.from_dict(data)
    models.pop(model_id)
    unified = data.get("unified_model")
    if isinstance(unified, dict) and unified.get("model") == model_id:
        fallback_model = fallback_unified_model(models)
        if fallback_model is None:
            data.pop("unified_model", None)
        else:
            data["unified_model"] = {"model": fallback_model}
    restart = commit_v2_config(path, data, old_config)
    return Group(section_panel(f"已删除模型 {model_id}。", "模型设置", "green"), restart)


def manage_provider_routes_interactively(path: Path) -> None:
    while True:
        data = load_v2_config_data(path)
        provider_id = select_provider(data, "选择供应商路径")
        if provider_id is None:
            return
        provider = raw_providers(data)[provider_id]
        routes = provider.setdefault("routes", {})
        route_rows = "\n".join(
            f"{UPSTREAM_ROUTE_LABELS[mode]}: [bold]{upstream_route_path(routes, mode)}[/bold]"
            for mode in UPSTREAM_ROUTE_MODES
        )
        mode_options = [
            (str(index + 1), UPSTREAM_ROUTE_LABELS[mode])
            for index, mode in enumerate(UPSTREAM_ROUTE_MODES)
        ] + [("c", "清空自定义路径"), ("0", "返回")]
        choice = select_option(
            f"供应商路径 · {provider_id}",
            mode_options,
            content=section_panel(route_rows, "当前路径", "cyan"),
        )
        if choice == "0":
            continue
        clear_terminal_history()
        result = update_provider_routes_interactively(path, provider_id, choice)
        if result is not None:
            show_result_page("供应商路径", result)


def update_provider_routes_interactively(path: Path, provider_id: str, choice: str) -> Any:
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    provider = raw_providers(data)[provider_id]
    routes = provider.setdefault("routes", {})
    if choice == "c":
        provider.pop("routes", None)
        message = f"已清空 {provider_id} 的自定义路径。"
    else:
        mode = UPSTREAM_ROUTE_MODES[int(choice) - 1]
        current = routes.get(mode) or UPSTREAM_ROUTE_DEFAULT_PATHS[mode]
        route = prompt_text(
            "供应商路径",
            f"{UPSTREAM_ROUTE_LABELS[mode]} 路径或前缀",
            default=current,
        ).strip()
        if not route:
            routes.pop(mode, None)
            message = f"已恢复 {UPSTREAM_ROUTE_LABELS[mode]} 默认路径。"
        else:
            routes[mode] = normalize_upstream_route_path(mode, route)
            message = f"已更新 {provider_id} 的 {UPSTREAM_ROUTE_LABELS[mode]} 路径。"
    restart = commit_v2_config(path, data, old_config)
    return Group(section_panel(message, "供应商路径", "green"), restart)


def manage_model_keys_interactively(path: Path) -> None:
    def on_key(key: str) -> str | None:
        return _open_config_on_key(path, key)
    while True:
        choice = select_option(
            "供应商与模型",
            [
                ("1", "添加供应商 Key"),
                ("2", "管理供应商 Key"),
                ("3", "模型池"),
                ("4", "模型设置"),
                ("0", "返回"),
            ],
            on_key=on_key,
        )
        if choice == "0":
            return
        if choice == "1":
            result = run_submodule(lambda: add_provider_key_interactively(path))
            if result is not None:
                show_result_page("添加供应商 Key", result)
        elif choice == "2":
            run_submodule(lambda: manage_provider_keys_interactively(path))
        elif choice == "3":
            run_submodule(lambda: manage_provider_pools_interactively(path))
        elif choice == "4":
            run_submodule(lambda: manage_v2_model_settings_interactively(path))


def manage_model_aliases_interactively(path: Path) -> None:
    def on_key(key: str) -> str | None:
        return _open_config_on_key(path, key)
    while True:
        data = load_config_data(path)
        models = data.get("models", [])
        if not models:
            show_result_page(
                "模型别称",
                section_panel(
                    "[yellow]暂无可设置别称的模型。[/yellow]", "模型别称", "yellow"
                ),
            )
            return

        options = []
        for index, model in enumerate(models):
            aliases = model.get("aliases", [])
            alias_summary = (
                short_text(", ".join(str(alias) for alias in aliases), 28)
                if aliases
                else "无别称"
            )
            options.append(
                (
                    str(index + 1),
                    f"{short_text(model.get('id') or '-', 28)} · {alias_summary}",
                )
            )
        options.append(("0", "返回"))
        choice = select_option("选择要设置别称的模型", options, on_key=on_key)
        if choice == "0":
            return
        model_id = str(models[int(choice) - 1].get("id") or "")
        run_submodule(
            lambda: manage_selected_model_aliases_interactively(path, model_id)
        )


def manage_selected_model_aliases_interactively(path: Path, model_id: str) -> None:
    def on_key(key: str) -> str | None:
        return _open_config_on_key(path, key)
    while True:
        data = load_config_data(path)
        model = find_model(data.get("models", []), model_id)
        if model is None:
            show_result_page(
                "模型别称",
                section_panel(f"[red]模型不存在: {model_id}[/red]", "模型别称", "red"),
            )
            return
        aliases = model.setdefault("aliases", [])
        options = [("1", "添加别称")]
        if aliases:
            options.extend([("2", "编辑别称"), ("3", "删除别称")])
        options.append(("0", "返回"))
        choice = select_option(
            f"模型别称 · {short_text(model_id, 28)}",
            options,
            content=model_aliases_panel(model),
            on_key=on_key,
        )
        if choice == "0":
            return

        clear_terminal_history()
        if choice == "1":
            result = add_model_alias_interactively(path, data, model)
            title = "添加别称"
        else:
            alias_index = select_model_alias(
                model, "选择要编辑的别称" if choice == "2" else "选择要删除的别称"
            )
            if alias_index is None:
                continue
            if choice == "2":
                result = edit_model_alias_interactively(path, data, model, alias_index)
                title = "编辑别称"
            else:
                result = delete_model_alias_interactively(
                    path, data, model, alias_index
                )
                title = "删除别称"
        if result is not None:
            show_result_page(title, result)


def model_aliases_panel(model: dict[str, Any]) -> Any:
    aliases = model.get("aliases", [])
    aliases_text = "\n".join(
        f"{index}. [bold]{short_text(str(alias), 64)}[/bold]"
        for index, alias in enumerate(aliases, 1)
    )
    if not aliases_text:
        aliases_text = "[dim]暂无别称[/dim]"
    return section_panel(
        f"模型 ID: [bold cyan]{short_text(model.get('id') or '-', 48)}[/bold cyan]\n\n{aliases_text}",
        "当前别称",
        "cyan",
    )


def select_model_alias(model: dict[str, Any], title: str) -> int | None:
    aliases = model.get("aliases", [])
    options = [
        (str(index + 1), short_text(str(alias), 48))
        for index, alias in enumerate(aliases)
    ]
    options.append(("0", "返回"))
    choice = select_option(title, options, content=model_aliases_panel(model))
    if choice == "0":
        return None
    return int(choice) - 1


def add_model_alias_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any]
) -> Any:
    alias = prompt_text("添加别称", "新别称").strip()
    if not alias:
        return section_panel("[red]模型别称不能为空。[/red]", "添加失败", "red")
    old_config = RouterConfig.from_dict(data)
    model.setdefault("aliases", []).append(alias)
    return save_model_alias_change(
        path, data, old_config, model, f"已添加别称: [bold]{alias}[/bold]", "添加完成"
    )


def edit_model_alias_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], alias_index: int
) -> Any:
    aliases = model.setdefault("aliases", [])
    old_alias = str(aliases[alias_index])
    alias = prompt_text("编辑别称", "模型别称", default=old_alias).strip()
    if not alias:
        return section_panel("[red]模型别称不能为空。[/red]", "编辑失败", "red")
    if alias == old_alias:
        return section_panel("[yellow]别称未变化。[/yellow]", "编辑别称", "yellow")
    old_config = RouterConfig.from_dict(data)
    aliases[alias_index] = alias
    replace_unified_model_alias(data, model, old_alias)
    return save_model_alias_change(
        path,
        data,
        old_config,
        model,
        f"原别称: [bold]{old_alias}[/bold]\n新别称: [bold]{alias}[/bold]",
        "编辑完成",
    )


def delete_model_alias_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], alias_index: int
) -> Any:
    aliases = model.setdefault("aliases", [])
    alias = str(aliases[alias_index])
    if not confirm_choice(
        f"确认删除模型 {model['id']} 的别称 {alias}？", default=False
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除取消", "yellow")
    old_config = RouterConfig.from_dict(data)
    del aliases[alias_index]
    replace_unified_model_alias(data, model, alias)
    return save_model_alias_change(
        path, data, old_config, model, f"已删除别称: [bold]{alias}[/bold]", "删除完成"
    )


def replace_unified_model_alias(
    data: dict[str, Any], model: dict[str, Any], alias: str
) -> None:
    unified_model = data.get("unified_model")
    if isinstance(unified_model, dict) and unified_model.get("model") == alias:
        unified_model["model"] = model["id"]


def save_model_alias_change(
    path: Path,
    data: dict[str, Any],
    old_config: RouterConfig,
    model: dict[str, Any],
    message: str,
    title: str,
) -> Any:
    try:
        new_config = commit_config_data(path, data, old_config).new_config
    except ValueError as exc:
        return section_panel(f"[red]{exc}[/red]", "别称设置失败", "red")
    return Group(
        section_panel(
            f"{message}\n模型: [bold]{model['id']}[/bold]\n配置文件: [bold]{path}[/bold]",
            title,
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def manage_selected_key_interactively(path: Path) -> None:
    def on_key(key: str) -> str | None:
        return _open_config_on_key(path, key)
    while True:
        data = load_config_data(path)
        manage_choice = select_option(
            "管理 Key",
            [
                ("1", "管理单个 Key"),
                ("2", "探测所有 Key"),
                ("0", "返回"),
            ],
            on_key=on_key,
        )
        if manage_choice == "0":
            return
        if manage_choice == "2":
            clear_terminal_history()
            result = probe_all_keys_interactively(path)
            show_result_page("探测所有 Key", result)
            continue
        if manage_choice != "1":
            continue

        selection = select_api_key(path, "选择要管理的 Key")
        if selection is None:
            continue
        data, model, key_index = selection
        key = model["keys"][key_index]
        key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
        enabled = key.get("enabled", True)
        status_text = "[green]启用[/green]" if enabled else "[red]禁用[/red]"
        visitor_allowed = bool(key.get("allow_visitor", False))
        visitor_installed = visitor_feature_available()
        visitor_status_text = format_visitor_status_text(
            visitor_allowed, visitor_installed
        )

        while True:
            options = [
                ("1", "编辑"),
                ("2", "删除"),
                ("3", "复制 API key"),
                ("4", "禁用" if enabled else "启用"),
                ("5", "统计"),
            ]
            if visitor_installed:
                options.append(
                    ("6", "禁止访客访问" if visitor_allowed else "允许访客访问")
                )
                probe_choice = "7"
            else:
                probe_choice = "6"
            options.append((probe_choice, "可用性探测"))
            options.append(("0", "返回"))
            key_info_lines = [
                f"模型: [bold]{short_text(model['id'], 32)}[/bold]",
                f"Key: {key_display_name(key, key_name, 32)}",
                f"上游: [bold]{compact_url(key.get('base_url') or '-', 48)}[/bold]",
                f"配置状态: {status_text}",
            ]
            if visitor_installed:
                key_info_lines.append(f"访客访问: {visitor_status_text}")
            choice = select_option(
                f"管理 Key · {short_text(key_name, 24)}",
                options,
                content=section_panel("\n".join(key_info_lines), "Key 信息", "cyan"),
                on_key=on_key,
            )
            if choice == "0":
                break
            if choice == "5":
                from .logs_tui import watch_key_stats
                config_data = load_config_data(path)
                db_path = str(config_data.get("metrics_db_path") or default_metrics_db_path())
                run_submodule(lambda: watch_key_stats(db_path, model["id"], key_name))
                continue
            clear_terminal_history()
            if choice == "1":
                result = edit_selected_key_interactively(path, data, model, key_index)
            elif choice == "2":
                result = delete_selected_key_interactively(path, data, model, key_index)
                if result is not None:
                    show_result_page("删除 API key", result)
                    return  # key已删除，返回选择列表
                continue
            elif choice == "3":
                result = copy_selected_key_interactively(data, model, key_index)
            elif choice == "4":
                result = toggle_selected_key_interactively(path, data, model, key_index)
            elif visitor_installed and choice == "6":
                result = toggle_visitor_access_interactively(
                    path, data, model, key_index
                )
            elif choice == probe_choice:
                result = probe_selected_key_interactively(path, data, model, key_index)
            else:
                continue
            if result is not None:
                result_title = {
                    "1": "编辑",
                    "3": "复制 API key",
                    "4": "Key 开关",
                    "6": "访客访问",
                    probe_choice: "可用性探测",
                }.get(choice, "Key 管理")
                show_result_page(result_title, result)
            if choice in ("1", "4", "6"):
                # 编辑或切换状态后刷新数据
                data = load_config_data(path)
                model = find_model(data.get("models", []), model["id"])
                if model is None or key_index >= len(model.get("keys", [])):
                    return
                key = model["keys"][key_index]
                key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
                enabled = key.get("enabled", True)
                status_text = "[green]启用[/green]" if enabled else "[red]禁用[/red]"
                visitor_allowed = bool(key.get("allow_visitor", False))
                visitor_installed = visitor_feature_available()
                visitor_status_text = format_visitor_status_text(
                    visitor_allowed, visitor_installed
                )


def manage_upstream_routes_interactively(path: Path) -> None:
    data = load_config_data(path)
    base_urls = configured_base_urls(data, {})
    if not base_urls:
        show_result_page(
            "上游路由",
            section_panel("[yellow]未配置上游 URL[/yellow]", "上游", "yellow"),
        )
        return
    base_url_choice = select_option(
        "上游路由",
        [
            (str(index + 1), compact_url(base_url, 56))
            for index, base_url in enumerate(base_urls)
        ]
        + [("0", "返回")],
    )
    if base_url_choice == "0":
        return
    try:
        base_url = base_urls[int(base_url_choice) - 1]
    except (IndexError, ValueError):
        return

    while True:
        routes = upstream_routes_for_base_url(data, base_url)
        options = [
            (
                str(index + 1),
                f"{UPSTREAM_ROUTE_LABELS[mode]} - {short_text(upstream_route_path(routes, mode), 36)}",
            )
            for index, mode in enumerate(UPSTREAM_ROUTE_MODES)
        ]
        options.extend([("c", "清空自定义路由"), ("0", "返回")])
        choice = select_option(
            f"上游路由 · {short_text(base_url, 32)}",
            options,
            content=upstream_routes_panel(data, base_url),
        )
        if choice == "0":
            return

        old_config = RouterConfig.from_dict(data)
        try:
            if choice == "c":
                routes = {}
                message = "已清空自定义上游路由。"
            else:
                mode = UPSTREAM_ROUTE_MODES[int(choice) - 1]
                current_path = routes.get(mode, "")
                raw_path = prompt_text(
                    "上游路由",
                    f"{UPSTREAM_ROUTE_LABELS[mode]} 路径/前缀（留空恢复默认）",
                    default=current_path,
                ).strip()
                if raw_path:
                    routes[mode] = normalize_upstream_route_path(mode, raw_path)
                    message = (
                        f"已设置 {UPSTREAM_ROUTE_LABELS[mode]}: "
                        f"[bold]{routes[mode]}[/bold]"
                    )
                else:
                    routes.pop(mode, None)
                    message = f"已恢复 {UPSTREAM_ROUTE_LABELS[mode]} 默认路径。"
        except (IndexError, ValueError) as exc:
            show_result_page(
                "上游路由",
                section_panel(f"[red]{exc}[/red]", "配置失败", "red"),
            )
            continue

        set_upstream_routes_for_base_url(data, base_url, routes)
        try:
            new_config = commit_config_data(path, data, old_config).new_config
        except (KeyError, TypeError, ValueError) as exc:
            show_result_page(
                "上游路由",
                section_panel(f"[red]{exc}[/red]", "配置失败", "red"),
            )
            continue
        show_result_page(
            "上游路由",
            Group(
                section_panel(
                    f"已更新配置文件: [bold]{path}[/bold]\n"
                    f"上游 URL: [bold]{base_url}[/bold]\n{message}",
                    "配置完成",
                    "green",
                ),
                restart_service_after_config_change(path, old_config, new_config),
            ),
        )
        data = load_config_data(path)

    return

    selection = select_api_key(path, "选择要配置上游路由的 Key")
    if selection is None:
        return
    data, model, key_index = selection
    while True:
        key = model["keys"][key_index]
        key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
        options = [
            (
                str(index + 1),
                f"{UPSTREAM_ROUTE_LABELS[mode]} · {short_text(upstream_route_path(upstream_routes_for_base_url(data, key.get('base_url') or data.get('default_base_url') or 'https://api.openai.com'), mode), 36)}",
            )
            for index, mode in enumerate(UPSTREAM_ROUTE_MODES)
        ]
        options.extend([("c", "清空自定义路由"), ("0", "返回")])
        choice = select_option(
            f"上游路由 · {short_text(key_name, 24)}",
            options,
            content=upstream_routes_panel(data, model, key_index),
        )
        if choice == "0":
            return

        old_config = RouterConfig.from_dict(data)
        routes = upstream_routes_for_base_url(
            data,
            key.get("base_url") or data.get("default_base_url") or "https://api.openai.com",
        )
        try:
            if choice == "c":
                routes = {}
                message = "已清空自定义上游路由，恢复默认路径。"
            else:
                mode = UPSTREAM_ROUTE_MODES[int(choice) - 1]
                current_path = routes.get(mode, "")
                raw_path = prompt_text(
                    "上游路由",
                    f"{UPSTREAM_ROUTE_LABELS[mode]} 路径/前缀（留空恢复默认）",
                    default=current_path,
                ).strip()
                if raw_path:
                    routes[mode] = normalize_upstream_route_path(mode, raw_path)
                    message = (
                        f"已设置 {UPSTREAM_ROUTE_LABELS[mode]}: "
                        f"[bold]{routes[mode]}[/bold]"
                    )
                else:
                    routes.pop(mode, None)
                    message = f"已恢复 {UPSTREAM_ROUTE_LABELS[mode]} 默认路径。"
        except (IndexError, ValueError) as exc:
            show_result_page(
                "上游路由",
                section_panel(f"[red]{exc}[/red]", "配置失败", "red"),
            )
            continue

        set_upstream_routes_for_base_url(
            data,
            key.get("base_url") or data.get("default_base_url") or "https://api.openai.com",
            routes,
        )
        try:
            new_config = commit_config_data(path, data, old_config).new_config
        except (KeyError, TypeError, ValueError) as exc:
            show_result_page(
                "上游路由",
                section_panel(f"[red]{exc}[/red]", "配置失败", "red"),
            )
            continue
        show_result_page(
            "上游路由",
            Group(
                section_panel(
                    f"已更新配置文件: [bold]{path}[/bold]\n"
                    f"模型: [bold]{model['id']}[/bold]\n"
                    f"Key: [bold]{key_name}[/bold]\n{message}",
                    "配置完成",
                    "green",
                ),
                restart_service_after_config_change(path, old_config, new_config),
            ),
        )
        data = load_config_data(path)
        model = find_model(data.get("models", []), model["id"])
        if model is None or key_index >= len(model.get("keys", [])):
            return


def edit_selected_key_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    old_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    key_name = prompt_text("编辑 Key", "Key 名称", default=old_name).strip() or old_name
    key["name"] = key_name
    key["base_url"] = prompt_text(
        "编辑 Key",
        "上游 base_url",
        default=str(
            key.get("base_url")
            or data.get("default_base_url")
            or "https://api.openai.com"
        ),
    ).strip()
    api_key = prompt_text(
        "编辑 Key", "新 API key（留空则不修改）", default="", password=True
    ).strip()
    if api_key:
        key["api_key"] = api_key
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            f"已更新配置文件: [bold]{path}[/bold]\n模型: [bold]{model['id']}[/bold]\nKey: [bold]{key_name}[/bold]",
            "编辑完成",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def delete_selected_key_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    if not confirm_choice(
        f"确认删除模型 {model['id']} 的 Key {key_name}？", default=False
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除取消", "yellow")
    del model["keys"][key_index]
    if not model["keys"]:
        data["models"].remove(model)
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型 [bold]{model['id']}[/bold] 已无 API key，已一并移除。"
    else:
        message = (
            f"已删除 Key: [bold]{key_name}[/bold]\n模型: [bold]{model['id']}[/bold]"
        )
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(message, "删除完成", "green"),
        restart_service_after_config_change(path, old_config, new_config),
    )


def copy_selected_key_interactively(
    data: dict[str, Any], model: dict[str, Any], key_index: int
) -> ResultPage:
    key = model["keys"][key_index]
    api_key = str(key.get("api_key") or "")
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    base_url = compact_url(
        key.get("base_url") or data.get("default_base_url") or "-", 48
    )
    content = section_panel(
        f"模型: [bold]{short_text(model['id'], 48)}[/bold]\nKey: {key_display_name(key, key_name, 48)}\n上游: [bold]{base_url}[/bold]\n指纹: [bold]{key_fingerprint(api_key)}[/bold]\n\n选择“复制 API key”即可写入剪贴板。",
        "复制 API key",
        "green",
    )
    return ResultPage(content, copy_text=api_key, copy_label="复制 API key")


def toggle_selected_key_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    current_enabled = key.get("enabled", True)
    new_enabled = not current_enabled
    key["enabled"] = new_enabled
    new_config = commit_config_data(path, data, old_config).new_config
    old_status = "启用" if current_enabled else "禁用"
    new_status = "启用" if new_enabled else "禁用"
    return Group(
        section_panel(
            f"已切换 Key 状态。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\nKey: {key_display_name(key, key_name, 32)}\n原状态: [bold]{old_status}[/bold]\n新状态: [bold]{new_status}[/bold]",
            "Key 开关",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def toggle_visitor_access_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    if not visitor_feature_available():
        return section_panel(
            "访客功能未安装。请使用 auto-model-key-router[visitor] 重新安装或升级。",
            "访客访问",
            "yellow",
        )
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    visitor_allowed = not bool(key.get("allow_visitor", False))
    key["allow_visitor"] = visitor_allowed
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            f"已更新访客访问权限。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n"
            f"Key: {key_display_name(key, key_name, 32)}\n访客访问: {format_visitor_status_text(visitor_allowed, True)}",
            "访客访问",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def manage_config_transfer_interactively(path: Path) -> None:
    def on_key(key: str) -> str | None:
        return _open_config_on_key(path, key)
    while True:
        choice = select_option(
            "配置迁移", [("1", "复制 Key 配置"), ("2", "粘贴并应用"), ("0", "返回")],
            on_key=on_key,
        )
        if choice == "0":
            return
        clear_terminal_history()
        result = (
            export_config_interactively(path)
            if choice == "1"
            else paste_config_interactively(path)
        )
        if result is not None:
            show_result_page("配置迁移", result)


def transferable_key_config(
    data: dict[str, Any], *, include_visitor: bool
) -> dict[str, Any]:
    providers = deepcopy(raw_providers(data))
    models = deepcopy(raw_v2_models(data))
    if not include_visitor:
        for provider in providers.values():
            if isinstance(provider, dict):
                for key in provider_keys(provider).values():
                    if isinstance(key, dict):
                        key.pop("allow_visitor", None)
    return {
        "config_version": CONFIG_VERSION,
        "providers": providers,
        "models": models,
    }


def merge_transferable_key_config(
    current_data: dict[str, Any], transfer_data: dict[str, Any]
) -> tuple[dict[str, Any], int, int, int]:
    merged_data = deepcopy(current_data)
    merged_data["config_version"] = CONFIG_VERSION
    current_providers = raw_providers(merged_data)
    current_models = raw_v2_models(merged_data)
    transfer_providers = raw_providers(transfer_data)
    transfer_models = raw_v2_models(transfer_data)
    added_models = 0
    added_keys = 0
    skipped_keys = 0
    provider_name_map: dict[str, str] = {}
    pool_name_map: dict[tuple[str, str], tuple[str, str]] = {}

    for provider_id, provider in transfer_providers.items():
        if not isinstance(provider, dict):
            continue
        target_provider_id = str(provider_id)
        if target_provider_id in current_providers:
            current_provider = current_providers[target_provider_id]
            if normalize_upstream_base_url(current_provider.get("base_url")) != normalize_upstream_base_url(provider.get("base_url")):
                base_name = target_provider_id
                suffix = 2
                while f"{base_name}-{suffix}" in current_providers:
                    suffix += 1
                target_provider_id = f"{base_name}-{suffix}"
                current_provider = deepcopy(provider)
                current_provider["keys"] = {}
                current_provider["pools"] = {}
                current_providers[target_provider_id] = current_provider
        else:
            current_provider = deepcopy(provider)
            current_provider["keys"] = {}
            current_provider["pools"] = {}
            current_providers[target_provider_id] = current_provider
        provider_name_map[str(provider_id)] = target_provider_id
        current_keys = provider_keys(current_provider)
        current_pools = provider_pools(current_provider)
        existing_key_targets = {
            str(key.get("api_key") or "")
            for key in current_keys.values()
            if isinstance(key, dict)
        }
        key_name_map: dict[str, str | None] = {}
        for key_name, key in provider_keys(provider).items():
            api_key = str(key.get("api_key") or "") if isinstance(key, dict) else ""
            if api_key in existing_key_targets:
                skipped_keys += 1
                key_name_map[str(key_name)] = None
                continue
            target_key_name = str(key_name)
            base_name = target_key_name
            suffix = 2
            while target_key_name in current_keys:
                target_key_name = f"{base_name}-{suffix}"
                suffix += 1
            current_keys[target_key_name] = deepcopy(key)
            key_name_map[str(key_name)] = target_key_name
            existing_key_targets.add(api_key)
            added_keys += 1
        for pool_name, pool in provider_pools(provider).items():
            mapped_keys = [
                key_name_map[key_name]
                for key_name in pool_key_names(pool)
                if key_name_map.get(key_name)
            ]
            if not mapped_keys:
                pool_name_map[(str(provider_id), str(pool_name))] = (target_provider_id, "")
                continue
            target_pool_name = str(pool_name)
            base_name = target_pool_name
            suffix = 2
            while target_pool_name in current_pools:
                target_pool_name = f"{base_name}-{suffix}"
                suffix += 1
            target_pool = deepcopy(pool) if isinstance(pool, dict) else {}
            target_pool["keys"] = mapped_keys
            target_pool.setdefault("models", [])
            current_pools[target_pool_name] = target_pool
            pool_name_map[(str(provider_id), str(pool_name))] = (target_provider_id, target_pool_name)

    for model_id, transferred_model in transfer_models.items():
        target_model = current_models.get(str(model_id))
        if not isinstance(target_model, dict):
            current_models[str(model_id)] = deepcopy(transferred_model)
            target_model = current_models[str(model_id)]
            target_model["targets"] = []
            added_models += 1
        targets = model_targets(target_model)
        existing_targets = {
            (
                str(target.get("provider") or ""),
                str(target.get("pool") or ""),
                str(target.get("upstream_model") or model_id),
            )
            for target in targets
        }
        for target in model_targets(transferred_model):
            mapped = pool_name_map.get((str(target.get("provider") or ""), str(target.get("pool") or "")))
            if not mapped:
                continue
            provider_id, pool_name = mapped
            if not pool_name:
                continue
            new_target = {
                "provider": provider_id,
                "pool": pool_name,
                "upstream_model": str(target.get("upstream_model") or model_id),
            }
            target_key = (new_target["provider"], new_target["pool"], new_target["upstream_model"])
            if target_key not in existing_targets:
                targets.append(new_target)
                existing_targets.add(target_key)

    return merged_data, added_models, added_keys, skipped_keys


def export_config_interactively(path: Path) -> ResultPage:
    data = load_config_data(path)
    visitor_installed = visitor_feature_available()
    transfer_data = transferable_key_config(data, include_visitor=visitor_installed)
    config_text = json.dumps(transfer_data, ensure_ascii=False, separators=(",", ":"))
    model_count = len(transfer_data["models"])
    key_count = sum(len(provider_keys(provider)) for provider in transfer_data["providers"].values())
    visitor_message = "包含访客访问权限。" if visitor_installed else ""
    content = section_panel(
        f"配置文件: [bold]{path.resolve()}[/bold]\n模型数量: [bold]{model_count}[/bold]\n"
        f"Key 数量: [bold]{key_count}[/bold]\n\n"
        f"[bold yellow]复制内容仅包含模型与上游 API key，{visitor_message}请仅粘贴到可信终端。[/bold yellow]\n\n"
        "复制内容为单行 JSON。本地鉴权、监听地址、端口及其他 CLI 设置不会复制。\n\n"
        "在另一台机器或另一个 TUI 中进入“CLI 设置 → 配置迁移 → 粘贴并应用”即可导入。",
        "复制 Key 配置",
        "green",
    )
    return ResultPage(content, copy_text=config_text, copy_label="复制 Key 配置")


def paste_config_interactively(path: Path) -> Any:
    text = prompt_text("粘贴并应用", "请粘贴单行 Key 配置 JSON，然后按 Enter").strip()
    if not text:
        return section_panel("未输入配置内容。", "应用取消", "yellow")
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        return section_panel(f"粘贴内容不是有效 JSON: {exc.msg}", "应用失败", "red")
    if not isinstance(data, dict):
        return section_panel("粘贴内容必须是配置对象。", "应用失败", "red")
    try:
        transfer_data = transferable_key_config(
            data, include_visitor=visitor_feature_available()
        )
        RouterConfig.from_dict(transfer_data)
        current_data = load_config_data(path)
        old_config = RouterConfig.from_dict(current_data)
        merged_data, added_models, added_keys, skipped_keys = (
            merge_transferable_key_config(current_data, transfer_data)
        )
        RouterConfig.from_dict(merged_data)
    except (KeyError, TypeError, ValueError) as exc:
        return section_panel(f"配置校验失败: {exc}", "应用失败", "red")
    if not confirm_choice(
        f"将追加粘贴的 Key 配置，并保留现有模型和本机 CLI 设置：{path.resolve()}，是否继续？",
        default=False,
    ):
        return section_panel("配置未变化。", "应用取消", "yellow")
    try:
        new_config = commit_config_data(path, merged_data, old_config).new_config
    except (KeyError, TypeError, ValueError) as exc:
        return section_panel(f"配置校验失败: {exc}", "应用失败", "red")
    model_count = len(new_config.models)
    key_count = sum(len(model.keys) for model in new_config.models)
    content = section_panel(
        f"已追加粘贴的 Key 配置，并保留现有模型和本机 CLI 设置。\n配置文件: [bold]{path.resolve()}[/bold]\n"
        f"新增模型: [bold]{added_models}[/bold]\n新增 Key: [bold]{added_keys}[/bold]\n"
        f"跳过重复 Key: [bold]{skipped_keys}[/bold]\n"
        f"当前模型数量: [bold]{model_count}[/bold]\n当前 Key 数量: [bold]{key_count}[/bold]",
        "应用完成",
        "green",
    )
    return Group(
        content, restart_service_after_config_change(path, old_config, new_config)
    )


def select_model_id_for_new_key(models: dict[str, Any]) -> str | None:
    if not models:
        return prompt_text("添加 Key", "新模型 ID").strip()

    model_ids = sorted(models)
    options = [
        (
            str(index + 1),
            f"{short_text(model_id, 32)} · {len(model_targets(models[model_id]))} Target",
        )
        for index, model_id in enumerate(model_ids)
    ]
    options.extend([("n", "自定义添加新的模型 ID"), ("0", "返回")])
    choice = select_option("选择模型", options)
    if choice == "0":
        return None
    if choice == "n":
        return prompt_text("添加 Key", "新模型 ID").strip()
    return model_ids[int(choice) - 1]


def configured_base_urls(data: dict[str, Any], selected_model: dict[str, Any]) -> list[str]:
    base_urls: list[str] = []

    def append(value: Any) -> None:
        base_url = str(value or "").strip()
        if base_url and base_url not in base_urls:
            base_urls.append(base_url)

    append(selected_model.get("base_url"))
    for provider in raw_providers(data).values():
        if isinstance(provider, dict):
            append(provider.get("base_url"))
    append("https://api.openai.com")
    return base_urls


def select_base_url_for_new_key(
    data: dict[str, Any], selected_model: dict[str, Any]
) -> str | None:
    base_urls = configured_base_urls(data, selected_model)
    options = [
        (str(index + 1), compact_url(base_url, 48))
        for index, base_url in enumerate(base_urls)
    ]
    options.extend([("n", "自定义添加新的上游 URL"), ("0", "返回")])
    choice = select_option("选择上游 URL", options)
    if choice == "0":
        return None
    if choice == "n":
        default_base_url = str(data.get("default_base_url") or "https://api.openai.com")
        return prompt_text(
            "添加 Key", "新的上游 base_url", default=default_base_url
        ).strip()
    return base_urls[int(choice) - 1]


def discover_upstream_models_result(
    base_url: str,
    api_key: str,
    existing_model_ids: set[str],
    timeout: float = 15.0,
) -> tuple[list[str], str | None]:
    try:
        auth_header = f"Bearer {api_key}"
        auth_header.encode("ascii")
    except UnicodeEncodeError:
        return [], "API Key 仅支持 ASCII 字符"
    try:
        with httpx.Client(timeout=timeout) as client:
            response = client.get(
                _join_url(base_url, "/v1/models"),
                headers={"Authorization": auth_header},
            )
            response.raise_for_status()
            data = response.json()
    except httpx.HTTPStatusError as exc:
        return [], f"HTTP {exc.response.status_code}"
    except httpx.RequestError as exc:
        return [], f"网络错误: {exc}"[:160]
    except ValueError as exc:
        return [], f"JSON 解析失败: {exc}"[:160]
    if not isinstance(data, dict) or "data" not in data:
        return [], "响应 JSON 格式无效"
    items = data["data"]
    if not isinstance(items, list):
        return [], "响应 JSON 格式无效"
    try:
        model_ids = [str(item["id"]) for item in items]
    except (KeyError, TypeError, ValueError):
        return [], "响应 JSON 格式无效"
    return sorted(model_ids), None


def discover_upstream_models(
    base_url: str,
    api_key: str,
    existing_model_ids: set[str],
    timeout: float = 15.0,
) -> list[str]:
    return discover_upstream_models_result(
        base_url, api_key, existing_model_ids, timeout=timeout
    )[0]


def probe_payload_for_mode(mode: str, model_id: str) -> dict[str, Any]:
    if mode == "responses":
        return {"model": model_id, "input": ".", "max_output_tokens": 1}
    return {
        "model": model_id,
        "messages": [{"role": "user", "content": "."}],
        "max_tokens": 1,
    }


def _probe_error_text(response: httpx.Response) -> str:
    try:
        data = response.json()
    except ValueError:
        return response.text[:160]
    if isinstance(data, dict):
        error = data.get("error")
        if isinstance(error, dict):
            return str(error.get("message") or error.get("type") or error)[:160]
        if error:
            return str(error)[:160]
        message = data.get("message")
        if message:
            return str(message)[:160]
    return response.text[:160]


def probe_key_availability(
    data: dict[str, Any],
    model_id: str,
    key: dict[str, Any],
    timeout: float = 15.0,
) -> list[KeyProbeResult]:
    key_name = str(key.get("name") or f"{model_id}-key")
    api_key = str(key.get("api_key") or "")
    base_url = str(
        key.get("base_url") or data.get("default_base_url") or "https://api.openai.com"
    )
    routes = upstream_routes_for_base_url(data, base_url)
    results: list[KeyProbeResult] = []

    try:
        auth_header = f"Bearer {api_key}"
        auth_header.encode("ascii")
    except UnicodeEncodeError:
        error = "API Key 包含非 ASCII 字符，无法作为 HTTP Header 发送"
        return [
            KeyProbeResult(
                model_id=model_id,
                key_name=key_name,
                mode=mode,
                label=UPSTREAM_ROUTE_LABELS[mode],
                path=upstream_route_path(routes, mode),
                url=_join_url(base_url, upstream_route_path(routes, mode)),
                available=False,
                status_code=None,
                duration_ms=0,
                error=error,
            )
            for mode in PROBE_ROUTE_MODES
        ]

    with httpx.Client(timeout=timeout) as client:
        for mode in PROBE_ROUTE_MODES:
            path = upstream_route_path(routes, mode)
            url = _join_url(base_url, path)
            started = monotonic()
            status_code: int | None = None
            error = ""
            try:
                response = client.post(
                    url,
                    headers={"Authorization": f"Bearer {api_key}"},
                    json=probe_payload_for_mode(mode, model_id),
                )
                status_code = response.status_code
                available = 200 <= response.status_code < 300
                if not available:
                    error = _probe_error_text(response)
            except httpx.RequestError as exc:
                available = False
                error = str(exc)[:160]
            duration_ms = int((monotonic() - started) * 1000)
            results.append(
                KeyProbeResult(
                    model_id=model_id,
                    key_name=key_name,
                    mode=mode,
                    label=UPSTREAM_ROUTE_LABELS[mode],
                    path=path,
                    url=url,
                    available=available,
                    status_code=status_code,
                    duration_ms=duration_ms,
                    error=error,
                )
            )
    return results


def probe_all_key_availability(
    data: dict[str, Any], timeout: float = 15.0
) -> list[KeyProbeResult]:
    results: list[KeyProbeResult] = []
    for model in data.get("models", []):
        model_id = str(model.get("id") or "")
        for key in model.get("keys", []):
            results.extend(probe_key_availability(data, model_id, key, timeout=timeout))
    return results


def key_probe_results_panel(results: list[KeyProbeResult], title: str = "Key 可用性探测") -> Any:
    if not results:
        return section_panel("[yellow]没有可探测的 Key。[/yellow]", title, "yellow")
    table = Table(show_header=True, header_style="bold cyan")
    table.add_column("模型", overflow="fold")
    table.add_column("Key", overflow="fold")
    table.add_column("路径", overflow="fold")
    table.add_column("状态")
    table.add_column("HTTP")
    table.add_column("耗时")
    table.add_column("错误", overflow="fold")
    for result in results:
        status = "[green]可用[/green]" if result.available else "[red]不可用[/red]"
        table.add_row(
            short_text(result.model_id, 24),
            short_text(result.key_name, 24),
            result.path,
            status,
            str(result.status_code) if result.status_code is not None else "-",
            f"{result.duration_ms}ms",
            result.error or "-",
        )
    available_count = sum(1 for result in results if result.available)
    summary = f"可用路径: [bold]{available_count}/{len(results)}[/bold]"
    return Group(section_panel(summary, title, "cyan"), section_panel(table, "探测结果", "cyan"))


def probe_selected_key_interactively(
    path: Path, data: dict[str, Any], model: dict[str, Any], key_index: int
) -> Any:
    key = model["keys"][key_index]
    with console.status("[cyan]正在探测当前 Key...[/cyan]", spinner="dots"):
        results = probe_key_availability(data, str(model["id"]), key)
    return key_probe_results_panel(results, "当前 Key 可用性")


def probe_all_keys_interactively(path: Path) -> Any:
    data = load_config_data(path)
    with console.status("[cyan]正在探测所有 Key...[/cyan]", spinner="dots"):
        results = probe_all_key_availability(data)
    return key_probe_results_panel(results, "所有 Key 可用性")


def _select_model_with_discovery(
    models: dict[str, Any],
    base_url: str,
    api_key: str,
) -> tuple[str | None, list[str]]:
    existing_ids = {str(model_id) for model_id in models}
    with console.status("[cyan]正在探测上游可用模型...[/cyan]", spinner="dots"):
        discovered = discover_upstream_models(base_url, api_key, existing_ids)
    available = [mid for mid in discovered if mid in models]
    new_models = [mid for mid in discovered if mid not in existing_ids]
    manual_entry = ("n", "自定义添加新的模型 ID")
    return_entry = ("0", "返回")

    if available or new_models:
        options: list[tuple[str, str]] = []
        for mid in available:
            options.append((mid, f"{short_text(mid, 32)} · 已有 · {len(model_targets(models[mid]))} Target"))
        for mid in new_models:
            options.append((mid, f"{short_text(mid, 32)} · 新模型"))
        options.extend([manual_entry, return_entry])
        content = section_panel(
            f"已探测到 [bold]{len(discovered)}[/bold] 个上游模型，直接选择即可为已有模型添加路由，或选择新模型。",
            "发现上游模型",
            "cyan",
        )
        choice = select_option("选择模型", options, content=content)
    else:
        return select_model_id_for_new_key(models), []

    if choice == "0":
        return None, []
    if choice == "n":
        return prompt_text("添加 Key", "新模型 ID").strip(), new_models
    return choice, new_models


def add_config_interactively(path: Path, ask_continue: bool = True) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    providers = raw_providers(data)
    models = raw_v2_models(data)

    base_url = select_base_url_for_new_key(data, {})
    if base_url is None:
        return None
    if not base_url:
        return section_panel("[red]上游 base_url 不能为空[/red]", "添加失败", "red")
    api_key = prompt_text("添加 Key", "API key", password=True).strip()
    if not api_key:
        return section_panel("[red]API key 不能为空[/red]", "添加失败", "red")

    model_id, discovered_new_models = _select_model_with_discovery(models, base_url, api_key)
    if model_id is None:
        return None
    if not model_id:
        return section_panel("[red]模型 ID 不能为空[/red]", "添加失败", "red")

    model = models.get(model_id)
    is_new_model = model is None
    if model is None:
        model = {"aliases": [], "routing_mode": "round_robin", "targets": []}
        models[model_id] = model

    if is_new_model:
        aliases_text = prompt_text(
            "添加 Key",
            "显示名称/别名，多个用逗号分隔",
            default=",".join(model.get("aliases", [])),
        ).strip()
        model["aliases"] = (
            [alias.strip() for alias in aliases_text.split(",") if alias.strip()]
            if aliases_text
            else []
        )
        model["routing_mode"] = prompt_text(
            "添加 Key",
            "路由模式",
            choices=["priority", "round_robin", "only_first"],
            default=str(model.get("routing_mode") or "round_robin"),
        ).strip()
        reasoning_effort = prompt_text(
            "添加 Key",
            "推理强度",
            choices=["downstream", "none", "minimal", "low", "medium", "high", "xhigh"],
            default=normalize_reasoning_effort_choice(model.get("reasoning_effort")),
        ).strip()
        if reasoning_effort == "downstream":
            model.pop("reasoning_effort", None)
        else:
            model["reasoning_effort"] = reasoning_effort

    provider_id = "default"
    provider = providers.setdefault(provider_id, {"base_url": base_url, "keys": {}, "pools": {}})
    provider["base_url"] = base_url
    keys = provider_keys(provider)
    default_key_name = f"{model_id}-key-{len(keys) + 1}"
    key_name = (
        prompt_text("添加 Key", "Key 名称", default=default_key_name).strip()
        or default_key_name
    )
    keys[key_name] = {"api_key": api_key, "enabled": True}
    pool_name = key_name
    provider_pools(provider)[pool_name] = {"keys": [key_name]}
    model_targets(model).append(
        {"provider": provider_id, "pool": pool_name, "upstream_model": model_id}
    )
    new_config = commit_config_data(path, data, old_config).new_config

    added_by_discovery = 0
    if discovered_new_models:
        skip_marker = "__skip__"
        multi_options = [(mid, mid) for mid in discovered_new_models]
        multi_options.append((skip_marker, "跳过，不批量添加"))
        selected_multi = select_multiple(
            "快速添加其他模型",
            multi_options,
            content=section_panel(
                f"探测到 [bold]{len(discovered_new_models)}[/bold] 个其他模型，选择要一同添加的模型。",
                "批量添加",
                "cyan",
            ),
        )
        selected_multi = [mid for mid in selected_multi if mid != skip_marker]
        if selected_multi:
            added_by_discovery = _add_discovered_models(path, selected_multi, api_key, base_url)
            if added_by_discovery > 0:
                new_config = RouterConfig.load(path)

    discovery_note = ""
    if added_by_discovery > 0:
        discovery_note = f"\n上游自动发现并添加: [bold]{added_by_discovery}[/bold] 个模型"
    result = Group(
        section_panel(
            f"已写入配置文件: [bold]{path}[/bold]\n模型: [bold]{model_id}[/bold]\nKey: [bold]{key_name}[/bold]{discovery_note}",
            "添加完成",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )
    if ask_continue and not confirm_choice("继续启动服务？", default=False):
        raise SystemExit(0)
    return result


def _add_discovered_models(path: Path, model_ids: list[str], api_key: str, base_url: str) -> int:
    added = 0
    for mid in model_ids:
        data = load_config_data(path)
        old_config = RouterConfig.from_dict(data)
        models = raw_v2_models(data)
        if mid in models:
            continue
        provider = raw_providers(data).setdefault("default", {"base_url": base_url, "keys": {}, "pools": {}})
        provider["base_url"] = base_url
        key_name = f"{mid}-key-1"
        provider_keys(provider)[key_name] = {"api_key": api_key, "enabled": True}
        provider_pools(provider)[key_name] = {"keys": [key_name]}
        models[mid] = {
            "aliases": [],
            "routing_mode": "round_robin",
            "targets": [{"provider": "default", "pool": key_name, "upstream_model": mid}],
        }
        try:
            commit_config_data(path, data, old_config)
            added += 1
        except (KeyError, TypeError, ValueError):
            continue
    return added


def edit_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要编辑的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    old_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    key_name = prompt_text("编辑 Key", "Key 名称", default=old_name).strip() or old_name
    key["name"] = key_name
    key["base_url"] = prompt_text(
        "编辑 Key",
        "上游 base_url",
        default=str(
            key.get("base_url")
            or data.get("default_base_url")
            or "https://api.openai.com"
        ),
    ).strip()
    api_key = prompt_text(
        "编辑 Key", "新 API key（留空则不修改）", default="", password=True
    ).strip()
    if api_key:
        key["api_key"] = api_key
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            f"已更新配置文件: [bold]{path}[/bold]\n模型: [bold]{model['id']}[/bold]\nKey: [bold]{key_name}[/bold]",
            "编辑完成",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def delete_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要删除的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    if not confirm_choice(
        f"确认删除模型 {model['id']} 的 Key {key_name}？", default=False
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除取消", "yellow")
    del model["keys"][key_index]
    if not model["keys"]:
        data["models"].remove(model)
        message = f"已删除 Key: [bold]{key_name}[/bold]\n模型 [bold]{model['id']}[/bold] 已无 API key，已一并移除。"
    else:
        message = (
            f"已删除 Key: [bold]{key_name}[/bold]\n模型: [bold]{model['id']}[/bold]"
        )
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(message, "删除完成", "green"),
        restart_service_after_config_change(path, old_config, new_config),
    )


def copy_api_key_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要复制的 API key")
    if selection is None:
        return None
    data, model, key_index = selection
    key = model["keys"][key_index]
    api_key = str(key.get("api_key") or "")
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    base_url = compact_url(
        key.get("base_url") or data.get("default_base_url") or "-", 48
    )
    content = section_panel(
        f"模型: [bold]{short_text(model['id'], 48)}[/bold]\nKey: [bold]{short_text(key_name, 48)}[/bold]\n上游: [bold]{base_url}[/bold]\n指纹: [bold]{key_fingerprint(api_key)}[/bold]\n\n选择“复制 API key”即可写入剪贴板。",
        "复制 API key",
        "green",
    )
    return ResultPage(content, copy_text=api_key, copy_label="复制 API key")


def reorder_api_keys_interactively(path: Path) -> Any:
    data = load_config_data(path)
    selectable_models = [
        model for model in data.get("models", []) if len(model.get("keys", [])) > 1
    ]
    if not selectable_models:
        return section_panel(
            "[yellow]暂无可排序模型，需至少 2 个 Key。[/yellow]", "Key 排序", "yellow"
        )
    model_options = [
        (
            str(index + 1),
            f"{short_text(model['id'], 28)} · {len(model.get('keys', []))} Key",
        )
        for index, model in enumerate(selectable_models)
    ] + [("0", "返回")]
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    model = selectable_models[int(model_choice) - 1]
    old_config = RouterConfig.from_dict(data)
    keys = model.get("keys", [])
    selected = 0
    while True:
        action, selected = select_reorder_key_action(model, selected)
        if action == "cancel":
            return section_panel("[yellow]配置未变化。[/yellow]", "Key 排序", "yellow")
        if action == "save":
            new_config = commit_config_data(path, data, old_config).new_config
            return Group(
                section_panel(key_order_text(model), "顺序已保存", "green"),
                restart_service_after_config_change(path, old_config, new_config),
            )
        if action == "up" and selected > 0:
            keys[selected - 1], keys[selected] = keys[selected], keys[selected - 1]
            selected -= 1
        if action == "down" and selected < len(keys) - 1:
            keys[selected + 1], keys[selected] = keys[selected], keys[selected + 1]
            selected += 1


def toggle_key_enabled_interactively(path: Path) -> Any:
    selection = select_api_key(path, "选择要切换状态的 Key")
    if selection is None:
        return None
    data, model, key_index = selection
    old_config = RouterConfig.from_dict(data)
    key = model["keys"][key_index]
    key_name = str(key.get("name") or f"{model['id']}-{key_index + 1}")
    current_enabled = key.get("enabled", True)
    new_enabled = not current_enabled
    key["enabled"] = new_enabled
    new_config = commit_config_data(path, data, old_config).new_config
    old_status = "启用" if current_enabled else "禁用"
    new_status = "启用" if new_enabled else "禁用"
    return Group(
        section_panel(
            f"已切换 Key 状态。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\nKey: [bold]{key_name}[/bold]\n原状态: [bold]{old_status}[/bold]\n新状态: [bold]{new_status}[/bold]",
            "Key 开关",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def select_reorder_key_action(
    model: dict[str, Any], selected: int = 0
) -> tuple[str, int]:
    frame_offset = 0
    frame_state = render_key_order_menu_state(model, selected, frame_offset)
    last_wheel_key: str | None = None
    last_wheel_at = 0.0

    def refresh(*, ensure_selected_visible: bool) -> None:
        nonlocal frame_offset, frame_state
        frame_state = render_key_order_menu_state(
            model,
            selected,
            frame_offset,
            ensure_selected_visible=ensure_selected_visible,
        )
        frame_offset = frame_state.offset
        live.update(frame_state.renderable, refresh=True)

    with (
        posix_input_mode(),
        mouse_wheel_mode(),
        Live(
            frame_state.renderable, console=console, screen=True, auto_refresh=False
        ) as live,
    ):
        while True:
            key = read_key_responsive(lambda: refresh(ensure_selected_visible=True))
            if key == "cancel":
                return "cancel", selected
            if key in {"scroll_up", "scroll_down"}:
                handle_wheel, last_wheel_key, last_wheel_at = should_handle_wheel(
                    key, last_wheel_key, last_wheel_at
                )
                if not handle_wheel:
                    continue
            if (
                key in {"page_up", "page_down", "home", "end"}
                and frame_state.max_offset
            ):
                frame_offset = content_scroll_offset(
                    key,
                    frame_offset,
                    frame_state.max_offset,
                    frame_state.viewport_height,
                )
                refresh(ensure_selected_visible=False)
                continue
            if key in {"up", "scroll_up"}:
                selected = max(0, selected - 1)
                refresh(ensure_selected_visible=True)
                continue
            if key in {"down", "scroll_down"}:
                selected = min(len(model.get("keys", [])) - 1, selected + 1)
                refresh(ensure_selected_visible=True)
                continue
            if key in {"w", "W"}:
                return "up", selected
            if key in {"s", "S"}:
                return "down", selected
            if key == "enter":
                return "save", selected


def render_key_order_menu_state(
    model: dict[str, Any],
    selected: int,
    frame_offset: int = 0,
    *,
    ensure_selected_visible: bool = True,
) -> Any:
    table = Table(show_header=False, box=None, padding=(0, 1), expand=True)
    table.add_column("指示", justify="center", width=3)
    table.add_column("顺序", justify="center", width=5)
    table.add_column("Key", ratio=1)
    table.add_column("上游", ratio=1)
    for index, key in enumerate(model.get("keys", [])):
        name = key_display_name(key, f"{model['id']}-{index + 1}")
        base_url = compact_url(key.get("base_url") or "-", 28)
        if index == selected:
            selected_name = (
                name
                if key.get("allow_visitor", False)
                else f"[bold cyan]{name}[/bold cyan]"
            )
            table.add_row(
                "[bold cyan]▶[/bold cyan]",
                f"[bold black on cyan] {index + 1} [/bold black on cyan]",
                selected_name,
                f"[bold cyan]{base_url}[/bold cyan]",
            )
        else:
            table.add_row("", f"[dim]{index + 1}[/dim]", name, base_url)
    shortcuts = (
        "↑/↓ 选择  ·  W/S 移动  ·  PgUp/PgDn 翻阅  ·  Enter 保存  ·  Ctrl+C 取消"
    )
    if sys.platform == "win32":
        shortcuts = "↑/↓/滚轮 选择  ·  W/S 移动  ·  PgUp/PgDn 翻阅  ·  Enter 保存  ·  Ctrl+C 取消"
    return terminal_frame_state(
        [
            page_title("Key 排序", f"模型 · {short_text(model['id'], 24)}"),
            section_panel(
                table, "Key 顺序", "cyan", "[dim]选择 Key 后用 W/S 调整优先级[/dim]"
            ),
        ],
        shortcut_text(shortcuts),
        offset=frame_offset,
        focus_text="▶" if ensure_selected_visible else None,
    )


def render_key_order_menu(model: dict[str, Any], selected: int) -> Any:
    return render_key_order_menu_state(model, selected).renderable


def key_order_text(model: dict[str, Any]) -> str:
    lines = [f"模型: [bold]{short_text(model['id'], 32)}[/bold]", "当前顺序:"]
    for index, key in enumerate(model.get("keys", [])):
        name = key_display_name(key, f"{model['id']}-{index + 1}", 32)
        base_url = compact_url(key.get("base_url") or "-", 32)
        lines.append(f"{index + 1}. {name} · {base_url}")
    return "\n".join(lines)


def set_model_routing_mode_interactively(path: Path) -> Any:
    data = load_config_data(path)
    models = data.get("models", [])
    if not models:
        return section_panel("[yellow]还没有模型配置。[/yellow]", "路由模式", "yellow")
    model_options = []
    for index, model in enumerate(models):
        routing_mode = str(
            model.get("routing_mode") or data.get("routing_mode") or "round_robin"
        )
        routing_mode_text = routing_mode_display_text(routing_mode)
        model_options.append(
            (str(index + 1), f"{short_text(model['id'], 28)} · {routing_mode_text}")
        )
    model_options.append(("0", "返回"))
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    old_config = RouterConfig.from_dict(data)
    model = models[int(model_choice) - 1]
    current_mode = str(
        model.get("routing_mode") or data.get("routing_mode") or "round_robin"
    )
    mode_choice = select_option(
        "选择路由模式",
        [
            ("1", "分流：轮询"),
            ("2", "优先级：按顺序"),
            ("3", "仅首个：只重试第一个 Key"),
            ("0", "返回"),
        ],
        selected={"round_robin": 0, "priority": 1, "only_first": 2}.get(
            current_mode, 0
        ),
    )
    if mode_choice == "0":
        return section_panel("[yellow]配置未变化。[/yellow]", "路由模式", "yellow")
    new_mode = {"1": "round_robin", "2": "priority", "3": "only_first"}[mode_choice]
    if new_mode == current_mode:
        mode_text = routing_mode_display_text(new_mode)
        return section_panel(
            f"模型 [bold]{short_text(model['id'], 32)}[/bold] 已是 [bold]{mode_text}[/bold]。",
            "路由模式",
            "yellow",
        )
    model["routing_mode"] = new_mode
    new_config = commit_config_data(path, data, old_config).new_config
    old_mode_text = routing_mode_display_text(current_mode)
    new_mode_text = routing_mode_display_text(new_mode)
    return Group(
        section_panel(
            f"已更新路由模式。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n原模式: [bold]{old_mode_text}[/bold]\n新模式: [bold]{new_mode_text}[/bold]",
            "路由模式",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def routing_mode_display_text(value: str | None) -> str:
    return {"round_robin": "分流", "priority": "优先级", "only_first": "仅首个"}.get(
        str(value or "round_robin"), "分流"
    )


def set_model_reasoning_effort_interactively(path: Path) -> Any:
    data = load_config_data(path)
    models = data.get("models", [])
    if not models:
        return section_panel("[yellow]还没有模型配置。[/yellow]", "推理强度", "yellow")
    model_options = []
    for index, model in enumerate(models):
        effort = normalize_reasoning_effort_choice(model.get("reasoning_effort"))
        model_options.append(
            (
                str(index + 1),
                f"{short_text(model['id'], 28)} · {reasoning_effort_text(effort)}",
            )
        )
    model_options.append(("0", "返回"))
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    old_config = RouterConfig.from_dict(data)
    model = models[int(model_choice) - 1]
    current_effort = normalize_reasoning_effort_choice(model.get("reasoning_effort"))
    effort_choice = select_option(
        "选择推理强度",
        [
            ("1", "由下游决定"),
            ("2", "关闭 reasoning"),
            ("3", "minimal"),
            ("4", "low"),
            ("5", "medium"),
            ("6", "high"),
            ("7", "xhigh"),
            ("0", "返回"),
        ],
        selected={
            "downstream": 0,
            "none": 1,
            "minimal": 2,
            "low": 3,
            "medium": 4,
            "high": 5,
            "xhigh": 6,
        }.get(current_effort, 0),
    )
    if effort_choice == "0":
        return section_panel("[yellow]配置未变化。[/yellow]", "推理强度", "yellow")
    new_effort = {
        "1": "downstream",
        "2": "none",
        "3": "minimal",
        "4": "low",
        "5": "medium",
        "6": "high",
        "7": "xhigh",
    }[effort_choice]
    if new_effort == current_effort:
        return section_panel(
            f"模型 [bold]{short_text(model['id'], 32)}[/bold] 已是 [bold]{reasoning_effort_text(new_effort)}[/bold]。",
            "推理强度",
            "yellow",
        )
    if new_effort == "downstream":
        model.pop("reasoning_effort", None)
    else:
        model["reasoning_effort"] = new_effort
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            f"已更新推理强度。\n模型: [bold]{short_text(model['id'], 32)}[/bold]\n原强度: [bold]{reasoning_effort_text(current_effort)}[/bold]\n新强度: [bold]{reasoning_effort_text(new_effort)}[/bold]",
            "推理强度",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def reasoning_effort_text(value: str | None) -> str:
    effort = normalize_reasoning_effort_choice(value)
    return {
        "downstream": "由下游决定",
        "none": "关闭 reasoning",
        "minimal": "minimal",
        "low": "low",
        "medium": "medium",
        "high": "high",
        "xhigh": "xhigh",
    }.get(effort, effort)


def normalize_reasoning_effort_choice(value: Any) -> str:
    effort = str(value or "").strip()
    return "downstream" if effort in {"", "default", "downstream"} else effort


def select_api_key(
    path: Path, title: str
) -> tuple[dict[str, Any], dict[str, Any], int] | None:
    data = load_config_data(path)
    selectable_models = [model for model in data.get("models", []) if model.get("keys")]
    if not selectable_models:
        return None
    model_options = [
        (
            str(index + 1),
            f"{short_text(model['id'], 28)} · {len(model.get('keys', []))} Key",
        )
        for index, model in enumerate(selectable_models)
    ] + [("0", "返回")]
    model_choice = select_option("选择模型", model_options)
    if model_choice == "0":
        return None
    model = selectable_models[int(model_choice) - 1]
    key_options = []
    for index, key in enumerate(model.get("keys", [])):
        name = key_display_name(key, f"{model['id']}-{index + 1}")
        base_url = compact_url(
            key.get("base_url") or data.get("default_base_url") or "-", 28
        )
        enabled = key.get("enabled", True)
        status = "[green]启用[/green]" if enabled else "[red]禁用[/red]"
        key_options.append((str(index + 1), f"{name} · {base_url} · {status}"))
    key_options.append(("0", "返回"))
    key_choice = select_option(title, key_options)
    if key_choice == "0":
        return None
    return data, model, int(key_choice) - 1


def set_local_api_key_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    if data.get("local_api_key") and not confirm_choice(
        "是否重置本地鉴权密钥？", default=True
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "本地鉴权", "yellow")
    local_api_key = generate_local_api_key()
    data["local_api_key"] = local_api_key
    new_config = commit_config_data(path, data, old_config).new_config
    content = Group(
        section_panel(
            f"已生成新密钥。\n\n[bold]{local_api_key}[/bold]\n\n请求时添加：\nAuthorization: Bearer <key>",
            "本地鉴权",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )
    return ResultPage(content, copy_text=local_api_key, copy_label="复制本地鉴权 key")


def set_timeouts_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    fields = (
        ("request_timeout", "普通请求超时（秒）", old_config.request_timeout),
        (
            "stream_first_byte_timeout",
            "流式首字节超时（秒）",
            old_config.stream_first_byte_timeout,
        ),
        (
            "stream_idle_timeout",
            "流式空闲超时（秒）",
            old_config.stream_idle_timeout,
        ),
    )
    values: dict[str, float] = {}
    for field, label, current in fields:
        try:
            value = float(
                prompt_text("超时配置", label, default=str(current)).strip()
            )
        except ValueError:
            return section_panel(
                "[red]超时必须是数字。[/red]", "超时配置", "red"
            )
        if value <= 0:
            return section_panel(
                "[red]超时必须大于 0。[/red]", "超时配置", "red"
            )
        values[field] = value

    data.update(values)
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel(
            "已更新超时配置。\n"
            f"普通请求: [bold]{values['request_timeout']:g} 秒[/bold]\n"
            f"流式首字节: [bold]{values['stream_first_byte_timeout']:g} 秒[/bold]\n"
            f"流式空闲: [bold]{values['stream_idle_timeout']:g} 秒[/bold]",
            "超时配置",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def set_listen_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    current_host = str(data.get("host") or "127.0.0.1")
    current_port = int(data.get("port") or 8000)
    host = prompt_text("监听配置", "监听 IP/地址", default=current_host).strip()
    if not host:
        return section_panel("[red]监听 IP/地址不能为空。[/red]", "监听配置", "red")
    if "://" in host or "/" in host:
        return section_panel(
            "[red]监听地址只填写 IP 或主机名，不要包含协议或路径。[/red]",
            "监听配置",
            "red",
        )
    port_text = prompt_text("监听配置", "监听端口", default=str(current_port)).strip()
    try:
        port = int(port_text)
    except ValueError:
        return section_panel("[red]端口必须是数字。[/red]", "监听配置", "red")
    if port < 1 or port > 65535:
        return section_panel("[red]端口范围必须是 1-65535。[/red]", "监听配置", "red")
    if host == current_host and port == current_port:
        return section_panel(
            f"监听配置未变化: [bold]{host}:{port}[/bold]", "监听配置", "yellow"
        )
    if (
        host == "0.0.0.0"
        and host != current_host
        and not confirm_choice(
            "0.0.0.0 会允许局域网/公网访问，确认继续？", default=False
        )
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "监听配置", "yellow")
    data["host"] = host
    data["port"] = port
    new_config = commit_config_data(path, data, old_config).new_config
    warning = (
        "\n[bold red]风险提示: 0.0.0.0 会暴露到所有可达网络，请确保防火墙和本地鉴权已正确配置。[/bold red]"
        if host == "0.0.0.0"
        else ""
    )
    return Group(
        section_panel(
            f"已更新监听配置。\n配置文件: [bold]{path}[/bold]\n旧配置: [bold]{current_host}:{current_port}[/bold]\n新配置: [bold]{host}:{port}[/bold]{warning}",
            "监听配置",
            "green",
        ),
        restart_service_after_config_change(path, old_config, new_config),
    )


def find_model(models: list[dict[str, Any]], model_id: str) -> dict[str, Any] | None:
    for model in models:
        if model.get("id") == model_id:
            return model
    return None




