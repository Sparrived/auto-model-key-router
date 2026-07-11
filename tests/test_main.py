from __future__ import annotations

import importlib
import sys


main_module = importlib.import_module("auto_model_key_router.main")


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
