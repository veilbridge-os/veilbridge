package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/metrics"
)

// Server wires the core managers (for operations) and the config store (for
// auth + backup/restore) into a Huma API mounted on a net/http mux.
type Server struct {
	adapter core.Adapter
	store   *config.Store
	tokens  *tokenIssuer
	api     huma.API
	mux     *http.ServeMux
	// apply owns the snapshot / confirm / auto-revert transaction (M1.3). It
	// lives on the server rather than inside a manager because a transaction
	// spans every manager that writes configuration.
	apply *core.ApplyCoordinator
	// history is the in-RAM metrics ring (D-13) and sampler fills it on a
	// timer, independently of whether any client is connected (M2.2).
	history *metrics.History
	sampler *metrics.Sampler
	// streams counts live SSE connections, so a reloading browser cannot pile
	// up goroutines on a 256 MB device. See maxStreams in events.go.
	streams atomic.Int32
}

// defaultApplyTimeout lives in core (DefaultApplyWindow) since #29: the
// panel is told the window before applying, so it has one source.
const defaultApplyTimeout = core.DefaultApplyWindow

// maxApplyTimeout bounds what a client may ask for. An hour-long window is
// indistinguishable from having no watchdog at all.
const maxApplyTimeout = 10 * time.Minute

// Options controls API construction.
type Options struct {
	// Dev enables the live OpenAPI spec (/openapi.json) and Swagger UI (/docs).
	// Off in production builds (D-9).
	Dev bool
}

// New builds the Router Core API. The token signing key is derived from the
// stored password hash; if no password is set yet, auth will reject everything
// until one is configured.
func New(adapter core.Adapter, store *config.Store, opts Options) (*Server, error) {
	doc, err := store.Load()
	if err != nil {
		return nil, err
	}

	cfg := huma.DefaultConfig("VeilBridge Router Core API", "0.1.0")
	// Declare the bearer (JWT) scheme so the generated spec documents auth and
	// validators are satisfied. Protected operations reference it via authSecurity.
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	}
	// D-9: dev-only spec/docs. Empty paths disable the built-in endpoints.
	if !opts.Dev {
		cfg.OpenAPIPath = ""
		cfg.DocsPath = ""
		cfg.SchemasPath = ""
	}

	mux := http.NewServeMux()
	coordinator := core.NewApplyCoordinator(adapter.Applier())
	// The journal is an optional capability of the adapter: with it a pending
	// transaction survives the daemon dying, without it the watchdog is only
	// as durable as this process. Ask, do not require.
	if jp, ok := adapter.Applier().(interface{ Journal() core.ApplyJournal }); ok {
		if j := jp.Journal(); j != nil {
			coordinator = coordinator.WithJournal(j)
		}
	}

	history := metrics.NewHistory()
	s := &Server{
		adapter: adapter,
		store:   store,
		tokens:  newTokenIssuer(doc.Settings.PasswordHash),
		api:     humago.NewWithPrefix(mux, "/api/v1", cfg),
		mux:     mux,
		apply:   coordinator,
		history: history,
		sampler: metrics.NewSampler(adapter.System(), history),
	}
	s.register()
	s.mountUI()
	return s, nil
}

