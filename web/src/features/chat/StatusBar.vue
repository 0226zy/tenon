<script setup lang="ts">
import { computed } from 'vue'
import { Archive, Clock3, LoaderCircle, Octagon, WifiOff } from 'lucide-vue-next'
import { isRunActive, type RunStatus } from '@/types/contract'
import type { SseConnectionState } from '@/events/reducer'

const props = defineProps<{
  status?: RunStatus
  progress?: string
  connection: SseConnectionState
}>()

const content = computed(() => {
  if (props.connection === 'retrying') return { label: '连接中断，正在重连', detail: '恢复后会自动补齐记录', icon: WifiOff, kind: 'offline' }
  if (!isRunActive(props.status)) return null
  if (props.status === 'queued') return { label: '排队中', detail: '资源空闲后自动开始', icon: Clock3, kind: 'queued' }
  if (props.status === 'cancelling') return { label: '取消中', detail: '确认停止后才会结束本轮任务', icon: Octagon, kind: 'cancelling' }
  if (props.status === 'finalizing') return { label: '收尾中', detail: '正在保存本轮改动', icon: Archive, kind: 'finalizing' }
  if (props.status === 'dispatching') return { label: '准备执行', detail: '正在准备工作区', icon: LoaderCircle, kind: 'running' }
  return { label: '执行中', detail: props.progress || 'Agent 正在处理你的需求', icon: LoaderCircle, kind: 'running' }
})
</script>

<template>
  <div v-if="content" class="run-status-bar" :class="`run-status-bar--${content.kind}`" role="status">
    <component :is="content.icon" :size="16" :class="{ 'spin-animation': content.kind === 'running' }" />
    <strong>{{ content.label }}</strong>
    <span>{{ content.detail }}</span>
    <span v-if="connection === 'connecting'" class="run-status-bar__connection">正在连接事件流…</span>
  </div>
</template>
