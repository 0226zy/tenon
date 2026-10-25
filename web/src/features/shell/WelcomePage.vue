<script setup lang="ts">
import { ArrowRight, Braces, Sparkles } from 'lucide-vue-next'
import ExampleCards from '@/components/ExampleCards.vue'
import type { ExamplePrompt } from '@/constants/examples'

const emit = defineEmits<{ select: [example: ExamplePrompt]; create: [] }>()
</script>

<template>
  <section class="welcome-page">
    <div class="welcome-page__glow" aria-hidden="true" />
    <div class="welcome-page__content">
      <span class="welcome-page__mark"><Braces :size="28" /></span>
      <span class="eyebrow"><Sparkles :size="14" /> AI 原型工作区</span>
      <h1>今天想构建什么？</h1>
      <p>描述你的产品想法，Agent 会生成真实代码与可运行预览。<br />每一轮修改，都有迹可循。</p>
      <ExampleCards @select="emit('select', $event)" />
      <button class="welcome-page__blank" type="button" @click="emit('create')">从空白项目开始 <ArrowRight :size="15" /></button>
    </div>
  </section>
</template>
