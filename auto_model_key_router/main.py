from __future__ import annotations

import argparse
import sys
from pathlib import Path

from . import __version__
from .config import DEFAULT_CONFIG_PATH, RouterConfig
from .dashboard import render_config, run_terminal_ui
from .logs_tui import render_logs
from .service import background_status_panel, manage_system_service, start_service_background, start_service_foreground, stop_background_service
from .tui import clear_terminal_history, console, section_panel
from .update import check_latest_version, render_version_check_result, update_latest_version


def main() -> None:
    parser = argparse.ArgumentParser(prog=Path(sys.argv[0]).stem)
    parser.add_argument("--config", default=str(DEFAULT_CONFIG_PATH), help="配置文件路径")
    parser.add_argument("--host", help="覆盖配置中的监听地址")
    parser.add_argument("--port", type=int, help="覆盖配置中的监听端口")
    parser.add_argument("--show-config", action="store_true", help="只展示配置摘要，不启动服务")
    parser.add_argument("--show-logs", nargs="?", const=20, type=int, help="进入调用日志，显示最近 N 行运行日志，调用统计明细固定 10 行/页")
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    parser.add_argument("--check-update", action="store_true", help="通过 PyPI/GitHub 检查最新版本")
    parser.add_argument("--update", action="store_true", help="通过 PyPI/GitHub 手动更新到最新版本")
    parser.add_argument("--serve", action="store_true", help="跳过 Terminal UI，后台启动服务")
    parser.add_argument("--serve-foreground", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--stop", action="store_true", help="停止后台服务")
    parser.add_argument("--status", action="store_true", help="查看后台服务状态")
    parser.add_argument("--install-service", action="store_true", help="注册为 Windows/Linux 内置服务")
    parser.add_argument("--service", choices=["install", "uninstall", "start", "stop", "restart", "status"], help="管理 Windows/Linux 内置服务")
    args = parser.parse_args()

    clear_terminal_history()
    if args.check_update:
        console.print(render_version_check_result(check_latest_version(timeout=10.0)))
        return
    if args.update:
        console.print(update_latest_version(timeout=10.0))
        return

    config_path = Path(args.config)

    try:
        try:
            config = RouterConfig.load(config_path)
        except Exception as exc:
            console.print(section_panel(f"[red]{exc}[/red]", "配置加载失败", "red"))
            raise SystemExit(1) from exc

        if args.host:
            config = RouterConfig(args.host, config.port, config.request_timeout, config.max_retries, config.key_failure_threshold, config.key_cooldown_seconds, config.key_state_path, config.upstream_health_check_interval, config.metrics_db_path, config.log_file_path, config.local_api_key, config.models)
        if args.port:
            config = RouterConfig(config.host, args.port, config.request_timeout, config.max_retries, config.key_failure_threshold, config.key_cooldown_seconds, config.key_state_path, config.upstream_health_check_interval, config.metrics_db_path, config.log_file_path, config.local_api_key, config.models)

        if args.show_logs is not None:
            render_config(config, config_path)
            render_logs(config.metrics_db_path, config.log_file_path, args.show_logs)
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
            result = background_status_panel(config, config_path) if args.service == "status" else manage_system_service(config_path, args.service)
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
