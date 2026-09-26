// English — the base bundle. Its shape is the contract other locales follow.
const en = {
  shell: {
    search: 'Search settings',
    searchHint: 'Ctrl + K',
    searchEmpty: 'Nothing matches “{q}”',
    menu: 'Menu',
    soon: 'soon',
    groupEgress: 'Internet access',
    groupLan: 'Local network',
    groupRouting: 'Routing',
    groupDevice: 'Device',
    dashboard: 'Dashboard',
    internet: 'Internet',
    nodes: 'Nodes',
    policies: 'Policies',
    network: 'Network',
    wifi: 'Wi-Fi',
    devices: 'Devices',
    rules: 'Rules',
    system: 'System',
    dataFrom: 'data from {time}',
    connecting: 'connecting…',
    offline: 'No connection to the device',
    offlineHint: 'Showing the last known values. The panel reconnects by itself.',
    deviceError: 'The device stopped answering: {detail}',
    notAvailable: 'This section is not available on this device',
    notBuilt: 'This section is not built yet',
    theme: 'Theme',
    themeSystem: 'Follow the system',
    themeLight: 'Light',
    themeDark: 'Dark',
    openMenu: 'Open the menu',
    openSearch: 'Open the settings search',
    sessionExpired: 'Session expired',
  },
  apply: {
    draft: 'Draft: {n} change | Draft: {n} change | Draft: {n} changes',
    draftHint: 'The device is still running the previous settings.',
    review: 'Review',
    discard: 'Discard',
    applyNow: 'Apply',
    applyWithConfirm: 'Apply with confirmation',
    waiting: 'New settings applied. Is the panel still answering?',
    waitingHint: 'If you do not confirm, the device restores the previous settings by itself.',
    confirm: 'Yes, the panel is up',
    cancelNow: 'Restore the previous settings',
    remaining: 'remaining',
    applying: 'Applying… the panel is not answering yet',
    applyingHint:
      'This happens: the change touched the very link the panel talks over. Waiting for it to come back.',
    reverted: 'Your changes were undone automatically',
    revertedHint: 'The panel did not answer in time, so the device restored the previous settings.',
    restoreDraft: 'Restore my changes as a draft',
    close: 'Close',
    revertFailed: 'Could not restore the previous settings',
    revertFailedHint:
      'The device may be unreachable over the network. Connect a computer with a cable and open the panel at its local address — the password has not changed.',
    confirmFailed: 'The confirmation did not reach the device',
    confirmFailedHint:
      'Repeat it — there is still time. Doing nothing is also safe: the settings will come back by themselves.',
    unreachable: 'The panel is not answering — trying to reach the device',
    unreachableHint:
      'If you are reading this at the old address, do nothing: in {time} the settings come back by themselves and the panel opens where it opened before.',
    expired: 'Time is up — the device is restoring the previous settings',
    expiredHint:
      'Nobody confirmed the panel was reachable, so the change is being undone. Nothing to press.',
    acknowledge: 'Understood — continue configuring',
    retryConfirm: 'Retry confirmation',
    change: 'change {id}',
    confirmed: 'Change confirmed',
    willChange: 'What will change on this device',
    mayCutAccess: 'may cut access to the panel',
    flagOn: 'yes',
    flagOff: 'no',
    fwAccept: 'allow',
    fwReject: 'block',
    fwDrop: 'block without answering',
    fwRouter: 'the router itself',
    fwAnyZone: 'any zone',
    fwBothFamilies: 'IPv4 and IPv6',
    fwAnyPort: 'any',
    fwAllProtocols: 'all protocols',
    wasUnset: 'was not set',
    nowNothing: 'removed',
    showTechnical: 'Technical details — for the log and for support',
    hideTechnical: 'Hide technical details',
    autoRevertHint:
      'If the panel stops answering after this is applied, the device brings the previous settings back by itself.',
  },
  tiles: {
    egress: 'Egress',
    egressAddr: 'the address sites see',
    egressProof:
      'The provider address is {direct}. The egress differs from it, so traffic really does leave through the tunnel.',
    pastTunnel: 'past the tunnel',
    pastTunnelHint:
      'This is the same address as the direct provider egress: node “{node}” is active, but traffic is not going through it.',
    tunnelDown: 'tunnel is down',
    noNodes: 'No nodes yet',
    noNodesHint:
      'Add a node — the settings file from your tunnel provider — and the gateway starts moving traffic.',
    addNode: 'Add node',
    internet: 'Internet',
    internetDirect: 'direct address, no tunnel',
    noUplink: 'No internet connection',
    noUplinkHint:
      'No interface has received an address. This is a device state, not a panel failure.',
    channel: 'Channel',
    gateway: 'Gateway',
    resolvers: 'DNS servers',
    pickedByName:
      'The uplink was identified by its name; {n} other interfaces also have a route to the internet.',
    device: 'Device',
    uptime: 'up {duration}',
    load: 'load {value}',
    loadWindow: 'load over 3 minutes · live buffer of the device',
    memory: 'Memory',
    memoryNormal: 'normal for a router',
    memoryHint: 'Up to 85% is normal, 85–92% is a warning, above 92% is red.',
    storage: 'Space for settings and apps',
    storageFree: '{value} free',
    storageAlmostFull: 'almost full',
    storageHint: 'Less than 10% free means an update or a backup will no longer fit.',
    nodesTitle: 'Nodes',
    nodesCount: '{n} configured',
    active: 'active',
    standby: 'standby',
    handshakeAgo: 'handshake {ago}',
    handshakeNever: 'no handshake yet',
    engineKernel: 'tunnel mode: in the kernel',
    engineUserspace: 'tunnel mode: inside the program',
    engineUserspaceHint:
      'In this mode the gateway does not move traffic for the whole local network.',
    rxtx: 'received {rx} · sent {tx}',
    laterTitle: 'Coming later',
    later:
      'traffic speed and volume · local network clients · who uses the most · switch port map · Wi-Fi and a guest QR code · the city of the egress',
    checkPath: 'Check the path',
    checking: 'checking…',
    lastKnown: 'last known value',
    frozenHint:
      'No connection to the device. The tile looks frozen, but not empty: the old numbers say more than dashes.',
  },
  time: {
    secondsAgo: '{n} second ago | {n} second ago | {n} seconds ago',
    minutesAgo: '{n} minute ago | {n} minute ago | {n} minutes ago',
    hoursAgo: '{n} hour ago | {n} hour ago | {n} hours ago',
    days: '{n} day | {n} day | {n} days',
    hours: '{n} hour | {n} hour | {n} hours',
    minutes: '{n} minute | {n} minute | {n} minutes',
  },
  details: 'Technical details',
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
  // The words the apply bar shows for a staged change, looked up by the
  // stable part of the configuration key. They exist here because the device
  // answers in English and the panel does not: see ApplyBar.labelOf.
  // Nested per configuration, not keyed by a dotted string: vue-i18n resolves
  // `diff.network.uplink.proto` by walking the object, so a literal key with a dot in
  // it is unreachable \u2014 measured on the device, where every label came back in
  // English because the lookup silently missed. `section` is the row for a
  // whole added or removed section.
  diff: {
    // Keyed exactly like `labelKey` from the device: <config>.<role>.<option>,
    // plus `section` for a whole entry and `setting` for a key the device has
    // no words for. The panel no longer derives keys from `detail` (#27): it
    // did not know roles, and showed a change to the LOCAL address as
    // "Address on the internet side". check.ts fails the build when a key the
    // device can send is missing here.
    section: 'Configuration section',
    setting: 'System setting',
    network: {
      section: 'Network connection',
      setting: 'Network setting',
      uplink: {
        proto: 'Connection type',
        ipaddr: 'Address on the internet side',
        netmask: 'Network mask',
        gateway: 'Gateway',
        dns: 'DNS servers',
        peerdns: 'Use the provider\u2019s DNS servers',
        username: 'Provider login',
        password: 'Provider password',
      },
      lan: {
        proto: 'How the local network address is set',
        ipaddr: 'Address of this router on the local network',
        netmask: 'Local network mask',
        dns: 'DNS servers for the local network',
      },
    },
    firewall: {
      section: 'Firewall rule',
      setting: 'Firewall setting',
      redirect: {
        section: 'Port forward',
        name: 'Port forward name',
        enabled: 'Port forward is on',
        proto: 'Protocols',
        src: 'Connections from',
        src_dport: 'Port on the router',
        dest: 'Forwarded into',
        dest_ip: 'Device address',
        dest_port: 'Port on the device',
        target: 'Kind of forward',
      },
      // A whole rule is named by what it does: the action is the part a
      // person has to see before pressing Apply.
      rule: {
        section: 'Firewall rule',
        acceptInput: 'Allow access to the router',
        rejectInput: 'Block access to the router',
        dropInput: 'Block access to the router without answering',
        acceptForward: 'Allow traffic through the router',
        rejectForward: 'Block traffic through the router',
        dropForward: 'Block traffic through the router without answering',
        name: 'Rule name',
        enabled: 'Rule is on',
        src: 'Traffic from',
        dest: 'Traffic to',
        proto: 'Protocols',
        dest_port: 'Ports',
        target: 'Action',
        family: 'IP version',
      },
    },
    // The local network keeps two different things in one configuration, so
    // the handout and a device's reserved address are named apart.
    dhcp: {
      section: 'Address handout',
      setting: 'Local network setting',
      lan: {
        start: 'First address handed out',
        limit: 'Last address handed out',
        leasetime: 'How long an address is given for',
        ignore: 'Hand out addresses on the local network',
      },
      host: {
        section: 'Reserved address',
        mac: 'Device',
        ip: 'Reserved address',
        name: 'Device name',
      },
    },
    wireless: { section: 'Wi-Fi network', setting: 'Wi-Fi setting' },
    system: { setting: 'Device setting' },
  },
  wan: {
    title: 'Internet',
    linkUp: 'connection is up',
    linkDown: 'no connection',
    draftPending: 'draft: {n} change | draft: {n} change | draft: {n} changes',
    probe: 'Check the path',
    probing: 'Checking…',
    // Facts, never a green tick: the proof is the address traffic came back
    // from (D-5, NFR-5).
    probeVia:
      'Path checked just now: the request left through “{iface}” and came back from {addr} — the address of the connection itself.',
    probeMismatch:
      'Path checked just now: the request was expected to leave directly, and came back from {addr} instead. Expected {expected}, got {actual}.',
    probeFailed: 'The path check did not complete: {detail}',
    haveInternet: 'The internet works — through connection “{iface}”',
    noGateway: 'The connection is up, but there is no way out',
    noGatewayHint:
      'The device did get an address, but no gateway was assigned, so there is nowhere for traffic to go. This is usually the provider rather than your settings.',
    none: 'No connection leads to the internet',
    noneHint:
      'This is how a first start looks, and so does an unplugged cable. Choose a connection type below and save a draft.',
    setUp: 'Set up the connection',
    ambiguous: 'Showing connection “{iface}”',
    ambiguousHint:
      'Two connections lead to the internet: {list}. The panel shows the one called “{iface}”. Everything below is about that one; the other may have a different address and statistics.',
    ambiguousCoinToss:
      'Several connections lead to the internet: {list}. None of them is called “wan”, so the panel shows the first one — {iface}. Everything below is about that one.',
    proto: 'Connection type',
    protoDhcp: 'Automatic',
    protoStatic: 'Static address',
    protoPppoe: 'PPPoE',
    protoHint:
      'As in the contract with your provider. “Automatic” suits almost every home connection.',
    address: 'Address on the internet side',
    addressHint: 'From the contract with your provider',
    netmask: 'Network mask',
    netmaskHint: 'From the same place, usually 255.255.255.0',
    gateway: 'Gateway',
    gatewayHint: 'The address of the provider’s equipment',
    resolvers: 'DNS servers',
    resolversFromProvider: 'From the provider',
    resolversOwn: 'My own',
    resolversHint:
      'Choosing “my own” adds a separate line to the list of changes — “Use the provider’s DNS servers: yes → no”. Without it the setting would apply and change nothing.',
    addResolver: 'Add a DNS server',
    removeResolver: 'Remove this DNS server',
    username: 'Provider login',
    password: 'Provider password',
    passwordHint:
      'Leave it empty to keep the current one. The device does not hand the password back, so the panel does not know it.',
    uptime: 'Connection has been up',
    port: 'Port',
    settings: 'Connection settings',
    current: 'On the device right now',
    currentWhileDraft:
      'While the draft is unapplied, this block shows the previous settings unchanged: you need to see what you are leaving.',
    draftEmpty: 'draft is empty',
    save: 'Save draft',
    cancel: 'Cancel',
    onlyWayIn:
      'This connection is the only way to the panel. If the link goes away after applying, the settings come back by themselves in {sec} seconds.',
    onlyWayInBack:
      'If the panel still does not open after that, plug a cable into a local network port and open {url} — that way does not depend on the internet.',
    ipv6: 'IPv6',
    readOnly: 'read-only',
    ipv6Hint: 'There are no IPv6 settings here yet — the panel only reads the addresses.',
    fieldsBad: '{n} field is filled in incorrectly | {n} fields are filled in incorrectly',
    deviceSaid: 'the device answered: {detail}',
    // The device refuses in its own words; these are ours for the same thing.
    badAddress: 'The address is not an IPv4 address',
    badNetmask: 'The network mask is wrong: it is an address, not a mask',
    badGateway: 'The gateway is not an address',
    badResolver: 'This DNS server is not an address',
    needUsername: 'A login is required: PPPoE will not come up without one',
    refused: 'The device refused the draft',
    busyTitle: 'A previous change is still waiting for confirmation',
    busyHint:
      'A new draft cannot be saved until you confirm the link is up, or until the settings come back by themselves. The form is locked and your values are kept.',
    saved: 'Draft saved — nothing has changed on the device yet',
    dnsPlaceholder: '203.0.113.1',
  },
  lan: {
    title: 'Local network',
    handingOut: 'handing out addresses',
    handoutOff: 'not handing out addresses',
    draftPending: 'draft: {n} change | draft: {n} change | draft: {n} changes',
    draftEmpty: 'draft is empty',
    pinByHand: 'Pin an address by hand',
    unsupported: 'The panel cannot read the local network on this platform',
    unsupportedHint:
      'This is a limit of the panel, not of the device: this part is only written for OpenWrt so far.',
    none: 'There is no local network on this device',
    noneHint:
      'A gateway with a single interface looks like this. Nothing is broken — there is simply nothing to hand addresses out to.',
    summaryOn:
      'The router hands out addresses — {n} device in the network | The router hands out addresses — {n} device in the network | The router hands out addresses — {n} devices in the network',
    summaryOff: 'The router does not hand out addresses here',
    // Never a green tick on its own: the claim "it works" is backed by an
    // address that was actually handed out, and when there is no such
    // evidence the screen says so instead (D-5, NFR-5).
    proofIssued:
      'Last address handed out {ago} to {mac} — the handout works, not just switched on.',
    proofNoClients:
      'The handout is on, and nothing has asked for an address yet — so there is nothing here to prove it works.',
    proofUnknown:
      'The handout is on. How long ago the last address was given cannot be worked out from what the device reports.',
    proofOff:
      'Devices here have to be given an address by hand, or they will not reach the network.',
    routerHere: 'Router address here',
    pool: 'Hands out addresses',
    leaseTime: 'Address is given for',
    onlineNow: 'In the network now',
    pinnedCount: 'Pinned by hand',
    nDevices: '{n} device | {n} device | {n} devices',
    nAddresses: '{n} address | {n} address | {n} addresses',
    devices: 'Devices in the network',
    devicesCount: '{online} now · {pinned} with a pinned address',
    devicesHint:
      'A device announces its own name and often announces none, so a row has to read without it. A pinned address is a property of the row, not a second list.',
    noDevices: 'No device has asked for an address yet',
    noDevicesHint:
      'This is what an untouched network looks like, and also what one looks like when every device is configured by hand.',
    device: 'Device',
    address: 'Address',
    netmask: 'Network mask',
    hardware: 'MAC address',
    leaseLeftCol: 'Address valid',
    leaseLeft: '{d} left',
    forever: 'permanently',
    noName: 'no name announced',
    pinned: 'pinned',
    offline: 'not in the network',
    pin: 'Pin this address',
    unpin: 'Unpin',
    handoutTitle: 'Address handout',
    handoutSwitch: 'Hand out addresses',
    handoutOnHint:
      'Devices get an address by themselves as soon as they connect. This is what almost every home network wants.',
    handoutOffHint:
      'Every device will have to be given an address by hand. Pinned addresses stay, and the range below is kept for when you switch this back on.',
    first: 'First address',
    last: 'Last address',
    lease1h: '1 hour',
    lease12h: '12 hours',
    lease24h: '24 hours',
    poolHint:
      'The range is given as two addresses rather than a start and a count: that is the same language it is shown in above. Pinned addresses may sit outside it.',
    routerAddress: 'Router address in this network',
    mayCutAccess: 'may cut access to the panel',
    warnInside:
      'You are in this very network, so applying will cut your link to the panel. To get back: unplug and replug the cable or rejoin the Wi-Fi, open {url} and press “Yes, the panel is up”. If that takes longer than {sec} seconds, the settings return by themselves and the panel is back at the old address.',
    warnOutside:
      'You are connected from outside this network, so your own access to the panel is not affected. Devices inside it will have to reconnect, and anything given a fixed address by hand will have to be changed.',
    nameOptional: 'Name (optional)',
    nameHint:
      'The name is published to the whole network, so it is asked for here rather than taken from what the device called itself.',
    macHint: 'Written on the device itself, or copied from the row above',
    save: 'Save draft',
    cancel: 'Cancel',
    close: 'Close',
    saved: 'Draft saved — nothing has changed on the device yet',
    refused: 'The device refused the draft',
    deviceSaid: 'the device answered: {detail}',
    // What is shown at a field the device refused (#28): our sentence first,
    // the device's own words after it, which carry the specifics.
    bad: {
      address: 'This address cannot be used',
      netmask: 'This is not a network mask',
      first: 'The first address does not fit',
      last: 'The last address does not fit',
      mac: 'This is not a MAC address',
      ip: 'This address cannot be reserved',
      name: 'This name cannot be published on the network',
    },
    busyTitle: 'A previous change is still waiting for confirmation',
    busyHint:
      'A new draft cannot be saved until you confirm the link is up, or until the settings come back by themselves. The form is locked and your values are kept.',
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
