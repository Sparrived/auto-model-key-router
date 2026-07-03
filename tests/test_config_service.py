from __future__ import annotations

import json
from pathlib import Path

import pytest

from auto_model_key_router.config import RouterConfig, migrate_config_data, migrate_config_file
from auto_model_key_router.config_service import ConfigService


def config_data(api_key: str = "sk-a") -> dict[str, object]:
    return {
        "local_api_key": "local-key",
        "models": [
            {
                "id": "model-a",
                "keys": [
                    {
                        "name": "key-a",
                        "api_key": api_key,
                        "base_url": "https://example.test",
                    }
                ],
            }
        ],
    }


def test_update_validates_and_atomically_commits_config(tmp_path: Path) -> None:
    path = tmp_path / "router-config.json"
    path.write_text(json.dumps(config_data()), encoding="utf-8")

    change = ConfigService(path).update(
        lambda data: data["models"][0]["keys"][0].update({"api_key": "sk-b"})
    )

    assert change.old_config.models[0].keys[0].api_key == "sk-a"
    assert change.new_config.models[0].keys[0].api_key == "sk-b"
    assert RouterConfig.load(path).models[0].keys[0].api_key == "sk-b"


def test_invalid_update_leaves_existing_file_unchanged(tmp_path: Path) -> None:
    path = tmp_path / "router-config.json"
    original = config_data()
    path.write_text(json.dumps(original), encoding="utf-8")

    with pytest.raises(ValueError):
        ConfigService(path).update(
            lambda data: data["models"][0]["keys"][0].update({"base_url": "invalid"})
        )

    assert json.loads(path.read_text(encoding="utf-8")) == original


def test_upstream_routes_are_normalized_by_base_url_from_config() -> None:
    config = RouterConfig.from_dict(
        {
            "upstream_routes": {
                "https://example.test/": {
                    "anthropic_messages": "anthropic/",
                    "codex": "responses-v2",
                }
            },
            "models": [
                {
                    "id": "model-a",
                    "keys": [
                        {
                            "name": "key-a",
                            "api_key": "sk-a",
                            "base_url": "https://example.test",
                        }
                    ],
                }
            ]
        }
    )

    assert config.upstream_routes_for_base_url("https://example.test") == {
        "anthropic": "anthropic/v1/messages",
        "responses": "responses-v2/v1/responses",
    }


def test_legacy_key_upstream_routes_are_lifted_to_base_url_config() -> None:
    config = RouterConfig.from_dict(
        {
            "models": [
                {
                    "id": "model-a",
                    "keys": [
                        {
                            "name": "key-a",
                            "api_key": "sk-a",
                            "base_url": "https://example.test",
                            "upstream_routes": {"anthropic": "anthropic/"},
                        }
                    ],
                }
            ]
        }
    )

    assert config.upstream_routes_for_base_url("https://example.test/") == {
        "anthropic": "anthropic/v1/messages",
    }


def test_invalid_upstream_route_base_url_error_includes_value() -> None:
    with pytest.raises(ValueError, match="ftp://bad.example"):
        RouterConfig.from_dict(
            {
                "upstream_routes": {
                    "ftp://bad.example": {"anthropic": "anthropic/"}
                },
                "models": [
                    {
                        "id": "model-a",
                        "keys": [
                            {
                                "name": "key-a",
                                "api_key": "sk-a",
                                "base_url": "https://example.test",
                            }
                        ],
                    }
                ],
            }
        )


def test_legacy_config_migrates_keys_under_providers() -> None:
    migrated = migrate_config_data(
        {
            "local_api_key": "local-key",
            "default_base_url": "https://api.example.test",
            "upstream_routes": {
                "https://api.example.test": {"anthropic": "anthropic/"}
            },
            "models": [
                {
                    "id": "model-a",
                    "aliases": ["alias-a"],
                    "keys": [
                        {
                            "name": "main",
                            "api_key": "sk-a",
                            "base_url": "https://api.example.test",
                            "allow_visitor": True,
                        }
                    ],
                }
            ],
        }
    )

    assert migrated["config_version"] == 3
    assert migrated["providers"] == {
        "default": {
            "base_url": "https://api.example.test",
            "routes": {"anthropic": "anthropic/v1/messages"},
            "keys": {
                "main": {
                    "api_key": "sk-a",
                    "enabled": True,
                    "allow_visitor": True,
                }
            },
            "pools": {"default": {"keys": ["main"]}},
        }
    }
    assert migrated["models"] == {
        "model-a": {
            "aliases": ["alias-a"],
            "targets": [
                {
                    "provider": "default",
                    "pool": "default",
                    "upstream_model": "model-a",
                }
            ],
        }
    }


def test_migrate_config_file_persists_v2_data(tmp_path: Path) -> None:
    path = tmp_path / "router-config.json"
    path.write_text(json.dumps(config_data()), encoding="utf-8")

    migrated = migrate_config_file(path)
    saved = json.loads(path.read_text(encoding="utf-8"))

    assert migrated == saved
    assert saved["config_version"] == 3
    assert saved["providers"]["example-test"]["keys"]["key-a"]["api_key"] == "sk-a"
    assert saved["providers"]["example-test"]["pools"] == {"default": {"keys": ["key-a"]}}
    assert saved["models"]["model-a"]["targets"] == [
        {"provider": "example-test", "pool": "default", "upstream_model": "model-a"}
    ]


def test_provider_pool_targets_parse_to_runtime_models() -> None:
    config = RouterConfig.from_dict(
        {
            "config_version": 3,
            "providers": {
                "vendor": {
                    "base_url": "https://vendor.example.test",
                    "routes": {"responses": "responses-v2"},
                    "keys": {
                        "main": {"api_key": "sk-main", "allow_visitor": True}
                    },
                    "pools": {"premium": {"keys": ["main"]}},
                }
            },
            "models": {
                "local-model": {
                    "aliases": ["local-alias"],
                    "targets": [
                        {
                            "provider": "vendor",
                            "pool": "premium",
                            "upstream_model": "vendor-model",
                        }
                    ],
                }
            },
        }
    )

    model = config.models[0]
    key = model.keys[0]
    assert model.id == "local-model"
    assert model.aliases == ("local-alias",)
    assert key.name == "main"
    assert key.provider == "vendor"
    assert key.upstream_model == "vendor-model"
    assert key.base_url == "https://vendor.example.test"
    assert config.upstream_routes_for_base_url("https://vendor.example.test") == {
        "responses": "responses-v2/v1/responses"
    }


def test_v2_key_targets_migrate_to_single_key_pools() -> None:
    migrated = migrate_config_data(
        {
            "config_version": 2,
            "providers": {
                "vendor": {
                    "base_url": "https://vendor.example.test",
                    "keys": {
                        "main": {"api_key": "sk-main"},
                        "backup": {"api_key": "sk-backup"},
                    },
                }
            },
            "models": {
                "local-model": {
                    "targets": [
                        {"provider": "vendor", "key": "main", "upstream_model": "vendor-model"}
                    ]
                }
            },
        }
    )

    assert migrated["config_version"] == 3
    assert migrated["providers"]["vendor"]["pools"] == {
        "default": {"keys": ["main", "backup"]},
        "main": {"keys": ["main"]},
    }
    assert migrated["models"]["local-model"]["targets"] == [
        {"provider": "vendor", "upstream_model": "vendor-model", "pool": "main"}
    ]
