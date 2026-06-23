<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { clearToken, isAuthed } from '@/api/client'
import { elementLocale, LOCALES, setLocale } from '@/i18n'

const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()

const showNav = computed(() => isAuthed() && route.path !== '/login')
// Element Plus component strings follow the same selected locale (one source of
// truth) — bound to <el-config-provider> below.
const elLocale = computed(() => elementLocale(locale.value))
const currentName = computed(
  () => LOCALES.find((l) => l.code === locale.value)?.nativeName ?? 'English',
)

function logout() {
  clearToken()
  router.push('/login')
}
</script>

<template>
  <el-config-provider :locale="elLocale">
    <el-container style="height: 100vh">
      <el-header v-if="showNav" class="vb-header">
        <div class="vb-brand">VeilBridge</div>
        <el-menu mode="horizontal" :router="true" :default-active="route.path" class="vb-menu">
          <el-menu-item index="/">{{ t('nav.dashboard') }}</el-menu-item>
          <el-menu-item index="/nodes">{{ t('nav.nodes') }}</el-menu-item>
          <el-menu-item index="/routes">{{ t('nav.routing') }}</el-menu-item>
        </el-menu>
        <el-dropdown trigger="click" @command="setLocale">
          <span class="vb-lang">{{ currentName }} ▾</span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item
                v-for="l in LOCALES"
                :key="l.code"
                :command="l.code"
                :disabled="l.code === locale"
              >
                {{ l.nativeName }}
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <el-button link @click="logout">{{ t('nav.logout') }}</el-button>
      </el-header>
      <el-main>
        <router-view />
      </el-main>
    </el-container>
  </el-config-provider>
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
.vb-lang {
  cursor: pointer;
  color: var(--el-text-color-regular);
}
</style>
