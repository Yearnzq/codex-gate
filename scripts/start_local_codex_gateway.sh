#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/start_local_codex_gateway.sh [options]

Start a local gateway for Claude Code / CCR.

Options:
  --host HOST             Listen host, default: 127.0.0.1
  --port PORT             Listen port, default: 18080
  --state-dir DIR         State/log directory, default: /tmp/codex-gate-local-codex-gateway
  --backend MODE          Backend mode: codex-web or cli, default: codex-web
                          codex-web reads CODEX_WEB_ACCESS_TOKEN or CODEX_ACCESS_TOKEN;
                          if missing in an interactive terminal, it prompts hidden input
  --codex-token-command CMD
                          Trusted local command that prints an access token on stdout;
                          used only when CODEX_WEB_ACCESS_TOKEN/CODEX_ACCESS_TOKEN is absent
                          default auto-detects $HOME/.local/bin/codex-access-token
  --codex-command PATH    Codex command path, default: codex
  --codex-model MODEL     Override upstream model; cli mode passes it to "codex exec -m"
  --reasoning-effort LVL  Reasoning effort: low, medium, high, xhigh
  --model-speed SPEED     CLI backend speed tier: standard or fast; codex-web does not send service_tier by default
  --codex-web-stream-start-retries N
                          Retry a codex-web stream only if it fails before any upstream event, default: 1
  --codex-web-stream-resume
                          Experimental: resume partial codex-web streams with starting_after when supported
  --codex-web-no-stream-resume
                          Disable experimental codex-web stream resume
  --codex-web-stream-resume-retries N
                          Experimental resume retries, default: 1
  --codex-workdir DIR     Working root passed to "codex exec -C", default: current directory
  --codex-sandbox MODE    Sandbox mode; setting this disables default bypass mode
  --codex-add-dir DIR     Additional writable directory passed to codex exec, repeatable
  --codex-approval POLICY Approval policy; setting this disables default bypass mode
  --codex-bypass-sandbox  Pass --dangerously-bypass-approvals-and-sandbox, default enabled
  --codex-no-bypass-sandbox
                          Do not pass --dangerously-bypass-approvals-and-sandbox
  --codex-timeout SECONDS Codex backend timeout in seconds, default: 21600; 0 disables inner timeout
  --configure-claude-code
                          Write ~/.claude/settings.json for direct Claude Code access
  --claude-settings PATH  Claude Code settings path, default: ~/.claude/settings.json
  --claude-model MODEL    Claude Code model, default: <codex-model> or gpt-5.5
  --claude-small-fast-model MODEL
                          Claude Code small/fast model, default: <codex-model> or gpt-5.5
  --claude-auth-token KEY Placeholder gateway bearer token, default: test-key-not-secret
  --claude-api-key KEY    Deprecated alias for --claude-auth-token
  --claude-api-timeout-ms MS
                          Claude Code API timeout, default: 21600000
  --claude-stream-idle-timeout-ms MS
                          Claude Code stream idle timeout, default: 21600000
  --claude-auto-compact-window TOKENS
                          Claude Code auto-compact window, default: 200000
  --claude-auto-compact-pct PCT
                          Claude Code auto-compact trigger percent, default: 70
  --claude-deny-agent-tool
                          Opt-in: write Claude Code permissions.deny for Agent/Task subagents
  --rebuild               Rebuild gateway binary when Go is available; otherwise use packaged binary if present
  --restart               Restart an existing gateway before starting
  --status                Show gateway status
  --stop                  Stop gateway
  -h, --help              Show this help
EOF
}

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
HOST="127.0.0.1"
PORT="18080"
STATE_DIR="${TMPDIR:-/tmp}/codex-gate-local-codex-gateway"
GATEWAY_BACKEND="${CODEX_GATEWAY_BACKEND:-${CODEX_BACKEND:-codex-web}}"
CODEX_ACCESS_TOKEN_COMMAND="${CODEX_ACCESS_TOKEN_COMMAND:-}"
DEFAULT_CODEX_ACCESS_TOKEN_COMMAND="${CODEX_DEFAULT_ACCESS_TOKEN_COMMAND:-${HOME:-}/.local/bin/codex-access-token}"
CODEX_COMMAND="${CODEX_COMMAND:-codex}"
CODEX_MODEL="${CODEX_MODEL:-}"
CODEX_REASONING_EFFORT="${CODEX_REASONING_EFFORT:-}"
CODEX_MODEL_SPEED="${CODEX_MODEL_SPEED:-standard}"
CODEX_WEB_STREAM_START_RETRIES="${CODEX_WEB_STREAM_START_RETRIES:-1}"
CODEX_WEB_STREAM_RESUME="${CODEX_WEB_STREAM_RESUME:-0}"
CODEX_WEB_STREAM_RESUME_RETRIES="${CODEX_WEB_STREAM_RESUME_RETRIES:-1}"
CODEX_WORKDIR="${CODEX_WORKDIR:-$(pwd)}"
CODEX_SANDBOX="${CODEX_SANDBOX:-read-only}"
CODEX_APPROVAL_POLICY="${CODEX_APPROVAL_POLICY:-}"
CODEX_BYPASS_SANDBOX="${CODEX_BYPASS_SANDBOX:-1}"
CODEX_ADD_DIRS="${CODEX_ADD_DIRS:-}"
CODEX_TIMEOUT_SECONDS="${CODEX_TIMEOUT_SECONDS:-21600}"
CODEX_TOKEN_SOURCE="missing"
CONFIGURE_CLAUDE_CODE="0"
CLAUDE_SETTINGS_PATH="${CLAUDE_SETTINGS_PATH:-${HOME:-}/.claude/settings.json}"
CLAUDE_CODE_MODEL="${CLAUDE_CODE_MODEL:-${ANTHROPIC_MODEL:-}}"
CLAUDE_SMALL_FAST_MODEL="${CLAUDE_SMALL_FAST_MODEL:-${ANTHROPIC_SMALL_FAST_MODEL:-}}"
CLAUDE_AUTH_TOKEN="${CLAUDE_AUTH_TOKEN:-test-key-not-secret}"
CLAUDE_API_TIMEOUT_MS="${CLAUDE_API_TIMEOUT_MS:-${API_TIMEOUT_MS:-21600000}}"
CLAUDE_STREAM_IDLE_TIMEOUT_MS="${CLAUDE_STREAM_IDLE_TIMEOUT_MS:-21600000}"
CLAUDE_AUTO_COMPACT_WINDOW="${CLAUDE_AUTO_COMPACT_WINDOW:-${CLAUDE_CODE_AUTO_COMPACT_WINDOW:-200000}}"
CLAUDE_AUTO_COMPACT_PCT="${CLAUDE_AUTO_COMPACT_PCT:-${CLAUDE_AUTOCOMPACT_PCT_OVERRIDE:-70}}"
CLAUDE_DENY_AGENT_TOOL="${CLAUDE_DENY_AGENT_TOOL:-0}"
ACTION="start"
REBUILD="0"
RESTART="0"
RUNTIME_OPTION_SET="0"
CREDENTIAL_OPTION_SET="0"

