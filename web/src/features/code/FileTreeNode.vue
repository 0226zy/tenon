<script setup lang="ts">
import { computed } from 'vue'
import { ChevronDown, ChevronRight, File, Folder, FolderOpen, Link2, LoaderCircle } from 'lucide-vue-next'
import type { FileEntry } from '@/types/contract'
import { useWorkspaceStore } from '@/stores/workspace'
import { useChatStore } from '@/stores/chat'

defineOptions({ name: 'FileTreeNode' })
const props = defineProps<{ cid: string; entry: FileEntry; depth: number }>()
const workspace = useWorkspaceStore()
const chat = useChatStore()
const isDir = computed(() => props.entry.type === 'dir')
const dir = computed(() => workspace.ensure(props.cid).dirs[props.entry.path])
const expanded = computed(() => !!dir.value?.expanded)
const changedFiles = computed(() => chat.ensure(props.cid).changedFiles)
const changed = computed(() => changedFiles.value.has(props.entry.path))
const containsChanged = computed(() => isDir.value && [...changedFiles.value].some((path) => path.startsWith(`${props.entry.path}/`)))
const selected = computed(() => workspace.ensure(props.cid).openPath === props.entry.path)

async function activate(): Promise<void> {
  if (isDir.value) await workspace.expand(props.cid, props.entry.path)
  else if (props.entry.type === 'file') await workspace.openFile(props.cid, props.entry.path)
}
</script>

<template>
  <div class="file-tree-node">
    <button
      class="file-tree-row"
      :class="{ 'file-tree-row--selected': selected, 'file-tree-row--disabled': entry.type === 'symlink' }"
      :style="{ '--tree-depth': depth }"
      type="button"
      :aria-expanded="isDir ? expanded : undefined"
      :title="entry.type === 'symlink' ? '符号链接仅列出，不跟随打开' : entry.path"
      @click="activate"
    >
      <span class="file-tree-row__chevron">
        <LoaderCircle v-if="dir?.loading" :size="13" class="spin-animation" />
        <ChevronDown v-else-if="isDir && expanded" :size="13" />
        <ChevronRight v-else-if="isDir" :size="13" />
      </span>
      <FolderOpen v-if="isDir && expanded" :size="15" class="file-tree-row__folder" />
      <Folder v-else-if="isDir" :size="15" class="file-tree-row__folder" />
      <Link2 v-else-if="entry.type === 'symlink'" :size="14" />
      <File v-else :size="14" />
      <span class="file-tree-row__name">{{ entry.name }}</span>
      <span v-if="changed" class="change-dot change-dot--exact" title="本轮有改动" aria-label="本轮有改动" />
      <span v-else-if="containsChanged" class="change-dot change-dot--parent" title="包含改动文件" aria-label="包含改动文件" />
    </button>
    <div v-if="isDir && expanded && dir?.loaded" class="file-tree-children">
      <FileTreeNode v-for="child in dir.entries" :key="child.path" :cid="cid" :entry="child" :depth="depth + 1" />
      <p v-if="!dir.entries.length" class="file-tree-empty" :style="{ '--tree-depth': depth + 1 }">空目录</p>
    </div>
    <p v-if="isDir && expanded && dir?.error" class="file-tree-error" role="alert">目录加载失败，请收起后重试</p>
  </div>
</template>
