# Tenon 安全审查复核与改造设计

> 状态：设计草案 v0.1  
> 日期：2026-10-26  
> 审查基线：`main` @ `c8615d7`（安全相关代码与 `b2eb18f` 一致）  
> 输入：外部《Tenon 代码与配置安全审查报告》（2026-10-25，报告基线 `ee7680b`）  
> 性质：防御性静态复核 + 配置/响应头/权限核验 + 改造设计；**未实施任何漏洞利用，未修改业务代码**

---

## 0. 结论摘要

外部报告的核心判断成立：

1. **生产使用 `local` 沙箱，Agent 与控制面共享宿主机用户和文件系统**。文件工具的绝对路径/`../` 未做 jail，Bash 直接在宿主机执行，`full-auto` 又降低了审批门槛。对不可信注册用户开放时，这构成跨项目读取、控制面密钥/数据库凭据暴露、访问回环管理端口的真实攻击面。
2. **生产主站与预览全链路是明文 HTTP**：`web.origin`、`preview.scheme` 均为 HTTP，两个 Cookie 的 `Secure=false`，nginx 只有 80，没有 443/HSTS。密码、会话、对话内容、预览票据都暴露在链路中。

同时有三处需要更精确地表述：

- `local` 驱动给 WSD 的环境变量是**白名单**（`PATH/HOME/LANG/...` 加 sandbox 专用 token），没有直接继承数据库密码或 LLM Key；但沙箱与控制面同 UID，仍可读取 `secret.key`、`/etc/tenon/mysql.env`；在默认 Linux 配置下通常还可读取控制进程的 `/proc/<pid>/environ`（需按目标主机的 ptrace/Yama 配置实测），所以报告的核心结论不受影响。
- 独立 Agent Runtime Web UI 在 Tenon 主流程中被 `disable_webui: true` 关闭，TENON-004 不是主站当前暴露面；但代码路径仍应修复。
- API 路由自身设置了 `X-Frame-Options: DENY`、`nosniff`、`Referrer-Policy`；缺少这些头的是 nginx 直接提供的**静态主站**。报告 TENON-006 指向正确，但表述应区分 API 与静态资源。

报告之外，本轮补充了 12 项发现，主要集中在：预览网关的 CSP/第三方 Cookie、主站 CSP、CSRF 的 Origin 可选、会话 Cookie 加固、数据库最小权限、安全审计与告警、nginx 限流、构建供应链、密钥权限/轮换、诊断端口、密码策略与部署参数化。

**总体建议**：在完成真隔离与 TLS 之前，不要把服务开放给不可信公网用户；优先做「隔离 + TLS + 访问收敛」，再做「工具路径 jail、预览网关、限流、审计、供应链」等纵深防御。任何仅修改文件路径、隐藏 pprof、增加登录限流的措施，都不能替代租户隔离。

---

## 1. 审查方法与证据

### 1.1 方法

1. 逐条阅读外部报告，标记「确认 / 需修正 / 未覆盖」。
2. 对照当前代码核对可达性：认证、会话、CSRF、文件工具、Bash、预览网关、票据、密钥加载。
3. 对生产实例做**只读**配置与响应核验：nginx 路由与响应头、Cookie 属性、文件权限、进程用户、监听端口、MySQL 授权。
4. 不做漏洞利用、不读取他人数据、不修改配置；登录验证使用测试账号并立即登出。
5. 依据 CWE/OWASP ASVS 的通用分类给出改造设计，不把静态可达等同于已发生入侵。

### 1.2 关键证据

| 项目 | 观察结果 |
|---|---|
| 控制面配置 | `mode: local`、`sandbox.driver: local`；`web.origin: http://<prod-host>`；`auth.cookie_secure: false` |
| 预览配置 | `preview.scheme: http`；`public_address: {key}.preview.<host>:8081`；`cookie_secure: false`；监听 `0.0.0.0:8081` |
| 进程身份 | `control` 以 `tenon` 用户运行；WSD/Agent Runtime 由其派生，默认同 UID |
| 敏感文件 | `secret.key`、`control.yaml`、`mysql.env` 均为 `0600 tenon:tenon`；同 UID 沙箱可读 |
| 监听端口 | 公网：`80`、`22`；回环：`8080/8082/9090/3306`；预览：`*:8081` |
| 主站响应头 | `/tenon/` 仅有 `Cache-Control`，**无** `X-Frame-Options`、`CSP`、`nosniff`、`Referrer-Policy` |
| API 响应头 | `X-Frame-Options: DENY`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer` |
| 会话 Cookie | 实测：`HttpOnly; SameSite=Lax; Path=/; Max-Age≈7d`，**无 `Secure`** |
| 登录限流 | 键为 `userID + "|" + RemoteAddr`；`failures` map 无全局 TTL/容量上限；`RemoteIP` 不解析 `X-Forwarded-For` |
| nginx | 仅 80；无 443/HSTS；无 `limit_req`/`limit_conn` |
| MySQL 授权 | 应用账号 `tenon_app` 对 `tenon.*` 与 `tenon_prod.*` 均有 `SELECT, INSERT, UPDATE` |
| 前端 | Markdown 使用 DOMPurify；`safeInternalRedirect` 拒绝 `//`、反斜杠、跨 origin；Vue 路由有守卫 |

