<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { ArrowUp, LoaderCircle, Square } from 'lucide-vue-next'
import { useChatStore } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { ApiError } from '@/api/errors'
import { isRunActive } from '@/types/contract'
import ModelSelect from './ModelSelect.vue'

const props = defineProps<{ cid: string }>()
const chat = useChatStore()
const ui = useUiStore()
const textarea = ref<HTMLTextAreaElement>()
const submitting = ref(false)
const cancelling = ref(false)
const view = computed(() => chat.ensure(props.cid))
const active = computed(() => isRunActive(view.value.runStatus))
const draft = computed({
  get: () => chat.drafts[props.cid] || '',
  set: (value: string) => chat.setDraft(props.cid, value),
})
const charCount = computed(() => [...draft.value].length)
const inputDisabled = computed(() => active.value || submitting.value)
const sendDisabled = computed(() => submitting.value || !draft.value.trim() || charCount.value > 20_000)
const buttonText = computed(() => {
  if (submitting.value) return '发送中'
  if (!active.value) return '发送'
  if (view.value.runStatus === 'cancelling') return '取消中…'
  if (view.value.runStatus === 'finalizing') return '收尾中…'
  return '取消'
})

function resize(): void {
  if (!textarea.value) return
  textarea.value.style.height = 'auto'
  textarea.value.style.height = `${Math.min(180, Math.max(48, textarea.value.scrollHeight))}px`
}
function focus(): void { nextTick(() => { textarea.value?.focus(); resize() }) }
function onKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return
  event.preventDefault()
  void send()
}
async function send(): Promise<void> {
  if (active.value || sendDisabled.value) return
  const content = draft.value
  submitting.value = true
  try {
    await chat.send(props.cid, content)
  } catch (error) {
    if (error instanceof ApiError && (error.code === 'EMPTY' || error.code === 'TOO_LONG')) {
      chat.setDraft(props.cid, content)
    }
    ui.error(error)
  } finally {
    submitting.value = false
    focus()
  }
}
async function cancel(): Promise<void> {
  if (cancelling.value || !view.value.runId) return
  cancelling.value = true
  try { await chat.cancel(props.cid) } catch (error) { ui.error(error) } finally { cancelling.value = false }
}
function onPrimary(): void { if (active.value) void cancel(); else void send() }

watch(draft, resize)
watch(() => props.cid, focus)
onMounted(focus)
defineExpose({ focus })
</script>

<template>
  <div class="composer-wrap">
    <div class="composer" :class="{ 'composer--disabled': inputDisabled }">
      <textarea
        ref="textarea"
        v-model="draft"
        rows="1"
        :disabled="inputDisabled"
        :placeholder="active ? '本轮执行完成后，可继续追加需求…' : '描述你想构建或修改的内容…'"
        aria-label="需求内容"
        @keydown="onKeydown"
        @input="resize"
      />
      <div class="composer__toolbar">
        <ModelSelect :cid="cid" :disabled="inputDisabled" />
        <span class="composer__count" :class="{ 'composer__count--over': charCount > 20000 }">{{ charCount.toLocaleString() }} / 20,000</span>
        <button
          class="composer__send"
          :class="{ 'composer__send--cancel': active }"
          type="button"
          :disabled="active ? cancelling || view.runStatus === 'cancelling' || view.runStatus === 'finalizing' : sendDisabled"
          :aria-label="buttonText"
          @click="onPrimary"
        >
          <LoaderCircle v-if="submitting || cancelling || view.runStatus === 'cancelling'" :size="16" class="spin-animation" />
          <Square v-else-if="active" :size="14" fill="currentColor" />
          <ArrowUp v-else :size="18" />
          <span>{{ buttonText }}</span>
        </button>
      </div>
    </div>
    <div class="composer__hint"><span>Enter 发送 · Shift + Enter 换行</span><span>内容由 Agent 生成，请注意验证</span></div>
  </div>
</template>
