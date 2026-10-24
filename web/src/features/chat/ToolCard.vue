<script setup lang="ts">
import { computed, ref } from 'vue'
import { AlertCircle, Check, ChevronDown, ChevronRight, FileCode2, LoaderCircle, TerminalSquare } from 'lucide-vue-next'
import type { ToolItem } from '@/events/reducer'
import { isBashTool, toolFilePath } from '@/utils/tools'

const props = defineProps<{ item: ToolItem }>()
const emit = defineEmits<{ openFile: [path: string] }>()
const expanded = ref(false)
const name = computed(() => props.item.call?.name || props.item.result?.name || 'tool')
const title = computed(() => props.item.call?.summary || (props.item.callId ? `调用 ${props.item.callId}` : name.value))
const failed = computed(() => !!props.item.result?.is_error)
const bash = computed(() => isBashTool(name.value))
const filePath = computed(() => toolFilePath(props.item))
</script>

<template>
  <section class="tool-card" :class="{ 'tool-card--failed': failed }">
    <div class="tool-card__header">
      <button class="tool-card__toggle" type="button" :aria-expanded="expanded" @click="expanded = !expanded">
        <ChevronDown v-if="expanded" :size="15" /><ChevronRight v-else :size="15" />
        <TerminalSquare v-if="bash" :size="15" /><FileCode2 v-else :size="15" />
        <span class="tool-card__name">{{ name }}</span>
        <span class="tool-card__summary" :title="title">{{ title }}</span>
      </button>
      <span class="tool-card__state" :class="{ 'tool-card__state--error': failed }">
        <AlertCircle v-if="failed" :size="12" /><Check v-else-if="item.result" :size="12" /><LoaderCircle v-else :size="12" class="spin-animation" />
        {{ failed ? '失败' : item.result ? '已完成' : '执行中' }}
      </span>
      <button v-if="filePath" class="tool-card__open" type="button" title="在代码工作区打开" @click="emit('openFile', filePath)">查看代码</button>
    </div>
    <div v-if="item.result?.truncated" class="tool-card__truncated">输出已截断，仅展示服务端保留的部分</div>
    <div v-if="expanded" class="tool-card__details">
      <template v-if="item.call">
        <h4>调用参数</h4>
        <pre :class="{ 'tool-terminal': bash }"><code>{{ item.call.args || '{}' }}</code></pre>
      </template>
      <template v-if="item.result">
        <h4>工具输出</h4>
        <pre :class="{ 'tool-terminal': bash }"><code>{{ item.result.content || '（无输出）' }}</code></pre>
      </template>
      <p v-else class="tool-card__waiting">等待工具返回结果…</p>
    </div>
  </section>
</template>
