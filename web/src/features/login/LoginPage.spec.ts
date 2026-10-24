import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { api } from '@/api'
import { ApiError } from '@/api/errors'
import { useAuthStore } from '@/stores/auth'
import LoginPage from './LoginPage.vue'

const { route, replace } = vi.hoisted(() => ({
  route: { query: { redirect: '/c/c1' as unknown } },
  replace: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => ({ replace }),
}))

describe('LoginPage', () => {
  let pinia: Pinia
  let wrapper: VueWrapper | undefined

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    replace.mockReset()
    route.query.redirect = '/c/c1'
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-24T10:00:00Z'))
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    vi.useRealTimers()
  })

  function mountPage(): VueWrapper {
    wrapper = mount(LoginPage, { global: { plugins: [pinia] } })
    return wrapper
  }

  async function submitCredentials(page: VueWrapper): Promise<void> {
    await page.find('#username').setValue('alice')
    await page.find('#password').setValue('password123')
    await page.find('form').trigger('submit')
    await flushPromises()
  }

  it('错误密码或停用账号的 401 显示统一登录错误且不回跳', async () => {
    vi.spyOn(api.auth, 'login').mockRejectedValue(new ApiError({ code: 'UNAUTHENTICATED', status: 401 }))
    const page = mountPage()

    await submitCredentials(page)

    expect(page.get('[role="alert"]').text()).toBe('用户名或密码错误')
    expect(replace).not.toHaveBeenCalled()
    expect((page.get('#username').element as HTMLInputElement).disabled).toBe(false)
  })

  it('429 后按 Retry-After 显示倒计时，期间禁用登录，结束后恢复', async () => {
    const login = vi.spyOn(api.auth, 'login').mockRejectedValue(
      new ApiError({ code: 'RATE_LIMITED', status: 429, retryAfter: 2 }),
    )
    const page = mountPage()
    const button = page.get('.login-form__submit')

    await submitCredentials(page)

    expect(login).toHaveBeenCalledOnce()
    expect(page.get('[role="alert"]').text()).toContain('2 秒')
    expect(button.text()).toContain('2 秒 后重试')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)

    await vi.advanceTimersByTimeAsync(1000)
    await page.vm.$nextTick()
    expect(page.get('[role="alert"]').text()).toContain('1 秒')
    expect(button.text()).toContain('1 秒 后重试')

    await vi.advanceTimersByTimeAsync(1000)
    await page.vm.$nextTick()
    expect((button.element as HTMLButtonElement).disabled).toBe(false)
    expect(button.text()).toBe('登录')
    expect(page.find('[role="alert"]').exists()).toBe(false)
  })

  it('锁定期内即使触发表单提交也不会被当成登录成功回跳', async () => {
    const login = vi.spyOn(api.auth, 'login').mockResolvedValue(undefined)
    const auth = useAuthStore()
    auth.lockedUntil = Date.now() + 30_000
    const page = mountPage()

    await submitCredentials(page)

    expect(login).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect((page.get('.login-form__submit').element as HTMLButtonElement).disabled).toBe(true)
  })
})
