// 与 pkg/contract/api.go、wsd.go 对齐的浏览器侧契约。
export type RunStatus =
  | 'queued'
  | 'dispatching'
  | 'running'
  | 'cancelling'
  | 'finalizing'
  | 'finished'
  | 'failed'
  | 'cancelled'

export type ConvStatus = 'idle' | 'queued' | 'running' | 'error'
export type PreviewStatus = 'none' | 'starting' | 'ready' | 'failed'
export type FileChangeKind = 'A' | 'M' | 'D'

export interface LoginRequest { username: string; password: string }
export interface UserView { user_id: string; name: string }
export interface ModelView {
  id: string
  name: string
  tags: string[]
  desc: string
  default: boolean
}
export interface RunBrief { run_id: string; status: RunStatus }
export interface PreviewBrief {
  status: PreviewStatus
  commit?: string
  generation: number
}
export interface ConversationView {
  id: string
  title: string
  model: string
  status: ConvStatus
  last_active: string
  created_at: string
  run?: RunBrief
  preview?: PreviewBrief
}
export interface ConversationDetail extends ConversationView {
  sandbox: { status: string }
  preview: PreviewBrief
  last_seq: number
  active_run?: RunBrief
}
export interface CreateConversationRequest { title?: string; model?: string; template?: string }
export interface UpdateConversationRequest { title?: string; model?: string }
export interface AcceptRequest {
  client_request_id: string
  content: string
  model?: string
}
export interface AcceptResult {
  run_id: string
  message_id: string
  seq: number
  status: RunStatus
}
export interface MessageView {
  id: string
  role: string
  content: string
  run_id: string
  created_at: string
}
export interface Usage { prompt: number; completion: number }
export interface RunView {
  run_id: string
  status: RunStatus
  checkpoint_status: string
  commit?: string
  error_code?: string
  error_message?: string
  generation: number
  usage: Usage
  started_at?: string
  ended_at?: string
}
export interface FileChange { path: string; change: FileChangeKind }
export interface RunEnd {
  commit?: string
  checkpoint_status: string
  files: FileChange[]
  files_total: number
  duration_ms: number
  usage: Usage
  error_code?: string
}
export interface PreviewView {
  status: PreviewStatus
  url?: string
  commit?: string
  generation: number
  stale: boolean
}
export interface Page<T> { items: T[]; next_cursor: string }
export interface EventRecord {
  conv_id: string
  seq: number
  run_id?: string
  source: number
  runtime_epoch?: string
  agent_session_id?: string
  rt_seq?: number
  type: string
  payload: unknown
  created_at: string
}

export type RunStatusItemType = 'run_accepted' | 'run_started' | 'run_cancelling' | 'run_finalizing'
export type RunEndItemType = 'run_finished' | 'run_failed' | 'run_cancelled'
export type UIItem =
  | { key: string; type: RunStatusItemType; data: Record<string, never> }
  | { key: string; type: RunEndItemType; data: RunEnd }
  | { key: string; type: 'text'; data: { content: string } }
  | { key: string; type: 'tool_call'; data: { call_id: string; name: string; summary: string; args: string } }
  | { key: string; type: 'tool_result'; data: { call_id: string; name: string; content: string; is_error: boolean; truncated: boolean } }
  | { key: string; type: 'notice'; data: { text: string } }
  | { key: string; type: 'model_changed'; data: { from: string; to: string } }
  | { key: string; type: 'progress'; data: { text: string } }
  | { key: string; type: 'text_delta'; data: { text: string } }
  | { key: string; type: 'files_changed'; data: { paths: string[] } }
  | { key: string; type: 'preview_status'; data: { status: PreviewStatus; generation: number; commit?: string; error_code?: string } }

export interface UIFrame {
  seq?: number
  run_id?: string
  ts: string
  items: UIItem[]
}

export interface FileEntry {
  name: string
  path: string
  type: 'file' | 'dir' | 'symlink'
  size: number
  mtime: string
}
export interface FileContent {
  path: string
  size: number
  binary: boolean
  truncated: boolean
  content: string
  etag: string
}

export const ACTIVE_RUN_STATUSES: RunStatus[] = ['queued', 'dispatching', 'running', 'cancelling', 'finalizing']
export function isRunActive(status?: RunStatus): boolean {
  return !!status && ACTIVE_RUN_STATUSES.includes(status)
}
export function isRunTerminal(status?: RunStatus): boolean {
  return status === 'finished' || status === 'failed' || status === 'cancelled'
}
