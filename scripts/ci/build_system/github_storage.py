"""GitHub-native storage for resumable Chromium builds.

Replaces Cloudflare R2 with GitHub Releases + ``gh`` CLI (pre-installed on
GitHub Actions runners).  Public repositories get unlimited release storage
at no cost, with a 2 GB per-file limit.

Architecture
------------
A single rolling release ``build-checkpoint-{platform}`` holds all checkpoint
state.  Ninja state (``.ninja_log``, ``.ninja_deps``, ``args.gn``,
``build.ninja``, ``build_state.json``) is tar.gz'd and uploaded as
``ninja-state.tar.gz``, overwritten on each checkpoint.  Object file deltas
are uploaded as ``obj-delta-{seq:03d}.tar.gz`` with unique names.
"""

from __future__ import annotations

import logging
import os
import subprocess
import tarfile
import tempfile
from pathlib import Path
from typing import List, Optional

from .security import ChecksumVerifier

__all__ = ["GitHubReleaseStore"]

logger = logging.getLogger(__name__)


class GitHubReleaseStore:
    """Store and retrieve build checkpoint state via GitHub Releases.

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
        self, platform: str, build_dir: str, chromium_src: Path,
    ) -> None:
        self.platform = platform
        self.build_dir = build_dir
        self.chromium_src = Path(chromium_src)
        self.repo = os.environ.get("GITHUB_REPOSITORY", "")
        self.release_tag = f"build-checkpoint-{platform}"

    # -- gh CLI wrapper ---------------------------------------------------

    def _gh(self, args: List[str], timeout: int = 120) -> str:
        """Run ``gh`` with the given args and return stdout.

        Parameters
        ----------
        args :
            CLI arguments (without ``--repo``).
        timeout :
            Seconds before :class:`RuntimeError` is raised.

        Raises
        ------
        RuntimeError
            On timeout or authentication failure.
        subprocess.CalledProcessError
            On non-zero exit.
        """
        cmd = ["gh"] + args + ["--repo", self.repo]
        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=timeout,
                env={**os.environ, "GH_PROMPT_DISABLED": "1"},
            )
        except subprocess.TimeoutExpired:
            raise RuntimeError(
                f"gh command timed out after {timeout}s: {' '.join(cmd)}"
            )
        if result.returncode != 0:
            raise subprocess.CalledProcessError(
                result.returncode, cmd, result.stdout, result.stderr,
            )
        return result.stdout.strip()

    # -- Release lifecycle ------------------------------------------------

    def ensure_release(self) -> None:
        """Create the checkpoint release if it does not exist."""
        try:
            self._gh(["release", "view", self.release_tag], timeout=30)
        except (subprocess.CalledProcessError, RuntimeError):
            try:
                self._gh([
                    "release", "create", self.release_tag,
                    "--latest=false",
                    "--title", f"Build Checkpoint ({self.platform})",
                    "--notes", "Rolling release for resumable builds — auto-managed",
                ], timeout=30)
                logger.info("Created checkpoint release %s", self.release_tag)
            except subprocess.CalledProcessError as exc:
                if exc.returncode == 4:
                    logger.info("Release tag already exists — continuing with existing release")
                else:
                    raise

    def release_exists(self) -> bool:
        """Return ``True`` when the checkpoint release exists."""
        try:
            self._gh(["release", "view", self.release_tag], timeout=30)
            return True
        except (subprocess.CalledProcessError, RuntimeError):
            return False

    # -- Ninja state ------------------------------------------------------

    def upload_ninja_state(self) -> bool:
        """Tar+gz the ninja state files and upload as ``ninja-state.tar.gz``.

        Overwrites the previous version (``--clobber``).
        Verifies the upload by re-downloading and comparing the SHA-256 hash.
        Returns ``True`` on success.
        """
        self.ensure_release()
        build_path = self.chromium_src / self.build_dir
        with tempfile.TemporaryDirectory() as tmpdir:
            tarball = Path(tmpdir) / "ninja-state.tar.gz"
            with tarfile.open(tarball, "w:gz") as tar:
                for rel in (
                    ".ninja_log", ".ninja_deps", "args.gn",
                    "build.ninja", "build_state.json",
                ):
                    src = build_path / rel
                    if src.is_file():
                        tar.add(str(src), arcname=rel)

            local_hash = ChecksumVerifier.sha256_file(tarball)
            if not local_hash:
                logger.error("Failed to compute ninja state SHA-256")
                return False

            try:
                self._gh([
                    "release", "upload", self.release_tag,
                    str(tarball), "--clobber",
                ], timeout=180)
            except (subprocess.CalledProcessError, RuntimeError) as exc:
                logger.error("Failed to upload ninja state: %s", exc)
                return False

            # Verify: download back and compare hash.
            # Catches silent corruption from interrupted --clobber (delete
            # succeeded but upload was partial).
            try:
                verify_dir = Path(tmpdir) / "verify"
                verify_dir.mkdir(parents=True, exist_ok=True)
                self._gh([
                    "release", "download", self.release_tag,
                    "--pattern", "ninja-state.tar.gz",
                    "--dir", str(verify_dir),
                ], timeout=120)
                verify_tar = verify_dir / "ninja-state.tar.gz"
                if verify_tar.is_file():
                    remote_hash = ChecksumVerifier.sha256_file(verify_tar)
                    if remote_hash.lower() == local_hash.lower():
                        return True
                    else:
                        logger.error(
                            "Ninja state checksum mismatch: "
                            "local=%s remote=%s", local_hash, remote_hash,
                        )
                        return False
                else:
                    logger.error("Ninja state not found after upload (verify download returned no file)")
                    return False
            except (subprocess.CalledProcessError, RuntimeError) as exc:
                logger.error("Failed to verify ninja state upload: %s", exc)
                return False

    def download_ninja_state(self) -> bool:
        """Download and extract ``ninja-state.tar.gz`` to the build directory.

        Returns ``True`` when the tarball was found and extracted.
        """
        if not self.release_exists():
            return False
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            try:
                self._gh([
                    "release", "download", self.release_tag,
                    "--pattern", "ninja-state.tar.gz",
                    "--dir", str(tmp),
                ], timeout=120)
            except (subprocess.CalledProcessError, RuntimeError) as exc:
                logger.error("Failed to download ninja state: %s", exc)
                return False
            tarball = tmp / "ninja-state.tar.gz"
            if not tarball.is_file():
                return False
            build_path = self.chromium_src / self.build_dir
            build_path.mkdir(parents=True, exist_ok=True)
            try:
                with tarfile.open(tarball, "r:gz") as tar:
                    tar.extractall(path=str(build_path))
                return True
            except (tarfile.TarError, OSError) as exc:
                logger.error("Failed to extract ninja state: %s", exc)
                return False

    # -- Output file deltas -----------------------------------------------

    def find_changed_outputs(self, since_wall_time: float) -> List[Path]:
        """Return absolute paths of all output files with mtime >= *since_wall_time*.

        Scans ``obj/``, ``obj.host/``, and ``gen/`` under the build directory.
        Catches all file types: ``.o``, ``.obj``, ``.a``, ``.lib``, ``.so``,
        ``.dll``, ``.dylib``, ``.stamp``, ``.rsp``, generated headers, etc.

        Parameters
        ----------
        since_wall_time :
            Unix timestamp (seconds since epoch).  File mtimes are compared
            against this cutoff.
        """
        build_path = self.chromium_src / self.build_dir
        changed: List[Path] = []
        for subdir in ("obj", "obj.host", "gen"):
            d = build_path / subdir
            if not d.is_dir():
                continue
            for p in d.rglob("*"):
                if not p.is_file():
                    continue
                try:
                    if p.stat().st_mtime >= since_wall_time:
                        changed.append(p)
                except OSError:
                    continue
        return changed

    def upload_obj_delta(self, since_wall_time: float, seq: int) -> bool:
        """Create and upload a tar.gz of output files changed since *since_wall_time*.

        Scans ``obj/``, ``obj.host/``, and ``gen/`` for any file type.
        The tarball is named ``obj-delta-{seq:03d}.tar.gz`` (legacy name)
        and uploaded as a new asset (not clobbered — each seq is unique).

        Returns ``True`` when the upload succeeded or no files changed.
        """
        changed = self.find_changed_outputs(since_wall_time)
        if not changed:
            return True
        self.ensure_release()
        build_path = self.chromium_src / self.build_dir
        with tempfile.TemporaryDirectory() as tmpdir:
            tarball = Path(tmpdir) / f"obj-delta-{seq:03d}.tar.gz"
            with tarfile.open(tarball, "w:gz") as tar:
                for p in changed:
                    tar.add(str(p), arcname=str(p.relative_to(build_path)))
            size_mb = tarball.stat().st_size / (1024 * 1024)
            try:
                self._gh([
                    "release", "upload", self.release_tag, str(tarball),
                ], timeout=300)
            except (subprocess.CalledProcessError, RuntimeError) as exc:
                logger.error("Failed to upload obj delta %d: %s", seq, exc)
                return False

            # Verify the upload by re-downloading and comparing SHA-256.
            try:
                verify_dir = Path(tmpdir) / "verify"
                verify_dir.mkdir(parents=True, exist_ok=True)
                self._gh([
                    "release", "download", self.release_tag,
                    "--pattern", f"obj-delta-{seq:03d}.tar.gz",
                    "--dir", str(verify_dir),
                ], timeout=300)
                verify_tar = verify_dir / f"obj-delta-{seq:03d}.tar.gz"
                if verify_tar.is_file():
                    remote_hash = ChecksumVerifier.sha256_file(verify_tar)
                    local_hash = ChecksumVerifier.sha256_file(tarball)
                    if remote_hash.lower() != local_hash.lower():
                        logger.error(
                            "obj-delta-%03d checksum mismatch after upload",
                            seq,
                        )
                        return False
            except (subprocess.CalledProcessError, RuntimeError, OSError) as exc:
                logger.error("Failed to verify obj-delta-%03d: %s", seq, exc)
                return False

            logger.info(
                "Uploaded obj-delta-%03d: %d files, %.1f MB",
                seq, len(changed), size_mb,
            )
            return True

    def download_all_deltas(self) -> int:
        """Download and extract all ``obj-delta-*`` tarballs to the build directory.

        Returns the number of tarballs extracted.
        """
        if not self.release_exists():
            return 0
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            try:
                self._gh([
                    "release", "download", self.release_tag,
                    "--pattern", "obj-delta-*",
                    "--dir", str(tmp),
                ], timeout=300)
            except (subprocess.CalledProcessError, RuntimeError) as exc:
                logger.error("Failed to download obj-delta archives: %s", exc)
                return 0
            build_path = self.chromium_src / self.build_dir
            extracted = 0
            for f in sorted(tmp.iterdir()):
                if not f.name.startswith("obj-delta-") or not f.name.endswith(".tar.gz"):
                    continue
                try:
                    with tarfile.open(f, "r:gz") as tar:
                        tar.extractall(path=str(build_path))
                    extracted += 1
                except (tarfile.TarError, OSError) as exc:
                    logger.warning("Failed to extract %s: %s", f.name, exc)
            if extracted:
                logger.info("Extracted %d obj-delta tarball(s)", extracted)
            return extracted

    # -- Cleanup -----------------------------------------------------------

    def asset_names(self) -> List[str]:
        """Return names of all assets in the checkpoint release."""
        if not self.release_exists():
            return []
        try:
            out = self._gh([
                "release", "view", self.release_tag,
                "--json", "assets",
                "--jq", ".assets[].name",
            ], timeout=30)
            return out.splitlines() if out else []
        except (subprocess.CalledProcessError, RuntimeError, ValueError) as exc:
            logger.warning("Failed to list release assets: %s", exc)
            return []

    def delete_asset(self, name: str) -> bool:
        """Delete a single asset by name."""
        try:
            self._gh(["release", "delete-asset", self.release_tag, name], timeout=30)
            return True
        except (subprocess.CalledProcessError, RuntimeError) as exc:
            logger.warning("Failed to delete asset %s: %s", name, exc)
            return False

    def delete_release(self) -> bool:
        """Delete the entire checkpoint release (and its tag)."""
        if not self.release_exists():
            return True
        try:
            self._gh(["release", "delete", self.release_tag], timeout=30)
            return True
        except (subprocess.CalledProcessError, RuntimeError) as exc:
            logger.warning("Failed to delete release %s: %s", self.release_tag, exc)
            return False

    def has_assets(self) -> bool:
        """Return ``True`` when the release exists and has at least one asset."""
        if not self.release_exists():
            return False
        return len(self.asset_names()) > 0
