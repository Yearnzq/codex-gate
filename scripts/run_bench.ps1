$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Push-Location $Root
try {
  python scripts/validate.py
  python scripts/benchmark_smoke.py `
    --config benchmarks/configs/default-benchmark.json `
    --fixture benchmarks/fixtures/smoke-benchmark.json `
    --output dist/benchmark-smoke/smoke-report.md
  python scripts/collect_metrics.py benchmarks/fixtures/smoke-benchmark.json
} finally {
  Pop-Location
}
