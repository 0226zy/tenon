# 手工复现：用 HTTP API 让 agent-runtime 完成一个极简前端

本文记录一次端到端手工复现步骤：通过 curl 调用 agent-runtime，让 Agent 在工作区生成静态 Todo 前端，用于验证服务是否正常。

更完整的 API 说明见 [http-api.md](./http-api.md)。

---

## 前提

1. `agent-runtime` 已启动，默认监听 `http://127.0.0.1:3080`。
2. 已配置模型密钥（如 `DEEPSEEK_API_KEY`），`conf/models.yaml` 中有可用模型（如 `deepseek`）。
3. 下文按 **本机 `auth_token` 为空** 书写。若启用了鉴权，每条 `/v1/*` 请求增加：

   ```bash
   -H "Authorization: Bearer <token>"
   ```

启动示例（按你的实际部署路径调整）：

```bash
cd agent-runtime
./bin/agent-runtime -config conf/agent-runtime.yaml
```

---

## 流程一览

| 步骤 | 方法 | 路径 | 成功码 |
|---|---|---|---|
| 0 健康检查 | GET | `/healthz` | 200 |
| 1 列模型（可选） | GET | `/v1/models` | 200 |
| 2 创建会话 | POST | `/v1/sessions` | 201 |
| 3 订阅 SSE | GET | `/v1/sessions/{id}/stream` | SSE |
| 4 发任务 | POST | `/v1/sessions/{id}/messages` | **202** |
| 5 查状态（可选） | GET | `/v1/sessions/{id}` | 200 |
| 6 回放事件 | GET | `/v1/sessions/{id}/events` | 200 |
| 7 验收产物 | — | 工作区目录 | — |
| 8 审批（仅需要时） | POST | `/v1/sessions/{id}/approvals/{aid}` | 200 |

---

## 0. 准备

```bash
export BASE=http://127.0.0.1:3080
export WS="${TMPDIR:-/tmp}/agent-runtime-simple-frontend-manual"

mkdir -p "$WS"
curl -s "$BASE/healthz"
# 期望：{"status":"ok"}
```

---

## 1. 列模型（可选）

```bash
curl -s "$BASE/v1/models" | python3 -m json.tool
```

确认存在 `deepseek`（或你接下来要用的模型名）。

---

## 2. 创建会话

```bash
curl -s -H 'Content-Type: application/json' \
  -d "{\"cwd\":\"$WS\",\"mode\":\"auto-edit\",\"title\":\"manual-frontend\",\"model\":\"deepseek\"}" \
  "$BASE/v1/sessions" | tee /tmp/agent-runtime-sess.json | python3 -m json.tool

export SID=$(python3 -c 'import json; print(json.load(open("/tmp/agent-runtime-sess.json"))["id"])')
echo "SID=$SID"
```

期望：HTTP 201，响应含 `id`、`cwd`、`mode`、`model`。

`mode` 说明：

- `auto-edit`：适合本次复现（多数写文件操作可自动执行）
- `suggest`：高风险工具需人工审批（见第 8 步）

---

## 3. 订阅 SSE（建议另开终端，先于发消息）

**终端 B：**

```bash
curl -N "$BASE/v1/sessions/$SID/stream?from_seq=0"
```

有鉴权且客户端不能带 `Authorization` 头时：

```bash
# 先签发一次性 ticket（60s，消费即焚）
curl -s -X POST -H "Authorization: Bearer $TOKEN" "$BASE/v1/tickets"

# 再用 ticket 连流
curl -N "$BASE/v1/sessions/$SID/stream?ticket=<ticket>&from_seq=0"
```

正常运行时会陆续看到：

- `event: message`（assistant 带 tool_calls、tool 结果）
- `event: marker`（如 `progress`）
- 结束时 `marker` 类型为 `turn_done`（失败则为 `turn_failed`）

---

## 4. 发起回合（核心）

**终端 A：**

