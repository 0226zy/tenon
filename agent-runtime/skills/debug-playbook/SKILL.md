---
name: debug-playbook
description: 遇到报错或「预览能跑上线就挂」时的排障入口。按症状索引速查：React 前端（无限重渲染、CORS、env undefined、刷新 404）、Vue 特有坑（解构丢响应性、prop 突变）、Go 后端 Top12（连接池、rows 泄漏、错误被吞、map 并发写）、部署与 AI 应用四大失败模式。关键词：报错、404、CORS、排障、debug。
---

# Debug Playbook —— 症状索引排障速查

> 用法：从用户描述或报错信息中提取**症状关键词**，在下表定位行，按「根因 → 解法」执行。每条括注来源，详细规范见对应专项 skill。
> 原则：先按症状查表，不要上来就改代码；修法必须对准根因，不做「看起来能跑」的表面修补。

## 何时使用

1. 用户报告任何报错信息、白屏、功能不生效、数据异常时；
2. 「本地/预览正常，部署后挂了」类问题时；
3. 接口慢、请求挂起、间歇性报错等性能与稳定性问题时；
4. 上线前自查（本文末「交付前检查清单」即排障门禁）。

## 规范做法（排障三步）

1. **取症状关键词**：报错原文（如 `connection refused`、`Maximum update depth exceeded`）优先于用户转述；没有报错时按可观察行为（「刷新 404」「删除后数据串行」）查表。
2. **对表定位根因**：同名症状可能有多个候选根因（如「连不上数据库」），按表中根因逐一对照当前环境排除。
3. **按标准解法修复并验证**：修完跑对应验证动作（重启/重构建/复现路径重走一遍），确认症状消失而非被掩盖（如错误被 `_ = rows.Close()` / 未检查的 err 吞掉不算修好，R5-坑5）。

## 速查表 A：前端（10 条，来源 R1）

| 症状关键词 | 根因 | 标准解法 |
|---|---|---|
| effect 套 effect / 派生 state 不同步 / 无限循环 | 把 useEffect 当「响应 state 变化」的通用钩子 | 派生数据渲染期直接算；用户操作放事件处理器；prop 变化重置用 `key={id}`（R1-坑1） |
| 删除/重排后勾选框、输入内容「串行」/ unique key 警告 | `key={index}`，下标偏移导致 state 挂错行 | 换数据稳定唯一 id；🚫 `key={Math.random()}`（R1-坑2） |
| setInterval 里 count 永远是初始值 / 读到旧 props | 依赖 `[]` 使闭包停留在首渲染快照 | 函数式 `setCount(c => c+1)`；读最新值用 ref；开 eslint react-hooks（R1-坑3） |
| **CORS** / "blocked by CORS policy"（curl 正常、浏览器报错） | 问题在服务端响应头；5173↔8000 即跨源 | dev 用 Vite `server.proxy` 代理 `/api`；生产同源或枚举白名单；🚫 生产开 `*`（R1-坑4） |
| `import.meta.env.VITE_X` 是 **undefined** / 请求打到 `undefined/xxx` / dev 正常部署后失效 | 缺 `VITE_` 前缀 / 写了 `process.env` / 改 .env 没重启 / 构建后才设变量 | `VITE_` 前缀 + 字面量点访问；改后重启/重构建；密钥绝不入前端（R1-坑5） |
| **刷新 404** / 分享深层链接 404（首页点导航正常） | SPA 路由在客户端，服务器无此文件 | 平台配 SPA fallback（`_redirects` / vercel.json rewrite / nginx `try_files`）+ `path="*"` 404 页（R1-坑6） |
| token 被 XSS 偷走 / 与第三方 Bearer API 不兼容 | token 放 localStorage，页面任何 JS 可读 | cookie session（HttpOnly+Secure+SameSite）；🚫 有权限 token 不入 localStorage（R1-坑7） |
| 后端 500 却走 success 分支 / `data.xxx` undefined（**fetch 假成功**） | fetch 只在网络失败时 reject，4xx/5xx 照样 resolve | apiClient 统一 `if (!res.ok) throw` + isError 错误态（R1-坑8） |
| **Maximum update depth exceeded** / 页面卡死 | 渲染函数体内调 setState；effect 更新自己依赖的 state 成环 | setState 只在事件处理器或 effect；effect 依赖数组收敛（R1-坑9） |
| 输入框每敲一字整页闪 / Context 变化全场重渲染 | 高频 state 放顶层；大 Context value 每次新建引用 | 状态下沉最近公共祖先；Context 按关注点拆分；先测量再优化（R1-坑10） |

