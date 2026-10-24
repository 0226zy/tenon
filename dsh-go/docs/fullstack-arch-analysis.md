# 全栈 Coding Agent 四层架构 vs dsh-go 现状：借鉴与整合分析

> 分析对象：截图《1. 核心架构设计》提出的生产级全栈 Coding Agent 四层闭环
> （规划层 / 编码与执行层 / 自验证层 / 部署与运维层）。
> 本文只做分析，不构成代码改动；涉及当前实现的事实均以 dsh-go 当前代码为准。
>
> 现有内核的设计优缺点与 P0/P1 加固建议，见
> [design-implementation-review.md](./design-implementation-review.md)。

## 一、图片中的架构设计解读

截图的核心论点是：**生产级全栈 Coding Agent 不能仅靠单一大模型，而要组合
规划、执行、验证、运维四个层，形成闭环**。

| 层 | 职责 | 关键机制 |
|---|---|---|
| 规划层（Planner） | 需求拆解（PRD）、技术栈选型（如 React + FastAPI + PostgreSQL）、架构设计 | 多智能体协作：产品经理 Agent、架构师 Agent 等角色分工 |
| 编码与执行层（Coder & Tools） | 生成代码、操作文件系统、执行终端命令 | 通过 MCP（Model Context Protocol）标准化工具调用，安全读写代码仓库 |
| 自验证层（Self-Correction/Test） | 静态检查（Linter）、单元测试、端到端测试（E2E） | Agent 运行测试用例，根据报错日志自主修复（Fix Loop） |
| 部署与运维层（Deploy/CI-CD） | 编译产物一键推送上线，返回可访问 URL | Serverless、Docker、平台化托管（Supabase、AWS 流水线） |

这一设计的三个架构思想值得注意：

1. **闭环而非单点**：四层是 Plan → Code → Verify → Deploy 的运维闭环，
   验证层的失败回流到编码层（Fix Loop），规划层的产出约束编码层。
2. **角色化多智能体**：规划层不是一个"更聪明的提示词"，而是职责分离的
   多个 Agent（产品、架构师），各看各的上下文。
3. **工具协议标准化**：编码层明确提出用 MCP 统一工具接入，
   把"工具生态"从进程内实现解耦为可插拔的协议服务。

## 二、当前 dsh-go 的设计与实现盘点

### 2.1 分层骨架

项目遵循仓库根 `ARCHITECTURE.md` 的 go-ddd 规范（依赖指向领域，领域不依赖任何人），
同时对齐 DeepSeek Harness（dsh）的八层可替换设计：

- `domain`（零外部依赖）：entity（中立事件/LLM 模型信息）、port（全部下游能力端口）、
  plugin（注册表内核）、derr（哨兵错误）、port/contracttest（仓储契约测试套件）
- `application`：agent.Loop（ReAct 循环）、session.Manager（resume/fork/replay/search）
- `infrastructure/adapter`：openai（LLM）、llminfo（模型信息文件仓储）、
  storejsonl（append-only 会话日志）、tools（六件核心工具 + task 委派）、
  policy（命令语义风险解析 + 三档审批）、statusbar
- `interfaces`：httpapi（REST + SSE）、webui（内嵌单页控制台）
- `shared/deps`：组合根，唯一装配点

### 2.2 端口清单（全部替换点都在 domain/port）

`LLMPort`（模型对话）、`Tool`/`ToolRegistryPort`（工具）、
`SessionStorePort`（append-only 日志）、`Hook`（PreToolUse/PostToolUse 挂载点）、
`StatusBarPort`、`LLMInfoRepo` + `LLMResolver`（模型信息资源，DDD 仓储模式）、
`SessionRuntime`（循环看到的会话运行时）。

### 2.3 已实现的关键机制

- **ReAct 循环**：唯一正常终止条件是模型不再调用工具；轮数/预算/活性/熔断为保护性终止。
- **错误即输入**：工具失败、幻觉调用、参数畸形一律以普通工具结果回注，不终止会话。
- **子代理委派（task 工具）**：批量扫描/长日志分析等任务派给独立上下文的子 Agent，
  中间轨迹留在子代理（JSONL spill 落盘可审计），只有结论回主上下文；
  子代理高风险操作升级给主会话审批通道；深度计数器 + 工具视图过滤禁嵌套。
- **Hook 验证挂载点**：write_file 写入即触发语法检查反馈（PostToolUse），
  危险命令在 PreToolUse 拦截。
- **分级恢复 + 熔断**：可重试错误指数退避 + 抖动、尊重 Retry-After、重复调用指纹检测。
- **执行边界**：三档审批（suggest/auto-edit/full-auto）+ 命令语义级风险解析。
- **模型信息资源层**：`LLMInfo` 实体 + `LLMInfoRepo` 端口 + `llminfo` 文件实现 +
  契约测试套件；会话按名选模型，fork 继承、子代理沿用；用户可经 API 自定义模型。
- **中立轨迹**：append-only JSONL 事件日志，支持 resume/fork（血缘）/replay/search。

### 2.4 明确划出的边界（README 已知边界）

