param(
  [string]$DockerContainer = "",
  [string]$ReportPath = "",
  [switch]$TestCCRCode
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
  if ($script:GatewayClientReportPath.Trim() -ne "") {
    Add-Content -Path $script:GatewayClientReportPath -Value $Message -Encoding ASCII
  }
}

function Assert-ClientOK([string]$Label, [object]$Value) {
  if ($Value -ne "client-ok") {
    throw "$Label expected client-ok, got: $Value"
  }
  Write-Status "OK: $Label returned client-ok"
}

function Assert-ContainsClientOK([string]$Label, [object[]]$Lines) {
  $text = ($Lines -join "`n").Trim()
  if ($text -notmatch "(^|[\r\n])client-ok($|[\r\n])" -and $text -notmatch '"result"\s*:\s*"client-ok"') {
    throw "$Label expected client-ok, got: $text"
  }
  Write-Status "OK: $Label returned client-ok"
}

function Convert-LastJsonLine([object[]]$Lines, [string]$Label) {
  $jsonLine = $Lines | Where-Object { $_.TrimStart().StartsWith("{") } | Select-Object -Last 1
  if (-not $jsonLine) {
    throw "$Label did not emit a JSON result"
  }
  return $jsonLine | ConvertFrom-Json
}

function Invoke-DockerShell([string]$Container, [string]$Script, [string]$Label) {
  $normalizedScript = $Script -replace "`r`n", "`n"
  $output = $normalizedScript | docker exec -i $Container sh
  if ($LASTEXITCODE -ne 0) {
    $outputText = (@($output) -join "`n").Trim()
    throw "$Label failed with docker exit code $LASTEXITCODE. Output: $outputText"
  }
  return @($output)
}

function Restore-EnvVar([string]$Name, [string]$Value) {
  if ($null -eq $Value) {
    Remove-Item -Path "Env:\$Name" -ErrorAction SilentlyContinue
    return
  }
  Set-Item -Path "Env:\$Name" -Value $Value
}

function Ensure-CCRCodePromptPatch([string]$Container) {
  throw "CCR code prompt patching is not included in the public repository."
}

