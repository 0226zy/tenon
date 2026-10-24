import type {
  PreviewBrief,
  RunEnd,
  RunStatus,
  UIFrame,
  UIItem,
} from '@/types/contract'

export interface TextItem {
  id: string
  type: 'text'
  key: string
  runId: string
  seq?: number
  position: number
  ts: string
  content: string
  streaming?: boolean
}
export interface ToolItem {
  id: string
  type: 'tool'
  key: string
  runId: string
  seq?: number
  position: number
  ts: string
  callId: string
  call?: { name: string; summary: string; args: string }
  result?: { name: string; content: string; is_error: boolean; truncated: boolean }
}
export interface NoticeItem {
  id: string
  type: 'notice'
  key: string
  runId: string
  seq?: number
  position: number
  ts: string
  text: string
}
export interface ModelChangedItem {
  id: string
  type: 'model_changed'
  key: string
  runId: string
  seq?: number
  position: number
  ts: string
  from: string
  to: string
}
export interface RunEndItem {
  id: string
  type: 'run_end'
  key: string
  runId: string
  seq?: number
  position: number
  ts: string
  status: 'finished' | 'failed' | 'cancelled'
  data: RunEnd
}
export type ViewItem = TextItem | ToolItem | NoticeItem | ModelChangedItem | RunEndItem
export type SseConnectionState = 'closed' | 'connecting' | 'open' | 'retrying'

export interface ChatView {
  items: Map<string, ViewItem>
  order: string[]
  seen: Set<string>
  lastSeq: number
  earliestSeq?: number
  runId?: string
  runStatus?: RunStatus
  latestFinishedGeneration?: number
  progress?: string
  delta: Record<string, string>
  changedFiles: Set<string>
  preview?: PreviewBrief & { error_code?: string }
  eventCursor: string
  hasMoreEvents: boolean
  loaded: boolean
  loading: boolean
  loadingOlder: boolean
  connection: SseConnectionState
  warnings: string[]
}

export function emptyChatView(): ChatView {
  return {
    items: new Map(),
    order: [],
    seen: new Set(),
    lastSeq: 0,
    delta: {},
    changedFiles: new Set(),
    eventCursor: '',
    hasMoreEvents: false,
    loaded: false,
    loading: false,
    loadingOlder: false,
    connection: 'closed',
    warnings: [],
  }
}

function cloneView(view: ChatView): ChatView {
  return {
    ...view,
    items: new Map(view.items),
    order: [...view.order],
    seen: new Set(view.seen),
    delta: { ...view.delta },
    changedFiles: new Set(view.changedFiles),
    preview: view.preview ? { ...view.preview } : undefined,
    warnings: [...view.warnings],
  }
}

function sortOrder(view: ChatView): void {
  const position = new Map(view.order.map((id, index) => [id, index]))
  view.order.sort((left, right) => {
    const a = view.items.get(left)
    const b = view.items.get(right)
    if (!a || !b) return 0
    const aSeq = a.seq ?? Number.MAX_SAFE_INTEGER
    const bSeq = b.seq ?? Number.MAX_SAFE_INTEGER
    if (aSeq !== bSeq) return aSeq - bSeq
    if (a.position !== b.position) return a.position - b.position
    return (position.get(left) ?? 0) - (position.get(right) ?? 0)
  })
}

function upsert<T extends ViewItem>(view: ChatView, item: T): void {
  const existing = view.items.get(item.id)
  if (existing) {
    if (existing.seq !== undefined && item.seq !== undefined) item.seq = Math.min(existing.seq, item.seq)
    view.items.set(item.id, { ...existing, ...item } as T)
  } else {
    view.items.set(item.id, item)
    view.order.push(item.id)
  }
}

function removeDelta(view: ChatView, runId: string): void {
  delete view.delta[runId]
  const id = `delta:${runId}`
  if (view.items.delete(id)) view.order = view.order.filter((itemId) => itemId !== id)
}

function itemBase(frame: UIFrame, item: UIItem, index: number, id: string) {
  return {
    id,
    key: item.key,
    runId: frame.run_id || '',
    seq: frame.seq,
    position: index,
    ts: frame.ts,
  }
}

function applyRunStatus(view: ChatView, frame: UIFrame, status: RunStatus): void {
  if (!frame.run_id) return
  if (status === 'queued' && view.runId !== frame.run_id) {
    if (view.runId) removeDelta(view, view.runId)
    view.changedFiles = new Set()
    view.progress = undefined
  }
  view.runId = frame.run_id
  view.runStatus = status
  if (status !== 'running') view.progress = undefined
}