while [ "$#" -gt 0 ]; do
  arg="$(printf '%s' "$1" | tr -d '\r')"
  case "$arg" in
    --host)
      HOST="${2:?missing value for --host}"
      shift 2
      ;;
    --port)
      PORT="${2:?missing value for --port}"
      shift 2
      ;;
    --state-dir)
      STATE_DIR="${2:?missing value for --state-dir}"
      shift 2
      ;;
    --backend)
      GATEWAY_BACKEND="${2:?missing value for --backend}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-token-command)
      CODEX_ACCESS_TOKEN_COMMAND="${2:?missing value for --codex-token-command}"
      CREDENTIAL_OPTION_SET="1"
      shift 2
      ;;
    --codex-command)
      CODEX_COMMAND="${2:?missing value for --codex-command}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-model)
      CODEX_MODEL="${2:?missing value for --codex-model}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --reasoning-effort)
      CODEX_REASONING_EFFORT="${2:?missing value for --reasoning-effort}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --model-speed)
      CODEX_MODEL_SPEED="${2:?missing value for --model-speed}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-web-stream-start-retries)
      CODEX_WEB_STREAM_START_RETRIES="${2:?missing value for --codex-web-stream-start-retries}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-web-stream-resume)
      CODEX_WEB_STREAM_RESUME="1"
      RUNTIME_OPTION_SET="1"
      shift
      ;;
    --codex-web-no-stream-resume)
      CODEX_WEB_STREAM_RESUME="0"
      RUNTIME_OPTION_SET="1"
      shift
      ;;
    --codex-web-stream-resume-retries)
      CODEX_WEB_STREAM_RESUME_RETRIES="${2:?missing value for --codex-web-stream-resume-retries}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-workdir)
      CODEX_WORKDIR="${2:?missing value for --codex-workdir}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-sandbox)
      CODEX_SANDBOX="${2:?missing value for --codex-sandbox}"
      CODEX_BYPASS_SANDBOX="0"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-add-dir)
      add_dir="${2:?missing value for --codex-add-dir}"
      if [ -n "$CODEX_ADD_DIRS" ]; then
        CODEX_ADD_DIRS="$CODEX_ADD_DIRS
$add_dir"
      else
        CODEX_ADD_DIRS="$add_dir"
      fi
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-approval)
      CODEX_APPROVAL_POLICY="${2:?missing value for --codex-approval}"
      CODEX_BYPASS_SANDBOX="0"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --codex-bypass-sandbox)
      CODEX_BYPASS_SANDBOX="1"
      RUNTIME_OPTION_SET="1"
      shift
      ;;
    --codex-no-bypass-sandbox)
      CODEX_BYPASS_SANDBOX="0"
      RUNTIME_OPTION_SET="1"
      shift
      ;;
    --codex-timeout)
      CODEX_TIMEOUT_SECONDS="${2:?missing value for --codex-timeout}"
      RUNTIME_OPTION_SET="1"
      shift 2
      ;;
    --configure-claude-code)
      CONFIGURE_CLAUDE_CODE="1"
      shift
      ;;
    --claude-settings)
      CLAUDE_SETTINGS_PATH="${2:?missing value for --claude-settings}"
      CONFIGURE_CLAUDE_CODE="1"
      shift 2
      ;;
    --claude-model)
      CLAUDE_CODE_MODEL="${2:?missing value for --claude-model}"
      CONFIGURE_CLAUDE_CODE="1"
      shift 2
      ;;
    --claude-small-fast-model)
      CLAUDE_SMALL_FAST_MODEL="${2:?missing value for --claude-small-fast-model}"
      CONFIGURE_CLAUDE_CODE="1"
      shift 2
      ;;
    --claude-auth-token|--claude-api-key)
      CLAUDE_AUTH_TOKEN="${2:?missing value for $arg}"
      CONFIGURE_CLAUDE_CODE="1"
      shift 2
      ;;
    --claude-api-timeout-ms)
      CLAUDE_API_TIMEOUT_MS="${2:?missing value for --claude-api-timeout-ms}"
      CONFIGURE_CLAUDE_CODE="1"
      shift 2
      ;;
    --claude-stream-idle-timeout-ms)
      CLAUDE_STREAM_IDLE_TIMEOUT_MS="${2:?missing value for --claude-stream-idle-timeout-ms}"
      CONFIGURE_CLAUDE_CODE="1"
      shift 2
      ;;
    --claude-auto-compact-window)
      CLAUDE_AUTO_COMPACT_WINDOW="${2:?missing value for --claude-auto-compact-window}"
      CONFIGURE_CLAUDE_CODE="1"
      shift 2
      ;;
    --claude-auto-compact-pct)
      CLAUDE_AUTO_COMPACT_PCT="${2:?missing value for --claude-auto-compact-pct}"
      CONFIGURE_CLAUDE_CODE="1"
      shift 2
      ;;
    --claude-deny-agent-tool)
      CLAUDE_DENY_AGENT_TOOL="1"
      CONFIGURE_CLAUDE_CODE="1"
      shift
      ;;
    --rebuild)
      REBUILD="1"
      RUNTIME_OPTION_SET="1"
      shift
      ;;
    --restart)
      RESTART="1"
      shift
      ;;
    --status)
      ACTION="status"
      shift
      ;;
    --stop)
      ACTION="stop"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [ -z "$CLAUDE_CODE_MODEL" ]; then
  if [ -n "$CODEX_MODEL" ]; then
    CLAUDE_CODE_MODEL="$CODEX_MODEL"
  else
    CLAUDE_CODE_MODEL="gpt-5.5"
  fi
