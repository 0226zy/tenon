export interface ExamplePrompt {
  title: string
  description: string
  prompt: string
  icon: 'chart' | 'inventory' | 'calendar'
}

export const EXAMPLE_PROMPTS: ExamplePrompt[] = [
  {
    title: '客户反馈看板',
    description: '收集反馈、筛选状态，并查看趋势',
    prompt: '做一个客户反馈看板：支持提交反馈、按状态筛选，并用图表展示最近一个月的反馈趋势。',
    icon: 'chart',
  },
  {
    title: '轻量库存管理',
    description: '商品、库存预警与出入库记录',
    prompt: '创建一个轻量库存管理应用，包含商品列表、库存预警和出入库记录，支持按商品名称搜索。',
    icon: 'inventory',
  },
  {
    title: '读书会预约',
    description: '浏览书目、预约场次并管理报名',
    prompt: '做一个团队读书会预约页面，能浏览书目、预约场次并管理报名，首页展示下一场活动。',
    icon: 'calendar',
  },
]
