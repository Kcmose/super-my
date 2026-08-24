<template>
  <div class="relative min-h-screen bg-dark-950 text-slate-200 px-4 py-8 sm:px-6">
    <ThemeToggle class="absolute right-4 top-4" />

    <main class="mx-auto w-full max-w-4xl">
      <section class="card-glass border border-dark-700/80 rounded-xl shadow-2xl overflow-hidden">
        <header class="px-5 py-5 sm:px-8 border-b border-dark-700/60">
          <div class="flex items-center gap-3 pr-20">
            <div class="inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-xl border border-emerald-500/30 bg-emerald-500/10 text-emerald-400">
              <svg class="h-6 w-6" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z" />
              </svg>
            </div>
            <div>
              <h1 class="theme-heading text-xl font-extrabold text-white tracking-tight">PROBE 安全安装向导</h1>
              <p class="mt-1 text-xs text-slate-400">仅通过服务器回环地址和 SSH 隧道完成首次初始化</p>
            </div>
          </div>
        </header>

        <div class="px-5 py-5 sm:px-8">
          <div class="mb-5 flex items-start gap-2 rounded-lg border border-dark-700 bg-dark-900/80 p-3 text-xs text-slate-300">
            <span class="font-bold" :class="statusDotClass">●</span>
            <div>
              <div class="font-medium">服务状态：{{ statusLabel }}</div>
              <div v-if="sessionExpiresLabel" class="mt-0.5 font-mono text-[11px] text-slate-500">临时安装会话到期：{{ sessionExpiresLabel }}</div>
              <div v-else class="mt-0.5 text-[11px] text-slate-500">数据库密码、安装码和管理员密码只保存在当前页面内存中。</div>
            </div>
          </div>

          <ol v-if="!isRecovery" class="mb-7 grid grid-cols-5 gap-1" aria-label="安装进度">
            <li v-for="item in steps" :key="item.number" class="min-w-0 text-center">
              <div class="mx-auto flex h-7 w-7 items-center justify-center rounded-full border text-[11px] font-bold transition"
                :class="item.number <= step ? 'border-emerald-500/50 bg-emerald-500/10 text-emerald-400' : 'border-dark-700 bg-dark-900 text-slate-500'"
                :aria-current="item.number === step ? 'step' : undefined">
                {{ item.number }}
              </div>
              <div class="mt-1 truncate text-[10px]" :class="item.number === step ? 'text-slate-200' : 'text-slate-500'">{{ item.label }}</div>
            </li>
          </ol>

          <div v-if="initialLoading" class="rounded-lg border border-dark-700 bg-dark-900/80 p-8 text-center text-sm text-slate-400">
            正在确认服务器安装状态...
          </div>

          <div v-else-if="isRecovery" class="rounded-lg border border-rose-500/30 bg-rose-500/10 p-5" role="alert">
            <h2 class="text-sm font-bold text-rose-400">安装进入恢复状态</h2>
            <p class="mt-2 text-xs leading-5 text-slate-300">服务器没有重新开放安装权限。请在服务器终端运行本地管理命令查看状态并按提示恢复，不要刷新数据库或删除管理员来尝试重新安装。</p>
            <p v-if="serverMessage" class="mt-3 rounded bg-dark-900/80 p-3 font-mono text-[11px] text-slate-400">{{ serverMessage }}</p>
            <button type="button" class="mt-4 rounded-lg border border-dark-700 bg-dark-800 px-3 py-2 text-xs text-slate-300 hover:text-emerald-400 transition" @click="refreshInitialStatus">重新检查状态</button>
          </div>

          <div v-else-if="finishing" class="rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-7 text-center" aria-live="polite">
            <div class="mx-auto h-3 w-3 rounded-full bg-emerald-500 animate-pulse"></div>
            <h2 class="mt-4 text-sm font-bold text-emerald-400">正在完成服务器配置</h2>
            <p class="mt-2 text-xs leading-5 text-slate-300">系统正在创建数据库、申请证书、执行迁移并验证正式服务。请保持此页面打开，完成后会自动进入管理员登录页。</p>
            <p v-if="pollMessage" class="mt-3 text-[11px] text-amber-400">{{ pollMessage }}</p>
          </div>

          <template v-else>
            <section v-if="step === 1" aria-labelledby="setup-code-title">
              <h2 id="setup-code-title" class="theme-heading text-base font-bold text-white">验证一次性安装码</h2>
              <p class="mt-1 text-xs leading-5 text-slate-400">安装脚本会在服务器终端显示一个 30 分钟有效的安装码。不要把安装码放进 URL、命令参数或聊天记录。</p>

              <form class="mt-5 space-y-4" @submit.prevent="openSetupSession">
                <div>
                  <label for="setup-code" class="mb-1 block text-xs font-medium text-slate-300">安装码</label>
                  <input id="setup-code" v-model="setupCode" type="password" autocomplete="off" autocapitalize="off" spellcheck="false" maxlength="1024" required
                    class="w-full rounded-lg border border-dark-700 bg-dark-950 px-3 py-2.5 font-mono text-sm text-slate-200 placeholder-slate-600 focus:outline-none focus:border-emerald-500 transition"
                    placeholder="输入服务器终端显示的安装码" />
                </div>
                <div class="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-[11px] leading-5 text-amber-300">
                  安装码验证成功后立即失效。关闭或刷新页面会清除临时会话，需要在服务器终端生成新的安装码。
                </div>
                <button type="submit" :disabled="busy || !setupCode.trim()" class="w-full rounded-lg bg-emerald-600 py-2.5 text-sm font-medium text-white shadow-lg shadow-emerald-900/30 hover:bg-emerald-500 disabled:cursor-wait disabled:opacity-60 transition">
                  {{ busy ? '正在验证...' : '验证并开始配置' }}
                </button>
              </form>
            </section>

            <section v-else-if="step === 2" aria-labelledby="database-title">
              <h2 id="database-title" class="theme-heading text-base font-bold text-white">配置本机 PostgreSQL</h2>
              <p class="mt-1 text-xs leading-5 text-slate-400">首版只支持安装在同一台服务器上的 PostgreSQL，不会开放 5432 公网端口。</p>
              <div class="mt-5 grid gap-4 sm:grid-cols-2">
                <div>
                  <label for="database-name" class="mb-1 block text-xs font-medium text-slate-300">数据库名称</label>
                  <input id="database-name" v-model.trim="form.database.name" type="text" autocomplete="off" maxlength="63" class="form-control" />
                </div>
                <div>
                  <label for="database-user" class="mb-1 block text-xs font-medium text-slate-300">数据库用户</label>
                  <input id="database-user" v-model.trim="form.database.username" type="text" autocomplete="off" maxlength="63" class="form-control" />
                </div>
                <div>
                  <label for="database-password" class="mb-1 block text-xs font-medium text-slate-300">数据库密码</label>
                  <input id="database-password" v-model="form.database.password" type="password" autocomplete="new-password" maxlength="1024" class="form-control" />
                </div>
                <div>
                  <label for="database-password-confirmation" class="mb-1 block text-xs font-medium text-slate-300">确认数据库密码</label>
                  <input id="database-password-confirmation" v-model="form.database.password_confirmation" type="password" autocomplete="new-password" maxlength="1024" class="form-control" />
                </div>
              </div>
            </section>

            <section v-else-if="step === 3" aria-labelledby="network-title">
              <h2 id="network-title" class="theme-heading text-base font-bold text-white">配置域名、证书与访问白名单</h2>
              <p class="mt-1 text-xs leading-5 text-slate-400">请先把三个域名的 DNS 记录解析到本服务器。域名只填主机名，不要包含 <span class="font-mono">https://</span> 或路径。</p>
              <div class="mt-5 grid gap-4 sm:grid-cols-2">
                <div>
                  <label for="panel-domain" class="mb-1 block text-xs font-medium text-slate-300">游客面板域名</label>
                  <input id="panel-domain" v-model.trim="form.domains.panel" type="text" autocomplete="url" maxlength="253" class="form-control" placeholder="panel.example.com" />
                </div>
                <div>
                  <label for="admin-domain" class="mb-1 block text-xs font-medium text-slate-300">管理面板域名</label>
                  <input id="admin-domain" v-model.trim="form.domains.admin" type="text" autocomplete="url" maxlength="253" class="form-control" placeholder="admin.example.com" />
                </div>
                <div>
                  <label for="agent-domain" class="mb-1 block text-xs font-medium text-slate-300">Agent API 域名</label>
                  <input id="agent-domain" v-model.trim="form.domains.agent" type="text" autocomplete="url" maxlength="253" class="form-control" placeholder="api.example.com" />
                </div>
                <div>
                  <label for="acme-email" class="mb-1 block text-xs font-medium text-slate-300">ACME 证书通知邮箱</label>
                  <input id="acme-email" v-model.trim="form.tls.email" type="email" autocomplete="email" maxlength="254" class="form-control" placeholder="admin@example.com" />
                </div>
                <div class="sm:col-span-2">
                  <label for="allowlist" class="mb-1 block text-xs font-medium text-slate-300">游客与管理面板访问白名单</label>
                  <textarea id="allowlist" v-model="allowlistText" rows="5" spellcheck="false" class="form-control font-mono" placeholder="203.0.113.25/32&#10;2001:db8:1234::/48"></textarea>
                  <p class="mt-1 text-[11px] text-slate-500">每行一个 IPv4、IPv6 或 CIDR。禁止使用 0.0.0.0/0 和 ::/0；Agent API 不受此白名单限制。</p>
                </div>
              </div>
            </section>

            <section v-else-if="step === 4" aria-labelledby="administrator-title">
              <h2 id="administrator-title" class="theme-heading text-base font-bold text-white">创建首个管理员</h2>
              <p class="mt-1 text-xs leading-5 text-slate-400">系统只创建管理员账户。游客打开独立前端即可查看，不需要账号或密码。</p>
              <div class="mt-5 grid gap-4 sm:grid-cols-2">
                <div class="sm:col-span-2">
                  <label for="administrator-username" class="mb-1 block text-xs font-medium text-slate-300">管理员用户名</label>
                  <input id="administrator-username" v-model.trim="form.administrator.username" type="text" autocomplete="username" maxlength="128" class="form-control" />
                </div>
                <div>
                  <label for="administrator-password" class="mb-1 block text-xs font-medium text-slate-300">管理员密码</label>
                  <input id="administrator-password" v-model="form.administrator.password" type="password" autocomplete="new-password" maxlength="1024" class="form-control" />
                </div>
                <div>
                  <label for="administrator-password-confirmation" class="mb-1 block text-xs font-medium text-slate-300">确认管理员密码</label>
                  <input id="administrator-password-confirmation" v-model="form.administrator.password_confirmation" type="password" autocomplete="new-password" maxlength="1024" class="form-control" />
                </div>
              </div>
            </section>

            <section v-else aria-labelledby="review-title">
              <h2 id="review-title" class="theme-heading text-base font-bold text-white">确认安装配置</h2>
              <p class="mt-1 text-xs leading-5 text-slate-400">提交后将执行数据库初始化、证书申请、迁移和正式服务切换。完成前不会开放正式面板入口。</p>
              <dl class="mt-5 grid gap-3 text-xs sm:grid-cols-2">
                <div class="rounded-lg border border-dark-700 bg-dark-900/80 p-3">
                  <dt class="text-slate-500">本机数据库</dt>
                  <dd class="mt-1 font-mono text-slate-300">{{ payload.database.name }} / {{ payload.database.username }}</dd>
                </div>
                <div class="rounded-lg border border-dark-700 bg-dark-900/80 p-3">
                  <dt class="text-slate-500">首个管理员</dt>
                  <dd class="mt-1 font-mono text-slate-300">{{ payload.administrator.username }}</dd>
                </div>
                <div class="rounded-lg border border-dark-700 bg-dark-900/80 p-3 sm:col-span-2">
                  <dt class="text-slate-500">三个 HTTPS 入口</dt>
                  <dd class="mt-1 space-y-1 font-mono text-slate-300">
                    <div>{{ payload.domains.panel }}</div>
                    <div>{{ payload.domains.admin }}</div>
                    <div>{{ payload.domains.agent }}</div>
                  </dd>
                </div>
                <div class="rounded-lg border border-dark-700 bg-dark-900/80 p-3">
                  <dt class="text-slate-500">TLS</dt>
                  <dd class="mt-1 text-slate-300">ACME · {{ payload.tls.email }}</dd>
                </div>
                <div class="rounded-lg border border-dark-700 bg-dark-900/80 p-3">
                  <dt class="text-slate-500">访问白名单（{{ payload.allowlist.length }} 项）</dt>
                  <dd class="mt-1 max-h-20 overflow-auto whitespace-pre-wrap font-mono text-slate-300">{{ payload.allowlist.join('\n') }}</dd>
                </div>
              </dl>
              <div class="mt-4 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-[11px] leading-5 text-amber-300">提交前请确认 DNS 已生效、服务器公网 80/443 可达，并保持 SSH 隧道连接。</div>
            </section>

            <div v-if="errorMessage" role="alert" class="mt-5 rounded-lg border border-rose-500/30 bg-rose-500/10 p-3 text-xs text-rose-400">
              {{ errorMessage }}
              <span v-if="requestId" class="mt-1 block font-mono text-[10px] text-rose-300/70">请求 ID: {{ requestId }}</span>
            </div>

            <div v-if="step > 1" class="mt-7 flex items-center justify-between gap-3 border-t border-dark-700/60 pt-5">
              <button v-if="step > 2" type="button" :disabled="busy" class="rounded-lg border border-dark-700 bg-dark-800 px-4 py-2 text-xs text-slate-300 hover:text-emerald-400 disabled:opacity-60 transition" @click="previousStep">上一步</button>
              <span v-else class="text-[11px] text-slate-500">安装码已经消费，不能返回第一步</span>
              <button v-if="step < 5" type="button" :disabled="busy" class="rounded-lg bg-emerald-600 px-5 py-2 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-60 transition" @click="nextStep">下一步</button>
              <button v-else type="button" :disabled="busy" class="rounded-lg bg-emerald-600 px-5 py-2 text-xs font-medium text-white shadow-lg shadow-emerald-900/30 hover:bg-emerald-500 disabled:cursor-wait disabled:opacity-60 transition" @click="completeInstallation">
                {{ busy ? '正在提交...' : '确认并开始安装' }}
              </button>
            </div>
          </template>
        </div>
      </section>

      <p class="mt-4 text-center text-[11px] text-slate-500">初始化服务仅监听服务器回环地址 · 凭据不会写入浏览器存储</p>
    </main>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import ThemeToggle from '../components/ThemeToggle.vue'
