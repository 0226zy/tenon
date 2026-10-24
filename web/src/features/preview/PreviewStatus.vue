<script setup lang="ts">
import { computed } from 'vue'
import { AlertCircle, CheckCircle2, CircleDashed, LoaderCircle } from 'lucide-vue-next'
import type { PreviewConvState } from '@/stores/preview'
import { runErrorMessage } from '@/api/errors'

const props = defineProps<{ state: PreviewConvState }>()
const view = computed(() => {
  const state = props.state
  if (state.restarting || state.status === 'starting') return { label: '构建中', detail: '正在准备可运行预览', icon: LoaderCircle, kind: 'starting' }
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