fi
if [ -z "$CLAUDE_SMALL_FAST_MODEL" ]; then
  if [ -n "$CODEX_MODEL" ]; then
    CLAUDE_SMALL_FAST_MODEL="$CODEX_MODEL"
  else
    CLAUDE_SMALL_FAST_MODEL="gpt-5.5"
  fi
fi

STATE_PATH="$STATE_DIR/state.env"
CONFIG_PATH="$STATE_DIR/gateway.toml"
GATEWAY_BIN="$STATE_DIR/gateway"
RUNNER_PATH="$STATE_DIR/run_codex_cli.sh"
STDOUT_PATH="$STATE_DIR/gateway.out.log"
STDERR_PATH="$STATE_DIR/gateway.err.log"

is_running() {
  pid="$1"
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

load_state() {
  [ -f "$STATE_PATH" ] || return 0
  while IFS= read -r state_line || [ -n "$state_line" ]; do
    case "$state_line" in
      ""|\#*) continue ;;
      *=*) ;;
      *)
        echo "invalid gateway state line in $STATE_PATH" >&2
        return 1
        ;;
    esac
    state_key="${state_line%%=*}"
    state_value="${state_line#*=}"
    case "$state_value" in
      \'*\')
        state_value="${state_value#\'}"
        state_value="${state_value%\'}"
        ;;
    esac
    case "$state_key" in
      pid) pid="$state_value" ;;
      base_url) base_url="$state_value" ;;
      gateway_backend) gateway_backend="$state_value" ;;
      codex_command) codex_command="$state_value" ;;
      codex_model) codex_model="$state_value" ;;
      reasoning_effort) reasoning_effort="$state_value" ;;
      model_speed) model_speed="$state_value" ;;
      codex_web_stream_start_retries) codex_web_stream_start_retries="$state_value" ;;
      codex_web_stream_resume) codex_web_stream_resume="$state_value" ;;
      codex_web_stream_resume_retries) codex_web_stream_resume_retries="$state_value" ;;
      codex_workdir) codex_workdir="$state_value" ;;
      codex_sandbox) codex_sandbox="$state_value" ;;
      codex_approval_policy) codex_approval_policy="$state_value" ;;
      codex_bypass_sandbox) codex_bypass_sandbox="$state_value" ;;
      codex_add_dirs) codex_add_dirs="$state_value" ;;
      codex_timeout_seconds) codex_timeout_seconds="$state_value" ;;
      config) config="$state_value" ;;
      binary) binary="$state_value" ;;
      runner) runner="$state_value" ;;
      stdout) stdout="$state_value" ;;
      stderr) stderr="$state_value" ;;
      started_at) started_at="$state_value" ;;
      *) ;;
    esac
  done <"$STATE_PATH"
}

write_state_line() {
  key="$1"
  value="$2"
  clean_value="$(printf '%s' "$value" | tr '\r\n' '  ')"
  printf '%s=%s\n' "$key" "$clean_value"
}

configure_claude_code() {
  gateway_base_url="${1:?missing gateway base url}"
  agent_tool_args=""
  if [ "$CLAUDE_DENY_AGENT_TOOL" = "1" ]; then
    agent_tool_args="--deny-agent-tool"
  fi
  # shellcheck disable=SC2086
  sh "$ROOT/scripts/write_claude_code_direct_settings.sh" \
    --gateway-base-url "$gateway_base_url" \
    --model "$CLAUDE_CODE_MODEL" \
    --small-fast-model "$CLAUDE_SMALL_FAST_MODEL" \
    --auth-token "$CLAUDE_AUTH_TOKEN" \
    --api-timeout-ms "$CLAUDE_API_TIMEOUT_MS" \
    --stream-idle-timeout-ms "$CLAUDE_STREAM_IDLE_TIMEOUT_MS" \
    --auto-compact-window "$CLAUDE_AUTO_COMPACT_WINDOW" \
    --auto-compact-pct "$CLAUDE_AUTO_COMPACT_PCT" \
    --path "$CLAUDE_SETTINGS_PATH" \
    $agent_tool_args
}

prompt_codex_access_token() {
  [ -t 0 ] || return 1

  printf '%s' "Enter CODEX access token (hidden, not saved): " >&2
  old_stty="$(stty -g 2>/dev/null || true)"
  if [ -n "$old_stty" ]; then
    stty -echo
  fi
  prompted_token=""
  IFS= read -r prompted_token || prompted_token=""
  if [ -n "$old_stty" ]; then
    stty "$old_stty"
  fi
  printf '\n' >&2

  if [ -z "$prompted_token" ]; then
    return 1
  fi
  CODEX_ACCESS_TOKEN="$prompted_token"
  export CODEX_ACCESS_TOKEN
  CODEX_TOKEN_SOURCE="prompt"
  return 0
}

load_codex_access_token_command() {
  [ -n "$CODEX_ACCESS_TOKEN_COMMAND" ] || return 1

  set +e
  if [ -x "$CODEX_ACCESS_TOKEN_COMMAND" ]; then
    token_output="$("$CODEX_ACCESS_TOKEN_COMMAND" 2>/dev/null)"
  else
    token_output="$(sh -c "$CODEX_ACCESS_TOKEN_COMMAND" 2>/dev/null)"
  fi
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    echo "CODEX_ACCESS_TOKEN_COMMAND failed" >&2
    return 1
  fi

  token_output="$(printf '%s\n' "$token_output" | tr -d '\r' | sed -n '/./{p;q;}')"
  if [ -z "$token_output" ]; then
    echo "CODEX_ACCESS_TOKEN_COMMAND returned no token" >&2
    return 1
  fi
  CODEX_ACCESS_TOKEN="$token_output"
  export CODEX_ACCESS_TOKEN
  CODEX_TOKEN_SOURCE="token_helper"
  return 0
}

