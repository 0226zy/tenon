---
name: arch-design
description: 跟进用户需求构建前后端/Web 应用时，动手写代码前必用。引导完成架构设计与代码设计：需求收敛 → 技术栈决策（锁定表+取舍矩阵）→ 目录宪法 → 代码骨架纪律 → 验证门禁，并按阶段接力到 product-scope、fullstack-architecture、backend-patterns、frontend-react、frontend-vue、security-baseline、deploy-ship、debug-playbook 八个专项技能。关键词：做一个应用、设计架构、初始化项目、新建模块、技术选型、代码结构、设计评审。
---

# Arch Design —— 架构设计与代码设计引导（全栈交付的总设计流程）

> 场景：用户要构建（或大改）一个前后端应用。你是唯一懂技术的角色——用户没有
> 能力替你擦屁股，所以**设计阶段的克制与门禁，比编码速度值钱得多**。
> 本 skill 是设计阶段的入口：它不替代专项 skill，而是告诉你何时用哪个。
>
> 元原则（全部沉淀自工程文献与 AI 生成应用的事故复盘）：
>
> 1. **结构先于代码**：agent 生成代码量大、用户无技术能力维护——清晰边界比少文件更重要。
> 2. **简单优先、单体优先、无聊技术优先**：任何增加第二个有状态组件（队列、缓存、对象存储）
>    的决定都必须由用户的明确需求触发，而不是你主动引入。
> 3. **主干 vs 长尾**：主干保证「能跑、能维护、不踩高频坑」；长尾每一项都是复杂度税。
>    「听起来有用」「将来一定需要」不构成进 v1 的理由。
> 4. **能下沉为确定性产物的，绝不留给推理**：验证脚本判定「做对没有」，skill 提供
>    「怎么做」，你的推理只留给用户的具体业务逻辑。
> 5. **「能跑」≠「能上线」**：交付以验证门禁为准，不以代码写完为准。

## 设计流程总览（五阶段，顺序执行）

| 阶段 | 做什么 | 产出物 | 接力 skill |
|---|---|---|---|
| S0 需求收敛 | 锁定「一个目标用户 + 一个核心工作流 + 一个待验证假设」，砍掉蔓延项 | MVP 范围一句话 + 拒绝清单 | `product-scope` |
| S1 架构决策 | 按栈锁定表选默认答案；每个偏离默认的决定写下理由 | 技术栈清单 + 取舍记录 | `fullstack-architecture` |
| S2 目录宪法 | 定目录结构与前后端边界（先建骨架再填肉） | 目录树 + 边界规则 | `fullstack-architecture` |
| S3 代码设计 | 后端骨架纪律 + 前端骨架纪律（见下文两张清单） | 各层骨架代码 + 统一错误契约 | `backend-patterns` / `frontend-react` / `frontend-vue` |
| S4 验证门禁 | 测试 + 安全红线 + 上线四检 | 全部门禁通过的证据 | `security-baseline` / `deploy-ship` / `debug-playbook` |

每个阶段动手前，先 `use_skill` 加载对应专项 skill 的正文，按其中的模式与清单执行；
本文件只负责流程与决策框架。

## S0 需求收敛（先收敛，再谈技术）

非技术用户的需求是模糊的：「做一个 XX 平台」。第一动作永远不是选型，而是反推 MVP：

- 问清三件事：谁是第一个用户？他现在的痛点工作流是什么？v1 要验证的假设是什么？
- 反推主干五件套：landing 页、注册登录（优先托管认证）、**一个**核心 CRUD 闭环、
  响应式 UI、部署上线。
- 默认拒绝的蔓延项（进 backlog 不进 v1）：支付多档定价、实时协作、通知中心、
  admin 后台、i18n、PWA、复杂动画。
- 详细话术与提问清单：加载 `product-scope`。

## S1 架构决策（栈锁定表 + 取舍矩阵）

**默认答案（v2 锁定，无特殊理由不偏离）：**

