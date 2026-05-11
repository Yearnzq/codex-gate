#!/usr/bin/env python3
"""Local diagnostics for the Codex Claude Code gateway release."""

from __future__ import annotations

import argparse
import json
import os
import platform
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_BASE_URL = "http://127.0.0.1:18080"
IGNORED_DIRS = {".git", "node_modules", "__pycache__", "dist", "build"}


@dataclass
class CheckResult:
    name: str
    status: str
    message: str
    details: dict[str, Any] = field(default_factory=dict)


def detect_target() -> str:
    system = platform.system().lower()
    machine = platform.machine().lower()
    if system.startswith("linux"):
        os_name = "linux"
    elif system.startswith("windows") or system.startswith("mingw") or system.startswith("msys"):
        os_name = "windows"
    elif system.startswith("darwin"):
        os_name = "darwin"
    else:
        os_name = system

    if machine in {"x86_64", "amd64"}:
        arch = "amd64"
    elif machine in {"aarch64", "arm64"}:
        arch = "arm64"
    else:
        arch = machine
    return f"{os_name}-{arch}"


def binary_name(target: str) -> str:
    return "gateway.exe" if target.startswith("windows-") else "gateway"


def target_binary_path(root: Path, target: str) -> Path:
    return root / "dist" / "gateway" / target / binary_name(target)


def read_binary_target(path: Path) -> str | None:
    data = path.read_bytes()[:128]
    if len(data) >= 20 and data.startswith(b"\x7fELF"):
        endian = "little" if data[5] == 1 else "big"
        machine = int.from_bytes(data[18:20], endian)
        if machine == 0x3E:
            return "linux-amd64"
        if machine == 0xB7:
            return "linux-arm64"
        return f"linux-unknown-0x{machine:x}"

    if len(data) >= 64 and data.startswith(b"MZ"):
        pe_offset = int.from_bytes(data[0x3C:0x40], "little")
        header = path.read_bytes()[pe_offset : pe_offset + 6]
        if len(header) >= 6 and header.startswith(b"PE\0\0"):
            machine = int.from_bytes(header[4:6], "little")
            if machine == 0x8664:
                return "windows-amd64"
            if machine == 0xAA64:
                return "windows-arm64"
            return f"windows-unknown-0x{machine:x}"
    return None


def newest_go_source_mtime(root: Path) -> float:
    newest = 0.0
    for rel in ("go.mod", "go.sum"):
        path = root / rel
        if path.exists():
            newest = max(newest, path.stat().st_mtime)
    for folder in ("cmd", "internal"):
        base = root / folder
        if not base.exists():
            continue
        for path in base.rglob("*.go"):
            newest = max(newest, path.stat().st_mtime)
    return newest


def check_shell_lf(root: Path) -> CheckResult:
    offenders: list[str] = []
    for path in root.rglob("*.sh"):
        if not path.is_file():
            continue
        if any(part in IGNORED_DIRS for part in path.relative_to(root).parts):
            continue
        if b"\r" in path.read_bytes():
            offenders.append(path.relative_to(root).as_posix())
    if offenders:
        return CheckResult(
            "shell_lf",
            "FAIL",
            "shell scripts must use LF line endings",
            {"files": offenders[:20], "remaining": max(0, len(offenders) - 20)},
        )
    return CheckResult("shell_lf", "OK", "all shell scripts use LF")


def check_gateway_binary(root: Path, target: str) -> CheckResult:
    path = target_binary_path(root, target)
    if not path.exists():
        return CheckResult(
            "gateway_binary",
            "FAIL",
            "gateway binary is missing for current target",
            {"target": target, "path": str(path)},
        )
    detected = read_binary_target(path)
    if detected is not None and detected != target:
        return CheckResult(
            "gateway_binary",
            "FAIL",
            "gateway binary architecture does not match current target",
            {"target": target, "detected": detected, "path": str(path)},
        )
    if not target.startswith("windows-") and not os.access(path, os.X_OK):
        return CheckResult(
            "gateway_binary",
            "WARN",
            "gateway binary is not executable; start script will chmod its state copy",
            {"target": target, "path": str(path)},
        )
    source_mtime = newest_go_source_mtime(root)
    if source_mtime and path.stat().st_mtime < source_mtime:
        return CheckResult(
            "gateway_binary",
            "FAIL",
            "gateway binary is older than Go source",
            {
                "target": target,
                "path": str(path),
                "binary_mtime": int(path.stat().st_mtime),
                "source_mtime": int(source_mtime),
            },
        )
    return CheckResult(
        "gateway_binary",
        "OK",
        "gateway binary exists and matches target",
        {"target": target, "path": str(path), "detected": detected or "unknown"},
    )


