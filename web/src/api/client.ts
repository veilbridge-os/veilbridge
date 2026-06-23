// Thin typed client over the Router Core API. Types come from schema.ts, which
// is generated from api/openapi.yaml (the code-first snapshot, DESIGN D-8) — so
// the frontend never drifts from the backend contract.
import type { components } from './schema'

export type Node = components['schemas']['Node']
export type NodeStatus = components['schemas']['NodeStatus']
export type NodeWithStatus = components['schemas']['NodeWithStatus']
export type RouteRule = components['schemas']['RouteRule']
export type SystemInfo = components['schemas']['SystemInfo']
export type PathProbe = components['schemas']['PathProbe']

const TOKEN_KEY = 'veilbridge.token'
const BASE = '/api/v1'

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}
function setToken(t: string) {
  localStorage.setItem(TOKEN_KEY, t)
}
export function clearToken() {
  localStorage.removeItem(TOKEN_KEY)
}
export function isAuthed(): boolean {
  return !!getToken()
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
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
    try {
      const err = await resp.json()
      msg = err?.detail || err?.title || msg
    } catch {
      // non-JSON error body; keep the status message
    }
    throw new ApiError(resp.status, msg)
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
}
