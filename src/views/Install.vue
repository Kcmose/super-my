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
              <h1 class="theme-heading text-xl font-extrabold text-white tracking-tight">PROBE 管理端安全安装向导</h1>
              <p class="mt-1 text-xs text-slate-400">仅通过 root SSH 转发的服务器私有 Unix Socket 完成首次初始化</p>
            </div>
          </div>
        </header>

        <div class="px-5 py-5 sm:px-8">
          <div class="mb-5 flex items-start gap-2 rounded-lg border border-dark-700 bg-dark-900/80 p-3 text-xs text-slate-300">
            <span class="font-bold" :class="statusDotClass">●</span>
            <div>
              <div class="font-medium">服务状态：{{ statusLabel }}</div>
              <div v-if="sessionExpiresLabel" class="mt-0.5 font-mono text-[11px] text-slate-500">临时安装会话到期：{{ sessionExpiresLabel }}</div>
              <div v-else class="mt-0.5 text-[11px] text-slate-500">数据库密码和管理员密码只保存在当前页面内存中。</div>
            </div>
          </div>

          <ol v-if="!isRecovery" class="mb-7 grid grid-cols-4 gap-1" aria-label="安装进度">
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
            正在确认服务器安装状态并建立安全会话...
          </div>

          <div v-else-if="installedIPResult" class="rounded-lg border p-5" :class="installedIPResult.confirmed ? 'border-emerald-500/30 bg-emerald-500/10' : 'border-amber-500/30 bg-amber-500/10'" aria-live="polite">
            <div class="flex items-start gap-3">
              <div class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full" :class="installedIPResult.confirmed ? 'bg-emerald-500/15 text-emerald-400' : 'bg-amber-500/15 text-amber-300'">{{ installedIPResult.confirmed ? '✓' : '!' }}</div>
              <div>
                <h2 class="text-sm font-bold" :class="installedIPResult.confirmed ? 'text-emerald-400' : 'text-amber-300'">{{ installedIPResult.confirmed ? (installedIPResult.modeKnown ? 'IP 模式安装完成' : '安装完成，交接信息不可用') : '临时安装服务已关闭，请通过 SSH 确认结果' }}</h2>
                <p class="mt-1 text-xs leading-5 text-slate-300">{{ installedIPResult.confirmed ? '正式服务已经完成部署。' : '页面没有收到明确的 installed 或 recovery_required 终态，因此不会推断安装成功。' }} {{ installedIPResult.modeKnown && installedIPResult.confirmed ? '请先核对并信任私有 CA，再由你主动进入管理面板；页面不会自动绕过浏览器的 TLS 校验。' : '请按下方 SSH 说明核对正式状态，不要猜测或绕过证书警告。' }}</p>
              </div>
            </div>

            <dl v-if="installedIPResult.confirmed && installedIPResult.access" class="mt-5 grid gap-3 text-xs sm:grid-cols-1">
              <div class="rounded-lg border border-dark-700 bg-dark-900/80 p-3">
                <dt class="text-slate-500">管理面板</dt>
                <dd class="mt-1 break-all font-mono text-slate-300">{{ installedIPResult.access.admin_url }}</dd>
              </div>
            </dl>
            <div v-else class="mt-5 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-xs leading-5 text-amber-300">
              临时安装服务未能安全重建正式入口。请通过 root SSH 检查 <span class="font-mono">/srv/probe/config/probe-api.env</span>，不要猜测或手工绕过证书警告。
            </div>

            <div v-if="installedIPResult.confirmed && installedIPResult.privateCA.available" class="mt-4 rounded-lg border border-dark-700 bg-dark-900/80 p-4">
              <div class="text-xs font-medium text-slate-300">私有 CA 文件 SHA-256（整个 PEM 文件字节）</div>
              <div class="mt-2 break-all font-mono text-[11px] leading-5 text-emerald-400">{{ installedIPResult.privateCA.sha256 }}</div>
              <p class="mt-2 text-[11px] leading-5 text-slate-400">CA 只保存在当前页面内存中。下载后先用 <span class="font-mono">sha256sum probe-panel-ca.pem</span> 核对以上 64 位小写指纹，再导入操作系统或浏览器的受信任根证书库。</p>
            </div>
            <div v-else-if="installedIPResult.confirmed && installedIPResult.modeKnown" class="mt-4 rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 text-xs leading-5 text-amber-300">
              正式部署已经成功，但临时服务无法安全读取 CA 文件，因此没有在响应中提供 CA 或指纹。请使用下面的 SSH/scp 方式取回文件并在服务器与本机分别计算 SHA-256 后核对。
            </div>
            <div v-else class="mt-4 rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 text-xs leading-5 text-amber-300">
              临时服务关闭前没有取得足够的安全交接信息。请通过 root SSH 核对正式服务和公开入口配置；不要输出、复制或发送完整环境文件，因为其中包含数据库凭据。
            </div>

            <div class="mt-5 flex flex-wrap gap-3">
              <button v-if="installedIPResult.confirmed && installedIPResult.privateCA.available" type="button" class="rounded-lg bg-emerald-600 px-4 py-2 text-xs font-medium text-white hover:bg-emerald-500 transition" @click="downloadPrivateCA">
                下载 probe-panel-ca.pem
              </button>
              <button type="button" :disabled="!installedIPResult.confirmed || !installedIPResult.access" class="rounded-lg border border-dark-700 bg-dark-800 px-4 py-2 text-xs text-slate-300 hover:text-emerald-400 disabled:cursor-not-allowed disabled:opacity-50 transition" @click="enterInstalledAdmin">
                进入管理面板
              </button>
            </div>

            <div v-if="installedIPResult.modeKnown" class="mt-5 rounded-lg border border-dark-700 bg-dark-900/80 p-4 text-[11px] leading-5 text-slate-400">
              <div class="font-medium text-slate-300">SSH/scp 安全回退</div>
              <p class="mt-1">如果页面下载不可用，在可信终端通过 root SSH 获取固定路径；将 <span class="font-mono">SERVER</span> 替换为实际 SSH 地址，按需加入端口参数。</p>
              <code class="mt-2 block overflow-x-auto whitespace-nowrap rounded bg-dark-950 p-2 text-slate-300">ssh root@SERVER sha256sum /etc/probe-panel/tls/private-ca/ca.pem</code>
              <code class="mt-2 block overflow-x-auto whitespace-nowrap rounded bg-dark-950 p-2 text-slate-300">scp root@SERVER:/etc/probe-panel/tls/private-ca/ca.pem ./probe-panel-ca.pem</code>
              <code class="mt-2 block overflow-x-auto whitespace-nowrap rounded bg-dark-950 p-2 text-slate-300">sha256sum ./probe-panel-ca.pem</code>
            </div>
            <div v-else class="mt-5 rounded-lg border border-dark-700 bg-dark-900/80 p-4 text-[11px] leading-5 text-slate-400">
              <div class="font-medium text-slate-300">root SSH 安全回退</div>
              <p class="mt-1">登录服务器后只筛选公开入口字段，避免显示包含数据库密码的完整环境文件：</p>
              <code class="mt-2 block overflow-x-auto whitespace-nowrap rounded bg-dark-950 p-2 text-slate-300">grep -E '^PROBE_(INGRESS_MODE|ADMIN_ORIGIN)=' /srv/probe/config/probe-api.env</code>
              <code class="mt-2 block overflow-x-auto whitespace-nowrap rounded bg-dark-950 p-2 text-slate-300">systemctl status probe-api nginx --no-pager</code>
            </div>
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
              <p class="mt-2 text-xs leading-5 text-slate-300">系统正在创建数据库、配置管理入口、执行迁移并验证正式服务。请保持此页面打开；域名模式完成后进入登录页，IP 模式会先显示私有 CA 下载与核验步骤。</p>
            <p v-if="pollMessage" class="mt-3 text-[11px] text-amber-400">{{ pollMessage }}</p>
          </div>

          <div v-else-if="!sessionReady" class="rounded-lg border border-amber-500/30 bg-amber-500/10 p-5" role="alert">
            <h2 class="text-sm font-bold text-amber-300">暂时无法建立安装会话</h2>
            <p class="mt-2 text-xs leading-5 text-slate-300">请确认 root SSH 本地转发仍然连接，然后重新尝试。页面不会把会话或凭据写入浏览器存储。</p>
            <p v-if="errorMessage" class="mt-3 rounded bg-dark-900/80 p-3 text-[11px] text-rose-400">{{ errorMessage }}</p>
            <button type="button" :disabled="busy" class="mt-4 rounded-lg border border-dark-700 bg-dark-800 px-3 py-2 text-xs text-slate-300 hover:text-emerald-400 disabled:opacity-60 transition" @click="retrySetupSession">
              {{ busy ? '正在重试...' : '重新建立会话' }}
            </button>
          </div>

          <template v-else>
            <section v-if="step === 1" aria-labelledby="database-title">
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

            <section v-else-if="step === 2" aria-labelledby="network-title">
              <h2 id="network-title" class="theme-heading text-base font-bold text-white">配置域名、证书与访问白名单</h2>
              <p class="mt-1 text-xs leading-5 text-slate-400">管理域名留空时使用服务器 IP、18455 HTTPS 和本机私有 CA；填写管理域名时使用 ACME 公信证书。</p>
              <div class="mt-5 grid gap-4 sm:grid-cols-2">
                <div v-if="isIPMode" class="sm:col-span-2">
                  <label for="server-address" class="mb-1 block text-xs font-medium text-slate-300">服务器 IP</label>
                  <input id="server-address" v-model.trim="form.network.address" type="text" autocomplete="off" maxlength="45" class="form-control font-mono" placeholder="服务器自动检测的 IPv4 或 IPv6" @input="networkAddressOverridden = true" />
                  <p class="mt-1 text-[11px] text-slate-500">默认由服务器检测；使用 NAT 时可覆盖为管理员实际访问的规范 IPv4 或 IPv6。管理端口固定为 18455。</p>
                </div>
                <div>
                  <label for="admin-domain" class="mb-1 block text-xs font-medium text-slate-300">管理面板域名</label>
                  <input id="admin-domain" v-model.trim="form.domains.admin" type="text" autocomplete="url" maxlength="253" class="form-control" placeholder="留空使用 IP，或填写 admin.example.com" />
                </div>
                <div>
                  <label for="acme-email" class="mb-1 block text-xs font-medium text-slate-300">ACME 证书通知邮箱</label>
                  <input id="acme-email" v-model.trim="form.tls.email" type="email" autocomplete="email" maxlength="254" class="form-control disabled:cursor-not-allowed disabled:opacity-50" :disabled="isIPMode" :placeholder="isIPMode ? 'IP 模式不需要 ACME 邮箱' : 'admin@example.com'" />
                </div>
                <div class="sm:col-span-2">
                  <label for="allowlist" class="mb-1 block text-xs font-medium text-slate-300">管理面板访问白名单</label>
                  <textarea id="allowlist" v-model="allowlistText" rows="5" spellcheck="false" class="form-control font-mono" placeholder="203.0.113.25/32&#10;2001:db8:1234::/48"></textarea>
                  <p class="mt-1 text-[11px] text-slate-500">每行一个 IPv4、IPv6 或 CIDR。禁止使用 0.0.0.0/0 和 ::/0。本向导只配置管理面板入口。</p>
                </div>
              </div>
            </section>

            <section v-else-if="step === 3" aria-labelledby="administrator-title">
              <h2 id="administrator-title" class="theme-heading text-base font-bold text-white">创建首个管理员</h2>
              <p class="mt-1 text-xs leading-5 text-slate-400">本次只创建管理端的首个管理员。</p>
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
              <p class="mt-1 text-xs leading-5 text-slate-400">提交后将执行数据库初始化、证书配置、迁移和正式服务切换。完成前不会开放正式面板入口。</p>
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
                  <dt class="text-slate-500">{{ isIPMode ? '默认 IP HTTPS 入口' : '管理域名 HTTPS 入口' }}</dt>
                  <dd class="mt-1 space-y-1 font-mono text-slate-300">
                    <div>{{ effectiveURLs.admin_url }}</div>
                  </dd>
                </div>
                <div class="rounded-lg border border-dark-700 bg-dark-900/80 p-3">
                  <dt class="text-slate-500">TLS</dt>
                  <dd class="mt-1 text-slate-300">{{ isIPMode ? '本机私有 CA · IP SAN' : `ACME · ${payload.tls.email}` }}</dd>
                </div>
                <div class="rounded-lg border border-dark-700 bg-dark-900/80 p-3">
                  <dt class="text-slate-500">访问白名单（{{ payload.allowlist.length }} 项）</dt>
                  <dd class="mt-1 max-h-20 overflow-auto whitespace-pre-wrap font-mono text-slate-300">{{ payload.allowlist.join('\n') }}</dd>
                </div>
              </dl>
              <div class="mt-4 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-[11px] leading-5 text-amber-300">
                {{ isIPMode ? '提交前请确认显示的服务器 IP 可从管理员网络访问；安装后浏览器需要信任本机私有 CA。' : '提交前请确认管理域名 DNS 已生效、服务器公网 80/443 可达。' }} 请保持 SSH 隧道连接。
              </div>
            </section>

            <div v-if="errorMessage" role="alert" class="mt-5 rounded-lg border border-rose-500/30 bg-rose-500/10 p-3 text-xs text-rose-400">
              {{ errorMessage }}
              <span v-if="requestId" class="mt-1 block font-mono text-[10px] text-rose-300/70">请求 ID: {{ requestId }}</span>
            </div>

            <div class="mt-7 flex items-center justify-between gap-3 border-t border-dark-700/60 pt-5">
              <button v-if="step > 1" type="button" :disabled="busy" class="rounded-lg border border-dark-700 bg-dark-800 px-4 py-2 text-xs text-slate-300 hover:text-emerald-400 disabled:opacity-60 transition" @click="previousStep">上一步</button>
              <span v-else></span>
              <button v-if="step < 4" type="button" :disabled="busy" class="rounded-lg bg-emerald-600 px-5 py-2 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-60 transition" @click="nextStep">下一步</button>
              <button v-else type="button" :disabled="busy" class="rounded-lg bg-emerald-600 px-5 py-2 text-xs font-medium text-white shadow-lg shadow-emerald-900/30 hover:bg-emerald-500 disabled:cursor-wait disabled:opacity-60 transition" @click="completeInstallation">
                {{ busy ? '正在提交...' : '确认并开始安装' }}
              </button>
            </div>
          </template>
        </div>
      </section>

      <p class="mt-4 text-center text-[11px] text-slate-500">初始化服务仅通过 root SSH 私有通道访问 · 凭据不会写入浏览器存储</p>
    </main>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import ThemeToggle from '../components/ThemeToggle.vue'
