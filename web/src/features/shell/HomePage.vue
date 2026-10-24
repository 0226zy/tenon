<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { AlertCircle, Code2, MessageSquareText, MonitorPlay, RefreshCw } from 'lucide-vue-next'
import { useConversationsStore } from '@/stores/conversations'
import { useModelsStore } from '@/stores/models'
import { useChatStore } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { ApiError, errorMessage } from '@/api/errors'
import { conversationTitle } from '@/utils/format'
import type { ExamplePrompt } from '@/constants/examples'
import ConversationSidebar from '@/features/conversation-list/ConversationSidebar.vue'
import ChatPanel from '@/features/chat/ChatPanel.vue'
import WelcomePage from './WelcomePage.vue'
import WorkspacePanel from './WorkspacePanel.vue'

const route = useRoute()
const router = useRouter()
const conversations = useConversationsStore()
const models = useModelsStore()
const chat = useChatStore()
const ui = useUiStore()
const pageError = ref<unknown>(null)
const creating = ref(false)
let enterVersion = 0

const cid = computed(() => typeof route.params.cid === 'string' ? route.params.cid : '')
const current = computed(() => conversations.current)
const workspaceWidth = ref(readWorkspaceWidth())
const workspaceStyle = computed(() => ({ width: `${workspaceWidth.value}px` }))
const activeSignature = computed(() => conversations.list.map((item) => `${item.id}:${item.status}:${item.run?.status || ''}`).join('|'))
const runSignature = computed(() => Object.entries(chat.byConv).map(([id, view]) => `${id}:${view.runId || ''}:${view.runStatus || ''}`).join('|'))

function readWorkspaceWidth(): number {
  const saved = Number(localStorage.getItem('proto.workspace.width'))
  return Number.isFinite(saved) && saved >= 360 && saved <= 760 ? saved : 520
}
function saveWorkspaceWidth(): void { localStorage.setItem('proto.workspace.width', String(workspaceWidth.value)) }

async function enterConversation(id: string): Promise<void> {
  const version = ++enterVersion
  pageError.value = null
  conversations.select(id)
  if (!id) return
  try {
    await chat.enter(id)
  } catch (error) {
    if (version !== enterVersion) return
    if (error instanceof ApiError && error.status === 404) {
      ui.error(error)
      await conversations.load().catch(() => undefined)
      await router.replace('/')
    } else pageError.value = error
  }
}

async function createConversation(example?: ExamplePrompt): Promise<void> {
  if (creating.value) return
  creating.value = true
  try {
    await models.load().catch(() => undefined)
    const conversation = await conversations.create(models.defaultModel?.id)
    if (example) chat.setDraft(conversation.id, example.prompt)
    await router.push(`/c/${conversation.id}`)
  } catch (error) {
    ui.error(error)
  } finally {
    creating.value = false
  }
}

watch(cid, (id) => { void enterConversation(id) }, { immediate: true })
watch([cid, activeSignature], ([currentCid]) => {
  const active = conversations.list.filter((item) => item.status === 'queued' || item.status === 'running').map((item) => item.id)
  const wanted = new Set([...active, ...(currentCid ? [currentCid] : [])])
  // 当前对话最后打开，LRU 超限时会优先保留当前对话。
  active.forEach((id) => chat.attach(id))
  if (currentCid) chat.attach(currentCid)
  Object.keys(chat.byConv).filter((id) => !wanted.has(id)).forEach((id) => chat.detach(id))
}, { immediate: true })
watch(runSignature, () => {
  Object.entries(chat.byConv).forEach(([id, view]) => conversations.updateRunLocal(id, view.runId, view.runStatus))
})

let dragging = false
let dragStartX = 0
let dragStartWidth = 0
function onDrag(event: PointerEvent): void {
  if (!dragging) return
  workspaceWidth.value = Math.max(360, Math.min(760, dragStartWidth + dragStartX - event.clientX))
}
function stopDrag(): void {
  if (!dragging) return
  dragging = false
  document.body.classList.remove('is-resizing')
  saveWorkspaceWidth()
  window.removeEventListener('pointermove', onDrag)
  window.removeEventListener('pointerup', stopDrag)
}
function startDrag(event: PointerEvent): void {
  if (event.button !== 0) return
  event.preventDefault()
  dragStartX = event.clientX
  dragStartWidth = workspaceWidth.value
  dragging = true
  document.body.classList.add('is-resizing')
  window.addEventListener('pointermove', onDrag)
  window.addEventListener('pointerup', stopDrag, { once: true })
}
function resizeWithKeyboard(event: KeyboardEvent): void {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const delta = event.key === 'ArrowLeft' ? 24 : -24
  workspaceWidth.value = Math.max(360, Math.min(760, workspaceWidth.value + delta))
  saveWorkspaceWidth()
}
onBeforeUnmount(stopDrag)
</script>

<template>
  <main class="home-shell">
    <ConversationSidebar :current-id="cid" />
    <section class="chat-column">
      <header class="chat-header">
        <div class="chat-header__title">
          <span class="chat-header__icon"><MessageSquareText :size="17" /></span>
          <div><strong>{{ cid ? conversationTitle(current) : '工作区首页' }}</strong><small v-if="cid">{{ current?.model || chat.modelByConv[cid] || '默认模型' }}</small></div>
        </div>
        <div class="chat-header__actions">
          <button class="button button--secondary button--small" type="button" @click="ui.showWorkspace('code')"><Code2 :size="14" />代码</button>
          <button class="button button--secondary button--small" type="button" @click="ui.showWorkspace('preview')"><MonitorPlay :size="14" />预览</button>
        </div>
      </header>
      <div v-if="pageError" class="page-error" role="alert">
        <AlertCircle :size="28" /><h2>对话暂时无法加载</h2><p>{{ errorMessage(pageError) }}</p>
        <button class="button button--primary" type="button" @click="enterConversation(cid)"><RefreshCw :size="15" />重新加载</button>
      </div>
      <ChatPanel v-else-if="cid" :cid="cid" />
      <WelcomePage v-else @create="createConversation()" @select="createConversation" />
    </section>

    <button
      class="workspace-resizer"
      type="button"
      role="separator"
      aria-label="调整工作区宽度"
      aria-orientation="vertical"
      :aria-valuenow="workspaceWidth"
      aria-valuemin="360"
      aria-valuemax="760"
      @pointerdown="startDrag"
      @keydown="resizeWithKeyboard"
    ><span /></button>
    <button v-if="ui.mobileWorkspaceOpen" class="workspace-backdrop" aria-label="关闭工作区" @click="ui.mobileWorkspaceOpen = false" />
    <div class="workspace-container" :class="{ 'workspace-container--open': ui.mobileWorkspaceOpen }" :style="workspaceStyle">
      <WorkspacePanel :cid="cid" />
    </div>
  </main>
</template>
