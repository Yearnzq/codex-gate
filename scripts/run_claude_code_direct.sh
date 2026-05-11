#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/run_claude_code_direct.sh [options] [-- claude-args...]

Run Claude Code directly against this gateway's Anthropic-compatible API.
Use this from the target project directory.

Options:
  --gateway-base-url URL     Gateway base URL, default: http://127.0.0.1:18080
  --model MODEL             Claude Code display/upstream model, default: gpt-5.5
  --small-fast-model MODEL  Claude Code small/fast model, default: gpt-5.5
  --auth-token TOKEN        Placeholder gateway bearer token, default: test-key-not-secret
  --api-key KEY             Deprecated alias for --auth-token
  --api-timeout-ms MS       Claude Code API timeout, default: 21600000
  --stream-idle-timeout-ms MS
                            Claude Code stream idle timeout, default: 21600000
  --auto-compact-window TOKENS
                            Claude Code auto-compact window, default: 200000
  --auto-compact-pct PCT    Claude Code auto-compact trigger percent, default: 70
  -h, --help                Show this help
EOF
}

GATEWAY_BASE_URL="${ANTHROPIC_BASE_URL:-http://127.0.0.1:18080}"
MODEL="${ANTHROPIC_MODEL:-gpt-5.5}"
SMALL_FAST_MODEL="${ANTHROPIC_SMALL_FAST_MODEL:-gpt-5.5}"
AUTH_TOKEN="${CLAUDE_CODE_GATEWAY_AUTH_TOKEN:-test-key-not-secret}"
API_TIMEOUT_MS="${API_TIMEOUT_MS:-21600000}"
STREAM_IDLE_TIMEOUT_MS="${CLAUDE_STREAM_IDLE_TIMEOUT_MS:-21600000}"
AUTO_COMPACT_WINDOW="${CLAUDE_CODE_AUTO_COMPACT_WINDOW:-200000}"
AUTO_COMPACT_PCT="${CLAUDE_AUTOCOMPACT_PCT_OVERRIDE:-70}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --gateway-base-url)
      GATEWAY_BASE_URL="${2:?missing value for --gateway-base-url}"
      shift 2
      ;;
    --model)
      MODEL="${2:?missing value for --model}"
      shift 2
      ;;
    --small-fast-model)
      SMALL_FAST_MODEL="${2:?missing value for --small-fast-model}"
      shift 2
      ;;
    --auth-token|--api-key)
      AUTH_TOKEN="${2:?missing value for $1}"
      shift 2
      ;;
    --api-timeout-ms)
      API_TIMEOUT_MS="${2:?missing value for --api-timeout-ms}"
      shift 2
      ;;
    --stream-idle-timeout-ms)
      STREAM_IDLE_TIMEOUT_MS="${2:?missing value for --stream-idle-timeout-ms}"
      shift 2
      ;;
    --auto-compact-window)
      AUTO_COMPACT_WINDOW="${2:?missing value for --auto-compact-window}"
      shift 2
      ;;
    --auto-compact-pct)
      AUTO_COMPACT_PCT="${2:?missing value for --auto-compact-pct}"
      shift 2
      ;;
    --)
      shift
      break
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      break
      ;;
  esac
done

if ! command -v claude >/dev/null 2>&1; then
  echo "claude command is required" >&2
  exit 1
fi

export ANTHROPIC_BASE_URL="${GATEWAY_BASE_URL%/}"
export ANTHROPIC_AUTH_TOKEN="$AUTH_TOKEN"
unset ANTHROPIC_API_KEY
export ANTHROPIC_MODEL="$MODEL"
export ANTHROPIC_SMALL_FAST_MODEL="$SMALL_FAST_MODEL"
export API_TIMEOUT_MS="$API_TIMEOUT_MS"
export CLAUDE_STREAM_IDLE_TIMEOUT_MS="$STREAM_IDLE_TIMEOUT_MS"
export CLAUDE_CODE_AUTO_COMPACT_WINDOW="$AUTO_COMPACT_WINDOW"
export CLAUDE_AUTOCOMPACT_PCT_OVERRIDE="$AUTO_COMPACT_PCT"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
export CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1

echo "ANTHROPIC_BASE_URL=$ANTHROPIC_BASE_URL"
echo "ANTHROPIC_MODEL=$ANTHROPIC_MODEL"
echo "ANTHROPIC_SMALL_FAST_MODEL=$ANTHROPIC_SMALL_FAST_MODEL"
echo "API_TIMEOUT_MS=$API_TIMEOUT_MS"
echo "CLAUDE_STREAM_IDLE_TIMEOUT_MS=$CLAUDE_STREAM_IDLE_TIMEOUT_MS"
echo "CLAUDE_CODE_AUTO_COMPACT_WINDOW=$CLAUDE_CODE_AUTO_COMPACT_WINDOW"
echo "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE=$CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"
echo "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=$CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK"
printf 'claude_command=claude'
for arg in "$@"; do
  printf ' %s' "$arg"
done
printf '\n'
exec claude "$@"
