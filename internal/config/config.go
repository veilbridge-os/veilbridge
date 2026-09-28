// Package config holds VeilBridge's persisted state: the single JSON document
// that is the source of truth for nodes, routing rules, and settings, plus a
// Store that loads and saves it atomically. See the architecture notes in CONTRIBUTING.md.
//
// Secrets (node private keys, peer keys, obfuscation params, the admin password
// hash) live HERE — never in core.Node, which crosses the API boundary. The
// config document is exported/imported as-is for backup/restore (FR-8).
package config

import (
	"errors"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Version is the current config schema version.
const Version = "0.1.0"

// NodeSecret carries the engine parameters needed to bring a tunnel up. These
// never leave the agent through the public API; only the derived core.Node does.
// For AmneziaWG these are UAPI keys (see DESIGN §2, §5.2).
type NodeSecret struct {
	// PrivateKey is this peer's WireGuard private key (base64).
	PrivateKey string `json:"privateKey"`
	// PeerPublicKey is the server's public key (base64).
	PeerPublicKey string `json:"peerPublicKey"`
	// PresharedKey is optional (base64).
	PresharedKey string `json:"presharedKey,omitempty"`
	// Address is the tunnel-internal address, e.g. "10.8.1.2/32".
	Address string `json:"address"`
	// DNS servers to use while the tunnel is up.
	DNS []string `json:"dns,omitempty"`
	// AllowedIPs routed into the tunnel (e.g. "0.0.0.0/0").
	AllowedIPs []string `json:"allowedIPs,omitempty"`
	// PersistentKeepalive in seconds (0 = off).
	PersistentKeepalive int `json:"persistentKeepalive,omitempty"`
	// Obfuscation holds AmneziaWG params: jc, jmin, jmax, s1-s4, h1-h4.
	// Kept as a map so the parser and engine pass them through opaquely.
	Obfuscation map[string]string `json:"obfuscation,omitempty"`
}

// StoredNode is a node plus its secret, as persisted. The public core.Node is
// derived via Public().
type StoredNode struct {
	core.Node
	Secret NodeSecret `json:"secret"`
}

// Public returns the API-safe view of the node (no secrets).
func (n StoredNode) Public() core.Node { return n.Node }

// Settings holds agent-wide configuration.
type Settings struct {
	// PasswordHash is the bcrypt hash of the admin password (D-6).
	PasswordHash string `json:"passwordHash,omitempty"`
	// ActiveNodeID is the node currently selected as egress, "" if none.
	ActiveNodeID string `json:"activeNodeID,omitempty"`
	// ListenAddr for the HTTP API, e.g. ":8080".
	ListenAddr string `json:"listenAddr,omitempty"`
}

// Document is the whole persisted config. It is the backup/restore unit (FR-8).
type Document struct {
	Version  string           `json:"version"`
	Nodes    []StoredNode     `json:"nodes"`
	Routes   []core.RouteRule `json:"routes"`
	Settings Settings         `json:"settings"`
	// Devices is what the owner said about devices on the local network: a
	// name, or "I know this one" (M4, #51, D-86). Written only when the owner
	// does something, never because a device appeared — so the list costs
	// the flash nothing while phones come and go.
	Devices []core.DeviceNote `json:"devices,omitempty"`
}

// MaxDeviceNotes bounds the list: phones that change their private address
// every day would otherwise grow it for ever, one name at a time.
const MaxDeviceNotes = 1024

// ErrTooManyDevices is the refusal when the list is full.
var ErrTooManyDevices = errors.New("the panel remembers at most 1024 devices — forget some you no longer have")

// SetDeviceName names a device; an empty name removes the name but leaves
// the device known, because the owner has looked at it.
func (d *Document) SetDeviceName(mac, name string) error {
	n := d.deviceNote(mac)
	if n == nil {
		if len(d.Devices) >= MaxDeviceNotes {
			return ErrTooManyDevices
		}
		d.Devices = append(d.Devices, core.DeviceNote{MAC: mac})
		n = &d.Devices[len(d.Devices)-1]
	}
	n.Name = name
	n.Known = true
	return nil
}

// MarkDevicesKnown takes the "new" mark off every device in macs at once.
func (d *Document) MarkDevicesKnown(macs []string) error {
	for _, mac := range macs {
		if n := d.deviceNote(mac); n != nil {
			n.Known = true
			continue
		}
		if len(d.Devices) >= MaxDeviceNotes {
			return ErrTooManyDevices
		}
		d.Devices = append(d.Devices, core.DeviceNote{MAC: mac, Known: true})
	}
	return nil
}

// ForgetDevice drops everything the panel remembers about a device. It
// reports whether there was anything to drop.
func (d *Document) ForgetDevice(mac string) bool {
	for i := range d.Devices {
		if d.Devices[i].MAC == mac {
			d.Devices = append(d.Devices[:i], d.Devices[i+1:]...)
			return true
		}
	}
	return false
}

func (d *Document) deviceNote(mac string) *core.DeviceNote {
	for i := range d.Devices {
		if d.Devices[i].MAC == mac {
			return &d.Devices[i]
		}
	}
	return nil
}

// Default returns an empty document with the current version and sane defaults.
func Default() *Document {
	return &Document{
		Version:  Version,
		Nodes:    []StoredNode{},
		Routes:   []core.RouteRule{},
		Settings: Settings{ListenAddr: ":8080"},
	}
}

// PublicNodes returns the API-safe view of all nodes.
func (d *Document) PublicNodes() []core.Node {
	out := make([]core.Node, len(d.Nodes))
	for i, n := range d.Nodes {
		out[i] = n.Public()
	}
	return out
}