def helper_command(env: dict[str, str]) -> str | None:
    for key in ("CODEX_ACCESS_TOKEN_COMMAND", "CODEX_DEFAULT_ACCESS_TOKEN_COMMAND"):
        explicit = env.get(key, "").strip()
        if explicit:
            return explicit
    home = env.get("HOME", "").strip()
    if not home:
        return None
    default = Path(home) / ".local" / "bin" / "codex-access-token"
    if default.exists():
        return str(default)
    return None


def first_nonempty_line(text: str) -> str:
    for line in text.replace("\r", "\n").split("\n"):
        stripped = line.strip()
        if stripped:
            return stripped
    return ""


def check_token_helper(env: dict[str, str], run_helper: bool) -> CheckResult:
    for key in ("CODEX_WEB_ACCESS_TOKEN", "CODEX_ACCESS_TOKEN"):
        value = env.get(key, "")
        if value:
            return CheckResult(
                "token_helper",
                "OK" if len(value) >= 20 else "WARN",
                f"{key} is set; token content was not printed",
                {"source": key, "length_ok": len(value) >= 20},
            )

    command = helper_command(env)
    if command is None:
        return CheckResult(
            "token_helper",
            "WARN",
            "no Codex token environment variable or token helper was found",
        )
    if not run_helper:
        return CheckResult(
            "token_helper",
            "OK",
            "token helper is configured; helper execution skipped and command content was not printed",
            {"configured": True},
        )

    try:
        run_args: str | list[str]
        if os.path.exists(command):
            run_args = [command]
        else:
            run_args = ["sh", "-c", command]
        completed = subprocess.run(
            run_args,
            shell=False,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=5,
        )
    except Exception as exc:  # noqa: BLE001
        return CheckResult(
            "token_helper",
            "FAIL",
            "token helper failed before returning a token",
            {"error_type": type(exc).__name__},
        )
    if completed.returncode != 0:
        return CheckResult(
            "token_helper",
            "FAIL",
            "token helper exited non-zero",
            {"returncode": completed.returncode},
        )
    token = first_nonempty_line(completed.stdout)
    return CheckResult(
        "token_helper",
        "OK" if len(token) >= 20 else "WARN",
        "token helper returned a non-empty value; token content was not printed",
        {"length_ok": len(token) >= 20},
    )


def check_auth_conflict(env: dict[str, str]) -> CheckResult:
    has_key = bool(env.get("ANTHROPIC_API_KEY"))
    has_token = bool(env.get("ANTHROPIC_AUTH_TOKEN"))
    if has_key and has_token:
        return CheckResult(
            "auth_conflict",
            "FAIL",
            "ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN are both set in the shell",
        )
    if has_key:
        return CheckResult(
            "auth_conflict",
            "WARN",
            "ANTHROPIC_API_KEY is set in the shell and can override token-only settings",
        )
    return CheckResult("auth_conflict", "OK", "no shell-level Anthropic auth conflict detected")


def load_settings(path: Path) -> dict[str, Any] | None:
    if not path.exists():
        return None
    data = json.loads(path.read_text(encoding="utf-8-sig"))
    if not isinstance(data, dict):
        raise ValueError("settings root must be a JSON object")
    return data


