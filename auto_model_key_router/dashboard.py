from __future__ import annotations

import sys
from pathlib import Path
from typing import Any

from rich import box
from rich.console import Group
from rich.live import Live
from rich.table import Table

from .agent_config import (
    CLAUDE_CODE,
    CODEX,
    AgentConfigError,
    agent_display_name,
    configure_agent,
    get_agent_config_status,
    rollback_agent,
    router_origin,
)
from .config import (
    UNIFIED_MODEL_ID,
    RouterConfig,
    generate_local_api_key,
    load_config_data,
)
from .config_service import commit_config_data
from .config_editor import (
    manage_config_transfer_interactively,
    manage_model_keys_interactively,
    reasoning_effort_text,
    set_listen_interactively,
    set_local_api_key_interactively,
)
from .formatting import compact_url, short_text
from . import __version__
from .logs_tui import watch_logs
from .service import (
    is_service_healthy,
    is_system_service_registered,
    manage_system_service,
    service_status_panel,
    system_service_status_panel,
)
from .tui import (
    ResultPage,
    app_flag_title,
    clear_terminal_history,
    confirm_choice,
    console,
    content_scroll_offset,
    menu_table,
    mouse_wheel_mode,
    open_config_file,
    posix_input_mode,
    read_key_responsive,
    run_submodule,
    section_panel,
    select_option,
    shortcut_text,
    should_handle_wheel,
    show_result_page,
    terminal_frame_state,
)
from .unified_model import switch_unified_model
from .update import (
    UpdateInstallOutcome,
    VersionCheckResult,
    check_latest_version,
    install_latest_version_outcome,
    render_update_notice,
    render_version_check_result,
    update_target_label,
)
from .visitor import VISITOR_API_KEY, visitor_feature_available


MENU_OPTIONS = [
    ("1", "一键配置"),
    ("2", "模型 Key"),
    ("3", "统一模型"),
    ("4", "调用日志"),
    ("5", "CLI 设置"),
    ("0", "退出"),
]
ONE_CLICK_OPTIONS = [
    ("1", "路由服务"),
    ("2", "Claude Code"),
    ("3", "Codex"),
    ("0", "返回"),
]
SETTINGS_OPTIONS = [
    ("1", "模型服务"),
    ("2", "本地鉴权"),
    ("3", "监听配置"),
    ("4", "配置迁移"),
    ("5", "版本更新"),
    ("0", "返回"),
]


def run_terminal_ui(config_path: Path, config: RouterConfig) -> None:
    selected = 0
    update_result = check_latest_version(timeout=1.2)
    while True:
        choice, selected = select_menu_option(
            config_path, config, selected, update_result
        )
        if choice == "0":
            return
        if choice == "1":
            run_submodule(lambda: manage_one_click_config_interactively(config_path))
            config = RouterConfig.load(config_path)
            continue
        if choice == "2":
            run_submodule(lambda: manage_model_keys_interactively(config_path))
            config = RouterConfig.load(config_path)
            continue
        if choice == "3":
            run_submodule(lambda: manage_unified_model_interactively(config_path))
            config = RouterConfig.load(config_path)
            continue
        if choice == "4":
            config = RouterConfig.load(config_path)
            run_submodule(
                lambda: watch_logs(config.metrics_db_path, config.log_file_path, 20)
            )
            config = RouterConfig.load(config_path)
            continue
        if choice == "5":
            result = run_submodule(
                lambda: manage_cli_settings_interactively(config_path, update_result)
            )
            if isinstance(result, UpdateInstallOutcome):
                if result.handoff:
                    return
                show_result_page("手动更新", result.content)
                if result.updated:
                    return
                continue
            if isinstance(result, VersionCheckResult):
                update_result = result
            config = RouterConfig.load(config_path)


def _open_config_on_key(config_path: Path, key: str) -> str | None:
    if key in {"o", "O"}:
        open_config_file(config_path)
    return None


