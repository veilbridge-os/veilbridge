<script setup lang="ts">
// The firewall screen (M3.3, #36), built to the accepted artboards `Fw-*` in
// the mockup project (design task 06).
//
// People come here with three questions, most frequent first: "open a port",
// "what is open to the internet", "block or allow this". The screen answers
// them in that order and keeps zones — the thing LuCI leads with — as a
// read-only reference at the bottom.
//
// Every change here is dangerous (D-69): nothing applies from this screen, it
// only drafts, and the apply bar runs the draft under the confirmation window.
// The device is the one validator; its refusals land at the field they name.
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ApiError,
  api,
  type FirewallRule,
  type FirewallRuleConfig,
  type FirewallStatus,
  type FirewallZone,
  type LANStatus,
  type PortForward,
} from '@/api/client'
import VbIcon from '@/components/VbIcon.vue'
import { refreshStaged, useLive } from '@/stores/live'

const { t } = useI18n()
const { applyState, staged, stale, lastUpdate, can } = useLive()

const fw = ref<FirewallStatus | null>(null)
const lan = ref<LANStatus | null>(null)
const loaded = ref(false)
const unsupported = ref(false)
const loadError = ref('')

const SLOW_POLL_MS = 15_000
let poll = 0

async function load() {
  try {
    fw.value = await api.firewall()
    unsupported.value = false
    loadError.value = ''
  } catch (e) {
    if (e instanceof ApiError && e.status === 501) unsupported.value = true
    else if (!fw.value) loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loaded.value = true
  }
  // Only for naming devices in port forwards; a device without a local
  // network, or one that cannot say, simply has no names to offer.
  if (can('dhcp-server')) {
    try {
      lan.value = await api.lan()
    } catch {
      /* names are a courtesy here, not a requirement */
    }
  }
}

onMounted(() => {
  void load()
  void refreshStaged()
  poll = window.setInterval(load, SLOW_POLL_MS)
})
onUnmounted(() => window.clearInterval(poll))

const zones = computed<FirewallZone[]>(() => fw.value?.zones ?? [])
const rules = computed<FirewallRule[]>(() => fw.value?.rules ?? [])
const forwards = computed<PortForward[]>(() => fw.value?.portForwards ?? [])
const forwardings = computed(() => fw.value?.forwardings ?? [])

const busy = computed(() => applyState.value?.phase === 'awaiting_confirm')
const draftCount = computed(() => staged.value.length)
const dataFrom = computed(() =>
  lastUpdate.value
    ? lastUpdate.value.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    : '',
)

// --- words for zones -----------------------------------------------------

function zoneOf(name?: string): FirewallZone | undefined {
  return zones.value.find((z) => z.name === name)
}

/** What a zone is to a person. The role comes from what the zone does (D-68),
 * never from its name: on one of our stands the uplink sits in "lan". */
function zoneWord(name?: string): string {
  if (!name) return t('fw.zoneRouter')
  if (name === '*') return t('fw.zoneAny')
  const role = zoneOf(name)?.role
  if (role === 'internet') return t('fw.zoneInternet')
  if (role === 'local') return t('fw.zoneLocal')
  return name
}

/** The zone's own name, shown small beside the word — but only where the
 * word is a role, otherwise it would say the same thing twice. */
function zoneCode(name?: string): string {
  if (!name || name === '*') return ''
  const role = zoneOf(name)?.role
  return role === 'internet' || role === 'local' ? name : ''
}

const hasLocalZone = computed(() => zones.value.some((z) => z.role === 'local'))
const fromInternet = (name?: string) =>
  !!name && (name === '*' || zoneOf(name)?.role === 'internet')

// --- protocols and ports -------------------------------------------------

function protoText(protocols: string[] | null | undefined): string {
  const p = protocols ?? []
  if (p.includes('all')) return t('fw.allProtocols')
  return p.map((x) => x.toUpperCase()).join('+')
}

function portsText(ports?: string): string {
  return (ports ?? '').split(/\s+/).filter(Boolean).join(', ')
}

// --- devices, for port forwards -----------------------------------------

interface DeviceOption {
  ip: string
  name: string
  pinned: boolean
}

const devices = computed<DeviceOption[]>(() => {
  const out = new Map<string, DeviceOption>()
  for (const l of lan.value?.leases ?? []) {
    out.set(l.ip, { ip: l.ip, name: l.hostname ?? '', pinned: false })
  }
  for (const r of lan.value?.reserved ?? []) {
    const d = out.get(r.ip)
    if (d) {
      d.pinned = true
      d.name ||= r.name ?? ''
    } else out.set(r.ip, { ip: r.ip, name: r.name ?? '', pinned: true })
  }
  return [...out.values()]
})

function deviceName(ip: string): string {
  return devices.value.find((d) => d.ip === ip)?.name ?? ''
}

// --- "what is open to the internet" -------------------------------------

const ownRules = computed(() => rules.value.filter((r) => !r.system))
const firmwareCount = computed(() => rules.value.length - ownRules.value.length)

interface Opening {
  kind: 'rule' | 'forward'
  title: string
  sub: string
}

// Derived from the same data as the lists below, and only from what lets a
// connection IN: a switched-on rule of the owner's that allows traffic from
// the internet side, and a switched-on port forward. The firmware's own
// rules (DHCP renew, ping) are not doors anyone opened.
const openings = computed<Opening[]>(() => {
  const out: Opening[] = []
  for (const r of ownRules.value) {
    if (!r.enabled || r.action !== 'accept' || !fromInternet(r.from)) continue
    const what = [protoText(r.protocols), portsText(r.ports)].filter(Boolean).join(' ')
    out.push({
      kind: 'rule',
      title: `${r.to ? zoneWord(r.to) : t('fw.routerItself')} — ${what}`,
      sub: t('fw.viaRule', { name: r.name || r.id }),
    })
  }
  for (const f of forwards.value) {
    if (!f.enabled) continue
    out.push({
      kind: 'forward',
      title: `${deviceName(f.toAddress) || f.name || f.toAddress} — ${protoText(f.protocols)} ${f.externalPort}`,
      sub: t('fw.viaForward', { name: f.name || f.externalPort, to: `${f.toAddress}:${f.toPort}` }),
    })
  }
  return out
})

/** The first own rule that opens the router ITSELF to the internet: the thing
 * most often left open by forgetting, and the one worth a warning. */
