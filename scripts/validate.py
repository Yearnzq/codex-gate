#!/usr/bin/env python3
"""Validate the public Codex Gate repository layout."""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path

try:
    import tomllib as _toml_parser
except ModuleNotFoundError:  # Python 3.10 compatibility.
    try:
        import tomli as _toml_parser
    except ModuleNotFoundError:
        _toml_parser = None


ROOT = Path(__file__).resolve().parents[1]

REQUIRED_PATHS = [
    "LICENSE",
    "NOTICE",
    "README.md",
    "README.en.md",
    "USER_MANUAL.md",
    "go.mod",
    "cmd/gateway/main.go",
    "internal/codexclient/client.go",
    "internal/codexclient/errors.go",
    "internal/gateway/server.go",
    "internal/credentials/credentials.go",
    "internal/redaction/redaction.go",
    "evals/quality-gates/gates.json",
    "benchmarks/fixtures/smoke-benchmark.json",
    "scripts/benchmark_smoke.py",
    "scripts/build_gateway.ps1",
    "scripts/build_gateway.sh",
    "scripts/doctor.py",
    "scripts/package_release.py",
    "scripts/inspect_release_archive.py",
    ".gitattributes",
]

PROHIBITED_PUBLIC_PATHS = [
    ".agents",
    ".codex",
    ".mcp.json",
    ".openclaw",
    "AGENTS.md",
    "docs",
    "pr_templates",
    "reports",
]

SECRET_PATTERNS = [
    re.compile(r"sk-[A-Za-z0-9_-]{20,}"),
    re.compile(r"sk-ant-[A-Za-z0-9_-]{20,}"),
    re.compile(r"ghp_[A-Za-z0-9_]{20,}"),
    re.compile(r"github_pat_[A-Za-z0-9_]{20,}"),
    re.compile(r"(?i)aws_secret_access_key\s*[:=]\s*\S+"),
    re.compile(r"-----BEGIN (RSA |OPENSSH |EC |DSA )?PRIVATE KEY-----"),
]

SCAN_EXTENSIONS = {
    ".env",
    ".md",
    ".json",
    ".toml",
    ".py",
    ".go",
    ".ps1",
    ".sh",
    ".yml",
    ".yaml",
}

REQUIRED_README_ASSERTIONS = [
    "Active backend: Codex only.",
    "Disabled: OpenClaw, Hermes, Claude backend fallback",
]


def rel(path: Path, root: Path | None = None) -> str:
    base = ROOT if root is None else root
    return path.relative_to(base).as_posix()


def fail(message: str) -> None:
    print(f"FAIL: {message}")
    raise SystemExit(1)


def read_json(path: Path) -> object:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception as exc:  # noqa: BLE001
        fail(f"{rel(path)} is not valid JSON: {exc}")


def validate_required_paths(root: Path | None = None) -> None:
    base = ROOT if root is None else root
    for item in REQUIRED_PATHS:
        if not (base / item).exists():
            fail(f"missing required path: {item}")


def validate_prohibited_paths(root: Path | None = None) -> None:
    base = ROOT if root is None else root
    for item in PROHIBITED_PUBLIC_PATHS:
        if (base / item).exists():
            fail(f"prohibited public path exists: {item}")


def validate_structured_files(root: Path | None = None) -> None:
    base = ROOT if root is None else root
    for path in base.rglob("*.json"):
        parts = set(path.relative_to(base).parts)
        if ".git" in parts or "dist" in parts:
            continue
        read_json(path)
    for path in base.rglob("*.toml"):
        parts = set(path.relative_to(base).parts)
        if ".git" in parts or "dist" in parts:
            continue
        if _toml_parser is None:
            continue
        try:
            _toml_parser.loads(path.read_text(encoding="utf-8"))
        except Exception as exc:  # noqa: BLE001
            fail(f"{rel(path)} is not valid TOML: {exc}")


def validate_readme_boundary(root: Path | None = None) -> None:
    base = ROOT if root is None else root
    text = (base / "README.md").read_text(encoding="utf-8")
    for assertion in REQUIRED_README_ASSERTIONS:
        if assertion not in text:
            fail(f"README.md missing required boundary assertion: {assertion}")


def validate_no_public_internal_references(root: Path | None = None) -> None:
    base = ROOT if root is None else root
    public_docs = [
        base / "README.md",
        base / "README.en.md",
        base / "USER_MANUAL.md",
    ]
    forbidden_terms = [
        "private-container-name",
        "private-repository-name",
        "private-report-path",
        "docs/superpowers",
        ".codex/prompts",
        ".agents/skills",
        "C:\\Users\\",
        "D:\\ENV\\",
        "/absolute/private/workspace/",
    ]
    for path in public_docs:
        text = path.read_text(encoding="utf-8")
        for term in forbidden_terms:
            if term in text:
                fail(f"{rel(path, base)} contains internal reference: {term}")


def validate_shell_script_line_endings(root: Path | None = None) -> None:
    base = ROOT if root is None else root
    ignored_dirs = {".git", "node_modules", "__pycache__", "dist", "build"}
    offenders: list[str] = []
    for path in base.rglob("*.sh"):
        if not path.is_file():
            continue
        if any(part in ignored_dirs for part in path.relative_to(base).parts):
            continue
        if b"\r" in path.read_bytes():
            offenders.append(rel(path, base))
    if offenders:
        fail("shell scripts must use LF line endings: " + ", ".join(sorted(offenders)))


def should_scan_file(path: Path) -> bool:
    if path.suffix.lower() in SCAN_EXTENSIONS:
        return True
    name = path.name.lower()
    return name == ".env" or name.startswith(".env.")


def scan_for_secrets(root: Path | None = None) -> None:
    base = ROOT if root is None else root
    ignored_dirs = {".git", "node_modules", "__pycache__", "dist", "build"}
    findings: list[str] = []
    for path in base.rglob("*"):
        if not path.is_file():
            continue
        if any(part in ignored_dirs for part in path.relative_to(base).parts):
            continue
        if not should_scan_file(path):
            continue
        text = path.read_text(encoding="utf-8", errors="ignore")
        for pattern in SECRET_PATTERNS:
            if pattern.search(text):
                findings.append(rel(path, base))
                break
    if findings:
        fail("possible secrets found in: " + ", ".join(sorted(set(findings))))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--skip-secret-scan", action="store_true")
    args = parser.parse_args()

    validate_required_paths()
    validate_prohibited_paths()
    validate_structured_files()
    validate_readme_boundary()
    validate_no_public_internal_references()
    validate_shell_script_line_endings()
    if not args.skip_secret_scan:
        scan_for_secrets()

    print("OK: public repository layout validated")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
