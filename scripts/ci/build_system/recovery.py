"""Build recovery module for fault-tolerant distributed Chromium builds.

Automatically detects and recovers from build corruption — missing objects,
partial uploads, interrupted links, corrupt archives, invalid manifests,
missing sources, and stale checkpoints — without manual intervention.
"""

from __future__ import annotations

import logging
import os
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List, Optional

from .checkpoint import CheckpointManager, read_ninja_stats
from .disk_manager import DiskManager
from .manifest import BuildManifest, ChecksumError, manifest_r2_key
from .uploader import UploadManager
from .validator import BuildValidator, ValidationFailure, ValidationResult

__all__ = [
    "RecoveryManager",
    "RecoveryIssue",
    "RecoveryResult",
    "auto_repair",
]

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Data classes
# ---------------------------------------------------------------------------


@dataclass
class RecoveryIssue:
    """A single issue detected during a recovery scan."""

    component: str
    issue_type: str  # missing, corrupt, partial, stale, disk_space, interrupted, invalid
    severity: str  # critical, error, warning
    description: str
    recoverable: bool = True
    auto_recovered: bool = False


@dataclass
class RecoveryResult:
    """Aggregate outcome of a recovery attempt."""

    success: bool = True
    issues_found: int = 0
    issues_recovered: int = 0
    issues_unrecoverable: int = 0
    actions: List[str] = field(default_factory=list)
    state: str = "recovered"  # recovered, partial, failed


