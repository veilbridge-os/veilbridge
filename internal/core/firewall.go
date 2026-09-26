package core

// The firewall as the panel shows it (M3.3, #35): zones and what they allow,
// which zone may reach which, forwarded ports and traffic rules. Everything
// here is in the panel's terms — "accept", "internet", "local" — and nothing
// in it is nftables syntax (D-3). DESIGN D-66…D-69 record how it was decided.

// Firewall actions, lower-case: the panel's words, not the kernel's.
const (
	ActionAccept = "accept"
	ActionReject = "reject"
	ActionDrop   = "drop"
	// ActionOther is a rule that neither lets traffic through nor blocks it:
	// it marks it, or hands it to a helper. Reading such a rule as "accept"
	// would put a false line on the one screen people open to find out what
	// is allowed.
	ActionOther = "other"
)

// Zone roles (D-68). They are derived from what a zone does, never from its
// name: on one of our own stands the uplink sits in the zone called "lan".
const (
	// ZoneInternet is the zone that hides the local network behind the
	// router's address (NAT) — the side the internet is on.
	ZoneInternet = "internet"
	// ZoneLocal is the zone the local network belongs to.
	ZoneLocal = "local"
)

// FirewallZone is a group of connections with one policy.
type FirewallZone struct {
	Name string `json:"name"`
	// Role is ZoneInternet, ZoneLocal, or empty for any other zone.
	Role string `json:"role,omitempty"`
	// Networks are the connections the zone is made of, as configured.
	Networks []string `json:"networks"`
	// Live reports whether at least one of those connections is up right
	// now. A zone with nothing live is configured and not in use — on the x86
	// stand that is the zone called "wan".
	Live bool `json:"live"`
	// Input is what happens to traffic from this zone to the router itself,
	// Output to traffic the router sends into it, Forward to traffic passing
	// through between connections of the same zone.
	Input   string `json:"input"`
	Output  string `json:"output"`
	Forward string `json:"forward"`
	// Masquerade: addresses in other zones are hidden behind the router's
	// own address when leaving through this one (NAT).
	Masquerade bool `json:"masquerade"`
}

// ZoneForwarding allows traffic from one zone into another.
type ZoneForwarding struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PortForward sends connections arriving at the router on a port to a device
// inside the network.
type PortForward struct {
	// ID addresses the entry on the device; it is what an edit or a removal
	// refers to (same reasoning as D-49).
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
	// Protocols: "tcp", "udp", or both.
	Protocols []string `json:"protocols"`
	// From is the zone the connections come from, usually the internet side.
	From string `json:"from"`
	// ExternalPort is the port (or "first-last" range) on the router.
	ExternalPort string `json:"externalPort"`
	// ToAddress and ToPort are where the connection is sent. ToPort equals
	// ExternalPort when the device keeps the number.
	ToAddress string `json:"toAddress"`
	ToPort    string `json:"toPort"`
}

// FirewallRule is a traffic rule: what to do with matching traffic.
type FirewallRule struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
	// System marks a rule exactly as the firewall package ships it (D-67).
	// The panel shows it read-only; once a person changes it, it is theirs.
	System bool `json:"system"`
	// From is the zone traffic comes from. To is the zone it goes to; empty
	// means traffic to the router itself, "*" any zone.
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
	Protocols []string `json:"protocols"`
	// Ports are the destination ports: one, a first-last range, or several
	// of those separated by spaces. Empty for any.
	Ports  string `json:"ports,omitempty"`
	Action string `json:"action"`
	// Family limits the rule to "ipv4" or "ipv6"; empty means both.
	Family string `json:"family,omitempty"`
	// Unsupported names the conditions this rule has that the panel does not
	// show yet (RuleSourceAddress, RuleSchedule, ...). A rule with any of them
	// matches less traffic than its other fields say, so the panel names them
	// and lets such a rule be switched on and off or removed, but not edited:
	// editing what you cannot see is how a rule ends up doing something
	// nobody meant.
	Unsupported []string `json:"unsupported"`
}

