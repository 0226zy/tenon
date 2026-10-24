---
name: security-baseline
description: Web 应用安全默认基线：9 条红线作为交付门禁，含认证方案选择（scs session cookie + x/crypto/bcrypt）、密码哈希、密钥管理、CORS 白名单、安全响应头、服务端授权与 IDOR 防御、认证端点限流。涉及认证、登录、注册、密码、密钥、API key、CORS、用户数据、权限校验时必用。
---

# security-baseline — 安全默认做对

## 何时使用

- 实现注册/登录/登出/会话管理时。
- 处理任何密码、密钥、API key、`.env` 配置时。
- 配置 CORS、安全响应头、cookie 时。
- 写任何受保护端点（需要登录或归属校验）时。
- 交付前必须运行 `scripts/check_secrets.py` 做密钥与硬编码扫描。

## 交付门禁：9 条红线（置顶，违反即未完成）

| # | 红线 | 检查方法 |
|---|---|---|
| 1 | 密码只以自适应哈希存储（Argon2id 首选、bcrypt≥10 合格），禁止明文/可逆加密/MD5/SHA 系列/自造哈希（R3-二） | 查代码：哈希走 x/crypto/bcrypt（GenerateFromPassword）或 argon2id 库；数据库密码列长度与格式符合哈希输出（bcrypt 以 `$2` 开头），绝非 32 位十六进制串 |
| 2 | 密钥不出现在前端代码、Git 仓库、Docker 镜像层；只经环境变量运行时注入服务端；泄露即吊销轮换而非仅删代码（R3-三） | 运行 `check_secrets.py`；确认 `.env` 在 `.gitignore` 与 `.dockerignore`；grep 前端产物无 sk-/service_role/AKIA |
| 3 | 所有数据库访问走参数化查询/ORM，禁止字符串拼接 SQL（含 ORDER BY 列名，须白名单映射）（R3-四.1） | 运行 `check_backend.py`（BE01）；人工查 `fmt.Sprintf` 拼接 / `+` 字符串拼接 SQL |
| 4 | 渲染到 HTML 的用户输入默认转义；`dangerouslySetInnerHTML` 等绕过转义的 API 必须配 DOMPurify 净化（R3-四.2） | 运行 `check_frontend.py`（frontend-react/scripts）；grep 危险 API |
| 5 | 授权只在服务端做：每个受保护端点按「认证→授权→输入校验→执行」顺序；对象级归属校验（owner==当前用户），默认拒绝（R3-五） | 抽查任一端点：改 URL 中 id 为他人资源应返回 403/404；新端点默认要求认证 |
| 6 | Cookie 三属性齐全：Secure + HttpOnly + SameSite=Lax；登录/续期后轮换 Session ID；登出使服务端会话失效（R3-一） | 浏览器 DevTools 查 Set-Cookie 头三属性；登录前后对比 session id 变化；登出后旧 cookie 访问应 401 |
| 7 | CORS 白名单具体 Origin；携带凭据时禁止 `*`；禁止反射请求 Origin（R3-四.3） | `curl -H "Origin: https://evil.example" -H "Cookie: ..." -i <api>` 响应不应带 ACAO 通配或反射 |
| 8 | 认证端点（登录/注册/密码重置/验证码）与付费第三方代理端点必须限流（R3-四.5） | 连续快发 20+ 次登录请求应收到 429 |
| 9 | 安全响应头最小集：nosniff、X-Frame-Options/CSP frame-ancestors、HTTPS 站 HSTS（R3-四.4） | `curl -I <站点>` 查响应头 |

> 另两条防线：登录失败响应统一「用户名或密码错误」，不泄露账号是否存在（R3-二.4）；500 响应只回通用文案，不泄露堆栈/路径/版本（R5-5）。

## 规范做法

### 1. 认证方案选择树（R3-一）

