"""Rolling checkpoint system for a fault-tolerant distributed Chromium build system.

Creates periodic atomic snapshots of the build state and manages checkpoint
lifecycle with atomic updates, retention policies, and rollback support.
"""

from __future__ import annotations

import datetime
import gzip
import hashlib
import json
import logging
import os
import re
import subprocess
import tarfile
import tempfile
import time
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

from .manifest import BuildManifest
from .uploader import UploadManager, R2Client
from .security import ChecksumVerifier
from .retry import retry, RetryConfig

__all__ = [
    "CheckpointManager",
    "read_ninja_stats",
    "parse_ninja_target_count",
    "CHECKPOINT_INTERVAL_MINUTES",
    "CHECKPOINT_INTERVAL_TARGETS",
]

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

CHECKPOINT_INTERVAL_MINUTES = 20
CHECKPOINT_INTERVAL_TARGETS = 1000
CHECKPOINT_RETENTION = 5
CHECKPOINT_PREFIX = "checkpoint"

# Ninja log line format: start\texit\toutput\thash
NINJA_LOG_HEADER = "start"
_NINJA_LOG_SEP = "\t"
_NINJA_LOG_FIELDS = 4

# Chromium build.ninja fallback when file is unavailable
_CHROMIUM_TARGET_FALLBACK = 57046

# gzip magic bytes for integrity check
_GZIP_MAGIC = b"\x1f\x8b"


# ---------------------------------------------------------------------------
# Ninja helpers
# ---------------------------------------------------------------------------

def parse_ninja_target_count(build_ninja_path: Path) -> int:
    """Estimate total build targets by counting ``build`` rules in *build.ninja*.

    Falls back to 57046 (a typical Chromium full-build count) when the file
    is missing or unreadable.
    """
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