import { clearSetupSecrets, setupApi } from '../api/setup.js'
import {
  normalizeSetupPayload,
  setupDefaultsValue,
  setupInstalledIPAccess,
  setupIPURLs,
  setupPrivateCAValue,
  setupStatusValue,
  setupUsesIPMode,
  validateSetupStep,
} from '../utils/setup.js'

const steps = Object.freeze([
  { number: 1, label: '数据库' },
  { number: 2, label: '入口与证书' },
  { number: 3, label: '管理员' },
  { number: 4, label: '确认' },
])

const router = useRouter()
const step = ref(1)
const allowlistText = ref('')
const busy = ref(false)
const initialLoading = ref(true)
const finishing = ref(false)
const sessionReady = ref(false)
const serverStatus = ref('')
const serverMessage = ref('')
const sessionExpiresAt = ref('')
const setupDefaults = ref(null)
const networkAddressOverridden = ref(false)
const errorMessage = ref('')
const requestId = ref('')
const pollMessage = ref('')
const installedIPResult = ref(null)
const submittedIngressMode = ref('')
let statusController = null
let pollTimer = null

const form = reactive({
  database: {
    name: 'probe',
    username: 'probe',
    password: '',
    password_confirmation: '',
  },
  network: { address: '' },
  domains: {
    admin: '',
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
  network: form.network,
  domains: form.domains,
  tls: form.tls,
  allowlist: allowlistText.value,
  administrator: form.administrator,
}))
const isIPMode = computed(() => setupUsesIPMode(payload.value))
watch(isIPMode, (enabled) => {
  if (enabled) form.tls.email = ''
})
const effectiveURLs = computed(() => {
  if (!isIPMode.value) {
    return { admin_url: payload.value.domains.admin ? `https://${payload.value.domains.admin}` : '尚未填写' }
  }
  if (setupDefaults.value?.server_ip === payload.value.network.address) return setupDefaults.value
  return setupIPURLs(payload.value.network.address) || { admin_url: '服务器 IP 无效' }
})
const isRecovery = computed(() => serverStatus.value === 'recovery_required')
const statusLabel = computed(() => ({
  pending: '正在建立安装会话',
  configuring: '等待提交配置',
  finalizing: '正在激活正式服务',
  installed: '已经安装',
  handoff_unavailable: '终态需要 SSH 确认',
  recovery_required: '需要终端恢复',
}[serverStatus.value] || '正在确认'))
const statusDotClass = computed(() => (
  isRecovery.value ? 'text-rose-400' : finishing.value || serverStatus.value === 'installed' ? 'text-emerald-400' : 'text-amber-400'
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
  form.database.password = ''
  form.database.password_confirmation = ''
  form.administrator.password = ''
  form.administrator.password_confirmation = ''
}

function clearCurrentSession() {
  clearSetupSecrets()
  sessionReady.value = false
  sessionExpiresAt.value = ''
}

function applySetupDefaults(defaults) {
  setupDefaults.value = defaults
  if (!networkAddressOverridden.value) form.network.address = defaults.server_ip
}

function acceptStatusDefaults(response) {
  if (response?.defaults == null) return
  const defaults = setupDefaultsValue(response)
  if (!defaults) {
    const error = new Error('安装服务返回了无效的默认 IP 入口')
    error.code = 'invalid_setup_defaults_response'
    throw error
  }
  applySetupDefaults(defaults)
}

async function establishSetupSession({ signal } = {}) {
  const response = await setupApi.createSession({ signal })
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError')
  applySetupDefaults(response.defaults)
  sessionExpiresAt.value = response.expires_at
  sessionReady.value = true
  serverStatus.value = 'configuring'
}

async function goToLogin() {
  clearCurrentSession()
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
  clearCurrentSession()
  clearFormSecrets()
  const target = installedAdminURL(response)
  if (target) {
    window.location.replace(target)
    return
  }
  await goToLogin()
}

async function handleInstalledStatus(response) {
  const privateCA = setupPrivateCAValue(response)
  if (privateCA === null && response?.handoff_unavailable !== true) {
    await leaveInstalledSetup(response)
    return
  }
  clearCurrentSession()
  clearFormSecrets()
  finishing.value = false
  installedIPResult.value = {
    access: setupInstalledIPAccess(response),
    confirmed: true,
    modeKnown: privateCA !== null,
    privateCA: privateCA || { available: false, pem: '', sha256: '' },
  }
}

function downloadPrivateCA() {
  const privateCA = installedIPResult.value?.privateCA
  if (!privateCA?.available) return
  const blob = new Blob([privateCA.pem], { type: 'application/x-pem-file' })
  const objectURL = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = objectURL
  anchor.download = 'probe-panel-ca.pem'
  anchor.rel = 'noopener'
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  window.setTimeout(() => URL.revokeObjectURL(objectURL), 1000)
}

function enterInstalledAdmin() {
  const target = installedIPResult.value?.access?.login_url
  if (target) window.location.assign(target)
}

function showTerminalHandoffFallback() {
  clearCurrentSession()
  clearFormSecrets()
  finishing.value = false
  serverStatus.value = 'handoff_unavailable'
  installedIPResult.value = {
    access: null,
    confirmed: false,
    modeKnown: submittedIngressMode.value === 'ip',
    privateCA: { available: false, pem: '', sha256: '' },
  }
}

function scheduleStatusPoll(delay = 2000) {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => void pollInstallationStatus(), delay)
}

