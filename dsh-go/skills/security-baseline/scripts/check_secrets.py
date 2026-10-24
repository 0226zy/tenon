#!/usr/bin/env python3
"""check_secrets.py — 密钥与硬编码红线扫描（零依赖，仅标准库）。

用法:
    python3 check_secrets.py <项目根>

检出规则（全部为红线，命中即 exit 1）:
    SC01  .env 未被 .gitignore 忽略（防密钥入库）
    SC02  .env 未被 .dockerignore 忽略（防密钥烘进镜像层）
    SC03  前端 src/ 与 dist/ 中出现常见密钥模式（sk-xxx、service_role、AKIA...、-----BEGIN）
    SC04  代码库中硬编码密码/密钥赋值（password = "明文" 一类）

输出格式: 文件:行号:规则:建议
退出码: 有命中 exit 1；全部通过 exit 0。
"""

import argparse
import re
import sys
from pathlib import Path

# 前端产物中绝不该出现的密钥模式
SECRET_PATTERNS = [
    ("sk- 样式密钥", re.compile(r"sk-[A-Za-z0-9_-]{16,}")),
    ("Supabase service_role", re.compile(r"service_role")),
    ("AWS Access Key", re.compile(r"AKIA[0-9A-Z]{16}")),
    ("私钥块", re.compile(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----")),
]

# 硬编码密码赋值：password/secret/api_key 等（允许 DATABASE_PASSWORD 一类后缀结尾变量名）= "字面量"
HARDCODE_RE = re.compile(
    r"""(?ix)                      # 忽略大小写、宽松模式
    \b\w*(password|passwd|pwd|secret|secret_key|api_key|apikey|access_key|private_key)
    \s*[:=]\s*
    ["']([^"'\n]{6,})["']
    """
)

# 明显是占位符的值不误报
PLACEHOLDER_RE = re.compile(
    r"(?i)(example|placeholder|your[-_ ]|xxx|changeme|change[-_ ]?me|todo|dummy|"
    r"sample|test|^\.{3}$|\$\{|<.*>|^\*+$|env|none|null|redacted)"
)

# 扫描前端密钥模式的目录名（src/dist 内的前端代码与产物）
FRONTEND_DIR_NAMES = {"src", "dist"}
FRONTEND_EXTS = {".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".html", ".json", ".map"}

# 硬编码赋值扫描的扩展名
CODE_EXTS = {".py", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".yaml", ".yml", ".toml", ".json"}

SKIP_DIRS = {".git", "node_modules", "__pycache__", ".venv", "venv", ".mypy_cache", ".pytest_cache"}
MAX_FILE_BYTES = 2 * 1024 * 1024  # 跳过大文件（如压缩后的 bundle map）之外的巨型产物


def is_env_ignored(ignore_file: Path) -> bool:
    """判断 ignore 文件是否覆盖了 .env。"""
    if not ignore_file.is_file():
        return False
    for raw in ignore_file.read_text(encoding="utf-8", errors="replace").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or line.startswith("!"):
            continue
        pat = line.lstrip("/").rstrip("/")
        if pat in (".env", ".env.*", "*.env", ".env*", "**/.env", "**/.env*"):
            return True
        # 如 ".env.local" 之类只覆盖部分，不算覆盖 .env
    return False


def scan_file(path: Path, rel: str, findings: list):
    try:
        if path.stat().st_size > MAX_FILE_BYTES:
            return
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return
    in_frontend = bool(FRONTEND_DIR_NAMES & set(path.parts))
    suffix = path.suffix.lower()
    is_example = path.name.endswith((".example", ".sample", ".template"))

    for lineno, line in enumerate(text.splitlines(), start=1):
        # SC03 前端 src/dist 密钥模式
        if in_frontend and suffix in FRONTEND_EXTS:
            for label, pattern in SECRET_PATTERNS:
                m = pattern.search(line)
                if m:
                    findings.append(
                        (
                            rel,
                            lineno,
                            "SC03",
                            f"前端代码/产物中出现{label}；秘密键永不出服务端之外，"
                            "改由后端代理调用并立即轮换该密钥",
                        )
                    )
                    break
        # SC04 硬编码密码/密钥赋值（跳过示例文件）
        if suffix in CODE_EXTS and not is_example:
            m = HARDCODE_RE.search(line)
            if m and not PLACEHOLDER_RE.search(m.group(2)):
                findings.append(
                    (
                        rel,
                        lineno,
                        "SC04",
                        "疑似硬编码密钥/密码赋值；改为环境变量注入（os.Getenv / caarlos0/env（Go）、dotenv（前端 .env）），"
                        "已入库的密钥按泄露处理立即轮换",
                    )
                )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="密钥红线扫描：.env 忽略检查、前端密钥模式、硬编码密码"
    )
    parser.add_argument("project_root", help="项目根目录")
    args = parser.parse_args()

    root = Path(args.project_root)
    if not root.is_dir():
        print(f"错误: 目录不存在: {root}", file=sys.stderr)
        return 2

    findings: list = []

    # SC01 / SC02：.env 必须被 .gitignore 与 .dockerignore 覆盖
    if not is_env_ignored(root / ".gitignore"):
        findings.append(
            (
                str(root / ".gitignore"),
                0,
                "SC01",
                ".gitignore 未忽略 .env；添加 `.env`（或 `.env.*`）一行，密钥入库须按泄露处理并轮换",
            )
        )
    if not is_env_ignored(root / ".dockerignore"):
        findings.append(
            (
                str(root / ".dockerignore"),
                0,
                "SC02",
                ".dockerignore 未忽略 .env；添加 `.env` 一行，防止构建上下文把密钥烘进镜像层",
            )
        )

    scanned = 0
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        rel = str(path)
        suffix = path.suffix.lower()
        in_frontend = bool(FRONTEND_DIR_NAMES & set(path.parts))
        if (in_frontend and suffix in FRONTEND_EXTS) or suffix in CODE_EXTS:
            scanned += 1
            scan_file(path, rel, findings)

    for rel, lineno, rule_id, advice in sorted(findings):
        loc = f"{rel}:{lineno}" if lineno else rel
        print(f"{loc}:[{rule_id} 红线] {advice}")

    print(f"\n扫描 {scanned} 个文件：红线 {len(findings)} 处。")
    if findings:
        print("存在红线命中，视为未完成交付。")
        return 1
    print("无红线命中。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
