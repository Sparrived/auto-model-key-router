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
AGENT_MODE_NATIVE = "native"
AGENT_MODE_UNIFIED_MODEL = "unified-model"
SUPPORTED_AGENT_MODES = (AGENT_MODE_NATIVE, AGENT_MODE_UNIFIED_MODEL)
CODEX_PROVIDER_ID = "OpenAI"


class AgentConfigError(ValueError):
    pass


@dataclass(frozen=True)
class AgentConfigStatus:
    agent: str
    target_path: Path
    backup_path: Path
    backup_available: bool
    current_is_applied: bool
    mode: str | None = None


@dataclass(frozen=True)
class AgentConfigResult:
    agent: str
    target_path: Path
    backup_path: Path
    router_url: str
    extra_target_paths: tuple[Path, ...] = ()
    restored: bool = False
    mode: str | None = None


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
        current_is_applied = (
            _sha256(target.read_bytes()) == state.get("applied_sha256")
            and _extra_targets_are_applied(state)
        )
    mode = _backup_mode(state) if current_is_applied else None
    return AgentConfigStatus(agent, target, backup, backup_available, current_is_applied, mode)


def configure_agent(
    agent: str,
    config: RouterConfig,
    *,
    mode: str = AGENT_MODE_UNIFIED_MODEL,
    target_path: Path | None = None,
    backup_path: Path | None = None,
) -> AgentConfigResult:
    _validate_agent(agent)
    _validate_mode(mode)
    if mode == AGENT_MODE_UNIFIED_MODEL and config.unified_model is None:
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
        and _extra_targets_are_applied(state)
    )
    if preserve_existing_backup:
        original_exists = bool(state["original_exists"])
        original_content = str(state["original_content"])
    else:
        original_exists = target.exists()
        original_content = base64.b64encode(current).decode("ascii")
    remove_unified_model_overrides = (
        mode == AGENT_MODE_NATIVE
        and preserve_existing_backup
        and _backup_mode(state) == AGENT_MODE_UNIFIED_MODEL
    )

    extra_targets: tuple[tuple[Path, bytes], ...] = ()
    if agent == CLAUDE_CODE:
        updated = _configure_claude_code(current, config, mode, remove_unified_model_overrides)
        route_url = router_origin(config)
    else:
        updated = _configure_codex(current, config, mode, remove_unified_model_overrides)
        auth_target = _codex_auth_path(target)
        current_auth = auth_target.read_bytes() if auth_target.exists() else b""
        extra_targets = ((auth_target, _configure_codex_auth(current_auth, config)),)
        route_url = f"{router_origin(config)}/v1"

    new_state = {
        "version": 2,
        "agent": agent,
        "mode": mode,
        "target_path": str(target),
        "original_exists": original_exists,
        "original_content": original_content,
        "applied_sha256": _sha256(updated),
    }
    if extra_targets:
        new_state["extra_targets"] = _extra_backup_entries(
            extra_targets, preserve_existing_backup=preserve_existing_backup, state=state
        )
    previous_backup = backup.read_bytes() if backup.exists() else None
    snapshots = _file_snapshots((target, *(path for path, _ in extra_targets)))
    _write_atomic(
        backup,
        json.dumps(new_state, indent=2, ensure_ascii=True).encode("utf-8") + b"\n",
    )
    try:
        _write_atomic(target, updated)
        for extra_target, extra_content in extra_targets:
            _write_atomic(extra_target, extra_content)
    except Exception:
        _restore_file_snapshots(snapshots)
        if previous_backup is None:
            backup.unlink(missing_ok=True)
        else:
            _write_atomic(backup, previous_backup)
        raise
    return AgentConfigResult(
        agent,
        target,
        backup,
        route_url,
        tuple(path for path, _ in extra_targets),
        mode=mode,
    )


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

    restores = [(target, bool(state.get("original_exists")), _decode_backup_content(state))]
    for extra_state in _extra_backup_states(state):
        extra_target_text = str(extra_state.get("target_path") or "")
        if not extra_target_text:
            raise AgentConfigError("Agent 配置备份缺少附加目标路径")
        restores.append(
            (
                _resolved_path(Path(extra_target_text)),
                bool(extra_state.get("original_exists")),
                _decode_backup_content(extra_state),
            )
        )

    for restore_target, restore_exists, restore_content in restores:
        if restore_exists:
            _write_atomic(restore_target, restore_content)
        else:
            restore_target.unlink(missing_ok=True)
    backup.unlink(missing_ok=True)
    route_url = ""
    return AgentConfigResult(
        agent,
        target,
        backup,
        route_url,
        tuple(path for path, _, _ in restores[1:]),
        restored=True,
        mode=_backup_mode(state),
    )