> 生产地址、账号和密钥不在本文档重复记录；示例统一用 `<prod-host>` 占位。

---

## 2. 外部报告复核

### 2.1 逐项结论

| 报告 ID | 报告等级 | 复核结论 | 说明 |
|---|---|---|---|
| TENON-001 | Critical | **确认** | `local` 驱动无 OS 级隔离；`absPath` 接受绝对路径；Bash 直接 `exec.Command("bash")`；`full-auto` 是默认策略。WSD 环境变量虽为白名单，但同 UID 可读文件与 `/proc`，结论成立 |
| TENON-002 | High | **确认** | 80 端口明文；无 443/HSTS；`cookie_secure=false`；预览 `0.0.0.0:8081` 且 HTTP |
| TENON-003 | Medium | **确认** | 失败计数按 `账号+IP`；无清理/容量上限；nginx 反代后 `RemoteIP` 只看 `RemoteAddr`，实际来源趋同为回环 |
| TENON-004 | Medium | **确认，但当前影响受限** | 代码确实把长期 Token 放 localStorage/URL；Tenon 主流程 `disable_webui: true`，主站不暴露该 UI。仍应修复独立 Runtime |
| TENON-005 | Medium | **确认** | 公开注册无邮箱/邀请/审批；注册限流为进程内存按 IP；反代后可能变成全站共享额度 |
| TENON-006 | Medium | **确认** | 静态主站无 XFO/CSP；API 有安全头，但不能覆盖 nginx 静态响应 |
| TENON-007 | Low | **确认** | `9090` 注册 `/metrics`、`/debug/pprof/*`、`/debug/status`，无认证；仅回环监听，但 local 沙箱同主机/同 UID 可达 |
| TENON-008 | Low | **确认** | systemd 仅有 `User/Group/EnvironmentFile/Restart`，缺少 `NoNewPrivileges`、`ProtectSystem` 等 |
| TENON-009 | Info | **确认** | 生产 YAML、nginx、部署文档硬编码真实公网地址与拓扑 |

### 2.2 需要修正或补充的表述

1. **环境变量暴露链**：`internal/control/sandbox/local.go` 只向 WSD 注入 `PATH/HOME/LANG/WSD_* /LLM_PROXY_TOKEN/WSD_TOKEN` 等白名单变量，不继承 `MYSQL_*` 或模型 Key。因此「WSD 环境直接泄露数据库密码」不准确；真实风险是**同 UID 文件读取**与**`/proc` 进程环境读取**，以及沙箱内应用/Bash 对宿主文件系统的访问。
2. **Runtime Web UI**：`internal/wsd/boot/config.go` 设置 `disable_webui: true`，主站不加载该 UI。TENON-004 应标记为「独立 Runtime 的遗留风险」，避免误判为当前主站入口。
3. **安全响应头归属**：API middleware 已有 XFO/nosniff/referrer；缺失的是静态 HTML 与预览网关。整改时应分别配置，避免在 API 上重复或误配。
4. **报告未复跑 Go 漏洞扫描**：npm 生产依赖为 0 告警，但没有 `govulncheck`；不能据此推断 Go 依赖安全。本文档把 `govulncheck`、SBOM、密钥扫描列入供应链改造。
5. **「立即暂停公网服务」是产品/运维决策**：技术上等价的安全替代是「关闭公开注册 + 仅可信网络/受控账号 + TLS」，在隔离完成前不向不可信用户开放。

### 2.3 报告未覆盖的补充发现

| ID | 等级 | 发现 | 影响 |
|---|---|---|---|
| SEC-101 | Medium | 预览网关未设置 `frame-ancestors`/CSP，且 iframe 依赖第三方预览 Cookie；HTTP 下 `SameSite=Lax` 在跨站 iframe 中可能不发送，HTTPS 下 `SameSite=None` 又会被浏览器第三方 Cookie 策略拦截 | 预览可能空白；可被恶意站点嵌入；分享/嵌入行为不可控 |
| SEC-102 | Medium | 主站静态 HTML 没有 CSP；XSS 主要依赖 DOMPurify 与 Vue 转义 | 缺少纵深防御；一旦出现注入点，缺少浏览器层兜底 |
| SEC-103 | Low | CSRF middleware 只拒绝「Origin 非空且不匹配」和 `Sec-Fetch-Site: cross-site`；缺少两者时不拒绝 | 非浏览器客户端或未来客户端行为变化时，防护不完整 |
| SEC-104 | Low | 会话 Cookie 不是 `__Host-` 前缀；TTL 7 天且无空闲超时；无会话列表/单会话撤销 | Cookie 被窃取后的窗口较长，难以定位异常会话 |
| SEC-105 | Medium | 应用 DB 账号对 `tenon.*` 和 `tenon_prod.*` 都有全表 `UPDATE`，包括 `t_user` | SQL 注入或应用账号泄露时，影响面大于必要范围 |
| SEC-106 | Medium | 没有结构化安全审计与告警（登录失败、注册、会话撤销、预览入口、管理操作） | 入侵/滥用发现滞后，事件响应缺证据 |
| SEC-107 | Low/Medium | nginx 没有 `limit_req`/`limit_conn`、请求体与超时策略只在应用层 | 暴力破解、慢连接、批量注册可在网关层放大 |
| SEC-108 | Medium | 生成应用执行 `npm install`/生命周期脚本，构建进程与宿主同 UID；无私有 registry/allowlist | 恶意依赖可读取宿主文件、访问内网（在 local 模式下） |
| SEC-109 | Low | `LoadMaster` 读取已有密钥时不校验 owner/mode；无轮换流程 | 权限漂移或密钥泄露后缺少检测/恢复路径 |
| SEC-110 | Low | 密码最小 8 字节；无 breached-password 检查、无 MFA；注册即登录 | 弱口令与撞库风险，账号被接管后可直接消耗资源 |
| SEC-111 | Low | pprof/metrics/status 无认证；metrics 标签可能包含项目/租户维度 | 信息收集与 CPU profile 资源消耗（与 TENON-007 重叠） |
| SEC-112 | Info | 生产域名/IP/端口硬编码；源码文档与实例 runbook 混在一起 | 环境耦合、误部署、公开仓库暴露攻击面（与 TENON-009 重叠） |

