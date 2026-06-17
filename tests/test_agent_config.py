from __future__ import annotations

import json
import tomllib
from pathlib import Path

import pytest

from auto_model_key_router.agent_config import (
    CLAUDE_CODE,
    CODEX,
    AgentConfigError,
    configure_agent,
    get_agent_config_status,
    rollback_agent,
    router_origin,
)
from auto_model_key_router.config import UNIFIED_MODEL_ID, RouterConfig


def make_config(*, host: str = "127.0.0.1", port: int = 8000, local_api_key: str = "local-key") -> RouterConfig:
    return RouterConfig.from_dict(
        {
            "host": host,
            "port": port,
            "local_api_key": local_api_key,
            "unified_model": {"model": "test-model"},
            "models": [
                {
                    "id": "test-model",
                    "keys": [{"name": "main", "api_key": "upstream-key", "base_url": "https://upstream.test"}],
                }
            ],
        }
    )


def test_configure_claude_code_preserves_settings_and_rolls_back_exactly(tmp_path: Path) -> None:
    target = tmp_path / ".claude" / "settings.json"
    backup = tmp_path / "backups" / "claude-code.json"
    original = b'{\n  "theme": "dark",\n  "env": {"EXISTING": "yes"}\n}\n'
    target.parent.mkdir()
    target.write_bytes(original)

    result = configure_agent(CLAUDE_CODE, make_config(), target_path=target, backup_path=backup)

    configured = json.loads(target.read_text(encoding="utf-8"))
    assert configured["theme"] == "dark"
    assert configured["env"]["EXISTING"] == "yes"
    assert configured["env"]["ANTHROPIC_BASE_URL"] == "http://127.0.0.1:8000"
    assert configured["env"]["ANTHROPIC_AUTH_TOKEN"] == "local-key"
    assert configured["env"]["ANTHROPIC_MODEL"] == UNIFIED_MODEL_ID
    assert configured["env"]["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] == "1"
    assert result.router_url == "http://127.0.0.1:8000"
    assert get_agent_config_status(CLAUDE_CODE, target_path=target, backup_path=backup).current_is_applied

    configure_agent(CLAUDE_CODE, make_config(port=9000), target_path=target, backup_path=backup)
    rollback_agent(CLAUDE_CODE, target_path=target, backup_path=backup)

    assert target.read_bytes() == original
    assert not backup.exists()


def test_configure_codex_preserves_other_toml_settings_and_rolls_back(tmp_path: Path) -> None:
    target = tmp_path / ".codex" / "config.toml"
    backup = tmp_path / "backups" / "codex.json"
    original = b'# keep this comment\nsandbox_mode = "workspace-write"\n\n[features]\nweb_search = true\n'
    target.parent.mkdir()
    target.write_bytes(original)

    result = configure_agent(CODEX, make_config(), target_path=target, backup_path=backup)

    configured = tomllib.loads(target.read_text(encoding="utf-8"))
    provider = configured["model_providers"]["auto_model_key_router"]
    assert configured["sandbox_mode"] == "workspace-write"
    assert configured["features"]["web_search"] is True
    assert configured["model"] == UNIFIED_MODEL_ID
    assert configured["model_provider"] == "auto_model_key_router"
    assert provider["base_url"] == "http://127.0.0.1:8000/v1"
    assert provider["wire_api"] == "responses"
    assert provider["experimental_bearer_token"] == "local-key"
    assert result.router_url == "http://127.0.0.1:8000/v1"

    rollback_agent(CODEX, target_path=target, backup_path=backup)

    assert target.read_bytes() == original


def test_rollback_removes_agent_config_that_did_not_exist_before_apply(tmp_path: Path) -> None:
    target = tmp_path / ".claude" / "settings.json"
    backup = tmp_path / "backups" / "claude-code.json"

    configure_agent(CLAUDE_CODE, make_config(), target_path=target, backup_path=backup)
    assert target.exists()

    rollback_agent(CLAUDE_CODE, target_path=target, backup_path=backup)

    assert not target.exists()


def test_configure_agent_requires_unified_model(tmp_path: Path) -> None:
    config = RouterConfig.from_dict(
        {
            "local_api_key": "local-key",
            "models": [
                {
                    "id": "test-model",
                    "keys": [{"name": "main", "api_key": "upstream-key"}],
                }
            ],
        }
    )

    with pytest.raises(AgentConfigError, match=UNIFIED_MODEL_ID):
        configure_agent(CODEX, config, target_path=tmp_path / "config.toml", backup_path=tmp_path / "backup.json")


def test_rollback_rejects_corrupted_backup(tmp_path: Path) -> None:
    target = tmp_path / "config.toml"
    backup = tmp_path / "backup.json"
    backup.write_text(
        json.dumps(
            {
                "version": 1,
                "agent": CODEX,
                "target_path": str(target.resolve()),
                "original_exists": True,
                "original_content": "not-base64!",
                "applied_sha256": "",
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(AgentConfigError, match="备份内容已损坏"):
        rollback_agent(CODEX, target_path=target, backup_path=backup)


def test_router_origin_uses_loopback_for_wildcard_and_brackets_ipv6() -> None:
    assert router_origin(make_config(host="0.0.0.0")) == "http://127.0.0.1:8000"
    assert router_origin(make_config(host="::1")) == "http://[::1]:8000"
