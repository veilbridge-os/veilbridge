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
import { ApiError, api } from '@/api/client'
import { refreshApply, useLive } from '@/stores/live'

const { t } = useI18n()
const { applyState, connected } = useLive()

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

const countdown = computed(() => {
  const s = secondsLeft.value
  return `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
})

// The window is read from the deadline the server handed out, never assumed:
// the panel must not claim "2 minutes" while the daemon counts 90 seconds.
const progress = computed(() => {
  const s = secondsLeft.value
  return Math.min(100, Math.max(0, (s / Math.max(s, 1)) * 100))
})

const visible = computed(
  () => applying.value || ['awaiting_confirm', 'reverted', 'revert_failed'].includes(phase.value),
)

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
    <!-- 1. Applying, and the panel has not heard back. Normal, not an error. -->
    <template v-if="applying">
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
.vb-applybar__body {
  flex: 1 1 320px;
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
  .vb-applybar__actions .el-button {
    width: 100%;
    margin: 0 0 8px;
  }
}
</style>
