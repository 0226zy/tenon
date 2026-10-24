<script setup lang="ts">
import { AlertTriangle, X } from 'lucide-vue-next'

withDefaults(defineProps<{
  title: string
  description: string
  confirmText?: string
  busy?: boolean
}>(), { confirmText: '确认', busy: false })

const emit = defineEmits<{ confirm: []; cancel: [] }>()
</script>

<template>
  <Teleport to="body">
    <div class="modal-backdrop" @mousedown.self="!busy && emit('cancel')">
      <section class="confirm-dialog" role="dialog" aria-modal="true" :aria-label="title" @keydown.esc="!busy && emit('cancel')">
        <button class="icon-button confirm-dialog__close" type="button" aria-label="关闭" :disabled="busy" @click="emit('cancel')">
          <X :size="18" />
        </button>
        <div class="confirm-dialog__icon"><AlertTriangle :size="22" /></div>
        <h2>{{ title }}</h2>
        <p>{{ description }}</p>
        <div class="confirm-dialog__actions">
          <button class="button button--secondary" type="button" :disabled="busy" @click="emit('cancel')">取消</button>
          <button class="button button--danger" type="button" :disabled="busy" @click="emit('confirm')">
            <span v-if="busy" class="spinner" aria-hidden="true" />{{ busy ? '正在删除…' : confirmText }}
          </button>
        </div>
      </section>
    </div>
  </Teleport>
</template>
