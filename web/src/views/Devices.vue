<script setup lang="ts">
// The devices screen (#52), built to the accepted artboards Dev-* (design 08).
//
// It answers "who is on my network, and is anyone here I do not know" before
// the list is read: the summary counts online, new and nameless devices.
//
// Turning a device's internet off (#53, D-88) is a firewall change: it goes
// through the apply bar with its confirmation window, and the dialog before it
// says where the block stops. It is offered only where the router can do it
// (`internetControl`).
//
// An internet schedule (#54, D-89, D-98) goes the same way. The "Internet"
// column (artboard Dev-Main) says, by the router's own clock, whether the
// device has internet now and when that changes; while that clock is not
// checked against the internet it says so instead of pretending.
//
// Waking a device (#55) acts at once: it changes nothing on the router, the
// router only sends a packet. The panel cannot know whether the device woke,
// so it says the signal was sent and lets the row turn "online" by itself. A
// device last seen on Wi-Fi gets the reason instead of the action.
//
// Traffic (#56, D-92, D-99) is what the router counted since it started, in
// memory only; it sits in the Internet cell (as on the mid-width artboard) and
// says since when it runs, and that it is too low when offloading is on.
//
// A name and "I know this device" are the panel's own notes: they change
// nothing on the network and take effect at once (D-95). Reserving an address
// does change the network, so it goes through the apply bar like on 05.
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ApiError, api, type Device, type DeviceList } from '@/api/client'
import VbIcon from '@/components/VbIcon.vue'
import { fmtTraffic } from '@/lib/bytes'
import { useDuration } from '@/lib/duration'
import { overnight, scheduleWords, WEEKDAYS } from '@/lib/schedule'
import { rememberNames } from '@/stores/deviceNames'
import { refreshStaged, useLive } from '@/stores/live'

const { t, n, locale } = useI18n()
const { applyState, stale, lastUpdate, can } = useLive()
const { fmtAgo, fmtDuration } = useDuration()

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
    if (got) rememberNames(got.devices ?? [])
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
const order = ref<'new' | 'name' | 'traffic'>('new')
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
    if (order.value === 'traffic') {
      const d = (b.rxBytes ?? 0) + (b.txBytes ?? 0) - (a.rxBytes ?? 0) - (a.txBytes ?? 0)
      if (d !== 0) return d
    }
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

// --- internet off and back on (through the apply bar, #53) ---------------

const canBlock = computed(() => !!list.value?.internetControl)
const blocking = ref<Device | null>(null)
const blockError = ref('')
const blockOpen = computed({
  get: () => blocking.value !== null,
  set: (v: boolean) => {
    if (!v) blocking.value = null
  },
})

function askBlock(d: Device) {
  blockError.value = ''
  blocking.value = d
}

async function stageInternet(d: Device, allowed: boolean): Promise<boolean> {
  saving.value = d.mac
  rowError.value = {}
  try {
    await api.stageDeviceInternet(d.mac, allowed)
    await Promise.all([load(), refreshStaged()])
    return true
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    if (allowed) rowError.value[d.mac] = msg
    else blockError.value = msg
    return false
  } finally {
    saving.value = ''
  }
}

// --- waking (at once, #55) ------------------------------------------------

const canWake = computed(() => !!list.value?.wakeControl)
const traffic = computed(() => list.value?.traffic ?? null)
const inetCol = computed(() => canBlock.value || !!traffic.value)
const trafficLine = (d: Device) =>
  t('dev.rxtx', { rx: fmtTraffic(n, d.rxBytes), tx: fmtTraffic(n, d.txBytes) })
const trafficSince = computed(() => {
  const tr = traffic.value
  if (!tr) return ''
  return tr.sinceBoot
    ? t('dev.trafficSinceBoot')
    : t('dev.trafficSinceLast', { ago: fmtDuration(tr.sinceSec) })
})
const rowNote = ref<Record<string, string>>({})
/** Worth offering: not heard now, and not a device that sleeps on Wi-Fi. */
const wakeable = (d: Device) => !d.online && d.link.kind !== 'wifi'
const wifiAsleep = (d: Device) => !d.online && d.link.kind === 'wifi'

async function wake(d: Device) {
  saving.value = d.mac
  rowError.value = {}
  rowNote.value = {}
  try {
    await api.wakeDevice(d.mac)
    rowNote.value[d.mac] = t('dev.wakeSent')
  } catch (e) {
    rowError.value[d.mac] = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = ''
  }
}

// --- schedule (through the apply bar, #54) -------------------------------