def manage_unified_model_interactively(config_path: Path) -> None:
    on_key = lambda key: _open_config_on_key(config_path, key)
    while True:
        config = RouterConfig.load(config_path)
        choice = select_option(
            "统一模型",
            [("1", "切换模型和 Key"), ("2", "仅切换 Key"), ("0", "返回")],
            content=unified_model_status_panel(config),
            on_key=on_key,
        )
        if choice == "0":
            return
        result = run_submodule(
            lambda: switch_unified_model_interactively(
                config_path, choose_model=choice == "1"
            )
        )
        if result is not None:
            show_result_page("统一模型", result)


def switch_unified_model_interactively(config_path: Path, *, choose_model: bool) -> Any:
    config = RouterConfig.load(config_path)
    selectable_models = [
        model for model in config.models if any(key.enabled for key in model.keys)
    ]
    if not selectable_models:
        return section_panel(
            "[yellow]还没有包含已启用 Key 的模型配置。[/yellow]", "统一模型", "yellow"
        )

    current = config.unified_model
    if choose_model or current is None:
        model_options = []
        for index, model in enumerate(selectable_models):
            aliases = (
                f" · {short_text(', '.join(model.aliases), 24)}"
                if model.aliases
                else ""
            )
            model_options.append(
                (str(index + 1), f"{short_text(model.id, 28)}{aliases}")
            )
        model_options.append(("0", "返回"))
        current_model_id = (
            config.configured_model_id(current.model) if current is not None else None
        )
        selected_model = next(
            (
                index
                for index, model in enumerate(selectable_models)
                if model.id == current_model_id
            ),
            0,
        )
        model_choice = select_option(
            "选择统一模型", model_options, selected=selected_model
        )
        if model_choice == "0":
            return None
        target_model = selectable_models[int(model_choice) - 1]
    else:
        target_model_id = config.configured_model_id(current.model)
        target_model = next(
            (model for model in selectable_models if model.id == target_model_id), None
        )
        if target_model is None:
            return section_panel(
                "[red]当前统一模型目标不存在或没有已启用 Key，请重新选择模型。[/red]",
                "统一模型",
                "red",
            )

    enabled_keys = [key for key in target_model.keys if key.enabled]
    key_options = [("a", "自动路由")]
    for index, key in enumerate(enabled_keys):
        key_options.append(
            (
                str(index + 1),
                f"{short_text(key.name, 28)} · {compact_url(key.base_url, 32)}",
            )
        )
    key_options.append(("0", "返回"))
    current_key = (
        current.key
        if current is not None
        and target_model.id == config.configured_model_id(current.model)
        else None
    )
    selected_key = next(
        (
            index + 1
            for index, key in enumerate(enabled_keys)
            if key.name == current_key
        ),
        0,
    )
    key_choice = select_option("选择统一模型 Key", key_options, selected=selected_key)
    if key_choice == "0":
        return None
    key_name = None if key_choice == "a" else enabled_keys[int(key_choice) - 1].name

    updated = switch_unified_model(
        config_path, target_model.id, key_name, update_key=True
    )
    return unified_model_status_panel(updated, title="统一模型已切换", color="green")


def unified_model_status_panel(
    config: RouterConfig, title: str = "当前路由", color: str = "cyan"
) -> Any:
    if config.unified_model is None:
        content = (
            f"[yellow]尚未配置 {UNIFIED_MODEL_ID}。[/yellow]\n请选择已有模型和 Key。"
        )
    else:
        content = (
            f"请求模型: [bold cyan]{UNIFIED_MODEL_ID}[/bold cyan]\n"
            f"目标模型: [bold]{config.unified_model.model}[/bold]\n"
            f"使用 Key: [bold green]{config.unified_model.key or '自动路由'}[/bold green]"
        )
    return section_panel(content, title, color)


