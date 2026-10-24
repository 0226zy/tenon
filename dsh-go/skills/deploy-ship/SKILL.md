---
name: deploy-ship
description: Docker Compose 全栈部署与上线门禁。构建镜像、写 Dockerfile、配 docker-compose、管理环境变量、部署上线、上线前检查时使用。含前后端 Dockerfile 规范、健康检查门控、env fail-fast、上线四检、Top8 部署坑与 AI 失败模式防线。触发关键词：部署、上线、Docker、compose、构建失败、容器起不来、预览能跑上线挂、环境变量。
---

# deploy-ship — 部署与上线门禁

## 何时使用

- 编写或修改任何 Dockerfile、docker-compose.yml、`.dockerignore`、`.env.example` 时。
- 「预览能跑、上线就挂」「容器起不来」「改代码不生效」类问题排查时（查 Top 8 坑）。
- 上线前执行门禁四检与两个脚本时。

## 规范做法

### 1. 前端两阶段 Dockerfile 规范（R4-A1）

1. 两阶段：node 阶段 `npm ci && npm run build`，nginx:alpine 阶段 `COPY --from=builder` 只带走 `dist/`——最终镜像不含 node_modules 与工具链，体积从 1GB+ 降到几十 MB。
2. **层缓存排序是构建速度的最大杠杆**：先 `COPY package.json package-lock.json ./` → `RUN npm ci` → 最后 `COPY . .`。某层失效后其后的层全部重建，日常构建可从 10 分钟降到 30 秒级。
3. 版本 pin：基础镜像锁具体 tag（`node:20-alpine` 而非 latest）；npm 用 `npm ci`（严格按 lockfile），禁止 `npm install` 进 Dockerfile。
4. 前端运行时环境变量（`VITE_*`）在**构建期**烘入产物，改完必须重新 build 才生效（R4-B3 失败模式一）。
5. 每个构建上下文根目录必须有 `.dockerignore`：排除 node_modules、.git、dist、*.log、测试目录，以及 **.env、*.key、*.pem**（构建上下文整体发给 daemon，不排除就把密钥烘进镜像层）。判断标准：`docker build` 首行 `transferring context` 超过 ~100MB 说明 .dockerignore 不完整。

### 2. 后端 Go 多阶段 Dockerfile（R4-A1/R5-9）

- 构建阶段 `golang:1.23-alpine`：「先 go.mod/go.sum 后源码」吃层缓存；`CGO_ENABLED=0 go build` 静态编译（无 libc 依赖）。运行阶段 `alpine:3.20` 只拷二进制——goose 迁移已 embed 进二进制，运行时无需任何源码/SQL 文件，最终镜像 ~20MB。
- 运行镜像必须装 `wget`（compose 健康检查用，alpine 默认没有 curl/wget 是坑6）、建非 root 用户运行；监听端口读环境变量（PaaS 会注入 PORT），不硬编码。

### 3. compose 编排门控（R4-A2）

1. 短格式 `depends_on` 只保证启动顺序**不保证就绪**。数据库配 `healthcheck`（Postgres 用 `pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"`），依赖方用长格式 `condition: service_healthy`；健康检查测真实协议可用性，并设 `start_period` 覆盖冷启动，防止慢启动被误判 unhealthy。
2. **迁移执行位点**：Go 栈默认 goose 迁移 embed 进二进制、api 启动时自动 `goose.Up`（单副本无竞态，最省心，R5-3）；**多副本部署时必须拆独立一次性 migrate 服务**（同一镜像 `command: ["server", "migrate"]`，depends_on db: `service_healthy`，api 等 `service_completed_successfully`），防并发迁移竞态（R4-A2）。
3. 有状态服务必须挂**命名卷**（`db-data:/var/lib/postgresql/data`）；Postgres 设 `PGDATA` 为卷内子目录（如 `/var/lib/postgresql/data/pgdata`）规避挂载点权限问题。注意 `/docker-entrypoint-initdb.d` 只在空数据目录首 boot 执行，改 schema 靠迁移不靠 init 脚本。
4. 网络分层：db、api 不配 `ports:` 对外暴露，只在内部网络以服务名互访（`postgres://db:5432`）；对外入口收敛到一个反代（nginx/Traefik）的 80/443。若必须映射宿主机端口，用 `127.0.0.1:3000:3000` 绑定 loopback——Docker 直接改 iptables，`0.0.0.0` 映射会绕过 UFW 防火墙（R4-A5 坑2）。
5. 健康检查工具必须在镜像里存在：slim 镜像没有 curl 就装一个，或用镜像自带工具（pg_isready），并给足 `start_period`（R4-A5 坑6）。

