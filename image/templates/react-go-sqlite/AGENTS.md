# 平台工作约定

动手前必须加载 `platform-contract` 技能；它与其它技能冲突时，以 `platform-contract` 为准。

- 前端位于 `web/`，使用 React、Vite 与 TypeScript，按 feature 组织新增代码。
- 后端入口为 `cmd/server/`，领域代码放入 `internal/`，HTTP 使用 Go 标准库 ServeMux。
- 数据必须使用 modernc.org/sqlite 访问 SQLite，数据库文件放在 `$DATA_DIR`，不得提交数据库。
- `/api/v1/*` 只用于 JSON API；其余 GET 路径由 `web/dist` 和 SPA 回退处理。`GET /` 必须返回 200。
- 可以修改 `start.sh`，但必须保持：构建前端、`CGO_ENABLED=0` 编译后端、`exec ./bin/server`、监听 `0.0.0.0:$PORT`。
- 禁止运行 docker / docker compose，禁止修改 `.git`，禁止把密钥写入前端或仓库。
- 禁止后台启动服务、占用 3000 端口或留下脱离终端的守护进程；预览必须使用平台 preview 工具。
- 验证顺序：前端构建 → `go test ./...` → preview 工具探活 200 → `scripts/smoke.sh` 核心接口冒烟。
- 确需外部服务时，必须在最终回复说明并同步更新本文件。
