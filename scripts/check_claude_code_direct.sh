#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/check_claude_code_direct.sh [options]

Check the local Claude Code direct-to-gateway setup without printing tokens.
Run this in the same Linux environment/container where you run Claude Code.

Options:
  --gateway-base-url URL   Gateway base URL, default: http://127.0.0.1:18080
  --settings PATH          Claude Code settings path, default: ~/.claude/settings.json
  --model MODEL            Expected Claude Code model, default: gpt-5.5
  --run-claude-smoke       Run a real non-interactive Claude Code request
  --smoke-prompt PROMPT    Prompt for --run-claude-smoke, default: Reply exactly: proxy-ok
  --smoke-timeout SECONDS  Timeout for --run-claude-smoke, default: 180
  -h, --help               Show this help
EOF
}

GATEWAY_BASE_URL="${ANTHROPIC_BASE_URL:-http://127.0.0.1:18080}"
SETTINGS_PATH="${HOME:?HOME is required}/.claude/settings.json"
MODEL="${ANTHROPIC_MODEL:-gpt-5.5}"
RUN_CLAUDE_SMOKE="0"
SMOKE_PROMPT="Reply exactly: proxy-ok"
SMOKE_TIMEOUT_SECONDS="180"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --gateway-base-url)
      GATEWAY_BASE_URL="${2:?missing value for --gateway-base-url}"
      shift 2
      ;;
    --settings)
      SETTINGS_PATH="${2:?missing value for --settings}"
      shift 2
      ;;
    --model)
      MODEL="${2:?missing value for --model}"
      shift 2
      ;;
    --run-claude-smoke)
      RUN_CLAUDE_SMOKE="1"
      shift
      ;;
    --smoke-prompt)
      SMOKE_PROMPT="${2:?missing value for --smoke-prompt}"
      shift 2
      ;;
    --smoke-timeout)
      SMOKE_TIMEOUT_SECONDS="${2:?missing value for --smoke-timeout}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

GATEWAY_BASE_URL="${GATEWAY_BASE_URL%/}"

if ! command -v python3 >/dev/null 2>&1; then
  echo "FAIL: python3 is required" >&2
  exit 1
fi

echo "check=runtime"
echo "pwd=$(pwd)"
echo "home=$HOME"
if command -v claude >/dev/null 2>&1; then
  echo "claude=$(command -v claude)"
  claude --version 2>/dev/null | sed 's/^/claude_version=/' || true
else
  echo "claude=<missing>"
fi

python3 - "$GATEWAY_BASE_URL" "$SETTINGS_PATH" "$MODEL" <<'PY'
import json
import os
import socket
import sys
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import urlparse

base_url = sys.argv[1].rstrip("/")
settings_path = Path(sys.argv[2]).expanduser()
expected_model = sys.argv[3]

def mask(value):
    if value is None:
        return "<missing>"
    text = str(value)
    if text == "":
        return "<empty>"
    return f"<set len={len(text)}>"

def print_next_commands():
    print("next_command=claude")
    print(f"next_wrapper_command=sh scripts/run_claude_code_direct.sh --gateway-base-url {base_url}")

print("check=gateway")
parsed = urlparse(base_url)
host = parsed.hostname or "127.0.0.1"
port = parsed.port or (443 if parsed.scheme == "https" else 80)
sock = socket.socket()
sock.settimeout(2)
try:
    sock.connect((host, port))
    print(f"tcp={host}:{port}=ok")
except Exception as exc:
    print(f"tcp={host}:{port}=fail {type(exc).__name__}: {exc}")
finally:
    sock.close()

for path in ("/healthz", "/version"):
    url = base_url + path
    try:
        with urllib.request.urlopen(url, timeout=5) as response:
            body = response.read(512).decode("utf-8", "replace").strip()
        print(f"http{path}=ok {body}")
    except Exception as exc:
        print(f"http{path}=fail {type(exc).__name__}: {exc}")

print("check=current-env")
for key in (
    "ANTHROPIC_BASE_URL",
    "ANTHROPIC_MODEL",
    "ANTHROPIC_SMALL_FAST_MODEL",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_AUTH_TOKEN",
    "API_TIMEOUT_MS",
    "CLAUDE_STREAM_IDLE_TIMEOUT_MS",
    "CLAUDE_CODE_AUTO_COMPACT_WINDOW",
    "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE",
):
    value = os.environ.get(key)
    if key in {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}:
        print(f"{key}={mask(value)}")
    else:
        print(f"{key}={value or '<missing>'}")

if os.environ.get("ANTHROPIC_API_KEY") and os.environ.get("ANTHROPIC_AUTH_TOKEN"):
    print("WARN: auth_conflict=ANTHROPIC_API_KEY_and_ANTHROPIC_AUTH_TOKEN")
