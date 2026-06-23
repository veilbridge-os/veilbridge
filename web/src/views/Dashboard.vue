<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { api, type SystemInfo } from '@/api/client'

const router = useRouter()
const { t } = useI18n()
const info = ref<SystemInfo | null>(null)
const error = ref('')
let timer: number | undefined

async function refresh() {
  try {
    info.value = await api.system()
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  }
}

function pct(used?: number, total?: number): number {
  if (!used || !total) return 0
  return Math.round((used / total) * 100)
}
function mib(bytes?: number): string {
  if (!bytes) return '—'
  return Math.round(bytes / (1024 * 1024)) + ' MiB'
}
function uptime(sec?: number): string {
  if (!sec) return '—'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  return `${h}h ${m}m`
}

onMounted(() => {
  refresh()
  // Live-ish dashboard: poll every 5s (WebSocket streaming is a later milestone).
  timer = window.setInterval(refresh, 5000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <el-alert v-if="error" :title="error" type="error" show-icon style="margin-bottom: 16px" />
  <el-row :gutter="16">
    <!-- Egress through the active tunnel — the headline tile. Click → Nodes. -->
    <el-col :span="6">
      <el-card shadow="hover" class="vb-tile" @click="router.push('/nodes')">
        <div class="vb-tile-label">{{ t('dashboard.egressTunnel') }}</div>
        <div class="vb-tile-value">{{ info?.egressGeo || '—' }}</div>
        <div class="vb-tile-sub">{{ info?.egressIP || t('dashboard.noTunnel') }}</div>
        <el-tag :type="info?.tunnelUp ? 'success' : 'info'" size="small">
          {{ info?.tunnelUp ? t('dashboard.tunnelUp') : t('dashboard.direct') }}
        </el-tag>
      </el-card>
    </el-col>

    <el-col :span="6">
      <el-card shadow="hover" class="vb-tile">
        <div class="vb-tile-label">{{ t('dashboard.directWan') }}</div>
        <div class="vb-tile-value">{{ info?.wanIP || '—' }}</div>
        <div class="vb-tile-sub">{{ info?.platform }} · {{ info?.hostname }}</div>
      </el-card>
    </el-col>

    <el-col :span="6">
      <el-card shadow="hover" class="vb-tile">
        <div class="vb-tile-label">{{ t('dashboard.cpu') }}</div>
        <el-progress type="dashboard" :percentage="Math.round(info?.cpuPercent || 0)" :width="90" />
      </el-card>
    </el-col>

    <el-col :span="6">
      <el-card shadow="hover" class="vb-tile">
        <div class="vb-tile-label">{{ t('dashboard.memory') }}</div>
        <el-progress type="dashboard" :percentage="pct(info?.memUsed, info?.memTotal)" :width="90" />
        <div class="vb-tile-sub">{{ mib(info?.memUsed) }} / {{ mib(info?.memTotal) }}</div>
        <div class="vb-tile-sub">{{ t('dashboard.up') }} {{ uptime(info?.uptimeSec) }}</div>
      </el-card>
    </el-col>
  </el-row>
</template>

<style scoped>
.vb-tile {
  min-height: 150px;
  cursor: default;
}
.vb-tile-label {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.vb-tile-value {
  font-size: 22px;
  font-weight: 600;
  margin: 6px 0;
}
.vb-tile-sub {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>
