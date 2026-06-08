from __future__ import annotations

import json

from auto_model_key_router.update import check_latest_release, github_source_archive_url, is_newer_version, manual_update_command


class FakeResponse:
    def __init__(self, data: dict[str, str]) -> None:
        self.data = data

    def __enter__(self) -> "FakeResponse":
        return self

    def __exit__(self, exc_type: object, exc: object, traceback: object) -> None:
        return None

    def read(self) -> bytes:
        return json.dumps(self.data).encode("utf-8")


def test_is_newer_version_compares_semantic_numbers() -> None:
    assert is_newer_version("1.2.0", "1.1.9")
    assert is_newer_version("v2.0.0", "1.9.9")
    assert not is_newer_version("1.1.1", "1.1.1")
    assert not is_newer_version("1.1.0", "1.1.1")


def test_check_latest_release_reads_github_response(monkeypatch) -> None:
    def fake_urlopen(request: object, timeout: float) -> FakeResponse:
        return FakeResponse({"tag_name": "v9.8.7", "html_url": "https://github.com/example/releases/tag/v9.8.7"})

    monkeypatch.setattr("auto_model_key_router.update.urlopen", fake_urlopen)

    result = check_latest_release(current_version="1.0.0")

    assert result.current_version == "1.0.0"
    assert result.latest_version == "9.8.7"
    assert result.latest_tag == "v9.8.7"
    assert result.release_url == "https://github.com/example/releases/tag/v9.8.7"
    assert result.update_available


def test_manual_update_command_uses_github_archive() -> None:
    archive_url = github_source_archive_url("v1.2.3")
    command = manual_update_command("v1.2.3")

    assert archive_url == "https://github.com/Sparrived/auto-model-key-router/archive/refs/tags/v1.2.3.zip"
    assert command[-1] == archive_url
    assert command[-3:-1] == ["install", "--upgrade"]
