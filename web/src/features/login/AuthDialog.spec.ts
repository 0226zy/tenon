import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { api } from '@/api'
import AuthDialog from './AuthDialog.vue'

describe('AuthDialog', () => {
  let pinia: Pinia
  let wrapper: VueWrapper | undefined

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    document.body.classList.remove('is-modal-open')
  })

  function mountDialog(tab: 'login' | 'register' = 'login'): VueWrapper {
    wrapper = mount(AuthDialog, {
      props: { tab },
      global: { plugins: [pinia], stubs: { teleport: true } },
    })
    return wrapper
  }

  it('直接展示注册页签，并可提交注册', async () => {
    vi.spyOn(api.auth, 'register').mockResolvedValue({ user_id: 'carol', name: 'carol' })
    const page = mountDialog('register')

    expect(page.get('[role="dialog"]').attributes('aria-label')).toBe('登录或注册')
    expect(page.get('.login-form__submit').text()).toBe('注册并登录')

    await page.find('#username').setValue('carol')
    await page.find('#password').setValue('password123')
    await page.find('#confirm-password').setValue('password123')
    await page.find('form').trigger('submit')
    await flushPromises()

    expect(page.emitted('success')?.[0]).toEqual(['register'])
  })

  it('Escape 与关闭按钮都会触发 close', async () => {
    const page = mountDialog()

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(page.emitted('close')).toHaveLength(1)

    await page.get('[aria-label="关闭登录窗口"]').trigger('click')
    expect(page.emitted('close')).toHaveLength(2)
  })

  it('打开时锁定页面滚动，卸载后恢复', async () => {
    const page = mountDialog()
    expect(document.body.classList.contains('is-modal-open')).toBe(true)
    page.unmount()
    wrapper = undefined
    expect(document.body.classList.contains('is-modal-open')).toBe(false)
  })
})
