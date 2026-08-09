from __future__ import annotations

import hashlib
import json
import tomllib
from pathlib import Path

import pytest

from auto_model_key_router.agent_config import (
    AGENT_MODE_NATIVE,
    AGENT_MODE_UNIFIED_MODEL,
    CLAUDE_CODE,
    CODEX,
    PI_AGENT,
    AgentConfigError,
    configure_agent,
    get_agent_config_status,
    rollback_agent,
    router_origin,
)
from auto_model_key_router.config import UNIFIED_MODEL_ID, RouterConfig


def make_config(
    *,
    host: str = "127.0.0.1",
    port: int = 8000,
    local_api_key: str = "local-key",
    model_aliases: tuple[str, ...] = (),
    include_disabled_model: bool = False,
) -> RouterConfig:
    models = [
        {
            "id": "test-model",
            "aliases": list(model_aliases),
            "keys": [{"name": "main", "api_key": "upstream-key", "base_url": "https://upstream.test"}],
        }
    ]
    if include_disabled_model:
        models.append(
            {
                "id": "disabled-model",
                "keys": [
                    {
                        "name": "disabled",
                        "api_key": "disabled-key",
                        "base_url": "https://disabled.test",
                        "enabled": False,
                    }
                ],
            }
        )
    return RouterConfig.from_dict(
        {
            "host": host,
            "port": port,
            "local_api_key": local_api_key,
            "unified_model": {"model": "test-model"},
            "models": models,
        }
    )