const routerOpenRule = computed(() =>
  ownRules.value.find((r) => r.enabled && r.action === 'accept' && fromInternet(r.from) && !r.to),
)

// --- refusals ------------------------------------------------------------

type Field =
  | 'name'
  | 'protocols'
  | 'externalPort'
  | 'toPort'
  | 'toAddress'
  | 'from'
  | 'to'
  | 'ports'
  | 'action'
  | 'family'
  | 'before'
  | 'id'

// Spelled out, not built as `fw.bad.${field}`: a template key is not
// type-checked, and one built that way once rendered as the raw key.
const BAD: Record<Field, () => string> = {
  name: () => t('fw.bad.name'),
  protocols: () => t('fw.bad.protocols'),
  externalPort: () => t('fw.bad.externalPort'),
  toPort: () => t('fw.bad.toPort'),
  toAddress: () => t('fw.bad.toAddress'),
  from: () => t('fw.bad.from'),
  to: () => t('fw.bad.to'),
  ports: () => t('fw.bad.ports'),
  action: () => t('fw.bad.action'),
  family: () => t('fw.bad.family'),
  before: () => t('fw.bad.before'),
  id: () => t('fw.bad.id'),
}

/** place puts a refusal at the field the device named (#28), as this panel's
 * sentence followed by the device's own words. False when it named none. */
function place(e: unknown, into: Record<string, string>): boolean {
  if (!(e instanceof ApiError)) return false
  const field = (Object.keys(BAD) as Field[]).find((f) => e.fields[f])
  if (!field) return false
  into[field] = `${BAD[field]()} — ${t('fw.deviceSaid', { detail: e.fields[field] })}`
  return true
}

function failure(e: unknown): string {
  if (e instanceof ApiError && e.status === 409) return t('fw.busyHint')
  return e instanceof Error ? e.message : String(e)
}

const listError = ref('')
const working = ref('')

async function run(key: string, fn: () => Promise<unknown>) {
  working.value = key
  listError.value = ''
  try {
    await fn()
  } catch (e) {
    listError.value = failure(e)
  } finally {
    working.value = ''
    await refreshStaged()
    await load()
  }
}

// --- port forwards -------------------------------------------------------

function forwardConfig(f: PortForward, enabled = f.enabled) {
  return {
    id: f.id,
    name: f.name ?? '',
    enabled,
    protocols: f.protocols ?? ['tcp'],
    externalPort: f.externalPort,
    toAddress: f.toAddress,
    // The device reports the external port when no separate one is set;
    // sending it back unchanged would stage a "change" to the same number.
    toPort: f.toPort === f.externalPort ? '' : f.toPort,
  }
}

const toggleForward = (f: PortForward) =>
  run(`f:${f.id}`, () => api.stagePortForward(forwardConfig(f, !f.enabled)))
const removeForward = (f: PortForward) => run(`f:${f.id}`, () => api.removePortForward(f.id))

const OTHER = '__other'
// el-select reads an empty string as "nothing chosen" and shows its
// placeholder — but here empty MEANS something: the router itself, the end of
// the list. Measured on the router: both fields read "Select". So the form
// holds these two values and the request gets the empty string back.
const ROUTER = '@router'
const END = '@end'
const fwdOpen = ref(false)
const fwdSaving = ref(false)
const fwdErrors = ref<Record<string, string>>({})
const fwdError = ref('')
const fwdForm = ref({
  id: '',
  name: '',
  enabled: true,
  protocols: 'tcp' as 'tcp' | 'udp' | 'both',
  externalPort: '',
  toPort: '',
  device: '',
  address: '',
})

function openForward(f?: PortForward) {
  fwdErrors.value = {}
  fwdError.value = ''
  const p = f?.protocols ?? ['tcp']
  const known = f ? devices.value.some((d) => d.ip === f.toAddress) : true
  fwdForm.value = {
    id: f?.id ?? '',
    name: f?.name ?? '',
    enabled: f?.enabled ?? true,
    protocols: p.includes('tcp') && p.includes('udp') ? 'both' : p.includes('udp') ? 'udp' : 'tcp',
    externalPort: f?.externalPort ?? '',
    toPort: f && f.toPort !== f.externalPort ? f.toPort : '',
    device: f ? (known ? f.toAddress : OTHER) : (devices.value[0]?.ip ?? OTHER),
    address: f && !known ? f.toAddress : '',
  }
  fwdOpen.value = true
}

async function saveForward() {
  fwdSaving.value = true
  fwdErrors.value = {}
  fwdError.value = ''
  const v = fwdForm.value
  try {
    await api.stagePortForward({
      ...(v.id ? { id: v.id } : {}),
      name: v.name.trim(),
      enabled: v.enabled,
      protocols: v.protocols === 'both' ? ['tcp', 'udp'] : [v.protocols],
      externalPort: v.externalPort.trim(),
      toAddress: (v.device === OTHER ? v.address : v.device).trim(),
      toPort: v.toPort.trim(),
    })
    fwdOpen.value = false
  } catch (e) {
    if (!place(e, fwdErrors.value)) fwdError.value = failure(e)
  } finally {
    fwdSaving.value = false
    await refreshStaged()
    await load()
  }
}

// --- rules ---------------------------------------------------------------

const showFirmware = ref(false)

interface RuleRow {
  rule: FirewallRule
  /** 1-based, as the device runs them — the firmware's included. */
  n: number
}

const ruleRows = computed<RuleRow[]>(() =>
  rules.value
    .map((rule, i) => ({ rule, n: i + 1 }))
    .filter((r) => showFirmware.value || !r.rule.system),
)

/** The rule as it is sent back to the device, e.g. to switch it off. */
function ruleConfig(r: FirewallRule, enabled = r.enabled): FirewallRuleConfig {
  return {
    id: r.id,
    name: r.name ?? '',
    enabled,
    from: r.from ?? '',
    to: r.to ?? '',
    protocols: r.protocols ?? [],
    ports: r.ports ?? '',
    action: r.action,
    family: r.family === 'any' ? '' : (r.family ?? ''),
  }
}

const toggleRule = (r: FirewallRule) =>
  run(`r:${r.id}`, () => api.stageRule(ruleConfig(r, !r.enabled)))
const removeRule = (r: FirewallRule) => run(`r:${r.id}`, () => api.removeRule(r.id))