const clock = computed(() => list.value?.clock ?? null)
const scheduling = ref<Device | null>(null)
const schedForm = ref<{ days: string[]; from: string; to: string }>({
  days: [],
  from: '22:00',
  to: '07:00',
})
const schedError = ref('')
const schedOpen = computed({
  get: () => scheduling.value !== null,
  set: (v: boolean) => {
    if (!v) scheduling.value = null
  },
})

function askSchedule(d: Device) {
  schedError.value = ''
  schedForm.value = d.schedule
    ? { days: [...(d.schedule.days ?? [])], from: d.schedule.from, to: d.schedule.to }
    : { days: ['mon', 'tue', 'wed', 'thu', 'fri'], from: '22:00', to: '07:00' }
  scheduling.value = d
}

function toggleDay(day: string) {
  const days = schedForm.value.days
  schedForm.value.days = days.includes(day) ? days.filter((d) => d !== day) : [...days, day]
}

async function saveSchedule(remove: boolean) {
  const d = scheduling.value
  if (!d) return
  saving.value = d.mac
  schedError.value = ''
  try {
    if (remove) await api.removeDeviceSchedule(d.mac)
    else await api.stageDeviceSchedule(d.mac, schedForm.value)
    await Promise.all([load(), refreshStaged()])
    scheduling.value = null
  } catch (e) {
    schedError.value =
      e instanceof ApiError && Object.keys(e.fields).length
        ? `${e.message} — ${Object.values(e.fields).join('; ')}`
        : e instanceof Error
          ? e.message
          : String(e)
  } finally {
    saving.value = ''
  }
}

/** The "Internet" cell: a tag and, under it, what happens next. */
function inet(d: Device): { tag: string; type: '' | 'danger' | 'warning' | 'info'; cap: string } {
  if (d.internet === 'blocked') return { tag: t('dev.inetOffTag'), type: 'danger', cap: '' }
  if (d.internet === 'scheduled') {
    if (!clock.value?.synced)
      return { tag: t('dev.schedTag'), type: 'warning', cap: t('dev.schedNoSync') }
    if (d.offBySchedule) {
      return {
        tag: t('dev.schedOffTag'),
        type: 'danger',
        cap: d.scheduleChangeAt ? t('dev.schedBackAt', { at: d.scheduleChangeAt }) : '',
      }
    }
    return {
      tag: t('dev.schedTag'),
      type: 'info',
      cap: d.scheduleChangeAt ? t('dev.schedOnNow', { at: d.scheduleChangeAt }) : '',
    }
  }
  return { tag: '', type: '', cap: t('dev.inetYes') }
}

async function confirmBlock() {
  const d = blocking.value
  if (d && (await stageInternet(d, false))) blocking.value = null
}