if [ "$ACTION" = "status" ]; then
  if [ ! -f "$STATE_PATH" ]; then
    echo "status=stopped"
    exit 0
  fi
  pid=""
  base_url=""
  gateway_backend=""
  codex_model=""
  reasoning_effort=""
  model_speed=""
  codex_web_stream_start_retries=""
  codex_web_stream_resume=""
  codex_web_stream_resume_retries=""
  codex_workdir=""
  codex_sandbox=""
  codex_approval_policy=""
  codex_bypass_sandbox=""
  codex_add_dirs=""
  codex_timeout_seconds=""
  stdout=""
  stderr=""
  load_state
  if is_running "$pid"; then
    echo "status=running"
    echo "pid=$pid"
    echo "base_url=$base_url"
    echo "backend=${gateway_backend:-cli}"
    if [ -n "$codex_model" ]; then
      echo "codex_model=$codex_model"
    else
      echo "codex_model=<codex-cli-config-default>"
    fi
    if [ -n "$reasoning_effort" ]; then
      echo "reasoning_effort=$reasoning_effort"
    else
      echo "reasoning_effort=<codex-cli-config-default>"
    fi
    if [ -n "$model_speed" ]; then
      echo "model_speed=$model_speed"
    else
      echo "model_speed=standard"
    fi
    if [ "${gateway_backend:-cli}" = "codex-web" ] && [ "${model_speed:-standard}" = "fast" ]; then
      echo "note=codex-web does not send service_tier=fast because upstream rejects it"
    fi
    if [ "${gateway_backend:-cli}" = "codex-web" ]; then
      echo "codex_web_stream_start_retries=${codex_web_stream_start_retries:-1}"
      echo "codex_web_stream_resume=${codex_web_stream_resume:-0}"
      echo "codex_web_stream_resume_retries=${codex_web_stream_resume_retries:-1}"
    fi
    if [ -n "$codex_workdir" ]; then
      echo "codex_workdir=$codex_workdir"
    else
      echo "codex_workdir=$CODEX_WORKDIR"
    fi
    if [ -n "$codex_bypass_sandbox" ] && [ "$codex_bypass_sandbox" = "1" ]; then
      echo "codex_bypass_sandbox=true"
    else
      echo "codex_sandbox=${codex_sandbox:-read-only}"
      if [ -n "$codex_approval_policy" ]; then
        echo "codex_approval_policy=$codex_approval_policy"
      fi
    fi
    if [ -n "$codex_add_dirs" ]; then
      printf '%s\n' "$codex_add_dirs" | sed 's/^/codex_add_dir=/'
    fi
    echo "codex_timeout_seconds=${codex_timeout_seconds:-21600}"
    echo "anthropic_base_url=$base_url"
    echo "chat_completions_url=$base_url/v1/chat/completions"
    echo "stdout=$stdout"
    echo "stderr=$stderr"
  else
    echo "status=stopped"
    echo "stale_state=$STATE_PATH"
  fi
  exit 0
fi

if [ "$ACTION" = "stop" ]; then
  stopped="0"
  if [ -f "$STATE_PATH" ]; then
    pid=""
    load_state
    if is_running "$pid"; then
      kill "$pid" 2>/dev/null || true
      sleep 1
      if is_running "$pid"; then
        kill -9 "$pid" 2>/dev/null || true
      fi
      stopped="1"
    fi
    rm -f "$STATE_PATH"
  fi
  if [ "$stopped" = "1" ]; then
    echo "OK: stopped local Codex gateway"
  else
    echo "OK: local Codex gateway was not running"
  fi
  exit 0
fi

case "$PORT" in
  ''|*[!0-9]*)
    echo "port must be a number: $PORT" >&2
    exit 2
    ;;
esac
if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
  echo "port must be between 1 and 65535: $PORT" >&2
  exit 2
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required" >&2
  exit 1
fi
if [ -z "$CODEX_ACCESS_TOKEN_COMMAND" ] &&
  [ -n "$DEFAULT_CODEX_ACCESS_TOKEN_COMMAND" ] &&
  [ -x "$DEFAULT_CODEX_ACCESS_TOKEN_COMMAND" ]; then
  CODEX_ACCESS_TOKEN_COMMAND="$DEFAULT_CODEX_ACCESS_TOKEN_COMMAND"
fi
if [ -n "${CODEX_WEB_ACCESS_TOKEN:-}" ]; then
  CODEX_TOKEN_SOURCE="CODEX_WEB_ACCESS_TOKEN"
elif [ -n "${CODEX_ACCESS_TOKEN:-}" ]; then
  CODEX_TOKEN_SOURCE="CODEX_ACCESS_TOKEN"
fi
case "$GATEWAY_BACKEND" in
  codex-web|codex_web|chatgpt-codex)
    GATEWAY_BACKEND="codex-web"
    if [ -z "${CODEX_WEB_ACCESS_TOKEN:-}" ] && [ -z "${CODEX_ACCESS_TOKEN:-}" ]; then
      if ! load_codex_access_token_command && ! prompt_codex_access_token; then
        echo "CODEX_BACKEND=codex-web requires CODEX_WEB_ACCESS_TOKEN or CODEX_ACCESS_TOKEN" >&2
        echo "This is the Claude-native mode where Claude Code keeps the tool loop." >&2
        echo "You may also pass --codex-token-command or set CODEX_ACCESS_TOKEN_COMMAND." >&2
        echo "Use --backend cli only for legacy codex-exec-agent mode." >&2
        exit 1
      fi
    fi
    ;;
  cli)
    if ! command -v "$CODEX_COMMAND" >/dev/null 2>&1; then
      echo "codex command not found: $CODEX_COMMAND" >&2
      exit 1
    fi
    if ! "$CODEX_COMMAND" login status >/dev/null 2>&1; then
      echo "codex CLI is not logged in. Run: codex login" >&2
      exit 1
    fi
    ;;
  *)
    echo "backend must be one of: codex-web, cli" >&2
    exit 2
    ;;
esac
case "$CODEX_MODEL" in
  *[!A-Za-z0-9._:-]*)
    echo "codex model contains unsupported characters: $CODEX_MODEL" >&2
    exit 2
    ;;
esac
case "$CODEX_REASONING_EFFORT" in
  ""|low|medium|high|xhigh)
    ;;
  *)
    echo "reasoning effort must be one of: low, medium, high, xhigh" >&2
    exit 2
    ;;
esac
case "$CODEX_MODEL_SPEED" in
  standard|fast)
    ;;
  "")
    CODEX_MODEL_SPEED="standard"
    ;;
  *)
    echo "model speed must be one of: standard, fast" >&2
    exit 2
    ;;
esac
case "$CODEX_WEB_STREAM_START_RETRIES" in
  ''|*[!0-9]*)
    echo "codex web stream start retries must be a non-negative integer: $CODEX_WEB_STREAM_START_RETRIES" >&2
    exit 2
    ;;
esac
case "$CODEX_WEB_STREAM_RESUME_RETRIES" in
  ''|*[!0-9]*)
    echo "codex web stream resume retries must be a non-negative integer: $CODEX_WEB_STREAM_RESUME_RETRIES" >&2
    exit 2
    ;;
