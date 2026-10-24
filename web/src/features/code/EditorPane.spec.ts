import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import EditorPane from './EditorPane.vue'

const monaco = vi.hoisted(() => {
  const editor = { setModel: vi.fn(), dispose: vi.fn() }
  return {
    editor,
    create: vi.fn(() => editor),
    createModel: vi.fn((value: string, language: string, uri: unknown) => ({ value, language, uri, dispose: vi.fn() })),
    defineTheme: vi.fn(),
    file: vi.fn((path: string) => ({ path })),
  }
})

vi.mock('./monaco', () => ({
  loadMonaco: vi.fn(async () => ({
    Uri: { file: monaco.file },
    editor: {
      create: monaco.create,
      createModel: monaco.createModel,
      defineTheme: monaco.defineTheme,
    },
  })),
}))

describe('EditorPane', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('以只读 Monaco 打开代码，并在文件刷新后替换展示内容', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const workspace = useWorkspaceStore()
    const state = workspace.ensure('c1')
    state.openPath = 'src/App.ts'
    state.files['src/App.ts'] = {
      path: 'src/App.ts', loading: false, deleted: false, refreshing: false,
      content: {
        path: 'src/App.ts', size: 12, binary: false, truncated: false,
        content: 'const a = 1', etag: 'etag-1',
      },
    }
    const wrapper = mount(EditorPane, { props: { cid: 'c1' }, global: { plugins: [pinia] } })
    await flushPromises()

    expect(wrapper.get('.readonly-badge').text()).toBe('只读')
    expect(monaco.create).toHaveBeenCalledWith(expect.any(HTMLElement), expect.objectContaining({
      readOnly: true,
      domReadOnly: true,
    }))
    expect(monaco.createModel).toHaveBeenCalledWith(
      'const a = 1', 'typescript', expect.objectContaining({ path: '/workspace/app/src/App.ts' }),
    )
    const firstModel = monaco.createModel.mock.results[0]?.value as { dispose: ReturnType<typeof vi.fn> }

    state.files['src/App.ts'].content = {
      path: 'src/App.ts', size: 12, binary: false, truncated: false,
      content: 'const b = 2', etag: 'etag-2',
    }
    await nextTick()
    await flushPromises()

    expect(monaco.createModel).toHaveBeenCalledTimes(2)
    expect(monaco.createModel).toHaveBeenLastCalledWith(
      'const b = 2', 'typescript', expect.objectContaining({ path: '/workspace/app/src/App.ts' }),
    )
    expect(firstModel.dispose).toHaveBeenCalledOnce()
    expect(monaco.editor.setModel).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
