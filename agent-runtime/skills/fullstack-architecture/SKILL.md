---
name: fullstack-architecture
description: 初始化新项目或新增功能模块时使用。规定全栈目录宪法（前端 React/Vue 双路线二选一、后端 Go cmd/server+internal 按域分包）、前后端边界（统一 apiClient、/api/v1 前缀、错误契约）、栈锁定表与环境变量规范。关键词：初始化项目、目录结构、新建模块、架构、脚手架、技术栈。
---

# Fullstack Architecture —— 目录宪法与前后端边界

> 场景：从零初始化项目，或在既有项目中新增业务模块。结构先于代码——agent 生成代码量大、用户无技术能力维护，**清晰边界比少文件更重要**（R2-1）。
> v2 变更：前端 React/Vue 双路线二选一；后端统一 Go（DESIGN §1）。

## 何时使用

1. 新需求确认后，开始搭建项目骨架、选定前端路线时；
2. 新增一个业务模块（feature / 资源）需要决定文件放哪时；
3. 前后端联调出现「请求打不通 / 路径对不上」等契约问题时；
4. 用户问「代码应该怎么组织」「前后端怎么连」时。

## 规范做法

### 一、技术栈锁定表（v2，DESIGN §1）

本库所有代码骨架只出现这一套答案，不讨论「要不要用 Next.js / GraphQL / MongoDB / Redis」——默认答案是「不」：

| 层 | 默认答案 | 备选/升级边界 | 来源 |
|---|---|---|---|
| 前端路线 | **React**（生态与 LLM 语料最大） | 用户要求 Vue 时切 Vue 路线（见「路线选择」） | R6 |
| React 栈 | Vite + TS + React Router + TanStack Query + RHF/zod | — | R1 |
| Vue 栈 | Vite + Vue 3 `<script setup lang="ts">` + Vue Router + Pinia + vue-query + **Element Plus** + vee-validate/zod | UI 库备选 Naive UI | R6 |
| Go 路由 | **标准库 net/http ServeMux（Go 1.22+）** | 路由组/正则 → chi；禁 gorilla | R5 |
| Go 布局 | cmd/server + internal/ 按域分包 | 禁 project-layout 全家桶、禁 Clean Arch 四层 | R5 |
| 数据访问 | **sqlc + pgx（database/sql 接口）** | 高频原型 → GORM（AutoMigrate 仅开发） | R5 |
| 迁移 | **goose，embed 进二进制启动自动执行** | — | R5 |
| 配置/日志/校验 | caarlos0/env + log/slog + go-playground/validator v10 | 禁 viper | R5 |
| 认证 | **scs/v2 session cookie + x/crypto/bcrypt** | 禁 gorilla/sessions、不默认 JWT | R5 |
| 数据库 | PostgreSQL | timestamptz/UTC，索引默认 | R2/R5 |
| 部署 | Docker 多阶段：Go 静态编译（CGO_ENABLED=0）+ 前端 node→nginx + compose 门控 | 见 deploy-ship | R4/R5 |

### 二、路线选择（前端二选一）

1. **默认 React**：用户没有任何前端偏好时，一律走 React 路线（生态与 LLM 语料最大，生成质量最稳）。
2. **切 Vue 的信号**：用户明确要求 Vue，或提到「Vue」「饿了么 UI」「Element」「Element Plus」「Naive UI」等关键词时，整条前端路线切到 Vue 栈（R6）。
3. **一次项目只用一条路线，🚫 不混用**：选 React 则前端细节查 frontend-react skill，选 Vue 则查 frontend-vue skill；两套栈的依赖、写法、组件库不得出现在同一项目。
4. 路线决定在脚手架阶段一次性拍板并写入 README，中途不切换。

### 三、仓库总布局（前后端同仓）

```
project/
├── web/            # 前端（Vite + TS；React 或 Vue，按路线选择）
├── api/            # 后端（Go）
├── docker-compose.yml
├── .env            # 本地开发，必须进 .gitignore 与 .dockerignore
├── .env.example    # 入库，键名 + 空值/占位值
└── README.md       # 记录前端路线选择结果
```

### 四、前端目录宪法：feature-based（R1-五、R6-5）

按业务域组织，不按技术类型平铺（不要 `components/ + hooks/ + utils/` 铺全应用）。两条路线的骨架分别见 frontend-react / frontend-vue skill，此处只定共同边界：

```
web/src/
├── app/ 或 main 入口     # 路由表、providers 装配（QueryClientProvider / VueQueryPlugin 等）
├── components/           # 跨 feature 共享的通用组件（Button、Modal、EmptyState）
├── features/
│   └── <name>/           # 业务域，如 auth / orders
│       ├── api/            # 该域的请求函数（只允许通过 apiClient 发请求）
│       ├── components/     # 该域私有组件
│       ├── types.ts        # 该域类型
│       └── index.ts        # 唯一对外出口；跨 feature 只许从 index.ts 导入
├── lib/                  # apiClient 等基础设施
└── （React）hooks/ ｜（Vue）composables/ stores/
```

