import { afterEach, describe, expect, it, vi } from 'vitest'
import { SseManager } from './sse'

class FakeEventSource {
  static instances: FakeEventSource[] = []
  readonly url: string
  onopen: ((event: Event) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent<string>) => void) | null = null
  closed = false
  private listeners = new Map<string, Set<(event: MessageEvent<string>) => void>>()

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }
  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set())
    this.listeners.get(type)?.add(listener)
  }
  close(): void { this.closed = true }
  open(): void { this.onopen?.(new Event('open')) }
  emit(type: string, data: unknown, lastEventId = ''): void {
    const event = new MessageEvent(type, { data: JSON.stringify(data), lastEventId }) as MessageEvent<string>
    if (type === 'message') this.onmessage?.(event)
    this.listeners.get(type)?.forEach((listener) => listener(event))
  }
  fail(): void { this.onerror?.(new Event('error')) }
}

const factory = (url: string) => new FakeEventSource(url) as unknown as EventSource

describe('SseManager', () => {
  afterEach(() => {
    FakeEventSource.instances = []
    vi.useRealTimers()
  })

  it('区分 ui/temp，重连沿用 lastSeq，退避限制在 1–30 秒', async () => {
    vi.useFakeTimers()
    const manager = new SseManager(factory, 6)
    const onFrame = vi.fn()
    const onState = vi.fn()
    manager.onFrame(onFrame)
    manager.onState(onState)
    manager.open('c1', 0)
    const first = FakeEventSource.instances[0]
    first.open()
    first.emit('ui', { seq: 4, run_id: 'r1', ts: 'now', items: [] }, '4')
    first.emit('temp', { run_id: 'r1', ts: 'now', items: [{ key: 'p', type: 'progress', data: { text: 'working' } }] })
    expect(onFrame.mock.calls[0][2]).toBe(false)
    expect(onFrame.mock.calls[1][2]).toBe(true)

    first.fail()
    expect(onState).toHaveBeenCalledWith('c1', 'retrying')
    await vi.advanceTimersByTimeAsync(999)
    expect(FakeEventSource.instances).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(FakeEventSource.instances).toHaveLength(2)
    expect(FakeEventSource.instances[1].url).toContain('after=4')
    manager.closeAll()
  })

  it('浏览器离线立即提示，在线后从旧游标恢复且拒绝旧连接事件', async () => {
    vi.useFakeTimers()
    const manager = new SseManager(factory, 6)
    const onFrame = vi.fn()
    const onState = vi.fn()
    manager.onFrame(onFrame)
    manager.onState(onState)
    manager.open('c1', 0)
    const first = FakeEventSource.instances[0]
    first.open()
    first.emit('ui', { seq: 7, run_id: 'r1', ts: 'now', items: [] }, '7')

    window.dispatchEvent(new Event('offline'))
    expect(first.closed).toBe(true)
    expect(manager.getState('c1')).toBe('retrying')
    expect(onState).toHaveBeenCalledWith('c1', 'retrying')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(FakeEventSource.instances).toHaveLength(1)

    window.dispatchEvent(new Event('online'))
    expect(FakeEventSource.instances).toHaveLength(2)
    const second = FakeEventSource.instances[1]
    expect(second.url).toContain('after=7')
    second.open()
    first.emit('ui', { seq: 8, run_id: 'r1', ts: 'now', items: [] }, '8')
    expect(onFrame).toHaveBeenCalledTimes(1)
    second.emit('ui', { seq: 8, run_id: 'r1', ts: 'now', items: [] }, '8')
    expect(onFrame).toHaveBeenCalledTimes(2)
    manager.closeAll()
  })

  it('最多保留 6 条连接并按最近使用淘汰', () => {
    const manager = new SseManager(factory, 6)
    for (let index = 1; index <= 7; index += 1) manager.open(`c${index}`, index)
    expect(manager.activeCount).toBe(6)
    expect(manager.has('c1')).toBe(false)
    expect(manager.has('c7')).toBe(true)
    expect(FakeEventSource.instances[0].closed).toBe(true)
    manager.closeAll()
  })
})
