#!/usr/bin/env python3
"""Regression tests for local doctor diagnostics."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

import doctor


def write_linux_elf(path: Path, machine: int) -> None:
    data = bytearray(128)
    data[0:4] = b"\x7fELF"
    data[4] = 2
    data[5] = 1
    data[18:20] = machine.to_bytes(2, "little")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(bytes(data))


def write_windows_pe(path: Path, machine: int) -> None:
    data = bytearray(256)
    data[0:2] = b"MZ"
    data[0x3C:0x40] = (0x80).to_bytes(4, "little")
    data[0x80:0x84] = b"PE\0\0"
    data[0x84:0x86] = machine.to_bytes(2, "little")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(bytes(data))


class DoctorTests(unittest.TestCase):
    def test_binary_target_detection(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            linux = root / "dist" / "gateway" / "linux-amd64" / "gateway"
            windows = root / "dist" / "gateway" / "windows-amd64" / "gateway.exe"
            write_linux_elf(linux, 0x3E)
            write_windows_pe(windows, 0x8664)
            self.assertEqual(doctor.read_binary_target(linux), "linux-amd64")
            self.assertEqual(doctor.read_binary_target(windows), "windows-amd64")

    def test_shell_lf_detects_crlf(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            script = root / "scripts" / "bad.sh"
            script.parent.mkdir(parents=True, exist_ok=True)
            script.write_bytes(b"#!/usr/bin/env sh\r\n")
            result = doctor.check_shell_lf(root)
            self.assertEqual(result.status, "FAIL")
            self.assertIn("scripts/bad.sh", result.details["files"])

    def test_settings_check_does_not_return_auth_token(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            settings = Path(tmp) / "settings.json"
            secret = "secret-token-value-that-must-not-appear"
            settings.write_text(
                json.dumps(
                    {
                        "env": {
                            "ANTHROPIC_BASE_URL": "http://127.0.0.1:18080",
                            "ANTHROPIC_AUTH_TOKEN": secret,
                        }
                    }
                ),
                encoding="utf-8",
            )
            result = doctor.check_claude_settings(settings, "http://127.0.0.1:18080")
            self.assertEqual(result.status, "OK")
            rendered = doctor.render_text([result])
            self.assertNotIn(secret, rendered)

    def test_auth_conflict_fails_when_key_and_token_are_set(self) -> None:
        result = doctor.check_auth_conflict(
            {
                "ANTHROPIC_API_KEY": "sk-ant-not-printed",
                "ANTHROPIC_AUTH_TOKEN": "token-not-printed",
            }
        )
        self.assertEqual(result.status, "FAIL")
        self.assertNotIn("sk-ant-not-printed", doctor.render_text([result]))
        self.assertNotIn("token-not-printed", doctor.render_text([result]))

    def test_token_helper_does_not_print_secret_command(self) -> None:
        secret = "placeholder-token-that-must-not-appear"
        result = doctor.check_token_helper(
            {"CODEX_ACCESS_TOKEN_COMMAND": f"printf {secret}"},
            run_helper=False,
        )
        rendered = doctor.render_text([result])
        encoded = json.dumps(result.__dict__)
        self.assertEqual(result.status, "OK")
        self.assertNotIn(secret, rendered)
        self.assertNotIn(secret, encoded)
        self.assertNotIn("printf", rendered)

    def test_default_token_helper_command_is_supported(self) -> None:
        result = doctor.check_token_helper(
            {"CODEX_DEFAULT_ACCESS_TOKEN_COMMAND": "printf safe-placeholder-token-value"},
            run_helper=False,
        )
        self.assertEqual(result.status, "OK")

    def test_stream_diagnostics_reports_latest_category_without_raw_error(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            log_path = Path(tmp) / "gateway.out.log"
            log_path.write_text(
                "\n".join(
                    [
                        json.dumps({"event": "gateway_start"}),
                        json.dumps(
                            {
                                "event": "codex_web_stream_read_error",
                                "request_id": "req_1",
                                "category": "read_error",
                                "events_seen": 2,
                                "response_id_set": True,
                                "error": "token=secret-value",
                            }
                        ),
                    ]
                ),
                encoding="utf-8",
            )
            result = doctor.check_stream_diagnostics(log_path)
            rendered = doctor.render_text([result])
            self.assertEqual(result.status, "WARN")
            self.assertIn("read_error", rendered)
            self.assertIn("req_1", rendered)
            self.assertNotIn("secret-value", rendered)

    def test_claude_settings_reports_agent_task_policy(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            settings = Path(tmp) / "settings.json"
            settings.write_text(
                json.dumps(
                    {
                        "env": {
                            "ANTHROPIC_BASE_URL": "http://127.0.0.1:18080",
                            "ANTHROPIC_AUTH_TOKEN": "token-not-printed",
                        },
                        "permissions": {"deny": ["Agent", "Task"]},
                    }
                ),
                encoding="utf-8",
            )
            result = doctor.check_claude_settings(settings, "http://127.0.0.1:18080")
            rendered = doctor.render_text([result])
            self.assertEqual(result.status, "OK")
            self.assertIn("agent_task_tools=denied", rendered)

    def test_next_actions_are_rendered_without_tokens(self) -> None:
        result = doctor.next_action_result("http://127.0.0.1:18080")
        rendered = doctor.render_text([result])
        self.assertIn("check_local_codex_gateway.sh", rendered)
        self.assertIn("run_claude_code_direct.sh", rendered)
        self.assertNotIn("token-not-printed", rendered)


if __name__ == "__main__":
    unittest.main()
