from __future__ import annotations

import json
import os
import platform
import secrets
from dataclasses import dataclass
from pathlib import Path
from typing import Any


def default_cache_dir() -> Path:
    system = platform.system().lower()
    if system == "windows":
        root = os.environ.get("LOCALAPPDATA") or os.environ.get("APPDATA")
        if root:
            return Path(root) / "AutoModelKeyRouter"
        return Path.home() / "AppData" / "Local" / "AutoModelKeyRouter"
    if system == "darwin":
        return Path.home() / "Library" / "Caches" / "AutoModelKeyRouter"
    root = os.environ.get("XDG_CACHE_HOME")
    if root:
        return Path(root) / "auto-model-key-router"
    return Path.home() / ".cache" / "auto-model-key-router"


def default_config_path() -> Path:
    return default_cache_dir() / "router-config.json"


LEGACY_CONFIG_PATH = Path("router-config.json")
DEFAULT_CONFIG_PATH = default_config_path()


def default_metrics_db_path() -> str:
    return str(default_cache_dir() / "metrics.sqlite3")


def default_key_state_path() -> str:
    return str(default_cache_dir() / "key-state.json")


def default_log_file_path() -> str:
    return str(default_cache_dir() / "server.log")


def generate_local_api_key() -> str:
    return f"amkr_{secrets.token_urlsafe(32)}"


def empty_config_dict() -> dict[str, Any]:
    return {
        "host": "127.0.0.1",
        "port": 8000,
        "default_base_url": "https://api.openai.com",
        "request_timeout": 60,
        "max_retries": 2,
        "key_failure_threshold": 2,
        "key_cooldown_seconds": 60,
        "key_state_path": default_key_state_path(),
        "upstream_health_check_interval": 30,
        "metrics_db_path": default_metrics_db_path(),
        "log_file_path": default_log_file_path(),
        "local_api_key": generate_local_api_key(),
        "models": [],
    }


def _resolve_config_path(path: str | Path | None = None) -> Path:
    if path is not None:
        return Path(path)
    env_path = os.environ.get("AMKR_CONFIG")
    if env_path:
        return Path(env_path)
    default_path = default_config_path()
    if default_path.exists():
        return default_path
    if LEGACY_CONFIG_PATH.exists():
        default_path.parent.mkdir(parents=True, exist_ok=True)
        default_path.write_text(LEGACY_CONFIG_PATH.read_text(encoding="utf-8"), encoding="utf-8")
        return default_path
    return default_path


@dataclass(frozen=True)
class KeyConfig:
    name: str
    api_key: str
    base_url: str


@dataclass(frozen=True)
class ModelConfig:
    id: str
    keys: tuple[KeyConfig, ...]
    aliases: tuple[str, ...] = ()
    routing_mode: str = "round_robin"
    reasoning_effort: str | None = None


@dataclass(frozen=True)
class RouterConfig:
    host: str
    port: int
    request_timeout: float
    max_retries: int
    key_failure_threshold: int
    key_cooldown_seconds: float
    key_state_path: str
    upstream_health_check_interval: float
    metrics_db_path: str
    log_file_path: str
    local_api_key: str
    models: tuple[ModelConfig, ...]

    @classmethod
    def load(cls, path: str | Path | None = None) -> "RouterConfig":
        config_path = _resolve_config_path(path)
        if not config_path.exists():
            config_path.parent.mkdir(parents=True, exist_ok=True)
            config_path.write_text(json.dumps(empty_config_dict(), indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

        raw = json.loads(config_path.read_text(encoding="utf-8"))
        if not raw.get("local_api_key"):
            raw["local_api_key"] = generate_local_api_key()
            config_path.write_text(json.dumps(raw, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        return cls.from_dict(raw)

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "RouterConfig":
        models: list[ModelConfig] = []
        default_routing_mode = str(raw.get("routing_mode") or "round_robin")
        for model in raw.get("models", []):
            keys = tuple(
                KeyConfig(
                    name=str(key.get("name") or f"{model['id']}-{index + 1}"),
                    api_key=str(key["api_key"]),
                    base_url=str(key.get("base_url") or raw.get("default_base_url") or "https://api.openai.com"),
                )
                for index, key in enumerate(model.get("keys", []))
            )
            aliases = tuple(str(alias) for alias in model.get("aliases", []) if str(alias))
            routing_mode = str(model.get("routing_mode") or default_routing_mode)
            reasoning_effort = str(model.get("reasoning_effort") or "").strip() or None
            models.append(ModelConfig(id=str(model["id"]), keys=keys, aliases=aliases, routing_mode=routing_mode, reasoning_effort=reasoning_effort))

        config = cls(
            host=str(raw.get("host", "127.0.0.1")),
            port=int(raw.get("port", 8000)),
            request_timeout=float(raw.get("request_timeout", 60)),
            max_retries=int(raw.get("max_retries", 2)),
            key_failure_threshold=max(1, int(raw.get("key_failure_threshold", 2))),
            key_cooldown_seconds=max(0.0, float(raw.get("key_cooldown_seconds", 60))),
            key_state_path=str(raw.get("key_state_path") or default_key_state_path()),
            upstream_health_check_interval=max(0.0, float(raw.get("upstream_health_check_interval", 30))),
            metrics_db_path=str(raw.get("metrics_db_path") or default_metrics_db_path()),
            log_file_path=str(raw.get("log_file_path") or default_log_file_path()),
            local_api_key=str(raw.get("local_api_key", "")),
            models=tuple(models),
        )
        config.validate()
        return config

    def validate(self) -> None:
        model_names: set[str] = set()
        for model in self.models:
            if not model.id:
                raise ValueError("模型 id 不能为空")
            if model.routing_mode not in {"priority", "round_robin"}:
                raise ValueError(f"模型 {model.id} 的 routing_mode 必须是 priority 或 round_robin")
            if model.reasoning_effort is not None and model.reasoning_effort not in {"minimal", "low", "medium", "high"}:
                raise ValueError(f"模型 {model.id} 的 reasoning_effort 必须是 minimal、low、medium 或 high")
            for name in (model.id, *model.aliases):
                if name in model_names:
                    raise ValueError(f"模型名称重复: {name}")
                model_names.add(name)
            if not model.keys:
                raise ValueError(f"模型 {model.id} 至少需要配置一个 key")
            for key in model.keys:
                if not key.api_key:
                    raise ValueError(f"模型 {model.id} 存在空 api_key")
                if not key.base_url.startswith(("http://", "https://")):
                    raise ValueError(f"模型 {model.id} 的 base_url 必须以 http:// 或 https:// 开头")
