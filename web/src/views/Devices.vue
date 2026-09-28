<script setup lang="ts">
// The devices screen (#52), built to the accepted artboards Dev-* (design 08).
//
// It answers "who is on my network, and is anyone here I do not know" before
// the list is read: the summary counts online, new and nameless devices.
//
// What is NOT here, on purpose (D-87): the "internet" and "traffic" columns,
// turning internet off, the schedule and waking a device. They come with their
// own tasks (#53-#56); until the device can do them, a button or a column for
// them would be exactly the kind of promise #49 took back.
//
// A name and "I know this device" are the panel's own notes: they change
// nothing on the network and take effect at once (D-95). Reserving an address
// does change the network, so it goes through the apply bar like on 05.
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ApiError, api, type Device, type DeviceList } from '@/api/client'
import VbIcon from '@/components/VbIcon.vue'
import { useDuration } from '@/lib/duration'
import { refreshStaged, useLive } from '@/stores/live'

const { t, locale } = useI18n()
const { applyState, stale, lastUpdate, can } = useLive()
const { fmtAgo } = useDuration()

const list = ref<DeviceList | null>(null)
const loaded = ref(false)
const noLAN = ref(false)
const unsupported = ref(false)
const loadError = ref('')

// "Live data every few seconds" (design 08 §4). Five seconds keeps a phone
// that just joined from looking absent, and costs the router two calls to the
// access points per poll while somebody has this screen open.
const POLL_MS = 5_000
let poll = 0

async function load() {
  try {
    const got = await api.devices()
    noLAN.value = got === null
    list.value = got
    unsupported.value = false
    loadError.value = ''
  } catch (e) {
    if (e instanceof ApiError && e.status === 501) unsupported.value = true
    // Keep the last good list: the shell already says the router stopped
    // answering, and an empty list would read as "everybody left".
    else loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loaded.value = true
  }
}

onMounted(() => {
  void load()
  poll = window.setInterval(load, POLL_MS)
})
onUnmounted(() => window.clearInterval(poll))

const devices = computed<Device[]>(() => list.value?.devices ?? [])
const hasRadio = computed(() => can('wifi'))
const busy = computed(() => applyState.value?.phase === 'awaiting_confirm')
// Actions need the router: while it does not answer, the list is the last one
// known and nothing on it can be acted on (artboard Dev-States, 4).
const frozen = computed(() => stale.value || !!loadError.value)

const dataFrom = computed(() =>
  lastUpdate.value
    ? lastUpdate.value.toLocaleTimeString(locale.value, {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      })
    : '',
)

// --- the summary ---------------------------------------------------------

const onlineCount = computed(() => devices.value.filter((d) => d.online).length)
const newOnes = computed(() => devices.value.filter((d) => d.new))
const namelessCount = computed(() => devices.value.filter((d) => !d.name).length)

// --- what a row says -----------------------------------------------------

function displayName(d: Device): string {
  return d.name || d.reportedName || ''
}

/** Signal in words, the number only in the details (artboard Dev-Actions 4).
 * The thresholds are the usual ones for a home: above -60 dBm everything
 * works, below -70 video stutters. */
function signalWord(dbm?: number): string {
  if (!dbm) return ''
  if (dbm >= -60) return t('dev.level.good')
  if (dbm >= -70) return t('dev.level.fair')
  return t('dev.level.weak')
}

/** withSignal is off in the details, where the signal has its own line. */
function linkText(d: Device, withSignal = true): string {
  if (!d.online) {
    if (d.lastSeenSec !== undefined && d.lastSeenSec !== null) {
      return t('dev.lastSeen', { ago: fmtAgo(d.lastSeenSec) })
    }
    return t('dev.notSeenYet')
  }
  if (d.link.kind === 'cable') return t('dev.linkCable')
  if (d.link.kind === 'wifi') {
    const wifi = d.link.band
      ? t('dev.linkWifi', { band: d.link.band.replace('.', ',') })
      : t('dev.linkWifiNoBand')
    const level = withSignal ? signalWord(d.link.signalDbm) : ''
    return level ? `${wifi} · ${t('dev.signal', { level })}` : wifi
  }
  return t('dev.away1')
}

function linkIcon(d: Device): string {
  if (d.link.kind === 'wifi') return 'wifi'
  if (d.link.kind === 'cable') return 'cable'
  return 'dev'
}

