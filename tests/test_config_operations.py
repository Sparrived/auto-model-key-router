from __future__ import annotations

from auto_model_key_router import config_operations as operations
from auto_model_key_router.config import RouterConfig


def test_provider_operations_allow_an_empty_provider() -> None:
    data = {"config_version": 3, "providers": {}, "models": {}}

    operations.create_provider(data, "empty", "https://empty.example.test")

    assert data["providers"]["empty"] == {
        "base_url": "https://empty.example.test",
        "keys": {},
        "pools": {},
    }
    RouterConfig.from_dict(data)


def test_model_key_deletion_is_local_when_a_pool_is_shared() -> None:
    data = {
        "config_version": 3,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {
                    "main": {"api_key": "sk-main"},
                    "backup": {"api_key": "sk-backup"},
                },
                "pools": {
                    "shared": {
                        "keys": ["main", "backup"],
                        "models": ["upstream"],
                    }
                },
            }
        },
        "models": {
            "first": {
                "targets": [
                    {
                        "provider": "gateway",
                        "pool": "shared",
                        "upstream_model": "upstream",
                    }
                ]
            },
            "second": {
                "targets": [
                    {
                        "provider": "gateway",
                        "pool": "shared",
                        "upstream_model": "upstream",
                    }
                ]
            },
        },
    }

    operations.delete_model_key(data, "first", "shared-main")

    assert set(data["providers"]["gateway"]["keys"]) == {"main", "backup"}
    assert data["models"]["second"]["targets"][0]["provider"] == "gateway"
    RouterConfig.from_dict(data)


def test_clearing_pool_models_removes_models_even_when_they_have_aliases() -> None:
    data = {
        "config_version": 3,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"main": {"api_key": "sk-main"}},
                "pools": {
                    "default": {"keys": ["main"], "models": ["upstream"]}
                },
            }
        },
        "models": {
            "local": {
                "aliases": ["friendly"],
                "targets": [
                    {
                        "provider": "gateway",
                        "pool": "default",
                        "upstream_model": "upstream",
                    }
                ],
            }
        },
    }

    operations.enable_pool_models(data, "gateway", "default", [])

    assert data["models"] == {}


def test_disabling_a_pinned_model_key_clears_the_unified_key() -> None:
    data = {
        "config_version": 3,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"main": {"api_key": "sk-main"}},
                "pools": {
                    "default": {"keys": ["main"], "models": ["upstream"]}
                },
            }
        },
        "models": {
            "local": {
                "targets": [
                    {
                        "provider": "gateway",
                        "pool": "default",
                        "upstream_model": "upstream",
                    }
                ]
            }
        },
        "unified_model": {
            "default": {"primary": {"model": "local", "key": "main"}}
        },
    }

    operations.update_model_key_local(data, "local", "main", enabled=False)

    assert data["unified_model"]["default"]["primary"]["key"] is None
    RouterConfig.from_dict(data)