import { clearSetupSecrets, setupApi } from '../api/setup.js'
import { normalizeSetupPayload, setupStatusValue, validateSetupStep } from '../utils/setup.js'

const steps = Object.freeze([
  { number: 1, label: '安装码' },
  { number: 2, label: '数据库' },
  { number: 3, label: '域名与证书' },
  { number: 4, label: '管理员' },
  { number: 5, label: '确认' },
])

const router = useRouter()
const step = ref(1)
const setupCode = ref('')
const allowlistText = ref('')
const busy = ref(false)
const initialLoading = ref(true)
const finishing = ref(false)
const serverStatus = ref('')
const serverMessage = ref('')
const sessionExpiresAt = ref('')
const errorMessage = ref('')
const requestId = ref('')
const pollMessage = ref('')
let statusController = null
let pollTimer = null

const form = reactive({
  database: {
    name: 'probe',
    username: 'probe',
    password: '',
    password_confirmation: '',
  },
  domains: {
    panel: '',
    admin: '',
    agent: '',
  },
  tls: { email: '' },
  administrator: {
    username: 'admin',
    password: '',
    password_confirmation: '',
  },
})

const payload = computed(() => normalizeSetupPayload({
  database: form.database,
  domains: form.domains,
  tls: form.tls,
  allowlist: allowlistText.value,
  administrator: form.administrator,
}))
const isRecovery = computed(() => serverStatus.value === 'recovery_required')
const statusLabel = computed(() => ({
  pending: '等待初始化',
  configuring: '正在配置',
  finalizing: '正在激活正式服务',
  installed: '已经安装',
  recovery_required: '需要终端恢复',
}[serverStatus.value] || '正在确认'))
const statusDotClass = computed(() => (
  isRecovery.value ? 'text-rose-400' : finishing.value ? 'text-emerald-400' : 'text-amber-400'
))
const sessionExpiresLabel = computed(() => {
  if (!sessionExpiresAt.value) return ''
  const parsed = new Date(sessionExpiresAt.value)
  return Number.isFinite(parsed.getTime()) ? parsed.toLocaleString() : sessionExpiresAt.value
})

