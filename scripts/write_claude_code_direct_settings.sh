#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/write_claude_code_direct_settings.sh [options]

Persist Claude Code settings for direct Anthropic-compatible access to this gateway.
This writes ~/.claude/settings.json and preserves existing settings.

Options:
  --gateway-base-url URL       Gateway base URL, default: http://127.0.0.1:18080
  --model MODEL               Claude Code model, default: gpt-5.5
  --small-fast-model MODEL    Claude Code small/fast model, default: gpt-5.5
  --auth-token TOKEN          Placeholder gateway bearer token, default: test-key-not-secret
  --api-key KEY               Deprecated alias for --auth-token
  --api-timeout-ms MS         Claude Code API timeout, default: 21600000
  --stream-idle-timeout-ms MS Claude Code stream idle timeout, default: 21600000
  --auto-compact-window TOKENS
                             Claude Code auto-compact window, default: 200000
  --auto-compact-pct PCT      Claude Code auto-compact trigger percent, default: 70
  --deny-agent-tool           Add official Claude Code permission deny rules for Agent/Task
  --path PATH                 Settings path, default: ~/.claude/settings.json
  -h, --help                  Show this help
EOF
}

GATEWAY_BASE_URL="http://127.0.0.1:18080"
MODEL="gpt-5.5"
SMALL_FAST_MODEL="gpt-5.5"
AUTH_TOKEN="test-key-not-secret"
API_TIMEOUT_MS="21600000"
STREAM_IDLE_TIMEOUT_MS="21600000"
AUTO_COMPACT_WINDOW="${CLAUDE_CODE_AUTO_COMPACT_WINDOW:-200000}"
AUTO_COMPACT_PCT="${CLAUDE_AUTOCOMPACT_PCT_OVERRIDE:-70}"
DENY_AGENT_TOOL="0"
SETTINGS_PATH="${HOME:?HOME is required}/.claude/settings.json"

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
    --deny-agent-tool)
      DENY_AGENT_TOOL="1"
      shift
      ;;
    --path)
      SETTINGS_PATH="${2:?missing value for --path}"
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

export SETTINGS_PATH GATEWAY_BASE_URL MODEL SMALL_FAST_MODEL AUTH_TOKEN API_TIMEOUT_MS STREAM_IDLE_TIMEOUT_MS AUTO_COMPACT_WINDOW AUTO_COMPACT_PCT DENY_AGENT_TOOL
python3 <<'PY'
import json
import os
from pathlib import Path

path = Path(os.environ["SETTINGS_PATH"]).expanduser()
path.parent.mkdir(parents=True, exist_ok=True)
if path.exists():
    backup = path.with_name(path.name + ".codex-gate-backup-" + __import__("datetime").datetime.utcnow().strftime("%Y%m%d%H%M%S"))
    backup.write_bytes(path.read_bytes())
    print(f"backup={backup}")
    try:
        config = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"{path} is not valid JSON: {exc}")
else:
    config = {}

if not isinstance(config, dict):
    raise SystemExit(f"{path} must contain a JSON object")

env = config.get("env")
if env is None:
    env = {}
elif not isinstance(env, dict):
    raise SystemExit(f"{path}.env must be a JSON object")

base_url = os.environ["GATEWAY_BASE_URL"].rstrip("/")
auth_token = os.environ["AUTH_TOKEN"]

def parse_int(name, option):
    try:
        return int(os.environ[name])
    except ValueError:
        raise SystemExit(f"{option} must be an integer")

auto_compact_window = parse_int("AUTO_COMPACT_WINDOW", "--auto-compact-window")
auto_compact_pct = parse_int("AUTO_COMPACT_PCT", "--auto-compact-pct")
if auto_compact_window <= 0:
    raise SystemExit("--auto-compact-window must be a positive integer")
if auto_compact_pct < 1 or auto_compact_pct > 99:
    raise SystemExit("--auto-compact-pct must be between 1 and 99")
env.update({
    "ANTHROPIC_BASE_URL": base_url,
    "ANTHROPIC_AUTH_TOKEN": auth_token,
    "ANTHROPIC_MODEL": os.environ["MODEL"],
    "ANTHROPIC_SMALL_FAST_MODEL": os.environ["SMALL_FAST_MODEL"],
    "API_TIMEOUT_MS": os.environ["API_TIMEOUT_MS"],
    "CLAUDE_STREAM_IDLE_TIMEOUT_MS": os.environ["STREAM_IDLE_TIMEOUT_MS"],
    "CLAUDE_CODE_AUTO_COMPACT_WINDOW": str(auto_compact_window),
    "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": str(auto_compact_pct),
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": "1",
})
env.pop("ANTHROPIC_API_KEY", None)
config["env"] = env
config["model"] = os.environ["MODEL"]

if os.environ.get("DENY_AGENT_TOOL") == "1":
    permissions = config.get("permissions")
    if permissions is None:
        permissions = {}
    elif not isinstance(permissions, dict):
        raise SystemExit(f"{path}.permissions must be a JSON object")
    deny = permissions.get("deny")
    if deny is None:
        deny = []
    elif not isinstance(deny, list):
        raise SystemExit(f"{path}.permissions.deny must be a JSON array")
    for rule in ("Agent", "Task"):
        if rule not in deny:
            deny.append(rule)
    permissions["deny"] = deny
    config["permissions"] = permissions
else:
    permissions = config.get("permissions")
    if isinstance(permissions, dict):
        deny = permissions.get("deny")
        if isinstance(deny, list):
            permissions["deny"] = [
                item for item in deny
                if str(item) not in {"Agent", "Task"}
            ]
        config["permissions"] = permissions

path.write_text(json.dumps(config, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY

echo "OK: wrote Claude Code direct settings"
echo "path=$SETTINGS_PATH"
echo "anthropic_base_url=${GATEWAY_BASE_URL%/}"
echo "model=$MODEL"
echo "small_fast_model=$SMALL_FAST_MODEL"
echo "api_timeout_ms=$API_TIMEOUT_MS"
echo "stream_idle_timeout_ms=$STREAM_IDLE_TIMEOUT_MS"
echo "auto_compact_window=$AUTO_COMPACT_WINDOW"
echo "auto_compact_pct=$AUTO_COMPACT_PCT"
echo "auth_mode=auth_token"
echo "api_key_env=removed_from_settings"
if [ "$DENY_AGENT_TOOL" = "1" ]; then
  echo "permissions_deny_agent_tool=true"
else
  echo "permissions_agent_tool=allowed"
fi
echo "command=claude"
