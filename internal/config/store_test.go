package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

func tmpStore(t *testing.T) *config.Store {
	t.Helper()
	return config.NewStore(filepath.Join(t.TempDir(), "config.json"))
}

// TestLoadMissingReturnsDefault: first run is not an error.
func TestLoadMissingReturnsDefault(t *testing.T) {
	d, err := tmpStore(t).Load()
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if d.Version != config.Version {
		t.Errorf("version = %q, want %q", d.Version, config.Version)
	}
	if d.Settings.ListenAddr != ":8080" {
		t.Errorf("default listen addr = %q", d.Settings.ListenAddr)
	}
}

// TestLoadEmptyReturnsDefault: an empty or whitespace-only file is treated as
// first run, not corruption. A zero-byte config (e.g. a truncated write, or an
// external `touch`) must not wedge the daemon — same outcome as a missing file.
func TestLoadEmptyReturnsDefault(t *testing.T) {
	for _, body := range []string{"", "   \n\t  "} {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write empty: %v", err)
		}
		d, err := config.NewStore(path).Load()
		if err != nil {
			t.Fatalf("load empty %q: %v", body, err)
		}
		if d.Version != config.Version || d.Settings.ListenAddr != ":8080" {
			t.Errorf("empty file did not yield Default(): %+v", d.Settings)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := tmpStore(t)
	in := config.Default()
	in.Nodes = append(in.Nodes, config.StoredNode{
		Node: core.Node{ID: "n1", Name: "NL", Engine: core.EngineAmneziaWG, Endpoint: "203.0.113.10:443"},
		Secret: config.NodeSecret{
			PrivateKey:    "priv",
			PeerPublicKey: "pub",
			Address:       "10.8.1.2/32",
			Obfuscation:   map[string]string{"jc": "5", "h1": "1"},
		},
	})
	in.Routes = append(in.Routes, core.RouteRule{ID: "r1", Kind: core.RuleDomain, Value: "youtube.com", Target: core.TargetTunnel})
	in.Settings.ActiveNodeID = "n1"

	if err := s.Save(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out.Nodes) != 1 || out.Nodes[0].Secret.Obfuscation["jc"] != "5" {
		t.Errorf("node/secret not round-tripped: %+v", out.Nodes)
	}
	if out.Nodes[0].Public().ID != "n1" {
		t.Errorf("public node wrong: %+v", out.Nodes[0].Public())
	}
	if len(out.Routes) != 1 || out.Routes[0].Value != "youtube.com" {
		t.Errorf("routes not round-tripped: %+v", out.Routes)
	}
	if out.Settings.ActiveNodeID != "n1" {
		t.Errorf("settings not round-tripped: %+v", out.Settings)
	}
}

// TestPublicNodesHidesSecrets: the API-safe view must not leak secrets.
func TestPublicNodesHidesSecrets(t *testing.T) {
	d := config.Default()
	d.Nodes = append(d.Nodes, config.StoredNode{
		Node:   core.Node{ID: "n1"},
		Secret: config.NodeSecret{PrivateKey: "TOPSECRET"},
	})
	pub := d.PublicNodes()
	if len(pub) != 1 || pub[0].ID != "n1" {
		t.Fatalf("public nodes wrong: %+v", pub)
	}
	// core.Node has no secret field at all — this is a structural guarantee,
	// asserted here so a future field addition that leaks secrets breaks a test.
	if got := pub[0]; got != (core.Node{ID: "n1"}) {
		t.Errorf("public node carries unexpected data: %+v", got)
	}
}

func TestPassword(t *testing.T) {
	d := config.Default()
	if d.VerifyPassword("anything") {
		t.Error("verify should be false before a password is set")
	}
	if err := d.SetPassword("hunter2"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if !d.VerifyPassword("hunter2") {
		t.Error("correct password should verify")
	}
	if d.VerifyPassword("wrong") {
		t.Error("wrong password should not verify")
	}
	if err := d.SetPassword(""); err == nil {
		t.Error("empty password should error")
	}
}

// TestAtomicWriteLeavesNoTemp: after Save, the dir holds only the config file
// (the temp file was renamed/cleaned, never left behind).
func TestAtomicWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	s := config.NewStore(filepath.Join(dir, "config.json"))
	if err := s.Save(config.Default()); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only config.json, got %v", names)
	}
}

// TestSaveDoesNotCorruptOnReload: a second Save over an existing file yields a
// valid, fully-readable document (the rename replaces atomically).
func TestSaveDoesNotCorruptOnReload(t *testing.T) {
	s := tmpStore(t)
	if err := s.Save(config.Default()); err != nil {
		t.Fatalf("first save: %v", err)
	}
	second := config.Default()
	_ = second.SetPassword("pw")
	if err := s.Save(second); err != nil {
		t.Fatalf("second save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !out.VerifyPassword("pw") {
		t.Error("second save not durable")
	}
}
