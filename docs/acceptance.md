# P0 验收清单

本清单按三份输入规格合并。勾选项均有自动化测试或真实 Chrome 证据；未勾选项表示证据不足或验收前提未满足，不以“代码已存在”代替验证。

> 命名说明：系统已命名为 Tenon（原型工坊），Runtime 为 `agent-runtime`；本基线为命名后的重新复验结果。

## 本次本机验证基线

2026-10-24 已实际执行：根 module 与 `agent-runtime/` 的全量 `go test ./...` 和 `go test -race ./...`；前端 22 个测试文件、58 项测试及生产构建；`python3 scripts/smoke_local.py`；Chrome `155.0.8059.85` 全量 `npm run test:e2e`。Chrome 结果包含 Tenon 页面标题、登录与侧栏品牌断言，以及 CSP 内联脚本阻断、真实工具退出码 17、预览构建失败、六次写入恢复、隔离休眠与自动唤醒。运行命令见 `README.md`，结果保存在未跟踪的 `.local/last-smoke.json` 与 `.local/e2e/result.json`。

2026-10-25 MySQL 存储验证：在 82.156.224.101（Ubuntu 24.04 / MySQL 8.0.46）以管理员账号建库建表，创建仅有 `SELECT/INSERT/UPDATE` 的 `tenon_app` 账号；应用通过 `CONTROL_DB_DRIVER=mysql` + `CONTROL_MYSQL_*` 环境变量连接（本机经 SSH 隧道）。实测登录、创建对话、提交 Run、事件/消息/用量落库、Run 终态与 commit、登出后 401 均正常；账号执行 `DELETE`/`CREATE` 返回 1142 拒绝，`SELECT ... FOR UPDATE` 可用。根 module `go test ./...`、前端 24 文件/67 测试与生产构建通过。

2026-10-25 模型配置动态化验证：修改 `deploy/models.local.yaml` 后未重启即通过 `/api/models` 看到新增/移除；新增需要 `DEEPSEEK_API_KEY` 的模型时服务正常启动，`/api/models` 返回 `ready=false`/`missing_env`，发起对话返回 409 `LLM_NOT_CONFIGURED` 并携带变量名；前端显示“未配置”标记、输入区告警、toast 与失败气泡。沙箱启动快照缺少新模型时，切到新模型仍可完成 Run（运行时回退到控制层代理）。根 module 与 agent-runtime 的 `go test ./...`、前端 24 文件/67 测试与生产构建通过。

2026-10-25 Linux 部署验收：lang=linux/amd64 静态编译 control/wsd/agent-runtime，部署包只含二进制、前端产物与运行期资产（无后端源码）。服务器 82.156.224.101 安装 MySQL 8.0.46、Go 1.27.3、Node 22.23.4、nginx 1.24 后部署到 `/data/server`，systemd 服务与 nginx 均 enabled；公网完成登录、创建对话、Run 完成与事件/消息/用量落库，预览构建 ready 且本机可访问页面与静态资源。已知限制：云安全组未放行 8081，且 80 端口对未备案域名拦截，预览需放行 8081 或使用已备案域名。

默认模型是可复现的本地确定性 demo，不是外部真实模型；真实 agent-runtime 工具、进程、SQLite、文件修改和预览构建均实际执行。S6 的 prompt tokens 压缩由阈值与投影单测覆盖，不把 local-demo 结果当作真实上游模型质量或故障验收。

2026-10-25 账号入口调整（游客浏览 + 创建时登录/注册）的验证：根 module 全量 `go test ./...` 通过；前端 24 个测试文件、67 项测试与生产构建通过；Playwright 脚本在真实 Chrome 中实测游客首页不跳登录、不发受保护请求，点击新建对话弹出登录/注册，注册返回 201 后自动登录并继续创建对话，登录后刷新仍保持登录态。全量 `npm run test:e2e` 本轮未跑通：卡在既有的 `verifyFailureAndRetry`（连续多轮 Run 后前端 composer 仍禁用，服务端 Run 已 `run_finished`；同一对话重新加载后状态正确），该路径属于 chat/SSE 状态收敛，不在本次账号入口改动范围内，仍需单独排查。

## Runtime

- [x] S1：下一 Run 使用新模型；活跃 Run 切模型返回 409。
- [x] S2：工具事件带 `is_error`；终态 marker 带 run_id、迭代、token、模型和错误码。
- [x] S3：bash/MCP 环境白名单不泄露三类令牌。
- [x] S4：preview 工具返回状态、探活和日志；Run 内调用不死锁。
- [x] S5：所有 text_delta 与最终 assistant 文本一致。
- [x] S6：default_mode、每回合预算和按 prompt tokens 压缩生效。
- [x] S7：100 个并发冷加载得到同一 Runtime；race 测试通过。
- [x] S8：prepare/start 幂等；不同 hash 409；取消确认停止且无旧写入；重启恢复。
- [x] S9：1000 条事件慢消费者可补齐；并行工具顺序一致。
- [x] S10：120 MB 输出有界；5 MB 单行后无残留进程。

