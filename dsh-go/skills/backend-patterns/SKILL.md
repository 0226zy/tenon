---
name: backend-patterns
description: Go（net/http ServeMux + sqlc + goose + Postgres）后端工程的默认规范与高频坑。写、改、审查任何 Go 后端代码时使用：cmd/internal 布局、路由、统一错误契约、函数式中间件链、sqlc/goose 数据层、连接池、优雅停机、httptest 测试纪律。触发关键词：后端、接口、API、Go、路由、数据库、迁移、报错、接口慢、加字段。
---

# backend-patterns — Go 后端工程规范

## 何时使用

- 初始化 Go 后端项目或新增业务模块时（先定骨架再写代码）；写/改路由、handler、sqlc 查询、goose 迁移、中间件时。
- 接口变慢、数据没写入、报错被吞、连接耗尽等异常排查（查「高频坑 Top 12」），及认证/日志/健康检查/优雅停机场景；交付前必须运行 `scripts/check_backend.py`（exit 0 才算过）。

## 规范做法

### 1. 布局与职责边界（R5-2）

`cmd/server` 只做装配，`internal/` 按域分包（group by feature），不按 handler/service/repository 三层分包（那是「用 Go 写 Java」）：

```
myapp/
├── cmd/server/main.go     # 只做装配：读配置、连库、跑迁移、装中间件、注册路由、起服务、监听信号
├── internal/
│   ├── httpapi/           # 路由、中间件、统一错误处理（net/http 只允许出现在各域 handler 文件与中间件中）
│   ├── items/             # 按域分包，含自己的 handler.go、领域函数与数据访问（领域逻辑与数据访问不 import net/http 与 database/sql）
│   └── migrations/        # goose SQL 迁移 + embed.FS
├── schema.sql / queries.sql
└── go.mod
```

职责红线（R5-7.1，高星骨架共性）：

1. **入口只做装配**——main.go 里不允许出现业务逻辑。
2. **handler 薄**：只做「解码 → 校验 → 调用领域函数 → 编码响应」四步；业务规则与 SQL 进领域包。
3. 依赖通过构造函数显式注入（不用 wire/fx 等 DI 框架）；错误包装一律 `fmt.Errorf("...: %w", err)`；**HTTP 状态码只在 handler 文件与中间件中出现**，领域逻辑返回哨兵错误（`ErrNotFound` 等）或普通 error（R5-5）。
4. 🚫 禁 `golang-standards/project-layout` 全家桶（非官方标准）；🚫 禁 Clean Architecture 四层骨架（对一次性交付是过度设计）。

### 2. 路由：Go 1.22+ 标准库 ServeMux（R5-1）

默认用标准库 `net/http.ServeMux`，零外部依赖。关键细节：

1. 模式格式 `"METHOD /path/{param}"`：`mux.HandleFunc("GET /api/v1/items/{id}", h.get)`；注册 `GET` 自动覆盖 `HEAD`。
2. `{id}` 匹配单个路径段，`{path...}` 捕获剩余全部且必须位于末尾；精确匹配（防子路径兜底）用 `{$}`，如 `/admin/{$}`。
3. 路径参数用 `r.PathValue("id")` 取出（字符串），数字必须 `strconv.ParseInt`/`Atoi` 并处理错误。
4. 已注册路径但方法不匹配时**自动返回 405 + Allow 头**，无需手写方法分派。
5. **模式冲突在注册时 panic——这是优点**：错误在启动即暴露，不会带病上线；`/api` 与 `/api/` 是两个不同模式（前者会 301），路由设计时避开歧义。

升级 chi 的三条触发条件（满足任一才换；chi 完全兼容 net/http，handlers 不用改）：(a) 需要按路由组挂载不同中间件；(b) 需要正则路由（如 `/date/{yyyy:\d\d\d\d}/`）；(c) 嵌套路由多到 ServeMux 表达吃力。🚫 禁 gorilla/mux（2023 年归档，份额持续下滑）；不默认 Gin（`*gin.Context` 与 net/http 中间件生态不完全互通）。

### 3. 标配库与函数式中间件链（R5-4）

