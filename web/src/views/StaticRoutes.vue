<script setup lang="ts">
// Static routes (#48), built to the accepted artboards `Rt-*` in the mockup
// project (design task 07), on top of the API from #37.
//
// People come here twice: to add a route to a network behind another router,
// and — more often — because a route they added "does not work". So the
// state of every route is the first column, and "working" means what the
// kernel holds right now (D-74), never what the network service says: it
// lists routes the kernel refused.
//
// Every change is dangerous (D-69): nothing applies from this screen, it only
// drafts, and the apply bar runs the draft under the confirmation window.
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ApiError,
  api,
  type RouteInterface,
  type RoutesStatus,
  type StaticRoute,
} from '@/api/client'
import VbIcon from '@/components/VbIcon.vue'
import { refreshStaged, useLive } from '@/stores/live'

const { t } = useI18n()
const { applyState, staged, stale, lastUpdate } = useLive()

const st = ref<RoutesStatus | null>(null)
const loaded = ref(false)
const unsupported = ref(false)
const loadError = ref('')

const SLOW_POLL_MS = 15_000
let poll = 0

async function load() {
  try {
    st.value = await api.staticRoutes()
    unsupported.value = false
    loadError.value = ''
  } catch (e) {
    if (e instanceof ApiError && e.status === 501) unsupported.value = true
    else if (!st.value) loadError.value = e instanceof Error ? e.message : String(e)
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

const routes = computed<StaticRoute[]>(() => st.value?.routes ?? [])
const v4 = computed(() => routes.value.filter((r) => r.family !== 'ipv6'))
const v6 = computed(() => routes.value.filter((r) => r.family === 'ipv6'))
const ifaces = computed<RouteInterface[]>(() => st.value?.interfaces ?? [])

const busy = computed(() => applyState.value?.phase === 'awaiting_confirm')
const draftCount = computed(() => staged.value.length)
const dataFrom = computed(() =>
  lastUpdate.value
    ? lastUpdate.value.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    : '',
)

// --- words ---------------------------------------------------------------

/** A connection by what it is to a person; the device's name stays small
 * beside it. The two roles the panel knows are the uplink and the LAN. */
function ifaceWord(name: string): string {
  if (name === 'lan') return t('fw.zoneLocal')
  if (name === 'wan' || name === 'wan6') return t('fw.zoneInternet')
  return name
}
const ifaceCode = (name: string) => (ifaceWord(name) === name ? '' : name)

type State = 'ok' | 'off' | 'bad' | 'unknown'

/** The four answers D-74 allows. A route in its own table is not in the main
 * table the panel reads, so "not working" would be a guess — it says so. */
function stateOf(r: StaticRoute): State {
  if (!r.enabled) return 'off'
  if ((r.unsupported ?? []).includes('table')) return 'unknown'
  return r.active ? 'ok' : 'bad'
}
const STATE_TAG: Record<State, 'success' | 'info' | 'danger'> = {
  ok: 'success',
  off: 'info',
  bad: 'danger',
  unknown: 'info',
}
const STATE_WORD: Record<State, () => string> = {
  ok: () => t('rt.stateOk'),
  off: () => t('rt.stateOff'),
  bad: () => t('rt.stateBad'),
  unknown: () => t('rt.stateUnknown'),
}

/** Whether an IPv4 address lies in a network given as "address/bits" — the
 * same check the device refuses a new route by (D-75), done here only to say
 * why an existing route is not working. */
function inNetwork(ip: string, cidr: string): boolean {
  const [net, bitsText] = cidr.split('/')
  const bits = Number(bitsText)
  const a = ipNum(ip)
  const b = ipNum(net ?? '')
  if (a === null || b === null || !(bits >= 0 && bits < 32)) return false
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0
  return (a & mask) >>> 0 === (b & mask) >>> 0
}
function ipNum(s: string): number | null {
  const p = s.split('.').map(Number)
  if (p.length !== 4 || p.some((x) => !Number.isInteger(x) || x < 0 || x > 255)) return null
  return p.reduce((n, x) => n * 256 + x, 0)
}

/** The reason a switched-on route is not in the kernel, when one can be told. */
function whyNot(r: StaticRoute): string {
  const i = ifaces.value.find((x) => x.name === r.interface)
  if (i && !i.up) return t('rt.whyDown', { iface: ifaceWord(r.interface) })
  const nets = (i?.ipv4 ?? []).filter((c) => !c.endsWith('/32'))
  const gw = r.gateway ?? ''
  if (gw && nets.length && !nets.some((c) => inNetwork(gw, c))) {
    return t('rt.whyGateway', { gw, iface: ifaceWord(r.interface), net: nets[0] })
  }
  return t('rt.whyUnknown')
}

const HIDDEN: Record<string, () => string> = {
  table: () => t('rt.hiddenKey.table'),
  source: () => t('rt.hiddenKey.source'),
  type: () => t('rt.hiddenKey.type'),
  onlink: () => t('rt.hiddenKey.onlink'),
  mtu: () => t('rt.hiddenKey.mtu'),
  other: () => t('rt.hiddenKey.other'),
}
const hiddenText = (r: StaticRoute) =>
  (r.unsupported ?? []).map((c) => (HIDDEN[c] ?? (() => t('rt.hiddenKey.other')))()).join(', ')

// --- refusals ------------------------------------------------------------

type Field = 'target' | 'gateway' | 'interface' | 'metric' | 'name' | 'id'
const BAD: Record<Field, () => string> = {
  target: () => t('rt.bad.target'),
  gateway: () => t('rt.bad.gateway'),
  interface: () => t('rt.bad.interface'),
  metric: () => t('rt.bad.metric'),
  name: () => t('rt.bad.name'),
  id: () => t('rt.bad.id'),
}

/** A D-76 refusal: the network and metric are already routed. It has its own
 * sentence and a one-click way out, so it is told apart from a typo. */
const clashed = ref(false)

/** place puts a refusal at the field the device named (#28), as this panel's
 * sentence followed by the device's own words. False when it named none. */
function place(e: unknown, into: Record<string, string>): boolean {
  if (!(e instanceof ApiError)) return false
  const field = (Object.keys(BAD) as Field[]).find((f) => e.fields[f])
  if (!field) return false
  const said = e.fields[field] ?? ''
  clashed.value = field === 'target' && /already/.test(said)
  const human = clashed.value ? t('rt.bad.clash') : BAD[field]()
  into[field] = `${human} — ${t('rt.deviceSaid', { detail: said })}`
  return true
}

function failure(e: unknown): string {
  if (e instanceof ApiError && e.status === 409) return t('rt.busyHint')
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

/** The route as it is sent back, e.g. to switch it off. A route with hidden
 * settings goes back exactly as read — the device refuses anything else. */
function routeConfig(r: StaticRoute, enabled = r.enabled) {
  return {
    id: r.id,
    name: r.name ?? '',
    enabled,
    target: r.target,
    gateway: r.gateway ?? '',
    interface: r.interface,
    metric: r.metric ?? 0,
  }
}

const toggle = (r: StaticRoute) =>
  run(`r:${r.id}`, () => api.stageStaticRoute(routeConfig(r, !r.enabled)))
const remove = (r: StaticRoute) => run(`r:${r.id}`, () => api.removeStaticRoute(r.id))

// --- the dialog ----------------------------------------------------------

// el-select reads '' as "nothing chosen" and shows its placeholder; here the
// empty interface MEANS "pick it by the gateway", so the form holds a token.
const AUTO = '@auto'
const MASKS = Array.from({ length: 25 }, (_, i) => 8 + i).map((bits) => ({
  bits,
  dotted: [24, 16, 8, 0]
    .map((sh) => ((bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0) >>> sh) & 255)
    .join('.'),
}))

const open = ref(false)
const saving = ref(false)
const errors = ref<Record<string, string>>({})
const formError = ref('')
const advanced = ref(false)
const form = ref({
  id: '',
  kind: 'net' as 'net' | 'host',
  address: '',
  bits: 24,
  gateway: '',
  iface: AUTO,
  metric: 0,
  name: '',
  enabled: true,
})

function openDialog(r?: StaticRoute) {
  errors.value = {}
  formError.value = ''
  clashed.value = false
  const [addr, bitsText] = (r?.target ?? '').split('/')
  const bits = Number(bitsText ?? 24)
  advanced.value = !!r?.metric
  form.value = {
    id: r?.id ?? '',
    kind: r && bits === 32 ? 'host' : 'net',
    address: addr ?? '',
    bits: r && bits !== 32 ? bits : 24,
    gateway: r?.gateway ?? '',
    iface: r?.interface ?? AUTO,
    metric: r?.metric ?? 0,
    name: r?.name ?? '',
    enabled: r?.enabled ?? true,
  }
  open.value = true
}

async function save() {
  saving.value = true
  errors.value = {}
  formError.value = ''
  clashed.value = false
  const v = form.value
  try {
    await api.stageStaticRoute({
      ...(v.id ? { id: v.id } : {}),
      name: v.name.trim(),
      enabled: v.enabled,
      target: `${v.address.trim()}/${v.kind === 'host' ? 32 : v.bits}`,
      gateway: v.gateway.trim(),
      interface: v.iface === AUTO ? '' : v.iface,
      metric: Number(v.metric) || 0,
    })
    open.value = false
  } catch (e) {
    if (!place(e, errors.value)) formError.value = failure(e)
  } finally {
    saving.value = false
    await refreshStaged()
    await load()
  }
}

/** The one-click way out of a D-76 refusal: another metric. 10 above the one
 * refused, so it lands after the route already there, not in front of it. */
const nextMetric = computed(() => (Number(form.value.metric) || 0) + 10)
function useNextMetric() {
  form.value.metric = nextMetric.value
  advanced.value = true
  void save()
}

// --- a route with hidden settings: only on/off and removal ----------------

const hiddenOpen = ref(false)
const hiddenRoute = ref<StaticRoute | null>(null)
function openHidden(r: StaticRoute) {
  hiddenRoute.value = r
  hiddenOpen.value = true
}
async function hiddenToggle() {
  const r = hiddenRoute.value
  hiddenOpen.value = false
  if (r) await toggle(r)
}
async function hiddenRemove() {
  const r = hiddenRoute.value
  hiddenOpen.value = false
  if (r) await remove(r)
}

const discard = () => run('discard', () => api.discardStaged())
</script>

<template>
  <section class="vb-rt">
    <header class="vb-rt__head">
      <h1>{{ t('rt.title') }}</h1>
      <el-tag v-if="draftCount" type="warning" size="small" effect="light">
        {{ t('lan.draftPending', { n: draftCount }, draftCount) }}
      </el-tag>
      <el-tag v-if="stale && dataFrom" size="small" type="info">
        {{ t('shell.dataFrom', { time: dataFrom }) }}
      </el-tag>
    </header>
    <p class="vb-rt__hint vb-rt__intro">{{ t('rt.intro') }}</p>

    <el-card v-if="!loaded" shadow="never">
      <el-skeleton :rows="4" animated />
    </el-card>

    <el-card v-else-if="unsupported" shadow="never">
      <el-empty :description="t('rt.unsupported')">
        <p class="vb-rt__hint">{{ t('rt.unsupportedHint') }}</p>
      </el-empty>
    </el-card>

    <el-card v-else-if="!st" shadow="never">
      <el-empty :description="loadError || t('shell.offline')" />
    </el-card>

    <template v-else>
      <el-alert v-if="busy" type="warning" :closable="false" show-icon :title="t('rt.busyTitle')" :description="t('rt.busyHint')" />
      <el-alert v-if="listError" type="error" :closable="false" show-icon :title="t('rt.refused')" :description="listError" />

      <el-card shadow="never">
        <template #header>
          <div class="vb-rt__cardhead">
            <VbIcon name="route" class="vb-rt__muted" />
            <strong>{{ t('rt.listTitle') }}</strong>
            <span class="vb-rt__spacer" />
            <el-button size="small" :disabled="busy" @click="openDialog()">
              <VbIcon name="plus" size="sm" class="vb-rt__btnico" />{{ t('rt.add') }}
            </el-button>
          </div>
        </template>

        <div v-if="!routes.length" class="vb-rt__empty">
          <VbIcon name="route" size="xl" class="vb-rt__muted" />
          <strong>{{ t('rt.emptyTitle') }}</strong>
          <p class="vb-rt__muted">{{ t('rt.empty') }}</p>
          <el-button type="primary" :disabled="busy" @click="openDialog()">
            <VbIcon name="plus" size="sm" class="vb-rt__btnico" />{{ t('rt.add') }}
          </el-button>
        </div>

        <div v-else class="vb-rt__list" role="table">
          <div class="vb-rt__row vb-rt__row--th" role="row">
            <span />
            <span>{{ t('rt.colState') }}</span>
            <span>{{ t('rt.colTarget') }}</span>
            <span>{{ t('rt.colGateway') }}</span>
            <span>{{ t('rt.colIface') }}</span>
            <span>{{ t('rt.colMetric') }}</span>
            <span>{{ t('rt.colName') }}</span>
            <span />
          </div>
          <div
            v-for="r in [...v4, ...v6]"
            :key="r.id"
            class="vb-rt__row"
            :class="{ 'is-off': !r.enabled, 'is-v6': r.family === 'ipv6' }"
            role="row"
          >
            <span class="vb-rt__cell--switch">
              <el-switch
                v-if="r.family !== 'ipv6'"
                :model-value="r.enabled"
                :disabled="busy"
                :loading="working === `r:${r.id}`"
                :aria-label="t('rt.on')"
                @change="toggle(r)"
              />
            </span>
            <span class="vb-rt__cell--state">
              <el-tag size="small" :type="STATE_TAG[stateOf(r)]">{{ STATE_WORD[stateOf(r)]() }}</el-tag>
            </span>
            <span class="vb-rt__cell--target">
              <span class="vb-mono vb-rt__dest">{{ r.target }}</span>
              <!-- Narrower than a table: where it goes reads as one line under
                   the destination, as on the artboards Rt-768 and Rt-360. -->
              <span class="vb-rt__where">
                <template v-if="r.gateway">{{ t('rt.via', { gw: '' }) }}<span class="vb-mono">{{ r.gateway }}</span></template>
                <span v-else class="vb-rt__muted">{{ t('rt.direct') }}</span>
                · {{ ifaceWord(r.interface) }}<span v-if="r.metric" class="vb-rt__muted"> · {{ t('rt.metricN', { n: r.metric }) }}</span>
              </span>
            </span>
            <span class="vb-rt__cell--gw">
              <span v-if="r.gateway" class="vb-rt__nowrap">{{ t('rt.via', { gw: '' }) }}<span class="vb-mono">{{ r.gateway }}</span></span>
              <span v-else class="vb-rt__muted">{{ t('rt.direct') }}</span>
            </span>
            <span class="vb-rt__cell--iface vb-rt__nowrap">
              {{ ifaceWord(r.interface) }} <span v-if="ifaceCode(r.interface)" class="vb-rt__zn">{{ r.interface }}</span>
            </span>
            <span class="vb-rt__cell--metric vb-mono">{{ r.metric ?? 0 }}</span>
            <span class="vb-rt__cell--name">
              <strong v-if="r.name">{{ r.name }}</strong>
              <span v-if="r.family === 'ipv6'" class="vb-rt__muted">{{ t('rt.v6') }}</span>
              <span v-if="stateOf(r) === 'bad'" class="vb-rt__why">
                <VbIcon name="alert" size="sm" /><span>{{ whyNot(r) }}</span>
              </span>
              <span v-if="r.unsupported?.length" class="vb-rt__cond">
                <VbIcon name="alert" size="sm" /><span>{{ t('rt.hidden', { list: hiddenText(r) }) }}</span>
              </span>
              <span v-if="stateOf(r) === 'unknown'" class="vb-rt__hint">{{ t('rt.unknownHint') }}</span>
            </span>
            <span class="vb-rt__acts">
              <template v-if="r.family !== 'ipv6'">
                <el-button v-if="!r.unsupported?.length" text size="small" :disabled="busy" :aria-label="t('rt.edit')" @click="openDialog(r)">
                  <VbIcon name="edit" size="sm" /><span class="vb-rt__actlabel">{{ t('rt.edit') }}</span>
                </el-button>
                <el-button v-else text size="small" :disabled="busy" :aria-label="t('rt.edit')" @click="openHidden(r)">
                  <VbIcon name="lock" size="sm" /><span class="vb-rt__actlabel">{{ t('rt.edit') }}</span>
                </el-button>
                <el-button text size="small" :disabled="busy" :aria-label="t('rt.remove')" @click="remove(r)">
                  <VbIcon name="trash" size="sm" /><span class="vb-rt__actlabel">{{ t('rt.remove') }}</span>
                </el-button>
              </template>
            </span>
          </div>
        </div>
        <p v-if="routes.length" class="vb-rt__hint">{{ t('rt.proof') }}</p>
      </el-card>

      <div v-if="draftCount" class="vb-rt__actions">
        <el-button :disabled="busy" @click="discard">{{ t('lan.cancel') }}</el-button>
      </div>
    </template>

    <el-dialog
      v-model="open"
      :title="form.id ? t('rt.dialogEdit', { name: form.name || form.address }) : t('rt.dialogNew')"
      width="min(560px, calc(100vw - 24px))"
    >
      <el-form label-position="top" class="vb-rt__form">
        <el-form-item :label="t('rt.kind')">
          <el-radio-group v-model="form.kind">
            <el-radio-button value="net">{{ t('rt.kindNet') }}</el-radio-button>
            <el-radio-button value="host">{{ t('rt.kindHost') }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <div v-if="form.kind === 'net'" class="vb-rt__grid">
          <el-form-item :label="t('rt.netAddress')" :error="errors.target">
            <el-input v-model="form.address" class="vb-mono" placeholder="10.8.0.0" />
          </el-form-item>
          <el-form-item :label="t('rt.mask')">
            <el-select v-model="form.bits" class="vb-rt__wide">
              <el-option v-for="m in MASKS" :key="m.bits" :value="m.bits" :label="`${m.dotted} /${m.bits}`" />
            </el-select>
          </el-form-item>
        </div>
        <el-form-item v-else :label="t('rt.hostAddress')" :error="errors.target">
          <el-input v-model="form.address" class="vb-mono" placeholder="10.20.5.17" />
          <div class="vb-rt__hint">{{ t('rt.hostHint') }}</div>
        </el-form-item>
        <div v-if="form.kind === 'net' && !errors.target" class="vb-rt__hint vb-rt__under">{{ t('rt.netHint') }}</div>
        <el-button v-if="clashed" type="primary" size="small" class="vb-rt__fix" :loading="saving" @click="useNextMetric">
          {{ t('rt.setMetric', { n: nextMetric }) }}
        </el-button>

        <el-form-item :label="t('rt.gateway')" :error="errors.gateway">
          <el-input v-model="form.gateway" class="vb-mono" placeholder="192.168.1.2" />
          <div v-if="!errors.gateway" class="vb-rt__hint">{{ t('rt.gatewayHint') }}</div>
        </el-form-item>
        <el-form-item :label="t('rt.iface')" :error="errors.interface">
          <el-select v-model="form.iface" class="vb-rt__wide">
            <el-option :value="AUTO" :label="t('rt.ifaceAuto')" />
            <el-option
              v-for="i in ifaces"
              :key="i.name"
              :value="i.name"
              :label="ifaceCode(i.name) ? `${ifaceWord(i.name)} · ${i.name}` : i.name"
            />
          </el-select>
        </el-form-item>
        <el-button v-if="!advanced" text size="small" class="vb-rt__more" @click="advanced = true">
          {{ t('rt.advanced') }}<VbIcon name="down" size="sm" />
        </el-button>
        <el-form-item v-else :label="t('rt.metric')" :error="errors.metric">
          <el-input-number v-model="form.metric" :min="0" :max="65535" controls-position="right" />
          <div class="vb-rt__hint">{{ t('rt.metricHint') }}</div>
        </el-form-item>
        <el-form-item :label="t('rt.name')" :error="errors.name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item>
          <el-switch v-model="form.enabled" :active-text="t('rt.on')" />
        </el-form-item>
      </el-form>
      <el-alert v-if="formError" type="error" :closable="false" show-icon :title="t('rt.refused')" :description="formError" />
      <template #footer>
        <el-button @click="open = false">{{ t('rt.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" :disabled="busy" @click="save">{{ t('rt.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog
      v-model="hiddenOpen"
      :title="t('rt.dialogEdit', { name: hiddenRoute?.name || hiddenRoute?.target || '' })"
      width="min(560px, calc(100vw - 24px))"
    >
      <template v-if="hiddenRoute">
        <p class="vb-rt__line">
          <el-tag size="small" :type="STATE_TAG[stateOf(hiddenRoute)]">{{ STATE_WORD[stateOf(hiddenRoute)]() }}</el-tag>
          <span class="vb-mono">{{ hiddenRoute.target }}</span>
          <span v-if="hiddenRoute.gateway">{{ t('rt.via', { gw: hiddenRoute.gateway }) }}</span>
        </p>
        <el-alert type="warning" :closable="false" show-icon :title="t('rt.hiddenPanel', { list: hiddenText(hiddenRoute) })" />
        <p class="vb-rt__hint">{{ t('rt.hiddenPanelHint') }}</p>
      </template>
      <template #footer>
        <el-button :disabled="busy" @click="hiddenRemove"><VbIcon name="trash" size="sm" class="vb-rt__btnico" />{{ t('rt.remove') }}</el-button>
        <el-button type="primary" :disabled="busy" @click="hiddenToggle">
          {{ hiddenRoute?.enabled ? t('rt.switchOff') : t('rt.switchOn') }}
        </el-button>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped>
.vb-rt {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.vb-rt__head {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-rt__head h1 {
  margin: 0;
  font-size: 22px;
}
.vb-rt__intro {
  margin: -6px 0 0;
}
.vb-rt__spacer {
  flex: 1 1 auto;
}
.vb-rt__cardhead {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-rt__btnico {
  margin-right: 6px;
}
.vb-rt__hint {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
.vb-rt__under {
  margin: -12px 0 12px;
}
.vb-rt__muted {
  color: var(--vb-muted);
}
.vb-rt__zn {
  font-family: var(--vb-font-mono, ui-monospace, monospace);
  font-size: 11px;
  color: var(--vb-muted);
}
.vb-rt__nowrap {
  white-space: nowrap;
}
.vb-rt__empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 18px 12px;
  text-align: center;
}
.vb-rt__empty p {
  max-width: 520px;
  margin: 0 0 6px;
}
.vb-rt__why,
.vb-rt__cond {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  margin-top: 4px;
  font-size: 12px;
}
.vb-rt__why {
  color: var(--el-color-danger);
}
.vb-rt__cond {
  color: var(--el-color-warning);
}
.vb-rt__why .vb-ico,
.vb-rt__cond .vb-ico {
  flex: 0 0 auto;
  margin-top: 2px;
}
.vb-rt__line {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
  margin: 0 0 12px;
}

/* A grid of rows, not el-table: the same markup restacks into cards on a
   phone, with the actions kept (the firewall screen does the same). */
.vb-rt__list {
  display: flex;
  flex-direction: column;
  margin: 0 -20px;
  font-size: 13px;
}
.vb-rt__row {
  display: grid;
  /* Fixed widths where content varies per row: every row is its own grid, and
     an `auto` column there sizes itself per row, so headers drift off. */
  grid-template-columns: 48px 150px minmax(130px, 1fr) minmax(130px, 1fr) minmax(140px, 1fr) 64px minmax(160px, 1.6fr) 84px;
  align-items: center;
  gap: 8px 14px;
  padding: 8px 20px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.vb-rt__row--th {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--vb-muted);
}
.vb-rt__row.is-off {
  color: var(--vb-muted);
}
.vb-rt__row.is-v6 {
  background: var(--el-fill-color-light);
}
.vb-rt__dest {
  font-size: 14px;
  font-weight: 500;
  white-space: nowrap;
}
.vb-rt__cell--target {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.vb-rt__where {
  display: none;
}
.vb-rt__cell--name {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.vb-rt__cell--state {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.vb-rt__acts {
  display: flex;
  justify-content: flex-end;
  white-space: nowrap;
}
.vb-rt__actlabel {
  display: none;
}
.vb-rt__actions {
  display: flex;
  justify-content: flex-end;
}
.vb-rt__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}
.vb-rt__wide {
  width: 100%;
}
.vb-rt__more {
  margin: -8px 0 12px;
}
.vb-rt__fix {
  margin: -6px 0 16px;
}
/* Element Plus positions a field's error absolutely, below the field: a
   two-line refusal then lies over whatever comes next — here, over the
   one-click "Set metric 10" button, which could not be pressed (measured in
   a browser on the router). The firewall screen hit the same (#36). */
.vb-rt__form :deep(.el-form-item__error) {
  position: static;
  padding-top: 4px;
  line-height: 1.4;
}

/* A tablet: gateway, connection and metric fold into one line under the
   destination; their own columns go away, headers included. */
@media (width <= 1100px) {
  .vb-rt__row {
    grid-template-columns: 48px 150px minmax(0, 1.2fr) minmax(0, 1fr) 84px;
  }
  .vb-rt__row > :nth-child(n + 4):nth-child(-n + 6) {
    display: none;
  }
  .vb-rt__where {
    display: block;
  }
}

/* A phone: every route becomes a card, and actions get their words back. */
@media (width <= 700px) {
  .vb-rt__list {
    gap: 10px;
    margin: 0;
  }
  .vb-rt__list .vb-rt__row.vb-rt__row--th {
    display: none;
  }
  .vb-rt__row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 10px;
    padding: 12px 14px;
    border: 1px solid var(--el-border-color-light);
    border-radius: 10px;
  }
  .vb-rt__row > * {
    grid-row: auto !important;
    grid-column: auto !important;
  }
  .vb-rt__row > :not(.vb-rt__cell--switch, .vb-rt__cell--state) {
    flex: 0 0 100%;
  }
  .vb-rt__cell--state {
    order: -1;
  }
  .vb-rt__cell--switch {
    order: 0;
    margin-left: auto;
  }
  .vb-rt__row > :nth-child(n + 3) {
    order: 1;
  }
  .vb-rt__acts {
    order: 9;
    flex-wrap: wrap;
  }
  .vb-rt__actlabel {
    display: inline;
    margin-left: 4px;
  }
  .vb-rt__grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
