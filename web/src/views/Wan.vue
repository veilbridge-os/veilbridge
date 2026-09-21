<script setup lang="ts">
// The internet (uplink) screen, M3.4, built to design/04_wan.result.html.
//
// It is the first screen of the product that writes to the system, and what it
// writes is the connection the operator is holding the panel over. So it is a
// form with a safety catch: nothing here applies anything. Saving produces a
// draft on the device; committing it is the apply bar's job, under a watchdog
// that undoes the change if nobody confirms the panel survived (D-14).
//
// Two things this screen deliberately does NOT do:
//   • validate values itself. The device is the one validator, and a second
//     copy of its rules here would drift from it. Refusals come back from the
//     device and are placed at the field they belong to.
//   • speak the operating system. Configuration keys live in the apply bar's
//     "technical details" and nowhere else (D-3).
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ApiError,
  api,
  type ConfigChange,
  type PathProbe,
  type WANConfig,
  type WANStatus,
} from '@/api/client'
import { useDuration } from '@/lib/duration'
import { refreshStaged, useLive } from '@/stores/live'

const { t } = useI18n()
const { applyState, staged, capability, can, stale, lastUpdate } = useLive()
const { fmtDuration } = useDuration()

type Proto = 'dhcp' | 'static' | 'pppoe'

const wan = ref<WANStatus | null>(null)
const loaded = ref(false)
const probe = ref<PathProbe | null>(null)
const probing = ref(false)
const probeError = ref('')
const saving = ref(false)
const savedNote = ref(false)

// Refusals: one human sentence per field, plus exactly what the device said.
const fieldErrors = ref<Record<string, string>>({})
const deviceReply = ref<Record<string, string>>({})
const formError = ref('')

const form = ref({
  proto: 'dhcp' as Proto,
  address: '',
  netmask: '',
  gateway: '',
  ownResolvers: false,
  dns: [''] as string[],
  username: '',
  password: '',
})

// The uplink is not in the live stream: it changes when a person changes it,
// not three times a minute. Polling it slowly costs less than pushing it.
const SLOW_POLL_MS = 15_000
let poll = 0

