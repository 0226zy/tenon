#!/usr/bin/env bash
# 将 build.sh 生成的部署包解压到指定根目录，并在 bin 目录下启动 agent-runtime。
# 用法: ./deploy.sh [部署根目录]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARCHIVE="$SCRIPT_DIR/dist/agent-runtime.tar.gz"

if [[ $# -ge 1 ]]; then
  DEPLOY_ROOT="$1"
else
  read -r -p "请输入部署根目录: " DEPLOY_ROOT
fi

if [[ -z "${DEPLOY_ROOT}" ]]; then
  echo "部署根目录不能为空" >&2
  exit 1
fi

# 支持 ~/path 这类写法
case "$DEPLOY_ROOT" in
  "~"|"~/"*) DEPLOY_ROOT="$HOME${DEPLOY_ROOT:1}" ;;
esac
mkdir -p "$DEPLOY_ROOT"
DEPLOY_ROOT="$(cd "$DEPLOY_ROOT" && pwd)"

if [[ ! -f "$ARCHIVE" ]]; then
  echo "未找到部署包: $ARCHIVE" >&2
  echo "请先在 $SCRIPT_DIR 执行 ./build.sh" >&2
  exit 1
fi

echo "==> 解压到 $DEPLOY_ROOT"
tar -xzf "$ARCHIVE" -C "$DEPLOY_ROOT"

if [[ ! -x "$DEPLOY_ROOT/bin/agent-runtime" ]]; then
  echo "解压后未找到可执行文件: $DEPLOY_ROOT/bin/agent-runtime" >&2
  exit 1
fi

mkdir -p "$DEPLOY_ROOT/data"

if [[ -z "${DEEPSEEK_API_KEY:-}" && -n "${DEEPSEEKAPI:-}" ]]; then
  export DEEPSEEK_API_KEY="$DEEPSEEKAPI"
fi

PIDFILE="$DEPLOY_ROOT/bin/agent-runtime.pid"
LOGFILE="$DEPLOY_ROOT/bin/agent-runtime.log"

if [[ -f "$PIDFILE" ]]; then
  old="$(cat "$PIDFILE" || true)"
  if [[ -n "$old" ]] && kill -0 "$old" 2>/dev/null; then
    echo "==> 停止已有进程 $old"
    kill -TERM "$old" || true
    for _ in 1 2 3 4 5; do
      kill -0 "$old" 2>/dev/null || break
      sleep 0.3
    done
    if kill -0 "$old" 2>/dev/null; then
      kill -KILL "$old" || true
    fi
  fi
fi

# 升级兼容：只停止 PID 文件指向的旧 dshd，避免误杀复用 PID 的其他进程。
LEGACY_PIDFILE="$DEPLOY_ROOT/bin/dshd.pid"
if [[ -f "$LEGACY_PIDFILE" ]]; then
  legacy="$(cat "$LEGACY_PIDFILE" || true)"
  if [[ "$legacy" =~ ^[1-9][0-9]*$ ]] && kill -0 "$legacy" 2>/dev/null; then
    legacy_comm="$(ps -p "$legacy" -o comm= 2>/dev/null || true)"
    legacy_comm="${legacy_comm##*[[:space:]/]}"
    if [[ "$legacy_comm" == "dshd" ]]; then
      echo "==> 停止旧 dshd 进程 $legacy"
      kill -TERM "$legacy" || true
      for _ in 1 2 3 4 5; do
        kill -0 "$legacy" 2>/dev/null || break
        sleep 0.3
      done
      if kill -0 "$legacy" 2>/dev/null; then
        kill -KILL "$legacy" || true
      fi
    fi
  fi
fi

echo "==> 在 $DEPLOY_ROOT/bin 启动 agent-runtime"
# 新会话 + 二次 fork，避免随调用方终端/进程组一起被结束。
python3 - "$DEPLOY_ROOT" <<'PY'
import os, sys
root = sys.argv[1]
bin_dir = os.path.join(root, "bin")
pidfile = os.path.join(bin_dir, "agent-runtime.pid")
logfile = os.path.join(bin_dir, "agent-runtime.log")
os.chdir(bin_dir)
r, w = os.pipe()
if os.fork() > 0:
    os.close(w)
    data = os.read(r, 64).strip()
    with open(pidfile, "w") as f:
        f.write(data.decode() + "\n")
    os._exit(0)
os.close(r)
os.setsid()
pid = os.fork()
if pid > 0:
    os.write(w, str(pid).encode())
    os.close(w)
    os._exit(0)
os.close(w)
fd = os.open(logfile, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o644)
os.dup2(fd, 1)
os.dup2(fd, 2)
if fd > 2:
    os.close(fd)
os.execv("./agent-runtime", ["./agent-runtime", "-config", "../conf/agent-runtime.yaml"])
PY

sleep 0.5
pid="$(cat "$PIDFILE")"
if ! kill -0 "$pid" 2>/dev/null; then
  echo "进程已退出，最近日志:" >&2
  tail -n 30 "$LOGFILE" >&2 || true
  exit 1
fi

echo "已启动 PID $pid"
echo "  工作目录: $DEPLOY_ROOT/bin"
echo "  日志    : $LOGFILE"
echo "  Web UI  : http://127.0.0.1:3080/"