```
浏览器 + 单后端（我们的默认场景）
  → 服务端 Session + HttpOnly Cookie（默认答案）
浏览器 + 移动端/多端 + 强无状态扩展需求
  → 才考虑 JWT：访问令牌分钟级短寿命 + refresh token，payload 不放敏感数据
```

**我们的栈默认 session cookie（alexedwards/scs/v2）+ bcrypt（x/crypto），理由**（R3-一.4）：JWT 签名可验证≠内容保密（payload 仅 Base64），且签发后无法单点吊销，必须配 refresh 机制；对「一个前端 + 一个后端」的小应用，session cookie 简单、可吊销、无客户端存储难题，是社区共识默认解。token 放 localStorage 可被一段 XSS 整体窃取，禁止（OWASP/NIST）。🚫 gorilla/sessions 已归档，新项目用 scs/v2（R5-6）。

配套义务：用 Cookie 就必须处理 CSRF——SameSite=Lax 已覆盖大部分场景，跨域写操作另要求自定义头/Origin 校验；登录成功与续期后轮换 Session ID（scs 的 `RenewToken`）；登出使服务端会话失效（scs 的 `Destroy`），不能只清前端状态（R3-一.2/3/5）。

### 2. 密码红线（R3-二）

1. 哈希用自适应算法：Argon2id 首选（OWASP 参数 m=19MiB, t=2, p=1），bcrypt work factor≥10 合格。禁止明文、可逆加密、MD5/SHA-1/SHA-256、自造算法。
2. 盐由库自动生成（每密码随机盐嵌入输出），不得自己拼盐或复用固定盐。Go 栈默认 `golang.org/x/crypto/bcrypt`（`GenerateFromPassword(pw, bcrypt.DefaultCost)`，DefaultCost=10 满足红线）；更高要求用 `x/crypto/argon2` 的 argon2id（参数调优复杂，属长尾）（R5-6）。
3. 校验必须用库的 verify 函数（恒定时间比较），禁止 `hash(input) == stored` 字符串比较。
4. 密码策略最小集：最小长度 8（允许长口令优于强制复杂度）；对照常见泄露密码黑名单；登录失败统一文案；登录端点限流/渐进延迟。

### 3. 密钥红线（R3-三）

1. 密钥/数据库口令/第三方 API key 只通过环境变量在运行时注入**服务端**进程，绝不出现在前端代码、Git 仓库、镜像层、客户端打包产物。
2. **三件套必须配齐**：`.env` 进 `.gitignore`（防入库）+ 进 `.dockerignore`（防烘进镜像层）+ 仓库放 `.env.example`（键名 + 占位值）。
3. 区分公开键与秘密键：anon/publishable key 设计上可进前端；service_role、Stripe secret、OpenAI key 永不出服务端——AI 生成代码最高频事故就是把 secret 写进 `VITE_`/`NEXT_PUBLIC_` 变量打进 bundle（Escape.tech 实测 5600 个 AI 应用中 400+ 个可从客户端 JS 提取密钥）。
4. 密钥一旦进过仓库即视为已泄露：**立即吊销并轮换**，仅删代码无效（Git 历史可读）。

### 4. CORS 白名单（R3-四.3）

显式枚举前端 origin（含 localhost 开发端口），origin 列表走环境变量；`allow_credentials=True` 时禁止 `*`（浏览器规范禁止，且是安全红线）；禁止反射请求 Origin；仅纯公开只读 API 可 `*`。

### 5. 安全响应头最小集（R3-四.4）

低成本高收益，默认带上：

