#!/usr/bin/env python3
"""Shared runtime log redaction helpers."""

from __future__ import annotations

import re


SENSITIVE_HEADER_KEYS = {
    "authorization",
    "x-api-key",
    "api-key",
    "cookie",
    "set-cookie",
}

BEARER_PATTERN = re.compile(r"(?i)bearer\s+[^\s,;]+")
KEY_VALUE_PATTERN = re.compile(
    r"(?i)\b(api[_-]?key|token|password|secret)\b\s*([=:])\s*([^\s,;]+)"
)


def redact_headers(headers: dict[str, object]) -> dict[str, str]:
    redacted: dict[str, str] = {}
    for key, value in headers.items():
        key_str = str(key)
        value_str = str(value)
        if key_str.lower() in SENSITIVE_HEADER_KEYS:
            redacted[key_str] = "[REDACTED]"
        else:
            redacted[key_str] = value_str
    return redacted


def redact_log_line(log_line: str) -> str:
    line = BEARER_PATTERN.sub("Bearer [REDACTED]", log_line)

    def _replace(match: re.Match[str]) -> str:
        field = match.group(1)
        separator = match.group(2)
        return f"{field}{separator}[REDACTED]"

    return KEY_VALUE_PATTERN.sub(_replace, line)


def sanitize_log(headers: dict[str, object], log_line: str) -> str:
    redacted_headers = redact_headers(headers)
    rendered_headers = " ".join(
        f"{key}:{value}" for key, value in sorted(redacted_headers.items())
    )
    return f"{rendered_headers} {redact_log_line(log_line)}"
