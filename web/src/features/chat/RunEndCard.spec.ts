import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import type { RunEndItem } from '@/events/reducer'
import RunEndCard from './RunEndCard.vue'

const item: RunEndItem = {
  id: 'run-end:r1', type: 'run_end', key: 'end', runId: 'r1', seq: 8, position: 0,
  ts: '2026-10-24T10:00:00Z', status: 'finished',
  data: {
    commit: 'abc123456789', checkpoint_status: 'done', files_total: 2, duration_ms: 192000,
    files: [{ path: 'src/App.tsx', change: 'M' }, { path: 'README.md', change: 'A' }],
    usage: { prompt: 100000, completion: 23000 },
  },
}

describe('RunEndCard', () => {
  it('展示 commit、文件数、耗时与 token，并允许点击文件', async () => {
    const wrapper = mount(RunEndCard, { props: { item, canRetry: false } })
    expect(wrapper.text()).toContain('改动 2 个文件')
    expect(wrapper.text()).toContain('abc1234')
    expect(wrapper.text()).toContain('3 分 12 秒')
    expect(wrapper.text()).toContain('12.3 万 tokens')
    expect(wrapper.find('button[aria-label="重新执行"]').exists()).toBe(false)
    await wrapper.find('.run-end-card__files-toggle').trigger('click')
    await wrapper.find('.run-end-card__files button').trigger('click')
    expect(wrapper.emitted('openFile')).toEqual([['src/App.tsx']])
  })

  it('只有最近失败或取消 Run 由父级开放重新执行', async () => {
    const wrapper = mount(RunEndCard, { props: { item: { ...item, status: 'failed' }, canRetry: true } })
    const retry = wrapper.find('.run-end-card__summary .button')
    expect(retry.text()).toContain('重新执行')
    await retry.trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })
})