/** Moves are expressed the way the device takes them: "in front of" a rule,
 * or to the end (#46). Up is in front of the rule above; down is in front of
 * the one after next — or the end, when there is none. */
function moveTarget(r: FirewallRule, dir: -1 | 1): string | null {
  const all = rules.value
  const i = all.findIndex((x) => x.id === r.id)
  if (dir < 0) return i > 0 ? (all[i - 1]?.id ?? null) : null
  if (i < 0 || i >= all.length - 1) return null
  return all[i + 2]?.id ?? ''
}

const move = (r: FirewallRule, before: string) => run(`r:${r.id}`, () => api.moveRule(r.id, before))

function nudge(r: FirewallRule, dir: -1 | 1) {
  const before = moveTarget(r, dir)
  if (before !== null) void move(r, before)
}

// Dragging, for a mouse. The arrows do the same for a keyboard and a phone,
// where HTML drag and drop does not happen at all.
const dragId = ref('')
const dropAt = ref('')

function onDragStart(r: FirewallRule, ev: DragEvent) {
  dragId.value = r.id
  ev.dataTransfer?.setData('text/plain', r.id)
  if (ev.dataTransfer) ev.dataTransfer.effectAllowed = 'move'
}

/** Dropping on the upper half of a row puts the rule in front of it; on the
 * lower half, in front of the one after it. */
function dropTarget(row: FirewallRule, ev: DragEvent): string {
  const el = ev.currentTarget as HTMLElement
  const box = el.getBoundingClientRect()
  if (ev.clientY < box.top + box.height / 2) return row.id
  const i = rules.value.findIndex((x) => x.id === row.id)
  return rules.value[i + 1]?.id ?? ''
}

function onDragOver(row: FirewallRule, ev: DragEvent) {
  if (!dragId.value) return
  ev.preventDefault()
  dropAt.value = row.id
}

function onDrop(row: FirewallRule, ev: DragEvent) {
  ev.preventDefault()
  const id = dragId.value
  dragId.value = ''
  dropAt.value = ''
  if (!id) return
  const before = dropTarget(row, ev)
  if (before === id) return
  const r = rules.value.find((x) => x.id === id)
  if (r) void move(r, before)
}

function onDragEnd() {
  dragId.value = ''
  dropAt.value = ''
}

// --- the rule dialog -----------------------------------------------------

const ruleOpen = ref(false)
const ruleSaving = ref(false)
const ruleErrors = ref<Record<string, string>>({})
const ruleError = ref('')
const advanced = ref(false)
const ruleForm = ref({
  id: '',
  name: '',
  enabled: true,
  action: 'reject',
  from: '',
  to: '',
  protocols: ['tcp'] as string[],
  ports: '',
  family: '',
  before: END,
})

function openRule(r?: FirewallRule) {
  ruleErrors.value = {}
  ruleError.value = ''
  const internet =
    zones.value.find((z) => z.role === 'internet')?.name ?? zones.value[0]?.name ?? ''
  if (r) {
    const c = ruleConfig(r)
    ruleForm.value = {
      id: r.id,
      name: c.name ?? '',
      enabled: c.enabled,
      action: c.action,
      from: c.from,
      to: c.to || ROUTER,
      protocols: [...(c.protocols ?? [])],
      ports: c.ports ?? '',
      family: c.family ?? '',
      before: END,
    }
  } else {
    ruleForm.value = {
      id: '',
      name: '',
      enabled: true,
      action: 'reject',
      from: internet,
      to: ROUTER,
      protocols: ['tcp'],
      ports: '',
      family: '',
      before: END,
    }
  }
  advanced.value = !!ruleForm.value.family
  ruleOpen.value = true
}

// "All protocols" excludes the others, and choosing another undoes "all".
watch(
  () => [...ruleForm.value.protocols],
  (now, was) => {
    const added = now.filter((p) => !was?.includes(p))
    if (added.includes('all') && now.length > 1) ruleForm.value.protocols = ['all']
    else if (added.length && now.includes('all')) {
      ruleForm.value.protocols = now.filter((p) => p !== 'all')
    }
  },
)

const portsApply = computed(() => {
  const p = ruleForm.value.protocols
  return p.length > 0 && p.every((x) => x === 'tcp' || x === 'udp')
})

const zoneChoices = computed(() => zones.value.map((z) => z.name))

const placeChoices = computed(() =>
  rules.value.map((r, i) => ({
    id: r.id,
    label: t('fw.placeBefore', { n: i + 1, name: r.name || r.id }),
  })),
)

/** The rule named in a D-71 refusal, so the fix is one click: the device says
 * which rule is in the way, and that rule is the place to go in front of. */
const blocker = computed(() => {
  const said = ruleErrors.value.before ?? ''
  const name = said.match(/the rule "([^"]+)"/)?.[1] ?? said.match(/rule ([@\w[\]]+) above/)?.[1]
  if (!name) return null
  return rules.value.find((r) => r.name === name || r.id === name) ?? null
})

async function saveRule() {
  ruleSaving.value = true
  ruleErrors.value = {}
  ruleError.value = ''
  const v = ruleForm.value
  try {
    await api.stageRule({
      ...(v.id ? { id: v.id } : {}),
      name: v.name.trim(),
      enabled: v.enabled,
      action: v.action,
      from: v.from,
      to: v.to === ROUTER ? '' : v.to,
      protocols: v.protocols,
      ports: portsApply.value ? v.ports.trim() : '',
      family: v.family,
      ...(!v.id && v.before !== END ? { before: v.before } : {}),
    })
    ruleOpen.value = false
  } catch (e) {
    if (!place(e, ruleErrors.value)) ruleError.value = failure(e)
  } finally {
    ruleSaving.value = false
    await refreshStaged()
    await load()
  }
}

function putBeforeBlocker() {
  if (!blocker.value) return
  ruleForm.value.before = blocker.value.id
  void saveRule()
}

const condText = (r: FirewallRule) =>
  (r.unsupported ?? []).map((c) => (COND[c] ?? COND.other)()).join(', ')