esac
case "$CODEX_WEB_STREAM_RESUME" in
  0|1|true|false|yes|no|on|off)
    ;;
  *)
    echo "codex web stream resume must be boolean: $CODEX_WEB_STREAM_RESUME" >&2
    exit 2
    ;;
esac
case "$CODEX_SANDBOX" in
  read-only|workspace-write|danger-full-access)
    ;;
  *)
    echo "codex sandbox must be one of: read-only, workspace-write, danger-full-access" >&2
    exit 2
    ;;
esac
case "$CODEX_APPROVAL_POLICY" in
  ""|untrusted|on-failure|on-request|never)
    ;;
  *)
    echo "codex approval policy must be one of: untrusted, on-failure, on-request, never" >&2
    exit 2
    ;;
esac
case "$CODEX_BYPASS_SANDBOX" in
  0|1)
    ;;
  *)
    echo "CODEX_BYPASS_SANDBOX must be 0 or 1" >&2
    exit 2
    ;;
esac
case "$CODEX_TIMEOUT_SECONDS" in
  ''|*[!0-9]*)
    echo "codex timeout must be a non-negative integer number of seconds: $CODEX_TIMEOUT_SECONDS" >&2
    exit 2
    ;;
esac
if [ ! -d "$CODEX_WORKDIR" ]; then
  echo "codex workdir does not exist: $CODEX_WORKDIR" >&2
  exit 2
fi
if [ -n "$CODEX_ADD_DIRS" ]; then
  printf '%s\n' "$CODEX_ADD_DIRS" | while IFS= read -r add_dir; do
    [ -z "$add_dir" ] && continue
    if [ ! -d "$add_dir" ]; then
      echo "codex add-dir does not exist: $add_dir" >&2
      exit 2
    fi
  done
fi

mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR" 2>/dev/null || true

if [ -f "$STATE_PATH" ]; then
  pid=""
  base_url=""
  gateway_backend=""
  codex_command=""
  codex_model=""
  reasoning_effort=""
  model_speed=""
  codex_web_stream_start_retries=""
  codex_web_stream_resume=""
  codex_web_stream_resume_retries=""
  codex_workdir=""
  codex_sandbox=""
  codex_approval_policy=""
  codex_bypass_sandbox=""
  codex_add_dirs=""
  codex_timeout_seconds=""
  load_state
  if is_running "$pid"; then
    state_backend="${gateway_backend:-cli}"
    state_model_speed="${model_speed:-standard}"
    state_stream_start_retries="${codex_web_stream_start_retries:-1}"
    state_stream_resume="${codex_web_stream_resume:-0}"
    state_stream_resume_retries="${codex_web_stream_resume_retries:-1}"
    state_workdir="${codex_workdir:-$CODEX_WORKDIR}"
    state_sandbox="${codex_sandbox:-read-only}"
    state_bypass="${codex_bypass_sandbox:-1}"
    state_timeout="${codex_timeout_seconds:-21600}"
    should_restart="$RESTART"
    if [ "$CREDENTIAL_OPTION_SET" = "1" ]; then
      should_restart="1"
    fi
    if [ "$RUNTIME_OPTION_SET" = "1" ]; then
      if [ "$state_backend" != "$GATEWAY_BACKEND" ] ||
        [ "${codex_command:-codex}" != "$CODEX_COMMAND" ] ||
        [ "${codex_model:-}" != "$CODEX_MODEL" ] ||
        [ "${reasoning_effort:-}" != "$CODEX_REASONING_EFFORT" ] ||
        [ "$state_model_speed" != "$CODEX_MODEL_SPEED" ] ||
        [ "$state_stream_start_retries" != "$CODEX_WEB_STREAM_START_RETRIES" ] ||
        [ "$state_stream_resume" != "$CODEX_WEB_STREAM_RESUME" ] ||
        [ "$state_stream_resume_retries" != "$CODEX_WEB_STREAM_RESUME_RETRIES" ] ||
        [ "$state_workdir" != "$CODEX_WORKDIR" ] ||
        [ "$state_sandbox" != "$CODEX_SANDBOX" ] ||
        [ "${codex_approval_policy:-}" != "$CODEX_APPROVAL_POLICY" ] ||
        [ "$state_bypass" != "$CODEX_BYPASS_SANDBOX" ] ||
        [ "${codex_add_dirs:-}" != "$CODEX_ADD_DIRS" ] ||
        [ "$state_timeout" != "$CODEX_TIMEOUT_SECONDS" ]; then
        should_restart="1"
      fi
    fi
    if [ "$should_restart" = "1" ]; then
      kill "$pid" 2>/dev/null || true
      sleep 1
      if is_running "$pid"; then
        kill -9 "$pid" 2>/dev/null || true
      fi
      rm -f "$STATE_PATH"
      if [ "$RESTART" = "1" ]; then
        echo "OK: stopped existing local Codex gateway"
      else
        echo "OK: runtime options changed; restarting local Codex gateway"
      fi
    else
      echo "OK: local Codex gateway already running"
      echo "pid=$pid"
      echo "base_url=$base_url"
      echo "backend=${gateway_backend:-cli}"
      if [ -n "$codex_model" ]; then
        echo "codex_model=$codex_model"
      else
        echo "codex_model=<codex-cli-config-default>"
      fi
      if [ -n "$reasoning_effort" ]; then
        echo "reasoning_effort=$reasoning_effort"
      else
        echo "reasoning_effort=<codex-cli-config-default>"
      fi
      if [ -n "$model_speed" ]; then
        echo "model_speed=$model_speed"
      else
        echo "model_speed=standard"
      fi
      if [ "${gateway_backend:-cli}" = "codex-web" ] && [ "${model_speed:-standard}" = "fast" ]; then
        echo "note=codex-web does not send service_tier=fast because upstream rejects it"
      fi
      if [ "${gateway_backend:-cli}" = "codex-web" ]; then
        echo "codex_web_stream_start_retries=${codex_web_stream_start_retries:-1}"
        echo "codex_web_stream_resume=${codex_web_stream_resume:-0}"
        echo "codex_web_stream_resume_retries=${codex_web_stream_resume_retries:-1}"
      fi
      if [ -n "$codex_workdir" ]; then
        echo "codex_workdir=$codex_workdir"
      else
        echo "codex_workdir=$CODEX_WORKDIR"
      fi
      if [ -n "$codex_bypass_sandbox" ] && [ "$codex_bypass_sandbox" = "1" ]; then
        echo "codex_bypass_sandbox=true"
      else
        echo "codex_sandbox=${codex_sandbox:-read-only}"
        if [ -n "$codex_approval_policy" ]; then
          echo "codex_approval_policy=$codex_approval_policy"
        fi
      fi
      if [ -n "$codex_add_dirs" ]; then
        printf '%s\n' "$codex_add_dirs" | sed 's/^/codex_add_dir=/'
      fi
      echo "codex_timeout_seconds=${codex_timeout_seconds:-21600}"
      echo "hint=use --restart to force a restart"
      if [ "$CONFIGURE_CLAUDE_CODE" = "1" ]; then
        configure_claude_code "$base_url"
        echo "claude_command=claude"
        echo "claude_wrapper_command=sh $ROOT/scripts/run_claude_code_direct.sh --gateway-base-url $base_url"
      fi
      exit 0
    fi
  fi
