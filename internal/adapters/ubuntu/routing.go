package ubuntu

import (
	"context"
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
	gen   routing.Generator
}

func newRoutingManager(store *config.Store) *routingManager {
	return &routingManager{store: store, gen: routing.Generator{}}
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
	if _, err := m.gen.Render(rules); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return m.gen.Apply(ctx, doc.Routes)
}
