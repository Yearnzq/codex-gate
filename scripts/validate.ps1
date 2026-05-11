$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Push-Location $Root
try {
  python scripts/validate.py
} finally {
  Pop-Location
}