fi

python3 - "$HOST" "$PORT" <<'PY'
from pathlib import Path
import socket
import sys

host = sys.argv[1]
port = int(sys.argv[2])

def listener_inodes(listen_port):
    inodes = set()
    for table in (Path("/proc/net/tcp"), Path("/proc/net/tcp6")):
        if not table.exists():
            continue
        try:
            lines = table.read_text(encoding="utf-8", errors="replace").splitlines()[1:]
        except OSError:
            continue
        for line in lines:
            fields = line.split()
            if len(fields) < 10:
                continue
            local_address = fields[1]
            state = fields[3]
            inode = fields[9]
            try:
                observed_port = int(local_address.rsplit(":", 1)[1], 16)
            except (IndexError, ValueError):
                continue
            if observed_port == listen_port and state == "0A":
                inodes.add(inode)
    return inodes

def listener_processes(inodes):
    if not inodes:
        return []
    listeners = []
    for proc in Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        fd_dir = proc / "fd"
        try:
            fds = list(fd_dir.iterdir())
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
        for fd in fds:
            try:
                target = fd.readlink()
            except OSError:
                continue
            text = str(target)
            if not text.startswith("socket:[") or text[8:-1] not in inodes:
                continue
            try:
                comm = (proc / "comm").read_text(encoding="utf-8", errors="replace").strip()
            except OSError:
                comm = "<unknown>"
            listeners.append((int(proc.name), comm or "<unknown>"))
            break
    return sorted(set(listeners))

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    sock.bind((host, port))
except OSError as exc:
    print(f"port {host}:{port} is not available: {exc}", file=sys.stderr)
    print("port_diagnostic=procfs", file=sys.stderr)
    listeners = listener_processes(listener_inodes(port))
    if listeners:
        for pid, comm in listeners:
            print(f"listener_pid={pid} listener_comm={comm}", file=sys.stderr)
    else:
        print("listener_pid=<not-found> listener_comm=<not-found>", file=sys.stderr)
    print("next_status_command=sh scripts/start_local_codex_gateway.sh --status", file=sys.stderr)
    print("next_stop_command=sh scripts/start_local_codex_gateway.sh --stop", file=sys.stderr)
    print(f"next_port_command=sh scripts/start_local_codex_gateway.sh --port {port + 1}", file=sys.stderr)
    print("note=--restart only stops a gateway recorded in this script state file; it will not kill unrelated listeners", file=sys.stderr)
    raise SystemExit(1)
finally:
    sock.close()
PY

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

target="$(detect_target)"
release_bin="$ROOT/dist/gateway/$target/gateway"
release_bin_stale() {
  [ -f "$release_bin" ] || return 1
  if [ "$ROOT/go.mod" -nt "$release_bin" ]; then
    return 0
  fi
  if [ -f "$ROOT/go.sum" ] && [ "$ROOT/go.sum" -nt "$release_bin" ]; then
    return 0
  fi
  [ -n "$(find "$ROOT/cmd" "$ROOT/internal" -type f -name '*.go' -newer "$release_bin" -print -quit 2>/dev/null)" ]
}

refresh_gateway="0"
RELEASE_BIN_STALE="0"
if release_bin_stale; then
  RELEASE_BIN_STALE="1"
  refresh_gateway="1"
fi
if [ "$REBUILD" = "1" ] || [ ! -x "$GATEWAY_BIN" ]; then
  refresh_gateway="1"
fi
if [ "$RESTART" = "1" ] && [ -f "$release_bin" ]; then
  refresh_gateway="1"
fi
if [ "$refresh_gateway" = "1" ]; then
  if [ -f "$release_bin" ] && { [ "$REBUILD" != "1" ] || ! command -v go >/dev/null 2>&1; }; then
    if [ "$REBUILD" = "1" ] && ! command -v go >/dev/null 2>&1; then
      echo "WARNING: --rebuild requested but go is not available; using packaged gateway binary for $target" >&2
    fi
    if [ "$RELEASE_BIN_STALE" = "1" ] && ! command -v go >/dev/null 2>&1; then
      echo "WARNING: packaged gateway binary is older than Go source for $target; install Go to rebuild, or use a release built after the latest source changes" >&2
    fi
    cp "$release_bin" "$GATEWAY_BIN"
    chmod +x "$GATEWAY_BIN"
  else
    if ! command -v go >/dev/null 2>&1; then
      echo "gateway binary not found for $target and go is not available" >&2
      echo "install Go or provide $release_bin" >&2
      exit 1
    fi
    (cd "$ROOT" && go build -trimpath -o "$GATEWAY_BIN" ./cmd/gateway)
  fi
fi

cat >"$CONFIG_PATH" <<EOF
[gateway]
host = "$HOST"
port = $PORT
log_level = "info"
redact_logs = true
allow_wide_bind = false
EOF

cat >"$RUNNER_PATH" <<'EOF'
#!/usr/bin/env sh
set -eu

tmp="$(mktemp)"
log="$(mktemp)"
cleanup() {
  rm -f "$tmp" "$log"
}
trap cleanup EXIT

