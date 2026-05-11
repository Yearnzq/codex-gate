$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Push-Location $Root
try {
  $goCmd = (Get-Command go -ErrorAction SilentlyContinue)
  if ($null -eq $goCmd) {
    $fallback = $env:CODEX_GATE_GO
    if ($fallback -and (Test-Path $fallback)) {
      $go = $fallback
    } else {
      throw "Go toolchain not found. Install Go or set CODEX_GATE_GO to the go executable path."
    }
  } else {
    $go = "go"
  }

  $goos = if ($env:GOOS) { $env:GOOS } else { & $go env GOOS }
  $goarch = if ($env:GOARCH) { $env:GOARCH } else { & $go env GOARCH }

  $target = "$goos-$goarch"
  $outputDir = Join-Path "dist\gateway" $target
  New-Item -ItemType Directory -Path $outputDir -Force | Out-Null

  $binaryName = "gateway"
  if ($goos -eq "windows") {
    $binaryName = "gateway.exe"
  }
  $outputPath = Join-Path $outputDir $binaryName

  $version = if ($env:CODEX_GATEWAY_VERSION) { $env:CODEX_GATEWAY_VERSION } else { "0.6.0-phase6" }
  $buildTime = if ($env:CODEX_GATEWAY_BUILD_TIME) { $env:CODEX_GATEWAY_BUILD_TIME } else { (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ") }
  if ($env:CODEX_GATEWAY_BUILD_COMMIT) {
    $buildCommit = $env:CODEX_GATEWAY_BUILD_COMMIT
  } else {
    $gitCmd = Get-Command git -ErrorAction SilentlyContinue
    $buildCommit = if ($null -ne $gitCmd) { (& git rev-parse --short HEAD 2>$null) } else { $null }
    if (-not $buildCommit) { $buildCommit = "unknown" }
  }
  $ldflags = "-X codex-gate/internal/gateway.ServiceVersion=$version -X codex-gate/internal/gateway.BuildTime=$buildTime -X codex-gate/internal/gateway.BuildCommit=$buildCommit -X codex-gate/internal/gateway.BuildTarget=$target"

  & $go build -trimpath -ldflags $ldflags -o $outputPath ./cmd/gateway
  Write-Host "OK: built gateway binary at $outputPath"
} finally {
  Pop-Location
}