def manage_cli_settings_interactively(
    config_path: Path, update_result: VersionCheckResult | None = None
) -> VersionCheckResult | UpdateInstallOutcome | None:
    latest_result = update_result
    on_key = lambda key: _open_config_on_key(config_path, key)
    while True:
        choice = select_option("CLI 设置", SETTINGS_OPTIONS, on_key=on_key)
        if choice == "0":
            return latest_result
        if choice == "1":
            run_submodule(lambda: manage_system_service_interactively(config_path))
            continue
        if choice == "2":
            result = run_submodule(lambda: set_local_api_key_interactively(config_path))
            if result is not None:
                show_result_page("本地鉴权", result)
            continue
        if choice == "3":
            result = run_submodule(lambda: set_listen_interactively(config_path))
            if result is not None:
                show_result_page("监听配置", result)
            continue
        if choice == "4":
            run_submodule(lambda: manage_config_transfer_interactively(config_path))
            continue
        if choice == "5":
            result = run_submodule(
                lambda: manage_version_update_interactively(config_path, latest_result)
            )
            if isinstance(result, UpdateInstallOutcome):
                return result
            if isinstance(result, VersionCheckResult):
                latest_result = result


def manage_one_click_config_interactively(config_path: Path) -> None:
    on_key = lambda key: _open_config_on_key(config_path, key)
    while True:
        choice = select_option("一键配置", ONE_CLICK_OPTIONS, on_key=on_key)
        if choice == "0":
            return
        if choice == "1":
            result = run_submodule(lambda: configure_cli_interactively(config_path))
            if result is not None:
                show_result_page("路由服务", result)
            continue
        agent = CLAUDE_CODE if choice == "2" else CODEX
        run_submodule(lambda: manage_agent_config_interactively(config_path, agent))


def manage_agent_config_interactively(config_path: Path, agent: str) -> None:
    name = agent_display_name(agent)
    while True:
        config = RouterConfig.load(config_path)
        status = get_agent_config_status(agent)
        rollback_label = (
            "回退原配置" if status.backup_available else "回退原配置（无备份）"
        )
        choice = select_option(
            f"{name} 一键配置",
            [("1", "应用路由配置"), ("2", rollback_label), ("0", "返回")],
            content=agent_config_status_panel(config, agent),
        )
        if choice == "0":
            return
        if choice == "1":
            result = configure_agent_interactively(config_path, agent)
            show_result_page(f"{name} 配置", result)
            continue
        if not status.backup_available:
            show_result_page(
                f"{name} 回退",
                section_panel(
                    "当前没有可用于回退的配置备份。", f"{name} 回退", "yellow"
                ),
            )
            continue
        if not confirm_choice(f"将使用缓存内容覆盖 {status.target_path}，是否继续？"):
            continue
        try:
            restored = rollback_agent(agent)
        except (OSError, AgentConfigError) as exc:
            result = section_panel(f"[red]{exc}[/red]", f"{name} 回退失败", "red")
        else:
            result = section_panel(
                f"已恢复应用路由配置前的内容。\n配置文件: [bold]{restored.target_path}[/bold]",
                f"{name} 已回退",
                "green",
            )
        show_result_page(f"{name} 回退", result)


def configure_agent_interactively(config_path: Path, agent: str) -> Any:
    name = agent_display_name(agent)
    try:
        config = RouterConfig.load(config_path)
        result = configure_agent(agent, config)
    except (OSError, AgentConfigError) as exc:
        return section_panel(f"[red]{exc}[/red]", f"{name} 配置失败", "red")

    service_status = (
        "[green]路由服务正在运行。[/green]"
        if is_service_healthy(config.host, config.port, use_cache=False)
        else "[yellow]路由服务当前未运行，请先在“一键配置 → 路由服务”完成服务配置。[/yellow]"
    )
    return Group(
        section_panel(
            f"已将 {name} 指向本项目路由。\n"
            f"配置文件: [bold]{result.target_path}[/bold]\n"
            f"路由地址: [bold]{result.router_url}[/bold]\n"
            f"请求模型: [bold cyan]{UNIFIED_MODEL_ID}[/bold cyan]\n"
            f"原配置缓存: [bold]{result.backup_path}[/bold]\n\n"
            f"{service_status}",
            f"{name} 配置完成",
            "green",
        )
    )


