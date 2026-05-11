$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Push-Location $Root
try {
  python scripts/validate.py
  python scripts/scan_secrets.py
  python scripts/test_phase2_review_fixes.py
  python scripts/test_phase9_review_fixes.py
  python scripts/test_token_command_installer.py
  python scripts/test_protocol_fixtures.py
  $goCmd = (Get-Command go -ErrorAction SilentlyContinue)
  if ($null -eq $goCmd) {
    $fallback = $env:CODEX_GATE_GO
    if ($fallback -and (Test-Path $fallback)) {
      & $fallback test ./...
    } else {
      throw "Go toolchain not found. Install Go or set CODEX_GATE_GO to the go executable path."
    }
  } else {
    & go test ./...
  }
  & powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test_gateway_clients.ps1
  & powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test_gateway_cli_backend.ps1
} finally {
  Pop-Location
}
