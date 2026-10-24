<script setup lang="ts">
import { BarChart3, Boxes, CalendarDays, ArrowUpRight } from 'lucide-vue-next'
import { EXAMPLE_PROMPTS, type ExamplePrompt } from '@/constants/examples'

const emit = defineEmits<{ select: [example: ExamplePrompt] }>()
const icons = { chart: BarChart3, inventory: Boxes, calendar: CalendarDays }
</script>

<template>
  <div class="example-grid">
    <button
      v-for="example in EXAMPLE_PROMPTS"
      :key="example.title"
      class="example-card"
      type="button"
      @click="emit('select', example)"
    >
      <span class="example-card__top">
        <span class="example-card__icon"><component :is="icons[example.icon]" :size="19" /></span>
        <ArrowUpRight class="example-card__arrow" :size="17" />
      </span>
      <strong>{{ example.title }}</strong>
      <span>{{ example.description }}</span>
    </button>
  </div>
</template>
