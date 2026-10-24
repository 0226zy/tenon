import { afterEach, describe, expect, it, vi } from 'vitest'
import { parseRetryAfter, request, setUnauthorizedHandler } from './http'
import { ApiError } from './errors'
import { safeInternalRedirect } from '@/utils/navigation'

describe('HTTP 契约', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    setUnauthorizedHandler(() => undefined)
  })

  it('429 归一化 ApiError 并读取 Retry-After', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'RATE_LIMITED', message: 'slow down' } }), {
      status: 429, headers: { 'Retry-After': '7', 'Content-Type': 'application/json' },
    })))
    await expect(request('GET', '/api/test')).rejects.toMatchObject({ code: 'RATE_LIMITED', status: 429, retryAfter: 7 })
  })

  it('401 触发统一登出回跳钩子', async () => {
    const unauthorized = vi.fn()
    setUnauthorizedHandler(unauthorized)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'UNAUTHENTICATED' } }), { status: 401 })))
    await expect(request('GET', '/api/me')).rejects.toBeInstanceOf(ApiError)
    expect(unauthorized).toHaveBeenCalledOnce()
  })

  it('网络错误与超时类错误均转为可识别 ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    await expect(request('GET', '/api/test')).rejects.toMatchObject({ code: 'NETWORK_ERROR', status: 0, network: true })
  })

  it('Retry-After 支持秒数与 HTTP 日期', () => {
    const now = Date.parse('2026-10-24T10:00:00Z')
    expect(parseRetryAfter('3', now)).toBe(3)
    expect(parseRetryAfter('Sat, 24 Oct 2026 10:00:05 GMT', now)).toBe(5)
  })

  it('登录回跳仅允许站内路径', () => {
    expect(safeInternalRedirect('/c/abc?tab=code#files')).toBe('/c/abc?tab=code#files')
    expect(safeInternalRedirect('https://evil.example/')).toBe('/')
    expect(safeInternalRedirect('//evil.example/path')).toBe('/')
    expect(safeInternalRedirect('/\\evil.example')).toBe('/')
    expect(safeInternalRedirect('/login')).toBe('/')
  })
})
