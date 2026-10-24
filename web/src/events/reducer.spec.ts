import { describe, expect, it, vi } from 'vitest'
import { emptyChatView, reduce, reduceHistory, viewItems, type ToolItem } from './reducer'
import type { UIFrame } from '@/types/contract'

function frame(seq: number | undefined, key: string, type: string, data: Record<string, unknown>, runId = 'r1'): UIFrame {
  return {
    seq, run_id: runId, ts: `2026-10-24T10:00:${String(seq ?? 0).padStart(2, '0')}.000Z`,
    items: [{ key, type, data } as UIFrame['items'][number]],
  }
}

describe('UI reducer', () => {
  it('按 (seq,key) 去重，重复帧不会重复生成卡片', () => {
    const initial = emptyChatView()
    const event = frame(7, 'm1', 'text', { content: '只出现一次' })
    const once = reduce(initial, event)
    const twice = reduce(once, event)
    const third = reduce(twice, frame(7, 'm1', 'text', { content: '只出现一次' }))
    expect(viewItems(third)).toHaveLength(1)
    expect(third.lastSeq).toBe(7)
    expect(third.seen.has('7:m1')).toBe(true)
    expect(once).not.toBe(initial)
    expect(initial.items.size).toBe(0)
  })

  it('delta 追加展示，并在同 Run 完整 text 到达时替换缓冲', () => {
    let view = emptyChatView()
    view = reduce(view, frame(1, 'run', 'run_accepted', {}))
    view = reduce(view, frame(undefined, 'd1', 'text_delta', { text: '你好' }), true)
    view = reduce(view, frame(undefined, 'd2', 'text_delta', { text: '，世界' }), true)
    expect(view.delta.r1).toBe('你好，世界')
    expect(viewItems(view).filter((item) => item.type === 'text' && item.streaming)).toHaveLength(1)

    view = reduce(view, frame(2, 'assistant-1', 'text', { content: '你好，世界！' }))
    const texts = viewItems(view).filter((item) => item.type === 'text')
    expect(texts).toHaveLength(1)
    expect(texts[0].type === 'text' && texts[0].content).toBe('你好，世界！')
    expect(texts[0].type === 'text' && texts[0].streaming).toBeFalsy()
    expect(view.delta.r1).toBeUndefined()
    expect(view.items.has('delta:r1')).toBe(false)
  })

  it('tool_result 先到时也能按 call_id 与后续 tool_call 配对', () => {
    let view = emptyChatView()
    view = reduce(view, frame(8, 'call-1', 'tool_result', {
      call_id: 'call-1', name: 'bash', content: 'failed output', is_error: true, truncated: true,
    }))
    view = reduce(view, frame(5, 'call-1', 'tool_call', {
      call_id: 'call-1', name: 'bash', summary: 'npm test', args: '{"command":"npm test"}',
    }))
    const items = viewItems(view)
    expect(items).toHaveLength(1)
    const tool = items[0] as ToolItem
    expect(tool.type).toBe('tool')
    expect(tool.callId).toBe('call-1')
    expect(tool.call?.summary).toBe('npm test')
    expect(tool.result?.content).toBe('failed output')
    expect(tool.result?.is_error).toBe(true)
    expect(tool.result?.truncated).toBe(true)
    expect(tool.seq).toBe(5)
  })

  it('历史前插按 seq 排序，且不回滚当前 Run、delta 和改动文件', () => {
    let view = emptyChatView()
    view = reduceHistory(view, [
      frame(4, 'run', 'run_started', {}, 'new-run'),
      frame(3, 'new-text', 'text', { content: '新记录' }, 'new-run'),
    ])
    view = reduce(view, frame(undefined, 'd', 'text_delta', { text: '缓冲' }, 'new-run'), true)
    view = reduce(view, frame(undefined, 'f', 'files_changed', { paths: ['src/new.ts'] }, 'new-run'), true)
    const currentSeq = view.lastSeq
    view = reduceHistory(view, [
      frame(2, 'old-end', 'run_finished', {
        checkpoint_status: 'done', files: [{ path: 'old.txt', change: 'A' }], files_total: 1,
        duration_ms: 1000, usage: { prompt: 1, completion: 1 },
      }, 'old-run'),
      frame(1, 'old-text', 'text', { content: '旧记录' }, 'old-run'),
    ], true)

    expect(viewItems(view).filter((item) => item.seq !== undefined).map((item) => item.seq)).toEqual([1, 2, 3])
    expect(view.runId).toBe('new-run')
    expect(view.runStatus).toBe('running')
    expect(view.lastSeq).toBe(currentSeq)
    expect(view.delta['new-run']).toBe('缓冲')
    expect([...view.changedFiles]).toEqual(['src/new.ts'])
    expect(view.earliestSeq).toBe(1)
  })

  it('未知类型只警告，不中断后续帧处理', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    const view = reduce(emptyChatView(), frame(1, 'x', 'future_event', { value: 1 }))
    expect(view.lastSeq).toBe(1)
    expect(view.warnings.at(-1)).toContain('future_event')
    expect(warn).toHaveBeenCalled()
  })

  it('同名工具的多个 call_id 各自配对，不串联调用参数和结果', () => {
    let view = emptyChatView()
    view = reduce(view, frame(1, 'a', 'tool_call', {
      call_id: 'call-a', name: 'bash', summary: 'first', args: '{"command":"first"}',
    }))
    view = reduce(view, frame(2, 'b', 'tool_call', {
      call_id: 'call-b', name: 'bash', summary: 'second', args: '{"command":"second"}',
    }))
    view = reduce(view, frame(3, 'b-result', 'tool_result', {
      call_id: 'call-b', name: 'bash', content: 'result-b', is_error: false, truncated: false,
    }))
    view = reduce(view, frame(4, 'a-result', 'tool_result', {
      call_id: 'call-a', name: 'bash', content: 'result-a', is_error: true, truncated: false,
    }))

    const tools = viewItems(view).filter((item): item is ToolItem => item.type === 'tool')
    expect(tools).toHaveLength(2)
    expect(tools.map((item) => item.callId)).toEqual(['call-a', 'call-b'])
    expect(tools[0].call?.summary).toBe('first')
    expect(tools[0].result?.content).toBe('result-a')
    expect(tools[0].result?.is_error).toBe(true)
    expect(tools[1].call?.summary).toBe('second')
    expect(tools[1].result?.content).toBe('result-b')
  })
})