// mountUI serves the embedded web assets at / when built with `-tags ui`.
// Without that tag uiFS() is nil and only the API is served (dev uses Vite).
func (s *Server) mountUI() {
	files := uiFS()
	if files == nil {
		return
	}
	fileServer := http.FileServer(http.FS(files))
	// Serve assets; fall back to index.html for unknown paths so the SPA's
	// client-side (hash) router handles them.
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(files, strings.TrimPrefix(r.URL.Path, "/")); err != nil && r.URL.Path != "/" {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL = new(url.URL)
			*r2.URL = *r.URL
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// Handler returns the http.Handler serving the API (mount the embedded UI on
// the same mux in cmd/veilbridged).
func (s *Server) Handler() http.Handler { return s.mux }

// Mux exposes the underlying mux so the daemon can add the embedded UI routes.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// OpenAPIYAML returns the generated OpenAPI 3.1 spec. This is the source of the
// checked-in api/openapi.yaml snapshot (D-8) — regenerate and diff it in CI.
func (s *Server) OpenAPIYAML() ([]byte, error) {
	return s.api.OpenAPI().YAML()
}

// requireAuth is per-operation middleware enforcing a valid bearer token.
func (s *Server) requireAuth(ctx huma.Context, next func(huma.Context)) {
	auth := ctx.Header("Authorization")
	raw := strings.TrimPrefix(auth, "Bearer ")
	if raw == auth || !s.tokens.verify(raw) {
		_ = huma.WriteErr(s.api, ctx, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	next(ctx)
}

func (s *Server) register() {
	authed := huma.Middlewares{s.requireAuth}
	// authSec marks an operation as requiring the bearer scheme in the spec.
	authSec := []map[string][]string{{"bearerAuth": {}}}

	// --- auth (open) ---
	// Security: empty (non-nil) slice → `security: []` in the spec = explicitly
	// no auth required (login is the one open endpoint).
	huma.Register(s.api, huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: "/auth/login",
		Summary: "Exchange admin password for a JWT", Tags: []string{"auth"},
		Security: []map[string][]string{},
	}, s.login)

	// --- nodes ---
	huma.Register(s.api, huma.Operation{
		OperationID: "listNodes", Method: http.MethodGet, Path: "/nodes",
		Summary: "List nodes with status", Tags: []string{"nodes"}, Middlewares: authed, Security: authSec,
	}, s.listNodes)
	huma.Register(s.api, huma.Operation{
		OperationID: "importNodes", Method: http.MethodPost, Path: "/nodes/import",
		Summary: "Import node(s) from a .conf or subscription", Tags: []string{"nodes"},
		DefaultStatus: http.StatusCreated, Middlewares: authed, Security: authSec,
	}, s.importNodes)
	huma.Register(s.api, huma.Operation{
		OperationID: "removeNode", Method: http.MethodDelete, Path: "/nodes/{id}",
		Summary: "Remove a node", Tags: []string{"nodes"},
		DefaultStatus: http.StatusNoContent, Middlewares: authed, Security: authSec,
	}, s.removeNode)
	huma.Register(s.api, huma.Operation{
		OperationID: "activateNode", Method: http.MethodPost, Path: "/nodes/{id}/activate",
		Summary: "Switch the active exit to this node", Tags: []string{"nodes"}, Middlewares: authed, Security: authSec,
	}, s.activateNode)
	huma.Register(s.api, huma.Operation{
		OperationID: "nodeStatus", Method: http.MethodGet, Path: "/nodes/{id}/status",
		Summary: "Fresh status for one node", Tags: []string{"nodes"}, Middlewares: authed, Security: authSec,
	}, s.nodeStatus)

	// --- routes ---
	huma.Register(s.api, huma.Operation{
		OperationID: "listRoutes", Method: http.MethodGet, Path: "/routes",
		Summary: "List routing rules", Tags: []string{"routes"}, Middlewares: authed, Security: authSec,
	}, s.listRoutes)
	huma.Register(s.api, huma.Operation{
		OperationID: "addRoute", Method: http.MethodPost, Path: "/routes",
		Summary: "Add a routing rule", Tags: []string{"routes"},
		DefaultStatus: http.StatusCreated, Middlewares: authed, Security: authSec,
	}, s.addRoute)
	huma.Register(s.api, huma.Operation{
		OperationID: "updateRoute", Method: http.MethodPut, Path: "/routes/{id}",
		Summary: "Update a routing rule", Tags: []string{"routes"}, Middlewares: authed, Security: authSec,
	}, s.updateRoute)
	huma.Register(s.api, huma.Operation{
		OperationID: "deleteRoute", Method: http.MethodDelete, Path: "/routes/{id}",
		Summary: "Delete a routing rule", Tags: []string{"routes"},
		DefaultStatus: http.StatusNoContent, Middlewares: authed, Security: authSec,
	}, s.deleteRoute)
	huma.Register(s.api, huma.Operation{
		OperationID: "applyRoutes", Method: http.MethodPost, Path: "/routes/apply",
		Summary: "Apply the rule set to the OS", Tags: []string{"routes"}, Middlewares: authed, Security: authSec,
	}, s.applyRoutes)

	// --- apply transaction (M1.3) ---
	huma.Register(s.api, huma.Operation{
		OperationID: "applyConfig", Method: http.MethodPost, Path: "/apply",
		Summary: "Apply staged configuration with an automatic revert", Tags: []string{"apply"},
		Middlewares: authed, Security: authSec,
	}, s.applyConfig)
	huma.Register(s.api, huma.Operation{
		OperationID: "confirmApply", Method: http.MethodPost, Path: "/apply/confirm",
		Summary: "Confirm that the panel survived the change", Tags: []string{"apply"},
		Middlewares: authed, Security: authSec,
	}, s.confirmApply)
	huma.Register(s.api, huma.Operation{
		OperationID: "revertApply", Method: http.MethodPost, Path: "/apply/revert",
		Summary: "Undo the pending change immediately", Tags: []string{"apply"},
		Middlewares: authed, Security: authSec,
	}, s.revertApply)
	huma.Register(s.api, huma.Operation{
		OperationID: "applyState", Method: http.MethodGet, Path: "/apply",
		Summary: "State of the apply transaction", Tags: []string{"apply"},
		Middlewares: authed, Security: authSec,
	}, s.applyState)

	// --- capabilities (M1.6, D-17) ---
	huma.Register(s.api, huma.Operation{
		OperationID: "getCapabilities", Method: http.MethodGet, Path: "/capabilities",
		Summary: "What this device can do, and why not when it cannot",
		Tags:    []string{"system"}, Middlewares: authed, Security: authSec,
	}, s.getCapabilities)

	// --- live layer (M2.2, D-12/D-13) ---
	s.registerEvents(authed, authSec)
	huma.Register(s.api, huma.Operation{
		OperationID: "getMetrics", Method: http.MethodGet, Path: "/system/metrics",
		Summary: "CPU/memory/storage history kept in RAM",
		Tags:    []string{"system"}, Middlewares: authed, Security: authSec,
	}, s.getMetrics)

	// --- network (M1.5) ---
	huma.Register(s.api, huma.Operation{
		OperationID: "listInterfaces", Method: http.MethodGet, Path: "/network/interfaces",
		Summary: "L3 interfaces as the router's network daemon sees them",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.listInterfaces)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageWAN", Method: http.MethodPut, Path: "/network/wan",
		Summary: "Stage a new uplink configuration (does not apply it)",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.stageWAN)
	huma.Register(s.api, huma.Operation{
		OperationID: "stagedChanges", Method: http.MethodGet, Path: "/apply/changes",
		Summary: "Configuration edits staged but not yet applied",
		Tags:    []string{"apply"}, Middlewares: authed, Security: authSec,
	}, s.stagedChanges)
	huma.Register(s.api, huma.Operation{
		OperationID: "discardStaged", Method: http.MethodDelete, Path: "/apply/changes",
		Summary: "Throw away the staged draft without touching the device",
		Tags:    []string{"apply"}, DefaultStatus: http.StatusNoContent,
		Middlewares: authed, Security: authSec,
	}, s.discardStaged)
	huma.Register(s.api, huma.Operation{
		OperationID: "getWAN", Method: http.MethodGet, Path: "/network/wan",
		Summary: "The uplink, and which rule identified it",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.getWAN)
	huma.Register(s.api, huma.Operation{
		OperationID: "getLAN", Method: http.MethodGet, Path: "/network/lan",
		Summary: "The local network, its address handout and its clients",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.getLAN)
	huma.Register(s.api, huma.Operation{
		OperationID: "getFirewall", Method: http.MethodGet, Path: "/firewall",
		Summary: "Zones, what they allow, forwarded ports and traffic rules",
		Tags:    []string{"firewall"}, Middlewares: authed, Security: authSec,
	}, s.getFirewall)
	huma.Register(s.api, huma.Operation{
		OperationID: "stagePortForward", Method: http.MethodPut, Path: "/firewall/port-forwards",
		Summary: "Stage a port forward, new or edited (does not apply it)",
		Tags:    []string{"firewall"}, Middlewares: authed, Security: authSec,
	}, s.stagePortForward)
	huma.Register(s.api, huma.Operation{
		OperationID: "removePortForward", Method: http.MethodDelete,
		Path:    "/firewall/port-forwards/{id}",
		Summary: "Stage the removal of a port forward (does not apply it)",
		Tags:    []string{"firewall"}, Middlewares: authed, Security: authSec,
	}, s.removePortForward)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageFirewallRule", Method: http.MethodPut, Path: "/firewall/rules",
		Summary: "Stage a traffic rule of the owner's, new or edited (does not apply it)",
		Tags:    []string{"firewall"}, Middlewares: authed, Security: authSec,
	}, s.stageFirewallRule)
	huma.Register(s.api, huma.Operation{
		OperationID: "removeFirewallRule", Method: http.MethodDelete,
		Path:    "/firewall/rules/{id}",
		Summary: "Stage the removal of a traffic rule of the owner's (does not apply it)",
		Tags:    []string{"firewall"}, Middlewares: authed, Security: authSec,
	}, s.removeFirewallRule)
	huma.Register(s.api, huma.Operation{
		OperationID: "moveFirewallRule", Method: http.MethodPost,
		Path:    "/firewall/rules/{id}/move",
		Summary: "Stage moving a traffic rule of the owner's in the list (does not apply it)",
		Tags:    []string{"firewall"}, Middlewares: authed, Security: authSec,
	}, s.moveFirewallRule)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageLAN", Method: http.MethodPut, Path: "/network/lan",
		Summary: "Stage this router's own address on the local network (does not apply it)",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.stageLAN)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageHandout", Method: http.MethodPut, Path: "/network/lan/handout",
		Summary: "Stage the address handout (does not apply it)",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.stageHandout)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageReservation", Method: http.MethodPut, Path: "/network/lan/reservations",
		Summary: "Stage an address reserved for one device (does not apply it)",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.stageReservation)
	huma.Register(s.api, huma.Operation{
		OperationID: "removeReservation", Method: http.MethodDelete,
		Path:    "/network/lan/reservations/{id}",
		Summary: "Stage the removal of a reserved address (does not apply it)",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.removeReservation)
	huma.Register(s.api, huma.Operation{
		OperationID: "getStaticRoutes", Method: http.MethodGet, Path: "/network/routes",
		Summary: "Static routes, each with whether the kernel is using it, and the connections a route can use",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.getStaticRoutes)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageStaticRoute", Method: http.MethodPut, Path: "/network/routes",
		Summary: "Stage an IPv4 static route, new or edited (does not apply it)",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.stageStaticRoute)
	huma.Register(s.api, huma.Operation{
		OperationID: "removeStaticRoute", Method: http.MethodDelete,
		Path:    "/network/routes/{id}",
		Summary: "Stage the removal of a static route (does not apply it)",
		Tags:    []string{"network"}, Middlewares: authed, Security: authSec,
	}, s.removeStaticRoute)

	// --- system ---
	huma.Register(s.api, huma.Operation{
		OperationID: "getSystem", Method: http.MethodGet, Path: "/system",
		Summary: "System snapshot for the dashboard", Tags: []string{"system"}, Middlewares: authed, Security: authSec,
	}, s.getSystem)
	huma.Register(s.api, huma.Operation{
		OperationID: "probePath", Method: http.MethodPost, Path: "/system/probe",
		Summary: "Probe whether traffic goes via tunnel or direct", Tags: []string{"system"}, Middlewares: authed, Security: authSec,
	}, s.probePath)
	huma.Register(s.api, huma.Operation{
		OperationID: "diagnostics", Method: http.MethodGet, Path: "/system/diagnostics",
		Summary: "ping/traceroute from the agent", Tags: []string{"system"}, Middlewares: authed, Security: authSec,
	}, s.diagnostics)

	// --- config backup/restore ---
	huma.Register(s.api, huma.Operation{
		OperationID: "exportConfig", Method: http.MethodGet, Path: "/config/export",
		Summary: "Export the full config (includes secrets)", Tags: []string{"config"}, Middlewares: authed, Security: authSec,
	}, s.exportConfig)
	huma.Register(s.api, huma.Operation{
		OperationID: "importConfig", Method: http.MethodPost, Path: "/config/import",
		Summary: "Import and apply a backup", Tags: []string{"config"}, Middlewares: authed, Security: authSec,
	}, s.importConfig)
}

// --- handlers ---

func (s *Server) login(ctx context.Context, in *LoginInput) (*LoginOutput, error) {
	doc, err := s.store.Load()
	if err != nil {
		return nil, huma.Error500InternalServerError("load config", err)
	}
	if !doc.VerifyPassword(in.Body.Password) {
		return nil, huma.Error401Unauthorized("invalid password")
	}
	// Re-derive the issuer in case the password changed since New.
	s.tokens = newTokenIssuer(doc.Settings.PasswordHash)
	tok, exp, err := s.tokens.issue()
	if err != nil {
		return nil, huma.Error500InternalServerError("issue token", err)
	}
	out := &LoginOutput{}
	out.Body.Token = tok
	out.Body.ExpiresAt = exp.Format(time.RFC3339)
	return out, nil
}

func (s *Server) listNodes(ctx context.Context, _ *struct{}) (*NodesOutput, error) {
	nodes, err := s.adapter.VPN().ListNodes()
	if err != nil {
		return nil, huma.Error500InternalServerError("list nodes", err)
	}
	out := &NodesOutput{Body: make([]core.NodeWithStatus, 0, len(nodes))}
	for _, n := range nodes {
		nw := core.NodeWithStatus{Node: n}
		if st, err := s.adapter.VPN().Status(n.ID); err == nil {
			nw.Status = &st
		}
		out.Body = append(out.Body, nw)
	}
	return out, nil
}

func (s *Server) importNodes(ctx context.Context, in *ImportInput) (*ImportOutput, error) {
	var (
		nodes []core.Node
		err   error
	)
	switch in.Body.Kind {
	case "awg-config":
		nodes, err = s.adapter.VPN().ImportConfig([]byte(in.Body.Content))
	case "subscription":
		nodes, err = s.adapter.VPN().ImportSubscription(in.Body.URL)
	default:
		return nil, huma.Error400BadRequest("unknown import kind: " + in.Body.Kind)
	}
	if err != nil {
		return nil, huma.Error400BadRequest("import failed", err)
	}
	return &ImportOutput{Body: nodes}, nil
}

func (s *Server) removeNode(ctx context.Context, in *NodeIDInput) (*struct{}, error) {
	if err := s.adapter.VPN().RemoveNode(in.ID); err != nil {
		return nil, huma.Error404NotFound("remove node", err)
	}
	return nil, nil
}

// getCapabilities reports the detected hardware capabilities. An adapter that
// cannot probe (the mock in a unit test) yields an empty set rather than an
// error: the honest answer to "what can this device do" is then "nothing I can
// vouch for", and the UI degrades to its safe subset instead of showing a
// failure the operator cannot fix.
func (s *Server) getCapabilities(_ context.Context, _ *struct{}) (*CapabilitiesOutput, error) {
	caps := core.Capabilities{}
	if probe, ok := s.adapter.(core.CapabilityProbe); ok {
		if detected := probe.Capabilities(); detected != nil {
			caps = detected
		}
	}
	return &CapabilitiesOutput{Body: caps}, nil
}

func (s *Server) activateNode(ctx context.Context, in *NodeIDInput) (*SystemOutput, error) {
	if err := s.adapter.VPN().Activate(in.ID); err != nil {
		return nil, huma.Error502BadGateway("activate node", err)
	}
	info, err := s.adapter.System().Info()
	if err != nil {
		return nil, huma.Error500InternalServerError("system info", err)
	}
	return &SystemOutput{Body: info}, nil
}

func (s *Server) nodeStatus(ctx context.Context, in *NodeIDInput) (*NodeStatusOutput, error) {
	st, err := s.adapter.VPN().Status(in.ID)
	if err != nil {
		return nil, huma.Error404NotFound("node status", err)
	}
	return &NodeStatusOutput{Body: st}, nil
}

func (s *Server) listRoutes(ctx context.Context, _ *struct{}) (*RoutesOutput, error) {
	rules, err := s.adapter.Routing().ListRules()
	if err != nil {
		return nil, huma.Error500InternalServerError("list routes", err)
	}
	return &RoutesOutput{Body: rules}, nil
}

func (s *Server) addRoute(ctx context.Context, in *RuleInput) (*RuleOutput, error) {
	rules, err := s.adapter.Routing().ListRules()
	if err != nil {
		return nil, huma.Error500InternalServerError("list routes", err)
	}
	rule := in.Body.toRule(newRuleID(rules))
	if err := s.adapter.Routing().SetRules(append(rules, rule)); err != nil {
		return nil, huma.Error400BadRequest("add route", err)
	}
	return &RuleOutput{Body: rule}, nil
}

func (s *Server) updateRoute(ctx context.Context, in *RuleIDInput) (*RuleOutput, error) {
	rules, err := s.adapter.Routing().ListRules()
	if err != nil {
		return nil, huma.Error500InternalServerError("list routes", err)
	}
	updated := in.Body.toRule(in.ID)
	found := false
	for i := range rules {
		if rules[i].ID == in.ID {
			rules[i] = updated
			found = true
			break
		}
	}
	if !found {
		return nil, huma.Error404NotFound("rule not found: " + in.ID)
	}
	if err := s.adapter.Routing().SetRules(rules); err != nil {
		return nil, huma.Error400BadRequest("update route", err)
	}
	return &RuleOutput{Body: updated}, nil
}

func (s *Server) deleteRoute(ctx context.Context, in *NodeIDInput) (*struct{}, error) {
	rules, err := s.adapter.Routing().ListRules()
	if err != nil {
		return nil, huma.Error500InternalServerError("list routes", err)
	}
	out := rules[:0]
	found := false
	for _, r := range rules {
		if r.ID == in.ID {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return nil, huma.Error404NotFound("rule not found: " + in.ID)
	}
	if err := s.adapter.Routing().SetRules(out); err != nil {
		return nil, huma.Error500InternalServerError("delete route", err)
	}
	return nil, nil
}

// applyConfig starts a transaction: snapshot, commit, watchdog. The response
// carries the token and the deadline, so the UI can show a countdown and the
// operator knows how long they have to confirm.
func (s *Server) applyConfig(_ context.Context, in *ApplyTxInput) (*ApplyStateOutput, error) {
	timeout := defaultApplyTimeout
	if in.Body.TimeoutSeconds > 0 {
		timeout = time.Duration(in.Body.TimeoutSeconds) * time.Second
	}
	timeout = min(timeout, maxApplyTimeout)

	st, err := s.apply.Apply(timeout)
	out := &ApplyStateOutput{Body: st}
	switch {
	case err == nil:
		return out, nil
	case errors.Is(err, core.ErrApplyInFlight):
		// 409: the fix is to confirm or revert the pending one, not to retry.
		return nil, huma.Error409Conflict(err.Error())
	default:
		// The change did not take effect, and the state says whether the
		// device was restored — pass both on instead of a bare 500.
		return nil, huma.Error500InternalServerError(err.Error())
	}
}

func (s *Server) confirmApply(_ context.Context, in *ApplyConfirmInput) (*ApplyStateOutput, error) {
	st, err := s.apply.Confirm(in.Body.Token)
	switch {
	case err == nil:
		return &ApplyStateOutput{Body: st}, nil
	case errors.Is(err, core.ErrNoApplyInFlight):
		// 409 and not 404: a confirm that arrives after the automatic revert
		// is a timing conflict, and the state in the body says what happened.
		return nil, huma.Error409Conflict(err.Error())
	case errors.Is(err, core.ErrWrongToken):
		return nil, huma.Error409Conflict(err.Error())
	default:
		return nil, huma.Error500InternalServerError(err.Error())
	}
}

func (s *Server) revertApply(_ context.Context, _ *struct{}) (*ApplyStateOutput, error) {
	st, err := s.apply.Revert()
	switch {
	case err == nil:
		return &ApplyStateOutput{Body: st}, nil
	case errors.Is(err, core.ErrNoApplyInFlight):
		return nil, huma.Error409Conflict(err.Error())
	default:
		return nil, huma.Error500InternalServerError(err.Error())
	}
}

func (s *Server) applyState(_ context.Context, _ *struct{}) (*ApplyStateOutput, error) {
	return &ApplyStateOutput{Body: s.apply.State()}, nil
}

// RecoverPendingApply undoes a transaction left unconfirmed by a previous run
// of the daemon. cmd/veilbridged calls it at startup, before serving: a change
// nobody confirmed must not survive just because the process died.
func (s *Server) RecoverPendingApply() (bool, error) {
	loader, ok := s.adapter.Applier().(core.SnapshotLoader)
	if !ok {
		return false, nil
	}
	return s.apply.RecoverPending(loader)
}

func (s *Server) applyRoutes(ctx context.Context, _ *struct{}) (*ApplyOutput, error) {
	out := &ApplyOutput{}
	if err := s.adapter.Routing().Apply(); err != nil {
		out.Body.OK = false
		out.Body.Detail = err.Error()
		return nil, huma.Error502BadGateway("apply routes", err)
	}
	out.Body.OK = true
	return out, nil
}

func (s *Server) getSystem(ctx context.Context, _ *struct{}) (*SystemOutput, error) {
	info, err := s.adapter.System().Info()
	if err != nil {
		return nil, huma.Error500InternalServerError("system info", err)
	}
	return &SystemOutput{Body: info}, nil
}

func (s *Server) listInterfaces(ctx context.Context, _ *struct{}) (*InterfacesOutput, error) {
	ifaces, err := s.adapter.Network().Interfaces()
	if errors.Is(err, core.ErrNotImplemented) {
		return nil, huma.Error501NotImplemented("interfaces", err)
	}
	if err != nil {
		// 502: the panel is fine, the thing it asked (netifd) is not.
		return nil, huma.Error502BadGateway("interfaces", err)
	}
	// An empty list must serialise as [] and not null, so a UI that iterates
	// the response renders "nothing here" instead of throwing.
	if ifaces == nil {
		ifaces = []core.NetworkInterface{}
	}
	return &InterfacesOutput{Body: ifaces}, nil
}

func (s *Server) getWAN(ctx context.Context, _ *struct{}) (*WANOutput, error) {
	wan, err := s.adapter.Network().WANInfo()
	switch {
	case errors.Is(err, core.ErrNoWAN):
		// 404, not 500: the device answered, and the answer is "there is no
		// uplink here". An unconfigured router is legitimately in that state.
		return nil, huma.Error404NotFound("no wan interface", err)
	case errors.Is(err, core.ErrNotImplemented):
		return nil, huma.Error501NotImplemented("wan", err)
	case err != nil:
		return nil, huma.Error502BadGateway("wan", err)
	}
	return &WANOutput{Body: wan}, nil
}

// writer returns the adapter's network writer, if this platform has one.
// Writing is an optional capability of the adapter, asked for rather than
// required: an adapter that cannot change configuration must be able to say
// so instead of panicking at the first PUT.
// getLAN answers the local-network screen. Reading the local network is an
// optional capability of an adapter: a platform that cannot answer says so
// with 501 rather than pretending the device has no LAN, because "no local
// network here" is a fact about hardware and must not be faked by software
// that simply has not been written (D-20).
func (s *Server) getLAN(_ context.Context, _ *struct{}) (*LANOutput, error) {
	reader, ok := s.adapter.Network().(core.LANReader)
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot read the local network")
	}
	lan, err := reader.LANInfo()
	switch {
	case errors.Is(err, core.ErrNoLAN):
		// 404, not 500: the device answered, and the answer is "there is no
		// local network on this box".
		return nil, huma.Error404NotFound("no lan interface", err)
	case errors.Is(err, core.ErrNotImplemented):
		return nil, huma.Error501NotImplemented("lan", err)
	case err != nil:
		return nil, huma.Error502BadGateway("lan", err)
	}
	return &LANOutput{Body: lan}, nil
}

// getFirewall answers the firewall screen. Like the local network it is an
// optional capability of the adapter: 501 says "this build cannot read it",
// which must not look like a device without a firewall.
func (s *Server) getFirewall(_ context.Context, _ *struct{}) (*FirewallOutput, error) {
	reader, ok := s.adapter.Network().(core.FirewallReader)
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot read the firewall")
	}
	fw, err := reader.FirewallInfo()
	switch {
	case errors.Is(err, core.ErrNotImplemented):
		return nil, huma.Error501NotImplemented("firewall", err)
	case err != nil:
		return nil, huma.Error502BadGateway("firewall", err)
	}
	return &FirewallOutput{Body: fw}, nil
}

