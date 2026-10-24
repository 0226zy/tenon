---
name: platform-contract
description: 原型构建平台的强制工程、预览与安全约定。任何生成或修改应用的动手操作前必须加载。
---

# 平台约定（强制）

**动手前必须加载本技能。与其它技能冲突时，以本技能为准。**

| 约定 | 要求 |
| --- | --- |
| 目录 | `web/`（Vite + React + TS，按 feature 组织）、`cmd/server/` 与 `internal/`（Go 标准库 ServeMux，按域分包）、`start.sh`、`AGENTS.md`、`.gitignore`（含 node_modules、dist、bin、*.sqlite）。 |
| 启动 | `start.sh` 构建前端，以 `CGO_ENABLED=0` 编译 Go，使用纯 Go 的 `modernc.org/sqlite`，并 `exec` 服务；服务监听 `0.0.0.0:$PORT`。`/api/v1/*` 是 JSON API，其余请求由 `web/dist` 与 SPA 回退处理；`GET /` 必须返回 200。 |
| 数据 | 使用 SQLite，数据库文件放在 `$DATA_DIR`（默认 `./data` 仅用于本地开发）。确需外部服务时，必须在回复中说明并更新 `AGENTS.md`。 |
| 禁止 | 禁止后台启动服务占用 3000 端口（必须使用 preview 工具）；禁止运行 docker / docker compose；禁止修改 `.git`；禁止把密钥写入前端或仓库；禁止启动脱离终端的守护进程。 |
| 验证门禁 | 前端和后端构建通过 → `go test ./...` → 调用 preview 工具并确认探活 200 → 冒烟核心 API。 |
| 其它技能 | 保留 dsh-go 的九个技能，但数据库固定 SQLite，沙箱内不执行 docker，预览固定使用 preview 工具。deploy-ship 可以生成 Dockerfile / compose 文件，但不能在沙箱内运行。 |

`start.sh` 可以保持或增强，但必须保留前台 `exec ./bin/server` 和上述端口、构建及数据目录契约。
