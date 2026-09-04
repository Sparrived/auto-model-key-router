from __future__ import annotations

import json
from pathlib import Path

import pytest

from auto_model_key_router.config import RouterConfig
from auto_model_key_router.config_service import ConfigService


def config_data(api_key: str = "sk-a") -> dict[str, object]:
    return {
        "config_version": 3,
        "local_api_key": "local-key",
        "providers": {
            "example": {
                "base_url": "https://example.test",
                "keys": {"key-a": {"api_key": api_key}},
                "pools": {"default": {"keys": ["key-a"], "models": ["model-a"]}},
            }
        },
        "models": {
            "model-a": {
                "targets": [
                    {
                        "provider": "example",
                        "pool": "default",
                        "upstream_model": "model-a",
                    }
                ]
            }
        },
    }


def test_stream_timeouts_have_defaults_and_accept_custom_values() -> None:
    defaults = RouterConfig.from_dict(config_data())
    custom_data = config_data()
    custom_data["stream_first_byte_timeout"] = 12.5
    custom_data["stream_idle_timeout"] = 34.5

    custom = RouterConfig.from_dict(custom_data)

    assert defaults.stream_first_byte_timeout == 90
    assert defaults.stream_idle_timeout == 180
    assert custom.stream_first_byte_timeout == 12.5
    assert custom.stream_idle_timeout == 34.5


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("stream_first_byte_timeout", 0),
        ("stream_first_byte_timeout", -1),
        ("stream_idle_timeout", 0),
        ("stream_idle_timeout", -1),
    ],
)
def test_stream_timeouts_must_be_positive(field: str, value: float) -> None:
    data = config_data()
    data[field] = value

    with pytest.raises(ValueError, match=field):
        RouterConfig.from_dict(data)


def test_reasoning_effort_accepts_max() -> None:
    data = config_data()
    data["models"]["model-a"]["reasoning_effort"] = "max"

    config = RouterConfig.from_dict(data)

    assert config.models[0].reasoning_effort == "max"


def test_update_validates_and_atomically_commits_config(tmp_path: Path) -> None:
    path = tmp_path / "router-config.json"
    path.write_text(json.dumps(config_data()), encoding="utf-8")

    change = ConfigService(path).update(
        lambda data: data["providers"]["example"]["keys"]["key-a"].update({"api_key": "sk-b"})
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
            lambda data: data["providers"]["example"]["pools"]["default"].update({"keys": ["missing"]})
        )

    assert json.loads(path.read_text(encoding="utf-8")) == original


def test_runtime_migrates_legacy_list_model_config(tmp_path: Path) -> None:
    legacy = config_data()
    legacy.pop("config_version")
    legacy["models"] = [
        {
            "id": "model-a",
            "aliases": ["alias-a"],
            "keys": [
                {
                    "name": "key-a",
                    "api_key": "sk-a",
                    "base_url": "https://example.test",
                    "allow_visitor": True,
                }
            ],
        }
    ]

    path = tmp_path / "router-config.json"
    path.write_text(json.dumps(legacy), encoding="utf-8")

    config = RouterConfig.load(path)
    saved = json.loads(path.read_text(encoding="utf-8"))

    assert config.models[0].id == "model-a"
    assert config.models[0].aliases == ("alias-a",)
    assert config.models[0].keys[0].name == "key-a"
    assert config.models[0].keys[0].api_key == "sk-a"
    assert config.models[0].keys[0].allow_visitor is True
    assert saved["config_version"] == 4
    assert saved["providers"]["example.test"]["keys"]["key-a"]["api_key"] == "sk-a"
    assert saved["models"]["model-a"]["targets"] == [
        {"provider": "example.test", "key": "key-a", "upstream_model": "model-a"}
    ]


def test_legacy_key_state_path_maps_to_endpoint_capability_cache() -> None:
    data = config_data()
    data["key_state_path"] = "legacy-state.json"

    config = RouterConfig.from_dict(data)

    assert config.endpoint_capabilities_path == "legacy-state.json"


def test_legacy_key_state_path_is_rewritten_on_commit(tmp_path: Path) -> None:
    path = tmp_path / "router-config.json"
    data = config_data()
    data["key_state_path"] = "legacy-state.json"
    path.write_text(json.dumps(data), encoding="utf-8")

    ConfigService(path).commit(data, old_config=RouterConfig.from_dict(data))
    saved = json.loads(path.read_text(encoding="utf-8"))

    assert saved["endpoint_capabilities_path"] == "legacy-state.json"
    assert "key_state_path" not in saved


