<script setup lang="ts">
// Wi-Fi (#57), built to the accepted artboards `Wifi-*` (design task 09,
// D-100…D-104).
//
// Two kinds of edit, and the screen makes the difference visible:
//   - the RADIO (channel, width, band on/off, country) is drafted and goes
//     through the apply bar with its confirmation window: a bad channel is
//     cured by putting the old one back;
//   - a network's NAME and PASSWORD apply at once, after a dialog that shows
//     the new password and its QR code and says who will be disconnected.
//     No timer: phones that saw the new password refused do not come back
//     when the old one is restored (measured with two phones, D-100).
//
// The page is opened most often from a phone sitting on this very Wi-Fi, so
// every warning names how the reader is connected ("you are on 5 GHz").
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ApiError, api, type WiFiNetwork, type WiFiRadio, type WiFiStatus } from '@/api/client'
import VbIcon from '@/components/VbIcon.vue'
import {
  bandWord,
  COUNTRIES,
  countryName,
  generatePassword,
  passwordProblem,
  type QRPicture,
  securityWord,
  ssidBytes,
  wifiQR,
  wifiQRText,
} from '@/lib/wifi'
import { refreshStaged, useLive } from '@/stores/live'

const { t, locale } = useI18n()
const router = useRouter()
const { applyState, staged, stale, lastUpdate } = useLive()

const st = ref<WiFiStatus | null>(null)
const loaded = ref(false)
const missing = ref(false) // 404: no radio on this device
const unsupported = ref(false) // 501: this build cannot read Wi-Fi
const loadError = ref('')

const POLL_MS = 5_000
let poll = 0

async function load() {
  try {
    const got = await api.wifi()
    missing.value = got === null
    st.value = got
    unsupported.value = false
    loadError.value = ''
  } catch (e) {
    if (e instanceof ApiError && e.status === 501) unsupported.value = true
    else if (!st.value) loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loaded.value = true
  }
}

// On a phone the radio cards shrink to one line (D-104).
const narrow = ref(false)
let mq: MediaQueryList | null = null
const onMQ = () => {
  narrow.value = !!mq?.matches
}

onMounted(() => {
  void load()
  void refreshStaged()
  poll = window.setInterval(load, POLL_MS)
  mq = window.matchMedia('(max-width: 560px)')
  onMQ()
  mq.addEventListener('change', onMQ)
})
onUnmounted(() => {
  window.clearInterval(poll)
  mq?.removeEventListener('change', onMQ)
})

const radios = computed<WiFiRadio[]>(() => st.value?.radios ?? [])
const networks = computed<WiFiNetwork[]>(() => st.value?.networks ?? [])
const here = computed(() => st.value?.here)
const busy = computed(() => applyState.value?.phase === 'awaiting_confirm')
const draftCount = computed(() => staged.value.length)
const dataFrom = computed(() =>
  lastUpdate.value
    ? lastUpdate.value.toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
    : '',
)

// --- words ----------------------------------------------------------------

const band = (b: string) => bandWord(b, locale.value)
const radioName = (r: WiFiRadio) => t('wifi.bandName', { band: band(r.band) })
const radioById = (id: string) => radios.value.find((r) => r.id === id)
const netBands = (n: WiFiNetwork) => {
  const bands = (n.radios ?? []).map((id) => radioById(id)?.band).filter((b): b is string => !!b)
  return t('wifi.bandsOf', { bands: bands.map(band).join(t('wifi.and')) })
}
const hereWords = computed(() => {
  const h = here.value
  if (!h) return ''
  if (h.kind === 'cable') return t('wifi.hereCable')
  if (h.kind === 'wifi') return t('wifi.hereWiFi', { band: band(h.band ?? '') })
  return ''
})
const hereOn = (radioID: string) => here.value?.kind === 'wifi' && here.value.radio === radioID
const devicesWord = (n: number) => t('wifi.nDevices', { n }, n)

