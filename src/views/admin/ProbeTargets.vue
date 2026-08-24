<template>
  <div class="min-h-screen flex flex-col bg-dark-950 text-slate-200 custom-scrollbar">
    <PanelHeader>
      <template #summary>
        <div class="flex items-center gap-2 text-slate-400 font-mono text-[11px]">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>
          <span>{{ targets.length }} 个 TCP / HTTP(S) 目标</span>
          <span class="text-slate-600">•</span>
          <span>ICMP 暂缓</span>
        </div>
      </template>
      <template #actions>
        <router-link to="/admin/nodes" class="px-3 py-1.5 bg-dark-800 border border-dark-700/60 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition">
          管理首页
        </router-link>
      </template>
    </PanelHeader>

    <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6 flex-1 w-full space-y-6">
      <section class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl flex flex-col sm:flex-row sm:items-center justify-between gap-4 shadow-lg">
        <div>
          <h1 class="text-sm font-semibold text-slate-100">探测目标配置管理</h1>
          <p class="text-xs text-slate-400 mt-1">本轮开放 TCP 端口检查与 HTTP/HTTPS 状态监测；每个目标最长保留 90 天。ICMP 不创建、不下发。</p>
        </div>
        <div class="flex items-center gap-2">
          <button type="button" :disabled="loading" class="px-3 py-2 bg-dark-800 border border-dark-700 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition disabled:opacity-50" @click="loadData">
            刷新
          </button>
          <button type="button" :disabled="loading || nodes.length === 0" class="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 text-white rounded-lg text-xs font-medium transition disabled:opacity-50" :title="nodes.length ? '新建探测目标' : '请先创建节点'" @click="openCreate">
            + 新建探测目标
          </button>
        </div>
      </section>

      <div v-if="errorMessage" role="alert" class="p-3 bg-rose-500/10 border border-rose-500/30 rounded-xl text-xs text-rose-300">{{ errorMessage }}</div>
      <div v-if="successMessage" role="status" class="p-3 bg-emerald-500/10 border border-emerald-500/30 rounded-xl text-xs text-emerald-300">{{ successMessage }}</div>

      <section class="bg-dark-900 border border-dark-700/80 rounded-xl overflow-hidden shadow-lg">
        <div class="overflow-x-auto custom-scrollbar">
          <table class="w-full min-w-[980px] text-left text-xs text-slate-300">
            <thead class="bg-dark-950 text-slate-400 uppercase font-semibold border-b border-dark-700/80">
              <tr>
                <th class="py-3 px-4">状态</th>
                <th class="py-3 px-4">目标名称 / 节点</th>
                <th class="py-3 px-4">探测类型</th>
                <th class="py-3 px-4">目标地址 / 端口</th>
                <th class="py-3 px-4">周期 / 超时</th>
                <th class="py-3 px-4">保留期限 (≤90天)</th>
                <th class="py-3 px-4 text-right">操作</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-dark-700/60 font-mono">
              <tr v-if="loading">
                <td colspan="7" class="py-14 px-4 text-center text-slate-500">正在读取真实探测目标...</td>
              </tr>
              <tr v-else-if="targets.length === 0">
                <td colspan="7" class="py-14 px-4 text-center">
                  <div class="text-slate-300 font-sans font-semibold">暂无探测目标</div>
                  <div class="mt-1 text-[11px] text-slate-500 font-sans">创建 TCP、HTTP 或 HTTPS 目标后，Agent 会通过配置刷新获取任务。</div>
                </td>
              </tr>
              <template v-else>
                <tr v-for="target in targets" :key="target.target_id" class="hover:bg-dark-800/50 transition">
                  <td class="py-3 px-4">
                    <span class="px-2 py-0.5 rounded text-[10px] border" :class="target.enabled ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20' : 'bg-slate-700/50 text-slate-400 border-slate-600/50'">
                      {{ target.enabled ? '已启用' : '已停用' }}
                    </span>
                  </td>
                  <td class="py-3 px-4">
                    <div class="font-sans font-medium text-slate-200">{{ target.name }}</div>
                    <div class="text-[10px] text-slate-500 mt-0.5">{{ nodeLabelById(target.node_id) }}</div>
                  </td>
                  <td class="py-3 px-4">
                    <span class="px-1.5 py-0.5 rounded text-[10px] font-bold bg-sky-500/20 text-sky-400">{{ probeTypeLabel(target.type) }}</span>
                  </td>
                  <td class="py-3 px-4 max-w-xs truncate" :title="formatTargetAddress(target)">{{ formatTargetAddress(target) }}</td>
                  <td class="py-3 px-4">{{ target.interval_seconds }}s / {{ target.timeout_seconds }}s</td>
                  <td class="py-3 px-4 text-emerald-400 font-semibold">{{ formatRetention(target.retention_seconds) }}</td>
                  <td class="py-3 px-4 text-right font-sans whitespace-nowrap">
                    <button type="button" :disabled="busyTargetId === target.target_id" class="text-slate-300 hover:text-emerald-400 disabled:opacity-40" @click="openEdit(target)">编辑</button>
                    <button type="button" :disabled="busyTargetId === target.target_id" class="ml-3 text-amber-400 hover:text-amber-300 disabled:opacity-40" @click="toggleTarget(target)">{{ target.enabled ? '停用' : '启用' }}</button>
                    <button type="button" :disabled="busyTargetId === target.target_id" class="ml-3 text-rose-400 hover:text-rose-300 disabled:opacity-40" @click="removeTarget(target)">删除</button>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </section>
    </main>

    <PanelFooter />

    <div v-if="showModal" class="fixed inset-0 bg-black/70 flex items-center justify-center p-4 z-50 backdrop-blur-sm" @click.self="closeModal">
      <section class="bg-dark-800 border border-dark-700 rounded-xl max-w-2xl w-full max-h-[92vh] overflow-y-auto custom-scrollbar p-6 shadow-2xl" role="dialog" aria-modal="true" :aria-label="modalTitle">
        <div class="flex items-start justify-between gap-4 mb-5">
          <div>
            <h2 class="font-bold text-sm text-slate-100">{{ modalTitle }}</h2>
            <p class="text-[11px] text-slate-400 mt-1">仅允许结构化的 TCP、HTTP 与 HTTPS 探测配置。</p>
          </div>
          <button type="button" :disabled="saving" class="p-1.5 bg-dark-900 text-slate-400 hover:text-white rounded-lg disabled:opacity-50" aria-label="关闭" @click="closeModal">✕</button>
        </div>

        <form class="space-y-4 text-xs" @submit.prevent="saveTarget">
          <div v-if="modalError" role="alert" class="p-3 bg-rose-500/10 border border-rose-500/30 rounded-lg text-rose-300">{{ modalError }}</div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="target-node" class="block text-slate-300 mb-1">所属节点</label>
              <select id="target-node" v-model="form.node_id" required :disabled="editingTarget !== null" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 disabled:opacity-60 focus:outline-none focus:border-emerald-500">
                <option v-for="node in nodes" :key="node.node_id" :value="node.node_id">{{ nodeLabel(node) }}</option>
              </select>
            </div>
            <div>
              <label for="target-name" class="block text-slate-300 mb-1">目标名称</label>
              <input id="target-name" v-model.trim="form.name" required maxlength="128" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 focus:outline-none focus:border-emerald-500" placeholder="例如：网站健康检查">
            </div>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="target-type" class="block text-slate-300 mb-1">探测类型</label>
              <select id="target-type" v-model="form.type" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 focus:outline-none focus:border-emerald-500" @change="handleTypeChange">
                <option value="tcp">TCP (端口握手)</option>
                <option value="http">HTTP (状态响应)</option>
                <option value="https">HTTPS (TLS + 状态响应)</option>
              </select>
            </div>
            <div>
              <label for="target-host" class="block text-slate-300 mb-1">目标主机</label>
              <input id="target-host" v-model.trim="form.host" required maxlength="253" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500" placeholder="example.com 或 192.0.2.10">
            </div>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="target-port" class="block text-slate-300 mb-1">端口{{ form.type === 'tcp' ? '（必填）' : '（留空使用协议默认值）' }}</label>
              <input id="target-port" v-model.number="form.port" type="number" :required="form.type === 'tcp'" min="1" max="65535" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500" :placeholder="form.type === 'https' ? '443' : '80'">
            </div>
            <div>
              <label for="target-path" class="block text-slate-300 mb-1">请求路径</label>
              <input id="target-path" v-model.trim="form.path" :disabled="form.type === 'tcp'" :required="form.type !== 'tcp'" maxlength="2048" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 font-mono disabled:opacity-50 focus:outline-none focus:border-emerald-500" placeholder="/health（不含查询串或片段）">
            </div>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="target-interval" class="block text-slate-300 mb-1">探测周期（10 - 86400 秒）</label>
              <input id="target-interval" v-model.number="form.interval_seconds" type="number" required min="10" max="86400" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
            </div>
            <div>
              <label for="target-timeout" class="block text-slate-300 mb-1">超时（1 - 60 秒，且不大于周期）</label>
              <input id="target-timeout" v-model.number="form.timeout_seconds" type="number" required min="1" max="60" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
            </div>
          </div>

          <div class="p-3 bg-dark-900 border border-dark-700 rounded-lg space-y-3">
            <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-1">
              <label for="target-retention" class="font-medium text-slate-200">独立保留期限</label>
              <span class="text-emerald-400 font-mono font-bold">{{ retentionDisplay }} ({{ form.retention_seconds }} 秒)</span>
            </div>
            <input type="range" min="1" max="90" :value="retentionSliderDays" class="w-full accent-emerald-500" aria-label="保留天数" @input="setRetentionDays">
            <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
              <span class="text-[10px] text-slate-500">滑块按整天设置；也可输入精确秒数，数据库与 API 均限制 ≤ 7,776,000 秒。</span>
              <input id="target-retention" v-model.number="form.retention_seconds" type="number" required min="1" max="7776000" class="w-40 px-2 py-1.5 bg-dark-950 border border-dark-700 rounded text-slate-200 font-mono">
            </div>
          </div>

          <label class="flex items-center gap-2 p-3 bg-dark-900 border border-dark-700 rounded-lg text-slate-300 cursor-pointer">
            <input v-model="form.enabled" type="checkbox" class="accent-emerald-500">
            <span>创建或保存后立即启用该目标</span>
          </label>

          <div class="flex justify-end gap-2 pt-2">
            <button type="button" :disabled="saving" class="px-3 py-2 bg-dark-700 hover:bg-dark-600 rounded-lg disabled:opacity-50" @click="closeModal">取消</button>
            <button type="submit" :disabled="saving" class="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 text-white rounded-lg font-medium disabled:opacity-50">{{ saving ? '保存中...' : '确认保存' }}</button>
          </div>
        </form>
      </section>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import PanelFooter from '../../components/PanelFooter.vue'
