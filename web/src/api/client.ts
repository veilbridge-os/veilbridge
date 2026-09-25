// Thin typed client over the Router Core API. Types come from schema.ts, which
// is generated from api/openapi.yaml — so the frontend never drifts from the
// backend contract.
import { ref } from 'vue'
import type { components } from './schema'

export type Node = components['schemas']['Node']
export type NodeStatus = components['schemas']['NodeStatus']
export type NodeWithStatus = components['schemas']['NodeWithStatus']
export type RouteRule = components['schemas']['RouteRule']
export type SystemInfo = components['schemas']['SystemInfo']
export type PathProbe = components['schemas']['PathProbe']
export type Capability = components['schemas']['Capability']
// The capability set is a map keyed by capability name, not a fixed struct:
// the generator therefore has no named schema for it (D-17 — the UI branches
// on entries it finds, never on a hardcoded list).
export type Capabilities = Record<string, Capability>
export type ApplyState = components['schemas']['ApplyState']
export type NetworkInterface = components['schemas']['NetworkInterface']
export type WANStatus = components['schemas']['WANStatus']
export type WANConfig = components['schemas']['WANConfig']
export type LANStatus = components['schemas']['LANStatus']
export type LANConfig = components['schemas']['LANConfig']
export type HandoutConfig = components['schemas']['HandoutConfig']
export type ReservationConfig = components['schemas']['ReservationConfig']
export type AddressHandout = components['schemas']['AddressHandout']
export type AddressLease = components['schemas']['AddressLease']
export type ReservedAddress = components['schemas']['ReservedAddress']
export type ConfigChange = components['schemas']['ConfigChange']
export type MetricSample = components['schemas']['Sample']

/** What the device will change if the draft is applied, described in the
 * panel's own words rather than in configuration keys (M3.1a). */
export interface StagedChanges {
  changes: ConfigChange[]
  dangerous: boolean
}

const TOKEN_KEY = 'veilbridge.token'
const BASE = '/api/v1'

// Reactive mirror of the persisted token so Vue computed/watch (e.g. the nav
// bar's showNav) react to login/logout immediately, not just on reload —
// localStorage alone is not reactive.
const token = ref<string | null>(localStorage.getItem(TOKEN_KEY))

export function getToken(): string | null {
  return token.value
}
function setToken(t: string) {
  token.value = t
  localStorage.setItem(TOKEN_KEY, t)
}
export function clearToken() {
  token.value = null
  localStorage.removeItem(TOKEN_KEY)
}
export function isAuthed(): boolean {
  return !!token.value
}

