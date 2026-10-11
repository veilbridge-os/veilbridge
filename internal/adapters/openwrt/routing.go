package openwrt

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/routing"
)

// routingManager persists rules in the config store and renders/applies them via
// internal/routing (nftables). SetRules replaces the whole set declaratively (D-4).
type routingManager struct {
	mu    sync.Mutex
	store *config.Store
	// lan names the local network's interfaces, the only ones whose traffic
	// is marked (#47). Nil in tests that only validate rules.
	lan func() ([]string, error)
	// retry is how long applyUntilDone waits between attempts, and attempt
	// what it repeats (Apply when nil); tests change both.
	retry   time.Duration
	attempt func() error
	// ensure puts the tunnel's routing rules in place, the refusal included.
	// Marking without them sends marked traffic out by the uplink, so Apply
	// marks nothing unless it succeeds (#47). Nil: no tunnel interface.
	ensure func() error
}

func newRoutingManager(store *config.Store) *routingManager {
	return &routingManager{store: store, retry: 30 * time.Second}
}

// applyUntilDone repeats Apply until it succeeds. At start, a failed attempt
// (the interfaces not up yet, nft busy) would otherwise leave the saved rules
// unmarked for good, and the traffic they send to the tunnel leaving by the
// uplink once the tunnel is up (#47). Applying is idempotent, so a person's
// own apply in between does no harm.
func (m *routingManager) applyUntilDone() {
	attempt := m.attempt
	if attempt == nil {
		attempt = m.Apply
	}
	for {
		err := attempt()
		if err == nil {
			return
		}
		log.Printf("routing: could not apply the routing rules, retrying in %s: %v", m.retry, err)
		time.Sleep(m.retry)
	}
}

// gen builds the generator for the live router. Validation does not need the
// interfaces, so a placeholder stands in when only checking rules.
func (m *routingManager) gen(validateOnly bool) (routing.Generator, error) {
	if validateOnly || m.lan == nil {
		return routing.Generator{Ingress: []string{"lan"}}, nil
	}
	ifaces, err := m.lan()
	if err != nil {
		return routing.Generator{}, err
	}
	return routing.Generator{Ingress: ifaces}, nil
}

func (m *routingManager) ListRules() ([]core.RouteRule, error) {
	doc, err := m.store.Load()
	if err != nil {
		return nil, err
	}
	return doc.Routes, nil
}

func (m *routingManager) SetRules(rules []core.RouteRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Validate by rendering before persisting — a bad CIDR fails here, not on Apply.
	g, _ := m.gen(true)
	if _, err := g.Render(rules); err != nil {
		return err
	}
	doc, err := m.store.Load()
	if err != nil {
		return err
	}
	doc.Routes = rules
	return m.store.Save(doc)
}

func (m *routingManager) Apply() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.store.Load()
	if err != nil {
		return err
	}
	if m.ensure != nil {
		if err := m.ensure(); err != nil {
			return fmt.Errorf("openwrt: the tunnel's routing rules are not in place, so nothing is marked: %w", err)
		}
	}
	g, err := m.gen(false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return g.Apply(ctx, doc.Routes)
}
