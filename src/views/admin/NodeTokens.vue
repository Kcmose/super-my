<template>
  <div class="min-h-screen flex flex-col bg-dark-950 text-slate-200 custom-scrollbar">
    <PanelHeader>
      <template #summary>
        <div class="flex items-center gap-2 text-slate-400 font-mono text-[11px]">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>
          <span>{{ nodes.length }} 个节点</span>
          <span class="text-slate-600">•</span>
          <span>{{ agentRuntimeEnabled ? 'Agent 接入已配置' : 'Agent 接入待配置' }}</span>
        </div>
      </template>
      <template #actions>
        <router-link to="/admin/probes" class="px-3 py-1.5 bg-dark-800 border border-dark-700/60 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition">探测配置</router-link>
      </template>
    </PanelHeader>

    <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6 flex-1 w-full space-y-6">
      <section class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl flex flex-col sm:flex-row sm:items-center justify-between gap-4 shadow-lg">
        <div>
          <h1 class="text-sm font-semibold text-slate-100">节点接入与 Agent 凭证管理</h1>
          <p class="text-xs text-slate-400 mt-1">先创建节点并保存 Agent 参数；独立的 Agent 接入配置完成后，才生成安装命令并由你在目标机主动执行。</p>
        </div>
        <div class="flex items-center gap-2">
          <button type="button" :disabled="loading || agentStatusLoading" class="px-3 py-2 bg-dark-800 border border-dark-700 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition disabled:opacity-50" @click="refreshPage">刷新</button>
          <button type="button" :disabled="loading || isBusy()" class="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 text-white rounded-lg text-xs font-medium transition disabled:opacity-50" @click="openCreate">+ 新建节点</button>
        </div>
      </section>

      <div v-if="!agentRuntimeEnabled" role="status" class="p-3 bg-amber-500/10 border border-amber-500/30 rounded-xl text-xs text-amber-200">
        {{ agentStatusError ? 'Agent 接入状态暂时无法确认，安装与令牌轮换已安全禁用。' : 'Agent 接入尚未配置。你可以先创建节点并保存采集参数；完成独立的 Agent 接入配置后，才可生成安装命令。' }}
      </div>

      <div v-if="errorMessage" role="alert" class="p-3 bg-rose-500/10 border border-rose-500/30 rounded-xl text-xs text-rose-300">{{ errorMessage }}</div>
      <div v-if="successMessage" role="status" class="p-3 bg-emerald-500/10 border border-emerald-500/30 rounded-xl text-xs text-emerald-300">{{ successMessage }}</div>

      <section class="bg-dark-900 border border-dark-700/80 rounded-xl overflow-hidden shadow-lg">
        <div class="overflow-x-auto custom-scrollbar">
          <table class="w-full min-w-[1120px] text-left text-xs text-slate-300">
            <thead class="bg-dark-950 text-slate-400 uppercase font-semibold border-b border-dark-700/80">
              <tr>
                <th class="py-3 px-4">状态</th>
                <th class="py-3 px-4">节点名称 / 主机名</th>
                <th class="py-3 px-4">节点 ID</th>
                <th class="py-3 px-4">位置</th>
                <th class="py-3 px-4">配置版本</th>
                <th class="py-3 px-4">注册 / 最后上报</th>
                <th class="py-3 px-4 text-right">管理操作</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-dark-700/60 font-mono">
              <tr v-if="loading"><td colspan="7" class="py-14 px-4 text-center text-slate-500">正在读取真实节点...</td></tr>
              <tr v-else-if="nodes.length === 0">
                <td colspan="7" class="py-14 px-4 text-center">
                  <div class="text-slate-300 font-sans font-semibold">暂无节点</div>
                  <div class="mt-1 text-[11px] text-slate-500 font-sans">可先创建节点并保存 Agent 参数，安装命令将在 Agent 接入配置完成后提供。</div>
                </td>
              </tr>
              <template v-else>
              <tr v-for="node in nodes" :key="node.node_id" class="hover:bg-dark-800/50 transition">
                <td class="py-3 px-4"><span class="px-2 py-0.5 rounded text-[10px] border" :class="statusClass(node.status)">{{ statusMeta(node.status).label }}</span></td>
                <td class="py-3 px-4">
                  <div class="font-sans font-medium text-slate-200">{{ node.display_name }}</div>
                  <div class="text-[10px] text-slate-500 mt-0.5">{{ node.hostname || 'Agent 尚未注册' }}</div>
                </td>
                <td class="py-3 px-4 text-[11px] text-slate-400">{{ node.node_id }}</td>
                <td class="py-3 px-4 font-sans">
                  <div class="text-slate-300">{{ node.location || '未设置' }}</div>
                  <div class="text-[10px] text-slate-500 mt-0.5">{{ [node.country_code, node.region_key].filter(Boolean).join(' / ') || '—' }}</div>
                </td>
                <td class="py-3 px-4 text-emerald-400 font-semibold">v{{ node.config_version }}</td>
                <td class="py-3 px-4 text-[10px] leading-5 text-slate-400">
                  <div>注册：{{ formatUTCDateTime(node.enrolled_at) }}</div>
                  <div>上报：{{ formatUTCDateTime(node.last_received_at) }}</div>
                </td>
                <td class="py-3 px-4 text-right font-sans whitespace-nowrap">
                  <button type="button" :disabled="isBusy(node)" class="text-slate-300 hover:text-emerald-400 disabled:opacity-40" @click="openEdit(node)">编辑</button>
                  <button type="button" :disabled="isBusy(node) || !node.enabled || !agentRuntimeEnabled" :title="installActionTitle(node)" class="ml-3 text-emerald-400 hover:text-emerald-300 disabled:opacity-40" @click="createEnrollment(node)">{{ node.enrolled_at ? '重新安装命令' : '安装命令' }}</button>
                  <button type="button" :disabled="isBusy(node) || !agentRuntimeEnabled" :title="agentRuntimeEnabled ? '' : '请先完成 Agent 接入配置'" class="ml-3 text-amber-400 hover:text-amber-300 disabled:opacity-40" @click="rotateAgentToken(node)">轮换</button>
                  <button type="button" :disabled="isBusy(node)" class="ml-3 text-orange-400 hover:text-orange-300 disabled:opacity-40" @click="revokeAgentToken(node)">吊销</button>
                  <button type="button" :disabled="isBusy(node)" class="ml-3 text-rose-400 hover:text-rose-300 disabled:opacity-40" @click="removeNode(node)">删除</button>
                </td>
              </tr>
              </template>
            </tbody>
          </table>
        </div>
      </section>
    </main>

    <PanelFooter />

    <div v-if="showNodeModal" class="fixed inset-0 bg-black/70 flex items-center justify-center p-4 z-50 backdrop-blur-sm" @click.self="closeNodeModal">
      <section class="bg-dark-800 border border-dark-700 rounded-xl max-w-2xl w-full max-h-[92vh] overflow-y-auto custom-scrollbar p-6 shadow-2xl" role="dialog" aria-modal="true" :aria-label="nodeModalTitle">
        <div class="flex items-start justify-between gap-4 mb-5">
          <div>
            <h2 class="font-bold text-sm text-slate-100">{{ nodeModalTitle }}</h2>
            <p class="text-[11px] text-slate-400 mt-1">这里只管理节点元数据、启用状态与结构化 Agent 采集设置，不提供任何远程控制。</p>
          </div>
          <button type="button" :disabled="savingNode" class="p-1.5 bg-dark-900 text-slate-400 hover:text-white rounded-lg disabled:opacity-50" aria-label="关闭" @click="closeNodeModal">✕</button>
        </div>

        <form class="space-y-4 text-xs" @submit.prevent="saveNode">
          <div v-if="nodeModalError" role="alert" class="p-3 bg-rose-500/10 border border-rose-500/30 rounded-lg text-rose-300">{{ nodeModalError }}</div>
          <div>
            <label for="node-display-name" class="block text-slate-300 mb-1">节点显示名称</label>
            <input id="node-display-name" v-model.trim="nodeForm.display_name" required maxlength="128" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 focus:outline-none focus:border-emerald-500" placeholder="例如：香港边缘节点">
          </div>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label for="node-country" class="block text-slate-300 mb-1">国家/地区代码</label>
              <input id="node-country" v-model.trim="nodeForm.country_code" maxlength="2" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 font-mono uppercase focus:outline-none focus:border-emerald-500" placeholder="例如：HK">
            </div>
            <div>
              <label for="node-region" class="block text-slate-300 mb-1">地区标识</label>
              <input id="node-region" v-model.trim="nodeForm.region_key" maxlength="64" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500" placeholder="例如：hk">
            </div>
          </div>
          <div>
            <label for="node-location" class="block text-slate-300 mb-1">位置说明</label>
            <input id="node-location" v-model.trim="nodeForm.location" maxlength="128" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 focus:outline-none focus:border-emerald-500" placeholder="例如：中国香港">
          </div>
          <fieldset class="p-3 bg-dark-900 border border-dark-700 rounded-lg space-y-3">
            <legend class="px-1 text-slate-200 font-semibold">Agent 采集设置</legend>
            <p class="text-[11px] text-slate-500">参数可在安装 Agent 前预先保存；接入完成后，Agent 会在配置刷新时获取完整结构化设置。</p>
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label for="node-collect-interval" class="block text-slate-300 mb-1">采集周期（秒）</label>
                <input id="node-collect-interval" v-model.number="nodeForm.agent_settings.metrics.collect_interval_seconds" required type="number" min="5" max="300" step="1" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
              </div>
              <div>
                <label for="node-report-interval" class="block text-slate-300 mb-1">上报周期（秒）</label>
                <input id="node-report-interval" v-model.number="nodeForm.agent_settings.metrics.report_interval_seconds" required type="number" min="5" max="300" step="1" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
              </div>
            </div>
            <div>
              <label for="node-mountpoints" class="block text-slate-300 mb-1">监控挂载点（每行一个）</label>
              <textarea id="node-mountpoints" v-model="mountpointsText" required rows="3" spellcheck="false" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono resize-y focus:outline-none focus:border-emerald-500" placeholder="/&#10;/data"></textarea>
              <p class="mt-1 text-[10px] text-slate-500">1 - 32 个绝对路径，不能重复，且必须包含根目录 /。</p>
            </div>
            <label class="flex items-center gap-2 text-slate-300 cursor-pointer">
              <input v-model="nodeForm.agent_settings.metrics.include_virtual_interfaces" type="checkbox" class="accent-emerald-500">
              <span>统计容器及虚拟网卡</span>
            </label>
            <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <label for="node-config-refresh" class="block text-slate-300 mb-1">配置刷新（秒）</label>
                <input id="node-config-refresh" v-model.number="nodeForm.agent_settings.agent.config_refresh_interval_seconds" required type="number" min="10" max="86400" step="1" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
              </div>
              <div>
                <label for="node-memory-queue" class="block text-slate-300 mb-1">内存队列（秒）</label>
                <input id="node-memory-queue" v-model.number="nodeForm.agent_settings.agent.max_memory_queue_seconds" required type="number" min="1" max="300" step="1" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
              </div>
              <div>
                <label for="node-max-batch" class="block text-slate-300 mb-1">单批样本上限</label>
                <input id="node-max-batch" v-model.number="nodeForm.agent_settings.limits.max_batch_samples" required type="number" min="1" max="120" step="1" class="w-full px-3 py-2 bg-dark-950 border border-dark-700 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-emerald-500">
              </div>
            </div>
          </fieldset>
          <label class="flex items-center gap-2 p-3 bg-dark-900 border border-dark-700 rounded-lg text-slate-300 cursor-pointer">
            <input v-model="nodeForm.enabled" type="checkbox" class="accent-emerald-500">
            <span>允许该节点在 Agent 接入配置完成后使用有效凭证接入</span>
          </label>
          <div class="flex justify-end gap-2 pt-2">
            <button type="button" :disabled="savingNode" class="px-3 py-2 bg-dark-700 hover:bg-dark-600 rounded-lg disabled:opacity-50" @click="closeNodeModal">取消</button>
            <button type="submit" :disabled="savingNode" class="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 text-white rounded-lg font-medium disabled:opacity-50">{{ savingNode ? '保存中...' : '确认保存' }}</button>
          </div>
        </form>
      </section>
    </div>

    <div v-if="secretDialog" class="fixed inset-0 bg-black/75 flex items-center justify-center p-4 z-[60] backdrop-blur-sm">
      <section class="bg-dark-800 border border-emerald-500/30 rounded-xl max-w-xl w-full max-h-[92vh] overflow-y-auto custom-scrollbar p-6 shadow-2xl" role="dialog" aria-modal="true" :aria-label="secretDialog.title">
        <div class="flex items-start justify-between gap-4">
          <div>
            <h2 class="font-bold text-sm text-slate-100">{{ secretDialog.title }}</h2>
            <p class="text-[11px] text-amber-300 mt-1">{{ secretDialog.notice }}</p>
          </div>
          <button type="button" class="p-1.5 bg-dark-900 text-slate-400 hover:text-white rounded-lg" aria-label="关闭敏感内容显示" @click="closeSecret">✕</button>
        </div>
        <div class="mt-5 p-3 bg-dark-950 border border-dark-700 rounded-lg break-all font-mono text-xs leading-6 text-emerald-300 select-all">{{ secretDialog.value }}</div>
        <div class="mt-3 text-[11px] text-slate-400 font-mono">
          <div>节点：{{ secretDialog.nodeId }}</div>
          <div>{{ secretDialog.timeLabel }}：{{ formatUTCDateTime(secretDialog.time) }}</div>
        </div>
        <div class="mt-5 flex items-center justify-end gap-2">
          <span v-if="copyStatus" role="status" class="mr-auto text-[11px]" :class="copyStatus === '已复制' ? 'text-emerald-400' : 'text-rose-400'">{{ copyStatus }}</span>
          <button type="button" class="px-3 py-2 bg-dark-700 hover:bg-dark-600 text-slate-200 rounded-lg text-xs" @click="copySecret">复制到剪贴板</button>
          <button type="button" class="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 text-white rounded-lg text-xs font-medium" @click="closeSecret">关闭显示</button>
        </div>
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
import {
  cloneAgentSettings,
  collectCursorPages,
  createDefaultAgentSettings,
  formatUTCDateTime,
  normalizeNodePayload,
  validateNodePayload,
} from '../../utils/admin'
import { statusMeta } from '../../utils/panel'

