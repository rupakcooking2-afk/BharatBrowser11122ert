"""Workflow orchestration state machine for fault-tolerant distributed Chromium builds.

Phase 7 — manages the entire build lifecycle as a deterministic state machine
with checkpointing, recovery, validation, and R2-based resume.
"""

from __future__ import annotations

import logging
import os
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

from .manifest import BuildManifest
from .checkpoint import CheckpointManager, read_ninja_stats, CHECKPOINT_INTERVAL_MINUTES, CHECKPOINT_INTERVAL_TARGETS
from .validator import BuildValidator, ValidationResult
from .recovery import RecoveryManager, auto_repair
from .uploader import UploadManager
from .performance import PerformanceTracker
from .release_validator import ReleaseValidator
from .disk_manager import DiskManager

__all__ = ["WorkflowOrchestrator", "WORKFLOW_STATES"]

logger = logging.getLogger(__name__)

WORKFLOW_STATES = frozenset({
    "IDLE",
    "PREPARING",
    "DOWNLOADING",
    "PATCHING",
    "CONFIGURING",
    "COMPILING",
    "CHECKPOINTING",
    "VERIFYING",
    "PACKAGING",
    "RELEASING",
    "COMPLETE",
    "FAILED",
    "RECOVERING",
})

_NEXT_STATE: Dict[str, str] = {
    "IDLE": "PREPARING",
    "PREPARING": "DOWNLOADING",
    "DOWNLOADING": "PATCHING",
    "PATCHING": "CONFIGURING",
    "CONFIGURING": "COMPILING",
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


def read_ninja_progress(ninja_log_path: Path) -> Tuple[int, int]:
    completed, total, _ = read_ninja_stats(ninja_log_path)
    return (completed, total)


class WorkflowOrchestrator:
    """Deterministic state machine driving a distributed Chromium build."""

    def __init__(
        self,
        platform: str,
        build_dir: str,
        chromium_src: Path,
        browseros_dir: Path,
        repo_root: Path,
    ) -> None:
        self.platform = platform
        self.build_dir = build_dir
        self.chromium_src = Path(chromium_src)
        self.browseros_dir = Path(browseros_dir)
        self.repo_root = Path(repo_root)

        self.manifest = BuildManifest(platform, build_dir)
        self.checkpoint_mgr = CheckpointManager(platform, build_dir, self.chromium_src)
        self.validator = BuildValidator(self.chromium_src, build_dir, platform)
        self.recovery_mgr = RecoveryManager(platform, build_dir, self.chromium_src)
        self.uploader = UploadManager(platform)
        self.tracker = PerformanceTracker()
        self.release_validator = ReleaseValidator(self.chromium_src, build_dir, platform)
        self.disk_mgr = DiskManager(self.chromium_src, build_dir, platform)

        self._start_time: float = time.perf_counter()
        self._last_checkpoint_targets: int = 0
        self._last_checkpoint_time: float = time.perf_counter()
        self._last_ninja_command: str = ""

        self._load_or_create_manifest()

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
        self.manifest.save(self.manifest_path)
        logger.info("Workflow state: %s -> %s", old, new_state)

    def should_stop(self) -> bool:
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
            "DOWNLOADING": self.handle_downloading,
            "PATCHING": self.handle_patching,
            "CONFIGURING": self.handle_configuring,
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
        return "DOWNLOADING"

    def handle_downloading(self) -> str:
        logger.info("Downloading build artifacts from R2")
        r2_prefix = f"build-resume/{self.platform}"
        self.uploader.download_directory(r2_prefix, self.chromium_src / self.build_dir)
        return "PATCHING"

    def handle_patching(self) -> str:
        logger.info("Applying patches")
        patch_dir = self.repo_root / "patches"
        if patch_dir.is_dir():
            for patch_file in sorted(patch_dir.glob("*.patch")):
                result = subprocess.run(
                    ["git", "am", str(patch_file)],
                    cwd=str(self.chromium_src),
                    capture_output=True,
                    text=True,
                    timeout=120,
                )
                if result.returncode != 0:
                    logger.warning("Patch %s applied with issues: %s", patch_file.name, result.stderr.strip())
        else:
            logger.info("No patch directory found at %s", patch_dir)
        return "CONFIGURING"

    def handle_configuring(self) -> str:
        logger.info("Running gn gen")
        out_dir = self.chromium_src / self.build_dir
        out_dir.mkdir(parents=True, exist_ok=True)
        result = subprocess.run(
            ["gn", "gen", self.build_dir],
            cwd=str(self.chromium_src),
            capture_output=True,
            text=True,
            timeout=300,
        )
        if result.returncode != 0:
            logger.error("gn gen failed: %s", result.stderr.strip())
            return "FAILED"
        logger.info("gn gen completed successfully")
        return "COMPILING"

    def handle_compiling(self) -> str:
        if self.manifest["build_complete"]:
            logger.info("Build already marked complete — skipping to VERIFYING")
            return "VERIFYING"

        self.tracker.start_compile()
        self._last_checkpoint_targets = 0
        self._last_checkpoint_time = time.perf_counter()

        if self.checkpoint_mgr.get_latest_checkpoint() is not None:
            logger.info("Restoring latest checkpoint")
            self.checkpoint_mgr.restore_latest(self.chromium_src / self.build_dir)

        ninja_path = "autoninja"
        ninja_log_arg = f"-d explain -j 0"
        cmd = [ninja_path, "-C", str(self.chromium_src / self.build_dir), "-k", "0"]
        self._last_ninja_command = " ".join(cmd)

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
                return "FAILED"

            retcode = proc.poll()
            elapsed_minutes = (time.perf_counter() - self._last_checkpoint_time) / 60.0
            completed, total = read_ninja_progress(self.ninja_log_path)
            self.tracker.targets_completed = completed
            self.tracker.total_targets = total

            if self.checkpoint_mgr.should_checkpoint(
                elapsed_minutes, completed, self._last_checkpoint_targets
            ):
                logger.info("Checkpoint triggered at %d/%d targets", completed, total)
                try:
                    self.checkpoint_mgr.create_checkpoint()
                    self._last_checkpoint_targets = completed
                    self._last_checkpoint_time = time.perf_counter()
                except Exception as exc:
                    logger.warning("Checkpoint creation failed: %s", exc)

            if retcode is not None:
                break
            time.sleep(10)

        completed, total = read_ninja_progress(self.ninja_log_path)
        self.tracker.targets_completed = completed
        self.tracker.total_targets = total
        self.tracker.end_compile(total)

        if proc.returncode == 0:
            self.manifest["build_complete"] = True
            self.manifest.save(self.manifest_path)
            logger.info("Build completed successfully: %d/%d targets", completed, total)
            return "VERIFYING"

        logger.warning("Ninja exited with code %d (%d/%d targets)", proc.returncode, completed, total)
        if self._is_transient_failure(proc.returncode):
            return "RECOVERING"
        return "FAILED"

    def handle_checkpointing(self) -> str:
        logger.info("Creating final checkpoint")
        try:
            self.checkpoint_mgr.create_checkpoint()
        except Exception as exc:
            logger.warning("Final checkpoint creation failed: %s", exc)
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
        result = self.release_validator.validate_all()
        if not result.passed:
            logger.error("Release validation failed: %s", "; ".join(result.failures))
            return "FAILED"
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
        try:
            self.recovery_mgr.attempt_recovery()
        except Exception as exc:
            logger.warning("Recovery attempt failed: %s", exc)
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
        completed, total = read_ninja_progress(self.ninja_log_path)
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
            "checkpoint_number": self.checkpoint_mgr.checkpoint_sequence_number() - 1,
            "build_attempt": self.manifest.get("build_attempt", 1),
            "last_ninja_command": self._last_ninja_command,
        }

    @staticmethod
    def _is_transient_failure(returncode: int) -> bool:
        return returncode in (1, 2, 130)