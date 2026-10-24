#!/usr/bin/env bash
# Platform preview entrypoint. Keep the service in this process group.
set -euo pipefail
export GOWORK=off
cd "$(dirname "$0")"
export PORT="${PORT:-3000}"
export DATA_DIR="${DATA_DIR:-$PWD/data}"
mkdir -p "$DATA_DIR"
( cd web && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build )
CGO_ENABLED=0 go build -o bin/server ./cmd/server
exec ./bin/server
