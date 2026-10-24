<script setup lang="ts">
import { computed } from 'vue'
import { Code2, ExternalLink, Github, MonitorPlay, Rocket, X } from 'lucide-vue-next'
import { useUiStore } from '@/stores/ui'
import { usePreviewStore } from '@/stores/preview'
import CodePanel from '@/features/code/CodePanel.vue'
import PreviewPanel from '@/features/preview/PreviewPanel.vue'

const props = defineProps<{ cid: string }>()
const ui = useUiStore()
const preview = usePreviewStore()
const previewState = computed(() => props.cid ? preview.ensure(props.cid) : undefined)
</script>

<template>
  <aside class="workspace-panel" aria-label="代码与预览工作区">
    <header class="workspace-panel__header">
      <div class="workspace-tabs" role="tablist" aria-label="工作区视图">
        <button type="button" role="tab" :aria-selected="ui.workspaceTab === 'code'" :class="{ active: ui.workspaceTab === 'code' }" @click="ui.workspaceTab = 'code'"><Code2 :size="15" />代码</button>
        <button type="button" role="tab" :aria-selected="ui.workspaceTab === 'preview'" :class="{ active: ui.workspaceTab === 'preview' }" @click="ui.workspaceTab = 'preview'">
          <MonitorPlay :size="15" />预览
          <span v-if="previewState && previewState.status !== 'none'" class="workspace-tab-status" :class="`workspace-tab-status--${previewState.status}`">{{ previewState.stale ? '旧版本' : previewState.status === 'ready' ? '可用' : previewState.status === 'failed' ? '失败' : '构建中' }}</span>
        </button>
      </div>
      <div class="workspace-panel__actions">
        <span class="disabled-action" title="即将开放"><button type="button" disabled><Github :size="14" />推送</button></span>
        <span class="disabled-action" title="即将开放"><button type="button" disabled><Rocket :size="14" />发布</button></span>
        <button class="icon-button workspace-panel__close" type="button" aria-label="关闭工作区" @click="ui.mobileWorkspaceOpen = false"><X :size="17" /></button>
      </div>
    </header>
    <div v-if="!cid" class="workspace-placeholder">
      <span><Code2 :size="26" /><MonitorPlay :size="26" /></span>
      <strong>代码与预览</strong>
      <p>选择或新建对话后，在这里查看真实代码，并试用生成的应用。</p>
      <span class="workspace-placeholder__hint"><ExternalLink :size="13" />支持独立窗口预览</span>
    </div>
    <template v-else>
      <CodePanel v-if="ui.workspaceTab === 'code'" :cid="cid" />
      <PreviewPanel v-else :cid="cid" />
    </template>
  </aside>
</template>
