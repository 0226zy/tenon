import { beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { usePreviewStore } from '@/stores/preview'
import PreviewFrame from './PreviewFrame.vue'

describe('PreviewFrame', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('使用规定的 sandbox 权限，并渲染当前已确认 URL', () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const preview = usePreviewStore()
    Object.assign(preview.ensure('c1'), {
      status: 'ready', generation: 3, displayedGeneration: 3,
      displayedUrl: 'https://abc.preview.test/__enter?t=one-time', displayedCommit: 'abc1234',
    })
    const wrapper = mount(PreviewFrame, { props: { cid: 'c1' }, global: { plugins: [pinia] } })
    const iframe = wrapper.find('iframe')
    expect(iframe.attributes('src')).toBe('https://abc.preview.test/__enter?t=one-time')
    expect(iframe.attributes('sandbox')).toBe('allow-scripts allow-forms allow-same-origin allow-popups')
    expect(iframe.attributes('referrerpolicy')).toBe('no-referrer')
  })

  it('没有已确认版本时展示构建失败原因，并可查看日志或重启', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const preview = usePreviewStore()
    Object.assign(preview.ensure('c1'), { status: 'failed', errorCode: 'PORT_CONFLICT' })
    const wrapper = mount(PreviewFrame, { props: { cid: 'c1' }, global: { plugins: [pinia] } })

    expect(wrapper.get('[role="alert"]').text()).toContain('预览构建失败')
    expect(wrapper.get('[role="alert"]').text()).toContain('预览端口被占用')
    await wrapper.findAll('button').find((button) => button.text() === '查看日志')?.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === '重启预览')?.trigger('click')
    expect(wrapper.emitted('logs')).toHaveLength(1)
    expect(wrapper.emitted('restart')).toHaveLength(1)
  })
})
