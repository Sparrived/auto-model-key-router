from __future__ import annotations

import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from rich.console import Group
from rich.markup import escape
from rich.panel import Panel

from . import __version__
from .config import default_cache_dir
from .tui import section_panel


PACKAGE_NAME = "auto-model-key-router"
PYPI_PROJECT_URL = f"https://pypi.org/project/{PACKAGE_NAME}/"
PYPI_JSON_API = f"https://pypi.org/pypi/{PACKAGE_NAME}/json"
GITHUB_REPOSITORY = "Sparrived/auto-model-key-router"
GITHUB_RELEASES_URL = f"https://github.com/{GITHUB_REPOSITORY}/releases"
GITHUB_LATEST_RELEASE_API = f"https://api.github.com/repos/{GITHUB_REPOSITORY}/releases/latest"
UPDATE_LOG_FILE_NAME = "update.log"
UPDATE_PREVIEW_LINES = 120
UPDATE_PREVIEW_CHARS = 12000
WINDOWS_CONSOLE_SCRIPT_NAMES = {"amkr", "amkr.exe", "auto-model-key-router", "auto-model-key-router.exe"}


@dataclass(frozen=True)
class VersionCheckResult:
    current_version: str
    latest_version: str | None = None
    latest_tag: str | None = None
    release_url: str | None = None
    source: str | None = None
    fallback_error: str | None = None
    error: str | None = None

    @property
    def update_available(self) -> bool:
        return bool(self.latest_version and is_newer_version(self.latest_version, self.current_version))


@dataclass(frozen=True)
class UpdateInstallOutcome:
    content: Any
    updated: bool = False
    handoff: bool = False


def version_numbers(version: str) -> tuple[int, ...]:
    return tuple(int(part) for part in re.findall(r"\d+", version.split("+", 1)[0]))


def comparable_version(version: str) -> tuple[int, ...]:
    numbers = version_numbers(version)
    return numbers + (0,) * max(0, 3 - len(numbers))


def is_newer_version(latest: str, current: str) -> bool:
    return comparable_version(latest) > comparable_version(current)


def fetch_json(url: str, headers: dict[str, str], timeout: float) -> dict[str, Any]:
    request = Request(
        url,
        headers=headers,
    )
    with urlopen(request, timeout=timeout) as response:
        data = json.loads(response.read().decode("utf-8"))
    if not isinstance(data, dict):
        raise ValueError("响应不是 JSON 对象。")
    return data


def check_latest_pypi(current_version: str = __version__, timeout: float = 3.0) -> VersionCheckResult:
    try:
        data = fetch_json(
            PYPI_JSON_API,
            {
                "Accept": "application/json",
                "User-Agent": f"auto-model-key-router/{current_version}",
            },
            timeout,
        )
    except (HTTPError, URLError, OSError, ValueError, json.JSONDecodeError) as exc:
        return VersionCheckResult(current_version=current_version, error=str(exc))

    info = data.get("info")
    if not isinstance(info, dict):
        return VersionCheckResult(current_version=current_version, error="PyPI 响应中缺少 info。")
    latest_version = str(info.get("version") or "").strip()
    if not latest_version:
        return VersionCheckResult(current_version=current_version, error="PyPI 响应中缺少 version。")
    release_url = str(info.get("package_url") or info.get("project_url") or PYPI_PROJECT_URL)
    return VersionCheckResult(current_version=current_version, latest_version=latest_version, release_url=release_url, source="PyPI")


