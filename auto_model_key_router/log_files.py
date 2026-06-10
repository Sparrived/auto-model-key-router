from __future__ import annotations

from datetime import datetime
from pathlib import Path


def archive_current_log(log_file_path: str) -> Path | None:
    path = Path(log_file_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists() and path.stat().st_size > 0:
        archive_path = next_log_archive_path(path)
        path.replace(archive_path)
        path.touch()
        return archive_path
    path.write_text("", encoding="utf-8")
    return None


def next_log_archive_path(path: Path, now: datetime | None = None) -> Path:
    timestamp = (now or datetime.now()).strftime("%Y%m%d-%H%M%S")
    suffix = path.suffix or ".log"
    base_name = f"{path.stem}.{timestamp}"
    candidate = path.with_name(f"{base_name}{suffix}")
    index = 1
    while candidate.exists():
        candidate = path.with_name(f"{base_name}.{index}{suffix}")
        index += 1
    return candidate


def archived_log_paths(log_file_path: str) -> list[Path]:
    path = Path(log_file_path)
    suffix = path.suffix or ".log"
    if not path.parent.exists():
        return []
    paths = [candidate for candidate in path.parent.glob(f"{path.stem}.*{suffix}") if candidate != path and candidate.is_file()]
    return sorted(paths, key=lambda candidate: candidate.name, reverse=True)
