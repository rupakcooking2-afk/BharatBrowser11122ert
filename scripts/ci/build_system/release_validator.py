"""Release validation module for Phase 9 of a fault-tolerant distributed Chromium build system.

Verifies all build artifacts before packaging and releasing. Ensures the build
is complete and all components are valid across Linux, Windows, and macOS.
"""

from __future__ import annotations

import hashlib
import os
import stat
import subprocess
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Dict, List, Optional

__all__ = [
    "ReleaseValidator",
    "get_release_artifacts",
    "generate_release_notes",
    "ValidationResult",
]


# ---------------------------------------------------------------------------
# ValidationResult
# ---------------------------------------------------------------------------


@dataclass
class ValidationResult:
    """Result of a single validation check."""

    passed: bool
    failures: List[str] = field(default_factory=list)

    def __bool__(self) -> bool:
        return self.passed

    def __or__(self, other: ValidationResult) -> ValidationResult:
        return ValidationResult(
            passed=self.passed and other.passed,
            failures=self.failures + other.failures,
        )


# ---------------------------------------------------------------------------
# ReleaseValidator
# ---------------------------------------------------------------------------


class ReleaseValidator:
    """Validates build artifacts for Bharat Browser release.

    Parameters
    ----------
    chromium_src :
        Root of the Chromium source tree (contains ``chrome/``, ``out/``, etc.).
    build_dir :
        Relative build output directory (e.g. ``out/Default``, ``out/Default_x64``).
    platform :
        Target platform string (``"linux"``, ``"windows"``, ``"macos"``).
    """

    def __init__(
        self,
        chromium_src: Path,
        build_dir: str,
        platform: str,
    ) -> None:
        self.chromium_src = Path(chromium_src)
        self.build_dir = build_dir
        self.platform = platform.lower()
        self._build_output = self.chromium_src / self.build_dir

    # -- private helpers ---------------------------------------------------

    def _build_path(self, *parts: str) -> Path:
        return self._build_output.joinpath(*parts)

    def _fail(self, message: str) -> ValidationResult:
        return ValidationResult(passed=False, failures=[message])

    def _pass(self) -> ValidationResult:
        return ValidationResult(passed=True)

    def _check_exists(self, path: Path, label: str) -> ValidationResult:
        if not path.exists():
            return self._fail(f"{label} not found: {path}")
        return self._pass()

    def _check_nonzero(self, path: Path, label: str) -> ValidationResult:
        result = self._check_exists(path, label)
        if not result.passed:
            return result
        if path.stat().st_size == 0:
            return self._fail(f"{label} is empty (zero bytes): {path}")
        return self._pass()

    def _check_executable(self, path: Path, label: str) -> ValidationResult:
        result = self._check_nonzero(path, label)
        if not result.passed:
            return result
        if self.platform != "windows":
            mode = path.stat().st_mode
            if not (mode & stat.S_IXUSR):
                return self._fail(
                    f"{label} is not executable: {path} "
                    f"(mode={oct(mode)})"
                )
        return self._pass()

    def _check_directory(self, path: Path, label: str) -> ValidationResult:
        if not path.is_dir():
            return self._fail(f"{label} directory not found: {path}")
        return self._pass()

    def _check_globs(self, pattern: str, label: str) -> ValidationResult:
        matches = list(self._build_output.glob(pattern))
        if not matches:
            return self._fail(
                f"{label}: no files matching {pattern!r} in {self._build_output}"
            )
        return self._pass()

    # -- Individual validations -------------------------------------------

    def validate_chrome_executable(self) -> ValidationResult:
        if self.platform == "linux":
            candidates = ["chrome", "bharat-browser"]
            for name in candidates:
                path = self._build_output / name
                result = self._check_executable(path, f"Linux executable {name}")
                if result.passed:
                    return result
            return self._fail(
                f"Linux executable not found in {self._build_output} "
                f"(tried: {candidates})"
            )
        elif self.platform == "windows":
            path = self._build_output / "chrome.exe"
            return self._check_nonzero(path, "Windows chrome.exe")
        elif self.platform == "macos":
            path = self._build_output / "Bharat Browser.app"
            result = self._check_directory(path, "macOS Bharat Browser.app bundle")
            if not result.passed:
                return result
            executable = path / "Contents" / "MacOS" / "Bharat Browser"
            return self._check_executable(
                executable, "macOS Bharat Browser executable"
            )
        else:
            return self._fail(f"Unknown platform: {self.platform}")

    def validate_browseros_server(self) -> ValidationResult:
        server_dir = self._build_output / "BrowserOSServer"
        result = self._check_directory(server_dir, "BrowserOSServer")
        if not result.passed:
            return result
        for entry in server_dir.rglob("*"):
            if entry.is_file() and entry.stat().st_size > 0:
                return self._pass()
        return self._fail(
            f"BrowserOSServer directory {server_dir} exists but contains "
            f"no non-empty files"
        )

    def validate_browseros_apis(self) -> ValidationResult:
        return self._check_globs(
            "browseros_api*", "BrowserOS API extensions"
        )

    def validate_extensions(self) -> ValidationResult:
        return self._check_globs(
            "extensions/*", "Bundled extensions"
        )

    def validate_installer(self, platform: Optional[str] = None) -> ValidationResult:
        target = (platform or self.platform).lower()
        if target == "windows":
            result = self._check_globs(
                "*_installer.exe", "Windows mini_installer"
            )
            result |= self._check_globs(
                "*_installer.symbols.zip", "Windows symbols zip"
            )
            return result
        elif target == "linux":
            result = self._check_globs("*.AppImage", "Linux AppImage")
            result |= self._check_globs("*.deb", "Linux .deb package")
            return result
        elif target == "macos":
            result = self._check_globs("*.dmg", "macOS .dmg image")
            return result
        else:
            return self._fail(f"Unknown platform for installer: {target}")

    def validate_branding(self) -> ValidationResult:
        branding = self._build_output / "bharat_branding"
        if branding.is_dir():
            return self._pass()
        branding_alt = self._build_output / "chrome" / "app" / "theme" / "bharat"
        if branding_alt.is_dir():
            return self._pass()
        product_dir = self._build_output / "product"
        if product_dir.is_dir():
            files = list(product_dir.rglob("*"))
            if any(f.is_file() for f in files):
                return self._pass()
        return self._fail(
            f"Bharat Browser branding not found in {self._build_output}"
        )

    def validate_icons(self) -> ValidationResult:
        return self._check_globs(
            "*.ico", "Windows icon files"
        ) | self._check_globs(
            "*.png", "PNG icon files"
        )

    def validate_resources(self) -> ValidationResult:
        resources = self._build_output / "resources"
        result = self._check_directory(resources, "Resources directory")
        if not result.passed:
            return result
        contents = list(resources.iterdir())
        if not contents:
            return self._fail(f"Resources directory is empty: {resources}")
        return self._pass()

    def validate_locales(self) -> ValidationResult:
        locales = self._build_output / "locales"
        result = self._check_directory(locales, "Locales directory")
        if not result.passed:
            return result
        pak_files = list(locales.glob("*.pak"))
        if not pak_files:
            return self._fail(
                f"No locale .pak files found in {locales}"
            )
        return self._pass()

    def validate_ai_integration(self) -> ValidationResult:
        ai_dir = self._build_output / "ai"
        if ai_dir.is_dir():
            return self._check_globs("ai/*", "AI integration components")
        ai_globs = self._build_output.glob("*ai*")
        ai_matches = [p for p in ai_globs if p.is_file() or p.is_dir()]
        if ai_matches:
            return self._pass()
        return ValidationResult(
            passed=True,
            failures=["AI integration components not found (optional, skipped)"],
        )

    def validate_cdp(self) -> ValidationResult:
        if self.platform == "windows":
            path = self._build_output / "chrome.exe"
            return self._check_exists(path, "CDP via chrome.exe")
        elif self.platform == "linux":
            path = self._build_output / "chrome"
            return self._check_exists(path, "CDP via chrome")
        elif self.platform == "macos":
            path = self._build_output / "Bharat Browser.app"
            return self._check_exists(path, "CDP via Bharat Browser.app")
        return self._fail(f"Unknown platform: {self.platform}")

    def validate_headless(self) -> ValidationResult:
        headless = self._build_output / "headless_shell"
        if self.platform == "windows":
            headless = headless.with_suffix(".exe")
        result = self._check_executable(headless, "Headless shell")
        if not result.passed:
            test_shell = self._build_output / "chrome-headless"
            if self.platform == "windows":
                test_shell = test_shell.with_suffix(".exe")
            result = self._check_executable(test_shell, "chrome-headless-shell")
        return result

    def validate_version(self) -> ValidationResult:
        version_file = self.chromium_src / "chrome" / "VERSION"
        result = self._check_nonzero(version_file, "Chrome VERSION file")
        if not result.passed:
            return result
        components: Dict[str, str] = {}
        for line in version_file.read_text().splitlines():
            line = line.strip()
            if "=" in line:
                key, val = line.split("=", 1)
                components[key.strip()] = val.strip()
        required = {"MAJOR", "MINOR", "BUILD", "PATCH"}
        missing = required - set(components.keys())
        if missing:
            return self._fail(
                f"VERSION file missing components: {missing}"
            )
        return self._pass()

    def validate_all(self) -> ValidationResult:
        checks = [
            ("chrome_executable", self.validate_chrome_executable()),
            ("browseros_server", self.validate_browseros_server()),
            ("browseros_apis", self.validate_browseros_apis()),
            ("extensions", self.validate_extensions()),
            ("installer", self.validate_installer()),
            ("branding", self.validate_branding()),
            ("icons", self.validate_icons()),
            ("resources", self.validate_resources()),
            ("locales", self.validate_locales()),
            ("ai_integration", self.validate_ai_integration()),
            ("cdp", self.validate_cdp()),
            ("headless", self.validate_headless()),
            ("version", self.validate_version()),
        ]
        aggregated = ValidationResult(passed=True)
        for name, result in checks:
            if not result.passed:
                aggregated.passed = False
                aggregated.failures.append(f"[{name}]")
                aggregated.failures.extend(f"  {f}" for f in result.failures)
        return aggregated

    def generate_checksums(self, artifact_dir: Path) -> Dict[str, str]:
        artifact_dir = Path(artifact_dir)
        checksums: Dict[str, str] = {}
        if not artifact_dir.is_dir():
            return checksums
        for entry in sorted(artifact_dir.rglob("*")):
            if entry.is_file():
                relative = entry.relative_to(artifact_dir).as_posix()
                checksums[relative] = hashlib.sha256(
                    entry.read_bytes()
                ).hexdigest()
        return checksums


