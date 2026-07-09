"""Build validation module for fault-tolerant distributed Chromium builds.

Validates build integrity on resume, detects and reports corruption without
attempting auto-repair (which is handled by *recovery.py*).
"""

from __future__ import annotations

import gzip
import hashlib
import logging
import os
import random
import shutil
import stat
import subprocess
import sys
import tarfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List, Optional, Set, Tuple

from .manifest import BuildManifest, ChecksumError, compute_gn_args_hash
from .security import ChecksumVerifier, integrity_check

__all__ = [
    "BuildValidator",
    "ValidationResult",
    "ValidationFailure",
    "validate_checkpoint_integrity",
    "validate_environment",
    "aggregate_results",
]

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Result types
# ---------------------------------------------------------------------------


@dataclass
class ValidationFailure:
    """A single validation failure."""

    component: str
    severity: str  # "error" or "warning"
    message: str
    details: Optional[dict] = None


@dataclass
class ValidationResult:
    """Aggregate outcome of one or more validation steps."""

    passed: bool = True
    failures: List[ValidationFailure] = field(default_factory=list)

    def fail(self, component: str, severity: str, message: str, details: Optional[dict] = None) -> None:
        self.passed = False
        self.failures.append(ValidationFailure(component, severity, message, details))


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def aggregate_results(results: List[ValidationResult]) -> ValidationResult:
    """Combine multiple results into one.  Any failure → overall failed."""
    combined = ValidationResult(passed=True, failures=[])
    for r in results:
        if not r.passed:
            combined.passed = False
        combined.failures.extend(r.failures)
    return combined


def _path_suffixes() -> Tuple[str, ...]:
    """Return a tuple of object-file suffixes for the current platform."""
    if sys.platform == "win32":
        return (".obj", ".o")
    return (".o",)


def _sample_files(directory: Path, suffixes: Tuple[str, ...], ratio: float) -> List[Path]:
    """Return a random 1%-sample of files matching *suffixes* under *directory*."""
    all_files: List[Path] = []
    try:
        for path in directory.rglob("*"):
            if path.is_file() and path.suffix in suffixes:
                all_files.append(path)
    except (PermissionError, OSError):
        pass
    if not all_files:
        return []
    sample_size = max(1, int(len(all_files) * ratio))
    return random.sample(all_files, sample_size)


# ---------------------------------------------------------------------------
# BuildValidator
# ---------------------------------------------------------------------------


