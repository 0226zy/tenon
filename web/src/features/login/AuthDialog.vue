<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'
import { Braces, X } from 'lucide-vue-next'
import AuthForm from '@/components/AuthForm.vue'
import type { AuthTab } from '@/stores/ui'

withDefaults(defineProps<{ tab?: AuthTab }>(), { tab: 'login' })
const emit = defineEmits<{ success: [tab: AuthTab]; close: [] }>()

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') emit('close')
}
onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  document.body.classList.add('is-modal-open')
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  document.body.classList.remove('is-modal-open')
})
</script>

<template>
  <Teleport to="body">
    <div class="modal-backdrop auth-dialog" @mousedown.self="emit('close')">
      <section class="auth-dialog__panel" role="dialog" aria-modal="true" aria-label="登录或注册">
        <button class="icon-button auth-dialog__close" type="button" aria-label="关闭登录窗口" @click="emit('close')">
          <X :size="18" />
        </button>
        <div class="auth-dialog__brand">
          <span class="brand-mark"><Braces :size="20" /></span>
          <span>Tenon</span>
        </div>
        <p class="auth-dialog__hint">创建项目前需要先登录或注册</p>
        <AuthForm :initial-tab="tab" @success="emit('success', $event)" />
      </section>
    </div>
  </Teleport>
</template>