> SEC-101/102/103/104/105/106/107/108/109/110/111/112 编号为本文档自用，便于改造排期；不是外部报告原有编号。

---

## 3. 系统安全现状

### 3.1 资产与信任边界

```
Internet
  │ 80 (当前) / 443 (目标)
  ▼
nginx ── /tenon/ 静态 SPA
  │    ── /api/ 反代 → control 127.0.0.1:8080
  │    ── /ops/ 管理端（IP allowlist）
  │    ── *.preview.<domain>:8081 → preview gateway
  ▼
control (tenon)
  ├─ auth/session/CSRF/quota
  ├─ MySQL (tenon_app: SELECT/INSERT/UPDATE)
  ├─ master secret / data/secret.key
  ├─ sandbox manager
  │    └─ local driver: wsd + agent-runtime + preview app（同 UID）
  └─ LLM proxy（上游 Key 只在后端环境）
        │
        ▼
   沙箱工作区 /data/server/data/sandboxes/<conv>/
   （同 UID 可访问其他会话、控制面文件、回环端口）
```

**核心问题**：图中「沙箱」不是安全边界，只是进程/目录管理；认证用户的 Agent 实际获得 `tenon` 用户在宿主机上的文件与网络能力。

### 3.2 已确认的正面控制

1. Argon2id（64 MB、t=2、2 线程）、随机 salt、常数时间比较；不存在用户使用 dummy hash，降低用户名枚举的时序差异。
2. 会话 token 随机生成，数据库仅存 SHA-256 哈希；登出会撤销会话。
3. 会话 Cookie：`HttpOnly`、`SameSite=Lax`；生产只需打开 `Secure`。
4. CSRF：非 GET/HEAD 检查 `Origin` 与 `Sec-Fetch-Site`，已覆盖主要浏览器攻击路径。
5. 核心 API 做属主校验；参数化 SQL；请求体上限 1 MiB，nginx 2 MiB。
6. 控制面文件读取使用 `os.Root`，对路径/symlink 逃逸有防护（注意：不适用于 Agent 自己的文件工具）。
7. 预览使用一次性 Ticket、HMAC、host 绑定 Cookie；预览子域与主站分离；票据消费后可重放失败。
8. 前端 Markdown 使用 DOMPurify，禁用原始 HTML；`safeInternalRedirect` 拒绝开放重定向。
9. 主密钥 32 字节随机、新建文件 0600；生产缺失密钥 fail-closed。
10. 模型 API Key 只在后端使用，不进入浏览器；Bash 工具环境有 allowlist 与敏感变量 denylist。
11. MySQL 应用账号没有 DELETE/DDL 权限（但 UPDATE 范围过宽，见 SEC-105）。
12. 镜像 Dockerfile 使用非 root UID/GID 10001，可作为后续容器隔离基础。

### 3.3 风险收敛判断

| 场景 | 当前是否可接受 | 说明 |
|---|---|---|
| 单用户、可信网络、受控账号 | 有条件可接受 | 仍需 TLS、备份、日志与最小权限 |
| 公网自助注册、多租户 | **不可接受** | TENON-001 + TENON-002 是上线阻断项 |
| 内部演示、无敏感数据 | 有条件可接受 | 关闭注册、限制来源；仍不建议明文 HTTP |
| 对外分享预览 | 不可接受 | 预览 Cookie/嵌入策略未完成，见 SEC-101 |

---

## 4. 改造总体设计

### 4.1 目标架构