def _configure_claude_code(
    current: bytes,
    config: RouterConfig,
    mode: str,
    remove_unified_model_overrides: bool,
) -> bytes:
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

    env.update({
        "ANTHROPIC_BASE_URL": router_origin(config),
        "ANTHROPIC_AUTH_TOKEN": config.local_api_key,
        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
        "CLAUDE_CODE_ATTRIBUTION_HEADER": "false",
    })
    if mode == AGENT_MODE_UNIFIED_MODEL:
        env.update(
            {
            "ANTHROPIC_MODEL": UNIFIED_MODEL_ID,
            "ANTHROPIC_DEFAULT_HAIKU_MODEL": UNIFIED_MODEL_ID,
            "ANTHROPIC_DEFAULT_SONNET_MODEL": UNIFIED_MODEL_ID,
            "ANTHROPIC_DEFAULT_OPUS_MODEL": UNIFIED_MODEL_ID,
        }
        )
    elif remove_unified_model_overrides:
        for name in (
            "ANTHROPIC_MODEL",
            "ANTHROPIC_DEFAULT_HAIKU_MODEL",
            "ANTHROPIC_DEFAULT_SONNET_MODEL",
            "ANTHROPIC_DEFAULT_OPUS_MODEL",
        ):
            env.pop(name, None)
    data["attribution"] = {"commit": "", "pr": ""}
    return json.dumps(data, indent=2, ensure_ascii=False).encode("utf-8") + b"\n"


def _configure_codex(
    current: bytes,
    config: RouterConfig,
    mode: str,
    remove_unified_model_overrides: bool,
) -> bytes:
    if current:
        try:
            document = tomlkit.parse(current.decode("utf-8-sig"))
        except Exception as exc:
            raise AgentConfigError(f"Codex 配置不是有效的 UTF-8 TOML: {exc}") from exc
    else:
        document = tomlkit.document()

    document["model_provider"] = CODEX_PROVIDER_ID
    if mode == AGENT_MODE_UNIFIED_MODEL:
        document["model"] = UNIFIED_MODEL_ID
        document["review_model"] = UNIFIED_MODEL_ID
        document["model_reasoning_effort"] = (
            config.reasoning_effort_by_model.get(config.unified_model.model) or "xhigh"
        )
    elif remove_unified_model_overrides:
        for name in ("model", "review_model", "model_reasoning_effort"):
            document.pop(name, None)
    providers = document.get("model_providers")
    if providers is None:
        providers = tomlkit.table()
        document["model_providers"] = providers
    if not isinstance(providers, MutableMapping):
        raise AgentConfigError("Codex 配置中的 model_providers 必须是 TOML 表")

    provider = providers.get(CODEX_PROVIDER_ID)
    if provider is None:
        provider = tomlkit.table()
        providers[CODEX_PROVIDER_ID] = provider
    if not isinstance(provider, MutableMapping):
        raise AgentConfigError("Codex 配置中的 model_providers.OpenAI 必须是 TOML 表")
    provider["name"] = CODEX_PROVIDER_ID
    provider["base_url"] = f"{router_origin(config)}/v1"
    provider["wire_api"] = "responses"
    provider["requires_openai_auth"] = True

    return tomlkit.dumps(document).encode("utf-8")



