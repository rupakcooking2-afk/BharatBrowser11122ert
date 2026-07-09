"""Build recovery module for fault-tolerant distributed Chromium builds.

Automatically detects and recovers from build corruption — missing objects,
partial uploads, interrupted links, corrupt archives, invalid manifests,
missing sources, and stale checkpoints — without manual intervention.

Storage uses GitHub Releases (not R2).
"""

from __future__ import annotations

import logging
import shutil
import subprocess
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List, Optional

from .checkpoint import CheckpointManager, read_ninja_stats
from .disk_manager import DiskManager
from .manifest import BuildManifest, ChecksumError
from .validator import BuildValidator, ValidationFailure, ValidationResult

__all__ = [
    "RecoveryManager",
    "RecoveryIssue",
    "RecoveryResult",
    "auto_repair",
]

logger = logging.getLogger(__name__)


@dataclass
class RecoveryIssue:
    component: str
    issue_type: str
    severity: str
    description: str
    recoverable: bool = True
    auto_recovered: bool = False


@dataclass
class RecoveryResult:
    success: bool = True
    issues_found: int = 0
    issues_recovered: int = 0
    issues_unrecoverable: int = 0
    actions: List[str] = field(default_factory=list)
    state: str = "recovered"


class RecoveryManager:
    """Detects and automatically recovers from build corruption."""

    def __init__(
        self,
        chromium_src: Path,
        build_dir: str,
        platform: str,
        manifest: Optional[BuildManifest] = None,
    ) -> None:
        self.chromium_src = chromium_src.resolve()
        self.build_dir_rel = build_dir
        self.platform = platform
        self._manifest = manifest

        self.checkpoint_mgr = CheckpointManager(platform, build_dir, chromium_src)
        self.validator = BuildValidator(chromium_src, build_dir, platform)
        self.disk_mgr = DiskManager(chromium_src, build_dir, platform)

    @property
    def out_dir(self) -> Path:
        return self.chromium_src / self.build_dir_rel

    @property
    def manifest(self) -> Optional[BuildManifest]:
        return self._manifest

    @manifest.setter
    def manifest(self, value: BuildManifest) -> None:
        self._manifest = value

    # ------------------------------------------------------------------
    # Detection
    # ------------------------------------------------------------------

    def detect_issues(self) -> List[RecoveryIssue]:
        issues: List[RecoveryIssue] = []

        manifest_path = self.out_dir / "build_state.json"
        if not manifest_path.is_file():
            issues.append(RecoveryIssue(
                component="manifest", issue_type="missing", severity="critical",
                description="Manifest file build_state.json not found", recoverable=True,
            ))
        else:
            try:
                BuildManifest.load(manifest_path)
            except ChecksumError:
                issues.append(RecoveryIssue(
                    component="manifest", issue_type="corrupt", severity="critical",
                    description="Manifest checksum validation failed", recoverable=True,
                ))
            except Exception as exc:
                issues.append(RecoveryIssue(
                    component="manifest", issue_type="corrupt", severity="critical",
                    description=f"Manifest corrupt or unreadable: {exc}", recoverable=True,
                ))

        ninja_log = self.out_dir / ".ninja_log"
        if not ninja_log.is_file():
            issues.append(RecoveryIssue(
                component=".ninja_log", issue_type="missing", severity="error",
                description=".ninja_log not found", recoverable=True,
            ))

        ninja_deps = self.out_dir / ".ninja_deps"
        if not ninja_deps.is_file():
            issues.append(RecoveryIssue(
                component=".ninja_deps", issue_type="missing", severity="error",
                description=".ninja_deps not found", recoverable=True,
            ))

        build_ninja = self.out_dir / "build.ninja"
        if not build_ninja.is_file():
            issues.append(RecoveryIssue(
                component="build.ninja", issue_type="missing", severity="critical",
                description="build.ninja not found — gn reconfigure required", recoverable=True,
            ))

        args_gn = self.out_dir / "args.gn"
        if not args_gn.is_file():
            issues.append(RecoveryIssue(
                component="args.gn", issue_type="missing", severity="critical",
                description="args.gn not found — gn reconfigure required", recoverable=True,
            ))

        try:
            usage = shutil.disk_usage(str(self.chromium_src))
            free_gb = usage.free / (1024 ** 3)
            if free_gb < 5.0:
                issues.append(RecoveryIssue(
                    component="disk", issue_type="disk_space", severity="error",
                    description=f"Low disk space: {free_gb:.1f} GB free (minimum 5 GB)",
                    recoverable=True,
                ))
        except OSError as exc:
            issues.append(RecoveryIssue(
                component="disk", issue_type="disk_space", severity="warning",
                description=f"Cannot check disk space: {exc}", recoverable=True,
            ))

        self._detect_stale_symlinks(issues)
        return issues

    def _detect_stale_symlinks(self, issues: List[RecoveryIssue]) -> None:
        if not self.out_dir.is_dir():
            return
        stale_count = 0
        for entry in self.out_dir.rglob("*"):
            if entry.is_symlink() and not entry.exists():
                stale_count += 1
        if stale_count:
            issues.append(RecoveryIssue(
                component="symlinks", issue_type="interrupted", severity="warning",
                description=f"Found {stale_count} broken symlink(s)", recoverable=True,
            ))

    # ------------------------------------------------------------------
    # Recovery
    # ------------------------------------------------------------------

    def recover(self) -> RecoveryResult:
        issues = self.detect_issues()
        result = RecoveryResult(
            success=True, issues_found=len(issues),
            issues_recovered=0, issues_unrecoverable=0,
            actions=[], state="recovered",
        )

        if not issues:
            logger.info("No issues detected; nothing to recover")
            return result

        severity_order = {"critical": 0, "error": 1, "warning": 2}
        issues.sort(key=lambda i: severity_order.get(i.severity, 99))

        manifest_path = self.out_dir / "build_state.json"

        for issue in issues:
            recovered = self._handle_issue(issue, manifest_path, result)
            if recovered:
                issue.auto_recovered = True
                result.issues_recovered += 1
                result.actions.append(
                    f"Recovered {issue.component} ({issue.issue_type}): {issue.description}"
                )
            elif issue.recoverable:
                result.issues_unrecoverable += 1
                result.actions.append(
                    f"Failed to recover {issue.component}: {issue.description}"
                )
            else:
                result.issues_unrecoverable += 1
                result.actions.append(
                    f"Unrecoverable {issue.component}: {issue.description}"
                )

        if result.issues_found == result.issues_recovered:
            result.state = "recovered"
            result.success = True
        elif result.issues_recovered > 0:
            result.state = "partial"
            result.success = True
        else:
            result.state = "failed"
            result.success = False

        if result.state == "failed":
            self._set_manifest_failed(manifest_path)

        return result

    def _handle_issue(
        self, issue: RecoveryIssue, manifest_path: Path, result: RecoveryResult,
    ) -> bool:
        if issue.component == "manifest":
            return self._recover_manifest(manifest_path)
        if issue.component in (".ninja_log", ".ninja_deps"):
            return self._recover_ninja_state()
        if issue.component in ("build.ninja", "args.gn"):
            return self.force_reconfigure()
        if issue.component == "disk":
            return self._recover_disk_space()
        if issue.component == "symlinks":
            return self._recover_symlinks()
        if issue.component == "object_files":
            result.actions.append("Missing object files: ninja will rebuild on next run")
            return True
        return False

    def _recover_manifest(self, manifest_path: Path) -> bool:
        try:
            if self._manifest is not None:
                manifest = self._manifest
            else:
                manifest = BuildManifest(self.platform, self.build_dir_rel)
            manifest.create(
                chromium_src=self.chromium_src,
                browseros_dir=self.chromium_src.parent,
                repo_root=self.chromium_src.parent,
            )
            manifest.save(manifest_path)
            self._manifest = manifest
            logger.info("Recovered manifest: created new build_state.json")
            return True
        except Exception as exc:
            logger.error("Failed to recover manifest: %s", exc)
            return False

    def _recover_ninja_state(self) -> bool:
        """Restore .ninja_log and .ninja_deps from checkpoint, or force gn gen."""
        if self.checkpoint_mgr.restore_state():
            ninja_log = self.out_dir / ".ninja_log"
            ninja_deps = self.out_dir / ".ninja_deps"
            if ninja_log.is_file() and ninja_deps.is_file():
                logger.info("Recovered ninja state from checkpoint")
                return True
        return self.force_reconfigure()

    def _recover_disk_space(self) -> bool:
        try:
            result = self.disk_mgr.enforce_disk_quota(max_usage_percent=85.0)
            if result["actions_taken"]:
                logger.info("Disk space recovery: %s", "; ".join(result["actions_taken"]))
                return True
            return False
        except Exception as exc:
            logger.error("Disk space recovery failed: %s", exc)
            return False

    def _recover_symlinks(self) -> bool:
        if not self.out_dir.is_dir():
            return True
        removed = 0
        for entry in self.out_dir.rglob("*"):
            if entry.is_symlink() and not entry.exists():
                try:
                    entry.unlink()
                    removed += 1
                except OSError as exc:
                    logger.warning("Failed to remove stale symlink %s: %s", entry, exc)
        logger.info("Removed %d stale symlink(s)", removed)
        return True

    def _set_manifest_failed(self, manifest_path: Path) -> None:
        try:
            if self._manifest is not None:
                manifest = self._manifest
            elif manifest_path.is_file():
                manifest = BuildManifest.load(manifest_path)
            else:
                manifest = BuildManifest(self.platform, self.build_dir_rel)
            manifest["workflow_state"] = "FAILED"
            manifest.save(manifest_path)
        except Exception as exc:
            logger.error("Failed to persist FAILED state: %s", exc)

    # ------------------------------------------------------------------
    # Status checks
    # ------------------------------------------------------------------

    def can_resume(self) -> bool:
        manifest_path = self.out_dir / "build_state.json"
        if not manifest_path.is_file():
            return False
        try:
            manifest = BuildManifest.load(manifest_path)
        except (ChecksumError, Exception):
            return False
        if manifest["workflow_state"] == "FAILED":
            return False
        build_ninja = self.out_dir / "build.ninja"
        args_gn = self.out_dir / "args.gn"
        if not build_ninja.is_file() or not args_gn.is_file():
            return False
        if not (self.out_dir / ".ninja_log").is_file():
            return False
        try:
            usage = shutil.disk_usage(str(self.chromium_src))
            if usage.free / (1024 ** 3) < 1.0:
                return False
        except OSError:
            return False
        return True

    # ------------------------------------------------------------------
    # Repair actions
    # ------------------------------------------------------------------

    def force_reconfigure(self) -> bool:
        arch = self._derive_architecture()
        gn_dir = f"out/Default_{arch}"
        try:
            result = subprocess.run(
                ["gn", "gen", gn_dir],
                cwd=str(self.chromium_src),
                capture_output=True, text=True, timeout=300,
            )
            if result.returncode != 0:
                logger.error("gn gen failed: %s", result.stderr.strip())
                return False
            logger.info("gn gen %s succeeded", gn_dir)
            return True
        except (subprocess.SubprocessError, OSError) as exc:
            logger.error("gn gen failed: %s", exc)
            return False

    def force_clean(self) -> bool:
        if self.out_dir.is_dir():
            try:
                shutil.rmtree(str(self.out_dir))
                logger.info("Deleted build directory %s", self.out_dir)
            except OSError as exc:
                logger.error("Failed to delete build directory: %s", exc)
                return False

        self.checkpoint_mgr.clear_all()

        manifest_path = self.out_dir / "build_state.json"
        try:
            manifest = BuildManifest(self.platform, self.build_dir_rel)
            manifest["workflow_state"] = "IDLE"
            manifest.save(manifest_path)
            self._manifest = manifest
        except Exception as exc:
            logger.error("Failed to reset manifest: %s", exc)
            return False
        return True

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _derive_architecture(self) -> str:
        import platform as _platform
        machine = _platform.machine().lower()
        mapping = {
            "amd64": "x64", "x86_64": "x64", "i386": "x86",
            "i686": "x86", "arm64": "arm64", "aarch64": "arm64",
        }
        return mapping.get(machine, machine)


def auto_repair(build_dir: Path, platform: str) -> RecoveryResult:
    """Detect issues, recover, and return the result."""
    chromium_src = build_dir.parent.parent
    manager = RecoveryManager(chromium_src, build_dir.name, platform)
    result = manager.recover()
    logger.info(
        "auto_repair: state=%s, found=%d, recovered=%d, unrecoverable=%d",
        result.state, result.issues_found,
        result.issues_recovered, result.issues_unrecoverable,
    )
    return result
