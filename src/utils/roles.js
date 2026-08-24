const USER_ROLE_LABELS = Object.freeze({
  viewer: '已废弃角色',
  admin: '管理员',
})

export function userRoleLabel(role) {
  return USER_ROLE_LABELS[role] || '未知角色'
}

export function isAdminUser(user) {
  return user?.enabled === true && user?.role === 'admin'
}