- `Content-Security-Policy`：至少 `default-src 'self'` 起步
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`（或 CSP `frame-ancestors 'none'`）
- `Strict-Transport-Security`（HTTPS 站点）
- `Referrer-Policy: strict-origin-when-cross-origin`

### 6. 授权与 IDOR 防御（R3-五）

1. 授权判定只在服务端做——前端隐藏按钮/路由守卫只是 UX。每个受保护端点按序执行：**认证 → 授权 → 输入校验 → 执行**。授权回答两问：你是谁 + 这个资源/操作是否属于你。
2. IDOR/BOLA 防御：从 URL/请求体取到的对象 id，服务端必须校验该对象 `owner_id == 当前用户`（或具备相应角色）。BOLA 是 OWASP API Top 10 第一名。禁止「用户不知道 id 就安全」的隐匿式安全。
3. 默认拒绝：新端点默认要求认证，公开端点显式白名单放行，而非默认公开逐个补认证。

### 7. 认证端点限流（R3-四.5）

登录/注册/密码重置/验证码端点与付费第三方 API 代理端点必须限流（红线）；配渐进延迟或锁定。普通业务端点 MVP 阶段可依赖平台默认。健康检查端点不计入限流、不要求认证（见 backend-patterns）。

## 代码骨架

### bcrypt 哈希与校验（internal/users/password.go）

```go
package users

import "golang.org/x/crypto/bcrypt"

// 库自动加盐（每密码随机盐嵌入输出）；禁止自造哈希与手工拼盐
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost) // =10，满足 ≥10 红线
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CompareHashAndPassword 内部恒定时间比较；禁止 hash(input) == stored 字符串对比
func VerifyPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
```

### scs/v2 session 配置 + 登录/登出（internal/httpapi/auth.go）

```go
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-playground/validator/v10"
)

// Cookie 三属性齐全是红线 6；装配侧用 sm.LoadAndSave(mux) 包住整个路由
func NewSessionManager() *scs.SessionManager {
	sm := scs.New()
	sm.Lifetime = 7 * 24 * time.Hour
	sm.Cookie.Name = "session_id"
	sm.Cookie.HttpOnly = true                 // JS 不可读，防 XSS 窃取
	sm.Cookie.Secure = true                   // 仅 HTTPS（本地 http 开发可临时 false）
	sm.Cookie.SameSite = http.SameSiteLaxMode // 默认防 CSRF
	sm.Cookie.Path = "/"
	return sm
}

type AuthHandler struct {
	users    UserService // GetByEmail(ctx, email) (User, error)
	sessions *scs.SessionManager
	validate *validator.Validate // 装配时注入 validator.New()，与 backend-patterns 一致
}

var errInvalidCredentials = &APIError{HTTPStatus: http.StatusUnauthorized,
	Code: "INVALID_CREDENTIALS", Message: "用户名或密码错误"} // 统一文案，不泄露账号是否存在（R3-二.4）

func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email" validate:"required,email"`
		Password string `json:"password" validate:"required,min=8"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		respondError(w, r, err)
		return
	}
	if err := h.validate.Struct(in); err != nil { // 输入校验失败 → 400，不触达 DB
		respondError(w, r, &APIError{HTTPStatus: http.StatusBadRequest, Code: "VALIDATION_FAILED", Message: "参数校验失败"})
		return
	}
	user, err := h.users.GetByEmail(r.Context(), in.Email)
	if err != nil || !VerifyPassword(in.Password, user.PasswordHash) {
		respondError(w, r, errInvalidCredentials) // 查不到用户与密码错误同一响应
		return
	}
	if err := h.sessions.RenewToken(r.Context()); err != nil { // 登录成功轮换 session ID，防会话固定（红线 6）
		respondError(w, r, err)
		return
	}
	h.sessions.Put(r.Context(), "userID", user.ID)
	respondJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	// Destroy 使服务端会话失效（并换发新空会话），不只清前端状态（红线 6）
	if err := h.sessions.Destroy(r.Context()); err != nil {
		respondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RequireAuth：默认拒绝——受保护路由组全部包此中间件，公开端点显式放行
func (h *AuthHandler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := h.sessions.GetInt64(r.Context(), "userID")
		if uid == 0 {
			respondError(w, r, errUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, uid)))
	})
}
```

