<script setup lang="ts">
// Routing rules (v0.1), made honest until they route anything (#49, D-79).
//
// None of these rules changes where the traffic of the local network goes
// today: a subnet rule marks packets that nothing then routes (#47), and a
// domain rule is only written down — enforcing it needs DNS interception.
// The screen used to accept new rules, answer "Rules applied" and show "OK"
// from a check that measured the tunnel itself rather than the rule's address.
// Every one of those said "you are protected" on a product people install to
// be protected. So, until the routing is fixed: the screen says what is true,
// each rule carries its real state, adding is off with the reason beside it,
// and the check is gone. Existing rules stay visible and can be deleted —
// hiding the section would hide from people who upgraded what they set up.
import { ElMessage, ElMessageBox } from 'element-plus'
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, type RouteRule } from '@/api/client'
import VbIcon from '@/components/VbIcon.vue'

const { t } = useI18n()
const rules = ref<RouteRule[]>([])
const loading = ref(false)
const loaded = ref(false)

async function refresh() {
  loading.value = true
  try {
    rules.value = await api.listRoutes()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    loading.value = false
    loaded.value = true
  }
}

async function del(row: RouteRule) {
  try {
    await ElMessageBox.confirm(
      t('routes.confirmDelete', { value: row.value }),
      t('routes.delete'),
      {
        confirmButtonText: t('routes.delete'),
        cancelButtonText: t('routes.cancel'),
        type: 'warning',
      },
    )
  } catch {
    return
  }
  try {
    await api.deleteRoute(row.id)
    refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

/** Why this rule does nothing, in the words of the person who wrote it. */
function reasonOf(row: RouteRule): string {
  if (row.kind === 'domain') return t('routes.whyDomain')
  return row.target === 'tunnel' ? t('routes.whySubnetTunnel') : t('routes.whySubnetDirect')
}

onMounted(refresh)
</script>

<template>
  <section class="vb-rr">
    <header class="vb-rr__head">
      <h1>{{ t('routes.title') }}</h1>
    </header>

    <el-alert
      type="warning"
      :closable="false"
      show-icon
      :title="t('routes.notInEffectTitle')"
      :description="t('routes.notInEffect')"
    />

    <div class="vb-rr__actions">
      <el-button type="primary" disabled>{{ t('routes.addRule') }}</el-button>
      <span class="vb-rr__hint">{{ t('routes.addOff') }}</span>
    </div>

    <el-card v-if="!loaded" shadow="never">
      <el-skeleton :rows="3" animated />
    </el-card>

    <el-card v-else-if="rules.length === 0" shadow="never">
      <el-empty :description="t('routes.empty')" :image-size="64" />
    </el-card>

    <el-card v-else shadow="never" class="vb-rr__list" v-loading="loading">
      <div v-for="row in rules" :key="row.id" class="vb-rr__row">
        <div class="vb-rr__state">
          <el-tag type="info" size="small">{{ t('routes.notInEffectTag') }}</el-tag>
        </div>
        <div class="vb-rr__main">
          <div class="vb-rr__value">{{ row.value }}</div>
          <div class="vb-rr__meta">
            {{ row.kind === 'domain' ? t('routes.domain') : t('routes.subnet') }}
            ·
            {{ row.target === 'tunnel' ? t('routes.wantTunnel') : t('routes.wantDirect') }}
            <template v-if="row.note"> · {{ row.note }}</template>
          </div>
          <div class="vb-rr__hint">{{ reasonOf(row) }}</div>
        </div>
        <div class="vb-rr__del">
          <el-button text size="small" :aria-label="t('routes.delete')" @click="del(row)">
            <VbIcon name="trash" size="sm" /><span class="vb-rr__dellabel">{{ t('routes.delete') }}</span>
          </el-button>
        </div>
      </div>
    </el-card>
  </section>
</template>

<style scoped>
.vb-rr {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.vb-rr__head h1 {
  margin: 0;
  font-size: 22px;
}
.vb-rr__actions {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-rr__hint {
  font-size: 12px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
.vb-rr__row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 12px;
  align-items: start;
  padding: 12px 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.vb-rr__row:first-child {
  padding-top: 0;
}
.vb-rr__row:last-child {
  border-bottom: 0;
  padding-bottom: 0;
}
.vb-rr__value {
  font-weight: 600;
  overflow-wrap: anywhere;
}
.vb-rr__dellabel {
  margin-left: 6px;
}
.vb-rr__meta {
  margin-top: 2px;
  font-size: 13px;
  color: var(--el-text-color-regular);
  overflow-wrap: anywhere;
}
/* On a phone the state tag goes above the rule instead of eating a column. */
@media (max-width: 560px) {
  .vb-rr__row {
    grid-template-columns: minmax(0, 1fr) auto;
  }
  .vb-rr__state {
    grid-column: 1 / -1;
  }
}
</style>
