#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/check_local_codex_gateway.sh [options]

Check local Codex gateway token/helper wiring without printing secrets.

Options:
  --base-url URL      Gateway base URL, default: http://127.0.0.1:18080
  --model MODEL       Smoke-test model, default: gpt-5.5
  --token-command CMD Token helper path/command, default: $CODEX_ACCESS_TOKEN_COMMAND
                      or $HOME/.local/bin/codex-access-token
  --skip-upstream     Do not send the upstream smoke request
  -h, --help          Show this help
EOF
}

BASE_URL="${CODEX_GATEWAY_BASE_URL:-http://127.0.0.1:18080}"
MODEL="${CODEX_CHECK_MODEL:-gpt-5.5}"
TOKEN_COMMAND="${CODEX_ACCESS_TOKEN_COMMAND:-}"
if [ -z "$TOKEN_COMMAND" ]; then
  TOKEN_COMMAND="${CODEX_DEFAULT_ACCESS_TOKEN_COMMAND:-}"
fi
SKIP_UPSTREAM="0"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base-url)
      BASE_URL="${2:?missing value for --base-url}"
      shift 2
      ;;
    --model)
      MODEL="${2:?missing value for --model}"
      shift 2
      ;;
    --token-command)
      TOKEN_COMMAND="${2:?missing value for --token-command}"
      shift 2
      ;;
    --skip-upstream)
      SKIP_UPSTREAM="1"
      shift
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

BASE_URL="${BASE_URL%/}"
if [ -z "$TOKEN_COMMAND" ] && [ -n "${HOME:-}" ] && [ -x "$HOME/.local/bin/codex-access-token" ]; then
  TOKEN_COMMAND="$HOME/.local/bin/codex-access-token"
fi

if [ -n "${CODEX_ACCESS_TOKEN:-}${CODEX_WEB_ACCESS_TOKEN:-}" ]; then
  echo "env_token=present"
else
  echo "env_token=missing"
fi

run_token_helper() {
  if [ -x "$TOKEN_COMMAND" ]; then
    "$TOKEN_COMMAND"
  else
    sh -c "$TOKEN_COMMAND"
  fi
}

summarize_failure_body() {
  body_path="$1"
  python3 - "$body_path" <<'PY'
import json
import sys

path = sys.argv[1]
try:
    with open(path, "r", encoding="utf-8", errors="replace") as handle:
        payload = json.load(handle)
except Exception:
    print("response_body=redacted")
    raise SystemExit(0)

error = payload.get("error") if isinstance(payload, dict) else None
if isinstance(error, dict):
    if error.get("type"):
        print(f"error_type={error.get('type')}")
    if error.get("code"):
        print(f"error_code={error.get('code')}")
else:
    print("response_body=redacted")
PY
}

if [ -n "$TOKEN_COMMAND" ]; then
  echo "helper=ok"
  if run_token_helper | awk 'NR==1 { if (length($0) > 20) ok=1 } END { exit ok ? 0 : 1 }'; then
    echo "token_helper=ok"
  else
    echo "token_helper=failed"
    exit 1
  fi
else
  echo "helper=missing"
  if [ -z "${CODEX_ACCESS_TOKEN:-}${CODEX_WEB_ACCESS_TOKEN:-}" ]; then
    echo "token_helper=not_checked"
    echo "note=upstream_smoke can still validate the running gateway; the gateway process already holds upstream credentials"
  else
    echo "token_helper=not_needed"
  fi
fi

if curl -fsS "$BASE_URL/healthz" >/dev/null; then
  echo "gateway_health=ok"
else
  echo "gateway_health=failed"
  exit 1
fi

version_body="$(mktemp)"
cleanup_version() {
  rm -f "$version_body"
}
trap cleanup_version EXIT
if curl -fsS "$BASE_URL/version" >"$version_body"; then
  python3 - "$version_body" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    payload = json.load(handle)

backend = payload.get("backend_mode", payload.get("active_backend", "<missing>"))
print("gateway_version=ok")
print(f"backend_mode={backend}")
print(f"default_backend={backend}")
print(f"protocol_compatibility={payload.get('protocol_compatibility', '<missing>')}")
PY
else
  echo "gateway_version=failed"
  exit 1
fi
echo "next_command=sh scripts/run_claude_code_direct.sh --gateway-base-url $BASE_URL"

if [ "$SKIP_UPSTREAM" = "1" ]; then
  echo "upstream_smoke=skipped"
  exit 0
fi

tmp_body="$(mktemp)"
cleanup() {
  rm -f "$tmp_body"
}
trap 'cleanup; cleanup_version' EXIT

http_code="$(
  curl -sS \
    -m 180 \
    -o "$tmp_body" \
    -w '%{http_code}' \
    "$BASE_URL/v1/chat/completions" \
    -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer test-key-not-secret' \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply exactly: token-ok\"}],\"max_completion_tokens\":32}"
)"

case "$http_code" in
  2??)
    if python3 - "$tmp_body" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    data = json.load(handle)

text = (
    data.get("choices", [{}])[0]
    .get("message", {})
    .get("content", "")
)
raise SystemExit(0 if text.strip() else 1)
PY
    then
      echo "upstream_smoke=ok"
      exit 0
    fi
    echo "upstream_smoke=empty_response"
    summarize_failure_body "$tmp_body"
    exit 1
    ;;
  401|403)
    echo "upstream_smoke=auth_failed_http_$http_code"
    summarize_failure_body "$tmp_body"
    exit 1
    ;;
  *)
    echo "upstream_smoke=http_$http_code"
    summarize_failure_body "$tmp_body"
    exit 1
    ;;
esac
