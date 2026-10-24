import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import type { RunStatus } from '@/types/contract'
import StatusBar from './StatusBar.vue'

const cases: Array<[RunStatus, string, string]> = [
  ['queued', '排队中', '资源空闲后自动开始'],
  ['running', '执行中', '正在生成页面'],
  ['cancelling', '取消中', '确认停止后才会结束本轮任务'],
  ['finalizing', '收尾中', '正在保存本轮改动'],
]

describe('StatusBar', () => {
  it.each(cases)('%s 状态向用户展示准确阶段与说明', (status, label, detail) => {
    const wrapper = mount(StatusBar, {
      props: { status, progress: '正在生成页面', connection: 'open' },
    })
    const bar = wrapper.get('[role="status"]')
    expect(bar.get('strong').text()).toBe(label)
    expect(bar.text()).toContain(detail)
    wrapper.unmount()
  })

  it('终态不显示活跃状态，断线提示优先于执行进度', async () => {
    const wrapper = mount(StatusBar, {
      props: { status: 'finished' as RunStatus, connection: 'open' as const },
    })
    expect(wrapper.find('.run-status-bar').exists()).toBe(false)

    await wrapper.setProps({ status: 'running', connection: 'retrying' })
    expect(wrapper.get('[role="status"]').text()).toContain('连接中断，正在重连')
    expect(wrapper.get('[role="status"]').text()).toContain('恢复后会自动补齐记录')
    expect(wrapper.get('[role="status"]').text()).not.toContain('执行中')
    wrapper.unmount()
  })
})
