"""Performance reporting module for fault-tolerant distributed Chromium build system."""

from __future__ import annotations

import json
import os
import time
from pathlib import Path
from typing import List, Optional


__all__ = ["PerformanceTracker", "collect_system_stats", "parse_ccache_stats"]

try:
    import psutil as _psutil

    _HAS_PSUTIL = True
except ImportError:
    _HAS_PSUTIL = False


class PerformanceTracker:
    """Tracks and reports build performance metrics."""

    def __init__(self) -> None:
        self.start_time: float = time.perf_counter()
        self.compile_start: Optional[float] = None
        self.compile_end: Optional[float] = None
        self.targets_completed: int = 0
        self.total_targets: int = 0
        self.checkpoint_times: list[float] = []
        self.upload_times: list[float] = []
        self.download_times: list[float] = []
        self.cache_hits: int = 0
        self.cache_misses: int = 0
        self.bytes_uploaded: int = 0
        self.bytes_downloaded: int = 0
        self.peak_cpu_percent: float = 0.0
        self.peak_memory_mb: float = 0.0
        self.peak_disk_usage_gb: float = 0.0

    # ------------------------------------------------------------------
    # Recording methods
    # ------------------------------------------------------------------

    def start_compile(self) -> None:
        self.compile_start = time.perf_counter()

    def end_compile(self, targets: int) -> None:
        self.compile_end = time.perf_counter()
        self.total_targets = targets

    def record_checkpoint(self, duration_seconds: float, size_bytes: int) -> None:
        self.checkpoint_times.append(duration_seconds)

    def record_upload(self, duration_seconds: float, size_bytes: int) -> None:
        self.upload_times.append(duration_seconds)
        self.bytes_uploaded += size_bytes

    def record_download(self, duration_seconds: float, size_bytes: int) -> None:
        self.download_times.append(duration_seconds)
        self.bytes_downloaded += size_bytes

    def record_cache_stats(self, hits: int, misses: int) -> None:
        self.cache_hits = hits
        self.cache_misses = misses

    def update_resource_usage(
        self, cpu_percent: float, memory_mb: float, disk_gb: float
    ) -> None:
        if cpu_percent > self.peak_cpu_percent:
            self.peak_cpu_percent = cpu_percent
        if memory_mb > self.peak_memory_mb:
            self.peak_memory_mb = memory_mb
        if disk_gb > self.peak_disk_usage_gb:
            self.peak_disk_usage_gb = disk_gb

    # ------------------------------------------------------------------
    # Derived metrics
    # ------------------------------------------------------------------

    def compile_speed(self) -> float:
        if self.targets_completed <= 0:
            return 0.0
        if self.compile_start is None or self.compile_end is None:
            return 0.0
        elapsed = self.compile_end - self.compile_start
        if elapsed <= 0:
            return 0.0
        return self.targets_completed / elapsed

    def cache_hit_ratio(self) -> float:
        total = self.cache_hits + self.cache_misses
        if total == 0:
            return 0.0
        return self.cache_hits / total

    def checkpoint_overhead(self) -> float:
        total_checkpoint = sum(self.checkpoint_times)
        if self.compile_start is None or self.compile_end is None:
            return 0.0
        compile_duration = self.compile_end - self.compile_start
        if compile_duration <= 0:
            return 0.0
        return total_checkpoint / compile_duration

    def upload_speed(self) -> float:
        total_time = sum(self.upload_times)
        if total_time <= 0:
            return 0.0
        return self.bytes_uploaded / total_time

    def download_speed(self) -> float:
        total_time = sum(self.download_times)
        if total_time <= 0:
            return 0.0
        return self.bytes_downloaded / total_time

    def resume_efficiency(self, previous_targets: int) -> float:
        total = previous_targets + self.targets_completed
        if total == 0:
            return 0.0
        return self.targets_completed / total

    def estimated_time_remaining(self) -> Optional[float]:
        speed = self.compile_speed()
        if speed <= 0:
            return None
        remaining = self.total_targets - self.targets_completed
        if remaining <= 0:
            return 0.0
        return remaining / speed

    # ------------------------------------------------------------------
    # Serialisation / persistence
    # ------------------------------------------------------------------

    def to_dict(self) -> dict:
        return {
            "start_time": self.start_time,
            "compile_start": self.compile_start,
            "compile_end": self.compile_end,
            "targets_completed": self.targets_completed,
            "total_targets": self.total_targets,
            "checkpoint_times": self.checkpoint_times,
            "upload_times": self.upload_times,
            "download_times": self.download_times,
            "cache_hits": self.cache_hits,
            "cache_misses": self.cache_misses,
            "bytes_uploaded": self.bytes_uploaded,
            "bytes_downloaded": self.bytes_downloaded,
            "peak_cpu_percent": self.peak_cpu_percent,
            "peak_memory_mb": self.peak_memory_mb,
            "peak_disk_usage_gb": self.peak_disk_usage_gb,
            "compile_speed": self.compile_speed(),
            "cache_hit_ratio": self.cache_hit_ratio(),
            "checkpoint_overhead": self.checkpoint_overhead(),
            "upload_speed": self.upload_speed(),
            "download_speed": self.download_speed(),
        }

    def save_report(self, path: Path) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(self.to_dict(), fh, indent=2)

    @classmethod
    def load_report(cls, path: Path) -> PerformanceTracker:
        with open(path, "r", encoding="utf-8") as fh:
            data = json.load(fh)
        obj = cls()
        obj.start_time = data.get("start_time", obj.start_time)
        obj.compile_start = data.get("compile_start")
        obj.compile_end = data.get("compile_end")
        obj.targets_completed = data.get("targets_completed", 0)
        obj.total_targets = data.get("total_targets", 0)
        obj.checkpoint_times = data.get("checkpoint_times", [])
        obj.upload_times = data.get("upload_times", [])
        obj.download_times = data.get("download_times", [])
        obj.cache_hits = data.get("cache_hits", 0)
        obj.cache_misses = data.get("cache_misses", 0)
        obj.bytes_uploaded = data.get("bytes_uploaded", 0)
        obj.bytes_downloaded = data.get("bytes_downloaded", 0)
        obj.peak_cpu_percent = data.get("peak_cpu_percent", 0.0)
        obj.peak_memory_mb = data.get("peak_memory_mb", 0.0)
        obj.peak_disk_usage_gb = data.get("peak_disk_usage_gb", 0.0)
        return obj