## 速查表 A2：Vue 特有坑（6 条，来源 R6-7；Vue 路线时使用）

| 症状关键词 | 根因 | 标准解法 |
|---|---|---|
| 父组件改了值子组件不更新 / store 变了页面不变（**解构丢响应性**） | `const { x } = props/reactive()/store` 把 proxy 属性快照成普通值 | `toRefs(state)` / `toRef(() => props.foo)` / `storeToRefs(store)`（action 可直接解构，state/getter 不行）（R6-坑1） |
| reactive 对象整体重新赋值后视图不更新 | reactive 不能整体替换、不能解构、仅限对象 | **默认一律 `ref()`**，整体替换走 `.value =`；reactive 仅多字段表单且用 `Object.assign`（R6-坑2） |
| 控制台 prop 突变警告 + 父子数据不同步 | props 只读（单向数据流），子组件直接改了 prop | 双向绑定用 `defineModel()`（3.4+）或 emit 事件让父组件改（R6-坑3） |
| 列表重排/删除后输入框内容错位、动画异常 | `v-for` 的 `:key` 用了 index 或不写 | `:key` 用稳定唯一业务 id，禁止 index（静态列表除外）（R6-坑4） |
| watch 回调地狱 / 慢的旧请求覆盖新数据（竞态） | 能用 computed 表达的派生状态用了 watch；数据获取手写 watch | 派生值用 computed；副作用才用 watch 并返回 cleanup；数据获取交给 vue-query 的 reactive queryKey（R6-坑5） |
| 重复请求 / 不可预测的重复更新 | computed 里写了副作用（请求、改状态） | computed 必须是纯函数，副作用移到 watch/事件（R6-坑6） |

## 速查表 B：Go 后端（12 条，来源 R5-7）

| 症状关键词 | 根因 | 标准解法 |
|---|---|---|
| `too many connections` / 高峰期请求挂起（**连接池打满**） | 池默认 MaxOpenConns 无上限、MaxIdleConns=2 | `SetMaxOpenConns(25)`/`SetMaxIdleConns(10)`/`SetConnMaxLifetime(5min)`/`SetConnMaxIdleTime(1min)`（R5-坑1） |
| 启动正常但**首个请求才报连库失败** | `sql.Open` 不建立真实连接，只初始化池 | 启动即 `db.PingContext(ctx)`，失败 os.Exit(1)（fail-fast）（R5-坑2） |
| 连接数只涨不跌直至耗尽（**rows 泄漏**） | 忘关 rows / 迭代后没查 rows.Err() | `QueryContext` 后立即 `defer rows.Close()`；迭代完查 `rows.Err()`（R5-坑3） |
| 启动 panic: `pattern ... conflicts` / 路由被遮蔽 | 同优先级 ServeMux 模式冲突；`/api` 与 `/api/` 是两个模式 | 启动 panic 是优点尽早暴露；精确匹配用 `{$}` 后缀（R5-坑4） |
| 接口返回 200 但数据没写入、日志干净（**静默失败**） | `_ = rows.Close()`、err 赋了没检查 | 每个 `err != nil` 必处理；统一走 respondError 返回 4xx/5xx；CI 挂 errcheck（R5-坑5） |
| 响应里 0/false/"" 字段消失 / 未知字段被静默吞掉 | `omitempty` 与解码器默认行为 | 可空字段用指针 `*int`；解码开 `DisallowUnknownFields()` + `MaxBytesReader` 限大小（R5-坑6） |
| 异步任务数据写一半 / 请求结束后 DB 操作报 context canceled | 后台 goroutine 沿用已取消的请求 ctx | ctx 只显式传参；异步用 `context.WithoutCancel(ctx)` 派生；DB 调用带超时（R5-坑7） |
| 内存持续增长 / 服务越来越慢（**goroutine 泄漏**） | 无缓冲 channel 发送无人接收；循环里 `time.After` | 每个 goroutine 留退出路径；循环用 `time.NewTicker` + `defer ticker.Stop()`（R5-坑8） |
| 时间差 8 小时 / 时间比较结果错误 | `time.Now()` 本地时区入库；`==` 比较 time.Time | 一律 UTC 存传（RFC3339），展示层才转时区；比较用 `.Equal()`（R5-坑9） |
| 部署时进行中的请求被砍断 | 没有优雅停机 | `signal.NotifyContext(SIGINT, SIGTERM)` + `srv.Shutdown(ctx)` 带超时（R5-坑10） |
| 慢客户端拖死服务（Slowloris）/ 连接挂满 | 裸 `http.ListenAndServe` 无超时 | 构造 `http.Server{Read/ReadHeader/Write/IdleTimeout}`；check_backend.py BE02 拦截（R5-坑11） |
| `fatal error: concurrent map writes` | handler 每请求一个 goroutine，共享 map 无锁 | 共享 map 加 `sync.RWMutex` 或改 `sync.Map`；`go test -race` 兜底（R5-坑12） |

