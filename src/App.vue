<template>
  <router-view v-if="route.name === 'Login' || route.name === 'Install' || authStore.isAdmin" />
</template>

<script setup>
import { onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from './stores/auth'

const authStore = useAuthStore()
const route = useRoute()
const router = useRouter()

function handleUnauthorized() {
  authStore.clearAuth()
  if (router.currentRoute.value.name !== 'Login') {
    router.replace({ name: 'Login', query: { reason: 'expired' } })
  }
}

onMounted(() => window.addEventListener('probe:unauthorized', handleUnauthorized))
onUnmounted(() => window.removeEventListener('probe:unauthorized', handleUnauthorized))
</script>