const ipv4 = (d: Device) => (d.ips ?? []).find((ip) => !ip.includes(':')) ?? ''
const ipv6 = (d: Device) => (d.ips ?? []).filter((ip) => ip.includes(':'))

// --- search, order, filter -----------------------------------------------

type Filter = 'all' | 'wifi' | 'cable' | 'away'
const filter = ref<Filter>('all')
const order = ref<'new' | 'name'>('new')
const query = ref('')

const counts = computed(() => ({
  all: devices.value.length,
  wifi: devices.value.filter((d) => d.online && d.link.kind === 'wifi').length,
  cable: devices.value.filter((d) => d.online && d.link.kind === 'cable').length,
  away: devices.value.filter((d) => !d.online).length,
}))

function matches(d: Device, f: Filter): boolean {
  if (f === 'wifi') return d.online && d.link.kind === 'wifi'
  if (f === 'cable') return d.online && d.link.kind === 'cable'
  if (f === 'away') return !d.online
  return true
}

const collator = new Intl.Collator(undefined, { sensitivity: 'base', numeric: true })

function byName(a: Device, b: Device): number {
  const an = displayName(a)
  const bn = displayName(b)
  // Nameless devices after named ones, ordered by address: "No name" twelve
  // times in a row sorted by nothing is not a list.
  if (!an !== !bn) return an ? -1 : 1
  return collator.compare(an || a.mac, bn || b.mac)
}

const shown = computed(() => {
  const q = query.value.trim().toLowerCase()
  const out = devices.value.filter((d) => {
    if (!matches(d, filter.value)) return false
    if (!q) return true
    return [d.name, d.reportedName, d.mac, ...(d.ips ?? [])].some((s) =>
      s?.toLowerCase().includes(q),
    )
  })
  return out.sort((a, b) => {
    if (order.value === 'new' && a.new !== b.new) return a.new ? -1 : 1
    if (order.value === 'new' && a.online !== b.online) return a.online ? -1 : 1
    return byName(a, b)
  })
})

// Pages instead of an endless feed for a smart home with forty devices
// (artboard Dev-Many). Twenty fit a laptop screen without scrolling past the
// summary twice.
const PAGE = 20
const page = ref(1)
watch([filter, query, order], () => {
  page.value = 1
})
const paged = computed(() => shown.value.slice((page.value - 1) * PAGE, page.value * PAGE))

// --- naming, "known", forgetting -----------------------------------------

const editing = ref('')
const draftName = ref('')
const rowError = ref<Record<string, string>>({})
const saving = ref('')

function startRename(d: Device) {
  editing.value = d.mac
  draftName.value = d.name ?? ''
  rowError.value = {}
}

function explain(e: unknown): string {
  if (e instanceof ApiError && e.status === 409) return t('dev.tooMany')
  if (e instanceof ApiError && e.fields.name) {
    return `${t('dev.badName')} — ${t('dev.deviceSaid', { detail: e.fields.name })}`
  }
  return e instanceof Error ? e.message : String(e)
}

async function act(mac: string, run: () => Promise<void>) {
  saving.value = mac
  rowError.value = {}
  try {
    await run()
    await load()
    return true
  } catch (e) {
    rowError.value[mac] = explain(e)
    return false
  } finally {
    saving.value = ''
  }
}

async function saveName(d: Device) {
  if (await act(d.mac, () => api.nameDevice(d.mac, draftName.value))) editing.value = ''
}

const know = (d: Device) => act(d.mac, () => api.markDevicesKnown([d.mac]))
const knowAll = () => act('*', () => api.markDevicesKnown(newOnes.value.map((d) => d.mac)))

async function forget(d: Device) {
  if (await act(d.mac, () => api.forgetDevice(d.mac))) details.value = null
}

// --- reserving an address (through the apply bar) ------------------------

async function pin(d: Device) {
  await act(d.mac, async () => {
    await api.stageReservation({ mac: d.mac, ip: ipv4(d) })
  })
  await refreshStaged()
}

/** The reservation's own entry is on the local-network reading, not on the
 * device: a device only says which address is reserved for it. */
async function unpin(d: Device) {
  await act(d.mac, async () => {
    const lan = await api.lan()
    const r = lan?.reserved?.find((x) => x.mac.toLowerCase() === d.mac && x.id)
    if (!r?.id) throw new Error(t('dev.unpin'))
    await api.removeReservation(r.id)
  })
  await refreshStaged()
}