import PanelHeader from '../../components/PanelHeader.vue'
import { adminApi } from '../../api/admin'
import { panelApi } from '../../api/panel'
import { collectCursorPages } from '../../utils/admin'
import { MAX_PROBE_RETENTION_SECONDS, formatTargetAddress, probeTypeLabel, retentionDays } from '../../utils/probes'

const supportedTypes = new Set(['tcp', 'http', 'https'])
const nodes = ref([])
const targets = ref([])
const loading = ref(true)
const errorMessage = ref('')
const successMessage = ref('')
const showModal = ref(false)
const editingTarget = ref(null)
const saving = ref(false)
const modalError = ref('')
const busyTargetId = ref('')
const form = ref(createEmptyForm())

let loadController = null
let loadGeneration = 0
let mounted = false

const modalTitle = computed(() => editingTarget.value ? '编辑探测目标' : '新建探测目标')
const retentionSliderDays = computed(() => Math.max(1, Math.min(90, Math.round(Number(form.value.retention_seconds || 1) / 86400))))
const retentionDisplay = computed(() => {
  const days = retentionDays(form.value.retention_seconds)
  return days == null ? '—' : `${Number.isInteger(days) ? days.toFixed(0) : days.toFixed(2)} 天`
})

function createEmptyForm() {
  return {
    node_id: '',
    name: '',
    type: 'tcp',
    host: '',
    port: 443,
    path: null,
    interval_seconds: 30,
    timeout_seconds: 3,
    retention_seconds: MAX_PROBE_RETENTION_SECONDS,
    enabled: true,
  }
}

