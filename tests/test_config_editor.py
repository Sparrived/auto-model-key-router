from __future__ import annotations

from auto_model_key_router.config_editor import probe_key_capability


class FakeResult:
    def __init__(self, mode: str, available: bool = True) -> None:
        self.mode = mode
        self.available = available
        self.url = "https://vendor.example.test/v1/chat/completions"
        self.duration_ms = 5
        self.error = None if available else "HTTP 403"
        self.status_code = 200 if available else 403


def test_key_probe_discovers_models_and_route_status(monkeypatch) -> None:
    provider = {
        "base_url": "https://vendor.example.test",
        "keys": {
            "main": {"api_key": "sk-main"},
            "other": {"api_key": "sk-other"},
        },
        "routes": {"openai": "custom/v1"},
    }
    monkeypatch.setattr(
        "auto_model_key_router.config_editor.discover_upstream_models_result",
        lambda base_url, api_key, existing, timeout: (["gpt-a", "gpt-b"], None),
    )

    def fake_probe_availability(data, model_id, key, timeout=15.0, modes=None):
        return [FakeResult(mode) for mode in ("openai", "anthropic", "responses")]

    monkeypatch.setattr(
        "auto_model_key_router.config_editor.probe_key_availability",
        fake_probe_availability,
    )

    capabilities = probe_key_capability(provider, "main")

    assert capabilities["models"] == ["gpt-a", "gpt-b"]
    assert capabilities["route_status"] == {
        "openai": "ok",
        "anthropic": "ok",
        "responses": "ok",
    }
    assert capabilities["errors"] == {}
    assert capabilities["checked_at"]


def test_key_probe_restricts_modes_and_keeps_key_errors(monkeypatch) -> None:
    provider = {
        "base_url": "https://vendor.example.test",
        "keys": {"main": {"api_key": "sk-main"}},
        "routes": {},
    }
    monkeypatch.setattr(
        "auto_model_key_router.config_editor.discover_upstream_models_result",
        lambda base_url, api_key, existing, timeout: ([], "HTTP 401"),
    )

    def fake_probe_availability(data, model_id, key, timeout=15.0):
        raise AssertionError("路由探测不应在模型发现失败后执行")

    monkeypatch.setattr(
        "auto_model_key_router.config_editor.probe_key_availability",
        fake_probe_availability,
    )

    capabilities = probe_key_capability(
        provider, "main", modes=["openai", "responses"]
    )

    assert capabilities["models"] == []
    assert capabilities["route_status"] == {}
    assert capabilities["errors"] == {"main": "HTTP 401"}
    assert capabilities["checked_at"]


def test_key_probe_with_mode_restriction_only_checks_selected_modes(monkeypatch) -> None:
    provider = {
        "base_url": "https://vendor.example.test",
        "keys": {"main": {"api_key": "sk-main"}},
        "routes": {},
    }
    monkeypatch.setattr(
        "auto_model_key_router.config_editor.discover_upstream_models_result",
        lambda base_url, api_key, existing, timeout: (["gpt-a"], None),
    )
    def fake_probe_availability(data, model_id, key, timeout=15.0, modes=None):
        return [FakeResult(mode) for mode in ("openai", "anthropic", "responses")]

    monkeypatch.setattr(
        "auto_model_key_router.config_editor.probe_key_availability",
        fake_probe_availability,
    )

    capabilities = probe_key_capability(
        provider, "main", modes=["responses"], timeout=15.0
    )

    assert capabilities["models"] == ["gpt-a"]
    assert capabilities["route_status"] == {"responses": "ok"}
    assert capabilities["checked_at"]


def test_key_probe_requires_base_url_or_key(monkeypatch) -> None:
    provider = {"base_url": "", "keys": {}, "routes": {}}

    capabilities = probe_key_capability(provider, "missing")

    assert capabilities["models"] == []
    assert "provider" in capabilities["errors"]
    assert capabilities["checked_at"]


def test_each_key_probe_returns_its_own_model_list(monkeypatch) -> None:
    """同一供应商的不同 Key 可见模型集不同：各自探测各自缓存。"""
    provider = {
        "base_url": "https://vendor.example.test",
        "keys": {
            "free": {"api_key": "sk-free"},
            "paid": {"api_key": "sk-paid"},
        },
        "routes": {},
    }

    def fake_discover(base_url, api_key, existing, timeout=15.0):
        if api_key == "sk-free":
            return (["gpt-a"], None)
        return (["gpt-a", "gpt-b"], None)

    monkeypatch.setattr(
        "auto_model_key_router.config_editor.discover_upstream_models_result",
        fake_discover,
    )
    monkeypatch.setattr(
        "auto_model_key_router.config_editor.probe_key_availability",
        lambda *args, **kwargs: [FakeResult("openai")],
    )

    free_caps = probe_key_capability(provider, "free")
    paid_caps = probe_key_capability(provider, "paid")

    assert free_caps["models"] == ["gpt-a"]
    assert paid_caps["models"] == ["gpt-a", "gpt-b"]
    assert free_caps["route_status"] == {"openai": "ok"}
    assert paid_caps["errors"] == {}
