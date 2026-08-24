<template>
  <div class="min-h-screen flex flex-col bg-dark-950 text-slate-200 custom-scrollbar">
    <PanelHeader>
      <template #summary>
        <div class="flex items-center gap-2 text-slate-400 font-mono text-[11px]">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>
          <span>{{ users.length }} 个已加载管理员账号</span>
          <span class="text-slate-600">•</span>
          <span>仅管理员账号</span>
        </div>
      </template>
      <template #actions>
        <router-link to="/admin/nodes" class="px-3 py-1.5 bg-dark-800 border border-dark-700/60 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition">管理首页</router-link>
      </template>
    </PanelHeader>

    <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6 flex-1 w-full space-y-6">
      <section class="p-4 bg-dark-900 border border-dark-700/80 rounded-xl flex flex-col sm:flex-row sm:items-center justify-between gap-4 shadow-lg">
        <div>
          <h1 class="text-sm font-semibold text-slate-100">管理员账号管理</h1>
          <p class="text-xs text-slate-400 mt-1">此处只创建、更新和管理可登录的管理员账号。</p>
        </div>
        <div class="flex items-center gap-2">
          <button type="button" :disabled="loading || loadingMore" class="px-3 py-2 bg-dark-800 border border-dark-700 text-slate-300 hover:text-emerald-400 rounded-lg text-xs transition disabled:opacity-50" @click="loadUsers()">刷新</button>
          <button type="button" :disabled="loading" class="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 text-white rounded-lg text-xs font-medium transition disabled:opacity-50" @click="openCreate">+ 新建管理员</button>
        </div>
      </section>

      <div v-if="errorMessage" role="alert" class="p-3 bg-rose-500/10 border border-rose-500/30 rounded-xl text-xs text-rose-300">{{ errorMessage }}</div>
      <div v-if="successMessage" role="status" class="p-3 bg-emerald-500/10 border border-emerald-500/30 rounded-xl text-xs text-emerald-300">{{ successMessage }}</div>

      <section class="bg-dark-900 border border-dark-700/80 rounded-xl overflow-hidden shadow-lg">
        <div class="overflow-x-auto custom-scrollbar">
          <table class="w-full min-w-[880px] text-left text-xs text-slate-300">
            <thead class="bg-dark-950 text-slate-400 uppercase font-semibold border-b border-dark-700/80">
              <tr>
                <th class="py-3 px-4">状态</th>
                <th class="py-3 px-4">用户名</th>
                <th class="py-3 px-4">账号类型</th>
                <th class="py-3 px-4">最后登录</th>
                <th class="py-3 px-4">创建 / 更新</th>
                <th class="py-3 px-4 text-right">操作</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-dark-700/60 font-mono">
              <tr v-if="loading"><td colspan="6" class="py-14 px-4 text-center text-slate-500">正在读取管理员账号...</td></tr>
              <tr v-else-if="users.length === 0"><td colspan="6" class="py-14 px-4 text-center text-slate-500">暂无管理员账号</td></tr>
              <template v-else>
                <tr v-for="user in users" :key="user.user_id" class="hover:bg-dark-800/50 transition">
                  <td class="py-3 px-4"><span class="px-2 py-0.5 rounded text-[10px] border" :class="user.enabled ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20' : 'bg-slate-700/50 text-slate-400 border-slate-600/50'">{{ user.enabled ? '已启用' : '已禁用' }}</span></td>
                  <td class="py-3 px-4">
                    <div class="font-sans font-medium text-slate-200">{{ user.username }} <span v-if="isCurrentUser(user)" class="ml-1 text-[10px] text-emerald-400">当前用户</span></div>
                    <div class="text-[10px] text-slate-500 mt-0.5">{{ user.user_id }}</div>
                  </td>
                  <td class="py-3 px-4"><span class="px-2 py-0.5 rounded text-[10px] font-bold bg-amber-500/10 text-amber-400">管理员</span></td>
                  <td class="py-3 px-4 text-[11px] text-slate-400">{{ formatUTCDateTime(user.last_login_at) }}</td>
                  <td class="py-3 px-4 text-[10px] leading-5 text-slate-400"><div>{{ formatUTCDateTime(user.created_at) }}</div><div>{{ formatUTCDateTime(user.updated_at) }}</div></td>
                  <td class="py-3 px-4 text-right font-sans whitespace-nowrap">
                    <button type="button" :disabled="busyUserId === user.user_id" class="text-slate-300 hover:text-emerald-400 disabled:opacity-40" @click="openEdit(user)">编辑</button>
                    <button type="button" :disabled="busyUserId === user.user_id" class="ml-3 text-rose-400 hover:text-rose-300 disabled:opacity-40" @click="removeUser(user)">删除</button>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
        <div v-if="nextCursor" class="p-3 border-t border-dark-700/80 flex justify-center">
          <button type="button" :disabled="loadingMore" class="px-4 py-2 bg-dark-800 border border-dark-700 text-slate-300 hover:text-emerald-400 rounded-lg text-xs disabled:opacity-50" @click="loadUsers({ append: true })">{{ loadingMore ? '加载中...' : '加载更多管理员' }}</button>
        </div>
      </section>
    </main>

    <PanelFooter />

    <div v-if="showModal" class="fixed inset-0 bg-black/70 flex items-center justify-center p-4 z-50 backdrop-blur-sm" @click.self="closeModal">
      <section class="bg-dark-800 border border-dark-700 rounded-xl max-w-lg w-full p-6 shadow-2xl" role="dialog" aria-modal="true" :aria-label="modalTitle">
        <div class="flex items-start justify-between gap-4 mb-5">
          <div><h2 class="font-bold text-sm text-slate-100">{{ modalTitle }}</h2><p class="text-[11px] text-slate-400 mt-1">密码或账号状态变更可能立即撤销该管理员现有 Session。</p></div>
          <button type="button" :disabled="saving" class="p-1.5 bg-dark-900 text-slate-400 hover:text-white rounded-lg disabled:opacity-50" aria-label="关闭" @click="closeModal">✕</button>
        </div>
        <form class="space-y-4 text-xs" @submit.prevent="saveUser">
          <div v-if="modalError" role="alert" class="p-3 bg-rose-500/10 border border-rose-500/30 rounded-lg text-rose-300">{{ modalError }}</div>
          <div>
            <label for="user-name" class="block text-slate-300 mb-1">管理员账号</label>
            <input id="user-name" v-model.trim="userForm.username" required maxlength="128" autocomplete="username" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 focus:outline-none focus:border-emerald-500" placeholder="请输入管理员账号">
          </div>
          <div>
            <label for="user-password" class="block text-slate-300 mb-1">{{ editingUser ? '新密码（留空则不修改）' : '初始密码' }}</label>
            <input id="user-password" v-model="userForm.password" type="password" :required="!editingUser" maxlength="1024" autocomplete="new-password" class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-200 focus:outline-none focus:border-emerald-500" placeholder="至少 12 个 UTF-8 字节">
          </div>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <span class="block text-slate-300 mb-1">账号类型</span>
              <div class="w-full px-3 py-2 bg-dark-900 border border-dark-700 rounded-lg text-amber-300">管理员</div>
            </div>
            <label class="flex items-center gap-2 mt-5 p-2 bg-dark-900 border border-dark-700 rounded-lg text-slate-300 cursor-pointer"><input v-model="userForm.enabled" type="checkbox" class="accent-emerald-500"><span>启用账号</span></label>
          </div>
          <div class="p-3 bg-amber-500/10 border border-amber-500/20 rounded-lg text-[11px] text-amber-200">最后一个可用管理员无法被删除或停用；API 会强制执行该保护。</div>
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
import { useAuthStore } from '../../stores/auth'
import { MAX_ADMIN_PAGES, formatUTCDateTime, normalizeUserPayload, validateUserPayload } from '../../utils/admin'

