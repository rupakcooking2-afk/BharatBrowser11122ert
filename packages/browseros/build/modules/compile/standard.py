#!/usr/bin/env python3
"""Standard single-architecture build module for Bharat Browser"""

import os
import sys
import tempfile
import shutil
from pathlib import Path
from typing import List, Mapping, Optional
from ...common.module import CommandModule, ValidationError
from ...common.context import Context
from ...common.utils import (
    run_command,
    log_info,
    log_success,
    log_warning,
    join_paths,
    IS_WINDOWS,
)

GB_PER_COMPILE_JOB = 4
GB_PER_COMPILE_JOB_POSIX = 2  # Linux/macOS handle overcommit; can push harder

# Enable cache stats logging (ccache / sccache)
_CCACHE_STATS_QUERIED = False
_SCCACHE_STATS_QUERIED = False


def _run_and_log_cache_cmd(cmd: list[str], label: str) -> None:
    """Run a cache-query command and log key lines."""
    try:
        result = run_command(cmd, check=False)
        if result and result.returncode == 0:
            for line in result.stdout.splitlines():
                line = line.strip()
                if any(kw in line for kw in ("cache hit", "cache miss", "files in cache", "cache size")):
                    log_info(f"  {label}: {line}")
    except (FileNotFoundError, AttributeError, OSError):
        pass


def log_cache_stats() -> None:
    """Log ccache and sccache hit-rate stats once per build."""
    global _CCACHE_STATS_QUERIED, _SCCACHE_STATS_QUERIED
    if not _CCACHE_STATS_QUERIED:
        _CCACHE_STATS_QUERIED = True
        _run_and_log_cache_cmd(["ccache", "-s"], "ccache")
    if not _SCCACHE_STATS_QUERIED:
        _SCCACHE_STATS_QUERIED = True
        _run_and_log_cache_cmd(["sccache", "--show-stats"], "sccache")


def _total_memory_gb() -> Optional[float]:
    """Total physical RAM in GB; None when unavailable.  Cross-platform."""
    try:
        if sys.platform == "win32":
            import ctypes

            class MEMORYSTATUSEX(ctypes.Structure):
                _fields_ = [
                    ("dwLength", ctypes.c_uint32),
                    ("dwMemoryLoad", ctypes.c_uint32),
                    ("ullTotalPhys", ctypes.c_uint64),
                    ("ullAvailPhys", ctypes.c_uint64),
                    ("ullTotalPageFile", ctypes.c_uint64),
                    ("ullAvailPageFile", ctypes.c_uint64),
                    ("ullTotalVirtual", ctypes.c_uint64),
                    ("ullAvailVirtual", ctypes.c_uint64),
                    ("ullAvailExtendedVirtual", ctypes.c_uint64),
                ]

            status = MEMORYSTATUSEX()
            status.dwLength = ctypes.sizeof(MEMORYSTATUSEX)
            if not ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(status)):
                return None
            return status.ullTotalPhys / (1024**3)
        else:
            # Linux / macOS: read from /proc/meminfo or sysctl
            if sys.platform == "linux":
                with open("/proc/meminfo") as f:
                    for line in f:
                        if line.startswith("MemTotal:"):
                            kb = int(line.split()[1])
                            return kb / (1024 * 1024)
            elif sys.platform == "darwin":
                import subprocess
                result = subprocess.run(
                    ["sysctl", "-n", "hw.memsize"],
                    capture_output=True, text=True, check=False,
                )
                if result.returncode == 0:
                    return int(result.stdout.strip()) / (1024**3)
            return None
    except Exception:
        return None


def _available_memory_gb() -> Optional[float]:
    """Available (free) physical RAM in GB; None when unavailable.  Cross-platform.

    On Windows uses GlobalMemoryStatusEx ullAvailPhys.  On Linux/macOS this
    is a best-effort approximation since free memory reporting varies by OS.
    """
    try:
        if sys.platform == "win32":
            import ctypes

            class MEMORYSTATUSEX(ctypes.Structure):
                _fields_ = [
                    ("dwLength", ctypes.c_uint32),
                    ("dwMemoryLoad", ctypes.c_uint32),
                    ("ullTotalPhys", ctypes.c_uint64),
                    ("ullAvailPhys", ctypes.c_uint64),
                    ("ullTotalPageFile", ctypes.c_uint64),
                    ("ullAvailPageFile", ctypes.c_uint64),
                    ("ullTotalVirtual", ctypes.c_uint64),
                    ("ullAvailVirtual", ctypes.c_uint64),
                    ("ullAvailExtendedVirtual", ctypes.c_uint64),
                ]

            status = MEMORYSTATUSEX()
            status.dwLength = ctypes.sizeof(MEMORYSTATUSEX)
            if not ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(status)):
                return None
            return status.ullAvailPhys / (1024**3)
        else:
            # Linux: read MemAvailable from /proc/meminfo if present,
            # otherwise fall back to estimating from MemTotal - buffers/cache.
            # macOS: no simple one-liner; return None to use total-based calc.
            if sys.platform == "linux":
                try:
                    with open("/proc/meminfo") as f:
                        for line in f:
                            if line.startswith("MemAvailable:"):
                                kb = int(line.split()[1])
                                return kb / (1024 * 1024)
                except OSError:
                    pass
            return None
    except Exception:
        return None


