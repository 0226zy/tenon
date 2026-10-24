export const ERROR_MESSAGES: Record<string, string> = {
  UNAUTHENTICATED: '登录已失效，请重新登录',
  NOT_FOUND: '对话不存在或已删除',
  RUN_IN_PROGRESS: '当前对话正在执行，请等待完成或先取消',
  IDEMPOTENCY_CONFLICT: '请求编号冲突，请重新发送',
  QUOTA_EXCEEDED: '排队任务已达上限，请稍后再试',
  CONV_LIMIT: '对话数已达上限，请删除不用的对话',
  EMPTY: '请输入内容',
  TOO_LONG: '内容超过 20000 字，请精简',
  BAD_MODEL: '模型不可用，请重新选择',
  SANDBOX_STARTING: '工作区启动中',
  SANDBOX_BUSY: '平台资源繁忙，请稍后再试',
  TOO_LARGE: '文件过大，不在线展示',
  RATE_LIMITED: '操作过于频繁，请稍后再试',
  QUEUE_TIMEOUT: '排队超过 10 分钟未开始，已取消',
  RUN_TIMEOUT: '执行超过 30 分钟，已自动停止',
  SANDBOX_START_FAILED: '工作区启动失败',
  CONTAINER_STOPPED: '执行未能正常停止，工作区已重启；本轮改动未单独形成版本',
  RUNTIME_RESTARTED: '执行环境意外重启，本轮已终止',
  LLM_ERROR: '模型服务暂时不可用',
  ITER_LIMIT: '任务步骤过多已停止',
  TOKEN_BUDGET: '本轮用量超过上限已停止',
  LIVELOCK: '检测到重复操作已停止',
  CIRCUIT_OPEN: '连续出错次数过多，已停止',
  PORT_CONFLICT: '预览端口被占用，无法启动',
  PREVIEW_TIMEOUT: '预览启动超时（90 秒）',
  PREVIEW_NOT_READY: '预览尚未就绪，请稍后重试',
  POPUP_BLOCKED: '浏览器拦截了新窗口，请允许弹出窗口后重试',
  BUILD_FAILED: '预览构建失败',
  BUILD_SLOT_TIMEOUT: '构建排队超时，请稍后重启预览',
  DISK_FULL: '工作区磁盘空间不足',
  CSRF_REJECTED: '页面安全校验失败，请刷新后重试',
  NETWORK_ERROR: '网络连接失败，请检查网络后重试',
  TIMEOUT: '请求超时，请稍后重试',
  REQUEST_ABORTED: '请求已取消',
  INTERNAL: '服务暂时不可用，请稍后重试',
  NOT_IMPLEMENTED: '即将开放',
}

export class ApiError extends Error {
  readonly code: string
  readonly status: number
  readonly retryAfter?: number
  readonly network: boolean
  readonly serverMessage?: string

  constructor(options: {
    code: string
    message?: string
    status: number
    retryAfter?: number
    network?: boolean
    serverMessage?: string
  }) {
    super(options.message || ERROR_MESSAGES[options.code] || '操作失败，请稍后重试')
    this.name = 'ApiError'
    this.code = options.code
    this.status = options.status
    this.retryAfter = options.retryAfter
    this.network = options.network ?? options.status === 0
    this.serverMessage = options.serverMessage
  }
}

export function asApiError(error: unknown): ApiError {
  if (error instanceof ApiError) return error
  if (error instanceof DOMException && error.name === 'AbortError') {
    return new ApiError({ code: 'REQUEST_ABORTED', status: 0, network: true })
  }
  return new ApiError({
    code: 'NETWORK_ERROR',
    status: 0,
    network: true,
    serverMessage: error instanceof Error ? error.message : String(error),
  })
}

export function errorMessage(error: unknown, context?: 'login'): string {
  const apiError = asApiError(error)
  if (context === 'login' && apiError.status === 401) return '用户名或密码错误'
  if (context === 'login' && apiError.status === 429) return '尝试次数过多，请 15 分钟后再试'
  const known = ERROR_MESSAGES[apiError.code]
  if (known) return known
  return `操作失败，请稍后重试（错误码 ${apiError.code}）`
}

export function runErrorMessage(code?: string): string {
  if (!code) return '执行失败，请重新执行或切换模型后重试'
  return ERROR_MESSAGES[code] || `执行失败（错误码 ${code}）`
}
