#!/usr/bin/env python3
"""check_urls.py — 硬编码 URL 扫描（零依赖，仅标准库）。

用法:
    python3 check_urls.py <项目根>

检出源码中的硬编码地址：localhost、127.0.0.1、.lovable.app、.lovableproject.com 等
AI 生成代码常见的预览/开发域名。命中列出供人工确认：
  - 应用代码里的硬编码 URL 必须改为环境变量（失败模式四：预览能跑上线挂）。
  - compose healthcheck、容器内部互访（服务名）、本地开发默认值通常属正常，人工确认即可。

排除：.env*、README*、tests（tests/test/__tests__ 目录与 *.test.*/*.spec.* 文件）、
node_modules/.git/锁文件等。

退出码: 有命中 exit 1（需人工逐条确认）；无命中 exit 0。
"""

import argparse
import re
import sys
from pathlib import Path

URL_PATTERNS = [
    ("localhost", re.compile(r"\blocalhost\b")),
    ("127.0.0.1", re.compile(r"\b127\.0\.0\.1\b")),
    ("Lovable 预览域名", re.compile(r"[a-z0-9.-]+\.lovable\.app", re.IGNORECASE)),
    ("Lovable 项目域名", re.compile(r"[a-z0-9.-]+\.lovableproject\.com", re.IGNORECASE)),
]

SCAN_EXTS = {
    ".py", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs",
    ".json", ".yaml", ".yml", ".toml", ".conf", ".ini",
    ".html", ".css", ".sh", ".cfg",
}
SCAN_NAMES = {"Dockerfile", "docker-compose.yml", "docker-compose.yaml", "nginx.conf"}

SKIP_DIRS = {
    ".git", "node_modules", "__pycache__", ".venv", "venv",
    ".mypy_cache", ".pytest_cache", "dist", "build", ".next",
}
TEST_DIR_NAMES = {"tests", "test", "__tests__", "e2e"}
LOCK_NAMES = {"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "uv.lock", "poetry.lock"}
MAX_FILE_BYTES = 2 * 1024 * 1024


def is_excluded(path: Path) -> bool:
    name = path.name
    if name.startswith(".env"):          # .env、.env.example 等
        return True
    if name.lower().startswith("readme"):
        return True
    if name in LOCK_NAMES:
        return True
    if TEST_DIR_NAMES & set(path.parts):
        return True
    if re.search(r"\.(test|spec)\.[a-z]+$", name, re.IGNORECASE):
        return True
    if any(part in SKIP_DIRS for part in path.parts):
        return True
    return False


def should_scan(path: Path) -> bool:
    return path.suffix.lower() in SCAN_EXTS or path.name in SCAN_NAMES


def main() -> int:
    parser = argparse.ArgumentParser(
        description="扫描源码中的硬编码 localhost/127.0.0.1/预览域名，命中需人工确认改走环境变量"
    )
    parser.add_argument("project_root", help="项目根目录")
    args = parser.parse_args()

    root = Path(args.project_root)
    if not root.is_dir():
        print(f"错误: 目录不存在: {root}", file=sys.stderr)
        return 2

    findings = []
    advisory = []  # compose 文件中的容器内地址：合法（服务名网络），仅提示不计入红线
    scanned = 0
    for path in sorted(root.rglob("*")):
        if not path.is_file() or is_excluded(path) or not should_scan(path):
            continue
        try:
            if path.stat().st_size > MAX_FILE_BYTES:
                continue
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        scanned += 1
        rel = str(path)
        is_compose = path.name.startswith(("docker-compose", "compose."))
        for lineno, line in enumerate(text.splitlines(), start=1):
            for label, pattern in URL_PATTERNS:
                if pattern.search(line):
                    entry = (rel, lineno, label, line.strip()[:120])
                    if is_compose and label in ("localhost", "127.0.0.1"):
                        advisory.append(entry)
                    else:
                        findings.append(entry)
                    break

    for rel, lineno, label, snippet in findings:
        print(f"{rel}:{lineno}:[硬编码 {label}] 请确认：应用代码中的 URL 应改走环境变量  | {snippet}")

    for rel, lineno, label, snippet in advisory:
        print(f"{rel}:{lineno}:[提示 {label}] compose 编排内的容器地址，属合法用法  | {snippet}")

    print(f"\n扫描 {scanned} 个文件：命中 {len(findings)} 处，提示 {len(advisory)} 处。")
    if findings:
        print("存在命中，请逐条人工确认（容器内 healthcheck/服务名互访可豁免）；"
              "应用代码中的命中必须改为环境变量后方可上线。")
        return 1
    print("无命中。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
