<script setup lang="ts">
import { computed } from 'vue'
import { AlertCircle, LoaderCircle, MonitorPlay, MousePointer2 } from 'lucide-vue-next'
import { usePreviewStore } from '@/stores/preview'
import { runErrorMessage } from '@/api/errors'

type PreviewDevice = 'desktop' | 'tablet' | 'mobile'

const props = withDefaults(defineProps<{ cid: string; device?: PreviewDevice }>(), { device: 'desktop' })
const preview = usePreviewStore()
const state = computed(() => preview.ensure(props.cid))
const emit = defineEmits<{ restart: []; logs: [] }>()
</script>

<template>
  <div class="preview-frame">
    <div v-if="state.displayedUrl" class="preview-frame__canvas" :class="`preview-frame__canvas--${device}`">
      <iframe
        :key="state.displayedUrl"
        :src="state.displayedUrl"
        class="preview-frame__iframe"
        title="生成应用预览"
        sandbox="allow-scripts allow-forms allow-same-origin allow-popups"
        referrerpolicy="no-referrer"
      />
    </div>
    <div v-if="!state.displayedUrl && state.status === 'none'" class="preview-empty">
      <span class="preview-empty__icon"><MonitorPlay :size="29" /></span>
      <strong>预览会出现在这里</strong>
      <p>第一轮任务完成后，平台会自动构建并启动应用。</p>
      <span class="preview-empty__hint"><MousePointer2 :size="14" />先在左侧描述你的产品想法</span>
    </div>
    <div v-else-if="!state.displayedUrl && state.status === 'failed'" class="preview-empty preview-empty--error" role="alert">
      <AlertCircle :size="29" /><strong>预览构建失败</strong><p>{{ runErrorMessage(state.errorCode || 'BUILD_FAILED') }}</p>
      <div><button class="button button--secondary button--small" type="button" @click="emit('logs')">查看日志</button><button class="button button--primary button--small" type="button" @click="emit('restart')">重启预览</button></div>
    </div>
    <div v-if="state.displayedUrl && state.status === 'failed'" class="preview-fallback" role="status">
      <AlertCircle :size="14" /><span>新版本构建失败，仍展示上一个可用版本</span>
      <button type="button" @click="emit('logs')">查看日志</button>
    </div>
    <div v-if="state.restarting || state.status === 'starting'" class="preview-loading" role="status">
      <LoaderCircle :size="24" class="spin-animation" />
      <strong>{{ state.restarting ? '正在重启预览' : state.phase === 'building' ? '正在构建预览' : state.phase === 'healthcheck' ? '正在检查预览' : '正在启动预览' }}</strong>
      <p>通常需要几十秒，完成后会自动刷新</p>
    </div>
  </div>
</template>