- 沙盒为进程级（独立进程组 + 超时 + 审批），未到 OS 内核级隔离（Seatbelt/Landlock）。
- 命令语义解析是启发式，不是完整 shell AST。
- LLM 适配器非流式，SSE 推结构化事件而非 token 流。

## 三、四层架构与当前实现的映射对照

| 图片中的层 | dsh-go 现状 | 覆盖度 |
|---|---|---|
| 规划层（Planner / 多智能体） | 无独立规划层；最接近的是 task 子代理委派（隔离执行 + 结论回传），以及"不同会话可选用不同模型"的模型资源层 | **缺失**，但地基已备 |
| 编码与执行层（Coder & Tools） | 六件核心工具 + 持久 bash 会话 + task 委派，进程内注册表（`port.Tool`）；无 MCP | **已覆盖**，MCP 未接入 |
| 自验证层（Self-Correction/Test） | Hook 挂载点 + write_file 即时语法检查反馈 + 错误即输入回注；无测试运行器、无系统化 Fix Loop | **有萌芽**，未闭环 |
| 部署与运维层（Deploy/CI-CD） | 无 | **缺失** |

值得强调的一点：截图四层中的"编码与执行层"正是 dsh-go 目前投入最深、
机制最完整的部分（恢复、熔断、审批、轨迹、隔离），这说明项目的既有路线
与四层架构并不冲突——四层架构是在此坚实内核之上，**向外补三个方向的扩展**。

## 四、可借鉴与整合的架构思想（逐层分析）

### 4.1 规划层：不必新建系统，是现有三件地基的组合

截图的多智能体规划（产品经理 Agent / 架构师 Agent）看似是新系统，
拆解后其实是 dsh-go 已有机制的组合应用：

- **角色化 = 系统提示词 + 工具视图**。现有 SOUL 层（system_prompt.md）与
  工具注册表已支持"不同会话不同提示词"；task 工具已经能派生独立上下文的子代理。
  一个"架构师 Agent"≈ 一个携带架构师提示词、只读工具视图（无写权限）的子代理。
- **多模型分工 = 模型信息资源层的既有能力**。`LLMResolver` 已支持按会话解析模型：
  规划会话用强推理模型、编码会话用快模型，不需要新机制，只需要编排层按角色选模型。
- **需求拆解的产出 = 中立轨迹里的结构化消息**。PRD/技术选型可以落进 append-only
  日志成为可 fork 的血缘节点——编码会话直接从规划会话 fork，血缘天然可追溯。

整合建议的形态：在 application 层新增一个**编排用例**（如 `project.Orchestrator`），
顺序驱动"规划会话 → 编码会话（fork）"，而不是引入多智能体框架。
这符合项目既有纪律（抄设计不抄依赖）。

> 状态（2026-10-18）：**已落地**。`application/project.Orchestrator` + `POST /v1/projects/plan`：
> 规划会话以架构师角色提示词启动（可选用强模型），完成后编码会话从其末尾 fork
> （血缘 parent_id 可追溯），经新增的 `Manager.SetModel` 切换编码模型。
> v1 取舍：角色化是首条消息提示词注入，尚无按会话的只读工具视图过滤；
> 编排状态在内存（会话与血缘本体持久）。

### 4.2 MCP：与端口模式天然兼容，是最值得借鉴的标准化思想

截图提出用 MCP 标准化工具调用。当前工具层是进程内接口（`port.Tool`），
接入新工具要写 Go 代码。MCP 的价值在于**工具生态协议化**：文件系统、Git、
数据库、浏览器等能力以独立 MCP server 进程提供，Agent 经标准协议调用。

与现有架构的兼容性分析：

- `ToolRegistryPort` 是只读视图接口——新增一个 `infrastructure/adapter/mcp`
  适配器，启动时连接配置的 MCP server，把其工具列表翻译为 `entity.ToolDefinition`
  注册进注册表，调用时转发 JSON-RPC。**application 层与 domain 层零改动**，
  这正是端口模式预留的替换点。
- MCP 工具的执行语义可以完整复用现有管道：`ToolContext.Approve`（审批）、
  `Spill`（超长输出落盘）、Hook（Pre/Post）对 MCP 工具同样生效——
  这是相对"裸接 MCP"的一个差异化优势：**安全边界不绕过**。
- 风险点：MCP server 是外部进程，错误语义（超时、崩溃、协议错误）必须
  按"错误即输入"纪律回注为普通工具结果；工具名冲突需要命名空间策略
  （如 `mcp__<server>__<tool>`）。

> 状态（2026-10-18）：**已落地**。`infrastructure/adapter/mcp`：自实现 stdio
> JSON-RPC 2.0 客户端（不引外部 SDK），`initialize`/`tools/list`/`tools/call`；
> 远端工具以 `mcp__<server>__<tool>` 命名注册，schema 直接透传；
> dsh.yaml `mcp.servers` 配置决定启用；`read_only` server 的工具经
> `policy.AddReadOnlyTools` 免审批；超时即杀进程并回注结构化错误。
> v1 边界：stdio 传输 + 串行 RPC。

