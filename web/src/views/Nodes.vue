<script setup lang="ts">
import { ElMessage, ElMessageBox } from 'element-plus'
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, type NodeWithStatus } from '@/api/client'

const { t } = useI18n()
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
    ElMessage.success(t('nodes.imported'))
    importOpen.value = false
    confText.value = ''
    refresh()
  } catch (e) {
    ElMessage.error(`${t('nodes.importFailed')}: ${(e as Error).message}`)
  }
}

async function activate(id: string) {
  try {
    await api.activateNode(id)
    ElMessage.success(t('nodes.activated'))
    refresh()
  } catch (e) {
    ElMessage.error(`${t('nodes.activateFailed')}: ${(e as Error).message}`)
  }
}

async function remove(id: string) {
  await ElMessageBox.confirm(t('nodes.confirmRemove'), t('nodes.confirm'), {
    type: 'warning',
  }).catch(() => 'cancel')
  try {
    await api.removeNode(id)
    refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

function handshake(row: NodeWithStatus): string {
  const a = row.status?.handshakeAgeSec
  if (a === undefined || a < 0) return t('nodes.never')
  return t('nodes.ago', { n: a })
}

onMounted(refresh)
</script>

<template>
  <div style="margin-bottom: 12px">
    <el-button type="primary" @click="importOpen = true">{{ t('nodes.importConfig') }}</el-button>
    <el-button @click="refresh" :loading="loading">{{ t('nodes.refresh') }}</el-button>
  </div>

  <el-table :data="nodes" v-loading="loading" :empty-text="t('nodes.empty')">
    <el-table-column :label="t('nodes.name')" prop="node.name" />
    <el-table-column :label="t('nodes.endpoint')" prop="node.endpoint" />
    <el-table-column :label="t('nodes.engine')" prop="node.engine" width="120" />
    <el-table-column :label="t('nodes.active')" width="90">
      <template #default="{ row }">
        <el-tag v-if="row.status?.active" type="success" size="small">{{ t('nodes.active') }}</el-tag>
        <span v-else>—</span>
      </template>
    </el-table-column>
    <el-table-column :label="t('nodes.handshake')" width="120">
      <template #default="{ row }">{{ handshake(row) }}</template>
    </el-table-column>
    <el-table-column :label="t('nodes.actions')" width="200">
      <template #default="{ row }">
        <el-button size="small" type="primary" @click="activate(row.node.id)">{{ t('nodes.activate') }}</el-button>
        <el-button size="small" type="danger" @click="remove(row.node.id)">{{ t('nodes.remove') }}</el-button>
      </template>
    </el-table-column>
  </el-table>

  <el-dialog v-model="importOpen" :title="t('nodes.importTitle')" width="560px">
    <el-input
      v-model="confText"
      type="textarea"
      :rows="14"
      placeholder="[Interface]&#10;PrivateKey = …&#10;…&#10;[Peer]&#10;…"
    />
    <template #footer>
      <el-button @click="importOpen = false">{{ t('nodes.cancel') }}</el-button>
      <el-button type="primary" @click="doImport">{{ t('nodes.import') }}</el-button>
    </template>
  </el-dialog>
</template>
