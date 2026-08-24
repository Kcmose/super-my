<template>
  <div class="min-h-screen flex flex-col bg-dark-950 text-slate-200 custom-scrollbar">
    <PanelHeader>
      <template #summary>
        <div class="flex items-center gap-2 text-slate-400 font-mono text-[11px]">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>
          <span>{{ logs.length }} 条已加载记录</span>
          <span class="text-slate-600">•</span>
          <span>时间统一显示为 UTC</span>
        </div>
      </template>
      <template #actions>
        <router-link to="/admin/nodes" class="px-3 py-1.5 bg-dark-800 border border-dark-700/60 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition">管理首页</router-link>
      </template>
    </PanelHeader>

    <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6 flex-1 w-full space-y-6">
      <section class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl shadow-lg">
        <div>
          <h1 class="text-sm font-semibold text-slate-100">管理操作审计日志</h1>
          <p class="text-xs text-slate-400 mt-1">查询登录、用户、节点、凭证及探测配置等真实审计事件。审计内容只按文本显示。</p>
        </div>
        <form class="mt-4 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-[1fr_1fr_1fr_auto] gap-3 items-end text-xs" @submit.prevent="loadLogs()">
          <div>
            <label for="audit-action" class="block text-slate-400 mb-1">操作类型</label>
            <input id="audit-action" v-model.trim="filters.action" maxlength="128" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500" placeholder="例如：user.update">
          </div>
          <div>
            <label for="audit-from" class="block text-slate-400 mb-1">开始时间（本地输入）</label>
            <input id="audit-from" v-model="filters.from" type="datetime-local" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
          </div>
          <div>
            <label for="audit-to" class="block text-slate-400 mb-1">结束时间（本地输入）</label>
            <input id="audit-to" v-model="filters.to" type="datetime-local" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
          </div>
          <div class="flex gap-2">
            <button type="button" :disabled="loading || loadingMore" class="px-3 py-2 bg-dark-800 border border-dark-700 text-slate-300 hover:text-white rounded-lg disabled:opacity-50" @click="resetFilters">重置</button>
            <button type="submit" :disabled="loading || loadingMore" class="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 text-white rounded-lg font-medium disabled:opacity-50">查询</button>
          </div>
        </form>
      </section>

      <div v-if="errorMessage" role="alert" class="p-3 bg-rose-500/10 border border-rose-500/30 rounded-xl text-xs text-rose-300">{{ errorMessage }}</div>

      <section class="bg-dark-900 border border-dark-700/80 rounded-xl overflow-hidden shadow-lg">
        <div class="overflow-x-auto custom-scrollbar">
          <table class="w-full min-w-[1120px] text-left text-xs text-slate-300">
            <thead class="bg-dark-950 text-slate-400 uppercase font-semibold border-b border-dark-700/80">
              <tr>
                <th class="py-3 px-4">时间 (UTC)</th>
                <th class="py-3 px-4">操作者</th>
                <th class="py-3 px-4">操作类型</th>
                <th class="py-3 px-4">目标对象</th>
                <th class="py-3 px-4">来源 IP</th>
                <th class="py-3 px-4">结果</th>
                <th class="py-3 px-4 text-right">详情</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-dark-700/60 font-mono text-[11px]">
              <tr v-if="loading"><td colspan="7" class="py-14 px-4 text-center text-slate-500">正在读取真实审计日志...</td></tr>
              <tr v-else-if="logs.length === 0"><td colspan="7" class="py-14 px-4 text-center text-slate-500">当前筛选范围内没有审计记录</td></tr>
              <template v-else>
              <template v-for="log in logs" :key="log.audit_id">
                <tr class="hover:bg-dark-800/50 transition">
                  <td class="py-3 px-4 text-slate-400 whitespace-nowrap">{{ formatUTCDateTime(log.occurred_at) }}</td>
                  <td class="py-3 px-4 font-sans">
                    <div class="font-medium text-slate-200">{{ log.actor_username || '—' }}</div>
                    <div class="text-[10px] text-slate-500">{{ log.actor_user_id || '—' }}</div>
                  </td>
                  <td class="py-3 px-4 font-bold text-sky-400">{{ log.action }}</td>
                  <td class="py-3 px-4 text-slate-300"><div>{{ log.target_type || '—' }}</div><div class="text-[10px] text-slate-500">{{ log.target_id || '—' }}</div></td>
                  <td class="py-3 px-4 text-slate-400">{{ log.source_ip || '—' }}</td>
                  <td class="py-3 px-4">
                    <span class="px-2 py-0.5 rounded text-[10px] border" :class="log.result === 'success' ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20' : 'bg-rose-500/10 text-rose-400 border-rose-500/20'">{{ log.result === 'success' ? '成功' : '失败' }}</span>
                    <div v-if="log.error_code" class="mt-1 text-[10px] text-rose-300">{{ log.error_code }}</div>
                  </td>
                  <td class="py-3 px-4 text-right"><button type="button" class="text-slate-300 hover:text-emerald-400" :aria-expanded="expandedIds.has(log.audit_id)" @click="toggleDetails(log.audit_id)">{{ expandedIds.has(log.audit_id) ? '收起' : '查看' }}</button></td>
                </tr>
                <tr v-if="expandedIds.has(log.audit_id)" class="bg-dark-950/70">
                  <td colspan="7" class="p-4">
                    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
                      <div><div class="text-[10px] uppercase text-slate-500 mb-1">变更前摘要</div><pre class="p-3 bg-dark-900 border border-dark-700 rounded-lg text-[11px] text-slate-300 whitespace-pre-wrap break-words">{{ auditSummaryText(log.before_summary) }}</pre></div>
                      <div><div class="text-[10px] uppercase text-slate-500 mb-1">变更后摘要</div><pre class="p-3 bg-dark-900 border border-dark-700 rounded-lg text-[11px] text-slate-300 whitespace-pre-wrap break-words">{{ auditSummaryText(log.after_summary) }}</pre></div>
                    </div>
                    <div class="mt-3 text-[10px] text-slate-500">请求 ID：<span class="text-slate-300 select-all">{{ log.request_id }}</span></div>
                  </td>
                </tr>
              </template>
              </template>
            </tbody>
          </table>
        </div>
        <div v-if="nextCursor" class="p-3 border-t border-dark-700/80 flex justify-center">
          <button type="button" :disabled="loadingMore" class="px-4 py-2 bg-dark-800 border border-dark-700 text-slate-300 hover:text-emerald-400 rounded-lg text-xs disabled:opacity-50" @click="loadLogs({ append: true })">{{ loadingMore ? '加载中...' : '加载更多审计记录' }}</button>
        </div>
      </section>
    </main>

    <PanelFooter />
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import PanelFooter from '../../components/PanelFooter.vue'
import PanelHeader from '../../components/PanelHeader.vue'
import { adminApi } from '../../api/admin'
import { MAX_ADMIN_PAGES, auditSummaryText, formatUTCDateTime, validateAuditWindow } from '../../utils/admin'