### 4. env 管理（R4-A3）

1. 配置全走环境变量（12-factor 第 3 条）；compose 用 `env_file: .env` 运行时注入，**绝不把密码明文写进 yaml 或烘进镜像**。
2. 必填变量用 `${VAR:?Variable not set}` 语法 fail-fast——缺变量立刻报错，不带病启动深夜才爆。
3. dev/prod 用同一套 compose 文件 + 不同 `.env` 与镜像 tag 矩阵化区分（`ENVIRONMENT=local|production`、`TAG` 控制镜像版本）；每版镜像唯一 tag，不用 latest 覆盖（改了不生效是坑8）。
4. `.env` 必须进 `.gitignore` 与 `.dockerignore`；仓库只放 `.env.example`（键名+占位值）；泄露即轮换并清 git 历史。

### 5. 上线门禁四检（R4-A4，违反即不算上线完成）

```bash
# ① 构建通过（必要时 --no-cache 验证纯净构建）
docker compose build
# ② 起栈，db 健康检查转 healthy，迁移服务 exit 0
docker compose up -d
docker compose ps   # db=healthy，api/web=healthy；api 启动日志确认 goose.Up 成功
# ③ 健康检查打后端 liveness
python3 scripts/smoke_test.py http://127.0.0.1:8000
# ④ 业务旅程冒烟（注册→登录→访问受保护端点），而非只看首页能开
python3 scripts/smoke_test.py https://你的域名 \
  --register /api/v1/auth/register --login /api/v1/auth/login \
  --protected /api/v1/users/me
```

冷启动验证法：在空卷机器上 `docker compose up -d --wait` 全流程计时跑通，才算真正修复了启动依赖问题。

### 6. AI 失败模式四防线（R4-B3）

| 失败模式 | 防线 |
|---|---|
| 一：预览能跑上线挂（~55% 是环境变量缺失/错配；构建期变量改完没重新 build；认证回调域名指向 localhost） | 发布前变量清单逐一核对（对照 `.env.example` 键名）；改 `VITE_*` 后必须重新 build；认证回调/Site URL 配生产域名；上线后按业务旅程冒烟而非只看首页 |
| 二：密钥泄漏（72% vibe-coded 应用含硬编码密钥；service_role 打进前端 bundle 最致命） | service_role/secret key 只在服务端；交付前跑 `check_secrets.py`；泄露即轮换并清 git 历史 |
| 三：授权策略裸奔（只用 BaaS 时：RLS 未开或 `USING (true)`，170+ 应用被拖库） | 我们的栈默认服务端授权（见 security-baseline 红线 5）；若用 Supabase 类 BaaS：每张表开 RLS + owner 策略 + 双用户实测 |
| 四：URL 硬编码与环境混淆（预览域名/localhost/测试项目写死进代码） | 全 URL 走环境变量；开发与生产用分离的后端实例；上线前跑 `check_urls.py` 全局扫描 |

## 高频坑 Top 8 速查

