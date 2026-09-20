<script setup lang="ts">
// Root: the login screen renders bare, everything else inside the shell.
// Keeping the split here (rather than inside the shell) means the login page
// never starts the live subscription — there is no token to open it with.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { isAuthed } from '@/api/client'
import AppShell from '@/components/AppShell.vue'
import { elementLocale } from '@/i18n'

const route = useRoute()
const { locale } = useI18n()

const inShell = computed(() => isAuthed() && route.path !== '/login')
// Element Plus component strings follow the same selected locale (one source
// of truth) — bound to <el-config-provider> below.
const elLocale = computed(() => elementLocale(locale.value))
</script>

<template>
  <el-config-provider :locale="elLocale">
    <AppShell v-if="inShell" />
    <div v-else class="vb-bare">
      <router-view />
    </div>
  </el-config-provider>
</template>

<style>
html,
body,
#app {
  height: 100%;
  margin: 0;
}
body {
  background: var(--el-fill-color-light);
}
.vb-bare {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
}
</style>
