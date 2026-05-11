#!/usr/bin/env python3
"""Create a Markdown benchmark report from a benchmark JSON artifact."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def render_report(data: dict) -> str:
    title = data.get("title", "Benchmark Report")
    metrics = data.get("metrics", {})
    gate = data.get("gate", "unknown")
    lines = [
        f"# {title}",
        "",
        f"Gate: `{gate}`",
        "",
        "## Metrics",
        "",
        "| Metric | Value |",
        "| --- | --- |",
    ]
    for key, value in metrics.items():
        lines.append(f"| `{key}` | `{value}` |")
    lines.extend([
        "",
        "## Environment",
        "",
        data.get("environment", "Not provided."),
        "",
        "## Notes",
        "",
        data.get("notes", "No notes provided."),
        "",
    ])
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("input", nargs="?", type=Path, help="Benchmark JSON input")
    parser.add_argument("--output", type=Path, help="Markdown output path")
    args = parser.parse_args()

    if args.input is None:
        parser.print_help()
        return 0

    data = json.loads(args.input.read_text(encoding="utf-8"))
    report = render_report(data)
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(report, encoding="utf-8")
    else:
        print(report)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
