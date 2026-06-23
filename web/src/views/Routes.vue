<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { api, type RouteRule } from '../api/client'

const rules = ref<RouteRule[]>([])
const loading = ref(false)
const addOpen = ref(false)
const draft = ref<{ kind: 'domain' | 'subnet'; value: string; target: 'tunnel' | 'direct'; note: string }>({
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
    ElMessage.error('Add failed: ' + (e as Error).message)
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
    ElMessage.success(r.ok ? 'Rules applied' : 'Apply reported: ' + (r.detail || 'not ok'))
  } catch (e) {
    ElMessage.error('Apply failed: ' + (e as Error).message)
  }
}

// Path probe: does traffic to a rule's value actually take the expected path?
// Compares egress IPs, not HTTP status (the "curl 200 ≠ tunnel" lesson, D-5).
async function probe(row: RouteRule) {
  try {
    const p = await api.probe(row.value, row.target as 'tunnel' | 'direct')
    const verdict = p.ok ? 'OK' : 'MISMATCH'
    const type = p.ok ? 'success' : 'warning'
    ElMessage({ type, message: `${verdict}: expected ${p.expectedVia}, got ${p.actualVia}. ${p.detail || ''}` })
  } catch (e) {
    ElMessage.error('Probe failed: ' + (e as Error).message)
  }
}

onMounted(refresh)
</script>

<template>
  <div style="margin-bottom: 12px">
    <el-button type="primary" @click="addOpen = true">Add rule</el-button>
    <el-button @click="apply">Apply to OS</el-button>
    <el-button @click="refresh" :loading="loading">Refresh</el-button>
  </div>

  <el-table :data="rules" v-loading="loading" empty-text="No routing rules">
    <el-table-column label="Kind" prop="kind" width="100" />
    <el-table-column label="Value" prop="value" />
    <el-table-column label="Target" width="110">
      <template #default="{ row }">
        <el-tag :type="row.target === 'tunnel' ? 'primary' : 'info'" size="small">{{ row.target }}</el-tag>
      </template>
    </el-table-column>
    <el-table-column label="Note" prop="note" />
    <el-table-column label="Actions" width="200">
      <template #default="{ row }">
        <el-button size="small" @click="probe(row)">Probe</el-button>
        <el-button size="small" type="danger" @click="del(row.id)">Delete</el-button>
      </template>
    </el-table-column>
  </el-table>

  <el-dialog v-model="addOpen" title="Add routing rule" width="460px">
    <el-form label-width="80px">
      <el-form-item label="Kind">
        <el-select v-model="draft.kind">
          <el-option label="domain" value="domain" />
          <el-option label="subnet" value="subnet" />
        </el-select>
      </el-form-item>
      <el-form-item label="Value">
        <el-input v-model="draft.value" :placeholder="draft.kind === 'domain' ? 'youtube.com' : '128.116.0.0/17'" />
      </el-form-item>
      <el-form-item label="Target">
        <el-radio-group v-model="draft.target">
          <el-radio value="tunnel">tunnel</el-radio>
          <el-radio value="direct">direct</el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item label="Note">
        <el-input v-model="draft.note" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="addOpen = false">Cancel</el-button>
      <el-button type="primary" @click="add">Add</el-button>
    </template>
  </el-dialog>
</template>
