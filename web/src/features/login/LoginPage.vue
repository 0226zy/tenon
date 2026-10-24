<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowRight, Braces, Check, Eye, EyeOff, MessageSquareText, Play, Sparkles } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { errorMessage } from '@/api/errors'
import { safeInternalRedirect } from '@/utils/navigation'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const showPassword = ref(false)
const validationError = ref('')
const lockError = ref(false)
const now = ref(Date.now())
let timer: number | undefined

const remaining = computed(() => Math.max(0, Math.ceil((auth.lockedUntil - now.value) / 1000)))
const lockText = computed(() => {
  if (!remaining.value) return ''
  const minutes = Math.floor(remaining.value / 60)
  const seconds = remaining.value % 60
  return minutes ? `${minutes} 分 ${seconds} 秒` : `${seconds} 秒`
})
const error = computed(() => {
  if (lockError.value) return remaining.value ? `尝试次数过多，请 ${lockText.value} 后再试` : ''
  return validationError.value
})
const submitting = computed(() => auth.loading)

onMounted(() => { timer = window.setInterval(() => { now.value = Date.now() }, 1000) })
onBeforeUnmount(() => { if (timer) window.clearInterval(timer) })

async function submit(): Promise<void> {
  if (remaining.value || submitting.value) return
  validationError.value = ''
  lockError.value = false
  if (!username.value.trim() || username.value.trim().length > 64) {
    validationError.value = '请输入 1–64 个字符的用户名'
    return
  }
  if (password.value.length < 8 || password.value.length > 128) {
    validationError.value = '密码需为 8–128 个字符'
    return
  }
  try {
    await auth.login(username.value.trim(), password.value)
    const target = safeInternalRedirect(route.query.redirect)
    await router.replace(target)
  } catch (reason) {
    validationError.value = errorMessage(reason, 'login')
    lockError.value = remaining.value > 0
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-story">
      <div class="login-brand">
        <span class="brand-mark brand-mark--large"><Braces :size="25" /></span>
        <span>Tenon</span>
      </div>
      <div class="login-story__content">
        <span class="eyebrow"><Sparkles :size="14" /> 从需求到原型，只需一次对话</span>
        <h1>把想法，变成<br /><em>可运行的原型。</em></h1>
        <p>无需搭建环境。描述需求、查看生成过程，立即在浏览器里验证你的产品想法。</p>
        <ul class="login-benefits">
          <li><span><MessageSquareText :size="18" /></span><div><strong>自然语言构建</strong><small>多轮对话，持续迭代同一个工作区</small></div><Check :size="16" /></li>
          <li><span><Braces :size="18" /></span><div><strong>代码全程可见</strong><small>只读浏览每一轮真实改动</small></div><Check :size="16" /></li>
          <li><span><Play :size="18" /></span><div><strong>预览即时验证</strong><small>自动构建，保留已确认版本</small></div><Check :size="16" /></li>
        </ul>
      </div>
      <p class="login-story__foot">内部可信试点 · 原型不用于生产部署</p>
    </section>

    <section class="login-form-side">
      <form class="login-form" novalidate @submit.prevent="submit">
        <div class="login-form__mobile-brand"><span class="brand-mark"><Braces :size="20" /></span>Tenon</div>
        <span class="login-form__kicker">欢迎回来</span>
        <h2>登录你的工作区</h2>
        <p class="login-form__intro">使用平台分配的账号继续</p>
        <label class="field-label" for="username">用户名</label>
        <input
          id="username"
          v-model="username"
          class="text-input"
          name="username"
          autocomplete="username"
          maxlength="64"
          placeholder="请输入用户名"
          :disabled="submitting"
          autofocus
        />
        <label class="field-label" for="password">密码</label>
        <div class="password-field">
          <input
            id="password"
            v-model="password"
            class="text-input"
            name="password"
            :type="showPassword ? 'text' : 'password'"
            autocomplete="current-password"
            maxlength="128"
            placeholder="请输入密码"
            :disabled="submitting"
          />
          <button class="icon-button password-field__toggle" type="button" :aria-label="showPassword ? '隐藏密码' : '显示密码'" @click="showPassword = !showPassword">
            <EyeOff v-if="showPassword" :size="18" /><Eye v-else :size="18" />
          </button>
        </div>
        <p v-if="error" class="form-error" role="alert">{{ error }}</p>
        <button class="button button--primary login-form__submit" type="submit" :disabled="submitting || remaining > 0">
          <span v-if="submitting" class="spinner" aria-hidden="true" />
          <span>{{ submitting ? '正在登录…' : remaining ? `${lockText} 后重试` : '登录' }}</span>
          <ArrowRight v-if="!submitting && !remaining" :size="17" />
        </button>
        <p class="login-form__help">登录状态有效期为 7 天。遇到账号问题，请联系平台管理员。</p>
      </form>
    </section>
  </main>
</template>
