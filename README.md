# Tenon（原型工坊）

Tenon 定位为“从需求到可运行原型”：P0 以自然语言驱动 agent-runtime，在隔离工作区中生成、迭代、查看并预览 React + Go + SQLite 原型。

- 根模块：`github.com/0226zy/tenon`
- Runtime：`agent-runtime/`（module `github.com/0226zy/tenon/agent-runtime`，保留独立测试边界）
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

首页对游客开放：首次访问直接进入欢迎页，浏览示例和布局不会请求受保护接口；只有新建对话时才弹出登录 / 注册。注册走 `POST /api/register`（Argon2id 存密码，用户名 1–64 位字母、数字或 `_.-`，密码 8–128 位），成功后自动登录并继续刚才的创建动作。注册默认按来源地址每小时最多 10 次，可用 `auth.register_limit` 与 `auth.register_window` 调整（反向代理后面来源地址是代理 IP，需按团队规模放宽）；管理员仍可用 `bin/control user add` 预置账号。

默认 `local-demo` 是确定性本地模型适配器，**不是外部真实模型**。它会通过真实 agent-runtime 工具链写文件、执行测试并构建预览，便于无密钥复现产品流程；`local-demo-secondary` 用于服务端（API）级模型切换与独立计量验证；前端不展示、也不会指定模型。数据库（SQLite 或 MySQL）、对话工作区、wsd/agent-runtime 和生成应用进程均为真实运行。运行数据位于 `deploy/data/` 与 `.local/`，均已忽略，不应提交。

## MySQL 存储

控制层库表可以放在 MySQL 8（生产与 `local` 模式都支持），连接凭据只从环境变量读取：

```bash
CONTROL_DB_DRIVER=mysql          # 或 database.driver: mysql；留空时 local→sqlite，其余→mysql
CONTROL_MYSQL_HOST=127.0.0.1     # 默认 127.0.0.1
CONTROL_MYSQL_PORT=3306          # 默认 3306
CONTROL_MYSQL_DATABASE=tenon     # 默认 tenon
CONTROL_MYSQL_USER=tenon_app
CONTROL_MYSQL_PASSWORD=...
# 也可以用 CONTROL_MYSQL_DSN 直接给完整 DSN
```

`local` 模式现在只表示“本机沙箱 + 演示模型”，不再强制 SQLite。本机开发把上述变量写入未跟踪的 `.local/mysql.env`（权限 0600），`npm run dev` 会自动加载；显式设置 `CONTROL_CONFIG`（例如 e2e）时跳过，保证测试继续使用隔离的 SQLite。模板见 `deploy/mysql.env.example`。

应用账号只需要 `SELECT/INSERT/UPDATE`：
- 建表与迁移由管理员账号执行一次，并把版本写入 `t_schema_migration`；应用启动时只读该表，缺表时会给出“需要管理员建表”的明确错误。
- 会话注销/改密/停用通过把 `Fexpire_time` 提前实现，不依赖 `DELETE`；过期会话行不再参与鉴权，物理清理可用管理员账号执行（`DELETE FROM t_auth_session WHERE Fexpire_time<=UTC_TIMESTAMP()`）。
- 远程数据库不要直接暴露到公网：保持 `bind-address=127.0.0.1`，本机通过 SSH 隧道访问（`ssh -N -L 13306:127.0.0.1:3306 user@host`）并把 `CONTROL_MYSQL_PORT` 指向隧道端口。


## 模型目录与凭证

- `deploy/models.local.yaml` 在每次读取时按文件 mtime/大小判断是否变化，改完即热加载，无需重启；非法配置会保留上一份可用目录并在日志中提示。
- 模型只声明 `api_key_env`，密钥在每次请求时从环境变量读取；启动阶段不校验密钥，缺少密钥不会阻塞服务启动。
- `GET /api/models` 返回 `ready` 与 `missing_env`，供运维/管理端诊断；对话页不展示模型选择，默认模型由服务端目录决定。
- 真正发起对话时后端才做预检：缺少环境变量返回 `409 LLM_NOT_CONFIGURED`，message 里带上具体变量名；前端 toast 与失败气泡都会展示该说明。
- 新增/删除模型对已存在的沙箱同样生效：运行时把模型名交给控制层 `/llm/v1` 代理解析上游；沙箱启动快照里没有的名字会回退到代理默认配置，不需要重启沙箱。

Linux 部署（交叉编译、`/data/server` 目录、systemd/nginx、预览端口限制）见 `docs/deployment.md`。

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

E2E 使用独立的 `deploy/control.e2e.yaml` 和 `.local/e2e/data/`，自动构建、启动并清理服务，覆盖登录与锁定、对话与分页、真实工具成功/失败、取消与重试、服务端模型切换、离线恢复、文件树与只读编辑器、预览失败/重启/休眠唤醒、鉴权回跳和预览授权。结果与截图写入 `.local/e2e/result.json` 和 `.local/e2e/*.png`；`not_covered` 必须逐项检查，不能把 `ok:true` 理解为生产部署或所有浏览器安全矩阵均已通过。

逐项证据与未验证边界见 `docs/acceptance.md`。生产 MySQL 8、Docker 隔离、Caddy、故障注入和容量测试不在 local 验收的替代范围内。不得在仓库中写入真实模型密钥、账号密码或令牌。
