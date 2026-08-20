from __future__ import annotations

import tomllib
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def project_metadata() -> dict:
    return tomllib.loads((ROOT / "pyproject.toml").read_text(encoding="utf-8"))


def test_console_scripts_expose_both_supported_command_names() -> None:
    scripts = project_metadata()["project"]["scripts"]

    assert scripts["amkr"] == "auto_model_key_router.main:main"
    assert scripts["auto-model-key-router"] == "auto_model_key_router.main:main"


def test_package_discovery_includes_runtime_package() -> None:
    package_find = project_metadata()["tool"]["setuptools"]["packages"]["find"]

    assert "auto_model_key_router*" in package_find["include"]
    assert "build*" in package_find["exclude"]
    assert ".venv*" in package_find["exclude"]
