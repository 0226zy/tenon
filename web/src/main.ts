import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { setUnauthorizedHandler } from './api/http'
import { useAuthStore } from './stores/auth'
import { safeInternalRedirect } from './utils/navigation'
import './styles/main.css'
import './styles/auth.css'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(router)

const auth = useAuthStore(pinia)
// 会话失效时只清理本地状态：游客停留在首页继续浏览，
// 只有对话页（需要登录态）才回跳到登录页。
setUnauthorizedHandler(() => {
  const route = router.currentRoute.value
  const needsLogin = route.name === 'conversation'
  const redirect = safeInternalRedirect(route.fullPath)
  auth.clear()
  if (needsLogin) void router.push({ path: '/login', query: { redirect } })
})

void router.isReady().then(() => {
  app.mount('#app')
  // 已登录用户直接带着会话浏览首页；游客静默保持未登录状态。
  void auth.ensure().catch(() => undefined)
})
