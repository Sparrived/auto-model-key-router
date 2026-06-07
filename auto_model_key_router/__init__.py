from __future__ import annotations

from importlib.metadata import PackageNotFoundError, version
from pathlib import Path


pyproject = Path(__file__).resolve().parents[1] / "pyproject.toml"
if pyproject.exists():
    import tomllib

    __version__ = tomllib.loads(pyproject.read_text(encoding="utf-8"))["project"]["version"]
else:
    try:
        __version__ = version("auto-model-key-router")
    except PackageNotFoundError:
        __version__ = "0.0.0"
