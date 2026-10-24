import { ref } from 'vue'
import { defineStore } from 'pinia'
import { errorMessage } from '@/api/errors'

export interface Toast {
  id: number
  type: 'info' | 'success' | 'error'
  message: string
  closing?: boolean
}

let nextToastId = 1
export const useUiStore = defineStore('ui', () => {
  const toasts = ref<Toast[]>([])
  const sidebarCollapsed = ref(localStorage.getItem('proto.sidebar.collapsed') === '1')
  const workspaceTab = ref<'code' | 'preview'>('code')
  const mobileWorkspaceOpen = ref(false)

  function dismiss(id: number): void {
    toasts.value = toasts.value.filter((toast) => toast.id !== id)
  }

  function push(message: string, type: Toast['type'] = 'info', duration = 4500): number {
    const id = nextToastId++
    toasts.value.push({ id, type, message })
    toasts.value = toasts.value.slice(-4)
    if (duration > 0) window.setTimeout(() => dismiss(id), duration)
    return id
  }

  function error(reason: unknown, context?: 'login'): void {
    push(errorMessage(reason, context), 'error', 6000)
  }

  function toggleSidebar(): void {
    sidebarCollapsed.value = !sidebarCollapsed.value
    localStorage.setItem('proto.sidebar.collapsed', sidebarCollapsed.value ? '1' : '0')
  }

  function showWorkspace(tab: 'code' | 'preview'): void {
    workspaceTab.value = tab
    mobileWorkspaceOpen.value = true
  }

  return { toasts, sidebarCollapsed, workspaceTab, mobileWorkspaceOpen, dismiss, push, error, toggleSidebar, showWorkspace }
})
