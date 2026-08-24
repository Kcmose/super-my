import { request } from './client.js'

function queryString(values) {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined && value !== null && value !== '') query.set(key, String(value))
  }
  const encoded = query.toString()
  return encoded ? `?${encoded}` : ''
}

export const panelApi = {
  getNodes: ({ limit, cursor, status, signal } = {}) => request(
    `/api/v1/panel/nodes${queryString({ limit, cursor, status })}`,
    { signal },
  ),
}