func (s *Server) firewallWriter() (core.FirewallWriter, bool) {
	w, ok := s.adapter.Network().(core.FirewallWriter)
	return w, ok
}

func (s *Server) stagePortForward(_ context.Context, in *StagePortForwardInput) (*ChangesOutput, error) {
	w, ok := s.firewallWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the firewall")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.StagePortForward(in.Body)
	return stagedOr("staging a port forward", changes, err)
}

func (s *Server) removePortForward(_ context.Context, in *RemovePortForwardInput) (*ChangesOutput, error) {
	w, ok := s.firewallWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the firewall")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.RemovePortForward(in.ID)
	return stagedOr("removing a port forward", changes, err)
}

func (s *Server) stageFirewallRule(_ context.Context, in *StageFirewallRuleInput) (*ChangesOutput, error) {
	w, ok := s.firewallWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the firewall")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.StageRule(in.Body)
	return stagedOr("staging a firewall rule", changes, err)
}

func (s *Server) removeFirewallRule(_ context.Context, in *RemoveFirewallRuleInput) (*ChangesOutput, error) {
	w, ok := s.firewallWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the firewall")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.RemoveRule(in.ID)
	return stagedOr("removing a firewall rule", changes, err)
}

func (s *Server) moveFirewallRule(_ context.Context, in *MoveFirewallRuleInput) (*ChangesOutput, error) {
	w, ok := s.firewallWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the firewall")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.MoveRule(in.ID, in.Body.Before)
	return stagedOr("moving a firewall rule", changes, err)
}