const nodes = ref([])
const loading = ref(true)
const errorMessage = ref('')
const successMessage = ref('')
const showNodeModal = ref(false)
const editingNode = ref(null)
const nodeForm = ref(emptyNodeForm())
const nodeModalError = ref('')
const savingNode = ref(false)
const busyNodeId = ref('')
const secretDialog = ref(null)
const copyStatus = ref('')
const agentRuntimeEnabled = ref(false)
const agentStatusLoading = ref(true)
const agentStatusError = ref('')

let mounted = false
let loadGeneration = 0
let loadController = null

const nodeModalTitle = computed(() => editingNode.value ? '编辑节点' : '新建节点')
const mountpointsText = computed({
  get: () => nodeForm.value.agent_settings.metrics.mountpoints.join('\n'),
  set: (value) => {
    nodeForm.value.agent_settings.metrics.mountpoints = String(value).split(/\r?\n/)
  },
})

function emptyNodeForm() {
  return {
    display_name: '',
    enabled: true,
    country_code: '',
    region_key: '',
    location: '',
    agent_settings: createDefaultAgentSettings(),
  }
}

function isBusy() {
  return Boolean(busyNodeId.value)
}

function installActionTitle(node) {
  if (!agentRuntimeEnabled.value) return '请先完成 Agent 接入配置'
  return node.enabled ? '' : '请先启用节点'
}

