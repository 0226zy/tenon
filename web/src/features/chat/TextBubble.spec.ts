import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import TextBubble from './TextBubble.vue'

describe('TextBubble Markdown 安全', () => {
  it('不执行原始 HTML，并保留安全 Markdown', () => {
    const wrapper = mount(TextBubble, {
      props: { content: '**加粗**\n\n<img src="x" onerror="window.pwned=1">\n\n[安全链接](https://example.com)' },
    })
    expect(wrapper.find('strong').text()).toBe('加粗')
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.find('[onerror]').exists()).toBe(false)
    expect((window as Window & { pwned?: number }).pwned).toBeUndefined()
    const link = wrapper.find('a')
    expect(link.attributes('href')).toBe('https://example.com')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toContain('noopener')
  })

  it('过滤 javascript 链接', () => {
    const wrapper = mount(TextBubble, { props: { content: '[危险](javascript:alert(1))' } })
    expect(wrapper.find('a').exists()).toBe(false)
  })
})
