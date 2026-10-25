import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import AuthForm from './AuthForm.vue'

describe('AuthForm', () => {
  let pinia: Pinia
  let wrapper: VueWrapper | undefined

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-25T10:00:00Z'))
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    vi.useRealTimers()
  })

  function mountForm(initialTab: 'login' | 'register' = 'login'): VueWrapper {
    wrapper = mount(AuthForm, { props: { initialTab }, global: { plugins: [pinia] } })
    return wrapper
  }

  async function fill(page: VueWrapper, values: Record<string, string>): Promise<void> {
    for (const [selector, value] of Object.entries(values)) {
      await page.find(selector).setValue(value)
    }
  }

  async function submit(page: VueWrapper): Promise<void> {
    await page.find('form').trigger('submit')
    await flushPromises()
  }

  it('注册成功后回传 register，并把昵称一起提交', async () => {
    const register = vi.spyOn(api.auth, 'register').mockResolvedValue({ user_id: 'carol', name: 'Carol' })
    const page = mountForm('register')

    await fill(page, { '#username': ' carol ', '#display-name': 'Carol', '#password': 'password123', '#confirm-password': 'password123' })
    await submit(page)

    expect(register).toHaveBeenCalledWith({ username: 'carol', password: 'password123', name: 'Carol' })
    expect(page.emitted('success')?.[0]).toEqual(['register'])
  })

  it('两次密码不一致时不发请求', async () => {
    const register = vi.spyOn(api.auth, 'register')
    const page = mountForm('register')

    await fill(page, { '#username': 'carol', '#password': 'password123', '#confirm-password': 'password124' })
    await submit(page)

    expect(register).not.toHaveBeenCalled()
    expect(page.get('[role="alert"]').text()).toBe('两次输入的密码不一致')
    expect(page.emitted('success')).toBeUndefined()
  })

  it('用户名含空格时前端直接拦下，不发请求', async () => {
    const register = vi.spyOn(api.auth, 'register')
    const page = mountForm('register')

    await fill(page, { '#username': 'bad name', '#password': 'password123', '#confirm-password': 'password123' })
    await submit(page)

    expect(register).not.toHaveBeenCalled()
    expect(page.get('[role="alert"]').text()).toContain('用户名只能包含')
  })

  it('409 用户名重复时展示可读错误且不回传成功', async () => {
    vi.spyOn(api.auth, 'register').mockRejectedValue(new ApiError({ code: 'USER_EXISTS', status: 409 }))
    const page = mountForm('register')

    await fill(page, { '#username': 'carol', '#password': 'password123', '#confirm-password': 'password123' })
    await submit(page)

    expect(page.get('[role="alert"]').text()).toBe('用户名已被占用，请换一个')
    expect(page.emitted('success')).toBeUndefined()
  })

  it('注册 429 后按 Retry-After 倒计时禁用提交，结束后恢复', async () => {
    vi.spyOn(api.auth, 'register').mockRejectedValue(new ApiError({ code: 'RATE_LIMITED', status: 429, retryAfter: 5 }))
    const page = mountForm('register')
    const button = page.get('.login-form__submit')

    await fill(page, { '#username': 'carol', '#password': 'password123', '#confirm-password': 'password123' })
    await submit(page)

    expect(page.get('[role="alert"]').text()).toContain('5 秒')
    expect(button.text()).toContain('5 秒 后重试')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)

    await vi.advanceTimersByTimeAsync(5000)
    await page.vm.$nextTick()
    expect((button.element as HTMLButtonElement).disabled).toBe(false)
    expect(page.find('[role="alert"]').exists()).toBe(false)
  })

  it('登录成功后回传 login，并切换页签重置密码输入', async () => {
    vi.spyOn(api.auth, 'login').mockResolvedValue(undefined)
    vi.spyOn(api.auth, 'me').mockResolvedValue({ user_id: 'alice', name: 'Alice' })
    const page = mountForm('login')

    await fill(page, { '#username': 'alice', '#password': 'password123' })
    await submit(page)

    expect(page.emitted('success')?.[0]).toEqual(['login'])

    await page.findAll('[role="tab"]')[1].trigger('click')
    expect(page.get('.login-form__submit').text()).toBe('注册并登录')
    expect((page.find('#password').element as HTMLInputElement).value).toBe('')
  })
})