def _codex_auth_path(config_path: Path) -> Path:
    return config_path.parent / "auth.json"



def _configure_codex_auth(current: bytes, config: RouterConfig) -> bytes:
    if current:
        try:
            data = json.loads(current.decode("utf-8-sig"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise AgentConfigError(f"Codex 鉴权配置不是有效的 UTF-8 JSON: {exc}") from exc
    else:
        data = {}
    if not isinstance(data, dict):
        raise AgentConfigError("Codex 鉴权配置必须是 JSON 对象")
    data["OPENAI_API_KEY"] = config.local_api_key
    return json.dumps(data, indent=2, ensure_ascii=False).encode("utf-8") + b"\n"



def _extra_backup_states(state: dict[str, Any]) -> tuple[dict[str, Any], ...]:
    extra_targets = state.get("extra_targets")
    if not isinstance(extra_targets, list):
        return ()
    return tuple(item for item in extra_targets if isinstance(item, dict))



def _extra_targets_are_applied(state: dict[str, Any] | None) -> bool:
    if not state:
        return True
    for extra_state in _extra_backup_states(state):
        target_text = str(extra_state.get("target_path") or "")
        applied_sha256 = extra_state.get("applied_sha256")
        if not target_text or not isinstance(applied_sha256, str):
            return False
        target = _resolved_path(Path(target_text))
        if not target.exists() or _sha256(target.read_bytes()) != applied_sha256:
            return False
    return True



def _extra_backup_entries(
    extra_targets: tuple[tuple[Path, bytes], ...],
    *,
    preserve_existing_backup: bool,
    state: dict[str, Any] | None,
) -> list[dict[str, Any]]:
    existing_states = {
        str(_resolved_path(Path(str(item.get("target_path") or "")))): item
        for item in _extra_backup_states(state or {})
        if item.get("target_path")
    }
    entries: list[dict[str, Any]] = []
    for target, content in extra_targets:
        current = target.read_bytes() if target.exists() else b""
        stored_state = existing_states.get(str(target)) if preserve_existing_backup else None
        if stored_state and stored_state.get("applied_sha256") == _sha256(current):
            original_exists = bool(stored_state.get("original_exists"))
            original_content = str(stored_state.get("original_content") or "")
        else:
            original_exists = target.exists()
            original_content = base64.b64encode(current).decode("ascii")
        entries.append(
            {
                "target_path": str(target),
                "original_exists": original_exists,
                "original_content": original_content,
                "applied_sha256": _sha256(content),
            }
        )
    return entries



def _decode_backup_content(state: dict[str, Any]) -> bytes:
    try:
        return base64.b64decode(str(state.get("original_content") or ""), validate=True)
    except (binascii.Error, ValueError) as exc:
        raise AgentConfigError("Agent 配置备份内容已损坏") from exc



def _file_snapshots(paths: tuple[Path, ...]) -> tuple[tuple[Path, bool, bytes], ...]:
    return tuple(
        (path, path.exists(), path.read_bytes() if path.exists() else b"")
        for path in paths
    )



def _restore_file_snapshots(snapshots: tuple[tuple[Path, bool, bytes], ...]) -> None:
    for path, existed, content in snapshots:
        if existed:
            _write_atomic(path, content)
        else:
            path.unlink(missing_ok=True)



def _load_backup_state(path: Path) -> dict[str, Any] | None:
    if not path.exists():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return data if isinstance(data, dict) else None


def _backup_mode(state: dict[str, Any]) -> str | None:
    mode = state.get("mode")
    if mode in SUPPORTED_AGENT_MODES:
        return mode
    if state.get("version") == 1:
        return AGENT_MODE_UNIFIED_MODEL
    return None


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


def _validate_mode(mode: str) -> None:
    if mode not in SUPPORTED_AGENT_MODES:
        raise AgentConfigError(f"不支持的 Agent 路由模式: {mode}")