codex_cmd="${CODEX_LOCAL_COMMAND:-codex}"
codex_model="${CODEX_LOCAL_MODEL:-}"
codex_reasoning_effort="${CODEX_LOCAL_REASONING_EFFORT:-}"
codex_service_tier="${CODEX_LOCAL_SERVICE_TIER:-}"
codex_workdir="${CODEX_LOCAL_WORKDIR:-$(pwd)}"
codex_sandbox="${CODEX_LOCAL_SANDBOX:-read-only}"
codex_approval_policy="${CODEX_LOCAL_APPROVAL_POLICY:-}"
codex_bypass_sandbox="${CODEX_LOCAL_BYPASS_SANDBOX:-1}"
codex_add_dirs="${CODEX_LOCAL_ADD_DIRS:-}"
codex_timeout_seconds="${CODEX_LOCAL_TIMEOUT_SECONDS:-21600}"
stream_mode="${CODEX_HARNESS_STREAM:-0}"
set --
if [ "$codex_bypass_sandbox" = "1" ]; then
  set -- "$@" --dangerously-bypass-approvals-and-sandbox
elif [ -n "$codex_approval_policy" ]; then
  set -- "$@" -a "$codex_approval_policy"
fi
set -- "$@" exec
if [ -n "$codex_model" ]; then
  set -- "$@" -m "$codex_model"
fi
if [ -n "$codex_reasoning_effort" ]; then
  set -- "$@" -c "model_reasoning_effort=\"$codex_reasoning_effort\""
fi
if [ -n "$codex_service_tier" ]; then
  set -- "$@" -c "service_tier=\"$codex_service_tier\""
fi
set -- "$@" --ephemeral --skip-git-repo-check
if [ "$codex_bypass_sandbox" != "1" ]; then
  set -- "$@" -s "$codex_sandbox"
fi
set -- "$@" -C "$codex_workdir"
if [ -n "$codex_add_dirs" ]; then
  while IFS= read -r add_dir; do
    [ -z "$add_dir" ] && continue
    set -- "$@" --add-dir "$add_dir"
  done <<EOF_ADD_DIRS
$codex_add_dirs
EOF_ADD_DIRS
fi
set -- "$@" --output-last-message "$tmp"
if [ "$stream_mode" = "1" ]; then
  set -- "$@" --json
fi
set -- "$@" -
set +e
if [ "$stream_mode" = "1" ]; then
  if [ "$codex_timeout_seconds" -gt 0 ] && command -v timeout >/dev/null 2>&1; then
    timeout "${codex_timeout_seconds}s" "$codex_cmd" "$@" 2>"$log"
  else
    "$codex_cmd" "$@" 2>"$log"
  fi
else
  if [ "$codex_timeout_seconds" -gt 0 ] && command -v timeout >/dev/null 2>&1; then
    timeout "${codex_timeout_seconds}s" "$codex_cmd" "$@" >"$log" 2>&1
  else
    "$codex_cmd" "$@" >"$log" 2>&1
  fi
fi
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  if [ "$stream_mode" != "1" ]; then
    cat "$tmp"
  fi
else
  cat "$log" >&2
fi
exit "$rc"
EOF
chmod +x "$RUNNER_PATH"

CLI_ARGS_JSON="$(python3 - "$RUNNER_PATH" <<'PY'
import json
import sys
print(json.dumps([sys.argv[1]]))
PY
)"

if [ "$CODEX_TIMEOUT_SECONDS" -gt 0 ]; then
  CODEX_GATEWAY_TIMEOUT_SECONDS=$((CODEX_TIMEOUT_SECONDS + 60))
  CODEX_CLI_TIMEOUT_VALUE="${CODEX_GATEWAY_TIMEOUT_SECONDS}s"
else
  CODEX_CLI_TIMEOUT_VALUE="8760h"
fi

(
  export CODEX_BACKEND="$GATEWAY_BACKEND"
  if [ -n "${CODEX_WEB_ACCESS_TOKEN:-}" ]; then
    export CODEX_WEB_ACCESS_TOKEN
  fi
  if [ -n "${CODEX_ACCESS_TOKEN:-}" ]; then
    export CODEX_ACCESS_TOKEN
  fi
  if [ -n "${CODEX_CHATGPT_ACCOUNT_ID:-}" ]; then
    export CODEX_CHATGPT_ACCOUNT_ID
  fi
  if [ -n "${CODEX_WEB_BASE_URL:-}" ]; then
    export CODEX_WEB_BASE_URL
  fi
  export CODEX_WEB_REASONING_EFFORT="$CODEX_REASONING_EFFORT"
  export CODEX_WEB_TIMEOUT_SECONDS="$CODEX_TIMEOUT_SECONDS"
  export CODEX_WEB_STREAM_START_RETRIES="$CODEX_WEB_STREAM_START_RETRIES"
  export CODEX_WEB_STREAM_RESUME="$CODEX_WEB_STREAM_RESUME"
  export CODEX_WEB_STREAM_RESUME_RETRIES="$CODEX_WEB_STREAM_RESUME_RETRIES"
  export CODEX_CLI_COMMAND="sh"
  export CODEX_CLI_ARGS_JSON="$CLI_ARGS_JSON"
  export CODEX_CLI_TIMEOUT="$CODEX_CLI_TIMEOUT_VALUE"
  export CODEX_LOCAL_COMMAND="$CODEX_COMMAND"
  export CODEX_LOCAL_MODEL="$CODEX_MODEL"
  export CODEX_LOCAL_REASONING_EFFORT="$CODEX_REASONING_EFFORT"
  export CODEX_LOCAL_WORKDIR="$CODEX_WORKDIR"
  export CODEX_LOCAL_SANDBOX="$CODEX_SANDBOX"
  export CODEX_LOCAL_APPROVAL_POLICY="$CODEX_APPROVAL_POLICY"
  export CODEX_LOCAL_BYPASS_SANDBOX="$CODEX_BYPASS_SANDBOX"
  export CODEX_LOCAL_ADD_DIRS="$CODEX_ADD_DIRS"
  export CODEX_LOCAL_TIMEOUT_SECONDS="$CODEX_TIMEOUT_SECONDS"
  if [ "$CODEX_MODEL_SPEED" = "fast" ]; then
    export CODEX_LOCAL_SERVICE_TIER="fast"
  else
    export CODEX_LOCAL_SERVICE_TIER=""
  fi
  exec "$GATEWAY_BIN" -config "$CONFIG_PATH"
) >"$STDOUT_PATH" 2>"$STDERR_PATH" &
pid="$!"

BASE_URL="http://$HOST:$PORT"
if ! python3 - "$BASE_URL/healthz" "$pid" <<'PY'
import json
import os
import sys
import time
import urllib.request

