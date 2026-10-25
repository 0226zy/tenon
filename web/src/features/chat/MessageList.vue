<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { Braces, ChevronDown, LoaderCircle } from 'lucide-vue-next'
import { useChatStore, type UserMessageState } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { viewItems, type ViewItem } from '@/events/reducer'
import type { ExamplePrompt } from '@/constants/examples'
import ExampleCards from '@/components/ExampleCards.vue'
import TextBubble from './TextBubble.vue'
import UserBubble from './UserBubble.vue'
import ToolCard from './ToolCard.vue'
import RunEndCard from './RunEndCard.vue'

interface TimelineEntry {
  id: string
  time: number
  rank: number
  kind: 'user' | 'event'
  user?: UserMessageState
  item?: ViewItem
}

const props = defineProps<{ cid: string }>()
const emit = defineEmits<{ example: [example: ExamplePrompt]; openFile: [path: string] }>()
const chat = useChatStore()
const ui = useUiStore()
const scroller = ref<HTMLElement>()
const initialized = ref(false)
const paging = ref(false)
const view = computed(() => chat.ensure(props.cid))
const timeline = computed<TimelineEntry[]>(() => {
  const events = viewItems(view.value).map((item, index): TimelineEntry => ({
    id: item.id, time: Date.parse(item.ts) || index, rank: index, kind: 'event', item,
  }))
  const users = chat.users(props.cid).map((user, index): TimelineEntry => ({
    id: user.id, time: Date.parse(user.createdAt) || index, rank: -1, kind: 'user', user,
  }))
  return [...events, ...users].sort((a, b) => a.time - b.time || a.rank - b.rank)
})
const lastSignature = computed(() => {
  const last = timeline.value.at(-1)
  if (!last) return ''
  if (last.kind === 'user') return `${last.id}:${last.user?.status}`
  const item = last.item
  return `${last.id}:${item?.type === 'text' ? item.content.length : item?.type}`
})

function nearBottom(): boolean {
  const element = scroller.value
  return !element || element.scrollHeight - element.scrollTop - element.clientHeight < 180
}
async function scrollToBottom(force = false): Promise<void> {
  const stick = force || nearBottom() || !initialized.value
  await nextTick()
  if (stick && scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight
  initialized.value = true
}
async function loadOlder(): Promise<void> {
  const element = scroller.value
  if (!element || paging.value || !chat.hasMore(props.cid)) return
  paging.value = true
  const previousHeight = element.scrollHeight
  const previousTop = element.scrollTop
  try {
    await chat.loadOlder(props.cid)
    await nextTick()
    element.scrollTop = element.scrollHeight - previousHeight + previousTop
  } catch (error) {
    ui.error(error)
  } finally {
    paging.value = false
  }
}
function onScroll(): void {
  if (scroller.value && scroller.value.scrollTop < 36) void loadOlder()
}
async function retry(runId: string): Promise<void> {
  try { await chat.retryRun(props.cid, runId) } catch (error) { ui.error(error) }
}

watch(() => props.cid, () => { initialized.value = false; void scrollToBottom(true) })
watch(() => timeline.value.length, () => { void scrollToBottom() })
watch(lastSignature, () => { void scrollToBottom() })
onMounted(() => { void scrollToBottom(true) })
</script>

<template>
  <div ref="scroller" class="message-list" @scroll.passive="onScroll">
    <div v-if="chat.hasMore(cid)" class="message-list__older">
      <button class="button button--secondary button--small" type="button" :disabled="view.loadingOlder || paging" @click="loadOlder">
        <LoaderCircle v-if="view.loadingOlder || paging" :size="13" class="spin-animation" /><ChevronDown v-else :size="13" />
        {{ view.loadingOlder || paging ? '正在加载更早记录…' : '加载更早记录' }}
      </button>
    </div>
    <div v-if="view.loading" class="message-skeleton" aria-label="正在恢复项目记录">
      <span v-for="index in 4" :key="index" />
    </div>
    <section v-else-if="!timeline.length" class="empty-chat">
      <span class="empty-chat__icon"><Braces :size="23" /></span>
      <h2>从一个想法开始</h2>
      <p>选择一条示例预填输入框，也可以直接描述你的需求。</p>
      <ExampleCards @select="emit('example', $event)" />
    </section>
    <div v-else class="message-list__inner">
      <template v-for="entry in timeline" :key="entry.id">
        <UserBubble v-if="entry.kind === 'user' && entry.user" :cid="cid" :message="entry.user" />
        <TextBubble v-else-if="entry.item?.type === 'text'" :content="entry.item.content" :streaming="entry.item.streaming" />
        <ToolCard v-else-if="entry.item?.type === 'tool'" :item="entry.item" @open-file="emit('openFile', $event)" />
        <div v-else-if="entry.item?.type === 'notice'" class="chat-notice">{{ entry.item.text }}</div>
        <div v-else-if="entry.item?.type === 'model_changed'" class="chat-notice chat-notice--model">
          已切换为 <strong>{{ entry.item.to }}</strong>，下一次执行生效
        </div>
        <RunEndCard
          v-else-if="entry.item?.type === 'run_end'"
          :item="entry.item"
          :can-retry="chat.isLatestTerminal(cid, entry.item.runId)"
          @retry="retry(entry.item.runId)"
          @open-file="emit('openFile', $event)"
        />
      </template>
    </div>
  </div>
</template>
