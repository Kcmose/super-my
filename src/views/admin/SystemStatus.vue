<template>
  <div class="min-h-screen flex flex-col bg-dark-950 text-slate-200 custom-scrollbar">
    <PanelHeader>
      <template #summary>
        <div class="flex items-center gap-2 text-slate-400 font-mono text-[11px]">
          <span class="w-1.5 h-1.5 rounded-full" :class="headerState.tone === 'ready' ? 'bg-emerald-400' : headerState.tone === 'error' ? 'bg-rose-400' : 'bg-amber-400'"></span>
          <span>{{ headerState.label }}</span>
          <span class="text-slate-600">•</span>
          <span>仅管理员可读</span>
        </div>
      </template>
      <template #actions>
        <router-link to="/admin/nodes" class="px-3 py-1.5 bg-dark-800 border border-dark-700/60 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition">管理首页</router-link>
      </template>
    </PanelHeader>

    <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6 flex-1 w-full space-y-6">
      <section class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl flex flex-col sm:flex-row sm:items-center justify-between gap-4 shadow-lg">
        <div>
          <h1 class="text-sm font-semibold text-slate-100">系统只读状态</h1>
          <p class="text-xs text-slate-400 mt-1">显示 API、数据库就绪状态与进程内安全契约，不提供服务控制、环境变量或凭据信息。</p>
        </div>
        <button type="button" :disabled="loading" class="px-3 py-2 bg-dark-800 border border-dark-700 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition disabled:opacity-50" @click="loadStatus">
          {{ loading ? '读取中...' : '刷新状态' }}
        </button>
      </section>

      <div v-if="errorMessage" role="alert" class="p-3 bg-rose-500/10 border border-rose-500/30 rounded-xl text-xs text-rose-300">{{ errorMessage }}</div>

      <section class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <article class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl shadow-lg">
          <div class="text-[10px] uppercase tracking-wider text-slate-500">总体状态</div>
          <div class="mt-3 flex items-center gap-2">
            <span class="w-2 h-2 rounded-full" :class="overallTone === 'ready' ? 'bg-emerald-400' : overallTone === 'error' ? 'bg-rose-400' : 'bg-amber-400'"></span>
            <span class="text-sm font-semibold" :class="overallTone === 'ready' ? 'text-emerald-400' : overallTone === 'error' ? 'text-rose-400' : 'text-amber-400'">{{ overallLabel }}</span>
          </div>
          <div class="mt-2 text-[11px] text-slate-500 font-mono">检查时间：{{ checkedAt }}</div>
        </article>
        <article class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl shadow-lg">
          <div class="text-[10px] uppercase tracking-wider text-slate-500">API</div>
          <div class="mt-3 flex items-center justify-between gap-3">
            <span class="text-sm font-semibold text-emerald-400">{{ status?.api?.status === 'ready' ? '就绪' : '未知' }}</span>
            <span class="px-2 py-0.5 rounded text-[10px] font-mono bg-sky-500/10 text-sky-400 border border-sky-500/20">{{ status?.api?.version || '—' }}</span>
          </div>
          <div class="mt-2 text-[11px] text-slate-500">仅提供 V1 已冻结 API 契约。</div>
        </article>
        <article class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl shadow-lg">
          <div class="text-[10px] uppercase tracking-wider text-slate-500">PostgreSQL</div>
          <div class="mt-3 text-sm font-semibold" :class="status?.database?.status === 'ready' ? 'text-emerald-400' : !status && errorMessage ? 'text-slate-400' : 'text-amber-400'">{{ databaseLabel }}</div>
          <div class="mt-2 text-[11px] text-slate-500">只展示认证成功后的连接与迁移检查；认证前数据库故障会由认证层返回通用失败。</div>
        </article>
        <article class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl shadow-lg">
          <div class="text-[10px] uppercase tracking-wider text-slate-500">Agent 接入</div>
          <div class="mt-3 text-sm font-semibold" :class="status?.agent?.status === 'configured' ? 'text-emerald-400' : 'text-amber-400'">{{ agentLabel }}</div>
          <div class="mt-2 text-[11px] text-slate-500">未配置时管理面板仍可使用并可预设节点参数，但 Agent 路由、安装命令和令牌轮换保持禁用。</div>
        </article>
      </section>

      <section class="bg-dark-900 border border-dark-700/80 rounded-xl overflow-hidden shadow-lg">
        <div class="px-4 py-3 border-b border-dark-700/80">
          <h2 class="text-sm font-semibold text-slate-100">安全边界</h2>
          <p class="text-[11px] text-slate-500 mt-1">以下状态仅描述 API 进程可强制或定义的契约，不代表对 Nginx 入口拓扑的运行时验证。</p>
        </div>
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="item in boundaryItems" :key="item.label" class="p-4 border-b sm:border-r border-dark-700/60 last:border-b-0">
            <div class="flex items-center justify-between gap-3">
              <span class="text-xs text-slate-300">{{ item.label }}</span>
              <span class="px-2 py-0.5 rounded text-[10px] border" :class="item.healthy ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20' : 'bg-rose-500/10 text-rose-400 border-rose-500/20'">{{ item.value }}</span>
            </div>
          </div>
        </div>
      </section>
    </main>

    <PanelFooter />
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { adminApi } from '../../api/admin'
import PanelFooter from '../../components/PanelFooter.vue'
import PanelHeader from '../../components/PanelHeader.vue'
import { formatUTCDateTime } from '../../utils/admin'
import { systemDatabaseLabel, systemHeaderState, systemOverallLabel, systemOverallTone } from '../../utils/systemStatus'

