import { ref } from 'vue'
import { defineStore } from 'pinia'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import type { PreviewStatus, PreviewView } from '@/types/contract'

const MAX_LOG_BYTES = 128 * 1024
const requestVersions = new Map<string, number>()
const pollTimers = new Map<string, number>()

export interface PreviewConvState {
  status: PreviewStatus
  generation: number
  commit: string
  stale: boolean
  errorCode: string
  error?: unknown
  loading: boolean
  restarting: boolean
  displayedGeneration: number
  displayedUrl: string
  displayedCommit: string
  latestRunGeneration: number
  logs: string
  logsLoading: boolean
  logsOpen: boolean
  logsTruncated: boolean
}

export function emptyPreviewState(): PreviewConvState {
  return {
    status: 'none', generation: 0, commit: '', stale: false, errorCode: '',
    loading: false, restarting: false, displayedGeneration: 0, displayedUrl: '',
    displayedCommit: '', latestRunGeneration: 0, logs: '', logsLoading: false,
    logsOpen: false, logsTruncated: false,
  }
}

/** 纯合并函数：响应乱序或 generation 回退时，不覆盖当前展示版本。 */
export function mergePreviewState(current: PreviewConvState, incoming: PreviewView & { error_code?: string }): PreviewConvState {
  if (incoming.generation < current.generation) return { ...current, loading: false }
  const next: PreviewConvState = {
    ...current,
    status: incoming.status,
    generation: incoming.generation,
    commit: incoming.commit || current.commit,
    stale: incoming.stale || incoming.generation < current.latestRunGeneration,
    errorCode: incoming.error_code || '',
    error: undefined,
    loading: false,
  }
  if (incoming.status === 'ready' && incoming.url && incoming.generation >= current.displayedGeneration) {
    next.displayedGeneration = incoming.generation
    next.displayedUrl = incoming.url
    next.displayedCommit = incoming.commit || ''
  }
  next.stale = next.stale || next.displayedGeneration < next.latestRunGeneration
  return next
}

function tailByBytes(text: string): { text: string; truncated: boolean } {
  const bytes = new TextEncoder().encode(text)
  if (bytes.length <= MAX_LOG_BYTES) return { text, truncated: false }
  const tail = new TextDecoder().decode(bytes.slice(-MAX_LOG_BYTES))
  return { text: `… 仅显示最后 128 KB 日志 …\n${tail}`, truncated: true }
}

export const usePreviewStore = defineStore('preview', () => {
  const byConv = ref<Record<string, PreviewConvState>>({})

  function ensure(cid: string): PreviewConvState {
    if (!byConv.value[cid]) byConv.value[cid] = emptyPreviewState()
    return byConv.value[cid]
  }

  function setLatestRunGeneration(cid: string, generation: number): void {
    const state = ensure(cid)
    state.latestRunGeneration = Math.max(state.latestRunGeneration, generation)
    state.stale = state.generation < state.latestRunGeneration || state.displayedGeneration < state.latestRunGeneration
  }

  async function refresh(cid: string): Promise<PreviewConvState> {
    const state = ensure(cid)
    const version = (requestVersions.get(cid) ?? 0) + 1
    requestVersions.set(cid, version)
    state.loading = true
    try {
      const response = await api.preview.entry(cid)
      if (requestVersions.get(cid) !== version) return state
      Object.assign(state, mergePreviewState(state, response))
      if (state.status === 'ready' || state.status === 'failed') state.restarting = false
    } catch (error) {
      if (requestVersions.get(cid) === version) {
        state.error = error
        state.loading = false
        state.restarting = false
      }
      throw error
    }
    return state
  }

  function applyEvent(cid: string, data: {
    status: PreviewStatus
    generation: number
    commit?: string
    error_code?: string
  }): void {
    const state = ensure(cid)
    if (data.generation < state.generation) return
    const needsTicket = data.status === 'ready' && (
      !state.displayedUrl
      || state.displayedGeneration < data.generation
      || (!!data.commit && state.displayedCommit !== data.commit)
    )
    Object.assign(state, mergePreviewState(state, {
      status: data.status,
      generation: data.generation,
      commit: data.commit,
      stale: state.stale,
      error_code: data.error_code,
    }))
    if (needsTicket) {
      // temp 事件不含一次性票据；只有尚未展示的版本才需要重新换取。
      // 重复 ready 通知不能放大成预览入口和沙箱唤醒请求。
      void refresh(cid).catch(() => undefined)
    }
  }

  function schedulePoll(cid: string, startedAt: number): void {
    const previous = pollTimers.get(cid)
    if (previous !== undefined) window.clearTimeout(previous)
    pollTimers.set(cid, window.setTimeout(async () => {
      pollTimers.delete(cid)
      const state = ensure(cid)
      try { await refresh(cid) } catch { return }
      if ((state.status === 'starting' || state.restarting) && Date.now() - startedAt < 90_000) {
        schedulePoll(cid, startedAt)
      } else {
        state.restarting = false
      }
    }, 3000))
  }

  async function restart(cid: string): Promise<void> {
    const state = ensure(cid)
    state.restarting = true
    state.error = undefined
    state.errorCode = ''
    try {
      const response = await api.preview.restart(cid)
      if (response) Object.assign(state, mergePreviewState(state, response))
      state.status = 'starting'
      schedulePoll(cid, Date.now())
    } catch (error) {
      state.restarting = false
      state.error = error
      throw error
    }
  }

  async function openNewWindow(cid: string): Promise<void> {
    const response = await api.preview.entry(cid)
    if (response.generation < ensure(cid).displayedGeneration || !response.url) {
      throw new ApiError({ code: 'PREVIEW_NOT_READY', status: 409 })
    }
    const opened = window.open(response.url, '_blank', 'noopener,noreferrer')
    if (!opened) throw new ApiError({ code: 'POPUP_BLOCKED', status: 0 })
  }

  async function loadLogs(cid: string): Promise<void> {
    const state = ensure(cid)
    state.logsOpen = true
    state.logsLoading = true
    try {
      const result = tailByBytes(await api.preview.logs(cid, 200))
      state.logs = result.text || '暂无日志'
      state.logsTruncated = result.truncated
    } catch (error) {
      state.logs = error instanceof Error ? error.message : String(error)
    } finally {
      state.logsLoading = false
    }
  }

  function closeLogs(cid: string): void { ensure(cid).logsOpen = false }

  function dispose(cid: string): void {
    const timer = pollTimers.get(cid)
    if (timer !== undefined) window.clearTimeout(timer)
    pollTimers.delete(cid)
    requestVersions.delete(cid)
    delete byConv.value[cid]
  }

  return {
    byConv, ensure, refresh, applyEvent, restart, openNewWindow,
    loadLogs, closeLogs, setLatestRunGeneration, dispose,
  }
})
