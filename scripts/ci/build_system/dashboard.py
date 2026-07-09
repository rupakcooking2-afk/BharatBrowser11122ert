"""Build dashboard for fault-tolerant distributed Chromium builds.

Phase 8 — generates build_status.json used by CI dashboards and monitoring.
"""

from __future__ import annotations

import json
import os
import platform as _platform
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Optional

from .orchestrator import WorkflowOrchestrator
from .performance import PerformanceTracker

__all__ = ["BuildDashboard"]


class BuildDashboard:
    """Generates and persists the build_status.json dashboard payload.

    Parameters
    ----------
    build_dir :
        Relative build output directory (e.g. ``out/Default_x64``).
    platform :
        Platform identifier (e.g. ``sys.platform``).
    orchestrator :
        Optional orchestrator instance for live workflow state.
    tracker :
        Optional performance tracker for compile/upload/download metrics.
    """

    def __init__(
        self,
        build_dir: str,
        platform: str,
        orchestrator: Optional[WorkflowOrchestrator] = None,
        tracker: Optional[PerformanceTracker] = None,
    ) -> None:
        self.build_dir = build_dir
        self.platform = platform
        self.orchestrator = orchestrator
        self.tracker = tracker

    def generate(self) -> Dict[str, Any]:
        completed = 0
        total = 0
        elapsed = 0.0
        compile_rate = 0.0
        estimated_remaining = 0.0
        checkpoint_number = 0
        build_attempt = 1
        current_ninja_command = ""
        workflow_state = "IDLE"

        if self.orchestrator is not None:
            prog = self.orchestrator.get_progress()
            completed = prog["completed_targets"]
            total = completed + prog["remaining_targets"]
            elapsed = prog["elapsed_time_seconds"]
            compile_rate = prog["compile_rate"]
            estimated_remaining = prog["estimated_remaining_seconds"]
            checkpoint_number = prog["checkpoint_number"]
            build_attempt = prog["build_attempt"]
            current_ninja_command = prog["last_ninja_command"]
            workflow_state = prog["workflow_state"]

        cache_hit_rate = 0.0
        current_upload_speed = 0.0
        current_download_speed = 0.0

        if self.tracker is not None:
            cache_hit_rate = self.tracker.cache_hit_ratio()
            current_upload_speed = self.tracker.upload_speed()
            current_download_speed = self.tracker.download_speed()

        status: Dict[str, Any] = {
            "progress_percent": (completed / max(total, 1)) * 100.0,
            "completed_targets": completed,
            "remaining_targets": max(0, total - completed),
            "elapsed_time_seconds": round(elapsed, 2),
            "estimated_remaining_seconds": round(estimated_remaining, 1),
            "compile_rate": round(compile_rate, 2),
            "cache_hit_rate": round(cache_hit_rate, 4),
            "current_upload_speed": round(current_upload_speed, 2),
            "current_download_speed": round(current_download_speed, 2),
            "checkpoint_number": checkpoint_number,
            "build_attempt": build_attempt,
            "github_run_number": int(os.environ.get("GITHUB_RUN_NUMBER", "0")),
            "github_run_id": int(os.environ.get("GITHUB_RUN_ID", "0")),
            "platform": self.platform,
            "architecture": _platform.machine().lower(),
            "current_ninja_command": current_ninja_command,
            "workflow_state": workflow_state,
            "timestamp": datetime.now(timezone.utc).isoformat(),
        }

        return status

    def save(self, path: Path) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(
            json.dumps(self.generate(), indent=2, sort_keys=True),
            encoding="utf-8",
        )

    @classmethod
    def load(cls, path: Path) -> "BuildDashboard":
        raw = path.read_text(encoding="utf-8")
        data: Dict[str, Any] = json.loads(raw)
        inst = cls(
            build_dir=data.get("build_dir", ""),
            platform=data.get("platform", ""),
        )
        return inst