## wsd 与工作区

- [x] 建会话 → prepare → start → 事件 → 停止确认 → commit → preview ready。
- [x] 文件接口拒绝绝对路径、`..`、NUL 和越界 symlink；二进制与 1 MB 限制正确。
- [x] 生成应用进程环境不含 LLM_PROXY_TOKEN、MAGENTS_AUTH_TOKEN、WSD_TOKEN。
- [x] 无 Bearer 的 wsd/agent-runtime 管理请求返回 401。
- [x] 孤儿进程可清理；取消 Run 不误杀预览。
- [x] 未知进程占用预览端口时返回 PORT_CONFLICT，且不杀该进程。
- [x] Git commit 按 run_id 幂等；旧 generation 不覆盖新预览。

## 控制层

- [x] 同 client_request_id 和同内容返回同一 Run；不同内容返回 IDEMPOTENCY_CONFLICT。
- [x] 202 响应未知时沿原 ID 查询，不重复执行。
- [x] 事务回滚不广播；持久事件提交后才可见。
- [x] 最后一条源事件丢失时高水位/终态补扫仍能完成。
- [x] control 在 running/finalizing 重启后按 run_id 恢复，不重复执行。
- [x] 非 running Run 的沙箱令牌调用 LLM 返回 403。
- [x] 上游 429/5xx 和 Retry-After 原样透传，并记录用量。
- [x] 用户 A 访问用户 B 的对话、消息、文件、事件和预览全部 404。
- [x] Run、在线工作区、构建三类配额及按用户公平轮转让超额请求排队或 429。
- [x] CLI 用户新增/停用/改密、用量与 status 可用。

## Chrome 产品验收

- [x] 登录、错误/停用账号、锁定、登出、401 安全回跳。
- [x] 游客可直接浏览首页与示例，不请求受保护接口；新建对话时才弹出登录/注册，注册成功（201，Argon2id 落库、按来源地址限流）即自动登录并继续创建。
- [x] 对话新建、切换、行内重命名、删除、自动标题、状态和分页。
- [x] 新建对话不立即创建工作区；首轮 Run 才初始化（模板复制、git commit、agent 配置、wsd/agent-runtime），已有工作区在沙箱停止后按需恢复；重启中断留下的 creating 行会被回收。
- [x] 发送、乐观回显、网络重试复用请求号、取消中、终态后重新执行。
- [x] 排队/执行/取消/收尾状态准确；活跃时输入禁用；前端不渲染模型选择，创建对话与发送消息均不携带模型。
- [x] 工具调用/结果按 call_id 配对；失败标红；截断提示；bash 终端样式。
- [x] 刷新后聊天历史一致；断网 30 秒恢复后无重复、无缺失。
- [x] 流式 delta 不重复，完整文本到达后替换缓冲。
- [x] 文件树懒加载、排序、隐藏项、只读 Monaco、默认文件和改动高亮。
- [x] 文件工具卡片跳转代码；变更后刷新已展开目录和已打开文件。
- [x] Run 完成展示 commit、改动数、耗时和 token，并自动刷新已确认预览。
- [x] 预览启动/失败/旧版本、日志、重启、新窗口与休眠唤醒可验收。
- [ ] 第三方 Cookie 禁用时新窗口预览仍可用。（新窗口与匿名预览 401 已验证；本机 Chrome 未按测试参数阻断跨站 iframe Cookie，禁用前提未成立，见 `not_covered`。）
- [x] 推送和发布按钮置灰并提示“即将开放”；对应 API 返回 501。
- [x] Markdown 不执行原始 HTML；CSP、CSRF、iframe sandbox 和预览授权有效。

## 部署与故障矩阵（不能用 local 模式替代）

- [ ] Docker 非 root、cap_drop、no-new-privileges、资源限制和无宿主挂载。
- [ ] 容器间、宿主数据库、元数据和非白名单出网被拒绝。
- [ ] MySQL 事务/死锁重试、控制层恢复和配额重建通过。
- [ ] OOM、磁盘满、容器 IP 变化、端口冲突、SSE 终态丢失均能收敛。（端口冲突和 SSE 缺口仅有局部单测，不能替代部署环境故障矩阵。）
- [ ] 10 用户 × 2 对话候选负载下记录实测容量、P95/P99 和限额。