def compute_ninja_jobs(env: Optional[Mapping[str, str]] = None) -> Optional[int]:
    """Resolve the -j value: env override, else adaptive RAM+CPU cap, else autoninja default.

    The returned job count balances two constraints:
      * RAM available  — we must not spawn so many clang-cl jobs that total memory
        exceeds what the runner has (clang-cl processes peak ~4 GB on Windows, ~2 GB on
        Linux/macOS).  We use *available* physical RAM with a safety margin.
      * CPU count       — we do not exceed the number of logical CPUs, but we also
        avoid the naive "use all cores" when RAM is tight.

    Returns None when RAM cannot be determined, in which case autoninja decides.
    """
    if env is None:
        env = os.environ

    override = env.get("BROWSEROS_NINJA_JOBS")
    if override is not None:
        try:
            jobs = int(override)
        except ValueError:
            jobs = 0
        if jobs > 0:
            log_info(f"Ninja parallelism: -j {jobs} (BROWSEROS_NINJA_JOBS override)")
            # Even when overridden, log a warning if the value seems to exceed safe RAM limits.
            # We do not silently allow OOM; the user is responsible for a sensible value.
            return jobs
        log_warning(f"Ignoring invalid BROWSEROS_NINJA_JOBS={override!r}")

    # --- Adaptive: determine job count from available RAM + CPU cap ---

    total_gb = _total_memory_gb()
    avail_gb = _available_memory_gb()

    # Choose the "per-job" memory floor: 4 GB on Windows (clang-cl no overcommit),
    # 2 GB on Linux/macOS (overcommit is safer there).
    gb_per_job = GB_PER_COMPILE_JOB if IS_WINDOWS() else GB_PER_COMPILE_JOB_POSIX

    # Derive a RAM-constrained maximum: use AVAILABLE RAM with a safety margin.
    # We leave ~15 % headroom so the OS and other processes don't get starved.
    if avail_gb is not None and avail_gb > 0:
        max_by_avail = max(1, int(avail_gb * 0.85 / gb_per_job))
    elif total_gb is not None and total_gb > 0:
        # Fall back to total RAM (no availability data); still apply margin.
        max_by_avail = max(1, int(total_gb * 0.85 / gb_per_job))
    else:
        max_by_avail = None

    cpus = os.cpu_count()

    # Decide the final job count:
    #   • If we have a RAM-derived max, cap by it (with CPU as secondary limit).
    #   • If we cannot determine RAM, fall back to CPU cap alone.
    #   • If BROWSEROS_NINJA_JOBS was already handled above; we reach here only
    #     when it was absent/empty/invalid.
    if max_by_avail is not None:
        # RAM-aware: use the lower of (available-RAM-derived max) and (CPU count).
        # This prevents OOM while still using available cores when RAM is plentiful.
        jobs = min(max_by_avail, cpus) if cpus else max_by_avail
        log_info(
            f"Ninja parallelism: -j {jobs} (available RAM {avail_gb or 0:.1f} GB / "
            f"{gb_per_job} GB per job with 15%% margin; capped by {cpus} CPU cores)"
        )
    else:
        # No RAM info at all — fall back to CPU cap (autoninja default path).
        jobs = cpus if cpus else 0
        log_info(
            f"Ninja parallelism: -j {jobs} (CPU count={cpus}; "
            "RAM unavailable, using CPU count; override with BROWSEROS_NINJA_JOBS)"
        )
    return jobs


def autoninja_command(
    out_dir: str, targets: List[str], env: Optional[Mapping[str, str]] = None
) -> List[str]:
    """Assemble the autoninja argv with the resolved -j parallelism applied."""
    cmd = ["autoninja.bat" if IS_WINDOWS() else "autoninja", "-C", out_dir]
    jobs = compute_ninja_jobs(env)
    if jobs is not None:
        cmd += ["-j", str(jobs)]
    else:
        log_info("Ninja parallelism: autoninja default")
    return cmd + list(targets)


class CompileModule(CommandModule):
    produces = ["built_app"]
    requires = []
    description = "Build Bharat Browser using autoninja"

    def validate(self, ctx: Context) -> None:
        if not ctx.chromium_src.exists():
            raise ValidationError(f"Chromium source not found: {ctx.chromium_src}")

        if not ctx.browseros_chromium_version:
            raise ValidationError("Bharat Browser chromium version not set")

        args_file = ctx.get_gn_args_file()
        if not args_file.exists():
            raise ValidationError(f"Build not configured - args.gn not found: {args_file}")

    def execute(self, ctx: Context) -> None:
        log_info("\n🔨 Building Bharat Browser (this will take a while)...")

        self._create_version_file(ctx)

        log_cache_stats()

        run_command(
            autoninja_command(ctx.out_dir, ["chrome", "chromedriver"]),
            cwd=ctx.chromium_src,
        )

        log_cache_stats()

        app_path = ctx.get_chromium_app_path()
        new_path = ctx.get_app_path()

        if app_path.exists() and not new_path.exists():
            shutil.move(str(app_path), str(new_path))

        ctx.artifact_registry.add("built_app", new_path)

        log_success("Build complete!")

    def _create_version_file(self, ctx: Context) -> None:
        parts = ctx.browseros_chromium_version.split(".")
        if len(parts) != 4:
            log_warning(f"Invalid version format: {ctx.browseros_chromium_version}")
            return

        version_content = f"MAJOR={parts[0]}\nMINOR={parts[1]}\nBUILD={parts[2]}\nPATCH={parts[3]}"

        with tempfile.NamedTemporaryFile(mode="w", delete=False) as temp_file:
            temp_file.write(version_content)
            temp_path = temp_file.name

        chrome_version_path = join_paths(ctx.chromium_src, "chrome", "VERSION")
        shutil.copy2(temp_path, chrome_version_path)
        Path(temp_path).unlink()

        log_info(f"Created VERSION file: {ctx.browseros_chromium_version}")


def build_target(ctx: Context, target: str) -> bool:
    """Build a specific target (e.g., mini_installer)"""
    log_info(f"\n🔨 Building target: {target}")

    run_command(autoninja_command(ctx.out_dir, [target]), cwd=ctx.chromium_src)

    log_success(f"Target {target} built successfully")
    return True
