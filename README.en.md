# Codex Gate

**Codex Gate** is a local protocol gateway that lets Anthropic-compatible clients such as Claude Code or CCR talk to Codex / OpenAI-compatible backends while preserving the client-side tool loop, streaming behavior, and multi-turn workflow as much as possible.

Chinese version: [README.md](README.md)

Codex Gate is an independent open-source project. It is not affiliated with OpenAI, Anthropic, or the official Claude Code project.

## What It Does

Codex Gate focuses on one job: translate local client requests into Codex-compatible upstream requests, then translate the upstream response back into Anthropic Messages / OpenAI-compatible responses.

Current default path:

```text
Claude Code / CCR
        |
        v
Codex Gate local gateway
        |
        v
Codex-compatible backend
```

The current release defaults to `codex-web`. This uses the ChatGPT Codex backend path and is intended for local experiments and controlled personal use. Compared with the official OpenAI Responses API path, it carries stability, account-policy, and terms-of-service risk. Whether the standard OpenAI Responses API should become the default backend is deferred to a later release decision.

## Features

- Anthropic Messages API compatible endpoint for Claude Code / CCR-style clients.
- OpenAI Chat Completions compatible endpoint for generic proxy clients and debugging.
- SSE streaming translation for text, tool-call events, and keepalive events.
- Codex Web token injection through environment variables, trusted token helpers, or interactive input.
- Local safety boundaries: tokens are not written to the repo, and scripts report token presence without printing token values.
- Runtime / Dev release split: a clean user package and a fuller audit/development package.
- Go runtime: the long-term gateway runtime is Go; Python is limited to validation, packaging, fixtures, and reporting scripts.

## Non-Goals

- No OpenClaw, Hermes, or external orchestration layer.
- No Claude Code backend fallback; Claude Code is a client, not an execution backend.
- No production writes, auto-deploys, auto-rollbacks, or cloud-resource mutation.
- No reading, printing, copying, or summarizing secrets.
- No bypassing tests, lint, benchmarks, or CI policy to create a false pass.

## Quick Start

### 1. Provide a Token

Set a Codex Web access token:

```sh
export CODEX_ACCESS_TOKEN="..."
```

Or install a local token helper:

```sh
sh scripts/install_codex_token_command.sh
```

The scripts do not write tokens into the repository. For real use, prefer a trusted local helper instead of putting tokens into shell history.

### 2. Start the Local Gateway

```sh
sh scripts/start_local_codex_gateway.sh --port 18080
```

On success, the script prints:

- backend
- base URL
- health URL
- token source status
- next check command
- Claude Code direct configuration command

### 3. Check the Gateway

```sh
sh scripts/check_local_codex_gateway.sh --base-url http://127.0.0.1:18080 --model gpt-5.5
```

### 4. Configure Claude Code Direct Mode

```sh
sh scripts/write_claude_code_direct_settings.sh --gateway-base-url http://127.0.0.1:18080
```

Then run Claude Code inside your target repository:

```sh
claude
```

If your current shell has both `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN`, clear the conflict first or use the compatibility wrapper:

```sh
sh scripts/run_claude_code_direct.sh --gateway-base-url http://127.0.0.1:18080
```

## Release Packages

Codex Gate ships two release archive types:

- `codex-gate-runtime.zip`: user-facing package with gateway binaries, startup scripts, check scripts, and required documentation.
- `codex-gate-dev.zip`: audit/development package with source code, fixtures, tests, and validation scripts.

The public source tree and release packages intentionally exclude `.agents/`, `.codex/`, internal docs, dogfood/review reports, PR templates, caches, and secret-like files. The runtime package also excludes source trees, fixtures, benchmark outputs, and CI files.

## Local Development Checks

Common validation commands:

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

Build and inspect release archives:

```sh
python scripts/package_release.py --kind runtime --output dist/release/codex-gate-runtime.zip
python scripts/inspect_release_archive.py --kind runtime --archive dist/release/codex-gate-runtime.zip
python scripts/package_release.py --kind dev --output dist/release/codex-gate-dev.zip
python scripts/inspect_release_archive.py --kind dev --archive dist/release/codex-gate-dev.zip
```

## Documentation

- User manual: [USER_MANUAL.md](USER_MANUAL.md)
- Chinese README: [README.md](README.md)

The public repository intentionally excludes internal phase plans, dogfood reports, agent prompts, personal workspace notes, and local research logs.

## Security Notes

Codex Gate is a local gateway, not a hosted service. Run it only on machines and networks you trust. Do not commit personal tokens, ChatGPT/Codex session material, `.env` files, SSH keys, or cloud credentials to the repository or release archives.

The default `codex-web` backend is experimental. Before team or production use, review upstream account policy, terms of service, token storage, network boundaries, and log redaction.

## License

Codex Gate is licensed under the [Apache License 2.0](LICENSE). Please also retain the project notice in [NOTICE](NOTICE).
