# agent-runtime — 参考 DeepSeek Harness 的 Go 版 Coding Agent（服务端形态）

参考 [DeepSeek Harness（dsh）](https://github.com/deepseek-ai/deepseek-harness) 的架构理念，
按《Agent 构建设计指引》（本仓库 docs 基线文档）用 Go 重新实现的 Coding Agent。
与 dsh 的本地 CLI/Web 混合形态不同，本项目是**纯服务端形态**：
模型、会话、工具、审批全部经 HTTP API 暴露，事件流经 SSE 实时推送，
可部署在 Linux / macOS 服务器上，直接维护服务器上的编码项目。

## 与 dsh 的设计对应

| dsh（Cordis 插件树） | agent-runtime（注册表 + 组合根） |
|---|---|
| Inference 层（ctx.llm 模型适配器） | `domain/port.LLMPort` + `adapter/openai` 插件，按会话经 `LLMResolver` 解析 |
| Tools 层（工具注册表/策略/执行管道） | `domain/port.Tool` + `adapter/tools`（核心工具集 + run_tests 自验证 + task 委派） |
| State 层（append-only SessionEvent 日志，resume/fork/replay/search） | `domain/port.SessionStorePort` + `adapter/storejsonl` |
| Control 层（Agent 循环驱动器） | `application/agent.Loop` |
| Execution 层（shell/文件系统） | `adapter/tools`（持久 bash 会话 + 文件/搜索工具） |
| Composition 层（profiles/bundles/patches） | `conf/agent-runtime.yaml` + `conf/models.yaml` + `shared/deps` 组合根 |
| Experience 层（Web UI） | `interfaces/webui`（内嵌单页应用）+ SSE |
| Framework 层（Cordis DI 微内核） | `domain/plugin.Registry`（一切皆插件，配置决定启用） |

借鉴纪律（指引 §8）：**抄设计，不抄依赖**——移植的是 append-only 事件日志、
插件化分层、会话血缘这些机制，不引入 Cordis/Node 生态。

## LLM 模型信息层（DDD 仓储模式）

可选模型是一种**资源**，由 `domain/port.LLMInfoRepo` 仓储接口管理：
当前实现是本地文件（`infrastructure/adapter/llminfo`，`models.yaml`），
后续切换 DB 仓储时替换实现即可，application 与接口层零改动。

- 预置 **deepseek** 与 **kimi** 两条内置配置；密钥优先走 `api_key_env` 环境变量引用，
  字面 `api_key` 仅适合本地开发（文件权限 0600）。
- 会话按名选模型：`POST /v1/sessions` 带 `model` 字段（缺省取 `default: true` 的条目）；
  回合开始时解析一次并缓存适配器实例；fork 继承父会话的模型；子代理沿用父会话模型。
- 用户自定义模型：`POST /v1/models` upsert、`DELETE /v1/models/{name}` 删除，
  持久化在同一资源文件里，重启不丢。
- API 视图永不暴露密钥本体，只返回 `api_key_source`（env:XXX / literal / none）。
- 可替换性由契约测试保证：`domain/port/contracttest.RunLLMInfoRepoContract` 是统一行为套件
  （List/Get/Default/Save/Delete 七组用例），未来 DB 实现在自己的测试中跑一遍即验证行为一致。

## 工程技能库（agentskills.io 兼容）

`skills/` 是随仓库分发的只读知识资产：每个子目录一份 SKILL.md（YAML frontmatter
声明 name/description + 正文）加可选 scripts/ 验证脚本。启动期由
`infrastructure/adapter/skill` 扫描（`skills_dir` 配置，目录不存在/为空 = 不启用）：

- **目录清单**（name+description）并入系统提示词静态前缀，模型据此判断触发时机；
- **正文按需加载**：`use_skill` 工具（只读/免审批/并发安全）取回完整内容，
  超长走 spill——progressive disclosure，不把全部知识灌进上下文；
- skill 引用的验证脚本（`scripts/check_*.py`、`smoke_test.py`）由模型用 bash 直接运行，
  构成「做对没有」的机器判定层。

预置 9 个技能：**arch-design**（架构设计与代码设计总引导：需求收敛 → 栈锁定表决策 →
目录宪法 → 前后端代码骨架纪律 → 验证门禁，按阶段接力下列专项）+ product-scope /
fullstack-architecture / frontend-react / frontend-vue / backend-patterns /
security-baseline / deploy-ship / debug-playbook 八个专项
（蒸馏自 6 份调研简报约 150 条带溯源的工程共识，React/Vue 双前端路线 + Go 后端）。

## 核心机制（对齐设计指引）

- **ReAct 循环**（FR-1）：唯一正常终止条件是模型不再调用工具；轮数/预算/活性/熔断均为保护性终止。
- **中立轨迹**（§4.4）：四角色消息即中立格式，事件 append-only 落盘
  `data/sessions/<id>/events.jsonl`，支持 resume / fork（血缘 header + fork marker）/ replay / search。
- **最小核心工具集**（§2.3.1）：bash（持久会话，cwd/环境变量跨调用保持）、read_file（行号前缀）、
  write_file（写入即触发语法检查反馈）、edit_file（old_string→new_string，不唯一时报候选行号）、
  glob（`**` 跨层）、grep（`文件:行号:内容`）。
- **自验证（Fix Loop）**：`run_tests` 在工作区运行测试——缺省按工作区特征自动探测命令
  （go.mod→`go test ./...`、Cargo.toml、package.json 的 test 脚本、pytest 工程、Makefile 的 test 目标），
  失败输出以普通工具错误回注（错误即输入），修复循环是 ReAct 循环的涌现行为，无需专门驱动器；
  复用 bash 持久会话执行（超时/截断/spill 继承），自定义命令与 bash 同等过命令语义风险解析。
- **MCP 工具生态**：`adapter/mcp` 是 MCP（Model Context Protocol）stdio 客户端（自实现 JSON-RPC 2.0，
  不引外部 SDK）；agent-runtime.yaml 配置 server 子进程，启动期握手 + `tools/list` 后把远端工具以
  `mcp__<server>__<tool>` 命名注册进注册表——domain 与 application 零改动；审批/spill/Hook 管道
  对 MCP 工具同样生效（安全边界不旁路），声明 `read_only` 的 server 工具免审批；
  协议错误/超时/server 崩溃一律回注为普通工具错误。
- **部署与运维层**：`deploy` 工具把部署收敛为「选目标 + 填参数」——命令模板固化在
  agent-runtime.yaml（`deploy.targets`，占位符 `{{param}}`），参数值经 shell 单引号转义后才替换，
  模型无法注入命令片段；deploy 注册为**强制审批**，任何模式（含 full-auto）都需人工裁决。
- **规划层编排**：`application/project.Orchestrator` 把「需求 → 规划 → 编码」编排为两个血缘
  相连的会话——规划会话以架构师角色提示词启动（可选用强模型），完成后编码会话从其末尾
  **fork**（需求与规划结论完整继承、parent_id 可追溯）并可切换编码模型；
  `POST /v1/projects/plan` 异步发起，`GET /v1/projects/plans/{id}` 查询阶段
  （planning/planned/coding/done/*_failed）。不引入多智能体框架，全部复用既有端口与 Manager。
- **子代理隔离**（§2.2 隔离优于压缩）：`task` 工具把批量扫描、长日志分析等会产生大量中间观察的
  任务委派给独立上下文的子 Agent——中间轨迹留在子代理（JSONL 全文落盘 spill 供审计），
  只有结论回到主上下文；子代理的高风险操作升级给主会话审批通道裁决；
  深度计数器 + 工具视图过滤双重防护禁止嵌套委派。
- **上下文投影压缩**（评审 P1，与 spill / task 互补）：累计 token 达
  `agent.context_compress_at_percent`（占预算百分比，agent-runtime.yaml 缺省 70，0=禁用）后，
  投影到模型上下文的较早长工具输出/长文本被替换为「头部摘录 + spill 全文路径」
  （原文可用 read 工具取回，信息离场不丢失）；首条 user 消息与最近
  `agent.context_keep_recent` 条消息逐字保留，tool_call 配对结构不变。
  压缩只发生在投影层——append-only 事件日志永远完整，fork/replay/search 不受影响；
  首次激活时向轨迹注入一条说明 notice 并广播 progress marker。
- **错误即输入**（§4.3）：工具失败（含幻觉调用、参数畸形、超时）一律以普通工具结果回注，不终止会话。
- **分级恢复 + 熔断**（§4.2/§4.5）：可重试错误（429/5xx/超时/断连）指数退避+抖动、尊重 Retry-After；
  重复调用指纹检测无进展循环；连续失败熔断。
- **执行边界**（FR-7/§5.3）：三档审批（suggest / auto-edit / full-auto）；
  bash 命令做语义级风险解析（切段、展开 `$()`/反引号、识别 rm -rf / 强推 / 写系统目录 /
  管道执行远程脚本），高风险操作经 API 人工裁决，审批事件落盘可审计。
- **KV Cache 友好**（§2.2）：系统提示词 + 工具定义是稳定静态前缀；
  时间/cwd/git 状态以状态栏形式追加在上下文末尾，不改写前缀。
- **轨迹完整性**（§4.2）：上下文投影时自动补齐缺配对的 tool result。

## 快速开始

```bash
# 1. 配置模型密钥（conf/models.yaml 预置 deepseek / kimi，按 api_key_env 取密钥）
export DEEPSEEK_API_KEY=sk-...
export MOONSHOT_API_KEY=sk-...

# 2. 按需编辑 conf/agent-runtime.yaml（listen/auth_token/预算）与 conf/models.yaml（模型参数）

# 3. 构建并启动（在 agent-runtime 目录下）
go build -o bin/agent-runtime ./cmd/agent-runtime
./bin/agent-runtime -config conf/agent-runtime.yaml

# 4. 打开 Web UI
open http://127.0.0.1:3080/
```

服务端部署时请设置 `server.auth_token`（或环境变量 `MAGENTS_AUTH_TOKEN`），
API 需 `Authorization: Bearer <token>`（SSE 支持 `?token=` 查询参数）。

## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 健康检查 |
| GET | `/v1/plugins` | 插件清单（对齐 `dsh --dump-config` 的自省能力） |
| GET | `/v1/models` | 可选模型列表（密钥不暴露，仅返回来源形式） |
| POST | `/v1/models` | 新增/更新模型信息（用户自定义） |
| DELETE | `/v1/models/{name}` | 删除模型信息 |
| POST | `/v1/sessions` | 创建会话 `{cwd, mode, title, model}` |
| GET | `/v1/sessions` | 会话列表 |
| GET | `/v1/sessions/{id}` | 会话详情（含 running 状态） |
| GET | `/v1/sessions/{id}/events?from_seq=` | 事件回放（replay） |
| GET | `/v1/sessions/{id}/search?q=` | 日志检索 |
| POST | `/v1/sessions/{id}/fork` | 派生会话 `{at_seq}`（-1=当前末尾） |
| POST | `/v1/sessions/{id}/messages` | 发起回合 `{content}`（异步，202） |
| POST | `/v1/sessions/{id}/cancel` | 中断回合 |
| POST | `/v1/sessions/{id}/mode` | 切换审批模式 |
| POST | `/v1/projects/plan` | 规划层编排 `{cwd, requirement, planner_model?, coder_model?, mode?, auto_implement?}`（异步，202） |
| GET | `/v1/projects/plans/{id}` | 查询编排阶段（planning/planned/coding/done/*_failed） |
| POST | `/v1/sessions/{id}/approvals/{aid}` | 裁决审批 `{approved}` |
| POST | `/v1/tickets` | 签发 60s 一次性 stream ticket（供 EventSource 等无法带头的客户端） |
| GET | `/v1/sessions/{id}/stream?from_seq=` | SSE 实时事件流；支持 `from_seq` 与 `Last-Event-ID` 游标补发（先订阅后补发、按 seq 去重，无丢事件窗口），每条事件带 `id:`/`event:`/`data:` |
| GET | `/` | Web UI |

## 目录结构

```
agent-runtime/
├── cmd/agent-runtime/main.go            # 进程入口：只装配
├── conf/
│   ├── agent-runtime.yaml                # profile：预算/熔断/审批默认值/资源文件路径
│   └── models.yaml             # 可选模型资源（LLMInfoRepo 本地文件，预置 deepseek/kimi）
├── prompt/
│   └── system_prompt.md        # SOUL 层：身份、流程、忠诚度守则
├── skills/                     # 工程技能库（agentskills.io 兼容，预置 arch-design + 8 个全栈专项）
├── internal/
│   ├── domain/                 # entity（中立事件/LLM 模型信息）/ port（端口+LLMInfoRepo）/ plugin（注册表内核）/ derr
│   ├── application/
│   │   ├── agent/              # ReAct 循环（Control 层）+ task 子代理委派
│   │   ├── project/            # 规划层编排：规划会话 → fork 编码会话（血缘 + 分角色选模型）
│   │   └── session/            # 会话管理：resume/fork/replay/search、回合生命周期
│   ├── infrastructure/
│   │   ├── config/             # YAML 配置加载
│   │   └── adapter/
│   │       ├── openai/         # OpenAI 兼容 LLM 适配器（错误分类）
│   │       ├── llminfo/        # LLM 模型信息资源：本地文件仓储（可换 DB 实现）
│   │       ├── mcp/            # MCP stdio 客户端：外部工具生态接入（自实现 JSON-RPC）
│   │       ├── storejsonl/     # append-only JSONL 会话日志（State 层）
│   │       ├── tools/          # bash/read/write/edit/glob/grep + run_tests 自验证 + deploy 部署（Tools 层）
│   │       ├── policy/         # 命令语义风险解析 + 三档审批（PreToolUse 钩子）
│   │       ├── skill/          # 工程技能库加载器（扫描 skills_dir，目录清单/Catalog 注入）
│   │       └── statusbar/      # 环境状态栏注入
│   ├── interfaces/
│   │   ├── httpapi/            # REST + SSE
│   │   └── webui/              # 内嵌 Web 控制台（static/ 分文件：index.html + app.css + app.js，embed 单二进制交付）
│   └── shared/deps/            # 组合根
└── data/                       # 运行期生成：会话日志、spill 输出
```

## 测试

```bash
go test ./...
```

覆盖：ReAct 循环与工具结果回注、幻觉调用回注、活性检测、预算/轮数终止、
静默重试与不可重试快速失败、并行工具调用、子代理委派（结论回传/隔离性/禁嵌套/轨迹落盘）、
模型资源仓储（种子/List/Get/Default/upsert/删除/持久化/0600 权限 + LLMInfoRepo 契约套件）、
run_tests 自验证（命令探测/成功与失败回注/未探测到的结构化错误）、
MCP 客户端（fake server 全链路：握手/工具注册/调用回注/server 死亡）、
deploy 部署（模板渲染/shell 注入转义/缺参数拒绝/强制审批三模式）、
规划层编排（规划→fork 编码全链路/血缘与换模型/只规划/失败透出）、
append-only 日志与 fork 血缘、轨迹完整性修复、持久 bash 会话（cwd/环境变量/超时/exit）、
文件与搜索工具、命令语义风险解析（含 `$(echo rm) -rf /` 绕过样例）与审批钩子行为。
HTTP 接口层有 `httptest` 集成测试：鉴权（含空令牌放行与 `/healthz` 豁免）、创建会话、
202 异步回合、审批拒绝流、ticket 签发/一次性兑换、SSE 补发与 `Last-Event-ID` 游标；
另有部署前置检查（非回环监听 + 空令牌拒绝启动）与 cwd 白名单 guard 的单测；
上下文投影压缩有专项测试（未达阈值不压/触发后结构不变且原文可经 spill 取回/
重复投影幂等不重复 spill/禁用开关）。
技能库有 loader 测试（frontmatter 解析/损坏即报错/非 skill 目录跳过/清单格式）、
use_skill 工具测试（list/命中/未名纠错/截断 spill）与内置资产冒烟（skills/ 全部可解析）。

## 已知边界

- 上下文压缩的触发以「模型上报的累计 token」为代理指标，不是真实上下文长度；
  resume/fork 出的运行时在累计新 token 后才可能激活（事件日志完整，压缩只影响投影）。
- 沙盒为进程级（独立进程组 + 超时 + 审批），未做到 Codex CLI 的 OS 内核级
  （Seatbelt/Landlock）；部署到多租户环境前应加容器隔离，并配置
  `security.allowed_cwd_roots` 把会话工作区限制在白名单内。
- 鉴权为单 token（`server.auth_token` / `MAGENTS_AUTH_TOKEN`）；非回环监听且未配置令牌时
  进程拒绝启动。EventSource 等无法带 Authorization 头的客户端用 `POST /v1/tickets`
  换 60s 一次性 ticket（`?ticket=`），长期 token 不进 URL。
- 命令语义解析是启发式的（切段 + 替换展开 + 效果规则），不是完整 shell AST；
  fork 炸弹等少数模式不在 v1 覆盖范围（测试中有标注）。
- LLM 适配器目前为非流式；SSE 推送的是结构化事件而非 token 流。
- MCP 客户端 v1 为 stdio 传输 + 串行 RPC（无并发请求、无 SSE 传输）；
  server 的信任边界由配置作者负责（read_only 声明即免审批依据）。
- 规划层编排的角色化 v1 是提示词注入（规划会话仅靠提示词约束不改文件，
  尚无按会话的只读工具视图过滤）；编排状态存于内存，进程重启后不可查询
  （会话本体与血缘在日志中持久，可经会话 API 继续）。
- deploy 的命令模板经参数转义后仍以 shell 执行，模板本身的安全由配置作者负责；
  多租户环境部署前应叠加容器隔离（见第一条）。

## 文档

- [设计与实现分析及改进建议](docs/design-implementation-review.md)
- [全栈四层架构对照与整合分析](docs/fullstack-arch-analysis.md)