function channelWords(r: WiFiRadio): string {
  if (!r.enabled) return t('wifi.off')
  if (r.state === 'starting') return t('wifi.choosing')
  if (r.state === 'checking-radar') return t('wifi.radarNow')
  const now = r.channelNow || r.channel || 0
  return r.auto ? t('wifi.chanAuto', { n: now }) : String(now || r.channel || '—')
}
type Tag = 'success' | 'warning' | 'info' | 'danger'
function stateTag(r: WiFiRadio): { type: Tag; word: string } {
  if (!r.enabled) return { type: 'info', word: t('wifi.stateOff') }
  switch (r.state) {
    case 'up':
      return { type: 'success', word: t('wifi.stateUp') }
    case 'starting':
      return { type: 'warning', word: t('wifi.choosing') }
    case 'checking-radar':
      return { type: 'warning', word: t('wifi.radarNow') }
  }
  return { type: 'danger', word: t('wifi.stateDown') }
}
const country = computed(() => st.value?.country ?? '')
const hasFiveGHz = computed(() => radios.value.some((r) => r.band === '5'))

// --- password and QR --------------------------------------------------------

const shown = ref<Record<string, string>>({})
const passErr = ref<Record<string, boolean>>({})

async function fetchPassword(id: string): Promise<string | null> {
  try {
    const got = await api.wifiPassword(id)
    passErr.value = { ...passErr.value, [id]: false }
    return got.password
  } catch {
    passErr.value = { ...passErr.value, [id]: true }
    return null
  }
}
async function showPassword(n: WiFiNetwork) {
  const p = await fetchPassword(n.id)
  if (p !== null) shown.value = { ...shown.value, [n.id]: p }
}
function hidePassword(n: WiFiNetwork) {
  const next = { ...shown.value }
  delete next[n.id]
  shown.value = next
}
const copied = ref('')
async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = what
    window.setTimeout(() => {
      if (copied.value === what) copied.value = ''
    }, 2000)
  } catch {
    // Plain HTTP on a phone has no clipboard; the password is on screen.
  }
}

const qrOpen = ref(false)
const qr = ref<{ ssid: string; password: string; pic: QRPicture | null }>({
  ssid: '',
  password: '',
  pic: null,
})
async function openQR(n: WiFiNetwork) {
  const p = shown.value[n.id] ?? (await fetchPassword(n.id))
  if (p === null) return
  qr.value = { ssid: n.ssid, password: p, pic: await wifiQR(wifiQRText(n.ssid, p, n.security)) }
  qrOpen.value = true
}
function printQR() {
  document.body.classList.add('vb-printing-qr')
  window.print()
  document.body.classList.remove('vb-printing-qr')
}

// --- name and password: applied at once (D-100) ------------------------------

const accessOpen = ref(false)
const accessStep = ref<'form' | 'confirm'>('form')
const accessNet = ref<WiFiNetwork | null>(null)
const form = ref({ ssid: '', password: '', security: 'wpa2' })
const formErr = ref<Record<string, string>>({})
const accessFail = ref('')
const applying = ref(false)
const confirmPic = ref<QRPicture | null>(null)
const confirmPassword = ref('')

const offered = ['wpa2-wpa3', 'wpa2']
function openAccess(n: WiFiNetwork) {
  accessNet.value = n
  form.value = {
    ssid: n.ssid,
    password: '',
    security: offered.includes(n.security) ? n.security : 'wpa2',
  }
  formErr.value = {}
  accessFail.value = ''
  accessStep.value = 'form'
  accessOpen.value = true
}
const ssidLen = computed(() => ssidBytes(form.value.ssid))
function checkForm(): boolean {
  const e: Record<string, string> = {}
  if (!form.value.ssid) e.ssid = t('wifi.errNoName')
  else if (ssidLen.value > 32) e.ssid = t('wifi.errLong', { n: ssidLen.value })
  if (form.value.password) {
    const p = passwordProblem(form.value.password)
    if (p) e.password = t(`wifi.errPass_${p}`)
  }
  formErr.value = e
  return Object.keys(e).length === 0
}
const accessChanged = computed(() => {
  const n = accessNet.value
  if (!n) return false
  return (
    form.value.ssid !== n.ssid || form.value.password !== '' || form.value.security !== n.security
  )
})
async function toConfirm() {
  const n = accessNet.value
  if (!n || !checkForm()) return
  const p = form.value.password || (shown.value[n.id] ?? (await fetchPassword(n.id)) ?? '')
  confirmPassword.value = p
  confirmPic.value = await wifiQR(wifiQRText(form.value.ssid, p, form.value.security))
  accessStep.value = 'confirm'
}
/** How the reader will feel this edit: on this network over Wi-Fi, on a cable,
 * or somewhere else (a tunnel, the internet side). */
