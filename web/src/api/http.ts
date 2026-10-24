import { ApiError, asApiError } from './errors'

export interface RequestOptions {
  signal?: AbortSignal
  timeoutMs?: number
  headers?: Record<string, string>
}

type UnauthorizedHandler = () => void
let unauthorizedHandler: UnauthorizedHandler = () => undefined

export function setUnauthorizedHandler(handler: UnauthorizedHandler): void {
  unauthorizedHandler = handler
}

function defaultCode(status: number): string {
  if (status === 401) return 'UNAUTHENTICATED'
  if (status === 404) return 'NOT_FOUND'
  if (status === 413) return 'TOO_LARGE'
  if (status === 429) return 'RATE_LIMITED'
  if (status >= 500) return 'INTERNAL'
  return `HTTP_${status}`
}

export function parseRetryAfter(value: string | null, now = Date.now()): number | undefined {
  if (!value) return undefined
  const seconds = Number(value)
  if (Number.isFinite(seconds)) return Math.max(0, Math.ceil(seconds))
  const date = Date.parse(value)
  return Number.isNaN(date) ? undefined : Math.max(0, Math.ceil((date - now) / 1000))
}

export async function request<T>(method: string, url: string, body?: unknown, options: RequestOptions = {}): Promise<T> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort('timeout'), options.timeoutMs ?? 15_000)
  const abortFromCaller = () => controller.abort(options.signal?.reason)
  options.signal?.addEventListener('abort', abortFromCaller, { once: true })

  try {
    const headers: Record<string, string> = { Accept: 'application/json', ...options.headers }
    const init: RequestInit = {
      method,
      credentials: 'include',
      headers,
      signal: controller.signal,
    }
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json'
      init.body = JSON.stringify(body)
    }

    let response: Response
    try {
      response = await fetch(url, init)
    } catch (error) {
      if (controller.signal.aborted) {
        const timedOut = controller.signal.reason === 'timeout'
        throw new ApiError({ code: timedOut ? 'TIMEOUT' : 'REQUEST_ABORTED', status: 0, network: true })
      }
      throw asApiError(error)
    }

    if (response.status === 304) return undefined as T
    const text = response.status === 204 ? '' : await response.text()
    let payload: unknown
    if (text) {
      try { payload = JSON.parse(text) } catch { payload = text }
    }

    if (!response.ok) {
      const errorBody = typeof payload === 'object' && payload !== null && 'error' in payload
        ? (payload as { error?: { code?: string; message?: string } }).error
        : undefined
      const code = errorBody?.code || defaultCode(response.status)
      if (response.status === 401 && !url.endsWith('/api/login')) unauthorizedHandler()
      throw new ApiError({
        code,
        status: response.status,
        message: errorBody?.message,
        serverMessage: errorBody?.message,
        retryAfter: parseRetryAfter(response.headers.get('Retry-After')),
      })
    }
    return payload as T
  } finally {
    window.clearTimeout(timeout)
    options.signal?.removeEventListener('abort', abortFromCaller)
  }
}

export async function requestText(url: string, options: RequestOptions = {}): Promise<string> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort('timeout'), options.timeoutMs ?? 15_000)
  const abortFromCaller = () => controller.abort(options.signal?.reason)
  options.signal?.addEventListener('abort', abortFromCaller, { once: true })
  try {
    let response: Response
    try {
      response = await fetch(url, {
        credentials: 'include',
        headers: { Accept: 'text/plain', ...options.headers },
        signal: controller.signal,
      })
    } catch (error) {
      if (controller.signal.aborted) {
        throw new ApiError({ code: controller.signal.reason === 'timeout' ? 'TIMEOUT' : 'REQUEST_ABORTED', status: 0 })
      }
      throw asApiError(error)
    }
    const text = await response.text()
    if (!response.ok) {
      let body: { error?: { code?: string; message?: string } } | undefined
      try { body = JSON.parse(text) } catch { /* 文本错误由状态码归一 */ }
      if (response.status === 401) unauthorizedHandler()
      throw new ApiError({
        code: body?.error?.code || defaultCode(response.status),
        status: response.status,
        message: body?.error?.message,
        retryAfter: parseRetryAfter(response.headers.get('Retry-After')),
      })
    }
    return text
  } finally {
    window.clearTimeout(timeout)
    options.signal?.removeEventListener('abort', abortFromCaller)
  }
}

export function queryString(values: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams()
  Object.entries(values).forEach(([key, value]) => {
    if (value !== undefined && value !== '') params.set(key, String(value))
  })
  const result = params.toString()
  return result ? `?${result}` : ''
}