async function pollInstallationStatus() {
  try {
    const response = await setupApi.getStatus()
    acceptStatusDefaults(response)
    serverStatus.value = setupStatusValue(response)
    serverMessage.value = typeof response?.message === 'string' ? response.message : ''
    pollMessage.value = ''
    if (serverStatus.value === 'installed') {
      await handleInstalledStatus(response)
      return
    }
    if (serverStatus.value === 'recovery_required') {
      finishing.value = false
      clearCurrentSession()
      return
    }
    if (serverStatus.value === 'pending' || serverStatus.value === 'configuring') {
      finishing.value = false
      clearCurrentSession()
      clearFormSecrets()
      try {
        await establishSetupSession()
        errorMessage.value = '服务器预检未通过，尚未接管正式服务。请检查域名解析、端口占用和现有 Nginx 状态，修正配置并重新输入密码后再提交。'
      } catch (sessionError) {
        errorMessage.value = `服务器预检未通过，重新建立安全会话失败：${sessionError?.message || '请检查 SSH 隧道后重试'}`
      }
      return
    }
    scheduleStatusPoll()
  } catch (error) {
    if (error?.status === 404) {
      showTerminalHandoffFallback()
      return
    }
    pollMessage.value = '正式服务切换期间暂时无法读取状态，正在自动重试...'
    scheduleStatusPoll(3000)
  }
}

