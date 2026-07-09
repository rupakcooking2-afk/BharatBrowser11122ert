"""Build manifest for fault-tolerant distributed Chromium builds.

Stores all build state as JSON and is used to validate whether a build
can be resumed or must start fresh.
"""

import hashlib
import json
import platform as _platform
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional

__all__ = [
    "BuildManifest",
    "ChecksumError",
    "compute_gn_args_hash",
    "compute_build_ninja_hash",
    "compute_patch_hash",
    "get_compiler_version",
    "get_python_version",
    "manifest_r2_key",
]

WORKFLOW_STATES = frozenset({
    "IDLE",
    "PREPARING",
    "DOWNLOADING",
    "PATCHING",
    "CONFIGURING",
    "COMPILING",
    "CHECKPOINTING",
    "VERIFYING",
    "PACKAGING",
    "RELEASING",
    "COMPLETE",
    "FAILED",
    "RECOVERING",
})

R2_KEY_TEMPLATE = "build-resume/{platform}/build_state.json"

_FIELD_DEFAULTS: Dict[str, Any] = {
    "chromium_version": "",
    "browseros_commit": "",
    "bharat_browser_commit": "",
    "repository_commit_sha": "",
    "gn_args_hash": "",
    "build_gn_hash": "",
    "patch_hash": "",
    "python_version": "",
    "compiler_version": "",
    "platform": "",
    "architecture": "",
    "toolchain_version": "",
    "build_directory": "",
    "completed_targets": 0,
    "estimated_total_targets": 0,
    "last_successful_upload": "",
    "last_successful_checkpoint": "",
    "build_complete": False,
    "packaging_complete": False,
    "release_complete": False,
    "checksum": "",
    "timestamp": "",
    "workflow_state": "IDLE",
}


class ChecksumError(ValueError):
    """Raised when manifest checksum verification fails."""
    pass


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------

def _serialize_for_checksum(data: Dict[str, Any]) -> str:
    """Deterministic JSON serialisation of all fields *except* ``checksum``."""
    payload = {k: v for k, v in data.items() if k != "checksum"}
    return json.dumps(payload, sort_keys=True, ensure_ascii=False, default=str)


def _compute_checksum(data: Dict[str, Any]) -> str:
    """SHA-256 hex digest of the deterministic payload."""
    return hashlib.sha256(
        _serialize_for_checksum(data).encode("utf-8")
    ).hexdigest()


