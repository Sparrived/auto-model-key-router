from __future__ import annotations

import platform
import shutil
import subprocess


def clipboard_commands() -> list[list[str]]:
    system = platform.system()
    if system == "Windows":
        return [["clip"]]
    if system == "Darwin":
        return [["pbcopy"]] if shutil.which("pbcopy") else []
    commands: list[list[str]] = []
    if shutil.which("wl-copy"):
        commands.append(["wl-copy"])
    if shutil.which("xclip"):
        commands.append(["xclip", "-selection", "clipboard"])
    if shutil.which("xsel"):
        commands.append(["xsel", "--clipboard", "--input"])
    return commands


def copy_to_clipboard(text: str) -> tuple[bool, str]:
    if not text:
        return False, "没有可复制的内容。"
    commands = clipboard_commands()
    if not commands:
        return False, "当前系统未找到可用的剪贴板命令。"
    errors: list[str] = []
    for command in commands:
        try:
            result = subprocess.run(command, input=text, text=True, capture_output=True, timeout=5, check=False)
        except (OSError, subprocess.SubprocessError) as exc:
            errors.append(str(exc))
            continue
        if result.returncode == 0:
            return True, "已复制到剪贴板。"
        errors.append((result.stderr or result.stdout or f"退出码 {result.returncode}").strip())
    detail = errors[-1] if errors else "未知错误"
    return False, f"复制失败: {detail}"
