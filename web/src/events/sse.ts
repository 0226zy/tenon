import type { UIFrame } from '@/types/contract'
import type { SseConnectionState } from './reducer'

export type SseState = Exclude<SseConnectionState, 'closed'>
type FrameListener = (cid: string, frame: UIFrame, temp: boolean) => void
type StateListener = (cid: string, state: SseState) => void
type Unsubscribe = () => void

interface Connection {
  cid: string
  after: number
  source?: EventSource
  retryTimer?: number
  attempts: number
  usedAt: number
  state: SseState
}

export type EventSourceFactory = (url: string) => EventSource

export class SseManager {
  private readonly connections = new Map<string, Connection>()
  private readonly frameListeners = new Set<FrameListener>()
  private readonly stateListeners = new Set<StateListener>()
  private browserEventsBound = false
  private browserOffline = false

  constructor(
    private readonly factory: EventSourceFactory = (url) => new EventSource(url, { withCredentials: true }),
    private readonly maxConnections = 6,
  ) {}

  private readonly onBrowserOffline = (): void => {
    this.browserOffline = true
    for (const connection of this.connections.values()) {
      connection.source?.close()
      connection.source = undefined
      if (connection.retryTimer !== undefined) window.clearTimeout(connection.retryTimer)
      connection.retryTimer = undefined
      connection.state = 'retrying'
      this.emitState(connection.cid, 'retrying')
    }
  }

  private readonly onBrowserOnline = (): void => {
    this.browserOffline = false
    for (const connection of this.connections.values()) {
      if (connection.state !== 'retrying') continue
      if (connection.retryTimer !== undefined) window.clearTimeout(connection.retryTimer)
      connection.retryTimer = undefined
      connection.attempts = 0
      this.connect(connection.cid)
    }
  }

  private bindBrowserEvents(): void {
    if (this.browserEventsBound || typeof window === 'undefined') return
    this.browserOffline = typeof navigator !== 'undefined' && navigator.onLine === false
    window.addEventListener('offline', this.onBrowserOffline)
    window.addEventListener('online', this.onBrowserOnline)
    this.browserEventsBound = true
  }

  private unbindBrowserEvents(): void {
    if (!this.browserEventsBound || typeof window === 'undefined') return
    window.removeEventListener('offline', this.onBrowserOffline)
    window.removeEventListener('online', this.onBrowserOnline)
    this.browserEventsBound = false
    this.browserOffline = false
  }

  open(cid: string, after = 0): void {
    const existing = this.connections.get(cid)
    if (existing) {
      existing.after = Math.max(existing.after, after)
      existing.usedAt = Date.now()
      return
    }
    this.bindBrowserEvents()
    const connection: Connection = {
      cid,
      after,
      attempts: 0,
      usedAt: Date.now(),
      state: 'connecting',
    }
    this.connections.set(cid, connection)
    this.connect(cid)
    this.evictLeastRecentlyUsed(cid)
  }

  private connect(cid: string): void {
    const connection = this.connections.get(cid)
    if (!connection) return
    if (this.browserOffline) {
      connection.state = 'retrying'
      this.emitState(cid, 'retrying')
      return
    }
    connection.retryTimer = undefined
    connection.state = 'connecting'
    this.emitState(cid, 'connecting')
    const url = `/api/conversations/${encodeURIComponent(cid)}/events?after=${encodeURIComponent(connection.after)}`
    const source = this.factory(url)
    connection.source = source

    source.onopen = () => {
      if (this.connections.get(cid) !== connection || connection.source !== source || this.browserOffline) return
      connection.attempts = 0
      connection.usedAt = Date.now()
      connection.state = 'open'
      this.emitState(cid, 'open')
    }
    source.onmessage = (event) => this.receive(connection, source, event, false)
    source.addEventListener('ui', (event) => this.receive(connection, source, event as MessageEvent, false))
    source.addEventListener('temp', (event) => this.receive(connection, source, event as MessageEvent, true))
    // 某些实现会发送命名 ping；注释形式 ': ping' 由浏览器原生处理。
    source.addEventListener('ping', () => {
      if (connection.source === source) connection.usedAt = Date.now()
    })
    source.onerror = () => this.disconnect(connection, source)
  }

  private disconnect(connection: Connection, source: EventSource): void {
    const cid = connection.cid
    if (this.connections.get(cid) !== connection || connection.source !== source) return
    source.close()
    connection.source = undefined
    if (connection.retryTimer !== undefined) window.clearTimeout(connection.retryTimer)
    connection.state = 'retrying'
    this.emitState(cid, 'retrying')
    if (this.browserOffline) return
    const backoff = Math.min(30, 2 ** connection.attempts)
    connection.attempts += 1
    const delay = Math.max(1, backoff)
    connection.retryTimer = window.setTimeout(() => {
      if (this.connections.get(cid) === connection) this.connect(cid)
    }, delay * 1000)
  }

  private receive(connection: Connection, source: EventSource, event: MessageEvent<string>, temp: boolean): void {
    if (connection.source !== source || this.browserOffline) return
    this.handleMessage(connection, event, temp)
  }

  private handleMessage(connection: Connection, event: MessageEvent<string>, temp: boolean): void {
    if (this.connections.get(connection.cid) !== connection) return
    try {
      const frame = JSON.parse(event.data) as UIFrame
      if (!temp && frame.seq === undefined && event.lastEventId) {
        const seq = Number(event.lastEventId)
        if (Number.isSafeInteger(seq)) frame.seq = seq
      }
      if (!temp && typeof frame.seq === 'number') {
        connection.after = Math.max(connection.after, frame.seq)
      }
      connection.usedAt = Date.now()
      this.frameListeners.forEach((listener) => listener(connection.cid, frame, temp))
    } catch (error) {
      console.warn('[proto-web] 无法解析 SSE 帧，已忽略', error)
    }
  }

  private emitState(cid: string, state: SseState): void {
    this.stateListeners.forEach((listener) => listener(cid, state))
  }

  private evictLeastRecentlyUsed(protectedCid: string): void {
    while (this.connections.size > this.maxConnections) {
      const candidate = [...this.connections.values()]
        .filter((connection) => connection.cid !== protectedCid)
        .sort((a, b) => a.usedAt - b.usedAt)[0]
      if (!candidate) return
      this.close(candidate.cid)
    }
  }

  close(cid: string): void {
    const connection = this.connections.get(cid)
    if (!connection) return
    connection.source?.close()
    if (connection.retryTimer !== undefined) window.clearTimeout(connection.retryTimer)
    this.connections.delete(cid)
    if (this.connections.size === 0) this.unbindBrowserEvents()
  }

  closeAll(): void {
    ;[...this.connections.keys()].forEach((cid) => this.close(cid))
  }

  has(cid: string): boolean { return this.connections.has(cid) }
  get activeCount(): number { return this.connections.size }
  getState(cid: string): SseConnectionState { return this.connections.get(cid)?.state || 'closed' }

  onFrame(listener: FrameListener): Unsubscribe {
    this.frameListeners.add(listener)
    return () => this.frameListeners.delete(listener)
  }

  onState(listener: StateListener): Unsubscribe {
    this.stateListeners.add(listener)
    return () => this.stateListeners.delete(listener)
  }
}

export const sseManager = new SseManager()