| 层 | 默认 | 升级边界（撞到才换） |
|---|---|---|
| 前端路线 | React（Vite + TS + Router + TanStack Query + RHF/zod） | 用户点名 Vue → 切 Vue 路线（一项目只用一条） |
| Vue 路线 | Vue 3 `<script setup lang="ts">` + Router + Pinia + vue-query + Element Plus + vee-validate/zod | UI 库备选 Naive UI |
| Go 路由 | 标准库 net/http ServeMux（Go 1.22+） | 路由组/嵌套中间件 → chi；**禁 gorilla**（已归档） |
| Go 布局 | `cmd/server` + `internal/` **按域分包** | 大型长期项目再谈 Clean Arch；禁 project-layout 全家桶 |
| 数据访问 | sqlc + pgx（PostgreSQL）；超轻量场景 SQLite | 高频原型 CRUD → GORM（AutoMigrate 仅限开发） |
| 迁移 | goose，**embed 进二进制、启动自动执行** | — |
| 配置/日志/校验 | caarlos0/env + log/slog + go-playground/validator | **禁 viper**（大而全，单容器应用是负担） |
| 认证 | scs/v2 session cookie + x/crypto/bcrypt | 不默认 JWT（撤销/续期复杂度对单体是负担） |
| 部署 | Docker 多阶段：Go 静态编译（CGO_ENABLED=0）+ 前端 node→nginx + compose 门控 | 先 Dockerfile + compose；K8s 是长尾 |

**取舍矩阵（每个决策问三遍）：**

1. **没有这个组件，v1 能不能跑？** 能 → 砍掉。（第二个有状态组件=第二份运维事故）
2. **这是社区 2024+ 的默认答案吗？** 不是 → 写下理由（如 gorilla 已归档、Redux 默认过重）。
3. **非技术用户维护得起吗？** 维护不起 → 换托管方案或砍掉（认证优先 BaaS/托管）。

**禁止清单（高频过度设计，除非用户明确要求）**：SSR/SSG、微服务/服务发现、
消息队列、CQRS/事件溯源、GraphQL、Kubernetes、DI 框架（wire/fx）、
Redux 级全局 store、i18n 框架、E2E 全家桶、WebSocket 实时协作。

## S2 目录宪法（先建骨架再填肉）

- 前端按 **feature（业务域）组织**，不按技术类型平铺：`src/features/<域>/{api,components,hooks,types,index.ts}`，
  `index.ts` 是唯一对外出口；React/Vue 同理（Vue 小项目可用 create-vue 默认结构起步）。
- 后端 `cmd/server/main.go` **只做装配**（读配置、连库、装中间件、注册路由、起服务、监听信号）；
  `internal/` 按域分包；领域逻辑不 import net/http 与 database/sql。
- 前后端边界：统一 `apiClient`（组件不直接 fetch）、`/api/v1` 前缀、统一错误契约
  `{error:{code,message,details}}`。
- 模板取「装配模式」而非「全家桶选型」——高星 boilerplate 普遍过度工程，不要照搬。
- 完整目录宪法与骨架：加载 `fullstack-architecture`。

## S3 代码设计纪律（两张清单，写代码前过一遍）

**后端（Go）骨架纪律：**

- handler 薄：只做「解码（MaxBytesReader 限大小）→ 校验 → 调领域函数 → 编码响应」。
- 统一错误契约：集中 `respondError` 用 `errors.Is/As` 映射（ErrNoRows→404、校验→400、
  未知→500），**绝不把内部错误原文透传客户端**；错误包装一律 `fmt.Errorf("...: %w", err)`。
- 中间件纯函数链 `func(http.Handler) http.Handler`：request-id / slog 日志 / recover / CORS 白名单 / 超时。
- `http.Server` 显式构造四超时（**禁裸 `http.ListenAndServe`**——慢客户端可拖死服务）。
- 连接池四参数（MaxOpenConns/MaxIdleConns/MaxLifetime/MaxIdleTime）+ 启动 `PingContext` fail-fast。
- `rows` 立即 `defer Close()`；ctx 显式传参不进 struct；时间一律 UTC（RFC3339）。
- 每个 endpoint 至少 3 个 httptest 表驱动用例：正常、400、404；认证路由加 401 用例。
- 完整模式与 Top12 坑：加载 `backend-patterns`；机器判定跑 `check_backend.py`。

