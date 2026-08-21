from __future__ import annotations

import json
import subprocess
import sys

from rich.console import Console

from auto_model_key_router.update import VersionCheckResult, check_latest_pypi, check_latest_release, check_latest_version, detected_installation_method, github_source_archive_url, install_latest_version, install_latest_version_outcome, is_newer_version, manual_update_command, post_update_commands, should_use_windows_update_helper, start_windows_update_helper, update_output_preview, windows_update_helper_script


class FakeResponse:
    def __init__(self, data: dict[str, str]) -> None:
        self.data = data

    def __enter__(self) -> "FakeResponse":
        return self

    def __exit__(self, exc_type: object, exc: object, traceback: object) -> None:
        return None

    def read(self) -> bytes:
        return json.dumps(self.data).encode("utf-8")


def render_text(renderable: object) -> str:
    with Console(record=True, width=180) as console:
        console.print(renderable)
    return console.export_text()


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
        return FakeResponse({
            "info": {
                "version": "9.8.7",
                "package_url": "https://pypi.org/project/auto-model-key-router/",
                "release_url": "https://pypi.org/project/auto-model-key-router/9.8.7/",
            },
            "urls": [{
                "filename": "auto_model_key_router-9.8.7-py3-none-any.whl",
                "packagetype": "bdist_wheel",
                "url": "https://files.pythonhosted.org/packages/amkr.whl",
                "digests": {"sha256": "a" * 64},
            }],
        })

    monkeypatch.setattr("auto_model_key_router.update.urlopen", fake_urlopen)

    result = check_latest_version(current_version="1.0.0")

    assert result.current_version == "1.0.0"
    assert result.latest_version == "9.8.7"
    assert result.latest_tag is None
    assert result.release_url == "https://pypi.org/project/auto-model-key-router/9.8.7/"
    assert result.source == "PyPI"
    assert result.artifact_url == "https://files.pythonhosted.org/packages/amkr.whl"
    assert result.artifact_sha256 == "a" * 64
    assert result.update_available


def test_check_latest_pypi_builds_version_specific_release_url_when_metadata_omits_it(monkeypatch) -> None:
    def fake_urlopen(request: object, timeout: float) -> FakeResponse:
        return FakeResponse({
            "info": {
                "version": "9.8.7",
                "package_url": "https://pypi.org/project/auto-model-key-router/",
            },
            "urls": [],
        })

    monkeypatch.setattr("auto_model_key_router.update.urlopen", fake_urlopen)

    result = check_latest_pypi(current_version="1.0.0")

    assert result.release_url == "https://pypi.org/project/auto-model-key-router/9.8.7/"


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


