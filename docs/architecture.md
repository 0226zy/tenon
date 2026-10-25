# Tenon 原型工坊：架构与实施基线

## 项目决策

- 仓库名：`tenon`
- 根 Go module：`github.com/0226zy/tenon`
- Runtime：仓库内 `agent-runtime/` 独立模块（`github.com/0226zy/tenon/agent-runtime`），保留 ReAct、工具、事件日志和技能体系，并完成 S1–S10 改造。
- 控制层：`cmd/control`，按 C0–C16 分包；wsd：`cmd/wsd` 与 `internal/wsd`。
- Tenon Web：Vue 3 + TypeScript + Pinia + Monaco；生成应用模板：React + Go + SQLite。
- 共享跨进程 DTO 只放在 `pkg/contract`，前端 `web/src/types/contract.ts` 与其对齐。

## 主链路

浏览器 → control → wsd → agent-runtime；agent-runtime 只通过 control 的 LLM 代理访问模型。

消息接受后在同一事务写入用户消息、queued Run 和 `run_accepted`，提交后才广播。Driver 依次推进 `queued → dispatching → running → finalizing → finished/failed/cancelled`。只有 Runtime 确认停止且 wsd 清理完成，才能进入收尾；checkpoint、终态事件、助手消息和活跃位释放在同一事务提交。

预览绑定单调递增的 generation 和已确认 commit。失败或取消的 Run 不自动替换旧预览。文件、事件、Run 和预览均执行 owner 校验，越权统一返回 404。

## 事件一致性

三个序号域独立：Runtime 源 seq、控制层 UI seq、无 seq 的临时事件。源事件至少一次接收，以 `(runtime_epoch, agent_session_id, rt_seq)` 去重；游标和事件同事务推进。UI 持久事件先提交后通知，通知不作为权威数据。SSE 重连按 UI seq 补发；delta、progress 和文件变更提示不持久化。

## Runtime 复用与修复

保留端口化架构、ReAct 循环、工具结果回注、上下文压缩、JSONL 日志和技能加载。新增显式 Run prepare/start/query/cancel、run_id 幂等、generation fencing、结构化结果和模型切换。

不直接复用旧 `project.Orchestrator`、内嵌 Web UI、deploy 和 MCP。旧 Orchestrator 会把 fork 继承的 `turn_done` 误当成本轮结束；新实现必须以 run_id 和持久 Run 状态为准。旧 fork 不复制模型、Bash 取消不能保证停止、工作区白名单不是安全沙箱，均不能作为平台可靠性或隔离依据。

## 本机验收与生产部署

生产目标保持 MySQL 8、单机 Docker、Caddy、固定容器网络与每对话独立卷。控制层库表存储由 `database.driver`（或 `CONTROL_DB_DRIVER`）显式选择：`mysql` 时连接信息只来自环境变量，应用账号仅授予 `SELECT/INSERT/UPDATE`，建表与迁移由管理员账号执行一次并登记 `t_schema_migration`；会话注销通过提前 `Fexpire_time` 完成，不依赖 `DELETE`。模型目录是运行时资源：控制层按文件 mtime/大小热加载，允许不重启增删模型；凭证在每次请求时解析，缺失只影响对话请求（`LLM_NOT_CONFIGURED`），不影响进程启动。显式 `local` 驱动只描述“本机沙箱 + 演示模型”，可采用本地 SQLite 或远端 MySQL，同样使用每对话独立目录和受监督的 wsd/agent-runtime/预览进程，仍走相同 HTTP 契约。

`local` 不是安全容器，不用于不可信多租户，也不能据此宣称完成容器网络、Docker 资源限制或 MySQL 故障矩阵验收。配置和 UI 必须清楚标识当前模式。

## 范围

P0 实现游客浏览、登录、自助注册、对话、模型、Run、事件、工具卡片、代码、预览、配额、CLI 管理、计量和恢复。认证入口按“浏览不设门槛、动作需要身份”分流：`/` 与欢迎页公开，`/c/:cid` 与创建对话要求会话；未登录时创建动作弹出登录 / 注册对话框，成功后自动续做。GitHub 推送、一键发布只保留置灰入口和 501 路由；审批、fork、附件、在线编辑、版本回滚和 SSO 不在 P0 完整实现范围内。
