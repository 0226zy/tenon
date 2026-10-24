import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { computed, defineComponent } from 'vue'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import { sseManager } from '@/events/sse'
import { useChatStore } from '@/stores/chat'
import UserBubble from './UserBubble.vue'

const Host = defineComponent({
  components: { UserBubble },
  setup() {
    const chat = useChatStore()
    return { messages: computed(() => chat.users('c1')) }
  },
  template: '<UserBubble v-for="message in messages" :key="message.id" cid="c1" :message="message" />',
})

describe('UserBubble 乐观消息状态', () => {
  afterEach(() => sseManager.closeAll())

  it('网络拒绝后真实 DOM 显示发送失败和重试入口', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    vi.spyOn(api.run, 'send').mockRejectedValue(new ApiError({ code: 'NETWORK_ERROR', status: 0 }))
    const chat = useChatStore()
    chat.ensure('c1')
    const wrapper = mount(Host, { global: { plugins: [pinia] } })

    await expect(chat.send('c1', '验证失败状态')).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    await flushPromises()

    expect(wrapper.get('.user-message--failed').text()).toContain('发送失败')
    expect(wrapper.get('.user-message--failed').text()).not.toContain('发送中')
    expect(wrapper.get('button').text()).toContain('重试')
    wrapper.unmount()
  })
})
