import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { ConversationView } from '@/types/contract'
import ConvItem from './ConvItem.vue'

vi.mock('vue-router', () => ({ useRouter: () => ({ replace: vi.fn() }) }))

function conversation(overrides: Partial<ConversationView> = {}): ConversationView {
  return {
    id: 'c1',
    title: '',
    model: 'model-1',
    status: 'queued',
    last_active: '2026-10-24T10:00:00Z',
    created_at: '2026-10-24T09:00:00Z',
    ...overrides,
  }
}

describe('ConvItem', () => {
  it('展示自动标题兜底，并随服务端状态更新排队、收尾和异常标识', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const wrapper = mount(ConvItem, {
      props: { conversation: conversation(), active: false },
      global: { plugins: [pinia] },
    })
    expect(wrapper.get('.conv-item__title').text()).toBe('未命名项目')
    expect(wrapper.get('.status-badge').text()).toBe('排队中')

    await wrapper.setProps({
      conversation: conversation({
        title: '做一个待办应用',
        status: 'running',
        run: { run_id: 'r1', status: 'finalizing' },
      }),
    })
    expect(wrapper.get('.conv-item__title').text()).toBe('做一个待办应用')
    expect(wrapper.get('.status-badge').text()).toBe('收尾中')

    await wrapper.setProps({ conversation: conversation({ title: '做一个待办应用', status: 'error' }) })
    expect(wrapper.get('.status-badge').text()).toBe('异常')
    wrapper.unmount()
  })
})
