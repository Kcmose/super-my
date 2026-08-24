import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { setupApi } from '../api/setup'
import { requiresResolvedAuth } from '../utils/access'
import { isSetupInstallStatus, setupStatusValue } from '../utils/setup'

const routes = [
  { path: '/', redirect: '/admin/nodes' },
  {
    path: '/install',
    name: 'Install',
    component: () => import('../views/Install.vue'),
    meta: { setupOnly: true },
  },
  {
    path: '/login',
    name: 'Login',
    component: () => import('../views/Login.vue'),
    meta: { guestOnly: true },
  },
  {
    path: '/admin/nodes',
    name: 'NodeAdmin',
    component: () => import('../views/admin/NodeTokens.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/probes',
    name: 'ProbeAdmin',
    component: () => import('../views/admin/ProbeTargets.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/users',
    name: 'UserAdmin',
    component: () => import('../views/admin/Users.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/audit',
    name: 'AuditLogs',
    component: () => import('../views/admin/AuditLogs.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
  {
    path: '/admin/system',
    name: 'SystemStatus',
    component: () => import('../views/admin/SystemStatus.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

let installedSetupState = false
let setupStatusPromise = null

async function resolveSetupStatus() {
  if (installedSetupState) return 'installed'
  if (setupStatusPromise) return setupStatusPromise

  setupStatusPromise = (async () => {
    try {
      const status = setupStatusValue(await setupApi.getStatus())
      if (status === 'installed') installedSetupState = true
      return status
    } catch (error) {
      if (error?.status === 404) {
        installedSetupState = true
        return 'installed'
      }
      return ''
    } finally {
      setupStatusPromise = null
    }
  })()
  return setupStatusPromise
}

router.beforeEach(async (to) => {
  const setupStatus = await resolveSetupStatus()
  if (isSetupInstallStatus(setupStatus) && to.name !== 'Install') return { name: 'Install' }
  if (setupStatus === 'installed' && to.name === 'Install') return { name: 'Login' }

  const authStore = useAuthStore()
  if (to.matched.length === 0) {
    if (!authStore.initialized) await authStore.checkAuth()
    return { name: authStore.isAuthenticated ? 'NodeAdmin' : 'Login' }
  }
  if (!authStore.initialized && requiresResolvedAuth(to.meta)) await authStore.checkAuth()

  if (to.meta.requiresAuth && !authStore.isAuthenticated) {
    return { name: 'Login', query: { redirect: to.fullPath } }
  }
  if (to.meta.guestOnly && authStore.isAuthenticated) return { name: 'NodeAdmin' }
  if (to.meta.requiresAdmin && !authStore.isAdmin) {
    return { name: 'Login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
