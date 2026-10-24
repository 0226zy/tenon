import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import WorkspacePanel from './WorkspacePanel.vue'

vi.mock('@/features/code/CodePanel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/features/preview/PreviewPanel.vue', () => ({ default: { template: '<div />' } }))

describe('WorkspacePanel 未开放能力', () => {
  it('推送和发布均真正禁用，并提供即将开放提示', () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const wrapper = mount(WorkspacePanel, { props: { cid: '' }, global: { plugins: [pinia] } })

    for (const label of ['推送', '发布']) {
      const button = wrapper.findAll('button').find((candidate) => candidate.text() === label)
      expect(button, label).toBeDefined()
      expect((button?.element as HTMLButtonElement).disabled).toBe(true)
      expect(button?.element.parentElement?.getAttribute('title')).toBe('即将开放')
    }
    wrapper.unmount()
  })
})