const COND: Record<string, () => string> = {
  sourceAddress: () => t('fw.cond.sourceAddress'),
  sourcePort: () => t('fw.cond.sourcePort'),
  destinationAddress: () => t('fw.cond.destinationAddress'),
  icmpTypes: () => t('fw.cond.icmpTypes'),
  schedule: () => t('fw.cond.schedule'),
  rateLimit: () => t('fw.cond.rateLimit'),
  logging: () => t('fw.cond.logging'),
  other: () => t('fw.cond.other'),
}

const ACTION: Record<string, () => string> = {
  accept: () => t('fw.action.accept'),
  reject: () => t('fw.action.reject'),
  drop: () => t('fw.action.drop'),
  other: () => t('fw.action.other'),
}
const actionText = (a: string) => (ACTION[a] ?? ACTION.other)()
const actionType = (a: string) => (a === 'accept' ? 'success' : a === 'other' ? 'info' : 'danger')

const ACTION_HINT: Record<string, () => string> = {
  accept: () => t('fw.actionHint.accept'),
  reject: () => t('fw.actionHint.reject'),
  drop: () => t('fw.actionHint.drop'),
}

const discard = () => run('discard', () => api.discardStaged())
</script>

<template>
  <section class="vb-fw">
    <header class="vb-fw__head">
      <h1>{{ t('fw.title') }}</h1>
      <el-tag v-if="draftCount" type="warning" size="small" effect="light">
        {{ t('lan.draftPending', { n: draftCount }, draftCount) }}
      </el-tag>
      <el-tag v-if="stale && dataFrom" size="small" type="info">
        {{ t('shell.dataFrom', { time: dataFrom }) }}
      </el-tag>
    </header>

    <el-card v-if="!loaded" shadow="never">
      <el-skeleton :rows="4" animated />
    </el-card>

    <el-card v-else-if="unsupported" shadow="never">
      <el-empty :description="t('fw.unsupported')">
        <p class="vb-fw__hint">{{ t('fw.unsupportedHint') }}</p>
      </el-empty>
    </el-card>

    <el-card v-else-if="!fw" shadow="never">
      <el-empty :description="loadError || t('shell.offline')" />
    </el-card>

    <template v-else>
      <el-alert
        v-if="busy"
        type="warning"
        :closable="false"
        show-icon
        :title="t('fw.busyTitle')"
        :description="t('fw.busyHint')"
      />
      <el-alert v-if="listError" type="error" :closable="false" show-icon :title="t('fw.refused')" :description="listError" />

      <!-- 1. The anxious question first: what is open from outside. -->
      <el-card shadow="never">
        <div class="vb-fw__headline">
          <VbIcon :name="openings.length ? 'globe' : 'check'" size="lg" :class="openings.length ? 'vb-fw__accent' : 'vb-fw__ok'" />
          <strong>{{ openings.length ? t('fw.openN', { n: openings.length }, openings.length) : t('fw.openNone') }}</strong>
        </div>
        <ul v-if="openings.length" class="vb-fw__open">
          <li v-for="(o, i) in openings" :key="i">
            <VbIcon :name="o.kind === 'rule' ? 'lock' : 'fork'" size="sm" :class="o.kind === 'rule' ? 'vb-fw__warn' : 'vb-fw__muted'" />
            <span class="vb-fw__opentext">
              <strong>{{ o.title }}</strong>
              <span class="vb-fw__muted">{{ o.sub }}</span>
            </span>
          </li>
        </ul>
        <p v-else class="vb-fw__muted vb-fw__p">{{ t('fw.openNoneHint') }}</p>
        <el-alert
          v-if="routerOpenRule"
          type="warning"
          :closable="false"
          show-icon
          class="vb-fw__alert"
          :title="t('fw.routerOpenWarn', { name: routerOpenRule.name || routerOpenRule.id })"
        />
        <p class="vb-fw__hint">{{ t('fw.bySettings') }}</p>
      </el-card>

      <!-- 2. Port forwarding. Not drawn at all on a box with no local network:
           there is nothing to forward into (profile of D-17). -->
      <el-card v-if="hasLocalZone" shadow="never">
        <template #header>
          <div class="vb-fw__cardhead">
            <VbIcon name="fork" class="vb-fw__muted" />
            <strong>{{ t('fw.forwardsTitle') }}</strong>
            <span class="vb-fw__spacer" />
            <el-button size="small" :disabled="busy" @click="openForward()">
              <VbIcon name="plus" size="sm" class="vb-fw__btnico" />{{ t('fw.openPort') }}
            </el-button>
          </div>
        </template>
        <el-empty v-if="!forwards.length" :image-size="60" :description="t('fw.fwdNone')" />
        <div v-else class="vb-fw__list vb-fw__list--fwd" role="table">
          <div class="vb-fw__row vb-fw__row--th" role="row">
            <span />
            <span>{{ t('fw.colName') }}</span>
            <span>{{ t('fw.colRouterPort') }}</span>
            <span>{{ t('fw.colTarget') }}</span>
            <span />
          </div>
          <div v-for="f in forwards" :key="f.id" class="vb-fw__row" :class="{ 'is-off': !f.enabled }" role="row">
            <span class="vb-fw__cell--switch">
              <el-switch
                :model-value="f.enabled"
                :disabled="busy"
                :loading="working === `f:${f.id}`"
                :aria-label="t('fw.fwdOn')"
                @change="toggleForward(f)"
              />
            </span>
            <span class="vb-fw__cell--name">
              <strong>{{ f.name || f.externalPort }}</strong>
              <el-tag v-if="!f.enabled" size="small" type="info">{{ t('fw.off') }}</el-tag>
            </span>
            <span>{{ protoText(f.protocols) }} <span class="vb-mono">{{ f.externalPort }}</span></span>
            <span>
              <strong v-if="deviceName(f.toAddress)">{{ deviceName(f.toAddress) }}</strong>
              <span v-else class="vb-fw__muted">{{ t('fw.noName') }}</span>
              <span class="vb-mono vb-fw__muted vb-fw__ip">{{ f.toAddress }}:{{ f.toPort }}</span>
            </span>
            <span class="vb-fw__acts">
              <el-button text size="small" :disabled="busy" :aria-label="t('fw.edit')" @click="openForward(f)">
                <VbIcon name="edit" size="sm" /><span class="vb-fw__actlabel">{{ t('fw.edit') }}</span>
              </el-button>
              <el-button text size="small" :disabled="busy" :aria-label="t('fw.remove')" @click="removeForward(f)">
                <VbIcon name="trash" size="sm" /><span class="vb-fw__actlabel">{{ t('fw.remove') }}</span>
              </el-button>
            </span>
          </div>
        </div>
        <p class="vb-fw__hint">{{ t('fw.fwdHint') }}</p>
      </el-card>
      <el-alert v-else type="info" :closable="false" show-icon :title="t('fw.noLocal')" />

      <!-- 3. Rules, numbered as the device runs them: order is meaning (D-70). -->
      <el-card shadow="never">
        <template #header>
          <div class="vb-fw__cardhead">
            <VbIcon name="filter" class="vb-fw__muted" />
            <strong>{{ t('fw.rulesTitle') }}</strong>
            <el-tag size="small" type="info" class="vb-fw__nowide">{{ t('fw.firstMatch') }}</el-tag>
            <span class="vb-fw__spacer" />
            <el-button size="small" :disabled="busy" @click="openRule()">
              <VbIcon name="plus" size="sm" class="vb-fw__btnico" />{{ t('fw.addRule') }}
            </el-button>
          </div>
        </template>

        <div class="vb-fw__list vb-fw__list--rules" role="table">
          <div class="vb-fw__row vb-fw__row--sys" role="row">
            <span class="vb-fw__cell--grip"><VbIcon name="lock" size="sm" class="vb-fw__muted" /></span>
            <span class="vb-mono vb-fw__muted vb-fw__nowrap">1–{{ firmwareCount }}</span>
            <span class="vb-fw__cell--span">
              <strong>{{ t('fw.firmware', { n: firmwareCount }) }}</strong>
              <span class="vb-fw__muted">{{ t('fw.firmwareHint') }}</span>
            </span>
            <span class="vb-fw__acts">
              <el-button size="small" text @click="showFirmware = !showFirmware">
                {{ showFirmware ? t('fw.hide') : t('fw.show') }}
              </el-button>
            </span>
          </div>

          <div
            v-for="row in ruleRows"
            :key="row.rule.id"
            class="vb-fw__row"
            :class="{
              'is-off': !row.rule.enabled,
              'is-sys': row.rule.system,
              'is-drop': dropAt === row.rule.id,
              'is-drag': dragId === row.rule.id,
            }"
            role="row"
            :draggable="!row.rule.system && !busy"
            @dragstart="onDragStart(row.rule, $event)"
            @dragover="onDragOver(row.rule, $event)"
            @drop="onDrop(row.rule, $event)"
            @dragend="onDragEnd"
          >
            <span class="vb-fw__cell--grip">
              <VbIcon :name="row.rule.system ? 'lock' : 'grip'" size="sm" class="vb-fw__muted" />
            </span>
            <span class="vb-mono vb-fw__muted vb-fw__cell--n">{{ row.n }}</span>
            <span class="vb-fw__cell--switch">
              <el-switch
                v-if="!row.rule.system"
                :model-value="row.rule.enabled"
                :disabled="busy"
                :loading="working === `r:${row.rule.id}`"
                :aria-label="t('fw.ruleOn')"
                @change="toggleRule(row.rule)"
              />
            </span>
            <span class="vb-fw__cell--action"><el-tag size="small" :type="actionType(row.rule.action)">{{ actionText(row.rule.action) }}</el-tag></span>
            <span class="vb-fw__cell--way">
              <span class="vb-fw__nowrap">{{ zoneWord(row.rule.from) }} <span v-if="zoneCode(row.rule.from)" class="vb-fw__zn">{{ zoneCode(row.rule.from) }}</span></span>
              <span class="vb-fw__muted"> → </span>
              <span class="vb-fw__nowrap">{{ zoneWord(row.rule.to) }} <span v-if="zoneCode(row.rule.to)" class="vb-fw__zn">{{ zoneCode(row.rule.to) }}</span></span>
            </span>
            <span class="vb-fw__cell--proto">
              {{ protoText(row.rule.protocols) }}
              <span v-if="row.rule.ports" class="vb-mono">{{ portsText(row.rule.ports) }}</span>
              <span v-if="row.rule.family" class="vb-fw__muted"> · {{ row.rule.family === 'ipv6' ? 'IPv6' : 'IPv4' }}</span>
            </span>
            <span class="vb-fw__cell--name">
              <strong :class="{ 'vb-fw__muted': row.rule.system }">{{ row.rule.name || row.rule.id }}</strong>
              <span v-if="!row.rule.system && row.rule.unsupported?.length" class="vb-fw__cond">
                <VbIcon name="alert" size="sm" />
                <span>{{ t('fw.hidden', { list: condText(row.rule) }) }}</span>
              </span>
            </span>
            <span class="vb-fw__acts">
              <template v-if="!row.rule.system">
                <el-button text size="small" :disabled="busy || moveTarget(row.rule, -1) === null" :aria-label="t('fw.moveUp')" @click="nudge(row.rule, -1)">
                  <VbIcon name="up" size="sm" />
                </el-button>
                <el-button text size="small" :disabled="busy || moveTarget(row.rule, 1) === null" :aria-label="t('fw.moveDown')" @click="nudge(row.rule, 1)">
                  <VbIcon name="down" size="sm" />
                </el-button>
                <el-button v-if="!row.rule.unsupported?.length" text size="small" :disabled="busy" :aria-label="t('fw.edit')" @click="openRule(row.rule)">
                  <VbIcon name="edit" size="sm" /><span class="vb-fw__actlabel">{{ t('fw.edit') }}</span>
                </el-button>
                <el-button text size="small" :disabled="busy" :aria-label="t('fw.remove')" @click="removeRule(row.rule)">
                  <VbIcon name="trash" size="sm" /><span class="vb-fw__actlabel">{{ t('fw.remove') }}</span>
                </el-button>
              </template>
            </span>
          </div>
        </div>
        <p v-if="!ownRules.length" class="vb-fw__muted vb-fw__p">{{ t('fw.noRules', { n: firmwareCount }) }}</p>
        <p class="vb-fw__hint">{{ t('fw.dragHint') }}</p>
      </el-card>

      <!-- 4. Zones: a reference, not a task. There is no API to change them. -->
      <el-card shadow="never">
        <template #header>
          <div class="vb-fw__cardhead">
            <strong>{{ t('fw.zonesTitle') }}</strong>
            <el-tag size="small" type="info" class="vb-fw__tagright">{{ t('fw.viewOnly') }}</el-tag>
          </div>
        </template>
        <div class="vb-fw__list vb-fw__list--zones" role="table">
          <div class="vb-fw__row vb-fw__row--th" role="row">
            <span>{{ t('fw.colZone') }}</span>
            <span>{{ t('fw.colLinks') }}</span>
            <span>{{ t('fw.colToRouter') }}</span>
            <span>{{ t('fw.colInside') }}</span>
            <span>{{ t('fw.colNat') }}</span>
            <span />
          </div>
          <div v-for="z in zones" :key="z.name" class="vb-fw__row" role="row">
            <span><strong>{{ zoneWord(z.name) }}</strong> <span v-if="zoneCode(z.name)" class="vb-fw__zn">{{ z.name }}</span></span>
            <span><span class="vb-fw__label">{{ t('fw.colLinks') }}: </span><span class="vb-mono vb-fw__muted">{{ (z.networks ?? []).join(', ') || '—' }}</span></span>
            <span><span class="vb-fw__label">{{ t('fw.colToRouter') }}: </span>{{ z.input === 'accept' ? t('fw.allowed') : t('fw.denied') }}</span>
            <span><span class="vb-fw__label">{{ t('fw.colInside') }}: </span>{{ z.forward === 'accept' ? t('fw.allowed') : t('fw.denied') }}</span>
            <span :class="{ 'vb-fw__cell--empty': !z.masquerade }">{{ z.masquerade ? t('fw.natYes') : '—' }}</span>
            <span><el-tag size="small" :type="z.live ? 'success' : 'info'">{{ z.live ? t('fw.live') : t('fw.notLive') }}</el-tag></span>
          </div>
        </div>
        <p v-for="(f, i) in forwardings" :key="i" class="vb-fw__muted vb-fw__p">
          {{ t('fw.forwarding', { from: zoneWord(f.from), to: zoneWord(f.to) }) }}
        </p>
        <p class="vb-fw__hint">{{ t('fw.defaultsHint') }}</p>
      </el-card>

      <div v-if="draftCount" class="vb-fw__actions">
        <el-button :disabled="busy" @click="discard">{{ t('lan.cancel') }}</el-button>
      </div>
    </template>

    <!-- A port forward. The device comes from the list of devices on the
         network: retyping an address from another screen is a way to point
         a forward into nowhere, which looks exactly like "the service is down". -->
    <el-dialog v-model="fwdOpen" :title="t('fw.fwdDialog')" width="min(560px, calc(100vw - 24px))">
      <el-form label-position="top" class="vb-fw__form">
        <el-form-item :label="t('fw.name')" :error="fwdErrors.name">
          <el-input v-model="fwdForm.name" placeholder="NAS" />
        </el-form-item>
        <div class="vb-fw__grid">
          <el-form-item :label="t('fw.incoming')">
            <el-input :model-value="t('fw.zoneInternet')" disabled />
            <div class="vb-fw__hint">{{ t('fw.incomingHint') }}</div>
          </el-form-item>
          <el-form-item :label="t('fw.protocols')" :error="fwdErrors.protocols">
            <el-radio-group v-model="fwdForm.protocols">
              <el-radio-button value="tcp">TCP</el-radio-button>
              <el-radio-button value="udp">UDP</el-radio-button>
              <el-radio-button value="both">{{ t('fw.tcpUdp') }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item :label="t('fw.portRouter')" :error="fwdErrors.externalPort">
            <el-input v-model="fwdForm.externalPort" class="vb-mono" placeholder="8443" />
            <div class="vb-fw__hint">{{ t('fw.portRouterHint') }}</div>
          </el-form-item>
          <el-form-item :label="t('fw.portDevice')" :error="fwdErrors.toPort">
            <el-input v-model="fwdForm.toPort" class="vb-mono" placeholder="443" />
            <div class="vb-fw__hint">{{ t('fw.portDeviceHint') }}</div>
          </el-form-item>
        </div>
        <el-form-item :label="t('fw.device')" :error="fwdForm.device === OTHER ? '' : fwdErrors.toAddress">
          <el-select v-model="fwdForm.device" class="vb-fw__wide">
            <el-option v-for="d in devices" :key="d.ip" :value="d.ip" :label="`${d.name || t('fw.noName')} · ${d.ip}`">
              <span class="vb-fw__opt">
                <span>{{ d.name || t('fw.noName') }}</span>
                <el-tag v-if="d.pinned" size="small">{{ t('fw.pinned') }}</el-tag>
                <span class="vb-mono vb-fw__muted vb-fw__optip">{{ d.ip }}</span>
              </span>
            </el-option>
            <el-option :value="OTHER" :label="t('fw.otherAddress')" />
          </el-select>
          <div class="vb-fw__hint">{{ t('fw.deviceHint') }}</div>
        </el-form-item>
        <el-form-item v-if="fwdForm.device === OTHER" :error="fwdErrors.toAddress">
          <el-input v-model="fwdForm.address" class="vb-mono" placeholder="192.168.1.50" />
        </el-form-item>
        <el-form-item>
          <el-switch v-model="fwdForm.enabled" :active-text="t('fw.fwdOn')" />
        </el-form-item>
      </el-form>
      <el-alert v-if="fwdError" type="error" :closable="false" show-icon :title="t('fw.refused')" :description="fwdError" />
      <template #footer>
        <el-button @click="fwdOpen = false">{{ t('fw.cancel') }}</el-button>
        <el-button type="primary" :loading="fwdSaving" :disabled="busy" @click="saveForward">{{ t('fw.save') }}</el-button>
      </template>
    </el-dialog>

    <!-- A rule. Its place in the list is a visible field (D-70): a rule that
         would never act where it lands is refused there, with the fix one
         click away (D-71). -->
    <el-dialog
      v-model="ruleOpen"
      :title="ruleForm.id ? t('fw.ruleEdit', { name: ruleForm.name || ruleForm.id }) : t('fw.ruleNew')"
      width="min(560px, calc(100vw - 24px))"
    >
      <el-form label-position="top" class="vb-fw__form">
        <el-form-item :label="t('fw.actionLabel')" :error="ruleErrors.action">
          <el-radio-group v-model="ruleForm.action">
            <el-radio-button value="accept">{{ t('fw.action.accept') }}</el-radio-button>
            <el-radio-button value="reject">{{ t('fw.action.reject') }}</el-radio-button>
            <el-radio-button value="drop">{{ t('fw.action.drop') }}</el-radio-button>
          </el-radio-group>
          <div class="vb-fw__hint">{{ ACTION_HINT[ruleForm.action]?.() }}</div>
        </el-form-item>
        <div class="vb-fw__grid">
          <el-form-item :label="t('fw.from')" :error="ruleErrors.from">
            <el-select v-model="ruleForm.from" class="vb-fw__wide">
              <el-option v-for="z in zoneChoices" :key="z" :value="z" :label="zoneCode(z) ? `${zoneWord(z)} · ${z}` : zoneWord(z)" />
              <el-option value="*" :label="t('fw.zoneAny')" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('fw.to')" :error="ruleErrors.to">
            <el-select v-model="ruleForm.to" class="vb-fw__wide">
              <el-option :value="ROUTER" :label="t('fw.zoneRouter')" />
              <el-option v-for="z in zoneChoices" :key="z" :value="z" :label="zoneCode(z) ? `${zoneWord(z)} · ${z}` : zoneWord(z)" />
              <el-option value="*" :label="t('fw.zoneAny')" />
            </el-select>
          </el-form-item>
        </div>
        <el-form-item :label="t('fw.protocols')" :error="ruleErrors.protocols">
          <el-checkbox-group v-model="ruleForm.protocols">
            <el-checkbox value="tcp">TCP</el-checkbox>
            <el-checkbox value="udp">UDP</el-checkbox>
            <el-checkbox value="icmp">ICMP</el-checkbox>
            <el-checkbox value="all">{{ t('fw.allProtocols') }}</el-checkbox>
          </el-checkbox-group>
        </el-form-item>
        <el-form-item :label="t('fw.ports')" :error="ruleErrors.ports">
          <el-input v-model="ruleForm.ports" class="vb-mono" :disabled="!portsApply" :placeholder="t('fw.portsPlaceholder')" />
          <div class="vb-fw__hint">{{ portsApply ? t('fw.portsAny') : t('fw.portsNone') }}</div>
        </el-form-item>
        <el-button v-if="!advanced" text size="small" class="vb-fw__more" @click="advanced = true">
          {{ t('fw.advanced') }}<VbIcon name="down" size="sm" />
        </el-button>
        <el-form-item v-else :label="t('fw.family')" :error="ruleErrors.family">
          <el-radio-group v-model="ruleForm.family">
            <el-radio-button value="">{{ t('fw.familyBoth') }}</el-radio-button>
            <el-radio-button value="ipv4">IPv4</el-radio-button>
            <el-radio-button value="ipv6">IPv6</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('fw.name')" :error="ruleErrors.name">
          <el-input v-model="ruleForm.name" />
        </el-form-item>
        <el-form-item v-if="!ruleForm.id" :label="t('fw.place')" :class="{ 'is-error': !!ruleErrors.before }">
          <el-select v-model="ruleForm.before" class="vb-fw__wide">
            <el-option :value="END" :label="t('fw.placeEnd')" />
            <el-option v-for="p in placeChoices" :key="p.id" :value="p.id" :label="p.label" />
          </el-select>
          <div v-if="!ruleErrors.before" class="vb-fw__hint">{{ t('fw.placeHint') }}</div>
          <div v-else class="vb-fw__err">{{ ruleErrors.before }}</div>
          <el-button v-if="blocker" type="primary" size="small" class="vb-fw__fix" :loading="ruleSaving" @click="putBeforeBlocker">
            {{ t('fw.putBefore', { name: blocker.name || blocker.id }) }}
          </el-button>
        </el-form-item>
        <el-form-item>
          <el-switch v-model="ruleForm.enabled" :active-text="t('fw.ruleOn')" />
        </el-form-item>
      </el-form>
      <el-alert v-if="ruleError" type="error" :closable="false" show-icon :title="t('fw.refused')" :description="ruleError" />
      <template #footer>
        <el-button @click="ruleOpen = false">{{ t('fw.cancel') }}</el-button>
        <el-button type="primary" :loading="ruleSaving" :disabled="busy" @click="saveRule">{{ t('fw.save') }}</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped>
.vb-fw {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.vb-fw__head {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-fw__head h1 {
  margin: 0;
  font-size: 22px;
}
.vb-fw__spacer {
  flex: 1 1 auto;
}
.vb-fw__cardhead {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-fw__tagright {
  margin-left: auto;
}
.vb-fw__headline {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
  font-size: 20px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.vb-fw__open {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0 0 12px;
  padding: 0;
  list-style: none;
}
.vb-fw__open li {
  display: flex;
  gap: 8px;
  align-items: flex-start;
}
.vb-fw__open li .vb-ico {
  margin-top: 3px;
  flex: 0 0 auto;
}
.vb-fw__opentext {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.vb-fw__opentext .vb-fw__muted {
  font-size: 13px;
}
.vb-fw__alert {
  margin-bottom: 10px;
}
.vb-fw__p {
  margin: 0 0 10px;
}
.vb-fw__hint {
  flex: 0 0 100%;
  width: 100%;
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
.vb-fw__muted {
  color: var(--vb-muted);
}
.vb-fw__ok {
  color: var(--el-color-success);
}
.vb-fw__warn {
  color: var(--el-color-warning);
}
.vb-fw__accent {
  color: var(--el-color-primary);
}
.vb-fw__btnico {
  margin-right: 6px;
}
.vb-fw__zn {
  font-family: var(--vb-font-mono, ui-monospace, monospace);
  font-size: 11px;
  color: var(--vb-muted);
}
.vb-fw__nowrap {
  white-space: nowrap;
}
.vb-fw__cond {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-color-warning);
}
.vb-fw__cond .vb-ico {
  flex: 0 0 auto;
  margin-top: 2px;
}

/* Lists are a grid of rows rather than el-table: the same markup restacks
   into cards on a phone (no sideways scrolling that hides the actions), and
   a rule row can be dragged. */
.vb-fw__list {
  display: flex;
  flex-direction: column;
  margin: 0 -20px;
  font-size: 13px;
}
.vb-fw__row {
  display: grid;
  align-items: center;
  gap: 8px 14px;
  padding: 8px 20px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.vb-fw__row:last-child {
  border-bottom: 0;
}
.vb-fw__row--th {
  padding-top: 0;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--vb-muted);
}
.vb-fw__row.is-off {
  color: var(--vb-muted);
}
.vb-fw__row.is-sys,
.vb-fw__row--sys {
  background: var(--el-fill-color-lighter);
}
.vb-fw__row.is-drop {
  box-shadow: inset 0 2px 0 var(--el-color-primary);
}
.vb-fw__row.is-drag {
  opacity: 0.5;
}
.vb-fw__row[draggable='true'] .vb-fw__cell--grip {
  cursor: grab;
}
.vb-fw__list--fwd .vb-fw__row {
  grid-template-columns: 48px minmax(120px, 1fr) minmax(120px, 1fr) minmax(200px, 2fr) auto;
}
.vb-fw__list--rules .vb-fw__row {
  grid-template-columns: 20px 36px 48px minmax(110px, auto) minmax(170px, 1.4fr) minmax(120px, 1fr) minmax(140px, 1.4fr) auto;
}
.vb-fw__list--rules .vb-fw__row--sys {
  grid-template-columns: 20px 36px 1fr auto;
}
.vb-fw__list--zones .vb-fw__row {
  grid-template-columns: minmax(150px, 1.4fr) minmax(90px, 1fr) minmax(90px, 1fr) minmax(90px, 1fr) minmax(150px, 1.4fr) auto;
}
.vb-fw__label {
  display: none;
}
.vb-fw__cell--span {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 6px;
}
.vb-fw__ip {
  margin-left: 6px;
}
.vb-fw__cell--name {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.vb-fw__list--fwd .vb-fw__cell--name {
  flex-direction: row;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.vb-fw__acts {
  display: flex;
  justify-content: flex-end;
  flex-wrap: nowrap;
}
.vb-fw__acts .el-button + .el-button {
  margin-left: 2px;
}
.vb-fw__actlabel {
  display: none;
}
.vb-fw__actions {
  display: flex;
  justify-content: flex-end;
}
.vb-fw__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 14px;
}
.vb-fw__wide {
  width: 100%;
}
.vb-fw__opt {
  display: flex;
  gap: 8px;
  align-items: center;
  width: 100%;
}
.vb-fw__optip {
  margin-left: auto;
}
.vb-fw__more {
  margin: -6px 0 12px;
}
.vb-fw__fix {
  margin-top: 8px;
}
.vb-fw__err {
  width: 100%;
  padding-top: 4px;
  font-size: 12px;
  line-height: 1.4;
  color: var(--el-color-danger);
}

/* Middle widths: the rules lose their separate name column. */
@media (width <= 1100px) {
  .vb-fw__list--rules .vb-fw__row {
    grid-template-columns: 20px 32px 48px minmax(0, 1fr) minmax(0, 1.2fr) auto;
  }
  .vb-fw__list--rules .vb-fw__row > :nth-child(6) {
    grid-column: 5;
  }
  .vb-fw__list--rules .vb-fw__row > .vb-fw__cell--name {
    grid-column: 4;
    grid-row: 2;
  }
  .vb-fw__list--rules .vb-fw__row > :nth-child(5) {
    grid-column: 5;
    grid-row: 1;
  }
  .vb-fw__list--rules .vb-fw__row > :nth-child(6) {
    grid-row: 2;
  }
  .vb-fw__list--rules .vb-fw__row > .vb-fw__acts {
    grid-column: 6;
    grid-row: 1 / span 2;
  }
  .vb-fw__list--rules .vb-fw__row > :nth-child(-n + 3) {
    grid-row: 1 / span 2;
  }
  .vb-fw__list--rules .vb-fw__row--sys > * {
    grid-row: auto !important;
    grid-column: auto !important;
  }
  .vb-fw__list--rules .vb-fw__row--sys {
    grid-template-columns: 20px 32px 1fr auto;
  }
}

/* A phone: every list becomes cards, and actions get their words back. */
@media (width <= 700px) {
  .vb-fw__nowide {
    display: none;
  }
  .vb-fw__list {
    gap: 10px;
    margin: 0;
  }
  /* Same specificity as the per-list rows below would lose to them. */
  .vb-fw__list .vb-fw__row.vb-fw__row--th {
    display: none;
  }
  .vb-fw__row,
  .vb-fw__list--fwd .vb-fw__row,
  .vb-fw__list--rules .vb-fw__row,
  .vb-fw__list--zones .vb-fw__row,
  .vb-fw__list--rules .vb-fw__row--sys {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 10px;
    padding: 12px 14px;
    border: 1px solid var(--el-border-color-light);
    border-radius: 10px;
  }
  .vb-fw__row > * {
    grid-row: auto !important;
    grid-column: auto !important;
  }
  .vb-fw__row > :not(.vb-fw__cell--grip, .vb-fw__cell--switch, .vb-mono) {
    flex: 0 0 100%;
  }
  /* A rule card reads as the artboard: number and action, the switch at the
     right, then the name, where it goes, and what. */
  .vb-fw__list--rules .vb-fw__cell--grip,
  .vb-fw__list--rules .vb-fw__cell--n,
  .vb-fw__list--rules .vb-fw__cell--action {
    order: -1;
  }
  .vb-fw__list--rules .vb-fw__row > .vb-fw__cell--action {
    flex: 0 0 auto;
  }
  .vb-fw__list--rules .vb-fw__cell--switch {
    order: 0;
    margin-left: auto;
  }
  .vb-fw__list--rules .vb-fw__cell--name {
    order: 1;
  }
  .vb-fw__list--rules .vb-fw__cell--way {
    order: 2;
  }
  .vb-fw__list--rules .vb-fw__cell--proto {
    order: 3;
  }
  .vb-fw__cell--empty {
    display: none;
  }
  .vb-fw__list--fwd .vb-fw__cell--switch {
    order: 2;
  }
  .vb-fw__list--fwd .vb-fw__cell--name {
    flex: 1 1 0 !important;
    order: 1;
  }
  .vb-fw__list--fwd .vb-fw__row > :nth-child(n + 3) {
    order: 3;
  }
  .vb-fw__acts {
    justify-content: flex-end;
    flex-wrap: wrap;
    order: 9;
  }
  .vb-fw__actlabel {
    display: inline;
    margin-left: 4px;
  }
  .vb-fw__label {
    display: inline;
  }
  .vb-fw__grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