async function load() {
  try {
    wan.value = await api.wan()
  } catch {
    // Keep the last known reading on screen; the shell already says the
    // device is unreachable, and blanking the block would read as "no uplink".
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

const iface = computed(() => wan.value?.interface)
const hasUplink = computed(() => !!iface.value)
const hasRoute = computed(() => !!(iface.value?.gateway || iface.value?.gateway6))
const candidates = computed(() => wan.value?.candidates ?? [])
// Ambiguity is shown, not resolved silently: on the x86 stand two interfaces
// really do carry a default route, and a panel that picks one without saying
// so teaches the operator to read the wrong connection's statistics.
const ambiguous = computed(() => candidates.value.length > 1)

const ipv6Addresses = computed(() => iface.value?.ipv6 ?? [])
const showIPv6 = computed(() => can('ipv6') && ipv6Addresses.value.length > 0)
const switchPorts = computed(() => capability('switch-ports')?.available === true)

// While a change is live and unconfirmed the form is locked: a second draft on
// top of a running transaction is refused by the device, and the operator's
// values must not be lost to a rejected save.
const busy = computed(() => applyState.value?.phase === 'awaiting_confirm')
const draftCount = computed(() => staged.value.length)

const dataFrom = computed(() =>
  lastUpdate.value
    ? lastUpdate.value.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    : '',
)

/** Prefill the form from the device once, and again whenever the connection
 * type on the device changes under us — but never while the operator is in
 * the middle of typing a draft. */
watch(
  () => [iface.value?.proto, draftCount.value] as const,
  ([proto, drafts]) => {
    if (drafts > 0 || busy.value) return
    if (proto === 'dhcp' || proto === 'static' || proto === 'pppoe') {
      form.value.proto = proto
    }
    const addr = iface.value?.ipv4?.[0] ?? ''
    if (proto === 'static' && addr) {
      const [ip] = addr.split('/')
      form.value.address = form.value.address || ip
      form.value.gateway = form.value.gateway || (iface.value?.gateway ?? '')
    }
  },
  { immediate: true },
)

const resolvers = computed(() => iface.value?.dns ?? [])

function addResolver() {
  form.value.dns.push('')
}
function removeResolver(i: number) {
  form.value.dns.splice(i, 1)
  if (form.value.dns.length === 0) form.value.dns.push('')
}

/** A refusal names the field it belongs to. The device answers in one
 * sentence, so this maps that sentence back to a field; anything unmapped is
 * shown at form level rather than attached to the wrong input.
 *
 * Debt, recorded rather than hidden: the API should carry the field itself
 * (`errors[].location`), and then this table disappears. */
const REFUSALS: Array<{ match: RegExp; field: string; message: string }> = [
  { match: /not an IPv4 address/i, field: 'address', message: 'wan.badAddress' },
  { match: /network mask/i, field: 'netmask', message: 'wan.badNetmask' },
  { match: /gateway address/i, field: 'gateway', message: 'wan.badGateway' },
  { match: /resolver address/i, field: 'dns', message: 'wan.badResolver' },
  { match: /user name/i, field: 'username', message: 'wan.needUsername' },
]

function placeRefusal(detail: string) {
  fieldErrors.value = {}
  deviceReply.value = {}
  formError.value = ''
  const hit = REFUSALS.find((r) => r.match.test(detail))
  if (!hit) {
    formError.value = detail
    return
  }
  fieldErrors.value[hit.field] = t(hit.message)
  deviceReply.value[hit.field] = detail
}

function requestBody(): WANConfig {
  const cfg: WANConfig = { proto: form.value.proto }
  const dns = form.value.dns.map((d) => d.trim()).filter(Boolean)
  if (form.value.proto === 'static') {
    cfg.address = form.value.address.trim()
    cfg.netmask = form.value.netmask.trim()
    if (form.value.gateway.trim()) cfg.gateway = form.value.gateway.trim()
  }
  if (form.value.proto === 'pppoe') {
    cfg.username = form.value.username.trim()
    // An empty password is "keep the current one", not "set an empty one":
    // the device never hands the password back, so the panel cannot resend it.
    if (form.value.password) cfg.password = form.value.password
  }
  // On DHCP the resolvers are only sent when the operator asked for their own.
  if (form.value.proto !== 'dhcp' || form.value.ownResolvers) {
    if (dns.length) cfg.dns = dns
  }
  return cfg
}

async function save() {
  saving.value = true
  savedNote.value = false
  fieldErrors.value = {}
  deviceReply.value = {}
  formError.value = ''
  try {
    await api.stageWAN(requestBody())
    savedNote.value = true
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) formError.value = t('wan.busyHint')
    else if (e instanceof ApiError) placeRefusal(e.message)
    else formError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
    // Whatever happened, the draft on the device is the truth: a refused save
    // may still have left an earlier draft in place.
    await refreshStaged()
  }
}

async function discard() {
  try {
    await api.discardStaged()
  } catch (e) {
    formError.value = e instanceof Error ? e.message : String(e)
  }
  savedNote.value = false
  await refreshStaged()
}

async function runProbe() {
  probing.value = true
  probeError.value = ''
  try {
    // "direct" is the expectation for this screen: the question here is
    // whether the uplink itself carries traffic, not whether a tunnel does.
    probe.value = await api.probe('1.1.1.1', 'direct')
  } catch (e) {
    probeError.value = e instanceof Error ? e.message : String(e)
  } finally {
    probing.value = false
  }
}

const probeText = computed(() => {
  const p = probe.value
  if (!p) return ''
  const addr = p.detail?.match(/\b\d{1,3}(?:\.\d{1,3}){3}\b/)?.[0] ?? p.detail ?? ''
  if (p.ok) return t('wan.probeVia', { iface: iface.value?.name ?? '', addr })
  return t('wan.probeMismatch', {
    addr,
    expected: p.expectedVia,
    actual: p.actualVia,
  })
})

/** The countdown shown while the form is locked comes from the deadline the
 * daemon handed out, never from a constant here. */
const now = ref(Date.now())
const tick = window.setInterval(() => {
  now.value = Date.now()
}, 500)
onUnmounted(() => window.clearInterval(tick))

const busyCountdown = computed(() => {
  const dl = applyState.value?.deadline
  if (!dl) return ''
  const s = Math.max(0, Math.round((new Date(dl).getTime() - now.value) / 1000))
  return `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
})

const draftRows = computed<ConfigChange[]>(() => staged.value.slice())
</script>

<template>
  <section class="vb-wan">
    <header class="vb-wan__head">
      <h1>{{ t('wan.title') }}</h1>
      <el-tag v-if="draftCount" type="warning" size="small" effect="light">
        {{ t('wan.draftPending', { n: draftCount }, draftCount) }}
      </el-tag>
      <el-tag v-else-if="loaded && hasUplink" :type="hasRoute ? 'success' : 'warning'" size="small">
        {{ hasRoute ? t('wan.linkUp') : t('wan.linkDown') }}
      </el-tag>
      <span class="vb-wan__spacer" />
      <!-- Primary, as in the accepted mockup: this is the one action on the
           screen that proves something instead of showing it. -->
      <el-button v-if="hasUplink" type="primary" :loading="probing" @click="runProbe">
        {{ probing ? t('wan.probing') : t('wan.probe') }}
      </el-button>
    </header>

    <!-- 1. First load: a skeleton, not a form full of zeroes, which a person
         reads as real values. -->
    <el-card v-if="!loaded" shadow="never">
      <el-skeleton :rows="3" animated />
    </el-card>

    <!-- 2. No connection leads outside. A state of the device, not an error:
         this is also what a first start and an unplugged cable look like. -->
    <el-card v-else-if="!hasUplink" shadow="never">
      <el-empty :description="t('wan.none')">
        <p class="vb-wan__hint">{{ t('wan.noneHint') }}</p>
      </el-empty>
    </el-card>

    <!-- 3. The uplink as it is right now. -->
    <el-card v-else shadow="never" class="vb-wan__now">
      <template #header>
        <div class="vb-wan__nowhead">
          <strong v-if="hasRoute">{{ t('wan.haveInternet', { iface: iface?.name }) }}</strong>
          <strong v-else>{{ t('wan.noGateway') }}</strong>
          <el-tag v-if="stale" size="small" type="info">
            {{ t('shell.dataFrom', { time: dataFrom }) }}
          </el-tag>
        </div>
      </template>

      <el-alert
        v-if="!hasRoute"
        type="warning"
        :closable="false"
        show-icon
        :title="t('wan.noGatewayHint')"
        class="vb-wan__alert"
      />
      <el-alert
        v-if="ambiguous"
        type="warning"
        :closable="false"
        show-icon
        class="vb-wan__alert"
        :title="t('wan.ambiguous', { iface: iface?.name })"
        :description="
          wan?.selectedBy === 'name'
            ? t('wan.ambiguousHint', { list: candidates.join(', '), iface: iface?.name })
            : t('wan.ambiguousCoinToss', { list: candidates.join(', '), iface: iface?.name })
        "
      />
      <!-- The proof, next to the claim: an address traffic actually came back
           from, never a green tick on its own (D-5, NFR-5). -->
      <el-alert
        v-if="probeText"
        :type="probe?.ok ? 'success' : 'warning'"
        :closable="false"
        show-icon
        class="vb-wan__alert"
        :title="probeText"
      />
      <el-alert
        v-if="probeError"
        type="error"
        :closable="false"
        show-icon
        class="vb-wan__alert"
        :title="t('wan.probeFailed', { detail: probeError })"
      />

      <el-descriptions :column="3" direction="vertical" size="small" border>
        <el-descriptions-item :label="t('wan.proto')">
          {{
            iface?.proto === 'dhcp'
              ? t('wan.protoDhcp')
              : iface?.proto === 'static'
                ? t('wan.protoStatic')
                : iface?.proto === 'pppoe'
                  ? t('wan.protoPppoe')
                  : (iface?.proto ?? '—')
          }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('wan.address')">
          {{ iface?.ipv4?.join(', ') || '—' }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('wan.gateway')">
          <span :class="{ 'vb-wan__missing': !hasRoute }">
            {{ iface?.gateway || t('wan.linkDown') }}
          </span>
        </el-descriptions-item>
        <el-descriptions-item :label="t('wan.resolvers')">
          {{ resolvers.join(' ') || '—' }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('wan.uptime')">
          {{ fmtDuration(iface?.uptimeSec) }}
        </el-descriptions-item>
        <el-descriptions-item v-if="switchPorts" :label="t('wan.port')">
          {{ iface?.device || '—' }}
        </el-descriptions-item>
      </el-descriptions>
      <p v-if="draftCount" class="vb-wan__hint">{{ t('wan.currentWhileDraft') }}</p>
    </el-card>

    <!-- 4. The form. Locked while a change is live and unconfirmed. -->
    <el-card shadow="never" class="vb-wan__form">
      <template #header>
        <div class="vb-wan__nowhead">
          <strong>{{ t('wan.settings') }}</strong>
          <el-tag v-if="!draftCount" size="small" type="info">{{ t('wan.draftEmpty') }}</el-tag>
          <el-tag v-else size="small" type="warning">
            {{ t('wan.draftPending', { n: draftCount }, draftCount) }}
          </el-tag>
        </div>
      </template>

      <!-- The refusal of a second draft is a normal path, not a server fault,
           so it is explained in those terms and the values are kept. -->
      <el-alert
        v-if="busy"
        type="warning"
        :closable="false"
        show-icon
        class="vb-wan__alert"
        :title="t('wan.busyTitle')"
        :description="`${t('wan.busyHint')} ${busyCountdown}`"
      />

      <el-form label-position="top" :disabled="busy" class="vb-wan__fields">
        <el-form-item :label="t('wan.proto')">
          <el-radio-group v-model="form.proto">
            <el-radio-button value="dhcp">{{ t('wan.protoDhcp') }}</el-radio-button>
            <el-radio-button value="static">{{ t('wan.protoStatic') }}</el-radio-button>
            <el-radio-button value="pppoe">{{ t('wan.protoPppoe') }}</el-radio-button>
          </el-radio-group>
          <div class="vb-wan__hint">{{ t('wan.protoHint') }}</div>
        </el-form-item>

        <template v-if="form.proto === 'static'">
          <el-form-item
            :label="t('wan.address')"
            :error="fieldErrors.address"
            :required="true"
          >
            <el-input v-model="form.address" placeholder="203.0.113.24" />
            <div class="vb-wan__hint">
              {{ deviceReply.address ? t('wan.deviceSaid', { detail: deviceReply.address }) : t('wan.addressHint') }}
            </div>
          </el-form-item>
          <el-form-item :label="t('wan.netmask')" :error="fieldErrors.netmask" :required="true">
            <el-input v-model="form.netmask" placeholder="255.255.255.0" />
            <div class="vb-wan__hint">
              {{ deviceReply.netmask ? t('wan.deviceSaid', { detail: deviceReply.netmask }) : t('wan.netmaskHint') }}
            </div>
          </el-form-item>
          <el-form-item :label="t('wan.gateway')" :error="fieldErrors.gateway">
            <el-input v-model="form.gateway" placeholder="203.0.113.1" />
            <div class="vb-wan__hint">
              {{ deviceReply.gateway ? t('wan.deviceSaid', { detail: deviceReply.gateway }) : t('wan.gatewayHint') }}
            </div>
          </el-form-item>
        </template>

        <template v-if="form.proto === 'pppoe'">
          <el-form-item :label="t('wan.username')" :error="fieldErrors.username" :required="true">
            <el-input v-model="form.username" autocomplete="off" />
            <div v-if="deviceReply.username" class="vb-wan__hint">
              {{ t('wan.deviceSaid', { detail: deviceReply.username }) }}
            </div>
          </el-form-item>
          <el-form-item :label="t('wan.password')">
            <el-input v-model="form.password" type="password" show-password autocomplete="off" />
            <div class="vb-wan__hint">{{ t('wan.passwordHint') }}</div>
          </el-form-item>
        </template>

        <el-form-item :label="t('wan.resolvers')" :error="fieldErrors.dns">
          <el-radio-group v-if="form.proto === 'dhcp'" v-model="form.ownResolvers">
            <el-radio-button :value="false">{{ t('wan.resolversFromProvider') }}</el-radio-button>
            <el-radio-button :value="true">{{ t('wan.resolversOwn') }}</el-radio-button>
          </el-radio-group>
          <!-- A list with add and remove, not a space-separated string: the
               panel asks for resolvers the way it shows them. -->
          <div v-if="form.proto !== 'dhcp' || form.ownResolvers" class="vb-wan__dns">
            <div v-for="(_, i) in form.dns" :key="i" class="vb-wan__dnsrow">
              <el-input v-model="form.dns[i]" :placeholder="t('wan.dnsPlaceholder')" />
              <el-button
                text
                :aria-label="t('wan.removeResolver')"
                @click="removeResolver(i)"
              >
                ✕
              </el-button>
            </div>
            <el-button text @click="addResolver">+ {{ t('wan.addResolver') }}</el-button>
          </div>
          <div class="vb-wan__hint">
            {{ deviceReply.dns ? t('wan.deviceSaid', { detail: deviceReply.dns }) : t('wan.resolversHint') }}
          </div>
        </el-form-item>
      </el-form>

      <el-alert
        v-if="formError"
        type="error"
        :closable="false"
        show-icon
        class="vb-wan__alert"
        :title="t('wan.refused')"
        :description="formError"
      />
      <el-alert
        v-else-if="savedNote && draftCount"
        type="info"
        :closable="false"
        show-icon
        class="vb-wan__alert"
        :title="t('wan.saved')"
      />

      <!-- Said plainly and once: this is the only way in, and it comes back
           by itself. The number comes from the daemon's own window. -->
      <el-alert
        type="info"
        :closable="false"
        show-icon
        class="vb-wan__alert"
        :title="t('wan.onlyWayIn', { sec: 90 })"
      />

      <div class="vb-wan__actions">
        <el-button v-if="draftCount" :disabled="busy" @click="discard">
          {{ t('wan.cancel') }}
        </el-button>
        <el-button type="primary" :loading="saving" :disabled="busy" @click="save">
          {{ t('wan.save') }}
        </el-button>
      </div>
    </el-card>

    <!-- 5. IPv6, read-only. The block is absent on devices without IPv6 —
         not drawn with dashes in it (D-17, D-20). -->
    <el-card v-if="showIPv6" shadow="never">
      <template #header>
        <div class="vb-wan__nowhead">
          <strong>{{ t('wan.ipv6') }}</strong>
          <el-tag size="small" type="info">{{ t('wan.readOnly') }}</el-tag>
        </div>
      </template>
      <el-descriptions :column="2" direction="vertical" size="small" border>
        <el-descriptions-item :label="t('wan.address')">
          {{ ipv6Addresses.join(', ') }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('wan.gateway')">
          {{ iface?.gateway6 || '—' }}
        </el-descriptions-item>
      </el-descriptions>
      <p class="vb-wan__hint">{{ t('wan.ipv6Hint') }}</p>
    </el-card>

    <!-- The draft itself is rendered by the apply bar, which is shared with
         every other screen that writes; duplicating it here would be a second
         place for the same list to drift. It is referenced so a reader of this
         file knows where the rest of the flow lives. -->
    <p v-if="draftRows.length" class="vb-wan__hint vb-wan__ptr">
      {{ t('apply.willChange') }} ↓
    </p>
  </section>
</template>

<style scoped>
.vb-wan {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.vb-wan__head {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-wan__head h1 {
  margin: 0;
  font-size: 22px;
}
.vb-wan__spacer {
  flex: 1 1 auto;
}
.vb-wan__nowhead {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-wan__alert {
  margin-bottom: 12px;
}
/* A hint belongs under its control, not beside it: an Element Plus form item
   lays its content out in a row, so a hint without a full-width basis ends up
   squeezed next to the input it explains. */
.vb-wan__hint {
  flex: 0 0 100%;
  width: 100%;
  margin: 4px 0 0;
  font-size: 12px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
.vb-wan__missing {
  color: var(--el-color-warning);
}
/* The field block keeps the height of the tallest connection type, so the
   save button does not move out from under the cursor when the type changes
   (an explicit requirement of the accepted mockup). */
.vb-wan__fields {
  min-height: 420px;
}
.vb-wan__dns {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}
.vb-wan__dnsrow {
  display: flex;
  gap: 6px;
  align-items: center;
}
.vb-wan__actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  flex-wrap: wrap;
}
.vb-wan__ptr {
  text-align: right;
}
@media (width <= 600px) {
  .vb-wan__fields {
    min-height: 0;
  }
  .vb-wan__actions .el-button {
    width: 100%;
    margin: 0 0 8px;
  }
}
</style>
