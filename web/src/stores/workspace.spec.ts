import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import type { FileContent } from '@/types/contract'
import { useWorkspaceStore } from './workspace'

function content(etag: string): FileContent {
  return { path: 'README.md', size: 12, binary: false, truncated: false, content: '# 项目说明', etag }
}

describe('workspace store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('保留后端隐藏项决策，并按目录优先、名称升序排列', async () => {
    vi.spyOn(api.files, 'tree').mockResolvedValue([
      { name: 'z.ts', path: 'z.ts', type: 'file', size: 1, mtime: '' },
      { name: 'node_modules', path: 'node_modules', type: 'dir', size: 0, mtime: '' },
      { name: 'src', path: 'src', type: 'dir', size: 0, mtime: '' },
      { name: 'a.ts', path: 'a.ts', type: 'file', size: 1, mtime: '' },
    ])
    const workspace = useWorkspaceStore()
    await workspace.loadDir('c1')
    expect(workspace.ensure('c1').dirs[''].entries.map((entry) => entry.name)).toEqual(['node_modules', 'src', 'a.ts', 'z.ts'])
  })

  it('首次文件内容更新会触发响应式计算', async () => {
    let resolveRead!: (value: FileContent) => void
    vi.spyOn(api.files, 'content').mockImplementation(() => new Promise((resolve) => { resolveRead = resolve }))
    const workspace = useWorkspaceStore()
    const opening = workspace.openFile('c1', 'README.md')
    const etag = computed(() => workspace.openFileState('c1')?.content?.etag || 'empty')
    expect(etag.value).toBe('empty')
    resolveRead(content('etag-1'))
    await opening
    expect(etag.value).toBe('etag-1')
  })

  it('刷新携带 ETag，304 保留原内容；404 标记文件删除', async () => {
    const read = vi.spyOn(api.files, 'content')
      .mockResolvedValueOnce(content('etag-1'))
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new ApiError({ code: 'NOT_FOUND', status: 404 }))
    const workspace = useWorkspaceStore()
    await workspace.openFile('c1', 'README.md')
    await workspace.openFile('c1', 'README.md', true)
    expect(read.mock.calls[1][2]).toBe('etag-1')
    expect(workspace.openFileState('c1')?.content?.content).toBe('# 项目说明')
    expect(workspace.openFileState('c1')?.deleted).toBe(false)

    await workspace.openFile('c1', 'README.md', true)
    expect(workspace.openFileState('c1')?.deleted).toBe(true)
  })

  it('默认优先打开根目录 README，而不是任意改动文件或普通文件', async () => {
    vi.spyOn(api.files, 'tree').mockResolvedValue([
      { name: 'app.ts', path: 'app.ts', type: 'file', size: 1, mtime: '' },
      { name: 'README.md', path: 'README.md', type: 'file', size: 12, mtime: '' },
    ])
    const read = vi.spyOn(api.files, 'content').mockResolvedValue(content('etag-1'))
    const workspace = useWorkspaceStore()

    await workspace.ensureDefault('c1', new Set(['src/changed.ts']))

    expect(workspace.ensure('c1').openPath).toBe('README.md')
    expect(read).toHaveBeenCalledWith('c1', 'README.md', undefined)
    expect(workspace.openFileState('c1')?.content?.content).toBe('# 项目说明')
  })

  it('变更后刷新根目录、已展开目录和已打开文件，不请求折叠目录', async () => {
    const tree = vi.spyOn(api.files, 'tree').mockImplementation(async (_cid, path = '') => path === ''
      ? [
          { name: 'src', path: 'src', type: 'dir', size: 0, mtime: '' },
          { name: 'README.md', path: 'README.md', type: 'file', size: 12, mtime: '' },
        ]
      : [{ name: 'App.ts', path: 'src/App.ts', type: 'file', size: 20, mtime: '' }])
    const read = vi.spyOn(api.files, 'content').mockResolvedValue(content('etag-1'))
    const workspace = useWorkspaceStore()
    await workspace.loadDir('c1')
    await workspace.loadDir('c1', 'src')
    await workspace.openFile('c1', 'README.md')
    workspace.ensure('c1').dirs.collapsed = {
      path: 'collapsed', entries: [], loaded: true, loading: false, expanded: false,
    }
    tree.mockClear()
    read.mockClear()
    read.mockResolvedValue({ ...content('etag-2'), content: '# 变更后的说明' })

    await workspace.refreshAll('c1')

    expect(tree.mock.calls.map((call) => call[1]).sort()).toEqual(['', 'src'])
    expect(tree).not.toHaveBeenCalledWith('c1', 'collapsed')
    expect(read).toHaveBeenCalledOnce()
    expect(read).toHaveBeenCalledWith('c1', 'README.md', 'etag-1')
    expect(workspace.openFileState('c1')?.content?.content).toBe('# 变更后的说明')
    expect(workspace.openFileState('c1')?.content?.etag).toBe('etag-2')
  })
})