function onMenu(d: Device, cmd: string) {
  if (cmd === 'rename') startRename(d)
  else if (cmd === 'know') void know(d)
  else if (cmd === 'pin') void pin(d)
  else if (cmd === 'unpin') void unpin(d)
  else if (cmd === 'details') details.value = d.mac
}

// --- details --------------------------------------------------------------

const details = ref<string | null>(null)
const detailed = computed(() => devices.value.find((d) => d.mac === details.value) ?? null)
const detailsOpen = computed({
  get: () => details.value !== null,
  set: (v: boolean) => {
    if (!v) details.value = null
  },
})

// Remembered by the panel = there is something to forget.
const remembered = (d: Device) => !!d.name || !d.new
</script>

<template>
  <section class="vb-dev">
    <header class="vb-dev__head">
      <h1>{{ t('dev.title') }}</h1>
      <el-tag v-if="stale && dataFrom" size="small" type="info">
        {{ t('shell.dataFrom', { time: dataFrom }) }}
      </el-tag>
    </header>

    <el-card v-if="!loaded" shadow="never">
      <el-skeleton :rows="4" animated />
    </el-card>

    <el-card v-else-if="unsupported" shadow="never">
      <el-empty :description="t('dev.unsupported')" />
    </el-card>

    <el-card v-else-if="noLAN" shadow="never">
      <el-empty :description="t('dev.none')">
        <p class="vb-dev__hint">{{ t('dev.noneHint') }}</p>
      </el-empty>
    </el-card>

    <el-card v-else-if="!list && loadError" shadow="never">
      <el-alert type="error" :closable="false" show-icon :title="t('dev.readError')">
        <p class="vb-dev__hint">{{ t('dev.deviceSaid', { detail: loadError }) }}</p>
      </el-alert>
      <div class="vb-dev__actions">
        <el-button @click="load">{{ t('dev.retry') }}</el-button>
      </div>
    </el-card>

    <template v-else>
      <el-alert
        v-if="frozen"
        type="warning"
        :closable="false"
        show-icon
        :title="t('dev.stale')"
      />

      <!-- 1. The answer before the list: how many, how many new, how many
           nobody has named. -->
      <el-card shadow="never">
        <div class="vb-dev__summary">
          <div class="vb-dev__stat">
            <strong>{{ onlineCount }}</strong>
            <span>{{ t('dev.onlineOf', { n: devices.length }) }}</span>
          </div>
          <div class="vb-dev__stat" :class="{ 'is-new': newOnes.length }">
            <strong>{{ newOnes.length }}</strong>
            <span>{{ t('dev.newCount') }}</span>
          </div>
          <div class="vb-dev__stat">
            <strong>{{ namelessCount }}</strong>
            <span>{{ t('dev.noNameCount') }}</span>
          </div>
          <el-button
            v-if="newOnes.length > 1"
            class="vb-dev__knowall"
            :loading="saving === '*'"
            :disabled="frozen"
            @click="knowAll"
          >
            <VbIcon name="check" size="sm" class="vb-dev__btnico" />
            {{ t('dev.knowAllNew') }}
          </el-button>
        </div>
        <p class="vb-dev__hint">{{ t('dev.newHint') }}</p>
        <p v-if="rowError['*']" class="vb-dev__err">{{ rowError['*'] }}</p>
      </el-card>

      <el-card v-if="devices.length === 0" shadow="never">
        <el-empty :description="t('dev.empty')">
          <p class="vb-dev__hint">{{ t('dev.emptyHint') }}</p>
        </el-empty>
      </el-card>

      <!-- 2. The list. A grid of rows that turns into cards on a phone, the
           same markup as the local network screen's list (#33). -->
      <el-card v-else shadow="never">
        <div class="vb-dev__tools">
          <el-input
            v-model="query"
            clearable
            :placeholder="t('dev.search')"
            class="vb-dev__search"
          >
            <template #prefix><VbIcon name="search" size="sm" /></template>
          </el-input>
          <el-radio-group v-model="order" size="small">
            <el-radio-button value="new">{{ t('dev.sortNew') }}</el-radio-button>
            <el-radio-button value="name">{{ t('dev.sortName') }}</el-radio-button>
          </el-radio-group>
        </div>
        <!-- Without a radio there is no "Wi-Fi" filter at all, not a zero
             (artboard Dev-Box, D-20). -->
        <div class="vb-dev__filters" role="group">
          <el-check-tag :checked="filter === 'all'" @change="filter = 'all'">
            {{ t('dev.all', { n: counts.all }) }}
          </el-check-tag>
          <el-check-tag v-if="hasRadio" :checked="filter === 'wifi'" @change="filter = 'wifi'">
            {{ t('dev.wifi', { n: counts.wifi }) }}
          </el-check-tag>
          <el-check-tag :checked="filter === 'cable'" @change="filter = 'cable'">
            {{ t('dev.cable', { n: counts.cable }) }}
          </el-check-tag>
          <el-check-tag :checked="filter === 'away'" @change="filter = 'away'">
            {{ t('dev.away', { n: counts.away }) }}
          </el-check-tag>
        </div>
        <p v-if="query.trim()" class="vb-dev__hint">
          {{ t('dev.found', { n: shown.length, total: devices.length }) }}
        </p>

        <div class="vb-dev__list" role="table">
          <div class="vb-dev__row vb-dev__row--th" role="row">
            <span>{{ t('dev.colDevice') }}</span>
            <span>{{ t('dev.colLink') }}</span>
            <span>{{ t('dev.colAddress') }}</span>
            <span />
          </div>
          <div
            v-for="d in paged"
            :key="d.mac"
            class="vb-dev__row"
            :class="{ 'is-new': d.new, 'is-off': !d.online }"
            role="row"
          >
            <!-- Naming happens in the row itself (artboard Dev-Actions 1). -->
            <template v-if="editing === d.mac">
              <div class="vb-dev__edit">
                <label :for="`name-${d.mac}`" class="vb-dev__label">{{ t('dev.nameLabel') }}</label>
                <el-input
                  :id="`name-${d.mac}`"
                  v-model="draftName"
                  maxlength="64"
                  :placeholder="displayName(d) || t('dev.namePlaceholder')"
                  @keyup.enter="saveName(d)"
                  @keyup.esc="editing = ''"
                />
                <p class="vb-dev__hint">{{ t('dev.nameHint') }} {{ t('dev.nameSaveHint') }}</p>
                <div class="vb-dev__actions">
                  <el-button size="small" @click="editing = ''">{{ t('dev.cancel') }}</el-button>
                  <el-button
                    size="small"
                    type="primary"
                    :loading="saving === d.mac"
                    @click="saveName(d)"
                  >
                    {{ t('dev.save') }}
                  </el-button>
                </div>
                <p v-if="rowError[d.mac]" class="vb-dev__err">{{ rowError[d.mac] }}</p>
              </div>
            </template>
            <template v-else>
              <span class="vb-dev__cell--dev">
                <span class="vb-dev__name">
                  <strong v-if="d.name">{{ d.name }}</strong>
                  <strong v-else-if="d.reportedName" class="vb-dev__muted">{{ d.reportedName }}</strong>
                  <strong v-else class="vb-dev__muted">{{ t('dev.noName') }}</strong>
                  <el-tag v-if="d.new" size="small" type="warning" effect="light">
                    {{ t('dev.isNew') }}
                  </el-tag>
                  <el-tag v-if="d.privateAddress" size="small" type="info">
                    {{ t('dev.private') }}
                  </el-tag>
                </span>
                <span v-if="!d.name && d.reportedName" class="vb-dev__muted vb-dev__small">
                  {{ t('dev.reported') }}
                </span>
                <span v-else-if="d.name && d.reportedName && d.reportedName !== d.name" class="vb-dev__muted vb-dev__small">
                  {{ t('dev.dReported') }}: {{ d.reportedName }}
                </span>
              </span>
              <span class="vb-dev__cell--link" :class="{ 'vb-dev__muted': !d.online }">
                <VbIcon :name="linkIcon(d)" size="sm" class="vb-dev__muted" />
                {{ linkText(d) }}
              </span>
              <span class="vb-dev__cell--addr">
                <span v-if="ipv4(d) || ipv6(d)[0]" class="vb-mono">{{ ipv4(d) || ipv6(d)[0] }}</span>
                <span v-else class="vb-dev__muted">{{ t('dev.noAddress') }}</span>
                <span class="vb-mono vb-dev__muted vb-dev__small">{{ d.mac }}</span>
                <el-tag v-if="d.reservedIp" size="small" class="vb-dev__pintag">
                  {{ t('dev.pinned') }}
                </el-tag>
              </span>
              <span class="vb-dev__acts">
                <!-- One main action in a row: naming, where there is no name
                     yet (artboard Dev-Main). The rest are in the menu. -->
                <el-button
                  v-if="!d.name"
                  size="small"
                  :disabled="frozen"
                  @click="startRename(d)"
                >
                  <VbIcon name="edit" size="sm" class="vb-dev__btnico" />
                  {{ t('dev.giveName') }}
                </el-button>
                <el-dropdown trigger="click" :disabled="frozen" @command="(c: string) => onMenu(d, c)">
                  <el-button size="small" text :aria-label="t('dev.more')" :loading="saving === d.mac">
                    ⋯
                  </el-button>
                  <template #dropdown>
                    <el-dropdown-menu>
                      <el-dropdown-item command="rename">
                        {{ d.name ? t('dev.rename') : t('dev.giveName') }}
                      </el-dropdown-item>
                      <el-dropdown-item v-if="d.new" command="know">{{ t('dev.know') }}</el-dropdown-item>
                      <el-dropdown-item v-if="d.reservedIp" command="unpin" :disabled="busy">
                        {{ t('dev.unpin') }}
                      </el-dropdown-item>
                      <el-dropdown-item v-else-if="ipv4(d)" command="pin" :disabled="busy">
                        {{ t('dev.pin') }}
                      </el-dropdown-item>
                      <el-dropdown-item command="details" divided>{{ t('dev.details') }}</el-dropdown-item>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
                <p v-if="rowError[d.mac]" class="vb-dev__err">{{ rowError[d.mac] }}</p>
              </span>
            </template>
          </div>
        </div>

        <div v-if="shown.length > PAGE" class="vb-dev__pager">
          <span class="vb-dev__hint">
            {{
              t('dev.page', {
                from: (page - 1) * PAGE + 1,
                to: Math.min(page * PAGE, shown.length),
                total: shown.length,
              })
            }}
          </span>
          <el-pagination
            v-model:current-page="page"
            layout="prev, pager, next"
            :page-size="PAGE"
            :total="shown.length"
            small
          />
        </div>
        <p v-if="list" class="vb-dev__hint">
          {{ t('dev.watching', { ago: fmtAgo(list.watchingSec) }) }}
        </p>
      </el-card>
    </template>

    <el-drawer
      v-model="detailsOpen"
      :title="detailed ? displayName(detailed) || t('dev.noName') : ''"
      size="min(420px, 100vw)"
    >
      <template v-if="detailed">
        <dl class="vb-dev__facts">
          <div>
            <dt>{{ t('dev.dAddress') }}</dt>
            <dd class="vb-mono">{{ ipv4(detailed) || '—' }}</dd>
          </div>
          <div v-if="ipv6(detailed).length">
            <dt>{{ t('dev.dIPv6') }}</dt>
            <dd class="vb-mono">
              <span v-for="ip in ipv6(detailed)" :key="ip" class="vb-dev__line">{{ ip }}</span>
            </dd>
          </div>
          <div>
            <dt>{{ t('dev.dMac') }}</dt>
            <dd class="vb-mono">{{ detailed.mac }}</dd>
          </div>
          <div v-if="detailed.reportedName">
            <dt>{{ t('dev.dReported') }}</dt>
            <dd>{{ detailed.reportedName }}</dd>
          </div>
          <div>
            <dt>{{ detailed.online ? t('dev.dLink') : t('dev.dSeen') }}</dt>
            <dd>{{ linkText(detailed, false) }}</dd>
          </div>
          <div v-if="detailed.online && detailed.link.signalDbm">
            <dt>{{ t('dev.dSignal') }}</dt>
            <dd>
              {{
                t('dev.dSignalValue', {
                  word: signalWord(detailed.link.signalDbm),
                  dbm: String(detailed.link.signalDbm).replace('-', '−'),
                })
              }}
            </dd>
          </div>
          <div v-if="detailed.reservedIp">
            <dt>{{ t('dev.dReserved') }}</dt>
            <dd class="vb-mono">{{ detailed.reservedIp }}</dd>
          </div>
        </dl>
        <el-alert
          v-if="detailed.privateAddress"
          type="info"
          :closable="false"
          show-icon
          :title="t('dev.private')"
          :description="t('dev.privateHint')"
          class="vb-dev__alert"
        />
        <div class="vb-dev__actions vb-dev__actions--left">
          <el-button :disabled="frozen" @click="startRename(detailed); details = null">
            {{ detailed.name ? t('dev.rename') : t('dev.giveName') }}
          </el-button>
          <el-button
            v-if="remembered(detailed)"
            :disabled="frozen"
            :loading="saving === detailed.mac"
            @click="forget(detailed)"
          >
            {{ t('dev.forget') }}
          </el-button>
        </div>
        <p v-if="remembered(detailed)" class="vb-dev__hint">{{ t('dev.forgetHint') }}</p>
        <p v-if="rowError[detailed.mac]" class="vb-dev__err">{{ rowError[detailed.mac] }}</p>
      </template>
    </el-drawer>
  </section>
