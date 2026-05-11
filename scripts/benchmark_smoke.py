#!/usr/bin/env python3
"""Run benchmark smoke checks using synthetic/public fixtures only."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from make_report import render_report


def ensure(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(f"FAIL: {message}")


def load_json(path: Path) -> dict[str, object]:
    with path.open("r", encoding="utf-8") as handle:
        payload = json.load(handle)
    ensure(isinstance(payload, dict), f"{path} must contain a JSON object")
    return payload


def validate_config(config: dict[str, object], path: Path) -> None:
    metrics = config.get("metrics")
    ensure(isinstance(metrics, list) and len(metrics) > 0, f"{path} missing metrics list")
    gates = config.get("gates")
    ensure(isinstance(gates, dict), f"{path} missing gates object")


def validate_fixture(fixture: dict[str, object], fixture_path: Path, root: Path) -> None:
    resolved = fixture_path.resolve()
    fixtures_root = (root / "benchmarks" / "fixtures").resolve()
    try:
        resolved.relative_to(fixtures_root)
    except ValueError:
        ensure(False, f"{fixture_path} must be under benchmarks/fixtures/")

    ensure(fixture.get("synthetic") is True, f"{fixture_path} must set synthetic=true")
    gate = fixture.get("gate")
    ensure(gate in {"pass", "fail", "unknown"}, f"{fixture_path} has invalid gate value")

    metrics = fixture.get("metrics")
    ensure(isinstance(metrics, dict) and len(metrics) > 0, f"{fixture_path} missing metrics object")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, required=True, help="Benchmark config JSON")
    parser.add_argument("--fixture", type=Path, required=True, help="Synthetic benchmark fixture")
    parser.add_argument("--output", type=Path, required=True, help="Output Markdown report path")
    args = parser.parse_args()

    root = Path(__file__).resolve().parents[1]
    config = load_json(args.config)
    fixture = load_json(args.fixture)

    validate_config(config, args.config)
    validate_fixture(fixture, args.fixture, root)

    report = render_report(fixture)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(report, encoding="utf-8")

    metric_count = len(fixture.get("metrics", {}))
    print(
        "OK: benchmark smoke validated "
        f"(config={args.config}, fixture={args.fixture}, metrics={metric_count}, output={args.output})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
