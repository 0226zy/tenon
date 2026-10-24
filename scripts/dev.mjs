import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { randomBytes } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const root = fileURLToPath(new URL('../', import.meta.url))
const controlConfig = process.env.CONTROL_CONFIG || 'deploy/control.local.yaml'
const viteBin = path.join(root, 'web', 'node_modules', 'vite', 'bin', 'vite.js')
if (!existsSync(viteBin)) {
  console.error('缺少前端依赖，请先运行 npm --prefix web install。')
  process.exit(1)
}

const env = {
  ...process.env,
  GOTOOLCHAIN: process.env.GOTOOLCHAIN || 'go1.25.0',
  GOPROXY: process.env.GOPROXY || 'https://goproxy.cn,direct',
}
const builds = [
  ['bin/control', './cmd/control', root],
  ['bin/wsd', './cmd/wsd', root],
  ['bin/agent-runtime', './cmd/agent-runtime', path.join(root, 'agent-runtime')],
]
for (const [output, pkg, cwd] of builds) {
  console.log(`构建 ${output}`)
  const result = spawnSync('go', ['build', '-o', output, pkg], { cwd, env, stdio: 'inherit' })
  if (result.error || result.status !== 0) {
    if (result.error) console.error(result.error)
    process.exit(result.status || 1)
  }
}

const localDir = path.join(root, '.local')
const passwordFile = path.join(localDir, 'demo-password')
mkdirSync(localDir, { recursive: true, mode: 0o700 })
const password = existsSync(passwordFile)
  ? readFileSync(passwordFile, 'utf8').trim()
  : randomBytes(18).toString('base64url')
if (!existsSync(passwordFile)) writeFileSync(passwordFile, `${password}\n`, { mode: 0o600 })
const controlBin = path.join(root, 'bin', 'control')
const accountEnv = { ...env, CONTROL_PASSWORD: password }
let account = spawnSync(controlBin, ['user', 'add', '-config', controlConfig, '-user', 'demo', '-name', '本地验收'], { cwd: root, env: accountEnv, encoding: 'utf8' })
if (account.status !== 0) {
  account = spawnSync(controlBin, ['user', 'passwd', '-config', controlConfig, '-user', 'demo'], { cwd: root, env: accountEnv, encoding: 'utf8' })
}
if (account.error || account.status !== 0) {
  console.error(account.error || account.stderr || account.stdout || '无法初始化本地验收账号')
  process.exit(account.status || 1)
}
console.log(`本地验收账号：demo；密码：${password}`)

function service(command, args, cwd) {
  const child = spawn(command, args, { cwd, env, stdio: 'inherit' })
  const exited = new Promise((resolve) => {
    child.once('error', (error) => {
      console.error(`${command} 启动失败：${error.message}`)
      resolve(1)
    })
    child.once('exit', (code, signal) => resolve(code ?? (signal ? 1 : 0)))
  })
  return { child, exited }
}

const control = service(path.join(root, 'bin', 'control'), ['serve', '-config', controlConfig], root)
const web = service(process.execPath, [viteBin, ...process.argv.slice(2)], path.join(root, 'web'))
const services = [control, web]
let shutdownPromise

function shutdown() {
  if (shutdownPromise) return shutdownPromise
  shutdownPromise = (async () => {
    for (const { child } of services) {
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
    }
    const timer = setTimeout(() => {
      for (const { child } of services) {
        if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL')
      }
    }, 8000)
    await Promise.all(services.map(({ exited }) => exited))
    clearTimeout(timer)
  })()
  return shutdownPromise
}

let signalResolve
const interrupted = new Promise((resolve) => { signalResolve = resolve })
for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => signalResolve({ type: 'signal', code: 0 }))
}

const outcome = await Promise.race([
  control.exited.then((code) => ({ type: 'control', code })),
  web.exited.then((code) => ({ type: 'web', code })),
  interrupted,
])
if (outcome.type !== 'signal') {
  console.error(`${outcome.type} 已退出，正在关闭另一个进程。`)
}
await shutdown()
process.exitCode = outcome.type === 'signal' ? 0 : (outcome.code || 1)
