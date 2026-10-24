import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '@/api'
import { reduce } from '@/events/reducer'
import { sseManager } from '@/events/sse'
import { useChatStore } from '@/stores/chat'
import { useUiStore } from '@/stores/ui'
import { useWorkspaceStore } from '@/stores/workspace'
import ChatPanel from './ChatPanel.vue'

describe('ChatPanel 文件工具跳转', () => {
  afterEach(() => sseManager.closeAll())

  it('点击文件工具的查看代码会切到代码工作区并真正打开该文件', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const content = vi.spyOn(api.files, 'content').mockResolvedValue({
      path: 'src/App.ts', size: 18, binary: false, truncated: false,
      content: 'export const ok = true', etag: 'etag-1',
    })
    const chat = useChatStore()
    chat.byConv.c1 = reduce(chat.ensure('c1'), {
      seq: 1,
      run_id: 'r1',
      ts: '2026-10-24T10:00:00Z',
      items: [
        {
          key: 'call-1', type: 'tool_call',
          data: { call_id: 'call-1', name: 'write_file', summary: 'src/App.ts', args: '{"path":"src/App.ts"}' },
        },
        {
          key: 'call-1-result', type: 'tool_result',
          data: { call_id: 'call-1', name: 'write_file', content: 'ok', is_error: false, truncated: false },
        },
      ],
    })
    const ui = useUiStore()
    ui.workspaceTab = 'preview'
    ui.mobileWorkspaceOpen = false
    const wrapper = mount(ChatPanel, { props: { cid: 'c1' }, global: { plugins: [pinia] } })

    await wrapper.get('.tool-card__open').trigger('click')
    await flushPromises()

    expect(ui.workspaceTab).toBe('code')
    expect(ui.mobileWorkspaceOpen).toBe(true)
    expect(content).toHaveBeenCalledWith('c1', 'src/App.ts', undefined)
    expect(useWorkspaceStore().ensure('c1').openPath).toBe('src/App.ts')
    expect(useWorkspaceStore().openFileState('c1')?.content?.content).toBe('export const ok = true')
    wrapper.unmount()
  })
})