function nodeLabel(node) {
  return node.hostname || node.display_name || `节点 ${String(node.node_id).slice(0, 8)}`
}

function nodeLabelById(nodeId) {
  const node = nodes.value.find((item) => item.node_id === nodeId)
  return node ? nodeLabel(node) : `节点 ${String(nodeId || '').slice(0, 8)}`
}

function formatRetention(seconds) {
  const days = retentionDays(seconds)
  if (days == null) return '—'
  return `${Number.isInteger(days) ? days.toFixed(0) : days.toFixed(2)} 天 (${seconds}s)`
}

async function readAllNodes(signal) {
  return collectCursorPages(
    (cursor) => panelApi.getNodes({ limit: 100, cursor, signal }),
    'nodes',
  )
}

async function readAllTargets(signal) {
  return collectCursorPages(
    (cursor) => adminApi.getProbeTargets({ limit: 100, cursor, signal }),
    'targets',
  )
}

async function loadData() {
  loadGeneration += 1
  const generation = loadGeneration
  loadController?.abort()
  loadController = new AbortController()
  loading.value = true
  errorMessage.value = ''
  try {
    const [loadedNodes, loadedTargets] = await Promise.all([
      readAllNodes(loadController.signal),
      readAllTargets(loadController.signal),
    ])
    if (!mounted || generation !== loadGeneration) return
    nodes.value = loadedNodes
    targets.value = loadedTargets.filter((target) => supportedTypes.has(target.type))
  } catch (error) {
    if (error?.name === 'AbortError' || generation !== loadGeneration) return
    errorMessage.value = error?.message || '探测目标读取失败'
  } finally {
    if (mounted && generation === loadGeneration) loading.value = false
  }
}

