import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import type { ToolItem } from '@/events/reducer'
import ToolCard from './ToolCard.vue'

function tool(overrides: Partial<ToolItem> = {}): ToolItem {
  return {
    id: 'tool:call-1', type: 'tool', key: 'call-1', runId: 'r1', seq: 1, position: 0,
    ts: '2026-10-24T10:00:00Z', callId: 'call-1',
    call: { name: 'bash', summary: 'npm test', args: '{"command":"npm test"}' },
    result: { name: 'bash', content: '1 failing test', is_error: true, truncated: true },
    ...overrides,
  }
}

describe('ToolCard', () => {
  it('默认折叠，展开后以终端样式展示失败与截断输出', async () => {
    const wrapper = mount(ToolCard, { props: { item: tool() } })
    expect(wrapper.classes()).toContain('tool-card--failed')
    expect(wrapper.find('.tool-card__state--error').exists()).toBe(true)
    expect(wrapper.find('.tool-card__details').exists()).toBe(false)
    expect(wrapper.find('.tool-card__state').text()).toContain('失败')
    expect(wrapper.find('.tool-card__truncated').text()).toContain('已截断')
    await wrapper.find('.tool-card__toggle').trigger('click')
    expect(wrapper.find('.tool-card__details').exists()).toBe(true)
    expect(wrapper.findAll('pre.tool-terminal')).toHaveLength(2)
    expect(wrapper.text()).toContain('1 failing test')
  })

  it('文件工具可跳转到代码工作区', async () => {
    const wrapper = mount(ToolCard, { props: { item: tool({
      call: { name: 'write_file', summary: 'src/App.tsx', args: '{"path":"src/App.tsx"}' },
      result: { name: 'write_file', content: 'ok', is_error: false, truncated: false },
    }) } })
    await wrapper.find('.tool-card__open').trigger('click')
    expect(wrapper.emitted('openFile')).toEqual([['src/App.tsx']])
  })
})