```
                    TLS 443
Internet ────────── nginx ──────────────┐
  │                 ├─ /tenon/ 静态 SPA  │
  │                 ├─ /api/ → control   │
  │                 ├─ /ops/ → ops app   │
  │                 └─ *.preview → gateway
  │                                       ▼
  │                              control (host user, no untrusted exec)
  │                                ├─ auth / session / quota
  │                                ├─ sandbox orchestrator
  │                                ├─ preview gateway / tickets
  │                                ├─ artifact store
  │                                └─ MySQL / secret manager
  │
  └── iframe ── preview gateway ── sandbox network
                                     └─ container per project
                                        ├─ wsd
                                        ├─ agent-runtime
                                        ├─ app process
                                        └─ workspace volume (rw)
```

**不变式**：

1. 控制面与不可信执行面必须是不同安全主体（不同 UID/namespace/容器）。
2. 沙箱只挂载当前项目工作区；根文件系统只读；不挂载宿主敏感目录、Docker socket、控制面配置。
3. 沙箱默认无公网，按需 egress；不能访问 control 回环端口、云元数据、内网管理网。
4. 预览只经网关访问；预览进程不直接暴露宿主端口。
5. 密钥只存在于控制面/密钥管理，不进入沙箱环境、命令行、日志或工作区。

### 4.2 隔离模型（P0）

**短期（1 周内，最小可用）**

- 每个项目一个 rootless 容器（Podman/Docker/containerd 均可），镜像内包含 `wsd + agent-runtime + bash + node/go` 构建工具。
- 容器参数：
  - `--read-only`，`/tmp` tmpfs，工作区 volume 只挂当前项目；
  - `--cap-drop ALL`、`no-new-privileges`、seccomp 默认 profile、AppArmor（可选）；
  - cgroups v2：CPU、内存、PIDs、磁盘（volume quota/项目配额）、构建超时；
  - 独立 network namespace：默认 deny，egress 只允许 npm registry、Go proxy、控制面 LLM 代理（经固定域名/端口）；显式阻断 `127.0.0.0/8`、`169.254.169.254`、RFC1918、云元数据；
  - 每个项目独立 UID 或 userns 映射；工作区 0700；
  - 不挂载 `/data/server/config`、`/etc/tenon`、`secret.key`、宿主 `/home`、`/var/run/docker.sock`。
- 控制面通过容器 IP:port 或内部代理访问 wsd/预览，不再使用宿主 `127.0.0.1` 端口。
- `local` 驱动保留为开发模式：启动时若 `mode=local` 且 `allow_untrusted=true`，**拒绝启动**；生产配置必须 `sandbox.driver=container`。

**中期（1 个月）**

- 迁移到 microVM（Firecracker/Cloud Hypervisor）或 gVisor/Kata，获得更强内核隔离与快照恢复能力；
- 预热池 + 快照，降低冷启动；
- 每项目独立网络策略、审计与配额；
- 预览运行时与构建运行时分离（构建完成后静态产物/镜像进入 artifact store）。

**验收**

- 两个普通用户互相读取工作区失败；
- 沙箱读取 `secret.key`、`mysql.env`、`/proc/<control>/environ` 失败；
- 沙箱访问 `8080/8082/9090`、云元数据、内网地址失败；
- 绝对路径、`../`、symlink、hardlink、挂载点均无法逃逸；
- CPU/内存/PID/磁盘/进程数超限只影响该沙箱；
- 删除容器后工作区按策略保留/清理，不能残留可被其他项目读取的副本。

### 4.3 TLS 与认证（P0）

**TLS**

- nginx 增加 443（或云 LB 终止 TLS），80 仅 301 到 443；配置自动续期与到期告警。
- 预览使用独立通配证书：`*.preview.<domain>`；统一经 443 网关，关闭公网 `8081`，或仅允许网关回源。
- HSTS 在确认所有子域支持 HTTPS 后启用；先短 `max-age`，再评估 `includeSubDomains`/preload。
- 内部 control ↔ 沙箱可保留明文回环/容器网络，但不得有公网绕过路径。

**Cookie**

- 主站：`Secure; HttpOnly; SameSite=Lax; Path=/`，建议 `__Host-` 前缀；`web.origin` 改 HTTPS。
- 预览：`Secure; HttpOnly; SameSite=None; Partitioned`（或改用路径代理，见 4.5）；Host-only，不带 Domain。
- 会话 TTL：默认 7 天过长；建议绝对 12h–24h、空闲 30–60min，登录/提权后轮换，提供“退出所有会话”。

**认证与限流**

- 可信代理配置：仅从配置的 CIDR 解析 `X-Forwarded-For` 的最右不可信跳；禁止直连伪造。
- 四维限流：`account+IP`、`account`、`IP`、全局；失败状态有 TTL、最大容量、LRU 淘汰；多实例用 Redis/共享存储。
- 登录失败采用渐进延迟/短期挑战，避免永久锁定被用于 DoS；对目标账号攻击要能告警。
- nginx 增加 `limit_req`（登录/注册/预览入口）、`limit_conn`、`client_body_timeout`、`client_header_timeout`。
- 注册：默认关闭公开注册；如必须开放，使用邮箱/手机验证、邀请码、风险挑战与全局配额；注册成功不直接获得无限资源。
- 密码：最小 12 字节或 zxcvbn 强度；breached-password 检查（k-anonymity）；MFA 作为可选/管理员强制。

### 4.4 Agent 工具与执行策略（P0/P1）

