<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ExternalLink, Maximize2, Monitor, RefreshCw, RotateCcw, ScrollText, Smartphone, Tablet } from 'lucide-vue-next'
import { usePreviewStore } from '@/stores/preview'
import { useChatStore } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { isRunActive } from '@/types/contract'
import { ApiError } from '@/api/errors'
import PreviewStatus from './PreviewStatus.vue'
import PreviewFrame from './PreviewFrame.vue'
import LogDrawer from './LogDrawer.vue'

type PreviewDevice = 'desktop' | 'tablet' | 'mobile'

const props = defineProps<{ cid: string }>()
const preview = usePreviewStore()
const chat = useChatStore()
const ui = useUiStore()
const state = computed(() => preview.ensure(props.cid))
const active = computed(() => isRunActive(chat.ensure(props.cid).runStatus))
const device = ref<PreviewDevice>('desktop')
const stage = ref<HTMLElement>()
let wakeTimer: number | undefined
let logTimer: number | undefined

function scheduleWake(): void {
  if (wakeTimer) window.clearTimeout(wakeTimer)
  wakeTimer = window.setTimeout(() => {
    wakeTimer = undefined
    void refresh()
  }, 5000)
}

function syncLogTail(): void {
  if (logTimer !== undefined) window.clearInterval(logTimer)
  logTimer = undefined
  if (!state.value.logsOpen) return
  if (state.value.status !== 'starting' && !state.value.restarting) return
  void preview.loadLogs(props.cid, true)
  logTimer = window.setInterval(() => { void preview.loadLogs(props.cid, true) }, 2000)
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
      state.value.phase = 'starting'
      state.value.phaseStartedAt ||= Date.now()
      scheduleWake()
    } else if (showError) ui.error(error)
  } finally {
    syncLogTail()
  }
}

async function restart(): Promise<void> {
  try { await preview.restart(props.cid) } catch (error) { ui.error(error) }
}

async function openNewWindow(): Promise<void> {
  try { await preview.openNewWindow(props.cid) } catch (error) { ui.error(error) }
}

async function toggleFullscreen(): Promise<void> {
  try {
    if (document.fullscreenElement) await document.exitFullscreen()
    else await stage.value?.requestFullscreen()
  } catch (error) {
    ui.error(error)
  }
}

function chooseDevice(next: PreviewDevice): void {
  device.value = next
}

watch(() => props.cid, () => { void refresh() }, { immediate: true })
watch(() => [state.value.logsOpen, state.value.status] as const, syncLogTail)
onBeforeUnmount(() => {
  if (wakeTimer) window.clearTimeout(wakeTimer)
  if (logTimer !== undefined) window.clearInterval(logTimer)
})
</script>

<template>
  <div class="preview-panel">
    <div class="preview-toolbar">
      <PreviewStatus :state="state" />
      <div class="preview-toolbar__devices" role="group" aria-label="预览尺寸">
        <button type="button" :class="{ active: device === 'desktop' }" :aria-pressed="device === 'desktop'" title="桌面" aria-label="桌面尺寸" @click="chooseDevice('desktop')"><Monitor :size="14" /></button>
        <button type="button" :class="{ active: device === 'tablet' }" :aria-pressed="device === 'tablet'" title="平板" aria-label="平板尺寸" @click="chooseDevice('tablet')"><Tablet :size="14" /></button>
        <button type="button" :class="{ active: device === 'mobile' }" :aria-pressed="device === 'mobile'" title="手机" aria-label="手机尺寸" @click="chooseDevice('mobile')"><Smartphone :size="14" /></button>
        <button type="button" title="全屏预览" aria-label="全屏预览" @click="toggleFullscreen"><Maximize2 :size="14" /></button>
      </div>
      <div class="preview-toolbar__actions">
        <button class="icon-button" type="button" title="刷新预览状态" aria-label="刷新预览状态" :disabled="state.loading" @click="refresh(true)"><RefreshCw :size="15" :class="{ 'spin-animation': state.loading }" /></button>
        <button class="icon-button" type="button" title="查看日志" aria-label="查看日志" @click="preview.loadLogs(cid)"><ScrollText :size="15" /></button>
        <button class="icon-button" type="button" title="重启预览" aria-label="重启预览" :disabled="active || state.restarting || state.status === 'starting'" @click="restart"><RotateCcw :size="15" /></button>
        <button class="button button--secondary button--small" type="button" :disabled="!state.displayedUrl" @click="openNewWindow"><ExternalLink :size="14" />新窗口</button>
      </div>
    </div>
    <div ref="stage" class="preview-stage">
      <PreviewFrame :cid="cid" :device="device" @restart="restart" @logs="preview.loadLogs(cid)" />
    </div>
    <p class="preview-cookie-hint">页面空白或受限？浏览器可能拦截了第三方 Cookie，请使用「新窗口」预览。</p>
    <LogDrawer :cid="cid" />
  </div>
</template>