const authStore = useAuthStore()
const users = ref([])
const loading = ref(true)
const loadingMore = ref(false)
const nextCursor = ref(null)
const errorMessage = ref('')
const successMessage = ref('')
const showModal = ref(false)
const editingUser = ref(null)
const userForm = ref(emptyUserForm())
const modalError = ref('')
const saving = ref(false)
const busyUserId = ref('')

let mounted = false
let requestGeneration = 0
let requestController = null
let pageCount = 0
let seenCursors = new Set()

const modalTitle = computed(() => editingUser.value ? '编辑管理员' : '新建管理员')

function emptyUserForm() {
  return { username: '', password: '', enabled: true }
}

function isCurrentUser(user) {
  return user.user_id === authStore.user?.user_id
}

async function loadUsers({ append = false } = {}) {
  if (append && (!nextCursor.value || loadingMore.value)) return
  const generation = ++requestGeneration
  requestController?.abort()
  requestController = new AbortController()
  if (append) loadingMore.value = true
  else {
    loading.value = true
    pageCount = 0
    seenCursors = new Set()
  }
  errorMessage.value = ''

  try {
    const cursor = append ? nextCursor.value : undefined
    const response = await adminApi.getUsers({ limit: 50, cursor, signal: requestController.signal })
    if (!mounted || generation !== requestGeneration) return

    const following = response?.next_cursor || null
    if (following && seenCursors.has(following)) throw new Error('服务端返回了重复分页游标')
    if (following && pageCount + 1 >= MAX_ADMIN_PAGES) throw new Error(`管理员分页超过安全上限 ${MAX_ADMIN_PAGES}`)
    const loadedUsers = Array.isArray(response?.users)
      ? response.users.filter((user) => user?.role === 'admin')
      : []
    users.value = append ? [...users.value, ...loadedUsers] : loadedUsers
    pageCount += 1
    if (following) seenCursors.add(following)
    nextCursor.value = following
  } catch (error) {
    if (error?.name === 'AbortError' || generation !== requestGeneration) return
    errorMessage.value = error?.message || '管理员账号读取失败'
  } finally {
    if (mounted && generation === requestGeneration) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

function openCreate() {
  editingUser.value = null
  userForm.value = emptyUserForm()
  modalError.value = ''
  showModal.value = true
}

function openEdit(user) {
  editingUser.value = user
  userForm.value = { username: user.username, password: '', enabled: user.enabled }
  modalError.value = ''
  showModal.value = true
}

function closeModal() {
  if (saving.value) return
  showModal.value = false
  editingUser.value = null
  userForm.value = emptyUserForm()
  modalError.value = ''
}

async function saveUser() {
  const editing = Boolean(editingUser.value)
  const payload = normalizeUserPayload(userForm.value, editing)
  const validationError = validateUserPayload(payload, editing)
  if (validationError) {
    modalError.value = validationError
    return
  }

  saving.value = true
  modalError.value = ''
  const currentUserChanged = editing && isCurrentUser(editingUser.value)
  try {
    if (editing) await adminApi.updateUser(editingUser.value.user_id, payload)
    else await adminApi.createUser(payload)
    successMessage.value = editing ? '管理员设置已更新。' : '管理员已创建。'
    showModal.value = false
    editingUser.value = null
    userForm.value = emptyUserForm()
    if (currentUserChanged) await authStore.checkAuth({ force: true })
    else await loadUsers()
  } catch (error) {
    modalError.value = error?.message || '管理员保存失败'
  } finally {
    userForm.value.password = ''
    saving.value = false
  }
}

async function removeUser(user) {
  if (!window.confirm(`确认删除管理员“${user.username}”？该账号的全部 Session 将立即失效。`)) return
  busyUserId.value = user.user_id
  errorMessage.value = ''
  successMessage.value = ''
  try {
    await adminApi.deleteUser(user.user_id)
    successMessage.value = '管理员账号及其 Session 已删除。'
    if (isCurrentUser(user)) await authStore.checkAuth({ force: true })
    else await loadUsers()
  } catch (error) {
    errorMessage.value = error?.message || '管理员删除失败'
  } finally {
    busyUserId.value = ''
  }
}

onMounted(() => {
  mounted = true
  void loadUsers()
})

onUnmounted(() => {
  mounted = false
  requestGeneration += 1
  requestController?.abort()
  userForm.value = emptyUserForm()
})
</script>
