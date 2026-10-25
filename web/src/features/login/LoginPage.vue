<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'
import { ArrowRight, Braces, Check, MessageSquareText, Play, Sparkles } from 'lucide-vue-next'
import AuthForm from '@/components/AuthForm.vue'
import { safeInternalRedirect } from '@/utils/navigation'

const route = useRoute()
const router = useRouter()

async function onSuccess(): Promise<void> {
  await router.replace(safeInternalRedirect(route.query.redirect))
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
        <span class="eyebrow"><Sparkles :size="14" /> 从需求到原型，只需一次描述</span>
        <h1>把想法，变成<br /><em>可运行的原型。</em></h1>
        <p>无需搭建环境。描述需求、查看生成过程，立即在浏览器里验证你的产品想法。</p>
        <ul class="login-benefits">
          <li><span><MessageSquareText :size="18" /></span><div><strong>自然语言构建</strong><small>多轮迭代，持续完善同一个项目</small></div><Check :size="16" /></li>
          <li><span><Braces :size="18" /></span><div><strong>代码全程可见</strong><small>只读浏览每一轮真实改动</small></div><Check :size="16" /></li>
          <li><span><Play :size="18" /></span><div><strong>预览即时验证</strong><small>自动构建，保留已确认版本</small></div><Check :size="16" /></li>
        </ul>
      </div>
      <p class="login-story__foot">内部可信试点 · 原型不用于生产部署</p>
    </section>

    <section class="login-form-side">
      <div class="login-form-stack">
        <div class="login-form__mobile-brand"><span class="brand-mark"><Braces :size="20" /></span>Tenon</div>
        <AuthForm @success="onSuccess" />
        <p class="login-form__back">
          <button type="button" @click="router.push('/')">先随便看看，创建项目时再登录 <ArrowRight :size="14" /></button>
        </p>
      </div>
    </section>
  </main>
</template>
