import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api } from '@/api'
import type { ModelView } from '@/types/contract'

export const useModelsStore = defineStore('models', () => {
  const list = ref<ModelView[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref<unknown>(null)
  let pending: Promise<void> | null = null
  const defaultModel = computed(() => list.value.find((model) => model.default) || list.value[0])

  async function load(force = false): Promise<void> {
    if (loaded.value && !force) return
    if (pending) return pending
    loading.value = true
    error.value = null
    pending = api.models.list()
      .then((models) => {
        list.value = models
        loaded.value = true
      })
      .catch((reason: unknown) => {
        error.value = reason
        throw reason
      })
      .finally(() => {
        loading.value = false
        pending = null
      })
    return pending
  }

  function byId(id: string): ModelView | undefined {
    return list.value.find((model) => model.id === id)
  }

  return { list, loading, loaded, error, defaultModel, load, byId }
})
