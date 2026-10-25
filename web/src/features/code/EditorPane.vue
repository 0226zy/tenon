<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { AlertCircle, Binary, FileCode2, LockKeyhole, RefreshCw } from 'lucide-vue-next'
import { useWorkspaceStore } from '@/stores/workspace'
import { errorMessage } from '@/api/errors'
import { fileLanguage, formatBytes } from '@/utils/format'
import { loadMonaco } from './monaco'

const props = defineProps<{ cid: string }>()
const workspace = useWorkspaceStore()
const container = ref<HTMLElement>()
const state = computed(() => workspace.ensure(props.cid))
const file = computed(() => workspace.openFileState(props.cid))
const content = computed(() => file.value?.content)
const normalContent = computed(() => !!content.value && !content.value.binary && !file.value?.error)
let editor: import('monaco-editor').editor.IStandaloneCodeEditor | undefined
let model: import('monaco-editor').editor.ITextModel | undefined
let monaco: typeof import('monaco-editor') | undefined
let disposed = false

function syncModel(): void {
  if (!editor || !monaco || !content.value || content.value.binary) return
  model?.dispose()
  const uri = monaco.Uri.file(`/workspace/app/${content.value.path}`)
  model = monaco.editor.createModel(content.value.content, fileLanguage(content.value.path), uri)
  editor.setModel(model)
}

watch(() => [content.value?.path, content.value?.etag, content.value?.content], syncModel)
onMounted(async () => {
  monaco = await loadMonaco()
  if (disposed || !container.value) return
  monaco.editor.defineTheme('proto-light', {
    base: 'vs', inherit: true,
    rules: [{ token: 'comment', foreground: '89939f', fontStyle: 'italic' }],
    colors: {
      'editor.background': '#FFFFFF', 'editor.foreground': '#263143',
      'editorLineNumber.foreground': '#A5ADB9', 'editorLineNumber.activeForeground': '#596579',
      'editor.lineHighlightBackground': '#F7F8FC', 'editorIndentGuide.background1': '#EDF0F5',
      'editor.selectionBackground': '#DDE2FF',
    },
  })
  editor = monaco.editor.create(container.value, {
    readOnly: true,
    readOnlyMessage: { value: '代码为只读模式，修改请通过左侧需求描述完成' },
    domReadOnly: true,
    automaticLayout: true,
    minimap: { enabled: false },
    fontSize: 13,
    lineHeight: 21,
    fontFamily: '"SFMono-Regular", Consolas, "Liberation Mono", monospace',
    padding: { top: 18, bottom: 24 },
    scrollBeyondLastLine: false,
    smoothScrolling: true,
    folding: true,
    lineNumbersMinChars: 3,
    renderLineHighlight: 'none',
    overviewRulerBorder: false,
    hideCursorInOverviewRuler: true,
    contextmenu: false,
    quickSuggestions: false,
    theme: 'proto-light',
    ariaLabel: '只读代码编辑器',
  })
  syncModel()
})
onBeforeUnmount(() => {
  disposed = true
  model?.dispose()
  editor?.dispose()
})
</script>

<template>
  <section class="editor-pane" aria-label="代码查看器">
    <header class="editor-pane__header">
      <span class="editor-pane__path" :title="state.openPath"><FileCode2 :size="14" />{{ state.openPath || '未选择文件' }}</span>
      <span class="editor-pane__actions">
        <RefreshCw v-if="file?.refreshing" :size="13" class="spin-animation" aria-label="正在刷新" />
        <span class="readonly-badge"><LockKeyhole :size="11" />只读</span>
      </span>
    </header>
    <div v-if="content?.truncated" class="editor-pane__banner">文件内容已截断，仅展示已读取部分</div>
    <div class="editor-pane__body">
      <div v-if="!state.openPath" class="editor-empty"><FileCode2 :size="28" /><strong>选择一个文件查看代码</strong><p>目录在左侧，点击文件即可只读打开。</p></div>
      <div v-else-if="file?.deleted" class="editor-empty editor-empty--error" role="alert"><AlertCircle :size="27" /><strong>文件不存在</strong><p>文件可能已在本轮修改中删除，请刷新文件树。</p><button class="button button--secondary button--small" type="button" @click="workspace.refreshAll(cid)">刷新文件树</button></div>
      <div v-else-if="file?.error" class="editor-empty editor-empty--error" role="alert"><AlertCircle :size="27" /><strong>{{ errorMessage(file.error) }}</strong><p>{{ state.openPath }}</p><button class="button button--secondary button--small" type="button" @click="workspace.openFile(cid, state.openPath, true)">重试</button></div>
      <div v-else-if="content?.binary" class="editor-empty"><Binary :size="29" /><strong>二进制文件</strong><p>不支持在线显示内容</p><dl><div><dt>文件</dt><dd>{{ content.path }}</dd></div><div><dt>大小</dt><dd>{{ formatBytes(content.size) }}</dd></div></dl></div>
      <div v-else-if="file?.loading && !content" class="editor-empty"><span class="spinner spinner--dark" /><strong>正在读取文件…</strong><p v-if="state.starting[`file:${state.openPath}`]">工作区启动中，每 3 秒自动重试</p></div>
      <div v-show="normalContent" ref="container" class="monaco-container" />
    </div>
  </section>
</template>
