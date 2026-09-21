<script setup lang="ts">
// Dashboard (M2.4), built to design/02_dashboard.result.html.
//
// The rule the whole screen obeys: never assert without showing the proof
// (D-5, NFR-5). "Traffic goes through the tunnel" is only ever printed next to
// the two addresses that differ, and a tile with nothing to fill it is not
// drawn at all — the unfillable ones are listed in one honest block instead.
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { api, type NodeWithStatus, type PathProbe, type WANStatus } from '@/api/client'
import { useDuration } from '@/lib/duration'
import { useLive } from '@/stores/live'

const { t, n } = useI18n()
const router = useRouter()
const { system, samples, stale, capability } = useLive()
// Durations are formatted in one place for the whole panel: the internet
// screen shows "how long has this been up" as well, and a second copy of the
// pluralisation rules would drift where nobody reads.
const { fmtDuration } = useDuration()

const wan = ref<WANStatus | null>(null)
const wanLoaded = ref(false)
const nodes = ref<NodeWithStatus[]>([])
const nodesLoaded = ref(false)
const probe = ref<PathProbe | null>(null)
const probing = ref(false)

// The uplink and the node list are not in the live stream: they change when a
// person changes them, not three times a minute. Polling them slowly costs
// far less than pushing them, and nothing here goes off-device (see
// core.Vitals for why that distinction matters).
const SLOW_POLL_MS = 15_000
let poll = 0

async function loadSlow() {
  try {
    wan.value = await api.wan()
  } catch {
    // Leave the last known uplink on screen; the banner already says the
    // device is unreachable.
  } finally {
    wanLoaded.value = true
  }
  try {
    nodes.value = await api.listNodes()
  } catch {
    // Same reasoning.
  } finally {
    nodesLoaded.value = true
  }
}

onMounted(() => {
  void loadSlow()
  poll = window.setInterval(loadSlow, SLOW_POLL_MS)
})
onUnmounted(() => window.clearInterval(poll))

const activeNode = computed(() => nodes.value.find((x) => x.status?.active))
const loading = computed(() => system.value === null)

/** tunnelState collapses the egress question into one answer, because two
 * indicators that can disagree teach the user to trust neither. */
const tunnelState = computed<'up' | 'past' | 'down' | 'none'>(() => {
  if (nodesLoaded.value && nodes.value.length === 0) return 'none'
  if (!activeNode.value) return 'down'
  return system.value?.tunnelUp ? 'up' : 'past'
})

const engineUserspace = computed(() => system.value?.tunnelEngine === 'userspace')
const kernelTun = computed(() => capability('kernel-tun'))

function fmtBytes(bytes?: number): string {
  if (!bytes) return '0'
  const mb = bytes / 1024 / 1024
  if (mb >= 1024) return `${n(Math.round((mb / 1024) * 10) / 10)} GB`
  return `${n(Math.round(mb))} MB`
}

function fmtAgo(seconds?: number): string {
  if (seconds === undefined || seconds < 0) return t('tiles.handshakeNever')
  if (seconds < 60) return t('time.secondsAgo', { n: seconds }, seconds)
  const mins = Math.floor(seconds / 60)
  if (mins < 60) return t('time.minutesAgo', { n: mins }, mins)
  const hours = Math.floor(mins / 60)
  return t('time.hoursAgo', { n: hours }, hours)
}

const memPercent = computed(() => {
  const s = system.value
  if (!s?.memTotal) return 0
  return Math.round((s.memUsed / s.memTotal) * 100)
})
// Thresholds are stated, not felt: a router sitting at 74% is healthy, and
// colouring that red would train the operator to ignore the colour.
const memStatus = computed(() => (memPercent.value > 92 ? 'exception' : undefined))
const memTag = computed(() => (memPercent.value <= 85 ? 'tiles.memoryNormal' : ''))

// storageUsed is optional in the contract (a platform may not know it), so
// the absent case is an explicit zero rather than a silent NaN in a bar.
const storagePercent = computed(() => {
  const s = system.value
  if (!s?.storageTotal) return 0
  return Math.round(((s.storageUsed ?? 0) / s.storageTotal) * 100)
})
const storageFreeBytes = computed(() => {
  const s = system.value
  if (!s?.storageTotal) return 0
  return s.storageTotal - (s.storageUsed ?? 0)
})
const storageTight = computed(() => storagePercent.value >= 90)

