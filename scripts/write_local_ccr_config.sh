#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/write_local_ccr_config.sh [options]

Write a local CCR config that routes to this gateway.

Options:
  --gateway-base-url URL  Gateway base URL, default: http://127.0.0.1:18080
  --provider NAME        Provider name, default: codex-gate
  --model MODEL          CCR provider model name, default: gpt-5.5
  --api-key KEY          Placeholder API key, default: test-key-not-secret
  --ccr-port PORT        CCR service port in config, default: 3487
  --ccr-host HOST        CCR service host in config, default: 127.0.0.1
  --api-timeout-ms MS    CCR API timeout in milliseconds, default: 21600000
  -h, --help             Show this help
EOF
}

GATEWAY_BASE_URL="http://127.0.0.1:18080"
PROVIDER_NAME="codex-gate"
MODEL="${CCR_MODEL:-gpt-5.5}"
API_KEY="test-key-not-secret"
CCR_PORT="3487"
CCR_HOST="127.0.0.1"
API_TIMEOUT_MS="${CCR_API_TIMEOUT_MS:-21600000}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --gateway-base-url)
      GATEWAY_BASE_URL="${2:?missing value for --gateway-base-url}"
      shift 2
      ;;
    --provider)
      PROVIDER_NAME="${2:?missing value for --provider}"
      shift 2
      ;;
    --model)
      MODEL="${2:?missing value for --model}"
      shift 2
      ;;
    --api-key)
      API_KEY="${2:?missing value for --api-key}"
      shift 2
      ;;
    --ccr-port)
      CCR_PORT="${2:?missing value for --ccr-port}"
      shift 2
      ;;
    --ccr-host)
      CCR_HOST="${2:?missing value for --ccr-host}"
      shift 2
      ;;
    --api-timeout-ms)
      API_TIMEOUT_MS="${2:?missing value for --api-timeout-ms}"
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

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required" >&2
  exit 1
fi

CONFIG_DIR="${HOME:?HOME is required}/.claude-code-router"
CONFIG_PATH="$CONFIG_DIR/config.json"
API_BASE_URL="${GATEWAY_BASE_URL%/}/v1/chat/completions"
mkdir -p "$CONFIG_DIR"

if [ -f "$CONFIG_PATH" ]; then
  backup="$CONFIG_PATH.codex-gate-backup-$(date +%Y%m%d%H%M%S)"
  cp "$CONFIG_PATH" "$backup"
  echo "backup=$backup"
fi

export CONFIG_PATH API_BASE_URL PROVIDER_NAME MODEL API_KEY CCR_PORT CCR_HOST
export API_TIMEOUT_MS
python3 <<'PY'
import json
import os
from pathlib import Path

config = {
    "PORT": int(os.environ["CCR_PORT"]),
    "HOST": os.environ["CCR_HOST"],
    "LOG": False,
    "API_TIMEOUT_MS": int(os.environ["API_TIMEOUT_MS"]),
    "NON_INTERACTIVE_MODE": True,
    "APIKEY": os.environ["API_KEY"],
    "Providers": [
        {
            "name": os.environ["PROVIDER_NAME"],
            "api_base_url": os.environ["API_BASE_URL"],
            "api_key": os.environ["API_KEY"],
            "models": [os.environ["MODEL"]],
        }
    ],
    "Router": {
        "default": f"{os.environ['PROVIDER_NAME']},{os.environ['MODEL']}",
    },
}
path = Path(os.environ["CONFIG_PATH"])
path.write_text(json.dumps(config, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY

echo "OK: wrote local CCR config"
echo "path=$CONFIG_PATH"
echo "provider=$PROVIDER_NAME"
echo "model=$MODEL"
echo "api_base_url=$API_BASE_URL"
echo "api_timeout_ms=$API_TIMEOUT_MS"
echo "ccr_command=ccr code \"Reply exactly: ccr-ok\""
