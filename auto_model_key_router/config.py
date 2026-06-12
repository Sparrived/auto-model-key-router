from __future__ import annotations

import json
import os
import platform
import secrets
from dataclasses import dataclass, field
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
UNIFIED_MODEL_ID = "unified_model"


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
    enabled: bool = True


@dataclass(frozen=True)
class ModelConfig:
    id: str
    keys: tuple[KeyConfig, ...]
    aliases: tuple[str, ...] = ()
    routing_mode: str = "round_robin"
    reasoning_effort: str | None = None


@dataclass(frozen=True)
class UnifiedModelConfig:
    model: str
    key: str | None = None


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
    unified_model: UnifiedModelConfig | None = None
    reasoning_effort_by_model: dict[str, str] = field(init=False, repr=False)

    def __post_init__(self) -> None:
        object.__setattr__(
            self,
            "reasoning_effort_by_model",
            {model.id: model.reasoning_effort for model in self.models if model.reasoning_effort},
        )

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
                    enabled=bool(key.get("enabled", True)),
                )
                for index, key in enumerate(model.get("keys", []))
            )
            aliases = tuple(str(alias) for alias in model.get("aliases", []) if str(alias))
            routing_mode = str(model.get("routing_mode") or default_routing_mode)
            reasoning_effort = str(model.get("reasoning_effort") or "").strip() or None
            if reasoning_effort in {"default", "downstream"}:
                reasoning_effort = None
            models.append(ModelConfig(id=str(model["id"]), keys=keys, aliases=aliases, routing_mode=routing_mode, reasoning_effort=reasoning_effort))

        unified_model = None
        raw_unified_model = raw.get("unified_model")
        if raw_unified_model is not None:
            if not isinstance(raw_unified_model, dict):
                raise ValueError("unified_model 必须是对象")
            target_model = str(raw_unified_model.get("model") or "").strip()
            target_key = str(raw_unified_model.get("key") or "").strip() or None
            if not target_model:
                raise ValueError("unified_model.model 不能为空")
            unified_model = UnifiedModelConfig(model=target_model, key=target_key)

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
            unified_model=unified_model,
        )
        config.validate()
        return config

    def validate(self) -> None:
        model_names: set[str] = set()
        model_ids_by_name: dict[str, str] = {}
        models_by_id: dict[str, ModelConfig] = {}
        for model in self.models:
            if not model.id:
                raise ValueError("模型 id 不能为空")
            if model.routing_mode not in {"priority", "round_robin", "only_first"}:
                raise ValueError(f"模型 {model.id} 的 routing_mode 必须是 priority、round_robin 或 only_first")
            if model.reasoning_effort is not None and model.reasoning_effort not in {"none", "minimal", "low", "medium", "high", "xhigh"}:
                raise ValueError(f"模型 {model.id} 的 reasoning_effort 必须是 none、minimal、low、medium、high 或 xhigh")
            for name in (model.id, *model.aliases):
                if name in model_names:
                    raise ValueError(f"模型名称重复: {name}")
                model_names.add(name)
                model_ids_by_name[name] = model.id
            models_by_id[model.id] = model
            if not model.keys:
                raise ValueError(f"模型 {model.id} 至少需要配置一个 key")
            key_names: set[str] = set()
            for key in model.keys:
                if not key.name:
                    raise ValueError(f"模型 {model.id} 存在空 key name")
                if key.name in key_names:
                    raise ValueError(f"模型 {model.id} 的 key name 重复: {key.name}")
                key_names.add(key.name)
                if not key.api_key:
                    raise ValueError(f"模型 {model.id} 存在空 api_key")
                if not key.base_url.startswith(("http://", "https://")):
                    raise ValueError(f"模型 {model.id} 的 base_url 必须以 http:// 或 https:// 开头")

        if self.unified_model is None:
            return
        if UNIFIED_MODEL_ID in model_names:
            raise ValueError(f"启用 unified_model 时，模型 ID 和别名不能使用保留名称: {UNIFIED_MODEL_ID}")
        target_model_id = model_ids_by_name.get(self.unified_model.model)
        if target_model_id is None:
            raise ValueError(f"unified_model 引用了未配置的模型: {self.unified_model.model}")
        if self.unified_model.key is None:
            return
        target_model = models_by_id[target_model_id]
        if not any(key.name == self.unified_model.key and key.enabled for key in target_model.keys):
            raise ValueError(f"模型 {target_model_id} 未配置可用 key: {self.unified_model.key}")

    def configured_model_id(self, model_name: str) -> str | None:
        for model in self.models:
            if model_name == model.id or model_name in model.aliases:
                return model.id
        return None
