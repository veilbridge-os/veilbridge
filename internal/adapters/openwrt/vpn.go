// vpnManager owns the node inventory and the active tunnel: it parses imported
// .conf files, persists nodes and secrets through the config store, and drives
// the injected engine. See the package doc in adapter.go.
package openwrt

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

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
	// router routes a kernel engine's interface (#47); nil when the engine
	// has no interface to route.
	router connectionRouter
	// up tracks the ID of the node the engine is currently running, "" if down.
	up string
	// retry is how long bringBack waits between attempts; tests shorten it.
	retry time.Duration
}

// connectionRouter is what vpnManager needs from tunnelRouter; tests fake it.
type connectionRouter interface {
	Ensure() error
	Up(dev string, allowed []string) error
	Down() error
}

func newVPNManager(store *config.Store, engine vpn.Engine) *vpnManager {
	return &vpnManager{store: store, engine: engine, retry: 30 * time.Second}
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
	// Subscription fetch/parse lands with exit nodes (M5, needs the multi-node
	// format). Stub now.
	return nil, fmt.Errorf("openwrt: subscription import not implemented in v0.1: %w", core.ErrNotImplemented)
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
		return fmt.Errorf("openwrt: node %q not found", id)
	}
	// If the node being removed is active, tear the tunnel down first.
	if m.up == id {
		_ = m.engine.Down()
		m.unroute()
		m.up = ""
	}
	// Chosen but not up (still being brought back after a restart) counts
	// too: a removed node must not stay the one to bring back.
	if doc.Settings.ActiveNodeID == id {
		doc.Settings.ActiveNodeID = ""
	}
	doc.Nodes = append(doc.Nodes[:idx], doc.Nodes[idx+1:]...)
	return m.store.Save(doc)
}

func (m *vpnManager) Activate(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activateLocked(id)
}

// activateLocked is Activate with m.mu already held.
func (m *vpnManager) activateLocked(id string) error {
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
		return fmt.Errorf("openwrt: node %q not found", id)
	}
	// Bring the current tunnel down before switching (one engine, one tunnel).
	if m.up != "" {
		_ = m.engine.Down()
		m.unroute()
		m.up = ""
	}
	cfg := amneziawg.ToNodeConfig(sn.Endpoint, sn.Secret)
	if err := m.engine.Up(cfg); err != nil {
		return fmt.Errorf("openwrt: activate %q: %w", id, err)
	}
	if err := m.route(cfg.AllowedIPs); err != nil {
		// A tunnel nothing routes into is the defect #47 was about; do not
		// report it as up.
		_ = m.engine.Down()
		m.unroute()
		return fmt.Errorf("openwrt: activate %q: route the tunnel: %w", id, err)
	}
	m.up = id
	// Save only the choice, on a fresh read: bringing the tunnel up can take
	// a while (DNS, routing), and writing back the document read before it
	// would undo whatever else was saved meanwhile (rules, device names).
	return m.store.Update(func(d *config.Document) error {
		d.Settings.ActiveNodeID = id
		return nil
	})
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
		return core.NodeStatus{}, fmt.Errorf("openwrt: node %q not found", id)
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

// activeTunName returns the OS interface name of the active kernel tunnel, or ""
// when down, when the userspace engine is in use (it has a Dialer instead), or
// when no node is active. Lets the system manager verify egress on OpenWrt where
// there is no Dialer to probe through.
func (m *vpnManager) activeTunName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.up == "" {
		return ""
	}
	ie, ok := m.engine.(vpn.InterfaceEngine)
	if !ok {
		return ""
	}
	return ie.TunName()
}

// route fills the connection's table for a kernel engine's interface. A
// userspace engine has no interface, so there is nothing to route.
func (m *vpnManager) route(allowed []string) error {
	ie, ok := m.engine.(vpn.InterfaceEngine)
	if m.router == nil || !ok || ie.TunName() == "" {
		return nil
	}
	return m.router.Up(ie.TunName(), allowed)
}

// ensureRules puts the tunnel's routing rules in place for a kernel engine;
// a userspace engine has no interface and nothing to route.
func (m *vpnManager) ensureRules() error {
	if _, ok := m.engine.(vpn.InterfaceEngine); !ok || m.router == nil {
		return nil
	}
	return m.router.Ensure()
}

func (m *vpnManager) unroute() {
	if m.router != nil {
		_ = m.router.Down()
	}
}

// Restore brings back what a restart loses: the rules (so traffic marked for
// the tunnel is refused rather than leaking while nothing is up) and the node
// that was active. Without it the tunnel silently stayed down after any
// restart of the service, including the one procd makes after a network
// reload (D-107).
//
// The rules go in now; the node comes up in the background, retrying until it
// does: at boot the server's name may not resolve yet, and the panel should
// not wait for it to start serving.
func (m *vpnManager) Restore() error {
	var err error
	if _, ok := m.engine.(vpn.InterfaceEngine); ok && m.router != nil {
		if e := m.router.Ensure(); e != nil {
			err = fmt.Errorf("openwrt: tunnel rules: %w", e)
		}
	}
	doc, lerr := m.store.Load()
	if lerr != nil {
		return errors.Join(err, lerr)
	}
	if id := doc.Settings.ActiveNodeID; id != "" {
		go m.bringBack(id)
	}
	return err
}

// bringBack activates id until it is up, or until somebody chose otherwise:
// another node activated, or this one removed or deactivated. The choice is
// checked under the same lock activation takes, so a node the person
// activates meanwhile is never torn down to bring back the old one.
func (m *vpnManager) bringBack(id string) {
	for {
		done, err := m.bringBackOnce(id)
		if done {
			return
		}
		log.Printf("tunnel: could not bring back node %s, retrying in %s: %v", id, m.retry, err)
		time.Sleep(m.retry)
	}
}

func (m *vpnManager) bringBackOnce(id string) (done bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, err := m.store.Load()
	if err != nil {
		return false, err
	}
	if m.up != "" || doc.Settings.ActiveNodeID != id {
		return true, nil
	}
	if err := m.activateLocked(id); err != nil {
		return false, err
	}
	return true, nil
}
