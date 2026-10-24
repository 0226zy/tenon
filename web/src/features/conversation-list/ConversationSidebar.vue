<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Braces, ChevronLeft, ChevronRight, LogOut, Plus, SquarePen, UserRound } from 'lucide-vue-next'
import { useConversationsStore } from '@/stores/conversations'
import { useModelsStore } from '@/stores/models'
import { useAuthStore } from '@/stores/auth'
import { useUiStore } from '@/stores/ui'
import ConvItem from './ConvItem.vue'

const props = defineProps<{ currentId: string }>()
const conversations = useConversationsStore()
const models = useModelsStore()
const auth = useAuthStore()
const ui = useUiStore()
const router = useRouter()
const creating = ref(false)
const menuOpen = ref(false)
const root = ref<HTMLElement>()
let pollTimer: number | undefined

const hasActive = computed(() => conversations.list.some((item) => item.status === 'queued' || item.status === 'running'))

async function load(): Promise<void> {
  try { await conversations.load() } catch (error) { ui.error(error) }
}
async function createConversation(): Promise<void> {
  if (creating.value) return
  creating.value = true
  try {
    await models.load().catch(() => undefined)
    const conversation = await conversations.create(models.defaultModel?.id)
    await router.push(`/c/${conversation.id}`)
  } catch (error) {
    ui.error(error)
  } finally {
    creating.value = false
  }
}
async function logout(): Promise<void> {
  try { await auth.logout() } catch { /* 本地状态已在 finally 清理 */ }
  await router.replace('/login')
}
function onScroll(event: Event): void {
  const element = event.target as HTMLElement
  if (element.scrollHeight - element.scrollTop - element.clientHeight < 80) void conversations.loadMore().catch(ui.error)
}
function onDocumentClick(event: MouseEvent): void {
  if (menuOpen.value && !root.value?.contains(event.target as Node)) menuOpen.value = false
}

onMounted(() => {
  if (!conversations.loaded) void load()
  void models.load().catch(() => undefined)
  pollTimer = window.setInterval(() => {
    if (!document.hidden && hasActive.value) void conversations.refreshStatus().catch(() => undefined)
  }, 10_000)
  document.addEventListener('click', onDocumentClick)
})
onBeforeUnmount(() => {
  if (pollTimer) window.clearInterval(pollTimer)
  document.removeEventListener('click', onDocumentClick)
})
</script>

<template>
  <aside ref="root" class="conversation-sidebar" :class="{ 'conversation-sidebar--collapsed': ui.sidebarCollapsed }" aria-label="对话导航">
    <div class="sidebar-brand">
      <RouterLink to="/" class="sidebar-brand__link" aria-label="ProtoForge 首页">
        <span class="brand-mark"><Braces :size="20" /></span>
        <span v-if="!ui.sidebarCollapsed" class="sidebar-brand__text">ProtoForge<small>PROTOTYPE WORKSPACE</small></span>
      </RouterLink>
      <button class="icon-button sidebar-collapse" type="button" :aria-label="ui.sidebarCollapsed ? '展开对话栏' : '折叠对话栏'" :title="ui.sidebarCollapsed ? '展开' : '折叠'" @click="ui.toggleSidebar">
        <ChevronRight v-if="ui.sidebarCollapsed" :size="17" /><ChevronLeft v-else :size="17" />
      </button>
    </div>

    <div class="sidebar-new">
      <button class="button button--primary" type="button" :disabled="creating" :title="ui.sidebarCollapsed ? '新建对话' : undefined" @click="createConversation">
        <span v-if="creating" class="spinner" /><SquarePen v-else-if="!ui.sidebarCollapsed" :size="17" /><Plus v-else :size="19" />
        <span v-if="!ui.sidebarCollapsed">{{ creating ? '正在创建…' : '新建对话' }}</span>
      </button>
    </div>

    <div v-if="!ui.sidebarCollapsed" class="conversation-list" @scroll.passive="onScroll">
      <div class="conversation-list__label"><span>最近对话</span><span>{{ conversations.list.length }}</span></div>
      <div v-if="conversations.loading && !conversations.loaded" class="sidebar-skeleton" aria-label="正在加载对话">
        <span v-for="index in 5" :key="index" />
      </div>
      <div v-else-if="conversations.error && !conversations.loaded" class="sidebar-empty">
        <p>对话加载失败</p><button class="button button--secondary button--small" type="button" @click="load">重试</button>
      </div>
      <div v-else-if="!conversations.list.length" class="sidebar-empty sidebar-empty--initial">
        <span class="sidebar-empty__icon"><SquarePen :size="19" /></span>
        <p>还没有对话</p><small>新建一个对话，开始构建你的第一个原型。</small>
      </div>
      <ConvItem
        v-for="conversation in conversations.list"
        :key="conversation.id"
        :conversation="conversation"
        :active="conversation.id === currentId"
        @select="router.push(`/c/${conversation.id}`)"
      />
      <button v-if="conversations.nextCursor" class="conversation-list__more" type="button" :disabled="conversations.loadingMore" @click="conversations.loadMore().catch(ui.error)">
        {{ conversations.loadingMore ? '正在加载…' : '加载更多' }}
      </button>
    </div>

    <div class="sidebar-user">
      <button class="sidebar-user__button" type="button" :aria-expanded="menuOpen" aria-label="用户菜单" @click.stop="menuOpen = !menuOpen">
        <span class="avatar"><UserRound :size="17" /></span>
        <span v-if="!ui.sidebarCollapsed" class="sidebar-user__text"><strong>{{ auth.user?.name || auth.user?.user_id }}</strong><small>{{ auth.user?.user_id }}</small></span>
      </button>
      <div v-if="menuOpen" class="user-menu">
        <div class="user-menu__identity"><strong>{{ auth.user?.name }}</strong><span>{{ auth.user?.user_id }}</span></div>
        <button type="button" @click="logout"><LogOut :size="16" />退出登录</button>
      </div>
    </div>
  </aside>
</template>
