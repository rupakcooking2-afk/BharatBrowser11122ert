#!/usr/bin/env python3
"""
Bharat Browser Build System - Main Entry Point

Unified CLI for building, developing, and releasing Bharat Browser.

Usage:
    # As installed command:
    browseros build --help

    # As module:
    python -m build.browseros build --help
"""
import typer

from .cli import build

# Create main app
app = typer.Typer(
    help="Bharat Browser Build System",
    pretty_exceptions_enable=False,
    pretty_exceptions_show_locals=False
)

# Create build sub-app and register build.main as its callback
build_app = typer.Typer(
    pretty_exceptions_enable=False,
    pretty_exceptions_show_locals=False
)
build_app.callback(invoke_without_command=True)(build.main)

# Add build as a subcommand
app.add_typer(build_app, name="build", help="Build Bharat Browser")

# Add dev command
from .cli import dev
app.add_typer(dev.app, name="dev", help="Dev patch management")

# Release automation commands
from .cli import release
app.add_typer(release.app, name="release", help="Release automation")

# OTA update commands
from .cli import ota
app.add_typer(ota.app, name="ota", help="OTA update automation")

# Third-party resource uploads (Lima, future VM disk + agent tarballs)
from .cli import storage
app.add_typer(storage.app, name="upload", help="Upload third-party resources to R2")


if __name__ == "__main__":
    app()
