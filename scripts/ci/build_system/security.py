"""Cryptographic integrity verification for build artifacts, checkpoints, downloads, and uploads."""

from __future__ import annotations

import hashlib
import hmac
import json
import logging
import os
from pathlib import Path
from typing import Dict, List, Optional

__all__ = [
    "ChecksumVerifier",
    "SignatureVerifier",
    "integrity_check",
    "validate_download",
]

logger = logging.getLogger(__name__)

_CHUNK_SIZE = 65536  # 64 KB


class ChecksumVerifier:
    """SHA-256 checksum verification for files, byte streams, and directories."""

    @staticmethod
    def sha256_file(path: Path) -> str:
        """Compute SHA-256 hex digest of a file using chunked reading."""
        hasher = hashlib.sha256()
        try:
            with open(path, "rb") as f:
                while True:
                    chunk = f.read(_CHUNK_SIZE)
                    if not chunk:
                        break
                    hasher.update(chunk)
        except (FileNotFoundError, PermissionError, OSError):
            logger.exception("Failed to read file for checksum: %s", path)
            return ""
        return hasher.hexdigest()

    @staticmethod
    def sha256_bytes(data: bytes) -> str:
        """Compute SHA-256 hex digest of a byte string."""
        return hashlib.sha256(data).hexdigest()

    @staticmethod
    def sha256_stream(stream) -> str:
        """Compute SHA-256 hex digest of a readable binary stream."""
        hasher = hashlib.sha256()
        while True:
            chunk = stream.read(_CHUNK_SIZE)
            if not chunk:
                break
            hasher.update(chunk)
        return hasher.hexdigest()

    @staticmethod
    def verify_file(path: Path, expected_hash: str) -> bool:
        """Verify a file's SHA-256 matches the expected hash."""
        actual = ChecksumVerifier.sha256_file(path)
        if not actual:
            return False
        return actual.lower() == expected_hash.lower()

    @staticmethod
    def verify_directory(
        directory: Path, manifest: Dict[str, str]
    ) -> Dict[str, bool]:
        """Verify files in *directory* against a manifest dict of {relative_path: expected_hash}.

        Returns {relative_path: passed}.
        """
        results: Dict[str, bool] = {}
        for rel_path, expected_hash in manifest.items():
            full_path = directory / rel_path
            results[rel_path] = ChecksumVerifier.verify_file(full_path, expected_hash)
        return results

    @staticmethod
    def hash_directory(directory: Path, pattern: str = "*") -> Dict[str, str]:
        """Recursively compute SHA-256 of all files matching *pattern*.

        Returns {relative_path: hex_digest}. Non-existent or unreadable files
        yield an empty-string hash.
        """
        results: Dict[str, str] = {}
        if not directory.is_dir():
            logger.warning("Not a directory: %s", directory)
            return results

        for path in directory.rglob(pattern):
            if path.is_file():
                rel = path.relative_to(directory)
                results[str(rel)] = ChecksumVerifier.sha256_file(path)
        return results


class SignatureVerifier:
    """HMAC-SHA256 signing and verification for data, files, and manifests."""

    _DEFAULT_KEY = "ci-build-system-default-signing-key"

    @staticmethod
    def _resolve_key(key: Optional[str] = None) -> str:
        if key is not None:
            return key
        env_key = os.environ.get("BUILD_SIGNING_KEY")
        if env_key:
            return env_key
        return SignatureVerifier._DEFAULT_KEY

    @staticmethod
    def sign_data(data: str, key: Optional[str] = None) -> str:
        """Return HMAC-SHA256 hex digest of *data*."""
        resolved = SignatureVerifier._resolve_key(key)
        return hmac.new(
            resolved.encode("utf-8"), data.encode("utf-8"), hashlib.sha256
        ).hexdigest()

    @staticmethod
    def verify_signature(
        data: str, signature: str, key: Optional[str] = None
    ) -> bool:
        """Verify HMAC-SHA256 *signature* of *data*."""
        expected = SignatureVerifier.sign_data(data, key)
        return hmac.compare_digest(expected, signature)

    @staticmethod
    def sign_file(path: Path, key: Optional[str] = None) -> str:
        """Compute the SHA-256 of *path* and return its HMAC-SHA256 signature."""
        digest = ChecksumVerifier.sha256_file(path)
        return SignatureVerifier.sign_data(digest, key)

    @staticmethod
    def sign_manifest(manifest: dict, key: Optional[str] = None) -> str:
        """Return an HMAC-SHA256 signature of a manifest dict (keys sorted)."""
        normalized = json.dumps(manifest, separators=(",", ":"), sort_keys=True)
        return SignatureVerifier.sign_data(normalized, key)


def integrity_check(artifact_dir: Path, checksums: Dict[str, str]) -> List[str]:
    """Verify every file listed in *checksums* exists under *artifact_dir*.

    Returns a list of relative paths that failed verification (empty = all pass).
    Files not present in *checksums* are ignored.
    """
    failures: List[str] = []
    for rel_path, expected in checksums.items():
        full = artifact_dir / rel_path
        if not ChecksumVerifier.verify_file(full, expected):
            failures.append(rel_path)
    return failures


def validate_download(local_path: Path, expected_checksum: str) -> bool:
    """Verify a downloaded file's integrity before use.

    Logs a warning on mismatch.  Returns True when the file matches.
    """
    ok = ChecksumVerifier.verify_file(local_path, expected_checksum)
    if not ok:
        logger.warning(
            "Checksum mismatch for downloaded file: %s", local_path
        )
    return ok