**前端骨架纪律（两条路线通用）：**

- 状态三分层：服务端状态→TanStack Query（**禁 useEffect+fetch 手写数据层**）、
  客户端 UI 状态→useState/ref（跨组件才进 store）、URL 状态→search params（刷新可复现）。
- 每个远端数据区块显式处理**四态**：loading（骨架屏）/ error（带重试）/ empty（引导文案）/ success。
- 表单：≤3 字段受控即可；复杂表单 React 用 RHF+zod、Vue 用 vee-validate+zod——
  schema 一处定义，运行时校验 + TS 类型 + 错误信息三者同源。
- 提交按钮 pending 期必 disabled（双发创建是最常见脏数据来源）。
- 列表 key 用稳定业务 id（**禁 index 当 key**）；路由级懒加载 + `path="*"` 404 页；
  环境变量 `VITE_` 前缀（**密钥绝不进前端 bundle**）；部署配 SPA fallback。
- 路线专项：React 加载 `frontend-react`；Vue 加载 `frontend-vue`。

## S4 验证门禁（不满足不交付）

1. **构建/测试通过**：后端 `go test ./... -race` + `go vet`；前端 `tsc` + `vite build`。
   有测试命令探测需求时用 agent 的 `run_tests` 工具。
2. **安全红线**（违反即拒绝交付，完整 9 条见 `security-baseline`）：
   密码自适应哈希（bcrypt/Argon2id）；密钥不入前端/仓库/镜像（泄露即轮换而非删代码）；
   参数化查询（禁字符串拼 SQL）；输出默认转义（`dangerouslySetInnerHTML`/`v-html` 必须净化）；
   服务端每端点「认证→对象级授权→校验→执行」、默认拒绝；Cookie 三属性 + 登录轮换 Session ID；
   CORS 白名单；认证/付费端点限流；基础安全响应头。
   机器判定：`python3 skills/security-baseline/scripts/check_secrets.py .` 等脚本。
3. **上线四检**（加载 `deploy-ship`）：全量构建通过 → 健康检查转绿 → 迁移 exit 0 →
   冒烟打核心业务旅程（注册→登录→核心 CRUD），而非只看首页能开。
4. **排障**：症状先行查 `debug-playbook`（Go 12 + React 10 + Vue 6 + 部署 8 + 安全 8 高频坑索引），
   不要从零推理已知的坑。

## 设计产出物（写进项目，沉淀给后续会话）

设计决策不要只留在对话里。在目标项目根目录落一份 `AGENTS.md`（或 DESIGN.md），至少含：

```markdown
# 架构决策记录
- 技术栈：<前端路线> + Go（ServeMux + sqlc + goose）+ PostgreSQL，理由：…
- 目录宪法：<目录树要点；跨 feature 只许从 index.ts 出口导入>
- 错误契约：{error:{code,message,details}}；状态码约定 200/201/204/400/401/403/404/409/500
- 验证命令：后端 `go test ./... -race`；前端 `npm run build`；冒烟：…
- 安全红线核对：密码哈希 ✓ / 密钥入库 ✗ / 对象级授权 ✓ / CORS 白名单 ✓
```

后续会话（含本 agent 的其他会话）会先读 AGENTS.md——这是项目记忆的交接点。

## 快速自检（开始写代码前）

- [ ] MVP 范围一句话说得清？蔓延项都进了 backlog？
- [ ] 技术栈每一项都是锁定表默认答案，或写下了偏离理由？
- [ ] 目录骨架已建、边界规则已定（前后端契约、feature 出口）？
- [ ] 两张代码设计清单已在手（对应专项 skill 已加载）？
- [ ] 验证门禁的命令与红线核对方式已明确？