function statusClass(status) {
  if (status === 'online') return 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
  if (status === 'skewed') return 'bg-amber-500/10 text-amber-400 border-amber-500/20'
  if (status === 'unregistered') return 'bg-slate-700/50 text-slate-400 border-slate-600/50'
  return 'bg-rose-500/10 text-rose-400 border-rose-500/20'
}

async function loadNodes() {
  const generation = ++loadGeneration
  loadController?.abort()
  const controller = new AbortController()
  loadController = controller
  loading.value = true
  errorMessage.value = ''
  try {
    const loaded = await collectCursorPages(
      (cursor) => panelApi.getNodes({ limit: 100, cursor, signal: controller.signal }),
      'nodes',
    )
    if (!mounted || generation !== loadGeneration) return
    nodes.value = loaded
  } catch (error) {
    if (error?.name === 'AbortError' || generation !== loadGeneration) return
    errorMessage.value = error?.message || '节点读取失败'
  } finally {
    if (mounted && generation === loadGeneration) loading.value = false
  }
}

async function loadAgentStatus() {
  agentStatusLoading.value = true
  agentStatusError.value = ''
  try {
    const response = await adminApi.getSystemStatus()
    const state = response?.agent?.status
    if (!['configured', 'not_configured'].includes(state)) throw new Error('服务端未返回有效的 Agent 接入状态')
    if (!mounted) return
    agentRuntimeEnabled.value = state === 'configured'
  } catch (error) {
    if (!mounted) return
    agentRuntimeEnabled.value = false
    agentStatusError.value = error?.message || 'Agent 接入状态读取失败'
  } finally {
    if (mounted) agentStatusLoading.value = false
  }
}

