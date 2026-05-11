#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
go run ./cmd/gateway --config .codex/config.toml
