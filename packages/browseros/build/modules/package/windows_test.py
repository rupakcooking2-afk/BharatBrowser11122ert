#!/usr/bin/env python3
"""Tests for the Windows packaging module's autoninja routing."""

import unittest
from pathlib import Path
from types import SimpleNamespace
from typing import cast
from unittest import mock

from . import windows
from ..compile import standard
from ...common.context import Context


class BuildMiniInstallerTest(unittest.TestCase):
    def test_routes_through_shared_argv_builder_with_override(self):
        ctx = cast(
            Context,
            SimpleNamespace(
                out_dir="out/Default_x64", chromium_src=Path("/tmp/chromium-src")
            ),
        )
        with (
            mock.patch.object(windows, "run_command") as run_cmd,
            mock.patch.object(standard, "IS_WINDOWS", return_value=False),
            mock.patch("os.chdir"),
            mock.patch("os.getcwd", return_value="/anywhere"),
            mock.patch.dict("os.environ", {"BROWSEROS_NINJA_JOBS": "8"}, clear=True),
        ):
            result = windows.build_mini_installer(ctx)
        run_cmd.assert_called_once_with(
            ["autoninja", "-C", "out/Default_x64", "-j", "8", "setup", "mini_installer"]
        )
        # Artifacts were never produced (run_command is mocked), so it reports failure.
        self.assertFalse(result)


if __name__ == "__main__":
    unittest.main()
