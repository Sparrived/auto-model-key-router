from __future__ import annotations

import json
import re
import subprocess
import sys
from dataclasses import dataclass
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from rich.markup import escape
from rich.panel import Panel

from . import __version__
from .tui import section_panel


GITHUB_REPOSITORY = "Sparrived/auto-model-key-router"
GITHUB_RELEASES_URL = f"https://github.com/{GITHUB_REPOSITORY}/releases"
GITHUB_LATEST_RELEASE_API = f"https://api.github.com/repos/{GITHUB_REPOSITORY}/releases/latest"


@dataclass(frozen=True)
class VersionCheckResult:
    current_version: str
    latest_version: str | None = None
    latest_tag: str | None = None
    release_url: str | None = None
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


def check_latest_release(current_version: str = __version__, timeout: float = 3.0) -> VersionCheckResult:
    request = Request(
        GITHUB_LATEST_RELEASE_API,
        headers={
            "Accept": "application/vnd.github+json",
            "User-Agent": f"auto-model-key-router/{current_version}",
        },
    )
    try:
        with urlopen(request, timeout=timeout) as response:
            data = json.loads(response.read().decode("utf-8"))
    except (HTTPError, URLError, OSError, json.JSONDecodeError) as exc:
        return VersionCheckResult(current_version=current_version, error=str(exc))

    tag = str(data.get("tag_name") or "").strip()
    if not tag:
        return VersionCheckResult(current_version=current_version, error="GitHub Release 响应中缺少 tag_name。")

    latest_version = tag.removeprefix("v").removeprefix("V")
    release_url = str(data.get("html_url") or GITHUB_RELEASES_URL)
    return VersionCheckResult(current_version=current_version, latest_version=latest_version, latest_tag=tag, release_url=release_url)


def github_source_archive_url(tag: str) -> str:
    return f"https://github.com/{GITHUB_REPOSITORY}/archive/refs/tags/{tag}.zip"


def manual_update_command(tag: str) -> list[str]:
    return [sys.executable, "-m", "pip", "install", "--upgrade", github_source_archive_url(tag)]


def manual_update_command_text(tag: str) -> str:
    return f'"{sys.executable}" -m pip install --upgrade "{github_source_archive_url(tag)}"'


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
        f"最新版本: [bold]{escape(result.latest_version or '-') }[/bold]",
    ]
    if result.release_url:
        lines.append(f"发布页面: [bold]{escape(result.release_url)}[/bold]")
    if result.update_available and result.latest_tag:
        lines.extend([
            "状态: [yellow]发现新版本，可手动更新。[/yellow]",
            "手动更新命令:",
            f"[bold]{escape(manual_update_command_text(result.latest_tag))}[/bold]",
        ])
        return section_panel("\n".join(lines), "版本检查", "yellow")

    lines.append("状态: [green]当前已是最新版本。[/green]")
    return section_panel("\n".join(lines), "版本检查", "green")


def render_update_notice(result: VersionCheckResult | None) -> Panel | None:
    if result is None or not result.update_available or not result.latest_tag:
        return None
    return section_panel(
        "\n".join([
            f"发现新版本 [bold yellow]{escape(result.latest_tag)}[/bold yellow]，当前版本 [bold]{escape(result.current_version)}[/bold]。",
            "进入主菜单的“版本更新”可查看发布页面并手动更新。",
        ]),
        "更新提示",
        "yellow",
    )


def install_latest_release(tag: str) -> Panel:
    command = manual_update_command(tag)
    result = subprocess.run(command, capture_output=True, text=True)
    output = (result.stdout or result.stderr or "").strip()
    if len(output) > 4000:
        output = output[-4000:]
    if result.returncode == 0:
        content = "\n".join([
            f"已通过 GitHub 安装 [bold]{escape(tag)}[/bold]。",
            "请重启当前终端和正在运行的服务，让新版本生效。",
            f"命令: [bold]{escape(manual_update_command_text(tag))}[/bold]",
            escape(output) if output else "pip 未返回额外输出。",
        ])
        return section_panel(content, "手动更新", "green")

    content = "\n".join([
        f"更新失败，退出码: [bold]{result.returncode}[/bold]",
        f"命令: [bold]{escape(manual_update_command_text(tag))}[/bold]",
        escape(output) if output else "pip 未返回错误详情。",
    ])
    return section_panel(content, "手动更新", "red")


def update_latest_release(timeout: float = 10.0) -> Panel:
    result = check_latest_release(timeout=timeout)
    if result.error or not result.update_available or not result.latest_tag:
        return render_version_check_result(result)
    return install_latest_release(result.latest_tag)