elif os.environ.get("ANTHROPIC_API_KEY"):
    print("WARN: auth_conflict=ANTHROPIC_API_KEY_present")
else:
    print("auth_conflict=none")

print_next_commands()

print("check=settings")
print(f"settings_path={settings_path}")
if not settings_path.exists():
    print("settings_exists=false")
    raise SystemExit(0)
print("settings_exists=true")
try:
    data = json.loads(settings_path.read_text(encoding="utf-8"))
except Exception as exc:
    print(f"settings_json=fail {type(exc).__name__}: {exc}")
    raise SystemExit(0)
if not isinstance(data, dict):
    print(f"settings_json=fail expected object got {type(data).__name__}")
    raise SystemExit(0)
env = data.get("env")
print(f"settings_model={data.get('model', '<missing>')}")
if not isinstance(env, dict):
    print("settings_env=<missing-or-not-object>")
    raise SystemExit(0)
for key in (
    "ANTHROPIC_BASE_URL",
    "ANTHROPIC_MODEL",
    "ANTHROPIC_SMALL_FAST_MODEL",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_AUTH_TOKEN",
    "API_TIMEOUT_MS",
    "CLAUDE_STREAM_IDLE_TIMEOUT_MS",
    "CLAUDE_CODE_AUTO_COMPACT_WINDOW",
    "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE",
):
    value = env.get(key)
    if key in {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}:
        print(f"settings.{key}={mask(value)}")
    else:
        print(f"settings.{key}={value or '<missing>'}")

if env.get("ANTHROPIC_BASE_URL", "").rstrip("/") != base_url:
    print("WARN: settings ANTHROPIC_BASE_URL does not match --gateway-base-url")
if data.get("model") != expected_model and env.get("ANTHROPIC_MODEL") != expected_model:
    print("WARN: configured model does not match --model")

permissions = data.get("permissions")
if isinstance(permissions, dict):
    deny = permissions.get("deny")
    if isinstance(deny, list):
        agent_denied = any(str(item) in {"Agent", "Task"} for item in deny)
        print(f"settings.permissions.deny_agent_tool={'true' if agent_denied else 'false'}")
PY

if [ "$RUN_CLAUDE_SMOKE" = "1" ]; then
  if ! command -v claude >/dev/null 2>&1; then
    echo "FAIL: claude command is required for --run-claude-smoke" >&2
    exit 1
  fi
  case "$SMOKE_TIMEOUT_SECONDS" in
    ''|*[!0-9]*)
      echo "FAIL: --smoke-timeout must be a non-negative integer" >&2
      exit 2
      ;;
  esac
  echo "check=claude-smoke"
  if command -v timeout >/dev/null 2>&1; then
    ANTHROPIC_BASE_URL="$GATEWAY_BASE_URL" \
    ANTHROPIC_AUTH_TOKEN="${CLAUDE_CODE_GATEWAY_AUTH_TOKEN:-test-key-not-secret}" \
    ANTHROPIC_MODEL="$MODEL" \
    API_TIMEOUT_MS="${API_TIMEOUT_MS:-21600000}" \
    CLAUDE_STREAM_IDLE_TIMEOUT_MS="${CLAUDE_STREAM_IDLE_TIMEOUT_MS:-21600000}" \
    CLAUDE_CODE_AUTO_COMPACT_WINDOW="${CLAUDE_CODE_AUTO_COMPACT_WINDOW:-200000}" \
    CLAUDE_AUTOCOMPACT_PCT_OVERRIDE="${CLAUDE_AUTOCOMPACT_PCT_OVERRIDE:-70}" \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1 \
      env -u ANTHROPIC_API_KEY timeout "$SMOKE_TIMEOUT_SECONDS" claude --print --output-format text --model "$MODEL" "$SMOKE_PROMPT"
  else
    ANTHROPIC_BASE_URL="$GATEWAY_BASE_URL" \
    ANTHROPIC_AUTH_TOKEN="${CLAUDE_CODE_GATEWAY_AUTH_TOKEN:-test-key-not-secret}" \
    ANTHROPIC_MODEL="$MODEL" \
    API_TIMEOUT_MS="${API_TIMEOUT_MS:-21600000}" \
    CLAUDE_STREAM_IDLE_TIMEOUT_MS="${CLAUDE_STREAM_IDLE_TIMEOUT_MS:-21600000}" \
    CLAUDE_CODE_AUTO_COMPACT_WINDOW="${CLAUDE_CODE_AUTO_COMPACT_WINDOW:-200000}" \
    CLAUDE_AUTOCOMPACT_PCT_OVERRIDE="${CLAUDE_AUTOCOMPACT_PCT_OVERRIDE:-70}" \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1 \
      env -u ANTHROPIC_API_KEY claude --print --output-format text --model "$MODEL" "$SMOKE_PROMPT"
  fi
fi
