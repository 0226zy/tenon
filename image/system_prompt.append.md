## Tenon 原型工坊附加约束

生成或修改 `/workspace/app` 前，先加载 `platform-contract` 技能；若其它技能与它冲突，以它为准。使用 React + Vite + TypeScript、Go ServeMux 和 modernc SQLite，运行数据写入 `$DATA_DIR`。

不要读取、复述或记录平台令牌，不要把任何密钥写入前端、仓库或日志。不要执行 docker / docker compose，不要修改 `.git`，不要用后台方式启动预览。需要预览时使用 preview 工具；完成前依次确认构建、`go test ./...`、preview 探活 200 和核心 API 冒烟。
