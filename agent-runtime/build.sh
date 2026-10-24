#!/usr/bin/env bash
# 编译 agent-runtime，并把 bin、conf、skills、prompt 打成部署包。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

find_module_root() {
  local d="$ROOT"
  while [[ "$d" != "/" ]]; do
    if [[ -f "$d/go.mod" ]]; then
      echo "$d"
      return 0
    fi
    d="$(dirname "$d")"
  done
  return 1
}

MODULE_ROOT="$(find_module_root)" || {
  echo "未找到 go.mod（从 $ROOT 向上查找）" >&2
  exit 1
}

echo "==> 编译 agent-runtime"
mkdir -p "$ROOT/bin"
(
  cd "$MODULE_ROOT"
  go build -o "$ROOT/bin/agent-runtime" ./cmd/agent-runtime
)

for d in bin conf skills prompt; do
  if [[ ! -e "$ROOT/$d" ]]; then
    echo "缺少目录: $ROOT/$d" >&2
    exit 1
  fi
done

DIST="$ROOT/dist"
mkdir -p "$DIST"
ARCHIVE="$DIST/agent-runtime.tar.gz"

echo "==> 打包 $ARCHIVE"
tar --exclude='*.log' --exclude='*.pid' -czf "$ARCHIVE" -C "$ROOT" bin conf skills prompt
echo "部署包已生成: $ARCHIVE"