// getStaticRoutes answers the routes screen (#37). Optional like the firewall:
// 501 says "this build cannot read routes", not "there are none".
func (s *Server) getStaticRoutes(_ context.Context, _ *struct{}) (*StaticRoutesOutput, error) {
	reader, ok := s.adapter.Network().(core.RouteReader)
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot read static routes")
	}
	st, err := reader.StaticRoutes()
	switch {
	case errors.Is(err, core.ErrNotImplemented):
		return nil, huma.Error501NotImplemented("static routes", err)
	case err != nil:
		return nil, huma.Error502BadGateway("static routes", err)
	}
	return &StaticRoutesOutput{Body: st}, nil
}

func (s *Server) stageStaticRoute(_ context.Context, in *StageStaticRouteInput) (*ChangesOutput, error) {
	w, ok := s.adapter.Network().(core.RouteWriter)
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change static routes")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.StageStaticRoute(in.Body)
	return stagedOr("staging a static route", changes, err)
}

func (s *Server) removeStaticRoute(_ context.Context, in *RemoveStaticRouteInput) (*ChangesOutput, error) {
	w, ok := s.adapter.Network().(core.RouteWriter)
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change static routes")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.RemoveStaticRoute(in.ID)
	return stagedOr("removing a static route", changes, err)
}