def make_native_config(
    *, host: str = "127.0.0.1", port: int = 8000, local_api_key: str = "local-key"
) -> RouterConfig:
    return RouterConfig.from_dict(
        {
            "config_version": 3,
            "host": host,
            "port": port,
            "local_api_key": local_api_key,
            "models": {},
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
    auth = tmp_path / ".codex" / "auth.json"
    backup = tmp_path / "backups" / "codex.json"
    original = (
        b'# keep this comment\n'
        b'sandbox_mode = "workspace-write"\n'
        b'disable_response_storage = false\n'
        b'network_access = "restricted"\n'
        b'windows_wsl_setup_acknowledged = false\n\n'
        b'[features]\n'
        b'web_search = true\n'
        b'goals = false\n\n'
        b'[model_providers.OpenAI]\n'
        b'name = "Existing OpenAI"\n'
        b'base_url = "https://old.example/v1"\n'
        b'wire_api = "chat"\n'
        b'requires_openai_auth = false\n'
        b'custom_header = "keep"\n'
    )
    original_auth = b'{\n  "OTHER_TOKEN": "keep",\n  "OPENAI_API_KEY": "old-key"\n}\n'
    target.parent.mkdir()
    target.write_bytes(original)
    auth.write_bytes(original_auth)

    result = configure_agent(CODEX, make_config(), target_path=target, backup_path=backup)

    configured = tomllib.loads(target.read_text(encoding="utf-8"))
    auth_configured = json.loads(auth.read_text(encoding="utf-8"))
    provider = configured["model_providers"]["OpenAI"]
    assert configured["sandbox_mode"] == "workspace-write"
    assert configured["features"]["web_search"] is True
    assert configured["features"]["goals"] is False
    assert configured["disable_response_storage"] is False
    assert configured["network_access"] == "restricted"
    assert configured["windows_wsl_setup_acknowledged"] is False
    assert configured["model_provider"] == "OpenAI"
    assert configured["model"] == UNIFIED_MODEL_ID
    assert configured["review_model"] == UNIFIED_MODEL_ID
    assert configured["model_reasoning_effort"] == "xhigh"
    assert provider["name"] == "OpenAI"
    assert provider["base_url"] == "http://127.0.0.1:8000/v1"
    assert provider["wire_api"] == "responses"
    assert provider["requires_openai_auth"] is True
    assert provider["custom_header"] == "keep"
    assert "experimental_bearer_token" not in provider
    assert auth_configured == {"OTHER_TOKEN": "keep", "OPENAI_API_KEY": "local-key"}
    assert result.router_url == "http://127.0.0.1:8000/v1"
    assert result.extra_target_paths == (auth.resolve(),)
    assert get_agent_config_status(CODEX, target_path=target, backup_path=backup).current_is_applied

    configure_agent(CODEX, make_config(port=9000, local_api_key="new-key"), target_path=target, backup_path=backup)
    rollback_agent(CODEX, target_path=target, backup_path=backup)

    assert target.read_bytes() == original
    assert auth.read_bytes() == original_auth


def test_configure_pi_agent_preserves_other_providers_and_rolls_back(tmp_path: Path) -> None:
    target = tmp_path / ".pi" / "agent" / "models.json"
    backup = tmp_path / "backups" / "pi-agent.json"
    original = b'{\n  "providers": {"existing": {"baseUrl": "https://example.test", "api": "openai-completions", "models": [{"id": "keep"}]}}\n}\n'
    target.parent.mkdir(parents=True)
    target.write_bytes(original)

    result = configure_agent(
        PI_AGENT,
        make_config(model_aliases=("test-alias",), include_disabled_model=True),
        target_path=target,
        backup_path=backup,
    )

    configured = json.loads(target.read_text(encoding="utf-8"))
    assert configured["providers"]["existing"]["models"] == [{"id": "keep"}]
    assert configured["providers"]["amkr"] == {
        "baseUrl": "http://127.0.0.1:8000/v1",
        "api": "openai-completions",
        "apiKey": "local-key",
        "authHeader": True,
        "models": [
            {"id": UNIFIED_MODEL_ID, "contextWindow": 262144},
            {"id": "test-model", "contextWindow": 262144},
            {"id": "test-alias", "contextWindow": 262144},
        ],
    }
    assert result.router_url == "http://127.0.0.1:8000/v1"
    assert get_agent_config_status(PI_AGENT, target_path=target, backup_path=backup).current_is_applied

    configure_agent(PI_AGENT, make_config(port=9000), target_path=target, backup_path=backup)
    rollback_agent(PI_AGENT, target_path=target, backup_path=backup)

    assert target.read_bytes() == original


def test_configure_pi_agent_rejects_native_mode(tmp_path: Path) -> None:
    with pytest.raises(AgentConfigError, match="仅支持"):
        configure_agent(
            PI_AGENT,
            make_native_config(),
            mode=AGENT_MODE_NATIVE,
            target_path=tmp_path / "models.json",
            backup_path=tmp_path / "backup.json",
        )


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


def test_configure_codex_rejects_invalid_auth_without_overwriting_files(tmp_path: Path) -> None:
    target = tmp_path / ".codex" / "config.toml"
    auth = tmp_path / ".codex" / "auth.json"
    backup = tmp_path / "backups" / "codex.json"
    original = b'model = "existing"\n'
    invalid_auth = b'["not", "an", "object"]\n'
    target.parent.mkdir()
    target.write_bytes(original)
    auth.write_bytes(invalid_auth)

    with pytest.raises(AgentConfigError, match="Codex 鉴权配置必须是 JSON 对象"):
        configure_agent(CODEX, make_config(), target_path=target, backup_path=backup)

    assert target.read_bytes() == original
    assert auth.read_bytes() == invalid_auth
    assert not backup.exists()


def test_configure_codex_rejects_malformed_auth_without_overwriting_files(tmp_path: Path) -> None:
    target = tmp_path / ".codex" / "config.toml"
    auth = tmp_path / ".codex" / "auth.json"
    backup = tmp_path / "backups" / "codex.json"
    original = b'model = "existing"\n'
    invalid_auth = b'{invalid json\n'
    target.parent.mkdir()
    target.write_bytes(original)
    auth.write_bytes(invalid_auth)

    with pytest.raises(AgentConfigError, match="Codex 鉴权配置不是有效的 UTF-8 JSON"):
        configure_agent(CODEX, make_config(), target_path=target, backup_path=backup)

    assert target.read_bytes() == original
    assert auth.read_bytes() == invalid_auth
    assert not backup.exists()


def test_configure_codex_writes_minimal_new_config(tmp_path: Path) -> None:
    target = tmp_path / ".codex" / "config.toml"
    backup = tmp_path / "backups" / "codex.json"

    configure_agent(CODEX, make_config(), target_path=target, backup_path=backup)

    configured = tomllib.loads(target.read_text(encoding="utf-8"))
    assert "disable_response_storage" not in configured
    assert "network_access" not in configured
    assert "windows_wsl_setup_acknowledged" not in configured
    assert "features" not in configured


def test_configure_codex_writes_model_reasoning_effort_from_config(tmp_path: Path) -> None:
    target = tmp_path / ".codex" / "config.toml"
    backup = tmp_path / "backups" / "codex.json"
    config = RouterConfig.from_dict(
        {
            "host": "127.0.0.1",
            "port": 8000,
            "local_api_key": "local-key",
            "unified_model": {"model": "test-model"},
            "models": [
                {
                    "id": "test-model",
                    "keys": [{"name": "main", "api_key": "upstream-key", "base_url": "https://upstream.test"}],
                    "reasoning_effort": "high",
                }
            ],
        }
    )

    configure_agent(CODEX, config, target_path=target, backup_path=backup)

    configured = tomllib.loads(target.read_text(encoding="utf-8"))
    assert configured["model_reasoning_effort"] == "high"


def test_router_origin_uses_loopback_for_wildcard_and_brackets_ipv6() -> None:
    assert router_origin(make_config(host="0.0.0.0")) == "http://127.0.0.1:8000"
    assert router_origin(make_config(host="::1")) == "http://[::1]:8000"


@pytest.mark.parametrize(
    ("agent", "target_name"),
    ((CLAUDE_CODE, "settings.json"), (CODEX, "config.toml")),
)
def test_configure_native_mode_needs_no_unified_model_and_reports_mode(
    tmp_path: Path, agent: str, target_name: str
) -> None:
    target = tmp_path / target_name
    backup = tmp_path / "backups" / f"{agent}.json"

    result = configure_agent(
        agent,
        make_native_config(),
        mode=AGENT_MODE_NATIVE,
        target_path=target,
        backup_path=backup,
    )

    status = get_agent_config_status(agent, target_path=target, backup_path=backup)
    assert result.mode == AGENT_MODE_NATIVE
    assert status.mode == AGENT_MODE_NATIVE
    assert status.current_is_applied
    if agent == CLAUDE_CODE:
        env = json.loads(target.read_text(encoding="utf-8"))["env"]
        assert env["ANTHROPIC_BASE_URL"] == "http://127.0.0.1:8000"
        assert env["ANTHROPIC_AUTH_TOKEN"] == "local-key"
    else:
        configured = tomllib.loads(target.read_text(encoding="utf-8"))
        assert configured["model_provider"] == "OpenAI"
        assert {"model", "review_model", "model_reasoning_effort"}.isdisjoint(configured)
        assert json.loads((target.parent / "auth.json").read_text(encoding="utf-8"))["OPENAI_API_KEY"] == "local-key"


@pytest.mark.parametrize(("agent", "target_name"), ((CLAUDE_CODE, "settings.json"), (CODEX, "config.toml")))
def test_first_native_apply_preserves_existing_manual_model_settings(
    tmp_path: Path, agent: str, target_name: str
) -> None:
    target = tmp_path / target_name
    backup = tmp_path / "backups" / f"{agent}.json"
    target.parent.mkdir(exist_ok=True)
    if agent == CLAUDE_CODE:
        target.write_text(
            json.dumps(
                {
                    "env": {
                        "ANTHROPIC_MODEL": "manual-model",
                        "ANTHROPIC_DEFAULT_HAIKU_MODEL": "manual-haiku",
                        "ANTHROPIC_DEFAULT_SONNET_MODEL": "manual-sonnet",
                        "ANTHROPIC_DEFAULT_OPUS_MODEL": "manual-opus",
                    }
                }
            ),
            encoding="utf-8",
        )
    else:
        target.write_text(
            'model = "manual-model"\nreview_model = "manual-review"\nmodel_reasoning_effort = "high"\n',
            encoding="utf-8",
        )

    configure_agent(
        agent,
        make_native_config(),
        mode=AGENT_MODE_NATIVE,
        target_path=target,
        backup_path=backup,
    )

    if agent == CLAUDE_CODE:
        env = json.loads(target.read_text(encoding="utf-8"))["env"]
        assert env["ANTHROPIC_MODEL"] == "manual-model"
        assert env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] == "manual-haiku"
        assert env["ANTHROPIC_DEFAULT_SONNET_MODEL"] == "manual-sonnet"
        assert env["ANTHROPIC_DEFAULT_OPUS_MODEL"] == "manual-opus"
    else:
        configured = tomllib.loads(target.read_text(encoding="utf-8"))
        assert configured["model"] == "manual-model"
        assert configured["review_model"] == "manual-review"
        assert configured["model_reasoning_effort"] == "high"


def test_unified_to_native_transition_rolls_back_exactly_including_codex_auth(tmp_path: Path) -> None:
    target = tmp_path / ".codex" / "config.toml"
    auth = target.parent / "auth.json"
    backup = tmp_path / "backups" / "codex.json"
    original = b'profile = "keep"\n'
    original_auth = b'{"OPENAI_API_KEY": "original", "other": "keep"}\n'
    target.parent.mkdir()
    target.write_bytes(original)
    auth.write_bytes(original_auth)

    configure_agent(CODEX, make_config(), target_path=target, backup_path=backup)
    configure_agent(
        CODEX,
        make_native_config(local_api_key="native-key"),
        mode=AGENT_MODE_NATIVE,
        target_path=target,
        backup_path=backup,
    )

    configured = tomllib.loads(target.read_text(encoding="utf-8"))
    assert {"model", "review_model", "model_reasoning_effort"}.isdisjoint(configured)
    assert get_agent_config_status(CODEX, target_path=target, backup_path=backup).mode == AGENT_MODE_NATIVE
    rollback = rollback_agent(CODEX, target_path=target, backup_path=backup)

    assert rollback.mode == AGENT_MODE_NATIVE
    assert target.read_bytes() == original
    assert auth.read_bytes() == original_auth


def test_unified_to_native_transition_removes_claude_model_settings(tmp_path: Path) -> None:
    target = tmp_path / ".claude" / "settings.json"
    backup = tmp_path / "backups" / "claude-code.json"

    configure_agent(CLAUDE_CODE, make_config(), target_path=target, backup_path=backup)
    configure_agent(
        CLAUDE_CODE,
        make_native_config(),
        mode=AGENT_MODE_NATIVE,
        target_path=target,
        backup_path=backup,
    )

    env = json.loads(target.read_text(encoding="utf-8"))["env"]
    assert not {
        "ANTHROPIC_MODEL",
        "ANTHROPIC_DEFAULT_HAIKU_MODEL",
        "ANTHROPIC_DEFAULT_SONNET_MODEL",
        "ANTHROPIC_DEFAULT_OPUS_MODEL",
    } & env.keys()


def test_matching_legacy_backup_reports_unified_model_mode(tmp_path: Path) -> None:
    target = tmp_path / "config.toml"
    backup = tmp_path / "backup.json"
    configured = b'model = "router/unified"\n'
    target.write_bytes(configured)
    backup.write_text(
        json.dumps(
            {
                "version": 1,
                "agent": CODEX,
                "target_path": str(target.resolve()),
                "original_exists": False,
                "original_content": "",
                "applied_sha256": hashlib.sha256(configured).hexdigest(),
            }
        ),
        encoding="utf-8",
    )

    status = get_agent_config_status(CODEX, target_path=target, backup_path=backup)

    assert status.current_is_applied
    assert status.mode == AGENT_MODE_UNIFIED_MODEL
    target.write_bytes(b'model = "changed"\n')
    assert get_agent_config_status(CODEX, target_path=target, backup_path=backup).mode is None


def test_invalid_mode_does_not_change_files(tmp_path: Path) -> None:
    target = tmp_path / "config.toml"
    backup = tmp_path / "backup.json"
    original = b'model = "existing"\n'
    backup_content = b'{"existing": true}\n'
    target.write_bytes(original)
    backup.write_bytes(backup_content)

    with pytest.raises(AgentConfigError, match="模式"):
        configure_agent(CODEX, make_config(), mode="invalid", target_path=target, backup_path=backup)

    assert target.read_bytes() == original
    assert backup.read_bytes() == backup_content