| 职责 | 默认答案 | 说明 |
|---|---|---|
| 配置 | `caarlos0/env`（struct tag 映射环境变量） | 🚫 禁 viper（大而全，单容器应用是负担）；必填项用 `required` tag fail-fast |
| 日志 | 标准库 `log/slog` + `NewJSONHandler` 输出 stdout | 无需 zap/zerolog；中间件注入 request-scoped 字段 |
| 校验 | `go-playground/validator/v10` | 锁定 v10 并封装在自己接口后（仓库挂了 Call for Maintainers） |
| HTTP 中间件 | 纯函数式 `func(http.Handler) http.Handler` 链 | 与标准库、chi 完全兼容（Alex Edwards 经典写法） |

中间件链顺序固定为：**recover（最外层）→ 请求 ID → 访问日志 → CORS → mux**。每请求一条访问日志含五元组：方法、路径、状态码、耗时 ms、request_id——覆盖 90% 排障场景。健康检查分离：`/health` 只做 liveness（**不查 DB**）；`/health/ready` 做 readiness（查 DB），防 DB 抖动导致编排无限重启。

### 4. API 契约与统一错误处理（R5-5）

1. URL 用名词复数 + 版本前缀 `/api/v1`：`/api/v1/items`；URL 中禁止动词；从属关系用层级 `/users/{id}/items`。
2. **统一错误体**：所有非 2xx 返回同一结构 `{"error":{"code":"VALIDATION_FAILED","message":"...","details":{...}}}`。
3. **集中映射**：handler 内部只返回 `error`，由唯一的 `respondError(w, r, err)` 用 `errors.As` 找 `*APIError`、`errors.Is` 匹配哨兵错误（`sql.ErrNoRows`→404、`context.DeadlineExceeded`→504），兜底 500——**内部错误原文绝不透传给客户端**（日志记完整 err + request_id，响应只给通用文案）。
4. 状态码约定表：

| 场景 | 状态码 |
|---|---|
| 读取成功 | 200 |
| 创建成功 | 201 |
| 删除成功（无响应体） | 204 |
| JSON 非法 / 参数校验失败 | 400 |
| 未认证 | 401 |
| 已认证但无权限（owner 校验失败） | 403 |
| 资源不存在 | 404 |
| 方法不允许（ServeMux 自动） | 405 |
| 唯一约束等冲突 | 409 |
| 服务端错误（原文不透传） | 500 |
| 下游/DB 超时 | 504 |

5. 解码请求体必须 `http.MaxBytesReader` 限大小（默认 1MB）+ `DisallowUnknownFields()`（未知字段报错而非静默忽略，R5-坑6）。
6. 分页基线：`?limit=50&offset=0`，**limit 必须设上限（≤100），禁止无界全表返回**；响应至少含 `total`。
7. 出参绝不含密码哈希等敏感字段；写路径入参不接受客户端传来的 id/owner_id/role（mass assignment 防护，见 security-baseline）。

### 5. 数据层：sqlc + goose + 连接池（R5-3）

1. **sqlc 工作流（默认）**：维护 `schema.sql`（DDL）+ `queries.sql`（`-- name: GetItem :one` 命名查询）→ `sqlc generate` 产出类型安全 Go 方法。类型错配在生成期暴露，无 ORM 黑盒、无运行时反射 panic；agent 只改 SQL 文件。
2. **goose 迁移（红线）**：一切 schema 变更走 goose SQL 迁移文件（`-- +goose Up` / `-- +goose Down` 同文件）；迁移目录用 `embed.FS` 嵌进二进制，**启动时自动 `goose.Up`**——单体交付最省心的姿势。🚫 禁 `AutoMigrate` 上线（check_backend.py BE04 会拦）。
3. **连接池四参数必配**（`database/sql` 池默认 MaxOpenConns 无上限、MaxIdleConns 仅 2，高峰期必打满）：

```go
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(5 * time.Minute)
db.SetConnMaxIdleTime(1 * time.Minute)
```

4. **`sql.Open` 不建立真实连接**（R5-坑2）：启动必须 `db.PingContext(ctx)`，失败即退出（fail fast），否则程序启动成功、第一个请求才爆。
5. `rows, err := db.QueryContext(...)` 后**立即** `defer rows.Close()`，迭代完检查 `rows.Err()`（R5-坑3）。
6. 时间列一律 `TIMESTAMPTZ`，应用内一律 UTC 存储与传输（RFC3339），展示层才转时区；`time.Time` 比较用 `.Equal()` 而非 `==`（R5-坑9）。
7. 所有 DB 调用带 `ctx`；脱离请求生命周期的异步任务用 `context.WithoutCancel(ctx)` 派生新 ctx，不得沿用已取消的请求 ctx（R5-坑7）。

