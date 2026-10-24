# ProtoForge（原型工坊）

ProtoForge 定位为“从需求到可运行原型”：P0 以自然语言驱动 agent-runtime，在隔离工作区中生成、迭代、查看并预览 React + Go + SQLite 原型。

- 根模块：`github.com/0226zy/protoforge`
- Runtime：`agent-runtime/`（module `github.com/0226zy/protoforge/agent-runtime`，保留独立测试边界）
- 控制层：`cmd/control`
- 工作区守护：`cmd/wsd`
- 平台前端：`web/`（Vue 3 + TypeScript + Pinia + Monaco）
- 共享契约：`pkg/contract`
- 架构决策：`docs/architecture.md`
- 验收清单：`docs/acceptance.md`

## 范围说明

生产部署目标为 MySQL 8 + Docker + Caddy。显式 `local` 模式用于可信本机功能验收，采用本地 SQLite 和独立工作区进程；它不是多租户安全沙箱。任何本地验收结果都不能替代 Docker 隔离、MySQL 故障注入和容量测试。

## 本地启动

要求 Go 1.25+、Node.js 20+、Git。真实 Chrome 验收还需要本机 Chrome；扩展休眠场景需要 `sqlite3`，API 冒烟脚本需要 Python 3。

```bash
npm ci
npm --prefix web ci
npm run dev -- --host 127.0.0.1 --port 7100 --strictPort
```

打开 `http://127.0.0.1:7100/`。首次启动会构建 control、wsd、agent-runtime，并初始化账号 `demo`；随机密码只打印在终端，同时写入权限为 `0600`、未被 Git 跟踪的 `.local/demo-password`。停止服务请在前台终端按 `Ctrl+C`。

默认 `local-demo` 是确定性本地模型适配器，**不是外部真实模型**。它会通过真实 agent-runtime 工具链写文件、执行测试并构建预览，便于无密钥复现产品流程；`local-demo-secondary` 用于验证模型切换与独立计量。SQLite、对话工作区、wsd/agent-runtime 和生成应用进程均为真实运行。运行数据位于 `deploy/data/` 与 `.local/`，均已忽略，不应提交。

## 自动化验证

```bash
# 控制层、wsd 与共享契约
GOTOOLCHAIN=go1.25.0 go test ./...
GOTOOLCHAIN=go1.25.0 go test -race ./...

# 独立 Runtime module
(cd agent-runtime && GOWORK=off GOTOOLCHAIN=go1.25.0 go test ./...)
(cd agent-runtime && GOWORK=off GOTOOLCHAIN=go1.25.0 go test -race ./...)

# 前端
npm run test:web
npm run build
```

真实 API 冒烟会启动并清理独立 control 进程；先构建所需二进制，且不要同时占用 `8080–8082` 或 `9090` 端口：

```bash
go build -o bin/control ./cmd/control
go build -o bin/wsd ./cmd/wsd
(cd agent-runtime && go build -o bin/agent-runtime ./cmd/agent-runtime)
python3 scripts/smoke_local.py
```

真实 Chrome 全量产品验收：

```bash
npm run test:e2e
# 可视化运行：HEADFUL=1 npm run test:e2e
# 非默认安装位置：CHROME_PATH=/absolute/path/to/chrome npm run test:e2e
```

E2E 使用独立的 `deploy/control.e2e.yaml` 和 `.local/e2e/data/`，自动构建、启动并清理服务，覆盖登录与锁定、对话与分页、真实工具成功/失败、取消与重试、模型切换、离线恢复、文件树与只读编辑器、预览失败/重启/休眠唤醒、鉴权回跳和预览授权。结果与截图写入 `.local/e2e/result.json` 和 `.local/e2e/*.png`；`not_covered` 必须逐项检查，不能把 `ok:true` 理解为生产部署或所有浏览器安全矩阵均已通过。

逐项证据与未验证边界见 `docs/acceptance.md`。生产 MySQL 8、Docker 隔离、Caddy、故障注入和容量测试不在 local 验收的替代范围内。不得在仓库中写入真实模型密钥、账号密码或令牌。
