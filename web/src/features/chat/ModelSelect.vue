<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronDown, Cpu } from 'lucide-vue-next'
import { useModelsStore } from '@/stores/models'
import { useConversationsStore } from '@/stores/conversations'
import { useChatStore } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { ApiError } from '@/api/errors'

const props = defineProps<{ cid: string; disabled?: boolean }>()
const models = useModelsStore()
const conversations = useConversationsStore()
const chat = useChatStore()
const ui = useUiStore()
const saving = ref(false)
const currentModel = computed(() => chat.modelByConv[props.cid] || conversations.current?.model || models.defaultModel?.id || '')
const selected = ref(currentModel.value)
watch(currentModel, (value) => { selected.value = value })
const selectedView = computed(() => models.byId(selected.value))

async function change(): Promise<void> {
  const previous = currentModel.value
  const next = selected.value
  if (!next || next === previous) return
  saving.value = true
  try {
    await conversations.setModel(props.cid, next)
    chat.setModel(props.cid, next)
  } catch (error) {
    selected.value = previous
    ui.error(error)
    if (error instanceof ApiError && error.code === 'BAD_MODEL') await models.load(true).catch(() => undefined)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="model-select" :title="selectedView?.desc || '选择下一轮使用的模型'">
    <Cpu :size="15" aria-hidden="true" />
    <select v-model="selected" aria-label="执行模型" :disabled="disabled || saving || models.loading" @change="change">
      <option v-if="!models.list.length" value="">{{ models.loading ? '加载模型…' : '默认模型' }}</option>
      <option v-for="model in models.list" :key="model.id" :value="model.id" :title="model.desc">
        {{ model.name }}{{ model.default ? ' · 默认' : '' }}{{ model.tags.length ? ` · ${model.tags.join(' / ')}` : '' }}
      </option>
    </select>
    <ChevronDown :size="13" aria-hidden="true" />
    <span v-if="saving" class="model-select__saving">切换中…</span>
  </div>
</template>
