package api

import "github.com/veilbridge-os/veilbridge/internal/core"

// Huma derives request/response schemas from these structs. Inputs carry path/
// query/body via struct tags; outputs wrap the payload in a Body field. The
// shapes mirror DESIGN §3 / api/openapi.yaml.

// --- auth ---

type LoginInput struct {
	Body struct {
		Password string `json:"password" doc:"Admin password"`
	}
}

type LoginOutput struct {
	Body struct {
		Token     string `json:"token" doc:"JWT bearer token"`
		ExpiresAt string `json:"expiresAt" doc:"RFC3339 expiry"`
	}
}

// --- nodes ---

type NodeIDInput struct {
	ID string `path:"id" doc:"Node ID"`
}

type ImportInput struct {
	Body struct {
		Kind    string `json:"kind" enum:"awg-config,subscription" doc:"Import source kind"`
		Content string `json:"content,omitempty" doc:"Raw .conf content (kind=awg-config)"`
		URL     string `json:"url,omitempty" doc:"Subscription URL (kind=subscription)"`
	}
}

type NodesOutput struct {
	Body []core.NodeWithStatus
}

type ImportOutput struct {
	Body []core.Node
}

type NodeStatusOutput struct {
	Body core.NodeStatus
}

type SystemOutput struct {
	Body core.SystemInfo
}

// --- routes ---

// RuleBody is the writable shape of a routing rule (no ID — the server assigns
// it on create and takes it from the path on update). Mirrors openapi.yaml's
// RouteRuleInput.
type RuleBody struct {
	Kind   core.RuleKind `json:"kind" enum:"domain,subnet" doc:"Match kind"`
	Value  string        `json:"value" doc:"Domain or CIDR"`
	Target core.Target   `json:"target" enum:"tunnel,direct" doc:"Where to send it"`
	Note   string        `json:"note,omitempty" doc:"User comment"`
}

func (b RuleBody) toRule(id string) core.RouteRule {
	return core.RouteRule{ID: id, Kind: b.Kind, Value: b.Value, Target: b.Target, Note: b.Note}
}

type RuleInput struct {
	Body RuleBody
}

type RuleIDInput struct {
	ID   string `path:"id" doc:"Rule ID"`
	Body RuleBody
}

type RoutesOutput struct {
	Body []core.RouteRule
}

type RuleOutput struct {
	Body core.RouteRule
}

type ApplyOutput struct {
	Body struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail,omitempty"`
	}
}

// --- apply transaction (M1.3) ---

// ApplyTxInput asks for a confirmation window. Zero means "server default";
// anything above the server's maximum is clamped, because a window too long to
// notice is the same as having no watchdog.
type ApplyTxInput struct {
	Body struct {
		TimeoutSeconds int `json:"timeout_seconds,omitempty" minimum:"0" maximum:"600" doc:"Confirmation window in seconds; 0 uses the server default"`
	}
}

// ApplyConfirmInput carries the token handed out by the pending apply. It is
// required on purpose: confirming "whatever is pending" would let a stale
// browser tab confirm a change it never saw.
type ApplyConfirmInput struct {
	Body struct {
		Token string `json:"token" required:"true" doc:"Token returned by POST /apply"`
	}
}

// ApplyStateOutput is what the apply-bar renders: which phase, until when, and
// what went wrong if anything did.
type ApplyStateOutput struct {
	Body core.ApplyState
}

// --- system ---

// CapabilitiesOutput is what the UI asks for before it renders anything: the
// front end never branches on the platform, it branches on this (D-3, D-17).
type CapabilitiesOutput struct {
	Body core.Capabilities
}

// --- network (M1.5) ---

type InterfacesOutput struct {
	Body []core.NetworkInterface
}

type WANOutput struct {
	Body core.WANStatus
}

// LANOutput is the local network, its address handout and its clients in one
// answer — see core.LANStatus for why they are not three endpoints.
type LANOutput struct {
	Body core.LANStatus
}

// FirewallOutput is the whole firewall in one answer (M3.3, #35).
type FirewallOutput struct {
	Body core.FirewallStatus
}

// StageLANInput is this router's own address on the local network. As with
// the uplink, staging is a separate call from applying.
type StageLANInput struct {
	Body core.LANConfig
}

// StageHandoutInput is the address handout, expressed in addresses rather
// than in the offsets the device stores (D-44).
type StageHandoutInput struct {
	Body core.HandoutConfig
}

// StageReservationInput pins one address to one device.
type StageReservationInput struct {
	Body core.ReservationConfig
}

// RemoveReservationInput names the entry to drop as the device reported it.
// Addressing it by hardware address instead would race with a draft that
// already changed that address.
type RemoveReservationInput struct {
	ID string `path:"id" doc:"Reservation entry as reported by GET /network/lan"`
}

// StagePortForwardInput adds (no id) or edits (an id from GET /firewall) one
// port forward.
type StagePortForwardInput struct {
	Body core.PortForwardConfig
}

// RemovePortForwardInput names the forward to drop as the device reported it.
type RemovePortForwardInput struct {
	ID string `path:"id" doc:"Port forward entry as reported by GET /firewall"`
}

// StageFirewallRuleInput adds (no id) or edits (an id from GET /firewall) one
// of the owner's traffic rules.
type StageFirewallRuleInput struct {
	Body core.FirewallRuleConfig
}

// MoveFirewallRuleInput places one of the owner's rules elsewhere in the list.
type MoveFirewallRuleInput struct {
	ID   string `path:"id" doc:"Rule to move, as reported by GET /firewall"`
	Body struct {
		Before string `json:"before,omitempty" doc:"Rule (id from GET /firewall) to place it in front of; empty moves it to the end. The first matching rule wins."`
	}
}

// RemoveFirewallRuleInput names the rule to drop as the device reported it.
type RemoveFirewallRuleInput struct {
	ID string `path:"id" doc:"Rule as reported by GET /firewall"`
}

// StaticRoutesOutput is the routes screen in one answer (#37).
type StaticRoutesOutput struct {
	Body core.RoutesStatus
}

// StageStaticRouteInput adds (no id) or edits (an id from GET
// /network/routes) one IPv4 static route.
type StageStaticRouteInput struct {
	Body core.StaticRouteConfig
}

// RemoveStaticRouteInput names the route to drop as the device reported it.
type RemoveStaticRouteInput struct {
	ID string `path:"id" doc:"Route as reported by GET /network/routes"`
}

// StageWANInput is a requested uplink configuration. Staging is deliberately
// a separate call from applying: the operator sees the diff first, and the
// change only reaches the device through the watchdogged transaction.
type StageWANInput struct {
	Body core.WANConfig
}

type ChangesOutput struct {
	Body struct {
		Changes []core.ConfigChange `json:"changes"`
		// Dangerous is true when any staged edit can cut the panel's own
		// access, which is what makes the confirmation window mandatory.
		Dangerous bool `json:"dangerous"`
	}
}

type ProbeInput struct {
	Body struct {
		Target      string      `json:"target" doc:"Domain or IP to test"`
		ExpectedVia core.Target `json:"expectedVia" enum:"tunnel,direct" doc:"Expected path"`
	}
}

type ProbeOutput struct {
	Body core.PathProbe
}

type DiagnosticsInput struct {
	Target string `query:"target" doc:"Host or IP to diagnose"`
}

type DiagnosticsOutput struct {
	Body struct {
		Target string `json:"target"`
		Output string `json:"output"`
	}
}

// --- config (backup/restore) ---

type ConfigOutput struct {
	Body any `doc:"Full VeilBridge config document (includes secrets)"`
}

type ConfigImportInput struct {
	RawBody []byte
}