def _git_hash(path: Path) -> str:
    """Return the full SHA of HEAD at *path*, or empty string on failure."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=str(path),
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except (subprocess.SubprocessError, OSError):
        pass
    return ""


def _read_chromium_version(chromium_src: Path) -> str:
    """Read *chromium_src*/chrome/VERSION → MAJOR.MINOR.BUILD.PATCH."""
    version_file = chromium_src / "chrome" / "VERSION"
    if not version_file.is_file():
        return "0.0.0.0"
    components: Dict[str, str] = {}
    try:
        for line in version_file.read_text().splitlines():
            line = line.strip()
            if "=" in line:
                key, val = line.split("=", 1)
                components[key.strip()] = val.strip()
        return "{MAJOR}.{MINOR}.{BUILD}.{PATCH}".format(**components)
    except (KeyError, OSError):
        return "0.0.0.0"


def _get_architecture() -> str:
    """Normalise platform.machine() to a short arch string."""
    machine = _platform.machine().lower()
    mapping = {
        "amd64": "x64",
        "x86_64": "x64",
        "i386": "x86",
        "i686": "x86",
        "arm64": "arm64",
        "aarch64": "arm64",
        "armv7l": "arm",
    }
    return mapping.get(machine, machine)


# ---------------------------------------------------------------------------
# Exported helpers
# ---------------------------------------------------------------------------

def compute_gn_args_hash(args_gn_path: Path) -> str:
    """SHA-256 hex digest of the *args.gn* file contents."""
    if not args_gn_path.is_file():
        return ""
    return hashlib.sha256(args_gn_path.read_bytes()).hexdigest()


def compute_build_ninja_hash(build_ninja_path: Path) -> str:
    """SHA-256 hex digest of the *build.ninja* file contents."""
    if not build_ninja_path.is_file():
        return ""
    return hashlib.sha256(build_ninja_path.read_bytes()).hexdigest()


def compute_patch_hash(patch_dir: Path) -> str:
    """SHA-256 hex digest of all ``.patch`` / ``.diff`` files, sorted by name."""
    if not patch_dir.is_dir():
        return ""
    hasher = hashlib.sha256()
    for entry in sorted(patch_dir.iterdir()):
        if entry.is_file() and entry.suffix in {".patch", ".diff"}:
            hasher.update(entry.read_bytes())
    return hasher.hexdigest()


def get_compiler_version() -> str:
    """Detect the C/C++ compiler version string.

    Tries ``cc``, ``gcc``, ``clang`` on non-Windows; ``cl.exe`` first on
    Windows.  Returns the first line of ``--version`` output, or
    ``"unknown"``.
    """
    candidates: List[tuple]
    if sys.platform == "win32":
        candidates = [
            ("cl.exe", ""),
            ("cc", "--version"),
            ("gcc", "--version"),
            ("clang", "--version"),
        ]
    else:
        candidates = [
            ("cc", "--version"),
            ("gcc", "--version"),
            ("clang", "--version"),
        ]
    for compiler, flag in candidates:
        try:
            args = [compiler] if flag == "" else [compiler, flag]
            result = subprocess.run(
                args, capture_output=True, text=True, timeout=15,
            )
            if result.returncode == 0:
                return result.stdout.splitlines()[0].strip()
        except (subprocess.SubprocessError, OSError):
            continue
    return "unknown"


def get_python_version() -> str:
    """Return ``sys.version``."""
    return sys.version.strip()


def manifest_r2_key(platform: str) -> str:
    """Return the S3 / R2 object key for the manifest on *platform*."""
    return R2_KEY_TEMPLATE.format(platform=platform)


# ---------------------------------------------------------------------------
# BuildManifest
# ---------------------------------------------------------------------------

class BuildManifest:
    """Stores and validates distributed-build state for resume decisions.

    Parameters
    ----------
    platform :
        Value for the ``platform`` field (e.g. ``sys.platform``).
    build_dir :
        Relative build output directory (e.g. ``out/Default_x64``).
    """

    def __init__(self, platform: str, build_dir: str) -> None:
        self._data: Dict[str, Any] = dict(_FIELD_DEFAULTS)
        self._data["platform"] = platform
        self._data["build_directory"] = build_dir
        self._data["timestamp"] = datetime.now(timezone.utc).isoformat()

    # -- Populate ----------------------------------------------------------

    def create(
        self,
        chromium_src: Path,
        browseros_dir: Path,
        repo_root: Path,
    ) -> None:
        """Populate every field from the current environment.

        Reads version files, runs git, probes the compiler, and computes
        content hashes for ``args.gn`` / ``build.ninja`` (resolved under
        *chromium_src* / ``build_directory``).
        """
        compiler_ver = get_compiler_version()
        build_path = chromium_src / self._data["build_directory"]

        self._data.update({
            "chromium_version": _read_chromium_version(chromium_src),
            "browseros_commit": _git_hash(browseros_dir),
            "bharat_browser_commit": _git_hash(repo_root),
            "repository_commit_sha": _git_hash(repo_root),
            "gn_args_hash": compute_gn_args_hash(build_path / "args.gn"),
            "build_gn_hash": compute_build_ninja_hash(build_path / "build.ninja"),
            "python_version": get_python_version(),
            "compiler_version": compiler_ver,
            "architecture": _get_architecture(),
            "toolchain_version": compiler_ver,
            "timestamp": datetime.now(timezone.utc).isoformat(),
        })

    # -- Serialisation -----------------------------------------------------

    def save(self, path: Path) -> None:
        """Write the manifest as JSON, computing and embedding the checksum."""
        self._data["checksum"] = _compute_checksum(self._data)
        self._data["timestamp"] = datetime.now(timezone.utc).isoformat()
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(
            json.dumps(self._data, indent=2, sort_keys=True, ensure_ascii=False),
            encoding="utf-8",
        )

    @classmethod
    def load(cls, path: Path) -> "BuildManifest":
        """Deserialise from JSON, verify checksum, return instance.

        Raises
        ------
        ChecksumError
            If the file is missing, malformed, or its checksum does not
            match the recomputed value.
        """
        try:
            raw = path.read_text(encoding="utf-8")
            data: Dict[str, Any] = json.loads(raw)
        except (json.JSONDecodeError, OSError) as exc:
            raise ChecksumError(f"Cannot read manifest: {exc}") from exc

        stored = data.get("checksum", "")
        computed = _compute_checksum(data)
        if stored != computed:
            raise ChecksumError(
                f"Checksum mismatch: stored={stored}, computed={computed}"
            )

        inst = cls(
            platform=data.get("platform", ""),
            build_dir=data.get("build_directory", ""),
        )
        inst._data.update(data)
        return inst

    # -- Validation --------------------------------------------------------

    def validate_environment(
        self,
        chromium_src: Path,
        browseros_dir: Path,
        repo_root: Path,
    ) -> List[str]:
        """Compare the current environment against the stored manifest.

        Returns a list of human-readable mismatch descriptions.  An empty
        list means the environment is identical to when the manifest was
        created.
        """
        mismatches: List[str] = []

        def _check(label: str, stored: str, current: str) -> None:
            if stored != current:
                mismatches.append(
                    f"{label}: manifest={stored!r}, current={current!r}"
                )

        _check("chromium_version",
               self._data["chromium_version"],
               _read_chromium_version(chromium_src))

        _check("browseros_commit",
               self._data["browseros_commit"],
               _git_hash(browseros_dir))

        _check("bharat_browser_commit",
               self._data["bharat_browser_commit"],
               _git_hash(repo_root))

        _check("repository_commit_sha",
               self._data["repository_commit_sha"],
               _git_hash(repo_root))

        _check("platform",
               self._data["platform"],
               sys.platform)

        _check("architecture",
               self._data["architecture"],
               _get_architecture())

        _check("python_version",
               self._data["python_version"],
               get_python_version())

        _check("compiler_version",
               self._data["compiler_version"],
               get_compiler_version())

        return mismatches

    # -- Mutation ----------------------------------------------------------

    def update(self, **kwargs: Any) -> None:
        """Set one or more fields and refresh the timestamp.

        The ``checksum`` field cannot be updated directly; call
        :meth:`save` to recompute it.
        """
        for key, value in kwargs.items():
            if key == "checksum":
                raise ValueError("Cannot set checksum directly; use save()")
            if key not in _FIELD_DEFAULTS:
                raise KeyError(f"Unknown field: {key}")
            self._data[key] = value
        self._data["timestamp"] = datetime.now(timezone.utc).isoformat()

    # -- Dict-like access --------------------------------------------------

    def __getitem__(self, key: str) -> Any:
        if key not in _FIELD_DEFAULTS:
            raise KeyError(key)
        return self._data[key]

    def __setitem__(self, key: str, value: Any) -> None:
        if key == "checksum":
            raise ValueError("Cannot set checksum directly; use save()")
        if key not in _FIELD_DEFAULTS:
            raise KeyError(key)
        self._data[key] = value
        self._data["timestamp"] = datetime.now(timezone.utc).isoformat()

    # -- Conversion --------------------------------------------------------

    def to_dict(self) -> Dict[str, Any]:
        """Return all fields as a plain dict, *excluding* ``checksum``."""
        return {k: v for k, v in self._data.items() if k != "checksum"}

    # -- Checksum verification ---------------------------------------------

    def checksum_valid(self) -> bool:
        """``True`` when the stored checksum matches the recomputed value."""
        stored = self._data.get("checksum")
        if not stored:
            return False
        return stored == _compute_checksum(self._data)

    def __repr__(self) -> str:
        return (
            f"BuildManifest(platform={self._data['platform']!r}, "
            f"build_dir={self._data['build_directory']!r}, "
            f"state={self._data['workflow_state']!r})"
        )
