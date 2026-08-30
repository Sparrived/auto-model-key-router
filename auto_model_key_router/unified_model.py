from __future__ import annotations

from pathlib import Path

from .config import RouterConfig
from .config_operations import switch_unified_target as apply_unified_target
from .config_service import ConfigService


def switch_unified_target(
    config_path: str | Path,
    target: str,
    model_name: str | None = None,
    key_name: str | None = None,
    *,
    update_key: bool = False,
) -> RouterConfig:
    """Persist one unified-model target through the shared operation layer."""

    path = Path(config_path)

    def mutation(data: dict[str, object]) -> None:
        apply_unified_target(
            data,
            target,
            model_name,
            key_name,
            update_key=update_key,
        )

    return ConfigService(path).update(mutation).new_config


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
    """Compatibility wrapper for the default primary and image primary slots."""

    path = Path(config_path)

    def mutation(data: dict[str, object]) -> None:
        apply_unified_target(
            data,
            "default.primary",
            model_name,
            key_name,
            update_key=update_key,
        )
        if image_model_name is not None or image_key_name is not None:
            apply_unified_target(
                data,
                "image.primary",
                image_model_name,
                image_key_name,
                update_key=update_image_key,
            )

    return ConfigService(path).update(mutation).new_config