def agent_config_status_panel(config: RouterConfig, agent: str) -> Any:
    name = agent_display_name(agent)
    status = get_agent_config_status(agent)
    if status.current_is_applied:
        applied_status = "[green]已应用当前路由配置[/green]"
    elif status.backup_available:
        applied_status = "[yellow]配置已变化，仍可回退到应用前内容[/yellow]"
    else:
        applied_status = "[dim]尚未由本项目配置[/dim]"
    unified_status = (
        f"[green]{UNIFIED_MODEL_ID} → {config.unified_model.model}[/green]"
        if config.unified_model is not None
        else f"[yellow]未配置 {UNIFIED_MODEL_ID}，暂时不能应用[/yellow]"
    )
    route_url = router_origin(config)
    if agent == CODEX:
        route_url += "/v1"
    return section_panel(
        f"配置文件: [bold]{status.target_path}[/bold]\n"
        f"路由地址: [bold]{route_url}[/bold]\n"
        f"统一模型: {unified_status}\n"
        f"当前状态: {applied_status}\n"
        f"备份文件: [bold]{status.backup_path}[/bold]",
        f"{name} 状态",
        "cyan",
    )


def configure_cli_interactively(config_path: Path) -> Any:
    config_exists = config_path.exists()
    data = load_config_data(config_path)
    old_config = RouterConfig.from_dict(data)
    local_api_key = str(data.get("local_api_key") or "").strip()
    if not local_api_key:
        local_api_key = generate_local_api_key()
        data["local_api_key"] = local_api_key
        config = commit_config_data(config_path, data, old_config).new_config
        auth_message = "已生成本地鉴权 key。"
    else:
        config = old_config
        auth_message = (
            "已使用现有本地鉴权 key。" if config_exists else "已生成本地鉴权 key。"
        )
    auth_panel = section_panel(
        f"{auth_message}\n\n[bold]{local_api_key}[/bold]\n\n请求时添加：\nAuthorization: Bearer {local_api_key}\n或：\nx-api-key: {local_api_key}",
        "本地鉴权",
        "green",
    )
    endpoint_panel = section_panel(
        f"配置文件: [bold]{config_path.resolve()}[/bold]\n服务地址: [bold]http://{config.host}:{config.port}[/bold]",
        "CLI 设置",
        "green",
    )
    return ResultPage(
        Group(
            auth_panel, manage_system_service(config_path, "install"), endpoint_panel
        ),
        copy_text=local_api_key,
        copy_label="复制本地鉴权 key",
    )


