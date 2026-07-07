#!/usr/bin/env python3
"""Build configuration module for BrowserOS build system"""

import re
import sys

from ...common.module import CommandModule, ValidationError
from ...common.context import Context
from ...common.utils import (
    run_command,
    log_info,
    log_warning,
    log_success,
    join_paths,
    IS_LINUX,
    IS_WINDOWS,
)


class ConfigureModule(CommandModule):
    produces = []
    requires = []
    description = "Configure build with GN"

    def validate(self, ctx: Context) -> None:
        if not ctx.chromium_src.exists():
            raise ValidationError(f"Chromium source not found: {ctx.chromium_src}")

        if not ctx.paths.gn_flags_file:
            raise ValidationError("GN flags file not set")

        flags_file = join_paths(ctx.root_dir, ctx.paths.gn_flags_file)
        if not flags_file.exists():
            raise ValidationError(f"GN flags file not found: {flags_file}")

    def execute(self, ctx: Context) -> None:
        log_info(f"\n⚙️  Configuring {ctx.build_type} build for {ctx.architecture}...")

        if IS_LINUX():
            self._ensure_linux_sysroot(ctx)

        out_path = join_paths(ctx.chromium_src, ctx.out_dir)
        out_path.mkdir(parents=True, exist_ok=True)

        flags_file = join_paths(ctx.root_dir, ctx.paths.gn_flags_file)
        args_file = ctx.get_gn_args_file()

        args_content = flags_file.read_text()
        args_content += f'\ntarget_cpu = "{ctx.architecture}"\n'

        args_file.write_text(args_content)

        gn_cmd = "gn.bat" if IS_WINDOWS() else "gn"

        if ctx.build_type != "debug":
            self._configure_with_arg_filter(ctx, gn_cmd, args_file)
        else:
            run_command([gn_cmd, "gen", ctx.out_dir], cwd=ctx.chromium_src)

        log_success("Build configured")

    def _configure_with_arg_filter(self, ctx, gn_cmd, args_file):
        # Pass 1 — probe: generate WITHOUT --fail-on-unused-args, then parse
        # warnings to detect GN args the current Chromium version does not
        # recognise (e.g. use_jumbo_build was removed after M147).
        log_info("Probing for unsupported GN build arguments...")
        result = run_command(
            [gn_cmd, "gen", ctx.out_dir],
            cwd=ctx.chromium_src,
            check=False,
        )
        if result.returncode != 0:
            raise RuntimeError(
                f"gn gen failed (exit {result.returncode}): {result.stdout}"
            )

        unsupported = self._find_unsupported_args(result.stdout)
        if unsupported:
            log_warning(
                f"Removing unsupported GN arg(s): {', '.join(sorted(unsupported))}"
            )
            self._remove_args_from_file(args_file, unsupported)

        # Pass 2 — validate: regenerate with --fail-on-unused-args to ensure
        # all remaining args are recognised.
        run_command(
            [gn_cmd, "gen", ctx.out_dir, "--fail-on-unused-args"],
            cwd=ctx.chromium_src,
        )

    @staticmethod
    def _find_unsupported_args(output):
        return set(re.findall(r'Unused build argument "(\w+)"', output))

    @staticmethod
    def _remove_args_from_file(args_file, unsupported):
        lines = args_file.read_text().splitlines()
        filtered = []
        for line in lines:
            stripped = line.strip()
            if not stripped or stripped.startswith("#"):
                filtered.append(line)
                continue
            match = re.match(r"(\w+)\s*=", stripped)
            if match and match.group(1) in unsupported:
                continue
            filtered.append(line)
        text = "\n".join(filtered)
        if text:
            text += "\n"
        args_file.write_text(text)

    def _ensure_linux_sysroot(self, ctx: Context) -> None:
        install_script = (
            ctx.chromium_src / "build" / "linux" / "sysroot_scripts" / "install-sysroot.py"
        )
        if not install_script.exists():
            log_warning(
                f"⚠️  install-sysroot.py not found at {install_script}; "
                f"skipping sysroot bootstrap. gn gen will fail if the "
                f"{ctx.architecture} sysroot is missing."
            )
            return

        # install-sysroot.py accepts our arch names directly: it translates
        # `x64`→`amd64` internally via ARCH_TRANSLATIONS, and `arm64` is a
        # valid pass-through value.
        log_info(
            f"📦 Ensuring Linux sysroot for {ctx.architecture} (idempotent)..."
        )
        run_command(
            [sys.executable, str(install_script), f"--arch={ctx.architecture}"],
            cwd=ctx.chromium_src,
        )