def read_ninja_stats(
    ninja_log_path: Path,
) -> Tuple[int, int, Optional[float]]:
    """Parse a ``.ninja_log`` file and return build progress statistics.

    Returns
    -------
    (completed, total, start_timestamp)
        *completed* — number of finished (non-zero exit) build edges with
        unique output paths.
        *total* — estimated total targets from the sibling ``build.ninja``,
        or ``completed`` if unavailable.
        *start_timestamp* — ninja internal timestamp (seconds since epoch) of
        the earliest recorded edge, or ``None``.
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
        if parts[0] == NINJA_LOG_HEADER:
            continue
        try:
            start = int(parts[0])
            end = int(parts[1])
            output = parts[2]
        except (ValueError, IndexError):
            continue
        if end == 0:
            continue
        if output in seen:
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
    """Rolling checkpoint manager for distributed Chromium builds.

    Parameters
    ----------
    platform :
        Platform identifier (e.g. ``sys.platform``).
    build_dir :
        Relative build output directory (e.g. ``out/Default_x64``).
    chromium_src :
        Absolute path to the Chromium source tree.
    uploader :
        Pre-configured :class:`UploadManager` instance. Created automatically
        when ``None``.
    r2_client :
        Pre-configured :class:`R2Client` instance. Used only when *uploader*
        is also ``None`` to auto-create an ``UploadManager``.
    """

    def __init__(
        self,
        platform: str,
        build_dir: str,
        chromium_src: Path,
        uploader: Optional[UploadManager] = None,
        r2_client: Optional[R2Client] = None,
    ) -> None:
        self.platform = platform
        self.build_dir = build_dir
        self.chromium_src = chromium_src

        if uploader is not None:
            self.uploader = uploader
        elif r2_client is not None:
            self.uploader = UploadManager(platform, r2_client=r2_client)
        else:
            self.uploader = UploadManager(platform)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    @property
    def _build_path(self) -> Path:
        return self.chromium_src / self.build_dir

    def _run_aws(self, args: List[str]) -> subprocess.CompletedProcess:
        """Execute an AWS CLI command with R2 credentials."""
        env = {**os.environ, **self.uploader.r2_client._env()}
        result = subprocess.run(
            args,
            env=env,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise subprocess.CalledProcessError(
                result.returncode,
                args,
                output=result.stdout,
                stderr=result.stderr,
            )
        return result

    def _s3_uri(self, r2_key: str) -> str:
        return f"s3://{self.uploader.r2_client.bucket()}/{r2_key}"

    def _latest_r2_key(self) -> str:
        return f"build-resume/{self.platform}/latest.txt"

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def should_checkpoint(
        self,
        elapsed_minutes: float,
        targets_completed: int,
        last_targets: int,
    ) -> bool:
        """Return ``True`` when a checkpoint should be created.

        Triggers when *elapsed_minutes* exceeds
        :data:`CHECKPOINT_INTERVAL_MINUTES` **or** when the number of
        completed targets since the last checkpoint exceeds
        :data:`CHECKPOINT_INTERVAL_TARGETS`.
        """
        if elapsed_minutes >= CHECKPOINT_INTERVAL_MINUTES:
            return True
        if (targets_completed - last_targets) >= CHECKPOINT_INTERVAL_TARGETS:
            return True
        return False

    def checkpoint_sequence_number(self) -> int:
        """Determine the next checkpoint sequence number.

        Reads ``checkpoint_counter`` from the local ``build_state.json``
        manifest.  Falls back to listing R2 when no manifest is present.
        """
        build_state = self._build_path / "build_state.json"
        if build_state.is_file():
            try:
                data = json.loads(build_state.read_text(encoding="utf-8"))
                ctr = data.get("checkpoint_counter", 0)
                return ctr + 1
            except (OSError, json.JSONDecodeError):
                pass
        # fallback: list R2 (avoided when manifest exists)
        checkpoints = self.list_checkpoints()
        return (max(cp["seq"] for cp in checkpoints) + 1) if checkpoints else 1

    def get_checkpoint_r2_key(self, seq: str) -> str:
        """Build the R2 object key for a checkpoint sequence string.

        Keys are zero-padded to three digits for consistent lexicographic
        ordering (e.g. ``checkpoint_001.tar.gz``).
        """
        seq_num = int(seq)
        return (
            f"build-resume/{self.platform}/"
            f"{CHECKPOINT_PREFIX}_{seq_num:03d}.tar.gz"
        )

    def create_checkpoint(self) -> str:
        """Create an atomic snapshot of the current build state.

        Steps
        -----
        1. Build a ``.tar.gz`` archive of the build directory (``.ninja_log``,
           ``.ninja_deps``, ``args.gn``, ``build.ninja``, ``build_state.json``,
           plus all other files under the build output directory).
        2. Compute the SHA-256 digest of the tarball.
        3. Upload the tarball to R2.
        4. Re-download and verify the checksum to confirm upload integrity.
        5. Write the sequence number to ``latest.txt`` in R2 (atomic commit).

        Returns
        -------
        str
            The checkpoint sequence number (padded, e.g. ``"001"``).
        """
        seq = self.checkpoint_sequence_number()
        seq_str = f"{seq:03d}"
        build_path = self._build_path

        with tempfile.TemporaryDirectory() as tmpdir:
            tmp_path = Path(tmpdir)
            tarball_name = f"{CHECKPOINT_PREFIX}_{seq_str}.tar.gz"
            tarball_path = tmp_path / tarball_name

            # -- build tarball ------------------------------------------
            try:
                with tarfile.open(tarball_path, "w:gz") as tar:
                    for rel in (
                        ".ninja_log",
                        ".ninja_deps",
                        "args.gn",
                        "build.ninja",
                        "build_state.json",
                    ):
                        src = build_path / rel
                        if src.is_file():
                            tar.add(str(src), arcname=rel)

                    if build_path.is_dir():
                        for entry in build_path.iterdir():
                            ename = entry.name
                            if ename in {
                                ".ninja_log",
                                ".ninja_deps",
                                "args.gn",
                                "build.ninja",
                                "build_state.json",
                            }:
                                continue
                            tar.add(str(entry), arcname=ename)
            except (OSError, tarfile.TarError) as exc:
                raise RuntimeError(
                    f"Failed to create checkpoint tarball: {exc}"
                ) from exc

            # -- compute local checksum ---------------------------------
            local_checksum = ChecksumVerifier.sha256_file(tarball_path)
            if not local_checksum:
                raise RuntimeError(
                    "Failed to compute SHA-256 of checkpoint tarball"
                )

            # -- upload to R2 -------------------------------------------
            r2_key = self.get_checkpoint_r2_key(seq_str)
            try:
                returned_checksum = self.uploader.upload_file(
                    tarball_path, r2_key
                )
            except subprocess.CalledProcessError as exc:
                raise RuntimeError(
                    f"Failed to upload checkpoint {seq_str}: {exc}"
                ) from exc

            # -- verify upload integrity --------------------------------
            if returned_checksum.lower() != local_checksum.lower():
                raise RuntimeError(
                    f"Checksum mismatch for checkpoint {seq_str}: "
                    f"local={local_checksum}, remote={returned_checksum}"
                )

            # -- atomically mark as latest ------------------------------
            latest_path = tmp_path / "latest.txt"
            latest_path.write_text(seq_str, encoding="utf-8")
            try:
                self.uploader.upload_file(latest_path, self._latest_r2_key())
            except subprocess.CalledProcessError as exc:
                logger.error(
                    "Checkpoint %s uploaded but failed to update "
                    "latest.txt: %s",
                    seq_str,
                    exc,
                )

        logger.info(
            "Created checkpoint %s (%s, %d bytes, sha256=%s)",
            seq_str,
            tarball_name,
            tarball_path.stat().st_size,
            local_checksum[:16],
        )
        return seq_str

    def list_checkpoints(self) -> List[dict]:
        """List all checkpoint objects stored in R2.

        Returns
        -------
        List[dict]
            Each dict contains ``seq`` (int), ``key`` (str),
            ``size`` (int bytes), and ``timestamp`` (str).
            Returns an empty list on failure.
        """
        prefix = (
            f"build-resume/{self.platform}/{CHECKPOINT_PREFIX}_"
        )
        bucket = self.uploader.r2_client.bucket()

        try:
            result = self._run_aws([
                "aws", "s3api", "list-objects",
                "--bucket", bucket,
                "--prefix", prefix,
                "--output", "json",
            ])
        except subprocess.CalledProcessError as exc:
            logger.warning("Failed to list checkpoints: %s", exc)
            return []

        try:
            payload = json.loads(result.stdout)
        except json.JSONDecodeError:
            return []

        contents = payload.get("Contents", [])
        checkpoints: List[Dict[str, Any]] = []

        pattern = re.compile(
            rf"{re.escape(CHECKPOINT_PREFIX)}_(\d+)\.tar\.gz$"
        )

        for obj in contents:
            key: str = obj.get("Key", "")
            m = pattern.search(key)
            if not m:
                continue
            checkpoints.append({
                "seq": int(m.group(1)),
                "key": key,
                "size": obj.get("Size", 0),
                "timestamp": obj.get("LastModified", ""),
            })

        return checkpoints

    def get_latest_checkpoint(self) -> Optional[str]:
        """Return the latest checkpoint sequence string from ``latest.txt``.

        Returns ``None`` when no ``latest.txt`` exists in R2.
        """
        latest_key = self._latest_r2_key()
        try:
            with tempfile.TemporaryDirectory() as tmpdir:
                local_path = Path(tmpdir) / "latest.txt"
                self.uploader.download_file(latest_key, local_path)
                seq = local_path.read_text(encoding="utf-8").strip()
                return seq if seq else None
        except subprocess.CalledProcessError:
            logger.debug("No latest.txt found at %s", latest_key)
            return None

    def restore_latest(self, local_dir: Path) -> bool:
        """Download and extract the latest checkpoint to *local_dir*.

        Returns ``True`` on success.
        """
        seq = self.get_latest_checkpoint()
        if seq is None:
            logger.warning("No latest checkpoint to restore")
            return False
        return self.restore_checkpoint(seq, local_dir)

    def restore_checkpoint(self, seq: str, local_dir: Path) -> bool:
        """Download and extract a specific checkpoint to *local_dir*.

        Performs a gzip-magic-byte sanity check on the downloaded tarball
        before extraction.  Returns ``True`` on success.
        """
        r2_key = self.get_checkpoint_r2_key(seq)

        try:
            with tempfile.TemporaryDirectory() as tmpdir:
                tarball_name = f"{CHECKPOINT_PREFIX}_{seq}.tar.gz"
                tarball_path = Path(tmpdir) / tarball_name

                # download from R2
                self.uploader.download_file(r2_key, tarball_path)

                # verify gzip magic header
                try:
                    with open(tarball_path, "rb") as fh:
                        magic = fh.read(2)
                except OSError as exc:
                    logger.error(
                        "Cannot read downloaded checkpoint %s: %s",
                        seq, exc,
                    )
                    return False

                if magic != _GZIP_MAGIC:
                    logger.error(
                        "Downloaded checkpoint %s has invalid gzip header "
                        "(got %r)", seq, magic,
                    )
                    return False

                # extract to target directory
                local_dir.mkdir(parents=True, exist_ok=True)
                with tarfile.open(tarball_path, "r:gz") as tar:
                    tar.extractall(path=str(local_dir))

            logger.info("Restored checkpoint %s to %s", seq, local_dir)
            return True

        except (OSError, tarfile.TarError, subprocess.CalledProcessError) as exc:
            logger.error("Failed to restore checkpoint %s: %s", seq, exc)
            return False

    def prune_old_checkpoints(
        self,
        retention: int = CHECKPOINT_RETENTION,
    ) -> int:
        """Delete checkpoints beyond the retention window.

        Keeps the *retention* most-recent checkpoints.  ``latest.txt`` and
        the latest valid checkpoint are never deleted.

        Returns the number of checkpoints deleted.
        """
        checkpoints = self.list_checkpoints()
        if len(checkpoints) <= retention:
            return 0

        sorted_cps = sorted(checkpoints, key=lambda x: x["seq"], reverse=True)
        to_delete = sorted_cps[retention:]

        deleted = 0
        for cp in to_delete:
            try:
                self.delete_checkpoint(str(cp["seq"]))
                deleted += 1
            except subprocess.CalledProcessError as exc:
                logger.warning(
                    "Failed to delete checkpoint %d: %s", cp["seq"], exc,
                )

        if deleted:
            logger.info(
                "Pruned %d old checkpoint(s), kept %d",
                deleted, retention,
            )

        return deleted

    def delete_checkpoint(self, seq: str) -> None:
        """Delete a single checkpoint tarball from R2."""
        r2_key = self.get_checkpoint_r2_key(seq)
        uri = self._s3_uri(r2_key)
        self._run_aws(["aws", "s3", "rm", uri])
        logger.debug("Deleted checkpoint %s (%s)", seq, r2_key)

    def __repr__(self) -> str:
        return (
            f"CheckpointManager(platform={self.platform!r}, "
            f"build_dir={self.build_dir!r})"
        )
