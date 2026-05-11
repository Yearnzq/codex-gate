#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."

goos="${GOOS:-$(go env GOOS)}"
goarch="${GOARCH:-$(go env GOARCH)}"
target="${goos}-${goarch}"
out_dir="dist/gateway/${target}"
mkdir -p "$out_dir"

bin_name="gateway"
if [ "$goos" = "windows" ]; then
  bin_name="gateway.exe"
fi

out_path="${out_dir}/${bin_name}"
version="${CODEX_GATEWAY_VERSION:-0.6.0-phase6}"
build_time="${CODEX_GATEWAY_BUILD_TIME:-$(date -u +"%Y-%m-%dT%H:%M:%SZ")}"
build_commit="${CODEX_GATEWAY_BUILD_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
ldflags="-X codex-gate/internal/gateway.ServiceVersion=${version} -X codex-gate/internal/gateway.BuildTime=${build_time} -X codex-gate/internal/gateway.BuildCommit=${build_commit} -X codex-gate/internal/gateway.BuildTarget=${target}"

go build -trimpath -ldflags "$ldflags" -o "$out_path" ./cmd/gateway
echo "OK: built gateway binary at $out_path"
