import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import path from 'node:path'

const CONTROL_CONFIG = 'deploy/control.e2e.yaml'
const SECONDARY_MODEL = 'local-demo-secondary'
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

export async function runExtendedP0({
  root,
  outDir,
  baseURL,
  chromePath,
  chromium,
  browser,
  context,
  page,
  password,
  conversationID,
  renamedTitle,
  previewHost,
  secondGeneration,
  pageErrors,
  apiGet,
  toolCards,
}) {
  const coverage = {}
  const screenshots = []
  const notCovered = []
  const mainURL = `${baseURL}/c/${conversationID}`
  const messagesPath = `/api/conversations/${conversationID}/messages`
  const previewPath = `/api/conversations/${conversationID}/preview`
  const previewFrame = page.frameLocator('iframe[title="生成应用预览"]')

  function watch(target) {
    target.setDefaultTimeout(30_000)
    target.on('pageerror', (error) => pageErrors.push(error.message))
    return target
  }

  async function capture(name, target = page) {
    await target.screenshot({ path: path.join(outDir, `${name}.png`), fullPage: true })
    screenshots.push(`${name}.png`)
  }

  async function raw(method, pathname, body) {
    const cookies = await context.cookies(baseURL)
    const headers = {
      Accept: 'application/json',
      cookie: cookies.map((cookie) => `${cookie.name}=${cookie.value}`).join('; '),
    }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    return fetch(`${baseURL}${pathname}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(30_000),
    })
  }

  async function json(method, pathname, body, expected = 200) {
    const response = await raw(method, pathname, body)
    const text = await response.text()
    assert.equal(response.status, expected, `${method} ${pathname}：${response.status} ${text.slice(0, 500)}`)
    return text ? JSON.parse(text) : null
  }

  async function errorPayload(response, expected, label) {
    const text = await response.text()
    assert.equal(response.status, expected, `${label}：${response.status} ${text.slice(0, 500)}`)
    return text ? JSON.parse(text) : {}
  }

  function control(args, withPassword = false) {
    const result = spawnSync(path.join(root, 'bin', 'control'), args, {
      cwd: root,
      env: withPassword ? { ...process.env, CONTROL_PASSWORD: password } : process.env,
      encoding: 'utf8',
      maxBuffer: 4 * 1024 * 1024,
    })
    assert.ok(!result.error, `control ${args.join(' ')}：${result.error?.message}`)
    assert.equal(result.status, 0, `control ${args.join(' ')}：${result.stderr || result.stdout}`)
    return result.stdout
  }

  async function submitLogin(target, username, secret) {
    await target.locator('#username').fill(username)
    await target.locator('#password').fill(secret)
    const responsePromise = target.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/api/login' && response.request().method() === 'POST'
    })
    await target.getByRole('button', { name: '登录', exact: true }).click()
    return responsePromise
  }

  async function openPreview() {
    await page.getByRole('tab', { name: /预览/ }).click()
  }

  async function waitPreviewGeneration(generation, expectedText = '') {
    await openPreview()
    await page.locator('.preview-status--ready').waitFor({ timeout: 180_000 })
    await page.locator('.preview-status__version', { hasText: `#${generation}` }).waitFor({ timeout: 180_000 })
    await previewFrame.getByRole('heading', { name: '原型已真实修改' }).waitFor({ timeout: 60_000 })
    if (expectedText) await previewFrame.getByText(expectedText, { exact: false }).waitFor({ timeout: 60_000 })
  }

  async function sendAndWait(content, finishedBefore, timeout = 240_000) {
    const acceptedPromise = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === messagesPath && response.request().method() === 'POST'
    })
    await page.getByRole('textbox', { name: '需求内容' }).fill(content)
    await page.getByRole('button', { name: '发送', exact: true }).click()
    const acceptedResponse = await acceptedPromise
    const acceptedText = await acceptedResponse.text()
    assert.equal(acceptedResponse.status(), 202, `发送 ${content}：${acceptedResponse.status()} ${acceptedText.slice(0, 500)}`)
    const accepted = JSON.parse(acceptedText)
    await page.locator('.run-end-card--finished').nth(finishedBefore).waitFor({ timeout })
    return accepted
  }

  async function poll(read, predicate, label, timeout = 60_000) {
    const deadline = Date.now() + timeout
    let last
    let lastError
    while (Date.now() < deadline) {
      try {
        last = await read()
        lastError = undefined
        if (predicate(last)) return last
      } catch (error) {
        lastError = error
      }
      await sleep(750)
    }
    throw new Error(`${label} 超时：${JSON.stringify(last)}${lastError ? `；${lastError.message}` : ''}`)
  }

  async function verifyAuthentication() {
    for (const username of ['e2e-lock', 'e2e-disabled']) {
      control(['user', 'add', '-config', CONTROL_CONFIG, '-user', username, '-name', username], true)
    }
    control(['user', 'disable', '-config', CONTROL_CONFIG, '-user', 'e2e-disabled'])

    const disabledContext = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: 'zh-CN' })
    try {
      const disabledPage = watch(await disabledContext.newPage())
      await disabledPage.goto(`${baseURL}/login`, { waitUntil: 'domcontentloaded' })
      const response = await submitLogin(disabledPage, 'e2e-disabled', password)
      assert.equal(response.status(), 401, '停用账号必须返回通用 401')
      await disabledPage.getByRole('alert').getByText('用户名或密码错误', { exact: true }).waitFor()
    } finally {
      await disabledContext.close()
    }

    const lockContext = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: 'zh-CN' })
    try {
      const lockPage = watch(await lockContext.newPage())
      await lockPage.goto(`${baseURL}/login`, { waitUntil: 'domcontentloaded' })
      const statuses = []
      for (let attempt = 1; attempt <= 5; attempt += 1) {
        const response = await submitLogin(lockPage, 'e2e-lock', `wrong-password-${attempt}`)
        statuses.push(response.status())
        await lockPage.getByRole('alert').getByText('用户名或密码错误', { exact: true }).waitFor()
        assert.equal(await lockPage.getByRole('button', { name: '登录', exact: true }).isDisabled(), false, `第 ${attempt} 次失败不应提前锁定`)
      }
      assert.deepEqual(statuses, [401, 401, 401, 401, 401], '前五次失败必须均为 401')

      const lockedResponse = await submitLogin(lockPage, 'e2e-lock', 'wrong-password-6')
      statuses.push(lockedResponse.status())
      assert.equal(lockedResponse.status(), 429, '第六次失败才应触发限流')
      const retryAfter = Number(lockedResponse.headers()['retry-after'])
      assert.ok(Number.isFinite(retryAfter) && retryAfter > 0 && retryAfter <= 7, `Retry-After 不正确：${retryAfter}`)
      const submit = lockPage.locator('.login-form__submit')
      await lockPage.getByRole('alert').getByText(/尝试次数过多，请 \d+\s*秒\s*后再试/).waitFor()
      await submit.waitFor()
      assert.equal(await submit.isDisabled(), true, '锁定期间登录按钮未禁用')
      const initialText = await submit.innerText()
      const initialSeconds = Number(initialText.match(/(\d+)\s*秒\s*后重试/)?.[1])
      assert.ok(initialSeconds > 0, `未显示锁定倒计时：${initialText}`)
      await lockPage.waitForFunction((previous) => {
        const text = document.querySelector('.login-form__submit')?.textContent || ''
        const current = Number(text.match(/(\d+)\s*秒\s*后重试/)?.[1])
        return current > 0 && current < previous
      }, initialSeconds, { timeout: 5_000 })
      await capture('11-login-lock', lockPage)

      await lockPage.getByRole('button', { name: '登录', exact: true }).waitFor({ timeout: 12_000 })
      const unlockedResponse = await submitLogin(lockPage, 'e2e-lock', password)
      assert.equal(unlockedResponse.status(), 204, '锁定到期后正确密码仍无法登录')
      await lockPage.getByRole('heading', { name: '今天想构建什么？' }).waitFor()
      coverage.authentication = {
        wrong_password_generic: true,
        disabled_account_generic: true,
        failure_statuses: statuses,
        countdown_decreased: true,
        unlock_and_login: true,
      }
    } finally {
      await lockContext.close()
    }
  }

  async function verifyPagination() {
    const created = []
    for (let index = 1; index <= 31; index += 1) {
      const item = await json('POST', '/api/conversations', { title: `E2E 分页 ${String(index).padStart(2, '0')}` }, 201)
      created.push(item.id)
    }
    const first = await apiGet('/api/conversations?limit=30')
    assert.equal(first.items.length, 30, '对话 API 首页不是 30 项')
    assert.ok(first.next_cursor, '对话 API 首页缺少 next_cursor')
    const second = await apiGet(`/api/conversations?limit=30&cursor=${encodeURIComponent(first.next_cursor)}`)
    assert.equal(second.items.length, 2, '对话 API 第二页不是 2 项')
    const ids = [...first.items, ...second.items].map((item) => item.id)
    assert.equal(new Set(ids).size, 32, '分页对话存在重复')
    assert.ok(ids.includes(conversationID), '分页结果缺少主对话')
    assert.ok(created.every((id) => ids.includes(id)), '分页结果缺少新建对话')

    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.locator('.run-end-card--finished').nth(1).waitFor({ timeout: 60_000 })
    await page.waitForFunction(() => document.querySelectorAll('.conv-item').length === 30)
    await page.getByRole('button', { name: '加载更多', exact: true }).click()
    await page.waitForFunction(() => document.querySelectorAll('.conv-item').length === 32)
    await page.locator('.conv-item__title').getByText(renamedTitle, { exact: true }).waitFor()
    await capture('12-pagination')
    await page.locator('.conv-item__title').getByText(renamedTitle, { exact: true }).click()
    await page.waitForURL(new RegExp(`/c/${conversationID}$`))
    await page.locator('.run-end-card--finished').nth(1).waitFor()
    coverage.pagination = { api_pages: [30, 2], unique_total: 32, ui_initial: 30, ui_after_load_more: 32 }
  }

  async function verifySlowRunAndModel() {
    await waitPreviewGeneration(secondGeneration)
    const finishedBefore = await page.locator('.run-end-card--finished').count()
    assert.equal(finishedBefore, 2, '慢响应场景前主对话应有两轮完成记录')
    const slowMessage = '[e2e:slow]'
    let postCount = 0
    const routePredicate = (url) => url.pathname === messagesPath
    const delayRoute = async (route) => {
      if (route.request().method() !== 'POST') return route.continue()
      postCount += 1
      if (postCount === 1) await sleep(1500)
      return route.continue()
    }
    await page.route(routePredicate, delayRoute)
    const acceptedPromise = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === messagesPath && response.request().method() === 'POST'
    })
    await page.getByRole('textbox', { name: '需求内容' }).fill(slowMessage)
    await page.getByRole('button', { name: '发送', exact: true }).click()
    const optimistic = page.locator('.user-message').filter({ hasText: slowMessage }).last()
    await optimistic.locator('.user-message__status').getByText('发送中', { exact: true }).waitFor()
    await page.getByRole('button', { name: '发送中', exact: true }).waitFor()
    const acceptedResponse = await acceptedPromise
    assert.equal(acceptedResponse.status(), 202, '慢响应消息未被接受')
    await acceptedResponse.body()
    await page.unroute(routePredicate, delayRoute)
    assert.equal(postCount, 1, '慢响应 POST 次数不正确')

    await page.waitForFunction(() => document.querySelector('textarea[aria-label="需求内容"]')?.disabled)
    assert.equal(await page.locator('select[aria-label="执行模型"]').count(), 0, '前端不应渲染模型选择')
    const conflict = await errorPayload(await raw('PATCH', `/api/conversations/${conversationID}`, { model: SECONDARY_MODEL }), 409, '活跃 Run 切换模型')
    assert.equal(conflict.error?.code, 'RUN_IN_PROGRESS', '活跃 Run 切换模型错误码不正确')

    const previewResponsePromise = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === previewPath && response.request().method() === 'GET'
    })
    await page.getByRole('button', { name: '刷新预览状态', exact: true }).click()
    const previewResponse = await previewResponsePromise
    const activePreview = await previewResponse.json()
    assert.equal(previewResponse.status(), 200, '活跃 Run 刷新预览状态失败')
    assert.equal(activePreview.stale, true, '活跃 Run 中预览未标记 stale')
    await page.locator('.preview-stale').getByText('旧版本', { exact: true }).waitFor()
    await page.locator('.preview-status__version', { hasText: `#${secondGeneration}` }).waitFor()
    await previewFrame.getByRole('heading', { name: '原型已真实修改' }).waitFor()
    await capture('13-slow-active')

    await page.locator('.run-end-card--finished').nth(2).waitFor({ timeout: 240_000 })
    await waitPreviewGeneration(3, slowMessage)
    await page.locator('.preview-stale').waitFor({ state: 'hidden' })

    // 前端不再提供模型选择；模型切换只保留服务端 API 能力（用于管理与计量验证）。
    const patched = await json('PATCH', `/api/conversations/${conversationID}`, { model: SECONDARY_MODEL }, 200)
    assert.equal(patched.model, SECONDARY_MODEL, 'PATCH 未返回第二模型')
    assert.equal((await apiGet(`/api/conversations/${conversationID}`)).model, SECONDARY_MODEL, '第二模型未持久化')
    coverage.active_run = {
      optimistic_sending: true,
      composer_disabled: true,
      no_model_picker_in_ui: true,
      model_patch_conflict: 'RUN_IN_PROGRESS',
      model_switch_api_only: true,
      stale_preview_preserved: secondGeneration,
      recovered_generation: 3,
    }
  }

  async function verifyMarkdownAndUsage() {
    const finishedBefore = await page.locator('.run-end-card--finished').count()
    const toolsBefore = await page.locator('.tool-card').count()
    assert.equal(finishedBefore, 3, 'Markdown 场景前完成轮数不正确')
    await page.evaluate(() => { window.__E2E_MARKDOWN_XSS__ = false })
    const requestPromise = page.waitForRequest((request) => {
      const url = new URL(request.url())
      return url.pathname === messagesPath && request.method() === 'POST'
    })
    const acceptedPromise = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === messagesPath && response.request().method() === 'POST'
    })
    await page.getByRole('textbox', { name: '需求内容' }).fill('[e2e:markdown]')
    await page.getByRole('button', { name: '发送', exact: true }).click()
    const post = await requestPromise
    assert.equal(post.postDataJSON().model, undefined, '前端不应在发送请求里指定模型')
    const accepted = await acceptedPromise
    assert.equal(accepted.status(), 202, 'Markdown 消息未被接受')
    await accepted.body()
    await page.locator('.run-end-card--finished').nth(3).waitFor({ timeout: 240_000 })
    assert.equal(await page.locator('.tool-card').count(), toolsBefore, 'Markdown 分支不应调用工具')

    const markdown = page.locator('.assistant-message .markdown-body').last()
    await markdown.getByRole('heading', { name: 'E2E Markdown 安全渲染', exact: true }).waitFor()
    await markdown.locator('strong').getByText('E2E_BOLD_SENTINEL', { exact: true }).waitFor()
    const link = markdown.getByRole('link', { name: 'E2E 安全链接', exact: true })
    assert.equal(await link.getAttribute('target'), '_blank')
    assert.equal(await link.getAttribute('rel'), 'noopener noreferrer nofollow')
    const markdownText = await markdown.textContent()
    assert.ok(markdownText.includes('<script>') && markdownText.includes('</script>') && markdownText.includes('E2E_MARKDOWN_XSS'), '原始 script 未作为文本保留')
    assert.ok(markdownText.includes('<img src="x" onerror='), '原始 img 未作为文本保留')
    assert.equal(await markdown.locator('script, img, iframe, object, embed, form, input, button').count(), 0, 'Markdown 中出现危险 DOM')
    assert.equal(await page.evaluate(() => window.__E2E_MARKDOWN_XSS__), false, 'Markdown 原始 HTML 被执行')

    const csp = await page.locator('meta[http-equiv="Content-Security-Policy"]').getAttribute('content')
    assert.ok(csp, '页面缺少 CSP meta')
    const scriptDirective = csp.split(';').map((part) => part.trim()).find((part) => part.startsWith('script-src '))
    assert.ok(scriptDirective?.includes("'self'") && !scriptDirective.includes("'unsafe-inline'"), 'CSP 未禁止内联脚本')
    assert.match(csp, /object-src 'none'/, 'CSP 未禁用 object')
    await page.evaluate(() => {
      window.__E2E_CSP_INLINE__ = false
      const script = document.createElement('script')
      script.textContent = 'window.__E2E_CSP_INLINE__ = true'
      document.body.appendChild(script)
      script.remove()
    })
    await page.waitForTimeout(100)
    assert.equal(await page.evaluate(() => window.__E2E_CSP_INLINE__), false, 'CSP 未阻断真实内联脚本')

    const usage = JSON.parse(control(['usage', '-config', CONTROL_CONFIG, '-user', 'demo']))
    const secondary = usage.items.find((item) => item.Model === SECONDARY_MODEL)
    assert.ok(secondary, '用量报表缺少第二模型')
    assert.ok(secondary.Requests >= 1 && secondary.PromptTokens > 0 && secondary.ComplTokens > 0, `第二模型用量不正确：${JSON.stringify(secondary)}`)
    await capture('14-markdown-secondary')
    await waitPreviewGeneration(4)
    assert.equal(
      await page.locator('iframe[title="生成应用预览"]').getAttribute('sandbox'),
      'allow-scripts allow-forms allow-same-origin allow-popups',
      '预览 iframe sandbox 权限不符合约定',
    )
    coverage.secondary_model_markdown = {
      request_model: SECONDARY_MODEL,
      persisted_model: true,
      usage_requests: secondary.Requests,
      markdown_sanitized: true,
      safe_link_attributes: true,
      csp_inline_script_blocked: true,
      iframe_sandbox_verified: true,
      preview_generation: 4,
    }
  }

  async function verifyFailureAndRetry() {
    assert.equal(await page.locator('.run-end-card--finished').count(), 4, '故障场景前完成轮数不正确')
    const usersBefore = await page.locator('.user-message').count()
    const runTestsBefore = await toolCards('run_tests').count()
    const writeBefore = await toolCards('write_file').count()
    const previewBefore = await toolCards('preview').count()
    const failedToolsBefore = await page.locator('.tool-card--failed').count()
    const requestBodies = []
    const onRequest = (request) => {
      const url = new URL(request.url())
      if (url.pathname === messagesPath && request.method() === 'POST') requestBodies.push(request.postDataJSON())
    }
    let aborted = 0
    const routePredicate = (url) => url.pathname === messagesPath
    const abortRoute = async (route) => {
      if (route.request().method() !== 'POST') return route.continue()
      aborted += 1
      return route.abort('failed')
    }
    page.on('request', onRequest)
    await page.route(routePredicate, abortRoute)
    try {
      await page.getByRole('textbox', { name: '需求内容' }).fill('[e2e:failure]')
      await page.getByRole('button', { name: '发送', exact: true }).click()
      const failedMessage = page.locator('.user-message--failed').filter({ hasText: '[e2e:failure]' }).last()
      await failedMessage.getByRole('alert').getByText(/发送失败/).waitFor()
      assert.equal(aborted, 1, '第一次故障请求未被浏览器 abort')
      assert.equal(await page.locator('.user-message').count(), usersBefore + 1, '乐观消息数量不正确')
      let serverMessages = await apiGet(`/api/conversations/${conversationID}/messages?limit=100`)
      assert.equal(serverMessages.items.filter((item) => item.role === 'user' && item.content === '[e2e:failure]').length, 0, 'abort 的请求到达了服务端')

      await page.unroute(routePredicate, abortRoute)
      const retryResponsePromise = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return url.pathname === messagesPath && response.request().method() === 'POST'
      })
      await failedMessage.getByRole('button', { name: '重试', exact: true }).click()
      const retryResponse = await retryResponsePromise
      assert.equal(retryResponse.status(), 202, '失败消息重试未被接受')
      await retryResponse.body()
      await page.locator('.run-end-card--finished').nth(4).waitFor({ timeout: 240_000 })
      assert.equal(await page.locator('.user-message').count(), usersBefore + 1, '重试增加了重复用户消息')
      serverMessages = await apiGet(`/api/conversations/${conversationID}/messages?limit=100`)
      assert.equal(serverMessages.items.filter((item) => item.role === 'user' && item.content === '[e2e:failure]').length, 1, '服务端故障消息重复或缺失')
    } finally {
      page.off('request', onRequest)
      await page.unroute(routePredicate, abortRoute).catch(() => undefined)
    }
    assert.equal(requestBodies.length, 2, '故障发送与重试的 POST 捕获数量不正确')
    assert.ok(requestBodies[0].client_request_id, '第一次请求缺少 client_request_id')
    assert.equal(requestBodies[1].client_request_id, requestBodies[0].client_request_id, '重试更换了 client_request_id')

    assert.equal(await toolCards('run_tests').count(), runTestsBefore + 1, '故障分支 run_tests 数量不正确')
    assert.equal(await toolCards('write_file').count(), writeBefore + 1, '故障分支 write_file 数量不正确')
    assert.equal(await toolCards('preview').count(), previewBefore + 1, '故障分支 preview 数量不正确')
    const failedTest = toolCards('run_tests').nth(runTestsBefore)
    await failedTest.locator('.tool-card__state').getByText('失败', { exact: true }).waitFor()
    await failedTest.locator('.tool-card__truncated').getByText(/输出已截断/).waitFor()
    await failedTest.locator('.tool-card__toggle').click()
    const testOutput = await failedTest.locator('.tool-card__details').textContent()
    assert.ok(testOutput.includes('退出码 17'), 'run_tests 详情缺少真实退出码 17')
    const failedPreview = toolCards('preview').nth(previewBefore)
    await failedPreview.locator('.tool-card__state').getByText('失败', { exact: true }).waitFor()
    await failedPreview.locator('.tool-card__toggle').click()
    const previewOutput = await failedPreview.locator('.tool-card__details').textContent()
    assert.ok(/BUILD_FAILED|"status"\s*:\s*"failed"/i.test(previewOutput), 'preview 详情缺少真实构建失败状态')
    assert.equal(await page.locator('.run-end-card--failed').count(), 0, '工具失败被错误提升为 Run 级失败')
    assert.equal(await page.locator('.tool-card--failed').count(), failedToolsBefore + 2, '实际失败工具卡数量不正确')

    await openPreview()
    await page.locator('.preview-status--failed').waitFor({ timeout: 180_000 })
    await capture('15-tool-failure')
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.locator('.run-end-card--finished').nth(4).waitFor({ timeout: 60_000 })
    await openPreview()
    await page.locator('.preview-empty--error strong').getByText('预览构建失败', { exact: true }).waitFor({ timeout: 180_000 })
    await page.locator('.preview-empty--error').getByRole('button', { name: '查看日志', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('.log-drawer__content')?.textContent?.includes('E2E_PREVIEW_BUILD_FAILURE'), null, { timeout: 60_000 })
    await page.getByRole('button', { name: '关闭日志', exact: true }).click()
    coverage.failure_retry = {
      first_request_aborted_before_server: true,
      same_client_request_id: true,
      no_duplicate_user_message: true,
      run_tests_exit_code: 17,
      tool_result_truncated: true,
      preview_build_failed: true,
      run_itself_finished: true,
    }
  }

  async function verifyRecoveryAndWorkspace() {
    assert.equal(await page.locator('.run-end-card--finished').count(), 5, '恢复场景前完成轮数不正确')
    const writeBefore = await toolCards('write_file').count()
    const previewBefore = await toolCards('preview').count()
    const toolsBefore = await page.locator('.tool-card').count()
    const failedToolsBefore = await page.locator('.tool-card--failed').count()
    const accepted = await sendAndWait('[e2e:recover]', 5, 300_000)
    const writes = toolCards('write_file')
    const previews = toolCards('preview')
    assert.equal(await writes.count(), writeBefore + 6, '恢复场景必须有六次真实写入')
    assert.equal(await previews.count(), previewBefore + 1, '恢复场景必须调用一次真实预览')
    assert.equal(await page.locator('.tool-card').count(), toolsBefore + 7, '恢复场景工具卡总数不正确')
    for (let index = writeBefore; index < writeBefore + 6; index += 1) {
      await writes.nth(index).locator('.tool-card__state').getByText('已完成', { exact: true }).waitFor({ timeout: 60_000 })
    }
    await previews.nth(previewBefore).locator('.tool-card__state').getByText('已完成', { exact: true }).waitFor({ timeout: 60_000 })
    assert.equal(await page.locator('.tool-card--failed').count(), failedToolsBefore, '恢复场景产生了新的失败工具卡')
    await waitPreviewGeneration(6)

    const history = await apiGet(`/api/conversations/${conversationID}/events/history?limit=999`)
    let recoveryRunEnd
    for (const frame of history.items) {
      if (frame.run_id !== accepted.run_id) continue
      const item = frame.items.find((candidate) => candidate.type === 'run_finished')
      if (item) recoveryRunEnd = item.data
    }
    assert.ok(recoveryRunEnd, '恢复 Run 缺少 run_finished 事件')
    assert.equal(recoveryRunEnd.files_total, 6, '恢复 Run 改动文件数不是 6')

    await page.getByRole('tab', { name: /代码/ }).click()
    const treeDirectory = page.locator('.file-tree-row[title="e2e-tree"]')
    await treeDirectory.waitFor({ timeout: 60_000 })
    assert.equal(await page.locator('.file-tree-row[title=".e2e-hidden"]').count(), 0, '文件树不应列出隐藏目录')
    const hidden = await apiGet(`/api/conversations/${conversationID}/files/content?path=${encodeURIComponent('.e2e-hidden/secret.txt')}`)
    assert.equal(hidden.content, 'E2E_HIDDEN_FILE_PRESENT\n', '隐藏文件未真实写入')
    const hiddenCard = toolCards('write_file').filter({ has: page.locator('.tool-card__summary').getByText('.e2e-hidden/secret.txt', { exact: true }) })
    assert.equal(await hiddenCard.count(), 1, '找不到隐藏文件对应的写入工具卡')
    await hiddenCard.getByRole('button', { name: '查看代码', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('.editor-pane__path')?.textContent?.includes('.e2e-hidden/secret.txt'))
    await page.waitForFunction(() => document.querySelector('.monaco-container .view-lines')?.textContent?.includes('E2E_HIDDEN_FILE_PRESENT'), null, { timeout: 60_000 })
    assert.equal(await page.locator('.editor-pane__error').count(), 0, '隐藏文件工具卡跳转被误报为不存在')

    const directoryRequests = []
    const onTreeRequest = (request) => {
      const url = new URL(request.url())
      if (url.pathname === `/api/conversations/${conversationID}/files/tree` && url.searchParams.get('path') === 'e2e-tree') {
        directoryRequests.push(request)
      }
    }
    page.on('request', onTreeRequest)
    try {
      assert.equal(await page.locator('.file-tree-row[title="e2e-tree/2.txt"]').count(), 0, '目录展开前已渲染子项')
      await page.waitForTimeout(300)
      assert.equal(directoryRequests.length, 0, '目录展开前已请求子目录')
      const directoryResponsePromise = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return url.pathname === `/api/conversations/${conversationID}/files/tree` && url.searchParams.get('path') === 'e2e-tree'
      })
      await treeDirectory.click()
      const directoryResponse = await directoryResponsePromise
      assert.equal(directoryResponse.status(), 200, '展开目录请求失败')
      assert.ok(directoryRequests.length >= 1, '展开目录没有发出真实请求')
    } finally {
      page.off('request', onTreeRequest)
    }
    const directoryNode = treeDirectory.locator('..')
    const childRows = directoryNode.locator(':scope > .file-tree-children > .file-tree-node > .file-tree-row')
    await page.waitForFunction(() => document.querySelectorAll('.file-tree-row[title^="e2e-tree/"]').length >= 4)
    assert.deepEqual(await childRows.evaluateAll((rows) => rows.map((row) => row.getAttribute('title'))), [
      'e2e-tree/z-dir', 'e2e-tree/2.txt', 'e2e-tree/10.txt', 'e2e-tree/alpha.txt',
    ], '文件树目录优先或自然排序不正确')
    assert.equal(await treeDirectory.locator('.change-dot--parent').count(), 1, '父目录缺少改动标记')
    const twoRow = page.locator('.file-tree-row[title="e2e-tree/2.txt"]')
    assert.equal(await twoRow.locator('.change-dot--exact').count(), 1, '改动文件缺少精确标记')

    const twoCard = toolCards('write_file').filter({ has: page.locator('.tool-card__summary').getByText('e2e-tree/2.txt', { exact: true }) })
    assert.equal(await twoCard.count(), 1, '找不到 e2e-tree/2.txt 对应的写入工具卡')
    await twoCard.getByRole('button', { name: '查看代码', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('.editor-pane__path')?.textContent?.includes('e2e-tree/2.txt'))
    await page.waitForFunction(() => document.querySelector('.monaco-container .view-lines')?.textContent?.includes('E2E_READONLY_SENTINEL=two'), null, { timeout: 60_000 })
    assert.ok(await page.locator('.readonly-badge').isVisible(), '恢复夹具编辑器未显示只读')
    const viewLines = page.locator('.monaco-container .view-lines')
    const beforeEdit = await viewLines.textContent()
    await viewLines.click()
    await page.keyboard.type('E2E_SHOULD_NOT_INSERT')
    await page.waitForTimeout(250)
    assert.equal(await viewLines.textContent(), beforeEdit, '只读 Monaco 内容被键盘修改')

    for (const name of ['推送', '发布']) {
      const button = page.getByRole('button', { name, exact: true })
      assert.equal(await button.isDisabled(), true, `${name} 按钮未禁用`)
      assert.equal(await button.locator('..').getAttribute('title'), '即将开放', `${name} 容器提示不正确`)
    }
    for (const endpoint of ['git/push', 'deployments']) {
      const payload = await errorPayload(await raw('POST', `/api/conversations/${conversationID}/${endpoint}`, {}), 501, endpoint)
      assert.equal(payload.error?.code, 'NOT_IMPLEMENTED', `${endpoint} 未返回 NOT_IMPLEMENTED`)
    }
    await capture('16-recovery-tree')

    const firstFrame = history.items.find((frame) => frame.items.some((item) => item.type === 'run_finished'))
    const firstRunEnd = firstFrame?.items.find((item) => item.type === 'run_finished')?.data
    assert.ok(firstRunEnd?.commit, '首个 run_finished 缺少 commit')
    assert.ok(firstRunEnd.files_total > 0 && firstRunEnd.duration_ms > 0, '首个 run_finished 文件数或用时不正确')
    assert.ok(firstRunEnd.usage.prompt + firstRunEnd.usage.completion > 0, '首个 run_finished tokens 不正确')
    const firstCardText = await page.locator('.run-end-card').first().textContent()
    assert.ok(firstCardText.includes(`版本 ${firstRunEnd.commit.slice(0, 7)}`), '首个终态卡版本号与事件不一致')
    assert.ok(firstCardText.includes(`改动 ${firstRunEnd.files_total} 个文件`), '首个终态卡文件数与事件不一致')
    assert.ok(firstCardText.includes('用时') && firstCardText.includes('tokens'), '首个终态卡缺少用时或 tokens')
    coverage.recovery_workspace = {
      successful_write_cards: 6,
      successful_preview_cards: 1,
      changed_files: recoveryRunEnd.files_total,
      hidden_file_via_api: true,
      lazy_directory_and_sort: true,
      change_markers: true,
      monaco_read_only: true,
      push_and_deploy_disabled_and_501: true,
      run_end_matches_event: true,
    }
  }

  async function logoutViaUI() {
    await page.getByRole('button', { name: '用户菜单', exact: true }).click()
    await page.getByRole('button', { name: '退出登录', exact: true }).click()
    await page.getByRole('heading', { name: '今天想构建什么？' }).waitFor()
  }

  async function verifySessionRedirects() {
    const beforeCookies = await context.cookies(baseURL)
    const beforeSession = beforeCookies.find((cookie) => cookie.name === 'proto_sid')
    assert.ok(beforeSession?.value, '主浏览器缺少会话 Cookie')
    const logout = await raw('POST', '/api/logout')
    assert.equal(logout.status, 204, 'Node 侧退出请求失败')
    await logout.text()
    const afterSession = (await context.cookies(baseURL)).find((cookie) => cookie.name === 'proto_sid')
    assert.equal(afterSession?.value, beforeSession.value, 'Node 侧退出不应清除浏览器旧 Cookie')

    await page.getByRole('tab', { name: /代码/ }).click()
    const unauthorizedPromise = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.status() === 401 && url.pathname === `/api/conversations/${conversationID}/files/tree`
    })
    await page.getByRole('button', { name: '刷新文件树', exact: true }).click()
    const unauthorized = await unauthorizedPromise
    assert.equal(unauthorized.status(), 401, '文件树未产生真实 401')
    await page.waitForURL((url) => url.pathname === '/login')
    assert.equal(new URL(page.url()).searchParams.get('redirect'), `/c/${conversationID}`, '401 回跳缺少原对话 redirect')
    const login = await submitLogin(page, 'demo', password)
    assert.equal(login.status(), 204, '401 后重新登录失败')
    await page.waitForURL(new RegExp(`/c/${conversationID}$`))
    await page.locator('.run-end-card--finished').nth(5).waitFor({ timeout: 60_000 })

    const rejectedRedirects = []
    for (const target of ['https://evil.example/', '//evil.example/']) {
      await logoutViaUI()
      await page.goto(`${baseURL}/login?redirect=${encodeURIComponent(target)}`, { waitUntil: 'domcontentloaded' })
      const response = await submitLogin(page, 'demo', password)
      assert.equal(response.status(), 204, '外部 redirect 夹具登录失败')
      await page.waitForURL((url) => url.origin === baseURL && url.pathname === '/')
      assert.equal(new URL(page.url()).pathname, '/', '外部 redirect 未回退到站内首页')
      rejectedRedirects.push(target)
    }
    await page.goto(mainURL, { waitUntil: 'domcontentloaded' })
    await page.locator('.run-end-card--finished').nth(5).waitFor({ timeout: 60_000 })
    await capture('18-session-reauth')
    coverage.session_redirects = { revoked_session_cookie_retained: true, real_401_redirect: true, returned_to_conversation: true, external_redirects_rejected: rejectedRedirects }
  }

  async function verifyThirdPartyCookies() {
    await waitPreviewGeneration(6)
    const sharedCookies = await context.cookies()
    assert.ok(sharedCookies.some((cookie) => cookie.name === 'proto_sid'), '缺少可复制的登录 Cookie')
    assert.ok(sharedCookies.some((cookie) => cookie.name === 'proto_preview'), '缺少可复制的预览 Cookie')
    let thirdBrowser
    let outcome
    let unauthorizedStatus
    try {
      thirdBrowser = await chromium.launch({
        executablePath: chromePath,
        headless: process.env.HEADFUL !== '1',
        args: ['--no-first-run', '--no-default-browser-check', '--test-third-party-cookie-phaseout'],
      })
      const thirdContext = await thirdBrowser.newContext({ viewport: { width: 1440, height: 1000 }, locale: 'zh-CN' })
      await thirdContext.addCookies(sharedCookies)
      const thirdPage = watch(await thirdContext.newPage())
      await thirdPage.goto(mainURL, { waitUntil: 'domcontentloaded' })
      await thirdPage.locator('.run-end-card--finished').nth(5).waitFor({ timeout: 60_000 })
      await thirdPage.getByRole('tab', { name: /预览/ }).click()
      await thirdPage.locator('iframe[title="生成应用预览"]').waitFor({ timeout: 60_000 })
      const thirdFrame = thirdPage.frameLocator('iframe[title="生成应用预览"]')
      outcome = await Promise.race([
        thirdFrame.getByText('preview authorization required', { exact: false }).waitFor({ timeout: 60_000 }).then(() => 'blocked').catch(() => 'blocked-timeout'),
        thirdFrame.getByRole('heading', { name: '原型已真实修改' }).waitFor({ timeout: 60_000 }).then(() => 'allowed').catch(() => 'allowed-timeout'),
      ])
      assert.ok(['blocked', 'allowed'].includes(outcome), `第三方 Cookie  iframe 未进入可识别终态：${outcome}`)
      await capture('19-third-party-cookie', thirdPage)

      const popupPromise = thirdPage.waitForEvent('popup')
      await thirdPage.getByRole('button', { name: '新窗口', exact: true }).click()
      const popup = watch(await popupPromise)
      await popup.waitForLoadState('domcontentloaded')
      await popup.getByRole('heading', { name: '原型已真实修改' }).waitFor({ timeout: 60_000 })
      await popup.close()

      const anonymousContext = await thirdBrowser.newContext()
      try {
        const anonymousPage = watch(await anonymousContext.newPage())
        const response = await anonymousPage.goto(`http://${previewHost}/`, { waitUntil: 'domcontentloaded' })
        unauthorizedStatus = response.status()
        assert.equal(unauthorizedStatus, 401, '无 Cookie 直接访问预览必须返回 401')
        await anonymousPage.getByText('preview authorization required', { exact: false }).waitFor()
      } finally {
        await anonymousContext.close()
      }
      if (outcome === 'allowed') {
        notCovered.push('本机 Chrome 未按 --test-third-party-cookie-phaseout 阻断跨站 iframe Cookie；未宣称第三方 Cookie 阻断通过。')
      }
      coverage.third_party_cookies = {
        phaseout_flag: '--test-third-party-cookie-phaseout',
        cross_site_iframe_blocked: outcome === 'blocked',
        new_window_preview: true,
        anonymous_preview_status: unauthorizedStatus,
      }
    } finally {
      if (thirdBrowser) await thirdBrowser.close()
    }
  }

  async function verifySleepAndWake() {
    assert.match(conversationID, /^[a-z2-7]{12}$/, '主对话 ID 不符合预期，拒绝回拨数据库')
    const database = path.join(root, '.local', 'e2e', 'data', 'control.db')
    const sqlite = process.env.SQLITE3 || 'sqlite3'
    const readSandbox = () => {
      const result = spawnSync(sqlite, ['-readonly', database, `SELECT Finst_id || '|' || Fstatus || '|' || Fpreview_gen FROM t_sandbox WHERE Fconv_id='${conversationID}';`], {
        cwd: root,
        encoding: 'utf8',
      })
      assert.ok(!result.error, `sqlite3 不可用：${result.error?.message}`)
      assert.equal(result.status, 0, `读取 E2E 沙箱状态失败：${result.stderr || result.stdout}`)
      const [instance, status, generation] = result.stdout.trim().split('|')
      return { instance, status: Number(status), generation: Number(generation) }
    }
    const old = readSandbox()
    assert.equal(old.status, 2, '休眠场景前沙箱不是 running')
    assert.equal(old.generation, 6, '休眠场景前预览 generation 不正确')

    // 彻底销毁页面 JS、SSE、timer 和 iframe，单独验证健康循环的停止语义。
    await page.goto('about:blank')
    const update = spawnSync(sqlite, [database, `UPDATE t_sandbox SET Flast_active_time=datetime('now','-2 hours') WHERE Fconv_id='${conversationID}';`], {
      cwd: root,
      encoding: 'utf8',
    })
    assert.ok(!update.error, `sqlite3 不可用：${update.error?.message}`)
    assert.equal(update.status, 0, `回拨 E2E 沙箱时间失败：${update.stderr || update.stdout}`)
    const stopped = await poll(
      readSandbox,
      (sandbox) => sandbox.status === 3 && sandbox.instance === old.instance,
      '健康循环停止空闲沙箱',
      45_000,
    )
    assert.equal(stopped.generation, 6, '正常停止清除了预览元数据')
    await sleep(4500)
    assert.deepEqual(readSandbox(), stopped, '无页面活动时 stopped 状态未跨越两个健康周期保持稳定')

    const isPreviewResponse = (response) => {
      const url = new URL(response.url())
      return url.pathname === previewPath && response.request().method() === 'GET'
    }
    const readyResponsePromise = page.waitForResponse(async (response) => {
      if (!isPreviewResponse(response) || response.status() !== 200) return false
      const preview = await response.json()
      assert.notEqual(preview.status, 'failed', `唤醒后的预览构建失败：${JSON.stringify(preview)}`)
      return preview.status === 'ready' && preview.generation === 6
    }, { timeout: 180_000 })
    const wakeResponsePromise = page.waitForResponse((response) => isPreviewResponse(response))
    await page.goto(mainURL, { waitUntil: 'domcontentloaded' })
    await page.locator('.run-end-card--finished').nth(5).waitFor({ timeout: 60_000 })
    await openPreview()
    const wakeResponse = await wakeResponsePromise
    const wake = await wakeResponse.json()
    assert.equal(wakeResponse.status(), 200, '重新打开预览未触发唤醒请求')
    assert.equal(wake.status, 'starting', '停止后的预览状态不是 starting')
    assert.equal(wake.generation, 6, '唤醒请求前预览 generation 已回退')
    const running = await poll(
      () => apiGet(`/api/conversations/${conversationID}`),
      (detail) => detail.sandbox?.status === 'running',
      '沙箱恢复 running',
      120_000,
    )
    assert.equal(running.preview.generation, 6, '实例替换把预览元数据重置为 none/0')

    const readyResponse = await readyResponsePromise
    const ready = await readyResponse.json()
    assert.equal(ready.status, 'ready', '页面自动轮询未取得 ready')
    assert.equal(ready.generation, 6, '唤醒后预览 generation 回退')
    await page.locator('.preview-status--ready').waitFor({ timeout: 60_000 })
    await page.locator('.preview-status__version', { hasText: '#6' }).waitFor()
    await previewFrame.getByRole('heading', { name: '原型已真实修改' }).waitFor({ timeout: 60_000 })
    const retained = await apiGet(`/api/conversations/${conversationID}/files/content?path=${encodeURIComponent('e2e-tree/2.txt')}`)
    assert.ok(retained.content.includes('E2E_READONLY_SENTINEL=two'), '休眠唤醒后夹具文件丢失')

    await page.getByRole('tab', { name: /代码/ }).click()
    if (await page.locator('.file-tree-row[title="e2e-tree/2.txt"]').count() === 0) {
      await page.locator('.file-tree-row[title="e2e-tree"]').click()
    }
    await page.locator('.file-tree-row[title="e2e-tree/2.txt"]').click()
    await page.waitForFunction(() => document.querySelector('.monaco-container .view-lines')?.textContent?.includes('E2E_READONLY_SENTINEL=two'), null, { timeout: 60_000 })
    await openPreview()
    await page.locator('.preview-status--ready').waitFor()
    await capture('20-sleep-wake')
    coverage.sleep_wake = {
      idle_time_adjusted_only_in_e2e_db: true,
      health_loop_stopped: true,
      remained_stopped_without_page_activity: true,
      ui_preview_mount_triggered_wake: true,
      running_and_preview_ready: true,
      preview_metadata_preserved: true,
      automatic_poll_after_http_200_starting: true,
      second_manual_refresh: false,
      files_retained: true,
    }
  }

  // 游客可直接浏览首页；只有创建对话时才弹出登录/注册，注册后继续创建。
  async function verifyGuestBrowseAndRegistration() {
    await logoutViaUI()
    const guestLogin = page.locator('.sidebar-user').getByRole('button', { name: '登录或注册' })
    await guestLogin.waitFor()
    assert.equal(new URL(page.url()).pathname, '/', '退出登录后没有回到游客首页')

    const protectedPaths = []
    const recordRequest = (request) => {
      const pathname = new URL(request.url()).pathname
      if (pathname === '/api/conversations' || pathname === '/api/models') protectedPaths.push(pathname)
    }
    page.on('request', recordRequest)
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.getByRole('heading', { name: '今天想构建什么？' }).waitFor()
    await sleep(800)
    page.off('request', recordRequest)
    assert.deepEqual(protectedPaths, [], `游客首页请求了受保护接口：${protectedPaths.join('、')}`)
    assert.equal(new URL(page.url()).pathname, '/', '游客刷新首页被重定向到登录页')

    await page.getByRole('button', { name: /从空白对话开始/ }).click()
    const dialog = page.getByRole('dialog', { name: '登录或注册' })
    await dialog.waitFor()
    await dialog.getByRole('tab', { name: '注册' }).click()
    const guestUser = `e2e_${Date.now().toString(36)}`
    const guestPassword = 'password-guest-1'
    await page.locator('#username').fill(guestUser)
    await page.locator('#password').fill(guestPassword)
    await page.locator('#confirm-password').fill(guestPassword)
    const registerPromise = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/api/register' && response.request().method() === 'POST'
    })
    await dialog.getByRole('button', { name: '注册并登录' }).click()
    assert.equal((await registerPromise).status(), 201, '自助注册未返回 201')
    await dialog.waitFor({ state: 'detached' })
    await page.waitForURL(/\/c\/[a-z2-7]{12}$/)
    const guestConversation = page.url().split('/').at(-1)
    const guestMe = await apiGet('/api/me')
    assert.equal(guestMe.user_id, guestUser, '注册后会话未切换到新账号')
    assert.equal((await apiGet(`/api/conversations/${guestConversation}`)).id, guestConversation, '注册后未继续创建对话')
    await capture('21-guest-register')
    await json('DELETE', `/api/conversations/${guestConversation}`, undefined, 204)

    // 收尾：退出新账号并用 demo 重新登录，保持后续断言使用的登录态。
    await page.goto(baseURL, { waitUntil: 'domcontentloaded' })
    await page.getByRole('button', { name: '用户菜单', exact: true }).click()
    await page.getByRole('button', { name: '退出登录', exact: true }).click()
    await page.getByRole('heading', { name: '今天想构建什么？' }).waitFor()
    await page.locator('.sidebar-user').getByRole('button', { name: '登录或注册' }).click()
    const loginDialog = page.getByRole('dialog', { name: '登录或注册' })
    await loginDialog.waitFor()
    await page.locator('#username').fill('demo')
    await page.locator('#password').fill(password)
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await loginDialog.waitFor({ state: 'detached' })
    await page.getByRole('button', { name: '用户菜单', exact: true }).waitFor()
    assert.match((await page.locator('.sidebar-user__text').textContent()) || '', /demo/, '收尾未恢复到 demo 登录态')

    coverage.guest_browse = {
      first_visit_stays_on_home: true,
      no_protected_requests_while_guest: true,
      create_prompts_auth_dialog: true,
      register_returns_201_and_continues_create: true,
      reload_after_logout_keeps_guest_home: true,
    }
  }

  await verifyAuthentication()
  await verifyPagination()
  await verifySlowRunAndModel()
  await verifyMarkdownAndUsage()
  await verifyFailureAndRetry()
  await verifyRecoveryAndWorkspace()
  await verifySessionRedirects()
  await verifyThirdPartyCookies()
  await verifySleepAndWake()
  await verifyGuestBrowseAndRegistration()

  return { coverage, screenshots, notCovered }
}