三条边界规则（R1-13/14）：
1. 跨 feature 导入只走对方的 `index.ts` 出口，形成模块边界；
2. 共享组件保持无业务语义（Button 不知道什么叫「订单」）；
3. 组件单一职责，组合优先于配置——props 爆炸时拆组件。

### 五、后端目录宪法：Go cmd/server + internal/ 按域分包（R5-2）

`golang-standards/project-layout` **不是官方标准**（Go 团队明确其误导性），🚫 不套它的全家桶；🚫 不套 Clean Arch 四层（handler/service/repository/entity 对一次性交付的中小应用是过度设计）。**入口只做装配，internal 按域分包**：

```
api/
├── cmd/server/main.go    # 唯一入口：读 env → 连库 Ping → 装中间件 → 注册路由 → 起服务 → 优雅停机
├── internal/             # 不允许外部导入，天然私有
│   ├── server/           # 路由装配、中间件链（日志/请求ID/recover/CORS）、统一错误响应
│   ├── items/            # 业务域：handler + 领域逻辑 + 数据访问（sqlc 生成代码置此域内）
│   └── users/            # 业务域：登录、会话、用户
├── migrations/           # goose SQL 迁移，embed 进二进制，启动时自动 Up
├── sqlc.yaml + schema.sql + queries.sql
└── go.mod
```

职责边界（R5-2、R5-7.1）：
- `cmd/server/main.go` 只做装配，不写任何业务逻辑；
- handler 薄：只做「解码 → 校验 → 调用领域函数 → 编码响应」，不写业务逻辑；
- 领域逻辑不 import net/http 与 database/sql，依赖通过构造函数显式注入（🚫 不用 wire/fx 等 DI 框架）；
- 从扁平开始，需要时才加目录；按域分包优先于按层分包。

### 六、前后端边界（契约层）

1. **统一前缀与版本**：所有业务 API 挂在 `/api/v1` 前缀下（`/api/v1/items`）；健康检查用 `/health`（liveness，不查 DB）与 `/health/ready`（readiness，查 DB Ping）（R2-2、R2-5）。
2. **统一 API 层**：前端所有请求收敛到唯一 `apiClient`（baseURL + 错误规范化 `if (!res.ok) throw` + 401 统一跳登录），组件禁止直接 fetch（R1-二）。
3. **URL 设计**：名词复数资源（`/items`）、层级表达从属（`/users/{id}/items`）、GET/POST/PUT/PATCH/DELETE 各守幂等语义；列表端点必须带默认分页 `?limit=100&offset=0`（limit 上限 100）且返回 `total`（R2-2）。Go ServeMux 路由直接写 `"GET /api/v1/items/{id}"` 模式，路径参数用 `r.PathValue("id")` 取出（R5-1）。
4. **错误契约**：统一错误体 `{"error":{"code","message","details","request_id"}}`（嵌套式，request_id 在 error 对象内）；后端由一个集中的 `respondError(w, err)` 用 `errors.Is/As` 映射哨兵错误/APIError，绝不把内部错误原文透传给客户端；状态码约定 200/201/204/400/401/403/404/405/409/500/504（R2-2、R5-5）。
5. **API 文档**：Go 标准库路由无自动 OpenAPI，契约以「端点表 + 请求/响应示例」维护在 api/README.md，前端 types 与其对齐；若用户明确要求 OpenAPI 再引入代码生成（属长尾，默认不做）。
6. **CORS**：开发环境用 Vite `server.proxy` 把 `/api` 代理到后端（同源，无 CORS）；生产前后端同源部署（反代）或后端中间件枚举白名单 origin，绝不 `*`（R1-坑4、R3-四）。

### 七、环境变量规范总则（R2-坑10、R1-坑5、R4-A3、R5-4）

1. 一切配置走环境变量，密钥绝不入库、不入镜像、不入前端 bundle；仓库只放 `.env.example`。
2. 后端：`caarlos0/env` struct tag 映射 + 启动即校验（fail-fast）；🚫 不引入 viper；compose 必填变量用 `${VAR:?Variable not set}`。
3. 前端：只用 `VITE_` 前缀 + `import.meta.env.VITE_X` 字面量点访问；`VITE_` 变量会打进公开 bundle，**绝不放密钥**。
4. URL 全部走环境变量，代码中禁止硬编码 `localhost`/预览域名（上线前排障见 debug-playbook「环境混淆」）。

## 代码骨架

### 后端应用装配骨架（api/cmd/server/main.go；items/users/server 包按同一模式类推）