| # | 症状 | 根因（一行） | 解法（一行） |
|---|---|---|---|
| 1 | app 连不上数据库 connection refused | 容器里 localhost 指容器自身，宿主端口映射对容器间无意义 | Compose 内用服务名+容器端口 `postgres://db:5432`；访宿主机用 host.docker.internal |
| 2 | `ports: "3000:3000"` 后端口暴露公网、UFW 无效 | Docker 直接改 iptables 且默认绑 0.0.0.0 | 只映射 `127.0.0.1:3000:3000`，或内部服务不映射走反代 |
| 3 | build 首行 transferring context: 500MB+ | 缺 .dockerignore，node_modules/.git 全打包 | 配 .dockerignore，可降到 10MB 级 |
| 4 | 上周能构建今天挂 / 生产本地不一致 | latest 漂移、npm install 不读 lockfile | 镜像/包全部 pin 版本 + `npm ci`；主动 `--pull --no-cache` 升级 |
| 5 | 启动后报表不存在/启动即 crash | 裸 depends_on 不等就绪；多副本同时跑迁移竞态 | db healthcheck + service_healthy 门控；单副本 embed 启动自动 Up，多副本拆独立 migrate 服务（R4-坑5/R5-3） |
| 6 | 服务永远 starting→unhealthy | slim 镜像没有 curl/wget | 用镜像自带工具（pg_isready）或构建时装检查工具 + 给足 start_period |
| 7 | .env 出现在镜像层或 git 历史 | 没加 .dockerignore/.gitignore | env_file 运行时注入 + 泄露即轮换 + 清历史 |
| 8 | 部署后仍是旧行为 | 层缓存复用、latest 覆盖、浏览器缓存旧 bundle | 每版唯一 tag、必要时 --no-cache、发布后硬刷新验证 |

## 代码骨架

### api/Dockerfile（Go 多阶段，完整可用）

```dockerfile
# 构建阶段
FROM golang:1.23-alpine AS build
WORKDIR /app
# 层缓存排序：先依赖清单，再源码（R4-A1）
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 静态编译：CGO_ENABLED=0 产物无 libc 依赖，可跑在裸 alpine 上（R5-9）
RUN CGO_ENABLED=0 go build -o /bin/server ./cmd/server

# 运行阶段：只带二进制（goose 迁移已 embed 进二进制，无需 SQL 文件）
FROM alpine:3.20
# 装 wget 供 compose 健康检查；建非 root 用户（R4-A5 坑6）
RUN apk add --no-cache wget && adduser -D -u 10001 appuser
COPY --from=build /bin/server /usr/local/bin/server
USER appuser

EXPOSE 8000

# 端口读环境变量（代码内 env:"PORT"），PaaS 会注入 PORT（R4-A3）
CMD ["server"]
```

### web/Dockerfile（两阶段）+ nginx.conf 要点

```dockerfile
# 构建阶段
FROM node:20-alpine AS builder
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
# VITE_* 变量在构建期烘入产物，改动后必须重新 build
RUN npm run build

# 运行阶段：只带静态产物
FROM nginx:1.27-alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
```

nginx.conf 要点：