**路径 jail**

- 所有文件工具（read/write/edit/glob/grep）统一使用 `os.Root` 或 `securejoin`；拒绝绝对路径、`..`、symlink 逃逸；根目录固定为项目工作区。
- `allowed_cwd_roots` 只约束会话 cwd，不能代替工具 jail；两者都要保留。
- Bash 仍在容器内执行；容器是最终边界，文件 jail 是纵深防御。

**审批策略**

- `full-auto` 只在已隔离沙箱中启用；生产默认对写文件、安装依赖、网络访问、`git push` 等高风险操作要求审批或策略允许。
- `read_file` 在容器内也默认允许，但容器不得挂载控制面文件；不要把“只读工具免审批”当成安全边界。
- 工具环境：保留 allowlist；每个项目独立 HOME/cache；不注入控制面环境变量。
- 测试：`/etc/passwd`、`../../`、symlink、hardlink、`glob ../*`、`bash cat /data/server/...` 全部失败。

**构建供应链**

- 生成应用依赖安装默认走私有 registry/allowlist 或镜像缓存；对 `npm install` 生命周期脚本做策略（能关闭则关闭，必须开启时在容器内）。
- 锁定 lockfile、校验完整性；构建日志脱敏；限制网络到依赖源。

### 4.5 预览网关与嵌入策略（P1）

**现状问题**

- 预览 Cookie 在跨站 iframe 中受第三方 Cookie 策略限制：HTTP + `SameSite=Lax` 可能不发送；HTTPS + `SameSite=None` 可能被浏览器拦截。UI 已提示“使用新窗口”，说明这是已知体验缺口。
- 网关未设置 `frame-ancestors`/CSP；预览可被任意站点嵌入。
- 入口 Ticket 在 URL 中，虽一次性且短 TTL，仍需避免日志/Referrer 泄露。

**设计选项**

| 方案 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| A. 子域 + CHIPS | `SameSite=None; Secure; Partitioned`，网关设置 `frame-ancestors` 主站 | 改动小，保持子域隔离 | 浏览器支持差异；Safari/隐私模式可能仍拦截 |
| B. 路径代理 | `/preview/<id>/` 由主站同源代理，iframe 同源；用独立 sandbox 属性/无 Cookie | 无第三方 Cookie；鉴权简单 | 需 catch-all 改写根相对资源、Cookie、WebSocket、Service Worker；v0 式代理复杂 |
| C. 新窗口/独立页 | 预览只允许顶层打开，iframe 仅展示静态截图/占位 | 最简单、安全 | 失去内嵌交互体验 |

**推荐**：短期 A + 新窗口兜底；中期评估 B（若产品强依赖内嵌）。无论哪种：

- 网关响应头：
  - `Content-Security-Policy: frame-ancestors <主站 origin>`（不限制生成应用自身资源）；
  - `X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`；
  - `Permissions-Policy` 按需收紧。
- Ticket：一次性、1 分钟、Host 绑定；入口响应 `Referrer-Policy: no-referrer`；日志脱敏 query。
- 分享：独立只读 token/ Cookie，TTL 可撤销；不复用主站会话。
- 限流：入口、资源、WebSocket 分别限流；预览进程 egress 隔离。
- 验收：Chrome/Safari/Firefox 下 iframe 与顶层窗口都可用；跨站嵌入被阻止；Ticket 重放失败；分享撤销立即生效。

### 4.6 滥用、配额与成本（P1）

- 注册闸门 + 账号审批/验证；
- 每账号：项目数、磁盘、每日构建数、每日 token/费用、并发 Run；
- 全局：注册速率、构建并发、磁盘水位、模型费用熔断；
- 闲置项目/沙箱回收；管理员停用、配额调整、审计导出；
- 告警：注册突增、登录失败、构建失败率、预览 5xx、单账号成本异常。

### 4.7 密钥与数据库最小权限（P0/P1）

**主密钥**

- 启动时校验 `secret.key`：owner 必须是服务用户、mode `0600`、目录 `0700`；否则生产 fail-closed。
- 轮换流程：生成新 key → 双 key 验证窗口（或强制所有会话/票据失效）→ 旧 key 退役；记录审计。
- 备份加密；不要把 key 放进镜像、Git、日志、支持包。
- 容器内不可见；控制面环境变量也不应向沙箱暴露。

**数据库**

- 生产应用账号只授权 `tenon_prod` 的必要表：
  - 读写：`t_conversation/t_message/t_run/t_event/t_llm_usage/t_sandbox/t_auth_session` 等；
  - `t_user` 仅 SELECT（改密走管理员/独立账号）；
  - 禁止 DDL/DELETE/GRANT；
- 撤销 `tenon.*`（开发库）授权；生产与开发账号分离；
- 迁移使用独立管理员账号，应用启动不执行 DDL；
- 开启慢查询/错误日志脱敏；数据库仅监听回环/私网。

### 4.8 浏览器与运营加固（P1/P2）

**主站响应头**