/** sparkline turns the live samples into an SVG path. The window is the
 * server's own live ring (3 minutes), so the picture cannot outrun the data. */
const sparkline = computed(() => {
  const pts = samples.value
  if (pts.length < 2) return ''
  const w = 320
  const h = 40
  const max = Math.max(10, ...pts.map((p) => p.cpuPercent ?? 0))
  return pts
    .map((p, i) => {
      const x = (i / (pts.length - 1)) * w
      const y = h - ((p.cpuPercent ?? 0) / max) * h
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
})

async function checkPath() {
  probing.value = true
  probe.value = null
  try {
    probe.value = await api.probe('example.com', tunnelState.value === 'up' ? 'tunnel' : 'direct')
  } catch {
    probe.value = null
  } finally {
    probing.value = false
  }
}
</script>

<template>
  <div class="vb-dash">
    <div class="vb-dash__head">
      <h1>{{ t('shell.dashboard') }}</h1>
      <el-tag v-if="tunnelState === 'up'" type="success" size="large">
        {{ t('dashboard.tunnelUp') }}
      </el-tag>
      <el-tag v-else-if="tunnelState === 'past'" type="danger" size="large">
        {{ t('tiles.pastTunnel') }}
      </el-tag>
      <el-tag v-else-if="tunnelState === 'down'" type="warning" size="large">
        {{ t('tiles.tunnelDown') }}
      </el-tag>
      <el-button type="primary" :loading="probing" class="vb-dash__probe" @click="checkPath">
        {{ probing ? t('tiles.checking') : t('tiles.checkPath') }}
      </el-button>
    </div>

    <el-alert
      v-if="probe"
      class="vb-dash__probe-result"
      :type="probe.ok ? 'success' : 'warning'"
      :closable="true"
      :title="probe.ok ? t('routes.probeOk') : t('routes.probeMismatch')"
      :description="probe.detail"
      show-icon
    />

    <div v-if="loading" class="vb-grid">
      <el-card v-for="i in 6" :key="i" class="vb-tile" shadow="never">
        <el-skeleton :rows="3" animated />
      </el-card>
    </div>

    <div v-else class="vb-grid">
      <!-- Egress: one question, one answer, with the proof beside it. -->
      <el-card class="vb-tile vb-tile--wide" shadow="never">
        <div class="vb-tile__head">
          <span class="vb-tile__title">{{ t('tiles.egress') }}</span>
          <el-tag v-if="activeNode" size="small" round>{{ activeNode.node.name }}</el-tag>
        </div>

        <template v-if="tunnelState === 'none'">
          <el-empty :description="t('tiles.noNodes')" :image-size="64">
            <p class="vb-tile__hint">{{ t('tiles.noNodesHint') }}</p>
            <el-button type="primary" @click="router.push('/nodes')">
              {{ t('tiles.addNode') }}
            </el-button>
          </el-empty>
        </template>

        <template v-else>
          <div class="vb-tile__value">{{ system?.egressIP || '—' }}</div>
          <div class="vb-tile__sub">
            {{ stale ? t('tiles.lastKnown') : t('tiles.egressAddr') }}
          </div>

          <el-alert
            v-if="tunnelState === 'up'"
            class="vb-tile__proof"
            type="success"
            :closable="false"
            :description="t('tiles.egressProof', { direct: system?.wanIP })"
            show-icon
          />
          <el-alert
            v-else-if="tunnelState === 'past'"
            class="vb-tile__proof"
            type="error"
            :closable="false"
            :description="t('tiles.pastTunnelHint', { node: activeNode?.node.name })"
            show-icon
          />

          <div class="vb-tile__foot">
            <span>{{ t('tiles.handshakeAgo', { ago: fmtAgo(activeNode?.status?.handshakeAgeSec) }) }}</span>
            <span>{{ engineUserspace ? t('tiles.engineUserspace') : t('tiles.engineKernel') }}</span>
            <span v-if="activeNode?.status">
              {{
                t('tiles.rxtx', {
                  rx: fmtBytes(activeNode.status.rxBytes),
                  tx: fmtBytes(activeNode.status.txBytes),
                })
              }}
            </span>
          </div>

          <!-- The fallback engine is a real limitation, so it is stated in the
               panel's own words; the device node and the package name stay in
               the technical detail (D-3/D-17). -->
          <el-alert
            v-if="engineUserspace"
            class="vb-tile__proof"
            type="warning"
            :closable="false"
            :title="t('tiles.engineUserspaceHint')"
            show-icon
          >
            <el-collapse v-if="kernelTun?.detail">
              <el-collapse-item :title="t('details')">
                <code>{{ kernelTun.detail }}</code>
              </el-collapse-item>
            </el-collapse>
          </el-alert>
        </template>
      </el-card>

      <!-- Internet: the uplink, and which rule identified it. -->
      <el-card class="vb-tile" shadow="never">
        <div class="vb-tile__head">
          <span class="vb-tile__title">{{ t('tiles.internet') }}</span>
        </div>
        <template v-if="wanLoaded && !wan">
          <div class="vb-tile__value vb-tile__value--muted">{{ t('tiles.noUplink') }}</div>
          <p class="vb-tile__hint">{{ t('tiles.noUplinkHint') }}</p>
        </template>
        <template v-else>
          <div class="vb-tile__value">{{ system?.wanIP || '—' }}</div>
          <div class="vb-tile__sub">{{ t('tiles.internetDirect') }}</div>
          <dl v-if="wan" class="vb-kv">
            <dt>{{ t('tiles.channel') }}</dt>
            <dd>{{ wan.interface.name }} · {{ wan.interface.proto }}</dd>
            <dt>{{ t('tiles.gateway') }}</dt>
            <dd>{{ wan.interface.gateway || '—' }}</dd>
            <dt>{{ t('tiles.resolvers') }}</dt>
            <dd>{{ wan.interface.dns?.join(', ') || '—' }}</dd>
          </dl>
          <!-- When more than one interface has a default route, the choice was
               made by a rule and not by measurement. Saying so is the whole
               point of selectedBy. -->
          <el-alert
            v-if="wan && wan.selectedBy !== 'default-route'"
            class="vb-tile__proof"
            type="warning"
            :closable="false"
            :description="t('tiles.pickedByName', { n: (wan.candidates?.length ?? 1) - 1 })"
            show-icon
          />
        </template>
      </el-card>

      <el-card class="vb-tile" shadow="never">
        <div class="vb-tile__head">
          <span class="vb-tile__title">{{ t('tiles.device') }}</span>
        </div>
        <div class="vb-tile__model">{{ system?.model || system?.hostname }}</div>
        <div class="vb-tile__sub">{{ system?.firmware }} · {{ system?.kernel }}</div>
        <div class="vb-tile__foot">
          <span>{{ t('tiles.uptime', { duration: fmtDuration(system?.uptimeSec) }) }}</span>
          <span>{{ t('tiles.load', { value: n(system?.loadAvg?.[0] ?? 0) }) }}</span>
        </div>
        <svg v-if="sparkline" class="vb-spark" viewBox="0 0 320 40" preserveAspectRatio="none">
          <path :d="sparkline" fill="none" stroke="currentColor" stroke-width="1.5" />
        </svg>
        <div class="vb-tile__sub">{{ t('tiles.loadWindow') }}</div>
      </el-card>

      <el-card class="vb-tile" shadow="never">
        <div class="vb-tile__head">
          <span class="vb-tile__title">{{ t('tiles.memory') }}</span>
          <el-tag v-if="memTag" size="small" type="success" round>{{ t(memTag) }}</el-tag>
        </div>
        <div class="vb-tile__value">
          {{ fmtBytes(system?.memUsed) }} / {{ fmtBytes(system?.memTotal) }}
          <small>· {{ memPercent }}%</small>
        </div>
        <el-progress :percentage="memPercent" :status="memStatus" :show-text="false" />
        <p class="vb-tile__hint">{{ t('tiles.memoryHint') }}</p>
      </el-card>

      <el-card v-if="system?.storageTotal" class="vb-tile" shadow="never">
        <div class="vb-tile__head">
          <span class="vb-tile__title">{{ t('tiles.storage') }}</span>
          <el-tag :type="storageTight ? 'danger' : 'success'" size="small" round>
            {{
              storageTight
                ? t('tiles.storageAlmostFull')
                : t('tiles.storageFree', { value: fmtBytes(storageFreeBytes) })
            }}
          </el-tag>
        </div>
        <div class="vb-tile__value">
          {{ fmtBytes(system?.storageUsed) }} / {{ fmtBytes(system?.storageTotal) }}
        </div>
        <el-progress
          :percentage="storagePercent"
          :status="storageTight ? 'exception' : undefined"
          :show-text="false"
        />
        <p class="vb-tile__hint">{{ t('tiles.storageHint') }}</p>
      </el-card>

      <el-card v-if="nodes.length" class="vb-tile vb-tile--wide" shadow="never">
        <div class="vb-tile__head">
          <span class="vb-tile__title">{{ t('tiles.nodesTitle') }}</span>
          <el-tag size="small" round>{{ t('tiles.nodesCount', { n: nodes.length }) }}</el-tag>
        </div>
        <!-- A list, not a table: at 360 a table can only scroll sideways,
             and the actions scroll out of reach with it. These rows wrap. -->
        <ul class="vb-nodes">
          <li v-for="row in nodes" :key="row.node.id" class="vb-nodes__row">
            <span class="vb-nodes__name">{{ row.node.name }}</span>
            <code class="vb-nodes__endpoint">{{ row.node.endpoint }}</code>
            <span class="vb-nodes__hs">{{ fmtAgo(row.status?.handshakeAgeSec) }}</span>
            <el-tag :type="row.status?.active ? 'success' : 'info'" size="small" round>
              {{ row.status?.active ? t('tiles.active') : t('tiles.standby') }}
            </el-tag>
          </li>
        </ul>
      </el-card>

      <!-- Everything we cannot fill honestly, in one place instead of a grid
           of tiles full of dashes. -->
      <el-card class="vb-tile vb-tile--wide vb-tile--later" shadow="never">
        <strong>{{ t('tiles.laterTitle') }}:</strong> {{ t('tiles.later') }}
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.vb-dash__head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.vb-dash__head h1 {
  margin: 0;
  font-size: 24px;
}
.vb-dash__probe {
  margin-left: auto;
}
.vb-dash__probe-result {
  margin-bottom: 16px;
}
.vb-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 14px;
}
.vb-tile--wide {
  grid-column: span 2;
}
.vb-tile__head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.vb-tile__title {
  font-size: 12px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--el-text-color-secondary);
}
.vb-tile__value {
  font-family: var(--el-font-family-mono, monospace);
  font-size: 28px;
  line-height: 1.2;
  word-break: break-all;
}
.vb-tile__value--muted {
  color: var(--el-text-color-secondary);
  font-size: 22px;
}
.vb-tile__value small {
  font-size: 14px;
  color: var(--el-text-color-secondary);
}
.vb-tile__model {
  font-size: 18px;
  font-weight: 600;
}
.vb-tile__sub,
.vb-tile__hint {
  margin: 6px 0 0;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.vb-tile__proof {
  margin-top: 10px;
}
.vb-tile__foot {
  display: flex;
  flex-wrap: wrap;
  gap: 14px;
  margin-top: 10px;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.vb-kv {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 4px 14px;
  margin: 12px 0 0;
  font-size: 13px;
}
.vb-kv dt {
  color: var(--el-text-color-secondary);
}
.vb-kv dd {
  margin: 0;
  font-family: var(--el-font-family-mono, monospace);
}
.vb-spark {
  display: block;
  width: 100%;
  height: 40px;
  margin-top: 10px;
  color: var(--el-color-primary);
}
.vb-nodes {
  margin: 0;
  padding: 0;
  list-style: none;
}
.vb-nodes__row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
  padding: 10px 0;
  border-top: 1px solid var(--el-border-color-lighter);
}
.vb-nodes__name {
  font-weight: 600;
  min-width: 120px;
}
.vb-nodes__endpoint {
  font-size: 13px;
  color: var(--el-text-color-regular);
  word-break: break-all;
}
.vb-nodes__hs {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  margin-left: auto;
}
.vb-tile--later {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
@media (width <= 900px) {
  .vb-tile--wide {
    grid-column: span 1;
  }
  .vb-grid {
    grid-template-columns: 1fr;
  }
}
</style>
