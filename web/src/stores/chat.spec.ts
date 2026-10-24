import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import { sseManager } from '@/events/sse'
import { useChatStore } from './chat'
import type { ChatView } from '@/events/reducer'

function accepted(runId: string, messageId: string) {
  return { run_id: runId, message_id: messageId, seq: 1, status: 'queued' as const }
}

describe('chat store 幂等发送', () => {
  let pinia: Pinia
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
  })
  afterEach(() => {
    sseManager.closeAll()
    vi.useRealTimers()
  })

  it('网络失败后重试复用 client_request_id，并保持乐观消息', async () => {
    const send = vi.spyOn(api.run, 'send')
      .mockRejectedValueOnce(new ApiError({ code: 'NETWORK_ERROR', status: 0 }))
      .mockResolvedValueOnce(accepted('run-1', 'message-1'))
    const chat = useChatStore()
    await expect(chat.send('c1', '创建一个待办应用')).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    const pending = chat.users('c1')[0]
    expect(pending.status).toBe('failed')
    expect(pending.content).toBe('创建一个待办应用')
    const requestId = pending.clientRequestId
    expect(requestId).toMatch(/^[0-9A-HJKMNP-TV-Z]{26}$/)

    await chat.retryMessage('c1', pending.id)
    expect(send).toHaveBeenCalledTimes(2)
    expect(send.mock.calls[0][1].client_request_id).toBe(requestId)
    expect(send.mock.calls[1][1].client_request_id).toBe(requestId)
    expect(send.mock.calls[0][1].content).toBe(send.mock.calls[1][1].content)
    expect(pending.status).toBe('accepted')
    expect(pending.runId).toBe('run-1')
  })

  it('只有终态重新执行才生成新请求号并新增用户消息', async () => {
    const send = vi.spyOn(api.run, 'send').mockResolvedValue(accepted('run-new', 'message-new'))
    const chat = useChatStore()
    chat.ingestMessages('c1', [{
      id: 'message-old', role: 'user', content: '原始需求', run_id: 'run-old', created_at: '2026-10-24T10:00:00Z',
    }])
    const view: ChatView = chat.ensure('c1')
    view.runId = 'run-old'
    view.runStatus = 'failed'

    await chat.retryRun('c1', 'run-old')
    const users = chat.users('c1')
    expect(users).toHaveLength(2)
    expect(users.at(-1)?.content).toBe('原始需求')
    expect(users.at(-1)?.clientRequestId).not.toBe('message-old')
    expect(send.mock.calls[0][1].client_request_id).toHaveLength(26)
  })

  it('HTTP 响应未知但 SSE 已接受时，不再误标发送失败', async () => {
    let rejectSend: (error: unknown) => void = () => undefined
    vi.spyOn(api.run, 'send').mockImplementation(() => new Promise((_, reject) => { rejectSend = reject }))
    const chat = useChatStore()
    const sending = chat.send('c1', '测试响应未知')
    const pending = chat.users('c1')[0]
    expect(pending.status).toBe('sending')
    pending.status = 'accepted'
    pending.runId = 'run-from-sse'
    rejectSend(new ApiError({ code: 'NETWORK_ERROR', status: 0 }))
    await expect(sending).resolves.toMatchObject({ status: 'accepted', runId: 'run-from-sse' })
    expect(pending.status).toBe('accepted')
    expect(pending.runId).toBe('run-from-sse')
  })
})

describe('chat store 断线恢复', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })
  afterEach(() => {
    sseManager.closeAll()
  })

  it('重新进入已加载对话时，先从旧游标补历史再推进高水位', async () => {
    vi.spyOn(api.conv, 'get').mockResolvedValue({
      id: 'c1',
      title: '测试对话',
      model: 'model-1',
      status: 'idle',
      last_active: '2026-10-24T10:00:00Z',
      created_at: '2026-10-24T10:00:00Z',
      sandbox: { status: 'ready' },
      preview: { status: 'none', generation: 0 },
      last_seq: 9,
    })
    const history = vi.spyOn(api.events, 'history').mockResolvedValue({ items: [], next_cursor: '' })
    vi.spyOn(api.preview, 'entry').mockResolvedValue({ status: 'none', generation: 0, stale: false })
    const open = vi.spyOn(sseManager, 'open').mockImplementation(() => undefined)
    const chat = useChatStore()
    const view = chat.ensure('c1')
    view.loaded = true
    view.lastSeq = 5

    await chat.enter('c1')

    expect(history).toHaveBeenCalledTimes(1)
    expect(history.mock.calls[0][1]).toEqual({ after: 5, limit: 200 })
    expect(chat.ensure('c1').lastSeq).toBe(9)
    expect(open).toHaveBeenCalledWith('c1', 9)
  })
})