// lanWriter is the local-network half of the writer. Like LANReader it is an
// optional capability of the adapter, asked for by type assertion.
func (s *Server) lanWriter() (core.LANWriter, bool) {
	w, ok := s.adapter.Network().(core.LANWriter)
	return w, ok
}

// stagedOr maps a staging result onto the answers the panel reacts to. A
// rejected value is the caller's mistake and its reason has to land where the
// client reads it (`detail`), or the operator is told "staging failed" and
// nothing about which field was wrong.
func stagedOr(what string, changes []core.ConfigChange, err error) (*ChangesOutput, error) {
	switch {
	case errors.Is(err, core.ErrNotImplemented):
		return nil, huma.Error501NotImplemented(what, err)
	case errors.Is(err, core.ErrNoLAN):
		return nil, huma.Error404NotFound("no lan interface", err)
	case err != nil:
		return nil, refusal(err)
	}
	return changesOutput(changes), nil
}

// refuseWhileApplying keeps a second draft off a live transaction: the
// operator would otherwise confirm two edits having reviewed one, and the
// snapshot would roll both back.
func (s *Server) refuseWhileApplying() error {
	if st := s.apply.State(); st.Phase == core.PhaseAwaitingConfirm {
		return huma.Error409Conflict("a change is already waiting for confirmation")
	}
	return nil
}

