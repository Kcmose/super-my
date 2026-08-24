<template>
  <details v-if="authStore.isAdmin" ref="menu" class="relative">
    <summary
      class="list-none cursor-pointer p-2 sm:px-2.5 sm:py-1.5 bg-dark-800 border border-dark-700/60 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition select-none flex items-center gap-1"
      aria-label="打开管理面板导航"
      @keydown.esc.prevent="closeMenu"
    >
      <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 6V4m0 16v-2m6-6h2M4 12h2m10.24-4.24 1.42-1.42M6.34 17.66l1.42-1.42m8.48 0 1.42 1.42M6.34 6.34l1.42 1.42M15 12a3 3 0 11-6 0 3 3 0 016 0z" /></svg>
      <span class="hidden sm:inline">管理面板</span><span class="hidden sm:inline text-[10px] text-slate-500" aria-hidden="true">▾</span>
    </summary>
    <nav class="absolute right-0 top-full mt-2 w-44 p-1.5 bg-dark-850 border border-dark-700 rounded-xl shadow-2xl z-50" aria-label="管理面板">
      <router-link
        v-for="item in items"
        :key="item.to"
        :to="item.to"
        class="block px-3 py-2 rounded-lg text-xs transition"
        :class="isActive(item.to) ? 'bg-emerald-500/10 text-emerald-400 font-semibold' : 'text-slate-300 hover:bg-dark-800 hover:text-white'"
        @click="closeMenu"
      >
        {{ item.label }}
      </router-link>
    </nav>
  </details>
</template>

<script setup>
import { ref } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const authStore = useAuthStore()
const route = useRoute()
const menu = ref(null)

const items = [
  { to: '/admin/probes', label: '探测目标配置' },
  { to: '/admin/nodes', label: '节点与凭证' },
  { to: '/admin/users', label: '管理员账号' },
  { to: '/admin/audit', label: '审计日志' },
  { to: '/admin/system', label: '系统只读状态' },
]

function isActive(path) {
  return route.path === path || route.path.startsWith(`${path}/`)
}

function closeMenu() {
  if (menu.value) menu.value.open = false
}
</script>