def select_menu_option(
    config_path: Path,
    config: RouterConfig,
    selected: int = 0,
    update_result: VersionCheckResult | None = None,
) -> tuple[str, int]:
    frame_offset = 0
    frame_state = render_terminal_ui_state(
        config_path, config, selected, update_result, frame_offset
    )
    last_wheel_key: str | None = None
    last_wheel_at = 0.0
    status_message: str | None = None

    def refresh(*, ensure_selected_visible: bool) -> None:
        nonlocal frame_offset, frame_state
        frame_state = render_terminal_ui_state(
            config_path,
            config,
            selected,
            update_result,
            frame_offset,
            ensure_selected_visible=ensure_selected_visible,
            status_message=status_message,
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
                return "0", next(
                    index
                    for index, option in enumerate(MENU_OPTIONS)
                    if option[0] == "0"
                )
            if key in {"o", "O"}:
                status_message = open_config_file(config_path)
                refresh(ensure_selected_visible=False)
                continue
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
                selected = (selected - 1) % len(MENU_OPTIONS)
                refresh(ensure_selected_visible=True)
                continue
            if key in {"down", "scroll_down"}:
                selected = (selected + 1) % len(MENU_OPTIONS)
                refresh(ensure_selected_visible=True)
                continue
            if key == "enter":
                return MENU_OPTIONS[selected][0], selected
            if key in {option[0] for option in MENU_OPTIONS}:
                return key, next(
                    index
                    for index, option in enumerate(MENU_OPTIONS)
                    if option[0] == key
                )


def render_terminal_ui_state(
    config_path: Path,
    config: RouterConfig,
    selected: int,
    update_result: VersionCheckResult | None = None,
    frame_offset: int = 0,
    *,
    ensure_selected_visible: bool = True,
    status_message: str | None = None,
) -> Any:
    update_notice = render_update_notice(update_result)
    renderables = [
        app_flag_title(
            "Auto-Model-Key-Router",
            "OpenAI-Compatible 模型 API Key 路由控制台",
            __version__,
        ),
        *config_renderables(config, config_path),
    ]
    if update_notice is not None:
        renderables.append(update_notice)
    if status_message:
        renderables.append(section_panel(status_message, "提示", "green" if status_message.startswith("已") else "yellow"))
    renderables.append(
        section_panel(
            menu_table(MENU_OPTIONS, selected),
            "主菜单",
            "cyan",
            "[dim]选择要管理的模块[/dim]",
        )
    )
    shortcuts = (
        "↑/↓ 选择  ·  Enter 确认  ·  O 打开配置文件  ·  PgUp/PgDn 查看概览  ·  数字快捷键  ·  Ctrl+C 退出"
    )
    if sys.platform == "win32":
        shortcuts = "↑/↓/滚轮 选择  ·  Enter 确认  ·  O 打开配置文件  ·  PgUp/PgDn 查看概览  ·  数字快捷键  ·  Ctrl+C 退出"
    return terminal_frame_state(
        renderables,
        shortcut_text(shortcuts),
        offset=frame_offset,
        focus_text="▶" if ensure_selected_visible else None,
    )


def render_terminal_ui(
    config_path: Path,
    config: RouterConfig,
    selected: int,
    update_result: VersionCheckResult | None = None,
) -> Any:
    return render_terminal_ui_state(
        config_path, config, selected, update_result
    ).renderable


def manage_system_service_interactively(config_path: Path) -> None:
    on_key = lambda key: _open_config_on_key(config_path, key)
    while True:
        choice = select_option(
            "模型服务",
            [
                ("1", "开机自启"),
                ("2", "启动服务"),
                ("3", "停止服务"),
                ("4", "重启服务"),
                ("5", "服务状态"),
                ("0", "返回"),
            ],
            selected=4,
            on_key=on_key,
        )
        actions = {"2": "start", "3": "stop", "4": "restart", "5": "status"}
        if choice == "0":
            return
        if choice == "1":
            run_submodule(lambda: manage_autostart_interactively(config_path))
            continue
        clear_terminal_history()
        result = (
            service_status_panel(RouterConfig.load(config_path), config_path)
            if choice == "5"
            else manage_system_service(config_path, actions[choice])
        )
        show_result_page("模型服务", result)


def manage_autostart_interactively(config_path: Path) -> None:
    while True:
        registered = is_system_service_registered(config_path)
        options = (
            [("1", "卸载自启"), ("0", "返回")]
            if registered
            else [("1", "安装开机自启"), ("2", "安装登录自启"), ("0", "返回")]
        )
        choice = select_option(
            "开机自启",
            options,
            selected=0,
            content=system_service_status_panel(config_path),
        )
        if choice == "0":
            return
        clear_terminal_history()
        action = (
            "uninstall" if registered else {"1": "install", "2": "install-user"}[choice]
        )
        show_result_page("开机自启", manage_system_service(config_path, action))


def manage_version_update_interactively(
    config_path: Path, update_result: VersionCheckResult | None = None
) -> VersionCheckResult | UpdateInstallOutcome:
    latest_result = (
        update_result
        if update_result is not None and not update_result.error
        else check_latest_version(timeout=10.0)
    )
    while True:
        choice = select_option(
            "版本更新",
            [("1", "重新检查"), ("2", "手动更新"), ("0", "返回")],
            selected=0,
            content=render_version_check_result(latest_result),
        )
        if choice == "0":
            return latest_result
        if choice == "1":
            latest_result = check_latest_version(timeout=10.0)
            continue
        if choice == "2":
            if latest_result.error or not latest_result.update_available:
                show_result_page("版本更新", render_version_check_result(latest_result))
                continue
            if confirm_choice(
                f"将通过 {latest_result.source or '可用来源'} 安装 {update_target_label(latest_result)}。Windows 会交由独立更新器接管并退出当前界面，更新成功后自动重启服务和 Terminal UI。是否继续？"
            ):
                clear_terminal_history()
                return install_latest_version_outcome(
                    latest_result, config_path, restart_tui=True
                )


def render_config(config: RouterConfig, path: Path) -> None:
    for renderable in config_renderables(config, path):
        console.print(renderable)


def config_renderables(config: RouterConfig, path: Path) -> tuple[Any, ...]:
    model_count = len(config.models)
    key_count = sum(len(model.keys) for model in config.models)
    configured_visitor_key_count = sum(
        1
        for model in config.models
        for key in model.keys
        if key.enabled and key.allow_visitor
    )
    visitor_installed = visitor_feature_available()
    upstream_count = len(
        {key.base_url for model in config.models for key in model.keys}
    )
    service_status = (
        "[green]● 运行中[/green]"
        if is_service_healthy(config.host, config.port)
        else "[yellow]● 未运行[/yellow]"
    )
    auth_status = (
        "[green]已启用[/green]" if config.local_api_key else "[yellow]未设置[/yellow]"
    )
    summary = Table.grid(expand=True)
    summary.add_column(ratio=1)
    summary.add_column(ratio=1)
    summary.add_column(ratio=1)
    summary.add_row(
        f"[dim]监听地址[/dim]\n[bold]{config.host}:{config.port}[/bold]",
        f"[dim]服务状态[/dim]\n[bold]{service_status}[/bold]",
        f"[dim]本地鉴权[/dim]\n[bold]{auth_status}[/bold]",
    )
    summary.add_row(
        f"[dim]模型数量[/dim]\n[bold cyan]{model_count}[/bold cyan]",
        f"[dim]Key 数量[/dim]\n[bold green]{key_count}[/bold green]",
        f"[dim]上游数量[/dim]\n[bold magenta]{upstream_count}[/bold magenta]",
    )
    if visitor_installed:
        summary.add_row(
            f"[dim]访客 Key[/dim]\n[bold]{VISITOR_API_KEY}[/bold]",
            f"[dim]访客可用 Key[/dim]\n[bold green]{configured_visitor_key_count}[/bold green]",
            "",
        )
    elif configured_visitor_key_count:
        summary.add_row(
            "[dim]访客功能[/dim]\n[yellow]未安装[/yellow]",
            f"[dim]未生效授权[/dim]\n[yellow]{configured_visitor_key_count}[/yellow]",
            "[dim]安装[/dim]\n[bold]auto-model-key-router[visitor][/bold]",
        )
    warning = (
        section_panel(
            "[bold red]⚠ 当前监听地址为 0.0.0.0，服务会接受所有可达网络的连接。[/bold red]\n[red]如果机器暴露在公网或未受信任网络中，请务必启用本地鉴权、限制防火墙访问，并避免泄露上游 API Key。[/red]",
            "公网开放风险",
            "red",
        )
        if config.host == "0.0.0.0"
        else None
    )
    table = Table(show_lines=False, box=box.SIMPLE_HEAVY, expand=True)
    table.add_column("模型 ID", style="cyan", ratio=2)
    table.add_column("名称")
    table.add_column("路由模式")
    table.add_column("推理强度")
    table.add_column("Keys", justify="right", style="green")
    table.add_column("上游", ratio=2)
    for model in config.models:
        upstreams = sorted({compact_url(key.base_url) for key in model.keys})
        display_names = "\n".join(model.aliases) if model.aliases else "-"
        routing_mode = {
            "round_robin": "分流",
            "priority": "优先级",
            "only_first": "仅首个",
        }.get(model.routing_mode, "分流")
        table.add_row(
            short_text(model.id, 28),
            short_text(display_names, 24),
            routing_mode,
            reasoning_effort_text(model.reasoning_effort),
            str(len(model.keys)),
            "\n".join(upstreams),
        )
    if not config.models:
        table.add_row("未配置", "-", "-", "-", "0", "-")
    renderables = [section_panel(summary, "运行概览", "cyan")]
    if warning is not None:
        renderables.append(warning)
    if config.unified_model is not None:
        key_text = config.unified_model.key or "自动路由"
        renderables.append(
            section_panel(
                f"[bold cyan]{UNIFIED_MODEL_ID}[/bold cyan] → [bold]{config.unified_model.model}[/bold]\nKey: [bold green]{key_text}[/bold green]",
                "统一模型",
                "cyan",
            )
        )
    renderables.append(section_panel(table, "模型路由", "blue"))
    return tuple(renderables)
