import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { api } from '@/api'
import { useChatStore } from '@/stores/chat'
import { sseManager } from '@/events/sse'
import Composer from './Composer.vue'

function mountComposer(pinia: Pinia) {
  return mount(Composer, {
    props: { cid: 'c1' },
    global: { plugins: [pinia] },
  })
}

describe('Composer', () => {
  let pinia: Pinia
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    sseManager.closeAll()
  })

  it('Enter 发送，Shift+Enter 不发送', async () => {
    const send = vi.spyOn(api.run, 'send').mockResolvedValue({ run_id: 'r1', message_id: 'm1', seq: 1, status: 'queued' })
    const wrapper = mountComposer(pinia)
    const textarea = wrapper.find('textarea')
    await textarea.setValue('第一行')
    await textarea.trigger('keydown', { key: 'Enter', shiftKey: true })
    await flushPromises()
    expect(send).not.toHaveBeenCalled()
    await textarea.setValue('做一个待办应用')
    await textarea.trigger('keydown', { key: 'Enter', shiftKey: false, isComposing: false })
    await flushPromises()
    expect(send).toHaveBeenCalledOnce()
    expect(send.mock.calls[0][1].content).toBe('做一个待办应用')
    expect(useChatStore().users('c1')).toHaveLength(1)
  })

  it('活跃 Run 禁用输入并提供取消，cancelling 后禁用按钮', async () => {
    const cancel = vi.spyOn(api.run, 'cancel').mockResolvedValue({ run_id: 'r1', status: 'cancelling' })
    const chat = useChatStore()
    const view = chat.ensure('c1')
    view.runId = 'r1'
    view.runStatus = 'running'
    const wrapper = mountComposer(pinia)
    expect(wrapper.find('textarea').attributes('disabled')).toBeDefined()
    const button = wrapper.find('.composer__send')
    expect(button.text()).toContain('取消')
    await button.trigger('click')
    await flushPromises()
    expect(cancel).toHaveBeenCalledWith('c1', 'r1')
    expect(view.runStatus).toBe('cancelling')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)
  })

  it('queued、running、cancelling、finalizing 均禁用输入和真实模型 select', async () => {
    const chat = useChatStore()
    const view = chat.ensure('c1')
    view.runId = 'r1'
    const wrapper = mountComposer(pinia)
    const textarea = wrapper.get('textarea')
    const select = wrapper.get('select[aria-label="执行模型"]')
    const button = wrapper.get('.composer__send')
    expect((textarea.element as HTMLTextAreaElement).disabled).toBe(false)
    expect((select.element as HTMLSelectElement).disabled).toBe(false)

    const labels = { queued: '取消', running: '取消', cancelling: '取消中…', finalizing: '收尾中…' } as const
    for (const status of ['queued', 'running', 'cancelling', 'finalizing'] as const) {
      view.runStatus = status
      await nextTick()
      expect((textarea.element as HTMLTextAreaElement).disabled).toBe(true)
      expect((select.element as HTMLSelectElement).disabled).toBe(true)
      expect(button.text()).toContain(labels[status])
      expect((button.element as HTMLButtonElement).disabled).toBe(status === 'cancelling' || status === 'finalizing')
    }
    wrapper.unmount()
  })
})
