import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '@/api'
import type { ConversationView } from '@/types/contract'
import { useConversationsStore } from './conversations'

function conversation(id: string, lastActive: string, overrides: Partial<ConversationView> = {}): ConversationView {
  return {
    id,
    title: `对话 ${id}`,
    model: 'model-1',
    status: 'idle',
    last_active: lastActive,
    created_at: '2026-10-24T09:00:00Z',
    ...overrides,
  }
}

describe('conversations store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('加载下一页时按 id 合并更新、去重并保持最近活跃排序', async () => {
    const list = vi.spyOn(api.conv, 'list')
      .mockResolvedValueOnce({
        items: [
          conversation('c1', '2026-10-24T10:01:00Z'),
          conversation('c2', '2026-10-24T10:02:00Z'),
        ],
        next_cursor: 'cursor-1',
      })
      .mockResolvedValueOnce({
        items: [
          conversation('c2', '2026-10-24T10:03:00Z', { title: '更新后的标题' }),
          conversation('c0', '2026-10-24T10:00:00Z'),
        ],
        next_cursor: 'cursor-2',
      })
    const conversations = useConversationsStore()
    await conversations.load()

    await conversations.loadMore()

    expect(list.mock.calls[1]?.slice(0, 2)).toEqual(['cursor-1', 30])
    expect(conversations.list.map((item) => item.id)).toEqual(['c2', 'c1', 'c0'])
    expect(conversations.list.filter((item) => item.id === 'c2')).toHaveLength(1)
    expect(conversations.list[0]?.title).toBe('更新后的标题')
    expect(conversations.nextCursor).toBe('cursor-2')
  })

  it('状态轮询合并且不截断后续分页，自动标题和具体 Run 状态立即可见', async () => {
    vi.spyOn(api.conv, 'list')
      .mockResolvedValueOnce({
        items: [conversation('c1', '2026-10-24T10:01:00Z', { title: '' })],
        next_cursor: 'cursor-1',
      })
      .mockResolvedValueOnce({
        items: [conversation('c0', '2026-10-24T10:00:00Z')],
        next_cursor: 'cursor-2',
      })
      .mockResolvedValueOnce({
        items: [conversation('c1', '2026-10-24T10:02:00Z', {
          title: '做一个待办应用',
          status: 'running',
          run: { run_id: 'r1', status: 'finalizing' },
        })],
        next_cursor: 'first-page-only',
      })
    const conversations = useConversationsStore()
    await conversations.load()
    await conversations.loadMore()
    conversations.select('c1')

    await conversations.refreshStatus()

    expect(conversations.current?.title).toBe('做一个待办应用')
    expect(conversations.current?.status).toBe('running')
    expect(conversations.current?.run?.status).toBe('finalizing')
    expect(conversations.list.some((item) => item.id === 'c0')).toBe(true)
    expect(conversations.nextCursor).toBe('cursor-2')
  })
})
