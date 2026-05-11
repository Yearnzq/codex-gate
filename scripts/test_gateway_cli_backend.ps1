param(
  [string]$ReportPath = ""
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot

function Resolve-Go {
  $goCmd = Get-Command go -ErrorAction SilentlyContinue
  if ($null -ne $goCmd) {
    return "go"
  }
  $fallback = $env:CODEX_GATE_GO
  if ($fallback -and (Test-Path $fallback)) {
    return $fallback
  }
  throw "Go toolchain not found. Install Go or set CODEX_GATE_GO to the go executable path."
}

function Get-FreeTcpPort {
  $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
  $listener.Start()
  try {
    return $listener.LocalEndpoint.Port
  } finally {
    $listener.Stop()
  }
}

function Wait-TcpPort([int]$Port, [int]$TimeoutMs = 10000) {
  $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMs)
  while ([DateTime]::UtcNow -lt $deadline) {
    try {
      $client = [System.Net.Sockets.TcpClient]::new()
      $attempt = $client.BeginConnect("127.0.0.1", $Port, $null, $null)
      if ($attempt.AsyncWaitHandle.WaitOne(250)) {
        $client.EndConnect($attempt)
        $client.Close()
        return $true
      }
      $client.Close()
    } catch {
      Start-Sleep -Milliseconds 150
    }
  }
  return $false
}

function Write-Status([string]$Message) {
  Write-Output $Message
  if ($script:ReportPathValue.Trim() -ne "") {
    Add-Content -Path $script:ReportPathValue -Value $Message -Encoding ASCII
  }
}

function Restore-EnvVar([string]$Name, [string]$Value) {
  if ($null -eq $Value) {
    Remove-Item -Path "Env:\$Name" -ErrorAction SilentlyContinue
    return
  }
  Set-Item -Path "Env:\$Name" -Value $Value
}

