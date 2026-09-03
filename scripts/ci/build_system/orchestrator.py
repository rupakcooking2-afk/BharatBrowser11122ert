"""Workflow orchestration state machine for fault-tolerant distributed Chromium builds.

Phase 7 — manages the entire build lifecycle as a deterministic state machine
with checkpointing, recovery, validation, and GitHub Releases-based resume.
"""
from __future__ import annotations

import logging
import os
import shutil
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

from .manifest import BuildManifest
from .checkpoint import CheckpointManager, read_ninja_stats, CHECKPOINT_INTERVAL_MINUTES, CHECKPOINT_INTERVAL_TARGETS
from .validator import BuildValidator, ValidationResult
from .recovery import RecoveryManager, auto_repair
from .performance import PerformanceTracker
from .release_validator import ReleaseValidator
from .disk_manager import DiskManager

__all__ = ["WorkflowOrchestrator", "WORKFLOW_STATES"]

logger = logging.getLogger(__name__)

WORKFLOW_STATES = frozenset({
    "IDLE", "PREPARING", "COMPILING", "CHECKPOINTING",
    "VERIFYING", "PACKAGING", "RELEASING", "COMPLETE",
    "FAILED", "RECOVERING",
})

# Removed: DOWNLOADING, PATCHING, CONFIGURING — these are handled by YAML

_NEXT_STATE: Dict[str, str] = {
    "IDLE": "PREPARING",
    "PREPARING": "COMPILING",
    "COMPILING": "VERIFYING",
    "CHECKPOINTING": "VERIFYING",
    "VERIFYING": "PACKAGING",
    "PACKAGING": "RELEASING",
    "RELEASING": "COMPLETE",
    "COMPLETE": "COMPLETE",
    "FAILED": "FAILED",
    "RECOVERING": "COMPILING",
}

WORKFLOW_TIMEOUT_HOURS = 6
MAX_CHECKPOINT_RETRIES = 3


def fast_ninja_count(ninja_log_path: Path) -> int:
    """Count completed Ninja edges by reading the log end + line count.

    Fast path: uses ``wc -l`` to avoid parsing the entire file.
    Falls back to ``read_ninja_stats`` when ``wc`` is not available.
    """
    if not ninja_log_path.is_file():
        return 0
    try:
        import shutil
        wc = shutil.which("wc")
        if wc:
            result = subprocess.run(
                [wc, "-l", str(ninja_log_path)],
                capture_output=True, text=True, timeout=30,
            )
            if result.returncode == 0:
                total_lines = int(result.stdout.strip().split()[0])
                return max(0, total_lines - 1)  # subtract header
    except (OSError, ValueError, subprocess.TimeoutExpired):
        pass
    # fallback: parse the log properly
    try:
        text = ninja_log_path.read_text(encoding="utf-8")
    except OSError:
        return 0
    count = 0
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or stripped.startswith("start"):
            continue
        count += 1
    return count


def _ninja_total_targets(build_path: Path) -> int:
    """Fast estimate of total build targets from build.ninja."""
    bn = build_path / "build.ninja"
    if not bn.is_file():
        return 57046
    try:
        import shutil
        wc = shutil.which("wc")
        if wc:
            result = subprocess.run(
                ["grep", "-c", "^build ", str(bn)],
                capture_output=True, text=True, timeout=60,
            )
            if result.returncode == 0:
                return max(int(result.stdout.strip()), 1)
    except (OSError, ValueError, subprocess.TimeoutExpired):
        pass
    return 57046