</template>

<style scoped>
.vb-dev {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.vb-dev__head {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-dev__head h1 {
  margin: 0;
  font-size: 22px;
}
.vb-dev__summary {
  display: flex;
  gap: 28px;
  align-items: baseline;
  flex-wrap: wrap;
}
.vb-dev__stat {
  display: flex;
  gap: 6px;
  align-items: baseline;
}
.vb-dev__stat strong {
  font-size: 28px;
  font-weight: 600;
  letter-spacing: -0.02em;
}
.vb-dev__stat span {
  color: var(--vb-muted);
}
.vb-dev__stat.is-new strong {
  color: var(--el-color-warning);
}
.vb-dev__knowall {
  margin-left: auto;
}
.vb-dev__hint {
  margin: 8px 0 0;
  font-size: 12px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
.vb-dev__err {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-color-error);
  white-space: normal;
}
.vb-dev__muted,
.vb-dev__name .vb-dev__muted {
  color: var(--vb-muted);
  font-weight: 500;
}
.vb-dev__small {
  font-size: 12px;
}
.vb-dev__btnico {
  margin-right: 6px;
}
.vb-dev__tools {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.vb-dev__search {
  flex: 1 1 260px;
  max-width: 420px;
}
.vb-dev__filters {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.vb-dev__list {
  display: flex;
  flex-direction: column;
  margin: 8px -20px 0;
  font-size: 13px;
}
.vb-dev__row {
  display: grid;
  grid-template-columns: minmax(220px, 1.6fr) minmax(170px, 1fr) 180px 200px;
  align-items: center;
  gap: 8px 14px;
  padding: 9px 20px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.vb-dev__row--th {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--vb-muted);
}
/* New devices are lit, as in the artboard: the question the screen answers
   first is "is anyone here I do not know". */
.vb-dev__row.is-new {
  background: var(--el-color-warning-light-9);
}
.vb-dev__cell--dev,
.vb-dev__cell--addr {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.vb-dev__name {
  display: flex;
  gap: 6px;
  align-items: center;
  flex-wrap: wrap;
  min-width: 0;
  overflow-wrap: anywhere;
}
.vb-dev__cell--link {
  display: flex;
  gap: 6px;
  align-items: center;
}
.vb-dev__pintag {
  align-self: flex-start;
}
.vb-dev__acts {
  display: flex;
  gap: 4px;
  align-items: center;
  justify-content: flex-end;
  flex-wrap: wrap;
}
.vb-dev__edit {
  grid-column: 1 / -1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-width: 520px;
}
.vb-dev__label {
  font-size: 12px;
  font-weight: 600;
}
.vb-dev__actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  flex-wrap: wrap;
  margin-top: 8px;
}
.vb-dev__actions--left {
  justify-content: flex-start;
}
.vb-dev__pager {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 10px;
}
.vb-dev__facts {
  display: grid;
  gap: 10px;
  margin: 0 0 14px;
}
.vb-dev__facts dt {
  font-size: 12px;
  color: var(--vb-muted);
}
.vb-dev__facts dd {
  margin: 2px 0 0;
  overflow-wrap: anywhere;
}
.vb-dev__line {
  display: block;
}
.vb-dev__alert {
  margin-bottom: 12px;
}
@media (width <= 1100px) {
  .vb-dev__row {
    grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr) 150px 170px;
  }
}
/* A phone: every device a card, the action kept (artboard Dev-360). */
@media (width <= 700px) {
  .vb-dev__list {
    gap: 10px;
    margin: 8px 0 0;
  }
  .vb-dev__list .vb-dev__row.vb-dev__row--th {
    display: none;
  }
  .vb-dev__row {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 6px;
    padding: 12px 14px;
    border: 1px solid var(--el-border-color-light);
    border-radius: 10px;
  }
  .vb-dev__acts {
    justify-content: flex-start;
  }
  .vb-dev__knowall {
    margin-left: 0;
  }
  .vb-dev__summary {
    gap: 16px;
  }
}
</style>
