<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { AlertCircle, Code2, MessageSquareText, MonitorPlay, PanelRightOpen, RefreshCw } from 'lucide-vue-next'
import { useConversationsStore } from '@/stores/conversations'
import { useChatStore } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { ApiError, errorMessage } from '@/api/errors'
import { conversationTitle } from '@/utils/format'
import type { ExamplePrompt } from '@/constants/examples'
import ConversationSidebar from '@/features/conversation-list/ConversationSidebar.vue'
import ChatPanel from '@/features/chat/ChatPanel.vue'
import AuthDialog from '@/features/login/AuthDialog.vue'
import WelcomePage from './WelcomePage.vue'
import WorkspacePanel from './WorkspacePanel.vue'

const route = useRoute()
const router = useRouter()
const conversations = useConversationsStore()
const chat = useChatStore()
const ui = useUiStore()
const auth = useAuthStore()
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
  // 游客可以浏览首页，真正创建对话时才要求登录或注册。
  // 刷新后会话可能尚未回填，先确认一次登录态，避免把已登录用户当成游客。
  if (!auth.authenticated) await auth.ensure().catch(() => null)
  if (!auth.authenticated) {
    ui.openAuth({ type: 'create', example })
    return
  }
  if (creating.value) return
  creating.value = true
  try {
    const conversation = await conversations.create()
    if (example) chat.setDraft(conversation.id, example.prompt)
    await router.push(`/c/${conversation.id}`)
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      ui.openAuth({ type: 'create', example })
      return
    }
    ui.error(error)
  } finally {
    creating.value = false
  }
}

/** 登录/注册成功后继续刚才被拦截的动作，避免用户二次点击。 */
async function onAuthSuccess(): Promise<void> {
  const intent = ui.authIntent
  ui.closeAuth()
  if (!intent) return
  if (intent.type === 'redirect') {
    await router.replace(intent.path)
    return
  }
  await createConversation(intent.example)
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
          <div><strong>{{ cid ? conversationTitle(current) : '工作区首页' }}</strong></div>
        </div>
        <div class="chat-header__actions">
          <button class="button button--secondary button--small" type="button" @click="ui.showWorkspace('code')"><Code2 :size="14" />代码</button>
          <button class="button button--secondary button--small" type="button" @click="ui.showWorkspace('preview')"><MonitorPlay :size="14" />预览</button>
        </div>
      </header>
      <div v-if="pageError" class="page-error" role="alert">
        <AlertCircle :size="28" /><h2>项目暂时无法加载</h2><p>{{ errorMessage(pageError) }}</p>
        <button class="button button--primary" type="button" @click="enterConversation(cid)"><RefreshCw :size="15" />重新加载</button>
      </div>
      <ChatPanel v-else-if="cid" :cid="cid" />
      <WelcomePage v-else @create="createConversation()" @select="createConversation" />
    </section>

    <button
      v-if="!ui.workspaceCollapsed"
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
    <div v-if="!ui.workspaceCollapsed" class="workspace-container" :class="{ 'workspace-container--open': ui.mobileWorkspaceOpen }" :style="workspaceStyle">
      <WorkspacePanel :cid="cid" />
    </div>
    <aside v-else class="workspace-rail" aria-label="代码与预览已收起">
      <button class="workspace-rail__button workspace-rail__expand" type="button" aria-label="展开代码与预览" title="展开代码与预览" @click="ui.toggleWorkspace()">
        <PanelRightOpen :size="16" />
      </button>
      <span class="workspace-rail__divider" />
      <button class="workspace-rail__button" type="button" aria-label="展开代码" title="代码" @click="ui.showWorkspace('code')"><Code2 :size="16" /></button>
      <button class="workspace-rail__button" type="button" aria-label="展开预览" title="预览" @click="ui.showWorkspace('preview')"><MonitorPlay :size="16" /></button>
      <span class="workspace-rail__label">代码与预览</span>
    </aside>
    <AuthDialog
      v-if="ui.authDialogOpen"
      :tab="ui.authDialogTab"
      @close="ui.closeAuth()"
      @success="onAuthSuccess"
    />
  </main>
</template>
