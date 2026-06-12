from __future__ import annotations

import json
import secrets
from pathlib import Path

from .config import RouterConfig


def switch_unified_model(
    config_path: str | Path,
    model_name: str | None = None,
    key_name: str | None = None,
    *,
    update_key: bool = False,
) -> RouterConfig:
    path = Path(config_path)
    current_config = RouterConfig.load(path)
    data = json.loads(path.read_text(encoding="utf-8"))
    current = current_config.unified_model

    if model_name is None:
        if current is None:
            raise ValueError("尚未配置 unified_model，请先使用 --switch-model 选择模型")
        target_model_id = current_config.configured_model_id(current.model)
    else:
        target_model_id = current_config.configured_model_id(model_name.strip())
        if target_model_id is None:
            raise ValueError(f"未配置模型或别名: {model_name}")

    if target_model_id is None:
        raise ValueError("unified_model 当前目标模型无效")

    current_model_id = current_config.configured_model_id(current.model) if current is not None else None
    selected_key = current.key if current is not None else None
    if target_model_id != current_model_id and not update_key:
        selected_key = None
    if update_key:
        selected_key = key_name.strip() if key_name else None

    unified_data = {"model": target_model_id}
    if selected_key:
        unified_data["key"] = selected_key
    data["unified_model"] = unified_data

    updated_config = RouterConfig.from_dict(data)
    _write_json_atomic(path, data)
    return updated_config


def _write_json_atomic(path: Path, data: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_path = path.with_name(f".{path.name}.{secrets.token_hex(6)}.tmp")
    try:
        temporary_path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        temporary_path.replace(path)
    finally:
        try:
            temporary_path.unlink()
        except OSError:
            pass
