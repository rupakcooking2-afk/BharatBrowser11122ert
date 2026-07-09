"""Resumable multipart upload module for a fault-tolerant distributed Chromium build system.

Handles uploads to Cloudflare R2 with multipart support, resume capability,
checksum verification, retry with backoff, and parallel upload.
"""

from __future__ import annotations

import json
import logging
import os
import subprocess
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Dict, List, Optional, Tuple

from .retry import retry, RetryConfig, is_transient_failure
from .security import ChecksumVerifier, validate_download

__all__ = ["R2Client", "UploadManager", "ParallelUploader"]

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

MULTIPART_THRESHOLD = 100 * 1024 * 1024  # 100 MB
CHUNK_SIZE = 10 * 1024 * 1024  # 10 MB
MAX_RETRIES = 3
RETRY_DELAY = 5.0


# ---------------------------------------------------------------------------
# R2Client
# ---------------------------------------------------------------------------

class R2Client:
    """Reads R2 credentials from the environment and builds AWS CLI env dicts."""

    def __init__(self) -> None:
        self.account_id: str = os.environ["R2_ACCOUNT_ID"]
        self.access_key_id: str = os.environ["R2_ACCESS_KEY_ID"]
        self.secret_access_key: str = os.environ["R2_SECRET_ACCESS_KEY"]
        self.bucket_name: str = os.environ["R2_BUCKET"]
        self.endpoint: str = (
            f"https://{self.account_id}.r2.cloudflarestorage.com"
        )

    def _env(self) -> Dict[str, str]:
        return {
            "AWS_ACCESS_KEY_ID": self.access_key_id,
            "AWS_SECRET_ACCESS_KEY": self.secret_access_key,
            "AWS_DEFAULT_REGION": "auto",
            "AWS_ENDPOINT_URL_S3": self.endpoint,
        }

    def bucket(self) -> str:
        return self.bucket_name

    def key(self, platform: str, *parts: str) -> str:
        return f"build-resume/{platform}/{'/'.join(parts)}"


# ---------------------------------------------------------------------------
# UploadManager
# ---------------------------------------------------------------------------

