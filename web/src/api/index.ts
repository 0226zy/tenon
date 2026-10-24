import type {
  AcceptRequest,
  AcceptResult,
  ConversationDetail,
  ConversationView,
  CreateConversationRequest,
  FileContent,
  FileEntry,
  MessageView,
  ModelView,
  Page,
  PreviewView,
  RunBrief,
  RunView,
  UIFrame,
  UpdateConversationRequest,
  UserView,
} from '@/types/contract'
import { queryString, request, requestText } from './http'

const convPath = (cid: string) => `/api/conversations/${encodeURIComponent(cid)}`
const runPath = (cid: string, rid: string) => `${convPath(cid)}/runs/${encodeURIComponent(rid)}`

function asPage<T>(value: Page<T> | T[]): Page<T> {
  return Array.isArray(value) ? { items: value, next_cursor: '' } : value
}

export const api = {
  auth: {
    login: (username: string, password: string) => request<void>('POST', '/api/login', { username, password }),
    logout: () => request<void>('POST', '/api/logout'),
    me: () => request<UserView>('GET', '/api/me'),
  },
  models: {
    list: async () => {
      const result = await request<ModelView[] | Page<ModelView>>('GET', '/api/models')
      return Array.isArray(result) ? result : result.items
    },
  },
  conv: {
    create: (body: CreateConversationRequest) => request<ConversationView>('POST', '/api/conversations', body),
    list: async (cursor = '', limit = 30) => asPage(await request<Page<ConversationView> | ConversationView[]>(
      'GET', `/api/conversations${queryString({ cursor, limit })}`,
    )),
    get: (cid: string) => request<ConversationDetail>('GET', convPath(cid)),
    update: (cid: string, body: UpdateConversationRequest) => request<ConversationView>('PATCH', convPath(cid), body),
    remove: (cid: string) => request<void>('DELETE', convPath(cid)),
    messages: async (cid: string, before = '', limit = 100) => asPage(await request<Page<MessageView> | MessageView[]>(
      'GET', `${convPath(cid)}/messages${queryString({ before, limit })}`,
    )),
  },
  run: {
    send: (cid: string, body: AcceptRequest) => request<AcceptResult>('POST', `${convPath(cid)}/messages`, body),
    get: (cid: string, rid: string) => request<RunView>('GET', runPath(cid, rid)),
    cancel: (cid: string, rid: string) => request<RunBrief | RunView>('POST', `${runPath(cid, rid)}/cancel`),
  },
  events: {
    history: async (cid: string, options: { after?: number; before?: string; limit?: number } = {}) => asPage(
      await request<Page<UIFrame> | UIFrame[]>(
        'GET',
        `${convPath(cid)}/events/history${queryString({ after: options.after, before: options.before, limit: options.limit ?? 200 })}`,
      ),
    ),
  },
  files: {
    tree: async (cid: string, path = '') => {
      const result = await request<FileEntry[] | Page<FileEntry>>('GET', `${convPath(cid)}/files/tree${queryString({ path })}`)
      return Array.isArray(result) ? result : result.items
    },
    content: (cid: string, path: string, ifNoneMatch?: string) => request<FileContent | undefined>(
      'GET',
      `${convPath(cid)}/files/content${queryString({ path })}`,
      undefined,
      { headers: ifNoneMatch ? { 'If-None-Match': ifNoneMatch } : undefined },
    ),
  },
  preview: {
    entry: (cid: string) => request<PreviewView>('GET', `${convPath(cid)}/preview`),
    restart: (cid: string) => request<PreviewView | void>('POST', `${convPath(cid)}/preview/restart`),
    logs: (cid: string, tail = 200) => requestText(`${convPath(cid)}/preview/logs${queryString({ tail })}`),
  },
}
