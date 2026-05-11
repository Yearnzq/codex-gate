#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
python3 scripts/validate.py
python3 scripts/benchmark_smoke.py \
  --config benchmarks/configs/default-benchmark.json \
  --fixture benchmarks/fixtures/smoke-benchmark.json \
  --output dist/benchmark-smoke/smoke-report.md
python3 scripts/collect_metrics.py benchmarks/fixtures/smoke-benchmark.json
