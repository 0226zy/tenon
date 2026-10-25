import { spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdirSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import assert from 'node:assert/strict'
import { chromium } from 'playwright'
import { runExtendedP0 } from './e2e_p0.mjs'

const root = fileURLToPath(new URL('../', import.meta.url))
const outDir = path.join(root, '.local', 'e2e')
const baseURL = 'http://127.0.0.1:7100'
const chromePath = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
mkdirSync(outDir, { recursive: true, mode: 0o700 })
if (!existsSync(chromePath)) throw new Error(`找不到本机 Chrome：${chromePath}`)
const testDataDir = path.join(outDir, 'data')
if (existsSync(testDataDir)) {
  // Go 的模块缓存目录默认只读；先恢复属主写权限再清理上一轮数据。
  const chmod = spawnSync('chmod', ['-R', 'u+w', testDataDir], { stdio: 'inherit' })
  if (chmod.error || chmod.status !== 0) throw chmod.error || new Error('无法恢复上一轮验收数据的清理权限')
  rmSync(testDataDir, { recursive: true, force: true, maxRetries: 3, retryDelay: 200 })
}

const devLog = openSync(path.join(outDir, 'dev.log'), 'w')
const dev = spawn(process.execPath, ['scripts/dev.mjs', '--host', '127.0.0.1', '--port', '7100', '--strictPort'], {
  cwd: root,
  env: { ...process.env, CONTROL_CONFIG: 'deploy/control.e2e.yaml' },
  stdio: ['ignore', devLog, devLog],
})
let browser
let page
const pageErrors = []

async function waitForAPI() {
  const deadline = Date.now() + 120_000
  while (Date.now() < deadline) {
    if (dev.exitCode !== null) throw new Error(`开发服务提前退出，代码 ${dev.exitCode}；详见 .local/e2e/dev.log`)
    try {
      const response = await fetch(`${baseURL}/api/models`, { signal: AbortSignal.timeout(1500) })
      if (response.status === 401) return
    } catch { /* 服务仍在启动 */ }
    await new Promise((resolve) => setTimeout(resolve, 400))
  }
  throw new Error('等待开发服务超时；详见 .local/e2e/dev.log')
}

async function stopDev() {
  if (dev.exitCode !== null || dev.signalCode !== null) return
  dev.kill('SIGTERM')
  const timeout = setTimeout(() => dev.kill('SIGKILL'), 20_000)
  await once(dev, 'exit')
  clearTimeout(timeout)
}

async function screenshot(name) {
  await page.screenshot({ path: path.join(outDir, `${name}.png`), fullPage: true })
}

try {
  await waitForAPI()
  const password = readFileSync(path.join(root, '.local', 'demo-password'), 'utf8').trim()
  assert.ok(password.length >= 8, '本地验收密码未正确初始化')

  browser = await chromium.launch({
    executablePath: chromePath,
    headless: process.env.HEADFUL !== '1',
    args: ['--no-first-run', '--no-default-browser-check'],
  })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, locale: 'zh-CN' })
  page = await context.newPage()
  page.setDefaultTimeout(30_000)
  page.on('pageerror', (error) => pageErrors.push(error.message))

  async function apiFetch(pathname) {
    const cookies = await context.cookies(baseURL)
    return fetch(`${baseURL}${pathname}`, {
      headers: { cookie: cookies.map((cookie) => `${cookie.name}=${cookie.value}`).join('; ') },
      signal: AbortSignal.timeout(30_000),
    })
  }
  async function apiGet(pathname) {
    const response = await apiFetch(pathname)
    const body = await response.text()
    assert.equal(response.status, 200, `GET ${pathname}：${response.status} ${body.slice(0, 500)}`)
    return JSON.parse(body)
  }
  function toolCards(name) {
    return page.locator('.tool-card').filter({
      has: page.locator('.tool-card__name').getByText(name, { exact: true }),
    })
  }
  async function assertSuccessfulTools(expectedRuns) {
    for (const name of ['write_file', 'run_tests', 'preview']) {
      const cards = toolCards(name)
      assert.equal(await cards.count(), expectedRuns, `${name} 工具卡片数量不正确或重复`)
      for (let index = 0; index < expectedRuns; index += 1) {
        await cards.nth(index).locator('.tool-card__state', { hasText: '已完成' }).waitFor({ timeout: 30_000 })
      }
    }
    const failed = page.locator('.tool-card--failed')
    assert.equal(await failed.count(), 0, `存在失败的工具调用：${await failed.allTextContents()}`)
  }

  await page.goto(baseURL, { waitUntil: 'domcontentloaded' })
  assert.equal(await page.title(), 'Tenon · 原型工坊')
  // 首次访问是游客首页：不跳登录页，浏览首页不发受保护请求。
  await page.getByRole('heading', { name: '今天想构建什么？' }).waitFor()
  assert.equal(new URL(page.url()).pathname, '/', '首次访问被重定向到登录页')
  await page.locator('.sidebar-brand__text').waitFor()
  assert.match((await page.locator('.sidebar-brand__text').textContent()) || '', /Tenon/)
  await page.locator('.sidebar-user').getByRole('button', { name: '登录或注册' }).click()
  const authDialog = page.getByRole('dialog', { name: '登录或注册' })
  await authDialog.waitFor()
  await page.locator('#username').fill('demo')
  await page.locator('#password').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await authDialog.waitFor({ state: 'detached' })
  await page.getByRole('heading', { name: '今天想构建什么？' }).waitFor()

  await page.getByRole('button', { name: /从空白对话开始/ }).click()
  await page.waitForURL(/\/c\/[a-z2-7]{12}$/)
  const conversationID = page.url().split('/').at(-1)
  const message = `Chrome 真实验收 ${new Date().toISOString()}：生成可运行的产品状态看板。`
  const firstAcceptedPromise = page.waitForResponse((response) =>
    response.url().endsWith(`/api/conversations/${conversationID}/messages`) && response.request().method() === 'POST')
  await page.getByRole('textbox', { name: '需求内容' }).fill(message)
  await page.getByRole('button', { name: '发送', exact: true }).click()
  const firstAccepted = await firstAcceptedPromise
  assert.equal(firstAccepted.status(), 202, '首轮消息未被接受')
  await firstAccepted.body()
  const autoTitleLine = message.split('\n', 1)[0].trim()
  const autoTitleRunes = [...autoTitleLine]
  const expectedAutoTitle = autoTitleRunes.length > 32 ? `${autoTitleRunes.slice(0, 32).join('')}…` : (autoTitleLine || '新对话')
  assert.equal((await apiGet(`/api/conversations/${conversationID}`)).title, expectedAutoTitle, '自动标题未按首行 32 个 Unicode 码点生成')
  await page.locator('.tool-card').first().waitFor({ timeout: 120_000 })
  await page.locator('.run-end-card--finished').waitFor({ timeout: 240_000 })
  await assertSuccessfulTools(1)

  await page.locator('.tool-card__toggle').first().click()
  await page.locator('.tool-card__details').first().waitFor()
  await screenshot('01-chat-and-tools')

  await page.locator('.run-end-card__files-toggle').click()
  await page.locator('.run-end-card__files button', { hasText: 'web/src/App.tsx' }).click()
  await page.waitForFunction(() => document.querySelector('.editor-pane__path')?.textContent?.includes('web/src/App.tsx'))
  await page.waitForFunction(() => document.querySelector('.monaco-container')?.textContent?.replace(/\u00a0/g, ' ').includes('LOCAL DEMO'), null, { timeout: 60_000 })
  assert.ok(await page.locator('.readonly-badge').isVisible(), '代码编辑器未显示只读状态')
  await screenshot('02-generated-code')

  await page.getByRole('tab', { name: /预览/ }).click()
  const iframe = page.locator('iframe[title="生成应用预览"]')
  await iframe.waitFor({ timeout: 180_000 })
  const previewFrame = page.frameLocator('iframe[title="生成应用预览"]')
  await previewFrame.getByRole('heading', { name: '原型已真实修改' }).waitFor({ timeout: 60_000 })
  await previewFrame.getByText(message, { exact: false }).waitFor()
  const firstPreviewSrc = await iframe.getAttribute('src')
  const previewHost = new URL(firstPreviewSrc).host
  assert.ok(previewHost.endsWith('.preview.localhost:8081'), `预览域名不正确：${previewHost}`)
  await screenshot('03-live-preview')

  const popupPromise = page.waitForEvent('popup')
  await page.getByRole('button', { name: '新窗口', exact: true }).click()
  const popup = await popupPromise
  await popup.waitForLoadState('domcontentloaded')
  await popup.getByRole('heading', { name: '原型已真实修改' }).waitFor({ timeout: 60_000 })
  await popup.close()

  await page.getByRole('button', { name: '查看日志', exact: true }).click()
  await page.locator('.log-drawer__content').waitFor()
  await page.waitForFunction(() => (document.querySelector('.log-drawer__content')?.textContent || '').trim().length > 0)
  await page.getByRole('button', { name: '关闭日志', exact: true }).click()

  const historyResponse = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return url.pathname === `/api/conversations/${conversationID}/messages` && response.request().method() === 'GET'
  })
  await page.reload({ waitUntil: 'domcontentloaded' })
  await historyResponse
  await page.locator('.run-end-card--finished').waitFor({ timeout: 60_000 })
  await page.getByText(message, { exact: true }).waitFor({ timeout: 60_000 })
  await assertSuccessfulTools(1)
  assert.equal(await page.locator('.user-message').count(), 1, '刷新后用户消息重复')
  await screenshot('04-history-recovered')

  // Start a real second Run, disconnect Chrome for at least 30 seconds, and
  // confirm from the server that it finished while the browser was offline.
  const secondMessage = `第二轮需求 ${new Date().toISOString()}：离线完成后自动恢复并刷新预览。`
  await page.getByRole('textbox', { name: '需求内容' }).fill(secondMessage)
  const acceptedResponse = page.waitForResponse((response) =>
    response.url().endsWith(`/api/conversations/${conversationID}/messages`) && response.request().method() === 'POST')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  const accepted = await (await acceptedResponse).json()
  const secondRunID = accepted.run_id
  assert.ok(secondRunID, '第二轮没有返回 run_id')
  await page.locator('.run-status-bar').waitFor()
  await context.setOffline(true)
  const offlineStartedAt = Date.now()
  await page.getByText('连接中断，正在重连', { exact: true }).waitFor({ timeout: 30_000 })
  await page.waitForTimeout(30_000)
  await screenshot('05-offline-30s')

  let secondTerminal
  const secondDeadline = Date.now() + 240_000
  while (Date.now() < secondDeadline) {
    secondTerminal = await apiGet(`/api/conversations/${conversationID}/runs/${secondRunID}`)
    if (['finished', 'failed', 'cancelled'].includes(secondTerminal.status)) break
    await new Promise((resolve) => setTimeout(resolve, 1000))
  }
  assert.equal(secondTerminal?.status, 'finished', `离线期间第二轮未成功完成：${JSON.stringify(secondTerminal)}`)
  assert.equal(await page.locator('.run-end-card--finished').count(), 1, '离线期间意外收到了终态，无法验证补漏')
  const offlineSeconds = Math.round((Date.now() - offlineStartedAt) / 1000)
  assert.ok(offlineSeconds >= 30, '实际断网时间不足 30 秒')

  await context.setOffline(false)
  await page.locator('.run-end-card--finished').nth(1).waitFor({ timeout: 90_000 })
  await assertSuccessfulTools(2)
  assert.equal(await page.locator('.user-message').count(), 2, '断网恢复后用户消息重复或缺失')
  assert.equal(await page.getByText(secondMessage, { exact: true }).count(), 1, '第二轮消息恢复后重复或缺失')
  await screenshot('06-offline-recovered')

  const secondGeneration = secondTerminal.generation
  assert.ok(secondGeneration >= 2, `第二轮 generation 不正确：${secondGeneration}`)
  await page.getByRole('tab', { name: /预览/ }).click()
  await previewFrame.getByText(secondMessage, { exact: false }).waitFor({ timeout: 180_000 })
  await page.locator('.preview-status__version', { hasText: `#${secondGeneration}` }).waitFor({ timeout: 60_000 })
  assert.notEqual(await iframe.getAttribute('src'), firstPreviewSrc, '第二轮预览仍使用旧票据或旧页面')
  await screenshot('07-second-preview')

  const beforeRestartSrc = await iframe.getAttribute('src')
  await page.getByRole('button', { name: '重启预览', exact: true }).click()
  await page.getByText('正在重启预览', { exact: true }).waitFor()
  await page.getByText('正在重启预览', { exact: true }).waitFor({ state: 'hidden', timeout: 180_000 })
  await page.waitForFunction((previous) => document.querySelector('iframe[title="生成应用预览"]')?.src !== previous, beforeRestartSrc, { timeout: 60_000 })
  await page.locator('.preview-status--ready').waitFor({ timeout: 60_000 })
  await previewFrame.getByText(secondMessage, { exact: false }).waitFor({ timeout: 60_000 })
  await screenshot('08-preview-restarted')

  const renamedTitle = `Chrome 全链路验收 ${conversationID}`
  const activeConversation = page.locator('.conv-item--active')
  await activeConversation.hover()
  await activeConversation.getByRole('button', { name: '重命名项目', exact: true }).click()
  await activeConversation.getByRole('textbox', { name: '项目标题' }).fill(renamedTitle)
  await activeConversation.getByRole('button', { name: '保存标题', exact: true }).click()
  await activeConversation.locator('.conv-item__title').getByText(renamedTitle, { exact: true }).waitFor()
  assert.equal((await apiGet(`/api/conversations/${conversationID}`)).title, renamedTitle, '重命名未持久化')

  // Use a separate conversation for cancellation so expected cancelled tool
  // results cannot weaken the main conversation's all-tools-success assertion.
  await page.getByRole('button', { name: '新建项目', exact: true }).click()
  await page.waitForURL((url) => /\/c\/[a-z2-7]{12}$/.test(url.pathname) && !url.pathname.endsWith(conversationID))
  const cancelConversationID = page.url().split('/').at(-1)
  const cancelMessage = `取消与重新执行验收 ${new Date().toISOString()}`
  await page.getByRole('textbox', { name: '需求内容' }).fill(cancelMessage)
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await page.locator('.composer__send--cancel').waitFor({ timeout: 30_000 })
  await page.locator('.composer__send--cancel').click()
  await page.locator('.run-end-card--cancelled').waitFor({ timeout: 120_000 })
  await page.getByRole('button', { name: '重新执行', exact: true }).click()
  await page.locator('.run-end-card--finished').waitFor({ timeout: 240_000 })
  assert.equal(await page.locator('.run-end-card--cancelled').count(), 1, '取消终态丢失或重复')
  assert.equal(await page.locator('.user-message').count(), 2, '重新执行应产生一条新的用户消息')
  await screenshot('09-cancel-and-retry')

  const cancelConversation = page.locator('.conv-item--active')
  await cancelConversation.hover()
  await cancelConversation.getByRole('button', { name: '删除对话', exact: true }).click()
  await page.getByRole('dialog', { name: '删除这个项目？' }).getByRole('button', { name: '删除项目', exact: true }).click()
  await page.getByRole('heading', { name: '今天想构建什么？' }).waitFor({ timeout: 60_000 })
  const deleted = await apiFetch(`/api/conversations/${cancelConversationID}`)
  await deleted.arrayBuffer()
  assert.equal(deleted.status, 404, '删除后仍可通过 API 读取对话')

  await page.locator('.conv-item__title').getByText(renamedTitle, { exact: true }).click()
  await page.waitForURL(new RegExp(`/c/${conversationID}$`))
  await page.locator('.run-end-card--finished').nth(1).waitFor({ timeout: 60_000 })
  await assertSuccessfulTools(2)

  await page.getByRole('button', { name: '用户菜单', exact: true }).click()
  await page.getByRole('button', { name: '退出登录', exact: true }).click()
  // 退出登录回到游客首页，再通过侧栏入口登录。
  await page.getByRole('heading', { name: '今天想构建什么？' }).waitFor()
  await page.locator('.sidebar-user').getByRole('button', { name: '登录或注册' }).click()
  const reloginDialog = page.getByRole('dialog', { name: '登录或注册' })
  await reloginDialog.waitFor()
  await page.locator('#username').fill('demo')
  await page.locator('#password').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await reloginDialog.waitFor({ state: 'detached' })
  await page.getByRole('heading', { name: '今天想构建什么？' }).waitFor()
  await page.locator('.conv-item__title').getByText(renamedTitle, { exact: true }).click()
  await page.waitForURL(new RegExp(`/c/${conversationID}$`))
  await page.locator('.run-end-card--finished').nth(1).waitFor({ timeout: 60_000 })
  await assertSuccessfulTools(2)
  assert.equal(await page.locator('.user-message').count(), 2, '重新登录后消息历史不一致')
  await screenshot('10-restored-after-login')

  const extended = await runExtendedP0({
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
  })

  assert.deepEqual(pageErrors, [], `页面出现未捕获异常：${pageErrors.join('；')}`)
  const result = {
    ok: true,
    chrome: browser.version(),
    conversation_id: conversationID,
    automatic_title: expectedAutoTitle,
    tool_cards: 6,
    tools_successful: ['write_file', 'run_tests', 'preview'],
    generated_file: 'web/src/App.tsx',
    preview_host: previewHost,
    new_window: true,
    preview_logs: true,
    history_recovery: true,
    offline_seconds: offlineSeconds,
    offline_recovery_without_duplicates: true,
    second_generation: secondGeneration,
    preview_auto_refresh: true,
    preview_restart: true,
    conversation_rename: true,
    cancel_and_retry: true,
    conversation_delete: true,
    logout_and_login: true,
    extended_p0: extended.coverage,
    not_covered: extended.notCovered,
    screenshots: [
      '01-chat-and-tools.png', '02-generated-code.png', '03-live-preview.png', '04-history-recovered.png',
      '05-offline-30s.png', '06-offline-recovered.png', '07-second-preview.png', '08-preview-restarted.png',
      '09-cancel-and-retry.png', '10-restored-after-login.png',
      ...extended.screenshots,
    ],
  }
  writeFileSync(path.join(outDir, 'result.json'), `${JSON.stringify(result, null, 2)}\n`)
  console.log(JSON.stringify(result, null, 2))
} catch (error) {
  if (page) {
    try { await screenshot('failure') } catch { /* 页面可能已关闭 */ }
    try {
      console.error('浏览器未捕获异常：', pageErrors)
      console.error('失败页面：', page.url())
      console.error('编辑器诊断：', await page.evaluate(() => ({
        text: document.querySelector('.editor-pane__body')?.textContent?.slice(0, 500),
        monaco: document.querySelector('.monaco-container')?.outerHTML.slice(0, 1000),
      })))
      console.error('消息诊断：', await page.evaluate(() => ({
        userMessages: [...document.querySelectorAll('.user-message')].map((item) => item.textContent),
        bubbles: [...document.querySelectorAll('.user-message__bubble')].map((item) => ({
          text: item.textContent,
          codes: [...(item.textContent || '')].map((ch) => ch.codePointAt(0)),
          rect: item.getBoundingClientRect().toJSON(),
        })),
        cards: document.querySelectorAll('.run-end-card').length,
        pageError: document.querySelector('.page-error')?.textContent,
      })))
    } catch { /* 页面可能已关闭 */ }
  }
  console.error(error)
  writeFileSync(path.join(outDir, 'result.json'), `${JSON.stringify({
    ok: false,
    error: error instanceof Error ? error.message : String(error),
    page: page?.url(),
  }, null, 2)}\n`)
  process.exitCode = 1
} finally {
  if (browser) await browser.close()
  await stopDev()
}