def test_upstream_routes_are_normalized_by_base_url_from_config() -> None:
    config = RouterConfig.from_dict(
        {
            "config_version": 3,
            "providers": {
                "example": {
                    "base_url": "https://example.test/",
                    "routes": {"anthropic_messages": "anthropic/", "codex": "responses-v2"},
                    "keys": {"key-a": {"api_key": "sk-a"}},
                    "pools": {"default": {"keys": ["key-a"]}},
                }
            },
            "models": {
                "model-a": {
                    "targets": [{"provider": "example", "pool": "default"}],
                }
            },
        }
    )

    assert config.upstream_routes_for_base_url("https://example.test") == {
        "anthropic": "anthropic/v1/messages",
        "responses": "responses-v2/v1/responses",
    }


def test_invalid_upstream_route_base_url_error_includes_value() -> None:
    with pytest.raises(ValueError, match="ftp://bad.example"):
        RouterConfig.from_dict(
            {
                "config_version": 3,
                "providers": {
                    "bad": {
                        "base_url": "ftp://bad.example",
                        "routes": {"anthropic": "anthropic/"},
                        "keys": {"key-a": {"api_key": "sk-a"}},
                        "pools": {"default": {"keys": ["key-a"]}},
                    }
                },
                "models": {
                    "model-a": {
                        "targets": [{"provider": "bad", "pool": "default"}],
                    }
                },
            }
        )


