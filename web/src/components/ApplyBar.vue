<script setup lang="ts">
// The apply bar (M2.3) — the user-facing half of the transaction that M1's
// risk gate proved on real hardware.
//
// Its hardest state is the one that looks like a failure and is not: a change
// dangerous enough to need confirmation is usually a change that cuts the very
// connection carrying the reply. The reply to POST /apply is therefore
// routinely never received, and the bar must say "waiting for the panel to
// come back", not "error".
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ApiError, api, type ConfigChange } from '@/api/client'
import { refreshApply, refreshStaged, useLive } from '@/stores/live'

const { t, te } = useI18n()
const { applyState, connected, staged } = useLive()

/** labelOf translates a staged change into the language of the interface.
 *
 * The device already names every change in words (M3.1a) \u2014 but in English,
 * because a daemon has no locale, and this panel ships in 13 languages. So the
 * words from the device are the fallback, and the translation is looked up by
 * the stable part of the technical key: `network.wan.proto` \u2192 `diff.network.proto`.
 *
 * Debt, recorded rather than hidden: the API should carry a key of its own
 * instead of making the panel derive one from `detail`. */
function labelOf(c: ConfigChange): string {
  const parts = (c.detail ?? '').split('.')
  if (parts.length < 2) return c.label
  const key = parts.length >= 3 ? `diff.${parts[0]}.${parts.at(-1)}` : `diff.${parts[0]}.section`
  return te(key) ? t(key) : c.label
}

/** valueOf does for a value what labelOf does for a name. The device stores a
 * connection type as `dhcp`, and a row reading \u201cConnection type: dhcp \u2192 static\u201d
 * puts the operating system back on screen through the side door (D-3). Only
 * known enumerations are translated; an address stays exactly as measured. */
function shownValue(c: ConfigChange, value: string): string {
  if (!value) return ''
  const option = (c.detail ?? '').split('.').at(-1)
  if (option === 'proto') {
    const key = `wan.proto${value.charAt(0).toUpperCase()}${value.slice(1)}`
    return te(key) ? t(key) : value
  }
  // A flag the panel words as a question: \u201cuse the provider\u2019s resolvers\u201d.
  if (option === 'peerdns') return value === '0' ? t('apply.flagOff') : t('apply.flagOn')
  return value
}

// applying is true between pressing Apply and learning any outcome. It is a
// local flag because no server state can describe it: the server may already
// have applied the change and simply be unable to tell us.
const applying = ref(false)
const confirming = ref(false)
const confirmFailed = ref(false)
const now = ref(Date.now())

const timer = window.setInterval(() => {
  now.value = Date.now()
}, 250)
onUnmounted(() => window.clearInterval(timer))

const phase = computed(() => applyState.value?.phase ?? 'idle')

// Once the daemon answers again, the local "applying" flag must give way to
// whatever actually happened.
watch(
  () => [phase.value, connected.value] as const,
  ([p, ok]) => {
    if (applying.value && (p !== 'idle' || ok)) applying.value = false
    if (p === 'confirmed' || p === 'idle') confirmFailed.value = false
  },
)

const secondsLeft = computed(() => {
  const dl = applyState.value?.deadline
  if (!dl) return 0
  return Math.max(0, Math.round((new Date(dl).getTime() - now.value) / 1000))
})

// The length of the window is not in the API: only the deadline is. Measuring
// it as the largest remaining time seen for THIS transaction keeps the bar
// honest — it never claims a window the daemon did not grant, and it never
// hardcodes 90 seconds either (D-14).
const windowSec = ref(0)
watch(
  () => [applyState.value?.snapshot_id, secondsLeft.value] as const,
  ([id, left], prev) => {
    if (prev && id !== prev[0]) windowSec.value = 0
    if (left > windowSec.value) windowSec.value = left
  },
  { immediate: true },
)

