import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { requiresResolvedAuth } from '../utils/access'

const routes = [
  { path: '/', redirect: '/admin/nodes' },
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

router.beforeEach(async (to) => {
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