def check_claude_settings(settings_path: Path, base_url: str) -> CheckResult:
    try:
        data = load_settings(settings_path)
    except Exception as exc:  # noqa: BLE001
        return CheckResult(
            "claude_settings",
            "FAIL",
            "Claude Code settings could not be parsed",
            {"path": str(settings_path), "error_type": type(exc).__name__},
        )
    if data is None:
        return CheckResult(
            "claude_settings",
            "WARN",
            "Claude Code settings file was not found",
            {"path": str(settings_path)},
        )
    env = data.get("env")
    if not isinstance(env, dict):
        return CheckResult(
            "claude_settings",
            "FAIL",
            "Claude Code settings env must be a JSON object",
            {"path": str(settings_path)},
        )
    observed_url = str(env.get("ANTHROPIC_BASE_URL", "")).rstrip("/")
    expected_url = base_url.rstrip("/")
    issues: list[str] = []
    if observed_url != expected_url:
        issues.append("ANTHROPIC_BASE_URL does not point to gateway")
    if "ANTHROPIC_AUTH_TOKEN" not in env:
        issues.append("ANTHROPIC_AUTH_TOKEN is not configured")
    if "ANTHROPIC_API_KEY" in env:
        issues.append("ANTHROPIC_API_KEY is still present in settings env")
    if issues:
        return CheckResult(
            "claude_settings",
            "FAIL",
            "; ".join(issues),
            {"path": str(settings_path), "expected_base_url": expected_url, "observed_base_url": observed_url},
        )
    permissions = data.get("permissions")
    agent_task_tools = "allowed"
    if isinstance(permissions, dict):
        deny = permissions.get("deny")
        if isinstance(deny, list) and any(str(item) in {"Agent", "Task"} for item in deny):
            agent_task_tools = "denied"
    return CheckResult(
        "claude_settings",
        "OK",
        "Claude Code settings point to the local gateway with token auth",
        {
            "path": str(settings_path),
            "base_url": expected_url,
            "agent_task_tools": agent_task_tools,
        },
    )


def http_json(url: str, timeout: float, payload: dict[str, Any] | None = None) -> tuple[int, dict[str, Any]]:
    data = None
    headers = {"Accept": "application/json"}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers, method="POST" if payload else "GET")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            body = response.read().decode("utf-8")
            decoded = json.loads(body) if body else {}
            return response.status, decoded if isinstance(decoded, dict) else {"value": decoded}
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        try:
            decoded = json.loads(body) if body else {}
        except json.JSONDecodeError:
            decoded = {"raw": body[:200]}
        return exc.code, decoded if isinstance(decoded, dict) else {"value": decoded}


def check_health(base_url: str, timeout: float) -> CheckResult:
    try:
        status, payload = http_json(base_url.rstrip("/") + "/healthz", timeout)
    except Exception as exc:  # noqa: BLE001
        return CheckResult("healthz", "FAIL", "gateway /healthz request failed", {"error_type": type(exc).__name__})
    if status == 200 and payload.get("status") == "ok":
        return CheckResult("healthz", "OK", "gateway /healthz returned ok")
    return CheckResult("healthz", "FAIL", "gateway /healthz did not return ok", {"status": status})


def check_version(base_url: str, timeout: float) -> CheckResult:
    try:
        status, payload = http_json(base_url.rstrip("/") + "/version", timeout)
    except Exception as exc:  # noqa: BLE001
        return CheckResult("version", "FAIL", "gateway /version request failed", {"error_type": type(exc).__name__})
    if status != 200:
        return CheckResult("version", "FAIL", "gateway /version did not return HTTP 200", {"status": status})
    required = {"name", "version", "active_backend"}
    missing = sorted(key for key in required if key not in payload)
    if missing:
        return CheckResult("version", "FAIL", "gateway /version is missing required fields", {"missing": missing})
    recommended = {"build_time", "target_platform", "backend_mode", "protocol_compatibility"}
    missing_recommended = sorted(key for key in recommended if key not in payload)
    if missing_recommended:
        return CheckResult(
            "version",
            "WARN",
            "gateway /version works but lacks release diagnostics fields",
            {"missing_recommended": missing_recommended},
        )
    return CheckResult("version", "OK", "gateway /version returned release diagnostics")


def check_message_smoke(base_url: str, timeout: float, model: str) -> CheckResult:
    payload = {
        "model": model,
        "max_tokens": 16,
        "stream": False,
        "messages": [{"role": "user", "content": "Reply with exactly: ok"}],
    }
    try:
        status, response = http_json(base_url.rstrip("/") + "/v1/messages", timeout, payload)
    except Exception as exc:  # noqa: BLE001
        return CheckResult("messages_smoke", "FAIL", "minimal /v1/messages request failed", {"error_type": type(exc).__name__})
    if status == 200:
        return CheckResult("messages_smoke", "OK", "minimal /v1/messages request succeeded")
    error = response.get("error") if isinstance(response.get("error"), dict) else {}
    return CheckResult(
        "messages_smoke",
        "FAIL",
        "minimal /v1/messages request returned an error",
        {"status": status, "error_type": error.get("type"), "code": error.get("code")},
    )


def default_gateway_log_path(env: dict[str, str]) -> Path:
    state_dir = env.get("CODEX_GATEWAY_STATE_DIR", "").strip()
    if not state_dir:
        state_dir = str(Path(env.get("TMPDIR", tempfile.gettempdir())) / "codex-gate-local-codex-gateway")
    return Path(state_dir) / "gateway.out.log"


