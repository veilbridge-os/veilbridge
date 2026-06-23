<script setup lang="ts">
import { ElMessage, ElMessageBox } from 'element-plus'
import { onMounted, ref } from 'vue'
import { api, type NodeWithStatus } from '../api/client'

const nodes = ref<NodeWithStatus[]>([])
const loading = ref(false)
const importOpen = ref(false)
const confText = ref('')

async function refresh() {
  loading.value = true
  try {
    nodes.value = await api.listNodes()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    loading.value = false
  }
}

async function doImport() {
  if (!confText.value.trim()) return
  try {
    await api.importConfig(confText.value)
    ElMessage.success('Node imported')
    importOpen.value = false
    confText.value = ''
    refresh()
  } catch (e) {
    ElMessage.error('Import failed: ' + (e as Error).message)
  }
}

async function activate(id: string) {
  try {
    await api.activateNode(id)
    ElMessage.success('Activated — egress switched')
    refresh()
  } catch (e) {
    ElMessage.error('Activate failed: ' + (e as Error).message)
  }
}

async function remove(id: string) {
  await ElMessageBox.confirm('Remove this node?', 'Confirm', { type: 'warning' }).catch(
    () => 'cancel',
  )
  try {
    await api.removeNode(id)
    refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

function handshake(row: NodeWithStatus): string {
  const a = row.status?.handshakeAgeSec
  if (a === undefined || a < 0) return 'never'
  return `${a}s ago`
}

onMounted(refresh)
</script>

<template>
  <div style="margin-bottom: 12px">
    <el-button type="primary" @click="importOpen = true">Import config</el-button>
    <el-button @click="refresh" :loading="loading">Refresh</el-button>
  </div>

  <el-table :data="nodes" v-loading="loading" empty-text="No nodes — import a .conf to start">
    <el-table-column label="Name" prop="node.name" />
    <el-table-column label="Endpoint" prop="node.endpoint" />
    <el-table-column label="Engine" prop="node.engine" width="120" />
    <el-table-column label="Active" width="90">
      <template #default="{ row }">
        <el-tag v-if="row.status?.active" type="success" size="small">active</el-tag>
        <span v-else>—</span>
      </template>
    </el-table-column>
    <el-table-column label="Handshake" width="120">
      <template #default="{ row }">{{ handshake(row) }}</template>
    </el-table-column>
    <el-table-column label="Actions" width="200">
      <template #default="{ row }">
        <el-button size="small" type="primary" @click="activate(row.node.id)">Activate</el-button>
        <el-button size="small" type="danger" @click="remove(row.node.id)">Remove</el-button>
      </template>
    </el-table-column>
  </el-table>

  <el-dialog v-model="importOpen" title="Import AmneziaWG config" width="560px">
    <el-input
      v-model="confText"
      type="textarea"
      :rows="14"
      placeholder="[Interface]&#10;PrivateKey = …&#10;…&#10;[Peer]&#10;…"
    />
    <template #footer>
      <el-button @click="importOpen = false">Cancel</el-button>
      <el-button type="primary" @click="doImport">Import</el-button>
    </template>
  </el-dialog>
</template>
