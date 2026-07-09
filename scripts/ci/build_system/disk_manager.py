"""
Disk management module for fault-tolerant distributed Chromium build system.
Monitors disk usage, manages temporary files, enforces cache size limits,
and automatically cleans obsolete data.
"""

from __future__ import annotations

import logging
import shutil
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Dict, Generator, List, Optional, Tuple

__all__ = ["DiskManager", "format_bytes", "dir_size"]

logger = logging.getLogger(__name__)


def format_bytes(bytes_: int) -> str:
    """Convert byte count to human-readable string (e.g. '1.5 GB')."""
    if bytes_ < 0:
        return f"-{format_bytes(-bytes_)}"
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if bytes_ < 1024:
            return f"{bytes_:.1f} {unit}" if unit != "B" else f"{bytes_} B"
        bytes_ /= 1024
    return f"{bytes_:.1f} PB"


def dir_size(path: Path) -> int:
    """Recursively compute the total size (in bytes) of all files under *path*."""
    if not path.is_dir():
        return 0
    total = 0
    for entry in path.rglob("*"):
        try:
            if entry.is_file():
                total += entry.stat().st_size
        except PermissionError:
            logger.debug("Permission denied reading %s, skipping", entry)
        except OSError:
            logger.debug("OS error reading %s, skipping", entry)
    return total


