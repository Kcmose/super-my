import { request } from './client.js'

export const authApi = {
  getAccessStatus: ({ signal } = {}) => request('/api/v1/auth/access', { signal }),
  login: (username, password) => request('/api/v1/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username, password })
  }),
  logout: () => request('/api/v1/auth/logout', { method: 'POST' }),
  getCurrentUser: () => request('/api/v1/auth/me')
}
