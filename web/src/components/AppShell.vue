<script setup lang="ts">
// The panel shell (M2.1): navigation, header, search, freshness, apply bar.
//
// Two rules decide what the menu shows, and neither of them is the platform
// (D-3, D-17):
//   - a section whose capability is off does not exist here at all. Not
//     greyed out, not a row of dashes — absent, because the hardware cannot
//     do it and pretending otherwise wastes the user's attention.
//   - a section we have not built yet is shown disabled and labelled, because
//     hiding it would make the panel look smaller than the plan while a dead
//     link would make it look broken.
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { clearToken } from '@/api/client'
import { LOCALES, setLocale } from '@/i18n'
import { startLive, stopLive, useLive } from '@/stores/live'
import { setTheme, type ThemeChoice, theme } from '@/stores/theme'
import ApplyBar from './ApplyBar.vue'
import VbIcon from './VbIcon.vue'

interface NavItem {
  key: string
  path: string
  group: string
  /** capability that must be on for this section to exist at all (D-17). */
  needs?: string
  /** false while the section is planned but not implemented. */
  ready: boolean
}

const NAV: NavItem[] = [
  { key: 'dashboard', path: '/', group: '', ready: true },
  // The uplink and the VPN nodes are two sections, not one: the internet
  // screen edits the connection the panel itself rides on, and the accepted
  // mockup for it explicitly excludes nodes and policies.
  { key: 'internet', path: '/internet', group: 'groupEgress', ready: true },
  { key: 'nodes', path: '/nodes', group: 'groupEgress', ready: true },
  { key: 'policies', path: '/policies', group: 'groupEgress', ready: false },
  { key: 'network', path: '/network', group: 'groupLan', ready: false },
  { key: 'wifi', path: '/wifi', group: 'groupLan', needs: 'wifi', ready: false },
  { key: 'devices', path: '/devices', group: 'groupLan', ready: false },
  { key: 'rules', path: '/rules', group: 'groupRouting', ready: true },
  { key: 'system', path: '/system', group: 'groupDevice', ready: false },
]

// One icon per section, taken from the accepted mockup of the shell rather
// than chosen again here: the menu is the place people learn the vocabulary
// of the product, and two different icon sets would teach two vocabularies.
const NAV_ICON: Record<string, string> = {
  dashboard: 'dash',
  internet: 'globe',
  nodes: 'node',
  policies: 'policy',
  network: 'net',
  wifi: 'wifi',
  devices: 'dev',
  rules: 'rule',
  system: 'sys',
}

// The routing rules screen still lives at its old path; the menu points at
// what exists rather than at what it will be called later.
const PATH_OVERRIDE: Record<string, string> = { rules: '/routes' }

const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
// `reachable`, not `connected`: a link that dies under the stream leaves its
// socket open, so the dot stayed green on a device that was gone (measured on
// the stand while an uplink change was live).
const { reachable, lastUpdate, stale, deviceError, system, can } = useLive()

const drawer = ref(false)
const searchOpen = ref(false)
const query = ref('')

const visibleNav = computed(() => NAV.filter((i) => !i.needs || can(i.needs)))

const groups = computed(() => {
  const out: { group: string; items: NavItem[] }[] = []
  for (const item of visibleNav.value) {
    const last = out[out.length - 1]
    if (last && last.group === item.group) last.items.push(item)
    else out.push({ group: item.group, items: [item] })
  }
  return out
})

const searchResults = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return visibleNav.value
  return visibleNav.value.filter((i) => t(`shell.${i.key}`).toLowerCase().includes(q))
})

const freshness = computed(() => {
  if (!lastUpdate.value) return t('shell.connecting')
  return t('shell.dataFrom', {
    time: lastUpdate.value.toLocaleTimeString(locale.value, {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }),
  })
})

const deviceLine = computed(() => {
  const s = system.value
  if (!s) return ''
  return [s.model, s.firmware].filter(Boolean).join(' · ')
})

function pathOf(item: NavItem) {
  return PATH_OVERRIDE[item.key] ?? item.path
}

function go(item: NavItem) {
  if (!item.ready) return
  drawer.value = false
  searchOpen.value = false
  query.value = ''
  router.push(pathOf(item))
}

function logout() {
  stopLive()
  clearToken()
  router.push('/login')
}

function onKey(e: KeyboardEvent) {
  const typingInField =
    e.target instanceof HTMLElement && ['INPUT', 'TEXTAREA'].includes(e.target.tagName)
  if ((e.key === 'k' && (e.metaKey || e.ctrlKey)) || (e.key === '/' && !typingInField)) {
    e.preventDefault()
    searchOpen.value = true
  }
}

onMounted(() => {
  startLive()
  window.addEventListener('keydown', onKey)
})
onUnmounted(() => window.removeEventListener('keydown', onKey))

// A locale change must reach Element Plus and our own strings together; the
// freshness label recomputes on its own because it reads locale.value.
watch(locale, () => {
  document.documentElement.lang = String(locale.value)
})

const activePath = computed(() => route.path)
</script>

