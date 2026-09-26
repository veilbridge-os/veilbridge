<script setup lang="ts">
// The local network screen (M3.2), built to the accepted artboard
// `Lan-Main.dc.html` in the mockup project.
//
// It is the second screen that writes, and the first where the danger is not
// uniform: moving the pool or the lease time cannot disconnect anyone, while
// moving the router's own address disconnects everybody standing in this
// network — including, often, the person reading the screen. So the two live
// in two cards with two different tones (D-51). A timer on every edit teaches
// people to click through timers; this screen spends that attention once.
//
// Like the uplink screen, nothing here applies anything: saving produces a
// draft on the device, and the apply bar commits it under the watchdog.
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  type AddressLease,
  ApiError,
  api,
  type LANStatus,
  type ReservedAddress,
} from '@/api/client'
import VbIcon from '@/components/VbIcon.vue'
import { useDuration } from '@/lib/duration'
import { refreshStaged, useLive } from '@/stores/live'

const { t } = useI18n()
const { applyState, staged, stale, lastUpdate } = useLive()
// How long a change will have to be confirmed, from the daemon (#29): the
// warning is read BEFORE Apply, so it cannot quote a number from the UI; until
// the daemon has said, the sentence that needs the number is not shown.
const windowSeconds = computed(() => applyState.value?.window_seconds ?? 0)
const { fmtDuration, fmtAgo } = useDuration()

const lan = ref<LANStatus | null>(null)
const loaded = ref(false)
// Three different "there is nothing to show", and they are three different
// sentences: the device has no local network (404), this platform cannot read
// one (501), or we simply could not reach the device.
const unsupported = ref(false)
const loadError = ref('')

// Same reasoning as the uplink screen: the local network changes when a person
// changes it, so it is polled slowly instead of pushed.
const SLOW_POLL_MS = 15_000
let poll = 0

