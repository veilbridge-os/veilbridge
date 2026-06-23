// Package ubuntu implements the core managers for Ubuntu/Debian hosts: userspace
// AmneziaWG (netstack engine), nftables routing, and /proc-based system info.
// Selected by adapters.Detect() when /etc/openwrt_release is absent. See
// CONTRIBUTING.md §5 and the project history Phase 5.
package ubuntu

import (
	"fmt"
	"sync"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
	"github.com/veilbridge-os/veilbridge/internal/vpn/amneziawg"
)

// vpnManager persists nodes in the config store and drives the active tunnel
// through a single userspace engine. One node is active at a time (v0.1).
type vpnManager struct {
	mu     sync.Mutex
	store  *config.Store
	engine vpn.Engine
	// up tracks the ID of the node the engine is currently running, "" if down.
	up string
}

func newVPNManager(store *config.Store, engine vpn.Engine) *vpnManager {
	return &vpnManager{store: store, engine: engine}
}

func (m *vpnManager) ImportConfig(raw []byte) ([]core.Node, error) {
	node, sec, err := amneziawg.Parse(defaultName(raw), raw)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.store.Load()
	if err != nil {
		return nil, err
	}
	// Re-import of the same node (same ID) replaces its secret rather than
	// duplicating — IDs are derived from the peer key (stable).
	replaced := false
	for i := range doc.Nodes {
		if doc.Nodes[i].ID == node.ID {
			doc.Nodes[i] = config.StoredNode{Node: node, Secret: sec}
			replaced = true
			break
		}
	}
	if !replaced {
		doc.Nodes = append(doc.Nodes, config.StoredNode{Node: node, Secret: sec})
	}
	if err := m.store.Save(doc); err != nil {
		return nil, err
	}
	return []core.Node{node}, nil
}

func (m *vpnManager) ImportSubscription(url string) ([]core.Node, error) {
	// Subscription fetch/parse is v0.2 (needs the multi-node format). Stub now.
	return nil, fmt.Errorf("ubuntu: subscription import not implemented in v0.1: %w", core.ErrNotImplemented)
}

func (m *vpnManager) ListNodes() ([]core.Node, error) {
	doc, err := m.store.Load()
	if err != nil {
		return nil, err
	}
	return doc.PublicNodes(), nil
}

func (m *vpnManager) RemoveNode(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.store.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i := range doc.Nodes {
		if doc.Nodes[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("ubuntu: node %q not found", id)
	}
	// If the node being removed is active, tear the tunnel down first.
	if m.up == id {
		_ = m.engine.Down()
		m.up = ""
		doc.Settings.ActiveNodeID = ""
	}
	doc.Nodes = append(doc.Nodes[:idx], doc.Nodes[idx+1:]...)
	return m.store.Save(doc)
}

func (m *vpnManager) Activate(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.store.Load()
	if err != nil {
		return err
	}
	var sn *config.StoredNode
	for i := range doc.Nodes {
		if doc.Nodes[i].ID == id {
			sn = &doc.Nodes[i]
			break
		}
	}
	if sn == nil {
		return fmt.Errorf("ubuntu: node %q not found", id)
	}
	// Bring the current tunnel down before switching (one engine, one tunnel).
	if m.up != "" {
		_ = m.engine.Down()
		m.up = ""
	}
	cfg := amneziawg.ToNodeConfig(sn.Endpoint, sn.Secret)
	if err := m.engine.Up(cfg); err != nil {
		return fmt.Errorf("ubuntu: activate %q: %w", id, err)
	}
	m.up = id
	doc.Settings.ActiveNodeID = id
	return m.store.Save(doc)
}

func (m *vpnManager) Status(id string) (core.NodeStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.store.Load()
	if err != nil {
		return core.NodeStatus{}, err
	}
	found := false
	for i := range doc.Nodes {
		if doc.Nodes[i].ID == id {
			found = true
			break
		}
	}
	if !found {
		return core.NodeStatus{}, fmt.Errorf("ubuntu: node %q not found", id)
	}
	st := core.NodeStatus{NodeID: id, Active: m.up == id, HandshakeAgeSec: -1}
	if m.up == id {
		s, err := m.engine.Stats()
		if err != nil {
			st.LastError = err.Error()
			return st, nil
		}
		st.HandshakeAgeSec = s.HandshakeAgeSec
		st.RxBytes = s.RxBytes
		st.TxBytes = s.TxBytes
	}
	return st, nil
}

// activeNodeID returns the ID of the running node (for the system manager).
func (m *vpnManager) activeNodeID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.up
}

// activeDialer returns the active tunnel's dialer (nil if down or kernel engine).
func (m *vpnManager) activeDialer() vpn.Dialer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.up == "" {
		return nil
	}
	d, err := m.engine.Dialer()
	if err != nil {
		return nil
	}
	return d
}