# ------------------------------------------------------------------
# System-level helpers
# ------------------------------------------------------------------


def collect_system_stats() -> dict:
    """Collect current CPU, memory, and disk usage statistics.

    Falls back to approximate values when *psutil* is not installed.
    """
    stats: dict = {}

    if _HAS_PSUTIL:
        stats["cpu_percent"] = _psutil.cpu_percent(interval=0.1)
        mem = _psutil.virtual_memory()
        stats["memory_total_mb"] = mem.total / (1024 * 1024)
        stats["memory_available_mb"] = mem.available / (1024 * 1024)
        stats["memory_percent"] = mem.percent
        disk = _psutil.disk_usage("/")
        stats["disk_total_gb"] = disk.total / (1024**3)
        stats["disk_free_gb"] = disk.free / (1024**3)
        stats["disk_percent"] = disk.percent
    else:
        stats["cpu_percent"] = _approx_cpu_percent()
        stats["memory_total_mb"] = 0.0
        stats["memory_available_mb"] = 0.0
        stats["memory_percent"] = 0.0
        stats["disk_total_gb"] = 0.0
        stats["disk_free_gb"] = 0.0
        stats["disk_percent"] = 0.0

    try:
        stats["load_avg"] = list(os.getloadavg())
    except (AttributeError, OSError):
        stats["load_avg"] = []

    stats["timestamp"] = time.time()
    return stats


def _approx_cpu_percent() -> float:
    """Rough CPU estimate based on /proc/stat or idle loop fallback."""
    try:
        with open("/proc/stat", "r") as fh:
            line = fh.readline()
        parts = line.strip().split()
        if parts and parts[0] == "cpu" and len(parts) > 4:
            user, nice_, system, idle = int(parts[1]), int(parts[2]), int(parts[3]), int(parts[4])
            total = user + nice_ + system + idle
            return 100.0 * (1.0 - idle / total) if total else 0.0
    except (FileNotFoundError, OSError, IndexError, ValueError):
        pass
    return 50.0


def parse_ccache_stats() -> dict:
    """Parse ``ccache -s`` output into a dictionary.

    Returns an empty dict if ccache is not available or fails.
    """
    import subprocess

    try:
        result = subprocess.run(
            ["ccache", "-s"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode != 0:
            return {}
    except (FileNotFoundError, subprocess.TimeoutExpired, OSError):
        return {}

    stats: dict = {}
    lines = result.stdout.splitlines()

    for line in lines:
        line_stripped = line.strip().lower()
        if "cache hit rate" in line_stripped:
            parts = line.split(":")
            if len(parts) == 2:
                val = parts[1].strip().replace("%", "").strip()
                try:
                    stats["cache_hit_rate"] = float(val)
                except ValueError:
                    pass
        elif "cache size" in line_stripped:
            parts = line.split(":")
            if len(parts) == 2:
                stats["cache_size"] = parts[1].strip()
        elif "files in cache" in line_stripped:
            parts = line.split(":")
            if len(parts) == 2:
                val = parts[1].strip()
                try:
                    stats["files_cached"] = int(val)
                except ValueError:
                    pass

    return stats