async function load() {
  try {
    lan.value = await api.lan()
    unsupported.value = false
    loadError.value = ''
  } catch (e) {
    if (e instanceof ApiError && e.status === 501) {
      unsupported.value = true
    } else if (!lan.value) {
      // Keep the last good reading if we have one: the shell already says the
      // device stopped answering, and blanking the screen would read as "the
      // local network went away".
      loadError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    loaded.value = true
  }
}

onMounted(() => {
  void load()
  void refreshStaged()
  poll = window.setInterval(load, SLOW_POLL_MS)
})
onUnmounted(() => window.clearInterval(poll))

const iface = computed(() => lan.value?.interface)
const handout = computed(() => lan.value?.handout)
const leases = computed<AddressLease[]>(() => lan.value?.leases ?? [])
const reserved = computed<ReservedAddress[]>(() => lan.value?.reserved ?? [])
const hasLAN = computed(() => !!lan.value)
const handingOut = computed(() => handout.value?.enabled === true)

const draftCount = computed(() => staged.value.length)
const busy = computed(() => applyState.value?.phase === 'awaiting_confirm')

const dataFrom = computed(() =>
  lastUpdate.value
    ? lastUpdate.value.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    : '',
)

// --- the network this router sits in -----------------------------------

const cidr = computed(() => iface.value?.ipv4?.[0] ?? '')
const routerAddress = computed(() => cidr.value.split('/')[0] ?? '')
const prefix = computed(() => {
  const p = Number(cidr.value.split('/')[1])
  return Number.isFinite(p) ? p : NaN
})

function ipToUint(ip: string): number | null {
  const parts = ip.split('.')
  if (parts.length !== 4) return null
  let out = 0
  for (const p of parts) {
    const n = Number(p)
    if (!/^\d{1,3}$/.test(p) || n > 255) return null
    out = out * 256 + n
  }
  return out
}

function maskFromPrefix(bits: number): string {
  const m = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0
  return [24, 16, 8, 0].map((s) => (m >>> s) & 255).join('.')
}

const netmask = computed(() => (Number.isNaN(prefix.value) ? '' : maskFromPrefix(prefix.value)))

/** inThisNetwork answers whether an address belongs to the local network. */
function inThisNetwork(ip: string): boolean {
  const a = ipToUint(ip)
  const r = ipToUint(routerAddress.value)
  if (a === null || r === null || Number.isNaN(prefix.value)) return false
  const m = prefix.value === 0 ? 0 : (0xffffffff << (32 - prefix.value)) >>> 0
  return ((a ^ r) & m) === 0
}

/** Whether the person reading this screen is standing INSIDE the network they
 * are about to renumber. It changes what is true, not just the wording: from
 * the uplink side (how both stands are reached) changing the local address
 * does not touch the operator's own link at all, and warning them that it
 * might is the kind of untrue caution that gets ignored later.
 *
 * A name in the address bar is not resolvable to an address here, so it is
 * treated as "unknown" and gets the cautious wording. */
/** Where the panel will be after the change: the new address on the port this
 * page was opened on. "Reconnect" alone was a consequence, not a way back (#31). */
const newPanelUrl = computed(() => {
  const port = window.location.port ? `:${window.location.port}` : ''
  return `${window.location.protocol}//${lanForm.value.address.trim() || routerAddress.value}${port}`
})

const insideThisNetwork = computed(() => {
  const host = window.location.hostname
  if (ipToUint(host) === null) return true
  return inThisNetwork(host)
})

// --- the proof that the handout actually works --------------------------

/** The most recently issued (or renewed) lease. The lease file gives time
 * LEFT, and the handout gives how long a lease is granted for, so the age of
 * the newest lease is the difference — derived, not invented, and shown only
 * when both numbers are there and agree. */
const freshest = computed(() => {
  const total = handout.value?.leaseSeconds ?? 0
  if (!total) return null
  let best: { lease: AddressLease; ago: number } | null = null
  for (const l of leases.value) {
    if (!l.expiresSec || l.expiresSec > total) continue
    const ago = total - l.expiresSec
    if (!best || ago < best.ago) best = { lease: l, ago }
  }
  return best
})

// --- clients, including the ones that are not here right now -------------

interface Row {
  mac: string
  ip: string
  name: string
  /** seconds left on the lease, absent for a device that is not connected. */
  expiresSec?: number
  /** the reservation this device has, if any. */
  reservation?: ReservedAddress
  online: boolean
}

const rows = computed<Row[]>(() => {
  const byMac = new Map<string, Row>()
  for (const l of leases.value) {
    byMac.set(l.mac.toLowerCase(), {
      mac: l.mac,
      ip: l.ip,
      name: l.hostname ?? '',
      expiresSec: l.expiresSec,
      online: true,
    })
  }
  for (const r of reserved.value) {
    const key = r.mac.toLowerCase()
    const row = byMac.get(key)
    if (row) {
      row.reservation = r
      // A reservation the device has not handed out yet would otherwise show
      // the old address as if it were already in force.
      if (r.ip !== row.ip) row.ip = `${row.ip} → ${r.ip}`
      continue
    }
    // A reserved device that is not connected still has to be on this screen:
    // otherwise the only way to undo a reservation is to wait for its owner to
    // come back, and a wrong reservation is exactly the thing people want to
    // undo immediately. Not in the artboard — recorded as an addition.
    byMac.set(key, { mac: r.mac, ip: r.ip, name: r.name ?? '', reservation: r, online: false })
  }
  return [...byMac.values()]
})

const onlineCount = computed(() => leases.value.length)

/** A lease the device lists with no time left: it was handed out, and the
 * device has not renewed it since — most likely it is gone (#33). Shown as
 * such rather than as a dash, which read like missing data. */
const expired = (row: Row) => row.online && !row.reservation && row.expiresSec === 0

/** The far end of the pool, shortened to the part that differs "192.168.1.100
 * \u2013 .249", as the artboard writes it. The shared part is on screen one
 * centimetre to the left, so repeating it costs a line break and buys
 * nothing; when the ends do NOT share a prefix the address is written out in
 * full, because then the tail alone would be a riddle. */
const poolLastShort = computed(() => {
  const first = handout.value?.first ?? ''
  const last = handout.value?.last ?? ''
  const a = first.split('.')
  const b = last.split('.')
  if (a.length !== 4 || b.length !== 4) return last
  return a[0] === b[0] && a[1] === b[1] && a[2] === b[2] ? `.${b[3]}` : last
})

// --- the handout form ----------------------------------------------------

// The three lease times of the artboard. A device set to anything else keeps
// its own value as a fourth option rather than being silently rounded into
// one of these.
const LEASE_CHOICES = [3600, 43200, 86400]

const handoutForm = ref({ enabled: true, first: '', last: '', leaseSeconds: 43200 })
const lanForm = ref({ address: '', netmask: '' })

const leaseOptions = computed(() => {
  const own = handout.value?.leaseSeconds ?? 0
  const all = LEASE_CHOICES.includes(own) || !own ? LEASE_CHOICES : [...LEASE_CHOICES, own]
  return all.sort((a, b) => a - b)
})

function leaseLabel(sec: number): string {
  if (sec === 3600) return t('lan.lease1h')
  if (sec === 43200) return t('lan.lease12h')
  if (sec === 86400) return t('lan.lease24h')
  return fmtDuration(sec)
}

/** Fill both forms from the device, and refill them whenever the device's own
 * values change — but never while the operator has a draft in flight or is
 * typing on top of a live transaction, which would throw their input away. */
watch(
  [() => lan.value, draftCount],
  ([status, drafts]) => {
    if (!status || drafts > 0 || busy.value) return
    handoutForm.value = {
      enabled: status.handout.enabled,
      first: status.handout.first ?? '',
      last: status.handout.last ?? '',
      leaseSeconds: status.handout.leaseSeconds || 43200,
    }
    lanForm.value = { address: routerAddress.value, netmask: netmask.value }
  },
  { immediate: true },
)

const handoutChanged = computed(() => {
  const h = handout.value
  if (!h) return false
  return (
    handoutForm.value.enabled !== h.enabled ||
    handoutForm.value.first !== (h.first ?? '') ||
    handoutForm.value.last !== (h.last ?? '') ||
    handoutForm.value.leaseSeconds !== (h.leaseSeconds || 43200)
  )
})

const lanChanged = computed(
  () => lanForm.value.address !== routerAddress.value || lanForm.value.netmask !== netmask.value,
)

// --- refusals ------------------------------------------------------------

// The device is the one validator (same rule as the uplink screen), so none
// of its rules are copied here. It also says which field a refusal is about
// (`errors[].location`, #28), and that is the only thing used to place it:
// this screen used to match the quoted value and the word "mask", which
// breaks the moment the device words a sentence differently. A refusal about
// a field this card does not show stays at card level.
const handoutErrors = ref<Record<string, string>>({})
const handoutError = ref('')
const lanErrors = ref<Record<string, string>>({})
const lanError = ref('')
const manualErrors = ref<Record<string, string>>({})
const rowError = ref<Record<string, string>>({})
const savedCard = ref('')

type RefusedField = 'address' | 'netmask' | 'first' | 'last' | 'mac' | 'ip' | 'name'

// Spelled out rather than built as `lan.bad.${field}`: a template key is not
// type-checked, and one built that way rendered as the raw key on the stand.
const REFUSED: Record<RefusedField, () => string> = {
  address: () => t('lan.bad.address'),
  netmask: () => t('lan.bad.netmask'),
  first: () => t('lan.bad.first'),
  last: () => t('lan.bad.last'),
  mac: () => t('lan.bad.mac'),
  ip: () => t('lan.bad.ip'),
  name: () => t('lan.bad.name'),
}

/** place puts a refusal at the first of `shown` it names, or returns false.
 * The field gets this panel's sentence in the interface language, then the
 * device's own words: they are English (a daemon has no locale) but carry the
 * specifics — which address, which network. */
function place(e: unknown, shown: RefusedField[], into: Record<string, string>): boolean {
  if (!(e instanceof ApiError)) return false
  const field = shown.find((f) => e.fields[f])
  if (!field) return false
  into[field] = `${REFUSED[field]()} — ${t('lan.deviceSaid', { detail: e.fields[field] })}`
  return true
}

function clearMessages() {
  handoutErrors.value = {}
  handoutError.value = ''
  lanErrors.value = {}
  lanError.value = ''
  manualErrors.value = {}
  rowError.value = {}
  savedCard.value = ''
}

const savingHandout = ref(false)
const savingLAN = ref(false)
const pinning = ref('')

async function saveHandout() {
  savingHandout.value = true
  clearMessages()
  try {
    await api.stageHandout({
      enabled: handoutForm.value.enabled,
      first: handoutForm.value.first.trim(),
      last: handoutForm.value.last.trim(),
      leaseSeconds: handoutForm.value.leaseSeconds,
    })
    savedCard.value = 'handout'
  } catch (e) {
    const detail = e instanceof Error ? e.message : String(e)
    if (e instanceof ApiError && e.status === 409) {
      handoutError.value = t('lan.busyHint')
    } else if (!place(e, ['first', 'last'], handoutErrors.value)) {
      handoutError.value = detail
    }
  } finally {
    savingHandout.value = false
    await refreshStaged()
  }
}

async function saveLAN() {
  savingLAN.value = true
  clearMessages()
  try {
    await api.stageLAN({
      address: lanForm.value.address.trim(),
      netmask: lanForm.value.netmask.trim(),
    })
    savedCard.value = 'lan'
  } catch (e) {
    const detail = e instanceof Error ? e.message : String(e)
    if (e instanceof ApiError && e.status === 409) {
      lanError.value = t('lan.busyHint')
    } else if (!place(e, ['address', 'netmask'], lanErrors.value)) {
      lanError.value = detail
    }
  } finally {
    savingLAN.value = false
    await refreshStaged()
  }
}

/** Pinning from a row sends the hardware address and the address it already
 * holds, and deliberately NOT the name the client announced: that name would
 * go into DNS for the whole network, which is a decision the operator makes in
 * the manual form, not a side effect of clicking "keep this address". */
async function pin(row: Row) {
  pinning.value = row.mac
  clearMessages()
  try {
    await api.stageReservation({ mac: row.mac, ip: row.ip })
  } catch (e) {
    rowError.value[row.mac] = e instanceof Error ? e.message : String(e)
  } finally {
    pinning.value = ''
    await refreshStaged()
  }
}

async function unpin(row: Row) {
  const id = row.reservation?.id
  if (!id) return
  pinning.value = row.mac
  clearMessages()
  try {
    await api.removeReservation(id)
  } catch (e) {
    rowError.value[row.mac] = e instanceof Error ? e.message : String(e)
  } finally {
    pinning.value = ''
    await refreshStaged()
  }
}

// --- pinning an address by hand -----------------------------------------

const manualOpen = ref(false)
const manual = ref({ mac: '', ip: '', name: '' })
const manualError = ref('')
const manualSaving = ref(false)

function openManual() {
  manual.value = { mac: '', ip: '', name: '' }
  manualError.value = ''
  manualErrors.value = {}
  manualOpen.value = true
}

async function saveManual() {
  manualSaving.value = true
  manualError.value = ''
  manualErrors.value = {}
  try {
    await api.stageReservation({
      mac: manual.value.mac.trim(),
      ip: manual.value.ip.trim(),
      ...(manual.value.name.trim() ? { name: manual.value.name.trim() } : {}),
    })
    manualOpen.value = false
  } catch (e) {
    if (!place(e, ['mac', 'ip', 'name'], manualErrors.value)) {
      manualError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    manualSaving.value = false
    await refreshStaged()
  }
}

async function discard() {
  clearMessages()
  try {
    await api.discardStaged()
  } catch (e) {
    handoutError.value = e instanceof Error ? e.message : String(e)
  }
  await refreshStaged()
  await load()
}
</script>

<template>
  <section class="vb-lan">
    <header class="vb-lan__head">
      <h1>{{ t('lan.title') }}</h1>
      <el-tag v-if="draftCount" type="warning" size="small" effect="light">
        {{ t('lan.draftPending', { n: draftCount }, draftCount) }}
      </el-tag>
      <el-tag v-else-if="loaded && hasLAN" :type="handingOut ? 'success' : 'info'" size="small">
        {{ handingOut ? t('lan.handingOut') : t('lan.handoutOff') }}
      </el-tag>
      <span class="vb-lan__spacer" />
      <el-button v-if="hasLAN" :disabled="busy" @click="openManual">
        <VbIcon name="plus" size="sm" class="vb-lan__btnico" />
        {{ t('lan.pinByHand') }}
      </el-button>
    </header>

    <!-- 1. First load: a skeleton, never a form full of zeroes. -->
    <el-card v-if="!loaded" shadow="never">
      <el-skeleton :rows="3" animated />
    </el-card>

    <!-- 2. This platform cannot answer. A limitation of our code, said as
         one: it must not be dressed up as a fact about the hardware (D-20). -->
    <el-card v-else-if="unsupported" shadow="never">
      <el-empty :description="t('lan.unsupported')">
        <p class="vb-lan__hint">{{ t('lan.unsupportedHint') }}</p>
      </el-empty>
    </el-card>

    <!-- 3. The device has no local network. A state of the box, not an error:
         an x86 gateway with one interface looks exactly like this. -->
    <el-card v-else-if="!hasLAN" shadow="never">
      <el-empty :description="t('lan.none')">
        <p class="vb-lan__hint">{{ loadError || t('lan.noneHint') }}</p>
      </el-empty>
    </el-card>

    <template v-else>
      <!-- 4. What the local network is right now, with the proof under the
           claim: the handout is called working only when something was
           actually handed out (D-5, NFR-5). -->
      <el-card shadow="never">
        <div class="vb-lan__headline">
          <VbIcon
            :name="handingOut ? 'check' : 'alert'"
            size="lg"
            :class="handingOut ? 'vb-lan__ok' : 'vb-lan__warn'"
          />
          <strong>
            {{
              !handingOut
                ? t('lan.summaryOff')
                : onlineCount
                  ? t('lan.summaryOn', { n: onlineCount }, onlineCount)
                  : t('lan.summaryNone')
            }}
          </strong>
          <el-tag v-if="stale" size="small" type="info">
            {{ t('shell.dataFrom', { time: dataFrom }) }}
          </el-tag>
        </div>

        <el-alert
          v-if="freshest"
          type="success"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="
            t('lan.proofIssued', { ago: fmtAgo(freshest.ago), mac: freshest.lease.mac })
          "
        />
        <el-alert
          v-else-if="handingOut && leases.length === 0"
          type="info"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="t('lan.proofNoClients')"
        />
        <el-alert
          v-else-if="handingOut"
          type="info"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="t('lan.proofUnknown')"
        />
        <el-alert
          v-else
          type="warning"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="t('lan.proofOff')"
        />

        <dl class="vb-facts">
          <div class="vb-facts__i">
            <dt>{{ t('lan.routerHere') }}</dt>
            <dd class="vb-mono">{{ cidr || '—' }}</dd>
          </div>
          <div v-if="handingOut" class="vb-facts__i">
            <dt>{{ t('lan.pool') }}</dt>
            <dd class="vb-mono">{{ handout?.first }} – {{ poolLastShort }}</dd>
          </div>
          <div v-if="handingOut" class="vb-facts__i">
            <dt>{{ t('lan.leaseTime') }}</dt>
            <dd>{{ fmtDuration(handout?.leaseSeconds) }}</dd>
          </div>
          <div class="vb-facts__i">
            <dt>{{ t('lan.onlineNow') }}</dt>
            <dd>{{ t('lan.nDevices', { n: onlineCount }, onlineCount) }}</dd>
          </div>
          <div class="vb-facts__i">
            <dt>{{ t('lan.pinnedCount') }}</dt>
            <dd>{{ t('lan.nAddresses', { n: reserved.length }, reserved.length) }}</dd>
          </div>
        </dl>
      </el-card>

      <!-- 5. The devices. One table, with pinning as a property of the row:
           a separate list of reservations would make a person compare two
           tables by eye to answer one question. -->
      <el-card shadow="never">
        <template #header>
          <div class="vb-lan__cardhead">
            <VbIcon name="dev" class="vb-lan__muted" />
            <strong>{{ t('lan.devices') }}</strong>
            <el-tag size="small" type="info" class="vb-lan__tagright">
              {{ t('lan.devicesCount', { online: onlineCount, pinned: reserved.length }) }}
            </el-tag>
          </div>
        </template>

        <el-empty v-if="rows.length === 0" :description="t('lan.noDevices')">
          <p class="vb-lan__hint">{{ t('lan.noDevicesHint') }}</p>
        </el-empty>

        <!-- A grid of rows, not el-table: on a phone the same markup becomes
             cards, with the pin button kept (artboards Lan-360, #33); a table
             scrolled sideways put the button past the edge. -->
        <div v-else class="vb-lan__list" role="table">
          <div class="vb-lan__row vb-lan__row--th" role="row">
            <span>{{ t('lan.device') }}</span>
            <span>{{ t('lan.address') }}</span>
            <span>{{ t('lan.hardware') }}</span>
            <span>{{ t('lan.leaseLeftCol') }}</span>
            <span />
          </div>
          <div
            v-for="row in rows"
            :key="row.mac"
            class="vb-lan__row"
            :class="{ 'is-off': expired(row) || !row.online }"
            role="row"
          >
            <span class="vb-lan__cell--dev">
              <span class="vb-lan__dev">
                <VbIcon name="dev" size="sm" class="vb-lan__muted" />
                <strong v-if="row.name">{{ row.name }}</strong>
                <span v-else class="vb-lan__muted">{{ t('lan.noName') }}</span>
                <el-tag v-if="row.reservation" size="small">{{ t('lan.pinned') }}</el-tag>
                <el-tag v-if="!row.online" size="small" type="info">{{ t('lan.offline') }}</el-tag>
              </span>
              <span class="vb-mono vb-lan__muted vb-lan__macunder">{{ row.mac }}</span>
            </span>
            <span class="vb-mono vb-lan__cell--ip">{{ row.ip }}</span>
            <span class="vb-mono vb-lan__muted vb-lan__cell--mac">{{ row.mac }}</span>
            <span class="vb-lan__muted vb-lan__cell--left">
              <span class="vb-lan__label">{{ t('lan.leaseLeftCol') }}: </span>
              <template v-if="row.reservation">{{ t('lan.forever') }}</template>
              <el-tag v-else-if="expired(row)" size="small" type="info">{{ t('lan.expired') }}</el-tag>
              <template v-else-if="row.expiresSec">{{ t('lan.leaseLeft', { d: fmtDuration(row.expiresSec) }) }}</template>
              <template v-else>—</template>
            </span>
            <span class="vb-lan__acts">
              <el-button
                v-if="row.reservation"
                size="small"
                text
                :loading="pinning === row.mac"
                :disabled="busy"
                @click="unpin(row as Row)"
              >
                <VbIcon name="close" size="sm" class="vb-lan__btnico" />
                {{ t('lan.unpin') }}
              </el-button>
              <el-button
                v-else
                size="small"
                :loading="pinning === row.mac"
                :disabled="busy"
                @click="pin(row as Row)"
              >
                <VbIcon name="pin" size="sm" class="vb-lan__btnico" />
                {{ t('lan.pin') }}
              </el-button>
              <div v-if="rowError[row.mac]" class="vb-lan__rowerr">
                {{ t('lan.deviceSaid', { detail: rowError[row.mac] }) }}
              </div>
            </span>
          </div>
        </div>
        <p v-if="rows.length" class="vb-lan__hint">{{ t('lan.devicesHint') }}</p>
      </el-card>

      <!-- 6. The handout. Deliberately without a warning and without a
           confirmation timer: this edit cannot cut anybody off, and a timer
           spent here is a timer ignored on the card below (D-51). -->
      <el-card shadow="never">
        <template #header>
          <div class="vb-lan__cardhead">
            <strong>{{ t('lan.handoutTitle') }}</strong>
            <el-tag size="small" :type="draftCount ? 'warning' : 'info'" class="vb-lan__tagright">
              {{
                draftCount
                  ? t('lan.draftPending', { n: draftCount }, draftCount)
                  : t('lan.draftEmpty')
              }}
            </el-tag>
          </div>
        </template>

        <el-alert
          v-if="busy"
          type="warning"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="t('lan.busyTitle')"
          :description="t('lan.busyHint')"
        />

        <el-form label-position="top" :disabled="busy">
          <el-form-item :label="t('lan.handoutSwitch')">
            <el-switch v-model="handoutForm.enabled" />
            <div class="vb-lan__hint">
              {{ handoutForm.enabled ? t('lan.handoutOnHint') : t('lan.handoutOffHint') }}
            </div>
          </el-form-item>

          <div v-if="handoutForm.enabled" class="vb-lan__grid">
            <el-form-item :label="t('lan.first')" :error="handoutErrors.first">
              <el-input v-model="handoutForm.first" class="vb-mono" placeholder="192.168.1.100" />
            </el-form-item>
            <el-form-item :label="t('lan.last')" :error="handoutErrors.last">
              <el-input v-model="handoutForm.last" class="vb-mono" placeholder="192.168.1.249" />
            </el-form-item>
            <el-form-item :label="t('lan.leaseTime')">
              <el-radio-group v-model="handoutForm.leaseSeconds">
                <el-radio-button v-for="o in leaseOptions" :key="o" :value="o">
                  {{ leaseLabel(o) }}
                </el-radio-button>
              </el-radio-group>
            </el-form-item>
          </div>
        </el-form>

        <p v-if="handoutForm.enabled" class="vb-lan__hint">{{ t('lan.poolHint') }}</p>

        <el-alert
          v-if="handoutError"
          type="error"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="t('lan.refused')"
          :description="handoutError"
        />
        <el-alert
          v-else-if="savedCard === 'handout'"
          type="info"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="t('lan.saved')"
        />

        <div class="vb-lan__actions">
          <el-button :disabled="busy || !draftCount" @click="discard">
            {{ t('lan.cancel') }}
          </el-button>
          <el-button
            type="primary"
            :loading="savingHandout"
            :disabled="busy || !handoutChanged"
            @click="saveHandout"
          >
            {{ t('lan.save') }}
          </el-button>
        </div>
      </el-card>

      <!-- 7. The router's own address. The one edit on this screen that can
           take the network away from the person making it, so it is separated,
           framed in the warning colour, and says plainly what will happen. -->
      <el-card shadow="never" class="vb-lan__danger">
        <template #header>
          <div class="vb-lan__cardhead">
            <VbIcon name="alert" class="vb-lan__warn" />
            <strong>{{ t('lan.routerAddress') }}</strong>
            <el-tag size="small" type="warning" class="vb-lan__tagright">
              {{ t('lan.mayCutAccess') }}
            </el-tag>
          </div>
        </template>

        <el-form label-position="top" :disabled="busy">
          <div class="vb-lan__grid vb-lan__grid--two">
            <el-form-item :label="t('lan.address')" :error="lanErrors.address">
              <el-input v-model="lanForm.address" class="vb-mono" placeholder="192.168.1.1" />
            </el-form-item>
            <el-form-item :label="t('lan.netmask')" :error="lanErrors.netmask">
              <el-input v-model="lanForm.netmask" class="vb-mono" placeholder="255.255.255.0" />
            </el-form-item>
          </div>
        </el-form>

        <!-- Two different truths, and the screen tells the one that applies:
             from inside this network the panel moves out from under the
             operator, from outside it does not. -->
        <el-alert
          v-if="windowSeconds || !insideThisNetwork"
          :type="insideThisNetwork ? 'warning' : 'info'"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="insideThisNetwork ? t('lan.warnInside', { sec: windowSeconds, url: newPanelUrl }) : t('lan.warnOutside')"
        />

        <el-alert
          v-if="lanError"
          type="error"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="t('lan.refused')"
          :description="lanError"
        />
        <el-alert
          v-else-if="savedCard === 'lan'"
          type="info"
          :closable="false"
          show-icon
          class="vb-lan__alert"
          :title="t('lan.saved')"
        />

        <div class="vb-lan__actions">
          <el-button :disabled="busy || !draftCount" @click="discard">
            {{ t('lan.cancel') }}
          </el-button>
          <el-button
            type="primary"
            :loading="savingLAN"
            :disabled="busy || !lanChanged"
            @click="saveLAN"
          >
            {{ t('lan.save') }}
          </el-button>
        </div>
      </el-card>
    </template>

    <!-- Pinning by hand: for a device that is not connected right now, which
         is the common case for a printer or a camera being installed. -->
    <!-- A fixed 440 px dialog was clipped on a 360 px phone: it lives in an
         overlay, so the page itself never reported the overflow. -->
    <el-dialog
      v-model="manualOpen"
      :title="t('lan.pinByHand')"
      width="min(440px, calc(100vw - 24px))"
    >
      <el-form label-position="top">
        <el-form-item :label="t('lan.hardware')" :error="manualErrors.mac" required>
          <el-input v-model="manual.mac" class="vb-mono" placeholder="2c:44:fd:18:0b:71" />
          <div class="vb-lan__hint">{{ t('lan.macHint') }}</div>
        </el-form-item>
        <el-form-item :label="t('lan.address')" :error="manualErrors.ip" required>
          <el-input v-model="manual.ip" class="vb-mono" placeholder="192.168.1.50" />
        </el-form-item>
        <el-form-item :label="t('lan.nameOptional')" :error="manualErrors.name">
          <el-input v-model="manual.name" placeholder="printer-hp" />
          <div class="vb-lan__hint">{{ t('lan.nameHint') }}</div>
        </el-form-item>
      </el-form>
      <el-alert
        v-if="manualError"
        type="error"
        :closable="false"
        show-icon
        :title="t('lan.refused')"
        :description="manualError"
      />
      <template #footer>
        <el-button @click="manualOpen = false">{{ t('lan.close') }}</el-button>
        <el-button type="primary" :loading="manualSaving" @click="saveManual">
          {{ t('lan.save') }}
        </el-button>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped>
.vb-lan {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.vb-lan__head {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-lan__head h1 {
  margin: 0;
  font-size: 22px;
}
.vb-lan__spacer {
  flex: 1 1 auto;
}
.vb-lan__cardhead {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-lan__tagright {
  margin-left: auto;
}
/* The headline of the state card: 20/600 with the icon beside it, as measured
   on the artboard. */
.vb-lan__headline {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
  font-size: 20px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.vb-lan__alert {
  margin-bottom: 12px;
}
.vb-lan__hint {
  flex: 0 0 100%;
  width: 100%;
  margin: 4px 0 0;
  font-size: 12px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
.vb-lan__muted {
  color: var(--vb-muted);
}
.vb-lan__ok {
  color: var(--el-color-success);
}
.vb-lan__warn {
  color: var(--el-color-warning);
}
.vb-lan__btnico {
  margin-right: 6px;
}
.vb-lan__dev {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-lan__list {
  display: flex;
  flex-direction: column;
  margin: 0 -20px;
  font-size: 13px;
}
.vb-lan__row {
  display: grid;
  /* Fixed widths where content varies per row: each row is its own grid. */
  grid-template-columns: minmax(200px, 1.6fr) 150px 170px 150px 200px;
  align-items: center;
  gap: 8px 14px;
  padding: 8px 20px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.vb-lan__row--th {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--vb-muted);
}
.vb-lan__row.is-off {
  color: var(--vb-muted);
}
.vb-lan__cell--dev {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.vb-lan__macunder,
.vb-lan__label {
  display: none;
}
.vb-lan__acts {
  text-align: right;
}
/* A tablet: the MAC address folds under the name (artboard Lan-768). */
@media (width <= 1100px) {
  .vb-lan__row {
    grid-template-columns: minmax(0, 1.6fr) 140px 140px 190px;
  }
  .vb-lan__row > :nth-child(3) {
    display: none;
  }
  .vb-lan__macunder {
    display: block;
  }
}
/* A phone: every device a card, the button kept (artboard Lan-360). */
@media (width <= 700px) {
  .vb-lan__list {
    gap: 10px;
    margin: 0;
  }
  .vb-lan__list .vb-lan__row.vb-lan__row--th {
    display: none;
  }
  .vb-lan__row {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 4px;
    padding: 12px 14px;
    border: 1px solid var(--el-border-color-light);
    border-radius: 10px;
  }
  .vb-lan__label {
    display: inline;
  }
}
.vb-lan__rowerr {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-color-error);
  text-align: right;
  white-space: normal;
}
.vb-lan__grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0 14px;
}
.vb-lan__grid--two {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
/* The dangerous card is framed in the warning colour, as in the artboard: the
   difference between the two forms has to be visible before reading. */
.vb-lan__danger.el-card {
  border-color: var(--el-color-warning-light-5);
}
.vb-lan__actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  flex-wrap: wrap;
}
@media (width <= 900px) {
  .vb-lan__grid,
  .vb-lan__grid--two {
    grid-template-columns: minmax(0, 1fr);
  }
}
@media (width <= 600px) {
  .vb-lan__actions .el-button {
    width: 100%;
    margin: 0 0 8px;
  }
}
</style>
