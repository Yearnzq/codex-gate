#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/install_codex_token_command.sh [options]

Install a local token-command helper for start_local_codex_gateway.sh.

The installed helper prints one Codex/ChatGPT access token to stdout. It can
optionally ask the local Codex app-server to refresh the stored login first.

Options:
  --output PATH       Helper path, default: $HOME/.local/bin/codex-access-token
  --auth-file PATH    Codex auth JSON path, default: ${CODEX_HOME:-$HOME/.codex}/auth.json
  --codex-command CMD Codex CLI command, default: codex
  --no-refresh        Do not call "codex app-server" before reading auth JSON
  --force             Overwrite existing helper
  -h, --help          Show this help
EOF
}

OUTPUT_PATH="${CODEX_TOKEN_COMMAND_PATH:-${HOME:-}/.local/bin/codex-access-token}"
CODEX_HOME_DIR="${CODEX_HOME:-${HOME:-}/.codex}"
AUTH_FILE="${CODEX_AUTH_FILE:-$CODEX_HOME_DIR/auth.json}"
CODEX_COMMAND="${CODEX_COMMAND:-codex}"
REFRESH="1"
FORCE="0"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --output)
      OUTPUT_PATH="${2:?missing value for --output}"
      shift 2
      ;;
    --auth-file)
      AUTH_FILE="${2:?missing value for --auth-file}"
      shift 2
      ;;
    --codex-command)
      CODEX_COMMAND="${2:?missing value for --codex-command}"
      shift 2
      ;;
    --no-refresh)
      REFRESH="0"
      shift
      ;;
    --force)
      FORCE="1"
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

if [ -z "$OUTPUT_PATH" ]; then
  echo "output path is required; HOME is not set" >&2
  exit 2
fi
if [ -e "$OUTPUT_PATH" ] && [ "$FORCE" != "1" ]; then
  echo "token command already exists: $OUTPUT_PATH" >&2
  echo "use --force to overwrite" >&2
  exit 1
fi

output_dir="$(dirname -- "$OUTPUT_PATH")"
mkdir -p "$output_dir"
chmod 700 "$output_dir" 2>/dev/null || true

tmp_path="$OUTPUT_PATH.tmp.$$"
cat >"$tmp_path" <<EOF
#!/usr/bin/env sh
set -eu

AUTH_FILE="\${CODEX_AUTH_FILE:-$AUTH_FILE}"
CODEX_COMMAND="\${CODEX_COMMAND:-$CODEX_COMMAND}"
REFRESH="\${CODEX_TOKEN_REFRESH:-$REFRESH}"

refresh_codex_auth() {
  [ "\$REFRESH" = "1" ] || return 0
  command -v "\$CODEX_COMMAND" >/dev/null 2>&1 || return 0
  "\$CODEX_COMMAND" app-server --help >/dev/null 2>&1 || return 0

  payload='{"method":"account/read","id":1,"params":{"refreshToken":true}}'
  if command -v timeout >/dev/null 2>&1; then
    printf '%s\n' "\$payload" | timeout 15 "\$CODEX_COMMAND" app-server --listen stdio >/dev/null 2>&1 || true
  else
    printf '%s\n' "\$payload" | "\$CODEX_COMMAND" app-server --listen stdio >/dev/null 2>&1 || true
  fi
}

read_token_with_jq() {
  jq -er '
    .tokens.access_token //
    .access_token //
    .token //
    .credentials.access_token //
    .auth.access_token //
    .oauth.access_token
    | select(type == "string" and length > 0)
  ' "\$AUTH_FILE"
}

read_token_with_python() {
  python3 - "\$AUTH_FILE" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as handle:
    data = json.load(handle)

def find(value, parts):
    current = value
    for part in parts:
        if not isinstance(current, dict) or part not in current:
            return None
        current = current[part]
    return current

for key_path in (
    ("tokens", "access_token"),
    ("access_token",),
    ("token",),
    ("credentials", "access_token"),
    ("auth", "access_token"),
    ("oauth", "access_token"),
):
    candidate = find(data, key_path)
    if isinstance(candidate, str) and candidate.strip():
        print(candidate.strip())
        raise SystemExit(0)

raise SystemExit("access token not found in auth JSON")
PY
}

refresh_codex_auth

if [ ! -f "\$AUTH_FILE" ]; then
  echo "Codex auth file not found: \$AUTH_FILE" >&2
  echo "Run codex login, or set CODEX_AUTH_FILE to the correct auth JSON path." >&2
  exit 1
fi

if command -v jq >/dev/null 2>&1; then
  read_token_with_jq
else
  read_token_with_python
fi
EOF

chmod 700 "$tmp_path"
mv "$tmp_path" "$OUTPUT_PATH"

echo "OK: installed Codex token command"
echo "path=$OUTPUT_PATH"
echo "auth_file=$AUTH_FILE"
echo "refresh=$REFRESH"
echo "usage=sh scripts/start_local_codex_gateway.sh --restart --port 18080 --codex-token-command $OUTPUT_PATH"