- `Content-Security-Policy`：先 Report-Only，确认 Monaco worker（`worker-src blob:`）、Vite 资源、SSE、预览 iframe 不被破坏后转 enforce；至少 `default-src 'self'`、`object-src 'none'`、`base-uri 'self'`、`frame-ancestors 'none'`。
- `X-Frame-Options: DENY`（兼容旧浏览器）。
- `X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`、`Permissions-Policy` 最小化。
- 静态 HTML 与前端路由回退都带同样头；API 与预览网关分别配置。

**CSRF**

- 对 Cookie 认证的 unsafe 方法，要求 `Origin` 或 `Sec-Fetch-Site` 至少一个存在且可信；两者都缺失时拒绝（保留 CLI 的独立 Bearer 通道）。
- 登录/注册同样校验；错误响应不区分“来源缺失/不匹配”。

**诊断与 systemd**

- 生产关闭 pprof；metrics/status 放独立管理网络，加认证或仅 Unix socket；
- systemd：`NoNewPrivileges=yes`、`PrivateTmp=yes`、`ProtectSystem=strict`、`ProtectHome=yes`、`ProtectProc=invisible`、`RestrictAddressFamilies`、`SystemCallFilter`、`CapabilityBoundingSet`、`ReadWritePaths` 精确开放 `/data/server`；
- 使用 `systemd-analyze security` 做基线与回归。

**审计与告警**

- 结构化事件：登录成功/失败、注册、会话撤销、密码变更、项目删除、预览入口失败、构建失败、管理操作；
- 字段：时间、request_id、user_id、来源 IP（可信解析）、结果、原因码；不记录密码/token/完整票据；
- 保留期与访问控制；SIEM/告警接入；
- 指标：401/403/429 率、注册速率、构建耗时/失败率、预览 5xx、单账号成本。

### 4.9 供应链与发布（P2）

- CI：`gitleaks`/`trufflehog`、`npm audit --omit=dev`、`govulncheck ./...`、`osv-scanner`、`syft` SBOM、镜像扫描、依赖锁文件校验；
- 生成应用构建：私有 registry/allowlist、`npm ci`、限制生命周期脚本、构建容器无宿主凭据；
- 发布：签名、provenance、可复现构建；部署包不包含源码/密钥；
- 定期轮换数据库/模型 Key；密钥泄露按“撤销优先、清理历史其次”处理。

### 4.10 部署参数化（P2）

- `web.origin`、`preview.domain`、`public_address`、nginx `server_name` 由环境变量/模板注入；
- 公开仓库使用 `<prod-host>`/RFC 5737 保留地址；真实 runbook 单独存放；
- 发布前检查真实 IP、域名、路径、密钥、内部拓扑；
- 不把“隐藏 IP”当安全控制。

---

## 5. 改造项清单

| ID | 优先级 | 对应报告 | 改造项 | 主要验收 |
|---|---|---|---|---|
| ISO-01 | P0 | TENON-001 | 容器化沙箱、独立 UID/namespace、只读根、工作区单挂载、cgroups、seccomp、无宿主敏感目录 | 跨租户/密钥/端口/资源逃逸全部失败 |
| NET-01 | P0 | TENON-001/002 | 沙箱网络默认 deny + 受控 egress；阻断回环控制面、云元数据、内网 | 沙箱无法访问 8080/8082/9090/元数据 |
| TLS-01 | P0 | TENON-002 | 443 + 80→443 + HSTS；预览 HTTPS/通配证书；关闭公网 8081 | 全站 HTTPS，Cookie Secure |
| SEC-01 | P0 | TENON-001 | 控制面密钥/DB 凭据与沙箱分离；`/proc` 隐藏；密钥权限校验 | 沙箱读不到 secret/key/env |
| REG-01 | P0/P1 | TENON-005 | 关闭公开注册或邀请/验证/审批；注册与资源配额 | 批量注册/成本放大被限制 |
| TOOL-01 | P1 | TENON-001 | 文件工具 `os.Root`/securejoin；拒绝对路径/`..`/symlink；全工具覆盖 | 越界文件测试全部失败 |
| AUTH-01 | P1 | TENON-003 | 可信代理 + 四维限流 + TTL/容量 + nginx limit_req；避免账号锁定 DoS | 多 IP/多账号/伪造头测试符合预期 |
| PREV-01 | P1 | SEC-101 | 预览 CSP/headers、Cookie/CHIPS 或路径代理、分享 token、Ticket 脱敏 | 跨站嵌入被阻止；iframe 可用；重放失败 |
| HDR-01 | P1 | TENON-006/SEC-102 | 主站 CSP（Report-Only→enforce）、XFO、nosniff、Referrer/Permissions | 响应头齐全且业务不回归 |
| CSRF-01 | P1 | SEC-103 | unsafe 方法要求 Origin/Sec-Fetch-Site；CLI Bearer 独立通道 | 缺头请求被拒，浏览器流程正常 |
| RUNTIME-01 | P1 | TENON-004 | Runtime 删除 `?token=` 长令牌；SSE 仅一次性 Ticket；CSP/no-referrer | 长 Token 不出现在 URL/存储/日志 |
| DB-01 | P1 | SEC-105 | 应用账号仅生产库必要表；`t_user` 只读；禁止 DDL/DELETE；撤销开发库授权 | `SHOW GRANTS` 最小化；迁移账号分离 |
| AUDIT-01 | P1 | SEC-106 | 安全审计事件、指标、告警、保留/脱敏 | 登录/注册/预览/管理事件可追溯 |
| DIAG-01 | P2 | TENON-007/SEC-111 | 生产关闭 pprof；metrics/status 认证或管理网络 | 沙箱/公网不可达，未授权不可 profile |
| SYS-01 | P2 | TENON-008 | systemd 硬化（NoNewPrivileges/ProtectSystem/ProtectProc/...） | `systemd-analyze security` 改善且功能正常 |
| SUP-01 | P2 | SEC-108/报告第 7 节 | gitleaks、govulncheck、SBOM、镜像扫描、依赖 allowlist/生命周期脚本策略 | CI 门禁通过，无高危 |
| SESSION-01 | P2 | SEC-104 | `__Host-`、TTL/空闲超时、轮换、会话列表/全撤销 | 会话管理可用，Cookie 属性达标 |
| PASS-01 | P2 | SEC-110 | 最小 12 字节/强度、breached 检查、MFA（可选） | 弱口令被拒，管理员可强制 MFA |
| DEPLOY-01 | P2 | TENON-009/SEC-112 | 域名/Origin/地址参数化；公开文档脱敏 | 无真实 IP/密钥进入公开产物 |