async function refreshInitialStatus() {
  statusController?.abort()
  const controller = new AbortController()
  statusController = controller
  initialLoading.value = true
  clearMessages()
  try {
    const response = await setupApi.getStatus({ signal: controller.signal })
    if (controller.signal.aborted) return
    acceptStatusDefaults(response)
    serverStatus.value = setupStatusValue(response)
    serverMessage.value = typeof response?.message === 'string' ? response.message : ''
    if (serverStatus.value === 'installed') {
      await handleInstalledStatus(response)
      return
    }
    if (serverStatus.value === 'finalizing') {
      clearCurrentSession()
      finishing.value = true
      scheduleStatusPoll(500)
      return
    }
    if (serverStatus.value === 'recovery_required') {
      clearCurrentSession()
      return
    }
    if (serverStatus.value === 'pending' || serverStatus.value === 'configuring') {
      finishing.value = false
      await establishSetupSession({ signal: controller.signal })
      return
    }
    clearCurrentSession()
    errorMessage.value = '安装服务返回了未知状态，请在服务器终端检查服务状态'
  } catch (error) {
    if (error?.name === 'AbortError') return
    if (error?.status === 404) {
      await goToLogin()
      return
    }
    errorMessage.value = error?.message || '暂时无法读取安装服务状态'
    requestId.value = error?.requestId || ''
    clearCurrentSession()
  } finally {
    if (statusController === controller) initialLoading.value = false
  }
}

