/** 只允许站内绝对路径，拒绝协议相对地址、反斜杠与登录页回跳环。 */
export function safeInternalRedirect(value: unknown, fallback = '/'): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || value.includes('\\')) {
    return fallback
  }
  try {
    const base = typeof window === 'undefined' ? 'http://localhost' : window.location.origin
    const parsed = new URL(value, base)
    if (parsed.origin !== base || parsed.pathname === '/login') return fallback
    return `${parsed.pathname}${parsed.search}${parsed.hash}`
  } catch {
    return fallback
  }
}
