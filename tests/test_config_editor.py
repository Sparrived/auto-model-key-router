from __future__ import annotations

from auto_model_key_router.config_editor import probe_provider_capability


def test_provider_probe_discovers_models_and_route_status(monkeypatch) -> None:
    provider = {
        "base_url": "https://vendor.example.test",
        "keys": {
            "main": {"api_key": "sk-main"},
            "disabled": {"api_key": "sk-disabled", "enabled": False},
        },
        "routes": {"openai": "custom/v1"},
    }
    monkeypatch.setattr(
        "auto_model_key_router.config_editor.discover_upstream_models_result",
        lambda base_url, api_key, existing, timeout: (["gpt-a", "gpt-b"], None),
    )

    class Result:
        mode = "openai"
        available = True
        url = "https://vendor.example.test/custom/v1/chat/completions"
        duration_ms = 5
        error = None
        status_code = 200

    monkeypatch.setattr(
        "auto_model_key_router.config_editor.probe_key_availability",
        lambda *args, **kwargs: [Result()],
    )

    capabilities = probe_provider_capability(provider, ["main", "disabled"])

    assert capabilities["models"] == ["gpt-a", "gpt-b"]
    assert capabilities["route_status"] == {"openai": "ok"}
    assert capabilities["errors"] == {}
    assert capabilities["checked_at"]


def test_provider_probe_reports_discovery_error(monkeypatch) -> None:
    provider = {
        "base_url": "https://vendor.example.test",
        "keys": {"main": {"api_key": "sk-main"}},
        "routes": {},
    }
    monkeypatch.setattr(
        "auto_model_key_router.config_editor.discover_upstream_models_result",
        lambda base_url, api_key, existing, timeout: ([], "HTTP 401"),
    )

    capabilities = probe_provider_capability(provider, ["main"])

    assert capabilities["models"] == []
    assert capabilities["errors"] == {"main": "HTTP 401"}
    assert capabilities["checked_at"]


def test_provider_probe_requires_base_url_and_key(monkeypatch) -> None:
    provider = {"base_url": "", "keys": {}, "routes": {}}

    capabilities = probe_provider_capability(provider, [])

    assert capabilities["models"] == []
    assert "provider" in capabilities["errors"]
    assert capabilities["checked_at"]
