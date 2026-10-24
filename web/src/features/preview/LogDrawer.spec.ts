import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '@/api'
import { usePreviewStore } from '@/stores/preview'
import LogDrawer from './LogDrawer.vue'

describe('LogDrawer', () => {
  it('加载并展示日志，可刷新最新内容和关闭抽屉', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const logs = vi.spyOn(api.preview, 'logs')
      .mockResolvedValueOnce('boot\nready')
      .mockResolvedValueOnce('boot\nready\nrestarted')
    const preview = usePreviewStore()
    const wrapper = mount(LogDrawer, { props: { cid: 'c1' }, global: { plugins: [pinia] } })
    expect(wrapper.find('.log-drawer').exists()).toBe(false)

    await preview.loadLogs('c1')
    expect(logs).toHaveBeenCalledWith('c1', 200)
    expect(wrapper.get('.log-drawer__content').text()).toContain('ready')

    await wrapper.get('[aria-label="刷新日志"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('.log-drawer__content').text()).toContain('restarted')

    await wrapper.get('[aria-label="关闭日志"]').trigger('click')
    expect(wrapper.find('.log-drawer').exists()).toBe(false)
    expect(preview.ensure('c1').logsOpen).toBe(false)
    wrapper.unmount()
  })
})