def test_manual_update_command_uses_github_archive(monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.update.detected_installation_method", lambda: "pip")
    archive_url = github_source_archive_url("v1.2.3")
    command = manual_update_command(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", latest_tag="v1.2.3", source="GitHub"))

    assert archive_url == "https://github.com/Sparrived/auto-model-key-router/archive/refs/tags/v1.2.3.zip"
    assert command[-1] == archive_url
    assert command[-3:-1] == ["install", "--upgrade"]


def test_manual_update_command_uses_pypi_package(monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.update.detected_installation_method", lambda: "pip")
    command = manual_update_command(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"))

    assert command[-1] == "auto-model-key-router"
    assert command[-3:-1] == ["install", "--upgrade"]


def test_detected_installation_method_reads_pipx_metadata(tmp_path) -> None:
    prefix = tmp_path / "venv"
    prefix.mkdir()
    (prefix / "pipx_metadata.json").write_text("{}", encoding="utf-8")

    assert detected_installation_method(prefix) == "pipx"


def test_manual_update_command_uses_pipx_upgrade(monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.update.detected_installation_method", lambda: "pipx")

    command = manual_update_command(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"))

    assert command == ["pipx", "upgrade", "auto-model-key-router"]


def test_manual_update_command_uses_uv_tool_upgrade(monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.update.detected_installation_method", lambda: "uv-tool")

    command = manual_update_command(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"))

    assert command == ["uv", "tool", "upgrade", "auto-model-key-router"]


def test_manual_update_command_uses_uv_tool_install_for_uvx(monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.update.detected_installation_method", lambda: "uvx")

    command = manual_update_command(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"))

    assert command == ["uv", "tool", "install", "--force", "auto-model-key-router"]


def test_detected_installation_method_reads_uv_tool_dir(tmp_path, monkeypatch) -> None:
    prefix = tmp_path / "tools" / "auto-model-key-router"
    prefix.mkdir(parents=True)
    monkeypatch.setenv("UV_TOOL_DIR", str(tmp_path / "tools"))

    assert detected_installation_method(prefix) == "uv-tool"


def test_detected_installation_method_reads_windows_uv_tool_default(tmp_path, monkeypatch) -> None:
    prefix = tmp_path / "Roaming" / "uv" / "tools" / "auto-model-key-router"
    prefix.mkdir(parents=True)
    monkeypatch.delenv("UV_TOOL_DIR", raising=False)
    monkeypatch.setenv("APPDATA", str(tmp_path / "Roaming"))
    monkeypatch.setattr("auto_model_key_router.update.os.name", "nt")

    assert detected_installation_method(prefix) == "uv-tool"


def test_detected_installation_method_reads_uv_cache_dir(tmp_path, monkeypatch) -> None:
    prefix = tmp_path / "uv-cache" / "archive-v0" / "tool"
    prefix.mkdir(parents=True)
    monkeypatch.setenv("UV_CACHE_DIR", str(tmp_path / "uv-cache"))

    assert detected_installation_method(prefix) == "uvx"


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


def test_install_latest_version_restarts_service_after_success(monkeypatch, tmp_path) -> None:
    result = subprocess.CompletedProcess(["python", "-m", "pip"], 0, stdout="updated", stderr="")
    restarted: list[str] = []
    monkeypatch.setattr("auto_model_key_router.update.subprocess.run", lambda *args, **kwargs: result)
    monkeypatch.setattr("auto_model_key_router.update.update_log_path", lambda: tmp_path / "update.log")
    monkeypatch.setattr("auto_model_key_router.update.restart_service_after_update", lambda config_path: restarted.append(str(config_path)) or "service restarted")

    outcome = install_latest_version_outcome(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"), tmp_path / "config.json", restart_tui=True)
    text = render_text(outcome.content)

    assert outcome.updated
    assert not outcome.handoff
    assert restarted == [str(tmp_path / "config.json")]
    assert "Terminal UI 将重新启动" in text
    assert "service restarted" in text


def test_should_use_windows_update_helper_for_console_script(monkeypatch) -> None:
    monkeypatch.setattr("auto_model_key_router.update.os.name", "nt")
    monkeypatch.setattr(sys, "argv", ["C:\\Users\\Sparr\\.local\\bin\\amkr.exe"])

    assert should_use_windows_update_helper(["uv", "tool", "upgrade", "auto-model-key-router"])


def test_install_latest_version_hands_off_to_windows_update_helper(monkeypatch, tmp_path) -> None:
    started: list[tuple[list[str], list[list[str]]]] = []
    monkeypatch.setattr("auto_model_key_router.update.os.name", "nt")
    monkeypatch.setattr(sys, "argv", ["C:\\Users\\Sparr\\.local\\bin\\amkr.exe"])
    monkeypatch.setattr("auto_model_key_router.update.detected_installation_method", lambda: "uv-tool")
    monkeypatch.setattr("auto_model_key_router.update.update_log_path", lambda: tmp_path / "update.log")

    def fake_start(version_result: VersionCheckResult, command: list[str], commands_after_update: list[list[str]]) -> None:
        started.append((command, commands_after_update))

    monkeypatch.setattr("auto_model_key_router.update.start_windows_update_helper", fake_start)
    monkeypatch.setattr("auto_model_key_router.update.subprocess.run", lambda *args, **kwargs: (_ for _ in ()).throw(AssertionError("不应直接运行更新命令")))

    outcome = install_latest_version_outcome(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"), tmp_path / "config.json", restart_tui=True)
    text = render_text(outcome.content)

    assert started[0][0] == ["uv", "tool", "upgrade", "auto-model-key-router"]
    assert started[0][1][0][-1] == "--restart-service-after-update"
    assert outcome.updated
    assert outcome.handoff
    assert "独立更新器已接管" in text
    assert "当前界面将立即退出" in text


def test_post_update_commands_restart_service_and_tui(tmp_path) -> None:
    config_path = tmp_path / "config.json"

    commands = post_update_commands(config_path, restart_tui=True)

    assert commands[0][-1:] == ["--restart-service-after-update"]
    assert str(config_path) in commands[0]
    assert commands[1][-2:] == ["--config", str(config_path)]


def test_windows_update_helper_script_handshakes_retries_and_writes_log(tmp_path) -> None:
    command = ["uv", "tool", "upgrade", "auto-model-key-router"]

    script = windows_update_helper_script(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"), command, 123, tmp_path / "update.ready", tmp_path / "update.log", tmp_path / "stdout.log", tmp_path / "stderr.log")

    assert "Set-Content -LiteralPath $readyPath" in script
    assert "$parentProcess = Get-Process -Id 123 -ErrorAction SilentlyContinue" in script
    assert "if ($null -ne $parentProcess) { $parentProcess.WaitForExit() }" in script
    assert "Wait-Process" not in script
    assert "for ($attempt = 1; $attempt -le $maxAttempts; $attempt++)" in script
    assert "$argumentLine = 'tool upgrade auto-model-key-router'" in script
    assert "Start-Process -FilePath $tool -ArgumentList $argumentLine -Wait -PassThru -NoNewWindow -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath" in script
    assert "$exitCode = $updateProcess.ExitCode" in script
    assert "& $tool @commandArgs" not in script
    assert "命令: uv tool upgrade auto-model-key-router" in script
    assert "Save-UpdateLog '更新失败'" in script
    assert "Read-Host '按 Enter 关闭更新器'" in script


def test_windows_update_helper_script_verifies_installed_version_before_success(tmp_path) -> None:
    command = ["uv", "tool", "upgrade", "auto-model-key-router"]

    script = windows_update_helper_script(
        VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"),
        command,
        123,
        tmp_path / "update.ready",
        tmp_path / "update.log",
        tmp_path / "stdout.log",
        tmp_path / "stderr.log",
    )

    assert "$expectedVersion = '1.2.3'" in script
    assert "auto_model_key_router.main" in script
    assert "--version" in script
    assert "更新命令退出码为 0，但版本仍不是目标版本" in script
    assert "if ($exitCode -eq 0 -and $versionVerified) { break }" in script


def test_windows_update_helper_script_runs_post_update_commands_on_success(tmp_path) -> None:
    command = ["uv", "tool", "upgrade", "auto-model-key-router"]
    post_commands = [[sys.executable, "-m", "auto_model_key_router.main", "--config", str(tmp_path / "config.json"), "--restart-service-after-update"], [sys.executable, "-m", "auto_model_key_router.main", "--config", str(tmp_path / "config.json")]]

    script = windows_update_helper_script(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"), command, 123, tmp_path / "update.ready", tmp_path / "update.log", tmp_path / "stdout.log", tmp_path / "stderr.log", post_commands)

    assert "if ($exitCode -eq 0)" in script
    assert "--restart-service-after-update" in script
    assert "& $postTool0 @postArgs0" in script
    assert "Start-Process -FilePath $postTool1" in script
    assert "更新成功，后续操作失败" in script


def test_start_windows_update_helper_writes_utf8_bom_for_windows_powershell(tmp_path, monkeypatch) -> None:
    launched: list[tuple[list[str], dict[str, object]]] = []
    script_path = tmp_path / "update.ps1"
    monkeypatch.setattr("auto_model_key_router.update.windows_update_script_path", lambda: script_path)
    monkeypatch.setattr("auto_model_key_router.update.windows_update_ready_path", lambda: tmp_path / "update.ready")
    monkeypatch.setattr("auto_model_key_router.update.update_log_path", lambda: tmp_path / "update.log")
    monkeypatch.setattr("auto_model_key_router.update.default_cache_dir", lambda: tmp_path)
    monkeypatch.setattr("auto_model_key_router.update.wait_for_windows_update_helper", lambda process, ready_path: None)
    monkeypatch.setattr("auto_model_key_router.update.subprocess.Popen", lambda command, **kwargs: launched.append((command, kwargs)) or object())

    start_windows_update_helper(VersionCheckResult(current_version="1.0.0", latest_version="1.2.3", source="PyPI"), ["uv", "tool", "upgrade", "auto-model-key-router"])

    assert script_path.read_bytes().startswith(b"\xef\xbb\xbf")
    assert launched[0][0][0] == "powershell.exe"
    assert "-File" in launched[0][0]


def test_update_output_preview_keeps_tail_for_long_logs() -> None:
    output = "\n".join(f"line-{index}" for index in range(200))

    preview, truncated = update_output_preview(output, line_limit=3, char_limit=100)

    assert truncated
    assert preview == "line-197\nline-198\nline-199"
