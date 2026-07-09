"""Disk management module for fault-tolerant distributed Chromium build system.

Monitors disk usage, manages temporary files, enforces cache size limits,
and automatically cleans obsolete data.
"""

from __future__ import annotations

import logging
import shutil
import tempfile
import time
from pathlib import Path
from typing import Dict, Generator, List, Optional

__all__ = ["DiskManager", "format_bytes", "dir_size"]

logger = logging.getLogger(__name__)


def format_bytes(bytes_: int) -> str:
    if bytes_ < 0:
        return f"-{format_bytes(-bytes_)}"
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if bytes_ < 1024:
            return f"{bytes_:.1f} {unit}" if unit != "B" else f"{bytes_} B"
        bytes_ /= 1024
    return f"{bytes_:.1f} PB"


def dir_size(path: Path) -> int:
    if not path.is_dir():
        return 0
    total = 0
    for entry in path.rglob("*"):
        try:
            if entry.is_file():
                total += entry.stat().st_size
        except (PermissionError, OSError):
            continue
    return total


class DiskManager:
    """Monitor and manage disk resources during a distributed Chromium build."""

    def __init__(
        self,
        chromium_src: Path,
        build_dir: str,
        platform: str,
    ) -> None:
        self.chromium_src = chromium_src
        self.build_dir = build_dir
        self.platform = platform

    def disk_usage(self, path: Optional[Path] = None) -> dict:
        target = path or self.chromium_src
        usage = shutil.disk_usage(target)
        return {
            "total_gb": round(usage.total / (1024 ** 3), 2),
            "used_gb": round(usage.used / (1024 ** 3), 2),
            "free_gb": round(usage.free / (1024 ** 3), 2),
            "percent_used": round((usage.used / usage.total) * 100.0, 2),
        }

    def build_directory_size(self) -> int:
        return dir_size(Path(self.build_dir))

    def cache_sizes(self) -> dict:
        candidates = [
            Path.home() / ".ccache",
            Path.home() / "AppData" / "Local" / "ccache",
            Path.home() / "Library" / "Caches" / "ccache",
        ]
        ccache_bytes = 0
        ccache_path: Optional[Path] = None
        for candidate in candidates:
            if candidate.is_dir():
                size = dir_size(candidate)
                ccache_bytes += size
                if ccache_path is None:
                    ccache_path = candidate
        return {"ccache_bytes": ccache_bytes, "ccache_path": str(ccache_path) if ccache_path else ""}

    def temp_file_cleanup(self, max_age_hours: int = 24) -> int:
        cutoff = time.time() - max_age_hours * 3600
        deleted = 0
        directories: List[Path] = [
            Path(tempfile.gettempdir()),
        ]
        if self.platform != "win32":
            directories.append(Path("/tmp"))
        for tmp_dir in set(directories):
            if not tmp_dir.is_dir():
                continue
            for entry in tmp_dir.iterdir():
                try:
                    if entry.is_file() and entry.stat().st_mtime < cutoff:
                        entry.unlink()
                        deleted += 1
                    elif entry.is_dir() and entry.stat().st_mtime < cutoff:
                        try:
                            entry.rmdir()
                            deleted += 1
                        except OSError:
                            pass
                except (PermissionError, OSError):
                    pass
        logger.info("Temp cleanup: removed %d files older than %d hours", deleted, max_age_hours)
        return deleted

    def clean_stale_caches(self) -> dict:
        freed_bytes = 0
        deleted_paths: List[str] = []
        limit = 30 * (1024 ** 3)
        candidates = [
            Path.home() / ".ccache",
            Path.home() / "AppData" / "Local" / "ccache",
            Path.home() / "Library" / "Caches" / "ccache",
        ]
        for cache_dir in candidates:
            if not cache_dir.is_dir():
                continue
            size = dir_size(cache_dir)
            if size > limit:
                try:
                    shutil.rmtree(cache_dir)
                    freed_bytes += size
                    deleted_paths.append(str(cache_dir))
                    logger.info("Deleted stale cache at %s (%.2f GB)", cache_dir, size / (1024 ** 3))
                except PermissionError as exc:
                    logger.warning("Permission denied deleting %s: %s", cache_dir, exc)
                except OSError as exc:
                    logger.warning("Failed to delete %s: %s", cache_dir, exc)
        return {"freed_bytes": freed_bytes, "deleted_paths": deleted_paths}

    def enforce_disk_quota(self, max_usage_percent: float = 90.0) -> dict:
        freed_bytes = 0
        actions_taken: List[str] = []
        usage = self.disk_usage()
        if usage["percent_used"] < max_usage_percent:
            return {"freed_bytes": 0, "actions_taken": []}
        logger.warning(
            "Disk usage %.1f%% exceeds threshold %.1f%%, cleaning",
            usage["percent_used"], max_usage_percent,
        )
        n = self.temp_file_cleanup(max_age_hours=1)
        if n:
            actions_taken.append(f"Deleted {n} temp files")
        cache_result = self.clean_stale_caches()
        freed_bytes += cache_result["freed_bytes"]
        if cache_result["deleted_paths"]:
            actions_taken.append(f"Deleted caches: {', '.join(cache_result['deleted_paths'])}")
        return {"freed_bytes": freed_bytes, "actions_taken": actions_taken}

    def disk_warning(self) -> Optional[str]:
        usage = self.disk_usage()
        free_gb = usage["free_gb"]
        percent_used = usage["percent_used"]
        if free_gb < 5.0:
            return (
                f"CRITICAL: Only {free_gb:.1f} GB free. "
                f"Usage at {percent_used:.1f}%. Build may fail."
            )
        if percent_used > 95.0:
            return (
                f"WARNING: Disk usage at {percent_used:.1f}% "
                f"({free_gb:.1f} GB free). Consider cleaning."
            )
        return None
