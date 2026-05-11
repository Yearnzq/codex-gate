# Codex Gate User Manual

This is the public user manual for Codex Gate.

Codex Gate is a local protocol gateway. It exposes Anthropic-compatible and OpenAI-compatible local endpoints, then forwards model work to a Codex-compatible upstream backend.

## Support Boundary

- Active backend: Codex only.
- Disabled: OpenClaw, Hermes, Claude backend fallback.
- Claude Code is treated as a client, not as an execution backend.
- The default release backend is `codex-web`.
- `codex-web` is experimental and uses the ChatGPT Codex backend path, not the official `api.openai.com/v1/responses` path.

## Requirements

- A built gateway binary for your platform, or a Go toolchain if you want to rebuild locally.
- A Codex Web access token provided through one of:
  - `CODEX_ACCESS_TOKEN`
  - `CODEX_WEB_ACCESS_TOKEN`
  - `--codex-token-command`
  - interactive hidden input from the startup script
- Optional: Claude Code or another Anthropic-compatible client.

Do not put tokens directly in shell history or committed config files. Prefer a local trusted token helper.

## Start The Gateway

```sh
sh scripts/start_local_codex_gateway.sh --port 18080
```

The startup script prints the selected backend, base URL, health URL, token source status, and next diagnostic command. It does not print token values.

To force a specific upstream model or reasoning effort:

```sh
sh scripts/start_local_codex_gateway.sh \
  --restart \
  --port 18080 \
  --codex-model gpt-5.5 \
  --reasoning-effort high
```

`--model-speed fast` is ignored for `codex-web` because the current upstream path rejects the corresponding service tier.

## Check The Gateway

```sh
sh scripts/check_local_codex_gateway.sh \
  --base-url http://127.0.0.1:18080 \
  --model gpt-5.5
```

The check script reports:

- gateway health
- `/version` result when available
- backend mode
- token variable/helper presence without exposing token values
- next command to run after a pass

## Configure Claude Code Direct Mode

Write user-level Claude Code settings:

```sh
sh scripts/write_claude_code_direct_settings.sh \
  --gateway-base-url http://127.0.0.1:18080
```

Then run Claude Code in your target repository:

```sh
claude
```

If your shell already has `ANTHROPIC_API_KEY`, clear it before using a local gateway token shape:

```sh
unset ANTHROPIC_API_KEY
```

Or use the compatibility wrapper:

```sh
sh scripts/run_claude_code_direct.sh \
  --gateway-base-url http://127.0.0.1:18080
```

The placeholder `ANTHROPIC_AUTH_TOKEN` is only for the local client-to-gateway connection. The upstream Codex credential remains inside the gateway process.

## Endpoints

Anthropic Messages:

```text
POST /v1/messages
POST /v1/messages/count_tokens
```

OpenAI-compatible chat completions:

```text
POST /v1/chat/completions
```

Health and version:

```text
GET /healthz
GET /version
```

## Release Packages

Codex Gate produces two package kinds:

- `codex-gate-runtime.zip`: user-facing runtime package with binaries, startup scripts, check scripts, license files, README files, and this manual.
- `codex-gate-dev.zip`: development package with source code, tests, fixtures, validation scripts, binaries, license files, README files, and this manual.

The public source tree and release packages intentionally exclude internal planning notes, private dogfood evidence, local agent prompts, PR templates, internal docs, and local workspace reports.

## Security Notes

- Do not commit `.env` files, access tokens, SSH keys, local auth files, or cloud credentials.
- Do not expose gateway ports beyond trusted local networks unless you add your own authentication and network controls.
- Treat `codex-web` as experimental.
- Review upstream account policy and terms before team or production use.
- Keep logs redacted. Gateway scripts should report whether credentials exist, never the credential values.

## Local Development Checks

```sh
python scripts/validate.py
python scripts/scan_secrets.py
python scripts/test_protocol_fixtures.py
python scripts/test_phase9_review_fixes.py
```

If Go is available:

```sh
go test ./...
```

Package checks:

```sh
python scripts/package_release.py --kind runtime --output dist/release/codex-gate-runtime.zip
python scripts/inspect_release_archive.py --kind runtime --archive dist/release/codex-gate-runtime.zip
python scripts/package_release.py --kind dev --output dist/release/codex-gate-dev.zip
python scripts/inspect_release_archive.py --kind dev --archive dist/release/codex-gate-dev.zip
```
