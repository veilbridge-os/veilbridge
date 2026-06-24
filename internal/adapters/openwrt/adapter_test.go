package openwrt_test

import (
	"path/filepath"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// TestPlatformLabel: the OpenWrt adapter must report "openwrt" so the dashboard
// shows the truth (and proves NewWithEngine threaded the label through).
func TestPlatformLabel(t *testing.T) {
	a := openwrt.New(filepath.Join(t.TempDir(), "config.json"))
	if got := a.Platform(); got != "openwrt" {
		t.Errorf("Platform() = %q, want %q", got, "openwrt")
	}
}

// TestSatisfiesAdapter: structural proof of interchangeability (NFR-1) — the
// OpenWrt adapter is a core.Adapter, same as Ubuntu, exposing all managers.
// (Info() is not exercised here: it makes a real WAN egress HTTP call, which
// belongs in the on-VM e2e, not a unit test.)
func TestSatisfiesAdapter(t *testing.T) {
	var a core.Adapter = openwrt.New(filepath.Join(t.TempDir(), "config.json"))
	if a.VPN() == nil || a.Routing() == nil || a.System() == nil {
		t.Fatal("adapter is missing a manager")
	}
}
