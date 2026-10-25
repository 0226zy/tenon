import { ref } from 'vue'
import { defineStore } from 'pinia'
import { errorMessage } from '@/api/errors'
import type { ExamplePrompt } from '@/constants/examples'

export interface Toast {
  id: number
  type: 'info' | 'success' | 'error'
  message: string
  closing?: boolean
}

export type AuthTab = 'login' | 'register'

/** 登录/注册成功之后要继续的动作：创建对话（可带示例），或回到受限页面。 */
export type AuthIntent =
  | { type: 'create'; example?: ExamplePrompt }
  | { type: 'redirect'; path: string }

let nextToastId = 1
export const useUiStore = defineStore('ui', () => {
  const toasts = ref<Toast[]>([])
  const sidebarCollapsed = ref(localStorage.getItem('proto.sidebar.collapsed') === '1')
  const workspaceCollapsed = ref(localStorage.getItem('proto.workspace.collapsed') === '1')
  const workspaceTab = ref<'code' | 'preview'>('code')
  const mobileWorkspaceOpen = ref(false)
  const authDialogOpen = ref(false)
  const authDialogTab = ref<AuthTab>('login')
  const authIntent = ref<AuthIntent | null>(null)

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

  function setWorkspaceCollapsed(collapsed: boolean): void {
    workspaceCollapsed.value = collapsed
    localStorage.setItem('proto.workspace.collapsed', collapsed ? '1' : '0')
    if (collapsed) mobileWorkspaceOpen.value = false
  }

  function toggleWorkspace(): void {
    setWorkspaceCollapsed(!workspaceCollapsed.value)
  }

  function showWorkspace(tab: 'code' | 'preview'): void {
    workspaceTab.value = tab
    setWorkspaceCollapsed(false)
    mobileWorkspaceOpen.value = true
  }

  function openAuth(intent: AuthIntent | null = null, tab: AuthTab = 'login'): void {
    authIntent.value = intent
    authDialogTab.value = tab
    authDialogOpen.value = true
  }

  function closeAuth(): void {
    authDialogOpen.value = false
    authIntent.value = null
  }

  return {
    toasts, sidebarCollapsed, workspaceCollapsed, workspaceTab, mobileWorkspaceOpen,
    authDialogOpen, authDialogTab, authIntent,
    dismiss, push, error, toggleSidebar, setWorkspaceCollapsed, toggleWorkspace, showWorkspace, openAuth, closeAuth,
  }
})