class WorkflowOrchestrator:
    """Deterministic state machine driving a distributed Chromium build."""

    def __init__(
        self,
        platform: str,
        build_dir: str,
        chromium_src: Path,
        browseros_dir: Path,
        repo_root: Path,
        start_state: str = "PREPARING",
    ) -> None:
        self.platform = platform
        self.build_dir = build_dir
        for _name, _val in (
            ("chromium_src", chromium_src),
            ("browseros_dir", browseros_dir),
            ("repo_root", repo_root),
        ):
            if callable(_val):
                raise RuntimeError(
                    f"Unevaluated callable received for path argument: {_name}"
                )
        self.chromium_src = Path(chromium_src).resolve()
        self.browseros_dir = Path(browseros_dir).resolve()
        self.repo_root = Path(repo_root).resolve()

        self.manifest = BuildManifest(platform, build_dir)
        self.checkpoint_mgr = CheckpointManager(platform, build_dir, self.chromium_src)
        self.validator = BuildValidator(self.chromium_src, build_dir, platform)
        self.recovery_mgr = RecoveryManager(self.chromium_src, build_dir, platform)
        self.tracker = PerformanceTracker()
        self.release_validator = ReleaseValidator(self.chromium_src, build_dir, platform)
        self.disk_mgr = DiskManager(self.chromium_src, build_dir, platform)

        self._start_time: float = time.perf_counter()
        # Set here so get_progress()/dashboard callers work even before
        # run() resets it.
        self._build_time_value: float = time.perf_counter()
        self._last_checkpoint_targets: int = 0
        self._last_checkpoint_time: float = time.perf_counter()
        self._last_checkpoint_wall_time: float = time.time()
        self._last_ninja_command: str = ""
        self._timeout_check_counter: int = 0
        self._checkpoint_retries: int = 0
        self._checkpoint_disabled: bool = False
        # None = no checkpoint attempted yet; True = last attempt persisted;
        # False = persistence failing (build must not claim resumability).
        self._checkpoint_healthy: Optional[bool] = None

        self._load_or_create_manifest()
        # Allow YAML to skip directly to COMPILING (patches+gn gen already done there)
        if start_state != "PREPARING":
            self.manifest["workflow_state"] = start_state

    def _load_or_create_manifest(self) -> None:
        mf = self.chromium_src / self.build_dir / "build_state.json"
        try:
            m = BuildManifest.load(mf)
            self.manifest = m
        except Exception:
            self.manifest = BuildManifest(self.platform, self.build_dir)

    @property
    def build_path(self) -> Path:
        return self.chromium_src / self.build_dir

    @property
    def manifest_path(self) -> Path:
        return self.build_path / "build_state.json"

    @property
    def ninja_log_path(self) -> Path:
        return self.build_path / ".ninja_log"

    # -- State management ---------------------------------------------------

    @property
    def current_state(self) -> str:
        return self.manifest["workflow_state"]

    def transition(self, new_state: str) -> None:
        if new_state not in WORKFLOW_STATES:
            raise ValueError(f"Invalid workflow state: {new_state}")
        old = self.current_state
        self.manifest["workflow_state"] = new_state
        # Save at key transitions only — not every intermediate hop
        if new_state in ("VERIFYING", "COMPLETE", "FAILED"):
            self.manifest.save(self.manifest_path)
        logger.info("Workflow state: %s -> %s", old, new_state)

    def should_stop(self) -> bool:
        """Check timeout once per minute (not every 10s poll)."""
        self._timeout_check_counter += 1
        if self._timeout_check_counter % 6 != 0:
            return False
        elapsed = time.perf_counter() - self._build_time
        if elapsed > WORKFLOW_TIMEOUT_HOURS * 3600:
            logger.warning("Workflow timeout after %.1f hours", elapsed / 3600)
            return True
        return False

    @property
    def _build_time(self) -> float:
        return self._build_time_value

    # -- Main loop ---------------------------------------------------------

    def run(self) -> int:
        self._build_time_value = time.perf_counter()
        state = self.current_state

        handler_map = {
            "IDLE": self.handle_idle,
            "PREPARING": self.handle_preparing,
            "COMPILING": self.handle_compiling,
            "CHECKPOINTING": self.handle_checkpointing,
            "VERIFYING": self.handle_verifying,
            "PACKAGING": self.handle_packaging,
            "RELEASING": self.handle_releasing,
            "FAILED": self.handle_failed,
            "RECOVERING": self.handle_recovering,
            "COMPLETE": self.handle_complete,
        }

        handler = handler_map.get(state)
        if handler is None:
            logger.error("Unknown state %r — transitioning to FAILED", state)
            self.transition("FAILED")
            return 1

        try:
            next_state = handler()
            if next_state is not None:
                self.transition(next_state)
            return 0
        except Exception as exc:
            logger.exception("Handler for %s raised %s", state, exc)
            self.transition("FAILED")
            return 1

    # -- State handlers -------------------------------------------------------

    def handle_idle(self) -> str:
        return "PREPARING"

    def handle_preparing(self) -> str:
        logger.info("Preparing build environment")
        self.manifest.create(self.chromium_src, self.browseros_dir, self.repo_root)
        self.manifest.save(self.manifest_path)
        return "COMPILING"

    @staticmethod
    def _locate_autoninja(chromium_src: Path) -> Optional[str]:
        """Locate the autoninja script, searching PATH then depot_tools."""
        autoninja = shutil.which("autoninja")
        if autoninja:
            logger.info("autoninja found on PATH: %s", autoninja)
            return autoninja

        # depot_tools lives alongside chromium_src: <chromium_root>/depot_tools
        depot_tools = chromium_src.parent / "depot_tools"
        if depot_tools.is_dir():
            # On Windows depot_tools uses .bat; on Linux/Mac it's a shell script
            candidates = [
                depot_tools / "autoninja.bat",
                depot_tools / "autoninja.cmd",
                depot_tools / "autoninja",
                depot_tools / "autoninja.py",
            ]
            for candidate in candidates:
                if candidate.is_file():
                    logger.info("autoninja found in depot_tools: %s", candidate)
                    # Add depot_tools to PATH so child processes find goma/gn/ etc.
                    os.environ["PATH"] = str(depot_tools) + os.pathsep + os.environ.get("PATH", "")
                    return str(candidate)

        raise RuntimeError(
            "autoninja not found on PATH or in depot_tools\n\n"
            "  Searched:\n"
            f"    PATH entries\n"
            f"    {depot_tools / 'autoninja*'}\n\n"
            "  Ensure depot_tools is installed and on PATH.\n"
            "  Run:  python scripts/ci/setup_chromium.py --step checkout\n"
        )

    def handle_compiling(self) -> str:
        if self.manifest["build_complete"]:
            logger.info("Build already complete — skipping to VERIFYING")
            return "VERIFYING"

        self.tracker.start_compile()

        # Restore only when local state is missing/invalid
        ninja_log = self.ninja_log_path
        needs_restore = not ninja_log.is_file() or ninja_log.stat().st_size == 0
        if needs_restore and self.checkpoint_mgr.has_checkpoint():
            logger.info("Restoring from GitHub checkpoint (local .ninja_log missing)")
            self.checkpoint_mgr.restore_state()

        self._last_checkpoint_targets = fast_ninja_count(self.ninja_log_path)
        self._last_checkpoint_time = time.perf_counter()
        self._last_checkpoint_wall_time = time.time()
        self._checkpoint_retries = 0
        self._checkpoint_disabled = False
        self._checkpoint_healthy = None

        ninja_path = self._locate_autoninja(self.chromium_src)
        cmd = [ninja_path, "-C", str(self.chromium_src / self.build_dir), "-k", "0"]

        build_dir = self.chromium_src / self.build_dir
        total_targets = _ninja_total_targets(build_dir)
        self._total_targets_hint = total_targets

        proc = subprocess.Popen(
            cmd,
            cwd=str(self.chromium_src),
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )

        while True:
            if self.should_stop():
                logger.warning("Stopping build due to timeout or cancellation")
                proc.terminate()
                # Upload final checkpoint before exiting (best-effort)
                try:
                    _, ok = self.checkpoint_mgr.create_checkpoint(
                        self._last_checkpoint_wall_time,
                    )
                    if ok:
                        self._checkpoint_healthy = True
                        self._last_checkpoint_wall_time = time.time()
                    else:
                        self._checkpoint_healthy = False
                        logger.warning("WARNING: checkpoint could not be persisted")
                except Exception as exc:
                    self._checkpoint_healthy = False
                    logger.warning("WARNING: checkpoint could not be persisted")
                    logger.warning("Final checkpoint upload failed: %s", exc)
                return "FAILED"

            retcode = proc.poll()
            elapsed_minutes = (time.perf_counter() - self._last_checkpoint_time) / 60.0

            # Fast ninja progress using wc -l
            completed = fast_ninja_count(self.ninja_log_path)
            self.tracker.targets_completed = completed
            self.tracker.total_targets = total_targets

            self._maybe_checkpoint(completed, elapsed_minutes)

            if retcode is not None:
                break
            time.sleep(10)

        completed = fast_ninja_count(self.ninja_log_path)
        self.tracker.targets_completed = completed
        self.tracker.total_targets = total_targets
        self.tracker.end_compile(total_targets)

        # A healthy compilation with broken checkpoint storage must still
        # finish — but it must NOT claim resumability.
        resumable_ok = self.checkpoints_saved()
        if proc.returncode == 0:
            self.manifest["checkpoint_healthy"] = resumable_ok
            self.manifest["build_complete"] = True
            self.manifest.save(self.manifest_path)
            logger.info("Build completed: %d/%d targets", completed, total_targets)
            if not resumable_ok:
                logger.warning(
                    "Build completed but checkpoints were NOT persisted — "
                    "the next run will start from scratch (NOT resumable)"
                )
            return "VERIFYING"

        logger.warning("Ninja exited code %d (%d/%d targets)", proc.returncode, completed, total_targets)
        if self._is_transient_failure(proc.returncode):
            return "RECOVERING"
        return "FAILED"

    def checkpoints_saved(self) -> bool:
        """Return ``True`` when checkpoint persistence is trustworthy.

        ``False`` only when an upload actually failed or checkpointing was
        disabled after repeated failures.  Never-triggered checkpoints
        (short builds) count as healthy.
        """
        if self._checkpoint_disabled:
            return False
        return self._checkpoint_healthy is not False

    def _maybe_checkpoint(self, completed: int, elapsed_minutes: float) -> None:
        """Trigger a rolling checkpoint when thresholds are exceeded.

        Checkpoint failures NEVER cancel the build: they are retried on the
        next threshold crossing and, after MAX_CHECKPOINT_RETRIES consecutive
        failures, checkpointing is disabled for the rest of the run while
        compilation continues.  The failure is reported loudly so the run is
        never mistaken for a resumable one.
        """
        if self._checkpoint_disabled:
            return
        # Do not checkpoint if progress has not advanced beyond the last
        # checkpoint. This prevents infinite loops when resuming from a
        # checkpoint at the same target count.
        if completed <= self._last_checkpoint_targets:
            logger.debug(
                "Skipping checkpoint: progress not advanced "
                "(completed=%d, last=%d, next_threshold=%d)",
                completed, self._last_checkpoint_targets,
                self._last_checkpoint_targets + CHECKPOINT_INTERVAL_TARGETS,
            )
            return
        if not self.checkpoint_mgr.should_checkpoint(
            elapsed_minutes, completed, self._last_checkpoint_targets
        ):
            return

        logger.info(
            "Checkpoint triggered at %d/%d targets",
            completed, self._total_targets_hint,
        )
        try:
            _, ok = self.checkpoint_mgr.create_checkpoint(
                self._last_checkpoint_wall_time,
            )
        except Exception as exc:
            logger.warning("Checkpoint failed: %s", exc)
            ok = False

        # Advance counters on every attempt (even failure) to prevent
        # an infinite retry loop when should_checkpoint keeps returning
        # True because _last_checkpoint_targets / _last_checkpoint_time
        # were never updated.
        self._last_checkpoint_targets = completed
        self._last_checkpoint_time = time.perf_counter()

        if ok:
            # Only advance the mtime cutoff when upload actually succeeded.
            # Without this guard, a failed checkpoint causes files in
            # the window to be permanently omitted from all future deltas.
            self._last_checkpoint_wall_time = time.time()
            self._checkpoint_retries = 0
            self._checkpoint_healthy = True
            return

        self._checkpoint_retries += 1
        self._checkpoint_healthy = False
        logger.warning("WARNING: checkpoint could not be persisted")
        if self._checkpoint_retries >= MAX_CHECKPOINT_RETRIES:
            logger.warning(
                "Checkpoint failed %d consecutive times — disabling "
                "all future checkpoints for this build (compilation "
                "continues; this run will NOT be resumable)",
                self._checkpoint_retries,
            )
            self._checkpoint_disabled = True

    def handle_checkpointing(self) -> str:
        logger.info("Creating final checkpoint")
        try:
            _, ok = self.checkpoint_mgr.create_checkpoint(self._last_checkpoint_wall_time)
            if ok:
                self._checkpoint_healthy = True
                self._last_checkpoint_wall_time = time.time()
            else:
                self._checkpoint_healthy = False
                logger.warning("WARNING: checkpoint could not be persisted")
        except Exception as exc:
            self._checkpoint_healthy = False
            logger.warning("WARNING: checkpoint could not be persisted")
            logger.warning("Final checkpoint failed: %s", exc)
        return "VERIFYING"

    def handle_verifying(self) -> str:
        logger.info("Validating build artifacts")
        result = self.validator.validate_build_state(self.manifest, self.chromium_src)
        if result.passed:
            logger.info("Build validation passed")
            return "PACKAGING"
        logger.warning("Build validation found %d failure(s)", len(result.failures))
        return "RECOVERING"

    def handle_packaging(self) -> str:
        logger.info("Packaging build artifacts")
        self.manifest["packaging_complete"] = True
        self.manifest.save(self.manifest_path)
        return "RELEASING"

    def handle_releasing(self) -> str:
        logger.info("Releasing build artifacts")
        self.manifest["release_complete"] = True
        self.manifest.save(self.manifest_path)
        return "COMPLETE"

    def handle_complete(self) -> Optional[str]:
        logger.info("Build workflow complete")
        return None

    def handle_failed(self) -> Optional[str]:
        logger.error("Build failed in state %s", self.current_state)
        self.manifest.save(self.manifest_path)
        return None

    def handle_recovering(self) -> str:
        logger.info("Attempting auto-repair")
        try:
            auto_repair(self.chromium_src, self.build_dir)
        except Exception as exc:
            logger.error("Auto-repair failed: %s", exc)
            return "FAILED"
        return "COMPILING"

    # -- Progress ----------------------------------------------------------

    def get_progress(self) -> Dict[str, Any]:
        completed = fast_ninja_count(self.ninja_log_path)
        total = self.tracker.total_targets or _ninja_total_targets(self.chromium_src / self.build_dir)
        elapsed = time.perf_counter() - self._build_time
        compile_rate = completed / elapsed if elapsed > 0 and completed > 0 else 0.0
        remaining = max(0, total - completed)
        estimated_remaining = remaining / compile_rate if compile_rate > 0 else 0.0

        return {
            "progress_percent": (completed / max(total, 1)) * 100.0,
            "completed_targets": completed,
            "remaining_targets": remaining,
            "elapsed_time_seconds": elapsed,
            "estimated_remaining_seconds": round(estimated_remaining, 1),
            "compile_rate": round(compile_rate, 2),
            "workflow_state": self.current_state,
            "checkpoint_number": self.manifest.get("checkpoint_counter", 0),
            "checkpoint_healthy": self.checkpoints_saved(),
            "build_attempt": self.manifest.get("build_attempt", 1),
            "last_ninja_command": self._last_ninja_command,
        }

    @staticmethod
    def _is_transient_failure(returncode: int) -> bool:
        return returncode in (1, 2, 130)
