<script setup lang="ts">
import { computed } from 'vue'
import { LoaderCircle, RefreshCw, X } from 'lucide-vue-next'
import { usePreviewStore } from '@/stores/preview'

const props = defineProps<{ cid: string }>()
const preview = usePreviewStore()
const state = computed(() => preview.ensure(props.cid))
</script>

<template>
  <section v-if="state.logsOpen" class="log-drawer" aria-label="预览日志">
    <header><div><strong>预览日志</strong><span>最近 200 行</span><em v-if="state.logsTruncated">已达 128 KB 上限</em></div><div><button class="icon-button" type="button" aria-label="刷新日志" @click="preview.loadLogs(cid)"><RefreshCw :size="14" /></button><button class="icon-button" type="button" aria-label="关闭日志" @click="preview.closeLogs(cid)"><X :size="16" /></button></div></header>
    <div v-if="state.logsLoading" class="log-drawer__loading"><LoaderCircle :size="17" class="spin-animation" />正在读取日志…</div>
    <pre v-else class="log-drawer__content"><code>{{ state.logs || '暂无日志' }}</code></pre>
  </section>
</template>