# ---------------------------------------------------------------------------
# Module-level helpers
# ---------------------------------------------------------------------------


def get_release_artifacts(platform: str, release_dir: Path) -> List[Path]:
    """Return list of all release artifact files for a platform.

    Glob patterns mirror the artifact upload paths in ``nightly-build.yml``.
    """
    platform = platform.lower()
    release_dir = Path(release_dir)
    patterns: List[str] = []

    if platform == "windows":
        patterns = ["*_installer.exe", "*_installer.zip"]
    elif platform == "linux":
        patterns = ["*.AppImage", "*.deb"]
    elif platform == "macos":
        patterns = ["*.dmg"]

    artifacts: List[Path] = []
    for pattern in patterns:
        artifacts.extend(sorted(release_dir.glob(pattern)))
    return artifacts


def generate_release_notes(
    version: str,
    platform: str,
    commit_sha: str,
    artifacts: List[Path],
) -> str:
    """Generate markdown release notes for a Bharat Browser release."""
    lines: List[str] = [
        f"# Bharat Browser v{version}",
        "",
        f"**Platform:** {platform}",
        f"**Commit:** `{commit_sha}`",
        f"**Date:** {datetime.now(timezone.utc).strftime('%Y-%m-%d %H:%M UTC')}",
        "",
        "## Artifacts",
        "",
    ]
    for art in artifacts:
        size = art.stat().st_size
        if size < 1024:
            size_str = f"{size} B"
        elif size < 1024 * 1024:
            size_str = f"{size / 1024:.1f} KB"
        else:
            size_str = f"{size / (1024 * 1024):.1f} MB"
        lines.append(f"- `{art.name}` ({size_str})")

    lines.extend([
        "",
        "## Checksums",
        "",
        "SHA-256 checksums for all artifacts are available in the "
        "accompanying `.sha256sums` file.",
        "",
        "---",
        "",
        "*Generated by Bharat Browser CI*",
    ])

    return "\n".join(lines)