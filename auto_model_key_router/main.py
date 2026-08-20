from __future__ import annotations

import argparse
import sys
from dataclasses import replace
from pathlib import Path

from . import __version__
from .config import DEFAULT_CONFIG_PATH, UNIFIED_MODEL_ID, RouterConfig
from .config_editor import repair_duplicate_pool_memberships_interactively
from .dashboard import render_config, run_terminal_ui
from .logs_tui import render_logs
from .service import background_status_panel, manage_system_service, service_status_panel, start_service_background, start_service_foreground, stop_background_service
from .tui import clear_terminal_history, console, section_panel
from .unified_model import switch_unified_model, switch_unified_target
from .update import check_latest_version, render_version_check_result, restart_service_after_update, update_latest_version


def router_address_text(config: RouterConfig) -> str:
    host = config.host
    url_host = host
    if ":" in url_host and not url_host.startswith("["):
        url_host = f"[{url_host}]"
    return (
        f"监听 IP: [bold]{host}[/bold]\n"
        f"监听端口: [bold]{config.port}[/bold]\n"
        f"服务地址: [bold]http://{url_host}:{config.port}[/bold]"
    )


def main() -> None:
    parser = argparse.ArgumentParser(prog=Path(sys.argv[0]).stem)
    parser.add_argument("--config", default=str(DEFAULT_CONFIG_PATH), help="配置文件路径")
    parser.add_argument("--host", help="覆盖配置中的监听地址")
    parser.add_argument("--port", type=int, help="覆盖配置中的监听端口")
    parser.add_argument("--show-config", action="store_true", help="只展示配置摘要，不启动服务")
    parser.add_argument("--show-address", action="store_true", help="查询 AMKR 的监听 IP、端口和服务地址")
    parser.add_argument("--switch-model", metavar="MODEL", help=f"切换 {UNIFIED_MODEL_ID} 指向的已有模型或别名")
    parser.add_argument("--switch-key", metavar="KEY", help=f"切换 {UNIFIED_MODEL_ID} 使用的已有 key；传 auto 恢复自动路由")
    parser.add_argument("--unified-target", choices=["default.primary", "default.fallback", "image.primary", "image.fallback"], default="default.primary", help="选择要修改的 unified 路由目标")
    parser.add_argument("--show-unified-model", action="store_true", help=f"查看 {UNIFIED_MODEL_ID} 当前指向")
    parser.add_argument("--show-logs", nargs="?", const=20, type=int, help="进入调用日志，显示最近 N 行运行日志，调用统计明细固定 10 行/页")
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    parser.add_argument("--check-update", action="store_true", help="通过 PyPI/GitHub 检查最新版本")
    parser.add_argument("--update", action="store_true", help="通过 PyPI/GitHub 手动更新到最新版本")
    parser.add_argument("--serve", action="store_true", help="跳过 Terminal UI，后台启动服务")
    parser.add_argument("--serve-foreground", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--restart-service-after-update", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--stop", action="store_true", help="停止后台服务")
    parser.add_argument("--status", action="store_true", help="查看后台服务状态")
    parser.add_argument("--install-service", action="store_true", help="注册为 Windows/Linux 内置服务")
    parser.add_argument("--service", choices=["install", "install-user", "uninstall", "start", "stop", "restart", "status", "install-elevated", "uninstall-elevated", "start-elevated", "stop-elevated", "restart-elevated"], help="管理 Windows/Linux 内置服务")
    args = parser.parse_args()

    clear_terminal_history()
    if args.check_update:
        console.print(render_version_check_result(check_latest_version(timeout=10.0)))
        return
    config_path = Path(args.config)

    try:
        interactive_tui = not any(
            (
                args.show_config,
                args.show_address,
                args.switch_model is not None,
                args.switch_key is not None,
                args.show_unified_model,
                args.show_logs is not None,
                args.update,
                args.restart_service_after_update,
                args.serve,
                args.serve_foreground,
                args.stop,
                args.status,
                args.install_service,
                args.service is not None,
            )
        )
        if interactive_tui:
            repair_duplicate_pool_memberships_interactively(config_path)
        try:
            config = RouterConfig.load(config_path)
        except Exception as exc:
            console.print(section_panel(f"[red]{exc}[/red]", "配置加载失败", "red"))
            raise SystemExit(1) from exc

        if args.host:
            config = replace(config, host=args.host)
        if args.port:
            config = replace(config, port=args.port)

        if args.switch_model is not None or args.switch_key is not None:
            try:
                config = switch_unified_target(
                    config_path,
                    args.unified_target,
                    args.switch_model,
                    None if args.switch_key == "auto" else args.switch_key,
                    update_key=args.switch_key is not None,
                )
            except (OSError, ValueError) as exc:
                console.print(section_panel(f"[red]{exc}[/red]", "统一模型切换失败", "red"))
                raise SystemExit(1) from exc
            unified = config.unified_model
            key_text = unified.key if unified and unified.key else "自动路由"
            console.print(section_panel(f"请求模型: [bold]{UNIFIED_MODEL_ID}[/bold]\n目标模型: [bold]{unified.model if unified else '-'}[/bold]\n使用 Key: [bold]{key_text}[/bold]", "统一模型已切换", "green"))
            return
        if args.show_unified_model:
            unified = config.unified_model
            if unified is None:
                console.print(section_panel(f"[yellow]尚未配置 {UNIFIED_MODEL_ID}。[/yellow]", "统一模型", "yellow"))
            else:
                console.print(section_panel(f"请求模型: [bold]{UNIFIED_MODEL_ID}[/bold]\n目标模型: [bold]{unified.model}[/bold]\n使用 Key: [bold]{unified.key or '自动路由'}[/bold]", "统一模型", "cyan"))
            return

        if args.update:
            console.print(update_latest_version(timeout=10.0, config_path=config_path))
            return
        if args.restart_service_after_update:
            result = restart_service_after_update(config_path)
            if result is not None:
                console.print(result)
            return

        if args.show_logs is not None:
            render_config(config, config_path)
            render_logs(config.metrics_db_path, config.log_file_path, args.show_logs)
            return
        if args.show_address:
            console.print(section_panel(router_address_text(config), "AMKR 地址", "cyan"))
            return
        if args.show_config:
            render_config(config, config_path)
            return
        if args.stop:
            console.print(stop_background_service(config))
            return
        if args.status:
            console.print(background_status_panel(config, config_path))
            return
        if args.install_service:
            console.print(manage_system_service(config_path, "install"))
            return
        if args.service:
            result = service_status_panel(config, config_path) if args.service == "status" else manage_system_service(config_path, args.service)
            console.print(result)
            return
        if args.serve_foreground:
            start_service_foreground(config_path, config)
            return
        if not args.serve:
            run_terminal_ui(config_path, config)
            return
        console.print(start_service_background(config_path, config))
    except KeyboardInterrupt:
        clear_terminal_history()
        raise SystemExit(130)


if __name__ == "__main__":
    main()
