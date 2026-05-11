#!/usr/bin/env python3
"""Collect benchmark metrics from a JSON file and print a compact summary."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("input", type=Path, help="Benchmark JSON file")
    args = parser.parse_args()

    data = json.loads(args.input.read_text(encoding="utf-8"))
    metrics = data.get("metrics", {})
    print(json.dumps({"source": str(args.input), "metrics": metrics}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
