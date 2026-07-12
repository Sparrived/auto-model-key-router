from __future__ import annotations

from pathlib import Path

from .config import RouterConfig, load_config_data, save_config_data


def switch_unified_target(
    config_path: str | Path,
    target: str,
    model_name: str | None = None,
    key_name: str | None = None,
    *,
    update_key: bool = False,
) -> RouterConfig:
    if target not in {"default.primary", "default.fallback", "image.primary", "image.fallback"}:
        raise ValueError(f"无效 unified 目标: {target}")
    path = Path(config_path)
    current_config = RouterConfig.load(path)
    data = load_config_data(path)
    current = current_config.unified_model
    plan_name, role = target.split(".")
    current_plan = current.default if current and plan_name == "default" else (current.image if current else None)
    current_target = getattr(current_plan, role) if current_plan else None
    if model_name is None:
        if current_target is None:
            raise ValueError(f"尚未配置 {target}，请先选择模型")
        model_id = current_target.model
    else:
        model_id = current_config.configured_model_id(model_name.strip())
        if model_id is None:
            raise ValueError(f"未配置模型或别名: {model_name}")
    selected_key = current_target.key if current_target else None
    if current_target is None or model_id != current_target.model:
        selected_key = None
    if update_key:
        selected_key = key_name.strip() if key_name else None
    unified = data.setdefault("unified_model", {"default": {}})
    if not isinstance(unified, dict):
        raise ValueError("unified_model 必须是对象")
    plan = unified.setdefault(plan_name, {})
    if not isinstance(plan, dict):
        raise ValueError(f"unified_model.{plan_name} 必须是对象")
    target_data: dict[str, object] = {"model": model_id}
    if selected_key:
        target_data["key"] = selected_key
    plan[role] = target_data
    if plan_name == "image" and role == "fallback" and "primary" not in plan:
        raise ValueError("配置 image.fallback 前必须先配置 image.primary")
    updated_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    return updated_config


def switch_unified_model(
    config_path: str | Path,
    model_name: str | None = None,
    key_name: str | None = None,
    *,
    update_key: bool = False,
    image_model_name: str | None = None,
    image_key_name: str | None = None,
    update_image_key: bool = False,
) -> RouterConfig:
    if image_model_name is None and image_key_name is None:
        return switch_unified_target(
            config_path, "default.primary", model_name, key_name, update_key=update_key
        )
    path = Path(config_path)
    current_config = RouterConfig.load(path)
    data = load_config_data(path)
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

    # 图像模型映射
    selected_image_key = current.image_key if current is not None else None
    if image_model_name is not None:
        target_image_model_id = current_config.configured_model_id(image_model_name.strip())
        if target_image_model_id is None:
            raise ValueError(f"未配置模型或别名: {image_model_name}")
        current_image_model_id = current_config.configured_model_id(current.image_model) if current is not None and current.image_model else None
        if target_image_model_id != current_image_model_id and not update_image_key:
            selected_image_key = None
        if update_image_key:
            selected_image_key = image_key_name.strip() if image_key_name else None
    elif current is not None and current.image_model:
        target_image_model_id = current_config.configured_model_id(current.image_model)
    else:
        target_image_model_id = None

    unified_data: dict[str, object] = {"model": target_model_id}
    if selected_key:
        unified_data["key"] = selected_key
    if target_image_model_id:
        unified_data["image_model"] = target_image_model_id
        if selected_image_key:
            unified_data["image_key"] = selected_image_key
    data["unified_model"] = unified_data

    updated_config = RouterConfig.from_dict(data)
    save_config_data(path, data)
    return updated_config
