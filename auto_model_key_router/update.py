from __future__ import annotations

import json
import re
import subprocess
import sys
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

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


def manual_update_command(result: VersionCheckResult) -> list[str]:
    if result.source == "GitHub":
        if not result.latest_tag:
            raise ValueError("GitHub 更新缺少 tag。")
        return [sys.executable, "-m", "pip", "install", "--upgrade", github_source_archive_url(result.latest_tag)]
    return [sys.executable, "-m", "pip", "install", "--upgrade", PACKAGE_NAME]


def manual_update_command_text(result: VersionCheckResult) -> str:
    command = manual_update_command(result)
    return f'"{command[0]}" -m pip install --upgrade "{command[-1]}"'


def update_target_label(result: VersionCheckResult) -> str:
    return result.latest_tag or result.latest_version or "最新版本"


def update_log_path() -> Path:
    return default_cache_dir() / UPDATE_LOG_FILE_NAME


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
                f"命令: {manual_update_command_text(version_result)}",
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


def install_latest_version(version_result: VersionCheckResult) -> Panel:
    try:
        command = manual_update_command(version_result)
    except ValueError as exc:
        return section_panel(str(exc), "手动更新", "red")
    try:
        result = subprocess.run(command, capture_output=True, text=True, errors="replace")
    except (OSError, subprocess.SubprocessError) as exc:
        stderr = str(exc)
        log_path, log_error = write_update_log(version_result, command, None, "", stderr)
        content = "\n".join([
            "更新命令执行失败。",
            f"命令: [bold]{escape(manual_update_command_text(version_result))}[/bold]",
            *update_log_lines(log_path, log_error, False),
            escape(stderr),
        ])
        return section_panel(content, "手动更新", "red")

    stdout = update_output_text(result.stdout)
    stderr = update_output_text(result.stderr)
    output = update_process_output(stdout, stderr)
    preview, truncated = update_output_preview(output)
    log_path, log_error = write_update_log(version_result, command, result.returncode, stdout, stderr)
    if result.returncode == 0:
        content = "\n".join([
            f"已通过 {escape(version_result.source or '可用来源')} 安装 [bold]{escape(update_target_label(version_result))}[/bold]。",
            "请重启当前终端和正在运行的服务，让新版本生效。",
            f"命令: [bold]{escape(manual_update_command_text(version_result))}[/bold]",
            *update_log_lines(log_path, log_error, truncated),
            escape(preview) if preview else "pip 未返回额外输出。",
        ])
        return section_panel(content, "手动更新", "green")

    content = "\n".join([
        f"更新失败，退出码: [bold]{result.returncode}[/bold]",
        f"命令: [bold]{escape(manual_update_command_text(version_result))}[/bold]",
        *update_log_lines(log_path, log_error, truncated),
        escape(preview) if preview else "pip 未返回错误详情。",
    ])
    return section_panel(content, "手动更新", "red")


def install_latest_release(tag: str) -> Panel:
    return install_latest_version(VersionCheckResult(current_version=__version__, latest_version=tag.removeprefix("v").removeprefix("V"), latest_tag=tag, source="GitHub"))


def update_latest_version(timeout: float = 10.0) -> Panel:
    result = check_latest_version(timeout=timeout)
    if result.error or not result.update_available:
        return render_version_check_result(result)
    return install_latest_version(result)


def update_latest_release(timeout: float = 10.0) -> Panel:
    return update_latest_version(timeout)
