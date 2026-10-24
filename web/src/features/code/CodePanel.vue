<script setup lang="ts">
import { watch } from 'vue'
import { useWorkspaceStore } from '@/stores/workspace'
import { useChatStore } from '@/stores/chat'
import FileTree from './FileTree.vue'
import EditorPane from './EditorPane.vue'

const props = defineProps<{ cid: string }>()
const workspace = useWorkspaceStore()
const chat = useChatStore()

watch(() => props.cid, async (cid) => {
  workspace.ensure(cid)
  await workspace.ensureDefault(cid, chat.ensure(cid).changedFiles)
}, { immediate: true })
</script>

<template>
  <div class="code-panel">
    <FileTree :cid="cid" />
    <EditorPane :cid="cid" />
  </div>
</template>
