import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { safeInternalRedirect } from '@/utils/navigation'
import LoginPage from '@/features/login/LoginPage.vue'

const HomePage = () => import('@/features/shell/HomePage.vue')

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/login', name: 'login', component: LoginPage },
    { path: '/', name: 'home', component: HomePage },
    { path: '/c/:cid', name: 'conversation', component: HomePage },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

// 首页与欢迎页对游客开放；只有进入某个对话时才要求登录，
// 创建对话的登录/注册拦截由首页与侧栏在动作发生时触发。
router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (to.name === 'login') {
    const user = await auth.ensure().catch(() => null)
    return user ? safeInternalRedirect(to.query.redirect) : true
  }
  if (to.name === 'conversation') {
    const user = await auth.ensure().catch(() => null)
    if (!user) return { path: '/login', query: { redirect: safeInternalRedirect(to.fullPath) } }
  }
  return true
})
