import { ref } from 'vue'
import { defineStore } from 'pinia'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import type { FileContent, FileEntry } from '@/types/contract'

export interface DirectoryState {
  path: string
  entries: FileEntry[]
  loaded: boolean
  loading: boolean
  expanded: boolean
  error?: unknown
}
export interface OpenFileState {
  path: string
  content?: FileContent
  loading: boolean
  error?: unknown
  deleted: boolean
  refreshing: boolean
}
export interface WorkspaceConvState {
  dirs: Record<string, DirectoryState>
  files: Record<string, OpenFileState>
  openPath: string
  starting: Record<string, boolean>
}

const START_RETRY_LIMIT = 60_000
const timerKeys = new Map<string, number>()
const fileVersions = new Map<string, number>()

function sortedEntries(entries: FileEntry[]): FileEntry[] {
  return [...entries].sort((a, b) => {
    const aDir = a.type === 'dir' ? 0 : 1
    const bDir = b.type === 'dir' ? 0 : 1
    return aDir - bDir || a.name.localeCompare(b.name, 'zh-CN', { numeric: true, sensitivity: 'base' })
  })
}

function isStarting(error: unknown): boolean {
  return error instanceof ApiError && error.code === 'SANDBOX_STARTING'
}

function retryDelay(error: unknown): number {
  return error instanceof ApiError ? (error.retryAfter ?? 3) : 3
}

export const useWorkspaceStore = defineStore('workspace', () => {
  const byConv = ref<Record<string, WorkspaceConvState>>({})

  function ensure(cid: string): WorkspaceConvState {
    if (!byConv.value[cid]) {
      byConv.value[cid] = {
        dirs: {
          '': { path: '', entries: [], loaded: false, loading: false, expanded: true },
        },
        files: {},
        openPath: '',
        starting: {},
      }
    }
    return byConv.value[cid]
  }

  function schedule(key: string, callback: () => void, seconds: number): void {
    const previous = timerKeys.get(key)
    if (previous !== undefined) window.clearTimeout(previous)
    timerKeys.set(key, window.setTimeout(() => {
      timerKeys.delete(key)
      callback()
    }, Math.max(1, seconds) * 1000))
  }

  async function loadDir(cid: string, path = '', startedAt = Date.now(), force = false): Promise<void> {
    const state = ensure(cid)
    if (!state.dirs[path]) {
      state.dirs[path] = { path, entries: [], loaded: false, loading: false, expanded: true }
    }
    const dir = state.dirs[path]
    if (dir.loading && !force) return
    dir.loading = true
    dir.error = undefined
    try {
      const entries = await api.files.tree(cid, path)
      dir.entries = sortedEntries(entries)
      dir.loaded = true
      dir.expanded = true
      delete state.starting[path]
    } catch (error) {
      if (isStarting(error) && Date.now() - startedAt < START_RETRY_LIMIT) {
        state.starting[path] = true
        schedule(`dir:${cid}:${path}`, () => { void loadDir(cid, path, startedAt, true) }, retryDelay(error))
      } else {
        dir.error = isStarting(error)
          ? new ApiError({ code: 'SANDBOX_START_FAILED', status: 503 })
          : error
        delete state.starting[path]
      }
    } finally {
      dir.loading = false
    }
  }

  async function expand(cid: string, path: string): Promise<void> {
    const state = ensure(cid)
    const dir = state.dirs[path]
    if (dir?.loaded && dir.expanded) {
      dir.expanded = false
      return
    }
    if (dir) dir.expanded = true
    await loadDir(cid, path)
  }

  async function openFile(cid: string, path: string, force = false): Promise<void> {
    const state = ensure(cid)
    state.openPath = path
    const existing = state.files[path]
    if (existing?.content && !force) return
    await loadFile(cid, path, Date.now(), false)
  }

  async function loadFile(cid: string, path: string, startedAt = Date.now(), force = false): Promise<void> {
    const state = ensure(cid)
    if (!state.files[path]) {
      state.files[path] = { path, loading: false, deleted: false, refreshing: false }
    }
    const file = state.files[path]
    if (file.loading && !force) return
    const versionKey = `${cid}:${path}`
    const version = (fileVersions.get(versionKey) ?? 0) + 1
    fileVersions.set(versionKey, version)
    file.loading = true
    file.refreshing = !!file.content
    file.error = undefined
    file.deleted = false
    try {
      const content = await api.files.content(cid, path, file.content?.etag)
      if (fileVersions.get(versionKey) !== version) return
      if (content) file.content = content
      delete state.starting[`file:${path}`]
    } catch (error) {
      if (fileVersions.get(versionKey) !== version) return
      if (isStarting(error) && Date.now() - startedAt < START_RETRY_LIMIT) {
        state.starting[`file:${path}`] = true
        schedule(`file:${cid}:${path}`, () => { void loadFile(cid, path, startedAt, true) }, retryDelay(error))
      } else {
        file.error = isStarting(error)
          ? new ApiError({ code: 'SANDBOX_START_FAILED', status: 503 })
          : error
        file.deleted = error instanceof ApiError && error.status === 404
        delete state.starting[`file:${path}`]
      }
    } finally {
      if (fileVersions.get(versionKey) === version) {
        file.loading = false
        file.refreshing = false
      }
    }
  }

  async function refreshAll(cid: string): Promise<void> {
    const state = ensure(cid)
    const paths = Object.values(state.dirs)
      .filter((dir) => dir.loaded && (dir.expanded || dir.path === ''))
      .map((dir) => dir.path)
    await Promise.allSettled(paths.map((path) => loadDir(cid, path, Date.now(), true)))
    await Promise.allSettled(Object.values(state.files)
      .filter((file) => !!file.content || file.path === state.openPath)
      .map((file) => loadFile(cid, file.path, Date.now(), true)))
  }

  function refreshChanged(cid: string): void {
    schedule(`refresh:${cid}`, () => { void refreshAll(cid) }, 0.5)
  }

  async function ensureDefault(cid: string, changedFiles: Set<string>): Promise<void> {
    const state = ensure(cid)
    if (state.openPath) return
    if (!state.dirs[''].loaded) {
      await loadDir(cid, '')
      if (!state.dirs[''].loaded) return
    }
    if (state.openPath) return
    const rootFiles = state.dirs[''].entries.filter((entry) => entry.type === 'file')
    const readme = rootFiles.find((entry) => /^readme(?:\.|$)/i.test(entry.name))
    const changed = [...changedFiles].reverse().find((path) => !path.endsWith('/'))
    const fallback = rootFiles[0]?.path
    const path = readme?.path || changed || fallback
    if (path) await openFile(cid, path)
  }

  function openFileState(cid: string): OpenFileState | undefined {
    const state = byConv.value[cid]
    return state?.openPath ? state.files[state.openPath] : undefined
  }

  function dispose(cid: string): void {
    ;[...timerKeys.entries()].forEach(([key, timer]) => {
      if (key.includes(`:${cid}:`)) {
        window.clearTimeout(timer)
        timerKeys.delete(key)
      }
    })
    delete byConv.value[cid]
  }

  return { byConv, ensure, loadDir, expand, openFile, loadFile, refreshAll, refreshChanged, ensureDefault, openFileState, dispose }
})
