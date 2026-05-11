#!/usr/bin/env python3
"""Regression tests for Phase 9 review fixes."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile

import benchmark_smoke
import inspect_release_archive
import package_release


def add_executable_member(archive: ZipFile, name: str, payload: bytes = b"binary") -> None:
    from zipfile import ZipInfo

    info = ZipInfo(name)
    info.create_system = 3
    info.external_attr = 0o755 << 16
    info.compress_type = ZIP_DEFLATED
    archive.writestr(info, payload)


class Phase9ReviewFixTests(unittest.TestCase):
    def test_package_release_excludes_nested_credentials_and_non_gateway_dist(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            files = {
                "README.md": "ok",
                "old-release.zip": "nested release archive",
                "nested/.env": "OPENAI_API_KEY=sk-test",
                "nested/.env.local": "ANTHROPIC_API_KEY=sk-ant-test",
                "secrets/id_rsa": "private-key",
                "dist/benchmark-smoke/smoke-report.md": "private report",
                "dist/cache/local.tmp": "cache",
                "dist/gateway/gateway.txt": "gateway output",
            }
            for rel, content in files.items():
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")

            included = {
                path.relative_to(root).as_posix()
                for path in package_release.list_package_files(root)
            }
            self.assertIn("README.md", included)
            self.assertIn("dist/gateway/gateway.txt", included)
            self.assertNotIn("old-release.zip", included)
            self.assertNotIn("nested/.env", included)
            self.assertNotIn("nested/.env.local", included)
            self.assertNotIn("secrets/id_rsa", included)
            self.assertNotIn("dist/benchmark-smoke/smoke-report.md", included)
            self.assertNotIn("dist/cache/local.tmp", included)

    def test_package_release_excludes_all_reports_from_public_packages(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            files = {
                "README.md": "ok",
                "reports/private/evidence.md": "evidence",
                "reports/local-debug.md": "local debug",
                "reports/private/customer.md": "private report",
            }
            for rel, content in files.items():
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")

            included = {
                path.relative_to(root).as_posix()
                for path in package_release.list_package_files(root)
            }
            self.assertNotIn("reports/private/evidence.md", included)
            self.assertNotIn("reports/local-debug.md", included)
            self.assertNotIn("reports/private/customer.md", included)

    def test_package_release_normalizes_shell_scripts_to_lf(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            script = root / "scripts" / "start.sh"
            script.parent.mkdir(parents=True, exist_ok=True)
            script.write_bytes(b"#!/usr/bin/env sh\r\nset -eu\r\necho ok\r\n")
            archive_path = root / "release.zip"

            package_release.create_release_archive(root, archive_path)

            with ZipFile(archive_path, mode="r") as archive:
                payload = archive.read("scripts/start.sh")
                mode = (archive.getinfo("scripts/start.sh").external_attr >> 16) & 0o777
            self.assertEqual(payload, b"#!/usr/bin/env sh\nset -eu\necho ok\n")
            self.assertTrue(mode & 0o111)

    def test_package_release_marks_gateway_binary_executable(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            binary = root / "dist" / "gateway" / "linux-amd64" / "gateway"
            binary.parent.mkdir(parents=True, exist_ok=True)
            binary.write_bytes(b"\x7fELF")
            archive_path = root / "release.zip"

            package_release.create_release_archive(root, archive_path)

            with ZipFile(archive_path, mode="r") as archive:
                info = archive.getinfo("dist/gateway/linux-amd64/gateway")
                mode = (info.external_attr >> 16) & 0o777
            self.assertTrue(mode & 0o111)
            self.assertEqual(info.create_system, 3)

    def test_package_release_writes_release_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "README.md").write_text("ok", encoding="utf-8")
            archive_path = root / "release.zip"

            package_release.create_release_archive(root, archive_path)

            with ZipFile(archive_path, mode="r") as archive:
                manifest = archive.read("release.json").decode("utf-8")
            self.assertIn("required_gateway_binaries", manifest)
            self.assertIn("protocol_compatibility", manifest)

    def test_start_script_rebuild_falls_back_to_packaged_binary_without_go(self) -> None:
        root = Path(__file__).resolve().parents[1]
        script = (root / "scripts" / "start_local_codex_gateway.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            "WARNING: --rebuild requested but go is not available; using packaged gateway binary",
            script,
        )
        self.assertIn(
            "packaged gateway binary is older than Go source",
            script,
        )
        self.assertNotIn(
            "gateway binary is older than Go source for $target and go is not available",
            script,
        )

    def test_public_manual_documents_packaged_binary_fallback(self) -> None:
        root = Path(__file__).resolve().parents[1]
        docs = (root / "USER_MANUAL.md").read_text(encoding="utf-8")
        self.assertIn("built gateway binary", docs)
        self.assertIn("Go toolchain", docs)

    def test_benchmark_fixture_rejects_prefix_sibling_directory(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fixture_path = root / "benchmarks" / "fixtures_evil" / "smoke.json"
            fixture_path.parent.mkdir(parents=True, exist_ok=True)
            fixture_path.write_text("{}", encoding="utf-8")
            (root / "benchmarks" / "fixtures").mkdir(parents=True, exist_ok=True)

            fixture = {
                "synthetic": True,
                "gate": "pass",
                "metrics": {"latency_ms": 10},
            }
            with self.assertRaises(SystemExit) as exit_info:
                benchmark_smoke.validate_fixture(fixture, fixture_path, root)
            self.assertIn("benchmarks/fixtures", str(exit_info.exception))

    def test_benchmark_fixture_accepts_valid_path_under_fixtures(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fixture_path = root / "benchmarks" / "fixtures" / "smoke.json"
            fixture_path.parent.mkdir(parents=True, exist_ok=True)
            fixture_path.write_text("{}", encoding="utf-8")

            fixture = {
                "synthetic": True,
                "gate": "pass",
                "metrics": {"latency_ms": 10},
            }
            benchmark_smoke.validate_fixture(fixture, fixture_path, root)

    def test_archive_inspection_detects_forbidden_entries(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            archive_path = Path(tmp) / "release.zip"
            with ZipFile(archive_path, mode="w", compression=ZIP_DEFLATED) as archive:
                archive.writestr("README.md", "ok")
                archive.writestr("nested/.env", "secret")
                archive.writestr("old-release.zip", "nested")
                archive.writestr("dist/cache/local.tmp", "cache")

            _, violations = inspect_release_archive.inspect_archive(archive_path)
            self.assertTrue(any("nested/.env" in item for item in violations))
            self.assertTrue(any("old-release.zip" in item for item in violations))
            self.assertTrue(any("dist/cache/local.tmp" in item for item in violations))

    def test_archive_inspection_detects_crlf_shell_scripts(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            archive_path = Path(tmp) / "release.zip"
            with ZipFile(archive_path, mode="w", compression=ZIP_DEFLATED) as archive:
                archive.writestr("scripts/start.sh", "#!/usr/bin/env sh\r\nset -eu\r\n")

            _, violations = inspect_release_archive.inspect_archive(archive_path)
            self.assertTrue(any("scripts/start.sh" in item for item in violations))

    def test_archive_inspection_accepts_safe_entries(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            archive_path = Path(tmp) / "release.zip"
            with ZipFile(archive_path, mode="w", compression=ZIP_DEFLATED) as archive:
                archive.writestr("README.md", "ok")
                archive.writestr("dist/gateway/gateway.txt", "binary")
                archive.writestr("release.json", "{}")
                add_executable_member(archive, "dist/gateway/linux-amd64/gateway")
                add_executable_member(archive, "dist/gateway/linux-arm64/gateway")
                add_executable_member(archive, "dist/gateway/windows-amd64/gateway.exe")

            _, violations = inspect_release_archive.inspect_archive(archive_path)
            self.assertEqual(violations, [])

    def test_archive_inspection_requires_all_gateway_platforms(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            archive_path = Path(tmp) / "release.zip"
            with ZipFile(archive_path, mode="w", compression=ZIP_DEFLATED) as archive:
                archive.writestr("README.md", "ok")
                add_executable_member(archive, "dist/gateway/linux-amd64/gateway")
                add_executable_member(archive, "dist/gateway/windows-amd64/gateway.exe")

            _, violations = inspect_release_archive.inspect_archive(archive_path)
            self.assertTrue(any("dist/gateway/linux-arm64/gateway" in item for item in violations))

    def test_archive_inspection_rejects_public_reports(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            archive_path = Path(tmp) / "release.zip"
            with ZipFile(archive_path, mode="w", compression=ZIP_DEFLATED) as archive:
                archive.writestr("README.md", "ok")
                archive.writestr("reports/private/evidence.md", "evidence")
                archive.writestr("reports/private/customer.md", "private report")

            _, violations = inspect_release_archive.inspect_archive(archive_path)
            self.assertTrue(
                any("reports/private/evidence.md" in item for item in violations)
            )
            self.assertTrue(any("reports/private/customer.md" in item for item in violations))

    def test_runtime_package_keeps_only_runtime_files(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            files = {
                "LICENSE": "Apache License",
                "NOTICE": "Codex Gate",
                "README.md": "user docs",
                "README.en.md": "English user docs",
                "USER_MANUAL.md": "manual",
                "AGENTS.md": "developer agent contract",
                "cmd/gateway/main.go": "package main",
                "internal/gateway/server.go": "package gateway",
                "fixtures/README.md": "fixtures",
                "reports/private/evidence.md": "evidence",
                ".codex/config.toml": "[gateway]",
                ".agents/skills/pr-review-algo/SKILL.md": "skill",
                "scripts/start_local_codex_gateway.sh": "#!/usr/bin/env sh\n",
                "scripts/check_local_codex_gateway.sh": "#!/usr/bin/env sh\n",
                "scripts/check_claude_code_direct.sh": "#!/usr/bin/env sh\n",
                "dist/gateway/linux-amd64/gateway": "binary",
            }
            for rel, content in files.items():
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")

            included = {
                path.relative_to(root).as_posix()
                for path in package_release.list_package_files(root, kind="runtime")
            }

            self.assertIn("LICENSE", included)
            self.assertIn("NOTICE", included)
            self.assertIn("README.md", included)
            self.assertIn("README.en.md", included)
            self.assertIn("USER_MANUAL.md", included)
            self.assertIn("scripts/start_local_codex_gateway.sh", included)
            self.assertIn("dist/gateway/linux-amd64/gateway", included)
            self.assertNotIn("AGENTS.md", included)
            self.assertNotIn("cmd/gateway/main.go", included)
            self.assertNotIn("internal/gateway/server.go", included)
            self.assertNotIn("fixtures/README.md", included)
            self.assertNotIn("reports/private/evidence.md", included)
            self.assertNotIn(".codex/config.toml", included)
            self.assertNotIn(".agents/skills/pr-review-algo/SKILL.md", included)

    def test_dev_package_keeps_source_and_excludes_internal_artifacts(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            files = {
                "README.md": "ok",
                "AGENTS.md": "agent contract",
                "cmd/gateway/main.go": "package main",
                "internal/gateway/server.go": "package gateway",
                "fixtures/README.md": "fixtures",
                ".codex/config.toml": "[gateway]",
                ".codex/local/state.json": "local only",
                ".agents/skills/pr-review-algo/SKILL.md": "skill",
                "reports/private/evidence.md": "sanitized evidence",
                "reports/private/customer.md": "private report",
                "dist/release/old.zip": "old release",
                "dist/gateway/linux-amd64/gateway": "binary",
            }
            for rel, content in files.items():
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")

            included = {
                path.relative_to(root).as_posix()
                for path in package_release.list_package_files(root, kind="dev")
            }

            self.assertNotIn("AGENTS.md", included)
            self.assertIn("cmd/gateway/main.go", included)
            self.assertIn("internal/gateway/server.go", included)
            self.assertIn("fixtures/README.md", included)
            self.assertNotIn(".codex/config.toml", included)
            self.assertNotIn(".agents/skills/pr-review-algo/SKILL.md", included)
            self.assertNotIn("reports/private/evidence.md", included)
            self.assertIn("dist/gateway/linux-amd64/gateway", included)
            self.assertNotIn(".codex/local/state.json", included)
            self.assertNotIn("reports/private/customer.md", included)
            self.assertNotIn("dist/release/old.zip", included)

    def test_release_manifest_records_package_kind_and_backend(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "README.md").write_text("ok", encoding="utf-8")
            archive_path = root / "runtime.zip"

            package_release.create_release_archive(root, archive_path, kind="runtime")

            with ZipFile(archive_path, mode="r") as archive:
                manifest = json.loads(archive.read("release.json").decode("utf-8"))
            self.assertEqual(manifest["package_kind"], "runtime")
            self.assertEqual(manifest["default_backend"], "codex-web")
            self.assertIn(
                "standard OpenAI Responses API",
                manifest["deferred_backend_decision"],
            )

    def test_runtime_archive_inspection_rejects_source_and_reports(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            archive_path = Path(tmp) / "runtime.zip"
            with ZipFile(archive_path, mode="w", compression=ZIP_DEFLATED) as archive:
                archive.writestr("README.md", "ok")
                archive.writestr("cmd/gateway/main.go", "package main")
                archive.writestr("reports/private/evidence.md", "evidence")
                archive.writestr("release.json", "{}")
                add_executable_member(archive, "dist/gateway/linux-amd64/gateway")
                add_executable_member(archive, "dist/gateway/linux-arm64/gateway")
                add_executable_member(archive, "dist/gateway/windows-amd64/gateway.exe")

            _, violations = inspect_release_archive.inspect_archive(
                archive_path,
                kind="runtime",
            )

            self.assertTrue(any("cmd/gateway/main.go" in item for item in violations))
            self.assertTrue(
                any("reports/private/evidence.md" in item for item in violations)
            )

    def test_dev_archive_inspection_allows_source_but_rejects_internal_artifacts(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            archive_path = Path(tmp) / "dev.zip"
            with ZipFile(archive_path, mode="w", compression=ZIP_DEFLATED) as archive:
                archive.writestr("README.md", "ok")
                archive.writestr("cmd/gateway/main.go", "package main")
                archive.writestr(".codex/config.toml", "[gateway]")
                archive.writestr(".codex/local/state.json", "{}")
                archive.writestr(".agents/skills/pr-review-algo/SKILL.md", "skill")
                archive.writestr("release.json", "{}")
                add_executable_member(archive, "dist/gateway/linux-amd64/gateway")
                add_executable_member(archive, "dist/gateway/linux-arm64/gateway")
                add_executable_member(archive, "dist/gateway/windows-amd64/gateway.exe")

            _, violations = inspect_release_archive.inspect_archive(archive_path, kind="dev")

            self.assertFalse(any("cmd/gateway/main.go" in item for item in violations))
            self.assertTrue(any(".codex/config.toml" in item for item in violations))
            self.assertTrue(any(".codex/local/state.json" in item for item in violations))
            self.assertTrue(any(".agents/skills/pr-review-algo/SKILL.md" in item for item in violations))

    def test_ci_workflow_runs_release_archive_inspection(self) -> None:
        root = Path(__file__).resolve().parents[1]
        workflow = root / ".github" / "workflows" / "harness-validate.yml"
        text = workflow.read_text(encoding="utf-8")
        self.assertIn(
            "python3 scripts/inspect_release_archive.py --kind dev --archive dist/release/codex-gate-local.zip",
            text,
        )
        self.assertIn(
            "python3 scripts/inspect_release_archive.py --kind runtime --archive dist/release/codex-gate-runtime.zip",
            text,
        )
        self.assertIn(
            "python3 scripts/inspect_release_archive.py --kind dev --archive dist/release/codex-gate-dev.zip",
            text,
        )
        self.assertIn("python3 scripts/benchmark_smoke.py", text)

    def test_start_script_does_not_source_tmp_state(self) -> None:
        root = Path(__file__).resolve().parents[1]
        script = (root / "scripts" / "start_local_codex_gateway.sh").read_text(
            encoding="utf-8"
        )
        self.assertNotIn('. "$STATE_PATH"', script)
        self.assertIn("write_state_line()", script)
        self.assertIn('chmod 700 "$STATE_DIR"', script)
        self.assertIn("gateway failed to start; stdout follows:", script)

    def test_check_script_does_not_cat_raw_failure_body(self) -> None:
        root = Path(__file__).resolve().parents[1]
        script = (root / "scripts" / "check_local_codex_gateway.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("summarize_failure_body()", script)
        self.assertNotIn('cat "$tmp_body"', script)
        self.assertIn("CODEX_DEFAULT_ACCESS_TOKEN_COMMAND", script)

    def test_check_script_continues_without_local_token_helper(self) -> None:
        root = Path(__file__).resolve().parents[1]
        script = (root / "scripts" / "check_local_codex_gateway.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("token_helper=not_checked", script)
        self.assertIn("upstream_smoke can still validate the running gateway", script)
        self.assertNotIn(
            'if [ -z "${CODEX_ACCESS_TOKEN:-}${CODEX_WEB_ACCESS_TOKEN:-}" ]; then\n'
            "    exit 1\n"
            "  fi",
            script,
        )

    def test_start_script_uses_generated_state_config(self) -> None:
        root = Path(__file__).resolve().parents[1]
        script = (root / "scripts" / "start_local_codex_gateway.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('CONFIG_PATH="$STATE_DIR/gateway.toml"', script)
        self.assertIn('cat >"$CONFIG_PATH" <<EOF', script)
        self.assertIn('exec "$GATEWAY_BIN" -config "$CONFIG_PATH"', script)
        self.assertNotIn(".codex/config.toml", script)

    def test_start_script_prints_runtime_next_steps(self) -> None:
        root = Path(__file__).resolve().parents[1]
        script = (root / "scripts" / "start_local_codex_gateway.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('echo "health_url=$BASE_URL/healthz"', script)
        self.assertIn('echo "version_url=$BASE_URL/version"', script)
        self.assertIn(
            'echo "next_check_command=sh $ROOT/scripts/check_local_codex_gateway.sh --base-url $BASE_URL"',
            script,
        )
        self.assertIn('echo "agent_task_tools=allowed_by_default"', script)
        self.assertIn(
            'echo "agent_task_disable_command=claude --disallowedTools Agent --disallowedTools Task"',
            script,
        )

    def test_start_script_port_conflict_prints_procfs_fallback(self) -> None:
        root = Path(__file__).resolve().parents[1]
        script = (root / "scripts" / "start_local_codex_gateway.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("port_diagnostic=procfs", script)
        self.assertIn("listener_pid=", script)
        self.assertIn("listener_comm=", script)
        self.assertIn("next_status_command=sh scripts/start_local_codex_gateway.sh --status", script)
        self.assertIn("next_stop_command=sh scripts/start_local_codex_gateway.sh --stop", script)
        self.assertIn("next_port_command=sh scripts/start_local_codex_gateway.sh --port", script)
        self.assertNotIn("cmdline", script)

    def test_check_scripts_print_version_and_next_steps(self) -> None:
        root = Path(__file__).resolve().parents[1]
        local_check = (root / "scripts" / "check_local_codex_gateway.sh").read_text(
            encoding="utf-8"
        )
        direct_check = (root / "scripts" / "check_claude_code_direct.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('"$BASE_URL/version"', local_check)
        self.assertIn(
            'echo "next_command=sh scripts/run_claude_code_direct.sh --gateway-base-url $BASE_URL"',
            local_check,
        )
        self.assertIn(
            "auth_conflict=ANTHROPIC_API_KEY_and_ANTHROPIC_AUTH_TOKEN",
            direct_check,
        )
        self.assertIn('print("next_command=claude")', direct_check)

    def test_user_docs_do_not_use_fixed_workspace_paths(self) -> None:
        root = Path(__file__).resolve().parents[1]
        docs = [
            root / "README.md",
            root / "README.en.md",
            root / "USER_MANUAL.md",
        ]
        for path in docs:
            text = path.read_text(encoding="utf-8")
            self.assertNotIn("/absolute/private/workspace", text)


if __name__ == "__main__":
    unittest.main()
