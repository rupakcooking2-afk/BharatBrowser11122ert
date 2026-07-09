"""Rolling checkpoint system using GitHub Releases for storage.

Replaces the previous R2-based checkpoint approach with a GitHub-native
strategy: ninja state is packaged as a single tarball and overwritten on
each checkpoint; object file deltas are uploaded as new assets and
accumulated across checkpoints.
"""

from __future__ import annotations

import json
import logging
import time
from pathlib import Path
from typing import Optional, Tuple

from .github_storage import GitHubReleaseStore

__all__ = [
    "CheckpointManager",
    "read_ninja_stats",
    "parse_ninja_target_count",
    "CHECKPOINT_INTERVAL_MINUTES",
    "CHECKPOINT_INTERVAL_TARGETS",
]

logger = logging.getLogger(__name__)

CHECKPOINT_INTERVAL_MINUTES = 20
CHECKPOINT_INTERVAL_TARGETS = 1000
CHECKPOINT_RETENTION = 5

# Ninja log constants
_NINJA_LOG_SEP = "\t"
_NINJA_LOG_FIELDS = 4
_NINJA_LOG_HEADER = "start"
_CHROMIUM_TARGET_FALLBACK = 57046


# ---------------------------------------------------------------------------
# Ninja helpers
# ---------------------------------------------------------------------------


def parse_ninja_target_count(build_ninja_path: Path) -> int:
    """Estimate total build targets by counting ``build`` rules in *build.ninja*."""
    if not build_ninja_path.is_file():
        return _CHROMIUM_TARGET_FALLBACK
    try:
        text = build_ninja_path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return _CHROMIUM_TARGET_FALLBACK
    count = 0
    for line in text.splitlines():
        if line.startswith("build "):
            count += 1
    return max(count, 1)


def read_ninja_stats(ninja_log_path: Path) -> Tuple[int, int, Optional[float]]:
    """Parse a ``.ninja_log`` file and return build progress.

    Returns (completed, total, start_timestamp).
    """
    if not ninja_log_path.is_file():
        return (0, 0, None)
    try:
        text = ninja_log_path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return (0, 0, None)

    lines = text.splitlines()
    if not lines:
        return (0, 0, None)

    completed = 0
    start_timestamp: Optional[float] = None
    seen: set = set()

    for line in lines:
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        parts = stripped.split(_NINJA_LOG_SEP)
        if len(parts) < _NINJA_LOG_FIELDS:
            continue
        if parts[0] == _NINJA_LOG_HEADER:
            continue
        try:
            start = int(parts[0])
            end = int(parts[1])
            output = parts[2]
        except (ValueError, IndexError):
            continue
        if end == 0 or output in seen:
            continue
        seen.add(output)
        completed += 1
        if start_timestamp is None or start < start_timestamp:
            start_timestamp = float(start)

    total = completed
    build_ninja = ninja_log_path.parent / "build.ninja"
    if build_ninja.is_file():
        total = parse_ninja_target_count(build_ninja)
    if total < completed:
        total = completed

    return (completed, total, start_timestamp)


# ---------------------------------------------------------------------------
# CheckpointManager
# ---------------------------------------------------------------------------


class CheckpointManager:
    """Rolling checkpoint manager using GitHub Releases for storage.

    Parameters
    ----------
    platform :
        Platform identifier (e.g. ``linux-x64``).
    build_dir :
        Relative build output directory (e.g. ``out/Default_x64``).
    chromium_src :
        Absolute path to the Chromium source tree.
    """

    def __init__(
        self,
        platform: str,
        build_dir: str,
        chromium_src: Path,
    ) -> None:
        self.platform = platform
        self.build_dir = build_dir
        self.chromium_src = chromium_src
        self.store = GitHubReleaseStore(platform, build_dir, chromium_src)

    @property
    def _build_path(self) -> Path:
        return self.chromium_src / self.build_dir

    # -- Checkpoint lifecycle ----------------------------------------------

    def should_checkpoint(
        self,
        elapsed_minutes: float,
        targets_completed: int,
        last_targets: int,
    ) -> bool:
        """Return ``True`` when time or targets exceed the interval thresholds."""
        if elapsed_minutes >= CHECKPOINT_INTERVAL_MINUTES:
            return True
        if (targets_completed - last_targets) >= CHECKPOINT_INTERVAL_TARGETS:
            return True
        return False

    def checkpoint_sequence_number(self) -> int:
        """Read the next sequence number from the local manifest.

        Falls back to 1 when the manifest is missing or unreadable.
        """
        build_state = self._build_path / "build_state.json"
        if build_state.is_file():
            try:
                data = json.loads(build_state.read_text(encoding="utf-8"))
                return data.get("checkpoint_counter", 0) + 1
            except (OSError, json.JSONDecodeError):
                pass
        return 1

    def create_checkpoint(self, since_wall_time: float) -> Tuple[str, bool]:
        """Create a checkpoint: upload ninja-state + output file deltas.

        Parameters
        ----------
        since_wall_time :
            Unix timestamp of the last checkpoint (or restore).  Used to
            detect newly compiled output files via mtime comparison.

        Returns
        -------
        (seq, ok)
            *seq* is the checkpoint sequence string (e.g. ``"003"``).
            *ok* is ``True`` when ninja-state was uploaded successfully.
            The caller should only advance *since_wall_time* when *ok* is
            ``True`` to avoid permanently losing files in the delta window.
        """
        seq = self.checkpoint_sequence_number()
        seq_str = f"{seq:03d}"

        ninja_ok = self.store.upload_ninja_state()
        delta_ok = self.store.upload_obj_delta(since_wall_time, seq)

        # Update local manifest counter (atomic write: .tmp → rename)
        manifest_path = self._build_path / "build_state.json"
        if manifest_path.is_file():
            try:
                data = json.loads(manifest_path.read_text(encoding="utf-8"))
                data["checkpoint_counter"] = seq
                tmp = manifest_path.with_suffix(".tmp")
                tmp.write_text(
                    json.dumps(data, indent=2, sort_keys=True), encoding="utf-8",
                )
                tmp.replace(manifest_path)
            except (OSError, json.JSONDecodeError) as exc:
                logger.error("Checkpoint counter update failed: %s", exc)

        if ninja_ok:
            logger.info(
                "Checkpoint %s (ninja=%s, delta=%s)", seq_str, ninja_ok, delta_ok,
            )
        else:
            logger.warning("Checkpoint %s ninja-state upload FAILED", seq_str)

        return seq_str, ninja_ok

    def restore_state(self) -> bool:
        """Download and extract the latest checkpoint state.

        Restores ninja-state + all accumulated object file deltas from the
        checkpoint release.  Returns ``True`` when at least ninja-state was
        restored.
        """
        if not self.store.release_exists():
            logger.warning("No checkpoint release available to restore from")
            return False

        ninja_ok = self.store.download_ninja_state()
        deltas = self.store.download_all_deltas()

        if ninja_ok:
            logger.info("State restored (ninja=%s, obj-deltas=%d)", ninja_ok, deltas)
        else:
            logger.warning("No ninja state found in checkpoint release")
        return ninja_ok

    def has_checkpoint(self) -> bool:
        """Return ``True`` when a checkpoint release exists with assets."""
        return self.store.has_assets()

    def clear_all(self) -> bool:
        """Delete the entire checkpoint release."""
        return self.store.delete_release()

    def __repr__(self) -> str:
        return (
            f"CheckpointManager(platform={self.platform!r}, "
            f"build_dir={self.build_dir!r})"
        )