func (s *Server) stageLAN(_ context.Context, in *StageLANInput) (*ChangesOutput, error) {
	w, ok := s.lanWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the local network")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.StageLAN(in.Body)
	return stagedOr("staging the local network", changes, err)
}

func (s *Server) stageHandout(_ context.Context, in *StageHandoutInput) (*ChangesOutput, error) {
	w, ok := s.lanWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the address handout")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.StageHandout(in.Body)
	return stagedOr("staging the address handout", changes, err)
}

func (s *Server) stageReservation(_ context.Context, in *StageReservationInput) (*ChangesOutput, error) {
	w, ok := s.lanWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot reserve addresses")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.StageReservation(in.Body)
	return stagedOr("staging a reserved address", changes, err)
}

func (s *Server) removeReservation(_ context.Context, in *RemoveReservationInput) (*ChangesOutput, error) {
	w, ok := s.lanWriter()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot reserve addresses")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := w.RemoveReservation(in.ID)
	return stagedOr("staging the removal of a reserved address", changes, err)
}

func (s *Server) writer() (core.NetworkWriter, bool) {
	w, ok := s.adapter.Network().(core.NetworkWriter)
	return w, ok
}

func (s *Server) stageWAN(_ context.Context, in *StageWANInput) (*ChangesOutput, error) {
	w, ok := s.writer()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the uplink")
	}
	// A change may not be staged on top of one that is already live and
	// waiting to be confirmed: the operator would then confirm two edits
	// having reviewed one, and the snapshot would roll back both.
	if st := s.apply.State(); st.Phase == core.PhaseAwaitingConfirm {
		return nil, huma.Error409Conflict("a change is already waiting for confirmation")
	}
	changes, err := w.StageWAN(in.Body)
	if err != nil {
		if errors.Is(err, core.ErrNotImplemented) {
			return nil, huma.Error501NotImplemented("staging", err)
		}
		// A rejected value is the caller's mistake, and the message has to
		// name it where the panel actually reads it: clients show `detail`,
		// so burying the reason in the errors array means the operator is
		// told "stage uplink" and nothing else.
		return nil, refusal(err)
	}
	return changesOutput(changes), nil
}

