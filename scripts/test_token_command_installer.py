#!/usr/bin/env python3
"""Regression tests for the local Codex token-command installer."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
INSTALLER = ROOT / "scripts" / "install_codex_token_command.sh"


@unittest.skipUnless(shutil.which("sh"), "sh is required for token command installer tests")
class TokenCommandInstallerTests(unittest.TestCase):
    def test_installed_helper_reads_access_token_from_auth_json(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            helper = tmp_path / "bin" / "codex-access-token"
            auth_file = tmp_path / "auth.json"
            auth_file.write_text(
                json.dumps({"tokens": {"access_token": "fixture-access-token"}}),
                encoding="utf-8",
            )

            subprocess.run(
                [
                    "sh",
                    str(INSTALLER),
                    "--output",
                    str(helper),
                    "--auth-file",
                    str(auth_file),
                    "--no-refresh",
                ],
                cwd=ROOT,
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )

            env = dict(os.environ)
            env["CODEX_TOKEN_REFRESH"] = "0"
            result = subprocess.run(
                [str(helper)],
                cwd=ROOT,
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                env=env,
            )

            self.assertEqual(result.stdout.strip(), "fixture-access-token")
            self.assertEqual(result.stderr.strip(), "")

    def test_installer_refuses_to_overwrite_without_force(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            helper = Path(tmp) / "codex-access-token"
            helper.write_text("#!/usr/bin/env sh\n", encoding="utf-8")

            result = subprocess.run(
                [
                    "sh",
                    str(INSTALLER),
                    "--output",
                    str(helper),
                    "--auth-file",
                    str(Path(tmp) / "auth.json"),
                    "--no-refresh",
                ],
                cwd=ROOT,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("already exists", result.stderr)


if __name__ == "__main__":
    unittest.main()
