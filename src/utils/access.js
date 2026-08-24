export function requiresResolvedAuth(meta = {}) {
  return meta.requiresAuth === true || meta.requiresAdmin === true || meta.guestOnly === true
}
