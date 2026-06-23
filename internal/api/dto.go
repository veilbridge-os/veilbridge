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

// --- system ---

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
