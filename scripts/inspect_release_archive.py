#!/usr/bin/env python3
"""Inspect a release zip and fail on forbidden artifact patterns."""

from __future__ import annotations

import argparse
import fnmatch
from pathlib import Path, PurePosixPath
from zipfile import ZipFile


FORBIDDEN_PREFIXES = (
    ".git/",
    ".github/",
    ".agents/",
    ".codex/",
    "docs/",
    "pr_templates/",
    "reports/",
    "__pycache__/",
    "node_modules/",
    "build/",
    "tmp/",
    ".idea/",
    ".vscode/",
)
DIST_ALLOWED_PREFIX = "dist/gateway/"
REQUIRED_GATEWAY_BINARIES = {
    "dist/gateway/linux-amd64/gateway",
    "dist/gateway/linux-arm64/gateway",
    "dist/gateway/windows-amd64/gateway.exe",
}
REQUIRED_MEMBERS = REQUIRED_GATEWAY_BINARIES | {"release.json"}
PACKAGE_KINDS = {"dev", "runtime"}
DEFAULT_PACKAGE_KIND = "dev"
RUNTIME_ALLOWED_FILES = {
    "LICENSE",
    "NOTICE",
    "README.md",
    "README.en.md",
    "USER_MANUAL.md",
    "release.json",
    "scripts/check_claude_code_direct.sh",
    "scripts/check_local_codex_gateway.sh",
    "scripts/install_codex_token_command.sh",
    "scripts/run_claude_code_direct.sh",
    "scripts/start_local_codex_gateway.sh",
    "scripts/write_claude_code_direct_settings.sh",
    "scripts/write_local_ccr_config.sh",
}
RUNTIME_ALLOWED_PREFIX = "dist/gateway/"
RELEASE_ALLOWED_REPORTS: set[str] = set()
FORBIDDEN_PATTERNS = (
    "AGENTS.md",
    ".mcp.json",
    ".env",
    ".env.*",
    "*.zip",
    "*.tar",
    "*.tar.gz",
    "*.tgz",
    "*.pem",
    "*.key",
    "*.p12",
    "*.pfx",
    "id_rsa",
    "id_ed25519",
    "*.pyc",
    "reports/**",
)


def normalize_member(name: str) -> str:
    normalized = PurePosixPath(name).as_posix()
    while normalized.startswith("./"):
        normalized = normalized[2:]
    return normalized


def normalize_kind(kind: str) -> str:
    normalized = kind.strip().lower()
    if normalized not in PACKAGE_KINDS:
        raise ValueError(f"package kind must be one of: {', '.join(sorted(PACKAGE_KINDS))}")
    return normalized


def classify_runtime_forbidden(member: str) -> str | None:
    common = classify_common_forbidden(member)
    if common is not None:
        return common
    if member in RUNTIME_ALLOWED_FILES:
        return None
    if member.startswith(RUNTIME_ALLOWED_PREFIX):
        return None
    return "runtime package may only contain runtime allowlist entries"


def classify_common_forbidden(member: str) -> str | None:
    member_name = PurePosixPath(member).name
    parts = member.split("/")

    if member in RELEASE_ALLOWED_REPORTS:
        return None

    if member.startswith("dist/") and not member.startswith(DIST_ALLOWED_PREFIX):
        return "dist output outside dist/gateway/"

    for prefix in FORBIDDEN_PREFIXES:
        if member.startswith(prefix):
            return f"forbidden prefix: {prefix}"

    for pattern in FORBIDDEN_PATTERNS:
        if fnmatch.fnmatch(member, pattern) or fnmatch.fnmatch(member_name, pattern):
            return f"forbidden pattern: {pattern}"

    for part in parts:
        if fnmatch.fnmatch(part, ".env") or fnmatch.fnmatch(part, ".env.*"):
            return "forbidden nested env filename"
        if part in {"id_rsa", "id_ed25519"}:
            return "forbidden SSH private key filename"

    return None


def classify_forbidden(member: str, kind: str = DEFAULT_PACKAGE_KIND) -> str | None:
    package_kind = normalize_kind(kind)
    if package_kind == "runtime":
        return classify_runtime_forbidden(member)
    return classify_common_forbidden(member)


def inspect_archive(archive_path: Path, kind: str = DEFAULT_PACKAGE_KIND) -> tuple[int, list[str]]:
    package_kind = normalize_kind(kind)
    violations: list[str] = []
    files = 0
    members_seen: set[str] = set()
    with ZipFile(archive_path, mode="r") as archive:
        for info in archive.infolist():
            if info.is_dir():
                continue
            files += 1
            member = normalize_member(info.filename)
            members_seen.add(member)
            reason = classify_forbidden(member, package_kind)
            if reason is not None:
                violations.append(f"{member} ({reason})")
                continue
            if member.endswith(".sh"):
                data = archive.read(info)
                if b"\r" in data:
                    violations.append(f"{member} (shell script must use LF line endings)")
                mode = (info.external_attr >> 16) & 0o777
                if mode and mode & 0o111 == 0:
                    violations.append(f"{member} (shell script should be executable in release zip)")
                if info.create_system != 3:
                    violations.append(f"{member} (shell script executable mode must use Unix zip metadata)")
            if member.startswith(DIST_ALLOWED_PREFIX) and PurePosixPath(member).name in {"gateway", "gateway.exe"}:
                mode = (info.external_attr >> 16) & 0o777
                if mode and mode & 0o111 == 0:
                    violations.append(f"{member} (gateway binary should be executable in release zip)")
                if info.create_system != 3:
                    violations.append(f"{member} (gateway binary executable mode must use Unix zip metadata)")
    for required in sorted(REQUIRED_MEMBERS - members_seen):
        reason = "required release manifest missing from release zip"
        if required in REQUIRED_GATEWAY_BINARIES:
            reason = "required gateway binary missing from release zip"
        violations.append(f"{required} ({reason})")
    return files, violations


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--archive", type=Path, required=True, help="Release archive path")
    parser.add_argument(
        "--kind",
        choices=sorted(PACKAGE_KINDS),
        default=DEFAULT_PACKAGE_KIND,
        help="Release package kind",
    )
    args = parser.parse_args()

    archive = args.archive
    if not archive.exists():
        raise SystemExit(f"FAIL: archive not found: {archive}")

    file_count, violations = inspect_archive(archive, args.kind)
    if violations:
        lines = "\n".join(f"- {item}" for item in violations[:20])
        remaining = len(violations) - 20
        if remaining > 0:
            lines = f"{lines}\n- ... and {remaining} more"
        raise SystemExit(
            "FAIL: release archive contains forbidden entries:\n"
            f"{lines}"
        )

    print(
        f"OK: {args.kind} release archive passed policy checks "
        f"(files={file_count}, archive={archive})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
