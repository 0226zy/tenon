<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Braces, ChevronLeft, ChevronRight, LogOut, Plus, SquarePen, UserRound } from 'lucide-vue-next'
import { useConversationsStore } from '@/stores/conversations'
import { useAuthStore } from '@/stores/auth'
import { useUiStore } from '@/stores/ui'
import { ApiError } from '@/api/errors'
import ConvItem from './ConvItem.vue'

const props = defineProps<{ currentId: string }>()
const conversations = useConversationsStore()
const auth = useAuthStore()
const ui = useUiStore()
const router = useRouter()
const creating = ref(false)
const menuOpen = ref(false)
const root = ref<HTMLElement>()
let pollTimer: number | undefined

const authed = computed(() => auth.authenticated)
const hasActive = computed(() => conversations.list.some((item) => item.status === 'queued' || item.status === 'running'))

async function load(): Promise<void> {
  if (!authed.value) return
  try { await conversations.load() } catch (error) { ui.error(error) }
}
async function createConversation(): Promise<void> {
  if (!authed.value) await auth.ensure().catch(() => null)
  if (!authed.value) {
    ui.openAuth({ type: 'create' })
    return
  }
  if (creating.value) return
  creating.value = true
  try {
    const conversation = await conversations.create()
    await router.push(`/c/${conversation.id}`)
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      ui.openAuth({ type: 'create' })
      return
    }
    ui.error(error)
  } finally {
    creating.value = false
  }
}
async function logout(): Promise<void> {
  try { await auth.logout() } catch { /* 本地状态已在 finally 清理 */ }
  menuOpen.value = false
  await router.replace('/')
}
function onScroll(event: Event): void {
  const element = event.target as HTMLElement
  if (element.scrollHeight - element.scrollTop - element.clientHeight < 80) void conversations.loadMore().catch(ui.error)
}
function onDocumentClick(event: MouseEvent): void {
  if (menuOpen.value && !root.value?.contains(event.target as Node)) menuOpen.value = false
}

onMounted(() => {
  if (authed.value && !conversations.loaded) void load()
  pollTimer = window.setInterval(() => {
    if (!document.hidden && hasActive.value) void conversations.refreshStatus().catch(() => undefined)
  }, 10_000)
  document.addEventListener('click', onDocumentClick)
})

// 登录/注册成功后侧栏立即补齐对话列表，无需刷新页面。
watch(authed, (value) => {
  if (!value) {
    menuOpen.value = false
    return
  }
  void load()
})

onBeforeUnmount(() => {
  if (pollTimer) window.clearInterval(pollTimer)
  document.removeEventListener('click', onDocumentClick)
})
</script>

<template>
  <aside ref="root" class="conversation-sidebar" :class="{ 'conversation-sidebar--collapsed': ui.sidebarCollapsed }" aria-label="项目导航">
    <div class="sidebar-brand">
      <RouterLink to="/" class="sidebar-brand__link" aria-label="Tenon 首页">
        <span class="brand-mark"><Braces :size="20" /></span>
        <span v-if="!ui.sidebarCollapsed" class="sidebar-brand__text">Tenon<small>PROTOTYPE WORKSPACE</small></span>
      </RouterLink>
      <button class="icon-button sidebar-collapse" type="button" :aria-label="ui.sidebarCollapsed ? '展开项目栏' : '折叠项目栏'" :title="ui.sidebarCollapsed ? '展开' : '折叠'" @click="ui.toggleSidebar">
        <ChevronRight v-if="ui.sidebarCollapsed" :size="17" /><ChevronLeft v-else :size="17" />
      </button>
    </div>

    <div class="sidebar-new">
      <button class="button button--primary" type="button" :disabled="creating" :title="ui.sidebarCollapsed ? '新建项目' : undefined" @click="createConversation">
        <span v-if="creating" class="spinner" /><SquarePen v-else-if="!ui.sidebarCollapsed" :size="17" /><Plus v-else :size="19" />
        <span v-if="!ui.sidebarCollapsed">{{ creating ? '正在创建…' : '新建项目' }}</span>
      </button>
    </div>

    <div v-if="!ui.sidebarCollapsed" class="conversation-list" @scroll.passive="onScroll">
      <template v-if="authed">
        <div class="conversation-list__label"><span>最近项目</span><span>{{ conversations.list.length }}</span></div>
        <div v-if="conversations.loading && !conversations.loaded" class="sidebar-skeleton" aria-label="正在加载项目">
          <span v-for="index in 5" :key="index" />
        </div>
        <div v-else-if="conversations.error && !conversations.loaded" class="sidebar-empty">
          <p>项目加载失败</p><button class="button button--secondary button--small" type="button" @click="load">重试</button>
        </div>
        <div v-else-if="!conversations.list.length" class="sidebar-empty sidebar-empty--initial">
          <span class="sidebar-empty__icon"><SquarePen :size="19" /></span>
          <p>还没有项目</p><small>新建一个项目，开始构建你的第一个原型。</small>
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
      </template>
      <div v-else-if="!auth.checked" class="sidebar-skeleton" aria-label="正在检查登录状态">
        <span v-for="index in 3" :key="index" />
      </div>
      <div v-else class="sidebar-empty sidebar-empty--guest">
        <span class="sidebar-empty__icon"><UserRound :size="19" /></span>
        <p>登录后查看项目</p>
        <button class="button button--secondary button--small" type="button" @click="ui.openAuth(null)">登录或注册</button>
      </div>
    </div>

    <div class="sidebar-user">
      <button v-if="authed" class="sidebar-user__button" type="button" :aria-expanded="menuOpen" aria-label="用户菜单" @click.stop="menuOpen = !menuOpen">
        <span class="avatar"><UserRound :size="17" /></span>
        <span v-if="!ui.sidebarCollapsed" class="sidebar-user__text"><strong>{{ auth.user?.name || auth.user?.user_id }}</strong><small>{{ auth.user?.user_id }}</small></span>
      </button>
      <button v-else-if="!auth.checked" class="sidebar-user__button" type="button" disabled aria-label="正在加载账号">
        <span class="avatar"><UserRound :size="17" /></span>
        <span v-if="!ui.sidebarCollapsed" class="sidebar-user__text"><strong>正在加载…</strong></span>
      </button>
      <button v-else class="sidebar-user__button sidebar-user__button--guest" type="button" aria-label="登录或注册" @click="ui.openAuth(null)">
        <span class="avatar"><UserRound :size="17" /></span>
        <span v-if="!ui.sidebarCollapsed" class="sidebar-user__text"><strong>登录或注册</strong><small>创建项目前需要账号</small></span>
      </button>
      <div v-if="authed && menuOpen" class="user-menu">
        <div class="user-menu__identity"><strong>{{ auth.user?.name }}</strong><span>{{ auth.user?.user_id }}</span></div>
        <button type="button" @click="logout"><LogOut :size="16" />退出登录</button>
      </div>
    </div>
  </aside>
</template>