### 6. 超时与优雅停机（R5-坑10/11）

1. 🚫 禁裸 `http.ListenAndServe(addr, handler)`（无超时，慢客户端 Slowloris 可拖死服务）。必须构造 `http.Server{ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout}`。
2. 优雅停机是交付级要求：`signal.NotifyContext(SIGINT, SIGTERM)` + `srv.Shutdown(ctx)`（带超时），否则部署时正在处理的请求被砍断。普通端点可用 `http.TimeoutHandler` 包总超时；SSE/长轮询端点单独用 `http.NewResponseController` 放宽写超时。

### 7. 测试纪律（R5-8）

1. handler 层用 `net/http/httptest`（`httptest.NewRequest` + `httptest.NewRecorder()`，直接 `mux.ServeHTTP(rec, req)` 测整条路由+中间件链，无需起端口）；表驱动是铁律：用例切片 `{name, input, wantStatus, wantBody}` 循环 `t.Run`，断言辅助标 `t.Helper()`，不依赖执行顺序与全局状态。
2. **每端点 ≥3 个用例**：正常路径（状态码+响应体）、校验失败（400+错误体结构）、资源不存在（404）；认证路由必须再加一个无 cookie/无效凭证的 401 用例。
3. 领域函数优先接受接口而非 `*sql.DB` 具体类型，便于内存假实现；数据层测试用 testcontainers-go 或独立测试库，🚫 不允许测生产库。
4. `go vet ./...` 与 `go test ./... -race` 必须全绿（并发 bug 靠 -race 兜底，R5-坑12）。

## 代码骨架

### 1. main.go 装配（cmd/server/main.go）

```go
package main
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"github.com/caarlos0/env/v11"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx 的 database/sql 驱动
	"github.com/pressly/goose/v3"
	"myapp/internal/httpapi"
	"myapp/internal/items"
	"myapp/internal/migrations"
)

type config struct {
	DatabaseURL string   `env:"DATABASE_URL,required"`
	Port        int      `env:"PORT" envDefault:"8000"`
	CORSOrigins []string `env:"CORS_ORIGINS" envSeparator:","`
}
func fatal(msg string, err error) { slog.Error(msg, "err", err); os.Exit(1) }
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var cfg config // 必填变量缺失即 fail-fast
	if err := env.Parse(&cfg); err != nil {
		fatal("config parse failed", err)
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL) // 注意：Open 不建立真实连接
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil { // 启动即 Ping，fail-fast（R5-坑2）
		fatal("database ping failed", err)
	}
	db.SetMaxOpenConns(25) // 连接池四参数（默认值会打满数据库，R5-坑1）
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)
	// 迁移 embed 进二进制，启动自动 Up（R5-3）
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		fatal("goose dialect", err)
	}
	if err := goose.Up(db, "."); err != nil { fatal("migration failed", err) }
	// 装配：mux 注册路由 + 函数式中间件链（recover 在最外层）
	mux := http.NewServeMux()
	httpapi.NewItemsHandler(items.NewService(db)).Register(mux)
	// liveness：不查 DB
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /health/ready", httpapi.ReadyHandler(db))
	handler := httpapi.Chain(httpapi.Recover, httpapi.RequestID,
		httpapi.Logging, httpapi.CORS(cfg.CORSOrigins))(mux) // Logging/CORS 写法见 middleware.go 与 security-baseline
	srv := &http.Server{ // 禁裸 ListenAndServe，四个超时必配（R5-坑11）
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	// 优雅停机：SIGINT/SIGTERM → Shutdown（R5-坑10）
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		slog.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("server failed", err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fatal("graceful shutdown failed", err)
	}
}
```

### 2. 统一错误处理（internal/httpapi/errors.go）

```go
package httpapi
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// APIError：带 HTTP 状态码的业务错误，只在 httpapi 层使用（R5-5）
type APIError struct {
	HTTPStatus int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Details    any    `json:"details,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}
