package core

// Static routes as the panel shows them (M3.4, #37). DESIGN D-73…D-76 record
// how they were decided, all by measurement on both stands:
//
//   - netifd owns the main routing table; a static route is a `route` section
//     it applies, and the panel never writes a kernel route for it (D-73);
//   - a route "works" only when the kernel has it — netifd's own status lists
//     routes the kernel refused (D-74);
//   - the gateway has to be on the chosen connection's network, or netifd
//     accepts the route and the kernel silently never gets it (D-75);
//   - a route with the prefix and metric of a kernel route netifd does not
//     own is refused, because netifd would take that route over and remove
//     it with its own (D-76).

// StaticRoute is one route of the owner's, as configured plus whether the
// kernel is actually using it.
type StaticRoute struct {
	// ID addresses the entry on the device (same reasoning as D-49).
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
	// Family is "ipv4" or "ipv6".
	Family string `json:"family"`
	// Target is the network the route is for, in CIDR form ("10.8.0.0/24");
	// a single address is a /32 (or /128).
	Target string `json:"target"`
	// Gateway is the next hop; empty when the network is reached directly
	// through the connection.
	Gateway string `json:"gateway,omitempty"`
	// Interface is the connection the route leaves through, by its logical
	// name ("lan", "wan").
	Interface string `json:"interface"`
	Metric    int    `json:"metric"`
	// Active reports whether the kernel has this exact route right now
	// (D-74). A switched-off route is never active; a switched-on one that
	// is not active is the thing a person opens this screen to find.
	Active bool `json:"active"`
	// Unsupported names options this route has that the panel does not show
	// (RouteTable, RouteSource, …). Such a route can be switched on and off or
	// removed, but not edited here — the same rule as for firewall rules.
	Unsupported []string `json:"unsupported"`
}

// Route options the panel does not model yet.
const (
	RouteTable  = "table"
	RouteSource = "source"
	RouteType   = "type"
	RouteOnLink = "onlink"
	RouteMTU    = "mtu"
	// RouteOther is any remaining option.
	RouteOther = "other"
)

// RouteInterface is a connection a route can leave through, with the networks
// it is on — what the form offers, and what a gateway has to fall into.
type RouteInterface struct {
	Name string `json:"name"`
	Up   bool   `json:"up"`
	// IPv4 are the connection's own addresses in CIDR form: a gateway has to
	// be inside one of these networks (D-75).
	IPv4 []string `json:"ipv4"`
}

// RoutesStatus is the routes screen in one answer, for the same reason as
// D-45: a route whose connection has not loaded yet cannot be judged.
type RoutesStatus struct {
	Routes     []StaticRoute    `json:"routes"`
	Interfaces []RouteInterface `json:"interfaces"`
}

// RouteReader is an optional capability of an adapter, asked for by type
// assertion like FirewallReader.
type RouteReader interface {
	StaticRoutes() (RoutesStatus, error)
}

// StaticRouteConfig is a requested IPv4 route: an intent on its way to the
// apply transaction. An empty ID adds a new one; an ID from RoutesStatus edits
// that route in place.
type StaticRouteConfig struct {
	ID        string `json:"id,omitempty" doc:"Route to edit, as reported by GET /network/routes; empty adds a new one"`
	Name      string `json:"name,omitempty" maxLength:"64"`
	Enabled   bool   `json:"enabled"`
	Target    string `json:"target" doc:"IPv4 network (10.8.0.0/24) or a single address; host bits are cleared the way the device clears them"`
	Gateway   string `json:"gateway,omitempty" doc:"Next hop on the chosen connection's network; empty for a network reached directly"`
	Interface string `json:"interface,omitempty" doc:"Connection to leave through (GET /network/routes lists them); empty picks the one whose network holds the gateway"`
	Metric    int    `json:"metric,omitempty" minimum:"0" maximum:"65535" doc:"Lower wins between routes to the same network"`
}

// RouteWriter stages static route changes. Like every writer it only stages;
// every route change runs under the confirmation window, because a route can
// take the panel's own address away (D-73, D-69).
type RouteWriter interface {
	StageStaticRoute(cfg StaticRouteConfig) ([]ConfigChange, error)
	RemoveStaticRoute(id string) ([]ConfigChange, error)
}