def check_latest_release(current_version: str = __version__, timeout: float = 3.0) -> VersionCheckResult:
    try:
        data = fetch_json(
            GITHUB_LATEST_RELEASE_API,
            {
                "Accept": "application/vnd.github+json",
                "User-Agent": f"auto-model-key-router/{current_version}",
            },
            timeout,
        )
    except (HTTPError, URLError, OSError, ValueError, json.JSONDecodeError) as exc:
        return VersionCheckResult(current_version=current_version, error=str(exc))

    tag = str(data.get("tag_name") or "").strip()
    if not tag:
        return VersionCheckResult(current_version=current_version, error="GitHub Release 响应中缺少 tag_name。")

    latest_version = tag.removeprefix("v").removeprefix("V")
    release_url = str(data.get("html_url") or GITHUB_RELEASES_URL)
    return VersionCheckResult(current_version=current_version, latest_version=latest_version, latest_tag=tag, release_url=release_url, source="GitHub")


def check_latest_version(current_version: str = __version__, timeout: float = 3.0) -> VersionCheckResult:
    pypi_result = check_latest_pypi(current_version, timeout)
    if not pypi_result.error:
        return pypi_result

    github_result = check_latest_release(current_version, timeout)
    if github_result.error:
        return VersionCheckResult(current_version=current_version, error=f"PyPI 检查失败: {pypi_result.error}；GitHub 检查失败: {github_result.error}")
    return VersionCheckResult(current_version=current_version, latest_version=github_result.latest_version, latest_tag=github_result.latest_tag, release_url=github_result.release_url, source=github_result.source, fallback_error=pypi_result.error)


def github_source_archive_url(tag: str) -> str:
    return f"https://github.com/{GITHUB_REPOSITORY}/archive/refs/tags/{tag}.zip"


def is_relative_to(path: Path, parent: Path) -> bool:
    try:
        path.resolve().relative_to(parent.resolve())
    except (OSError, ValueError):
        return False
    return True


def default_uv_cache_dir() -> Path:
    if os.environ.get("UV_CACHE_DIR"):
        return Path(os.environ["UV_CACHE_DIR"])
    if os.name == "nt" and os.environ.get("LOCALAPPDATA"):
        return Path(os.environ["LOCALAPPDATA"]) / "uv" / "cache"
    return Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "uv"


def default_uv_tool_dirs() -> tuple[Path, ...]:
    dirs: list[Path] = []
    if os.environ.get("UV_TOOL_DIR"):
        dirs.append(Path(os.environ["UV_TOOL_DIR"]))
    if os.name == "nt":
        if os.environ.get("APPDATA"):
            dirs.append(Path(os.environ["APPDATA"]) / "uv" / "tools")
        if os.environ.get("LOCALAPPDATA"):
            dirs.append(Path(os.environ["LOCALAPPDATA"]) / "uv" / "tools")
    elif sys.platform == "darwin":
        dirs.append(Path.home() / "Library" / "Application Support" / "uv" / "tools")
    else:
        dirs.append(Path(os.environ.get("XDG_DATA_HOME", Path.home() / ".local" / "share")) / "uv" / "tools")
    return tuple(dict.fromkeys(dirs))


def detected_installation_method(prefix: Path | None = None) -> str:
    current_prefix = (prefix or Path(sys.prefix)).resolve()
    if (current_prefix / "pipx_metadata.json").exists():
        return "pipx"
    pipx_home = os.environ.get("PIPX_HOME")
    if pipx_home and is_relative_to(current_prefix, Path(pipx_home)):
        return "pipx"
    if any(is_relative_to(current_prefix, uv_tool_dir) for uv_tool_dir in default_uv_tool_dirs()):
        return "uv-tool"
    if is_relative_to(current_prefix, default_uv_cache_dir()):
        return "uvx"
    return "pip"


def manual_update_target(result: VersionCheckResult) -> str:
    if result.source == "GitHub":
        if not result.latest_tag:
            raise ValueError("GitHub 更新缺少 tag。")
        return github_source_archive_url(result.latest_tag)
    return PACKAGE_NAME


def manual_update_command(result: VersionCheckResult) -> list[str]:
    target = manual_update_target(result)
    method = detected_installation_method()
    if method == "pipx":
        if target == PACKAGE_NAME:
            return ["pipx", "upgrade", PACKAGE_NAME]
        return ["pipx", "install", "--force", target]
    if method in {"uv-tool", "uvx"}:
        if target == PACKAGE_NAME and method == "uv-tool":
            return ["uv", "tool", "upgrade", PACKAGE_NAME]
        return ["uv", "tool", "install", "--force", target]
    return [sys.executable, "-m", "pip", "install", "--upgrade", target]