function openCreate() {
  if (!nodes.value.length) return
  editingTarget.value = null
  form.value = { ...createEmptyForm(), node_id: nodes.value[0].node_id }
  modalError.value = ''
  showModal.value = true
}

function openEdit(target) {
  editingTarget.value = target
  form.value = {
    node_id: target.node_id,
    name: target.name,
    type: target.type,
    host: target.host,
    port: target.port,
    path: target.path,
    interval_seconds: target.interval_seconds,
    timeout_seconds: target.timeout_seconds,
    retention_seconds: target.retention_seconds,
    enabled: target.enabled,
  }
  modalError.value = ''
  showModal.value = true
}

function closeModal() {
  if (saving.value) return
  showModal.value = false
  editingTarget.value = null
  modalError.value = ''
}

function handleTypeChange() {
  if (form.value.type === 'tcp') {
    form.value.path = null
    if (!Number.isInteger(Number(form.value.port))) form.value.port = 443
    return
  }
  if (!form.value.path) form.value.path = '/'
  if (form.value.port === '') form.value.port = null
}

function setRetentionDays(event) {
  form.value.retention_seconds = Number(event.target.value) * 86400
}

function normalizedPayload() {
  const portValue = form.value.port === '' || form.value.port == null ? null : Number(form.value.port)
  return {
    name: String(form.value.name || '').trim(),
    type: form.value.type,
    host: String(form.value.host || '').trim(),
    port: portValue,
    path: form.value.type === 'tcp' ? null : (String(form.value.path || '').trim() || '/'),
    interval_seconds: Number(form.value.interval_seconds),
    timeout_seconds: Number(form.value.timeout_seconds),
    retention_seconds: Number(form.value.retention_seconds),
    enabled: Boolean(form.value.enabled),
  }
}

