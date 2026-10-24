#!/usr/bin/env python3
"""check_frontend.py — 前端红线静态扫描（零依赖，Python 3.11+ 标准库）

扫描 <web目录>/src 下的 .tsx/.jsx（React 路线）、.vue（Vue 路线）与 .ts 文件，
按文件后缀分流规则集（.ts 只查共用规则，不查 React/Vue 专属规则）：

React 规则集（.tsx/.jsx）：
  1. dangerouslySetInnerHTML          —— XSS 面，用户内容必须走默认转义或 DOMPurify
  2. key={index} / key={i}            —— 列表 key 用数组下标，重排/删除后 state 挂错行

Vue 规则集（.vue）：
  1. v-html                           —— XSS 面，用户内容必须走默认转义或 DOMPurify
  2. :key="index" / :key="i"          —— 列表 key 用数组下标，重排/删除后 DOM 状态错乱

共用规则（.tsx/.jsx/.vue/.ts 都查）：
  1. token 存 localStorage            —— 会话凭证写入 localStorage，XSS 一键窃取
  2. process.env. 误用                —— 浏览器没有 process，应用 import.meta.env.VITE_X
  3. 动态 import.meta.env[...]        —— Vite 构建期静态替换，动态 key 不生效

用法：
  python3 check_frontend.py <web目录>          # 扫描 <web目录>/src
  python3 check_frontend.py <web目录> --src-dir custom/src

输出：每条命中打印  文件:行号:规则:建议
退出码：有红线命中 exit 1，全部通过 exit 0。
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Rule:
    name: str
    pattern: re.Pattern[str]
    suggestion: str


# —— 共用规则：React / Vue 两类文件都查 ——
COMMON_RULES: list[Rule] = [
    Rule(
        name="token-in-localStorage",
        pattern=re.compile(r"localStorage\s*\.\s*setItem\s*\([^)]*token", re.IGNORECASE),
        suggestion=(
            "会话凭证不得存 localStorage（任何页面 JS 可读，XSS 即全账号接管）。"
            "改用 HttpOnly+Secure+SameSite 的 cookie session"
        ),
    ),
    Rule(
        name="process-env-in-browser",
        pattern=re.compile(r"\bprocess\s*\.\s*env\s*\."),
        suggestion=(
            "浏览器运行时没有 process。改用 Vite 的 import.meta.env.VITE_X"
            "（变量名必须带 VITE_ 前缀）"
        ),
    ),
    Rule(
        name="dynamic-import-meta-env",
        pattern=re.compile(r"import\s*\.\s*meta\s*\.\s*env\s*\["),
        suggestion=(
            "import.meta.env 是构建期静态替换，动态 key 访问不生效。"
            "改用字面量点访问 import.meta.env.VITE_X"
        ),
    ),
]

# —— React 规则集：仅 .tsx/.jsx ——
REACT_RULES: list[Rule] = [
    Rule(
        name="dangerouslySetInnerHTML",
        pattern=re.compile(r"dangerouslySetInnerHTML"),
        suggestion=(
            "禁止直接注入 HTML（XSS 面）。用户内容交给 React 默认转义；"
            "确需富文本时先用 DOMPurify 净化再渲染"
        ),
    ),
    Rule(
        name="array-index-as-key",
        pattern=re.compile(r"key=\{\s*(?:index|i|idx)\s*\}"),
        suggestion=(
            "不要用数组下标当 key（重排/删除后 state 挂错行）。"
            "改用数据中稳定的唯一 id（数据库 id / crypto.randomUUID()）"
        ),
    ),
]

# —— Vue 规则集：仅 .vue ——
VUE_RULES: list[Rule] = [
    Rule(
        name="v-html",
        pattern=re.compile(r"\bv-html\b"),
        suggestion=(
            "禁止 v-html 直接注入 HTML（XSS 面）。用户内容交给 Vue 默认转义（{{ }}）；"
            "确需富文本时先用 DOMPurify 净化再渲染"
        ),
    ),
    Rule(
        name="array-index-as-key",
        pattern=re.compile(r":key=\s*[\"'](?:index|i|idx)[\"']"),
        suggestion=(
            "不要用数组下标当 :key（重排/删除后 DOM 状态错乱）。"
            "改用数据中稳定的唯一 id（数据库 id）"
        ),
    ),
]

RULES_BY_SUFFIX: dict[str, list[Rule]] = {
    ".tsx": COMMON_RULES + REACT_RULES,
    ".jsx": COMMON_RULES + REACT_RULES,
    ".vue": COMMON_RULES + VUE_RULES,
    ".ts": COMMON_RULES,  # .ts 不查 React/Vue 专属规则
}

SCAN_EXTENSIONS = tuple(RULES_BY_SUFFIX)


def iter_source_files(src_dir: Path) -> list[Path]:
    return sorted(
        p for p in src_dir.rglob("*") if p.is_file() and p.suffix in SCAN_EXTENSIONS
    )


def scan_file(path: Path) -> list[tuple[int, Rule, str]]:
    """返回 (行号, 规则, 命中行内容) 列表。"""
    hits: list[tuple[int, Rule, str]] = []
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        print(f"警告：无法读取 {path}: {exc}", file=sys.stderr)
        return hits
    rules = RULES_BY_SUFFIX.get(path.suffix, COMMON_RULES)
    for lineno, line in enumerate(lines, start=1):
        for rule in rules:
            if rule.pattern.search(line):
                hits.append((lineno, rule, line.strip()))
    return hits


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="扫描前端 src 下 .tsx/.jsx/.vue/.ts 的红线违规，命中打印 文件:行号:规则:建议，有命中 exit 1。"
    )
    parser.add_argument("web_dir", help="前端项目目录（默认扫描其下 src/）")
    parser.add_argument(
        "--src-dir",
        default=None,
        help="自定义源码目录（相对 web_dir 或绝对路径），默认 <web_dir>/src",
    )
    args = parser.parse_args(argv)

    web_dir = Path(args.web_dir).resolve()
    if not web_dir.is_dir():
        print(f"错误：目录不存在 {web_dir}", file=sys.stderr)
        return 2

    src_dir = Path(args.src_dir) if args.src_dir else web_dir / "src"
    if not src_dir.is_absolute():
        src_dir = (web_dir / src_dir).resolve()
    if not src_dir.is_dir():
        print(f"错误：源码目录不存在 {src_dir}", file=sys.stderr)
        return 2

    files = iter_source_files(src_dir)
    if not files:
        print(f"未在 {src_dir} 找到 .tsx/.jsx/.vue/.ts 文件。")
        return 0

    total = 0
    for path in files:
        for lineno, rule, line in scan_file(path):
            rel = path.relative_to(web_dir) if path.is_relative_to(web_dir) else path
            print(f"{rel}:{lineno}: {rule.name}: {rule.suggestion}")
            print(f"    | {line}")
            total += 1

    if total:
        print(f"\n红线命中 {total} 处（扫描 {len(files)} 个文件）。请按上述建议修复后再交付。")
        return 1
    print(f"通过：扫描 {len(files)} 个文件，无红线命中。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
