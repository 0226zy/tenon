import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '@/api'
import { sseManager } from '@/events/sse'
import { useChatStore } from '@/stores/chat'
import { useWorkspaceStore } from '@/stores/workspace'
import FileTree from './FileTree.vue'

describe('FileTree', () => {
  afterEach(() => sseManager.closeAll())

  it('目录点击时才加载子项，保留可见点文件并标记目录及文件改动', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const tree = vi.spyOn(api.files, 'tree').mockImplementation(async (_cid, path = '') => {
      if (path === 'src') return [
        { name: 'App.ts', path: 'src/App.ts', type: 'file', size: 20, mtime: '' },
      ]
      return [
        { name: '.gitignore', path: '.gitignore', type: 'file', size: 8, mtime: '' },
        { name: 'src', path: 'src', type: 'dir', size: 0, mtime: '' },
      ]
    })
    const workspace = useWorkspaceStore()
    useChatStore().ensure('c1').changedFiles = new Set(['src/App.ts'])
    await workspace.loadDir('c1')
    const wrapper = mount(FileTree, { props: { cid: 'c1' }, global: { plugins: [pinia] } })

    expect(tree).toHaveBeenCalledTimes(1)
    expect(tree).not.toHaveBeenCalledWith('c1', 'src')
    expect(wrapper.text()).toContain('.gitignore')
    expect(wrapper.text()).not.toContain('App.ts')
    const sourceRow = wrapper.findAll('.file-tree-row').find((row) => row.text() === 'src')
    expect(sourceRow).toBeDefined()
    expect(sourceRow?.find('[aria-label="包含改动文件"]').exists()).toBe(true)

    await sourceRow?.trigger('click')
    await flushPromises()

    expect(tree).toHaveBeenCalledWith('c1', 'src')
    const appRow = wrapper.findAll('.file-tree-row').find((row) => row.text() === 'App.ts')
    expect(appRow).toBeDefined()
    expect(appRow?.find('[aria-label="本轮有改动"]').exists()).toBe(true)
    expect(sourceRow?.attributes('aria-expanded')).toBe('true')
    wrapper.unmount()
  })
})
