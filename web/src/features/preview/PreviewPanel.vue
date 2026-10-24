<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue'
import { ExternalLink, RefreshCw, RotateCcw, ScrollText } from 'lucide-vue-next'
import { usePreviewStore } from '@/stores/preview'
import { useChatStore } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { isRunActive } from '@/types/contract'
import { ApiError } from '@/api/errors'
import PreviewStatus from './PreviewStatus.vue'
import PreviewFrame from './PreviewFrame.vue'
import LogDrawer from './LogDrawer.vue'

const props = defineProps<{ cid: string }>()
const preview = usePreviewStore()
const chat = useChatStore()
const ui = useUiStore()
const state = computed(() => preview.ensure(props.cid))
const active = computed(() => isRunActive(chat.ensure(props.cid).runStatus))
let wakeTimer: number | undefined

function scheduleWake(): void {
  if (wakeTimer) window.clearTimeout(wakeTimer)
  wakeTimer = window.setTimeout(() => {
    wakeTimer = undefined
    void refresh()
  }, 5000)
}

async function refresh(showError = false): Promise<void> {
  if (wakeTimer) window.clearTimeout(wakeTimer)
  wakeTimer = undefined
  try {
    await preview.refresh(props.cid)
    if (state.value.status === 'starting') scheduleWake()
  } catch (error) {
    if (error instanceof ApiError && error.code === 'SANDBOX_STARTING') {
      state.value.status = 'starting'
      scheduleWake()
    } else if (showError) ui.error(error)
  }
}
async function restart(): Promise<void> {
  try { await preview.restart(props.cid) } catch (error) { ui.error(error) }
}
async function openNewWindow(): Promise<void> {
  try { await preview.openNewWindow(props.cid) } catch (error) { ui.error(error) }
}

watch(() => props.cid, () => { void refresh() }, { immediate: true })
onBeforeUnmount(() => { if (wakeTimer) window.clearTimeout(wakeTimer) })
</script>

<template>
  <div class="preview-panel">
    <div class="preview-toolbar">
      <PreviewStatus :state="state" />
      <div class="preview-toolbar__actions">
        <button class="icon-button" type="button" title="刷新预览状态" aria-label="刷新预览状态" :disabled="state.loading" @click="refresh(true)"><RefreshCw :size="15" :class="{ 'spin-animation': state.loading }" /></button>
        <button class="icon-button" type="button" title="查看日志" aria-label="查看日志" @click="preview.loadLogs(cid)"><ScrollText :size="15" /></button>
        <button class="icon-button" type="button" title="重启预览" aria-label="重启预览" :disabled="active || state.restarting || state.status === 'starting'" @click="restart"><RotateCcw :size="15" /></button>
        <button class="button button--secondary button--small" type="button" :disabled="!state.displayedUrl" @click="openNewWindow"><ExternalLink :size="14" />新窗口</button>
      </div>
    </div>
    <PreviewFrame :cid="cid" @restart="restart" @logs="preview.loadLogs(cid)" />
    <p class="preview-cookie-hint">页面空白或受限？浏览器可能拦截了第三方 Cookie，请使用「新窗口」预览。</p>
    <LogDrawer :cid="cid" />
  </div>
</template>
