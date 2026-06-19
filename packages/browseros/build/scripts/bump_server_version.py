#!/usr/bin/env python3
"""Bump the @browseros/server version in package.json + bun.lock, print the result.

Used by the nightly release flow: release-server.yml bumps in-place before building,
and the macOS build job applies the same version (--set) so the bump rides in the
existing browser-version PR. Mirrors build/scripts/bump_version.py for the browser.
"""

import argparse
import re
import sys
from pathlib import Path
from typing import Sequence

# Anchor on the workspace package's own name so the single version line that follows
# is matched in BOTH package.json and bun.lock; \s* spans the newline + indentation.
_VERSION_RE = re.compile(
    r'("name":\s*"@browseros/server",\s*"version":\s*")(\d+\.\d+\.\d+)(")'
)


def _replace_version(text: str, new_version: str) -> str:
    """Rewrite the single @browseros/server version occurrence, or fail loudly."""
    updated, count = _VERSION_RE.subn(
        lambda match: f"{match.group(1)}{new_version}{match.group(3)}", text
    )
    if count != 1:
        raise ValueError(
            f"Expected exactly one @browseros/server version entry, found {count}"
        )
    return updated


def _current_version(package_json_text: str) -> str:
    match = _VERSION_RE.search(package_json_text)
    if match is None:
        raise ValueError("Could not find @browseros/server version in package.json")
    return match.group(2)


def _next_patch(version: str) -> str:
    parts = version.split(".")
    if len(parts) != 3 or not all(part.isdigit() for part in parts):
        raise ValueError(f"Unsupported server version (expected MAJOR.MINOR.PATCH): {version}")
    major, minor, patch = (int(part) for part in parts)
    return f"{major}.{minor}.{patch + 1}"


def bump_server_version(agent_root: Path, set_version: str | None = None) -> str:
    """Set (or patch-bump) the server version in package.json + bun.lock; return it."""
    package_json = agent_root / "apps" / "server" / "package.json"
    bun_lock = agent_root / "bun.lock"

    new_version = set_version or _next_patch(_current_version(package_json.read_text()))

    for path in (package_json, bun_lock):
        text = path.read_text()
        updated = _replace_version(text, new_version)
        if updated != text:
            path.write_text(updated)

    return new_version


def main(argv: Sequence[str] | None = None) -> int:
    """CLI for GitHub Actions: bump (or --set) the server version and print it."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--set", dest="set_version", default=None)
    parser.add_argument(
        "--agent-root",
        default=str(Path(__file__).resolve().parents[3] / "browseros-agent"),
    )
    args = parser.parse_args(argv)

    print(bump_server_version(Path(args.agent_root), args.set_version))
    return 0


if __name__ == "__main__":
    sys.exit(main())
