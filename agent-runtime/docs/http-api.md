# agent-runtime HTTP API 完整示范

本文给出可直接复制运行的 REST + SSE 调用示例。默认服务地址：`http://127.0.0.1:3080`（见 `conf/agent-runtime.yaml` 的 `server.listen`）。

端点实现见 `internal/interfaces/httpapi/server.go`；路由总表亦见 [README.md](../README.md#http-api)。

---

## 0. 前置

```bash
export BASE=http://127.0.0.1:3080

# 若配置了 auth_token（或环境变量 MAGENTS_AUTH_TOKEN），所有 /v1/* 都要带：
export TOKEN=your-secret
export AUTH="Authorization: Bearer $TOKEN"
# 本机 auth_token 为空时，下文可去掉 -H "$AUTH"
```

仓库根目录启动：

```bash
go build -o agent-runtime/bin/agent-runtime ./agent-runtime/cmd/agent-runtime
cd agent-runtime && ./bin/agent-runtime -config conf/agent-runtime.yaml
```

模型密钥按 `conf/models.yaml` 的 `api_key_env` 注入，例如：

```bash
export DEEPSEEK_API_KEY=sk-...
export MOONSHOT_API_KEY=sk-...
```

鉴权规则摘要：

| 路径 | 鉴权 |
|---|---|
| `GET /healthz`、`GET /`（Web UI） | 永远免鉴权 |
| `/v1/*` | 需 `Authorization: Bearer <token>`（`auth_token` 为空则本机放行） |
| `GET /v1/sessions/{id}/stream` | 还可用一次性 `?ticket=`，或兼容 `?token=` |

---

## 1. 健康检查与自省

```bash
# 健康检查（永远免鉴权）
curl -s "$BASE/healthz"
# => {"status":"ok"}

# 已注册插件（llm / tool / hook / statusbar）
curl -s -H "$AUTH" "$BASE/v1/plugins"
```

---

## 2. 模型管理

密钥在 API 视图中永不回传，只返回 `api_key_source`（`env:XXX` / `literal` / `none`）。

```bash
# 列出可选模型
curl -s -H "$AUTH" "$BASE/v1/models"

# 新增/更新自定义模型（upsert）
curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{
    "name": "my-local",
    "provider": "openai-compat",
    "base_url": "http://127.0.0.1:11434/v1",
    "model": "qwen2.5-coder",
    "api_key": "ollama",
    "temperature": 0.2,
    "max_tokens": 8192,
    "timeout_seconds": 120,
    "default": false
  }' \
  "$BASE/v1/models"

# 删除模型（内置 deepseek / kimi 通常不可删）
curl -s -X DELETE -H "$AUTH" "$BASE/v1/models/my-local"
```

---

## 3. 核心工作流：创建会话 → SSE → 发消息 → 审批

### 3.1 创建会话

`mode` 取值：`suggest` | `auto-edit` | `full-auto`。`model` 省略则用 `conf/models.yaml` 中 `default: true` 的条目。

```bash
SID=$(curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{
    "cwd": "/path/to/workspace",
    "mode": "auto-edit",
    "title": "demo",
    "model": "deepseek"
  }' \
  "$BASE/v1/sessions" | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')
echo "session=$SID"
```

响应示例（HTTP 201）：

```json
{
  "id": "xxxxxxxx",
  "title": "demo",
  "cwd": "/path/to/workspace",
  "mode": "auto-edit",
  "model": "deepseek",
  "created_at": "...",
  "parent_id": "",
  "inherited_event_count": 0,
  "running": false
}
```

### 3.2 订阅 SSE（推荐先连流再发消息）

浏览器 `EventSource` 不能自定义 `Authorization` 头时，先签发一次性 ticket（60 秒有效，消费即焚）：

```bash
TICKET=$(curl -s -H "$AUTH" -X POST "$BASE/v1/tickets" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["ticket"])')
# => {"ticket":"...","expires_in":60}

# curl 模拟 SSE
curl -N -H "$AUTH" "$BASE/v1/sessions/$SID/stream?from_seq=0"
# 或：
curl -N "$BASE/v1/sessions/$SID/stream?ticket=$TICKET&from_seq=0"
```

游标：`?from_seq=N`，或重连时由浏览器自动携带的 `Last-Event-ID`（服务端按 `Last-Event-ID + 1` 补发）。实现上先订阅再补发历史，按 `seq` 去重，避免丢事件窗口。

SSE 行格式示例：

```
: connected

id: 0
event: marker
data: {"seq":0,"ts":"...","kind":"marker","marker":{"type":"created"}}

id: 3
event: message
data: {"seq":3,"kind":"message","message":{"role":"assistant","content":"..."}}

id: 5
event: approval
data: {"seq":5,"kind":"approval","approval":{"id":"appr-...","tool_name":"bash","summary":"...","by":"pending"}}
```

事件 `kind`：

| kind | 含义 |
|---|---|
| `message` | 四角色消息（user / assistant / tool / system） |
| `notice` | Harness 提示（活性警告、熔断、截断说明等） |
| `marker` | 结构标记（created / forked / turn_done / progress 等），不进模型上下文 |
| `approval` | 高风险操作审批请求/结果；`by=pending` 表示等待裁决 |

### 3.3 发起回合（异步，HTTP 202）

```bash
curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"content":"用一句话说明当前工作区顶层有哪些目录"}' \
  "$BASE/v1/sessions/$SID/messages"
# => {"status":"running"}
```

### 3.4 查询会话 / 列表 / 回放 / 搜索

```bash
curl -s -H "$AUTH" "$BASE/v1/sessions"
curl -s -H "$AUTH" "$BASE/v1/sessions/$SID"

# 从序号 N 起回放事件
curl -s -H "$AUTH" "$BASE/v1/sessions/$SID/events?from_seq=0"

# 全文检索事件内容（q 必填）
curl -s -H "$AUTH" --get --data-urlencode "q=顶层" \
  "$BASE/v1/sessions/$SID/search"
```

### 3.5 审批

在 `suggest` 模式或高风险工具调用时，会出现 `approval.by == "pending"`。从 SSE / events 取出 `approval.id`：

```bash
AID=appr-xxxxxxxx

curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"approved": true}' \
  "$BASE/v1/sessions/$SID/approvals/$AID"
# => {"status":"resolved"}

# 拒绝：
# -d '{"approved": false}'
```

### 3.6 切换模式 / 取消回合 / Fork

```bash
curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"mode":"suggest"}' \
  "$BASE/v1/sessions/$SID/mode"

curl -s -H "$AUTH" -X POST "$BASE/v1/sessions/$SID/cancel"
# => {"status":"cancelling"}

# fork：at_seq 省略或 -1 = 当前末尾
curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"at_seq": -1}' \
  "$BASE/v1/sessions/$SID/fork"
# 201，新会话带 parent_id / inherited_event_count
```

---

## 4. 规划 → 编码编排

`POST /v1/projects/plan` 异步发起；`plan_id` 即规划会话 ID。编排阶段状态在**进程内存**中，重启后 `GET /plans/{id}` 会 404；规划/编码会话本体仍可通过会话 API 继续使用。

```bash
PLAN=$(curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{
    "cwd": "/path/to/workspace",
    "requirement": "给 bin/agent-runtime 增加一个 -version 打印版本号的 flag",
    "planner_model": "deepseek",
    "coder_model": "deepseek",
    "mode": "auto-edit",
    "auto_implement": true
  }' \
  "$BASE/v1/projects/plan")
echo "$PLAN"
# 202 => {"plan_id":"...","phase":"planning"}

PID=$(echo "$PLAN" | python3 -c 'import sys,json; print(json.load(sys.stdin)["plan_id"])')

# 轮询阶段：planning → planned → coding → done
# 失败：plan_failed / code_failed
curl -s -H "$AUTH" "$BASE/v1/projects/plans/$PID"
```

请求体字段：

| 字段 | 说明 |
|---|---|
| `cwd` | 工作区根（空 = 服务端当前目录） |
| `requirement` | 需求描述（必填） |
| `planner_model` | 规划会话模型（空 = 缺省） |
| `coder_model` | 编码会话模型（空 = 沿用规划会话模型） |
| `mode` | 审批模式（空 = 服务端缺省） |
| `auto_implement` | `true` 时规划完成后自动 fork 并启动编码会话 |

---

## 5. 一条龙脚本（本机无鉴权）

需服务已启动，且已配置模型密钥。保存为 `demo-api.sh` 后执行：

```bash
#!/usr/bin/env bash
set -euo pipefail
BASE=${BASE:-http://127.0.0.1:3080}
# 有 token 时：AUTH_HDR=(-H "Authorization: Bearer $TOKEN")
AUTH_HDR=()

curl -sf "$BASE/healthz" | grep -q ok

SID=$(curl -sf "${AUTH_HDR[@]}" -H 'Content-Type: application/json' \
  -d "{\"cwd\":\"$(pwd)\",\"mode\":\"auto-edit\",\"title\":\"api-demo\",\"model\":\"deepseek\"}" \
  "$BASE/v1/sessions" | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')
echo "created session: $SID"

curl -sN "${AUTH_HDR[@]}" "$BASE/v1/sessions/$SID/stream?from_seq=0" &
SSE_PID=$!
trap 'kill $SSE_PID 2>/dev/null || true' EXIT
sleep 0.5

curl -sf "${AUTH_HDR[@]}" -H 'Content-Type: application/json' \
  -d '{"content":"只回答：你好，agent-runtime"}' \
  "$BASE/v1/sessions/$SID/messages"
echo "turn accepted (202)"

sleep 25
echo "--- events ---"
curl -sf "${AUTH_HDR[@]}" "$BASE/v1/sessions/$SID/events?from_seq=0" | python3 -m json.tool | head -80
```

浏览器也可直接打开 `http://127.0.0.1:3080/`，内嵌 Web UI 走同一套 API。

---

## 6. 端点速查

| 方法 | 路径 | 成功码 | 请求体要点 |
|---|---|---|---|
| GET | `/healthz` | 200 | — |
| GET | `/v1/plugins` | 200 | — |
| GET | `/v1/models` | 200 | — |
| POST | `/v1/models` | 200 | `LLMInfo` JSON |
| DELETE | `/v1/models/{name}` | 200 | — |
| POST | `/v1/sessions` | 201 | `{cwd, mode, title, model}` |
| GET | `/v1/sessions` | 200 | — |
| GET | `/v1/sessions/{id}` | 200 | — |
| GET | `/v1/sessions/{id}/events?from_seq=` | 200 | — |
| GET | `/v1/sessions/{id}/search?q=` | 200 | `q` 必填 |
| POST | `/v1/sessions/{id}/fork` | 201 | `{at_seq}`，默认 -1 |
| POST | `/v1/sessions/{id}/messages` | **202** | `{content}` |
| POST | `/v1/sessions/{id}/cancel` | 200 | — |
| POST | `/v1/sessions/{id}/mode` | 200 | `{mode}` |
| POST | `/v1/sessions/{id}/approvals/{aid}` | 200 | `{approved: bool}` |
| POST | `/v1/tickets` | 201 | — |
| GET | `/v1/sessions/{id}/stream` | SSE | `?from_seq=` / `?ticket=` / `Last-Event-ID` |
| POST | `/v1/projects/plan` | **202** | `{cwd, requirement, ...}` |
| GET | `/v1/projects/plans/{id}` | 200 | — |
| GET | `/` | 200 | Web UI |

常见错误：

| 状态码 | 含义 |
|---|---|
| 401 | 鉴权失败 |
| 404 | 会话 / 审批 / 编排不存在 |
| 409 | 会话忙（上一回合未结束） |
| 400 | 参数非法 |
