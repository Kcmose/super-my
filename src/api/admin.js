import { request } from './client.js'

function queryString(values) {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined && value !== null && value !== '') query.set(key, String(value))
  }
  const encoded = query.toString()
  return encoded ? `?${encoded}` : ''
}

export const adminApi = {
  // 系统只读状态
  getSystemStatus: ({ signal } = {}) => request('/api/v1/admin/system/status', { signal }),

  // 节点与凭证管理
  createNode: (data) => request('/api/v1/admin/nodes', {
    method: 'POST',
    body: JSON.stringify(data),
  }),
  updateNode: (nodeId, data) => request(`/api/v1/admin/nodes/${encodeURIComponent(nodeId)}`, {
    method: 'PATCH',
    body: JSON.stringify(data),
  }),
  createEnrollmentToken: (nodeId, { expiresInSeconds = 900 } = {}) => request(
    `/api/v1/admin/nodes/${encodeURIComponent(nodeId)}/enrollment-token`,
    {
      method: 'POST',
      body: JSON.stringify({ expires_in_seconds: expiresInSeconds }),
    },
  ),
  rotateToken: (nodeId) => request(`/api/v1/admin/nodes/${encodeURIComponent(nodeId)}/rotate-token`, { method: 'POST' }),
  revokeToken: (nodeId) => request(`/api/v1/admin/nodes/${encodeURIComponent(nodeId)}/revoke-token`, { method: 'POST' }),
  deleteNode: (nodeId) => request(`/api/v1/admin/nodes/${encodeURIComponent(nodeId)}`, { method: 'DELETE' }),

  // 探测目标配置管理
  getProbeTargets: ({ nodeId, limit = 100, cursor, signal } = {}) => request(
    `/api/v1/admin/probe-targets${queryString({ node_id: nodeId, limit, cursor })}`,
    { signal },
  ),
  createProbeTarget: (data) => request('/api/v1/admin/probe-targets', {
    method: 'POST',
    body: JSON.stringify(data)
  }),
  updateProbeTarget: (targetId, data) => request(`/api/v1/admin/probe-targets/${encodeURIComponent(targetId)}`, {
    method: 'PATCH',
    body: JSON.stringify(data)
  }),
  deleteProbeTarget: (targetId) => request(`/api/v1/admin/probe-targets/${encodeURIComponent(targetId)}`, {
    method: 'DELETE'
  }),

  // 用户与角色管理
  getUsers: ({ limit = 50, cursor, signal } = {}) => request(
    `/api/v1/admin/users${queryString({ limit, cursor })}`,
    { signal },
  ),
  createUser: (data) => request('/api/v1/admin/users', {
    method: 'POST',
    body: JSON.stringify(data),
  }),
  updateUser: (userId, data) => request(`/api/v1/admin/users/${encodeURIComponent(userId)}`, {
    method: 'PATCH',
    body: JSON.stringify(data),
  }),
  deleteUser: (userId) => request(`/api/v1/admin/users/${encodeURIComponent(userId)}`, {
    method: 'DELETE',
  }),

  // 审计日志
  getAuditLogs: ({ limit = 50, cursor, action, from, to, signal } = {}) => request(
    `/api/v1/admin/audit-logs${queryString({ limit, cursor, action, from, to })}`,
    { signal },
  ),
}
