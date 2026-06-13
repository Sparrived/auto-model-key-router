from __future__ import annotations

import base64
import binascii
import hashlib
import json
import os
import secrets
from collections.abc import MutableMapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import tomlkit

from .config import UNIFIED_MODEL_ID, RouterConfig, default_cache_dir


CLAUDE_CODE = "claude-code"
CODEX = "codex"
SUPPORTED_AGENTS = (CLAUDE_CODE, CODEX)
CODEX_PROVIDER_ID = "auto_model_key_router"


class AgentConfigError(ValueError):
    pass


@dataclass(frozen=True)
class AgentConfigStatus:
    agent: str
    target_path: Path
    backup_path: Path
    backup_available: bool
    current_is_applied: bool


@dataclass(frozen=True)
class AgentConfigResult:
    agent: str
    target_path: Path
    backup_path: Path
    router_url: str
    restored: bool = False


def agent_display_name(agent: str) -> str:
    return {
        CLAUDE_CODE: "Claude Code",
        CODEX: "Codex",
    }.get(agent, agent)


def agent_config_path(agent: str) -> Path:
    _validate_agent(agent)
    if agent == CLAUDE_CODE:
        config_dir = os.environ.get("CLAUDE_CONFIG_DIR")
        return (Path(config_dir).expanduser() if config_dir else Path.home() / ".claude") / "settings.json"
    codex_home = os.environ.get("CODEX_HOME")
    return (Path(codex_home).expanduser() if codex_home else Path.home() / ".codex") / "config.toml"


def agent_backup_path(agent: str, backup_dir: Path | None = None) -> Path:
    _validate_agent(agent)
    root = backup_dir if backup_dir is not None else default_cache_dir() / "agent-config-backups"
    return root / f"{agent}.json"


def router_origin(config: RouterConfig) -> str:
    host = "127.0.0.1" if config.host in {"0.0.0.0", "::", "[::]"} else config.host
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    return f"http://{host}:{config.port}"


def get_agent_config_status(
    agent: str,
    *,
    target_path: Path | None = None,
    backup_path: Path | None = None,
) -> AgentConfigStatus:
    target = _resolved_path(target_path or agent_config_path(agent))
    backup = _resolved_path(backup_path or agent_backup_path(agent))
    state = _load_backup_state(backup)
    backup_available = bool(state and state.get("agent") == agent and state.get("target_path") == str(target))
    current_is_applied = False
    if backup_available and target.exists():
        current_is_applied = _sha256(target.read_bytes()) == state.get("applied_sha256")
    return AgentConfigStatus(agent, target, backup, backup_available, current_is_applied)


def configure_agent(
    agent: str,
    config: RouterConfig,
    *,
    target_path: Path | None = None,
    backup_path: Path | None = None,
) -> AgentConfigResult:
    _validate_agent(agent)
    if config.unified_model is None:
        raise AgentConfigError(f"请先配置 {UNIFIED_MODEL_ID}，再应用 {agent_display_name(agent)} 路由配置")
    if not config.local_api_key:
        raise AgentConfigError("本地鉴权 key 为空，无法配置 Agent")

    target = _resolved_path(target_path or agent_config_path(agent))
    backup = _resolved_path(backup_path or agent_backup_path(agent))
    current = target.read_bytes() if target.exists() else b""
    state = _load_backup_state(backup)

    preserve_existing_backup = bool(
        state
        and state.get("agent") == agent
        and state.get("target_path") == str(target)
        and state.get("applied_sha256") == _sha256(current)
    )
    if preserve_existing_backup:
        original_exists = bool(state["original_exists"])
        original_content = str(state["original_content"])
    else:
        original_exists = target.exists()
        original_content = base64.b64encode(current).decode("ascii")

    if agent == CLAUDE_CODE:
        updated = _configure_claude_code(current, config)
        route_url = router_origin(config)
    else:
        updated = _configure_codex(current, config)
        route_url = f"{router_origin(config)}/v1"

    new_state = {
        "version": 1,
        "agent": agent,
        "target_path": str(target),
        "original_exists": original_exists,
        "original_content": original_content,
        "applied_sha256": _sha256(updated),
    }
    previous_backup = backup.read_bytes() if backup.exists() else None
    _write_atomic(backup, json.dumps(new_state, indent=2, ensure_ascii=True).encode("utf-8") + b"\n")
    try:
        _write_atomic(target, updated)
    except Exception:
        if previous_backup is None:
            backup.unlink(missing_ok=True)
        else:
            _write_atomic(backup, previous_backup)
        raise
    return AgentConfigResult(agent, target, backup, route_url)


