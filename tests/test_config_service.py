from __future__ import annotations

import json
from pathlib import Path

import pytest

from auto_model_key_router.config import RouterConfig
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
