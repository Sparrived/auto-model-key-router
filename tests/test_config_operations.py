from __future__ import annotations

from auto_model_key_router import config_operations as operations
from auto_model_key_router.config import RouterConfig


def test_provider_operations_allow_an_empty_provider() -> None:
    data = {"config_version": 4, "providers": {}, "models": {}}

    operations.create_provider(data, "empty", "https://empty.example.test")

    assert data["providers"]["empty"] == {
        "base_url": "https://empty.example.test",
        "keys": {},
    }
    RouterConfig.from_dict(data)


def test_delete_model_key_is_local_when_a_key_is_shared() -> None:
    data = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {
                    "main": {"api_key": "sk-main"},
                    "backup": {"api_key": "sk-backup"},
                },
            }
        },
        "models": {
            "first": {
                "targets": [
                    {
                        "provider": "gateway",
                        "key": "main",
                        "upstream_model": "upstream",
                    },
                    {
                        "provider": "gateway",
                        "key": "backup",
                        "upstream_model": "upstream",
                    },
                ]
            },
            "second": {
                "targets": [
                    {
                        "provider": "gateway",
                        "key": "main",
                        "upstream_model": "upstream",
                    }
                ]
            },
        },
    }

    # Removing the binding from "first" must not delete the shared provider key.
    operations.delete_model_key(data, "first", "gateway-main")

    assert set(data["providers"]["gateway"]["keys"]) == {"main", "backup"}
    assert data["models"]["second"]["targets"][0]["provider"] == "gateway"
    assert data["models"]["second"]["targets"][0]["key"] == "main"
    # "first" still routes through its remaining backup key.
    assert data["models"]["first"]["targets"] == [
        {
            "provider": "gateway",
            "key": "backup",
            "upstream_model": "upstream",
        }
    ]
    RouterConfig.from_dict(data)


def test_clearing_all_model_keys_removes_model_even_when_it_has_aliases() -> None:
    data = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"main": {"api_key": "sk-main"}},
            }
        },
        "models": {
            "local": {
                "aliases": ["friendly"],
                "targets": [
                    {
                        "provider": "gateway",
                        "key": "main",
                        "upstream_model": "upstream",
                    }
                ],
            }
        },
    }

    operations.delete_model_key_local(data, "local", "main")

    assert data["models"] == {}
    assert data["providers"] == {}


def test_deleting_last_key_referencing_provider_key_removes_provider_key() -> None:
    data = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"main": {"api_key": "sk-main"}},
            }
        },
        "models": {
            "local": {
                "targets": [
                    {
                        "provider": "gateway",
                        "key": "main",
                        "upstream_model": "upstream",
                    }
                ]
            }
        },
    }

    operations.delete_model_key(data, "local", "main")

    assert data["models"] == {}
    assert data["providers"] == {}


def test_disabling_a_pinned_model_key_clears_the_unified_key() -> None:
    data = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"main": {"api_key": "sk-main"}},
            }
        },
        "models": {
            "local": {
                "targets": [
                    {
                        "provider": "gateway",
                        "key": "main",
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


def test_merge_transferable_config_skips_targets_bound_to_duplicate_secret_keys() -> None:
    current = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"existing": {"api_key": "sk-shared"}},
            }
        },
        "models": {
            "local": {
                "targets": [
                    {
                        "provider": "gateway",
                        "key": "existing",
                        "upstream_model": "upstream",
                    }
                ]
            }
        },
    }
    transfer = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {
                    # 与现有 key secret 重复 → 跳过不导入。
                    "existing": {"api_key": "sk-shared"},
                    "fresh": {"api_key": "sk-fresh"},
                },
            }
        },
        "models": {
            "remote-a": {
                "targets": [
                    {
                        "provider": "gateway",
                        "key": "existing",
                        "upstream_model": "remote-a",
                    },
                    {
                        "provider": "gateway",
                        "key": "fresh",
                        "upstream_model": "remote-a",
                    },
                ]
            }
        },
    }

    merged, added_models, added_keys, skipped_keys = operations.merge_transferable_config(
        current, transfer
    )

    assert added_models == 1
    assert added_keys == 1
    assert skipped_keys == 1
    # 绑定到被跳过 key 的 target 一并过滤，避免引用不存在的 key。
    assert merged["models"]["remote-a"]["targets"] == [
        {"provider": "gateway", "key": "fresh", "upstream_model": "remote-a"}
    ]
    RouterConfig.from_dict(merged)


def test_merge_transferable_config_renames_target_key_when_source_key_is_renamed() -> None:
    current = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"main": {"api_key": "sk-main"}},
            }
        },
        "models": {},
    }
    transfer = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {
                    # 重名但 secret 不同 → 追加为 main-2。
                    "main": {"api_key": "sk-other"},
                },
            }
        },
        "models": {
            "remote-a": {
                "targets": [
                    {
                        "provider": "gateway",
                        "key": "main",
                        "upstream_model": "remote-a",
                    }
                ]
            }
        },
    }

    merged, added_models, added_keys, skipped_keys = operations.merge_transferable_config(
        current, transfer
    )

    assert added_models == 1
    assert added_keys == 1
    assert skipped_keys == 0
    assert merged["providers"]["gateway"]["keys"]["main-2"]["api_key"] == "sk-other"
    assert merged["models"]["remote-a"]["targets"] == [
        {"provider": "gateway", "key": "main-2", "upstream_model": "remote-a"}
    ]
    RouterConfig.from_dict(merged)


def test_updating_provider_key_api_key_drops_stale_capabilities() -> None:
    data = {
        "config_version": 4,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {
                    "main": {
                        "api_key": "sk-old",
                        "capabilities": {
                            "models": ["gpt-a"],
                            "route_status": {"openai": "ok"},
                            "errors": {},
                            "checked_at": "2026-09-01T00:00:00+00:00",
                        },
                    }
                },
            }
        },
        "models": {},
    }

    # 换 API Key：探测缓存属于旧凭据，必须失效。
    operations.update_provider_key(data, "gateway", "main", api_key="sk-new")
    assert data["providers"]["gateway"]["keys"]["main"]["api_key"] == "sk-new"
    assert "capabilities" not in data["providers"]["gateway"]["keys"]["main"]

    # 重新探测后，改名/禁用不应丢新缓存。
    key = data["providers"]["gateway"]["keys"]["main"]
    key["capabilities"] = {"models": ["gpt-b"], "checked_at": "2026-09-02T00:00:00+00:00"}
    operations.update_provider_key(data, "gateway", "main", new_name="renamed", enabled=False)
    renamed = data["providers"]["gateway"]["keys"]["renamed"]
    assert renamed["capabilities"]["models"] == ["gpt-b"]
    assert renamed["enabled"] is False
    RouterConfig.from_dict(data)