// Conditions a rule can have that the panel does not model yet.
const (
	RuleSourceAddress      = "sourceAddress"
	RuleSourcePort         = "sourcePort"
	RuleDestinationAddress = "destinationAddress"
	RuleICMPTypes          = "icmpTypes"
	RuleSchedule           = "schedule"
	RuleRateLimit          = "rateLimit"
	RuleLogging            = "logging"
	// RuleOther is any remaining option: marks, helpers, raw extras.
	RuleOther = "other"
)

// FirewallStatus is the whole firewall in one answer, for the same reason as
// D-45: a screen assembled from four requests shows a rule whose zone has not
// loaded yet.
type FirewallStatus struct {
	Zones        []FirewallZone   `json:"zones"`
	Forwardings  []ZoneForwarding `json:"forwardings"`
	PortForwards []PortForward    `json:"portForwards"`
	Rules        []FirewallRule   `json:"rules"`
}

// FirewallReader is an optional capability of an adapter, asked for by type
// assertion like LANReader: a platform that cannot answer says 501, which is
// not the same as "no firewall here".
type FirewallReader interface {
	FirewallInfo() (FirewallStatus, error)
}

// PortForwardConfig is a requested port forward: an intent on its way to the
// apply transaction. An empty ID adds a new one; an ID from FirewallStatus
// edits that entry in place.
type PortForwardConfig struct {
	ID      string `json:"id,omitempty" doc:"Entry to edit, as reported by GET /firewall; empty adds a new one"`
	Name    string `json:"name,omitempty" maxLength:"64"`
	Enabled bool   `json:"enabled"`
	// Protocols: "tcp", "udp", or both.
	Protocols    []string `json:"protocols" minItems:"1" maxItems:"2"`
	ExternalPort string   `json:"externalPort" doc:"Port or first-last range on the router"`
	ToAddress    string   `json:"toAddress" doc:"Device on the local network the connections go to"`
	ToPort       string   `json:"toPort,omitempty" doc:"Port on that device; empty keeps the external one"`
}

// FirewallWriter stages firewall changes. Like every writer it only stages:
// the apply transaction commits, and every firewall change is dangerous
// (D-69), so it always runs under the confirmation window.
type FirewallWriter interface {
	StagePortForward(cfg PortForwardConfig) ([]ConfigChange, error)
	RemovePortForward(id string) ([]ConfigChange, error)
	StageRule(cfg FirewallRuleConfig) ([]ConfigChange, error)
	RemoveRule(id string) ([]ConfigChange, error)
}

// FirewallRuleConfig is a requested traffic rule of the owner's. An empty ID
// adds a new one; an ID from FirewallStatus edits that rule in place.
type FirewallRuleConfig struct {
	ID      string `json:"id,omitempty" doc:"Rule to edit, as reported by GET /firewall; empty adds a new one"`
	Name    string `json:"name,omitempty" maxLength:"64"`
	Enabled bool   `json:"enabled"`
	From    string `json:"from" doc:"Zone the traffic comes from, as reported by GET /firewall, or * for any"`
	To      string `json:"to,omitempty" doc:"Zone the traffic goes to; empty for the router itself, * for any zone"`
	// Protocols: any of "tcp", "udp", "icmp", or just "all".
	Protocols []string `json:"protocols" minItems:"1" maxItems:"3"`
	Ports     string   `json:"ports,omitempty" doc:"Destination ports for TCP/UDP: one, first-last, or several separated by spaces or commas; empty for any"`
	Action    string   `json:"action" doc:"accept, reject (the sender is told) or drop (silently)"`
	Family    string   `json:"family,omitempty" doc:"ipv4 or ipv6 to limit the rule to one; empty for both"`
	// Before places a NEW rule in front of an existing one. Order matters:
	// the firewall acts on the first rule that matches (D-70).
	Before string `json:"before,omitempty" doc:"For a new rule: the rule (id from GET /firewall) to place it in front of; empty puts it last. The first matching rule wins."`
}
