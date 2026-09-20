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
  isAuthed,
  type MetricSample,
  openEvents,
  type SystemInfo,
} from '@/api/client'

const system = ref<SystemInfo | null>(null)
const applyState = ref<ApplyState | null>(null)
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

  // The first picture comes over REST: a stream that has not spoken yet is
  // indistinguishable from one that failed to connect, and capabilities are
  // needed before the menu can render at all.
  void (async () => {
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
  started = false
  connected.value = false
  system.value = null
  applyState.value = null
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
}

export function useLive() {
  return {
    system: readonly(system),
    applyState: readonly(applyState),
    capabilities: readonly(capabilities),
    samples: readonly(liveSamples),
    connected: readonly(connected),
    lastUpdate: readonly(lastUpdate),
    deviceError: readonly(deviceError),
    /** stale is true once the stream is down and we are showing old numbers. */
    stale: computed(() => !connected.value && system.value !== null),
    /** can reports a capability, defaulting to true for anything the device
     * did not mention: hiding a section because a probe failed would be this
     * code's limitation dressed up as a fact about the hardware (D-20). */
    can: (name: string) => capabilities.value[name]?.available ?? true,
    capability: (name: string) => capabilities.value[name],
  }
}
