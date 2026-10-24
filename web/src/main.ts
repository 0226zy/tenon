import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { setUnauthorizedHandler } from './api/http'
import { useAuthStore } from './stores/auth'
import { safeInternalRedirect } from './utils/navigation'
import './styles/main.css'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(router)

const auth = useAuthStore(pinia)
setUnauthorizedHandler(() => {
  const shouldNavigate = auth.checked && router.currentRoute.value.path !== '/login'
  const redirect = safeInternalRedirect(router.currentRoute.value.fullPath)
  auth.clear()
  if (shouldNavigate) void router.push({ path: '/login', query: { redirect } })
})

void router.isReady().then(() => app.mount('#app'))
