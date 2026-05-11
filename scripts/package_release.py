#!/usr/bin/env python3
"""Create a local release zip with explicit exclusion rules."""

from __future__ import annotations

import argparse
import fnmatch
import json
import time
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo


EXCLUDE_DIRS = {
    ".git",
    ".github",
    ".agents",
    ".codex",
    ".codex/local",
    "__pycache__",
    "node_modules",
    "dist/release",
    "docs",
    "pr_templates",
    "reports",
    "build",
    "tmp",
    ".idea",
    ".vscode",
}
DIST_ALLOWED_PREFIXES = ("dist/gateway/",)
RELEASE_ALLOWED_REPORTS: set[str] = set()

EXCLUDE_PATTERNS = [
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
]
SENSITIVE_NAME_PATTERNS = [
    ".env",
    ".env.*",
    "id_rsa",
    "id_ed25519",
]
LF_NORMALIZED_EXTENSIONS = {
    ".sh",
}
RELEASE_MANIFEST = "release.json"
REQUIRED_GATEWAY_BINARIES = [
    "dist/gateway/linux-amd64/gateway",
    "dist/gateway/linux-arm64/gateway",
    "dist/gateway/windows-amd64/gateway.exe",
]
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
RUNTIME_ALLOWED_PREFIXES = ("dist/gateway/",)


def normalize_kind(kind: str) -> str:
    normalized = kind.strip().lower()
    if normalized not in PACKAGE_KINDS:
        raise ValueError(f"package kind must be one of: {', '.join(sorted(PACKAGE_KINDS))}")
    return normalized


def should_include_runtime_file(root: Path, path: Path) -> bool:
    rel = path.relative_to(root).as_posix()
    if rel in RUNTIME_ALLOWED_FILES:
        return True
    return rel.startswith(RUNTIME_ALLOWED_PREFIXES)


def should_exclude(root: Path, path: Path, kind: str = DEFAULT_PACKAGE_KIND) -> bool:
    package_kind = normalize_kind(kind)
    if package_kind == "runtime":
        return not should_include_runtime_file(root, path) or should_exclude_common(root, path)
    return should_exclude_common(root, path)


def should_exclude_common(root: Path, path: Path) -> bool:
    rel = path.relative_to(root).as_posix()
    parts = rel.split("/")
    file_name = path.name

    if rel in RELEASE_ALLOWED_REPORTS:
        return False

    # Only gateway build outputs are allowed under dist/.
    if rel.startswith("dist/") and not rel.startswith(DIST_ALLOWED_PREFIXES):
        return True

    # Directory-based exclusions.
    for excluded in EXCLUDE_DIRS:
        if rel == excluded or rel.startswith(excluded + "/"):
            return True
        if excluded in parts:
            return True

    # Pattern-based exclusions for full path and filename.
    for pattern in EXCLUDE_PATTERNS:
        if fnmatch.fnmatch(rel, pattern) or fnmatch.fnmatch(file_name, pattern):
            return True

    # Secret-like filenames are denied even in nested directories.
    for part in parts:
        for pattern in SENSITIVE_NAME_PATTERNS:
            if fnmatch.fnmatch(part, pattern):
                return True
    return False


def list_package_files(root: Path, kind: str = DEFAULT_PACKAGE_KIND) -> list[Path]:
    package_kind = normalize_kind(kind)
    files: list[Path] = []
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        if should_exclude(root, path, package_kind):
            continue
        files.append(path)
    return files


def archive_payload(path: Path) -> bytes:
    data = path.read_bytes()
    if path.suffix in LF_NORMALIZED_EXTENSIONS:
        data = data.replace(b"\r\n", b"\n").replace(b"\r", b"\n")
    return data


def archive_info(root: Path, path: Path) -> ZipInfo:
    rel = path.relative_to(root).as_posix()
    info = ZipInfo(rel)
    info.create_system = 3
    # ZIP timestamps cannot represent dates before 1980.
    timestamp = max(315532800, path.stat().st_mtime)
    info.date_time = time.localtime(timestamp)[:6]
    mode = 0o644
    if path.suffix == ".sh" or (
        rel.startswith(DIST_ALLOWED_PREFIXES) and path.name in {"gateway", "gateway.exe"}
    ):
        mode = 0o755
    info.external_attr = mode << 16
    info.compress_type = ZIP_DEFLATED
    return info


def create_release_archive(root: Path, output: Path, kind: str = DEFAULT_PACKAGE_KIND) -> int:
    package_kind = normalize_kind(kind)
    included = 0
    files = list_package_files(root, package_kind)
    with ZipFile(output, mode="w", compression=ZIP_DEFLATED) as archive:
        for path in files:
            if path.relative_to(root).as_posix() == RELEASE_MANIFEST:
                continue
            archive.writestr(archive_info(root, path), archive_payload(path))
            included += 1
        manifest = {
            "name": f"codex-gate-{package_kind}",
            "package_kind": package_kind,
            "schema_version": 2,
            "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "required_gateway_binaries": REQUIRED_GATEWAY_BINARIES,
            "protocol_compatibility": "anthropic-messages-v1/openai-responses-v1",
            "default_backend": "codex-web",
            "deferred_backend_decision": (
                "standard OpenAI Responses API is not the default backend in this phase"
            ),
        }
        info = ZipInfo(RELEASE_MANIFEST)
        info.create_system = 3
        info.date_time = time.gmtime(max(315532800, time.time()))[:6]
        info.external_attr = 0o644 << 16
        info.compress_type = ZIP_DEFLATED
        archive.writestr(info, json.dumps(manifest, indent=2, sort_keys=True).encode("utf-8"))
        included += 1
    return included


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--output",
        type=Path,
        default=Path("dist/release/codex-gate-local.zip"),
        help="Release zip output path",
    )
    parser.add_argument(
        "--kind",
        choices=sorted(PACKAGE_KINDS),
        default=DEFAULT_PACKAGE_KIND,
        help="Release package kind",
    )
    args = parser.parse_args()

    root = Path(__file__).resolve().parents[1]
    output = args.output if args.output.is_absolute() else root / args.output
    output.parent.mkdir(parents=True, exist_ok=True)

    included = create_release_archive(root, output, args.kind)
    print(f"OK: {args.kind} release package created at {output} (files={included})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