Push-Location $Root
try {
  $script:ReportPathValue = $ReportPath
  if ($script:ReportPathValue.Trim() -ne "") {
    $reportParent = Split-Path -Parent $script:ReportPathValue
    if ($reportParent -and -not (Test-Path $reportParent)) {
      New-Item -ItemType Directory -Path $reportParent -Force | Out-Null
    }
    Set-Content -Path $script:ReportPathValue -Value $null -Encoding ASCII
  }

  Write-Status "BEGIN: gateway cli backend e2e"
  $go = Resolve-Go
  $python = (Get-Command python -ErrorAction Stop).Source
  $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("codex-gate-gateway-cli-" + [Guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Path $tmp | Out-Null

  $gatewayPort = Get-FreeTcpPort
  $gatewayConfig = Join-Path $tmp "gateway.toml"
  $gatewayBin = Join-Path $tmp "gateway.exe"
  $fakeCLI = Join-Path $tmp "fake_codex_cli.py"
  $gatewayOut = Join-Path $tmp "gateway.out.log"
  $gatewayErr = Join-Path $tmp "gateway.err.log"
  $gatewayProc = $null

  try {
@'
import sys

prompt = sys.argv[1] if len(sys.argv) > 1 else sys.stdin.read()
if "client-ok" not in prompt:
    print("unexpected-prompt")
    sys.exit(2)
print("client-ok")
'@ | Set-Content -Path $fakeCLI -Encoding UTF8

@"
[gateway]
host = "127.0.0.1"
port = $gatewayPort
log_level = "info"
redact_logs = true
allow_wide_bind = false
"@ | Set-Content -Path $gatewayConfig -Encoding ASCII

    & $go build -trimpath -o $gatewayBin ./cmd/gateway

    $oldBackend = $env:CODEX_BACKEND
    $oldCommand = $env:CODEX_CLI_COMMAND
    $oldArgs = $env:CODEX_CLI_ARGS_JSON
    $oldTimeout = $env:CODEX_CLI_TIMEOUT
    try {
      $env:CODEX_BACKEND = "cli"
      $env:CODEX_CLI_COMMAND = $python
      $env:CODEX_CLI_ARGS_JSON = ConvertTo-Json @($fakeCLI, "{{prompt}}") -Compress
      $env:CODEX_CLI_TIMEOUT = "15s"
      $gatewayProc = Start-Process -FilePath $gatewayBin -ArgumentList @("-config", $gatewayConfig) -PassThru -RedirectStandardOutput $gatewayOut -RedirectStandardError $gatewayErr -WindowStyle Hidden
    } finally {
      Restore-EnvVar "CODEX_BACKEND" $oldBackend
      Restore-EnvVar "CODEX_CLI_COMMAND" $oldCommand
      Restore-EnvVar "CODEX_CLI_ARGS_JSON" $oldArgs
      Restore-EnvVar "CODEX_CLI_TIMEOUT" $oldTimeout
    }
    if (-not (Wait-TcpPort -Port $gatewayPort -TimeoutMs 10000)) {
      throw "gateway did not start on port $gatewayPort"
    }

    $headers = @{"x-api-key" = "test-key-not-secret"}
    $chatBody = @{
      model = "claude-sonnet-4-5"
      messages = @(@{role = "user"; content = "Reply with exactly: client-ok"})
      max_completion_tokens = 64
      tools = @(
        @{
          type = "function"
          function = @{
            name = "local_tool"
            parameters = @{
              type = "object"
              properties = @{
                status = @{
                  anyOf = @(
                    @{type = "string"; enum = @("pending", "completed")},
                    @{const = "completed"}
                  )
                }
                query = @{
                  type = @("string", "null")
                }
                items = @{
                  type = "array"
                  prefixItems = @(
                    @{type = "string"},
                    @{type = "integer"}
                  )
                  items = @{
                    oneOf = @(
                      @{type = "string"},
                      @{type = "number"}
                    )
                  }
                  contains = @{type = "string"; const = "needle"}
                  uniqueItems = $true
                }
                filters = @{
                  type = "object"
                  patternProperties = @{
                    "^x-" = @{type = "string"}
                  }
                  dependentRequired = @{
                    start = @("end")
                  }
                  dependentSchemas = @{
                    advanced = @{
                      type = "object"
                      properties = @{
                        enabled = @{type = "boolean"}
                      }
                    }
                  }
                }
              }
              allOf = @(@{type = "object"})
              required = @("query")
              additionalProperties = $false
            }
          }
        }
      )
    } | ConvertTo-Json -Depth 16
    $chatResponse = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$gatewayPort/v1/chat/completions" -Headers $headers -Body $chatBody -ContentType "application/json"
    if ($chatResponse.choices[0].message.content -ne "client-ok") {
      throw "chat completions expected client-ok, got: $($chatResponse.choices[0].message.content)"
    }
    Write-Status "OK: cli backend /v1/chat/completions returned client-ok"

    $streamBody = @{
      model = "claude-sonnet-4-5"
      messages = @(@{role = "user"; content = "Reply with exactly: client-ok"})
      max_tokens = 64
      stream = $true
    } | ConvertTo-Json -Depth 16
    $streamResponse = Invoke-WebRequest -UseBasicParsing -Method Post -Uri "http://127.0.0.1:$gatewayPort/v1/messages" -Headers $headers -Body $streamBody -ContentType "application/json"
    if ($streamResponse.Content -notmatch '"text":"client-ok"' -or $streamResponse.Content -notmatch 'message_stop') {
      throw "messages stream expected client-ok and message_stop, got: $($streamResponse.Content)"
    }
    Write-Status "OK: cli backend /v1/messages stream returned client-ok"
  } finally {
    if ($null -ne $gatewayProc -and -not $gatewayProc.HasExited) {
      Stop-Process -Id $gatewayProc.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }

  Write-Status "OK: gateway cli backend e2e completed"
} finally {
  Pop-Location
}
