#!/usr/bin/env python3
"""Tests for the Bharat Browser server version bump script."""

import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).with_name("bump_server_version.py")

PACKAGE_JSON = "\n".join(
    [
        "{",
        '  "name": "@browseros/server",',
        '  "version": "0.0.98",',
        '  "description": "Bharat Browser server",',
        '  "scripts": {',
        '    "build": "bun ../../scripts/build/server.ts --target=all"',
        "  }",
        "}",
        "",
    ]
)

# bun.lock is JSONC-ish (trailing commas); the script regex-edits, never parses it.
BUN_LOCK = "\n".join(
    [
        "{",
        '  "lockfileVersion": 1,',
        '  "workspaces": {',
        '    "apps/server": {',
        '      "name": "@browseros/server",',
        '      "version": "0.0.98",',
        '      "bin": {',
        '        "browseros-server": "./src/index.ts",',
        "      },",
        "    },",
        "  },",
        "}",
        "",
    ]
)


def _load_module():
    spec = importlib.util.spec_from_file_location("bump_server_version", SCRIPT_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Could not load {SCRIPT_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class BumpServerVersionTest(unittest.TestCase):
    def setUp(self) -> None:
        self.module = _load_module()
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "apps" / "server").mkdir(parents=True)
        self.pkg = self.root / "apps" / "server" / "package.json"
        self.lock = self.root / "bun.lock"
        self.pkg.write_text(PACKAGE_JSON)
        self.lock.write_text(BUN_LOCK)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_default_bumps_patch_in_both_files(self) -> None:
        version = self.module.bump_server_version(self.root)

        self.assertEqual(version, "0.0.99")
        self.assertIn('"version": "0.0.99",', self.pkg.read_text())
        self.assertIn('"version": "0.0.99",', self.lock.read_text())
        self.assertNotIn("0.0.98", self.pkg.read_text())
        self.assertNotIn("0.0.98", self.lock.read_text())

    def test_set_writes_exact_version(self) -> None:
        version = self.module.bump_server_version(self.root, set_version="1.2.3")

        self.assertEqual(version, "1.2.3")
        self.assertIn('"version": "1.2.3",', self.pkg.read_text())
        self.assertIn('"version": "1.2.3",', self.lock.read_text())

    def test_patch_rolls_into_three_digits(self) -> None:
        self.pkg.write_text(PACKAGE_JSON.replace("0.0.98", "0.0.99"))
        self.lock.write_text(BUN_LOCK.replace("0.0.98", "0.0.99"))

        version = self.module.bump_server_version(self.root)

        self.assertEqual(version, "0.0.100")
        self.assertIn('"version": "0.0.100",', self.pkg.read_text())
        self.assertIn('"version": "0.0.100",', self.lock.read_text())

    def test_only_version_line_changes_in_package_json(self) -> None:
        before = self.pkg.read_text()
        self.module.bump_server_version(self.root)
        after = self.pkg.read_text()

        diff = [
            (b, a)
            for b, a in zip(before.splitlines(), after.splitlines())
            if b != a
        ]
        self.assertEqual(len(before.splitlines()), len(after.splitlines()))
        self.assertEqual(diff, [('  "version": "0.0.98",', '  "version": "0.0.99",')])

    def test_missing_server_block_raises(self) -> None:
        self.lock.write_text('{\n  "lockfileVersion": 1\n}\n')

        with self.assertRaisesRegex(ValueError, "exactly one"):
            self.module.bump_server_version(self.root)

    def test_set_is_idempotent(self) -> None:
        self.module.bump_server_version(self.root, set_version="0.0.99")
        version = self.module.bump_server_version(self.root, set_version="0.0.99")

        self.assertEqual(version, "0.0.99")
        self.assertIn('"version": "0.0.99",', self.pkg.read_text())
        self.assertIn('"version": "0.0.99",', self.lock.read_text())

    def test_main_prints_new_version(self) -> None:
        import io
        from contextlib import redirect_stdout

        buffer = io.StringIO()
        with redirect_stdout(buffer):
            rc = self.module.main(["--agent-root", str(self.root)])

        self.assertEqual(rc, 0)
        self.assertEqual(buffer.getvalue().strip(), "0.0.99")


if __name__ == "__main__":
    unittest.main()