class DiskManager:
    """Monitor and manage disk resources during a distributed Chromium build."""

    def __init__(
        self,
        chromium_src: Path,
        build_dir: str,
        platform: str,
        r2_client=None,
    ) -> None:
        self.chromium_src = chromium_src
        self.build_dir = build_dir
        self.platform = platform
        self.r2_client = r2_client

    # ------------------------------------------------------------------
    # Queries
    # ------------------------------------------------------------------

    def disk_usage(self, path: Optional[Path] = None) -> dict:
        """Return disk usage stats for *path* (defaults to build_dir)."""
        target = path or self.chromium_src
        usage = shutil.disk_usage(target)
        total_gb = usage.total / (1024 ** 3)
        used_gb = usage.used / (1024 ** 3)
        free_gb = usage.free / (1024 ** 3)
        percent_used = (usage.used / usage.total) * 100.0
        return {
            "total_gb": round(total_gb, 2),
            "used_gb": round(used_gb, 2),
            "free_gb": round(free_gb, 2),
            "percent_used": round(percent_used, 2),
        }

    def build_directory_size(self) -> int:
        """Return the recursive size (bytes) of the build output directory."""
        return dir_size(Path(self.build_dir))

    def cache_sizes(self) -> dict:
        """Return sizes for ccache / sccache directories across platforms."""
        candidates: List[Path] = []
        home = Path.home()

        # Linux / generic
        candidates.append(home / ".ccache")
        # Windows
        candidates.append(Path.home() / "AppData" / "Local" / "ccache")
        # macOS
        candidates.append(Path.home() / "Library" / "Caches" / "ccache")

        ccache_bytes = 0
        ccache_path: Optional[Path] = None
        for candidate in candidates:
            if candidate.is_dir():
                size = dir_size(candidate)
                ccache_bytes += size
                if ccache_path is None:
                    ccache_path = candidate

        return {"ccache_bytes": ccache_bytes, "ccache_path": str(ccache_path) if ccache_path else ""}

    def checkpoint_size(self, r2_client=None) -> int:
        """Return total size (bytes) of all checkpoints stored in R2."""
        client = r2_client or self.r2_client
        if client is None:
            logger.warning("No r2_client available, cannot query checkpoint size")
            return 0

        try:
            result = subprocess.run(
                ["r2", "ls", "--summarize", "--recursive"],
                capture_output=True,
                text=True,
                check=False,
            )
            if result.returncode != 0:
                logger.warning("r2 ls failed: %s", result.stderr.strip())
                return 0
            # Parse total size from summary line
            for line in result.stdout.splitlines():
                line_s = line.strip()
                if line_s.endswith("total_size") or "total" in line_s.lower():
                    parts = line_s.split()
                    if parts:
                        try:
                            return int(parts[-1])
                        except ValueError:
                            pass
            return 0
        except FileNotFoundError:
            logger.warning("r2 CLI not found on PATH")
            return 0

    # ------------------------------------------------------------------
    # Cleanup operations
    # ------------------------------------------------------------------

    def temp_file_cleanup(self, max_age_hours: int = 24) -> int:
        """Delete temp files older than *max_age_hours*; return count deleted."""
        cutoff = time.time() - max_age_hours * 3600
        deleted = 0
        directories: List[Path] = [
            Path(tempfile.gettempdir()),
            Path("/tmp") if self.platform != "win32" else Path(tempfile.gettempdir()),
        ]

        for tmp_dir in set(directories):
            if not tmp_dir.is_dir():
                continue
            for entry in tmp_dir.iterdir():
                try:
                    if entry.is_file() and entry.stat().st_mtime < cutoff:
                        entry.unlink()
                        deleted += 1
                    elif entry.is_dir():
                        # Clean up empty old directories
                        if entry.stat().st_mtime < cutoff:
                            try:
                                entry.rmdir()
                                deleted += 1
                            except OSError:
                                pass
                except PermissionError:
                    pass
                except OSError:
                    pass
        logger.info("Temp file cleanup: removed %d files older than %d hours", deleted, max_age_hours)
        return deleted

    def clean_old_checkpoints(self, r2_client=None, retention: int = 5) -> int:
        """Delete old checkpoints keeping the *retention* newest; return count deleted."""
        client = r2_client or self.r2_client

        # Try delegating to CheckpointManager first
        try:
            from scripts.ci.build_system.checkpoint_manager import CheckpointManager
            cm = CheckpointManager(self.chromium_src, self.build_dir, self.platform, client)
            return cm.prune_old_checkpoints(retention)
        except ImportError:
            logger.info("CheckpointManager not available, falling back to direct r2 rm")
        except AttributeError:
            logger.info("CheckpointManager has no prune_old_checkpoints, falling back")

        if client is None:
            logger.warning("No r2_client and no CheckpointManager — cannot clean checkpoints")
            return 0

        try:
            # List checkpoints sorted by last-modified, keep newest N
            result = subprocess.run(
                ["r2", "ls", "--recursive", "--format=json"],
                capture_output=True,
                text=True,
                check=False,
            )
            if result.returncode != 0:
                logger.warning("r2 ls failed: %s", result.stderr.strip())
                return 0

            import json
            objects = json.loads(result.stdout)
            # Sort by last_modified descending
            objects.sort(key=lambda o: o.get("last_modified", ""), reverse=True)
            to_delete = objects[retention:]

            deleted = 0
            for obj in to_delete:
                key = obj.get("key", "")
                if not key:
                    continue
                rm_result = subprocess.run(
                    ["r2", "rm", key],
                    capture_output=True,
                    text=True,
                    check=False,
                )
                if rm_result.returncode == 0:
                    deleted += 1
                else:
                    logger.warning("Failed to delete checkpoint %s: %s", key, rm_result.stderr.strip())
            logger.info("Cleaned %d old checkpoints (retention=%d)", deleted, retention)
            return deleted
        except FileNotFoundError:
            logger.warning("r2 CLI not found on PATH")
            return 0
        except (json.JSONDecodeError, KeyError) as exc:
            logger.warning("Failed to parse r2 output: %s", exc)
            return 0

    def clean_stale_caches(self) -> dict:
        """Delete ccache / sccache caches exceeding limits; return freed bytes and paths."""
        freed_bytes = 0
        deleted_paths: List[str] = []
        limit = 30 * (1024 ** 3)  # 30 GB

        candidates: List[Path] = [
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
                    logger.warning("Permission denied deleting cache %s: %s", cache_dir, exc)
                except OSError as exc:
                    logger.warning("Failed to delete cache %s: %s", cache_dir, exc)

        return {"freed_bytes": freed_bytes, "deleted_paths": deleted_paths}

    # ------------------------------------------------------------------
    # Quota enforcement
    # ------------------------------------------------------------------

    def enforce_disk_quota(self, max_usage_percent: float = 90.0) -> dict:
        """If disk usage exceeds threshold, clean aggressively and return report."""
        freed_bytes = 0
        actions_taken: List[str] = []
        usage = self.disk_usage()

        if usage["percent_used"] < max_usage_percent:
            logger.info(
                "Disk usage %.1f%% below threshold %.1f%%, no cleanup needed",
                usage["percent_used"],
                max_usage_percent,
            )
            return {"freed_bytes": 0, "actions_taken": []}

        logger.warning(
            "Disk usage %.1f%% exceeds threshold %.1f%%, starting aggressive cleanup",
            usage["percent_used"],
            max_usage_percent,
        )

        # 1. Clean temp files
        n = self.temp_file_cleanup(max_age_hours=1)
        if n:
            actions_taken.append(f"Deleted {n} temp files")

        # 2. Clean stale caches
        cache_result = self.clean_stale_caches()
        freed_bytes += cache_result["freed_bytes"]
        if cache_result["deleted_paths"]:
            actions_taken.append(f"Deleted caches: {', '.join(cache_result['deleted_paths'])}")

        # 3. Clean old checkpoints
        n_cp = self.clean_old_checkpoints(retention=3)
        if n_cp:
            actions_taken.append(f"Deleted {n_cp} old checkpoints")

        return {"freed_bytes": freed_bytes, "actions_taken": actions_taken}

    def disk_warning(self) -> Optional[str]:
        """Return warning string if disk space is critical, else None."""
        usage = self.disk_usage()
        free_gb = usage["free_gb"]
        percent_used = usage["percent_used"]

        if free_gb < 5.0:
            return (
                f"CRITICAL: Only {free_gb:.1f} GB free on build volume. "
                f"Usage at {percent_used:.1f}%. Build may fail."
            )
        if percent_used > 95.0:
            return (
                f"WARNING: Disk usage at {percent_used:.1f}% "
                f"({free_gb:.1f} GB free). Consider cleaning."
            )
        return None

    # ------------------------------------------------------------------
    # Monitoring generator
    # ------------------------------------------------------------------

    def monitor(self, build_src: Path, interval_seconds: int = 300) -> Generator[dict, None, None]:
        """Yield disk usage stats every *interval_seconds* during long builds."""
        while True:
            stats = self.disk_usage(build_src)
            stats["build_directory_bytes"] = dir_size(Path(self.build_dir))
            stats["cache"] = self.cache_sizes()
            yield stats
            time.sleep(interval_seconds)