const accessWho = computed<'self' | 'cable' | 'other'>(() => {
  const n = accessNet.value
  const h = here.value
  if (n && h?.kind === 'wifi' && h.radio && (n.radios ?? []).includes(h.radio)) return 'self'
  if (h?.kind === 'cable') return 'cable'
  return 'other'
})

const after = ref<{ at: string; before: number; id: string } | null>(null)
// The answer to the change can be lost with the Wi-Fi it changes: the router
// replies before the access point restarts (about 2 s), but a slow phone may
// miss it. A dropped connection here is the expected outcome, not an error.
const dropped = ref<{ ssid: string; password: string } | null>(null)
async function applyAccess() {
  const n = accessNet.value
  if (!n) return
  applying.value = true
  accessFail.value = ''
  try {
    await api.setWiFiAccess(n.id, {
      ssid: form.value.ssid,
      password: form.value.password || undefined,
      security: form.value.security as 'wpa2' | 'wpa2-wpa3',
    })
    after.value = {
      at: new Date().toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' }),
      before: n.devices,
      id: n.id,
    }
    shown.value = {}
    accessOpen.value = false
    await load()
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) accessFail.value = t('wifi.refusedDraft')
    else if (e instanceof ApiError && Object.keys(e.fields).length) {
      formErr.value = Object.fromEntries(Object.entries(e.fields).map(([k, v]) => [k, v]))
      accessStep.value = 'form'
    } else if (!(e instanceof ApiError)) {
      dropped.value = { ssid: form.value.ssid, password: confirmPassword.value }
      accessOpen.value = false
    } else accessFail.value = e.message
  } finally {
    applying.value = false
  }
}
const afterNet = computed(() => networks.value.find((n) => n.id === after.value?.id))

// --- the radio: drafted, through the apply bar ---------------------------------

const radioOpen = ref(false)
const radioEdit = ref<{ r: WiFiRadio | null; enabled: boolean; channel: string; width: number }>({
  r: null,
  enabled: true,
  channel: 'auto',
  width: 20,
})
const radioErr = ref<Record<string, string>>({})
const radioFail = ref('')
function openRadio(r: WiFiRadio) {
  radioEdit.value = {
    r,
    enabled: r.enabled,
    channel: r.auto ? 'auto' : String(r.channel ?? 'auto'),
    width: r.width || (r.widths?.[0] ?? 20),
  }
  radioErr.value = {}
  radioFail.value = ''
  radioOpen.value = true
}
const radarChosen = computed(() => {
  const r = radioEdit.value.r
  return !!r?.channels?.find((c) => String(c.channel) === radioEdit.value.channel)?.radar
})
const lastOff = computed(() => {
  const r = radioEdit.value.r
  if (!r || radioEdit.value.enabled) return false
  return radios.value.every((x) => x.id === r.id || !x.enabled)
})
async function stageRadio() {
  const r = radioEdit.value.r
  if (!r) return
  radioFail.value = ''
  try {
    await api.stageWiFiRadio(r.id, {
      enabled: radioEdit.value.enabled,
      channel: radioEdit.value.channel,
      width: radioEdit.value.width,
    })
    radioOpen.value = false
    await refreshStaged()
  } catch (e) {
    if (e instanceof ApiError && Object.keys(e.fields).length) radioErr.value = e.fields
    else if (e instanceof ApiError && e.status === 409) radioFail.value = t('wifi.refusedBusy')
    else radioFail.value = e instanceof Error ? e.message : String(e)
  }
}

const countryOpen = ref(false)
const countryPick = ref('')
const countryFail = ref('')
function openCountry() {
  countryPick.value = country.value || (navigator.language.split('-')[1] ?? '').toUpperCase()
  countryFail.value = ''
  countryOpen.value = true
}
const countryOptions = computed(() =>
  COUNTRIES.map((code) => ({ code, name: countryName(code, locale.value) })).sort((a, b) =>
    a.name.localeCompare(b.name, locale.value),
  ),
)
async function stageCountry() {
  if (!countryPick.value) return
  try {
    await api.stageWiFiCountry(countryPick.value)
    countryOpen.value = false
    await refreshStaged()
  } catch (e) {
    countryFail.value =
      e instanceof ApiError && e.status === 409 ? t('wifi.refusedBusy') : String(e)
  }
}
</script>