def check_stream_diagnostics(log_path: Path) -> CheckResult:
    if not log_path.exists():
        return CheckResult(
            "stream_diagnostics",
            "WARN",
            "gateway log was not found; stream-read categories could not be inspected",
            {"path": str(log_path)},
        )
    latest: dict[str, Any] | None = None
    for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(event, dict) and event.get("event") == "codex_web_stream_read_error":
            latest = event
    if latest is None:
        return CheckResult(
            "stream_diagnostics",
            "OK",
            "no codex web stream-read errors found in gateway log",
            {"path": str(log_path)},
        )
    return CheckResult(
        "stream_diagnostics",
        "WARN",
        "latest codex web stream-read error found",
        {
            "path": str(log_path),
            "category": latest.get("category"),
            "request_id": latest.get("request_id"),
            "events_seen": latest.get("events_seen"),
            "response_id_set": latest.get("response_id_set"),
            "action": "check upstream connectivity and avoid retrying after tool-use events",
        },
    )


def next_action_result(base_url: str) -> CheckResult:
    clean_base_url = base_url.rstrip("/")
    return CheckResult(
        "next_actions",
        "OK",
        "run the gateway check, then start Claude Code through the direct wrapper",
        {
            "check_command": f"sh scripts/check_local_codex_gateway.sh --base-url {clean_base_url}",
            "claude_wrapper_command": (
                f"sh scripts/run_claude_code_direct.sh --gateway-base-url {clean_base_url}"
            ),
        },
    )


def render_text(results: list[CheckResult]) -> str:
    lines: list[str] = []
    for result in results:
        lines.append(f"{result.status}: {result.name}: {result.message}")
        for key, value in sorted(result.details.items()):
            lines.append(f"  {key}={value}")
    return "\n".join(lines)


def exit_code(results: list[CheckResult], strict: bool) -> int:
    statuses = {result.status for result in results}
    if "FAIL" in statuses:
        return 1
    if strict and "WARN" in statuses:
        return 1
    return 0


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Diagnose a local Codex Claude Code gateway release.")
    parser.add_argument("--root", type=Path, default=ROOT, help="release root, default: repository root")
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL, help="gateway base URL")
    parser.add_argument("--target", default=detect_target(), help="target platform, e.g. linux-amd64")
    parser.add_argument("--settings", type=Path, default=None, help="Claude Code settings path")
    parser.add_argument("--model", default="gpt-5.5", help="model used for /v1/messages smoke")
    parser.add_argument("--timeout", type=float, default=3.0, help="HTTP timeout seconds")
    parser.add_argument("--run-token-helper", action="store_true", help="execute token helper and validate non-empty output")
    parser.add_argument("--skip-http", action="store_true", help="skip /healthz and /version checks")
    parser.add_argument("--skip-message-smoke", action="store_true", help="skip minimal /v1/messages request")
    parser.add_argument("--gateway-log", type=Path, default=None, help="gateway stdout log for stream diagnostics")
    parser.add_argument("--strict", action="store_true", help="treat warnings as failures")
    parser.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    root = args.root.resolve()
    settings = args.settings
    if settings is None:
        home = Path(os.environ.get("HOME", str(Path.home())))
        settings = home / ".claude" / "settings.json"

    results = [
        check_shell_lf(root),
        check_gateway_binary(root, args.target),
        check_token_helper(dict(os.environ), args.run_token_helper),
        check_auth_conflict(dict(os.environ)),
        check_claude_settings(settings.expanduser(), args.base_url),
    ]
    gateway_log = args.gateway_log if args.gateway_log is not None else default_gateway_log_path(dict(os.environ))
    results.append(check_stream_diagnostics(gateway_log.expanduser()))
    results.append(next_action_result(args.base_url))
    if not args.skip_http:
        results.append(check_health(args.base_url, args.timeout))
        results.append(check_version(args.base_url, args.timeout))
    if not args.skip_http and not args.skip_message_smoke:
        results.append(check_message_smoke(args.base_url, args.timeout, args.model))

    if args.json:
        print(json.dumps([result.__dict__ for result in results], indent=2, sort_keys=True))
    else:
        print(render_text(results))
    return exit_code(results, args.strict)


if __name__ == "__main__":
    raise SystemExit(main())
