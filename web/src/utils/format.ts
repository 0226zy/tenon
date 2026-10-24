import type { ConversationView, RunStatus } from '@/types/contract'

export const RUN_LABELS: Record<RunStatus, string> = {
  queued: '排队中',
  dispatching: '准备执行',
  running: '执行中',
  cancelling: '取消中',
  finalizing: '收尾中',
  finished: '已完成',
  failed: '执行失败',
  cancelled: '已取消',
}

export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const seconds = Math.round(ms / 1000)
  if (seconds < 60) return `${Math.max(1, seconds)} 秒`
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  if (minutes < 60) return rest ? `${minutes} 分 ${rest} 秒` : `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  return `${hours} 小时 ${minutes % 60} 分`
}

export function formatTokens(value: number): string {
  if (value >= 100_000_000) return `${(value / 100_000_000).toFixed(1)} 亿`
  if (value >= 10_000) return `${(value / 10_000).toFixed(1)} 万`
  return new Intl.NumberFormat('zh-CN').format(value)
}

export function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / 1024 / 1024).toFixed(1)} MB`
}

export function relativeTime(value: string, now = Date.now()): string {
  const time = Date.parse(value)
  if (Number.isNaN(time)) return ''
  const diff = Math.max(0, now - time)
  if (diff < 60_000) return '刚刚'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`
  if (diff < 7 * 86_400_000) return `${Math.floor(diff / 86_400_000)} 天前`
  return new Intl.DateTimeFormat('zh-CN', { month: 'short', day: 'numeric' }).format(time)
}

export function conversationTitle(conversation?: Pick<ConversationView, 'title'>): string {
  return conversation?.title.trim() || '未命名对话'
}

export function fileLanguage(path: string): string {
  const ext = path.split('.').pop()?.toLowerCase()
  const languages: Record<string, string> = {
    ts: 'typescript', tsx: 'typescript', js: 'javascript', jsx: 'javascript', mjs: 'javascript',
    json: 'json', html: 'html', css: 'css', scss: 'scss', less: 'less', vue: 'html',
    md: 'markdown', markdown: 'markdown', go: 'go', mod: 'go', sql: 'sql', sqlite: 'sql',
    yaml: 'yaml', yml: 'yaml', toml: 'ini', ini: 'ini', sh: 'shell', bash: 'shell',
    py: 'python', java: 'java', rs: 'rust', xml: 'xml', svg: 'xml', dockerfile: 'dockerfile',
  }
  if (path.toLowerCase().endsWith('dockerfile')) return 'dockerfile'
  return languages[ext || ''] || 'plaintext'
}