## 速查表 C：部署（8 条，来源 R4-A5）

| 症状关键词 | 根因 | 标准解法 |
|---|---|---|
| **connection refused**（容器内 app 连不上数据库） | 容器里 `localhost` 指容器自身；宿主机端口映射对容器间通信无意义 | Compose 网络内用服务名+容器端口（`postgres://db:5432`）；访宿主机用 `host.docker.internal`（R4-坑1） |
| 端口映射后服务直接暴露公网 / UFW 规则无效 | Docker 直改 iptables 且默认绑 0.0.0.0 | 只映射 `127.0.0.1:3000:3000`；内部服务不配 ports，走内部网络+反代（R4-坑2） |
| build 首行 `transferring context: 500MB+` / CI 构建慢 | 缺 `.dockerignore`，node_modules/.git/dist 全打包 | 配 .dockerignore（含 `.env`/`*.key`）；目标降到 10MB 级（R4-坑3） |
| 「上周还能构建今天挂了」/ 生产与本地行为不一致 | `latest` 漂移、`npm install` 不读 lockfile、apt 未 pin | 镜像与包全部 pin 版本；`npm ci`；主动 `--pull --no-cache` 升级（R4-坑4） |
| 启动后报**表不存在** / 启动即 crash | 裸 `depends_on` 不等就绪；迁移混在应用启动里竞态 | db 健康检查（pg_isready）+ 独立 migrate 服务 + `service_completed_successfully` 门控；迁移幂等（R4-坑5） |
| 服务永远 starting→**unhealthy** | slim/distroless 镜像没有 curl/wget，健康检查命令不存在 | 用镜像自带工具（pg_isready、redis-cli）或构建时装入；给足 `start_period`（R4-坑6） |
| `.env` 出现在镜像层 / 仓库历史里 | `.env` 未进 .dockerignore/.gitignore | 运行时 `env_file` 注入；泄露即轮换 + 清 git 历史（R4-坑7） |
| **改代码不生效** / 部署后仍是旧行为 | 层缓存复用；latest 覆盖镜像 tag；浏览器缓存旧 bundle | 每版唯一 tag；必要时 `--no-cache`；发布后硬刷新验证（R4-坑8） |

## 速查表 D：AI 生成应用四大失败模式（来源 R4-B3）

| 症状关键词 | 根因 | 标准解法/防线 |
|---|---|---|
| **预览能跑上线就挂** | ~55% 是环境变量缺失/错配（预览自动注入，发布后需构建期重新注入）；认证回调仍指向 localhost | 发布前变量清单逐一核对；改 env 必重新 build；上线后按「注册→登录→核心操作」冒烟（R4-B3-1） |
| **登录回调死循环** / 魔法链接指向 localhost | OAuth 回调未注册生产域名；认证配置按预览域名生成 | 认证服务 Site URL 与全部 redirect 路径配上生产域名（R4-B3-1） |
| **密钥泄漏**（前端 bundle 可提取 key） | service key / Stripe secret 写进 `VITE_` 变量或硬编码进代码 | 秘密 key 只在服务端；CI 密钥扫描；泄露即轮换并清历史（R4-B3-2） |
| 任何人都能读到别人数据（**数据库裸奔**） | 数据库直连架构下未启用行级安全或策略形同虚设 | 每张表启用行级安全 + owner-scoped 最小权限策略；上线前做双用户越权测试（R4-B3-3） |
| 发布后行为怪异 / 测试数据污染生产（**环境混淆**） | AI 把预览域名、localhost、测试项目硬编码进代码；开发/生产共用后端 | 全 URL 走环境变量；开发/生产环境分离；上线前全局 grep localhost 与预览域名（R4-B3-4） |