<template>
  <div class="vb-shell">
    <aside class="vb-side">
      <div class="vb-side__brand">
        <strong>VeilBridge</strong>
        <small v-if="system?.hostname">{{ system.hostname }}</small>
      </div>
      <nav class="vb-side__nav" :aria-label="t('shell.menu')">
        <template v-for="g in groups" :key="g.group || 'root'">
          <div v-if="g.group" class="vb-side__group">{{ t(`shell.${g.group}`) }}</div>
          <button
            v-for="item in g.items"
            :key="item.key"
            type="button"
            class="vb-side__item"
            :class="{
              'is-active': activePath === pathOf(item),
              'is-disabled': !item.ready,
            }"
            :disabled="!item.ready"
            :title="item.ready ? undefined : t('shell.notBuilt')"
            @click="go(item)"
          >
            <span class="vb-side__label">
              <VbIcon :name="NAV_ICON[item.key] ?? 'dash'" />
              {{ t(`shell.${item.key}`) }}
            </span>
            <el-tag v-if="!item.ready" size="small" type="info" round>{{ t('shell.soon') }}</el-tag>
          </button>
        </template>
      </nav>
    </aside>

    <div class="vb-main">
      <header class="vb-head">
        <el-button
          class="vb-head__burger"
          text
          :aria-label="t('shell.openMenu')"
          @click="drawer = true"
        >
          ☰
        </el-button>
        <div class="vb-head__device">
          <strong>{{ system?.hostname ?? 'VeilBridge' }}</strong>
          <small v-if="deviceLine">{{ deviceLine }}</small>
        </div>
        <button
          type="button"
          class="vb-head__search"
          :aria-label="t('shell.openSearch')"
          @click="searchOpen = true"
        >
          <span>{{ t('shell.search') }}</span>
          <kbd>{{ t('shell.searchHint') }}</kbd>
        </button>
        <!-- Colour is never the only carrier: the dot has a text label beside
             it, and the region announces itself when the state changes. -->
        <span
          class="vb-head__fresh"
          :class="{ 'is-stale': stale }"
          role="status"
          aria-live="polite"
        >
          <i class="vb-dot" :class="reachable ? 'is-live' : 'is-down'" aria-hidden="true" />
          {{ freshness }}
        </span>
        <!-- Language and logout live in the drawer on a phone: at 360 the
             header has room for the device, the search and the freshness
             mark, and nothing else fits without overflowing. -->
        <div class="vb-head__wide-only">
          <el-dropdown trigger="click" @command="(v: ThemeChoice) => setTheme(v)">
            <span class="vb-head__lang" :title="t('shell.theme')">
              {{ theme === 'dark' ? '◐' : theme === 'light' ? '○' : '◑' }}
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="system" :disabled="theme === 'system'">
                  {{ t('shell.themeSystem') }}
                </el-dropdown-item>
                <el-dropdown-item command="light" :disabled="theme === 'light'">
                  {{ t('shell.themeLight') }}
                </el-dropdown-item>
                <el-dropdown-item command="dark" :disabled="theme === 'dark'">
                  {{ t('shell.themeDark') }}
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <el-dropdown trigger="click" @command="setLocale">
            <span class="vb-head__lang">
              {{ LOCALES.find((l) => l.code === locale)?.nativeName ?? 'English' }} ▾
            </span>
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
          <el-button text @click="logout">{{ t('nav.logout') }}</el-button>
        </div>
      </header>

      <!-- Losing the stream is routine on a router, so it is an inline banner
           and not a screen: the content below stays readable. -->
      <el-alert
        v-if="stale"
        class="vb-banner"
        type="warning"
        :closable="false"
        :title="t('shell.offline')"
        :description="t('shell.offlineHint')"
        show-icon
      />
      <el-alert
        v-else-if="deviceError"
        class="vb-banner"
        type="error"
        :closable="false"
        :title="t('shell.deviceError', { detail: deviceError })"
        show-icon
      />

      <main class="vb-content">
        <router-view />
      </main>

      <ApplyBar />
    </div>

    <el-drawer v-model="drawer" direction="ltr" size="270px" :title="t('shell.menu')">
      <nav class="vb-side__nav">
        <template v-for="g in groups" :key="g.group || 'root'">
          <div v-if="g.group" class="vb-side__group">{{ t(`shell.${g.group}`) }}</div>
          <button
            v-for="item in g.items"
            :key="item.key"
            type="button"
            class="vb-side__item"
            :class="{ 'is-active': activePath === pathOf(item), 'is-disabled': !item.ready }"
            :disabled="!item.ready"
            @click="go(item)"
          >
            <span class="vb-side__label">
              <VbIcon :name="NAV_ICON[item.key] ?? 'dash'" />
              {{ t(`shell.${item.key}`) }}
            </span>
            <el-tag v-if="!item.ready" size="small" type="info" round>{{ t('shell.soon') }}</el-tag>
          </button>
        </template>
      </nav>
      <div class="vb-drawer__foot">
        <el-select
          :model-value="theme"
          size="default"
          :aria-label="t('shell.theme')"
          @change="(v: ThemeChoice) => setTheme(v)"
        >
          <el-option value="system" :label="t('shell.themeSystem')" />
          <el-option value="light" :label="t('shell.themeLight')" />
          <el-option value="dark" :label="t('shell.themeDark')" />
        </el-select>
        <el-select :model-value="locale" size="default" @change="setLocale">
          <el-option v-for="l in LOCALES" :key="l.code" :value="l.code" :label="l.nativeName" />
        </el-select>
        <el-button @click="logout">{{ t('nav.logout') }}</el-button>
      </div>
    </el-drawer>

    <el-dialog v-model="searchOpen" :title="t('shell.search')" width="min(520px, 92vw)" top="10vh">
      <el-input v-model="query" autofocus :placeholder="t('shell.search')" clearable />
      <ul class="vb-search">
        <li v-for="item in searchResults" :key="item.key">
          <button type="button" :disabled="!item.ready" @click="go(item)">
            {{ t(`shell.${item.key}`) }}
            <small v-if="item.group">{{ t(`shell.${item.group}`) }}</small>
            <el-tag v-if="!item.ready" size="small" type="info" round>{{ t('shell.soon') }}</el-tag>
          </button>
        </li>
        <li v-if="!searchResults.length" class="vb-search__empty">
          {{ t('shell.searchEmpty', { q: query }) }}
        </li>
      </ul>
    </el-dialog>
  </div>