class BuildValidator:
    """Validates every aspect of a distributed Chromium build on resume."""

    def __init__(self, chromium_src: Path, build_dir: str, platform: str) -> None:
        self.chromium_src = chromium_src.resolve()
        self.build_dir_rel = build_dir
        self.platform = platform

    @property
    def out_dir(self) -> Path:
        return self.chromium_src / self.build_dir_rel

    # -- Individual validators ----------------------------------------------

    def validate_manifest(self, manifest_path: Path) -> ValidationResult:
        result = ValidationResult(passed=True, failures=[])
        try:
            BuildManifest.load(manifest_path)
        except ChecksumError as exc:
            result.fail(
                component="manifest",
                severity="error",
                message=f"Manifest checksum validation failed: {exc}",
                details={"path": str(manifest_path)},
            )
        except FileNotFoundError:
            result.fail(
                component="manifest",
                severity="error",
                message=f"Manifest file not found: {manifest_path}",
                details={"path": str(manifest_path)},
            )
        except Exception as exc:
            result.fail(
                component="manifest",
                severity="error",
                message=f"Unexpected error loading manifest: {exc}",
                details={"path": str(manifest_path)},
            )
        return result

    def validate_gn_args(self, args_gn_path: Path, expected_hash: str) -> ValidationResult:
        result = ValidationResult(passed=True, failures=[])
        if not args_gn_path.is_file():
            result.fail(
                component="args.gn",
                severity="error",
                message=f"args.gn not found at {args_gn_path}",
                details={"path": str(args_gn_path)},
            )
            return result
        actual = compute_gn_args_hash(args_gn_path)
        if actual.lower() != expected_hash.lower():
            result.fail(
                component="args.gn",
                severity="error",
                message=f"args.gn checksum mismatch: expected={expected_hash}, actual={actual}",
                details={"path": str(args_gn_path), "expected_hash": expected_hash, "actual_hash": actual},
            )
        return result

    def validate_ninja_log(self, ninja_log_path: Path) -> ValidationResult:
        result = ValidationResult(passed=True, failures=[])
        if not ninja_log_path.is_file():
            result.fail(
                component=".ninja_log",
                severity="error",
                message=f".ninja_log not found at {ninja_log_path}",
                details={"path": str(ninja_log_path)},
            )
            return result
        try:
            raw = ninja_log_path.read_text(encoding="utf-8", errors="replace")
        except (OSError, PermissionError) as exc:
            result.fail(
                component=".ninja_log",
                severity="error",
                message=f".ninja_log unreadable: {exc}",
                details={"path": str(ninja_log_path)},
            )
            return result
        lines = [line for line in raw.splitlines() if line.strip() and not line.startswith("#")]
        if not lines:
            result.fail(
                component=".ninja_log",
                severity="error",
                message=f".ninja_log is empty or contains only comments at {ninja_log_path}",
                details={"path": str(ninja_log_path), "total_lines": len(raw.splitlines())},
            )
            return result
        header_line = lines[0].strip()
        if not header_line.startswith("ninja"):
            result.fail(
                component=".ninja_log",
                severity="warning",
                message=f".ninja_log header missing or malformed: first_line={header_line!r}",
                details={"path": str(ninja_log_path), "first_line": header_line},
            )
        return result

    def validate_ninja_deps(self, ninja_deps_path: Path) -> ValidationResult:
        result = ValidationResult(passed=True, failures=[])
        if not ninja_deps_path.is_file():
            result.fail(
                component=".ninja_deps",
                severity="error",
                message=f".ninja_deps not found at {ninja_deps_path}",
                details={"path": str(ninja_deps_path)},
            )
        else:
            try:
                size = ninja_deps_path.stat().st_size
                if size == 0:
                    result.fail(
                        component=".ninja_deps",
                        severity="warning",
                        message=f".ninja_deps is empty at {ninja_deps_path}",
                        details={"path": str(ninja_deps_path), "size_bytes": size},
                    )
            except OSError as exc:
                result.fail(
                    component=".ninja_deps",
                    severity="error",
                    message=f".ninja_deps stat failed: {exc}",
                    details={"path": str(ninja_deps_path)},
                )
        return result

    def validate_build_ninja(self, build_ninja_path: Path, expected_hash: str) -> ValidationResult:
        result = ValidationResult(passed=True, failures=[])
        if not build_ninja_path.is_file():
            result.fail(
                component="build.ninja",
                severity="error",
                message=f"build.ninja not found at {build_ninja_path}",
                details={"path": str(build_ninja_path)},
            )
            return result
        try:
            actual = hashlib.sha256(build_ninja_path.read_bytes()).hexdigest()
        except (OSError, PermissionError) as exc:
            result.fail(
                component="build.ninja",
                severity="error",
                message=f"Cannot read build.ninja: {exc}",
                details={"path": str(build_ninja_path)},
            )
            return result
        if actual.lower() != expected_hash.lower():
            result.fail(
                component="build.ninja",
                severity="error",
                message=f"build.ninja checksum mismatch: expected={expected_hash}, actual={actual}",
                details={"path": str(build_ninja_path), "expected_hash": expected_hash, "actual_hash": actual},
            )
        return result

    def validate_toolchain(self) -> ValidationResult:
        result = ValidationResult(passed=True, failures=[])
        try:
            manifest_path = self.out_dir / "build_state.json"
            try:
                manifest = BuildManifest.load(manifest_path)
            except (ChecksumError, FileNotFoundError):
                result.fail(
                    component="toolchain",
                    severity="warning",
                    message="Cannot validate toolchain: no valid manifest found",
                    details={"manifest_path": str(manifest_path)},
                )
                return result

            from .manifest import get_compiler_version, get_python_version

            current_compiler = get_compiler_version()
            stored_compiler = manifest["compiler_version"]
            if stored_compiler and current_compiler != stored_compiler:
                result.fail(
                    component="toolchain.compiler",
                    severity="error",
                    message=f"Compiler version mismatch: manifest={stored_compiler!r}, current={current_compiler!r}",
                    details={
                        "stored": stored_compiler,
                        "current": current_compiler,
                        "component": "compiler",
                    },
                )

            current_python = get_python_version()
            stored_python = manifest["python_version"]
            if stored_python and current_python != stored_python:
                result.fail(
                    component="toolchain.python",
                    severity="error",
                    message=f"Python version mismatch: manifest={stored_python!r}, current={current_python!r}",
                    details={
                        "stored": stored_python,
                        "current": current_python,
                        "component": "python",
                    },
                )
        except Exception as exc:
            result.fail(
                component="toolchain",
                severity="error",
                message=f"Toolchain validation error: {exc}",
                details={"error": str(exc)},
            )
        return result

    def validate_checkpoint(self, checkpoint_path: Path, expected_checksums: Dict[str, str]) -> ValidationResult:
        result = ValidationResult(passed=True, failures=[])
        if not checkpoint_path.is_file():
            result.fail(
                component="checkpoint",
                severity="error",
                message=f"Checkpoint tarball not found at {checkpoint_path}",
                details={"path": str(checkpoint_path)},
            )
            return result
        try:
            with tarfile.open(str(checkpoint_path), "r:gz") as tar:
                members = tar.getmembers()
                if not members:
                    result.fail(
                        component="checkpoint",
                        severity="warning",
                        message=f"Checkpoint tarball is empty at {checkpoint_path}",
                        details={"path": str(checkpoint_path), "member_count": len(members)},
                    )
                    return result
                extracted_dir = checkpoint_path.parent / f".validate_{checkpoint_path.stem}"
                extracted_dir.mkdir(parents=True, exist_ok=True)
                try:
                    tar.extractall(path=str(extracted_dir))
                    failed_checksums: List[str] = []
                    for rel_path, expected_hash in expected_checksums.items():
                        full = extracted_dir / rel_path
                        if not full.is_file():
                            result.fail(
                                component="checkpoint.file",
                                severity="error",
                                message=f"Expected checkpoint file missing: {rel_path}",
                                details={"expected_path": rel_path, "expected_hash": expected_hash},
                            )
                            continue
                        actual = ChecksumVerifier.sha256_file(full)
                        if actual.lower() != expected_hash.lower():
                            failed_checksums.append(rel_path)
                            result.fail(
                                component="checkpoint.file",
                                severity="error",
                                message=f"Checkpoint file checksum mismatch: {rel_path}",
                                details={
                                    "path": rel_path,
                                    "expected_hash": expected_hash,
                                    "actual_hash": actual,
                                },
                            )
                    if failed_checksums:
                        result.fail(
                            component="checkpoint",
                            severity="error",
                            message=f"Checkpoint integrity failure: {len(failed_checksums)} file(s) failed checksum verification",
                            details={"failed_files": failed_checksums},
                        )
                finally:
                    shutil.rmtree(str(extracted_dir), ignore_errors=True)
        except (tarfile.TarError, gzip.BadGzipFile, OSError) as exc:
            result.fail(
                component="checkpoint",
                severity="error",
                message=f"Checkpoint tarball corrupt or unreadable: {exc}",
                details={"path": str(checkpoint_path), "error": str(exc)},
            )
        return result

    def validate_object_files(self, out_dir: Path, sample_ratio: float = 0.01) -> ValidationResult:
        result = ValidationResult(passed=True, failures=[])
        if not out_dir.is_dir():
            result.fail(
                component="object_files",
                severity="error",
                message=f"Output directory does not exist: {out_dir}",
                details={"path": str(out_dir)},
            )
            return result
        suffixes = _path_suffixes()
        sampled = _sample_standard(out_dir, suffixes=suffixes, ratio=sample_ratio)
        if not sampled:
            result.fail(
                component="object_files",
                severity="warning",
                message=f"No object files found for spot-check in {out_dir}",
                details={"path": str(out_dir), "suffixes": list(suffixes)},
            )
            return result
        corrupt_count = 0
        examined = 0
        for obj_path in sampled:
            examined += 1
            reason = _is_corrupt_object_file(obj_path)
            if reason:
                corrupt_count += 1
                result.fail(
                    component="object_files",
                    severity="error",
                    message=f"Corrupt object file: {obj_path.relative_to(out_dir)} — {reason}",
                    details={
                        "path": str(obj_path),
                        "relative_to_out": str(obj_path.relative_to(out_dir)),
                        "reason": reason,
                    },
                )
        if corrupt_count == 0:
            logger.info(
                "Object file spot-check passed: %d file(s) sampled, 0 corrupt", examined
            )
        return result

    def validate_build_state(self, manifest: BuildManifest, chromium_src: Path) -> ValidationResult:
        results: List[ValidationResult] = []
        build_path = chromium_src / manifest["build_directory"]

        manifest_path = build_path / "build_state.json"
        args_gn_path = build_path / "args.gn"
        ninja_log_path = build_path / ".ninja_log"
        ninja_deps_path = build_path / ".ninja_deps"
        build_ninja_path = build_path / "build.ninja"

        results.append(self.validate_manifest(manifest_path))
        results.append(self.validate_gn_args(args_gn_path, manifest["gn_args_hash"]))
        results.append(self.validate_ninja_log(ninja_log_path))
        results.append(self.validate_ninja_deps(ninja_deps_path))
        results.append(self.validate_build_ninja(build_ninja_path, manifest["build_gn_hash"]))
        results.append(self.validate_toolchain())
        results.append(self.validate_object_files(build_path))

        return aggregate_results(results)