class UploadManager:
    """Upload, download, and sync build artifacts to/from Cloudflare R2."""

    def __init__(self, platform: str, r2_client: Optional[R2Client] = None) -> None:
        self.platform = platform
        self.r2_client = r2_client if r2_client is not None else R2Client()

    # -- helpers -----------------------------------------------------------

    def _run_aws(self, args: List[str]) -> subprocess.CompletedProcess:
        """Run an AWS CLI command with R2 credentials and return the result."""
        env = {**os.environ, **self.r2_client._env()}
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
        return f"s3://{self.r2_client.bucket()}/{r2_key}"

    # -- single file operations -------------------------------------------

    def upload_file(self, local_path: Path, r2_key: str) -> str:
        """Upload a single file to R2 and return its SHA-256 checksum."""
        dest = self._s3_uri(r2_key)
        self._run_aws(["aws", "s3", "cp", str(local_path), dest, "--no-progress"])
        checksum = ChecksumVerifier.sha256_file(local_path)
        return checksum

    def download_file(
        self, r2_key: str, local_path: Path, expected_checksum: Optional[str] = None
    ) -> bool:
        """Download a single file from R2 with optional checksum verification."""
        src = self._s3_uri(r2_key)
        local_path.parent.mkdir(parents=True, exist_ok=True)
        self._run_aws(["aws", "s3", "cp", src, str(local_path), "--no-progress"])
        if expected_checksum:
            return validate_download(local_path, expected_checksum)
        return True

    # -- directory operations ---------------------------------------------

    def upload_directory(
        self, local_dir: Path, r2_prefix: str, pattern: str = "**/*"
    ) -> Dict[str, str]:
        """Upload all files matching *pattern* under *local_dir* to *r2_prefix*.

        Returns {relative_path: sha256_checksum} for verification.
        """
        dest = self._s3_uri(r2_prefix) + "/"
        self._run_aws(
            [
                "aws", "s3", "sync", str(local_dir), dest,
                "--no-progress", "--no-follow-symlinks",
            ]
        )
        return ChecksumVerifier.hash_directory(local_dir, pattern)

    def download_directory(self, r2_prefix: str, local_dir: Path) -> bool:
        """Download all objects under *r2_prefix* to *local_dir*."""
        src = self._s3_uri(r2_prefix) + "/"
        local_dir.mkdir(parents=True, exist_ok=True)
        self._run_aws(
            [
                "aws", "s3", "sync", src, str(local_dir),
                "--no-progress", "--no-follow-symlinks",
            ]
        )
        return True

    # -- retry wrapper ----------------------------------------------------

    def upload_with_retry(
        self, local_path: Path, r2_key: str, max_attempts: int = 3
    ) -> str:
        """Upload a file with exponential-backoff retry on transient failures."""
        config = RetryConfig(
            max_attempts=max_attempts,
            base_delay_seconds=RETRY_DELAY,
        )
        last_error: Optional[Exception] = None
        for attempt in range(1, config.max_attempts + 1):
            try:
                return self.upload_file(local_path, r2_key)
            except subprocess.CalledProcessError as exc:
                last_error = exc
                if not is_transient_failure(exc, config=config):
                    raise
                if attempt < config.max_attempts:
                    delay = min(
                        RETRY_DELAY * (3.0 ** (attempt - 1)),
                        120.0,
                    )
                    logger.warning(
                        "Upload attempt %d/%d failed, retrying in %.1fs: %s",
                        attempt, config.max_attempts, delay, exc,
                    )
                    time.sleep(delay)
        raise RuntimeError(
            f"Upload failed after {config.max_attempts} attempts: {last_error}"
        )

    # -- multipart management ---------------------------------------------

    def wait_for_multipart_completion(self, r2_key: str, upload_id: str) -> bool:
        """Wait for an in-progress multipart upload to complete."""
        bucket = self.r2_client.bucket()
        try:
            self._run_aws([
                "aws", "s3api", "complete-multipart-upload",
                "--bucket", bucket,
                "--key", r2_key,
                "--upload-id", upload_id,
            ])
            return True
        except subprocess.CalledProcessError:
            return False

    def abort_multipart(self, r2_key: str, upload_id: str) -> None:
        """Abort a failed or abandoned multipart upload."""
        bucket = self.r2_client.bucket()
        try:
            self._run_aws([
                "aws", "s3api", "abort-multipart-upload",
                "--bucket", bucket,
                "--key", r2_key,
                "--upload-id", upload_id,
            ])
        except subprocess.CalledProcessError as exc:
            logger.warning(
                "Failed to abort multipart upload %s: %s", upload_id, exc,
            )

    def list_parts(self, r2_key: str, upload_id: str) -> List[dict]:
        """List uploaded parts for a multipart upload."""
        bucket = self.r2_client.bucket()
        result = self._run_aws([
            "aws", "s3api", "list-parts",
            "--bucket", bucket,
            "--key", r2_key,
            "--upload-id", upload_id,
            "--output", "json",
        ])
        data = json.loads(result.stdout)
        return data.get("Parts", [])

    # -- sync / status ----------------------------------------------------

    def sync_incremental(
        self, local_dir: Path, r2_prefix: str, delete: bool = False
    ) -> bool:
        """Incremental sync of *local_dir* to *r2_prefix* with retry.

        Returns True when the sync completes successfully.
        """
        dest = self._s3_uri(r2_prefix) + "/"
        cmd = [
            "aws", "s3", "sync", str(local_dir), dest,
            "--no-progress", "--no-follow-symlinks",
        ]
        if delete:
            cmd.append("--delete")

        config = RetryConfig(
            max_attempts=MAX_RETRIES,
            base_delay_seconds=RETRY_DELAY,
        )
        for attempt in range(1, config.max_attempts + 1):
            try:
                self._run_aws(cmd)
                return True
            except subprocess.CalledProcessError as exc:
                if not is_transient_failure(exc, config=config):
                    raise
                if attempt < config.max_attempts:
                    delay = min(
                        RETRY_DELAY * (3.0 ** (attempt - 1)),
                        120.0,
                    )
                    logger.warning(
                        "Sync attempt %d/%d failed, retrying in %.1fs: %s",
                        attempt, config.max_attempts, delay, exc,
                    )
                    time.sleep(delay)

        logger.error(
            "Sync failed after %d attempts: %s",
            config.max_attempts, config,
        )
        return False

    def check_key_exists(self, r2_key: str) -> bool:
        """Return True when *r2_key* exists in the R2 bucket."""
        bucket = self.r2_client.bucket()
        try:
            self._run_aws([
                "aws", "s3api", "head-object",
                "--bucket", bucket,
                "--key", r2_key,
            ])
            return True
        except subprocess.CalledProcessError:
            return False


# ---------------------------------------------------------------------------
# ParallelUploader
# ---------------------------------------------------------------------------

class ParallelUploader:
    """Upload multiple files to R2 concurrently using a thread pool."""

    def __init__(self, max_workers: int = 4) -> None:
        self.max_workers = max_workers

    def upload_files(
        self,
        files: List[Tuple[Path, str]],
        manager: UploadManager,
    ) -> Dict[Path, bool]:
        """Upload all *(local_path, r2_key)* pairs in parallel.

        Returns {local_path: success}.
        """
        results: Dict[Path, bool] = {}
        with ThreadPoolExecutor(max_workers=self.max_workers) as pool:
            futures = {
                pool.submit(manager.upload_with_retry, path, r2_key): path
                for path, r2_key in files
            }
            for future in as_completed(futures):
                path = futures[future]
                try:
                    future.result()
                    results[path] = True
                except Exception as exc:
                    logger.error("Failed to upload %s: %s", path, exc)
                    results[path] = False
        return results