function refreshPage() {
  void loadNodes()
  void loadAgentStatus()
}

function openCreate() {
  editingNode.value = null
  nodeForm.value = emptyNodeForm()
  nodeModalError.value = ''
  showNodeModal.value = true
}

function openEdit(node) {
  editingNode.value = node
  nodeForm.value = {
    display_name: node.display_name,
    enabled: node.enabled,
    country_code: node.country_code || '',
    region_key: node.region_key || '',
    location: node.location || '',
    agent_settings: cloneAgentSettings(node.agent_settings),
  }
  nodeModalError.value = ''
  showNodeModal.value = true
}

function closeNodeModal() {
  if (savingNode.value) return
  showNodeModal.value = false
  editingNode.value = null
  nodeForm.value = emptyNodeForm()
  nodeModalError.value = ''
}

async function saveNode() {
  const payload = normalizeNodePayload(nodeForm.value)
  const validationError = validateNodePayload(payload)
  if (validationError) {
    nodeModalError.value = validationError
    return
  }

  savingNode.value = true
  nodeModalError.value = ''
  try {
    if (editingNode.value) {
      await adminApi.updateNode(editingNode.value.node_id, payload)
      successMessage.value = '节点设置已更新。'
      showNodeModal.value = false
      editingNode.value = null
      nodeForm.value = emptyNodeForm()
      await loadNodes()
      return
    }

    const createdNode = await adminApi.createNode(payload)
    showNodeModal.value = false
    editingNode.value = null
    nodeForm.value = emptyNodeForm()
    successMessage.value = agentRuntimeEnabled.value
      ? (payload.enabled ? '节点已创建，正在生成一次性安装命令。' : '节点已创建但当前处于禁用状态；启用后才能生成安装命令。')
      : '节点及 Agent 参数已保存；Agent 接入尚未配置，暂不生成安装命令。'
    await loadNodes()

    if (!payload.enabled || !agentRuntimeEnabled.value) return

    if (typeof createdNode?.node_id !== 'string' || !createdNode.node_id) {
      errorMessage.value = '节点已经创建，但服务端未返回节点 ID，无法自动生成安装命令。请刷新列表后点击“安装命令”重试。'
      return
    }
    busyNodeId.value = createdNode.node_id
    try {
      const response = await adminApi.createEnrollmentToken(createdNode.node_id, { expiresInSeconds: 900 })
      if (!mounted) return
      showInstallCommand(response)
      successMessage.value = '节点已创建，请复制命令并在目标 Debian/Linux 主机上主动执行。'
    } catch (error) {
		if (error?.code === 'agent_not_configured') {
			agentRuntimeEnabled.value = false
			agentStatusError.value = ''
		}
      errorMessage.value = `节点已经创建，但安装命令生成失败：${error?.message || '未知错误'}。请点击该节点的“安装命令”重试。`
    } finally {
      busyNodeId.value = ''
    }
  } catch (error) {
    nodeModalError.value = error?.message || '节点保存失败'
  } finally {
    savingNode.value = false
  }
}