func (e *APIError) Error() string { return e.Code + ": " + e.Message }
var ErrNotFound = errors.New("not found") // 领域层哨兵错误
func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil { slog.Error("encode response failed", "err", err) }
}
// respondError 是唯一错误出口：As 找 APIError → Is 匹配哨兵 → 兜底 500
func respondError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		// 业务错误：按 APIError 自带状态码返回
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrNotFound):
		apiErr = &APIError{HTTPStatus: http.StatusNotFound, Code: "NOT_FOUND", Message: "资源不存在"}
	case errors.Is(err, context.DeadlineExceeded):
		apiErr = &APIError{HTTPStatus: http.StatusGatewayTimeout, Code: "TIMEOUT", Message: "请求超时"}
	default: // 未知错误：日志记完整原文 + request_id，响应只给通用文案（R5-5）
		slog.Error("internal error", "err", err, "path", r.URL.Path, "request_id", requestID(r.Context()))
		apiErr = &APIError{HTTPStatus: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "服务器内部错误"}
	}
	apiErr.RequestID = requestID(r.Context()) // 错误体带 request_id，客户端报障可定位日志
	respondJSON(w, apiErr.HTTPStatus, map[string]any{"error": apiErr}) // 嵌套错误体 {"error":{...}}
}
```

### 3. 标准 CRUD handler（ServeMux + 校验 + owner 检查）

```go
package httpapi
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"github.com/go-playground/validator/v10"
)

// 出参契约：绝不含密码哈希等敏感字段；owner_id 不外泄
type Item struct {
	ID          int64  `json:"id"`
	OwnerID     int64  `json:"-"`
	Title       string `json:"title"`
	Description string `json:"description"`
}
// 入参契约：不接受客户端传来的 id/owner_id/role（mass assignment 防护）
type CreateItemInput struct {
	Title       string `json:"title" validate:"required,min=1,max=200"`
	Description string `json:"description" validate:"max=2000"`
}
// 依赖以接口注入，测试可用内存假实现
type ItemService interface {
	Create(ctx context.Context, ownerID int64, in CreateItemInput) (Item, error)
	Get(ctx context.Context, id int64) (Item, error)
}
type ItemsHandler struct {
	svc      ItemService
	validate *validator.Validate
}
func NewItemsHandler(svc ItemService) *ItemsHandler {
	return &ItemsHandler{svc: svc, validate: validator.New()}
}
func (h *ItemsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/items", h.create)
	mux.HandleFunc("GET /api/v1/items/{id}", h.get)
	// PATCH/DELETE /api/v1/items/{id} 同构复刻：取 id → 校验 → owner 检查 → 执行
}
func (h *ItemsHandler) create(w http.ResponseWriter, r *http.Request) {
	uid, err := currentUserID(r) // 1. 认证（默认拒绝，未登录 401）
	if err != nil { respondError(w, r, err); return }
	var in CreateItemInput
	if err := decodeJSON(w, r, &in); err != nil { // 2. 解码（限大小 + 拒未知字段）
		respondError(w, r, err)
		return
	}
	if err := h.validate.Struct(in); err != nil { // 3. 校验
		respondError(w, r, &APIError{HTTPStatus: http.StatusBadRequest, Code: "VALIDATION_FAILED",
			Message: "参数校验失败", Details: validationDetails(err)})
		return
	}
	item, err := h.svc.Create(r.Context(), uid, in) // 4. 执行：owner_id 由服务端注入
	if err != nil { respondError(w, r, err); return }
	respondJSON(w, http.StatusCreated, item)
}
func (h *ItemsHandler) get(w http.ResponseWriter, r *http.Request) {
	uid, err := currentUserID(r)
	if err != nil { respondError(w, r, err); return }
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64) // PathValue 是字符串，必须显式转换
	if err != nil {
		respondError(w, r, &APIError{HTTPStatus: http.StatusBadRequest, Code: "INVALID_ID", Message: "id 必须是整数"})
		return
	}
	item, err := h.svc.Get(r.Context(), id)
	if err != nil { // sql.ErrNoRows 由 respondError 统一映射为 404
		respondError(w, r, err)
		return
	}
	if item.OwnerID != uid { // 授权：对象级归属校验，防 IDOR/BOLA（security-baseline 红线 5）
		respondError(w, r, &APIError{HTTPStatus: http.StatusForbidden, Code: "FORBIDDEN", Message: "无权访问该资源"})
		return
	}
	respondJSON(w, http.StatusOK, item)
}
// ---- handler 公共助手 ----
var errUnauthorized = &APIError{HTTPStatus: http.StatusUnauthorized, Code: "UNAUTHORIZED", Message: "未登录"}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB 上限防 OOM（R5-坑6）
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // 未知字段报错而非静默忽略
	if err := dec.Decode(dst); err != nil {
		return &APIError{HTTPStatus: http.StatusBadRequest, Code: "INVALID_JSON", Message: "请求体不是合法 JSON 或含未知字段"}
	}
	return nil
}
func currentUserID(r *http.Request) (int64, error) {
	uid, ok := r.Context().Value(userIDKey).(int64) // 由认证中间件写入
	if !ok { return 0, errUnauthorized }
	return uid, nil
}
func validationDetails(err error) map[string]string {
	details := map[string]string{}
	var ves validator.ValidationErrors
	if errors.As(err, &ves) {
		for _, fe := range ves { details[fe.Field()] = fe.Tag() }
	}
	return details
}
```

### 4. 函数式中间件链（internal/httpapi/middleware.go）

```go
package httpapi
import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"github.com/google/uuid"
)

