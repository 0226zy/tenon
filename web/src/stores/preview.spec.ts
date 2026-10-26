import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '@/api'
import { emptyPreviewState, mergePreviewState, usePreviewStore } from './preview'

function response(generation: number, url?: string) {
  return { status: 'ready' as const, generation, commit: `commit-${generation}`, stale: false, url }
}

describe('预览 generation fencing', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('旧 generation 不覆盖当前展示 URL，新版本才替换', () => {
    let state = emptyPreviewState()
    state = mergePreviewState(state, response(2, 'https://preview.test/entry-2'))
    const currentUrl = state.displayedUrl
    state = mergePreviewState(state, response(1, 'https://preview.test/old'))
    expect(state.displayedGeneration).toBe(2)
    expect(state.displayedUrl).toBe(currentUrl)
    state = mergePreviewState(state, response(3, 'https://preview.test/entry-3'))
    expect(state.displayedGeneration).toBe(3)
    expect(state.displayedUrl).toBe('https://preview.test/entry-3')
  })

  it('异步响应乱序时 store 仍保留高版本，并计算 stale', async () => {
    const preview = usePreviewStore()
    Object.assign(preview.ensure('c1'), mergePreviewState(emptyPreviewState(), response(5, 'https://preview.test/v5')))
    vi.spyOn(api.preview, 'entry').mockResolvedValue(response(4, 'https://preview.test/v4'))
    await preview.refresh('c1')
    expect(preview.ensure('c1').displayedGeneration).toBe(5)
    expect(preview.ensure('c1').displayedUrl).toBe('https://preview.test/v5')
    preview.setLatestRunGeneration('c1', 6)
    expect(preview.ensure('c1').stale).toBe(true)
  })

  it('唤醒或失败的新 generation 合并且不清空已确认 URL，旧版本与错误码保持可见', () => {
    let state = mergePreviewState(emptyPreviewState(), response(2, 'https://preview.test/v2'))
    state.latestRunGeneration = 3
    state.stale = true

    state = mergePreviewState(state, { status: 'starting', generation: 3, stale: false })
    expect(state.status).toBe('starting')
    expect(state.displayedUrl).toBe('https://preview.test/v2')
    expect(state.displayedGeneration).toBe(2)
    expect(state.stale).toBe(true)

    state = mergePreviewState(state, { status: 'failed', generation: 3, stale: false, error_code: 'BUILD_FAILED' })
    expect(state.status).toBe('failed')
    expect(state.errorCode).toBe('BUILD_FAILED')
    expect(state.displayedUrl).toBe('https://preview.test/v2')
    expect(state.stale).toBe(true)
  })

  it('重复 ready 事件不重复换取已展示版本的票据', async () => {
    const preview = usePreviewStore()
    Object.assign(preview.ensure('c1'), mergePreviewState(emptyPreviewState(), response(6, 'https://preview.test/v6')))
    const entry = vi.spyOn(api.preview, 'entry').mockResolvedValue(response(7, 'https://preview.test/v7'))

    preview.applyEvent('c1', { status: 'ready', generation: 6, commit: 'commit-6' })
    preview.applyEvent('c1', { status: 'ready', generation: 6, commit: 'commit-6' })
    expect(entry).not.toHaveBeenCalled()

    preview.applyEvent('c1', { status: 'ready', generation: 7, commit: 'commit-7' })
    expect(entry).toHaveBeenCalledTimes(1)
    await vi.waitFor(() => expect(preview.ensure('c1').displayedGeneration).toBe(7))
    expect(preview.ensure('c1').displayedUrl).toBe('https://preview.test/v7')
  })

  it('日志超过 128 KB 时保留最新尾部并明确标记截断', async () => {
    const logs = vi.spyOn(api.preview, 'logs').mockResolvedValue(
      `old-marker\n${'x'.repeat(140 * 1024)}\nfinal-log-line`,
    )
    const preview = usePreviewStore()

    await preview.loadLogs('c1')

    const state = preview.ensure('c1')
    expect(logs).toHaveBeenCalledWith('c1', 200)
    expect(state.logsOpen).toBe(true)
    expect(state.logsTruncated).toBe(true)
    expect(state.logs).toContain('仅显示最后 128 KB 日志')
    expect(state.logs).not.toContain('old-marker')
    expect(state.logs.endsWith('final-log-line')).toBe(true)
  })

  it('合并构建阶段、错误码与失败日志尾部', () => {
    let state = mergePreviewState(emptyPreviewState(), {
      status: 'starting', phase: 'building', generation: 3, stale: false,
    })
    expect(state.phase).toBe('building')
    expect(state.phaseStartedAt).toBeGreaterThan(0)

    state = mergePreviewState(state, {
      status: 'starting', phase: 'healthcheck', generation: 3, stale: false,
    })
    expect(state.phase).toBe('healthcheck')

    state = mergePreviewState(state, {
      status: 'failed', phase: 'failed', generation: 3, stale: false,
      error_code: 'BUILD_FAILED', log_tail: 'vite build failed',
    })
    expect(state.status).toBe('failed')
    expect(state.errorCode).toBe('BUILD_FAILED')
    expect(state.logTail).toBe('vite build failed')
    expect(state.displayedUrl).toBe('')
  })

})