url = sys.argv[1]
pid = int(sys.argv[2])
deadline = time.time() + 10
while time.time() < deadline:
    try:
        with urllib.request.urlopen(url, timeout=0.5) as response:
            payload = json.loads(response.read().decode("utf-8"))
            if payload.get("status") == "ok":
                raise SystemExit(0)
    except Exception:
        pass
    try:
        os.kill(pid, 0)
    except OSError:
        raise SystemExit("gateway process exited before health check passed")
    time.sleep(0.2)
raise SystemExit("gateway did not become healthy within 10s")
PY
then
  kill "$pid" 2>/dev/null || true
  echo "gateway failed to start; stdout follows:" >&2
  tail -n 40 "$STDOUT_PATH" >&2 || true
  echo "gateway failed to start; stderr follows:" >&2
  tail -n 40 "$STDERR_PATH" >&2 || true
  exit 1
fi

{
  write_state_line "pid" "$pid"
  write_state_line "base_url" "$BASE_URL"
  write_state_line "gateway_backend" "$GATEWAY_BACKEND"
  write_state_line "codex_command" "$CODEX_COMMAND"
  write_state_line "codex_model" "$CODEX_MODEL"
  write_state_line "reasoning_effort" "$CODEX_REASONING_EFFORT"
  write_state_line "model_speed" "$CODEX_MODEL_SPEED"
  write_state_line "codex_web_stream_start_retries" "$CODEX_WEB_STREAM_START_RETRIES"
  write_state_line "codex_web_stream_resume" "$CODEX_WEB_STREAM_RESUME"
  write_state_line "codex_web_stream_resume_retries" "$CODEX_WEB_STREAM_RESUME_RETRIES"
  write_state_line "codex_workdir" "$CODEX_WORKDIR"
  write_state_line "codex_sandbox" "$CODEX_SANDBOX"
  write_state_line "codex_approval_policy" "$CODEX_APPROVAL_POLICY"
  write_state_line "codex_bypass_sandbox" "$CODEX_BYPASS_SANDBOX"
  write_state_line "codex_add_dirs" "$CODEX_ADD_DIRS"
  write_state_line "codex_timeout_seconds" "$CODEX_TIMEOUT_SECONDS"
  write_state_line "config" "$CONFIG_PATH"
  write_state_line "binary" "$GATEWAY_BIN"
  write_state_line "runner" "$RUNNER_PATH"
  write_state_line "stdout" "$STDOUT_PATH"
  write_state_line "stderr" "$STDERR_PATH"
  write_state_line "started_at" "$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
} >"$STATE_PATH"
chmod 600 "$STATE_PATH" 2>/dev/null || true

echo "OK: started local Codex gateway"
echo "pid=$pid"
echo "base_url=$BASE_URL"
echo "backend=$GATEWAY_BACKEND"
echo "health_url=$BASE_URL/healthz"
echo "version_url=$BASE_URL/version"
echo "codex_token_source=$CODEX_TOKEN_SOURCE"
echo "next_check_command=sh $ROOT/scripts/check_local_codex_gateway.sh --base-url $BASE_URL"
echo "agent_task_tools=allowed_by_default"
echo "agent_task_disable_command=claude --disallowedTools Agent --disallowedTools Task"
if [ -n "$CODEX_MODEL" ]; then
  echo "codex_model=$CODEX_MODEL"
else
  echo "codex_model=<codex-cli-config-default>"
fi
if [ -n "$CODEX_REASONING_EFFORT" ]; then
  echo "reasoning_effort=$CODEX_REASONING_EFFORT"
else
  echo "reasoning_effort=<codex-cli-config-default>"
fi
echo "model_speed=$CODEX_MODEL_SPEED"
if [ "$GATEWAY_BACKEND" = "codex-web" ] && [ "$CODEX_MODEL_SPEED" = "fast" ]; then
  echo "note=codex-web does not send service_tier=fast because upstream rejects it"
fi
if [ "$GATEWAY_BACKEND" = "codex-web" ]; then
  echo "codex_web_stream_start_retries=$CODEX_WEB_STREAM_START_RETRIES"
  echo "codex_web_stream_resume=$CODEX_WEB_STREAM_RESUME"
  echo "codex_web_stream_resume_retries=$CODEX_WEB_STREAM_RESUME_RETRIES"
fi
echo "codex_workdir=$CODEX_WORKDIR"
if [ "$CODEX_BYPASS_SANDBOX" = "1" ]; then
  echo "codex_bypass_sandbox=true"
else
  echo "codex_sandbox=$CODEX_SANDBOX"
  if [ -n "$CODEX_APPROVAL_POLICY" ]; then
    echo "codex_approval_policy=$CODEX_APPROVAL_POLICY"
  fi
fi
if [ -n "$CODEX_ADD_DIRS" ]; then
  printf '%s\n' "$CODEX_ADD_DIRS" | sed 's/^/codex_add_dir=/'
fi
echo "codex_timeout_seconds=$CODEX_TIMEOUT_SECONDS"
echo "anthropic_base_url=$BASE_URL"
echo "chat_completions_url=$BASE_URL/v1/chat/completions"
echo "ccr_provider_api_base_url=$BASE_URL/v1/chat/completions"
if [ "$CONFIGURE_CLAUDE_CODE" = "1" ]; then
  configure_claude_code "$BASE_URL"
  if [ "$CLAUDE_DENY_AGENT_TOOL" = "1" ]; then
    echo "claude_command=claude --disallowedTools Agent --disallowedTools Task"
    echo "claude_wrapper_command=sh $ROOT/scripts/run_claude_code_direct.sh --gateway-base-url $BASE_URL -- --disallowedTools Agent --disallowedTools Task"
  else
    echo "claude_agent_tool=allowed"
    echo "claude_command=claude"
    echo "claude_wrapper_command=sh $ROOT/scripts/run_claude_code_direct.sh --gateway-base-url $BASE_URL"
  fi
fi
if [ "$CONFIGURE_CLAUDE_CODE" != "1" ]; then
  echo "next_settings_command=sh $ROOT/scripts/write_claude_code_direct_settings.sh --gateway-base-url $BASE_URL"
  echo "next_claude_wrapper_command=sh $ROOT/scripts/run_claude_code_direct.sh --gateway-base-url $BASE_URL"
fi
echo "state=$STATE_PATH"
echo "stdout=$STDOUT_PATH"
echo "stderr=$STDERR_PATH"