const status = ref(null)
const loading = ref(true)
const errorMessage = ref('')
let requestController = null
let mounted = false

const headerState = computed(() => systemHeaderState(status.value, loading.value, errorMessage.value))
const overallLabel = computed(() => systemOverallLabel(status.value, loading.value, errorMessage.value))
const overallTone = computed(() => systemOverallTone(status.value, errorMessage.value))
const databaseLabel = computed(() => systemDatabaseLabel(status.value, loading.value, errorMessage.value))
const agentLabel = computed(() => {
  if (status.value?.agent?.status === 'configured') return '已配置'
  if (status.value?.agent?.status === 'not_configured') return '待独立配置'
  return loading.value ? '读取中' : '未知'
})
const checkedAt = computed(() => formatUTCDateTime(status.value?.checked_at))
const boundaryItems = computed(() => {
  const boundary = status.value?.security_boundary || {}
  return [
    { label: 'API 管理 IP/CIDR 白名单', value: boundary.management_ip_allowlist_enforced === true ? '已强制' : '异常', healthy: boundary.management_ip_allowlist_enforced === true },
    { label: '管理员 Session', value: boundary.administrator_session_required === true ? '必需' : '异常', healthy: boundary.administrator_session_required === true },
    { label: '管理写请求 CSRF', value: boundary.admin_write_csrf_required === true ? '必需' : '异常', healthy: boundary.admin_write_csrf_required === true },
    { label: '远程操作能力', value: boundary.remote_operations_enabled === false ? '已禁用' : '已开启', healthy: boundary.remote_operations_enabled === false },
  ]
})

async function loadStatus() {
  requestController?.abort()
  requestController = new AbortController()
  loading.value = true
  errorMessage.value = ''
  try {
    const response = await adminApi.getSystemStatus({ signal: requestController.signal })
    if (!response || !['ready', 'degraded'].includes(response.status) || response?.api?.version !== 'v1') {
      throw new Error('服务端返回的系统状态格式无效')
    }
    if (mounted) status.value = response
  } catch (error) {
    if (error?.name === 'AbortError') return
    if (mounted) errorMessage.value = error?.message || '系统状态读取失败'
  } finally {
    if (mounted) loading.value = false
  }
}

onMounted(() => {
  mounted = true
  void loadStatus()
})
onUnmounted(() => {
  mounted = false
  requestController?.abort()
})
</script>
