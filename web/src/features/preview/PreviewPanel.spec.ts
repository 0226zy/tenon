import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import { sseManager } from '@/events/sse'
import { usePreviewStore } from '@/stores/preview'
import PreviewPanel from './PreviewPanel.vue'

describe('PreviewPanel 休眠唤醒', () => {
  let pinia: Pinia
  let wrapper: VueWrapper | undefined

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    vi.useFakeTimers()
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    sseManager.closeAll()
    vi.useRealTimers()
  })

  it('唤醒期间保留已确认版本，成功后合并新地址并清除旧版本标记', async () => {
    const preview = usePreviewStore()
    Object.assign(preview.ensure('c1'), {
      status: 'ready', generation: 1, commit: 'commit-1',
      displayedGeneration: 1, displayedUrl: 'https://preview.test/v1', displayedCommit: 'commit-1',
    })
    preview.setLatestRunGeneration('c1', 2)
    const entry = vi.spyOn(api.preview, 'entry')
      .mockRejectedValueOnce(new ApiError({ code: 'SANDBOX_STARTING', status: 503 }))
      .mockResolvedValueOnce({
        status: 'ready', generation: 2, commit: 'commit-2', stale: false, url: 'https://preview.test/v2',
      })
    wrapper = mount(PreviewPanel, { props: { cid: 'c1' }, global: { plugins: [pinia] } })
    await flushPromises()

    expect(entry).toHaveBeenCalledTimes(1)
    expect(preview.ensure('c1').status).toBe('starting')
    expect(wrapper.get('iframe').attributes('src')).toBe('https://preview.test/v1')
    expect(wrapper.get('.preview-status').text()).toContain('构建中')
    expect(wrapper.get('.preview-stale').text()).toBe('旧版本')

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()

    expect(entry).toHaveBeenCalledTimes(2)
    expect(preview.ensure('c1').status).toBe('ready')
    expect(wrapper.get('iframe').attributes('src')).toBe('https://preview.test/v2')
    expect(wrapper.get('.preview-status').text()).toContain('预览可用')
    expect(wrapper.find('.preview-stale').exists()).toBe(false)
    expect(preview.ensure('c1').stale).toBe(false)
  })

  it('HTTP 200 starting 后自动连续轮询，同代 ready 后停止并更新地址', async () => {
    const preview = usePreviewStore()
    Object.assign(preview.ensure('c1'), {
      status: 'ready', generation: 6, commit: 'commit-6', latestRunGeneration: 6,
      displayedGeneration: 6, displayedUrl: 'https://preview.test/before-sleep', displayedCommit: 'commit-6',
    })
    const entry = vi.spyOn(api.preview, 'entry')
      .mockResolvedValueOnce({ status: 'starting', generation: 6, commit: 'commit-6', stale: false })
      .mockResolvedValueOnce({ status: 'starting', generation: 6, commit: 'commit-6', stale: false })
      .mockResolvedValueOnce({
        status: 'ready', generation: 6, commit: 'commit-6', stale: false, url: 'https://preview.test/after-wake',
      })
    wrapper = mount(PreviewPanel, { props: { cid: 'c1' }, global: { plugins: [pinia] } })
    await flushPromises()

    expect(entry).toHaveBeenCalledTimes(1)
    expect(preview.ensure('c1').status).toBe('starting')
    expect(wrapper.get('iframe').attributes('src')).toBe('https://preview.test/before-sleep')

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(entry).toHaveBeenCalledTimes(2)
    expect(preview.ensure('c1').status).toBe('starting')

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(entry).toHaveBeenCalledTimes(3)
    expect(preview.ensure('c1').status).toBe('ready')
    expect(wrapper.get('iframe').attributes('src')).toBe('https://preview.test/after-wake')
    expect(wrapper.get('.preview-status').text()).toContain('预览可用')
    expect(wrapper.get('.preview-status').text()).toContain('#6')

    await vi.advanceTimersByTimeAsync(10_000)
    await flushPromises()
    expect(entry).toHaveBeenCalledTimes(3)
  })
})
