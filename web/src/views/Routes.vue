<script setup lang="ts">
import { ElMessage } from 'element-plus'
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, type RouteRule } from '@/api/client'

const { t } = useI18n()
const rules = ref<RouteRule[]>([])
const loading = ref(false)
const addOpen = ref(false)
const draft = ref<{
  kind: 'domain' | 'subnet'
  value: string
  target: 'tunnel' | 'direct'
  note: string
}>({
  kind: 'domain',
  value: '',
  target: 'tunnel',
  note: '',
})

async function refresh() {
  loading.value = true
  try {
    rules.value = await api.listRoutes()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    loading.value = false
  }
}

async function add() {
  if (!draft.value.value.trim()) return
  try {
    await api.addRoute({ ...draft.value })
    addOpen.value = false
    draft.value = { kind: 'domain', value: '', target: 'tunnel', note: '' }
    refresh()
  } catch (e) {
    ElMessage.error(`${t('routes.applyFailed')}: ${(e as Error).message}`)
  }
}

async function del(id: string) {
  try {
    await api.deleteRoute(id)
    refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

async function apply() {
  try {
    const r = await api.applyRoutes()
    ElMessage.success(
      r.ok ? t('routes.applied') : t('routes.applyReported', { detail: r.detail || '' }),
    )
  } catch (e) {
    ElMessage.error(`${t('routes.applyFailed')}: ${(e as Error).message}`)
  }
}

// Path probe: does traffic to a rule's value actually take the expected path?
// Compares egress IPs, not HTTP status — a 200 response doesn't prove the tunnel.
async function probe(row: RouteRule) {
  try {
    const p = await api.probe(row.value, row.target as 'tunnel' | 'direct')
    const type = p.ok ? 'success' : 'warning'
    ElMessage({
      type,
      message: t('routes.probeResult', {
        verdict: p.ok ? t('routes.probeOk') : t('routes.probeMismatch'),
        expected: p.expectedVia,
        actual: p.actualVia,
        detail: p.detail || '',
      }),
    })
  } catch (e) {
    ElMessage.error(`${t('routes.probeFailed')}: ${(e as Error).message}`)
  }
}

onMounted(refresh)
</script>

<template>
  <div style="margin-bottom: 12px">
    <el-button type="primary" @click="addOpen = true">{{ t('routes.addRule') }}</el-button>
    <el-button @click="apply">{{ t('routes.applyToOs') }}</el-button>
    <el-button @click="refresh" :loading="loading">{{ t('routes.refresh') }}</el-button>
  </div>

  <el-table :data="rules" v-loading="loading" :empty-text="t('routes.empty')">
    <el-table-column :label="t('routes.kind')" prop="kind" width="100" />
    <el-table-column :label="t('routes.value')" prop="value" />
    <el-table-column :label="t('routes.target')" width="110">
      <template #default="{ row }">
        <el-tag :type="row.target === 'tunnel' ? 'primary' : 'info'" size="small">{{ row.target }}</el-tag>
      </template>
    </el-table-column>
    <el-table-column :label="t('routes.note')" prop="note" />
    <el-table-column :label="t('routes.actions')" width="200">
      <template #default="{ row }">
        <el-button size="small" @click="probe(row as RouteRule)">{{ t('routes.probe') }}</el-button>
        <el-button size="small" type="danger" @click="del(row.id)">{{ t('routes.delete') }}</el-button>
      </template>
    </el-table-column>
  </el-table>

  <el-dialog v-model="addOpen" :title="t('routes.addTitle')" width="460px">
    <el-form label-width="90px">
      <el-form-item :label="t('routes.kind')">
        <el-select v-model="draft.kind">
          <el-option :label="t('routes.domain')" value="domain" />
          <el-option :label="t('routes.subnet')" value="subnet" />
        </el-select>
      </el-form-item>
      <el-form-item :label="t('routes.value')">
        <el-input v-model="draft.value" :placeholder="draft.kind === 'domain' ? 'youtube.com' : '128.116.0.0/17'" />
      </el-form-item>
      <el-form-item :label="t('routes.target')">
        <el-radio-group v-model="draft.target">
          <el-radio value="tunnel">{{ t('routes.tunnel') }}</el-radio>
          <el-radio value="direct">{{ t('routes.direct') }}</el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item :label="t('routes.note')">
        <el-input v-model="draft.note" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="addOpen = false">{{ t('routes.cancel') }}</el-button>
      <el-button type="primary" @click="add">{{ t('routes.add') }}</el-button>
    </template>
  </el-dialog>
</template>
