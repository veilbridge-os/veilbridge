<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'
import { computed } from 'vue'
import { clearToken, isAuthed } from './api/client'

const route = useRoute()
const router = useRouter()
const showNav = computed(() => isAuthed() && route.path !== '/login')

function logout() {
  clearToken()
  router.push('/login')
}
</script>

<template>
  <el-container style="height: 100vh">
    <el-header v-if="showNav" class="vb-header">
      <div class="vb-brand">VeilBridge</div>
      <el-menu mode="horizontal" :router="true" :default-active="route.path" class="vb-menu">
        <el-menu-item index="/">Dashboard</el-menu-item>
        <el-menu-item index="/nodes">Nodes</el-menu-item>
        <el-menu-item index="/routes">Routing</el-menu-item>
      </el-menu>
      <el-button link @click="logout">Logout</el-button>
    </el-header>
    <el-main>
      <router-view />
    </el-main>
  </el-container>
</template>

<style>
.vb-header {
  display: flex;
  align-items: center;
  gap: 24px;
  border-bottom: 1px solid var(--el-border-color);
}
.vb-brand {
  font-weight: 700;
  font-size: 18px;
}
.vb-menu {
  flex: 1;
  border-bottom: none;
}
</style>