def rollback_agent(
    agent: str,
    *,
    target_path: Path | None = None,
    backup_path: Path | None = None,
) -> AgentConfigResult:
    _validate_agent(agent)
    backup = _resolved_path(backup_path or agent_backup_path(agent))
    state = _load_backup_state(backup)
    if not state or state.get("agent") != agent:
        raise AgentConfigError(f"没有可用于回退的 {agent_display_name(agent)} 配置")

    stored_target_text = str(state.get("target_path") or "")
    if not stored_target_text:
        raise AgentConfigError("Agent 配置备份缺少目标路径")
    stored_target = _resolved_path(Path(stored_target_text))
    target = _resolved_path(target_path) if target_path is not None else stored_target
    if target != stored_target:
        raise AgentConfigError("备份对应的配置路径与当前目标路径不一致")

    try:
        original = base64.b64decode(str(state.get("original_content") or ""), validate=True)
    except (binascii.Error, ValueError) as exc:
        raise AgentConfigError("Agent 配置备份内容已损坏") from exc
    if state.get("original_exists"):
        _write_atomic(target, original)
    else:
        target.unlink(missing_ok=True)
    backup.unlink(missing_ok=True)
    route_url = ""
    return AgentConfigResult(agent, target, backup, route_url, restored=True)


def _configure_claude_code(current: bytes, config: RouterConfig) -> bytes:
    if current:
        try:
            data = json.loads(current.decode("utf-8-sig"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise AgentConfigError(f"Claude Code 配置不是有效的 UTF-8 JSON: {exc}") from exc
        if not isinstance(data, dict):
            raise AgentConfigError("Claude Code 配置根节点必须是 JSON 对象")
    else:
        data = {}

    env = data.get("env")
    if env is None:
        env = {}
        data["env"] = env
    if not isinstance(env, dict):
        raise AgentConfigError("Claude Code 配置中的 env 必须是 JSON 对象")

    env.update(
        {
            "ANTHROPIC_BASE_URL": router_origin(config),
            "ANTHROPIC_AUTH_TOKEN": config.local_api_key,
            "ANTHROPIC_MODEL": UNIFIED_MODEL_ID,
            "ANTHROPIC_DEFAULT_HAIKU_MODEL": UNIFIED_MODEL_ID,
            "ANTHROPIC_DEFAULT_SONNET_MODEL": UNIFIED_MODEL_ID,
            "ANTHROPIC_DEFAULT_OPUS_MODEL": UNIFIED_MODEL_ID,
        }
    )
    return json.dumps(data, indent=2, ensure_ascii=False).encode("utf-8") + b"\n"


def _configure_codex(current: bytes, config: RouterConfig) -> bytes:
    if current:
        try:
            document = tomlkit.parse(current.decode("utf-8-sig"))
        except Exception as exc:
            raise AgentConfigError(f"Codex 配置不是有效的 UTF-8 TOML: {exc}") from exc
    else:
        document = tomlkit.document()

    document["model"] = UNIFIED_MODEL_ID
    document["model_provider"] = CODEX_PROVIDER_ID
    providers = document.get("model_providers")
    if providers is None:
        providers = tomlkit.table()
        document["model_providers"] = providers
    if not isinstance(providers, MutableMapping):
        raise AgentConfigError("Codex 配置中的 model_providers 必须是 TOML 表")

    provider = tomlkit.table()
    provider["name"] = "Auto Model Key Router"
    provider["base_url"] = f"{router_origin(config)}/v1"
    provider["wire_api"] = "responses"
    provider["experimental_bearer_token"] = config.local_api_key
    providers[CODEX_PROVIDER_ID] = provider
    return tomlkit.dumps(document).encode("utf-8")


def _load_backup_state(path: Path) -> dict[str, Any] | None:
    if not path.exists():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return data if isinstance(data, dict) else None


def _write_atomic(path: Path, content: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{secrets.token_hex(6)}.tmp")
    try:
        temporary.write_bytes(content)
        if os.name != "nt":
            temporary.chmod(0o600)
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def _resolved_path(path: Path) -> Path:
    return path.expanduser().resolve(strict=False)


def _sha256(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


def _validate_agent(agent: str) -> None:
    if agent not in SUPPORTED_AGENTS:
        raise AgentConfigError(f"不支持的 Agent: {agent}")
