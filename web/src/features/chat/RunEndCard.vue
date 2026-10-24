<script setup lang="ts">
import { computed, ref } from 'vue'
import { AlertCircle, CheckCircle2, ChevronDown, ChevronRight, CircleStop, FileCode2, RotateCcw } from 'lucide-vue-next'
import type { RunEndItem } from '@/events/reducer'
import { formatDuration, formatTokens } from '@/utils/format'
import { runErrorMessage } from '@/api/errors'

const props = defineProps<{ item: RunEndItem; canRetry: boolean }>()
const emit = defineEmits<{ retry: []; openFile: [path: string] }>()
const expanded = ref(false)
const shortCommit = computed(() => props.item.data.commit?.slice(0, 7))
const tokenCount = computed(() => props.item.data.usage.prompt + props.item.data.usage.completion)
const headline = computed(() => {
  const data = props.item.data
  if (props.item.status === 'finished') {
    const parts = ['完成', `改动 ${data.files_total} 个文件`]
    if (shortCommit.value) parts.push(`版本 ${shortCommit.value}`)
    parts.push(`用时 ${formatDuration(data.duration_ms)}`, `${formatTokens(tokenCount.value)} tokens`)
    return parts.join(' · ')
  }
  if (props.item.status === 'cancelled') {
    return shortCommit.value ? `已取消 · 已保存版本 ${shortCommit.value}` : '已取消 · 无改动'
  }
  return `执行失败 · ${runErrorMessage(data.error_code)}`
})
const icon = computed(() => props.item.status === 'finished' ? CheckCircle2 : props.item.status === 'cancelled' ? CircleStop : AlertCircle)
const changeLabels = { A: '新增', M: '修改', D: '删除' }
</script>

<template>
  <section class="run-end-card" :class="`run-end-card--${item.status}`">
    <div class="run-end-card__summary">
      <component :is="icon" :size="18" />
      <div><strong>{{ headline }}</strong><p v-if="item.data.checkpoint_status === 'failed'">改动未保存为版本</p></div>
      <button v-if="canRetry" class="button button--secondary button--small" type="button" @click="emit('retry')"><RotateCcw :size="14" />重新执行</button>
    </div>
    <button v-if="item.data.files.length" class="run-end-card__files-toggle" type="button" :aria-expanded="expanded" @click="expanded = !expanded">
      <ChevronDown v-if="expanded" :size="14" /><ChevronRight v-else :size="14" />查看本轮文件
      <span v-if="item.data.files_total > item.data.files.length">（展示前 {{ item.data.files.length }} / {{ item.data.files_total }} 个）</span>
    </button>
    <div v-if="expanded" class="run-end-card__files">
      <button v-for="file in item.data.files" :key="`${file.path}:${file.change}`" type="button" @click="emit('openFile', file.path)">
        <FileCode2 :size="14" /><span>{{ file.path }}</span><em :class="`change-${file.change.toLowerCase()}`">{{ changeLabels[file.change] || file.change }}</em>
      </button>
    </div>
  </section>
</template>