### 4.3 自验证层：把已有萌芽接成闭环，成本最低、收益最直接

截图的 Fix Loop（运行测试 → 报错日志 → 自主修复）在 dsh-go 里
**不需要新循环**——它就是 ReAct 循环本身，缺的只是两件工具：

1. `run_tests` 工具：在会话工作区执行测试命令（可复用 bash 持久会话），
   输出超长时经 `Spill` 落盘、摘要回注。
2. `lint` 工具或更强的 PostToolUse 检查：现有 write_file 语法检查已是雏形，
   扩展到语言级 linter 只是策略增强。

报错日志经"错误即输入"回注后，模型的下一轮自然进入修复路径——
Fix Loop 是 ReAct 循环 + 测试工具 + 错误回注三者组合后的**涌现行为**，
不需要专门的循环驱动器。这是四层架构中最先应该整合的一层。

> 状态（2026-10-18）：**已落地**。`run_tests` 工具已实现并注册
> （`infrastructure/adapter/tools/run_tests.go`）：缺省按工作区特征自动探测测试命令，
> 复用 bash 持久会话执行，失败输出错误回注；自定义命令与 bash 同等过语义风险解析
> （policy `assess` 新增 run_tests 分支）。

### 4.4 部署与运维层：作为"高审批档工具"接入，而非新子系统

部署（docker build、推送、触发流水线）本质上是**高风险工具调用**，
现有执行边界机制恰好是为这类操作设计的：

- 落地形态：新增 `deploy` 工具 + `infrastructure/adapter/deploy`（docker/目标平台适配），
  命令模板固化在适配器内，模型只能填参数（镜像名、环境），不能直接拼命令——
  比 bash 直跑部署命令的风险面小得多。
- 审批档位：部署应强制走 `Approve` 通道且不可被 full-auto 豁免，
  这需要在 policy 适配器增加"按工具强制审批"的规则（当前按命令风险分级）。
- 沙盒边界：部署工具触达外部系统，与 README 已声明的"进程级沙盒"边界叠加后，
  多租户部署前容器隔离的优先级会进一步提高。

> 状态（2026-10-18）：**已落地**。`deploy` 工具：命令模板固化在
> dsh.yaml `deploy.targets`（占位符 `{{param}}`），参数值经 shell 单引号转义后替换
> （注入不可逃逸）；policy 新增**强制审批名单**（`AddForceApproveTools`），
> deploy 在任何模式（含 full-auto）下都需人工裁决。空配置 = 不注册工具。

### 4.5 闭环编排：四层合起来是 application 层的一个新用例

四层闭环（Plan → Code → Verify → Deploy）在现有架构中的落位
是 application 层的新编排服务：它按阶段创建/fork 会话、为每阶段选模型与工具视图、
把验证层的失败作为编码会话的下一轮输入。**domain 层无需新增端口**，
因为所需能力（会话管理、模型解析、工具执行、审批）全部已有端口覆盖。
这印证了现有端口设计的完备性。

## 五、整合时应守住的既有纪律

1. **抄设计不抄依赖**：多智能体协作自己编排，不引入外部 Agent 框架；
   MCP 只引协议客户端，不引生态全家桶。
2. **端口在 domain，实现在 infrastructure**：MCP、deploy、run_tests
   都以适配器身份进入，组合根注入，上层零感知。
3. **错误即输入**：MCP 协议错误、部署失败、测试失败一律回注为普通工具结果。
4. **安全边界不旁路**：任何新工具（含 MCP 转发）都经过 Hook 与审批通道。
5. **可替换性用契约测试兜底**：若 deploy/MCP 引入新的资源仓储，
   照 `contracttest` 模式先立契约套件。

## 六、建议的整合顺序（按性价比）

| 阶段 | 内容 | 理由 |
|---|---|---|
| 1 ✅ | 自验证层：`run_tests` 工具 + lint 反馈增强 | 纯增量、复用现有循环、立刻提升编码质量（run_tests 已落地） |
| 2 ✅ | MCP 客户端适配器（只读类工具先行） | 打开工具生态，端口模式零上层改动（stdio 客户端 + 只读名单已落地） |
| 3 ✅ | 规划层编排用例（角色会话 + fork 血缘 + 分角色选模型） | 复用 task/fork/LLMResolver 三地基（Orchestrator + /v1/projects/plan 已落地） |
| 4 ✅ | 部署工具（强制审批档）+ 容器隔离评估 | deploy 工具 + 强制审批名单已落地；容器隔离评估留待多租户部署前 |

## 七、结论

截图的四层架构与 dsh-go 现有设计**同构而不冲突**：编码与执行层已被
dsh-go 的 ReAct 内核深度覆盖；自验证层是现有 Hook + 错误回注机制的自然延伸；
规划层与部署层是缺失维度，但所需地基（子代理隔离、模型资源层、fork 血缘、
审批通道、端口模式）都已就位。整合的关键不是引入新框架，而是把四层
理解为"现有内核之上的三个增量适配器 + 一个编排用例"。