---

## 6. 分期路线

### 6.1 0–24 小时：止血

1. **访问收敛**：关闭公开注册；将 `/tenon/`、`/api/` 限制到可信网络/VPN/受控账号；无法保证时暂停公网服务。
2. **TLS**：为 `<prod-host>` 配置 443 与有效证书；80 只跳转；`auth.cookie_secure`、`preview.cookie_secure` 改 true；`web.origin`/`preview.scheme` 改 HTTPS。
3. **预览暴露面**：关闭公网 `8081`，只允许网关回源；或给 8081 单独 TLS + IP allowlist。
4. **安全头**：主站加 `frame-ancestors 'none'` + XFO；预览网关加 `frame-ancestors <主站>`、nosniff、no-referrer。
5. **排查**：检查 `tenon` 用户的进程、登录/注册日志、敏感文件访问迹象；若发现异常，按事件响应轮换会话/数据库/模型 Key。

### 6.2 第 1 周：高风险结构性修复

1. 容器化沙箱 MVP（独立 UID、只读根、工作区单挂载、cgroups、seccomp、网络隔离、egress allowlist）。
2. 文件工具 `os.Root`/securejoin + 越界测试。
3. 登录/注册限流四维化 + 可信代理解析 + nginx `limit_req`。
4. 预览网关安全头、Cookie/分享策略、Ticket 脱敏。
5. 数据库最小权限与密钥权限校验；撤销开发库授权。
6. 关闭公开注册或加邀请/验证；资源与成本配额。

### 6.3 第 1 个月：纵深与运营

1. microVM/快照/预热池（如需更强隔离与更快启动）。
2. 主站 CSP Report-Only → enforce；CSRF 严格化；会话管理。
3. 审计事件、指标、告警、事件响应手册。
4. systemd 硬化、pprof 关闭/管理面认证。
5. SBOM、govulncheck、gitleaks、镜像扫描、发布门禁。
6. 部署参数化与公开文档脱敏。

### 6.4 上线门禁

- 未完成 ISO-01 + NET-01 + TLS-01：不允许公网不可信用户；
- 未完成 TOOL-01 + DB-01 + AUDIT-01：不允许自称“多租户安全”；
- 每次发布前执行跨租户、密钥、端口、资源、TLS、预览嵌入回归。

---

## 7. 复测清单

### 7.1 隔离

- [ ] 用户 A 不能读取/写入/删除用户 B 的工作区、日志、预览。
- [ ] 沙箱读不到 `secret.key`、`mysql.env`、`/data/server/config`；`/proc/<pid>/environ` 在目标主机配置下也不可读。
- [ ] 文件工具拒绝绝对路径、`../`、symlink、hardlink、挂载逃逸。
- [ ] Bash/构建脚本/预览进程处于同一容器边界，无法访问宿主。
- [ ] 沙箱无法访问 8080/8082/9090、云元数据、内网、其他租户网络。
- [ ] CPU/内存/PID/磁盘/进程数限制生效，超限只影响该沙箱。

### 7.2 传输与认证

- [ ] 全站 HTTPS；HTTP 仅 301；HSTS 生效；无明文公网替代入口。
- [ ] 主站/预览 Cookie 具备 `Secure`、`HttpOnly`、合适 `SameSite`；主站建议 `__Host-`。
- [ ] 登录限流覆盖账号、真实 IP、组合键、全局；状态有过期/容量；多实例一致。
- [ ] 伪造 `X-Forwarded-For` 不能绕过；nginx 限流生效。
- [ ] 注册闸门与配额有效；批量账号不能放大成本。
- [ ] Runtime 长 Token 不出现在 localStorage、URL、历史、代理日志。

### 7.3 预览与浏览器

