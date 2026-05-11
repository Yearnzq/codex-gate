$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Push-Location $Root
try {
  $goCmd = (Get-Command go -ErrorAction SilentlyContinue)
  if ($null -eq $goCmd) {
    $fallback = $env:CODEX_GATE_GO
    if ($fallback -and (Test-Path $fallback)) {
      & $fallback run ./cmd/gateway
    } else {
      throw "Go toolchain not found. Install Go or set CODEX_GATE_GO to the go executable path."
    }
  } else {
    & go run ./cmd/gateway
  }
} finally {
  Pop-Location
}
