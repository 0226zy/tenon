<script setup lang="ts">
import { computed } from 'vue'
import { Sparkles } from 'lucide-vue-next'
import { renderMarkdown } from '@/utils/markdown'

const props = defineProps<{ content: string; streaming?: boolean }>()
const html = computed(() => renderMarkdown(props.content))
</script>

<template>
  <article class="assistant-message">
    <span class="assistant-message__avatar" aria-hidden="true"><Sparkles :size="16" /></span>
    <div class="assistant-message__body">
      <div class="markdown-body" v-html="html" />
      <span v-if="streaming" class="typing-cursor" aria-label="正在生成">▍</span>
    </div>
  </article>
</template>
