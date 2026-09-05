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
from . import config_operations as operations
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




def service_management_base_url(data: dict[str, Any]) -> str:
    host = str(data.get("host") or "127.0.0.1")
    port = int(data.get("port") or 8000)
    connect_host = "127.0.0.1" if host in {"0.0.0.0", "::"} else host
    if ":" in connect_host and not connect_host.startswith("["):
        connect_host = f"[{connect_host}]"
    return f"http://{connect_host}:{port}"




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






def native_endpoint_support_text(state: dict[str, Any] | None) -> str:
    if not state:
        return "\n[dim]探测: 未测试[/dim]"
    reason = str(state.get("reason") or "-")
    if state.get("supported") is True:
        return f"\n[green]探测: 支持[/green] [dim]{reason}[/dim]"
    expires = state.get("expires_in_seconds")
    retry = f"，{int(expires)}s 后重试" if isinstance(expires, int) else ""
    return f"\n[yellow]探测: 回退缓存[/yellow] [dim]{reason}{retry}[/dim]"




def _open_config_on_key(path: Path, key: str) -> str | None:
    if key in {"o", "O"}:
        open_config_file(path)
    return None


def load_v2_config_data(path: Path) -> dict[str, Any]:
    return load_config_data(path)


def raw_providers(data: dict[str, Any]) -> dict[str, Any]:
    return operations.providers(data)


def raw_v2_models(data: dict[str, Any]) -> dict[str, Any]:
    return operations.models(data)


def provider_keys(provider: dict[str, Any]) -> dict[str, Any]:
    return operations.provider_keys(provider)






















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


def model_targets(model: dict[str, Any]) -> list[dict[str, Any]]:
    return operations.model_targets(model)