</template>

<style scoped>
.vb-shell {
  display: flex;
  min-height: 100vh;
  /* The page tone, not the "light fill" one: the fill is for surfaces sitting
     ON the page, and using it here made the whole panel a shade brighter than
     the accepted mockup (measured: #FAFAF8 against #F4F4F1). */
  background: var(--el-bg-color-page);
}
.vb-side {
  width: 264px;
  flex: none;
  border-right: 1px solid var(--el-border-color-light);
  background: var(--el-bg-color);
}
.vb-side__brand {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 18px 20px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.vb-side__brand small {
  color: var(--el-text-color-secondary);
  font-family: var(--el-font-family-mono, monospace);
}
.vb-side__nav {
  padding: 12px;
}
.vb-side__group {
  padding: 14px 10px 6px;
  font-size: 11px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--el-text-color-secondary);
}
.vb-side__label {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.vb-side__item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  padding: 9px 12px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-primary);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.vb-side__item:focus-visible,
.vb-head__search:focus-visible,
.vb-search button:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 2px;
}
.vb-side__item:hover:not(.is-disabled) {
  background: var(--el-fill-color-light);
}
.vb-side__item.is-active {
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
  font-weight: 600;
}
.vb-side__item.is-disabled {
  color: var(--el-text-color-placeholder);
  cursor: default;
}
.vb-main {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
}
.vb-head {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 12px 20px;
  border-bottom: 1px solid var(--el-border-color-light);
  background: var(--el-bg-color);
}
.vb-head__burger {
  display: none;
}
.vb-head__device {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.vb-head__device small {
  color: var(--el-text-color-secondary);
  font-family: var(--el-font-family-mono, monospace);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.vb-head__search {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: 1;
  max-width: 420px;
  padding: 7px 12px;
  border: 1px solid var(--el-border-color);
  border-radius: 8px;
  background: var(--el-fill-color-blank);
  color: var(--el-text-color-placeholder);
  cursor: text;
}
.vb-head__search kbd {
  margin-left: auto;
  padding: 1px 6px;
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  font-size: 11px;
}
.vb-head__fresh {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
}
.vb-head__fresh.is-stale {
  color: var(--el-color-warning);
}
.vb-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--el-color-success);
}
.vb-dot.is-down {
  background: var(--el-color-warning);
}
.vb-head__lang {
  cursor: pointer;
  white-space: nowrap;
}
.vb-banner {
  margin: 12px 20px 0;
  width: auto;
}
.vb-content {
  flex: 1;
  padding: 20px;
  min-width: 0;
}
.vb-search {
  margin: 12px 0 0;
  padding: 0;
  list-style: none;
}
.vb-search button {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 10px 12px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.vb-search button:hover:not(:disabled) {
  background: var(--el-fill-color-light);
}
.vb-search button:disabled {
  color: var(--el-text-color-placeholder);
  cursor: default;
}
.vb-search small {
  color: var(--el-text-color-secondary);
}
.vb-search__empty {
  padding: 14px 12px;
  color: var(--el-text-color-secondary);
}
.vb-head__wide-only {
  display: flex;
  align-items: center;
  gap: 8px;
}
.vb-drawer__foot {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 16px 12px 0;
  margin-top: 12px;
  border-top: 1px solid var(--el-border-color-lighter);
}
@media (width <= 900px) {
  .vb-side {
    display: none;
  }
  .vb-head__wide-only {
    display: none;
  }
  .vb-head__burger {
    display: inline-flex;
  }
  .vb-head__search span {
    display: none;
  }
  .vb-head__fresh {
    font-size: 12px;
  }
  .vb-content {
    padding: 14px;
  }
}
</style>
