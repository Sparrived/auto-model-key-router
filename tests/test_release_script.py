from __future__ import annotations

import importlib.util
import sys
from pathlib import Path


def load_release_script():
    path = Path(__file__).resolve().parents[1] / "scripts" / "release.py"
    spec = importlib.util.spec_from_file_location("release_script", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules["release_script"] = module
    spec.loader.exec_module(module)
    return module


release_script = load_release_script()


def test_calculate_next_version_for_stable_releases() -> None:
    assert release_script.calculate_next_version("1.2.2", "patch") == "1.2.3"
    assert release_script.calculate_next_version("1.2.2", "minor") == "1.3.0"
    assert release_script.calculate_next_version("1.2.2", "major") == "2.0.0"


def test_calculate_next_version_for_post_and_preview_releases() -> None:
    assert release_script.calculate_next_version("1.2.2", "post") == "1.2.2.post1"
    assert release_script.calculate_next_version("1.2.2.post1", "post") == "1.2.2.post2"
    assert release_script.calculate_next_version("1.2.2", "preview") == "1.2.3rc1"
    assert release_script.calculate_next_version("1.2.3rc1", "preview") == "1.2.3rc2"


def test_calculate_next_version_for_extra_release_types() -> None:
    assert release_script.calculate_next_version("1.2.2", "alpha") == "1.2.3a1"
    assert release_script.calculate_next_version("1.2.3a1", "alpha") == "1.2.3a2"
    assert release_script.calculate_next_version("1.2.2", "beta") == "1.2.3b1"
    assert release_script.calculate_next_version("1.2.2", "dev") == "1.2.3.dev1"
    assert release_script.calculate_next_version("1.2.3rc1", "stable") == "1.2.3"
    assert release_script.calculate_next_version("1.2.2", "custom", "3.0.0rc1") == "3.0.0rc1"


def test_git_args_disable_pager() -> None:
    assert release_script.git_args(["diff", "--check"]) == ["git", "--no-pager", "diff", "--check"]
    assert release_script.git_args(["push", "origin", "master"], no_proxy=True) == [
        "git",
        "--no-pager",
        "-c",
        "http.proxy=",
        "-c",
        "https.proxy=",
        "push",
        "origin",
        "master",
    ]


def test_render_updated_changelog_moves_unreleased_body() -> None:
    text = "# Changelog\n\n## [Unreleased]\n\n### Added\n- 新增功能\n\n## [1.0.0] - 2026-01-01\n\n### Added\n- 初始版本\n"

    updated = release_script.render_updated_changelog(text, "1.0.1", "2026-06-09")

    assert "## [Unreleased]\n\n## [1.0.1] - 2026-06-09" in updated
    assert "### Added\n- 新增功能" in updated
    assert updated.index("## [1.0.1]") < updated.index("## [1.0.0]")


def test_render_updated_changelog_uses_notes_when_unreleased_empty() -> None:
    text = "# Changelog\n\n## [Unreleased]\n\n## [1.0.0] - 2026-01-01\n"

    updated = release_script.render_updated_changelog(text, "1.0.1", "2026-06-09", "修复发布流程")

    assert "## [1.0.1] - 2026-06-09\n\n### Changed\n- 修复发布流程" in updated