const countdown = computed(() => {
  const s = secondsLeft.value
  return `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
})

const progress = computed(() => {
  const total = windowSec.value
  if (total <= 0) return 0
  return Math.min(100, Math.max(0, (secondsLeft.value / total) * 100))
})

// The draft is the bar's sixth state and the only one nobody is waiting on:
// the device holds edits that are not live yet. It is read from the device,
// so it survives a reload and shows up in a second tab as well.
const draft = computed(() => (phase.value === 'idle' ? staged.value : []))
const dangerous = computed(() => draft.value.some((c) => c.dangerous))
const showTech = ref(false)
const applyError = ref('')

const visible = computed(
  () =>
    applying.value ||
    draft.value.length > 0 ||
    ['awaiting_confirm', 'reverted', 'revert_failed'].includes(phase.value),
)

/** applyDraft commits the draft. A failure here means "unknown", not "it did
 * not happen": the change under test can cut the link carrying the reply, and
 * that is the expected case rather than the exception. */
async function applyDraft() {
  applyError.value = ''
  applying.value = true
  try {
    await api.applyConfig()
  } catch (e) {
    // 409 is the one answer that really means "nothing was started".
    if (e instanceof ApiError && e.status === 409) {
      applying.value = false
      applyError.value = e.message
    }
  }
  await refreshApply()
}

async function discardDraft() {
  applyError.value = ''
  try {
    await api.discardStaged()
  } catch (e) {
    applyError.value = e instanceof Error ? e.message : String(e)
  }
  await refreshStaged()
}

async function confirm() {
  const token = applyState.value?.token
  if (!token) return
  confirming.value = true
  confirmFailed.value = false
  try {
    await api.confirmApply(token)
    await refreshApply()
  } catch (e) {
    // A confirmation that did not arrive is not the same as a rejected one:
    // the watchdog is still running, and doing nothing is safe.
    confirmFailed.value = !(e instanceof ApiError && e.status === 409)
    await refreshApply()
  } finally {
    confirming.value = false
  }
}

async function revertNow() {
  try {
    await api.revertApply()
  } catch {
    // Same reasoning as above: the device may already be undoing the change.
  }
  await refreshApply()
}

function dismiss() {
  void refreshApply()
}
</script>

<template>
  <div
    v-if="visible"
    class="vb-applybar"
    :class="`vb-applybar--${applying ? 'applying' : phase}`"
    role="region"
    :aria-label="t('apply.applyNow')"
  >
    <!-- 0. A draft sits on the device and nothing is live yet. This is where
         the operator reads what they are about to do, so it speaks the
         panel's words and keeps configuration keys under a disclosure. -->
    <template v-if="!applying && phase === 'idle' && draft.length">
      <div class="vb-applybar__body">
        <div class="vb-applybar__head">
          <strong>{{ t('apply.willChange') }}</strong>
          <el-tag v-if="dangerous" type="warning" size="small" effect="light">
            {{ t('apply.mayCutAccess') }}
          </el-tag>
        </div>
        <dl class="vb-applybar__diff">
          <template v-for="c in draft" :key="c.detail || c.label">
            <dt>{{ labelOf(c) }}</dt>
            <dd>
              <s v-if="c.from">{{ shownValue(c, c.from) }}</s>
              <span v-else class="vb-applybar__unset">{{ t('apply.wasUnset') }}</span>
              <strong>{{ shownValue(c, c.to) || t('apply.nowNothing') }}</strong>
            </dd>
          </template>
        </dl>
        <el-button link size="small" @click="showTech = !showTech">
          {{ showTech ? t('apply.hideTechnical') : t('apply.showTechnical') }}
        </el-button>
        <dl v-if="showTech" class="vb-applybar__tech">
          <template v-for="c in draft" :key="`k-${c.detail || c.label}`">
            <dt>{{ c.detail }}</dt>
            <dd>{{ c.from || '—' }} → {{ c.to || '—' }}</dd>
          </template>
        </dl>
        <p v-if="dangerous">{{ t('apply.autoRevertHint') }}</p>
        <p v-if="applyError" class="vb-applybar__err">{{ applyError }}</p>
      </div>
      <div class="vb-applybar__actions">
        <el-button type="primary" @click="applyDraft">{{ t('apply.applyNow') }}</el-button>
        <el-button @click="discardDraft">{{ t('apply.discard') }}</el-button>
      </div>
    </template>

    <!-- 1. Applying, and the panel has not heard back. Normal, not an error. -->
    <template v-else-if="applying">
      <div class="vb-applybar__body">
        <strong>{{ t('apply.applying') }}</strong>
        <p>{{ t('apply.applyingHint') }}</p>
      </div>
    </template>

    <!-- 2. The watchdog is ticking and a human has to say the panel survived. -->
    <template v-else-if="phase === 'awaiting_confirm'">
      <div class="vb-applybar__timer">
        <span class="vb-applybar__clock">{{ countdown }}</span>
        <span class="vb-applybar__unit">{{ t('apply.remaining') }}</span>
        <el-progress
          :percentage="progress"
          :show-text="false"
          :stroke-width="3"
          class="vb-applybar__progress"
        />
      </div>
      <div class="vb-applybar__body" role="status" aria-live="assertive">
        <strong v-if="!confirmFailed">{{ t('apply.waiting') }}</strong>
        <strong v-else>{{ t('apply.confirmFailed') }}</strong>
        <p>{{ confirmFailed ? t('apply.confirmFailedHint') : t('apply.waitingHint') }}</p>
      </div>
      <div class="vb-applybar__actions">
        <el-button type="primary" :loading="confirming" @click="confirm">
          {{ confirmFailed ? t('apply.retryConfirm') : t('apply.confirm') }}
        </el-button>
        <el-button @click="revertNow">{{ t('apply.cancelNow') }}</el-button>
      </div>
    </template>

    <!-- 3. Nobody confirmed: the device undid the change by itself. -->
    <template v-else-if="phase === 'reverted'">
      <div class="vb-applybar__body" role="alert">
        <strong>{{ t('apply.reverted') }}</strong>
        <p>{{ t('apply.revertedHint') }}</p>
        <code v-if="applyState?.snapshot_id" class="vb-applybar__id">
          {{ t('apply.change', { id: applyState.snapshot_id }) }}
        </code>
      </div>
      <div class="vb-applybar__actions">
        <el-button @click="dismiss">{{ t('apply.close') }}</el-button>
      </div>
    </template>

    <!-- 4. The worst state, and the one that must not be hidden: the undo
         itself failed, so the answer is a cable, not a button. -->
    <template v-else-if="phase === 'revert_failed'">
      <div class="vb-applybar__body" role="alert">
        <strong>{{ t('apply.revertFailed') }}</strong>
        <p>{{ t('apply.revertFailedHint') }}</p>
        <code v-if="applyState?.error" class="vb-applybar__id">{{ applyState.error }}</code>
      </div>
    </template>
  </div>
</template>

<style scoped>
.vb-applybar {
  position: sticky;
  bottom: 0;
  z-index: 20;
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  align-items: center;
  padding: 14px 20px;
  border-top: 3px solid var(--el-color-warning);
  background: var(--el-bg-color);
  box-shadow: 0 -6px 18px rgb(0 0 0 / 8%);
}
.vb-applybar--reverted {
  border-top-color: var(--el-color-success);
}
.vb-applybar--revert_failed {
  border-top-color: var(--el-color-danger);
}
.vb-applybar--applying {
  border-top-color: var(--el-color-info);
}
.vb-applybar__timer {
  min-width: 190px;
}
.vb-applybar__clock {
  font-size: 30px;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}
.vb-applybar__unit {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
}
.vb-applybar__progress {
  margin-top: 6px;
}
.vb-applybar--idle {
  border-top-color: var(--el-color-primary);
}
.vb-applybar__body {
  flex: 1 1 320px;
}
.vb-applybar__head {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.vb-applybar__diff,
.vb-applybar__tech {
  display: grid;
  grid-template-columns: minmax(120px, max-content) 1fr;
  gap: 2px 16px;
  margin: 8px 0 4px;
}
.vb-applybar__diff dt,
.vb-applybar__tech dt {
  color: var(--el-text-color-secondary);
}
.vb-applybar__diff dd,
.vb-applybar__tech dd {
  margin: 0;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.vb-applybar__diff s,
.vb-applybar__unset {
  color: var(--el-text-color-secondary);
}
.vb-applybar__tech {
  font-family: var(--el-font-family-mono, monospace);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.vb-applybar__err {
  color: var(--el-color-danger);
}
/* The diff can be long; the countdown and the buttons may never be pushed off
   screen by it, so the list scrolls and the bar does not. */
@media (height <= 900px) {
  .vb-applybar__diff {
    max-height: 38vh;
    overflow: auto;
  }
}
.vb-applybar__body p {
  margin: 4px 0 0;
  color: var(--el-text-color-regular);
}
.vb-applybar__id {
  display: inline-block;
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.vb-applybar__actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
/* On a phone the bar stacks, but it never moves into a menu: a countdown to
   an automatic rollback is the one thing that may not be hidden. */
@media (width <= 600px) {
  .vb-applybar {
    flex-direction: column;
    align-items: stretch;
  }
  /* Two columns of six characters each is not a table, it is a puzzle: on a
     phone the name goes above the value, as in the accepted mockup. */
  .vb-applybar__diff,
  .vb-applybar__tech {
    grid-template-columns: 1fr;
    gap: 0;
  }
  .vb-applybar__diff dt,
  .vb-applybar__tech dt {
    margin-top: 8px;
  }
  .vb-applybar__actions .el-button {
    width: 100%;
    margin: 0 0 8px;
  }
}
</style>