Push-Location $Root
try {
  $script:GatewayClientReportPath = $ReportPath
  if ($script:GatewayClientReportPath.Trim() -ne "") {
    $reportParent = Split-Path -Parent $script:GatewayClientReportPath
    if ($reportParent -and -not (Test-Path $reportParent)) {
      New-Item -ItemType Directory -Path $reportParent -Force | Out-Null
    }
    Set-Content -Path $script:GatewayClientReportPath -Value $null -Encoding ASCII
  }
  Write-Status "BEGIN: gateway client e2e"
  $go = Resolve-Go
  $python = (Get-Command python -ErrorAction Stop).Source
  $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("codex-gate-gateway-clients-" + [Guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Path $tmp | Out-Null

  $fakePort = Get-FreeTcpPort
  $gatewayPort = Get-FreeTcpPort
  $fakeScript = Join-Path $tmp "fake_codex_upstream.py"
  $fakeLog = Join-Path $tmp "fake_upstream.jsonl"
  $gatewayConfig = Join-Path $tmp "gateway.toml"
  $gatewayBin = Join-Path $tmp "gateway.exe"
  $fakeOut = Join-Path $tmp "fake.out.log"
  $fakeErr = Join-Path $tmp "fake.err.log"
  $gatewayOut = Join-Path $tmp "gateway.out.log"
  $gatewayErr = Join-Path $tmp "gateway.err.log"
  $fakeProc = $null
  $gatewayProc = $null

  try {
@'
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

port = int(sys.argv[1])
log_path = sys.argv[2]

class Handler(BaseHTTPRequestHandler):
    def _json(self, status, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("x-request-id", "req_fake_gateway_clients")
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("content-length") or "0")
        raw = self.rfile.read(length) if length else b"{}"
        try:
            data = json.loads(raw.decode("utf-8"))
        except Exception:
            data = {}
        with open(log_path, "a", encoding="utf-8") as handle:
            handle.write(json.dumps({
                "path": self.path,
                "top_level_keys": sorted(list(data.keys())),
                "stream": data.get("stream", False),
                "model": data.get("model"),
                "input_items": len(data.get("input") or []),
            }) + "\n")

        if self.path == "/v1/responses" and data.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("x-request-id", "req_fake_gateway_clients_stream")
            self.end_headers()
            for event in [
                {"type": "response.created", "response": {"id": "resp_fake_gateway_clients_stream"}},
                {"type": "response.output_text.delta", "delta": "client-ok"},
                {"type": "response.completed", "response": {"id": "resp_fake_gateway_clients_stream"}},
            ]:
                self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode("utf-8"))
                self.wfile.flush()
            return

        if self.path == "/v1/responses/stream":
            self._json(200, {
                "final_status": "completed",
                "stream": [
                    {"event": "response.started", "response_id": "resp_fake_gateway_clients"},
                    {"event": "response.output_text.delta", "delta": "client-ok"},
                    {"event": "response.completed", "response_id": "resp_fake_gateway_clients", "status": "completed"},
                ],
            })
            return

        if self.path == "/v1/responses":
            self._json(200, {
                "id": "resp_fake_gateway_clients",
                "status": "completed",
                "output": [{
                    "type": "message",
                    "role": "assistant",
                    "content": [{"type": "output_text", "text": "client-ok"}],
                }],
            })
            return

        self._json(404, {"error": {"message": "not found"}})

    def log_message(self, format, *args):
        return

ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
'@ | Set-Content -Path $fakeScript -Encoding UTF8

@"
[gateway]
host = "127.0.0.1"
port = $gatewayPort
log_level = "info"
redact_logs = true
allow_wide_bind = false
"@ | Set-Content -Path $gatewayConfig -Encoding ASCII

    & $go build -trimpath -o $gatewayBin ./cmd/gateway

    $fakeProc = Start-Process -FilePath $python -ArgumentList @($fakeScript, $fakePort, $fakeLog) -PassThru -RedirectStandardOutput $fakeOut -RedirectStandardError $fakeErr -WindowStyle Hidden
    if (-not (Wait-TcpPort -Port $fakePort -TimeoutMs 10000)) {
      throw "fake Codex upstream did not start on port $fakePort"
    }

    $oldCodexBaseURL = $env:CODEX_BASE_URL
    $oldCodexAPIKey = $env:CODEX_API_KEY
    try {
      $env:CODEX_BASE_URL = "http://127.0.0.1:$fakePort"
      $env:CODEX_API_KEY = "test-key-not-secret"
      $gatewayProc = Start-Process -FilePath $gatewayBin -ArgumentList @("-config", $gatewayConfig) -PassThru -RedirectStandardOutput $gatewayOut -RedirectStandardError $gatewayErr -WindowStyle Hidden
    } finally {
      Restore-EnvVar "CODEX_BASE_URL" $oldCodexBaseURL
      Restore-EnvVar "CODEX_API_KEY" $oldCodexAPIKey
    }
    if (-not (Wait-TcpPort -Port $gatewayPort -TimeoutMs 10000)) {
      throw "gateway did not start on port $gatewayPort"
    }

    $headers = @{"x-api-key" = "test-key-not-secret"}
    $messagesBody = @{
      model = "claude-sonnet-4-5"
      system = @(@{type = "text"; text = "Test compatibility wrapper."})
      messages = @(@{role = "user"; content = "Reply with exactly: client-ok"})
      max_tokens = 64
    } | ConvertTo-Json -Depth 16
    $messagesResponse = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$gatewayPort/v1/messages" -Headers $headers -Body $messagesBody -ContentType "application/json"
    Assert-ClientOK "host /v1/messages" $messagesResponse.content[0].text

    $chatBody = @{
      model = "claude-sonnet-4-5"
      messages = @(
        @{role = "system"; content = "Test OpenAI-compatible wrapper."},
        @{role = "user"; content = "Reply with exactly: client-ok"}
      )
      max_completion_tokens = 64
      temperature = 0
    } | ConvertTo-Json -Depth 16
    $chatResponse = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$gatewayPort/v1/chat/completions" -Headers $headers -Body $chatBody -ContentType "application/json"
    Assert-ClientOK "host /v1/chat/completions" $chatResponse.choices[0].message.content

    $streamBody = @{
      model = "claude-sonnet-4-5"
      messages = @(@{role = "user"; content = "Reply with exactly: client-ok"})
      max_completion_tokens = 64
      stream = $true
    } | ConvertTo-Json -Depth 16
    $streamResponse = Invoke-WebRequest -UseBasicParsing -Method Post -Uri "http://127.0.0.1:$gatewayPort/v1/chat/completions" -Headers $headers -Body $streamBody -ContentType "application/json"
    if ($streamResponse.Content -notmatch '"content":"client-ok"' -or $streamResponse.Content -notmatch 'data: \[DONE\]') {
      throw "host /v1/chat/completions stream did not emit expected OpenAI SSE content"
    }
    Write-Status "OK: host /v1/chat/completions stream returned client-ok"

    if ($DockerContainer.Trim() -ne "") {
      $containerLine = docker ps --format "{{.Names}}" | Where-Object { $_ -eq $DockerContainer } | Select-Object -First 1
      if (-not $containerLine) {
        throw "Docker container '$DockerContainer' is not running"
      }
      docker exec $DockerContainer sh -lc "command -v claude >/dev/null && command -v ccr >/dev/null" | Out-Null
      if ($LASTEXITCODE -ne 0) {
        throw "Docker container '$DockerContainer' must provide both claude and ccr commands"
      }

      $directScript = @'
set -u
tmp="/tmp/claude-direct-codex-gate-$$"
cleanup() { rc=$?; rm -rf "$tmp"; exit $rc; }
trap cleanup EXIT
rm -rf "$tmp"; mkdir -p "$tmp"
export HOME="$tmp"
export ANTHROPIC_BASE_URL="http://host.docker.internal:__GATEWAY_PORT__"
export ANTHROPIC_API_KEY="test-key-not-secret"
export ANTHROPIC_AUTH_TOKEN="test-key-not-secret"
export DISABLE_TELEMETRY=1
export DISABLE_COST_WARNINGS=1
cd /tmp
claude --bare -p --model claude-sonnet-4-5 --output-format json "Reply with exactly: client-ok"
'@.Replace("__GATEWAY_PORT__", [string]$gatewayPort)
      $directResult = Convert-LastJsonLine (Invoke-DockerShell $DockerContainer $directScript "Claude Code direct client") "Claude Code direct client"
      Assert-ClientOK "docker Claude Code direct" $directResult.result

      $ccrScript = @'
set -u
tmp="/tmp/ccr-codex-gate-$$"
ccr_pid=""
cleanup() {
  rc=$?
  if [ -n "$ccr_pid" ]; then kill "$ccr_pid" >/dev/null 2>&1 || true; fi
  HOME="$tmp" ccr stop >/dev/null 2>&1 || true
  if [ $rc -ne 0 ] && [ -f "$tmp/ccr.log" ]; then tail -n 80 "$tmp/ccr.log" >&2 || true; fi
  rm -rf "$tmp"
  exit $rc
}
trap cleanup EXIT
rm -rf "$tmp"; mkdir -p "$tmp/.claude-code-router"
cat >"$tmp/.claude-code-router/config.json" <<'EOF'
{"PORT":3484,"HOST":"127.0.0.1","LOG":false,"NON_INTERACTIVE_MODE":true,"APIKEY":"test-key-not-secret","Providers":[{"name":"codex-gate","api_base_url":"http://host.docker.internal:__GATEWAY_PORT__/v1/chat/completions","api_key":"test-key-not-secret","models":["claude-sonnet-4-5"]}],"Router":{"default":"codex-gate,claude-sonnet-4-5"}}
EOF
HOME="$tmp" ccr start >"$tmp/ccr.log" 2>&1 &
ccr_pid=$!
i=0
while [ $i -lt 40 ]; do
  if command -v curl >/dev/null 2>&1 && curl -sS --max-time 1 "http://127.0.0.1:3484/" >/dev/null 2>&1; then break; fi
  i=$((i+1)); sleep 0.25
done
export HOME="$tmp"
export ANTHROPIC_BASE_URL="http://127.0.0.1:3484"
export ANTHROPIC_API_KEY="test-key-not-secret"
export ANTHROPIC_AUTH_TOKEN="test-key-not-secret"
export DISABLE_TELEMETRY=1
export DISABLE_COST_WARNINGS=1
cd /tmp
claude --bare -p --model claude-sonnet-4-5 --output-format json "Reply with exactly: client-ok"
'@.Replace("__GATEWAY_PORT__", [string]$gatewayPort)
      $ccrResult = Convert-LastJsonLine (Invoke-DockerShell $DockerContainer $ccrScript "CCR routed client") "CCR routed client"
      Assert-ClientOK "docker CCR routed Claude Code" $ccrResult.result

      if ($TestCCRCode) {
        Ensure-CCRCodePromptPatch $DockerContainer
        $ccrCodeScript = @'
set -u
tmp="/tmp/ccr-code-codex-gate-$$"
cleanup() {
  rc=$?
  HOME="$tmp" ccr stop >/dev/null 2>&1 || true
  rm -rf "$tmp"
  exit $rc
}
trap cleanup EXIT
rm -rf "$tmp"; mkdir -p "$tmp/.claude-code-router"
cat >"$tmp/.claude-code-router/config.json" <<'EOF'
{"PORT":3485,"HOST":"127.0.0.1","LOG":false,"NON_INTERACTIVE_MODE":true,"APIKEY":"test-key-not-secret","Providers":[{"name":"codex-gate","api_base_url":"http://host.docker.internal:__GATEWAY_PORT__/v1/chat/completions","api_key":"test-key-not-secret","models":["claude-sonnet-4-5"]}],"Router":{"default":"codex-gate,claude-sonnet-4-5"}}
EOF
export HOME="$tmp"
export ANTHROPIC_API_KEY="test-key-not-secret"
export ANTHROPIC_AUTH_TOKEN="test-key-not-secret"
export DISABLE_TELEMETRY=1
export DISABLE_COST_WARNINGS=1
cd /tmp
ccr code "Reply with exactly: client-ok"
'@.Replace("__GATEWAY_PORT__", [string]$gatewayPort)
        $ccrCodeOutput = Invoke-DockerShell $DockerContainer $ccrCodeScript 'CCR code prompt client'
        Assert-ContainsClientOK 'docker ccr code prompt' $ccrCodeOutput
      }
    }

    if (Test-Path $fakeLog) {
      $observed = Get-Content $fakeLog
      if ($observed.Count -lt 3) {
        throw "fake upstream observed too few requests: $($observed.Count)"
      }
      Write-Status "OK: fake Codex upstream observed $($observed.Count) request(s)"
    }
  } finally {
    if ($null -ne $gatewayProc -and -not $gatewayProc.HasExited) {
      Stop-Process -Id $gatewayProc.Id -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $fakeProc -and -not $fakeProc.HasExited) {
      Stop-Process -Id $fakeProc.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }
  Write-Status "OK: gateway client e2e completed"
} finally {
  Pop-Location
}
