'use strict';
// dsh-go Web 控制台逻辑：会话列表 / 聊天视图 / 审批裁决 / fork / 模式切换。
// 数据流：SSE 单一来源（from_seq=0 由服务端补发全部历史后接实时流），不再先 GET /events 预渲染。
let cur = null, es = null;
const TOKEN = localStorage.getItem('dsh_token') || '';
const q = p => p + (p.includes('?') ? '&' : '?') + (TOKEN ? 'token=' + encodeURIComponent(TOKEN) : '');
const authH = TOKEN ? { 'Authorization': 'Bearer ' + TOKEN } : {};

async function api(method, path, body) {
  const r = await fetch(q(path), { method, headers: { 'Content-Type': 'application/json', ...authH },
    body: body ? JSON.stringify(body) : undefined });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
  return data;
}

async function refreshSessions() {
  const { sessions } = await api('GET', '/v1/sessions');
  const box = document.getElementById('sessions');
  box.innerHTML = '';
  (sessions || []).forEach(s => {
    const d = document.createElement('div');
    d.className = 'sess' + (cur && cur.id === s.id ? ' active' : '');
    d.innerHTML = `<div class="t">${s.running ? '<span class="dot"></span>' : ''}${esc(s.title || s.id)}</div>
      <div class="m">${esc(s.cwd)} · ${esc(s.mode)}${s.parent_id ? ' · fork' : ''}</div>`;
    d.onclick = () => select(s.id);
    box.appendChild(d);
  });
}

async function createSession() {
  const cwd = document.getElementById('ncwd').value.trim();
  const mode = document.getElementById('nmode').value;
  const model = document.getElementById('nmodel').value;
  const s = await api('POST', '/v1/sessions', { cwd, mode, model }).catch(e => alert(e.message));
  if (s) { await refreshSessions(); select(s.id); }
}

async function loadModels() {
  const sel = document.getElementById('nmodel');
  try {
    const { models } = await api('GET', '/v1/models');
    sel.innerHTML = '';
    (models || []).forEach(m => {
      const o = document.createElement('option');
      o.value = m.name;
      o.textContent = `${m.name}（${m.model}）${m.default ? ' ·默认' : ''}`;
      if (m.default) o.selected = true;
      sel.appendChild(o);
    });
  } catch (e) {
    sel.innerHTML = '<option value="">（模型列表加载失败）</option>';
  }
}

async function select(id) {
  if (es) { es.close(); es = null; }
  cur = await api('GET', '/v1/sessions/' + id);
  document.getElementById('topbar').style.display = 'flex';
  document.getElementById('composer').style.display = 'flex';
  document.getElementById('tid').textContent = cur.title || cur.id;
  document.getElementById('tmeta').textContent = cur.cwd + (cur.model ? ' · ' + cur.model : '');
  document.getElementById('modesel').value = cur.mode;
  const box = document.getElementById('events');
  box.innerHTML = '';
  // EventSource 不能自定义请求头：启用鉴权时先换取一次性 ticket，避免长期 token 进 URL。
  let streamURL = `/v1/sessions/${id}/stream?from_seq=0`;
  if (TOKEN) {
    try {
      const t = await api('POST', '/v1/tickets');
      streamURL += '&ticket=' + encodeURIComponent(t.ticket);
    } catch (e) {
      streamURL += '&token=' + encodeURIComponent(TOKEN); // 兼容旧服务端
    }
  }
  es = new EventSource(streamURL);
  ['message', 'notice', 'marker', 'approval'].forEach(k =>
    es.addEventListener(k, e => { render(JSON.parse(e.data)); scrollBottom(); refreshSessions(); }));
  refreshSessions();
}

function esc(s) { const d = document.createElement('div'); d.textContent = s == null ? '' : String(s); return d.innerHTML; }
function scrollBottom() { const b = document.getElementById('events'); b.scrollTop = b.scrollHeight; }