function showSecret({ title, value, nodeId, time, timeLabel, notice = '此明文只显示一次。关闭窗口后前端会立即清除，无法再次查看。' }) {
  secretDialog.value = { title, value, nodeId, time, timeLabel, notice }
  copyStatus.value = ''
}

function showInstallCommand(response, { reinstallation = false } = {}) {
  if (typeof response?.install_command !== 'string' || !response.install_command) throw new Error('服务端未返回有效的安装命令')
  showSecret({
    title: 'Agent 一键安装命令',
    value: response.install_command,
    nodeId: response.node_id,
    time: response.expires_at,
		timeLabel: '命令内令牌过期时间',
		notice: reinstallation
			? '这是重新安装命令：请直接粘贴到目标机可使用 sudo 的 Shell。若在另一台主机成功注册，原 Agent 会立即失效。一次性令牌可能短暂出现在进程参数，并可能留在 Shell 历史和剪贴板中，请用后主动覆盖。'
			: '命令含 15 分钟有效的一次性令牌，新生成的命令会废止旧命令。请直接粘贴到目标机可使用 sudo 的 Shell；令牌可能短暂出现在进程参数，并可能留在 Shell 历史和剪贴板中，请用后主动覆盖。',
  })
}

function closeSecret() {
  secretDialog.value = null
  copyStatus.value = ''
}