async function retrySetupSession() {
  if (busy.value) return
  busy.value = true
  try {
    await refreshInitialStatus()
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
  step.value = Math.min(4, step.value + 1)
}

function previousStep() {
  clearMessages()
  step.value = Math.max(1, step.value - 1)
}

async function completeInstallation() {
  if (busy.value) return
  clearMessages()
  const validationError = validateSetupStep(4, payload.value)
  if (validationError) {
    errorMessage.value = validationError
    return
  }

  busy.value = true
  try {
    submittedIngressMode.value = isIPMode.value ? 'ip' : 'domain'
    const response = await setupApi.complete(payload.value)
    serverStatus.value = setupStatusValue(response) || 'finalizing'
    sessionReady.value = false
    clearFormSecrets()
    sessionExpiresAt.value = ''
    finishing.value = true
    scheduleStatusPoll(500)
  } catch (error) {
    errorMessage.value = error?.message || '安装配置提交失败'
    requestId.value = error?.requestId || ''
    if (error?.status === 401 || error?.status === 403 || error?.code === 'setup_session_missing') {
      clearCurrentSession()
      clearFormSecrets()
      step.value = 1
      try {
        statusController?.abort()
        const controller = new AbortController()
        statusController = controller
        await establishSetupSession({ signal: controller.signal })
        errorMessage.value = '临时安装会话已经失效，已重新建立安全会话。配置没有自动重复提交，请重新输入密码后确认安装'
      } catch (sessionError) {
        errorMessage.value = `临时安装会话已经失效，且重新建立失败：${sessionError?.message || '请检查 SSH 隧道后重试'}`
      }
    }
  } finally {
    busy.value = false
  }
}

onMounted(() => void refreshInitialStatus())
onUnmounted(() => {
  statusController?.abort()
  if (pollTimer) clearTimeout(pollTimer)
  clearCurrentSession()
  clearFormSecrets()
  installedIPResult.value = null
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