```go
// cmd/server/main.go —— 入口只做装配：
// 读 env → 连库 PingContext → 装中间件链 → 注册 ServeMux 路由 → 带超时的 http.Server → signal 优雅停机
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx 的 database/sql 驱动

	"myapp/internal/items"
	"myapp/internal/server"
	"myapp/internal/users"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. 配置：环境变量，缺失即 fail-fast（正式项目用 caarlos0/env 映射 struct）
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL 未设置")
	}

	// 2. 连库并验证：sql.Open 不建立真实连接，必须 PingContext（R5-7.2）
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	// 连接池四参数（R5-7.1）：默认值会打满数据库连接
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return err
	}

	// 3. 装配依赖：构造函数显式注入，不用 DI 框架（R5-7.1）
	itemsHandler := &items.Handler{DB: db}
	usersHandler := &users.Handler{DB: db}

	// 4. 注册路由：Go 1.22+ ServeMux「方法 + 路径变量」模式（R5-1）
	//    模式冲突在注册时 panic —— 启动即暴露，是好事
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`)) // liveness：不依赖 DB
	})
	mux.HandleFunc("GET /api/v1/items", itemsHandler.List)
	mux.HandleFunc("POST /api/v1/items", itemsHandler.Create)
	mux.HandleFunc("GET /api/v1/items/{id}", itemsHandler.Get)
	mux.HandleFunc("POST /api/v1/login", usersHandler.Login)

	// 5. 中间件链：纯函数式 func(http.Handler) http.Handler（R5-4）
	handler := server.Chain(
		server.Recover,  // panic 兜底放最外层
		server.RequestID,
		server.Logging,
	)(mux)

	// 6. 带超时的 http.Server：🚫 禁止裸 http.ListenAndServe（无超时，慢客户端可拖死服务，R5-7.11）
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadTimeout:       5 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 7. 优雅停机：SIGINT/SIGTERM 后停止接收新连接，等在途请求完成（R5-7.10）
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-sigCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
```

### 前端统一 apiClient（web/src/lib/apiClient.ts，两条路线共用）

```ts
// 全应用唯一请求出口：baseURL + 错误规范化 + 401 统一跳转
export async function apiClient<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    credentials: "include", // cookie session
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  if (res.status === 401) {
    window.location.href = "/login"; // 统一跳转
    throw new Error("unauthorized");
  }
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error(body?.error?.message ?? `请求失败（${res.status}）`);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}
```

路由装配（路由级懒加载 + 404 兜底）按所选路线抄 frontend-react / frontend-vue skill 的骨架，不在此处重复。

## 反模式与红线

- 🚫 同一项目混用 React 与 Vue 两条路线；或用户提 Vue 信号仍默认 React（DESIGN §1）。
- 🚫 前端按 `components/ + hooks/ + utils/` 技术类型平铺全应用（R1-13）。
- 🚫 组件内直接 `fetch(...)`，绕过统一 apiClient（R1-8）。
- 🚫 Go 项目套 project-layout 全家桶或 Clean Arch 四层；main.go 里写业务逻辑（R5-2）。
- 🚫 引入 gorilla/mux、gorilla/sessions（已归档）、viper、wire/fx（R5-1/4/9）。
- 🚫 handler 里写业务逻辑/裸 SQL 拼接；领域层 import net/http（R5-5、R5-7.1）。
- 🚫 响应结构体带 `password_hash` 等敏感字段直接返回（等同 v1 的「返回整个 ORM 对象」，R2-坑8）。
- 🚫 用 GORM `AutoMigrate` / `CREATE TABLE IF NOT EXISTS` 代替 goose 迁移上线（R5-3）。
- 🚫 裸 `http.ListenAndServe(addr, handler)` 起服务（无超时）；无优雅停机（R5-7.10/11）。
- 🚫 代码中硬编码任何 URL、密钥、环境判断（R4-B3 失败模式四）。
- 🚫 无版本前缀直接挂 `/items`；列表端点无分页默认 limit（R2-2）。

## 交付前检查清单

- [ ] README 记录了前端路线选择（React 或 Vue），全项目只用一条路线
- [ ] 目录结构符合本 skill 的目录宪法（前端 feature-based、后端 cmd/server + internal 按域分包）
- [ ] 新业务模块有 `index.ts` 出口，跨 feature 导入未穿透内部文件
- [ ] 所有端点挂 `/api/v1` 前缀，错误体统一 `{"error":{"code","message","details","request_id"}}`，列表端点有分页
- [ ] 前端只有 `lib/apiClient.ts` 一处发请求；后端 handler 薄、领域层无 net/http import
- [ ] 后端 `go vet ./...` 与 `go build ./...` 通过；迁移走 goose embed，启动自动 Up
- [ ] `.env.example` 齐全，代码中 grep 不到 `localhost` 与硬编码密钥
- [ ] 前端跑 `python3 frontend-react/scripts/check_frontend.py web` 无红线命中（React/Vue 均适用）

## 长尾不做（本 skill 域内）

SSR（Next.js/Nuxt）、微前端、GraphQL、微服务、消息队列、多租户、Redis 缓存、ES 全文检索、WebSocket 实时推送、gRPC、CQRS/事件溯源、Clean Arch 四层、DI 框架、Kubernetes 清单、自动 OpenAPI 生成——默认不做；任何引入「第二个有状态组件」的决定必须由用户明确需求触发（R1-附、R2-7、R5-9、R6-9）。
