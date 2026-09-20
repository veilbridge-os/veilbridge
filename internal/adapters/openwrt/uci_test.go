package openwrt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// fakeRunner records the exact command line of every OS call and can script
// outputs and failures. Tests assert on the sequence, because with uci the
// order is the safety property, not a detail.
type fakeRunner struct {
	calls []string
	// out maps a command prefix to its stdout.
	out map[string][]byte
	// fail maps a command prefix to the error it should return.
	fail map[string]error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{out: map[string][]byte{}, fail: map[string]error{}}
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	for prefix, err := range f.fail {
		if strings.HasPrefix(line, prefix) {
			return nil, err
		}
	}
	for prefix, out := range f.out {
		if strings.HasPrefix(line, prefix) {
			return out, nil
		}
	}
	return []byte{}, nil
}

func (f *fakeRunner) sawPrefix(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newTestApplier(t *testing.T, f *fakeRunner) *uciApplier {
	t.Helper()
	return &uciApplier{
		run:           f.run,
		configRoot:    "/etc",
		configDir:     "config",
		snapshotDir:   t.TempDir(),
		keepSnapshots: 3,
		now:           func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
}

func TestSnapshotPacksTheConfigDirectory(t *testing.T) {
	f := newFakeRunner()
	f.out["tar czf -"] = []byte("PACKED")
	u := newTestApplier(t, f)

	snap, err := u.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if string(snap.Payload) != "PACKED" {
		t.Fatalf("payload = %q, want the archive bytes", snap.Payload)
	}
	if want := "tar czf - -C /etc config"; f.calls[0] != want {
		t.Fatalf("first call = %q, want %q", f.calls[0], want)
	}
	if snap.ID == "" {
		t.Fatal("snapshot without an id cannot be traced in logs")
	}
}

// An empty archive that reports success is the worst outcome for a safety net:
// the revert would "work" and restore nothing.
func TestSnapshotRefusesAnEmptyArchive(t *testing.T) {
	f := newFakeRunner()
	f.out["tar czf -"] = []byte{}
	u := newTestApplier(t, f)

	if _, err := u.Snapshot(); err == nil {
		t.Fatal("empty archive accepted as a valid snapshot")
	}
}

func TestSnapshotIsPersistedForLifeAfterARestart(t *testing.T) {
	f := newFakeRunner()
	f.out["tar czf -"] = []byte("PACKED")
	u := newTestApplier(t, f)

	snap, err := u.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(u.snapshotDir, snap.ID+".tar.gz"))
	if err != nil {
		t.Fatalf("snapshot not on disk: %v", err)
	}
	if string(data) != "PACKED" {
		t.Fatalf("stored payload = %q", data)
	}
}

// Overlay space on a router is tens of megabytes; an unbounded snapshot
// directory eventually breaks the very operation it exists for.
func TestOldSnapshotsArePruned(t *testing.T) {
	f := newFakeRunner()
	f.out["tar czf -"] = []byte("PACKED")
	u := newTestApplier(t, f)

	for i := range 6 {
		u.now = func() time.Time {
			return time.Unix(1700000000+int64(i)*60, 0).UTC()
		}
		if _, err := u.Snapshot(); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(u.snapshotDir)
	if err != nil {
		t.Fatalf("read snapshot dir: %v", err)
	}
	if len(entries) != u.keepSnapshots {
		t.Fatalf("kept %d snapshots, want %d", len(entries), u.keepSnapshots)
	}
}

func TestCommitAppliesAndReloads(t *testing.T) {
	f := newFakeRunner()
	u := newTestApplier(t, f)

	if err := u.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	want := []string{"uci commit", "/sbin/reload_config"}
	if len(f.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, f.calls[i], want[i])
		}
	}
}

// A commit that writes files but never tells the services is a change that
// "worked" and did nothing until the next reboot.
func TestCommitFailsLoudlyWhenReloadFails(t *testing.T) {
	f := newFakeRunner()
	f.fail["/sbin/reload_config"] = errors.New("ubus timeout")
	u := newTestApplier(t, f)

	if err := u.Commit(); err == nil {
		t.Fatal("commit reported success although services were never reloaded")
	}
}

// Staged uci changes live in /tmp/.uci and survive a file-level restore, so the
// next commit would quietly re-apply exactly what was just undone.
func TestRevertDropsStagedChangesBeforeRestoring(t *testing.T) {
	f := newFakeRunner()
	f.out["uci changes"] = []byte("network.lan.ipaddr='192.168.9.1'\n-firewall.@zone[1]\n")
	u := newTestApplier(t, f)

	err := u.Revert(core.Snapshot{ID: "uci-test", Payload: []byte("PACKED")})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}

	idxChanges, idxFirewall, idxNetwork, idxUnpack := -1, -1, -1, -1
	for i, c := range f.calls {
		switch {
		case c == "uci changes":
			idxChanges = i
		case c == "uci revert firewall":
			idxFirewall = i
		case c == "uci revert network":
			idxNetwork = i
		case strings.HasPrefix(c, "tar xzf"):
			idxUnpack = i
		}
	}
	if idxChanges < 0 || idxFirewall < 0 || idxNetwork < 0 || idxUnpack < 0 {
		t.Fatalf("missing steps in %v", f.calls)
	}
	if idxFirewall > idxUnpack || idxNetwork > idxUnpack {
		t.Fatalf("staged changes dropped after unpacking, order: %v", f.calls)
	}
	if !f.sawPrefix("/sbin/reload_config") {
		t.Fatal("configuration restored but services never reloaded")
	}
}

func TestRevertUnpacksIntoTheConfigRoot(t *testing.T) {
	f := newFakeRunner()
	u := newTestApplier(t, f)

	if err := u.Revert(core.Snapshot{ID: "uci-test", Payload: []byte("PACKED")}); err != nil {
		t.Fatalf("revert: %v", err)
	}
	var unpack string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "tar xzf") {
			unpack = c
		}
	}
	if !strings.HasSuffix(unpack, "-C /etc") {
		t.Fatalf("unpack call = %q, want it to target /etc", unpack)
	}
}

func TestRevertRefusesAnEmptySnapshot(t *testing.T) {
	f := newFakeRunner()
	u := newTestApplier(t, f)

	if err := u.Revert(core.Snapshot{ID: "uci-empty"}); err == nil {
		t.Fatal("revert accepted a snapshot with no data")
	}
	if len(f.calls) != 0 {
		t.Fatalf("an empty snapshot still touched the system: %v", f.calls)
	}
}

func TestStagedPackagesParsesUCIChangesOutput(t *testing.T) {
	out := "network.lan.ipaddr='192.168.9.1'\n" +
		"-firewall.@zone[1]\n" +
		"network.wan.proto='dhcp'\n" +
		"\n" +
		"dhcp.lan.start='100'\n"
	got := stagedPackages(out)
	want := []string{"dhcp", "firewall", "network"}
	if len(got) != len(want) {
		t.Fatalf("packages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("packages = %v, want %v", got, want)
		}
	}
}
