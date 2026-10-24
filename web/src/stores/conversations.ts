import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api } from '@/api'
import { sseManager } from '@/events/sse'
import type {
  ConversationDetail,
  ConversationView,
  RunStatus,
} from '@/types/contract'

function mergeConversation(list: ConversationView[], value: ConversationView): ConversationView[] {
  const index = list.findIndex((item) => item.id === value.id)
  if (index < 0) return [value, ...list]
  const next = [...list]
  next[index] = { ...next[index], ...value }
  return next
}

export const useConversationsStore = defineStore('conversations', () => {
  const list = ref<ConversationView[]>([])
  const details = ref<Record<string, ConversationDetail>>({})
  const currentId = ref('')
  const nextCursor = ref('')
  const loaded = ref(false)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref<unknown>(null)
  const current = computed(() => details.value[currentId.value] || list.value.find((item) => item.id === currentId.value))

  function sort(): void {
    list.value.sort((a, b) => Date.parse(b.last_active || b.created_at) - Date.parse(a.last_active || a.created_at))
  }

  async function load(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const page = await api.conv.list('', 30)
      list.value = page.items
      nextCursor.value = page.next_cursor
      loaded.value = true
      sort()
    } catch (reason) {
      error.value = reason
      throw reason
    } finally {
      loading.value = false
    }
  }

  async function loadMore(): Promise<void> {
    if (!nextCursor.value || loadingMore.value) return
    loadingMore.value = true
    try {
      const page = await api.conv.list(nextCursor.value, 30)
      page.items.forEach((item) => { list.value = mergeConversation(list.value, item) })
      nextCursor.value = page.next_cursor
      sort()
    } finally {
      loadingMore.value = false
    }
  }

  async function refreshStatus(): Promise<void> {
    const page = await api.conv.list('', 30)
    page.items.forEach((item) => { list.value = mergeConversation(list.value, item) })
    if (!loaded.value) {
      loaded.value = true
      nextCursor.value = page.next_cursor
    }
    sort()
  }

  async function create(model?: string): Promise<ConversationView> {
    const conversation = await api.conv.create({ model })
    list.value = [conversation, ...list.value.filter((item) => item.id !== conversation.id)]
    currentId.value = conversation.id
    return conversation
  }

  async function rename(cid: string, title: string): Promise<void> {
    const normalized = title.trim()
    if (!normalized || normalized.length > 128) throw new Error('标题需为 1–128 个字符')
    const updated = await api.conv.update(cid, { title: normalized })
    list.value = mergeConversation(list.value, updated)
    if (details.value[cid]) details.value[cid] = { ...details.value[cid], ...updated }
  }

  async function setModel(cid: string, model: string): Promise<void> {
    const updated = await api.conv.update(cid, { model })
    list.value = mergeConversation(list.value, updated)
    if (details.value[cid]) details.value[cid] = { ...details.value[cid], ...updated }
  }

  function updateRunLocal(cid: string, runId: string | undefined, status: RunStatus | undefined): void {
    const item = list.value.find((conversation) => conversation.id === cid)
    if (!item || !runId || !status) return
    const active = ['queued', 'dispatching', 'running', 'cancelling', 'finalizing'].includes(status)
    item.run = active ? { run_id: runId, status } : undefined
    item.status = status === 'queued' ? 'queued' : active ? 'running' : item.status === 'error' ? 'error' : 'idle'
  }

  async function remove(cid: string): Promise<void> {
    const index = list.value.findIndex((item) => item.id === cid)
    const snapshot = index >= 0 ? list.value[index] : undefined
    if (index >= 0) list.value.splice(index, 1)
    delete details.value[cid]
    sseManager.close(cid)
    if (currentId.value === cid) currentId.value = ''
    try {
      await api.conv.remove(cid)
    } catch (error) {
      if (snapshot) list.value = mergeConversation(list.value, snapshot)
      sort()
      throw error
    }
  }

  async function getDetail(cid: string, force = false): Promise<ConversationDetail> {
    if (!force && details.value[cid]) return details.value[cid]
    const detail = await api.conv.get(cid)
    details.value[cid] = detail
    list.value = mergeConversation(list.value, detail)
    return detail
  }

  function select(cid: string): void { currentId.value = cid }

  return {
    list, details, currentId, nextCursor, loaded, loading, loadingMore, error, current,
    load, loadMore, refreshStatus, create, rename, setModel, remove, getDetail, select, updateRunLocal,
  }
})