# ---------------------------------------------------------------------------
# Module-level helpers
# ---------------------------------------------------------------------------


def validate_checkpoint_integrity(checkpoint_tarball: Path) -> ValidationResult:
    result = ValidationResult(passed=True, failures=[])
    if not checkpoint_tarball.is_file():
        result.fail(
            component="checkpoint_tarball",
            severity="error",
            message=f"Checkpoint tarball not found: {checkpoint_tarball}",
            details={"path": str(checkpoint_tarball)},
        )
        return result

    # Validate gzip header
    try:
        with gzip.open(str(checkpoint_tarball), "rb") as gf:
            gf.read(1)
    except gzip.BadGzipFile as exc:
        result.fail(
            component="checkpoint_tarball",
            severity="error",
            message=f"Not a valid gzip file: {exc}",
            details={"path": str(checkpoint_tarball), "error": str(exc)},
        )
        return result
    except OSError as exc:
        result.fail(
            component="checkpoint_tarball",
            severity="error",
            message=f"Cannot read tarball: {exc}",
            details={"path": str(checkpoint_tarball), "error": str(exc)},
        )
        return result

    # Validate tarball structure
    try:
        with tarfile.open(str(checkpoint_tarball), "r:gz") as tf:
            members = tf.getmembers()
            if not members:
                result.fail(
                    component="checkpoint_tarball",
                    severity="warning",
                    message="Checkpoint tarball contains no files",
                    details={"path": str(checkpoint_tarball), "member_count": 0},
                )
                return result

            truncated_files: List[str] = []
            for member in members:
                if member.issparse():
                    truncated_files.append(f"{member.name} (sparse)")
                    continue
                if member.size == 0:
                    continue
                try:
                    actual = tf.extractfile(member)
                    if actual is None:
                        truncated_files.append(member.name)
                        continue
                    hasher = hashlib.sha256()
                    while True:
                        chunk = actual.read(65536)
                        if not chunk:
                            break
                        hasher.update(chunk)
                except (OSError, tarfile.TarError) as exc:
                    truncated_files.append(f"{member.name} ({exc})")

            if truncated_files:
                result.fail(
                    component="checkpoint_tarball",
                    severity="error",
                    message=f"Truncated or unreadable files in tarball: {len(truncated_files)} issue(s)",
                    details={"truncated_files": truncated_files},
                )
    except tarfile.TarError as exc:
        result.fail(
            component="checkpoint_tarball",
            severity="error",
            message=f"Cannot read tarball structure: {exc}",
            details={"path": str(checkpoint_tarball), "error": str(exc)},
        )

    return result


