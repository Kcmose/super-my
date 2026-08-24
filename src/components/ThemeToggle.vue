<template>
  <button
    type="button"
    class="inline-flex items-center justify-center gap-1.5 px-2 py-1.5 bg-dark-800 border border-dark-700/60 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition"
    :aria-label="actionLabel"
    :title="actionLabel"
    @click="handleToggle"
  >
    <svg v-if="theme === 'dark'" class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.36 6.36-.7-.7M6.34 6.34l-.7-.7m12.72 0-.7.7M6.34 17.66l-.7.7M16 12a4 4 0 11-8 0 4 4 0 018 0z" />
    </svg>
    <svg v-else class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20.35 15.35A9 9 0 118.65 3.65a7 7 0 0011.7 11.7z" />
    </svg>
    <span class="hidden sm:inline">{{ destinationLabel }}</span>
  </button>
</template>

<script setup>
import { computed, ref } from 'vue'
import { appliedTheme, toggleTheme } from '../utils/theme'

const theme = ref(appliedTheme())
const destinationLabel = computed(() => theme.value === 'dark' ? '浅色' : '深色')
const actionLabel = computed(() => `切换到${destinationLabel.value}模式`)

function handleToggle() {
  theme.value = toggleTheme()
}
</script>
