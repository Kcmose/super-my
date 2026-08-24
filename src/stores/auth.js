import { defineStore } from 'pinia'
import { authApi } from '../api/auth'
import { clearCsrfToken, setCsrfToken } from '../api/client'
import { isAdminUser } from '../utils/roles'

let checkPromise = null

export const useAuthStore = defineStore('auth', {
  state: () => ({
    user: null,
    initialized: false,
    loading: true,
    startupError: '',
  }),
  getters: {
    isAuthenticated: (state) => isAdminUser(state.user),
    isAdmin: (state) => isAdminUser(state.user),
    currentUsername: (state) => state.user?.username || '',
  },
  actions: {
    applyAuth(response) {
      this.user = response?.user || null
      setCsrfToken(response?.csrf_token || '')
      this.startupError = ''
    },
    clearAuth() {
      this.user = null
      clearCsrfToken()
      this.startupError = ''
    },
    async checkAuth({ force = false } = {}) {
      if (this.initialized && !force) return this.isAuthenticated
      if (checkPromise) return checkPromise

      this.loading = true
      checkPromise = (async () => {
        try {
          this.applyAuth(await authApi.getCurrentUser())
          if (!this.isAdmin) this.clearAuth()
          return this.isAdmin
        } catch (error) {
          this.clearAuth()
          if (error?.status !== 401) {
            this.startupError = error?.message || '暂时无法确认管理员登录状态'
          }
          return false
        } finally {
          this.initialized = true
          this.loading = false
          checkPromise = null
        }
      })()
      return checkPromise
    },
    async login(username, password) {
      this.applyAuth(await authApi.login(username, password))
      this.initialized = true
      if (!this.isAdmin) {
        try {
          await authApi.logout()
        } catch {
          // 服务端仍会对管理接口复核 admin 角色。
        }
        this.clearAuth()
        throw new Error('仅管理员账号可以登录')
      }
      return this.user
    },
    async logout() {
      try {
        await authApi.logout()
      } catch (error) {
        if (error?.status !== 401) throw error
      }
      this.clearAuth()
      this.initialized = true
    },
  },
})
