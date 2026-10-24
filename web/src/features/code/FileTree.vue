<script setup lang="ts">
import { computed } from 'vue'
import { FolderTree, LoaderCircle, RefreshCw } from 'lucide-vue-next'
import { useWorkspaceStore } from '@/stores/workspace'
import { useUiStore } from '@/stores/ui'
import FileTreeNode from './FileTreeNode.vue'

const props = defineProps<{ cid: string }>()
const workspace = useWorkspaceStore()
const ui = useUiStore()
const state = computed(() => workspace.ensure(props.cid))
const root = computed(() => state.value.dirs[''])
const starting = computed(() => Object.keys(state.value.starting).some((key) => !key.startsWith('file:')))

async function refresh(): Promise<void> {
  try { await workspace.refreshAll(props.cid) } catch (error) { ui.error(error) }
}
</script>

<template>
  <section class="file-tree" aria-label="项目文件">
    <header class="file-tree__header"><span><FolderTree :size="14" />项目文件</span><button class="icon-button" type="button" aria-label="刷新文件树" title="刷新" @click="refresh"><RefreshCw :size="13" /></button></header>
    <div v-if="starting" class="workspace-starting" role="status"><LoaderCircle :size="14" class="spin-animation" /><span>工作区启动中<small>每 3 秒自动重试，最长 60 秒</small></span></div>
    <div class="file-tree__scroll">
      <div v-if="root.loading && !root.loaded" class="tree-loading"><LoaderCircle :size="16" class="spin-animation" />正在读取目录…</div>
      <div v-else-if="root.error && !root.loaded" class="tree-error" role="alert"><p>目录加载失败</p><button type="button" @click="workspace.loadDir(cid, '', Date.now(), true)">重试</button></div>
      <template v-else>
        <FileTreeNode v-for="entry in root.entries" :key="entry.path" :cid="cid" :entry="entry" :depth="0" />
        <p v-if="root.loaded && !root.entries.length" class="file-tree-empty file-tree-empty--root">工作区暂无文件</p>
      </template>
    </div>
    <footer class="file-tree__legend"><span class="change-dot change-dot--exact" />本轮改动</footer>
  </section>
</template>