### CORS 白名单中间件（internal/httpapi/cors.go）

```go
package httpapi

import "net/http"

// 显式枚举 origin（环境变量注入，含 localhost 开发端口）；
// 只回表白名单内的 origin——禁止 "*" 配凭据、禁止反射任意 Origin（红线 7）
func CORS(allowed []string) func(http.Handler) http.Handler {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && set[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

### 带 owner 校验的端点（防 IDOR）

```go
func (h *ItemsHandler) get(w http.ResponseWriter, r *http.Request) {
	// 1. 认证：未登录 401（默认拒绝）
	uid, err := currentUserID(r)
	if err != nil { respondError(w, r, err); return }
	// 3. 输入校验：PathValue 取 id 并显式转换
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		respondError(w, r, &APIError{HTTPStatus: http.StatusBadRequest, Code: "INVALID_ID", Message: "id 必须是整数"})
		return
	}
	item, err := h.svc.Get(r.Context(), id)
	if err != nil { respondError(w, r, err); return } // 不存在 → 404
	// 2. 授权：对象级归属校验，防 IDOR/BOLA（红线 5）
	if item.OwnerID != uid {
		respondError(w, r, &APIError{HTTPStatus: http.StatusForbidden, Code: "FORBIDDEN", Message: "无权访问该资源"})
		return
	}
	respondJSON(w, http.StatusOK, item) // 4. 执行
}
```

## 反模式与红线

- 🚫 明文/MD5/SHA-256/自造算法存密码；`hash(input) == stored` 比较（红线 1）。
- 🚫 密钥进前端常量、`VITE_`/`NEXT_PUBLIC_` 变量、Git 仓库、镜像层；泄露后只删代码不轮换（红线 2）。
- 🚫 字符串拼接 SQL；动态列名/表名不走白名单映射（红线 3）。
- 🚫 `dangerouslySetInnerHTML`/`v-html`/`|safe` 直接渲染用户输入不配净化（红线 4）。
- 🚫 只做登录校验不做对象归属校验；前端隐藏按钮当作权限控制；默认公开逐个补认证（红线 5）。
- 🚫 Cookie 缺 Secure/HttpOnly/SameSite 任一属性；token 存 localStorage；登出只清前端状态（红线 6）。
- 🚫 CORS `*` 配 credentials、反射请求 Origin（红线 7）。
- 🚫 登录/注册/重置端点无限流（红线 8）。
- 登录失败区分「用户不存在」与「密码错误」两种文案（泄露账号存在性）。
- gorilla/sessions（已归档，用 scs/v2）；默认上 JWT 自签发体系（撤销/续期复杂度对单体是负担，R5-6）。

## 交付前检查清单

- [ ] `python3 scripts/check_secrets.py <项目根>` 通过（exit 0）。
- [ ] 对照顶部 9 条红线的「检查方法」逐条过一遍。
- [ ] 密码哈希走 x/crypto/bcrypt（或 argon2id），校验用 CompareHashAndPassword 库函数。
- [ ] `.env` 在 `.gitignore` 与 `.dockerignore`；仓库只有 `.env.example`。
- [ ] Set-Cookie 含 Secure/HttpOnly/SameSite；登录后 RenewToken 轮换 session id；登出 Destroy 使服务端会话失效。
- [ ] CORS 为显式白名单且 origin 走环境变量。
- [ ] 任一受保护端点通过「换他人 id 应 403/404」手动验证。
- [ ] 登录端点连发请求出现 429。
- [ ] 响应头含 nosniff 与 X-Frame-Options/frame-ancestors。

## 长尾不做

SSO/OAuth 第三方登录、MFA、审计日志与 SIEM、WAF/RASP、Vault/KMS 密钥管理服务（环境变量够用）、渗透测试与红队、合规认证（SOC2/等保）、细粒度 ABAC 权限模型、完整 CSP 调优——超出 MVP 门槛，验证后按需追加（R3-七）。