function clearMessages() {
  errorMessage.value = ''
  requestId.value = ''
}

function clearFormSecrets() {
  setupCode.value = ''
  form.database.password = ''
  form.database.password_confirmation = ''
  form.administrator.password = ''
  form.administrator.password_confirmation = ''
}

async function goToLogin() {
  clearSetupSecrets()
  clearFormSecrets()
  await router.replace({ name: 'Login' })
}

function installedAdminURL(response) {
  if (typeof response?.admin_url !== 'string') return ''
  try {
    const target = new URL(response.admin_url)
    if (
      target.protocol !== 'https:'
      || target.username
      || target.password
      || target.pathname !== '/login'
      || target.search
      || target.hash
    ) return ''
    return target.href
  } catch {
    return ''
  }
}

async function leaveInstalledSetup(response) {
  clearSetupSecrets()
  clearFormSecrets()
  const target = installedAdminURL(response)
  if (target) {
    window.location.replace(target)
    return
  }
  await goToLogin()
}

function scheduleStatusPoll(delay = 2000) {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => void pollInstallationStatus(), delay)
}

async function pollInstallationStatus() {
  try {
    const response = await setupApi.getStatus()
    serverStatus.value = setupStatusValue(response)
    serverMessage.value = typeof response?.message === 'string' ? response.message : ''
    pollMessage.value = ''
    if (serverStatus.value === 'installed') {
      await leaveInstalledSetup(response)
      return
    }
    if (serverStatus.value === 'recovery_required') {
      finishing.value = false
      clearSetupSecrets()
      return
    }
    scheduleStatusPoll()
  } catch (error) {
    if (error?.status === 404) {
      await goToLogin()
      return
    }
    pollMessage.value = '正式服务切换期间暂时无法读取状态，正在自动重试...'
    scheduleStatusPoll(3000)
  }
}