function render(ev) {
  const box = document.getElementById('events');
  const empty = document.getElementById('empty');
  if (empty) empty.remove();
  const div = document.createElement('div');

  if (ev.kind === 'message' && ev.message) {
    const m = ev.message;
    if (m.role === 'user') {
      div.className = 'ev user';
      div.innerHTML = `<div class="role">你</div><div class="bubble">${esc(m.content)}</div>`;
    } else if (m.role === 'assistant') {
      if (m.content) {
        const d = document.createElement('div');
        d.className = 'ev assistant';
        d.innerHTML = `<div class="role">Agent</div><div class="bubble">${esc(m.content)}</div>`;
        box.appendChild(d);
      }
      (m.tool_calls || []).forEach(tc => {
        const t = document.createElement('div');
        t.className = 'ev toolcall';
        t.innerHTML = `<div class="bubble"><details><summary>🔧 ${esc(tc.function.name)}</summary>${esc(prettyArgs(tc.function.arguments))}</details></div>`;
        box.appendChild(t);
      });
      refreshSessions();
      return;
    } else if (m.role === 'tool') {
      div.className = 'ev tool';
      div.innerHTML = `<div class="bubble"><details><summary>📄 工具结果${m.name ? ' · ' + esc(m.name) : ''}</summary>${esc(m.content)}</details></div>`;
    } else if (m.role === 'system') {
      div.className = 'ev notice';
      div.textContent = m.content;
    }
  } else if (ev.kind === 'notice') {
    div.className = 'ev notice';
    div.textContent = ev.note;
  } else if (ev.kind === 'marker' && ev.marker) {
    if (ev.marker.type === 'turn_done') { div.className = 'ev marker'; div.textContent = '— 回合完成 —'; refreshSessions(); }
    else if (ev.marker.type === 'turn_failed') { div.className = 'ev marker failed'; div.textContent = '— 回合失败：' + ev.marker.detail + ' —'; refreshSessions(); }
    else if (ev.marker.type === 'subagent_start') { div.className = 'ev marker'; div.textContent = '↳ 子代理开始：' + (ev.marker.detail || ''); }
    else if (ev.marker.type === 'subagent_done') { div.className = 'ev marker'; div.textContent = '↰ 子代理结束：' + (ev.marker.detail || ''); }
    else if (ev.marker.type === 'progress') { div.className = 'ev marker progress'; div.textContent = '· ' + (ev.marker.detail || ''); }
    else return;
  } else if (ev.kind === 'approval' && ev.approval) {
    const a = ev.approval;
    if (a.by === 'pending') {
      div.className = 'ev approval';
      div.id = 'appr-' + a.id;
      div.innerHTML = `<b>⚠ 审批请求</b> ${esc(a.summary)}<div class="detail">${esc(a.detail || '')}</div>
        <button class="primary" onclick="resolveAppr('${a.id}', true)">批准</button>
        <button class="danger" onclick="resolveAppr('${a.id}', false)">拒绝</button>`;
    } else {
      const pend = document.getElementById('appr-' + a.id);
      if (pend) pend.remove();
      div.className = 'ev marker';
      div.textContent = `审批 ${a.approved ? '✔ 已批准' : '✘ 已拒绝'}（${a.by}）`;
    }
  } else { return; }
  box.appendChild(div);
}

function prettyArgs(s) { try { return JSON.stringify(JSON.parse(s), null, 2); } catch { return s; } }
async function resolveAppr(id, approved) {
  await api('POST', `/v1/sessions/${cur.id}/approvals/${id}`, { approved }).catch(e => alert(e.message));
}
async function send() {
  const ta = document.getElementById('input');
  const content = ta.value.trim();
  if (!content || !cur) return;
  ta.value = '';
  await api('POST', `/v1/sessions/${cur.id}/messages`, { content }).catch(e => alert(e.message));
  refreshSessions();
}
async function cancelTurn() { if (cur) await api('POST', `/v1/sessions/${cur.id}/cancel`).catch(() => {}); }
async function setMode() {
  const mode = document.getElementById('modesel').value;
  await api('POST', `/v1/sessions/${cur.id}/mode`, { mode }).catch(e => alert(e.message));
}
async function forkSession() {
  if (!cur) return;
  const s = await api('POST', `/v1/sessions/${cur.id}/fork`, { at_seq: -1 }).catch(e => alert(e.message));
  if (s) { await refreshSessions(); select(s.id); }
}
document.getElementById('input').addEventListener('keydown', e => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
});
loadModels();
refreshSessions();