function onMenu(d: Device, cmd: string) {
  if (cmd === 'rename') startRename(d)
  else if (cmd === 'know') void know(d)
  else if (cmd === 'pin') void pin(d)
  else if (cmd === 'unpin') void unpin(d)
  else if (cmd === 'details') details.value = d.mac
  else if (cmd === 'inetOff') askBlock(d)
  else if (cmd === 'inetOn') void stageInternet(d, true)
  else if (cmd === 'schedule') askSchedule(d)
  else if (cmd === 'wake') void wake(d)
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
            <el-radio-button v-if="traffic" value="traffic">{{ t('dev.sortTraffic') }}</el-radio-button>
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
            <span v-if="inetCol">{{ traffic ? t('dev.colInternetTraffic') : t('dev.colInternet') }}</span>
            <span />
          </div>
          <div
            v-for="d in paged"
            :key="d.mac"
            class="vb-dev__row"
            :class="{ 'is-new': d.new, 'is-off': !d.online, 'has-inet': inetCol }"
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
                  <el-tag v-if="d.here" size="small" type="success" effect="light">
                    {{ t('dev.here') }}
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
              <span v-if="inetCol" class="vb-dev__cell--inet">
                <el-tag v-if="canBlock && inet(d).tag" size="small" :type="inet(d).type || undefined" effect="light">
                  {{ inet(d).tag }}
                </el-tag>
                <span
                  v-if="canBlock && inet(d).cap"
                  :class="inet(d).tag ? 'vb-dev__small vb-dev__muted' : 'vb-dev__muted'"
                >
                  {{ inet(d).cap }}
                </span>
                <span v-if="traffic" class="vb-dev__small vb-dev__muted vb-mono">{{ trafficLine(d) }}</span>
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
                      <template v-if="canBlock">
                        <el-dropdown-item
                          v-if="d.internet === 'blocked'"
                          command="inetOn"
                          :disabled="busy"
                        >
                          {{ t('dev.inetOn') }}
                        </el-dropdown-item>
                        <el-dropdown-item v-else command="inetOff" :disabled="busy">
                          {{ t('dev.inetOff') }}
                        </el-dropdown-item>
                        <el-dropdown-item command="schedule" :disabled="busy">
                          {{ d.schedule ? t('dev.scheduleEdit') : t('dev.schedule') }}
                        </el-dropdown-item>
                      </template>
                      <el-dropdown-item v-if="canWake && wakeable(d)" command="wake">
                        {{ t('dev.wake') }}
                      </el-dropdown-item>
                      <el-dropdown-item v-else-if="canWake && wifiAsleep(d)" disabled>
                        {{ t('dev.wakeWifi') }}
                      </el-dropdown-item>
                      <el-dropdown-item command="details" divided>{{ t('dev.details') }}</el-dropdown-item>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
                <p v-if="rowError[d.mac]" class="vb-dev__err">{{ rowError[d.mac] }}</p>
                <p v-if="rowNote[d.mac]" class="vb-dev__hint vb-dev__note">{{ rowNote[d.mac] }}</p>
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
          <template v-if="trafficSince"> {{ trafficSince }}</template>
        </p>
        <el-alert
          v-if="traffic?.partial"
          type="warning"
          :closable="false"
          show-icon
          :title="t('dev.trafficPartial')"
          class="vb-dev__alert"
        />
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
          <div>
            <dt>{{ t('dev.dInternet') }}</dt>
            <dd>
              {{ detailed.internet === 'blocked' ? t('dev.dInternetOff') : t('dev.dInternetOn') }}
            </dd>
          </div>
          <div v-if="detailed.schedule">
            <dt>{{ t('dev.dSchedule') }}</dt>
            <dd>{{ scheduleWords(t, detailed.schedule) }}</dd>
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
          <template v-if="canBlock">
            <el-button
              v-if="detailed.internet === 'blocked'"
              :disabled="frozen || busy"
              :loading="saving === detailed.mac"
              @click="stageInternet(detailed, true)"
            >
              {{ t('dev.inetOn') }}
            </el-button>
            <el-button v-else :disabled="frozen || busy" @click="askBlock(detailed)">
              {{ t('dev.inetOff') }}
            </el-button>
            <el-button :disabled="frozen || busy" @click="askSchedule(detailed)">
              {{ detailed.schedule ? t('dev.scheduleEdit') : t('dev.schedule') }}
            </el-button>
          </template>
          <el-button
            v-if="canWake && wakeable(detailed)"
            :disabled="frozen"
            :loading="saving === detailed.mac"
            @click="wake(detailed)"
          >
            {{ t('dev.wake') }}
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
        <p v-if="canWake && wifiAsleep(detailed)" class="vb-dev__hint">{{ t('dev.wakeWifiHint') }}</p>
        <p v-if="rowNote[detailed.mac]" class="vb-dev__hint">{{ rowNote[detailed.mac] }}</p>
        <p v-if="rowError[detailed.mac]" class="vb-dev__err">{{ rowError[detailed.mac] }}</p>
      </template>
    </el-drawer>

    <!-- Turning internet off says where it stops BEFORE the apply bar
         (D-88, design 08 §3 and §6): the boundary, a private address that
         may slip out of the block, and "this is the device you are on". -->
    <el-dialog
      v-model="blockOpen"
      :title="blocking ? t('dev.blockTitle', { name: displayName(blocking) || blocking.mac }) : ''"
      width="min(520px, 94vw)"
      append-to-body
    >
      <template v-if="blocking">
        <p class="vb-dev__blocktext">{{ t('dev.blockBoundary') }}</p>
        <el-alert
          v-if="blocking.here"
          type="warning"
          :closable="false"
          show-icon
          :title="t('dev.blockHere')"
          class="vb-dev__alert"
        />
        <el-alert
          v-if="blocking.privateAddress"
          type="info"
          :closable="false"
          show-icon
          :title="t('dev.blockPrivate')"
          class="vb-dev__alert"
        />
        <p class="vb-dev__hint">{{ t('dev.blockApplyHint') }}</p>
        <p v-if="blockError" class="vb-dev__err">{{ blockError }}</p>
      </template>
      <template #footer>
        <el-button @click="blocking = null">{{ t('dev.cancel') }}</el-button>
        <el-button
          type="danger"
          :loading="!!blocking && saving === blocking.mac"
          :disabled="busy"
          @click="confirmBlock"
        >
          {{ t('dev.blockConfirm') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- The schedule (artboard Dev-Schedule): days as buttons, "from — to",
         the router's clock beside it (D-89, D-98). -->
    <el-dialog
      v-model="schedOpen"
      :title="scheduling ? t('dev.schedTitle', { name: displayName(scheduling) || scheduling.mac }) : ''"
      width="min(560px, 94vw)"
      append-to-body
    >
      <template v-if="scheduling">
        <p class="vb-dev__label">{{ t('dev.schedDays') }}</p>
        <div class="vb-dev__days" role="group" :aria-label="t('dev.schedDays')">
          <el-button
            v-for="day in WEEKDAYS"
            :key="day"
            size="small"
            :type="schedForm.days.includes(day) ? 'primary' : ''"
            :aria-pressed="schedForm.days.includes(day)"
            @click="toggleDay(day)"
          >
            {{ t(`dev.days.short.${day}`) }}
          </el-button>
        </div>
        <p class="vb-dev__label">{{ t('dev.schedFromTo') }}</p>
        <div class="vb-dev__times">
          <el-time-select
            v-model="schedForm.from"
            start="00:00"
            step="00:15"
            end="23:45"
            :clearable="false"
            class="vb-dev__time"
          />
          <span class="vb-dev__muted">—</span>
          <el-time-select
            v-model="schedForm.to"
            start="00:00"
            step="00:15"
            end="23:45"
            :clearable="false"
            class="vb-dev__time"
          />
          <span v-if="overnight(schedForm)" class="vb-dev__hint">
            {{ t('dev.schedNextMorning', { to: schedForm.to }) }}
          </span>
        </div>
        <el-alert
          v-if="clock"
          :type="clock.synced ? 'info' : 'warning'"
          :closable="false"
          show-icon
          :title="
            t(clock.synced ? 'dev.schedClockOk' : 'dev.schedClockNo', {
              now: clock.now,
              tz: clock.timezone || 'UTC',
            })
          "
          class="vb-dev__alert"
        />
        <el-alert
          v-if="scheduling.internet === 'blocked'"
          type="info"
          :closable="false"
          show-icon
          :title="t('dev.schedBlocked')"
          class="vb-dev__alert"
        />
        <el-alert
          v-if="scheduling.privateAddress"
          type="info"
          :closable="false"
          show-icon
          :title="t('dev.schedPrivate')"
          class="vb-dev__alert"
        />
        <p class="vb-dev__hint">{{ t('dev.schedHint') }}</p>
        <p class="vb-dev__hint">{{ t('dev.schedApplyHint') }}</p>
        <p v-if="schedError" class="vb-dev__err">{{ schedError }}</p>
      </template>
      <template #footer>
        <div class="vb-dev__schedfoot">
          <el-button
            v-if="scheduling?.schedule"
            text
            :disabled="busy"
            :loading="!!scheduling && saving === scheduling.mac"
            @click="saveSchedule(true)"
          >
            {{ t('dev.schedRemove') }}
          </el-button>
          <span class="vb-dev__grow" />
          <el-button @click="scheduling = null">{{ t('dev.cancel') }}</el-button>
          <el-button
            type="primary"
            :disabled="busy || !schedForm.days.length"
            :loading="!!scheduling && saving === scheduling.mac"
            @click="saveSchedule(false)"
          >
            {{ t('dev.schedSave') }}
          </el-button>
        </div>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped>
.vb-dev__blocktext {
  margin: 0 0 12px;
  line-height: 1.5;
}
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
/* With the "Internet" column (#54): one more cell, the name keeps the most. */
.vb-dev__row.has-inet,
.vb-dev__list:has(.has-inet) .vb-dev__row--th {
  grid-template-columns: minmax(200px, 1.5fr) minmax(150px, 1fr) 170px minmax(170px, 1fr) 150px;
}
.vb-dev__cell--inet {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  min-width: 0;
}
.vb-dev__days {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 4px 0 14px;
}
.vb-dev__days .el-button + .el-button {
  margin-left: 0;
}
.vb-dev__times {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin: 4px 0 14px;
}
.vb-dev__time {
  width: 120px;
}
.vb-dev__schedfoot {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.vb-dev__schedfoot .el-button + .el-button {
  margin-left: 0;
}
.vb-dev__grow {
  flex: 1;
}
.vb-dev__note {
  flex-basis: 100%;
  margin: 0;
  text-align: right;
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
  .vb-dev__row.has-inet,
  .vb-dev__list:has(.has-inet) .vb-dev__row--th {
    grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) 140px minmax(0, 1fr) 120px;
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
  .vb-dev__note {
    text-align: left;
  }
  .vb-dev__knowall {
    margin-left: 0;
  }
  .vb-dev__summary {
    gap: 16px;
  }
}
</style>