- [ ] 非授权站点无法 iframe 主站与预览。
- [ ] Chrome/Safari/Firefox 下 iframe 与顶层预览均可用（或明确降级为新窗口）。
- [ ] Ticket 一次性、过期、跨 Host、重放均失败；日志不含 Ticket。
- [ ] 分享链接 TTL 生效、撤销立即 401；不暴露主站会话。
- [ ] 主站 CSP 不破坏登录/对话/预览/SSE；Monaco worker 正常。

### 7.4 运维与供应链

- [ ] pprof/metrics/status 仅管理面且认证；生产默认关闭 pprof。
- [ ] systemd 硬化后构建/预览/恢复流程正常。
- [ ] CI 密钥扫描、npm 审计、govulncheck、SBOM、镜像扫描纳入门禁。
- [ ] 数据库账号最小权限；迁移账号分离；密钥轮换演练通过。
- [ ] 审计事件可查询、告警可触发、日志已脱敏。

---

## 8. 待决策与残余风险

### 8.1 待决策

1. **隔离运行时**：rootless 容器（快）还是 microVM（强）？是否接受每项目常驻容器成本？
2. **TLS/域名**：使用自有备案域名 + 通配证书，还是继续 IP/nip.io（不建议用于生产）？
3. **注册模式**：关闭注册、邀请制、还是邮箱验证 + 配额？
4. **预览嵌入**：子域 + CHIPS，还是路径代理？是否接受“新窗口优先”？
5. **会话策略**：绝对/空闲 TTL、是否强制 MFA、是否保留 7 天。
6. **数据回滚**：SQLite 数据是否随 generation 快照（影响预览回滚与分享语义）。
7. **多实例**：限流/票据/预览状态是否需要共享存储（Redis）。

### 8.2 残余风险

- 即使容器化，内核/运行时漏洞仍可能逃逸；需要镜像最小化、定期补丁、seccomp/AppArmor 与安全测试。
- 生成应用本身可能包含 XSS/SSRF/恶意依赖；预览网关与网络策略只能降低影响，不能保证应用安全。
- TLS 解决链路窃听，但不能防止终端被入侵、恶意扩展、账号共享。
- 审计与告警依赖日志完整性；需防止日志被租户写入或篡改。
- 预览分享本质上是“把生成应用暴露给持有链接的人”；TTL/撤销/水印需要产品明确。

---

## 9. 附录

### 9.1 关键代码位置

| 主题 | 位置 |
|---|---|
| 本地沙箱驱动 | `internal/control/sandbox/local.go` |
| Agent 文件路径 | `agent-runtime/internal/infrastructure/adapter/tools/common.go`、`file_ops.go` |
| Bash 与 env | `agent-runtime/internal/infrastructure/adapter/tools/bash.go` |
| 审批策略 | `agent-runtime/internal/infrastructure/adapter/policy/policy.go` |
| 登录/注册/限流 | `internal/control/auth/auth.go` |
| API 安全头/CSRF | `internal/control/api/router.go` |
| 预览网关 | `internal/control/preview/handler.go` |
| 预览票据 | `internal/control/preview/tickets.go` |
| 主密钥 | `internal/kit/secret/secret.go` |
| systemd | `dist/tenon-server-linux-amd64/install/tenon-control.service` |
| nginx | `dist/tenon-server-linux-amd64/install/nginx-tenon.conf` |

### 9.2 生产观测记录（脱敏）

```text
GET /tenon/
  Cache-Control: no-store, must-revalidate
  （无 X-Frame-Options / CSP / nosniff / Referrer-Policy）

GET /api/me (401)
  X-Frame-Options: DENY
  X-Content-Type-Options: nosniff
  Referrer-Policy: no-referrer

POST /api/login (204)
  Set-Cookie: <session>=<redacted>; Path=/; Max-Age=604799;
              HttpOnly; SameSite=Lax
  （无 Secure）
```

### 9.3 参考标准

- OWASP ASVS 4.0：认证、会话、访问控制、输入验证、通信安全。
- OWASP Top 10 2021：A01 访问控制失效、A02 加密失败、A03 注入、A05 安全配置错误、A07 认证失败。
- CWE：CWE-250（不必要权限）、CWE-319（明文传输）、CWE-22（路径遍历）、CWE-307（认证尝试限制不足）、CWE-770（资源耗尽）、CWE-1021（点击劫持）、CWE-598（URL 敏感信息）、CWE-522（凭据保护不足）。
- NIST SP 800-63B：口令与认证器建议。
- SLSA / CycloneDX：构建来源与 SBOM。

---

## 10. 总结

外部报告的判断应被采纳：**当前版本不适合对不可信公网用户开放**。最优先的不是增加更多功能，而是：

1. 把 Agent 执行面从宿主机控制面中真正隔离出去；
2. 全站 TLS，关闭公网预览端口；
3. 在隔离完成前收敛注册与访问；
4. 以文件 jail、限流、预览网关加固、最小权限、审计和供应链作为纵深防御。

文件路径修复、隐藏 pprof、systemd 加固、CSP 都是必要项，但只能作为隔离与 TLS 之后的第二层防线，不能替代它们。