function applyItem(view: ChatView, frame: UIFrame, item: UIItem, index: number, temp: boolean): void {
  const runId = frame.run_id || ''
  switch (item.type) {
    case 'run_accepted':
      applyRunStatus(view, frame, 'queued')
      return
    case 'run_started':
      applyRunStatus(view, frame, 'running')
      return
    case 'run_cancelling':
      applyRunStatus(view, frame, 'cancelling')
      return
    case 'run_finalizing':
      applyRunStatus(view, frame, 'finalizing')
      return
    case 'run_finished':
    case 'run_failed':
    case 'run_cancelled': {
      const status = item.type === 'run_finished' ? 'finished' : item.type === 'run_failed' ? 'failed' : 'cancelled'
      applyRunStatus(view, frame, status)
      view.progress = undefined
      removeDelta(view, runId)
      view.changedFiles = new Set(item.data.files.map((file) => file.path))
      const generation = (item.data as RunEnd & { generation?: number }).generation
      if (status === 'finished' && generation !== undefined) {
        view.latestFinishedGeneration = Math.max(view.latestFinishedGeneration ?? 0, generation)
      }
      const id = `run-end:${runId}`
      upsert(view, { ...itemBase(frame, item, index, id), type: 'run_end', status, data: item.data })
      return
    }
    case 'text_delta': {
      const content = (view.delta[runId] || '') + item.data.text
      view.delta[runId] = content
      const id = `delta:${runId}`
      upsert(view, { ...itemBase(frame, item, index, id), type: 'text', content, streaming: true, seq: undefined })
      return
    }
    case 'text': {
      removeDelta(view, runId)
      const id = `text:${runId}:${item.key || frame.seq || frame.ts}`
      upsert(view, { ...itemBase(frame, item, index, id), type: 'text', content: item.data.content })
      return
    }
    case 'tool_call': {
      const id = `tool:${item.data.call_id}`
      const existing = view.items.get(id) as ToolItem | undefined
      upsert(view, {
        ...itemBase(frame, item, index, id),
        type: 'tool',
        callId: item.data.call_id,
        call: { name: item.data.name, summary: item.data.summary, args: item.data.args },
        result: existing?.result,
      })
      return
    }
    case 'tool_result': {
      const id = `tool:${item.data.call_id}`
      const existing = view.items.get(id) as ToolItem | undefined
      upsert(view, {
        ...itemBase(frame, item, index, id),
        type: 'tool',
        callId: item.data.call_id,
        call: existing?.call,
        result: {
          name: item.data.name,
          content: item.data.content,
          is_error: item.data.is_error,
          truncated: item.data.truncated,
        },
      })
      return
    }
    case 'notice': {
      const id = `notice:${frame.seq ?? frame.ts}:${item.key}`
      upsert(view, { ...itemBase(frame, item, index, id), type: 'notice', text: item.data.text })
      return
    }
    case 'model_changed': {
      const id = `model:${frame.seq ?? frame.ts}:${item.key}`
      upsert(view, { ...itemBase(frame, item, index, id), type: 'model_changed', from: item.data.from, to: item.data.to })
      return
    }
    case 'progress':
      if (temp && runId) {
        if (view.runId !== runId) view.runId = runId
        view.progress = item.data.text
      }
      return
    case 'files_changed':
      item.data.paths.forEach((path) => view.changedFiles.add(path))
      return
    case 'preview_status': {
      const current = view.preview
      if (!current || item.data.generation >= current.generation) {
        view.preview = {
          status: item.data.status,
          generation: item.data.generation,
          commit: item.data.commit,
          error_code: item.data.error_code,
        }
      }
      return
    }
    default: {
      const type = (item as { type?: string }).type || 'unknown'
      const warning = `未知 UI 事件类型：${type}`
      console.warn(`[proto-web] ${warning}`, item)
      view.warnings = [...view.warnings, warning].slice(-5)
    }
  }
}

/** 纯函数：持久项按 (seq,key) 去重；临时项不推进持久游标。 */
export function reduce(view: ChatView, frame: UIFrame, temp = false): ChatView {
  const next = cloneView(view)
  if (!frame || !Array.isArray(frame.items)) {
    console.warn('[proto-web] 忽略结构无效的 UI 帧', frame)
    next.warnings = [...next.warnings, '收到结构无效的事件'].slice(-5)
    return next
  }
  if (!temp && typeof frame.seq === 'number') {
    next.lastSeq = Math.max(next.lastSeq, frame.seq)
    next.earliestSeq = next.earliestSeq === undefined ? frame.seq : Math.min(next.earliestSeq, frame.seq)
  }
  frame.items.forEach((item, index) => {
    if (!temp && typeof frame.seq === 'number') {
      const identity = `${frame.seq}:${item.key}`
      if (next.seen.has(identity)) return
      next.seen.add(identity)
    }
    applyItem(next, frame, item, index, temp)
  })
  sortOrder(next)
  return next
}

/** 历史统一走实时渲染规则；前插时保留当前 Run、增量缓冲和预览高水位。 */
export function reduceHistory(view: ChatView, frames: UIFrame[], prepend = false): ChatView {
  const head = prepend ? {
    runId: view.runId,
    runStatus: view.runStatus,
    latestFinishedGeneration: view.latestFinishedGeneration,
    progress: view.progress,
    delta: { ...view.delta },
    changedFiles: new Set(view.changedFiles),
    preview: view.preview ? { ...view.preview } : undefined,
    lastSeq: view.lastSeq,
    streaming: [...view.items.values()].filter((item) => item.type === 'text' && item.streaming),
  } : undefined
  const sorted = [...frames].sort((a, b) => (a.seq ?? Number.MAX_SAFE_INTEGER) - (b.seq ?? Number.MAX_SAFE_INTEGER))
  let next = sorted.reduce((current, frame) => reduce(current, frame, false), view)
  if (head) {
    next.runId = head.runId
    next.runStatus = head.runStatus
    next.latestFinishedGeneration = head.latestFinishedGeneration
    next.progress = head.progress
    next.delta = head.delta
    next.changedFiles = head.changedFiles
    next.preview = head.preview
    next.lastSeq = Math.max(next.lastSeq, head.lastSeq)
    const streamingIds = new Set(head.streaming.map((item) => item.id))
    next.order = next.order.filter((id) => !streamingIds.has(id))
    head.streaming.forEach((item) => {
      next.items.set(item.id, item)
      if (!next.order.includes(item.id)) next.order.push(item.id)
    })
    sortOrder(next)
  }
  return next
}

export function viewItems(view: ChatView): ViewItem[] {
  return view.order.map((id) => view.items.get(id)).filter((item): item is ViewItem => !!item)
}