const filters = ref({ action: '', from: '', to: '' })
const activeFilters = ref({ action: undefined, from: undefined, to: undefined })
const logs = ref([])
const loading = ref(true)
const loadingMore = ref(false)
const nextCursor = ref(null)
const errorMessage = ref('')
const expandedIds = ref(new Set())

let mounted = false
let requestGeneration = 0
let requestController = null
let pageCount = 0
let seenCursors = new Set()

async function loadLogs({ append = false } = {}) {
  if (append && (!nextCursor.value || loadingMore.value)) return
  if (!append) {
    const validatedWindow = validateAuditWindow(filters.value.from, filters.value.to)
    if (validatedWindow.error) {
      errorMessage.value = validatedWindow.error
      loading.value = false
      return
    }
    activeFilters.value = {
      action: String(filters.value.action || '').trim() || undefined,
      from: validatedWindow.from,
      to: validatedWindow.to,
    }
  }

  const generation = ++requestGeneration
  requestController?.abort()
  requestController = new AbortController()
  if (append) loadingMore.value = true
  else {
    loading.value = true
    pageCount = 0
    seenCursors = new Set()
    expandedIds.value = new Set()
  }
  errorMessage.value = ''

  try {
    const cursor = append ? nextCursor.value : undefined
    const response = await adminApi.getAuditLogs({
      limit: 50,
      cursor,
      ...activeFilters.value,
      signal: requestController.signal,
    })
    if (!mounted || generation !== requestGeneration) return

    const loadedLogs = Array.isArray(response?.logs) ? response.logs : []
    const following = response?.next_cursor || null
    if (following && seenCursors.has(following)) throw new Error('服务端返回了重复分页游标')
    if (following && pageCount + 1 >= MAX_ADMIN_PAGES) throw new Error(`审计分页超过安全上限 ${MAX_ADMIN_PAGES}`)
    logs.value = append ? [...logs.value, ...loadedLogs] : loadedLogs
    pageCount += 1
    if (following) seenCursors.add(following)
    nextCursor.value = following
  } catch (error) {
    if (error?.name === 'AbortError' || generation !== requestGeneration) return
    errorMessage.value = error?.message || '审计日志读取失败'
  } finally {
    if (mounted && generation === requestGeneration) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

function resetFilters() {
  filters.value = { action: '', from: '', to: '' }
  void loadLogs()
}

function toggleDetails(auditId) {
  const next = new Set(expandedIds.value)
  if (next.has(auditId)) next.delete(auditId)
  else next.add(auditId)
  expandedIds.value = next
}

onMounted(() => {
  mounted = true
  void loadLogs()
})

onUnmounted(() => {
  mounted = false
  requestGeneration += 1
  requestController?.abort()
  expandedIds.value = new Set()
})
</script>