def validate_environment(
    manifest: BuildManifest,
    chromium_src: Path,
    browseros_dir: Path,
    repo_root: Path,
) -> ValidationResult:
    result = ValidationResult(passed=True, failures=[])

    # Disk space
    try:
        usage = shutil.disk_usage(str(chromium_src))
        free_gb = usage.free / (1024 ** 3)
        if free_gb < 10:
            result.fail(
                component="environment.disk_space",
                severity="warning",
                message=f"Low disk space: {free_gb:.1f} GB free (minimum recommended: 10 GB)",
                details={"free_gb": round(free_gb, 1), "minimum_gb": 10},
            )
    except OSError as exc:
        result.fail(
            component="environment.disk_space",
            severity="warning",
            message=f"Cannot check disk space: {exc}",
            details={"error": str(exc)},
        )

    # Verify environment mismatches via manifest
    env_mismatches = manifest.validate_environment(chromium_src, browseros_dir, repo_root)
    if env_mismatches:
        result.fail(
            component="environment.build_env",
            severity="error",
            message=f"Build environment mismatch(es): {len(env_mismatches)} difference(s)",
            details={"mismatches": env_mismatches},
        )

    # depot_tools in PATH
    path_dirs = os.environ.get("PATH", "").split(os.pathsep)
    depot_tools_found = any("depot_tools" in d for d in path_dirs)
    if not depot_tools_found:
        result.fail(
            component="environment.depot_tools",
            severity="warning",
            message="depot_tools not found in PATH",
            details={},
        )

    # aws CLI availability
    try:
        subprocess.run(
            ["aws", "--version"],
            capture_output=True,
            text=True,
            timeout=15,
        )
    except (FileNotFoundError, subprocess.SubprocessError):
        result.fail(
            component="environment.aws_cli",
            severity="warning",
            message="aws CLI not available in PATH",
            details={},
        )

    return result


# ---------------------------------------------------------------------------
# Low-level helpers (module-private)
# ---------------------------------------------------------------------------


def _sample_standard(directory: Path, suffixes: Tuple[str, ...], ratio: float) -> List[Path]:
    all_files: List[Path] = []
    try:
        for path in directory.rglob("*"):
            if path.is_file() and path.suffix in suffixes:
                all_files.append(path)
    except (PermissionError, OSError):
        pass
    if not all_files:
        return []
    sample_size = max(1, int(len(all_files) * ratio))
    return random.sample(all_files, sample_size)


def _is_corrupt_object_file(path: Path) -> Optional[str]:
    """Return a human-readable corruption reason, or *None* if the file is healthy."""
    try:
        data = path.read_bytes()
    except (OSError, PermissionError) as exc:
        return f"unreadable: {exc}"
    if not data:
        return "zero-byte file"
    if len(data) < 4:
        return f"too small ({len(data)} bytes)"
    return None