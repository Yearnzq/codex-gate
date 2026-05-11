#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
cleanup() {
  if [ -n "${GATEWAY_PID:-}" ]; then
    kill "$GATEWAY_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

detect_target() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$os" in
    linux*) os="linux" ;;
    darwin*) os="darwin" ;;
  esac
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
  esac
  printf '%s-%s\n' "$os" "$arch"
}

GATEWAY_BIN="$ROOT/dist/gateway/$(detect_target)/gateway"
if [ -f "$GATEWAY_BIN" ] && [ ! -x "$GATEWAY_BIN" ]; then
  chmod +x "$GATEWAY_BIN" 2>/dev/null || true
fi
if [ ! -x "$GATEWAY_BIN" ]; then
  if ! command -v go >/dev/null 2>&1; then
    echo "gateway binary not found and go is not available: $GATEWAY_BIN" >&2
    exit 1
  fi
  mkdir -p "$(dirname "$GATEWAY_BIN")"
  (cd "$ROOT" && go build -trimpath -o "$GATEWAY_BIN" ./cmd/gateway)
fi

PORT="$(python3 - <<'PY'
import socket
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
)"

CONFIG="$TMP_DIR/gateway.toml"
FAKE_CODEX="$TMP_DIR/fake_codex_cli.py"
OUT_LOG="$TMP_DIR/gateway.out.log"
ERR_LOG="$TMP_DIR/gateway.err.log"

cat >"$CONFIG" <<EOF
[gateway]
host = "127.0.0.1"
port = $PORT
log_level = "info"
redact_logs = true
allow_wide_bind = false
EOF

cat >"$FAKE_CODEX" <<'PY'
import sys

prompt = sys.argv[1] if len(sys.argv) > 1 else sys.stdin.read()
if "client-ok" not in prompt:
    print("unexpected prompt", file=sys.stderr)
    sys.exit(2)
print("client-ok")
PY

export CODEX_BACKEND="cli"
export CODEX_CLI_COMMAND="python3"
export CODEX_CLI_ARGS_JSON="$(python3 - "$FAKE_CODEX" <<'PY'
import json
import sys
print(json.dumps([sys.argv[1], "{{prompt}}"]))
PY
)"
export CODEX_CLI_TIMEOUT="15s"

"$GATEWAY_BIN" -config "$CONFIG" >"$OUT_LOG" 2>"$ERR_LOG" &
GATEWAY_PID="$!"

python3 - "$PORT" <<'PY'
import json
import os
import sys
import time
import urllib.request

port = int(sys.argv[1])
health_url = f"http://127.0.0.1:{port}/healthz"
deadline = time.time() + 10
while time.time() < deadline:
    try:
        with urllib.request.urlopen(health_url, timeout=0.5) as response:
            if json.loads(response.read().decode("utf-8")).get("status") == "ok":
                break
    except Exception:
        time.sleep(0.2)
else:
    raise SystemExit("gateway did not become healthy")

payload = {
    "model": "gpt-5.5",
    "messages": [{"role": "user", "content": "Reply with exactly: client-ok"}],
    "max_completion_tokens": 64,
    "tools": [
        {
            "type": "function",
            "function": {
                "name": "local_tool",
                "parameters": {
                    "type": "object",
                    "$defs": {"mode": {"type": "string", "const": "read"}},
                    "properties": {
                        "status": {
                            "anyOf": [
                                {"type": "string", "enum": ["pending", "completed"]},
                                {"const": "completed"},
                            ]
                        },
                        "query": {"type": ["string", "null"]},
                        "mode": {"$ref": "#/$defs/mode"},
                        "items": {
                            "type": "array",
                            "prefixItems": [{"type": "string"}, {"type": "integer"}],
                            "items": {
                                "oneOf": [{"type": "string"}, {"type": "number"}]
                            },
                            "contains": {"type": "string", "const": "needle"},
                            "uniqueItems": True,
                        },
                        "filters": {
                            "type": "object",
                            "patternProperties": {"^x-": {"type": "string"}},
                            "dependentRequired": {"start": ["end"]},
                            "dependentSchemas": {
                                "advanced": {
                                    "type": "object",
                                    "properties": {"enabled": {"type": "boolean"}},
                                }
                            },
                        },
                    },
                    "allOf": [{"type": "object"}],
                    "required": ["query"],
                    "additionalProperties": False,
                },
            },
        }
    ],
}
request = urllib.request.Request(
    f"http://127.0.0.1:{port}/v1/chat/completions",
    data=json.dumps(payload).encode("utf-8"),
    headers={
        "Content-Type": "application/json",
        "Authorization": "Bearer test-key-not-secret",
    },
    method="POST",
)
with urllib.request.urlopen(request, timeout=15) as response:
    body = json.loads(response.read().decode("utf-8"))
content = body["choices"][0]["message"]["content"]
if content != "client-ok":
    raise SystemExit(f"unexpected response: {content!r}")
print("OK: gateway schema compatibility smoke returned client-ok")
PY