async function copySecret() {
  if (!secretDialog.value) return
  try {
    if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
    await navigator.clipboard.writeText(secretDialog.value.value)
    copyStatus.value = '已复制'
  } catch {
    copyStatus.value = '无法自动复制，请手动选择内容'
  }
}

async function createEnrollment(node) {
	if (!agentRuntimeEnabled.value) {
		errorMessage.value = 'Agent 接入尚未配置，请先完成独立的 Agent 接入配置。'
		return
	}
	if (!node.enabled) {
		errorMessage.value = '节点当前已禁用，请先编辑并启用节点，再生成安装命令。'
		return
	}
	if (node.enrolled_at && !window.confirm(`确认生成节点“${node.display_name}”的重新安装命令？命令一旦在任意主机成功执行，当前 Agent 凭证会立即失效。`)) return
  busyNodeId.value = node.node_id
  errorMessage.value = ''
  successMessage.value = ''
  closeSecret()
  try {
    const response = await adminApi.createEnrollmentToken(node.node_id, { expiresInSeconds: 900 })
    if (!mounted) return
    showInstallCommand(response, { reinstallation: Boolean(node.enrolled_at) })
  } catch (error) {
	if (error?.code === 'agent_not_configured') {
		agentRuntimeEnabled.value = false
		agentStatusError.value = ''
	}
    errorMessage.value = error?.message || '安装命令生成失败'
  } finally {
    busyNodeId.value = ''
  }
}

async function rotateAgentToken(node) {
  if (!agentRuntimeEnabled.value) {
    errorMessage.value = 'Agent 接入尚未配置，请先完成独立的 Agent 接入配置。'
    return
  }
  if (!window.confirm(`确认轮换节点“${node.display_name}”的 Agent Token？旧 Token 将立即失效。`)) return
  busyNodeId.value = node.node_id
  errorMessage.value = ''
  successMessage.value = ''
  closeSecret()
  try {
    const response = await adminApi.rotateToken(node.node_id)
    if (!mounted) return
    if (typeof response?.agent_token !== 'string' || !response.agent_token) throw new Error('服务端未返回有效的 Agent Token')
    showSecret({ title: '新的 Agent Token', value: response.agent_token, nodeId: response.node_id, time: response.created_at, timeLabel: '签发时间' })
  } catch (error) {
    errorMessage.value = error?.message || 'Agent Token 轮换失败'
  } finally {
    busyNodeId.value = ''
  }
}

async function revokeAgentToken(node) {
  if (!window.confirm(`确认吊销节点“${node.display_name}”的全部有效 Agent Token？`)) return
  busyNodeId.value = node.node_id
  errorMessage.value = ''
  successMessage.value = ''
  closeSecret()
  try {
    await adminApi.revokeToken(node.node_id)
    successMessage.value = '该节点的有效 Agent Token 已全部吊销。'
    await loadNodes()
  } catch (error) {
    errorMessage.value = error?.message || 'Agent Token 吊销失败'
  } finally {
    busyNodeId.value = ''
  }
}

async function removeNode(node) {
  if (!window.confirm(`确认删除节点“${node.display_name}”？服务端记录和凭证会被删除，但不会向 Agent 下发任何命令。`)) return
  busyNodeId.value = node.node_id
  errorMessage.value = ''
  successMessage.value = ''
  closeSecret()
  try {
    await adminApi.deleteNode(node.node_id)
    successMessage.value = '节点服务端记录及其凭证已删除。'
    await loadNodes()
  } catch (error) {
    errorMessage.value = error?.message || '节点删除失败'
  } finally {
    busyNodeId.value = ''
  }
}

onMounted(() => {
  mounted = true
  refreshPage()
})

onUnmounted(() => {
  mounted = false
  loadGeneration += 1
  loadController?.abort()
  closeSecret()
  nodeForm.value = emptyNodeForm()
})
</script>
