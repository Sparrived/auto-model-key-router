from __future__ import annotations

import json
import subprocess

from auto_model_key_router.update import VersionCheckResult, check_latest_release, check_latest_version, github_source_archive_url, install_latest_version, is_newer_version, manual_update_command, update_output_preview


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


def test_check_latest_version_prefers_pypi(monkeypatch) -> None:
    def fake_urlopen(request: object, timeout: float) -> FakeResponse:
        return FakeResponse({"info": {"version": "9.8.7", "package_url": "https://pypi.org/project/auto-model-key-router/9.8.7/"}})

    monkeypatch.setattr("auto_model_key_router.update.urlopen", fake_urlopen)

    result = check_latest_version(current_version="1.0.0")

    assert result.current_version == "1.0.0"
    assert result.latest_version == "9.8.7"
    assert result.latest_tag is None
    assert result.release_url == "https://pypi.org/project/auto-model-key-router/9.8.7/"
    assert result.source == "PyPI"
    assert result.update_available


def test_check_latest_version_falls_back_to_github(monkeypatch) -> None:
    calls: list[str] = []

    def fake_urlopen(request: object, timeout: float) -> FakeResponse:
        url = str(getattr(request, "full_url", ""))
        calls.append(url)
        if "pypi.org" in url:
            raise OSError("pypi unavailable")
        return FakeResponse({"tag_name": "v9.8.7", "html_url": "https://github.com/example/releases/tag/v9.8.7"})

    monkeypatch.setattr("auto_model_key_router.update.urlopen", fake_urlopen)

    result = check_latest_version(current_version="1.0.0")

    assert calls == ["https://pypi.org/pypi/auto-model-key-router/json", "https://api.github.com/repos/Sparrived/auto-model-key-router/releases/latest"]
    assert result.latest_version == "9.8.7"
    assert result.latest_tag == "v9.8.7"
    assert result.source == "GitHub"
    assert result.fallback_error == "pypi unavailable"
    assert result.update_available


def test_manual_update_command_uses_github_archive() -> None:
    archive_url = github_source_archive_url("v1.2.3")
    command = manual_update_command(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", latest_tag="v1.2.3", source="GitHub"))

    assert archive_url == "https://github.com/Sparrived/auto-model-key-router/archive/refs/tags/v1.2.3.zip"
    assert command[-1] == archive_url
    assert command[-3:-1] == ["install", "--upgrade"]


def test_manual_update_command_uses_pypi_package() -> None:
    command = manual_update_command(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"))

    assert command[-1] == "auto-model-key-router"
    assert command[-3:-1] == ["install", "--upgrade"]


def test_install_latest_version_shows_stderr_when_stdout_exists(monkeypatch, tmp_path) -> None:
    result = subprocess.CompletedProcess(["python", "-m", "pip"], 1, stdout="Looking in indexes", stderr="ERROR: network timeout")
    monkeypatch.setattr("auto_model_key_router.update.subprocess.run", lambda *args, **kwargs: result)
    monkeypatch.setattr("auto_model_key_router.update.update_log_path", lambda: tmp_path / "update.log")

    panel = install_latest_version(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"))
    text = str(panel.renderable)

    assert "Looking in indexes" in text
    assert "ERROR: network timeout" in text
    assert str(tmp_path / "update.log") in text
    assert "ERROR: network timeout" in (tmp_path / "update.log").read_text(encoding="utf-8")


def test_update_output_preview_keeps_tail_for_long_logs() -> None:
    output = "\n".join(f"line-{index}" for index in range(200))

    preview, truncated = update_output_preview(output, line_limit=3, char_limit=100)

    assert truncated
    assert preview == "line-197\nline-198\nline-199"
