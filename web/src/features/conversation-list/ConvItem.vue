<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useRouter } from 'vue-router'
import { AlertCircle, Check, Clock3, LoaderCircle, Pencil, Trash2, X } from 'lucide-vue-next'
import type { ConversationView } from '@/types/contract'
import { RUN_LABELS, conversationTitle, relativeTime } from '@/utils/format'
import { useConversationsStore } from '@/stores/conversations'
import { useUiStore } from '@/stores/ui'
import { errorMessage } from '@/api/errors'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const props = defineProps<{ conversation: ConversationView; active: boolean }>()
const emit = defineEmits<{ select: []; deleted: [] }>()
const conversations = useConversationsStore()
const ui = useUiStore()
const router = useRouter()
const editing = ref(false)
const draft = ref('')
const editError = ref('')
const saving = ref(false)
const deleting = ref(false)
const confirmOpen = ref(false)
const input = ref<HTMLInputElement>()

const status = computed(() => {
  const conversation = props.conversation
  if (conversation.status === 'error') return { label: '异常', kind: 'error', icon: AlertCircle }
  if (conversation.status === 'queued') return { label: '排队中', kind: 'queued', icon: Clock3 }
  if (conversation.status === 'running') {
    const runStatus = conversation.run?.status
    return { label: runStatus ? RUN_LABELS[runStatus] : '执行中', kind: 'running', icon: LoaderCircle }
  }
  return null
})

function select(): void { if (!editing.value) emit('select') }
async function startEdit(): Promise<void> {
  draft.value = props.conversation.title
  editError.value = ''
  editing.value = true
  await nextTick()
  input.value?.focus()
  input.value?.select()
}
function cancelEdit(): void {
  editing.value = false
  editError.value = ''
}
async function save(): Promise<void> {
  const title = draft.value.trim()
  if (!title || title.length > 128) {
    editError.value = '标题需为 1–128 个字符'
    return
  }
  if (title === props.conversation.title) { cancelEdit(); return }
  saving.value = true
  editError.value = ''
  try {
    await conversations.rename(props.conversation.id, title)
    editing.value = false
  } catch (error) {
    editError.value = error instanceof Error && !(('status' in error)) ? error.message : errorMessage(error)
  } finally {
    saving.value = false
  }
}
async function remove(): Promise<void> {
  deleting.value = true
  try {
    await conversations.remove(props.conversation.id)
    confirmOpen.value = false
    ui.push('对话已删除', 'success')
    emit('deleted')
    if (props.active) await router.replace('/')
  } catch (error) {
    ui.error(error)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="conv-item" :class="{ 'conv-item--active': active }">
    <div v-if="editing" class="conv-item__edit">
      <input
        ref="input"
        v-model="draft"
        class="text-input"
        maxlength="128"
        aria-label="对话标题"
        :disabled="saving"
        @keydown.enter.prevent="save"
        @keydown.esc.prevent="cancelEdit"
      />
      <div class="conv-item__edit-actions">
        <span v-if="editError" class="conv-item__error" role="alert">{{ editError }}</span>
        <button class="icon-button" type="button" aria-label="保存标题" :disabled="saving" @click="save"><Check :size="15" /></button>
        <button class="icon-button" type="button" aria-label="取消重命名" :disabled="saving" @click="cancelEdit"><X :size="15" /></button>
      </div>
    </div>
    <button v-else class="conv-item__main" type="button" :aria-current="active ? 'page' : undefined" @click="select">
      <span class="conv-item__title">{{ conversationTitle(conversation) }}</span>
      <span class="conv-item__meta">
        <span>{{ relativeTime(conversation.last_active || conversation.created_at) }}</span>
        <span v-if="status" class="status-badge" :class="`status-badge--${status.kind}`">
          <component :is="status.icon" :size="11" :class="{ 'spin-animation': status.kind === 'running' }" />
          {{ status.label }}
        </span>
      </span>
    </button>
    <div v-if="!editing" class="conv-item__actions">
      <button class="icon-button" type="button" aria-label="重命名对话" title="重命名" @click.stop="startEdit"><Pencil :size="14" /></button>
      <button class="icon-button icon-button--danger" type="button" aria-label="删除对话" title="删除" @click.stop="confirmOpen = true"><Trash2 :size="14" /></button>
    </div>
    <ConfirmDialog
      v-if="confirmOpen"
      title="删除这个对话？"
      description="删除后对话记录与工作区不可恢复。进行中的任务会先取消。"
      confirm-text="删除对话"
      :busy="deleting"
      @cancel="confirmOpen = false"
      @confirm="remove"
    />
  </div>
</template>