## 速查表 E：安全类高频坑（8 条，来源 R3-六）

| 症状关键词 | 根因 | 标准解法 |
|---|---|---|
| 密钥出现在浏览器 JS bundle / git 历史 | AI「能跑就行」硬编码或写进前端常量 | 密钥只注入服务端环境变量；构建前 grep 前端产物；泄露即吊销轮换（R3-坑1） |
| 数据库可见明文或 32 位十六进制密码（MD5） | 自造哈希、不懂自适应哈希 | Argon2id（首选）/bcrypt ≥10，库自动加盐；verify 恒定时间比较（R3-坑2） |
| 匿名身份可拖全表 | 行级安全未开启或策略写成恒真 | 每表启用行级安全 + owner 策略；实测「换 ID 能否读别人数据」（R3-坑3） |
| 改 URL 里 `/users/123` 即可看/改他人数据（**IDOR 越权**） | 只校验登录，没校验对象归属 | 服务端每个对象操作校验 owner/role；默认拒绝（R3-坑4） |
| 一次 XSS 即全账号接管 | token 存 localStorage | HttpOnly+Secure+SameSite cookie session（R3-坑5） |
| 搜索/筛选接口被注入拖库 | 字符串拼接 SQL 绕过 ORM | 参数化查询；动态列名走白名单映射（R3-坑6） |
| 任意网站可带用户凭据调 API | CORS 通配 `*` 或反射请求 Origin | 显式白名单；带凭据禁 `*`（R3-坑7） |
| 登录接口可被在线爆破 | 认证端点无限流 | 登录/注册/重置端点必加限流 + 渐进延迟/锁定（R3-坑8） |

## 排障操作速查

```bash
# 容器排障
docker compose ps                      # 看健康检查状态与迁移服务退出码
docker compose logs api --tail=100     # 取报错原文
docker compose up -d --wait            # 冷启动全流程验证

# HTTP 排障（绕过浏览器排除 CORS 干扰）
curl -i https://api.example.com/health
curl -i -X OPTIONS https://api.example.com/api/v1/items -H "Origin: https://example.com"

# 环境混淆自查（上线前必跑）
grep -rn "localhost\|127\.0\.0\.1" web/src api/cmd api/internal

# 前端/后端红线扫描
python3 frontend-react/scripts/check_frontend.py web
python3 backend-patterns/scripts/check_backend.py api
```

## 反模式与红线

- 🚫 不看报错原文，凭猜测改代码（先取症状关键词再查表）。
- 🚫 用吞 err（`_ = x.Close()` / 赋了不检查）、放宽权限策略、`CORS *` 等方式「消除报错」——症状消失 ≠ 修好（R5-坑5、R4-B3-3）。
- 🚫 为解决 CORS 给生产后端开 `Access-Control-Allow-Origin: *`（R1-坑4）。
- 🚫 「能跑」就当「能上线」，跳过冒烟与安全自查（R4-B3 总防线）。
- 🚫 修复后不回归验证原路径。

## 交付前检查清单（排障完毕判定）

- [ ] 症状可稳定复现的路径在修复后重走一遍，确认消失
- [ ] 修复对准根因，未引入吞异常/放宽权限/通配 CORS 等表面修补
- [ ] 涉及环境变量的改动已重启 dev server 或重新 build
- [ ] 部署类问题已做冷启动验证（空卷 `docker compose up -d --wait`）
- [ ] 上线前跑完：构建通过、迁移 exit 0、健康检查转绿、核心旅程冒烟、密钥扫描（R4-A4/B3）
- [ ] 相关红线脚本复跑通过（check_frontend.py / check_backend.py / check_secrets.py）

## 长尾不做（本 skill 域内）

APM/分布式追踪、Prometheus 指标全家桶、混沌工程、性能压测调优——属于规模化后的长尾；MVP 阶段「每请求一条访问日志（方法/路径/状态码/耗时/request_id）+ 错误日志」已覆盖 90% 排障场景（R5-4）。