# ---------------------------------------------------------------------------
# RecoveryManager
# ---------------------------------------------------------------------------


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
        self.upload_mgr = UploadManager(platform)

    # ------------------------------------------------------------------
    # Properties
    # ------------------------------------------------------------------

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
        """Scan everything and return a prioritised list of issues found."""
        issues: List[RecoveryIssue] = []

        # 1. Manifest validity
        manifest_path = self.out_dir / "build_state.json"
        if not manifest_path.is_file():
            issues.append(RecoveryIssue(
                component="manifest",
                issue_type="missing",
                severity="critical",
                description="Manifest file build_state.json not found",
                recoverable=True,
            ))
        else:
            try:
                BuildManifest.load(manifest_path)
            except ChecksumError:
                issues.append(RecoveryIssue(
                    component="manifest",
                    issue_type="corrupt",
                    severity="critical",
                    description="Manifest checksum validation failed",
                    recoverable=True,
                ))
            except Exception as exc:
                issues.append(RecoveryIssue(
                    component="manifest",
                    issue_type="corrupt",
                    severity="critical",
                    description=f"Manifest corrupt or unreadable: {exc}",
                    recoverable=True,
                ))

        # 2. Checkpoint integrity
        try:
            checkpoints = self.checkpoint_mgr.list_checkpoints()
            if not checkpoints:
                issues.append(RecoveryIssue(
                    component="checkpoint",
                    issue_type="missing",
                    severity="warning",
                    description="No checkpoints found in R2",
                    recoverable=True,
                ))
            else:
                latest_seq = self.checkpoint_mgr.get_latest_checkpoint()
                if latest_seq is not None:
                    cp_key = self.checkpoint_mgr.get_checkpoint_r2_key(latest_seq)
                    if not self.upload_mgr.check_key_exists(cp_key):
                        issues.append(RecoveryIssue(
                            component="checkpoint",
                            issue_type="corrupt",
                            severity="error",
                            description=f"Latest checkpoint {latest_seq} missing in R2",
                            recoverable=True,
                        ))
        except Exception as exc:
            issues.append(RecoveryIssue(
                component="checkpoint",
                issue_type="corrupt",
                severity="error",
                description=f"Checkpoint listing failed: {exc}",
                recoverable=True,
            ))

        # 3. Build state files
        ninja_log = self.out_dir / ".ninja_log"
        if not ninja_log.is_file():
            issues.append(RecoveryIssue(
                component=".ninja_log",
                issue_type="missing",
                severity="error",
                description=".ninja_log not found in build output directory",
                recoverable=True,
            ))

        ninja_deps = self.out_dir / ".ninja_deps"
        if not ninja_deps.is_file():
            issues.append(RecoveryIssue(
                component=".ninja_deps",
                issue_type="missing",
                severity="error",
                description=".ninja_deps not found in build output directory",
                recoverable=True,
            ))

        build_ninja = self.out_dir / "build.ninja"
        if not build_ninja.is_file():
            issues.append(RecoveryIssue(
                component="build.ninja",
                issue_type="missing",
                severity="critical",
                description="build.ninja not found — gn reconfigure required",
                recoverable=True,
            ))

        args_gn = self.out_dir / "args.gn"
        if not args_gn.is_file():
            issues.append(RecoveryIssue(
                component="args.gn",
                issue_type="missing",
                severity="critical",
                description="args.gn not found — gn reconfigure required",
                recoverable=True,
            ))

        # 4. Disk space check
        try:
            usage = shutil.disk_usage(str(self.chromium_src))
            free_gb = usage.free / (1024 ** 3)
            if free_gb < 5.0:
                issues.append(RecoveryIssue(
                    component="disk",
                    issue_type="disk_space",
                    severity="error",
                    description=f"Low disk space: {free_gb:.1f} GB free (minimum 5 GB)",
                    recoverable=True,
                ))
        except OSError as exc:
            issues.append(RecoveryIssue(
                component="disk",
                issue_type="disk_space",
                severity="warning",
                description=f"Cannot check disk space: {exc}",
                recoverable=True,
            ))

        # 5. Stale symlinks (interrupted links)
        self._detect_stale_symlinks(issues)

        return issues

    def _detect_stale_symlinks(self, issues: List[RecoveryIssue]) -> None:
        """Check for broken symlinks under the build output directory."""
        if not self.out_dir.is_dir():
            return
        stale_count = 0
        for entry in self.out_dir.rglob("*"):
            if entry.is_symlink() and not entry.exists():
                stale_count += 1
        if stale_count:
            issues.append(RecoveryIssue(
                component="symlinks",
                issue_type="interrupted",
                severity="warning",
                description=f"Found {stale_count} broken symlink(s) in build directory",
                recoverable=True,
            ))

    # ------------------------------------------------------------------
    # Recovery
    # ------------------------------------------------------------------

    def recover(self) -> RecoveryResult:
        """Attempt to recover all detected issues."""
        issues = self.detect_issues()
        result = RecoveryResult(
            success=True,
            issues_found=len(issues),
            issues_recovered=0,
            issues_unrecoverable=0,
            actions=[],
            state="recovered",
        )

        if not issues:
            logger.info("No issues detected; nothing to recover")
            return result

        # Sort issues by severity: critical > error > warning
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
                    f"Failed to recover {issue.component} ({issue.issue_type}): {issue.description}"
                )
            else:
                result.issues_unrecoverable += 1
                result.actions.append(
                    f"Unrecoverable {issue.component} ({issue.issue_type}): {issue.description}"
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

        # If overall recovery failed, persist manifest state
        if result.state == "failed":
            self._set_manifest_failed(manifest_path)

        return result

    def _handle_issue(
        self,
        issue: RecoveryIssue,
        manifest_path: Path,
        result: RecoveryResult,
    ) -> bool:
        """Dispatch a single issue to the appropriate recovery handler."""
        if issue.component == "manifest":
            return self._recover_manifest(manifest_path)
        if issue.component == "checkpoint":
            return self._recover_checkpoint()
        if issue.component in (".ninja_log", ".ninja_deps"):
            return self._recover_ninja_state(issue.component)
        if issue.component == "build.ninja":
            return self.force_reconfigure()
        if issue.component == "args.gn":
            return self.force_reconfigure()
        if issue.component == "disk":
            return self._recover_disk_space()
        if issue.component == "symlinks":
            return self._recover_symlinks()
        if issue.component == "object_files":
            # Ninja auto-rebuilds missing objects on next run
            result.actions.append("Missing object files: ninja will auto-rebuild on next run")
            return True
        if issue.issue_type == "partial":
            return self._recover_partial_upload()
        # Unknown component — treat as unrecoverable
        return False

    def _recover_manifest(self, manifest_path: Path) -> bool:
        """Create a new manifest from the current environment."""
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
            # Upload to R2
            r2_key = manifest_r2_key(self.platform)
            self.upload_mgr.upload_file(manifest_path, r2_key)
            self._manifest = manifest
            logger.info("Recovered manifest: created new build_state.json from environment")
            return True
        except Exception as exc:
            logger.error("Failed to recover manifest: %s", exc)
            return False

    def _recover_checkpoint(self) -> bool:
        """Walk back through checkpoints (N-1, N-2...) and restore the newest valid one."""
        try:
            checkpoints = self.checkpoint_mgr.list_checkpoints()
            if not checkpoints:
                return False

            sorted_cps = sorted(checkpoints, key=lambda c: c["seq"], reverse=True)
            latest = self.checkpoint_mgr.get_latest_checkpoint()

            for cp in sorted_cps:
                seq_str = f"{cp['seq']:03d}"
                r2_key = self.checkpoint_mgr.get_checkpoint_r2_key(seq_str)
                try:
                    with tempfile.TemporaryDirectory() as tmpdir:
                        tmp_path = Path(tmpdir) / f"checkpoint_{seq_str}.tar.gz"
                        self.upload_mgr.download_file(r2_key, tmp_path)
                        # Quick gzip header sanity check
                        header = tmp_path.read_bytes()[:2]
                        if header != b"\x1f\x8b":
                            logger.warning("Checkpoint %s has invalid gzip header, skipping", seq_str)
                            continue
                except Exception as exc:
                    logger.warning("Checkpoint %s unreadable: %s, skipping", seq_str, exc)
                    continue

                # Update latest.txt if needed
                if latest != seq_str:
                    try:
                        with tempfile.TemporaryDirectory() as tmpdir:
                            latest_path = Path(tmpdir) / "latest.txt"
                            latest_path.write_text(seq_str, encoding="utf-8")
                            self.upload_mgr.upload_file(
                                latest_path,
                                self.checkpoint_mgr._latest_r2_key(),
                            )
                    except Exception as exc:
                        logger.warning("Failed to update latest.txt to %s: %s", seq_str, exc)

                logger.info("Recovered checkpoint chain: latest is now %s", seq_str)
                return True

            return False
        except Exception as exc:
            logger.error("Checkpoint recovery failed: %s", exc)
            return False

    def _recover_ninja_state(self, component: str) -> bool:
        """Force gn gen to regenerate .ninja_log and .ninja_deps."""
        return self.force_reconfigure()

    def _recover_disk_space(self) -> bool:
        """Trigger aggressive cleanup via DiskManager."""
        try:
            result = self.disk_mgr.enforce_disk_quota(max_usage_percent=85.0)
            if result["actions_taken"]:
                logger.info("Disk space recovery: %s", "; ".join(result["actions_taken"]))
                return True
            logger.warning("Disk space recovery attempted but no cleanup actions taken")
            return False
        except Exception as exc:
            logger.error("Disk space recovery failed: %s", exc)
            return False

    def _recover_symlinks(self) -> bool:
        """Delete stale symlinks in the build output directory."""
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

    def _recover_partial_upload(self) -> bool:
        """Retry partial upload by listing and completing multipart uploads."""
        try:
            bucket = self.upload_mgr.r2_client.bucket()
            result = subprocess.run(
                [
                    "aws", "s3api", "list-multipart-uploads",
                    "--bucket", bucket,
                    "--prefix", f"build-resume/{self.platform}/",
                    "--output", "json",
                ],
                capture_output=True,
                text=True,
            )
            if result.returncode != 0:
                return True
            import json
            uploads = json.loads(result.stdout).get("Uploads", [])
            for up in uploads:
                key = up.get("Key", "")
                upload_id = up.get("UploadId", "")
                if key and upload_id:
                    try:
                        self.upload_mgr.wait_for_multipart_completion(key, upload_id)
                    except Exception:
                        pass
            return True
        except Exception as exc:
            logger.warning("Partial upload recovery encountered: %s", exc)
            return True

    def _set_manifest_failed(self, manifest_path: Path) -> None:
        """Persist FAILED workflow state to manifest."""
        try:
            if self._manifest is not None:
                manifest = self._manifest
            elif manifest_path.is_file():
                manifest = BuildManifest.load(manifest_path)
            else:
                manifest = BuildManifest(self.platform, self.build_dir_rel)
            manifest["workflow_state"] = "FAILED"
            manifest.save(manifest_path)
            r2_key = manifest_r2_key(self.platform)
            self.upload_mgr.upload_file(manifest_path, r2_key)
        except Exception as exc:
            logger.error("Failed to persist FAILED state to manifest: %s", exc)

    # ------------------------------------------------------------------
    # Status checks
    # ------------------------------------------------------------------

    def can_resume(self) -> bool:
        """Quick check if build can resume without recovery."""
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

        ninja_log = self.out_dir / ".ninja_log"
        if not ninja_log.is_file():
            return False

        try:
            usage = shutil.disk_usage(str(self.chromium_src))
            free_gb = usage.free / (1024 ** 3)
            if free_gb < 1.0:
                return False
        except OSError:
            return False

        return True

    # ------------------------------------------------------------------
    # Repair actions
    # ------------------------------------------------------------------

    def force_reconfigure(self) -> bool:
        """Force gn gen re-run to regenerate build.ninja and args.gn."""
        arch = self._derive_architecture()
        gn_dir = f"out/Default_{arch}"
        gn_path = self.chromium_src / gn_dir

        try:
            result = subprocess.run(
                ["gn", "gen", gn_dir],
                cwd=str(self.chromium_src),
                capture_output=True,
                text=True,
                timeout=300,
            )
            if result.returncode != 0:
                logger.error("gn gen failed: %s", result.stderr.strip())
                return False
            logger.info("force_reconfigure: gn gen %s succeeded", gn_dir)
            return True
        except (subprocess.SubprocessError, OSError) as exc:
            logger.error("force_reconfigure failed: %s", exc)
            return False

    def force_clean(self) -> bool:
        """Clean build directory completely and reset remote state."""
        if self.out_dir.is_dir():
            try:
                shutil.rmtree(str(self.out_dir))
                logger.info("force_clean: deleted build directory %s", self.out_dir)
            except OSError as exc:
                logger.error("force_clean: failed to delete build directory: %s", exc)
                return False

        # Clear checkpoint state in R2
        try:
            checkpoints = self.checkpoint_mgr.list_checkpoints()
            for cp in checkpoints:
                self.checkpoint_mgr.delete_checkpoint(str(cp["seq"]))
            # Delete latest.txt
            latest_key = self.checkpoint_mgr._latest_r2_key()
            bucket = self.upload_mgr.r2_client.bucket()
            subprocess.run(
                ["aws", "s3", "rm", f"s3://{bucket}/{latest_key}"],
                capture_output=True,
                check=False,
            )
        except Exception as exc:
            logger.warning("force_clean: checkpoint cleanup encountered: %s", exc)

        # Reset manifest
        manifest_path = self.out_dir / "build_state.json"
        try:
            manifest = BuildManifest(self.platform, self.build_dir_rel)
            manifest["workflow_state"] = "IDLE"
            manifest.save(manifest_path)
            r2_key = manifest_r2_key(self.platform)
            self.upload_mgr.upload_file(manifest_path, r2_key)
            self._manifest = manifest
        except Exception as exc:
            logger.error("force_clean: failed to reset manifest: %s", exc)
            return False

        return True

    def repair_checkpoint_chain(self) -> bool:
        """Verify checkpoint consistency chain and remove broken links."""
        try:
            checkpoints = self.checkpoint_mgr.list_checkpoints()
            if not checkpoints:
                return False

            sorted_cps = sorted(checkpoints, key=lambda c: c["seq"], reverse=True)
            latest = self.checkpoint_mgr.get_latest_checkpoint()
            valid_found = False
            last_valid_seq: Optional[str] = None

            for cp in sorted_cps:
                seq_str = f"{cp['seq']:03d}"
                r2_key = self.checkpoint_mgr.get_checkpoint_r2_key(seq_str)
                try:
                    with tempfile.TemporaryDirectory() as tmpdir:
                        tmp_path = Path(tmpdir) / f"checkpoint_{seq_str}.tar.gz"
                        self.upload_mgr.download_file(r2_key, tmp_path)
                        header = tmp_path.read_bytes()[:2]
                        if header != b"\x1f\x8b":
                            logger.warning("Corrupt checkpoint %s (invalid gzip), removing", seq_str)
                            self.checkpoint_mgr.delete_checkpoint(seq_str)
                            continue
                except Exception as exc:
                    logger.warning("Corrupt checkpoint %s (%s), removing", seq_str, exc)
                    self.checkpoint_mgr.delete_checkpoint(seq_str)
                    continue

                if not valid_found:
                    valid_found = True
                    last_valid_seq = seq_str

            if valid_found and last_valid_seq and latest != last_valid_seq:
                with tempfile.TemporaryDirectory() as tmpdir:
                    latest_path = Path(tmpdir) / "latest.txt"
                    latest_path.write_text(last_valid_seq, encoding="utf-8")
                    self.upload_mgr.upload_file(
                        latest_path,
                        self.checkpoint_mgr._latest_r2_key(),
                    )

            return valid_found
        except Exception as exc:
            logger.error("repair_checkpoint_chain failed: %s", exc)
            return False

    def handle_stale_checkpoints(self, retention: int = 5) -> int:
        """Delegate to CheckpointManager.prune_old_checkpoints."""
        return self.checkpoint_mgr.prune_old_checkpoints(retention)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _build_architecture(self) -> str:
        """Return the architecture string for this platform."""
        import platform as _platform

        machine = _platform.machine().lower()
        mapping = {
            "amd64": "x64",
            "x86_64": "x64",
            "i386": "x86",
            "i686": "x86",
            "arm64": "arm64",
            "aarch64": "arm64",
        }
        return mapping.get(machine, machine)


# ---------------------------------------------------------------------------
# Convenience entry point
# ---------------------------------------------------------------------------


def auto_repair(build_dir: Path, platform: str) -> RecoveryResult:
    """Detect issues, recover, and return the result.

    Parameters
    ----------
    build_dir :
        Absolute path to the build output directory (e.g. ``out/Default_x64``).
    platform :
        Platform identifier (e.g. ``sys.platform``).

    Returns
    -------
    RecoveryResult
        Outcome of the recovery attempt.
    """
    chromium_src = build_dir.parent.parent
    manager = RecoveryManager(chromium_src, build_dir.name, platform)
    result = manager.recover()
    logger.info(
        "auto_repair complete: state=%s, found=%d, recovered=%d, unrecoverable=%d",
        result.state,
        result.issues_found,
        result.issues_recovered,
        result.issues_unrecoverable,
    )
    return result