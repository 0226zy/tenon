import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { safeInternalRedirect } from '@/utils/navigation'
import LoginPage from '@/features/login/LoginPage.vue'

const HomePage = () => import('@/features/shell/HomePage.vue')

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginPage },
    { path: '/', name: 'home', component: HomePage },
    { path: '/c/:cid', name: 'conversation', component: HomePage },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  const user = await auth.ensure().catch(() => null)
  if (to.path === '/login') {
    return user ? safeInternalRedirect(to.query.redirect) : true
  }
  if (!user) {
    return { path: '/login', query: { redirect: safeInternalRedirect(to.fullPath) } }
  }
  return true
})
