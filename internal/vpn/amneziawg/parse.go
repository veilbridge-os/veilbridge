// Package amneziawg implements the AmneziaWG VPN engine (amneziawg-go, MIT) and
// the parser for its .conf files. Two Engine implementations live here: a
// netstackEngine (userspace, Ubuntu/test) and a kernelEngine (OpenWrt). The OS
// adapter picks which one — the TUN type is an adapter detail. See DESIGN §5.
package amneziawg

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// obfuscationKeys are the AmneziaWG-specific [Interface] keys (lower-cased) that
// are NOT standard WireGuard. They are passed through to the engine opaquely.
var obfuscationKeys = map[string]bool{
	"jc": true, "jmin": true, "jmax": true,
	"s1": true, "s2": true, "s3": true, "s4": true,
	"h1": true, "h2": true, "h3": true, "h4": true,
}

// Parse reads an AmneziaWG .conf (INI: [Interface] + one [Peer]) and returns the
// public node plus its secret. Name is supplied by the caller (the .conf has no
// name); the node ID is derived from the peer public key so re-imports are stable.
//
// Grabли (lessons baked in): the parser keeps obfuscation keys (Jc/H1-H4) that
// Keenetic's UI hides — VeilBridge supports them as first-class (KEENETIC_REF §5).
func Parse(name string, raw []byte) (core.Node, config.NodeSecret, error) {
	var (
		sec       config.NodeSecret
		section   string
		endpoint  string
		keepalive int
		havePeer  bool
	)
	sec.Obfuscation = map[string]string{}

	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			if section == "peer" {
				havePeer = true
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return core.Node{}, config.NodeSecret{}, fmt.Errorf("amneziawg: malformed line %q", line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		switch section {
		case "interface":
			switch key {
			case "privatekey":
				sec.PrivateKey = val
			case "address":
				sec.Address = firstCSV(val)
			case "dns":
				sec.DNS = splitCSV(val)
			default:
				if obfuscationKeys[key] {
					sec.Obfuscation[key] = val
				}
				// Unknown [Interface] keys (e.g. ListenPort, MTU) are ignored
				// for v0.1; add as needed.
			}
		case "peer":
			switch key {
			case "publickey":
				sec.PeerPublicKey = val
			case "presharedkey":
				sec.PresharedKey = val
			case "endpoint":
				endpoint = val
			case "allowedips":
				sec.AllowedIPs = splitCSV(val)
			case "persistentkeepalive":
				keepalive = atoiDefault(val, 0)
			}
		}
	}
	sec.PersistentKeepalive = keepalive
	if err := sc.Err(); err != nil {
		return core.Node{}, config.NodeSecret{}, fmt.Errorf("amneziawg: read config: %w", err)
	}

	if sec.PrivateKey == "" {
		return core.Node{}, config.NodeSecret{}, fmt.Errorf("amneziawg: missing [Interface] PrivateKey")
	}
	if !havePeer || sec.PeerPublicKey == "" {
		return core.Node{}, config.NodeSecret{}, fmt.Errorf("amneziawg: missing [Peer] PublicKey")
	}
	if endpoint == "" {
		return core.Node{}, config.NodeSecret{}, fmt.Errorf("amneziawg: missing [Peer] Endpoint")
	}
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		return core.Node{}, config.NodeSecret{}, fmt.Errorf("amneziawg: bad endpoint %q: %w", endpoint, err)
	}

	node := core.Node{
		ID:       nodeID(sec.PeerPublicKey),
		Name:     name,
		Engine:   core.EngineAmneziaWG,
		Endpoint: endpoint,
	}
	return node, sec, nil
}

// ToNodeConfig builds the engine input from a stored secret and the node's
// endpoint. This is the bridge between persisted config and the live tunnel.
func ToNodeConfig(endpoint string, sec config.NodeSecret) vpn.NodeConfig {
	return vpn.NodeConfig{
		PrivateKey:          sec.PrivateKey,
		PeerPublicKey:       sec.PeerPublicKey,
		PresharedKey:        sec.PresharedKey,
		Endpoint:            endpoint,
		Address:             sec.Address,
		DNS:                 sec.DNS,
		AllowedIPs:          sec.AllowedIPs,
		PersistentKeepalive: sec.PersistentKeepalive,
		Obfuscation:         sec.Obfuscation,
	}
}

// nodeID derives a stable short ID from the peer public key.
func nodeID(peerPublicKey string) string {
	sum := sha256.Sum256([]byte(peerPublicKey))
	return hex.EncodeToString(sum[:8])
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstCSV(s string) string {
	if parts := splitCSV(s); len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// atoiDefault is a small helper for keepalive-style numeric fields.
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
