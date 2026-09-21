// The live layer (D-12, M2.2): one subscription for the whole panel.
//
// Shape: a REST snapshot on first use, then deltas from GET /events. Every
// component reads the same refs, so ten tiles cost one stream — the reason
// this is a module-level singleton and not a per-component composable.
//
// The device drops this stream routinely (a reboot, a change that cuts the
// link), so "disconnected" is a normal state with a visible answer — the
// last known values plus the time they were taken — rather than an error
// screen.
import { computed, readonly, ref } from 'vue'
import {
  type ApplyState,
  api,
  type Capabilities,
  type ConfigChange,
  isAuthed,
  type MetricSample,
  openEvents,
  type SystemInfo,
} from '@/api/client'

const system = ref<SystemInfo | null>(null)
const applyState = ref<ApplyState | null>(null)
// staged is the draft the DEVICE holds, never anything this tab remembers: a
// draft outlives the page and the daemon, so a reload, a second tab and a
// second operator all have to see the same pending change.
const staged = ref<ConfigChange[]>([])
const capabilities = ref<Capabilities>({})
const liveSamples = ref<MetricSample[]>([])
const connected = ref(false)
const lastUpdate = ref<Date | null>(null)
// deviceError is what the daemon reports when the device stops answering it:
// the panel is up, the router is not, and that difference has to be visible.
const deviceError = ref<string | null>(null)

let stop: (() => void) | null = null
let started = false

/** liveSamples is capped at the server's own live window (3 min at 3 s). */
const MAX_SAMPLES = 60

/** STALE_AFTER_MS is how long without a frame means "the device is not
 * answering".
 *
 * Reachability cannot be read off the stream alone, and this was measured on
 * a live device: when a change moves the uplink to another address, the TCP
 * connection carrying the event stream does not fail \u2014 nothing sends a reset,
 * so the browser keeps it "open" for minutes. The panel looked perfectly
 * healthy while the device was gone. Silence is the signal instead: the
 * daemon pushes every ~3 s, so three missed frames is an answer. */
const STALE_AFTER_MS = 10_000

// A clock for the freshness check to depend on. Without it the check would
// only be recomputed when a frame arrives \u2014 which is exactly what stops
// happening in the case it exists to catch.
const nowTs = ref(Date.now())
let clock = 0

function sinceLastFrame(): number {
  if (!lastUpdate.value) return Number.POSITIVE_INFINITY
  return nowTs.value - lastUpdate.value.getTime()
}

function pushSample(s: MetricSample | undefined) {
  if (!s?.at) return
  const last = liveSamples.value[liveSamples.value.length - 1]
  if (last && last.at === s.at) return
  liveSamples.value = [...liveSamples.value, s].slice(-MAX_SAMPLES)
}

/** start subscribes once per session. Calling it again is a no-op. */
export function startLive() {
  if (started || !isAuthed()) return
  started = true
  clock = window.setInterval(() => {
    nowTs.value = Date.now()
  }, 1000)

  // The first picture comes over REST: a stream that has not spoken yet is
  // indistinguishable from one that failed to connect, and capabilities are
  // needed before the menu can render at all.
  void (async () => {
    await refreshStaged()
    try {
      capabilities.value = await api.capabilities()
    } catch {
      // Menu falls back to showing everything; a capability we could not read
      // is not a capability we may silently hide.
    }
    try {
      const m = await api.metrics()
      liveSamples.value = m.live.slice(-MAX_SAMPLES)
    } catch {
      // No history yet is a normal state on a daemon that just started.
    }
  })()

  stop = openEvents(
    (e) => {
      lastUpdate.value = new Date()
      switch (e.topic) {
        case 'system': {
          const payload = e.data as { system: SystemInfo; sample?: MetricSample }
          system.value = payload.system
          pushSample(payload.sample)
          deviceError.value = null
          break
        }
        case 'apply':
          applyState.value = (e.data as { state: ApplyState }).state
          break
        case 'error':
          deviceError.value = (e.data as { detail?: string }).detail ?? 'unknown'
          break
      }
    },
    (ok, err) => {
      connected.value = ok
      if (!ok && err === 'unauthorized') stopLive()
    },
  )
}

export function stopLive() {
  stop?.()
  stop = null
  window.clearInterval(clock)
  clock = 0
  started = false
  connected.value = false
  system.value = null
  applyState.value = null
  staged.value = []
  liveSamples.value = []
  lastUpdate.value = null
  deviceError.value = null
}

/** refreshApply pulls the transaction state after an action, without waiting
 * for the next pushed frame. */
export async function refreshApply() {
  try {
    applyState.value = await api.applyState()
  } catch {
    // Unreachable devices are reported by the stream, not by this helper.
  }
  await refreshStaged()
}

/** refreshStaged re-reads the draft from the device: after staging, after
 * discarding, after applying, and on mount. Both paths that can answer this
 * question describe the draft in the same words (M3.1a), so it does not
 * matter which one the operator's reload happened to take. */
export async function refreshStaged() {
  try {
    const out = await api.stagedChanges()
    staged.value = out.changes ?? []
  } catch {
    // Keep the last known list. An unreachable device is announced by the
    // stream; blanking the draft here would suggest it went away, and it did
    // not — it is on the device.
  }
}

export function useLive() {
  return {
    system: readonly(system),
    applyState: readonly(applyState),
    staged: readonly(staged),
    capabilities: readonly(capabilities),
    samples: readonly(liveSamples),
    connected: readonly(connected),
    lastUpdate: readonly(lastUpdate),
    deviceError: readonly(deviceError),
    /** reachable is true while frames keep arriving. Deliberately not just
     * `connected`: a dead link leaves the stream's socket open (see
     * STALE_AFTER_MS). */
    reachable: computed(() => connected.value && sinceLastFrame() < STALE_AFTER_MS),
    /** stale is true once we are showing numbers nobody refreshed. */
    stale: computed(
      () => system.value !== null && (!connected.value || sinceLastFrame() >= STALE_AFTER_MS),
    ),
    /** can reports a capability, defaulting to true for anything the device
     * did not mention: hiding a section because a probe failed would be this
     * code's limitation dressed up as a fact about the hardware (D-20). */
    can: (name: string) => capabilities.value[name]?.available ?? true,
    capability: (name: string) => capabilities.value[name],
  }
}