def test_provider_key_targets_parse_to_runtime_models() -> None:
    config = RouterConfig.from_dict(
        {
            "config_version": 4,
            "providers": {
                "vendor": {
                    "base_url": "https://vendor.example.test",
                    "routes": {"responses": "responses-v2"},
                    "keys": {
                        "main": {"api_key": "sk-main", "allow_visitor": True}
                    },
                }
            },
            "models": {
                "local-model": {
                    "aliases": ["local-alias"],
                    "targets": [
                        {
                            "provider": "vendor",
                            "key": "main",
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


def test_model_with_multiple_key_targets_routes_all_keys() -> None:
    config = RouterConfig.from_dict(
        {
            "config_version": 4,
            "providers": {
                "vendor": {
                    "base_url": "https://vendor.example.test",
                    "keys": {
                        "pool-a-key": {"api_key": "sk-a"},
                        "pool-b-key": {"api_key": "sk-b"},
                    },
                }
            },
            "models": {
                "gpt-5.6": {
                    "targets": [
                        {"provider": "vendor", "key": "pool-a-key", "upstream_model": "gpt-5.6"},
                        {"provider": "vendor", "key": "pool-b-key", "upstream_model": "gpt-5.6"},
                    ]
                }
            },
        }
    )

    assert [key.name for key in config.models[0].keys] == ["pool-a-key", "pool-b-key"]


@pytest.mark.parametrize("targets", [[], None])
def test_model_without_key_targets_produces_no_route_keys(targets: object) -> None:
    model: dict[str, object] = {}
    if targets is not None:
        model["targets"] = targets
    config = RouterConfig.from_dict(
        {
            "config_version": 4,
            "providers": {
                "vendor": {
                    "base_url": "https://vendor.example.test",
                    "keys": {"main": {"api_key": "sk-main"}},
                }
            },
            "models": {"gpt-5.6": model},
        }
    )

    assert config.models[0].keys == ()


def test_models_may_share_the_same_provider_key() -> None:
    config = RouterConfig.from_dict(
        {
            "config_version": 4,
            "providers": {
                "vendor": {
                    "base_url": "https://vendor.example.test",
                    "keys": {"main": {"api_key": "sk-main"}},
                }
            },
            "models": {
                "gpt-5.5": {
                    "targets": [{"provider": "vendor", "key": "main", "upstream_model": "gpt-5.5"}]
                },
                "gpt-5.6": {
                    "targets": [{"provider": "vendor", "key": "main", "upstream_model": "gpt-5.6"}]
                },
            },
        }
    )

    assert [key.name for key in config.models[0].keys] == ["main"]
    assert [key.name for key in config.models[1].keys] == ["main"]
    assert config.providers[0].keys[0].name == "main"


def test_provider_key_referenced_by_model_target_is_required() -> None:
    with pytest.raises(ValueError, match=r"供应商 vendor 不存在的 key"):
        RouterConfig.from_dict(
            {
                "config_version": 4,
                "providers": {
                    "vendor": {
                        "base_url": "https://vendor.example.test",
                        "keys": {"main": {"api_key": "sk-main"}},
                    }
                },
                "models": {
                    "local-model": {
                        "targets": [
                            {"provider": "vendor", "key": "missing", "upstream_model": "vendor-model"}
                        ]
                    }
                },
            }
        )


def test_provider_keys_need_no_pool_assignment_in_v4() -> None:
    config = RouterConfig.from_dict(
        {
            "config_version": 4,
            "providers": {
                "vendor": {
                    "base_url": "https://vendor.example.test",
                    "keys": {"main": {"api_key": "sk-main"}},
                }
            },
            "models": {},
        }
    )

    assert [key.name for key in config.providers[0].keys] == ["main"]
    assert config.models == ()


def test_v4_model_named_local_models_parse_without_pools() -> None:
    data = {
        "config_version": 4,
        "providers": {
            "vendor": {
                "base_url": "https://vendor.example",
                "keys": {"main": {"api_key": "sk-main"}},
            }
        },
        "models": {
            "gpt-5.5": {"targets": [{"provider": "vendor", "key": "main", "upstream_model": "gpt-5.5"}]},
            "codex-auto-review": {"targets": [{"provider": "vendor", "key": "main", "upstream_model": "codex-auto-review"}]},
        },
    }

    config = RouterConfig.from_dict(data)

    assert config.providers[0].id == "vendor"
    assert config.providers[0].keys[0].name == "main"
    assert [model.id for model in config.models] == ["gpt-5.5", "codex-auto-review"]
    assert all(
        key.provider == "vendor" and key.pool is None
        for model in config.models
        for key in model.keys
    )


def test_v3_config_migrates_on_disk_to_v4_key_targets(tmp_path: Path) -> None:
    v3 = {
        "config_version": 3,
        "local_api_key": "local-key",
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
                        "models": ["gpt-a", "gpt-b"],
                        "available_models": ["gpt-a", "gpt-b", "gpt-c"],
                        "checked_at": "2026-08-01T00:00:00+00:00",
                    }
                },
            }
        },
        "models": {
            "gpt-a": {
                "targets": [
                    {"provider": "gateway", "pool": "shared", "upstream_model": "gpt-a"}
                ]
            },
            "gpt-b": {
                "targets": [
                    {"provider": "gateway", "pool": "shared", "upstream_model": "gpt-b"}
                ]
            },
        },
    }
    path = tmp_path / "router-config.json"
    path.write_text(json.dumps(v3), encoding="utf-8")

    config = RouterConfig.load(path)
    saved = json.loads(path.read_text(encoding="utf-8"))

    assert saved["config_version"] == 4
    assert "pools" not in saved["providers"]["gateway"]
    assert saved["providers"]["gateway"]["keys"]["main"]["api_key"] == "sk-main"
    # v3 池探测元数据合并进 v4 供应商级 capabilities。
    caps = saved["providers"]["gateway"]["capabilities"]
    assert set(caps["models"]) == {"gpt-a", "gpt-b", "gpt-c"}
    assert caps["checked_at"] == "2026-08-01T00:00:00+00:00"
    # 每个模型 target 展开为该池全部 key 的 key 级 target。
    assert saved["models"]["gpt-a"]["targets"] == [
        {"provider": "gateway", "key": "main", "upstream_model": "gpt-a"},
        {"provider": "gateway", "key": "backup", "upstream_model": "gpt-a"},
    ]
    assert [key.name for key in config.models[0].keys] == ["main", "backup"]
    assert all(key.provider == "gateway" for key in config.models[0].keys)


def test_v3_pool_whitelist_filters_models_on_migration(tmp_path: Path) -> None:
    v3 = {
        "config_version": 3,
        "providers": {
            "gateway": {
                "base_url": "https://gateway.example.test",
                "keys": {"main": {"api_key": "sk-main"}},
                "pools": {"only-a": {"keys": ["main"], "models": ["gpt-a"]}},
            }
        },
        "models": {
            "gpt-a": {
                "targets": [
                    {"provider": "gateway", "pool": "only-a", "upstream_model": "gpt-a"}
                ]
            },
            "gpt-b": {
                "targets": [
                    {"provider": "gateway", "pool": "only-a", "upstream_model": "gpt-b"}
                ]
            },
        },
    }
    path = tmp_path / "router-config.json"
    path.write_text(json.dumps(v3), encoding="utf-8")

    config = RouterConfig.load(path)
    saved = json.loads(path.read_text(encoding="utf-8"))

    # gpt-b 不在池启用白名单，迁移时不生成 key target，模型保留为空。
    assert saved["models"]["gpt-b"]["targets"] == []
    assert saved["models"]["gpt-a"]["targets"] == [
        {"provider": "gateway", "key": "main", "upstream_model": "gpt-a"}
    ]