func (s *Server) stagedChanges(_ context.Context, _ *struct{}) (*ChangesOutput, error) {
	w, ok := s.writer()
	if !ok {
		return changesOutput(nil), nil
	}
	changes, err := w.StagedChanges()
	if err != nil {
		if errors.Is(err, core.ErrNotImplemented) {
			return changesOutput(nil), nil
		}
		return nil, huma.Error502BadGateway("staged changes", err)
	}
	return changesOutput(changes), nil
}

func (s *Server) discardStaged(_ context.Context, _ *struct{}) (*struct{}, error) {
	w, ok := s.writer()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot change the uplink")
	}
	if err := w.DiscardStaged(); err != nil && !errors.Is(err, core.ErrNotImplemented) {
		return nil, huma.Error502BadGateway("discard draft", err)
	}
	return nil, nil
}

// reasonFor turns an adapter error into a sentence for the panel. The adapter
// prefixes its errors with its own package name, which is useful in a log and
// noise in a dialog.
// refusal answers a rejected value with 400. When the device said which field
// it was about, that goes into the standard `errors[].location` as
// `body.<field>`, so a screen places the refusal next to its input without
// reading the sentence (#28); the sentence stays in `detail` for people.
func refusal(err error) error {
	reason := reasonFor(err)
	var fe *core.FieldError
	if errors.As(err, &fe) {
		return huma.Error400BadRequest(reason, &huma.ErrorDetail{
			Message:  reason,
			Location: "body." + fe.Field,
		})
	}
	return huma.Error400BadRequest(reason)
}

