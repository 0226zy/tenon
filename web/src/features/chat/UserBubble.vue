<script setup lang="ts">
import { AlertCircle, LoaderCircle, RotateCcw } from 'lucide-vue-next'
import type { UserMessageState } from '@/stores/chat'
import { useChatStore } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { errorMessage } from '@/api/errors'

const props = defineProps<{ cid: string; message: UserMessageState }>()
const chat = useChatStore()
const ui = useUiStore()

async function retry(): Promise<void> {
  try { await chat.retryMessage(props.cid, props.message.id) } catch (error) { ui.error(error) }
}
</script>

<template>
  <article class="user-message" :class="{ 'user-message--failed': message.status === 'failed' }">
    <div class="user-message__bubble">{{ message.content }}</div>
    <div v-if="message.status === 'sending'" class="user-message__status" role="status"><LoaderCircle :size="12" class="spin-animation" />发送中</div>
    <div v-else-if="message.status === 'failed'" class="user-message__status user-message__status--error" role="alert">
      <AlertCircle :size="13" /><span>发送失败 · {{ errorMessage(message.error) }}</span>
      <button type="button" @click="retry"><RotateCcw :size="12" />重试</button>
    </div>
  </article>
</template>
