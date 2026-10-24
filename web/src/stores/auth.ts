import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import { sseManager } from '@/events/sse'
import type { UserView } from '@/types/contract'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<UserView | null>(null)
  const checked = ref(false)
  const loading = ref(false)
  const lockedUntil = ref(0)
  let ensurePromise: Promise<UserView | null> | null = null

  const authenticated = computed(() => !!user.value)

  async function ensure(force = false): Promise<UserView | null> {
    if (checked.value && !force) return user.value
    if (ensurePromise) return ensurePromise
    loading.value = true
    ensurePromise = api.auth.me()
      .then((result) => {
        user.value = result
        checked.value = true
        return result
      })
      .catch((error: unknown) => {
        checked.value = true
        user.value = null
        if (error instanceof ApiError && error.status === 401) return null
        throw error
      })
      .finally(() => {
        loading.value = false
        ensurePromise = null
      })
    return ensurePromise
  }

  async function login(username: string, password: string): Promise<void> {
    if (Date.now() < lockedUntil.value) return
    loading.value = true
    try {
      await api.auth.login(username, password)
      user.value = await api.auth.me()
      checked.value = true
      lockedUntil.value = 0
    } catch (error) {
      if (error instanceof ApiError && error.status === 429) {
        lockedUntil.value = Date.now() + (error.retryAfter ?? 15 * 60) * 1000
      }
      throw error
    } finally {
      loading.value = false
    }
  }

  function clear(): void {
    user.value = null
    checked.value = true
    sseManager.closeAll()
  }

  async function logout(): Promise<void> {
    try { await api.auth.logout() } finally { clear() }
  }

  return { user, checked, loading, lockedUntil, authenticated, ensure, login, logout, clear }
})
