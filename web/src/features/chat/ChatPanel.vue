<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useChatStore } from '@/stores/chat'
import { useWorkspaceStore } from '@/stores/workspace'
import { useUiStore } from '@/stores/ui'
import type { ExamplePrompt } from '@/constants/examples'
import StatusBar from './StatusBar.vue'
import MessageList from './MessageList.vue'
import Composer from './Composer.vue'

const props = defineProps<{ cid: string }>()
const chat = useChatStore()
const workspace = useWorkspaceStore()
const ui = useUiStore()
const composer = ref<InstanceType<typeof Composer>>()
const view = computed(() => chat.ensure(props.cid))

async function useExample(example: ExamplePrompt): Promise<void> {
  chat.setDraft(props.cid, example.prompt)
  await nextTick()
  composer.value?.focus()
}
async function openFile(path: string): Promise<void> {
  ui.showWorkspace('code')
  await workspace.openFile(props.cid, path)
  if (workspace.openFileState(props.cid)?.deleted) ui.push('文件不存在', 'error')
}
</script>

<template>
  <section class="chat-panel" aria-label="对话工作区">
    <StatusBar :status="view.runStatus" :progress="view.progress" :connection="view.connection" />
    <MessageList :cid="cid" @example="useExample" @open-file="openFile" />
    <Composer ref="composer" :cid="cid" />
  </section>
</template>
