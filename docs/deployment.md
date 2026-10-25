# Linux 部署（/data/server）

本部署包只包含：Linux 可执行文件、前端构建产物、生成应用模板、agent 技能与提示词、配置样例、systemd/nginx 配置。
**不包含后端源码**（`cmd/`、`internal/`、`pkg/`、`web/src/`、`node_modules`、`.git`）。

## 1. 交叉编译与打包（在 macOS 开发机）

```bash
npm run build                                     # 前端产物 web/dist
go build -trimpath -ldflags '-s -w' -o dist/tenon-server-linux-amd64/bin/control ./cmd/control
go build -trimpath -ldflags '-s -w' -o dist/tenon-server-linux-amd64/bin/wsd     ./cmd/wsd
(cd agent-runtime && GOWORK=off go build -trimpath -ldflags '-s -w' -o ../dist/tenon-server-linux-amd64/bin/agent-runtime ./cmd/agent-runtime)
```

环境：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`（三个二进制均为静态链接）。

## 2. 包内容

```
bin/        control / wsd / agent-runtime
web/        前端构建产物（nginx 静态站点）
templates/  生成应用模板（沙箱运行时复制）
skills/     agent 技能库
prompt/     agent 基础提示词
config/     control.yaml、models.yaml
install/    systemd 单元、nginx 站点
```

## 3. 服务器依赖

- MySQL 8（控制层库表；应用账号只需 `SELECT/INSERT/UPDATE`）
- Go 1.25+（生成应用构建与 `go test`）
- Node.js 20+ 与 npm（生成应用前端构建）
- git（每轮改动提交）、nginx（静态站点 + API 反向代理）

## 4. 目录与运行

```
/data/server/
├── bin/        可执行文件
├── web/tenon/  前端静态文件（/tenon/ 子路径）
├── templates/  生成应用模板
├── skills/     agent 技能
├── prompt/     agent 提示词
├── config/     control.yaml / models.yaml（0600）
├── data/       secret.key、沙箱工作区（0700）
├── logs/
└── system_prompt.append.md
```

- 访问：`http://82.156.224.101/tenon/`（`/` 302 到 `/tenon/`）；控制层：`systemctl status tenon-control`。
- 数据库连接：`/etc/tenon/mysql.env`（`CONTROL_DB_DRIVER`、`CONTROL_MYSQL_*`，0600）。
- 前端与 API：nginx :80，`/api/` 反代到 `127.0.0.1:8080`。

## 5. 端口与预览

| 端口 | 用途 | 暴露 |
|---|---|---|
| 80 | 前端 + `/api` | 公网 |
| 8081 | 预览子域 `<key>.preview.<preview.domain>:8081` | 公网（需安全组放行） |
| 8080/8082/9090 | API/内部/指标 | 仅本机 |

预览使用 `preview.domain=82.156.224.101.nip.io`，URL 形如
`http://<26位key>.preview.82.156.224.101.nip.io:8081`。

> 注意：部分云厂商会对 80 端口上“未备案域名”的 Host 做拦截（返回 webblock 页面），
> 因此子域预览不能在 80 复用；需要放行 8081，或使用已备案域名 + 反向代理。

## 6. 升级

```bash
sudo rsync -a --delete /tmp/tenon-deploy/bin/ /data/server/bin/
sudo rsync -a --delete /tmp/tenon-deploy/web/ /data/server/web/
sudo rsync -a --delete /tmp/tenon-deploy/templates/ /data/server/templates/
sudo chown -R tenon:tenon /data/server
sudo systemctl restart tenon-control && sudo systemctl reload nginx
```

数据库迁移需要管理员账号执行 DDL 并登记 `t_schema_migration`；应用账号没有 DDL 权限。

## 7. 本次部署实例（2026-10-25）

- 主机：`82.156.224.101`（Ubuntu 24.04 / MySQL 8.0.46）
- 目录：`/data/server`；服务 `tenon-control`（systemd，已 enable/active）
- 入口：`http://82.156.224.101/tenon/`（nginx :80 → `/tenon/` 静态前端；`/api` → `127.0.0.1:8080`；`/ops/` 为管理端）
- 数据库：`tenon_prod`（应用账号 `tenon_app`，仅 `SELECT/INSERT/UPDATE`；与开发库 `tenon` 隔离）
- 依赖：Go 1.27.3、Node 22.23.4 / npm 10.9.9、nginx 1.24、git 2.43、sqlite3
- 已验证：公网登录、创建对话、Run 完成、事件/消息/用量落库；预览构建 `ready` 并可在服务器本机访问页面与静态资源。
- 预览外网访问：该云安全组未放行 8081，且 80 端口对未备案域名 Host 返回 webblock。
  在安全组放行 TCP 8081 后，预览地址为 `http://<key>.preview.82.156.224.101.nip.io:8081`。

> 部署包不含后端源码；`templates/` 是“生成应用模板”，属于运行期资产，不是 Tenon 自身源码。