func reasonFor(err error) string {
	return strings.TrimPrefix(err.Error(), "openwrt: ")
}

func changesOutput(changes []core.ConfigChange) *ChangesOutput {
	out := &ChangesOutput{}
	// [] and not null: the apply bar iterates this to decide whether it has
	// anything to show.
	out.Body.Changes = changes
	if out.Body.Changes == nil {
		out.Body.Changes = []core.ConfigChange{}
	}
	for _, c := range changes {
		if c.Dangerous {
			out.Body.Dangerous = true
		}
	}
	return out
}

func (s *Server) probePath(ctx context.Context, in *ProbeInput) (*ProbeOutput, error) {
	probe, err := s.adapter.System().ProbePath(in.Body.Target, in.Body.ExpectedVia)
	if err != nil {
		return nil, huma.Error400BadRequest("probe", err)
	}
	return &ProbeOutput{Body: probe}, nil
}

func (s *Server) diagnostics(ctx context.Context, in *DiagnosticsInput) (*DiagnosticsOutput, error) {
	out, err := s.adapter.System().Diagnostics(in.Target)
	if err != nil {
		return nil, huma.Error400BadRequest("diagnostics", err)
	}
	res := &DiagnosticsOutput{}
	res.Body.Target = in.Target
	res.Body.Output = out
	return res, nil
}

func (s *Server) exportConfig(ctx context.Context, _ *struct{}) (*ConfigOutput, error) {
	doc, err := s.store.Load()
	if err != nil {
		return nil, huma.Error500InternalServerError("export config", err)
	}
	return &ConfigOutput{Body: doc}, nil
}

func (s *Server) importConfig(ctx context.Context, in *ConfigImportInput) (*ApplyOutput, error) {
	var doc config.Document
	if err := json.Unmarshal(in.RawBody, &doc); err != nil {
		return nil, huma.Error400BadRequest("parse config", err)
	}
	if err := s.store.Save(&doc); err != nil {
		return nil, huma.Error500InternalServerError("save config", err)
	}
	out := &ApplyOutput{}
	out.Body.OK = true
	return out, nil
}

// newRuleID returns a short unique rule ID not already used.
func newRuleID(existing []core.RouteRule) string {
	used := make(map[string]bool, len(existing))
	for _, r := range existing {
		used[r.ID] = true
	}
	for i := 1; ; i++ {
		id := "rule-" + strconv.Itoa(i)
		if !used[id] {
			return id
		}
	}
}
