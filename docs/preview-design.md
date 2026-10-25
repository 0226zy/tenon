# Tenon 预览功能设计与技术方案

> 状态：方案草案 v0.1  
> 日期：2026-10-25  
> 范围：代码/预览工作区中的「预览」能力  
> 说明：本文档为调研、现状实测与设计，不包含代码变更。

---

## 摘要

Tenon 现有的预览链路已经能完整跑通：Run 完成后由控制层触发沙箱构建，`wsd` 执行 `start.sh` 启动生成应用，控制层通过 `<key>.preview.<domain>` 子域网关和一次性票据暴露预览，前端用 iframe 展示。

当前主要短板不在「能不能预览」，而在：

- 状态与日志依赖 5 秒轮询，构建过程不可观测；
- 每个项目只有一个当前预览，没有不可变 generation 产物，无法回滚；
- 预览入口受端口/域名限制，缺少分享、设备尺寸、截图、控制台错误等体验；
- 网关安全头与网络出口策略还不完整。

建议路线：**保留沙箱实时预览，同时引入不可变 generation 产物与预览网关**。先用低成本改造补齐 SSE、设备模式、分享、安全头与失败兜底，再演进到「快照产物优先、沙箱只负责构建」的目标架构。

---

## 目录

1. [背景与目标](#1-背景与目标)
2. [主流产品调研](#2-主流产品调研)
3. [Tenon 现状](#3-tenon-现状)
4. [实测调用结果](#4-实测调用结果)
5. [产品设计方案](#5-产品设计方案)
6. [技术方案](#6-技术方案)
7. [分期落地](#7-分期落地)
8. [风险与待决策](#8-风险与待决策)
9. [附录](#9-附录)

---

## 1. 背景与目标

### 1.1 当前问题

| 问题 | 表现 |
|---|---|
| 构建不可观测 | 前端只看到 `starting`，不知道在装依赖、编译还是启动 |
| 失败定位弱 | `/preview` 响应不包含 `error_code`/`log_tail`，只能手动拉日志 |
| 无版本概念 | 只有当前预览，构建失败会失去可用版本；无法回滚 |
| 分享困难 | 入口依赖登录态与一次性票据，没有可控 TTL 的只读分享 |
| 体验缺口 | 无设备尺寸、全屏、缩放、截图、控制台错误 |
| 公网入口受限 | 预览走 `:8081`，安全组未放行；80 端口对未备案域名有限制 |
| 安全策略不足 | 缺少 `frame-ancestors`、预览网络出口限制、分享撤销 |

### 1.2 设计目标

1. **快**：已有版本秒开；新版本构建期间继续展示旧版本。
2. **可观测**：阶段进度、实时日志、结构化错误码、重试入口。
3. **可回溯**：每轮 Run 对应不可变 generation，可回滚、可分享。
4. **安全**：独立 origin、短期票据、CSP、网络出口与资源配额。
5. **多端适配**：桌面/平板/手机、全屏、独立窗口。

### 1.3 非目标

- 本期不把 Tenon 迁移到浏览器内 WebContainers（生成应用包含 Go + SQLite）。
- 本期不做完整的云端 IDE（终端、协作编辑、调试器）。
- 本期不改变 Run/Commit 的语义，只在预览侧消费 generation。

---

## 2. 主流产品调研

### 2.1 三类实现路线

| 路线 | 代表产品 | 执行位置 | 优势 | 限制 |
|---|---|---|---|---|
| 浏览器运行时 | Bolt.new、StackBlitz | 浏览器标签页内（WebContainers/WASM） | 毫秒启动、离线、无服务器成本 | 仅 Node/Web 栈，无法跑 Go、原生依赖 |
| 云端沙箱 + 端口代理 | v0、Lovable、Replit、CodeSandbox、E2B | 云端 VM/容器/microVM | 技术栈完整、可跑后端 | 冷启动、成本、隔离与回收复杂 |
| 静态产物/沙箱 iframe | Claude Artifacts、纯前端分享 | 浏览器 sandbox iframe 或 CDN | 简单、安全、可缓存 | 无后端、能力受限 |

### 2.2 产品对比

| 产品 | 构建/执行 | 预览入口 | 隔离与鉴权 | 更新方式 | 备注 |
|---|---|---|---|---|---|
| **v0 / Vercel** | 云端 Sandbox，Next.js | API 返回 preview URL + 短期 token；由自有后端代理到独立预览 origin | 短期 token、可信预览域名、host 白名单 | 构建完成切换，支持 loading/refresh | 文档明确：iframe 无法带自定义 header，必须后端代理 |
| **Lovable** | 云端项目环境，React/Vite 全栈 | 编辑器内 iframe，发布后独立托管 | 账号 + 项目权限 | 实时更新 + Publish | 支持 Supabase、GitHub 同步 |
| **Bolt.new** | 浏览器内 WebContainers | 浏览器内 dev server + Service Worker URL | 代码不离开标签页 | 即时 HMR | 不能跑原生二进制/Go |
| **Replit** | 云端容器（Nix） | 端口代理到 Webview / 发布域名 | 账号 + 端口策略 | dev server 实时刷新 | 常驻/自动休眠等部署形态 |
| **CodeSandbox** | VM/microVM 沙箱 | 每个端口一个 preview URL（通配域） | 沙箱级隔离 + 端口转发 | HMR + 快照 | 偏开发环境，成本较高 |
| **Claude Artifacts** | 浏览器 sandbox iframe | 单 HTML/React 直接渲染 | iframe sandbox + CSP，无后端 | 重新生成即刷新 | 只能自包含前端 |
| **E2B** | 云端沙箱 | 每个端口一个公网 URL | 沙箱 token/域名 | 启动即访问 | 面向 Agent 代码执行 |

### 2.3 五条共性规律

1. **执行位置决定能力边界**：浏览器运行时最快但栈受限；云端沙箱能力完整但需要生命周期管理。
2. **预览入口必须解决鉴权**：要么签名 URL/一次性票据换 Cookie，要么后端代理注入 token；不能假设 iframe 能带自定义 Header。
3. **origin 隔离是安全底线**：预览放在独立域名/子域，避免与主站共享 Cookie、LocalStorage、Service Worker。
4. **generation 与 stale-while-revalidate**：新版本构建时继续服务旧版本，构建失败不影响可用预览。
5. **体验标配**：状态阶段、实时日志、设备尺寸、刷新/重启、全屏/新窗口、分享、回滚。

### 2.4 对 Tenon 的启示

- Tenon 生成 Go + SQLite 服务端，**不能走纯浏览器运行时**；云端沙箱是主线。
- 子域 + 一次性票据的方向是对的，应保留并补齐安全头、分享与限流。
- v0 的「后端代理 + 独立预览 origin + loading 路由 + 资源 catch-all」值得借鉴；Tenon 目前用子域根路径，天然避免了绝对路径改写问题。
- 必须引入不可变 generation 产物，才能同时满足「构建失败保留旧版本」「分享」「回滚」「CDN 缓存」。

参考资料：

- [v0 Accessing Previews](https://v0.app/docs/api/v2/guides/accessing-previews)
- [WebContainers Introduction](https://webcontainers.io/guides/introduction)
- [PostHog: Inside the tech that powers Bolt.new](https://posthog.com/newsletter/inside-bolt-dot-new)
- [CodeSandbox Preview URLs](https://codesandbox.stream/docs/learn/environment/preview-urls)
- [Lovable Preview docs](https://docs.lovable.dev/features/projects/preview)
- [Replit Ports](https://docs.replit.com/features/project-setup/ports)
- [E2B Sandbox public URL](https://docs.e2b.dev/network/public-url)
- [Vercel: Introducing the new v0 API](https://vercel.com/blog/introducing-the-new-v0-api)

---

## 3. Tenon 现状

### 3.1 现有链路

```
Run 完成并 commit
   │
Control: preview.Service.Entry / Restart / Logs
   │  generation + commit 对齐（LatestSuccessfulRun）
   ▼
Sandbox wsd: preview.Supervisor
   ├─ 获取 build slot
   ├─ 停止旧 preview 进程
   ├─ 分配端口，执行 start.sh（npm build + go build + 启动服务）
   ├─ 健康检查 GET /
   └─ 状态写入 t_sandbox（preview_status / generation / commit）
   │
Preview Gateway (:8081)
   ├─ Host: <26位key>.preview.<domain>
   ├─ /__enter?t=... → 一次性 ticket → HttpOnly Cookie
   └─ 反代到沙箱实际端口（剥离 Cookie）
   │
前端 PreviewPanel
   ├─ 每 5 秒轮询 /api/conversations/{id}/preview
   ├─ ready 后 iframe 加载签名 URL
   └─ 日志抽屉、重启、新窗口
```

### 3.2 代码位置

| 模块 | 文件 | 职责 |
|---|---|---|
| 控制层预览 | `internal/control/preview/service.go` | Entry/Restart/Logs、generation 对齐、stale |
| 预览网关 | `internal/control/preview/handler.go` | 子域路由、ticket/cookie、反向代理 |
| 票据 | `internal/control/preview/tickets.go` | 一次性 entry ticket、签名 Cookie |
| 沙箱构建 | `internal/wsd/preview/preview.go` | build slot、端口、`start.sh`、健康检查、日志环 |
| 前端状态 | `web/src/stores/preview.ts` | 轮询、合并、日志、stale |
| 前端 UI | `web/src/features/preview/PreviewPanel.vue` | 工具栏、刷新/重启/日志/新窗口 |
| iframe | `web/src/features/preview/PreviewFrame.vue` | 签名 URL、空态/失败态 |

### 3.3 当前能力与缺口

| 维度 | 现状 | 缺口 |
|---|---|---|
| 状态 | `none / starting / ready / failed` + `stale` | 无阶段进度、队列位置、预计耗时 |
| 日志 | 拉取尾部 N 行（前端最多 128 KB） | 无实时流、无阶段分组、失败定位弱 |
| 版本 | 每项目一个当前预览 | 无历史 generation 产物、无回滚 |
| 访问 | 子域 + ticket/cookie | 公网端口受限；无分享；无 `frame-ancestors` |
| 体验 | 刷新/重启/日志/新窗口 | 无设备尺寸、全屏、缩放、截图、控制台错误 |
| 性能 | 依赖沙箱冷启动与构建 | 无预热池、无产物缓存、无 CDN |
| 隔离 | 每项目独立沙箱与子域 | iframe CSP/网络出口策略未收紧 |

### 3.4 当前限制说明

- 预览监听在 `:8081`，云安全组未放行时只有服务器本机可访问。
- 80 端口使用 `nip.io` 等未备案域名时，云厂商可能拦截。
- `/api/conversations/{id}/preview` 响应只有 `status/url/commit/generation/stale`，**不包含错误码与日志尾部**。
- 前端在 `starting` 时每 5 秒轮询一次，构建完成后才能看到 `ready`。
- 构建使用 `start.sh`，每次先停旧进程再启动，不支持 HMR。

---

## 4. 实测调用结果

### 4.1 环境与步骤

- 环境：线上 `/tenon/`，MySQL 存储，本地确定性模型 `local-demo`。
- 步骤：登录 → 创建临时项目 → 发送「做一个最小计数器页面」→ 轮询 Run → 查询预览 → 拉日志 → 打开签名入口 → 清理。

### 4.2 结果

| 步骤 | 调用 | 结果 |
|---|---|---|
| 创建项目 | `POST /api/conversations` | `201`，返回项目 ID |
| 发送需求 | `POST /api/conversations/{id}/messages` | `202` |
| 执行 | 轮询项目详情 | 10–80s 为 `running`，90s 变 `idle` |
| 预览状态 | `GET /api/conversations/{id}/preview` | 首次轮询即 `ready`，`generation=1`，`stale=false` |
| 文件树 | `GET /files/tree?path=` | 10 个条目 |
| 预览日志 | `GET /preview/logs?tail=15` | Vite 构建 31 modules、1.15s；服务监听 `0.0.0.0:45471` |
| 签名入口 | `GET https://<key>.preview.<domain>/__enter?t=...` | `302` → `/`，写入预览 Cookie |
| 预览页面 | `GET /`（带 Cookie） | `200`，489 B，`<title>原型工作台</title>` |
| 清理 | `DELETE /api/conversations/{id}` | `204`，工作区已删除 |

### 4.3 观察结论

1. **链路可用**：生成的是实际 Vite/Go 应用，不是静态占位页。
2. **构建很快**：本次依赖缓存命中，Vite 构建仅 1.15s；但冷启动仍可能到几十秒。
3. **中间状态不可观测**：Run 结束后的首次预览查询已经是 `ready`，前端看不到 `starting → ready` 的阶段；SSE 才能解决。
4. **失败信息不足**：`/preview` 没有错误码，失败时只能拉日志尾部。
5. **入口受限**：签名 URL 使用 `:8081` 子域，公网未放行时只能服务器本机验证。
6. **单版本**：只有一个活跃预览，没有历史 generation 可用于回滚或分享。

> 本次临时项目与工作区已删除，仓库与线上数据未留下测试项目。

---

## 5. 产品设计方案

### 5.1 预览面板结构

```
┌─────────────────────────────────────────────────────────────┐
│ 代码  预览   [ready · v12 · 3s]      [设备▾][缩放][刷新][日志] │
│                                      [重启][分享][新窗口][全屏] │
├─────────────────────────────────────────────────────────────┤
│   ┌─────────────── 设备画布 ───────────────┐                │
│   │                                       │                │
│   │         生成应用 iframe               │                │
│   │                                       │                │
│   └───────────────────────────────────────┘                │
├─────────────────────────────────────────────────────────────┤
│ 构建日志 / 控制台错误 / 网络请求（可折叠抽屉）                 │
└─────────────────────────────────────────────────────────────┘
```

### 5.2 状态与文案

| 状态 | 展示 | 行为 |
|---|---|---|
| `none` | 预览会出现在这里；第一轮任务完成后自动构建 | 显示空态引导 |
| `starting` | 正在准备/构建/启动 + 阶段 + 已耗时 | SSE 推送阶段；可查看实时日志 |
| `ready` | 可用 · vN · 构建耗时 | 正常 iframe；可分享/全屏/截图 |
| `stale` | 旧版本 · 新版本构建中 | 继续展示上一个可用版本 |
| `failed` | 构建失败 + 错误码 + 日志入口 | 保留旧版本；重试；把错误发给 Agent |

### 5.3 关键交互

- **设备模式**：桌面 1440×900、平板 768×1024、手机 390×844；支持旋转、适应宽度。
- **日志抽屉**：按「安装 / 构建 / 启动 / 健康检查」分段；失败自动展开并高亮错误行。
- **版本菜单**：generation、commit、时间、状态；回滚到历史可用版本。
- **分享**：TTL（1h/24h/7d）只读链接；可撤销；不暴露主站登录态。
- **失败兜底**：保留上一个可用版本；提供「重试」「用 Agent 修复」。
- **移动端**：预览全屏浮层，顶部保留关闭与设备切换。

---

## 6. 技术方案

### 6.1 目标架构

```
┌──────────────┐        /api         ┌────────────────────────────┐
│  Vue 前端     │ ──────────────────▶ │ Control Plane              │
│  PreviewPanel │                     │  ├ Preview Orchestrator    │
└──────┬───────┘                     │  ├ Build Scheduler         │
       │ iframe                      │  ├ Preview Gateway         │
       │                             │  └ Event Bus (SSE)         │
       ▼                             └──────────┬─────────────────┘
┌──────────────────────────┐                    │
│ Preview Gateway :443     │                    │ build/start
│ <key>.preview.domain     │                    ▼
│  ├ ticket → cookie       │        ┌──────────────────────────┐
│  ├ CSP / 限流 / WS       │        │ Sandbox (per project)    │
│  └ reverse proxy         │◀──────▶│  wsd + agent-runtime     │
└──────────┬───────────────┘        │  app runtime             │
           │                        └──────────┬───────────────┘
           │                                   │ 产物
           ▼                                   ▼
┌──────────────────────────┐        ┌──────────────────────────┐
│ Artifact Store / CDN     │◀───────│ Immutable Generation gN  │
│ bundle / image / DB 快照  │        │ manifest + logs + commit │
└──────────────────────────┘        └──────────────────────────┘
```

### 6.2 两种预览模式

| 模式 | 用途 | 实现 | 特点 |
|---|---|---|---|
| **Live（开发态）** | 当前项目迭代 | 沙箱内 dev server + HMR，网关代理 WebSocket | 改动即时可见，资源常驻 |
| **Snapshot（快照态）** | 分享、回滚、验收 | 构建产物不可变存储，按 generation 提供 | 秒开、可缓存、可长期保留 |

推荐：保留 Live 模式做编辑态；每次 Run 完成后生成 Snapshot 产物，预览默认展示最新可用 Snapshot。构建失败不影响已有预览，分享也不依赖沙箱存活。

### 6.3 Generation 状态机

```
queued
  │
  ▼
building ──fail──▶ failed(code, log_tail)
  │
  ▼
starting ──fail──▶ failed
  │
  ▼
ready ──new commit──▶ stale(继续服务旧版本) ──▶ building(gN+1) ──▶ ready
  │
  └──restart──▶ building(同 generation)
```

规则：

- 每个 `(project_id, generation)` 唯一；重复触发幂等。
- 构建失败不删除上一个 `ready` 产物。
- `stale` 表示「有更新版本在构建」，不是不可用。
- 网关只服务 `ready` 的 generation；`starting` 返回可重试的 loading 页。

### 6.4 构建流水线

1. **触发**：Run 完成拿到 commit → 入队；同项目串行，跨项目并发受 slot 限制。
2. **准备**：复用沙箱；校验磁盘/配额；恢复依赖缓存。
3. **安装**：`npm ci --prefer-offline`（共享 npm cache）。
4. **构建**：`npm run build` + `go build`；输出结构化阶段日志。
5. **启动**：启动服务，健康检查 `/api/v1/health`（当前是 `/`）。
6. **发布**：写产物 manifest，切换 active generation，SSE 广播 `ready`。
7. **清理**：保留最近 N 个 generation；回收旧进程/端口；孤儿清理。

优化点：

- 模板预装 `node_modules`，依赖变更才重新安装；
- Go build cache、npm cache 持久化到项目卷；
- 预热池：常见模板的沙箱/进程预热，降低冷启动；
- 构建与 Run 解耦：Run 结束后异步构建，前端先看到 `starting` 再流式收到进度。

### 6.5 API 设计

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/conversations/{id}/preview` | 当前/目标 generation 状态（兼容现有响应） |
| GET | `/api/conversations/{id}/preview/events` | SSE：status/phase/log/progress/ready/failed |
| GET | `/api/conversations/{id}/preview/logs?follow=1` | 实时日志流；不带 follow 返回尾部 |
| POST | `/api/conversations/{id}/preview/restart` | 同 generation 重建 |
| POST | `/api/conversations/{id}/preview/rollback` | `{generation}` 切回历史可用版本 |
| POST | `/api/conversations/{id}/preview/share` | `{ttl}` → 只读分享 URL |
| DELETE | `/api/conversations/{id}/preview/share` | 撤销分享 |
| GET | `/api/conversations/{id}/preview/screenshot` | 可选：服务端截图（设备尺寸） |

> 路径沿用当前 `conversations` 命名；若未来 API 统一为 `projects`，可加别名，不影响前端。

SSE 事件示例：

```json
{"type":"phase","generation":12,"phase":"install","progress":0.2}
{"type":"log","generation":12,"stream":"build","line":"vite v6 ..."}
{"type":"status","generation":12,"status":"ready","url":"https://.../__enter?t=..."}
{"type":"failed","generation":12,"code":"BUILD_FAILED","log_tail":"..."}
```

网关入口：

- `GET /__enter?t=<ticket>`：一次性票据 → 设置 Host-only、HttpOnly、Secure、SameSite=None Cookie → 302 到 `/`。
- `GET /__share?t=<share-token>`：只读分享票据，独立 Cookie/Path，可撤销。
- 其余路径：校验 Cookie → 反代到目标 generation 的 runtime；透传 WebSocket（HMR）。
- 响应头：`Content-Security-Policy: frame-ancestors <主站 origin>`、`Referrer-Policy: no-referrer`、`X-Content-Type-Options: nosniff`。

### 6.6 数据模型

```sql
t_preview (
  id, conv_id, generation, commit,
  status, phase, progress,
  artifact_uri, runtime, entry_path, port,
  error_code, log_uri,
  created_at, started_at, ready_at,
  UNIQUE(conv_id, generation)
)

t_preview_token (
  id, conv_id, generation, kind,      -- enter / share
  token_hash, expires_at, revoked_at, created_at
)

t_preview_event (
  id, conv_id, generation, seq, type, payload, created_at
)
```

现有 `t_sandbox.preview_*` 保留为「当前活跃 runtime」缓存；历史状态迁移到 `t_preview`。

### 6.7 路由与网关

- **子域优先**：生成应用的绝对路径、Cookie、Service Worker、WebSocket 在子域下无需改写。
- **路径前缀仅作兜底**：若必须使用 `/preview/<id>/`，需要按 v0 的方式做 catch-all 代理，处理根相对资源与前端路由。
- **通配证书**：`*.preview.<domain>`；若域名受限，可先用独立预览域名 + 反向代理端口。
- **分享与登录入口分离**：分享 Cookie 只读、独立 Path、可撤销；不复用主站 Session。

### 6.8 安全设计

| 层面 | 措施 |
|---|---|
| origin 隔离 | 预览独立域名/子域，避免与主站共享 Cookie/Storage |
| 入口鉴权 | 一次性 ticket、短 TTL、绑定 Host；分享 token 可撤销 |
| iframe | `allow-scripts allow-forms allow-same-origin allow-popups`；`referrerpolicy=no-referrer` |
| CSP | `frame-ancestors` 限定主站；禁止第三方嵌入 |
| Cookie | Host-only、HttpOnly、Secure、SameSite=None；不复用主站 Cookie |
| 网络出口 | 默认拒绝内网/元数据地址；按需放行 npm registry 与用户 API |
| 资源 | CPU/内存/磁盘/构建时长/并发配额；构建进程非 root、seccomp |
| 接口 | 入口/日志/分享限流；日志脱敏；审计 |

### 6.9 性能、可靠性与可观测性

| 指标 | 目标 |
|---|---|
| 已有版本首屏 | < 1s |
| 热构建（依赖未变） | P50 < 10s，P95 < 30s |
| 冷构建 | P95 < 90s |
| 预览可用性 | 99.9%（Snapshot）/ 99.5%（Live） |
| 回滚 | < 2s |

可靠性：

- 构建幂等 + generation fencing；
- 预览进程健康检查 + 自动重启；
- 沙箱回收后按需唤醒；
- 旧版本保留 N 个，按磁盘水位淘汰；
- 构建失败不影响已就绪预览。

可观测性：

- 指标：队列等待、各阶段耗时、构建成功率、预览 5xx、入口 401 率；
- 结构化日志：build_id、generation、phase、duration、error_code；
- Trace：一次 Run 到一次预览构建的关联 ID。

### 6.10 方案选型

| 方案 | 说明 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| A. 演进现有沙箱预览 | 保持 `start.sh` + 子域代理，补 SSE/设备/分享 | 改动小、最快上线 | 无版本产物、冷启动、分享依赖沙箱 | P0 采用 |
| B. 快照产物优先 | 每次构建产出不可变 bundle/image，网关按 generation 服务 | 秒开、可回滚、可分享、可 CDN | 需产物存储与运行时抽象 | **目标架构** |
| C. 浏览器 WebContainers | 纯前端在浏览器运行 | 零服务器成本、毫秒启动 | 无法运行 Go/SQLite/原生依赖 | 仅静态分支 |

推荐：**A 先落地体验与可观测性，B 作为目标；C 不做主线。**

---

## 7. 分期落地

### P0（约 1 周）：体验与可观测性

- 预览状态/日志改 SSE，展示构建阶段与耗时；
- 设备尺寸切换、全屏、缩放、截图入口；
- 失败保留旧版本 + 自动展开日志 + 重试；
- 分享链接（TTL、可撤销）；
- 网关补 CSP `frame-ancestors`、安全头、限流；
- 放行 8081 或改用 443 子域（需域名/证书决策）。

### P1（约 1–2 周）：版本与产物

- `t_preview` 历史表 + generation 产物存储；
- 回滚、历史版本列表；
- 依赖缓存/预热池；
- 静态/Snapshot 模式接入 CDN；
- 服务端截图。

### P2（约 3–6 周）：运行时升级

- 容器/microVM 隔离替代本地进程；
- 通配 TLS + 独立预览域名；
- 构建队列与自动扩缩；
- 网络出口策略、审计与配额；
- Live HMR + Snapshot 双模式。

### 验收标准

- 新项目 `none` → 首轮完成后 SSE 顺序收到 `queued/building/phase/ready`；
- 构建期间旧版本仍可访问，标记 `stale`；
- 构建失败保留旧版本，日志可定位到阶段与错误行；
- 回滚到任意历史 generation < 2s；
- 分享链接在 TTL 内可访问、过期/撤销后 401；
- 预览不可被非授权站点 iframe 嵌入；
- 手机/平板/桌面尺寸切换正常，移动端全屏可用；
- 构建成功率、P95 时长、预览 5xx 有指标与告警。

---

## 8. 风险与待决策

### 需要确认

1. 预览域名：继续 `nip.io`，还是使用已备案域名 + 通配证书？
2. 入口：继续 `:8081` 子域，还是统一到 443？
3. 分享是否允许匿名访问，TTL 上限多少？
4. 回滚时 SQLite 数据是否也按 generation 快照（“回滚代码”还是“回滚代码+数据”）？
5. Live HMR 与 Snapshot 的优先级与资源预算。

### 主要风险

- 云厂商对 80/443 未备案域名的拦截、8081 安全组限制；
- 生成应用自身安全问题（XSS、任意请求）需要 CSP/出口策略兜底；
- 构建资源消耗与滥用，需要配额、队列与审计；
- SQLite 单文件状态使“代码回滚”和“数据回滚”语义复杂，需产品明确。

---

## 9. 附录

### 9.1 当前 `/preview` 响应示例

```json
{
  "status": "ready",
  "url": "https://<key>.preview.<domain>/__enter?t=<ticket>",
  "commit": "<sha>",
  "generation": 1,
  "stale": false
}
```

### 9.2 建议的错误码

| 错误码 | 含义 | 前端展示 |
|---|---|---|
| `BUILD_FAILED` | 依赖安装/编译失败 | 构建失败，查看日志 |
| `BUILD_SLOT_TIMEOUT` | 构建队列超时 | 构建繁忙，稍后重试 |
| `PREVIEW_TIMEOUT` | 启动后健康检查超时 | 启动超时，查看日志 |
| `PORT_CONFLICT` | 预览端口被占用 | 重启预览 |
| `DISK_FULL` | 工作区磁盘超限 | 清理文件或联系管理员 |
| `SANDBOX_STARTING` | 沙箱正在启动 | 自动重试 |
| `RUN_IN_PROGRESS` | Run 进行中，禁止重启 | 等待当前任务完成 |

### 9.3 复现本次调用

```bash
# 登录后
curl -s -c /tmp/ck -X POST "$BASE/api/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"<user>","password":"<password>"}'

ID=$(curl -s -b /tmp/ck -X POST "$BASE/api/conversations" \
  -H 'Content-Type: application/json' -d '{}' | jq -r .id)

curl -s -b /tmp/ck -X POST "$BASE/api/conversations/$ID/messages" \
  -H 'Content-Type: application/json' \
  -d '{"client_request_id":"probe-1","content":"做一个计数器"}'

# 轮询
curl -s -b /tmp/ck "$BASE/api/conversations/$ID/preview"
curl -s -b /tmp/ck "$BASE/api/conversations/$ID/files/tree?path="
curl -s -b /tmp/ck "$BASE/api/conversations/$ID/preview/logs?tail=20"
```

> 仅供本地/测试环境复现，注意不要在脚本中硬编码生产密码。

---

## 参考链接

- [v0 Accessing Previews](https://v0.app/docs/api/v2/guides/accessing-previews)
- [v0 API introduction](https://vercel.com/blog/introducing-the-new-v0-api)
- [WebContainers Introduction](https://webcontainers.io/guides/introduction)
- [Bolt.new tech breakdown (PostHog)](https://posthog.com/newsletter/inside-bolt-dot-new)
- [CodeSandbox Preview URLs](https://codesandbox.stream/docs/learn/environment/preview-urls)
- [Lovable Preview docs](https://docs.lovable.dev/features/projects/preview)
- [Replit Ports](https://docs.replit.com/features/project-setup/ports)
- [E2B Sandbox public URL](https://docs.e2b.dev/network/public-url)
