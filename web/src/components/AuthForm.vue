<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowRight, Eye, EyeOff } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { ApiError, errorMessage } from '@/api/errors'
import type { AuthTab } from '@/stores/ui'

const props = withDefaults(defineProps<{ initialTab?: AuthTab }>(), { initialTab: 'login' })
const emit = defineEmits<{ success: [tab: AuthTab] }>()

const auth = useAuthStore()
const tab = ref<AuthTab>(props.initialTab)
const username = ref('')
const displayName = ref('')
const password = ref('')
const confirm = ref('')
const showPassword = ref(false)
const validationError = ref('')
const lockError = ref(false)
const registerLockUntil = ref(0)
const now = ref(Date.now())
const usernameInput = ref<HTMLInputElement>()
let timer: number | undefined

const submitting = computed(() => auth.loading)
const loginRemaining = computed(() => Math.max(0, Math.ceil((auth.lockedUntil - now.value) / 1000)))
const registerRemaining = computed(() => Math.max(0, Math.ceil((registerLockUntil.value - now.value) / 1000)))
const remaining = computed(() => (tab.value === 'login' ? loginRemaining.value : registerRemaining.value))
const locked = computed(() => remaining.value > 0)
const lockText = computed(() => formatLock(remaining.value))
const error = computed(() => {
  if (lockError.value) return locked.value ? `尝试次数过多，请 ${lockText.value} 后再试` : ''
  return validationError.value
})
const kicker = computed(() => (tab.value === 'login' ? '欢迎回来' : '创建账号'))
const heading = computed(() => (tab.value === 'login' ? '登录你的工作区' : '注册你的工作区'))
const intro = computed(() => (tab.value === 'login' ? '使用已有账号登录，或切换到注册' : '注册后即可创建项目，账号仅用于当前平台'))
const submitText = computed(() => (tab.value === 'login' ? '登录' : '注册并登录'))
const busyText = computed(() => (tab.value === 'login' ? '正在登录…' : '正在注册…'))
const helpText = computed(() => (tab.value === 'login'
  ? '登录状态有效期为 7 天。遇到账号问题，请联系平台管理员。'
  : '密码 8–128 个字符；注册成功后会自动登录。'))

watch(() => props.initialTab, (value) => { if (value !== tab.value) switchTab(value) })
onMounted(() => {
  timer = window.setInterval(() => { now.value = Date.now() }, 1000)
  usernameInput.value?.focus()
})
onBeforeUnmount(() => { if (timer) window.clearInterval(timer) })

function formatLock(seconds: number): string {
  if (!seconds) return ''
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  return minutes ? `${minutes} 分 ${rest} 秒` : `${rest} 秒`
}

function switchTab(next: AuthTab): void {
  if (tab.value === next) return
  tab.value = next
  password.value = ''
  confirm.value = ''
  validationError.value = ''
  lockError.value = false
}

function validateLogin(): string {
  const name = username.value.trim()
  if (!name || name.length > 64) return '请输入 1–64 个字符的用户名'
  if (password.value.length < 8 || password.value.length > 128) return '密码需为 8–128 个字符'
  return ''
}

function validateRegister(): string {
  const name = username.value.trim()
  if (!name || name.length > 64) return '请输入 1–64 个字符的用户名'
  if (!/^[A-Za-z0-9_.-]+$/.test(name)) return '用户名只能包含字母、数字、下划线、点或短横线'
  if (displayName.value.trim().length > 64) return '昵称不能超过 64 个字符'
  if (password.value.length < 8 || password.value.length > 128) return '密码需为 8–128 个字符'
  if (password.value !== confirm.value) return '两次输入的密码不一致'
  return ''
}

async function submit(): Promise<void> {
  if (submitting.value || locked.value) return
  validationError.value = ''
  lockError.value = false
  const message = tab.value === 'login' ? validateLogin() : validateRegister()
  if (message) {
    validationError.value = message
    return
  }
  try {
    if (tab.value === 'login') await auth.login(username.value.trim(), password.value)
    else await auth.register(username.value.trim(), password.value, displayName.value.trim())
    emit('success', tab.value)
  } catch (reason) {
    if (tab.value === 'login') {
      validationError.value = errorMessage(reason, 'login')
      lockError.value = loginRemaining.value > 0
      return
    }
    validationError.value = errorMessage(reason)
    if (reason instanceof ApiError && reason.status === 429) {
      registerLockUntil.value = Date.now() + (reason.retryAfter ?? 10 * 60) * 1000
      lockError.value = registerRemaining.value > 0
    }
  }
}
</script>

<template>
  <form class="login-form" novalidate @submit.prevent="submit">
    <div class="auth-tabs" role="tablist" aria-label="登录或注册">
      <button type="button" role="tab" :aria-selected="tab === 'login'" :class="{ 'active': tab === 'login' }" @click="switchTab('login')">登录</button>
      <button type="button" role="tab" :aria-selected="tab === 'register'" :class="{ 'active': tab === 'register' }" @click="switchTab('register')">注册</button>
    </div>
    <span class="login-form__kicker">{{ kicker }}</span>
    <h2>{{ heading }}</h2>
    <p class="login-form__intro">{{ intro }}</p>
    <label class="field-label" for="username">用户名</label>
    <input
      id="username"
      ref="usernameInput"
      v-model="username"
      class="text-input"
      name="username"
      autocomplete="username"
      maxlength="64"
      placeholder="请输入用户名"
      :disabled="submitting"
    />
    <template v-if="tab === 'register'">
      <label class="field-label" for="display-name">昵称（可选）</label>
      <input
        id="display-name"
        v-model="displayName"
        class="text-input"
        name="display-name"
        autocomplete="nickname"
        maxlength="64"
        placeholder="留空则使用用户名"
        :disabled="submitting"
      />
    </template>
    <label class="field-label" for="password">密码</label>
    <div class="password-field">
      <input
        id="password"
        v-model="password"
        class="text-input"
        name="password"
        :type="showPassword ? 'text' : 'password'"
        :autocomplete="tab === 'login' ? 'current-password' : 'new-password'"
        maxlength="128"
        placeholder="请输入密码"
        :disabled="submitting"
      />
      <button class="icon-button password-field__toggle" type="button" :aria-label="showPassword ? '隐藏密码' : '显示密码'" @click="showPassword = !showPassword">
        <EyeOff v-if="showPassword" :size="18" /><Eye v-else :size="18" />
      </button>
    </div>
    <template v-if="tab === 'register'">
      <label class="field-label" for="confirm-password">确认密码</label>
      <input
        id="confirm-password"
        v-model="confirm"
        class="text-input"
        name="confirm-password"
        :type="showPassword ? 'text' : 'password'"
        autocomplete="new-password"
        maxlength="128"
        placeholder="请再次输入密码"
        :disabled="submitting"
      />
    </template>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <button class="button button--primary login-form__submit" type="submit" :disabled="submitting || locked">
      <span v-if="submitting" class="spinner" aria-hidden="true" />
      <span>{{ submitting ? busyText : locked ? `${lockText} 后重试` : submitText }}</span>
      <ArrowRight v-if="!submitting && !locked" :size="17" />
    </button>
    <p class="login-form__help">{{ helpText }}</p>
  </form>
</template>
