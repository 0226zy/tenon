<script setup lang="ts">
import { AlertCircle, CheckCircle2, Info, X } from 'lucide-vue-next'
import { useUiStore } from '@/stores/ui'

const ui = useUiStore()
</script>

<template>
  <div class="toast-region" aria-live="polite" aria-atomic="false">
    <div
      v-for="toast in ui.toasts"
      :key="toast.id"
      class="toast"
      :class="`toast--${toast.type}`"
      :role="toast.type === 'error' ? 'alert' : 'status'"
    >
      <AlertCircle v-if="toast.type === 'error'" :size="18" aria-hidden="true" />
      <CheckCircle2 v-else-if="toast.type === 'success'" :size="18" aria-hidden="true" />
      <Info v-else :size="18" aria-hidden="true" />
      <span>{{ toast.message }}</span>
      <button class="icon-button toast__close" type="button" aria-label="关闭提示" @click="ui.dismiss(toast.id)">
        <X :size="15" />
      </button>
    </div>
  </div>
</template>
