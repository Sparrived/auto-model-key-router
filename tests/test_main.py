from __future__ import annotations

import importlib
import json
import sys
from types import SimpleNamespace

import pytest


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


def test_show_api_key_prints_configured_key_without_interactive_repair(tmp_path, monkeypatch, capsys) -> None:
    config_path = tmp_path / "router-config.json"
    config = SimpleNamespace(local_api_key="amkr_local-test-key")
    events: list[str] = []
    monkeypatch.setattr(sys, "argv", ["amkr", "--config", str(config_path), "--show-api-key"])
    monkeypatch.setattr(
        main_module,
        "repair_duplicate_pool_memberships_interactively",
        lambda path: events.append("repair"),
        raising=False,
    )
    monkeypatch.setattr(main_module.RouterConfig, "load", lambda path: events.append("load") or config)

    main_module.main()

    assert events == ["load"]
    assert capsys.readouterr().out.strip() == "amkr_local-test-key"


@pytest.mark.parametrize("flag", ["--get-api-key", "--get-key"])
def test_get_key_aliases_print_configured_key(tmp_path, monkeypatch, capsys, flag) -> None:
    config_path = tmp_path / "router-config.json"
    config = SimpleNamespace(local_api_key="amkr_alias-test-key")
    monkeypatch.setattr(sys, "argv", ["amkr", "--config", str(config_path), flag])
    monkeypatch.setattr(main_module.RouterConfig, "load", lambda path: config)

    main_module.main()

    assert capsys.readouterr().out.strip() == "amkr_alias-test-key"


def test_show_api_key_has_priority_over_show_address(tmp_path, monkeypatch, capsys) -> None:
    config_path = tmp_path / "router-config.json"
    config = SimpleNamespace(local_api_key="amkr_priority-test-key", host="127.0.0.1", port=8000)
    monkeypatch.setattr(
        sys,
        "argv",
        ["amkr", "--config", str(config_path), "--show-address", "--show-api-key"],
    )
    monkeypatch.setattr(main_module.RouterConfig, "load", lambda path: config)

    main_module.main()

    output = capsys.readouterr().out
    assert output.strip() == "amkr_priority-test-key"
    assert "监听 IP" not in output


def test_show_api_key_creates_and_persists_missing_config(tmp_path, monkeypatch, capsys) -> None:
    config_path = tmp_path / "router-config.json"
    monkeypatch.setattr(
        sys,
        "argv",
        ["amkr", "--config", str(config_path), "--show-api-key"],
    )

    main_module.main()

    output = capsys.readouterr().out.strip()
    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert output == saved["local_api_key"]
    assert output.startswith("amkr_")


def test_show_api_key_returns_failure_when_config_load_fails(tmp_path, monkeypatch, capsys) -> None:
    config_path = tmp_path / "router-config.json"
    monkeypatch.setattr(
        sys,
        "argv",
        ["amkr", "--config", str(config_path), "--show-api-key"],
    )
    monkeypatch.setattr(
        main_module.RouterConfig,
        "load",
        lambda path: (_ for _ in ()).throw(ValueError("invalid config")),
    )

    with pytest.raises(SystemExit) as exc_info:
        main_module.main()

    assert exc_info.value.code == 1
    assert "配置加载失败" in capsys.readouterr().out


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
