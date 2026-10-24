import type { ToolItem } from '@/events/reducer'

export function toolFilePath(item: Pick<ToolItem, 'call'>): string {
  const call = item.call
  if (!call || !['write_file', 'edit_file'].includes(call.name)) return ''
  try {
    const args = JSON.parse(call.args) as Record<string, unknown>
    const path = args.path || args.file_path || args.filePath
    if (typeof path === 'string') return path
  } catch { /* 截断的 JSON 使用 summary 兜底 */ }
  return call.summary.trim()
}

export function isBashTool(name: string): boolean {
  return ['bash', 'shell', 'run_bash', 'run_command'].includes(name)
}
