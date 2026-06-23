package amneziawg_test

import (
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn/amneziawg"
)

// A realistic AmneziaWG client .conf — obfuscation keys (Jc/Jmin/Jmax/S1/S2/
// H1-H4) live in [Interface], the format the panels (NL/FI nodes) emit.
const sampleConf = `[Interface]
PrivateKey = aGVsbG9wcml2YXRla2V5ZXhhbXBsZWJhc2U2NHN0cmluZw==
Address = 10.8.1.2/32
DNS = 1.1.1.1, 1.0.0.1
Jc = 5
Jmin = 50
Jmax = 1000
S1 = 37
S2 = 86
H1 = 1111111111
H2 = 2222222222
H3 = 3333333333
H4 = 4444444444

[Peer]
PublicKey = c2VydmVycHVibGlja2V5ZXhhbXBsZWJhc2U2NHN0cmluZ2hlcmU=
PresharedKey = cHJlc2hhcmVka2V5ZXhhbXBsZWJhc2U2NHN0cmluZ3ZhbHVl
Endpoint = 203.0.113.10:443
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 15
`

func TestParse(t *testing.T) {
	node, sec, err := amneziawg.Parse("NetherlandsAWG", []byte(sampleConf))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if node.Name != "NetherlandsAWG" || node.Engine != core.EngineAmneziaWG {
		t.Errorf("node meta wrong: %+v", node)
	}
	if node.Endpoint != "203.0.113.10:443" {
		t.Errorf("endpoint = %q", node.Endpoint)
	}
	if node.ID == "" {
		t.Error("node ID should be derived, got empty")
	}

	if sec.Address != "10.8.1.2/32" {
		t.Errorf("address = %q", sec.Address)
	}
	if len(sec.DNS) != 2 || sec.DNS[0] != "1.1.1.1" {
		t.Errorf("dns = %v", sec.DNS)
	}
	if len(sec.AllowedIPs) != 1 || sec.AllowedIPs[0] != "0.0.0.0/0" {
		t.Errorf("allowedIPs = %v", sec.AllowedIPs)
	}
	if sec.PresharedKey == "" {
		t.Error("preshared key not parsed")
	}
	if sec.PersistentKeepalive != 15 {
		t.Errorf("keepalive = %d, want 15", sec.PersistentKeepalive)
	}
	// Obfuscation keys must survive — VeilBridge supports what Keenetic hides.
	for k, want := range map[string]string{"jc": "5", "jmin": "50", "jmax": "1000", "s1": "37", "h1": "1111111111", "h4": "4444444444"} {
		if sec.Obfuscation[k] != want {
			t.Errorf("obfuscation[%q] = %q, want %q", k, sec.Obfuscation[k], want)
		}
	}
}

// TestParseStableID: same peer key → same node ID (re-import idempotency).
func TestParseStableID(t *testing.T) {
	a, _, _ := amneziawg.Parse("x", []byte(sampleConf))
	b, _, _ := amneziawg.Parse("y", []byte(sampleConf))
	if a.ID != b.ID {
		t.Errorf("same peer key gave different IDs: %q vs %q", a.ID, b.ID)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"no private key": "[Peer]\nPublicKey = x\nEndpoint = h:1\n",
		"no peer key":    "[Interface]\nPrivateKey = x\n[Peer]\nEndpoint = h:1\n",
		"no endpoint":    "[Interface]\nPrivateKey = x\n[Peer]\nPublicKey = y\n",
		"bad endpoint":   "[Interface]\nPrivateKey = x\n[Peer]\nPublicKey = y\nEndpoint = noport\n",
		"malformed line": "[Interface]\nthis has no equals sign\n",
	}
	for name, conf := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := amneziawg.Parse("n", []byte(conf)); err == nil {
				t.Errorf("expected error for %q, got nil", name)
			}
		})
	}
}

// TestToNodeConfig: stored secret → engine input round-trips through the parser.
func TestToNodeConfig(t *testing.T) {
	node, sec, err := amneziawg.Parse("n", []byte(sampleConf))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := amneziawg.ToNodeConfig(node.Endpoint, sec)
	if cfg.Endpoint != "203.0.113.10:443" || cfg.Address != "10.8.1.2/32" {
		t.Errorf("config wrong: %+v", cfg)
	}
	if cfg.PersistentKeepalive != 15 || cfg.Obfuscation["jc"] != "5" {
		t.Errorf("config missing fields: %+v", cfg)
	}
}