async function refreshInitialStatus() {
  statusController?.abort()
  statusController = new AbortController()
  initialLoading.value = true
  clearMessages()
  try {
    const response = await setupApi.getStatus({ signal: statusController.signal })
    serverStatus.value = setupStatusValue(response)
    serverMessage.value = typeof response?.message === 'string' ? response.message : ''
    if (serverStatus.value === 'installed') {
      await leaveInstalledSetup(response)
      return
    }
    if (serverStatus.value === 'configuring' || serverStatus.value === 'finalizing') {
      finishing.value = true
      scheduleStatusPoll(500)
    }
    if (!serverStatus.value) errorMessage.value = '安装服务返回了未知状态，请在服务器终端检查服务状态'
  } catch (error) {
    if (error?.name === 'AbortError') return
    if (error?.status === 404) {
      await goToLogin()
      return
    }
    errorMessage.value = error?.message || '暂时无法读取安装服务状态'
    requestId.value = error?.requestId || ''
  } finally {
    initialLoading.value = false
  }
}

async function openSetupSession() {
  if (busy.value || !setupCode.value.trim()) return
  busy.value = true
  clearMessages()
  try {
    const response = await setupApi.createSession(setupCode.value)
    setupCode.value = ''
    sessionExpiresAt.value = response?.expires_at || ''
    step.value = 2
  } catch (error) {
    setupCode.value = ''
    errorMessage.value = error?.message || '安装码验证失败'
    requestId.value = error?.requestId || ''
  } finally {
    busy.value = false
  }
}

