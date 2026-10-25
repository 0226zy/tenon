import { onScopeDispose, ref } from 'vue'
import { defineStore } from 'pinia'
import { ulid } from 'ulid'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import { emptyChatView, reduce, reduceHistory, type ChatView } from '@/events/reducer'
import { sseManager } from '@/events/sse'
import { isRunActive, isRunTerminal, type MessageView, type RunStatus, type UIFrame } from '@/types/contract'
import { useWorkspaceStore } from './workspace'
import { usePreviewStore } from './preview'

export interface UserMessageState {
  id: string
  serverId?: string
  role: 'user'
  content: string
  runId: string
  createdAt: string
  clientRequestId: string
  status: 'sending' | 'accepted' | 'failed'
  error?: unknown
  needsNewRequestId?: boolean
}

const enterPromises = new Map<string, Promise<void>>()
const recoveryPromises = new Map<string, Promise<void>>()

export const useChatStore = defineStore('chat', () => {
  const byConv = ref<Record<string, ChatView>>({})
  const usersByConv = ref<Record<string, Record<string, UserMessageState>>>({})
  const userOrderByConv = ref<Record<string, string[]>>({})
  const messageCursorByConv = ref<Record<string, string>>({})
  const hasMoreMessagesByConv = ref<Record<string, boolean>>({})
  const drafts = ref<Record<string, string>>({})
  let eventsBound = false
  let unbindFrame: (() => void) | undefined
  let unbindState: (() => void) | undefined

  function ensure(cid: string): ChatView {
    if (!byConv.value[cid]) byConv.value[cid] = emptyChatView()
    if (!usersByConv.value[cid]) usersByConv.value[cid] = {}
    if (!userOrderByConv.value[cid]) userOrderByConv.value[cid] = []
    bindEvents()
    return byConv.value[cid]
  }

  function replaceView(cid: string, view: ChatView): void {
    byConv.value[cid] = view
  }

  function sortUsers(cid: string): void {
    const users = usersByConv.value[cid] || {}
    userOrderByConv.value[cid].sort((left, right) => {
      const time = Date.parse(users[left].createdAt) - Date.parse(users[right].createdAt)
      return time || userOrderByConv.value[cid].indexOf(left) - userOrderByConv.value[cid].indexOf(right)
    })
  }

  function ingestMessages(cid: string, messages: MessageView[]): void {
    ensure(cid)
    const users = usersByConv.value[cid]
    messages.filter((message) => message.role === 'user').forEach((message) => {
      const existingId = userOrderByConv.value[cid].find((id) => {
        const candidate = users[id]
        return candidate.serverId === message.id || (
          !candidate.serverId && candidate.runId === message.run_id && candidate.content === message.content
        )
      })
      if (existingId) {
        const existing = users[existingId]
        existing.serverId = message.id
        existing.runId = message.run_id
        existing.createdAt = message.created_at
        existing.status = 'accepted'
        existing.error = undefined
        return
      }
      const id = `message:${message.id}`
      users[id] = {
        id,
        serverId: message.id,
        role: 'user',
        content: message.content,
        runId: message.run_id,
        createdAt: message.created_at,
        clientRequestId: '',
        status: 'accepted',
      }
      if (!userOrderByConv.value[cid].includes(id)) userOrderByConv.value[cid].push(id)
    })
    sortUsers(cid)
  }

  function markAcceptedFromEvent(cid: string, runId: string): void {
    const users = usersByConv.value[cid] || {}
    const pending = userOrderByConv.value[cid]
      .map((id) => users[id])
      .find((message) => message.status !== 'accepted' && !message.runId)
    if (pending) {
      pending.status = 'accepted'
      pending.runId = runId
      pending.error = undefined
    }
  }

  function bindEvents(): void {
    if (eventsBound) return
    eventsBound = true
    const workspace = useWorkspaceStore()
    const preview = usePreviewStore()
    unbindFrame = sseManager.onFrame((cid, frame, temp) => {
      const current = ensure(cid)
      if (frame.run_id && frame.items.some((item) => item.type === 'run_accepted')) {
        markAcceptedFromEvent(cid, frame.run_id)
      }
      const next = reduce(current, frame, temp)
      replaceView(cid, next)
      frame.items.forEach((item) => {
        if (item.type === 'files_changed') workspace.refreshChanged(cid)
        if (item.type === 'preview_status') preview.applyEvent(cid, item.data)
        if (item.type === 'run_finished' || item.type === 'run_failed' || item.type === 'run_cancelled') {
          void workspace.refreshAll(cid)
          if (item.type === 'run_finished') void preview.refresh(cid).catch(() => undefined)
          const generation = (item.data as typeof item.data & { generation?: number }).generation
          if (generation !== undefined) preview.setLatestRunGeneration(cid, generation)
        }
      })
    })
    unbindState = sseManager.onState((cid, state) => {
      const view = ensure(cid)
      const wasRetrying = view.connection === 'retrying'
      view.connection = state
      if (state === 'open' && wasRetrying) void recover(cid).catch(() => undefined)
    })
  }

  onScopeDispose(() => {
    unbindFrame?.()
    unbindState?.()
  })

  function applyDetail(
    cid: string,
    detail: Awaited<ReturnType<typeof api.conv.get>>,
    advanceCursor = true,
  ): void {
    const view = ensure(cid)
    if (advanceCursor) view.lastSeq = Math.max(view.lastSeq, detail.last_seq)
    if (detail.active_run) {
      view.runId = detail.active_run.run_id
      view.runStatus = detail.active_run.status
    }
    if (!view.preview || detail.preview.generation >= view.preview.generation) {
      view.preview = { ...detail.preview }
    }
  }

  async function enter(cid: string): Promise<void> {
    const existing = enterPromises.get(cid)
    if (existing) return existing
    const promise = (async () => {
      const view = ensure(cid)
      if (view.loaded) {
        await recover(cid)
        sseManager.open(cid, ensure(cid).lastSeq)
        return
      }
      view.loading = true
      try {
        const [detail, events, messages] = await Promise.all([
          api.conv.get(cid),
          api.events.history(cid, { limit: 200 }),
          api.conv.messages(cid, '', 100),
        ])
        let next = reduceHistory(ensure(cid), events.items, false)
        next.eventCursor = events.next_cursor
        next.hasMoreEvents = !!events.next_cursor || events.items.length >= 200
        next.loaded = true
        next.loading = false
        replaceView(cid, next)
        messageCursorByConv.value[cid] = messages.next_cursor
        hasMoreMessagesByConv.value[cid] = !!messages.next_cursor || messages.items.length >= 100
        ingestMessages(cid, messages.items)
        applyDetail(cid, detail)
        sseManager.open(cid, ensure(cid).lastSeq)
      } catch (error) {
        ensure(cid).loading = false
        throw error
      }
    })().finally(() => enterPromises.delete(cid))
    enterPromises.set(cid, promise)
    return promise
  }

  function attach(cid: string): void {
    const view = ensure(cid)
    if (view.loaded) sseManager.open(cid, view.lastSeq)
    else void enter(cid).catch(() => undefined)
  }

  function detach(cid: string): void {
    if (byConv.value[cid]) byConv.value[cid].connection = 'closed'
    sseManager.close(cid)
  }

  async function loadOlder(cid: string): Promise<void> {
    const view = ensure(cid)
    if (view.loadingOlder || (!view.hasMoreEvents && !hasMoreMessagesByConv.value[cid])) return
    view.loadingOlder = true
    try {
      const eventBefore = view.eventCursor || (view.earliestSeq ? String(view.earliestSeq) : '')
      const oldestUserId = userOrderByConv.value[cid]
        .map((id) => usersByConv.value[cid][id].serverId)
        .find(Boolean) || ''
      const messageBefore = messageCursorByConv.value[cid] || oldestUserId
      const [events, messages] = await Promise.all([
        view.hasMoreEvents
          ? api.events.history(cid, { before: eventBefore, limit: 200 })
          : Promise.resolve({ items: [] as UIFrame[], next_cursor: '' }),
        hasMoreMessagesByConv.value[cid]
          ? api.conv.messages(cid, messageBefore, 100)
          : Promise.resolve({ items: [] as MessageView[], next_cursor: '' }),
      ])
      const next = reduceHistory(ensure(cid), events.items, true)
      next.eventCursor = events.next_cursor
      next.hasMoreEvents = view.hasMoreEvents && (!!events.next_cursor || events.items.length >= 200)
      next.loadingOlder = false
      replaceView(cid, next)
      if (hasMoreMessagesByConv.value[cid]) {
        messageCursorByConv.value[cid] = messages.next_cursor
        hasMoreMessagesByConv.value[cid] = !!messages.next_cursor || messages.items.length >= 100
        ingestMessages(cid, messages.items)
      }
    } finally {
      ensure(cid).loadingOlder = false
    }
  }

  async function recover(cid: string): Promise<void> {
    const existing = recoveryPromises.get(cid)
    if (existing) return existing
    const promise = (async () => {
      const workspace = useWorkspaceStore()
      const preview = usePreviewStore()
      const view = ensure(cid)
      const previousSeq = view.lastSeq
      const detail = await api.conv.get(cid)
      // 详情只同步运行与预览状态；必须先按旧游标补齐事件，再推进高水位。
      applyDetail(cid, detail, false)
      if (view.runId) {
        const run = await api.run.get(cid, view.runId)
        view.runStatus = run.status
        if (run.status === 'finished') preview.setLatestRunGeneration(cid, run.generation)
      }
      let hasMore = true
      let beforeSeq = previousSeq
      while (hasMore) {
        const page = await api.events.history(cid, { after: beforeSeq, limit: 200 })
        if (page.items.length) {
          replaceView(cid, reduceHistory(ensure(cid), page.items, false))
          ingestMessages(cid, (await api.conv.messages(cid, '', 100)).items)
        }
        const nextSeq = ensure(cid).lastSeq
        hasMore = page.items.length >= 200 && nextSeq > beforeSeq
        beforeSeq = nextSeq
      }
      const current = ensure(cid)
      current.lastSeq = Math.max(current.lastSeq, detail.last_seq)
      await Promise.allSettled([workspace.refreshAll(cid), preview.refresh(cid)])
    })().finally(() => recoveryPromises.delete(cid))
    recoveryPromises.set(cid, promise)
    return promise
  }

  function validateContent(content: string): string {
    const normalized = content.trim()
    if (!normalized) throw new ApiError({ code: 'EMPTY', status: 400 })
    if ([...normalized].length > 20_000) throw new ApiError({ code: 'TOO_LONG', status: 400 })
    return normalized
  }

  async function dispatch(cid: string, message: UserMessageState): Promise<void> {
    try {
      const result = await api.run.send(cid, {
        client_request_id: message.clientRequestId,
        content: message.content,
      })
      message.status = 'accepted'
      message.serverId = result.message_id
      message.runId = result.run_id
      message.error = undefined
      const view = ensure(cid)
      view.runId = result.run_id
      view.runStatus = result.status
      sseManager.open(cid, view.lastSeq)
    } catch (error) {
      // SSE 可能先于未知 HTTP 结果到达；已接受就不能再标成失败。
      if (message.status === 'accepted') return
      message.status = 'failed'
      message.error = error
      message.needsNewRequestId = error instanceof ApiError && error.code === 'IDEMPOTENCY_CONFLICT'
      if (error instanceof ApiError && error.code === 'RUN_IN_PROGRESS') void recover(cid).catch(() => undefined)
      throw error
    }
  }

  async function send(cid: string, content: string): Promise<UserMessageState> {
    const view = ensure(cid)
    if (isRunActive(view.runStatus)) throw new ApiError({ code: 'RUN_IN_PROGRESS', status: 409 })
    const normalized = validateContent(content)
    const clientRequestId = ulid()
    const id = `local:${clientRequestId}`
    const message: UserMessageState = {
      id,
      role: 'user',
      content: normalized,
      runId: '',
      createdAt: new Date().toISOString(),
      clientRequestId,
      status: 'sending',
    }
    usersByConv.value[cid][id] = message
    userOrderByConv.value[cid].push(id)
    drafts.value[cid] = ''
    const pending = usersByConv.value[cid][id]
    await dispatch(cid, pending)
    return pending
  }

  async function retryMessage(cid: string, messageId: string): Promise<void> {
    const message = usersByConv.value[cid]?.[messageId]
    if (!message || message.status !== 'failed') return
    if (isRunActive(ensure(cid).runStatus)) throw new ApiError({ code: 'RUN_IN_PROGRESS', status: 409 })
    if (message.needsNewRequestId) {
      message.clientRequestId = ulid()
      message.needsNewRequestId = false
    }
    message.status = 'sending'
    message.error = undefined
    await dispatch(cid, message)
  }

  async function retryRun(cid: string, runId: string): Promise<void> {
    const view = ensure(cid)
    if (view.runId !== runId || (view.runStatus !== 'failed' && view.runStatus !== 'cancelled')) return
    let source = Object.values(usersByConv.value[cid] || {}).find((message) => message.runId === runId)
    if (!source) {
      const page = await api.conv.messages(cid, '', 100)
      ingestMessages(cid, page.items)
      source = Object.values(usersByConv.value[cid] || {}).find((message) => message.runId === runId)
    }
    if (source) await send(cid, source.content)
  }

  async function cancel(cid: string): Promise<void> {
    const view = ensure(cid)
    if (!view.runId || !isRunActive(view.runStatus) || view.runStatus === 'finalizing') return
    const result = await api.run.cancel(cid, view.runId)
    view.runStatus = result.status
  }

  function setDraft(cid: string, value: string): void { drafts.value[cid] = value }
  function users(cid: string): UserMessageState[] {
    return (userOrderByConv.value[cid] || []).map((id) => usersByConv.value[cid][id]).filter(Boolean)
  }
  function hasMore(cid: string): boolean {
    return !!ensure(cid).hasMoreEvents || !!hasMoreMessagesByConv.value[cid]
  }
  function setRunStatus(cid: string, status: RunStatus): void { ensure(cid).runStatus = status }
  function isLatestTerminal(cid: string, runId: string): boolean {
    const view = ensure(cid)
    return view.runId === runId && isRunTerminal(view.runStatus) && view.runStatus !== 'finished'
  }

  function dispose(cid: string): void {
    detach(cid)
    delete byConv.value[cid]
    delete usersByConv.value[cid]
    delete userOrderByConv.value[cid]
    delete drafts.value[cid]
    delete messageCursorByConv.value[cid]
    delete hasMoreMessagesByConv.value[cid]
  }

  return {
    byConv, usersByConv, userOrderByConv, drafts,
    ensure, enter, attach, detach, recover, loadOlder, send, retryMessage, retryRun,
    cancel, setDraft, users, hasMore, setRunStatus, isLatestTerminal, ingestMessages, dispose,
  }
})
