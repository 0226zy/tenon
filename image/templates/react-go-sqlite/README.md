# React + Go + SQLite 原型模板

这是一个可直接运行的全栈模板：React/Vite/TypeScript 前端、Go ServeMux JSON API、modernc SQLite 持久化，以及静态文件与 SPA 回退。

## 本地验证（前台运行）

要求 Go 1.25+、Node.js 20+。

```bash
(cd web && npm ci --no-audit --no-fund && npm run build)
go test ./...
PORT=3000 DATA_DIR="$PWD/data" ./start.sh
```

另开终端执行核心冒烟（不会启动或留下后台服务）：

```bash
BASE_URL=http://127.0.0.1:3000 ./scripts/smoke.sh
```

- `GET /`：构建后的 React 页面，必须为 200。
- `GET /api/v1/health`：服务与 SQLite 健康检查。
- `GET /api/v1/items`：最近 200 条记录。
- `POST /api/v1/items`：JSON `{"title":"..."}`，标题 1–200 字。
- `DELETE /api/v1/items/{id}`：删除记录。

`PORT` 默认为 3000；`DATA_DIR` 默认为 `./data`（仅本地开发）。生产预览由 wsd 传入这两个值。不得在沙箱内用 `nohup`、`&` 或其它方式留下后台服务。
