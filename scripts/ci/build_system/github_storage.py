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
import random
import subprocess
import tarfile
import tempfile
import time
from pathlib import Path
from typing import List, Optional

from .security import ChecksumVerifier

__all__ = [
    "GitHubReleaseStore",
    "GhCommandError",
    "classify_gh_failure",
    "CATEGORY_AUTH",
    "CATEGORY_PERMISSION",
    "CATEGORY_NOT_FOUND",
    "CATEGORY_ALREADY_EXISTS",
    "CATEGORY_TRANSIENT",
    "CATEGORY_UNKNOWN",
]

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Failure classification
# ---------------------------------------------------------------------------
# gh CLI exit codes of interest:
#   0  success
#   1  general error (API errors surface here, e.g. HTTP 403/404/5xx)
#   4  authentication failure (no token / invalid token / expired token)

CATEGORY_AUTH = "auth"
CATEGORY_PERMISSION = "permission"
CATEGORY_NOT_FOUND = "not_found"
CATEGORY_ALREADY_EXISTS = "already_exists"
CATEGORY_TRANSIENT = "transient"
CATEGORY_UNKNOWN = "unknown"

_AUTH_PATTERNS = (
    "gh auth login",
    "to get started with github cli",
    "bad credentials",
    "http 401",
    "unauthorized",
    "invalid credentials",
)
_PERMISSION_PATTERNS = (
    "resource not accessible",
    "http 403",
    "forbidden",
    "must have admin rights",
    "permission denied to",
    "not authorized to",
)
_ALREADY_EXISTS_PATTERNS = ("already_exists", "already exists")
_NOT_FOUND_PATTERNS = ("http 404", "release not found", "not found")
_TRANSIENT_PATTERNS = (
    "timeout",
    "timed out",
    "connection reset",
    "connection refused",
    "connection aborted",
    "eof",
    "dial tcp",
    "tls handshake",
    "server error",
    "service unavailable",
    "bad gateway",
    "internal server error",
    "internal error",
    "rate limit",
    "abuse detection",
    "too many requests",
    "secondary rate",
)
# HTTP status codes that indicate a *transient* GitHub-side problem.
_TRANSIENT_EXIT_CODES = {429, 500, 502, 503, 504}


def classify_gh_failure(
    exit_code: Optional[int], stderr: Optional[str],
) -> str:
    """Classify a failed ``gh`` invocation into a failure category.

    Categories:
      * ``auth``           — missing/invalid credentials (gh exit code 4).
                             Not retryable; requires GH_TOKEN to be set.
      * ``permission``     — valid token but lacks ``contents: write``.
                             Not retryable; fix workflow permissions.
      * ``already_exists`` — create raced with a concurrent run.
                             Treat as success by callers that just need
                             the release to exist.
      * ``not_found``      — release/asset does not exist (HTTP 404).
      * ``transient``      — network blips, timeouts, 5xx, rate limits.
                             Safe to retry with backoff.
      * ``unknown``        — anything else; not retried to avoid masking bugs.
    """
    s = (stderr or "").lower()
    if exit_code == 4:
        return CATEGORY_AUTH
    if any(p in s for p in _AUTH_PATTERNS):
        return CATEGORY_AUTH
    if any(p in s for p in _PERMISSION_PATTERNS):
        return CATEGORY_PERMISSION
    if any(p in s for p in _ALREADY_EXISTS_PATTERNS):
        return CATEGORY_ALREADY_EXISTS
    if any(p in s for p in _NOT_FOUND_PATTERNS):
        return CATEGORY_NOT_FOUND
    if exit_code in _TRANSIENT_EXIT_CODES:
        return CATEGORY_TRANSIENT
    if any(p in s for p in _TRANSIENT_PATTERNS):
        return CATEGORY_TRANSIENT
    return CATEGORY_UNKNOWN


class GhCommandError(Exception):
    """A classified failure of a single logical ``gh`` operation.

    Raised only after all retries for transient failures are exhausted,
    or immediately for non-transient categories.
    """

    def __init__(
        self,
        message: str,
        category: str = CATEGORY_UNKNOWN,
        exit_code: Optional[int] = None,
        stderr: str = "",
    ) -> None:
        super().__init__(message)
        self.category = category
        self.exit_code = exit_code
        self.stderr = stderr or ""

    @property
    def is_retryable(self) -> bool:
        return self.category == CATEGORY_TRANSIENT


def _stderr_snippet(stderr: Optional[str], limit: int = 300) -> str:
    """First line(s) of *stderr*, truncated for readable log diagnostics."""
    if not stderr:
        return ""
    text = " ".join(str(stderr).split())
    return text[:limit]