def shell_command_text(command: list[str]) -> str:
    if os.name == "nt":
        return subprocess.list2cmdline(command)
    return shlex.join(command)


def manual_update_command_text(result: VersionCheckResult) -> str:
    return shell_command_text(manual_update_command(result))


def update_target_label(result: VersionCheckResult) -> str:
    return result.latest_tag or result.latest_version or "最新版本"


def update_log_path() -> Path:
    return default_cache_dir() / UPDATE_LOG_FILE_NAME


def windows_update_script_path() -> Path:
    return default_cache_dir() / f"update-{os.getpid()}.ps1"


def windows_update_ready_path() -> Path:
    return default_cache_dir() / f"update-{os.getpid()}.ready"


def is_windows_console_script_process(argv0: str | None = None) -> bool:
    if os.name != "nt":
        return False
    return Path(argv0 or sys.argv[0]).name.lower() in WINDOWS_CONSOLE_SCRIPT_NAMES


def should_use_windows_update_helper(command: list[str]) -> bool:
    return bool(command) and is_windows_console_script_process()


def powershell_string(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def powershell_array(values: list[str]) -> str:
    return "@(" + ", ".join(powershell_string(value) for value in values) + ")"


def windows_update_creationflags() -> int:
    return sum(getattr(subprocess, name, 0) for name in ("CREATE_NEW_PROCESS_GROUP", "CREATE_NEW_CONSOLE"))


def resolved_update_command(command: list[str]) -> list[str]:
    executable = shutil.which(command[0]) or command[0]
    return [executable, *command[1:]]


def windows_update_helper_script(version_result: VersionCheckResult, command: list[str], parent_pid: int, ready_path: Path, log_path: Path, stdout_path: Path, stderr_path: Path, post_update_commands: list[list[str]] | None = None, max_attempts: int = 6) -> str:
    source_line = f"来源: {version_result.source or '可用来源'}"
    target_line = f"目标: {update_target_label(version_result)}"
    command_line = f"命令: {shell_command_text(command)}"
    argument_line = subprocess.list2cmdline(command[1:])
    post_update_lines: list[str] = []
    for index, post_update_command in enumerate(post_update_commands or []):
        post_command = resolved_update_command(post_update_command)
        post_update_lines.extend(
            [
                f"$postTool{index} = {powershell_string(post_command[0])}",
                f"$postArgs{index} = {powershell_array(post_command[1:])}",
            ]
        )
        if index == 0:
            post_update_lines.extend(
                [
                    "Write-Host '正在执行更新后的服务处理...'",
                    "try {",
                    f"    & $postTool{index} @postArgs{index}",
                    "    if ($LASTEXITCODE -ne 0) { $postUpdateFailed = $true; $attemptLog += @('', '[post-update warning]', \"更新后命令退出码: $LASTEXITCODE\") }",
                    "} catch {",
                    "    $postUpdateFailed = $true",
                    "    $attemptLog += @('', '[post-update warning]', ($_ | Out-String))",
                    "}",
                ]
            )
        else:
            post_update_lines.extend(
                [
                    f"$postArgumentLine{index} = {powershell_string(subprocess.list2cmdline(post_command[1:]))}",
                    "try {",
                    f"    Start-Process -FilePath $postTool{index} -ArgumentList $postArgumentLine{index} | Out-Null",
                    "} catch {",
                    "    $postUpdateFailed = $true",
                    "    $attemptLog += @('', '[post-update warning]', ($_ | Out-String))",
                    "}",
                ]
            )
    if post_update_lines:
        post_update_lines = ["if ($exitCode -eq 0) {", *[f"    {line}" for line in post_update_lines], "}"]
    return "\n".join([
        "$ErrorActionPreference = 'Stop'",
        "$ProgressPreference = 'SilentlyContinue'",
        f"$tool = {powershell_string(command[0])}",
        f"$argumentLine = {powershell_string(argument_line)}",
        f"$readyPath = {powershell_string(str(ready_path))}",
        f"$logPath = {powershell_string(str(log_path))}",
        f"$stdoutPath = {powershell_string(str(stdout_path))}",
        f"$stderrPath = {powershell_string(str(stderr_path))}",
        f"$maxAttempts = {max_attempts}",
        "$exitCode = 1",
        "$stdoutText = ''",
        "$stderrText = ''",
        "$attemptLog = @()",
        "$postUpdateFailed = $false",
        "$parent = Split-Path -Parent $logPath",
        "if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }",
        "function Save-UpdateLog([string]$status) {",
        "    $lines = @(",
        "        \"时间: $(Get-Date -Format o)\"",
        f"        {powershell_string(source_line)}",
        f"        {powershell_string(target_line)}",
        f"        {powershell_string(command_line)}",
        "        \"状态: $status\"",
        "        \"退出码: $exitCode\"",
        "    ) + $attemptLog",
        "    Set-Content -LiteralPath $logPath -Value $lines -Encoding UTF8",
        "}",
        "Save-UpdateLog '更新器已接管，等待当前进程退出'",
        "'ready' | Set-Content -LiteralPath $readyPath -Encoding ASCII",
        "Write-Host 'Windows 更新器已接管。正在等待当前 amkr 退出并释放文件锁...'",
        "try {",
        f"    Wait-Process -Id {parent_pid} -ErrorAction SilentlyContinue",
        "    Start-Sleep -Milliseconds 500",
        "    for ($attempt = 1; $attempt -le $maxAttempts; $attempt++) {",
        "        Write-Host \"正在更新（第 $attempt/$maxAttempts 次）...\"",
        "        Remove-Item -LiteralPath $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue",
        "        $updateProcess = Start-Process -FilePath $tool -ArgumentList $argumentLine -Wait -PassThru -NoNewWindow -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath",
        "        $exitCode = $updateProcess.ExitCode",
        "        $stdoutText = if (Test-Path -LiteralPath $stdoutPath) { Get-Content -LiteralPath $stdoutPath -Raw -ErrorAction SilentlyContinue } else { '' }",
        "        $stderrText = if (Test-Path -LiteralPath $stderrPath) { Get-Content -LiteralPath $stderrPath -Raw -ErrorAction SilentlyContinue } else { '' }",
        "        $attemptLog += @('', \"[attempt $attempt stdout]\", $stdoutText, '', \"[attempt $attempt stderr]\", $stderrText)",
        "        Save-UpdateLog $(if ($exitCode -eq 0) { '更新成功' } else { \"第 $attempt 次更新失败\" })",
        "        if ($exitCode -eq 0) { break }",
        "        if ($attempt -lt $maxAttempts) { Start-Sleep -Seconds ([Math]::Min(5, $attempt)) }",
        "    }",
        "    if ($exitCode -ne 0) { throw \"更新命令在 $maxAttempts 次尝试后仍失败，退出码: $exitCode\" }",
        *post_update_lines,
        "} catch {",
        "    $failure = $_ | Out-String",
        "    $attemptLog += @('', '[updater error]', $failure)",
        "    Save-UpdateLog '更新失败'",
        "    Write-Host ''",
        "    Write-Host '更新失败，详情已写入：' -ForegroundColor Red",
        "    Write-Host $logPath -ForegroundColor Yellow",
        "    Write-Host $failure -ForegroundColor Red",
        "    Read-Host '按 Enter 关闭更新器'",
        "    exit 1",
        "}",
        "$finalStatus = if ($postUpdateFailed) { '更新成功，后续操作失败' } else { '更新成功' }",
        "Save-UpdateLog $finalStatus",
        "Write-Host ''",
        "if ($postUpdateFailed) {",
        "    Write-Host '更新已完成，但服务或 Terminal UI 的自动重启失败，请查看更新日志。' -ForegroundColor Yellow",
        "} else {",
        "    Write-Host '更新完成。' -ForegroundColor Green",
        "}",
        "Start-Sleep -Seconds 1",
        "Remove-Item -LiteralPath $stdoutPath, $stderrPath, $readyPath -Force -ErrorAction SilentlyContinue",
        "Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue",
        "",
    ])


def wait_for_windows_update_helper(process: subprocess.Popen[Any], ready_path: Path, timeout: float = 5.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if ready_path.exists():
            return
        returncode = process.poll()
        if returncode is not None:
            raise RuntimeError(f"Windows 更新器在接管前退出，退出码: {returncode}")
        time.sleep(0.05)
    raise TimeoutError("Windows 更新器启动超时，未确认接管。")


def start_windows_update_helper(version_result: VersionCheckResult, command: list[str], post_update_commands: list[list[str]] | None = None) -> Path:
    script_path = windows_update_script_path()
    ready_path = windows_update_ready_path()
    log_path = update_log_path()
    stdout_path = default_cache_dir() / f"update-{os.getpid()}.stdout.log"
    stderr_path = default_cache_dir() / f"update-{os.getpid()}.stderr.log"
    command = resolved_update_command(command)
    script_path.parent.mkdir(parents=True, exist_ok=True)
    ready_path.unlink(missing_ok=True)
    script_path.write_text(windows_update_helper_script(version_result, command, os.getpid(), ready_path, log_path, stdout_path, stderr_path, post_update_commands), encoding="utf-8-sig")
    process = subprocess.Popen(
        ["powershell.exe", "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(script_path)],
        close_fds=True,
        creationflags=windows_update_creationflags(),
    )
    wait_for_windows_update_helper(process, ready_path)
    ready_path.unlink(missing_ok=True)
    return script_path


def update_process_output(stdout: str | None, stderr: str | None) -> str:
    streams = (("stdout", (stdout or "").strip()), ("stderr", (stderr or "").strip()))
    return "\n\n".join(f"[{name}]\n{content}" for name, content in streams if content)


def update_output_text(value: str | bytes | None) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value


def update_output_preview(output: str, line_limit: int = UPDATE_PREVIEW_LINES, char_limit: int = UPDATE_PREVIEW_CHARS) -> tuple[str, bool]:
    lines = output.splitlines()
    truncated = len(lines) > line_limit
    preview = "\n".join(lines[-line_limit:]) if truncated else output
    if len(preview) > char_limit:
        preview = preview[-char_limit:]
        truncated = True
    return preview, truncated


def write_update_log(version_result: VersionCheckResult, command: list[str], returncode: int | str | None, stdout: str | None, stderr: str | None) -> tuple[Path | None, str | None]:
    path = update_log_path()
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(
            "\n".join([
                f"时间: {datetime.now().astimezone().isoformat(timespec='seconds')}",
                f"来源: {version_result.source or '可用来源'}",
                f"目标: {update_target_label(version_result)}",
                f"命令: {shell_command_text(command)}",
                f"退出码: {'未启动' if returncode is None else returncode}",
                "",
                "[stdout]",
                stdout or "",
                "",
                "[stderr]",
                stderr or "",
                "",
            ]),
            encoding="utf-8",
        )
    except OSError as exc:
        return None, str(exc)
    return path, None


def update_log_lines(log_path: Path | None, log_error: str | None, truncated: bool) -> list[str]:
    lines: list[str] = []
    if log_path is not None:
        lines.append(f"完整日志: [bold]{escape(str(log_path))}[/bold]")
    if log_error is not None:
        lines.append(f"日志写入失败: [yellow]{escape(log_error)}[/yellow]")
    if truncated:
        lines.append(f"界面仅展示最后 {UPDATE_PREVIEW_LINES} 行，完整内容请打开日志文件。")
    return lines


def render_version_check_result(result: VersionCheckResult) -> Panel:
    if result.error:
        return section_panel(
            "\n".join([
                f"当前版本: [bold]{escape(result.current_version)}[/bold]",
                f"检查失败: [red]{escape(result.error)}[/red]",
                f"GitHub Releases: [bold]{escape(GITHUB_RELEASES_URL)}[/bold]",
            ]),
            "版本检查",
            "red",
        )

    lines = [
        f"当前版本: [bold]{escape(result.current_version)}[/bold]",
        f"最新版本: [bold]{escape(result.latest_version or '-')}[/bold]",
    ]
    if result.source:
        lines.append(f"检查来源: [bold]{escape(result.source)}[/bold]")
    if result.fallback_error:
        lines.append(f"PyPI 回退原因: [yellow]{escape(result.fallback_error)}[/yellow]")
    if result.release_url:
        lines.append(f"发布页面: [bold]{escape(result.release_url)}[/bold]")
    if result.update_available:
        lines.extend([
            "状态: [yellow]发现新版本，可手动更新。[/yellow]",
            "手动更新命令:",
            f"[bold]{escape(manual_update_command_text(result))}[/bold]",
        ])
        return section_panel("\n".join(lines), "版本检查", "yellow")

    lines.append("状态: [green]当前已是最新版本。[/green]")
    return section_panel("\n".join(lines), "版本检查", "green")


def render_update_notice(result: VersionCheckResult | None) -> Panel | None:
    if result is None or not result.update_available:
        return None
    return section_panel(
        "\n".join([
            f"发现 {escape(result.source or '可用来源')} 新版本 [bold yellow]{escape(update_target_label(result))}[/bold yellow]，当前版本 [bold]{escape(result.current_version)}[/bold]。",
            "进入主菜单的“版本更新”可查看发布页面并手动更新。",
        ]),
        "更新提示",
        "yellow",
    )


def post_update_commands(config_path: Path | None, restart_tui: bool) -> list[list[str]]:
    if config_path is None:
        return []
    commands = [[sys.executable, "-m", "auto_model_key_router.main", "--config", str(config_path), "--restart-service-after-update"]]
    if restart_tui:
        commands.append([sys.executable, "-m", "auto_model_key_router.main", "--config", str(config_path)])
    return commands


def restart_service_after_update(config_path: Path | None) -> Any | None:
    if config_path is None:
        return None
    from .config import RouterConfig
    from .service import is_process_running, is_service_healthy, is_system_service_registered, manage_system_service, pid_file_path, read_pid, start_service_background, stop_background_service

    config = RouterConfig.load(config_path)
    if is_system_service_registered(config_path):
        return manage_system_service(config_path, "restart")
    pid = read_pid(pid_file_path(config))
    if pid and is_process_running(pid):
        return Group(stop_background_service(config), start_service_background(config_path, config))
    if is_service_healthy(config.host, config.port, use_cache=False):
        return Group(stop_background_service(config), start_service_background(config_path, config))
    return section_panel("未检测到正在运行的后台/系统服务，已跳过服务重启。", "更新后服务重启", "yellow")


def install_latest_version_outcome(version_result: VersionCheckResult, config_path: Path | None = None, restart_tui: bool = False) -> UpdateInstallOutcome:
    try:
        command = manual_update_command(version_result)
    except ValueError as exc:
        return UpdateInstallOutcome(section_panel(str(exc), "手动更新", "red"))
    if should_use_windows_update_helper(command):
        try:
            start_windows_update_helper(version_result, command, post_update_commands(config_path, restart_tui))
        except (OSError, subprocess.SubprocessError, RuntimeError, TimeoutError) as exc:
            stderr = str(exc)
            log_path, log_error = write_update_log(version_result, command, None, "", stderr)
            content = "\n".join([
                "Windows 独立更新器启动失败。",
                f"命令: [bold]{escape(shell_command_text(command))}[/bold]",
                *update_log_lines(log_path, log_error, False),
                escape(stderr),
            ])
            return UpdateInstallOutcome(section_panel(content, "手动更新", "red"))
        log_path = update_log_path()
        follow_up = "更新成功后会自动重启正在运行的服务，并重新打开 Terminal UI。" if restart_tui and config_path is not None else "更新成功后会自动重启正在运行的后台/系统服务。" if config_path is not None else "更新成功后请重新打开终端，并重启正在运行的后台/系统服务。"
        content = "\n".join([
            f"Windows 独立更新器已接管，将通过 {escape(version_result.source or '可用来源')} 安装 [bold]{escape(update_target_label(version_result))}[/bold]。",
            "当前界面将立即退出；请在新打开的更新器窗口中查看进度和错误信息。",
            follow_up,
            f"命令: [bold]{escape(shell_command_text(command))}[/bold]",
            *update_log_lines(log_path, None, False),
        ])
        return UpdateInstallOutcome(section_panel(content, "手动更新", "yellow"), updated=True, handoff=True)
    try:
        result = subprocess.run(command, capture_output=True, text=True, errors="replace")
    except (OSError, subprocess.SubprocessError) as exc:
        stderr = str(exc)
        log_path, log_error = write_update_log(version_result, command, None, "", stderr)
        content = "\n".join([
            "更新命令执行失败。",
            f"命令: [bold]{escape(shell_command_text(command))}[/bold]",
            *update_log_lines(log_path, log_error, False),
            escape(stderr),
        ])
        return UpdateInstallOutcome(section_panel(content, "手动更新", "red"))

    stdout = update_output_text(result.stdout)
    stderr = update_output_text(result.stderr)
    output = update_process_output(stdout, stderr)
    preview, truncated = update_output_preview(output)
    log_path, log_error = write_update_log(version_result, command, result.returncode, stdout, stderr)
    if result.returncode == 0:
        restart_result = restart_service_after_update(config_path)
        content = "\n".join([
            f"已通过 {escape(version_result.source or '可用来源')} 安装 [bold]{escape(update_target_label(version_result))}[/bold]。",
            "服务已按当前运行状态自动重启；Terminal UI 将重新启动以加载新版本。" if restart_tui else "服务已按当前运行状态自动重启；请重新打开终端让新版本 CLI 生效。",
            f"命令: [bold]{escape(shell_command_text(command))}[/bold]",
            *update_log_lines(log_path, log_error, truncated),
            escape(preview) if preview else "pip 未返回额外输出。",
        ])
        panel = section_panel(content, "手动更新", "green")
        return UpdateInstallOutcome(Group(panel, restart_result) if restart_result is not None else panel, updated=True)

    content = "\n".join([
        f"更新失败，退出码: [bold]{result.returncode}[/bold]",
        f"命令: [bold]{escape(shell_command_text(command))}[/bold]",
        *update_log_lines(log_path, log_error, truncated),
        escape(preview) if preview else "pip 未返回错误详情。",
    ])
    return UpdateInstallOutcome(section_panel(content, "手动更新", "red"))


def install_latest_version(version_result: VersionCheckResult, config_path: Path | None = None) -> Any:
    return install_latest_version_outcome(version_result, config_path).content


def install_latest_release(tag: str) -> Panel:
    return install_latest_version(VersionCheckResult(current_version=__version__, latest_version=tag.removeprefix("v").removeprefix("V"), latest_tag=tag, source="GitHub"))


def update_latest_version(timeout: float = 10.0, config_path: Path | None = None) -> Any:
    result = check_latest_version(timeout=timeout)
    if result.error or not result.update_available:
        return render_version_check_result(result)
    return install_latest_version(result, config_path)


def update_latest_release(timeout: float = 10.0) -> Panel:
    return update_latest_version(timeout)
