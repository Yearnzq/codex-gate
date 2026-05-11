#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
python3 scripts/validate.py
python3 scripts/scan_secrets.py
python3 scripts/test_phase2_review_fixes.py
python3 scripts/test_phase9_review_fixes.py
python3 scripts/test_token_command_installer.py
python3 scripts/test_protocol_fixtures.py
go test ./...
