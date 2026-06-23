package amneziawg

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// key32 returns a valid 32-byte base64 key for tests.
func key32(fill byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = fill
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestUAPIConfig(t *testing.T) {
	cfg := vpn.NodeConfig{
		PrivateKey:          key32(1),
		PeerPublicKey:       key32(2),
		PresharedKey:        key32(3),
		Endpoint:            "203.0.113.10:443",
		Address:             "10.8.1.2/32",
		PersistentKeepalive: 15,
		AllowedIPs:          []string{"0.0.0.0/0"},
		Obfuscation:         map[string]string{"jc": "5", "h1": "111", "s1": "37"},
	}
	out, err := uapiConfig(cfg)
	if err != nil {
		t.Fatalf("uapiConfig: %v", err)
	}
	// Keys must be hex, lowercase, 64 chars.
	if !strings.Contains(out, "private_key=01010101") {
		t.Errorf("private_key not hex-encoded:\n%s", out)
	}
	if !strings.Contains(out, "public_key=02020202") {
		t.Errorf("public_key missing:\n%s", out)
	}
	if !strings.Contains(out, "preshared_key=03030303") {
		t.Errorf("preshared_key missing:\n%s", out)
	}
	for _, want := range []string{"jc=5", "h1=111", "s1=37", "endpoint=203.0.113.10:443", "persistent_keepalive_interval=15", "allowed_ip=0.0.0.0/0"} {
		if !strings.Contains(out, want) {
			t.Errorf("uapi missing %q:\n%s", want, out)
		}
	}
}

func TestUAPIConfigDefaultsAllowedIPs(t *testing.T) {
	out, err := uapiConfig(vpn.NodeConfig{
		PrivateKey:    key32(1),
		PeerPublicKey: key32(2),
		Endpoint:      "1.2.3.4:443",
	})
	if err != nil {
		t.Fatalf("uapiConfig: %v", err)
	}
	if !strings.Contains(out, "allowed_ip=0.0.0.0/0") || !strings.Contains(out, "allowed_ip=::/0") {
		t.Errorf("expected default allowed_ips, got:\n%s", out)
	}
}

func TestB64ToHexRejectsBadKey(t *testing.T) {
	if _, err := b64ToHex("not-base64!!"); err == nil {
		t.Error("expected error for bad base64")
	}
	if _, err := b64ToHex(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Error("expected error for non-32-byte key")
	}
}

func TestParseStats(t *testing.T) {
	// last_handshake_time_sec=0 → never (-1).
	never := parseStats("public_key=ab\nlast_handshake_time_sec=0\nrx_bytes=0\ntx_bytes=0\n")
	if never.HandshakeAgeSec != -1 {
		t.Errorf("no handshake should be -1, got %d", never.HandshakeAgeSec)
	}
	// A recent handshake → small non-negative age, and rx/tx parsed.
	recent := parseStats("last_handshake_time_sec=" + strconv.FormatInt(nowUnix()-3, 10) + "\nrx_bytes=4096\ntx_bytes=2048\n")
	if recent.HandshakeAgeSec < 0 || recent.HandshakeAgeSec > 60 {
		t.Errorf("recent handshake age out of range: %d", recent.HandshakeAgeSec)
	}
	if recent.RxBytes != 4096 || recent.TxBytes != 2048 {
		t.Errorf("rx/tx wrong: rx=%d tx=%d", recent.RxBytes, recent.TxBytes)
	}
}