export class ApiError extends Error {
  status: number
  /** Refusals the server tied to one field of the request body, keyed by the
   * field's JSON name (`address`, `netmask`, `dns`…). Screens place an error
   * by this, never by the wording of the sentence (#28). */
  fields: Record<string, string>
  constructor(status: number, message: string, fields: Record<string, string> = {}) {
    super(message)
    this.status = status
    this.fields = fields
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const token = getToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const resp = await fetch(BASE + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  if (resp.status === 401) {
    clearToken()
    throw new ApiError(401, 'Unauthorized')
  }
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`
    const fields: Record<string, string> = {}
    try {
      const err = await resp.json()
      // Huma puts validation reasons in `errors[]` and a short summary in
      // `detail`. Showing only `detail` tells the operator "stage uplink" and
      // hides the one sentence that says what was wrong with their input.
      // A reason with a `body.<field>` location is also kept by field, and
      // not repeated in the sentence when it already is the sentence.
      const detail: string = err?.detail || err?.title || msg
      const reasons: string[] = []
      for (const e of Array.isArray(err?.errors) ? err.errors : []) {
        const text: string | undefined = e?.message
        if (!text) continue
        const at = typeof e.location === 'string' ? e.location : ''
        if (at.startsWith('body.')) fields[at.slice(5)] ??= text
        if (text !== detail) reasons.push(text)
      }
      msg = [detail, ...reasons].join(': ')
    } catch {
      // non-JSON error body; keep the status message
    }
    throw new ApiError(resp.status, msg, fields)
  }
  if (resp.status === 204) return undefined as T
  return (await resp.json()) as T
}

export const api = {
  async login(password: string): Promise<void> {
    const out = await request<{ token: string }>('POST', '/auth/login', { password })
    setToken(out.token)
  },

  listNodes: () => request<NodeWithStatus[]>('GET', '/nodes'),
  importConfig: (content: string) =>
    request<Node[]>('POST', '/nodes/import', { kind: 'awg-config', content }),
  removeNode: (id: string) => request<void>('DELETE', `/nodes/${id}`),
  activateNode: (id: string) => request<SystemInfo>('POST', `/nodes/${id}/activate`),

  listRoutes: () => request<RouteRule[]>('GET', '/routes'),
  addRoute: (r: Omit<RouteRule, 'id'>) => request<RouteRule>('POST', '/routes', r),
  deleteRoute: (id: string) => request<void>('DELETE', `/routes/${id}`),
  applyRoutes: () => request<{ ok: boolean; detail?: string }>('POST', '/routes/apply'),

  system: () => request<SystemInfo>('GET', '/system'),
  probe: (target: string, expectedVia: 'tunnel' | 'direct') =>
    request<PathProbe>('POST', '/system/probe', { target, expectedVia }),

  capabilities: () => request<Capabilities>('GET', '/capabilities'),

  // The apply transaction (M1.3). The reply to applyConfig is routinely never
  // received: the change under test can cut the very link carrying it. That is
  // not an error, and callers must treat a failure here as "unknown" rather
  // than "it did not happen" — the live stream and GET /apply are what tell
  // the truth afterwards.
  applyState: () => request<ApplyState>('GET', '/apply'),
  applyConfig: (timeoutSeconds?: number) =>
    request<ApplyState>('POST', '/apply', { timeout_seconds: timeoutSeconds ?? 0 }),
  confirmApply: (token: string) => request<ApplyState>('POST', '/apply/confirm', { token }),
  revertApply: () => request<ApplyState>('POST', '/apply/revert'),

  // Staging is not applying. PUT writes a draft the device itself keeps; only
  // POST /apply commits it, under the watchdog. The two verbs stay apart all
  // the way down to uci (internal/adapters/openwrt/network_write.go), which is
  // what makes editing the uplink survivable.
  stageWAN: (cfg: WANConfig) => request<StagedChanges>('PUT', '/network/wan', cfg),
  // The local network, staged the same way and in three pieces, because the
  // device refuses them for three different reasons: the router's own address
  // can strand the pool, the pool can fall outside the network, and a
  // reservation can point anywhere. One endpoint per decision keeps a refusal
  // attached to the decision that caused it.
  stageLAN: (cfg: LANConfig) => request<StagedChanges>('PUT', '/network/lan', cfg),
  stageHandout: (cfg: HandoutConfig) => request<StagedChanges>('PUT', '/network/lan/handout', cfg),
  stageReservation: (cfg: ReservationConfig) =>
    request<StagedChanges>('PUT', '/network/lan/reservations', cfg),
  removeReservation: (id: string) =>
    request<StagedChanges>('DELETE', `/network/lan/reservations/${encodeURIComponent(id)}`),
  stagedChanges: () => request<StagedChanges>('GET', '/apply/changes'),
  discardStaged: () => request<void>('DELETE', '/apply/changes'),

  interfaces: () => request<NetworkInterface[]>('GET', '/network/interfaces'),
  // 404 here means "this device has no uplink", which is a state and not a
  // failure. It is surfaced as null so callers stop at a value instead of an
  // exception.
  async wan(): Promise<WANStatus | null> {
    try {
      return await request<WANStatus>('GET', '/network/wan')
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null
      throw e
    }
  },

  // 404 means "this device has no local network", which is a state of the
  // hardware; 501 means "this platform cannot read one", which is a state of
  // our own code. They are different sentences to the operator, so only the
  // first is folded into a value here and the second is left to throw.
  async lan(): Promise<LANStatus | null> {
    try {
      return await request<LANStatus>('GET', '/network/lan')
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null
      throw e
    }
  },

  metrics: () =>
    request<{ live: MetricSample[]; day: MetricSample[]; bufferBytes: number }>(
      'GET',
      '/system/metrics',
    ),
}

// --- live stream (D-12) ---

/** One frame of GET /events: the topic name and its already-parsed payload. */
export interface LiveEvent {
  topic: string
  data: unknown
}

/**
 * openEvents subscribes to GET /events and calls onEvent per frame until the
 * returned function is called.
 *
 * It is written on fetch + a stream reader rather than the browser's
 * EventSource on purpose: EventSource cannot send an Authorization header, so
 * using it would mean putting the bearer token in the query string — where it
 * lands in server logs, proxy logs and the browser history. A stream we read
 * ourselves keeps the token in the header, and costs us only the (small) SSE
 * framing parser below.
 */
export function openEvents(
  onEvent: (e: LiveEvent) => void,
  onStateChange?: (connected: boolean, error?: string) => void,
): () => void {
  const ctrl = new AbortController()
  let stopped = false

  const run = async () => {
    // Reconnect with a backoff: the device drops this stream every time it
    // reboots or a change cuts the link, which here is routine rather than
    // exceptional.
    let delay = 1000
    while (!stopped) {
      try {
        const headers: Record<string, string> = { Accept: 'text/event-stream' }
        const t = getToken()
        if (t) headers['Authorization'] = `Bearer ${t}`
        const resp = await fetch(`${BASE}/events`, { headers, signal: ctrl.signal })
        if (resp.status === 401) {
          clearToken()
          onStateChange?.(false, 'unauthorized')
          return
        }
        if (!resp.ok || !resp.body) throw new Error(`HTTP ${resp.status}`)

        onStateChange?.(true)
        delay = 1000
        await readStream(resp.body, onEvent)
        // A clean end of stream still means we are no longer connected.
        onStateChange?.(false)
      } catch (err) {
        if (stopped) return
        onStateChange?.(false, err instanceof Error ? err.message : String(err))
      }
      if (stopped) return
      await new Promise((r) => setTimeout(r, delay))
      delay = Math.min(delay * 2, 15000)
    }
  }
  void run()

  return () => {
    stopped = true
    ctrl.abort()
  }
}

/** readStream parses the SSE framing: `event:` / `data:` lines, blank-line separated. */
async function readStream(body: ReadableStream<Uint8Array>, onEvent: (e: LiveEvent) => void) {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let topic = ''
  let data = ''

  const flush = () => {
    if (data !== '') {
      try {
        onEvent({ topic: topic || 'message', data: JSON.parse(data) })
      } catch {
        // A frame we cannot parse is dropped rather than allowed to kill the
        // stream: one bad payload must not cost the panel its live updates.
      }
    }
    topic = ''
    data = ''
  }

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    let nl = buffer.indexOf('\n')
    while (nl !== -1) {
      const line = buffer.slice(0, nl).replace(/\r$/, '')
      buffer = buffer.slice(nl + 1)
      if (line === '') flush()
      else if (line.startsWith('event:')) topic = line.slice(6).trim()
      else if (line.startsWith('data:')) data += line.slice(5).trim()
      nl = buffer.indexOf('\n')
    }
  }
  flush()
}
