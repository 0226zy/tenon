<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { AlertCircle, CheckCircle2, CircleDashed, LoaderCircle } from 'lucide-vue-next'
import type { PreviewConvState } from '@/stores/preview'
import { runErrorMessage } from '@/api/errors'

const props = defineProps<{ state: PreviewConvState }>()
const now = ref(Date.now())
let clock: number | undefined

onMounted(() => {
  clock = window.setInterval(() => { now.value = Date.now() }, 1000)
})
onBeforeUnmount(() => {
  if (clock !== undefined) window.clearInterval(clock)
})

const starting = computed(() => props.state.restarting || props.state.status === 'starting')
const phaseLabel = computed(() => {
  if (!starting.value) return ''
  switch (props.state.phase) {
    case 'building': return '构建中'
    case 'starting': return '启动中'
    case 'healthcheck': return '健康检查'
    default: return '准备中'
  }
})
const elapsed = computed(() => {
  if (!starting.value || !props.state.phaseStartedAt) return ''
  const seconds = Math.max(0, Math.floor((now.value - props.state.phaseStartedAt) / 1000))
  return seconds < 1 ? '' : `${seconds}s`
})
const view = computed(() => {
  const state = props.state
  if (starting.value) {
    return {
      label: phaseLabel.value,
      detail: elapsed.value ? `${elapsed.value} · 正在准备可运行预览` : '正在准备可运行预览',
      icon: LoaderCircle,
      kind: 'starting',
    }
  }
  if (state.status === 'ready') return { label: '预览可用', detail: '当前为已确认版本', icon: CheckCircle2, kind: 'ready' }
  if (state.status === 'failed') return { label: '构建失败', detail: runErrorMessage(state.errorCode || 'BUILD_FAILED'), icon: AlertCircle, kind: 'failed' }
  return { label: '尚未生成', detail: '完成第一轮任务后自动构建', icon: CircleDashed, kind: 'none' }
})
</script>

<template>
  <div class="preview-status" :class="`preview-status--${view.kind}`" role="status">
    <component :is="view.icon" :size="15" :class="{ 'spin-animation': view.kind === 'starting' }" />
    <strong>{{ view.label }}</strong>
    <span v-if="state.stale" class="preview-stale" title="最近完成版本尚未部署到预览">旧版本</span>
    <span class="preview-status__detail">{{ view.detail }}</span>
    <span v-if="state.displayedCommit" class="preview-status__version" :title="`generation ${state.displayedGeneration}`">
      {{ state.displayedCommit.slice(0, 7) }} · #{{ state.displayedGeneration }}
    </span>
  </div>
</template>
