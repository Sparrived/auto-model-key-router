from __future__ import annotations

from copy import deepcopy
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from .config import RouterConfig, load_config_data, migrate_config_data, save_config_data


@dataclass(frozen=True)
class ConfigChange:
    path: Path
    old_config: RouterConfig
    new_config: RouterConfig
    data: dict[str, Any]


class ConfigService:
    def __init__(self, path: str | Path) -> None:
        self.path = Path(path)

    def commit(
        self,
        data: dict[str, Any],
        *,
        old_config: RouterConfig | None = None,
    ) -> ConfigChange:
        previous = old_config or RouterConfig.load(self.path)
        migrated = migrate_config_data(data)
        new_config = RouterConfig.from_dict(migrated)
        save_config_data(self.path, migrated)
        return ConfigChange(self.path, previous, new_config, deepcopy(migrated))

    def update(
        self,
        mutation: Callable[[dict[str, Any]], None],
    ) -> ConfigChange:
        data = load_config_data(self.path)
        old_config = RouterConfig.from_dict(data)
        updated = deepcopy(data)
        mutation(updated)
        return self.commit(updated, old_config=old_config)


def commit_config_data(
    path: str | Path,
    data: dict[str, Any],
    old_config: RouterConfig,
) -> ConfigChange:
    return ConfigService(path).commit(data, old_config=old_config)