```nginx
server {
    listen 80;
    root /usr/share/nginx/html;

    # SPA history 路由回退
    location / {
        try_files $uri $uri/ /index.html;
    }

    # 反代后端，容器网络内用服务名（不是 localhost！）
    location /api/ {
        proxy_pass http://api:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

### docker-compose.yml 全文（黄金标准版）

```yaml
services:
  db:
    image: postgres:17
    env_file: .env
    environment:
      POSTGRES_USER: ${POSTGRES_USER:?Variable not set}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?Variable not set}
      POSTGRES_DB: ${POSTGRES_DB:?Variable not set}
      # PGDATA 指向卷内子目录，规避挂载点权限问题（R4-A2）
      PGDATA: /var/lib/postgresql/data/pgdata
    volumes:
      - db-data:/var/lib/postgresql/data
    healthcheck:
      # 测真实协议可用性而非仅进程存在；start_period 覆盖冷启动
      test: ["CMD-SHELL", "pg_isready -U $${POSTGRES_USER} -d $${POSTGRES_DB}"]
      interval: 5s
      timeout: 5s
      retries: 10
      start_period: 10s
    restart: unless-stopped
    # 不配 ports：只在内部网络以服务名互访（R4-A2）

  api:
    image: ${DOCKER_IMAGE_API:-app-api}:${TAG:?TAG not set}
    build: ./api
    env_file: .env
    environment:
      # 容器网络内用服务名 db（不是 localhost）；pgx 标准连接串
      DATABASE_URL: postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@db:5432/${POSTGRES_DB}?sslmode=disable
      CORS_ORIGINS: ${CORS_ORIGINS:?Variable not set}
    # 迁移 embed 进二进制、启动自动 goose.Up（单副本最省心，R5-3）；
    # 多副本时拆独立 migrate 服务（command: ["server", "migrate"]），api 改等其 service_completed_successfully
    depends_on:
      db:
        condition: service_healthy
    healthcheck:
      # alpine 镜像已装 wget；打 liveness 端点（不依赖 DB）
      test: ["CMD-SHELL", "wget -qO- http://localhost:8000/health || exit 1"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 15s
    restart: unless-stopped

  web:
    image: ${DOCKER_IMAGE_WEB:-app-web}:${TAG:?TAG not set}
    build: ./web
    depends_on:
      api:
        condition: service_healthy
    ports:
      # 唯一对外入口；若宿主机另有反代，改绑 127.0.0.1:80:80
      - "80:80"
    restart: unless-stopped

volumes:
  db-data:
```

对应 `.env.example`：

```bash
POSTGRES_USER=app
POSTGRES_PASSWORD=change-me-to-a-long-random-string
POSTGRES_DB=app
CORS_ORIGINS=http://localhost
TAG=v0.1.0
DOCKER_IMAGE_API=app-api
DOCKER_IMAGE_WEB=app-web
```

## 反模式与红线

- 🚫 `.env` 未进 `.gitignore`/`.dockerignore`、密钥写进 compose yaml 或镜像层（红线，见 security-baseline）。
- 🚫 多副本部署仍在每个副本启动时跑迁移（并发竞态）；裸 `depends_on` 短格式当就绪等待。
- 🚫 应用容器内把数据库指向 `localhost` / `127.0.0.1`（指容器自身）。
- 🚫 基础镜像用 `latest`、Dockerfile 里 `npm install` 代替 `npm ci`。
- 🚫 内部服务（db、api）配 `ports:` 暴露到 0.0.0.0。
- 🚫 健康检查打依赖 DB 的端点当 liveness（DB 抖动导致编排无限重启）。
- 🚫 上线只做「首页能开」检查，不跑注册→登录→核心业务旅程。

## 交付前检查清单

- [ ] `python3 scripts/check_urls.py <项目根>` 无未确认命中（排除 .env*/README/tests 后无 localhost/预览域名硬编码）。
- [ ] `python3 scripts/check_secrets.py <项目根>` 通过（见 security-baseline）。
- [ ] `docker compose build` 全量通过；`transferring context` < 100MB。
- [ ] `docker compose up -d` 后 `docker compose ps`：db=healthy、api/web=healthy；api 启动日志确认迁移成功。
- [ ] `python3 scripts/smoke_test.py <base_url> [--register --login --protected]` 全部通过。
- [ ] 空卷冷启动 `docker compose up -d --wait` 计时跑通。
- [ ] 必填 env 全部 `${VAR:?}` fail-fast；`.env.example` 与实际使用键名一致。
- [ ] 镜像 tag 唯一、非 latest；基础镜像 pin 版本。
- [ ] 认证回调/Site URL 已配生产域名。

## 长尾不做

多机编排（K8s）、蓝绿/金丝雀发布、CDN 调优、多区域部署、服务网格、独立 Traefik 自动证书体系（单机 nginx 够用）、监控告警全家桶——单机 compose + 反代足以撑过 MVP 验证期，规模化后再加（R4-A2/A4）。
