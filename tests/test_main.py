from __future__ import annotations

import importlib
import sys
from types import SimpleNamespace


main_module = importlib.import_module("auto_model_key_router.main")


def test_router_address_text_formats_ipv4_and_ipv6() -> None:
    ipv4_text = main_module.router_address_text(
        SimpleNamespace(host="127.0.0.1", port=8000)
    )
    ipv6_text = main_module.router_address_text(
        SimpleNamespace(host="::1", port=9000)
    )
    assert "监听 IP:" in ipv4_text
    assert "127.0.0.1" in ipv4_text
    assert "监听端口:" in ipv4_text
    assert "8000" in ipv4_text
    assert "服务地址:" in ipv6_text
    assert "http://[::1]:9000" in ipv6_text


def test_show_address_does_not_run_interactive_pool_repair(tmp_path, monkeypatch, capsys) -> None:
    config_path = tmp_path / "router-config.json"
    config = SimpleNamespace(host="0.0.0.0", port=8123)
    events: list[str] = []
    monkeypatch.setattr(
        sys, "argv", ["amkr", "--config", str(config_path), "--show-address"]
    )
    monkeypatch.setattr(
        main_module,
        "repair_duplicate_pool_memberships_interactively",
        lambda path: events.append("repair"),
        raising=False,
    )
    monkeypatch.setattr(main_module.RouterConfig, "load", lambda path: events.append("load") or config)

    main_module.main()

    output = capsys.readouterr().out
    assert events == ["load"]
    assert "监听 IP: 0.0.0.0" in output
    assert "监听端口: 8123" in output
    assert "服务地址: http://0.0.0.0:8123" in output


def test_default_cli_repairs_pool_memberships_before_loading_config(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    events: list[str] = []
    config = object()
    monkeypatch.setattr(sys, "argv", ["amkr", "--config", str(config_path)])
    monkeypatch.setattr(
        main_module,
        "repair_duplicate_pool_memberships_interactively",
        lambda path: events.append("repair"),
        raising=False,
    )
    monkeypatch.setattr(
        main_module.RouterConfig,
        "load",
        lambda path: events.append("load") or config,
    )
    monkeypatch.setattr(
        main_module,
        "run_terminal_ui",
        lambda path, loaded: events.append("tui"),
    )

    main_module.main()

    assert events == ["repair", "load", "tui"]


def test_serve_does_not_run_interactive_pool_repair(tmp_path, monkeypatch) -> None:
    config_path = tmp_path / "router-config.json"
    events: list[str] = []
    config = object()
    monkeypatch.setattr(sys, "argv", ["amkr", "--config", str(config_path), "--serve"])
    monkeypatch.setattr(
        main_module,
        "repair_duplicate_pool_memberships_interactively",
        lambda path: events.append("repair"),
        raising=False,
    )
    monkeypatch.setattr(main_module.RouterConfig, "load", lambda path: events.append("load") or config)
    monkeypatch.setattr(
        main_module,
        "start_service_background",
        lambda path, loaded: events.append("serve") or "started",
    )

    main_module.main()

    assert events == ["load", "serve"]
