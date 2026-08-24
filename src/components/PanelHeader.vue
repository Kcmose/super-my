<template>
  <header v-if="authStore.isAdmin" class="sticky top-0 z-40 bg-dark-900/80 border-b border-dark-700/80 backdrop-blur-md">
    <div class="max-w-7xl mx-auto px-2 sm:px-6 lg:px-8 h-16 flex items-center justify-between gap-2 sm:gap-4">
      <router-link to="/admin/nodes" class="flex items-center gap-2 sm:gap-3 flex-none" aria-label="返回管理面板首页">
        <div class="w-8 h-8 sm:w-9 sm:h-9 rounded-xl bg-gradient-to-br from-emerald-400 to-emerald-600 flex items-center justify-center text-slate-950 font-black shadow-lg shadow-emerald-500/20">
          <svg class="w-5 h-5 text-slate-950" fill="none" stroke="currentColor" stroke-width="2.5" viewBox="0 0 24 24" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" d="M13 10V3L4 14h7v7l9-11h-7z" />
          </svg>
        </div>
        <div>
          <div class="flex items-center gap-2">
            <span class="theme-heading font-extrabold text-sm sm:text-base tracking-tight text-white">PROBE ADMIN</span>
            <span class="hidden sm:inline-flex px-1.5 py-0.5 rounded text-[10px] font-mono bg-emerald-500/10 text-emerald-400 border border-emerald-500/30">V1.0</span>
          </div>
          <p class="hidden sm:block text-[11px] text-slate-400 font-medium">探针系统管理面板</p>
        </div>
      </router-link>

      <div class="hidden md:flex items-center gap-6 text-xs min-w-0">
        <slot name="summary" />
      </div>

      <div class="flex items-center gap-1.5 sm:gap-2 flex-none">
        <AdminNav />
        <slot name="actions" />
        <ThemeToggle />
        <div class="flex items-center gap-2 pl-2 border-l border-dark-700/70 text-xs">
          <span class="hidden lg:inline max-w-28 truncate text-slate-300 font-medium">{{ authStore.currentUsername }}</span>
          <span class="px-1.5 py-0.5 rounded font-mono text-[10px] bg-amber-500/10 text-amber-400">管理员</span>
        </div>
        <button
          type="button"
          :disabled="logoutLoading"
          class="p-2 text-slate-400 hover:text-rose-400 bg-dark-800 border border-dark-700/60 rounded-lg transition disabled:opacity-50"
          :title="logoutError || '退出登录'"
          aria-label="退出登录"
          @click="handleLogout"
        >
          <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1" />
          </svg>
        </button>
      </div>
    </div>
  </header>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import AdminNav from './AdminNav.vue'
import ThemeToggle from './ThemeToggle.vue'
import { useAuthStore } from '../stores/auth'

const router = useRouter()
const authStore = useAuthStore()
const logoutLoading = ref(false)
const logoutError = ref('')

async function handleLogout() {
  if (logoutLoading.value) return
  logoutLoading.value = true
  logoutError.value = ''
  try {
    await authStore.logout()
    await router.replace({ name: 'Login' })
  } catch (error) {
    logoutError.value = error?.message || '退出失败，请稍后重试'
  } finally {
    logoutLoading.value = false
  }
}
</script>