```bash
curl -s -H 'Content-Type: application/json' \
  -d '{
    "content": "请在当前工作区实现一个极简静态前端 Todo 系统：\n1. 仅原生 HTML+CSS+JS，无框架/无 npm\n2. 文件：index.html、styles.css、app.js、README.md\n3. 功能：添加、勾选完成/取消、删除、localStorage 持久化\n4. 页面标题「Simple Todo」\n5. 完成后用 bash 确认文件存在，并说明如何用浏览器打开\n不要做多余功能，完成后简短总结。"
  }' \
  "$BASE/v1/sessions/$SID/messages" | python3 -m json.tool
```

期望：HTTP **202**，`{"status":"running"}`。

之后在终端 B 观察 SSE，直到出现 `turn_done`（通常数十秒，视模型与网络而定）。

---

## 5. 轮询会话状态（可选）

```bash
curl -s "$BASE/v1/sessions/$SID" | python3 -m json.tool
# running: true  → 进行中
# running: false → 本回合已结束
```

---

## 6. 回放事件

完整 JSON：

```bash
curl -s "$BASE/v1/sessions/$SID/events?from_seq=0" | python3 -m json.tool | less
```

精简摘要：

```bash
curl -s "$BASE/v1/sessions/$SID/events?from_seq=0" \
  | python3 -c 'import sys,json
for e in json.load(sys.stdin)["events"]:
  if e["kind"]=="marker":
    print(e["seq"], e["marker"])
  elif e["kind"]=="message":
    m=e["message"]; tc=m.get("tool_calls") or []
    print(e["seq"], m["role"], [t["function"]["name"] for t in tc], (m.get("content") or "")[:80].replace("\n"," "))
  elif e["kind"]=="notice":
    print(e["seq"], "notice", e.get("note","")[:120].replace("\n"," "))'
```

期望末尾有 `turn_done`。若是 `turn_failed`，看同会话里的 `notice` 文案（例如模型 API 400、密钥缺失等）。

---

## 7. 验收产物

```bash
ls -la "$WS"
# 应有：index.html  styles.css  app.js  README.md

# 可选：JS 语法检查
node --check "$WS/app.js"

# 浏览器打开
open "$WS/index.html"   # macOS
# 或：
# python3 -m http.server -d "$WS" 8765
# → http://127.0.0.1:8765/
```

功能自测清单：

- [ ] 页面标题为「Simple Todo」
- [ ] 可添加待办
- [ ] 可勾选完成 / 取消
- [ ] 可删除
- [ ] 刷新后 localStorage 仍保留数据

---

## 8. 若卡在审批（仅需要时）

`mode=suggest` 或高风险工具时，SSE / events 会出现 `kind=approval` 且 `by=pending`。取出 `approval.id`：

```bash
export AID=appr_xxxxxxxx   # 换成真实 ID

curl -s -H 'Content-Type: application/json' \
  -d '{"approved": true}' \
  "$BASE/v1/sessions/$SID/approvals/$AID" | python3 -m json.tool
```

拒绝则改 `-d '{"approved": false}'`。
本次推荐的 `auto-edit` 复现一般不需要本步。

---

## 轨迹落盘位置

会话事件（执行轨迹）在 `storage.data_dir` 下，当前常见部署为：

```text
<data_dir>/sessions/<SID>/
├── header.json
└── events.jsonl
```

例如，`data_dir` 配置为 `./data` 时：

```text
./data/sessions/<SID>/events.jsonl
```

超长工具输出 / 子代理审计在：

```text
<data_dir>/spill/<SID>/
```

`data_dir` 以实际 `agent-runtime.yaml` 为准（相对路径相对于**启动 agent-runtime 时的 cwd**）。

---

## 常见问题

| 现象 | 排查 |
|---|---|
| `healthz` 不通 | 进程未起、端口不是 3080、被防火墙拦截 |
| 创建会话 400 | `cwd` / `mode` / `model` 非法 |
| 消息后立刻 `turn_failed` | 看 `notice`：密钥、模型 API、工具 schema 等 |
| `running` 一直 true | 模型慢或卡在审批；看 SSE，必要时 `POST .../cancel` |
| 工作区空 | 确认创建会话时的 `cwd` 与验收目录是同一路径 |

取消进行中的回合：

```bash
curl -s -X POST "$BASE/v1/sessions/$SID/cancel"
```
