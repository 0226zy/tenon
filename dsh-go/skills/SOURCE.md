# fullstack-skills-pack

面向 coding agent 的全栈工程经验 skill 库——沉淀开源社区与工程文献的成熟共识，目标：**agent + 模型 为「非技术背景用户」一次性构建完整可用的 Web 应用时的一次成功率最大化**。

## 设计哲学

能下沉为确定性产物的，绝不留给 LLM 推理：

| 层 | 机制 | 承担 |
|---|---|---|
| L0 | 项目模板/脚手架 | 目录结构、构建配置、依赖版本 |
| L1 | 验证脚本（本库 scripts/） | 「做对没有」的机器判定 |
| L2 | 本 skill 库 | 「怎么做」的程序性知识（按需加载） |
| L3 | LLM 推理 | 只剩用户的具体业务逻辑 |

## Skill 索引（8 个）

| Skill | 触发时机 | 一句话内容 |
|---|---|---|
| `product-scope` | 接到新应用需求 | MVP 范围裁剪：五件套保留、蔓延项拒绝话术、需求澄清提问清单 |
| `fullstack-architecture` | 初始化/新增模块 | 技术栈锁定表、前后端路线选择、目录宪法、Go 装配骨架 |
| `frontend-react` | 项目用 React 路线 | 状态分层、数据四态、RHF+zod 表单、Top10 坑 |
| `frontend-vue` | 项目用 Vue 路线 | script setup、Pinia+vue-query、Element Plus、vee-validate/zod、Top10 坑 |
| `backend-patterns` | 写 Go 后端代码 | ServeMux 路由、统一错误契约、sqlc+goose、连接池、Top12 坑 + `check_backend.py` |
| `security-baseline` | 涉及认证/密钥/用户数据 | 9 条交付红线、scs+bcrypt 认证骨架、授权模式 + `check_secrets.py` |
| `deploy-ship` | 构建/部署/上线前 | Go+前端 Docker 黄金标准、compose 门控、上线门禁四检 + `smoke_test.py`、`check_urls.py` |
| `debug-playbook` | 排障 | Go 12 + React 10 + Vue 6 + 部署 8 + 安全 8 高频坑按症状索引 |

## 技术栈（v2）

- **前端双路线**：默认 **React**（Vite + TS + TanStack Query + RHF/zod）；用户要求时切 **Vue**（Vue 3 script setup + Pinia + vue-query + Element Plus + vee-validate/zod）。一次项目只用一条路线。
- **后端统一 Go**：标准库 ServeMux 路由 + sqlc + goose 迁移 + slog + scs/bcrypt 认证，cmd/+internal 按域分包。单二进制静态编译，容器部署极简。

## 接入方法

### Claude Code / 任何兼容 agentskills.io 的 agent
把整个目录复制到项目的 `.claude/skills/`（或等价 skills 目录），agent 按各 SKILL.md 的 description 自动触发。前端两个 skill 按所选路线只会命中其一。

### stackforge（本项目配套 CLI agent）
stackforge 用 `read_file` 即可加载任意 SKILL.md；验证脚本通过 `run_command` 执行：
```bash
python3 skills-pack/security-baseline/scripts/check_secrets.py .
python3 skills-pack/backend-patterns/scripts/check_backend.py ./api
python3 skills-pack/deploy-ship/scripts/smoke_test.py http://localhost:8080
```

## 评测建议

skill 库要靠数据迭代，不靠感觉：

1. 准备 10–20 个代表性任务（「做一个带登录的 XX 应用」变体，React/Vue 各半）。
2. 对照实验：同一模型，有/无 skill 各跑一遍，记录一次成功率、token 消耗、修复轮次。
3. 每次改 skill 后重跑评测集，回归恶化即回滚。
4. 依赖版本与安全建议会过时——skill 库按代码资产维护（版本号 + CI 评测）。

## 溯源

所有条目蒸馏自 `../research/` 下 6 份调研简报（React / Go / 安全 / 部署与产品化 / Vue，共约 150 条经验，每条带溯源 URL 与共识级别标注），主要来源：go.dev、react.dev、vuejs.org、pinia.vuejs.org、tanstack.com、vite.dev、OWASP Cheat Sheet Series、12factor.net、docs.docker.com、uber-go/guide、Bulletproof React、JetBrains Go 生态调查 2025，以及 2025–2026 年多份 AI 生成应用安全审计（CVE-2025-48757 等）。

## 版本

- v2（当前）：Vue/React 双前端路线 + Go 后端
- v1：单 React 前端 + FastAPI 后端（已归档于 git 历史）