function nextStep() {
  clearMessages()
  const error = validateSetupStep(step.value, payload.value)
  if (error) {
    errorMessage.value = error
    return
  }
  step.value = Math.min(5, step.value + 1)
}

function previousStep() {
  clearMessages()
  step.value = Math.max(2, step.value - 1)
}

async function completeInstallation() {
  if (busy.value) return
  clearMessages()
  const validationError = validateSetupStep(5, payload.value)
  if (validationError) {
    errorMessage.value = validationError
    return
  }

  busy.value = true
  try {
    const response = await setupApi.complete(payload.value)
    serverStatus.value = setupStatusValue(response) || 'finalizing'
    clearFormSecrets()
    sessionExpiresAt.value = ''
    finishing.value = true
    scheduleStatusPoll(500)
  } catch (error) {
    errorMessage.value = error?.message || '安装配置提交失败'
    requestId.value = error?.requestId || ''
    if (error?.status === 401 || error?.status === 403 || error?.code === 'setup_session_missing') {
      errorMessage.value += '。临时安装会话已经失效，请在服务器终端生成新的安装码'
      clearFormSecrets()
    }
  } finally {
    busy.value = false
  }
}

onMounted(() => void refreshInitialStatus())
onUnmounted(() => {
  statusController?.abort()
  if (pollTimer) clearTimeout(pollTimer)
  clearSetupSecrets()
  clearFormSecrets()
})
</script>

<style scoped>
.form-control {
  width: 100%;
  border-radius: 0.5rem;
  border-width: 1px;
  border-color: #232f48;
  background-color: #070a13;
  padding: 0.625rem 0.75rem;
  font-size: 0.875rem;
  color: #e2e8f0;
  transition: border-color 150ms ease;
}

.form-control:focus {
  border-color: #10b981;
  outline: none;
}

</style>