def v2_summary_panel(data: dict[str, Any]) -> Any:
    providers = raw_providers(data)
    models = raw_v2_models(data)
    visitor_installed = visitor_feature_available()

    provider_table = Table(show_header=True, header_style="bold cyan", expand=True)
    provider_table.add_column("供应商", ratio=1)
    provider_table.add_column("Base URL", ratio=2)
    provider_table.add_column("Keys", justify="right")
    if visitor_installed:
        provider_table.add_column("访客", justify="right")
    provider_table.add_column("路由", ratio=2)
    for provider_id, provider in sorted(providers.items()):
        keys = provider_keys(provider)
        visitor_keys = sum(1 for key in keys.values() if key.get("allow_visitor"))
        routes = provider.get("routes") if isinstance(provider.get("routes"), dict) else {}
        route_text = ", ".join(sorted(routes)) if routes else "默认"
        row = [
            short_text(str(provider_id), 18),
            compact_url(str(provider.get("base_url") or "-"), 42),
            str(len(keys)),
            short_text(route_text, 28),
        ]
        if visitor_installed:
            row.insert(3, format_visitor_status_text(visitor_keys > 0, visitor_installed))
        provider_table.add_row(*row)
    if not provider_table.rows:
        empty_row = ["-", "[yellow]暂无供应商[/yellow]", "0", "-"]
        if visitor_installed:
            empty_row.insert(3, "-")
        provider_table.add_row(*empty_row)

    model_table = Table(show_header=True, header_style="bold cyan", expand=True)
    model_table.add_column("本地模型", ratio=2)
    model_table.add_column("别名", ratio=2)
    model_table.add_column("路由模式", ratio=1)
    model_table.add_column("Keys", justify="right")
    for model_id, model in sorted(models.items()):
        aliases = ", ".join(str(alias) for alias in model.get("aliases", []) if str(alias))
        model_table.add_row(
            short_text(str(model_id), 28),
            short_text(aliases or "-", 36),
            str(model.get("routing_mode") or "round_robin"),
            str(len(model_targets(model))),
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


def select_provider_key(
    data: dict[str, Any], title: str, provider_id: str | None = None
) -> tuple[str, str] | None:
    if provider_id is None:
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












def probe_key_capability(
    provider: dict[str, Any],
    key_name: str,
    *,
    modes: list[str] | None = None,
    timeout: float = 15.0,
) -> dict[str, Any]:
    """Probe a single provider Key: /v1/models discovery plus one minimal
    request per upstream route mode, all made with that Key.

    Different keys of the same provider often have access to different model
    sets (e.g. a free tier vs a paid tier key), so the probe result is cached
    per key in providers.<id>.keys.<key>.capabilities and never folded across
    keys. ``modes`` restricts which route modes get the minimal request
    (defaults to all probe modes); route availability is a property of the
    key, since upstream may authorize endpoints per credential.
    """
    base_url = str(provider.get("base_url") or "").strip()
    keys = provider_keys(provider)
    raw_key = keys.get(key_name)
    errors: dict[str, str] = {}
    discovered: list[str] = []
    api_key = str(raw_key.get("api_key") or "") if isinstance(raw_key, dict) else ""
    if not base_url:
        errors["provider"] = "缺少 Base URL"
    elif not isinstance(raw_key, dict):
        errors[key_name] = "Key 不存在"
    elif not api_key:
        errors[key_name] = "API Key 为空"
    else:
        models, error = discover_upstream_models_result(
            base_url, api_key, set(), timeout=timeout
        )
        if error:
            errors[key_name] = error
        else:
            discovered = models
    route_status: dict[str, str] = {}
    probe_modes = [mode for mode in (modes or list(PROBE_ROUTE_MODES)) if mode in UPSTREAM_ROUTE_MODES]
    if api_key and base_url and not errors:
        probe_key = dict(raw_key)
        probe_key["name"] = key_name
        probe_key["base_url"] = base_url
        routes = provider.get("routes") if isinstance(provider.get("routes"), dict) else {}
        probe_data = {"upstream_routes": {base_url: routes}}
        model_for_probe = (discovered or ["probe-model"])[0]
        for result in probe_key_availability(
            probe_data, model_for_probe, probe_key, timeout=timeout, modes=probe_modes
        ):
            if result.mode in probe_modes:
                route_status[result.mode] = (
                    "ok" if result.available else f"failed: {result.error or result.status_code}"
                )
    return {
        "models": discovered,
        "route_status": route_status,
        "errors": errors,
        "checked_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    }


def provider_capabilities_panel(provider: dict[str, Any]) -> Any:
    """Per-key capability summary used as the provider detail content."""
    keys = provider_keys(provider)
    if not keys:
        return section_panel(
            "[yellow]该供应商暂无 Key。添加 Key 时会自动探测其可用模型。[/yellow]",
            "供应商能力",
            "yellow",
        )
    blocks: list[Any] = []
    any_probed = False
    for key_name, key in sorted(keys.items()):
        capabilities = key.get("capabilities") if isinstance(key, dict) else None
        enabled_text = "启用" if key.get("enabled", True) else "禁用"
        if not isinstance(capabilities, dict):
            blocks.append(
                section_panel(
                    f"[yellow]尚未探测。[/yellow]",
                    f"Key · {key_name} · {enabled_text}",
                    "yellow",
                )
            )
            continue
        any_probed = True
        models = capabilities.get("models") or []
        route_status = capabilities.get("route_status") or {}
        checked_at = capabilities.get("checked_at") or "-"
        errors = capabilities.get("errors") or {}
        lines = [f"探测时间: [bold]{checked_at}[/bold]"]
        if errors:
            lines.append("[red]探测错误: " + "; ".join(str(v) for v in errors.values()) + "[/red]")
        if route_status:
            lines.append(
                "路由: "
                + " · ".join(
                    f"{UPSTREAM_ROUTE_LABELS.get(mode, mode)}: [{'green' if str(status) == 'ok' else 'red'}]{status}[/{'green' if str(status) == 'ok' else 'red'}]"
                    for mode, status in sorted(route_status.items())
                )
            )
        if models:
            lines.append(f"模型（{len(models)}）: " + ", ".join(short_text(str(model_id), 40) for model_id in models[:8]))
            if len(models) > 8:
                lines.append(f"[dim]… 共 {len(models)} 个模型[/dim]")
        else:
            lines.append("[dim]未发现模型[/dim]")
        blocks.append(section_panel("\n".join(lines), f"Key · {key_name} · {enabled_text}", "cyan"))
    if not any_probed:
        blocks.insert(
            0,
            section_panel(
                "[yellow]全部 Key 尚未探测。添加 Key 时自动探测该 Key，或选「刷新能力探测」。[/yellow]",
                "提示",
                "yellow",
            ),
        )
    return Group(*blocks) if len(blocks) > 1 else blocks[0]


def select_models_to_serve(
    provider_id: str,
    key_name: str,
    models: list[str],
    *,
    extra_title: str = "该 Key 服务哪些模型",
) -> list[str] | None:
    """Multi-select which models a key should serve; returns [] to skip."""
    all_ids = sorted({str(model_id) for model_id in models if str(model_id)})
    if not all_ids:
        manual = prompt_text(
            extra_title,
            "上游未发现模型，请手动填写可用模型（逗号分隔）",
        ).strip()
        if not manual:
            return None
        return [item.strip() for item in manual.split(",") if item.strip()]
    selected = select_multiple(
        extra_title,
        [(model_id, model_id) for model_id in all_ids],
        content=section_panel(
            "选择后这些模型会创建/更新为本地模型，并把该 Key 绑定到它们。\n"
            "取消勾选表示该 Key 不服务该模型。",
            "说明",
            "cyan",
        ),
    )
    if selected is None:
        return None
    return selected


def add_provider_key_interactively(
    path: Path, provider_id: str | None = None, *, create_provider: bool = False
) -> Any:
    draft = form_draft("add_provider_key")
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    providers = raw_providers(data)
    if provider_id is None:
        existing = sorted(providers)
        provider_choice = "n" if create_provider else select_option(
            "供应商",
            [("n", "新供应商")]
            + [(str(index + 1), current_id) for index, current_id in enumerate(existing)]
            + [("0", "返回")],
            content=v2_summary_panel(data),
        )
        if provider_choice == "0":
            return None
        if provider_choice == "n":
            provider_id = prompt_text(
                "添加供应商",
                "供应商 ID",
                default=draft.get("provider_id", "openai"),
            ).strip()
            draft["provider_id"] = provider_id
            if not provider_id:
                return section_panel("[red]供应商 ID 不能为空[/red]", "添加失败", "red")
            if provider_id in providers:
                return section_panel(f"[red]供应商已存在: {provider_id}[/red]", "添加失败", "red")
            base_url = prompt_text(
                "添加供应商",
                "Base URL",
                default=draft.get("base_url", "https://api.openai.com"),
            ).strip()
            draft["base_url"] = base_url
            try:
                operations.create_provider(data, provider_id, base_url)
            except operations.ConfigOperationError as exc:
                return section_panel(f"[red]{exc}[/red]", "添加失败", "red")
        else:
            provider_id = existing[int(provider_choice) - 1]
    elif provider_id not in providers:
        return section_panel(f"[red]供应商不存在: {provider_id}[/red]", "添加失败", "red")
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
    operations.create_provider_key(
        data,
        provider_id,
        key_name,
        api_key,
        enabled=True,
    )
    # 每个 Key 单独探测：不同 Key 能访问的模型集合可能不同（如免费/付费
    # 额度、不同订阅），探测结果按 Key 缓存，互不复用。
    with console.status(
        f"[cyan]正在探测 Key {provider_id}/{key_name} 的可用能力...[/cyan]",
        spinner="dots",
    ):
        probe = probe_key_capability(provider, key_name)
    provider["keys"][key_name]["capabilities"] = probe
    models = probe.get("models") or []
    errors = probe.get("errors") or {}
    if not models and errors:
        show_result_page(
            "添加 Key",
            section_panel(
                "探测失败，仍将保存该 Key；可稍后在供应商菜单手动刷新探测。\n错误: "
                + "; ".join(str(v) for v in errors.values()),
                "探测失败",
                "yellow",
            ),
        )
    probe_failed = not models and bool(errors)
    selected_models = select_models_to_serve(
        provider_id, key_name, models or []
    ) if not probe_failed else []
    if selected_models is None:
        # 用户在模型多选界面取消：保存 Key（不绑定模型）后返回。
        RouterConfig.from_dict(data)
        restart = commit_v2_config(path, data, old_config)
        FORM_DRAFTS.pop("add_provider_key", None)
        return Group(
            section_panel(
                f"供应商: [bold]{provider_id}[/bold]\nKey: [bold]{key_name}[/bold]\n"
                "已保存 Key，未绑定任何模型。可在模型设置中绑定。",
                "Key 已保存",
                "green",
            ),
            restart,
        )
    bound = 0
    for upstream_model in selected_models:
        local_models = raw_v2_models(data)
        if upstream_model not in local_models:
            operations.create_model(data, upstream_model)
        try:
            operations.add_model_target(
                data,
                upstream_model,
                {
                    "provider": provider_id,
                    "key": key_name,
                    "upstream_model": upstream_model,
                },
            )
            bound += 1
        except operations.ConfigOperationError:
            continue
    RouterConfig.from_dict(data)
    restart = commit_v2_config(path, data, old_config)
    FORM_DRAFTS.pop("add_provider_key", None)
    return Group(
        section_panel(
            f"供应商: [bold]{provider_id}[/bold]\nKey: [bold]{key_name}[/bold]\n"
            f"上游: [bold]{compact_url(str(provider.get('base_url') or '-'), 56)}[/bold]\n"
            f"已绑定模型: [bold]{bound}[/bold]",
            "添加完成",
            "green",
        ),
        restart,
    )


def manage_provider_keys_interactively(
    path: Path, provider_id: str | None = None
) -> None:
    while True:
        data = load_v2_config_data(path)
        if provider_id is not None and provider_id not in raw_providers(data):
            return
        selected = select_provider_key(data, "选择供应商 Key", provider_id)
        if selected is None:
            return
        provider_id, key_name = selected
        provider = raw_providers(data)[provider_id]
        key = provider_keys(provider)[key_name]
        visitor_installed = visitor_feature_available()
        options = [
            ("1", "开关"),
            ("2", "重命名"),
        ]
        if visitor_installed:
            options.append(("3", "访客访问"))
        options.extend([("4", "删除"), ("0", "返回")])
        bound_models = [
            model_id
            for model_id, model in raw_v2_models(data).items()
            for target in model_targets(model)
            if target.get("provider") == provider_id and target.get("key") == key_name
        ]
        lines = [
            f"供应商: [bold]{provider_id}[/bold]",
            f"Key: [bold]{key_name}[/bold]",
            f"状态: [bold]{'启用' if key.get('enabled', True) else '禁用'}[/bold]",
            f"服务模型: [bold]{', '.join(bound_models) or '未绑定模型'}[/bold]",
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
    provider = operations.require_provider(data, provider_id)
    key = operations.require_key(provider, key_name)
    if choice == "1":
        enabled = not bool(key.get("enabled", True))
        operations.update_provider_key(
            data, provider_id, key_name, enabled=enabled
        )
        message = f"已{'启用' if enabled else '禁用'} {provider_id}/{key_name}。"
    elif choice == "2":
        new_name = prompt_text("重命名 Key", "新名称", default=key_name).strip()
        if not new_name or new_name == key_name:
            return section_panel("[yellow]配置未变化。[/yellow]", "重命名 Key", "yellow")
        try:
            operations.update_provider_key(
                data, provider_id, key_name, new_name=new_name
            )
        except operations.ConfigOperationError as exc:
            return section_panel(f"[red]{exc}[/red]", "重命名 Key", "red")
        message = f"已重命名 {provider_id}/{key_name} → {new_name}。"
    elif choice == "3" and visitor_feature_available():
        allowed = not bool(key.get("allow_visitor", False))
        operations.update_provider_key(
            data, provider_id, key_name, allow_visitor=allowed
        )
        message = f"已{'允许' if allowed else '禁止'}访客访问 {provider_id}/{key_name}。"
    elif choice == "4":
        used_by = [
            model_id
            for model_id, model in raw_v2_models(data).items()
            for target in model_targets(model)
            if target.get("provider") == provider_id and target.get("key") == key_name
        ]
        if used_by and not confirm_choice(
            f"该 Key 被 {len(used_by)} 个模型使用，删除会一并移除这些绑定。继续？",
            default=False,
        ):
            return section_panel("[yellow]配置未变化。[/yellow]", "删除 Key", "yellow")
        removed_models = operations.delete_provider_key(data, provider_id, key_name)
        suffix = f"\n已移除空模型: [bold]{len(removed_models)}[/bold]" if removed_models else ""
        message = f"已删除 {provider_id}/{key_name}。{suffix}"
    else:
        return None
    restart = commit_v2_config(path, data, old_config)
    return Group(section_panel(message, "供应商 Key", "green"), restart)


def model_key_targets_panel(data: dict[str, Any], model_id: str) -> Any:
    model = raw_v2_models(data).get(model_id)
    targets = model_targets(model) if model else []
    rows = []
    for index, target in enumerate(targets):
        provider_id = str(target.get("provider") or "")
        key_name = str(target.get("key") or "")
        upstream = str(target.get("upstream_model") or model_id)
        provider = raw_providers(data).get(provider_id, {})
        key = provider_keys(provider).get(key_name) if isinstance(provider, dict) else None
        status = "启用" if isinstance(key, dict) and key.get("enabled", True) else "禁用"
        rows.append(
            f"{index + 1}. [bold]{provider_id}[/bold]/{key_name} → [bold]{upstream}[/bold] ({status})"
        )
    if not rows:
        rows.append("[yellow]该模型尚未绑定任何 Key[/yellow]")
    return section_panel("\n".join(rows), f"模型 Key · {short_text(model_id, 32)}", "cyan")


def add_model_route_interactively(path: Path, model_id: str | None = None) -> Any:
    """Bind an existing provider Key to a model (add a target)."""
    draft = form_draft("add_model_route")
    data = load_v2_config_data(path)
    if not raw_providers(data):
        return section_panel("[yellow]请先添加供应商 Key。[/yellow]", "添加模型 Key", "yellow")
    old_config = RouterConfig.from_dict(data)
    models = raw_v2_models(data)
    if model_id is None:
        model_id = select_or_enter_model_id(
            "添加模型 Key",
            "本地模型 ID",
            models,
            default=draft.get("model_id", ""),
        )
        draft["model_id"] = model_id
    if not model_id:
        return section_panel("[red]模型 ID 不能为空[/red]", "添加模型 Key", "red")
    if model_id not in models:
        operations.create_model(data, model_id)
    provider_id = select_provider(data, "选择供应商")
    if provider_id is None:
        return None
    provider = raw_providers(data)[provider_id]
    keys = provider_keys(provider)
    if not keys:
        return section_panel("[yellow]该供应商暂无 Key。[/yellow]", "添加模型 Key", "yellow")
    key_options = [
        (key_name, f"{key_name} · {key_fingerprint(str(key.get('api_key') or ''))}")
        for key_name, key in sorted(keys.items())
    ]
    key_choice = select_option(
        "选择 Key",
        [(name, label) for name, label in key_options] + [("0", "返回")],
    )
    if key_choice == "0":
        return None
    key_name = key_choice
    key = keys[key_name]
    key_capabilities = key.get("capabilities") if isinstance(key, dict) else None
    key_models = (
        key_capabilities.get("models")
        if isinstance(key_capabilities, dict)
        and key_capabilities.get("models")
        else []
    )
    default_upstream = str(
        draft.get("upstream_model", "") or key_models[0] if key_models else model_id
    )
    upstream_model = prompt_text(
        "添加模型 Key",
        "上游模型 ID（默认同本地模型）",
        default=default_upstream if default_upstream != model_id else "",
    ).strip() or model_id
    try:
        operations.add_model_target(
            data,
            model_id,
            {
                "provider": provider_id,
                "key": key_name,
                "upstream_model": upstream_model,
            },
        )
    except operations.ConfigOperationError as exc:
        return section_panel(f"[yellow]{exc}[/yellow]", "添加模型 Key", "yellow")
    restart = commit_v2_config(path, data, old_config)
    FORM_DRAFTS.pop("add_model_route", None)
    return Group(
        section_panel(
            f"本地模型: [bold]{model_id}[/bold]\n供应商 Key: [bold]{provider_id}/{key_name}[/bold]\n上游模型: [bold]{upstream_model}[/bold]",
            "添加完成",
            "green",
        ),
        restart,
    )


def manage_model_routes_interactively(path: Path, selected_model_id: str | None = None) -> None:
    """Manage which provider Keys are bound to a model."""
    while True:
        data = load_v2_config_data(path)
        model_id = selected_model_id or select_v2_model(data, "选择模型 Key")
        if model_id is None:
            return
        model = raw_v2_models(data)[model_id]
        targets = model_targets(model)
        options = [("a", "绑定 Key"), ("d", "解绑 Key")]
        if targets:
            options.append(("u", "改上游模型"))
        options.append(("0", "返回"))
        choice = select_option(
            f"模型 Key · {short_text(model_id, 28)}",
            options,
            content=model_key_targets_panel(data, model_id),
        )
        if choice == "0":
            if selected_model_id is not None:
                return
            continue
        clear_terminal_history()
        if choice == "a":
            result = add_model_route_interactively(path, model_id)
            if result is not None:
                show_result_page("模型 Key", result)
        elif choice == "u" and targets:
            result = update_model_target_upstream_interactively(path, model_id)
            if result is not None:
                show_result_page("模型 Key", result)
        elif choice == "d":
            result = delete_model_key_binding_interactively(path, model_id)
            if result is not None:
                show_result_page("模型 Key", result)


def update_model_target_upstream_interactively(path: Path, model_id: str) -> Any:
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    model = raw_v2_models(data)[model_id]
    targets = model_targets(model)
    options = [
        (
            str(index + 1),
            f"{target.get('provider')}/{target.get('key')} → {target.get('upstream_model') or model_id}",
        )
        for index, target in enumerate(targets)
    ] + [("0", "返回")]
    choice = select_option(f"选择 Key · {short_text(model_id, 28)}", options)
    if choice == "0":
        return None
    target_index = int(choice) - 1
    target = targets[target_index]
    current = str(target.get("upstream_model") or model_id)
    upstream_model = prompt_text(
        "改上游模型", "上游模型 ID", default=current
    ).strip()
    if not upstream_model or upstream_model == current:
        return section_panel("[yellow]配置未变化。[/yellow]", "模型 Key", "yellow")
    operations.update_model_target(data, model_id, target_index, upstream_model)
    restart = commit_v2_config(path, data, old_config)
    return Group(
        section_panel(
            f"已更新 {model_id} 的上游模型: {current} → {upstream_model}。",
            "模型 Key",
            "green",
        ),
        restart,
    )


def delete_model_key_binding_interactively(path: Path, model_id: str) -> Any:
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    model = raw_v2_models(data)[model_id]
    targets = model_targets(model)
    options = [
        (
            str(index + 1),
            f"{target.get('provider')}/{target.get('key')} → {target.get('upstream_model') or model_id}",
        )
        for index, target in enumerate(targets)
    ] + [("0", "返回")]
    choice = select_option(f"解绑 Key · {short_text(model_id, 28)}", options)
    if choice == "0":
        return None
    target_index = int(choice) - 1
    target = targets[target_index]
    if len(targets) == 1 and not confirm_choice(
        "这是该模型最后一个 Key，解绑后模型将不可用。继续？", default=False
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "模型 Key", "yellow")
    removed = operations.delete_model_target(data, model_id, target_index)
    restart = commit_v2_config(path, data, old_config)
    return Group(
        section_panel(
            f"已解绑 Key: {removed.get('provider')}/{removed.get('key')}。",
            "模型 Key",
            "green",
        ),
        restart,
    )


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
                ("3", "管理 Key"),
                ("4", "绑定 Key"),
                ("5", "删除模型"),
                ("0", "返回"),
            ],
            content=model_key_targets_panel(data, model_id),
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
        operations.update_model(
            data,
            model_id,
            aliases=[alias.strip() for alias in aliases_text.split(",") if alias.strip()],
        )
        message = f"已更新 {model_id} 的别名。"
    elif choice == "2":
        current = str(model.get("routing_mode") or "round_robin")
        routing_mode = prompt_text(
            "路由模式",
            "路由模式",
            choices=["priority", "round_robin", "only_first"],
            default=current,
        ).strip()
        operations.update_model(
            data, model_id, routing_mode=routing_mode
        )
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
    operations.delete_model(data, model_id)
    restart = commit_v2_config(path, data, old_config)
    return Group(section_panel(f"已删除模型 {model_id}。", "模型设置", "green"), restart)


def manage_provider_routes_interactively(
    path: Path, provider_id: str | None = None
) -> None:
    while True:
        data = load_v2_config_data(path)
        if provider_id is None:
            selected_provider_id = select_provider(data, "选择供应商路径")
            if selected_provider_id is None:
                return
        else:
            selected_provider_id = provider_id
            if selected_provider_id not in raw_providers(data):
                return
        provider = raw_providers(data)[selected_provider_id]
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
            f"供应商路径 · {selected_provider_id}",
            mode_options,
            content=section_panel(route_rows, "当前路径", "cyan"),
        )
        if choice == "0":
            if provider_id is not None:
                return
            continue
        clear_terminal_history()
        result = update_provider_routes_interactively(path, selected_provider_id, choice)
        if result is not None:
            show_result_page("供应商路径", result)


def update_provider_routes_interactively(path: Path, provider_id: str, choice: str) -> Any:
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    provider = raw_providers(data)[provider_id]
    routes = provider.setdefault("routes", {})
    if choice == "c":
        operations.update_provider(
            data, provider_id, routes={}, update_routes=True
        )
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
        operations.update_provider(
            data, provider_id, routes=routes, update_routes=True
        )
    restart = commit_v2_config(path, data, old_config)
    return Group(section_panel(message, "供应商路径", "green"), restart)


def delete_provider_interactively(path: Path, provider_id: str) -> Any:
    data = load_v2_config_data(path)
    if provider_id not in raw_providers(data):
        return section_panel(f"[red]供应商不存在: {provider_id}[/red]", "删除供应商", "red")
    if not confirm_choice(f"确认删除供应商 {provider_id}？", default=False):
        return section_panel("[yellow]配置未变化。[/yellow]", "删除供应商", "yellow")

    old_config = RouterConfig.from_dict(data)
    removed_models = operations.delete_provider(data, provider_id)
    restart = commit_v2_config(path, data, old_config)
    return Group(
        section_panel(
            f"已删除供应商 {provider_id}。\n已移除空模型: [bold]{len(removed_models)}[/bold]",
            "删除供应商",
            "green",
        ),
        restart,
    )


def refresh_provider_capability_interactively(path: Path, provider_id: str) -> Any:
    """Manually re-probe one or all keys of a provider.

    Probes are per key (each key may see a different model list), and for a
    single key the user may refresh all route modes at once or check one
    endpoint mode specifically (openai / anthropic / responses).
    """
    data = load_v2_config_data(path)
    old_config = RouterConfig.from_dict(data)
    provider = raw_providers(data)[provider_id]
    keys = provider_keys(provider)
    if not keys:
        return section_panel("[yellow]该供应商暂无 Key，无法探测。[/yellow]", "刷新能力", "yellow")
    scope_choice = select_option(
        "刷新能力探测",
        [("1", "刷新全部 Key"), ("2", "指定 Key")] + [("0", "返回")],
        content=provider_capabilities_panel(provider),
    )
    if scope_choice == "0":
        return None
    key_names = sorted(keys)
    modes: list[str] | None = None
    if scope_choice == "2":
        key_choice = select_option(
            "选择要刷新的 Key",
            [(key_name, key_name) for key_name in key_names] + [("0", "返回")],
        )
        if key_choice == "0":
            return None
        key_names = [key_choice]
        mode_choice = select_option(
            "端点检查范围",
            [("1", "全部路由模式"), ("2", "仅 Chat (openai)"), ("3", "仅 Messages (anthropic)"), ("4", "仅 Responses")],
        )
        if mode_choice == "0":
            return None
        modes = {
            "1": None,
            "2": ["openai"],
            "3": ["anthropic"],
            "4": ["responses"],
        }[mode_choice]
    refreshed: list[str] = []
    with console.status(
        f"[cyan]正在探测 {provider_id} 的 {len(key_names)} 个 Key...[/cyan]",
        spinner="dots",
    ):
        for key_name in key_names:
            probe = probe_key_capability(
                provider, key_name, modes=modes
            )
            if isinstance(keys[key_name], dict):
                keys[key_name]["capabilities"] = probe
            refreshed.append(key_name)
    restart = commit_v2_config(path, data, old_config)
    message_lines = [f"已刷新 Key: [bold]{', '.join(refreshed)}[/bold]"]
    for key_name in refreshed:
        probe = keys[key_name]["capabilities"] if isinstance(keys[key_name], dict) else None
        if not isinstance(probe, dict):
            continue
        models = probe.get("models") or []
        errors = probe.get("errors") or {}
        route_status = probe.get("route_status") or {}
        lines = [f"[bold]{key_name}[/bold]: {len(models)} 个模型"]
        if route_status:
            lines.append(
                "路由: "
                + " · ".join(
                    f"{UPSTREAM_ROUTE_LABELS.get(mode, mode)}: [{'green' if str(status) == 'ok' else 'red'}]{status}[/{'green' if str(status) == 'ok' else 'red'}]"
                    for mode, status in sorted(route_status.items())
                )
            )
        if errors:
            lines.append("[red]错误: " + "; ".join(str(v) for v in errors.values()) + "[/red]")
        message_lines.append("\n".join(lines))
    return Group(
        section_panel("\n".join(message_lines), "刷新完成", "green"),
        restart,
    )


def manage_providers_interactively(path: Path) -> None:
    while True:
        data = load_v2_config_data(path)
        providers = raw_providers(data)
        provider_ids = sorted(providers)
        choice = select_option(
            "供应商",
            [("n", "添加供应商")]
            + [
                (
                    str(index + 1),
                    f"{short_text(provider_id, 22)} · {compact_url(str(providers[provider_id].get('base_url') or '-'), 42)} · {len(provider_keys(providers[provider_id]))} Key",
                )
                for index, provider_id in enumerate(provider_ids)
            ]
            + [("0", "返回")],
            content=v2_summary_panel(data),
        )
        if choice == "0":
            return
        if choice == "n":
            result = run_submodule(
                lambda: add_provider_key_interactively(path, create_provider=True)
            )
            if result is not None:
                show_result_page("添加供应商", result)
            continue

        provider_id = provider_ids[int(choice) - 1]
        while provider_id in raw_providers(load_v2_config_data(path)):
            choice = select_option(
                f"供应商 · {provider_id}",
                [
                    ("1", "添加 Key"),
                    ("2", "管理 Key"),
                    ("3", "刷新能力探测"),
                    ("4", "Base URL / 路由设置"),
                    ("5", "删除供应商"),
                    ("0", "返回"),
                ],
                content=provider_capabilities_panel(
                    raw_providers(load_v2_config_data(path))[provider_id]
                ),
            )
            if choice == "0":
                break
            if choice == "1":
                result = run_submodule(
                    lambda: add_provider_key_interactively(path, provider_id)
                )
                if result is not None:
                    show_result_page("添加供应商 Key", result)
            elif choice == "2":
                run_submodule(
                    lambda: manage_provider_keys_interactively(path, provider_id)
                )
            elif choice == "3":
                result = refresh_provider_capability_interactively(path, provider_id)
                if result is not None:
                    show_result_page("刷新能力", result)
            elif choice == "4":
                run_submodule(
                    lambda: manage_provider_routes_interactively(path, provider_id)
                )
            elif choice == "5":
                result = delete_provider_interactively(path, provider_id)
                if result is not None:
                    show_result_page("删除供应商", result)
                break






































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
    return operations.transferable_config(
        data, include_visitor=include_visitor
    )


def merge_transferable_key_config(
    current_data: dict[str, Any], transfer_data: dict[str, Any]
) -> tuple[dict[str, Any], int, int, int]:
    return operations.merge_transferable_config(current_data, transfer_data)


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
    modes: list[str] | None = None,
) -> list[KeyProbeResult]:
    key_name = str(key.get("name") or f"{model_id}-key")
    api_key = str(key.get("api_key") or "")
    base_url = str(
        key.get("base_url") or data.get("default_base_url") or "https://api.openai.com"
    )
    routes = upstream_routes_for_base_url(data, base_url)
    probe_modes = [mode for mode in (modes or list(PROBE_ROUTE_MODES)) if mode in PROBE_ROUTE_MODES]
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
            for mode in probe_modes
        ]

    with httpx.Client(timeout=timeout) as client:
        for mode in probe_modes:
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














































def set_local_api_key_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    if data.get("local_api_key") and not confirm_choice(
        "是否重置本地鉴权密钥？", default=True
    ):
        return section_panel("[yellow]配置未变化。[/yellow]", "本地鉴权", "yellow")
    local_api_key = generate_local_api_key()
    operations.regenerate_local_api_key(data, local_api_key)
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

    operations.update_settings(data, **values)
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
    operations.update_settings(data, host=host, port=port)
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