function validatePayload(payload) {
  if (!form.value.node_id) return '请选择所属节点'
  if (!payload.name || payload.name.length > 128) return '目标名称长度必须为 1 - 128 个字符'
  if (!supportedTypes.has(payload.type)) return '仅支持 TCP、HTTP 与 HTTPS'
  if (!payload.host || payload.host.length > 253 || /\s/.test(payload.host)) return '目标主机不能为空、不能包含空白，且最长 253 个字符'
  if (payload.port != null && (!Number.isInteger(payload.port) || payload.port < 1 || payload.port > 65535)) return '端口必须为 1 - 65535 的整数'
  if (payload.type === 'tcp' && payload.port == null) return 'TCP 探测必须填写端口'
  if (payload.type !== 'tcp' && (!payload.path.startsWith('/') || payload.path.length > 2048 || /[?#]/.test(payload.path))) return 'HTTP(S) 请求路径必须以 / 开头、不能包含 ? 或 #，且最长 2048 个字符'
  if (!Number.isInteger(payload.interval_seconds) || payload.interval_seconds < 10 || payload.interval_seconds > 86400) return '探测周期必须为 10 - 86400 秒的整数'
  if (!Number.isInteger(payload.timeout_seconds) || payload.timeout_seconds < 1 || payload.timeout_seconds > 60) return '超时必须为 1 - 60 秒的整数'
  if (payload.timeout_seconds > payload.interval_seconds) return '超时不能大于探测周期'
  if (!Number.isInteger(payload.retention_seconds) || payload.retention_seconds < 1 || payload.retention_seconds > MAX_PROBE_RETENTION_SECONDS) return '保留期限必须为 1 - 7,776,000 秒的整数'
  return ''
}

async function saveTarget() {
  const payload = normalizedPayload()
  const validationError = validatePayload(payload)
  if (validationError) {
    modalError.value = validationError
    return
  }

  saving.value = true
  modalError.value = ''
  try {
    if (editingTarget.value) await adminApi.updateProbeTarget(editingTarget.value.target_id, payload)
    else await adminApi.createProbeTarget({ node_id: form.value.node_id, ...payload })
    successMessage.value = editingTarget.value ? '探测目标已更新，节点配置版本已推进。' : '探测目标已创建，Agent 将在下一次配置刷新时获取。'
    showModal.value = false
    editingTarget.value = null
    await loadData()
  } catch (error) {
    modalError.value = error?.message || '保存探测目标失败'
  } finally {
    saving.value = false
  }
}

async function toggleTarget(target) {
  busyTargetId.value = target.target_id
  errorMessage.value = ''
  successMessage.value = ''
  try {
    await adminApi.updateProbeTarget(target.target_id, { enabled: !target.enabled })
    successMessage.value = target.enabled ? '探测目标已停用。' : '探测目标已启用。'
    await loadData()
  } catch (error) {
    errorMessage.value = error?.message || '更新探测目标状态失败'
  } finally {
    busyTargetId.value = ''
  }
}

async function removeTarget(target) {
  if (!window.confirm(`确认删除探测目标“${target.name}”？其历史结果也会被永久删除。`)) return
  busyTargetId.value = target.target_id
  errorMessage.value = ''
  successMessage.value = ''
  try {
    await adminApi.deleteProbeTarget(target.target_id)
    successMessage.value = '探测目标及其历史结果已删除。'
    await loadData()
  } catch (error) {
    errorMessage.value = error?.message || '删除探测目标失败'
  } finally {
    busyTargetId.value = ''
  }
}

onMounted(() => {
  mounted = true
  void loadData()
})

onUnmounted(() => {
  mounted = false
  loadGeneration += 1
  loadController?.abort()
})
</script>