<template>
  <section class="vb-wifi">
    <header class="vb-wifi__head">
      <h1>{{ t('wifi.title') }}</h1>
      <el-tag v-if="draftCount" type="warning" size="small" effect="light">
        {{ t('lan.draftPending', { n: draftCount }, draftCount) }}
      </el-tag>
      <el-tag v-if="stale" size="small" type="info">{{ t('shell.dataFrom', { time: dataFrom }) }}</el-tag>
      <span class="vb-wifi__spacer" />
      <span v-if="hereWords" class="vb-wifi__here">
        <VbIcon :name="here?.kind === 'cable' ? 'cable' : 'wifi'" size="sm" />
        {{ hereWords }}
      </span>
    </header>

    <el-card v-if="!loaded" shadow="never"><el-skeleton :rows="4" animated /></el-card>
    <el-card v-else-if="unsupported" shadow="never">
      <el-empty :description="t('wifi.unsupported')" />
    </el-card>
    <el-card v-else-if="missing" shadow="never">
      <el-empty :description="t('wifi.none')" />
    </el-card>
    <el-alert v-else-if="loadError" type="error" :closable="false" :title="t('wifi.readError')">
      <el-button size="small" @click="load">{{ t('wifi.retry') }}</el-button>
    </el-alert>

    <template v-else>
      <el-alert
        v-if="dropped"
        type="info"
        show-icon
        :title="t('wifi.dropped', { ssid: dropped.ssid, password: dropped.password })"
        @close="dropped = null"
      />

      <!-- After a password change: who came back. Seen by whoever is not on
           that Wi-Fi (a cable, the internet side); the number is live. -->
      <el-card v-if="after && afterNet" shadow="never" class="vb-wifi__after">
        <div class="vb-wifi__cardhead">
          <strong>{{ t('wifi.afterTitle', { time: after.at }) }}</strong>
          <el-tag size="small" type="info">{{ t('wifi.afterNoRevert') }}</el-tag>
          <span class="vb-wifi__spacer" />
          <el-button size="small" text @click="after = null">{{ t('wifi.close') }}</el-button>
        </div>
        <p class="vb-wifi__back">
          <span class="vb-mono vb-wifi__big">{{ t('wifi.backOf', { n: afterNet.devices, of: after.before }) }}</span>
          {{ t('wifi.backWord') }}
        </p>
        <el-progress
          v-if="after.before"
          :percentage="Math.min(100, Math.round((afterNet.devices / after.before) * 100))"
          :show-text="false"
        />
        <p class="vb-wifi__hint">{{ t('wifi.afterHint') }}</p>
      </el-card>

      <!-- 1. The networks: name, security, password on request, QR. -->
      <el-card v-for="n in networks" :key="n.id" shadow="never">
        <div class="vb-wifi__cardhead">
          <strong>{{ n.kind === 'main' ? t('wifi.mainNet') : t('wifi.otherNet') }}</strong>
          <el-tag size="small" type="info">{{ netBands(n) }}</el-tag>
        </div>
        <div class="vb-wifi__net">
          <div class="vb-wifi__netbody">
            <dl class="vb-facts">
              <div class="vb-facts__i">
                <dt>{{ t('wifi.ssid') }}</dt>
                <dd>{{ n.ssid }}</dd>
              </div>
              <div class="vb-facts__i">
                <dt>{{ t('wifi.security') }}</dt>
                <dd>{{ securityWord(t, n.security) }}</dd>
              </div>
              <div class="vb-facts__i">
                <dt>{{ t('wifi.password') }}</dt>
                <dd class="vb-wifi__pass">
                  <template v-if="!n.hasPassword">{{ t('wifi.noPassword') }}</template>
                  <template v-else-if="shown[n.id] !== undefined">
                    <span class="vb-mono">{{ shown[n.id] }}</span>
                    <el-button size="small" text @click="copy(shown[n.id] ?? '', n.id)">
                      {{ copied === n.id ? t('wifi.copied') : t('wifi.copy') }}
                    </el-button>
                    <el-button size="small" text @click="hidePassword(n)">{{ t('wifi.hide') }}</el-button>
                  </template>
                  <template v-else>
                    <span class="vb-mono">••••••••••</span>
                    <el-button size="small" @click="showPassword(n)">{{ t('wifi.show') }}</el-button>
                  </template>
                  <span v-if="passErr[n.id]" class="vb-wifi__err">{{ t('wifi.passFailed') }}</span>
                </dd>
              </div>
              <div class="vb-facts__i">
                <dt>{{ t('wifi.connected') }}</dt>
                <dd>
                  <el-link type="primary" @click="router.push('/devices')">
                    {{ t('wifi.devicesLink', { count: devicesWord(n.devices) }) }}
                  </el-link>
                </dd>
              </div>
            </dl>
            <div class="vb-wifi__acts">
              <el-button :disabled="busy" @click="openAccess(n)">
                <VbIcon name="edit" size="sm" class="vb-wifi__btnico" />{{ t('wifi.changeAccess') }}
              </el-button>
              <el-button v-if="n.hasPassword" class="vb-wifi__qrbtn" @click="openQR(n)">{{ t('wifi.qr') }}</el-button>
            </div>
          </div>
          <button v-if="n.hasPassword" type="button" class="vb-wifi__qrclosed" @click="openQR(n)">
            <VbIcon name="tabs" size="lg" />
            <span>{{ t('wifi.showQR') }}</span>
            <small>{{ t('wifi.qrHasPassword') }}</small>
          </button>
        </div>
      </el-card>

      <!-- 2. The radios: how the network sounds on the air. -->
      <div class="vb-wifi__section">
        <strong>{{ t('wifi.radio') }}</strong>
        <span class="vb-wifi__muted">{{ t('wifi.radioHint') }}</span>
      </div>
      <el-alert v-if="!country && hasFiveGHz" type="warning" :closable="false" show-icon :title="t('wifi.noCountry')">
        <el-button size="small" :disabled="busy" @click="openCountry">{{ t('wifi.pickCountry') }}</el-button>
      </el-alert>
      <div class="vb-wifi__radios">
        <el-card v-for="r in radios" :key="r.id" shadow="never">
          <div class="vb-wifi__cardhead">
            <el-switch :model-value="r.enabled" disabled size="small" />
            <strong>{{ radioName(r) }}</strong>
            <el-tag v-if="!narrow" size="small" :type="stateTag(r).type">{{ stateTag(r).word }}</el-tag>
            <el-tag v-if="hereOn(r.id)" size="small">{{ t('wifi.youHere') }}</el-tag>
            <span class="vb-wifi__spacer" />
            <el-button v-if="!narrow" size="small" :disabled="busy" @click="openRadio(r)">
              <VbIcon name="edit" size="sm" class="vb-wifi__btnico" />{{ t('wifi.edit') }}
            </el-button>
          </div>
          <template v-if="narrow">
            <p class="vb-wifi__oneline">
              <span class="vb-wifi__nw">{{ channelWords(r) }}</span>
              <template v-if="r.enabled">
                · <span class="vb-wifi__nw">{{ t('wifi.mhz', { n: r.width }) }}</span> ·
                <span class="vb-wifi__nw">{{ devicesWord(r.devices) }}</span>
              </template>
            </p>
            <el-button size="small" :disabled="busy" @click="openRadio(r)">
              <VbIcon name="edit" size="sm" class="vb-wifi__btnico" />{{ t('wifi.edit') }}
            </el-button>
          </template>
          <dl v-else-if="r.enabled" class="vb-facts">
            <div class="vb-facts__i">
              <dt>{{ t('wifi.channel') }}</dt>
              <dd>{{ channelWords(r) }}</dd>
            </div>
            <div class="vb-facts__i">
              <dt>{{ t('wifi.width') }}</dt>
              <dd>{{ t('wifi.mhz', { n: r.width }) }}</dd>
            </div>
            <div class="vb-facts__i">
              <dt>{{ t('wifi.connectedShort') }}</dt>
              <dd>{{ r.devices }}</dd>
            </div>
          </dl>
        </el-card>
      </div>
      <p v-if="country" class="vb-wifi__country">
        <VbIcon name="globe" size="sm" />
        <span>
          {{ t('wifi.countryIs') }} <strong>{{ countryName(country, locale) }}</strong> — {{ t('wifi.countryWhy') }}
          <el-link type="primary" :disabled="busy" @click="openCountry">{{ t('wifi.edit') }}</el-link>
        </span>
      </p>
    </template>

    <!-- QR: made in the browser, the password never leaves the page. -->
    <el-dialog v-model="qrOpen" :title="t('wifi.qrTitle', { ssid: qr.ssid })" width="min(420px, 94vw)" append-to-body>
      <div class="vb-wifi__qrbox vb-wifi-print">
        <strong class="vb-wifi__printonly">{{ t('wifi.printTitle') }}</strong>
        <svg v-if="qr.pic" class="vb-wifi__qrsvg" :viewBox="`-2 -2 ${qr.pic.size + 4} ${qr.pic.size + 4}`" role="img" :aria-label="t('wifi.qr')">
          <path :d="qr.pic.path" fill="#000" />
        </svg>
        <p class="vb-wifi__muted">{{ t('wifi.qrHint') }}</p>
        <dl class="vb-facts">
          <div class="vb-facts__i">
            <dt>{{ t('wifi.ssid') }}</dt>
            <dd>{{ qr.ssid }}</dd>
          </div>
          <div class="vb-facts__i">
            <dt>{{ t('wifi.password') }}</dt>
            <dd class="vb-mono">{{ qr.password }}</dd>
          </div>
        </dl>
      </div>
      <template #footer>
        <el-button @click="printQR">{{ t('wifi.print') }}</el-button>
        <el-button type="primary" @click="qrOpen = false">{{ t('wifi.close') }}</el-button>
      </template>
    </el-dialog>

    <!-- Name and password: a form, then the consequences. -->
    <el-dialog
      v-model="accessOpen"
      :title="accessStep === 'form' ? t('wifi.accessTitle') : t('wifi.confirmTitle')"
      width="min(560px, 96vw)"
      append-to-body
    >
      <template v-if="accessStep === 'form'">
        <el-alert v-if="draftCount" type="warning" :closable="false" show-icon :title="t('wifi.draftFirst', { n: draftCount })" />
        <el-form label-position="top" @submit.prevent>
          <el-form-item :label="t('wifi.ssid')" :error="formErr.ssid">
            <el-input v-model="form.ssid" />
            <span class="vb-wifi__hint">{{ t('wifi.ssidHint', { n: ssidLen }) }}</span>
          </el-form-item>
          <el-form-item :label="t('wifi.newPassword')" :error="formErr.password">
            <div class="vb-wifi__row">
              <el-input v-model="form.password" class="vb-mono" :placeholder="t('wifi.keepPassword')" />
              <el-button @click="form.password = generatePassword()">{{ t('wifi.generate') }}</el-button>
            </div>
            <span class="vb-wifi__hint">{{ t('wifi.passHint') }}</span>
          </el-form-item>
          <el-form-item :label="t('wifi.security')" :error="formErr.security">
            <el-radio-group v-model="form.security">
              <el-radio-button value="wpa2-wpa3">{{ t('wifi.secMixed') }}</el-radio-button>
              <el-radio-button value="wpa2">{{ t('wifi.secOld') }}</el-radio-button>
            </el-radio-group>
            <span class="vb-wifi__hint">{{ t('wifi.secHint') }}</span>
          </el-form-item>
        </el-form>
      </template>
      <template v-else>
        <div class="vb-wifi__newpass">
          <svg v-if="confirmPic" class="vb-wifi__qrsvg vb-wifi__qrsvg--sm" :viewBox="`-2 -2 ${confirmPic.size + 4} ${confirmPic.size + 4}`" role="img" :aria-label="t('wifi.qr')">
            <path :d="confirmPic.path" fill="#000" />
          </svg>
          <dl class="vb-facts">
            <div class="vb-facts__i">
              <dt>{{ t('wifi.ssid') }}</dt>
              <dd>{{ form.ssid }}</dd>
            </div>
            <div class="vb-facts__i">
              <dt>{{ form.password ? t('wifi.newPassword') : t('wifi.password') }}</dt>
              <dd class="vb-mono">
                {{ confirmPassword }}
                <el-button size="small" text @click="copy(confirmPassword, 'new')">
                  {{ copied === 'new' ? t('wifi.copied') : t('wifi.copy') }}
                </el-button>
              </dd>
            </div>
          </dl>
        </div>
        <el-alert
          v-if="accessWho === 'self'"
          type="error"
          :closable="false"
          show-icon
          :title="
            (accessNet?.devices ?? 0) > 1
              ? t('wifi.whoSelf', { count: devicesWord(accessNet?.devices ?? 0), band: band(here?.band ?? '') })
              : t('wifi.whoSelfOne', { band: band(here?.band ?? '') })
          "
        />
        <el-alert
          v-else-if="(accessNet?.devices ?? 0) > 0"
          type="warning"
          :closable="false"
          show-icon
          :title="
            (accessNet?.devices ?? 0) > 1
              ? t('wifi.whoAll', { count: devicesWord(accessNet?.devices ?? 0) })
              : t('wifi.whoAllOne')
          "
        />
        <el-alert v-else type="info" :closable="false" show-icon :title="t('wifi.whoNobody')" />
        <el-alert v-if="accessWho === 'cable'" type="info" :closable="false" :title="t('wifi.whoCable')" />
        <el-alert type="info" :closable="false" :title="t('wifi.noUndo')" />
        <el-alert v-if="accessFail" type="error" :closable="false" :title="accessFail" />
      </template>
      <template #footer>
        <template v-if="accessStep === 'form'">
          <el-button @click="accessOpen = false">{{ t('wifi.cancel') }}</el-button>
          <el-button type="primary" :disabled="!!draftCount || busy || !accessChanged" @click="toConfirm">
            {{ t('wifi.next') }}
          </el-button>
        </template>
        <template v-else>
          <el-button @click="accessStep = 'form'">{{ t('wifi.back') }}</el-button>
          <el-button
            :type="(accessNet?.devices ?? 0) > 0 ? 'danger' : 'primary'"
            :loading="applying"
            @click="applyAccess"
          >
            {{ (accessNet?.devices ?? 0) > 0 ? t('wifi.applyDisconnect') : t('wifi.apply') }}
          </el-button>
        </template>
      </template>
    </el-dialog>

    <!-- A radio: drafted, applied through the bar with its window. -->
    <el-dialog
      v-model="radioOpen"
      :title="radioEdit.r ? radioName(radioEdit.r) : ''"
      width="min(520px, 96vw)"
      append-to-body
    >
      <el-form v-if="radioEdit.r" label-position="top" @submit.prevent>
        <el-form-item>
          <el-switch v-model="radioEdit.enabled" :active-text="t('wifi.bandOn')" />
        </el-form-item>
        <template v-if="radioEdit.enabled">
          <el-form-item :label="t('wifi.channel')" :error="radioErr.channel">
            <el-select v-model="radioEdit.channel">
              <el-option value="auto" :label="t('wifi.autoRecommended')" />
              <el-option
                v-for="c in radioEdit.r.channels ?? []"
                :key="c.channel"
                :value="String(c.channel)"
                :label="c.radar ? t('wifi.chanRadar', { n: c.channel }) : String(c.channel)"
              />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('wifi.width')" :error="radioErr.width">
            <el-radio-group v-model="radioEdit.width" :size="narrow ? 'small' : 'default'">
              <el-radio-button v-for="w in radioEdit.r.widths ?? []" :key="w" :value="w">
                {{ t('wifi.mhz', { n: w }) }}
              </el-radio-button>
            </el-radio-group>
            <span class="vb-wifi__hint">
              {{ radioEdit.r.band === '2.4' ? t('wifi.widthHint24') : t('wifi.widthHint') }}
            </span>
          </el-form-item>
          <el-alert v-if="radarChosen" type="warning" :closable="false" show-icon :title="t('wifi.radarHint')" />
        </template>
        <el-alert v-if="lastOff && here?.kind === 'wifi'" type="error" :closable="false" show-icon :title="t('wifi.lastOffSelf')" />
        <el-alert
          v-else-if="hereOn(radioEdit.r.id)"
          type="warning"
          :closable="false"
          show-icon
          :title="t('wifi.radioSelf', { band: band(radioEdit.r.band) })"
        />
        <el-alert
          v-else-if="radioEdit.r.devices > 0"
          type="info"
          :closable="false"
          show-icon
          :title="t('wifi.radioOthers', { band: band(radioEdit.r.band) })"
        />
        <el-alert v-if="radioFail" type="error" :closable="false" :title="radioFail" />
      </el-form>
      <template #footer>
        <el-button @click="radioOpen = false">{{ t('wifi.cancel') }}</el-button>
        <el-button type="primary" :disabled="busy" @click="stageRadio">{{ t('wifi.toDraft') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="countryOpen" :title="t('wifi.countryTitle')" width="min(460px, 96vw)" append-to-body>
      <el-form label-position="top" @submit.prevent>
        <el-form-item :label="t('wifi.country')">
          <el-select v-model="countryPick" filterable>
            <el-option v-for="c in countryOptions" :key="c.code" :value="c.code" :label="c.name" />
          </el-select>
          <span class="vb-wifi__hint">{{ t('wifi.countryHint') }}</span>
        </el-form-item>
      </el-form>
      <el-alert type="info" :closable="false" :title="t('wifi.countryRestart')" />
      <el-alert v-if="countryFail" type="error" :closable="false" :title="countryFail" />
      <template #footer>
        <el-button @click="countryOpen = false">{{ t('wifi.cancel') }}</el-button>
        <el-button type="primary" :disabled="!countryPick || busy" @click="stageCountry">{{ t('wifi.toDraft') }}</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped>
.vb-wifi {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.vb-wifi__head,
.vb-wifi__cardhead,
.vb-wifi__acts,
.vb-wifi__row,
.vb-wifi__pass {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-wifi__head {
  gap: 12px;
}
.vb-wifi__head h1 {
  margin: 0;
  font-size: 22px;
}
.vb-wifi__cardhead {
  margin-bottom: 12px;
}
.vb-wifi__spacer {
  flex: 1 1 auto;
}
.vb-wifi__here {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  padding: 4px 10px;
  border-radius: 999px;
  background: var(--el-fill-color-light);
  border: 1px solid var(--el-border-color-lighter);
}
.vb-wifi__net {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 18px;
  align-items: start;
}
.vb-wifi__netbody {
  display: flex;
  flex-direction: column;
  gap: 14px;
  min-width: 0;
}
.vb-wifi__qrclosed {
  width: 132px;
  height: 132px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border: 1px dashed var(--el-border-color);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
  color: var(--el-color-primary);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}
.vb-wifi__qrclosed small {
  color: var(--vb-muted);
  font-size: 11px;
}
.vb-wifi__qrbtn {
  display: none;
}
.vb-wifi__section {
  display: flex;
  gap: 10px;
  align-items: baseline;
  flex-wrap: wrap;
}
.vb-wifi__radios {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 14px;
}
.vb-wifi__country {
  display: flex;
  gap: 8px;
  align-items: baseline;
  margin: 0;
  font-size: 13px;
  color: var(--vb-muted);
}
.vb-wifi__muted {
  color: var(--vb-muted);
  font-size: 13px;
}
.vb-wifi__hint {
  display: block;
  width: 100%;
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
.vb-wifi__err {
  color: var(--el-color-danger);
  font-size: 12px;
}
.vb-wifi__acts .el-button + .el-button {
  margin-left: 0;
}
.vb-wifi__btnico {
  margin-right: 6px;
}
.vb-wifi__oneline {
  margin: 0 0 10px;
  font-size: 14px;
}
.vb-wifi__nw {
  white-space: nowrap;
}
.vb-wifi__back {
  display: flex;
  gap: 10px;
  align-items: baseline;
  flex-wrap: wrap;
  margin: 0 0 8px;
}
.vb-wifi__big {
  font-size: 24px;
}
.vb-wifi__qrbox {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  text-align: center;
}
.vb-wifi__qrsvg {
  width: 220px;
  height: 220px;
  background: #fff;
  border-radius: 8px;
  display: block;
  shape-rendering: crispEdges;
}
.vb-wifi__qrsvg--sm {
  width: 140px;
  height: 140px;
  flex: 0 0 auto;
}
.vb-wifi__newpass {
  display: flex;
  gap: 18px;
  align-items: center;
  flex-wrap: wrap;
  padding: 14px;
  margin-bottom: 12px;
  border-radius: 10px;
  background: var(--el-fill-color-light);
}
.vb-wifi__printonly {
  display: none;
  font-size: 20px;
}
:deep(.el-alert) + :deep(.el-alert),
.vb-wifi :deep(.el-dialog__body .el-alert) {
  margin-top: 10px;
}
@media (max-width: 560px) {
  .vb-wifi__net {
    grid-template-columns: minmax(0, 1fr);
  }
  .vb-wifi__qrclosed {
    display: none;
  }
  .vb-wifi__qrbtn {
    display: inline-flex;
  }
}
</style>

<style>
/* "Print" shows only the sheet with the code, name and password. */
@media print {
  body.vb-printing-qr * {
    visibility: hidden;
  }
  body.vb-printing-qr .vb-wifi-print,
  body.vb-printing-qr .vb-wifi-print * {
    visibility: visible;
  }
  body.vb-printing-qr .vb-wifi-print {
    position: fixed;
    inset: 0;
    justify-content: center;
  }
  body.vb-printing-qr .vb-wifi__printonly {
    display: block;
  }
}
</style>
