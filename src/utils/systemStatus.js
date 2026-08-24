export function systemHeaderState(status, loading, errorMessage) {
  if (status?.status === 'ready') return { label: 'API 与数据库就绪', tone: 'ready' }
  if (status) return { label: '系统处于降级状态', tone: 'degraded' }
  if (errorMessage) return { label: '系统状态读取失败', tone: 'error' }
  if (loading) return { label: '正在读取系统状态', tone: 'loading' }
  return { label: '系统状态未知', tone: 'unknown' }
}

export function systemOverallLabel(status, loading, errorMessage) {
  if (status?.status === 'ready') return '正常'
  if (status) return '降级'
  if (errorMessage) return '读取失败'
  if (loading) return '读取中'
  return '未知'
}

export function systemOverallTone(status, errorMessage) {
  if (status?.status === 'ready') return 'ready'
  if (status) return 'degraded'
  if (errorMessage) return 'error'
  return 'unknown'
}

export function systemDatabaseLabel(status, loading, errorMessage) {
  if (status?.database?.status === 'ready') return '就绪'
  if (status) return '不可用'
  if (errorMessage) return '未知'
  if (loading) return '读取中'
  return '未知'
}
