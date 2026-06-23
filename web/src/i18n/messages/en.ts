// English — the base bundle. Its shape is the contract other locales follow.
const en = {
  nav: {
    dashboard: 'Dashboard',
    nodes: 'Nodes',
    routing: 'Routing',
    logout: 'Logout',
  },
  login: {
    title: 'VeilBridge',
    password: 'Admin password',
    signIn: 'Sign in',
    failed: 'Login failed',
  },
  dashboard: {
    egressTunnel: 'Egress (tunnel)',
    noTunnel: 'no tunnel',
    tunnelUp: 'Tunnel up',
    direct: 'Direct',
    directWan: 'Direct WAN',
    cpu: 'CPU',
    memory: 'Memory',
    up: 'up',
  },
  nodes: {
    importConfig: 'Import config',
    refresh: 'Refresh',
    empty: 'No nodes — import a .conf to start',
    name: 'Name',
    endpoint: 'Endpoint',
    engine: 'Engine',
    active: 'Active',
    handshake: 'Handshake',
    actions: 'Actions',
    activate: 'Activate',
    remove: 'Remove',
    never: 'never',
    ago: '{n}s ago',
    importTitle: 'Import AmneziaWG config',
    cancel: 'Cancel',
    import: 'Import',
    imported: 'Node imported',
    importFailed: 'Import failed',
    activated: 'Activated — egress switched',
    activateFailed: 'Activate failed',
    confirmRemove: 'Remove this node?',
    confirm: 'Confirm',
  },
  routes: {
    addRule: 'Add rule',
    applyToOs: 'Apply to OS',
    refresh: 'Refresh',
    empty: 'No routing rules',
    kind: 'Kind',
    value: 'Value',
    target: 'Target',
    note: 'Note',
    actions: 'Actions',
    probe: 'Probe',
    delete: 'Delete',
    domain: 'domain',
    subnet: 'subnet',
    tunnel: 'tunnel',
    direct: 'direct',
    addTitle: 'Add routing rule',
    cancel: 'Cancel',
    add: 'Add',
    applied: 'Rules applied',
    applyReported: 'Apply reported: {detail}',
    applyFailed: 'Apply failed',
    probeFailed: 'Probe failed',
    probeOk: 'OK',
    probeMismatch: 'MISMATCH',
    probeResult: '{verdict}: expected {expected}, got {actual}. {detail}',
  },
} as const

// The schema every other locale must satisfy — a missing/misspelled key fails
// to compile. DeepString maps the `as const` literal types back to `string` so
// other locales can carry their own text while keeping the exact key structure.
type DeepString<T> = {
  [K in keyof T]: T[K] extends string ? string : DeepString<T[K]>
}
export type MessageSchema = DeepString<typeof en>

export default en
