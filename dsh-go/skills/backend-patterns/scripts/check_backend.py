#!/usr/bin/env python3
"""check_backend.py — Go 后端红线机械检查（零依赖，仅标准库）。

用法:
    python3 check_backend.py <api目录>

检出规则（递归扫描 *.go，跳过 vendor/ 等目录，保留 *_test.go）:
    [红线] BE01  fmt.Sprintf 拼接 SQL 关键词 —— SQL 注入风险，走参数化查询/sqlc
    [红线] BE02  裸 http.ListenAndServe( / http.ListenAndServeTLS( —— 无超时，
                 必须构造带 Read/Write/Idle 超时的 http.Server
    [红线] BE03  import gorilla/mux 或 gorilla/sessions —— 2023 年已归档，
                 路由用 Go 1.22+ ServeMux，session 用 scs/v2
    [红线] BE04  .AutoMigrate( —— 生产迁移必须走 goose（embed + 启动自动 Up）
    [建议] BE05  time.Now() 未跟 .UTC() —— 应用内一律 UTC 存储与传输
    [建议] BE06  _ = xxx.Close() —— 吞 Close 错误，至少记日志

输出格式: 文件:行号:规则:建议
退出码: 有红线命中 exit 1；仅建议命中或全部通过 exit 0；目录不存在 exit 2。
"""

import argparse
import re
import sys
from pathlib import Path

# (规则号, 是否红线, 正则, 建议)
LINE_RULES = [
    (
        "BE01",
        True,
        re.compile(
            r"fmt\.Sprintf\([^)]*\b(SELECT|INSERT|UPDATE|DELETE|DROP|ALTER)\b",
            re.IGNORECASE,
        ),
        "禁止 fmt.Sprintf 拼接 SQL；用参数化查询（$1 占位）或 sqlc，动态列名/表名走白名单映射",
    ),
    (
        "BE02",
        True,
        re.compile(r"\bhttp\.ListenAndServe(TLS)?\s*\("),
        "禁止裸 http.ListenAndServe；构造 http.Server{ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout} 再 srv.ListenAndServe()",
    ),
    (
        "BE03",
        True,
        re.compile(r'"github\.com/gorilla/(mux|sessions)"'),
        "gorilla/mux 与 gorilla/sessions 已归档；路由用 Go 1.22+ 标准库 ServeMux，session 用 alexedwards/scs/v2",
    ),
    (
        "BE04",
        True,
        re.compile(r"\.AutoMigrate\s*\("),
        "AutoMigrate 仅限开发期原型；生产 schema 变更必须走 goose 迁移文件（-- +goose Up/Down）",
    ),
    (
        "BE06",
        False,
        re.compile(r"_\s*=\s*[\w.]*[\w]\.Close\(\)"),
        "吞 Close 错误会掩盖故障；改为 if err := rows.Close(); err != nil { slog.Warn(...) }",
    ),
]

# BE05 单独处理：time.Now() 出现但同行无 .UTC()
TIME_NOW_RE = re.compile(r"\btime\.Now\(\)")
UTC_RE = re.compile(r"\.UTC\(\)")

# 扫描时跳过的目录
SKIP_DIRS = {".git", "vendor", "node_modules", ".venv", "venv", "testdata"}

# 纯注释行不纳入匹配（防误报）
COMMENT_LINE_RE = re.compile(r"^\s*//")


def iter_go_files(root: Path):
    for path in sorted(root.rglob("*.go")):
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        yield path


def check_lines(path: Path, rel: str, findings: list):
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        print(f"警告: 无法读取 {rel}: {exc}", file=sys.stderr)
        return
    for lineno, line in enumerate(text.splitlines(), start=1):
        if COMMENT_LINE_RE.match(line):
            continue
        for rule_id, is_red, pattern, advice in LINE_RULES:
            if pattern.search(line):
                findings.append((rel, lineno, rule_id, is_red, advice, line.strip()))
        if TIME_NOW_RE.search(line) and not UTC_RE.search(line):
            findings.append(
                (
                    rel,
                    lineno,
                    "BE05",
                    False,
                    "time.Now() 未跟 .UTC()；应用内一律 UTC（time.Now().UTC()），展示层才转时区",
                    line.strip(),
                )
            )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Go 后端红线检查：Sprintf 拼 SQL / 裸 ListenAndServe / gorilla 依赖 / AutoMigrate / 时区 / 吞 Close 错误"
    )
    parser.add_argument("api_dir", help="Go 后端代码目录（递归扫描 *.go）")
    args = parser.parse_args()

    root = Path(args.api_dir)
    if not root.is_dir():
        print(f"错误: 目录不存在: {root}", file=sys.stderr)
        return 2

    findings: list = []
    scanned = 0
    for path in iter_go_files(root):
        scanned += 1
        check_lines(path, str(path), findings)

    red = [f for f in findings if f[3]]
    warn = [f for f in findings if not f[3]]

    for rel, lineno, rule_id, is_red, advice, snippet in sorted(findings):
        level = "红线" if is_red else "建议"
        print(f"{rel}:{lineno}:[{rule_id} {level}] {advice}  | 命中: {snippet}")

    print(f"\n扫描 {scanned} 个文件：红线 {len(red)} 处，建议 {len(warn)} 处。")
    if red:
        print("存在红线命中，视为未完成交付。")
        return 1
    print("无红线命中。" + ("（建议项请人工确认）" if warn else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