def _guidance(category: str) -> str:
    """Actionable hint per failure category."""
    if category == CATEGORY_AUTH:
        return (
            "GH_TOKEN/GITHUB_TOKEN not set or invalid. In GitHub Actions "
            "export it on the step: env: GH_TOKEN: ${{ github.token }}"
        )
    if category == CATEGORY_PERMISSION:
        return (
            "token lacks 'contents: write' — ensure the job has "
            "permissions: contents: write"
        )
    if category == CATEGORY_NOT_FOUND:
        return "release/asset does not exist yet"
    if category == CATEGORY_ALREADY_EXISTS:
        return "release/tag already exists"
    if category == CATEGORY_TRANSIENT:
        return "temporary GitHub/network problem"
    return "unclassified error"


def _compute_gh_retry_delay(attempt: int) -> float:
    """Exponential backoff with jitter: ~5s, ~15s, ... capped at 60s."""
    delay = min(5.0 * (3.0 ** (attempt - 1)), 60.0)
    return delay * random.uniform(0.75, 1.25)


GH_RETRY_ATTEMPTS = 3


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

    def _gh_retry(
        self, args: List[str], timeout: int = 120,
        description: Optional[str] = None,
    ) -> str:
        """Run ``gh`` with automatic retry on *transient* failures.

        Auth/permission/not-found errors fail fast (retrying cannot help);
        transient network/API errors are retried with exponential backoff.

        Raises
        ------
        GhCommandError
            With ``category`` set; raised after retries are exhausted or
            immediately for non-transient categories.
        """
        if description is None:
            description = f"gh {' '.join(args[:2])}"

        err: Optional[GhCommandError] = None
        for attempt in range(1, GH_RETRY_ATTEMPTS + 1):
            try:
                return self._gh(args, timeout=timeout)
            except subprocess.CalledProcessError as exc:
                stderr = exc.stderr
                if isinstance(stderr, bytes):
                    stderr = stderr.decode("utf-8", errors="replace")
                category = classify_gh_failure(exc.returncode, stderr)
                err = GhCommandError(
                    f"{description} failed (exit {exc.returncode})",
                    category, exc.returncode, stderr or "",
                )
            except RuntimeError as exc:
                # TimeoutExpired from _gh — transient by definition.
                err = GhCommandError(
                    f"{description} timed out after {timeout}s",
                    CATEGORY_TRANSIENT, None, str(exc),
                )

            assert err is not None
            if not err.is_retryable or attempt >= GH_RETRY_ATTEMPTS:
                raise err

            delay = _compute_gh_retry_delay(attempt)
            logger.warning(
                "%s failed (attempt %d/%d, %s): %s — retrying in %.1fs",
                description, attempt, GH_RETRY_ATTEMPTS, err.category,
                _stderr_snippet(err.stderr), delay,
            )
            time.sleep(delay)

        raise err  # pragma: no cover — loop always raises or returns

    @staticmethod
    def _log_storage_failure(what: str, exc: GhCommandError) -> None:
        """Emit a clear, category-aware diagnostic for a failed operation."""
        logger.warning(
            "%s FAILED [%s]: %s (%s) | fix hint: %s",
            what, exc.category, exc, _stderr_snippet(exc.stderr),
            _guidance(exc.category),
        )

    # -- Release lifecycle ------------------------------------------------

    def ensure_release(self) -> bool:
        """Create the checkpoint release if it does not exist.

        Reuses the existing release on every subsequent call.  Handles the
        race where two runs create it simultaneously ("already exists" is
        treated as success).  Retries transient GitHub API failures.

        Returns ``True`` when the release is known to exist.
        This method must NEVER raise.
        """
        try:
            self._gh_retry(
                ["release", "view", self.release_tag],
                timeout=30, description="check checkpoint release",
            )
            return True
        except GhCommandError as exc:
            if exc.category != CATEGORY_NOT_FOUND:
                self._log_storage_failure("ensure checkpoint release", exc)
                return False

        try:
            self._gh_retry([
                "release", "create", self.release_tag,
                "--latest=false",
                "--title", f"Build Checkpoint ({self.platform})",
                "--notes", "Rolling release for resumable builds — auto-managed",
            ], timeout=30, description="create checkpoint release")
            logger.info("Created checkpoint release %s", self.release_tag)
            return True
        except GhCommandError as exc:
            if exc.category == CATEGORY_ALREADY_EXISTS:
                logger.info(
                    "Checkpoint release %s already exists (created by a concurrent run)",
                    self.release_tag,
                )
                return True
            self._log_storage_failure("create checkpoint release", exc)
            return False

    def release_exists(self) -> bool:
        """Return ``True`` when the checkpoint release exists."""
        try:
            self._gh_retry(
                ["release", "view", self.release_tag],
                timeout=30, description="check checkpoint release",
            )
            return True
        except GhCommandError as exc:
            if exc.category != CATEGORY_NOT_FOUND:
                self._log_storage_failure("check checkpoint release", exc)
            return False

    # -- Ninja state ------------------------------------------------------

    def upload_ninja_state(self) -> bool:
        """Tar+gz the ninja state files and upload as ``ninja-state.tar.gz``.

        Overwrites the previous version (``--clobber``).
        Verifies the upload by re-downloading and comparing the SHA-256 hash.
        Returns ``True`` on success.
        """
        if not self.ensure_release():
            logger.warning(
                "Skipping ninja state upload — checkpoint release unavailable "
                "(build continues; this run will NOT be resumable)"
            )
            return False
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
                self._gh_retry([
                    "release", "upload", self.release_tag,
                    str(tarball), "--clobber",
                ], timeout=180, description="upload ninja-state.tar.gz")
            except GhCommandError as exc:
                self._log_storage_failure("ninja state upload", exc)
                return False

            # Verify: download back and compare hash.
            # Catches silent corruption from interrupted --clobber (delete
            # succeeded but upload was partial).
            try:
                verify_dir = Path(tmpdir) / "verify"
                verify_dir.mkdir(parents=True, exist_ok=True)
                self._gh_retry([
                    "release", "download", self.release_tag,
                    "--pattern", "ninja-state.tar.gz",
                    "--dir", str(verify_dir),
                ], timeout=120, description="verify-download ninja-state.tar.gz")
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
            except GhCommandError as exc:
                self._log_storage_failure(
                    "verify ninja state upload", exc,
                )
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
                self._gh_retry([
                    "release", "download", self.release_tag,
                    "--pattern", "ninja-state.tar.gz",
                    "--dir", str(tmp),
                ], timeout=120, description="download ninja-state.tar.gz")
            except GhCommandError as exc:
                self._log_storage_failure("ninja state download", exc)
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
        if not self.ensure_release():
            logger.warning(
                "Skipping obj-delta-%03d upload — checkpoint release "
                "unavailable (build continues)", seq,
            )
            return False
        build_path = self.chromium_src / self.build_dir
        with tempfile.TemporaryDirectory() as tmpdir:
            tarball = Path(tmpdir) / f"obj-delta-{seq:03d}.tar.gz"
            with tarfile.open(tarball, "w:gz") as tar:
                for p in changed:
                    tar.add(str(p), arcname=str(p.relative_to(build_path)))
            size_mb = tarball.stat().st_size / (1024 * 1024)
            try:
                # --clobber: a re-run can regenerate the same seq number, so
                # an existing asset must be replaced rather than failing.
                self._gh_retry([
                    "release", "upload", self.release_tag, str(tarball),
                    "--clobber",
                ], timeout=300, description=f"upload obj-delta-{seq:03d}.tar.gz")
            except GhCommandError as exc:
                self._log_storage_failure(f"obj-delta-{seq:03d} upload", exc)
                return False

            # Verify the upload by re-downloading and comparing SHA-256.
            try:
                verify_dir = Path(tmpdir) / "verify"
                verify_dir.mkdir(parents=True, exist_ok=True)
                self._gh_retry([
                    "release", "download", self.release_tag,
                    "--pattern", f"obj-delta-{seq:03d}.tar.gz",
                    "--dir", str(verify_dir),
                ], timeout=300, description=f"verify-download obj-delta-{seq:03d}.tar.gz")
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
            except (GhCommandError, OSError) as exc:
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
                self._gh_retry([
                    "release", "download", self.release_tag,
                    "--pattern", "obj-delta-*",
                    "--dir", str(tmp),
                ], timeout=300, description="download obj-delta archives")
            except GhCommandError as exc:
                self._log_storage_failure("obj-delta download", exc)
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
            out = self._gh_retry([
                "release", "view", self.release_tag,
                "--json", "assets",
                "--jq", ".assets[].name",
            ], timeout=30, description="list checkpoint release assets")
            return out.splitlines() if out else []
        except GhCommandError as exc:
            self._log_storage_failure("list checkpoint release assets", exc)
            return []

    def delete_asset(self, name: str) -> bool:
        """Delete a single asset by name."""
        try:
            self._gh_retry(
                ["release", "delete-asset", self.release_tag, name],
                timeout=30, description=f"delete asset {name}",
            )
            return True
        except GhCommandError as exc:
            self._log_storage_failure(f"delete asset {name}", exc)
            return False

    def delete_release(self) -> bool:
        """Delete the entire checkpoint release (and its tag)."""
        if not self.release_exists():
            return True
        try:
            self._gh_retry(
                ["release", "delete", self.release_tag],
                timeout=30, description="delete checkpoint release",
            )
            return True
        except GhCommandError as exc:
            self._log_storage_failure(
                f"delete checkpoint release {self.release_tag}", exc,
            )
            return False

    def has_assets(self) -> bool:
        """Return ``True`` when the release exists and has at least one asset."""
        if not self.release_exists():
            return False
        return len(self.asset_names()) > 0