type ctxKey string
const (
	requestIDKey ctxKey = "requestID"
	userIDKey    ctxKey = "userID" // 由认证中间件写入（见 security-baseline）
)

type Middleware func(http.Handler) http.Handler
// Chain(mw1, mw2)(mux)：mw1 在最外层，先执行（Alex Edwards 惯用法）
func Chain(mws ...Middleware) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		return h
	}
}
func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
// Recover 放链最外层：兜底 panic，日志记原文，响应给通用 500
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { if rec := recover(); rec != nil { respondError(w, r, fmt.Errorf("panic: %v", rec)) } }()
		next.ServeHTTP(w, r)
	})
}
// RequestID 生成/透传 X-Request-ID，写入 context 与响应头
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" { id = uuid.NewString() }
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}
// Logging 每请求输出一条五元组访问日志：method/path/status/duration_ms/request_id
type statusWriter struct {
	http.ResponseWriter
	status int
}
func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(), "request_id", requestID(r.Context()))
	})
}

// ReadyHandler 是 readiness 探针（查 DB）；liveness 的 /health 不查 DB
func ReadyHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil { respondError(w, r, err); return }
		w.WriteHeader(http.StatusOK)
	}
}
```

### 5. goose embed 迁移（sqlc 的 queries.sql 命名查询写法见规范做法 §5）

```go
// internal/migrations/migrations.go —— 迁移文件嵌进二进制，启动自动 Up
package migrations
import "embed"
//go:embed *.sql
var FS embed.FS
```

```sql
-- internal/migrations/00001_create_items.sql
-- +goose Up
CREATE TABLE items (
    id          BIGSERIAL PRIMARY KEY,
    owner_id    BIGINT      NOT NULL REFERENCES users(id),
    title       TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()  -- 一律 timestamptz + UTC
);
CREATE INDEX idx_items_owner_id ON items(owner_id); -- 出现在 WHERE 的列建索引
-- +goose Down
DROP TABLE items;
```

## 反模式与红线

- 🚫 `fmt.Sprintf` 拼接 SQL（含动态列名/表名，须白名单映射）——SQL 注入，check_backend.py BE01 拦截（R3-四.1）。
- 🚫 裸 `http.ListenAndServe(` / `http.ListenAndServeTLS(`——无超时配置，慢客户端拖死服务，BE02 拦截（R5-坑11）。
- 🚫 import `gorilla/mux` / `gorilla/sessions`——2023 年已归档，BE03 拦截（R5-1/6）。
- 🚫 `AutoMigrate(` 当生产迁移——schema 漂移无药可救，一切变更走 goose，BE04 拦截（R5-3）。
- 🚫 `_ = rows.Close()`、err 赋了不检查——静默丢数据，接口 200 但写入失败；CI 挂 errcheck（R5-坑5）。
- 🚫 把内部错误原文透传给客户端——500 只回通用文案，日志记完整 err；领域层不 import `net/http`，HTTP 映射集中在 httpapi 层（R5-5）。
- 🚫 出参带敏感字段、写路径接受客户端传入 id/owner_id/role（R3-五）；列表端点 limit 无上限。
- 🚫 把 ctx 存进 struct、后台 goroutine 沿用已取消的请求 ctx——异步任务用 `context.WithoutCancel` 派生（R5-坑7）。

## 高频坑 Top 12 速查（R5-7）

| # | 症状 | 根因（一行） | 解法（一行） |
|---|---|---|---|
| 1 | `too many connections` / 高峰请求挂起 | 池默认 MaxOpenConns 无上限、MaxIdleConns=2 | 四参数：SetMaxOpenConns(25)/SetMaxIdleConns(10)/SetConnMaxLifetime(5min)/SetConnMaxIdleTime(1min) |
| 2 | 启动正常但首个请求才报连库失败 | `sql.Open` 不建立真实连接 | 启动即 `PingContext`，失败 os.Exit(1)（fail-fast） |
| 3 | 连接数只涨不跌直至耗尽 | 忘关 rows 导致连接泄漏 | `QueryContext` 后立即 `defer rows.Close()`；迭代完查 `rows.Err()` |
| 4 | 启动 panic: pattern conflict / 路由被遮蔽 | 同优先级模式冲突；`/api` 与 `/api/` 是两个模式 | 启动 panic 是优点尽早暴露；精确匹配用 `{$}` 后缀 |
| 5 | 接口 200 但数据没写、日志干净 | `_ = rows.Close()`、err 未检查被吞 | 每个 `err != nil` 必处理；CI 挂 errcheck/golangci-lint |
| 6 | 0/false/"" 字段消失 / 未知字段静默吞掉 | `omitempty` 与解码器默认行为 | 可空字段用 `*int` 指针；解码开 `DisallowUnknownFields()` + `MaxBytesReader` |
| 7 | 异步任务数据写一半 / 请求结束后 DB 报错 | 后台 goroutine 沿用已取消的请求 ctx | ctx 显式传参；异步用 `context.WithoutCancel` 派生；DB 调用带超时 |
| 8 | 内存持续增长 / 服务越来越慢 | goroutine 泄漏：无缓冲 channel 无人接收、循环里 `time.After` | 每个 goroutine 有退出路径；循环用 `time.NewTicker` + `defer ticker.Stop()` |
| 9 | 时间差 8 小时 / 比较错乱 | `time.Now()` 本地时区入库、`==` 比 time.Time | 一律 UTC 存传（RFC3339）；比较用 `.Equal()`；展示层才转时区 |
| 10 | 部署时进行中的请求被砍断 | 没有优雅停机 | `signal.NotifyContext(SIGINT, SIGTERM)` + `srv.Shutdown(ctx)` |
| 11 | 慢客户端拖死服务（Slowloris） | 裸 `http.ListenAndServe` 无超时 | 构造 `http.Server` 配 Read/ReadHeader/Write/Idle 四个超时 |
| 12 | `fatal error: concurrent map writes` | handler 每请求一个 goroutine，共享 map 无锁 | 共享 map 加 `sync.RWMutex` 或改 `sync.Map`；`-race` 测试兜底 |

## 交付前检查清单

- [ ] `python3 scripts/check_backend.py <api目录>` 通过（exit 0）。
- [ ] 路由全部用 ServeMux 方法+`{id}` 模式注册，启动无 panic；无 gorilla 依赖。
- [ ] 统一错误体 `{"error":{...}}`；sql.ErrNoRows→404 等映射集中在 respondError；500 不透传内部错误原文；列表端点 limit ≤100；写路径入参不含 id/owner_id/role。
- [ ] 连接池四参数已设；启动 `PingContext` fail-fast；rows 全部 defer Close。
- [ ] 迁移文件含 `-- +goose Up/Down` 且已 embed；启动 `goose.Up` 通过；`goose.Down` 回滚演练过。
- [ ] `http.Server` 四个超时已配；`signal.NotifyContext` + `Shutdown` 优雅停机已实现。
- [ ] 每端点 ≥3 个 httptest 表驱动用例（正常/校验失败/404，认证路由加 401）。
- [ ] `go vet ./...` 与 `go test ./... -race` 全绿。

## 长尾不做

gRPC / 微服务 / 服务发现（单体 API 足够）、CQRS / 事件溯源 / 消息队列（Kafka/RabbitMQ）、Clean Architecture 四层骨架、GraphQL、WebSocket 集群化、JWT 自签发体系（默认 scs session cookie，见 security-baseline）、GORM AutoMigrate 当生产方案、viper 配置全家桶、wire/fx DI 框架（手写装配即可）、Kubernetes 清单（先 Dockerfile + compose，见 deploy-ship）——均由用户明确需求触发，不主动引入（R5-9）。
