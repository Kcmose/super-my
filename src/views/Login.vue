<template>
  <div class="relative min-h-screen flex items-center justify-center p-4 bg-dark-950 text-slate-200">
    <ThemeToggle class="absolute right-4 top-4" />
    <div class="max-w-md w-full card-glass border border-dark-700/80 rounded-xl p-8 shadow-2xl">
      <div class="text-center mb-6">
        <div class="inline-flex items-center justify-center w-14 h-14 bg-emerald-500/10 text-emerald-400 rounded-xl mb-3 border border-emerald-500/30 shadow-lg shadow-emerald-950/20">
          <svg class="w-8 h-8" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z" />
          </svg>
        </div>
        <h1 class="theme-heading text-xl font-extrabold text-white tracking-tight">PROBE ADMIN</h1>
        <p class="text-xs text-slate-400 mt-1">管理面板 · 配置与凭证管理</p>
      </div>

      <div class="mb-6 p-3 bg-dark-900/80 border border-dark-700 rounded-lg text-xs text-slate-300 flex items-start gap-2">
        <span class="font-bold" :class="accessStatus?.allowed === true ? 'text-emerald-400' : accessError ? 'text-rose-400' : 'text-amber-400'">●</span>
        <div>
          <span class="font-medium text-slate-300">本管理面板仅供管理员登录</span>
          <div v-if="accessStatus?.allowed === true" class="text-emerald-400/80 mt-0.5 font-mono text-[11px]">
            来源 IP：{{ accessStatus.source_ip }} · 允许（allowed=true）
          </div>
          <div v-else-if="accessError" class="text-rose-400/80 mt-0.5 font-mono text-[11px]">
            {{ accessError }}
          </div>
          <div v-else class="text-slate-500 mt-0.5 font-mono text-[11px]">正在由服务端确认来源 IP 与白名单状态...</div>
        </div>
      </div>

      <form class="space-y-4" @submit.prevent="handleLogin">
        <div>
          <label for="username" class="block text-xs font-medium text-slate-300 mb-1">管理员账号</label>
          <input id="username" v-model.trim="username" name="username" type="text" autocomplete="username" required maxlength="128" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-emerald-500 transition" placeholder="请输入用户名" />
        </div>
        <div>
          <label for="password" class="block text-xs font-medium text-slate-300 mb-1">登录密码</label>
          <input id="password" v-model="password" name="password" type="password" autocomplete="current-password" required maxlength="1024" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-emerald-500 transition" placeholder="请输入密码" />
        </div>

        <div v-if="message" role="alert" class="p-2.5 bg-rose-500/10 border border-rose-500/30 rounded-lg text-xs text-rose-400">
          {{ message }}
          <span v-if="requestId" class="block mt-1 text-[10px] text-rose-300/70 font-mono">请求 ID: {{ requestId }}</span>
        </div>

        <button type="submit" :disabled="loading" class="w-full py-2.5 bg-emerald-600 hover:bg-emerald-500 font-medium text-sm text-white rounded-lg transition shadow-lg shadow-emerald-900/30 flex items-center justify-center gap-2 disabled:opacity-60 disabled:cursor-wait">
          <span v-if="loading" class="w-2 h-2 rounded-full bg-white animate-pulse"></span>
          <span>{{ loading ? '验证登录中...' : '管 理 员 登 录' }}</span>
        </button>
      </form>

      <div class="mt-6 pt-4 border-t border-dark-700/60 text-[11px] text-slate-500 text-center">
        无 WebSSH • 无远程命令 • 5分钟短期指标 • 90天聚合探测
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { authApi } from '../api/auth'
import ThemeToggle from '../components/ThemeToggle.vue'
import { useAuthStore } from '../stores/auth'

const username = ref('')
const password = ref('')
const errorMessage = ref('')
const requestId = ref('')
const loading = ref(false)
const accessStatus = ref(null)
const accessError = ref('')
const authStore = useAuthStore()
const route = useRoute()
const router = useRouter()
let accessController = null

const message = computed(() => {
  if (errorMessage.value) return errorMessage.value
  if (route.query.reason === 'expired') return '管理员会话已过期，请重新登录'
  return authStore.startupError
})

function safeRedirect() {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : ''
  if (redirect.startsWith('/') && !redirect.startsWith('//')) {
    const resolved = router.resolve(redirect)
    if (resolved.matched.some((record) => record.meta.requiresAdmin === true)) return redirect
  }
  return '/admin/nodes'
}

async function handleLogin() {
  if (loading.value) return
  loading.value = true
  errorMessage.value = ''
  requestId.value = ''
  try {
    await authStore.login(username.value, password.value)
    password.value = ''
    await router.replace(safeRedirect())
  } catch (error) {
    password.value = ''
    errorMessage.value = error?.message || '管理员登录失败，请稍后重试'
    requestId.value = error?.requestId || ''
  } finally {
    loading.value = false
  }
}

async function loadAccessStatus() {
  accessController?.abort()
  accessController = new AbortController()
  accessStatus.value = null
  accessError.value = ''
  try {
    const response = await authApi.getAccessStatus({ signal: accessController.signal })
    if (response?.allowed !== true || typeof response?.source_ip !== 'string' || !response.source_ip) {
      throw new Error('服务端未返回有效的来源校验结果')
    }
    accessStatus.value = { source_ip: response.source_ip, allowed: true }
  } catch (error) {
    if (error?.name === 'AbortError') return
    accessError.value = '暂时无法读取服务端来源校验状态'
  }
}

onMounted(() => void loadAccessStatus())
onUnmounted(() => accessController?.abort())
</script>
